# M6 Retrieval Indexing And Search 实施清单

## Child Task Map

1. [x] M6-A Retrieval Index Foundation：Schema、Build Manifest、Lexical Builder、领域模型、Repository、批量 Projection 和原子激活。
2. [x] M6-B Reindex Consumer：Outbox Delivery、`CaptureCommittedSourceVersion`、River Job、Index Build 和共享事务完成归约。
3. [x] M6-C Embedding And Hybrid Search：直接 Provider Adapter、Keyword/Trigram/Vector Query、RRF/dedup/rerank/显式降级。
4. [x] M6-D Search API And Integration：OpenAPI、Evidence、EXPLAIN、Compose 与完整写回闭环 smoke。

## Dependency Order

```text
M6-A -> M6-B -> M6-C -> M6-D
              \-------> M6-D
```

M6-B 依赖 M6-A 的版本/激活契约；M6-C 依赖 M6-A 的 Projection；M6-D 同时依赖 B/C。
父任务不直接编写业务代码，只维护跨子任务契约与最终集成验收。

## Validation

```bash
go test -race ./internal/retrieval/... ./internal/changecontrol/... ./internal/workflow/... ./cmd/worker ./internal/app
go test -race -count=20 ./internal/retrieval/...
go vet ./...
make test
node api/openapi/check.mjs
docker compose -f deploy/compose.yml --env-file .env.example config --quiet
git diff --check
```

数据库子任务必须在 disposable PostgreSQL 上执行完整 Goose Up/重复 Up/空数据 Down→Up，
并检查 FTS/vector 查询计划。最终 M6-D 必须证明 Approved Proposal → Safe Writeback →
Reindex → Active Index Search → Proposal/Execution completed 的唯一事实闭环。

## Stop Gates

- M6-A 未证明失败版本不污染 Active，不进入 Consumer。
- M6-B 未证明 duplicate/crash 幂等，不开放 completed 状态。
- M6-C 未证明降级显式和引用正确，不接 Agent/RAG。
- M6-D 未通过真实 PostgreSQL/River smoke，不归档父任务。
