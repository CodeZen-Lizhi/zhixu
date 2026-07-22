# M7-03 Smart Collection 与健康扫描实施计划

## Ordered Tasks

| ID | Work | Primary files | Depends | Exit evidence | Main risk | Status |
|---|---|---|---|---|---|---|
| T01 | Freeze Collection AST, registry, canonical hash and view config domain | `internal/collection/domain/**` | none | unit tests cover depth/node/size/type/operator/sort/view invariants | query injection / split semantics | completed |
| T02 | Freeze Health Issue, observation, fingerprint, severity, decision, scan and schedule domain | `internal/health/domain/**` | none | state/fingerprint/reopen/resolve/coverage tests pass | duplicate/reopened spam | completed |
| T03 | Add guarded `00025` migration and indexes | `migrations/**`, migration tests | T01,T02 | up/repeat/down/guarded-down/constraint tests on real PG | weak polymorphic/workspace constraints | completed: scope version/hash 与 Workspace 复合绑定、exact command receipt、partial/defer、append-only/guarded Down 约束通过真实 PostgreSQL；Domain race/vet、完整 migration gate、主 Go/SQL review 与独立两轮复验通过，无未关闭 P0-P2 |
| T04 | Implement Collection persistence, idempotent lifecycle and registry validation | `internal/collection/adapter/postgres`, `application` | T01,T03 | create/update/archive/replay/CAS/cross-workspace integration | saved query second fact | completed: canonical lifecycle service/PG adapter、receipt exact replay/payload conflict、CAS、Workspace 隔离与 archive immutable；race/unit/vet/真实 PG integration 通过 |
| T05 | Implement parameterized unified read model, preview/count, cursor and hydration | collection application/postgres | T01,T03 | all fields/operators, stable page/stale cursor, fixed query count, EXPLAIN | unbounded/N+1/query injection | completed: registry 全字段/operator 真实 PostgreSQL 矩阵、literal LIKE、nullable keyset、HMAC cursor invalid/stale/restart、Preview 6/Saved 7 条固定查询、批量 hydration 与 Topic/Claim/Relation/Health 索引 EXPLAIN 均通过；516-item 参考 fixture 25 次采样 P95 5.627375ms |
| T06 | Implement detector registry and real supported detectors | `internal/health/application`, detector adapters | T02,T03 | deterministic positive/negative fixtures and unavailable coverage | false clean result | completed: 稳定 detector registry、Review/Smart Collection/Directory unavailable coverage、9 类首版批量 keyset reader、identity observation 转换；unit/race/vet 通过 |
| T07 | Implement Issue reconciliation, decisions, evidence and repair option seam | health application/postgres, change control adapter | T02,T03,T06 | unchanged/reopen/ignore/resolve/CAS/idempotency/proposal tests | direct knowledge write/fake proposal | completed: Issue identity/fingerprint reconciliation、Evidence/observation history、decision CAS/exact replay、complete-only resolve 与 unavailable repair seam 已通过 Health race/vet、真实 PostgreSQL lifecycle/concurrency integration 和独立审计 |
| T08 | Implement durable Health Scan Workflow/River, cancellation and schedule dispatcher | health workflow/river, workflow composition | T06,T07 | success/partial/retry/cancel/restart/response-loss/missed-once tests | River as fact source | completed: durable Scan/coverage/checkpoint、Workflow/River retry/cancel/restart/response-loss、schedule missed-once/claim-reclaim/Ack 已通过 Health race/vet 与真实 PostgreSQL/River；scope advisory lock 已移除 NUL text 并以不同幂等键并发回归证明只生成一套 Scan/Workflow/Node/River job |
| T09 | Wire affected-scope outbox/event triggers without coupling source commits | Knowledge/Relation/Conflict/Index adapters, health dispatch | T08 | committed source change survives dispatch failure and later scan starts once | rollback coupling/duplicate scans | completed: typed/versioned Knowledge、Index、vector 与 lexical committed-change outbox 已替代废弃 workflow affected-scope 第二事实源；Worker 使用 `AffectedChangePlanner/Repository/Dispatcher`，source commit rollback、dispatch failure/restart、concurrent claim、active-scan backoff、schema/source poison、commit-response-loss、exact replay 与 production composition 均通过 fresh PostgreSQL race integration，cleanup 无 receipt/retrieval 泄漏 |
| T10 | Add strict Collection/Health HTTP, OpenAPI, status and production composition | `internal/*/http`, `internal/app`, `cmd/api`, `cmd/worker`, OpenAPI | T04-T09 | strict JSON/Problem/404/503/public process tests | route/readiness coupling | completed: Collection/Health strict JSON/query/Problem/404 防枚举、snake_case DTO、独立 system status 与 API/Worker composition 已通过 HTTP race/vet、cmd/app tests、OpenAPI check 和独立审计；T11/T15 已补齐跨模块和最终门禁 |
| T11 | Extend Semantic Link scan to real SMART_COLLECTION scope | graph domain/application/postgres/http/workflow/OpenAPI | T04,T05,T08 | Collection->Candidate scan with version/revision/replay/fault tests | arbitrary JSON scope / stale membership | completed: durable Collection binding、typed v2 Workflow input、跨页 keyset/restart、replay/response-loss、version/query/revision drift fail-closed 与真实 PG/River composition 通过 |
| T12 | Add strict Web clients, query keys, hooks, URL state and cache isolation | `web/src/api/{collections,health}.ts`, feature queries | T10 | decoder/key/url/workspace tests | browser as second fact source | completed: Collection/Health strict unknown decoder、Workspace/canonical Query keys、URL recovery、cursor/cache isolation 和 SSE invalidation 测试通过 |
| T13 | Build Collection list/detail, Query Builder and three views | `web/src/features/collections/**`, routes/styles | T12 | same refs in list/table/card; count/columns/sort/group/a11y tests | three filtering implementations | completed: `/collections`、`/collections/:id`、Query Builder、count、LIST/TABLE/COMPACT_CARD 同一结果 refs、列/排序/分组与移动端无溢出真实 browser smoke 通过 |
| T14 | Build Knowledge Health overview, Issue/Evidence/decision/repair and scan recovery | `web/src/features/health/**`, routes/styles | T12 | overview/trend/coverage/lazy evidence/focus/refresh tests | system-health confusion / false success | completed: `/health` summary/issues/evidence lazy load、coverage/partial、decision/repair、scan refresh recovery、桌面/移动 browser smoke 与零 console warning/error 通过 |
| T15 | Add migration/integration/fault/performance/browser/secret gates | Makefile, deploy scripts, fixtures, e2e | T03-T14 | all commands below pass on real PG/River/API/browser | smoke bypass / fixture leakage | completed: migrations `00025`–`00029`、Collection/Health/Graph/API/Worker real PG integration、fault、benchmark/EXPLAIN、browser、OpenAPI、secret、bash、Go/Web full gates 通过；npm audit high gate 通过（DOMPurify moderate advisory 保留） |
| T16 | Sync authoritative docs/spec/parent status, run full gate and two-round review | docs, `.trellis/spec/**`, parent/task docs | T15 | no P0-P2; docs match actual capabilities and deferrals | false completion | completed: 权威 docs/spec 已同步；主 Agent `go-review`、`code-review-and-quality`、`sql-code-review` 通过；backend/SQL reviewer 两轮与 frontend/cross-layer reviewer 最终复验无未关闭 P0-P2；反向 Workspace move 锁顺序风险已修复并由真实 PG race/integration 复验 |
| T17 | Create scoped commits, archive task and record journal | Git/Trellis | T16 | clean worktree, task completed/archived, no push | mixed commits / premature archive | pending |

## Checkpoint A - Domain And Migration (T01-T03)

Required tests before adapters:

```bash
go test -race -count=1 ./internal/collection/domain ./internal/health/domain
go test -tags=integration -count=1 -run 'TestSmartCollectionHealthMigration' ./internal/platform/migration
go vet ./internal/collection/... ./internal/health/...
git diff --check
```

Stop conditions:

- AST can reach SQL without a registry-owned identifier.
- Health fingerprint cannot distinguish unchanged evidence from new detector/object versions.
- Migration allows duplicate active logical Issue, invalid JSON/version/status, or unguarded destructive Down.

## Checkpoint B - PostgreSQL And Application (T04-T07)

Required coverage:

- Collection lifecycle exact replay and idempotency conflict.
- Query fields/operators AND/OR depth, deterministic sort, count, cursor invalid/stale and cross Workspace.
- List/table/card view config never changes result membership.
- Detector positive/negative cases, fixed batch query count and explicit unavailable coverage.
- Issue create/verify/reopen/ignore/false-positive/defer/resolve, scan complete vs partial, Evidence binding and real/unavailable repair options.

Commands:

```bash
go test -race -count=1 ./internal/collection/... ./internal/health/...
ZHIXU_TEST_DATABASE_URL='postgres://...' go test -race -tags=integration -count=1 -p 1 ./internal/collection/... ./internal/health/...
ZHIXU_TEST_DATABASE_URL='postgres://...' go test -tags=integration -count=1 -run 'TestCollectionQueryPlan|TestHealthDetectorQueryPlan' ./internal/collection/... ./internal/health/...
```

## Checkpoint C - Workflow, HTTP And Cross-Module Scope (T08-T11)

Required scenarios:

- Health scan success, partial detector, retry exhaustion, cancel, worker restart and completion response-loss.
- Daily/weekly/cron disabled-by-default, missed-once and concurrent schedule claim.
- affected-scope dispatch failure leaves source commit intact and replays once.
- strict public Collection/Health API, 404 anti-enumeration and independent status/readiness.
- Collection scope Candidate scan binds ID/version/query/revision and still only creates Candidate.

Commands:

```bash
ZHIXU_TEST_DATABASE_URL='postgres://...' make collection-health-integration
ZHIXU_TEST_DATABASE_URL='postgres://...' make collection-health-fault-smoke
make openapi-check
go test -race -count=1 ./cmd/api ./cmd/worker ./internal/app ./internal/graph/... ./internal/workflow/...
```

## Checkpoint D - Frontend And Browser (T12-T14)

Required states:

- no Workspace, loading, empty, results, invalid AST, stale cursor, partial scan, dependency unavailable, version conflict and retryable failure.
- Collection list/detail, Query Builder and identical list/table/card refs.
- Health overview/trend, lazy Evidence, decision/repair modal and refresh-restored scan.
- Workspace switch clears old cache; URL holds only recoverable state; SSE only invalidates.
- desktop 1440x900 and mobile 390x844, zero console error/warning, no horizontal overflow, focus trap/return and keyboard actions.

Commands:

```bash
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web
npm run build --prefix web
ZHIXU_TEST_DATABASE_URL='postgres://...' make collection-health-browser-smoke
```

## Checkpoint E - Full Gate And Review (T15-T17)

```bash
go test -race -count=1 ./...
go vet ./...
go mod tidy -diff
make test
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web
npm run build --prefix web
ZHIXU_TEST_DATABASE_URL='postgres://...' make collection-health-integration
ZHIXU_TEST_DATABASE_URL='postgres://...' make collection-health-fault-smoke
ZHIXU_TEST_DATABASE_URL='postgres://...' make collection-health-benchmark
ZHIXU_TEST_DATABASE_URL='postgres://...' make collection-health-smoke
make openapi-check
python3 ./.trellis/scripts/task.py validate 07-21-smart-collection-health
git diff --check
```

Additional gates:

- `npm audit --prefix web --audit-level=high`.
- `bash -n` for every changed deploy script.
- secret/DSN/path/request-body scan over source, fixtures, logs and browser output.
- empty/upgraded/repeated migration and fixture cleanup on success/failure.
- Main Agent runs `go-review`, `code-review-and-quality`, and `sql-code-review`.
- Independent backend/SQL and frontend/cross-layer reviewers receive requirements, diff and actual command output; after fixes the same reviewers re-run one verification round, maximum two rounds.

## Documentation And Spec Updates

Before commit, synchronize only facts proven by implementation/tests:

- `docs/product/PRD.md` Collection/Health current delivery boundary.
- `docs/architecture/{database-design,module-architecture,api-and-events,frontend-architecture,performance,testing-and-evaluation}.md`.
- `docs/architecture/workflows/07-knowledge-health.md`.
- `.trellis/spec/backend/**` and `.trellis/spec/frontend/**` via `trellis-update-spec`.
- parent `07-16-product-delivery/implement.md` M7-03 status and implementation checklist.

Do not mark tag/review/directory, final 500k capacity, authentication, Artifact/Review action or M7-04 Timeline/Impact complete without their own evidence.

## Rollback Points

- Domain/HTTP before production composition: remove only unreferenced new module wiring; preserve task docs and migration evidence.
- Migration is forward-only in normal development; application rollback leaves additive `learning`/`ops` tables intact. Down is test-only and refuses when business rows exist.
- Health runtime: stop schedule dispatcher/Health executor to halt new scans; existing Issue/scan facts remain queryable.
- Collection scope Candidate path: disable only the new planner capability; never reinterpret it as Workspace scope.
- Frontend: routes can show capability unavailable while preserving strict API data; do not fall back to mock/local data.

## Completion Rule

T17 may run only after every AC in `prd.md` has direct evidence, all T01-T16 rows are completed, full gates and both independent review tracks are closed, and the worktree contains only intentional task bookkeeping. No push unless the user explicitly requests it.
