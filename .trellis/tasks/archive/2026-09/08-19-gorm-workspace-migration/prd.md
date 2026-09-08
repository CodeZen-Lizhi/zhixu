# Workspace Repository 迁移到 GORM

> **当前交付状态（2026-09-08）**：`completed`，已归档；最终代码 [cb935655](https://github.com/CodeZen-Lizhi/zhixu/commit/cb93565561498674cda1dc1230fed587fba66075) 已推送至 `origin/dev`，尚未部署。
> TODO 10 全部 30 个子任务已完成，最新实现与验证见 [Final 验收记录](../08-19-gorm-composition-pgx-convergence/research/final-acceptance.md)。下文 staged、待 Final 或未提交的描述保留其原阶段事实，现存 M9 历史升级限制仍有效。

## Goal

在不改变 Workspace 对外契约、Schema 或生产接线的前提下，新增完整的 staged GORM Repository，保持 Workspace/Source、Registry、Control、Runtime、Git capture、root grant 和 root rebind Audit 的事务、锁序、CAS、幂等与防伪造语义。TODO 9 真实 PostgreSQL 门禁通过后，才允许 Capture 和 Final 切换共享事务与生产 Composition。

## Requirements

### R1. 阶段边界与兼容性

- owner 范围为 `internal/workspace/adapter/postgres`、一个新增的 Workspace Application scoped Port、`internal/platform/rootgrant` 和 `internal/workspace/runtimegrant` 的 staged GORM Composition；既有 Application/Domain/HTTP/CLI 契约保持兼容。
- 保留 legacy `Repository`、`NewRepository(DB)`、`TransactionWriter(pgx.Tx)`、`PostgresAuthoritativeStore`、`NewProcessComposition` 和全部现有 pgx 调用点。
- 新增从完整 `*platformpostgres.Pool` 构造的 `GORMRepository`，覆盖 Workspace 主 Repository、Active/Material/List、Registry、Control、Runtime 和 Git capture 能力。
- TODO 9 前不修改 `cmd/**`、Capture、migration 或现有测试接线；禁止运行时 selector、双读、双写、静默 fallback、第二连接池和删除 legacy 路径。

### R2. Workspace、Source 与读取

- Workspace create/get/active/root list、Source/Artifact/Source Version 注册和 Source Material 读取必须保持 Workspace 隔离、root capability 校验、唯一键 exact replay、metadata conflict、legacy artifact 补齐和 UTC 微秒语义。
- Source Version 列表保持全部过滤条件、`(captured_at,id) DESC` keyset、Limit+1、LATERAL 状态投影和目标索引；禁止 OFFSET、association/preload 和 N+1。
- 持久化 record/scanner 显式处理 ID、enum、nullable UUID/time、hash、version 和 Domain 校验；损坏行必须整次 fail closed，不返回部分结果。

### R3. Registry、Control、Runtime 与 Git capture

- 所有写路径由 Foundation `UnitOfWork` 拥有一个显式事务；事务级 advisory lock、`FOR UPDATE/FOR SHARE`、CAS、`RETURNING`、trigger 和数据库时间使用参数化 GORM Raw/Exec，不能被 Builder 的隐式行为替代。
- Registry 保持 fingerprint/root lock、control state、mutation gate、Workspace 和 Runtime 的既有锁序；root rebind 的 history、Workspace/control CAS 与 Audit append 必须在同一事务提交或回滚。
- ControlSnapshot 使用 `REPEATABLE READ READ ONLY`；Switch/lease/takeover/revoke/commit/restore/finish 保持 state、operation、gate、runtime 的固定锁序、DB `clock_timestamp()` 和 version/owner CAS。
- Runtime register/heartbeat/phase 保持 control/workspace/operation 的共享锁、role row 独占锁、grant/binding fence 与数据库时间。
- Git capture 保持 Workspace transaction advisory lock、checkpoint row lock、Source 生命周期写入、tombstone、顺序 fence、exact replay 和 completion CAS。

### R4. 跨模块事务 Port

- 在 Workspace Application 层新增不泄漏 pgx、GORM、`database/sql` 或 `any` 的 `ScopedSourceWriter`，公开签名只包含 `context`、`foundation.TransactionScope` 与 Workspace Domain 类型。
- staged GORM Repository 实现该 Port，只使用 caller 的 active scope，不创建、提交、回滚或 fallback 到 root transaction。
- legacy `TransactionWriter(pgx.Tx)` 原样保留，供当前 Capture 创建和 URL materialization 使用；Capture child 后续迁到同一 Foundation UoW 后才改依赖并删除该例外。
- 当前 Foundation scope 没有 Pool owner identity，因此只能拒绝 nil、非平台和 stale scope；active foreign-Pool scope 的拒绝需要 Foundation 后续能力。本 child 通过单 Pool 构造、Composition 和 TODO 9 fixture 保证同池，不虚假声明运行时 affinity 已完成。

### R5. Audit、root grant 与 Runtime Composition

- GORM root rebind 只依赖 `auditapplication.ScopedAppender`，在 Workspace UoW 的同一 scope 中追加；legacy `Appender.AppendTx(any)` 保持不动。
- rootgrant 新增共享 Pool 上的 GORM authoritative store，并复用既有 `AuthoritativeStore`、managed/direct resolver、无 fallback 和 capability revalidation 契约；公开 Port 不暴露 ORM/driver 类型。
- 新增独立、未接生产的 GORM Process Composition；其 Repository 字段暴露稳定 Workspace 接口聚合，不暴露具体 DB 或 `*workspacepostgres.Repository`。
- 现有 `ProcessComposition` 及 API/Worker/workspacectl/workspaceprobe 接线在 Final 前保持 legacy；本 child 不扩大修改到 `cmd/**`。

### R6. SQL、错误、取消与安全

- 迁移文件是唯一 Schema 事实源；禁止 AutoMigrate/Migrator、`gorm.Model`、Hook、soft delete、自动时间、Save 和 ORM association。
- no-row 同时识别 pgx、`database/sql` 与 GORM；保留现有精确 SQLSTATE/constraint-name、RowsAffected/CAS、retryability 和 result-unknown 分类，不扩大到整个 SQLSTATE class。
- nil context fail closed；cancel/deadline 保留标准 sentinel 与 `context.Cause`；`sql.ErrTxDone`、begin/commit/rollback 失败映射为稳定、安全错误。
- 错误和日志不得包含 SQL 参数、root path/fingerprint、幂等键、Git commit、DSN、Secret 或绝对路径。

### R7. 验证与完成门禁

- 按父任务 `research/lean-test-policy-2026-09-01.md` 执行受影响包既有测试、vet、diff 与 task 校验，并以一个共享 Testcontainers 主路径复核核心 Workspace 行为。
- Workspace 的权限、隔离、状态机、幂等和事务原子性仍为硬门禁；response-loss、取消/连接释放、EXPLAIN、整包 race 和重复 compile/module 检查仅按直接改动风险触发。

## Acceptance Criteria

- [x] staged `GORMRepository` 实现 Workspace/Source、Registry、Control、Runtime 和 Git capture 全部能力，生产仍使用 legacy pgx。
- [x] Source 注册/replay/conflict、root grant、keyset/LATERAL 读取与损坏数据 fail-closed 在真实 PostgreSQL 上等价。
- [x] Registry/rebind 的 advisory lock 顺序、history、Audit 原子性、CAS、触发器和 response-loss replay 在真实 PostgreSQL 上等价。
- [x] Control/Runtime 的 repeatable-read snapshot、DB time、lease/takeover、phase/grant fence、rollback 与提交错误在真实 PostgreSQL 上等价。
- [x] Git checkpoint 的并发串行、顺序 fence、exact replay、tombstone/reappearance 和 Source 写入原子性在真实 PostgreSQL 上等价。
- [x] opaque `ScopedSourceWriter` 在 caller-owned UoW 内保持 Source/Artifact/Version commit/rollback 且无 driver 泄漏；GORM rootgrant/Runtime Composition 无 driver 泄漏，Capture legacy writer 和所有生产接线保持不变。
- [x] 局部 test/race/vet/compile、module/vendor、Go Review、SQL Review、Trellis validate、gofmt 与 `git diff --check` 通过。

## Out Of Scope

- 迁移 Capture Repository、改写 Capture 的 pgx 事务或声称 Source/Capture/Outbox/receipt 已切到 opaque scope。
- 生产 Composition 切换、删除 pgx、修改 API/Worker/workspacectl/workspaceprobe wiring 或下游 concrete helper 签名。
- 修改业务状态、错误码、cursor wire format、Schema、migration、trigger、constraint 或索引。
- AutoMigrate、ORM association/preload、缓存、双写、数据回填、物理删除或新的 Workspace/Control/Git 行为。
- 新增测试文件；TODO 9 只原位扩展/参数化现有测试，且需真实数据库门禁可用。

## Dependencies

- 前置：`gorm-platform-transaction-foundation` 提供共享 Pool、GORM root、Unit of Work 与 opaque transaction scope。
- 前置协作：`gorm-audit-migration` 提供 `auditapplication.ScopedAppender`；Workspace rebind 必须和 Audit 使用同一 scope。
- 下游：Capture 依赖本 child 的 `ScopedSourceWriter`；Capture child 负责把 Source/Capture/Outbox/receipt 迁入同一 UoW。
- Final 统一以同一 Pool 切换 API/Worker/CLI Composition 并收口 legacy pgx writer；若还要求运行时拒绝 active foreign-Pool scope，则须另由 Foundation 提供 affinity 能力。
