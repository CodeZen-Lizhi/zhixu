# Workspace Repository GORM 迁移实施清单

## 1. 规划与基线

- [x] 核对 Workspace Domain/Application/Adapter、rootgrant、runtimegrant、Capture writer、Audit rebind 与 API/Worker/CLI Composition。
- [x] 固定 Workspace/Source replay、Registry/Rebind、Control/Runtime、Git checkpoint 的事务、锁序、CAS、DB time 和错误语义。
- [x] 核对 `00002/00006/00031/00067/00077/00081` Schema/trigger/index 事实与只读跨 owner 投影。
- [x] 盘点现有 unit/integration/migration/EXPLAIN tests、真实 PostgreSQL fixture 和 TODO 9 双实现方案。
- [x] 运行 Workspace/rootgrant/Capture unit/race/vet、integration compile、cmd compile、vendor/module 与 diff baseline。
- [x] 完成独立 Go/调用链、SQL/Schema 和规划 Review；P0/P1 边界已纳入 PRD/Design。
- [x] 用户审批本最终方案。
- [x] 运行 `task.py start`，确认 child 为 `in_progress` 后再使用 `trellis-before-dev` 读取实施上下文并修改 Go 源码。

## 2. Opaque Port 与 staged 构造

- [x] 在 Workspace Application 新增 `ScopedSourceWriter`；公开签名只包含 context、foundation scope 与 Workspace Domain 类型。
- [x] 保留 legacy `TransactionWriter(pgx.Tx)` 和 Capture 当前调用；GORM scoped writer 不 commit/rollback/fallback，拒绝 nil、非平台和 stale scope。
- [x] 新增 `GORMRepository` / `NewGORMRepository(*platformpostgres.Pool, ...options)`，从一个 Pool 获取 GORM root 与 UoW。
- [x] 添加主 Repository、Active/Material/List、Registry、Control、Git capture 和 scoped writer 的静态断言。
- [x] GORM options 只接稳定 root resolver 与 Audit ScopedAppender；不复用 legacy `any` transaction appender。

## 3. Workspace、Source 与读取

- [x] 抽取/复用 column、validation、nullable/UTC、binding 和窄 scanner helper；legacy pgx 行为保持不变，不新增弱类型 DB 抽象。
- [x] 实现 Workspace create/get/active/root list 与 capability gating，保持 managed/direct、唯一性和防枚举语义。
- [x] 实现 Source/Artifact/Version 单条与批量注册，保持 exact replay、metadata conflict、soft-delete 恢复、legacy artifact 补齐和原子回滚。
- [x] 实现 Source Material 与 Source Version keyset/LATERAL list，保持全部 Workspace predicate、Limit+1、过滤和损坏行 fail-closed。
- [x] Raw Row/Rows 防御 statement/nil handle，Rows Close/Err；禁止 OFFSET、N+1、association、自动时间或 root/tx 混用。

## 4. Registry、Control、Runtime 与 Git

- [x] 实现 Resolve/List/availability/remove/rebind，保持 advisory lock、state/gate/Workspace/runtime 锁序、history/CAS 和同 scope Audit。
- [x] 实现 ControlSnapshot 的 repeatable-read read-only snapshot，以及 Switch begin/renew/takeover/advance/revoke/commit/restore/finish 状态机。
- [x] 实现 Runtime register/heartbeat/phase，保持 share/update lock、role/instance/version、grant/binding、DB time 和 quiescence fence。
- [x] 实现 Git apply/complete，保持 Workspace advisory lock、checkpoint、scoped Source writer、`pq.Array` tombstone、顺序 fence和 exact replay。
- [x] 保留精确 SQLSTATE/constraint、RowsAffected/CAS、context cause、`sql.ErrTxDone`、callback/commit/rollback 和安全错误文本。

## 5. rootgrant 与 staged Runtime Composition

- [x] 新增共享 Pool 上的 GORM `AuthoritativeStore` 与 runtime resolver factory，复用 managed/direct 无 fallback、authority view 和 capability revalidation。
- [x] 新增独立 `GORMProcessComposition`，Repository 字段只暴露稳定 Port 聚合，Control/Resolver/Lease 生命周期与 legacy 等价。
- [x] 保留现有 `ProcessComposition`、具体 `*workspacepostgres.Repository` 字段及全部 cmd helper；本 child 不改生产接线。
- [x] 静态确认无 AutoMigrate/Migrator、Hook、association/preload、soft delete、second pool、selector、双写/fallback。

## 6. 局部验证

- [x] `go test -mod=vendor ./internal/workspace/... ./internal/platform/rootgrant -count=1 -timeout 60s`
- [x] `go test -race -mod=vendor ./internal/workspace/... ./internal/platform/rootgrant -count=1 -timeout 60s`
- [x] `go vet -mod=vendor ./internal/workspace/... ./internal/platform/rootgrant ./internal/platform/postgres ./internal/capture/adapter/postgres`
- [x] `go test -mod=vendor -tags=integration -run '^$' ./internal/workspace/adapter/postgres ./internal/platform/migration -count=1 -timeout 60s`
- [x] `go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker ./cmd/workspacectl ./cmd/workspaceprobe ./internal/capture/... -count=1 -timeout 60s`
- [x] `go list -mod=vendor ./internal/workspace/... ./internal/platform/rootgrant ./internal/capture/... ./cmd/api ./cmd/worker ./cmd/workspacectl ./cmd/workspaceprobe`
- [x] `go mod verify`
- [x] `go mod tidy -diff`；若只显示任务前已有 `go.sum` 漂移，记录而不应用。
- [x] 静态扫描 Application/Domain 无 GORM/database/sql/pgx/any 新泄漏，production/legacy Capture 构造与 writer 未切换。
- [x] 静态扫描 SQL 参数化、advisory/row lock 只在 scoped tx、array 单值绑定、无 AutoMigrate/association/second pool。
- [x] `python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-workspace-migration`
- [x] `gofmt -d`（受影响 Go 文件）与 `git diff --check`

integration `-run '^$'` 只验证测试编译，不作为真实 PostgreSQL 证据。预计超过 60 秒的命令停止扩大范围并记录盲区。

## 7. Review

- [x] 使用 `go-review` 检查公开 Port、constructor/typed nil、UoW/scope、context cause、Raw Row/Rows、scanner、接口兼容和 runtime lifecycle。
- [x] 使用 `sql-code-review` 检查参数化、array、Workspace scope、锁序、CAS、DB time、keyset/LATERAL、trigger、SQLSTATE 和执行计划。
- [x] 使用 `trellis-check` 检查 PRD/Design、Audit/Capture/rootgrant 依赖、production wiring、验证证据和 TODO 9 状态。
- [x] 修复范围内明确问题并重跑对应门禁；结果记录到 `research/static-validation.md`。

## 8. TODO 9 真实 PostgreSQL 门禁

- [ ] 原位抽取 implementation factory；每个 legacy/GORM 子用例创建独立已迁移 database，seed/cleanup 使用同一 `platformpostgres.Pool.DB()`。
- [ ] 比较 Workspace/Source/Artifact/Version lifecycle、exact replay/conflict、batch rollback、Material scope、root capability 与 keyset/LATERAL page。
- [ ] 比较 Resolve/availability/remove/rebind 的 lock/CAS/history/Audit 原子性、两个反向 fingerprint 并发与 response-loss replay。
- [ ] 比较 Control snapshot、Begin/TakeOver/Advance/Revoke/Commit/Restore/Finish 的 lease、lock、DB time、trigger、rollback 与 commit failure。
- [ ] 比较 Runtime role register/heartbeat/phase、owner/version/future heartbeat、grant/binding fence 与 quiescence。
- [ ] 比较 Git apply/complete、并发串行、out-of-order、exact replay、tombstone/reappearance、array binding 和 checkpoint consistency。
- [ ] 覆盖真实 FK/CHECK/trigger/SQLSTATE、cancel/cause/deadline、`sql.ErrTxDone`、corrupt row no-partial、连接释放与目标规模 EXPLAIN。
- [ ] 以 caller-owned UoW 验证 scoped writer 的 Source/Artifact/Version commit/rollback、invalid/stale scope 拒绝及不自行提交/回滚；Foundation affinity 完成后再补 cross-pool scope 拒绝。
- [ ] TODO 9 全部通过后才勾选 PRD AC 并完成 child；生产 Composition 切换和 legacy 删除仍由 Final 执行。

## 9. 下游交接（不阻断本 child）

- Capture child 改为依赖 `ScopedSourceWriter`，并验证 Source/Capture/Outbox/receipt 与 URL materialization 的共同 commit/rollback/replay。
- Final 将 cmd helper 从具体 `*workspacepostgres.Repository` 收窄为实际 Port，以同一 Pool 切换 staged Runtime Composition。

## 10. 回滚点

- TODO 9 前：删除 staged GORM/scoped Port/rootgrant/Composition 文件并还原本 child 的共享 helper；生产行为与 Schema 不变。
- TODO 9 后、Final 前：保留 legacy Repository/TransactionWriter/Composition 即可回退 staged 就绪状态。
- Final 切换失败：由 Final 统一恢复 legacy Composition；禁止双写、fallback 或独立事务掩盖差异。
