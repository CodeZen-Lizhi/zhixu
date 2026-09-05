# Model Settings SQL/Schema 研究

## Schema owners

`00064` owns revisions/state/runtime; `00078` owns chat API style; `00079` owns hot activation participant/runtime gate/workflow claim fence; `00080` owns Managed Ollama lifecycle and least-privilege command functions; `00065/00066` guard durable revision provenance. GORM does not alter migrations.

## Transaction facts

- SaveDesired locks state, inserts immutable revision, advances desired, verifies persisted data and appends redacted Audit in one transaction.
- Activation uses State -> Runtime ordered by role -> Participant ordered by role. Start/Fail/Finalize/Recover also compose Local Runtime operation/hold facts in the same transaction.
- Workflow enqueue admission reads state `FOR SHARE` in the same transaction as River insert.
- Snapshot is RepeatableRead+ReadOnly. Lease/freshness/heartbeat/recovery use `clock_timestamp()`.

## SQL review requirements

Use parameterized GORM Raw/Exec with explicit UUID/interval/JSON casts, `RETURNING`, `FOR UPDATE/FOR SHARE` and complete CAS predicates. Guard Row/Rows, recognize `sql.ErrNoRows`, retain `*pgconn.PgError` causes, never log SQL args or credential envelopes. No AutoMigrate, association, implicit time, dynamic identifiers or N+1.
