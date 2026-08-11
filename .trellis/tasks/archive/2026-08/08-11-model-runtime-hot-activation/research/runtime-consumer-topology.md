# Research: Model runtime consumer topology and hot-activation seam

- Query: Trace the current model runtime from API/Worker composition through `internal/modelsettings/runtime`, `internal/platform/models`, workflow/retrieval/agent consumers; identify process-single immutable assumptions, revision propagation, lifecycle/cleanup, concurrency, and the narrowest in-process candidate/active/retired manager seam.
- Scope: internal
- Date: 2026-08-11

## Findings

### Executive conclusion

There is no adapter-level requirement to restart an API or Worker container. The restart requirement comes from composition and ownership:

1. Startup loads exactly one revision and constructs one immutable `*runtime.Models` (`internal/modelsettings/runtime/loader.go:19-34`, `internal/modelsettings/runtime/loader.go:39-81`).
2. API and Worker immediately project that object into long-lived raw Chat models, Embedders, contracts, booleans, registries, handlers, processors, and executors.
3. The persisted rollout/controller model describes process replacement: a `Controller` owns one fixed instance/revision (`internal/modelsettings/runtime/controller.go:34-59`), and the database stores only one runtime row per role (`migrations/00064_model_settings.sql:228-244`).
4. Loss of that fixed ownership terminates the API or Worker (`cmd/api/main.go:666-673`, `cmd/worker/main.go:570-574`).

The immutable `Models` object is a useful safety primitive and should not be mutated in place. The narrowest safe design is an RCU-like manager in `internal/modelsettings/runtime` that owns immutable candidate/active/retired generations and returns operation-scoped leases. Stable composition-layer dispatchers must acquire one generation for a whole model-dependent unit of work; changing only `Models.Chat()` or `Models.Embedding()` into dynamic getters is unsafe because a single workflow/search could observe two revisions.

### Files found

- `.trellis/spec/backend/model-settings-runtime.md` - current contract explicitly requires one immutable runtime per process and restart rollout.
- `.trellis/tasks/08-11-model-runtime-hot-activation/prd.md` - target contract: no container restart, old work pinned, both roles prepared, retired cleanup, embedding compatibility.
- `internal/modelsettings/runtime/models.go` - managed settings to immutable platform runtime adapter.
- `internal/modelsettings/runtime/loader.go` - one-time active/candidate revision loader.
- `internal/modelsettings/runtime/bootstrap.go` - startup repository/service/manager/loaded-runtime composition.
- `internal/modelsettings/runtime/controller.go` - fixed process ownership and drain state machine.
- `internal/modelsettings/domain/rollout.go` - process-replacement rollout/runtime phases.
- `internal/modelsettings/adapter/postgres/runtime.go` - one-row-per-role registration, heartbeat, enqueue fence.
- `internal/modelsettings/adapter/postgres/rollout.go` - two-role prepared verification and atomic commit.
- `internal/modelsettings/application/coordinator.go` - drain, wait, commit, and queue resume ordering.
- `internal/platform/models/runtime.go` - immutable Chat/Embedding capabilities.
- `internal/platform/models/model_transport.go` - per-adapter HTTP client/transport construction.
- `cmd/api/main.go` - API composition root and static capability projections.
- `cmd/api/model_runtime_gate.go` - mutation-only drain gate.
- `internal/app/router.go` - startup-frozen RAG readiness/status projection.
- `cmd/worker/main.go` - Worker composition root and all model-bound component bundles.
- `cmd/worker/model_runtime_drain.go` - producer/queue drain and River running-job check.
- `internal/workflow/adapter/river/runtime_worker.go` - fixed revision claim and execution dispatch.
- `internal/workflow/adapter/postgres/runtime_state.go` - attempt provenance persistence and runtime authorization.
- `internal/workflow/application/executor_registry.go` - frozen concurrent executor registry.
- `internal/agent/adapter/workflow/executor.go` and `internal/agent/adapter/workflow/rag_executor.go` - model-bound Agent executors and ModelRun provenance.
- `internal/capture/profile/generator.go`, `internal/artifact/workflow/executor.go`, `internal/organizing/workflow/generation.go` - additional model-bound workflow consumers.
- `internal/retrieval/application/search.go` - query-time contract check and Embedding call.
- `internal/retrieval/application/vector_builder.go` - index-time fixed embedder/contract.
- `internal/retrieval/domain/embedding.go` and `internal/retrieval/domain/embedding_contract.go` - persisted Embedding Version compatibility.
- `internal/retrieval/adapter/river/worker.go` - fixed reindex processor per River worker.

### Current topology

```text
PostgreSQL active_revision
        |
        v
Bootstrap -> LoadSettings (once) -> *runtime.Models {revision, immutable ModelRuntime}
        |                                      |
        |                                      +-- ChatCapability {model, contract}
        |                                      +-- EmbeddingCapability {embedder, contract}
        |
        +-- API process
        |     +-- startup RAG booleans/readiness/routes
        |     +-- SearchService(raw embedder) x2
        |     +-- frozen workflow contracts/definitions
        |
        +-- Worker process
              +-- fixed {revision, runtime instance id}
              +-- frozen workflow ExecutorRegistry
              +-- Agent/RAG/Capture/Artifact/Organizing executors
              +-- tool SearchService and workflow SearchServices
              +-- VectorBuilder + SourceRefresher + Reindex Processor
              +-- RuntimeNodeWorker claims every attempt with startup binding
```

The repository specification documents the same invariant: API/Worker load one runtime and reuse its capabilities everywhere (`.trellis/spec/backend/model-settings-runtime.md:16-22`), while every attempt freezes its revision (`.trellis/spec/backend/model-settings-runtime.md:40-43`). The current "Good" path is explicitly a prepared candidate process plus restart (`.trellis/spec/backend/model-settings-runtime.md:77-83`).

### Runtime construction and immutable behavior

- `Models` contains only a `*platformmodels.ModelRuntime` and revision; `Chat()`, `Embedding()`, and `Revision()` return that fixed generation (`internal/modelsettings/runtime/models.go:28-55`).
- `Build` overlays one resolved revision and calls `platformmodels.NewConfiguredModelRuntime` once (`internal/modelsettings/runtime/models.go:89-107`).
- Managed `LoadSettings` reads a snapshot, chooses active or an environment-bound rollout target, decrypts once, builds once, and returns `LoadedSettings` (`internal/modelsettings/runtime/loader.go:27-81`). Bootstrap calls this once during composition (`internal/modelsettings/runtime/bootstrap.go:28-57`).
- `ModelRuntime` is explicitly process-wide and immutable; construction creates the enabled Chat and Embedding adapters and freezes their contracts (`internal/platform/models/runtime.go:126-170`). Getters only return stored capabilities (`internal/platform/models/runtime.go:173-187`).
- Tests assert adapter identity remains the same, source config mutation cannot alter it, and 64 concurrent readers observe stable adapters/contracts (`internal/platform/models/runtime_test.go:13-49`, `internal/platform/models/runtime_test.go:90-118`).

Immutability is therefore not the blocker. It is what makes a generation lease useful: each candidate can be built independently, then an atomic active pointer can select one immutable generation without changing an object already held by old work.

### Complete production caller inventory and freeze points

Direct production references to `*modelsettingsruntime.Models` under `cmd/` and `internal/` are confined to the two composition roots and the runtime loader. The table also includes the long-lived objects that receive extracted adapters/contracts/booleans and therefore indirectly assume the same process-single runtime.

| Owner / caller | Frozen projection | Long-lived consumers and consequence |
|---|---|---|
| API `runAPI` | One `configuredModels` from Bootstrap/static load/fallback (`cmd/api/main.go:192-250`) | Passed throughout API lifetime; credentials are removed from `cfg` only after composition (`cmd/api/main.go:252`). |
| API capability/status | `ragEnabled` is computed once from Chat state (`cmd/api/main.go:323-324`) | Artifact generation dependency, question dispatcher, router readiness/status are decided once (`cmd/api/main.go:565-585`, `cmd/api/main.go:620-638`, `internal/app/router.go:91-92`, `internal/app/router.go:358-387`). Disabled-to-enabled cannot appear without rebuilding these projections. |
| API retrieval handler | Raw `configuredModels.Embedding().Embedder()` (`cmd/api/main.go:479-487`) | Stored in one `SearchService` for handler lifetime (`cmd/api/main.go:1351-1380`). |
| API organizing handler | Raw Embedder (`cmd/api/main.go:546-549`) | Stored in another `SearchService` and owner graph (`cmd/api/main.go:829-892`). |
| API workflow composition | Chat state converted to a bool (`cmd/api/main.go:1626-1661`) | Agent/RAG contracts and definitions are conditionally registered, then registries are frozen (`cmd/api/main.go:1697-1737`, `cmd/api/main.go:1748-1792`). A pointer swap cannot add missing definitions after disabled startup. |
| Worker `runWorker` | One `configuredModels`; one `workerModelRuntimeBinding` copies its revision and generates one instance ID (`cmd/worker/main.go:324-362`) | Same object and binding build the entire Worker graph (`cmd/worker/main.go:1006-1077`). The composition helper explicitly accepts at most one frozen runtime (`cmd/worker/main.go:1045-1062`). |
| Worker tool runtime | Raw Embedder | Tool search stores one `SearchService` (`cmd/worker/main.go:1678-1735`). |
| Worker Agent/RAG | Raw Chat model, Chat contract, runtime catalog, raw Embedder | Relation and RAG executors are built once (`cmd/worker/main.go:1955-2022`, `cmd/worker/main.go:2038-2069`). Disabled Chat returns no executors (`cmd/worker/main.go:1968-1971`). |
| Capture profile | Agent component's raw Chat model/contract | Generator/catalog are built once; the enabled implementation stores the model (`cmd/worker/main.go:2089-2127`, `internal/capture/profile/generator.go:34-51`). |
| Artifact workflow | Agent raw Chat model/contract plus raw Embedder | Executor and SearchService are only constructed if Chat was enabled at composition (`cmd/worker/main.go:2148-2234`). |
| Organizing workflow | Agent raw Chat model/contract | Generator is either permanently unavailable or built with the startup model/catalog (`cmd/worker/main.go:1414-1429`). |
| Source processing | Raw Embedder and its Contract; fixed EmbeddingVersion ID | Registers Embedding Version with startup revision, creates one `VectorBuilder`, and freezes `SourceRefresher` options (`cmd/worker/main.go:2305-2391`). |
| Reindex | Source-processing bundle plus the same `Models` | Composition checks the bundle matches the startup Embedder and creates one fixed Processor/worker (`cmd/worker/main.go:2472-2505`, `internal/retrieval/adapter/river/worker.go:49-77`). |
| Workflow runtime worker | Frozen executor registry plus startup revision/instance ID | Every concurrent `Work` uses the same claim binding and resolves the same registry (`cmd/worker/main.go:1638-1647`, `internal/workflow/adapter/river/runtime_worker.go:34-67`, `internal/workflow/adapter/river/runtime_worker.go:104-197`). |
| Connection test | A throwaway `Models` per test | `TestResolvedConnection` calls `Build` and probes it but has no release/close (`internal/modelsettings/runtime/models.go:200-229`). This is not a process-single consumer, but it exposes the cleanup gap candidate preparation must avoid. |

The Worker registry itself is concurrency-safe but intentionally frozen: registration takes a write lock, `Freeze` makes it read-only, and `Resolve` uses an `RLock` (`internal/workflow/application/executor_registry.go:113-120`, `internal/workflow/application/executor_registry.go:148-198`, `internal/workflow/application/executor_registry.go:215-229`). Hot activation should keep that registry frozen and register stable delegating executors, rather than mutating the registry at every revision.

### How `model_settings_revision` currently reaches model calls

The workflow path is:

```text
Models.Revision()
  -> workerModelRuntimeBinding {revision, instance ID}
  -> RuntimeWorkerOptions (copied once)
  -> ClaimCommand
  -> workflow.node_attempt.model_settings_revision
  -> ExecutionContext.ModelSettingsRevision
  -> ModelRun / workflow-specific provenance record
```

Evidence and limits:

- Worker copies `models.Revision()` into one binding and one generated instance ID (`cmd/worker/main.go:1014-1042`); composition rejects a binding that differs from the frozen runtime (`cmd/worker/main.go:1065-1077`).
- `RuntimeNodeWorker` freezes copies of those pointers at construction (`internal/workflow/adapter/river/runtime_worker.go:46-87`) and sends them on every Claim (`internal/workflow/adapter/river/runtime_worker.go:127-130`). The test mutates the source values after construction and proves Work still claims revision 7 (`internal/workflow/adapter/river/runtime_worker_test.go:60-79`).
- PostgreSQL rejects a duplicate delivery whose binding differs (`internal/workflow/adapter/postgres/runtime_state.go:76-83`), persists revision/instance on the attempt (`internal/workflow/adapter/postgres/runtime_state.go:144-147`), and authorizes a new managed claim against both global active revision and the exact active Worker runtime row (`internal/workflow/adapter/postgres/runtime_state.go:167-203`).
- After claim, `RuntimeNodeWorker` copies the persisted attempt revision into `ExecutionContext` before invoking the executor (`internal/workflow/adapter/river/runtime_worker.go:173-197`).
- Relation and RAG Agent executors copy the execution revision into `ModelRun` (`internal/agent/adapter/workflow/executor.go:88-100`, `internal/agent/adapter/workflow/rag_executor.go:173-198`). Their actual provider calls still use the model captured in the executor (`internal/agent/adapter/workflow/executor.go:129-139`). Revision is provenance, not a model selector.
- Capture copies the request revision into both profile and ModelRun facts, but calls the generator's captured model (`internal/capture/profile/generator.go:170-186`, `internal/capture/profile/generator.go:224-229`, `internal/capture/profile/generator.go:451-461`).
- Organizing copies the execution revision into its generation and ModelRun facts, but calls its captured model (`internal/organizing/workflow/generation.go:283-302`, `internal/organizing/workflow/generation.go:359-365`, `internal/organizing/workflow/generation.go:402-408`).
- Retrieval provenance is separate from workflow attempt provenance. Source composition registers an immutable Embedding Version with `modelSettingsRevision` and freezes its ID into processing (`cmd/worker/main.go:2355-2387`); `EmbeddingVersion` stores that optional revision (`internal/retrieval/domain/embedding.go:38-51`). Reindex job arguments do not carry a model revision; the fixed Processor/EmbeddingVersion ID supplies the binding.
- Static mode intentionally passes nil workflow revision/instance while its runtime reports revision 0 (`cmd/worker/main_test.go:205-232`, `internal/workflow/adapter/river/runtime_worker_test.go:82-90`). A manager must preserve this distinction.

Critical provenance defect: Artifact's model-bound workflow receives `ExecutionContext`, but `createModelRun` omits `execution.ModelSettingsRevision`, and replay equality also omits it (`internal/artifact/workflow/executor.go:298-350`). This is the only production workflow `ModelRun` constructor found that fails to copy attempt revision; it must be corrected before revision-selected execution can be audited reliably.

For hot activation, revision can no longer be "startup metadata." It must identify the acquired generation. New work should acquire active generation first and claim with that lease's `{revision, instanceID}`. Duplicate delivery/recovery must resolve the persisted attempt binding and acquire that revision; blindly using the newly active revision would trigger the existing duplicate-binding conflict.

### Current lifecycle and cleanup support

No model-runtime cleanup contract exists:

- `Models`, `ModelRuntime`, Chat adapters, and Embedders expose no `Close`, `Shutdown`, or `Destroy` method. The code search found no model-side `CloseIdleConnections` call.
- Each production adapter creates an `http.Client`; when no client is supplied, it clones `http.DefaultTransport` and owns a dedicated `*http.Transport` (`internal/platform/models/model_transport.go:37-66`). Those idle connection pools survive until process exit today.
- Chat stores `authorization` as a long-lived Go string (`internal/platform/models/chat_http.go:86-95`, `internal/platform/models/chat_http.go:149-165`). OpenAI-compatible Embedding similarly constructs and stores a Bearer string (`internal/platform/models/embedding_openai.go:31-51`, `internal/platform/models/embedding_http.go:53-60`).
- Loader destroys decrypted `Secret` byte buffers after `Build` (`internal/modelsettings/runtime/loader.go:58-66`), but `secretValue` has already copied bytes to an immutable Go string (`internal/modelsettings/runtime/models.go:235-239`). Clearing the temporary config strings only drops references (`internal/modelsettings/runtime/models.go:262-268`); it cannot zero string backing memory retained by adapters.
- Connection tests build throwaway production adapters and never close their transports (`internal/modelsettings/runtime/models.go:200-229`). Candidate validation must either probe the candidate that will be promoted or close the failed/aborted candidate.

Therefore PRD R6 cannot be satisfied by a manager alone. `Models`/`ModelRuntime` need an idempotent owned-resource close path, adapters must expose transport ownership, and retirement must call it exactly once after the final lease. Strict credential zeroization is not achievable while authorization is retained as immutable Go strings; the implementation must either redesign credential retention or state the best-effort limitation accurately.

### Concurrency and quiescence patterns

- Immutable capability reads are tested concurrent-safe (`internal/platform/models/runtime_test.go:90-118`). The underlying `http.Client`/`Transport` are shared across calls, which is supported by the standard types.
- One River `RuntimeNodeWorker` is shared across concurrent jobs. Each Work creates its own attempt lease/heartbeat goroutine, but all jobs use the same frozen registry and model-bound executors (`internal/workflow/adapter/river/runtime_worker.go:104-197`).
- `RecordingChatModel` is created per ModelRun around the shared underlying Chat model (`internal/agent/adapter/workflow/executor.go:129-139`, `internal/agent/application/recording_chat.go:24-60`). A runtime generation lease must outlive the complete executor call, including recording/finalization.
- Hybrid search launches lexical and vector goroutines and waits for both (`internal/retrieval/application/search.go:154-190`). An embedding lease must cover the entire Search call, not one adapter method.
- API drain uses one atomic mutation gate. `IsQuiesced` becomes true immediately after the gate flips and GET/HEAD/OPTIONS continue (`cmd/api/model_runtime_gate.go:20-75`). Semantic retrieval is commonly a GET, so current API quiescence does not prove no old model user exists.
- Worker drain disables periodic producers, pauses River, stops the reindex dispatcher, and waits for database `RunningJobCount == 0` (`cmd/worker/model_runtime_drain.go:69-115`). However the direct Git Sync loop checks the gate only before `dispatchGitSync`; an already-running `RunOnce` batch is not included in that count (`cmd/worker/main.go:761-803`, `cmd/worker/main.go:806-857`). It can retain `SourceRefresher`/Embedding after drain reports quiesced.
- Current Controller state mutation runs serially in one polling goroutine (`internal/modelsettings/runtime/controller.go:95-127`). Its fields are not designed for concurrent acquisition/promotion; only `Active` closure is guarded by `sync.Once` (`internal/modelsettings/runtime/controller.go:45-59`, `internal/modelsettings/runtime/controller.go:213-215`).

Reference-counted runtime leases provide the missing local fact: "how many complete operations still hold revision N." They should replace assumptions based only on producer gates or River counts for model resource retirement. Existing gates remain useful for controlling new work during commit.

### Persisted rollout topology that assumes process replacement

- Runtime phases only allow `active -> quiescing -> quiesced` for the old owner and `prepared -> verifying` for a candidate (`internal/modelsettings/domain/rollout.go:101-133`).
- `ops.model_settings_runtime` has `role text PRIMARY KEY`, so API and Worker each have only one persisted owner slot (`migrations/00064_model_settings.sql:228-244`). Registration uses `ON CONFLICT(role)` to replace the instance/revision (`internal/modelsettings/adapter/postgres/runtime.go:30-48`). It cannot represent active and candidate owners simultaneously.
- Candidate registration is authorized only during applying/verifying (`internal/modelsettings/adapter/postgres/runtime.go:175-205`). The existing schema therefore cannot publish "target prepared while old active remains registered" without either candidate columns/a second slot or a separate preparation fact.
- The coordinator waits for old Worker quiescence before entering applying, then waits for candidates (`internal/modelsettings/application/coordinator.go:283-324`). This is the old-container/new-container handoff, not candidate preload beside active work.
- Commit atomically publishes active and changes candidate rows to active (`internal/modelsettings/adapter/postgres/rollout.go:151-198`), then the coordinator resumes the queue immediately (`internal/modelsettings/application/coordinator.go:327-352`). There is no acknowledgement that each in-process manager has promoted its local pointer before producers resume.

The narrowest persistence-compatible option is to keep candidate construction process-local while the old row remains active, then register the already-built candidate when the rollout reaches applying. This avoids requiring simultaneous active/candidate rows, but it cannot expose durable per-role "prepared" status before applying. If the product requires durable preparation acknowledgement while old active continues accepting work, the runtime projection needs a second candidate slot (columns, rows keyed by role+slot/instance, or a separate preparation table).

The old runtime may remain process-local and retired after candidate registration replaces its row; already claimed work uses its workflow attempt lease, not a new model-runtime claim. The existing `Controller` cannot do this because heartbeat ownership loss returns an error and terminates the process (`internal/modelsettings/runtime/controller.go:217-230`, `cmd/api/main.go:671-673`, `cmd/worker/main.go:570-574`). Normal generation retirement must be separated from genuine process ownership loss.

### Narrowest safe in-process manager seam

Keep `Models` immutable. Add a concurrency-safe generation owner in `internal/modelsettings/runtime` with semantics equivalent to:

```go
type Lease interface {
    Models() *Models
    Revision() int64
    InstanceID() *foundation.ID // nil in static mode
    Release()                    // idempotent
}

type Manager interface {
    Prepare(ctx context.Context, rolloutID foundation.ID, revision int64) error
    AcquireActive(ctx context.Context) (Lease, error)
    AcquireRevision(ctx context.Context, revision int64) (Lease, error)
    Promote(ctx context.Context, rolloutID foundation.ID, revision int64) error
    Abort(ctx context.Context, rolloutID foundation.ID) error
}
```

Required internal behavior:

- One immutable entry per revision/generation with state `candidate`, `active`, or `retired`, its runtime instance ID, reference count, and `sync.Once` cleanup.
- An atomic active-entry pointer for the high-frequency acquisition path; a mutex protects prepare/promote/abort/retire maps and reference transitions. Do not hold the manager lock across Provider I/O.
- Candidate is never visible to ordinary consumers. Promote swaps one pointer only after the database commit is observed/authorized. The previous active entry becomes retired but remains addressable by revision until all local holders and required durable old-attempt pins are gone.
- Failed/aborted candidates close immediately after their last preflight holder. Active/retired cleanup closes owned transports exactly once.
- Static mode remains one immutable revision-0 entry with no database instance binding and no rollout watcher.

Do not implement a globally mutable ChatModel/Embedder whose individual methods read the active pointer. That permits contract/model/revision splits and violates the single-operation invariant. Acquisition must happen at the following complete work boundaries:

| Boundary | Required lease behavior |
|---|---|
| API semantic/hybrid/organizing search | Acquire once after loading the active index (or acquire the revision/contract compatible with that index), hold through `Contract`, `Embed`, vector search, and both hybrid goroutines, release when Search returns. |
| API capability checks and commands | Read a live active snapshot at request/command time. Register routes, workflow contracts, and definitions independently of current Chat availability; return stable capability-unavailable only at execution/start boundary. |
| Workflow River Work | Acquire the generation used for Claim, claim with its revision/instance, resolve a revision-bound executor bundle, and hold through Execute, heartbeat completion, and terminal settlement. For duplicate delivery, select the persisted attempt revision rather than current active. |
| Agent/RAG/Capture/Artifact/Organizing | Build model, contract, catalog, tool/search dependencies, and executor together as a per-generation bundle. A stable executor registered in the frozen workflow registry selects the bundle using the Work lease. |
| Git Sync/source refresh | Acquire one source-processing bundle before `RunOnce`/refresh and hold through all vector building. Count this lease so drain cannot report quiesced early. |
| Reindex River Work | Acquire the processor/EmbeddingVersion bundle for the delivery and hold through `Process` and completion. Do not switch a `VectorBuilder`'s embedder mid-index. |

This leaves package ownership intact: `internal/modelsettings/runtime` owns generation lifetime and immutable `Models`; API/Worker composition owns revision-bound workflow/retrieval bundles and stable delegating facades. Putting workflow, agent, or retrieval construction inside the model-settings manager would create an overly broad module and likely dependency cycles.

### Embedding is a separate compatibility constraint

A simple active Embedder switch is unsafe even with leases:

- `SearchService` stores one `QueryEmbedder` (`internal/retrieval/application/search.go:65-84`). `embedQuery` calls `Contract()` and later `Embed()` as separate operations (`internal/retrieval/application/search.go:232-252`); a dynamic per-method proxy could cross revisions between those calls.
- The runtime contract must exactly match the active index's persisted Embedding Version (`internal/retrieval/domain/embedding_contract.go:103-124`). Incompatible provider/model/dimensions/normalization/distance/config changes therefore make existing semantic search fail until a compatible index exists.
- Revision itself is provenance, not vector compatibility: `SameEmbeddingContractBinding` deliberately ignores `ModelSettingsRevision` (`internal/retrieval/domain/embedding.go:84-102`). Chat-only, credential-only, or otherwise contract-identical revisions can search an old index safely; incompatible embedding contracts cannot.
- Hybrid fallback only accepts retryable dependency/retryable failures (`internal/retrieval/application/search.go:618-627`). A contract consistency error is not silently degradable.
- `VectorBuilder` captures one embedder and contract and verifies them against the persisted page before embedding (`internal/retrieval/application/vector_builder.go:25-68`, `internal/retrieval/application/vector_builder.go:79-125`, `internal/retrieval/application/vector_builder.go:160-173`). This is the correct per-index shape and should remain immutable.

The activation design must explicitly choose one of these policies for an incompatible Embedding contract: retain/select the old runtime by the active index contract until a new index activates; complete reindex before model activation; deliberately block/degrade semantic search during migration; or reject apply until a migration plan exists. "Swap the Embedder and reindex later" does not meet the PRD.

### Suggested verification focus for implementation planning

- Manager race tests: concurrent Acquire/Promote/Release, failed candidate, abort, exactly-once cleanup, static revision 0, no ABA between identical revisions and different instance IDs.
- Workflow tests: a job claimed before commit keeps old model; a job claimed after commit gets new model; duplicate delivery selects persisted old revision; Artifact ModelRun records revision.
- API tests: disabled-to-enabled changes capability without rebuilding Router; Search holds one generation across contract/embed; GET search counts as an old holder during promotion.
- Worker tests: River concurrency, direct Git Sync in-flight tracking, reindex/source processing cannot mix Embedding versions, old runtime closes only after the final holder.
- Cross-process integration: both roles prepare the same target, one-role failure leaves old active, commit/local promotion interruption recovers, queue cannot run against an unpromoted local generation, no API/Worker container restart.

## Related specs

- `.trellis/spec/backend/model-settings-runtime.md:16-22` - current one-runtime-per-process signature.
- `.trellis/spec/backend/model-settings-runtime.md:26-43` - desired/active/applied and attempt revision contracts.
- `.trellis/spec/backend/model-settings-runtime.md:77-100` - existing restart rollout and required test gates.
- `.trellis/spec/backend/config-loading.md` - static Env/YAML configuration compatibility remains relevant for revision 0.
- `.trellis/spec/guides/cross-layer-thinking-guide.md` - cross-layer contract/data-flow review guidance.

## External references

No external references were required. This topology and recommendation are based on repository code and project contracts. Go `http.Client`/`http.Transport` concurrency behavior is standard-library behavior, but implementation should still expose explicit ownership rather than relying on process exit.

## Caveats / Not Found

- No model adapter/runtime close or credential-destruction interface was found; exact retired-resource cleanup is a new contract, not an existing hook.
- The direct `*Models` caller inventory is exhaustive for production Go files found under `cmd/` and `internal/` via `rg`; tests were used only to confirm intended invariants.
- Artifact workflow provenance is currently incomplete (`internal/artifact/workflow/executor.go:298-350`). This is a concrete existing gap, not merely a hot-activation design preference.
- The persisted one-row-per-role projection cannot simultaneously describe old active and candidate prepared. Whether to accept process-local preloading or add a durable candidate slot is a product/rollout design decision.
- Retention of retired revisions for abandoned/retryable durable attempts depends on the unresolved maximum-retention policy in `.trellis/tasks/08-11-model-runtime-hot-activation/prd.md:50-54`. Reference counts alone cover in-process holders, not future redelivery after all local holders disappear.
