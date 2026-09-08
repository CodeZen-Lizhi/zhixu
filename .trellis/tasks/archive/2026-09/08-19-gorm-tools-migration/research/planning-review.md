# Tools GORM Planning Review

## Review Scope

Independent Go/architecture and SQL/transaction reviews compared the planning artifacts with the parent GORM design,
Tools legacy code, current Workflow/Agent scoped contracts, migrations and integration fixtures. No product code was
changed.

## Findings And Resolution

### P1: Commit Response-Loss Contract

Initial planning incorrectly deferred exact recovery to a later caller request. Legacy Tools performs a fresh
transaction in the same call after a commit error and returns canonical replay when the complete durable closure is
proven. The PRD/design/implementation/TODO 9 fixture now require the same behavior for ordinary calls, Trusted Write,
WA authorization/finalization, receipt/failure and refusal. Unproven closure returns the existing commit/unknown error.

### P1: TODO 9 Ownership

Initial planning placed GORM Worker and cross-module integration parameterization inside the Tools child while also
forbidding `cmd/**` and cross-owner test changes. TODO 9 is now an independently authorized shared real-database task.
Tools only records the executable fixture/scenario contract and remains incomplete until evidence is written back.
Worker GORM composition and its integration variant remain Final work.

### P1: Cross-Owner SQL And Missing Ports

The legacy Tools adapter directly queries/locks Workflow facts and mutates Agent Workspace Analysis facts. The parent
GORM contract requires stable Application Ports for cross-module transaction participants; copying those SQL blocks
would violate ownership and one-module rollback.

Current prerequisite state:

- available: Workflow `ScopedWorkspaceAnalysisExecutionFence`, Events `ScopedAppender`, Audit `RecordScoped`;
- missing in Workflow: raw scoped Tool policy snapshot and SKIP LOCKED recovery fence;
- missing in Agent: scoped Tool participant (operation/budget), durable closure verifier, refusal store/exact load and
  authority projection reader.

The Tools task now remains `planning` and product implementation is blocked. Required Ports must be delivered in the
Workflow and Agent module tasks, then Tools consumes them under one outer UoW. Tools GORM has an explicit static ban
on `workflow.run/node_*` and `agent.workspace_analysis_*` SQL; Tool-owned `workflow.tool_call` and receipt tables remain
allowed.

### P1: Admission And Replay Were Coupled

Initial planning reused the live Workflow admission fence during commit response-loss recovery. That could reject an
already committed operation when the current lease, deadline or cancellation state changed before the recovery read.
Owner contracts now separate the two concerns: Workflow returns a raw immutable snapshot and Tools validates live
lease/cancel/deadline only before the first mutation. Ordinary recovery reads only Tool-owned durable facts; WA
recovery verifies immutable Workflow binding plus Agent durable closure/refusal facts and never re-runs live admission.

### P2: Response-Loss Fault Injection

The public constructor still accepts only `*platformpostgres.Pool`. A same-package TODO 9 test may decorate the
repository's private `foundation.UnitOfWork` interface field so real `Within` commits and then returns one injected
error. This tests immediate recovery without adding a public weak transaction abstraction.

### P2: Baseline Corrections

- deterministic Workspace Analysis refusal writes refusal + Audit only; it emits no Server Event;
- requested/completed Events belong to WA operation/receipt paths;
- authority multi-statement reads use legacy default transaction options, not RR/ReadOnly;
- Timeline order is `started_at,call_no,id`;
- fixed Search/Read authority fan-out is bounded to at most three receipts and is not an unbounded N+1.

## Final Planning Status

No unresolved design defect remains inside the Tools-owned boundary. Implementation is intentionally blocked on the
listed Workflow/Agent owner contracts and TODO 9 remains an external completion gate. User approval is still required
before any task activation or owner-module follow-up implementation.
