# Workspace Go/API、接线与测试基线

## Owner 与能力

- `internal/workspace/adapter/postgres` 的 legacy Repository 同时实现 Domain Repository、Active Workspace、Source Material、Source Version List、Application Registry/Control 和 Git Capture。
- Runtime register/heartbeat/phase 是 `application.ControlStore` 的一部分；状态机由 ControlService 调用。
- `internal/platform/rootgrant` 已有 library-neutral `AuthoritativeStore`，但 PostgreSQL实现的 `QueryRower`/`pgx.Row` 仍是 driver 边界。
- `internal/workspace/runtimegrant.ProcessComposition` 当前公开具体 `*workspacepostgres.Repository`，并由 API/Worker 直接构造。

## 跨模块事务事实

- legacy `TransactionWriter` 接收 `pgx.Tx`，封装 Source/Artifact/Source Version 写入。
- Capture create 与 URL materialization 都在 Capture-owned `pgx.Tx` 中调用该 writer，再写 Capture、outbox、attempt/receipt；它们必须共同提交或回滚。
- Foundation GORM UoW scope 内部是 `*sql.Tx`，无法加入现有 pgx transaction。因此 Workspace child 只能新增 opaque scoped writer并保留 legacy writer，Capture child 后续才能切换事务 owner。
- Foundation scope 当前无 Pool identity；nil/foreign type/stale 可拒绝，active foreign-Pool scope 不能由 adapter识别。

## 生产接线

- API 和 Worker 使用 `runtimegrant.NewProcessComposition(..., database.DB(), ...)`。
- Worker fallback、workspacectl 和 workspaceprobe 直接构造 legacy `workspacepostgres.NewRepository`。
- 多个 cmd helper 参数仍是 `*workspacepostgres.Repository`；Final 切换前不能机械替换 Composition。
- workspacectl 的 rebind 同时构造 legacy Audit Store 和 `WithAuditAppender`；staged GORM rebind 必须改用 Audit `ScopedAppender`，但本 child 不改 cmd。

## 现有验证资产

- `repository_guard_test.go`：tombstone list、managed root list、active Workspace 唯一性与 grant resolver fail-closed。
- `repository_integration_test.go`：Workspace/Source/Artifact/Version lifecycle、material scope、约束与 immutable 数据。
- `list_integration_test.go`：筛选、keyset、LATERAL 状态和 EXPLAIN/index。
- `git_capture_integration_test.go`：apply/complete、exact replay、out-of-order、tombstone/reappearance。
- `internal/platform/migration/workspace_root_grant_repository_integration_test.go`：Begin/TakeOver、switch/rollback。
- `internal/platform/migration/workspace_root_rebinding_integration_test.go`：rebind、Audit、idempotency、fail-closed。
- rootgrant 与 runtimegrant unit tests：env mode、authority mismatch/no fallback、capability revalidation、lease/quiescence。

所有真实 PostgreSQL 测试由 `ZHIXU_TEST_DATABASE_URL` 控制；当前无该变量时 integration compile-only 不能作为 TODO 9 证据。TODO 9 应原位参数化现有测试，为 legacy/GORM 子用例使用独立 database。
