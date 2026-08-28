# Git Sync GORM Repository 实施清单

## Phase 1. Baseline And Scope

1. [x] 确认本任务工作树没有 Git Sync 代码脏改，保存其他会话改动为只读基线。
2. [x] 盘点 23 个方法、11 个事务、八张表、锁/CAS/lease/receipt/error 与现有测试入口。
3. [x] 运行未改动前的 Git Sync 单元测试与可用集成测试，记录环境跳过或失败证据。
4. [x] 核对 `Pool.GORM()`、`Pool.UnitOfWork()`、`GORMTransaction()` 当前签名；禁止修改 Foundation。

## Phase 2. Boundary And Models

1. [x] 新增 `GORMRepository`，依赖共享 GORM Root、Unit of Work 和 Sealer，并断言五个 Application interface。
2. [x] 为八张表新增显式 Model/TableName，锁定 UUID、JSONB、bytea、nullable、时间与版本字段。
3. [x] 新增 scanner、within、advisory lock、no-row 和错误分类 helper；不改旧 pgx 文件。
4. [x] 建立参数化 GORM SQL；不调用 AutoMigrate/Migrator，不使用 Hook/association/自动时间戳。

## Phase 3. Config And Credential

1. [x] 实现 Get/Replay/Save/Delete Config 与 OpenCredential。
2. [x] 保持 replay 优先、Workspace lock、Config `FOR UPDATE`、active Run fence、Revision append-only 和 CAS。
3. [x] 保持 Credential `keep|replace|clear`、AAD、envelope 校验和所有明文销毁。

## Phase 4. Run And Attempt

1. [x] 实现 Create/Replay/Get/GetCurrent/List Run，保持配置双重栅栏、唯一活动 Run 和 keyset 分页。
2. [x] 实现 BeginAttempt、TransitionRun、CompleteRun，保持 DB-time lease、phase/version CAS 和 checkpoint。
3. [x] 保持 changed_files JSONB 有界校验，不新增 N+1、无界查询或逐条写入。

## Phase 5. Outbox, Follow-up And Auto Sync

1. [x] 实现 Claim/Publish/Reschedule/Poison，保留单 statement SKIP LOCKED claim 与 DB-time lease fence。
2. [x] 实现 Begin/Complete/Fail Follow-up、Retry/Replay Index，保持 active lease、CAS 和 receipt。
3. [x] 保持 Poison 的 mutation result-unknown 与只读失败分流。
4. [x] 实现有界 Auto Sync LATERAL 查询，保持有效 Revision、稳定排序和幂等 key。

## Phase 6. Verification And Review

1. [x] `go test -timeout=60s ./internal/gitsync/...`。
2. [x] 使用 `internal/platform/testdb` 既有 fixture 分别运行 legacy 与 GORM integration test；不复制容器生命周期。
3. [x] 运行 `go vet ./internal/gitsync/...`、局部仅编译、import/AutoMigrate/范围检查和 `git diff --check`。
4. [x] 使用 `go-review`、`sql-code-review`、`trellis-check` 审查并修复范围内问题。
5. [x] 记录真实 PostgreSQL GORM 并发、锁、lease、trigger、回放和查询计划证据；生产切换与 legacy 删除仍留给 Final。
6. [x] 与 Review Learning Path、Authoring staged 实现一并同步到当前 `dev` 主工作树，并完成跨模块编译、测试、race、vet、静态范围与 Trellis 状态复核。

## Validation Commands

```bash
go test -timeout=60s ./internal/gitsync/...
go test -timeout=60s -tags=integration ./internal/gitsync/adapter/postgres
go vet ./internal/gitsync/...
go test -run '^$' ./internal/gitsync/adapter/postgres
rg -n 'gorm\.io/(gorm|driver/postgres)' internal/gitsync --glob '*.go'
rg -n 'github\.com/jackc/pgx/v5' internal/gitsync --glob '*.go' --glob '!*_test.go'
rg -n 'AutoMigrate|\.Migrator\(' internal/gitsync cmd --glob '*.go'
git diff --check
```

## Rollback Points

- GORM implementation is additive and not wired into production; revert only this child task's GORM files and task artifacts.
- Do not revert Foundation or other sessions' changes, modify Schema, or delete legacy pgx code.
- If official GORM/Unit of Work cannot preserve an invariant, stop and return to parent design; do not add fallback or second data path.

## Execution Evidence (2026-08-19)

- Baseline `go test -timeout=60s ./internal/gitsync/...` passed before implementation; Git Sync code had no pre-existing dirty files. Other sessions' changes were left untouched.
- Added a staged implementation only. The legacy pgx `Repository`, `NewRepository`, production Composition, schema and dependencies were not changed.
- `go test -run '^$' ./internal/gitsync/adapter/postgres`, `go test -timeout=60s ./internal/gitsync/...`, `go vet ./internal/gitsync/...` and `go test -race -timeout=60s ./internal/gitsync/...` passed after implementation.
- `go test -v -timeout=60s -tags=integration ./internal/gitsync/adapter/postgres` completed with all seven tests skipped because `ZHIXU_TEST_DATABASE_URL` is unset. Those existing tests also construct the legacy pgx repository rather than this staged GORM repository.
- Static checks found 23 interface methods, 11 `UnitOfWork.Within` transaction calls and eight explicit `TableName` models. No direct GORM transaction, `SQLTransaction`, `AutoMigrate`, `Migrator`, `gorm.Model`, Hook or association API is present. Scoped whitespace/conflict checks and `git diff --check` produced no findings.
- `go-review` and `sql-code-review` self-review fixed JSONB bytea inference, the Attempt config fence scan shape and raw PostgreSQL error-cause leakage. Independent `trellis-check` then compared all 23 methods, 11 Unit of Work transactions, eight explicit models and the lock/CAS/lease/error paths against the legacy pgx implementation and migration constraints; it found no additional Critical or Required issue. It fixed two unchecked `database/sql.Rows.Close` results, normalized `goimports` grouping, removed one unused helper and added compile-time assertions for all eight explicit `TableName` mappings.
- Independent verification passed scoped compile, `go test -count=1 -timeout=60s ./internal/gitsync/...`, `go vet ./internal/gitsync/...`, `go test -race -count=1 -timeout=60s ./internal/gitsync/...`, `gofmt -d`, forbidden API/import scans, per-file whitespace checks and Trellis task validation. The pinned `golangci-lint v2.3.0` reported zero `errcheck`/`unused` issues and no formatter diff. Its full `--new` report retains 31 non-blocking observations for complexity, error-text style, wrapping consistency and one embedded-field selector; the repository config explicitly marks this ruleset report-only, and the parity migration did not refactor state machines or change observable legacy error behavior to clear style debt.
- `go mod tidy -diff` is non-clean because concurrently modified dependency files contain broad unrelated checksum churn and a pending SQLite dependency; this child task did not and must not modify `go.mod` or `go.sum`.
- 2026-08-20 已在当前 `dev` 主工作树完成三 child 集成复核；生产 Composition、legacy pgx、Schema 和依赖仍未由本 child 切换或删除。
- 本轮跨模块 `trellis-check` 进一步修正 Config、Run/Attempt 与 Follow-up 事务返回路径：当 callback 已赋值但后续提交或事务包装失败时，公开方法现在统一返回零值而不是未提交结果；修复后已重跑 Git Sync test、race、vet、integration compile-only 与 API/Worker compile 门禁。

### TODO 9 Validation Evidence (2026-08-27)

- Existing `repository_integration_test.go` obtains one `testdb.Require` fixture per test invocation and constructs
  legacy from `Fixture.Pool().DB()` and GORM from that same `platformpostgres.Pool`'s `GORM()`/`UnitOfWork()`;
  both subtests therefore share one migrated PostgreSQL container and pool.
- `go test -mod=vendor -tags=integration -race -count=1 -p 1 ./internal/gitsync/adapter/postgres -timeout 12m` passed;
  all seven existing PostgreSQL scenarios passed for both `legacy` and `gorm` subtests.
- The suite covers exact config replay/keep fencing, active-run concurrency, trigger and changed-file constraints,
  auto-sync completion/replay, server-event redaction, clear-secret fencing, lease expiry/reclaim, Poison and
  result-unknown convergence, Run/Attempt transitions, Outbox claim/publish and independent index retry/failure.
- The integration fixture also asserts `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)` for the Run keyset index,
  auto-sync writeback candidate index and Outbox pending `SKIP LOCKED` path.
- `go vet -mod=vendor ./internal/gitsync/adapter/postgres`, `go test -mod=vendor -count=1 ./internal/gitsync/...`,
  integration compile-only and `python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-gitsync-migration`
  passed; `git diff --check` is clean. No production Composition, `cmd/**`, migration, dependency or legacy code changed.
