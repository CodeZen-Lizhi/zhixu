# Review Interview Staged GORM Implementation Contract

## Hard Boundaries

1. Add a sibling `GORMRepository`; do not replace legacy `Repository`, ports, tests or production constructors.
2. Constructor accepts one `*platformpostgres.Pool` and derives both GORM root and UoW from it.
3. Every multi-statement write runs in one `UnitOfWork.Within`; scoped DB never escapes and no helper opens a nested transaction.
4. Continue reading/writing `learning.interview_learning_path(_step)` compatibility views, never the shared base table.
5. No AutoMigrate, ORM association/cascade, implicit timestamps, soft delete, dynamic identifiers or sensitive SQL logging.

## Lock And Replay Rules

- Preserve each flow's exact lock order; do not invent one generic lock helper that reorders Submit, Completion, Path or maintenance.
- Check exact receipt before mutable-state rejection.
- Keep Workspace predicates on every aggregate/receipt/path query.
- Preserve all version/attempt/digest predicates and require exact RowsAffected/RETURNING cardinality.
- Start, Submit, Complete and Path commands recover ambiguous commit outcomes only through root durable receipt lookup.
- Maintenance remains one bounded CTE with `FOR UPDATE SKIP LOCKED` and does not lock Session.

## Completion Rules

- Begin/Prepare/Complete remain three durable stages.
- REPORT and PATH holds must both be ACTIVE, match workspace/session/reservation attempt/digest/artifact and be released exactly once in the terminal transaction.
- Complete writes Report, INTERVIEW Path/Steps, Review shell/Interview terminal state, receipt and reservation atomically.
- Never infer success from a partial row; only a strict durable receipt plus full binding can replay success.

## Read And Carrier Rules

- Use `Row().Scan` for one-row miss semantics; use `Rows()` with Close/Err for collections.
- Use `pq.Array` for uuid/text arrays and a JSON `driver.Valuer` returning string for JSONB.
- Preserve keyset order, explicit columns, bounded limits and set-based Question selection.
- Scan then run the existing strict codec, ID/status/version/time/digest and Domain validation; no partial aggregate returns.

## Context And Error Rules

- Reject nil context before `WithContext`.
- Preserve foundation errors and both context sentinel/custom cause.
- Recognize `sql.ErrNoRows`, `gorm.ErrRecordNotFound`, `sql.ErrTxDone` and `*pgconn.PgError` without losing the original cause.
- Let the call site map no-row to miss/not-found/conflict; unknown DB failures are sanitized dependency errors.

## Completion Gate

Static compile/test/review evidence is insufficient for completion. Until TODO 9 runs the legacy/GORM suite against separately migrated disposable PostgreSQL databases, the child remains `in_progress`, PRD AC remain unchecked, production stays pgx, and legacy files remain available for rollback.
