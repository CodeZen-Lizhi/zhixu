# Tools Go API And Wiring Research

## Scope

Read-only inventory covered `internal/tools/application`, `internal/tools/adapter/postgres`, direct production
constructors, scoped collaborators, and existing tests. No product code was changed during research.

## Application Surface

The legacy PostgreSQL Repository implements these behavior groups:

| Group | Methods / responsibility |
| --- | --- |
| Policy | Workflow/tool admission and fail-closed policy reads |
| Ordinary calls | `RecordRefused`, `StartCall`, `FinalizeCall`, `MarkUnknown` |
| Recovery/list | `RecoverStaleStarted`, `ListTimeline` |
| Trusted Write | `StartTrustedWriteCall`, `LoadTrustedWriteCall` |
| Result facts | successful receipt, receipt failure, durable receipt lookup |
| WA operation | authorize, reconcile/replacement, FAILED/UNKNOWN finalization |
| WA refusal | deterministic pre-executor refusal with Audit; no Server Event |
| WA authority | synthesis, search/publication and citation authority projections |

The public Application contracts do not expose pgx. The pgx leak is confined to the legacy Adapter and its
transaction-owned helpers. A staged sibling can therefore preserve all existing Ports without changing callers.

## Scoped Dependencies Already Available

- Foundation: `UnitOfWork` and opaque `TransactionScope`.
- Platform PostgreSQL: one Pool supplies GORM root, UoW and scope unwrapping.
- Events: `ScopedAppender.AppendScoped`.
- Audit: `Recorder.RecordScoped`.
- Workflow: the Workspace Analysis execution fence exists; raw Tool policy snapshot and SKIP LOCKED recovery scoped
  Ports are missing. The policy snapshot must not enforce admission itself, so replay never rechecks a changed lease.
- Agent: existing scoped contracts do not expose Tool operation/budget settlement, durable closure verification,
  refusal persistence/exact load or the authority projections required by Tools.

The parent design requires cross-module transaction participants to use stable Application Ports. Therefore the
legacy direct SQL against Workflow/Agent tables is only a behavioral baseline; copying it into Tools GORM would
violate ownership. Missing concrete Ports must be delivered in the Workflow and Agent module tasks first.

Foundation rejects nil, foreign concrete type and expired scope. It cannot distinguish two active scopes created by
different platform Pools. The GORM fixture and Final Composition must construct all collaborators from one Pool.

## Production Wiring

Current production remains legacy:

- Worker basic Tools repository: `cmd/worker/main.go` constructs `toolpostgres.NewRepository(...)`.
- Worker Workspace Analysis path constructs the legacy repository with Events and Audit dependencies.
- API/Worker composition exposes no Tools GORM constructor.

The child must not modify these sites. Final owns constructor replacement, dependency ordering and legacy deletion.

## Recommended Staged Structure

- Core: root/UoW/readiness/transaction stage/error helpers.
- Mapping: explicit records and JSONB/bytea/null/time/version scanners.
- Ordinary behavior: policy, calls, recovery, timeline, Trusted Write.
- Workspace Analysis: operation authorization/terminal closure, receipts, events, refusal, authority reads, all
  composed through Workflow/Agent scoped Ports.

Legacy constructor behavior remains untouched: the basic repository permits Workspace Analysis without Events, the
Events constructor appends requested/completed facts, and deterministic refusal needs the full Audit constructor.
The staged design instead separates ordinary and full Workspace Analysis repositories; the full staged constructor
requires Workflow/Agent/Event/Audit collaborators, so it cannot create new no-event WA facts.

## Compatibility Risks

1. Splitting operation Event or refusal Audit from the outer Tools transaction creates durable partial state.
2. Treating a same-attempt STARTED call as stale replacement changes reconcile semantics.
3. Generic recovery can corrupt Trusted Write calls unless the existing exclusion remains.
4. GORM `WithContext(nil)` silently loses caller cancellation; every entry needs an explicit context guard.
5. A callback-success/commit-error path must be distinguishable from a transaction-body failure.
6. Raw `[]byte` JSONB binding may become `bytea`; receipt `bytea` must not be converted into JSONB.
7. Copying `workflow.run/node_*` or `agent.workspace_analysis_*` SQL into Tools GORM violates parent ownership and
   makes one-module rollback impossible.

## Conclusion

Tools remains a distinct module task, but product implementation is blocked on Workflow policy/recovery and Agent
participant/refusal/authority Ports. After those owner tasks deliver the prerequisites, Tools can stage its sibling
without changing production; TODO 9 still supplies the real database proof.
