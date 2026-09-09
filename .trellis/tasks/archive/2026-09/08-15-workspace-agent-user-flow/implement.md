# 受限 Workspace Agent 实施清单

> 状态（2026-09-08）：开发交付完成。Phase 0–6、本地实库/River、浏览器、兼容与恢复已有下文直接证据。按用户此次精简收尾要求，目标环境部署/灰度/OTLP 观察不再阻塞开发归档；这些操作本轮未执行，功能开关仍默认关闭。

## 1. Delivery Strategy

本任务保持一个跨层交付单元，不再创建 Trellis 子任务。原因是 Question mode、Workflow Definition、Tool versions、Answer union、OpenAPI 和前端严格 decoder 必须按同一合同版本发布；拆成可独立上线的子任务会制造不可用的中间组合。实现阶段仍可按下列互不重叠文件区域并行派发子 Agent，主会话负责合同冻结、迁移/共享接口、整合和最终验证。

Ordered dependencies:

```text
Phase 0 contracts/fixtures
  -> Phase 1 schema/domain
  -> Phase 2 tool receipts + four exact tools
  -> Phase 3 durable workflow/budget/publication
  -> Phase 4 HTTP/OpenAPI/SSE
  -> Phase 5 Web UX
  -> Phase 6 E2E/review/docs/rollout
```

Frontend may implement against frozen Phase 4 fixtures after OpenAPI is committed, but it cannot declare completion before the real backend contract and browser flow pass.

## 2. Preconditions

- [x] User explicitly approves the latest planning summary in a message after planning artifacts are presented.
- [x] Run `python3 ./.trellis/scripts/task.py start workspace-agent-user-flow` and confirm task status is `in_progress`.
- [x] Load `trellis-before-dev` and all curated `implement.jsonl` context before edits.
- [x] Re-read `prd.md`, `design.md`, this file and both research notes; preserve unrelated dirty worktree files.
- [x] Capture current fixed RAG Definition/Graph/Tool hashes, OpenAPI operation baseline and relevant migration head.
- [x] Confirm no overlapping migration or Tool catalog edits appeared after planning; choose the next free migration number only at implementation time.

Implementation baseline (2026-08-15): OpenAPI SHA-256 `fdaf3390a3305383c042cd140a13c159fe68abb00176e4617b18ce4d311f8342`; migration head `00082`; fixed Tool/RAG/Question hashes are asserted by Phase 0 golden tests.

## 3. Implementation Phases

### Phase 0. Freeze contracts and failure fixtures

- [x] Freeze `QuestionMode`, the complete Workspace Analysis Answer terminal matrix (`completed/refused/clarification_required/failed/cancelled`), result schemas, nullable/required Model Run rules, timeline DTO, Problems and rollout readiness behavior.
- [x] Freeze `workspace-analysis@1` graph, six node keys, deterministic operation kinds/ordinals/request hashes, input/output schema versions, per-node single-Tool allowlists and exact Tool refs.
- [x] Freeze v1 budget policy: 6 nodes, 3 Model Calls, 6 Tool Calls, max 3 Source reads, serial tools, Token/output ceilings and the exact node/Run/River/lease deadline formulas from `design.md`.
- [x] Freeze `ResultPersistencePolicy` as an `omitempty` zero-compatible Definition field, receipt/private-binding schemas/byte limits, and capture canonical bytes/hash fixtures for every existing Tool contract (planning baseline: 13), not only the four new versions.
- [x] Create deterministic fixtures for clean/dirty Git, retrieval short refs, receipt replay, Citation drift, Provider/Tool Unknown, budget exhaustion and cancellation.
- [x] Add regression assertions for all 13 current Tool hashes, current RAG v1/v2 graph hashes, and the existing default-RAG Question hash; omitted mode and explicit `rag` must preserve that v1 hash while `workspace_analysis` uses the versioned mode-aware hash.

Validation:

```bash
go test ./internal/conversation/domain ./internal/conversation/workflow ./internal/tools/adapter/catalog
go test ./internal/agent/application ./internal/agent/adapter/workflow
git diff --check
```

Rollback point: only task-owned tests/fixtures and contract constants; no schema or runtime behavior enabled.

### Phase 1. Forward migration and domain contracts

- [x] Add a forward migration for Question mode/backfill, Workspace Analysis Answer terminal constraints, Analysis Run, logical Operation, budget reservation, immutable Candidate and Tool result receipt.
- [x] Add composite FK/unique/check/size/time/immutability constraints and indexes for exact Workspace/Run/Answer/Attempt binding.
- [x] Extend Conversation domain canonicalization/hash/persistence with mode; omission maps to `rag` and unsupported Workspace Analysis scope rejects.
- [x] Add Workspace Analysis run policy, cross-Attempt logical operation, budget reservation/settlement and Candidate domain types with Chinese comments/Javadoc-equivalent Go comments per project rules.
- [x] Add Tool receipt domain/repository contracts and `ResultPersistencePolicy`; prove old Tool definitions and replay policies remain canonical-byte/hash compatible.
- [x] Add real PostgreSQL migration/backfill/constraint/transaction tests, including old-binary-compatible inserts where applicable.

Validation:

```bash
go test ./internal/conversation/domain ./internal/agent/domain ./internal/tools/domain
go test ./internal/platform/migration -run 'WorkspaceAnalysis|QuestionMode|ToolResultReceipt'
go test ./internal/conversation/adapter/postgres ./internal/agent/adapter/postgres ./internal/tools/adapter/postgres
```

Rollback point: feature flag remains off; additive schema stays in place if application code is reverted. Do not delete persisted receipts/candidates.

### Phase 2. Canonical receipts and exact read-only tools

- [x] Add bounded `ExecutorResult.PrivateBinding` for explicitly opted-in versions and a `FinalizeCallWithReceipt`/equivalent transaction primitive; success Call CAS and immutable receipt insertion must be atomic.
- [x] Add the Workspace Analysis tool-completion unit of work so Call finalization, receipt insert, reservation settlement and logical-operation success share one transaction; uncertain commit returns no output to model/successor and reconciles by exact identity.
- [x] Keep old Tool versions on their historical receipt loaders and hashes; only new Workspace Analysis versions opt in.
- [x] Implement a narrow dirty Git status port in `internal/platform/gitcli`, separate from Approval Snapshot, with the fixed argv/config-isolated command profile, 1 MiB cap and exact porcelain-v2 record/count semantics from `design.md`.
- [x] Add `ReadGitStatus@2` with aggregate-only output and fail-closed detached/unborn/ambiguous behavior.
- [x] Add `SearchKnowledge@2` with max five refs, 4 KiB snippets, deterministic first-three selection, 32 KiB output and identity/hash-only private Citation binding.
- [x] Add `ReadSource@3` resolving only the frozen same-Run refs; enforce 4 KiB excerpt, `truncated`, `content_hash`, 8 KiB output and no replacement/partial continuation.
- [x] Add `ValidateCitation@3` resolving candidate refs and complete tuples server-side.
- [x] Register all four as `TRUSTED_WORKFLOW_ONLY` and verify write/legacy/dynamic/model-selected tools refuse before Executor invocation.
- [x] In Phase 3, wire each real node to server-select its sole exact Tool with no generic Agent Tool Invoker.
- [x] Add adapter/runtime scans proving no path, porcelain line, tuple, Prompt, snippet/excerpt body or private binding leakage through result documents, JSON, formatting or structured logs.
- [x] In Phase 4, scan the actual Event, HTTP Problem, SSE, Audit, Trace and Metric projections after those surfaces are wired.

Validation:

```bash
go test ./internal/tools/application ./internal/tools/adapter/catalog ./internal/tools/adapter/postgres
go test ./internal/tools/adapter/workspace ./internal/tools/adapter/retrieval ./internal/platform/gitcli
go test -race ./internal/tools/...
```

Rollback point: new versions are unreachable while Definition/feature gate is off; old tools and fixed RAG remain unchanged.

### Phase 3. Durable Workflow, budget and publication

- [x] Implement immutable `workspace-analysis@1` contracts and register six node executors in API/Worker.
- [x] Generalize Question Dispatcher selection by canonical mode while keeping one transaction for Question, Workflow and pending Answer.
- [x] Create Analysis Run and budget facts atomically with Workspace Analysis dispatch.
- [x] Implement each node with exact predecessor receipt checks and stable output receipts; Search freezes its first 1–3 reads and any selected read failure stops before synthesis. Normal next phases use Workflow successors, not retries.
- [x] Implement `AuthorizeOperation` with the frozen lock order and one transaction for active DB-time lease/fence, cancel/terminal, deadline/hash/budget checks, logical operation, reservation and `STARTED` Model/Tool Call.
- [x] Reconcile replacement Attempts through the same logical operation: exact success/failure replay, old-lease isolation, and fail-closed hash mismatch/Unknown; never re-execute Git/Search or duplicate a charge.
- [x] Apply the frozen Node context, Provider/Tool and River job formulas plus the renewable lease/heartbeat/commit-margin formula; readiness rejects incompatible global runtime configuration without requiring one lease grant to span the whole job.
- [x] Reuse structured Query Planner, recording model adapters, Answer Stream and Faithfulness Review without leaking Eino types.
- [x] Persist immutable candidate after synthesis and implement the dedicated Workspace Analysis finalizer: completed Answer binds Synthesis Model Run while exact validation receipt and separate succeeded Review Model Run are mandatory in the same publication transaction.
- [x] Implement the frozen terminal matrix so deterministic refusal can omit Model Run, clarification binds Planner, and pre-model failure/cancel/Unknown leaves no pending Answer; terminal Run cannot authorize new calls.
- [x] Add River/PostgreSQL fault tests: response loss, Worker kill, duplicate delivery, stale fence, receipt committed but Node completion lost then lease reclaim, authorization/cancel race, budget race, and Synthesis/Review dual-ModelRun publication/replay.
  - [x] Prove response loss, receipt commit plus lost Node completion/lease reclaim, stale fence, exact replay, and Synthesis/Review publication bindings.
  - [x] Prove concurrent duplicate Model authorization reserves budget exactly once and real Runtime Cancel racing authorization leaves either zero facts or one complete atomic bundle.
  - [x] Exercise an actual Worker process SIGKILL/restart with River rescue, lease-lost replacement and exact-once durable facts.
  - [x] Exercise every operationally reachable v1 terminal reason in one aggregate real River gate; prove `WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED` is unreachable for every valid frozen-v1 operation prefix and that PostgreSQL rejects any non-causal budget terminal proof.

Validation:

```bash
go test ./internal/agent/application ./internal/agent/adapter/workflow ./internal/workflow/adapter/postgres
go test ./cmd/worker -run 'WorkspaceAnalysis|ToolReceipt|QuestionDispatch'
go test -race ./internal/agent/... ./internal/workflow/...
```

Rollback point: disable Workspace Analysis submissions and drain/cancel existing Runs. Never remap them to fixed RAG.

### Phase 4. HTTP, OpenAPI, timeline and observability

- [x] Add `mode` to Question request/response mapping, strict decode and examples; old request omission remains accepted as `rag`.
- [x] Extend Answer union and strict HTTP projection for `workspace_analysis`, `workspace_analysis_refusal`, clarification and `workspace_analysis_termination`, including exact Model Run nullability rules.
- [x] Add the authoritative scoped Analysis Timeline snapshot with deterministic safe projections for Git/Search/Read/Validate/Model phases.
- [x] Append idempotent sanitized Workspace Analysis invalidation events with deterministic source refs; commit terminal facts/event together and prove SSE never overrides the snapshot.
- [x] Add Audit for run start/cancel/terminal and Tool refusal using sanitized `ActorAgent`; do not treat receipts as Audit or leak their bodies/bindings.
- [x] Reuse Answer Draft SSE for synthesis tokens; verify unvalidated Draft never appears as published Answer.
- [x] Add Workflow cancel action wiring for the running Turn, reusing existing authorization and safe checkpoint behavior.
- [x] Add independent API/Worker readiness/feature gate and mode-specific low-cardinality metrics.
- [x] Update `api/openapi/openapi.json`, operation parity and error/cache/security tests in the same change.

Validation:

```bash
go test ./internal/conversation/http ./internal/events/http ./internal/app ./cmd/api
go test -race ./internal/conversation/http ./internal/events/http
make openapi-check
```

Rollback point: return capability unavailable for new Workspace Analysis submissions; existing fixed RAG HTTP/SSE stays active.

### Phase 5. `/chat` Workspace Analysis UX

- [x] Extend `web/src/api/conversation.ts` with strict mode/result/timeline decoders and semantic binding checks.
- [x] Add the Composer segmented control; default `rag`, new command key when mode changes, and explicit unsupported-scope validation.
- [x] Add query hooks/reducer for Analysis timeline snapshot + SSE invalidation + Answer Draft state.
- [x] Render stable running/waiting/tool/terminal states with Lucide icons, tooltips and no raw JSON/intermediate reasoning.
- [x] Add Stop control, safe Git aggregate, existing Citation Inspector and optional “查看提案” link only; suggestion uses public validated Citation IDs and server-fixed `/proposals`, never `E<n>`.
- [x] Preserve each historical Turn's submitted mode and ensure Draft/terminal disagreement favors authoritative facts.
- [x] Add desktop and 390x844 responsive styles with stable dimensions, wrapping, keyboard/focus behavior and no horizontal overflow.
- [x] Add API decoder, reducer, component, route-cycle, reconnect/cursor, cancellation and accessibility tests.

Validation:

```bash
npm run test --prefix web -- --run src/api/conversation.test.ts src/features/rag/RagPage.test.tsx src/events/event-store.test.tsx
npm run lint --prefix web
npm run typecheck --prefix web
npm run build --prefix web
```

Rollback point: hide/disable the segmented option through the feature gate; fixed RAG page remains the default path.

### Phase 6. End-to-end verification, review and docs

- [x] Add a bounded real PostgreSQL/River/API/Worker/Vite fixture with deterministic model responses, a temporary Git repo and retrieval Evidence.
- [x] Prove success dependency chain and inspect DB receipts: Git -> Search -> 1–3 ReadSource -> Candidate -> ValidateCitation -> Review -> Answer.
- [x] Prove dirty Git aggregates without path leakage and detached/cross-workspace refusal.
- [x] Prove literal page refresh, Last-Event-ID recovery, response loss, Worker restart, duplicate delivery, cancellation and every operationally reachable v1 terminal reason; separately prove the frozen-v1 budget-exhaustion branch is unreachable and cannot be fabricated.
- [x] Prove Tool receipt commit followed by lost Node completion and lease reclaim reuses the exact logical operation/receipt with no second Git/Search observation or budget charge.
- [x] Prove completed publication binds Synthesis Model Run while requiring the separate Review Model Run; deterministic refusal, cancellation and Unknown publish the frozen terminal bundle and replay after commit-response loss.
- [x] Prove no Proposal create request, file mutation or Commit occurs when the result recommends a change.
- [x] Run desktop and 390x844 browser smoke for mode selection, timeline, tokens, Citation opening, Stop and Proposal link; inspect console/network and screenshots.
- [x] Update `docs/roadmap.md`, `docs/requirements.md`, `docs/user-guide.md`, architecture docs and relevant `.trellis/spec` only after behavior is verified.
- [x] Run `go-review`, `sql-code-review` and `code-review-and-quality`; use an independent reviewer for security, persistence, frontend and cross-layer contracts.
- [x] Map direct evidence to AC1–AC11; do not mark roadmap/task complete while any real DB/browser/fault gate is missing.

Validation:

```bash
go test ./...
go test -race ./internal/agent/... ./internal/tools/... ./internal/workflow/... ./internal/conversation/...
go vet ./...
go build ./cmd/api ./cmd/worker
npm run test --prefix web
npm run lint --prefix web
npm run typecheck --prefix web
npm run build --prefix web
make openapi-check
make workspace-analysis-terminal-matrix
make workspace-analysis-fault-smoke
git diff --check
```

Any new bounded browser/Compose Make target must be named and documented from the implemented fixture; do not claim a placeholder command ran.

## 4. Expected File Areas

| Layer | Expected areas |
| --- | --- |
| Migration | next forward migration, `internal/platform/migration/**` tests |
| Conversation | `internal/conversation/{domain,application,adapter/postgres,http,workflow}/**` |
| Agent | `internal/agent/{domain,application,adapter/postgres,adapter/workflow}/**` |
| Tools/Git/Retrieval | `internal/tools/**`, narrow `internal/platform/gitcli/**` additions |
| Workflow/Events | `internal/workflow/**`, `internal/events/**`, River composition |
| Composition/config | `cmd/api/**`, `cmd/worker/**`, `internal/app/**`, `internal/platform/config/**` |
| Contract | `api/openapi/openapi.json` and parity tests |
| Web | `web/src/api/conversation*`, `web/src/features/rag/**`, `web/src/events/**`, scoped CSS |
| E2E/docs/spec | bounded fixture/Make target, `docs/**`, affected `.trellis/spec/**` |

Shared migration, Tool Definition hashing, Question/Answer wire contracts and OpenAPI are main-session ownership boundaries. Parallel workers must not independently edit these shared facts.

## 5. Review Gates

- [x] **Go**: context/deadline, error wrapping, nil dependencies, goroutine/resource cleanup, Provider stream drain, race safety, production composition.
- [x] **SQL**: FK/unique/check/immutability, Workspace scope, cross-Attempt operation identity, lease/fence, CAS, frozen lock order, authorization/cancel race, receipt+settlement atomicity, Answer terminal matrix, backfill and rolling compatibility.
- [x] **Security**: `TRUSTED_WORKFLOW_ONLY` per-node Tool allowlist/version, historical Definition hashes, receipt byte caps/private bindings, prompt injection, path/ID forgery, Git config isolation, log/event/Problem leakage and no write capability.
- [x] **Frontend**: strict decoders, Query/SSE/local state ownership, reconnect, authoritative terminal state, a11y, mobile overflow, no Browser Storage content.
- [x] **Cross-layer**: Question mode, Definition, Tool refs, operation/recovery identities, Synthesis/Review publication bindings, Answer union, authoritative timeline/events, Problems and deadline/readiness formulas agree across DB/Go/OpenAPI/Web.
- [x] **Compatibility**: fixed RAG hashes/API/history/tests unchanged; before enablement every old/new API/Worker combination keeps RAG available and fails closed for the new mode; after a Workspace stores new facts, routing keeps that Workspace on a compatible API reader.
  - [x] `make compose-workspace-analysis-compat-smoke` builds legacy binaries from frozen pre-feature commit `541033dd4548f8a53ef1064053ff6545faf2820e`, applies current migrations once, and proves all four legacy/current API/Worker combinations preserve fixed RAG, reject Workspace Analysis fail closed, preserve the retrieval safety projection, and create zero Workspace Analysis facts.
  - [x] `make compose-rag-browser-smoke` proves deterministic fixed RAG through real API/River/Worker/PostgreSQL, multi-frame SSE, final Answer/Citation, and desktop/mobile browsers.
  - [x] The post-fact phase recreates the API with the exact same current image and feature-off configuration, preserves the ingress netns identity, and rolls back only the Worker artifact to the frozen legacy image. It proves historical Answer/Turn/Timeline/exact-run SSE replay, fail-closed new admission, fixed RAG continuity, and unchanged Workspace-wide facts; it does not claim uninterrupted API connections. The repository has no Workspace-aware dual-API router, so this is the complete supported local rollback topology.

## 6. Release And Recovery Checklist

### Local release rehearsal

- [x] Apply the additive migration once while API and Worker Workspace Analysis flags are off.
- [x] Start matching current API/Worker, verify a fresh exact Worker capability through successful admission, and complete a real Workspace Analysis Run. Generic `/readyz` proves only process health.
- [x] Exercise one temporary canary Workspace through success, SSE/refresh, Stop/recovery and desktop/mobile browsers while preserving fixed RAG.
- [x] Run a pinned isolated Collector with Worker telemetry required; prove a real completed Run and exact replay leave one `workspace_analysis.outcome_total` cumulative series at value `1`, with the frozen four business labels, bounded process Resource plus fixed Collector `job`, and no business identity labels, including graceful shutdown flush.
- [x] Require successful real PostgreSQL/River, fault/recovery, browser and compatibility gates before any expansion decision.
- [x] Disable new admission, drain all Runs, recreate API from the exact same current image while retaining ingress identity, roll back only the Worker artifact, and prove historical reads, exact-run SSE replay, fixed RAG and immutable facts.

### 目标环境操作参考（移出开发交付门禁，本轮未执行）

- Apply the additive migration to the target environment with both feature flags off and retain the migration/change record.
- Deploy approved immutable API/Worker artifacts, record their digests and `config_revision`, and verify fresh exact Worker capability plus process readiness.
- Enable one authorized canary Workspace and query the Worker OTLP backend for stable outcomes, Unknowns, budget terminals and receipt failures over an approved observation window.
- Expand only after the real canary success/recovery/browser evidence and an explicit expansion approval are archived.
- If rollback is required, disable admission, drain/cancel, investigate Unknown, retain current API/schema/facts and roll back only the Worker; archive the target-environment rollback record.

## 7. Verified Evidence (2026-08-17 to 2026-08-18)

- `go test -count=1 -timeout 60s ./...`、`go vet ./...`、`go build ./cmd/api ./cmd/worker` 通过。
- `go test -race -count=1 -timeout 60s ./internal/agent/... ./internal/tools/... ./internal/workflow/... ./internal/conversation/...` 通过。
- Web 全量 test（1228 tests）、lint、typecheck、build 与 `make openapi-check` 通过。
- 真实 PostgreSQL/River fault 用例证明 canonical Tool receipt 已提交但 Node completion 响应丢失后，实际 lease 到期、replacement Attempt 恢复，Git Executor/Call/receipt/reservation 均仍为一份。
- 固定 RAG 在外部环境强制打开 Workspace Analysis 的 hostile env 下仍以 feature-off 配置通过 deterministic Compose smoke。
- `make compose-workspace-analysis-smoke` 通过真实临时 Git、PostgreSQL、API、River、Worker、deterministic Provider、SSE、Stop cancellation、exact replay、只读边界及 1440x900/390x844 Playwright；最终截图分别为 1440x2321 与 390x2393，无横向溢出或遮挡。
- `make compose-rag-browser-smoke` 通过固定 RAG 的真实 API/River/Worker/PostgreSQL、多帧 SSE、权威 Answer/Citation 与 1440x900/390x844 浏览器；截图保存在 `/tmp/zhixu-rag-fixture-browser-final-20260817-1500/`，人工复核无溢出或遮挡。
- `make compose-workspace-analysis-compat-smoke` 使用冻结的 pre-feature commit `541033dd4548f8a53ef1064053ff6545faf2820e` 生成真实 legacy API/Worker，在一次 feature-off current migration 后通过四个 pre-enable 滚动组合；每组固定 RAG 成功、新 mode fail closed、检索十项计数不漂移且 Workspace Analysis 事实为零。随后 post-fact 阶段以 current/current 完成真实 Run，排空后用 exact same current API image 以 feature-off 配置重建 API、保持 ingress netns identity、仅回退 Worker artifact，并验证历史 Answer/Turn/Timeline、绑定 exact Run 的 started/terminated SSE replay、新 mode 非重试 503、固定 RAG 和 Workspace 全量事实不变；该门禁不证明连接无中断。
- `make compose-workspace-analysis-worker-restart-smoke` 在最终树上通过真实 Worker SIGKILL、generation barrier、River rescue、lease-lost replacement、`RESULT_UNKNOWN`、Stop cancellation、桌面/移动浏览器和 exact-once durable facts；清理后无残留 Compose 资源。
- `make compose-workspace-analysis-otlp-smoke` 在固定 digest 的隔离 OpenTelemetry Collector 上通过：Worker 以 `required` 模式启动，真实 PostgreSQL/River 六节点 Run 完成并 exact replay，优雅停机后 Collector 只暴露一个值为 `1` 的 cumulative monotonic `workspace_analysis.outcome_total` 序列；业务标签精确为 `mode`、`definition`、`outcome`、`termination_reason`，另有有界进程 Resource 和固定 `job=zhixu-worker` 投影，且不含 Workspace/Run/Answer/Tool/Receipt 身份。该证据不替代目标环境 backend 和观察窗口。
- Stop 对精确 `WORKFLOW_VERSION_CONFLICT` 实施最多 5 次 POST/4 次权威 Answer GET 的严格有界恢复；25 个命令测试覆盖连续冲突、边界耗尽、非单调版本、控制冲突、未知 Problem、网络/5xx、刷新失败、身份/终态漂移和成功体未确认取消，浏览器只输出受限 status/error-code 诊断。
- `go test -race -tags=integration -count=3` 真实 PostgreSQL 通过并发重复 Model 授权与 Runtime Cancel/授权竞争；Review passed/failed 候选绑定夹具同样连续三次通过。
- `make workspace-analysis-terminal-matrix` 在隔离 PostgreSQL 上通过：公开 Question 经真实 River/Worker 覆盖除 `BUDGET_EXHAUSTED` 外的 13 个可达终态，并逐项反查 proof、operation、Model/Tool Call、artifact/receipt failure 和零残留预算；race 矩阵 55.069s，预算全前缀性质 0.179s，非因果 budget proof 数据库拒绝 19.718s。
- 真实 PostgreSQL/River 取消竞态回归在 Synthesis Provider 已成功返回、Candidate 持久化前通过公开 Runtime Cancel 让取消胜出；普通与 `-race` 均通过，最终 Call/Run/operation/reservation 为 `FAILED/SETTLED`，零 Candidate/ModelResult，Workflow/Analysis/Answer/proof 全部收敛为 cancelled。
- Planner clarification 的 `subject_candidate_id/hash=NULL` 真实 PostgreSQL 回归通过；Finalizer 发布并重放同一 Planner Model Run/proof，不再因 nullable 扫描错误二次归约为 `RUNTIME_FAILED`。
- `go-review`、`sql-code-review`、前端、安全、持久化和跨层独立审查已完成；发现项均在本任务内修复并复验。

本地实现、AC1–AC11、Compatibility 和 release rehearsal 已完成。2026-09-08 用户取消目标环境发布观察作为开发完成前置，任务按此范围完成归档；Migration、发布物核验、canary、OTLP 观察、扩量和现场回滚本轮未执行。保留 Runbook 供实际部署时使用，不把未执行项目记为 PASS。
