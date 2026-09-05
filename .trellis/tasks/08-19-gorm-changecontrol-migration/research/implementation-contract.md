# Change Control Staged Implementation Contract

## Hard Boundaries

1. One `platformpostgres.Pool` owns GORM root, UoW, Event store and Workflow scoped runtime dependencies.
2. Legacy pgx types and production wiring stay intact until TODO 9 and Final.
3. Domain/Application receive no new `gorm`, `database/sql`, `pgx` or `any` transaction contract.
4. Every cross-owner write uses the same live `foundation.TransactionScope`; collaborators never start/commit/rollback a nested transaction.
5. Audit remains the existing Application/Tools boundary; no duplicate adapter-level audit event is introduced.

## Transaction Invariants

- Proposal create remains exact idempotent and append-only.
- Approve/AppendRevision/rejected dispatch Event appends are atomic with their business state.
- AppendRevision locks Authorization before Proposal and verifies the authorization set before revoke/append.
- BeginWriteback sorts and locks both Authorization rows before Proposal/Workflow/Execution, then consumes both or neither.
- Publish writes Commit, Outbox, Execution and Proposal state in one UoW.
- Approval Dispatch approved writes Approval, dispatch binding, Workflow facts/River and Proposal state in one UoW; rejected writes no Workflow/River facts.
- Commit ambiguity is never guessed from partial state; replay requires the existing complete durable binding.

## Data And Security Invariants

- Explicit columns only; no Schema mutation or implicit GORM timestamps/soft delete/associations.
- JSONB is validated and bound as string; arrays use a single Valuer parameter.
- Credential plaintext is only hashed in the Begin call stack; token hash, content, absolute path, JSON payload and lock tokens do not enter logs/errors.
- Database time controls authorization/lease/lifecycle facts.
- Workspace, Proposal, Revision, Approval, Workflow and external owner bindings are revalidated after scan.

## Completion Gate

Static compilation/review proves only staged shape. Under the 2026-09-01 parent
lean-test policy, child completion additionally requires a real PostgreSQL GORM
main path and one representative transaction/idempotency/cross-owner atomicity
scenario. The former full parity matrix for triggers, SQLSTATE, DB time, broad
lock competition, response-loss and EXPLAIN remains a Final or direct-risk gate,
not a default child blocker. Completion never switches production: Composition
remains legacy until Final.
