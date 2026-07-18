# M6-D Search API And Integration

## Goal

把 M6-C 已验证的 Active-only Keyword/Semantic/Hybrid Search 作为真实 HTTP/OpenAPI 查询能力交付，
使客户端能够分页读取可解释 Evidence、打开 Source Version/Source Span 引用，并用生产 API、Worker、
PostgreSQL/River 与 Compose 证明 Approved Proposal → Safe Writeback → Reindex → Active Index Search →
Proposal/Execution completed 的唯一事实闭环。

## Background And Confirmed Facts

- `internal/retrieval/application.SearchService` 已实现 Keyword、Semantic、Hybrid、strict RRF v1、
  相邻 Chunk 去重、可选 Rerank、真实空结果和显式降级；查询只读取 Workspace 当前 Active Index。
- `domain.SearchRequest` 已锁定 8 KiB Query、每次最多 100 个最终 Evidence、每类过滤值最多 100 个，
  Filter 支持 Source、Source Version、受控路径前缀和 `[captured_at_from,captured_at_before)`。
- Evidence v1 已包含 Chunk、Parse Projection、Source Span、Source Version provenance、相对路径、Hash、
  Snippet、各阶段 rank/score、Index/Embedding Version 与 provenance 截断事实。
- 生产 API 使用 chi、严格 JSON 解码、统一 `Problem{error_code,message,retryable,details}`；OpenAPI 3.1
  的事实源是 `api/openapi/openapi.json`，检查入口为 `node api/openapi/check.mjs`。
- 产品/API 文档已保留 `/api/v1/search`，Search 请求包含 query、scope/filter、retrieval mode、cursor、limit；
  当前仓库尚无 Search Handler、Source Version/Span 查询端点或前端 Search Decoder。
- API Composition Root 当前未构造 Retrieval Repository/SearchService，也未接收 Worker 已使用的
  Embedding 配置；Compose 只向 Worker 注入 Embedding/RRF 环境变量。
- 正式认证、Session/API Token、CSRF/Origin 与 Capability Middleware 归 M10；当前默认 API 绑定
  `127.0.0.1`，Compose 只发布 `127.0.0.1:${ZHIXU_HTTP_PORT}`。M6-D 必须实现 Workspace 隔离，
  但不得用 allow-all 伪认证声称完成 M10 安全门禁。
- 当前没有已批准的生产 Rerank 协议；M6-D 继续使用 nil Reranker 的显式 degraded 行为。

## Requirements

### R1. Search HTTP Contract

- 新增 `POST /api/v1/search`。它是无业务副作用的复杂 Query，不要求 `Idempotency-Key`，不创建 Workflow。
- 请求字段：
  - `workspace_id`：必填 UUID。
  - `query`：必填 UTF-8 文本，trim 后非空，最大 8 KiB。
  - `retrieval_mode`：可选 `keyword|semantic|hybrid`，省略时默认 `hybrid`。
  - `filters`：可选 `source_ids`、`source_version_ids`、`path_prefixes`、`captured_at_from`、
    `captured_at_before`；沿用 M6-C 统一 Canonicalization，不在 Handler 复制规则。
  - `cursor`：可选 opaque cursor。
  - `limit`：可选，默认 20，范围 `1..100`。
- Handler 必须严格拒绝未知字段、多 JSON 值、非法 UUID/时间/模式/过滤器/cursor；错误不回退成空结果。
- Search 超时和取消沿用请求 Context；不得在 Handler 启动脱离请求生命周期的后台检索。

### R2. Stable Cursor Window

- API Cursor 只分页当前查询的有界 top-100 结果窗口，不宣称数据库总命中数，也不引入 offset/page 公共契约。
- Cursor v1 必须使用进程内随机 HMAC-SHA256 密钥签名，并绑定规范化 Query/Mode/Filter/Limit 的指纹、
  实际 `index_version_id`、完整有序 top-100 结果指纹和下一 offset；
  客户端篡改、跨请求复用或越过 100 窗口返回 `RETRIEVAL_SEARCH_CURSOR_INVALID`。
- 后续页重新执行同一有界检索并验证 Active Index 与完整结果指纹仍匹配 Cursor；索引切换、未来
  Reranker 非确定性或结果漂移返回 `409 RETRIEVAL_SEARCH_CURSOR_STALE`，不得拼接两次不同排序。
- Cursor 生命周期限定为当前 API 进程；重启或切换实例后旧 Cursor 明确失效，客户端从第一页重启。
- `next_cursor` 仅在 top-100 窗口内仍有结果时返回；空结果或最后一页不返回伪 cursor。

### R3. Explainable Search Response

- 响应必须包含：Workspace、requested/effective mode、Index Version、可选 Embedding Version、
  持久 Index degraded capabilities、单次 Query degradations、Evidence Items、可选 next cursor。
- 每个 Evidence 必须保留：Chunk/Parse Projection、Sequence、Content Hash、Heading Path、Snippet、
  Source Span、稳定有界多 Source provenance、截断标志、Lexical rank/score 与 FTS/trigram 原始分数、
  Vector rank/distance、Fusion rank/score、可选 Rerank rank/score/model version。
- Vector 字段必须明确是持久 Distance Metric 对应的原始 distance，不能冒充跨模型可比较的 similarity。
- FTS-only Hybrid 返回 `effective_mode=keyword` 且显式 vector/rerank degraded；FTS-only Semantic 返回
  `503 RETRIEVAL_SEMANTIC_UNAVAILABLE`；真实零命中返回 `200` 与 `items=[]`。

### R4. Openable Evidence References

- 每个 provenance 返回实际可请求的 `source_version_href` 与 `source_span_href`，不能返回不存在的 URL。
- 新增只读资源：
  - `GET /api/v1/workspaces/{workspace_id}/source-versions/{source_version_id}`。
  - `GET /api/v1/workspaces/{workspace_id}/source-versions/{source_version_id}/spans/{source_span_id}`。
- Source Version 响应只返回真实持久字段：Source ID/type/logical name、受控相对路径、内容 Hash、大小、
  MIME、安全状态和 captured time；不返回 Workspace 绝对根路径或 Content Artifact locator。
- Span 响应验证 Workspace → Source Version → Content Artifact → Parse Projection → Span 的完整绑定，
  通过现有安全 Artifact Reader 校验 managed locator、大小和全文 Hash 后读取 `[start_byte,end_byte)`；
  返回 span type、1-based 闭区间行号、0-based 半开区间 byte range、selector、excerpt hash、
  parser/schema version、最大 4 KiB UTF-8 `excerpt` 和 `excerpt_truncated`。
- 不得从 provenance 的相对路径读取当前工作树；写回后工作树内容可以与引用的不可变版本不同。
- 跨 Workspace、Source Version 与 Span 不关联、或资源不存在统一返回 404，避免泄漏其他 Workspace 身份。

### R5. Composition And Compatibility

- 在 `internal/retrieval/http` 建立 Handler；Application/Domain 不依赖 HTTP，Handler 不直接执行 SQL、
  RRF、cursor 排名或 Source/Span 绑定规则。
- 提取 Worker/API 共用的配置化 Embedder Factory，确保两进程使用同一 Provider/Model/Dimensions/
  Config Hash；禁止复制一份将来会漂移的构造逻辑。
- API 在数据库可用时构造 Retrieval PostgreSQL Repository、SearchService 与 Evidence Reference Service；
  Embedding disabled 时仍提供 Keyword 和明确退化的 Hybrid。
- Compose 将同一 Embedding 配置注入 API 与 Worker；Credential 不进入日志、响应、cursor、OpenAPI 示例或 smoke 输出。
- 只做向后兼容的 OpenAPI 新增，不修改既有路径语义；无数据库迁移，不修改历史迁移 `00014`–`00016`。

### R6. OpenAPI And Client Boundary

- OpenAPI 3.1 完整声明 Search、Source Version、Source Span、Cursor、Evidence、Scores、Degradation 和 Problem 响应。
- 更新 `api/openapi/check.mjs`，将三个新路径及关键 Schema 纳入漂移门禁；所有操作必须声明 405。
- 新增 `web/src/api/search.ts` 的严格 Decoder/Client 与单元测试，供 M9 Search/RAG UI 复用；本任务不实现页面。
- 前端 Decoder 必须拒绝未知 mode/capability、非有限分数、非法时间/UUID、缺失引用和错误 cursor 类型，
  不能在组件中强转原始 JSON。

### R7. Integration, Performance And Operations

- Handler 单测覆盖正常、默认 Hybrid、Filter、分页、cursor 篡改/跨请求/过期、Semantic unavailable、
  degraded、零结果、Store/Provider 错误和安全 Problem 映射。
- 真实 PostgreSQL 集成覆盖 Search HTTP 成功、Workspace 隔离、Source Version/Span 打开、三种模式与过滤器；
  复用并保持 FTS GIN、trigram GIN、Active/Manifest B-tree 与三种 vector operator 的 EXPLAIN 基线。
- 扩展真实 PostgreSQL/River fault smoke：在唯一 Completion 后通过真实 HTTP Router 查询新内容并打开
  返回的 Source Version/Span；继续断言重复 Delivery/response-loss 不产生第二 Activation/Completion。
- 新增可重复 Compose API smoke：创建 disposable Git Workspace，通过公开 API 完成 Scan/Ingestion/
  Proposal/Approval，等待 Worker 完成 FTS-only Reindex，再用 `/api/v1/search` 命中新正文并打开引用；
  结束时删除 Compose volume 和临时 Workspace。
- 日志、Problem、cursor、测试失败输出和 River payload 不得包含 Provider Key、正文、DSN、绝对路径或 Content Artifact locator。

## Acceptance Criteria

- [x] `POST /api/v1/search` 的三种模式、统一过滤、默认值、100 上限、真实空结果和稳定 Problem 映射通过 Handler 测试。
- [x] Cursor v1 绑定规范请求与 Index Version；正常翻页、篡改、跨请求复用、窗口越界和 Active 切换均有确定性测试。
- [x] Search 响应完整保留 M6-C Evidence/Score/Degraded 语义，vector 明确为 distance，不丢 provenance 截断事实。
- [x] Evidence 的 Source Version/Span href 均可真实请求；完整绑定、Workspace 隔离、404 防枚举通过 PostgreSQL 集成测试。
- [x] API/Worker 复用同一配置化 Embedder Factory；Embedding disabled 与配置启用均通过 composition 测试，Secret canary 不泄漏。
- [x] OpenAPI 新路径和 Schema 无漂移；Web Search Decoder 正常、边界和失败路径测试通过。
- [x] PostgreSQL Search/EXPLAIN、真实 River fault smoke 和 Compose API smoke 全部通过，闭环只产生一个 Active/Completion。
- [x] `go test -race`、关键包 `-count=20`、全仓 integration `-p 1`、`go vet ./...`、`make test`、
  `go mod tidy -diff`、Compose config/build/up/down、go-review、sql-code-review、独立审查和 Trellis full-scope check 通过。
- [x] 父任务 M6 Retrieval Indexing And Search 的全部 AC 有当前代码、测试或运行日志证据后归档；M6-01 标记完成。

## Out Of Scope

- RAG Answer、Conversation、Citation Validation、Query Rewrite、Agent/Tool Calling 和 SSE；归 M6-02～M6-04。
- Search 页面、结果表格、Diff/Graph/Collection UI；归 M9。
- 正式认证、Session/API Token、CSRF/Origin 和 Capability Middleware；归 M10。M6-D 不声明它们已完成。
- 生产 Rerank Adapter、模型协议或假重排分数；只有既有 Port 和显式 degraded。
- Document/Article Revision、Topic/Claim/Relation/Conflict 过滤或空壳字段；对应领域模型尚未落地。
- 50 万容量最终 HNSW/IVFFlat、分区和 P95 锁定；归 M10，本任务只保留可重复 exact-scan/索引计划基线。
