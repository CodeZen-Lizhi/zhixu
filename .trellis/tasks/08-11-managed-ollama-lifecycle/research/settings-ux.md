# Research: Settings UX and API wire for managed Ollama

- Query: Inspect the current Settings model UI/API wire and provider model; define the minimal changes for explicit local Ollama Chat/Embedding selection, managed-runtime status, desired/active/applied semantics, test/apply behavior, errors, accessibility, and tests; identify conflicts with the in-progress hot-activation implementation.
- Scope: mixed (repository, current task artifacts, and official Ollama documentation)
- Date: 2026-08-11

## Findings

### Files found

- `.trellis/spec/backend/model-settings-runtime.md` - authoritative desired/active/applied, hot-activation, security, and connection-test contracts.
- `.trellis/spec/frontend/model-settings.md` - authoritative strict wire, save/apply, polling, Secret, accessibility, and browser requirements.
- `.trellis/spec/frontend/quality-guidelines.md` - model-settings frontend quality gate and exact invalid-state expectations.
- `.trellis/tasks/08-11-managed-ollama-lifecycle/prd.md` - managed Ollama requirements and acceptance criteria.
- `.trellis/tasks/08-11-managed-ollama-lifecycle/research/runtime-and-official-docs.md` - read-only evidence about the current unmanaged Ollama container and official lifecycle APIs.
- `.trellis/tasks/08-11-model-runtime-hot-activation/design.md` - the in-progress durable activation protocol that this task must extend, not replace.
- `internal/modelsettings/domain/settings.go` - persisted provider unions and structural settings validation.
- `internal/modelsettings/domain/rollout.go` - Snapshot, API/Worker runtime, rollout, participant, and capability types.
- `internal/modelsettings/application/service.go` - save/test flow and exact production-adapter connection probe.
- `internal/modelsettings/runtime/models.go` - settings-to-factory overlay, fixed Relay validation, and production probe construction.
- `internal/modelsettings/http/handler.go` - exact HTTP DTOs, activation/test handlers, Snapshot projection, and Problem mapping.
- `internal/modelsettings/adapter/postgres/{revision,snapshot,audit}.go` - immutable revision loading, Secret AAD/validation, capability derivation, and audit provider validation.
- `migrations/00064_model_settings.sql` - current provider/Secret constraints and append-only revision trigger.
- `api/openapi/{openapi.json,check.mjs}` - exact provider, Snapshot, test, and Problem schema gates.
- `web/src/api/model-settings.ts` - sole frontend wire owner, strict decoder/encoder, provider unions, and Relay rules.
- `web/src/features/settings/ModelSettingsPanel.tsx` and `model-settings.css` - current UX, polling, focus, responsive layout, and form behavior.
- `web/src/api/model-settings.test.ts`, `web/src/features/settings/ModelSettingsPanel.test.tsx`, and `web/e2e/model-settings-hot-activation.smoke.spec.ts` - current contract, component, and real-stack coverage.

### Current contract and modeling

1. The current state semantics are already correct and must remain the outer protocol:

   - `desired` is the last saved immutable revision, `active` is the globally committed default, and each API/Worker `applied_revision` is the revision installed in that process (`.trellis/spec/backend/model-settings-runtime.md:28`, `.trellis/tasks/08-11-model-runtime-hot-activation/design.md:41`).
   - Save advances only `desired`; activation prepares both roles, commits `active` once, then acknowledges each role's `applied` revision (`.trellis/spec/backend/model-settings-runtime.md:28`, `.trellis/tasks/08-11-model-runtime-hot-activation/design.md:180`, `.trellis/tasks/08-11-model-runtime-hot-activation/design.md:193`, `.trellis/tasks/08-11-model-runtime-hot-activation/design.md:198`).
   - The current panel separately shows desired, active, API applied, and Worker applied (`web/src/features/settings/ModelSettingsPanel.tsx:628`). Its success path waits for the authoritative Snapshot instead of treating activation `202` as success (`web/src/features/settings/ModelSettingsPanel.tsx:377`, `web/src/features/settings/ModelSettingsPanel.tsx:517`).

2. Chat local intent is implicit, while Embedding local intent is explicit:

   - Chat accepts only `disabled|openai-compatible` in the domain and frontend (`internal/modelsettings/domain/settings.go:19`, `web/src/api/model-settings.ts:10`). The managed endpoint validator special-cases an `openai-compatible` Chat URL equal to `http://127.0.0.1:11434` (`internal/modelsettings/runtime/models.go:316`).
   - Embedding has an explicit `ollama` provider in the domain and frontend (`internal/modelsettings/domain/settings.go:35`, `web/src/api/model-settings.ts:12`). The UI selects it directly, fixes the Relay, clears the Secret, and disables endpoint editing (`web/src/features/settings/ModelSettingsPanel.tsx:663`, `web/src/features/settings/ModelSettingsPanel.tsx:664`, `web/src/features/settings/ModelSettingsPanel.tsx:669`).
   - The Chat UI still exposes only Disabled/OpenAI-compatible and leaves the URL and API Key controls visible for the implicit-local case (`web/src/features/settings/ModelSettingsPanel.tsx:646`, `web/src/features/settings/ModelSettingsPanel.tsx:648`, `web/src/features/settings/ModelSettingsPanel.tsx:651`). This does not meet PRD R1/R4 (`.trellis/tasks/08-11-managed-ollama-lifecycle/prd.md:20`, `.trellis/tasks/08-11-managed-ollama-lifecycle/prd.md:39`).

3. The existing provider constraint is repeated at every boundary, so the change must be atomic:

   - PostgreSQL currently allows Chat only `disabled|openai-compatible`, and prevents a Secret only for disabled Chat; Embedding already allows Ollama and forbids its Secret (`migrations/00064_model_settings.sql:35`, `migrations/00064_model_settings.sql:69`).
   - Audit validation also accepts only the two Chat providers and assumes any configured Chat key belongs to OpenAI-compatible (`internal/modelsettings/adapter/postgres/audit.go:122`).
   - OpenAPI's checker explicitly fails unless Chat summary/draft/test remain the two-value union (`api/openapi/check.mjs:1837`, `api/openapi/check.mjs:1977`, `api/openapi/check.mjs:2008`).
   - The frontend strict decoder and request encoder use exact provider arrays and currently reject Chat Ollama (`web/src/api/model-settings.ts:182`, `web/src/api/model-settings.ts:443`, `web/src/api/model-settings.ts:699`).

4. The runtime factory can reuse the OpenAI-compatible Chat adapter, but an explicit persisted Ollama value cannot simply be passed through today:

   - `runtime.overlay` casts the domain Chat provider directly to the platform config provider (`internal/modelsettings/runtime/models.go:197`, `internal/modelsettings/runtime/models.go:204`).
   - Platform config/factory accept only Disabled/OpenAI-compatible (`internal/platform/config/config.go:899`, `internal/platform/models/chat_factory.go:11`).
   - Therefore the minimal implementation is: persist/expose `chat.provider="ollama"`, validate the exact Relay and no Secret in Model Settings, then map that value to the existing OpenAI-compatible adapter at the Model Settings runtime boundary. Do not broaden generic static config to arbitrary local endpoints.

5. The connection-test protocol currently reports one inaccurate path:

   - Ollama Embedding is a native adapter that calls `/api/embed` (`internal/platform/models/embedding_factory.go:24`, `internal/platform/models/embedding_ollama.go:29`).
   - The Settings test result nevertheless reports `/v1/embeddings` for every Embedding provider (`internal/modelsettings/application/service.go:161`), and OpenAPI/client tests lock that value (`api/openapi/check.mjs:1847`, `web/src/api/model-settings.ts:626`).
   - The explicit provider union should correct the success projection: remote OpenAI-compatible Embedding reports `/v1/embeddings`; Ollama Embedding reports `/api/embed`. Chat continues to report `/v1/chat/completions` or `/v1/responses` according to its API style.

6. `POST /test` and `POST /activations` currently cannot satisfy automatic host preparation by themselves:

   - The connection test resolves the draft, builds the production adapters, and immediately probes them inside the API process (`internal/modelsettings/application/service.go:126`, `internal/modelsettings/application/service.go:137`, `internal/modelsettings/application/service.go:147`). The HTTP operation defaults to 35 seconds (`internal/modelsettings/http/handler.go:28`).
   - Activation persists the rollout and returns `202`; API/Worker participants then build/probe the target (`internal/modelsettings/http/handler.go:317`, `.trellis/tasks/08-11-model-runtime-hot-activation/design.md:178`).
   - API/Worker are forbidden to receive Docker Socket or host command capability (`.trellis/tasks/08-11-managed-ollama-lifecycle/prd.md:46`). A trusted host reconciler or equally narrow host-owned control port is therefore a hard prerequisite. UI code must never call Docker, infer ownership, or fake readiness.

7. A single local-runtime phase is insufficient unless active serving readiness remains separate:

   - Hot activation deliberately separates serving runtime from candidate participant so preparing a target does not mark the old active runtime unavailable (`.trellis/tasks/08-11-model-runtime-hot-activation/design.md:144`, `.trellis/tasks/08-11-model-runtime-hot-activation/design.md:262`).
   - The same issue occurs for Ollama: while a new local model is preparing, an old active local model may still be serving. Conversely, a local target may be prepared while the active revision is remote. The wire needs both active-local readiness and the current reconciliation operation, not a single ambiguous `phase` badge.

8. Current Snapshot and error decoders will reject naive additions:

   - The frontend requires the exact ten top-level Snapshot keys (`web/src/api/model-settings.ts:537`), mirrored by the OpenAPI checker (`api/openapi/check.mjs:1763`). Adding a runtime field only on the server immediately becomes `INVALID_RESPONSE`.
   - Test Problem details accept only transport/provider stages and a fixed exact field set (`web/src/api/model-settings.ts:731`, `api/openapi/check.mjs:1772`). Host preparation failures cannot be represented safely without extending that union.
   - The UI polls only while the activation rollout is live (`web/src/features/settings/ModelSettingsPanel.tsx:406`). It would stop observing a host-side `starting`, `preparing`, or post-activation `stopping` operation when the rollout is idle.

9. Secret and immutable-history compatibility need explicit handling:

   - Secret AAD binds provider and normalized Base URL (`internal/modelsettings/adapter/postgres/revision.go:337`, `internal/modelsettings/adapter/postgres/revision.go:344`). Changing an implicit-local Chat from `openai-compatible` to `ollama` cannot retain (`keep`) the old ciphertext; Ollama must force `clear`.
   - Existing revision rows are append-only and reject update/delete/truncate (`migrations/00064_model_settings.sql:89`). A forward migration must not rewrite historical implicit-local revisions merely to rename their provider.
   - Compatibility recommendation: all new PUTs require explicit `ollama` for the fixed Relay, while loaders/lifecycle detection retain a narrowly documented legacy exception for already persisted `openai-compatible + exact fixed Relay`. The UI should label such a returned revision as legacy local and convert it only by creating a new explicit revision. Never mutate the old revision or rebind historical Attempts.

### Recommended minimal wire contract

Keep all existing endpoints and the exact activation body. Extend the existing Snapshot instead of creating a second Settings status endpoint:

```json
{
  "desired_revision": 7,
  "active_revision": 6,
  "desired_settings": {
    "chat": {
      "provider": "ollama",
      "api_style": "chat_completions",
      "base_url": "http://127.0.0.1:11434",
      "model": "qwen2.5:3b",
      "model_version": "qwen2.5:3b",
      "adapter_version": "v1",
      "api_key_configured": false
    },
    "embedding": {}
  },
  "active_settings": {},
  "runtime": {},
  "rollout": {},
  "participants": {},
  "ollama_runtime": {
    "desired_required": true,
    "active_required": false,
    "active_ready": false,
    "phase": "preparing",
    "target_revision": 7,
    "fresh": true,
    "last_error_code": null,
    "retryable": false
  },
  "apply_required": true,
  "restart_required": false,
  "capabilities": {}
}
```

The omitted existing nested objects above are unchanged; this is only a shape illustration.

Provider rules:

| Target | Provider | Endpoint | Secret | Protocol result |
|---|---|---|---|---|
| Chat | `disabled` | empty | `clear` | no test |
| Chat | `openai-compatible` | canonical HTTPS only for new PUTs | existing `keep|replace|clear` rules | `/v1/chat/completions` or `/v1/responses` |
| Chat | `ollama` | exact fixed Relay, system supplied | `clear`, `api_key_configured=false` | `/v1/chat/completions`; Responses only after the managed image floor is proven |
| Embedding | `disabled` | empty | `clear` | no test |
| Embedding | `openai-compatible` | canonical HTTPS | Key required as today | `/v1/embeddings` |
| Embedding | `ollama` | exact fixed Relay, system supplied | `clear`, `api_key_configured=false` | `/api/embed` |

`ollama_runtime` semantics:

- `desired_required` and `active_required` are server-owned projections of whether either Chat or Embedding in that exact revision needs managed Ollama. The strict client should cross-check them against the explicit provider unions.
- `active_ready` answers only whether the active revision's required local models are currently serviceable by a fresh, ownership-validated managed instance. It is independent of candidate preparation.
- `phase` is exhaustive: `stopped|starting|preparing|ready|stopping|failed|unavailable`.
- `target_revision` binds activation/reconciliation work to an exact persisted target; it is nullable for idle/stopped and an unsaved connection-test preparation.
- `fresh` is host-controller freshness, not API/Worker freshness.
- `failed` requires a stable `last_error_code`; other phases forbid an error. `retryable` belongs to this local operation and must not be inferred from the activation badge.
- `stopped` is valid while `desired_required=true` but the user has only saved the draft; save alone must not be displayed as started or ready.
- `active_required=true && active_ready=false` makes the corresponding active local capability `unavailable`; it does not rewrite `active` or either process's `applied_revision`.
- `apply_required` remains the hot-activation fact derived from desired/active/live rollout/API+Worker serving health. Do not fold host start/stop progress into it or turn “Apply” into a generic Docker repair button.
- `restart_required` remains exactly `false`.

Recommended strict phase invariants include:

| State | Required invariant |
|---|---|
| `stopped` | `active_required=false`, `active_ready=false`, no target/error, fresh authoritative observation |
| `starting|preparing` | no error; target is exact revision for activation, nullable only for connection test |
| `ready` | managed ownership and Relay probe succeeded; candidate readiness may coexist with an independently true `active_ready` |
| `stopping` | active revision no longer requires Ollama, rollout is idle, and both API/Worker are fresh/applied to active before stop begins |
| `failed` | stable error code present; active readiness remains truthful and is not derived from the failed candidate operation |
| `unavailable` | host controller/observation is stale or absent; never treat last known `ready` as current |

This shape deliberately does not expose Docker IDs, labels, ports, image credentials, filesystem paths, command output, raw provider bodies, or model inventory. Model identity is already present in desired/active settings.

### Test and apply flow

1. Remote connection test is unchanged.
2. Local connection test calls the existing `POST /api/v1/settings/models/test` with an explicit Ollama draft. The application first asks the trusted host boundary to ensure the one managed instance and the target model are ready, then runs the same production adapter probe. It never saves the draft or changes active/applied.
3. While a local test is pending, the query also polls `GET /settings/models` when `ollama_runtime.phase` is transitional, so startup/preparation is visible. Success text must continue to say “draft test passed; not saved or active.”
4. A test-only readiness lease must be released after the probe. If neither active nor a live activation needs Ollama, the reconciler may stop it; otherwise repeated tests would recreate the idle-memory leak.
5. Save-only still advances desired and clears transient Secret values. It does not start Ollama.
6. Save-and-Apply remains `PUT`, then exact `POST /activations {expected_revision}`. During rollout `preparing`, the trusted reconciler ensures the target local models before API/Worker candidate probes can become prepared. A local preparation failure is pre-commit: rollout becomes failed and previous active/applied remain unchanged.
7. Switching away from Ollama does not stop on save, activation start, or one role's acknowledgement. Stop is permitted only after rollout is idle, active no longer requires Ollama, both roles are fresh/applied to active, and no test/old-generation lease needs the instance.
8. Do not add the host reconciler as a third API/Worker rollout participant in the MVP. Treat its readiness as a prerequisite inside existing `preparing`, and expose it through `ollama_runtime`; this avoids weakening the hot-activation two-role commit protocol.
9. Every ensure/start/prepare/stop operation must be idempotent. Frontend recovery remains Snapshot-first after response loss; it never invents a phase locally.

The current 35-second synchronous test contract is only plausible if MVP starts the instance and validates already-present models. Automatic model pulls may exceed this bound and have progress. If the product chooses auto-pull, use a durable asynchronous test/preparation operation (`202` plus operation identity/polling) rather than silently extending one HTTP request indefinitely. This decision is blocked by PRD Open Question 1 (`.trellis/tasks/08-11-managed-ollama-lifecycle/prd.md:72`).

### Error contract

Keep provider/transport diagnostics separate from host lifecycle errors. Extend test `details.stage` with `local_runtime`; do not place Docker/CLI text in `provider_message` or `transport_error`.

Recommended stable local codes:

| Code | Meaning | Retryability guidance |
|---|---|---|
| `MODEL_OLLAMA_CONTROLLER_UNAVAILABLE` | trusted host reconciler absent/stale | retryable after controller recovery |
| `MODEL_OLLAMA_OWNERSHIP_CONFLICT` | exact-name resource exists but ownership validation fails | not automatically retryable |
| `MODEL_OLLAMA_PORT_CONFLICT` | host `11434` is occupied by an unowned/unexpected resource | not automatically retryable |
| `MODEL_OLLAMA_IMAGE_PULL_FAILED` | managed image could not be obtained | retryable for transient registry/network failures |
| `MODEL_OLLAMA_START_FAILED` | owned container failed to start | classification follows bounded cause, never raw output |
| `MODEL_OLLAMA_NOT_READY` | owned instance did not become ready in time | retryable |
| `MODEL_OLLAMA_MODEL_MISSING` | required model absent when auto-pull is disabled | not automatically retryable; user/environment action required |
| `MODEL_OLLAMA_MODEL_PREPARE_FAILED` | model validation/pull failed | cause-dependent stable retryability |
| `MODEL_OLLAMA_STOP_FAILED` | post-commit stop failed; active remote settings remain valid | retryable reconciliation, never activation rollback |

Provider HTTP/protocol failures remain existing Chat/Embedding adapter codes with `provider_response|response_read|response_validation`. A missing model detected through the host preflight is `MODEL_OLLAMA_MODEL_MISSING`, not a generic connect failure. Immediate activation errors and asynchronous runtime failures must render error code plus retryability; the current immediate activation feedback shows only the message (`web/src/features/settings/ModelSettingsPanel.tsx:680`).

The current hot-activation wire forces global `rollout.failed.retryable=true` (`web/src/api/model-settings.ts:584`, `.trellis/spec/frontend/model-settings.md:37`), and the handler sets it true for every failed rollout (`internal/modelsettings/http/handler.go:781`). Do not encode a non-retryable ownership/port fact by violating that existing union. For the minimal change, preserve “the activation may be reissued after remediation” at rollout level and expose the precise local cause/retryability in `ollama_runtime`. A later change may add persisted rollout retryability, but it is not required for this lifecycle UX.

### UX and accessibility

- Add `本地 Ollama` as an explicit option in both provider selects. Selecting it atomically fixes the Relay and clears the Secret; switching back to remote requires HTTPS and a valid Secret action.
- For Ollama, do not render an editable/disabled URL input or API Key radio group. Render a read-only product fact such as `运行位置: 本机（系统管理）`; keep the fixed Relay only in the wire. Active summary should likewise avoid making users interpret `127.0.0.1`.
- Keep desired/active/API applied/Worker applied as the top status strip. Add one unframed `本地 Ollama` status section below it; do not label its target revision as a third “applied” revision.
- The local section should separately say whether desired needs local, whether active needs local, whether active local service is ready, and what operation is in progress. This prevents “preparing target” from hiding “old active still serves.”
- Transitional runtime updates use one polite live region; failures use `role="alert"`. Background polling must not steal focus on every phase change. Focus the feedback/status heading only after the user's test/apply action fails, following the existing focused feedback pattern (`web/src/features/settings/ModelSettingsPanel.tsx:561`, `web/src/features/settings/ModelSettingsPanel.tsx:676`).
- Use textual phase/error/retry labels in addition to badge tone/icon. Existing component guidance requires native semantics, visible focus, associated labels, and non-color status (`.trellis/spec/frontend/component-guidelines.md:39`).
- Preserve native `label/select/button`, `aria-expanded/aria-controls` for the collapsible sections, stable 44px controls, and visible keyboard focus. Add `aria-busy` to the affected status/form region while test/apply preparation is pending.
- Long model names and error codes must wrap. Existing CSS already uses `min-width:0`, `overflow-wrap:anywhere`, desktop-to-single-column reflow, and a 390px-friendly breakpoint (`web/src/features/settings/model-settings.css:34`, `web/src/features/settings/model-settings.css:167`, `web/src/features/settings/model-settings.css:408`). Extend those patterns rather than introducing nested cards.
- Test at 1440x900 and 390x844 with no horizontal overflow, overlap, console warning/error, or Secret/Endpoint/instance leakage, as already required (`.trellis/spec/frontend/model-settings.md:39`, `.trellis/spec/frontend/model-settings.md:74`).

### Conflicts with the in-progress hot-activation worktree

1. **Exact Snapshot shape:** current backend DTO, OpenAPI checker, frontend decoder, fixtures, and Playwright helpers all assume no `ollama_runtime` field (`internal/modelsettings/http/handler.go:160`, `api/openapi/check.mjs:1769`, `web/src/api/model-settings.ts:539`, `web/e2e/model-settings-hot-activation.smoke.spec.ts:36`). These must land atomically.
2. **Provider union and factory cast:** adding Chat Ollama only in UI/domain will be rejected by PostgreSQL/OpenAPI/audit/platform config/factory. The runtime mapping seam must prevent the persisted intent from leaking as an unsupported generic config provider.
3. **Append-only history:** rewriting old implicit-local rows conflicts with immutable revisions and frozen workflow provenance. Use forward constraints plus a legacy read/load exception, not an update migration.
4. **Two-role commit:** changing the participant union to add a host role would touch the `00079` state machine, locks, coordinator, strict frontend union, and recovery proofs. Keep host preparation as a prerequisite during `preparing` for the minimal design.
5. **Old-serving vs candidate state:** reusing `runtime.api/worker` or participant fields for Docker status would violate the deliberate serving/candidate split. Keep `ollama_runtime` independent.
6. **Polling termination:** current polling stops when rollout is idle/failed. It must also observe local transitional phases, especially post-commit `stopping`.
7. **Capability derivation:** backend and strict client currently derive capability only from active provider and API/Worker readiness (`internal/modelsettings/adapter/postgres/snapshot.go:70`, `web/src/api/model-settings.ts:605`). Extend capability validation for active Ollama readiness without changing desired/active/applied values.
8. **Failed retryability:** current rollout failed is always retryable. Put precise local retryability in the new runtime projection rather than emitting a wire combination the current client rejects.
9. **Connection-test path union:** adding `/api/embed`, Chat Ollama, or `stage=local_runtime` requires synchronized Handler result validation, OpenAPI, checker, strict client, fixtures, and component labels.
10. **Legacy local Secret:** provider-bound AAD makes `keep` invalid when converting implicit local Chat to explicit Ollama. Both UI and backend must force `clear`; otherwise activation will fail later or the new revision will retain an invalid Secret.

These are current-working-tree conflicts identified by reading the files directly. This research role forbids git operations, so it does not classify individual lines as staged, unstaged, or committed.

### Tests required

Backend/domain/migration:

- Chat provider domain/structural tests for `ollama`, exact Relay, API style constraint, and no Secret.
- New forward migration tests that allow Chat Ollama for new rows, preserve old rows, keep append-only triggers, and reject Ollama Chat Secret envelopes.
- Audit/revision/Secret AAD tests for Ollama Chat and explicit legacy conversion with `clear`; reject `keep|replace` where invalid.
- Runtime overlay test proving Chat Ollama maps only to the existing OpenAI-compatible adapter while its persisted/audit/test provider remains `ollama`.
- `RequiresManagedOllama` truth table: neither, Chat only, Embedding only, both, and legacy exact-Relay Chat.
- Snapshot invariant tests for every local phase, freshness, target binding, desired/active requirement, active readiness, error/retry shape, and capability derivation.
- Connection-test ordering: host ensure before provider probe; preparation failure makes zero provider calls and changes no desired/active/applied state.
- Activation ordering: online-to-local waits for host readiness before participants prepare; failure stays pre-commit with previous active/applied. Local-to-online does not stop on save or partial acknowledgement and stops only after full convergence.
- Idempotency/race tests for repeated test/apply/reconcile and Chat/Embedding sharing one instance without mutual stop.

HTTP/OpenAPI/frontend API:

- Exact Chat/Embedding provider unions, branch-specific endpoint/Secret rules, legacy response compatibility, and request rejection for new implicit-local drafts.
- Exact `ollama_runtime` decoder with unknown/missing field, illegal phase/error, stale-ready, target/revision, desired/active cross-binding, and capability contradictions.
- Test success binding for Chat Ollama and Embedding Ollama `/api/embed`.
- Test Problem `stage=local_runtime`, error-code/retryability mapping, and rejection of Docker/CLI/Secret/Endpoint fields.
- Existing activation exact `{expected_revision}`, no-store, Session/Origin/CSRF, response-loss recovery, and `restart_required=false` remain locked.
- OpenAPI checker updates must assert the entire new discriminated union, not merely add an optional object.

Component/browser:

- Explicit local selection for Chat and Embedding; fixed system endpoint is not editable; Key is absent/cleared; remote mode restores its required fields.
- Status matrix: desired local/active remote stopped; local target starting/preparing; active old local remains ready during target preparation; failed local preparation; post-apply stopping; stopped online state; stale controller unavailable.
- Test pending/success/failure remains distinct from save/apply; test success never says saved or active.
- Save-only never displays started/ready as a consequence of save. Save-and-Apply preserves exact PUT->activation revision ordering.
- Polling continues for local transition with idle rollout and stops at a terminal local phase; focus/reconnect refetch remains authoritative.
- Error code, retryability, remediation text, keyboard operation, visible focus, live-region behavior, no focus theft, and long-text wrapping.
- Real browser/Compose smoke covers Chat-only local, Embedding-only local, both local sharing one instance, and switch to all-online. Verify API/Worker container identity is unchanged, managed Ollama ownership is exact, stop preserves the model volume, and desktop/mobile have zero overflow/console errors/Secret leakage.

Current tests already provide a strong base for desired/active/applied, save-only, exact activation, response loss, failed recovery, focus, and mobile overflow (`web/src/features/settings/ModelSettingsPanel.test.tsx:185`, `web/src/features/settings/ModelSettingsPanel.test.tsx:400`, `web/src/features/settings/ModelSettingsPanel.test.tsx:441`, `web/src/features/settings/ModelSettingsPanel.test.tsx:470`, `web/e2e/model-settings-hot-activation.smoke.spec.ts:148`). Extend them; do not replace those proofs with local-runtime mocks.

### External references

- Ollama official OpenAI compatibility documentation, accessed 2026-08-11: <https://docs.ollama.com/api/openai-compatibility>. It documents `/v1/chat/completions`, `/v1/embeddings`, and `/v1/responses`, and states that Responses support was added in Ollama `v0.13.3`.
- The current independently created container is `ollama/ollama:0.9.6`, so its existence does not prove Responses compatibility (`.trellis/tasks/08-11-managed-ollama-lifecycle/research/runtime-and-official-docs.md:11`). Until the managed image is pinned to `>=0.13.3` and the repository's structured Responses payload is fixture-tested, the minimal safe Ollama Chat UI should force `chat_completions`.
- Official Ollama docs also require a model to be pulled before use; the task's existing research identifies `/api/tags`, `/api/show`, and `/api/pull` as the relevant model-presence/preparation APIs (`.trellis/tasks/08-11-managed-ollama-lifecycle/research/runtime-and-official-docs.md:26`).

### Related specs

- Backend hot-activation and local Docker boundary: `.trellis/spec/backend/model-settings-runtime.md:15`, `.trellis/spec/backend/model-settings-runtime.md:28`, `.trellis/spec/backend/model-settings-runtime.md:45`, `.trellis/spec/backend/model-settings-runtime.md:55`.
- Frontend strict wire and UX state ownership: `.trellis/spec/frontend/model-settings.md:13`, `.trellis/spec/frontend/model-settings.md:23`, `.trellis/spec/frontend/model-settings.md:41`, `.trellis/spec/frontend/model-settings.md:66`.
- Frontend model-settings quality gate: `.trellis/spec/frontend/quality-guidelines.md:105`.
- Cross-layer single wire-owner rule: `.trellis/spec/guides/cross-layer-thinking-guide.md:28`.
- Managed Ollama PRD R1-R5 and AC1-AC10: `.trellis/tasks/08-11-managed-ollama-lifecycle/prd.md:18`, `.trellis/tasks/08-11-managed-ollama-lifecycle/prd.md:52`.

## Caveats / Not Found

- PRD Open Question 1 is unresolved: auto-pull versus “start and report missing model.” This determines whether the existing synchronous test endpoint is sufficient or an asynchronous preparation resource is required.
- The managed Ollama image/version is not yet selected. Responses API support must not be inferred from current Ollama documentation while the observed legacy container remains `0.9.6`.
- The trusted host reconciler mechanism and persistence schema are outside this UX/wire research. The proposed Snapshot shape assumes it can publish a freshness-bound, ownership-validated projection without exposing Docker access to API/Worker.
- A test-only runtime retention/grace period is not specified. Correctness requires a bounded lease and eventual stop when active/activation no longer needs Ollama; the exact grace duration is a product/operations choice.
- Legacy implicit-local Chat revisions may exist in other installations even though the current captured desired/active state does not use them. A compatibility policy must be explicit before migration implementation.
- This research did not edit product code, planning artifacts, specs, or git state.
