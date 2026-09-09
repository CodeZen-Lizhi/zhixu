# TODO2 v2 runtime 验证记录

日期：2026-09-09。Owner：dynamic_runtime。未 commit/push，未部署。

## 已交付范围

- 真 Eino ADK 动态循环，只通过项目模型授权端口调用 Provider；四个工具串行执行。运行时不持久化 Eino checkpoint，恢复时重建持久 decision/tool journal。
- Decision runner：共享当前 Attempt 的 ModelRun，replacement 的真实 CallNo 从新 Run 内的 1 开始；保留全局 decision ordinal。提交确认丢失只查 exact journal，PENDING denial 重投递复用已持久 OperationID。
- 模型引用必须来自受控 Search 输出，Citation 必须来自已读引用；每次重放重建这两个集合。Tools repository 继续承担最终身份与引用授权。Provider 缺少合法 usage 时记录 UNKNOWN，不以零用量伪装成功或已知失败。
- v2 Synthesis、Review、candidate stream/catalog 及四个 Workflow Executor。原始 Search 的 `items/degradations` 被显式投影为模型输入的 `hit_count/degradation_codes`，多 Search 的实际索引绑定各自保留。
- HTTP fixture 保留旧 RAG/v1；新增 v2 simple/extended/budget-loop 路由。Candidate schema2 使用实际 excerpt/global ref，proposal_suggestion 为 null。v1/v2 共用 `workspace_analysis_candidate_stream` barrier stage；v2 默认/extended 决策使用 `workspace_analysis_decision`，预算路径使用独立 `workspace_analysis_budget_decision`，便于核对真实 HTTP 调用数。
- Outcome metric 仅扩展固定 v2 label，并从 Finalizer command 的 definition version 选择；旧 helper/v1 golden 保留。无新敏感或高基数标签。

## 文件

新增：

- `internal/agent/application/workspace_analysis_loop_runtime.go`
- `internal/agent/application/workspace_analysis_decision_runner.go`
- `internal/agent/application/workspace_analysis_v2_evidence.go`
- `internal/agent/application/workspace_analysis_v2_runners_test.go`
- `internal/agent/adapter/eino/workspace_analysis_loop.go`、`workspace_analysis_loop_test.go`
- `internal/agent/adapter/workflow/workspace_analysis_v2_catalog.go`
- `internal/agent/adapter/workflow/workspace_analysis_v2_executor.go`、`workspace_analysis_v2_executor_test.go`
- `internal/agent/adapter/workflow/workspace_analysis_v2_outputs.go`
- `internal/agent/adapter/workflow/workspace_analysis_v2_decide.go`、`workspace_analysis_v2_decide_test.go`
- `internal/agent/adapter/workflow/workspace_analysis_v2_synthesize.go`
- `internal/agent/adapter/workflow/workspace_analysis_v2_publish.go`
- `cmd/rag-model-fixture/workspace_analysis_v2.go`、`workspace_analysis_v2_test.go`

在既有实现中窄改：

- `internal/agent/application/workspace_analysis_synthesis_runner.go`
- `internal/agent/application/workspace_analysis_review_runner.go`
- `internal/agent/application/workspace_analysis_candidate_stream.go`
- `internal/agent/adapter/eino/workspace_analysis_candidate_stream.go`
- `internal/agent/adapter/workflow/catalog.go`
- `internal/agent/adapter/workflow/workspace_analysis_review_publish.go`
- `internal/agent/adapter/workflow/workspace_analysis_metrics.go`、`workspace_analysis_metrics_test.go`
- `internal/conversation/workflow/workspace_analysis_publication_output.go`
- `internal/platform/observability/metrics.go`、`metrics_test.go`、`prometheus_test.go`
- `internal/observability/facade.go`
- `cmd/rag-model-fixture/main.go`、`main_test.go`

没有修改 Tools catalog/application/adapters/Postgres、Agent domain/Postgres、Finalizer SQL、Worker/main、Web/OpenAPI 或 migrations；这些部分由对应 owner 承担。

## 检查结果

以下命令全部通过：

```sh
go test -mod=vendor -race ./internal/agent/application ./internal/agent/adapter/eino ./internal/agent/adapter/workflow ./internal/conversation/workflow ./internal/platform/observability ./internal/observability ./cmd/rag-model-fixture -count=1 -timeout=120s
go vet -mod=vendor ./internal/agent/application ./internal/agent/adapter/eino ./internal/agent/adapter/workflow ./internal/conversation/workflow ./internal/platform/observability ./internal/observability ./cmd/rag-model-fixture
```

首次实现期间的定向测试已发现并修复：v2 candidate schema2 的 terminal binding（foundation owner 修复）、replay fake 缺少持久 draft、Eino 对空 Git schema 字段的等价省略、fixture 动态路由误拦旧 RAG、Search 摘要字段映射、PENDING denial ID 恢复、未知/未读引用过早进入工具阶段。

已按 go-review 自检模型/工具边界、context 与取消、独立结算超时、错误分类、不可变绑定、重放和 v1 兼容。完整 race 测试覆盖旧版回归与新增接口测试，不代表真实 Provider 回答质量验收。

真实 Compose 第二轮暴露了 Decision Receipt 的时间表示比较错误：事务返回 UTC 时间，journal 回读保留驱动 Location，两处完整 `reflect.DeepEqual` 导致首次工具调用与 finish 被误拒绝。现改为逐字段绑定及 `Time.Equal`；先 RED 复现工具 0 次与 finish 拒绝，再通过 4 个相同瞬时正例和 24 个真实漂移负例。修复后的 workflow/application/eino 全包 race 通过（5.278s / 1.940s / 1.982s），同范围 vet 通过。

第四轮真实 Compose 已 PASS（exit 0）：默认/追加检索/第 13 Decision 预算拒绝、精确 replay、SSE、Stop、只读边界及桌面/390px 页面闭环全部通过；项目容器、卷和网络已精确清空，截图已查看。前三轮真实失败与 RunStart/Decision 时间比较、Tools 脱敏回读修复证据均保留在 [Compose 验证](todo2-v2-compose-verification.md)。Tools 的公共修复由对应 owner 完成并独立验证，正常来源 fixture 使用可读的 Evidence marker 文本。

## 集成边界与跟进

- dynamic_runtime 已完成既有 Compose 的 v2 默认、追加检索、预算拒绝和浏览器 Stop/refresh；实际结果另记 [Compose 验证](todo2-v2-compose-verification.md)。主会话仍负责正式 Docker 部署。
- Finalizer owner 必须在同一终态事务调用 foundation 的 `agent.close_workspace_analysis_v2_decision_runs`，关闭 budget/tool denial 后已结算的 loop ModelRun 前缀；STARTED Call 仍必须走 UNKNOWN。
- 多 Search 的不同 global alias 可能引用同一个 tuple。Runtime 保留所有采用的 alias；最终公共 Citation 按 tuple 去重并保留每个 alias 的正文映射，由 Finalizer owner 实施。
- M11 全产品测试、Eval 和发布包仍按主会话范围暂缓。完成本子任务不等同 TODO2 全部集成验收，更不等同整个项目全部开发完成。
