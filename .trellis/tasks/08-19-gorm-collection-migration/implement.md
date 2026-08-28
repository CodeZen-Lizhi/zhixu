# Collection Repository GORM 迁移实施清单

## 1. 规划与基线

- [x] 核对 Collection Application/Domain/Adapter、动态 compiler、cursor、durable scan、Schema/trigger/index 与现有测试。
- [x] 盘点 5 个生产 `NewRepository` 构造点、Graph/Health legacy pgx verifier 和 Export/Organizing read consumers。
- [x] 固定写锁/receipt/CAS、read snapshot、revision、keyset、hydration、SQLSTATE 和 TODO 9 前阶段边界。
- [x] 明确 GORM `$n` renderer、重复 marker、array/JSONB carrier 和 scoped verifier 兼容策略。
- [x] 完成 PRD、Design、Go/调用链与 SQL/Schema 研究；独立 Go/SQL 规划 Review 的 P2 已修正并复验。
- [x] 用户审批本最终方案。
- [x] 运行 `task.py start`，确认 child 为 `in_progress` 后再修改 Go 源码。
- [x] 使用 `trellis-before-dev`，从文件系统完整读取 database/error/quality 等适用规范；不得把 context manifest 的摘要当作完整规范替代品。

## 2. Scoped Application Port

- [x] 在 Collection Application 新增 `ScopedDurableScanBindingVerifier`，公开签名只包含 context、foundation scope 与 DurableScanBinding。
- [x] 保留 legacy `VerifyDurableScanBinding(pgx.Tx)` 和 Graph/Health 当前调用；GORM Repository 不通过 `any` 暴露事务。
- [x] scoped verifier 只 unwrap active caller scope，不 commit/rollback/fallback；nil、非平台和 stale scope fail closed。
- [x] 文档与静态检查明确 active foreign-Pool scope 受 Foundation 无 owner identity 限制，TODO 9/Composition 保证同池。

## 3. Staged GORM Repository

- [x] 新增 `GORMRepository` / `NewGORMRepository(*platformpostgres.Pool)`，从一个 Pool 获取 GORM root、Unit of Work 与随机 cursor codec。
- [x] 实现 Create/Update/Archive 默认事务，保持 Workspace lock、receipt exact replay、aggregate lock/CAS、JSONB snapshot 和 commit/rollback 顺序。
- [x] 实现 Get/Search 和 read-only repeatable-read List/Results/Preview，保留 1500ms local timeout、revision、count、keyset、Limit+1 与同快照 hydration。
- [x] 实现 Plan/Read/Revision Verify 与 scoped binding verify；只有 caller-owned scoped verifier使用 `FOR SHARE` 并复核 exact count，独立 Revision Verify 无锁且不重算 count。
- [x] 抽取/复用纯 validation、hash、cursor、SQL builder 和 scanner mapping；legacy/GORM 不得出现两个业务规则事实源。
- [x] 静态断言全部 Repository 能力；生产、测试 fixture 与跨模块 consumer 仍走 legacy。

## 4. SQL binding 与安全

- [x] 实现最终 SQL `$n -> ?` renderer：支持重复 marker/多位序号，拒绝 `$0`、越界、残缺和未消费参数。
- [x] compiler IN、List status、hydration、durable pair/node 数组全部使用单值 `pq.Array`/driver.Valuer；禁止 GORM slice expansion。
- [x] query/view/receipt JSONB 使用 string Valuer 与严格 Scanner；array 输出使用 Scanner，nullable 使用显式 carrier。
- [x] Raw Row/Rows 防御 nil/statement error，Rows Close/Err，no-row 覆盖 pgx/sql/GORM，损坏数据无 partial result。
- [x] 实现共享 `classifyGORM(ctx, err)`：优先 join `ctx.Err()` 与不同的 `context.Cause(ctx)`，再处理 `sql.ErrTxDone`、no-row、精确 SQLSTATE、timeout retryability 和脱敏 unknown error。
- [x] 确认无 AutoMigrate/Migrator、Hook、association/preload、soft delete、第二 pool、delete、双写或 fallback。

## 5. 局部验证

- [x] `go test -mod=vendor ./internal/collection/... -count=1 -timeout 60s`
- [x] `go test -race -mod=vendor ./internal/collection/... -count=1 -timeout 60s`
- [x] `go vet -mod=vendor ./internal/collection/... ./internal/platform/postgres ./internal/foundation`
- [x] `go test -mod=vendor -tags=integration -run '^$' ./internal/collection/... -count=1 -timeout 60s`
- [x] `go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker ./internal/graph/... ./internal/health/... ./internal/export/... ./internal/organizing/... -count=1 -timeout 60s`
- [x] `go list -mod=vendor ./internal/collection/... ./cmd/api ./cmd/worker ./internal/graph/... ./internal/health/... ./internal/export/... ./internal/organizing/...`
- [x] `go mod verify`
- [x] `go mod tidy -diff`；若只显示任务前已有 `go.sum` 漂移，记录而不应用。
- [x] 静态扫描 Application/Domain 无 GORM/database/sql/pgx 泄漏，5 个生产构造和 2 个 legacy verifier consumer 未切换。
- [x] 静态扫描全部 SQL 参数化、动态 identifier 仅来自 Registry/Compiler 白名单、array/JSONB 无裸 slice/`[]byte` 写入。
- [x] `python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-collection-migration`
- [x] `gofmt -d`（受影响 Go 文件）与 `git diff --check`

integration `-run '^$'` 只验证测试编译，不作为真实 PostgreSQL 证据。预计超过 60 秒的命令停止扩大范围并记录盲区。

## 6. Review

- [x] 使用 `go-review` 检查公开 Port、constructor/typed nil、UoW/scope、context cause、Raw Row/Rows、scanner、cursor 和接口兼容。
- [x] 使用 `sql-code-review` 检查动态 SQL 白名单、positional renderer、array/JSONB、Workspace scope、锁序、CAS、keyset、revision、hydration、SQLSTATE 和执行计划。
- [x] 使用 `trellis-check` 检查 PRD/Design、production wiring、Graph/Health 依赖、验证证据和 TODO 9 状态。
- [x] 修复范围内明确问题并重跑对应门禁；结果记录到 `research/static-validation.md`。

## 7. TODO 9 真实 PostgreSQL 门禁

- [ ] 原位抽取 implementation factory；每个 legacy/GORM 子用例创建独立已迁移 database，seed/cleanup 用已提交的 `Pool.DB()`，legacy 用同一 DB、GORM 用完整 Pool，禁止复用外层未提交 pgx fixture。
- [ ] 比较 lifecycle、idempotency exact replay/conflict、name conflict、CAS、archive、Workspace isolation、trigger/constraint/SQLSTATE 和 rollback。
- [ ] 覆盖全部 Registry field/operator、重复/多位 marker、IN/array/JSONB、nullable NULL tail、stable keyset、cursor binding/stale 和 pagination no-gap/no-duplicate。
- [ ] 比较 Results/Preview 的 count/page/hydration/revision 同快照、timeout、`context.WithCancelCause` sentinel+cause、显式 rollback、corrupt row no-partial 和连接释放。
- [ ] 比较 durable Plan/Read/restart、pair/node ordering、checkpoint、definition/revision/count drift、health membership revision 与 scoped caller transaction commit/rollback。
- [ ] 对核心 List/Search/Result/Durable SQL 做目标规模 EXPLAIN，复核现有索引、P95、statement count、page/hydration 上限和无 N+1。
- [ ] TODO 9 全部通过后才勾选 PRD AC 并完成 child；Graph/Health caller 迁移、生产 Composition 切换和 legacy 删除仍由对应 child/Final 执行。

## 8. 回滚点

- TODO 9 前：删除 staged GORM/scoped Port 文件并还原本 child 共享 helper；生产行为与 Schema 不变。
- TODO 9 后、Final 前：保留 legacy 构造与 pgx verifier即可回退 staged 就绪状态。
- Final 切换失败：由 Final 统一回退 Collection Composition；禁止双写、fallback 或独立事务掩盖差异。
