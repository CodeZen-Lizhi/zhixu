# M6-C Embedding And Hybrid Search 实施清单

1. [x] 建立 Embedding Contract、strict RRF v1、Search/Filter/Candidate/Evidence Domain；覆盖 canonical、limits、排序、分数和降级矩阵。`go test ./internal/retrieval/...`、`go vet ./internal/retrieval/...` 已通过。
2. [x] 新增 `00016_embedding_hybrid_search.sql`：Workspace-scoped immutable Embedding Cache、维度/范数约束、V2 Regression/Completion 兼容、Down `55000` 与迁移测试；真实 PostgreSQL migration、Regression、Completion 集成测试已通过。
3. [x] 扩展 Workspace Snapshot/Processor/Regression：可选 Embedding Version、pending vector Projection、分页恢复、`SNAPSHOT_STRUCTURE_V2` 与 V1 历史 replay；Processor 按持久 Index kind 恢复。
4. [x] 扩展 Retrieval PostgreSQL Store：分页 pending vector inputs、批量 cache lookup、cache+Projection 原子保存及 response-loss replay；真实 PostgreSQL 冲突回滚/终态重放已通过。
5. [x] 实现 `BuildNextVectorBatch`：cache hit/miss、单次批量 Provider、input byte policy、L2 normalization、skipped/failed 与 Done checkpoint；同 hash fan-out 与 Provider 去重已覆盖。
6. [x] 实现共享 Embedder Contract Test，并落地 OpenAI-Compatible `/v1/embeddings` Adapter：严格 batch index、float、dimensions、错误映射和响应上限。
7. [x] 实现 Ollama `/api/embed` Adapter：`truncate=false`、同序 batch、模型/维度校验、错误映射和响应上限。
8. [x] 增加非敏感 Embedding 配置、环境变量和 Worker Composition：启动时注册版本，配置启用后新 Reindex 构建 Hybrid；配置/Adapter String、错误与环境示例不泄漏 Credential/完整 Endpoint。
9. [x] 实现共享 Search Filter SQL 与 Lexical Candidate Query：Active/included/active Chunk、FTS+trigram、Source/path/time、bounded provenance、稳定排序与候选上限；trigram threshold 固定为 0.3，不受连接 GUC 漂移。
10. [x] 实现 Vector Candidate Query：Embedding binding、三种白名单 distance operator、exact scan、统一过滤与稳定排序；真实 PostgreSQL EXPLAIN 已通过。
11. [x] 实现 Search Application：Keyword/Semantic/Hybrid、并行双路、RRF、SourceVersion 相邻去重、多 provenance Evidence v1 和真实零结果；截断 provenance 不参与无法证明的去重。
12. [x] 实现可选 Reranker Port、exact output validator 与 nil/retryable degraded；损坏输出 fail closed。
13. [x] 增加真实 PostgreSQL Integration/EXPLAIN、Provider httptest Contract、Hybrid Reindex fault smoke、race/故障/安全 canary 和最小检索 smoke；Vector commit response-loss 已在真实 River 重投恢复。
14. [x] 同步产品 PRD、Retrieval/Database/Testing/Deployment/Config 与 backend code-spec，修正 FTS-only、Rerank、cache FK 和 trigram threshold 文档冲突并明确 exact scan、Token 和 Rerank 边界。
15. [ ] 执行 `go test -race`、关键包 `-count=20`、全仓 integration `-p 1`、`go vet`、`make test`、go-review、sql-code-review、独立审查、提交、归档和 journal。

## Dependency Order

```text
Domain/Contracts -> 00016/V2 Snapshot+Regression -> Store/Vector Builder -> Provider Adapters/Config
                 -> Hybrid Processor -> Candidate Queries -> Search/RRF/Dedup/Rerank -> Integration/Docs
```

Provider Adapter 与 Query Store 可在 Domain 契约冻结后按文件边界并行；Migration/Repository、
公共 Application Interface 和最终整合由主 Agent 串行处理。

## Validation

```bash
go test -race ./internal/retrieval/... ./internal/platform/models ./internal/platform/config
go test -race -count=20 ./internal/retrieval/domain ./internal/retrieval/application ./internal/platform/models
ZHIXU_TEST_DATABASE_URL='postgres://...' go test -race -tags=integration -p 1 ./internal/platform/migration ./internal/retrieval/adapter/postgres
ZHIXU_TEST_DATABASE_URL='postgres://...' go test -race -tags=integration -p 1 ./...
go vet ./...
make test
go mod tidy -diff
git diff --check
docker compose -f deploy/compose.yml --env-file .env.example config --quiet
```

## Risk And Rollback Points

- Provider：任何数量/顺序/维度/模型不符立即失败，不把部分批次保存为成功。
- Cache：只缓存同 Workspace+Embedding Version+Content Hash；写入与 Projection 终态同事务。
- Search：两路 filter 必须等集；动态 distance 只能从枚举选择固定 SQL。
- Reindex：V1 FTS-only 与 V2 Hybrid 使用不同 Regression code/hash；配置变化只创建新版本。
- Degraded：Hybrid 可降级，Semantic 不可用必须返回稳定错误；零命中与失败严格区分。
- Rollback：停用 Adapter/Search composition 即可保留旧 FTS Active；00016 有数据禁止 Down。
