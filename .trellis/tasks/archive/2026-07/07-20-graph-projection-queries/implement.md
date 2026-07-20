# M7-01 Graph Projection And Queries Implementation Plan

## Execution Rules

- 先公共 vocabulary/read model，再 SQL，再 HTTP，再前端；Graph 只读，不修改 Knowledge 写模型。
- 每项开始前读取相关 specs/代码，采用测试先行；公共 DTO、migration、OpenAPI 由主 Agent 整合。
- 先测 EXPLAIN 再决定索引；没有证据不新增 stored projection 或图数据库。
- 每项完成后执行 focused tests、lint/vet/build、diff review，并同步状态。

## Task Table

| ID | Task | Likely files/modules | Depends on | Acceptance and verification | Risk | Status |
|---|---|---|---|---|---|---|
| T01 | Freeze Graph vocabulary, DTO, limits and errors | task docs, `docs/architecture/CONTEXT.md` | none | no TBD; Topic/Claim-only and path/cursor semantics explicit; task validate | scope drift | completed: planning review closed all P1/P2; Graph Query Projection/Global/Local/Path glossary captured |
| T02 | Add Graph domain/application query and cursor contracts | `internal/graph/domain`, `internal/graph/application` | T01 | unit tests cover discriminated nodes, filters, closure, ordering, budgets, path continuity, canonical request and HMAC cursor invalid/stale behavior | second vocabulary | completed: discriminated read models、filter/direction恶意返回防线、bounded/truncated windows、Application-owned canonical HMAC cursor、closure/path validation与fail-closed Query Service通过race/vet及两轮独立审查 |
| T03 | Implement PostgreSQL global, node search and detail reads | `internal/graph/adapter/postgres` | T02 | real PG frozen cluster_score/confidence filters, exact/prefix Topic alias/Claim search, detail and result-window cursor tests; Workspace/status isolation; no Evidence eager load | aggregation scan | completed: canonical Knowledge SQL、过滤后聚合、500 窗口截断、严格搜索/详情、HMAC cursor stale 与真实 PostgreSQL race 覆盖通过两轮 Go+SQL 独立审查 |
| T04 | Implement bounded neighborhood queries | Graph PG adapter/application | T02,T03 | depth1 result-window cursor and depth2/3 cycle/budget tests on real PG; complete layers and no N+1 | recursive explosion | completed: MaxFrontier/center/topic过滤合同、双侧一跳500窗口、固定层批量frontier、完整层预算、闭包/对称/方向/cursor stale/cancel通过真实PG race与两轮独立审查 |
| T05 | Implement deterministic shortest path | Graph application/PG frontier seam | T02,T03 | batch bidirectional BFS proves all best-hop meetings, full path-key tie-break, directed/symmetric/filter/common-topic/no-path/cancel/budget; no per-node SQL | false shortest path | completed: 原子RR只读快照、薄frontier双向BFS、全meeting path-key决胜、批量edge/evidence hydrate、方向/对称/maxDepth/budget/common-topic/cancel通过真实PG race和两轮独立审查 |
| T06 | Add Relation detail and lazy Evidence pages | Graph PG/application, Retrieval href seam | T02,T03 | bounded result-window Evidence cursor, per-evidence reason/applicability, immutable href, missing diagnostic, no body/path leak | provenance drift | completed: 单语句owner/count/501窗口、逐Evidence语义/Provenance/confirmation、Retrieval href、cursor stale/500截断/非正式与跨Workspace NotFound通过真实PG race和独立审查 |
| T07 | Prove query plans and add only required indexes | migration + integration/EXPLAIN tests | T03-T06 | representative plans avoid full scan on adjacency; repeat migration; rollback strategy | index guess/write cost | completed: 1k+真实Relation/Evidence夹具对4个生产SQL执行ANALYZE/BUFFERS JSON计划，source/target/owner索引命中且目标表无Seq Scan；无证据新增00024，T12保留100k容量门禁 |
| T08 | Implement Graph HTTP/OpenAPI over application cursor | `internal/graph/http`, `api/openapi` | T03-T07 | strict JSON, node search, opaque cursor mapping, 404/405/503/timeout, discriminated schema, OpenAPI gate | wire drift | completed: 7 个严格 HTTP endpoint、Topic/Claim 判别联合、Workspace-scoped followable href、统一 Problem/OpenAPI gate、owned RR read snapshot 与真实 PostgreSQL statement timeout/cancel/404 回归通过两轮独立 Go+SQL 审查 |
| T09 | Wire production API and readiness | `internal/app/router.go`, `cmd/api/main.go`, config tests | T08 | real dependencies only; missing config fail closed; existing routes unaffected | partial assembly | completed: 真实 Graph Repository→随机 Cursor→Application→HTTP production composition、常驻 503 routes、Handler-owned readiness、system status/OpenAPI/前端严格契约、2s timeout 配置与 Compose 透传均通过 race/vet/真实 PG/生产 smoke/浏览器及两轮独立审查 |
| T10 | Implement strict frontend Graph client/query hooks | `web/src/api/graph.ts`, `features/graph` | T08 | strict decoder, workspace query keys, server node search, global/local/path/detail/evidence query tests | duplicate parser | completed: 7 endpoint 单一 strict decoder/client、Workspace/等价语义 query keys、cursor infinite queries、深层快照、搜索 gating、Evidence lazy load 与 Problem/network/Abort 经 186 项全前端测试、lint/typecheck/build 及两轮独立审查通过 |
| T11 | Implement minimum real `/graph` page | routes, Graph components/styles | T10 | three modes, search/legend, URL recovery, session lock/fixed layout, bounded SVG/list, lazy drawer, all explicit states, keyboard/a11y | graph hairball | completed: Global/Local/Path、服务端搜索、URL 恢复、显式锁定/固定布局、60/100 有界画布与完整列表、Evidence 延迟加载、错误/空/截断/无路径状态及移动 modal 焦点闭环通过 230 项前端测试、构建、桌面/移动浏览器 smoke 和两轮独立复验 |
| T12 | Add public integration, browser and performance smoke | Graph integration/capacity fixture/Makefile | T09,T11 | public HTTP Global→Local→Path→Evidence; desktop/mobile browser; 20k Active Topic/100k Confirmed IMPACTS/100k Evidence reference topology, 5 warmup+30 sample p95 and EXPLAIN evidence | smoke bypass | completed: 真实 PG 公共 HTTP、API 进程 smoke、cursor response-loss/stale、Workspace 隔离、timeout、20k Active Topic/100k Confirmed IMPACTS/100k Evidence、5+30 样本固定 6 statements、三类 EXPLAIN、0600 产物及桌面/移动浏览器验收通过；Mixed Topic/Claim 与 BELONGS_TO 正确性由功能集成覆盖，失败诊断保留与日志正文扫描经独立二轮复验 |
| T13 | Sync authoritative docs and run full quality gate | `README.md`, product PRD, API/events, database, frontend, performance/testing docs, relevant specs/task | T01-T12 | commands below pass; each authority matches Topic/Claim scope, path/cursor/failure contract and M10 boundary | false completion | completed: README、产品/API/数据库/前端/技术栈/性能/测试/workflow 与 Trellis 规范已同步；最新 100k Confirmed IMPACTS 参考拓扑 p95=7.445958ms；race/vet/tidy/make test/Graph 三门禁/task validate/diff check 通过，独立文档审查两轮问题已关闭 |
| T14 | Independent review, fix, commit and archive | full diff/Trellis/Git | T13 | Go+SQL+frontend cross-layer review, two rounds max, clean commit/archive, no push | review blind spot | completed: 独立 Go/前端跨层复验无未关闭 P0-P2；Evidence 缓存生命周期 P2 已修复并由 27 项 Graph 定向测试覆盖；主 Go/通用/SQL review、Trellis check、全量门禁、最新构建桌面/移动 smoke 通过；仅提交 Graph M7-01，不 push |

## Checkpoints

### A — Contract and database projection (T01-T07)

```bash
go test -race ./internal/graph/...
ZHIXU_TEST_DATABASE_URL=postgres://zhixu:quality@127.0.0.1:55432/zhixu?sslmode=disable go test -race -tags=integration -count=1 -p 1 ./internal/graph/...
go test ./internal/platform/migration
```

### B — Public API and frontend (T08-T11)

```bash
go test ./internal/graph/... ./internal/app ./cmd/api
make openapi-check
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web
npm run build --prefix web
```

### C — Delivery (T12-T14)

```bash
go test -race -count=1 ./...
go vet ./...
go mod tidy -diff
make test
make graph-integration
make graph-smoke
make graph-benchmark
python3 ./.trellis/scripts/task.py validate 07-20-graph-projection-queries
git diff --check
```

`make graph-benchmark` 使用 `pgvector/pgvector:0.8.5-pg18-bookworm`、单客户端并发、默认 Compose 连接池/statement timeout，确定性生成 20k Active Topic/100k Confirmed IMPACTS/100k Evidence 参考拓扑，执行 5 次预热和 30 次采样；输出 fixture seed/version、PostgreSQL/CPU/memory 元数据、原始样本、p95 与 EXPLAIN artifact，p95>1.5s 非零退出。该门禁不替代 Mixed Topic/Claim 与 BELONGS_TO 正确性的功能集成门禁，也不替代 M10 的 claim-heavy/mixed、500k Relation 和 FPS 门禁。

## Review Gates

- Main Agent: `go-review` + `code-review-and-quality`; migration/SQL additionally `sql-code-review`.
- Mandatory independent read-only reviewer because the task changes database query plans, public API, recursive/path algorithms, cursor security and frontend.
- Review checks requirements, logic, edge cases, quality, tests and actual runtime; verified P0-P2 are fixed and re-run.

## Rollback Points

- T02: remove unused Graph module; no data change.
- T07: revert additive indexes only after confirming no production dependency; never alter Relation facts.
- T09: disable/unregister Graph route while other API remains available.
- T11: remove `/graph` assets/routes without backend data changes.
- Release: roll back binary/assets; retain additive indexes and ship forward fix if cursor/API clients are already deployed.
