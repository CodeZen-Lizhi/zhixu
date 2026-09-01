# Capture 规划基线验证

验证日期：2026-08-20。当前 child 仍为 `planning`，本阶段只修改规划工件和父依赖说明，未修改 Capture/Agent生产 Go代码。

## 已通过

- `go test -mod=vendor ./internal/capture/... -count=1 -timeout 60s`
- `go test -race -mod=vendor ./internal/capture/... -count=1 -timeout 60s`
- `go vet -mod=vendor ./internal/capture/... ./internal/workspace/application ./internal/platform/postgres`
- `go test -mod=vendor -tags=integration -run '^$' ./internal/platform/migration ./cmd/worker -count=1 -timeout 60s`
- `go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker ./internal/workspace/... -count=1 -timeout 60s`
- `go list -mod=vendor ./internal/capture/... ./internal/workspace/... ./internal/platform/postgres ./cmd/api ./cmd/worker`
- `go mod verify`
- `python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-capture-migration`
- `python3 .trellis/scripts/task.py validate .trellis/tasks/08-18-gorm-data-access-migration`
- `python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-agent-migration`
- `git diff --check`
- 静态扫描 Capture/API/Worker 范围无 AutoMigrate、Migrator、`gorm.Model` 或 staged Capture production构造。

三项 Trellis validation 均通过。Capture/父任务仅提示超大数据库规范在上下文注入时会截断；这是现有注入上限提醒，不影响 JSONL 引用有效性。

## 未应用的 tidy diff

`go mod tidy -diff` 返回 1，内容仍为任务前已有的大量 `go.sum` 规范化删除及 sqlite checksum增补。该输出未应用，本规划未修改 `go.sum`。

## 真实数据库盲区

`ZHIXU_TEST_DATABASE_URL` 当前为 absent。integration `-run '^$'` 只证明测试编译，不能证明 GORM Raw/UUID/JSONB/array、跨 Workspace/Agent scope、advisory/row lock、SKIP LOCKED、DB time、trigger/deferred FK、SQLSTATE、commit/rollback、并发、连接释放或 EXPLAIN；这些保持 TODO 9未完成。
