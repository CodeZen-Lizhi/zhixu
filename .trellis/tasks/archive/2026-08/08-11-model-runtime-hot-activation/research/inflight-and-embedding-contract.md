# Research: In-flight Workflow and Embedding Contracts

- Query: Trace how `model_settings_revision` is frozen and consumed across Workflow enqueue, Claim, Attempt, retry/replay and long-running execution; trace how Embedding/Index Version prevents model or dimension mixing; determine the runtime-retention contract required for model hot activation without restarting the Worker container.
- Scope: internal
- Date: 2026-08-11

## Findings

### Executive conclusion

The current restart requirement is an intentional consequence of three existing choices, not a fundamental requirement of Workflow or Retrieval:

1. A Worker is constructed with one frozen model revision and runtime instance for its entire lifetime (`internal/workflow/adapter/river/runtime_worker.go:46-67`).
2. Agent, capture, organizing, search, and vector-build services capture concrete Chat/Embedding adapters when the process is composed, rather than resolving a runtime from the persisted Attempt or Index binding (`internal/agent/adapter/workflow/executor.go:24-64`, `internal/retrieval/application/vector_builder.go:58-68`, `internal/retrieval/application/search.go:65-84`).
3. Runtime ownership has one row per role and does not permit the same instance to change revision. Registering another instance overwrites the role row, while the old controller fails closed when ownership is lost (`migrations/00064_model_settings.sql:228-275`, `internal/modelsettings/adapter/postgres/runtime.go:30-50`, `internal/modelsettings/runtime/controller.go:34-55`).

Workflow already has most of the durable semantics needed for hot activation. A queued Job does not carry a model revision. The successful Claim freezes the then-authorized revision and runtime instance into an immutable `workflow.node_attempt`; the executor receives that persisted revision. Therefore a hot cutover can be linearized at Claim without changing queued Jobs (`internal/workflow/adapter/river/args.go:21-28`, `internal/workflow/adapter/postgres/runtime_state.go:30-42`, `internal/workflow/adapter/postgres/runtime_state.go:139-164`, `internal/workflow/adapter/river/runtime_worker.go:127-197`).

The required lifetime mechanism is not “refcount or reconstruction”; it is both, for different recovery scopes:

- A live, already-claimed execution must retain the exact immutable runtime bundle selected for its persisted Attempt until the executor and its finalization path return. An explicit `RuntimeLease`/reference count is the clearest contract. A strong Go reference would technically keep the current adapters alive because no `Close` contract exists, but immediate cache replacement or eviction would be unsafe and untestable.
- A durable historical operation that is resumed after process loss cannot depend on an in-memory reference. Positive model settings revisions are append-only and can be loaded and decrypted by exact revision, so a historical runtime can be lazily reconstructed, then verified against the persisted Chat/Embedding contract before use (`migrations/00064_model_settings.sql:89-106`, `internal/modelsettings/adapter/postgres/revision.go:81-98`, `internal/modelsettings/adapter/postgres/revision.go:246-277`).
- Reconstruction must not be used to revive an old Workflow provider call after process loss. Workflow lease reclaim creates a new Attempt, and ModelRun replay rules deliberately reject automatic provider replay when calls may already exist (`internal/workflow/adapter/postgres/runtime_state.go:119-164`, `internal/agent/adapter/workflow/executor.go:101-107`, `.trellis/spec/backend/database-guidelines.md:906-909`).
- Reconstruction is required for Retrieval because a pending historical Index is durably bound to an EmbeddingVersion and may need its exact compatible adapter after restart. The existing recovery builder explicitly finishes an index with no pending vectors but refuses pending work unless a matching embedder is restored (`internal/retrieval/application/vector_builder.go:70-76`, `internal/retrieval/application/vector_builder.go:100-125`).

### Files found

- `.trellis/spec/backend/model-settings-runtime.md`: current managed-settings rollout, enqueue fence, Claim freeze, and restart contract.
- `.trellis/spec/backend/database-guidelines.md`: Retrieval embedding/index immutability, historical vector recovery, ModelRun replay, and transaction rules.
- `docs/architecture/ai-runtime.md`: durable Workflow, model-run, active-index, and versioning architecture.
- `docs/architecture/adr/0012-version-workflows-prompts-schemas.md`: long-running Workflow version-pinning decision.
- `internal/workflow/adapter/river/args.go`: minimal Workflow transport payload with no model revision.
- `internal/workflow/adapter/river/inserter.go`: tx-scoped enqueue fence and River insertion.
- `internal/workflow/adapter/river/runtime_worker.go`: process-lifetime runtime binding, Claim, heartbeat, and executor context construction.
- `internal/workflow/adapter/postgres/runtime_state.go`: authoritative Claim, Attempt creation, duplicate delivery, lease reclaim, retry, pause/resume, and human wait transitions.
- `internal/workflow/domain/model.go`: persisted `NodeAttempt` model.
- `internal/workflow/application/runtime_contract.go`: Claim/runtime binding validation.
- `internal/workflow/application/executor_registry.go`: trusted execution context and process-frozen executor registry.
- `internal/workflow/adapter/postgres/runtime_state_integration_test.go`: managed runtime binding, replay, owner replacement, and drain tests.
- `migrations/00065_model_settings_workflow_provenance.sql`: immutable Attempt revision/instance constraints and active Worker trigger.
- `internal/modelsettings/runtime/loader.go`: active/target revision selection and exact revision loading.
- `internal/modelsettings/runtime/models.go`: immutable Chat/Embedding runtime bundle construction.
- `internal/modelsettings/adapter/postgres/revision.go`: append-only revision creation and exact historical decryption.
- `internal/modelsettings/adapter/postgres/runtime.go`: singleton runtime ownership and enqueue fence.
- `internal/modelsettings/runtime/controller.go`: one fixed revision per process controller and fail-closed ownership.
- `migrations/00064_model_settings.sql`: append-only settings history and one-row-per-role runtime schema.
- `internal/agent/adapter/workflow/executor.go`: fixed Chat adapter, Attempt-to-ModelRun revision propagation, and unsafe replay rejection.
- `internal/agent/adapter/workflow/rag_executor.go`: RAG ModelRun revision propagation and fixed model dependency.
- `internal/capture/profile/generator.go`: profile request/attempt/model-run revision freeze.
- `internal/capture/adapter/postgres/profile_repository.go`: exact profile replay binding and stale-on-active-revision-change finalization.
- `internal/organizing/workflow/generation.go`: generation and ModelRun replay identity includes settings revision.
- `internal/retrieval/domain/embedding.go`: EmbeddingVersion identity and vector-contract compatibility semantics.
- `internal/retrieval/domain/embedding_contract.go`: runtime-to-persisted embedding contract validation.
- `internal/retrieval/domain/index.go`: IndexVersion embedding/settings binding and replay identity.
- `internal/retrieval/application/vector_builder.go`: historical pending-vector recovery and exact adapter contract check.
- `internal/retrieval/application/search.go`: active-index lookup and query embedder contract check.
- `internal/retrieval/adapter/postgres/vector_build.go`: version-scoped cache/page/write transactions.
- `internal/retrieval/adapter/postgres/search.go`: active Index/Embedding loading and exact-vector query validation.
- `internal/retrieval/adapter/river/args.go`: minimal Reindex transport payload, also without model revision.
- `internal/retrieval/adapter/river/worker.go`: Reindex Claim/lease/processor/completion lifecycle, separate from Workflow Attempts.
- `internal/retrieval/adapter/postgres/completion.go`: atomic Index activation and Reindex completion gate.
- `migrations/00014_retrieval_index_foundation.sql`: Index/Manifest/Projection foreign keys, active uniqueness, dimensions, and immutable vector writes.
- `migrations/00016_embedding_hybrid_search.sql`: version-scoped embedding cache and vector shape validation.
- `migrations/00066_model_settings_execution_provenance.sql`: settings provenance consistency across EmbeddingVersion, IndexVersion, ModelRun, and NodeAttempt.

### 1. Workflow revision freeze from enqueue through replay

#### Enqueue

`NodeJobArgs` contains only schema version, NodeRun ID, and dispatch number; it deliberately does not copy node input or model configuration (`internal/workflow/adapter/river/args.go:21-28`). The inserter checks the model-settings enqueue fence inside the same transaction as River insertion (`internal/workflow/adapter/river/inserter.go:115-140`). The current fence allows `idle`, `failed`, and `validating`, and blocks `draining` and later phases (`internal/modelsettings/adapter/postgres/runtime.go:155-172`).

This proves that an unclaimed queued Job has no revision to migrate. Its revision is determined only when a Worker successfully Claims it. Hot activation therefore does not require rewriting or re-enqueuing existing jobs.

#### Claim and Attempt creation

The Worker currently copies one process-lifetime revision and runtime instance into every Claim (`internal/workflow/adapter/river/runtime_worker.go:46-86`, `internal/workflow/adapter/river/runtime_worker.go:127-130`). The Claim transaction first locks/checks `ops.model_settings_state`, then verifies the exact active Worker runtime row (`internal/workflow/adapter/postgres/runtime_state.go:167-203`). It allows new Claims only in `idle`, `failed`, or `validating`; the command revision must equal `active_revision`.

On a successful Claim, the transaction appends a new Attempt with both `model_settings_revision` and `model_runtime_instance_id` (`internal/workflow/adapter/postgres/runtime_state.go:131-164`). The migration makes this pair immutable, requires a known positive revision (with revision `0` as the disabled exception), and verifies the pair against the active Worker runtime on insert (`migrations/00065_model_settings_workflow_provenance.sql:3-75`).

The River Worker does not trust its original option after Claim for execution provenance. It builds `ExecutionContext.ModelSettingsRevision` from the persisted Attempt returned by Claim (`internal/workflow/adapter/river/runtime_worker.go:189-197`). Database constraints then require `agent.model_run.model_settings_revision` to exactly match the corresponding Attempt (`migrations/00066_model_settings_execution_provenance.sql:153-188`).

#### Duplicate delivery and transport retry

For the same Node, dispatch, and delivery ID, Claim compares the incoming revision/instance pair with the existing Attempt before replaying it. A mismatch is `WORKFLOW_MODEL_RUNTIME_BINDING_MISMATCH`; the same live owner and binding may receive the same Attempt; terminal or still-leased duplicates are stale (`internal/workflow/adapter/postgres/runtime_state.go:76-91`). The integration test verifies exact replay, immutability, replacement-owner mismatch, and a new queued Job being claimed only by the replacement owner (`internal/workflow/adapter/postgres/runtime_state_integration_test.go:191-247`).

Claim authorizes the command against the current active runtime before looking up the existing delivery (`internal/workflow/adapter/postgres/runtime_state.go:35-42`). Consequently, after a hot switch:

- An exact old command from the superseded runtime fails ownership authorization before replay lookup.
- A command from the new runtime that collides with an old delivery reaches the binding comparison and fails rather than silently changing the Attempt.
- A higher River transport attempt has a new delivery ID (`job-<id>-attempt-<n>`), so after the old lease expires it may mark the old Attempt `lease_lost` and append a new Attempt using the then-current Claim binding (`internal/workflow/adapter/river/runtime_worker.go:127-130`, `internal/workflow/adapter/postgres/runtime_state.go:104-164`).

This behavior should be preserved. Hot activation must never mutate an existing Attempt to a new revision.

#### Heartbeat and completion of already-claimed work

After Claim, heartbeat uses only the Node/Attempt lease fence and database time. It does not re-check `active_revision` or the runtime ownership row (`internal/workflow/adapter/postgres/runtime_state.go:264-305`). The same pattern applies to terminal transitions: the persisted Attempt and lease fence are the authority, not the current model default.

Therefore the durable Workflow model already permits an old-revision Attempt to finish after the global active revision changes. The present rollout avoids observing that state by draining all running River jobs before committing the target, but that is an operational restriction rather than a Workflow data-model requirement. The hot-activation implementation should remove the all-running-jobs drain requirement while retaining the existing lease fence.

#### Business retry, pause/resume, and human wait

- A retryable business failure ends the old Attempt, increments `dispatch_no`, and inserts a new minimal Job. The new Job has no revision and gets the active revision at its later Claim (`internal/workflow/adapter/postgres/runtime_state.go:1141-1191`).
- Pause/resume similarly increments `dispatch_no` and inserts a new minimal Job for paused/pending/retry-wait Nodes (`internal/workflow/adapter/postgres/runtime_state.go:1503-1529`).
- `WaitForHuman` ends/releases the running Attempt and does not hold a Worker lease while waiting (`internal/workflow/adapter/postgres/runtime_state.go:635-701`). Human submission completes that Node and activates successor Nodes; those successor jobs are unbound until Claim (`internal/workflow/adapter/postgres/runtime_state.go:723-825`). The old model runtime need not remain retained throughout a human wait.
- A lease-expired running Node is not resumed inside the same Attempt. Claim marks the old Attempt `lease_lost` and appends a new Attempt (`internal/workflow/adapter/postgres/runtime_state.go:119-164`).

One Workflow Run can therefore legitimately contain NodeAttempts from multiple model settings revisions. The invariant is per Attempt, while Definition/Prompt/Schema/Tool versions remain independently frozen by their own durable bindings (`docs/architecture/ai-runtime.md:169-200`).

### 2. Long-running model consumers and replay behavior

The revision in `ExecutionContext` is currently provenance only; it does not select the adapter. The executor registry stores concrete executors, and model executors capture concrete Chat models during process composition (`internal/workflow/application/executor_registry.go:17-38`, `internal/workflow/application/executor_registry.go:113-127`, `internal/agent/adapter/workflow/executor.go:24-64`). A simple atomic replacement of a global “current model” would therefore be insufficient and dangerous: either old executors would keep the old adapter forever, or a dynamic wrapper could switch an already-started Attempt between calls.

Hot activation must acquire one immutable execution bundle after Claim, keyed by the persisted Attempt revision, and hold it for the entire `executor.Execute` call. The bundle must include more than the raw HTTP adapter:

- Chat adapter and its frozen contract.
- The matching model profile/runtime catalog used to persist ModelRun model/profile metadata.
- Embedding adapter and its complete EmbeddingContract where the executor performs retrieval or indexing.
- Any revision-derived limits that affect request/result semantics.

Agent ModelRun creation copies the Attempt revision and rejects replay once the run already exists (`internal/agent/adapter/workflow/executor.go:93-107`). RAG does the same and uses the fixed model dependency for all recorded calls (`internal/agent/adapter/workflow/rag_executor.go:173-198`, `internal/agent/adapter/workflow/rag_executor.go:217-239`). Organizing replay identity includes the settings revision, model, prompt, and schemas (`internal/organizing/workflow/generation.go:348-423`, `internal/organizing/workflow/generation.go:804-826`). These checks must remain exact.

Capture profile generation also freezes the revision into the profile contract, ModelRun, and ProfileRevision (`internal/capture/profile/generator.go:224-243`, `internal/capture/profile/generator.go:451-473`, `internal/capture/profile/generator.go:509-529`). Its finalization intentionally compares the Attempt revision with the current active revision: if the model settings changed while work was running, the immutable result is still committed but the profile pointer becomes `STALE` rather than `READY` (`internal/capture/adapter/postgres/profile_repository.go:366-419`, `internal/capture/adapter/postgres/profile_repository.go:455-491`). Hot activation should preserve this behavior; it is an explicit currentness policy, not a reason to cancel the old Attempt.

### 3. Embedding and Index Version isolation

#### Identity versus compatibility

`EmbeddingVersion` freezes provider, adapter name/version, model, dimensions, normalization, distance metric, config hash, and optional model settings revision (`internal/retrieval/domain/embedding.go:38-52`). `EmbeddingContract` additionally freezes endpoint identity and batching/input limits, and its hash covers all non-secret result-affecting configuration (`internal/retrieval/domain/embedding_contract.go:18-59`, `internal/retrieval/domain/embedding_contract.go:62-100`). Before a provider call, the runtime contract must match the persisted EmbeddingVersion exactly on vector-result fields (`internal/retrieval/domain/embedding_contract.go:103-124`).

The settings revision is provenance identity, but it is deliberately not part of vector compatibility. Two settings revisions with an otherwise identical EmbeddingContract are distinct EmbeddingVersion identities but may safely use the same runtime adapter (`internal/retrieval/domain/embedding.go:84-102`, `internal/retrieval/domain/model_settings_provenance_test.go:10-32`). This distinction should drive the resolver:

1. Resolve the target persisted EmbeddingVersion.
2. Reuse an already-built runtime, including the current runtime, if `ValidateEmbeddingContractBinding` succeeds.
3. Otherwise load the EmbeddingVersion's positive `model_settings_revision`, build a historical runtime, and verify the same full contract before use.
4. Fail closed with `RETRIEVAL_VECTOR_EMBEDDER_VERSION_UNAVAILABLE` if no compatible runtime can be produced.

Revision equality alone is insufficient because a corrupted or incorrectly composed adapter could still expose a different contract. Conversely, forcing revision equality for adapter reuse would rebuild equivalent adapters unnecessarily.

#### Index and projection binding

`IndexVersion` stores both the EmbeddingVersion ID and its model settings provenance (`internal/retrieval/domain/index.go:20-43`). The database requires a vector Index's settings revision to equal its referenced EmbeddingVersion revision, while an FTS-only Index cannot carry one (`migrations/00066_model_settings_execution_provenance.sql:108-150`). Index replay identity includes this revision (`internal/retrieval/domain/index.go:205-224`).

The schema permits multiple vector dimensions in the generic `vector` column, but each projection is bound through the Index to one EmbeddingVersion. Projection writes verify that exact EmbeddingVersion, validate `vector_dims` against its registered dimensions, and make the terminal vector immutable (`migrations/00014_retrieval_index_foundation.sql:101-143`, `migrations/00014_retrieval_index_foundation.sql:277-388`). The embedding cache key is `(workspace_id, embedding_version_id, content_hash)` and its statement trigger validates dimension, finite norm, and required L2 normalization (`migrations/00016_embedding_hybrid_search.sql:3-48`).

Vector building reads only projections and cache rows for the target Index's EmbeddingVersion, validates the runtime contract before calling the provider, and commits cache plus projection terminal state in one transaction (`internal/retrieval/adapter/postgres/vector_build.go:17-57`, `internal/retrieval/application/vector_builder.go:100-125`, `internal/retrieval/application/vector_builder.go:160-223`, `internal/retrieval/adapter/postgres/vector_build.go:153-198`). Exact cache readback rejects the same key with different float32 values (`internal/retrieval/adapter/postgres/vector_build.go:251-304`). These existing layers prevent model/dimension mixing even if multiple runtime revisions coexist in memory.

#### Search must follow the active Index, not the active model setting

Search first loads the Workspace's unique active Index and its referenced EmbeddingVersion (`internal/retrieval/adapter/postgres/search.go:47-79`). Query embedding validates the configured adapter contract against that persisted version before calling the provider (`internal/retrieval/application/search.go:232-252`). The vector query then reloads the persisted EmbeddingVersion, verifies full identity, validates the query vector, and chooses the distance operator from the persisted metric/dimensions (`internal/retrieval/adapter/postgres/search.go:118-164`).

After hot activation, the active Index may still reference the old EmbeddingVersion. A search service that blindly uses only the newly active model runtime would make Semantic unavailable or make Hybrid degrade until a new Index activates. A revision/contract-aware query embedder resolver should instead acquire a runtime compatible with the active Index for that request. The model-settings active pointer selects defaults for new model work; it does not redefine an already-active Index's vector space.

### 4. Reindex is a separate durable edge case

Reindex is not a Workflow NodeAttempt and currently has no model settings provenance on its DeliveryAttempt. Its River payload contains only schema version, Delivery ID, and dispatch number (`internal/retrieval/adapter/river/args.go:21-37`). The Worker Claims a Reindex delivery using only delivery/dispatch/River/lease identity, then invokes a process-composed Processor (`internal/retrieval/adapter/river/worker.go:103-133`).

The target Index/EmbeddingVersion is therefore the only durable model-selection authority for Reindex. A hot-runtime implementation must resolve the embedder from that target, not from the Reindex Job payload and not from the global current revision. Hold a compatible runtime lease for one active `Process` invocation; after transport retry or process restart, resolve it again from the persisted Index/EmbeddingVersion.

The completion transaction validates delivery/lease/regression/ready-index facts and atomically activates the target (`internal/retrieval/adapter/postgres/completion.go:80-166`, `internal/retrieval/adapter/postgres/completion.go:242-256`). It contains no comparison with `ops.model_settings_state.active_revision`. Consequently, current behavior permits a ready historical Index to activate after model-settings cutover. Hot activation must make an explicit product choice:

- Preserve that behavior and guarantee that Search resolves an adapter compatible with whichever Index is active; or
- Add a currentness gate that marks/reschedules a stale target and builds a new Index under the new embedding revision.

What is not valid is activating the historical Index and embedding queries with an incompatible new default adapter.

### 5. Required hot-activation invariants

1. **Prepare before publish.** Build and validate an immutable target runtime bundle in API and Worker before changing `active_revision`. Candidate failure leaves the current active bundle and Claim authority unchanged.
2. **One linearization point for new work.** The database update that commits `active_revision` and the Worker Claim-authorized runtime slot must be atomic or protected by the same lock order used by Claim. A Claim committed before the point receives the old revision; one after receives the new revision. No Claim may observe a state/slot mismatch.
3. **Queued jobs remain unbound.** Do not add revision to Workflow or Reindex River Args. Existing queued jobs simply Claim after the switch.
4. **Attempt binding never changes.** Once Claim returns an Attempt, resolve by `Attempt.ModelSettingsRevision` once and hold the bundle through model calls and finalization. Do not resolve “current” separately for each call.
5. **Old in-process bundles drain by reference, not by queue drain.** Mark the old bundle non-current after the switch, but evict it only after its local lease count reaches zero. New Claims must never acquire it merely because it is cached.
6. **Historical reconstruction is fail-closed.** A cache miss for a positive revision may call `LoadRevision`, build the immutable bundle, destroy temporary secret values as today, and verify the requested persisted contract. Wrong/missing key or contract mismatch is an explicit unavailable/consistency result, never fallback to current.
7. **Workflow crash semantics remain unchanged.** Do not reconstruct an old bundle to replay an uncertain old ModelRun. Lease expiry creates a new Attempt under the then-active revision; provider-call uncertainty remains manual recovery/unknown.
8. **Retrieval resolves from persisted vector space.** Vector build and query resolve by EmbeddingVersion full contract. Same contract/different revision may reuse an adapter; cache and Index identities remain version-scoped.
9. **Capture currentness remains explicit.** An old Attempt may complete after activation, but existing profile finalization may mark its result stale.
10. **Revision `0` and static nil remain special.** Revision `0` is canonical disabled and has no revision row; it cannot reconstruct an enabled adapter. Static/unmanaged `nil` provenance also has no durable settings history and should remain non-hot-reloadable unless a separate immutable static configuration archive is introduced.
11. **Runtime ownership schema must support overlap.** The current `role` primary key plus immutable same-instance revision cannot represent a current target slot alongside locally draining old bundles (`migrations/00064_model_settings.sql:228-275`). The implementation needs either:
    - a stable process record plus multiple revision/generation slots and one explicitly active Claim slot; or
    - a revised single process controller whose active slot can CAS from old to new while historical bundles remain local and non-claimable.

The Attempt should continue to persist revision plus an unambiguous runtime generation/slot identity. Reusing only a mutable process instance ID weakens duplicate-delivery diagnostics. Overwriting the current singleton role row with a new instance while the old controller still heartbeats is not viable because the old controller treats ownership loss as fatal.

### 6. Suggested resolution and eviction model

A practical internal interface is a revision/contract-aware Runtime Registry:

```text
active claim slot -> immutable RuntimeBundle(revision, generation, chat, embedding, catalogs/contracts)
                         |
                         +-- AcquireForAttempt(revision, generation) -> RuntimeLease
                         +-- AcquireForEmbedding(EmbeddingVersion)   -> RuntimeLease
                         +-- Release()                               -> refcount--
```

- The active bundle is pinned.
- Switching active marks the old entry historical but does not close/evict it.
- Every in-flight operation holds a lease/strong reference.
- A historical positive revision cache miss uses a single-flight build from `LoadRevision`; raw resolved secrets are destroyed immediately after `Build`, matching `LoadSettings` (`internal/modelsettings/runtime/loader.go:58-81`).
- Historical entries may use bounded LRU/TTL eviction only when refcount is zero. Eviction affects latency, not correctness, because positive revisions are append-only and reconstructable.
- If adapters later gain `Close`, only the registry closes them after zero references. Without an explicit lease, a swap/eviction race would be difficult to prove safe.
- For Embedding, resolution is by full contract compatibility. The revision is the primary reconstruction hint, not the sole compatibility test.

### 7. Exact queued versus claimed behavior to preserve

| State at activation | Required result |
|---|---|
| Candidate is only prepared/validating | Current code continues enqueue and Claim against the previous `active_revision`; preparation alone must not route production work to the candidate. |
| Current rollout enters draining/applying/verifying | Current code blocks enqueue/Claim and waits for running work to quiesce. A hot design may replace this broad drain with an atomic Claim-slot cutover, but must preserve the same all-old/all-new linearization. |
| Workflow Job queued, never Claimed | Carries no revision; first successful post-switch Claim creates a new Attempt under the new active slot. |
| Claim transaction committed before cutover | Attempt remains old revision/generation and runs to completion with an old runtime lease. |
| Claim races cutover | Database lock/CAS order decides entirely old or entirely new; no state/slot split and no partially inserted Attempt. |
| Same delivery replay while old Claim is live | Only exact old revision/generation may return the same Attempt; another binding conflicts. |
| Higher River attempt while old lease is live | Remains retryable `WORKFLOW_LEASE_HELD`; it must not start a second provider execution. |
| Old lease expires after cutover | New River delivery/Claim marks the old Attempt lease-lost and appends a new Attempt under the current revision. |
| Business retry scheduled before cutover, Claims after cutover | New dispatch Job has no revision and creates a new Attempt under the new revision. |
| Pause/resume after cutover | New dispatch is unbound and Claims the new revision. |
| Human wait spans cutover | Old Attempt has ended; successor work Claims the active revision when dispatched. No runtime lease is held across the human wait. |
| Old Attempt completes after cutover | Lease-fenced completion remains valid; consumers with currentness policy may mark derived output stale. |
| Reindex has no pending vectors | May finish without an embedder, as today. |
| Historical Reindex still has pending vectors | Must acquire a runtime compatible with the persisted EmbeddingVersion or fail unavailable; never use incompatible current defaults. |
| Active Index still uses old EmbeddingVersion | Semantic/Hybrid query acquires a compatible historical/current adapter by contract; vector query remains in that Index's vector space. |

### 8. Tests required for the hot-activation implementation

- Claim/cutover race with many queued nodes: every persisted Attempt is wholly old or new; no binding mismatch, duplicate execution, or missing job.
- A multi-call Agent executor blocked mid-call while activation commits: all calls and ModelRun metadata remain old revision/model/profile; a later Claim uses the new bundle.
- Old runtime entry cannot be evicted while an Attempt lease is held; it becomes evictable after Work returns.
- Business retry, pause/resume, and lease reclaim after activation create new Attempts with the current revision; existing Attempt rows remain immutable.
- Exact duplicate delivery with a different generation/revision conflicts; higher River attempt with live lease remains retryable held.
- Capture profile started under the old revision completes successfully but becomes `STALE` when active changed.
- Historical pending hybrid Index resumes after activation and after process restart by reconstructing a compatible revision; contract mismatch/unknown revision/wrong key fails closed.
- Same EmbeddingContract under different settings revisions may reuse the runtime adapter, but produces/uses distinct EmbeddingVersion and cache identities.
- Changed model or dimensions cannot write to the old EmbeddingVersion; the entire batch rolls back and projection/cache remain unchanged.
- Search on an active old Index after model activation resolves a compatible adapter and never sends a vector with the new dimensions/model.
- Reindex completion across settings cutover exercises the chosen policy explicitly: either compatible historical activation remains searchable, or stale activation is rejected/rescheduled.
- Runtime registry concurrent single-flight load, failed build, retry, refcount, eviction, and candidate-abort tests; no endpoint/credential leaks in errors or logs.
- Runtime ownership migration tests prove old and new slots can overlap without terminating the process/controller, while exactly one slot authorizes new Claims.

## External References

No external references were needed. The conclusions are derived from repository contracts, migrations, and tests at the current workspace version.

## Related Specs

- `.trellis/spec/backend/model-settings-runtime.md:18-19`: one immutable runtime per process in the current restart design.
- `.trellis/spec/backend/model-settings-runtime.md:26-43`: desired/active/applied meanings, append-only revisions, enqueue fence, Claim freeze, and no Attempt hot switch.
- `.trellis/spec/backend/model-settings-runtime.md:69-71`: runtime ownership and replay error requirements.
- `.trellis/spec/backend/model-settings-runtime.md:108-109`: queued Job must not declare revision; Claim freezes it.
- `.trellis/spec/backend/database-guidelines.md:636-647`: immutable EmbeddingVersion, per-version dimensions, exact replay, and all-or-none projection batches.
- `.trellis/spec/backend/database-guidelines.md:703-716`: full EmbeddingContract, version-scoped cache, historical recovery, and active-only search.
- `.trellis/spec/backend/database-guidelines.md:724-732`: model/dimension/result mismatch and cache conflict behavior.
- `.trellis/spec/backend/database-guidelines.md:906-924`: one ModelRun per Attempt and no automatic replay of unknown provider calls.
- `docs/architecture/ai-runtime.md:44-47`: independent Index build/activation and no embedding-space mixing.
- `docs/architecture/ai-runtime.md:73-76`: ModelRun freeze and no runtime model switching.
- `docs/architecture/ai-runtime.md:173-188`: minimal River args, Claim/lease, and new Attempt on retry.
- `docs/architecture/ai-runtime.md:198-200`: in-flight version retention and explicit migration for unavailable executors.
- `docs/architecture/adr/0012-version-workflows-prompts-schemas.md:5-12`: long-running work continues with its start-time versions.

## Caveats / Not Found

- No Runtime Registry, runtime lease/refcount abstraction, adapter `Close` contract, or revision-aware executor factory exists in the inspected code. Refcount is therefore a recommended explicit lifecycle contract, not an existing implementation fact.
- The current runtime ownership schema cannot model overlapping active and historical slots for one role. A database/runtime-controller change is required before a same-container hot switch can be authoritative for Workflow Claim.
- Reindex DeliveryAttempt does not persist `model_settings_revision`; this is not necessarily a missing column because IndexVersion/EmbeddingVersion is the stronger durable vector-space authority. Any new Reindex provenance field must not replace that binding.
- `internal/retrieval/adapter/postgres/completion.go` contains no `model_settings`/`active_revision` check. Whether an old-revision ready Index may activate after model cutover is a product/architecture decision that must be made explicitly.
- Static/unmanaged historical EmbeddingVersions with nil revision cannot be reconstructed from model-settings history. The proposed reconstruction guarantee applies to positive managed revisions.
- Revision `0` is canonical disabled and has no persisted row; it is reconstructable only as disabled capability.
- The API request path was not exhaustively traced in this topic. The same principle should apply to model-using API requests: acquire the active bundle once per operation and hold it through the response. Long-lived connections that do not retain model work should not pin a runtime indefinitely.
- No existing test covers a model-settings active switch while a Workflow Attempt, Reindex Processor, or semantic query remains in flight; the current suite assumes process drain/restart.
