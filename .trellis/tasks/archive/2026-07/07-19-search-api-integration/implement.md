# M6-D Search API And Integration 实施清单

1. [x] 冻结 Search HTTP DTO、Cursor v1、Evidence Reference Domain/Application 契约；覆盖默认值、canonical fingerprint、100 窗口、错误矩阵和 SourceVersion/Span binding 单测。
2. [x] 扩展 Retrieval PostgreSQL Repository：Source Version/Span 只读绑定查询、显式列、参数化 Workspace 隔离、NotFound/错误分类与真实 PostgreSQL 集成测试。
3. [x] 实现 `internal/retrieval/http` Handler：`POST /search`、两个 Evidence GET 路由、严格 JSON、Problem 映射、href、degraded/score 映射和 cursor 分页；覆盖正常/边界/失败路径。
4. [x] 提取 Worker/API 共享 Configured Embedder Factory，删除 Worker 私有重复构造；增加 disabled/OpenAI/Ollama/Secret composition 测试。
5. [x] 接入 `internal/app.Router` 与 `cmd/api` Composition Root；数据库不可用/配置错误返回显式 503，Embedding disabled 保留 Keyword/Hybrid degraded。
6. [x] 将 Embedding 配置同步注入 Compose app，更新 `.env.example`/deployment/config 文档并执行 Compose config canary。
7. [x] 更新 OpenAPI 3.1、`check.mjs` 和 Search/SourceVersion/SourceSpan/Score/Degradation/Cursor Schema；验证所有新操作 405/Problem 响应。
8. [x] 新增 `web/src/api/search.ts` 严格 Decoder/Client 及 Vitest，覆盖 mode/capability/finite score/time/UUID/href/cursor 和 Problem。
9. [x] 新增真实 PostgreSQL HTTP Integration/EXPLAIN：三模式、filter 等集、Active-only、cursor stale、Workspace 隔离、Source/Span href 可打开。
10. [x] 扩展 Safe Writeback/Reindex River fault smoke：Completion 后走真实 Router Search 与 Evidence GET，断言 response-loss 重投仍只有一个 Active/Completion。
11. [x] 新增 disposable Compose API smoke：Git Workspace → Scan/Ingestion → Proposal/Approval → Worker Reindex → Hybrid Search degraded → Evidence GET；确保 trap 清理和敏感信息 canary。
12. [x] 同步 PRD、API/Retrieval/Interfaces/Testing/Deployment、安全边界与 backend/frontend code-spec；明确 top-100 cursor、distance、Auth/M10 和 exact-scan/M10 边界。
13. [x] 执行 `go test -race`、关键包 `-count=20`、全仓 integration `-p 1`、`go vet ./...`、`make test`、`go mod tidy -diff`、Docker/Compose smoke、go-review、sql-code-review、独立审查和 Trellis full-scope check。
14. [x] 更新父任务全部 AC 与 M6-01 状态，提交 M6-D、归档子任务/父任务并记录 journal；不 push。

## Dependency Order

```text
Contracts/Cursor -> PostgreSQL Evidence Store -> HTTP Handler -> App Composition
                 -> OpenAPI/Web Decoder -> PostgreSQL/River/Compose Integration -> Docs/Quality Gate
```

- Domain/Application 公共契约与 Cursor 由主 Agent 串行冻结。
- PostgreSQL Store、OpenAPI/Web Decoder、Compose smoke 可在契约冻结后按文件边界并行。
- `internal/app`、`cmd/api`、公共 OpenAPI、fault smoke 和最终整合由主 Agent统一处理。

## Validation

```bash
go test -race ./internal/retrieval/... ./internal/app ./cmd/api ./internal/platform/models
go test -race -count=20 ./internal/retrieval/domain ./internal/retrieval/application ./internal/retrieval/http
ZHIXU_TEST_DATABASE_URL='postgres://...' go test -race -tags=integration -p 1 ./internal/retrieval/... ./internal/changecontrol/application
ZHIXU_TEST_DATABASE_URL='postgres://...' go test -race -tags=integration -p 1 ./...
go vet ./...
make test
go mod tidy -diff
node api/openapi/check.mjs
docker compose -f deploy/compose.yml --env-file .env.example config --quiet
make compose-search-smoke
git diff --check
```

## Stop Gates

- Cursor 未绑定规范请求与 Index Version，不进入 HTTP 集成。
- Source Version/Span href 未能真实打开或跨 Workspace 可枚举，不进入 OpenAPI/前端。
- API/Worker Embedder 配置存在第二事实源，不进入 Compose smoke。
- River fault smoke 未证明唯一 Active/Completion，或 Compose smoke 只测 readiness/404，不归档 M6-D。
- 正式 Auth 尚未完成时，文档与部署不得把非 loopback 暴露描述为可交付生产安全状态。

## Rollback Points

- Handler/OpenAPI：移除新增路由和 Schema，M6-C Application/Store 保持可用。
- Composition：恢复 API 不注入 Retrieval Handler；Worker Embedder Factory 仍可保留为无行为变化重构。
- Compose smoke：独立脚本/Make target，可回滚而不改变运行时数据。
- 本任务无迁移；禁止修改或回滚 `00014`、`00015`、`00016`。
