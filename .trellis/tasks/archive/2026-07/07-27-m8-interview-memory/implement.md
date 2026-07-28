# M8-03 实施清单

1. [x] 在 M8-02 稳定合同上定义 Interview、Gap、Learning Path 与 Memory 的领域模型、不变量、端口和状态机。
2. [x] 追加 `00045`–`00058` 的 M8 前向迁移、PostgreSQL Repository、幂等 receipt、scope/expiry 索引、Interview Completion reservation/digest fence 与有界维护。
3. [x] 接入 Interview 题目选择/评分、难度/deadline/连续追问、报告与 Path 生成，完成其 API/Worker/鉴权/OpenAPI；Memory effective context 仅接入 Interview。
4. [x] 实现 Interview Path、Memory Web 页面、严格 decoder、倒计时、SSE 恢复与可访问性状态；Review Path 客户端、Query 与页面代码已落地。
5. [x] 执行归档前获准的 production build/vet、OpenAPI、前端 lint/typecheck/build、格式和独立审查门禁，并如实记录当时未获准的动态验证范围。
6. [x] 收口 `00059/00060` 与 Review-derived shared Learning Path 的生产代码与静态契约：统一 migration 与 Repository 的 snapshot/digest、Path/Step、command/receipt 和 completed Artifact tuple，以完整 Draft/renderer/`attempt_no` digest fence 隔离重开尝试，分离 Step 读取态与 mutation target，API Composition Root 注入真实 Service，Worker 启动与周期路径调用 24 小时有界 maintenance。
7. [x] 使用 disposable PostgreSQL 动态补验 `00059/00060` 前向契约、Review Path 生产组合、Interview/Memory Repository、真实 API/Worker/Vite，以及桌面/390x844 浏览器主链路。
8. [ ] 补 `00059/00060` 业务数据 Down guard、Review Path reservation/hold 专门并发与 ABANDONED 重开集成用例。

## Validation

- PASS：`go build`（Interview/Memory/Artifact/App/API/Worker 受影响 production packages）。
- PASS：`go vet -tests=false`（同范围）。
- PASS：`make openapi-check`。
- PASS：`npm run lint --prefix web`、`npm run typecheck --prefix web`、`npm run build --prefix web`。
- PASS：production Go `gofmt -l`、JSON parse、路由/Capability 静态核对。
- PASS：Interview PostgreSQL integration、Memory PostgreSQL integration 全包及真实 canonical Session/Path/Step provenance；`00060` trigger 正确投影三个结构化 ID。
- PASS：`go test -race -tags=integration -count=1 -p 1 -timeout 60s -run '^(TestM8|TestReview)' ./internal/platform/migration`（45.231s），以及 `TestReviewLearningPathProductionCompositionCreatesDraftPostgreSQL` race（13.682s）。
- PASS：`npm run test --prefix web`（69/69 files，746/746 tests）与 `bash deploy/m8-learning-browser-smoke.sh`；fresh migration、真实 API/Worker/Vite、Memory expiry、Interview report/Path、Review Answer→Path、桌面/390x844 均通过。
- 已修复：动态 smoke 发现 `00060` 共享 Path 仍继承 Interview 两列 `NOT NULL`，导致 Review Path 持久化失败；Up 现先 `DROP NOT NULL`，Down 在 guard 后恢复 `SET NOT NULL`，并有 Up→Down→Up schema 回归。
- 未覆盖：`00059/00060` 存在业务数据时的 Down guard；Review Path reservation/hold 的专门并发与 ABANDONED 重开。迁移包全包在 60 秒上限内跑到无关 Knowledge/M9 用例后超时，M8/Review 定向 race 已独立通过。
