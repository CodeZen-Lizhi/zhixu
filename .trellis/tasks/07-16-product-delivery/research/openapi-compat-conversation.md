# Conversation OpenAPI 兼容修复交接（2026-09-09）

## 实施结果

固定比较基线仍为 `a4c16248ce1082ce500aea2a99d640e4e0195ddf`。本代理仅修改 Conversation 兼容边界与对应测试；没有修改 OpenAPI、生成客户端、Web、公共鉴权/Router、`cmd/api`、数据库 Schema 或用户数据库，也未提交或推送。

`Handler.Routes` 继续提供 v1；`Handler.RoutesV2` 只增加以下四条新版本接口：

- `POST /api/v2/conversations/:conversation_id/questions`
- `GET /api/v2/conversations/:conversation_id/turns`
- `GET /api/v2/answers/:answer_id`
- `GET /api/v2/answers/:answer_id/analysis-timeline`

每个路由组绑定独立的 Handler 值副本，HTTP 版本不会成为共享可变状态。`status_url` 跟随调用版本；Workflow、Source/Citation、Feedback 和 SSE 等合同未变的链接仍使用 v1。

## 版本与身份边界

| 持久事实 | HTTP v1 | HTTP v2 |
| --- | --- | --- |
| 固定 RAG Workflow v1/v2 | 兼容 | 兼容 |
| Workspace Analysis Workflow v1 | 保留历史响应及精确重放 | 可读、可精确重放 |
| Workspace Analysis Workflow v2 | 稳定 409，列表查询排除 | 返回完整动态能力 |

HTTP 在命令/查询中传入显式 `APIVersion`，不把版本加入 Question 的 canonical request、幂等键或 request hash。内部未限定版本的调用仍允许零值。

生产组合继续为新分析选择 `workspace-analysis@2`。**通过 v1 发起新的 Workspace Analysis，在任何新 ID 分配、Question/Answer/Workflow/Job/Analysis Run 写入和通知之前，返回不可重试的 `409 CONVERSATION_API_VERSION_UNSUPPORTED`，提示改用 `/api/v2`。** 本次没有重建一套 v1 runtime。旧版 dispatcher 仍能按其可信配置创建 v1 分析。

dispatcher 先在 Workspace 范围锁定 Conversation 并查原幂等键：

- 原键且业务请求不同：保留原 `CONVERSATION_QUESTION_IDEMPOTENCY_CONFLICT`。
- 原键且业务请求相同：按既有 Workflow Definition key/version/graph 重放；v1 重放 v2 在 `runtime.StartScoped`、Analysis Run starter 与 ID generator 之前拒绝。
- 历史 v1 精确重放：当前 v2 dispatcher 仍返回原 Question、Answer、Workflow、Job 与 request hash，不重新生成业务事实。
- 新 v1 RAG：继续正常创建。

版本依据来自持久 Workflow Definition key/version，属于内部 metadata，不添加到公共 JSON。不能仅依赖 Answer result 的 `schema_version`：动态分析 pending 尚无 result，refused/failed/cancelled 也可能使用原 v1 envelope。

GetAnswer 在 ETag/304 判断之前拒绝不兼容的 Definition。Timeline 在只读 RepeatableRead 事务内读取 Analysis Run 的 definition_version 后、读取 items 之前检查版本。Workspace 隔离优先于版本披露，跨 Workspace 和不存在资源仍返回一致的 404。

## 列表与游标

Turn 的版本过滤进入持久 SQL，在 keyset、LIMIT 和 latest 选择之前执行；HTTP/Application 只做结果防御检查，不能静默丢弃新版本行。混合序列 `RAG@2, Analysis@2 cancelled, Analysis@1 cancelled, Analysis@2 pending` 在 v1 中分页得到 ordinal `1, 3`，latest 为 `3`；v2 仍得到 `1, 2, 3, 4`，latest 为 `4`。数据库查询保持有界，测试验证 v1 每页至多两次查询。

LEFT JOIN 的缺失 Definition/Answer 行通过 `d.id IS NULL` 保留，让原 scanner 报一致性错误，避免版本过滤隐藏损坏事实。原缺失 Answer slot 测试已扩展为内部/v1/v2 × 普通分页/latest。

Turn cursor 的 `schema_version` 绑定 HTTP 版本：v1 仍为 1 且编码字节不变，v2 为 2。跨版本 cursor 返回 `400 CONVERSATION_CURSOR_INVALID`，不会调用业务服务。Conversation 列表的 cursor 合同不变。

## 本次涉及文件

- Application：新增 `api_version.go`、`api_version_test.go`；修改 `repository.go`、`service.go`、`workspace_analysis_timeline.go`。
- PostgreSQL：修改 `dispatch_contract.go`、`gorm_dispatch.go`、`turns_contract.go`、`scans.go`、`gorm_turns.go`、`gorm_feedback.go`、`gorm_finalizer.go`、`gorm_workspace_analysis_terminal_hook.go`、`gorm_workspace_analysis_timeline.go`；新增 `api_version_integration_test.go`，扩展 `turns_integration_test.go`。
- HTTP：修改 `handler.go`、`cursor.go`、`handler_test.go`、`cursor_test.go`；新增 `api_version_test.go`。
- 当前工作树还有其他先前的 Conversation v2 改动；不能把整个目录的 Git diff 都归为本增量。

## 验证证据

以下均已执行通过：

```sh
env GIN_MODE=test go test -mod=vendor ./internal/conversation/... -count=1 -timeout=60s
env GIN_MODE=test go test -mod=vendor -race ./internal/conversation/... -count=1 -timeout=60s
go vet -mod=vendor ./internal/conversation/...
git diff --check -- internal/conversation
```

本次所有修改的 Go 文件 `gofmt -l` 无输出。

PostgreSQL 只通过隔离 Testcontainers 运行，并显式移除外部数据库环境变量：

```sh
env -u ZHIXU_TEST_DATABASE_URL GIN_MODE=test \
  go test -mod=vendor -tags=integration,testcontainers \
  ./internal/conversation/adapter/postgres \
  -run '^(TestConversationAPIVersionsKeepDispatchIdentityAndFilterMixedTurns|TestRepositoryListTurnsRejectsQuestionWithoutAnswerSlot|TestRepositoryReadsTurnsAnswersAndPublishedContextWithoutCrossWorkspaceLeak|TestRepositoryRecordsFeedbackWithExactReplayAndImmutableProductFacts|TestAnswerFinalizerAtomicallyPublishesRefusalAndExactlyReplaysConcurrently|TestRepositoryLoadsBoundedQuestionExecutionContext|TestWorkspaceAnalysisCancellationTerminalHookDirectRuntimeCancelClosesPublicationAndReplays)$' \
  -count=1 -timeout=180s
```

结果：`ok github.com/CodeZen-Lizhi/zhixu/internal/conversation/adapter/postgres 55.690s`。

新增集成用例使用真实 dispatcher、Workflow/River/Events/Audit 和 cancellation hook，验证混合分页、pending/terminal Definition、历史 replay、v1 RAG、跨 Workspace 隔离。拒绝路径同时检查 runtime/analysis/ID generator 零调用，以及 Question/Answer/Workflow/Job/Analysis Run/Event/Conversation version 未改变。

HTTP 测试覆盖 HTTP v1/v2 × Definition v1/v2 × pending/completed/refused/failed/cancelled，包含匹配 If-None-Match 时仍拒绝 v1 读取动态事实；严格 v1 字段、v2 `git_status:null` 与真实预算、重放投影、versioned status URL、共享 v1 链接和游标范围。

首次集成测试只有新测试断言错误：刚创建即取消的 v1 timeline 只有已持久的 `inspect_workspace` 节点，不会预先产生六个节点。断言已改为真实旧行为及原预算，重跑通过。Go 自检核对了全部公共 Answer scanner 的 Definition JOIN，并修复上述 NULL 行被过滤的边界。

## 后续整合

Conversation 分工内没有已知未处理问题。原基线 OpenAPI 门禁、公共路由鉴权、客户端/Web 和独立 Trellis 检查由主会话整合。发布说明应明确 v1 新 Workspace Analysis 的 409 行为，并与 Web 切换到 v2 同步；本增量不授权部署。
