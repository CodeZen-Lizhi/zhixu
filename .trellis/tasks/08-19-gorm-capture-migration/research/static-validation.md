# Capture Core 静态验证

验证日期：2026-08-20。当前只完成未接生产 Composition 的 Capture Core staged GORM Adapter；Profile Closure、真实 PostgreSQL TODO 9 和 PRD AC 均未完成，child 保持 `in_progress`。

## 实施范围

- 新增 `gorm_core.go`：从一个 `platformpostgres.Pool` 取得 GORM root/UoW，校验 typed-nil 与 nil context，提供 live scope、Raw Row/Rows 和 GORM 错误映射。
- 新增 `gorm_repository.go`：Replay/Create/Get/List。Create 在一个 Capture-owned UoW 内让 Workspace `ScopedSourceWriter` 加入同一 scope，再写 Capture、Outbox 和 receipt。
- 新增 `gorm_retry.go`：Capture lock、transaction-local exact replay、状态 CAS、Outbox 和 strict receipt。
- 新增 `gorm_runtime.go`：Outbox DB-time CTE/lease CAS，以及全部 Core Processing checkpoint；保持 Capture -> Attempt 锁序和 URL materialization 的 Workspace scoped write。
- 新增 `gorm_model.go`：JSONB `driver.Valuer`/Scanner，写入返回 string 并显式 cast，避免 `[]byte` 被推断为 bytea。
- legacy `Repository`、`ProfileRepository`、Workspace `TransactionWriter`、migration、测试和 `cmd/**` 未改；全仓没有 Capture `NewGORMRepository` 生产调用点。

## Review 结果

- Go Review 未发现 P0/P1 代码行为缺陷。发现一个 P2：GORM classifier 的 SQLSTATE/未知错误曾丢失底层 `errors.Is/As` 链；已改为由安全 `foundation.Error` 保持公开文本稳定，同时内部 Cause 保留原始错误。Create 的 unique-conflict/no-receipt 分支也恢复 legacy cause。
- SQL Review 未发现 P0/P1/P2/P3：参数顺序、JSONB carrier、Workspace predicate、keyset、Capture -> Attempt 锁序、CAS、DB time、`SKIP LOCKED` 和目标索引均与 legacy/Schema 静态一致。
- Trellis Check 确认 Core 实现符合 PRD/Design，生产仍为 legacy，Profile 前置和 TODO 9 未被越过；无 AutoMigrate/Migrator、association/preload、hook、`gorm.Model`、soft delete 或自动时间。
- Go Review 指出的真实 PostgreSQL GORM 回归缺口属于 PRD R8/TODO 9 硬门禁；当前环境缺少数据库，不能用编译或 mock 宣称通过。

## 已通过命令

- `go test -mod=vendor ./internal/capture/... -count=1 -timeout 60s`
- `go test -race -mod=vendor ./internal/capture/... -count=1 -timeout 60s`
- 修复后额外执行 `go test -race -mod=vendor ./internal/capture/adapter/postgres -count=1 -timeout 60s`
- `go vet -mod=vendor ./internal/capture/... ./internal/workspace/application ./internal/platform/postgres`
- `go test -mod=vendor -tags=integration -run '^$' ./internal/platform/migration ./cmd/worker -count=1 -timeout 60s`
- `go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker ./internal/workspace/... -count=1 -timeout 60s`
- `go list -mod=vendor ./internal/capture/... ./internal/workspace/... ./internal/platform/postgres ./cmd/api ./cmd/worker`
- `go mod verify`
- `python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-capture-migration`
- 受影响文件 `gofmt -d` 与全工作树 `git diff --check`

`go mod tidy -diff` 已审查但返回 1，只显示本任务前已有的大量 `go.sum` 规范化删除及 sqlite checksum 增补；未应用，也未修改 `go.sum`。

## 静态接线与安全扫描

- `rg 'NewGORMRepository\\(' cmd internal/capture --glob '*.go'` 只命中新构造器定义；API/Worker 继续调用 legacy `NewRepository` / `NewProfileRepository`。
- 新文件无 AutoMigrate、Migrator、Preload、Association、`gorm.Model` 或 `context.Background`。
- 唯一动态 SQL 是 `FailAttempt` 从封闭的 `AttemptStage` switch 选择固定状态列片段；所有外部值仍参数化。
- 新公开 Port/构造签名不暴露 GORM、database/sql、pgx 或 `any`；pgx 仅在 Adapter 内用于 legacy no-row/SQLSTATE 兼容识别。

## 未完成门禁

`ZHIXU_TEST_DATABASE_URL` 为 absent。尚未真实验证 GORM/pgx-stdlib 的 UUID/JSONB/cast、Workspace scoped commit/rollback、并发 Create/Retry exact replay、Outbox 双 worker claim/reclaim、Attempt/URL 锁与 CAS、trigger/deferred FK/SQLSTATE、cancel/cause/deadline、commit unknown、连接释放和 EXPLAIN。Agent scoped Model Run Port 与 GORM Profile Adapter也未实现。因此 PRD AC、TODO 9 和 task completion保持未勾选。
