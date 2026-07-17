# M6-A Retrieval Index Foundation 实施清单

1. [x] 新增 Domain 状态、Embedding 契约、Manifest/Projection/Activation 校验和纯函数测试。
2. [x] 新增 `00014_retrieval_index_foundation.sql`：pg_trgm、五张表、Manifest、复合 FK、唯一 Active、向量维度和不可变约束。
3. [x] 引入官方 `pgvector-go`，在项目 pgx Pool `AfterConnect` 注册 vector 类型并补 Pool 测试。
4. [x] 新增 Application Service/Store Port：Register Embedding、Begin Index+Manifest、Build Lexical、SaveVectorBatch、Ready/Fail、Activate/Rollback、精确读取与 GetBuildStatus。
5. [x] 实现 PostgreSQL Repository 的精确重放、Manifest 冻结、批量 Lexical/Vector Projection、乐观锁和原子激活。
6. [x] 增加真实 PostgreSQL 集成测试：Up/重复 Up/Down guard、跨 Workspace、维度、重复、并发双激活和响应丢失重放。
7. [x] 执行 FTS GIN、gin_trgm_ops 与关键唯一索引 EXPLAIN，记录实际查询计划。
8. [x] 同步 database/retrieval/testing 文档与 backend Spec，明确 exact vector/HNSW 后续边界。
9. [x] 执行全量门禁、go-review、sql-code-review、独立审查、提交、归档和 journal。

## Validation

```bash
go test -race ./internal/retrieval/...
go test -race -count=20 ./internal/retrieval/domain ./internal/retrieval/application
ZHIXU_TEST_DATABASE_URL='postgres://...' go test -race -count=1 ./internal/retrieval/adapter/postgres
go vet ./...
make test
git diff --check
```

## Risk And Rollback Points

- Migration：只新增 00014；若空库/现有库升级不一致，停止在 Repository 实现前修正。
- 向量维度：不锁具体模型，不创建错误维度 HNSW；只校验每个 Embedding Version 内一致。
- Activation：任何并发/重放测试失败都不得继续 M6-B。
- Down：任意 Retrieval 业务数据 fail closed；普通回滚保留 Schema，切回旧应用即可。

## Checkpoints

### Schema/Domain Gate

- Migration、状态、Manifest、Embedding/Projection 绑定测试全部通过后才实现 Repository 激活。

### Repository/Activation Gate

- Lexical Builder、Ready 完整性、并发双激活与响应丢失重放全部通过后才进入 M6-B。
