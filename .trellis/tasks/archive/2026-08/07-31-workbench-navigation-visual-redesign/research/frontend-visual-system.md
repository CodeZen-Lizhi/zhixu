# Research: Frontend visual system

- Query: 审计 `web/src/styles.css`、全部 feature CSS、`shared/ui.tsx`、`AppShell` 与响应式测试，并为“白蓝、简洁、少文字”的知识工作台提出可实施视觉系统。
- Scope: mixed
- Date: 2026-07-31

## Findings

### 1. Design read

这是给知识工作者高频、长时间使用的产品工作台，不是营销页。推荐设计参数为：

- `DESIGN_VARIANCE: 3`：规则网格，避免装饰性不对称。
- `MOTION_INTENSITY: 2`：只保留状态反馈、菜单和抽屉过渡。
- `VISUAL_DENSITY: 7`：信息密度高，但通过层级、留白和渐进披露降低认知负担。
- 主题锁定为 light：白色表面、冷灰画布、单一产品蓝；绿/黄/红只承担业务状态，Graph 的 Topic/Claim 继续保留非颜色语义。

用户所说的“少文字”应解释为减少重复标签、说明性 chrome 和装饰性 eyebrow，不得删除错误影响、恢复动作、权限边界、状态文字或证据说明。这些内容受现有产品规范约束。

### 2. Files found

| File | Description |
| --- | --- |
| `web/src/styles.css` | 1224 行全局 CSS；同时拥有基础元素、AppShell、共享组件及大量业务 feature 样式。 |
| `web/src/features/graph/graph.css` | 1110 行 Graph 独立视觉域、画布、三栏布局、1080px drawer 与 720px 移动布局。 |
| `web/src/features/graph/semantic-link.css` | 733 行候选关系面板；依赖 Graph 局部变量并用黄褐色与正式关系区分。 |
| `web/src/features/timeline/timeline.css` | 430 行 Timeline 卡片、筛选、列表和 800/540px 响应规则。 |
| `web/src/features/settings/model-settings.css` | 382 行模型设置表单、desired/active 对照和 860/520px 响应规则。 |
| `web/src/shared/ui.tsx` | Button、Badge、Card、状态、Radix Dialog/Sheet/Tabs/Tooltip/Dropdown 的共享入口。 |
| `web/src/app/AppShell.tsx` | 分组侧栏、移动 Sheet、同步/认证/Workspace 上下文和顶部栏。 |
| `web/src/app/AppShell.test.tsx` | 覆盖分组导航、未连接裁剪、根路由 active 与 Suspense；未覆盖移动 Sheet。 |
| `web/src/shared/ui.test.tsx` | 覆盖 Tabs、键盘 Tooltip、Dropdown Escape 焦点恢复。 |
| `web/src/features/graph/GraphPage.test.tsx` | 用 `matchMedia` 覆盖 1080px compact drawer 的 inert、焦点约束和 Escape 恢复。 |
| `web/e2e/semantic-link-graph.smoke.spec.ts` | 1440x900 与 390x844 Graph/Candidate 流程、焦点、console 和 overflow 门禁。 |
| `web/e2e/artifact.smoke.spec.ts` | 1440x900 与 390x844 Artifact 流程及 document/workbench overflow 门禁。 |
| `web/e2e/collection-health.smoke.spec.ts` | 1440x900 与 390x844 Collection/Health 页面和 overflow 门禁。 |
| `web/e2e/m8-learning.smoke.spec.ts` | 1440x900 与 390x844 Review/Interview/Memory 主链路及 overflow 门禁。 |
| `web/package.json` | 已锁定 Radix primitives、CVA、clsx、Lucide、React 19 和 Playwright；无需增加视觉依赖。 |

### 3. Current visual system audit

#### 3.1 Global palette and typography

- 当前根 token 是暖纸张与黄铜：`--paper #f3f0e8`、`--paper-deep #e9e3d6`、`--ink #20211d`、`--muted #716e63`、`--line #d3ccbe`、`--brass #a9782c`，见 `web/src/styles.css:9-19`。Body 还有白色径向渐变，见 `web/src/styles.css:25-32`。这与“白蓝、冷静精密”直接冲突。
- 当前正文栈以 `Avenir Next` 开头，标题和品牌大量使用 Georgia，见 `web/src/styles.css:4`、`web/src/styles.css:94`、`web/src/styles.css:133`。Georgia 不含中文字形，中文会临时回退到平台默认 serif，跨平台字重与字面不稳定。
- 标题大量使用 viewport `clamp()`、负字距和展示级字号，见 `web/src/styles.css:118`、`web/src/styles.css:133`、`web/src/styles.css:451-458`。这适合编辑式页面，不适合紧凑工作台，也让长中文标题更易换行和挤压操作区。
- 现有 `--muted #716e63` 在 `--paper #f3f0e8` 上计算约为 4.48:1，低于普通文字 4.5:1 门槛；另一个常用 `#74766c` 在同背景上约 4.05:1。小号元数据大量使用这两类颜色，见 `web/src/styles.css:559-563`、`web/src/styles.css:594-600`。
- 全部 CSS 合计出现 338 个字面颜色，去重后 168 个；根级语义 token 只有 8 个，Graph、Candidate、Timeline 又分别创建局部 token 岛。颜色替换若按字面值操作，很容易漏改或改变业务语义。

#### 3.2 Shared components and recurring patterns

- `Button` 已通过 CVA 固定 primary/secondary/ghost/danger 与 sm/md，是正确的收敛入口，见 `web/src/shared/ui.tsx:13-15`。`Badge`、`Card`、`CardHeader` 和状态组件也已集中，见 `web/src/shared/ui.tsx:16-21`。
- Dialog/Sheet 基于 Radix，显式处理关闭后的焦点恢复，见 `web/src/shared/ui.tsx:22-23`；Tabs、Tooltip、Dropdown 也复用 Radix，见 `web/src/shared/ui.tsx:25-35`。这些行为契约应保留，只换 token 和布局。
- `CardHeader` 把 eyebrow 作为一等属性，`EmptyState` 还硬编码“暂无记录” eyebrow，见 `web/src/shared/ui.tsx:18-19`。仓库中 `className="eyebrow"` 或 `eyebrow=` 共 98 处，Card/CardHeader 使用共 97 处。这是“文字多、节奏重复、页面卡片化”的主要来源。
- 全局基础按钮是 999px pill，而共享按钮是 7px、Graph 是 4px，见 `web/src/styles.css:34-44`、`web/src/styles.css:146-151`、`web/src/features/graph/graph.css:25-33`。全仓 radius 同时存在 0/1/2/4/5/6/7/8/10/12/14/50%/999px，缺少形状规则。
- 现有可保留优点：普通控件大多至少 44px，见 `web/src/styles.css:34-38`、`web/src/styles.css:60-71`；键盘焦点是 3px 蓝色 outline，见 `web/src/styles.css:50-53`、`web/src/styles.css:78-85`；状态通常同时有文字、图标或形状，而非只靠颜色。

#### 3.3 AppShell

- 桌面为 248px rail + 主栏，见 `web/src/styles.css:89-90`；导航按工作台、资料与知识、审阅与产出、学习与研究、系统分组，见 `web/src/app/AppShell.tsx:23-67`。未连接 Workspace 时只显示允许分组，见 `web/src/app/AppShell.tsx:80-84`，这是必须保留的产品契约。
- 920px 以下隐藏 rail 内容并显示移动菜单，见 `web/src/styles.css:426`；移动导航使用 Radix Sheet，且导航后自动关闭，见 `web/src/app/AppShell.tsx:182-186`。
- 顶部栏当前同时显示 breadcrumb、大号 H1、认证徽标、Workspace 短 ID、状态点和快捷菜单，见 `web/src/app/AppShell.tsx:187`。390px 下它被改为纵向堆叠，见 `web/src/styles.css:427`，功能完整但 chrome 文字过多、首屏纵向占用偏大。
- rail 底部仍显示 `M9 · 业务操作台`，品牌同时显示“知序”和 `ZHIXU / WORKBENCH`，见 `web/src/app/AppShell.tsx:182-184`。这些是最先可移除的装饰性文字，不影响导航语义。

#### 3.4 Feature CSS

- Graph 已经最接近目标方向：白色 surface、冷灰 field、蓝/绿节点、红色 stale、黄 warning，见 `web/src/features/graph/graph.css:1-13`。应保留 Topic 圆形、Claim 方形、stale 虚线和列表 fallback，而不是把所有状态统一成蓝色，见 `web/src/features/graph/graph.css:422-445`、`web/src/features/graph/graph.css:666-680`。
- Semantic Link 用独立 candidate amber，并通过 dashed 左边框、状态文字和 Candidate 文案与正式 Relation 分离，见 `web/src/features/graph/semantic-link.css:1-7`、`web/src/features/graph/semantic-link.css:224-233`。视觉升级只能把颜色映射到统一 warning token，不能抹掉候选/正式边界。
- Timeline 有自己的 surface/line，但标题仍回退 Georgia，并继续依赖全局 `--muted/--brass/--red/--green`，见 `web/src/features/timeline/timeline.css:1-8`、`web/src/features/timeline/timeline.css:35-50`、`web/src/features/timeline/timeline.css:302-342`。
- Model Settings 几乎完全依赖全局 token；desired/active 双栏和版本四格是有效信息结构，见 `web/src/features/settings/model-settings.css:34-50`、`web/src/features/settings/model-settings.css:121-137`。移动 520px 下已转单列并让保存按钮全宽，见 `web/src/features/settings/model-settings.css:348-381`。
- `styles.css` 后半段仍承载 Workspace、RAG、Export、Review、Interview 等 feature 规则，见 `web/src/styles.css:631-1089`。视觉系统落地前，不能假设“feature CSS”只有四个独立文件。

#### 3.5 Responsive contract and test coverage

- 当前断点集合为 420、520、540、620、720、800、860、900、920、1080、1100px，共 17 个 `@media` 块。相邻组件在几乎相同宽度切换，造成 840-920px 等区间难以推理。
- Graph 的 1080px 不只是 CSS：React `matchMedia` 常量也硬编码同值，见 `web/src/features/graph/GraphPage.tsx:39-49`；CSS drawer 切换见 `web/src/features/graph/graph.css:978-1013`，单测 stub 也依赖精确字符串，见 `web/src/features/graph/GraphPage.test.tsx:198-213`。修改断点必须三处同步。
- 现有 390x844 E2E 的强项是检查真实业务链路、document/workbench 横向溢出与 console/pageerror。例如 Graph 的几何检查见 `web/e2e/semantic-link-graph.smoke.spec.ts:62-93`，390px 主链路见 `web/e2e/semantic-link-graph.smoke.spec.ts:164-188`；Artifact 的 overflow 与 390px 检查见 `web/e2e/artifact.smoke.spec.ts:44-55`、`web/e2e/artifact.smoke.spec.ts:312-325`。
- `AppShell.test.tsx` 只覆盖桌面语义与未连接导航，见 `web/src/app/AppShell.test.tsx:28-95`；仓库没有测试打开“工作台导航”Sheet、Escape 关闭、触发点焦点恢复或 390px 下 rail/topbar 可见性。
- `ModelSettingsPanel.test.tsx` 没有 viewport/overflow 响应断言，`web/e2e/` 也没有 Settings 浏览器测试，尽管 `.trellis/spec/frontend/model-settings.md:62-63` 明确要求 1440x900 与 390x844 门禁。
- 现有 smoke 没有截图基线或 overlap/text-clipping 断言；“无 document overflow”不能证明标题、徽标、按钮之间不重叠。

### 4. High-risk coupling

| Risk | Evidence | Consequence | Recommendation |
| --- | --- | --- | --- |
| Wrong task boundary | 当前任务 PRD 明确把导航、Settings IA 和全局视觉重设计列为另一个任务，见 `.trellis/tasks/07-31-docker-host-workspace-path/prd.md:45-52`。 | 若直接把本研究变成当前任务实现，会违反已评审 scope。 | 将本文作为规划输入；实现前由主会话确认目标任务并把研究加入正确 context manifest。 |
| Global element cascade | `button/input/h1` 是裸元素规则，见 `web/src/styles.css:34-85`、`web/src/styles.css:445-459`；Graph 被迫大量覆盖且全 CSS 有 57 个 `!important`。 | 改一个基础属性可同时破坏 Graph node、dialog close、RAG link-button 和普通表单。 | 先把基础视觉收敛到 `.ui-*` primitive，裸元素只保留 reset、字体继承与 box sizing。 |
| `--brass` dual semantics | 同一 token 同时用于 rail/tab active，见 `web/src/styles.css:107`、`web/src/styles.css:179`，也用于 warning badge/state，见 `web/src/styles.css:155`、`web/src/styles.css:162`。 | 直接把 `--brass` 改为蓝会让 warning 变蓝；保留 amber 又无法得到蓝色 selection。 | 先拆成 `--color-accent` 与 `--color-warning-*`，逐语义迁移，再删除 `--brass`。 |
| Duplicate global selectors | `.table-scroll` 在 `web/src/styles.css:199` 与 `web/src/styles.css:872-876` 重复；`.form-error` 在 `web/src/styles.css:288` 与 `web/src/styles.css:623` 重复。 | 后段声明静默改变所有早期消费者，局部页面难以预测。 | 合并为单一 shared primitive；feature 差异使用命名 modifier。 |
| Graph width depends on shell gutter | `--workbench-content-gutter` 在 `web/src/styles.css:130` 定义；Graph 用该值做宽度补偿和负 margin，见 `web/src/features/graph/graph.css:15-23`。 | 调整 shell rail/gutter 时可造成 document overflow 或 Graph 偏移。 | 在改 gutter 同一批次验证 Graph 1440/1080/720/390 几何；禁止换成 `100vw`。 |
| Breakpoint drift | CSS 有 11 个不同宽度；Graph 1080 同时存在 TS/CSS/Test。 | 某一层更新后 drawer 语义与视觉状态不一致。 | 只保留 1080/920/720/480 四档；Graph 1080 作为契约常量集中说明并同步测试。 |
| Heading and eyebrow blast radius | `.eyebrow`/`CardHeader` 广泛使用；全局 h1/h2 与 Georgia 规则穿透 feature。 | 一次“去衬线/少文字”改动会影响近百调用点和可访问标题层级。 | 先保持 DOM heading，不以删除标题实现少文字；让 eyebrow 默认缺省，再按页面逐批清理。 |
| Mobile test gap | 现有 E2E 验证内容和 overflow，但没有 AppShell Sheet/Settings 390px 闭环。 | 视觉改造可能通过现有测试却出现菜单不可用、焦点丢失、按钮遮挡。 | 把 AppShell Sheet 与 Settings 加入 390x844 browser gate，并增加 bounding box/可见性断言。 |

### 5. Recommended visual system

#### 5.1 Color tokens

核心只使用冷灰 + 单一产品蓝。以下值在白色表面上的普通文字/主按钮对比度已通过本地计算；最终实现仍应在真实渲染下跑 axe/浏览器检查。

| Token | Value | Use |
| --- | --- | --- |
| `--color-canvas` | `#F7F9FC` | 应用画布、rail 背景 |
| `--color-surface` | `#FFFFFF` | 主内容、card、dialog、input |
| `--color-surface-subtle` | `#F1F5F9` | hover、分组、只读区 |
| `--color-surface-selected` | `#EAF2FF` | active nav、selected row、tab active |
| `--color-text` | `#172033` | 主文字；在 canvas 上约 15.4:1 |
| `--color-text-secondary` | `#526075` | 说明文字；在白色上约 6.4:1 |
| `--color-text-muted` | `#64748B` | 元数据；在白色上约 4.8:1，12px 以下不要继续变浅 |
| `--color-border` | `#D8E1EC` | 非关键分隔线 |
| `--color-border-strong` | `#7C8DA5` | 输入/可交互边界；在白色上约 3.4:1 |
| `--color-accent` | `#2563EB` | primary、active、link、focus；白字其上约 5.2:1 |
| `--color-accent-hover` | `#1D4ED8` | hover/pressed；白字其上约 6.7:1 |
| `--color-focus` | `#2563EB` | 2px focus ring + 2px offset |

状态 token 不参与品牌层级：

| State | Text / icon | Surface | Border | Required secondary cue |
| --- | --- | --- | --- | --- |
| success | `#166534` | `#ECFDF3` | `#A7E5C1` | Check icon + 明确状态文字 |
| warning | `#8A4B0F` | `#FFF7E6` | `#F2C572` | Triangle icon + 明确状态文字 |
| danger | `#B42318` | `#FFF1F0` | `#FDA29B` | Alert icon + 错误影响/恢复动作 |
| info | `#1D4ED8` | `#EEF4FF` | `#B9D1FF` | Info icon + 文字 |
| neutral | `#475569` | `#F1F5F9` | `#CBD5E1` | 文字或 shape |

Graph domain 继续拥有语义 token，但基于全局表面：Topic `#0F766E` + circle，Claim `#2563EB` + square，Candidate 使用 warning amber + dashed，Stale 使用 danger + dashed。不要用产品蓝统一 Topic/Claim/Candidate/Stale。

迁移时临时保留 `--paper/--ink/--muted/--line/--green/--red` alias；`--brass` 不做一对一 alias，必须先按 selection 与 warning 分拆。

#### 5.2 Typography

不新增字体依赖，不发起网络字体请求：

```css
--font-ui: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", "Noto Sans CJK SC", sans-serif;
--font-mono: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
```

- Brand、shell、标题、正文统一 sans；不再用 Georgia。品牌通过字重与布局识别，不通过跨平台不稳定的 serif fallback。
- 字距统一 `0`；不对中文或小标签使用负字距/大写宽 tracking。
- 桌面：page title 28/36、shell title 22/28、section title 18/26、card title 15/22、body 14/22、control 13/18、meta 12/18。
- 390px：page title 24/32，其余保持；不按 viewport 连续缩放字号。
- `font-weight` 主体 400/500，标题 600/650，选中/关键操作 600；避免当前大量 750/780/850 的伪字重插值。
- Mono 只用于 ID、hash、路径和代码；计数/时间优先 `font-variant-numeric: tabular-nums`，不把整块业务文字变成 monospace。

#### 5.3 Spacing and density

采用 4px 基线：`4, 8, 12, 16, 20, 24, 32, 40, 48`。不再混用大量 `.35/.45/.55/.65/.7/.75/.8/.85/.9rem` 微差。

- Desktop shell rail 232px，topbar 56px，内容 gutter 24-32px，页面最大内容宽度 1440px；Graph 可在主栏内 full bleed，但不跨 rail。
- 普通 row 48px，双行信息 row 56-64px；card padding 16px；section gap 24px；page stack 24px。
- Desktop control 36px；紧凑 icon control 32px 且周围有足够间距；touch/coarse pointer 和 390px 统一 44px。
- Field 内 label-control gap 6px，field group gap 12px，form section gap 24px。
- 信息密度通过紧凑行和清晰列对齐获得，不靠 10px 字体、低对比文字或每项套 card。

#### 5.4 Borders, radius and elevation

- `--radius-control: 6px`、`--radius-surface: 8px`、`--radius-overlay: 8px`、`--radius-pill: 999px`。
- Pill 只用于 Badge/Status/单选 chip，不用于所有按钮。
- 页面 section 不做浮卡；重复独立对象才用 Card。Card 为 1px border + white surface，无默认 shadow。
- 只有 Dialog/Dropdown/Sheet 可用 elevation：`0 12px 32px rgb(15 23 42 / 12%)`；drawer 可用定向 shadow。
- 选中态使用蓝色边/底色，不用 glow、gradient 或纯装饰 shadow。

#### 5.5 Component contracts

**Button**

- Primary：blue fill + white text；Secondary：white + strong border；Ghost：transparent；Danger：red fill。
- Desktop 36px，390px 44px；文本单行。图标命令优先 Lucide，熟悉图标可 icon-only，但必须有 `aria-label` 和 Tooltip。
- hover 只改变 surface/border；active 可 `translateY(1px)`；disabled 使用 `not-allowed`，不能统一显示 `wait`。Pending 才显示 progress cursor/文字。

**Form / Field**

- Label 永远在控件上方；helper 在下方；error 在同一 field 下方并用 `aria-describedby`。
- Input white surface、strong border、6px radius、36/44px height；focus 使用 2px 蓝 ring，不只改 border 色。
- Checkbox/toggle 表达二元设置；radio/segmented control 表达少量互斥模式；select 表达较长枚举。不要用文字按钮模拟开关。
- Placeholder 不是 label，颜色不低于 `--color-text-muted`。

**Card / Section**

- `CardHeader` 的 eyebrow 改为真正可选且默认不渲染。正常结构只保留 title、最多一行 description、一个 action。
- Empty/Loading/Error 不再默认加 eyebrow；状态标题、影响和一个下一步即可。
- 不允许 card 内再套视觉 card；内部用 section、divider 或 `SettingRow`。

**SettingRow**

- Desktop 使用 `grid-template-columns: minmax(0, 1fr) minmax(16rem, 24rem)`；左侧为名称 + 最多一行说明，右侧为 control/status/action。
- 行 padding 12px 0，组间使用一个 divider。密钥、冲突、restart 等安全状态仍可展开成 inline state，不塞进小 badge。
- 720px 以下单列；390px control 100% 宽、action 不遮挡错误文字。现有 desired/active 对照和 revision 四格继续保留，但去掉重复 `Desired/Active` eyebrow。

**Badge / Status / InlineState**

- Badge 只承载短状态，不承载句子；必须有文字，重要状态加 icon/shape。
- InlineState 使用 3px 左边界、浅色 surface、icon、标题、说明和可选 action；Error 必须说明影响与恢复，不用 toast 代替持久错误。

**Tabs / Segmented control**

- Settings 顶层分类继续用 Tabs；active 用蓝色 2px underline，不做 pill row。
- Graph Global/Local/Path、Card/List 等 2-4 个模式用稳定宽度 segmented control；selected 用蓝 fill 或 selected surface。
- 390px Tabs 可水平滚动，但当前项必须完整可见；不要缩小文字到不可读。

**Dialog / Sheet / Dropdown**

- 保留 Radix 行为与现有 focus restore；视觉统一为 8px overlay surface、明确 close icon、无嵌套 card。
- 390px navigation Sheet 宽 `min(22rem, calc(100vw - 24px))`，底部 drawer 使用 `100dvh` 与 safe-area padding。

#### 5.6 Motion

- Token：fast 120ms、normal 160ms、overlay 220ms；ease `cubic-bezier(.2, 0, 0, 1)`。
- 控件只过渡 color/background/border/opacity；overlay 只用 opacity/transform。禁止 `transition: all`。
- Skeleton shimmer、spinner 只表达真实 pending；`prefers-reduced-motion: reduce` 下变静态，不用 0.01ms 循环模拟。
- 不增加滚动动画、parallax、glow 或 perpetual decoration。当前 global reduced-motion block，见 `web/src/styles.css:1208-1224`，应保留并改为 token 化规则。

#### 5.7 Desktop and 390px layout

| Width | Shell | Content |
| --- | --- | --- |
| `>1080` | 232px fixed/sticky rail，56px topbar | Graph 三栏；普通页 1-3 列按组件需要 |
| `921-1080` | rail 保留 | Graph detail 进入 modal drawer，保持现有焦点契约 |
| `721-920` | rail 折叠为单一 sticky app bar + Sheet | 两列 summary 可保留，其余优先单列 |
| `<=720` | 只保留品牌、页面名、menu；认证/Workspace 详情移入 Sheet/overflow | 页面单列，16px gutter，table 在自身 wrapper 内滚动 |
| `<=480`，含 390px | 44px touch target，Sheet/Drawer 接近全宽，safe area | setting/action/score grid 单列；无 document 横向 overflow |

390px 具体收敛：

- 不同时展示 `ZHIXU / WORKBENCH`、认证完整文案、Workspace 短 ID、context dot 和第二个快捷菜单。Header 首屏只保留“知序”、当前页名、主导航按钮；详细状态进入 Sheet 或 overflow。
- Breadcrumb 单行省略，详情页可显示“父级 / 当前”两段；页面 H1 24px，不再出现 2.2rem 以上 shell heading。
- 内容 gutter 16px，card/section gap 12-16px。所有长 ID、路径和错误允许 `overflow-wrap:anywhere`。
- 真实 data table 保留内部横向滚动；document 与 `.workbench__content` 仍必须无横向 overflow。
- Graph 保留 1080px modal detail 和完整列表 fallback；390px 不缩小桌面 detail 到侧栏宽度。

### 6. Recommended migration order

1. 先确认正确 Trellis 任务边界；本研究不应让当前 Docker Workspace PRD 自动扩 scope。
2. 在根级建立 semantic color/type/space/radius/motion token，并写 temporary legacy alias。先拆 `--brass` 双语义。
3. 收敛 `shared/ui.tsx` primitive 和对应 CSS；裸元素规则降为 reset，去除对 feature button/input/h1 的隐式控制。
4. 重做 AppShell chrome，保持导航分组、未连接裁剪、root active、Sheet 和同步状态行为不变。
5. 迁移全局业务样式中的 Card/Field/State/Table/SettingRow，再迁 Timeline 与 Model Settings。
6. 最后迁 Graph/Semantic Link，只映射 surface/type/status token；保留 1080px TS/CSS/Test 同步、Domain shape 与 gutter 公式。
7. 每批执行 lint/typecheck/component test；最终跑真实 1440x900、1080px、720px、390x844 browser gate，并审查截图、overlap、focus、console、network、document overflow。

### 7. Test additions required for the redesign

- `AppShell.test.tsx`：打开移动 Sheet、导航后关闭、Escape 关闭、焦点回到 menu trigger；未连接/已连接两种导航集合。
- Browser 1440/390：desktop rail/mobile menu 的互斥可见性；topbar 元素 bounding boxes 不相交；Sheet 中 active route、connection、sign-out 可访问。
- Settings 1440/390：七个 tab 可达，Chat/Embedding desired/active 文本不裁切，长 Provider/Model/Error 可换行，保存/测试按钮尺寸稳定，document 无 overflow。
- Shared primitives：Button 四种 tone、focus-visible、disabled/pending cursor 区分；Badge/InlineState 不只靠颜色；Field label/error association。
- Graph：继续保留现有 1080 compact drawer 单测和 390 E2E；改变 gutter/token 后重跑几何断言。
- 视觉验收至少保存 AppShell、Settings、Graph 三个代表页面的 1440x900 与 390x844 截图供人工 diff；仅有 overflow 断言不足以发现文字重叠。

## External references

- WCAG 2.2 Target Size Minimum：最低 24x24 CSS px；本方案保留 desktop 32/36px 且用间距满足最低要求，touch 使用 44px。https://www.w3.org/WAI/WCAG22/Understanding/target-size-minimum
- WCAG 2.2 Contrast Minimum：普通文字 4.5:1，大文字 3:1，placeholder 也在范围内。https://www.w3.org/WAI/WCAG22/Understanding/contrast-minimum
- WCAG 2.2 Non-text Contrast：必要的控件边界和 focus indicator 目标至少 3:1。https://www.w3.org/WAI/WCAG22/Understanding/non-text-contrast.html
- Radix Dialog：modal 自动 focus trap，Escape 关闭并回到 Trigger；现有 wrapper 的行为应保留。https://www.radix-ui.com/primitives/docs/components/dialog
- 当前依赖版本来自 `web/package.json:18-32`：Radix Dialog 1.1.20、Dropdown 2.1.21、Tabs 1.1.18、Tooltip 1.2.13、CVA 0.7.1、Lucide 1.25.0、React 19.2.7。推荐方案不新增依赖。

## Related specs

- `.trellis/spec/frontend/component-guidelines.md:16-22`：shared/feature component ownership 与语义 props。
- `.trellis/spec/frontend/component-guidelines.md:32-48`：状态不可只靠颜色、键盘、焦点、Dialog/Drawer、Loading/Error 等要求。
- `.trellis/spec/frontend/component-guidelines.md:181-208`：AppShell 未连接导航、root active、同步状态和 1440/390 移动 Sheet browser contract。
- `.trellis/spec/frontend/quality-guidelines.md:85-152`：Graph 1080 compact drawer、完整列表 fallback、焦点恢复和 1440/390 gate。
- `.trellis/spec/frontend/quality-guidelines.md:221-246`：Semantic Candidate 与 formal Relation 分离，以及 Graph 宽度必须相对 workbench main 计算。
- `.trellis/spec/frontend/model-settings.md:20-31`：desired/active/applied、Secret、状态与 390px 长文本契约。
- `.trellis/spec/frontend/model-settings.md:54-75`：Settings component/browser 测试与移动单列要求。

## Caveats / Not Found

- 本研究最初由 Docker 宿主机 Workspace 路径任务 dispatch；主会话发现 scope mismatch 后，已将研究归档到 `07-31-workbench-navigation-visual-redesign/research/`。它只为视觉重设计任务提供规划证据，不构成 Docker 路径任务的实现授权。
- 本次处于规划阶段，未修改产品代码、未启动开发服务、未执行浏览器截图或运行测试；视觉判断来自源码、测试和规范静态审计。
- 未发现专门的 design token 文件、Storybook、视觉回归快照、AppShell 移动 Sheet E2E 或 Model Settings E2E。
- 颜色对比度为 sRGB token 对的本地计算；透明 surface、实际字体抗锯齿、hover/focus 组合仍需浏览器验证。
- CSS 字面颜色/eyebrow/Card/`!important` 数量是 2026-07-31 对当前工作树的 `rg` 审计快照，后续并行修改可能改变计数。
