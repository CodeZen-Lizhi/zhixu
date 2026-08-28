# Document History GORM 迁移执行计划

## 1. 规划与基线

- [x] 核对父任务执行顺序：Document History 位于 Wave 1，仅依赖 Foundation。
- [x] 读取现有 Repository、Application Port、Domain 校验、API Composition、integration test 与 `00075` 契约。
- [x] 确认 Repository 只有两条只读路径，不拥有写事务、Git 状态或 Proposal 创建。
- [x] 冻结 TODO 9 前边界：不切生产、不删 pgx、不改 migration、不完成或归档。
- [x] 用户审批本 child 的最终 PRD/Design/Implement 摘要。
- [x] 审批后运行 `task.py start`，确认 child 状态为 `in_progress` 再改 Go 代码。

## 2. 实现

- [x] 集中 pgx/GORM 两套参数化 SQL，确保只存在占位符差异且列顺序一致。
- [x] 新增私有 Document/Commit Mapping persistence record 和共享 scanner/mapping。
- [x] 将已固定且 vendored 的 `github.com/lib/pq v1.12.3` 在 `go.mod` 中提升为直接依赖；不改版本、`go.sum` 或 vendor 源。
- [x] 新增 `GORMRepository` 与 `NewGORMRepository(*gorm.DB)`，实现 `application.DocumentReader`。
- [x] 实现 GORM `GetDocument`：使用 `Raw(...).Row().Scan` 保持 Workspace/Document 精确绑定、nullable revision 与 `sql.ErrNoRows` NotFound。
- [x] 实现 GORM `MapCommits`：输入校验、`pq.Array(values)` 单参数 `text[]`、单条 CTE、ordinality、直接/间接 Document 绑定、partial mapping、Rows close/Err；禁止裸 `[]string`。
- [x] 兼容 `sql.ErrNoRows`/GORM no-row，同时保持取消、deadline 与 dependency error 分类。
- [x] 静态确认 Domain/Application 不导入 GORM/`database/sql`/pgx，生产 Composition 仍构造 legacy Repository。

## 3. 局部验证

- [x] `go test -mod=vendor ./internal/documenthistory/... -count=1 -timeout 60s`
- [x] `go test -race -mod=vendor ./internal/documenthistory/... -count=1 -timeout 60s`
- [x] `go vet -mod=vendor ./internal/documenthistory/... ./internal/platform/postgres`
- [x] `go test -race -mod=vendor -tags=integration -run '^$' ./internal/documenthistory/adapter/postgres -count=1 -timeout 60s`
- [x] `go test -mod=vendor -run '^$' ./cmd/api -count=1 -timeout 60s`
- [x] `go list -mod=vendor ./internal/documenthistory/... ./cmd/api`
- [x] `go mod verify`
- [x] `go mod tidy -diff`；若仅涉及任务开始前的用户 `go.sum` 脏改，记录 diff 而不覆盖。
- [x] `rg -n 'gorm\.io|database/sql|jackc/pgx' internal/documenthistory/{application,domain} --glob '*.go'` 无数据库实现泄漏。
- [x] `rg -n 'AutoMigrate|\.Migrator\(' internal/documenthistory cmd/api --glob '*.go'` 无新增 ORM schema 路径。
- [x] `python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-documenthistory-migration`
- [x] `git diff --check`

带 `integration` 且 `-run '^$'` 的命令只验证集成测试编译，不作为真实 PostgreSQL 通过证据。

## 4. Review

- [x] 使用 `go-review` 检查 context、nil/零值依赖、资源关闭、接口满足、错误链和并发安全。
- [x] 使用 `sql-code-review` 检查参数绑定、数组 cast、Workspace/Document/path 隔离、CTE/UNION、ordinality、N+1 和 nullable 映射。
- [x] 修复范围内确认问题并重跑对应局部门禁。
- [x] 将静态交付、命令结果、Review 结论和剩余盲区写入 `research/static-validation.md`。

## 5. TODO 9 门禁

- [x] 复用并调整现有 integration test（不新建测试文件），通过 shared `testdb.Require(...)` 获取 Pool，使用 `database.DB()` 写 fixture、`database.GORM()` 构造 GORM Repository。
- [x] 验证 managed/proposal/external Commit 混合页、请求顺序、部分映射和跨 Workspace 空结果。
- [x] 验证 40/64 位 OID、50 条上限、重复/非法输入拒绝与单 statement 行为。
- [x] 验证 Document missing/cross-Workspace、nullable revision、UTC 微秒时间和 context cancel/timeout；确认 target path 来自精确 `GetDocument` snapshot，并记录 Proposal 映射的间接 Document 绑定边界。
- [x] 在至少 5,000 条跨 Workspace/Document 的代表性 binding/commit fixture 上执行 `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)`；记录 `idx_authoring_publication_history_git` 与 `uq_proposal_commit_git` 的实际使用，并证明 statement 数不随 Commit 数增长。
- [x] TODO 9 工厂已交付且本 child 真实 PostgreSQL 门禁已通过；已勾选 PRD AC 并完成归档。不在本 child 修改 `cmd/**` 或删除 legacy pgx，这些由 Final 执行。

## 6. 回滚点

- TODO 9 前回滚：删除新增 GORM/record/query 文件并恢复 `repository.go` 内部提取；生产行为不变。
- Final 切换回滚：由 Final child 统一回退 Document History Composition 到 legacy `NewRepository(pool)`；本 child 不拥有该改动，无需 Schema 或数据回滚。
- 任何真实 PostgreSQL 行为差异都阻断本 child 完成与 Final 收口就绪，不以 fallback 或双写掩盖。
