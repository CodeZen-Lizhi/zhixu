# Model Settings GORM staged handoff

## 当前状态（历史基线，2026-09-01 更新）

staged GORM 实现仍刻意未接入生产 Composition。按精简政策，真实 PostgreSQL 主路径与直接改动触发的硬风险证据已满足 child 验收；工作区尚未提交，因此 task 元数据暂保持 `in_progress`，不执行自动归档。

## 已核验范围

- `GORMRepository` 与 `BootstrapGORM` 仍是 sibling-only 新增；legacy `Repository`、`Bootstrap`、`cmd/**` 和迁移均未修改。没有新增测试文件，只在既有 integration fixture 中原位扩充 GORM 场景。
- Model Settings、Audit 与 managed Local Runtime 均由同一个 `*platformpostgres.Pool` 构造；事务内 Audit、Local Runtime preparation 与 Workflow admission 均使用 `foundation.TransactionScope`。
- staged adapter 保留 Raw 参数化 SQL、数据库时间、state/runtime/participant 锁序、CAS/no-row 错误映射、脱敏和 repeatable-read Snapshot 语义。
- 没有生产调用点引用 `NewGORMRepository` 或 `BootstrapGORM`。

## 已完成验证（历史基线 + 本轮增量）

下列列表保留历史成功记录；其中 race、compile-only、`go list` 与模块整理不再是现行默认门禁。本轮以清单中的目标 Testcontainers 场景和父任务精简政策为准。

```text
go test -mod=vendor ./internal/modelsettings/... -count=1 -timeout 60s
go test -race -mod=vendor ./internal/modelsettings/... -count=1 -timeout 60s
go vet -mod=vendor ./internal/modelsettings/... ./internal/audit/... ./internal/localmodelruntime/... ./internal/platform/postgres
go test -mod=vendor -tags=integration -run '^$' ./internal/modelsettings/adapter/postgres ./internal/platform/migration -count=1 -timeout 60s
go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker ./cmd/modelctl ./cmd/local-model-runtime -count=1 -timeout 60s
go list -mod=vendor ./internal/modelsettings/... ./internal/audit/... ./internal/localmodelruntime/... ./internal/workflow/adapter/river ./cmd/api ./cmd/worker ./cmd/modelctl
go mod verify
go mod tidy -diff
go test -v -mod=vendor -tags=integration -run '^TestGORMRepositorySaveDesiredRollbackAndScopedEnqueueFence$' ./internal/modelsettings/adapter/postgres -count=1 -timeout 120s
gofmt -d <task Go files>
git diff --check
python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-modelsettings-migration
```

`go mod tidy -diff` 无输出，且未修改 `go.mod` 或 `go.sum`。Go、SQL 和 Trellis review 未发现剩余 P0-P2 实现缺陷。`task.py validate` 通过，仅有已知的大 manifest context-injection 截断警告。真实 Testcontainers 场景使用 `FailWhenUnavailable`，并同时覆盖 Revision/Audit、managed Local Runtime、scoped River 回滚与提交后读回、stale scope 和 GORM 锁竞争/释放。

Phase 3.3 已在 `model-settings-runtime.md` 补齐 staged GORM 的同池 Composition、opaque scope、三条跨 owner 原子事务和精简 Testcontainers 门禁；没有改变生产切线、legacy 删除或 Final 的稳定契约。

## TODO 9 完整专项（历史说明，当前不阻断 child）

以下“数据库不可用/只允许 integration compile”的判断是旧基线，当时用于说明尚未接入真实 fixture；2026-09-01 已由 `testdb` Testcontainers 实测主路径。当前以父任务精简政策的硬风险触发项为准，不将旧 compile-only 要求当作额外门禁。

TODO 9 原始清单要求原位参数化既有 integration fixture，并使用 `internal/platform/testdb` 的共享工厂。以下完整专项保留为后续风险触发/Final 组合参考：

- GORM cast、nullable/bytea/time scan、trigger SQLSTATE 与 no-row 行为。
- Revision + Audit、Activation + Local Runtime、Fence + River 的原子 commit/rollback。
- 并发锁序、数据库时间 lease、CAS、cancel/commit ambiguity、连接释放、Snapshot 一致性和相关 `EXPLAIN` 计划。

## Handoff 约束

不得切换 API、Worker、modelctl 或 River 的生产 Composition；不得删除 legacy pgx 代码；不得新增第二连接池或 fixture。本会话未创建提交。
