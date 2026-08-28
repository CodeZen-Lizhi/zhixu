# Workspace staged GORM 静态验证

日期：2026-08-20

## 交付状态

- 已新增未接生产的 `GORMRepository`，覆盖 Workspace/Source、Registry、Control、Runtime、Git capture、rootgrant 和 Runtime Composition。
- 已新增 `application.ScopedSourceWriter`；公开 Port 不包含 GORM、`database/sql`、pgx 或 `any`。
- legacy `Repository`、`TransactionWriter(pgx.Tx)`、`PostgresAuthoritativeStore`、`ProcessComposition`、Capture 调用和全部 `cmd/**` 接线保持不变。
- `task.json` 保持 `in_progress`，PRD AC 与 TODO 9 全部未勾选；本记录只证明 staged 代码的静态、编译与现有测试门禁。

## Review 与修复

Go Review、SQL Review 和 Trellis Check 均已执行。独立审查确认 Control/Runtime、Registry/Rebind 和 Git checkpoint 的锁序、CAS、数据库时间、参数绑定与 legacy 路径静态一致，并修复以下问题：

1. Source Version 列表原先把数据库 UUID 直接强转为 `foundation.ID`；现在逐项 `ParseID`，并校验结果 Workspace 与请求一致，损坏行整页 fail closed。
2. `AdvanceSwitch` 原先可能在 nil context 下先返回 phase 错误；现在公开方法入口先执行 Repository ready/context 门禁。
3. Registry 候选扫描的错误返回路径原先没有显式关闭 `Rows`；现在以 defer 覆盖所有退出路径，并保留正常路径的及时关闭。
4. GORM rootgrant 原先在 `WithCancelCause` / `WithDeadlineCause` 下丢失自定义 cause；现在保留标准 sentinel，并以 `errors.Join` 同时保留不同的 caller cause。

Review 未发现剩余 P0/P1。静态性能审查未发现 OFFSET、association/preload、N+1 或无界新查询；真实执行计划仍属于 TODO 9。

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

`go mod tidy -diff` 已执行但返回 1；输出是任务开始前已存在的 `go.sum` 大范围规范化差异，并建议补充当前测试依赖的 sqlite checksum。未应用该 diff，未覆盖或回退现有 `go.sum` 改动。

## 静态接线与 SQL 证据

- `cmd/**` 中没有 `NewGORMProcessComposition`、Workspace `NewGORMRepository` 或 `NewGORMAuthoritativeStore` 调用。
- Capture 仍在 `internal/capture/adapter/postgres` 使用 legacy `workspacepostgres.TransactionWriter` 与 caller-owned `pgx.Tx`。
- staged 文件不存在 AutoMigrate/Migrator、Hook、`gorm.Model`、association/preload、`gorm.Open` 或新的 `pgxpool.New`。
- 所有 caller 值均参数化；动态 predicate/lock suffix 只来自私有固定常量。Git tombstone 的 `text[]` 使用 `pq.Array` 单值绑定。
- advisory lock、`FOR UPDATE/FOR SHARE`、CAS、checkpoint 和状态机写入均在 Foundation UoW 解出的 scoped GORM transaction 上执行；Control snapshot 使用 `RepeatableRead + ReadOnly`。
- Application/Domain 未新增 ORM、driver 或 `database/sql` 依赖；已有 `any` 仅是原有的 typed-nil helper，不属于本次 Port。

## TODO 9 阻断与剩余风险

当前环境没有 `ZHIXU_TEST_DATABASE_URL`，因此 integration 命令仅完成编译，不能作为真实 PostgreSQL 证据。以下项目仍未验证并继续阻断 PRD AC、child 完成和归档：

- GORM `?` 占位、UUID、`pq.Array(text[])`、nullable carrier、真实 SQLSTATE/constraint/trigger 行为。
- advisory/row lock 竞争、反向 fingerprint 并发、CAS/lease、repeatable-read snapshot、rollback/commit response-loss。
- Source/Artifact/Version replay、legacy artifact 补齐、root capability、corrupt row no-partial、连接释放与目标规模 EXPLAIN。
- Git checkpoint 串行、tombstone/reappearance、Control/Runtime 状态机和 scoped Audit 原子提交/回滚。
- caller-owned UoW 中 scoped writer 的真实 commit/rollback、不自行提交/回滚及 invalid/stale scope。

Foundation `TransactionScope` 目前没有 Pool owner identity，因此 active foreign-Pool scope 无法由 Workspace adapter 运行时拒绝。本 child 只通过同一 Pool 的 constructor/Composition 和后续 TODO 9 fixture 保证正确接线；若需要强制 affinity，必须先由 Foundation 增加可校验 owner identity。
