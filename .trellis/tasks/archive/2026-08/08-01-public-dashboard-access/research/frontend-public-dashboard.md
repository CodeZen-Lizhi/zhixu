# Research: Frontend public Dashboard access

- Query: Inspect the frontend boot/auth/routing flow required to make `/dashboard` reachable without a Host Controller session while keeping control operations and business data protected.
- Scope: internal
- Date: 2026-08-01

## Findings

### Executive conclusion

The current controller build has two independent credential planes at the server, but the frontend composes them as one gate:

```text
BrowserRouter
  -> HostControlProvider (Controller cookie/state)
    -> HostControlledRuntime
      -> AuthProvider/AuthBoundary (business cookie/session)
        -> WorkspaceCacheBoundary
          -> EventStoreProvider
            -> AppRoutes/AppShell/DashboardPage
```

`HostControlledRuntime` blocks every route until the Controller session and Controller state are available (`web/src/app/App.tsx:57-74`). This is stricter than the HTTP boundary: static SPA assets are always served, `/control/v1/**` owns the Controller session, and ready-only `/api/v1/**` proxying preserves the independent business cookie while stripping Controller credentials (`internal/hostcontroller/http.go:76-87`, `internal/hostcontroller/http.go:384-410`, `internal/hostcontroller/http.go:554-582`).

There are two materially different meanings of "public Dashboard":

1. **Anonymous-safe entry Dashboard (recommended minimal scope):** without a Controller session, exact `/dashboard` renders only the existing static `EntryDashboard` experience. It never mounts business Auth, Query, SSE, AppShell, or a connected Dashboard. A valid Controller session plus verified ready state continues to upgrade `/dashboard` to the existing connected workbench.
2. **Connected Dashboard without a Controller session:** a second browser can see current Workspace data using only business authentication. This is not a frontend-only change. A new, business-authenticated active-runtime Workspace projection is required because a fresh browser has no authoritative Workspace ID, and the only current source is the path-bearing protected Controller state.

The PRD is still `TBD` (`.trellis/tasks/08-01-public-dashboard-access/prd.md:1-13`). The implementation plan must explicitly choose between these meanings. The minimal static entry is safe and local. Claiming multi-browser connected data requires the broader design described below.

### Current startup and routing flow

1. `main.tsx` creates one QueryClient and consumes `#control=...` only in controller mode before rendering the app (`web/src/main.tsx:17-21`). Fragment consumption removes the fragment with `history.replaceState`; it is not persisted (`web/src/api/controller.ts:319-330`).
2. `App` selects `ControllerApp` or `DirectApp` from build-time `VITE_RUNTIME_MODE` (`web/src/app/runtime-mode.ts:1-8`, `web/src/app/App.tsx:98-101`). Production `web/package.json` builds controller mode; direct mode remains a separate development build (`web/package.json:11-18`).
3. `ControllerApp` puts `HostControlProvider` above all route decisions (`web/src/app/App.tsx:90-96`). The provider exchanges/restores a Controller session, fetches authoritative state, polls by `poll_after_ms`, and publishes an effective Workspace only when Active/API/Worker are all ready (`web/src/app/host-control-context.tsx:52-67`, `web/src/app/host-control-context.tsx:181-197`, `web/src/app/host-control-context.tsx:261-303`).
4. Any Controller 401 or instance change clears only Controller CSRF/session state, sets `session_required`, removes the effective Workspace, and suspends business runtime (`web/src/app/host-control-context.tsx:199-213`). Business CSRF remains independent; this is explicitly covered by `web/src/app/host-control-context.test.tsx:161-176`.
5. `/` and `/workspace` always render the Controller-owned Workspace page. Other routes mount `BusinessRuntime` only after Controller state yields a non-empty effective Workspace (`web/src/app/App.tsx:70-74`).
6. `BusinessRuntime` mounts `AuthProvider`/`AuthBoundary` before workspace cache, SSE, routes, or pages (`web/src/app/App.tsx:12-22`). `AuthProvider` first calls public `/api/v1/system/status`; required mode restores a business Session only when a CSRF hint exists, while anonymous/error states never mount children (`web/src/app/auth-context.tsx:62-88`, `web/src/app/auth-context.tsx:153-159`).
7. `AppRoutes` has one shared `AppShell`; exact `/dashboard` is one child route and unknown routes redirect there only after the protected route tree has mounted (`web/src/routes/AppRoutes.tsx:32-64`). In controller mode, a sessionless unknown URL never reaches that wildcard because the Controller gate wins first.

### Controller session and business authorization are already separate server boundaries

- The Controller cookie is issued with `Path=/control/`, `HttpOnly`, and `SameSite=Strict`; unsafe Controller commands additionally require exact Origin, Controller CSRF, `If-Match`, and `Idempotency-Key` (`internal/hostcontroller/http.go:118-149`, `internal/hostcontroller/http.go:325-360`).
- The Host Controller serves static assets without authentication, protects `/control/v1/state`, and proxies `/api/v1/**` only when the runtime is ready (`internal/hostcontroller/http.go:76-87`, `internal/hostcontroller/http.go:151-178`, `internal/hostcontroller/http.go:384-410`).
- The proxy strips Controller headers/cookie/bootstrap credentials but preserves the business Session cookie and normal business Authorization (`internal/hostcontroller/http.go:554-582`; regression tests at `internal/hostcontroller/http_test.go:316-381`).
- The API exposes only system status and auth session exchange outside business auth. All domain routes, including Workspace, Workflow, Proposal, Source Version, and SSE, are registered inside the business auth middleware when auth is required (`internal/app/router.go:144-163`, `internal/app/router.go:194-247`).
- Browser business requests use `authFetch`, always include credentials, inject business CSRF only for Cookie-authenticated unsafe requests, and globally invalidate auth on 401 (`web/src/api/auth.ts:242-259`). SSE independently includes credentials and converges a 401 through the same auth invalidation path (`web/src/events/server-events.ts:577-596`).

Therefore, frontend route composition should not use a Controller session as a substitute for business authentication. Conversely, removing the Controller gate must not be interpreted as permission to mount protected business resources anonymously.

### Active Workspace ownership and why `localStorage` cannot authorize a public page

- `active-workspace.ts` synchronously accepts any canonical UUID from `localStorage`, publishes it through `useSyncExternalStore`, and propagates same-origin storage events (`web/src/app/active-workspace.ts:3-51`). It is explicitly only a hint under the Controller state contract, not a grant or identity proof (`.trellis/spec/frontend/state-management.md:343-350`).
- `HostControlProvider` is currently the authority that clears the hint during suspension and writes the Controller-confirmed ID only after readiness and cleanup (`web/src/app/host-control-context.tsx:123-179`).
- A fresh browser/profile has a separate localStorage namespace and therefore no Workspace ID. Tabs in one profile can share it, but separate browsers cannot. A valid-looking stale UUID can also survive a Controller restart or earlier Workspace.
- `WorkspaceCacheBoundary` and `EventStoreProvider` react immediately to the published ID. SSE opens whenever it is non-empty (`web/src/app/WorkspaceCacheBoundary.tsx:7-20`, `web/src/events/event-store.tsx:38-60`). Dashboard Query keys are scoped correctly, but they still issue protected reads as soon as the hint is non-empty (`web/src/features/business/DashboardPage.tsx:185-217`).

Consequently, a sessionless public route must never decide "connected" from `useActiveWorkspaceId()`. Merely moving `DashboardPage` or `AppRoutes` above `HostControlProvider` is unsafe: a stale UUID would start Workspace, Workflow, Proposal, Source Version, and SSE requests before Controller verification.

### Dashboard dependencies

The current `DashboardPage` contains two distinct products in one component:

- `EntryDashboard` is static except for a local disclosure toggle and navigation links. It makes no API calls (`web/src/features/business/DashboardPage.tsx:97-130`). Existing tests lock the no-request contract (`web/src/features/business/DashboardPage.test.tsx:83-110`).
- The connected Dashboard reads the active Workspace, one waiting Workflow, one failed Workflow, up to five ready Proposals, and four recent Source Versions (`web/src/features/business/DashboardPage.tsx:185-217`). It then renders links to protected details (`web/src/features/business/DashboardPage.tsx:237-300`).

All five `useQuery` calls are declared before the `!enabled` return. `enabled: false` prevents network calls, but the component still requires a QueryClient and reads global active Workspace state. The public entry should therefore be extracted into a pure exported component rather than rendering `DashboardPage` with a synthetic empty Workspace.

`AppShell` also cannot be reused directly outside business providers. It unconditionally calls `useEventStore`, `useAuth`, and `useActiveWorkspaceId` before deriving entry mode (`web/src/app/AppShell.tsx:190-209`). The entry markup later hides event/auth widgets, but the hooks and provider dependencies already exist (`web/src/app/AppShell.tsx:223-259`). A public composition needs a pure entry shell or pure shell primitives; adding fake/no-op Auth/SSE providers would blur the security boundary.

### Recommended minimal architecture: public static entry, connected workbench unchanged

Use exact route and state partitioning at the controller app boundary:

```text
ControllerApp / BrowserRouter / HostControlProvider
  exact /dashboard
    Controller ready + effective Workspace -> existing BusinessRuntime
    otherwise                              -> pure PublicDashboardEntry

  / or /workspace
    valid Controller session -> ControllerWorkspacePage
    otherwise                -> existing ControllerGate

  every other route
    Controller ready + effective Workspace -> existing BusinessRuntime/AuthBoundary
    otherwise                              -> existing ControllerGate/ControllerWorkspaceRoutes
```

Required invariants:

1. The public branch is selected from exact `location.pathname === "/dashboard"`, never from localStorage or a Workspace UUID in the URL.
2. The public branch mounts no `AuthProvider`, `WorkspaceCacheBoundary`, `EventStoreProvider`, `AppRoutes`, connected `AppShell`, or TanStack business Query.
3. It renders the existing entry content and a pure entry shell. Its only state is the local data-boundary disclosure toggle.
4. `/`, `/workspace`, settings, Workflow/Proposal/Source deep links, and all other routes keep their current gates. Navigating from the public entry to `/workspace` must reveal only the "control link required" gate when no Controller session exists, never the directory form or Controller state.
5. A ready Controller session and effective Workspace preserve current behavior: `/dashboard` enters business Auth first, then mounts Query/SSE/routes. A missing business Session in required mode still shows business login; Controller authentication must never authenticate business API calls.
6. When polling reports Controller 401, instance change, switching, or recovery failure, the effective Workspace must be cleared before the public entry appears; the existing `suspendBusinessRuntime`/cache cleanup path should remain the transition owner.
7. A fragment link opened directly at `/dashboard#control=...` must still consume/exchange the token once. The public entry may be the non-sensitive loading fallback, but the fragment must be removed before any navigation or render-owned logging.

This approach is the smallest safe frontend change. It makes the product entry reachable in a fresh or second browser without weakening control commands or exposing connected business data. It intentionally does **not** promise that a second browser sees the connected Dashboard.

### Broader architecture only if connected multi-browser Dashboard is required

If acceptance means "a business-authenticated browser without a Controller cookie must see the current connected Dashboard," the following additional contract is mandatory:

1. Add a business-authenticated, ready-runtime endpoint that returns a strict minimal projection such as `{workspace_id, grant_generation}`. It must not return `root_path`, repository path, recent Workspace registry, operation details, Controller instance/session fields, or Controller CSRF. A display name can be fetched later through the protected Workspace resource if product requirements permit it.
2. Add a runtime Workspace bootstrap owner after `AuthBoundary` and before Query/SSE/routes. It validates the active projection, enters an explicit `loading|none|ready|unavailable` state, publishes an ID only after success, and uses an empty intermediate state plus `clearWorkspaceRuntimeState` before an A-to-B transition.
3. Route `/` and `/workspace` through `HostControlProvider` only; route business pages through business Auth + runtime Workspace bootstrap. The Host Controller's ready-only proxy remains the server-side runtime fence.
4. Treat localStorage only as an optional warm hint. Storage changes must trigger authoritative revalidation, not directly publish an active Workspace. Ideally replace the module-global external store with a provider-owned authoritative snapshot.
5. On API revocation/switch, close the old SSE, abort and remove old Workspace queries, and refresh the active projection before reconnecting. A generation should be part of the remount identity so an identical Workspace ID after runtime replacement cannot retain stale runtime state accidentally.
6. Controller restart must affect only `/workspace` control authorization. Business pages should continue if the ready API and business Session remain valid; otherwise they degrade without displaying cached facts.

No current endpoint supplies this projection. `GET /api/v1/workspaces/{workspace_id}` requires a caller-provided ID and returns `root_path` plus `git.repository_path` (`web/src/api/workspace.ts:87-113`, `web/src/api/workspace.ts:169-175`). `/control/v1/state` has the ID but also active/recent host paths and correctly requires a Controller session (`web/src/api/controller.ts:179-237`, `internal/hostcontroller/http_test.go:119-149`). Making that state public or parsing a 503/proxy response for identity is not acceptable.

### Privacy and information-disclosure concerns

1. **Absolute host paths:** Controller state contains `rootPath` for active and recent Workspaces (`web/src/api/controller.ts:179-191`). The Controller page intentionally renders it only after control authentication (`web/src/features/workspace/ControllerWorkspacePage.tsx:214-223`). A public Dashboard must never fetch or embed this state.
2. **Connected Dashboard DOM path leak:** the connected Dashboard attaches `workspace.data.rootPath` as a `title` attribute even though visible text omits it (`web/src/features/business/DashboardPage.tsx:243-249`). This does not affect a correctly isolated static public entry, but any broader connected/public design should remove that attribute or use a path-free dashboard summary. Hidden/tooltip DOM still counts as disclosure.
3. **Workspace response:** `getWorkspace` decodes both `root_path` and `git.repository_path` (`web/src/api/workspace.ts:87-113`). Do not use this response as an anonymous bootstrap payload.
4. **Knowledge metadata:** Source Version `path`, Proposal target/risk, Workflow definition/status, Workspace name/Git state, and SSE resource references are business data. Their APIs remain behind business authentication, and none belongs in the anonymous entry or browser storage.
5. **Storage hints:** `zhixu.active-workspace-id` is not a secret, but it is stale, browser-profile-local, and attacker-controlled from same-origin script. It cannot prove runtime readiness, grant ownership, business identity, or Controller authority.
6. **Cache transition:** Auth 401 clears the whole QueryClient (`web/src/app/auth-context.tsx:37-59`, `web/src/api/auth.ts:255-257`). Controller suspension clears all known Workspace-scoped query families (`web/src/app/workspace-runtime-state.ts:3-33`). The public transition must continue to unmount pages before any stale cached data can render.
7. **Credential separation:** never reuse the Controller fragment/bootstrap, Controller cookie, or Controller CSRF for business auth. The Host Controller already strips those from the business proxy (`internal/hostcontroller/http.go:554-582`).

### Route and navigation behavior to preserve or decide explicitly

- `/dashboard` must remain in place; no redirect to `/workspace` merely because Controller state is missing or has zero Active Workspace.
- `/` retains Controller Workspace semantics in controller mode (`web/src/app/App.tsx:70-71`); direct mode retains its existing Workspace page (`web/src/routes/AppRoutes.tsx:35`).
- The public entry's "连接知识目录" link continues to `/workspace` (`web/src/features/business/DashboardPage.tsx:106-111`), where Controller authentication is enforced.
- Public entry shell links that target `/settings` or `/settings?section=workspace` need an explicit product decision. The safe default is to allow navigation but show the protected gate; do not render Settings outside business Auth.
- Existing business deep links and `AppRoutes` route table remain intact after Controller/business gates pass. Do not globally redirect protected deep links to the public Dashboard, because that hides authorization/runtime failures and loses deep-link intent.
- The `AppRoutes` wildcard currently redirects to `/dashboard` only inside the mounted business tree (`web/src/routes/AppRoutes.tsx:63`). Whether anonymous unknown routes should also redirect is separate from making exact `/dashboard` public; the minimal change keeps exact-only behavior.

### Affected files and expected tests

Minimal static-entry implementation:

| File | Expected change |
| --- | --- |
| `web/src/app/App.tsx` | Add an exact `/dashboard` public fallback while preserving ready connected behavior and all other Controller gates. |
| `web/src/features/business/DashboardPage.tsx` | Extract/export a pure entry Dashboard; optionally split the connected Query component so public composition has no Query/global Workspace dependency. |
| `web/src/app/AppShell.tsx` or a new `web/src/app/PublicDashboardShell.tsx` | Extract/reuse pure entry chrome without `useAuth`/`useEventStore`; keep route display registry as navigation owner. |
| `web/src/app/App.controller.test.tsx` | Add anonymous exact-route, protected deep-link, ready upgrade, switch/restart downgrade, and provider non-mount assertions. |
| `web/src/features/business/DashboardPage.test.tsx` | Preserve the zero-request static-entry contract and connected query behavior after extraction. |
| `web/src/app/AppShell.test.tsx` | Verify the pure entry shell does not require Auth/SSE and preserves desktop/mobile semantics. |
| `web/src/routes/AppRoutes.test.tsx` | Usually no production change; retain `/dashboard` and all existing deep-link compatibility. |
| `.trellis/spec/frontend/state-management.md` | Update the Controller session-required matrix with the exact anonymous-safe `/dashboard` exception; keep business providers unmounted. |
| `.trellis/spec/frontend/component-guidelines.md` | Clarify that the public entry is independent of Active Workspace hints and Controller/business providers. |

Broader connected multi-browser implementation additionally affects:

| File/area | Expected change |
| --- | --- |
| `web/src/api/workspace.ts` or a new strict runtime API client | Decode the path-free active-runtime projection. |
| `web/src/app/active-workspace.ts` | Stop publishing raw localStorage as authority; move to a validated provider/bootstrap model. |
| `web/src/app/WorkspaceCacheBoundary.tsx` and `web/src/events/event-store.tsx` | Bind cleanup/reconnect to authoritative Workspace ID plus runtime generation. |
| `web/src/app/host-control-context.tsx` | Limit its ownership to control routes/hints; avoid making a Controller cookie necessary for business routing. |
| API/OpenAPI/backend tests | Add the active-runtime projection and prove auth, path omission, ready-only behavior, switch/restart generation, and no cross-Workspace disclosure. |

### Validation scenarios

#### Unit/component gates

1. Fresh context at `/dashboard`, no fragment, no Controller cookie, empty storage: static entry renders, URL stays `/dashboard`, and business Auth/SSE/Routes are absent.
2. Same case with a valid-looking stale `zhixu.active-workspace-id`: still no Workspace/Workflow/Proposal/Source request, no `/api/v1/events`, and no connected shell.
3. Anonymous direct navigation to `/workspace`, `/settings`, `/inbox`, `/proposals/:id`, and `/workflows/:id`: protected page components never render; no host path or cached business text is present.
4. Valid Controller session + ready state + effective Workspace at `/dashboard`: existing `BusinessRuntime` mounts; business Auth still gates Query/SSE.
5. Controller ready but business auth required/anonymous: business login renders; no Dashboard Query or SSE. Controller credentials are never passed to `/api/v1/**`.
6. Controller waiting/zero Active at `/dashboard`: static entry remains on `/dashboard`; it is not redirected to `/workspace`.
7. One-time fragment at `/dashboard#control=...`: URL fragment is removed, exchange occurs once under StrictMode, and success upgrades only after authoritative ready state.
8. Poll transition from connected ready to switching, 401, instance mismatch, or recovery_failed: connected subtree unmounts, old requests abort, old Workspace cache is removed, SSE closes, and only the static entry remains.
9. Public data-boundary toggle retains `aria-expanded`, keyboard operation, and hidden/visible semantics.

#### Multi-browser and lifecycle browser gates

1. **Fresh browser:** open bare `/dashboard`; first viewport shows the public entry at 1440x900 and 390x844 without needing the launcher fragment.
2. **Two isolated browser contexts:** Browser A consumes the one-time fragment and can open `/workspace`; Browser B has no Controller cookie and can only see the public entry. Browser B cannot see the directory form, active/recent Workspace names or paths, or issue control commands.
3. **Controller restart (minimal scope):** while Browser A is on connected `/dashboard`, restart changes Controller instance/session. The page aborts connected work, removes stale facts, closes SSE, and falls back to public entry. `/workspace` requires a fresh launcher link. Browser B remains on the public entry.
4. **Controller restart (broader scope, if selected):** business-authenticated Dashboard remains connected when the ready API/grant is unchanged; only control navigation requires a fresh link. This is a different acceptance result and requires the active-runtime projection.
5. **Workspace switch:** no old Workspace Query/SSE survives the non-ready interval; no A data appears after B becomes active. Public-only browsers never infer either Workspace from storage.
6. Capture console, failed requests, DOM, screenshots, local/session storage, and relevant response bodies. Assert no bootstrap/CSRF/session values and no absolute host/repository path. An expected Controller-session probe 401, if the minimal design retains the global provider, must be classified explicitly rather than ignored as a generic browser failure.
7. Assert one SSE per active Workspace only after business Auth and connected state; public entry has zero SSE connections.

### Files found

| Path | Description |
| --- | --- |
| `web/src/main.tsx` | QueryClient root and one-time Controller fragment consumption. |
| `web/src/app/App.tsx` | Current Controller gate, business provider composition, direct/controller mode split. |
| `web/src/app/host-control-context.tsx` | Controller session/state polling, effective Workspace derivation, business suspension, control commands. |
| `web/src/app/active-workspace.ts` | LocalStorage-backed Workspace UUID external store. |
| `web/src/app/auth-context.tsx` | Business auth mode/session owner and full-screen AuthBoundary. |
| `web/src/app/WorkspaceCacheBoundary.tsx` | Workspace-change cache cleanup effect. |
| `web/src/app/workspace-runtime-state.ts` | Canonical list of Workspace-scoped Query families and cancellation/removal helper. |
| `web/src/app/AppShell.tsx` | Navigation/chrome; unconditionally depends on Auth, Event Store, and active Workspace. |
| `web/src/routes/AppRoutes.tsx` | Shared shell route table, `/dashboard`, protected deep links, wildcard redirect. |
| `web/src/routes/route-display.ts` | Navigation and route-section single source of truth. |
| `web/src/features/business/DashboardPage.tsx` | Static entry plus connected Dashboard Query/rendering in one component. |
| `web/src/api/auth.ts` | Independent business Cookie/CSRF fetch boundary and global 401 invalidation. |
| `web/src/api/controller.ts` | Controller fragment/session/state/command strict client, including path-bearing state. |
| `web/src/api/workspace.ts` | Path-bearing Workspace resource decoder/client. |
| `web/src/api/business.ts` | Strict Workspace-bound Workflow/Proposal/Source clients used by Dashboard. |
| `web/src/events/event-store.tsx` | Single Workspace SSE owner and Query invalidation/recovery. |
| `web/src/events/server-events.ts` | Credentialed SSE fetch-stream and shared auth invalidation on 401. |
| `internal/hostcontroller/http.go` | Static/control/business proxy separation and credential stripping. |
| `internal/app/router.go` | Public status/auth exchange and business-authenticated domain route composition. |
| `internal/hostcontroller/http_test.go` | Existing path non-disclosure and Controller/business credential-isolation tests. |
| `web/src/app/App.controller.test.tsx` | Existing provider mount/unmount tests for waiting, ready, switch, rollback, recovery failure. |
| `web/src/app/host-control-context.test.tsx` | Existing Controller 401, stale response, abort, effective Workspace, and cache cleanup tests. |
| `web/src/features/business/DashboardPage.test.tsx` | Existing static no-request and connected data tests. |
| `web/src/app/AppShell.test.tsx` | Existing entry/connected navigation and mobile focus tests. |

### Code patterns

- **Fail-closed provider composition:** protected children are nested below both Controller readiness and business `AuthBoundary` (`web/src/app/App.tsx:12-22`, `web/src/app/App.tsx:57-74`).
- **Authoritative Controller projection:** effective Workspace is derived only from matching Active/API/Worker/operation state (`web/src/app/host-control-context.tsx:52-67`).
- **Suspend-before-switch:** active ID is emptied and old Query families are cancelled/removed before a control command or state transition can publish the next ID (`web/src/app/host-control-context.tsx:114-179`, `web/src/app/host-control-context.tsx:335-362`).
- **Business auth invalidation:** REST and SSE 401 converge on CSRF removal, Query clear, and anonymous state (`web/src/api/auth.ts:145-149`, `web/src/api/auth.ts:244-259`, `web/src/app/auth-context.tsx:37-59`).
- **Workspace-scoped Query/SSE:** Dashboard Query keys contain Workspace ID; EventStore has one connection owner and closes/clears on ID change (`web/src/features/business/DashboardPage.tsx:188-217`, `web/src/events/event-store.tsx:53-72`, `web/src/events/event-store.tsx:333-350`).
- **Static entry no-request contract:** empty active Workspace returns a product entry and existing tests assert all business clients remain unused (`web/src/features/business/DashboardPage.tsx:97-130`, `web/src/features/business/DashboardPage.test.tsx:83-110`).
- **Strict credential separation:** Host proxy removes only Controller credentials and preserves business credentials (`internal/hostcontroller/http.go:394-405`, `internal/hostcontroller/http.go:554-582`).

### External references and versions

- No external web lookup was needed; this research is based on repository contracts and source.
- Implementation is bound to the checked-in toolchain: React `19.2.7`, React Router DOM `7.18.1`, TanStack React Query `5.101.2`, TypeScript `5.9.3`, Vitest `4.1.10`, and Playwright `1.61.1` (`web/package.json:27-49`).
- Local architecture references: `docs/architecture/frontend-architecture.md:22-32` defines the Route -> Feature -> typed API/SSE dependency direction; `docs/architecture/frontend-architecture.md:219-232` requires Workspace cache cleanup and CSRF/Origin; `docs/architecture/security.md:34-47` separates loopback exposure reduction from Cookie Session authentication.

### Related specs

- `.trellis/spec/frontend/index.md:25-27`: Auth boundary and the existing no-business-request Dashboard entry contract.
- `.trellis/spec/frontend/component-guidelines.md:167-211`: Workspace connection, entry Dashboard, App Shell navigation, exact no-query/no-SSE empty state, and browser gates.
- `.trellis/spec/frontend/state-management.md:255-303`: browser business authentication owner and anonymous Query cleanup.
- `.trellis/spec/frontend/state-management.md:325-384`: Controller state ownership, active hint status, non-ready provider suppression, cache/SSE cleanup, and restart tests.
- `.trellis/spec/frontend/state-management.md:129-195`: single Workspace SSE owner and switch/recovery invariants.
- `.trellis/spec/frontend/hook-guidelines.md:24-33`: Workspace-scoped Query keys and cache cleanup on switch/logout.
- `.trellis/spec/backend/workspace-root-grant.md:30-50`: exact Root grant, protected path-bearing Controller state, and ready-only business proxy.
- `.trellis/spec/backend/auth-security.md:18-27`: business Cookie/CSRF/Capability boundary and separation from Approval/write authorization.
- `docs/architecture/adr/0014-single-user-authentication.md:7-27`: business Session/API Token and write authorization separation.
- `docs/architecture/adr/0018-workspace-root-identity.md:7-18`: Active Workspace identity and switch versus migration semantics.

## Caveats / Not Found

- The task PRD contains no requirements or acceptance criteria yet, so "public" is ambiguous between a static anonymous entry and connected business data without a Controller cookie. This is the critical planning decision.
- No existing public or business-authenticated endpoint returns only the currently granted Workspace ID/generation. A connected multi-browser design cannot be implemented safely from current frontend inputs alone.
- No existing Host Controller Playwright test covers two isolated browser contexts or Controller restart. Current browser suites mainly seed `zhixu.active-workspace-id` directly and run direct mode, so they do not prove controller-mode access separation.
- The current Controller state spec says session 401 shows the control-link gate and does not mount business providers. The minimal exact `/dashboard` static exception preserves the security intent but requires an explicit spec update.
- Tests were not executed; this was read-only design research. Existing test source was inspected to identify baseline coverage and missing scenarios.
