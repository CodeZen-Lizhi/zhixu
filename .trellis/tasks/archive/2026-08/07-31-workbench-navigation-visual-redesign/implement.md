# 实施计划

## 1. 建立冷色视觉基线

- 在 `web/src/styles.css` 重建全局颜色、字体、间距、圆角、Focus 和交互令牌。
- 先拆分 `--brass` 的选中与 warning 双重语义；迁移期只为无歧义旧 token 提供临时 alias。
- 将裸 `button/input/h1` 规则收敛为 reset，把视觉规则移入 `.ui-*` 或 Feature class，避免全局级联破坏 Graph/RAG/Overlay。
- 将主按钮从黑色改为蓝色，移除胶囊按钮、Georgia 标题、米黄/黄铜背景和装饰渐变。
- 收紧 `page-intro`、`ui-card`、状态面板、表格、Dialog、Sheet 和 Badge 的共享样式。
- 同步清理 `model-settings.css`、`timeline.css`、Graph CSS 中依赖旧暖色或 Georgia 的样式。
- 保留语义化绿/琥珀/红，不把业务状态统一染蓝。

验证：颜色扫描、`git diff --check`、前端 lint/typecheck，以及共享 UI 组件测试。

## 2. 收敛 AppShell 导航

- 建立 route display registry，集中维护 section、label、base path、详情 parent 和 active match；AppRoutes 仍只负责 Route/lazy 组合。
- 把 `AppShell.tsx` 的能力平铺数组改为“一级入口 + 可选菜单分组”的单一导航模型。
- 常驻“工作台 / 知识 / 创作”，底部保留“设置”；删除“系统”与独立“工作区”一级入口。
- 知识标签默认进入 `/inbox`，Chevron 图标按钮打开分组菜单；创作标签直接进入 `/authoring`，不再提供 Proposal、Workflow Run、Artifact 技术对象菜单。
- `/authoring` 创作台顶部呈现“新建文章 / 整理成文”，下方按“最近草稿 / 待确认 / 已完成”组织状态；正式 Document Draft 创建能力由新增创作子任务拥有，导航任务不得用空壳或假成功替代。
- 桌面 Dropdown 和移动 Sheet 复用同一数据、活动路径判定和 Workspace 可用性规则；保留 `/proposals`、`/workflows`、`/artifacts` 深链与创作归属高亮。
- 删除 Topbar `…` 菜单；工作区设置、系统状态和资料收件箱继续使用正式导航。认证模式提供明确账户/退出按钮，开发模式只显示状态。
- 将认证、SSE 和 Workspace 上下文压缩为图标/短标签，完整解释进入 Tooltip 或明确会话动作。
- 从 AppShell 移除第二个页面 `h1`，新增共享 PageHeader，并把路由页的品牌/口号式 intro 收敛为一个 `h1` 与最多一句说明。
- 更新 `AppShell.test.tsx`：一级入口、菜单可达性、详情路由高亮、未连接状态、键盘语义和 Lazy route 持久性。

回滚点：只回退 AppShell 组合即可恢复原导航，业务路由不变。

## 3. 重组 Settings

- 从 `BasicPages.tsx` 提取 Settings 到 `web/src/features/settings/SettingsPage.tsx`，避免继续与 Inbox/Documents 共置。
- 新增 Settings section URL 解析与写入模块，允许 `workspace|models|exports|access|system`，未知值规范回退。
- 创建响应式本地分类导航：桌面纵向导航，移动端单一菜单。
- 创建 Workspace 设置面板，统一拥有当前 Workspace、宿主机路径、Git 和切换入口；扫描入口链接到资料收件箱。
- 保留 `ModelSettingsPanel` 与 `AttachmentExportPanel` 的既有状态所有权，只移除外层重复 Card/说明。
- 将 API Token 设置提取为独立 Feature 组件，保持一次性 Secret、离页保护、分页和撤销重试行为。
- 将 Runtime 与 Maintenance 合并为系统状态分类；里程碑与技术边界放入按需展开区域。
- 重构 `SystemStatusPage display="full"`：顶部运行结论与最近检查时间；只列异常、降级和关闭项；正常依赖合并；版本、请求 ID 和完整依赖值进入默认折叠的技术详情。
- 保持 Loading、请求失败、严格响应解码失败与 Degraded 不同状态，异常项提供重新检查或对应设置入口。
- 更新 Settings、模型、导出和 Token 测试，重点确认结构变化没有改变业务契约。

回滚点：Settings route 可恢复指向旧组件；API、Query 和存储层无需回滚。

## 4. 简化 Workspace 入口

- 将首次连接改为“新建 / 使用 Workspace ID”分段模式，默认只显示当前模式的字段。
- 删除巨型品牌引言、重复 section heading、常驻 System Status 说明和已连接 Workspace 摘要副本。
- 未连接时保留创建、打开、错误和无效本地引用恢复；连接成功后进入 Settings 工作区分类。
- Git 和当前 Workspace 事实移入 Workspace 设置面板；扫描 mutation、文件表和结果说明从 Workspace 页面移除，由 Inbox 统一拥有。
- 更新 `WorkspacePage.test.tsx` 和路由测试，保留 `/`、`/workspace` 与 `/settings` 深链兼容。

依赖：Workspace Root 切换的真实 Host Controller 状态来自独立任务；本任务不得伪造成功或自行定义后端协议。

## 5. 重构已连接首页

- 保留现有 `waiting_for_human Workflow → failed Workflow → ready_for_review Proposal` 的有界焦点优先级与独立失败恢复。
- 在焦点区后加入固定命令带：“快速记录 / 新建文章 / 整理成文 / 搜索知识”；不增加个性化状态。
- 分别绑定 Quick Capture、`/authoring/new`、Organizing 发起页与 `/search`；对应能力未交付前不合入可点击入口。
- 合并“继续最近的资料线索”和“最近捕获”的重复导航，只保留一个真实 Source Version 上下文区和一个次级 Inbox 文本入口。
- 将 Dashboard 固定列改为按可用宽度重排的流式栅格；消除 `1440px` 内容帽导致的失衡、固定 `164px / 188px / 320px` 中间宽度问题和容器高度造成的无意义滚动。
- 更新 `DashboardPage.test.tsx`：四个固定动作、唯一 Inbox 导航、真实待办优先级、部分/全部失败、Source 空态和依赖未就绪不渲染假入口。

依赖：Quick Capture、Document Draft 与 Organizing 的真实路由/状态契约分别来自笔记工作流能力子任务。可以先完成纯布局、重复入口和搜索入口，但完整首页 AC 必须等待三项依赖可用后集成验收。

## 6. 精简目标页面文案

- Workspace 与 Settings 各保留一个页面级标题和最多一句说明。
- 删除中英混排 Eyebrow、口号式标题、重复 Card 描述和常驻实现细节。
- 将 Secret、权限、重启、冲突和不可逆行为的必要说明移到对应控件旁或 `details` 中。
- 对 AppShell 状态和目标页面执行文案重复扫描，避免同一事实同时出现在 Topbar、页面引言和 CardHeader。

## 7. 质量验证

短时自动化门禁：

```bash
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web -- AppShell AppRoutes DashboardPage SystemStatusPage WorkspacePage SettingsPage shared/ui
npm run build --prefix web
git diff --check
```

静态审查：

```bash
rg -n '#f3f0e8|#e9e3d6|#a9782c|Georgia|radial-gradient|linear-gradient' web/src
rg -n '运行边界 / Settings|连接你的本地知识|资料与知识|系统' web/src
rg -n '工作台快捷入口|查看资料收件箱|前往资料收件箱|产出菜单' web/src
```

真实浏览器门禁：

- `1440x900`：首页四个动作、唯一 Inbox 导航、Settings 五分类、知识菜单、创作台入口与旧创作生命周期深链、长路径、错误、模型状态与 Token 流程。
- `1024x768` 与 `768x1024`：首页焦点/命令带/资料区重排、Topbar 换行、Settings 栏切换和无意义页面滚动。
- `390x844`：首页两列命令带、Sheet 导航、Settings 分类菜单、连接表单、长文本换行、表格滚动和按钮稳定尺寸。
- 检查所有引用资源可见、无页面级横向溢出、无内容不足时的无意义纵向滚动、无文本遮挡、无 Console error/warning、无失败 Network 请求。
- 检查键盘 Tab 顺序、Dropdown/Sheet 焦点恢复、Focus Ring 和 `prefers-reduced-motion`。

## 8. Review Gate

- 使用 `code-review-and-quality` 审查路由兼容、可访问性、状态所有权、移动端与测试覆盖。
- 若修改模型设置组件，按 `.trellis/spec/frontend/model-settings.md` 逐项核对 Secret 与 desired/active/applied 契约。
- 视觉验收以 PRD 中桌面/移动观察结果为准；不得只以 Snapshot 或 CSS 搜索替代真实浏览器检查。
