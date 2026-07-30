# Research: Runtime Fence Audit

- Query: Audit W3/G3 against the current shared worktree: enumerate every Workflow/River producer, prove or disprove tx-scoped EnqueueFence coverage, map API/Worker model runtime construction and registration/drain lifecycle, identify attempt/index/model-run revision-binding gaps, and name the exact implementation and test files still needed.
- Scope: internal
- Date: 2026-07-30

## Findings

### Executive verdict

W3/G3 is only partially complete.

1. The shared transactional insertion helper is correct: it checks `EnqueueFence` in the caller's `pgx.Tx` before River `InsertTx` (`internal/workflow/adapter/river/inserter.go:115-147`), and the PostgreSQL fence locks the singleton rollout row with `FOR SHARE` (`internal/modelsettings/adapter/postgres/runtime.go:155-172`). The rollout mutation path locks the same row with `FOR UPDATE`, so a producer transaction either commits before draining or waits and is rejected after draining.
2. Every currently wired production producer found by source-wide `StartTx` / `InsertTx` / direct River insert searches reaches that helper in managed mode. This includes HTTP workflow starts, state-machine successor/retry/resume jobs, the health scheduler, Export create/recovery, and Reindex first/retry dispatch.
3. The boundary is not closed: public `Client.Insert` bypasses the fence (`internal/workflow/adapter/river/client.go:91-100`), and `export/adapter/river.NewDispatcher` selects that bypass (`internal/export/adapter/river/dispatcher.go:35-40,67-86`). No current production composition calls the unsafe constructor, but the API remains available and can silently regress G3.
4. The non-transactional model-settings `CheckEnqueueAllowed` path is absent in the final worktree scan. This satisfies one explicit G3 item, but it changed concurrently during the audit and needs a compile/test gate.
5. API/Worker do not consume a single frozen Model Runtime. `LoadSettings` builds Chat/Embedding, then composition copies its `Config` and constructs adapters again. Current command composition has six direct factory calls: one in API plus five in Worker. `Models.Config` retains plaintext API-key strings after temporary `Secret` buffers are destroyed.
6. Worker correctly waits for controller `Active()` before starting Reindex/River consumers; API starts its HTTP server concurrently with registration and never waits for `Active()`. A candidate API can therefore serve before rollout commit, and an ordinary API can serve before runtime registration succeeds.
7. Worker drain only checks `RunningJobCount`; it does not stop/resume the Reindex background dispatcher. The dispatcher keeps polling and receives retryable fence errors during drain. The database invariant remains protected, but the required local producer shutdown is missing.
8. `workflow.node_attempt`, `retrieval.embedding_version` / `retrieval.index_version`, and `agent.model_run` persist no model-settings revision. Claim and executor context also carry no revision. The database cannot prove which runtime revision executed an attempt, built an index, or created a model run.

### Contract being audited

- All HTTP/background/retry/rescue producers must share a tx-scoped fence; no late job may commit after draining (`.trellis/tasks/07-30-dev-docker-model-settings/design.md:119-127`, especially `:122`).
- `RuntimeSession` order must be `Prepare -> role Registry -> Ready -> Run watcher` (`design.md:239-241`).
- API/Worker must each construct one immutable Chat/Embedding Runtime and inject it into role consumers (`design.md:262-280`).
- After draining commits, no new Workflow/River job may commit; started attempts retain their revision and queued jobs wait (`design.md:320-327`).
- Implementation explicitly requires deleting non-tx checks, a unified insertion helper, one configured Model Runtime, composition/lifecycle tests, and attempt revision binding (`implement.md:68-86`). G1/G3 gates are at `implement.md:194-211`.

### River insertion boundary

The canonical path is:

```text
producer-owned pgx.Tx
  -> TypedJobInserter.InsertTx
  -> EnqueueFence.CheckEnqueue(ctx, same tx)
  -> River client.InsertTx(ctx, same tx)
  -> producer commit
```

- `Client` retains the configured fence (`internal/workflow/adapter/river/client.go:43-52,76-88`).
- `NewTypedJobInserter` copies that fence (`internal/workflow/adapter/river/inserter.go:44-63`).
- `insertValidatedJobTx` rejects non-`pgx.Tx`, calls the fence at `:123-127`, then calls River at `:140` (`inserter.go:115-147`).
- The existing unit test proves only call ordering with fakes (`internal/workflow/adapter/river/inserter_test.go:227-250`). It does not prove the PostgreSQL lock race or actual River persistence.
- `CheckEnqueue` permits `idle`, `failed`, and `validating`; it rejects `draining`, `applying`, and `verifying` while holding `FOR SHARE` (`internal/modelsettings/adapter/postgres/runtime.go:155-172`). This matches the drain invariant.
- Rollout state changes use the same singleton row under `FOR UPDATE` before updating the phase (`internal/modelsettings/adapter/postgres/rollout.go:24-33,82-117`). PostgreSQL lock ordering closes the late-commit window.

Remaining bypass surface:

- `Client.Insert` performs direct non-transactional River insertion and never consults `fence` (`internal/workflow/adapter/river/client.go:91-100`).
- Export's `NewDispatcher` stores only that client and `Dispatch` uses `Client.Insert` (`internal/export/adapter/river/dispatcher.go:35-40,67-86`).
- Production API and Worker use `NewTransactionalDispatcher` (`cmd/api/main.go:808-826`; `cmd/worker/main.go:1007-1018`), so no current production callsite takes the bypass. The unsafe API should still be deleted or made test-only; otherwise G3 depends on composition discipline rather than the type boundary.
- The fence argument is optional and variadic in API/Worker construction; only `enqueueFences[0]` is used (`cmd/api/main.go:1168-1189`; `cmd/worker/main.go:719-731`). The current managed composition supplies exactly the model-settings repository, but an architecture test should lock this down.

### Complete producer inventory

Only three project River job kinds were found: Workflow node, Retrieval reindex, and Export. Source-wide production `InsertTx` search found no fourth kind or hidden raw River client.

| Producer / trigger | Persist + insert path | Fence status in current production composition |
| --- | --- | --- |
| Generic Workflow HTTP start | `internal/workflow/http/handler.go:252-282` -> `internal/workflow/application/start_runtime.go:163-168` -> own transaction in `internal/workflow/adapter/postgres/runtime_start.go:95-121`, insert at `:183-197` | Covered by API factory's fenced inserter (`cmd/api/main.go:1168-1189`) |
| Change-control approval HTTP | `internal/changecontrol/http/handler.go:680-710` -> `internal/changecontrol/adapter/approvaldispatchpostgres/repository.go:238-254`, `RuntimeRepository.StartTx` at `:247` | Covered; same approval transaction and shared API Runtime Repository |
| Conversation question HTTP | `internal/conversation/http/handler.go:285-310` -> `internal/conversation/adapter/postgres/dispatch.go:282-303`, `StartTx` at `:296` | Covered; question facts and River row share caller tx |
| Graph topic/smart candidate scan HTTP | `internal/graph/http/candidate_scan_handler.go:45-76` -> `internal/graph/adapter/postgres/scan_repository.go:145-175`, `StartTx` at `:164` | Covered |
| Health scan HTTP | `internal/health/http/handler.go:405-430` -> `internal/health/adapter/postgres/scan_repository.go:135-157`, `StartTx` at `:150` | Covered |
| Artifact section-generation HTTP | `internal/artifact/http/handler.go:365-406` -> `internal/artifact/adapter/postgres/generation.go:125-151`, `StartTx` at `:146` | Covered |
| Background health schedule | Worker loop `cmd/worker/main.go:437-443` -> `internal/health/application/schedule.go:128-155` -> the same health scan `StartTx` path | Covered by the Worker shared fenced inserter; local scheduler is not stopped during drain, so calls fail at DB fence rather than at a local gate |
| Workflow business retry | `internal/workflow/adapter/postgres/runtime_state.go:1059-1110`, scheduled River insert at `:1091-1097` | Covered; attempt transition and new River job use the same tx |
| Workflow DAG successor after executor completion | `activateSuccessors` at `runtime_state.go:1184-1265`, inserts existing/new successor at `:1232-1238` and `:1257-1263` | Covered |
| Workflow successor after human decision | `SubmitHuman` transaction at `runtime_state.go:651-745`, calls `activateSuccessors` at `:726-728` | Covered |
| Workflow resume / recovery generation | `Control` owns the transaction and invokes `resumeNodes`; inserts at `runtime_state.go:1417-1443` | Covered |
| Export create HTTP | `internal/export/http/handler.go:160-173` -> `internal/export/application/service.go:61-86` -> transactional Export dispatcher | Covered in API production. Business Export fact commits before dispatch by design; failed/blocked dispatch is represented by `DispatchPending` and recoverable |
| Export startup/periodic recovery | `internal/export/application/service.go:350-377`, called by Worker startup/maintenance (`cmd/worker/main.go:358-360,463-466`) | Covered by Worker's `NewTransactionalDispatcher` and shared fenced client (`cmd/worker/main.go:1007-1018`) |
| Reindex first background dispatch | Runner starts and polls at `internal/retrieval/runtime/dispatcher_runner.go:70-94,143-165`; first outbox path inserts at `internal/retrieval/adapter/postgres/dispatcher.go:85-145`, specifically `:126` | Covered via `retrieval/adapter/river.NewApplicationInserter`, which delegates to shared typed inserter (`internal/retrieval/adapter/river/inserter.go:25-69`) |
| Reindex retry background dispatch | `internal/retrieval/adapter/postgres/dispatcher.go:147-186`, insert at `:179` | Covered in the same delivery transition transaction |
| River built-in retry / stuck-job rescue | River is configured with `RescueStuckJobsAfter` (`internal/workflow/adapter/river/client.go:55-64,219-228`) | Not a new application enqueue: it reuses an existing River row. It must be governed by QueuePause/claim tests, not by a new `EnqueueFence` call |

Production composition proof:

- API appends the model-settings repository as the fence (`cmd/api/main.go:150-166`) and passes it to the shared workflow runtime (`cmd/api/main.go:297-326`) and Export (`cmd/api/main.go:808-826`).
- Worker does the same at bootstrap (`cmd/worker/main.go:251-272`), constructs one shared fenced insert client (`cmd/worker/main.go:719-731`), and passes that client to Workflow, Export, and Reindex (`cmd/worker/main.go:1007-1018,1039-1055`).
- `rg` found no production caller of Export `NewDispatcher` and no production direct `Client.Insert` outside its unsafe branch.

### Queue pause, heartbeat, drain, and ownership

What exists:

- Runtime registration authorizes active vs fixed target under the state-row lock, then claims one row per role (`internal/modelsettings/adapter/postgres/runtime.go:13-58`).
- Heartbeat is a CAS over role, instance, rollout, applied revision, runtime phase, and current rollout state (`runtime.go:61-93`). Ownership or authorization loss returns a stable runtime conflict.
- The schema stores `role`, `instance_id`, `applied_revision`, `rollout_id`, phase, applied time, and heartbeat; triggers constrain transitions (`migrations/00064_model_settings.sql:219-272`). Snapshot freshness defaults to 20 seconds (`internal/modelsettings/adapter/postgres/snapshot.go:12-23,76-105`).
- Controller registers before marking active, then polls/heartbeats (`internal/modelsettings/runtime/controller.go:95-125`). It drives active -> quiescing -> quiesced and candidate prepared -> verifying -> active (`controller.go:129-207`).
- `modelctl drain` advances state to draining before QueuePause (`cmd/modelctl/control.go:231-242`); commit, abort, and recovery resume the queue (`control.go:169-176,279-299`). This ordering makes failure of QueuePause operationally bad but does not reopen enqueue, because the DB fence is already closed.
- QueuePause/Resume call River's persistent queue controls (`internal/workflow/adapter/river/client.go:184-203`).
- Worker builds all components, starts the controller, waits for `Active()`, and only then starts the dispatcher/River lifecycle (`cmd/worker/main.go:300-350`). The lifecycle starts Reindex dispatcher before River consumers (`cmd/worker/lifecycle.go:71-101`), but both remain stopped for a prepared candidate until commit.
- API builds handlers/router before starting the controller (`cmd/api/main.go:470-500`), so registration happens after assembly attempt.

Gaps:

- API also starts `ListenAndServe` immediately and does not select on `modelRuntimeController.Active()` (`cmd/api/main.go:491-509`). Candidate traffic-before-commit is therefore possible unless an external proxy happens to isolate the process; the in-process contract is not enforced.
- API starts the controller even when individual model-backed component construction was logged as unavailable and the server continued in degraded mode. Registration is not gated by one role-level `Ready` result.
- API provides no local producer gate / drain hooks. It moves to `quiescing` but continues accepting producer requests; correctness relies solely on transaction rejection. The design permits the DB fence as final boundary but explicitly asks for a fast local 503 gate (`design.md:122-124,257-260`).
- Worker controller supplies only `IsQuiesced = RunningJobCount == 0` (`cmd/worker/main.go:312-318`). `DrainHooks.Begin` and `Resume` are nil, so Reindex runner and maintenance producer loops are not stopped/resumed by the watcher.
- Reindex runner treats the fence's retryable `MODEL_SETTINGS_ENQUEUE_PAUSED` as transient and backs off (`internal/modelsettings/adapter/postgres/repository.go:261-263`; `internal/retrieval/runtime/dispatcher_runner.go:143-165,220-226`). It should be stopped during drain rather than continuously opening rejected transactions.
- Existing controller tests cover only one drain transition and one candidate transition (`internal/modelsettings/runtime/controller_test.go:31-82`). They do not test registration ordering, heartbeat takeover, stale owner fail-closed, Begin/Resume hooks, API serving gate, or Worker process lifecycle.
- There is no explicit unregister. A valid new instance replaces the role row; the old process notices at its next heartbeat and exits. This behavior needs a real concurrent ownership test because up to one heartbeat interval remains between takeover and old-process termination.

### Model construction and the six direct factory calls

`LoadSettings` correctly selects only active for an ordinary managed process or the fixed target for a valid applying/verifying rollout (`internal/modelsettings/runtime/loader.go:27-70`). It destroys the resolved secret buffers after `Build` (`loader.go:61-70`). However, the result is not a safe/frozen runtime boundary:

- `Models` exposes `Config`, Chat, and Embedding (`internal/modelsettings/runtime/models.go:28-33`). Overlay copies plaintext Chat/Embedding keys into `config.Config` (`models.go:81-113`). Destroying `ResolvedSettings.Secret` does not clear Go strings retained by `Models.Config`.
- `Build` already calls both production factories (`models.go:116-130`). API/Worker then replace their process config with `Loaded.Models.Config` (`cmd/api/main.go:160-167`; `cmd/worker/main.go:251-261`) and ignore `Loaded.Models.Chat` / `Embedding`.
- Static mode returns only `Models{Config: base}` and constructs no adapters (`internal/modelsettings/runtime/loader.go:27-31`), contrary to the plan to migrate static composition to the single runtime first.
- No `NewConfiguredModelRuntime` symbol exists in the current source.

Current direct command-layer factories:

| Process / consumer | Direct call | Required replacement |
| --- | --- | --- |
| API Retrieval search | `cmd/api/main.go:920-947`, call at `:941` | Inject API Runtime Embedder + frozen Embedding Contract |
| Worker Tool search | `cmd/worker/main.go:1102-1119`, call at `:1114` | Inject the Worker Runtime Embedder |
| Worker Agent relation/RAG Chat | `cmd/worker/main.go:1270-1290`, call at `:1282` | Inject Worker Runtime Chat + Chat Contract |
| Worker RAG retrieval | `cmd/worker/main.go:1342-1367`, call at `:1358` | Reuse the same Worker Runtime Embedder |
| Worker Artifact retrieval | `cmd/worker/main.go:1470-1495`, call at `:1486` | Reuse the same Worker Runtime Embedder |
| Worker Reindex vector builder | `cmd/worker/main.go:1580-1604`, call at `:1593` | Reuse the same Worker Runtime Embedder and persist its runtime revision |

The task wording's “five direct configured model factories” matches the five Worker calls (one Chat plus four Embedder). Including API, the current worktree has six direct command-layer calls. The existing architecture test explicitly expects one direct Reindex factory call (`cmd/worker/main_test.go:186-226`), which now enforces the old architecture and must be replaced.

### Persisted revision-binding gaps

#### Workflow attempt

- `workflow.node_attempt` has no model-settings revision or runtime instance binding (`migrations/00012_workflow_runtime_state_machine.sql:91-127`).
- `domain.NodeAttempt` has no revision (`internal/workflow/domain/model.go:162-179`).
- `RuntimeRepository.Claim` creates the attempt without checking the current Worker runtime row or state and without writing a revision (`internal/workflow/adapter/postgres/runtime_state.go:30-40,125-141`). Column/scanner definitions omit it (`runtime_state.go:28,895-904`).
- Runtime worker `ClaimCommand` supplies delivery/lease data only, and `ExecutionContext` has no revision (`internal/workflow/adapter/river/runtime_worker.go:97-100,133-140`; `internal/workflow/application/executor_registry.go:17-36`).

Minimum invariant: `ClaimCommand` must carry the frozen Worker revision and runtime instance identity; inside the claim transaction, verify that `ops.model_settings_runtime(role='worker')` is still owned/active and authorized by the singleton state, then insert the revision on `node_attempt`. This closes the heartbeat-delay window in which a replaced old Worker could otherwise claim another job. The immutable attempt remains on its starting revision even after rollout.

#### Retrieval embedding/index

- `retrieval.embedding_version` records provider/model/adapter/config hash but no model-settings revision; `index_version` records only the embedding-version FK (`migrations/00014_retrieval_index_foundation.sql:9-70`).
- `domain.EmbeddingVersion` and `SameEmbeddingBinding` omit revision (`internal/retrieval/domain/embedding.go:38-50,80-90`). `domain.IndexVersion` also omits it (`internal/retrieval/domain/index.go:20-42`).
- PostgreSQL inserts omit revision (`internal/retrieval/adapter/postgres/repository.go:35-55,108-137`).
- Reindex registers the direct factory's contract without the loaded runtime revision (`cmd/worker/main.go:1593-1604`).

Minimum invariant: persist the runtime revision that produced each model-backed embedding/index build. Attempt/ModelRun revision equality is strict, but Index revision should remain an audit fact rather than blindly forcing equality with every later query ModelRun: an unchanged embedding contract may remain compatible across a chat-only settings revision. Compatibility remains contract-based; provenance remains revision-based.

#### Agent ModelRun

- `agent.model_run` binds Workflow attempt and model/profile/prompt/retrieval versions but no model-settings revision (`migrations/00018_agent_runtime.sql:13-64`).
- `domain.ModelRun` and validation omit revision (`internal/agent/domain/runtime.go:91-127`).
- Both relation and RAG executors construct ModelRun from `ExecutionContext` without a revision (`internal/agent/adapter/workflow/executor.go:88-100`; `internal/agent/adapter/workflow/rag_executor.go:173-197`).
- Insert, scan, and replay comparison paths omit revision (`internal/agent/adapter/postgres/repository.go:33-58`; `internal/agent/adapter/postgres/scans.go:21-86,157-201`). The RAG memory snapshot finalization path reuses the same ModelRun SQL and must be updated too (`internal/agent/adapter/postgres/memory_snapshots.go`).

Minimum invariant: `ExecutionContext.ModelSettingsRevision` comes only from the persisted attempt; every new `ModelRun` copies it; repository replay comparison includes it; and a composite database FK/check ensures ModelRun revision equals its NodeAttempt revision.

Static-mode schema caveat: managed revisions are nonnegative, while current static loader uses `Revision = -1` (`loader.go:27-31`) and has no row in `ops.model_settings_revisions`. Before adding NOT NULL/FKs, choose an explicit representation. The least misleading migration is a nullable revision where NULL means static/unmanaged, or a separate runtime-source discriminator. Reusing revision 0 or storing `-1` would falsely bind configured static models to the canonical managed disabled revision.

### Prioritized integration checklist and minimum file changes

#### P0: close every enqueue and activation boundary

1. Delete the non-tx River insertion surface.
   - Change `internal/workflow/adapter/river/client.go`: remove or unexport direct `Insert`/`direct` for business producers.
   - Change `internal/export/adapter/river/dispatcher.go`: remove `NewDispatcher` and the direct branch; require database + typed inserter.
   - Keep production composition in `cmd/api/main.go` and `cmd/worker/main.go` on the transactional constructor.
   - Replace/extend `internal/export/adapter/river/dispatcher_test.go` to prove a dispatcher cannot exist without a transaction and that fence failure rolls back.

2. Gate process activation and local producers.
   - Change `cmd/api/main.go`: do not start serving until registration succeeds and `Controller.Active()` closes; aggregate model-backed component readiness before registration; add a local producer gate that closes on drain and reopens only on authorized resume.
   - Change `cmd/worker/main.go` and `cmd/worker/lifecycle.go`: wire `DrainHooks.Begin` to stop Reindex/local producer dispatch, `IsQuiesced` to running River jobs, and `Resume` to restart only for the still-authorized old active runtime.
   - Consider a small role lifecycle wrapper under `internal/modelsettings/runtime/` rather than duplicating registration/ready/run ordering in both commands.

3. Bind attempt start atomically to Worker ownership and revision.
   - Schema: amend task-owned `migrations/00064_model_settings.sql` only if it is not published; otherwise add the next forward migration. Add attempt revision/source columns and the composite constraints needed by ModelRun.
   - Change `internal/workflow/application/runtime_contract.go` (Claim command/result port), `internal/workflow/domain/model.go`, `internal/workflow/adapter/postgres/runtime_state.go`, `internal/workflow/application/executor_registry.go`, and `internal/workflow/adapter/river/runtime_worker.go`.
   - In Claim's existing transaction, lock/verify state + Worker runtime ownership before inserting the attempt.

#### P1: one frozen Model Runtime and persisted downstream provenance

4. Introduce one safe Runtime factory and inject it.
   - Change `internal/modelsettings/runtime/models.go`, `loader.go`, and `bootstrap.go`: return immutable Chat/Embedding adapters plus contracts/capabilities/revision, never a secret-bearing `config.Config`.
   - Change `cmd/api/main.go`: inject the Runtime Embedder into Retrieval.
   - Change `cmd/worker/main.go`: inject one Runtime Chat and one concurrency-safe Runtime Embedder into Tool, RAG, Artifact, and Reindex.
   - Remove all six direct command-layer `NewConfigured*` calls and replace the outdated AST expectation in `cmd/worker/main_test.go:186-226`.

5. Persist Index and ModelRun provenance.
   - Change `internal/retrieval/domain/embedding.go`, `internal/retrieval/domain/index.go`, `internal/retrieval/application/service.go` / registration commands, `internal/retrieval/adapter/postgres/repository.go`, and its scan helpers to persist model runtime revision/source.
   - Change `internal/agent/domain/runtime.go`, `internal/agent/adapter/workflow/executor.go`, `rag_executor.go`, `internal/agent/adapter/postgres/repository.go`, `scans.go`, `runs.go`, and `memory_snapshots.go` to persist and replay-check revision.
   - Add database constraints for ModelRun == Attempt revision. Keep Index/query compatibility based on the frozen embedding contract while recording each build's revision provenance.

#### Required tests before claiming G3

6. Real PostgreSQL/River fence race:
   - Add `internal/workflow/adapter/river/enqueue_fence_integration_test.go` (or extend `worker_real_integration_test.go`) using the actual model-settings repository and River schema.
   - Hold concurrent producer/drain transactions and prove exactly one ordering: producer commits before draining, or producer is rejected and no River row/business mutation commits after draining.
   - Cover root start, business retry, successor, human successor, and resume via `internal/workflow/adapter/postgres/runtime_start_integration_test.go` and `runtime_state_integration_test.go`.

7. Background producer coverage:
   - Extend `internal/retrieval/adapter/postgres/dispatcher_integration_test.go` for both first and retry dispatch under draining with the real fence.
   - Add `internal/export/adapter/river/dispatcher_integration_test.go` for HTTP dispatch/recovery with the real fence and remove unsafe-constructor coverage.
   - Add an architecture/source test that enumerates project River job kinds and rejects raw/direct insert callsites outside the shared helper.

8. Queue/drain lifecycle:
   - Extend `internal/workflow/adapter/river/worker_real_integration_test.go` with QueuePause/Resume, already-running completion, ready/scheduled/retry preservation, and stuck-job rescue while paused.
   - Extend `internal/modelsettings/runtime/controller_test.go` for registration-before-active, heartbeat ownership loss, Begin/IsQuiesced/Resume, candidate commit/abort, and stale owner rejection.
   - Add process composition tests in `cmd/api/main_test.go` and `cmd/worker/main_test.go`: candidate API does not serve before Active; prepared Worker does not start dispatcher/River; drain stops/restarts local dispatcher; ownership loss terminates the process path.

9. Revision persistence:
   - Extend `internal/workflow/adapter/postgres/runtime_state_integration_test.go`: claim stores process revision; reclaim creates a new immutable attempt; stale instance/revision cannot claim; old attempt never changes after rollout.
   - Extend `internal/retrieval/adapter/postgres/repository_integration_test.go` and `vector_build_integration_test.go`: embedding/index revision is persisted, replay conflicts are detected, and compatible contract behavior is explicit.
   - Extend `internal/agent/adapter/postgres/repository_integration_test.go` and `internal/agent/adapter/workflow/executor_integration_test.go`: ModelRun copies Attempt revision, replay includes it, and mismatched revision is rejected by PostgreSQL.
   - Add/update `internal/platform/migration/model_settings_runtime_binding_integration_test.go` for empty Up, repeated Up, forward upgrade, FKs/checks, append-only attempts/runs, and guarded Down behavior.

10. Runtime architecture/security regression tests:
    - Replace `cmd/worker/main_test.go:186-226` with a test requiring zero direct `NewConfiguredChatModel/NewConfiguredEmbedder` calls in both commands and exactly one Runtime factory per process.
    - Add identity/contract tests proving all four Worker embedding consumers receive the same adapter/contract and that API uses the API Runtime instance.
    - Extend `internal/modelsettings/runtime/loader_test.go` / `models_test.go` to assert Runtime has no `config.Config`, no API key in `String`/`GoString`/errors, and temporary secrets are destroyed after construction.

### Files found

- `.trellis/tasks/07-30-dev-docker-model-settings/{prd,design,implement}.md`: W3/G1/G3 contracts and acceptance gates.
- `.trellis/spec/backend/database-guidelines.md`: pgx transaction ownership, database constraints, migration, and River-as-transport rules (`:13-16,29-42,74-77`).
- `.trellis/spec/guides/cross-layer-thinking-guide.md`: requires mapping data across layer boundaries and assigning validation responsibility (`:19-50`).
- `internal/workflow/adapter/river/{client,inserter}.go`: shared River client, unsafe direct insert, typed tx insertion, and fence seam.
- `internal/modelsettings/adapter/postgres/{runtime,rollout,snapshot}.go`: DB fence, runtime CAS ownership, rollout lock, and freshness projection.
- `internal/workflow/adapter/postgres/{runtime_start,runtime_state}.go`: all Workflow root/state-transition producer inserts and attempt claim.
- `internal/export/adapter/river/dispatcher.go`: safe transactional and unsafe direct Export paths.
- `internal/retrieval/adapter/postgres/dispatcher.go` and `internal/retrieval/runtime/dispatcher_runner.go`: Reindex first/retry background producers.
- `cmd/api/main.go`, `cmd/worker/main.go`, `cmd/worker/lifecycle.go`, `cmd/modelctl/control.go`: process composition, model factories, controller registration, queue lifecycle, and drain orchestration.
- `internal/modelsettings/runtime/{loader,models,controller}.go`: active/fixed-target loading, duplicated adapter construction, and watcher lifecycle.
- `migrations/00012_workflow_runtime_state_machine.sql`, `00014_retrieval_index_foundation.sql`, `00018_agent_runtime.sql`, `00064_model_settings.sql`: current attempt/index/model-run/runtime schemas.
- `internal/workflow/domain/model.go`, `internal/retrieval/domain/{embedding,index}.go`, `internal/agent/domain/runtime.go`: domain facts currently lacking model-settings revision.

### External references

No external browsing was needed. Project-pinned versions were taken from `.trellis/spec/backend/database-guidelines.md:9`: River/riverpgxv5 `v0.40.0` and Goose `v3.27.0`.

### Related specs

- `.trellis/spec/backend/database-guidelines.md:14-16`: River is transport, PostgreSQL owns facts/invariants, and versioned Workflow/Embedding/Index facts require constraints.
- `.trellis/spec/backend/database-guidelines.md:29-35`: explicit parameterized SQL, database-enforced FKs/state/idempotency, and module-owned transactions.
- `.trellis/spec/backend/database-guidelines.md:38-45`: forward schema evolution and safe/repeatable migrations.
- `.trellis/spec/backend/database-guidelines.md:74-77`: empty/repeated migrations and real PostgreSQL constraint coverage.
- `.trellis/spec/guides/cross-layer-thinking-guide.md:19-50`: map Source -> Transform -> Store and define each boundary's exact contract/errors.

## Caveats / Not Found

- Per Trellis research-role isolation, `implement.jsonl` and `check.jsonl` were not read. The audit used `prd.md`, `design.md`, `implement.md`, project specs, and current source.
- The shared worktree changed during this read-only audit. A final rescan found that `CheckEnqueueAllowed` had been removed; all other findings above reflect that final scan. Re-run the architecture searches after concurrent W1/W2 edits settle.
- No production project “rescue producer” that inserts a new row was found. River stuck-job rescue/retry mutates/reclaims an existing job; QueuePause behavior still needs real integration coverage.
- The task phrase “five direct factories” is correct for Worker alone. The full `cmd/api` + `cmd/worker` scan found six.
- Static-mode revision representation is not decided by current docs/schema. Do not add a misleading FK sentinel without settling NULL/source semantics.
- Tests were not executed; this was a source and contract audit. Existing tests prove individual helper behavior, not the cross-process PostgreSQL/River race required by G3.
