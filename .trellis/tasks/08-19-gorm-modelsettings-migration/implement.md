# Model Settings GORM 迁移实施清单

## 1. 规划与基线

- [x] 读取 PRD、父任务、Model Settings runtime/database/error/logging/quality 与 cross-layer/code-reuse 指南。
- [x] 盘点 Repository 全部 public surface、Bootstrap/生产构造、Audit/Local Runtime/Workflow 事务依赖和 integration fixture。
- [x] 核对 `00064`、`00078`、`00079`、`00080` 及 `00065/00066` 的表、trigger、锁序、DB time 与权限 guard。
- [x] 冻结 staged sibling 策略：不改 legacy Repository/Bootstrap/cmd，不用第二事务或 no-op scoped dependency。
- [x] 完成独立规划审查并关闭 P0-P2 后运行 `task.py start`；start 前不得改 Go 源码。

## 2. Scoped Ports 与 Adapter

- [x] 在 Application 新增 `ScopedSettingsAuditAppender`，不修改 legacy `SettingsAuditAppender(any)`。
- [x] 新增 `GORMSettingsAuditAppender`，复用现有 redacted event 规则并调用 Audit `AppendScoped`。
- [x] GORM Repository 接收 `localmodelruntime.ScopedTxLifecycle`，caller scope 内使用 `WithScope`，绝不自建/提交/回滚 Local Runtime 事务。
- [x] GORM Repository 实现 Workflow `ScopedEnqueueFence`，仅在 caller scope 内读取 `FOR SHARE`，无 fallback/no-op。

## 3. Staged GORM Repository

- [x] 新增 `GORMRepository`/`NewGORMRepository(*platformpostgres.Pool, ...GORMOption)`，复用同一 GORM root/UoW，不创建第二池。
- [x] 新增必需的 SecretSealer/scoped Audit 和可选 scoped Local Runtime GORM options；拒绝 nil/typed-nil、重复配置，并允许 TODO 9 注入故障实现。
- [x] 迁移 Revision：SaveDesired/ResolveDraft/LoadRevision，保持 secret AAD、immutable revision、state CAS 与 scoped Audit 原子性。
- [x] 迁移 Rollout、Runtime、Participant，保持 Raw SQL、DB time、CAS、no-row 与 SQLSTATE 语义。
- [x] 迁移 Activation 全流程，保持 State -> sorted Runtime -> sorted Participant 锁序及 scoped Local Runtime 原子性。
- [x] 迁移 Snapshot，使用 RepeatableRead+ReadOnly 单事务读取 state/revision/runtime/participant/local facts。
- [x] 抽取 GORM Row/Rows/no-row/context/error helper，复用领域 scanner/validation，不引入 AutoMigrate/association/hooks。
- [x] 新增独立 `BootstrapGORM`/`GORMBootstrapResult`，同池构造 GORM Audit/Local Runtime/Model Settings；现有 Bootstrap 与 `cmd/**` 不改。

## 4. 局部验证

- [x] `go test -mod=vendor ./internal/modelsettings/... -count=1 -timeout 60s`
- [x] `go test -race -mod=vendor ./internal/modelsettings/... -count=1 -timeout 60s`
- [x] `go vet -mod=vendor ./internal/modelsettings/... ./internal/audit/... ./internal/localmodelruntime/... ./internal/platform/postgres`
- [x] `go test -mod=vendor -tags=integration -run '^$' ./internal/modelsettings/adapter/postgres ./internal/platform/migration -count=1 -timeout 60s`
- [x] `go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker ./cmd/modelctl ./cmd/local-model-runtime -count=1 -timeout 60s`
- [x] `go list -mod=vendor ./internal/modelsettings/... ./internal/audit/... ./internal/localmodelruntime/... ./internal/workflow/adapter/river ./cmd/api ./cmd/worker ./cmd/modelctl`
- [x] `go mod verify`；`go mod tidy -diff` 只报告已有 `go.sum` 漂移，不应用无关修改。
- [x] 静态扫描 Domain/Application 不导入 GORM/database/sql/pgx；无 AutoMigrate/Migrator/第二 pool/生产 GORM Bootstrap 调用。
- [x] `python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-modelsettings-migration`
- [x] gofmt 与 `git diff --check`

## 5. Review

- [x] 使用 `go-review` 深审 scoped dependency、typed nil、context cause、UoW、commit/rollback、Rows、secret 与错误链。
- [x] 使用 `sql-code-review` 深审参数化/casts、锁序、DB time、CAS、trigger、权限、Snapshot 和执行计划。
- [x] 使用 `trellis-check` 核对 PRD/Design、legacy wiring、Bootstrap、TODO 9 状态与静态证据。
- [x] 独立 reviewer 复核 P0-P2；明确缺陷修复后重跑门禁。

## 6. TODO 9

- [ ] 仅原位参数化既有 legacy/GORM integration factory，不新增测试文件；每个实现使用独立数据库与同一完整 platform Pool。
- [ ] 实测 Revision+Audit、Activation+Local Runtime、Fence+River 三条同事务提交/回滚。
- [ ] 实测双进程锁序、DB-time lease、CAS/no-row/SQLSTATE、Snapshot、一致性、cancel/commit ambiguity、连接释放和 EXPLAIN。
- [ ] TODO 9 全部通过后才勾选 PRD AC；Final 仍负责生产切换和 legacy 删除。

## 7. 回滚点

- TODO 9 前只删除 staged GORM/scoped/Bootstrap 文件；legacy pgx、Schema、测试和 `cmd/**` 不变。
- TODO 9 后保留 legacy Composition 为 Final 回退点；禁止双写或 fallback 掩盖差异。
