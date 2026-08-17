# Research: Post-fact rollback routing

- Query: Determine how to prove that rollback never sends a Workspace containing Workspace Analysis facts to a legacy API reader, identify the authoritative routing marker, and define the smallest honest repository-side verification.
- Scope: internal
- Date: 2026-08-17

> Implementation update (2026-08-18): the recommendation below has been implemented inside the existing five-phase
> `compose-workspace-analysis-compat-smoke`. The post-fact phase recreates API from the exact same current image with
> admission disabled, preserves the `app-netns` identity, and rolls back only the Worker artifact. It proves reconnect
> reads/SSE replay, not API container identity or uninterrupted connections. Sections describing this gate as missing
> are retained only as the original research path and are superseded by this note and the updated evidence table.

## Findings

### Conclusion

The repository does not contain a multi-backend production router that can choose between legacy and current APIs per request or per Workspace. Its supported deployment topology has one Active Workspace and one API process behind a byte-forwarding `socat` ingress. Therefore the safe rollback invariant for this repository is simpler and stronger than dynamic per-Workspace routing:

> Once any Workspace Analysis fact has been accepted, keep the current compatible API binary on the only published ingress. Disable new Workspace Analysis admission, drain or terminalize its runs, and roll back only the Worker. Never replace the published API with the frozen legacy binary.

This invariant can be exercised locally with a real post-fact Compose rollback smoke. It does not by itself prove an external production load balancer or canary router that is not present in this repository. The existing production-routing checklist must remain unchecked until the actual deployment environment provides route configuration and request evidence, or until the checklist is explicitly narrowed to the supported single-API Compose topology.

### Files found

| Path | Description |
| --- | --- |
| `docs/architecture/system-design.md` | Defines one Active Workspace and the one-shot Workspace Control boundary. |
| `deploy/compose.yml` | Defines one `app` service and one `worker`; the API binds `127.0.0.1:8081` inside the app network namespace. |
| `deploy/compose.netns.yml` | Publishes only the `app-netns` ingress on the host. |
| `deploy/netns-ingress.sh` | Uses `socat` to forward all ingress bytes to the single API listener; it does not inspect auth or Workspace identity. |
| `deploy/compose-workspace-analysis-compat-smoke.sh` | Five-phase real compatibility gate: four pre-enable legacy/current pairs plus a post-fact current-API/legacy-Worker rollback. |
| `deploy/compose-workspace-analysis-compat-smoke-contract.sh` | Static contract for the pre-enable matrix, post-fact facts/image/ingress checks, exact-run SSE replay and cleanup. |
| `internal/conversation/adapter/postgres/dispatch.go` | Atomically creates the mode-bearing Question, Workflow, pending Answer, Analysis Run, and started event. |
| `internal/agent/adapter/postgres/workspace_analysis_runs.go` | Persists the immutable Workspace-to-Analysis-Run binding. |
| `internal/conversation/adapter/postgres/scans.go` | Current reader understands Question mode and the Workspace Analysis Answer union. |
| `internal/conversation/adapter/postgres/turns.go` | Current Answer/Turn query projection includes Question mode and new Answer shapes. |
| `cmd/api/main.go` | Always composes the current Conversation repository and Workspace Analysis timeline reader; the feature flag controls admission/runtime hooks, not historical reads. |
| `cmd/worker/main.go` | Releases the Workspace Analysis capability before beginning runtime shutdown. |
| `internal/agent/adapter/postgres/workspace_analysis_capability.go` | Admission requires a non-released, non-expired exact Worker capability advertisement. |
| `migrations/00085_workspace_analysis_persistence.sql` | Adds the mode and durable facts, indexes the Workspace/Question binding, and refuses destructive Down when new facts exist. |

### Code patterns and evidence

#### 1. The deployed ingress cannot perform Workspace routing

- The architecture permits only one Active Workspace (`docs/architecture/system-design.md:37-40`).
- Compose defines one API service, bound at `127.0.0.1:8081`, sharing the `app-netns` namespace (`deploy/compose.yml:110-144`).
- Only `app-netns` publishes the host port (`deploy/compose.netns.yml:3-37`).
- The ingress is a blind `socat TCP-LISTEN:8080 ... TCP:127.0.0.1:8081` forwarder (`deploy/netns-ingress.sh:14-20`). It has no authentication or database authority and must not be taught to trust `X-Workspace-ID` as a routing credential.

Consequently, adding an nginx/Traefik-style per-request router solely for this gate would invent a second deployment architecture. The established topology can satisfy the requirement by retaining the compatible API globally while rolling back only the Worker.

#### 2. The frozen legacy API cannot safely read post-fact rows

The frozen compatibility reference is `541033dd4548f8a53ef1064053ff6545faf2820e` (`deploy/compose-workspace-analysis-compat-smoke.sh:17-20`). It is not merely missing a cosmetic field:

- Its Question query omits `q.mode` (`541033dd:internal/conversation/adapter/postgres/turns.go:13-19`).
- Its scanner reconstructs every row as the legacy RAG request (`541033dd:internal/conversation/adapter/postgres/scans.go:74-118`).
- Its validator recomputes only the schema-v1 request hash and rejects a mismatch (`541033dd:internal/conversation/domain/question.go:100-110,253-276`). A Workspace Analysis Question persists the mode-bound v2 hash in the current tree (`internal/conversation/domain/question.go:294-339`).
- Its Answer decoder supports only `rag_answer`, `refusal`, and `clarification`; every new Workspace Analysis result type is rejected as unsupported (`541033dd:internal/conversation/domain/answer.go:124-163`). The scanner converts that into `CONVERSATION_PERSISTENCE_CORRUPT` (`541033dd:internal/conversation/adapter/postgres/scans.go:185-220`).
- It has no `/answers/:answer_id/analysis-timeline` route (`541033dd:internal/conversation/http/handler.go:56-65`).

A legacy API can start against the additive schema and serve old RAG rows, as the existing pre-enable matrix proves, but it cannot be the reader for a Workspace after that Workspace accepts the new mode.

#### 3. The routing marker is durable and transactionally created

`SubmitQuestion` starts one transaction, inserts the mode-bearing Question, starts the exact Workflow, inserts the pending Answer, creates the Analysis Run, appends the started event, and commits only after every step succeeds (`internal/conversation/adapter/postgres/dispatch.go:122-267,571-609`). The Analysis Run persists the exact `workspace_id`, `question_id`, `answer_id`, and `workflow_run_id` binding (`internal/agent/adapter/postgres/workspace_analysis_runs.go:25-76`). Migration `00085` enforces a unique `(workspace_id,question_id)` Analysis Run binding (`migrations/00085_workspace_analysis_persistence.sql:225-240`).

For an operational routing guard, use an append-only, Workspace-scoped marker and fail closed on disagreement:

```sql
SELECT
  EXISTS (
    SELECT 1
    FROM agent.question
    WHERE workspace_id = $1 AND mode = 'workspace_analysis'
  ) AS has_mode_fact,
  EXISTS (
    SELECT 1
    FROM agent.workspace_analysis_run
    WHERE workspace_id = $1
  ) AS has_analysis_run;
```

Safe decision table:

| Probe result | Allowed API reader |
| --- | --- |
| `true / true` | compatible current API only |
| `false / false` | legacy may be considered before enablement |
| disagreement, query error, unknown schema | compatible current API only; alert on invariant drift |

The migration Down guard independently treats any non-RAG Question or any Workspace Analysis table fact as proof that destructive rollback is unsafe (`migrations/00085_workspace_analysis_persistence.sql:4790-4839`). Production rollback must leave the additive schema and facts in place.

#### 4. The current API remains a compatible reader without an accepting Worker

- Conversation routes always register Answer and Analysis Timeline reads (`internal/conversation/http/handler.go:58-67`).
- `newConversationHandlers` always injects the PostgreSQL repository as `WorkspaceAnalysisTimelineReader`; the optional Workspace Analysis starter only changes Question dispatch (`cmd/api/main.go:1667-1719`).
- The feature flag gates runtime hooks and the starter (`cmd/api/main.go:448-460,675-687`), not the historical Answer/Timeline reader.
- The existing composition test already requires Conversation read/feedback/SSE services to remain available when dispatch is disabled (`cmd/api/main_test.go:265-284`).

Therefore a current API can remain on ingress during and after Worker rollback, continue reading Answer/Turn/Timeline facts, and fail new Workspace Analysis submission closed when no exact Worker capability exists.

#### 5. Graceful Worker stop closes new admission before drain

On shutdown the current Worker marks readiness shutting down and releases its Workspace Analysis capability before shutting down the River lifecycle (`cmd/worker/main.go:788-815`). The database marks the advertisement released (`internal/agent/adapter/postgres/workspace_analysis_capability.go:213-229`), and API admission accepts only a matching advertisement whose `released_at IS NULL` and lease is still fresh (`internal/agent/adapter/postgres/workspace_analysis_capability.go:231-244`).

This provides the correct rollback order for the supported topology:

1. Gracefully stop the current Worker, which removes new-mode admission immediately.
2. Allow its lifecycle shutdown to drain, or use the still-current API cancellation path to reach safe terminal checkpoints.
3. Prove every Analysis Run is terminal and there are no runnable `workspace-analysis@1` nodes/jobs.
4. Recreate API from the same current image with admission disabled, and recreate Worker from the frozen legacy source.
5. Keep the current API version/read contract and published ingress identity; only the Worker artifact moves to legacy.

#### 6. The original compatibility gap is now covered by a post-fact phase

The first four phases still preserve the original zero-fact pre-enable invariant. The fifth phase now creates one real terminal Workspace Analysis Run under current/current, freezes its Workspace-wide projection, drains active Runs, and then applies the supported rollback topology.

The phase records the actual running image identities, recreates API from the same current image with feature-off configuration, preserves the ingress netns identity, changes only the Worker image to the frozen legacy artifact, then proves historical Answer/Turn/Timeline and exact-run SSE replay, non-retryable admission rejection, fixed RAG continuity, and unchanged facts.

### Implemented repository-side verification

`make compose-workspace-analysis-compat-smoke` and its static contract reuse the deterministic provider, frozen legacy archive, disposable PostgreSQL, cleanup helper, and exact Workspace grant, and prove this sequence:

1. Start a current API/current Worker pair with Workspace Analysis enabled and complete one real Workspace Analysis Question.
2. Save the Answer, Conversation, Workflow, Workspace and current API/Worker image IDs. Assert the exact Workspace has both the mode fact and Analysis Run marker.
3. Assert the Answer and Analysis Run are terminal and no runnable Workspace Analysis nodes/jobs remain.
4. Stop current services after drain and rebuild the API from the same current image with admission disabled.
5. Build the Worker from frozen ref `541033dd...`; start it beside the current API reader without enabling the new capability.
6. Assert the API image and `app-netns` identity are unchanged, the Worker image changed to legacy, and the published ingress still resolves to the compatible API. Do not claim API process or connection continuity.
7. Through that ingress, re-read the stored Answer, Turn list, Analysis Timeline, Citation/result binding, and SSE replay with exact current wire shapes.
8. Run one fixed RAG Question through the legacy Worker to prove the supported fallback workload remains available.
9. Reassert that new Workspace Analysis admission fails closed and all existing Workspace Analysis fact counts/hashes are unchanged.

The safe-path smoke must never publish a legacy API for the fact-bearing Workspace. A legacy-reader negative control may be done through source-level contract checks or a private, non-published fixture, but it is not required once the incompatibility above is locked by the frozen source ref.

### What this evidence can and cannot close

| Evidence | Can close |
| --- | --- |
| Real disposable Compose post-fact drill, unchanged current API image/read contract and ingress identity, legacy Worker artifact rollback, current Answer/Timeline/SSE replay | The local supported single-API rollback retains a compatible reader; it does not prove connection continuity |
| Existing pre-enable four-way matrix | Mixed-binary compatibility before any new fact exists |
| Actual deployment config plus API image digest/route trace for a fact-bearing canary Workspace | The task's current wording: production routing never sends that Workspace to a legacy API |
| Repository smoke alone when production routing is external/unknown | **Cannot** close the production-routing or release/canary checklist |

The task/design/spec now formally define the repository-supported topology as a single compatible API version behind one ingress with Worker-artifact-only rollback. This closes local AC10/Compatibility evidence. The target-environment migration, canary, OTLP observation, expansion and actual rollback records remain unchecked external release actions.

### External references

No external behavioral reference was required. The routing conclusion comes from the repository's own Compose topology, frozen legacy source, current database transaction boundaries, and task/spec contracts. The smoke should continue using the repository-pinned Docker Compose commands and frozen legacy commit rather than relying on an unversioned proxy product.

### Related specs and task contracts

- `.trellis/spec/backend/workspace-analysis-contract.md:130-164` requires compatible reads after new facts and a real post-enable compatibility proof.
- `.trellis/tasks/08-15-workspace-agent-user-flow/prd.md:82-100` requires default-off rollout and retention of a compatible API reader.
- `.trellis/tasks/08-15-workspace-agent-user-flow/design.md:347-355` defines Worker-only rollback after drain and forbids routing fact-bearing Workspaces to a legacy API.
- `.trellis/tasks/08-15-workspace-agent-user-flow/implement.md:225-236` separates pre-enable compatibility, post-fact production routing, and externally authorized release steps.

## Caveats / Not Found

- No production load balancer, service mesh, canary router, per-Workspace routing table, or external production traffic-routing configuration exists in this repository. Production traffic behavior cannot be inferred from the smoke scripts.
- The post-fact phase exercises a fact-bearing Workspace after changing only the Worker artifact to the frozen legacy binary; it recreates API from the same current image to apply feature-off configuration.
- No current test directly starts the frozen legacy API against a terminal Workspace Analysis Answer and observes the expected persistence-corrupt/route-missing behavior; the incompatibility is proven by frozen source inspection, not a runtime negative control.
- A request router must not decide from an unauthenticated `X-Workspace-ID` header. A future multi-API topology needs a trusted control-plane decision derived from authoritative Workspace identity and the append-only database marker, with errors defaulting to the compatible reader.
- The API flag is not itself a historical-read gate, but disabling it also removes Workspace Analysis cancellation hooks. Prefer graceful Worker capability release to close admission while the current API remains capable of controlling/draining existing runs.
- This research originally did not edit code; the 2026-08-18 implementation and task/spec updates are recorded above.
