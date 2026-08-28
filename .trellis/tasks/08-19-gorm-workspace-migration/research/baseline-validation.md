# Workspace 规划基线验证

验证日期：2026-08-20。当前任务仍为 `planning`，未启动实现、未修改生产 Go 代码。

## 已通过

- `go test -mod=vendor ./internal/workspace/... ./internal/platform/rootgrant -count=1 -timeout 60s`
- `go test -race -mod=vendor ./internal/workspace/... ./internal/platform/rootgrant -count=1 -timeout 60s`
- `go vet -mod=vendor ./internal/workspace/... ./internal/platform/rootgrant ./internal/platform/postgres ./internal/capture/adapter/postgres`
- `go test -mod=vendor -tags=integration -run '^$' ./internal/workspace/adapter/postgres ./internal/platform/migration -count=1 -timeout 60s`
- `go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker ./cmd/workspacectl ./cmd/workspaceprobe ./internal/capture/... -count=1 -timeout 60s`
- `go list -mod=vendor ./internal/workspace/... ./internal/platform/rootgrant ./internal/capture/... ./cmd/api ./cmd/worker ./cmd/workspacectl ./cmd/workspaceprobe`
- `go mod verify`
- `python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-workspace-migration`
- `git diff --check`

静态接线扫描确认 API/Worker 仍使用 `NewProcessComposition`，workspacectl/workspaceprobe/Worker fallback 仍使用 legacy `NewRepository`，Capture 仍持有 `workspacepostgres.TransactionWriter`。Workspace Application/Domain 当前没有 GORM、`database/sql` 或 pgx import；已有 `any` 仅是原有 nil-interface helper。

## 未应用的 tidy diff

`go mod tidy -diff` 返回 1，内容为任务开始前已存在的大量 `go.sum` 规范化删除及 sqlite checksum 增补。该输出未应用，`go.sum` 没有被本规划修改。

## 真实数据库盲区

`ZHIXU_TEST_DATABASE_URL` 未配置。integration `-run '^$'` 只证明测试可编译，不能证明 GORM Raw/array、advisory/row lock、DB time、trigger SQLSTATE、commit/rollback、并发、EXPLAIN 或 legacy/GORM 等价；这些保持为 TODO 9 未完成门禁。
