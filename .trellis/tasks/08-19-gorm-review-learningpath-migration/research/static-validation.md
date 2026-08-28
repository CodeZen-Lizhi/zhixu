# Review Learning Path GORM 静态验证

## 结论

- 已在 `internal/review/learningpath/adapter/postgres` 新增 staged `GORMRepository`，完整实现现有 11 个 `application.Store` 方法。
- legacy `Repository` / `NewRepository` 与 API、Worker 生产构造保持不变；未新增 selector、双写、fallback 或第二连接池。
- 所有多语句写事务和 Path+Steps 聚合读取均通过 `Pool.UnitOfWork()` / `UnitOfWork.Within`，callback 内只使用 `platformpostgres.GORMTransaction(scope)`。单 SQL receipt lookup 使用同一 Pool 的共享 GORM root。
- 本次静态阶段未发现 P0/P1 Go 或 SQL 缺陷；真实 PostgreSQL TODO 9 已部分执行但尚未全量通过，因此任务保持 `in_progress`，PRD AC 全部未勾选。

## 修改文件

- `internal/review/learningpath/adapter/postgres/gorm_model.go`
- `internal/review/learningpath/adapter/postgres/gorm_queries.go`
- `internal/review/learningpath/adapter/postgres/gorm_repository.go`
- 本任务 `implement.md` 与本记录。

## 行为与边界证据

- 构造函数只接受一个 `*platformpostgres.Pool`，并从该 Pool 同时获取 GORM root 和 Unit of Work；编译期断言覆盖稳定 Store Port。
- 四个私有 model 显式映射 Path、Step、command 与 reservation，关闭自动时间；没有 `AutoMigrate`、`Migrator`、association、hook 或 soft delete。
- Workspace/Answer/reservation/advisory/aggregate 锁顺序、PENDING/COMPLETED/ABANDONED 状态机、attempt/digest fence、Path/Step CAS、append-only receipt、精确 hold release 和数据库时间与 legacy 实现对齐。
- Evidence 仍以一个参数化 `unnest`/join 查询按请求序号投影；Step 使用一次有界 batch insert；Path/Steps 按 `step_no,id` 稳定读取并执行完整 aggregate validation。
- SQL 为包内固定常量和值参数；不存在用户控制 identifier、`SELECT *`、root `.Transaction(`、`SQLTransaction(scope)`、`*sql.Tx` 解包或独立 `gorm.Open`。
- 错误分类保留已有 foundation error，识别 context cancel/deadline、transaction ended、GORM/database/sql/pgx no-row 与既有 SQLSTATE；Adapter 不新增日志或含敏感参数的公开错误。

## 已运行验证

| 检查 | 结果 |
| --- | --- |
| `go test -mod=vendor ./internal/review/learningpath/... -count=1 -timeout 60s` | PASS |
| `go test -race -mod=vendor ./internal/review/learningpath/... -count=1 -timeout 60s` | PASS |
| `go vet -mod=vendor ./internal/review/learningpath/...` | PASS |
| integration tag compile-only | PASS |
| `go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker -count=1 -timeout 60s` | PASS |
| scoped `go list -mod=vendor` | PASS |
| `go mod verify` | PASS，`all modules verified` |
| `go mod tidy -diff` | NOT PASS；输出为共享脏工作树既有的全局 `go.sum`/依赖漂移，本 child 未应用输出且未修改依赖文件 |
| target 禁止模式、Port 泄漏、生产 wiring 静态扫描 | PASS |
| `python3 ./.trellis/scripts/task.py validate ...` | PASS；仅有已知的单项注入超过 32 KiB 警告 |
| `gofmt -d internal/review/learningpath/adapter/postgres/*.go` | PASS，无输出 |
| scoped `git diff --check` 与 untracked 文件 whitespace 检查 | PASS，无输出 |

## Go Review

- Store 完整性、constructor 防御、context/error chain、opaque transaction scope、Rows `Err/Close`、锁顺序、CAS、commit/rollback ownership 与 legacy 兼容均完成静态审查。
- callback 输出只在 `Within` 成功返回后对 caller 可见；commit 失败返回空结果，不把未提交状态作为成功响应。
- scoped GORM DB 不缓存、不逃逸 callback；公开聚合读取使用 read-only UoW，写事务内部聚合读取复用当前 write scope，不发生 nested UoW。

## SQL Review

- 所有 workspace 归属、receipt key、reservation binding、Path/Step identity 与 CAS 条件均显式进入 SQL；唯一约束、FK 与 trigger 继续作为最终一致性防线。
- reservation/receipt/maintenance 使用 `statement_timestamp()`；Path/Step command 使用 Application 提供的冻结时间。
- maintenance 保持有界稳定排序、`FOR UPDATE SKIP LOCKED`、精确 digest hold 更新和 duplicate ACTIVE hold guard。
- 未发现动态 SQL、N+1、逐 Step 写入、无界 maintenance、非参数化值或应用层 upsert 掩盖唯一冲突。

## TODO 9 剩余门禁

- 将 maintenance-vs-hold/Complete 的前两种确定性 winner 场景扩展为 legacy/GORM 成对断言；当前 GORM 已覆盖同/异 key 结果、reopen、late hold/Complete 与 terminal closure，但 Complete-wins-before-maintenance 的 `SKIP LOCKED` CTE barrier 仍只覆盖 legacy。
- 补齐更多 SQLSTATE（尤其 FK）和 Rows/额外 commit failure 的 GORM 专项故障注入；当前已验证 no-row、`context.Canceled`、deadline、receipt unique `23505`、历史 trigger `23514` 和 commit-response-loss。
- 记录 evidence/maintenance 关键查询的真实 PostgreSQL statement count、索引与 `EXPLAIN` 证据。
- TODO 9 通过前不得切生产 Composition、删除 legacy、勾选 PRD AC、完成或归档任务。

## 独立 Trellis Check

- 按 `check.jsonl` 从文件系统复读 `AGENTS.md`、Trellis workflow、父任务 inventory、baseline、PRD/Design/Implement，以及 backend database/error/logging/quality 的相关 M8 章节；未把 32 KiB 注入截断视为完整规范。
- 对新增 3 个 GORM 文件逐方法复核 11 个 `application.Store` 方法、`00060` 列/约束、legacy 锁序、CAS、receipt/reservation/hold fence、Workspace 隔离、稳定顺序、DB 时间、取消、错误链、Rows 生命周期和无日志敏感载荷。
- 未发现当前静态范围内可证实的 Critical/Required 或 P0/P1 缺陷；未修改 legacy pgx、生产 wiring、Foundation、迁移、依赖或其他任务文件。
- `go test -race -mod=vendor ./internal/review/learningpath/... -count=1 -timeout 60s`、`go vet -mod=vendor ./internal/review/learningpath/...`、integration compile-only、API/Worker compile-only、`go list -mod=vendor`、`go mod verify`、Trellis validate、gofmt、禁止模式扫描和 `git diff --check` 均通过。
- `go mod tidy -diff` 仍报告共享 dirty worktree 的既有依赖/`go.sum` 漂移；未应用输出，未修改依赖文件。
- 本轮没有产生值得更新 `.trellis/spec/` 的稳定新约定；剩余真实 PostgreSQL 行为继续留给 TODO 9，任务保持 `in_progress`，PRD Acceptance Criteria 保持未勾选。

## Dev 分支同步

- 2026-08-24 将 3 个 staged GORM 文件提交到本地 `dev`，代码提交为 `2cbad522 feat(review): 新增 Learning Path GORM 持久化实现`。
- 提交前在 `dev` 工作树复跑 `go test -mod=vendor ./internal/review/learningpath/... -count=1 -timeout 60s`、`go vet -mod=vendor ./internal/review/learningpath/...`、Trellis validate 与 gofmt，均通过；validate 仅保留既有的 32 KiB 注入警告。
- 提交只包含本 child 的 3 个目标文件；Foundation、依赖、Trellis runtime、其他模块及其他任务的 staged/dirty 内容均未纳入。
- Phase 3.3 判断无需修改公共 spec：本轮没有新增可复用契约，opaque Unit of Work 约束已有稳定事实源；TODO 9 未通过，任务继续保持 `in_progress`，不归档、不切生产 Composition。

## TODO 9 Shared Fixture Probe (2026-08-27)

- `review_learning_path_test_helpers_integration_test.go` now provisions each integration database through `testdb.Require`, returning the shared `platformpostgres.Pool.DB()`; manual `ZHIXU_TEST_DATABASE_URL` admin pooling and migration lifecycle were removed.
- Added `TestReviewLearningPathPostgreSQLLegacyAndGORMCreateReadParity`, running once per isolated fixture for `legacy-pgx` and `gorm` stores and checking Create, `GetByReviewAnswer`, `Get`, exact replay, Step transition/replay, and Path status transition/replay on real PostgreSQL/Testcontainers.
- `go test -mod=vendor -tags=integration -run '^TestReviewLearningPathPostgreSQLLegacyAndGORMCreateReadParity$' -count=1 -p 1 -timeout 120s ./internal/review/learningpath/adapter/postgres` passed (10.550s); the full package integration suite passed (48.110s), and `go test -race -mod=vendor -tags=integration -count=1 -p 1 -timeout 120s ./internal/review/learningpath/adapter/postgres` passed (56.172s). The parity race rerun also passed (14.264s).
- Existing barrier and commit-response-loss fixtures wrap pgx transactions directly and remain legacy-only; extending those fault injections to GORM requires a separate adapter-aware hook and is an explicit TODO 9 gap, not claimed as parity evidence.

## TODO 9 GORM Transaction And Fault Probe (2026-08-28)

- `runReviewPathIntegrationVariant` continues to use only `testdb.Require`'s migrated `platformpostgres.Pool`. Its GORM branch obtains `Pool.GORM()` and `Pool.UnitOfWork()` before constructing `NewGORMRepository(platform)`; no test opens a second application pool. `internal/platform/testdb/fixture.go:286-309` confirms that fixture setup runs migration through a temporary migration pool, closes it, then calls one `platformpostgres.Open` for the exposed shared pool. The existing real-platform test `go test -mod=vendor -tags=integration -run '^TestRealGORMPoolUsesSingleFacadeAndClosesIdempotently$' -count=1 -p 1 -timeout 60s ./internal/platform/postgres` passed (0.740s), proving the shared facade closes idempotently and stops exposing GORM/UoW afterward.
- `TestReviewLearningPathPostgreSQLAbandonedReopenAndAttemptFence` now runs both `legacy-pgx` and `gorm` against isolated databases. It proves PENDING -> ABANDONED maintenance, ORPHANED old Artifact hold, same-key reopen with incremented attempt, and old-attempt Prepare/Complete fences without altering the terminal closure. The targeted PostgreSQL run passed (13.151s) and its race run passed (22.763s).
- `TestReviewLearningPathPostgreSQLGORMConcurrentSameAndDifferentKeys` proves real GORM same-key one-original/one-replay and different-key one-original plus one-replay-or-reservation-conflict behavior, with a single completed Path and exact current hold release. PostgreSQL passed (15.701s); race passed (13.984s).
- `TestReviewLearningPathPostgreSQLGORMHistoryTriggerRollback` calls the GORM Store with a deliberately non-monotonic command timestamp. PostgreSQL's path/step history trigger returns SQLSTATE `23514`, classified as non-retryable `LEARNING_PATH_PERSISTENCE_INVALID`, and verifies rollback leaves Path/Step versions and receipt count unchanged. PostgreSQL passed with the lock probe (16.073s); race passed (22.980s).
- `TestReviewLearningPathPostgreSQLGORMTransactionLockBarrierAndDeadline` acquires the real Workspace `FOR UPDATE` row through the same shared `Pool.DB()`, observes the staged GORM UoW in `pg_stat_activity` waiting on that lock, verifies it cannot return early, then confirms a deadline retains `errors.Is(err, context.DeadlineExceeded)`, releases the transaction, observes no lingering lock waiter, and successfully reuses the same GORM Store. This is transaction-visible evidence that callback SQL uses the opaque shared UoW scope rather than an independent root. The targeted race rerun passed (10.846s).
- `TestReviewLearningPathPostgreSQLGORMCompleteResponseLossReplay` decorates the repository's already-created `foundation.UnitOfWork` only in test code. The decorator invokes the real inner UoW, returns a controlled error only after it successfully returns, and is armed at the blocked `CompleteReviewCreate` call so Begin/Prepare do not fault. It proves a committed GORM completion is reported as `LEARNING_PATH_DEPENDENCY_UNAVAILABLE` with the injected cause and then replays correctly with both the original and a different key. PostgreSQL passed (10.143s); race passed (11.257s). No `internal/platform` hook or alternative pool was introduced.
- `TestReviewLearningPathPostgreSQLMaintenanceFencesLateHoldAndComplete` now runs its first two deterministic maintenance-winner scenarios for both implementations. They prove that a late Artifact hold cannot escape an ABANDONED reservation, and a late Complete leaves exactly the ORPHANED Artifact hidden. The direct Testcontainers rerun, together with `TestReviewLearningPathPostgreSQLGORMCancellationAndUniqueReceiptRollback`, passed in 37.891s and race passed in 35.146s.
- `TestReviewLearningPathPostgreSQLGORMCancellationAndUniqueReceiptRollback` verifies that a canceled context retains both `context.Canceled` and its caller cause through the staged Store. It then inserts the same `learning_path_command` receipt twice through the real GORM transaction scope: PostgreSQL `23505` is classified as the stable idempotency conflict and the failed transaction leaves no receipt behind. The normal and race runs above cover this probe.
- All targeted PostgreSQL commands used `-mod=vendor -tags=integration -p 1`; Testcontainers started `pgvector/pgvector:pg16` through the shared fixture. The final full package suite passed in 108.194s, and its final full `-race` rerun passed in 106.384s. The package static tests, race tests, vet, gofmt, scoped `git diff --check`, and `task.py validate` were also rerun on 2026-08-28. Task validation passed with only the pre-existing 32 KiB context-injection warnings.
- These probes do not claim full TODO 9 completion: the existing legacy-only maintenance barrier remains unported to GORM, no explicit FK SQLSTATE or Rows/extra commit-failure injection was added, and no statement-count/EXPLAIN evidence exists. Therefore all PRD Acceptance Criteria remain unchecked, the task stays `in_progress`, production composition remains legacy, and the task is not archived.
