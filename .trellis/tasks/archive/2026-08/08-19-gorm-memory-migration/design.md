# Memory GORM 迁移技术设计

## 1. Boundary

- 保留 `internal/memory/adapter/postgres.Repository` 和 `NewRepository(DB)` 作为生产 pgx 路径。
- 在同包新增 `GORMRepository` 与 `NewGORMRepository(*gorm.DB, foundation.UnitOfWork)`，编译期断言其实现 `application.Repository`。
- 新路径只接受同一 Foundation Pool 的 `Pool.GORM()` root 和 `Pool.UnitOfWork()`：root 只用于 `Get/List/LoadEffective`，`FindCommand/CreateCandidate/Mutate/ExpireDue` 统一通过 `UnitOfWork.Within` 建立事务，并在 Adapter 内用 `platformpostgres.GORMTransaction(scope)` 受控解包。
- Repository 不持有 `pgxpool.Pool`、不在模块内直接调用 root `Transaction`、不独立建池、不改动 Application/Domain 端口。这遵循 Foundation “写事务通过项目 Unit of Work 显式建立”的固定配置。
- 四个 transaction-owning 方法统一传 `foundation.TransactionOptions{}`，表示 PostgreSQL 默认隔离级别且 `ReadOnly=false`，与现有 `pgx.Begin(ctx)` 一致。`Get/List/LoadEffective` 保持 root 上的单条只读查询，不额外建立事务。
- Schema 继续由 migrations 管理；GORM 只是连接/事务/查询执行边界，不运行 AutoMigrate 或 Migrator。

## 2. File Layout

- `gorm_repository.go`：构造防御、七个端口方法、GORM transaction 边界、context/no-row/error 适配。
- `gorm_queries.go`：GORM `?` placeholder 版的所有参数化 SQL，显式列表和锁语句；不用动态用户输入拼接标识符。
- `gorm_model.go`：仅定义 Adapter 私有 JSONB carrier 与必要的 insert record/table mapping；不复制 Domain 模型，不定义 association/hook/soft delete。
- 复用 `codec.go` 的 `persistedCommand`、snapshot codec、`scanMemory` 与值映射；若需共享仅数据库无关的 validation/equality helper，做最小抽取并保留 legacy 路径语义。

## 3. Transaction Flows

### 3.1 FindCommand

```text
Foundation UnitOfWork.Within -> GORM transaction scope
  -> advisory lock(workspace, client key)
  -> SELECT memory_command ... FOR UPDATE
  -> miss: commit and return found=false
  -> exact binding check + strict snapshot decode
  -> commit and return replay
```

No-row 必须用 `Row().Scan` / `Rows()+Next` 显式识别，不用 `Raw().Scan` 的零值结果推断 miss。

### 3.2 CreateCandidate

```text
Foundation UnitOfWork.Within -> GORM transaction scope
  -> client-key advisory lock
  -> exact command replay / conflict
  -> [INTERVIEW only] provenance advisory lock
  -> [INTERVIEW only] locked semantic replay
  -> workspace SELECT ... FOR KEY SHARE
  -> INSERT memory ... RETURNING explicit columns
  -> compare returned aggregate with requested candidate
  -> INSERT memory_audit
  -> INSERT memory_command
  -> commit
```

Interview 结构化 session/path/step 列由现有数据库 trigger 从 canonical `source_ref` 投影；GORM insert 不主动覆盖这些列。

### 3.3 Mutate

```text
Foundation UnitOfWork.Within -> GORM transaction scope
  -> client-key advisory lock
  -> exact command replay / conflict
  -> SELECT owner-scoped memory ... FOR UPDATE
  -> full snapshot + expected-version comparison
  -> [next ACTIVE with expiry] SELECT clock_timestamp()
  -> UPDATE explicit columns WHERE workspace/id/owner/version RETURNING
  -> compare returned aggregate with requested next state
  -> INSERT memory_audit after aggregate reached new version
  -> INSERT memory_command
  -> commit
```

CAS 零行映射 `MEMORY_VERSION_CONFLICT`。不使用 `Save` 或 struct `Updates`，避免 nil 字段清空失效和隐式列改写。

### 3.4 ExpireDue

```text
Foundation UnitOfWork.Within -> GORM transaction scope
  -> SELECT clock_timestamp() once
  -> SELECT bounded due rows ORDER BY expires_at,id FOR UPDATE SKIP LOCKED
  -> for each locked row: Domain Expire -> version CAS UPDATE RETURNING -> audit insert
  -> commit and return batch count
```

现有最大批次为 100。循环不并发，不移出事务；该路径的锁分工与审计唯一性必须由真实 PostgreSQL 验证。

## 4. Query And Mapping Strategy

- 锁、`RETURNING`、tuple keyset、PostgreSQL array、DB clock 与 Interview join 继续使用 GORM `Raw`/`Exec`；这是受控 PostgreSQL SQL，不尝试改写成 association CRUD。
- 所有 Memory SELECT/RETURNING 使用与 legacy `memoryColumns` 同序的显式列清单，并继续通过 `scanMemory` 和 Domain validator 进入领域。
- GORM `*sql.Rows` 使用前检查 error/nil，成功后 `defer Close()`，循环后检查 `rows.Err()`。
- `List` 的 type/status 使用 `pq.Array` 或等价 `driver.Valuer` 作为单个 `?::text[]` 参数；nil 表示不限定，不传裸 `[]string`。
- JSONB carrier 的 `Value()` 返回受验证 JSON string，`Scan()` 只接受 string/bytes 并复制数据，保留 strict decode/canonicalization。
- 时间统一 UTC 微秒，不让 GORM `NowFunc`/autoCreateTime/autoUpdateTime 生成领域事实。

## 5. Error And Context Compatibility

- 新 `gormClassify(ctx, err)` 的优先级固定为：已有 `foundation.Error` 原样保留 -> 原始 `ctx.Err()` -> `sql.ErrTxDone` -> `sql.ErrNoRows` / `pgx.ErrNoRows` / `gorm.ErrRecordNotFound` -> `*pgconn.PgError` -> generic unavailable。transaction callback、`Rows`、Unit of Work/commit 返回的错误全部携带原始 ctx 进入该分类。
- no-row 的最终语义由调用点决定：receipt lookup 是 `found=false`，owner Get 是 NotFound，CAS `RETURNING` 是 VersionConflict。
- `context.Canceled` 保留 non-retryable 与 `errors.Is`；`context.DeadlineExceeded` 按 legacy 保留 retryable dependency unavailable。因此 context 已取消时，即使 database/sql 只返回 `sql.ErrTxDone`，也必须以 `ctx.Err()` 为 cause 恢复取消语义。
- GORM transaction 外层错误不得覆盖 callback 返回的 Domain/foundation error；commit/rollback/driver 错误按同一 Memory SQLSTATE 矩阵分类。

## 6. Verification Design

### Static Stage

- 保留现有 codec/application/domain/http 测试，执行 Memory 包 test/race/vet。
- integration build tag 只做 compile-only；检查 GORM 实现无 pgx 公共签名、无 AutoMigrate/Migrator、无 `cmd/**` 切换。
- API/Worker 做 compile-only，确认 legacy wiring 仍可构建。

### TODO 9 PostgreSQL Stage

- 仅扩展 `repository_integration_test.go`，不新建测试文件。fixture 先使用临时 pgx migration pool 完成 migrations 并关闭它，再针对同一 disposable database 调用普通 `platformpostgres.Open`；从这一 runtime Pool 取 `DB()`、`GORM()` 与 `UnitOfWork()`。清理顺序是 runtime Pool `Close()` 后由 admin pool drop database。
- 现有三类 contract test 按 legacy/GORM 参数化，每个实现使用独立数据库，防止固定 UUID/key 互相污染。
- 在同一现有文件补齐：audit/receipt 失败整体回滚；append-only 修改拒绝；并发 `ExpireDue` 不重复处理；keyset/effective/expiry 等价；context cancel/deadline；共享 Pool 可见性与事务隔离。

## 7. Compatibility, Rollback And Deferred Work

- staged 实现无生产流量，回滚为删除本 child 新增 Adapter 文件；不需回滚 Schema 或数据。
- Final child 才切换 API/Worker 构造、更新 Composition 测试并删除 legacy pgx 实现。
- TODO 9 之前的最大剩余风险是 GORM/database/sql 在真实 PostgreSQL 上的 advisory lock、`FOR UPDATE SKIP LOCKED`、Raw JSONB/array 绑定、trigger SQLSTATE 与事务 rollback 行为尚未被证明。
