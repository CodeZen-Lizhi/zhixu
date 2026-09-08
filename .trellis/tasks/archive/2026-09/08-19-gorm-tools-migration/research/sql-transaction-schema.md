# Tools SQL, Transaction And Schema Research

## Data Facts

The relevant migrated schema is already authoritative:

- `workflow.tool_call`: identity, lifecycle, active-call uniqueness, idempotency and recovery indexes.
- `workflow.tool_result_receipt`: immutable successful output/private binding fact.
- `workflow.tool_result_receipt_failure`: immutable observation-only rejection fact.
- `agent.workspace_analysis_run`: lifecycle, deadline, budget totals and version.
- `agent.workspace_analysis_operation`: logical operation slot, tool/model binding and state transition.
- `agent.workspace_analysis_budget_reservation`: one reservation per operation and settlement closure.
- `agent.workspace_analysis_tool_refusal`: deterministic refusal slot and immutable audit binding.

No migration is required by this ORM child. Existing triggers and deferred constraints remain the final guard for
immutable binding, valid status transitions, version increments, budget closure and cross-table consistency.

## Owner Boundary

- Tools GORM owns SQL for `workflow.tool_call` and result receipt/failure facts.
- Workflow scoped Ports own Definition/Run/Node/Attempt policy and locks.
- Agent scoped Ports own Analysis Run/Operation/Reservation/Refusal/Candidate SQL.
- Tools owns the outer UoW and invokes participants in order; no participant commits or rolls back.

The existing Workflow WA execution fence is usable. Raw Tool policy snapshot/recovery and Agent Tool participant/
durable closure/refusal/authority Ports are missing and must be delivered by their owner tasks before Tools
implementation starts. Commit recovery uses exact durable readers and never re-runs live lease/cancel/deadline
admission. Direct
cross-owner SQL in the legacy adapter is a baseline only and must not appear in staged GORM files.

## Fixed Lock Order

Workspace Analysis mutation order is:

```text
Workflow scoped fence: run -> node_run -> node_attempt
Agent scoped participant: analysis_run -> operation -> budget_reservation
Tools GORM: tool_call
```

The order applies to authorization, receipt, receipt failure, terminalization and replacement. Reordering call or
operation locks ahead of Workflow facts can introduce deadlocks with cancel/recovery paths.

## Atomic Closures

### Authorization

Within one transaction: Workflow fence -> Analysis Run -> Operation exact create/load -> reconcile/replacement ->
DB clock and budget/concurrency checks -> STARTED Call -> Run reserved-budget CAS -> Operation CAS -> Reservation ->
scoped Event. Any failure rolls back all facts.

### Successful Receipt

Call terminal CAS -> canonical receipt -> reservation settlement -> Run reserved-to-settled CAS -> Operation success
-> scoped Event. Exact replay verifies the complete closure, not only the receipt unique key.

### Failure / Unknown

Receipt Failure stores only observation hash/bytes. FAILED or UNKNOWN closes Call, Reservation, budget and Operation
in the same transaction. UNKNOWN preserves the current full-charge rule.

### Deterministic Refusal

Only refusal + Audit are written; no Server Event is emitted. No Call, Operation, Reservation or budget mutation is
permitted.

## Time, CAS And Recovery

- Lease, deadline, staleness and replacement use database `clock_timestamp()`.
- Every update retains existing owner/lease/status/version predicates and exact RowsAffected/no-row handling.
- stale recovery remains bounded and uses `FOR UPDATE SKIP LOCKED`.
- Trusted Write calls bound to a writeback execution remain excluded from generic stale recovery.
- Result/Refusal/Operation facts are durable response-loss sources. After a commit error, the same call opens a new
  transaction and returns canonical replay only when the complete durable closure is proven; otherwise it returns the
  original commit/unknown error.

## Carrier Rules

- Tool request/response summaries are JSONB objects. The GORM Valuer validates JSON and returns string.
- Receipt output/private binding/candidate are `bytea`; scan copies bytes before Domain validation.
- Receipt Failure never persists the rejected document.
- Nullable tool/schema/capability/idempotency/result/error/time fields use explicit pointer or `sql.Null*` carriers.
- GORM Raw SQL uses `?` bindings and explicit PostgreSQL casts where needed. No external identifier is interpolated.

## Error Matrix

| Input | Required classification |
| --- | --- |
| caller canceled | non-retryable Tools canceled error, preserving sentinel and custom cause |
| deadline exceeded | retryable Tools timeout error, preserving cause |
| `sql.ErrTxDone` | dependency unavailable |
| 40001 / 40P01 / 55P03 | retryable database unavailable |
| 23505 | operation-specific idempotency, receipt or state conflict |
| 23503 / 23514 / 55000 | consistency violation, fail closed |
| no row | method-specific not found/replay/state conflict |
| unknown driver error | safe type in message, original cause retained |

`pgconn.PgError` is allowed only for SQLSTATE classification. GORM data access itself must not use pgx transaction,
row, rows, pool or protocol APIs.

## TODO 9 Database Matrix

- ordinary start/replay/CAS/terminal, policy, timeline and stale recovery;
- Trusted Write recovery exclusion and side-effect idempotency;
- authorization budget/concurrency, reconcile and replacement under two connections;
- all terminal/receipt/refusal fault slices with zero partial state;
- trigger/deferred closure and raw negative SQL cases;
- database time, real SQLSTATE and cancellation/deadline behavior;
- commit-response-loss recovery, Rows close and pool connection release;
- Event/Audit sensitive-data canaries.

Without a migrated PostgreSQL instance these remain unproven and the child cannot complete.
