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

## 4. 局部验证（历史基线）

本节勾选项记录 staged 实现阶段的历史验证结果，不是 2026-09-01 精简
测试政策下每轮必须重复执行的清单。当前迭代以文末“2026-09-01 精简测试
门禁”和父任务 `research/lean-test-policy-2026-09-01.md` 为准；除非风险
触发，不再默认运行整包 integration race、命令 compile-only、`go list`
或 `go mod verify/tidy`。

> 统一标记：本节全部 `[x]` 项均为“历史基线，不再默认执行”，仅保留可
> 追溯记录；后续验收只按精简门禁和实际风险触发项执行。

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

本节原始清单保留为历史完整契约；按 2026-09-01 精简政策，只有直接改动触发的硬风险项阻断 child 验收，不要求恢复整套矩阵。

- [x] 仅原位参数化既有 integration fixture，不新增测试文件；每个实现使用独立数据库与同一完整 platform Pool。
- [x] 精简硬风险：Revision+Audit、Activation + Local Runtime、Fence+River 均有同事务提交/回滚代表场景。
- [x] 精简硬风险：GORM enqueue `FOR SHARE` 与竞争事务 `FOR UPDATE NOWAIT` 的锁冲突/释放代表场景通过。
- [ ] DB-time lease、CAS/no-row/SQLSTATE、Snapshot、一致性、cancel/commit ambiguity、连接释放和 EXPLAIN 等低频专项按风险触发，不作为当前无差别门禁。
- [x] 直接改动触发的硬风险项已通过，可勾选对应 PRD AC；Final 仍负责生产切换和 legacy 删除。

## 7. 回滚点

- TODO 9 前只删除 staged GORM/scoped/Bootstrap 文件；legacy pgx、Schema、测试和 `cmd/**` 不变。
- TODO 9 后保留 legacy Composition 为 Final 回退点；禁止双写或 fallback 掩盖差异。

## 2026-09-01 精简测试门禁

按父任务精简政策，本 child 保留一个既有 Testcontainers 主路径，并针对本模块直接负责的 Revision/Activation 同事务边界验证一条提交/回滚或竞争场景。双进程、DB-time、EXPLAIN、commit ambiguity、连接释放等专项只在对应实现确有改动或出现风险时执行；不再作为无差别全矩阵前置。Workflow enqueue fence 和跨 owner 原子性仍是硬依赖。

## 2026-09-01 本轮增量

- 在现有 `repository_integration_test.go` 原位接入 TODO 9 `testdb` 工厂，新增共享 `platformpostgres.Pool` 的 GORM 主路径验证。
- 已实测 SaveDesired + scoped Audit 的成功写入、Audit 故障整事务回滚、sequence 不复用和 pre-commit activation failure；已实测 caller-owned enqueue fence 及 stale scope 拒绝。
- 第 6 节精简硬风险项已通过：Activation + Local Runtime、GORM 路径锁竞争/释放、Fence + River 均在本轮同事务/竞争场景中覆盖。双进程完整矩阵、快照/取消/连接/EXPLAIN 等低频专项按风险触发，不作为当前无差别门禁。
