# Hot Activation Implementation Context

## Purpose

This file is the compact context-injection index for implement/check agents. `prd.md`, `design.md` and `implement.md` remain authoritative for this task. The full source specs/research below must be opened directly when changing their owning layer; they are intentionally not injected when their size exceeds Trellis limits.

## Authoritative Sources

- `.trellis/spec/backend/database-guidelines.md`
  - Workflow/River queue, Claim, lease and retry contracts.
  - M6-A Index/Embedding Version immutability and activation.
  - M6-C Embedding Contract, historical pending-vector recovery and active-index search.
- `.trellis/spec/backend/error-handling.md`
  - Stable Foundation category/code/retryability mapping and fail-closed behavior.
- `.trellis/spec/backend/quality-guidelines.md`
  - Domain/Application/PostgreSQL/HTTP/Composition gates, real database tests and canonical checks.
- `.trellis/spec/frontend/quality-guidelines.md`
  - Typed boundary, explicit status, accessibility, browser and canonical Web gates.
- `.trellis/tasks/08-11-model-runtime-hot-activation/research/hot-activation-state-machine.md`
  - Existing schema/controller evidence, participant table, commit-side recovery and migration matrix.
- `.trellis/tasks/08-11-model-runtime-hot-activation/research/inflight-and-embedding-contract.md`
  - Workflow enqueue/Claim/Attempt/retry evidence and exact Embedding/Index compatibility behavior.

## Database Invariants

1. `desired_revision` is the latest immutable save; `active_revision` is the commit point for new default work; each role's `applied_revision` is its truthful serving projection.
2. Global live phases are `preparing|arming|activating`. Only `arming -> activating` updates active. `failed` is pre-commit only; `activating` is forward-only.
3. Every state/participant mutation uses rollout ID + expected phase/version CAS and database time. Locks are acquired in a single documented order.
4. API/Worker candidate facts live in `(rollout_id, role)` participant rows. They never overwrite the current serving runtime row.
5. A different process cannot replace a fresh runtime owner. Stale takeover locks the role row, compares database heartbeat age and makes late old heartbeats fail by instance predicate.
6. Saving desired is blocked while activation is live. Start freezes exact expected desired revision; response loss/repeat for the same live target returns the existing operation.
7. Commit verifies both current process owners and both fresh armed participants, updates active once and retains rollout binding in `activating`.
8. Per-role acknowledgement atomically updates the truthful serving `applied_revision` and participant to activated. Finalize requires both roles before returning global state to idle.
9. Migration is forward-only after activation history. Up accepts legacy idle/failed and fails closed on a legacy live rollout; Down rejects participant history/new live shape with SQLSTATE `55000`.
10. Update the shared runtime mutation gate and Workflow Attempt insert guard in the same forward migration; do not edit `00064`-`00078`.

## Workflow Invariants

1. River Job args stay minimal and do not carry model settings revision.
2. A new Attempt's model binding is selected inside the Claim transaction from locked active state + current fresh Worker serving row.
3. An existing Attempt's persisted binding never changes. Exact duplicate/replay, lease-held, lease-lost and higher-delivery behavior retain existing safety semantics.
4. A Claim committed before cutover is entirely old; after cutover it is entirely target. Arming/activating block new Claim/admission but do not wait for running Attempts.
5. Worker acquires one generation from the Claim-returned binding and holds it through Executor resolution, provider calls, heartbeat/finalization and terminal settlement.
6. Business retry/pause/resume/human successor work creates or dispatches unbound future work and selects the then-active revision on its later Claim.
7. ModelRun provenance must equal the Attempt revision. `internal/artifact/workflow/executor.go` currently omits this field/equality and must be fixed with regression tests.

## Embedding And Index Invariants

1. Search resolves the persisted Active Index and its Embedding Version before choosing an Embedder. Current model settings are not the vector-space authority.
2. Vector Builder/Reindex resolves the target Index/Embedding Version and holds one compatible generation for the whole processing invocation.
3. Compatibility uses the complete persisted Embedding Contract. Revision is provenance/rebuild hint; equal Contract under another revision may reuse an Adapter.
4. A changed model/dimension/normalization/distance cannot write or query the old Index. Contract mismatch is a consistency error, never a fallback.
5. Historical positive managed revisions can be loaded/decrypted/built on demand; missing/wrong-key/contract failure returns explicit unavailable. Revision 0/static nil does not promise historical reconstruction.
6. Existing active or ready historical Index behavior remains valid across settings activation; search must continue using a compatible historical/current Adapter.
7. Embedding cache and projection writes remain version-scoped, set-based and transactionally all-or-none.

## RuntimeHost Invariants

1. Generation is immutable and contains revision/binding, Chat+Embedding capabilities/contracts and the role-specific dependency graph.
2. Consumer surface is `Acquire(target) -> Lease`; rollout lifecycle stays private to Host/coordinator.
3. Acquire pins one generation for the complete operation. Per-method dynamic model proxies are prohibited.
4. Arming closes a short acquisition gate; existing leases continue. Activating swaps the local default under the closed gate and reopens only after global idle/applied convergence.
5. Candidate failure/abort and retired cleanup are idempotent. No Adapter/Transport closes while a lease exists; only owned transports are closed.
6. Resolved Secret buffers are destroyed after Build. Persisted/logged errors never include Secret, Authorization, full Endpoint, provider body, DSN, stack or internal instance IDs.
7. Static mode remains one revision-0 process generation with no managed activation watcher.

## HTTP And Frontend Invariants

1. Activation is `POST /api/v1/settings/models/activations` with strict `{expected_revision}` only. It is Session-only, Origin/CSRF protected and `no-store`.
2. Main UI action performs Save then exact-target Start. Save success is durable even if Start fails; never roll it back or report the whole action as unsaved.
3. Secondary Save Only advances desired and exposes Apply Config later.
4. Browser is an observer, not coordinator. Abort/timeout/page close does not cancel an operation; refetch recovers the durable state.
5. Snapshot separates serving role state from candidate participant state and strictly exposes apply-required, target, phase, role progress and safe errors.
6. Capability badges continue to reflect active/serving facts until commit/finalization. Prepared candidate is not displayed as already enabled.
7. `web/src/api/model-settings.ts` remains the sole strict decoder/encoder owner. No Secret enters Query cache, Storage, URL, toast, DOM error, logs or activation payload.
8. Poll only while non-terminal; mutation success updates/invalidate cache immediately, terminal state triggers one final authoritative refetch, focus/network recovery also refetches.

## Required Quality Evidence

- Go unit/race and vet on affected packages; no unbounded goroutine, double Release, lock inversion or context leak.
- Real PostgreSQL migration/Repository/Claim concurrency tests with `-p 1 -timeout 60s`.
- SQL review for parameterization, tenant/workspace scope, lock/CAS order, constraints/triggers and guarded Down.
- OpenAPI path/schema/checker plus Handler/Router Session/CSRF/no-store tests.
- Web lint/typecheck/test/build; component tests for Save-and-Apply intermediate state, polling/recovery, strict invalid response, Secret and accessibility.
- Controlled fake-model browser smoke with desktop/mobile, Network/DOM/Storage/log scan and unchanged API/Worker container ID/StartedAt.
- `go mod tidy -diff` and `git diff --check`.

## Deferred / Prohibited

- No user Cancel/Rollback endpoint in MVP.
- No dedicated runtime service, provider fallback, traffic split or automatic new Active Index.
- No mutation of historical Attempt/ModelRun/Embedding/Index facts.
- No normal-path launcher/Docker restart, volume deletion or real user Provider access in tests.
- No claim of hard API-key memory zeroization while Authorization remains an immutable Go string; minimize lifetime and release owned references instead.
