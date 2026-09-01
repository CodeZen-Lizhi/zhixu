# Collection Final Handoff

## 已交付

- 完整 staged `GORMRepository` 覆盖 Collection 写入、检索、Results/Preview 和 durable scan；共享 UoW/Pool、严格 binding、scanner 与错误分类已通过真实 PostgreSQL 双实现门禁。
- `ScopedDurableScanBindingVerifier` 仅接受 opaque Foundation scope，不拥有 commit/rollback，也不回退 root DB。
- HTTP exact replay、全 Registry 查询、NULL keyset、revision/cursor、durable restart/drift、timeout/cancel、SQLSTATE、rollback/commit、连接释放、P95 与 EXPLAIN 均已验证。
- 共享 Graph fixture 已从废弃的 Workspace `test` 状态迁到合法 `inactive`，并通过 functional/capacity seed+cleanup 回归。

## Final Composition

- Final 从唯一 `platformpostgres.Pool` 构造 `collectionpostgres.NewGORMRepository(pool)`，并把同一 Pool 派生的 UoW scope 交给 Graph/Health scoped verifier；不得创建第二连接池、双读、双写或 fallback。
- 切换前确认 Graph/Health owner child 已不再依赖 legacy `VerifyDurableScanBinding(ctx, pgx.Tx, ...)`，再统一替换 API/Worker 的 5 个 Collection legacy 构造点。
- Final 才能删除 `Repository`、`NewRepository(DB)`、legacy pgx verifier 与只为兼容保留的 pgx adapter 分支；本 child 不授权提前删除。

## 发布与回滚

- staged 阶段无需数据迁移或发布动作。Final 切换失败时恢复 5 个 legacy Composition 调用即可；Schema 与数据不需回滚。
- Foundation scope 目前不携带 Pool identity，Composition 必须保证 verifier 与 caller scope 来自同一个 Pool。
- `idx_learning_smart_collection_workspace_status_updated` 的 `id ASC` 与 List 的 `id DESC` 不完全匹配；当前目标规模无顺序扫描且 P95 通过。规模扩张前由 Schema owner 评估匹配方向的新 migration。

## 验证基线

- Collection unit/race、vet、integration compile、HTTP replay、Graph fixture、API/Worker 及受影响模块 compile、`go list`、`go mod tidy -diff`、`go mod verify`、Trellis validate 与 `git diff --check` 均通过。
- 真实 PostgreSQL 场景按顶层测试拆分并设置 60 秒上限；详细矩阵见 `research/static-validation.md`。
- 当前未执行 commit/push；需按 TODO10 child 依赖顺序统一提交，并在纯 Git 快照复跑 API/Worker 编译。
