# Research: Graph Candidate / Change Control Transaction Boundary

- Query: Freeze the existing Graph semantic-link Candidate and Candidate Confirm transaction contract, identify the minimum staged GORM boundary with Change Control, and define the TODO 9 PostgreSQL parity matrix.
- Scope: internal
- Date: 2026-08-20

## Findings

### Executive conclusion

The Candidate data path can be migrated inside Graph, but Candidate Confirm has a real dependency blocker before implementation:

1. The legacy Confirm adapter owns one transaction across `graph.semantic_link_candidate`, `graph.semantic_link_candidate_decision`, `change_control.proposal`, and `change_control.proposal_revision` (`internal/graph/adapter/postgres/candidate_confirm.go:20-29`, `internal/graph/adapter/postgres/candidate_confirm.go:47-89`).
2. The staged Change Control GORM repository exposes only transaction-owning `CreateKnowledgeChangeProposal`; its helper starts `UnitOfWork.Within` itself (`internal/changecontrol/adapter/postgres/gorm_proposal.go:71-112`, `internal/changecontrol/adapter/postgres/gorm_proposal.go:231-288`). Calling it from a Graph-owned transaction would create a second transaction and break atomic rollback.
3. Change Control currently exposes scoped APIs only for its Workflow cancellation/read and its own Event/Workflow collaborators; it does not expose a scoped Knowledge Proposal create/read Port (`internal/changecontrol/adapter/postgres/gorm_writeback.go:21-28`, `internal/changecontrol/domain/repository.go:58-62`).
4. Graph therefore must not port the current direct `change_control.*` SQL into its GORM sibling. The missing owner capability must be added first: an opaque-scope Change Control Port that can create-or-exactly-load a typed Knowledge Proposal and load the immutable initial Revision while remaining inside the caller's live `foundation.TransactionScope`.

This is an implementation dependency, not a TODO 9-only verification gap. Until the scoped Change Control Port exists, a compliant staged GORM Candidate Confirm cannot be implemented without either a nested transaction or continued cross-owner table ownership.

### Files found

| File | Description |
| --- | --- |
| `.trellis/tasks/08-19-gorm-graph-migration/prd.md` | Graph child goal, staged-only constraint, dependencies, and acceptance criteria. |
| `.trellis/tasks/08-19-gorm-changecontrol-migration/prd.md` | Change Control staged ownership and opaque-scope constraints. |
| `.trellis/tasks/08-19-gorm-changecontrol-migration/design.md` | Change Control GORM transaction, JSON/array, error, response-loss, and TODO 9 contracts. |
| `internal/graph/candidateconfirm/contract.go` | Public Confirm command/result/Port plus v1/v2 canonical hashes. |
| `internal/graph/application/candidate_service.go` | Application routing between ordinary decisions and the Confirm Port. |
| `internal/graph/adapter/postgres/candidate_repository.go` | Candidate upsert, list/get hydration, ordinary decisions, receipt replay, and candidate storage codecs. |
| `internal/graph/adapter/postgres/candidate_confirm.go` | Legacy cross-schema Confirm transaction and stable error mapping. |
| `internal/changecontrol/domain/repository.go` | Current Change Control Repository interfaces; no scoped Knowledge Proposal interface. |
| `internal/changecontrol/adapter/postgres/gorm_core.go` | Staged GORM root/UoW and error/context helpers. |
| `internal/changecontrol/adapter/postgres/gorm_proposal.go` | Staged typed Proposal implementation; currently transaction-owning only. |
| `internal/platform/postgres/transaction.go` | Shared GORM UoW and live-scope unwrapping. |
| `internal/foundation/transaction.go` | Database-independent `UnitOfWork` and opaque `TransactionScope`. |
| `migrations/00024_semantic_link_candidates.sql` | Candidate/decision/evidence schema, constraints, indexes, and triggers. |
| `migrations/00082_proposal_revision_three_way_merge.sql` | Current Proposal revision-pointer contract and legacy nullable compatibility. |
| `internal/graph/adapter/postgres/candidate_repository_integration_test.go` | Existing lifecycle, batch hydration, workspace/version, concurrency, and append-only tests. |
| `internal/graph/adapter/postgres/candidate_confirm_integration_test.go` | Existing typed confirm, rollback, response-loss, and v1/v2 replay tests. |
| `internal/graph/adapter/postgres/candidate_approval_apply_integration_test.go` | Downstream approval/apply atomicity and Candidate-before-Proposal lock-order tests. |

### Existing application and domain contract

- `candidateconfirm.Port` deliberately exposes only `Confirm(context.Context, Command)` and requires Proposal, Revision, Decision, and Candidate state to commit together or leave no partial durable result (`internal/graph/candidateconfirm/contract.go:48-53`). No database type appears in the Port.
- The command binds Workspace, Candidate, expected version, client idempotency key, action, optional relation type, fixed Proposal risk level, free-text risk, and rollback plan (`internal/graph/candidateconfirm/contract.go:25-38`). New requests must be `HIGH`; only historical v1 replay may omit the level (`internal/graph/candidateconfirm/contract.go:57-71`, `internal/graph/candidateconfirm/contract.go:104-125`).
- The v2 request hash includes every command field, including the client idempotency key and Proposal risk level (`internal/graph/candidateconfirm/contract.go:129-165`). The frozen v1 hash omits only risk level and exists strictly for historical replay (`internal/graph/candidateconfirm/contract.go:168-203`).
- Application sends `CONFIRM` and `CONFIRM_WITH_RELATION_TYPE` exclusively through the Confirm Port, then projects only Candidate/Decision/Proposal IDs and timestamps into the HTTP receipt (`internal/graph/application/candidate_service.go:91-117`). Other actions remain on `DecideSemanticLinkCandidate` (`internal/graph/application/candidate_service.go:119-132`).
- Confirm creates a typed `knowledge_change` Proposal only; it never creates a formal Relation and never dispatches Safe Writeback (`internal/graph/adapter/postgres/candidate_confirm.go:27-29`). The downstream approved Relation apply path remains Knowledge-owned.

### Candidate persistence facts

- Candidate identity is unique by `(workspace_id,fingerprint)` and Proposal binding is globally unique by `current_proposal_id`; Decision receipt uniqueness is `(workspace_id,idempotency_key)` plus `(candidate_id,candidate_version)` (`migrations/00024_semantic_link_candidates.sql:140-145`, `migrations/00024_semantic_link_candidates.sql:219-255`).
- Candidate insert is restricted to version 1 and `ACTIVE|DEFERRED`, with no Proposal; later writes must increment exactly one version and preserve all immutable fields and monotonic `updated_at` (`migrations/00024_semantic_link_candidates.sql:514-548`).
- Candidate transition and Proposal binding are trigger-enforced. Once a Proposal is assigned it cannot change, and `SUPERSEDED` is terminal (`migrations/00024_semantic_link_candidates.sql:551-568`).
- Decision insert rechecks the current Candidate version and state and verifies that any Proposal belongs to the same Workspace and has type `knowledge_change` (`migrations/00024_semantic_link_candidates.sql:612-667`). Decision and Evidence rows are append-only (`migrations/00024_semantic_link_candidates.sql:605-609`, `migrations/00024_semantic_link_candidates.sql:672-682`).
- Stored Candidate JSONB columns are `discovery_methods`, `evidence_semantic_hashes`, and `generation`, all bounded and shape-checked by PostgreSQL (`migrations/00024_semantic_link_candidates.sql:113-135`). Evidence is stored as normalized child rows with provenance and stable `evidence_no` (`migrations/00024_semantic_link_candidates.sql:195-217`).
- List/get hydration is intentionally bounded: the root Candidate window is loaded once and all Evidence is loaded in one `ANY(uuid[])` query, then hashes and Domain invariants are revalidated (`internal/graph/adapter/postgres/candidate_repository.go:121-143`, `internal/graph/adapter/postgres/candidate_repository.go:539-608`). The existing integration assertion fixes Candidate list hydration at two queries (`internal/graph/adapter/postgres/candidate_repository_integration_test.go:43-53`).

### Frozen transaction and lock order

The staged GORM implementation must preserve the following distinct flows rather than collapsing them into a generic save helper.

#### Candidate upsert

1. Start a Graph-owned default read-write UoW.
2. Exact fingerprint lookup `FOR UPDATE`; an existing row is a replay/suppression result (`internal/graph/adapter/postgres/candidate_repository.go:66-73`).
3. Revalidate both Knowledge endpoints in the same transaction, including Workspace, current lifecycle, and the rule that a frozen endpoint version may be historical but not ahead of canonical (`internal/graph/adapter/postgres/candidate_repository.go:734-765`).
4. Lock the newest matching suppressed Candidate as the optional reopen source (`internal/graph/adapter/postgres/candidate_repository.go:715-731`).
5. `INSERT ... ON CONFLICT(workspace_id,fingerprint) DO NOTHING RETURNING`; a concurrent loser loads the committed winner in the same transaction (`internal/graph/adapter/postgres/candidate_repository.go:89-100`, `internal/graph/adapter/postgres/candidate_repository.go:658-682`).
6. Insert the complete Evidence batch, then supersede prior endpoint-pair Candidates, all before commit (`internal/graph/adapter/postgres/candidate_repository.go:102-109`, `internal/graph/adapter/postgres/candidate_repository.go:685-712`, `internal/graph/adapter/postgres/candidate_repository.go:768-776`).

#### Ordinary non-confirm decision

1. Exact receipt lookup by `(workspace,key)` before mutable state.
2. Candidate row `FOR UPDATE`.
3. If expected version is stale, recheck the receipt after acquiring the Candidate lock; return exact replay only if the same request won, otherwise return version conflict (`internal/graph/adapter/postgres/candidate_repository.go:205-229`).
4. Validate transition; for the legacy externally-created confirm seam, validate the Proposal `FOR SHARE` before any receipt (`internal/graph/adapter/postgres/candidate_repository.go:230-237`, `internal/graph/adapter/postgres/candidate_repository.go:897-909`).
5. Read PostgreSQL `CURRENT_TIMESTAMP`, insert the append-only Decision receipt, then Candidate CAS update (`internal/graph/adapter/postgres/candidate_repository.go:239-272`, `internal/graph/adapter/postgres/candidate_repository.go:275-323`).

#### Candidate Confirm

The fixed lock sequence is:

`exact Decision receipt lookup -> Candidate FOR UPDATE -> exact receipt recheck on stale version -> Proposal insert or Proposal/initial Revision FOR UPDATE -> Decision receipt insert -> Candidate CAS -> commit`

Evidence:

- The initial receipt lookup precedes the Candidate lock (`internal/graph/adapter/postgres/candidate_confirm.go:92-104`).
- The stale-version branch rechecks the exact receipt only after the Candidate lock (`internal/graph/adapter/postgres/candidate_confirm.go:108-116`).
- New Proposal/Revision creation occurs only after Candidate validation and before Decision insert (`internal/graph/adapter/postgres/candidate_confirm.go:119-180`, `internal/graph/adapter/postgres/candidate_confirm.go:186-205`).
- Proposal replay locks the Proposal, and the snapshot query locks Proposal plus initial Revision (`internal/graph/adapter/postgres/candidate_confirm.go:350-379`).
- Decision receipt is inserted before Candidate CAS (`internal/graph/adapter/postgres/candidate_confirm.go:186-217`).
- PostgreSQL triggers acquire Proposal `FOR SHARE` while validating Decision and Candidate writes, so reversing the explicit Candidate-before-Proposal order risks deadlock (`migrations/00024_semantic_link_candidates.sql:623-647`, `migrations/00024_semantic_link_candidates.sql:502-511`).
- The downstream approval/apply integration suite explicitly freezes Candidate-before-Proposal lock order (`internal/graph/adapter/postgres/candidate_approval_apply_integration_test.go:765-838`). Candidate Confirm must remain compatible with that global order.

No scoped Change Control collaborator may acquire Proposal before Graph has acquired the Candidate lock. No root GORM call, nested UoW, or post-commit Proposal write is permitted.

### Idempotency, replay, and commit response loss

- New v2 confirmation derives its private Proposal key as `semantic-link-confirm/v2:<v2-command-hash>` (`internal/graph/adapter/postgres/candidate_confirm.go:139-148`). Because the command hash includes the client key, distinct client keys intentionally create independent typed Proposals even when the semantic relation is otherwise equal.
- The Decision receipt is the first and only recovery anchor for a committed Confirm response whose return was lost. Replay must verify request hash, Candidate ID, Workspace, Proposal ID, action, and optional relation type before returning success (`internal/graph/adapter/postgres/candidate_confirm.go:235-260`). Candidate `PROPOSAL_CREATED` alone is not sufficient evidence.
- A same-key different request is a stable `RELATION_PROPOSAL_CONFIRM_CONFLICT`; an absent receipt plus stale Candidate version is also conflict, not inferred success (`internal/graph/adapter/postgres/candidate_confirm.go:108-116`, `internal/graph/adapter/postgres/candidate_confirm.go:235-239`).
- A lost commit must first return the commit error; retry on an unwrapped repository must load the exact Decision and return one Proposal, one Revision, and one Decision (`internal/graph/adapter/postgres/candidate_confirm_integration_test.go:165-218`).
- Historical v1 receipts and Proposal keys remain replayable by v1 and v2 callers, but a missing-risk v1 command cannot create new state and cannot replay a persisted v2 receipt (`internal/graph/adapter/postgres/candidate_confirm_integration_test.go:221-292`). Persisted non-`HIGH` Proposal facts fail closed for both versions (`internal/graph/adapter/postgres/candidate_confirm_integration_test.go:294-315`).
- The replay snapshot deliberately loads Revision `revision_no=1`, not the mutable/current revision pointer (`internal/graph/adapter/postgres/candidate_confirm.go:372-382`). A generic current-Proposal reader is therefore not behaviorally sufficient for response replay after later revision activity.

### Minimum staged GORM boundary

The smallest compliant staged shape is:

1. Graph owns a `GORMRepository` for Candidate upsert/read/ordinary decisions and a separate `GORMCandidateConfirmRepository` implementing the existing `candidateconfirm.Port`.
2. Both derive GORM root and `foundation.UnitOfWork` from one `*platformpostgres.Pool`; neither accepts independently injectable root/UoW, opens a pool, calls root `.Transaction`, or mutates schema.
3. `GORMCandidateConfirmRepository` owns one default read-write `UnitOfWork.Within`, locks Candidate first, and passes the live opaque scope to a Change Control owner Port.
4. The Change Control owner Port needs two exact scoped capabilities:
   - create-or-exactly-load the caller-supplied typed `knowledge_change` Proposal and immutable Revision 1 in the live scope;
   - load by exact Proposal ID the immutable Revision 1 snapshot needed by a Decision-receipt replay in the live scope.
5. The Port signature may expose `foundation.TransactionScope` and Change Control Domain values only. It must not expose `*gorm.DB`, `*sql.Tx`, `pgx.Tx`, `any`, commit/rollback, or a generic query callback. A suitable owner-defined shape is conceptually `CreateKnowledgeChangeProposalScoped(ctx, scope, proposal)` plus `GetInitialKnowledgeChangeProposalScoped(ctx, scope, proposalID)`; final naming belongs to Change Control.
6. The Change Control implementation unwraps the existing live scope through the platform adapter, validates typed JSON/risk/hash, handles `ON CONFLICT` and exact binding inside that same transaction, and never calls its own `Within` from a scoped method.
7. Foundation scope currently has no Pool identity. Graph and Change Control staged constructors must be composed from the same `platformpostgres.Pool`; TODO 9 must prove this with one fixture. A private concrete-scope type assertion is not an affinity solution (`internal/platform/postgres/transaction.go:70-97`, `.trellis/tasks/08-19-gorm-changecontrol-migration/design.md:5-14`).

This boundary requires a small Change Control owner follow-up. Implementing it solely in `internal/graph/adapter/postgres` would preserve the current cross-owner SQL but violate the Graph PRD requirement that cross-module transactions collaborate through Ports.

### Proposal revision-pointer compatibility decision

There is a verified legacy/canonical difference that planning must make explicit:

- Current Graph Candidate Confirm directly inserts `change_control.proposal` without `current_revision_id` (`internal/graph/adapter/postgres/candidate_confirm.go:288-296`).
- Current Change Control pgx and staged GORM create paths write `current_revision_id` atomically with a new Proposal (`internal/changecontrol/adapter/postgres/repository.go:232-239`, `internal/changecontrol/adapter/postgres/gorm_proposal.go:237-240`).
- Migration `00082` states that old owner projections may temporarily leave the pointer null, but all new Change Control commands write it in the same transaction; a missing pointer is legacy/read-only (`migrations/00082_proposal_revision_three_way_merge.sql:31-39`).

Recommendation: the new scoped Change Control owner method must write `current_revision_id=revision.ID` for newly-created v2 Proposals, while its exact initial-revision reader must continue to replay historical v1/v2 rows whose pointer is null. This is the current Change Control owner contract, not a reason for Graph to reproduce an obsolete direct-writer shape. TODO 9 must assert both new-pointer creation and historical-null replay. The API receipt is unaffected because Candidate Application exposes only the Proposal ID (`internal/graph/application/candidate_service.go:100-117`).

### JSONB and array binding rules

- Raw Candidate JSON must not be passed to GORM as bare `[]byte`, which the pgx stdlib path may infer as `bytea`. Use a Graph-local validated `driver.Valuer`/`sql.Scanner` that returns a string, matching the established Change Control pattern (`internal/changecontrol/adapter/postgres/gorm_model.go:10-38`).
- Preserve strict `json.Unmarshal` plus Domain validation on reads. Invalid discovery methods, generation, evidence hashes, typed Proposal JSON, or partial Approval fields are consistency failures, never empty/default results (`internal/graph/adapter/postgres/candidate_repository.go:479-527`, `internal/graph/adapter/postgres/candidate_confirm.go:389-437`).
- Candidate Evidence bulk insertion currently uses parallel `unnest` arrays (`internal/graph/adapter/postgres/candidate_repository.go:685-712`). Under GORM/database/sql, every array must be one `pq.Array` Valuer with an explicit PostgreSQL cast; raw slices must not be given to GORM expansion. The repository already uses this mature pattern for Knowledge batch evidence (`internal/knowledge/adapter/postgres/gorm_relation.go:405-426`), and `github.com/lib/pq v1.12.3` is already a direct dependency (`go.mod:16`).
- Query filters and Evidence hydration using `ANY(uuid[])` follow the same `pq.Array` rule. All dynamic SQL identifiers/order fragments must remain package-owned fixed text; user values remain parameters.

### Stable error and context classification

Candidate Confirm must keep these public families:

| Condition | Stable outward result |
| --- | --- |
| invalid IDs/action/relation/risk/rollback or new v1 create | `RELATION_PROPOSAL_CONFIRM_INVALID`, non-retryable |
| same key with different binding, expected version/CAS loss, unique competing decision | `RELATION_PROPOSAL_CONFIRM_CONFLICT`, non-retryable version conflict |
| corrupt/partial Candidate-Proposal-Revision-Approval binding or trigger contract | `RELATION_PROPOSAL_CONFIRM_CONSISTENCY`, non-retryable |
| missing/unusable dependency | `RELATION_PROPOSAL_CONFIRM_UNAVAILABLE`, retryable |
| begin/statement/commit infrastructure failure | operation-specific `RELATION_PROPOSAL_CONFIRM_*_FAILED` code with existing retry classification |

The legacy SQLSTATE mapping treats `40001`, `40P01`, `55P03`, connection states, and shutdown as retryable; `23503`, `23514`, and `55000` as consistency; and `23505` as version conflict (`internal/graph/adapter/postgres/candidate_confirm.go:516-541`). The GORM classifier must additionally handle `sql.ErrNoRows`, `sql.ErrTxDone`, and `gorm.ErrRecordNotFound`, and preserve a canceled context plus distinct `context.Cause`, following the staged Change Control helper (`internal/changecontrol/adapter/postgres/gorm_core.go:147-165`, `internal/changecontrol/adapter/postgres/gorm_core.go:202-219`).

A scoped Change Control error must not accidentally leak a different public code such as `IDEMPOTENCY_KEY_REUSED` through Candidate Confirm. Graph must map owner conflict/consistency outcomes back to the frozen Candidate Confirm error family, while retaining the original cause for `errors.Is/As`. Errors and logs must not include SQL, arguments, Candidate excerpts, Evidence JSON, Proposal typed JSON, DSN, or raw PostgreSQL messages.

### TODO 9 real PostgreSQL matrix

Only extend existing integration files. Legacy and GORM cases should use separately migrated disposable databases; each GORM fixture must construct one `platformpostgres.Pool` and derive Graph root/UoW plus the Change Control scoped collaborator from that same Pool.

#### Candidate repository parity

- Upsert create and exact fingerprint replay, including `Created/Suppressed/Reopened` flags.
- Eight-way fingerprint race: exactly one Candidate and one Evidence set; all callers return the same winner (`internal/graph/adapter/postgres/candidate_repository_integration_test.go:216-293`).
- Cross-Workspace endpoint rejection, ineligible endpoint lifecycle, frozen historical version acceptance, and future-version conflict.
- Reopen from newest ignored/false-positive Candidate and supersede prior endpoint-pair rows with monotonic version/time.
- Evidence `unnest` binding for UUID/text/int/timestamp arrays, provenance trigger failure, and whole-transaction rollback.
- Candidate JSONB round trip and corrupt JSON/enum/hash fail-closed behavior.
- List/get two-query hydration, no N+1, stable ordering/window/truncation, Topic-membership filtering, empty result, Rows close/error, and Repeatable Read + ReadOnly snapshot.
- Ordinary decision exact replay/conflict, stale-version race, Candidate CAS, append-only trigger, DB timestamp, explicit cancel/deadline, SQLSTATE, and connection release.

#### Candidate Confirm parity

- Normal and typed relation confirms create independent HIGH typed Proposals, Revision 1, one Decision, Candidate `PROPOSAL_CREATED`, no Relation, no Writeback, no Workflow (`internal/graph/adapter/postgres/candidate_confirm_integration_test.go:22-125`).
- Same-key exact replay before and after Approval; Proposal/initial Revision/Approval binding remains exact.
- Same-key different payload, reverse v1-to-v2 replay, typed endpoint incompatibility, non-HIGH persisted risk, missing/partial typed JSON, and cross-Workspace Proposal binding fail closed.
- Candidate row is locked before Proposal on create, replay, and stale-version branches; concurrent same-key and different-key confirms produce neither deadlock nor orphan Proposal.
- Failure injection after Proposal/Revision but before Decision, after Decision but before Candidate CAS, and from the scoped Change Control collaborator: no Proposal, Revision, Decision, or Candidate partial fact (`internal/graph/adapter/postgres/candidate_confirm_integration_test.go:127-163`).
- Commit response loss: wrapped Graph UoW commits then returns an injected error; first call fails, second call on an unwrapped repository replays exclusively from the exact Decision receipt and leaves one Proposal/Revision/Decision (`internal/graph/adapter/postgres/candidate_confirm_integration_test.go:165-218`). Do not wrap the Change Control scoped collaborator because it does not own commit.
- Historical v1 receipt + `semantic-link-confirm/v1:` Proposal, v2 caller replaying historical v1, new v1 rejection, persisted v2 reverse-replay rejection, and whitespace-risk rejection (`internal/graph/adapter/postgres/candidate_confirm_integration_test.go:221-315`).
- New scoped creation persists `current_revision_id=revision.id`; a seeded historical row with `current_revision_id IS NULL` remains replayable through Revision 1.
- Real `?::jsonb`, `pq.Array`, nullable UUID/time, SQLSTATE/constraint, `sql.ErrTxDone`, cancellation/custom cause, commit/rollback, and same-Pool scope behavior.

#### Plan and capacity checks

- `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)` should verify candidate Workspace/status/order queries use `idx_graph_semantic_link_candidate_workspace_status_updated`, endpoint-pair queries use `idx_graph_semantic_link_candidate_workspace_endpoints`, and Evidence hydration uses `idx_graph_semantic_link_candidate_evidence_candidate` (`migrations/00024_semantic_link_candidates.sql:184-217`).
- Confirm receipt lookup must use the unique `(workspace_id,idempotency_key)` index and Candidate lookup the `(id,workspace_id)` key; Proposal replay must use Change Control's Workspace/key uniqueness. Record actual rows, loops, sort/spill, planning/execution time, and index condition from the real plan. Compile-only or a tiny fixture is not capacity evidence.

### Rollback boundary

- Before TODO 9 and Final, production constructors remain on legacy pgx; no selector, dual write, `cmd/**` switch, migration edit, or legacy deletion belongs to this child.
- Graph rollback removes only staged Graph GORM files and the Graph-side dependency injection. The scoped Knowledge Proposal methods are Change Control owner capability and should be reverted only with their owning follow-up if unused elsewhere.
- TODO 9 absence leaves this child `in_progress`, all acceptance criteria unchecked, and the Composition switch blocked.

## External references

No external documentation was required. This research uses the repository-pinned GORM/Foundation conventions and already-selected `github.com/lib/pq v1.12.3`; no new framework or dependency is proposed.

## Related specs

- `.trellis/spec/backend/index.md`: Composition Root ownership, domain dependency direction, and formal Proposal-to-Knowledge path.
- `.trellis/spec/backend/database-guidelines.md`: explicit SQL, transaction, migration, PostgreSQL, pagination, and real-database verification contracts.
- `.trellis/spec/backend/error-handling.md`: stable error categories, retry policy, and cause preservation.
- `.trellis/spec/backend/logging-guidelines.md`: structured logging and sensitive-data redaction.
- `.trellis/spec/backend/quality-guidelines.md`: idempotency/version, bounded queries, Adapter contract, integration, EXPLAIN, and independent review requirements.
- `.trellis/tasks/08-18-gorm-data-access-migration/design.md`: opaque scope, single Pool, no cross-module concrete transaction type, staged sibling, and pgx allowlist.
- `.trellis/tasks/08-19-gorm-changecontrol-migration/design.md`: Change Control Proposal lock/replay, JSONB, scoped collaborator, same-Pool, and TODO 9 contracts.

## Caveats / Not Found

- No Change Control scoped Knowledge Proposal create/read interface exists in the inspected code. This is the critical implementation blocker described above.
- `foundation.TransactionScope` intentionally does not expose Pool identity. Same-Pool affinity can only be guaranteed by staged construction and verified in TODO 9; it cannot currently be proven inside a scoped method.
- Existing Candidate integration fixtures primarily run inside caller-owned pgx transactions (`internal/graph/adapter/postgres/repository_integration_test.go:1283-1303`). They cannot prove the staged GORM root/UoW, database/sql binding, commit response loss, or connection release without being extended.
- `ZHIXU_TEST_DATABASE_URL` availability was not assumed and no integration test was executed during research. PostgreSQL triggers, casts, locks, deadlocks, real SQLSTATE, `current_revision_id`, and EXPLAIN remain TODO 9 evidence.
- Existing Graph Candidate Confirm writes new Proposal rows without `current_revision_id`; migration `00082` identifies that shape as legacy/read-only. The planning artifacts must explicitly approve the owner-canonical new-pointer behavior and historical-null replay rather than silently treating the difference as parity.
- The Decision receipt stores Proposal ID but not Revision ID or a full response snapshot. Exact replay is therefore coupled to immutable Revision 1. Replacing that lookup with Change Control's generic current-revision projection would be a behavioral regression.
- Existing tests cover Candidate-before-Proposal lock order in the downstream approval/apply path, but no current Candidate Confirm test directly probes that order or concurrent different-key Confirm deadlocks. Those are required TODO 9 extensions; no new test file should be added without user authorization.
