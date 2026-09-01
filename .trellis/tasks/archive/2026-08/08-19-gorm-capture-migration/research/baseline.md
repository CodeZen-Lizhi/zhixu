# Capture Go/API、接线与测试基线

## Owner 与持久化面

- `internal/capture/adapter/postgres.Repository` 实现 Capture create/read/list、retry、outbox lease 和 processing checkpoint。
- `internal/capture/adapter/postgres.ProfileRepository` 实现 Profile ready/source/read、prepare/bind/complete/fail 与 profile retry。
- Application 持久化端口位于 `internal/capture/application/model.go`、`processing.go`、`profile.go` 与 `profile_retry.go`；这些端口当前不暴露 pgx。
- Schema 与跨 owner 事实分别由 Capture、Workspace、Agent、Workflow/Retrieval/Model Settings 拥有；Capture Adapter 只能通过稳定 Port 写其他 owner。

## 跨模块事务事实

- legacy Capture `Repository` 直接持有 `workspacepostgres.TransactionWriter`。Create 和 URL materialization 在 Capture-owned `pgx.Tx` 中写 Source/Artifact/Version，再写 Capture/Outbox/receipt 或 Capture/Attempt。
- Workspace child 已新增 `workspaceapplication.ScopedSourceWriter` 及 GORM 实现，可以加入 caller-owned `foundation.TransactionScope` 且不提交/回滚。
- legacy Profile 依赖 `agentapplication.ModelRunTxFinalizer(any)`。其唯一 PostgreSQL 实现把 `any` 断言为 `pgx.Tx`；Profile complete/fail/replay 在同一事务中读锁并 CAS Agent Model Run。
- Foundation GORM UoW scope 内部是 `*sql.Tx`，不能转换成 `pgx.Tx`。因此 Capture Core 可先迁移，Profile 必须等待 Agent scoped Port，不能拆成两个事务或复制 Agent SQL。

## 生产接线

TODO 9 前以下构造均保持 legacy：

- API Profile reader：`cmd/api/main.go:980`；
- API Capture handler：`cmd/api/main.go:1061`、Profile `:1078`；
- Worker Capture core：`cmd/worker/main.go:1549`；
- Worker Profile generator：`cmd/worker/main.go:3041`；
- Worker helper 仍有 concrete Capture Repository 参数：`cmd/worker/main.go:1969`。

Final 统一把这些 helper 改为完整平台 Pool 和稳定 Port。本 child 不修改 `cmd/**`。

## 现有验证资产

- `internal/capture/adapter/postgres/profile_test.go`：constructor、Profile batch validation、Evidence closure、Model Run binding/replay、terminal CAS。
- `internal/capture/adapter/postgres/retry_test.go`：Capture/Profile retry receipt strict codec、binding、queued/terminal/retryable 分支。
- `internal/platform/migration/capture_repository_integration_test.go`：8 并发 Create、exact replay/conflict、Source/Artifact/Version/Capture/Outbox/receipt 原子事实、Retry、跨 Workflow Run Attempt。
- `internal/platform/migration/capture_profile_repository_integration_test.go`：capability unavailable、Profile retry、READY/STALE/rebuild、Evidence、Model Run terminal、response replay 和 Index activation锁交错。
- `internal/platform/migration/capture_profile_integration_test.go`：Schema、constraint、trigger 和 guarded Down。
- `cmd/worker/capture_composition_integration_test.go`：Capture Outbox 到 Workflow Run/Node/River Job 的生产组合链。

真实 PostgreSQL 用例都依赖 `ZHIXU_TEST_DATABASE_URL`。环境缺失时 build-tag compile 只证明编译，不能作为 GORM、锁、trigger、SQLSTATE、commit/rollback 或 legacy parity 证据。
