# M8-02 实施清单

1. [x] 收紧 Domain/Application：状态映射、daily limit、证据校验、服务器评分 port、Answer/FSRS 原子性、`question_ref` 与失效入口。
2. [x] 追加 `00044`–`00055`、`00058` 前向迁移和 PostgreSQL Repository：状态/原因、receipt、Scorer version、失效传播、批量投影与必要索引。
3. [x] 实现可信评分 Adapter、API/Composition/Worker 接线，更新 OpenAPI、鉴权和路由。
4. [x] 新建严格 Web client、Review 页面与路由，接入 SSE 查询失效和 Workspace cache 清理。
5. [x] 执行归档前获准的 production build/vet、OpenAPI、前端 lint/typecheck/build 与格式门禁，并如实记录当时未获准的动态验证范围。
6. [x] 使用 go-review、code-review-and-quality、sql-code-review 及独立只读复审修复发现项；同步规范与任务状态。
7. [x] 归档后使用 disposable PostgreSQL 补验 Review Repository、并发提交、M8/Review 迁移、真实 API/Worker/Vite 与桌面/390x844 浏览器闭环；修复已关闭 Session 的无效 due 回查和烟测错误过滤问题。

## Validation

- PASS：`go build ./internal/review/... ./internal/platform/scheduler/... ./internal/app ./cmd/api ./cmd/worker`。
- PASS：按上述 package 的 `go list .GoFiles` 逐包执行 production-only `go vet`，没有加载 `_test.go`。
- PASS：`make openapi-check`。
- PASS：`npm run lint --prefix web`、`npm run typecheck --prefix web`、`npm run build --prefix web`。
- PASS：`make compose-auth-check`；共享 `question_ref` key 仅进入 API，Compose 解析模型满足密钥门禁。
- PASS：production Go `gofmt -l`、OpenAPI JSON parse、相关 tracked/untracked 文件 `git diff --check`、路由/Capability 与 SQL 参数化静态核对。
- PASS：Review PostgreSQL integration 全包；并发调度命令在 `-count=20 -race` 下通过。
- PASS：`go test -race -tags=integration -count=1 -p 1 -timeout 60s -run '^(TestM8|TestReview)' ./internal/platform/migration`（45.231s）。
- PASS：`npm run lint --prefix web`、`npm run test --prefix web`（69/69 files，746/746 tests）；真实 smoke 内再次执行 typecheck/production build。
- PASS：`bash deploy/m8-learning-browser-smoke.sh`；fresh migration、真实 API/Worker/Vite、Review FSRS、桌面和 390x844 浏览器均通过，且 response 级错误收集只放行 Path 首次可用前的预期 404。
- 未覆盖：Review Path reservation/hold 的专门并发与 ABANDONED 重开测试；未执行 `EXPLAIN (ANALYZE, BUFFERS)`。迁移包全包在 60 秒门禁内跑到无关 Knowledge/M9 用例后超时，M8/Review 定向 race 已独立通过。
