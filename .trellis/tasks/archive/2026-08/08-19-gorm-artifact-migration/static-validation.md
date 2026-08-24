# Artifact GORM Migration Validation

## Result

- Date: 2026-08-24
- Result: staged Artifact GORM implementation and TODO 9 PostgreSQL gate passed.
- Production state: unchanged; API and Worker still construct the legacy pgx repositories.
- Schema state: unchanged; no migration, DDL, shadow read, or dual write was added.
- Review state: final Go review found no remaining P0-P2; SQL review and Trellis check findings were fixed and revalidated.

## Real PostgreSQL Fixture

- Server: PostgreSQL `18.4 (Debian 18.4-1.pgdg12+1)`.
- The base URL was assembled from local environment variables and was never written to this task or command output.
- Every integration test creates a unique database, runs the repository migration runner, and force-drops the database in `t.Cleanup`.
- The GORM variant migrates through a migration-only pool, closes it, then opens exactly one `platformpostgres.Pool` with max/min connections `4/1` for GORM, `database/sql`, and scoped transactions.
- The long-lived local PostgreSQL container was not modified. The review-only disposable container was stopped and auto-removed.

Final real-database commands:

```bash
go test -mod=vendor -tags=integration ./internal/artifact/adapter/postgres \
  -run '^TestGORMSectionGenerationPostgreSQLStartContextFinalizeAndLookup$' \
  -count=1 -timeout 120s -v
# PASS; package 3.209s, test 2.42s

go test -mod=vendor -tags=integration ./internal/artifact/adapter/postgres \
  -count=1 -timeout 360s
# PASS; 58.963s
```

The full package run executes the legacy and GORM fixtures against freshly migrated databases. It covers:

- Repository create/transition/replay, Workspace isolation, visibility holds, external reservation contention and exact deletion.
- Revision v1/v2 mapping, citation selector array binding, exact document-source trigger projection, and malformed/missing projection fail-closed behavior.
- Citation backfill feature gate, `FOR UPDATE SKIP LOCKED`, bounded batches, static SAVEPOINT failure (`23514`), durable FAILED marker, resume, completion, cancellation cause, and connection release.
- Generation start/replay/load/lookup/finalize, current/source rebase, reverse completion order, evidence verification outside row locks, Workflow graph/input drift, terminal outcomes, outer rollback, and start/finalize response loss.
- Context cancellation/deadline classification for Generation Load/Lookup/Finalize, including custom `context.Cause`, retryability, and stable Artifact error codes.
- Advisory locks, `FOR UPDATE`, `NOWAIT`, `SKIP LOCKED`, Repeatable Read snapshots, SQLSTATE mapping, rows close/error handling, and pool connection release.

## Static And Compile Gates

All of the following passed after the final context-classification fix:

```bash
go test -mod=vendor ./internal/artifact/... -count=1 -timeout 120s
go test -mod=vendor -race ./internal/artifact/... -count=1 -timeout 120s
go vet -mod=vendor ./internal/artifact/... ./internal/platform/postgres \
  ./internal/workflow/adapter/postgres ./internal/workflow/application
go test -mod=vendor -tags=integration ./internal/artifact/adapter/postgres \
  -run '^$' -count=1 -timeout 120s
go test -mod=vendor ./cmd/api ./cmd/worker -run '^$' -count=1 -timeout 120s
go list -mod=vendor ./internal/artifact/...
go mod verify
git diff --check
```

Additional scans passed:

- `gofmt -d` is empty for all Artifact task files and the required Workflow prerequisite files.
- Artifact `gorm_*.go` has no pgx Tx/Row/Rows/pool/protocol import, `gorm.Open`, root `Transaction`, DDL, `AutoMigrate`, `Migrator`, `Save`, `Preload`, or `Association`.
- The core GORM Repository and backfill do not access Workflow, Agent, or Change Control owner tables.
- `cmd/**` contains no Artifact GORM constructor, so production remains on legacy.
- No staged Artifact GORM code logs DSNs, credentials, Markdown/content payloads, or model output.

`go mod tidy -diff` exits `1` because the shared worktree already contains dependency metadata drift outside the Artifact task. The command was diff-only, its output was not accepted, and this task did not add dependency churn.

## Review Closure

- Trellis check fixed durable Workflow binding validation and response-loss recovery, rows closure, Repository lock order/schema validation, v1 backfill selection, nullable Workflow output scanning, v1 document-source normalization, and realistic SAVEPOINT/River fixtures.
- SQL/transaction review fixed backfill cancellation-cause loss and confirmed parameterization, Workspace predicates, lock order, CAS, SAVEPOINT, SKIP LOCKED, selector batching, and transaction ownership.
- Final Go review fixed Generation UoW cancellation/deadline classification in Load, Lookup, Finalize, preverification, and committed-start recovery. The reviewer then reported no remaining P0-P2.

## Residual Boundaries

- Production switching and legacy deletion belong exclusively to `08-19-gorm-composition-pgx-convergence`.
- The Generation integration fixture uses a no-op `generationScopedEnqueueFence`; it proves the real scoped River insert and transaction boundary, not the Model Settings policy decision. That policy remains owned by Model Settings and Final cross-module composition tests.
- `foundation.TransactionScope` does not identify a foreign active Pool. Constructors require one shared Pool, tests use one shared Pool, and Final must preserve this composition invariant.
- Artifact file export and Change Control proposal creation remain outside the Artifact database transaction by design.
