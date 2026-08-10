# 代码质量与架构优化计划

## Goal

在不改变知序模块化单体方向、不重写业务领域模型、不引入新的部署复杂度的前提下，优先关闭已经证实的容量与恢复风险，再提高测试门禁的真实性、边界代码复用率和高变更区域的可读性。

本轮优化以同一套九维评分口径复评，目标从当前 **77/100** 提升到 **85 分以上**；分数只作为结果摘要，是否完成以以下可观察验收标准为准。

## Background

- 当前架构主干健康：`presentation -> application -> domain` 依赖方向基本成立，未发现 `domain -> adapter/http` 反向依赖。
- 领域不变量、事务、幂等、认证、SSRF/路径/Secret 防护与前后端类型边界是现有优势，优化不得削弱这些契约。
- 2026-08-05 的基线审计曾记录默认 `make test`、`go mod tidy -diff` 与 `git diff --check` 通过；外部 PostgreSQL/River 全量门禁和 7 个 Playwright E2E 当时未全部执行。本轮状态回填不以该历史运行替代当前最终门禁。
- 详细基线、证据和文件锚点见 `research/audit-baseline.md`。

## 当前状态

- 已批准且完成：步骤 1 Health 历史有界化；AC-01..AC-03 已由归档任务、当前代码和定向测试证据回填。
- 已批准且部分完成：步骤 0 的可重复静态质量基线已交付；CI duration/flaky 历史和迁移 `00077` 在受控数据量下的耗时、锁等待观察仍缺。
- 未批准、未实现：WP2 路由错误恢复与 WP5 热点/Domain 边界拆分。
- 未批准、工作包未完成：WP3 分层 CI/测试门禁与 WP6 性能/契约治理；仓库已有局部基础，但没有满足整组验收。
- 未批准、部分顺带实现：WP4 的前端 record/exact/UUID primitives 已部分共享；Go HTTP pilot 与 scalar/array/problem 收敛仍未完成。

两个已登记 child 均已归档只说明已批准子任务完成，不表示本父计划或 AC-04..AC-13 完成。步骤 2-6 仍需用户批准后再分别实施，不因现有局部基础反推批准。

## Requirements

### R1. 先关闭已证实的运行时风险

- Health Issue 详情不得继续无界读取全部 observation、evidence 和 decision 历史。
- 历史查询必须稳定分页、绑定 Workspace 与 Issue，并且只批量加载当前 observation 页的 evidence。
- 现有 `/api/v1` 调用方应采用增量兼容迁移：先提供分页能力并保留有界旧字段，不在同一发布中直接删除旧字段。
- React 路由内容必须有统一错误恢复边界，能够区分正常 lazy loading 与 chunk/render 失败，并提供重试或刷新入口。
- `migrations/00077_workspace_git_capture.sql` 已进入 Git 历史，且项目规范禁止修改已发布迁移；本任务只验证标准停机迁移路径并记录非标准滚动/直连迁移的发布前提，不直接改写该历史迁移。

### R2. 让 CI 绿灯代表真实的回归覆盖

- PR、主分支和定时任务使用分层门禁，不能只由同一套 `make test` 代表所有发布质量。
- PR 必须运行 Unit、Lint/Static、Migration Contract、OpenAPI Contract 和按改动范围选择的 Integration。
- 主分支或 nightly 必须执行完整 PostgreSQL/River、Docker Smoke、Security、Evaluation 与全部当前 Playwright E2E，并保存失败诊断产物。
- CI 中被声明为强制的数据库测试在缺少 `ZHIXU_TEST_DATABASE_URL` 时必须失败，不得静默 SKIP。
- Review Learning Path 的 domain/application/http 关键行为必须进入默认 Go 测试；真实 PostgreSQL 行为继续由独立 integration gate 验证。
- Go 与 Web 生成可追踪的覆盖率产物。第一阶段采用关键包趋势与不下降门禁，不以全仓单一百分比驱动无价值测试。

### R3. 收敛重复的边界基础设施

- 后端只抽取不含业务语义的 HTTP wire primitives，例如严格 canonical ID、受限 JSON 解码和统一 Problem 输出。
- 前端只抽取不含领域语义的 runtime decoder primitives，例如 record、exact keys、UUID/string/array、JSON 和 HTTP problem。
- 领域 schema、状态机、不变量、错误类型和请求构造继续归各模块所有；禁止形成万能 decoder、共享大 DTO 或 `common` 领域模型。
- 先迁移 2 至 4 个高相似模块作为试点，用契约测试证明错误码、字段路径、拒绝规则和响应绑定没有漂移，再决定是否扩大范围。

### R4. 降低高变更区域的认知负担

- `cmd/worker/main.go` 的长组合函数按现有模块边界提取私有 builder，原入口继续只负责编排，初始化顺序和实例共享语义不变。
- 前端优先拆分 `ReviewSessionPage` 与 `OrganizingPage`：页面保留路由和流程编排，命令/幂等逻辑进入 feature hook，展示进入聚焦组件。
- 后续热点拆分必须一次只处理一个模块或状态机，不与 API 契约变化、产品行为变化或跨模块迁移混在同一变更中。
- 文件行数只作为趋势信号；验收重点是职责、依赖、状态所有权和测试边界变得清楚。

### R5. 阻止模块边界继续恶化

- 为现有跨 Domain 直接依赖建立可审计 allowlist，CI 禁止新增 `domain -> adapter/http` 或未批准的跨 Domain 依赖。
- 选择一个已有边界做契约化试点：优先 `conversation -> retrieval/agent`，备选 `graph -> knowledge`。
- 试点只迁移消费者真正需要的稳定事实或操作，通过 owner 的窄 contract/Application Port 暴露；canonicalization 和业务不变量仍由 owner 保持唯一事实源。
- 本轮不把模块化单体拆成微服务，也不做全仓 Domain 类型搬迁。

### R6. 建立持续的性能、依赖和契约治理

- 前端构建产物输出可机读体积报告，并为入口与 Monaco 编辑器路径设置基于当前基线的回归预算。
- 先测量 Monaco 冷启动请求链，再按证据裁剪 worker/语言；不得为了体积破坏 Markdown 编辑、Diff Viewer 或离线加载。
- CI 增加 `go mod tidy -diff` 与 `govulncheck`；SBOM、许可证和依赖更新报告进入 nightly 或独立治理任务。
- 修正文档中 “Testcontainers” 与实际外部 `ZHIXU_TEST_DATABASE_URL` 模式的冲突。
- OpenAPI 短期补齐运行时路由覆盖清单；自动生成或单一契约源属于后续 ADR，不在本轮直接重写 3,500 行检查器。

## Key Decisions

- 继续采用模块化单体；本轮不以微服务拆分解决维护性问题。
- Health 采用“新增分页端点 + 旧详情有界兼容 + 弃用期”的迁移路线，优先保证资源上界和可回滚性；该兼容策略已由用户确认。
- CI 采用 Fast / Selected Integration / Full 三层，而不是把所有慢门禁塞进每个 PR。
- 复用只下沉 wire-level、业务无关的基础能力；领域 decoder、错误和状态规则仍由 owner 模块维护。
- 已进入历史的 `00077` 不直接改写；如果未来要求滚动升级，另立迁移发布设计任务。
- 大文件指标用于趋势和选点，不设置“超过某行数就失败”的机械门禁。

## Acceptance Criteria

- [x] AC-01：Health 详情单次响应和数据库读取有明确上限；历史端点默认 `25`、最大 `100`，使用稳定 keyset cursor，cursor 不能跨 Workspace、Issue、历史类型或 limit 复用。证据见已归档 `08-05-health-bounded-history` 及当前 Application/HTTP/OpenAPI/Web 实现。
- [x] AC-02：大历史 fixture 下，Health 详情与单页历史保持常数级 SQL statement 数；evidence 只对当前 observation 页执行一次批量查询，无 N+1、无跨页预取。证据见已归档 Health 任务的真实 PostgreSQL 验收记录。
- [x] AC-03：Health 旧字段在兼容期内仍可解码，但明确表示分页/截断；当前 Web 调用方迁移后不再假定数组等于完整审计历史。证据见当前前端 API/query/page 与定向测试。
- [ ] AC-04：route lazy import rejection 与页面 render error 都显示稳定恢复状态；重试/刷新可恢复，现有 Suspense loading、认证、导航和 Workspace 隔离测试继续通过。
- [ ] AC-05：CI 明确区分 PR 快速门禁与 main/nightly 完整门禁；全部当前 Playwright spec 都被某个强制 job 执行，失败时上传 trace、截图和相关日志。
- [ ] AC-06：CI 强制 integration job 未配置数据库时失败；Review Learning Path 的命令校验、幂等、状态/CAS、归属校验和 HTTP 错误映射进入默认测试。
- [ ] AC-07：Go/Web 覆盖率产物可下载；关键包建立基线和不下降规则，且没有通过排除失败分支或堆叠无断言测试达标。
- [ ] AC-08：后端 HTTP 与前端 decoder 各完成至少一个 2 至 4 模块试点；原模块契约测试不变通过，没有新增业务无关 helper 的第三份实现。
- [ ] AC-09：Worker 主组合函数缩减为可扫描的模块编排；ReviewSession/Organizing 页面中的路由、命令、展示职责能够独立测试，URL、query key、幂等签名和错误恢复语义不变。
- [ ] AC-10：跨 Domain 依赖检查进入 CI，存量 allowlist 可见且不增加；至少减少一条存量边界依赖，且 JSON/数据库/哈希兼容测试通过。
- [ ] AC-11：构建产物体积报告和预算进入 CI；Monaco 优化前后有相同环境的冷启动数据，没有 worker/语言能力回归。
- [ ] AC-12：`go mod tidy -diff`、`govulncheck` 与文档一致性检查进入相应门禁；OpenAPI 路由覆盖差异可被自动发现或显式 allowlist。
- [ ] AC-13：每个工作包有独立提交与回滚点；最终复评达到 85 分以上，且九个维度均不低于 7.5 分。

## Out Of Scope

- 微服务拆分、数据库更换、前端框架迁移或全仓目录重写。
- 一次性统一所有 handler、decoder、领域类型或错误模型。
- 改变 Proposal、Approval、Workflow、Review、Health 等业务状态机和权限语义。
- 以文件行数或全仓覆盖率单指标作为质量目标。
- 在没有实测证据前删除 Monaco worker/语言包或新增数据库索引。
- 直接修改已发布迁移；需要支持不停机滚动升级时另立迁移/发布架构任务。
- 本轮直接把 OpenAPI 改为代码生成或生成完整前端客户端。

## Risks And Deferred Items

- 全量 integration/E2E 可能显著增加 CI 时间；先记录时长和失败率，再决定哪些进入 PR 必跑、哪些进入 main/nightly。
- 复用抽象如果吞入领域语义，会把局部重复升级为全局耦合；所有试点必须先锁定契约测试。
- 历史迁移 `00077` 在标准启动器中由停机窗口保护；非标准滚动/直连升级仍是已知运营限制。
- OpenAPI 单一事实源、SBOM/许可证阻断策略和更大范围的 Domain 解耦在本轮只建立证据与后续决策入口。
