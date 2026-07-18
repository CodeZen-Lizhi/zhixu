# Research: M6-D Compose 与 API Smoke

## Planning Resolution

M6-D 实施清单已采用双层证据：保留真实 PostgreSQL/River response-loss fault smoke，并新增唯一 Compose
project 的生产二进制黑盒 API smoke。Compose 默认 Embedding disabled，期望 Hybrid 明确退化但 Keyword
真实命中新正文；API/Worker 将复用同一 Embedder Factory 与配置环境块。

- Query: 核对现有 Makefile、deploy/compose.yml、cmd/api/cmd/worker composition，设计可复用的真实 Compose/API smoke 与列出缺口、影响文件和风险。
- Scope: internal
- Date: 2026-07-19

## Findings

### Files Found

- `Makefile`：当前只有 OpenAPI 静态检查、Compose config、build/up/down；没有行为级 API smoke。
- `deploy/compose.yml`：PostgreSQL 18 + pgvector、一次性 migrate、API、Worker、healthcheck 与 Workspace bind mount。
- `.env.example`：Worker Reindex/Embedding/RRF 配置；Embedding 默认 disabled。
- `deploy/Dockerfile`：构建 API/Worker/Migrate，runtime UID/GID 为 `10001`。
- `cmd/api/main.go`：Workspace、Workflow、Change Control、Ingestion composition；未接 Retrieval Search。
- `cmd/worker/main.go`：Safe Writeback、Reindex Dispatcher、Reindex River Worker、Embedding/Vector/Regression/Completion 的完整 production composition。
- `internal/app/router.go`：HTTP module composition seam；当前 Dependencies 没有 Retrieval。
- `api/openapi/openapi.json`、`api/openapi/check.mjs`：OpenAPI 3.1 文档与存在性检查；当前没有 Search operation/schema/security scheme。
- `README.md`、`docs/architecture/deployment.md`：现有 Compose health 手册与发布 smoke 命令。

### Current Behavior And Composition

1. `make compose-check` 只运行 `docker compose ... config --quiet`（`Makefile:43`）；`compose-up` 只等待服务 health（`:49`）。本轮两个命令的静态门禁结果：
   - `make compose-check`：通过。
   - `make openapi-check`：通过，但当前检查集合不含 Search。
2. Compose 启动顺序是 PostgreSQL healthy → migrate completed → app/worker（`deploy/compose.yml:17`-`:48`、`:104`-`:106`）。这能证明启动依赖，不证明 Approval、River、Reindex 或 Search 行为。
3. API health 只 ping PostgreSQL（`internal/app/router.go:68`、`:158`）；Worker readiness 额外要求 River schema/client、Registry、Safe Writeback、Reindex Worker 和 Dispatcher（`cmd/worker/main.go:134`-`:164`）。
4. Worker composition 已完整接线：
   - Safe Writeback Node/Bootstrap/Runtime（`cmd/worker/main.go:276`-`:343`）。
   - 同一 River Workers bundle 注册 Workflow Worker 与 Reindex Worker（`:362`-`:369`）。
   - Reindex Capture → Ingestion → Retrieval → Vector → Regression → Completion → Dispatcher（`:384`-`:499`）。
5. API composition 没有 `retrievalpostgres.NewSearchRepository`、`retrievalapplication.NewSearchService` 或 Retrieval HTTP Handler；`internal/app.Dependencies` 也没有 Retrieval 字段（`cmd/api/main.go:147`、`internal/app/router.go:33`）。
6. `internal/retrieval/http` 目录不存在；OpenAPI path 列表也没有 `/api/v1/search` 或 Workspace-scoped Search。
7. Compose 只把 Embedding/RRF 配置注入 Worker（`deploy/compose.yml:83`-`:99`），app 没有同一组配置。即使 M6-D 在 API 内构造 Query Embedder，Hybrid/Semantic 查询也会因 API 与 Worker 配置不一致而降级或失败。
8. `newConfiguredEmbedder` 和 `configuredRRF` 位于 `cmd/worker` 的 `package main` 私有函数（`cmd/worker/main.go:502`、`:528`），`cmd/api` 无法复用；直接复制会形成第二套 provider/config 事实源。

### Reusable Fixtures

1. `TestApprovalDispatchRealRiverSafeWritebackSmoke` 已拥有最完整的业务 seed：临时 Git Workspace、base commit、真实 Workspace Repository、Proposal creation、HTTP Approval、双 Workflow Worker 与唯一性断言（`internal/changecontrol/application/approval_dispatch_river_smoke_integration_test.go:43`-`:266`）。
2. `runReindexRiverFaultSmoke` 已拥有 Reindex Dispatcher、双 Reindex Worker、Embedding httptest、全部 checkpoint/Ready/Completion response-loss 与 Hybrid Search 断言（`internal/changecontrol/application/reindex_river_fault_smoke_integration_test.go:46`-`:231`）。
3. 以上 fixture 都在 `application_test` 的 `_test.go` 私有作用域，不能被 `cmd/api`、新的 Retrieval HTTP integration 或外部 Compose script 直接导入。
4. Compose Workspace fixture 可以复用业务内容与断言，但应改成黑盒 API 驱动：宿主创建独立 temp Git repo，挂载到 `/workspace`，API 只接收 `/workspace`，不能把宿主绝对路径写入容器数据库。

### Recommended Smoke Layers

建议保留两层，避免用一次 Compose health 替代故障恢复：

#### Layer A: in-process real PostgreSQL/River fault smoke

继续运行现有：

```bash
ZHIXU_TEST_DATABASE_URL='postgres://.../postgres?sslmode=disable' \
  go test -race -tags=integration -count=1 -v \
  ./internal/changecontrol/application \
  -run '^TestApprovalDispatchRealRiverSafeWritebackSmoke$'
```

它负责 response-loss、双 Worker、唯一 Activation/Active/Completion、敏感信息 canary，不应移除或被 Compose smoke 取代。

#### Layer B: production binary Compose/API smoke

建议新增 `make compose-api-smoke`，脚本必须使用唯一 Compose project 与临时 Workspace：

1. 创建临时 Git Workspace：受支持 Markdown 文件、initial commit、可写权限；设置 `COMPOSE_PROJECT_NAME=zhixu-smoke-<unique>`、独立宿主端口和 `ZHIXU_WORKSPACE_ROOT=<temp>`。
2. 使用 trap 始终执行该唯一 project 的 `down -v --remove-orphans`；不得清理开发者默认 project/volume。
3. `docker compose ... up -d --build --wait`，随后分别验证 API `/readyz` 和容器内 Worker `/readyz`。
4. `POST /api/v1/workspaces` 创建 `/workspace`，记录 `workspace_id`。
5. 计算 fixture 文件 base hash，`POST /api/v1/workspaces/{id}/proposals`，带稳定 `Idempotency-Key`；从响应读取 `proposal_id`、`revision_id`、`change_hash`。
6. `POST /api/v1/proposals/{id}/approvals` 批准 revision；断言 `201`、`workflow_run_id` 与 status URL。
7. 轮询 `GET /api/v1/proposals/{id}`，允许先见 `verifying`，最终必须是 `completed`；超时诊断同时输出 Workflow、Proposal、Worker logs 的稳定错误码，不输出正文/密钥。
8. 调用新增 Search API，首个 Compose smoke 使用 `keyword` 模式，因为 `.env.example` 默认 Embedding disabled；断言：
   - 非空 item；
   - `workspace_id`、`index_version_id`、`chunk_id`、Span、SourceVersion provenance、阶段分数存在；
   - `index_degraded_capabilities=["vector"]` 是持久 Index 事实，不被包装成 Hybrid 成功。
9. 打开每个 provenance 的 evidence URL，断言返回同一 SourceVersion/Span、相同行/byte range、同一 content hash，并且内容来自 committed artifact，不是写回后的 worktree drift。
10. 再次提交同一 Approval，断言 exact replay；再次 Search，断言相同 Active Index 和稳定首项身份。
11. 可选使用容器内 `psql` 做黑盒后置事实断言：一个 Writeback Execution、一个 Reindex Delivery、一个 Activation、一个 Active、Proposal/Execution completed。DB 断言是 smoke 诊断，不取代 API 契约。
12. 扫描 API/Worker logs，禁止 Workspace host path、正文 canary、Embedding key、DSN 出现。

若必须在 Compose 内验收 Hybrid，可增加仅在 `smoke` profile 启用的 deterministic OpenAI-compatible stub sidecar，并把完全相同的 Embedding config 注入 app 与 worker；不得访问真实外部模型，也不得把 stub 放入生产默认 profile。

### Makefile And Compose Recommendations

- `make compose-check`：保留静态渲染检查。
- `make compose-up/down`：保留开发体验，不承载 CI smoke。
- 新增 `make integration-smoke`：真实 disposable PostgreSQL/River fault smoke。
- 新增 `make compose-api-smoke`：唯一 project、生产 binary、真实 HTTP、自动清理。
- `make test` 是否包含行为级 smoke 应由 CI 环境决定；本地无 Docker/DB 时不能静默把“未运行”写成通过。可让 `make release-check` 显式串联 integration + compose smoke。
- Compose 可使用 `x-embedding-environment` extension/anchor，让 app 与 worker 共享 provider/model/dimensions/limits，避免复制 12 个变量后漂移。

### Impact Files

- `internal/retrieval/http/handler.go`、`handler_test.go`：Search/Evidence HTTP 边界。
- `internal/app/router.go`、`router_test.go`：注册 Retrieval routes。
- `cmd/api/main.go`、`main_test.go`：Search Repository、Query Embedder、Reranker nil、Handler composition。
- `cmd/worker/main.go`、`main_test.go`：抽取/复用 Embedder factory 后保持 Worker 行为一致。
- 建议新增 `internal/retrieval/composition/**` 或 `internal/platform/models/factory.go`：只接受显式 options 的 adapter factory；不得自行读取环境变量。
- `deploy/compose.yml`、`.env.example`：app/worker 共享 Query Embedding 配置；可选 smoke profile stub。
- `Makefile`：`integration-smoke`、`compose-api-smoke`、可选 `release-check`。
- 建议新增 `scripts/compose-api-smoke.sh` 或 `test/smoke/**`：黑盒 smoke 编排；脚本只持有临时路径和非敏感 fixture。
- `api/openapi/openapi.json`、`api/openapi/check.mjs`：Search/Evidence paths、schemas、错误、cursor/security assertions。
- `README.md`、`docs/architecture/deployment.md`、`docs/architecture/testing-and-evaluation.md`：真实命令、边界与失败诊断。

### Risks

1. 默认 project 上执行 `compose-down -v` 会删除开发数据库；smoke 必须使用唯一 project name。
2. runtime UID `10001` 必须能写挂载 Workspace；临时目录权限和 Git ownership 需要在启动前校验（`deploy/Dockerfile:18`-`:26`）。
3. API 与 Worker Embedding config 漂移会造成新 Active Hybrid Index 可构建但 API 无法生成匹配 query embedding。
4. 真实 Provider 会引入网络、成本、配额和非确定性；Compose smoke 应使用 keyword 或 deterministic stub。
5. healthcheck 只证明依赖可用，不证明 Dispatcher 正在推进指定 Delivery，也不证明 Search API 已接线。
6. 轮询只看 Proposal completed 会遗漏“错误 Active Index 被搜索”；Search response 必须与 DB/Activation 的 `index_version_id` 绑定。
7. 当前 OpenAPI check 只检查 operation/schema 存在，不做 handler-to-spec 自动生成或响应 schema round-trip；需要增加 Search DTO contract tests 防漂移。

### Related Specs

- `.trellis/spec/backend/index.md`：Composition Root、API/Worker 独立运行、River 与 Workflow 事实边界。
- `.trellis/spec/backend/quality-guidelines.md`：Docker Smoke、E2E、真实数据库与发布门禁。
- `.trellis/spec/backend/logging-guidelines.md`：日志/Trace/Metric 脱敏和 bounded labels。
- `.trellis/spec/guides/cross-layer-thinking-guide.md`：API DTO、Application、DB、Compose 的跨层 contract。
- `docs/architecture/deployment.md`：现有 Compose 发布手册。
- `docs/architecture/testing-and-evaluation.md`：M6-B/C fault smoke 不得被健康检查替代。

## Caveats / Not Found

- 本轮只执行了 `make compose-check` 与 `make openapi-check`，均通过；没有启动 Compose stack，也没有改动现有 `workspace/` 或数据库 volume。
- 未发现现有生产 binary 的行为级 Compose/API smoke target 或脚本。
- 未发现可供 app 与 worker 共同使用的 Query Embedder composition factory。
- 未发现 Search API、Evidence 打开 API、OpenAPI Search schema 或认证 security scheme。
