# Research: Agent-owned scoped ports for Tools GORM

- Query: Agent owner 为 Tools GORM 提供哪些最小 scoped ports，才能保持 Workspace Analysis Tool Operation、预算、拒绝、durable recovery 与 authority 的 legacy 行为，同时消除 Tools 对 `agent.*` 的直接 SQL？
- Scope: internal
- Date: 2026-08-21

## Findings

### 1. 结论

Agent Application 应新增一个只依赖 `context.Context`、`foundation.TransactionScope`、
`foundation` 和 Agent Domain 值的文件：

```text
internal/agent/application/scoped_workspace_analysis_tool.go
```

其中冻结三组 Port 名称：

```go
type ScopedWorkspaceAnalysisToolParticipant interface { ... }
type ScopedWorkspaceAnalysisToolRefusalStore interface { ... }
type ScopedWorkspaceAnalysisToolAuthorityReader interface { ... }
```

不能把 Tools 的 `domain.ToolCall`、`domain.ResultReceipt`、Tools error code 类型或 GORM/sql/pgx
类型放进这些签名。依赖方向必须保持：

```text
Tools Application/Adapter
  -> Agent Application Port + Agent Domain values
  -> Foundation TransactionScope

Agent Application
  -/-> Tools Application/Domain
```

Tools 继续拥有唯一 outer UoW、Tool Call/receipt/failure mutation、Event/Audit append、
`SET CONSTRAINTS ALL IMMEDIATE` 和 commit response-loss recovery。Agent scoped Adapter 只在传入 scope
中锁/读/改 Agent-owned facts，不 begin、commit、rollback，也不回退到 root GORM。

最关键的接口形状必须是多阶段，而不是一个“Agent 授权 Tool Call”的大方法：

```text
Workflow lock/read
  -> Agent prepare/lock Operation
  -> Tools lock or insert Tool Call
  -> Agent reserve or settle
```

原因是 `agent.workspace_analysis_operation.tool_call_id` 与
`agent.workspace_analysis_budget_reservation.tool_call_id` 都是 immediate FK；Tool Call 尚未插入时，
Agent 无法合法写 STARTED Operation 或 Reservation
（`migrations/00085_workspace_analysis_persistence.sql:347-348`、`:465-472`）。

### 2. 建议冻结的 Application DTO 与方法签名

以下签名是可直接实施的最小合同。所有结构体都应增加简洁中文注释和 `Validate`；时间返回值统一 UTC
微秒。DTO 不应实现或携带 transaction handle。

#### 2.1 共享身份与锁定快照

```go
// WorkspaceAnalysisToolExecutionIdentity 是 Tools 从当前 Workflow Attempt 恢复的服务端身份。
type WorkspaceAnalysisToolExecutionIdentity struct {
    WorkspaceID       foundation.ID
    DefinitionID      foundation.ID
    DefinitionVersion int64
    DefinitionHash    string
    WorkflowRunID     foundation.ID
    NodeKey           domain.WorkspaceAnalysisOperationNodeKey
    NodeRunID         foundation.ID
    NodeAttemptID     foundation.ID
    LeaseOwner        string
    LeaseFence        int64
}

// WorkspaceAnalysisToolOperationSnapshot 是当前 scope 中已锁定并完成 Agent 领域校验的值快照。
type WorkspaceAnalysisToolOperationSnapshot struct {
    Run          domain.WorkspaceAnalysisRun
    Operation    domain.WorkspaceAnalysisOperation
    Reservation *domain.WorkspaceAnalysisBudgetReservation
    DatabaseNow  time.Time
}
```

身份形状与现有 Model identity 相同
（`internal/agent/application/workspace_analysis_model_execution.go:101-137`），但不要为了本 Port
重命名既有公开 Model 类型。先增加 Tool-specific value，并抽取包内私有的 ID/hash/lease validator，避免两个
`Validate` 漂移。

快照规则：

- `PENDING` Operation 的 `Reservation=nil`；
- `STARTED/SUCCEEDED/FAILED/UNKNOWN` 必须带经过
  `ValidateWorkspaceAnalysisBudgetReservation` 和 binding 校验的 Reservation；
- Operation/Reservation 只使用 Agent Domain 类型，不投影 Tool Call；Tool Call ID 已在 typed call ref 中；
- `DatabaseNow` 由 Agent 在取得 Agent locks 后用 `clock_timestamp()` 读取，只供 live admission；durable
  verifier 不用它重新判断 deadline。

#### 2.2 Participant：prepare/lock -> Tools Call -> reserve/settle

```go
// PrepareWorkspaceAnalysisToolOperationCommand 只为授权入口创建或锁定 logical Operation。
type PrepareWorkspaceAnalysisToolOperationCommand struct {
    Identity                WorkspaceAnalysisToolExecutionIdentity
    OperationKey            domain.WorkspaceAnalysisOperationKey
    CandidateOperationID    foundation.ID
    RequestHash             string
    ExpectedToolCatalogHash string
}

// WorkspaceAnalysisToolCallLockQuery 用 Tool-owned Call ID 锁定既有 Agent closure。
type WorkspaceAnalysisToolCallLockQuery struct {
    Identity    WorkspaceAnalysisToolExecutionIdentity
    ToolCallID  foundation.ID
    RequestHash string
}

// ReserveWorkspaceAnalysisToolOperationCommand 在 Tool Call 已插入后建立 Agent runtime facts。
type ReserveWorkspaceAnalysisToolOperationCommand struct {
    Identity                 WorkspaceAnalysisToolExecutionIdentity
    OperationKey             domain.WorkspaceAnalysisOperationKey
    OperationID              foundation.ID
    CandidateReservationID   foundation.ID
    ToolCallID               foundation.ID
    RequestHash              string
    ExpectedRunVersion       int64
    ExpectedOperationVersion int64
}

type WorkspaceAnalysisToolSettlementKind string

const (
    WorkspaceAnalysisToolSettlementSucceeded      WorkspaceAnalysisToolSettlementKind = "SUCCEEDED"
    WorkspaceAnalysisToolSettlementFailed         WorkspaceAnalysisToolSettlementKind = "FAILED"
    WorkspaceAnalysisToolSettlementReceiptInvalid WorkspaceAnalysisToolSettlementKind = "RECEIPT_INVALID"
    WorkspaceAnalysisToolSettlementUnknown        WorkspaceAnalysisToolSettlementKind = "UNKNOWN"
)

// SettleWorkspaceAnalysisToolOperationCommand 在 Tools 已完成 Call/receipt mutation 后归约 Agent facts。
type SettleWorkspaceAnalysisToolOperationCommand struct {
    Identity                 WorkspaceAnalysisToolExecutionIdentity
    OperationKey             domain.WorkspaceAnalysisOperationKey
    OperationID              foundation.ID
    ReservationID            foundation.ID
    ToolCallID               foundation.ID
    RequestHash              string
    ExpectedRunVersion       int64
    ExpectedOperationVersion int64
    Kind                     WorkspaceAnalysisToolSettlementKind
    Result                   *domain.WorkspaceAnalysisOperationResultRef
    ErrorCode                string
    CompletedAt              time.Time
}

// AdvanceWorkspaceAnalysisToolOperationAttemptCommand 只推进 terminal replay 的 latest Attempt。
type AdvanceWorkspaceAnalysisToolOperationAttemptCommand struct {
    Identity                 WorkspaceAnalysisToolExecutionIdentity
    OperationKey             domain.WorkspaceAnalysisOperationKey
    OperationID              foundation.ID
    ToolCallID               foundation.ID
    ExpectedOperationVersion int64
}

// WorkspaceAnalysisToolClosureQuery 精确定位一次可恢复的 Agent durable closure。
type WorkspaceAnalysisToolClosureQuery struct {
    WorkspaceID   foundation.ID
    WorkflowRunID foundation.ID
    AnalysisRunID foundation.ID
    OperationKey  domain.WorkspaceAnalysisOperationKey
    OperationID   foundation.ID
    ReservationID foundation.ID
    ToolCallID    foundation.ID
    RequestHash   string
}

type WorkspaceAnalysisToolClosure struct {
    Run         domain.WorkspaceAnalysisRun
    Operation   domain.WorkspaceAnalysisOperation
    Reservation domain.WorkspaceAnalysisBudgetReservation
}

type ScopedWorkspaceAnalysisToolParticipant interface {
    PrepareWorkspaceAnalysisToolOperationScoped(
        context.Context,
        foundation.TransactionScope,
        PrepareWorkspaceAnalysisToolOperationCommand,
    ) (WorkspaceAnalysisToolOperationSnapshot, error)

    LockWorkspaceAnalysisToolOperationByCallScoped(
        context.Context,
        foundation.TransactionScope,
        WorkspaceAnalysisToolCallLockQuery,
    ) (WorkspaceAnalysisToolOperationSnapshot, bool, error)

    ReserveWorkspaceAnalysisToolOperationScoped(
        context.Context,
        foundation.TransactionScope,
        ReserveWorkspaceAnalysisToolOperationCommand,
    ) (WorkspaceAnalysisToolOperationSnapshot, error)

    SettleWorkspaceAnalysisToolOperationScoped(
        context.Context,
        foundation.TransactionScope,
        SettleWorkspaceAnalysisToolOperationCommand,
    ) (WorkspaceAnalysisToolOperationSnapshot, error)

    AdvanceWorkspaceAnalysisToolOperationAttemptScoped(
        context.Context,
        foundation.TransactionScope,
        AdvanceWorkspaceAnalysisToolOperationAttemptCommand,
    ) (domain.WorkspaceAnalysisOperation, error)

    VerifyWorkspaceAnalysisToolClosureScoped(
        context.Context,
        foundation.TransactionScope,
        WorkspaceAnalysisToolClosureQuery,
    ) (WorkspaceAnalysisToolClosure, bool, error)
}
```

`Prepare...` 与 `Lock...ByCall` 不能合并：授权时只有 logical key，且允许
`INSERT ... ON CONFLICT DO NOTHING` 创建 PENDING；success/failure/unknown finalization 的现有 Tools command
只有 Tool Call/identity，没有 Operation ID/key，必须由 Agent owner 按 Call ID 找回并锁定 exact closure
（现有 command 见 `internal/tools/application/persistence.go:89-97`、`:127-133`、`:239-244`）。

各方法职责固定如下：

- `Prepare...`：锁 Analysis Run；按 logical key insert-and-lock Operation；锁可选 Reservation；验证
  Workspace/Workflow/Node/request/catalog binding。只有 PENDING 分支检查 frozen timeout + durable margin、Run
  budget 和 Agent active-operation concurrency；不得查询 Tool Call 表。
- `Lock...ByCall`：锁 Analysis Run -> exact Operation -> Reservation；不存在返回 `found=false`，部分事实或
  binding 损坏返回 consistency error；不得创建 PENDING。
- `Reserve...`：假定 Tools 已插入并锁定 STARTED Call；按 expected version 执行 Run reserved budget CAS ->
  Operation STARTED CAS -> Reservation insert。DB immediate FK 负责证明 Call 已存在。
- `Settle...`：假定 Tools 已将 Call 归约并已按需插入 receipt/failure；按 Reservation -> Run budget ->
  Operation 执行 CAS。`UNKNOWN` 必须 `UNKNOWN_CHARGED` 且 settled amount 等于 reserved；FAILED 和
  RECEIPT_INVALID 都 SETTLED；SUCCEEDED 必须使用 `TOOL_RESULT_RECEIPT` result ref。
- `Advance...Attempt`：只允许 terminal Operation，same Attempt 为 no-op，replacement Attempt 用
  expected version 更新 `latest_node_attempt_id`；不得修改预算或重新执行 Tool。
- `Verify...Closure`：独立 durable verifier。只核对 immutable scope、Run/Operation/Reservation、budget
  totals、call/result/error binding，不调用任何 live admission helper，不检查当前 lease/cancel/deadline/catalog。
  `found=false` 只表示完整目标 closure 不存在；发现 PENDING/partial/mismatch/corrupt row 必须返回 error，不能
  降级成未找到。

Settlement 用四值 kind 而不是任意 Operation status，是为了冻结唯一合法组合：

| Settlement kind | Tools-owned Call fact | Agent Operation | Reservation | Agent result/error |
| --- | --- | --- | --- | --- |
| `SUCCEEDED` | SUCCEEDED + receipt | SUCCEEDED | SETTLED | exact receipt ID/hash |
| `RECEIPT_INVALID` | SUCCEEDED + failure | FAILED | SETTLED | fixed `WORKSPACE_ANALYSIS_RECEIPT_INVALID` |
| `FAILED` | FAILED | FAILED | SETTLED | canonical Call error code |
| `UNKNOWN` | UNKNOWN | UNKNOWN | UNKNOWN_CHARGED | canonical unknown error code |

Agent Port 不自行读取 Call status；Tools 在调用前验证 Tool-owned facts，最终 cross-owner closure 由 deferred
trigger 与 outer `SET CONSTRAINTS ALL IMMEDIATE` 再证明。数据库函数现有矩阵见
`migrations/00085_workspace_analysis_persistence.sql:3771-3848`。

#### 2.3 Refusal store：live record 与 durable exact load 分离

```go
type RecordWorkspaceAnalysisToolRefusalScopedCommand struct {
    Identity      WorkspaceAnalysisToolExecutionIdentity
    OperationKey  domain.WorkspaceAnalysisOperationKey
    RefusalID     foundation.ID
    AuditEventID  foundation.ID
    ErrorCode     string
}

type WorkspaceAnalysisToolRefusalQuery struct {
    WorkspaceID   foundation.ID
    WorkflowRunID foundation.ID
    AnalysisRunID foundation.ID
    NodeRunID     foundation.ID
    OperationKey  domain.WorkspaceAnalysisOperationKey
    RefusalID     foundation.ID
    AuditEventID  foundation.ID
    ErrorCode     string
}

type WorkspaceAnalysisToolRefusal struct {
    ID            foundation.ID
    WorkspaceID   foundation.ID
    AnalysisRunID foundation.ID
    WorkflowRunID foundation.ID
    NodeRunID     foundation.ID
    OperationKey  domain.WorkspaceAnalysisOperationKey
    AuditEventID  foundation.ID
    ErrorCode     string
    CreatedAt     time.Time
}

type ScopedWorkspaceAnalysisToolRefusalStore interface {
    RecordWorkspaceAnalysisToolRefusalScoped(
        context.Context,
        foundation.TransactionScope,
        RecordWorkspaceAnalysisToolRefusalScopedCommand,
    ) (WorkspaceAnalysisToolRefusal, bool, error)

    LoadWorkspaceAnalysisToolRefusalExactScoped(
        context.Context,
        foundation.TransactionScope,
        WorkspaceAnalysisToolRefusalQuery,
    ) (WorkspaceAnalysisToolRefusal, bool, error)
}
```

`Record...` 的 bool 表示 exact replay。它由 caller 先完成 Workflow live fence，然后 Agent 锁 Analysis Run，
完成 existing refusal exact load、no-operation proof 和 insert/replay；它不写 Audit。Tools 使用返回的 DB
`CreatedAt` 构造 deterministic Audit event，并在同一 scope 调
`audit.Recorder.RecordScoped`。Refusal 的 Audit FK 是 `DEFERRABLE INITIALLY DEFERRED`，因此 Agent row 可以先于
Audit append 写入（`migrations/00089_workspace_analysis_tool_refusal_audit.sql:3-41`）。

`Load...Exact` 专用于普通 replay 和 commit response-loss recovery：锁 Analysis Run，精确读取 Refusal，证明
logical slot 没有 Operation；不复用 live identity 检查。它返回 `AuditEventID`，但不直接查询 `ops.audit_event`。
Audit 当前没有 scoped Get，只有 scoped append
（`internal/audit/application/ports.go:29-40`）。最小恢复方案是 Tools 用 deterministic event 再调用
`Recorder.RecordScoped` 并要求 exact replay；这既证明 Audit 存在，又不新增 Audit Port
（`internal/audit/application/recorder.go:65-83`）。

允许的 refusal code 仍由 Tools pre-executor policy 决定；Agent command 只校验 canonical error-code shape，
migration CHECK 是最终 allowlist。把 Tools refusal enum 复制到 Agent 会建立第二业务事实源并造成 import-cycle
压力。

#### 2.4 Authority reader：Agent 返回闭合投影，Tools 组合 Call/receipt

```go
type WorkspaceAnalysisRunToolAuthorityQuery struct {
    WorkspaceID   foundation.ID
    WorkflowRunID foundation.ID
}

type WorkspaceAnalysisRunToolAuthority struct {
    AnalysisRunID foundation.ID
    WorkspaceID   foundation.ID
    WorkflowRunID foundation.ID
}

type WorkspaceAnalysisSuccessfulToolAuthorityQuery struct {
    WorkspaceID   foundation.ID
    WorkflowRunID foundation.ID
    OperationKey  domain.WorkspaceAnalysisOperationKey
}

type WorkspaceAnalysisSuccessfulToolAuthority struct {
    AnalysisRunID foundation.ID
    OperationID   foundation.ID
    ReservationID foundation.ID
    ToolCallID    foundation.ID
    ResultID      foundation.ID
    ResultHash    string
}

type WorkspaceAnalysisSourceReadCountQuery struct {
    WorkspaceID   foundation.ID
    WorkflowRunID foundation.ID
    AnalysisRunID foundation.ID
}

type WorkspaceAnalysisToolCandidateAuthorityQuery struct {
    WorkspaceID   foundation.ID
    WorkflowRunID foundation.ID
    CandidateID   foundation.ID
}

type WorkspaceAnalysisToolCandidateAuthority struct {
    CandidateID   foundation.ID
    AnalysisRunID foundation.ID
    CandidateHash string
    CitationRefs  []string
}

type ScopedWorkspaceAnalysisToolAuthorityReader interface {
    LoadWorkspaceAnalysisRunToolAuthorityScoped(
        context.Context,
        foundation.TransactionScope,
        WorkspaceAnalysisRunToolAuthorityQuery,
    ) (WorkspaceAnalysisRunToolAuthority, bool, error)

    LoadWorkspaceAnalysisSuccessfulToolAuthorityScoped(
        context.Context,
        foundation.TransactionScope,
        WorkspaceAnalysisSuccessfulToolAuthorityQuery,
    ) (WorkspaceAnalysisSuccessfulToolAuthority, bool, error)

    CountWorkspaceAnalysisSourceReadOperationsScoped(
        context.Context,
        foundation.TransactionScope,
        WorkspaceAnalysisSourceReadCountQuery,
    ) (int, error)

    LoadWorkspaceAnalysisToolCandidateAuthorityScoped(
        context.Context,
        foundation.TransactionScope,
        WorkspaceAnalysisToolCandidateAuthorityQuery,
    ) (WorkspaceAnalysisToolCandidateAuthority, bool, error)
}
```

`Load...SuccessfulToolAuthority` 内部必须证明 Operation=SUCCEEDED、CallKind=TOOL、
Reservation=SETTLED、result kind=`TOOL_RESULT_RECEIPT` 以及 Agent 全部 exact binding；Tools 再按返回的
ToolCallID/ResultID/ResultHash 读取并验证自身 Call/receipt。不要把完整 Analysis Run、Candidate document 或
Reservation DTO 暴露给 Tools。

`Count...SourceRead` 保持 legacy 的“全部 SOURCE_READ logical slots 数量”语义，而不是只数成功 receipt；当前
合成 authority 用它检测多余/缺失 operation
（`internal/tools/adapter/postgres/workspace_analysis_authority.go:156-168`）。固定 v1 上限为 3，Adapter 读到
超界值应 consistency fail closed。

Candidate reader 应在 Agent 内复用 strict Candidate closure，解码 document 并只返回 hash + 有界
Citation refs，避免 Candidate 正文跨 owner。现有 root Agent authority 已要求 Candidate 与 Synthesis
Operation/Reservation/Model Run/Call 闭合
（`internal/agent/application/workspace_analysis_candidate_authority.go:11-35`）；Tools legacy 目前只做较弱的
Candidate -> Analysis join（`internal/tools/adapter/postgres/workspace_analysis_authority.go:451-493`），新 scoped
实现应采用 Agent 已有强合同。

四个 authority 方法都在 Tools caller-owned 默认隔离 read transaction 中执行，不升级为
RepeatableRead/ReadOnly，不 begin/commit；固定 Search + 1..3 ReadSource 循环是有界 fan-out，可以保留。它们不
获取 Workflow/Tool locks，也不使用 Preload/N+1。

### 3. 固定锁序与事务所有权

#### 3.1 首次授权

```text
Tools outer UoW: Begin
  Workflow ScopedWorkspaceAnalysisExecutionFence
    -> workflow.run FOR UPDATE
    -> workflow.node_run FOR UPDATE
    -> workflow.node_attempt FOR UPDATE
  Agent Prepare...Scoped
    -> agent.workspace_analysis_run FOR UPDATE
    -> INSERT PENDING operation ON CONFLICT DO NOTHING
    -> agent.workspace_analysis_operation FOR UPDATE
    -> optional existing reservation FOR UPDATE
    -> clock_timestamp()
  Tools
    -> verify Tool-owned STARTED-call concurrency
    -> INSERT/lock workflow.tool_call STARTED
  Agent Reserve...Scoped
    -> Analysis Run reserved-budget CAS
    -> Operation PENDING -> STARTED CAS
    -> INSERT RESERVED reservation
  Events AppendScoped
  Tools SET CONSTRAINTS ALL IMMEDIATE
Tools outer UoW: Commit/Rollback
```

“Reservation 在 Tool Call 之前”的固定锁序适用于已存在事实。首次授权没有 Reservation 行可锁，而且
immediate FK 强制先插入 Tool Call 再插 Reservation；这不是锁序逆转。相同 Analysis Run 的并发由最先取得的
Analysis Run row lock 串行化，Agent partial unique active-operation index 和 Tools-owned active Call check 分别
保护各 owner 事实。legacy 当前把全部动作放在一个 repository 中，顺序可见于
`internal/tools/adapter/postgres/workspace_analysis_operations.go:394-449`、`:474-512`。

#### 3.2 existing STARTED、replacement 与 terminal replay

```text
Workflow Run -> Node Run -> Attempt
  -> Agent Run -> Operation -> Reservation
  -> Tools exact Tool Call FOR UPDATE
```

- same Attempt STARTED：返回 reconcile；不得 settle Unknown；requested Event exact replay。
- replacement Attempt STARTED：Tools 先 CAS old Call -> UNKNOWN，再调用 Agent `Settle...UNKNOWN`，随后
  completed Event；不得创建第二次外部调用。legacy 行为见
  `internal/tools/adapter/postgres/workspace_analysis_operations.go:92-121`、`:558-581`。
- terminal：Tools 验证 exact receipt/failure 或 terminal Call，Agent 只执行 `Advance...Attempt`，Event exact
  replay；不得重复结算。legacy terminal attempt CAS 见同文件 `:635-646`。

#### 3.3 success / receipt-invalid / failed / unknown

所有 live finalization 使用：

```text
Workflow locks
  -> Agent Lock...ByCall (Run -> Operation -> Reservation)
  -> Tools Tool Call FOR UPDATE + Call CAS + receipt/failure insert
  -> Agent Settle...
  -> Event AppendScoped
  -> Tools SET CONSTRAINTS ALL IMMEDIATE
  -> outer Commit
```

legacy success 事务和 exact binding 在
`internal/tools/adapter/postgres/result_receipts.go:73-151`、`:209-319`、`:408-500`；failure 与
commit recovery 在 `internal/tools/adapter/postgres/result_receipt_failures.go:20-103`、`:171-195`、`:277-305`。

`SET CONSTRAINTS ALL IMMEDIATE` 必须留在 Tools outer repository，并且晚于 Agent settle 与 Event/Audit
append。数据库 deferred triggers 同时检查 budget totals、operation/reservation 和 Tool receipt closure
（`migrations/00085_workspace_analysis_persistence.sql:4716-4764`）；任何 owner 在内部提前执行都会在闭包尚未
组合完成时误报。

#### 3.4 commit response-loss

Tools 在 callback 成功但 commit 返回错误后，从 root 开一个新 UoW：

```text
Workflow raw immutable binding snapshot
  -> Agent Verify...ClosureScoped / Refusal Load...ExactScoped
  -> Tools exact Call + receipt/failure
  -> Event/Audit deterministic AppendScoped exact replay
  -> commit recovery read transaction
```

恢复严禁重用 Workflow live fence、Agent `Prepare`、budget/deadline admission 或当前 policy；否则一个已经提交
但随后 lease/cancel/deadline 改变的事实会被错误判成未提交。Workspace Analysis 合同明确要求 commit unknown
先 exact lookup/reconcile，未证明前不得返回输出
（`.trellis/spec/backend/workspace-analysis-contract.md:54-57`、`:147-150`）。

当前 Workflow 已交付的 `ScopedWorkspaceAnalysisExecutionFence` 是 live lock/fence，只返回定义、Run、Node、
Attempt 快照（`internal/workflow/application/scoped_workspace_analysis_execution_fence.go:11-60`）。Tools durable
recovery 还需要 Workflow owner 的 raw immutable snapshot 能力；不能让 Agent verifier 越权查询 `workflow.*`。

### 4. 最小 Adapter 文件拆分

建议把方法直接实现到现有 `*postgres.GORMRepository`；这些都是 caller-scoped Agent owner 方法，不需要再建
一个持有 Workflow Port 的 public repository 或第二个 Pool：

```text
internal/agent/application/scoped_workspace_analysis_tool.go
internal/agent/adapter/postgres/gorm_workspace_analysis_tool_participant.go
internal/agent/adapter/postgres/gorm_workspace_analysis_tool_refusals.go
internal/agent/adapter/postgres/gorm_workspace_analysis_tool_authority.go
internal/agent/adapter/postgres/gorm_workspace_analysis_records.go
```

职责：

- `...participant.go`：prepare/by-call lock、reserve、settle、attempt advance、durable verifier；
- `...refusals.go`：refusal record/exact load 与 no-operation proof；
- `...authority.go`：四个 bounded Agent-owned projection；
- `...records.go`：Run/Operation/Reservation/Refusal/Candidate 的私有 persistence records、显式 columns、
  scanner-to-domain 与 pure equality/closure helper。不要把 SQL UoW 或 owner orchestration放进该文件。

现有可直接复用：

- `internal/agent/adapter/postgres/gorm_core.go:17-42` 的单 Pool GORM root/UoW，`:74-97` 的 scope unwrap，
  `:117-207` 的 Raw Row/Rows、no-row/context/SQLSTATE helper；
- `internal/agent/adapter/postgres/gorm_workspace_analysis_runs.go` 的
  `workspaceAnalysisRunColumns` / `scanWorkspaceAnalysisRun`；
- Agent Domain 的 `ValidateWorkspaceAnalysisOperation`、call/result binding、replay disposition
  （`internal/agent/domain/workspace_analysis_operation.go:265-369`）；
- Reservation/Run totals validators
  （`internal/agent/domain/workspace_analysis_persistence.go:303-404`）；
- existing Candidate authority 的 strict closure/scanner；抽取纯 scanner/validation，GORM SQL 仍使用 `?`
  placeholder，不复用 pgx `$n` SQL string。

现有 legacy Model operation record 只有 `modelCallID`，其 `domain()` 也只生成 Model call ref
（`internal/agent/adapter/postgres/workspace_analysis_model_operations.go:53-69`、`:897-915`）。不要为 Tool
复制第二套领域映射；`gorm_workspace_analysis_records.go` 应一次支持 nullable model/tool call IDs，并由未来
Model GORM 阶段共同使用。legacy pgx 文件暂不为本 Port 做大规模机械重构。

新增文件末尾加静态断言：

```go
var _ application.ScopedWorkspaceAnalysisToolParticipant = (*GORMRepository)(nil)
var _ application.ScopedWorkspaceAnalysisToolRefusalStore = (*GORMRepository)(nil)
var _ application.ScopedWorkspaceAnalysisToolAuthorityReader = (*GORMRepository)(nil)
```

### 5. Files found

- `.trellis/tasks/08-19-gorm-tools-migration/design.md`：冻结 owner prerequisites、锁序、事务组合、恢复与
  authority 分工，重点 `:35-68`、`:152-235`。
- `.trellis/tasks/08-19-gorm-agent-migration/design.md`：Agent staged 边界、Workflow fence、Workspace Analysis
  lock/closure 约束，重点 `:45-59`、`:203-230`。
- `internal/tools/adapter/postgres/workspace_analysis_operations.go`：legacy authorization、replacement、预算和
  attempt-advance 行为基线。
- `internal/tools/adapter/postgres/result_receipts.go`：success receipt UoW、固定锁序、settlement 与 recovery。
- `internal/tools/adapter/postgres/result_receipt_failures.go`：receipt-invalid closure 和 recovery。
- `internal/tools/adapter/postgres/workspace_analysis_refusals.go`：deterministic refusal、Audit binding 与 recovery。
- `internal/tools/adapter/postgres/workspace_analysis_authority.go`：六个 Tools authority reader 的 Agent/Tool
  跨表查询基线。
- `internal/agent/application/workspace_analysis_model_execution.go`：现有 Agent execution identity、operation
  repository 与错误合同。
- `internal/agent/application/workspace_analysis_candidate_authority.go`：严格 Candidate authority contract。
- `internal/agent/domain/workspace_analysis_operation.go`：logical operation、typed call/result、transition/replay
  validators。
- `internal/agent/domain/workspace_analysis_persistence.go`：Run budget 与 Reservation domain/validators。
- `internal/workflow/application/scoped_workspace_analysis_execution_fence.go`：现有 caller-scoped Workflow live
  fence value contract。
- `internal/workflow/adapter/postgres/gorm_execution_fence.go`：同 scope Workflow Run -> Node Run -> Attempt
  `FOR UPDATE` concrete 实现。
- `migrations/00085_workspace_analysis_persistence.sql`：operation/reservation immediate FK、state checks、closure
  functions/deferred triggers。
- `migrations/00089_workspace_analysis_tool_refusal_audit.sql`：Agent refusal、deferred Audit FK、insert guard 与
  append-only 约束。

### 6. Code patterns

- caller-owned scope Port 已有标准形状：
  `internal/agent/application/scoped_workspace_analysis.go:11-25`、
  `internal/workflow/application/scoped_workspace_analysis_execution_fence.go:52-60`。
- Workflow GORM fence 只 unwrap scope 并按 owner lock order 查询，不管理事务：
  `internal/workflow/adapter/postgres/gorm_execution_fence.go:36-55`、`:66-151`。
- legacy 固定锁序为 Workflow -> Analysis -> Operation -> Reservation -> Tool Call：
  `internal/tools/adapter/postgres/workspace_analysis_operations.go:394-449`；receipt 路径同序：
  `internal/tools/adapter/postgres/result_receipts.go:209-300`。
- successful settlement 固定 Reservation -> Run budget -> Operation：
  `internal/tools/adapter/postgres/result_receipts.go:408-476`；failed/unknown 对应实现见
  `internal/tools/adapter/postgres/workspace_analysis_operations.go:592-632`。
- refusal 不创建 Call/Operation/Reservation/budget，且与 Audit 同 UoW：
  `internal/tools/adapter/postgres/workspace_analysis_refusals.go:52-127`。
- authority 的 owner 分割点正好在 Agent successful operation projection 与 Tool Call/receipt load 之间：
  `internal/tools/adapter/postgres/workspace_analysis_authority.go:372-434`。
- Application/Domain 不泄漏数据库类型，显式列、参数化 Raw SQL 和数据库约束优先：
  `.trellis/spec/backend/database-guidelines.md:23-35`、`:55-63`。

### 7. External references

无。本研究只冻结仓库现有 Application contracts、legacy SQL、migration/trigger 事实和已批准的 Tools/Agent
design；没有引入或评估新的第三方 API/框架。

### 8. Related specs

- `.trellis/spec/backend/workspace-analysis-contract.md:78-106`：logical operation、Attempt/replacement、Unknown
  与预算合同。
- `.trellis/spec/backend/workspace-analysis-contract.md:133-155`：receipt invalid、commit unknown、same/replacement
  Attempt error matrix。
- `.trellis/spec/backend/workspace-analysis-contract.md:169-180`：PostgreSQL、response-loss 和 fixed lock-order
  验收要求。
- `.trellis/spec/backend/database-guidelines.md:27-35`：参数化、显式约束、事务与 N+1 规则。
- `.trellis/tasks/08-19-gorm-agent-migration/prd.md:40-47`：Agent Workspace Analysis scoped transition 与 owner
  boundary。
- `.trellis/tasks/08-19-gorm-agent-migration/prd.md:56-67`：GORM Raw/Exec、错误分类与 TODO 9 门禁。

## Caveats / Not Found

1. **Workflow durable recovery Port 缺口**：当前代码只有 live
   `ScopedWorkspaceAnalysisExecutionFence`。Tools design 要求 commit-loss 使用 raw immutable Workflow snapshot；
   该能力不能由 Agent 越权补，也不能用 live lease/cancel/deadline fence替代。
2. **Refusal/Operation 跨表互斥没有数据库唯一约束**：refusal path 会做 no-operation proof，但 legacy
   Operation prepare 没有反向 `NOT EXISTS refusal`。两条路径都先锁 Analysis Run，Agent 新 participant 应在创建
   PENDING 前增加 exact no-refusal proof；这是保护既有意图的 hardening，TODO 9 必须加入 refusal-vs-authorize
   并发 parity 场景。
3. **Tool concurrency 是分属两个 owner 的不变量**：Agent 只能检查 active Tool Operation/budget，Tools 只能
   检查 active Tool Call。二者必须在同一 Analysis Run serialization scope 中执行，并由 deferred closure 最终
   兜底；Agent Port 不得查询 `workflow.tool_call`。
4. **active foreign-Pool scope 当前无法识别**：scope 只能验证平台类型/lifetime，不能证明 Pool affinity；同池
   依赖 Composition 与 TODO 9 fixture，不能在 Port 中用反射/DSN 私有断言补洞。
5. **Audit scoped exact load 不存在**：推荐用 deterministic `Recorder.RecordScoped` exact replay 证明 Audit，而
   不是让 Agent 查询 `ops.audit_event`。若 Audit owner 拒绝这种 recovery 语义，需由 Audit task 新增 scoped Get，
   不能在 Agent/Tools 复制 Audit SQL。
6. **本任务尚无真实 PostgreSQL parity 证据**：Agent `implement.md:44-54` 的 Model Operation 阶段和
   `:92-103` 的 TODO 9 仍未完成。静态编译不能证明 immediate/deferred FK、deadlock、commit-loss 或 refusal
   race 行为。
