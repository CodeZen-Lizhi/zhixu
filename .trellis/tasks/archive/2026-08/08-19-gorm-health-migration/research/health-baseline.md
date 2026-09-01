# Health GORM 迁移基线

## 已核对的 owner 与调用边界

- `internal/health/adapter/postgres/scan_repository.go`：Scan start/replay、Repeatable Read、scope advisory lock、Workflow runtime、coverage CAS、Finish/Event、commit response-loss recovery。
- `internal/health/adapter/postgres/schedule_repository.go`：command receipt、Workspace lock、DB time、`FOR UPDATE SKIP LOCKED`、pending due lease、ack/release CAS。
- `internal/health/adapter/postgres/issue_repository.go`：detector page 原子 upsert、seen identity、Issue/Observation/Evidence、Decision idempotency/CAS、missing-set 收敛。
- `internal/health/adapter/postgres/read_repository.go` 与 `detector_reader.go`：Workspace-scoped keyset/history/read-model queries。
- `internal/health/adapter/postgres/affected_change_dispatcher.go`：typed outbox 单行 claim、source binding、active-scan defer、poison、commit recovery。
- `internal/health/adapter/collection/membership.go`：Collection durable binding、分页和 revision 复核；GORM 路径应使用 Collection `ScopedDurableScanBindingVerifier`。

## 已核对的共享契约

- `internal/platform/postgres.Pool` 暴露同一个 GORM root 与 `foundation.UnitOfWork`；`GORMTransaction`/`SQLTransaction` 只接受当前 live `TransactionScope`。
- Workflow staged Port 为 `ScopedRuntimeStarter`、`ScopedCancellationSafetyGuard`；Events staged Port 为 `ScopedAppender`；Collection staged Port 为 `ScopedDurableScanBindingVerifier`。
- TODO 10 父设计要求 TODO 9 前不得切换 Composition、删除 legacy 或宣称模块验收完成；本 child 只新增 staged Adapter。

## 实现取舍

Health 旧 SQL 已有较完整行为/集成基线。新增 GORM adapter 在包内用受控 `database/sql` 行/事务桥接复用这些 SQL，避免复制锁、CAS、receipt 和 recovery 规则；桥接只允许在 Adapter 内出现，新的 scoped Workflow/Event/Collection 依赖仍通过 opaque `TransactionScope` 加入同一 UoW。真实 PostgreSQL 锁、River、并发和 response-loss 门禁需 TODO 9 DSN 后执行。
