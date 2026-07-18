# Research: M6-D PostgreSQL EXPLAIN 与集成夹具

- Query: 核对 M6-D 可复用的真实 PostgreSQL 夹具、现有 Search EXPLAIN 证据、缺口、建议验证步骤与影响文件。
- Scope: mixed
- Date: 2026-07-19

## Findings

### Files Found

- `internal/retrieval/adapter/postgres/repository_integration_test.go`：Retrieval 独立临时数据库、全量迁移、pgvector 类型注册与基础索引 EXPLAIN 夹具。
- `internal/retrieval/adapter/postgres/search_integration_test.go`：Active-only、过滤等集、bounded provenance、三种 pgvector distance operator 与 exact scan EXPLAIN。
- `internal/retrieval/adapter/postgres/search.go`：生产 Lexical/Vector Candidate SQL，是 M6-D 应直接解释而不是另写简化 SQL 的事实源。
- `migrations/00014_retrieval_index_foundation.sql`：Active、Manifest、Projection、FTS GIN、trigram GIN 与复合 B-tree 索引。
- `migrations/00015_reindex_consumer.sql`：Source Manifest 与 Reindex Delivery 索引。
- `.trellis/tasks/archive/2026-07/07-17-retrieval-index-foundation/research/explain-plans.md`：M6-A 已保存的 FTS、trigram、Active partial unique 计划文本。
- `.trellis/tasks/archive/2026-07/07-19-embedding-hybrid-search/design.md`：M6-C 明确 exact vector scan 仅是正确性基线，容量与 ANN 归 M10。
- `docs/architecture/testing-and-evaluation.md`：M6-A/B/C 的 PostgreSQL、River、EXPLAIN 与 smoke 验收边界。

### Existing Reusable Patterns

1. `newRetrievalTestRepository` 已具备 M6-D 所需的核心数据库能力：
   - 从 `ZHIXU_TEST_DATABASE_URL` 连接 admin database；缺失时明确 skip（`internal/retrieval/adapter/postgres/repository_integration_test.go:520`）。
   - 每个测试创建唯一临时 database，并在 cleanup 中 `DROP DATABASE ... WITH (FORCE)`（`internal/retrieval/adapter/postgres/repository_integration_test.go:535`、`:571`）。
   - 使用 migration-only pool 完成全部 Goose + River migration，再使用生产 `postgres.Open` 注册 pgvector 类型（`:543`、`:549`、`:559`）。
2. `seedSnapshotWorkspace`、`seedSnapshotSourceVersion`、`createHybridSearchIndex` 能构造 Active Hybrid Index、Source/SourceVersion/Span/Chunk/Projection 的真实绑定；M6-D 不需要重新发明 SQL fixture（`internal/retrieval/adapter/postgres/search_integration_test.go:28`、`:48`、`:349`）。
3. 当前 EXPLAIN 分成两类：
   - 索引可达性：`SET LOCAL enable_seqscan=off` 后，字符串断言指定索引名（`internal/retrieval/adapter/postgres/repository_integration_test.go:697`）。
   - exact vector 语义：解释完整 `vectorCandidateSQL`，断言持久 distance operator 存在且没有 HNSW/IVFFlat（`internal/retrieval/adapter/postgres/search_integration_test.go:328`、`:336`）。
4. 已锁定的生产索引包括：
   - 单 Workspace 唯一 Active：`uq_retrieval_index_version_active`（`migrations/00014_retrieval_index_foundation.sql:72`）。
   - Workspace/Index/Chunk Projection：`idx_retrieval_projection_workspace_index_chunk`（`:146`）。
   - FTS GIN：`idx_retrieval_projection_search_vector`（`:149`）。
   - Canonical Chunk trigram GIN：`idx_ingestion_canonical_chunk_content_trgm`（`:152`）。
   - Source Manifest Workspace/Index/Source：`idx_retrieval_source_manifest_workspace_index_source`（`migrations/00015_reindex_consumer.sql:125`）。

### Gaps

1. 当前 FTS/trigram EXPLAIN 使用简化的单表查询，而不是生产 `lexicalCandidateSQL` 的完整 Active/Manifest/Provenance/Span 查询（`internal/retrieval/adapter/postgres/search_integration_test.go:258` 对比 `internal/retrieval/adapter/postgres/search.go:362`）。因此只能证明单个 GIN 可达，不能证明生产 join/filter shape。
2. vector EXPLAIN 只断言 `<=>`、`<#>`、`<->` 与“没有 ANN”，没有断言 Active Index、Projection、Manifest、Source filter 的关键访问路径，也没有保存计划 artifact。
3. 所有现有 helper 都设置 `enable_seqscan=off`。这适合“索引可达性”门禁，但会掩盖默认 planner 在真实数据分布下是否选择顺序扫描；不能作为性能基线。
4. 当前计划使用 text format、`COSTS OFF`，没有 machine-readable `FORMAT JSON`，也没有 `ANALYZE, BUFFERS` 的实际 rows/loops/buffer 证据。
5. 当前 fixture 数据量很小；没有代表性 fan-out、过滤选择性或候选上限数据集。仓库文档已明确这不是 50 万 Chunk/P95/ANN 结论（`docs/architecture/testing-and-evaluation.md:190`）。
6. 临时 database 创建/迁移逻辑在多个 integration package 重复，且 helper 是包内私有函数，M6-D 的 HTTP/Compose 测试无法直接复用。重复位置至少包括 Approval Dispatch、Workflow Runtime、Retrieval 和 River kill smoke。

### Recommended Fixture Shape

建议新增 test-only 公共夹具，而不是把业务 Repository 暴露给测试：

```text
internal/integrationtest/postgresdb/
  database.go          # OpenDisposable(t, baseURL), migration pool, app pool, cleanup
  explain.go           # JSON plan tree walker、index/operator/node assertions
internal/integrationtest/retrievalfixture/
  workspace.go         # committed Git workspace + Source/Projection/Chunk seed
  active_index.go      # FTS-only / Hybrid Active Index fixture
```

夹具应返回稳定 ID 集合和 cleanup，不返回内部 SQL 字符串；生产 Search SQL 继续只由 `SearchRepository` 拥有。若不希望本任务引入共享 testkit，次优方案是在 `internal/retrieval/adapter/postgres` 内增加仅供该 package 使用的 plan helper，但 Compose/API smoke 仍会重复 database/Git 初始化。

### Recommended EXPLAIN Steps

1. 保留现有“索引可达性”测试：`enable_seqscan=off`，验证 FTS GIN、trigram GIN、Active partial unique。
2. 新增“生产查询 shape”测试：直接解释完整 `lexicalCandidateSQL` 和三种完整 `vectorCandidateSQL`，覆盖无 filter、SourceVersion filter、path prefix、captured time 四种代表性请求。
3. 计划格式改为 `EXPLAIN (FORMAT JSON, COSTS OFF, VERBOSE)`；递归断言：
   - Active 读取仅绑定目标 Workspace + Index + `status='active'`。
   - FTS 分支存在 `idx_retrieval_projection_search_vector`；trigram 分支存在 `idx_ingestion_canonical_chunk_content_trgm`。
   - 三种 vector 计划包含持久 operator，且明确不存在 HNSW/IVFFlat。
   - `Limit`/Window rank 在 provenance 聚合前生效，防止 fan-out 改变 Top-K。
4. 单独增加小型“运行计划”证据：默认 planner 设置，使用具有选择性的代表性 fixture，运行只读 `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, TIMING OFF)`；断言实际 rows 有界、没有意外跨 Workspace 放大。不要对微秒时间设脆弱阈值。
5. 将实际 JSON/text 计划写入当前任务或测试日志。实现阶段若要持久证据，应写入任务 research，而不是仓库根目录临时文件。
6. canonical command 建议：

```bash
ZHIXU_TEST_DATABASE_URL='postgres://.../postgres?sslmode=disable' \
  go test -race -tags=integration -count=1 -v \
  ./internal/retrieval/adapter/postgres \
  -run 'TestSearchRepository(ActiveFiltersAndBoundedProvenance|DistanceOperatorsAndExactExplain|ProductionPlans)'
```

全仓 integration 共用基准 PostgreSQL 时继续 `-p 1`；每个测试自己创建独立 database 的并发场景可保留。

### Impact Files

- `internal/retrieval/adapter/postgres/search_integration_test.go`：完整生产 SQL 计划与 filter 计划断言。
- `internal/retrieval/adapter/postgres/repository_integration_test.go`：可保留基础索引可达性，或迁移公共 database helper。
- `internal/integrationtest/postgresdb/**`：建议新增的 disposable PostgreSQL 与 JSON plan testkit。
- `internal/integrationtest/retrievalfixture/**`：建议新增的共享 Retrieval fixture。
- `.trellis/tasks/07-19-search-api-integration/research/**`：保存本次真实计划证据。
- `Makefile`：增加明确的 integration/explain target，避免 `make test` 因环境变量缺失静默跳过真实数据库证据。

### External References

- PostgreSQL 18 `EXPLAIN`: https://www.postgresql.org/docs/18/sql-explain.html
  - 官方支持 `FORMAT JSON`，适合稳定解析计划节点。
  - `EXPLAIN ANALYZE` 会实际执行查询；本任务 Search 是只读查询，可安全使用，但写查询必须放在可回滚事务或避免 `ANALYZE`。
- PostgreSQL 18 `Using EXPLAIN`: https://www.postgresql.org/docs/18/using-explain.html
  - 官方示例使用 `enable_seqscan=off` 观察索引可达性；该设置不等同于默认 planner 性能证明。
- PostgreSQL 18 `pg_test_timing`: https://www.postgresql.org/docs/18/pgtesttiming.html
  - `EXPLAIN ANALYZE` 的 per-node timing 有额外开销；建议运行计划使用 `TIMING OFF` 并关注 rows/loops/buffers。
- 仓库锁定版本：PostgreSQL 18 / pgvector image `pgvector/pgvector:0.8.5-pg18-bookworm`（`deploy/compose.yml:3`），pgx `v5.10.0`、pgvector-go `v0.4.0`、Goose `v3.27.0`、River `v0.40.0`（`go.mod:7`-`:13`）。

### Related Specs

- `.trellis/spec/backend/database-guidelines.md`：真实 PostgreSQL、参数化 SQL、EXPLAIN 与 Active-only Search 契约。
- `.trellis/spec/backend/quality-guidelines.md`：数据库约束、迁移、EXPLAIN 与集成门禁。
- `.trellis/spec/guides/cross-layer-thinking-guide.md`：Search API → Application → PostgreSQL 的数据流与错误边界。
- `docs/architecture/retrieval-architecture.md`：Active-only、FTS/vector、RRF、Evidence 与 M10 容量边界。
- `docs/architecture/testing-and-evaluation.md`：M6-A/B/C 已有证据和 M6-D 不得伪装容量结论的边界。

## Caveats / Not Found

- 当前环境 `ZHIXU_TEST_DATABASE_URL` 未设置，因此本轮没有重新执行真实 PostgreSQL integration/EXPLAIN；结论来自已读测试、迁移、历史计划 artifact 与官方 PostgreSQL 18 文档。
- 未发现仓库内共享的 integration testkit；所有可复用 helper 目前都是 `_test.go` 私有实现。
- 未发现 50 万 Chunk、P95/P99、HNSW/IVFFlat 的已验证 M6-D 数据；这些仍属于 M10，不应写入本任务验收。
