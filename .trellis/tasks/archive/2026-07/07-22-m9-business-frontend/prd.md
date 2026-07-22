# M9 业务前端、Diff 与统一 SSE UX

## Goal

交付 M9-01、M9-02 与 M9-04 的当前真实产品切片：用户在同一可访问的知识操作台中查看 Workspace 状态、资料摄取、资料版本、Proposal、Workflow 和设置；审阅真实 Proposal 变更并执行服务端支持的批准/驳回；由一个 Workspace-scoped SSE Event Store 负责重连、游标恢复和定向 Query 失效。页面不得使用 mock、浏览器缓存或静态数字冒充服务端事实。

## Confirmed Facts

- 当前前端为 React 19、TypeScript、Vite、TanStack Query、React Router、Vitest/Testing Library/Playwright；现有样式是原生 CSS，没有 Tailwind、shadcn 或 Radix。
- 当前 OpenAPI 已提供 Workspace、System Status、Source Version Detail、Proposal Detail/Approval、Workflow Detail/Control 和 SSE；但缺少 Workspace-scoped Source Version、Proposal、Workflow 列表 GET。
- `web/src/events/server-events.ts` 已拥有严格 SSE decoder/连接器，RAG Feature 仍单独持有连接生命周期与 Query 失效逻辑；M9-04 应收敛为全站唯一连接 Owner。
- 当前没有正式 Document 聚合和 Article Revision 原始基线正文；Source Version 是可查询的真实资料事实。页面不得把 Source Version 假称为已发布 Document，也不得伪造三方 Diff。
- Change Control 当前公开批准/驳回与 Apply Preflight；暂缓、编辑后批准、批量审批、请求重分析尚无服务端命令，不在本任务伪造。
- 正式 Auth/Session/CSRF/Capability 属于 M10；Workspace ID、URL 和 SSE cursor 不是身份凭据。

## Product Requirements

### R1 — App Shell 与 UI System

- 建立响应式产品 Shell：桌面侧栏、移动抽屉、Workspace 状态、全局 SSE 状态、主导航和面包屑。
- 采用 shadcn open-code 组合方式：Radix primitives、CVA 变体、Lucide 图标和项目 CSS variables；业务状态不依赖组件库类型。
- 视觉方向为“编辑部式知识操作台”：暖纸底、墨色正文、黄铜/赭红/松绿状态强调、清晰的证据与版本层级；保留现有 Graph/RAG 功能和视觉可用性。
- 所有关键操作支持键盘、可见焦点、文字+图形状态表达；Dialog/Sheet 关闭后恢复焦点。

### R2 — 真实只读列表契约

- 新增 Workspace-scoped Source Version、Proposal、Workflow 列表 GET，使用稳定 keyset cursor、limit 和显式筛选。
- Source Version 列表联合最新 Ingestion Attempt、Workflow 和当前索引选择状态，作为 Inbox/资料列表读模型；`source_version.workspace_id` 只镜像所属 Source 的持久 Workspace 并受复合外键约束，不创建第二份业务状态机。
- Proposal 列表由 Change Control 拥有；`proposal.risk_level` 是审批和筛选使用的唯一等级事实，Revision `risk` 仍是独立自由文本。Workflow 列表由 Workflow 拥有。列表响应只含页面需要的摘要，详情仍通过既有详情接口获取。
- 所有查询强制 Workspace 绑定、参数化 SQL、稳定排序、限制最大页大小，并提供正常、空、非法 cursor、跨 Workspace 和依赖不可用测试。

### R3 — Dashboard

- 展示 Workspace/Git/API/Worker/Database/Retrieval/Graph/Conversation/Semantic Link 的真实状态。
- 展示来自 Proposal/Workflow/Source Version 首屏的可点击待办摘要与最近活动；异常优先于统计数字。
- Health、Review 等尚未交付能力显示“能力尚未交付/不可用”，不得显示虚构数量。
- 无 Workspace、加载、部分能力不可用、请求失败和 SSE 重连必须可区分。

### R4 — Inbox 与资料版本

- Inbox 支持状态筛选、游标分页、重新扫描入口、文件/类型/大小/捕获时间/安全/解析/索引/Workflow 状态展示。
- 资料列表与 `/documents/:sourceVersionId` 详情只展示真实 Source Version/Content Artifact/Parse/Index 元数据；明确标注“资料版本”与“正式 Document 尚未交付”的边界。
- 详情提供打开来源引用、进入 Search/Chat/Graph 的可恢复入口；没有标准化正文接口时明确显示能力缺口，不伪造预览。

### R5 — Proposal 列表、详情与 Diff/审批

- Proposal 列表支持状态、类型、风险和创建时间筛选、游标分页、等待时间与目标对象展示。
- 新建 Proposal 必须显式提交 `risk_level=CRITICAL|HIGH|MEDIUM|LOW`，并将其纳入 v2 幂等请求 Hash；不得从 Revision 风险说明推导等级。
- Proposal 详情穷尽 `file_patch` 与 `knowledge_change`；展示证据、风险、回滚计划、Revision/Approval/Workflow 绑定与状态。
- `file_patch` 使用成熟 Diff Viewer 展示“当前 Workspace 内容 vs Proposal 内容”；只有当前文件 Hash 等于 Proposal base hash 时才标记为基线 Diff。Hash 漂移时显示版本冲突并禁止批准。
- `knowledge_change` 使用结构化 Relation Diff，不把它伪装为 Markdown Diff。
- 支持服务端已有的批准/驳回与 Apply Preflight：首次批准直接调用 Approval 命令；只有已批准的 file patch 才能单独执行写回前 Apply Preflight。同一次逻辑重试复用 Idempotency-Key。高风险批准二次确认，失败/冲突不得显示 optimistic success。
- 暂缓、编辑后批准、批量审批和完整三方合并只显示未交付能力说明，不添加假按钮。

### R6 — Workflow 中心

- Workflow 列表支持状态筛选、游标分页，显示 definition、状态、创建/更新时间、耗时和是否等待用户。
- Workflow 详情展示持久 Run 状态和服务端当前公开字段；节点时间线/Tool Call/Token 尚无公开详情时明确标注契约缺口。
- 只在服务端允许的状态显示 pause/resume/cancel，提交 expected version，Version Conflict 后重新查询。

### R7 — Settings

- 设置页按 Workspace、运行依赖、模型/检索能力、Git 与维护边界分组展示当前真实配置和能力状态。
- 只允许已有 Workspace 连接/切换与安全扫描；模型密钥、Auth、索引维护、导出和危险清理没有正式接口时只显示说明，不写入 Browser Storage、不构造假保存成功。

### R8 — Unified SSE Event Store

- App Shell 对每个 Active Workspace 只建立一个 SSE 连接；Feature 不再各自创建 EventSource/fetch stream。
- Event Store 保存的仅是连接状态和 Last-Event-ID；业务事实仍由 TanStack Query/API 拥有。
- Event decoder 支持现有 conversation/question/answer/workflow/model_run，并以可扩展映射定向失效 dashboard、inbox、proposal、workflow、RAG 等 Query Family。
- 409 expired cursor 必须先完成 Workspace 权威资源回查后再清游标重连；回查失败保留游标并显示恢复失败。400 invalid/future cursor 停止有游标连接并在成功回查后无游标重连。
- Workspace 切换关闭旧连接、清理旧 Workspace cache，不泄漏事件或数据。

## Non-Functional Requirements

- Route-level lazy loading；大列表由服务端 cursor 有界分页，不一次加载全 Workspace。
- 网络/URL/SSE 输入从 `unknown` 在唯一边界严格解码；组件不解析 snake_case DTO。
- 只新增支撑真实列表与 Proposal 公共契约所需的 additive Schema hardening：`proposal.risk_level`、`source_version.workspace_id`、复合约束和有界查询索引；迁移按 Expand → concurrent index → exact backfill → Contract 拆分，不新增 Dashboard/Inbox 第二事实表。
- 不修改或回滚 M7-03 当前 `internal/collection/**`、`internal/health/**`、`migrations/00025*` 的用户改动。
- 新增依赖必须进入 `web/package.json` 和 lockfile，并通过 audit、lint、typecheck、test、build。
- 正式认证仍未完成，页面必须保留 loopback/未认证安全提示。

## Acceptance Criteria

- [x] AC-01：桌面 1440×900 与移动 390×844 可通过 Shell 导航到 Dashboard、Inbox、Documents、Proposals、Workflow、Settings、Graph、Chat，页面无横向溢出。
- [x] AC-02：shadcn open-code primitives 提供 Button、Badge、Card、Dialog/Sheet、Tabs、Tooltip、Dropdown/Menu 等必要可访问组合，业务状态类型不依赖 Radix。
- [x] AC-03：三个 Workspace 列表接口使用真实 PostgreSQL、稳定 cursor、limit/filter、Workspace 隔离，OpenAPI 与严格前端 decoder 一致。
- [x] AC-04：Dashboard 所有数字和活动均可追溯到真实 Query；未交付能力明确显示 unavailable，不出现假统计。
- [x] AC-05：Inbox/资料列表分页、筛选、扫描和详情刷新恢复；Source Version 不被描述为正式 Document。
- [x] AC-06：Proposal 列表/详情支持两种 Proposal 类型；Markdown/Relation Diff、风险、证据、回滚和审批状态可理解且可键盘操作。
- [x] AC-07：批准/驳回使用服务端 Revision/Change Hash；首次批准由 Approval 命令完成原子安全校验，已批准 file patch 在写回前可执行 Apply Preflight；Hash/Version 冲突阻止操作并显示恢复路径。
- [x] AC-08：Workflow 列表/详情和 pause/resume/cancel 使用真实状态/版本，202/成功响应不被误当为后续副作用完成。
- [x] AC-09：Settings 只展示/操作已有契约，Secret 不进入 URL、日志或 Browser Storage。
- [x] AC-10：每个 Workspace 只有一个 SSE 连接；RAG 与 M9 页面共享失效和重连机制，cursor expired/invalid、网络重连、Workspace 切换和清理均有测试。
- [x] AC-11：组件测试覆盖 Loading、Empty、Error、Degraded、Conflict、Reconnect、Recovery、键盘、焦点和高风险确认。
- [x] AC-12：前端 lint/typecheck/test/build、Go 单测/race/vet、真实 PostgreSQL API integration、OpenAPI check、浏览器 smoke、依赖审计和 diff check 通过。

2026-07-22 最终复验说明：前端 37 files / 368 tests、lint/typecheck/build、audit high gate、包含 `cmd/api` 的 M9 Go race/vet、三条真实 PostgreSQL integration/EXPLAIN、OpenAPI、浏览器与 diff check 均通过；此前并行 M7 Collection 的临时编译阻断已解除。

2026-07-23 契约复验说明：修复第二套 Semantic Link Proposal detail decoder 将缺失 `approval` 静默映射为
`null` 的 fail-open；`file_patch` 与 `knowledge_change` 均新增缺字段负测。相关 129 tests、前端全量 46 files /
569 tests、lint/typecheck/build、`make test`、OpenAPI、Trellis validate 与 diff check 通过，同一独立 reviewer 第三轮
确认无剩余 P0-P2。

## Out of Scope

- M7-03 Collection/Health 页面与接口。
- 正式 Document/Article Revision 聚合、标准化正文公共预览、完整三方合并和 Proposal Revision 编辑命令。
- 暂缓/请求重分析/批量审批。
- M10 Auth/Session/CSRF/Capability、正式审计查询和 50 万容量门禁。
- M8 Artifact/Review/Interview/Memory 与 M9-03 异步导出、脱敏、权限/过期和结果追踪。
