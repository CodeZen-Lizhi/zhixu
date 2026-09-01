# Capture Final Composition Handoff

日期：2026-08-31

## 同池构造

Final 必须从同一个 `*platformpostgres.Pool` 构造 Capture 的全部数据库 participant：

- `workspacepostgres.NewGORMRepository(pool)` 提供 `workspaceapplication.ScopedSourceWriter`。
- `agentpostgres.NewGORMRepository(pool)` 提供 `agentapplication.ScopedModelRunFinalizer`。
- `capturepostgres.NewGORMRepository(pool, workspaceRepository)` 替换 Capture Core、Retry、Outbox 与 Processing 的 legacy Repository。
- `capturepostgres.NewGORMProfileRepository(pool, agentRepository)` 替换 Profile generation/read/batch/retry 的 legacy ProfileRepository。

不得从裸 `*pgxpool.Pool` 重新创建第二个平台 Pool，不得并行保留 runtime selector、双写或 fallback。GORM root、`database/sql` view 与 UoW 共享同一物理连接池生命周期。

## API 切换点

`cmd/api/main.go` 当前在 Capture 列表/详情和 Profile 读取组装中调用：

- `capturepostgres.NewRepository(pool)`；
- `capturepostgres.NewProfileRepository(pool, modelRuns)`。

Final 应将相关 helper 输入升级为平台 Pool与稳定 application port，并用上述 GORM constructors 替换。API 外部合同、Handler、Service、cursor 和错误映射保持不变。

## Worker 切换点

`cmd/worker/main.go` 当前在 Capture workflow、Outbox publisher 与 Profile generator 中调用：

- `capturepostgres.NewRepository(db)`；
- `capturepostgres.NewProfileRepository(db, modelRuns)`；
- `newCaptureProfileGenerator` 当前接收裸 `*pgxpool.Pool` 与 legacy `*agentpostgres.Repository`。

Final 应让 Worker 的 Workspace、Agent、Capture Core 与 Profile Repository 全部来自同一平台 Pool，并把 `newCaptureProfileGenerator` 改为接收 scoped Agent port/GORM Profile Repository 所需的稳定依赖。Capture workflow executor 与 Outbox publisher 继续复用同一个 GORM Capture Repository。

## Final 门禁

生产 Composition 切换后必须复跑：

- Worker Capture Outbox -> Workflow/River 的 claim、启动、ack、retry、poison 与恢复链；
- API/Worker Capture/Profile 构造、启动、关闭和连接预算；
- URL materialization 的 Workspace scoped writer 与 Profile completion 的 Agent scoped finalizer 同池原子性；
- 最终生产组合上的 deadlock/serialization、commit response-loss、cancel/deadline 与 pool shutdown；
- Capture 文件发布/read-repair 链及既有 `cmd/worker/capture_composition_integration_test.go`；
- 全仓 pgx allowlist、构造器扫描和禁止 AutoMigrate/第二 Pool/双写门禁。

## Legacy 清理

Final 通过后删除或抽离以下 legacy-only 代码：

- `internal/capture/adapter/postgres/repository.go`
- `internal/capture/adapter/postgres/retry.go`
- `internal/capture/adapter/postgres/runtime.go`
- `internal/capture/adapter/postgres/profile_repository.go`
- `internal/capture/adapter/postgres/profile_retry.go`

`profile_test.go`、`retry_test.go` 及纯 domain/codec/validation helper 若仍被 GORM 使用，应迁入中性文件后再删除 pgx imports。Final 还需清理 `cmd/api`、`cmd/worker` 中只为 legacy constructor 保留的 `*pgxpool.Pool` 参数和具体类型依赖。

## 回滚

生产切换失败时，整体恢复 API/Worker 的 legacy constructors 与裸 pgx participant 链；不回滚 Schema、migration 或已提交业务事实。禁止以双写或运行时 fallback 掩盖切换失败。
