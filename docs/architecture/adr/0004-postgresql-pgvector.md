---
status: accepted
---

# 使用 PostgreSQL 与 pgvector

个人知识库容量基线约 50 万 Chunk，需要关系数据、事务、全文、向量、Workflow 和审计的统一查询。PostgreSQL + pgvector 能减少独立向量库的双写和运维，并允许“只检索最新批准版本”等过滤在同一查询中完成。

## Considered Options

- PostgreSQL + pgvector。
- Qdrant/Milvus + PostgreSQL。
- Elasticsearch/OpenSearch + 向量。

## Consequences

- 必须以容量基线压测 HNSW 和过滤查询。
- 只有 pgvector 经调优仍无法满足指标时才评估专用向量库。

