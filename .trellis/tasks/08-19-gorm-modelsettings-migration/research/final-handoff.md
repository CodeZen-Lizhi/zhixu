# Model Settings GORM staged handoff

## 当前状态

staged GORM 实现仍刻意未接入生产 Composition。child task 保持 `in_progress` 且不归档：TODO 9 的真实 PostgreSQL 证据是明确的验收前置条件。

## 已核验范围

- `GORMRepository` 与 `BootstrapGORM` 仍是 sibling-only 新增；legacy `Repository`、`Bootstrap`、`cmd/**`、迁移和既有 integration fixture 均未修改。
- Model Settings、Audit 与 managed Local Runtime 均由同一个 `*platformpostgres.Pool` 构造；事务内 Audit、Local Runtime preparation 与 Workflow admission 均使用 `foundation.TransactionScope`。
- staged adapter 保留 Raw 参数化 SQL、数据库时间、state/runtime/participant 锁序、CAS/no-row 错误映射、脱敏和 repeatable-read Snapshot 语义。
- 没有生产调用点引用 `NewGORMRepository` 或 `BootstrapGORM`。

## 已完成验证

下列命令均成功完成，除特别说明外均使用 vendor 模式：

```text
go test -mod=vendor ./internal/modelsettings/... -count=1 -timeout 60s
go test -race -mod=vendor ./internal/modelsettings/... -count=1 -timeout 60s
go vet -mod=vendor ./internal/modelsettings/... ./internal/audit/... ./internal/localmodelruntime/... ./internal/platform/postgres
go test -mod=vendor -tags=integration -run '^$' ./internal/modelsettings/adapter/postgres ./internal/platform/migration -count=1 -timeout 60s
go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker ./cmd/modelctl ./cmd/local-model-runtime -count=1 -timeout 60s
go list -mod=vendor ./internal/modelsettings/... ./internal/audit/... ./internal/localmodelruntime/... ./internal/workflow/adapter/river ./cmd/api ./cmd/worker ./cmd/modelctl
go mod verify
go mod tidy -diff
gofmt -d <task Go files>
git diff --check
python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-modelsettings-migration
```

`go mod tidy -diff` 无输出，且未修改 `go.mod` 或 `go.sum`。Go、SQL 和 Trellis review 未发现剩余 P0-P2 实现缺陷。`task.py validate` 通过，仅有已知的大 manifest context-injection 截断警告。

Phase 3.3 未修改共享 code-spec：现有 Model Settings 与数据库规范已经明确同池 GORM/UoW、scope、锁序、数据库时间和 TODO 9 真实 PostgreSQL 门禁；本轮没有产生可在未完成 TODO 9 前安全宣称的新稳定契约。

## TODO 9 阻断

`ZHIXU_TEST_DATABASE_URL` 不可用。按 PRD/design，此时只允许 integration compile，不得勾选 PRD AC、标记任务完成或归档。

TODO 9 只能原位参数化既有 integration fixture，并使用 `internal/platform/testdb` 的共享工厂。仍须以真实 PostgreSQL 验证：

- GORM cast、nullable/bytea/time scan、trigger SQLSTATE 与 no-row 行为。
- Revision + Audit、Activation + Local Runtime、Fence + River 的原子 commit/rollback。
- 并发锁序、数据库时间 lease、CAS、cancel/commit ambiguity、连接释放、Snapshot 一致性和相关 `EXPLAIN` 计划。

## Handoff 约束

不得切换 API、Worker、modelctl 或 River 的生产 Composition；不得删除 legacy pgx 代码；不得新增第二连接池或 fixture。本会话未创建提交。
