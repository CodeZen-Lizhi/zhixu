# Eino Structured Output 短 Graph 契约

## 1. Scope / Trigger

修改以下任一范围时必须读取本规范：`internal/agent/application/runner.go`、
`internal/agent/adapter/eino`、五个 StructuredRunner 消费者、Worker Composition Root、
`structured_scheduler_*` 配置、Compose selector 或 Eino core Graph 版本。

本规范只覆盖 `INITIAL -> REPAIR -> REDUCED` 的进程内、无状态短 Graph。完整 RAG Graph、
Retriever/Rerank、ToolsNode、Token Streaming、Checkpoint/Interrupt、River 调度、长期 Agent 状态和任何
外部副作用均不在本契约内。

## 2. Stable Port

Application 对外只暴露项目类型：

```go
type StructuredPhaseScheduler interface {
    Schedule(context.Context, *StructuredPhaseRun) error
}

func NewStructuredRunner(ChatModel, *RuntimeCatalog, RunBudget) (*StructuredRunner, error)
func NewStructuredRunnerWithScheduler(ChatModel, *RuntimeCatalog, RunBudget, StructuredPhaseScheduler) (*StructuredRunner, error)
func (run *StructuredPhaseRun) Advance(context.Context, domain.ModelCallPhase) error
func (run *StructuredPhaseRun) Completed() bool
func (run *StructuredPhaseRun) Failure() error
```

`NewStructuredRunner` 和 nil scheduler 都必须保持 direct 行为。Eino、`compose.Graph`、Lambda、Branch 或
Runnable 类型不得进入 Application、Domain、Workflow input、Model Run/Call、HTTP API 或数据库。

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

## 5. Consumer Selectors

五个 Worker 消费者独立选择 `direct|eino`，缺省全部为 `direct`：

| Consumer | YAML | Environment |
|---|---|---|
| RAG answer StructuredRunner | `structured_scheduler_rag` | `ZHIXU_STRUCTURED_SCHEDULER_RAG` |
| Relation assessment | `structured_scheduler_relation` | `ZHIXU_STRUCTURED_SCHEDULER_RELATION` |
| Artifact generation | `structured_scheduler_artifact` | `ZHIXU_STRUCTURED_SCHEDULER_ARTIFACT` |
| Capture profile | `structured_scheduler_capture` | `ZHIXU_STRUCTURED_SCHEDULER_CAPTURE` |
| Organizing generation | `structured_scheduler_organizing` | `ZHIXU_STRUCTURED_SCHEDULER_ORGANIZING` |

Selector 只影响 Worker 内部调度实现，不进入 Chat Provider/Model/Adapter 身份、managed model revision、
Workflow input、Model Run/Call 或 API DTO。任一消费者可单独切回 `direct`，无需迁移数据库或重放其他消费者。

## 6. Validation And Error Matrix

| Condition | Required result |
|---|---|
| `NewStructuredRunner`，或 `NewStructuredRunnerWithScheduler(..., nil)` | 使用 direct 三阶段调度，不产生 Graph 错误 |
| nil/unavailable Eino scheduler | `dependency_unavailable` / `AGENT_EINO_STRUCTURED_GRAPH_INVOKE_FAILED` / non-retryable，且不调用模型 |
| nil `StructuredPhaseRun` 进入 scheduler | `invalid_input` / `AGENT_EINO_STRUCTURED_GRAPH_STATE_INVALID` / non-retryable，且不调用模型 |
| Graph 内 state 丢失或无 run | `consistency_violation` / `AGENT_EINO_STRUCTURED_GRAPH_STATE_INVALID` / non-retryable |
| Graph 构建失败 | `dependency_unavailable` / `AGENT_EINO_STRUCTURED_GRAPH_BUILD_FAILED` / non-retryable，并阻止 Worker 启动 |
| Graph 返回的 output 不是原 state | `consistency_violation` / `AGENT_EINO_STRUCTURED_GRAPH_OUTPUT_INVALID` / non-retryable |
| scheduler 重复、越序、终态后推进或未推进到终态 | `consistency_violation` / `AGENT_STRUCTURED_SCHEDULER_CONTRACT_VIOLATION` / non-retryable |
| Provider、持久化 unknown、取消、deadline、请求/响应/token 预算或三次校验耗尽 | 原样保留项目 error kind、code、retryable 和 `errors.Is` 关系；Graph 不改写为通用 Eino 错误 |
| 未知 selector 值 | Composition fail closed，API/Worker 不得静默切回另一实现 |

## 7. Good / Base / Bad Cases

| Case | Example | Expected evidence |
|---|---|---|
| Good | INITIAL 响应通过严格 Schema | direct/Eino 都只调用一次，accepted bytes、usage、RuntimeRefs 和终态完全相同；Eino 计数包装器为 `1` |
| Good | INITIAL、REPAIR 失败，REDUCED 通过 | 固定三次调用且 phase 为 `INITIAL,REPAIR,REDUCED`；Model Call 审计各一条并成功闭合 |
| Base | selector 未配置或显式 `direct` | 不构建/注入该消费者 scheduler，保持既有 direct 行为与恢复语义 |
| Base | 五个 selector 只有一个设为 `eino` | 只切换指定消费者；其他四个仍为 direct，配置不进入持久业务身份 |
| Bad | Graph node 自己调用 Provider、retry、Tool、DB 或 checkpoint | 拒绝实现；这些职责分别属于 `StructuredPhaseRun`、持久 Workflow/Tool 边界和 PostgreSQL/River |
| Bad | 消费者测试只断言 scheduler 字段非 nil | 门禁不成立；必须用计数包装器委托真实 Eino scheduler，并断言实际 `Schedule` 恰好调用一次 |
| Bad | 已完成 River Node 的后续 transport attempt 再次调用模型 | 回归；真实 PostgreSQL/River 测试必须证明 Claim stale，Provider/Model Call/Attempt/业务终态均无新增或漂移 |

## 8. Required Gates

- Application Port：direct 默认、nil direct、越序、提前返回、取消、deadline 和稳定 contract error。
- direct/Eino 等价：INITIAL、REPAIR、REDUCED、三次 exhaustion、请求/响应/token 预算、Provider error、
  accepted bytes、RuntimeRefs、usage、call count、kind/code/retryable 和 `errors.Is`。
- 审计：Eino path 经过 `RecordingChatModel`，每次 Model Call 的 `call_no`、phase、status、bytes 和 usage 完整。
- 并发：同一个已编译 scheduler 至少 32 个并发 run，并在 `-race` 下通过；不得共享可变运行 state。
- Composition：五个 selector 默认 direct、互相独立、未知值 fail closed；五个消费者的 Eino 测试必须用计数包装器委托真实 scheduler，并断言 `Schedule` 恰好执行一次，不能只检查字段或依赖指针。
- 回归：运行 Relation、RAG、Artifact、Capture、Organizing 的现有测试/eval；Graph 不能改变其领域 finalizer、
  Evidence/Citation、Revision、receipt、Human Task 或恢复语义。
- 持久恢复：真实 PostgreSQL/River RAG 门禁必须分别运行 direct/Eino，并模拟完成 Node 的后续 transport attempt；不得增加 Provider/Model Call、Attempt 或业务终态。
- 发布：相关 `go test`、`go test -race`、`go vet`、vendor build、`go mod tidy -diff`、Compose contract、
  PoC race/vet 和 `git diff --check` 必须通过。

## 9. Wrong vs Correct

| Wrong | Correct |
|---|---|
| Graph 节点直接调用 Eino ChatModel | 节点只调用 Application `Advance`，后者调用项目 `ChatModel` |
| 用 Graph retry 修复 Provider 错误 | Provider 单次调用并分类；Workflow 决定 Node Attempt retry |
| 把 Graph state/checkpoint 当恢复事实 | PostgreSQL/River 继续拥有持久 Workflow，Graph 完全无状态 |
| 一个全局开关同时切五个消费者 | 五个固定 selector 独立灰度和回滚 |
| 把 selector 写入 Model Run 身份 | selector 只属于进程 Composition 配置 |
| 为了接 Eino 重写 decoder/预算 | 保留项目严格 decoder、原文闭包和全部预算所有权 |
