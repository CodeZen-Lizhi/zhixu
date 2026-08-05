# 工作台导航与全局视觉重设计

## Goal

把工作台从按技术能力平铺、重复解释和暖色纸张风格，重设计为简洁、冷静、适合高频操作的白蓝知识工作台。用户应能快速找到工作台、知识、创作和设置，不需要先理解系统分层；真实业务能力、安全边界和可访问性状态必须完整保留。

## Confirmed Facts

- 当前 route registry 把“产出”默认目标设为 `/proposals`，并把 Proposal、Workflow Run、Artifact 作为同组菜单项，见 `web/src/routes/route-display.ts:53`；这三个对象分别表示审批、执行和派生结果，不是用户发起创作的同级任务。
- Topbar 的 `…` 菜单重复提供工作区设置、系统状态和资料收件箱，见 `web/src/app/AppShell.tsx:247`；这些入口已经分别由 Settings、一级导航和知识菜单拥有。
- 已连接首页的空态焦点、资料空态和“查看全部”会重复导向 Inbox，见 `web/src/features/business/DashboardPage.tsx:256`、`:285`、`:291`；首页没有新建文章或整理成文入口。
- 首页只在 `720px` 以下整体换列，桌面使用固定 `164px / 188px / 320px` 栅格且内容宽度上限为 `1440px`，见 `web/src/styles.css:153`、`1585`、`1636`、`1806`；断点适配没有覆盖所有中间可用宽度与无意义页面滚动。
- 系统状态完整视图把 14 个运行事实平铺为四列矩阵，并把版本、请求 ID 与可操作异常置于同一视觉层级，见 `web/src/features/system-status/SystemStatusPage.tsx:178`。
- Settings 已有工作区、模型与检索、数据导出、访问权限、系统状态五个分类；现有业务深链与 AppShell Workspace 门禁已有测试保护。

## In Scope

- 全站颜色、字体、按钮、表面、边界、Focus、基础字号和共享状态组件的白蓝冷色视觉统一。
- AppShell 一级导航、知识/创作按需入口、活动路径、未连接状态、桌面侧栏和移动 Sheet。
- 已连接 Dashboard 的固定常用动作、重复 Inbox 入口移除、流式栅格和页面级滚动修正。
- Topbar 重复菜单删除、认证模式退出入口归位，以及系统状态“总览 + 异常详情”重构。
- AppShell 与路由页的单一页面标题、共享 PageHeader 和路由展示元数据；不重做 Feature 内部数据流。
- Settings 分类、URL section、Workspace/Git 合并、Runtime/Maintenance 合并和目标页面文案精简。
- Workspace 首次连接模式、已连接后的 Settings 归属和重复事实移除。
- `1440x900`、`1024x768`、`768x1024` 与 `390x844` 的响应式、键盘、焦点、长文本和页面滚动验证。

## Requirements

### R1. 一级导航

- 桌面侧栏只常驻“工作台 / 知识 / 创作”三个业务入口，设置固定在底部。
- 删除“系统”分组标题和独立“工作区”一级入口；Workspace 连接与配置统一从设置或首次连接流程进入。
- 知识使用按需菜单承载探索、组织与学习能力；创作是直接进入 `/authoring` 的普通一级链接，不保留 Chevron 技术对象菜单，也不能默认进入 `/proposals`。
- 创作台首先呈现“新建文章”和“整理成文”，并按“最近草稿 / 待确认 / 已完成”展示进度；Proposal、Workflow Run、Artifact 只作为详情和高级记录中的生命周期身份。
- 所有原有能力在最多两次导航操作内可达，详情页正确高亮所属一级入口。
- 未连接 Workspace 时隐藏知识和创作，不发出依赖 Workspace 的业务请求。
- 路由、面包屑、一级入口和详情归属使用一份展示元数据 registry，避免 AppRoutes 与 AppShell 继续维护两套含义不一致的清单；registry 不承载业务规则。

### R2. Settings 信息架构

- 7 个横向页签收敛为“工作区 / 模型与检索 / 数据导出 / 访问权限 / 系统状态”5 个分类。
- Workspace 与 Git 合并；Runtime 与 Maintenance 合并。同一 Workspace 名称、路径、Git 和连接事实只有一个主要展示位置。
- 分类状态使用 `?section=workspace|models|exports|access|system`；无参数或未知值安全回退到 Workspace。
- 桌面使用纵向本地导航，移动端使用单一分类菜单，不使用横向滚动的 5 页签条。
- 系统状态默认只回答“当前能否继续工作”：正常依赖合并为摘要，异常、降级、关闭和恢复动作按影响展开；版本与请求 ID 放入“技术详情”，不再渲染大面积能力矩阵。

### R3. Workspace 入口

- `/` 与 `/workspace` 在未连接时提供“新建 / 使用 Workspace ID”两个互斥模式，默认只展示当前模式的字段。
- 连接完成后，当前 Workspace、Git 和切换操作由 Settings 工作区分类拥有；扫描与扫描结果统一归资料收件箱，Workspace 页面不保留第二套扫描结果。
- 完整系统状态只在 Settings 系统状态分类展示；连接页只在阻断当前操作时显示紧凑、可操作状态。

### R4. 文案密度

- AppShell 不再渲染第二个页面 `h1`；路由页面使用共享 PageHeader 拥有唯一 `h1` 和最多一句必要说明。
- Workspace 与 Settings 不再保留额外的品牌引言、巨型标题或重复 section heading。
- 删除中英混排 Eyebrow、口号式标题、重复 Card 说明和与当前操作无关的常驻实现细节。
- 普通设置项使用紧凑的名称与控件/值；辅助文字最多一行。
- 权限、Secret、重启、冲突、不可逆操作和失败原因必须保留在对应控件附近；较长技术解释按需展开。
- 删除 Topbar `…` 快捷菜单；工作区设置、系统状态和资料收件箱使用既有正式入口。认证模式下的退出登录进入明确的账户/会话按钮，不复用无名称的通用菜单。

### R5. 视觉系统

- 主视觉使用白色表面、冷灰画布/边界、蓝色主操作和独立的绿/琥珀/红语义色。
- 先把同时承担选中与警告语义的 `--brass` 拆成产品蓝 accent 与独立 warning token，再移除米黄、黄铜、暖灰主背景、装饰渐变、Georgia 和胶囊形普通按钮。
- 标题与正文统一使用无衬线字体链；字号使用固定层级，不随视口连续缩放；字距为 `0`。
- Settings 区段使用留白与分隔线，不把每个设置做成 Card；禁止 Card 嵌套 Card。
- 仅 Dropdown、Sheet 和 Dialog 使用轻量冷灰阴影；页面区段与普通 Card 不使用装饰阴影。

### R6. 兼容与可访问性

- 保留所有现有业务 URL 和路由语义，不删除 API、状态、错误、权限或业务入口。
- 不改变 TanStack Query、SSE、Active Workspace、认证、Secret 和模型 desired/active/applied 的事实所有权。
- 主导航、菜单、Settings 分类、分段模式、Sheet 和 Dialog 支持键盘、可见焦点与关闭后的焦点恢复。
- 状态不得只靠颜色表达；Reduced Motion 下关闭非必要位移和动画。
- 已连接首页和共享内容容器使用流式可用宽度与可重排栅格；任何受支持宽度下都不得产生页面级横向滚动，正常内容未超过视口高度时不得因容器最小高度或额外 padding 产生无意义纵向滚动。
- 除 `1440x900` 与 `390x844` 基准外，增加 `1024x768` 与 `768x1024` 中间宽度验证，防止只在单一断点两端成立。

### R7. 已连接首页

- 首页把“需要继续的真实事项”“固定常用动作”和“最近上下文”分开；待办仍由真实 Workflow/Proposal 有界列表派生，资料区只展示真实 Source Version。
- 常用动作首版固定为“快速记录 / 新建文章 / 整理成文 / 搜索知识”，不提供用户自定义、排序或隐藏能力。
- 快速记录打开全局 Quick Capture；新建文章进入 `/authoring/new`；整理成文进入 Organizing 发起页；搜索知识进入 `/search`。依赖能力未交付前不得渲染可点击假入口或假成功页面。
- 同一屏只保留一个次级 Inbox 导航；空资料提示、最近捕获标题和待办空态不得再分别重复 Inbox 主按钮。
- 最近资料与最近捕获只负责恢复上下文，不承担全局功能导航；首页不伪造最近访问、文章摘要、总数或跨资源关系。

## Key Decisions

- 一级“产出”改为“创作”，默认进入独立创作台；技术生命周期对象退出一级菜单。
- 首页四个常用动作固定，不建设个性化快捷入口。
- 系统状态采用“运行总览 + 仅异常展开 + 技术详情”，Topbar 通用 `…` 菜单删除。
- Document Draft 与 `/authoring` 由笔记工作流父任务下的新增创作子任务交付；本任务只在依赖能力真实可用后完成导航和首页集成。

## Acceptance Criteria

- [x] 已连接时侧栏只常驻“工作台 / 知识 / 创作”，底部只有“设置”；不再出现“系统”或独立“工作区”一级入口。
- [x] 知识菜单可到达 Inbox、Search、Chat、Graph、Collections、Timeline、Health、Review、Memories 和 Interviews；创作入口优先提供“新建文章”和“整理成文”，Proposals、Workflows 和 Artifacts 仍可在两次导航操作内到达但不作为同级主任务。
- [x] 点击“创作”进入 `/authoring`，页面按用户任务与状态组织；侧栏不再出现 Proposal、Workflow Run、Artifact 技术对象菜单。
- [x] 未连接时仍可从 `/` 与 `/workspace` 创建或按 Workspace ID 打开，且知识/创作请求不会执行。
- [x] Workspace 连接完成后不再展示第二套扫描结果；扫描与结果只由资料收件箱拥有。
- [x] Settings 只显示 5 个分类；Workspace 与 Git、Runtime 与 Maintenance 分别合并，旧 `/settings` 深链仍可用。
- [x] 系统状态正常时只显示可继续工作的紧凑摘要；存在异常时显示受影响能力、影响和恢复动作，版本与请求 ID 仅在技术详情中出现。
- [x] Topbar 不再显示 `…` 快捷菜单；认证模式仍有明确、可访问且不重复正式导航的退出入口。
- [x] 每个路由页面只有一个页面级 `h1`；Workspace 与 Settings 没有额外品牌引言或巨型标题，普通设置项没有大段介绍，必要风险说明紧邻相关操作。
- [x] 全站共享视觉不再使用 `#f3f0e8`、`#e9e3d6`、`#a9782c`、Georgia 或页面背景渐变；主按钮为蓝色，状态色语义保持独立。
- [x] 现有业务深链、模型配置状态、Token 一次性明文保护、导出恢复、Workspace cache/SSE 隔离均未因重组而改变。
- [x] `1440x900`、`1024x768`、`768x1024` 与 `390x844` 下无页面级横向溢出、无意义纵向滚动、文本遮挡或不稳定控件尺寸；长路径、Model、Error 和 Scope 可完整查看。
- [x] 已连接首页固定显示“快速记录 / 新建文章 / 整理成文 / 搜索知识”，每项进入真实目标；页面最多保留一个次级 Inbox 导航，不再出现三个同义入口。
- [x] 键盘可打开和选择知识/创作入口、切换 Settings 分类和 Workspace 模式；Focus Ring 清晰，关闭浮层后焦点返回触发控件。
- [x] 前端 lint、typecheck、相关组件测试、build、`git diff --check` 和真实浏览器 Console/Network 检查通过。

## Out of Scope

- Workspace Root Grant、Host Controller、Compose 挂载、Workspace 身份或后端路径契约；这些由 `07-31-docker-host-workspace-path` 任务拥有。
- Dashboard 的品牌入口态与未连接 Workspace 首屏构图；已连接首页的常用动作、重复入口移除和响应式修正纳入本轮导航集成，历史首页任务只作为现状证据。
- 快捷入口个性化、排序、使用频率学习和跨设备同步。
- Document Draft、Organizing 或 Quick Capture 的业务实现；这些由 `08-02-note-workflow-capabilities` 及其子任务拥有，本任务只做真实能力就绪后的界面集成。
- 重做 Graph、Review、Collection、Artifact 等业务页面的数据流或领域交互。
- 删除业务路由、伪造功能完成状态或改变 Workspace/storage owner。
