# Authoring Repository GORM 迁移实施清单

## 1. 规划与基线

- [x] 核对 Authoring Application/Domain/PostgreSQL Adapter、Change Control Finalizer、Artifact/Organizing consumers 和 API/Worker Composition。
- [x] 固定 Draft receipt/CAS/Freeze、Publication reservation/binding/reconcile、Restore publication 的锁序、状态、回放和回滚语义。
- [x] 核对 `00069/00071/00072/00073`，并补入 Restore 实际依赖的 `00075` Schema/trigger/index 事实。
- [x] 盘点现有 unit/integration/hardening tests、真实 PostgreSQL fixture 和 TODO 9 双实现方案。
- [x] 运行 Authoring unit/race/vet、integration compile、API/Worker compile、vendor/module 与 diff baseline。
- [x] 完成独立 Go/调用链与 SQL/Schema 规划 Review，修正文档后由用户审批最终方案。
- [x] 用户审批本最终方案。
- [x] 运行 `task.py start`，确认 child 为 `in_progress`，再使用 `trellis-before-dev` 读取实施上下文并修改 Go 源码。

## 2. Staged 构造与共享 mapper

- [x] 新增 `GORMRepository` / `NewGORMRepository(*platformpostgres.Pool)`，只使用共享 GORM root + UoW，并拒绝 nil/失效依赖与 nil context。
- [x] 添加主 Repository、ArticleRevisionSearchRepository 和 Change Control Restore 接口静态断言。
- [x] 抽取可安全共享的 validation、binding、hash、UTC、result mapper 和最小 row scanner；legacy pgx 行为保持不变，不引入公开或弱类型 DB 抽象。
- [x] 新增私有 GORM Row/Rows/Exec helper、固定 `?` SQL 和 nullable/array carrier；全部 Raw SQL 参数化、Rows Close/Err、no-row fail closed。

## 3. Draft 与读取实现

- [x] 实现 Create/Get/Update/List/ListDocuments/Freeze，保持 command lock、receipt-first replay、row lock/CAS、Document/Revision append 和 exact response reconstruction。
- [x] 实现 SearchArticleRevisions、GetDocumentDetail、GetArticleRevisions、GetOverview，保持 Workspace、有界 keyset/lateral、ordinal batch 和 repeatable-read read-only snapshot。
- [x] 使用单值 uuid[] carrier，复核 ordinality/cardinality/Domain binding；禁止 OFFSET、N+1、association、自动时间或 root/transaction 混用。

## 4. Publication 与 Restore 实现

- [x] 实现 Reserve/Abandon/Complete，保持 publish command lock、server-owned target facts、Proposal snapshot、Binding+Reservation+PUBLISH receipt 原子闭合。
- [x] 实现 Reconcile candidate query 与逐 Binding 事务，保持 lock order、exact commit、Recovery/Closed/Published、previous supersede 和 deferred terminal trigger。
- [x] 实现 ValidateRestoreWriteback/FinalizeRestorePublication，保持 exact restore proof、UUIDv5 replay、append-only bridge、Revision/Document CAS 和 deferred closure。
- [x] 保持现有精确 SQLSTATE/constraint error、context cause、`sql.ErrTxDone`、commit/rollback 和安全错误文本；以 callback success 区分 transaction 与 `AUTHORING_COMMIT_FAILED` fallback。
- [x] 静态确认无 AutoMigrate/Migrator、Hook、association、second pool、selector、双写/fallback；生产仍构造 legacy Repository。

## 5. 局部验证

- [x] `go test -mod=vendor ./internal/authoring/... -count=1 -timeout 60s`
- [x] `go test -race -mod=vendor ./internal/authoring/... -count=1 -timeout 60s`
- [x] `go vet -mod=vendor ./internal/authoring/... ./internal/platform/postgres`
- [x] `go test -mod=vendor -tags=integration -run '^$' ./internal/authoring/adapter/postgres -count=1 -timeout 60s`
- [x] `go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker -count=1 -timeout 60s`
- [x] `go list -mod=vendor ./internal/authoring/... ./internal/platform/postgres ./cmd/api ./cmd/worker`
- [x] `go mod verify`
- [ ] `go mod tidy -diff` 只检查：当前输出为任务前已有的全仓 `go.sum`/sqlite 依赖漂移，未应用，待 Foundation/依赖 owner 收敛。
- [x] 静态检查 Domain/Application 无 GORM/database/sql/pgx，API/Worker 仍使用 `NewRepository`，无 migration/测试文件误改。
- [x] `python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-authoring-migration`
- [x] 受影响 Go 文件 `gofmt -d` 与 `git diff --check`

integration `-run '^$'` 只验证测试编译，不作为真实 PostgreSQL 证据。预计超过 60 秒的命令停止扩大范围并记录盲区。

## 6. Review

- [x] 使用 `go-review` 检查接口覆盖、UoW/context、typed nil、Raw Row/Rows、资源生命周期、错误链和 production wiring。
- [x] 使用 `sql-code-review` 检查参数化、锁序、CAS、array、keyset、Workspace、Proposal/Restore proof、trigger 和 transaction closure。
- [x] 使用 `trellis-check` 检查 PRD/Design、owner 范围、验证证据和 TODO 9 状态。
- [x] 修复范围内明确问题并重跑对应门禁；结果写入 `research/static-validation.md`。
- [x] detached 来源工作树的 staged 实现已同步到当前 `dev` 主工作树；保留主工作树较新的 PRD/Design/Schema research，并与 Git Sync、Review Learning Path 完成集成复核。
- [x] 2026-08-24 在 `dev` 主工作树完成提交前复核：8/20 SQL placeholder、未提交结果清零、commit-stage fallback 与 context cause 修复均在位；局部 test/race/vet、integration/API/Worker compile、module、Trellis、gofmt、禁止模式及 scoped diff 门禁通过。

## 7. TODO 9 真实 PostgreSQL 门禁

- [ ] 原位参数化现有 Repository/Publication integration tests；每个 legacy/GORM 子用例使用独立 database 和同一 `platformpostgres.Pool` 的 DB/GORM/UoW。
- [ ] 比较 Draft CAS/replay/Freeze、keyset/Search/batch/detail/overview、Workspace isolation、并发 command/freeze 和 rollback。
- [ ] 比较 Reserve/Abandon/Complete/Reconcile、CREATE_ONLY/REPLACE、Recovery/Closed/Published、Proposal terminal 和 deferred closure。
- [ ] 比较 Restore owner check、exact commit、deterministic Revision ID、并发 finalize/replay 与 append-only bridge。
- [ ] 验证真实 FK/CHECK/SQLSTATE、cancel/deadline/commit failure、corrupt row no-partial、Rows/连接释放和 pool 复用。
- [ ] 对关键查询运行目标数据量 EXPLAIN，确认现有 keyset/publication/revision/history 索引和 statement/result 上限。
- [ ] TODO 9 全部通过后才勾选 PRD AC 并完成 child；Production Composition 与 legacy 删除仍只归 Final。

## 8. 回滚点

- TODO 9 前：删除 staged GORM 文件并还原本 child 的纯 helper 抽取；生产行为和 Schema 不变。
- TODO 9 后、Final 前：继续保留 legacy 构造即可回退 staged 就绪状态。
- Final 切换失败：由 Final child 统一恢复 API/Worker legacy Composition；禁止双写、fallback 或独立 pool 掩盖差异。
