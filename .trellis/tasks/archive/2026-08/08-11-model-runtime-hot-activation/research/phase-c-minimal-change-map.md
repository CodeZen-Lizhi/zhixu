# Research: Phase C minimal change map

- Query: Read-only review of the current code and the exact minimum Phase C changes for Workflow Claim binding selection, RuntimeNodeWorker generation leases, persisted EmbeddingVersion routing for Search/Reindex vector work, and Artifact ModelRun provenance. Identify Phase A/B dependencies and independently assignable file boundaries.
- Scope: internal
- Date: 2026-08-11

## Findings

### 1. Baseline and blocking dependencies

The current workspace has not landed the Phase A/B surfaces that Phase C is designed to consume:

- There is no `migrations/00079_model_settings_hot_activation.sql`; the latest migration is `00078_model_settings_chat_api_style.sql`.
- `internal/modelsettings/runtime` currently contains `bootstrap.go`, `controller.go`, `loader.go`, `models.go`, and tests, but no `RuntimeHost`, `RuntimeTarget`, generation, or lease implementation.
- `Models` is still one process-start-frozen `platformmodels.ModelRuntime` plus revision (`internal/modelsettings/runtime/models.go:28-56`), and process composition captures that object into Search, Workflow executors, source processing, and reindex (`cmd/worker/main.go:2305-2399`, `cmd/worker/main.go:2472-2508`).

Therefore Phase C must not invent its own rollout phase interpretation or a second runtime registry. Its exact dependencies are:

1. **Phase A migration/schema contract.** `00079` must establish `idle|preparing|arming|activating|failed`, the truthful Worker serving row, database-time freshness, the fixed state -> runtime -> participant lock order, and the forward replacement of the Workflow Attempt insert guard. The task explicitly makes later Workflow Claim work depend on Phase A (`.trellis/tasks/08-11-model-runtime-hot-activation/implement.md:5-16`).
2. **Phase B acquisition contract.** Phase C needs one immutable generation lease, an Attempt target, a current target, and an EmbeddingVersion-compatible target. The current design sketch has only current/Attempt/revision targets (`.trellis/tasks/08-11-model-runtime-hot-activation/research/design-minimal-interface.md:18-49`); revision-only targeting is insufficient to implement same-contract/different-revision reuse.
3. **One server-owned freshness policy.** Claim must use database time and the same Worker freshness interval as Phase A/Host. Freshness must not be supplied by `ClaimCommand`. The existing coordinator defaults to 20 seconds (`internal/modelsettings/application/coordinator.go:13-18`, `internal/modelsettings/application/coordinator.go:83-103`), but no Workflow repository option currently carries it (`internal/workflow/adapter/postgres/runtime_start.go:24-59`). Phase A/B must publish or composition must inject the authoritative value.

The RuntimeHost acquisition API needs one additional settled semantic before Phase C starts:

```text
EmbeddingRuntime(persisted EmbeddingVersion) -> acquire any resident compatible
generation; otherwise rebuild from positive ModelSettingsRevision and revalidate
the full contract.
```

`RevisionRuntime(revision)` alone can rebuild an exact revision, but cannot inspect or select a compatible current generation under a different revision. `EmbeddingVersion` deliberately separates provenance identity from vector compatibility (`internal/retrieval/domain/embedding.go:84-102`). The model-settings runtime package already imports retrieval domain types (`internal/modelsettings/runtime/models.go:15-16`), so using the existing domain value does not create a new package cycle.

### 2. Workflow Claim: exact transaction shape

#### Current problem

`Claim` currently authorizes the caller-supplied revision/instance before it reads the Run, Node, or exact existing Attempt (`internal/workflow/adapter/postgres/runtime_state.go:30-46`). Exact delivery replay is then rejected if that caller-supplied pair differs from the persisted Attempt (`internal/workflow/adapter/postgres/runtime_state.go:76-91`). A new Attempt is inserted directly from the command pair (`internal/workflow/adapter/postgres/runtime_state.go:139-164`). The application contract also requires the returned Attempt pair to equal the command pair (`internal/workflow/application/runtime_contract.go:362-381`).

That ordering makes an old, already-committed exact delivery depend on the mutable current active revision and blocks it during rollout. It must be inverted.

#### Minimum command contract change

Remove `ClaimCommand.ModelSettingsRevision`. Retain only optional `ModelRuntimeInstanceID` as process ownership input:

- `nil` instance means static/unmanaged and a new Attempt persists `(nil, nil)`.
- non-nil canonical instance means managed; PostgreSQL selects the revision and confirms that this process still owns the fresh Worker serving row.

This preserves the current static distinction without adding revision to River args. `RuntimeNodeWorker` currently constructs the command at `internal/workflow/adapter/river/runtime_worker.go:127-130`; Worker process identity is already stable across generations by design (`.trellis/tasks/08-11-model-runtime-hot-activation/design.md:75-77`).

`isValidClaimCommand` should validate only the optional instance owner. `isValidClaimResult` should require the returned Attempt to contain a valid immutable pair and require its instance to equal the command instance, but must not compare a returned revision with caller input. The Attempt itself already carries the complete persisted binding (`internal/workflow/domain/model.go:162-183`).

#### Required transaction order

Keep all existing Run/Node/delivery lease logic, but move model-runtime selection to the point where a new Attempt is actually about to be appended:

1. Begin transaction and get database time.
2. Load and lock Run and Node exactly as today (`runtime_state.go:47-75`).
3. Read exact `(node_run_id, dispatch_no, delivery_id)` Attempt before consulting current model settings.
4. If an exact Attempt exists, first verify only the persisted process-instance ownership tuple against the command (managed old instance versus same managed instance, or static nil versus nil). Do not compare it to current active revision.
5. Apply the existing exact-delivery rules unchanged:
   - same running Attempt, same lease owner, and live Node/Attempt leases returns that exact Attempt and its persisted binding;
   - terminal or still-leased duplicate is benign stale;
   - expired running duplicate continues into the existing lease-lost/new-Attempt path.
6. Apply terminal Run, dispatch, retry-wait, Node status, and live higher-River-attempt checks unchanged (`runtime_state.go:92-118`). In particular, a higher River attempt while the old lease is live remains retryable `WORKFLOW_LEASE_HELD` (`runtime_state.go:104-116`).
7. Only when a new Attempt is now eligible, select its binding:
   - static command: `(nil, nil)`;
   - managed command: lock singleton state `FOR SHARE`, then Worker serving row `FOR SHARE`, matching the Phase A commit lock order;
   - allow new selection only in `idle|preparing|failed`; `preparing` still serves old active, while `arming|activating` returns retryable `WORKFLOW_MODEL_RUNTIME_CLAIM_BLOCKED`;
   - require `runtime.instance_id == command.ModelRuntimeInstanceID`, `runtime.phase == active`, `runtime.applied_revision == state.active_revision`, and `heartbeat_at` fresh by database time;
   - return `(state.active_revision, runtime.instance_id)` as repository-selected values.
8. Select the binding before marking an expired Attempt `lease_lost`. If selection is blocked/stale, rollback leaves the old Attempt untouched.
9. Insert the new Attempt using the selected local values, then update Node/Run and commit exactly as today (`runtime_state.go:119-164`).

The old Attempt binding is never updated. An expired delivery still appends a new Attempt, so it uses the active binding selected at that new Claim. This preserves the existing reclaim model and the task's queued-versus-claimed rule: a lease-lost row remains historical, while the replacement is a new Attempt. The common River retry changes `job.Attempt` and therefore the delivery ID (`runtime_worker.go:127`), so higher transport delivery naturally takes the new-Attempt path.

An exact live replay must work during `arming|activating`, because it is not new admission. A replacement process with a different instance ID must not replay an old exact delivery; the current integration test already protects this ownership mismatch (`internal/workflow/adapter/postgres/runtime_state_integration_test.go:204-231`). A later higher River delivery may reclaim after lease expiry and select the replacement process/current active binding.

#### Migration dependency

Migration `00065` currently guards every managed INSERT against the one current Worker row and requires its revision/instance/phase to match (`migrations/00065_model_settings_workflow_provenance.sql:15-75`). Phase A `00079` must replace this function/trigger, not Phase C and not an edit to `00065`:

- preserve UPDATE immutability;
- preserve positive-revision existence and revision-0 semantics;
- for a new managed Attempt, structurally require the selected state active revision and current Worker serving instance/applied revision;
- match the new phase shape (`preparing` can authorize old active; `arming|activating` cannot authorize a new insert);
- keep Claim query as the authoritative database-time freshness check unless Phase A defines a single database-owned freshness function.

The activation commit and Claim query must both lock state then Worker runtime. This makes the commit edge the linearization point: Claim commits wholly old before it, or wholly target after acknowledgement/final idle; no state/runtime split can be inserted. Phase A already requires `00079` to replace the Attempt guard (`.trellis/tasks/08-11-model-runtime-hot-activation/design.md:168-174`).

#### Exact tests to change/add

Owning files:

- `internal/workflow/application/runtime_contract_test.go`
- `internal/workflow/adapter/postgres/runtime_state_integration_test.go`
- Phase A migration integration tests, owned by the Phase A agent

Minimum cases:

1. command carries instance only; repository returns DB-selected revision;
2. exact live old Attempt replays after active changes and while state is `arming`/`activating`;
3. exact replay from another process instance still fails;
4. `preparing` creates an old-active Attempt; `arming|activating` create none and return retryable blocked;
5. stale/missing/unavailable Worker serving row creates no Attempt;
6. expired old lease plus higher delivery after cutover marks only the old row `lease_lost` and appends a target-revision Attempt;
7. concurrent Claim versus activation commit produces only complete old or complete target pairs;
8. direct mismatched insert and binding UPDATE remain rejected by the forward trigger.

Existing reclaim coverage is at `runtime_state_integration_test.go:960-1064`; it should be extended, not replaced.

### 3. RuntimeNodeWorker generation lease

#### Current problem

`RuntimeNodeWorker` stores one startup-frozen executor registry and revision/instance pair (`internal/workflow/adapter/river/runtime_worker.go:32-52`). It Claims using that pair, resolves from the fixed registry, starts heartbeat, executes, joins heartbeat, and performs failure/human/success settlement (`runtime_worker.go:127-266`). This guarantees one fixed runtime only because the whole process is fixed; it cannot hot-switch future Attempts.

#### Narrow consumer interface

Do not make the Workflow adapter depend on a concrete generic `RuntimeHost[workerPayload]`, and do not place a Worker payload containing Workflow types in `internal/modelsettings/runtime` (the design forbids RuntimeHost reverse dependencies on Workflow/Retrieval). Define a narrow port beside the River worker, implemented by a composition adapter around Phase B Host:

```text
RuntimeExecutorAcquirer.AcquireAttempt(ctx, persisted NodeAttempt)
    -> RuntimeExecutorLease

RuntimeExecutorLease.Executors() -> *ExecutorRegistry
RuntimeExecutorLease.Release()    // idempotent
```

The composition adapter converts the Attempt's `(nil,nil)` or `(revision,instance)` into the Phase B `AttemptRuntime` target and extracts the generation's executor registry. This keeps generic role payload and factories in composition while Workflow sees only what it uses.

#### Exact Work lifetime

In `Work`:

1. Claim first. Do not Acquire for Claim error or `ClaimDispositionStale` (`runtime_worker.go:127-136`).
2. For a claimed result, Acquire from `claim.Attempt` before `ExecutorRegistry.Resolve` at current line 173.
3. Immediately `defer lease.Release()` after successful Acquire.
4. Resolve the executor from `lease.Executors()`, not a worker field.
5. Keep the defer at `Work` scope. It must run after executor resolution failure settlement, `Execute`, heartbeat join, control failure, ordinary failure, human-wait persistence, or successful `Complete` (`runtime_worker.go:171-266`). Releasing immediately after `Execute` would be too early because finalization and terminal settlement can still use generation-owned dependencies or provenance.

Acquisition of a Claim-returned frozen Attempt needs a Phase B guarantee: a Claim committed immediately before arming must not be abandoned merely because the in-process current-acquisition gate closed before the next instruction. The clean contract is that a persisted `AttemptRuntime` target is already durable admission and can pin its exact resident generation while `CurrentRuntime` acquisition is fenced. If Phase B instead blocks it, RuntimeNodeWorker must start the Workflow heartbeat before the potentially blocking Acquire and keep it alive until Acquire succeeds or the River context ends. Returning a switching error after a committed Claim without heartbeat is safe from duplicate execution but strands the lease until expiry and forces subsequent River attempts through `WORKFLOW_LEASE_HELD`.

Phase B must also retain a resident generation while a running/recoverable Attempt for the same process can request it. Historical reconstruction is not a substitute for replaying an Attempt owned by a replaced process: `AttemptRuntime` must enforce instance ownership.

#### Worker tests

Update `internal/workflow/adapter/river/runtime_worker_test.go`:

- fake Claim owns the returned persisted binding; it must no longer copy revision from the command (`runtime_worker_test.go:497-507`);
- stale/Claim error makes zero acquisition calls;
- old and target Claim fixtures resolve different registries/executors;
- acquisition target exactly matches the returned Attempt, including static nil binding;
- Release occurs exactly once and only after Complete, Fail, control settlement, or human wait returns;
- executor resolution failure still settles while the lease is held;
- Acquire failure performs no executor/provider call;
- heartbeat cancellation and lease-loss paths release exactly once.

`cmd/worker/main.go` must stop passing `modelBinding.revision` into `RuntimeWorkerOptions`; the process instance remains stable (`cmd/worker/main.go:1014-1043`). Composition integration is root-owned because this file also builds every Worker generation.

### 4. Search: acquire from persisted Active Index

#### Current authority is already correct

`SearchRepository.LoadActiveSearchIndex` returns the active Index and its referenced immutable EmbeddingVersion (`internal/retrieval/adapter/postgres/search.go:47-79`). `SearchService` currently ignores that fact for runtime selection because it holds one startup-fixed `queryEmbedder` (`internal/retrieval/application/search.go:65-83`). It validates the fixed contract only after routing (`search.go:232-252`).

#### Minimum application port and flow

Replace the fixed query embedder field with a narrow compatible-embedding acquirer:

```text
CompatibleEmbeddingAcquirer.Acquire(ctx, persisted EmbeddingVersion)
    -> CompatibleEmbeddingLease

CompatibleEmbeddingLease.Embedder() -> QueryEmbedder
CompatibleEmbeddingLease.Release()
```

Semantic/Hybrid flow:

1. canonicalize request and load/validate active SearchIndex exactly as today (`search.go:87-113`);
2. keyword/FTS-only routes do not acquire a model runtime;
3. for a vector-capable persisted index, acquire once with `*index.EmbeddingVersion` before launching the Hybrid vector goroutine;
4. defer Release until the complete Search call, including vector DB query and optional rerank, returns;
5. pass the leased embedder explicitly into `embedQuery`; keep `ValidateEmbeddingContractBinding` immediately before the provider call (`search.go:232-252`);
6. contract-selection/reconstruction failure returns explicit `RETRIEVAL_VECTOR_EMBEDDER_VERSION_UNAVAILABLE` (or the settled Phase B stable error mapped to it) and never tries the new default adapter.

Do not classify an acquisition/contract mismatch as the existing retryable Hybrid provider degradation. A retryable error from the already-compatible provider call may retain today's keyword fallback (`search.go:173-199`); failure to prove a compatible vector space is an AC8 fail-closed result.

The Host adapter must first try resident current/retiring/historical generations by full `ValidateEmbeddingContractBinding`; only then use positive `ModelSettingsRevision` as a reconstruction hint. The existing contract includes endpoint identity and limits through `ConfigHash` (`internal/retrieval/domain/embedding_contract.go:18-100`), and the final binding check is at `embedding_contract.go:103-124`.

Tests in `internal/retrieval/application/search_test.go` should prove no Acquire for keyword/FTS-only, one Acquire/Release for Semantic and Hybrid, release after vector/rerank completion, same-contract/different-revision success, old Active Index after cutover, incompatible runtime fail-closed without provider/vector call, and historical reconstruction unavailable without use of current default. Existing mismatch/fallback cases at `search_test.go:173-224` should be adapted rather than weakened.

### 5. Vector Builder/Reindex: acquisition belongs above the page loop

#### Why per-page acquisition is not sufficient

`VectorBuilder` currently binds one Embedder/Contract at construction, loads a bounded page using that contract's limits, verifies it against the page's persisted EmbeddingVersion, calls the provider, and atomically commits (`internal/retrieval/application/vector_builder.go:22-67`, `vector_builder.go:79-223`). That immutable inner algorithm is correct inside one generation.

The Reindex `Processor` calls `BuildNextVectorBatch` in a loop (`internal/retrieval/application/processor.go:335-367`), while the River Worker invokes one fixed process-composed Processor (`internal/retrieval/adapter/river/worker.go:103-190`). Changing only `VectorBuilder.BuildNextVectorBatch` to Acquire/Release per page would allow one `Process` invocation to span multiple generation leases. It also has a target/limit cycle: the persisted EmbeddingVersion is returned by `LoadVectorBuildPage`, but the page command's batch/input limits are currently derived from the Embedder contract before the load (`vector_builder.go:84-100`).

The minimum correct boundary is therefore a Reindex processor-graph lease, not a dynamic proxy inside each Embed call:

```text
ReindexProcessorAcquirer.Acquire(ctx, claimed Delivery)
    -> ReindexProcessorLease

ReindexProcessorLease.Processor() -> ProcessorRunner
ReindexProcessorLease.Release()
```

The composition adapter resolves the target as follows:

- if `claim.Delivery.IndexVersionID` is present, load that Index and its EmbeddingVersion, then acquire a generation compatible with the full persisted EmbeddingVersion;
- if no Index checkpoint exists yet, acquire `CurrentRuntime` and use that generation's registered default EmbeddingVersion/Processor options to create the new Index;
- construct/select a Processor and VectorBuilder bound to the selected target EmbeddingVersion ID and the leased compatible Embedder. Same contract under another settings revision may reuse the adapter, but `ProcessorOptions.EmbeddingVersionID` remains the persisted target identity.

`domain.Delivery` already carries the optional durable `IndexVersionID` (`internal/retrieval/domain/delivery.go:153-177`), and Claim returns the Delivery (`internal/retrieval/application/delivery_runtime.go:55-63`). No revision belongs in Reindex River args or DeliveryAttempt.

In `internal/retrieval/adapter/river/worker.go`, Acquire after successful Delivery Claim and before creating the delivery heartbeat/Processor call, and defer Release through `Processor.Process`, heartbeat join, failure settlement, final heartbeat, and `CompletionService.Complete` (`worker.go:120-190`). On transport retry/process restart, resolve again from the persisted Delivery/Index.

Under this boundary, `VectorBuilder` should remain immutable and fixed to the lease's Embedder; it does not need to know RuntimeHost. A small constructor/factory adjustment may be required so the generation-scoped Reindex factory can create a target-ID-specific Processor/VectorBuilder, but the vector batching/commit algorithm should not be rewritten. This is a smaller and stronger implementation than per-page Host acquisition.

Preserve the existing terminal recovery behavior: when a historical index has no pending vectors, it may finish without an Embedder (`internal/retrieval/application/vector_builder.go:100-117`, `internal/retrieval/application/vector_builder_test.go:67-124`). The target resolver/processor factory must not require historical reconstruction solely to prove an already-empty vector stage.

The same generation-lease principle applies to `SourceRefresher`, whose options and vector builder are also startup-frozen and whose vector loop spans the operation (`internal/retrieval/application/source_refresher.go:80-108`, `source_refresher.go:111-227`; batch path `source_batch_refresher.go:80-190`). Workflow-invoked refresh is covered by the RuntimeNodeWorker's Worker generation lease. Git/direct refresh needs its own operation acquisition boundary in the generation-scoped source-processing graph; a dynamic per-page Embedder is not enough.

Minimum Reindex tests:

- no Acquire for stale/committed Delivery Claim;
- a new uncheckpointed Delivery acquires current once and freezes the generation's EmbeddingVersion when it creates the Index;
- a resumed Delivery resolves the persisted Index/EmbeddingVersion and acquires compatible once;
- lease remains held through all vector pages and completion/failure settlement;
- same contract/different revision reuses an adapter while retaining old EmbeddingVersion/cache identity;
- changed model/dimension/normalization/distance fails before provider or commit;
- historical pending target unavailable is explicit and fail-closed;
- empty historical vector stage completes without requiring reconstruction.

### 6. Artifact executor provenance fix

This change is independent of Phase A/B and can be implemented immediately.

`createModelRun` currently omits `ExecutionContext.ModelSettingsRevision` from the new `agentdomain.ModelRun` (`internal/artifact/workflow/executor.go:298-315`), and `sameModelRunExecutionBinding` omits it from create/replay equality (`executor.go:345-350`). The database already requires ModelRun revision to equal its Workflow Attempt revision (`migrations/00066_model_settings_execution_provenance.sql:153-188`), and the Agent repository already persists/scans/compares the field (`internal/agent/adapter/postgres/repository.go:98`, `internal/agent/adapter/postgres/scans.go:179-200`). No schema migration is needed.

Minimum code change in `internal/artifact/workflow/executor.go`:

1. set `ModelSettingsRevision` to a cloned `execution.ModelSettingsRevision` when constructing the run;
2. include nil-sensitive revision equality in `sameModelRunExecutionBinding`;
3. reject a negative execution revision in `validateExecution` (`executor.go:512-520`);
4. add local clone/equality helpers, following existing Agent/Organizing patterns (`internal/agent/adapter/workflow/executor.go:93-100`, `internal/organizing/workflow/generation.go:817-835`).

Tests in `internal/artifact/workflow/executor_test.go`:

- a managed execution persists exact revision 0/positive provenance;
- exact replay with the same revision succeeds;
- nil versus zero and different positive revision replay fail with `ErrorCodeRunReplayUnsafe` before any provider call;
- negative revision is rejected by execution validation.

The current replay test entry point is `executor_test.go:187-226`; its fixture currently omits revision (`executor_test.go:251-269`). Use a dedicated managed fixture or explicit mutation so unrelated static cases remain covered.

### 7. Independently assignable file boundaries

| Work packet | Files owned | Dependency | May run independently |
| --- | --- | --- | --- |
| Artifact provenance | `internal/artifact/workflow/executor.go`, `executor_test.go` | Existing migration 00066 only | Yes, now |
| Workflow Claim contract/repository | `internal/workflow/application/runtime_contract.go`, tests; `internal/workflow/adapter/postgres/runtime_state.go`, integration tests | Phase A state/runtime schema, phase names, freshness and lock order | Yes after Phase A; no RuntimeHost dependency |
| Workflow RuntimeNodeWorker | `internal/workflow/adapter/river/runtime_worker.go`, tests | Stable Phase B lease/acquirer semantics | Yes after Phase B; use fake acquirer before composition |
| Search acquisition | `internal/retrieval/application/search.go`, tests, optionally one retrieval-owned acquisition port file | Phase B full EmbeddingVersion-compatible target | Yes after Phase B |
| Reindex/vector operation lease | `internal/retrieval/adapter/river/worker.go`, tests; generation-scoped Processor/VectorBuilder factory in retrieval/application as finally located | Phase B Host and Worker generation payload | Separate from Search after shared port is frozen |
| Source refresh/Git graph | `internal/retrieval/application/source_refresher.go`, `source_batch_refresher.go` only if a wrapper is required; Git/source adapters and tests | Worker generation/acquirer contract | Separate after runtime port is frozen |
| Composition integration | `cmd/worker/main.go`, `cmd/worker/main_test.go`; `cmd/api/main.go` and tests | All prior packets | Root-owned, serial integration |

Do not assign `cmd/worker/main.go` concurrently to Workflow and Retrieval agents. It currently owns the stable process instance, executor registry, Search services, source processing, VectorBuilder, Processor, and both River workers (`cmd/worker/main.go:1014-1043`, `cmd/worker/main.go:1292-1304`, `cmd/worker/main.go:1638-1656`, `cmd/worker/main.go:2289-2522`). The task plan already reserves this integration to root (`.trellis/tasks/08-11-model-runtime-hot-activation/implement.md:26-36`).

### 8. Smallest safe implementation order

1. Land Phase A migration/ports and publish the exact freshness/lock contract.
2. Land Phase B RuntimeHost including `AttemptRuntime`, `CurrentRuntime`, and full EmbeddingVersion-compatible acquisition; settle persisted-Attempt behavior during arming.
3. Independently fix Artifact provenance.
4. Implement Workflow Claim and its real PostgreSQL race/replay tests.
5. Implement RuntimeNodeWorker against a fake acquirer, then Search and Reindex operation acquisition in parallel.
6. Build immutable API/Worker generation payloads and integrate once in `cmd/api/main.go` / `cmd/worker/main.go`.
7. Run the cross-cut tests only after composition: pre-commit old Claim, post-commit target Claim, old Attempt still executing, old Active Index Search, historical pending Reindex, and unchanged process/container identity.

## Files Found

- `internal/workflow/application/runtime_contract.go` - Claim input/result contract currently equates command and Attempt model bindings.
- `internal/workflow/adapter/postgres/runtime_state.go` - Claim transaction, exact delivery replay, lease reclaim, and current startup-frozen authorization query.
- `internal/workflow/adapter/river/runtime_worker.go` - fixed registry/revision Worker execution and settlement lifetime.
- `migrations/00065_model_settings_workflow_provenance.sql` - immutable Attempt binding and current Worker-row insert guard that Phase A must replace forward.
- `migrations/00066_model_settings_execution_provenance.sql` - Embedding/Index/ModelRun provenance constraints and triggers already in force.
- `internal/retrieval/application/search.go` - active-index load followed by use of one fixed query Embedder.
- `internal/retrieval/adapter/postgres/search.go` - persisted Active Index/Embedding lookup and vector-query identity revalidation.
- `internal/retrieval/application/vector_builder.go` - fixed-contract bounded page/provider/atomic commit algorithm.
- `internal/retrieval/application/processor.go` - one Process loops over multiple vector pages.
- `internal/retrieval/adapter/river/worker.go` - Reindex Claim, Processor call, heartbeat, and completion lifetime.
- `internal/retrieval/domain/embedding.go` and `embedding_contract.go` - provenance identity versus complete runtime compatibility contract.
- `internal/artifact/workflow/executor.go` - missing ModelRun revision write/replay comparison.
- `cmd/worker/main.go` and `cmd/api/main.go` - process-start composition points that currently freeze all model-dependent graphs.

## Code Patterns

- Workflow exact replay and reclaim: `internal/workflow/adapter/postgres/runtime_state.go:76-164`.
- Existing command/return binding equality: `internal/workflow/application/runtime_contract.go:362-391`.
- Worker execution-to-settlement lifetime: `internal/workflow/adapter/river/runtime_worker.go:127-266`.
- Full Embedding contract check: `internal/retrieval/domain/embedding_contract.go:103-124`.
- Search's persisted index authority: `internal/retrieval/adapter/postgres/search.go:47-79`.
- Vector page and atomic write boundary: `internal/retrieval/application/vector_builder.go:79-223`.
- Reindex vector loop versus Worker operation lifetime: `internal/retrieval/application/processor.go:335-421`, `internal/retrieval/adapter/river/worker.go:103-190`.
- Existing correct revision-copy patterns: `internal/agent/adapter/workflow/executor.go:93-100`, `internal/organizing/workflow/generation.go:402-420`.

## External References

No external references were needed. All conclusions come from the current repository, active task design, migrations, and tests.

## Related Specs

- `.trellis/tasks/08-11-model-runtime-hot-activation/prd.md:20-53` - R5/R6/R8 and AC4/AC8/AC9.
- `.trellis/tasks/08-11-model-runtime-hot-activation/design.md:55-118` - acquisition/generation lifetime.
- `.trellis/tasks/08-11-model-runtime-hot-activation/design.md:120-174` - durable state and migration compatibility.
- `.trellis/tasks/08-11-model-runtime-hot-activation/design.md:223-242` - Workflow and Retrieval binding.
- `.trellis/tasks/08-11-model-runtime-hot-activation/research/implementation-context.md:24-65` - database, Workflow, Embedding, and Host invariants.
- `.trellis/tasks/08-11-model-runtime-hot-activation/research/inflight-and-embedding-contract.md:204-238` - queued/claimed behavior and required tests.

## Caveats / Not Found

- `00079`, RuntimeHost, RuntimeTarget, and generation lease APIs are not present in the current workspace. Names and concrete signatures above are minimum behavioral contracts, not claims about landed APIs.
- The active design sketch does not yet expose a full EmbeddingVersion-compatible target; Phase B must close that gap before Search/Reindex can implement same-contract/different-revision reuse without layering a second registry.
- The exact server-owned Worker freshness value is not currently available to Workflow Claim. Do not hard-code a second interval or accept it from the Worker command.
- A literal edit only to `search.go` and `vector_builder.go` cannot satisfy the frozen Reindex whole-Process lease requirement. `internal/retrieval/adapter/river/worker.go` (or an equivalent operation runner above `Processor`) must own that lease.
- The task documents use both “exact existing delivery returns persisted binding” and “lease expiry creates a new Attempt under then-active revision.” The minimum plan resolves them by returning the persisted binding only when the existing Attempt is replayable; once lease expiry causes append of a new Attempt, the new row selects current active while the old row remains immutable/lease-lost. This matches current append-only reclaim behavior and should be locked by an explicit integration test.
