# Local Model Runtime 静态验证记录

## 实现范围

- 新增 `internal/localmodelruntime/gorm_core.go`、`gorm_root.go`、`gorm_probe.go`、`gorm_scoped.go`。
- `internal/localmodelruntime/lifecycle_store.go` 只新增 `ScopedTxLifecycle`，保留 legacy `TxLifecycle/WithTx(pgx.Tx)`。
- 未修改 `cmd/**`、Model Settings legacy wiring、migration、credential-init 或现有测试；生产仍使用 `NewPostgresStore(database.DB())`。
- GORM 构造只接受共享 `*platformpostgres.Pool`，从 `Pool.GORM()`/`Pool.UnitOfWork()` 取得 root 和 UoW；没有第二物理池、AutoMigrate、Migrator、Hook、Association 或 fallback。

## 已执行门禁

以下命令在当前工作树通过：

```text
go test -mod=vendor ./internal/localmodelruntime/... ./internal/modelsettings/adapter/postgres ./internal/modelsettings/runtime -count=1 -timeout 60s
go test -race -mod=vendor ./internal/localmodelruntime/... -count=1 -timeout 60s
go vet -mod=vendor ./internal/localmodelruntime/... ./internal/modelsettings/adapter/postgres ./internal/modelsettings/runtime
go test -mod=vendor -tags=integration -run '^$' ./internal/platform/migration ./internal/localmodelruntime/... -count=1 -timeout 60s
go test -mod=vendor -run '^$' ./cmd/local-model-runtime ./cmd/local-model-runtime-credential-init ./internal/modelsettings/adapter/postgres ./internal/modelsettings/runtime -count=1 -timeout 60s
go list -mod=vendor ./internal/localmodelruntime/... ./internal/platform/migration ./cmd/local-model-runtime ./internal/modelsettings/...
go mod verify
python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-localmodelruntime-migration
gofmt -d internal/localmodelruntime/gorm_*.go internal/localmodelruntime/lifecycle_store.go
git diff --check
go test -mod=vendor -run '^$' ./internal/...
go test -mod=vendor -run '^$' ./cmd/...
```

静态扫描确认新文件没有 `gorm.Open`、`sql.Open`、`pgxpool.New*`、`AutoMigrate`、`Migrator` 或 credential-init/命令接线引用。 `go mod verify` 输出 `all modules verified`。

## Review 结论

- Go review：scope 每次操作重新检查 active；nil/typed-nil store、scope、context 被拒绝；自有 UoW 的 callback/commit error 返回零值结果；rows 检查 `Scan`、`Rows.Err` 和 `Close`；context cause 保留；无 process/network I/O 进入事务。
- SQL review：固定 `SECURITY DEFINER` 函数名、显式 `?` 参数与 `uuid/interval/jsonb` casts、`clock_timestamp()`、CAS/idempotency、`FOR UPDATE`、Repeatable Read/Read Only 快照均保留；JSONB 使用 string carrier；无动态外部标识符或 N+1。
- Trellis check：PRD/design/implement 与 legacy consumer、生产接线和回滚边界一致；任务仍为 `in_progress`，PRD AC/TODO9 未勾选。
- 复核修正：`CompleteTestProbe` 的 GORM `UPDATE ... RETURNING` 在 `Scan` 阶段无行时现在映射为与 legacy 一致的 `LOCAL_MODEL_RUNTIME_CONFLICT`，不会把 CAS 失败暴露为裸 `sql.ErrNoRows`。

## 未验证项

当前没有 `ZHIXU_TEST_DATABASE_URL`，因此没有真实 PostgreSQL 证据证明 GORM `database/sql` 参数类型、SECURITY DEFINER 权限、trigger/SQLSTATE、锁竞争、commit response-loss、scope commit/rollback 可见性、连接释放或 EXPLAIN。Foundation `TransactionScope` 当前不带 Pool identity，foreign-pool active scope 无法由本 child 运行时拒绝；同池由后续 Composition/TODO9 门禁保证。不能把 integration compile-only 或现有 pgx 集成测试当作 GORM 等价验收，不能勾选 PRD AC 或归档任务。

## 2026-08-27 TODO 9 fixture 筛选复核

- 该 child 的唯一既有 managed-Ollama 实库 fixture 是 `internal/platform/migration/managed_ollama_integration_test.go`，其包名为 `migration`。
- `internal/platform/testdb` 为统一 factory，但 `fixture.go` 依赖 `internal/platform/migration` 执行 Goose migration；同包测试导入 `testdb` 会形成 Go import cycle（实证：`go test -mod=vendor -tags=integration -run '^$' ./internal/platform/migration` 返回 `import cycle not allowed in test`）。
- 因此当前无法在“不改 migration 测试包边界、不改 shared factory、不复制 fixture 生命周期”的条件下接入 shared factory。改为外部测试包还需重写多个未导出的 runner/provider helper，已超出“只差 fixture 接入”的筛选条件。
- 未修改该 integration 文件，未运行真实 PostgreSQL legacy/GORM 回归；child 保持 `in_progress`，PRD AC 和 TODO 9 清单不勾选。
