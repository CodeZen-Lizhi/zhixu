# Research: Document Draft authoring backend reuse

- Query: Inspect existing `core.document` / `core.article_revision` persistence, Change Control `file_patch` Proposal, Approval/Safe Writeback terminal facts, and finalizer integration points for Document Draft authoring.
- Scope: internal
- Date: 2026-08-03

## Findings

### Conclusion

- The database already supplies the formal Document and Article Revision tables, but there is no production Go repository, domain model, or application service for either table. Authoring must own that code; the reusable part is the schema, not an existing Document module.
- `changecontrol/application.Service.CreateProposal(context.Context, CreateCommand) (CreateResult, error)` is the correct existing `file_patch` entry point. It already creates Proposal + immutable Proposal Revision atomically and replays `(workspace_id, idempotency_key)` exactly.
- The first immutable cross-module fact proving the Git result was durably mapped is `change_control.proposal_commit`. It is the appropriate source for an Authoring finalizer; Proposal `completed` is later reindex completion, and outbox `published_at` is transport state only.
- Critical blocker: current Change Control target reading and Safe Writeback are replacement-only. Both require an existing regular Markdown file. The design's "existing missing-file hash contract" does not exist, so a new blank Document cannot publish to a new path without an additive Change Control create-target contract.

### Files found

| File | Relevant fact |
| --- | --- |
| `.trellis/tasks/08-02-document-draft-authoring/prd.md:15` | Working Draft, freeze, publication binding, and post-commit finalizer requirements. |
| `.trellis/tasks/08-02-document-draft-authoring/design.md:3` | Proposed Authoring ownership, tables, API, bridge, and recovery flow. |
| `migrations/00034_learning_ops_auth.sql:8` | Existing `core.document` and `core.article_revision` schema. |
| `internal/changecontrol/application/service.go:19` | Existing `TargetReader` and Proposal application service signatures. |
| `internal/changecontrol/domain/model.go:59` | Proposal/Revision/Approval identities and status model. |
| `internal/changecontrol/domain/workspace_store.go:114` | Workspace Markdown target policy and 10 MiB Safe Writeback limit. |
| `internal/changecontrol/domain/typed_proposal.go:608` | Exact `file_patch` Proposal Revision validation. |
| `internal/changecontrol/adapter/postgres/repository.go:57` | Atomic Proposal + Proposal Revision creation and exact replay. |
| `internal/changecontrol/adapter/localfs/reader.go:36` | Existing-file-only target hash/content reader. |
| `internal/changecontrol/adapter/localfs/writer.go:92` | Existing-file-only Safe Writeback target acquisition. |
| `migrations/00009_safe_writeback.sql:332` | Immutable Proposal Commit mapping and database binding trigger. |
| `internal/changecontrol/application/writeback_service.go:348` | Git commit recovery/checkpoint and subsequent publish step. |
| `internal/changecontrol/adapter/postgres/repository_writeback.go:528` | Atomic Commit Mapping + verifying + reindex outbox transaction. |
| `internal/retrieval/adapter/postgres/completion.go:41` | Later reindex terminal transaction that sets Proposal/Execution `completed`. |
| `internal/artifact/adapter/changecontrol/publication_creator.go:21` | Narrow Change Control adapter and stable external idempotency-key pattern. |
| `internal/artifact/application/command_service.go:271` | Reservation-aware external Proposal creation and response-loss closure pattern. |
| `internal/foundation/strictjson/strictjson.go:12` | Reusable bounded decoder that rejects duplicate and unknown JSON fields. |
| `internal/artifact/http/handler.go:830` | Existing HTTP pattern for strict JSON and exactly one `Idempotency-Key`. |
| `cmd/api/main.go:470` | API composition point for target reader and Change Control service. |
| `cmd/worker/main.go:875` | Worker composition point for Safe Writeback and periodic maintenance. |
| `internal/app/router.go:49` | HTTP dependency/router boundary; it currently has no Authoring handler. |

### Existing Document schema: reusable facts and missing invariants

`core.document` currently provides (`migrations/00034_learning_ops_auth.sql:8`):

- Workspace ownership, nonblank relative POSIX `canonical_path`, nonblank title with `octet_length(title) <= 512`, lifecycle `DRAFT|PUBLISHED|ARCHIVED|DELETED`, nullable `current_published_revision_id`, positive CAS-style `version`, and unique `(workspace_id, canonical_path)`.
- Its current-revision FK is `(current_published_revision_id, workspace_id) -> core.article_revision(id, workspace_id)` (`:64`). It proves same Workspace, not that the Revision belongs to this Document.

`core.article_revision` currently provides (`migrations/00034_learning_ops_auth.sql:28`):

- Workspace/Document identity, optional parent, positive `revision_no`, nonblank content, lowercase SHA-256 `content_hash`, status `DRAFT|REVIEW|APPROVED|PUBLISHED|SUPERSEDED|ARCHIVED`, optional Git commit, and creator type.
- Unique `(document_id, revision_no)` and composite Workspace FKs exist. The parent FK is `(parent_revision_id, workspace_id)` (`:47`), so it does not prove the parent belongs to the same Document.
- A partial unique index allows only one row with `status='PUBLISHED'` per Document (`:73`). A later publication finalizer must lock the Document and change the previous published Revision to `SUPERSEDED` before marking the new Revision `PUBLISHED`.
- `PUBLISHED` requires a non-null Git commit (`:50`), but no migration or Go code currently installs an Article Revision immutability trigger. Content/hash/parent/revision number immutability therefore needs an additive database guard or equally strict repository enforcement; the allowed finalizer mutation must be limited to status/Git publication fields.
- Content has no byte-size constraint. Authoring must reject a frozen/published body larger than `changecontrol/domain.MaxWritebackContentBytes` (10 MiB) before creating a Proposal (`internal/changecontrol/domain/workspace_store.go:14`).

Repository search on 2026-08-03 found no `core.document` or `core.article_revision` read/write in production Go under `internal/`. The only non-migration references outside docs are count assertions proving Artifact does not create Documents (`cmd/api/artifact_integration_test.go:64`, `cmd/worker/artifact_generation_success_integration_test.go:74`). There is no existing Document repository to inject or extend.

Recommended Authoring persistence constraints:

- Freeze must run command-receipt lookup, Working Draft `FOR UPDATE`/expected-version CAS, Document insert/update, next Revision allocation, parent binding, and receipt write in one transaction. Lock the Document before allocating `revision_no`; do not use an unlocked `MAX(revision_no)+1`.
- Strengthen or revalidate same-Document bindings for `parent_revision_id` and `current_published_revision_id`; existing FKs only prove same Workspace.
- Treat `canonical_path` as stable after publication begins, or reject path changes while a publication binding is nonterminal. Otherwise an older Proposal can commit to path A after the Document has moved to path B.
- Permit at most one nonterminal publication binding per Document, or define and enforce monotonic Revision publication. Per-Revision and per-Proposal uniqueness alone does not prevent two approved Revisions racing and moving the current pointer backward.
- `working_draft_command` must include autosave update receipts as well as create/freeze/publish because the PRD requires every command to carry one Workspace-scoped key (`prd.md:49`); the design currently names only create/freeze/publish (`design.md:15`).

### Exact Change Control bridge

Existing signatures (`internal/changecontrol/application/service.go:19`, `:95`, `:154`, `:160`):

```go
type TargetReader interface {
    CurrentHash(context.Context, foundation.ID, string) (string, error)
}

type CreateCommand struct {
    WorkspaceID     foundation.ID
    TargetPath      string
    IdempotencyKey  string
    BaseHash        string
    Content         string
    EvidenceSummary string
    RiskLevel       domain.ProposalRiskLevel
    Risk            string
    RollbackPlan    string
}

func (s *Service) CreateProposal(
    context.Context,
    CreateCommand,
) (CreateResult, error)
```

`CreateResult.Proposal` contains both `Proposal.ID` and `Proposal.Revision.ID`; the immutable Authoring binding must persist and revalidate both IDs, not Proposal ID alone (`internal/changecontrol/domain/model.go:59`).

Bridge constraints:

- Create a narrow Authoring-owned interface exposing only `CreateProposal`; inject the existing `*changecontrol/application.Service` in `cmd/api/main.go` after its construction at `:483`. Follow the adapter shape in `internal/artifact/adapter/changecontrol/publication_creator.go:21`, but do not reuse the typed `publish_artifact` operation because it has no Document or Safe Writeback execution semantics.
- Rebuild `TargetPath`, `Content`, and content hash from the frozen server-side Article Revision. Call the trusted target reader for the base state; never accept body/base hash/evidence/risk/rollback from HTTP.
- Authoring must apply the task's stricter `.md` rule. Shared `ValidateWorkspaceTarget` accepts both `.md` and `.markdown` and rejects `.knowledge`/`.git` (`internal/changecontrol/domain/workspace_store.go:114`).
- Pass an explicit exact risk enum (`CRITICAL|HIGH|MEDIUM|LOW`) and fixed server-owned narratives. New Proposal creation requires the risk-aware v2 request hash (`.trellis/spec/backend/database-guidelines.md:1390`).
- `CreateProposal` validates a canonical target, SHA-256 base hash, nonblank content, and exact Change Hash (`internal/changecontrol/application/service.go:161`, `internal/changecontrol/domain/typed_proposal.go:608`). The PostgreSQL repository creates Proposal and Revision in one transaction and replays only an exact type/risk/request binding (`internal/changecontrol/adapter/postgres/repository.go:57`).
- The Proposal service does not enforce the 10 MiB Safe Writeback content limit; that validation happens later in `ValidatePrepareWrite` (`internal/changecontrol/domain/workspace_store.go:147`). Freeze/publish must enforce it early so an approved Proposal cannot be guaranteed to fail during writeback.
- Use a Change Control idempotency key derived from immutable `(workspace, document, article revision, target path, exact content hash)` rather than forwarding the caller command key. Keep the caller key for the Authoring receipt. This is the same two-level identity pattern used by Artifact at `internal/artifact/adapter/changecontrol/publication_creator.go:77`.
- Because Change Control owns its own transaction, Authoring cannot atomically create the Proposal and binding. Reuse the Artifact sequence `probe -> domain preflight -> durable reservation -> external create -> binding/receipt/reservation close` (`internal/artifact/application/command_service.go:271`; contract at `.trellis/spec/backend/artifact-contract.md:58`). Stable external idempotency recovers a response-loss Proposal through `FindProposalByIdempotencyKey(context.Context, workspaceID, key)` (`internal/changecontrol/adapter/postgres/repository.go:423`).
- Canonicalize line endings once before hashing/freezing. `ComputeChangeHash` normalizes CRLF to LF (`internal/changecontrol/domain/model.go:205`), while Safe Writeback `result_hash` hashes raw bytes (`internal/changecontrol/domain/workspace_store.go:139`). The final frozen `content_hash` must equal SHA-256 of the exact bytes written and later equal `proposal_commit.result_hash`.

### Critical missing-file incompatibility

The statement at `.trellis/tasks/08-02-document-draft-authoring/design.md:63` that a new file can use an existing missing-file hash contract is not supported by current code:

- `localfs.Reader.CurrentHash` calls `openSafeTarget` and opens a real target (`internal/changecontrol/adapter/localfs/reader.go:36`). There is no absent-target value or sentinel hash.
- `localfs.Writer.AcquireTarget` opens the target before and after acquiring path/inode locks (`internal/changecontrol/adapter/localfs/writer.go:92`, `:165`).
- `openSafeRegular` returns `WRITEBACK_TARGET_NOT_FOUND` on `os.ErrNotExist` (`internal/changecontrol/adapter/localfs/writer.go:932`). Resume and compensation also depend on original inode/mode/backup identity.
- Supplying SHA-256 of empty content or all zeros cannot solve this: it cannot distinguish an absent path from an existing empty file, and the reader/writer fail before a content CAS can succeed.

Required prerequisite for the PRD's new-file path: Change Control must add an explicit create-target mode owned by its Reader/Writer/Saga. It must durably bind path absence, lock by canonical path without an inode, validate parent/root identity, atomically create without replacing a concurrently appeared file, support Git add, record create-mode recovery facts, and roll back only the file created by that execution. Existing-file replacement behavior must remain unchanged. Restricting the MVP to existing files is the only implementation that works without this addition, but it contradicts the blank-new-Document goal.

### Safe Writeback terminal fact and Authoring finalizer

The existing sequence is:

1. `resumeGitPrepared` recovers or creates the exact Git commit and checkpoints Execution to `git_committed` (`internal/changecontrol/application/writeback_service.go:348`).
2. `resumePublish` builds a deterministic Proposal Commit plus retrieval outbox (`:449`, `:653`).
3. `Repository.PublishWriteback` atomically inserts/replays `proposal_commit`, moves Execution and Proposal to `verifying`, and inserts the reindex outbox (`internal/changecontrol/adapter/postgres/repository_writeback.go:528`).
4. Much later, `CompleteReindexTx` atomically marks reindex Delivery, Execution, and Proposal completed (`internal/retrieval/adapter/postgres/completion.go:41`, `:142`). Outbox `published_at` proves dispatch only, not business completion (`.trellis/spec/backend/database-guidelines.md:475`).

`change_control.proposal_commit` is immutable and uniquely binds Execution, Proposal, Proposal Revision, Approval, Workspace/Git commit, target path, diff hash, and result hash. Its insert trigger validates those fields against the approved Execution (`migrations/00009_safe_writeback.sql:332`, `:356`). This is the correct authoritative input to finalization. Do not finalize from a frontend status, `workflow.outbox_event.published_at`, or Proposal status alone.

There is no existing generic completion callback/finalizer hook: `WritebackServiceDependencies` contains only repository, Workspace, Git, audit, IDs, and clock (`internal/changecontrol/application/writeback_service.go:37`), and its only publish outbox is retrieval-specific. Recommended integration:

- Add an Authoring-owned idempotent reconciler that scans PENDING/retryable bindings joined to immutable `proposal_commit` by exact `(proposal_id, proposal_revision_id)`. Run it at worker startup/periodically using the maintenance pattern in `cmd/worker/main.go:523`, and allow an explicit status/read path to request the same reconciliation.
- In one fixed-lock-order PostgreSQL transaction, lock binding, Document, target Revision, and any previous published Revision; verify Workspace, Document/Revision, Proposal/Proposal Revision, frozen path, exact byte content hash/result hash, and Git commit. Then supersede the previous published Revision, set the target Revision `PUBLISHED` with Git commit, update Document current pointer/lifecycle/version, and mark the binding `PUBLISHED`.
- Exact replay must return the already published result without a second mutation. A conflicting commit/path/hash/current-revision binding must fail closed and persist recovery diagnostics without changing frozen identities.
- Do not wait for Proposal `completed`: that represents reindex completion, whereas the task defines Document publication after durable Git commit mapping. Keep indexing state separately visible if the UI needs it.
- If event-driven latency is required later, introduce a generic Change Control `file_patch committed` owner event/outbox. Do not put Authoring SQL into `PublishWriteback`, and do not consume the retrieval-specific event as an Authoring contract.

The binding state design also needs one correction: `design.md:21` only permits `PENDING -> PUBLISHED|RECOVERY_REQUIRED`, while `design.md:67` requires background/explicit recovery. Either transient failures remain PENDING, or the state machine must explicitly allow a validated `RECOVERY_REQUIRED -> PUBLISHED` recovery transition.

### HTTP and composition reuse

- Reuse `strictjson.DecodeObject[T](raw, limits, validate)` for mutation bodies; it rejects duplicate keys, unknown fields, trailing values, invalid Unicode, and resource-limit violations (`internal/foundation/strictjson/strictjson.go:51`). Generic `httpapi.DecodeJSON` is capped at 1 MiB but does not reject duplicate object fields (`internal/httpapi/response.go:56`).
- Follow `internal/artifact/http/handler.go:830`: validate `application/json`, read a bounded body, configure `strictjson.Limits`, and require exactly one UTF-8, trimmed, <=128-byte, control-free `Idempotency-Key` using `Header.Values` (`:848`). Default strict JSON limits allow only a 32 KiB string (`internal/foundation/strictjson/strictjson.go:40`), so Authoring needs explicit body/string limits consistent with the 10 MiB writeback ceiling.
- Add Authoring to `internal/app/router.Dependencies` and register its routes without changing existing Proposal/Artifact routes (`internal/app/router.go:49`). Compose the API repository/service/bridge beside the existing Change Control service (`cmd/api/main.go:470`). Compose the finalizer in the worker beside Safe Writeback/reindex components (`cmd/worker/main.go:875`, `:2022`).

### Timeline caveat

`migrations/00037_timeline_impact_hardening.sql:505` emits `VERSION_PUBLISHED` with aggregate kind `ARTICLE_REVISION` but uses `change_control.proposal_revision.id` as the aggregate ID (`:527`; backfill at `:671`). It is not a `core.article_revision` publication fact and must not be used as the Authoring finalizer trigger or identity mapping.

### Related specs

- `.trellis/spec/backend/database-guidelines.md:294` - Safe Writeback persistence, immutable mapping, response-loss recovery, and atomic publish transaction.
- `.trellis/spec/backend/database-guidelines.md:448` - Reindex delivery/business terminal contract; `published_at` is transport only.
- `.trellis/spec/backend/database-guidelines.md:1390` - exact Proposal risk level and v2 request-hash requirements.
- `.trellis/spec/backend/artifact-contract.md:58` - reservation-aware external side-effect pattern applicable to Proposal creation.
- `.trellis/spec/backend/directory-structure.md` - package ownership and dependency direction for the new `internal/authoring` module.
- `.trellis/spec/backend/database-guidelines.md` - additive migrations, Workspace composite identity, CAS, append-only facts, and guarded Down conventions.

### External references

None. This query concerns repository-local contracts and versions; no external documentation was required.

## Caveats / Not Found

- No production `core.document` / `core.article_revision` repository or service was found; only schema and negative Artifact integration assertions exist.
- No missing-target hash/state contract, create-new-file Safe Writeback path, or generic post-commit owner hook was found.
- No database-enforced Article Revision content immutability was found.
- The task design does not yet resolve pending publication path drift, multiple in-flight Revisions for one Document, or recovery from `RECOVERY_REQUIRED`; these must be frozen as explicit invariants before implementation.
