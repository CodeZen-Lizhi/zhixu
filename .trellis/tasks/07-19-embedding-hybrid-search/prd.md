# M6-C Embedding And Hybrid Search

## Goal

在 M6-A/B 已建立的不可变 Index/Manifest/Projection/Active 契约上，增加真实、可替换、
可批量和可恢复的 Embedding 能力，并提供只读取当前 Active Index 的 Keyword、Semantic、
Hybrid 检索内核。Hybrid 使用版本化 RRF、稳定去重和可选 Rerank；Embedding/Rerank 不可用时
必须明确降级，不写伪向量、不返回假成功，也不提前实现 M6-D HTTP/OpenAPI 空壳。

## Background

- M6-A 已提供 `embedding_version`、可变维度 pgvector Projection、vector-only 原子批次、
  Ready/Activate 与 FTS/trigram 索引，但没有生产 Embedder、向量批量编排或查询用例。
- M6-B 当前构建真实 FTS-only Active Index，并显式保存 `degraded_capabilities=["vector"]`；
  M6-C 必须兼容该历史事实，Hybrid 在此状态下退化为 Keyword，而 Semantic 明确不可用。
- 主模块不采用 Eino；模型边界继续使用项目自有 Port 和直接 HTTP Adapter。
- OpenAI 官方 Embeddings 支持字符串数组、可选 dimensions 与 float 编码；Ollama 原生
  `/api/embed` 支持字符串数组并返回同序二维向量。两种协议都不得把 SDK 类型带入领域层。
- 当前模型、维度和容量尚未通过 M10 基线锁定，因此本期保留 exact vector scan；不创建
  跨维度全局 HNSW，也不把小数据正确性测试包装成 50 万容量结论。

## Requirements

### R1. Provider-Neutral Embedding Contract

- 在 Retrieval Application 定义小型 `Embedder` Port：输入为有界字符串批次，输出必须与输入
  数量和顺序一一对应；契约包含 Provider、Adapter/Version、Model、Dimensions、Normalization、
  Distance、Config Hash、批量上限、单输入字节上限和单批累计输入字节上限，不包含 Credential。
- 实现直接 OpenAI-Compatible `/v1/embeddings` 与 Ollama `/api/embed` Adapter。固定 JSON
  Schema、响应大小上限、超时、取消、Content-Type、状态码和严格解码；未知字段可忽略，
  缺失/重复 index、数量/顺序、维度、NaN/Inf/零范数或模型不符必须 fail closed。
- OpenAI Adapter 强制 `encoding_format=float`，按返回 `index` 恢复输入顺序；Ollama 按数组顺序
  验证。API Key 只从 Composition Root 注入，Endpoint 不允许 userinfo、query 或 fragment，
  日志/错误不得包含 Key、请求正文或完整响应。
- Provider 5xx/429/网络/超时映射 Retryable；401/403、模型/Schema/维度不符映射稳定
  NonRetryable/Consistency。任何 Adapter 错误不能伪造向量或空数组成功。

### R2. Deduplicated And Recoverable Vector Build

- 新增前向迁移 `00016_embedding_hybrid_search.sql`，建立 Workspace-scoped、Embedding Version +
  Content Hash 唯一的可重建向量缓存；缓存不保存正文，向量维度/范数由数据库再次验证，
  创建后禁止篡改。存在 M6-C 缓存数据时 Down 返回 SQLSTATE `55000`。
- Repository 使用稳定 Manifest sequence/chunk ID 分页读取 `vector_status=pending` 的 lexical-ready
  Chunk；批量查询缓存，禁止逐 Chunk 查库。只把缓存 miss 的文本交给一次 Embedder batch，
  然后在单一事务中写缓存并调用现有 Projection 终态更新。
- cache INSERT 与 Projection UPDATE 必须各使用单条 set-based SQL；`pgx.Batch` 包装逐行 statement
  不满足 no-N+1。正文 page 同时受条数、单输入和累计字节上限约束。
- `BuildNextVectorBatch` 每次处理有界一批，可精确重放；Provider 成功但数据库响应丢失后，重试
  通过缓存/Projection 事实恢复，不修改 search_vector、不生成第二 Index/Embedding Version。
- Adapter 的单输入字节上限属于 Config Hash。超限且 `atomic_oversized=true` 时写
  `skipped_oversized/EMBEDDING_INPUT_OVERSIZED`；非 atomic Chunk 超限说明处理契约不一致，
  写稳定 failed 并使 Index 显式 vector degraded。当前 `token_count` 继续表示 M6-A 的确定性
  PostgreSQL lexical lexeme count，不冒充 Provider 计费 Token。
- 全部向量 Ready 才允许完整 Hybrid；存在 skipped/failed 时 Index 可 Ready，但必须保存
  `degraded_capabilities=["vector"]`。构建失败不影响旧 Active。
- Worker Composition 在配置合法 Embedder 时注册/精确重放 Embedding Version，并把该版本绑定到
  后续 Reindex Snapshot；Processor 流程演进为 Snapshot → Lexical → bounded Vector batches →
  `SNAPSHOT_STRUCTURE_V2` → Ready。未配置 Embedder 时保持现有 FTS-only/V1 行为。
- Hybrid 必须创建新的 Index Version，禁止向现有 FTS-only Active 事后补向量。Provider/Tokenizer/
  Fusion 配置变化通过新 Embedding/Index Version 演进；下一次 Safe Writeback Reindex 必须使用
  当前 Composition 冻结配置，不能继续无条件硬编码 `fts_only`。
- V2 Regression Hash 冻结 Embedding Version、Manifest、ready/skipped/failed vector count 和最终
  degraded capability；Delivery/Completion/数据库 deferred invariant 接受并精确重放 V1 或 V2，
  但不得扩大历史 `SNAPSHOT_STRUCTURE_V1` 的含义。
- Worker 配置变化后，历史 Hybrid 已无 pending Projection 时允许不调用 Provider 继续 V2 Regression；
  仍有 pending 时必须返回明确的版本 Provider unavailable，不能用当前默认模型改写历史。

### R3. Unified Search Contract And Filters

- 定义 `keyword/semantic/hybrid` 三种模式、统一 Filter、Candidate、Evidence 和 Search Result；
  M6-C 只使用当前真实存在的 Workspace/Source/SourceVersion/Chunk/Span 字段，不增加
  Document/Revision/Topic/Conflict 空壳。
- Store 查询必须只读取指定 Workspace 的当前 Active Index。Lexical 与 Vector 两路使用完全
  相同的 Source IDs、受控路径前缀和 captured time range 过滤；只选择 included Source Manifest、
  active Canonical Chunk、lexical ready，以及向量路的 vector ready。
- Keyword 组合 `websearch_to_tsquery(simple)` 与 pg_trgm，返回原始 lexical/trigram score；
  Semantic 根据 Embedding Version 的受限 Distance Metric 选择固定 SQL operator，禁止动态拼接
  用户输入。查询、候选和返回数量均有服务端上限和稳定排序。
- Evidence v1 至少包含 Index Version、Source/SourceVersion、Chunk/sequence、Source Span 行/字节范围、
  受控相对路径、Content Hash、Heading Path、bounded Snippet、各阶段 rank/score 和 degraded 标志，
  使 M6-D 可以直接建立可打开引用契约。
- 相同 Parse Projection/Chunk 可能被多个 included SourceVersion 复用；候选排名身份仍是 Chunk，
  Evidence 以稳定排序的 bounded provenance 列表返回全部匹配 SourceVersion/Path，不让相同 Chunk
  占据多个 rank，也不静默丢失可打开来源。超过 provenance 上限必须显式截断标志。

### R4. RRF, Dedup And Optional Rerank

- `fusion_config` 建立严格版本化 RRF v1 Schema，至少冻结 `method/version/k` 与 lexical/vector/
  fused/rerank candidate limits；未知字段、非法范围或非 canonical JSON 必须拒绝。
- RRF 只融合排名，不混合不可比原始分数；相同输入使用 chunk ID 稳定打破平分。Keyword、
  Semantic 和 Hybrid 均返回可解释阶段分数。
- 去重仅基于当前存在的 SourceVersion + 相邻 sequence：同一 SourceVersion 的相邻 Chunk 只保留
  高排名证据；不同 SourceVersion/Source 不擅自合并。Document/Revision 去重留待知识模型存在后扩展。
- 定义可选 `Reranker` Port，输入仅为有界候选和 bounded text，输出必须精确覆盖候选且无重复。
  仓库没有已批准的通用生产 Rerank 协议，本期不伪造供应商 Adapter；未配置、Retryable 故障时
  返回 RRF 顺序并显式 `rerank` degraded，契约/数据损坏则 fail closed。

### R5. Compatibility, Security And Operations

- FTS-only Active：Keyword 正常；Hybrid 返回 Keyword 结果并标记 vector/rerank degraded；Semantic
  返回稳定 capability unavailable，不用空结果冒充成功。真实零命中返回空 items。
- Embedding Adapter 必须批量调用、复用 `http.Client`、限制 response bytes；不得逐 Chunk 远程调用。
  SQL 全参数化，路径过滤先 canonicalize，动态 Distance SQL 仅由枚举白名单选择。
- Metrics/Trace 仅记录 provider/adapter、mode、result、batch/candidate count 和 duration 等 bounded
  label；日志不记录 query、chunk text、snippet、Credential、DSN 或完整 Endpoint。
- 提供 Domain/Application/Adapter Contract、真实 PostgreSQL cache/query/EXPLAIN、race/故障降级测试，
  并同步 Retrieval、Database、Testing、Deployment/Config 文档。

## Acceptance Criteria

- [x] OpenAI-Compatible 与 Ollama Adapter 对批量顺序、dimensions、normalization、timeout/cancel、
  429/5xx/4xx、非法 JSON、超大响应和敏感信息边界的共享 Contract Test 通过。
- [x] Credential 仅由环境/Composition 注入；Config String、日志、错误、Trace/Metric 不包含 Key、正文、
  query、snippet、DSN 或完整 Endpoint。
- [x] 00016 空库 Up、重复 Up、空数据 Down→Up 通过；存在缓存数据时 Down `55000`。
- [x] 相同 Workspace+Embedding Version+Content Hash 只产生一个缓存向量；跨 Workspace 不共享缓存身份。
- [x] Vector Builder 以单次有界批量调用处理 cache miss，cache hit 不访问 Provider，禁止 N+1；
  commit response-loss 重放不覆盖 search_vector 或终态向量。
- [x] 合法最大配置下单批正文不超过 `MaxBatchInputBytes`；cache/Projection 使用 set-based SQL，
  真实 PostgreSQL 证明没有逐 Chunk statement。
- [x] 配置 Embedder 后，真实 Reindex 创建绑定 Embedding Version 的新 Index，执行 Vector batch 与
  `SNAPSHOT_STRUCTURE_V2` 后完成；未配置时继续 V1 FTS-only，均不修改旧 Active。
- [x] 维度、NaN/Inf、零范数、返回数量/顺序、模型 binding 和 input size 异常均稳定失败；
  atomic oversized 显式 skipped，普通不一致显式 failed。
- [x] Keyword 使用 FTS/trigram，Semantic 使用正确 distance operator，Hybrid 两路使用同一 Filter；
  非法路径、时间范围、limit/mode 和 query 被领域校验拒绝。
- [x] RRF v1 对排名、平分、单路缺失确定性；相邻 Chunk 去重不跨 SourceVersion。
- [x] Rerank exact output 生效；未配置或 Retryable 故障保留 RRF 并标记 degraded；损坏输出 fail closed。
- [x] FTS-only Active 的 Keyword 可用、Hybrid 显式退化、Semantic 明确不可用；零命中真实返回空数组。
- [x] Evidence v1 精确返回 SourceVersion/Span/Path/Hash/Index 和阶段分数，不出现不存在的知识模型字段。
- [x] 共享 Chunk 只参与一次排名，并返回稳定有界的多 Source provenance；路径/Source filter 后的
  provenance 与 Lexical/Vector 候选完全一致。
- [x] PostgreSQL Integration 证明只读 Active、过滤一致、旧 Active/Building/Failed 不参与查询，
  FTS GIN/trigram 与 exact vector 计划可解释；未评测 HNSW 不作为本期证据。
- [x] `go test -race`、关键包 `-count=20`、真实 PostgreSQL integration、`go vet ./...`、`make test`、
  go-review、sql-code-review、独立审查和 Trellis full-scope check 通过。

## Out Of Scope

- Search HTTP/OpenAPI、cursor、权限中间件与前端页面；归 M6-D。
- Query Rewrite、Conversation、RAG Answer、Citation Validation、Agent 与 Tool Calling。
- Document/Revision/Topic/Claim/Conflict/Collection 过滤及 Graph-assisted Search。
- 通用生产 Rerank 供应商协议；待仓库选定真实 Provider 后实现 Adapter。
- 50 万 Chunk 的最终 HNSW/IVFFlat 参数、分区或独立向量库；归 M10 容量任务。
