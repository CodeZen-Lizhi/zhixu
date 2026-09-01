# Workspace staged GORM 静态验证

日期：2026-08-31

## 交付状态

- 已新增未接生产的 `GORMRepository`，覆盖 Workspace/Source、Registry、Control、Runtime、Git capture、rootgrant 和 Runtime Composition。
- 已新增 `application.ScopedSourceWriter`；公开 Port 不包含 GORM、`database/sql`、pgx 或 `any`。
- legacy `Repository`、`TransactionWriter(pgx.Tx)`、`PostgresAuthoritativeStore`、`ProcessComposition` 和全部 `cmd/**` 生产接线保持不变；共享工作区中虽已有下游 Capture staged 文件，生产 Capture Repository 仍走 legacy writer。
- TODO 9 的真实 PostgreSQL 门禁已通过，PRD AC 已闭合；生产 Composition 切换和 legacy 删除仍属于 Final。

## Review 与修复

Go Review、SQL Review 和 Trellis Check 均已执行。独立审查确认 Control/Runtime、Registry/Rebind 和 Git checkpoint 的锁序、CAS、数据库时间、参数绑定与 legacy 路径静态一致，并修复以下问题：

1. Source Version 列表原先把数据库 UUID 直接强转为 `foundation.ID`；现在逐项 `ParseID`，并校验结果 Workspace 与请求一致，损坏行整页 fail closed。
2. `AdvanceSwitch` 原先可能在 nil context 下先返回 phase 错误；现在公开方法入口先执行 Repository ready/context 门禁。
3. Registry 候选扫描的错误返回路径原先没有显式关闭 `Rows`；现在以 defer 覆盖所有退出路径，并保留正常路径的及时关闭。
4. GORM rootgrant 原先在 `WithCancelCause` / `WithDeadlineCause` 下丢失自定义 cause；现在保留标准 sentinel，并以 `errors.Join` 同时保留不同的 caller cause。
5. legacy Workspace 分类器原先会覆盖已有 `foundation.Error`；现在保留稳定错误码与 cause，并由现有 guard 测试覆盖 `sql.ErrTxDone` 映射。
6. Runtime freshness 原先会接受未来 heartbeat；legacy/GORM 现在统一要求 heartbeat 落在 `[now-freshWithin, now]`，future heartbeat fail closed。
7. GORM Control/Runtime 公开方法原先可能在 callback 或 commit 失败时返回未提交的部分结果；现在错误时统一返回零值，空 snapshot 的列表也与 legacy 一致为非 nil 空切片。
8. Rebind 新增 deferred commit guard 场景：Audit appender 返回成功却未写入时，commit 触发 SQLSTATE 55000，Workspace、control、history 和 Audit 全部回滚且连接释放。

Review 未发现剩余 P0/P1。静态性能审查未发现 OFFSET、association/preload、N+1 或无界新查询；目标规模 EXPLAIN 已在真实 PostgreSQL 上通过。

## 已通过门禁

以下命令均在 60 秒内通过：

```text
go test -mod=vendor ./internal/workspace/... ./internal/platform/rootgrant -count=1 -timeout 60s
go test -race -mod=vendor ./internal/workspace/... ./internal/platform/rootgrant -count=1 -timeout 60s
go vet -mod=vendor ./internal/workspace/... ./internal/platform/rootgrant ./internal/platform/postgres ./internal/capture/adapter/postgres
go test -mod=vendor -tags=integration -run '^$' ./internal/workspace/adapter/postgres ./internal/platform/migration -count=1 -timeout 60s
go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker ./cmd/workspacectl ./cmd/workspaceprobe ./internal/capture/... -count=1 -timeout 60s
go list -mod=vendor ./internal/workspace/... ./internal/platform/rootgrant ./internal/capture/... ./cmd/api ./cmd/worker ./cmd/workspacectl ./cmd/workspaceprobe
go mod verify
python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-workspace-migration
gofmt -d <affected Go files>
git diff --check
```

`go mod tidy -diff` 已通过且无输出；未改写共享工作区中的 module/vendor 文件。

## 静态接线与 SQL 证据

- `cmd/**` 中没有 `NewGORMProcessComposition`、Workspace `NewGORMRepository` 或 `NewGORMAuthoritativeStore` 调用。
- 生产 Capture `internal/capture/adapter/postgres/repository.go` 仍使用 legacy `workspacepostgres.TransactionWriter` 与 caller-owned `pgx.Tx`；共享工作区中的下游 staged 文件不改变当前生产接线。
- staged 文件不存在 AutoMigrate/Migrator、Hook、`gorm.Model`、association/preload、`gorm.Open` 或新的 `pgxpool.New`。
- 所有 caller 值均参数化；动态 predicate/lock suffix 只来自私有固定常量。Git tombstone 的 `text[]` 使用 `pq.Array` 单值绑定。
- advisory lock、`FOR UPDATE/FOR SHARE`、CAS、checkpoint 和状态机写入均在 Foundation UoW 解出的 scoped GORM transaction 上执行；Control snapshot 使用 `RepeatableRead + ReadOnly`。
- Application/Domain 未新增 ORM、driver 或 `database/sql` 依赖；已有 `any` 仅是原有的 typed-nil helper，不属于本次 Port。

## TODO 9 真实 PostgreSQL 证据

测试通过 `testdb.Require` 为 legacy/GORM 子用例分别创建已迁移的 disposable PostgreSQL，并从同一 `platformpostgres.Pool` 获取 pgx、GORM 与 UoW。以下现有 integration gate 均已真实执行并通过：

- Workspace/Source：lifecycle、exact replay、metadata conflict、batch rollback、legacy/cross-scope material fail-closed、FK/CHECK、caller-owned scoped writer commit/rollback/invalid/stale scope、cancel/cause/deadline 和连接释放。
- List/rootgrant：keyset/LATERAL filter/page、corrupt row no-partial、目标规模 EXPLAIN、managed/direct authoritative view 与 capability revalidation。
- Registry/Rebind：Resolve/availability/remove、反向 fingerprint 并发、response-loss replay、Audit callback rollback、deferred commit trigger rollback、history/Audit closure 与 Git lineage guard。
- Control/Runtime：snapshot、Begin/TakeOver/Advance/Revoke/Commit/Restore/Finish、lease/CAS/DB time、full switch/rollback、owner/version/binding/grant fence、future heartbeat 和 quiescence。
- Git capture：apply/complete、并发 checkpoint 串行、唯一 winner、out-of-order、exact replay、tombstone/reappearance、`pq.Array` 单值绑定与 row/checkpoint 一致性。

单次运行全部容器型 integration gate 预计超过 120 秒，因此按 owner test 逐项执行；integration `-run '^$'` 仅作为额外编译门禁，不冒充真实数据库证据。`sql.ErrTxDone` 的稳定错误映射由现有 guard 测试覆盖。

## 剩余边界

Foundation `TransactionScope` 仍没有 Pool owner identity，因此 active foreign-Pool scope 无法由 Workspace adapter 运行时拒绝。本 child 通过同一 Pool constructor/Composition 和真实 fixture 保证接线；若 Final 要求运行时强制 affinity，需先由 Foundation 增加 owner identity。生产 Composition 仍是 legacy，这属于 Final 的显式切换与回滚范围。
