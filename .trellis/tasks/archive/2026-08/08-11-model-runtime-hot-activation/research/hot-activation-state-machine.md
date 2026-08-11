# Research: Hot Activation State Machine

- Query: Determine the minimal durable state machine, PostgreSQL schema, and application/runtime interface evolution needed for in-process API + Worker prepare/activate/abort/retire without a container restart. Cover CAS, ownership, stale heartbeats, coordinator crash/recovery, process restart during rollout, and compatibility with desired/active/applied and existing rows.
- Scope: internal
- Date: 2026-08-11

## Recommendation

Keep the current three truths, but narrow their meanings:

- `desired_revision` remains the latest saved revision.
- `active_revision` remains the database commit point for the revision selected for new work.
- `ops.model_settings_runtime.applied_revision` remains each role's locally installed serving revision. It must not be overwritten while a candidate is merely preparing.

Add a rollout-participant row per `(rollout_id, role)` for candidate state. Replace the restart-oriented global phases with `idle -> preparing -> arming -> activating -> idle`, with only the pre-commit phases allowed to transition to `failed`. API and Worker keep serving the old generation during `preparing`; `arming` briefly fences only new runtime acquisitions/new Worker claims; `activating` is a forward-only post-commit recovery phase. Existing requests and claimed attempts retain an old-generation lease until completion.

This is the smallest design that preserves a real cross-process cutover boundary. Merely polling `active_revision` and swapping an in-memory pointer has an unavoidable race: a request can acquire the old pointer after the database commit but before one process observes it. The short `arming` fence closes that race without draining running jobs or restarting either process.

## Files Found

- `.trellis/tasks/08-11-model-runtime-hot-activation/prd.md` - accepted no-restart behavior, version invariants, failure behavior, and acceptance criteria.
- `.trellis/spec/backend/model-settings-runtime.md` - current restart-based model-settings runtime contract and migration rules.
- `.trellis/spec/backend/index.md` - backend specification routing.
- `.trellis/workflow.md` - active Trellis workflow and research constraints.
- `internal/modelsettings/domain/rollout.go` - persisted rollout/runtime phases and snapshot types.
- `internal/modelsettings/application/ports.go` - rollout, runtime ownership, settings, and loader ports.
- `internal/modelsettings/application/coordinator.go` - restart-oriented rollout coordinator and lease policy.
- `internal/modelsettings/application/service.go` - validation and store delegation.
- `internal/modelsettings/adapter/postgres/rollout.go` - state-row locking, commit, failure, and expired-rollout recovery.
- `internal/modelsettings/adapter/postgres/runtime.go` - role registration, heartbeat, phase changes, and enqueue fence.
- `internal/modelsettings/adapter/postgres/snapshot.go` - desired/active/applied read model and capability projection.
- `internal/modelsettings/runtime/loader.go` - startup-only active/candidate loading.
- `internal/modelsettings/runtime/controller.go` - one-runtime-per-process reconciliation.
- `internal/modelsettings/runtime/models.go` - immutable model bundle construction and connection probes.
- `internal/platform/models/runtime.go` - immutable Chat + Embedding runtime.
- `internal/platform/models/model_transport.go` - per-runtime HTTP transport construction.
- `internal/workflow/adapter/postgres/runtime_state.go` - Worker claim authorization and immutable attempt binding.
- `internal/workflow/adapter/river/runtime_worker.go` - process-lifetime frozen model binding and executor registry.
- `cmd/api/main.go` - API startup composition captures one model runtime.
- `cmd/api/model_runtime_gate.go` - current API drain gate.
- `cmd/worker/main.go` - Worker startup composition captures one model runtime and embedding contract.
- `cmd/worker/model_runtime_drain.go` - current queue/producer drain waits for all running jobs.
- `cmd/modelctl/control.go` - restart rollout command/session orchestration.
- `cmd/modelctl/preflight.go` - current target probes.
- `migrations/00064_model_settings.sql` - revision/state/runtime schema and guards.
- `migrations/00065_model_settings_workflow_provenance.sql` - immutable attempt model-runtime provenance.
- `migrations/00066_model_settings_execution_provenance.sql` - embedding/index/model-run revision provenance.
- `migrations/00067_workspace_root_grant.sql` - shared runtime mutation gate keyed to old rollout phases.
- `migrations/00068_capture_profile.sql` - downstream invalidation on active revision change.
- `migrations/00078_model_settings_chat_api_style.sql` - latest additive model-settings evolution.
- `migrations/embed.go` - all versioned SQL migrations are embedded automatically.
- `internal/modelsettings/application/coordinator_test.go` - current coordinator order, lease, freshness, and recovery tests.
- `internal/modelsettings/runtime/controller_test.go` - current old/candidate process-controller tests.
- `internal/modelsettings/runtime/loader_test.go` - startup active/candidate selection tests.
- `internal/modelsettings/adapter/postgres/repository_integration_test.go` - current database rollout, CAS, fence, and ownership coverage.
- `internal/workflow/adapter/postgres/runtime_state_integration_test.go` - current attempt binding and owner replacement coverage.
- `internal/platform/migration/model_settings_integration_test.go` - migration guard and rollback coverage.

## Current Contract And Blocking Facts

### The current state machine is a process-replacement protocol

- The domain declares `validating`, `draining`, `applying`, and `verifying`; its only legal advances are validating-to-draining-to-applying-to-verifying (`internal/modelsettings/domain/rollout.go:11`, `internal/modelsettings/domain/rollout.go:101`).
- Per-role phases mix active ownership and candidate ownership in one type: `active`, `quiescing`, `quiesced`, `prepared`, `verifying`, and `unavailable` (`internal/modelsettings/domain/rollout.go:31`).
- The coordinator explicitly says its queue controller pauses claims "around process replacement" (`internal/modelsettings/application/coordinator.go:33`). It advances the database before pausing the queue, waits for old Worker quiescence, then waits for replacement runtimes (`internal/modelsettings/application/coordinator.go:243`, `internal/modelsettings/application/coordinator.go:283`, `internal/modelsettings/application/coordinator.go:305`).
- Worker drain stops producers and the queue, then requires the persisted count of running River jobs to reach zero (`cmd/worker/model_runtime_drain.go:25`, `cmd/worker/model_runtime_drain.go:69`, `cmd/worker/model_runtime_drain.go:103`). That directly conflicts with the new requirement that already claimed work continue on the old generation through cutover.

### One row per role cannot represent active plus candidate

- `ops.model_settings_runtime.role` is the primary key, so there is exactly one API row and one Worker row (`migrations/00064_model_settings.sql:228`).
- The same-instance trigger forbids changing `applied_revision`; candidate phases are stored in that same row (`migrations/00064_model_settings.sql:261`, `migrations/00064_model_settings.sql:265`).
- Registration uses `ON CONFLICT(role)` and overwrites instance, revision, rollout, and phase whenever the instance differs (`internal/modelsettings/adapter/postgres/runtime.go:30`). A candidate therefore displaces the old serving owner instead of coexisting with it.
- Registration does not require the existing owner heartbeat to be stale before a different instance takes the role (`internal/modelsettings/adapter/postgres/runtime.go:33`). A duplicate live process can currently steal a role; identity CAS protects later heartbeats but not the takeover itself (`internal/modelsettings/adapter/postgres/runtime.go:61`).

### Commit erases the recovery record too early

- Current commit verifies two prepared rows, changes `active_revision`, immediately clears rollout fields, sets global phase to `idle`, and promotes candidate rows in one transaction (`internal/modelsettings/adapter/postgres/rollout.go:151`, `internal/modelsettings/adapter/postgres/rollout.go:175`, `internal/modelsettings/adapter/postgres/rollout.go:178`).
- There is no durable phase between database publication and confirmation that both in-process pointers have switched. A coordinator crash after database commit cannot distinguish "both activated" from "one process still old."
- Expired recovery treats every active phase as pre-commit and fails it (`internal/modelsettings/adapter/postgres/rollout.go:201`, `internal/modelsettings/adapter/postgres/rollout.go:219`). That is unsafe after `active_revision` has moved, because one role may already have accepted target-revision work.

### Runtime composition is process-lifetime, not generation-lifetime

- The loader chooses exactly one revision: ordinary managed startup always loads `active`; a candidate requires a rollout ID and prepared startup phase (`internal/modelsettings/runtime/loader.go:27`, `internal/modelsettings/runtime/loader.go:43`, `internal/modelsettings/runtime/loader.go:46`).
- The controller owns one loaded revision/rollout/phase and registers it once before heartbeat/reconciliation (`internal/modelsettings/runtime/controller.go:34`, `internal/modelsettings/runtime/controller.go:95`).
- `Models` is explicitly the single frozen process runtime, and `Build` constructs Chat and Embedding together (`internal/modelsettings/runtime/models.go:28`, `internal/modelsettings/runtime/models.go:89`). This joint construction is useful: candidate prepare can preserve atomic Chat + Embedding activation.
- API and Worker startup wire concrete model-dependent services once. The Worker additionally freezes revision and instance for its entire lifetime (`internal/workflow/adapter/river/runtime_worker.go:34`, `internal/workflow/adapter/river/runtime_worker.go:46`, `internal/workflow/adapter/river/runtime_worker.go:62`). Swapping only `*Models` would leave executor registries, embedding versions, and services bound to the previous generation.

### Existing provenance is valuable and must remain authoritative

- A workflow attempt persists an immutable pair of model settings revision and runtime instance (`migrations/00065_model_settings_workflow_provenance.sql:3`, `migrations/00065_model_settings_workflow_provenance.sql:25`).
- New attempt insertion currently must match the singleton active Worker row (`migrations/00065_model_settings_workflow_provenance.sql:44`, `migrations/00065_model_settings_workflow_provenance.sql:61`). Updates deliberately do not re-check the current owner, so an old running attempt can complete after ownership changes.
- Embedding version, index version, and model run already carry model-settings revision provenance; index revision must match its embedding version and model-run revision must match the attempt (`migrations/00066_model_settings_execution_provenance.sql:3`, `migrations/00066_model_settings_execution_provenance.sql:49`, `migrations/00066_model_settings_execution_provenance.sql:63`). These facts should be reused, not replaced by a mutable "current embedding" pointer.
- Claim currently authorizes the process-frozen command binding before it reads an exact existing delivery (`internal/workflow/adapter/postgres/runtime_state.go:30`, `internal/workflow/adapter/postgres/runtime_state.go:40`, `internal/workflow/adapter/postgres/runtime_state.go:76`). Exact replay also requires the command pair to equal the persisted pair (`internal/workflow/adapter/postgres/runtime_state.go:79`). The integration test confirms a replacement owner cannot replay the old pair (`internal/workflow/adapter/postgres/runtime_state_integration_test.go:224`). Hot activation therefore needs a targeted replay/claim contract change.

## Minimal Durable State Machine

### Global rollout phases

| Phase | Commit side | Durable meaning | Admission behavior | Allowed next phases |
| --- | --- | --- | --- | --- |
| `idle` | none | No rollout; `active` is the revision for new work | Open if local applied equals active | `preparing` |
| `preparing` | pre-commit | Target is fixed; both roles may build/probe a candidate while old remains active | Open on old generation | `arming`, `failed` |
| `arming` | pre-commit | Both candidates were prepared; each role must close only new model-runtime acquisitions/new Worker claims and acknowledge `armed` | New model work retryably fenced; old holders continue | `activating`, `failed` |
| `activating` | post-commit | `active_revision == target_revision`; roles swap their local default, persist applied target, and acknowledge activation | Keep new model work fenced until both roles acknowledge | `idle` only |
| `failed` | pre-commit terminal | Target was not committed; `active_revision == previous_active_revision`; candidate cleanup may continue | Open on old generation | `preparing` for a new rollout |

Required invariants:

1. `target_revision` and `previous_active_revision` are immutable from begin through finalization/failure.
2. `active_revision == previous_active_revision` in `preparing`, `arming`, and `failed`.
3. `active_revision == target_revision` in `activating`.
4. Only `arming -> activating` may change `active_revision`.
5. `activating` has no transition to `failed` or back to the previous revision. Recovery after the commit is forward-only; rollback is a later rollout whose target is the old revision.
6. Saving desired remains disallowed while a rollout is live, preserving the current fixed-target rule (`migrations/00064_model_settings.sql:175`, `migrations/00064_model_settings.sql:178`). Begin additionally takes explicit target/expected desired and state version, so a stale UI cannot accidentally apply a newer save.
7. The shared Workspace/model-settings mutation gate stays held in `preparing`, `arming`, and `activating`, then releases on `idle`/`failed`. Its trigger currently hard-codes old active phases and must be replaced in the forward migration (`migrations/00067_workspace_root_grant.sql:155`, `migrations/00067_workspace_root_grant.sql:162`, `migrations/00067_workspace_root_grant.sql:175`).

`desired != active` with `idle` or `failed` is the durable "pending apply" state; it does not need another persisted phase.

### Per-role participant phases

Use a new row keyed by `(rollout_id, role)`:

| Participant phase | Meaning |
| --- | --- |
| `preparing` | Current process owner accepted the target and is constructing the full generation bundle. |
| `prepared` | Full Chat + Embedding generation and generation-scoped dependencies passed validation/preflight; old generation still serves. |
| `armed` | The current role owner fenced new acquisition/claim and still holds the candidate. |
| `activated` | Local default points at target and the role's applied revision was persisted. |
| `failed` | Pre-commit construction/preflight failed with a stable, redacted error code. |
| `aborted` | Candidate was discarded after a pre-commit abort/failure. |
| `retired` | Target remains active, and the participant has also released the previous generation after its local holder count reached zero. Observational; it must not block rollout finalization. |

Normal transition is `preparing -> prepared -> armed -> activated -> retired`. `preparing|prepared|armed -> failed|aborted` is allowed only while the global rollout is pre-commit. A replacement process may replace a stale participant binding and restart from `preparing`; in post-commit `activating`, a process that loaded the already-active target may replace the stale binding directly as `activated`.

Do not reuse `ops.model_settings_runtime.phase` for candidates. That row should continue to describe only the current role/process owner and its installed serving revision.

## Minimal Schema Evolution

Implement a new forward migration after `00078`; published `00064`-`00078` files remain immutable. This follows the current spec's forward-migration and shape-detection rule (`.trellis/spec/backend/model-settings-runtime.md:46`) and the repository's embedded migration source (`migrations/embed.go:6`).

### `ops.model_settings_state`

Retain all existing columns and `version`; replace the phase/shape and transition guard to support the five phases above. No separate coordinator-owner column is required for the minimal design: the unique `rollout_id` remains the lease token, while `version` becomes an explicit expected-version CAS on every state mutation. A resumed coordinator reads the current token/version; a stale coordinator's next command fails even if its phase expectation happens to match.

Add an optional `committed_at` only if audit/UI needs a stable commit timestamp; phase plus `active_revision == target_revision` is sufficient for correctness. Keep rollout binding and lease populated throughout `activating`; clear them only on finalization. Keep a stable `last_error_code` only for pre-commit `failed`; participant error codes provide role-specific diagnostics.

### `ops.model_settings_runtime`

Reuse the existing one-row-per-role table as the process ownership/serving projection:

- Preserve `role`, `instance_id`, `applied_revision`, `applied_at`, and `heartbeat_at` so existing rows and HTTP applied summaries remain compatible.
- New hot-rollout code keeps `rollout_id` null and uses `active|unavailable`; legacy candidate phases can remain in the check constraint for migration compatibility but are not written by the new path.
- Replace the trigger clause that forbids same-instance `applied_revision` changes (`migrations/00064_model_settings.sql:261`). Permit an applied change only through the participant activation transaction when state is `activating`, target matches, participant is `armed`, and role/instance ownership matches.
- Fix registration takeover: same-instance refresh is allowed; a different instance may replace the row only when the existing `heartbeat_at` is older than a server-owned stale interval, using database time and a locked row. Current unconditional different-instance replacement is unsafe (`internal/modelsettings/adapter/postgres/runtime.go:33`). Once replaced, late old heartbeats fail their existing instance-ID predicate (`internal/modelsettings/adapter/postgres/runtime.go:70`).

Reusing `heartbeat_at` is the minimal schema. An explicit `lease_expires_at` can be added later if operators need per-owner lease duration in SQL, but it is not required if takeover, commit, and snapshots all use one application-owned freshness policy and database time.

### New `ops.model_settings_rollout_participant`

Minimum columns:

```text
rollout_id uuid not null
role text not null check (role in ('api','worker'))
instance_id uuid not null
target_revision bigint not null
phase text not null
heartbeat_at timestamptz not null
last_error_code text null
version bigint not null
prepared_at timestamptz null
activated_at timestamptz null
retired_at timestamptz null
primary key (rollout_id, role)
```

`target_revision` is duplicated intentionally: participant history stays interpretable after the singleton clears its rollout binding. Guard target existence with `ops.model_settings_revision_exists`, including canonical revision 0 (`migrations/00064_model_settings.sql:143`). Use `(rollout_id, role, instance_id, expected_phase, expected_version)` for participant mutation CAS. A nonterminal participant must also match the current process owner row before a transition succeeds.

Do not delete participant rows during finalization. Keep terminal rows for audit/recovery and apply a later retention policy; the PRD still leaves retention duration open. A guarded Down must refuse while participant rows or new live phases exist, matching current history-preserving migration behavior (`migrations/00064_model_settings.sql:290`, `migrations/00065_model_settings_workflow_provenance.sql:77`).

### Migration of existing rows

1. Lock state/runtime tables while replacing guards.
2. Accept existing databases only when the legacy singleton is `idle` or `failed`. If it is `validating|draining|applying|verifying`, fail closed with an actionable error requiring the legacy rollout to be recovered/aborted first. Inferring whether an overwritten role row is old or candidate is not safe.
3. Preserve desired, active, rollout history fields, revision history, and state version. Existing `idle` runtime rows already represent serving owners; no participant backfill is needed.
4. Preserve existing `failed` state and active runtime rows. A new begin overwrites the singleton binding through normal CAS.
5. Replace `ops.sync_model_settings_mutation_gate` because it enumerates legacy phases (`migrations/00067_workspace_root_grant.sql:162`). Repair the gate from the current state as `00067` already does for its phase set (`migrations/00067_workspace_root_grant.sql:198`).
6. Preserve the `active_revision` update event exactly once. The profile-staleness trigger depends on that edge and remains correct if commit performs one state-row active update (`migrations/00068_capture_profile.sql:484`, `migrations/00068_capture_profile.sql:560`).
7. Replace the workflow attempt guard/query only as needed for the serving-row semantics; do not alter existing immutable attempt rows.

This migration is compatible with current data, but not necessarily with a mixed old/new binary rolling deployment. No evidence was found that the project promises mixed-version runtime compatibility; deployment of the feature should still be a normal software upgrade. The no-restart requirement applies to subsequent configuration activation, not installation of new code.

## Interface Evolution

### Domain and application ports

The existing `RolloutState.Version` is already exposed (`internal/modelsettings/domain/rollout.go:52`), but rollout commands only carry ID and expected phase (`internal/modelsettings/application/ports.go:103`). Make version CAS explicit:

- `BeginRolloutCommand`: `RolloutID`, `TargetRevision`, `ExpectedDesiredRevision`, `ExpectedStateVersion`, lease duration.
- `AdvanceRolloutCommand`: rollout ID, expected phase/version, next phase, lease duration.
- `RenewRolloutCommand`: rollout ID, expected phase/version, lease duration.
- `FailRolloutCommand`: rollout ID, expected phase/version, error code; reject in `activating`.
- `CommitActivationCommand`: rollout ID, expected `arming` state version, freshness window; performs only `arming -> activating` and the active-revision update.
- `FinalizeActivationCommand`: rollout ID, expected `activating` version, freshness window; verifies both roles applied/activated and performs `activating -> idle`.

Split process ownership from participant control:

- Keep `RegisterRuntime`/`HeartbeatRuntime` for the serving process row.
- Add `RegisterParticipant`, `HeartbeatParticipant`, `TransitionParticipant`, and `ReplaceStaleParticipant` with rollout/role/instance/target/expected phase/version.
- Add one atomic `AcknowledgeActivation` repository operation that verifies global `activating`, current process ownership, target, and armed participant, then changes the role's `applied_revision` and participant phase together.
- Snapshot adds candidate summaries for API and Worker: target, phase, fresh, safe error code. Keep current applied summaries. Preserve `restartRequired` on the wire as a compatibility alias while introducing `applyRequired`; current computation conflates desired drift, rollout activity, and unhealthy runtime (`internal/modelsettings/adapter/postgres/snapshot.go:65`, `internal/modelsettings/adapter/postgres/snapshot.go:69`).

The normal apply entry point should live in the application service/background coordinator used by the API. `modelctl` can remain an operational recover/inspect tool; its current command set is explicitly the old restart sequence (`cmd/modelctl/control.go:16`).

### In-process generation manager

Introduce a process-owned, generation-aware boundary instead of mutating `Models`:

```text
Prepare(rolloutID, targetRevision) -> candidate generation
Arm(rolloutID)                    -> fence new acquisitions
Activate(rolloutID)               -> atomically swap local default
Abort(rolloutID)                  -> close candidate and reopen old if pre-commit
Acquire(revision?)                -> ref-counted generation lease
Retire(revision)                  -> close only when no holders remain
```

A generation is the complete role-specific dependency bundle, not only `*runtime.Models`. For API it includes handlers/services that captured Chat/Embedding dependencies; for Worker it includes the executor registry, embedding/index contract, model services, and other model-dependent adapters. This follows from the current Worker storing one registry and resolving from it for every execution (`internal/workflow/adapter/river/runtime_worker.go:34`, `internal/workflow/adapter/river/runtime_worker.go:173`).

Preparation must use the same constructors as production and build Chat + Embedding atomically. The existing `Build` path already does so (`internal/modelsettings/runtime/models.go:89`). Run minimal target probes before `prepared`; current connection tester uses production adapters (`internal/modelsettings/runtime/models.go:171`, `internal/modelsettings/runtime/models.go:200`). Destroy the resolved secret object immediately after construction as today; do not persist or log it.

### Request and Worker claim binding

- API model-dependent request middleware acquires the current generation once and holds that lease for the request. A request admitted before arming continues on old; one admitted after finalization gets target.
- Worker does not freeze revision/registry in `RuntimeNodeWorkerOptions`. It closes new Claim admission in `arming`, then after finalization asks PostgreSQL for the attempt's authoritative revision and acquires that generation for the whole execution.
- The admission fence is a reader/writer barrier around the short acquire/Claim-and-acquire critical section. `Arm` waits for those critical sections to either finish or obtain a generation lease; it does not wait for the request or execution itself. This closes the race where a pre-arm Claim commits an old binding but has not yet retained the old generation.
- For a new attempt, Claim derives `model_settings_revision` and current Worker instance from locked state/runtime rows instead of trusting a process-frozen command pair.
- For an exact existing delivery, load the persisted attempt before current-runtime authorization and return its immutable binding as authoritative. This fixes the current ordering/mismatch at `internal/workflow/adapter/postgres/runtime_state.go:40` and `internal/workflow/adapter/postgres/runtime_state.go:76`.
- A newly reclaimed attempt after an expired old lease uses the then-current active revision. The previous attempt remains `lease_lost`, preserving current append-only attempt behavior (`internal/workflow/adapter/postgres/runtime_state.go:120`).

The process instance ID can remain stable across multiple generations. `(instance_id, model_settings_revision)` already distinguishes the generation in attempt provenance. This avoids inventing another identifier and remains compatible with existing rows.

## Transaction And Recovery Protocol

### 1. Begin and prepare

1. Begin locks the singleton, checks expected version, expected desired, no live rollout, and target existence; writes `preparing` and a bounded lease.
2. Each role watcher keeps serving active, inserts/claims its participant row, loads the fixed target, builds its full generation, probes, then CASes `preparing -> prepared`.
3. Process and participant heartbeats use database time. Coordinator considers a role ready only when both its process owner and participant are fresh, participant instance matches process owner, target matches, and phase is `prepared`.

### 2. Arm

1. Coordinator CASes global `preparing -> arming` only after both fresh prepared rows.
2. Each role closes only new model-generation acquisition. Worker also pauses new Claims/producers and waits for the short Claim-and-acquire critical sections to leave the admission barrier, but does not wait for running jobs. Existing generation leases continue.
3. After its local fence is closed and candidate still exists, each role CASes participant `prepared -> armed`.
4. If either role fails or expires before commit, coordinator CASes `arming -> failed`; watchers abort the candidate and reopen old admission.

### 3. Commit and activate

1. Commit transaction locks singleton, process rows ordered by role, and participant rows ordered by role.
2. It verifies rollout token/version/lease, both fresh process owners, both fresh `armed` participants, owner-instance equality, and one fixed target.
3. It performs the sole `active_revision = target_revision` update and changes global phase to `activating`, retaining rollout binding. It does not clear the rollout or claim local activation has happened.
4. Each role observes `activating`, atomically swaps its local default to the prepared generation, then retries `AcknowledgeActivation` until the same transaction updates its serving `applied_revision` and participant to `activated`. The admission fence stays closed on any database error.
5. Finalize locks and verifies both fresh current owners have `applied_revision == target` and matching `activated` participants, then CASes `activating -> idle` and clears rollout fields.
6. Watchers reopen admission only after observing `idle`, local applied equals active, and their process ownership remains valid. Thus no new work can use old after the active commit.

### 4. Retire

The old generation becomes non-default at local activation, but remains addressable by revision. Every request/execution holds a reference-counted lease. API retirement needs only the local holder count. Worker retirement additionally queries PostgreSQL for nonterminal attempts bound to `(model_runtime_instance_id, model_settings_revision)`; retire only when that durable count and the local holder count are both zero. Then close adapters/transports, release references, and optionally CAS participant `activated -> retired`.

The Worker count check and local retirement decision must run under the same generation-manager lock that protects `Acquire`. A running old attempt is the durable "must continue" fact; after a process crash, its old instance lease eventually becomes `lease_lost`, and a replacement attempt may bind current active. This is narrower than the current all-jobs drain and preserves per-attempt semantics.

Retirement is process-local correctness, not a global commit prerequisite. A process crash destroys all of its memory/resources; a replacement process loads only persisted active plus any revision needed by a recovered attempt. Blocking global `idle` or the next rollout on a long old task would reintroduce drain semantics.

## Failure Matrix

| Failure point | Durable state | Required recovery |
| --- | --- | --- |
| Candidate build/probe fails | `preparing`, participant `failed` | CAS global to `failed`; active/applied stay old; discard candidate. |
| Coordinator dies in `preparing` or `arming` | Active still old | On lease expiry, recovery CASes to `failed`; armed roles reopen old admission. |
| Role heartbeat becomes stale before commit | Pre-commit | Do not commit. A new process may take the role only after stale-owner CAS, then replace/rebuild its participant. |
| Coordinator dies after `arming -> activating` commit | Active is target; rollout binding retained | Never roll back. Role watchers continue activation; any recovery coordinator reads current version, renews/adopts the rollout token, and finalizes when both acknowledge. |
| Process dies during `preparing` | Active old | Replacement waits for stale role ownership, loads old active, rebuilds target, and replaces stale participant from `preparing`. |
| Process dies during `arming` | Active old; admission fenced | Replacement takes stale ownership, starts fenced, rebuilds target, and re-arms. Commit still waits for two current owners. |
| Process dies during `activating` before local swap | Active target | Startup loader naturally loads target; process starts fenced, takes stale ownership, replaces participant as activated after generation readiness, then participates in forward finalization. |
| Process dies after local swap but before DB acknowledgement | Active target; applied row may still old | Startup again loads target and retries activation acknowledgement. No old admission is reopened. |
| Finalize succeeds but one watcher has not observed `idle` | Active/applied target | That role remains unnecessarily fenced until its next poll; safe and retryable. |
| Old generation close fails | Rollout may already be idle | Report bounded metric/error and retry local retirement; do not switch default back or corrupt active truth. |

Expired recovery therefore branches on commit side: `preparing|arming` expires backward to `failed`; `activating` expires forward by adoption/renew/finalize. This distinction is the key coordinator-crash invariant missing from the current implementation.

## Resource And Secret Retirement

- `ResolvedSettings.Secret` destruction remains necessary, but construction converts secret bytes to Go strings (`internal/modelsettings/runtime/models.go:235`) and clears only the copied config fields after factories run (`internal/modelsettings/runtime/models.go:101`, `internal/modelsettings/runtime/models.go:262`).
- `ModelRuntime` has no `Close` method (`internal/platform/models/runtime.go:126`), while production HTTP clients use cloned transports (`internal/platform/models/model_transport.go:37`, `internal/platform/models/model_transport.go:57`). Add an idempotent close boundary that at least calls `CloseIdleConnections` on owned transports and releases adapter references.
- Guaranteed zeroization of API keys already copied into immutable Go strings is not possible with the current adapter representation. The implementation can minimize lifetime and drop all references, but a hard zeroization guarantee requires a separate credential-holder redesign. This is a caveat, not a reason to keep container restarts.

## Embedding Contract Isolation

Build/register the candidate embedding version as part of the target generation during `preparing`, keyed by its immutable contract and `model_settings_revision`. Do not route writes/searches through it before activation. Existing revision provenance and index/embedding consistency constraints already prevent silent cross-revision projection mixing (`migrations/00066_model_settings_execution_provenance.sql:18`, `migrations/00066_model_settings_execution_provenance.sql:49`). Old holders continue using the old generation's embedding/index version; new work after finalization resolves the target generation's version.

If preparation creates durable embedding metadata, make the operation idempotent by the existing contract uniqueness rather than publishing a mutable active pointer. Cleanup of an unused prepared version must not delete historical embeddings/indexes.

## Test Plan Implied By The Design

1. Domain transition tests: only pre-commit aborts; no `activating -> failed/previous`; terminal/idempotent retries.
2. PostgreSQL migration tests from existing revision 0, positive desired/active, `idle`, and `failed` rows; fail closed on legacy active rollout; guarded Down with participant history.
3. Ownership tests: same process owns serving plus candidate; a different process cannot steal a fresh role; stale takeover is serialized; late old heartbeat fails.
4. Participant CAS tests: wrong target/instance/version/phase and stale heartbeat fail; process-owner replacement permits controlled participant rebuild.
5. Commit test: two fresh armed roles move active exactly once and leave state `activating`; runtime applied rows remain truthful until per-role acknowledgement.
6. Finalize test: requires both current owners applied/activated/fresh; duplicate finalizers converge by state version CAS.
7. Crash recovery matrix: coordinator crash pre-commit aborts; post-commit recovers forward; API/Worker restart in each live phase.
8. Concurrency test around arming/commit: no new request/claim observes old after active commit; pre-arming holders finish old.
9. Workflow exact-delivery replay across activation returns persisted old binding; a new or reclaimed attempt gets current active; immutable provenance remains enforced.
10. Generation/refcount tests under `-race`: activation does not free an old generation with holders; abort frees only candidate; close is idempotent.
11. Embedding tests: dimension/normalization/distance changes create an isolated version and never write/search across incompatible indexes.
12. End-to-end acceptance: container IDs/start times remain unchanged while desired becomes active and API/Worker applied converge.

Current tests encode the old restart order and should be replaced or retained only as legacy/modelctl coverage: coordinator order (`internal/modelsettings/application/coordinator_test.go:18`), candidate process startup (`internal/modelsettings/runtime/controller_test.go:132`), startup rollout environment (`internal/modelsettings/runtime/loader_test.go:72`), prepared commit (`internal/modelsettings/adapter/postgres/repository_integration_test.go:252`), and full drain claim blocking (`internal/workflow/adapter/postgres/runtime_state_integration_test.go:255`).

## Related Specs And Requirements

- PRD R1-R12 and AC1-AC9: `.trellis/tasks/08-11-model-runtime-hot-activation/prd.md:16` and `.trellis/tasks/08-11-model-runtime-hot-activation/prd.md:31`.
- Current desired/active/applied and fixed-runtime contract: `.trellis/spec/backend/model-settings-runtime.md:16`, `.trellis/spec/backend/model-settings-runtime.md:26`.
- Current drain/attempt-freeze contract: `.trellis/spec/backend/model-settings-runtime.md:40`.
- Current forward-only migration requirements: `.trellis/spec/backend/model-settings-runtime.md:46`.
- Current restart workflow, which this task intentionally supersedes for normal apply: `.trellis/spec/backend/model-settings-runtime.md:79`.

## External References

None. The recommendation is derived from the repository's existing PostgreSQL locks/triggers, runtime composition, workflow provenance, and accepted task requirements. No external framework is needed for the core state machine.

## Caveats / Not Found

- The PRD has not chosen save-and-auto-apply versus explicit Apply. The state machine supports either; explicit target/state-version CAS should be used in both.
- The maximum retention time for old generations and terminal participant rows is still open. Correctness requires no forced close while a local holder exists; product timeout/cancellation behavior needs a separate decision.
- No existing `Close`/destroy contract was found for model adapters or cloned HTTP transports. Reference dropping and `CloseIdleConnections` are implementable, but hard API-key memory zeroization is not currently guaranteed.
- No evidence was found for mixed old/new binary compatibility during the migration that installs hot activation. Treat that deployment separately from normal no-restart configuration apply.
- A short retryable admission fence during `arming`/`activating` is still required for a linearizable cross-process cutover. The design removes container restart and full job drain; it does not promise that every new model request is admitted during the few synchronization polls.
