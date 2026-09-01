# Agent Repository GORM 迁移设计

## 1. 目标与阶段边界

Agent 持久化面同时包含 Model Run/Call、crash recovery、RAG Memory Snapshot、Workspace Analysis 多表状态机和 RAG Progress Event。它不是普通 CRUD，不能用 GORM association 或逐表 Save 机械改写。

本 child 形成四个 staged 阶段：

1. **Model Runtime**：Model Run/Call、Recovery 与 Capture 所需 `ScopedModelRunFinalizer`；
2. **Memory**：RAG Memory Snapshot 与 Model Run 的同事务双向绑定；
3. **Workspace Analysis**：Run/Capability scoped 过渡、Model Operation、Result/Candidate/Checkpoint/Authority；
4. **RAG Progress**：同一 UoW 内的 advisory lock 与 Events scoped append。

四阶段都不接入生产 Composition。TODO 9 前保留全部 legacy pgx 文件、构造和 `any` Port，不增加 selector/双写/fallback，不修改 migration、cmd 或测试。任一 staged 阶段都可通过删除对应新文件和新 scoped Port 回滚，生产路径不受影响。

## 2. Owner 与 Schema 事实源

| Owner | 表/事实 | 主要 migration |
| --- | --- | --- |
| Agent Runtime | `agent.model_run`、`agent.model_call` | `00018_agent_runtime.sql`，后续 `00020/00022/00041/00061/00066/00068/00074/00083/00085` 扩展 |
| Agent Memory | `agent.rag_memory_snapshot` | `00061_agent_rag_memory_context.sql` |
| Agent Workspace Analysis | run、operation、budget reservation、candidate、model result、publication/termination proof、worker capability | `00085_workspace_analysis_persistence.sql`、`00086/00087/00088/00089/00091` |
| Workflow | workflow run/node run/node attempt lease 与取消 fence | Agent 只在既有原子操作中按冻结合同读取/锁定，不取得 Schema owner |
| Events | `ops.server_event` append | Agent 只调用 `eventsapplication.ScopedAppender`，不复制 Event SQL |
| Capture | Profile Revision/Evidence/Attempt | Capture 通过 Agent scoped Port 在 Capture-owned transaction 中终结 Model Run |

本 child 不新增或改写 migration，不调用 GORM Schema API。Run/Call trigger、Workspace Analysis deferred constraints 和后续 hardening migration 均是唯一数据库事实源。

## 3. Staged 构造与公开 Port

### 3.1 GORM Repository

```go
NewGORMRepository(*platformpostgres.Pool) (*GORMRepository, error)
```

构造器从同一个 Pool 取得 `GORM()` 与 `UnitOfWork()`，拒绝 nil/无效 root、typed-nil UoW 和既有 GORM error。Repository 不接受 DSN、不自行打开连接。

`GORMRepository` 静态实现：

- `application.ModelRunRepository`
- `application.ScopedModelRunFinalizer`
- `application.RAGMemorySnapshotRepository`
- `application.WorkspaceAnalysisRunLoader`
- 新增的 scoped Workspace Analysis Run persistence
- Workspace Analysis capability lifecycle + scoped readiness

它不实现 legacy `ModelRunTxFinalizer(any)`、legacy Workspace Analysis `*Tx(any)` Port，防止新路径重新把 scope 放回弱类型接口。

Workspace Analysis Model Operation 使用单独构造：

```go
NewGORMWorkspaceAnalysisRepository(
    *platformpostgres.Pool,
    workflowapplication.ScopedWorkspaceAnalysisExecutionFence,
) (*GORMWorkspaceAnalysisRepository, error)
```

该 Repository 静态实现 `WorkspaceAnalysisModelOperationRepository`、`WorkspaceAnalysisRetrievalPlanCheckpointReader` 和 `WorkspaceAnalysisCandidateAuthorityReader`。Workflow fence 是必需依赖，构造器拒绝 nil/typed-nil；不得使用可选字段、默认实现或 Agent 内部 SQL fallback。两个 Agent Repository 可复用同包私有 scanner/query/error helper，但不互相持有或隐藏新事务。

### 3.2 Scoped Model Run

在 `internal/agent/application/scoped_model_run.go` 定义：

```go
type ScopedModelRunFinalizer interface {
    GetModelRunScoped(context.Context, foundation.TransactionScope, foundation.ID, foundation.ID, bool) (domain.ModelRun, error)
    GetModelRunRecordScoped(context.Context, foundation.TransactionScope, foundation.ID, foundation.ID, bool) (ModelRunRecord, error)
    FinalizeModelRunScoped(context.Context, foundation.TransactionScope, FinalizeModelRunCommand) (domain.ModelRun, bool, error)
}

type ScopedModelRunAttemptFinder interface {
    GetModelRunByAttemptScoped(context.Context, foundation.TransactionScope, foundation.ID, foundation.ID, bool) (domain.ModelRun, bool, error)
}

type ScopedModelRunStore interface {
    ScopedModelRunFinalizer
    ScopedModelRunAttemptFinder
}
```

Capture 只依赖 `ScopedModelRunFinalizer`；Artifact/Conversation 后续可依赖组合 `ScopedModelRunStore`，Organizing 可继续选择窄 Finalizer。GORM Adapter 只调用 `platformpostgres.GORMTransaction(scope)`，立即在 callback 内使用，不缓存 scope/transaction，不 begin/commit/rollback，不 fallback root。四个 scoped 方法与 legacy 共用纯 validation、scanner、binding comparison 和 error code，但 SQL 使用 `?` 参数。

### 3.3 Workspace Analysis scoped siblings

保留 legacy `WorkspaceAnalysisRunPersistence`、`WorkspaceAnalysisRunStarter` 和 Capability `Require...Tx(any)`。新增：

```go
type ScopedWorkspaceAnalysisRunPersistence interface {
    InsertWorkspaceAnalysisRunScoped(context.Context, foundation.TransactionScope, domain.WorkspaceAnalysisRun) (domain.WorkspaceAnalysisRun, error)
    FindWorkspaceAnalysisRunScoped(context.Context, foundation.TransactionScope, foundation.ID, foundation.ID) (domain.WorkspaceAnalysisRun, bool, error)
}

type ScopedWorkspaceAnalysisRunStarter interface {
    StartWorkspaceAnalysisRunScoped(context.Context, foundation.TransactionScope, WorkspaceAnalysisRunStartCommand) (domain.WorkspaceAnalysisRun, error)
}

type ScopedWorkspaceAnalysisReadiness interface {
    RequireWorkspaceAnalysisWorkerReadyScoped(context.Context, foundation.TransactionScope, WorkspaceAnalysisCapabilityContract) error
}
```

新增 `ScopedWorkspaceAnalysisRunService` 实现 scoped starter，并新增 `ScopedWorkspaceAnalysisCapabilityCheckedRunStarter` 作为唯一组合入口。后者的构造固定接收 `ScopedWorkspaceAnalysisReadiness`、`ScopedWorkspaceAnalysisRunStarter` 和冻结 contract；其 `StartWorkspaceAnalysisRunScoped(ctx, scope, command)` 必须按以下顺序执行：

```text
RequireWorkspaceAnalysisWorkerReadyScoped(ctx, scope, contract)
-> delegate.StartWorkspaceAnalysisRunScoped(ctx, scope, command)
```

两步必须收到同一个 scope，组合器不 begin/commit/rollback，也不允许 consumer 自行拆开编排。两套 scoped Service 复用既有纯构造与 replay 校验，不复制领域规则。`WorkspaceAnalysisCapabilityService` 的三项 Store-owned lifecycle dependency 收窄为不含 legacy transaction 方法的 lifecycle Port；legacy Repository 继续兼容。

该 sibling 只为后续 Conversation owner 迁移准备，不在本 child 改 Conversation。Final 前两套 Port 并存，但同一业务路径只能选择一套，禁止混用 pgx Tx 与 GORM Scope。

### 3.4 RAG Progress

```go
NewGORMRAGProgressStore(
    *platformpostgres.Pool,
    eventsapplication.ScopedAppender,
) (*GORMRAGProgressStore, error)
```

Store 从 Pool 取得 UoW；一次 `RecordRAGProgress` 只开启一个默认隔离事务，在 scope 中取 GORM tx、获取 advisory lock、读取既有 occurred_at、调用 Event `AppendScoped`，由 UoW 唯一决定 commit/rollback。

### 3.5 Workflow scoped execution fence

Workflow owner 在 `internal/workflow/application` 提供稳定 Port：

```go
type ScopedWorkspaceAnalysisExecutionFence interface {
    LockWorkspaceAnalysisExecutionScoped(
        context.Context,
        foundation.TransactionScope,
        WorkspaceAnalysisExecutionFenceRequest,
    ) (WorkspaceAnalysisExecutionFenceSnapshot, bool, error)
}
```

Request 固定包含 Workspace ID、Workflow Run ID、Node Run ID 和 Node Attempt ID。Snapshot 固定返回 Definition ID/Key/Version/Graph、Workflow status/cancel request、Node key/status/attempt/lease owner/lease，以及 Attempt status/number/lease owner/lease。Workflow 实现必须在同一 caller-owned scope 内按 Run -> Node Run -> Node Attempt 执行 `FOR UPDATE`，保持 Workspace/binding 校验；它不得锁 Agent 表、读取 Agent 预算、取得 DB clock，也不得 begin/commit/rollback。

错误合同固定如下：

- 任一 Workflow Run/Node Run/Node Attempt 缺失，或 request 的 Workspace/父子 binding 不匹配时，返回零快照、`found=false`、`err=nil`；不得用 Workflow not-found 错误代替；
- request 无效时返回可识别的非重试 invalid error；Agent 在调用前也必须验证 request，并映射为现有 command-invalid；
- nil、非平台或失效 scope 返回可识别的 dependency-unavailable error；context cancel/deadline 同时保留 sentinel 与 custom cause；数据库错误保留底层 SQLSTATE cause chain；
- Agent 收到 `found=false`，或快照的 status/cancel/lease/definition 与 command 不匹配时，统一映射现有 `AGENT_WORKSPACE_ANALYSIS_MODEL_AUTHORIZATION_INVALID` conflict；
- Agent 不透传 Workflow 公开错误码。scope/未知依赖故障映射 `AGENT_WORKSPACE_ANALYSIS_MODEL_AUTHORIZATION_UNAVAILABLE`；context 与 `40001/40P01/55P03` 等 SQLSTATE 按 Agent 既有 code/retryability 重新分类并保留 cause chain。

Agent 调用 fence 后才按既有顺序锁自己的 Analysis/Operation/Reservation/Model Run/Call，最后读取 `clock_timestamp()`。Port 返回的是不可变快照，不泄漏 GORM/sql/pgx/`any`。在 Workflow child 交付该 Port 前，Agent 的 Workspace Analysis Model Operation 阶段不得实现或声称可验收；其他 Agent staged 阶段可独立推进。

### 3.6 Tools owner scoped participant/refusal/authority Ports

Tools 继续拥有 `workflow.tool_call` 和 receipt 表，但 Agent 拥有 Workspace Analysis Run、Operation、Budget
Reservation、Tool Refusal、Candidate 与 Model Result。为避免 Tools 复制 Agent SQL，在
`internal/agent/application` 增加以下纯 Application 合同；所有方法接收 caller-owned
`foundation.TransactionScope`，不 begin/commit/rollback、不缓存 scope，也不接收 Tools/GORM/sql/pgx/`any`。

共同身份和值：

```go
type WorkspaceAnalysisToolExecutionIdentity struct {
    WorkspaceID foundation.ID
    DefinitionID foundation.ID
    DefinitionVersion int64
    DefinitionHash string
    WorkflowRunID foundation.ID
    NodeKey string
    NodeRunID foundation.ID
    NodeAttemptID foundation.ID
    LeaseOwner string
    LeaseFence int64
}

type WorkspaceAnalysisToolParticipantSnapshot struct {
    Run domain.WorkspaceAnalysisRun
    Operation domain.WorkspaceAnalysisOperation
    Reservation *domain.WorkspaceAnalysisBudgetReservation
    DatabaseNow time.Time
}
```

Participant 生命周期拆为五个边界，保证 Tools 写入 Call 前后锁序不变：

```text
Prepare...Scoped: Analysis Run FOR UPDATE -> Operation INSERT ON CONFLICT + FOR UPDATE -> Reservation FOR UPDATE (if present)
Reserve...Scoped: Run reserved budget CAS -> Operation STARTED CAS -> Reservation INSERT RESERVED
Settle...Scoped: Reservation SETTLED/UNKNOWN_CHARGED -> Run budget CAS -> Operation terminal CAS
Advance...AttemptScoped: terminal Operation latest_attempt CAS
Verify...ClosureScoped: durable-only immutable closure readback; no live lease/cancel/deadline check
```

`Prepare` 返回现有状态，Tools 据此执行 exact replay/reconcile/replacement；`Reserve` 接收已持久化
Tool Call ID、Reservation ID、expected versions 和 Agent-owned reserved amount；`Settle` 接收
Tool Call ID、terminal outcome、settled amount/result reference/error code/completion time；所有 CAS
零行和 deferred constraint 错误保持 Agent 原错误类别。UNKNOWN 必须按 Reservation 全额计费，replacement
不得在同一 Attempt 伪造新事实。

Refusal Port 只写 `agent.workspace_analysis_tool_refusal`：

```go
RecordWorkspaceAnalysisToolRefusalScoped(ctx, scope, command) (domain.WorkspaceAnalysisToolRefusal, bool, error)
LoadWorkspaceAnalysisToolRefusalScoped(ctx, scope, query) (domain.WorkspaceAnalysisToolRefusal, bool, error)
```

Command 显式包含 Refusal ID、Workflow/Analysis/Node identity、logical OperationKey、稳定 ErrorCode 和
Audit Event ID。Agent 只校验 no-operation proof、唯一 logical slot、binding 与 exact replay；Audit 由
Tools 在同一 scope 调用 `RecordScoped`，不得让 Agent 自己打开 Audit/Events 事务。

Authority Reader 只暴露最小、已验证的不可变投影：Analysis Run identity；成功 Tool Operation 的
Operation/Reservation/Call/Result IDs+hash；精确 `SOURCE_READ` 数量；Candidate ID/AnalysisRunID/hash/
CitationRefs。所有查询 Workspace-scoped、bounded、fail-closed，正文 JSON/receipt output 不穿过 Agent
Port。Tools 再与自己拥有的 receipt closure 组合，不得重新查询 Agent 表。

Agent GORM Adapter 可按 `gorm_workspace_analysis_tool.go`、`gorm_workspace_analysis_tool_refusals.go`、
`gorm_workspace_analysis_tool_authority.go` 拆分；不能把这些 owner 方法塞进 Tools Adapter，也不能在
Agent 侧复制 Workflow fence SQL。跨 Pool active scope 无法由 Foundation 识别，继续作为同 Pool Composition/TODO9
硬约束记录。

## 4. 文件与复用策略

建议按职责拆分，而不是再建立单个超大文件：

- `gorm_core.go`：构造、ready/context、UoW、Raw Row/Rows、防御与 error classifier；
- `gorm_model.go` / `gorm_queries.go`：显式 scanner/carrier、固定列序、参数化 SQL；
- `gorm_repository.go`：Model Run create/get；
- `gorm_calls.go` / `gorm_runs.go` / `gorm_recovery.go`：Call、Run、scoped 与 crash recovery；
- `gorm_memory_snapshots.go`：Memory Snapshot；
- `gorm_workspace_analysis_runs.go` / `gorm_workspace_analysis_capability.go`：Run/Capability 与 scoped siblings；
- `gorm_workspace_analysis_core.go`：独立构造、必需 Workflow fence 与共享 Pool/UoW；
- `gorm_workspace_analysis_model_operations.go` 及按 Agent-owned lock/scan/mutate/recovery 分拆的私有 helper 文件；
- `gorm_workspace_analysis_tool.go`、`gorm_workspace_analysis_tool_refusals.go`、`gorm_workspace_analysis_tool_authority.go`：Tools participant、拒绝事实与最小 authority projection；
- `gorm_workspace_analysis_candidate_authority.go`：bounded authority read；
- `gorm_rag_progress.go`：Events scoped append。

优先把 legacy/GORM 可共享的纯 validation、stable equality、receipt/result codec、column list 和 `Scan(...any) error` helper 收敛为单一事实源。不得建立一个用 `any` 同时模拟 pgx/GORM 的 DB abstraction，也不得让 GORM Repository 持有 legacy Repository 作为 fallback。

## 5. Model Run/Call 与 scoped 事务

### 5.1 创建与读取

- Create Run 保持 `ON CONFLICT DO NOTHING`，按 Node Attempt 读取 winner，并比较全部不可变 binding 后才 exact replay；
- Start Call 保持 parent Workspace 过滤、`(model_run_id,call_no)`、phase/active-call 约束和 exact replay；
- Get Record 先读取 Workspace-scoped Run，再按 `call_no,id` 稳定读取全部 Calls，Rows 必须 Close/Err；
- UUID、nullable、枚举、时间、memory/retrieval/model provenance 均显式 scan 并经 Domain validation，损坏行 fail closed。

### 5.2 CAS 与 replay

- Complete Call 只更新 STARTED + expected version，RowsAffected=0 后按 Workspace/ID 回读；只有完整结果相同返回 replay；
- Finalize Run 只更新 RUNNING + expected version，RowsAffected=0 后按 Workspace/ID 回读；只有完整终态相同返回 replay；
- scoped Get/Record/ByAttempt/Finalize 使用 caller tx；`forUpdate` 只锁 Run，Calls 仍按稳定顺序读取，与 legacy 一致；ByAttempt 无行返回 `found=false`；
- Run trigger 禁止有 STARTED Call 时终结，GORM 不在应用层绕过或重排该约束。

### 5.3 Recovery

Call/Run recovery 保留单条 CTE、`FOR UPDATE ... SKIP LOCKED`、limit 1..500、caller `Before/At`、UNKNOWN code 和稳定排序。不得把候选查询和逐行更新拆开，避免双 worker 重复归约。

## 6. RAG Memory Snapshot

保持以下事务：

```text
Snapshot FOR UPDATE
-> 验证 PREPARING claimant / Workflow+Node+Attempt binding
-> Model Run INSERT（必须新建）
-> Snapshot CAS READY + memory context + Model Run ID
-> 同 transaction readback 双向 binding
-> commit
```

Rollback 使用独立清理 context，不能因 caller cancellation 留下事务。`updated_at=GREATEST(clock_timestamp(),created_at)` 保持 DB time。Begin claimant、FAILED exact replay、cross-Workspace/attempt uniqueness 和 model_run/snapshot 双向 FK 均保持。

## 7. Workspace Analysis

### 7.1 Run 与 Capability

Run 的 scoped insert/find 只参与 Conversation caller-owned transaction，不自行提交。Replay 继续按 Question 与冻结 config/budget/deadline binding 逐字段比较。Conversation 后续只消费 `ScopedWorkspaceAnalysisCapabilityCheckedRunStarter`，由它在同一 scope 固定执行 readiness -> Run；不得分别调用两个底层 Port。

Capability Advertise/Heartbeat/Release 使用 Store-owned 短 UoW 和 `clock_timestamp()`；Ready scoped check 在 Conversation scope 中执行 exact contract EXISTS。lease interval 仍限制 10..60 秒，released/stale/contract drift fail closed。

### 7.2 Model Operation 固定锁序

每次授权/终结/恢复必须在一个 UoW 内依次锁定：

```text
Workflow ScopedWorkspaceAnalysisExecutionFence
  -> workflow.run FOR UPDATE
  -> workflow.node_run FOR UPDATE
  -> workflow.node_attempt FOR UPDATE
-> agent.workspace_analysis_run FOR UPDATE
-> agent.workspace_analysis_operation FOR UPDATE/insert-and-lock
-> budget reservation FOR UPDATE
-> model_run FOR UPDATE
-> model_call FOR UPDATE
-> SELECT clock_timestamp()
```

前三个锁只能由 Workflow owner 的 fence 在同一 scope 内取得；Agent GORM Adapter 不直接查询 `workflow.*`。不能把任一 advisory/row lock 放到 root GORM DB，也不能为了 ORM 简化改变顺序。授权保持 Operation/Run/Call/Reservation/Analysis Budget CAS；终结保持 Call -> Run -> Reservation -> Analysis Budget -> Operation 的归约顺序。UNKNOWN 继续按 reservation 全额结算。

成功/失败/拒绝/UNKNOWN、replacement attempt、deadline/cancel fence、result/candidate closure 后都执行 `SET CONSTRAINTS ALL IMMEDIATE`。commit error 继续开启新事务按 durable binding 恢复；恢复失败不得猜测成功。

### 7.3 读取闭包

Result、Candidate、Retrieval Plan Checkpoint 和 Candidate Authority 的多表 JOIN 保持固定参数化 Raw SQL。`00085` 将 Result/Candidate 的 canonical JSON document 定义为带 hash/bytes 约束的 `bytea`；GORM 必须按 `[]byte` 绑定和扫描，再执行 canonical/hash/bytes/schema/domain 校验，不得误用 JSONB carrier 或 `::text` 改变字节事实。所有读取保持 Workspace predicate 和 bounded 单行语义，无 Preload/N+1。

## 8. RAG Progress/Event 原子性

`RecordRAGProgress` 固定流程：

```text
validate IDs/counts/stage/time
-> UoW
-> pg_advisory_xact_lock(hash(workspace + source ref))
-> read existing event occurred_at in same tx
-> Events AppendScoped in same scope
-> commit
```

Event exact replay 沿用既有 source ref 和原 occurred_at；payload 只含允许的计数与 ID 摘要，不包含 Prompt/Evidence/原始响应。callback 成功后的 commit error 映射 manual recovery required，不自动重试或另开 Event 事务。

## 9. Error、context、资源与 scope 限制

- root/scoped 方法都校验 repository、GORM root、UoW、ctx；Raw `Row()`/`Rows()` 先检查 statement error 和 nil handle；多行查询总是 Close 并检查 `Rows.Err()`；
- no-row 同时识别 `sql.ErrNoRows`、`gorm.ErrRecordNotFound` 和共享 legacy helper 需要的 `pgx.ErrNoRows`；
- Foundation Error 原样传递；新 classifier 保留原始 cause chain 和稳定安全 code；cancel/deadline 连接 `ctx.Err()` 与 distinct `context.Cause(ctx)`；
- `sql.ErrTxDone`、nil/foreign/stale scope 映射 dependency unavailable；无效调用参数映射 existing invalid code；跨 owner fence error 先按 3.5 翻译为 Agent code，禁止直接透传 Workflow code；
- Foundation scope 当前无 Pool affinity 标识。GORM Repository 只接受 active platform scope，但无法区分 Pool A/B；同池由构造和 TODO 9 fixture 保证。若需要运行时 cross-pool rejection，回到 Foundation 增加 owner identity，不在 Agent 私自类型断言。

## 10. TODO 9 验证

不新增测试文件。现有 fixture 扩为每个 legacy/GORM 子测试独立数据库：raw migration pool 完成迁移并关闭后，用子库 URL 创建唯一 `platformpostgres.Pool`；legacy 从 `DB()` 构造，GORM 从同一个 Pool 构造 Repository/UoW/Events Store。cleanup 先关平台 Pool 再 drop。

重点矩阵：

- Model Run/Call：caller scope 内未提交可见性、按 ID/Attempt 查询、FOR UPDATE 竞争、exact replay/conflict、active-call trigger、call phase/order、UNKNOWN recovery；
- Scoped Capture：Profile Complete/Fail、Revision/Evidence/Attempt 与 Agent Run 一起 commit/rollback，CAS replay 与竞争；
- Memory：claimant 竞争、READY+Run 双向绑定、rollback/commit-loss、FAILED replay；
- Workspace Analysis：双授权单 reservation、cancel-vs-authorize 无孤儿、stale fence/deadline、success/candidate/review/failure/refusal/unknown、replacement attempt、commit-loss recovery；
- Capability/Run：DB lease 时间、exact contract、Conversation dispatch owner 事实与 Run/Event/Workflow 同事务；
- RAG Progress：advisory lock、Event exact/conflicting replay、owner rollback、commit unknown；
- 错误/资源：真实 SQLSTATE、cancel/cause/deadline、nil/foreign/stale scope、corrupt row no partial、连接释放；
- 性能：Run/Call history、recovery、Workspace Analysis operation/result/candidate/capability 使用目标索引且无无界 Sort/N+1。

`ZHIXU_TEST_DATABASE_URL` 缺失时只允许 integration compile，不得把 skip 视为验收。

## 11. Final 交接与回滚

- 本 child 记录 API/Worker、Capture、Artifact、Conversation、Organizing 及 tests 的 legacy 构造/Port 清单，不修改它们；
- TODO 9 后仍由 Final 按依赖顺序切换完整平台 Pool、Agent GORM Repository、Events/Audit/Workflow scoped 消费者；
- Workflow child 必须先交付 scoped execution fence，Agent 才可实现并验收 Workspace Analysis Model Operation staged 阶段；
- Consumer child 分别切到 owner-defined scoped Port，Final 才删除 legacy `any` 和模块内非 allowlist pgx；
- 任一 staged 回滚只删除对应 GORM/scoped 文件；Final 切换失败恢复 legacy Composition，禁止双写或 fallback。
