# Research: Supervisor lifecycle UX, progress, and legacy migration placement

- Query: Review the managed-Ollama PRD and Settings UX after accepting explicit local Ollama, durable automatic model pulls, and a small main-Compose supervisor that starts/stops an `ollama serve` child over a managed volume; define understandable status copy, the no-container-details UI boundary, and whether one-time legacy-volume migration belongs in Settings or the launcher.
- Scope: mixed (current repository/task artifacts plus already-captured official Docker and Ollama documentation)
- Date: 2026-08-11

## Findings

### Files found

- `.trellis/tasks/08-11-managed-ollama-lifecycle/prd.md` - accepted product requirements for the always-present lightweight manager, on-demand heavy process, durable automatic pulls, retention, and Settings observability.
- `.trellis/tasks/08-11-managed-ollama-lifecycle/research/settings-ux.md` - prior provider/wire analysis and the initial `ollama_runtime` projection, written before automatic pull was accepted.
- `.trellis/tasks/08-11-managed-ollama-lifecycle/research/runtime-and-official-docs.md` - observed legacy volume/container facts and official Ollama pull/status API evidence.
- `.trellis/tasks/08-11-managed-ollama-lifecycle/research/docker-lifecycle.md` - launcher ownership, volume retention, migration safety, and official Docker volume semantics; its host-reconciler recommendation is superseded by the now-selected supervisor architecture.
- `web/src/features/settings/ModelSettingsPanel.tsx` - current desired/active/applied strip, provider controls, test feedback, rollout polling, copy, focus, and accessibility behavior.
- `.trellis/spec/frontend/model-settings.md` - strict wire, status ownership, save/apply, polling, accessibility, and browser-test contract.
- `.trellis/spec/frontend/component-guidelines.md` - non-color status, live-region, focus, and disabled-reason requirements.
- `zhixu` - current launcher command surface, exact resource allowlists, read-only status, down retention, and confirmed reset boundary.
- `deploy/compose.yml` - current main Compose services and the only two declared named volumes; no local-model supervisor or managed model volume exists yet.
- `README.md`, `docs/operations.md`, `docs/user-guide.md`, `docs/requirements.md` - user, operator, and product documentation that must converge on the accepted behavior.
- `docs/architecture/adr/0022-model-runtime-hot-activation.md` - accepted hot-activation ADR whose “no new always-on service” clause conflicts with the selected lightweight supervisor.

### Conclusion

The PRD already captures the chosen architecture correctly: the small manager remains in the main Compose project, the heavy local model process is absent when unused, automatic pulls are durable, Save-only has no runtime side effect, and model files survive ordinary stops (`prd.md:30-44`). It also requires distinct runtime/progress feedback (`prd.md:46-51`) and refresh-safe download recovery (`prd.md:61-71`).

The remaining PRD/UX changes are semantic rather than architectural:

1. Treat **manager availability**, **current active-local readiness**, and **target preparation** as separate facts. “The manager is present” must never be rendered as “Ollama is running.”
2. Add an explicit user-visible `pulling`/“正在下载模型” phase with durable progress. The prior flat union only had `preparing` (`settings-ux.md:138-160`) because automatic pull was then unresolved (`settings-ux.md:176`); that caveat is now obsolete.
3. Make the Settings surface speak in product terms—local model, model files, download, ready, memory released—not Docker/Compose/container/volume/Relay terms.
4. Resolve the remaining legacy-volume open question in favor of a **launcher-owned, explicit one-time migration**. Settings may display a generic recovery notice only if migration blocks local-model availability; it must not detect, select, mount, copy, adopt, or delete volumes.

### PRD adjustments recommended

The following should be added or made explicit before design starts:

- Rename the product-facing task/section wording from “自动管理 Ollama 容器” to “自动管理本地 Ollama 运行时.” Technical requirements may still precisely document the supervisor container and child process; the Goal and acceptance language should lead with the user outcome. The current Goal already says users need not understand Compose, Relay, or networking (`prd.md:5-7`).
- Extend R4 with an exhaustive display contract: `未运行`, `正在启动`, `正在下载模型`, `正在检查模型`, `已就绪`, `正在停止`, and `暂不可用/失败`. “由知序自动管理” is a stable explanatory note, not a runtime-success badge.
- State that a fresh manager plus no child process is the normal all-online/disabled state, not degraded. This sharpens the current requirement that the manager remains while the heavy process and loaded models are absent (`prd.md:30-31`, `prd.md:51`).
- State that active readiness and target preparation can coexist. If the current active revision uses local model A while a candidate downloads model B, the UI must say A remains available and B is being prepared; it must not replace the current-ready fact with a single ambiguous “downloading” badge. This follows the hot-activation rule that the previous active remains serving until commit (`docs/architecture/adr/0022-model-runtime-hot-activation.md:62-65`).
- Define download progress as a persisted, monotonic, bounded projection of the current exact model and model-set progress. Page refresh, request loss, and duplicate Test/Apply recover the same operation, as R2/AC11 already require (`prd.md:33-34`, `prd.md:71`). Before the upstream total is known, the UI may be indeterminate; after `total>0`, `0 <= completed <= total` is mandatory and percentage is derived rather than independently stored.
- Resolve Open Question 1 (`prd.md:80-82`) as: support an explicit launcher migration from the exact known legacy source into the new managed volume; never silently adopt/relabel the old volume; preserve the source for rollback; skipping migration remains valid because exact needed models can be downloaded automatically.
- Add acceptance cases for: manager available + local process off is healthy; Save-only local remains off and does not download; active local remains ready while a different candidate pulls; refresh resumes the same progress; Settings DOM contains no infrastructure identifiers; and launcher migration is idempotent, resumable/fail-closed, source-preserving, and absent from ordinary Settings actions.

### Recommended Settings information hierarchy

Keep the existing desired/active/API-applied/Worker-applied strip as the authoritative top-level state (`ModelSettingsPanel.tsx:625-638`; `.trellis/spec/frontend/model-settings.md:23-36`). Add one “本地 Ollama” section below it with two layers:

1. **Current service fact:** whether the active local models are usable now. This answers “Can current work use the local model?”
2. **Current operation:** starting, downloading, checking, or stopping. This answers “What is the system doing next?”

This two-layer presentation is required for a truthful case such as `active_ready=true` plus `operation.phase=pulling`. A single badge may summarize the operation, but it must not hide the current-serving sentence.

The manager itself should normally be implicit. Use the secondary line `由知序自动管理` under the section title. Only manager unavailability becomes a user state. Do not add a green “管理容器运行中” badge: it falsely suggests that the memory-heavy model runtime is active and forces users to understand an implementation layer.

### Recommended user-facing copy

| Internal fact | Primary status | Supporting text | Notes/actions |
|---|---|---|---|
| Manager fresh; local process off; active does not require local | **本地模型未运行** | 当前生效配置不需要本地模型。模型文件已保留，需要时会自动启动。 | Healthy/neutral, not degraded. Stable note: `由知序自动管理`. |
| Manager fresh; desired requires local but user only saved | **本地模型未运行** | 本地配置已保存但尚未应用；仅保存不会启动或下载模型。 | Keep `配置已保存，尚未应用`; Apply remains the explicit action. |
| Starting child process | **正在启动本地模型** | 启动完成后会自动检查并准备所选模型。 | `role="status"`; no Docker troubleshooting text. |
| Pull operation, total not known yet | **正在准备模型下载** | 正在获取模型大小，请稍候。 | Indeterminate progress; do not invent `0%`. |
| Pull operation, total known | **正在下载本地模型** | 正在下载 `{model}`：已完成 `{percent}%`。 | Also show `第 n/m 个模型` when activation needs more than one. The model name is already user-selected; never show layer digest. |
| Model present, validating/probing | **正在检查本地模型** | 下载已完成，正在确认模型可以正常使用。 | This is user wording for verify + production Adapter probe; avoid protocol/endpoint details. |
| Active local models ready, no target operation | **本地模型已就绪** | 当前生效的本地对话/向量模型可以使用。 | Green only in addition to text. |
| Active local model A ready while candidate B pulls | **当前本地模型可用** | 当前生效模型继续服务；正在下载待应用模型 `{model}`（`{percent}%`）。 | This is the critical desired/active separation; do not call the candidate active. |
| Waiting for old generation leases and/or terminating | **正在停止本地模型** | 正在等待进行中的任务安全结束并释放本地模型内存，不会中断已有工作。 | If remote target is already active, it may additionally say `在线配置已生效`. |
| Manager observation absent/stale | **本地模型管理暂不可用** | 在线模型和已关闭的能力不受影响；本地测试或应用需等待服务恢复。 | Offer Refresh; advanced recovery may point to `./zhixu status`, not raw service names. |
| Start/pull/check failed before commit | **本地模型准备失败** | 当前生效配置未改变。请按提示重试，或先检查网络与可用磁盘空间。 | Stable sanitized error code and retryability may appear in details. |
| Stop failed after remote commit | **本地模型尚未停止** | 在线配置已经生效；知序会继续尝试释放本地模型资源。 | Do not claim Apply failed or roll active back. |

Use `本地 Ollama` as the provider option in both selects, replacing the current plain `Ollama` label (`ModelSettingsPanel.tsx:95-99`, `ModelSettingsPanel.tsx:663`). When selected, show `运行位置：本机（由知序管理）`; do not render a disabled Base URL control. The current Embedding form exposes the fixed Relay as an input placeholder and the validation error can print the Relay URL (`ModelSettingsPanel.tsx:257`, `ModelSettingsPanel.tsx:663-669`); both conflict with the accepted product abstraction.

### Durable pull progress and wire implications

The initial `ollama_runtime` proposal separates `active_ready` from a local operation (`settings-ux.md:109-149`), which remains useful. Automatic pull requires the following minimum extension:

- Include `pulling` as an explicit transitional operation phase rather than hiding it inside generic `preparing`.
- Persist an operation identity and reason (`connection_test|activation|reconcile`) so refresh and response-loss recovery can re-associate the progress. Unsaved connection tests cannot rely on `target_revision`, which is nullable in the prior proposal (`settings-ux.md:143`).
- Project only the current user-selected model name, `completed_bytes`, `total_bytes`, `completed_models`, and `total_models`. Do not expose Ollama blob digests, manifest/layer status strings, registry URLs, storage paths, or raw stream content.
- Keep current readiness independent: `active_ready=true` is legal while `phase=pulling` prepares a candidate. `apply_required` and `restart_required=false` retain their existing meanings (`settings-ux.md:146-149`).
- Make operation state authoritative and durable. The current Test UI is tied to one in-flight HTTP mutation (`ModelSettingsPanel.tsx:332-358`) and polling only follows live rollout (`ModelSettingsPanel.tsx:404-413`); both are insufficient for a long, resumable pull. Test/Apply should start or join a durable operation and the page should poll while any local operation is transitional.
- Progress must remain monotonic in the operation projection. A retry is identified by the durable `(operation_id, attempt_no)` pair; it keeps cumulative projected progress rather than silently moving the UI backward.

For accessibility, use a named native `<progress>` element or an equivalent correctly labelled progressbar only after a total is known. Announce phase changes and coarse milestones through one polite live region, not every pull chunk. Failures use `role="alert"`; background polling never steals focus. This follows the existing focus-on-user-action error pattern (`ModelSettingsPanel.tsx:561-563`, `ModelSettingsPanel.tsx:676-683`) and the project requirements for live regions, non-color states, and actionable disabled controls (`.trellis/spec/frontend/component-guidelines.md:30-48`).

### No-container-details interface principle

Settings should expose product intent and outcome, not orchestration topology.

Allowed in the normal Settings UI:

- `本地 Ollama`, `本地模型`, `由知序自动管理`, `模型文件已保留`.
- User-selected model names, download byte/percentage progress, safe model count, current/target revision, and stable product error codes.
- User actions: Test, Save-only, Save-and-Apply, Retry, Refresh.

Forbidden in the normal Settings UI, DOM, toast, or browser storage:

- Docker, Compose, container, supervisor/child-process terminology, service/container/volume names or IDs, labels, image digests, mount/storage paths, shell/CLI output.
- Relay, `127.0.0.1`, `11434`, `host.docker.internal`, internal network names, registry URLs, Ollama blob/layer digests, or raw provider bodies.
- A migration button that causes host resource inspection/copy/deletion.

The boundary is intentionally asymmetric: architecture and operations documents must name the supervisor, child process, managed volume, ownership rules, and rollback mechanics precisely; Settings must not. The launcher is an operator surface and may show technical detail under an explicit verbose/diagnostic mode, but its default copy should still prefer `本地模型管理器`, `本地模型服务`, and `本地模型数据` over raw Docker identities.

### Legacy volume migration belongs in the launcher

The migration must not be a Settings workflow.

Evidence:

- The accepted supervisor is explicitly without Docker Socket and manages only its own child process (`prd.md:30`, `prd.md:53-57`). Settings/API therefore cannot safely inspect or mount an unlabelled legacy volume.
- Host/Docker mutations are already restricted to the launcher boundary; the README says the browser cannot perform host path or Docker mutations (`README.md:140-145`).
- The current launcher validates an exact service allowlist before removal (`zhixu:879-895`), validates exact project/volume labels (`zhixu:969-1010`), keeps ordinary `status` read-only (`zhixu:1438-1475`), preserves data on `down` (`zhixu:1510-1525`), and requires explicit confirmation for reset (`zhixu:1528-1563`). A one-time copy belongs beside these ownership and destructive-action controls.
- The legacy resource has no Compose ownership labels (`runtime-and-official-docs.md:7-15`), so it cannot be silently adopted or relabelled as project-owned. Prior research correctly requires copy-and-verify with the source preserved (`docker-lifecycle.md:199-218`), although its suggestion to initiate that from Settings assumed the now-rejected host reconciler architecture.

Recommended operator flow:

1. `./zhixu up` performs read-only exact legacy detection and prints one notice: `检测到旧版本地模型数据。可执行一次迁移以避免重新下载；原数据会保留。`
2. A dedicated explicit launcher command (suggested shape: `./zhixu local-model migrate`) owns confirmation, disk-space preflight, stopping the exact validated legacy runtime if necessary, source-read-only copy, target verification, resumability, rollback, and a durable completion marker. Exact command spelling is a design choice.
3. Default progress uses user/operator stages: `正在检查`, `正在复制模型数据`, `正在验证`, `迁移完成`, `迁移失败`; verbose diagnostics may show exact resource identities. Raw Docker stderr and credentials do not appear in normal output.
4. Success preserves the legacy source as rollback input. Ordinary `down` preserves the new managed model volume. Confirmed `reset` deletes only the new project-owned managed volume and must say that local model files will be deleted; it must not delete the unowned legacy source.
5. Skipping migration is allowed. Local Test/Apply can download only the exact models it needs into the new managed volume. Migration therefore does not block remote/disabled startup and is never part of Save/Apply semantics.
6. Settings has no migration action. If an operator starts migration while the app is available and local actions are temporarily blocked, Settings may say only: `本地模型数据正在升级，请稍后重试。` On a terminal migration fault it may point to `./zhixu status`; it must not show volume/container identities.

This placement avoids conflating a one-time deployment upgrade with ordinary model selection and preserves the existing contract that normal model Apply does not require a launcher command (`docs/operations.md:181-190`).

### Project documentation changes needed

The user-facing and operator documents need one consistent story:

- **README:** replace the stale instruction that model changes are applied with `./zhixu restart` (`README.md:190-194`). State that Settings `保存并应用` hot-applies the exact revision; selecting local Ollama starts/prepares it automatically; selecting online/disabled eventually releases local-model memory while keeping model files.
- **README data retention:** extend the current `down`/`reset` text (`README.md:120-127`) to say `down` preserves managed local-model files and confirmed `reset` deletes the new managed local-model volume.
- **Operations guide:** add a “本地模型生命周期” subsection next to managed model settings (`docs/operations.md:168-200`), document user-level states, automatic pull/recovery, launcher migration, `status`, and the distinction between daily Apply and one-time upgrade recovery. Extend the data-retention table (`docs/operations.md:119-128`) with managed model data and the separately preserved legacy source.
- **User guide:** expand Settings and Maintenance (`docs/user-guide.md:235-241`) with explicit `本地 Ollama`, automatic download, Save-only versus Apply, and the meaning of `未运行/已就绪`; do not teach Relay, Docker, or volume concepts there.
- **Requirements:** add the local supervisor/child-process lifecycle and progress outcome next to the existing hot-activation requirements (`docs/requirements.md:178-186`) and add a deployment acceptance criterion for all-online manager-idle memory behavior, managed model retention, and refresh-safe pull progress.
- **Architecture:** add a new ADR for the selected main-Compose supervisor. ADR-0022 currently makes “不新增常驻服务” a hard constraint and scored criterion (`docs/architecture/adr/0022-model-runtime-hot-activation.md:15-22`, `docs/architecture/adr/0022-model-runtime-hot-activation.md:29-49`). The new ADR must explicitly narrow/supersede that clause while preserving “no API/Worker restart for normal Apply.” Leaving both as accepted without reconciliation would make project documentation internally contradictory.
- **Launcher help/confirmation:** update `status`, `down`, `reset`, and logs/help to include the known local-model manager and managed model data. Current help says reset deletes project volumes but its prompt names only PostgreSQL/model-secret volumes (`zhixu:49-65`, `zhixu:1528-1563`); after adding the model volume, the prompt must explicitly name local model files.

### Tests required

PRD/contract and frontend:

- Exhaustive state-copy tests for manager available + off, Save-only local + off, starting, pull total unknown, pulling with bounded progress, checking, ready, active-ready plus candidate-pulling, stopping, manager stale, preparation failure, and post-commit stop failure.
- Strict decoder invariants for operation identity/reason, `pulling` progress, `completed <= total`, model-count bounds, fresh/off semantics, and the legal `active_ready=true + pulling` combination.
- Durable operation tests: refresh/reconnect/HTTP response loss recovers the same Test/Apply pull; repeated Test/Apply joins rather than duplicates; retries increment the same operation's durable `attempt_no` and never reset projected cumulative progress.
- Assert Save-only performs no start/pull, while Test and Save-and-Apply make preparation visible and do not claim active before commit.
- Assert Settings DOM, errors, storage, and Network projections contain no `docker`, `compose`, container/volume IDs, Relay URL, `127.0.0.1`, `11434`, image/blob digest, raw pull status, or shell output.
- Accessibility tests for labelled determinate/indeterminate progress, textual non-color state, polite throttled announcements, alert semantics, keyboard controls, no focus theft during polling, and long model/error text at 390x844. Existing specifications already require these properties (`.trellis/spec/frontend/model-settings.md:39`, `.trellis/spec/frontend/model-settings.md:66-77`).

Launcher/migration/docs:

- Exact legacy detection and rejection of near-match/foreign resources; no silent adoption.
- Explicit confirmation/non-interactive behavior, disk-space preflight, source read-only, interrupted copy resume, target verification, source preservation, rollback, and idempotent completed rerun.
- `up` remains successful for remote/disabled when migration is skipped; local Test/Apply can auto-download into the new volume.
- `down` preserves managed model files; reset text explicitly includes them and deletes only label-proven project-owned data; the legacy source survives.
- Default launcher output uses product-level local-model terms and does not print raw engine output; verbose diagnostics remain bounded and secret-free.
- Documentation checks assert README no longer tells users to restart for normal model Apply and the retention/migration behavior is consistent across README, operations, user guide, requirements, and the new ADR.

### External references

- Official Ollama API evidence captured in `runtime-and-official-docs.md:26-34`: `/api/pull` supports progress, `/api/tags`/`/api/show` support presence/verification, and `/api/ps` reports loaded models.
- Official Docker Compose volume semantics captured in `docker-lifecycle.md:239-248`: ordinary `compose down` preserves named volumes unless `--volumes` is supplied; named volumes carry project lifecycle identity.
- The observed legacy runtime uses `ollama/ollama:0.9.6` and an unlabelled `zhixu-eino-live-models` volume (`runtime-and-official-docs.md:7-15`); upstream current API/storage behavior must not be assumed compatible with that legacy version.

### Related specs

- `.trellis/spec/frontend/model-settings.md:23-39` - desired/active/applied, test/apply, polling, failure, and responsive accessibility semantics.
- `.trellis/spec/frontend/component-guidelines.md:30-48` - pending/success/failure, non-color status, focus, labels, live regions, and disabled reasons.
- `.trellis/spec/backend/model-settings-runtime.md:28-36` - Save-only, activation, previous serving generation, and lease-safe retirement.
- `docs/architecture/adr/0022-model-runtime-hot-activation.md:51-100` - current rollout phases, serving/candidate separation, generation leases, and launcher-not-required Apply.

## Caveats / Not Found

- The exact supervisor status/progress persistence schema and endpoint shape are not designed yet. The UX requires operation identity and durable progress but does not prescribe the database table layout.
- Ollama pull streams may not provide a meaningful total immediately. The UI must support a short indeterminate stage and must not fabricate percentage; the pinned managed image still needs fixture tests for progress/resume behavior.
- The managed image/version, model-storage compatibility from legacy `0.9.6`, and required temporary free space for copy migration remain unresolved. These facts decide whether direct copy is safe or the launcher must offer redownload as the only supported path.
- The exact launcher migration command name, whether it is interactive by default, and the durable completion-marker location remain design choices. The ownership decision is not open: migration belongs to the launcher, not Settings.
- A small always-present supervisor still consumes some memory. PRD AC1 requires real Compose measurement, but no accepted idle-memory budget is currently stated.
- This research did not edit PRD, design, specs, product code, documentation, or git state.
