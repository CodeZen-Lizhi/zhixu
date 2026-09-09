# 代码质量审查基线

## 审查结论

- 综合评分：`77/100`。
- 定位：生产级标准下的中上水平模块化单体；架构、安全、类型和测试资产较强，主要差距在运行时上界、CI 真实覆盖、边界重复和高变更热点。
- 评分维度：架构与模块边界、正确性与领域不变量、复用、可读性与复杂度、测试与 CI、类型与 API 契约、安全与数据保护、性能与容量、文档与工程治理。

## 规模与结构基线

- Go：1,327 个文件，其中 628 个测试文件；生产代码约 205,831 行，测试约 175,807 行。
- Web：146 个生产 TS/TSX 文件、35,514 行；107 个单测文件、24,278 行；7 个 Playwright E2E spec。
- SQL：77 个 migration，约 30,818 行。
- 生产文件超过 1,000 行：37 个，共 47,639 行。
- 主要热点：
  - `cmd/worker/main.go`：2,582 行；`newWorkerComponentsWithModels` 从 `cmd/worker/main.go:1052` 开始，约 613 行。
  - `web/src/api/review.ts`：2,174 行。
  - `cmd/api/main.go`：1,872 行。
  - `internal/platform/config/config.go`：1,790 行。
  - `internal/changecontrol/adapter/localfs/writer.go`：1,659 行。
- 重复边界 helper 的静态基线：12 个后端 `parseID`、17 个同名 `decodeJSON`（含非 HTTP codec）、22 个 `writeError`、24 个前端 API `isRecord`、31 个常见 UUID reader/validator 命名实现。
- Domain 文件中存在 17 条跨 Domain import 语句，形成约 10 条模块边；未发现 Domain 反向依赖 adapter/http。

## 已证实风险

### 1. Health 详情无界读取完整历史

- `internal/health/adapter/postgres/read_repository.go:115` 明确读取完整 observation/evidence/decision 历史。
- `internal/health/adapter/postgres/read_repository.go:188` 与 `:243` 的历史查询无 limit。
- `internal/health/http/handler.go:226` 的详情入口没有分页参数。
- `internal/health/application/read.go:114` 把详情定义为“完整可审阅历史”。
- 当前 Web 详情在 `web/src/features/health/HealthPage.tsx:89` 只展示 `issue`，没有消费完整 observation/decision 数组；`web/src/api/health.ts:292` 仍严格要求并最多解码 256 项。
- 结论：这是当前唯一明确的无界数据路径，应优先修复；可以通过增量 API 迁移降低现有调用方风险。

### 2. CI 未执行多数真实门禁

- `.github/workflows/ci.yml:54` 只运行 `make test`。
- `Makefile:6` 的默认门禁不包含大多数 PostgreSQL/River/Fault/Playwright 套件。
- `.github/workflows/ci.yml:73` 只额外执行 Collection/Health 专项数据库门禁。
- 仓库有 169 个 integration 命名测试文件、162 个 integration build tag 文件、123 处 `t.Skip`，但缺少完整 CI 映射。
- `docs/architecture/testing-and-evaluation.md:517` 已要求 PR 跑 Selected Integration，主分支/发布跑 Full Integration、E2E、Security、Evaluation 和 Docker Smoke。

### 3. Review Learning Path 缺少默认测试

- 约 2,437 行生产代码的模块测试主要受 integration tag 保护。
- 证据入口：`internal/review/learningpath/adapter/postgres/review_learning_path_integration_test.go:1`、`cmd/api/review_learning_path_integration_test.go:1`。
- 默认 `make go-test` 不验证其 domain/application/http 的关键规则。

### 4. 前端路由异常没有统一恢复边界

- `web/src/routes/AppRoutes.tsx:7` 起定义 29 个 lazy route。
- `web/src/app/AppShell.tsx:298` 只使用 Suspense 包裹 Outlet。
- 唯一 error boundary 是 `web/src/features/business/ProposalsPage.tsx:75` 的 Diff Viewer 专用边界。

### 5. 边界代码重复并出现契约差异

- `internal/review/learningpath/http/handler.go:340` 使用会 trim/lowercase 的 `foundation.ParseID`。
- `internal/review/http/handler.go:1043` 拒绝带空白或非 canonical UUID。
- `internal/foundation/id.go:16` 是宽松规范化事实源。
- 前端 26 个 API 模块各自维护大量 record、UUID、exact-key、array、problem 解码逻辑。

### 6. 高变更热点和跨 Domain 直接耦合

- `internal/conversation/domain/answer.go:10` 依赖 agent/domain。
- `internal/conversation/domain/question.go:13` 依赖 retrieval/domain。
- `internal/graph/domain/candidate.go:14` 依赖 knowledge/domain。
- `docs/architecture/module-architecture.md:11` 与 `:17` 要求模块通过小而明确的 Interface 协作。
- 前端热点包括 `web/src/features/review/ReviewSessionPage.tsx:459`、`web/src/features/organizing/OrganizingPage.tsx:479`。

### 7. 构建与工程治理缺口

- 当前构建警告：`editor.api` 约 2.66 MB（gzip 682 KB）、`monaco-runtime` 约 1.28 MB（gzip 325 KB）、TypeScript worker 约 6.92 MB、入口约 466.8 KB。
- `docs/architecture/testing-and-evaluation.md:54` 描述 Testcontainers，而 `:155` 描述外部 `ZHIXU_TEST_DATABASE_URL`；`go.mod` 没有 Testcontainers 依赖。
- CI 只有 npm 高危审计，没有 Go `govulncheck`、覆盖率趋势、SBOM 或依赖更新策略。
- `api/openapi/check.mjs` 约 3,531 行，`api/openapi/openapi.json` 约 29,658 行；检查器只直接解析少量 handler，运行时路由覆盖仍有漂移风险。

## 迁移 00077 的优先级校正

- `migrations/00077_workspace_git_capture.sql:5` 在已有 `core.source` 上使用普通 `CREATE INDEX`，直接或滚动升级可能阻塞写入。
- 该文件已经由提交 `903ffbf` 进入 Git 历史；`.trellis/spec/backend/database-guidelines.md:63` 禁止修改已发布迁移。
- 标准 `zhixu` 启动流程在迁移前停止 Controller、撤销 Workspace runtime，再启动基础栈，降低当前支持部署方式中的在线写锁风险。
- 因此该项不是当前标准部署路径的无条件 P1。规划结论：验证并文档化停机迁移前提；如果未来必须支持不停机滚动/直连升级，单独设计前置 schema preparation 或迁移发布机制，不能简单改写历史 migration。

## 已验证优势与保护约束

- 模块化单体方向正确，不建议改为微服务。
- Domain 验证、状态机、幂等、CAS、事务与错误分类整体扎实。
- Collection 动态 SQL 使用固定 registry 和参数绑定；未发现 SQL 注入证据。
- 认证、CSRF/Origin、loopback 限制、SSRF/DNS pinning、重定向/响应体上限、路径和 Secret 脱敏实现较强。
- TypeScript 启用 `strict`、`noUncheckedIndexedAccess`、`exactOptionalPropertyTypes`；生产显式 `any` 为 0。
- TanStack Query、AbortSignal、Query Key、SSE Workspace 隔离与恢复模式成熟。
- 优化必须保持上述不变量，不用大重构换取表面行数下降。

## 已执行验证

- `make test`：通过，包括 Go 单测/vet、Web lint/typecheck/1,136 个测试、Controller 99 个测试、生产构建、Eino race/vet、离线 Agent Eval、OpenAPI 与 Compose 静态契约。
- `go mod tidy -diff`：通过。
- `git diff --check`：通过。
- 未覆盖：全部外部 PostgreSQL/River/Fault/Capacity 门禁和全部 7 个 Playwright E2E。
