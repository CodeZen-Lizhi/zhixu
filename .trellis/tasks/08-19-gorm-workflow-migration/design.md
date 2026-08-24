# Workflow Repository GORM 迁移设计

## 1. 目标与阶段边界

Workflow 持久化面同时拥有兼容 Repository、Runtime 状态机、业务事务内 River 入队、租约、控制命令、人审和跨 owner Hook。它不是普通 CRUD，不能通过替换一个 Repository 或使用 GORM Association/Save 完成迁移。

本 child 分为三个 staged 交付阶段：

1. **Execution Fence**：先交付 Agent Workspace Analysis 所需的 caller-owned scoped lock Port；
2. **Repository / Runtime 基线**：新增未接生产 Composition 的 GORM Repository、Runtime Start/State/Output，并复用 scoped River producer；
3. **TODO 9 收口**：在真实 PostgreSQL 上验证等价行为，并等待 Model Settings enqueue fence 与各消费 owner 的 scoped Start/Hook 后再允许切生产。

全部阶段保留 legacy pgx Repository、RuntimeRepository、`StartTx(pgx.Tx)`、`JobInserter(any)` 和现有 Hook。TODO 9 前不修改 `cmd/**`、migration 或测试，不增加 selector、双写或 fallback，不删除 legacy 文件。任一 GORM staged 文件可独立删除回滚，生产路径不受影响。

## 2. Owner、Schema 与索引事实源

| 事实 | 主要 migration / owner |
| --- | --- |
| `workflow.definition/run/node_run/human_task/outbox_event` | `00003_workflow.sql` |
| Runtime identity、Run/Node/Outbox 幂等索引与不可变 trigger | `00011_river_runtime_foundation.sql` |
| pause/cancel、retry、`node_attempt`、`control_command`、状态 trigger | `00012_workflow_runtime_state_machine.sql` |
| Workspace-safe Definition/Run 外键与列表索引 | `00031_m9_business_contract_indexes.sql`、`00033_m9_business_contract_contract.sql` |
| Model runtime provenance | `00065_model_settings_workflow_provenance.sql`、`00066_model_settings_execution_provenance.sql` |
| River job 表、worker、listener、migration | River / `riverpgxv5` owner；本 child 仅复用 scoped insert producer |
| Agent Workspace Analysis 表 | Agent owner；Workflow fence 不访问这些表 |

本 child 不新增或改写 migration，不调用 `AutoMigrate`、`Migrator`、`Preload`、`Association` 或 `Save`。复杂状态修改全部保持固定参数化 Raw SQL、明确 `FOR UPDATE/FOR SHARE`、CAS 与 `RETURNING`。

## 3. Application scoped Port

### 3.1 Workspace Analysis execution fence

在 `internal/workflow/application` 新增：

```go
type ScopedWorkspaceAnalysisExecutionFence interface {
    LockWorkspaceAnalysisExecutionScoped(
        context.Context,
        foundation.TransactionScope,
        WorkspaceAnalysisExecutionFenceRequest,
    ) (WorkspaceAnalysisExecutionFenceSnapshot, bool, error)
}
```

Request 固定包含 Workspace ID、Workflow Run ID、Node Run ID 和 Node Attempt ID。公开纯值合同固定为：

```go
type WorkspaceAnalysisExecutionFenceRequest struct {
    WorkspaceID   foundation.ID
    WorkflowRunID foundation.ID
    NodeRunID     foundation.ID
    NodeAttemptID foundation.ID
}

type WorkspaceAnalysisExecutionFenceSnapshot struct {
    DefinitionID      foundation.ID
    DefinitionKey     string
    DefinitionVersion int64
    DefinitionGraph   string

    WorkspaceID     foundation.ID
    WorkflowRunID   foundation.ID
    WorkflowStatus  domain.RunStatus
    PauseRequested  bool
    CancelRequested bool

    NodeRunID          foundation.ID
    NodeKey            string
    NodeStatus         domain.NodeStatus
    NodeAttempt        int
    NodeLeaseOwner     string
    NodeLeaseOwnerSet  bool
    NodeLeaseUntil     time.Time
    NodeLeaseUntilSet  bool

    NodeAttemptID        foundation.ID
    AttemptStatus        domain.AttemptStatus
    AttemptNo            int
    AttemptLeaseOwner    string
    AttemptLeaseOwnerSet bool
    AttemptLeaseUntil    time.Time
    AttemptLeaseUntilSet bool
}
```

`PauseRequested` / `CancelRequested` 分别由对应 nullable timestamp 的 `Valid` 决定；owner/lease 只有 `*Set=true` 时值字段才有效。不得把数据库 NULL 混同为空 owner 或零时间。

Adapter 只在 caller-owned scope 内依次执行：

```text
workflow.run + definition FOR UPDATE OF run
-> workflow.node_run FOR UPDATE
-> workflow.node_attempt FOR UPDATE
```

每一步都校验 Workspace 与父子 binding。任一行缺失或 binding 不匹配返回零快照、`found=false,nil`。它不访问 Agent 表、不读取 DB clock、不 begin/commit/rollback、不缓存 scope，也不 fallback root DB。Graph 返回不可变 string，nullable 时间/owner 使用值加显式存在标记，避免共享可变 slice/指针。

错误合同：

- request 或 nil context 无效：非重试 `WORKFLOW_WORKSPACE_ANALYSIS_EXECUTION_FENCE_INVALID`；
- nil、非平台或失效 scope：dependency unavailable；
- cancel/deadline 同时保留 `ctx.Err()` sentinel 与 distinct `context.Cause(ctx)`；
- SQL 错误保留底层 SQLSTATE cause；`40001/40P01` 固定为 `retryable_failure`，`55P03` 固定沿用 legacy fallback 的 `dependency_unavailable` 且 `Retryable=true`；
- Agent 只依赖 error 类别与 cause，不解析 Workflow 文案或透传 Workflow code。

Foundation scope 当前不携带 Pool affinity。Adapter 能拒绝非法/失效 scope，但不能识别来自另一个 Pool 的 active scope；同池由 Composition 与 TODO 9 fixture 保证。若需要运行时 cross-pool 拒绝，应回到 Foundation 增加 owner identity，不在本 child 私自断言。

### 3.2 Tools execution policy snapshot and recovery fence

Tools 仍然拥有 `workflow.tool_call`，但不能在 GORM Adapter 中继续直接读取或锁定 Workflow
owner 的 Run、Definition、Node Run 与 Node Attempt。为此在 `internal/workflow/application` 增加两个
caller-owned scoped Port；公开类型只包含 Foundation ID、Workflow Domain 状态、标量和显式 nullable
标志，不包含 Tools、GORM、database/sql 或 pgx 类型。

实时 admission 使用：

```go
type ScopedToolExecutionPolicySnapshot interface {
    LockToolExecutionPolicyScoped(
        context.Context,
        foundation.TransactionScope,
        ToolExecutionPolicySnapshotRequest,
    ) (ToolExecutionPolicySnapshot, bool, error)
}
```

Request 固定包含 Workspace ID、Workflow Run ID、Node Run ID、Node Attempt ID。Snapshot 返回
Definition ID/key/version/graph、Run status/pause/cancel、Node key/kind/status/attempt/lease、Attempt
status/no/lease 以及 `DatabaseNow`；nullable owner/lease 继续使用值加 `Set` 标志。Adapter 在同一个
caller scope 中执行 legacy 等价的单条 join query，并保持 `FOR SHARE OF run,definition,node,attempt`。
它只返回经过 Workspace 与父子 binding 校验的原始事实，不解析 Tool policy、catalog、deadline 或
lease admission，也不访问 Agent 或 Tool Call 表。

过期恢复使用：

```go
type ScopedToolCallRecoveryFence interface {
    LockToolCallRecoveryScoped(
        context.Context,
        foundation.TransactionScope,
        ToolCallRecoveryFenceRequest,
    ) (ToolCallRecoveryFenceResult, error)
}
```

Result 显式区分 `Found`、`Skipped` 与 `Stale`，并返回 Run/Node/Attempt 状态、lease 和
`DatabaseNow`。Adapter 先做无锁父子 binding preflight：不存在或 mismatch 返回 `Found=false`；随后
严格按 legacy 顺序锁 `workflow.node_run FOR UPDATE SKIP LOCKED -> workflow.node_attempt FOR UPDATE
SKIP LOCKED`。任一目标存在但被其他事务锁住返回 `Skipped=true`；两把锁都取得后才依据数据库时间
计算 `Stale`。恢复 fence 不锁 Workflow Run，只读取其 status；Tools 在此之后才锁自身 Tool Call。

两个 Port 都拒绝 nil context、无效 ID、非法或失效 scope；不 begin/commit/rollback、不缓存 scope、
不 fallback root，也不访问 Agent 或 `workflow.tool_call`。live policy/fence 只用于首次授权或恢复判断；
commit response-loss 的 durable replay 禁止再次调用它们，避免当前 lease/cancel/deadline 改写既有事实。
Foundation 无 Pool identity 的限制继续由同一 Pool Composition 与 TODO 9 fixture 保证。

### 3.3 Scoped Runtime Start

保留 `RuntimeStarter.Start` 和 legacy `RuntimeRepository.StartTx(pgx.Tx, ...)`，新增：

```go
type ScopedRuntimeStarter interface {
    StartScoped(context.Context, foundation.TransactionScope, RuntimeStartRequest) (RuntimeStartResult, error)
}
```

GORM Runtime 同时实现 root `Start` 与 `StartScoped`。Root `Start` 只开启一个平台 UoW，再委托同一 scoped helper；caller-owned `StartScoped` 不管理事务。Artifact、Conversation、Change Control、Health、Graph 在各自迁移前继续使用 legacy `StartTx`，本 child 不修改这些消费者。

### 3.4 Scoped lifecycle Hooks

现有 Cancellation、Terminal、Control Hook 的 `any` 实现都实际断言 `pgx.Tx`。不能把 `foundation.TransactionScope` 塞入 legacy Hook。新增并行 Port：

```go
type ScopedCancellationSafetyGuard interface {
    SafeToCancelWorkflowNodeScoped(context.Context, foundation.TransactionScope, foundation.ID) (bool, error)
}

type ScopedWorkflowTerminalHook interface {
    OnWorkflowNodeTerminalScoped(context.Context, foundation.TransactionScope, WorkflowNodeTerminalEvent) error
}

type ScopedWorkflowControlHook interface {
    OnWorkflowControlScoped(context.Context, foundation.TransactionScope, WorkflowControlEvent) error
}
```

同时提供 scoped composite，保留注册顺序、typed-nil 防御和首错停止。GORM Runtime 构造只接受 scoped Hook；legacy Runtime 构造只接受 legacy Hook。未配置 Hook 的普通路径可做 staged 基线；携带 Hook 的生产路径必须等待各 owner 提供 scoped 实现，禁止 no-op、双事务或自动降级到 legacy Hook。

## 4. Staged 构造与文件拆分

### 4.1 GORM Repository

```go
NewGORMRepository(*platformpostgres.Pool) (*GORMRepository, error)
```

从同一 Pool 获取 GORM root 与 UoW，拒绝 nil/无效 root。实现 `domain.Repository`、`domain.RunListRepository` 和 `domain.PendingHumanTaskRepository`。只读路径使用 `Raw(...).Row()/Rows()`；写路径使用平台 UoW。保留 Workspace predicate、keyset、Limit+1、DB/调用方时间和错误粒度。

### 4.2 GORM Runtime

```go
type GORMRuntimeRepositoryHooks struct {
    CancellationSafety ScopedCancellationSafetyGuard
    Terminal           ScopedWorkflowTerminalHook
    Control            ScopedWorkflowControlHook
    ModelRuntimeFreshWithin time.Duration
}

NewGORMRuntimeRepositoryWithHooks(
    *platformpostgres.Pool,
    riveradapter.Options,
    riveradapter.ScopedEnqueueFence,
    GORMRuntimeRepositoryHooks,
) (*GORMRuntimeRepository, error)
```

公开构造器要求完整平台 Pool、River Options 以及 typed-nil 安全的 scoped enqueue fence。Options 的 legacy `EnqueueFence` 必须为空；factory 在内部依次用 `riveradapter.NewClientWithOptions(pool.DB(), nil, options)` 建立同池 insert-only 配置 Client，再调用 `riveradapter.NewScopedJobInserter(pool, client, scopedFence)`。调用方不能注入另一个 Pool 的 Client/producer，Runtime root 与 River insert driver 因而都从同一个 Pool 派生。仅供同包验证的私有构造 helper 才可接收 `ScopedJobInserter` fake。Worker/listener client 仍由 Final 从同一个 `pool.DB()` 与同一份 queue options 构造，并在 TODO 9 增加错误配置反例。Model Settings rollout 的 enqueue fence 当前只有 `CheckEnqueue(ctx, pgx.Tx)`；在 Model Settings owner 提供 `ScopedEnqueueFence` 前，带 drain/fence 的 GORM Runtime 不可接生产，也不得使用 no-op fence。Foundation scope 的 cross-pool 限制仍适用于外部 caller-owned `StartScoped`，由 Composition/TODO 9 保证。

建议文件：

- `application/scoped_runtime.go`：Scoped Start 与 scoped Hook/Composite；
- `application/scoped_workspace_analysis_execution_fence.go`：fence 请求、快照、Port；
- `application/scoped_tool_execution.go`：Tools policy/recovery 请求、快照与 scoped Port；
- `adapter/postgres/gorm_core.go`：Pool/UoW、ready/context、scope、Raw Row/Rows、防御与 error classifier；
- `gorm_model.go` / `gorm_queries.go`：固定列、显式 scanner、JSONB carrier、参数化 SQL；
- `gorm_execution_fence.go`：独立三段锁实现；
- `gorm_tool_execution_policy.go` / `gorm_tool_call_recovery_fence.go`：Tools owner scoped read/lock；
- `gorm_repository.go` / `gorm_list.go`：兼容 Repository；
- `gorm_runtime_start.go`：Start/Scoped Start/replay/commit-response-loss recovery；
- `gorm_runtime_state.go` 及按 claim/control/delivery/human 拆出的私有 helper；
- `gorm_runtime_output.go`：Run input、node output、human node、definition 只读投影。

不得建立用 `any` 同时模拟 pgx/GORM 的 DB abstraction，也不得让 GORM 类型持有 legacy Repository 作为 fallback。

## 5. Repository 等价行为

- Definition 注册保持 `(workspace_id,key,version)` exact graph replay；Definition 不更新；
- Run/Node 创建保持 Workspace、Definition、ID、input、状态、version 与时间绑定；
- Claim/Heartbeat/Complete 继续显式锁/CAS，零行保持原错误类别；
- Human Task 创建/提交保持 Node/Run 状态联动、过期语义和同一事务；
- ListRuns 使用 `(updated_at,id) DESC` keyset、可选 status、Limit+1，依赖 `idx_workflow_run_workspace_updated_id` / status 索引，无 Offset/N+1；
- JSONB 用验证后返回 string 的 Valuer 或 `::text` scan，避免 `[]byte` 被 pgx stdlib 推断为 `bytea`；读取后经既有 domain/canonical 校验，损坏行 fail closed；
- 多行读取始终检查 statement error、nil rows、Close 和 `Rows.Err()`。

## 6. Runtime Start 与 River 原子性

Start/StartScoped 在同一个 scope 中保持：

```text
SELECT clock_timestamp()
-> Definition upsert / graph exact replay
-> active legacy Run guard FOR UPDATE
-> Run insert or idempotency replay FOR UPDATE
-> root Node insert or exact replay
-> Outbox insert or exact replay
-> ScopedJobInserter.InsertTx(same scope)
-> UoW commit
```

Run/Node/Outbox replay 必须逐字段比较。新事实得到 duplicate job、或 replay 事实得不到 duplicate job，都为 consistency violation。Root Start 的 callback 成功但 commit 报错时，沿用 legacy 独立 root recovery，核对 Run、Node、Outbox 和 River job 全部 durable binding；找不到或不一致不得猜测成功。Caller-owned StartScoped 只返回事务内结果，由 caller 决定 commit/rollback，不执行 root recovery。

River 侧直接复用现有 `riveradapter.ScopedJobInserter`：它通过 `platformpostgres.SQLTransaction(scope)` 取得同一个 `*sql.Tx`，经官方 `riverdatabasesql` 插入。`riverpgxv5` client/worker/listener/migrator 保持不变，因为 database/sql driver 不支持 listener。GORM Runtime 不调用 legacy `JobInserter(any)`。

Runtime 的全部入队点均使用同一 scoped producer：Start、retry scheduling、successor activation（已有/新节点）和 resume nodes。Claim、Heartbeat、WaitForHuman、Pause/Cancel 不凭空增加 job。

## 7. Runtime State、锁序与 DB 时间

迁移以 legacy SQL/顺序为事实源：

- Claim：定位 Run -> `run FOR UPDATE` -> Definition -> `node FOR UPDATE` -> 只锁匹配 `(node_run_id,dispatch_no,delivery_id)` 的最新 Attempt（`ORDER BY attempt_no DESC LIMIT 1 FOR UPDATE`）-> 第一次 `clock_timestamp()`。exact replay/stale/active-lease 分支在此直接返回，不读取 Model Settings。只有确需新 Attempt 时才按 `ops.model_settings_state FOR SHARE -> ops.model_settings_runtime FOR SHARE -> 第二次 clock_timestamp()` 做 managed-runtime freshness admission，随后按第一次 claim 时间归约旧 lease、插 Attempt 并做 Node/Run CAS；不得锁整个 Attempt history 或把 rollout 锁提前到 replay 路径；
- Heartbeat：Run control fence -> Node -> Attempt 锁，再用 DB time 对 owner/attempt/version/lease 做完整 CAS；
- Delivery：Run `FOR UPDATE`，所有 Nodes 按 `node_key,id` 固定顺序锁定，再锁当前 Attempt；成功/失败/retry/successor/Outbox/River/Hook 同事务；
- Control：Run lock -> command receipt lock/replay -> Nodes 固定排序锁 -> pause/resume/cancel 状态归约 -> scoped Hook -> receipt；
- Human Wait：Run `FOR UPDATE` -> 全部 Nodes 按 `node_key,id FOR UPDATE` -> 精确 `(node_run_id,attempt_no)` Attempt `FOR UPDATE` -> DB time/fence -> Attempt/Node/Run 归约 -> INSERT Human Task/Outbox；
- Human Submit：Run `FOR UPDATE` -> authorization read -> 全部 Nodes 稳定锁 -> Human Task `FOR UPDATE` -> submitted replay/版本/expiry/control 校验；只有 replay 返回或有效提交所需时才按既有位置锁 latest Attempt。不得把 Attempt 提前到 Task 之前；提交成功后的 successor/River 与全部状态仍在同一事务；
- terminal/cancel/control Hook 只接收当前 live scope，不自行提交、回滚或执行事务外副作用；Hook 失败回滚 Workflow 状态、Outbox 与 River job；
- `clock_timestamp()`、`GREATEST`、lease duration 和 lock-wait 后时间判定保持数据库时间，不能换成 Go `time.Now()`。

`node_attempt` 是 append-only terminal transition 事实；禁止 GORM struct Save 覆盖。`node_run.dispatch_no` 只能单调 +1。Run/Node/Outbox identity 由 trigger 冻结，所有 CAS 保留 WHERE predicate 与 RowsAffected/no-row 语义。

## 8. Runtime Output 与边界查询

`GetRunInput`、`GetSucceededNodeOutput`、`GetPendingHumanTaskNode`、`GetRunDefinition` 保持 Workspace/Run/Task/Node 组合 predicate、最大 JSON 大小、object 校验和 not-found/error code。不得通过 Preload 加载整棵 Graph、Attempts 或私有表，也不得引入 N+1。

## 9. Error、context、资源与安全

- root/scoped 方法均校验 repository、GORM root、UoW、ctx 和输入；
- no-row 统一识别 `sql.ErrNoRows`、`gorm.ErrRecordNotFound`，共享 legacy helper 时兼容 `pgx.ErrNoRows`；
- Foundation Error 原样传递。Repository/Runtime 必须逐项保留 legacy classifier：`23505 -> version_conflict/WORKFLOW_CONFLICT`，`23503 -> consistency_violation/WORKFLOW_REFERENCE_INVALID`，`23514/22P02 -> invalid_input/WORKFLOW_DATA_INVALID`，`40001/40P01 -> retryable_failure/调用点 code`；`55000`、`55P03`、无 caller cancel 的 `57014` 以及未知 SQLSTATE 继续是 `dependency_unavailable/调用点 code/retryable=true`，不得在 ORM 迁移中改成新的不可重试类别。Execution Fence 是新 Port，但也沿用相同 kind/retryability，并保留 SQLSTATE cause；
- context 分类先处理 explicit cancellation，再 deadline；同时连接 distinct custom cause；
- `sql.ErrTxDone`、非法/失效 scope 映射 dependency unavailable；
- 未知数据库错误使用安全稳定 code 和 `%T` 摘要，不把 SQL 参数、JSON、lease owner 或内部查询写入日志/错误文本；
- commit/rollback 与 scope 失效只由平台 UoW 负责；Workflow Adapter 不直接操作 `*sql.Tx` 生命周期。

## 10. TODO 9 验收与生产切换门禁

不新增测试文件。扩展现有 integration fixture：每个 legacy/GORM 子测试使用独立临时数据库；migration pool 完成迁移后关闭，再从子库 URL 构造唯一 `platformpostgres.Pool`。legacy 使用 `DB()`，GORM 使用同一 Pool 的 GORM/UoW/River scoped producer。

必须验证：

- Fence：Run -> Node -> Attempt 锁序、完整快照、各 binding mismatch `found=false`、invalid/stale scope、cancel/deadline/SQLSTATE；
- Start：正常提交、caller rollback、River insert 失败全回滚、并发 duplicate、exact conflict、commit response-loss recovery；
- River：Workflow/Outbox/job 同时可见或同时消失，pgx worker 可消费 scoped producer 插入的 job，listener/migrator 仍正常；factory 拒绝 legacy fence 与 scoped fence 混用，且不存在可注入异池 Client/producer 的公开构造路径；
- Claim/Heartbeat：DB time、lock wait 后 lease、reclaim、stale owner、managed runtime admission/rollout；
- Delivery：success/failure/retry exhausted、successor join 只激活一次、Outbox/River 去重、Hook failure rollback；
- Control/Human：pause/resume/cancel replay、safe cancellation、human wait/submit/expiry；
- Repository/Output：List cursor/status、JSON/corrupt row/no partial、连接释放和目标索引 EXPLAIN；
- cross-owner：Artifact、Conversation、Change Control、Health、Graph scoped Start；Model Settings scoped enqueue fence；Change Control/Graph/Health cancellation guard；Artifact/Conversation/Organizing terminal/control Hook。

若 `ZHIXU_TEST_DATABASE_URL` 不可用，只允许 unit/race/vet/integration compile 与静态检查，不得勾选 PRD AC、切生产、删除 legacy 或归档任务。普通无 Hook GORM 基线也不能代表带 Hook/rollout 的生产 Runtime 已验收。

## 11. 依赖、回滚与 Final 交接

- Workflow fence 交付后，Agent 才能实现 Workspace Analysis Model Operation staged 阶段；
- Model Settings 必须提供 scoped enqueue fence，才能保持 rollout drain；
- Artifact、Conversation、Change Control、Health、Graph、Organizing 在各自 child 中迁移 scoped Start/Hook；
- TODO 9 通过后，Final 才按依赖图切 API/Worker Composition，且一次业务路径只使用 legacy 或 GORM 一种事务体系；
- 回滚只需恢复 Final wiring 到 legacy；本 child 不改 Schema，staged GORM 文件无数据回滚需求。
