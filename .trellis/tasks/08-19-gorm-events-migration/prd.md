# Events Store 迁移到 GORM

## Goal

将 Server Events PostgreSQL Store 的 Workspace 水位、逻辑保留窗口、SSE 重放读取和调用方事务追加迁移为未接生产 Composition 的 staged GORM 路径，同时提供不暴露具体数据库事务类型的稳定追加 Port。

## Background

- `ops.server_event.seq` 是全局 `bigserial`，Workspace 内只保证严格递增而不保证连续；水位必须继续按 Workspace 计算 `MAX(seq)`（`internal/events/adapter/postgres/store.go:40-55`、`migrations/00020_rag_conversation_sse.sql:240-279`）。
- 当前保留策略是数据库时间驱动的 24 小时逻辑过滤：`EarliestRetained`/`ListAfter` 使用 `expires_at > CURRENT_TIMESTAMP`；仓库没有生产物理清理 Port 或 Worker（`store.go:58-109`）。
- `AppendTx(any)` 被 Conversation、Change Control、Knowledge、Export、Agent、Health、Tools 等 17 个生产调用点用于加入 owner 事务；生产入口有 7 个 `NewStore` 构造点，不能由本 child 提前切换。
- 现有真实 PostgreSQL 测试覆盖 Workspace 隔离、sequence gap、保留边界、分页、unsafe summary、exact replay、冲突、回滚和并发 claim；TODO 9 门禁已扩展为从同一 `platformpostgres.Pool` 构造 legacy pgx Store、GORM Store 与 Unit of Work。

## Requirements

1. 仅修改 `internal/events/application` 的事务 Port 和 `internal/events/adapter/postgres` 的 staged 实现；不改 Domain、HTTP wire、migration、trigger 或生产 Composition。
2. 保留 `Store`、`NewStore`、`Appender.AppendTx(any)` 与所有 legacy 调用方；新增基于 `foundation.TransactionScope` 的 `ScopedAppender`/`ScopedStore`，GORM Store 不实现 legacy `Appender`。
3. `NewGORMStore` 只接受一个 `*platformpostgres.Pool` 并从中取得共享 GORM root；读取不得创建第二连接池。`AppendScoped` 只加入调用方 opaque transaction，不 commit/rollback 或自行另开事务。
4. 保持 Workspace-scoped `MAX(seq)` 水位、数据库 `CURRENT_TIMESTAMP`、nullable earliest、`seq > cursor`、升序和 `1..100` 有界分页；一行损坏时整页 fail closed。
5. 保持 AppendRequest 的 UTC 微秒规范化、JSON 摘要白名单、24 小时 expiry、`(workspace_id,source_event_ref)` exact replay、完整 binding 比较和 SQLSTATE 分类。
6. 所有 SQL 参数化；GORM 路径使用 Raw Row/Rows、单参数 JSONB text carrier、显式 nullable UUID 和共享 scanner。禁止 AutoMigrate/Migrator、Hook、association、soft delete、双读、双写或 fallback。
7. 保留 caller cancel/deadline cause、稳定 `SSE_*` code 和安全错误输出；不得记录或返回 SQL、DSN、JSON payload、正文、Secret 或绝对路径。
8. 使用现有测试和局部校验。TODO 9 真实 PostgreSQL 门禁通过后，仍不切换生产 Composition、不删除 pgx；归档由父任务进度同步后执行。

## Out Of Scope

- 不新增物理 retention cleanup。直接删除过期行会使 Workspace `MAX(seq)` 水位回退，改变 future/expired cursor 语义；若需要物理清理，必须另立任务设计持久 high-water fact、批量删除与发布迁移。
- 不迁移 17 个下游 `AppendTx(any)` 调用点；它们由各 owner child 迁到 scoped Port，Final 统一删除 legacy Port。
- 不修改 SSE HTTP Handler、OpenAPI、Web EventSource、现有数据库 trigger/index/schema，也不把 Event 变成业务事实源。

## Acceptance Criteria

- [x] staged GORM Store 的水位、最早保留边界和有界重放结果与 legacy Store 等价，保持 Workspace 隔离、全局 sequence gap、数据库时间和 fail-closed scanner。
- [x] 新 scoped Port 不暴露 GORM、`database/sql`、pgx 或 `any`；调用方 owner 写入与 Event append 在同一事务提交或回滚。
- [x] exact replay、binding conflict、并发 claim、JSONB、24 小时 expiry、SQLSTATE、取消和资源释放在真实 PostgreSQL 上通过 legacy/GORM 等价检查。
- [x] 生产 `cmd/**` 与跨模块调用仍使用 legacy Store/Port；无物理 cleanup、Schema 变更、自动迁移、双写或 fallback。
- [x] `git diff --check`、受影响包 test/race/vet、integration 编译、生产入口编译、vendor/module 校验通过。
- [x] TODO 9 共享工厂可用，Events 全部实库门禁（含目标数据量、默认 planner 索引证据）已于 2026-08-28 通过；staged 实现未接入 Composition，TODO 3 仅阻断 Final。

## Dependencies

- 依赖 `gorm-platform-transaction-foundation` 的共享 Pool、GORM root 与 opaque `TransactionScope`。
- Export、Health、Change Control、Knowledge、Agent、Tools、Conversation 等后续 child 依赖 Events scoped Port；它们完成前 legacy Port 必须保留。
