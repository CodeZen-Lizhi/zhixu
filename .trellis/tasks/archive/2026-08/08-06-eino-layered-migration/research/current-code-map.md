# Research: Current Code Map For Layered Eino Migration

- Query: Map the current AI/Agent/RAG/model invocation chain by module and classify each boundary as `建议替换为 Eino`, `保留自研(现状)`, or `可延后`.
- Scope: internal / mixed
- Date: 2026-08-06

> 状态：这是实施前代码快照。当前正式结果以 ADR-0019、`implement.md` 和
> `research/stage2-gate-and-stage3-stage4-decisions.md` 为准；Chat/Callback 与 Structured Output 短 Graph
> 已进入主模块，本文中的“future/not current authorization”不再描述当前代码。

## Findings

### 1. Executive conclusion

The production application currently has one project-owned model contract and one frozen process runtime. `internal/platform/models` owns provider transport and contract validation; `internal/agent/application` owns provider-neutral messages, versioned Prompt/Schema/Profile references, bounded structured calls, and Model Run/Call recording; RAG, Artifact, Capture, and Organizing own their business sequencing and finalization. API and Worker receive the same frozen Chat/Embedding capabilities from Composition Roots.

The practical migration boundary is therefore narrow:

- **建议替换为 Eino (conditional candidate):** provider-specific Chat transport internals, first the OpenAI-compatible Chat adapter. An Eino adapter may sit behind the existing `agentapplication.ChatModel` and `ChatContract`; it must preserve request/response limits, model identity checks, stable error classes, no hidden retry, and credential-safe behavior. This is a future implementation target, not current authorization.
- **保留自研(现状):** `ModelRuntime`, managed-settings/revision semantics, project interfaces, RuntimeCatalog, strict decoders, StructuredRunner budget semantics, RecordingChatModel, Model Run/Call repositories, retrieval/RRF/eligibility/citation, durable Workflow state, Human Task, Artifact/Proposal/finalization, and all composition/readiness/security boundaries.
- **可延后:** Eino Structured Output/Graph replacement for the short `StructuredRunner`/planner/reviewer seams; Eino Embedding/Retriever/Rerank adapters; streaming; ToolsNode/general tool loop; River/Node integration. The isolated PoC has not passed these gates.

This classification is constrained by ADR-0013: Eino may only be used at the Agent/Application short-flow edge or inside Adapter/Infrastructure; Domain, durable Workflow, Proposal/Approval, permission, and authorization must remain framework-free (`docs/architecture/adr/0013-eino-adoption-gate.md:9-16`). M2 explicitly concluded that Eino is not formally adopted and requires a new ADR plus a complete gate re-run before reconsideration (`docs/architecture/adr/0013-eino-adoption-gate.md:18-23`).

### 2. Current invocation chain

```text
managed/static config
    -> modelsettings runtime loader (desired/active/applied revision)
    -> one process-wide ModelRuntime
       -> ChatCapability {ChatModel + ChatContract}
       -> EmbeddingCapability {Embedder + EmbeddingContract}
    -> API and Worker composition roots
       -> project-owned Agent/Retrieval/Workflow ports
          -> RecordingChatModel (STARTED -> terminal ModelCall fact)
          -> StructuredRunner (INITIAL -> REPAIR -> REDUCED, max three responses)
          -> direct provider adapter
          -> strict decoder + domain validation
          -> atomic domain/workflow finalizer
```

Evidence for the runtime side:

- `internal/platform/models/runtime.go:126-170` constructs Chat and Embedding adapters once, stores only capability/contract state, and validates the embedding contract before exposing it.
- `internal/platform/models/runtime.go:173-187` exposes frozen Chat/Embedding capabilities.
- `internal/modelsettings/runtime/models.go:30-109` keeps one runtime plus revision and builds it through the production factory; `internal/modelsettings/runtime/models.go:137-169` overlays managed settings before validation.
- `cmd/api/main.go:192-250` loads managed or static settings once and scrubs credentials before the rest of API composition.
- `cmd/worker/main.go:326-364` does the equivalent for Worker; `cmd/worker/main.go:1032-1049` rejects more than one supplied frozen runtime.

Evidence for the call side:

- `internal/agent/application/chat.go:35-76` defines project-owned roles, request/response and a single-call `ChatModel`; the interface explicitly forbids internal retry or model switching.
- `internal/agent/application/catalog.go:41-74` defines strict output decoders and immutable runtime snapshots; `internal/agent/application/catalog.go:91-180` registers and freezes Prompt/Schema/Profile versions.
- `internal/agent/application/recording_chat.go:36-68` describes the recording wrapper; `internal/agent/application/recording_chat.go:87-141` persists `STARTED` before the provider call and CAS-completes the call, converting persistence uncertainty to manual recovery.
- `internal/agent/application/runner.go:88-110` makes `StructuredRunner` the sole owner of the three-phase budget; `internal/agent/application/runner.go:120-198` executes at most three calls, validates exact bytes, and fails after validation exhaustion.

The durable node shape is `frozen context -> retrieval/evidence checks -> Model Run -> RecordingChatModel -> StructuredRunner -> strict output -> atomic finalizer`, not a framework-owned graph. Artifact is explicit in `internal/artifact/workflow/executor.go:44-65` and `internal/artifact/workflow/executor.go:93-131`; Capture is explicit in `internal/capture/profile/generator.go:103-178` and `internal/capture/profile/generator.go:239-250`; Organizing is explicit in `internal/organizing/workflow/executor.go:202-275` and `internal/organizing/workflow/executor.go:305-417`.

### 3. Per-module mapping

| Module / boundary | Current role | Classification | Eino boundary and reason |
|---|---|---|---|
| `internal/platform/models/chat_openai.go`, `chat_http.go` | OpenAI-compatible `/v1/chat/completions`, JSON Schema request, HTTP limits, response/error validation | **建议替换为 Eino (first candidate, gated)** | Wrap an Eino OpenAI model inside the existing `ChatModel`; keep `ChatContract`, exact model/usage/finish/tool-call checks, timeout, redirect/SSRF policy, and stable errors in the project wrapper. Do not expose Eino types. |
| `internal/platform/models/embedding_openai.go`, `embedding_ollama.go`, `embedding_http.go` | Batch OpenAI/Ollama embedding transport, order restoration, dimensions/normalization/size validation | **可延后** | PoC has not instantiated Eino Embedding/Retriever/Rerank components. If revisited, replace only transport behind `retrievalapplication.Embedder` and preserve `EmbeddingContract` binding. |
| `internal/platform/models/runtime.go`, `chat_factory.go`, `embedding_factory.go` | One immutable capability runtime and provider selection | **保留自研(现状)** | Runtime identity, disabled semantics, adapter/contract inseparability, and one-instance sharing are project facts, not framework concerns. |
| `internal/modelsettings`, `internal/platform/config` | Managed desired/active/applied revisions, secret lifecycle, provider allowlist and static/managed loading | **保留自研(现状)** | Eino must not read config or secrets directly. Settings/revision and readiness behavior are specified in `.trellis/spec/backend/model-settings-runtime.md:16-43`. |
| `internal/agent/application/chat.go` | Provider-neutral message/request/response contract | **保留自研(现状)** | Stable seam for dual-run, rollback, tests, and domain isolation; Eino SDK types must terminate at the adapter. |
| `internal/agent/application/catalog.go`, workflow catalogs | Versioned Prompt/Schema/Profile registry and frozen snapshot | **保留自研(现状)** | Prompt/schema/profile refs are durable provenance and must remain project-owned; framework registries cannot become the source of truth. |
| `internal/agent/application/runner.go` | Bounded strict structured runner: INITIAL/REPAIR/REDUCED, byte-preserving decode, budgets | **可延后** | Candidate for an Eino Runnable/Graph only after Structured Output and finite-repair gates pass. Preserve exact three-response budget and decoder semantics during any experiment. |
| `internal/agent/application/recording_chat.go` | Model Call fact persistence and replay-safety wrapper | **保留自研(现状)** | Must remain outside Eino so every call is recorded before/after provider invocation and unknown persistence cannot look successful. |
| `internal/agent/application/query_plan.go`, `faithfulness.go`, `answer.go`, `citation.go` | One-call PLAN/REVIEW, citation/evidence publication gates | **保留自研(现状) / inner call may defer** | Keep server-owned `model_run_ref`, strict domain decoding, citation eligibility and publication policy. Only the inner provider invocation can be swapped later. |
| `internal/agent/application/rag.go` | Retrieval-first RAG sequence and rewrite drift/dedup policy | **保留自研(现状)** | Current sequence is a product invariant; replacing it with an Eino graph would move business policy and durable facts into a framework. |
| `internal/agent/adapter/retrieval`, `knowledge`, `workflow` | Maps project ports to retrieval/knowledge/durable workflow repositories | **保留自研(现状)** | These adapters preserve workspace/evidence/model-run bindings; Eino cannot own SQL, River, or replay semantics. |
| `internal/retrieval/application/embedding.go`, `search.go`, `rerank.go` | Embedder port, keyword/semantic/hybrid routing, RRF, degradation and optional rerank | **保留自研(outer pipeline) / Eino adapters可延后** | Keep SearchService, active-index binding, RRF/dedup and explicit degradation. There is an interface for rerank but no production reranker implementation found; there is no current adapter to replace. |
| `internal/retrieval/application/vector_builder.go`, `internal/retrieval/adapter/postgres` | Bounded provider batches, cache, version checks and atomic vector/index commits | **保留自研(现状)** | Durable projection and transaction boundaries cannot be delegated to Eino. |
| `internal/artifact/workflow` | Retrieval/evidence eligibility, Model Run, structured section generation and atomic Revision finalization | **保留自研(outer workflow) / generator inner call可延后** | Keep replay-first and finalizer ownership. A future Eino runner may implement only the short generation call behind `ChatModel`. |
| `internal/capture/profile`, `internal/capture/workflow` | Frozen source chunks -> labeled evidence -> profile revision/evidence and capability degradation | **保留自研(outer workflow) / StructuredRunner可延后** | Preserve profile provenance, append-only revision, old revision survival and atomic completion. |
| `internal/organizing/workflow` | Frozen material/template snapshot, bounded E/D labels, human approvals, Artifact/Proposal outputs | **保留自研(outer workflow) / generator inner call可延后** | Custom template allowlist, human task and proposal boundaries must remain project-owned. |
| `internal/tools`, Agent tool catalog / Tool Loop | Tool contracts, permission and durable execution infrastructure | **可延后** | Current RAG deliberately has no general model Tool Loop. Eino ToolsNode PoC passing does not authorize production permission/persistence replacement. |
| `cmd/api`, `cmd/worker` composition roots | Construct shared runtime and inject all adapters/ports | **保留自研(现状)** | Only change the factory implementation behind the existing interfaces after gates; do not let Eino construct business repositories or workflows. |
| `*_test.go`, `poc/eino` | Contract, integration, composition and isolated Eino gate evidence | **保留自研(现状) / extend before migration** | Existing project tests remain authoritative. Add adapter dual-run/contract tests; keep PoC isolated until all gates pass. |

### 4. Module details and code patterns

#### 4.1 Model runtime and provider adapters

`ModelRuntime` is deliberately immutable and process-wide. It validates config, constructs each enabled adapter once, extracts its project contract, and exposes disabled/configured capability state (`internal/platform/models/runtime.go:133-170`). The factories select only the currently supported providers (`internal/platform/models/chat_factory.go:11-24`; `internal/platform/models/embedding_factory.go:10-34`).

The Chat adapter already has the exact location where an Eino adapter could be introduced: it turns the project request into strict `response_format=json_schema`, makes one request, and then applies project response validation (`internal/platform/models/chat_openai.go:29-85`). It rejects model-version drift, wrong choice shape, refusals, tool calls, missing usage, and invalid token totals (`internal/platform/models/chat_openai.go:105-125`). The shared HTTP layer owns URL security, size limits, timeout, no redirect, and stable error classification (`internal/platform/models/chat_http.go:97-139`; `internal/platform/models/chat_http.go:142-169`; `internal/platform/models/chat_http.go:191-286`). Those checks must remain in a wrapper even if Eino performs the HTTP call.

Embedding has the same project-owned shape but a wider unresolved compatibility surface. OpenAI restores `data.index` order and validates the response (`internal/platform/models/embedding_openai.go:54-75`); Ollama uses native `/api/embed` and validates batch count/model (`internal/platform/models/embedding_ollama.go:49-66`). Shared transport limits and error mapping are in `internal/platform/models/embedding_http.go:77-120` and `internal/platform/models/embedding_http.go:160-253`. Because the PoC has no Eino component contract test for this path, this is deferred.

#### 4.2 Agent application and structured calls

The Agent layer owns a small provider-neutral contract. `ChatRequest` carries phase, Profile/Prompt/Schema refs, model identity, bounded messages, output schema, and token cap; `ChatModel` is one call with no hidden retry or model switch (`internal/agent/application/chat.go:47-76`). RuntimeCatalog freezes exact Prompt/Schema/Profile versions and decoders (`internal/agent/application/catalog.go:41-74`, `internal/agent/application/catalog.go:91-180`).

`StructuredRunner` is not a generic agent loop. It selects one frozen snapshot, runs exactly `INITIAL`, `REPAIR`, `REDUCED`, applies per-call and total byte/token/deadline budgets, requires decoder output bytes to equal the accepted response, and returns a terminal exhaustion error after three responses (`internal/agent/application/runner.go:120-198`). An Eino Graph may eventually implement this short flow, but replacing it before the Structured Output gate passes risks changing retries, schema versions, or byte-level acceptance.

`RecordingChatModel` is a separate persistence decorator. It validates and hashes the request, writes `STARTED`, calls the underlying model once, then CAS-completes success/failure facts; a completion persistence error returns manual recovery (`internal/agent/application/recording_chat.go:67-141`). This must wrap any future Eino-backed adapter, never be replaced by callback-only tracing.

Query Plan is intentionally one strict PLAN call with no repair/retry/reduced/tool loop (`internal/agent/application/query_plan.go:38-53`). It server-binds and verifies `model_run_ref` (`internal/agent/application/query_plan.go:63-107`). Faithfulness Review is another independent one-call seam (`internal/agent/application/faithfulness.go:46-71`). These are suitable for later adapter experiments, but their domain/publication contracts stay self-owned.

#### 4.3 RAG and retrieval

`RAGExecutor` owns the fixed retrieval-first sequence: validate scope, plan, retrieve, batch eligibility, derive topics, execute bounded answer, then citation/faithfulness/publication (`internal/agent/application/rag.go:152-172`; `internal/agent/application/rag.go:184-322`). Rewrite handling preserves tuple binding and deduplicates without changing the frozen scope (`internal/agent/application/rag.go:340-376`). The architecture document states that this is not a general model Tool Loop (`docs/architecture/agent-rag-architecture.md:290-297`) and fixes the sequence and three-call PLAN/ANSWER/REVIEW shape (`docs/architecture/agent-rag-architecture.md:299-309`).

Retrieval owns routing and degradation, not a model framework. SearchService can run keyword, semantic, or hybrid search; hybrid executes lexical/vector branches, fuses with project RRF, deduplicates, and optionally reranks (`internal/retrieval/application/search.go:65-113`; `internal/retrieval/application/search.go:154-217`). Query embedding must match the active embedding version and pass the project contract (`internal/retrieval/application/search.go:232-252`). Missing or failed rerank is an explicit degradation (`internal/retrieval/application/search.go:255-303`). The Reranker interface requires exact candidate coverage and unique IDs (`internal/retrieval/application/rerank.go:16-20`, `internal/retrieval/application/rerank.go:46-87`).

VectorBuilder is a durable application process: it loads a bounded page, batches provider calls outside DB transactions, verifies embedding-version binding, and atomically commits cache/projection terminal facts (`internal/retrieval/application/vector_builder.go:22-80`; `internal/retrieval/application/vector_builder.go:110-223`). This is a hard self-owned boundary.

#### 4.4 Artifact generation

Artifact Executor is explicitly retrieval-first and has no Tool Loop (`internal/artifact/workflow/executor.go:44-62`). It replays an existing final receipt before reading current state, loads frozen context, retrieves/open-batches evidence, checks eligibility, creates a Model Run, and atomically finalizes a Revision (`internal/artifact/workflow/executor.go:65-131`; `internal/artifact/workflow/executor.go:181-285`). The short generation portion wraps the model in RecordingChatModel and StructuredRunner, then maps strictly decoded output to a proposal (`internal/artifact/workflow/executor.go:361-403`). Only this inner call is a plausible later Eino candidate.

#### 4.5 Capture profile generation

Capture profile generation has independent capability degradation. It can construct an unavailable generator that persists `CAPABILITY_UNAVAILABLE` without calling a provider (`internal/capture/profile/generator.go:76-83`, `internal/capture/profile/generator.go:122-127`). A configured generator loads a frozen catalog/source snapshot, labels bounded chunks `E0001...`, prepares/binds a Model Run, wraps the provider with RecordingChatModel and StructuredRunner, and atomically completes Profile Revision/Evidence (`internal/capture/profile/generator.go:129-178`; `internal/capture/profile/generator.go:224-250`; `internal/capture/profile/generator.go:270-311`). The profile catalog explicitly forbids outside knowledge, invented identities, permissions, and tool requests (`internal/capture/profile/contract.go:88-102`). Keep all of that; only the short structured call may be experimented with later.

#### 4.6 Organizing generation and workflow

Organizing reloads and verifies the authoritative Workflow Run, binding, immutable Snapshot, and Template Revision before dispatching a node (`internal/organizing/workflow/executor.go:202-259`). It separates generation from human approval and Artifact/Proposal creation (`internal/organizing/workflow/executor.go:262-302`; `internal/organizing/workflow/executor.go:305-417`). Its catalog says model input is untrusted data, allows only server-provided E/D labels, forbids tools/permissions/scripts, and requires explicit GAPs (`internal/organizing/workflow/generation_catalog.go:73-124`). A future Eino short runner must not absorb these workflow or human-review boundaries.

#### 4.7 Composition roots and configuration

The API and Worker roots load one managed/static runtime, retain revision/readiness state, and scrub credentials before composition (`cmd/api/main.go:192-250`; `cmd/worker/main.go:326-364`). Worker composition rejects a runtime binding that does not match the frozen revision (`cmd/worker/main.go:1052-1065`). Artifact and Capture components receive the already-built `ChatModel`, `ChatContract`, and optional shared embedder; they do not construct providers themselves (`cmd/worker/main.go:2076-2114`; `cmd/worker/main.go:2135-2221`). This is the correct injection point for a future Eino-backed adapter, while preserving API/Worker parity.

### 5. Eino PoC and gate implications

The main `go.mod` contains no Eino dependency (`go.mod:1-3`). The isolated PoC pins `github.com/cloudwego/eino v0.9.12` and `github.com/cloudwego/eino-ext/components/model/openai v0.1.13` (`poc/eino/go.mod:1-7`). Its report says the current conclusion is **do not formally adopt Eino** (`poc/eino/report.md:5-11`). Chat/Graph, ToolsNode, Callback/Trace, error limits, and OpenAI extension compilation passed, but `WithTools` concurrency is partial and Streaming, Structured Output, Embedding/Retriever/Rerank, and River Node integration failed; real provider smoke was skipped (`poc/eino/report.md:15-28`).

The report lists the exact re-evaluation work: exercise Eino `schema.StreamReader`, connect Graph output to Structured Output and finite repair, implement Embedding/Retriever/Rerank adapter contract tests, complete River Node/retry/manual-recovery integration, and run credentialed provider smoke (`poc/eino/report.md:47-55`). Until those are closed, adding Eino to the main module would contradict the current ADR and remove the easy rollback path.

### 6. Test and migration evidence map

Existing project contracts should remain the acceptance oracle for any future adapter:

- Model transport/runtime: `internal/platform/models/chat_contract_test.go`, `chat_factory_test.go`, `embedding_contract_test.go`, `embedding_factory_test.go`, `model_transport_test.go`, `runtime_test.go`.
- Agent calls: `internal/agent/application/runner_test.go`, `recording_chat_test.go`, `query_plan_test.go`, `faithfulness_test.go`, `catalog_test.go`, `rag_test.go`; workflow/replay coverage under `internal/agent/adapter/workflow/*_test.go`.
- Retrieval: `internal/retrieval/application/embedding_test.go`, `search_test.go`, `rerank_test.go`, `vector_builder_test.go`, plus PostgreSQL search/vector integration tests.
- Artifact/Capture/Organizing: workflow executor, generator, repository, terminal, and composition tests under each module; representative files are `internal/artifact/workflow/executor_test.go`, `internal/capture/profile/generator_test.go`, `internal/capture/workflow/executor_test.go`, and `internal/organizing/workflow/generation_test.go`.
- Composition: `cmd/api/model_runtime_gate_test.go`, `cmd/api/*composition_test.go`, `cmd/worker/*composition*test.go`, and Worker RAG integration tests.
- Eino gate evidence: `poc/eino/chatgraph`, `streaming`, `structured`, `retrieval`, `tooltrace`, `nodeexec`, and `live` tests.

Recommended pre-production verification for a future Chat adapter:

1. Run the existing Chat contract/transport tests unchanged against the adapter wrapper, including model-version mismatch, strict JSON schema, refusal/tool-call rejection, byte limits, timeout, redirect and error classification.
2. Add a dual-run or deterministic replay test at the `ChatModel` boundary to compare project response/error/usage semantics, while keeping RecordingChatModel and Model Run/Call persistence in the path.
3. Repeat the PoC gates for streaming, structured output, provider smoke, and rollback before changing `go.mod` or Composition Root behavior.
4. Only after a new ADR passes should Embedding/Rerank, short Runner, or ToolsNode experiments be promoted beyond `poc/eino`.

### 7. Files found

| Path | One-line description |
|---|---|
| `internal/platform/models/runtime.go` | Immutable process-wide Chat/Embedding capability runtime. |
| `internal/platform/models/chat_factory.go` | Chat provider selection and adapter construction. |
| `internal/platform/models/chat_openai.go`, `chat_http.go` | OpenAI-compatible Chat transport, schema request, limits and response/error contract. |
| `internal/platform/models/embedding_factory.go` | Embedding provider selection. |
| `internal/platform/models/embedding_openai.go`, `embedding_ollama.go`, `embedding_http.go` | OpenAI/Ollama batch embedding transports and shared validation. |
| `internal/modelsettings/runtime/models.go` | Frozen runtime plus managed/static revision loading. |
| `internal/platform/config/config.go` | Provider enums, defaults and configuration validation. |
| `internal/agent/application/chat.go` | Project-owned ChatModel contract. |
| `internal/agent/application/catalog.go` | Versioned Prompt/Schema/Profile catalog and snapshots. |
| `internal/agent/application/runner.go` | Three-phase strict structured runner and budgets. |
| `internal/agent/application/recording_chat.go` | Model Call persistence decorator and replay-safety behavior. |
| `internal/agent/application/query_plan.go`, `faithfulness.go`, `answer.go`, `citation.go` | PLAN/REVIEW/publication gates and citation validation. |
| `internal/agent/application/rag.go` | Retrieval-first RAG sequence and rewrite policy. |
| `internal/agent/adapter/retrieval`, `knowledge`, `workflow` | Project adapters for retrieval, evidence and durable workflow execution. |
| `internal/retrieval/application/search.go`, `embedding.go`, `rerank.go`, `vector_builder.go` | Search routing, embedding/rerank ports and durable vector construction. |
| `internal/retrieval/adapter/postgres` | PostgreSQL/pgvector search and durable index state. |
| `internal/artifact/workflow/executor.go`, `catalog.go` | Artifact evidence loading, generation, strict output and finalization. |
| `internal/capture/profile/generator.go`, `contract.go` | Capture profile generation, evidence labels, degradation and strict catalog. |
| `internal/capture/workflow/executor.go` | Durable Capture stage orchestration and Profile port call. |
| `internal/organizing/workflow/generation.go`, `generation_catalog.go`, `executor.go` | Organizing generation, snapshots, human tasks and result ownership. |
| `cmd/api/main.go`, `cmd/worker/main.go` | Composition roots and shared runtime injection. |
| `poc/eino/go.mod`, `README.md`, `report.md` | Isolated Eino versions, scope and gate outcome. |
| `docs/architecture/adr/0013-eino-adoption-gate.md` | Framework placement and non-adoption decision. |
| `docs/architecture/agent-rag-architecture.md` | RAG sequence, no-general-tool-loop rule and Model Run facts. |
| `.trellis/spec/backend/index.md` | Backend boundaries and current M2 Eino status. |
| `.trellis/spec/backend/model-settings-runtime.md` | Frozen runtime/revision/security contract. |
| `.trellis/spec/backend/artifact-contract.md`, `capture-profile-contract.md`, `organizing-contract.md` | Durable generation, degradation and publication contracts. |

### 8. Related specs and architecture constraints

- Backend index requires Composition Roots to construct adapters, forbids domain dependencies on model SDKs, and records M2 as “do not formally adopt Eino” (`.trellis/spec/backend/index.md`, implementation boundary and M1/M2 facts).
- Model settings requires one immutable runtime shared by all API/Worker consumers and preserves desired/active/applied revision semantics (`.trellis/spec/backend/model-settings-runtime.md:16-43`).
- Adapter boundaries require project-owned interfaces and keep Workflow/Model Run facts outside provider/framework types (`docs/architecture/interfaces-and-adapters.md:75-88`).
- Artifact contract requires server-side citation rebuilding/verification, controlled generation, Model Run/output validation, and transactional finalization (`.trellis/spec/backend/artifact-contract.md:51-57`, `:98-100`).
- Capture contract requires independent stages, Keyword availability when Embedding is disabled, append-only Profile Revision/Evidence, and no fake success (`.trellis/spec/backend/capture-profile-contract.md:66-76`, `:93-97`).
- Organizing contract keeps bounded labelled inputs, Snapshot/template fences, human review, Artifact/Proposal ownership, and manual paths when models are unavailable (`.trellis/spec/backend/organizing-contract.md:52-80`, `:93-102`).
- RAG architecture fixes retrieval-first execution, citation/faithfulness gates, bounded three-response structured runs, and no arbitrary Tool Loop (`docs/architecture/agent-rag-architecture.md:299-322`, `:343-351`).

### 9. External references / versions

No external web source was used for this inventory. The only version evidence is the repository's isolated PoC: Go `1.25.4`, Eino `v0.9.12`, Eino OpenAI extension `v0.1.13`, and indirect ACL package `v0.1.17` (`poc/eino/go.mod:1-17`). The main module intentionally has no Eino dependency (`go.mod:1-3`).

## Caveats / Not Found

- This is read-only code mapping, not an implementation authorization or a new ADR. The recommendation “建议替换为 Eino” means “first candidate after all gates and a new decision record,” not “change production now.”
- No production Eino adapter exists in the main module. The PoC uses an isolated module, so provider behavior, streaming resource ownership, structured output integration, embedding/rerank compatibility, and River integration remain unverified at production boundaries.
- No non-test production Reranker implementation was found; only the project port/validation and optional SearchService seam are present. Therefore there is no concrete reranker adapter to migrate today.
- The current RAG path deliberately does not implement arbitrary model Tool Calling. A passing PoC ToolsNode test proves only isolated node behavior, not production permission, persistence, audit, or replay compatibility.
- External provider live smoke was skipped because explicit credentials were absent (`poc/eino/report.md:26-28`); network/provider latency and wire quirks are not covered by this inventory.
- Test names and file paths were enumerated from the current worktree; this report does not claim that every test was executed. No code, module manifest, spec, or planning artifact outside this research file was changed.
