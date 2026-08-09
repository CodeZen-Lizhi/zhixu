# 工作台导航与全局视觉重设计

## 设计目标

把知序从“把技术模块逐项陈列出来的说明页”改成一个适合长期、高频使用的知识工作台。界面先帮助用户定位和操作，再在风险真正相关时解释约束。

设计方向命名为 **Clearline**：白色主画布、冷灰结构线、单一蓝色交互信号和紧凑的信息层级。辨识度来自贯穿导航、焦点和选中态的蓝色定位线，而不是渐变、暖色纸张、巨型标题或装饰性卡片。

## 产品不变量

- 保留全部现有业务能力、URL 深链、Workspace 隔离、服务端事实源和错误语义。
- “简洁”只移除重复介绍和常驻技术解释，不移除权限、Secret、重启、冲突、不可逆操作和失败原因。
- 未连接 Workspace 时仍能完成首次连接；需要 Workspace 的业务页面不发出无效请求。
- Settings 模型面板继续分别展示 desired、active、API applied 和 Worker applied，不把保存描述成已经生效。
- 状态不能只用颜色表达；保留图标、文字、形状或标签。

## 信息架构

### 一级导航

桌面侧栏只常驻三个业务入口，设置固定在侧栏底部：

| 入口 | 默认目标 | 行为 |
| --- | --- | --- |
| 工作台 | `/dashboard` | 直接进入当前 Workspace 概览 |
| 知识 | `/inbox` | 标签进入资料收件箱；相邻 Chevron 图标打开能力菜单 |
| 创作 | `/authoring` | 进入创作台；首先呈现“新建文章”和“整理成文”，不再默认进入待审提案 |
| 设置 | `/settings` | 固定在底部，不再出现“系统”分组标题或独立“工作区”一级入口 |

知识菜单只在用户请求时展开，按任务分组但不添加说明段落：

| 分组 | 入口 |
| --- | --- |
| 资料 | 资料收件箱 `/inbox`；资料详情继续从列表进入 |
| 探索 | 检索 `/search`、对话 `/chat`、知识图谱 `/graph` |
| 组织 | 集合 `/collections`、时间线 `/timeline`、知识健康 `/health` |
| 学习 | 复习 `/review`、记忆 `/memories`、访谈 `/interviews` |

创作包含两条主任务：“新建文章”从空白 Document Draft 开始，“整理成文”从确认材料与模板开始。提案 `/proposals`、流程 `/workflows` 和产物 `/artifacts` 保持可达，但作为审批、执行和派生结果的生命周期视图，不再与两条创作任务同级陈列。当前路由属于哪个一级入口，由路径映射决定；详情页继续高亮其所属入口。

知识的文字区域继续是普通链接，Chevron 是带 Tooltip 和可访问名称的独立图标按钮。创作是直接进入 `/authoring` 的普通一级链接，不再保留 Chevron 技术对象菜单。创作台顶部提供“新建文章 / 整理成文”，其中“新建文章”进入 `/authoring/new` Markdown 编辑器；下方以“最近草稿 / 待确认 / 已完成”组织真实状态。Proposal、Workflow Run 与 Artifact 的技术身份只在详情和高级记录中展示。桌面与移动端消费同一导航事实，旧能力路由仍最多两次操作可达。

AppRoutes 继续拥有 lazy Route 组合；另建一份不含 Query、权限或业务规则的 route display registry，集中表达 `section / label / basePath / match / parent`。AppShell、面包屑和活动入口消费该 registry，防止路由与导航继续维护两套漂移清单。

未连接 Workspace 时隐藏知识和创作，只显示工作台与设置。根路由和 `/workspace` 继续承担首次连接；连接完成后，独立 Workspace 入口不再常驻导航。

### Settings 分类

现有 7 个横向页签收敛为 5 个用户任务分类：

| 分类 | 内容 | 合并或移除的重复 |
| --- | --- | --- |
| 工作区 | 当前名称、宿主机路径、连接状态、Git 分支/HEAD/dirty 和切换入口 | 合并 Workspace 与 Git；当前事实只在这里完整展示 |
| 模型与检索 | Chat、Embedding、desired/active/applied、连接测试和保存 | 保留模型契约，不重复页面级技术说明 |
| 数据导出 | Workspace 附件导出和历史 Job | 原导出页签 |
| 访问权限 | API Token 创建、一次性明文确认、列表与撤销 | 原 API Token 页签 |
| 系统状态 | API/依赖状态、维护边界和恢复入口 | 合并运行依赖与维护边界；里程碑文案不作为主要内容 |

桌面使用 `11rem minmax(0, 1fr)` 的本地导航与内容区；本地导航粘在内容顶部，只显示分类名称和图标。`720px` 以下改为单一分类菜单，当前分类显示在触发器中，避免 5 个页签横向滚动。

分类状态写入 `?section=workspace|models|exports|access|system`，无参数时默认为 `workspace`。未知值回退到 `workspace`，但不影响其他 Query 参数。旧 `/settings` 深链保持可用。

系统状态分类不再渲染 14 格能力矩阵。顶层只显示一条运行结论：正常时为“运行正常”，降级或失败时为“部分功能受影响”或“当前无法继续”，并附最近检查时间与重新检查动作。下方只列异常、关闭或降级项，每项包含影响、原因和可执行恢复入口；所有正常能力合并为“其余服务正常”。版本、请求 ID 与完整依赖值进入默认折叠的“技术详情”。Loading、请求失败和响应解码失败保持独立状态。

### Workspace 页面

`/` 与 `/workspace` 只拥有首次连接和恢复无效本地引用的职责：

- 未连接：显示“连接工作区”标题、一句说明，以及“新建 / 使用 Workspace ID”分段模式。
- 新建模式：名称、宿主机路径、初始化 Git 开关和主操作。
- 已有模式：Workspace ID 和打开操作，不再把两张表单同时常驻展示。
- 连接成功：进入 `/settings?section=workspace`；当前 Workspace 与 Git 由 Settings 工作区分类统一拥有，扫描入口指向资料收件箱。
- 系统健康在连接页只显示紧凑状态；完整依赖事实位于 Settings 的系统状态分类。

扫描与扫描结果只由 `/inbox` 拥有。Workspace 连接页不再维护第二套扫描 mutation、文件表和“扫描不等于索引”说明；连接成功后的下一步使用一个“前往资料收件箱”入口。

Workspace Root Grant 和 Host Controller 的真实切换状态由独立路径任务定义。本任务只提供承载该状态的紧凑 UI，不推导或伪造运行时成功。

### 已连接首页

首页按三层组织，不再把资料收件箱重复成整页主角：

1. “先处理这一件”继续从等待人工处理的 Workflow、失败 Workflow 和待审 Proposal 中按现有优先级选择，数据失败时保留明确错误与重试。
2. 紧接一条固定常用动作带：“快速记录 / 新建文章 / 整理成文 / 搜索知识”。它们分别进入 Quick Capture、`/authoring/new`、Organizing 发起页和 `/search`，不做用户自定义或排序。
3. 最近资料只保留一个主列表或续接区域；空态可以有一个次级 Inbox 文本链接，但不再同时出现“查看资料收件箱 / 前往资料收件箱 / 查看全部”三个同义入口。

常用动作使用图标、短名称与必要状态，不做四张装饰性 Card；它是稳定的命令带。依赖能力尚未交付时，首页集成不得放置可点击占位链接或成功空壳，而应随对应能力任务一起启用。

### Topbar

Topbar 只保留面包屑、实时连接状态和认证/会话状态。删除通用 `…` 菜单：工作区设置、系统状态和资料收件箱继续由正式导航拥有。`required` 认证模式使用带 Tooltip 的明确账户/退出按钮；开发模式只显示不可交互状态，不制造菜单。

## 页面与文案规则

### 层级

- AppShell 只保留品牌、一级导航、面包屑和全局上下文，不再渲染页面 `h1`。
- 路由页面使用共享 `PageHeader` 拥有唯一 `h1`、可选的一句说明、页面级操作和必要状态。Workspace 和 Settings 不再同时出现 Topbar 标题、巨型引言标题和卡片标题。
- 页面级说明最多一句，建议不超过 48 个中文字符；没有必要说明时直接省略。
- 取消中英混排 Eyebrow、口号式标题和“这里不会……”式防御性介绍。
- 普通设置项使用“名称 + 控件/值”的紧凑行；只有输入格式确实不明显时保留一行辅助文字。
- 安全与权限说明紧邻对应操作；长技术说明放入 `details`，标题统一为“技术详情”或更具体的风险名称。
- 错误出现时替代普通辅助文字，给出影响和下一步；不重复通用错误码与同义句。

### 建议文案

| 位置 | 文案 |
| --- | --- |
| Workspace 未连接标题 | 连接工作区 |
| Workspace 说明 | 选择知序可以访问的本地目录。 |
| Settings 页面标题 | 设置 |
| Settings 工作区标题 | 工作区 |
| Settings 模型标题 | 模型与检索 |
| 根路径字段 | 宿主机目录 |
| 打开已有模式 | 使用 Workspace ID |

## 视觉系统

### 颜色令牌

```css
--canvas: #f7f9fc;
--surface: #ffffff;
--surface-subtle: #f1f5fa;
--surface-selected: #eaf2ff;
--ink: #172033;
--muted: #66758a;
--line: #d8e1ec;
--line-strong: #7c8da5;
--blue: #2563eb;
--blue-hover: #1d4ed8;
--blue-focus: #2563eb;
--green: #166534;
--amber: #8a4b0f;
--red: #b42318;
--mono: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
```

- 页面背景以 `--canvas` 为底，内容与工具表面使用纯白；不使用径向或线性渐变。
- 蓝色只负责当前定位、主操作、链接和焦点，不把所有状态染成蓝色。
- 成功、警告、错误继续使用绿、琥珀、红，并配文字或图标。
- 移除 `#f3f0e8`、`#e9e3d6`、`#a9782c` 及其暖色透明派生值。

### 字体与字号

不引入远程字体或新的运行时依赖。正文和标题统一使用现有的 `Avenir Next / PingFang SC / Noto Sans SC` 字体链，移除 Georgia。英文和数字由 Avenir Next 提供清晰识别，中文使用平台原生高质量字形。

| 用途 | 字号 / 行高 | 字重 |
| --- | --- | --- |
| 页面标题 | `1.5rem / 2rem` | 650 |
| 分类标题 | `1.125rem / 1.6rem` | 650 |
| 卡片或工具标题 | `0.9375rem / 1.4rem` | 650 |
| 正文 | `0.875rem / 1.5rem` | 400 |
| 辅助文字 | `0.75rem / 1.25rem` | 500 |
| 导航 | `0.875rem / 1.25rem` | 550，活动态 650 |

字号不随视口宽度缩放，`letter-spacing` 统一为 `0`。ID、哈希、路径和数值使用 `--mono`，并启用 tabular numbers。

### 空间与表面

- 桌面侧栏宽 `14.5rem`，Topbar 高度 `3.5rem`；内容最大宽度 `90rem`。
- 内容间距使用 `4/8/12/16/24/32px` 六档；设置行垂直高度以 `56-72px` 为基线。
- 普通容器圆角 `6px`，对话框 `8px`；按钮不使用胶囊形，状态 Badge 可以保留胶囊形。
- Settings 分类和页面区段使用留白与分隔线，不把每个区段包成浮动 Card。
- Card 只用于重复条目、对话框和真正需要框定的工具；禁止 Card 内再嵌 Card。
- 页面与 Card 不使用常驻阴影；Dropdown、Sheet 和 Dialog 使用单一冷灰阴影层级。

### 控件与图标

- 主按钮为蓝底白字；次按钮为白底冷灰边框；危险按钮为红色。
- 工具、刷新、菜单和关闭使用 Lucide 图标按钮与 Tooltip；清晰命令可使用图标加文字。
- 数字选项使用输入框或 Stepper，布尔项使用 Checkbox/Toggle，互斥模式使用 Segmented Control。
- 所有交互控件保持至少 `44x44px` 的触达区域，视觉图形可以更小。

### 动效

- Hover、Active 和 Focus 使用 `140-180ms` 的颜色、背景与 `transform` 过渡。
- 导航活动蓝线以 `transform: scaleY()` 或平移进入；Dropdown 只做 `4px` 位移与淡入。
- 不使用页面级滚动动画、视差、弹跳或装饰性加载动画。
- `prefers-reduced-motion` 下关闭位移与非必要动画。

## 响应式结构

### Shell Navigation

- `> 920px` 固定左侧栏；知识菜单从侧栏展开，创作直接进入创作台。Settings 使用左侧分类导航与右侧内容。
- `<= 920px` 侧栏收进 Sheet；Sheet 直接呈现一级入口，知识与创作消费和桌面相同的任务数据。Topbar 保留品牌、菜单按钮和必要状态。
- 所有宽度下，Topbar 只保留面包屑和紧凑状态/操作；页面标题由内容区的 `PageHeader` 唯一拥有。

### Content Wide `> 1100px`

- 通用页面内容使用居中最大宽度；已连接首页使用主区域全部可用宽度。
- 首页通过 `minmax(0, 1fr)`、`auto-fit` 和容器可用宽度组织焦点、命令带与最近资料，不依赖固定 `164px / 188px / 320px` 列组合。

### Content Intermediate `721-1100px`

- 首页日期、标题、上下文操作允许换行；焦点区重排为“标签 + 内容”与下一行操作，常用动作带按两列排列，资料区切为单列。
- Settings 在内容不足时提前切为单列或移动分类菜单，不以 Shell 断点强行维持双列。
- `1024x768` 与 `768x1024` 都不得出现页面级横向滚动。内容少于视口时，`min-height`、padding 和边框合计不得额外制造纵向滚动。

### Content Mobile `<= 720px`

- Topbar 高度稳定，次要状态进入菜单；`PageHeader` 的标题和页面操作允许换行，不得相互遮挡。
- Settings 分类使用单一菜单触发器，内容单列。
- 表单按钮占满可用宽度；表格保持受控横向滚动，页面本身不得横向溢出。
- `390x844` 下最长路径、错误、模型名称和 Token Scope 必须换行或截断并可完整查看。
- 首页常用动作保持两列稳定网格，标签换行不改变相邻按钮尺寸；焦点与最近资料单列显示。

## 状态与可访问性

- 主导航、分类导航、Dropdown、Sheet、Tabs/Segmented Control 和 Dialog 全部支持键盘操作与焦点恢复。
- Focus Ring 使用 `2px --blue-focus` 加 `2px` offset，在白色和浅蓝表面均清晰可见。
- 当前一级导航同时使用蓝线、浅蓝背景、较高字重和 `aria-current`。
- Loading 使用与最终结构一致的 Skeleton；Empty、Unavailable、Error、Conflict 和 Reconnecting 保持不同标题与操作。
- 图标按钮提供 `aria-label` 与 Tooltip；纯状态点必须同时有可访问文字。
- Workspace 切换或认证失效时不保留旧 Workspace 的可见事实。

## 兼容与边界

- 不删除或改写现有 `/inbox`、`/documents`、`/search`、`/graph`、`/collections`、`/health`、`/timeline`、`/proposals`、`/workflows`、`/artifacts`、`/review`、`/memories`、`/interviews`、`/chat`、`/workspace`、`/settings` 深链。
- 新增 `/authoring` 与 `/authoring/new`，但 Document Draft 业务能力由 `08-02-note-workflow-capabilities` 下待创建创作子任务拥有；Quick Capture 与 Organizing 入口分别依赖对应子任务。导航与首页集成只能在真实路由和状态契约可用后启用。
- 新 Settings section 只增加 Query State；不把 Server State 或 Secret 写入 URL。
- AppShell 继续只根据 Active Workspace 决定业务导航可用性，不把导航状态当权限事实。
- 不修改 API Decoder、Query Key、SSE Owner、认证 Cookie/CSRF 或模型设置保存契约。

## 发布与回滚

全局令牌、AppShell 导航、Settings 结构和 Workspace 入口分为独立提交检查点。出现严重视觉或导航回归时，可先回退 AppShell/Settings 组合而保留冷色令牌；实施过程不得删除现有业务路由或改变业务数据。
