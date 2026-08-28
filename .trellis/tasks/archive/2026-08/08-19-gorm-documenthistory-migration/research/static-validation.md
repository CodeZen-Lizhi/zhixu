# Document History GORM 静态验证

## 结论

- 已新增未接线的 `GORMRepository`，并保留 legacy pgx Repository 与生产 Composition。
- `GORMRepository` 实现既有 `application.DocumentReader`；Document 与 Commit Mapping 复用私有 record/scanner、输入校验和错误分类。
- 本轮未修改 `cmd/**`、migration、`go.sum` 或 vendor 源，也未调用 `AutoMigrate`/Migrator。
- 已原位修改既有 `repository_integration_test.go`：测试数据库统一由 `internal/platform/testdb` 管理；legacy 从 `Fixture.Pool().DB()`、GORM 从同一 `Fixture.Pool().GORM()` 构造。没有平行容器、临时库生命周期或独立 GORM root。
- TODO 9 的 Document History 真实 PostgreSQL 证据已经补齐；本 child 仍保持 `in_progress`，等待父任务复核后统一更新状态、归档和提交。

## 实现范围

- `queries.go` 集中 legacy pgx 与 GORM Raw SQL。两条路径保持相同列投影和 CTE 结构，仅按执行器使用 `$n` 或 `?` 占位符。
- `model.go` 通过共享 scanner 显式处理 UUID text、nullable revision、revision number 与 `sql.NullTime`，时间统一投影为 UTC。
- `gorm_repository.go` 通过共享 GORM root 执行 `Raw(...).Row()` 与 `Raw(...).Rows()`；`MapCommits` 使用 `pq.Array(values)` 作为一个 `driver.Valuer` 绑定 `?::text[]`，并关闭 Rows、检查 `rows.Err()`。
- `repository.go` 的 legacy pgx 路径复用相同 SQL、scanner、校验和 no-row 映射，公开接口与生产构造点不变。
- `go.mod` 仅将仓库已固定且已 vendored 的 `github.com/lib/pq v1.12.3` 提升为直接依赖；同文件其他 GORM/River 差异属于共享 Foundation 基线。

Proposal Commit 没有 `document_id`。SQL 保留既有 `(workspace_id, target_path)` 绑定；调用链先以 `(workspace_id, document_id)` 精确读取 Document，再传入 Workspace 内唯一 canonical path。Article Revision 与 Publication Binding 仍直接约束 Workspace/Document。

## 局部门禁

以下命令通过：

- `go test -mod=vendor ./internal/documenthistory/... -count=1 -timeout 60s`
- `go test -race -mod=vendor ./internal/documenthistory/... -count=1 -timeout 60s`
- `go vet -mod=vendor ./internal/documenthistory/... ./internal/platform/postgres`
- `go test -race -mod=vendor -tags=integration -run '^$' ./internal/documenthistory/adapter/postgres -count=1 -timeout 60s`（只证明 integration 编译）
- `go test -mod=vendor -run '^$' ./cmd/api -count=1 -timeout 60s`
- `go list -mod=vendor ./internal/documenthistory/... ./cmd/api`
- `go mod verify`
- Domain/Application 数据库 import 扫描、`AutoMigrate`/Migrator 扫描、生产 wiring 扫描、`task.py validate` 与 `git diff --check`

`go mod tidy -diff` 已执行并以 1 退出：它会规范化任务开始前已存在的 `go.sum` 脏基线（移除历史 `go.mod` checksum，并补充共享 Foundation 间接依赖 checksum）。本 child 未应用该 diff，也未改写 `go.sum`。

## Review

### Go Review

- 检查了 context 传递、nil/零值 GORM root、资源关闭、接口满足、错误链与并发安全。
- 发现 GORM callback 异常时 `Row()`/`Rows()` 理论上可能返回 nil；已在 scanner/Close 前增加 nil guard，避免 panic，并重跑全部局部门禁。
- `GetDocument` 使用 `Row().Scan` 保持 `sql.ErrNoRows`；取消、deadline 与其他数据库错误继续使用既有稳定分类。未发现剩余 P0/P1 问题。

### SQL Review

- SQL 显式列投影且全部参数化；OID 不拼接，不传裸 `[]string`。单条 CTE 使用 ordinality 保持请求顺序，无逐 Commit N+1。
- Workspace/Document/path 条件、nullable 扫描和 external Commit 跳过语义保持 legacy 行为。底层多重映射仍由 Application 校验 fail closed。
- migration 已存在 `idx_authoring_publication_history_git` 与 `uq_proposal_commit_git`，但实际计划必须由 TODO 9 的真实数据 `EXPLAIN` 证明。未发现剩余 P0/P1 问题。

### Trellis Check

- 最终检查补齐了 `MapCommits` 对 nil `*sql.Rows` 的防御，异常 GORM callback/root 不再在 `Close` 处 panic；修复后重新通过模块 test/race、integration compile、API compile、vet、typecheck、module verify、Trellis validate 与格式检查。
- 本机未安装 `golangci-lint`；lint 覆盖使用仓库现有 `go vet`，并结合 `gofmt -d`、`go list` 与编译门禁。未新增 suppress、调试日志或类型绕过。
- 本轮没有形成新的跨模块稳定契约，现有数据库、错误、日志、质量与 Document History 规范已覆盖实现，因此无需修改 `.trellis/spec/`。

## TODO 9 真实 PostgreSQL 门禁

以下命令在本机 Testcontainers PostgreSQL/pgvector migrated disposable database 上通过：

- `go test -race -mod=vendor -tags='integration testcontainers' -run '^TestRepositoryReadsDocumentAndBatchMapsManagedAndExternalCommits$' ./internal/documenthistory/adapter/postgres -count=1 -timeout 5m -p 1`

结果：`ok github.com/CodeZen-Lizhi/zhixu/internal/documenthistory/adapter/postgres 11.153s`。

该测试通过 `testdb.Require` 取得一个共享 `platformpostgres.Pool`，使用其 `DB()` 写入 fixture、其 `GORM()` 运行 staged Repository，并与同一 Pool 的 legacy Repository 对照。实证覆盖：

- managed Article Revision、Proposal Commit、external Commit 混合页与请求顺序；Article-only/Proposal-only partial mapping、cross-Workspace 与错误 target path 均为空映射。
- 40 位 SHA-1、64 位 SHA-256 OID，50 条上限，重复/非法 OID 拒绝；legacy `MapCommits` 计数为一次 `Query`，GORM callback 计数为一次 statement，50 个 Commit 不增加 statement 数。
- 精确 Document missing/cross-Workspace 的 `DOCUMENT_HISTORY_NOT_FOUND`，nullable current revision，`2026-08-04T12:00:00.123456Z` UTC 微秒 Approval 时间，以及 cancel/deadline 的既有 non-retryable/retryable 分类与 cause 保留。
- target path 由精确读取到的 Document snapshot 提供；错误 path 不返回 Proposal 映射。Proposal Commit SQL 仍只直接绑定 Workspace/path，Document 绑定由该调用链与 `(workspace_id, canonical_path)` 唯一约束间接保证。
- 5,000 条跨两个 Workspace、100 个 Document/path 的 Article/Publication Binding/Proposal/Approval/Proposal Commit fixture。50 条实际 `MapCommits` 等价；单条精确请求的 `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, COSTS OFF)` 实际使用 `idx_authoring_publication_history_git` 和 `uq_proposal_commit_git`，并拒绝目标 relation 的 Seq Scan。单条计划用于证明选择性索引访问，50 条行为测试用于证明 statement 数不随输入 Commit 数增长。

首次以 25% 选择性 fixture 跑计划时，PostgreSQL 合理选择了 `proposal_commit` Seq Scan；测试随后改为 100 个跨 Workspace/Document path（精确 path 只占 50/5,000），使索引断言代表实际选择性，而非通过 planner 开关强制索引。

## 回滚

删除本 child 新增的 GORM/record/query 文件，恢复 `repository.go` 内部提取并撤销 `lib/pq` 直接依赖即可。生产仍使用 legacy pgx，无 Schema 或数据回滚。
