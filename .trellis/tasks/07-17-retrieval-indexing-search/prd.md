# M6 Retrieval Indexing And Search

## Goal

建立统一、可追溯、可降级的 Retrieval 事实边界：把 Ingestion Canonical Chunk
投影为版本化 FTS/pgvector 索引，消费 Safe Writeback 的重索引请求，提供可打开引用的
混合检索，并且只有在索引与回归验证成功后才把 Proposal/Writeback 从
`verifying/index_pending` 推进到 `completed`。

## Background

- Ingestion 已持久化不可变 Parse Projection、Source Span 和 Canonical Chunk；Retrieval
  不得复制正文事实或重新定义解析生命周期。
- Safe Writeback 已原子发布 `retrieval.revision.reindex_requested`，但当前没有消费者，
  Proposal/Execution 会永久停在 `verifying/index_pending`。
- 仓库只有空 `retrieval` Schema，没有 Index Version、Embedding Version、Chunk Projection、
  Search API 或生产 Model Adapter。
- Eino PoC 未通过全部采用门禁，主模块继续使用项目自有 Port + 直接
  OpenAI-Compatible/Ollama Adapter，不引入 Eino 类型。
- pgvector 官方支持可变维度 `vector` 列；ANN 索引要求同一表达式维度。首个模型和维度
  尚未通过评测锁定，因此本任务先建立精确检索基线，后续按 Embedding Version 创建
  可替换的部分表达式 HNSW 索引。

## Requirements

### R1. Versioned Index Projection

- 新增 Embedding Version、Index Version 和 Chunk Projection；所有投影必须关联
  Workspace、Canonical Chunk、Source Span、Parser/Chunk Strategy 与 Index Version。
- 每个 Index Version 必须冻结不可变 Build Manifest：source snapshot/Git Commit、Parser/
  Chunk Strategy、目标 Chunk identity/content hash 集合摘要和 expected count。Ready/Active
  必须证明 Manifest 中每个 Chunk 都有合格 Projection，不能以“已有行数”猜完整性。
- 同一 Workspace 同一时刻只能有一个 Active Index；Building/Failed 版本不得污染查询。
- Index 激活必须原子切换，新版本失败时旧 Active 继续服务。
- FTS、向量、Tokenizer、Embedding、RRF 和 Rerank 配置均必须有版本或配置哈希。

### R2. Lexical And Vector Capabilities

- PostgreSQL FTS 为必需能力；中文 v1 基线使用可替换的 `simple + pg_trgm`，不得把该
  基线写成领域永久约束。
- Embedding 通过项目自有批量 `Embedder` Port；生产 Adapter 支持直接
  OpenAI-Compatible/Ollama 协议，测试使用确定性 Fake。
- Embedding 未配置或不可用时允许显式 FTS-only degraded；不得写入伪向量、返回假成功
  或声称 Hybrid 完整可用。
- 向量模型、维度或配置变化必须建立新的 Embedding/Index Version，不得混写旧投影。

### R3. Reindex Delivery And Completion

- 重索引请求必须从版本化 Outbox payload 解码并重新进入受控 Source/Ingestion 流程，
  不能仅凭路径直接构造 Retrieval 投影。
- Outbox 的“已派发”和“业务处理成功”必须是不同事实；`published_at` 不得同时承担两种
  语义。重复事件、River 重投和进程崩溃只能产生一个逻辑 Index Build。
- 成功必须原子保存索引结果、激活版本、记录消费结果，并推进 Writeback Execution 与
  Proposal `verifying → completed`。
- 索引或回归失败不回滚 Git Commit；保持旧 Active Index 与
  Proposal/Execution `verifying`，按稳定错误分类重试或进入人工恢复。API 根据 Delivery
  与 Active Index 派生 `index_pending`/`index_stale`，不新增模糊的 Proposal `stale` 状态。

### R4. Search Contract

- 支持 Keyword、Semantic 和 Hybrid；Hybrid 通过版本化 RRF 融合，不直接混合不可比
  原始分数。
- 默认仅返回当前允许生命周期的最新版本；Workspace、Source 状态/版本、路径、时间过滤在
  Lexical 与 Vector 两路一致生效。
- Evidence v1 必须包含 Source Version、Canonical Chunk、Source Span、受控相对路径、内容
  Hash、Snippet、各阶段分数、Index Version 和 Degraded 标志，且引用可由 API 打开。
- Document/Article Revision、Topic 与 Conflict 字段只有在对应知识模型存在后才扩展，
  本任务不得返回空壳字段或伪造绑定。
- 结果为空必须真实返回空结果；不得包装为模型答案。Rerank 不可用时返回 RRF 结果并
  显式标记 degraded。

### R5. Security, Performance And Operations

- 所有 SQL 参数化；动态过滤、排序和检索模式使用白名单，禁止把模型文本拼成 SQL。
- Embedding 批量调用，候选和返回数量有上限；禁止逐 Chunk 远程调用和无分页全量查询。
- Metrics/Trace 只使用 bounded labels；日志和 Outbox 不包含正文、Credential、数据库
  连接串或模型密钥。
- 提供迁移重复执行、索引构建/切换、查询 EXPLAIN、失败恢复和回滚文档。

### R6. Child Deliverables

1. M6-A：Retrieval Index Foundation——Schema、Build Manifest、确定性 Lexical Builder、领域契约、Repository 与安全激活。
2. M6-B：Reindex Consumer——Outbox 派发、River Job、受控重摄取、索引构建与完成归约。
3. M6-C：Embedding And Hybrid Search——生产 Adapter、Keyword/Trigram/Vector Query、RRF/dedup/rerank 与显式降级。
4. M6-D：Search API And Integration——OpenAPI、Evidence Contract、性能基线和完整闭环 smoke。

## Acceptance Criteria

- [ ] 迁移支持空库 Up、重复 Up、空数据 Down→Up；任意 Retrieval 业务数据存在时 Down fail closed。
- [ ] 单 Workspace 只有一个 Active Index；失败构建不改变旧 Active。
- [ ] Build Manifest 冻结目标 Chunk 集合；缺一 Projection 或 Hash 不匹配时不能 Ready/Active。
- [ ] Chunk Projection 与 Canonical Chunk/Workspace/Embedding Version 的交叉绑定由数据库和领域共同约束。
- [ ] FTS 可检索新 Chunk；Embedding 未配置时返回明确 `degraded_capabilities=["vector"]`。
- [ ] OpenAI-Compatible/Ollama Embedding Adapter 批量、超时、取消、错误映射和维度校验通过 Contract Test。
- [ ] Keyword/vector/RRF/dedup/filter 的确定性测试和 PostgreSQL EXPLAIN 通过。
- [ ] 重复 Outbox/River delivery、响应丢失和进程重启不重复激活索引或完成状态。
- [ ] Safe Writeback → Reindex → Active Index → Proposal/Execution completed 的真实 PostgreSQL/River smoke 通过。
- [ ] Search API 返回可打开 Source Version/Source Span、阶段分数、Index Version 和显式降级，OpenAPI 无漂移。
- [ ] `go test -race ./...`、关键包 `-count=20`、`go vet ./...`、`make test`、迁移、Compose、go-review、sql-code-review 与 Trellis full-scope check 通过。

## Out Of Scope

- RAG 生成、Conversation、Agent、Tool Calling、Citation Validation 与回答 UI；依赖本任务的 Evidence Contract。
- Document/Article Revision、Topic/Claim/Relation/Conflict、Graph、Collection、Artifact 和 Review。
- 50 万容量的最终 HNSW 参数锁定；本任务提供可重复基线，M10 根据真实模型和数据创建 ANN 索引。
- 独立向量数据库、Redis、Kafka 或外部搜索引擎。
