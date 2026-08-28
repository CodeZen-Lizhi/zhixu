# Local Model Runtime GORM 迁移设计

## 1. 目标与阶段边界

Local Model Runtime 不是普通 CRUD：它维护 manager lease、runtime owner、model demand、hold、operation/attempt 状态机以及 test probe 的原子释放。迁移只增加 staged GORM 实现，不改 Schema、不改进程/网络副作用、不改命令 Composition。

TODO 9 真实 PostgreSQL 门禁通过前：

- 保留 `PostgresStore`、`NewPostgresStore(DB)`、legacy `TxLifecycle`/`WithTx(pgx.Tx)` 和所有现有生产接线；不改 `cmd/**`、Model Settings legacy adapter、migration、credential-init 或测试行为。
- 新增 `GORMStore`/`NewGORMStore(*platformpostgres.Pool)`，实现现有 root Store 能力，并新增 `ScopedTxLifecycle`/`WithScope(foundation.TransactionScope)`，供后续 Model Settings GORM child 在同一事务中调用。
- 不增加运行时 selector、双写、异步补偿或第二连接池；GORM 实现通过共享 Pool 的 GORM root/UoW，无法验证或执行的真实 DB 语义保留为 TODO 9。

直接替换 `TxLifecycle` 会破坏 `internal/modelsettings/adapter/postgres` 当前以 `pgx.Tx` 持有的原子链（seed/complete/fail/recover）。因此 legacy 端口是有明确 owner 和退出条件的临时 allowlist：Model Settings child 切换到 Unit of Work/scoped Port 后，Final 再删除它。

## 2. Schema 与安全事实

`migrations/00080_managed_ollama_runtime.sql` 是唯一 Schema 事实源，包含：

- `ops.managed_ollama_runtime` singleton：owner/epoch/version、requirement hash/models、observed phase、lease 和 DB 时间字段；数据库 trigger 约束 owner takeover、phase、hash、版本和时间。
- `ops.managed_ollama_holds`：owner kind/id/epoch、revision/rollout/operation、requirement/models、lease/released/version；终态 operation 必须释放 hold。
- `ops.managed_ollama_operations`：kind/idempotency/request hash、target/rollout binding、requirement、phase、attempt/progress、claim lease、terminal/error；唯一键和 trigger 固定 replay/CAS/状态图。
- `ops.managed_ollama_revision_requirements` security-barrier view：只暴露允许的 local model 字段。
- `ops.model_settings_state` 仅授予 runtime role 的指定列 SELECT。

runtime role 只获得上述只读表/view 和 10 个 `SECURITY DEFINER` command function 的 EXECUTE；GORM Store 必须继续调用固定函数 `SELECT ...`，不能改为对表的 Create/Save/Updates。`cmd/local-model-runtime-credential-init` 的管理员连接、角色属性/成员资格校验、`ALTER ROLE`、0600 文件和密码清理留在 Final 安全 allowlist。

禁止 AutoMigrate/Migrator、GORM Hook、association、soft delete、隐式时间戳或在事务内执行 Ollama/文件/网络 I/O。

## 3. Port 与连接边界

新增应用侧契约保持 database-agnostic：

```go
type ScopedTxLifecycle interface {
    WithScope(foundation.TransactionScope) (TxStore, error)
}
```

`TxStore` 的四个方法仍是 `SeedActivationPreparation`、`ReadOperationByRollout`、`ReadOperationByTarget`、`CompleteActivationPreparation`，但绑定实现只保存 opaque scope，并在每次调用时用平台 `GORMTransaction(scope)` 重新确认 scope 仍 active；不缓存裸 `*sql.Tx`/`*gorm.DB` 到跨 callback 生命周期，也不 commit/rollback。

`GORMStore` 构造器必须接收完整 `*platformpostgres.Pool`，调用 `Pool.GORM()` 与 `Pool.UnitOfWork()`；禁止 `gorm.Open`、`sql.Open`、DSN 重连或独立 pgx pool。平台 scope 当前没有 Pool identity，active foreign-Pool scope 无法运行时拒绝；Composition/TODO 9 必须保证同一 Pool，限制记录在静态验证中。

## 4. 方法分层

### 4.1 Root 单语句/函数调用

以下方法使用共享 GORM root 的 `Raw(...).Row().Scan` 或 `Exec`，所有 SQL 使用 `?` 参数并保留显式 casts；固定 SECURITY DEFINER 函数名不接受外部标识符：

- `ClaimManager`、`HeartbeatManager`；
- `PublishDemand`、`LoadEffectiveIntent`、`SeedActiveRecovery`、`ActiveRecoveryCurrent`；
- `ClaimOperation`、`BeginPullAttempt`、`RecordOperationProgress`、`CompleteOperation`、`SweepExpiredOperations`；
- `AcquireHold`、`RenewHold`、`ReleaseHold`、`CompareAndSetRuntimePhase`；
- `ReadTestOperation`。

Raw scanner 复用现有领域 mapper（ID、enum、hash、nullable、JSON models、UTC 时间）并在 no-row 时统一识别 `sql.ErrNoRows` 与 legacy `pgx.ErrNoRows`，映射为原有 `LOCAL_MODEL_RUNTIME_CONFLICT` 或原始安全错误。

### 4.2 Owned UoW

通过 `Pool.UnitOfWork().Within` 保留既有事务边界：

- `SeedTestPreparation`：operation idempotency/replay 与 hold insert/read 同一事务；
- `ClaimTestProbe`：CAS claim、精确 operation 回读/ownership 判断同一事务；
- `CompleteTestProbe`：probe CAS 与 terminal hold release 同一事务；
- `ReadDemand`：`RepeatableRead + ReadOnly`，在同一 snapshot 读取 runtime、settings state、holds、operations、revision requirements，并统一计算 demand；
- `CompleteTestPreparation` 的 legacy 兼容流程保持既有 root 调用顺序，不把外部探测放入事务。

callback 失败回滚；callback 成功后的 commit error 原样保留并标记为不可确定，绝不重放或再开补偿事务。所有 rows 显式 `Close` 并检查 `Rows.Err`。自有 UoW 在任意错误（包括 commit error）时返回零值结果，避免把未确认的 operation/snapshot 当作已提交事实；调用方只能使用无错误返回的结果。

### 4.3 Scoped caller-owned UoW

`WithScope` 只用于四个 Model Settings activation preparation 方法。调用时先校验 nil context、scope 类型/active、ID/command；然后在 caller scope 内执行与 legacy 相同的 `INSERT ... ON CONFLICT`、`FOR UPDATE`、hold identity 校验和 operation terminal CAS。它不 fallback 到 root GORM，不自建事务，不执行 commit/rollback。

## 5. SQL、时间与错误

- 所有外部值均参数化；`?::uuid`、`?::interval`、`?::jsonb` 显式保留类型。model list/requirement JSON 使用已验证的 JSON string carrier，不能把裸 `[]byte` 作为 JSONB 参数。
- 所有 lease/expiry/updated/terminal/release 判定继续使用 PostgreSQL `clock_timestamp()`；应用 `time.Now` 不参与持久状态判断。
- CAS 零行、唯一冲突、过期 owner、非法状态和 trigger SQLSTATE 保留 legacy 冲突/依赖语义；不把所有 `08xxx` 或未知错误扩大为可重试。
- context nil 立即拒绝；取消/超时返回保留 `ctx.Err()` 和不同的 `context.Cause(ctx)`，不吞掉 caller cause。错误文本不得包含 SQL、参数、DSN、model secret、credential 或绝对路径。
- GORM `TranslateError` 保持平台 `false`，允许 `*pgconn.PgError` cause 继续被 `errors.As` 识别。

## 6. 生产接线与回滚

生产仍使用：

- `cmd/local-model-runtime/main.go` 的 `platformpostgres.Open` + legacy `NewPostgresStore(database.DB())`；
- `internal/modelsettings/runtime/bootstrap.go` 的 legacy lifecycle option；
- `cmd/local-model-runtime-credential-init` 的原生 pgx 管理路径。

TODO 9 前回滚只删除本 child 新增 GORM/scoped 文件和文档；不修改 migration、legacy adapter、命令或 credential bootstrap。TODO 9 后由 Model Settings/Final 统一切换，先验证同一 Pool、同一 scope 和 role 权限，再删除 legacy allowlist。

## 7. TODO 9 实库门禁

用现有 `internal/platform/migration/managed_ollama_integration_test.go` 原位扩展 implementation factory：每个 legacy/GORM 子用例创建独立已迁移数据库，关闭迁移用 raw pool 后用 `platformpostgres.Open` 得到完整 Pool；legacy 使用 `Pool.DB()`，GORM 使用同一 Pool 的 `GORM()`/UoW。必须覆盖：

- manager claim/heartbeat stale takeover、owner/version CAS、DB-time/future lease；
- ReadDemand repeatable-read snapshot 与 settings/holds/operations 混合写竞争；
- test preparation/probe success/abandon/expired/ownership conflict，operation/hold 原子回滚；
- active recovery seed/supersede、operation attempt budget/progress/terminal/release、runtime phase trigger/SQLSTATE；
- scoped activation seed/read/complete 在同一 caller transaction 的 commit/rollback/visibility；
- JSONB models、nullable IDs/times、cancel/deadline cause、rows close/connection release、role permission and SECURITY DEFINER function access；
- 目标查询 EXPLAIN/statement count 与 legacy 对等。

当前无 `ZHIXU_TEST_DATABASE_URL` 时只做 integration compile，不能把 skip 当成验收或勾选 AC。
