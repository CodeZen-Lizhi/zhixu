# TODO 9 Real PostgreSQL Screening

Date: 2026-08-27

## Decision

Keep this child `in_progress`. The shared Testcontainers factory is available,
but Review Core does not satisfy the parent task's strict rule that the only
remaining work be wiring its existing integration fixture to
`internal/platform/testdb` and executing it.

## Verified Existing State

- `internal/review/adapter/postgres/gorm_*.go` supplies the staged
  `GORMRepository`; its static validation records the required
  `NewGORMRepository(*platformpostgres.Pool)` and Foundation Unit of Work
  boundary.
- `cmd/api/main.go` remains on legacy `NewRepository`, as required before
  Final.
- `repository_integration_test.go` has ten legacy PostgreSQL scenarios for
  Answer/Schedule atomicity, idempotency, Review/Interview shell handling,
  Card ABA, schedule races, quarantine/high-conflict projections, due limits,
  malformed evidence and claim invalidation.
- Its old `newReviewTestDatabase` owns an external-admin URL, migration runner,
  generated database and cleanup. It has not been replaced in this screening.

## Blocking Gaps

The existing integration test file contains no real-PostgreSQL coverage for:

1. `Repository.InvalidateCards` through all five static selectors, its
   `batch+1`/200 cap and `has_more` summary;
2. due and invalidation `EXPLAIN (ANALYZE, BUFFERS)` evidence for the expected
   indexes, bounded statement count and no N+1 behavior;
3. GORM cancel/deadline and `sql.ErrTxDone` classification, including Rows
   closure/connection-release behavior.

The old response-loss harness wraps pgx `Begin`; it does not exercise the
GORM `UnitOfWork.Within` path. Supplying the required GORM harness would be
new test behavior, not a fixture-only conversion.

## Consequence

Do not mark any PRD acceptance criterion complete, do not archive the task and
do not change production composition. A later scoped task must add the missing
Review Core real-PG gate coverage before converting the fixture into paired
legacy/GORM Testcontainers execution.
