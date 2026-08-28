# Review Core Staged GORM Contract

This compact task-local contract supplements the large shared backend specs that
Trellis truncates during context injection. The shared specs remain authoritative.

## Boundary

- Only add staged files under `internal/review/adapter/postgres/gorm_*.go`.
- Keep legacy `Repository`, migrations, tests, Application/Domain ports and
  `cmd/api` production wiring unchanged until TODO 9 and Final.
- Construct GORM root and Unit of Work from one `platformpostgres.Pool`; never
  call `gorm.Open`, root `Transaction`, `AutoMigrate` or `Migrator` here.
- Do not expose GORM, `database/sql`, pgx or `any` through public ports.

## Transaction And SQL

- Every write starts with `core.workspace FOR UPDATE` and uses one Foundation
  Unit of Work. A scoped transaction never escapes its callback.
- Query exact command/answer replay before mutable state. On an ambiguous commit,
  only an exact root receipt/answer lookup may prove success.
- Preserve the legacy order for SubmitAnswer: Workspace, Answer replay, Session,
  Card, Evidence `FOR SHARE`, Schedule `FOR UPDATE`, Answer insert, Schedule CAS.
- Keep Answer, Schedule and receipt in one transaction. Preserve all card/deck/
  session CAS predicates and existing database trigger ownership.
- Use fixed parameterized Raw SQL for due, evidence and invalidation. JSONB binds
  through a validated string `driver.Valuer`; arrays use a single `pq.Array` bind.
- Invalidation keeps five static indexed selectors, `batch+1`, stable order and
  `FOR UPDATE`; no `SKIP LOCKED`, JSON full scan, row loop or N+1.

## Error, Context And Resources

- Every public path rejects nil context; Raw Row/Rows/Exec always calls
  `WithContext(ctx)`.
- Preserve existing Foundation errors and underlying causes, including custom
  context cause, `sql.ErrTxDone` and `*pgconn.PgError` SQLSTATE/constraint data.
- Map no-row at each call site to the legacy NotFound, replay miss or conflict.
- Rows paths check statement errors, nil handles, `Rows.Err` and close results;
  never return partial results after a scan/iteration failure.
- Errors and evidence must not log SQL arguments, Answer, Score/Feedback, receipt,
  DSN, secret or absolute path data.

## Verification Gate

- Static stage requires Review tests/race/vet, integration and API compile-only,
  module/vendor checks, Trellis validate, formatting/diff checks and independent
  Go/SQL/Trellis review.
- Static checks do not prove the staged GORM path executes against PostgreSQL.
  Until TODO 9 runs against migrated disposable databases, leave PRD AC unchecked,
  task status `in_progress`, production on pgx and the child unarchived.
