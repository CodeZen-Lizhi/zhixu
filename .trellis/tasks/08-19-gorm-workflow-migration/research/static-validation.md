# Workflow GORM staged 静态验证记录

日期：2026-08-20

## 结论

Workflow staged GORM 已覆盖 scoped Application Port、Workspace Analysis execution fence、legacy-compatible Repository、Runtime Start/State/Output，以及同事务 River database/sql producer。生产 `cmd/api`、`cmd/worker` 和跨 owner 调用仍使用 legacy pgx 路径；未修改 migration、测试或生产 Composition，未增加 selector、双写或 fallback。

本阶段不能完成 child：`ZHIXU_TEST_DATABASE_URL` 未配置，TODO 9 的真实 PostgreSQL 等价门禁未执行；Model Settings scoped enqueue fence 和跨 owner scoped Start/Hook 仍是生产切换前置。PRD AC、任务完成状态和归档保持未完成。

## 实现边界

- Application 新 Port 只公开 `foundation.TransactionScope` 与纯值 Request/Snapshot；不公开 GORM、`database/sql` 或 pgx，legacy `StartTx` 和 `any` Hook 保留。
- `GORMRepository`、`GORMRuntimeRepository` 和 execution fence 均从一个 `platformpostgres.Pool` 派生；caller-owned scoped 方法不 begin、commit、rollback、缓存 scope 或 fallback root。
- Runtime factory 从同一 Pool 创建 insert-only River client 与 `ScopedJobInserter`，拒绝 legacy fence 混入，并强制非 nil/typed-nil scoped enqueue fence；底层 scoped producer 同样不允许绕过 fence。
- Runtime 保持 Claim 的两个 DB time、固定锁序、CAS、exact replay、Human expiry 先提交后返回冲突，以及 Start commit-response-loss recovery。
- River worker、listener 和 migrator 继续使用 `riverpgxv5`；database/sql driver 仅用于当前 `*sql.Tx` 中的 job insert。
- GORM SQL 全部参数化；JSONB 使用 string Valuer 或 `::text` scan，多行读取检查 Scan、`Rows.Err` 与 Close。
- 新文件不使用 AutoMigrate、Migrator、Preload、Association、Save、独立连接池或生产 GORM wiring；execution fence 不访问 Agent 表。
- `ScopedRuntimeBindingReader` 返回同一 Workspace-bound Run -> Definition -> Node closure 的 raw `run.input`/`node.input`/`definition.graph` JSON；Attempt 仅报告归属存在性，不解码或 canonicalize 调用方数据。

## Review finding / fix ledger

1. Runtime Output readiness 曾错误依赖 UoW/River，导致只读投影在无 producer 时不可用；已收窄为只校验共享 GORM root。
2. `classifyGORMWorkflow` 曾对未显式分类的 PostgreSQL 错误只保留类型摘要，导致 `55000`、`55P03`、`57014` 等 SQLSTATE cause 丢失；现所有 `*pgconn.PgError` 均保留 cause，稳定 kind/code 不变，非 PostgreSQL 未知错误仍使用安全类型摘要。
3. scoped River producer 曾允许 nil fence；现构造和 InsertTx 都强制 scoped fence，禁止绕过 Model Settings rollout drain。
4. 非法/失效 scope 曾映射为 invalid input 或替换底层 cause；现 Workflow fence/UoW/River 均映射 dependency unavailable、可重试并保留 cause。
5. Control/Human 的 UoW begin 失败曾缺少 legacy 操作级 transaction code；现分别恢复 `WORKFLOW_CONTROL_TRANSACTION_FAILED`、`HUMAN_TASK_TRANSACTION_FAILED` 和 `HUMAN_SUBMIT_TRANSACTION_FAILED`。
6. 直接取消节点曾绕过统一 Terminal Hook 包装；现非 Foundation hook error 统一映射 `WORKFLOW_TERMINAL_HOOK_FAILED` 并触发同一 UoW 回滚。
7. Node 多行锁查询的 scan 失败曾未合并 `Rows.Err`/Close error；现错误与正常路径都显式收敛资源错误。

独立 Go Review、SQL/事务 Review 与 Trellis Check 复核后未发现剩余 P0/P1/P2。

## 已执行门禁

以下命令均通过：

```text
go test -mod=vendor ./internal/workflow/... -count=1 -timeout 60s
go test -race -mod=vendor ./internal/workflow/... -count=1 -timeout 60s
go vet -mod=vendor ./internal/workflow/... ./internal/platform/postgres
go test -mod=vendor -tags=integration -run '^$' ./internal/workflow/... -count=1 -timeout 60s
go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker ./internal/artifact/... ./internal/conversation/... ./internal/changecontrol/... ./internal/health/... ./internal/graph/... ./internal/organizing/... -count=1 -timeout 60s
go list -mod=vendor ./internal/workflow/... ./internal/platform/postgres ./cmd/api ./cmd/worker
go mod verify
python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-workflow-migration
gofmt -d <Workflow staged Go files>
git diff --check
```

静态搜索确认：无禁止 ORM 模式、debug log、GORM production wiring、execution fence 的 Agent 表访问，以及 GORM Runtime 对 legacy Hook/`JobInserter(any)` 的调用。Application scoped 文件中的 `any` 仅存在于私有 typed-nil helper，不属于公开 API。

`go mod tidy -diff` 只读执行并返回 1，输出为任务开始前已有的全仓 `go.sum` 规范化差异并建议补充 SQLite checksum；未应用输出，也未由 Workflow child 修改依赖文件。

## 2026-08-21 scoped runtime binding 增量

Artifact GORM 的 scoped Generation 前置要求 Workflow 交付 durable binding reader。新增 `application.ScopedRuntimeBindingReader`、纯值 query/snapshot，以及 `NewGORMRuntimeBindingReader`。实现以 caller-owned `foundation.TransactionScope` 解封同一 GORM transaction，并按 `workspace_id + run.id + node.id` 查询 `workflow.run/definition/node_run`；不存在返回 `WORKFLOW_RUNTIME_BINDING_NOT_FOUND`，无效或过期 scope 按现有 Workflow scoped 语义返回可重试的 `WORKFLOW_RUNTIME_BINDING_TRANSACTION_UNAVAILABLE`，损坏持久事实返回一致性错误。节点 schema version 的历史 NULL 被明确拒绝，不让驱动 Scan 错误掩盖一致性语义。

本增量已通过：

```text
go test -mod=vendor ./internal/workflow/... -count=1 -timeout 60s
go test -race -mod=vendor ./internal/workflow/... -count=1 -timeout 60s
go vet -mod=vendor ./internal/workflow/...
go test -mod=vendor -tags=integration ./internal/workflow/adapter/postgres -run '^$' -count=1 -timeout 60s
```

## 生产与测试 wiring

- 生产仍由现有 `NewRepository`、legacy `RuntimeRepository`、`StartTx(pgx.Tx)`、legacy Hook 和 pgx River worker/listener/migrator 构造；`cmd/**` 没有调用本 child 的 GORM 构造器。
- Artifact、Conversation、Change Control、Health、Graph 仍使用 legacy caller-owned `StartTx`；Organizing 与其他 owner 的 terminal/control/cancellation Hook 也未在本 child 切换。
- 现有 Workflow `*_test.go` 没有引用 `NewGORMRepository`、`NewGORMRuntimeRepositoryWithHooks` 或 `NewGORMWorkspaceAnalysisExecutionFence`。unit/race 证明旧行为和编译不回归，不是新 GORM SQL 的运行证据。
- integration `-run '^$'` 只证明测试可编译；TODO 9 将原位参数化现有 fixture，不新增平行事实源。

## 未完成与风险

1. 当前环境没有 `ZHIXU_TEST_DATABASE_URL`，因此没有真实 PostgreSQL 证据证明 GORM placeholder/cast、锁等待、DB time、SQLSTATE、trigger、连接释放和执行计划。
2. 尚未验证 Workflow/Node/Outbox/River job 同 commit/rollback、并发 exact replay、commit response-loss，以及 database/sql producer 写入后 pgx worker 消费。
3. Model Settings 尚未交付 `ScopedEnqueueFence`，因此 GORM Runtime 不能接生产，也不得使用 no-op fence。
4. Artifact、Conversation、Change Control、Health、Graph、Organizing 等 owner 尚未全部交付 scoped Start/Hook，跨 owner 原子性仍需各 child 与 TODO 9 验证。
5. Foundation `TransactionScope` 不携带 Pool affinity identity。当前能拒绝 nil、非平台和 stale scope，但无法识别另一个 Pool 的 active scope；同池由 Composition 与 TODO 9 fixture 保证，严格 cross-pool rejection 需 Foundation 后续能力。

## 2026-08-25 Tool policy / recovery scoped Port review

本轮开发盘点确认 Workflow child 已提供以下未接生产的 staged API：

- `internal/workflow/application/scoped_tool_execution.go` 暴露纯值 Request/Snapshot/Result 与两个 caller-owned Port；签名不含 Tools、GORM、`database/sql`、pgx 或 `any`。
- `gorm_tool_execution_policy.go` 在 caller scope 内以一条参数化 join 执行 `FOR SHARE OF r,d,n,a`，返回 Definition/Run/Node/Attempt、显式 nullable 标志和同一查询的 `clock_timestamp()`；不执行 Tools admission。
- `gorm_tool_call_recovery_fence.go` 先做无锁 Workspace/Run/Node/Attempt binding preflight，再按 Node Run -> Node Attempt 使用 `FOR UPDATE SKIP LOCKED`；锁竞争返回 `Found=true,Skipped=true` 且不读取 DB time，双锁成功后才计算 `Stale`，不锁 Run 或 `workflow.tool_call`。
- Tools GORM 的 stale candidate 路径只在 live stale recovery 调用 recovery fence；receipt/operation commit-response-loss 路径通过 Agent durable closure 与 Tool-owned receipt 复核，不重新调用 live policy/recovery fence。

Go/SQL/Trellis review finding ledger：

1. Workflow execution-fence 构造器在 `pool.GORM()` 失败时曾用摘要错误覆盖底层依赖 cause；已改为保留原错误链，和两个 Tool Adapter 的 dependency unavailable 合同一致。
2. Tool policy/recovery SQL 检查通过：所有外部 ID/limit 均参数化；无 AutoMigrate/Migrator/Preload/Association/Save；Workflow Adapter 不访问 Agent 或 `workflow.tool_call`；recovery 不锁 Run，且 DB time 查询位于两把锁之后。
3. 资源与错误检查通过：单行使用 `Raw(...).Row().Scan`，未引入需关闭的 rows；无效/foreign/stale scope 映射 dependency unavailable 并保留 cause，cancel/deadline 保留 context sentinel 与 custom cause，SQLSTATE 保留 `pgconn.PgError` cause。

本轮实际门禁：

```text
go test -mod=vendor ./internal/workflow/... -count=1 -timeout 60s
go test -race -mod=vendor ./internal/workflow/... -count=1 -timeout 90s
go vet -mod=vendor ./internal/workflow/... ./internal/platform/postgres
go test -mod=vendor -tags=integration -run '^$' ./internal/workflow/... -count=1 -timeout 60s
go list -mod=vendor ./internal/workflow/... ./internal/platform/postgres ./cmd/api ./cmd/worker
gofmt -l internal/workflow internal/platform/postgres
git diff --check
python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-workflow-migration
```

以上命令通过。当前 `ZHIXU_TEST_DATABASE_URL` 未配置，因此本轮没有真实 PostgreSQL 证据；Tool policy/recovery 的 `FOR SHARE`、`SKIP LOCKED`、锁等待、SQLSTATE、连接释放和并发 fairness 仍属于 TODO 9 门禁，不能据此勾选 PRD AC、切换生产 wiring、完成或归档 child。
