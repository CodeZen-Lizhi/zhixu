# Audit staged GORM 静态与 Testcontainers 验证

日期：2026-08-19，实库门禁更新：2026-08-27。

## 阶段结果

- 新增 `GORMStore`，构造器只接收一个 `platformpostgres.Pool`，并从该 Pool 派生 GORM root 与 Store 自有 Unit of Work，禁止调用方分别注入这两项依赖。
- 新增 `ScopedAppender`、`ScopedReader`、`ScopedStore` 与 Recorder 的 scoped 读写入口；公开 scoped Port 只暴露 `foundation.TransactionScope`。
- 保留 legacy `Appender.AppendTx(any)`、`Recorder.RecordTx`、`Store` 与所有生产构造点，未改 `cmd/**` 或 migration；仅原位更新既有 Audit integration fixture。
- `GORMStore.GetScoped` 与 `Recorder.ReadScoped` 只在 caller-owned scope 中读取不可变事件，供 Tools 等后续
  owner 做 commit-response-loss durable closure 验证；不自行提交、回滚或执行访问控制。
- List 与幂等查询显式检查 `Rows.Err` 和 `Rows.Close`，关闭阶段的 driver 错误会和扫描错误一起分类返回，
  不会把连接释放失败当作空结果或成功 replay。
- GORM 追加保持脱敏、transaction-scoped advisory lock、`IS NOT DISTINCT FROM` 精确 replay、`INSERT ... DO NOTHING RETURNING`、重复行 fail-closed 和调用方事务加入语义。
- Get/List 保持 Workspace 精确隔离、`(occurred_at,id)` keyset、固定排序和 `1..200` 上限；JSONB 通过单参数文本 carrier 绑定。
- 未使用 AutoMigrate、Migrator、association、soft delete、Hook、第二连接池、双写或 fallback；未映射或主动写入 `transaction_id`。

## 已执行门禁

以下命令均通过：

```text
go test -mod=vendor ./internal/audit/... -count=1 -timeout 60s
go test -race -mod=vendor ./internal/audit/... -count=1 -timeout 60s
go vet -mod=vendor ./internal/audit/... ./internal/platform/postgres
go test -mod=vendor -tags=integration -run '^$' ./internal/audit/adapter/postgres -count=1 -timeout 60s
go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker ./cmd/workspacectl -count=1 -timeout 60s
go list -mod=vendor ./internal/audit/... ./cmd/api ./cmd/worker ./cmd/workspacectl
go mod verify
python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-audit-migration
gofmt -d <本 child 的 Go 文件>
git diff --check
```

`task.py validate` 仅报告既有大型 spec 注入会截断的 warning，校验本身通过。integration `-run '^$'` 只证明带标签测试可以编译，不是实库证据。

`go mod tidy -diff` 已执行并返回 1；输出是任务开始前已存在的 `go.sum` 大范围规范化差异，并补充未落盘的 sqlite checksum。本 child 未修改依赖，也未应用该输出或改写 `go.sum`。

## 静态检查

- `internal/audit/domain` 未新增数据库实现依赖；新 Application Port 不包含 GORM、`database/sql`、pgx 或 `any`。
- `RecordTx`/`AppendTx(any)` 及跨模块调用点仍保留，未误接 scoped 路径。
- `rg` 未发现 Audit 新路径调用 AutoMigrate/Migrator；`cmd/api`、`cmd/worker`、`cmd/workspacectl` 仍构造 legacy `NewStore`/`NewRepository`。
- 所有 Raw SQL 动态值均通过参数绑定；没有动态 identifier、字符串拼接、N+1 或无界列表。
- `NewGORMStore(*platformpostgres.Pool)` 关闭了独立 root/UoW 构造造成的跨池混配风险；外部传入的 active scope
  仍无法校验 Pool 归属，因为当前 Foundation scope 没有 owner identity。该限制不能登记为 Adapter 已 fail closed。

## Review 结果

- Go review：未发现 P0/P1。确认 legacy 接口兼容、同 Pool 构造、opaque scope 生命周期、UoW callback、取消 cause、Rows 生命周期、并发 WaitGroup/channel 收口和错误分类受控；`-race` 真库回归通过。
- SQL review：最初发现独立 GORM root/UoW 参数可能跨池混配（P2）；已将构造器收敛为单一平台 Pool 并复核关闭。未发现 SQL 注入、锁序、JSONB、keyset 或事务原子性的其他静态缺陷。
- Go/SQL 独立复核另指出 `AppendScoped` 无法识别来自另一 Pool 的 active scope（P2）。这是 Foundation scope
  缺少 owner identity 的共享契约限制，本 child 未跨模块修改；TODO 9 fixture 与 Final Composition 必须保证同池，
  如需运行时拒绝须回 Foundation child 增加不可伪造的 Pool identity。
- Trellis check：PRD/Design/Implement、后台 spec、production wiring、依赖 owner 和 Testcontainers evidence 一致；生产仍只构造 legacy Store。

## TODO 9 真实 PostgreSQL 证据

`store_integration_test.go` 现在以 `integration && testcontainers` 运行 `testdb.Require`。每个 legacy/GORM top-level suite 都取得独立、迁移完成且自动清理的 fixture；两条路径均由该 fixture 返回的同一个 `platformpostgres.Pool` 构造，未复制容器或 pool 生命周期。

以下命令均通过：

```text
go test -mod=vendor -tags='integration testcontainers' -run '^TestAuditStorePostgresAppendOnlyAndIdempotency$' ./internal/audit/adapter/postgres -count=1 -timeout 60s
# PASS; 13.193s

go test -race -mod=vendor -tags='integration testcontainers' -run '^(TestAuditStorePostgresAppendOnlyAndIdempotency|TestAuditGORMStorePostgresEquivalenceAndScopedTransaction)$' ./internal/audit/adapter/postgres -count=1 -timeout 60s
# PASS; 21.287s

go test -mod=vendor ./internal/audit/... -count=1 -timeout 60s
go vet -mod=vendor ./internal/audit/... ./internal/platform/postgres
python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-audit-migration
git diff --check
```

实库矩阵覆盖 legacy/GORM 相互读写与精确重放、全局/Workspace 首写、binding conflict、12 并发单行、预置重复行 fail-closed、JSONB 脱敏与损坏 envelope、global/Workspace List、同微秒 keyset、no-row、目标索引 EXPLAIN、UoW commit/rollback、过期 scope、cancel/deadline cause、`transaction_id=pg_current_xact_id()`、`55000` append-only 触发器和共享 pool 连接释放。

首次运行还发现既有 Audit fixture 的 Workspace seed 使用了已被 `00067` 拒绝的 `status='test'`。已按当前 schema 改为合法的 `inactive`；legacy suite 和 GORM suite 均通过，未改生产 Schema。

## 剩余边界

- `foundation.TransactionScope` 不携带 Pool identity，因此 active foreign scope 仍不能被 Audit Adapter 在运行时区分。fixture 和 Final 均必须保持 single-Pool 构造；如需运行时拒绝，必须回 Foundation child 扩展不可伪造的 owner identity。
- Workspace root rebind history 和 `00089` tool refusal 的 owner-fact 写入由 Workspace、Tools 与 Final 跨模块回归拥有。Audit 已在真实 caller UoW 中验证默认 `transaction_id`；本 child 不伪造或迁移 owner 表、也不切换这些模块的生产组合。
- Production Composition、legacy 删除和 `cmd/**` 只属于 `08-19-gorm-composition-pgx-convergence`。
