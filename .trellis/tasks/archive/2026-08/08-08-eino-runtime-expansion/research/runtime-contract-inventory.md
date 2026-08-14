# Research: Phase 1 Runtime Contract Inventory

- Query: 盘点 Phase 1 所需的 ModelCall domain/SQL/repository/RecordingChatModel、现有预算所有权与 composition root，并判断 `AGENT`、`ANSWER` 和 NodeAttempt 级 `RunBudgetLedger` 的最小正确实现。
- Scope: mixed
- Date: 2026-08-08

> Decision supersession (2026-08-11): this inventory predates the explicit Eino-only
> deployment decision. Any reference below to a direct rollback path or Eino capability
> selector is historical analysis, not a current production option. The active contract
> deletes those selectors and uses Git history for application recovery; persisted v1
> replay remains Eino-backed.

## Findings

### 1. 结论

Phase 1 的最小正确切法不是继续给 `StructuredRunner` 扩字段，而是建立一个 NodeAttempt 级、项目拥有的调用授权边界：

1. Domain 与 SQL 增加可重复 `AGENT`、单次 `ANSWER`，并把 Go 的 `call_no` 上限显式对齐数据库的 32。
2. 从 `RecordingChatModel` 抽出不依赖 Provider/Eino 的 `ModelCallRecorder`；调用编号由 `RunBudgetLedger` 授权，Recorder 只负责 STARTED、CAS terminal、canonical hash、耗时与错误归约，不能再拥有第二个 `nextCallNo`。
3. Worker 启动时只构造不可变 `RunBudgetPolicy` 和可复用 Eino runtime；在每次 RAG `ModelRun` 创建成功后，为该 `NodeAttemptID` 构造一份可变 ledger，并把同一实例交给 PLAN、AGENT、ANSWER、metadata 与 REVIEW 的所有模型调用。
4. Ledger 在 Provider 调用前按最大输入/输出 Token 预占；Agent 预占必须在保留 ANSWER、最多三次 metadata（INITIAL/REPAIR/REDUCED）和 REVIEW 的调用/Token 后仍可满足。Provider usage 缺失、STARTED/UNKNOWN 恢复时按对应预占上限扣账。
5. Phase 1 可以不新增 ledger 表，也可以先不查询 ToolCall：使用版本固定的 policy、现有 ModelCall facts，并把 no-tool 实现设为 `MaxToolCalls=0`。但如果允许配置漂移后继续同一 NodeAttempt，则必须把 budget policy identity（或每次 input reservation）持久化；否则无法精确恢复，不能宣称满足设计中的恢复合同。

当前实现虽然在 RAG 内复用一个 `RecordingChatModel`，所以 `call_no` 连续，但没有共享预算：PLAN 没有 `RunBudget`，INITIAL/REPAIR/REDUCED 使用一份新的结构化运行预算，REVIEW 又独立获得完整预算。这个结构不能证明 Agent 不会耗尽下游调用或 Token。

### 2. Files Found

| Path | Description |
|---|---|
| `.trellis/tasks/08-08-eino-runtime-expansion/prd.md` | 当前任务的生产合同；24-26 行定义共享 ledger、phase、stream terminal，36 行定义验收门禁。 |
| `.trellis/tasks/08-08-eino-runtime-expansion/design.md` | 29-43 行定义 Eino-free Port/Recorder 边界，73-81 行定义 Agent 与 ledger，100-104 行定义 final ANSWER stream 生命周期。 |
| `.trellis/tasks/08-08-eino-runtime-expansion/implement.md` | 9-14 行是本次 Phase 1 的直接实施范围。 |
| `internal/agent/domain/runtime.go` | `ModelCallPhase`、`TokenUsage`、`ModelRun`、`ModelCall` 及生命周期校验。 |
| `internal/agent/application/chat.go` | 现有严格结构化 `ChatRequest/ChatResponse/ChatModel`；不支持 tool call 或无结构化 Markdown stream。 |
| `internal/agent/application/recording_chat.go` | 当前记录包装器；同时拥有 provider 调用、call number、hash 和 repository CAS。 |
| `internal/agent/application/runtime_repository.go` | `ModelRunRepository`、`ModelRunRecord` 和 call terminal command。 |
| `internal/agent/application/runner.go` | 三阶段 `RunBudget`、`StructuredRunner` 和每次运行独立累计器。 |
| `internal/agent/application/query_plan.go` | PLAN 单调用；只用 profile timeout，不进入 RunBudget。 |
| `internal/agent/application/faithfulness.go` | REVIEW 单调用；拥有独立 `RunBudget`。 |
| `internal/agent/adapter/postgres/calls.go` | ModelCall STARTED insert、replay binding 与 terminal CAS。 |
| `internal/agent/adapter/postgres/scans.go` | ModelCall scan、稳定 call_no 排序、replay binding/result 比较。 |
| `internal/agent/adapter/postgres/repository.go` | ModelRun create/get；`GetModelRun` 已能按 run ID 返回完整 call facts。 |
| `internal/agent/adapter/postgres/runs.go` | 只有事务内 `GetModelRunByAttemptTx`；没有普通 read port。 |
| `internal/agent/adapter/workflow/rag_executor.go` | 当前 RAG NodeAttempt 外层和 per-run recorder/planner/runner/reviewer 组合点。 |
| `internal/agent/adapter/eino/scheduler.go` | 现有 Eino 仅是固定 INITIAL/REPAIR/REDUCED scheduler，不拥有 Provider 或预算。 |
| `internal/platform/models/runtime.go` | `ChatCapability` 只暴露项目 `ChatModel`；进程级 runtime 是不可变共享对象。 |
| `internal/platform/models/eino_chat.go` | 当前 Eino Chat backend 是私有字段，公开路径仅实现一次 `Generate`。 |
| `cmd/worker/main.go` | Tool 与 Agent 的唯一生产 composition root。 |
| `internal/tools/domain/call.go` | ToolCall 已绑定 NodeAttempt 与独立 call_no。 |
| `internal/tools/application/persistence.go` | 当前 Tool timeline query 不支持精确 NodeAttempt，且最多 500 条。 |
| `internal/tools/application/execution.go` | Tool 执行 call_no 上限为 PostgreSQL smallint 的 32767。 |
| `migrations/00018_agent_runtime.sql` | ModelCall 表、call_no=32 上限、single-active、generation uniqueness、基础顺序/生命周期 trigger。 |
| `migrations/00020_rag_conversation_sse.sql` | 当前 PLAN-aware phase CHECK、predecessor trigger 和 guarded Down。 |
| `migrations/00019_tool_registry_security.sql` | ToolCall `(node_attempt_id, call_no)` 唯一约束。 |
| `internal/platform/migration/agent_runtime_integration_test.go` | 现有真实 SQL 状态机、workspace、CAS 与 guarded Down 测试。 |
| `internal/agent/adapter/postgres/repository_integration_test.go` | 现有 ModelRun/Call replay、CAS、UNKNOWN recovery 测试。 |

### 3. Current ModelCall Contract

#### Domain

- `ModelCallPhase` 只有 PLAN、INITIAL、REPAIR、REDUCED、REVIEW（`internal/agent/domain/runtime.go:31-41`）；`validModelCallPhase` 同样只接受这五项（248-250）。
- `ModelCall` 已包含 `ModelRunID`、连续 `CallNo`、实际 Model/Profile/Prompt/Schema refs、`MaxOutputTokens`、hash/bytes/usage/latency/status/version（176-198）。该形状可以承载 Generate 和 Stream 的单条最终事实，不需要新增 STREAMING 状态。
- `ValidateModelCall` 只验证 `CallNo >= 1`，未验证数据库的 32 上限（200-208）。应新增共享常量，例如 `MaxModelCallsPerRun = 32`，并同时用于 domain、ledger policy 校验和测试。
- STARTED 不能带 terminal 数据；SUCCEEDED 必须有 response hash/bytes；UNKNOWN 不能声称拿到 provider response（210-236）。这与“Stream EOF 才成功，无法证明结果则 UNKNOWN”兼容。
- `TokenUsage.Validate` 允许全零（79-92），但现有严格 ChatResponse 额外拒绝 `TotalTokens == 0`（`internal/agent/application/chat.go:111-127`）。因此新 Eino recording contract 可以把 terminal 的全零明确定义为“provider 未报告 usage”，ledger 恢复时按 reservation 扣账，而不必仅为 presence 新增列；任何“已报告”的 usage 仍必须 total > 0 且精确相加。

#### Existing Chat DTO Is Not the New Agent/Stream Port

- `ChatRequest` 强制 OutputSchema，`ChatResponse` 只有非空 Content，且 `ChatModel` 只有 `Chat`（`internal/agent/application/chat.go:47-76,78-128`）。
- AGENT 合法返回可能只有 tool calls，ANSWER 是不带 JSON output schema 的 Markdown stream；直接放宽这个旧 Port 会破坏全部严格结构化消费者。
- Phase 1 应新增项目 DTO/Port（例如 `ModelGenerateRequest/Result`、`ModelStreamRequest/Chunk`），显式表示有界 messages、tool definitions、assistant tool calls、可选 usage 和 stream EOF；旧 `ChatModel` 继续服务结构化路径，并由兼容 wrapper 委托同一个 Recorder/Ledger。
- `ModelCall.Schema` 仍应保存一个版本化输出合同引用；ANSWER 即使是 Markdown，也应有项目定义的 content-contract `SchemaRef`，不能填空或借用 metadata schema。

### 4. SQL Migration: `00078_eino_runtime_model_call_phases.sql`

仓库当前最新迁移是 `00077_workspace_git_capture.sql`，因此下一号应为 `00078`。不要改写 00018/00020 的历史文件。

#### Must Preserve

- `call_no smallint CHECK (call_no > 0 AND call_no <= 32)`（`migrations/00018_agent_runtime.sql:87-103`）。
- `(model_run_id, call_no)` unique、INITIAL/REPAIR/REDUCED singleton partial index、每 run 最多一个 STARTED（115-143）。
- 基础 trigger 在插入时要求 parent RUNNING、前一 call 已存在且不再 STARTED，并把 immutable binding/CAS 锁死（217-282）。
- 这些约束已经提供 gap、active predecessor 和串行化防线；新迁移只扩 phase grammar，不应替换这些约束。

#### Required Up Changes

1. 重建 `agent_model_call_phase_check`，允许 `PLAN, AGENT, ANSWER, INITIAL, REPAIR, REDUCED, REVIEW`。
2. 重建 `agent_model_call_phase_order`。固定 call number 集合无法表达 `AGENT*`，因此 CHECK 只能做局部范围约束，完整 predecessor 规则继续放 trigger。
3. 重写 `agent.guard_model_call_phase_sequence()`（当前实现在 `migrations/00020_rag_conversation_sse.sql:289-321`），同时读取前驱 phase/status；存在前驱时必须为 SUCCEEDED，不能在 FAILED/UNKNOWN 后继续 pipeline。
4. 保留 `uq_agent_model_call_generation_phase`，另加 `uq_agent_model_call_answer_phase ON agent.model_call(model_run_id) WHERE phase='ANSWER'`。不要把 AGENT 加入 singleton index。
5. Down 必须先 `ACCESS EXCLUSIVE` 锁表，并在存在 AGENT/ANSWER facts 时抛 SQLSTATE `55000`，再删除 ANSWER index并恢复 00020 的五 phase 合同；现有 00020 Down 对 PLAN 采取相同模式（585-654）。

#### Backward-Compatible Global FSM

`agent.model_run` 当前没有 `run_kind/pipeline_version`，而 Relation、Artifact、Capture、Organizing 等消费者仍会以 INITIAL 开始。因此数据库不能把所有 ModelRun 都强制为 ANSWER 之后才 INITIAL。最小兼容 grammar 应为：

| New phase | Allowed position/predecessor | Reason |
|---|---|---|
| PLAN | only `call_no=1` | 保持当前可选 query plan。 |
| AGENT | call 1，或 predecessor PLAN/AGENT | 支持无 PLAN 或重复 Agent turn。 |
| ANSWER | call 1，或 predecessor PLAN/AGENT | `AGENT*` 可为零；partial unique 保证 singleton。 |
| INITIAL | call 1，或 predecessor PLAN/ANSWER | call 1/PLAN 保留旧消费者与旧 RAG；ANSWER 开启新 RAG metadata。 |
| REPAIR | predecessor INITIAL | 保持结构化修复。 |
| REDUCED | predecessor REPAIR | 保持结构化降级。 |
| REVIEW | predecessor INITIAL/REPAIR/REDUCED | 保持发布前审查。 |

该 FSM 会拒绝 ANSWER 后再次 AGENT、AGENT 后跳入 metadata、重复 ANSWER、越序 repair/reduced/review，同时保留现有调用方。现有基础 trigger 只检查前驱“不再 STARTED”（`migrations/00018_agent_runtime.sql:239-247`），所以新 phase trigger 还应要求前驱 SUCCEEDED；Provider FAILED/UNKNOWN 后必须终止。结构化 decode 失败仍可进入 REPAIR，因为这时 Provider ModelCall 已 SUCCEEDED。RAG 的更严格语法“可选 PLAN -> AGENT* -> ANSWER -> metadata -> REVIEW”必须由 RAG runtime/ledger 再校验。

如果验收要求数据库自身精确区分 RAG 与非 RAG grammar，则最小兼容 FSM 不够，必须先给 `model_run` 增加不可变 `run_kind`/`pipeline_version` 并在 trigger 中按 kind 分支。用 `output_schema_id` 猜 run kind 虽然当前可行（RAG 由 `RAGAnswerSchemaID` 识别），但会把 schema 演进与执行状态机耦合，不建议作为生产合同。

### 5. Repository Impact

- `ModelRunRepository.GetModelRun(workspaceID, runID)` 已返回按 call_no 排序的完整 `ModelRunRecord`（`internal/agent/application/runtime_repository.go:50-65`; `internal/agent/adapter/postgres/repository.go:61-78`），足够在已知 run ID 时恢复模型调用额度。
- 普通 port 没有 `GetModelRunByAttempt`；只有事务 finalizer 的 `GetModelRunByAttemptTx`（`runtime_repository.go:11-21`; `runs.go:64-81`）。RAG 已持有刚创建的 run，因此 Phase 1 不需要为 ledger 新增 attempt lookup；未来独立恢复器才需要公开 read-only attempt query。
- `StartModelCall` 使用 `ON CONFLICT DO NOTHING` 后按完整 binding 比较 replay（`calls.go:13-60`）；`CompleteModelCall` 只从 STARTED/version CAS 到 terminal（62-97）。新 Recorder 应继续复用这两个端口，不能另建 Eino persistence path。
- `loadModelCalls` 按 call_no/id 排序并拒绝非递增结果（`scans.go:133-155`）；`sameModelCallBinding` 已比较 phase、runtime refs、max output、request hash/bytes/startedAt（208-218）。若以后新增 `max_input_tokens` 或 `budget_policy_ref`，insert/select/scan/binding/result/recovery SQL 必须同步更新，不能只改 struct。
- Phase 只作为 string 存取，单纯加入 AGENT/ANSWER 不要求改变 insert/scan SQL；repository 变更主要是 Recorder 调用方式和集成测试。

#### Tool Facts Gap

- ToolCall 已有 `NodeAttemptID` 与独立 `CallNo`（`internal/tools/domain/call.go:109-142`），数据库唯一键是 `(node_attempt_id, call_no)`（`migrations/00019_tool_registry_security.sql:3-9,111`）。ModelCall 与 ToolCall 的 call_no 是两套独立序列，ledger 必须分别计数。
- 当前 `ListTimeline` 只按 workspace/workflow/可选 node run 查询，限制 500，并不按 NodeAttempt 精确过滤（`internal/tools/application/persistence.go:98-120`; `internal/tools/adapter/postgres/calls.go:344-388`）。它不能作为生产 ledger 恢复数据源。
- Phase 1 no-tool 实现可把 `MaxToolCalls=0` 并传空 tool facts，不必扩大 Tools repository。接入 Agent Tools 前必须新增精确 `ListToolCallsByAttempt`/usage facts port；不能用 timeline 截断结果推断剩余额度。

### 6. `ModelCallRecorder` Extraction

#### Current Problems

- `RecordingChatModel` 直接持有 provider、repository、IDs/clock 和 `nextCallNo`（`internal/agent/application/recording_chat.go:24-47`）。
- 它在 repository START 前调用 `takeCallNo`，即使 START 失败编号也已前移（78-99,144-150）。当前行为会让该 Attempt fail closed，但无法与 ledger 形成一个权威编号源。
- 请求 hash 是 `json.Marshal(ChatRequest)`，响应 hash 只有 `response.Content`（78-91,114-128）；这无法覆盖 Eino tool schema/tool calls/usage，也把 hash 稳定性隐式绑定到 Go/Eino 表示。
- terminal persistence 已正确使用 `context.WithoutCancel` 加 5 秒收口；失败返回 manual recovery（130-141），应保留。

#### Minimum Project-Owned API

建议在 `internal/agent/application/model_call_recorder.go` 提取：

```go
type ModelCallRecorder interface {
    Begin(context.Context, BeginModelCallCommand) (ModelCallRecording, error)
    Complete(context.Context, CompleteRecordedModelCallCommand) error
}

type BeginModelCallCommand struct {
    Authorization ModelCallAuthorization // ledger 产生：CallNo/Phase/max input/max output
    Runtime       FrozenRuntimeRefs
    Request       CanonicalModelRequest
}

type CompleteRecordedModelCallCommand struct {
    Recording ModelCallRecording
    Outcome   RecordedModelOutcome // success/failed/unknown + canonical response + optional usage
}
```

具体命名可随现有风格调整，但职责必须满足：

- Ledger 是 `call_no` 和 phase authorization 的唯一来源；Recorder 不再接受 `StartingCallNo`，也不维护 `nextCallNo`。
- `Begin` 必须在调用 Generate/Stream 之前持久化 STARTED；失败时绝不调用 Provider。
- recording handle 固化 persisted call ID/version/startedAt，并且只能 terminalize 一次；重复 terminal 必须走 repository 的精确 replay/CAS，而不是第二次调用 Provider。
- 兼容的 `RecordingChatModel` 可以保留，改成“旧 `ChatModel` adapter -> ledger authorization -> shared Recorder -> provider”，让现有 PLAN/structured/REVIEW 在 Phase 1 就共享一本账。
- 新 `internal/agent/adapter/eino` recording model 使用同一 Recorder 包装 `Generate`、`Stream`、`WithTools`；Eino 类型不进入上述 Application DTO。

#### Canonical Hash Contract

- 请求 canonical document 至少包含 phase、实际 model/profile/prompt/schema refs、max token、按原顺序的 project messages，以及按稳定 tool ref/name 排序且 JSON schema 已规范化的 tool definitions。
- 响应 canonical document包含 assistant role/content、按 provider 返回顺序的受限 tool calls、finish outcome 和 usage presence/value。不要 hash `schema.Message` 或 eino-ext 的 JSON。
- JSON schema/tool arguments 必须先严格 parse 成项目值再用固定 canonical encoder 输出；只对原始 byte 做 `TrimSpace` 不足以消除 object key/空白差异。
- 消息顺序和 tool-call 顺序具有语义，不能排序；工具定义集合可按冻结项目身份排序。
- 现有 request hash 行为在 `recording_chat.go:78-91`，response-only-content 行为在 114-128，二者都应由 shared canonical helper 取代。

#### Stream Terminal Rules

- `Stream()` 返回前先有 STARTED；唯一 consumer 持续读取并总结合并 project chunks。
- 只有收到 EOF、完整响应通过项目校验并成功 CAS 时记录 SUCCEEDED。
- stream read error、显式 cancel、deadline、主动提前 close 都归 FAILED，并使用稳定项目错误码；所有路径 Close reader。
- Provider 已可能执行但 terminal CAS 结果无法确认时，调用方返回 `AGENT_MODEL_CALL_RESULT_UNKNOWN`；数据库中的 STARTED 由既有 stale recovery 归 UNKNOWN，不能伪造 response。
- usage 可能只在尾帧出现；只允许一个 authoritative usage，重复/冲突 usage fail closed。缺失 usage 以全零 fact 表示，ledger 按 authorization 上限结算。

### 7. NodeAttempt-Level `RunBudgetLedger`

#### Existing Ownership Is Fragmented

- `RunBudget` 只有 request/response bytes、total tokens、timeout（`internal/agent/application/runner.go:45-60`），没有总模型调用、Agent iteration、ToolCall、输入/输出分账或 phase reservation。
- 每个 `StructuredRunner.Run` 新建自己的 `StructuredPhaseRun` 和 result accumulator（151-180），在 `Advance` 内按该次结构化运行累计 bytes/tokens（196-285）。
- QueryPlanner 只用 profile timeout，完全没有 RunBudget（`query_plan.go:52-107`）。
- Faithfulness reviewer 保存另一份独立 RunBudget（`faithfulness.go:52-68`），只校验单次 REVIEW（70-118）。
- RAG 虽创建一个 recorder，但随后分别构造 planner、runner、reviewer，并向后两者各传完整 budget（`rag_executor.go:218-263`）。因此现状不是 NodeAttempt aggregate budget。

#### Minimum Types and Invariants

建议新建 `internal/agent/application/run_budget_ledger.go`，至少定义：

```go
type RunBudgetPolicy struct {
    Version             string
    MaxTotalModelCalls  int
    MaxAgentIterations  int
    MaxToolCalls        int
    MaxInputTokens      int64
    MaxOutputTokens     int64
    Timeout             time.Duration
    PhaseReservations   []PhaseBudgetReservation
}

type PhaseBudgetReservation struct {
    Phase           domain.ModelCallPhase
    MaxCalls        int
    MaxInputTokens  int64
    MaxOutputTokens int64
}

type ModelCallAuthorization struct {
    CallNo          int
    Phase           domain.ModelCallPhase
    MaxInputTokens  int64
    MaxOutputTokens int64
}
```

Ledger 需要 mutex 保护的一次性 reserve/settle token，不能只暴露 `Remaining()` 后由调用方自行扣减，否则并发 Generate/Stream 会超卖。

核心不变量：

1. `MaxTotalModelCalls <= domain.MaxModelCallsPerRun (32)`；AGENT 每次模型 turn 同时占一个 model call 和一个 agent iteration。ANSWER/metadata/REVIEW 不占 Agent iteration。
2. 以 `ModelRun.CreatedAt + policy.Timeout` 形成单一绝对 deadline；所有 child call 使用 `min(profile timeout, remaining attempt deadline)`，不能每一阶段重新获得完整 timeout。
3. 初始 standing reservations 至少覆盖 ANSWER 1 次、metadata 最坏 3 次、REVIEW 1 次，即 5 个 model-call slots 及各自最大 input/output tokens。可选 PLAN 在执行前单独占用；已经确定不需要的下游 branch 才能显式释放。
4. AGENT reserve 的判定式为：`settled + inflight + requested_agent_max + still_required_downstream <= policy total`，模型调用数、input、output 三个维度都要成立。
5. 某个下游 phase 开始时，原子地把它的 standing reservation 转为 inflight，不能先释放再竞争 reserve。
6. usage 已报告且合法时按实际值结算并释放差额；usage 缺失、持久 STARTED/UNKNOWN、provider outcome 不可证明时按该 authorization 的 input/output 上限全额扣账。
7. Provider 之前失败（例如 STARTED insert 失败）可以释放 inflight，但该 Attempt 应终止或安全重用同一候选 call_no；Provider 一旦可能执行就不能退还全额 reservation。
8. 从 persisted facts 构造时校验 call_no 无 gap/重复、phase 合法、最多一个 STARTED、usage 不超过当次 reservation、总账未超限；违反任一项即 consistency/manual recovery，不能重置为零。

在总调用数 32 的硬上限下，若 PLAN 已执行且完整保留五个下游 slots，则 Agent 最多还能授权 `32 - 1 - 5 = 26` 轮；无 PLAN 时最多 27。实际 `MaxAgentIterations` 应更小，并取 policy 与此计算值的最小值。ADK `MaxIterations` 每次只使用 ledger 当下授权值，不能另设更大的独立配置。

#### Persistence Decision

Phase 1 可采用下列最小方案，不新增 reservation 列：

- policy 是带 Version 的不可变目录项；同一 NodeAttempt 的 policy 不因热配置变化。
- phase/agent 的 max input reservation 由该 policy 确定，output reservation 与 persisted `ModelCall.MaxOutputTokens` 对齐。
- terminal 全零 usage 明确定义为未报告；restore 时按 policy/max output 全额扣账。
- ModelCall facts 来自 `GetModelRun`；no-tool facts 为空。

这只有在“同一 Attempt 恢复时能解析到完全相同的 historical policy”成立时才正确。若 policy 可以修改/删除，必须在首次创建 ModelRun 时持久化 `budget_policy_id/version`；若每轮 Agent 允许不同 input reservation，还必须在 ModelCall 持久化 `max_input_tokens`（并加入 immutable binding）。否则 crash 后无法知道当时预占上限，最多只能把全局剩余额度全部烧掉并进入人工恢复。

### 8. Composition Root

#### Correct Lifetime

- `platformmodels.ModelRuntime` 和 `modelsettingsruntime.Models` 是进程级冻结 factory/runtime（`internal/platform/models/runtime.go:126-170`; `internal/modelsettings/runtime/models.go:30-109`）。Mutable ledger 绝不能放入这些对象。
- Worker 先构造 Tool components（`cmd/worker/main.go:1099-1102`），后构造 Agent components（1319-1321）。Tool repository/execution service 已在 `toolRuntimeComponents` 中（229-239），未来可显式传给 Agent runtime。
- `newAgentWorkflowComponents` 是唯一 Agent production composition root（1973-2110）；它应在启动时验证 `RunBudgetPolicy`，构造 shared recorder dependencies，并编译可复用 Eino runtime。
- 真正的 ledger 应在 `RAGWorkflowExecutor.Execute` 完成 Memory snapshot 与 `FinalizeRAGMemorySnapshotAndCreateModelRun` 后、任何模型调用前创建（`internal/agent/adapter/workflow/rag_executor.go:140-153`）。当前最自然位置是 `executeRun` 开头（218-231）。
- 每次 Execute/NodeAttempt 创建一个 ledger；Eino Graph 可进程复用，但 ledger、messages、stream reader、call number 和 reservations 都是 per-run state。

#### Current Eino Capability Gap

- `ChatCapability` 只保存/返回 `agentapplication.ChatModel`（`internal/platform/models/runtime.go:24-48`）。
- `EinoOpenAIChatModel` 的 `backend *einoopenai.ChatModel` 是私有字段，公开 `Chat` 只调用 Generate（`internal/platform/models/eino_chat.go:27-32,72-126`）。现有 composition 无法取得 `ToolCallingChatModel.WithTools` 或 `Stream`。
- Eino v0.9.13 的 `ToolCallingChatModel` 明确提供并发安全的 immutable `WithTools`；eino-ext openai v0.1.13 同时实现 Generate、Stream、WithTools。Phase 1 需要一个只存在于 infrastructure/`internal/agent/adapter/eino` 的 typed capability/factory，不能通过 `any`，也不能把 Eino interface 加进 Application Port。
- 不能简单暴露当前 raw backend：现有 hardened RoundTripper 在 `eino_chat.go:190-205` 强制 `application/json` 并完整读取/解码响应，SSE `text/event-stream` 会被拒绝；直接绕过 `Chat` 还会绕过当前 request payload/response project validation。Stream/Tool path 必须有独立的 hardened Eino adapter，而不是对私有 backend 做 type assertion 后裸调。

#### Suggested Phase 1 Wiring

1. `newAgentWorkflowComponents` 构造不可变 policy 和 Eino-free `RAGRuntime` Port implementation；direct rollback 与 Eino capability选择在 composition root fail closed。
2. `RAGWorkflowExecutorDependencies` 接收 `Runtime RAGRuntime`、`BudgetPolicy RunBudgetPolicy`、Recorder factory/dependencies，而不是一个 mutable ledger。
3. `Execute` 创建 run 后加载/校验 ModelCall facts，创建 ledger，再调用 runtime；runtime request 携带 ledger/recording capability，但 public DTO 不出现 Eino 类型。
4. Phase 1 no-tool/no-draft runtime 仍必须走同一 ledger/recorder，并允许合法的 `PLAN -> ANSWER -> metadata -> REVIEW`（`AGENT* = 0`）。不要为后续 Agent 再建第二套 runtime。
5. 接 Tools 时，把已先构造的 `toolComponents.execution/repository/enabledRefs` 作为只读 bridge 注入 `newAgentWorkflowComponents`；当前函数签名尚未接收 tool components（`cmd/worker/main.go:1319,1973`）。

### 9. Exact Change Inventory for Phase 1

| File | Symbols/change |
|---|---|
| `internal/agent/domain/runtime.go` | 新增 `ModelCallAgent`、`ModelCallAnswer`、`MaxModelCallsPerRun=32`；扩 `validModelCallPhase`；`ValidateModelCall` 校验上限。 |
| `internal/agent/application/chat.go` | 旧 strict Chat phase validation 若兼容 wrapper 需接受新 phase；不要把 tool/stream 语义硬塞进旧 `ChatResponse`。 |
| `internal/agent/application/model_call_recorder.go` (new) | `ModelCallRecorder`、begin/terminal commands、canonical project request/response、STARTED/CAS/UNKNOWN 生命周期。 |
| `internal/agent/application/model_call_canonical.go` (new or same file) | 固定 canonical encoder/hash；无 Eino imports。 |
| `internal/agent/application/run_budget_ledger.go` (new) | `RunBudgetPolicy`、phase reservations、`RunBudgetLedger`、one-shot authorization/settlement、restore/deadline。 |
| `internal/agent/application/runtime.go` (new or existing suitable Port file) | Eino-free `RAGRuntime`/`AgentRuntime`/stream DTO Port；先实现 no-tool/no-draft 合法路径。 |
| `internal/agent/application/recording_chat.go` | 变为旧 Chat compatibility adapter，委托 ledger+shared Recorder；移除 `StartingCallNo`/`takeCallNo` 独立所有权。 |
| `internal/agent/application/runtime_repository.go` | 现有 port 足够 Phase 1；仅当选择持久 policy identity 时扩 ModelRun fields/commands。 |
| `internal/agent/adapter/postgres/calls.go` | phase-only 无 SQL shape 改动；若新增 policy/max input 字段则同步 insert/update。 |
| `internal/agent/adapter/postgres/scans.go` | 若新增持久字段，同步 select/scan/same binding/result；否则只补 tests。 |
| `internal/agent/adapter/workflow/rag_executor.go` | 在 ModelRun 创建后生成 per-attempt ledger；所有现有模型路径共享；逐步改为 `RAGRuntime` Port。 |
| `internal/platform/models/runtime.go` / `eino_chat.go` | 增加 infrastructure-only Eino typed capability/factory；不得裸暴露当前非流式 hardened backend。 |
| `cmd/worker/main.go` | 启动时构造 immutable policy/compiled runtime，注入 executor；不保存 mutable ledger。 |
| `migrations/00078_eino_runtime_model_call_phases.sql` (new) | phase CHECK/order trigger、ANSWER singleton index、guarded Down；保留 call_no/single-active/generation constraints。 |

### 10. Test Checklist

#### Domain/Application Unit

- `internal/agent/domain/runtime_test.go`: AGENT/ANSWER accepted；unknown phase rejected；call_no 1/32 accepted、0/33 rejected；现有 STARTED/SUCCEEDED/FAILED/UNKNOWN invariants 不回归。
- `internal/agent/application/run_budget_ledger_test.go` (new): policy 零值/上限/overflow；可选 PLAN；零/重复 AGENT；ANSWER singleton；非法 phase order；32 上限；分别耗尽 model calls/iterations/tool calls/input/output/deadline。
- Ledger reservation: Agent 无法侵占 ANSWER+三次 metadata+REVIEW；实际 usage 结算释放差额；missing usage、STARTED、UNKNOWN 全额扣 reservation；provider 前失败释放；provider 后 unknown 不退款。
- Ledger restore: persisted call gap/duplicate/越序/多 STARTED/usage 超 reservation/总账超限全部 fail closed；两个 NodeAttempt 不共享状态；并发 reserve/settle 不超卖且 one-shot。
- `internal/agent/application/recording_chat_test.go`: 复用一个 recorder 记录 `PLAN, AGENT, AGENT, ANSWER, INITIAL, REVIEW` 连续编号；START 失败时 provider count=0；call 32/33；terminal CAS replay/conflict；persistence loss 返回 manual recovery。
- Canonical hash: JSON key/空白等价稳定；消息顺序或 tool-call 内容变化产生不同 hash；Eino struct serialization 不参与；response hash 覆盖 tool calls 与 usage presence/value。
- Stream recorder: multi-frame EOF success；usage 只在尾帧；EOF 前 error；cancel/deadline；early close；Close error；terminal persistence error；每条路径 reader 恰好关闭且无 goroutine 泄漏。
- 旧 `runner_test.go`、`query_plan_test.go`、`faithfulness_test.go`: 同一 ledger 下保持严格解码、错误 kind/code/retryable、accepted bytes、runtime refs 和无 provider retry。

#### PostgreSQL/Migration

- `internal/platform/migration/agent_runtime_integration_test.go`: fresh Up、重复 Up；新 phase CHECK/index/trigger 存在；空数据 Down->Up；存在 AGENT 或 ANSWER 时 Down 返回 55000。
- 合法序列：`ANSWER->INITIAL->REVIEW`、`PLAN->ANSWER->INITIAL->REPAIR->REDUCED->REVIEW`、`AGENT->AGENT->ANSWER->INITIAL->REVIEW`、`PLAN->AGENT->ANSWER...`。
- 兼容序列：既有 `INITIAL->REPAIR->REDUCED->REVIEW` 与 `PLAN->INITIAL...` 继续合法；PLAN clarification 可无后继。
- 非法序列：PLAN 非首位、AGENT after ANSWER、INITIAL after AGENT、ANSWER after ANSWER/INITIAL、REPAIR/REDUCED/REVIEW 错 predecessor，以及任一 FAILED/UNKNOWN 前驱后继续插入。
- 唯一/顺序：重复 ANSWER 触发 unique；AGENT 可重复；call_no gap、active predecessor、call 33 拒绝，call 32 接受；并发 insert 最多一个 STARTED。
- `internal/agent/adapter/postgres/repository_integration_test.go`: AGENT/ANSWER START/CAS/replay、不同 canonical hash 冲突、完整 refs/usage round trip、stale STARTED->UNKNOWN、calls 稳定排序。

#### Adapter/Composition/Race

- `internal/agent/adapter/eino`: Generate、WithTools、Stream 全部先 STARTED 后 provider；自动 retry/failover 为空；stream EOF/error/cancel 映射；canonical project hash；32+ 并发独立 runtime 在 `-race` 下无共享 state。
- `internal/agent/adapter/workflow/rag_executor_test.go`: 每个 Execute 恰建一本 ledger；planner/answer/metadata/reviewer 共享同一实例；terminal receipt replay 在构造 ledger/provider 前返回；第二 NodeAttempt 新建 ledger。
- `cmd/worker/*composition*_test.go`: immutable policy 在 startup 校验；compiled Eino runtime 只构造一次；mutable ledger 不进入 `agentWorkflowComponents`/process Models；unknown/missing Eino capability fail closed。
- 后续 Tools 接入前补 exact NodeAttempt ToolCall facts 测试，证明 timeline limit/其他 attempt 不影响 ledger。

建议的相关门禁命令（由实现/检查阶段执行，本次研究未运行）：

```text
go test ./internal/agent/domain ./internal/agent/application ./internal/agent/adapter/eino ./internal/agent/adapter/workflow ./cmd/worker
go test -race ./internal/agent/application ./internal/agent/adapter/eino ./internal/agent/adapter/workflow
go test -tags=integration ./internal/platform/migration ./internal/agent/adapter/postgres
go vet ./internal/agent/... ./internal/platform/models/... ./cmd/worker
```

### 11. External References / Versions

- `go.mod:6` 固定 `github.com/cloudwego/eino v0.9.13`。
- `go.mod:9` 固定 `github.com/cloudwego/eino-ext/components/model/openai v0.1.13`；Embedding extensions 固定到 pseudo-version `v0.0.0-20260803030130-90a15623ddb6`（6-9）。
- 本机已下载的 Eino v0.9.13 `components/model/interface.go:89-103` 定义并发安全、返回新实例的 `ToolCallingChatModel.WithTools`；同文件 67-68 明确 StreamReader 单消费。
- 本机 eino-ext openai v0.1.13 `chatmodel.go:250-275` 实现 Generate、Stream、WithTools。
- 未查询外网；版本与接口结论来自仓库 lockfile 和本机该精确版本源码。

### 12. Related Specs

- `.trellis/spec/backend/eino-structured-scheduler.md:48-60`: StructuredRunner/PhaseRun 当前拥有 snapshot、deadline、预算，RecordingChatModel 在 graph 外；这是需要向 Attempt ledger 收口的既有边界。
- `.trellis/spec/backend/eino-structured-scheduler.md:103-115`: 要求审计、并发、composition、持久恢复门禁。
- `.trellis/spec/backend/database-guidelines.md:877-949`: ModelRun/Call STARTED/CAS/UNKNOWN、连续 call_no、不可保存 raw content 与 migration/repository 测试合同。
- `.trellis/spec/backend/directory-structure.md:54-56`: Adapter/SDK 只能由 `cmd/api`、`cmd/worker` composition root 构造；Application/Domain 不读取配置或创建 SDK client。
- 当前任务 PRD 明确取代历史 No-Go；`.trellis/tasks/08-08-eino-runtime-expansion/research/final-result.md` 只能作为历史背景，不能作为本任务决策依据。

## Caveats / Not Found

1. 当前代码没有 `ModelCallRecorder`、`RunBudgetLedger`、`RAGRuntime`、`AgentRuntime` 或 stream Port；搜索结果只出现在任务设计/实施文档中。
2. 当前没有精确按 NodeAttempt 列出 ToolCall 的 repository port；Phase 1 no-tool 可避开，但 Tool Agent 接入前必须补齐。
3. 当前 `model_run` 没有 run kind 或 budget policy identity。数据库只能用兼容的全局 FSM；要让 SQL 精确执行 RAG-only grammar 或跨配置恢复同一 Attempt，必须增加不可变 discriminator/policy ref。
4. 当前 Eino Chat hardened transport 只接受完整 JSON 响应，不能直接用于 SSE Stream；Phase 1 只能定义/验证 recording contract，Phase 2 必须补真实 stream-safe transport 后才能宣称 end-to-end Stream 可用。
5. 本次为只读研究，未修改产品代码、未运行测试或迁移；仅新增本研究文件。
