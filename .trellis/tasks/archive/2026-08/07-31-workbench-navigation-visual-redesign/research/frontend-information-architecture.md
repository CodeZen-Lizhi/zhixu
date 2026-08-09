# Research: 前端信息架构与文案密度

- Query: 审计现有 AppShell、路由、Workspace/Settings 与全部路由级 Feature 页面，提出减少一级导航、保持全部现有路由可达、收敛信息事实源并精简中文文案的具体方案。
- Scope: internal
- Date: 2026-07-31

## Superseding Product Decisions — 2026-08-02

本研究以下“工作台 / 知识 / 产出 / 设置”与“产出默认进入 `/proposals`”建议仅保留为历史方案，不再作为实施结论。用户审阅现有界面后确认：

- 一级入口改为“工作台 / 知识 / 创作 / 设置”；“创作”直接进入 `/authoring`。
- 创作首先提供“新建文章 / 整理成文”，并以“最近草稿 / 待确认 / 已完成”组织状态；Proposal、Workflow Run、Artifact 只作为生命周期详情。
- 已连接首页固定提供“快速记录 / 新建文章 / 整理成文 / 搜索知识”，不做个性化排序。
- Topbar `…` 菜单删除，系统状态改为“运行总览 + 异常详情 + 技术详情”。

后续实现以当前任务 `prd.md`、`design.md` 和 `implement.md` 为准；本文其余内容用于追溯当时的代码证据和被替代方案。

## Findings

### 结论

推荐采用四个一级入口：`工作台 / 知识 / 产出 / 设置`。

- `工作台` 直接进入 `/dashboard`。
- `知识` 直接进入 `/inbox`，并在知识页面内用分组局部导航到资料、探索、维护和学习能力。
- `产出` 直接进入 `/proposals`，并在页面内切换提案、流程和产物。
- `设置` 直接进入 `/workspace`；Workspace 成为 Settings 的第一个局部页面，不再作为侧栏中的并列入口，也不再设置一个没有页面语义的“系统”分组。

四个入口可以复用已有落地页，不需要创建空的 `/knowledge` 或 `/output` Hub。现有 27 条明确路由全部保留；新增 URL 只用于把 Settings 当前不可深链的页签变成可恢复的局部页面。

页面标题应改由路由页面统一拥有：AppShell 只保留面包屑和全局上下文，不再渲染第二个页面标题。普通页面使用“一个 `h1` + 最多一句必要说明”；详情页面的 `h1` 使用实际资源名称。Graph、RAG 的内部品牌头和产品导航应移除，但其图谱控制、会话栏、证据栏等任务内导航继续保留。

### Files Found

| File | Description |
| --- | --- |
| `.trellis/tasks/07-31-workbench-navigation-visual-redesign/prd.md` | 本任务目标、验收标准和两个尚未确认的产品决策。 |
| `.trellis/workflow.md` | Trellis 规划、研究和上下文持久化规则。 |
| `.trellis/spec/frontend/index.md` | 前端事实基线、规范索引与开发前检查清单。 |
| `.trellis/spec/frontend/directory-structure.md` | Routes/App 只能组合 Feature 的依赖边界。 |
| `.trellis/spec/frontend/component-guidelines.md` | 页面组合、状态、键盘、焦点和非纯颜色表达约束。 |
| `.trellis/spec/frontend/model-settings.md` | Settings 中 desired/active/applied、Secret 生命周期和移动端不可删减契约。 |
| `.trellis/spec/frontend/quality-guidelines.md` | 路由、状态、移动端、测试和浏览器验证门禁。 |
| `docs/architecture/frontend-architecture.md` | 路由、URL State、App Shell、拆包和浏览器验收的架构事实。 |
| `web/src/app/AppShell.tsx` | 当前 5 个导航分组、17 个侧栏链接、面包屑、第二页面标题和快捷菜单。 |
| `web/src/routes/AppRoutes.tsx` | 当前 27 条明确路由和 wildcard 重定向。 |
| `web/src/app/AppShell.test.tsx` | 固化当前分组导航、折叠组及未连接时“工作台/工作区/设置”三入口的测试。 |
| `web/src/routes/AppRoutes.test.tsx` | 现有深链兼容测试；尚未覆盖 Workspace、Collection、Health 和 Artifact 路由。 |
| `web/src/features/workspace/WorkspacePage.tsx` | Workspace 创建/打开、路径/Git、扫描、扫描结果和运行状态的混合页面。 |
| `web/src/features/business/BasicPages.tsx` | Inbox、资料版本与当前 7 页签 Settings；包含 Workspace/Git 重复事实。 |
| `web/src/features/business/DashboardPage.tsx` | Dashboard、系统状态和业务入口；包含已经过时的里程碑文案。 |
| `web/src/features/{graph,rag,timeline,search,collections,health,artifacts,review,memory,interview}` | 其余路由页的标题、局部模式、空状态、详情和跨功能入口。 |
| `web/src/shared/ui.tsx` | Card、CardHeader、Tabs、状态、Dialog、Sheet 等共享展示原语。 |
| `web/src/styles.css` 与 Feature CSS | 当前暖色主题、页面大标题、Card 说明、Graph/RAG/Timeline 等独立视觉岛。 |

### Current Information Architecture

#### 一级导航过度暴露实现能力

`navigationGroups` 当前把 17 个基础页面链接分成 5 组；“资料与知识”一次平铺 Inbox、Documents、Search、Graph、Collections、Health、Timeline 7 个能力，见 `web/src/app/AppShell.tsx:23-67`。这不是只有 5 个入口，而是 5 个标题下的 17 个同层链接。

“系统”只是分组标签，却在其下同时放置“工作区”和“设置”，见 `web/src/app/AppShell.tsx:59-66`。未连接时 AppShell 还保留“工作台/工作区/设置”三个链接，测试明确固化了这一行为，见 `web/src/app/AppShell.test.tsx:70-90`。这正是 PRD 所述的概念层级冲突。

当前“学习与研究”只折叠自身 4 个链接，其他 13 个业务/系统链接仍持续占据侧栏；折叠状态也只是组件局部 `<details>`，见 `web/src/app/AppShell.tsx:101-110`。

#### 路由与导航存在两份清单

路由表在 `web/src/routes/AppRoutes.tsx:32-64`，导航表在 `web/src/app/AppShell.tsx:23-69`。AppShell 再通过字符串前缀判断当前页面和“详情”，见 `web/src/app/AppShell.tsx:71-72`、`web/src/app/AppShell.tsx:151-155`。新增路由必须人工同步至少两处，且所有详情面包屑都退化成通用“详情”，见 `web/src/app/AppShell.tsx:187`。

应建立一份只含展示元数据的 route registry，至少拥有 `section / localGroup / label / basePath / match / parent`。`AppRoutes` 仍只负责 lazy Route 组合，Shell 与局部导航共享 registry；不要把 Query、Decoder 或业务规则移入 registry，符合 `Routes/Pages -> Feature Modules` 边界，见 `.trellis/spec/frontend/directory-structure.md:35-43`。

#### Settings 页签不是可恢复导航

Settings 当前以 `defaultValue="workspace"` 打开 7 个 Radix Tabs，见 `web/src/features/business/BasicPages.tsx:151-180`。页签选择不进入 URL，刷新、浏览器返回、书签和直接分享都不能恢复“数据导出/运行依赖/模型/API Token”等分区。测试只能通过 `mouseDown` 切页签，见 `web/src/features/business/SettingsPage.test.tsx:28-35`、`web/src/features/business/SettingsPage.test.tsx:49-55`。

这些分区是不同设置资源，不是同一数据的显示模式，适合用路由链接；Tabs 应继续用于 Graph 模式、Collection 结果视图等同页模式，而不是承担 Settings 信息架构。

#### 无 Workspace 的恢复入口不一致

共享 `BusinessWorkspaceGate` 统一链接 `/workspace`，见 `web/src/features/business/BusinessWorkspaceGate.tsx:5-17`；但其他页面有的链接 `/settings`，有的链接 `/`，还有的只显示提示而没有操作：

- Collections 与 Health 链到 `/settings`，见 `web/src/features/collections/CollectionsPage.tsx:158`、`web/src/features/health/HealthPage.tsx:148`。
- Graph 与 RAG 链到 `/`，见 `web/src/features/graph/GraphPage.tsx:287-288`、`web/src/features/rag/RagPage.tsx:219`。
- Review、Memory、Interview 只显示不可用状态，没有连接入口，见 `web/src/features/review/ReviewPage.tsx:110`、`web/src/features/memory/MemoriesPage.tsx:100`、`web/src/features/interview/InterviewPage.tsx:321`。

所有 Workspace gate 应复用一个组件，并统一指向 `/workspace`。这不会改变授权或 Query gating，只收敛恢复路径。

### Proposed Primary And Local Navigation

#### 一级入口

| Primary | Landing | Availability | Responsibility |
| --- | --- | --- | --- |
| 工作台 | `/dashboard` | 始终可见 | 当前 Workspace 的待办、异常和最近活动；无 Workspace 时显示连接引导。 |
| 知识 | `/inbox` | 有 Active Workspace 时可见 | 资料摄取、检索、组织、健康、时间线、学习与研究。 |
| 产出 | `/proposals` | 有 Active Workspace 时可见 | Proposal 审阅、Workflow 运行和 Artifact 产物。 |
| 设置 | `/workspace` | 始终可见 | Workspace、运行状态、模型、访问凭据和数据导出。 |

未连接状态只显示“工作台/设置”两个可用一级入口；“设置”直接落到 Workspace。这样首次连接仍可达，同时侧栏不再出现“系统/工作区/设置”三层或三项并列。直接访问受限深链时仍显示统一 CTA，而不是隐藏失败原因。

#### 知识局部导航

知识页面内显示分组后的直接链接；组标题只用于扫描，不增加一次点击：

| Local group | Direct destinations |
| --- | --- |
| 资料 | 收件箱 `/inbox`；资料版本 `/documents` |
| 探索 | 检索 `/search`；图谱 `/graph`；集合 `/collections` |
| 维护 | 健康 `/health`；时间线 `/timeline` |
| 学习 | 对话 `/chat`；复习 `/review`；访谈 `/interviews`；记忆 `/memories` |

从任意页面先进入“知识”，再选择任一直接链接，最多两次导航操作即可到达 PRD 指定的 7 个原“资料与知识”能力。详情和会话路由继承其基础入口的 active state，不在局部导航再增加详情项。

#### 产出局部导航

| Destination | Path family |
| --- | --- |
| 提案 | `/proposals`、`/proposals/:proposalId` |
| 流程 | `/workflows`、`/workflows/:workflowId` |
| 产物 | `/artifacts`、`/artifacts/:artifactId` |

#### 设置局部导航

| Destination | Route | Content owner |
| --- | --- | --- |
| 工作区 | `/workspace`，`/` 为兼容别名 | 创建/打开/切换、名称、根路径、状态、ID、Git 分支/HEAD/dirty/version。 |
| 运行状态 | `/settings` | `/api/v1/system/status` 的完整诊断与恢复入口。 |
| 模型与检索 | `/settings/models` | desired、active、API applied、Worker applied、restart/rollout/Secret。 |
| 访问凭据 | `/settings/access` | Session 模式和 API Token 生命周期。 |
| 数据导出 | `/settings/exports` | Workspace Attachment Export 与下载历史。 |

当前“维护边界”页签没有独立可执行任务，主要展示里程碑和能力说明，见 `web/src/features/business/BasicPages.tsx:186-187`。不要把它迁移为新路由；把真正的禁用原因放到对应操作附近，未来有维护命令时再增加页面。

### Existing Route Reachability And Compatibility

保留 `web/src/routes/AppRoutes.tsx:35-61` 中全部 27 条现有路径，不改名、不删除，也不把现有 query state 搬到新 URL：

| Section | Existing routes kept |
| --- | --- |
| 工作台 | `/dashboard` |
| 知识 | `/inbox`; `/documents`; `/documents/:sourceVersionId`; `/search`; `/graph`; `/collections`; `/collections/:collectionId`; `/health`; `/timeline`; `/timeline/:eventId`; `/chat`; `/chat/:conversationId`; `/review`; `/review/session`; `/interviews`; `/interviews/:sessionId`; `/memories` |
| 产出 | `/proposals`; `/proposals/:proposalId`; `/workflows`; `/workflows/:workflowId`; `/artifacts`; `/artifacts/:artifactId` |
| 设置 | `/`; `/workspace`; `/settings` |

兼容策略：

1. `/` 继续原样渲染 Workspace，不重写 URL。现有兼容测试明确要求根路由保持 Workspace 语义且 URL 仍为 `/`，见 `web/src/routes/AppRoutes.test.tsx:50-56`。
2. `/workspace` 继续作为 Workspace 的显式路径，并成为“设置”一级入口的 landing。
3. `/settings` 保留，改为“运行状态”设置页；现有 Settings Tab 没有 URL 状态，因此不存在需要迁移的旧 Tab 深链。
4. 新增 `/settings/models`、`/settings/access`、`/settings/exports`；它们是新增深链，不要求旧路径重定向。
5. 现有 Search、Graph、Timeline、Review 等 query string 原样保留。route registry 只匹配 pathname，不解析或重写 Feature URL State。
6. wildcard 继续 replace 到 `/dashboard`，见 `web/src/routes/AppRoutes.tsx:63`。

当前 route compatibility test 未 mock 或覆盖 `/workspace`、`/collections`、`/collections/:collectionId`、`/health`、`/artifacts`、`/artifacts/:artifactId`，见 `web/src/routes/AppRoutes.test.tsx:5-34`、`web/src/routes/AppRoutes.test.tsx:58-92`。重组时必须先补齐完整 27 路由矩阵，避免“导航减少”意外变成“路由删除”。

### Information Ownership

| Fact/action | Current duplication | Proposed canonical owner |
| --- | --- | --- |
| Workspace 名称、根路径、状态、ID | Workspace summary 展示名称/路径，Settings Workspace Tab 再展示名称/路径/状态/ID，见 `web/src/features/workspace/WorkspacePage.tsx:30-42`、`web/src/features/business/BasicPages.tsx:151-163`。 | `/workspace`；Settings 其他分区不再调用 `getWorkspace` 复制这些事实。 |
| Git present/branch/dirty/HEAD | Workspace summary 与 Settings Git Tab 重复，见 `web/src/features/workspace/WorkspacePage.tsx:37-47`、`web/src/features/business/BasicPages.tsx:185`。 | `/workspace`；不再保留独立 Git 设置页。 |
| Workspace 扫描 | Workspace 可扫描并展示文件表，Inbox 也有“重新扫描”，见 `web/src/features/workspace/WorkspacePage.tsx:117-120`、`web/src/features/workspace/WorkspacePage.tsx:197-219`、`web/src/features/business/BasicPages.tsx:40-54`。 | `/inbox`；Workspace 连接成功只提供“前往收件箱”，扫描和 Source Version 归资料任务。 |
| API/依赖完整状态 | Workspace 嵌入 compact SystemStatus，Dashboard 嵌入 SystemStatus 和一套 operational grid，Settings 又嵌入完整 SystemStatus，见 `web/src/features/workspace/WorkspacePage.tsx:215`、`web/src/features/business/DashboardPage.tsx:21`、`web/src/features/business/BasicPages.tsx:183`。 | `/settings` 为完整诊断；Dashboard 只保留需要行动的异常摘要；Workspace 只显示阻止连接动作的局部错误。 |
| SSE 连接/恢复 | AppShell 已集中显示 connection state 与 retry，见 `web/src/app/AppShell.tsx:156-162`、`web/src/app/AppShell.tsx:180-186`；RAG 又有独立连接条，见 `web/src/features/rag/RagPage.tsx:220-221`。 | AppShell 为全局 owner；Feature 只在连接状态实际改变当前任务时显示局部影响，不复制第二个全局徽标。 |
| 模型 desired/active/applied 与 Secret | 当前 ModelSettingsPanel 独立拥有。 | `/settings/models`，完整保留；不得为了精简合并状态。规范要求见 `.trellis/spec/frontend/model-settings.md:20-31`。 |
| API Token 一次性明文 | 当前 API Token Panel 在创建后强制复制确认并阻止离页，见 `web/src/features/business/BasicPages.tsx:137-147`。 | `/settings/access`，完整保留；这是必要安全文案，不属于冗余说明。 |

### Heading And Chrome Audit

AppShell 当前在 topbar 输出路由级 `h1`，见 `web/src/app/AppShell.tsx:187`；绝大多数 Feature 随后再输出 eyebrow、巨型 `h2` 和说明，例如 Inbox `web/src/features/business/BasicPages.tsx:52-54`、Settings `web/src/features/business/BasicPages.tsx:165-170`、Workspace `web/src/features/workspace/WorkspacePage.tsx:129-144`。CSS 又把 page intro 放大到 `clamp(2.2rem, 5vw, 4.8rem)`，见 `web/src/styles.css:131-135`。这形成“面包屑 + Shell 标题 + 眉题 + 口号标题 + 说明 + Card 眉题/标题/说明”的重复层级。

Graph 和 RAG 在 AppShell 内又各自渲染品牌、`h1` 和产品导航：

- Graph：`web/src/features/graph/GraphPage.tsx:294-300`。
- RAG：`web/src/features/rag/RagPage.tsx:219-225`。

建议的页面框架：

1. AppShell：品牌、一级导航、面包屑、Workspace/Auth/SSE 上下文；不输出页面 `h1`。
2. Route Page：一个 `PageHeader`，包含一个 `h1`、可选的一句说明、页面级操作和必要状态。
3. SectionNav：位于 PageHeader 下或内容区顶部，使用真实 Link 与 `aria-current`；不使用 Card 包裹。
4. Feature body：Card 标题只描述当前对象或任务，不重复 PageHeader。
5. Detail Page：`h1` 使用资源路径/名称/角色等实际值；版本、hash、状态用紧邻元数据，不再增加“详情”大标题。

### Concise Chinese Copy Rules

#### 默认写法

1. 页面标题使用名词或明确任务：`资料收件箱`、`检索`、`知识图谱`、`提案`、`运行状态`。不使用“把异常放在最前面”“先冻结产物，再决定是否入库”一类营销式口号作功能标题；现有例子见 `web/src/features/business/DashboardPage.tsx:21`、`web/src/features/artifacts/ArtifactsPage.tsx:126`。
2. 页面级说明最多一句，回答“这里能完成什么”；不同时解释架构、事实源和未实现项。标题已经说明的内容不再复述。
3. 默认不使用 decorative eyebrow。版本、状态、来源类型应使用 Badge、`dl` 或紧邻元数据；`Learning / Artifact`、`Ops / Knowledge Health`、`Automation credentials`、`Bounded actions` 等中英装饰标签改为中文任务名称或移除。
4. 用户可见名词优先统一为“工作区、资料版本、提案、流程、产物、复习、访谈、记忆、对话”；API/Schema 名、ID、枚举和代码保留原文并使用技术样式。不要在同一层级交替使用“工作区/Workspace”表达同一导航概念。
5. 命令按钮使用动词 + 对象：`创建提案`、`刷新流程`、`连接工作区`；状态使用已发生事实：`正在重连`、`需要重启`、`写回失败`。标题不加句号。
6. 普通说明避免反复出现“真实、服务端、不伪造、不会用样例”。这些约束只在信任边界第一次出现，或在错误/禁用状态中解释原因。当前多个 Workspace gate 重复这类文字，见 `web/src/features/business/BusinessWorkspaceGate.tsx:5-16` 和各 Feature gate。
7. 用户界面不显示内部里程碑。AppShell 的 `M9 · 业务操作台` 和 Settings 的 `M8/M10-02` 应删除，见 `web/src/app/AppShell.tsx:184`、`web/src/features/business/BasicPages.tsx:187`。

#### 必须保留的文案

以下文案可以缩短但不能隐藏到无关页面或删除：

- 认证、权限、CSRF、Session-only 和 Secret 生命周期。
- API Token 只显示一次、离页丢失和撤销失败。
- 模型 desired/active/applied、rollout、restart-required 和 test != saved/applied。
- Approval、Change Hash、版本冲突、写回前检查、不可逆/不可自动恢复边界。
- Workspace、Cursor、资源版本或 owner mismatch。
- Loading、Empty、Error、Degraded、Waiting for Human、Reconnect、Recovery Required 的不同结果和下一步。
- 扫描不等于解析/索引完成，批准不等于已经写回，SSE 已连接不等于业务成功。

组件规范要求业务错误展示错误、影响和下一步，且关键状态不能只靠颜色，见 `.trellis/spec/frontend/component-guidelines.md:9-14`、`.trellis/spec/frontend/component-guidelines.md:39-48`。文案精简不能把这些状态合并成通用 toast 或 Empty State。

#### Card 说明规则

- Card 默认只有短标题；页面说明已经覆盖的内容不再写 Card description。
- 只有安全、权限、重启、不可逆、版本冲突、一次性 Secret、禁用原因或“请求已受理但未完成”可以常驻在相关控件附近。
- 长的技术原理或 Schema 说明使用按需 `<details>`，但不能把阻止当前操作的原因放进去。
- 错误采用“结果 + 原因 + 下一步”：例如“写回未开始。Git 基线已变化。刷新提案后重新审阅。”错误码作为次级技术信息保留。

### Copy Correctness Findings

Dashboard 当前写着“Review、Artifact、Memory 与正式 Auth 属于未交付能力”，见 `web/src/features/business/DashboardPage.tsx:21`。这与前端规范记录的已交付事实冲突：认证已交付见 `.trellis/spec/frontend/index.md:25-26`，Artifact 见 `.trellis/spec/frontend/index.md:27-28`，Review 见 `.trellis/spec/frontend/index.md:29-36`，Interview/Memory 见 `.trellis/spec/frontend/index.md:41-44`。这是错误信息，不是可保留的谨慎说明，应在本次文案整理中删除或改成由真实 capability/status API 驱动的状态。

Settings 的“允许与未交付操作”把 `Artifact / Review / Memory` 仅标成 `M8`，同时暴露内部版本号，见 `web/src/features/business/BasicPages.tsx:187`。该区既不能作为当前能力事实源，也没有用户操作，建议整体移除。

### Test And Verification Impact

1. `AppShell.test.tsx` 改为断言恰好 4 个一级入口、无“系统”分组、无 7 个知识能力的一级平铺，并覆盖 `/`、详情路由和 Settings 子路由的 active state。当前测试在 `web/src/app/AppShell.test.tsx:44-57` 固化旧结构，必须同步更新。
2. `AppRoutes.test.tsx` 建立完整 27 条既有路由兼容表，并加入新增 Settings 子路由；现有缺口见 `web/src/routes/AppRoutes.test.tsx:58-92`。
3. 为知识、产出、设置三个 SectionNav 各做一次 reachability/`aria-current` 参数化测试；详情路由应激活其基础入口。
4. Settings 测试应通过 MemoryRouter 深链、刷新与 back/forward 验证分区，不再只用 `mouseDown` 切不可恢复 Tab。
5. Workspace 与 Settings 组合测试应证明名称/根路径/Git 只出现于 `/workspace`；Inbox 测试接管扫描和扫描结果。当前 Workspace 测试明确依赖页面内扫描，见 `web/src/features/workspace/WorkspacePage.test.tsx:91-95`、`web/src/features/workspace/WorkspacePage.test.tsx:118-143`。
6. 路由集成测试断言每页只有一个页面级 `h1`；Graph/RAG 不再出现第二套产品导航。
7. 文案测试优先断言角色、状态、必要边界和可操作下一步，避免固定整段普通介绍；Token、模型状态、审批/写回和错误恢复仍需精确覆盖。
8. 浏览器门禁继续覆盖 `1440x900` 与 `390x844`、移动 Sheet 焦点恢复、键盘、`aria-current`、无横向溢出和零 console error。现有架构要求见 `docs/architecture/frontend-architecture.md:247-260`、`docs/architecture/frontend-architecture.md:294-300`。

### Visual Scope Implication

仅修改 `:root` 变量不能完成全站白蓝统一。主样式直接写入暖色背景、黄铜 active、Georgia 大标题和装饰渐变，见 `web/src/styles.css:1-18`、`web/src/styles.css:25-31`、`web/src/styles.css:89-107`、`web/src/styles.css:131-157`；RAG 还有大量硬编码暖色，Graph/Timeline/Model Settings/Semantic Link 各有独立 CSS。

建议本任务覆盖所有路由的 Shell、PageHeader、SectionNav 和共享 semantic tokens，同时保留 Feature 内部布局与行为。只重做 Workspace/Settings/侧栏会让 Graph、RAG、Timeline 等继续拥有第二套 chrome 和不同视觉语言，无法满足“全局视觉”验收。

## Code Patterns

- Route-level lazy loading 已集中在 `web/src/routes/AppRoutes.tsx:5-30`，新 Settings 子页面应继续沿用，不把全部设置重新打进 Shell。
- AppShell 的 active route 当前通过基础 path 前缀匹配，见 `web/src/app/AppShell.tsx:71-72`；route registry 可保留同样语义，但应使用显式 route family，避免字符串表与 Route 表漂移。
- `Link/NavLink` 和 `aria-current` 已用于桌面/移动导航，见 `web/src/app/AppShell.tsx:85-120`；SectionNav 应复用该模式。
- Mobile Sheet 已通过 restore ref 恢复焦点，见 `web/src/app/AppShell.tsx:149`、`web/src/app/AppShell.tsx:186` 和 `web/src/shared/ui.tsx:23`；导航重组不能退化此行为。
- Error/Unavailable/Empty 已有语义组件，见 `web/src/shared/ui.tsx:19-21`；精简应改调用文案，不应把不同状态合并。
- Model Settings 的状态和 Secret 契约已经明确，见 `.trellis/spec/frontend/model-settings.md:20-31`；迁移路由时只改变组合位置。
- Routes/App 只组合 Feature，不承载业务规则，见 `.trellis/spec/frontend/directory-structure.md:35-43`。

## External References

未使用外部资料；本审计只依据仓库代码、测试、任务和规范。

当前实现可用的版本事实来自 `web/package.json:18-32`：React Router DOM `7.18.1`、Radix Tabs `^1.1.18`、Lucide React `^1.25.0`。提出的 Link/NavLink、nested Settings routes、Sheet 和图标方案不需要新增依赖。

## Related Specs

- `.trellis/spec/frontend/index.md:70-91`：开发前检查和 canonical 依赖边界。
- `.trellis/spec/frontend/directory-structure.md:35-43`：Routes/App/Feature 责任边界。
- `.trellis/spec/frontend/component-guidelines.md:16-30`：页面组合、组件职责和写操作状态。
- `.trellis/spec/frontend/component-guidelines.md:39-48`：键盘、焦点、图谱 fallback 和状态可区分性。
- `.trellis/spec/frontend/model-settings.md:20-31`：模型状态和 Secret 生命周期不可删减项。
- `.trellis/spec/frontend/quality-guidelines.md:17-25`：路由拆包、用户可见状态和行为测试。
- `docs/architecture/frontend-architecture.md:22-32`：Routes/Pages 到 Feature/API/Event 的依赖方向。
- `docs/architecture/frontend-architecture.md:66-90`：URL/Local/Event State 所有权。
- `docs/architecture/frontend-architecture.md:92-111`：现有产品路由基线。
- `docs/architecture/frontend-architecture.md:288-300`：App Shell、Workspace SSE 与桌面/移动 smoke 基线。

## Unresolved Product Decisions

1. **“知识”是否包含学习与研究。** 本文推荐把 Chat、Review、Interview、Memory 放入“知识 > 学习”，从而维持四个一级入口；若产品认为个人学习是与知识管理等重的心智模型，则应增加第五个一级入口“学习”，而不是把 4 个链接塞进“更多”。
2. **“产出”是否是最终名称。** Proposal、Workflow、Artifact 的共同语义也可以命名为“交付”或“审阅与产出”。本文暂用 PRD 候选“产出”，但需要产品确认对 Workflow 用户是否足够直观。
3. **本轮是否一次覆盖全部路由视觉。** 研究建议至少全站统一 Shell、PageHeader、SectionNav、字体与 semantic tokens；Feature 深层布局可以保持。若只覆盖 Workspace/Settings，将无法满足全局无暖色/无双重 chrome 的验收。
4. **Workspace 扫描是否迁移到 Inbox。** 本文推荐 Inbox 成为唯一扫描入口，Workspace 只管理连接与路径。若首次连接必须在同页立刻扫描，需要明确它是一次性 onboarding shortcut，且不要继续维护第二套扫描结果页面。
5. **Settings landing 的产品预期。** 本文选择侧栏“设置”进入 `/workspace`，以保证首次连接最短；`/settings` 负责运行状态。如果已连接用户更常访问运行状态，可让 primary 在已连接后进入 `/settings`，但这会让同一一级入口随状态改变目的地，不建议默认采用。
6. **中英文领域词表。** 需要确认用户界面是否统一采用中文名词，还是保留 Workspace/Proposal/Workflow/Artifact/Review 作为正式产品术语。无论选择哪种，都应形成一份映射，避免同层随机混用。

## Caveats / Not Found

- 本次是源代码与测试的只读审计，没有运行浏览器或生成视觉截图；视觉结论来自实际 CSS，不构成 1440x900/390x844 渲染验收。
- 任务目录当前只有 `prd.md` 和 `task.json`，没有 `design.md` 或 `implement.md`；上述结构仍需在设计文档中确认后实施。
- 未发现产品使用频率分析、用户研究或导航遥测；“四入口”和默认 landing 依据当前功能关系、现有 Dashboard 链路与 PRD 两次导航约束提出。
- Docker Host Controller、Root Grant、Compose mount 和宿主机路径契约明确不在本任务范围，未纳入本研究。
