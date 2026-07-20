# Research: M7-01 codebase seams for graph projection and queries

- Query: 扫描现有 Knowledge/Relation 数据模型、迁移、Repository、HTTP/OpenAPI、前端路由和测试基础，判断 M7-01 可复用 seam 与缺口，并建议实现拆分。
- Scope: internal
- Date: 2026-07-20

## Findings

### 1. Files found

- `internal/knowledge/domain/model.go` — Topic、Claim 生命周期与稳定领域字段。
- `internal/knowledge/domain/relation.go` — Graph 节点引用、关系类型矩阵、对称边规范化、Relation/Evidence 聚合不变量。
- `internal/knowledge/domain/repository.go` — Knowledge 唯一事务 Repository；已有有界批量聚合读取，但没有图查询端口。
- `internal/knowledge/application/queries.go` — GetTopic/GetClaims/GetRelations 查询服务及结果排序/范围防御。
- `internal/knowledge/adapter/postgres/read.go` — Claim/Relation 批量一致读与 Evidence 批量加载。
- `internal/knowledge/adapter/postgres/read.go`、`scans.go` — 显式列扫描、聚合校验、同事务批量关联加载模式。
- `migrations/00017_knowledge_domain.sql` — Topic/Claim/Relation/Relation Evidence 唯一事实表、约束和邻接索引。
- `internal/platform/migration/knowledge_domain_integration_test.go` — 空库迁移、约束、并发生命周期锁、guarded Down 基础。
- `internal/knowledge/adapter/postgres/repository_integration_test.go` — 真实 PostgreSQL Knowledge 生命周期、Evidence 历史、批量查询基础。
- `internal/app/router.go`、`cmd/api/main.go` — HTTP handler composition seam；当前没有 Knowledge/Graph dependency。
- `internal/retrieval/http/handler.go`、`internal/retrieval/http/cursor.go` — 可借鉴的严格 JSON、Problem Details、HMAC cursor、Evidence Reference 延迟加载 HTTP seam。
- `api/openapi/openapi.json` — 当前真实 API 契约；没有 `/api/v1/graph` 路径或 graph schema。
- `web/src/routes/AppRoutes.tsx` — 当前只有 Workspace 与 Chat 路由，无 Graph 页面。
- `web/src/app/active-workspace.ts` — 可复用的 Workspace-scoped 前端状态 seam。
- `web/src/features/rag/queries.ts`、`query-keys.ts` — TanStack Query 的分页、query key、AbortSignal 模式。
- `web/src/test/render.tsx` — React Query + MemoryRouter 页面测试 fixture。
- `docs/architecture/workflows/06-graph-and-semantic-links.md` — Global/Local/Path、Evidence 延迟加载、失败降级的产品流程。
- `docs/architecture/adr/0005-no-graph-database-v1.md` — v1 使用 PostgreSQL Relation 表，保留 Graph Query Adapter 的已接受决策。
- `.trellis/spec/backend/database-guidelines.md` — Graph/Collection 是可重建投影，Relation 仍是唯一事实源；大图查询必须稳定分页。

### 2. Existing domain seams that should be reused

1. **Canonical node identity is already frozen.** `NodeRef{Type, ID}` and `NodeTypeTopic|Claim` are the correct graph endpoint identity; graph DTO/SQL should not invent a second node discriminator (`internal/knowledge/domain/relation.go:15-27`).
2. **Relation type semantics have one owner.** `RelationTypeCompatible` owns the endpoint compatibility matrix, while `CanonicalizeRelationEndpoints` owns symmetric direction normalization (`internal/knowledge/domain/relation.go:104-141`). Query filters should use the same enum vocabulary; projection construction must preserve the persisted canonical direction and may separately expose traversal direction.
3. **Fact status is explicit.** Topic, Claim, Relation statuses are stable domain values (`internal/knowledge/domain/model.go:14-21`, `internal/knowledge/domain/model.go:42-52`, `internal/knowledge/domain/relation.go:66-75`). Graph defaults must explicitly decide whether only `ACTIVE/CONFIRMED` is visible or whether `DISPUTED/STALE/SUGGESTED` is opt-in; silently treating all rows as equivalent would lose business meaning.
4. **Evidence is already a separate immutable entity.** `RelationEvidence` carries source version/span provenance and confirmation history (`internal/knowledge/domain/relation.go:92-102`). This supports the required lazy-load design: graph list responses should return evidence counts/availability and stable relation IDs, not full Evidence arrays.
5. **Bounded batch-read pattern exists.** `MaxBatchLimit=500`, `BatchGetClaims`, and `BatchGetRelations` establish workspace scoping, explicit limits, stable ordering and N+1 avoidance (`internal/knowledge/domain/repository.go:15-16`, `187-240`; `internal/knowledge/adapter/postgres/read.go:12-66`, `69-124`). These are useful for detail hydration and tests, but are not an adequate graph query API because they filter by aggregate IDs/status only and always load full Sources/Evidence.
6. **Application result validation is reusable as a pattern.** The service rejects out-of-workspace aggregates, invalid aggregates, over-limit output and unstable ordering (`internal/knowledge/application/queries.go:38-99`). A Graph Query Service should similarly validate projection version, node/edge closure, page bounds, cursor binding and path continuity before returning adapter output.

### 3. Persistence and query shape

`core.relation` is already the formal edge source with workspace, typed source/target, relation type/status, confirmation/confidence/validity, fingerprints and version (`migrations/00017_knowledge_domain.sql:101-167`). Database constraints prevent self edges, duplicate logical edges, reverse duplicates for symmetric types, invalid confirmation state and invalid validity intervals (`migrations/00017_knowledge_domain.sql:135-166`). `core.relation_evidence` is separately keyed to Relation and immutable provenance (`migrations/00017_knowledge_domain.sql:169-203`).

Existing indexes provide the minimum adjacency seam:

- `idx_knowledge_relation_workspace_status` for workspace/status listing.
- `idx_knowledge_relation_source` for source-side expansion.
- `idx_knowledge_relation_target` for target-side expansion.
- `idx_knowledge_relation_evidence_owner` for later evidence hydration.

They are declared at `migrations/00017_knowledge_domain.sql:1228-1236`. Before adding indexes, M7-01 should run representative `EXPLAIN (ANALYZE, BUFFERS)` for global page, one-hop/two-hop neighborhood, filtered shortest path and relation evidence count/detail. The likely gap is that current indexes were designed for ownership/status lookup, not necessarily compound keyset order by graph importance/cursor columns. Exact new indexes must follow the final query predicates and sort order, not be guessed upfront.

There is currently no materialized graph projection table, projector checkpoint, projection version, node importance, cluster identity or rebuild mechanism. ADR 0005 explicitly allows PostgreSQL Relation queries first and says to preserve a Graph Query Adapter (`docs/architecture/adr/0005-no-graph-database-v1.md:5-12`). Therefore the lowest-risk seam is:

`Knowledge facts (Topic/Claim/Relation) -> graph read adapter (SQL/recursive CTE) -> versioned Graph read DTO -> HTTP`

Only add a stored/rebuildable projection if query/EXPLAIN evidence shows direct fact queries cannot meet the M7 acceptance target. A stored projection must remain derived and rebuildable; it must never accept independent Relation writes.

### 4. Current data flow and missing flow

Existing internal flow:

`Knowledge command -> application.Service -> domain.Repository -> core.topic/core.claim/core.relation -> validated aggregate result`

Existing graph-adjacent read flow:

`GetRelations(ids/statuses) -> BatchGetRelations -> relation rows -> all relation_evidence rows -> ValidateRelationAggregate`

Required M7 read flow:

`HTTP graph request -> strict decoder/cursor validation -> Graph Query Service -> Graph Query Port -> PostgreSQL direct/rebuildable projection query -> compact nodes/edges/page metadata -> UI render`

Required evidence flow:

`select relation -> graph relation detail/evidence endpoint -> Knowledge relation_evidence provenance -> existing Retrieval Evidence Reference endpoints -> verified source/span excerpt`

The Retrieval module already exposes immutable source-version and span endpoints (`internal/retrieval/http/handler.go:53-58`, `297-335`). Graph evidence DTOs can reuse their href shape/endpoint rather than copying source text into graph responses.

### 5. Backend gaps

- No `internal/knowledge/http` or `internal/graph` module exists.
- `cmd/api/main.go` does not construct the Knowledge Repository/Service, despite the domain and PostgreSQL adapter existing; composition dependencies list Workspace/Workflow/ChangeControl/Ingestion/Retrieval/Conversation/Events only (`cmd/api/main.go:91-97`, `193-209`).
- `app.Dependencies` and router registration have no Knowledge/Graph handler (`internal/app/router.go:36-55`, `94-118`).
- No Graph Query interface or read models for global graph, neighborhood, path, node detail, relation detail, evidence summary, cluster or projection metadata.
- Existing `BatchGetRelations` eagerly loads every Evidence item for every returned edge (`internal/knowledge/adapter/postgres/read.go:105-118`), conflicting with “Evidence 延迟加载” (`docs/architecture/workflows/06-graph-and-semantic-links.md:41-52`).
- Existing batch ordering is `updated_at DESC,id` and has no cursor predicate (`internal/knowledge/adapter/postgres/read.go:82-87`); it cannot provide multi-page stable traversal under concurrent updates.
- Topic has single-ID read only (`internal/knowledge/domain/repository.go:215-218`); global graph needs bounded topic/claim node hydration without per-node calls.
- No query timeout/budget/depth/path-length contract. Workflow requires depth 1 default and user-selected 2/3, shortest path, relation filters, and truthful no-path (`docs/architecture/workflows/06-graph-and-semantic-links.md:41-52`).
- No projection freshness/version field exists to bind a cursor or detect stale pages.
- No error-code matrix for invalid filter/depth/cursor, no-path, timeout, unavailable projection or inconsistent edge endpoint.

### 6. HTTP/OpenAPI gaps and reusable patterns

Architecture documentation names `/api/v1/graph` and the operations neighborhood/path/relation details, but the executable OpenAPI has no graph path (`docs/architecture/api-and-events.md:26`, `223-228`; search of `api/openapi/openapi.json`). This is contract drift that M7-01 must close in API code and OpenAPI together.

Reusable HTTP patterns:

- chi module-owned `Routes` method (`internal/retrieval/http/handler.go:53-58`).
- Missing dependencies retain routes and return explicit 503 (`internal/retrieval/http/handler.go:48-50`, `265-268`).
- Strict JSON decoding and Problem Details mapping (`internal/retrieval/http/handler.go:265-294`).
- HMAC cursor bound to canonical request and projection/result identity, with explicit invalid/stale errors (`internal/retrieval/http/cursor.go:26-43`, `71-124`, `127-162`).

For graph pagination, prefer database keyset cursors over Retrieval's “recompute top-100 + offset” implementation. The reusable concept is signed request/projection binding, not its offset payload. Global graph cursor should bind at least schema version, workspace, canonical filters, projection/fact snapshot identity, stable sort tuple and page limit.

Suggested endpoint surface to resolve in design (names are proposals, not repository facts):

- global page: `POST /api/v1/graph/query` or `GET /api/v1/workspaces/{workspace_id}/graph`;
- neighborhood: `/.../graph/nodes/{node_type}/{node_id}/neighborhood`;
- shortest path: `POST /.../graph/path`;
- relation detail/evidence: `/.../graph/relations/{relation_id}` and/or `/evidence`.

Do not finalize paths until PRD acceptance criteria and OpenAPI naming are reviewed.

### 7. Frontend gaps and reusable seams

- Routes only include `/`, `/chat`, `/chat/:conversationId` (`web/src/routes/AppRoutes.tsx:6-12`); there is no graph navigation/page.
- Active Workspace state is already centralized and UUID-validated (`web/src/app/active-workspace.ts:3-15`, `31-51`). Graph query keys must include workspace ID.
- TanStack Query is already configured; infinite query conventions and AbortSignal propagation are visible in `web/src/features/rag/queries.ts:17-37`.
- API clients use strict runtime decoding rather than TypeScript casts (for example `web/src/api/search.ts:127-189`). A graph client should follow this and reject unknown enum/status/node shapes.
- Page tests can reuse `renderWithAppProviders` (`web/src/test/render.tsx:1-13`).
- No graph/layout dependency exists in `web/package.json`. The minimum-real UI can start with deterministic list/SVG/CSS rendering for a bounded page and selected-node panel; adding a graph library should be justified as a separate dependency decision.

Minimum UI seam consistent with architecture:

`GraphPage -> useActiveWorkspaceId -> graph query hook -> bounded node/edge view + list fallback -> selected relation -> lazy evidence query -> existing evidence hrefs`

The list fallback is a requirement-level resilience behavior, not optional polish: layout failure must preserve list mode (`docs/architecture/workflows/06-graph-and-semantic-links.md:75-80`).

### 8. Test foundation and missing coverage

Existing reusable coverage:

- Domain compatibility matrix, symmetric canonicalization, lifecycle and Evidence invariants: `internal/knowledge/domain/relation_test.go:10-172`.
- PostgreSQL command lifecycle and `BatchGetRelations`/`BatchGetClaims`: `internal/knowledge/adapter/postgres/repository_integration_test.go:24-230`.
- Migration shape and concurrency/lifecycle locks: `internal/platform/migration/knowledge_domain_integration_test.go` (notably relation endpoint lock scenarios around lines 409-591 and shape assertions around 622-645).
- Router middleware/API NotFound foundation: `internal/app/router_test.go`.
- Strict API decoder tests: `web/src/api/search.test.ts`.
- React page/provider tests: `web/src/features/*/*.test.tsx`, `web/src/test/render.tsx`.

Missing M7-specific tests:

- projection/direct-query parity against canonical Knowledge facts;
- global keyset pagination: no duplicates/gaps, deterministic tie-break, cursor filter binding, stale projection behavior;
- local graph depth 1/2/3, directed/symmetric traversal semantics, cycle prevention and hard node/edge budgets;
- shortest path with relation/status filters, equal-length deterministic tie-break, no-path response, timeout/cancellation;
- endpoint closure: every edge references returned nodes or explicitly marked boundary nodes;
- lazy Evidence: global/local/path response executes no full Evidence load; relation detail loads one bounded Evidence page;
- workspace isolation and retired/stale/suggested visibility policies;
- index/EXPLAIN assertions for representative fixtures and larger synthetic graph;
- HTTP strict decoding, Problem Details, route 503 seam and OpenAPI parity;
- frontend route, loading/empty/error/no-path, selection, evidence lazy fetch, pagination and list fallback.

### 9. Risks

1. **Second source of truth:** a writable graph table or UI mutation that bypasses Knowledge commands would violate the core contract. Projection must be derived/read-only and rebuildable.
2. **Evidence amplification:** reusing `BatchGetRelations` for a graph page may load unbounded Evidence per edge even when the response does not show it.
3. **Cursor instability:** `updated_at` paging over mutable facts without snapshot/projection binding can skip or duplicate nodes/edges.
4. **Polymorphic endpoint hydration:** Topic and Claim have different fields/lifecycles. Per-edge endpoint loads create N+1; a union query or batched typed hydration is required.
5. **Directed vs visual-undirected ambiguity:** symmetric edges are canonicalized, while other relations are directional. Traversal and rendering must preserve semantic direction.
6. **Recursive CTE blow-up:** depth 3 and path queries need depth, visited set, node/edge count, statement timeout and filter bounds.
7. **Status leakage:** showing Suggested/Rejected/Deprecated facts as confirmed knowledge would mislead users; defaults and visual distinctions must be contractual.
8. **Projection freshness:** if a stored projection is added without checkpoint/version surfaced to API/cursor, pages can mix rebuild generations.
9. **OpenAPI drift:** architecture docs mention Graph, executable OpenAPI and router do not. Tests must make graph handler/OpenAPI changes atomic.
10. **Frontend dependency expansion:** adding a layout library before a bounded DTO and list fallback are stable increases delivery and testing risk.

### 10. Recommended task split

1. **M7-01A — Contract/design lock:** finish PRD acceptance criteria and design Graph node/edge DTOs, default statuses, filters, depth/budgets, stable cursor, no-path/timeout errors, evidence summary/detail, projection freshness, endpoint paths.
2. **M7-01B — Graph query domain/application seam:** add graph read models, `GraphQueryPort`, validating service and cursor canonical request model without HTTP/SQL details.
3. **M7-01C — PostgreSQL direct projection queries:** implement global page, bounded neighborhood and shortest path over canonical tables; add typed node hydration and evidence counts; run EXPLAIN and only then add a forward migration for required compound indexes.
4. **M7-01D — Optional stored projection (conditional):** only if C fails measured acceptance; add rebuild/checkpoint/version and parity/recovery tests. Otherwise omit this task.
5. **M7-01E — HTTP/OpenAPI/composition:** create Graph handler, strict decoders, signed keyset cursor, Problem Details mapping; wire repository/service in `cmd/api/main.go` and `internal/app/router.go`; update OpenAPI and contract tests.
6. **M7-01F — Evidence detail seam:** relation detail/evidence pagination returning stable Source Version/Span hrefs; reuse Retrieval Evidence Reference endpoints for verified excerpts.
7. **M7-01G — Minimum real frontend:** add `/graph`, workspace-scoped query hooks/keys, bounded graph + list fallback, global/local/path controls, selected node/relation panel and lazy Evidence load; no write operations.
8. **M7-01H — Integration/performance gate:** database fixtures for cyclic/branching graphs, pagination/path correctness, workspace/status isolation, HTTP/OpenAPI tests, UI tests, EXPLAIN/capacity budget and Docker smoke.

This split keeps the read model and API contract independently verifiable and makes the stored projection an evidence-triggered decision rather than an assumed requirement.

### 11. Related specs

- `.trellis/spec/backend/database-guidelines.md` — stable cursor pagination, explicit columns, no N+1, Relation as unique fact and Graph as rebuildable projection.
- `.trellis/spec/backend/directory-structure.md` — domain/application/adapter/composition dependency direction.
- `.trellis/spec/backend/quality-guidelines.md` — database integration and delivery gates.
- `.trellis/spec/frontend/directory-structure.md` — feature/API/routes placement.
- `.trellis/spec/frontend/type-safety.md` — boundary decoding and no unsafe payload casts.
- `.trellis/spec/guides/cross-layer-thinking-guide.md` — end-to-end contract/data-flow mapping.

### 12. External references

No external documentation was required for this repository seam scan. Versions observed from repository manifests: Go dependencies in `go.mod`; React 19.2.7, React Router 7.18.1, TanStack Query 5.101.2, TypeScript 5.9.3 and Vite 8.1.5 in `web/package.json`. Library/API choices for graph rendering were intentionally not researched because no dependency decision is yet justified by the current PRD.

## Caveats / Not Found

- `python3 ./.trellis/scripts/task.py current --source` returned `Current task: (none)` / `Source: none`. The parent dispatch explicitly supplied `.trellis/tasks/07-20-graph-projection-queries`, so this research was written only under that directory.
- The task PRD currently has `Requirements: TBD` and `Acceptance Criteria: TBD`; endpoint shape, visible statuses, performance targets and whether “projection” must be persisted are therefore unresolved.
- No Graph HTTP/OpenAPI implementation, frontend graph page, projection table/projector or graph-specific tests were found.
- No representative production-scale graph fixture or measured query plan was found; index/performance recommendations remain conditional until EXPLAIN and acceptance thresholds exist.
