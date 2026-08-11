# Research: Phase B RuntimeHost Implementation Audit

- Query: Audit the current code for a minimal `RuntimeHost`/generation/lease design, HTTP transport ownership and `Close` semantics, exact Phase B file changes, concurrency tests, and compatibility with `LoadSettings`/`Controller` while Phase A ports are changing.
- Scope: internal
- Date: 2026-08-11

## Findings

### Files Found

- `internal/modelsettings/runtime/models.go` - process-scoped `Models`, construction, validation, connection probing, and current secret conversion.
- `internal/modelsettings/runtime/loader.go` - static/managed bootstrap loading and the only current exact-revision build path.
- `internal/modelsettings/runtime/bootstrap.go` - composition bundle containing service, manager, repository, and loaded runtime.
- `internal/modelsettings/runtime/controller.go` - legacy process registration, candidate heartbeat, drain, and restart-oriented ownership loop.
- `internal/platform/models/runtime.go` - immutable chat/embedding runtime without lifecycle support.
- `internal/platform/models/model_transport.go` - hardened HTTP client construction, including implicit `http.Transport` cloning.
- `internal/platform/models/chat_http.go` - chat HTTP config and request path.
- `internal/platform/models/embedding_http.go` - embedding HTTP config and request path.
- `internal/platform/models/chat_openai.go` - concrete OpenAI-compatible chat adapter.
- `internal/platform/models/embedding_openai.go` - concrete OpenAI-compatible embedding adapter.
- `internal/platform/models/embedding_ollama.go` - concrete Ollama embedding adapter.
- `internal/modelsettings/application/ports.go` - current Phase A-facing loader/controller contracts; this file is concurrently changing and is not a Phase B edit target.

### Current Runtime and Ownership Gaps

`runtime.Models` is a fixed process object containing one `ModelRuntime` and one revision (`internal/modelsettings/runtime/models.go:28-55`). `Build` creates that object once (`internal/modelsettings/runtime/models.go:89-107`), so every current consumer implicitly treats the pointer as process-single and immutable.

The validator creates a temporary platform runtime and drops it without cleanup (`internal/modelsettings/runtime/models.go:79-86`). The connection tester similarly builds a temporary `Models`, probes it, and returns without closing it (`internal/modelsettings/runtime/models.go:200-233`). Adding lifecycle support only to long-lived generations would therefore leave two resource leaks.

`platform/models.ModelRuntime` holds immutable chat/embedding capabilities but has no `Close` method (`internal/platform/models/runtime.go:126-197`). Construction creates chat before embedding, and an embedding-construction error after chat succeeds has no compensation path (`internal/platform/models/runtime.go:133-170`). Existing concurrency coverage proves immutable access, not close-versus-use behavior (`internal/platform/models/runtime_test.go:90-118`).

`newModelHTTPClient` returns only `*http.Client`, erasing ownership information (`internal/platform/models/model_transport.go:37-40`). It shallow-copies a caller-supplied client (`internal/platform/models/model_transport.go:48-51`); when the copied client has no transport, it installs a newly cloned default transport (`internal/platform/models/model_transport.go:52-62`). Redirect hardening is applied to the copy (`internal/platform/models/model_transport.go:63-66`). Consequently there are three distinct cases:

1. No supplied client: the package-created cloned transport is generation-owned.
2. Supplied client with `Transport == nil`: the returned copy receives a package-created transport, while the original client remains unchanged; that clone is generation-owned.
3. Supplied client with a non-nil transport: the returned copy shares the externally owned `RoundTripper`; model runtime cleanup must not close it.

Calling `http.Client.CloseIdleConnections` unconditionally would violate case 3 because it delegates to the configured transport. Ownership must be retained explicitly from construction onward.

Chat and embedding configs currently store raw `*http.Client` values (`internal/platform/models/chat_http.go:86-95`, `internal/platform/models/embedding_http.go:53-60`), construct them inside configuration (`internal/platform/models/chat_http.go:149-165`, `internal/platform/models/embedding_http.go:110-121`), and call `Do` directly (`internal/platform/models/chat_http.go:247-276`, `internal/platform/models/embedding_http.go:168-198`). These are the narrow points where an ownership-aware private client can be introduced without changing public provider contracts.

### Minimal Phase B API

Keep the consumer-facing seam limited to the already designed acquisition contract:

```go
type RuntimeAcquirer[T any] interface {
    Acquire(context.Context, RuntimeTarget) (RuntimeLease[T], error)
}

type RuntimeLease[T any] interface {
    Binding() RuntimeBinding
    Value() T
    Release()
}

type RuntimeHost[T any] struct { /* private state */ }

func NewRuntimeHost[T any](RuntimeHostOptions[T]) (*RuntimeHost[T], error)
func (h *RuntimeHost[T]) Run(context.Context) error
func (h *RuntimeHost[T]) Acquire(context.Context, RuntimeTarget) (RuntimeLease[T], error)
```

`RuntimeTarget` should be a sealed value with constructors for current active runtime, a claimed-attempt binding, and an exact positive revision. Callers must not construct arbitrary target states. `RuntimeBinding` should contain mode, role, revision, and process instance ID; in managed mode the instance ID is host/process-level and stays stable across generations (`.trellis/tasks/08-11-model-runtime-hot-activation/design.md:57-77`).

Use an interface for the lease, backed by a pointer concrete type whose `Release` is idempotent. A value lease containing `sync.Once` is copyable and can double-release. Consumers receive only `RuntimeAcquirer[T]`, not the concrete host or its control/factory dependencies.

The host constructor needs an explicit initial-value handoff because `LoadSettings` already built the initial model runtime and destroyed its resolved secret buffers. A narrow option is:

```go
type InitialRuntime[T any] struct {
    Binding RuntimeBinding
    Value   T
}
```

Document the ownership boundary: on successful `NewRuntimeHost`, the host owns `InitialRuntime.Value`; on constructor error, the caller retains it and must close it. Validate all non-resource options before accepting that ownership.

Generation preparation needs one injected composition seam, not new methods on every consumer:

```go
type GenerationFactory[T any] interface {
    Build(context.Context, domain.ResolvedSettings, RuntimeBinding) (T, error)
    Probe(context.Context, T) error
    Close(context.Context, T) error
}
```

The host loads a resolved revision through the final Phase A port, defers `ResolvedSettings.Secret.Destroy`, invokes `Build`, runs a fresh `Probe` for every rollout attempt, and owns successful cleanup. The factory must compensate any resources created before returning a build error. Role-specific factories belong in API/Worker composition packages; `internal/modelsettings/runtime` must not import Agent, Workflow, or Retrieval packages (`.trellis/tasks/08-11-model-runtime-hot-activation/design.md:99-106`).

Do not expose `Prepare`, `Arm`, `Activate`, `Abort`, refcounts, or generation state. Those are host internals driven by an injected Phase A control adapter and tested through `Run`/`Acquire`. This keeps the interface deep: a small stable acquisition API hides rollout orchestration, concurrency, history, and cleanup.

No additional public `Ready` or test-only lifecycle method is required. `Run` owns managed registration and state following; `Acquire` waits context-sensitively for initial readiness or returns the host's terminal error. Composition may start `Run`, perform and immediately release one current acquisition, then expose HTTP/queue work. Static mode is ready immediately and remains revision `0`.

### Generation and Concurrency Design

Each private generation should contain immutable binding/value data plus host-lock-protected admission state, reference count, pin/state, and cleanup status. The host owns the active reference, optional candidate association by rollout ID, revision lookup, in-flight-build deduplication, and a bounded historical cache. Active and candidate generations are pinned; only zero-reference historical generations are evictable.

A naive atomic active pointer followed by `refs.Add(1)` is unsafe: retirement can observe zero references and close the generation after the pointer load but before the increment. The narrow, auditable implementation is one short host mutex covering generation selection, admission-state check, and refcount increment; promotion/retirement and release bookkeeping use the same mutex. Provider I/O, revision loading, build, probe, and close must always run outside it.

The activation gate blocks only new acquisition critical sections, not lease lifetimes. Existing leases continue through activation, matching the no-full-drain requirement (`.trellis/tasks/08-11-model-runtime-hot-activation/design.md:108-118`). A plain `sync.RWMutex.Lock` is not context-aware; implement a private gate using a mutex plus state/wait channels (or an equivalent context-aware primitive). The simplest safe Phase B policy gates all target kinds during the short arming/activation interval. A worker that already claimed an attempt can pause between claim and acquisition, but its persisted binding remains exact and cannot silently switch revisions.

Release atomically decrements once and only enqueues cleanup; it must not perform blocking close under the host mutex. Marking a generation closing/closed prevents any new acquisition. Activation swaps the active generation under the gate, marks the old one retiring, and permits new current acquisitions before waiting for old leases. Candidate failure or abort closes only the candidate and leaves active untouched.

Concurrent exact-revision reconstruction must deduplicate by revision. `golang.org/x/sync` already exists as an indirect dependency (`go.mod:97`), so `singleflight` is an available mature primitive. If used, a shared build must derive from the host lifetime plus a bounded preparation timeout, not the first caller's cancelable context; each waiter can cancel independently through `DoChan`. Making this import direct is expected if production code uses it.

Cleanup needs more state than a plain `sync.Once` if `GenerationFactory.Close` can fail. Permit only one close attempt at a time; mark success permanently, but retain a failed generation and retry from the host cleanup loop. Platform model cleanup should itself be idempotent and effectively non-failing, while role factories can aggregate multiple cleanup errors.

`Run` should be single-use. True process-ownership loss closes admission and becomes a terminal error, consistent with the current controller behavior (`internal/modelsettings/runtime/controller.go:217-230`). On context cancellation it should fence admission, abort the candidate, retire local generations, and close zero-reference resources. It must have a configured shutdown cleanup timeout and return/report outstanding leases rather than wait forever. Process composition must stop accepting work and cancel/wait request/job contexts before expecting all leases to drain.

### HTTP Transport Ownership and Close

Introduce a private deep transport wrapper in `model_transport.go`:

```go
type idleConnectionCloser interface {
    CloseIdleConnections()
}

type modelHTTPClient struct {
    client    *http.Client
    owned     idleConnectionCloser // nil for caller-owned transports
    closeOnce sync.Once
}

func (c *modelHTTPClient) Do(*http.Request) (*http.Response, error)
func (c *modelHTTPClient) Close() error
```

`newModelHTTPClient` should return `*modelHTTPClient`. Set `owned` only when the package creates the cloned `*http.Transport`; leave it nil when a caller supplied a non-nil `Transport`. This records the decision once at the construction boundary and is safer than returning a loosely associated `(*http.Client, cleanup func, error)` tuple.

Change the private chat/embedding HTTP config fields to `*modelHTTPClient`. Concrete adapters should implement an unexported package lifecycle interface, for example `closeModelResource() error`, delegating to the config client's idempotent `Close`. `ModelRuntime` recognizes/stores those private closers and exposes `Close() error`; `runtime.Models.Close() error` delegates to it. Do not add `Close` to the broad Agent chat-model or Retrieval embedder interfaces because those consumers borrow capabilities and do not own their transport.

`NewConfiguredModelRuntime` must compensate partial construction: once chat succeeds, defer cleanup until embedding also succeeds and the runtime is committed. Validator and connection-tester paths must defer `Close` for their temporary successful builds (`internal/modelsettings/runtime/models.go:79-86`, `internal/modelsettings/runtime/models.go:200-233`). Normal `Build`/`LoadSettings` transfers ownership to its caller or the host.

Closing idle transports is safe only after the lease count proves no provider call is in flight and the generation no longer admits work. Cleanup should not mutate adapter/runtime fields, because getters may still race in buggy callers; it should close only the owned connection pool and release references once the generation itself becomes unreachable.

Resolved secret byte buffers are explicitly destroyed after load (`internal/modelsettings/runtime/loader.go:65-66`), but model construction converts credentials to immutable Go strings (`internal/modelsettings/runtime/models.go:235-239`) and HTTP authorization values remain strings (`internal/platform/models/chat_http.go:153-165`, `internal/platform/models/embedding_openai.go:41-46`). `Close` can release references for garbage collection, but cannot guarantee hard zeroization of those strings. Tests should assert canaries never appear in `String`, `GoString`, logs, or returned errors, not claim memory erasure.

### Exact Phase B File Changes

New files:

- `internal/modelsettings/runtime/host.go` - public constructor, `Run`, `Acquire`, target/binding/acquirer/lease contracts, Phase A adapter wiring, readiness and terminal-state behavior.
- `internal/modelsettings/runtime/generation.go` - private generation state, lease implementation, refcount, retirement/history, build deduplication, and cleanup state machine.
- `internal/modelsettings/runtime/admission.go` - optional but justified if the context-aware gate is non-trivial; keep it private and separately testable.
- `internal/modelsettings/runtime/host_test.go` - fake Phase A control source and fake factories exercising behavior only through `Run`/`Acquire`.

Modified files:

- `internal/modelsettings/runtime/models.go` and `models_test.go` - add idempotent ownership delegation, partial/temporary cleanup tests, and preserve existing immutable accessors.
- `internal/modelsettings/runtime/loader.go` and `loader_test.go` - only extract/reuse an exact-revision resolved-load/build helper if the host needs it; retain current `LoadSettings` signature and behavior.
- `internal/platform/models/model_transport.go` and `model_transport_test.go` - retain transport ownership and close only package-created clones.
- `internal/platform/models/runtime.go` and `runtime_test.go` - aggregate resource closers, close idempotently, and compensate partial construction.
- `internal/platform/models/chat_http.go` and `embedding_http.go` - use the ownership-aware private client.
- `internal/platform/models/chat_openai.go`, `embedding_openai.go`, and `embedding_ollama.go` - implement private resource cleanup delegation.

Keep these files untouched in Phase B core work:

- `internal/modelsettings/runtime/bootstrap.go` - do not genericize the shared bootstrap bundle; composition builds a role-specific host from `bootstrap.Loaded` plus the final Phase A port.
- `internal/modelsettings/runtime/controller.go` - retain as the legacy restart/recovery adapter until the composition migration phase.
- `internal/modelsettings/application/ports.go`, domain, and Postgres files - Phase A owns these contracts and is actively changing them.
- Agent/Workflow/Retrieval consumers and API/Worker main wiring - dependency-graph migration belongs to the later consumer/composition phases. Thin role factory definitions may be added in composition-owned files once their payload boundaries are final, but the runtime package must not import those modules.

### Concurrency and Close Test Matrix

Host tests should use a fake final Phase A adapter and a fake generation factory. Avoid exporting activation operations merely to make tests convenient.

1. Static startup returns current revision `0`; managed startup waits until registration/readiness and returns the loaded active revision.
2. Preparing a candidate builds and freshly probes it while `CurrentRuntime` still resolves to the old active generation.
3. Build failure, probe failure, and preparation timeout close the partial candidate exactly once and never disturb active.
4. Arming closes the context-aware admission gate for new acquires without waiting for an already-held lease; a waiting acquire can cancel.
5. Activation makes new current acquires observe the new binding while an old lease continues to return its old value; old resources close only after final release.
6. Calling `Release` repeatedly and concurrently decrements exactly once.
7. At least 64 concurrent acquire/release loops across repeated activation pass `go test -race` without a closed-value acquisition.
8. An orchestrated close-versus-acquire race proves selection plus refcount increment is atomic and no generation is acquired after entering closing state.
9. Concurrent exact-revision acquisitions build a missing historical revision once; individual canceled waiters do not cancel the shared build.
10. Current, attempt-bound, and exact-revision targets return their exact bindings; malformed/zero/foreign-role attempt bindings fail closed.
11. Abort, duplicate snapshots, duplicate activation, and stale/out-of-order control transitions are idempotent.
12. Cleanup failure leaves the generation retained for retry; subsequent success is recorded once and never closes again.
13. `Run` cancellation with zero refs closes immediately; cancellation with an outstanding lease obeys the configured timeout and closes after later release/retry rather than blocking forever.
14. Host, binding, target, errors, and fake observability output never contain API-key or full-endpoint canaries.

Transport/runtime tests:

1. A package-created cloned transport receives `CloseIdleConnections` exactly once under concurrent `ModelRuntime.Close`/`Models.Close` calls.
2. A caller-supplied custom transport is never closed by model cleanup.
3. A supplied client with nil transport receives an owned clone in the copy, while the original client remains unchanged.
4. A runtime containing chat and embedding closes both owned transports; a disabled runtime close is a no-op.
5. Chat success followed by embedding-construction failure closes the chat owner. An unexported builder seam is acceptable for deterministic failure injection; no public test hook is needed.
6. Validator and connection-tester temporary runtimes always close on success and error paths.
7. A local HTTP server test may prove idle connections are released after the last lease; no active request is interrupted because close is generation-refcount gated.
8. Tests use `t.Cleanup` for every successful runtime build and assert secret/full-endpoint canaries never appear in formatted values or errors.

### Compatibility Migration

`LoadSettings` currently preserves static revision `0`, selects a managed bootstrap revision (including the existing prepared-rollout environment path), loads it, destroys the resolved secret, and builds `Models` (`internal/modelsettings/runtime/loader.go:27-35`, `internal/modelsettings/runtime/loader.go:39-81`). Preserve that API and behavior in Phase B. The hot host should adopt `Loaded.Models` as its initial role generation so startup does not duplicate provider construction. The existing rollout/prepared environment variables remain restart compatibility inputs; they should not become the in-process activation protocol.

`Bootstrap` packages dependencies and calls `LoadSettings` once (`internal/modelsettings/runtime/bootstrap.go:19-57`). Keep it non-generic. API/Worker composition can create the typed host from `bootstrap.Service` (or the final narrow Phase A control port), `bootstrap.Loaded`, and a role-specific factory. Explicit ownership transfer prevents both leaks and double close when host construction fails.

The current `Controller` freezes role, instance, revision, and phase at construction (`internal/modelsettings/runtime/controller.go:34-82`), then owns process registration, polling, heartbeat, candidate drain, and restart transitions (`internal/modelsettings/runtime/controller.go:95-210`). Do not evolve it into a generic runtime host: that would mix legacy process orchestration with typed generation ownership. Keep it temporarily for restart recovery/compatibility, then make composition select `RuntimeHost` for managed hot mode in the later migration phase.

Never run the legacy `Controller` and new `RuntimeHost` simultaneously for the same managed role/process identity; both would register and heartbeat the same single-owner control record. During migration, one composition branch owns registration. Static mode can use the host with revision `0` and no watcher, or retain direct immutable models until its consumer migration, but must not query managed rollout state.

Phase A's domain/application/Postgres surface is being modified concurrently. Phase B should depend on one narrow injected control/read adapter after that contract settles and should not hard-code today's `RevisionLoader`/`RuntimeController` method names (`internal/modelsettings/application/ports.go:194-206`). Host tests should fake that adapter, making later Phase A naming changes local to a constructor adapter rather than generation logic.

## External References

- Go standard library `net/http`: `Client.CloseIdleConnections` delegates to the client's transport when it implements the idle-connection closer contract; therefore it does not itself encode ownership.
- `golang.org/x/sync/singleflight`: suitable for same-revision build suppression; the project already resolves `golang.org/x/sync v0.22.0` indirectly (`go.mod:97`).

No external dependency is required for the admission gate, leases, generation state, or cleanup ownership.

## Related Specs

- `.trellis/spec/backend/model-settings-runtime.md` - immutable revision semantics, lifecycle, readiness, and observability constraints.
- `.trellis/spec/backend/directory-structure.md` - package ownership and dependency-direction rules.
- `.trellis/spec/backend/config-loading.md` - startup/config loading conventions.
- `.trellis/tasks/08-11-model-runtime-hot-activation/design.md:57-118` - public acquisition seam, composition-owned factories, and generation lifecycle.
- `.trellis/tasks/08-11-model-runtime-hot-activation/design.md:176-213` - prepare/arming/activate protocol and admission behavior.
- `.trellis/tasks/08-11-model-runtime-hot-activation/implement.md:18-24` - Phase B host, generation, transport ownership, factories, and tests.
- `.trellis/tasks/08-11-model-runtime-hot-activation/implement.md:64-78` - expected file impact.
- `.trellis/tasks/08-11-model-runtime-hot-activation/implement.md:122-131` - unit/concurrency/race verification matrix.

## Caveats / Not Found

- No `RuntimeHost`, generation, admission-gate, or lease implementation exists in the current tree; Phase B is a new deep module, not completion of a partial implementation.
- Phase A's final runtime-control port and exact transition names were not stable during this audit. The proposed host isolates that uncertainty behind one injected adapter and intentionally does not prescribe edits to Phase A files.
- The exact role payload shapes and factories depend on the Phase C Agent/Workflow/Retrieval dependency migration. Phase B can finalize the generic host and platform cleanup first; composition-owned factories should not force premature consumer refactors.
- Graceful shutdown ordering is not fully specified by the existing design. Before implementation is complete, define the cleanup timeout and whether a timed-out `Run` returns a typed outstanding-lease error; do not silently wait forever or force-close resources still protected by leases.
- Go strings containing credentials cannot be hard-zeroized. Lifecycle work reduces lifetime and closes transports, but must not be documented as guaranteed in-memory secret erasure.
