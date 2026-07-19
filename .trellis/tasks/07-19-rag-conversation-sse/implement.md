# M6-04 RAG Conversation API And SSE Implementation Plan

## Execution Rules

- Run tasks in dependency order. Public DTOs, migrations and shared contracts are integrated by the main agent.
- Before each code task, reload the referenced docs/specs and search for existing helpers.
- Use TDD for domain, persistence, HTTP/SSE and frontend boundary behavior.
- After every checkpoint run focused tests and inspect the diff; no task is complete on compile-only evidence.
- Do not start M9/M10/M11 work under this task except for the additive `ops.server_event` contract explicitly needed by M6-04.

## Task Table

| ID | Task | Likely files/modules | Depends on | Acceptance and verification | Risk | Status |
|---|---|---|---|---|---|---|
| T01 | Freeze Conversation vocabulary and task contracts | `docs/architecture/CONTEXT.md`, task `prd/design/implement`, task manifests | none | no TBD; PRD convergence; task validation passes | scope drift | completed |
| T02 | Add Conversation/SSE and Agent v2 migration constraints | `migrations/00020_*`, migration tests, backend DB spec | T01 | empty/repeat Up; empty Down/Up; guarded data Down; FK/CHECK/immutability/CAS/idempotency plus additive PLAN/clarification phase/result tests on real PG | data loss, weak constraints | completed: real PostgreSQL migration, CAS, active Answer, Model Run binding, feedback eligibility and safe event projection gates pass |
| T03 | Implement Conversation/RAG v2 domain validation and hashes | `internal/conversation/domain/**`, `internal/agent/domain/**` | T02 contract | unit tests cover Question options, Query Plan, Clarification, RAG v2 related topics/follow-ups, scope/cursor/status/feedback/result publication, invalid UTF-8/NUL/size and exact hashes | duplicate rules | completed: canonical hashes, 16 KiB persistence bounds, shared retrieval mode matrix, PLAN call order, v1 compatibility and two-round review gates pass |
| T04 | Implement create/list/read/turn/answer repositories | `internal/conversation/application/**`, `adapter/postgres/**` | T02,T03 | real PG tests cover Workspace isolation, stable paging, bounded context, no N+1, exact replay/conflict and terminal Answer read projection | cross-workspace leak | pending |
| T05 | Implement persistent Server Event store and SSE handler | `internal/events/**`, `internal/app/**`, migration trigger | T02,T03 | handler tests cover headers/framing/heartbeat/cancel; PG tests cover monotonic order, replay, future/invalid/expired cursor and redacted payload | long-lived resource leak | pending |
| T06 | Implement atomic Question-to-Workflow dispatch | `internal/conversation/adapter/postgres/dispatch*`, Workflow StartTx reuse | T02-T04 | concurrent same-key/different-key tests; any injected failure rolls back Question/Answer/Run/Node/Event/Job; response-loss exact replay | cross-schema half state | pending |
| T07 | Add Query Plan/RAG Workflow contract and context loader | `internal/agent/adapter/workflow/rag_*`, Conversation execution port, Agent catalog | T03,T04,T06 | strict input/output tests; only IDs/hash in persisted input; bounded context/options and binding drift fail closed; PLAN/RAG v2/Clarification schemas and Definition registered | raw content leak | pending |
| T08 | Implement retrieval-first Query Plan and RAG executor | Agent workflow, Retrieval/Knowledge related-topic adapters, model catalog | T07 | tests cover clarification, 1..3 rewrites, persistent retrieval summary, no evidence refusal, RAG v2 related topic/follow-up, conflicts, gates, degradation, provider failure and no Tool Loop | hallucination, duplicate model call | pending |
| T09 | Implement atomic Model Run/Answer finalizer and replay | Agent PG Tx seam, Conversation PG finalizer | T08 | real PG concurrency/response-loss tests prove one terminal Answer/Refusal/Clarification, matching terminal Model Run, immutable result/summary and replay without Provider call | split terminal facts | pending |
| T10 | Implement Conversation/Answer/Feedback HTTP and OpenAPI | `internal/conversation/http/**`, `internal/app/router.go`, `cmd/api/main.go`, `api/openapi/**` | T04,T06,T09 | strict JSON, depth/format defaults and hashing, idempotency, cursor, ETag, 202/status URL, clarification/retrieval summary, Problem/404 anti-enumeration and 405 tests; `make openapi-check` | wire drift | pending |
| T11 | Wire production API/Worker composition and readiness | `cmd/api/**`, `cmd/worker/**`, config/readiness tests | T05-T10 | RAG definition/executor registered only with real dependencies; disabled/mismatch states fail closed; existing relation/tool workers unaffected | partial readiness | pending |
| T12 | Implement typed frontend Conversation/RAG clients and shared SSE thin layer | `web/src/api/**`, mandatory `web/src/events/**` | T05,T10 | strict decoders reject invalid UUID/time/status/envelope/RAG v2/clarification/summary; command keys replay; sole SSE owner covers parser/reconnect/expired fallback and targeted invalidation | duplicate decoder | pending |
| T13 | Implement minimal real RAG page | `web/src/features/rag/**`, routes, styles, component tests | T12 | create/select Conversation, depth/format, submit/clarify, stage/poll recovery, Answer/Refusal/conflict/Citation/summary/related topics/follow-ups/feedback; keyboard/live region; desktop/mobile stable layout | draft shown as final | pending |
| T14 | Add RAG integration and Compose smoke gates | `Makefile`, `deploy/compose-rag-smoke.sh`, Go integration tests, deterministic model fixture | T09-T13 | `make rag-integration` and `make compose-rag-smoke` traverse public API→River→Retrieval→Agent→Answer→SSE→Feedback and exact replay | smoke bypasses real seam | pending |
| T15 | Sync docs/specs and run full quality gate | `README.md`, architecture/API/testing/runbook/spec docs, task status | T02-T14 | all commands below pass; browser actually opens desktop/mobile; diff/review clean; docs match wire/schema/runtime | undocumented behavior | pending |
| T16 | Independent cross-layer/Go/SQL/frontend review, fix and reverify | full task diff | T15 | reviewer checks requirement completeness, logic, edges, quality, tests and runtime; all verified findings fixed; at most two re-review rounds | review blind spot | pending |
| T17 | Commit, archive and record journal | Trellis task/parent/journal, Git | T16 | focused implementation commit plus archive/journal commits; clean worktree except pre-existing unrelated changes; no push | lost task trace | pending |

## Detailed Execution Order

### Checkpoint A: Persistence foundation (T02-T05)

- Database constraints own Workspace bindings, idempotency and append/transition invariants.
- Domain owns canonical request/result validation and hashes.
- Repository queries use explicit columns, stable cursor bounds and batch reads.
- Event replay is proven before HTTP streaming begins.

Verification:

```bash
go test ./internal/conversation/... ./internal/events/...
ZHIXU_TEST_DATABASE_URL=... go test -race -tags=integration -count=1 -p 1 ./internal/conversation/... ./internal/events/...
go test ./internal/platform/migration
```

### Checkpoint B: Durable RAG backend (T06-T11)

- Question dispatch reuses Workflow StartTx; no hand-written duplicate Workflow inserts.
- RAG executor uses Search/Evidence/Knowledge/Agent ports and no persistent Search Tool invocation.
- Final publication transaction owns Model Run and Answer consistency.
- HTTP/OpenAPI expose only implemented behavior.

Verification:

```bash
go test ./internal/agent/... ./internal/conversation/... ./internal/events/... ./internal/app ./cmd/api ./cmd/worker
go test -race ./internal/agent/... ./internal/conversation/... ./internal/events/...
make openapi-check
```

### Checkpoint C: Frontend product slice (T12-T13)

- Wire DTOs are decoded once from `unknown`.
- SSE/polling only invalidate and refetch authoritative resources.
- No UI text claims Auth, Web evidence or full project completion.

Verification:

```bash
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web
npm run build --prefix web
```

### Checkpoint D: Real gates and finish (T14-T17)

```bash
go test -race ./...
go vet ./...
go mod tidy -diff
make test
make rag-integration
make openapi-check
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web
npm run build --prefix web
docker compose -f deploy/compose.yml config
make compose-rag-smoke
git diff --check
```

Browser verification must open the served application at desktop and mobile viewports, complete the RAG flow, inspect console/network failures, test citation/feedback and simulate SSE reconnect. A server-start-only check is insufficient.

## Test Matrix

| Layer | Normal | Boundary | Failure |
|---|---|---|---|
| Domain | create/submit/publish/feedback | max bytes, empty context, page limits | invalid UTF-8/NUL/status/result/citation |
| PostgreSQL | exact UoW and pagination | concurrency, 100 items, expiry edge | FK/workspace/CAS/idempotency/guarded Down |
| Workflow | one Run/Node/Job | retry/reclaim/replay | start failure, unknown result, cancel |
| Agent | Query Plan + RAG v2 + review | clarification, rewrites, related topics, follow-ups, conflict/inference/degraded retrieval | no evidence, unapproved, forged Topic, broken citation, review failure |
| HTTP | create/read/202/replay | cursor/ETag/empty page | strict JSON, 404, 409, 405, 503 |
| SSE | live event/replay/heartbeat | high watermark and batch boundary | invalid/future/expired ID, disconnect |
| Frontend | full user flow | empty/clarification/refusal/conflict/mobile | decoder failure, API error, reconnect/poll fallback |
| Smoke | public API to final feedback | replay and reconnect | injected fault proves no duplicate/half state |

## Rollback Points

- After T05: revert application code; retain empty additive migration or use Down only on empty test databases.
- After T11: disable new Question submission/RAG executor, drain current jobs and keep Conversation read APIs available.
- After T13: revert `/chat` route/static assets without changing backend facts.
- After release: switch to previous image/binary, keep `00020` data, and ship a forward fix. Never drop populated tables or rewrite Workflow/Git history.

## Review Gates

- Main agent must run `go-review` after Go/SQL changes and `code-review-and-quality` for cross-layer/frontend changes.
- SQL changes additionally require `sql-code-review`.
- Because this task changes public API, database, Workflow, concurrency, SSE, security and frontend, an independent read-only reviewer is mandatory. It receives requirements, diff, affected files and actual command results, then performs up to two verification rounds.
