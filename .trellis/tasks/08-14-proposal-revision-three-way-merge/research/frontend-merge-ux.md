# Research: Proposal revision editing and three-way merge frontend UX

- Query: Research the existing Proposal review flow and design an MVP UX for `file_patch/REPLACE` Proposal revision editing with a server-owned three-way merge.
- Scope: mixed (repository code, project specifications, installed package APIs)
- Date: 2026-08-14

## Findings

### Executive conclusion

The current Proposal detail page has the right approval safety gate, query binding, and Monaco lifecycle foundations, but it is not yet a revision editor:

1. It renders only the latest Revision and its current Workspace content. When the target drifts, the displayed Diff is `current -> proposed`, not the immutable historical `base -> proposed`, and the only recovery is an unavailable state asking the user to regenerate.
2. The API boundary exposes neither Proposal aggregate `version` nor revision history. It also reduces every non-2xx Problem to `status + message`, so the UI cannot safely distinguish second drift, a concurrent revision, unresolved conflicts, or idempotency misuse.
3. The installed Monaco version has a two-way `DiffEditor` and a normal `Editor`, but no merge-editor API. The frontend must not implement the merge algorithm. It should render server-derived views and submit a full edited candidate bound to a server-issued merge fingerprint.
4. The MVP should be an extracted full-width revision workbench inside the existing Proposal detail route, not another section added to the already dense review sidebar. Its one distinctive visual device should be a compact provenance rail: `基线 -> 当前 Workspace -> 原提案 -> 待提交 Revision`.
5. Revision history is not optional for this PRD: R1 and AC1 require old Revision, old Diff, and old Approval to remain readable. A bounded history endpoint and a read-only historical selection UI are required alongside merge preview/create endpoints.

### Files found

| File | What it owns / why it matters |
| --- | --- |
| `.trellis/tasks/08-14-proposal-revision-three-way-merge/prd.md` | Scope, immutable revision, server merge, concurrency, recovery, security, and browser acceptance criteria. |
| `.trellis/spec/frontend/state-management.md` | Query/local-draft/SSE ownership and the prohibition on storing sensitive source text in Browser Storage. |
| `.trellis/spec/frontend/component-guidelines.md` | Monaco model URI, detach, disposal, keyboard, focus, and non-color semantics contracts. |
| `.trellis/spec/frontend/quality-guidelines.md` | Explicit loading/error/version-conflict/manual-recovery states and behavior-oriented test requirements. |
| `.trellis/spec/frontend/directory-structure.md` | Feature import boundaries; Business must not deep-import Authoring. |
| `web/src/features/business/ProposalsPage.tsx` | Current list/detail review flow, approval/preflight mutations, bound current-content query, and drift blocking UI. |
| `web/src/features/business/ProposalsPage.test.tsx` | Current behavior fixtures for Monaco readiness, cache cleanup, baseline mismatch, 409 recovery, approval snapshots, and focus restoration. |
| `web/src/api/business.ts` | Proposal discriminated union, strict decoders, current-content client, approval/preflight commands, and current lossy Problem handling. |
| `web/src/api/business.test.ts` | Strict response tests, content bounds, semantic identity checks, and unknown-field rejection. |
| `web/src/shared/MonacoDiffViewer.tsx` | Shared read-only two-way Monaco Diff viewer. |
| `web/src/shared/monaco-runtime.ts` | Local Monaco worker and safe editor/Diff model release helpers. |
| `web/src/features/authoring/MonacoMarkdownEditor.tsx` | Existing editable Markdown Monaco pattern; useful implementation reference but inaccessible by deep feature import. |
| `web/src/features/authoring/NewDocumentPage.tsx` | Existing 409 pattern that refreshes authority while preserving local content, plus exact-key response-loss retry. |
| `web/src/features/settings/ModelSettingsPanel.tsx` | Existing conflict recovery that updates the server binding while retaining a dirty non-secret draft. |
| `web/src/shared/ui.tsx` | Radix Tabs and Dialog wrappers, including focus restoration. |
| `web/src/styles.css` | Existing review grid, Monaco shell, 44px controls, responsive collapse, and document overflow clipping. |
| `web/src/features/authoring/authoring.css` | Proven desktop two-pane/mobile single-mode editor pattern. |
| `web/src/events/event-store.tsx` | Proposal/approval/workflow SSE invalidation routing. |
| `web/src/events/event-store.test.tsx` | Existing Proposal detail and current-content invalidation coverage. |
| `web/src/features/business/url-state.ts` | Strict Proposal list filter parsing; a selected historical revision would require a separate opaque parameter contract. |
| `web/src/features/business/url-state.test.ts` | URL duplicate/unknown-value and round-trip tests to extend only if historical selection enters the URL. |
| `api/openapi/openapi.json` | Existing Proposal detail/current-content/approval/preflight schemas and reusable Problem contract. |
| `docs/requirements.md` | Product requirement for retaining old revisions and AC-13 three-way merge. |
| `docs/architecture/application-contracts.md` | Stable discriminated Proposal and approval-boundary rules. |
| `internal/changecontrol/domain/model.go` | Proposal has a Version internally; ProposalRevision is immutable. |
| `migrations/00004_change_control.sql` | Database already supports ordered immutable revisions and one approval per revision. |

### Existing code patterns and constraints

#### Current Proposal review flow

- `ProposalsPage` binds detail to `['business', workspaceId, 'proposal', proposalId]` and sets `gcTime: 0`; current content is also sensitive and short-lived (`web/src/features/business/ProposalsPage.tsx:224-273`).
- The current-content key contains Revision ID, target path/mode, base hash, and the returned current hash. On a changed Revision, the page refetches Proposal first, removes the old current-content query, then fetches against the new binding (`web/src/features/business/ProposalsPage.tsx:34-74`, `web/src/features/business/ProposalsPage.tsx:274-290`). Preserve this ordering after revision creation.
- Approval success invalidates Proposal lists and Workflows, then refetches Proposal facts. Approval/preflight 409 also causes an authority refetch (`web/src/features/business/ProposalsPage.tsx:291-343`). The new mutation should reuse the pattern but preserve the local candidate on failure.
- Approval is enabled only when the Proposal is `ready_for_review`, current-content retrieval succeeded, the base hash matches, and Monaco is ready (`web/src/features/business/ProposalsPage.tsx:353-377`). A newly appended Revision must return to this same gate and must not inherit an Approval.
- The current detail labels and alert establish a strict safety promise for `file_patch/REPLACE`, but on drift the page only says to regenerate (`web/src/features/business/ProposalsPage.tsx:403-495`, `web/src/features/business/ProposalsPage.tsx:549-570`). This is the replacement entry point.
- The page currently sends `current.content` as Diff original and `revision.content` as modified. Once current diverges, that view cannot reconstruct the original immutable base. The merge preview/history contract must supply base content rather than deriving it in the browser.
- The sidebar already contains preflight, decision, approval, and version facts (`web/src/features/business/ProposalsPage.tsx:571-603`). Hide/replace the ordinary decision sidebar while the revision workbench is active; approving and revising simultaneously would create ambiguous primary actions.
- The high-risk confirmation dialog already restores focus to its trigger (`web/src/features/business/ProposalsPage.tsx:606-617`). Dirty exit and restart-merge confirmations should use the same shared Dialog behavior.

#### API boundary gaps

- `ProposalRevisionBase` and `FilePatchRevision` already expose immutable Revision identity, number, hashes, content, evidence, risk, and rollback fields (`web/src/api/business.ts:37-50`).
- `ProposalDetailBase` does not expose the Proposal aggregate `version`, although the domain model has one. The append command therefore cannot currently send `expected_proposal_version` (`web/src/api/business.ts:119-133`; `internal/changecontrol/domain/model.go:71-89`).
- `ProposalCurrentContent` exposes target content/hash/base-match but no immutable base body (`web/src/api/business.ts:201-204`). It cannot serve a three-way view by itself.
- `BusinessApiError` carries only the local error kind and HTTP status (`web/src/api/business.ts:245-251`). The request helper reads only an arbitrary `message` and guesses retryability from 5xx, discarding OpenAPI `error_code`, `retryable`, and typed `details` (`web/src/api/business.ts:618-637`). This must be fixed before implementing reliable 409 UX.
- The existing decoders use exact-field rejection and semantic binding (`web/src/api/business.ts:282-330`, `web/src/api/business.ts:849-934`). New merge/history decoders must keep that standard: unknown fields, bad UUID/hash/ranges, cross-Proposal/Workspace identity, inconsistent conflict counts, and out-of-bound text must fail closed.
- Current Workspace content is capped at 1 MiB (`web/src/api/business.ts:258-318`). Latest file Revision content is decoded only as non-empty, without the same byte bound (`web/src/api/business.ts:865-883`). Base, current, proposed, candidate, and submitted content need one explicit UTF-8 byte contract.
- OpenAPI has no append-revision, revision-history, or merge-preview path. Its existing Problem schema already contains `error_code`, `message`, `retryable`, and `details`; reuse it rather than inventing a frontend-only error envelope.

#### Monaco and feature boundaries

- `MonacoDiffViewer` is read-only, side-by-side, word-wrapped, automatically laid out, and uses `keepCurrentOriginalModel`/`keepCurrentModifiedModel` (`web/src/shared/MonacoDiffViewer.tsx:5-61`). It is suitable for one selected comparison at a time.
- `monaco-runtime` configures a locally bundled worker and disposes only detached models in a microtask after the editor releases them (`web/src/shared/monaco-runtime.ts:13-44`). Every new editor URI must bind Workspace, Proposal, source Revision, merge fingerprint, and view role.
- Project spec requires route identity in the model key and validates repeated route switches with registry `2 -> 0` (`.trellis/spec/frontend/component-guidelines.md:104-165`). The editable candidate needs the same contract using `releaseDetachedEditorModel`.
- Authoring already has a controlled editable Markdown Editor with accessible label, word wrap, and `keepCurrentModel` (`web/src/features/authoring/MonacoMarkdownEditor.tsx:14-55`). Business cannot deep-import it because Features may import shared modules, not another Feature's internals (`.trellis/spec/frontend/directory-structure.md:35-43`).
- Recommended reuse: extract a generic `shared/MonacoTextEditor.tsx` accepting `ariaLabel`, `language`, `modelPath`, `value`, `disabled`, readiness/error callbacks, and dimensions. Keep `MonacoMarkdownEditor` as a thin Authoring wrapper. This removes real duplication without adding a dependency.
- Installed versions inspected in the workspace: `@monaco-editor/react@4.7.0`, `monaco-editor@0.56.0`, `@tanstack/react-query@5.101.2`, `react@19.2.7`, `react-router-dom@7.18.1`. Installed Monaco types expose `Editor`, `DiffEditor`, and multi-file diff APIs, but no `MergeEditor`, `IMergeEditor`, or `createMerge` API. No merge/diff3 dependency exists in the Go or Web manifests.

#### State, cache, and event ownership

- The spec assigns authoritative Proposal/Revision state to TanStack Query, selection/draft interactions to the nearest feature component, and SSE only to invalidation (`.trellis/spec/frontend/state-management.md:11-30`).
- Unsaved candidate content is a local, explicitly dirty, session-only value. It must not be copied into Query data and must never enter URL, `localStorage`, `sessionStorage`, logs, or telemetry (`.trellis/spec/frontend/state-management.md:41-55`). Refresh discards an unsaved edit; successful append is the durable recovery boundary.
- A user-triggered merge preview is best represented as a mutation result plus local reducer state, not a long-lived query. Reset it on close/success and do not persist it. The server must re-read current content when creating the preview.
- Proposal events are already detected by the `proposal.` prefix, and Proposal detail/current-content are invalidated (`web/src/events/event-store.tsx:168-170`, `web/src/events/event-store.tsx:220-230`; covered at `web/src/events/event-store.test.tsx:335-357`). The frozen `proposal.revised` event therefore reaches the current family automatically.
- Revision history adds two new query families which are not invalidated today: `['business', workspaceId, 'proposal-revisions', proposalId]` and `['business', workspaceId, 'proposal-revision', proposalId, revisionId]`. Add them to Proposal and conservative Workflow invalidation (`web/src/events/event-store.tsx:231-239`).
- On successful append, invalidate Proposal lists (including Dashboard consumers), Workflows, Proposal detail, current content, history, and selected historical detail. Then refetch Proposal before the new current-content binding, matching the existing sequencing.
- On 409, refetch authority but do not overwrite or silently rebind the dirty candidate. Updating its version/hash fields while keeping its body would risk overwriting changes introduced by the new current version.
- Existing Authoring conflict behavior preserves local content across a 409 and supports exact-key retry (`web/src/features/authoring/NewDocumentPage.tsx:202-303`, `web/src/features/authoring/NewDocumentPage.tsx:393-400`). Model Settings similarly refreshes the authoritative revision while retaining a dirty non-secret buffer (`web/src/features/settings/ModelSettingsPanel.tsx:498-550`). These are the closest local patterns.

#### Mobile and accessibility patterns

- Existing review layout is content plus a 290px sidebar and collapses to one column under 920px (`web/src/styles.css:357-380`, `web/src/styles.css:563-564`). The page clips document-level horizontal overflow at mobile width; inner code surfaces must scroll internally.
- Authoring uses two editor panes on desktop, then an explicit mode control and one visible pane on mobile, with full-width actions (`web/src/features/authoring/authoring.css:359-385`, `web/src/features/authoring/authoring.css:454-553`). Reuse that interaction shape rather than squeezing multiple code columns into 390px.
- Shared Radix Tabs and Dialog primitives provide keyboard tabs, accessible modal semantics, and focus restoration (`web/src/shared/ui.tsx:23-29`). Tabs should select one two-way Diff, not hide the editable candidate.
- Existing controls use a 44px minimum target, and dialogs constrain to the viewport (`web/src/styles.css:378-380`, `web/src/styles.css:548-553`). New buttons, tab triggers, and conflict navigation must keep that target.
- Conflict state must include text such as `未解决`/`已处理` and ordinal labels. Red/amber alone is prohibited by the component and quality specs. Status/error alerts should receive programmatic focus after a failed submit.
- Avoid nested cards and a three-column code wall. The existing product is a quiet operational UI; visual hierarchy should come from source provenance and one clear primary action, not decoration.

### Required product/API shape

#### Endpoints

Recommended HTTP surface, following the existing server-derived restore-preview precedent:

1. `POST /api/v1/proposals/{proposal_id}/revision-merge-previews`
   - Read-only command; server reads the immutable source Revision base, current Workspace target, and source proposed body.
   - No Idempotency Key is necessary because it has no durable side effect, but it must be `private, no-store` and authorized for local reads.
   - Request contains only source identity/binding, never caller-supplied base/current/proposed bodies.
2. `POST /api/v1/proposals/{proposal_id}/revisions`
   - Appends the immutable Revision; requires `Idempotency-Key` and write-Proposal authorization.
   - It does not approve, preflight, write the Workspace, or dispatch a Workflow.
3. `GET /api/v1/proposals/{proposal_id}/revisions`
   - Bounded newest-first summaries: Revision number/identity/change hash/base hash/created time, approval decision/time, and `is_current`.
4. `GET /api/v1/proposals/{proposal_id}/revisions/{revision_id}`
   - Read-only immutable Revision detail, immutable base body (or a dedicated bounded base-content read), proposed body, and that Revision's Approval.
   - `private, no-store`; no absolute filesystem path.

The database supports this model already: `(proposal_id, revision_no)` is unique, revisions reject update/delete, and Approval is unique per Revision (`migrations/00004_change_control.sql:16-48`, `migrations/00004_change_control.sql:77-85`).

#### Merge preview contract

The preview response should be a versioned, strictly decoded snapshot. Field names below are recommended rather than an existing contract:

```json
{
  "schema_version": "proposal-text-merge-preview/v1",
  "merge_algorithm_version": "<stable version token>",
  "merge_fingerprint": "<server-issued opaque token or 64-hex digest>",
  "proposal_id": "<uuid>",
  "workspace_id": "<uuid>",
  "proposal_version": 7,
  "source_revision_id": "<uuid>",
  "source_revision_no": 1,
  "source_change_hash": "<64hex>",
  "target_path": "notes/example.md",
  "target_mode": "REPLACE",
  "base": { "content": "...", "hash": "<64hex>" },
  "current": { "content": "...", "hash": "<64hex>" },
  "proposed": { "content": "...", "hash": "<64hex>" },
  "candidate": { "content": "...", "hash": "<64hex>" },
  "conflict_count": 1,
  "conflicts": [{
    "id": "<stable bounded token>",
    "ordinal": 1,
    "base_range": { "start_line": 10, "end_line": 12 },
    "current_range": { "start_line": 10, "end_line": 13 },
    "proposed_range": { "start_line": 10, "end_line": 11 },
    "result_range": { "start_line": 10, "end_line": 15 }
  }]
}
```

Decoder invariants:

- Proposal/Workspace/source Revision/source change hash must match the request and active route.
- `target_mode` is exactly `REPLACE`; target path is canonical Workspace-relative.
- All hashes and the merge fingerprint follow a fixed schema; conflict IDs are unique and bounded.
- `conflict_count === conflicts.length`; ordinals are contiguous; ranges are nonnegative, ordered, and within declared line bounds.
- The four full bodies have explicit UTF-8 byte maxima and the serialized preview response is capped at 16 MiB. Conflict context is derived from validated ranges instead of duplicated excerpts. Empty Markdown must be intentionally allowed or rejected consistently across domain and decoder.
- A server-issued fingerprint binds algorithm version and all three inputs. The browser cannot invent a binding or claim that a candidate is merge-safe.

#### Append command and response

```json
{
  "expected_proposal_version": 7,
  "source_revision_id": "<uuid>",
  "source_change_hash": "<64hex>",
  "expected_current_hash": "<64hex>",
  "merge_fingerprint": "<opaque server binding>",
  "content": "<complete edited candidate>",
  "evidence_summary": "...",
  "risk": "...",
  "rollback_plan": "..."
}
```

`Idempotency-Key` is a header. A successful response should return either a strictly bound full Proposal detail plus `replayed`, or this equivalent projection:

```json
{
  "proposal_id": "<uuid>",
  "proposal_version": 8,
  "status": "ready_for_review",
  "revision": { "...": "complete FilePatchRevision" },
  "approval": null,
  "replayed": false
}
```

The decoder must verify `revision_no === source_revision_no + 1`, new `base_hash === expected_current_hash`, changed Revision/change hash, `ready_for_review`, and no Approval. A replay must decode to the same Revision identity.

#### Stable Problems required by the UI

Extend `BusinessApiError` to retain strictly decoded `errorCode`, server `retryable`, status, and bounded typed details. Do not branch on HTTP 409 alone.

| Error code | Status / bounded details | UI behavior |
| --- | --- | --- |
| `PROPOSAL_REVISION_STALE` | 409; current Proposal version, current Revision ID/change hash, current content hash, enum reason | Preserve dirty candidate, refetch authority, disable create, offer explicit fresh merge. |
| `PROPOSAL_REVISION_CONFLICTS_UNRESOLVED` | 422; bounded unique conflict IDs only | Focus alert, select first unresolved conflict, keep editor content. |
| `PROPOSAL_REVISION_NOT_EDITABLE` | 409; current status/type/mode | Close or freeze workbench as read-only; show reason. |
| `IDEMPOTENCY_KEY_REUSED` | 409; no sensitive request echo | Require a newly started logical command; never report success. |
| `PROPOSAL_REVISION_INPUT_TOO_LARGE` | 413; field/reason enum and fixed byte limit | Keep draft, focus the oversized field, never retry unchanged payload automatically. |
| `PROPOSAL_MERGE_CONTENT_INVALID` | 400; field/reason enum | Keep draft, focus field/error, do not retry automatically. |
| `PROPOSAL_MERGE_RESULT_TOO_LARGE` | 422; fixed result limit | Freeze create, preserve draft, and require a smaller source/result rather than retrying unchanged input. |
| `PROPOSAL_MERGE_BUSY` / `PROPOSAL_MERGE_TIMEOUT` | 503; `retryable=true` | Keep source binding/draft and allow bounded retry. |
| engine/storage/output 503 codes | 503; strict stable code and server `retryable` | Keep draft; only expose retry when the decoded flag permits it. |

No Problem should contain full bodies, snippets, absolute paths, Git commands, or raw merge-library errors.

### MVP interaction design

#### Entry and eligibility

- Show `编辑修订版本` when the latest ordinary `file_patch/REPLACE` is server-editable and current matches base.
- Show `进入三方合并` when the same Proposal has drifted or is `needs_revision` and the server reports it as editable.
- Do not expose the action for `CREATE_ONLY`, `restore_document`, typed Proposals, or states with irreversible side effects/manual recovery.
- Treat editability as a server contract/capability, not a client guess from display text. If the API returns a stable capability/reason, decode it explicitly.
- Loading a merge preview is an explicit user action; do not fetch three full source bodies for every Proposal detail visit.

#### Desktop structure

```text
<- 返回审阅      修订 #1 -> #2       无冲突 / 1 处冲突   [检查并创建修订版本]

[stale / unresolved / delivery-unknown alert]

基线 #1  ->  当前 Workspace  ->  原提案 #1  ->  待提交 Revision #2
base hash    current hash         change hash       本地未提交 / 已验证

[基线 -> 当前] [基线 -> 原提案] [冲突 (N)]
[one full-width read-only Monaco Diff, or bounded conflict context list]

待提交的新 Revision                         12,340 bytes / 已编辑
[one full-width editable Monaco]

[Evidence summary]
[Risk level (read-only)] [Risk description]
[Rollback plan]

[取消]                                      [检查并创建 Revision #2]
```

The provenance rail is the sole signature element. It expresses audit lineage rather than decoration. Use the existing neutral surface, blue focus, red error, amber warning, and green success palette.

Use tabs for two independent questions:

- `基线 -> 当前`: what changed in the Workspace after the source Revision.
- `基线 -> 原提案`: what the source Revision proposed.
- `冲突 (N)`: server-derived conflicting regions with Base/Current/Proposed text labels and an ordinal.

Keep the candidate editor visible below the comparison; changing tabs must not unmount or reset the draft. `检查并创建` is deliberate wording: the authoritative append endpoint, not a frontend scan for marker text, decides whether conflicts are resolved.

#### Mobile at 390x844

- Replace the horizontal provenance rail with four labeled rows. Hashes truncate visually with an accessible full-value label/tooltip; never allow document horizontal overflow.
- Use short, wrapping tab labels in a feature-specific grid or horizontally scrolling tab list contained inside the workbench. Display one Diff at a time.
- Stack Base/Current/Proposed excerpts derived from the validated full bodies/ranges inside each conflict item. Each block scrolls internally for long unbroken text and uses `overflow-wrap:anywhere` for labels.
- Keep the candidate editor at a stable minimum height around 440px and use the existing Authoring single-mode pattern if viewport pressure requires toggling comparison/editor. Preserve editor content across mode switches.
- Make footer actions full width, 44px minimum, and keep submit reachable without a fixed overlay covering Monaco. Normal approval sidebar content follows the workbench or stays hidden until the user exits revision mode.
- Verify keyboard Tabs, focus-visible controls, alert focus, Dialog focus restoration, semantic status text, and reduced-motion compatibility.

### Frontend state machine

Use a local reducer for the nontrivial workbench transitions. The reducer owns only ephemeral interaction/draft state; Query owns Proposal/history authority.

| State | Visible behavior | Allowed action / transition |
| --- | --- | --- |
| `closed` | Normal current Proposal review. | Eligible CTA -> `preparing`. |
| `preparing` | Disabled CTA/loading; no empty workbench shell. | Preview success -> `active`; failure -> `preview_error`; cancel aborts. |
| `preview_error` | Explicit retryable/nonretryable alert. | Retry same source identity; back to review. |
| `active_clean` | Provenance, two Diffs, candidate editor, metadata form. | Editing -> `active_dirty`; submit if server preview has no unresolved requirement and form is valid. |
| `active_conflicts` | Conflict count/list plus editable candidate. | User edits; submit still goes to server; no client-owned resolution truth. |
| `editor_loading/error` | Stable editor frame and visible status; create disabled. | Ready -> active; retry/remount or cancel. |
| `submitting` | Snapshot input and one Idempotency Key; disable duplicate actions. | Success/replay -> success; stable validation -> active; 409 -> stale; network/5xx -> delivery unknown. |
| `delivery_unknown` | Exact input/key retained in memory; no success message. | Retry exact logical command with same key; abandon only via confirmation. |
| `stale` | Dirty body/metadata preserved as `上次编辑`; refreshed current binding summarized; create disabled. | `基于最新内容重新合并` requests a new preview. Never silently rebind old text. |
| `restart_review` | Compare old local edit with fresh server candidate before replacement. | Explicitly adopt fresh candidate, keep/copy old edit, or cancel. |
| `success` | New Revision number, `ready_for_review`, Approval absent/reapproval required. | Reset preview/draft, refetch bound facts, return to normal review. |
| `historical_readonly` | Selected old Revision/Diff/Approval, labeled historical. | Return to latest; never approve/edit an old selection. |

Additional rules:

- Generate one Idempotency Key when a submit snapshot is created. Exact retry reuses it; any changed content/binding starts a new logical command and key.
- A no-drift edit with no content/metadata change is a no-op and should disable append. A drifted, non-conflicting merge can legitimately create a Revision without manual keystrokes because the candidate incorporates current changes.
- Warn on dirty in-app exit with the shared Dialog and on tab/window unload with `beforeunload`. Do not persist body data to make refresh recovery appear to work.
- On an incoming Proposal SSE event while dirty, mark the binding stale and refetch authority, but keep the local draft. Do not automatically submit or replace it.
- On success, announce the new Revision and that reapproval is required; do not show approved, applied, or writeback success.

### Revision history UX

- Add a compact `修订历史` list/select near the Proposal facts, newest first. Each row shows `#N`, created time, approval decision or `未审批`, and `当前`/`历史` text.
- Selecting an old Revision changes the main Diff and approval facts to read-only historical mode. Clearly label it `历史 Revision #N`; hide approval, preflight, and edit actions until returning to latest.
- Historical Diff must be immutable `base -> proposed`. It must not re-read today's Workspace and relabel that content as the old base.
- An optional `revision=<uuid>` search parameter is acceptable because it is an opaque shareable selection, not content or authorization. If used, extend `url-state.ts` with exact single-value UUID parsing and round-trip/duplicate tests. If deep-linking history is not an MVP need, keep selection local and leave list-filter URL state unchanged.
- History and historical-content queries carry Workspace, Proposal, and Revision identity and use `gcTime: 0` because they contain full text.

### Exact frontend files affected

Required implementation set:

| File | Change |
| --- | --- |
| `web/src/api/business.ts` | Add Proposal `version`; strict Problem decoder; preview, append, history/detail types/decoders/clients; text/range/binding bounds. |
| `web/src/api/business.test.ts` | Add valid/invalid contract matrix, semantic bindings, 409 detail decoding, content/range/count limits, replay response tests. |
| `web/src/features/business/ProposalsPage.tsx` | Add eligible entry, history selection, workbench integration, targeted invalidation/refetch, and historical read-only mode. Extract logic rather than growing the page inline. |
| `web/src/features/business/ProposalsPage.test.tsx` | Extend fixtures with Proposal version/history; entry eligibility, history selection, success reset, reapproval, and latest-first refetch behavior. |
| `web/src/features/business/ProposalRevisionWorkbench.tsx` | New reducer-driven preview/Diff/editor/form/conflict/retry/stale/dirty-exit surface. |
| `web/src/features/business/ProposalRevisionWorkbench.test.tsx` | New component tests for every state, exact-key retry, dirty preservation, focus, keyboard tabs, eligibility, and non-color conflict semantics. |
| `web/src/features/business/proposal-revision.css` | Scoped workbench/provenance/conflict/editor responsive layout; no global style expansion unless an existing utility genuinely applies. |
| `web/src/shared/MonacoTextEditor.tsx` | New generic controlled Monaco Editor using shared local runtime and safe model lifecycle. |
| `web/src/shared/MonacoTextEditor.test.tsx` | URI/key/options/readiness/error/disposal/remount coverage. |
| `web/src/features/authoring/MonacoMarkdownEditor.tsx` | Make a thin wrapper around the shared editor to avoid duplicate Monaco setup. |
| `web/src/events/event-store.tsx` | Invalidate history/detail families for Proposal and conservative Workflow events. |
| `web/src/events/event-store.test.tsx` | Assert `proposal.revised` invalidates Proposal/current/history without making event payload authoritative. |
| `web/e2e/proposal-revision-merge.smoke.spec.ts` | Real-browser desktop/mobile scenarios below. |
| `deploy/proposal-revision-merge-browser-smoke.sh` | Start isolated DB/API/worker/Web fixture and seed a real Workspace/Git target. |
| `Makefile` | Add a named smoke target following existing browser harness conventions. |

Conditional only if historical selection is deep-linked:

- `web/src/features/business/url-state.ts`
- `web/src/features/business/url-state.test.ts`

No route change is necessary if the workbench stays inside the existing Proposal detail route. No new npm dependency is recommended.

Cross-layer files/interfaces the frontend depends on, for the implementation owners:

- `api/openapi/openapi.json`
- Proposal domain/application/repository and HTTP route implementations for preview, append, history, and stable Problems
- new forward migration only if persistence/idempotency/merge-input snapshots need new records
- `docs/requirements.md` AC-13 and parent product-delivery task status

### Test plan

#### Unit and component tests

1. Strict decoders reject unknown fields, identity/hash mismatch, invalid target/mode, Proposal version mismatch, inconsistent conflict count, duplicate IDs, invalid ranges, oversized content/response body, and malformed Problem details.
2. Preview request sends source identity only and uses `no-store`; it never sends caller-supplied base/current/proposed facts.
3. Append snapshots candidate and metadata with one Idempotency Key. Network/5xx retry sends byte-for-byte equivalent body/key. Editing after failure creates a new command/key.
4. A 409 refreshes Proposal/history/current facts but preserves the draft, disables old submit, focuses the stale alert, and requires explicit fresh preview.
5. An unresolved-conflict Problem keeps text, selects/focuses the first returned conflict ID, and does not create a frontend success state.
6. Success and replay bind the expected new Revision, clear draft/preview caches, render `ready_for_review`, and require a new approval.
7. Historical selection is read-only and renders immutable base/proposed plus the selected Revision's Approval. Returning to latest restores current review controls.
8. `CREATE_ONLY`, restore, typed, ineligible status, Monaco loading/error, preview error, missing authority, and oversize input never enable append.
9. Tabs are keyboard operable; conflict state has textual meaning; dialogs restore focus; alerts have accessible names/focus behavior.
10. Monaco models use unique Workspace/Proposal/source Revision/fingerprint/role URIs and dispose only after detach. Route enter/leave cycles return the registry to zero.
11. `proposal.revised` and Workflow events invalidate exact/all Proposal, current-content, and history keys; SSE payload is never written into Query as the Revision.

#### Real browser scenarios

Run each core layout at desktop `1440x900` and mobile `390x844` where indicated.

1. **Non-overlapping drift, full chain**
   - Seed base; Proposal changes region A; external Workspace edit changes region B.
   - Enter merge, verify Base/Current/Original Proposal labels and no-conflict candidate contains both changes.
   - Create `#2`; reload and verify `#2`, `ready_for_review`, and no Approval.
   - Verify `#1` history/Diff remains readable, approve `#2`, preflight/write back, and assert exact file plus Git/Workflow/Revision mapping.
2. **Same-region conflict**
   - Change the same lines in current and proposed.
   - Verify server conflict count, ordinal, and Base/Current/Proposed textual labels.
   - Attempt unresolved submit: no Revision, focused error, draft preserved.
   - Edit candidate, create `#2`, reapprove, and assert exact final Workspace content.
3. **Second drift and concurrent editor**
   - Two browser contexts open the same source/current binding and both edit.
   - Context A creates `#2`; B receives 409, retains its local text, shows refreshed current summary, and cannot submit the old binding.
   - B explicitly starts a fresh merge. Assert exactly one `#2`, no fork, no overwritten file.
4. **Refresh recovery and history**
   - After successful append, reload/deep-link and recover latest `#2` from the server.
   - Select `#1`, verify it is read-only with old Approval/Diff, then return to latest.
   - An unsaved draft is not recovered from Browser Storage; the page must not claim otherwise.
5. **Response-loss replay**
   - Where the harness can commit then drop the response, retry exact key/input and receive the same Revision with `replayed=true`; assert there is no `#3`.
6. **Responsive/accessibility/console gate**
   - At 390x844: no document horizontal overflow; provenance stacks; one Diff is visible; editor remains usable; controls are at least 44px; long path/hash/conflict text stays contained.
   - Keyboard-select all tabs, resolve/submit without pointer-only affordances, verify alert and dirty-dialog focus, and confirm status is not color-only.
   - Desktop and mobile runs have zero unexpected console errors, page errors, failed network requests, and Monaco disposal warnings.

Backend integration must separately own deterministic merge fixtures for empty content, EOF edits, CRLF/LF, adjacent changes, same-region add/delete, UTF-8, illegal encoding, size limits, and absence of unresolved marker tokens. Browser tests should prove orchestration, not re-specify the merge algorithm.

#### Verification commands expected after implementation

- Targeted Vitest suites for Business API, Proposal page/workbench, Monaco shared component/runtime, URL state if changed, and event store.
- Frontend lint, TypeScript check, and production build.
- `make openapi-check` (or the repository's current OpenAPI validation target).
- Named real-browser smoke harness using an isolated PostgreSQL/API/worker/Vite stack and a real Workspace Git repository.
- `git diff --check`.

### External references and versions

- No external documentation was required for this frontend research. Installed package manifests and local TypeScript declarations were inspected directly.
- Installed runtime versions: React 19.2.7, React Router DOM 7.18.1, TanStack Query 5.101.2, `@monaco-editor/react` 4.7.0, Monaco Editor 0.56.0.
- No existing merge/diff3 package or Monaco merge-editor API was found. This supports the PRD decision that the deterministic merge engine belongs to the backend and does not justify adding a frontend merge dependency.

### Related specs and requirements

- `.trellis/tasks/08-14-proposal-revision-three-way-merge/prd.md:25-55` defines immutable revision, server merge, reapproval, concurrency, and sensitive-data boundaries.
- `.trellis/tasks/08-14-proposal-revision-three-way-merge/prd.md:57-68` defines the end-to-end and browser acceptance criteria.
- `.trellis/spec/frontend/state-management.md:11-55` defines Query/local/SSE ownership and Browser Storage prohibition.
- `.trellis/spec/frontend/component-guidelines.md:104-165` defines the Monaco model lifecycle and browser registry test.
- `.trellis/spec/frontend/quality-guidelines.md:17-25` requires explicit version-conflict/manual-recovery states, strict typed boundaries, and behavior tests.
- `.trellis/spec/frontend/quality-guidelines.md:50-57` requires component, integration, browser, accessibility, and performance coverage.
- `.trellis/spec/frontend/directory-structure.md:35-43` prohibits Business from importing Authoring internals.
- `docs/requirements.md:86` requires new immutable revisions and old revision retention.
- `docs/requirements.md:225` requires original/current/proposed comparison when the approval target changes.
- `docs/requirements.md:279` is AC-13: approval-time file change must block overwrite and enter three-way merge.
- `docs/architecture/application-contracts.md:49-53` defines the discriminated Proposal and approval boundary.

## Caveats / Not Found

- There is no current endpoint or frontend projection for historical revisions. Without it, R1/AC1's requirement that old Revision, old Diff, and old Approval remain readable cannot be met by UI changes alone.
- The immutable base body is not present in Proposal detail/current-content. A true three-way view cannot be reconstructed from the current frontend responses.
- Proposal aggregate Version exists in the domain but is absent from the Web/OpenAPI detail contract. Concurrency-safe append requires exposing it or an equivalent ETag.
- Exact server merge algorithm/library, fingerprint schema, byte limits, empty-file semantics, conflict-range semantics, event name, and editable status/capability remain backend design decisions. The frontend should consume versioned outcomes and must not fill these gaps with client-side merge logic.
- The current domain state transition table was reported to contain `needs_revision -> draft` but not the full append-validation-return-to-review path. The UI must not hard-code a transition until domain/application behavior is defined.
- Browser response-loss simulation may need a server fault-injection seam; if unavailable, cover exact-key replay at API/integration level and keep browser coverage for second drift and concurrent tabs.
- Adding `revision=<uuid>` to the URL is optional. It is valuable for shareable historical review, but the MVP can keep selection local if URL state would expand scope; full content must never enter the URL.
