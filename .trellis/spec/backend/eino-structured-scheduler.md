# Eino Structured Output 短 Graph 契约

## 1. Scope / Trigger

修改以下任一范围时必须读取本规范：`internal/agent/application/runner.go`、
`internal/agent/adapter/eino`、五个 StructuredRunner 消费者、Worker Composition Root、
退休的 `structured_scheduler_*` 配置/Compose selector 门禁或 Eino core Graph 版本；以及
`internal/agent/application/query_plan.go`、`internal/agent/domain/query_plan.go`、
`internal/agent/adapter/workflow/catalog.go` 的 Query Plan Prompt/Schema 注册或正式 v2 RAG Composition。

本规范的 Graph 所有权只覆盖 `INITIAL -> REPAIR -> REDUCED` 的进程内、无状态短 Graph；第 5 节另行锁定
正式 RAG v2 的 PLAN Provider wire 边界。完整 RAG Graph、Retriever/Rerank、ToolsNode、Token Streaming、
Checkpoint/Interrupt、River 调度、长期 Agent 状态和任何外部副作用均不在本契约内。

## 2. Stable Port

Application 对外只暴露项目类型：

```go
type StructuredPhaseScheduler interface {
    Schedule(context.Context, *StructuredPhaseRun) error
}

func NewStructuredRunnerWithScheduler(ChatModel, *RuntimeCatalog, RunBudget, StructuredPhaseScheduler) (*StructuredRunner, error)
func (run *StructuredPhaseRun) Advance(context.Context, domain.ModelCallPhase) error
func (run *StructuredPhaseRun) Completed() bool
func (run *StructuredPhaseRun) Failure() error
```

`NewStructuredRunnerWithScheduler` 必须显式接收 scheduler；nil scheduler 在 Application 边界直接 fail closed。
生产 Composition 只能注入 Eino scheduler，测试替身也只能通过同一个项目 Port 显式注入。
Eino、`compose.Graph`、Lambda、Branch 或 Runnable 类型不得进入 Application、Domain、Workflow
input、Model Run/Call、HTTP API 或数据库。

## 3. Graph Topology

Eino Adapter 在 Worker Composition Root 编译一次固定 DAG，并可被并发运行复用：

```text
START
  -> initial_call_validate
       -> END | repair_call_validate
                    -> END | reduced_call_validate
                                 -> END
```

每个 Lambda 只能调用一次 `StructuredPhaseRun.Advance`。Branch 只能读取 `Completed` 并选择固定下一节点；
不得在 Graph 内调用 Provider、执行 retry、修改阶段顺序、持久化 state、写 DB/file/Git、调用 Tool 或持有
Workflow lease。

## 4. Ownership And Invariants

- `StructuredRunner` 创建冻结的 Prompt/Schema/Profile/Model snapshot 和总 deadline；Graph 不能替换或修改它。
- `StructuredPhaseRun` 独占 `ChatModel` 调用、单次 timeout、请求/响应/token 累计预算、严格 Schema decoder、
  accepted bytes 原文相等、脱敏 validation code、阶段顺序和三次硬上限。
- `RecordingChatModel` 继续包在 Scheduler 之外。每次模型调用仍先写 STARTED，再写原有 Model Call 终态；
  Graph 不创建第二套审计事实。
- Provider、取消、deadline、预算和校验耗尽错误必须原样保留项目 kind/code/retryable 语义。
  只有 Graph 构建、调用或输出状态自身失败才允许使用 `AGENT_EINO_STRUCTURED_GRAPH_*` 稳定错误。
- Scheduler 可被多个并发 Structured Run 复用，但一个 `StructuredPhaseRun` 的阶段必须严格串行；任何重复、
  越序或终态后推进都以 `AGENT_STRUCTURED_SCHEDULER_CONTRACT_VIOLATION` fail closed。
- Graph 不注册读取 state/input/output/raw error 的 callback。Chat callback 仍按
  [`eino-chat-adapter.md`](./eino-chat-adapter.md) 只发射脱敏摘要。

## 5. RAG Query Plan Provider Wire V2

- 正式 v2 RAG 的 PLAN 必须使用 Prompt `rag-query-plan/v5` 与 Provider Schema
  `agent.rag-query-plan/v2`；正式 v2 Composition 必须同时冻结这两个精确引用，不能单独替换其中之一。
- Prompt 文本发生行为变化必须提升 `PromptRef.Version`；不得在同一版本下静默改写。ModelCall 继续保存精确
  Prompt 引用，并以包含完整 ChatRequest 消息的 canonical request hash 固化实际请求；历史版本引用和 hash 不重写。
- v5 必须把服务端提供的 scope 视为已绑定当前授权 Workspace 的完整检索范围；空 `source_ids`、
  `source_version_ids` 和 `path_prefixes` 表示检索该范围内全部合格证据，不表示范围缺失。用户要求逐字包含、
  引用证据或指定格式属于下游 Answer 约束，只要仍可形成有用检索词就不得派生 clarification。
- Provider 输出必须是无身份、扁平、`additionalProperties=false` 的严格对象，且恰有五个必填短键：
  `i`（intent）、`r`（rewrites）、`d`（clarification reason）、`q`（clarification question）、
  `s`（suggested scopes）。不得包含 `result_type`、`schema_id`、`schema_version`、`model_run_ref`、
  `payload` 或任何布尔字段，包括 `requires_clarification`。
- `r=[]` 唯一表示 clarification，此时 `d/q` 必须非空，`s` 只能是有界建议范围；`r` 含 1 到 3 条改写时
  唯一表示 retrieval，此时 `d/q` 必须为空且 `s=[]`。项目只从 rewrites 是否为空派生
  `RequiresClarification`，不能信任或兼容 Provider 布尔判断。
- 项目必须严格解码原始 Provider 文档，拒绝缺失、重复、未知、尾随或分支不一致的字段；解码成功后由
  Compose 注入当前冻结的可信 ModelRunRef，并组装为既有 canonical domain `RAGQueryPlanResult` v1。
  Provider wire v2 不得穿透为新的持久领域结果、Workflow input、HTTP DTO 或数据库 Schema。
- PLAN 的 `MaxOutputTokens` 硬上限为 256；共享 Profile 可以给出更低上限，但不能把 PLAN 提高到 256 以上。
- 旧 Prompt `rag-query-plan/v3` 与 Schema `agent.rag-query-plan/v1` 只为历史 v1 RAG 执行/回放继续注册；
  正式 v2 新流量不得选择、fallback 或自动降级到该组合。

## 6. Consumer Composition

五个 Worker 消费者在 Composition Root 分别注入同一版本的 Eino fixed short Graph；不再读取
`ZHIXU_STRUCTURED_SCHEDULER_*` 或其他进程级实现 selector。实现选择不进入 Chat Provider/Model/Adapter 身份、
managed model revision、Workflow input、Model Run/Call 或 API DTO。Eino Graph 构建失败必须 fail closed，不能自动切换。

## 7. Validation And Error Matrix

| Condition | Required result |
|---|---|
| `NewStructuredRunnerWithScheduler(..., nil)` | `dependency_unavailable`，且不调用模型 |
| nil/unavailable Eino scheduler | `dependency_unavailable` / `AGENT_EINO_STRUCTURED_GRAPH_INVOKE_FAILED` / non-retryable，且不调用模型 |
| nil `StructuredPhaseRun` 进入 scheduler | `invalid_input` / `AGENT_EINO_STRUCTURED_GRAPH_STATE_INVALID` / non-retryable，且不调用模型 |
| Graph 内 state 丢失或无 run | `consistency_violation` / `AGENT_EINO_STRUCTURED_GRAPH_STATE_INVALID` / non-retryable |
| Graph 构建失败 | `dependency_unavailable` / `AGENT_EINO_STRUCTURED_GRAPH_BUILD_FAILED` / non-retryable，并阻止 Worker 启动 |
| Graph 返回的 output 不是原 state | `consistency_violation` / `AGENT_EINO_STRUCTURED_GRAPH_OUTPUT_INVALID` / non-retryable |
| scheduler 重复、越序、终态后推进或未推进到终态 | `consistency_violation` / `AGENT_STRUCTURED_SCHEDULER_CONTRACT_VIOLATION` / non-retryable |
| Provider、持久化 unknown、取消、deadline、请求/响应/token 预算或三次校验耗尽 | 原样保留项目 error kind、code、retryable 和 `errors.Is` 关系；Graph 不改写为通用 Eino 错误 |
| 正式 v2 Composition 使用非 `rag-query-plan/v5` / `agent.rag-query-plan/v2` 组合 | Composition 合同回归，门禁失败且不得发布 |
| v2 PLAN 输出含 envelope、identity、`payload`、布尔字段、缺失/重复/未知/尾随字段 | Query Plan 严格解码失败，不注入 ModelRunRef、不进入 Retrieval |
| v2 PLAN 的空/非空 `r` 与 `d/q/s` 分支不一致 | Query Plan 校验失败，不把非法结果解释为 clarification 或 retrieval |
| PLAN Profile 输出上限大于 256 | 实际 Provider 请求收紧为 `MaxOutputTokens=256` |

## 8. Good / Base / Bad Cases

| Case | Example | Expected evidence |
|---|---|---|
| Good | INITIAL 响应通过严格 Schema | Eino scheduler 只调用一次，accepted bytes、usage、RuntimeRefs 和终态完整；计数包装器为 `1` |
| Good | INITIAL、REPAIR 失败，REDUCED 通过 | 固定三次调用且 phase 为 `INITIAL,REPAIR,REDUCED`；Model Call 审计各一条并成功闭合 |
| Good | v2 PLAN 返回 `{"i":"...","r":["..."],"d":"","q":"","s":[]}` | 严格解码原文，Compose 注入冻结 ModelRunRef，得到 canonical `RAGQueryPlanResult` v1；请求上限不超过 256 tokens |
| Base | v2 PLAN 返回空 `r` 与合法 `d/q/s` | 确定性派生 clarification，不需要也不接受 Provider boolean |
| Base | 实现字段未配置 | 由固定 Composition 注入 Eino scheduler；构建失败必须阻止对应 Worker 启动 |
| Bad | 正式 v2 新流量选择 `rag-query-plan/v3` / `agent.rag-query-plan/v1` | 回归；该组合只允许历史 v1 执行/回放 |
| Bad | Graph node 自己调用 Provider、retry、Tool、DB 或 checkpoint | 拒绝实现；这些职责分别属于 `StructuredPhaseRun`、持久 Workflow/Tool 边界和 PostgreSQL/River |
| Bad | 消费者测试只断言 scheduler 字段非 nil | 门禁不成立；必须用计数包装器委托真实 Eino scheduler，并断言实际 `Schedule` 恰好调用一次 |
| Bad | 已完成 River Node 的后续 transport attempt 再次调用模型 | 回归；真实 PostgreSQL/River 测试必须证明 Claim stale，Provider/Model Call/Attempt/业务终态均无新增或漂移 |

## 9. Required Gates

- Application Port：显式 scheduler、nil scheduler fail closed、越序、提前返回、取消、deadline 和稳定 contract error。
- Eino 合同：INITIAL、REPAIR、REDUCED、三次 exhaustion、请求/响应/token 预算、Provider error、
  accepted bytes、RuntimeRefs、usage、call count、kind/code/retryable 和 `errors.Is`。
- 审计：Eino path 经过 `RecordingChatModel`，每次 Model Call 的 `call_no`、phase、status、bytes 和 usage 完整。
- 并发：同一个已编译 scheduler 至少 32 个并发 run，并在 `-race` 下通过；不得共享可变运行 state。
- Query Plan v2：锁定 `rag-query-plan/v5` / `agent.rag-query-plan/v2`、五个短键及边界、禁止字段、两条
  rewrites 分支、严格拒绝重复/未知/尾随字段、256 tokens 硬上限、可信 ModelRunRef Compose 与 canonical
  `RAGQueryPlanResult` v1；真实 Provider live gate 还必须用完整 scope、逐字包含与引用要求的检索问题断言
  `RequiresClarification=false` 且 rewrites 非空，且不得记录问题、响应、Endpoint 或 Credential；另证明旧
  v3/v1 只被历史 v1 执行/回放引用。
- Composition：五个消费者均注入 Eino scheduler；构建失败 fail closed；测试必须用计数包装器委托真实 scheduler，并断言 `Schedule` 恰好执行一次，不能只检查字段或依赖指针。
- 回归：运行 Relation、RAG、Artifact、Capture、Organizing 的现有测试/eval；Graph 不能改变其领域 finalizer、
  Evidence/Citation、Revision、receipt、Human Task 或恢复语义。
- 持久恢复：真实 PostgreSQL/River RAG 门禁运行 Eino 路径，并模拟完成 Node 的后续 transport attempt；不得增加 Provider/Model Call、Attempt 或业务终态。
- 发布：相关 `go test`、`go test -race`、`go vet`、vendor build、`go mod tidy -diff`、Compose contract、
  PoC race/vet 和 `git diff --check` 必须通过。

## 10. Wrong vs Correct

| Wrong | Correct |
|---|---|
| Graph 节点直接调用 Eino ChatModel | 节点只调用 Application `Advance`，后者调用项目 `ChatModel` |
| 用 Graph retry 修复 Provider 错误 | Provider 单次调用并分类；Workflow 决定 Node Attempt retry |
| 把 Graph state/checkpoint 当恢复事实 | PostgreSQL/River 继续拥有持久 Workflow，Graph 完全无状态 |
| 为五个消费者复制一套实现开关 | 五个消费者分别注入同一版本的固定 Eino scheduler |
| 把实现选择写入 Model Run 身份 | 实现固定在进程 Composition，不进入业务身份 |
| 为了接 Eino 重写 decoder/预算 | 保留项目严格 decoder、原文闭包和全部预算所有权 |
| 让 v2 PLAN 回显 envelope、ModelRunRef 或 `requires_clarification` | Provider 只返回 `i/r/d/q/s`；项目从 `r` 派生分支并在 Compose 注入可信 ModelRunRef |
| 用 `rag-query-plan/v3` / `agent.rag-query-plan/v1` 处理正式 v2 新流量 | v3/v1 只服务历史 v1 执行/回放；正式 v2 固定 v5/v2 |
