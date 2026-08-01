# 知序首页设计提案

## Design Thesis

首页由 Active Workspace 是否存在切换为两个明确状态，但视觉上属于同一个产品：

- **已有 Workspace：安静的今日知识桌面。** 恢复真实工作上下文，先处理一件最重要的事。
- **未连接 Workspace：沉浸式知识地图入口。** 用产品对象关系解释知序，并完成唯一动作“连接知识目录”。

两页统一采用用户确认的概念 B：严格无衬线、冷白画布、深墨文字、克制钴蓝、细线网格。它们的差异来自信息架构与空间构图，不来自简单换字体或换色。

## Canonical Page States

| 状态 | 判定条件 | 页面任务 | 视觉重心 |
| --- | --- | --- | --- |
| 入口态 | 没有 Active Workspace | 建立品牌理解并连接知识目录 | 全画布知识关系模型 |
| 工作态 | 已有 Active Workspace | 恢复上下文并开始今天的知识工作 | 单一下一步与连续工作面 |

这两个状态由 Active Workspace 是否存在直接决定。工作态之前不增加欢迎封面、启动动画或额外确认步骤。

## State A: 安静的今日知识桌面

工作态不是欢迎页，也不是统计仪表盘。首屏按工作节奏从上到下排列：

1. 日期、当前 Workspace 和视图切换，建立“我现在在哪”的上下文。
2. “先处理这一件”只呈现一个等待人工的 Workflow、失败 Workflow 或待审 Proposal，并提供一个主操作。该结果来自明确的首页展示规则，不冒充服务端全局优先级。
3. “继续最近的资料线索”承接最新 Source Version，以安全、解析和索引状态组成真实处理脉络。
4. “最近捕获”展示其余 Source Version，明确使用服务端捕获时间，不冒充访问历史。
5. 系统正常时只保留一句不争夺注意力的本地可用说明；阻断性故障才替换成可操作异常条。

页面使用完整应用侧栏和顶部搜索，说明用户已经进入工作状态。内容通过分隔线与稳定列宽组织，不堆叠 KPI 卡片，也不制造虚假数字。

效果稿中的资料摘要、主题归并和“最近进入”没有现有 API 契约支撑，产品实现必须以文件名、相对路径、捕获时间和 Source Version 状态替代。小标题统一改为中文，不保留 `PROPOSAL / SOURCE VERSION` 等装饰性英文标签。

## State B: 沉浸式知识地图入口

入口态采用用户选定的沉浸式知识地图方向，取消旧稿的左右分栏，知识地图直接成为整个主画布：

- 主标题以“知序”为第一品牌信号，价值文案与“连接知识目录”放在左上留白区，不装进卡片。
- 画布只展示 `本地资料 → 知识主题 → 证据关系 → 待审提案`，使用通用对象名，不出现文档数量、真实文件名或伪造的用户业务事实。
- 四个中文节点由一条蓝色主脉络连接；允许一条极淡的跨节点补充关系，使画面仍像知识关系而不是进度曲线。
- 不显示完整领域模型、聚类外圈、次级节点、关系动词、英文节点类型、图例、均匀网格或矩形节点卡片。
- 沉浸感来自非对称构图、节点尺度与大面积留白，不来自图形复杂度。
- 侧栏折叠为图标轨，保留产品连续性，同时把最大空间交给知识图。
- 次级动作仅解释数据边界，不与连接操作竞争。

该关系图是未连接状态的品牌化对象模型，不读取 Workspace 数据；一旦存在 Active Workspace，页面直接切换到工作态，真实关系由知识图谱页承载。

入口态复用 Clearline 的同一导航数据与 Token，只在 `/dashboard` 且没有 Active Workspace 时切换为紧凑图标轨和入口 Topbar；不会复制第二套路由或导航清单。桌面和 `390x844` 均保留该紧凑轨，连接后立即恢复完整工作台壳。

### 确认方案：极简知识脉络

`workbench-map-b-minimal`（历史稿对照中的 `2：Minimal map`）是当前确认的入口方案。它以四个中文节点、一条蓝色主脉络和一条极淡补充关系建立品牌表达，保持低噪声、大留白与清晰视觉重心。知识星座增强稿已被否决，不进入实现候选。

## Shared Visual System

### Color

- 主背景：`#FFFFFF` / `#FBFCFE`，导航面使用 `#F5F7FB`。
- 主文字：`#151923`；辅助文字：`#687184`。
- 主操作与选中态：`#1F5EFF`，仅用于行动、当前焦点和主关系链。
- 语义色：绿色表示本地可用，琥珀表示等待决定；不使用紫蓝渐变或大面积单色填充。

### Typography

- 品牌、标题、正文、数据全部使用清晰的中文无衬线体系。
- 工作态标题保持应用尺度，入口态仅“知序”使用品牌尺度。
- 字号按断点固定切换，不随视口连续缩放；字距保持 `0`。

### Surface and Composition

- 工作态是连续工作面；卡片只留给真正重复的业务实体。
- 入口态是无外框、全宽关系画布；节点标签属于图谱对象，不是装饰卡片。
- 所有固定控件、节点和侧栏具有稳定尺寸，避免动态内容导致布局位移。

### Motion

- 首次出现只做一次 `300–500ms` 的内容与节点淡入。
- 不使用持续漂浮、呼吸或无业务含义的粒子动画。
- `prefers-reduced-motion` 下直接呈现最终状态。

## Responsive Behavior

- 桌面以 `1440x900` 为设计基准。
- `390x844` 下，工作态遵循 Clearline 的移动导航与 Sheet，不为 Dashboard 复制第二套业务导航；“先处理这一件”仍保持在首屏内，双栏内容改为单列。
- `390x844` 下，入口态保留品牌、价值文案和主操作在首屏，关系节点向下重排，画布工具隐藏，主关系仍可辨认。

## Visual Artifacts

- [`research/visual-concepts/workbench-desk-b.png`](./research/visual-concepts/workbench-desk-b.png)：已有 Workspace 的“安静的今日知识桌面”，`1440x900`。
- [`research/visual-concepts/workbench-map-b-minimal.png`](./research/visual-concepts/workbench-map-b-minimal.png)：未连接 Workspace 的“极简知识脉络入口”确认稿，`1440x900`。
- [`research/visual-concepts/workbench-desk-b-mobile.png`](./research/visual-concepts/workbench-desk-b-mobile.png)：工作态移动版检查。
- [`research/visual-concepts/workbench-map-b-minimal-mobile.png`](./research/visual-concepts/workbench-map-b-minimal-mobile.png)：入口态移动版检查，使用单独的四节点构图而不是裁切桌面图。
- `workbench-desk-b.html`、`workbench-map-b-minimal.html` 与 `workbench-b.css` 是可重复渲染的设计源稿，不属于产品实现。

旧的 `entry-editorial` / `entry-strict` 只验证了字体与表面差异；`workbench-map-b` 与 `workbench-map-b-refined` 仍承担过多模型解释。它们均已被极简候选稿取代。

## System Failure Treatment

系统正常时不显示“系统就绪”。只有当前动作受阻时，在操作附近出现一条横向提示：

```text
知识查询暂不可用。已有本地资料仍可浏览。  [重新检查] [查看设置]
```

提示必须同时说明受影响动作与仍可执行的动作，不能把整页切回诊断控制台。
