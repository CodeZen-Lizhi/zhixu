# M7-02 Semantic Link Candidates Implementation Plan

## Execution Rules

- 先冻结 Candidate/typed Proposal vocabulary 与 migration contract，再实现 discovery、write seam、HTTP、Workflow、前端和评测。
- 每项开始前读取任务 PRD/design、相关 specs 和现有代码；测试先行，公共迁移/OpenAPI/Proposal 模型由主 Agent 统一整合。
- Candidate 不写 `core.relation`；只有 typed Proposal Approval apply 可调用 Knowledge 正式写入。
- 现有 Graph Query 与 file Safe Writeback 必须持续回归；任何 Candidate 依赖失败都不能拖垮它们。
- 每项完成后更新状态，执行 focused test、受影响构建、diff review，并同步权威文档。

## Task Table

| ID | Task | Likely files/modules | Depends on | Acceptance and verification | Risk | Status |
|---|---|---|---|---|---|---|
| T01 | Freeze Candidate/Proposal vocabulary, state machine, fingerprint and wire limits | task docs, `docs/architecture/CONTEXT.md` | none | no TBD/duplicate terms; task validate; fingerprint/state scenarios reviewed | semantic drift | completed: planning artifacts converged; Candidate/Evidence/Fingerprint/Relation Proposal glossary recorded; context manifests validate |
| T02 | Add Candidate domain and application contracts | `internal/graph/domain`, `internal/graph/application` | T01 | unit tests cover canonical endpoints/versions, all discovery methods, fingerprint sensitivity/order, states, decision reasons, CAS contracts and result validation | Candidate leaks into Relation | completed: domain/application race tests pass; fingerprint/state/decision/result contracts verified |
| T03 | Add `00024` Candidate tables and typed Proposal Expand migration | `migrations/**`, migration tests | T01,T02 | empty/upgrade/repeat/Down-Up/guarded Down; historical file rows unchanged; constraints reject mixed/fake payload; concurrent fingerprint unique | migration breaks M5 | completed: all SemanticLinkCandidates migration integration cases pass against disposable PostgreSQL |
| T04 | Implement Candidate PostgreSQL Repository and bounded queries | `internal/graph/adapter/postgres` | T02,T03 | real PG upsert/concurrency, suppression/reopen, append-only decisions, cursor window, Workspace/CAS/idempotency, batch Evidence/no N+1, EXPLAIN | noisy duplicates | completed: lifecycle/batch hydration and fingerprint concurrency integration pass |
| T05 | Implement typed Relation Proposal domain/repository compatibility | `internal/changecontrol/domain|application|adapter/postgres` | T02,T03 | file proposal full regression; typed canonical change hash/Revision/Approval; exact replay; file dispatch rejects/routes knowledge type | second proposal seam | completed: typed compatibility integration plus Change Control race regression pass |
| T06 | Implement Candidate confirm UoW and Approval-to-Knowledge apply | Graph/Change Control/Knowledge adapters/app | T04,T05 | Candidate→one Proposal; batch items independent; approval rechecks versions/Evidence; one canonical Confirmed Relation; stale→needs_revision; response-loss replay | cross-module half commit | completed: Approval service invokes idempotent Knowledge seam; Confirm/apply/stale/rollback/response-loss PostgreSQL integration pass |
| T07 | Implement deterministic discovery signals and Agent/Retrieval adapters | Graph discovery adapter, Retrieval/Agent ports | T04 | six source signals have real data path or explicit unavailable; excludes existing relation/proposal; bounded batch; NEW/LOW_CONFIDENCE never persist; frozen generation versions | model noise/N+1 | completed: four deterministic signals execute from bounded Topic pages; Semantic/RAG are explicit unsupported capabilities; formal Relation/active Proposal exclusions and rule-only Candidate persistence integration pass |
| T08 | Implement durable Topic scan Workflow/River | Graph/Workflow/River, `cmd/worker` | T04,T07 | 202 run, checkpoints/counters, 100-node pages, cancel/retry/lease reclaim/provider fault/response-loss; no duplicate candidates | long task recovery | completed: Worker registry/definition wired; bounded checkpoint/empty/retry/final/binding tests pass; real River success, transient recovery, exhausted fault, cancellation convergence and completion-response-loss replay pass without duplicate Candidate/formal Relation |
| T09 | Add Candidate/typed Proposal HTTP and OpenAPI | Graph/Change Control HTTP, router, `api/openapi` | T04-T08 | strict routes, Problem codes, cursor, 404 isolation, idempotency/version, discriminated Proposal compatibility, OpenAPI gate | public wire drift | completed: Candidate/decision/Topic scan strict HTTP and typed Proposal compatibility are declared; handler/router tests and OpenAPI gate pass |
| T10 | Wire production dependencies and separate readiness/status | `cmd/api`, `cmd/worker`, `internal/app`, config | T08,T09 | Candidate missing/provider down returns own 503/status while seven Graph queries and readiness remain healthy; no fake service | Graph outage coupling | completed: API/Worker use real repositories, frozen registries and composite cancellation guards; Candidate failure remains isolated from formal Graph readiness/status tests |
| T11 | Add strict frontend semantic-link client and query/mutation hooks | `web/src/api`, `web/src/features/graph` | T09 | unknown decoders, Workspace keys, pagination, scan refresh recovery, mutation invalidation and all Problem paths tested | cache shows false success | completed: strict Topic-only scan/Candidate/Proposal decoders, Workspace cache keys, cursor pagination, polling and mutation invalidation tests pass |
| T12 | Implement accessible Graph Candidate panel | Graph components/styles/routes | T11 | cards/evidence/actions/reopened message/grouping; Candidate never drawn as formal edge; desktop/mobile/keyboard/focus/error/empty states pass | visual fact confusion | completed: independent Candidate panel supports evidence, grouping, all decisions, reopened messaging, focus loop and durable Topic scan; component tests pass and GraphCanvas receives no Candidate edge |
| T13 | Add integration, fault, eval and browser smoke | integration fixtures, `eval`, `Makefile`, scripts | T06-T12 | public Candidate→Proposal→Approval→Relation; ignore/no-reappear/reopen; real River faults; five frozen metrics; desktop/mobile browser; secret/body scan | smoke bypass/unstable eval | completed: `semantic-link-integration`, `semantic-link-fault-smoke`, `semantic-link-eval` and `semantic-link-browser-smoke` pass; eval reports four quality metrics at 1 and ignored reappearance at 0 as `DETERMINISTIC_FAKE`; real Topic scan restores Claim-pair Candidates by membership on desktop/mobile without console/overflow or formal Graph pollution |
| T14 | Sync authoritative docs/specs and run full gate | `README.md`, `docs/**`, `.trellis/spec/**`, parent/task docs | T01-T13 | docs match actual scope/commands; parent M7-01 done/M7-02 done; commands below pass | false completion | completed: authoritative docs/specs and parent status match Topic membership/status URL/browser behavior; migration race, semantic integration/fault/eval/browser/composite smoke, full Go race/vet/tidy, `make test`, frontend lint/typecheck/282 tests/build, OpenAPI, Trellis validate and diff checks pass |
| T15 | Independent review, fixes, commit and archive | full diff/Trellis/Git | T14 | Go+SQL+frontend cross-layer two-round max; all P0-P2 closed; scoped commits/archive; no push unless requested | review blind spot | completed: main Go/general/SQL review and two-round independent backend/frontend review closed the eval pre-exclusion and cross-Topic scan recovery findings; all focused/full/database/browser gates pass，工作提交 `13bf907`/`f452525` 已创建并按 Trellis 流程归档，不 push |

## Checkpoint A - Domain, migration and persistence (T01-T04)

```bash
go test -race ./internal/graph/domain ./internal/graph/application
ZHIXU_TEST_DATABASE_URL='postgres://...' go test -race -tags=integration -count=1 -p 1 ./internal/graph/adapter/postgres
go test ./internal/platform/migration
```

## Checkpoint B - Typed proposal and formal write (T05-T06)

```bash
go test -race ./internal/changecontrol/... ./internal/knowledge/...
ZHIXU_TEST_DATABASE_URL='postgres://...' go test -race -tags=integration -count=1 -p 1 ./internal/changecontrol/... ./internal/knowledge/...
make writeback-fault-smoke
```

## Checkpoint C - Discovery, workflow and API (T07-T10)

```bash
go test -race ./internal/graph/... ./internal/agent/... ./internal/retrieval/... ./internal/workflow/...
ZHIXU_TEST_DATABASE_URL='postgres://...' make semantic-link-integration
ZHIXU_TEST_DATABASE_URL='postgres://...' make semantic-link-fault-smoke
make openapi-check
```

Target names are introduced by T13; before then use the underlying focused `go test` commands and do not report missing targets as passed.

## Checkpoint D - Frontend and delivery (T11-T15)

```bash
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web
npm run build --prefix web
ZHIXU_TEST_DATABASE_URL='postgres://...' make semantic-link-integration
ZHIXU_TEST_DATABASE_URL='postgres://...' make semantic-link-fault-smoke
ZHIXU_TEST_DATABASE_URL='postgres://...' make semantic-link-smoke
go test -race -count=1 ./...
go vet ./...
go mod tidy -diff
make test
python3 ./.trellis/scripts/task.py validate 07-21-semantic-link-candidates
git diff --check
```

## Review Gates

- Main Agent: `go-review` + `code-review-and-quality`; migration/SQL additionally `sql-code-review`。
- Mandatory independent read-only reviewers for Go/SQL backend and frontend/cross-layer behavior because the task changes public API, shared Proposal/Approval, migration, Workflow and formal Relation writes。
- Review evidence covers requirements, logic, edge cases, code quality, tests and actual runtime; verified current-scope P0-P2 issues are fixed and re-run,最多两轮复验。

## Rollback Points

- T02/T04: disable Candidate service/routes; Candidate tables remain non-formal and do not affect Graph facts。
- T03/T05: retain additive typed columns and default `file_patch`; revert application routing only, never rewrite historical Proposal rows。
- T06: disable `knowledge_change` approval/apply; keep Proposal/Candidate for later forward fix，不能删除已确认 Relation。
- T08/T10: unregister Candidate scan definition/executor and stop its queue consumption; existing Worker definitions remain。
- T12: remove Candidate panel/assets while Graph Global/Local/Path remains。
- Release: roll back binary/assets; preserve Candidate/Decision/Proposal/Relation data and ship forward migration/fix。
