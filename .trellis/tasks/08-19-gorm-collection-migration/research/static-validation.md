# Collection staged GORM 静态验证

## 结论

Collection staged GORM Repository、共享 Unit of Work 事务路径和 scoped durable binding verifier 已实现。局部单测、race、vet、integration 编译、受影响 Composition 编译、vendor/module 与任务校验均通过。生产 Composition、现有测试 fixture、Graph/Health pgx verifier 和数据库 Schema 均未切换。

当前环境未配置 `ZHIXU_TEST_DATABASE_URL`，因此本文只证明静态与编译就绪，不作为 TODO 9 真实 PostgreSQL 等价证据。`task.json` 保持 `in_progress`，PRD AC 与 TODO 9 清单保持未勾选。

## 实现范围

- 新增 `internal/collection/adapter/postgres/gorm_adapter.go`：共享 GORM/database/sql 事务适配、最终 SQL positional renderer、array/JSONB carrier、Row/Rows scanner 与 GORM 错误分类。
- 新增 `internal/collection/adapter/postgres/gorm_repository.go`：完整 staged Repository 能力、默认写事务、只读 repeatable-read snapshot 和 caller-owned scoped verifier。
- `internal/collection/application/membership.go` 仅新增 opaque `ScopedDurableScanBindingVerifier`，没有暴露 GORM、database/sql、pgx 或 `any`。
- legacy `repository.go`、`query.go`、`durable_scan.go` 只抽取共享 scanner/validation 和 GORM scanner mode 接入；legacy `NewRepository` 与 `VerifyDurableScanBinding(pgx.Tx)` 保留。
- 未修改 `cmd/**`、migration 或测试文件；未新增 AutoMigrate、Migrator、Hook、association/preload、soft delete、第二连接池、双写或 fallback。

## 关键静态事实

- `NewGORMRepository` 只接受完整 `*platformpostgres.Pool`，从同一 Pool 获取 GORM root 与 UnitOfWork。
- Create/Update/Archive 保持 Workspace lock -> immutable receipt lookup -> aggregate lock/CAS -> aggregate/receipt write 的既有顺序。
- List/Results/Preview/Plan/Read/Revision Verify 使用 repeatable-read、read-only UnitOfWork，并保留 1500ms `SET LOCAL statement_timeout`。
- scoped binding verifier 只 unwrap active `foundation.TransactionScope`，不 commit、rollback 或回退 root；Foundation scope 暂无 Pool owner identity，active foreign-Pool scope 只能由 TODO 9 fixture 与 Final Composition 保证同池。
- `$n -> ?` renderer 按出现顺序展开重复 marker，支持多位序号，跳过单/双引号、行/嵌套块注释和 dollar quote，并拒绝 `$0`、越界、残缺及未使用参数。
- `[]string`/`[]foundation.ID` 与其他非 byte slice 经 `pq.Array` 作为单参数绑定；durable node `text[]` 经 `pq.StringArray` 扫描。
- query/view/receipt/hydration JSONB 经严格 Scanner/Valuer，写入返回 string 并依靠固定 `::jsonb` cast，避免 database/sql 将 `[]byte` 推断为 bytea。
- Row/Rows 检查 statement error、nil、Close/Err；scanner 对跨 Workspace、重复、缺失、损坏 JSON/ID/时间/版本数据 fail closed，不返回 partial result。
- `classifyGORM` 保留 `ctx.Err()` sentinel 与不同的 `context.Cause(ctx)`，处理 `sql.ErrTxDone`、no-row，并复用 legacy 精确 SQLSTATE 分类。

## 生产接线证据

- API 的 4 个 Collection 构造点与 Worker 的 1 个构造点仍调用 `collectionpostgres.NewRepository(...)`。
- `internal/graph/adapter/postgres/scan_repository.go` 与 `internal/health/adapter/collection/membership.go` 仍调用 legacy `VerifyDurableScanBinding(ctx, pgx.Tx, ...)`。
- `NewGORMRepository` 与 `VerifyDurableScanBindingScoped` 没有生产调用点。
- Collection Application/Domain 没有 GORM、database/sql 或 pgx import；Application 只引用 Foundation transaction scope。

## 已执行门禁

以下命令均通过：

```text
go test -mod=vendor ./internal/collection/... -count=1 -timeout 60s
go test -race -mod=vendor ./internal/collection/... -count=1 -timeout 60s
go vet -mod=vendor ./internal/collection/... ./internal/platform/postgres ./internal/foundation
go test -mod=vendor -tags=integration -run '^$' ./internal/collection/... -count=1 -timeout 60s
go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker ./internal/graph/... ./internal/health/... ./internal/export/... ./internal/organizing/... -count=1 -timeout 60s
go list -mod=vendor ./internal/collection/... ./cmd/api ./cmd/worker ./internal/graph/... ./internal/health/... ./internal/export/... ./internal/organizing/...
go mod verify
python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-collection-migration
gofmt -d <affected Go files>
git diff --check
```

`go mod tidy -diff` 已运行但未应用；输出只显示本任务开始前已有的全仓 `go.sum` 规范化漂移和 sqlite checksum 差异，Collection 本次未改 `go.mod`、`go.sum` 或 vendor。

## Review

- Go Review：未发现 P0/P1 代码缺陷。确认公开 Port、构造器、UoW/scope、context cause、Rows/scanner、cursor 与 legacy 接口兼容；P2 仅为 staged GORM 路径缺少真实 PostgreSQL 直接测试。
- SQL/事务 Review：未发现 P0-P3 明确缺陷。确认 renderer、array/JSONB、Workspace predicate、写锁/receipt/CAS、read snapshot、keyset、hydration、durable revision/binding 与 Rows 资源处理保持 legacy 语义。
- Trellis Check：任务工件、实现范围、生产接线、Graph/Health legacy 依赖、验证证据和 TODO 9 状态一致；最终局部 test、vet、integration compile、task validate 与 diff check 复跑通过。
- 前置审计发现的 `text[] -> *[]string` database/sql 扫描风险已通过 `pq.StringArray` scanner mode 修复并复核。

## TODO 9 未验证项

- 真实 pgx-stdlib/GORM placeholder、重复/多位 marker、`pq.Array`、JSONB 和 nullable scan 行为。
- lifecycle、idempotency、CAS、archive、SQLSTATE、trigger/constraint、commit/rollback 和 response-loss replay 等价。
- stable keyset、cursor binding/stale、Results/Preview count/page/hydration/revision 同快照与 corrupt row no-partial。
- durable Plan/Read/restart、pair/node hydration、revision/count drift、scoped caller transaction 与 Graph/Health 跨模块原子性。
- `context.WithCancelCause`、deadline、`sql.ErrTxDone`、连接释放、目标规模 EXPLAIN/P95 和 statement count。

TODO 9 必须在每个 implementation 独立、已迁移的 disposable database 中执行；seed/cleanup 使用已提交的同一 `platformpostgres.Pool.DB()`，GORM 从该 Pool 构造，禁止复用外层未提交 pgx fixture。全部通过后才能勾 PRD AC、完成 child，并交给对应模块/Final 切换生产 Composition。

## 2026-08-27 TODO 9 partial gate

- `repository_integration_test.go` 已接入 `testdb.Require`；每个 legacy/GORM 子用例各自取得一个迁移后的 disposable database，并从同一 `platformpostgres.Pool` 构造 `NewRepository(pool.DB())` 与 `NewGORMRepository(pool)`。seed 通过该 Pool 提交，不再依赖外层未提交事务。
- 真实命令 `go test -race -mod=vendor -tags=integration -run '^TestCollectionRepositoryLifecycleIdempotencyCASAndWorkspaceIsolation$' -count=1 -p 1 -timeout 8m ./internal/collection/adapter/postgres` 通过；legacy 与 GORM 均覆盖 create/replay、idempotency conflict、CAS/version conflict、archive immutable、Workspace isolation、稳定 keyset、cursor invalid/stale。
- `go vet -mod=vendor ./internal/collection/adapter/postgres`、`go test -mod=vendor ./internal/collection/... -count=1 -timeout 60s`、`python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-collection-migration` 与 `git diff --check` 通过。
- child 仍保持 `in_progress`：`query_integration_test.go`、`query_contract_integration_test.go`、`durable_scan_integration_test.go` 和 HTTP replay fixture 尚未接入 shared factory。尤其 `TestCollectionQuerySnapshotCountAndReferenceP95` 仍直接读取 `ZHIXU_TEST_DATABASE_URL`、创建带手工 pgx tracer 的第二 pool；这违反同一 `platformpostgres.Pool`/禁止平行连接池约束，且当前 factory 没有 tracer 注入能力。其他 query/durable tests 仍把 seed 放在外层未提交 pgx transaction 中，GORM root 无法观察该数据。故不能勾选 PRD AC、归档或宣称模块完成。
