# 实施计划

## 总体节奏

- 建议按 6 个独立子任务执行，父任务只拥有总体要求、依赖关系和最终复评。
- 单人预计 28-44 个工程日；两人并行预计 5-7 周。估算不包含发现既有行为缺陷后的产品决策时间。
- 第一里程碑只完成 WP1-WP3，先把运行风险和质量门禁补齐；通过复评后再决定是否扩大结构重构。

## 当前批准范围

- 用户已批准先实施步骤 0“建立可重复基线”和步骤 1“Health 有界历史”。
- 对应子任务为 `08-05-architecture-quality-baseline` 与 `08-05-health-bounded-history`，均已归档完成；步骤 1 依赖步骤 0 的基线输出。
- 步骤 2-6 未批准、无独立 child，不因现有局部基础或其他任务的顺带修改反推批准。

## 当前实现状态

- 步骤 0：部分完成。可重复静态基线已交付；CI duration/flaky 历史，以及迁移 `00077` 的受控耗时和锁等待观察仍缺。
- 步骤 1：完成。Health Application/Repository/HTTP/OpenAPI/Web 与定向测试、归档证据均已落地。
- 步骤 2、5：未批准、未实现。
- 步骤 3、6：未批准、只有既存局部基础，工作包未完成。
- 步骤 4：未批准、部分顺带实现；前端 record/exact/UUID 已部分共享，Go pilot 和 scalar/array/problem 收敛仍缺。

`2/2 children done` 只表示两个已登记 child 均归档，不能作为父任务完成度。

## 0. 建立可重复基线（部分完成）

- 把 `research/audit-baseline.md` 的统计命令整理成只读脚本或 CI report，避免手工口径漂移。
- 记录现有 Make target、当前 Playwright spec 数量、integration build tag、SKIP、bundle chunk 和 CI 时长。
- 记录 `00077` 在受控测试库上的行数、迁移耗时与锁等待；不修改历史 migration。
- 为后续每个子任务创建独立 Trellis child task，并复制本任务相关 AC 与 spec/research context。

验收：基线可在干净工作树重复生成，结果差异能够解释。

## 1. Health 有界历史（已完成）

1. 为详情与两类历史分页补 domain/application/HTTP 契约测试，先复现超过 256 条历史的无界行为。
2. 定义 observation/decision page DTO、cursor binding、默认/最大 limit 与稳定排序。
3. 将 repository 拆为当前详情、observation page、decision page 三个有界查询。
4. 当前 observation page 的 evidence 使用一次 batch hydration；增加 statement-count 断言。
5. 新增 HTTP route、严格 query 校验、OpenAPI schema 和前端 runtime decoder/query。
6. 迁移 Health 页面为按需读取历史；旧详情数组保持有界兼容并返回截断元数据。
7. 在大 fixture 上运行 EXPLAIN、HTTP deadline、响应体上界和浏览器 smoke。

关键验证：

```bash
go test -timeout=60s ./internal/health/application ./internal/health/http
go test -race -tags=integration -count=1 -p 1 -timeout=60s ./internal/health/adapter/postgres
npm run test --prefix web -- --run src/api/health.test.ts src/features/health/HealthPage.test.tsx
make openapi-check
ZHIXU_TEST_DATABASE_URL='<test-db>' make collection-health-integration
ZHIXU_TEST_DATABASE_URL='<test-db>' make collection-health-browser-smoke
git diff --check
```

回滚点：保留新分页端点；只回退 Web 调用方或旧详情投影，不允许恢复无界 SQL。

## 2. Route 恢复边界（未批准、未实现）

1. 添加仅覆盖 route content 的 Error Boundary 与稳定错误状态。
2. 实现 boundary reset/retry 和 chunk 失败刷新，不吞错误日志。
3. 覆盖 lazy rejection、render throw、retry success、route change 和现有 Suspense pending。
4. 使用真实构建预览验证导航、认证和 Workspace 状态仍可操作。

关键验证：

```bash
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web -- AppShell AppRoutes
npm run build --prefix web
git diff --check
```

回滚点：删除 route content boundary 与测试即可，路由定义不变。

## 3. CI、测试与文档门禁（未批准、工作包未完成）

1. 生成 Make target 与领域路径映射，先用 report-only 模式观察 PR 命中结果。
2. 拆分 Fast、Selected Integration、Full/nightly jobs，并为每个 job 设置明确 timeout。
3. 让强制 integration 在 CI 缺少数据库配置时失败；保留本地显式 skip 的便利性。
4. 将全部当前 Playwright spec 接入 main/nightly，上传 trace、截图、容器与服务日志。
5. 为 Review Learning Path 增加 domain/application/http 单测和独立 integration target。
6. 输出 Go coverprofile 与 Web coverage；先归档和展示关键包趋势，稳定后启用不下降门禁。
7. 增加 `go mod tidy -diff`、`govulncheck`；修正 testing 文档的数据库测试模式。
8. 跟踪两周耗时和 flaky rate，再把稳定的 Selected Integration 从 report-only 改为 PR blocking。

关键验证：

```bash
make test
go mod tidy -diff
go test -timeout=60s ./internal/review/learningpath/... ./cmd/api/...
npm run test:e2e --prefix web
git diff --check
```

Review：使用 `go-review`、`sql-code-review` 和 `code-review-and-quality` 检查 CI 环境、数据库隔离、Secret、timeout、skip 与浏览器诊断产物。

回滚点：新 job 可先降级为非阻断；保留测试和报告，不删除已补覆盖。

## 4. 边界 primitives 试点（未批准、部分顺带实现）

### 4A. Go HTTP

1. 先用表驱动测试固定 Learning Path、Review/Interview 的 UUID、JSON、Content-Type、body limit 和 Problem 行为。
2. 在 `internal/httpapi` 添加最小 helper；API 只接受参数化限制，不 import 业务包。
3. 每批迁移 2 至 4 个 handler，保留领域错误转换。
4. 搜索剩余实现，记录未迁移原因；不追求一次清零。

### 4B. TypeScript API

1. 固定 graph/semantic-links 的 record、exact keys、scalar/array、problem 与 field-path 契约。
2. 添加 wire primitives，先迁移 graph/semantic-links。
3. 再按相似度迁移 2 至 4 个模块一批；每批独立 PR。
4. 增加检查，禁止业务无关 primitive 出现第三份实现。

关键验证：对应 handler/API 单测、`go vet`、Web lint/typecheck/test、OpenAPI check、`git diff --check`。

回滚点：按模块恢复本地 helper；共享 helper 可保留给已验证调用方。

## 5. 热点拆分与模块边界试点（未批准、未实现）

1. 逐模块提取 Worker builder，锁定共享实例 identity、初始化/关闭顺序和 capability readiness。
2. 拆 ReviewSessionPage：route orchestration、command hook、展示组件分别测试。
3. 拆 OrganizingPage，保持同样边界；不同时改 API decoder。
4. 生成跨 Domain import allowlist 并在 CI 阻止新增 edge。
5. 选择 `conversation -> retrieval/agent` 一个最小契约，迁移 owner contract 与 consumer mapping。
6. 验证 JSON、数据库字段、hash、幂等和回放完全兼容后，从 allowlist 删除对应 edge。

关键验证：

```bash
go test ./cmd/worker/... ./internal/conversation/... ./internal/agent/... ./internal/retrieval/...
go vet ./cmd/... ./internal/...
npm run test --prefix web -- --run ReviewSessionPage OrganizingPage
npm run lint --prefix web
npm run typecheck --prefix web
git diff --check
```

Review：Go 改动使用 `go-review`，前端与跨层契约使用 `code-review-and-quality`；涉及 SQL 映射时追加 `sql-code-review`。

回滚点：一个 builder、一个页面、一个 Domain edge 各自独立提交。

## 6. 性能与契约治理（未批准、工作包未完成）

1. 将 Vite manifest/bundle report 作为 CI artifact，按当前 gzip/raw 基线设置 5% 回归预算。
2. 用 Playwright 网络 trace 测量编辑器冷启动；只在确认未使用 worker/语言后做裁剪。
3. 对优化前后使用相同浏览器、缓存状态和硬件条件复测，并覆盖 Markdown/Diff Viewer。
4. 枚举 runtime route path/method，与 OpenAPI 做全量差异报告；先用有 owner 的 allowlist 处理动态路由。
5. 为 OpenAPI 单一事实源写 ADR，比较保留手写 spec、从 route/schema 生成、生成客户端三种方案；本轮不直接迁移。
6. nightly 生成 SBOM/许可证/依赖更新报告，观察误报后再决定阻断策略。

关键验证：Web build/bundle budget、Playwright cold-load smoke、OpenAPI route report、`govulncheck`、`git diff --check`。

## 最终验收

1. 运行 `make test`、所有受影响 integration/fault/browser gate、全部当前 Playwright spec、`go mod tidy -diff`、`govulncheck` 与 `git diff --check`。
2. 重跑九维评分与审查统计，逐项关联 PRD AC，而不是只报告总分。
3. 确认没有新增 domain reverse dependency、无界查询、未分页列表、重复业务规则或 CI 静默 skip。
4. 更新 `.trellis/spec/` 中稳定的新契约，记录 Health 分页、CI 分层、boundary primitives 和依赖门禁。
5. 父任务只在全部子任务验收或明确延期并记录 owner 后归档。
