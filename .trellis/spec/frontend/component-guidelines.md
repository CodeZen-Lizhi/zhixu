# 前端组件规范

> 定义组件职责、组合方式和可访问性约束。

## 适用范围

适用于现有 React 组件和路由级 UI；具体实现以 `web/src` 和相应行为测试为准。

## Route Content 恢复边界

- `web/src/routes/RouteContentBoundary.tsx` 只包裹 AppShell 内容区内的 Suspense/Outlet；导航、认证与 Workspace Provider 留在边界外。
- render throw 与 lazy rejection 显示同一稳定恢复状态，按钮可重试或显式刷新；不自动无限刷新，不把错误正文/stack 放入 UI。
- location key 变化只清除错误，健康页面的 URL 筛选变化不得无故重挂、丢失本地草稿；Workspace 变化则必须重建该内容边界。
- React 19 的默认 caught-error reporter 会记录原始 Error，单靠不实现 `componentDidCatch` 不能防止泄漏。`main.tsx` 通过 root `onCaughtError` 对该边界仅记录固定 `ROUTE_CONTENT_ERROR`。
- 恢复状态使用文本、可操作按钮与可访问焦点；业务 API 错误仍由页面处理，不交给此渲染边界。

## 已确认事实

- `docs/architecture/application-contracts.md` 要求使用 React + TypeScript，业务状态不得依赖 UI 组件库。
- Diff、Evidence、Status、Graph、Table、Workflow 和 Review 是核心产品界面。
- `docs/requirements.md` 要求关键操作支持键盘、焦点清晰、状态同时使用文字和视觉标识、图谱不只靠颜色区分、Diff 为屏幕阅读器提供文本说明。
- Error Boundary 仅处理渲染错误；业务错误必须在页面展示错误码、影响和下一步操作。

## 组件职责

- Route Component 负责页面组合并连接 Feature Query、Command 和 URL State。
- Feature Component 展示产品行为并使用 Feature Hook，不解码原始 API 或 SSE Payload。
- Shared Component 保持展示性和领域无关，以语义化 Props 代替完整 Transport Object。
- Domain Display Projection 在组件外生成，避免 Table、Card 和 Detail 重复解释同一业务状态。
- 优先使用小型语义组件和组合，避免用大量布尔 Props 承载无关模式。

## Props 与事件

- 本地 Props Type 与组件共置；只有存在真实公共组合边界时才导出。
- Props 表达 UI 意图，例如状态展示或 Evidence Reference，不直接暴露完整生成 DTO。
- 事件 Props 使用 `onApprove`、`onRetry` 等用户动作命名；只有基础组件才暴露 DOM 实现细节。
- 禁止修改 Props。派生值在渲染期或命名 Projection 中计算，不同步复制到 State。
- 破坏性、审批和写回操作必须表达 Pending、Success、Failure、Disabled Reason 和重复提交行为。

## 样式与视觉语义

- 当前未选择 UI 库和样式系统；M1 必须先记录并锁定方案，之后才能添加示例。
- 业务状态不得依赖组件库的状态或类型。
- Status、Risk、Graph Relation 和 Severity 不得只依赖颜色，应结合文字、形状、图标、线型或标签。
- 大表使用虚拟滚动，图谱增量渲染，大 Diff 分段加载，遵守 `docs/architecture/quality.md`。

## 可访问性

- 优先使用原生语义元素，避免无必要地用 ARIA 重建。
- 所有关键操作可用键盘完成，并有可见焦点。
- Dialog、Drawer、Menu 和错误恢复流程打开时管理焦点，关闭后恢复焦点。
- 纯图标控件必须有可访问名称；表单字段必须关联 Label 和错误说明。
- 需要用户注意的异步变化使用合适的 Live Region，但不逐 Token 播报流式内容。
- Diff 为屏幕阅读器提供文本变化说明，不只展示插入/删除颜色。
- Graph 必须提供非 Canvas 或其他键盘可访问方式，用于检查节点、关系和证据。
- Loading、Empty、Error、Degraded、Waiting for Human、Manual Recovery 状态必须可区分且可操作。

## 禁止模式

- 在 JSX 事件处理器中实现业务规则或 Workflow 状态转换。
- 架构要求服务端持久化时，只在组件内存保存 Proposal Revision 或 Approval Draft。
- 用 Error Boundary 捕获业务错误并显示通用成功或空页面。
- 不安全 HTML、可执行 Markdown 或未清理的外部内容。
- 用户必须解除前置条件时，只禁用按钮而不解释原因。
- 仅点击交互、移除焦点但不替代、仅颜色表达状态。
- 组件接收 `any`、原始 `unknown` 或未校验 SSE/API Envelope。

## 验证

M1 前执行：

```bash
rg -n 'Accessibility|键盘|颜色|焦点|屏幕阅读器|Error Boundary' docs/architecture/application-contracts.md docs/requirements.md
git diff --check
```

M1 后，Component Test 必须覆盖语义 Role、键盘、焦点、Loading/Error/Empty 以及 Diff/Evidence/Status；浏览器测试覆盖高风险审批和恢复流程。

## M1 待代码验证

M1 必须确定样式与组件库策略、测试渲染器、可访问性工具和代表性组件模式；只有 Manifest 和源码存在后才添加真实代码链接。

## M6-04 RAG 页面模式

- `/chat` 与 `/chat/:conversationId` 使用“会话栏—回答工作区—证据栏”研究台布局；移动端收敛单列，
  Citation 选择后以可聚焦 drawer 呈现，关闭后焦点返回触发按钮。
- Pending 只展示 Workflow/current_stage；Completed、Refusal、Clarification 必须穷尽分支，Clarification 不展示
  后端必然拒绝的 Feedback。
- Answer 正文按纯文本 `white-space: pre-wrap` 展示；Citation href 只使用服务端值，不执行 Markdown/HTML。
- Composer 支持 Enter 提交、Shift+Enter 换行，并显式提供 Scope、Depth、Format 与不可用能力说明。

## M7-01 Graph 页面模式

- `/graph` 是真实查询工作台，首版只呈现 Topic/Claim 与 canonical Relation，不提供创建、确认或修改 Relation
  的控件。Global/Local/Path 使用同一模式切换；Local 的 center、Path 的 from/to、depth/direction/filter 和
  当前 mode 由 URL State 持有，刷新和深链必须恢复为规范值。
- TanStack Query 只持有 Workspace-scoped Server State；节点锁定坐标、固定布局和画布/列表选择是会话内 Local
  State。切换到不同查询投影时清除旧锁定布局，选择节点不得隐式锁定或写回 Knowledge。
- `GraphCanvas` 最多绘制 60 个节点和 100 条边。超过上限、端点断裂、重复 key 或非法锁定坐标时必须强制
  显示完整列表 fallback，不能裁掉剩余事实或呈现空白画布；未超限时仍允许用户主动切换画布/列表。
- 节点用 Topic/Claim 的形状、标签和文字共同区分；Relation 用类型文字、状态文字和实线/虚线共同表达，
  不只依赖颜色。SVG edge 仅作视觉层，节点/边选择使用可聚焦 button，完整列表提供等价键盘入口。
- 选择节点或 Relation 后才查询详情；Relation Evidence 初始关闭，只有用户展开后才按 cursor 请求，并使用
  服务端 `span_href` 打开来源。无 Evidence、加载、失败和下一页必须分别可见，不得预取正文或自行拼接 href。
- 紧凑视口（当前断点 `max-width: 1080px`）的详情面板作为 modal drawer：背景 inert，焦点进入面板，Tab
  保持在 drawer 内，Escape/关闭按钮关闭后把焦点恢复到触发控件；桌面详情保持 complementary panel。
- Loading、Empty、Truncated、No Path、Cursor Stale、Timeout/Cancel 和 Layout Fallback 必须使用不同的可操作
  状态。共同 Topic 只能标为无路径建议，不能画成或描述为正式 path。
- Workspace selector、URL State 和 opaque cursor 只表达查询范围，不是身份或授权凭据；组件不得据此隐藏
  服务端授权失败或声称 Auth 已完成，正式 Session/CSRF/Capability 仍等待 M10。

## Scenario: M9 Monaco DiffEditor 模型生命周期

### 1. Scope / Trigger

- 使用 `@monaco-editor/react` 的 `DiffEditor` 展示 Workspace/Proposal 等路由绑定正文时应用。
- 目标是离页后释放显式 URI 模型，同时避免 Monaco 在 DiffEditor 仍绑定模型时收到提前 `dispose()`。

### 2. Signatures

```tsx
<DiffEditor
  key={`${workspaceId}:${proposalId}`}
  originalModelPath={`inmemory://zhixu/workspaces/${workspaceId}/proposals/${proposalId}/original.md`}
  modifiedModelPath={`inmemory://zhixu/workspaces/${workspaceId}/proposals/${proposalId}/modified.md`}
  keepCurrentOriginalModel
  keepCurrentModifiedModel
  onMount={releaseDetachedDiffModels}
/>
```

### 3. Contracts

- Model URI 必须绑定 Workspace 与资源 ID；路由身份变化必须通过同一身份组成 `key`，使旧 DiffEditor 完整卸载。
- `keepCurrentOriginalModel` 与 `keepCurrentModifiedModel` 必须同时开启，模型所有权由页面的卸载处理器接管。
- 监听内部 `getModifiedEditor().onDidDispose`，等待 DiffEditor 已解除模型绑定后，再在微任务中检查并释放模型。
- 只有 `!model.isDisposed() && !model.isAttachedToEditor()` 时才允许 `model.dispose()`；快速重挂载到同一 URI 的模型必须保留。

### 4. Validation & Error Matrix

| Condition | Required result |
|---|---|
| DiffEditor 正常离页，模型未再绑定 | 微任务后 original/modified 各释放一次 |
| 同 URI 在微任务前被新编辑器绑定 | 不释放任何仍 attached 的模型 |
| 模型已由其他 owner 释放 | 不重复调用 `dispose()` |
| Workspace 或 Proposal 路由身份变化 | 旧 editor 完整卸载，只保留新 URI 的两个模型 |

### 5. Good / Base / Bad Cases

- Good：连续进入/离开详情，Monaco registry 稳定为 `2 → 0`，console 没有模型提前释放异常。
- Base：页面尚未创建模型或 `editor.getModel()` 为空时直接返回。
- Bad：在 React effect cleanup 或 wrapper 开始卸载时立即释放模型；这会触发 `TextModel got disposed before DiffEditorWidget model got reset`。

### 6. Tests Required

- Component：断言 Workspace/Proposal URI、两个 keep-current 选项和资源身份 `key`。
- Lifecycle：触发内部 modified editor 的 `onDidDispose`，等待一个微任务后断言 detached 模型恰好释放一次。
- Remount：让 `isAttachedToEditor()` 返回 `true`，断言快速重挂载时模型不被提前释放。
- Browser：重复路由切换后检查 Monaco model registry 为 0，并确认 console error/warning 为 0。

### 7. Wrong vs Correct

```tsx
// Wrong: wrapper 卸载期间模型仍可能绑定在 DiffEditorWidget 上。
useEffect(() => () => model.dispose(), [model]);

// Correct: 等内部 editor 完成 detach，再释放没有被重新绑定的模型。
editor.getModifiedEditor().onDidDispose(() => {
  queueMicrotask(() => {
    if (!model.isDisposed() && !model.isAttachedToEditor()) model.dispose();
  });
});
```

## Scenario: Workspace 连接、入口工作台与 App Shell 导航

### 1. Scope / Trigger

- 修改 `AppShell`、入口 Dashboard、Workspace 状态页、设置分组、根路由 `/` 或 Active Workspace 启动/重连门禁时应用。

### 2. Signatures

```tsx
const knowledgeNavigationState = useKnowledgeNavigationState(location.pathname);
<Navigation workspaceConnected={workspaceId !== ""} currentPath={location.pathname} knowledgeState={knowledgeNavigationState} />
<SettingsPage /> // `?section=workspace|models|exports|access|system`
selectDashboardFocus({ waitingWorkflows, failedWorkflows, proposals })
```

### 3. Contracts

- `routeDisplayRegistry` 是导航分组、面包屑和路由归属的唯一来源；`AppShell` 不得复制一份手写菜单或按 URL 子串猜测所属分组。
- 无 Active Workspace 或切换重建期间，`/dashboard` 及其他业务 deep link 保留原 URL 并显示简短的启动/重连状态；
  服务恢复后重新读取 Active Workspace，再进入独立业务认证和既有工作台。状态页只保留一个紧凑标题、必要说明和重试动作，
  不展示旧 Workspace 事实、数据库 operation 细节或大段能力介绍。
- 已连接时一级导航固定为“工作台 / 知识 / 创作 / 设置”。知识必须使用由 registry 派生的内联三级 Disclosure：一级“知识”、二级“资料 / 探索 / 组织 / 学习”、三级具体路由；不得恢复覆盖正文的大型 Dropdown 或复制第二份菜单配置。“资料收件箱”是“资料”下的三级链接，不再由一级“知识”隐式直达。
- 知识一级与各二级分组使用原生按钮、Chevron、`aria-expanded` 和指向真实节点的 `aria-controls`；折叠分支保留 `hidden`，其中链接不可聚焦。`RouteNavigationGroup.icon` 是二级分组图标的唯一来源，同一分组各路由的 `label/order/icon` 必须一致；`AppShell` 不得按分组名称另建图标映射。二级按钮固定为“图标 / 标签 / Chevron”三列，三级标签相对二级标签至少额外缩进 `16px`，桌面 Rail 与移动 Sheet 使用同一规则。
- 当前知识路由必须同时以 `aria-current="true"` 标识一级和所属二级分组，并以 `aria-current="page"` 标识三级入口。选中视觉采用单一焦点：只有当前三级入口使用浅色背景与蓝色左边线；一级、二级祖先只用前景色、图标和字重表达当前路径，不得再叠加选中背景或蓝色边线。
- 知识展开状态由 `AppShell` 生命周期拥有，桌面 Rail 与移动 Sheet 复用同一状态；当前路由只追加展开所属分组，不关闭用户已展开的其他分组。关闭并重开移动 Sheet 不得重置非当前分组，选择三级链接后关闭 Sheet，Escape 关闭后恢复主导航按钮焦点。
- 桌面 Rail 高度必须受当前视口约束，超长知识树只滚动 `rail-section`，不得撑高整页；设置入口必须能在侧栏滚动区内到达。移动断点恢复自动高度，并在现有 Sheet 内展示同一层级。
- 创作直达 `/authoring`，不得恢复 Proposal、Workflow、Artifact 技术对象菜单。设置只保留一个入口，并通过 `section=workspace|models|exports|access|system` 选择内容；旧 `section=sync` 只做兼容归一化并保留其他 URL 参数。
- `/`、`/workspace` 是同一业务 Workspace 只读页面并归属设置，展示当前 Root 与本机切换命令；浏览器不得选择路径或触发
  Docker mutation。已有业务 deep link 保持原 URL，不能因 Active Workspace 短暂不可用重定向到 `/workspace` 或假成功页。
- System Status 只在设置的 `section=system` 使用 `SystemStatusPage display="full"` 展示。默认先回答能否继续工作；`unavailable|disabled` 项必须给出影响、原因和恢复入口，最近检查时间取 Query 的 `dataUpdatedAt`，全部运行事实默认折叠到技术详情。不得在入口 Dashboard 拼接能力矩阵、请求 ID 或运行摘要，也不得把 SSE 连接状态描述为业务成功/失败。
- 已连接 Dashboard 的“先处理这一件”只从有界真实列表派生：`waiting_for_human` Workflow（1 条）优先于 `failed` Workflow（1 条），再优先于当前 `ready_for_review` Proposal（最多 5 条，风险等级后按更新时间）；最近资料只使用最多 4 条 Source Version 的服务端捕获时间。无数据时展示下一步入口，不得伪造总数、最近访问或跨资源关联。
- 创建或打开 Workspace 后提供资料收件箱与工作台入口。不得自动扫描、伪造索引完成或改变 Workspace/storage owner。
- 响应式栅格断点必须按侧栏与内容 gutter 扣除后的主栏可用宽度设计，不能只看整个 viewport。固定最小列宽的表单在
  `1024x768` 等中间宽度必须先重排；例如 Search 查询占整行，模式、Source Version 与命令在剩余宽度内排列。
- 已连接 Dashboard 在 `721px-1100px` 使用两列常用动作、换行的焦点操作和单列最近上下文；`>1100px` 才使用四列动作与双列上下文，`<=720px` 再进入移动布局。任何断点都必须保持 `scrollWidth <= innerWidth`；只有重排后内容实际超过视口时才允许纵向滚动。

### 4. Validation & Error Matrix

| Condition | Required result |
|---|---|
| 无 Active Workspace 或切换中打开业务 deep link | 原业务 URL 显示简短重连与重试，不建立 Workspace Query/SSE，也不显示旧业务事实 |
| Active Workspace ready 且业务 Session 未恢复 | 进入正常业务 Auth；不显示任何控制链接或 Controller 会话门禁 |
| `/` 打开 Workspace 页 | 工作区 active 且无“详情”面包屑；只读展示 Root 和 `zhixu workspace switch` 命令 |
| `/search` 等知识路由 | “知识”和所属分组为 current path 但无选中背景/蓝色边线；只有当前三级链接显示强选中面并为 `aria-current="page"` |
| 展开任一知识二级分组 | 二级显示 registry 提供的语义图标，图标/标签/Chevron 列稳定；三级标签相对二级标签至少右移 `16px` |
| 移动 Sheet 关闭后重开 | AppShell 生命周期内的一级/分组展开状态保持；选择三级链接关闭 Sheet，Escape 恢复焦点 |
| 桌面四组全部展开 | 文档不因侧栏增长而变高；`rail-section.scrollHeight >= clientHeight` 且设置入口可滚动到达 |
| `1024x768` 打开 Search 等固定列宽表单 | 所有控件留在主栏内，`document.scrollWidth === document.clientWidth` |
| `721px-1100px` 打开已连接 Dashboard | 常用动作两列、焦点操作换行、最近上下文单列；无横向溢出或元素重叠 |
| Auth 或可选能力为 `disabled` | 系统仍可显示“运行正常”，但该项进入“需要关注”，并同时展示影响、原因和恢复入口 |
| 单个 Dashboard 待办列表失败 | 保留成功列表派生的焦点，提示结果可能不完整并提供独立重试 |
| 所有 Dashboard 待办列表失败 | 显示读取受阻和重试，不把失败伪装成“今日已收束” |
| `section` 非法 | 保留其他 URL 参数并 replace 为 `section=workspace` |
| 创建、打开或扫描失败 | 保留服务端错误，不显示后续成功事实 |

### 5. Good / Base / Bad Cases

- Good：全新浏览器可直接打开 ready 的 `/dashboard`；`/workspace` 只读展示 Active Workspace。知识以可独立展开的内联三级树呈现，二级有语义图标、三级缩进明确且只有当前三级入口显示强选中面；创作直达入口、四个常用动作与五类设置可用。
- Base：Workspace 切换重建时业务 deep link 保持原 URL 并显示简短重连状态，恢复后从服务端 Active Workspace 继续。
- Bad：用大型浮层或级联菜单遮挡正文、一级/二级/三级同时铺选中背景、二级缺少图标、二级与三级标签平齐、移动 Sheet 重开后丢失展开状态、侧栏内容撑高整页、平铺全部路由、恢复“产出”技术菜单或 Topbar 省略号、从 localStorage 显示旧 Workspace、把 `disabled` 隐藏成完全健康、在入口页渲染完整状态矩阵、以首屏列表长度冒充总数，或在浏览器中选择宿主机目录。

### 6. Tests Required

- Component：Active Workspace unavailable 时不发作用域业务请求；业务 deep link 不探测控制 Session或 Browser Storage；重连 gate、已连接待办优先级、四个常用动作、Source Version 真实投影、导航 registry/根路由、知识三级层级与十个入口、二级分组图标、祖先无强选中 class、独立分组展开、路由追加展开、`aria-controls` 目标、隐藏分支、移动 Sheet 状态保持/关闭/焦点、Topbar 无通用菜单、非法/旧设置分类归一化和系统 `disabled` 详情。
- Browser：真实 API 下使用两个隔离上下文检查无需控制 Cookie/fragment 的 `/dashboard` 与只读 `/workspace`，并覆盖 `1440x900`、`1024x768`、`768x1024`、`390x844`、二级图标与三列对齐、三级至少 `16px` 额外缩进、只有三级强选中面、知识多分组展开与侧栏内部滚动、移动 Sheet 状态保持/链接关闭/Escape 焦点恢复、44px 触达区、无重复 ID/失效 `aria-controls`、主操作可见、系统关闭项/技术详情、零横向溢出及零 console warning/error。

### 7. Wrong vs Correct

```text
Wrong: Active Workspace 暂不可用时从 localStorage 恢复旧 Workspace，或把 `/dashboard` 重定向到目录选择表单。
Correct: `/dashboard` 从 Active Workspace API 和业务认证恢复；宿主机授权只走本机命令，过渡状态保持短、清楚、可操作。

Wrong: “知识”打开覆盖正文的 Dropdown，或把展开状态放在会随移动 Sheet 关闭而卸载的树组件里。
Correct: 菜单内容从 `routeDisplayRegistry` 派生为内联三级 Disclosure，展开状态由 `AppShell` 持有，桌面超长内容只滚动侧栏。

Wrong: 一级、二级和三级都使用浅蓝背景与蓝色左边线，或省略二级图标并让二、三级标签平齐。
Correct: 二级图标来自 `RouteNavigationGroup.icon`，三级至少再缩进 `16px`；祖先只提示当前路径，强选中面只属于当前三级入口。
```
