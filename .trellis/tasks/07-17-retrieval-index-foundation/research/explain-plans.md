# M6-A Retrieval EXPLAIN Evidence

验证时间：2026-07-17

命令：

```bash
ZHIXU_TEST_DATABASE_URL='postgres://.../zhixu?sslmode=disable' \
  go test -race -tags=integration \
  -run TestRepositoryFTSOnlyBuildReadyActivateAndReplay -count=1 -v \
  ./internal/retrieval/adapter/postgres
```

测试在独立临时 Database 完成全部 Goose + River migration，并通过生产 `postgres.Open`
注册 pgvector 类型。为证明索引可达，事务内设置 `enable_seqscan=off`，实际计划如下。

## FTS GIN

```text
Bitmap Heap Scan on chunk_projection
  Recheck Cond: (search_vector @@ '''alpha'''::tsquery)
  ->  Bitmap Index Scan on idx_retrieval_projection_search_vector
        Index Cond: (search_vector @@ '''alpha'''::tsquery)
```

## Canonical Chunk Trigram GIN

```text
Bitmap Heap Scan on canonical_chunk
  Recheck Cond: (content % 'alpha'::text)
  ->  Bitmap Index Scan on idx_ingestion_canonical_chunk_content_trgm
        Index Cond: (content % 'alpha'::text)
```

## Active Index Partial Unique

```text
Index Scan using uq_retrieval_index_version_active on index_version
  Index Cond: (workspace_id = '81000000-0000-4000-8000-000000000001'::uuid)
```

结论：M6-A 的 FTS、trigram 和 Workspace Active 查询均存在真实索引执行计划证据；当前未创建
跨维度 HNSW，向量 exact scan/固定维度部分索引由后续容量评测决定。
