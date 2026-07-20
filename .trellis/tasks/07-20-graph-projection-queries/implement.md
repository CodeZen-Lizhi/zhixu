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
| T06 | Add Relation detail and lazy Evidence pages | Graph PG/application, Retrieval href seam | T02,T03 | bounded result-window Evidence cursor, per-evidence reason/applicability, immutable href, missing diagnostic, no body/path leak | provenance drift | in_progress |
| T07 | Prove query plans and add only required indexes | migration + integration/EXPLAIN tests | T03-T06 | representative plans avoid full scan on adjacency; repeat migration; rollback strategy | index guess/write cost | pending |
| T08 | Implement Graph HTTP/OpenAPI over application cursor | `internal/graph/http`, `api/openapi` | T03-T07 | strict JSON, node search, opaque cursor mapping, 404/405/503/timeout, discriminated schema, OpenAPI gate | wire drift | pending |
| T09 | Wire production API and readiness | `internal/app/router.go`, `cmd/api/main.go`, config tests | T08 | real dependencies only; missing config fail closed; existing routes unaffected | partial assembly | pending |
| T10 | Implement strict frontend Graph client/query hooks | `web/src/api/graph.ts`, `features/graph` | T08 | strict decoder, workspace query keys, server node search, global/local/path/detail/evidence query tests | duplicate parser | pending |
| T11 | Implement minimum real `/graph` page | routes, Graph components/styles | T10 | three modes, search/legend, URL recovery, session lock/fixed layout, bounded SVG/list, lazy drawer, all explicit states, keyboard/a11y | graph hairball | pending |
| T12 | Add public integration, browser and performance smoke | Graph integration/capacity fixture/Makefile | T09,T11 | public HTTP Global→Local→Path→Evidence; desktop/mobile browser; 100k Relation/20k Node, 5 warmup+30 sample p95 and EXPLAIN evidence | smoke bypass | pending |
| T13 | Sync authoritative docs and run full quality gate | `README.md`, product PRD, API/events, database, frontend, performance/testing docs, relevant specs/task | T01-T12 | commands below pass; each authority matches Topic/Claim scope, path/cursor/failure contract and M10 boundary | false completion | pending |
| T14 | Independent review, fix, commit and archive | full diff/Trellis/Git | T13 | Go+SQL+frontend cross-layer review, two rounds max, clean commit/archive, no push | review blind spot | pending |

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

`make graph-benchmark` 使用 `pgvector/pgvector:0.8.5-pg18-bookworm`、单客户端并发、默认 Compose 连接池/statement timeout，确定性生成 20k Node/100k Relation，执行 5 次预热和 30 次采样；输出 fixture seed/version、PostgreSQL/CPU/memory 元数据、原始样本、p95 与 EXPLAIN artifact，p95>1.5s 非零退出。M10 在 500k Relation 和正式资源预算下重跑最终容量门禁。

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
