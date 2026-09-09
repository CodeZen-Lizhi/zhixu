# TODO2 v2 公共 Answer / Timeline 合同验证

日期：2026-09-09。Owner：`dynamic_contract`。本记录仅覆盖 Conversation 的 Go 公共合同、时间线读取及相关测试；不是 TODO2 整体完成、真实 Provider 质量或部署成功证明。

后续主会话追加的 Finalizer、终态 Hook 与取消审计实现及实库回归见 [Finalizer 验证记录](todo2-v2-finalizer-verification.md)。本页“尚待集成”和未修改范围保留初次公共合同交付时的状态，后续结论以该记录及对应 owner 的最终报告为准。

## 已实现范围

- 成功结果新增独立 `WorkspaceAnalysisAnswerResultV2`、Payload 与 Budget Summary；按照持久 `schema_version` 严格分派。`git_status` 必须存在，可为 null；拒答及失败终止仍沿用原来的 v1 结果 Schema。
- Answer 绑定校验核对成功结果的 Model Run、Workspace 和 Citation 身份，拒绝重复 Citation tuple、Model Run 与 Evidence 身份复用。
- Timeline 保留既有 Application / HTTP 接口，新增独立 v2 validator。动态前缀为决策与工具交替，可重复 Search / Read、跳过 Git、在 loop 内执行 Citation 校验；候选之后仍要求独立的最终 Citation 校验和 Review。
- v2 查询直接投影 `agent.workspace_analysis_journal.sequence`，不按 phase 重排、不重新编号、不追加 Workflow node 占位。操作与 journal 使用 LEFT JOIN，使缺失 journal、序号间隙和超限不能被静默隐藏。
- Run、操作、预算与事件序号在同池 repeatable-read / read-only UoW 中读取。预算来自 reservation / settlement；pending 是尚未授权的槽，不计已调用次数。Unknown 不带成功摘要，也不允许后续继续。
- 摘要只返回允许的计数、短引用、Hash 与错误；不读取或公开模型 Prompt、决策参数、来源正文或私有 Evidence binding。

新增实现与测试：

- `internal/conversation/domain/workspace_analysis_result_v2.go`
- `internal/conversation/domain/workspace_analysis_timeline_v2.go`
- `internal/conversation/domain/workspace_analysis_result_v2_test.go`
- `internal/conversation/domain/workspace_analysis_timeline_v2_test.go`
- `internal/conversation/adapter/postgres/workspace_analysis_timeline_v2_contract.go`
- `internal/conversation/adapter/postgres/workspace_analysis_timeline_v2_contract_test.go`

修改现有接入点：`domain/answer.go`、`domain/workspace_analysis_result.go`、`domain/workspace_analysis_timeline.go`、`adapter/postgres/gorm_workspace_analysis_timeline.go`、`adapter/postgres/workspace_analysis_timeline_contract.go`、`application/workspace_analysis_timeline_test.go`、`http/handler_test.go`。

## 跨 owner 合同

数值以 `internal/agent/domain/workspace_analysis_v2_policy.go` 为唯一来源：最多 12 次决策、14 次模型调用、13 次工具调用、8 次 Source 读取、32 个 Run-global Evidence 引用；Timeline 最多 27 项。每次模型输入预留上限 65536，Run 输入上限 917504，Run 输出上限 11264。持久 Run 的输出额度允许在 7169–11264 范围缩小。

成功路径没有 repair、额外 planner 或自动重试，因此 `model_calls = tool_calls + 2`；成功 Token 汇总同时受实际模型调用数量对应的上限限制。E1–E32 在同一 Run 中不得改绑其他 Hash。ReadSource@4 公开 output 的 `evidence_ref` 已是 global ref，私有 `search_evidence_ref` 不进入 Timeline。

精确工具为 `ReadGitStatus@3`、`SearchKnowledge@3`、`ReadSource@4`、`ValidateCitation@4`。loop 工具与选择它的 decision 共用 ordinal。`CITATION_VALIDATION` 既可属于 `decide_next`，也可属于独立 `validate_citations`；公开 validator 根据实际序列区分两者。第 13 个 decision 只可作为 pending 预算拒绝槽，不能获得 ModelCall。

Call 与 Operation 状态严格核对，仅保留已有 owner 的明确例外：ModelCall SUCCEEDED 配 Operation FAILED / MODEL_REFUSED；ToolCall SUCCEEDED 配 Operation FAILED / RECEIPT_INVALID。不能用这些例外放过其他状态漂移。

`public_ui` 已于本轮确认收到上述最终规则，正在按 Go v2 DTO / validator 同步 OpenAPI 与 Web；该确认不是其测试通过证据。

## 已执行验证

以下定向测试通过：

```bash
go test -mod=vendor \
  ./internal/conversation/domain \
  ./internal/conversation/application \
  ./internal/conversation/http \
  ./internal/conversation/adapter/postgres \
  -run 'TestWorkspaceAnalysis|TestAnswer|TestServiceGetWorkspaceAnalysisTimeline|TestGetWorkspaceAnalysisTimeline' \
  -count=1 -timeout=60s

go test -mod=vendor -race \
  ./internal/conversation/domain \
  ./internal/conversation/application \
  ./internal/conversation/http \
  ./internal/conversation/adapter/postgres \
  -run 'TestWorkspaceAnalysis|TestAnswer|TestServiceGetWorkspaceAnalysisTimeline|TestGetWorkspaceAnalysisTimeline' \
  -count=1 -timeout=60s

go vet -mod=vendor \
  ./internal/conversation/domain \
  ./internal/conversation/application \
  ./internal/conversation/http \
  ./internal/conversation/adapter/postgres
```

最后一次 race 的四包结果分别为 1.454s / 1.358s / 1.523s / 1.530s，退出码 0。上述命令未带 `-tags=integration`，不会执行真实 PostgreSQL integration tests。

覆盖重复检索与阅读的实际顺序、可选 Git、loop Citation 与最终校验、未知状态、pending 预算拒绝、严格 nullable 字段、混合版本、Call namespace / 状态、Token / 调用账本、Source 全局引用及 Answer 身份绑定。新增文件已 gofmt；上述 owned tracked files 的 `git diff --check` 通过。已按 `go-review` 核对 SQL 参数化、资源关闭、一致快照、状态投影及兼容边界。

旧 v1 canonical bytes 与 golden hash 保留：成功 Answer 为 `b0176a43baf1b7bae2bfbd5e640f4d0f46ef0db1f963ed02865834a987db6d00`，Timeline 为 `84ea784346f65304247bd9ef30448b816314ffebe87e0cd389055ba7d88c9b10`；原有 refusal / termination golden 测试同样通过。

## 尚待集成验证

- 真实 PostgreSQL v2 fixture 尚未验证 `GORMRepository.GetWorkspaceAnalysisTimeline`。已向 foundation 请求在其真实决策 / 工具 fixture 中调用该入口，核对 journal 顺序、精确 Call 状态、receipt 摘要与 ledger；本项当前不记 PASS。
- 迁移、授权、finalizer、Workflow executor、Dispatcher、OpenAPI / Web 与 Docker 部署属于对应 owner / 主会话，需在实际集成后验收。公共合同单测不能替代这些证据，也不能关闭全部 TODO2。
- 本代理未执行 M11、Git commit / push 或部署；没有更改 Dispatcher、finalizer、Workflow Definition、Agent owner、迁移、OpenAPI / Web 或 cmd main。

## 限定补充：派发版本回归

主会话追加授权后，本代理新增 `internal/conversation/adapter/postgres/dispatch_version_test.go`，只补测试，没有修改 Dispatcher 生产实现。复用现有 constructor / Audit fixture，覆盖 v2 composition 的新分析派发、普通 RAG 继续使用 v2、显式分析 v1 / v2 选择、历史分析按持久 Definition / Hash / 输入 / 幂等键重放、历史 RAG v1 / v2 重放，以及 Definition key、版本、图、Tool 版本 Hash 或 JSON 形状漂移时拒绝返回可用 plan。

首轮定向命令如下，只有 `TestQuestionDispatchVersionRAGReplayKeepsHistoricalDefinition/v1` 失败，错误为 `CONVERSATION_QUESTION_DISPATCH_CORRUPT`：

```bash
go test -mod=vendor ./internal/conversation/adapter/postgres \
  -run '^TestQuestionDispatchVersion' -count=1 -timeout=60s
```

已将失败交给主会话。主会话修复 `buildReplayedQuestionWorkflowDispatchPlan`：历史 RAG v1 选择其冻结 Definition 并重算 root，新 RAG 仍默认 v2。本代理随后执行以下受影响检查，均通过；race 测试结果为 1.814s、退出码 0：

```bash
go test -mod=vendor -race ./internal/conversation/adapter/postgres \
  -run '^Test(QuestionDispatchVersion|QuestionWorkflowDispatchPlan|QuestionDispatcher|NewQuestionDispatcher)' \
  -count=1 -timeout=60s

go vet -mod=vendor ./internal/conversation/adapter/postgres
```

新测试已 gofmt，新增文件空白检查未报告问题。这是生产派发函数与 Runtime 端口的定向回归；没有执行真实数据库的 `replayQuestion`，不能替代 foundation / 主会话负责的 PostgreSQL 恢复验收。
