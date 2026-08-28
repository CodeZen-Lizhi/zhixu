# Model Settings staged GORM 静态验证

## 结论

已新增 sibling `GORMRepository`、scoped Audit/Local Runtime/Workflow fence 和独立 `BootstrapGORM`。legacy `Repository`、`Bootstrap`、三处 `cmd/**` 生产接线、迁移与测试文件均未修改；当前实现仅为 TODO 9 前的 staged 路径。

实现覆盖 Revision、Rollout、Runtime、Runtime Availability、Participant、Activation、Snapshot 共 25 个 Application 方法，并增加 opaque scope 的 `CheckEnqueue`。Model Settings、Audit 与 Local Runtime 均从同一个 `*platformpostgres.Pool` 获取 GORM root/UoW；没有第二连接池、AutoMigrate、ORM association/hook 或事务外 fallback。

## 原子边界与审查证据

- `SaveDesired` 在同一 UoW 内完成 immutable revision、state desired 更新、persisted binding 校验与 redacted Audit `AppendScoped`。
- Start/Advance/Fail/Finalize/Recover 在 caller scope 内通过 `ScopedTxLifecycle.WithScope` 组合 Local Runtime preparation；TxStore 不拥有 commit/rollback。
- `CheckEnqueue` 只 unwrap live scope，并在该事务读取 singleton state `FOR SHARE`；没有 root fallback 或 no-op。
- Activation 保持 State -> Runtime `ORDER BY role FOR UPDATE` -> Participant `ORDER BY role FOR UPDATE`；单角色路径保持 State -> Runtime -> Participant。
- Snapshot 通过 `TransactionIsolationRepeatableRead` + `ReadOnly` 在一个 UoW 内读取 state/revision/runtime/participant/local facts。
- 所有 lease/freshness 继续使用 `clock_timestamp()`；写路径保留完整 CAS、`RETURNING`、显式 UUID cast 与稳定 no-row 错误码。
- SecretSealer/AAD、密文列、Audit metadata 与安全错误字符串沿用 legacy 规则；GORM logger 由平台保持 silent。

Go Review、SQL Review 与 Trellis Check 未发现剩余 P0-P2。Review 中修复了一项取消语义偏差：Activation 的 GORM `RETURNING` 不再复用会提前 pgx-classify 的 legacy state scanner，而由 `gormScanState` 保留原始 `sql.Row.Scan` cause，再按当前 context 分类。

## 已通过门禁

```text
go test -mod=vendor ./internal/modelsettings/... -count=1 -timeout 60s
go test -race -mod=vendor ./internal/modelsettings/... -count=1 -timeout 60s
go vet -mod=vendor ./internal/modelsettings/... ./internal/audit/... ./internal/localmodelruntime/... ./internal/platform/postgres
go test -mod=vendor -tags=integration -run '^$' ./internal/modelsettings/adapter/postgres ./internal/platform/migration -count=1 -timeout 60s
go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker ./cmd/modelctl ./cmd/local-model-runtime -count=1 -timeout 60s
go list -mod=vendor ./internal/modelsettings/... ./internal/audit/... ./internal/localmodelruntime/... ./internal/workflow/adapter/river ./cmd/api ./cmd/worker ./cmd/modelctl
go mod verify
go mod tidy -diff
python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-modelsettings-migration
gofmt -d <本任务 Go 文件>
git diff --check
```

`go mod tidy -diff` 通过且无输出；未修改 `go.mod` 或 `go.sum`。

静态扫描确认：Model Settings Domain/Application 不导入 GORM、database/sql 或 pgx；staged 文件不含 AutoMigrate/Migrator/Save/Preload/Association、自建 DSN/第二 pool；`BootstrapGORM` 没有生产调用点。现有测试没有 `NewGORMRepository`/`BootstrapGORM` 引用，符合当前阶段“不改测试”的边界，但也构成下述运行时盲区。

## TODO 9 真实 PostgreSQL 阻断

环境未设置 `ZHIXU_TEST_DATABASE_URL`，因此以下不能由静态检查或 legacy 测试替代：

- GORM placeholder/cast、bytea/nullable/time scan、trigger SQLSTATE 和 no-row 的真实执行。
- Revision + Audit、Activation + Local Runtime、Fence + River 三条跨 owner 同事务提交/回滚。
- 双进程 State/Runtime/Participant 锁竞争、DB-time lease、CAS、死锁/serialization 分类。
- Snapshot repeatable-read/read-only 一致性、cancel/deadline/commit ambiguity、连接释放与 EXPLAIN。
- Foundation scope 没有 Pool identity；只能由同池 Composition 和 TODO 9 fixture 保证 active scope affinity。

因此 PRD AC、TODO 9 清单和任务状态保持未完成/`in_progress`，不切生产、不删除 legacy。
