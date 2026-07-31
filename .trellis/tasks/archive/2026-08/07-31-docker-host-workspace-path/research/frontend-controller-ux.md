# Research: Frontend Controller UX and API Boundary

- Query: 研究首次启动无 API/Worker 时的可用页面、Host Controller 认证、Workspace 页面与 active ID/cache/SSE 切换、recent/Unavailable/reconnect 契约及测试，并结合 PRD、ADR 0017/0018 给出最小安全 UX/API 边界。
- Scope: internal
- Date: 2026-07-31

## Findings

### 1. Authoritative product and architecture constraints

The accepted task contract requires a stable Host Controller and a waiting state before any business runtime exists:

- `.trellis/tasks/07-31-docker-host-workspace-path/prd.md`: first `./zhixu up` starts only the Web entry and Host Controller, with no selected host workspace and no API/Worker process holding a workspace grant.
- `docs/architecture/adr/0017-exact-workspace-root-grant.md`: one exact root grant is active at a time; switching must quiesce the old runtime, revoke the old grant, apply the target grant, wait for readiness, and commit or roll back.
- `docs/architecture/adr/0017-exact-workspace-root-grant.md`: the loopback controller link is one-time and must become an HttpOnly, SameSite control session; unsafe operations require CSRF and strict Origin checks; controller credentials must never reach API or Worker.
- `docs/architecture/adr/0018-workspace-root-identity.md`: distinct canonical roots are distinct workspace identities. Selecting a new root is not an implicit rebind; root migration is a separate, explicit operation and is outside this task.
- `docs/architecture/CONTEXT.md:11-45`: distinguishes Workspace Root, Root Grant, Switch, Migration, Registry, Active, Unavailable, Quiescence, and Host Controller.
- `docs/architecture/CONTEXT.md:49-55`: distinguishes the stable Workbench Entry from a ready Working runtime.

These constraints imply that browser-visible controller state, not browser storage and not a temporarily reachable business API, is the source of truth for the active root and runtime readiness.

### 2. Current first-launch topology cannot render a page without API

The current production image and compose topology couple the static SPA to the business API process:

- `deploy/Dockerfile:1-6` builds the web application, while `deploy/Dockerfile:23-32` copies both business binaries and `/app/web` into one runtime image whose entry point is the API binary.
- `cmd/api/main.go:229-233` creates the static handler from `WebAssetsDir` inside the API process.
- `internal/app/router.go:86-183` registers API routes and the SPA handler on the same router. Its non-API fallback at `internal/app/router.go:171-180` serves the SPA.
- `internal/webassets/handler.go:17-62` implements the static asset and SPA fallback behavior.
- `deploy/compose.yml:75-139` publishes the loopback port from the `app` network namespace; the ingress proxy uses `network_mode: service:app`. Removing or not starting `app` therefore also removes the current published Web entry.
- `deploy/compose.yml:101` and `deploy/compose.yml:198` currently mount the configured workspace directly into API and Worker.
- `zhixu:181-195` prepares the current environment and repository workspace, while `zhixu:374-390` starts API and Worker before ingress and waits for readiness.
- `README.md:67-78` documents this old full-stack startup and fixed workspace mount.

Minimal safe topology boundary:

1. The Host Controller must own the stable loopback listener and serve the static SPA for the lifetime of the workbench entry.
2. It must expose only the fixed controller surface under `/control/v1/*`.
3. It may reverse proxy `/api/v1/*` only while the selected business API is ready; no `/api` request may fall through to the SPA.
4. API and Worker can then be absent or recreated without unmounting the workspace-selection UI.
5. Existing API-hosted static serving can remain only as an explicit direct-binary/development mode. The browser must not infer that mode from a transient controller or API network failure.

`/control/*` and `/api/*` must have explicit non-SPA error behavior. Otherwise a missing runtime could return `index.html` with status 200 and look like a valid JSON response.

### 3. Controller authentication must be isolated from business authentication

Current authentication is an API/business-runtime concern:

- `web/src/app/App.tsx:8-19` mounts `AuthProvider` and `AuthBoundary` outside the router and outside all workspace UI. Consequently a missing API prevents the workspace-selection page from rendering.
- `web/src/app/auth-context.tsx:18-22` models only business authentication states.
- `web/src/app/auth-context.tsx:62-88` calls `/api/v1/system/status`; a missing API becomes an authentication error.
- `web/src/app/auth-context.tsx:37-49` clears the full query cache on business-session invalidation.
- `web/src/app/auth-context.tsx:153-158` blocks all children on authentication error or login.
- `web/src/api/auth.ts:70-72` uses `zhixu.csrf-token` for the business CSRF token.
- `web/src/api/auth.ts:107-165` owns business CSRF persistence and cross-tab invalidation.
- `web/src/api/auth.ts:244-259` makes credentialed business requests, injects the business CSRF token for unsafe calls, and invalidates the business session after a 401.
- `web/src/api/auth.ts:324-344` exchanges the business bootstrap token and stores the returned CSRF token.
- `.trellis/spec/frontend/state-management.md:255-303` and `.trellis/spec/backend/auth-security.md:12-54` codify the current business-session behavior.

Reusing `authFetch` or the current bootstrap exchange for the Host Controller is unsafe: a controller 401 would clear business state, a business 401 could destroy controller access, and control credentials could be attached to the wrong capability surface.

Recommended boundary:

- Add a controller session/client/context outside the business `AuthProvider`.
- Use a distinct control cookie name with `HttpOnly`, `SameSite=Strict`, and `Path=/control/` (`Secure` whenever transport supports it).
- Keep a distinct in-memory controller CSRF token. Do not use `zhixu.csrf-token` or the business session-invalidation event.
- Put the one-time controller credential in the URL fragment, for example `#control=<token>`, so it is not included in the HTTP request target or Referer. Exchange it immediately and remove it with `history.replaceState` before other application requests.
- The one-time exchange accepts only the one-time Bearer capability, atomically consumes it, and sets the controller cookie. It must not accept a raw host path or generic operation in the link.
- Every later unsafe control request must require both the controller CSRF token and exact allowed Origin. The controller client may call only fixed `/control/v1/*` operations.
- The business `AuthProvider`, business queries, and business SSE owner mount only after controller state reports a ready runtime.
- Expected API downtime during a switch must not clear the business CSRF token or replace the controller UI with an API login/error page. When the new API is ready, a genuine business 401 may show the inner business login while the controller workspace UI remains usable.
- A controller restart changes `controller_instance_id` and invalidates the prior control session. The UI must require a newly issued loopback link rather than silently retrying an old credential.

### 4. Minimal controller API contract

The controller API should be a small, separately versioned capability surface rather than an extension of the public business workspace API. Host paths and the recent registry are protected controller data.

#### `POST /control/v1/sessions`

- Input: one-time Bearer token; empty body.
- Effect: atomically consumes the token and sets the control-session cookie.
- Response: `{ controller_instance_id, csrf_token, expires_at }`.
- Replay, expiry, or a controller-lifetime mismatch returns a stable authorization problem.

#### `GET /control/v1/session`

- Restores/checks the current controller session without accepting a raw token.
- Returns controller instance identity and session metadata; it may rotate/return the in-memory CSRF token according to the chosen session design.

#### `GET /control/v1/state`

This is the single browser bootstrap and polling fact. A minimal strict response is:

```json
{
  "schema_version": 1,
  "controller_instance_id": "...",
  "state_version": 17,
  "runtime": {
    "status": "waiting_for_workspace",
    "api": "stopped",
    "worker": "stopped"
  },
  "active_workspace": null,
  "recent_workspaces": [],
  "operation": null,
  "poll_after_ms": 1000
}
```

Recommended enums:

- Runtime status: `waiting_for_workspace | switching | ready | recovery_failed`.
- Per-process readiness: `stopped | starting | ready | unavailable`.
- Registry workspace status: `active | inactive | unavailable`.
- Availability: `available | unavailable | migration_required`, with a stable reason code and optional user-safe detail.
- Operation phase: `validating | quiescing | revoking | applying_grant | starting_runtime | waiting_ready | rolling_back`.
- Terminal operation result: `succeeded | rolled_back | failed`.

A registry item minimally contains `{ id, name, root_path, status, availability, last_opened_at, version }`. Registry presence and availability must remain separate from an active grant. An operation contains a stable `operation_id`, source/target IDs, phase/result, timestamps, and a stable problem when applicable. It must not expose container-internal mount paths or generic compose/runtime commands.

Polling is the smallest robust transport here: business SSE is intentionally unavailable during a switch. The controller can later add an event stream, but controller lifecycle state must not be multiplexed into the workspace-scoped business SSE contract.

#### `POST /control/v1/workspace-switches`

- Asynchronous, returns `202 Accepted` and `{ operation_id, status_url }`.
- Requires controller CSRF, exact Origin, an `Idempotency-Key`, and an expected `state_version`/`If-Match` to reject stale tabs.
- `202` means only that the controller accepted the operation; it is not an active-workspace success signal.
- Requests must be discriminated structurally:

```json
{ "target_kind": "registered", "workspace_id": "..." }
```

or:

```json
{
  "target_kind": "new",
  "name": "...",
  "root_path": "/canonical/host/path",
  "initialize_git": false
}
```

The contract must reject a payload that supplies both an existing `workspace_id` and a replacement `root_path`. This makes implicit rebind impossible at the API boundary and preserves ADR 0018.

#### Operation and registry endpoints

- `GET /control/v1/workspace-switches/{operation_id}`: operation resumption/polling; alternatively, the same complete operation can be embedded in `GET /control/v1/state`.
- `POST /control/v1/workspaces/{id}/availability-checks`: retry host-path stat/validation only. It must not grant, mount, auto-create, delete, or infer a replacement path.
- `DELETE /control/v1/workspaces/{id}`: remove an inactive registry entry only. It must reject active/in-flight entries and must never delete filesystem content.

Suggested stable problem codes include:

- `CONTROL_UNAUTHORIZED`, `CONTROL_CSRF_REJECTED`, `CONTROL_STATE_CONFLICT`
- `WORKSPACE_ROOT_NOT_FOUND`, `WORKSPACE_ROOT_NOT_DIRECTORY`, `WORKSPACE_ROOT_PERMISSION_DENIED`, `WORKSPACE_ROOT_SYMLINK_UNSAFE`
- `WORKSPACE_MIGRATION_REQUIRED`, `WORKSPACE_SWITCH_IN_PROGRESS`, `WORKSPACE_QUIESCENCE_TIMEOUT`
- `WORKSPACE_RUNTIME_NOT_READY`, `WORKSPACE_SWITCH_ROLLED_BACK`, `WORKSPACE_MANUAL_RECOVERY_REQUIRED`

The problem body should have a fixed schema such as `{ code, message, retryable, operation_id?, field_errors? }`. UI decisions must use `code` and typed state, not parse human-readable messages.

### 5. Workspace page state and user flow

Current behavior treats browser state as authority and cannot represent controller switching:

- `web/src/features/workspace/WorkspacePage.tsx:91-125` drives create/get mutations from a locally stored workspace ID.
- `web/src/features/workspace/WorkspacePage.tsx:108-115` writes the active ID immediately after a business API create response.
- `web/src/features/workspace/WorkspacePage.tsx:137-180` offers only a new-root form or a manual UUID field; a connected workspace has no switch action.
- `web/src/features/workspace/WorkspacePage.tsx:184-192` advises clearing the local reference and recreating after a failed lookup, which conflicts with retaining an Unavailable registry entry.
- `web/src/features/workspace/WorkspacePage.tsx:215` always mounts business system status.
- `web/src/api/workspace.ts:12-22` accepts only `status: "active"`.
- `web/src/api/workspace.ts:87-114` strictly decodes that business representation.
- `web/src/api/workspace.ts:136-160` reduces failures to a message and loses typed controller remediation details.
- `web/src/api/workspace.ts:163-179` exposes only business create/get/scan operations.
- `internal/workspace/http/handler.go:23-47` has no recent registry or runtime switch route.
- `api/openapi/openapi.json:309` documents the current business create/get/scan surface; `api/openapi/openapi.json:11242-11349` has a generic root and an `active`-only workspace status.

Recommended controller-mode flow:

1. Exchange/restore the control session, then load controller state. Do not start business authentication, queries, or SSE yet.
2. In `waiting_for_workspace`, show the host-root form and recent registry. The page is useful while API and Worker are stopped.
3. On switch submission, block duplicate UI requests until the idempotent response resolves. After `202`, navigate with replace semantics to `/workspace`, suspend the active business scope, close SSE, and cancel/remove old workspace-scoped cache.
4. Render the server-reported operation phase. Client-only spinners or elapsed timers are not authoritative; a reload resumes from `GET /control/v1/state`.
5. Commit the browser active workspace only after the operation is terminal `succeeded`, API and Worker are both `ready`, and the returned `active_workspace.id` matches the operation target.
6. Mount business authentication and exactly one business SSE connection only after that commit.
7. If target activation fails but rollback succeeds, resume the old workspace only after its API and Worker are ready; label the operation as rolled back, never as a successful switch.
8. If rollback fails, controller state has no active workspace and is `recovery_failed`; old data and routes remain unmounted until manual recovery.

Recent list behavior:

- Active: show name and exact host path, with no duplicate switch action.
- Inactive and available: offer Switch.
- Unavailable: retain the row and its last known path; offer Retry availability check and Remove from list.
- Migration required: retain the row and show the stable migration-required state. Root migration itself remains out of scope.
- Removing an entry must explicitly state that it removes only registry metadata and never deletes files.
- `last_opened_at` comes from the controller registry; the browser must not probe a host path, auto-create it, guess a sibling/parent path, or infer availability from prior browser state.

The current manual UUID field should disappear in controller mode. If direct-binary development mode is retained, its legacy behavior must be selected by an explicit boot/config contract and kept out of the controller-mode state machine.

### 6. Active workspace identity and route switching

Current active identity is local-storage driven:

- `web/src/app/active-workspace.ts:3-16` reads `zhixu.active-workspace-id` and silently treats it as active.
- `web/src/app/active-workspace.ts:31-43` writes/removes that value and emits a browser event.
- `web/src/routes/AppRoutes.tsx:32-65` mounts all domain routes without a controller-ready guard.
- `web/src/app/AppShell.tsx:141-167` treats any local ID as connected; its labels describe only business SSE connectivity.

Minimal authority rule:

- In controller mode, local storage is at most a non-authoritative rendering hint. Ignore or remove it unless it exactly matches the controller-confirmed ready `active_workspace.id`.
- During waiting, switching, or failed recovery, the effective active ID is `null` even if old local storage exists.
- On an active-ID change, replace an old workspace detail route with `/workspace` or `/dashboard` before mounting the new workspace subtree. Do not carry document, proposal, review query-string, collection, chat, or artifact IDs into the new identity.
- Deep links may remain compatible on reload only when the controller confirms the same active ID as ready.
- Key the business-runtime subtree by active workspace ID so component-local drafts and mutation callbacks cannot survive a switch.
- Distinct canonical roots always produce distinct IDs. Do not copy the old workspace ID to a new root. A future same-ID migration would need an explicit binding generation as well as identity, but that is outside ADR 0018's current task scope.

The shell needs separate runtime labels for `waiting_for_workspace`, controller operation phases, `rolling_back`, and `recovery_failed`. Existing SSE `connecting/reconnecting/recovery_failed` labels remain business event-channel health; they cannot represent an intentionally stopped API/Worker.

### 7. Cache and mutation isolation during switch

Current cleanup is split and incomplete:

- `web/src/app/WorkspaceCacheBoundary.tsx:17-38` clears graph, semantic links, collections, collection exports, knowledge health, search, timeline, review, memory, interview, and attachment-export queries after an active-ID change.
- `web/src/events/event-store.tsx:37-84` separately removes workspace, business, search, RAG, collection, export, health, graph, and semantic-link query families when the SSE owner closes.
- `web/src/events/event-store.tsx:90-137` refetches additional review, memory, and interview data during recovery.
- `web/src/features/artifacts/query-keys.ts:1-6` defines a workspace-scoped `artifacts` root that neither cleanup owner currently removes.
- `.trellis/spec/frontend/state-management.md:129-182` requires one SSE owner, old-connection closure, workspace cache isolation, and rejection of old callbacks.
- `.trellis/spec/frontend/state-management.md:219-245` additionally requires pending export mutation/reset isolation on workspace switch.
- `docs/architecture/frontend-architecture.md:219-224` requires workspace-scoped cache clearing on logout/switch.

Recommended invariant:

- One shared `clearWorkspaceRuntimeState(oldWorkspaceId)` implementation cancels first, then removes every workspace-scoped query family. At minimum it must cover roots `workspace`, `business`, `search`, `rag`, `collections`, `collection-exports`, `workspace-attachment-exports`, `knowledge-health`, `graph`, `semantic-links`, `timeline`, `review`, `memory`, `interview`, and `artifacts`.
- Execute it when switch acceptance suspends the old scope, not only after the next ID becomes active.
- Queries should consume abort signals where supported. Mutations generally cannot prove that their server-side effects stopped; disabling UI and ignoring late callbacks is necessary but not sufficient.
- Host Controller quiescence is the authoritative side-effect boundary. Frontend `cancelQueries` must never be treated as proof that writes or jobs stopped.
- Unmount/key the old workspace subtree so late mutation success, optimistic state, form drafts, and selected object IDs cannot populate the new workspace.
- When rollback succeeds, refetch the old workspace after readiness instead of restoring an unverified cache snapshot.

### 8. SSE lifecycle and reconnect semantics

Current business SSE behavior is correctly workspace-scoped but assumes a continuously addressable API:

- `web/src/events/event-store.tsx:37-69` starts one connection from the active workspace ID.
- `web/src/events/event-store.tsx:257-350` owns connection creation, cleanup, recovery, and stale-snapshot suppression.
- `web/src/events/event-store.tsx:353-359` prevents an old workspace snapshot from being displayed after ID change.
- `web/src/events/server-events.ts:57-62` exposes `connecting | open | reconnecting | recovery_failed | closed`.
- `web/src/events/server-events.ts:541-669` calls `/api/v1/events?workspace_id=...`, includes business credentials, invalidates business auth on 401, and retries with cursor recovery.
- `web/src/events/event-store.tsx:23` namespaces the resume cursor by workspace ID.
- `web/src/events/event-store.test.tsx:128-170` covers one connection and cleanup on ID switch.
- `web/src/events/event-store.test.tsx:542-579` covers isolation from an old recovery/callback.

Required switch behavior:

- Close the old business SSE connection before the old root grant is revoked.
- Do not run its reconnect loop while controller state intentionally reports API stopped/starting.
- Start exactly one new business SSE connection only after controller state reports the target active and ready.
- Preserve the old cursor under the old workspace ID so a future switch back can resume it; never copy a cursor to the new identity.
- Old connection callbacks and delayed recovery responses must remain incapable of writing into the new query cache.
- Controller switch progress and failure recovery are driven by controller state polling, not by business SSE `reconnecting`.

### 9. Affected files and likely ownership

Frontend composition and control client:

- `web/src/app/App.tsx:8-19` - split stable controller/workspace entry from the ready-only business auth/runtime subtree.
- `web/src/app/auth-context.tsx:62-88` and `web/src/app/auth-context.tsx:153-158` - retain as business auth, mounted only for a ready runtime.
- `web/src/api/auth.ts:70-72`, `web/src/api/auth.ts:244-259`, `web/src/api/auth.ts:324-344` - keep business session semantics; do not reuse for control capability.
- New likely owners `web/src/api/controller.ts` and `web/src/app/controller-context.tsx` - strict control protocol, fragment exchange, session, polling, and typed operation state. These files do not currently exist.
- `web/src/api/system-status.ts:142-161` and `web/src/api/system-status.ts:219-260` - current business-only status decoder/fetcher must not be the controller bootstrap.
- `web/src/features/system-status/use-system-status.ts:5-12` and `web/src/features/system-status/SystemStatusPage.tsx:120-140` - render/fetch only when business runtime is ready.

Workspace identity, UI, routes, and cache:

- `web/src/app/active-workspace.ts:3-43` - replace local-storage authority with controller-confirmed effective state.
- `web/src/features/workspace/WorkspacePage.tsx:91-125`, `web/src/features/workspace/WorkspacePage.tsx:137-192`, `web/src/features/workspace/WorkspacePage.tsx:215` - waiting form, recent registry, typed Unavailable states, switch progress, rollback/manual recovery.
- `web/src/api/workspace.ts:12-22`, `web/src/api/workspace.ts:87-114`, `web/src/api/workspace.ts:136-179` - leave business workspace representation separate from the controller representation.
- `web/src/routes/AppRoutes.tsx:32-65` - ready guard and replace navigation away from old detail routes.
- `web/src/app/AppShell.tsx:141-167`, `web/src/app/AppShell.tsx:180-188` - show controller runtime/grant state separately from SSE connectivity.
- `web/src/app/WorkspaceCacheBoundary.tsx:17-38` - converge complete cancellation/removal and trigger it at switch suspension.
- `web/src/features/artifacts/query-keys.ts:1-6` - include the currently missed artifact cache family.
- `web/src/events/event-store.tsx:37-84`, `web/src/events/event-store.tsx:257-350` - mount only for a ready active identity and share cleanup ownership.
- `web/src/events/server-events.ts:541-669` - retain as business-only SSE; pause/unmount during controlled downtime.

Host Controller, runtime, and deployment:

- New controller command/internal package - no Host Controller implementation was found in the current tree.
- `internal/webassets/handler.go:17-62` - reusable static serving behavior for the stable entry, with explicit `/api` and `/control` exclusions.
- `internal/app/router.go:86-183` and `cmd/api/main.go:229-233` - retain API/direct-mode routes while moving production entry ownership to the controller.
- `internal/auth/http/handler.go:96-180` and `internal/auth/http/handler.go:197-214` - useful session/CSRF patterns, but controller credentials need a separate authority and cookie namespace.
- `internal/workspace/http/handler.go:23-47` - existing business workspace API has no registry/switch responsibility.
- `deploy/Dockerfile:1-32`, `deploy/compose.yml:75-139`, `deploy/compose.yml:155-210` - stable controller/static process and restartable, exact-grant API/Worker units.
- `zhixu:181-195`, `zhixu:289-390`, `zhixu:451-463` - waiting-state startup, controller link/status, and removal of the current full-runtime-first assumption.
- `.env.example:1-21` and `README.md:67-78` - document explicit runtime modes and the first-launch waiting state.
- `api/openapi/openapi.json:309` and `api/openapi/openapi.json:11242-11349` - keep the public business API contract stable; publish the controller surface as a separately versioned schema or clearly separated capability section.

### 10. Required test coverage

Contract and decoder tests:

- Strictly decode every controller runtime, workspace availability, operation phase/result, stable problem, and empty recent-list state.
- Reject unknown/ambiguous switch payloads, especially existing ID plus replacement root.
- Verify stale `state_version`, duplicate `Idempotency-Key`, and operation resumption behavior.

Control authentication tests:

- Fragment token never reaches request URLs, logs, Referer, localStorage, sessionStorage, or business request headers; `history.replaceState` removes it immediately.
- Token replay/expiry, cookie lifetime/path/flags, exact Origin, CSRF failure, and controller restart/lifetime mismatch.
- Controller 401 does not clear business CSRF; business 401 does not clear controller session; neither credential crosses the `/control` and `/api` boundary.

First-launch integration tests:

- Static UI is reachable with no API/Worker process and zero workspace bind.
- Waiting page issues no `/api/v1/*` or business SSE request.
- Host-root form and recent registry render from protected controller state.
- `/api/*` and `/control/*` never return SPA HTML for a missing route/runtime.

Switch state-machine tests:

- `202` is not treated as success; every server phase renders and reload resumes it.
- Quiescence timeout leaves/resumes the old runtime only after readiness.
- Invalid/unavailable target stays in registry and exposes Retry/Remove without auto-create.
- Target runtime failure followed by successful rollback resumes the old ID.
- Rollback failure yields `active_workspace: null`, `recovery_failed`, no business subtree, and no old cached view.
- New-root requests produce a new identity; registered switches send only the registry ID; no implicit rebind path exists.

Cache, mutation, route, and SSE tests:

- Switching cancels/removes every query root listed above, including `artifacts`.
- Pending queries abort where supported; late old mutation/query callbacks cannot write new cache or navigate into the new workspace.
- Representative old detail routes and review query IDs are replaced before the target subtree mounts; same-active reload deep links remain valid.
- Old SSE closes before grant revocation; no reconnect occurs during intentional downtime; exactly one target SSE opens after readiness.
- Old cursor remains under old ID and is not copied; old callbacks cannot overwrite target state.

Existing test files to extend or mirror:

- `web/src/app/App.test.tsx:15-22`
- `web/src/app/auth-context.test.tsx:30-208`
- `web/src/app/active-workspace.test.ts:13-42`
- `web/src/app/WorkspaceCacheBoundary.test.tsx:22-45`
- `web/src/features/workspace/WorkspacePage.test.tsx:52-145`
- `web/src/app/AppShell.test.tsx:23-96`
- `web/src/routes/AppRoutes.test.tsx:50-93`
- `web/src/events/event-store.test.tsx:128-170` and `web/src/events/event-store.test.tsx:542-579`
- `web/src/events/server-events.test.ts:216`
- `deploy/launcher-contract.sh:96-112`, which currently expects the full API/Worker startup and must gain a first-launch waiting contract.
- `internal/platform/config/compose_contract_test.go` and `internal/webassets/handler_test.go` for exact composition/static routing boundaries.

Browser/fault smoke coverage should include desktop and mobile waiting/switching/recovery views, reload during each switch phase, controller restart, API/Worker delayed readiness, console errors, request credential separation, and verification that the stable page never disappears. Exact-grant mount assertions belong to controller/deployment integration tests rather than frontend tests.

### 11. Compatibility and rollout risks

- Stable-origin ownership changes from API/network namespace to Controller. Business cookies and allowed Origin must continue to use the stable controller origin; reverse proxying must preserve the expected Origin/Host behavior.
- The existing business session cookie uses `Path=/`, so the browser will also send it to `/control`. The controller must ignore it and must not forward or log it. A control cookie with `Path=/control/` prevents control credentials from being sent to `/api`.
- Existing `zhixu.active-workspace-id` values can resurrect stale identities. Controller mode needs an explicit one-time ignore/clear rule and must never start API/SSE work from that value before confirmation.
- Existing strict decoders accept only the current business shapes, including `Workspace.status === "active"` and exact system-status keys. A separate controller model avoids accidentally breaking the public business API and older direct-mode clients.
- Reload during an operation cannot depend on React/local state; controller state and operation IDs must be recoverable for the controller lifetime.
- A controller restart invalidates its one-time/session capability. The UX needs a clear expired-link/new-link path. Durable recovery of an already running runtime/registry after controller restart is a controller design requirement not defined in the current frontend.
- Changing the exposed loopback port would alter Origin validation and cookie behavior. Keeping the existing stable origin minimizes auth compatibility risk.
- Registry persistence and its single authoritative storage owner were not found in production code. The frontend must consume one controller response and must not merge a browser-local recent list with a database list.
- No current frontend contract couples controller readiness for both API and Worker. Treating API HTTP readiness alone as switch success would violate the task contract.
- Root migration is explicitly out of scope. An Unavailable/moved root must not turn into a client-side edit/rebind flow.
- `.trellis/tasks/07-31-docker-host-workspace-path/research/host-path-contract.md` originally evaluated a parent-directory same-path bind, but its top-level decision override now explicitly supersedes that proposal. Only its current-code facts and corrected exact-root contract may drive implementation; ADR 0017/PRD remain authoritative.

## Files Found

- `.trellis/tasks/07-31-docker-host-workspace-path/prd.md` - active task product and security requirements.
- `docs/architecture/adr/0017-exact-workspace-root-grant.md` - accepted exact-root/controller/switch decision.
- `docs/architecture/adr/0018-workspace-root-identity.md` - accepted identity versus migration decision.
- `docs/architecture/CONTEXT.md` - shared domain vocabulary and entry/working states.
- `.trellis/spec/frontend/state-management.md` - current auth, cache, mutation, and SSE invariants.
- `.trellis/spec/backend/auth-security.md` - current session, Origin, CSRF, and bootstrap constraints.
- `web/src/app/App.tsx` - current top-level provider composition.
- `web/src/app/auth-context.tsx` and `web/src/api/auth.ts` - current business session implementation.
- `web/src/features/workspace/WorkspacePage.tsx` and `web/src/api/workspace.ts` - current workspace UI/business API contract.
- `web/src/app/active-workspace.ts` - current local-storage active-ID owner.
- `web/src/app/WorkspaceCacheBoundary.tsx` - current partial query cleanup.
- `web/src/events/event-store.tsx` and `web/src/events/server-events.ts` - current business SSE lifecycle.
- `web/src/routes/AppRoutes.tsx` and `web/src/app/AppShell.tsx` - current unguarded workspace routes and connectivity presentation.
- `deploy/Dockerfile`, `deploy/compose.yml`, and `zhixu` - current static/API/Worker startup topology.
- `internal/app/router.go`, `internal/webassets/handler.go`, and `cmd/api/main.go` - current API-owned static delivery.
- `internal/workspace/http/handler.go` and `api/openapi/openapi.json` - current business workspace endpoints and schema.

## Code Patterns

- API-owned SPA fallback: `internal/app/router.go:171-180`, `internal/webassets/handler.go:17-62`.
- API-gated whole frontend: `web/src/app/App.tsx:8-19`, `web/src/app/auth-context.tsx:153-158`.
- Business-only credentialed fetch and 401 invalidation: `web/src/api/auth.ts:244-259`.
- Local-storage active authority: `web/src/app/active-workspace.ts:3-43`.
- Immediate active commit after create: `web/src/features/workspace/WorkspacePage.tsx:108-115`.
- Split and incomplete cache ownership: `web/src/app/WorkspaceCacheBoundary.tsx:17-38`, `web/src/events/event-store.tsx:70-84`, `web/src/features/artifacts/query-keys.ts:1-6`.
- Single workspace SSE owner and cursor namespace: `web/src/events/event-store.tsx:23`, `web/src/events/event-store.tsx:257-350`.
- Current runtime requires full API/Worker before ingress: `zhixu:374-390`.

## External References

No external sources were required. The accepted project PRD, ADR 0017, ADR 0018, Trellis specs, and current source are the authoritative references for this research.

## Related Specs

- `.trellis/spec/frontend/state-management.md:129-182` - workspace cache and SSE isolation.
- `.trellis/spec/frontend/state-management.md:219-245` - pending export mutation behavior on switch.
- `.trellis/spec/frontend/state-management.md:255-303` - current business authentication/session ownership.
- `.trellis/spec/backend/auth-security.md:12-54` - business cookie, Origin, CSRF, and bootstrap rules.
- `docs/architecture/frontend-architecture.md:219-224` - workspace-scoped cache isolation.
- `docs/architecture/adr/0017-exact-workspace-root-grant.md` - controlling accepted security/runtime boundary.
- `docs/architecture/adr/0018-workspace-root-identity.md` - controlling identity/migration boundary.

## Caveats / Not Found

- No Host Controller implementation, controller HTTP schema, recent-workspace registry owner, or controller-state persistence implementation exists in the inspected tree.
- The exact controller restart/recovery persistence mechanism is not specified. Frontend can require `controller_instance_id` and resumable state, but backend/deployment design must decide persistence and reconciliation.
- No current production test proves that the static entry remains reachable while API and Worker are absent.
- No current centralized inventory guarantees that all future workspace-scoped query/mutation stores join switch cleanup; implementation should make registration/ownership explicit to prevent another `artifacts`-style omission.
- The original parent-bind proposal in `.trellis/tasks/07-31-docker-host-workspace-path/research/host-path-contract.md` is explicitly superseded and its active contract has been corrected to exact-root grants; implementation must continue to treat ADR 0017/PRD as authoritative.
