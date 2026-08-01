# 工作台首页实施计划

## 实施基线

- 代码基线使用 `/Users/zhenglizhi/GolandProjects/zhixu-workbench-navigation-visual-redesign`，分支 `codex/workbench-navigation-visual-redesign`。
- 该 worktree 已包含尚未提交的 Clearline 全局导航与白蓝 Token。本任务在其上增量实现，不把这些改动复制回脏的 `dev` 工作区，也不覆盖主工作区正在进行的 Host Controller/Workspace 改动。
- 不新增依赖，不修改后端、API decoder、路由 URL、Active Workspace、SSE 或认证所有权。
- 产品代码修改前先运行 `trellis-before-dev`，实现后运行 `trellis-check` 与前端 Review。

## 数据流与事实边界

```text
Active Workspace ID
  -> getWorkspace(id) ---------------------------------> Workspace 标题与本地目录事实
  -> listWorkflows(id, waiting_for_human, limit=1) ----┐
  -> listWorkflows(id, failed, limit=1) ---------------+-> Focus Projection -> 先处理这一件
  -> listProposals(id, ready_for_review, limit=5) ------┘
  -> listSourceVersions(id, limit=4) ------------------> 最新资料线索 + 最近捕获

无 Active Workspace
  -> 不发业务列表请求
  -> 品牌入口 + 静态产品对象脉络 + /workspace 主操作
```

- Proposal、Workflow 和 Source Version 都继续使用现有严格客户端与 Workspace-scoped Query Key。
- Dashboard 不再读取或渲染完整 `SystemStatusPage`。列表或 Workspace 查询失败时，在受影响区域显示紧凑错误和重试；完整系统状态仍由 Settings 拥有。
- Source Version 只能投影 `path`、`capturedAt`、`securityStatus`、`ingestionStatus`、`indexStatus` 等公开字段。
- 不把当前页条数当总数，不从时间或状态猜测 Proposal、Source 和 Workflow 的关联。

## 首页动作投影

新增一个小型纯投影函数，输入三个有界结果并返回单一 Focus 项：

1. 最新 `waiting_for_human` Workflow：文案“需要你的处理”，操作“继续流程”。
2. 最新 `failed` Workflow：文案“流程需要检查”，操作“检查流程”。
3. `ready_for_review` Proposal：在当前五项内按 `CRITICAL > HIGH > MEDIUM > LOW`，同风险按 `updatedAt` 倒序，操作“开始审阅”。
4. 三类都为空：展示“今天没有等待处理的事项”，操作进入资料收件箱。

这是首页展示策略，不显示“系统全局最高优先级”等无法由服务端证明的文案。任一 Query 失败时仍允许其他成功结果形成 Focus，同时在 Focus 下方标出“部分待办读取失败”并提供重试。

## 组件与样式

### 1. AppShell 入口变体

- 根据 `location.pathname === "/dashboard" && workspaceId === ""` 增加单一 `workbench--dashboard-entry` 壳状态。
- 复用现有 route display registry 和 Navigation；入口态只改变呈现，不创建第二套路由或导航数组。
- 入口态在桌面与 `390x844` 使用 `78px/52px` 紧凑图标轨，保留工作台与设置图标、可访问名称和 Tooltip。
- 标准 Topbar 在入口态替换为紧凑入口标题与本地边界入口；连接 Workspace 后立即恢复完整 Clearline 壳。
- 其他路由、已连接 Dashboard、移动 Sheet 和菜单行为保持不变。

### 2. 未连接 Dashboard

- 使用语义化 `section` 与内联 SVG 实现确认稿，不把整张 PNG 当页面背景。
- 主内容只包含“知序”、两行价值文案、简短说明、主链接“连接知识目录”和弱化的数据边界披露。
- SVG 保留四个中文节点、一条蓝色主路径和一条冷灰补充关系；桌面与移动使用各自独立 `viewBox`/路径。
- 数据边界披露最多一句：“只连接你选择的本地目录；AI 建议经确认后才写回。”，默认收起，不占用首屏。
- 一次性淡入和路径绘制不超过 `500ms`；Reduced Motion 直接显示最终状态。

### 3. 已连接 Dashboard

- 页面 `h1` 为“今天的知识桌面”，上方显示真实日期、Workspace 名称和 Git 分支；不重复 AppShell 标题。
- Focus Strip 使用连续分隔线，不使用 Card；对象类型、风险/状态、更新时间和 CTA 全部来自真实结果。
- “继续最近的资料线索”使用最新 Source Version：标题取文件名，保留完整相对路径和捕获时间；处理脉络映射为安全、解析、索引三个真实状态。
- “最近捕获”使用其余最多三项 Source Version；只有路径、时间、类型/状态和文档详情链接。
- 无 Source Version 时显示紧凑起步路径“前往资料收件箱”，不填充示例内容。
- 工作区/列表 Loading、Empty、Partial Error 使用稳定高度，避免加载完成后大幅跳动。

### 4. 响应式与可访问性

- `1440x900`：Focus 三列，资料区双列，首屏露出资料区；未连接图谱覆盖主画布。
- `390x844`：Focus 与资料区单列，主按钮在第一视口；未连接 CTA 与四节点图不重叠、页面无横向溢出。
- SVG 提供 `title/desc`，关系含义不只靠颜色；状态同时显示中文文字与形状。
- 所有链接和按钮支持键盘与可见 Focus，数据边界披露使用 `aria-expanded`/关联区域。
- 长路径用受控换行或省略，并保留完整 `title`/可访问文本。

## 预计修改文件

- `web/src/features/business/DashboardPage.tsx`：两种首页状态、Query 组合与页面组合。
- `web/src/features/business/dashboard-view-model.ts`：Focus、文件名、状态和时间的纯投影。
- `web/src/features/business/dashboard-view-model.test.ts`：优先级、同风险排序、非法时间和状态文案。
- `web/src/features/business/DashboardPage.test.tsx`：未连接、真实工作态、空态、部分失败和不请求系统状态。
- `web/src/app/AppShell.tsx`、`web/src/app/AppShell.test.tsx`：入口壳变体与其他路由回归。
- `web/src/styles.css`：Dashboard 专属白蓝布局、独立 SVG 断点、Focus/Reduced Motion。

## 验证顺序

1. `npm run test --prefix web -- src/features/business/dashboard-view-model.test.ts src/features/business/DashboardPage.test.tsx src/app/AppShell.test.tsx`
2. `npm run typecheck --prefix web`
3. `npm run lint --prefix web`
4. `npm run build --prefix web`
5. `git diff --check`
6. 启动 worktree 的 Vite 服务，使用真实浏览器分别检查未连接和确定性 API fixture 的已连接状态。
7. 在 `1440x900` 与 `390x844` 截图，与四张确认稿并排检查构图、留白、按钮、节点标签、横向溢出、Console 和异常 Network。

## 回滚边界

- Dashboard 页面、入口壳 class 和 Dashboard 专属样式是独立增量；出现回归时可整体移除，不影响 Clearline 导航、Settings、Workspace 或后端契约。
- 不删除旧路由和 API，完整系统状态继续从 Settings 进入。
