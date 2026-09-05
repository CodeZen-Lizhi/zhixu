# Model Settings staged GORM 静态验证

## 结论

已新增 sibling `GORMRepository`、scoped Audit/Local Runtime/Workflow fence 和独立 `BootstrapGORM`。legacy `Repository`、`Bootstrap`、三处 `cmd/**` 生产接线与迁移均未修改；没有新增测试文件，只在既有 integration fixture 中原位扩充 GORM 场景。当前实现仍是未接生产的 staged 路径。

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

静态扫描确认：Model Settings Domain/Application 不导入 GORM、database/sql 或 pgx；staged 文件不含 AutoMigrate/Migrator/Save/Preload/Association、自建 DSN/第二 pool；`BootstrapGORM` 没有生产调用点。现有 integration 文件已增加一个由 TODO 9 `testdb` 工厂驱动的 `NewGORMRepository` 主路径/回滚/fence 场景；生产 wiring 仍保持未切换，低频专项继续按风险触发。

## TODO 9 真实 PostgreSQL 阻断（按精简政策收敛）

本轮主路径及直接改动触发的硬风险已通过真实 Testcontainers；以下完整专项不由单条场景替代，但不再作为无差别 child 门禁：

- GORM placeholder/cast、bytea/nullable/time scan、trigger SQLSTATE 和 no-row 的真实执行。
- Activation + Local Runtime 同事务提交/回滚、Fence + River 同事务提交/回滚，以及 GORM 路径锁竞争/释放代表场景均已通过。
- DB-time lease、CAS、死锁/serialization 仅在对应风险触发时执行。
- Snapshot repeatable-read/read-only 一致性、cancel/deadline/commit ambiguity、连接释放与 EXPLAIN 属于专项风险触发项，不作为本轮无差别门禁。
- Foundation scope 没有 Pool identity；只能由同池 Composition 和 TODO 9 fixture 保证 active scope affinity。

因此按精简政策 AC1-AC5 及工厂可用性均可视为满足；任务代码证据可完成验收。生产 Composition 切换、不删除 legacy 和全仓 allowlist 仍由 Final 负责。
