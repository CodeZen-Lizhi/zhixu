# Ingestion GORM Repository 实施清单

## 1. 规划与基线

- [x] 核对父任务执行顺序：Ingestion 位于 Wave 1，仅直接依赖 Foundation。
- [x] 读取 Repository/Domain/Application、API/Worker/烟测构造点、现有 integration test 与 `00007`/`00015`/`00068` Schema 契约。
- [x] 固定五个端口、Attempt 幂等/CAS、Projection 原子性/不可变性、Strategy 隔离和错误矩阵。
- [x] 调研当前 vendored GORM 的 `Transaction`、`CreateInBatches` 与 `OnConflict DoNothing` 行为，确认 `SkipDefaultTransaction=true` 时必须使用显式外层事务。
- [x] 冻结 TODO 9 前边界：不切 API/Worker/烟测、不删 pgx、不改 migration/测试、不完成或归档。
- [x] 用户审批本 child 的最终 PRD/Design/Implement 摘要。
- [x] 审批后运行 `task.py start`，确认 child 为 `in_progress` 后再修改 Go 源码。

## 2. 共享持久化映射

- [x] Attempt/Projection 使用显式 Raw SQL 列映射；仅为 Clause/批量写入新增 schema-qualified、关闭自动时间/关联的 Provenance/Span/Chunk private record。
- [x] 新增明确的 JSONB carrier，复用 warnings/selector/heading path 的 canonical marshal、decode 和 Domain 校验。
- [x] 复用 pgx/GORM 共用 scanner、ID/UTC/nullable helper；GORM 路径新增兼容 `sql`/pgx/GORM no-row 与 SQLSTATE 分类，保持 legacy 签名。
- [x] 集中 GORM `?` 参数化 SQL；审查列顺序、冲突键、Strategy/Schema 过滤和稳定排序。

## 3. Staged GORM Repository

- [x] 新增 `GORMRepository` / `NewGORMRepository(*gorm.DB)` 并实现 `domain.Repository`，拒绝 nil root/context。
- [x] 实现 `CreateAttempt` 的 insert-or-read、完整幂等绑定复核和 `Created` 语义。
- [x] 实现 `GetAttempt` 与列白名单 `TransitionAttempt` version CAS；禁止 `Save`/全字段更新。
- [x] 实现事务内 `GetProjection`，按固定顺序读取 Spans 和目标 Strategy/Schema Chunks，关闭 Rows 并检查迭代错误。
- [x] 实现事务内 `SaveProjection`：Projection insert/get、Provenance append、新/复用分支、Span identity 映射和确定性 Chunk 复核。
- [x] 以 `projectionBatchSize=500` 批量写 Spans/Chunks；精确冲突目标只用于复用 Projection 的缺失 Chunks，冲突后重读并复核完整集合。
- [x] GORM 分类入口接收 `ctx`：优先保留 context cause，`sql.ErrTxDone` 且 `ctx.Err()!=nil` 时还原取消/deadline；保持 SQLSTATE、no-row、稳定 fallback code、非重试取消语义和日志脱敏。
- [x] 确认生产 API/Worker/跨模块烟测仍构造 legacy `NewRepository`，无双写、fallback、AutoMigrate 或第二连接池。

## 4. 局部验证

- [x] `go test -mod=vendor ./internal/ingestion/... -count=1 -timeout 60s`
- [x] `go test -race -mod=vendor ./internal/ingestion/... -count=1 -timeout 60s`
- [x] `go vet -mod=vendor ./internal/ingestion/... ./internal/platform/postgres`
- [x] `go test -race -mod=vendor -tags=integration -run '^$' ./internal/ingestion/adapter/postgres -count=1 -timeout 60s`
- [x] `go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker -count=1 -timeout 60s`
- [x] `go list -mod=vendor ./internal/ingestion/... ./cmd/api ./cmd/worker`
- [x] `go mod verify`
- [x] `go mod tidy -diff` 现已通过（此前为共享工作树的 go.sum/sqlite 漂移，vendor 同步后收敛）。
- [x] `rg -n 'gorm\.io|database/sql|jackc/pgx' internal/ingestion/{domain,application,http} --glob '*.go'` 无数据库实现泄漏。
- [x] `rg -n 'AutoMigrate|\.Migrator\(' internal/ingestion cmd/api cmd/worker --glob '*.go'` 无 ORM Schema 路径。
- [x] 静态复核 API/Worker/烟测仍使用 legacy 构造；批量 helper 全部接收事务 `tx`，不回落 root。
- [x] `python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-ingestion-migration`
- [x] `git diff --check`

带 `integration` 且 `-run '^$'` 的命令只验证测试编译，不算真实 PostgreSQL 证据。任一命令超过 60 秒则停止扩大范围并记录盲区。

## 5. Review

- [x] 使用 `go-review` 检查 context/error chain、nil/零值依赖、事务 callback、Rows 生命周期、接口满足、race 和 shared helper 漂移。
- [x] 使用 `sql-code-review` 检查参数绑定、SQLSTATE、唯一冲突目标、trigger/不可变性、Workspace/Artifact/Projection/Span 归属、批量/N+1 和事务半失败。
- [x] 使用 `trellis-check` 检查 PRD/Design 一致性、任务边界、production wiring、测试证据和 TODO 9 状态。
- [x] 修复范围内明确问题并重跑对应门禁；将结果写入 `research/static-validation.md`。

## 6. TODO 9 真实 PostgreSQL 门禁

- [x] 原位扩展现有 integration test：legacy 使用独立 pgx transaction fixture；GORM 从共享 `Pool.GORM()` 开外层 transaction，嵌套 savepoint 覆盖 Repository 自身事务；两套 fixture 使用不同 ID、各自 rollback 且无残留。
- [x] 对五个方法逐项比较 legacy/GORM：字段、UTC/nullable/JSON、Created 标志、稳定排序、no-row 和稳定错误码（既有成对用例通过）。
- [x] `GetProjection` 双非空严格隔离、双空返回全部 legacy 策略、一空一非空精确条件行为已由既有成对用例覆盖并通过。
- [x] 并发/重复 `CreateAttempt`：相同绑定只创建一次；幂等键相同但 Attempt Number/Workflow Run 不同冲突（既有用例通过）。
- [x] `TransitionAttempt` 合法矩阵、stale version 与并发 CAS：每个 version 只有一个赢家，失败不改变记录（既有用例通过）。
- [x] 并发/重复 `SaveProjection`：共享 Projection/Provenance、同/新 Chunk Strategy、跨 Workspace、parser/schema 交叉约束与不同确定性内容 fail closed（既有用例通过）。
- [x] Span 批次、Chunk 批次、Provenance、重读、trigger 与 transaction 内 context cancel 的稳定失败均无半个集合残留（既有回滚用例 + 新增阻塞取消用例通过）。
- [x] Projection/Span/Chunk/Provenance update/delete 继续被 trigger 拒绝，`evidence_kind`/`derived_excerpt` 形状保持（既有用例通过）。
- [x] 新增 `TestGORMRepositoryCancellationAndDeadlockClassification` 与 `TestGORMRepositoryBlockedQueryCancellation`：caller cancel/deadline 保留 sentinel 与自定义 cause、`sql.ErrTxDone` 还原、真实 40P01 deadlock 分类为 retryable（40001 与 40P01 共用同一分类分支，注释说明）、失败路径连接释放。期间发现并修复 gormClassify 丢失 WithCancelCause 自定义 cause 的缺陷。
- [x] 新增 `TestGORMRepositorySaveProjectionStatementBounds`：只计数 GORM logger（不展开/保存 SQL 与参数），5,000 Spans + 5,000 Chunks 实测上界 Cnew=4+ceil(spans/500)+ceil(chunks/500)=24、Creuse=5，连接占用与事务时长正常。
- [x] commit-time connection loss/response loss 不可在模块内稳定注入，记录为继承 Foundation 的验证盲区；未为测试增加生产 transaction hook，不声明该盲区已实测。
- [x] TODO 9 门禁全部通过；PRD AC 已勾选。生产 Composition 切换和 legacy 删除仍由 Final child 执行。

## 7. 回滚点

- TODO 9 前：删除 staged GORM/record/query 文件并还原本 child 的共享 helper 提取；生产行为和 Schema 不变。
- TODO 9 后、Final 前：继续保留 legacy 构造即可回退 staged 就绪状态，不涉及数据迁移。
- Final 切换失败：由 Final child 统一回退 API/Worker Composition；不得以双写或静默 fallback 掩盖差异。
- 任何真实 PostgreSQL 行为、批量边界或错误契约差异都阻断本 child 完成。
