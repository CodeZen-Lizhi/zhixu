# Capture Repository 迁移到 GORM

## Goal

将 Capture 核心持久化与 Document Knowledge Profile 持久化迁移到共享 PostgreSQL Pool 上的 GORM/`database/sql` 事务路径，同时保持 Workspace Source、Capture、Outbox、command receipt、Profile Revision/Evidence 与 Agent Model Run 的跨 owner 原子性。

## Requirements

### R1. 单 Pool 与 staged 边界

- 新 GORM Adapter 必须从同一个 `internal/platform/postgres.Pool` 获取 GORM root 与 `foundation.UnitOfWork`；禁止自行 `gorm.Open`、`sql.Open`、创建第二个 pgx pool 或依赖 GORM 默认事务。
- TODO 9 真实 PostgreSQL 门禁通过前，保留 legacy `Repository`、`ProfileRepository`、Workspace `TransactionWriter` 和全部 API/Worker Composition；不得增加 selector、双写、fallback 或提前删除 pgx 路径。
- 本 child 不修改 migration、HTTP/OpenAPI、Domain 状态机、Application Service、文件发布协议或 `cmd/**`。

### R2. Capture 核心事务

- `Create` 必须在一个 Capture-owned UoW 中按既有顺序提交 Workspace Source/Artifact/Source Version、`core.capture`、`ops.capture_outbox` 与 `core.capture_command`；任一环节失败全部回滚。
- `MaterializeURL` 必须在一个 UoW 中锁定 Capture/Attempt、通过 Workspace `ScopedSourceWriter` 注册 Source Version，再以 CAS 推进 Capture 与 Attempt；禁止 Source 先提交、Capture 后提交。
- Retry 必须保持状态重置、Outbox 和 exact command receipt 同事务；唯一冲突只能在事务回滚后从 root 读取 durable receipt。
- Capture 与 Workspace 的新协作只依赖 `workspace/application.ScopedSourceWriter` 和 `foundation.TransactionScope`，不得新增对 Workspace PostgreSQL具体类型的依赖。

### R3. Outbox 与 Processing 状态机

- Outbox claim 保留单条 CTE、`FOR UPDATE SKIP LOCKED`、`clock_timestamp()`、稳定排序和 lease/version CAS；MarkPublished、Reschedule、Poison 的 owner/version/terminal 条件不得放宽。
- BeginAttempt、URL materialization、stage checkpoint、ready/degraded/failure 必须保留 Capture `FOR UPDATE` -> Attempt `FOR UPDATE` 的锁序、严格版本递增、RowsAffected/no-row 语义和独立阶段状态。
- 数据库时间、`GREATEST`、状态分支、terminal replay 与 Workflow Run/Attempt binding 不得改为 GORM 自动时间、先读后写或普通 CRUD Save。

### R4. Profile 与 Agent 前置依赖

- Profile 完成、失败和 replay 必须让 Profile Revision/Evidence、Profile/Attempt 状态与 Agent Model Run 读取/锁定/CAS 终结处于同一个事务；Agent Port 至少固定 `GetModelRunScoped`、`GetModelRunRecordScoped`、`FinalizeModelRunScoped` 的 for-update/CAS/exact replay 语义。
- 现有 `agentapplication.ModelRunTxFinalizer(any)` 的唯一 PostgreSQL 实现只接受 `pgx.Tx`，不能用于 GORM UoW。Profile GORM 实现必须等待 Agent child 提供以 `foundation.TransactionScope` 为参数的 scoped Model Run Port；Capture child 不跨模块修改 Agent Adapter，也不得退化成两个事务。
- Agent scoped Port 可用前，本 child 先交付未接 Composition 的 Capture 核心 GORM Adapter，legacy Profile 保持生产唯一实现；任务继续保持 `in_progress`。
- Agent scoped Port 可用后，在同一 child 内完成 staged `GORMProfileRepository`，保持 Profile/source advisory lock、shared row locks、append-only Revision/Evidence、current pointer、retry receipt 和 Agent Model Run 原子终结。

### R5. 查询、映射与边界

- Capture List 保持 Workspace predicate、可选 kind/status、`(captured_at,id)` DESC keyset、Limit+1 和上限 100；禁止 OFFSET 和 N+1。
- Profile 读取保持 Workspace/Source Version 防枚举、最大 batch、三次有界 set query、完整 Evidence 闭包和稳定 Source Version 顺序。
- Profile source loader 保持最多 500 Chunk、单 Chunk 128 KiB 的 SQL 阶段 fail-closed；不得先读取超限正文再校验。
- 所有 UUID、enum、nullable time/ID、JSON receipt/profile content 和 domain binding 必须显式 scan/validate；损坏行整次 fail closed，不返回部分结果。

### R6. SQL、数组与 Schema

- `FOR UPDATE/FOR SHARE/SKIP LOCKED`、`pg_advisory_xact_lock`、CTE claim、CAS `UPDATE ... RETURNING`、DB time、复杂 manifest/source join 和 Profile Evidence `unnest` 保留参数化 GORM Raw/Exec。
- GORM SQL 统一使用 `?` placeholder；`ANY/unnest` 数组通过 `driver.Valuer`（如 `pq.Array`）作为单个参数，禁止裸 slice 展开或字符串拼接。
- Schema 唯一事实源仍是既有 migration；禁止 AutoMigrate、Migrator、GORM association/hook、`gorm.Model`、soft delete 或自动 timestamp。

### R7. 错误、取消与安全

- 保留现有 SQLSTATE、constraint、not-found、version/idempotency conflict、retryability、manual recovery 和 commit outcome unknown 语义；增加 `sql.ErrNoRows`、`sql.ErrTxDone` 与 GORM no-row 的等价映射。
- 所有公开 GORM Repository 方法拒绝 nil context；取消/超时同时保留 `ctx.Err()` sentinel 与自定义 `context.Cause(ctx)`，rollback 不得因 caller context 已取消而被跳过。
- 错误与日志不得包含 URL 响应正文、原始正文、Profile 内容、receipt JSON、SQL 参数、DSN、Secret 或暂存 locator。

### R8. 验证与完成门禁

- 未经用户明确授权不新增测试文件；静态阶段复用现有 unit tests、race、vet、integration compile、cmd compile、vendor/module 与 diff gate。
- TODO 9 使用现有 Capture/Profile/Migration/Worker integration 文件原位加入 legacy/GORM 双实现路径；每个实现使用独立已迁移数据库。raw pool 只负责 migration，随后关闭并用同一子库 URL 打开唯一平台 Pool；legacy 使用其 `DB()`，GORM 使用其 Workspace scoped writer、`GORM()` 与 UoW。
- TODO 9 未通过时不得勾选本 PRD AC、不得完成或归档 child；TODO 3 Atlas 仅阻断 Final Composition 收口。

## Acceptance Criteria

- [ ] AC1：Capture 核心 Repository、Retry、Outbox 和 Processing 已通过共享 GORM root/UoW 实现，Create 与 URL materialization 和 Workspace scoped writer 同事务提交/回滚。
- [ ] AC2：Agent scoped Model Run Port 已由 Agent owner 提供，Profile/Retry GORM Adapter 在同一 scope 内保持 Revision/Evidence/Profile Attempt/Model Run 原子终结。
- [ ] AC3：Capture/Profile 的 Workspace 隔离、keyset、批量读取、严格 scanner、DB time、锁序、CAS、exact replay、错误码和安全日志与 legacy 等价。
- [ ] AC4：现有 unit/race/vet、integration、Worker outbox/Workflow、真实 SQLSTATE/trigger/并发/回滚/连接释放与目标查询计划门禁通过。
- [ ] AC5：生产 Composition 仍未切换、legacy Adapter 未删除、无 selector/双写/fallback/second pool/AutoMigrate；Final 交接清单完整。
- [ ] AC6：TODO 9 全部通过并有可复核证据；否则任务保持 `in_progress` 且不得归档。

## Out Of Scope

- API/Worker Composition 切换、legacy pgx 删除和全仓 pgx allowlist 收口，归 Final child。
- Agent Repository、Workflow/River、Workspace、Ingestion、Retrieval 或模型运行时的完整 GORM 迁移。
- Capture/Profile Schema、trigger、index、状态、HTTP/OpenAPI、文件 stage/publish/read-repair 或产品行为变更。
- 新增测试文件、临时测试代码或运行时 ORM selector。

## Dependencies

- Capture 核心 staged 实现：依赖 `gorm-platform-transaction-foundation` 与 Workspace `ScopedSourceWriter`。
- Profile staged 实现和本 child 完成：额外依赖 Agent owner 提供 scoped Model Run reader/finalizer；Agent 的完整生产切换仍按其自身 Workflow/Events/Audit 依赖执行。
