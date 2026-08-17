# Research: Workspace Analysis aggregate terminal/fault matrix

- Query: Audit every `WorkspaceAnalysisRunTerminationReason` (including `COMPLETED`), identify its real trigger and frozen-v1 reachability, map current PostgreSQL/River/browser evidence, and recommend the smallest honest aggregate gate without fabricating terminal facts.
- Scope: internal
- Date: 2026-08-17

> Implementation update (2026-08-18): the recommended aggregate public Question -> River/Worker -> terminal Answer
> matrix now covers all 13 production-reachable reasons, and the frozen-v1 `BUDGET_EXHAUSTED` case is covered by exhaustive
> prefix non-reachability plus PostgreSQL rejection of non-causal proofs. The five-phase compatibility smoke also covers
> post-fact current-API/legacy-Worker rollback. The original gap analysis below is retained as historical rationale; its
> “remain unchecked now” disposition is superseded by the current task evidence, except for externally authorized
> production release actions.

## Findings

### Executive conclusion

The domain has 14 terminal reason values (`internal/agent/domain/workspace_analysis_persistence.go:87-119`). Current coverage is layered, not one aggregate River matrix:

- `COMPLETED`, `WORKSPACE_ANALYSIS_RESULT_UNKNOWN`, and `WORKSPACE_ANALYSIS_CANCELLED` already have end-to-end River/Compose evidence. `COMPLETED` and `CANCELLED` also reach Playwright.
- The other valid refusal/failure paths have combinations of executor unit tests and real PostgreSQL tests, but most do not yet drive a public Question through a live River worker to the final Answer bundle.
- `WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED` is structurally unreachable from a valid frozen-v1 run. The static operation catalog consumes at most exactly the frozen maxima, replacement/duplicate delivery reuses the same logical operation, and there is no production construction of `FinalizeWorkspaceAnalysisTerminationCommand.BudgetRequest`. The existing PostgreSQL test is intentionally negative: it rejects false budget-exhaustion proofs.
- Therefore the literal checklist items “full terminal-reason matrix in one aggregate River fault run” and “every budget terminal reason” cannot be marked complete honestly. Either the task wording must distinguish operationally reachable reasons from the v1 non-reachability invariant, or a new policy version must introduce a genuine over-budget next operation. Corrupting counters, inserting a terminal proof directly, or lowering frozen v1 limits only in a test would fabricate the condition.

### Files found

| File | Role in this audit |
| --- | --- |
| `internal/agent/domain/workspace_analysis_persistence.go` | Owns the 14 reason values and status/reason validation. |
| `internal/agent/domain/workspace_analysis_policy.go` | Freezes v1 node, call, source-read, and token maxima. |
| `internal/agent/domain/workspace_analysis_operation.go` | Freezes the nine possible logical call slots across the six-node DAG. |
| `internal/agent/adapter/workflow/workspace_analysis_retrieve.go` | Owns clarification and Planner/model terminal routing. |
| `internal/agent/adapter/workflow/workspace_analysis_read_evidence.go` | Owns evidence-insufficient and read Tool terminal routing. |
| `internal/agent/adapter/workflow/workspace_analysis_review_publish.go` | Owns citation-invalid, faithfulness-rejected, and completed publication. |
| `internal/agent/adapter/workflow/workspace_analysis_terminal.go` | Maps persisted Model/Tool/receipt/deadline evidence to stable terminal reasons. |
| `internal/conversation/adapter/postgres/workspace_analysis_terminal_hook.go` | Owns no-Call runtime failure and runtime cancellation publication. |
| `internal/conversation/application/workspace_analysis_finalizer.go` | Validates the proof shape required by each finalizer-owned reason. |
| `migrations/00085_workspace_analysis_persistence.sql` | Enforces causal termination proofs, including the budget-overage predicate. |
| `internal/platform/migration/workspace_analysis_persistence_integration_test.go` | Real PostgreSQL proof tests for refusal, clarification, budget rejection, deadline, and receipt invalid. |
| `internal/platform/migration/workspace_analysis_deadline_terminalization_integration_test.go` | Real PostgreSQL deadline publication and response-loss replay. |
| `internal/platform/migration/workspace_analysis_authorization_repository_integration_test.go` | Real PostgreSQL Tool failure, replacement Unknown, and deadline admission. |
| `internal/agent/adapter/postgres/workspace_analysis_model_operations_integration_test.go` | Real PostgreSQL Model failure/refusal/Unknown and Review result persistence. |
| `internal/conversation/adapter/postgres/workspace_analysis_terminal_hook_integration_test.go` | Real PostgreSQL runtime-failure and cancellation bundles. |
| `cmd/worker/workspace_analysis_conversation_integration_test.go` | Public API plus actual PostgreSQL/River success and receipt-loss redelivery. |
| `deploy/compose-rag-smoke.sh` | Actual API/Worker/Provider/PostgreSQL/SSE/browser aggregate and restart fault orchestration. |
| `web/e2e/workspace-analysis.smoke.spec.ts` | Completed/cancelled browser assertions and Stop wire contract. |
| `internal/events/http/handler_postgres_integration_test.go` | Real PostgreSQL `Last-Event-ID` replay and cursor recovery. |
| `Makefile` | Existing broad `workspace-analysis-integration` and Compose smoke targets. |

### Code patterns and invariants

#### One owner per terminal trigger

- `retrieve_evidence` publishes clarification only when the persisted Planner result says `requires_clarification=true` (`internal/agent/adapter/workflow/workspace_analysis_retrieve.go:239-278`).
- `read_evidence` publishes evidence-insufficient only from an exact Search authority whose hit count is zero (`internal/agent/adapter/workflow/workspace_analysis_read_evidence.go:180-220`).
- `review_publish` publishes citation-invalid before Review when the exact validation receipt rejects a citation, faithfulness-rejected when the persisted Review says `passed=false`, and success only after both checks pass (`internal/agent/adapter/workflow/workspace_analysis_review_publish.go:210-315`).
- Persisted Model terminal evidence maps only `FAILED/failed -> MODEL_FAILED`, `UNKNOWN/unknown -> RESULT_UNKNOWN`, and `SUCCEEDED/refused -> MODEL_REFUSED` (`internal/agent/adapter/workflow/workspace_analysis_terminal.go:40-63`).
- Persisted Tool terminal evidence maps only `FAILED -> TOOL_FAILED` and `UNKNOWN -> RESULT_UNKNOWN`; exact receipt failures map separately to `RECEIPT_INVALID` (`internal/agent/adapter/workflow/workspace_analysis_terminal.go:66-113`, `181-206`).
- Only a classified, frozen pre-authorization deadline marker can publish `DEADLINE_EXCEEDED` (`internal/agent/adapter/workflow/workspace_analysis_terminal.go:116-158`).
- A Workflow node failure with no provable Model/Tool call is published as `RUNTIME_FAILED`; cancellation has its own runtime hook and proof (`internal/conversation/adapter/postgres/workspace_analysis_terminal_hook.go:100-188`, `190-260`).
- The application finalizer rejects a reason whose proof shape does not match its owning seam (`internal/conversation/application/workspace_analysis_finalizer.go:177-201`). `RUNTIME_FAILED` is deliberately owned by the terminal hook rather than this operation finalizer.

#### Why v1 budget exhaustion is not a valid runtime scenario

The non-reachability is a conjunction of code-level invariants, not an assumption:

1. V1 permits exactly 3 Model calls, 6 Tool calls, 3 Source reads, `3 * 65,536` input tokens, and output tokens equal to Plan + bounded Synthesis + Review (`internal/agent/domain/workspace_analysis_policy.go:8-32`).
2. The only logical slots are Plan, Synthesis, Review; Git, Search, Validate; and Source Read ordinals 1..3 (`internal/agent/domain/workspace_analysis_operation.go:126-135`). Their maximum aggregate is exactly the limit above, never larger.
3. Each Model reservation is fixed to its phase; the three output reservations sum to the run limit, and each input reservation is exactly 65,536 (`internal/agent/domain/workspace_analysis_persistence.go:856-872`). Tool and Source reservations are also exact (`internal/agent/domain/workspace_analysis_persistence.go:802-816`).
4. Duplicate delivery/replacement does not create another logical slot or charge. The same operation is reconciled; Unknown charges the existing reservation and terminates instead of continuing.
5. The database accepts a budget terminal proof only for the next pending canonical operation, with the exact canonical request amount, no prior Failed/Unknown operation, and a request that really exceeds remaining capacity (`migrations/00085_workspace_analysis_persistence.sql:2927-2982`).
6. `TestWorkspaceAnalysisPersistenceMigrationBudgetExhaustionProofRequiresCausalFixedOverage` tries exact and inexact requests and a prior-failure state; every attempted false proof is rejected (`internal/platform/migration/workspace_analysis_persistence_integration_test.go:435-490`). There is no positive subtest.
7. Repository search found `BudgetRequest:` only in `internal/conversation/application/workspace_analysis_finalizer_test.go`; no production path constructs one. Model/Tool admission currently returns an ordinary conflict when counters cannot admit the next call (`internal/agent/adapter/postgres/workspace_analysis_model_operations.go:1185-1209`, `internal/tools/adapter/postgres/workspace_analysis_operations.go:516-555`).

Consequences:

- A real River run cannot end in `WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED` without violating a frozen v1 invariant first.
- Directly changing counters or inserting a proof would test database corruption handling, not a reachable product outcome.
- If the product requires a positive budget terminal, it needs a new policy/Definition version with a legitimate conditional next operation or a deliberately smaller run allowance. It must not mutate `workspace-analysis@1`.

### Terminal reason matrix

Legend: **normal** means a valid business outcome; **fault** means a valid fail-closed path reached through an owned production boundary. “PG” means a real PostgreSQL integration test. Unit tests are listed only when the real test stops before final Answer publication.

| Reason | Trigger seam | Frozen-v1 reachability | Strongest existing real evidence | Missing evidence |
| --- | --- | --- | --- | --- |
| `COMPLETED` | Exact Citation receipt valid, Review result `passed=true`, then `FinalizeSuccess` | Yes, normal | River/API/PG: `TestPublicConversationRunsThroughRiverWorkspaceAnalysis` (`cmd/worker/workspace_analysis_conversation_integration_test.go:65`). PG proof: `TestWorkspaceAnalysisPublicationProofCompletesOnlyAuthoritativeProjection/completed publication` (`internal/platform/migration/workspace_analysis_publication_integration_test.go:28-50`). Compose/browser: `deploy/compose-rag-smoke.sh:1587-1619`, `web/e2e/workspace-analysis.smoke.spec.ts:377-590`. | No reason-specific gap; it is absent only from a single aggregate terminal matrix. |
| `WORKSPACE_ANALYSIS_EVIDENCE_INSUFFICIENT` | Exact Search receipt has zero eligible hits; current `read_evidence` Attempt publishes refusal | Yes, normal | PG: `TestWorkspaceAnalysisPersistenceMigrationDeterministicRefusalsUseExactUpstreamProofs/empty search is finalized by the current read node` (`internal/platform/migration/workspace_analysis_persistence_integration_test.go:693-773`). Unit seam: `TestWorkspaceAnalysisReadEvidenceExecutorRejectsZeroHitsWithoutSourceCall`. | No public API/live River terminal test. Playwright supports `refused`, but Compose invokes only `cancelled` and `completed` (`deploy/compose-rag-smoke.sh:1635-1641`), so the dormant branch is not evidence. |
| `WORKSPACE_ANALYSIS_CITATION_INVALID` | Exact ValidateCitation receipt contains a rejected result; `review_publish` refuses before Review | Yes, normal | PG: `TestWorkspaceAnalysisPersistenceMigrationDeterministicRefusalsUseExactUpstreamProofs/invalid citations are finalized by the current review node` (`internal/platform/migration/workspace_analysis_persistence_integration_test.go:775-816`). Unit seam: `TestWorkspaceAnalysisReviewPublishExecutorRejectsInvalidCitationWithoutReview`. | No public API/live River/browser terminal test. |
| `WORKSPACE_ANALYSIS_FAITHFULNESS_REJECTED` | Persisted Review result is valid but `passed=false` | Yes, normal | PG stores/replays both Review outcomes: `TestWorkspaceAnalysisModelOperationReviewPassedFailedAndCandidateBindingIntegration/failed` (`internal/agent/adapter/postgres/workspace_analysis_model_operations_integration_test.go:277-350`). Unit publication seam: `TestWorkspaceAnalysisReviewPublishExecutorPublishesFaithfulnessRejection`. | PG test stops at Model result; no real finalizer Answer bundle and no public API/live River/browser path. |
| `WORKSPACE_ANALYSIS_MODEL_REFUSED` | Model call is durably `SUCCEEDED`, Model Run is `refused`, with the stable refusal result | Yes, controlled Provider outcome | PG closure/replay: `TestWorkspaceAnalysisModelOperationFailureRefusalAndUnknownClosureIntegration/refused` (`internal/agent/adapter/postgres/workspace_analysis_model_operations_integration_test.go:352-420`). Unit terminal mapping/public result: `TestWorkspaceAnalysisTerminationPublicationsFollowFrozenStatusAndModelMatrix`. | No real final Answer publication through public API/live River/browser. |
| `WORKSPACE_ANALYSIS_CLARIFICATION_REQUIRED` | Planner result is successful and says `requires_clarification=true` | Yes, normal | PG proof with replacement Attempt: `TestWorkspaceAnalysisPersistenceMigrationClarificationProofUsesCurrentRecoveryAttempt` (`internal/platform/migration/workspace_analysis_persistence_integration_test.go:846-883`). Unit seam: `TestWorkspaceAnalysisRetrieveExecutorClarificationIsStableAndNeverSearches`. | No public API/live River/browser terminal test. |
| `WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED` | A canonical next pending operation requests more than the persisted remaining budget | **No, structurally unreachable in valid v1** | Negative PG contract: `TestWorkspaceAnalysisPersistenceMigrationBudgetExhaustionProofRequiresCausalFixedOverage` (`internal/platform/migration/workspace_analysis_persistence_integration_test.go:435-490`). Domain matrix accepts the stable enum in `TestWorkspaceAnalysisRunValidatesLifecycleAndTerminalMatrix` (`internal/agent/domain/workspace_analysis_persistence_test.go:21-63`). | No positive path exists; no production `BudgetRequest` builder exists. A River test must not fabricate one. |
| `WORKSPACE_ANALYSIS_RECEIPT_INVALID` | Exact Tool result receipt is missing, hash-mismatched, contract-invalid, or binding-invalid after the Tool call closes | Yes, fault | PG atomic failure/commit recovery: `TestWorkspaceAnalysisReceiptFailureRepositoryAtomicClosureReplayAndCommitRecovery`. PG positive terminal proof: `TestWorkspaceAnalysisPersistenceMigrationDeadlineAndReceiptProofsRejectInvalidEvidence/receipt expected hash and failure code` (`internal/platform/migration/workspace_analysis_persistence_integration_test.go:633-690`). Unit routing: `TestFinalizeWorkspaceAnalysisReceiptFailureTerminalErrorMapsExactProof`. | No public API/live River terminal Answer path. |
| `WORKSPACE_ANALYSIS_RESULT_UNKNOWN` | Replacement Attempt sees an old `STARTED` call whose result cannot be proved; atomically closes `UNKNOWN/UNKNOWN_CHARGED` | Yes, fault | Actual process/River/Answer: `run_workspace_analysis_worker_restart_smoke` (`deploy/compose-rag-smoke.sh:976-1048`) proves SIGKILL, River rescue, `lease_lost`, replacement Attempt, exact-one proof, and failed Answer. PG Tool replacement: `TestWorkspaceAnalysisToolAuthorizationReplacementAttemptChargesUnknown` (`internal/platform/migration/workspace_analysis_authorization_repository_integration_test.go:117-173`). | Full River proof currently exercises the interrupted Synthesis Model call; a Tool-Unknown public terminal path is only covered at PG repository level. |
| `WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED` | Node context or exact pre-authorization call deadline lacks the durable-completion margin | Yes, fault/time boundary | PG final Answer + response-loss replay: `TestWorkspaceAnalysisPreoperationDeadlineFinalizerInitialGitCommitsBundleAndReplays` (`internal/platform/migration/workspace_analysis_deadline_terminalization_integration_test.go:44-122`), plus slot coverage tests for later operations. PG admission: `TestWorkspaceAnalysisToolAuthorizationRejectsPreAuthorizationDeadlineWithoutFacts`. | No public API/live River/browser terminal path. |
| `WORKSPACE_ANALYSIS_MODEL_FAILED` | A persisted Model Call and Model Run both close `failed` | Yes, Provider fault | PG call/operation/budget closure: `TestWorkspaceAnalysisModelOperationFailureRefusalAndUnknownClosureIntegration/failed` (`internal/agent/adapter/postgres/workspace_analysis_model_operations_integration_test.go:352-420`). Unit terminal mapping/public result covers the reason. | No real final Answer publication and no public API/live River/browser path. |
| `WORKSPACE_ANALYSIS_TOOL_FAILED` | An exact approved Tool Call closes `FAILED` with a stable error | Yes, Tool fault | PG call/operation/budget closure and replay: `TestWorkspaceAnalysisToolAuthorizationReconcileAndFailureSettlement` (`internal/platform/migration/workspace_analysis_authorization_repository_integration_test.go:26-115`). Unit terminal mapping/public result covers the reason. | No real final Answer publication and no public API/live River/browser path. |
| `WORKSPACE_ANALYSIS_RUNTIME_FAILED` | Workflow node fails before any Model/Tool operation fact exists | Yes, orchestration fault | PG RuntimeCoordinator + terminal hook + replay: `TestWorkspaceAnalysisRuntimeFailureHookClosesPendingAnswerWithoutFabricatingCall` (`internal/conversation/adapter/postgres/workspace_analysis_terminal_hook_integration_test.go:98-204`). | Uses the real PostgreSQL runtime coordinator but not a live River worker delivery/browser. |
| `WORKSPACE_ANALYSIS_CANCELLED` | Public control requests cancel; direct or next safe checkpoint closes Workflow, Analysis Run, Draft, Answer, events, audit | Yes, user control | PG runtime/control: `TestWorkspaceAnalysisCancellationTerminalHookDirectRuntimeCancelClosesPublicationAndReplays` and `TestWorkspaceAnalysisCancellationTerminalHookCheckpointCancelClosesAttemptBoundPublication` (`internal/conversation/adapter/postgres/workspace_analysis_terminal_hook_integration_test.go:29-96`, `357-395`). PG commit-response loss: `TestWorkspaceAnalysisFinalizerCancellationCommitsBundleAndRecoversExactResponseLoss`. Compose/browser Stop: `deploy/compose-rag-smoke.sh:1635-1639`, `web/e2e/workspace-analysis.smoke.spec.ts:438-558`. | No reason-specific gap; it is absent only from a single aggregate terminal matrix. |

### Recovery/fault coverage outside the reason matrix

| Required fault | Current direct evidence | Audit result |
| --- | --- | --- |
| Tool receipt committed, Node completion response lost | `TestPublicConversationWorkspaceAnalysisReceiptLossLeaseReclaimReusesGitReceipt` (`cmd/worker/workspace_analysis_conversation_integration_test.go:121-172`) injects the lost completion after receipt commit and proves two River Attempts, one `lease_lost`, one success, and one Git execution (`:577-668`, `:701-768`). | Covered with actual River redelivery. |
| Duplicate delivery/budget double charge | `TestWorkspaceAnalysisConcurrentModelAuthorizationKeepsOneBudgetReservation` uses real PostgreSQL row locking and gets exactly one create plus one reconcile (`internal/agent/adapter/postgres/workspace_analysis_concurrency_fault_integration_test.go:19-71`). Receipt-loss River redelivery also proves no second observation/charge. | Covered, although only the receipt-loss case is a live River redelivery. |
| Authorization/cancel race | `TestWorkspaceAnalysisCancellationRacingModelAuthorizationHasNoOrphanFacts` (`internal/agent/adapter/postgres/workspace_analysis_concurrency_fault_integration_test.go:73-141`). | Covered with real PostgreSQL and Runtime Cancel. |
| Worker process restart | `make compose-workspace-analysis-worker-restart-smoke` reaches `run_workspace_analysis_worker_restart_smoke`, actual `SIGKILL`, explicit replacement Worker, River rescue metadata, `lease_lost`, `UNKNOWN_CHARGED`, and one `RESULT_UNKNOWN` proof (`deploy/compose-rag-smoke.sh:976-1048`; contract assertions in `deploy/compose-workspace-analysis-worker-restart-smoke-contract.sh:18-41`). | Covered end to end. |
| Last-Event-ID replay | Compose records a pre-run watermark, reconnects with `Last-Event-ID`, and requires both started/terminated events (`deploy/compose-rag-smoke.sh:1578-1580`, `1621-1633`). `TestHandlerRealPostgreSQLCursorRecoveryAndFreshWatermark` independently proves retained-cursor replay, expiry/future errors, header precedence, and Workspace isolation (`internal/events/http/handler_postgres_integration_test.go:30-118`). | Covered at real HTTP/PostgreSQL level. The Playwright scenario does not itself force an SSE disconnect/reconnect. |
| Refresh/re-entry | The Playwright test opens the same terminal conversation in a new mobile context and reconstructs heading/timeline from authoritative APIs (`web/e2e/workspace-analysis.smoke.spec.ts:564-578`). | Re-entry is covered. A literal same-page `reload()` assertion is absent; add it if checklist “refresh” is intended literally. |
| Finalizer response loss | Cancellation and deadline finalizer integration tests both inject commit acknowledgement loss and require exact replay; Tool receipt failure has a corresponding repository test. | Covered at PostgreSQL finalizer/repository level, not for every reason. |

### Smallest honest aggregate executable gate

Do not label the current `make workspace-analysis-integration` as the missing terminal matrix. It is a useful broad regression target (`Makefile:228-232`), but only two `cmd/worker` tests exist and they cover success plus receipt-loss recovery.

Recommended implementation:

1. Add one table-driven integration test, for example `TestPublicConversationWorkspaceAnalysisReachableTerminalMatrixThroughRiver`, under `cmd/worker/`. It should run the 13 operationally reachable reasons (all values except `WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED`) through the production dispatcher, actual River client/worker, production finalizer/terminal hook, public Answer GET, and PostgreSQL facts.
2. Inject faults only at owned boundaries: deterministic Model adapter outcomes, exact Tool executor/result-receipt outcomes, runtime coordinator failure before a call, database-time deadline before authorization, and public cancel control. Do not directly update Answer/Analysis terminal columns or insert termination proofs in this River test.
3. For every scenario assert the public Answer status/result/reason/model-run nullability, one matching publication/termination proof, one terminal event, no pending Draft/Answer, no new calls after terminal, and exact budget closure. Also assert the scenario actually crossed its intended seam so a fallback runtime failure cannot satisfy the case accidentally.
4. Add `TestWorkspaceAnalysisV1CanonicalOperationPrefixesAlwaysFitFrozenBudget` at the domain/application layer. Enumerate every valid source-read count, synthesis-profile output cap, and successful operation prefix; prove the exact next reservation always fits. Keep the existing PostgreSQL negative proof test. This is the honest v1 evidence for the budget reason.
5. Add a narrow gate script patterned after `deploy/collection-health-go-gate.sh` with exact `require_tests` checks, then expose:

```make
workspace-analysis-terminal-matrix:
	go test -race -tags=integration -count=1 -p 1 -timeout 10m \
		-run '^TestPublicConversationWorkspaceAnalysisReachableTerminalMatrixThroughRiver$$' ./cmd/worker
	go test -count=1 \
		-run '^TestWorkspaceAnalysisV1CanonicalOperationPrefixesAlwaysFitFrozenBudget$$' ./internal/agent/domain
	go test -race -tags=integration -count=1 -p 1 -timeout 5m \
		-run '^TestWorkspaceAnalysisPersistenceMigrationBudgetExhaustionProofRequiresCausalFixedOverage$$' ./internal/platform/migration

workspace-analysis-fault-smoke: workspace-analysis-terminal-matrix
	bash deploy/compose-workspace-analysis-worker-restart-smoke.sh
```

6. Add one literal `desktop.reload()`/authoritative reconstruction assertion to the existing Playwright flow if “refresh” is meant more narrowly than the already-covered new-context re-entry.

Task wording should be revised before checking it off:

```text
Exercise every operationally reachable v1 terminal reason in one aggregate real River gate;
prove BUDGET_EXHAUSTED is unreachable for every valid frozen-v1 operation prefix and that
PostgreSQL rejects any non-causal budget terminal proof.
```

This keeps the stable public enum for compatibility without claiming a fabricated v1 runtime outcome.

### Historical checklist disposition

The following was the disposition at research time. The 2026-08-18 implementation update above supersedes the first four
items; the final production-release item remains external and unchecked:

- `.trellis/tasks/08-15-workspace-agent-user-flow/implement.md:111` and `:115`: the parent fault-test item and full terminal River matrix are incomplete until the reachable-reason River test exists; the current budget wording is impossible literally.
- `.trellis/tasks/08-15-workspace-agent-user-flow/implement.md:176`: response loss, restart, duplicate delivery, cancellation, Last-Event-ID, and re-entry have evidence, but a literal refresh and “every budget terminal reason” do not.
- `.trellis/tasks/08-15-workspace-agent-user-flow/prd.md:96` (`AC6`) is currently checked but overstates direct evidence. It should be reopened or clarified to treat v1 budget exhaustion as a proved non-reachability invariant; it must not be supported by a fabricated positive run.
- `.trellis/tasks/08-15-workspace-agent-user-flow/prd.md:100`, `implement.md:225`, and `implement.md:228`: post-fact production routing to a compatible API reader during rollback is not proved by the pre-enable compatibility matrix.
- `implement.md:232-236`: additive production migration, matched deployment, canary monitoring, expansion, and rollback execution are external release actions and remain unchecked without deployment authority and environment evidence.

### Related specs and task contracts

- `.trellis/spec/backend/workspace-analysis-contract.md:82-110` freezes the six-node DAG, recovery semantics, and terminal ownership.
- `.trellis/spec/backend/workspace-analysis-contract.md:126-146` requires fail-closed admission and stable terminal mapping.
- `.trellis/spec/backend/workspace-analysis-contract.md:160-184` requires PostgreSQL/River/browser evidence and specifically requires the lease-loss -> replacement -> Unknown -> Answer chain.
- `.trellis/tasks/08-15-workspace-agent-user-flow/prd.md:57-70` defines durable budget and terminal publication behavior.
- `.trellis/tasks/08-15-workspace-agent-user-flow/prd.md:89-101` contains AC5-AC11 and the currently overstated AC6.
- `.trellis/tasks/08-15-workspace-agent-user-flow/implement.md:99-125`, `:171-183`, and `:218-236` contain the remaining fault, E2E, compatibility, and release gates.

### External references and versions

No external behavioral claim was needed. This audit uses first-party repository source, migrations, specs, and tests. Relevant pinned implementation versions are River `v0.40.0`, `riverpgxv5 v0.40.0`, pgx `v5.10.0`, and Eino `v0.9.13` (`go.mod:6`, `:14`, `:21-23`).

## Caveats / Not Found

- This was a static evidence audit. It did not rerun the real PostgreSQL, Docker Compose, or Playwright gates; test existence and asserted behavior were read from source.
- No existing top-level test drives all terminal reasons through live River. `make workspace-analysis-integration` aggregates packages, not terminal scenarios.
- No production `BudgetRequest` construction was found. The only matches are validation tests.
- No positive PostgreSQL `WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED` termination test was found; the existing named test proves false claims are rejected.
- The Playwright file accepts `refused` as an expected terminal, but the Compose script currently invokes only `cancelled` and `completed`; support code alone is not executed evidence.
- The current public terminal union includes `WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED`. Removing it would be a wire/database compatibility change and is not recommended as a shortcut.
