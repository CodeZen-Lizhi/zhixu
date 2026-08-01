# Research: Go Host Controller public dashboard/control boundary

- Query: Analyze the Go Host Controller authentication, session, and routing boundary needed to make `GET /dashboard` usable without a Controller credential while keeping Workspace root grants, Workspace mutations, runtime rebuild, and sensitive control state protected.
- Scope: internal
- Date: 2026-08-01

## Findings

### Executive conclusion

The Go Host Controller does **not** currently authenticate the HTML request for
`GET /dashboard`. The request already falls through to the static SPA handler and
can return `index.html` anonymously. The actual blocker is the client bootstrap:
in Controller mode the React root always attempts to establish a Controller
session and read the authenticated full control state before it mounts either the
entry Dashboard or the business application.

This means a change that only adds a `/dashboard` route in Go would be a no-op.
The boundary that needs splitting is:

```text
public SPA/document access
  -> minimal effective-runtime discovery
  -> independent business authentication

privileged Controller session
  -> host paths and Workspace registry
  -> operation detail and concurrency version
  -> Workspace switch / availability check / removal
  -> Compose rebuild and exact root-grant mutation
```

The safest small-backend design is a **separate, GET-only, minimal runtime-access
projection** derived from the same authoritative Controller state used by the
business proxy. It must not make the existing `/control/v1/state` conditionally
public or reuse its response type. The full state endpoint and every control
mutation remain unchanged behind the Controller Cookie; unsafe commands retain
the Origin, CSRF, `If-Match`, and idempotency checks.

### Current request flow

| Request | Current Go path | Controller credential | Result / exposure |
| --- | --- | --- | --- |
| `GET /dashboard` | `Handler.ServeHTTP` default branch -> `webassets.Handler` SPA fallback | None | Static `index.html`; no Store or Docker operation is required (`internal/hostcontroller/http.go:76`, `internal/webassets/handler.go:42`). |
| `GET /control/v1/livez` | explicit control route | None, exact Host only | `204`, `no-store`; no state read (`internal/hostcontroller/http.go:90`, `internal/hostcontroller/http.go:109`). |
| `POST /control/v1/sessions` | control session exchange | One-time Bootstrap Bearer plus exact Host and Origin | Consumes the token once and sets `zhixu_control_session`, `HttpOnly`, `SameSite=Strict`, `Path=/control/` (`internal/hostcontroller/http.go:118`, `internal/hostcontroller/session.go:61`). |
| `GET /control/v1/session` | `authenticate` | Exactly one valid Controller Cookie and exact Host | Returns Controller instance/session/CSRF metadata (`internal/hostcontroller/http.go:151`, `internal/hostcontroller/http.go:306`). |
| `GET /control/v1/state` | `authenticate` -> `Store.State` | Exactly one valid Controller Cookie and exact Host | Returns full control state, including host root paths, recent registry, operation details, state version, and readiness (`internal/hostcontroller/http.go:161`, `internal/hostcontroller/types.go:53`). |
| Control mutations | `authorizeUnsafe` -> typed Store method | Cookie + exact Host/Origin + CSRF + `If-Match` + idempotency key | Only these routes reach `BeginSwitch`, `CheckAvailability`, and `RemoveWorkspace` (`internal/hostcontroller/http.go:188`, `internal/hostcontroller/http.go:259`, `internal/hostcontroller/http.go:284`, `internal/hostcontroller/http.go:325`). |
| `GET /api/v1/...`, `/livez`, `/readyz` | `proxyBusiness` | No Controller session required | Proxy opens only through `BackendLocator.ReadyBackend`; Controller headers/Cookie are stripped and business credentials are preserved (`internal/hostcontroller/http.go:81`, `internal/hostcontroller/http.go:384`, `internal/hostcontroller/http.go:554`). |
| Other paths | static handler | None | SPA/static assets. Unknown `/api/...` paths are explicitly prevented from falling back to HTML (`internal/hostcontroller/http.go:83`). |

The client then changes the effective behavior:

1. `main.tsx` consumes `#control=...` once in Controller mode
   (`web/src/main.tsx:20`, `web/src/api/controller.ts:319`).
2. `HostControlProvider` exchanges the fragment or calls
   `GET /control/v1/session`, then always calls authenticated
   `GET /control/v1/state` (`web/src/app/host-control-context.tsx:181`,
   `web/src/app/host-control-context.tsx:215`).
3. A missing/expired Controller session becomes `session_required` and prevents
   `BusinessRuntime` from mounting (`web/src/app/host-control-context.tsx:199`,
   `web/src/app/App.tsx:57`).
4. Only a fully ready control state produces `effectiveWorkspaceId`; then the
   separate business `AuthProvider`, Query tree, and SSE store mount
   (`web/src/app/host-control-context.tsx:52`, `web/src/app/App.tsx:12`).
5. `DashboardPage` already has a correct no-Workspace entry projection and
   suppresses all Workspace/Workflow/Proposal/Source queries when its Workspace
   ID is empty (`web/src/features/business/DashboardPage.tsx:185`,
   `web/src/features/business/DashboardPage.tsx:219`). It cannot currently be
   reached through `ControllerApp` without first passing the Controller gate.

### Existing security separations worth preserving

- Controller credentials and business credentials are already independent.
  The Controller Cookie is `zhixu_control_session` with `Path=/control/`;
  the reverse proxy removes it but preserves the business `zhixu_session`
  Cookie (`internal/hostcontroller/session.go:15`,
  `internal/hostcontroller/http_test.go:316`).
- A Controller Bootstrap Bearer is stripped before proxying, while an ordinary
  business Bearer is preserved (`internal/hostcontroller/http.go:554`,
  `internal/hostcontroller/http_test.go:364`).
- The business API independently applies single-user Session/API Token
  authentication and capabilities. In required mode, anonymous domain reads
  remain `401`; only liveness, readiness, system status, and the business
  Bootstrap exchange are open (`internal/app/router.go:144`,
  `internal/app/router_test.go:470`, `.trellis/spec/backend/auth-security.md:20`).
- Business proxy readiness is derived from the durable Workspace snapshot and
  current runtime discovery, not from the Controller Cookie or browser storage
  (`internal/hostcontroller/types.go:189`). This is the key reason dashboard
  access can be separated from Controller authorization without weakening the
  root grant.
- `Coordinator.State` projects from the durable control snapshot. The projection
  marks a non-terminal operation as switching and only marks ready when both API
  and Worker have fresh, matching runtime records for the active Workspace and
  generation (`internal/hostcontroller/coordinator.go:148`,
  `internal/hostcontroller/coordinator.go:1002`).

### Sensitive full-state fields that must stay authenticated

The current `State` wire is not suitable for anonymous use:

- `Workspace.RootPath` is a real canonical host path; Workspaces also expose
  names, availability reasons, and last-opened timestamps
  (`internal/hostcontroller/types.go:53`).
- `RecentWorkspaces` enumerates registered roots and `ActiveWorkspace` identifies
  the current grant (`internal/hostcontroller/types.go:76`).
- `Operation` exposes operation/target IDs, target name, exact phase, stable error
  code, and timestamps (`internal/hostcontroller/types.go:63`).
- `StateVersion` is the mutation concurrency value and `ControllerInstanceID`
  binds operations to one Controller lifetime (`internal/hostcontroller/types.go:76`,
  `internal/hostcontroller/http.go:172`).
- The existing anonymous-state regression explicitly asserts that a private host
  path is not returned without a Session (`internal/hostcontroller/http_test.go:119`).

Do not make `/control/v1/state` public, do not return a redacted/full response
from the same URL depending on Cookie presence, and do not add optional fields to
that response. The frontend decoder rejects unknown keys and binds the strong
ETag to `state_version`, so an in-place change also has avoidable compatibility
and cache ambiguity (`web/src/api/controller.ts:216`,
`web/src/api/controller.test.ts:125`).

### Recommended backend design

Add a distinct anonymous, GET-only runtime-access read model. The exact path is a
design choice; a separate namespace such as `GET /host/v1/runtime` most clearly
keeps public access outside the privileged `/control/` Cookie path. If the
existing control router is preferred for a smaller diff, an explicit route such
as `GET /control/v1/runtime-access` is viable only if it returns exactly the same
minimal response with or without a Controller Cookie.

A sufficient wire is intentionally coarse:

```json
{
  "status": "waiting|ready|unavailable",
  "active_workspace_id": null,
  "poll_after_ms": 1000
}
```

`active_workspace_id` is populated only for `ready`. The public response should
not include root/name/registry, controller instance, state or grant generation,
operation ID/phase/error, target identity, or individual API/Worker process
states. `waiting` means the entry Dashboard is usable but no business runtime is
granted. `unavailable` collapses switching, rollback, recovery, and dependency
failure into a non-sensitive state that keeps the business tree closed.

Implementation invariants:

1. Derive the public projection from the same server-owned readiness predicate
   used by `StateBackend.ReadyBackend`. The predicate should require effective
   Active Workspace, available root identity, matching ready API and Worker, and
   no blocking operation. Do not duplicate the frontend
   `effectiveWorkspaceFromState` decision in a second Go handler.
2. Keep per-business-request revalidation in `proxyBusiness`. A public `ready`
   response is only discovery, never an authorization or durable lease. If a
   switch starts between discovery and a business request, the proxy must still
   return `503` (`internal/hostcontroller/http.go:384`).
3. Require the exact configured Host on the anonymous runtime endpoint, emit
   `Cache-Control: no-store`, do not emit CORS headers, and map Store errors to a
   generic problem without `Fault.OperationID` or internal text.
4. Use a dedicated response struct, not embedding or serializing `State`. This
   prevents a future field added to the privileged control projection from
   becoming anonymously visible.
5. Keep all mutating route registration and `authorizeUnsafe` unchanged. The new
   read type should depend on a read-only state interface and must not expose a
   generic Docker/Compose action.
6. Treat Controller and business sessions independently in the client. An
   expired Controller Cookie may disable `/workspace` controls, but it should not
   log out or unmount an otherwise authorized business Dashboard. Existing tests
   already prove a Controller `401` does not clear business CSRF
   (`web/src/app/host-control-context.test.tsx:161`).
7. Poll at the bounded server-provided interval and abort/clear the Workspace
   business tree as soon as the public projection is not ready. Expected
   non-ready states can return `200`; a failed authoritative state read should
   return a generic retryable `503` rather than pretending there is simply no
   Workspace.

Recommended state matrix:

| Authoritative state | Public projection | Workspace ID | Business proxy |
| --- | --- | --- | --- |
| exact Active + fresh matching API/Worker + no blocking operation | `ready` | current opaque UUID | open, still subject to business Auth |
| no Active Workspace and no operation | `waiting` | `null` | `503 RUNTIME_NOT_READY` |
| switch/quiesce/revoke/prepare/verify/rollback/recovery | `unavailable` | `null` | `503 RUNTIME_NOT_READY` |
| recovery failed / zero Active | `unavailable` | `null` | `503 RUNTIME_NOT_READY` |
| snapshot/database error | generic retryable `503` | absent | `503` |

This is a derived read model, not a new authority. The durable Workspace control
snapshot remains the sole source of truth.

### Alternative designs

#### Option A: no backend contract change

Render only the no-Workspace entry Dashboard outside `HostControlProvider` and
require a Controller Session before showing any connected Dashboard. This is the
smallest implementation, and raw `GET /dashboard` already supports it, but it
does **not** fully achieve credential separation: a ready business runtime still
cannot be discovered or used after the Controller Session expires. Use this only
if acceptance is explicitly limited to a static/disconnected landing view.

#### Option B: ready business API exposes its immutable grant ID

Add a narrow endpoint such as `GET /api/v1/runtime-context`, backed by the API
process's verified immutable `rootgrant.ProcessGrant.WorkspaceID`. The Host
Controller already refuses to proxy any API request unless its durable state is
ready. This option can require business authentication and therefore exposes no
anonymous Workspace ID at all (`internal/platform/rootgrant/grant.go:50`,
`internal/workspace/runtimegrant/composition.go:15`).

Advantages: the Controller full state remains completely closed; a connected
Dashboard depends only on business identity. Disadvantages: it cannot answer
while there is no runtime, needs a static-entry/error bootstrap path, touches
`cmd/api`, `internal/app`, OpenAPI, and direct-mode semantics, and requires a
larger frontend provider reordering. Do not overload the existing public
`/api/v1/system/status`; its exact wire and strict decoder are already shared
contracts (`internal/app/router.go:285`, `web/src/api/system-status.ts:142`).

#### Option C: optional redaction on `/control/v1/state`

Rejected. One URL would have two schemas and two cache/auth meanings. It risks
host path or operation leakage, weakens the existing `401` regression, and makes
the mutation ETag/state-version contract ambiguous.

#### Option D: automatic/read-only Controller Session

Rejected unless a later requirement needs authenticated-but-nonmutating control
metadata. `controlSession` currently has no scope model
(`internal/hostcontroller/session.go:29`). Adding Session privilege levels only
to serve a public Dashboard adds credential lifecycle complexity while still
requiring a separate redacted read model.

### Compatibility and security risks

- **Readiness drift:** the public projection, frontend mount rule, and proxy gate
  can diverge. A single server-side effective-readiness predicate plus a matrix
  test is required. Browser localStorage remains only a hint, never authority.
- **Host path leak:** any reuse of `State`, `Workspace`, `Operation`, or generic
  Controller faults can disclose root paths or operation identity. Exact-key
  response tests should use canary host paths and target names.
- **DNS rebinding / Host confusion:** `ServeHTTP` currently applies exact Host
  checks inside control handlers, but the static and business-proxy branches do
  not reject an alternate Host (`internal/hostcontroller/http.go:76`). A new
  anonymous state endpoint must check Host explicitly. Global Host enforcement
  is a reasonable hardening follow-up, but it changes compatibility for
  `localhost:<port>` and should be tested separately; the launcher currently
  publishes only `127.0.0.1:<port>`.
- **Cross-site polling/DoS:** a credential-free loopback GET can be triggered by
  another site even if the same-origin policy prevents reading the body. Keep the
  response cheap, no-store, bounded, and consider coalescing short-interval
  snapshot reads if browser polling becomes a measurable database load. Do not
  add permissive CORS.
- **TOCTOU during switch:** a ready discovery response can become stale
  immediately. It must never bypass `ReadyBackend`; existing API quiescence and
  runtime revocation still own in-flight work (`cmd/api/workspace_runtime_gate.go:24`).
- **Existing long-lived SSE:** the proxy readiness check runs when the stream is
  opened. The public state poll must still unmount/abort the business tree during
  a switch; the old API container revocation closes the stream. Do not treat the
  public Workspace ID as permission to retain the old Event Store.
- **Business auth confusion:** an anonymous runtime projection is not a business
  login. Required-mode domain APIs must continue returning `401` without the
  independent business Session/API Token.
- **Cookie-dependent response:** if the new route lives under `/control/`, the
  browser will attach the Controller Cookie because of its Path. The response
  must not become richer when that Cookie happens to be present; full detail
  stays on `/control/v1/state`.
- **State-version exposure:** do not reuse the privileged strong ETag or publish
  `state_version`. Public polling does not need the mutation concurrency token.
- **Launcher behavior:** `./zhixu up` currently prints
  `http://127.0.0.1:<port>/#control=<token>`, and `status` prints the bare root
  URL (`zhixu:370`, `zhixu:455`). If product entry moves to `/dashboard`, update
  the launcher contract expectations while preserving one-time fragment exchange
  for privileged controls (`deploy/launcher-contract.sh:80`).
- **Spec ownership:** current specs and comments say the authenticated
  `/control/v1/state` is the only browser bootstrap source
  (`.trellis/spec/backend/workspace-root-grant.md:49`,
  `internal/hostcontroller/types.go:76`,
  `.trellis/spec/frontend/state-management.md:343`). An implementation must
  update this wording to distinguish the authoritative full control state from a
  minimal derived public runtime-access projection; it must not create a second
  state machine.

### Tests required for an implementation

Backend-focused tests:

1. Add a Host `Handler` test for exact anonymous `GET /dashboard` returning the
   SPA without invoking Store/backend or requiring any Cookie. The existing
   webassets test proves only a generic nested fallback
   (`internal/webassets/handler_test.go:11`).
2. Table-test the public runtime response for ready, waiting, switching,
   rollback/recovery, recovery-failed, missing Active, unavailable Active, and
   Store error. Assert exact keys, `no-store`, UUID only in ready, and no canary
   root/name/operation/state-version/controller-instance leakage.
3. Assert wrong Host is rejected and POST/DELETE on the public read route are
   stable `405` with zero Store mutation calls.
4. Retain and strengthen `TestControlStateRequiresSessionWithoutPathLeak`; full
   `/control/v1/state` must still be `401` anonymously and expose the path only
   after a valid Session (`internal/hostcontroller/http_test.go:119`).
5. Assert every existing mutation remains `401` without a Controller Cookie and
   that missing Origin/CSRF/`If-Match`/idempotency stops before Store invocation.
6. Add a shared-readiness matrix proving the public projection and
   `StateBackend.ReadyBackend` agree. There is currently no direct unit test for
   `StateBackend` (`internal/hostcontroller/types.go:197`).
7. Retain proxy credential tests: Controller credentials stripped, business
   Cookie/Bearer preserved, Controller `Set-Cookie` filtered
   (`internal/hostcontroller/http_test.go:316`).
8. Cross-layer: in required business-auth mode, public Dashboard/runtime
   discovery must not make anonymous domain reads succeed. Existing router tests
   provide the baseline (`internal/app/router_test.go:470`).
9. Frontend/App tests should cover bare `/dashboard` with no fragment/Cookie,
   Controller `401` while public runtime is ready, non-ready unmount/cache/SSE
   cleanup, `/workspace` still requiring a Controller Session, and separate
   business-auth login.
10. Add a real Host Controller browser smoke. The spec requires one, but no
    Host Controller-specific Playwright test/Make target was found; current
    browser smokes exercise API/Vite directly.

Baseline read-only verification run during this research:

```text
go test ./internal/hostcontroller ./internal/webassets ./cmd/hostcontroller
ok (all three packages)
```

## Files found

- `internal/hostcontroller/http.go` - top-level route dispatch, control routes,
  Controller authentication/authorization, reverse proxy, and credential
  redaction.
- `internal/hostcontroller/session.go` - one-time Bootstrap token and in-process
  Controller Cookie Session authority.
- `internal/hostcontroller/types.go` - full control wire types, Store interfaces,
  and ready-only backend locator.
- `internal/hostcontroller/coordinator.go` - durable control snapshot projection,
  runtime-readiness calculation, and mutation coordination.
- `cmd/hostcontroller/main.go` - exact IPv4 loopback listener and composition of
  Session authority, Store, backend locator, and static assets.
- `internal/webassets/handler.go` - static file and SPA history fallback used by
  `/dashboard`.
- `internal/hostcontroller/http_test.go` - current control authentication,
  sensitive-path, mutation, proxy readiness, and credential-isolation tests.
- `internal/webassets/handler_test.go` - current SPA fallback and traversal tests.
- `internal/hostcontroller/session_test.go` - one-time exchange, expiry, and
  concurrent exchange tests.
- `web/src/app/App.tsx` - current Controller Session gate that blocks the
  Dashboard/business tree.
- `web/src/app/host-control-context.tsx` - Controller Session/state polling and
  effective Workspace derivation.
- `web/src/api/controller.ts` - strict full-state transport and decoder contract.
- `web/src/features/business/DashboardPage.tsx` - already implemented no-Workspace
  entry Dashboard and Workspace-bound data queries.
- `web/src/app/auth-context.tsx` - independent business-auth state owner.
- `internal/app/router.go` - business API open/protected route split.
- `internal/platform/rootgrant/grant.go` - immutable API/Worker process grant,
  relevant to alternative B.
- `internal/workspace/runtimegrant/composition.go` - verified managed runtime
  composition and grant ownership, relevant to alternative B.
- `zhixu` - Host Controller startup URL, one-time fragment generation, and
  liveness polling.
- `deploy/launcher-contract.sh` - launcher URL/token and zero-grant regression
  contract.

## Code patterns

- Explicit prefix dispatch keeps control, business API, and static paths
  separate; API-like typos do not fall through to the SPA
  (`internal/hostcontroller/http.go:76`).
- Read authentication is Cookie + exact Host; unsafe authorization adds exact
  Origin, constant-time CSRF, idempotency key, and strong state-version ETag
  (`internal/hostcontroller/http.go:306`, `internal/hostcontroller/http.go:325`).
- The reverse proxy resolves a backend per request only after authoritative
  readiness and strips only Controller credentials
  (`internal/hostcontroller/http.go:384`, `internal/hostcontroller/http.go:554`).
- Full Controller state is a deliberately rich authenticated projection; state
  projection is centralized in `projectState`
  (`internal/hostcontroller/coordinator.go:1002`).
- Browser decoders use exact-key validation, so new wire contracts should use a
  distinct decoder rather than silently widening an old shape
  (`web/src/api/controller.ts:111`).
- Controller and business CSRF/session state are intentionally separate
  (`web/src/api/controller.test.ts:85`).

## External references

- Toolchain/package versions verified from the repository: Go `1.25.4` and
  `github.com/go-chi/chi/v5 v5.3.1` (`go.mod:3`, `go.mod:6`).
- No external network reference was required to establish the current behavior;
  all security and compatibility conclusions above are grounded in repository
  code, tests, ADRs, and Trellis specs.

## Related specs

- `.trellis/spec/backend/workspace-root-grant.md:7` - required contract for Host
  Controller, exact root grants, control credentials, readiness, and tests.
- `.trellis/spec/backend/auth-security.md:18` - independent business Session/API
  Token and capability boundary.
- `.trellis/spec/frontend/state-management.md:325` - current Controller state,
  cache, polling, and provider ownership.
- `.trellis/spec/frontend/component-guidelines.md:167` - public/no-Workspace
  Dashboard behavior and prohibition on business queries in entry mode.
- `.trellis/spec/guides/cross-layer-thinking-guide.md:12` - map and own each
  cross-layer contract rather than duplicating state logic.
- `docs/architecture/adr/0017-exact-workspace-root-grant.md:17` - accepted Host
  Controller trust boundary and one-time control-link design.

## Caveats / Not Found

- The task PRD is still `TBD`, so “accessible” is not yet defined as static entry
  only versus a fully connected Dashboard with independent business auth. This
  materially determines whether option A is sufficient or a runtime-discovery
  contract is required.
- No Host Controller API OpenAPI document was found. Controller wire ownership is
  currently split across Go types/tests, TypeScript strict decoders/tests, and
  Trellis specs.
- No Host Controller-specific real Docker + Playwright smoke or Make target was
  found, although the Workspace root-grant spec requires it.
- The current top-level static and business proxy branches do not enforce
  `ExpectedHost`; only control handlers do. This research did not attempt a live
  DNS-rebinding exploit, so the practical exposure remains an unverified risk,
  not a demonstrated incident.
- No product code was modified and no running Docker/browser stack was exercised.
