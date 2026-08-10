# Research: 架构质量优化步骤 0-6 当前代码状态审计

- Query: 对照当前代码、CI、测试、项目文档和已归档子任务，审计 `.trellis/tasks/08-05-architecture-quality-optimization/implement.md` 的步骤 0-6；区分审批状态与实际实现状态，并给出父任务及路线图的推荐措辞。
- Scope: internal
- Date: 2026-08-10

## Findings

### 结论总表

| 步骤 | 审批 / 任务状态 | 当前实现状态 | 判断 |
| --- | --- | --- | --- |
| 0 基线 | 已批准；收窄后的基线子任务已归档完成 | 可重复的静态基线已实现并通过当前测试；父计划要求的 CI 历史时长、易波动记录和 `00077` 受控迁移耗时/锁等待未完成 | 父步骤部分完成；子任务范围完成 |
| 1 Health 有界历史 | 已批准；子任务已归档完成 | API、仓储、前端、OpenAPI 与定向测试均已落地 | 完成 |
| 2 路由恢复边界 | 未批准；无子任务 | 只有 `Suspense` 和一个业务组件局部错误边界；没有路由级错误边界、重试/重置或 chunk 失败恢复测试 | 未实现 |
| 3 分层 CI / 测试 / 文档 | 未批准；无子任务 | 已有单一 `quality` CI、多个手动集成目标和 7 个 E2E spec，但没有 Fast/Selected/Full 分层、nightly、覆盖率/诊断产物或完整 E2E 接线 | 仅有既存基础，工作包未完成 |
| 4 边界 primitives | 未批准；无子任务 | 前端共享 `codec` 已顺带完成 record/exact/UUID 等一部分收敛；Go HTTP pilot 与 scalar/array/problem 收敛未完成 | 部分顺带实现，工作包未完成 |
| 5 热点与边界拆分 | 未批准；无子任务 | 三个目标热点未缩小；Domain 依赖边未减少，也没有 CI allowlist | 未实现 |
| 6 性能与契约治理 | 未批准；无子任务 | 有静态基线、可选 bundle 报告和手写 OpenAPI 检查器，但没有预算门禁、冷启动轨迹、全量路由契约比对、SBOM/依赖治理或相关 CI | 仅有既存基础，工作包未完成 |

审批与实现必须独立表述：父计划明确只批准步骤 0 和步骤 1，步骤 2-6 “暂未批准，不开始实施”（`.trellis/tasks/08-05-architecture-quality-optimization/implement.md:9`）。当前代码中顺带出现的共享 codec 或既有检查器，不能反向视为步骤 2-6 已获批准，也不足以满足其整组验收条件。

### 步骤 0：建立可重复基线

审批与任务状态：

- 父计划要求记录 Make 目标、7 个 Playwright spec、集成标签与 `SKIP`、bundle、CI 时长，以及对迁移 `00077` 的受控数据库行数、耗时和锁等待观察，并为后续工作包建立独立子任务（`.trellis/tasks/08-05-architecture-quality-optimization/implement.md:15`）。
- 归档子任务 `.trellis/tasks/archive/2026-08/08-05-architecture-quality-baseline/task.json:6` 标为 `completed`，其收窄后的验收项均已勾选（`.trellis/tasks/archive/2026-08/08-05-architecture-quality-baseline/prd.md:24`）。

当前实现证据：

- 可重复工具已存在：`deploy/architecture_quality_baseline.py`；Make 入口为 `Makefile:185`。
- 工具测试为 `deploy/architecture_quality_baseline_test.py`；当前执行 `python3 -m unittest discover -s deploy -p architecture_quality_baseline_test.py`，结果 10 项全部通过。
- 当前工具输出可稳定统计：169 个命名集成测试文件、其中 162 个具有集成 build tag；124 个原始 `t.Skip` 命中；7 个 Playwright spec；29 个 Make 质量目标。
- 当前相对归档基准的关键变化：前端本地 `isRecord` 定义从 24 降为 0；Go `decodeJSON` / `parseID` / `writeError` 分别仍为 16 / 12 / 22；Domain 依赖仍为 10 条边、17 次 import、0 条反向依赖；`cmd/worker/main.go` 从 2582 行增至 2609 行。

未完成证据：

- 归档结果本身明确记录：没有可用的 CI 历史时长，也没有迁移耗时与 lock-wait 观测；没有新增 CI 门禁或阈值（`.trellis/tasks/archive/2026-08/08-05-architecture-quality-baseline/result.md:24`）。
- 父任务只登记了基线与 Health 两个子任务（`.trellis/tasks/08-05-architecture-quality-optimization/task.json:21`），步骤 2-6 没有独立子任务。
- 因此应把步骤 0 拆成语义上的 `0A 静态可重复基线（完成）` 与 `0B CI/迁移运行时观测（待补）`，不能把父步骤整体写成已完成。

### 步骤 1：Health 数据有界化

审批与任务状态：

- 步骤 1 已获批准；归档子任务 `.trellis/tasks/archive/2026-08/08-05-health-bounded-history/task.json:6` 标为 `completed`，其验收项均已勾选（`.trellis/tasks/archive/2026-08/08-05-health-bounded-history/prd.md:43`）。

当前实现证据：

- 应用层已实现游标绑定与分页：`internal/health/application/read.go:32`、`internal/health/application/read.go:374`、`internal/health/application/read.go:611`。
- PostgreSQL 读仓储已接受 limit/cursor：`internal/health/adapter/postgres/read_repository.go:32`、`internal/health/adapter/postgres/read_repository.go:254`。
- HTTP 契约测试覆盖分页和错误边界：`internal/health/http/handler_test.go:242`。
- 前端 API、查询与页面消费新分页契约：`web/src/api/health.ts:338`、`web/src/features/health/queries.ts:35`、`web/src/features/health/HealthPage.tsx:206`。
- OpenAPI 检查器验证 Health 分页契约：`api/openapi/check.mjs:3613`。

当前验证：

- `go test -timeout=60s ./internal/health/application ./internal/health/http`：通过。
- `npm --prefix web test -- --run src/api/health.test.ts src/features/health/queries.test.tsx src/features/health/HealthPage.test.tsx`：3 个文件、20 项测试全部通过。
- `node api/openapi/check.mjs`：通过。
- 本次没有重跑真实 PostgreSQL 集成测试或浏览器烟测；归档结果记录了当时的定向后端、前端、OpenAPI 和真实 PostgreSQL 验证（`.trellis/tasks/archive/2026-08/08-05-health-bounded-history/result.md:14`）。

结论：步骤 1 对照其已批准子任务验收条件可判定为完成。

### 步骤 2：前端路由恢复边界

审批状态：未批准、无子任务。

当前代码证据：

- 路由使用 lazy import：`web/src/routes/AppRoutes.tsx:7`，路由树位于 `web/src/routes/AppRoutes.tsx:49`。
- `AppShell` 只以 `Suspense` 包裹 `Outlet`：`web/src/app/AppShell.tsx:405`。
- 对应测试只覆盖 lazy pending 与导航：`web/src/app/AppShell.test.tsx:37`。
- 唯一可检索到的 React 错误边界是 `DiffViewerErrorBoundary`，仅包裹提案页的 DiffViewer：`web/src/features/business/ProposalsPage.tsx:50`、`web/src/features/business/ProposalsPage.tsx:479`。

未找到：

- 路由级 error boundary；
- 页面级 retry/reset；
- chunk 加载失败刷新或回退策略；
- 对上述恢复路径的测试。

结论：局部 DiffViewer 边界不属于父计划定义的路由恢复边界，步骤 2 应标为“未批准、未实现”，而不是“部分完成”。

### 步骤 3：分层 CI、测试基础设施与文档同步

审批状态：未批准、无子任务。

已有基础：

- CI 在 PR 和 `main` / `dev` push 触发（`.github/workflows/ci.yml:3`），但只有一个 `quality` job（`.github/workflows/ci.yml:13`）。
- 该 job 执行 `make test`（`.github/workflows/ci.yml:54`）、静态 collection/health 检查（`.github/workflows/ci.yml:57`）、Collection 与 Health PostgreSQL gate（`.github/workflows/ci.yml:63`）及 Docker build（`.github/workflows/ci.yml:79`）。
- `Makefile:52` 以后已有多个显式集成测试目标，并在缺少数据库 DSN 时由目标入口失败。
- 仓库有 7 个 Playwright spec：`artifact-browser.spec.ts`、`attachment-export.spec.ts`、`collection-export.spec.ts`、`collection-health.spec.ts`、`graph-capacity.spec.ts`、`m8-learning.spec.ts`、`semantic-link-graph.spec.ts`。
- Playwright 配置已有单 worker、失败截图与 retain-on-failure trace：`web/playwright.config.ts:19`。

关键缺口：

- CI 没有 Fast / Selected Integration / Full 分层，没有 `schedule` nightly，没有 job timeout，也没有覆盖率、诊断或测试产物上传。
- 7 个 E2E spec 虽分别被浏览器脚本引用，但 CI 只调用 Collection Health 路径；其余 6 个没有接入当前 CI。
- `web/package.json:15` 只有普通 Vitest 和 Playwright 脚本，没有 coverage 脚本。
- Review Learning Path 只有 integration-tagged 测试，例如 `internal/review/learningpath/adapter/postgres/review_learning_path_integration_test.go:1` 和 `cmd/api/review_learning_path_integration_test.go:1`；domain/application/http 没有普通 `_test.go`，Makefile 也没有专门的 Review Learning Path 集成目标。
- `go.mod` 与代码未找到 Testcontainers-Go 依赖或使用；现有 DB 集成测试普遍依赖显式 DSN 并在环境缺失时跳过。

文档一致性：

- `.trellis/spec/backend/quality-guidelines.md:13` 把 Testcontainers-Go 写成当前事实，与代码不符。
- `docs/roadmap.md:87` 将 Testcontainers 正确列为未来阶段。
- `docs/operations.md:356` 正确描述当前依赖显式 `ZHIXU_TEST_DATABASE_URL` 的做法。

结论：步骤 3 有可复用的既存基础，但父计划定义的分层门禁和测试可诊断性尚未实现；应标为“未批准、工作包未完成”，不能按基础设施数量推断为完成。

### 步骤 4：边界 primitives

审批状态：未批准、无子任务。

Go 边界证据：

- 已有共享 `httpapi.Problem`、status 映射、`WriteJSON`、`WriteProblem` 与严格 `DecodeJSON`：`internal/httpapi/response.go:17`、`internal/httpapi/response.go:26`、`internal/httpapi/response.go:44`、`internal/httpapi/response.go:56`。
- 共享层当前测试只直接覆盖 `DecodeJSON` 的边界行为：`internal/httpapi/response_test.go:10`。
- Review 仍保留本地 `decodeJSON`、`parseID` 和 `writeError`：`internal/review/http/handler.go:966`、`internal/review/http/handler.go:1043`、`internal/review/http/handler.go:1196`。
- Interview 仍保留对应本地 helper：`internal/interview/http/handler.go:1265`、`internal/interview/http/handler.go:1308`、`internal/interview/http/handler.go:1360`。
- Learning Path 虽导入共享 httpapi，仍有本地 ID、decode 和 error helper：`internal/review/learningpath/http/handler.go:340`、`internal/review/learningpath/http/handler.go:348`、`internal/review/learningpath/http/handler.go:398`。
- 基线统计中 Go 的三类本地 helper 数量与归档基准相同，说明父计划的 Go pilot 尚未发生。

前端边界证据：

- `web/src/shared/codec.ts:1` 已提供共享 UUID、record、Abort、only/exact keys 等 primitive。
- Graph 与 Semantic Link Graph 已导入共享 codec：`web/src/api/graph.ts:3`、`web/src/api/semanticLinkGraph.ts:3`；另有二十多个 API 模块使用它。
- 当前生产代码的本地 `isRecord` 定义已由基准的 24 个降到 0，属于明确的顺带实现成果。
- 但 Graph 仍保留本地 scalar、array 与 problem 解码，例如 `web/src/api/graph.ts:327`、`web/src/api/graph.ts:468`、`web/src/api/graph.ts:1196`。
- Semantic Link Graph 同样保留本地 scalar/array 解码，例如 `web/src/api/semanticLinkGraph.ts:467`、`web/src/api/semanticLinkGraph.ts:593`。

结论：步骤 4 是步骤 2-6 中唯一具有明显“顺带部分实现”的工作包。准确说法是“前端 record/exact/UUID 等已部分收敛；Go pilot 以及 scalar/array/problem 收敛仍未完成”。该结果不改变其未批准状态，也不足以勾选父任务 AC08。

### 步骤 5：高风险热点与 Domain 边界

审批状态：未批准、无子任务。

当前代码证据：

- `cmd/worker/main.go` 当前 2609 行，归档基准为 2582 行；没有缩小，反而增加 27 行。
- `web/src/features/review/ReviewSessionPage.tsx` 当前与基准均为 1014 行。
- `web/src/features/organizing/OrganizingPage.tsx` 当前与基准均为 800 行。
- Worker 的主要装配仍集中在同一文件，`newWorkerComponentsWithModels` 从 `cmd/worker/main.go:1065` 开始，模块 builder 与窄依赖边界没有按父计划拆出。
- Organizing 的配置与命令处理仍集中在页面中，例如 `web/src/features/organizing/OrganizingPage.tsx:540` 和 `web/src/features/organizing/OrganizingPage.tsx:552`。
- 当前 Domain 基线与归档基准相同：10 条跨 Domain 边、17 次 import、0 条反向依赖；未找到 CI allowlist，也没有消除一条高风险边。

结论：已有的同文件 helper 和局部组件不满足父计划所述的 builder/service/hooks 拆分及收窄依赖目标；步骤 5 应标为“未批准、未实现”。

### 步骤 6：性能基线、契约治理与长期质量门禁

审批状态：未批准、无子任务。

已有基础：

- 静态基线工具可对已存在的 `web/dist` 做可选 raw/gzip 统计：`deploy/architecture_quality_baseline.py:510`、`deploy/architecture_quality_baseline.py:588`。
- OpenAPI 有手写检查器，读取 OpenAPI 与三个 handler 源文件：`api/openapi/check.mjs:1`；包含手工 required operations 清单（`api/openapi/check.mjs:144`）。
- Git Sync 有一段专用的路由清单正则核对：`api/openapi/check.mjs:1270`。
- PR 模板提醒人工检查相关项目：`.github/PULL_REQUEST_TEMPLATE.md:24`。

关键缺口：

- `web/vite.config.ts:17` 未配置 manifest、bundle 分析报告或预算；可选 `web/dist` 报告不是 CI 产物，也没有阈值门禁。归档基线结果也明确提示该目录可能缺失或陈旧（`.trellis/tasks/archive/2026-08/08-05-architecture-quality-baseline/result.md:24`）。
- 未找到首屏/Monaco 冷启动的资源加载轨迹或请求数预算。
- OpenAPI 检查器依赖手工 operation 清单与少数源码正则，没有从全部运行时路由生成并逐项 diff 的机制；未找到 OpenAPI 单一事实源 ADR。
- CI 没有 nightly/schedule、SBOM、许可证/依赖报告、`govulncheck` 或 `go mod tidy -diff` 门禁。
- Domain 依赖没有 allowlist 门禁，依赖边数也未下降。
- `docs/requirements.md:238` 只把 SBOM 列为 Should；`docs/operations.md:382` 描述发布物应包含 SBOM，但当前 CI 没有对应实现。

结论：静态基线、可选 bundle 数据和手写 OpenAPI checker 只是可复用基础，不构成步骤 6 的性能/契约治理闭环；步骤 6 应标为“未批准、工作包未完成”。

## Recommended Parent Task Wording

建议父任务的“当前状态”改成以下语义，避免 `2/2 children done` 被误读为整个父计划完成：

```markdown
## 当前状态

- 已批准并完成：0A 可重复静态质量基线、1 Health 历史有界化。
- 步骤 0 剩余观察项：CI duration/flaky 历史，以及迁移 `00077` 在受控数据量下的耗时和锁等待；并入后续 CI / 发布验证阶段。
- 未批准、未实现：2 路由错误恢复、5 热点与 Domain 边界拆分。
- 未批准、工作包未完成：3 分层 CI / 测试门禁、6 性能与契约治理；仓库已有部分基础，但未满足验收条件。
- 未批准、部分顺带实现：4 边界 primitives；前端 record/exact/UUID 已部分共享，Go pilot 及 scalar/array/problem 收敛仍待实施。
```

父 PRD 建议：

- 代码复核后可将 AC01-AC03（Health）标为完成，并链接归档 Health 子任务。
- AC04-AC13 保持未完成。
- AC08 增加“前端部分完成”的说明，但不要勾选。
- 步骤 2-6 只有在用户批准后再分别建子任务；不要用当前顺带实现反推批准。
- 父 `task.json` 继续保持 `planning` 是合理的；但应在面向人的状态摘要中明确“已完成 2 个已批准子任务，不等于父计划完成”。

## Recommended Roadmap Wording

`docs/roadmap.md` 只保留高层状态和可发现性，详细验收继续以 Trellis 父任务为准：

```markdown
## 工程质量收口（未完成）

已完成：可重复静态质量基线、Health 历史数据有界化。

待批准并实施：路由错误恢复；Fast / Selected Integration / Full 分层 CI；7 个 E2E 的完整接线与覆盖率/诊断产物；Go HTTP 边界 primitives；Worker、Review、Organizing 热点拆分；Domain 依赖、bundle、OpenAPI 与 SBOM 治理。

仓库已有共享 TypeScript codec、手写 OpenAPI checker 和可选 bundle 报告等局部基础，但它们不代表对应工作包已完成。精确验收条件和代码审计证据见 `.trellis/tasks/08-05-architecture-quality-optimization/` 与文档整合任务的 research 记录。
```

另建议同步修正 `.trellis/spec/backend/quality-guidelines.md:13`：当前事实是显式外部测试数据库 DSN；Testcontainers-Go 是路线图中的未来方向，不是已采用的基础设施。`docs/operations.md:356` 当前表述可作为事实来源。

## Files Found

- `.trellis/tasks/08-05-architecture-quality-optimization/implement.md` — 步骤 0-6、审批边界和验收顺序。
- `.trellis/tasks/08-05-architecture-quality-optimization/prd.md` — 父任务 AC01-AC13，当前均未勾选。
- `.trellis/tasks/08-05-architecture-quality-optimization/design.md` — WP2-WP6 的设计边界。
- `.trellis/tasks/08-05-architecture-quality-optimization/task.json` — 父任务状态及仅有的两个子任务。
- `.trellis/tasks/archive/2026-08/08-05-architecture-quality-baseline/` — 已完成的收窄基线子任务及残余风险。
- `.trellis/tasks/archive/2026-08/08-05-health-bounded-history/` — 已完成的 Health 子任务、验收和验证记录。
- `deploy/architecture_quality_baseline.py` — 当前可重复静态基线实现。
- `.github/workflows/ci.yml` — 当前单 job CI 实现。
- `internal/httpapi/response.go` — Go 共享 HTTP 边界基础。
- `web/src/shared/codec.ts` — 已顺带落地的前端共享解码基础。
- `api/openapi/check.mjs` — 当前手写 OpenAPI 静态检查器。
- `docs/roadmap.md`、`docs/operations.md`、`docs/requirements.md` — 面向项目的当前/未来状态说明。

## Code Patterns

- 当前基线适合记录确定性的源码结构与数量，但 CI 历史、真实迁移锁等待和冷启动性能必须来自运行环境，不能从静态扫描推断。
- “存在共享 helper”不等于完成调用方迁移；Go HTTP helper 数量未下降就是直接反证。
- “spec 被脚本引用”不等于“spec 已接入 CI”；应以 workflow 实际调用链为准。
- 父任务的审批状态、子任务归档状态、代码实现状态是三套不同维度，不能互相替代。

## Related Specs

- `.trellis/spec/backend/quality-guidelines.md` — 后端测试与质量约束；Testcontainers 当前状态需要校正。
- `.trellis/spec/frontend/quality-guidelines.md` — 前端测试与错误边界的质量背景。
- `.trellis/spec/guides/cross-layer-thinking-guide.md` — 跨层契约检查思路。
- `docs/architecture/adr/0019-mature-framework-first.md` — 新增测试/协议基础设施前的成熟方案优先约束。

## External References

无。本审计只判断仓库当前事实，没有使用外部资料或对第三方工具版本作推断。

## Caveats / Not Found

- 当前工作区存在未提交修改；本审计读取的是当前工作区内容，不等同于某个干净 commit 的快照。
- 基线工具的 `web/dist` 输入是可选且可能陈旧的本地产物，因此未把当前 bundle 数值作为步骤 6 完成证据。
- 本次没有启动外部 PostgreSQL、完整 E2E 浏览器环境、nightly 或性能采集环境；相关“未完成”判断来自代码、workflow 和归档记录中没有实现/证据，而不是一次完整运行失败。
- 未找到步骤 2-6 的归档或活跃子任务；父 `task.json` 只列出基线与 Health 两项。
- 当前真实 PostgreSQL Health 集成结果采用归档任务的已记录证据；本次仅重跑了可在本地稳定执行的定向 Go、前端和 OpenAPI 校验。
