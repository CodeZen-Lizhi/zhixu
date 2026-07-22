# M9 业务前端、Diff 与统一 SSE UX 实施计划

## Ordered Tasks

| ID | Work | Depends | Exit evidence | Status |
|---|---|---|---|---|
| T01 | 锁定 list/read contract、cursor、UI tokens 与 shadcn primitive seam | none | PRD/design review、接口测试草案 | completed |
| T02 | 新增 Source Version/Inbox list read model、HTTP、OpenAPI 与 PG integration | T01 | cursor/filter/isolation/query-count tests | completed：真实 PostgreSQL、Workspace 隔离、筛选/分页与 EXPLAIN 有界 Limit 通过 |
| T03 | 新增 Proposal list 与安全 current-content/preflight read contract | T01 | two proposal types、drift、path/size/isolation tests | completed |
| T04 | 新增 Workflow list/read projection 与 HTTP/OpenAPI | T01 | status/cursor/waiting-human/version tests | completed |
| T05 | 引入 Radix/CVA/Lucide，建立 shadcn open-code primitives、tokens、App Shell | T01 | component a11y tests、现有路由无回归 | completed |
| T06 | 实现严格 Source Version/Proposal/Workflow web API decoder、query keys/hooks | T02-T04 | invalid/unknown/cursor/problem/abort tests | completed |
| T07 | 实现 Unified SSE Event Store，并迁移 RAG 到单连接 Owner | T05,T06 | reconnect/recovery/workspace switch/invalidation tests | completed |
| T08 | 实现 Dashboard、Inbox、Documents、Settings | T05-T07 | loading/empty/error/degraded/pagination/refresh tests | completed |
| T09 | 实现 Proposal list/detail、Markdown/Relation Diff、preflight、批准/驳回 | T03,T05-T07 | risk confirm/conflict/idempotency/focus tests | completed |
| T10 | 实现 Workflow list/detail/control 与异步 UX | T04,T05-T07 | pause/resume/cancel/version conflict tests | completed |
| T11 | 路由 lazy split、全局导航、URL filters、Workspace cache isolation | T08-T10 | deep-link/refresh/mobile tests | completed |
| T12 | 真实 PostgreSQL/API/浏览器 smoke、性能/secret/audit gate | T02-T11 | all gates pass | completed：全量 Go/Web/OpenAPI/Trellis/diff 门禁通过；新建迁移至 33 的 disposable PostgreSQL 后，Workspace/Proposal/Workflow 三条列表 `-race` integration、M9 migration contract 与 Semantic Link fault smoke 通过；浏览器真实链路证据保持有效 |
| T13 | 同步 PRD/架构/OpenAPI/spec/父任务状态并执行主审查和独立复审 | T12 | no open P0-P2 | completed：补齐产品/前端/API/数据库/Checklist 状态；主 Agent 使用 `go-review`、`code-review-and-quality`、`sql-code-review` 完成审查；独立 reviewer 第三轮关闭 Semantic Link detail `approval` fail-open 后未发现新的 P0-P2 |
| T14 | scoped commit、archive、journal；不 push | T13 | clean scoped task result | pending |

## Verification Commands

```bash
go test -race -count=1 ./internal/changecontrol/... ./internal/workflow/... ./internal/retrieval/... ./internal/events/... ./internal/app/... ./cmd/api/...
go vet ./internal/changecontrol/... ./internal/workflow/... ./internal/retrieval/... ./internal/events/... ./internal/app/... ./cmd/api/...
ZHIXU_TEST_DATABASE_URL='postgres://...' go test -race -tags=integration -count=1 -p 1 ./internal/changecontrol/... ./internal/workflow/... ./internal/retrieval/... ./cmd/api/...
npm ci --prefix web
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web
npm run build --prefix web
npm audit --prefix web --audit-level=high
make openapi-check
python3 ./.trellis/scripts/task.py validate 07-22-m9-business-frontend
git diff --check
```

## Browser Gate

- 真实 API + PostgreSQL，桌面 1440×900、移动 390×844。
- Dashboard → Inbox → Document → Proposal → approve/reject conflict → Workflow control → Settings。
- 验证 URL 恢复、分页、SSE reconnect、Workspace switch、Dialog/Sheet focus、Escape/return focus。
- console error/warning 为 0；无横向溢出；网络中无重复 SSE 连接。

## Review Gate

- 主 Agent：`go-review`、`code-review-and-quality`；如 SQL 查询改动，追加 `sql-code-review`。
- 独立审查子 Agent：后端/SQL/API 与前端/跨层至少一轮；修复后同一审查者复验，最多两轮。
- 重点检查：第二事实源、cursor/filter binding、Workspace 隔离、Source 正文泄漏、approval optimistic success、SSE 双连接、路由包体和测试假阳性。

## Rollback Points

- T02-T04：新增 GET/OpenAPI 为 additive，可逐模块撤回，不触碰 Schema。
- T05：shared UI seam 可回退为原生组件，不改 Feature interface。
- T07：保留原 SSE connector；若 Provider 回归，可恢复 RAG hook，其他页面临时使用 Query polling，但不得显示实时成功。
- T08-T11：逐路由撤下入口，既有 Workspace/Graph/Chat 路由保持可用。
- 不回滚或覆盖 M7-03 的未提交改动。

## 2026-07-22 Verification And Review Record

- 已通过：M9 相关 Go test、race、vet；OpenAPI check；M9 ESLint；M9 定向 TypeScript 编译；前端 focused 4 files/24 tests；production build、dependency audit、diff check、Trellis validate 在独立审查前全量通过。
- 浏览器：1440×900 与 390×844 Inbox/Shell 无横向溢出，筛选 URL 可恢复，console 0 error/warn；当时只启动 Vite，未连接真实 API。
- 独立审查首轮发现并已修复：首次批准误用 post-approval preflight、详情 Workspace 绑定缺失、Active Index/excluded 投影错误、cursor 缺 kind/version、Business decoder 语义校验不足。
- 修复后全量前端 lint/typecheck 当前被并行 M7-03 的 `web/src/api/collections.ts`、`web/src/features/collections/CollectionsPage.tsx` 未提交错误阻断；M9 定向门禁为绿色，未越界修改 M7 文件。
- 环境盲区：`ZHIXU_TEST_DATABASE_URL` 未配置，因此未运行三个列表 SQL 的真实 PostgreSQL integration 与 `EXPLAIN (ANALYZE, BUFFERS)`；Source latest-attempt 查询的索引计划仍需真实数据验证。
- Approval 契约澄清：`apply-preflight` 只允许已经 approved 的 file patch，用于写回前检查；首次批准直接调用 Approval 命令，服务端在该命令内重新校验 Revision、Change Hash、当前文件 Hash 与 Git。
- 第二轮复验后修复：approved file patch 新增独立 Apply Preflight UI；preflight/Approval/Workflow Control 响应全部严格校验 UUID、Hash、状态和请求绑定；Knowledge Proposal 不再触发 current-content；409 会关闭确认框、回查权威事实并显示错误；Dialog 关闭恢复决策触发按钮焦点。
- 第二轮新增前端验证：M9 focused 4 files/31 tests、M9 ESLint、M9 scoped TypeScript、全量 28 files/312 tests、独立 Vite production bundle 均通过；canonical `npm run typecheck/lint/build` 仍被并行 M7 Collections 文件阻断。
- Active Index `excluded` 维持 Schema 定义的 Source 级语义（manifest `source_version_id=NULL`），OpenAPI 与 Inbox 文案已明确为“来源已排除”，不是单个历史版本的 included 绑定。
- 最终前端门禁：全量 ESLint、TypeScript、34 files / 352 tests、production build 全部通过；M9 focused Proposal/API/Event Store 为 3 files / 30 tests。
- 最终后端门禁：M9 相关 Go `-race`、`go vet`、OpenAPI check、Trellis validate、`git diff --check` 通过；三条列表真实 PostgreSQL integration/EXPLAIN 在 `-race` 下通过，根节点均为有界 `Limit`。
- 浏览器真实链路：Dashboard → Inbox → Document → Proposal → Workflow → Settings 已在 1440×900 与 390×844 完成；URL 恢复、Dialog/Sheet 焦点、Hash 漂移、Git Dirty、Workflow 控制、唯一 SSE 连接、console 0 error/warning 均通过。
- 依赖审计：`npm audit --audit-level=high` 通过；Monaco 间接依赖 DOMPurify 仍有 1 low + 1 moderate，强制修复会破坏性降级 Monaco，留待依赖上游修复。
- 全量 `-tags=integration` 在共享测试库运行时，范围外 Change Control fixture 出现重复 Workspace 主键和幂等键冲突；已用任务锁定的三条列表测试独立复验并通过，不将共享 fixture 冲突误记为 M9 回归。
- 主审查使用 `go-review`、`code-review-and-quality`、`sql-code-review`，未发现剩余 P0-P2。独立审查第二轮发现的 Knowledge Approval 响应和 Approval snapshot 绑定问题已修复；已达到两轮复验上限，最终修复由主 Agent 通过 API-boundary 回归测试和全量门禁复核。
- 2026-07-22 当前复验：前端 ESLint、TypeScript、37 files / 368 tests、production build、`npm audit --audit-level=high`、三条真实 PostgreSQL list integration/EXPLAIN、OpenAPI、Trellis validate 与 `git diff --check` 通过；主包约 400.84 kB，Graph/RAG/Workspace 保持独立 route chunks。
- 当前真实浏览器复验：1440×900 与 390×844 的 Dashboard、Inbox、Document、Proposal、Workflow、Settings 通过；移动 Sheet、Settings 横向 Tabs、桌面/移动 Dropdown 的 Escape/焦点恢复通过，无横向溢出，console 0 error/warning，捕获到恰好一个 `/api/v1/events` 请求。
- 最终 Go 复验：`internal/changecontrol`、`internal/workflow`、`internal/workspace`、`internal/retrieval`、`internal/events`、`internal/app` 与 `cmd/api` 的 race/vet 全部通过；此前并行 M7 Collection 的临时编译阻断已解除，无需修改 M7 业务逻辑。
- 2026-07-23 收尾复验：`go test -count=1 ./cmd/... ./internal/...`、`go vet ./cmd/... ./internal/...`、Web lint/typecheck、46 files / 557 tests、production build、`npm audit --audit-level=high`、OpenAPI、Trellis validate 与 `git diff --check` 全部通过；audit 仍仅报告 Monaco 间接 DOMPurify 的 1 low + 1 moderate，强制修复会破坏性降级 Monaco。
- 2026-07-23 使用全新数据库 `m9_verify_fresh_20260723` 从空库迁移至 33 后复验：Workspace/Change Control/Workflow 列表 PostgreSQL `-race` integration 全部通过；M9 Business Contract migration 的 schema/backfill/down-guard 测试与 Semantic Link fault smoke 全部通过。共享旧库的重复 Workspace fixture 失败未作为 M9 证据。
- 2026-07-23 主审查与独立复验均未发现当前范围内 P0-P2；针对 localfs 返回 Workspace 身份未显式比对的防御性问题已补上 `PROPOSAL_WORKSPACE_BINDING_INVALID`，并以 `CurrentHash`/`CurrentContent` 双路径回归覆盖，复验后未发现契约回归。剩余候选仅为 Proposal 创建单独 SSE 事件与 00031 `NO TRANSACTION` Down guard 并发窗口，不改变本任务已冻结契约，且未修改迁移或用户既有改动。
- 2026-07-23 最终契约复验发现并关闭 1 个 P1：`web/src/api/semantic-links.ts` 的 Proposal detail decoder 不再把缺失 `approval` 当成合法 `null`；两种 Proposal 各有缺字段负测。红测 2 failed 后修复为 37/37，Semantic Link + Business 129/129，前端全量 46 files / 569 tests、lint/typecheck/build、`make test`、OpenAPI、Trellis validate 与 diff check 通过；同一独立 reviewer 第三轮复验无剩余 P0-P2。
