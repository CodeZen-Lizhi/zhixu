# Research: Existing Workspace Root Rebinding

- Query: Design the exact fail-closed migration for an existing Workspace whose canonical root path is unchanged but whose physical fingerprint changed after the directory was recreated, while preserving Workspace ID, all Workspace-scoped business data, and append-only audit/history.
- Scope: internal
- Date: 2026-08-14

## Findings

### 1. Decision summary

This must be a separate, explicitly confirmed **rebind** command. Ordinary `up`,
`restart`, `workspace switch`, `ResolveWorkspace`, and recovery must continue to
fail closed on an identity mismatch. The safe operation has two boundaries:

```text
host validation + revoke/zero-bind proof
  -> one atomic PostgreSQL root-binding migration
  -> ordinary durable Workspace switch/prepare/verify/activate
  -> atomic launcher selection commit
```

The database migration changes only the root binding of the existing
`core.workspace` row. It never creates or substitutes another Workspace ID and
never rewrites Workspace-scoped business rows. A dedicated append-only root
binding receipt authorizes the otherwise forbidden Registry mutation and the
shared `ops.audit_event` records the security/business action in the same
transaction.

The current workspace contains an in-progress `00081_workspace_root_rebinding.sql`
and preliminary Domain/Application/CLI code. Those files appeared during this
research and are treated as a draft, not as accepted evidence. The draft has
critical gaps listed in section 10; in particular, it increments
`binding_version` while every host validation path still emits version `1`, and
it mutates the database without first revoking or proving absence of Docker
binds.

### 2. Existing invariants that must remain true

1. **Logical identity is the Workspace UUID, not the inode.**
   `core.workspace.id` is the primary key and `root_path` is merely unique
   Registry data (`migrations/00002_workspace_sources.sql:2-20`). Workspace ID
   is referenced throughout the schema: current migrations contain 128
   `workspace_id uuid` declarations across 45 migration files, starting with
   `core.source` (`migrations/00002_workspace_sources.sql:22-33`) and extending
   through workflow, ingestion, retrieval, knowledge, audit, review, authoring,
   export, Git sync, and capture. Updating the one Registry row in place is what
   preserves all of those records and their foreign-key history.

2. **The root path cannot move in this operation.**
   The request must identify the selected Workspace ID, the selected old
   fingerprint, and the exact saved canonical path. Fresh host validation must
   resolve the request to that same canonical path. A moved or symlink-retargeted
   path is a different migration problem and must remain rejected. This follows
   the current same-path identity contract (`.trellis/spec/backend/workspace-root-grant.md:41-44`)
   and avoids relinking business data to an arbitrary directory.

3. **Ordinary resolution remains immutable and fail closed.**
   `ResolveWorkspace` currently marks a same-path fingerprint mismatch
   `migration_required` with `WORKSPACE_ROOT_IDENTITY_CHANGED`, without changing
   the binding (`internal/workspace/adapter/postgres/registry.go:58-81`). Migration
   `00067` rejects every update of `(root_path, git_repository_path,
   root_fingerprint, binding_version)` (`migrations/00067_workspace_root_grant.sql:67-101`).
   A forward migration must create one narrow exception backed by an exact
   append-only receipt; it must not remove the guard or add an automatic fallback.

4. **Only an inactive, unremoved identity already awaiting this exact recovery
   is eligible.** Required pre-state:
   `status='inactive'`, `availability='migration_required'`,
   `availability_reason='WORKSPACE_ROOT_IDENTITY_CHANGED'`, `removed_at IS NULL`,
   same canonical path, exact expected old fingerprint, and a non-legacy binding.
   Legacy version `0`, a moved path, a healthy binding, and an identity belonging
   to another Workspace all require a different explicit migration.

5. **There is zero active grant at the binding commit.** No non-terminal
   `ops.workspace_switch`, no Active/Resume/Target/Previous projection, no
   controller lease, no global runtime mutation owner, no live API/Worker
   container retaining a Workspace bind, and no non-`unavailable`
   `ops.workspace_runtime` row may exist. This is the same fail-closed boundary
   required for recovery after an identity change
   (`.trellis/spec/backend/workspace-root-grant.md:52-63`).
   The zero state may be established by a preceding durable revoke transition or
   atomically inside the rebind transaction after host zero-bind proof; it cannot
   be assumed merely because `ResolveWorkspace` reported an identity conflict.

6. **All old authority is fenced.** The old fingerprint and binding identity
   must no longer authorize registration/heartbeat. In addition, increment
   `ops.workspace_control_state.grant_generation` exactly once in the successful
   rebind transaction. `ProcessGrant` contains only Workspace ID, canonical root,
   and generation, not the fingerprint (`internal/platform/rootgrant/grant.go:15-27,50-84`),
   so a fingerprint change alone cannot fence an already-started process.

7. **History is append-only.** Existing `ops.workspace_switch` rows remain
   untouched even though they contain old fingerprints/binding versions; they
   are historical operation facts (`migrations/00067_workspace_root_grant.sql:209-349`).
   Existing business/audit rows remain attached to the same Workspace UUID.
   New root-binding history and the generic audit event are appended, never
   updated or deleted.

8. **Selection remains a cache, never authority.** The protected launcher
   selection changes only after the rebound identity has completed the ordinary
   API/Worker prepare/verify/activate path and host readiness succeeds. Current
   selection commit already uses a protected atomic replace
   (`zhixu:282-530`), and current lifecycle checks ID/fingerprint before commit
   (`zhixu:1456-1499`).

9. **No root contents are inferred or copied.** Host validation remains metadata
   only, with canonical path/device/inode and TOCTOU rechecks
   (`internal/workspacecontrol/path.go:56-126`). Rebinding asserts user intent to
   attach the existing logical Workspace to the recreated physical directory; it
   does not assert that directory contents equal the lost directory.

10. **Git/capture state is not silently reinterpreted.** The Registry's cached
    Git branch/head/dirty values and the immutable Git capture checkpoint are
    tied to the former repository observation. The checkpoint is keyed only by
    Workspace ID and enforces contiguous commits
    (`migrations/00077_workspace_git_capture.sql:8-83`,
    `internal/workspace/adapter/postgres/git_capture.go:83-131`). The safe MVP
    must either require the new root to be the same Git lineage at the expected
    head before accepting the rebind, or add an explicit forward-only capture
    rebaseline protocol. It must not delete/reset the checkpoint or allow a later
    scan to treat an unrelated repository as a contiguous successor. Remote
    configuration/revision history may remain because it is logical Workspace
    configuration (`migrations/00076_git_remote_sync.sql:3-78`).

### 3. Binding-version semantics: required design choice

Current code uses `binding_version` as the fingerprint **schema version**:

- `PathValidator.Validate` hard-codes `BindingVersion: 1` and includes that value
  in the SHA-256 digest (`internal/workspacecontrol/path.go:117-132`).
- Every apply/prepare/reconcile check compares both the recomputed v1 digest and
  validator version to the persisted grant
  (`internal/workspacecontrol/compose_driver.go:85-143,219-223`,
  `internal/workspacecontrol/coordinator.go:284-293,907-913`).

Therefore the draft approach `binding_version = binding_version + 1` with a
fingerprint computed by ordinary `Validate` is invalid: after the first rebind,
the Registry says version `2` while host validation always returns version `1`,
so the rebound Workspace can never prepare, apply, reconcile, or activate.

Recommended minimal contract:

- Keep `binding_version` as the fingerprint schema version and leave it at `1`
  for same-algorithm root replacement.
- Add a distinct monotonic `root_binding_revision bigint NOT NULL DEFAULT 1` to
  `core.workspace` and carry it in root-binding history and runtime ownership if
  a per-rebind epoch is needed. The already mandatory `grant_generation` is the
  process fence; `root_binding_revision` is lineage/audit CAS, not a replacement
  for generation.
- The new physical fingerprint is computed exactly as today with fingerprint
  schema version `1` and differs because device/inode differs.

Alternative, larger contract: explicitly redefine `binding_version` as a
binding epoch and add a separate `fingerprint_schema_version`. That requires a
data migration and updates every validator/grant/runtime/selection consumer. It
is not the minimal safe change and must not be introduced implicitly.

### 4. Minimal affected files and contracts

#### Database and audit

- Add the next forward migration (currently allocated as
  `migrations/00081_workspace_root_rebinding.sql`; re-check the next number at
  implementation time). Do not edit published `00067`.
- Add append-only `ops.workspace_root_binding_history` as the relational command
  receipt/authorization record. It should freeze: receipt ID, Workspace ID,
  controller instance ID, idempotency key, canonical path, request hash, old/new
  fingerprint, unchanged fingerprint schema version, old/new binding revision,
  old/new Workspace version, old/new grant generation, stable audit event ID,
  reason, and DB timestamp.
  Unique `(controller_instance_id,idempotency_key)` supports exact replay;
  unique old-to-new transition prevents duplicate lineage.
- Add `core.workspace.last_root_rebind_id` (FK to the history row) or an
  equivalent current receipt reference. The Registry trigger may permit an
  identity update only when `NEW.last_root_rebind_id` is a new receipt whose
  complete old/new tuple and versions match `OLD`/`NEW`. Merely checking whether
  a matching historical row exists is weaker: a published row is not evidence
  that this update is the authorized transition in the current command.
- Replace the `core.enforce_workspace_registry` function with the narrow
  receipt-backed exception; retain all ordinary immutability, version, timestamp,
  removal, and truncation checks from `00067:67-101`.
- Append a redacted `ops.audit_event` in the same transaction, action
  `workspace.root_rebound`, resource `workspace:<id>`, SYSTEM actor with the
  control instance as actor reference, outcome succeeded, and metadata limited
  to old/new fingerprint hashes, fingerprint schema version, binding revisions,
  generation, and reason. Do not include the canonical absolute path. Because
  audit equality includes event ID and occurred-at time
  (`internal/audit/domain/event.go:186-224`), freeze both in the rebind receipt;
  generating either again during replay would convert an exact retry into an
  audit conflict. The existing audit store supports transaction-scoped append,
  advisory locking,
  exact replay, and conflict detection
  (`internal/audit/application/ports.go:23-27`,
  `internal/audit/adapter/postgres/append.go:21-97`); `ops.audit_event` is already
  append-only (`migrations/00034_learning_ops_auth.sql:263-276,395-415`).
- Follow the existing narrow audit adapter pattern in
  `internal/modelsettings/adapter/postgres/audit.go:27-93`: define a Workspace
  rebind audit appender port in Application, adapt the shared audit store in the
  Workspace PostgreSQL package, and inject it only into the one-shot
  `workspacectl` composition. Do not make API/Worker general repositories require
  the appender.

#### Domain, Application, and PostgreSQL adapter

- `internal/workspace/domain/control.go`: explicit rebind command/result and
  stable errors. The persisted command must include request hash, expected
  Workspace version, expected control-state version, expected old fingerprint,
  fingerprint schema version, old/new binding revision, and audit/receipt ID.
- `internal/workspace/application/control.go`: one `RebindWorkspaceRoot` service
  method/Registry port; validate the complete command and generate receipt/audit
  identity. Ordinary `ResolveWorkspace` stays unchanged.
- `internal/workspace/adapter/postgres/registry.go` or a focused `rebind.go`:
  implement the single transaction and exact replay. Extend
  `control_errors.go` so the new history uniqueness constraints and state/gate
  conflicts map to stable rebind conflict/idempotency errors instead of the
  generic control-corrupt fallback (`control_errors.go:23-49`).
- `internal/workspace/adapter/postgres/runtime.go`: no ordinary authorization
  relaxation is required if stale rows are terminalized before the Registry
  update; new active/candidate registration and heartbeat must retain exact
  Registry checks. The migration's runtime trigger may continue rejecting every
  attempted update whose new binding differs from Registry. Do not delete
  runtime rows; `00067` forbids delete/truncate
  (`migrations/00067_workspace_root_grant.sql:445-502`).

#### Host coordinator, native CLI, and launcher

- `internal/workspacecontrol/path.go`: no change to v1 digest semantics under the
  recommended design; add a rebind validator helper only if needed to return a
  raw physical observation twice without duplicating path checks.
- `internal/workspacecontrol/coordinator.go` / `types.go`: add one explicit
  `Rebind` orchestration. It must recover/terminalize any pending switch, revoke
  the old grant, prove zero bind, call the database migration, then call the
  ordinary switch path for the same Workspace ID/new fingerprint.
- `internal/workspacecontrol/compose_driver.go`: strengthen `RevokeGrant`'s
  postcondition or add `InspectGrantRevoked`. Proof requires no app/worker or
  relay container capable of retaining the bind and no grant override; a
  successful `docker compose stop/rm` exit alone is not enough. Current
  `RevokeGrant` stops/removes and unlinks the override but does not inspect the
  post-state (`compose_driver.go:244-289`).
- `cmd/workspacectl/main.go`: explicit `rebind` action with exact confirmation,
  selected Workspace ID, expected old fingerprint, and deterministic logical
  idempotency. It must compose the runtime driver/coordinator; a direct call to
  `ControlService.RebindWorkspace` before runtime construction is unsafe.
- `zhixu`: expose only `workspace rebind --confirm REBIND`, using the protected
  current selection as the expected ID/path/fingerprint. The command must not
  accept an arbitrary target path. It runs containment/revoke before the DB
  mutation, activates by ordinary switch afterward, and commits selection only
  after readiness. The launcher mutation-lock allowlist must include
  `workspace-rebind`; its current parser still omits it (`zhixu:609-645`) even
  though dispatch uses that name (`zhixu:2363-2371`).
- `deploy/launcher-contract.sh` and its fake Docker/workspacectl fixtures;
  `cmd/workspacectl/main_test.go`; Workspace Application tests;
  Coordinator/Compose driver tests; and a real PostgreSQL migration/repository
  integration suite.

No browser, public HTTP, OpenAPI, or frontend contract is needed: root migration
remains a host-local, explicitly confirmed command. No Workspace-scoped business
repository or table should be rewritten.

Because this changes launcher and Workspace root-control signatures, the same
implementation must update `.trellis/spec/backend/workspace-root-grant.md`,
`docs/operations.md`, `docs/requirements.md`, and
`docs/architecture/system-design.md` as required by the current spec
(`workspace-root-grant.md:5-12`).

### 5. Transaction and locking strategy

External Docker work cannot be held inside a database transaction. Serialize it
first with the existing launcher filesystem mutation lock and the coordinator's
process-local runtime gate, then use this order:

#### Host phase

1. Load and strictly validate protected selection.
2. Validate the saved root twice through `PathValidator`; require the same
   canonical path, expected old fingerprint different from the new fingerprint,
   and a stable new physical identity across checks.
3. Acquire the coordinator runtime mutex and recover any non-terminal switch to
   a terminal state. An identity-changed previous root must converge through the
   existing fail-closed recovery to zero Active, not be rewritten in place
   (`internal/workspacecontrol/coordinator.go:104-131,738-786`).
4. Call `RevokeGrant`, then inspect the fixed project and prove no Workspace
   runtime/relay container and no protected override remain. If proof fails, do
   not enter the DB rebind transaction.

There is one prerequisite gap in the current fail-closed recovery: ordinary
same-path `ResolveWorkspace` clears Active/Resume when it marks the Registry
`migration_required`, but it does not bump grant generation or terminalize old
runtime rows (`internal/workspace/adapter/postgres/registry.go:375-410`). The
explicit rebind flow must provide a durable, idempotent **revoke-to-zero** control
transition after Docker proof and before the Registry mutation, or the rebind
transaction itself must be allowed to consume the exact target's residual
Active/Resume pointers, mark its runtimes unavailable, clear the projections,
and bump generation atomically. Requiring every pointer already be NULL while
providing no such transition makes the command unreachable from a healthy
active Workspace that was just recreated. Pointers to any other Workspace still
fail as corruption/conflict.

#### PostgreSQL phase

Use one transaction and the following fixed lock order:

1. Acquire transaction advisory locks for both old and new fingerprints in
   lexical order, using the **same raw fingerprint lock key** as
   `ResolveWorkspace` (`registry.go:21-39`). This serializes rebind with a
   concurrent ordinary resolve of the new physical identity. A separate
   Workspace-ID advisory lock may follow, but cannot replace the matching
   fingerprint locks.
2. `ops.workspace_control_state ... FOR UPDATE`.
3. `ops.runtime_mutation_gate ... FOR UPDATE`.
4. Target `core.workspace ... FOR UPDATE`.
5. Target `ops.workspace_runtime` rows `ORDER BY role FOR UPDATE`.
6. Exact idempotency receipt lookup under the locks; return exact replay before
   applying any version/generation increment.
7. Obtain `clock_timestamp()` from PostgreSQL.
8. Transition old target runtime rows to `phase='unavailable'`,
   `operation_id=NULL`, monotonic heartbeat/version (including rows already
   unavailable, if present) while the old Registry binding still matches the
   current runtime trigger.
9. Insert the frozen rebind history receipt.
10. Update the exact Registry row by version CAS: same ID/path/Git path, new
    physical fingerprint, unchanged fingerprint schema version, incremented
    binding revision, inactive/available, cleared migration reason,
    `version+1`, `last_root_rebind_id=<receipt>`.
11. Update control state by state-version CAS: clear all root projections,
    increment `grant_generation` exactly once and `state_version` exactly once,
    clear the identity-changed error. Reject overflow.
12. Append the generic audit event through the injected `AppendTx` boundary,
    using the receipt's stable audit ID and DB timestamp so response-loss replay
    is byte-for-byte equal.
13. Commit. Any failure rolls back runtime, history, Registry, control fence,
    and audit together.

Why the control singleton must be first: runtime heartbeat locks control
`FOR SHARE`, then its role row `FOR UPDATE`, then Workspace `FOR SHARE`
(`internal/workspace/adapter/postgres/runtime.go:83-120`). Taking control
`FOR UPDATE` first waits for existing heartbeat/register transactions to finish
and blocks new ones before Workspace/runtime locks, avoiding the inverse-lock
deadlock. Workspace switch already uses control -> mutation gate -> Workspace
(`internal/workspace/adapter/postgres/control.go:117-180`), so rebind must match.

The database transaction must reject, rather than steal:

- any in-flight Workspace operation;
- any runtime mutation gate owner, including model-settings rollout, regardless
  of apparent lease expiry (its owner must reconcile/release itself);
- an Active/Resume pointer to another Workspace, and any Target/Previous
  operation projection; exact-target Active/Resume may only be consumed by the
  audited revoke-to-zero transition described above;
- any non-unavailable runtime row;
- stale expected Workspace/control versions;
- new fingerprint collision with another Workspace;
- wrong canonical path, old fingerprint, or eligibility reason.

### 6. Stale runtime and control-state handling

`ops.workspace_runtime` has one current ownership row per role and its trigger
requires the row binding to match Registry on every insert/update
(`migrations/00067_workspace_root_grant.sql:418-502`). `RegisterRuntime` permits
a new instance to replace an old role owner, but a same instance cannot change
binding (`internal/workspace/adapter/postgres/runtime.go:14-80`). Therefore:

- Old rows must be made `unavailable` before the Registry fingerprint changes.
- Keep the runtime trigger strict. A historical `unavailable`, operation-free
  row may remain stored after the Registry fingerprint changes because no row
  mutation occurs at that point. Any later mutation carrying the old binding is
  rejected, while a different new instance can replace the role row using the
  exact current Registry binding. This is the desired fencing behavior; no
  ordinary runtime-authorization exception is required.
- After the ordinary post-rebind switch starts new API/Worker instances, their
  registrations replace the role rows with the new fingerprint/generation and
  increment row version. Do not delete old rows.
- A runtime row already marked `unavailable` still needs its `heartbeat_at` and
  `version` advanced during rebind if the operation intends to record the revoke
  boundary. Updating only `phase<>'unavailable'` leaves a fresh-looking old
  ownership row unchanged. The SQL should update both roles (or deliberately
  define and test a no-op for already-unavailable rows), always using DB time.
- Old processes that somehow survive Docker revocation fail both generation and
  binding authorization on their next start/heartbeat. This is defense in depth;
  it does not replace the pre-commit zero-bind proof.

Control state handling:

- A pending `workspace_switch` is first recovered/failed through its existing
  state machine. Rebind never edits an operation's frozen identity or terminal
  row.
- The successful rebind transaction leaves zero Active/Resume and no operation,
  increments the generation fence, and keeps the Workspace inactive/available.
  The following ordinary `Switch` uses a later generation to prepare and
  publish the rebound identity. A skipped generation is acceptable; reuse is
  not.
- `last_error_code` is cleared only in the same successful transaction. Before
  then, `WORKSPACE_ROOT_IDENTITY_CHANGED` remains a durable explanation.
- Control snapshot validation currently does not cross-check historical
  unavailable runtimes against Registry (`internal/workspace/adapter/postgres/control.go:864-887`),
  which is compatible with keeping those rows until new owners replace them.

### 7. Idempotency, crash recovery, and rollback

The rebind request hash must cover at least schema discriminator, controller
instance, Workspace ID, canonical path, expected old fingerprint, new
fingerprint, fingerprint schema version, and old/new binding revision. The
idempotency key must be stable for the logical old-to-new transition. Generating
a fresh random key on every launcher retry is insufficient for response-loss
replay; either derive a bounded key from the transition hash or persist the
attempt key in protected launcher state until selection commit.

Exact behavior by failure point:

| Failure point | Durable state | Required retry behavior |
| --- | --- | --- |
| Before Docker revoke | Old DB binding and selection unchanged | Fail; ordinary old grant may remain only if it is still physically valid |
| After revoke, before DB commit | Old DB binding/selection; zero runtime | Retry rebind; do not invent an old bind to the recreated path |
| Within DB transaction | Full rollback of receipt, audit, Registry, runtime rows, generation/state versions | Retry the same request |
| DB committed, response lost | Same Workspace ID with new binding; zero Active; old selection | Exact receipt replay returns `Changed=false`; continue ordinary switch |
| Activation/prepare fails | New binding remains durable and inactive; old selection remains; zero grant after containment | Do not roll back to the vanished inode; retry activation/rebind replay |
| Runtime active, selection commit lost | DB and grant use new binding; old selection remains | Replay rebind, reconcile/switch exact binding, then atomically commit selection |
| Selection committed | Complete | Ordinary `up` validates and reuses exact new binding |

A root identity rollback is a new explicit forward rebind to a physically
present, freshly validated object; it is not automatic compensation. Migration
Down must refuse when any rebind history/current receipt exists, because dropping
the authorization/history would make the Registry change unauditable. Before
first use, Down may restore the old immutable trigger and drop the empty new
objects.

### 8. Audit and business-data preservation

Preservation is structural:

- The `core.workspace.id` is never updated or replaced.
- No referencing Workspace/business table is updated; all 128 observed
  `workspace_id uuid` declarations continue to point to the same row.
- Prior `ops.audit_event`, switch, workflow, proposal, source/version, knowledge,
  review, document, export, and Git configuration history is untouched.
- The root-binding receipt preserves the exact previous and new physical hashes,
  versions/revisions, generation fence, controller namespace, reason, and DB
  time. The generic audit event makes the action visible through the existing
  audit query/export boundary.
- The absolute host root is already in Registry but must not be copied into
  generic audit metadata, error documents, or logs. Current one-shot DB URL FD
  and safe error boundary remain unchanged
  (`.trellis/spec/backend/workspace-root-grant.md:91`,
  `cmd/workspacectl/main.go:353-378,425+`).

### 9. Test Matrix

##### Domain/Application unit tests

- Accept exact same canonical path, same Workspace ID, old != new 64-hex
  fingerprint, complete expected versions/revisions, stable key/hash.
- Reject moved path, legacy binding, equal fingerprints, malformed key/hash,
  overflow, nil context/dependencies, zero IDs/timestamps, and missing expected
  versions before persistence.
- Exact replay preserves receipt/audit ID and returns `Changed=false`; same key
  with different request hash returns `WORKSPACE_REBIND_IDEMPOTENCY_CONFLICT`.

##### Path/Coordinator/Compose tests

- Recreate a temporary directory at the same canonical path and prove device/inode
  digest changes while fingerprint schema version remains `1`.
- Symlink retarget and path TOCTOU fail before Docker/DB mutation.
- Pending switch is terminalized first; identity-changed previous recovery reaches
  zero Active.
- Revoke occurs before repository rebind; failed stop/rm, retained override,
  remaining fixed service container, or failed inspect prevents DB call.
- DB commit followed by prepare/probe/apply/readiness failure leaves the new
  binding inactive and selection uncommitted; retry resumes without a second
  root history/audit row.
- New grant validates and activates after rebind, proving the chosen
  binding-version semantics end to end.

##### PostgreSQL migration/repository integration tests

- Empty database Up and pre-use Down; Down refuses after history exists.
- Raw identity UPDATE without a new matching receipt still fails `55000`.
- Same-path rebind preserves Workspace ID, root/Git path, created time, and every
  seeded business/audit FK row; only allowed Registry fields/version/revision
  change.
- One history row and one generic audit event commit atomically; both are
  append-only and path-free in audit payload.
- Runtime rows become unavailable before Registry update; a historical
  unavailable row may remain stored afterward, but every later old-binding
  mutation is rejected; active/prepared mismatch cannot be created; new
  instances can replace both roles with the new binding.
- Control Active/Resume/projections clear, grant generation increments exactly
  once, state version increments once, and prior switch rows remain byte-for-byte
  unchanged.
- Exact replay performs no second increments/rows; response-loss query returns the
  persisted result. Conflicting key/hash and receipt ID are stable conflicts.
- Wrong expected Workspace/state/revision, new fingerprint collision, wrong path,
  wrong reason/status, removed/legacy Workspace, active other Workspace,
  non-terminal switch, mutation gate owner, and non-unavailable runtime all fail
  with no partial state.
- Concurrent rebind/rebind, rebind/Resolve(new fingerprint), rebind/BeginSwitch,
  rebind/RegisterRuntime, and rebind/HeartbeatRuntime have one valid winner,
  stable loser/replay behavior, no deadlock, and no split state.
- Fault injection at runtime update, history insert, Registry CAS, control CAS,
  and audit append proves complete transaction rollback.
- Seed `core.workspace_git_capture_checkpoint`; unrelated Git lineage is rejected
  or follows the separately approved rebaseline protocol, never silently reused.

##### workspacectl/launcher contract tests

- `workspace rebind` requires the exact `--confirm REBIND`; wrong/missing confirm
  fails before Docker and DB.
- No selection fails; command cannot supply another path/ID; protected selection
  corruption, permissions, or symlink fails.
- Ordinary `up` against a recreated path returns identity-changed, does not
  rebind/create a second Workspace, and leaves selection unchanged.
- Confirmed rebind returns same ID/new fingerprint; selection commits only after
  new API/Worker readiness and exact generation/DB Active agreement.
- Native failure, malformed result, different ID/path/fingerprint, readiness
  failure, response loss, and selection write failure retain old selection and
  contain uncommitted runtime. A later retry converges.
- Launcher mutation-lock metadata accepts and reports `workspace-rebind`.
- DB credential remains FD-only; errors/logs contain no password, DSN, absolute
  root, or raw dependency message.

##### End-to-end evidence

- Real PostgreSQL + Docker: activate Workspace A, seed representative Source,
  Workflow, Audit, Git config/capture, knowledge/review/document rows, revoke,
  recreate the same canonical directory, prove ordinary up fails, perform
  confirmed rebind, activate, and verify the same Workspace ID and seeded data.
- Crash checkpoints after Docker revoke, after DB commit, after switch commit,
  and before selection rename all recover without duplicate history/audit or two
  active grants.

### 10. Assessment of the current in-progress draft

The following are release blockers in the draft observed during research:

1. `migrations/00081_workspace_root_rebinding.sql:29-32,137` and
   `registry.go:203-219` increment `binding_version`, but `PathValidator` and all
   grant validators remain fixed at v1. The resulting Workspace is permanently
   ungrantable.
2. `cmd/workspacectl/main.go:152-173` calls the DB rebind directly before
   constructing a `ComposeDriver`; no runtime revoke or zero-bind proof occurs.
   `zhixu:1546-1566` starts base services and invokes that path without first
   containing existing app/worker containers.
3. `registry.go:183-187` rejects any residual Active/Resume state rather than
   providing a durable revoke-to-zero transition, and `registry.go:226-229`
   increments only state version, not grant generation, so old process grants
   with the same Workspace ID/path are not fenced.
4. The draft history is relational and append-only, but no `ops.audit_event` is
   appended. Its replay path does not bind `(controller_instance_id,
   idempotency_key)` or request hash; it accepts any matching old/new history.
   The launcher creates a new random key for every retry (`zhixu:1271-1274,1332-1359`).
5. The Registry trigger authorizes an update by existence of any matching history
   row (`00081:144-155`) without a current receipt reference on the Workspace.
   The history table lacks request hash, grant generation, and generic audit ID.
6. Advisory locking uses only Workspace ID (`registry.go:148`) and therefore
   does not serialize with `ResolveWorkspace`, whose lock key is the candidate
   fingerprint (`registry.go:21`).
7. The history trigger requires **all** Active/Resume pointers be null
   (`00081:66-80`), not merely safe target state, but the launcher/coordinator
   does not first reconcile/revoke the active selection. Common active-state
   recovery therefore cannot reach the repository precondition safely.
8. The strict runtime trigger can remain unchanged, but the draft updates only
   rows whose phase is not already `unavailable`; it neither records a fresh
   revoke boundary for an already-unavailable owner nor proves with integration
   tests that old heartbeat/phase mutations are rejected and a new-instance
   registration replaces the historical role row. There are no PostgreSQL
   rebind integration/concurrency/rollback tests yet; current tests cover only
   Application validation and CLI parsing/result shape.
9. `zhixu` dispatch names the mutation lock `workspace-rebind`, while
   `read_lock_metadata` rejects that value (`zhixu:609-645,2363-2371`). A crash
   can leave a lock that the next invocation declares invalid instead of safely
   recovering.
10. The task PRD/design and the current Workspace spec/docs do not mention root
    rebind. `.trellis/spec/backend/workspace-root-grant.md:43-44,100` currently
    says identity mismatch forbids rebinding the same ID. The explicit exception,
    command, audit, version semantics, and test requirements must be approved and
    documented before treating the draft as compliant.
11. Git lineage/capture checkpoint compatibility is not handled. Preserving the
    same Workspace ID is correct for business history, but blindly accepting an
    unrelated recreated repository can make the immutable contiguous capture
    checkpoint unusable or semantically false.

### 11. Files found

- `.trellis/spec/backend/workspace-root-grant.md` - authoritative host root,
  fail-closed switch/recovery, selection, logging, and test contract.
- `.trellis/spec/backend/database-guidelines.md` - ACID, database-enforced
  invariant, forward migration, append-history, and pgx rules.
- `.trellis/spec/backend/error-handling.md` - stable error, manual recovery,
  unknown-side-effect replay, and audit requirements.
- `.trellis/spec/guides/cross-layer-thinking-guide.md` - command/result/selection
  boundary checklist.
- `migrations/00002_workspace_sources.sql` - stable Workspace identity and first
  Workspace-scoped business FK.
- `migrations/00034_learning_ops_auth.sql` - existing append-only audit store.
- `migrations/00067_workspace_root_grant.sql` - immutable Registry, mutation
  gate, switch, control, and runtime contracts.
- `migrations/00076_git_remote_sync.sql` - Workspace-keyed remote configuration
  and append-only revision history.
- `migrations/00077_workspace_git_capture.sql` - immutable Workspace Git capture
  checkpoint that requires explicit lineage treatment.
- `migrations/00081_workspace_root_rebinding.sql` - in-progress draft forward
  rebind migration reviewed above.
- `internal/workspace/domain/model.go` and `control.go` - stable Workspace and
  control identities; preliminary rebind types.
- `internal/workspace/application/control.go` - Registry/Control ports and
  preliminary rebind application boundary.
- `internal/workspace/adapter/postgres/registry.go` - ordinary fail-closed
  resolution and preliminary rebind transaction.
- `internal/workspace/adapter/postgres/control.go` - current switch locks,
  generations, snapshots, and operation history.
- `internal/workspace/adapter/postgres/runtime.go` - runtime registration,
  heartbeat, phase, and exact binding authorization.
- `internal/workspace/adapter/postgres/git_capture.go` - contiguous Git capture
  checkpoint enforcement.
- `internal/audit/domain/event.go`, `application/ports.go`, and
  `adapter/postgres/append.go` - canonical redacted Audit event and transaction
  appender.
- `internal/modelsettings/adapter/postgres/audit.go` - existing narrow audit
  appender integration pattern.
- `internal/platform/rootgrant/grant.go` and
  `internal/workspace/runtimegrant/runtime.go` - process generation and runtime
  authority fencing.
- `internal/workspacecontrol/path.go`, `coordinator.go`, `compose_driver.go`, and
  `types.go` - host fingerprint, recovery, grant, and Docker orchestration.
- `cmd/workspacectl/main.go` and `main_test.go` - one-shot command/config/result
  contract and preliminary rebind surface.
- `zhixu`, `deploy/launcher-contract.sh`, and launcher fake Docker fixture -
  protected selection, host lifecycle, mutation lock, and launcher contracts.
- `internal/platform/migration/workspace_root_grant_*_integration_test.go` -
  existing real PostgreSQL root-grant migration/repository test base.

### 12. External references

None. This design is derived from the repository's own PostgreSQL, pgx, Docker,
audit, and Workspace contracts; no external API/version choice is needed.

### 13. Related specs and task artifacts

- `.trellis/workflow.md`
- `.trellis/spec/backend/workspace-root-grant.md`
- `.trellis/spec/backend/database-guidelines.md`
- `.trellis/spec/backend/error-handling.md`
- `.trellis/spec/backend/directory-structure.md`
- `.trellis/spec/guides/cross-layer-thinking-guide.md`
- `.trellis/tasks/08-11-managed-ollama-lifecycle/prd.md`
- `.trellis/tasks/08-11-managed-ollama-lifecycle/design.md`
- `.trellis/tasks/08-11-managed-ollama-lifecycle/implement.md`
- `.trellis/tasks/archive/2026-08/07-31-docker-host-workspace-path/design.md`
- `.trellis/tasks/archive/2026-08/07-31-docker-host-workspace-path/research/host-path-contract.md`

## Caveats / Not Found

- The active managed-Ollama task does not define Workspace root rebinding in its
  PRD, design, or implementation checklist. This is a newly introduced adjacent
  recovery contract and should be explicitly added to task/spec scope before
  implementation is accepted.
- No existing real PostgreSQL integration test for `00081` or Repository rebind
  was found. No launcher contract test for recreated same-path root/rebind was
  found.
- No approved contract was found that defines whether `binding_version` is a
  fingerprint schema version or a rebinding epoch. Current executable code makes
  it a schema version fixed at `1`; this research recommends preserving that fact
  and adding a distinct binding revision.
- No current rebaseline protocol was found for
  `core.workspace_git_capture_checkpoint`. Rebinding a Workspace with existing
  capture state therefore requires either proof of compatible Git lineage or a
  separately designed forward-only rebaseline before release.
- Docker post-revocation inspection is not currently exposed by
  `SwitchRuntime`; `RevokeGrant` command success alone does not prove zero bind.
