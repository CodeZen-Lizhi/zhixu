# Research: test-surface

- Query: Inspect front-end unit and browser coverage for AppShell navigation, WorkspacePage onboarding, SystemStatusPage, and responsive UX; recommend the smallest reliable test surface for the planned refinement.
- Scope: internal
- Date: 2026-07-31

## Findings

### Existing coverage

- `web/src/app/AppShell.test.tsx` mounts a real shell with mocked event/workspace/auth hooks. It verifies that the navigation, connection status, authenticated label, and lazy-route fallback stay mounted, but it only asserts a subset of links and does not exercise the mobile Sheet, breadcrumb active state, close behavior, or focus restoration (`web/src/app/AppShell.test.tsx:6-54`).
- `AppShell` owns one navigation tuple used by the desktop rail and mobile Sheet; the mobile trigger is named `打开主导航`, the Sheet title is `工作台导航`, and navigation click closes the Sheet (`web/src/app/AppShell.tsx:10-23`, `web/src/app/AppShell.tsx:57-64`). `Sheet` is a Radix dialog with close-button label `关闭导航` and explicit focus restoration (`web/src/shared/ui.tsx:23`).
- `WorkspacePage` tests already use the right component-test seam: `renderWithAppProviders`, per-test local storage, and a mocked `fetch` response for system status (`web/src/features/workspace/WorkspacePage.test.tsx:35-55`; `web/src/test/render.tsx:8-13`). They cover creation plus the real Git-dirty state and a successful scan table (`web/src/features/workspace/WorkspacePage.test.tsx:57-101`), but not first-visit onboarding structure/CTA or an existing-Workspace entry flow.
- `WorkspacePage` currently renders its onboarding hero, two product links, `SystemStatusPage`, then connection forms/summary and scan action (`web/src/features/workspace/WorkspacePage.tsx:119-190`). Root-route compatibility preserves `/` as Workspace without rewriting the URL (`web/src/routes/AppRoutes.tsx:34-58`; `web/src/routes/AppRoutes.test.tsx:50-56`).
- `SystemStatusPage` has broad behavioral coverage already: loading/ready, several independent degraded capabilities, fail-closed auth, and retry recovery (`web/src/features/system-status/SystemStatusPage.test.tsx:24-217`). It must keep showing actual API-derived state rather than a UX-only approximation: its ready/degraded headline is computed from all capability fields and it retains a retry action (`web/src/features/system-status/SystemStatusPage.tsx:61-134`, `web/src/features/system-status/SystemStatusPage.tsx:136-228`).
- Vitest is configured for `src/**/*.{test,spec}.{ts,tsx}` under jsdom, with Testing Library cleanup after every test (`web/vite.config.ts:29-34`; `web/src/test/setup.ts:1-31`). There is no `@testing-library/user-event` dependency, so existing tests use `fireEvent` (`web/package.json:34-47`; `web/src/features/workspace/WorkspacePage.test.tsx:1`).

### Responsive/browser surface

- Responsive behavior is CSS-only and therefore cannot be validated by jsdom. At `max-width: 920px`, the desktop rail hides and the mobile menu button becomes visible; at `max-width: 720px`, the status grid and Workspace forms change to two/one-column layouts (`web/src/styles.css:89-122`, `web/src/styles.css:418-419`, `web/src/styles.css:945-963`).
- Existing browser smoke tests use a desktop page plus a separate `390x844` context and assert no horizontal overflow using `document.scrollWidth` and `.workbench__content` (`web/e2e/collection-health.smoke.spec.ts:49-60`, `web/e2e/collection-health.smoke.spec.ts:138-177`; `web/e2e/semantic-link-graph.smoke.spec.ts:62-89`, `web/e2e/semantic-link-graph.smoke.spec.ts:131-191`). This is the established responsive test pattern; no screenshot/snapshot convention exists.
- Existing e2e specs target Graph, Collection/Health, Export, Artifact, and M8 Learning; none targets shell navigation or Workspace onboarding (`web/e2e/`). Their fixture setup is real-service oriented: for example Collection/Health requires loopback API/app URLs and three UUID fixtures at module load (`web/e2e/collection-health.smoke.spec.ts:14-30`).
- Playwright has no `webServer`; it requires a base URL, runs Chromium/Chrome serially, and uses a `1440x900` default viewport (`web/playwright.config.ts:8-38`). The package provides only `test:e2e: playwright test` (`web/package.json:10-16`).

### Smallest reliable approach

1. Extend the three existing component test files only for changed user-visible behavior:
   - `AppShell.test.tsx`: assert the revised navigation destination(s) and `aria-current` behavior; for any mobile-navigation change, click `打开主导航`, scope assertions to the dialog named `工作台导航`, then close by route selection or `Escape`/`关闭导航` and assert the dialog closes and focus returns to the trigger. The repository already uses this keyboard/focus style for modal drawers (`web/src/features/graph/GraphPage.test.tsx:668-692`).
   - `WorkspacePage.test.tsx`: add one no-active-Workspace test for the finalized onboarding heading, primary action, and any new deep link. Keep the existing mock status response and creation/scan test; add an existing-Workspace assertion only if that entry path changes.
   - `SystemStatusPage.test.tsx`: retain the existing ready/degraded/error/retry matrix. Add or adjust an assertion only if the refinement changes a semantic control (for example a status-details disclosure); do not add copy-only snapshot tests or duplicate API decoder coverage.
   - Leave `AppRoutes.test.tsx` unchanged unless a route or the `/` Workspace compatibility behavior changes.

2. Add one focused browser smoke only when the change touches CSS breakpoints, mobile Sheet layout, or causes a real overflow risk. It can avoid the database/API/worker fixtures:
   - Run at `/workspace` with empty local storage. With no active Workspace, `EventStoreProvider` exits before establishing SSE (`web/src/events/event-store.tsx:52-58`).
   - Before navigation, intercept `/api/v1/system/status` with one valid `ready` payload whose `auth.status` is `disabled`. That response lets `AuthProvider` proceed without a session (`web/src/app/auth-context.tsx:62-80`) and serves both the auth bootstrap and `SystemStatusPage`.
   - At `1440x900`, check visible rail navigation and the onboarding form. At `390x844`, check the visible menu trigger, Sheet navigation/close flow, Workspace onboarding controls, and `document` plus `.workbench__content` horizontal widths no greater than `window.innerWidth + 1`.
   - Keep this test in `web/e2e/` and invoke its file explicitly. A focused command is `ZHIXU_PLAYWRIGHT_BASE_URL=http://127.0.0.1:<port> npm run test:e2e --prefix web -- e2e/<new-workbench-spec>.ts`; start Vite separately because the config has no `webServer`.

3. For the implementation loop, run the targeted Vitest files first:

```bash
npm run test --prefix web -- src/app/AppShell.test.tsx src/features/workspace/WorkspacePage.test.tsx src/features/system-status/SystemStatusPage.test.tsx
```

Then run the full frontend gate required by the frontend specs:

```bash
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web
npm run build --prefix web
git diff --check
```

### Constraints from specs

- Component tests must cover semantic roles, keyboard, focus, and visible loading/error/empty/status states; dialogs/drawers must restore focus, and status must not rely only on color (`.trellis/spec/frontend/component-guidelines.md:39-48`, `.trellis/spec/frontend/component-guidelines.md:69-70`). The proposed AppShell Sheet test is needed for this contract if navigation changes.
- The quality guide separates Vitest `src/` coverage from Playwright `e2e/` coverage and requires component tests for Status/Form plus a selected Playwright smoke where appropriate (`.trellis/spec/frontend/quality-guidelines.md:43-52`, `.trellis/spec/frontend/quality-guidelines.md:230-238`). A jsdom assertion cannot prove the `920px` or `720px` CSS behavior.
- If the change reaches AppShell's Active Workspace or SSE lifecycle rather than presentation/navigation alone, broaden the scope to `web/src/events/event-store.test.tsx`: the state-management spec explicitly treats App Shell/SSE/workspace-switch edits as a trigger and requires connection/recovery coverage (`.trellis/spec/frontend/state-management.md:129-182`). Avoid incidental changes there for this UX task.
- The current task PRD contains only `TBD` requirements and acceptance criteria (`.trellis/tasks/07-31-workbench-ux-refinement/prd.md:3-17`). Exact wording and which of the conditional tests above are necessary must be selected after the UX acceptance criteria are written.

## Files Found

- `web/src/app/AppShell.tsx` - shared desktop/mobile navigation, breadcrumb, Workspace context, and Sheet state.
- `web/src/app/AppShell.test.tsx` - current shell/lazy-route component coverage.
- `web/src/features/workspace/WorkspacePage.tsx` - onboarding, Workspace creation/opening, status panel, and scan workflow.
- `web/src/features/workspace/WorkspacePage.test.tsx` - mocked fetch/local-storage component tests for create and scan.
- `web/src/features/system-status/SystemStatusPage.tsx` - API-derived status projection and retry action.
- `web/src/features/system-status/SystemStatusPage.test.tsx` - ready/degraded/error/retry test matrix.
- `web/src/routes/AppRoutes.test.tsx` - root/deep-link compatibility coverage.
- `web/src/styles.css` - workbench and Workspace responsive breakpoints.
- `web/e2e/collection-health.smoke.spec.ts` and `web/e2e/semantic-link-graph.smoke.spec.ts` - reusable desktop/mobile overflow and browser-context patterns.
- `web/playwright.config.ts` and `web/package.json` - available test scripts and browser-runner constraints.

## Related Specs

- `.trellis/spec/frontend/index.md`
- `.trellis/spec/frontend/quality-guidelines.md`
- `.trellis/spec/frontend/component-guidelines.md`
- `.trellis/spec/frontend/state-management.md`
- `.trellis/spec/frontend/type-safety.md`

## External References

- None. This research used repository code, configuration, and Trellis specifications only.

## Caveats / Not Found

- No existing e2e test or shared browser fixture targets AppShell/Workspace onboarding specifically.
- The current Playwright suite cannot be run as a whole with only a base URL: several existing specs validate required service fixtures at module load. Run an explicit e2e file, or use the corresponding `deploy/*-browser-smoke.sh` fixture harness for its owned feature.
- The focused browser smoke described above still requires a running Vite server and an installed Chrome/Chromium executable; Playwright defaults to the Chrome channel unless `ZHIXU_PLAYWRIGHT_EXECUTABLE_PATH` is supplied (`web/playwright.config.ts:16-37`).
- No tests were executed because this was read-only test-surface research.
