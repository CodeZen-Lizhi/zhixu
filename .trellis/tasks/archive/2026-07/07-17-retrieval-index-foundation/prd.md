# M6-A Retrieval Index Foundation

## Goal

建立 Retrieval 的数据库和领域事实源：版本化 Embedding/Index、引用 Canonical Chunk 的
FTS/Vector Projection、失败隔离和原子 Active 切换，为后续 Reindex Consumer 与 Search
提供稳定、可测试且不依赖 Provider/River 的公共契约。

## Background

- `retrieval` Schema 已创建但为空；现有迁移最高为 00013。
- Ingestion 已拥有正文、Source Span 和 Canonical Chunk，Retrieval 只能保存引用与可重建
  索引投影。
- 当前没有正式 Embedding 模型/维度；Eino 不进入主模块。本任务不调用外部模型。
- pgvector 可变维度列允许不同 Embedding Version 共存，但 ANN 索引必须按固定维度构建。
- 后续 Consumer 需要“构建失败不影响旧 Active”和“重复请求返回同一版本”的硬不变量。

## Requirements

### R1. Forward Migration

- 新增 `00014_retrieval_index_foundation.sql`，只做前向扩展，不改写历史迁移。
- 启用 `pg_trgm`，新增：
  - `retrieval.embedding_version`
  - `retrieval.index_version`
  - `retrieval.index_manifest_chunk`
  - `retrieval.chunk_projection`
  - `retrieval.index_activation`
- 增加 Canonical Chunk `(id, workspace_id)` 复合唯一引用，并用复合 FK 保证 Projection、
  Index 与 Chunk 属于同一 Workspace。
- 每个 Workspace 最多一个 `active` Index；Index 状态只允许
  `building/ready/active/retiring/archived/failed`。
- Down 只允许全部 Retrieval 业务表为空；存在任意数据时返回 SQLSTATE `55000`。

### R2. Embedding Version

- Embedding Version 保存稳定 ID、provider、provider adapter/version、model、dimensions、
  normalization、distance metric、config hash 和创建时间，
  不保存 API Key、Endpoint Credential 或请求正文。
- `(provider, model, dimensions, config_hash)` 唯一；完全相同注册幂等返回同一版本，
  相同 ID 不同绑定返回 Consistency Violation。
- `config_hash` 的 canonical 输入必须覆盖 adapter name/version、非敏感 endpoint/protocol
  identity、model、dimensions、normalization、distance metric 及所有影响向量结果的参数。
- 记录一经创建不可变；dimensions 必须为正，具体 Provider 上限由 Adapter 校验。

### R3. Index Version

- Index Version 绑定 Workspace、可选 Embedding Version、Tokenizer ID/Version/Config Hash、
  Fusion Config、source snapshot ref、manifest hash、expected chunk count、idempotency key、
  状态、degraded capabilities、failure code 和乐观锁版本。
- Begin Index 必须同时冻结 `index_manifest_chunk`：Chunk ID、Workspace、Content Hash、
  Sequence、Parser/Chunk Strategy/Schema Version。Manifest hash 按稳定排序计算，创建后不可变。
- 无 Embedding Version 时必须声明 `vector` degraded；不得声称完整 Hybrid 能力。
- 通用 Transition 只允许 `building→ready|failed`、`active→retiring`、
  `retiring→archived`；任何进入 `active` 或回滚到旧 Active 都只能通过带 Activation Record
  的原子 Activate/RollbackActivate 用例。
- 相同 Workspace + idempotency key 完全重放返回同一 Index；不同配置冲突。

### R4. Chunk Projection

- Projection 以 `(index_version_id, chunk_id)` 唯一，保存 `tsvector`、可选 `vector`、
  token count、lexical/vector status、failure code 和时间，不复制 Canonical Chunk 正文。
- Lexical Ready 必须有 search vector；Vector Ready 必须有 Embedding Version、向量且维度
  等于注册 dimensions。
- Projection `embedding_version_id` 必须与所属 Index 完全一致；FTS-only 时两者都为空且
  vector status=disabled。同一 Index 禁止混入另一 Embedding Version。
- `disabled/skipped_oversized/failed` 不得伪装为 Ready；失败摘要只保存稳定错误码。
- Vector Ready 必须拒绝空向量、NaN/Inf、零范数和 dimensions 不匹配；token count 使用
  非负 int32 范围。
- M6-A 实现确定性 Lexical Builder：根据不可变 Manifest 批量 `INSERT ... SELECT` Canonical
  Chunk，生成加权 `simple` tsvector。`pg_trgm` 直接在 Canonical Chunk content 建
  `gin_trgm_ops` 索引，不复制正文。
- Application/Repository 使用批量写入或 `INSERT ... SELECT`，禁止逐 Chunk 事务或远程调用。

### R5. Atomic Activation

- 激活在单一 PostgreSQL 事务内锁定 Workspace 的 Index 集合，将旧 Active 转为 Retiring、
  目标 Ready 转为 Active，并追加唯一 Activation 记录。
- 目标不是 Ready、Manifest/Projection 数量或 Content Hash 不一致、Vector 状态与 degraded
  声明冲突或 Expected Version
  过期时，事务全部回滚。
- 相同 activation idempotency key 精确重放返回既有结果；不同绑定冲突。
- `RollbackActivate` 原子将当前 Active→Retiring、指定旧 Retiring→Active并追加新的
  Activation Record；同样支持幂等重放、Workspace 锁和 Expected Version。
- Activation 的目标/上一 Index 使用带 Workspace 的复合 FK，Activation 表 append-only；
  degraded capabilities 只允许受限枚举、去重和规范顺序。
- `GetActive` 只能返回 Active；没有 Active 返回稳定 NotFound，不回退到 Building/Failed。
- 提供按 Index ID、Workspace + idempotency key 的精确读取，以及聚合 Build Status
  （Manifest/Projection/lexical/vector counts），供 River 重投和进程恢复使用。

### R6. Module Boundary

- 新增 `internal/retrieval/domain`、`application`、`adapter/postgres`。
- 使用官方 `pgvector-go` 处理 pgx vector 编解码，并在项目 PostgreSQL Pool 的
  `AfterConnect` 为每条连接注册 vector 类型；不得自行拼接向量 SQL 字面量。
- Domain/Application 不 import pgx、pgvector、River、HTTP、Eino 或模型 SDK。
- Application 使用 `foundation.IDGenerator` 和 `foundation.Clock`，公开错误使用
  `foundation.Error` 稳定分类。
- 本任务不接 Worker、Outbox、Search HTTP 或生产 Embedding Adapter。

## Acceptance Criteria

- [x] Migration 空库 Up、重复 Up、空数据 Down→Up 通过；任意 Retrieval 业务数据存在时 Down 返回 `55000`。
- [x] 数据库拒绝跨 Workspace Projection/Activation、第二 Active、非法状态、Embedding Version 混写和向量维度不匹配。
- [x] Domain 覆盖全部合法/非法状态转移、degraded/vector 一致性和 Projection 校验。
- [x] Begin Index 冻结稳定 Manifest；Chunk 缺失、Hash/Count 不一致或 Manifest 后续修改均被拒绝。
- [x] Embedding/Index 注册精确重放幂等，不同绑定返回稳定冲突。
- [x] Projection 批量保存全有或全无；重复批次不增加第二行，不同内容冲突。
- [x] 生产 Pool 与测试 Pool 的 pgvector 类型注册通过，向量参数保持参数化。
- [x] Activate 的旧 Active→Retiring、新 Ready→Active、Activation append 在单事务完成。
- [x] RollbackActivate 的当前 Active→Retiring、旧 Retiring→Active、Activation append 在单事务完成。
- [x] 激活失败、响应丢失重放和并发双激活最多产生一个 Active。
- [x] GetIndex/GetIndexByIdempotency/GetBuildStatus 可精确恢复已提交构建与激活结果。
- [x] Lexical Builder 从 Manifest 批量生成全部 tsvector；缺一行不能 Ready。
- [x] FTS GIN、Canonical Chunk `gin_trgm_ops` 与 Workspace/Index/Chunk 关键查询执行计划有索引证据。
- [x] `go test -race ./internal/retrieval/...`、PostgreSQL integration、`go vet ./...`、`make test`、sql-code-review、go-review 和 Trellis check 通过。

## Out Of Scope

- Outbox/River Reindex Consumer 与 Change Control completed 归约。
- 外部 Embedding/Rerank 调用、Search/RRF/API。
- HNSW/IVFFlat 参数和 50 万容量最终基线。
- Topic/Claim/Relation/Conflict metadata 投影。
