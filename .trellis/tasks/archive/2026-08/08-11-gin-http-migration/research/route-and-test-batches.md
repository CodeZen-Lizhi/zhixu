# Research: Production Routes, Chi Tests, And Migration Batches

- Query: 盘点所有生产 Routes、Chi import、测试辅助与 OpenAPI/runtime route 覆盖，提出文件互斥的迁移批次及每批定向验证，并识别当前未提交改动重叠。
- Scope: mixed（仓库源码、Trellis 规范、Gin v1.12.0 官方源码/文档）
- Date: 2026-08-11

## Findings

### 1. Executive Summary

1. 当前非 vendor 源码共有 **59 个 Go 文件直接 import Chi**：28 个生产文件、31 个测试文件。另有 `go.mod`、`go.sum`、`vendor/modules.txt` 和 vendor Chi 源码/文档引用。依赖事实见 `go.mod:3-7`。
2. 生产路由有 **25 个 Handler 注册入口、179 个 Handler operation**；`internal/app/router.go` 再注册 `GET /livez`、`GET /readyz`、`GET /api/v1/system/status`，因此恰好对应 OpenAPI 的 **154 paths / 182 operations**。可选 `GET /metrics` 不在 OpenAPI。
3. OpenAPI method 分布为 `GET=80, POST=84, PUT=12, PATCH=1, DELETE=5`；Handler 分布为 `GET=77, POST=84, PUT=12, PATCH=1, DELETE=5`，差额正是 app 的 3 个 GET。
4. 当前没有全量 runtime/OpenAPI route parity test。`api/openapi/check.mjs` 主要检查 OpenAPI 文档自身；只对 Git Sync 和 Export 用 Chi 源码正则核对注册行（`api/openapi/check.mjs:1267-1297`, `api/openapi/check.mjs:1962-1993`）。迁移后这些正则会因 Gin 大写注册 API 失效。
5. **Gin 注册前必须统一动态参数名。** 当前同一 `/workspaces/{...}` radix 分支混用 `{workspaceID}` 与 `{workspace_id}`；`impact-reports` 又混用 `{reportID}` 与 `{report_id}`。Gin v1.12.0 的路由树要求已存在 wildcard 名与新 wildcard 精确相同，否则在注册时 panic。建议所有 Gin 路由直接采用 OpenAPI snake_case 参数名，并同步 `request.PathValue` key。
6. Gin `Engine.Routes()` 可以枚举 method/path/handler，适合把 182 operations 建成可执行门禁。对比时应包含 `/livez`、`/readyz`、`/api/v1/system/status`，只在依赖非 nil 时单独允许额外 `GET /metrics`。
7. `internal/review/learningpath/http/handler.go` 有 4 个生产路由（`internal/review/learningpath/http/handler.go:58`），但该包没有 `_test.go`；目前主要依赖 app/composition 覆盖。迁移必须补包级路由、PathValue、严格 JSON 和错误映射测试。
8. 现有 HTTP 包、核心 Router、Auth、SSE 和 OpenAPI 基线均通过；详情见“Baseline Evidence”。

### 2. Complete Production Route Inventory

下表覆盖所有 25 个 Handler `Routes/OpenRoutes/ProtectedRoutes` 入口。路径为当前 Chi 模板；迁移时仅模板占位符语法/名称变化，公开 URL 不变。

| Owner | Count | Registration and operations |
| --- | ---: | --- |
| Auth | 7 | `internal/auth/http/handler.go:97`: `POST /auth/sessions`; `GET /auth/session`; `POST /auth/session/rotate`; `DELETE /auth/session`; `POST,GET /auth/api-tokens`; `DELETE /auth/api-tokens/{token_id}` |
| Workspace | 5 | `internal/workspace/http/handler.go:46`: `POST /workspaces`; `GET /workspaces/active`; `GET /workspaces/{workspaceID}`; `POST .../scan`; `GET .../source-versions` |
| Workflow | 7 | `internal/workflow/http/handler.go:62`: `POST,GET /workspaces/{workspaceID}/workflows`; `GET /workflows/{runID}`; `POST .../human-tasks/{taskID}/decision`; `POST .../pause|resume|cancel` |
| Change Control | 7 | `internal/changecontrol/http/handler.go:67`: `POST,GET /workspaces/{workspaceID}/proposals`; `POST .../impact-reports/{reportID}/proposals`; `GET /proposals/{proposalID}`; `GET .../current-content`; `POST .../approvals`; `POST .../apply-preflight` |
| Collection | 8 | `internal/collection/http/handler.go:65`: `GET,POST /collections`; `POST /collections/validate|preview`; `GET,PUT /collections/{collection_id}`; `POST .../archive`; `GET .../results` |
| Health | 11 | `internal/health/http/handler.go:57`: `GET /health/summary`; `GET /health/issues`; `GET /health/issues/{issue_id}`; `GET .../observations|decisions`; `POST .../decisions|repair-proposals`; `POST /health/scans`; `GET /health/scans/{scan_id}`; `GET,PUT /health/schedules` |
| Ingestion | 1 | `internal/ingestion/http/handler.go:29`: `POST /source-versions/{sourceVersionID}/ingestion-attempts` |
| Retrieval | 3 | `internal/retrieval/http/handler.go:54`: `POST /search`; `GET /workspaces/{workspace_id}/source-versions/{source_version_id}`; `GET .../spans/{source_span_id}` |
| Graph | 7 | `internal/graph/http/handler.go:52`: `POST /graph/global|neighborhood|path`; `GET /graph/nodes`; `GET /graph/nodes/{node_type}/{node_id}`; `GET /graph/relations/{relation_id}`; `GET .../evidence` |
| Graph Candidate | 5 | `internal/graph/http/candidate_handler.go:64`: `GET /graph/candidates`; `GET /graph/candidates/{candidate_id}`; `POST .../decisions`; `POST /graph/candidate-scans`; `GET /graph/candidate-scans/{scan_id}` |
| Conversation | 7 | `internal/conversation/http/handler.go:57`: `POST,GET /conversations`; `GET /conversations/{conversation_id}`; `POST .../questions`; `GET .../turns`; `GET /answers/{answer_id}`; `POST .../feedback` |
| Events | 1 | `internal/events/http/handler.go:71`: `GET /events` (SSE) |
| Knowledge | 4 | `internal/knowledge/http/handler.go:63`: `GET /workspaces/{workspace_id}/timeline`; `GET .../timeline/{event_id}`; `POST .../impact-analysis`; `GET .../impact-reports/{report_id}` |
| Artifact | 13 | `internal/artifact/http/handler.go:87`: `GET,POST /artifacts`; `GET /artifacts/{artifact_id}`; `POST .../outline`; `POST .../outline/approve`; `POST .../revisions`; `POST .../sections`; `POST .../sections/generate`; `GET .../section-generations`; `POST .../draft/approve`; `POST .../exports/markdown`; `GET .../exports/{export_id}`; `POST .../publish-proposals` |
| Authoring | 9 | `internal/authoring/http/handler.go:81`: `POST,GET /workspaces/{workspaceID}/authoring/working-drafts`; `GET,PUT .../{draftID}`; `POST .../{draftID}/freeze`; `POST .../documents/{documentID}/revisions/{revisionID}/publish-proposals`; `GET .../documents/{documentID}`; `GET .../authoring/documents`; `GET .../authoring/overview` |
| Capture | 7 | `internal/capture/http/handler.go:71`: `POST /workspaces/{workspaceID}/captures`; `POST .../capture-files`; `GET .../captures`; `GET .../captures/{captureID}`; `POST .../retry`; `GET .../source-versions/{sourceVersionID}/knowledge-profile`; `POST .../knowledge-profile/retry` |
| Organizing | 16 | `internal/organizing/http/handler.go:95`: draft create/get/update/suggest/material add-select-delete/confirm; material search; snapshot get; template list/create/get/clone/revise; run get, all under `/workspaces/{workspaceID}/organizing/...` |
| Document History | 4 | `internal/documenthistory/http/handler.go:58`: `GET .../documents/{documentID}/history`; `GET .../history/compare`; `POST .../restore-previews`; `POST .../restore-proposals` |
| Git Sync | 9 | `internal/gitsync/http/handler.go:65`: `GET,PUT,DELETE .../git-remote`; `POST .../git-remote/tests`; `GET .../git-sync`; `GET,POST .../git-sync/runs`; `GET .../runs/{runID}`; `POST .../retries` |
| Model Settings | 3 | `internal/modelsettings/http/handler.go:72`: `GET,PUT /settings/models`; `POST /settings/models/test` |
| Export | 8 | `internal/export/http/handler.go:60`: `POST /exports`; `GET /exports/{export_id}`; `GET .../download`; `GET /workspaces/{workspace_id}/exports`; `POST,GET .../attachment-exports`; `GET .../{export_id}`; `GET .../{export_id}/download` |
| Review | 17 | `internal/review/http/handler.go:73`: deck list/create/get; card list/create/edit; deck schedule pause/resume/reset; due list; card approve/reject/invalidate; bulk invalidation; session start/complete/answer |
| Learning Path | 4 | `internal/review/learningpath/http/handler.go:58`: `POST,GET /review/answers/{answer_id}/learning-path`; `PUT .../status`; `PUT .../steps/{step_id}` |
| Memory | 8 | `internal/memory/http/handler.go:77`: `POST,GET /memories`; `GET /memories/{memory_id}`; `POST .../confirm`; `PUT /memories/{memory_id}`; `POST .../pause|resume`; `DELETE /memories/{memory_id}` |
| Interview | 8 | `internal/review/interview/http/handler.go:84`: start/list/get; submit turn; complete; suggest memory candidate; update learning path status/step |

App-owned and composition routes:

- `internal/app/router.go:116-121` constructs Chi and installs global no-store, request ID, trace, request log and recovery middleware.
- `internal/app/router.go:122`, `internal/app/router.go:125`, `internal/app/router.go:158`, `internal/app/router.go:161` register `GET /livez`, `GET /readyz`, optional `GET /metrics`, and `GET /api/v1/system/status`.
- `internal/app/router.go:164-179` owns open Auth vs protected Auth vs auth-unavailable grouping.
- `internal/app/router.go:211-280` is the single production domain registration list. This is the 26th route-registration entry point when counted with the 25 Handler entries.
- `internal/app/router.go:180-197` owns API 404, global 405 and static fallback behavior.
- `internal/export/http/attachment.go:149` and `internal/graph/http/candidate_scan_handler.go:94` do not register routes but directly read Chi params and are part of the 28 production Chi files.

### 3. Parameter And Middleware Migration Hazards

#### 3.1 Gin wildcard-name conflict is a hard precondition

Examples of conflicting names already present:

- `internal/workspace/http/handler.go:49` uses `{workspaceID}`, while `internal/export/http/handler.go:64` and `internal/knowledge/http/handler.go:64` use `{workspace_id}` under the same `/workspaces/...` branch.
- `internal/changecontrol/http/handler.go:70` uses `{reportID}`, while `internal/knowledge/http/handler.go:67` uses `{report_id}` on the same structural route.
- Source Version routes mix `{sourceVersionID}` (`internal/ingestion/http/handler.go:30`) and `{source_version_id}` (`internal/retrieval/http/handler.go:56`).

Gin v1.12.0 `tree.go` checks the existing wildcard token for an exact match and panics on conflict. Therefore a mechanical `{x}` -> `:x` rewrite cannot even construct the full engine. Canonical migration rule:

```text
Gin registration placeholder == OpenAPI placeholder (snake_case)
GinHandler SetPathValue key   == same snake_case name
Handler request.PathValue key == same snake_case name
```

The full engine must be built in a test immediately after each integrated route batch; the final route inventory test also proves that no wildcard panic or shadowed route remains. Exact static routes remain higher priority than params in Gin, but `GET /workspaces/active` must retain its existing regression (`internal/app/router_test.go:206`).

#### 3.2 Existing handlers are mostly net/http handlers

Handlers currently accept `http.ResponseWriter` and `*http.Request`; Chi is used mainly by the registration signature and `chi.URLParam`. Examples: Artifact route registration at `internal/artifact/http/handler.go:87` and param read at `internal/artifact/http/handler.go:310`; Authoring registration at `internal/authoring/http/handler.go:81` and param helpers at `internal/authoring/http/handler.go:556`; Retrieval registration at `internal/retrieval/http/handler.go:54` and params at `internal/retrieval/http/handler.go:305`.

This supports the planned thin `httpapi.GinHandler`: copy Gin params into standard `Request.SetPathValue`, then invoke `gin.WrapF`. It keeps Gin Context out of Application/Domain and avoids rewriting strict decode/streaming logic. The bridge itself needs tests for all params, request context, status/Header, `http.Flusher`, cancellation and encoded path behavior.

#### 3.3 Middleware behavior is contract-bearing

- Auth is currently `func(http.Handler) http.Handler`, writes Principal into standard request context, and checks all explicit Capability rules before delegation (`internal/auth/http/handler.go:111-131`, `internal/auth/http/handler.go:159-180`). Gin conversion must call `Abort()` on every rejected path and `Next()` only after Principal injection.
- Capability matching uses concrete `request.URL.Path`, not Chi route context (`internal/auth/http/handler.go:163-175`, `internal/auth/http/handler.go:334-351`). Canonicalizing route placeholder names must not alter this policy table.
- Request logging currently reads the Chi matched template to avoid logging concrete IDs (`internal/app/router.go:528-551`); recovery also logs that safe template (`internal/app/recovery.go:31-40`). Gin must use `Context.FullPath()` and normalize `:param` to `{param}` if log shape compatibility is required.
- Existing `statusWriter` preserves `http.Flusher` for SSE (`internal/app/router.go:497-526`). Gin Writer implements `http.Flusher`, but bridge and response-started recovery must prove this rather than assume it.
- Do not use `gin.Default()`: it would install framework logger/recovery in addition to project middleware. `gin.New()` plus explicit settings/handlers is required.
- Gin default trailing-slash redirects and default 404/405 text are incompatible with roadmap R1. Explicitly disable redirects, enable method handling, and supply project `NoRoute`/`NoMethod` Problem responses.

Missing baseline cases to add before relying on parity:

- trailing slash and repeated slash for public, protected and static paths;
- percent-encoded/unescaped path parameter behavior;
- wrong method on a protected path, both authenticated and unauthenticated, to freeze auth-vs-405 ordering;
- `HEAD`/`OPTIONS` on known and unknown paths;
- NoRoute/NoMethod request log value must be empty/safe and never the concrete URL;
- full engine construction with all handlers must not panic.

### 4. Strict JSON, Validator, SSE, Upload And Download Coverage

The migration must not substitute Gin automatic binding for project boundaries:

- Shared response/Problem and basic bounded decode owner: `internal/httpapi/response.go:44-80`.
- Many high-risk handlers use `strictjson.DecodeObject`, including Artifact (`internal/artifact/http/handler.go:843-859`), Collection (`internal/collection/http/handler.go:591-607`), Model Settings (`internal/modelsettings/http/handler.go:500-537`), Authoring, Capture, Export, Git Sync, Graph, Health, Knowledge, Memory, Organizing and Review.
- Existing strict tests explicitly cover duplicate keys, unknown fields, Unicode, multiple documents, content type and body limits; representative anchors are `internal/artifact/http/handler_test.go:177`, `internal/collection/http/handler_test.go:202`, `internal/modelsettings/http/handler_test.go:172`, `internal/organizing/http/handler_test.go:494`.
- Validator already exists as a direct dependency (`go.mod:7`) but HTTP handlers do not currently use its default Gin binding. Validator adoption should be a separately testable helper and must map back to existing Problem/error codes; framework-generated 400 bodies are not acceptable.
- SSE checks `http.Flusher`, writes exact stream headers, flushes initial replay and heartbeat, and exits on request cancellation (`internal/events/http/handler.go:97-123`, `internal/events/http/handler.go:191-258`). Existing SSE tests cover cursor failures, retention, frame formats, heartbeat and cancellation (`internal/events/http/handler_test.go:25`, `internal/events/http/handler_test.go:101`, `internal/events/http/handler_test.go:197`, `internal/events/http/handler_test.go:246`, `internal/events/http/handler_test.go:271`).
- Capture upload and Export/Attachment download remain stdlib-stream boundaries. Representative tests: `internal/capture/http/handler_test.go:56`, `internal/export/http/handler_test.go:125`, `internal/export/http/handler_test.go:174`, `internal/export/http/handler_test.go:217`.

No request-level application rate limiter implementation was found. Repository references to rate limits concern Provider/Worker retry classification. The roadmap's “限流语义保持兼容” therefore means no new framework default or accidental behavior; it is not evidence that a current HTTP rate limiter exists.

### 5. Chi Test Helper Inventory

All 31 direct test imports must migrate; removing only production imports will leave Chi in `go.mod`/vendor.

| Area | Chi-dependent test files and anchor |
| --- | --- |
| cmd/api composition | `cmd/api/artifact_composition_test.go:102`; `cmd/api/health_smart_collection_composition_integration_test.go:89`; `cmd/api/main_test.go:245` |
| app core | `internal/app/recovery_test.go:140`; `internal/app/router_test.go:792` |
| Artifact/Auth/Authoring/Capture | `internal/artifact/http/handler_test.go:98`; `internal/auth/http/handler_test.go:159`; `internal/authoring/http/handler_test.go:576`; `internal/capture/http/handler_test.go:358` |
| Change Control | `internal/changecontrol/http/handler_test.go:89`; `internal/changecontrol/application/approval_dispatch_river_smoke_integration_test.go:131` |
| Collection | `internal/collection/http/handler_test.go:292`; `internal/collection/http/replay_integration_test.go:56` |
| Conversation/Document History/Events | `internal/conversation/http/handler_test.go:179`; `internal/documenthistory/http/handler_test.go:266`; `internal/events/http/handler_test.go:57` |
| Export/Git Sync | `internal/export/http/handler_test.go:342`; `internal/gitsync/http/handler_test.go:173` |
| Graph/Health/Ingestion | `internal/graph/http/candidate_handler_test.go:615`; `internal/graph/http/handler_test.go:679`; `internal/health/http/handler_test.go:622`; `internal/ingestion/http/handler_test.go:78` |
| Knowledge/Memory/Model Settings | `internal/knowledge/http/handler_test.go:558`; `internal/memory/http/handler_test.go:521`; `internal/modelsettings/http/handler_test.go:740` |
| Organizing/Retrieval | `internal/organizing/http/handler_test.go:621`; `internal/retrieval/http/handler_test.go:34` |
| Review/Interview | `internal/review/http/handler_test.go:30`; `internal/review/interview/http/handler_test.go:197` |
| Workflow/Workspace | `internal/workflow/http/handler_test.go:231`; `internal/workspace/http/handler_test.go:329` |

Notable non-mechanical helpers:

- `cmd/api/artifact_composition_test.go:102` exposes `interface{ Routes(chi.Router) }`; change it to the final Gin router interface or an `http.Handler` fixture.
- Collection, Document History and Health helpers return `chi.Router` (`internal/collection/http/handler_test.go:292`, `internal/documenthistory/http/handler_test.go:266`, `internal/health/http/handler_test.go:622`).
- Memory, Interview and Workflow tests construct nested protected groups with Auth middleware (`internal/memory/http/handler_test.go:521-525`, `internal/review/interview/http/handler_test.go:576-580`, `internal/workflow/http/handler_test.go:267-271`); these are the best tests for Gin `Use/Abort/Next` ordering.
- `internal/app/router_test.go:790` is explicitly named for Chi route-template logging and must be renamed/reworked to assert the same safe `{param}` output under Gin.
- Learning Path has no package test; add one instead of considering the app test sufficient.

### 6. OpenAPI And Runtime Route Gate

Current evidence:

- `api/openapi/openapi.json` has 154 path items and 182 operations (structured `jq` count on 2026-08-11).
- `api/openapi/check.mjs:144-271` maintains a large required-operation subset and checks success/405 responses.
- `api/openapi/check.mjs:342-368` enforces public-vs-business authentication for OpenAPI operations.
- No Go test reads `api/openapi/openapi.json` or compares it with the production Router.
- `api/openapi/check.mjs:5-7` reads only Export, Git Sync and Auth handler source; source regex is not a general runtime inventory.

Required Gin parity test shape:

1. Build the full `gin.Engine` with every optional Handler non-nil but services allowed to fail closed.
2. Call `Engine.Routes()` before serving requests.
3. Convert Gin `:param` segments to OpenAPI `{param}`. This should be a syntax conversion only because registration names are already canonical snake_case.
4. Parse OpenAPI with `encoding/json`, collect only HTTP operation keys.
5. Compare exact sorted `METHOD path` sets and assert exactly 182.
6. Build a second engine with Metrics Handler and assert the only set difference is `GET /metrics`.
7. Keep behavioral tests for NoRoute, NoMethod and static fallback because those are not returned by `Routes()`.

Do not exclude `/livez` or `/readyz` from the 182 comparison: both are OpenAPI operations. The only runtime-only route is optional `/metrics`.

### 7. File-Exclusive Migration Batches

The following ownership keeps source files disjoint. Shared files stay with the main session. Domain lanes may run in parallel after the Gin bridge/dependency exists, but the whole repository can temporarily fail to compile until app composition and every `Routes` signature are integrated. Do not commit or deploy such an intermediate state, and do not add a dual production Router merely to keep interim commits green.

#### Batch 0: Main-session baseline and shared bridge

Owned files:

- `go.mod`, `go.sum`, `vendor/**`
- `internal/httpapi/**`
- new runtime route inventory fixture/test

Required work:

- Add Gin dependency without deleting Chi until all direct imports are migrated.
- Implement/test Gin -> stdlib Handler bridge and validator helper.
- Freeze exact OpenAPI operation set and add path-value/Flusher/context tests.

Validation:

```bash
go test ./internal/httpapi
go test -race ./internal/httpapi
```

#### Batch 1: Main-session engine, auth and app integration

Owned files:

- `internal/app/**`
- `internal/auth/http/**`
- `api/openapi/check.mjs` (incremental merge only)

Required work:

- Migrate Engine/global middleware/Auth grouping/404/405/static/metrics.
- Canonicalize safe route-template logging.
- Update all app router tests, including route parity and missing edge baselines.
- Remove Chi-specific Export/Git Sync regex checks only after runtime parity supersedes them, while retaining their capability/schema checks.

Validation:

```bash
go test ./internal/app ./internal/auth/http
go test -race ./internal/app ./internal/auth/http
make openapi-check
```

#### Batch 2: Foundation, indexing and graph lane

Owned files:

- `internal/workspace/http/**`
- `internal/ingestion/http/**`
- `internal/retrieval/http/**`
- `internal/graph/http/**`

Validation:

```bash
go test ./internal/workspace/http ./internal/ingestion/http ./internal/retrieval/http ./internal/graph/http
```

Extra assertions: `/workspaces/active` vs dynamic workspace route, canonical `workspace_id`/`source_version_id`, strict Graph bodies, Graph/Candidate duplicate route construction.

#### Batch 3: Workflow, change control, timeline and SSE lane

Owned files:

- `internal/workflow/http/**`
- `internal/changecontrol/http/**`
- `internal/changecontrol/application/approval_dispatch_river_smoke_integration_test.go`
- `internal/knowledge/http/**`
- `internal/events/http/**`

Validation:

```bash
go test ./internal/workflow/http ./internal/changecontrol/http ./internal/knowledge/http ./internal/events/http
go test ./internal/changecontrol/application -run '^TestApprovalDispatchRealRiverSafeWritebackSmoke$'
```

Extra assertions: canonical `report_id`, header-bound Workflow Workspace, SSE `Flusher`, heartbeat, Last-Event-ID, cancellation and response-started behavior.

#### Batch 4: Collection, health and export lane

Owned files:

- `internal/collection/http/**`
- `internal/health/http/**`
- `internal/export/http/**`

Validation:

```bash
go test ./internal/collection/http ./internal/health/http ./internal/export/http
```

Extra assertions: exact replay, bounded history, strict body/query, attachment download headers and ZIP streaming. The Collection replay integration test remains in this lane.

#### Batch 5: Conversation and content-production lane

Owned files:

- `internal/conversation/http/**`
- `internal/artifact/http/**`
- `internal/authoring/http/**`
- `internal/capture/http/**`

Validation:

```bash
go test ./internal/conversation/http ./internal/artifact/http ./internal/authoring/http ./internal/capture/http
```

Extra assertions: ETag, strict command bodies, Artifact replay, Authoring 10 MiB boundary, Capture multipart and duplicate JSON keys.

#### Batch 6: Organizing and local integration lane

Owned files:

- `internal/organizing/http/**`
- `internal/documenthistory/http/**`
- `internal/gitsync/http/**`

Validation:

```bash
go test ./internal/organizing/http ./internal/documenthistory/http ./internal/gitsync/http
```

Extra assertions: material PATCH/DELETE, strict restore bodies, Git no-store and token redaction. This lane must not edit `api/openapi/check.mjs`; that remains Batch 1 ownership.

#### Batch 7: Review, learning and memory lane

Owned files:

- `internal/review/http/**`
- `internal/review/learningpath/http/**`
- `internal/review/interview/http/**`
- `internal/memory/http/**`

Validation:

```bash
go test ./internal/review/http ./internal/review/learningpath/http ./internal/review/interview/http ./internal/memory/http
```

Extra assertions: Review group no-store middleware, Session/Interview route separation, Auth Principal owner binding, Learning Path's new package-level tests.

#### Batch 8: Main-session dirty-overlap and composition tests

Owned files:

- `internal/modelsettings/http/handler.go`
- `internal/modelsettings/http/handler_test.go`
- all three Chi-dependent `cmd/api` test files
- `api/openapi/openapi.json` only if evidence proves a contract change is required (none is expected for a framework-only migration)

Validation:

```bash
go test ./internal/modelsettings/http ./cmd/api
make openapi-check
```

This batch is main-session-only because Model Settings and OpenAPI already contain user changes.

#### Batch 9: Cleanup and full integration

Validation/search gates:

```bash
rg -n 'github\.com/go-chi/chi|\bchi\.' --glob '!vendor/**' .
go test ./cmd/api ./internal/app ./internal/auth/http ./internal/events/http ./internal/capture/http ./internal/export/http
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/api
make openapi-check
go mod tidy -diff
go mod vendor
go list -mod=vendor ./...
```

Expected first `rg`: no non-vendor hits. After vendor regeneration, `rg -n 'go-chi/chi' go.mod go.sum vendor` must also have no hits. Domain/Application/Repository/Workflow packages must have no Gin import; the one intentional `internal/changecontrol/application/..._test.go` Router smoke should be migrated without adding Gin to production Application code.

### 8. Current Dirty-Worktree Overlap

The Trellis researcher role forbids Git operations. The following list is authoritative input from the main session's `git status --short` on 2026-08-11:

Direct overlap, main-session-only ownership:

- `api/openapi/check.mjs`
- `api/openapi/openapi.json`
- `internal/modelsettings/http/handler.go`
- `internal/modelsettings/http/handler_test.go`

Related existing changes that must remain untouched unless independently required:

- `.trellis/spec/backend/model-settings-runtime.md`
- `internal/modelsettings/runtime/models.go`
- `internal/platform/models/chat_contract_test.go`
- `internal/platform/models/chat_http.go`
- `internal/platform/models/chat_openai.go`
- `internal/platform/models/embedding_http.go`
- `internal/platform/models/connection_diagnostic.go`
- `internal/platform/models/connection_diagnostic_test.go`
- Web Model Settings files
- untracked `.trellis/tasks/08-11-docker-project-restart-namespace`, `.trellis/tasks/08-11-model-connection-test-feedback`, `.workbuddy`

No domain migration sub-agent should edit the four direct-overlap files. Before/after incremental edits, the main session must inspect and preserve the user's Model Connection diagnostics/OpenAPI changes; whole-file replacement or regeneration without a three-way review is unsafe.

### 9. Baseline Evidence

Executed from repository root on 2026-08-11:

```bash
go test ./internal/app ./internal/artifact/http ./internal/auth/http ./internal/authoring/http \
  ./internal/capture/http ./internal/changecontrol/http ./internal/collection/http \
  ./internal/conversation/http ./internal/documenthistory/http ./internal/events/http \
  ./internal/export/http ./internal/gitsync/http ./internal/graph/http ./internal/health/http \
  ./internal/ingestion/http ./internal/knowledge/http ./internal/memory/http \
  ./internal/modelsettings/http ./internal/organizing/http ./internal/retrieval/http \
  ./internal/review/http ./internal/review/interview/http ./internal/review/learningpath/http \
  ./internal/workflow/http ./internal/workspace/http
```

Result: pass; Learning Path reports `[no test files]`.

```bash
go test ./internal/httpapi
```

Result: pass (`ok`, cached).

```bash
go test ./cmd/api
```

Result: pass (`ok`, 1.305s in this run).

```bash
make openapi-check
```

Result: `OpenAPI contract check passed`.

These runs used the current working tree, including existing Model Settings/OpenAPI changes. Most HTTP package results came from Go's test cache; they prove the present source hash has passing cached results, not a fresh `-count=1` or race run.

## Files Found

- `docs/roadmap.md:63` — TODO 5 goal, immutable contracts and staged migration rule.
- `internal/app/router.go:98` — unique production Router composition root.
- `internal/app/recovery.go:17` — project panic recovery and redaction contract.
- `internal/auth/http/handler.go:96` — open/protected Auth routes, Middleware and explicit Capability map.
- `internal/httpapi/response.go:1` — shared JSON/Problem stdlib boundary suitable for a thin Gin bridge.
- `internal/events/http/handler.go:48` — SSE protocol boundary.
- `api/openapi/openapi.json` — public method/path contract, currently 154 paths/182 operations.
- `api/openapi/check.mjs:1` — OpenAPI checker, including two Chi-source regex inventories.
- `go.mod:3` — Go 1.25.4, Chi v5.3.1 and validator v10.30.3.
- The 25 Handler registration files are enumerated in “Complete Production Route Inventory”.
- The 31 Chi-dependent test files are enumerated in “Chi Test Helper Inventory”.

## Code Patterns

- **Registration:** package `Routes(chi.Router)` registers relative `/api/v1` paths; app owns the prefix and protection group (`internal/app/router.go:160-179`).
- **Param extraction:** route handlers use `chi.URLParam`, with mixed camelCase/snake_case keys; app uses `chi.RouteContext` only for safe route logging (`internal/app/router.go:542-551`).
- **HTTP isolation:** business methods accept stdlib request/writer and call Application interfaces, so the framework need not cross the HTTP layer.
- **Error owner:** each handler maps stable Foundation/Application errors to project Problem JSON; Gin bind/recovery output is not a compatible substitute (`internal/httpapi/response.go:17-53`).
- **Strict decode:** high-risk modules retain local byte/depth/array/string/null policies around `strictjson`; consolidation must preserve policy arguments, not flatten them into one default binder.
- **Auth:** concrete URL path plus explicit method/pattern table owns Capability; unknown mutations fail closed to `WRITE_KNOWLEDGE` (`internal/auth/http/handler.go:173-180`).
- **Streaming:** SSE depends only on stdlib `http.Flusher` and context cancellation, which the Gin bridge can preserve if tested.

## External References

- Gin route introspection: https://github.com/gin-gonic/gin/blob/master/_autodocs/api-reference/engine.md — `Engine.Routes()` returns method, path and handler information.
- Gin v1.12.0 router interfaces: https://github.com/gin-gonic/gin/blob/v1.12.0/routergroup.go — `IRouter`, `IRoutes`, groups and uppercase method registration.
- Gin v1.12.0 radix conflicts: https://github.com/gin-gonic/gin/blob/v1.12.0/tree.go — wildcard mismatch panics during `addRoute`; exact/static branches are preferred over wildcard branches.
- Gin v1.12.0 writer: https://github.com/gin-gonic/gin/blob/v1.12.0/response_writer.go — Writer implements `http.ResponseWriter`, `http.Flusher`, status/size/written tracking and unwrap.
- Documentation was resolved through Context7 as `/gin-gonic/gin` (high-reputation official repository) and checked against the v1.12.0 tagged source.

## Related Specs

- `.trellis/spec/backend/index.md` — backend pre-development and full quality gates.
- `.trellis/spec/backend/auth-security.md:7-54` — Auth route, Cookie, Bearer priority, Origin/CSRF, Capability and readiness tests.
- `.trellis/spec/backend/error-handling.md:38-56` — stable Problem and SSE response contract.
- `.trellis/spec/backend/quality-guidelines.md` — API/SSE/security/full delivery checks.
- `.trellis/spec/backend/directory-structure.md` — Gin must remain at Presentation/Composition boundary.
- `.trellis/spec/guides/cross-layer-thinking-guide.md:19-50` — exact boundary and validation ownership mapping.
- `docs/architecture/application-contracts.md:61-77` — SSE is notification, REST remains source of truth.
- `docs/architecture/quality.md:20-40`, `docs/architecture/quality.md:206-214` — identity/capability/security and API/SSE test matrix.
- `.trellis/tasks/08-11-gin-http-migration/prd.md` and `design.md` — current planned boundary and acceptance criteria.

## Caveats / Not Found

- No Git command was run by this researcher; dirty-file evidence came from the main session as required by the role restriction.
- No full PostgreSQL, Docker, browser, full-repository race or uncached HTTP test was run during this research.
- No current Go runtime/OpenAPI route parity test exists; the 179+3 equality is a static inventory backed by registration lines and structured OpenAPI count.
- No package-level Learning Path HTTP test exists.
- Existing tests do not appear to freeze all trailing-slash, repeated-slash, encoded-path, HEAD/OPTIONS or protected-route 405 ordering behaviors; add these before claiming behavioral parity.
- Gin is not yet in the current `go.mod`, so wildcard conflict was verified from official v1.12.0 source rather than a local Gin engine reproduction.
- The task implementation plan currently says Phase 5 should exclude `/livez` and `/readyz` from runtime parity. That must be corrected: both belong to the 182 OpenAPI operations; only optional `/metrics` is excluded/differenced.
