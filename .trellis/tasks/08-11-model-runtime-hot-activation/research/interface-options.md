# Interface Option Comparison

## Framing

The current runtime is immutable and safe inside one generation, but API/Worker composition projects it into process-lifetime handlers, executors, search services, model catalogs and workflow claim bindings. The new module must therefore switch a complete generation at an operation boundary, not mutate an adapter or return a different model from each getter call.

The comparison below synthesizes the independent runtime-topology, durable-state-machine, in-flight/Embedding and UX research. It evaluates the public interface by depth, locality and caller burden; the PostgreSQL protocol remains an internal adapter of the selected module.

## Option A: Dynamic Capability Proxy

```text
Chat() -> current Chat adapter
Embedding() -> current Embedding adapter
Revision() -> current revision
```

- Strength: smallest mechanical change at a few existing getters.
- Weakness: `Contract()` and `Embed()` or two Chat calls in one Attempt may observe different generations; revision, adapter and persisted provenance can split.
- Weakness: existing long-lived executors, disabled-at-start registries and readiness booleans still do not rebuild when a capability becomes enabled.
- Weakness: no explicit holder lifetime exists, so old transports and credentials cannot be retired safely.
- Verdict: reject. The interface is shallow and makes cross-revision consistency every caller's responsibility.

## Option B: Explicit Lifecycle Controller

```text
Prepare(target)
Probe(target)
Arm(target)
Activate(target)
Acquire(revision)
Abort(target)
Retire(revision)
```

- Strength: exposes every rollout stage and supports unusual operational workflows.
- Strength: maps directly to the durable `preparing -> arming -> activating` protocol.
- Weakness: API composition, Worker composition and coordinator callers must reproduce legal transition order, commit-side recovery rules, cleanup and admission fencing.
- Weakness: tests need mocks for many lifecycle methods and can pass while production callers omit one compensating action.
- Weakness: PostgreSQL phases and implementation mechanics become public application vocabulary.
- Verdict: viable as an internal implementation seam, but reject as the normal consumer interface.

## Option C: Generation-Scoped Runtime Host

```text
Run(ctx)
Acquire(ctx, Current | Frozen(binding) | Compatible(embeddingContract)) -> Lease
```

- Strength: a lease returns revision, runtime instance/generation, Chat/Embedding contracts and role-specific dependency graph as one immutable value.
- Strength: `Run` hides build/probe, participant heartbeats, admission fence, publish/finalize recovery, reference counting and retirement.
- Strength: common callers have one safe operation: acquire once, defer release, execute the complete request/Attempt/index operation.
- Strength: API and Worker use different role payload factories without making `modelsettings/runtime` depend on workflow, agent or retrieval packages.
- Cost: model-dependent objects captured at process startup must move behind generation factories or stable delegating executors.
- Cost: Workflow Claim must select and freeze the serving binding transactionally; it can no longer trust fields frozen at Worker startup.
- Verdict: recommend. It is the deepest interface with the lowest recurring caller burden.

## Option D: Dedicated Runtime Service

```text
API/Worker -> remote model-runtime service -> Provider
```

- Strength: centralizes credentials, connection pools and revision routing outside API/Worker containers.
- Strength: process-local retirement and cross-process candidate duplication disappear.
- Weakness: adds a new availability boundary, deployment unit, remote protocol and potentially large streaming/embedding data path.
- Weakness: workflow Attempt and Index provenance still require request-scoped routing tokens; the durable cutover problem is moved rather than removed.
- Weakness: conflicts with current architecture and task scope without evidence that an in-process host cannot meet requirements.
- Verdict: keep out of scope; reconsider only if future provider isolation or centralized quota requirements justify a service.

## Recommendation

Choose Option C for consumers, while implementing the lifecycle steps from Option B privately inside `RuntimeHost.Run` and its PostgreSQL/runtime adapters.

The safe usage rule is uniform:

1. Determine the authority for the operation: current serving default, persisted Workflow Attempt binding, or persisted Index/Embedding Contract.
2. Acquire one immutable generation lease.
3. Execute the complete model-dependent operation without re-resolving current state.
4. Release the lease; retirement may proceed only after all holders are gone.

For new Workflow Attempts, the database Claim transaction is the authority that freezes the serving binding. For Retrieval, the active Index/Embedding Version is the authority. For ordinary API model work, the locally applied serving generation is the authority after the cross-process admission fence has reopened.

## Required Errors At The Public Boundary

- `MODEL_RUNTIME_SWITCHING`: short retryable admission fence.
- `MODEL_RUNTIME_REVISION_UNAVAILABLE`: exact historical revision cannot currently be acquired.
- `MODEL_RUNTIME_BINDING_MISMATCH`: persisted provenance and generation identity disagree.
- `MODEL_RUNTIME_EMBEDDING_CONTRACT_MISMATCH`: requested vector space is incompatible.
- `MODEL_RUNTIME_NOT_READY`: process has not installed the database-selected serving generation.

Prepare/probe/participant/CAS errors remain rollout diagnostics and do not leak as lifecycle calls that business consumers must orchestrate.
