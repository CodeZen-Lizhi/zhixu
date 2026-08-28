# Events staged GORM 静态验证

- 日期：2026-08-27
- 范围：`internal/events/application`、`internal/events/adapter/postgres` 与本任务文档
- 结论：staged 实现、TODO 9 的功能/事务/失败门禁和局部静态门禁通过；目标数据量下默认 planner 的索引性能证据尚未完成，child 保持 `in_progress`。生产 Composition 切换和 legacy 删除仍保持未完成。

## 审查发现与修复

1. `gormEventContextError` 在 `context.WithCancelCause` / `context.WithDeadlineCause` 使用不包装标准 sentinel 的自定义 cause 时，只保留自定义 cause，导致 `errors.Is(err, context.Canceled|DeadlineExceeded)` 失败。现同时保留 `ctx.Err()` 与自定义 cause，稳定错误码和安全输出不变。
2. 当前 `foundation.TransactionScope` 不携带可比较的 Pool identity；Events 能拒绝 nil、非平台实现和失效 scope，但无法单独拒绝另一 Pool 的 active scope。该限制已在 Design 固定，后续 owner Composition 和 TODO 9 必须从同一 `platformpostgres.Pool` 派生 Store 与 UnitOfWork。

未发现 P0/P1；取消 cause 问题已关闭。跨 Pool identity 是 Foundation/后续 Composition 的已知盲区，不在本 child 扩 scope 修复。

## 静态契约

- Application 新 Port 仅暴露 `foundation.TransactionScope`；legacy `Appender.AppendTx(any)` 保留，`GORMStore` 不实现 legacy Appender。
- 生产仍有 7 个 `eventspostgres.NewStore` 构造点和 17 个 Events `AppendTx` 调用点；没有 `NewGORMStore` / `AppendScoped` 生产调用。
- Events 范围没有 migration、trigger、Domain 或生产 HTTP 文件修改；既有 PostgreSQL Store/HTTP integration fixture 已接入共享 `testdb`，没有 AutoMigrate/Migrator、独立连接池、物理 cleanup、双读、双写或 fallback。
- Raw SQL 保持 Workspace `MAX(seq)`、数据库 `CURRENT_TIMESTAMP`、nullable earliest、`seq > cursor`、升序、`1..100` Limit、JSONB text bind、24 小时 expiry、exact replay 和既有 SQLSTATE 分类。

## 已执行门禁

- `go test -mod=vendor ./internal/events/... -count=1 -timeout 60s`：PASS
- `go test -race -mod=vendor ./internal/events/... -count=1 -timeout 60s`：PASS
- `go vet -mod=vendor ./internal/events/... ./internal/platform/postgres`：PASS
- `go test -mod=vendor -tags=integration -run '^$' ./internal/events/... -count=1 -timeout 60s`：PASS（仅编译）
- `go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker -count=1 -timeout 60s`：PASS（仅编译）
- `go list -mod=vendor ./internal/events/... ./cmd/api ./cmd/worker`：PASS
- `go mod verify`：PASS
- `go mod tidy -diff`：按预期非零，仅显示任务前已有的全仓 `go.sum` 漂移；未应用输出
- `python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-events-migration`：PASS，只有大规格文件注入截断警告
- 受影响 Go 文件 `gofmt -d`、工作树和 index `git diff --check`：PASS

## 未完成门禁

`assertEventReplayPlans` 在很小的 fixture 上执行 `SET LOCAL enable_seqscan=off`。这确认三条 SQL 可以使用 `idx_ops_server_event_workspace_seq`，但不能证明目标数据量、默认 planner 下的性能选择。因此功能、事务、锁、SQLSTATE、触发器和 SSE 实库结果仍保留，PRD 的性能项不勾选，child 必须保持 `in_progress`，不归档、不切生产 Composition。

## TODO 9 实库证据（2026-08-27）

- 两个既有 integration 文件均改为 `integration && testcontainers`，并通过 `testdb.Require` 取得由同一个 `platformpostgres.Pool` 派生的 legacy Store、GORM Store 与 Unit of Work；已删除它们手写的外部 DSN、临时数据库、迁移和清理生命周期。
- `TestGORMStorePostgresEquivalenceAndScopedTransactions` 在真实 PostgreSQL 对照 Workspace 交错全局 seq/gap、watermark、earliest、页大小 `1/100`、空 Workspace、逻辑保留、strict JSON fail-closed、JSONB、UTC 微秒、exact replay/conflict、同事务 owner commit/rollback、nil/foreign/stale scope、response-loss replay、并发 unique claim、FK/CHECK SQLSTATE、cancel cause、连接释放及三条读取 SQL 的可用索引。默认 planner 的目标数据量性能证据尚未完成。
- 同一测试写入 Git Remote 配置投影并由 trigger 生成 `git.remote.updated`，legacy/GORM 均可重放同一受限摘要；`handler_postgres_integration_test.go` 用 GORM Store 驱动真实 SSE cursor、retention、Workspace 隔离和 fresh-watermark 回归。
- 共享夹具揭露两个过期 fixture 假设，均已在 Events 测试内修正：Workspace 状态必须为 `inactive`，成功 Append 的 `OccurredAt` 必须处于数据库当前 24 小时保留窗内。

## 本轮命令

- `go test -mod=vendor -tags='integration testcontainers' ./internal/events/adapter/postgres ./internal/events/http -count=1 -timeout 120s`：PASS。
- `go test -race -mod=vendor -tags='integration testcontainers' ./internal/events/adapter/postgres ./internal/events/http -count=1 -timeout 120s`：PASS。
- `go test -mod=vendor ./internal/events/... -count=1 -timeout 60s`：PASS。
- `go vet -mod=vendor ./internal/events/... ./internal/platform/postgres`：PASS。
- `go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker -count=1 -timeout 60s`、`go list -mod=vendor ./internal/events/... ./cmd/api ./cmd/worker`、`go mod verify`、`git diff --check`：PASS。
- Go/SQL/Trellis review：PASS；未发现本轮范围内 P0/P1。
