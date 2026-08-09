# 工作台导航与全局视觉重设计：交付结果

## Delivered Behavior

- 主导航收敛为“工作台 / 知识 / 创作 / 设置”。知识主链接直达资料收件箱，Chevron 菜单按“资料 / 探索 / 组织 / 学习”提供 10 个知识能力入口；创作直达 `/authoring`，不再展示 Proposal、Workflow Run、Artifact 技术对象菜单。
- 创作台以“新建文章 / 整理成文”为主任务，并按“最近草稿 / 待确认 / 已完成”组织真实状态；底部“高级记录”用任务语言提供审批记录、执行记录和生成产物入口，保留既有深链可达性。
- Topbar 删除通用 `…` 菜单，只保留面包屑、快速记录、同步状态和明确的认证退出动作；移动端使用同一导航事实与可恢复焦点的 Sheet/Dropdown。
- Settings 收敛为工作区、模型与检索、数据导出、访问权限、系统状态五类；Workspace/Git 与 Runtime/Maintenance 分别合并，首次连接和资料扫描职责不再重复。
- 系统状态改为运行结论、异常/降级项与折叠技术详情，不再平铺能力矩阵；版本和请求 ID 只在技术详情中展示。
- 已连接首页固定提供快速记录、新建文章、整理成文、搜索知识四个真实入口，只保留一个次级 Inbox 导航，并改为按可用宽度重排的稳定布局。
- 全站共享视觉统一为白色表面、冷灰边界、蓝色主操作和独立语义状态色；移除暖色纸张、Georgia、页面背景渐变，并补齐 44px 菜单触达区。

## Validation Evidence

- 前端静态门禁：`npm run typecheck`、`npm run lint`、`npm run build` 和 `git diff --check` 通过；构建只保留既有 Monaco 大 Chunk 提示。
- 前端回归：`npm test --prefix web` 通过，共 107 个测试文件、1136 个用例；AppShell 定向覆盖知识四组菜单、创作直达、Topbar 无通用菜单、桌面/移动 Escape 焦点恢复和未连接门禁，路由回归覆盖开发模式下 Memory/Interview 三条身份型深链不挂载受保护页面，Authoring 定向覆盖三类高级记录深链。
- 静态视觉扫描：`web/src` 不再命中 `#f3f0e8`、`#e9e3d6`、`#a9782c`、Georgia、旧黄铜透明派生或页面渐变。
- 浏览器烟测：真实 API、Worker、Vite 与隔离 QA Workspace 下验证 `1440x900`、`1024x768`、`768x1024`、`390x844`；四个视口 `scrollWidth === innerWidth`，菜单完整位于视口内，Console 0 error / 0 warning。首页在 1440 下文档高度等于视口，1024/768/390 仅因动作和上下文重排产生正常纵向滚动；中间宽度为两列动作、换行焦点操作和单列资料区，无元素重叠。额外逐页巡检 16 个业务入口，均只有一个主标题且无横向溢出。
- 系统状态复验：桌面与移动端均显示“运行正常”主结论，并把认证开发模式和 RAG 关闭列入“需要关注”；两项均显示影响、原因和设置恢复入口，最近检查时间可见，15 项运行事实默认折叠。`1440x900` 文档无额外滚动，`390x844` 无横向溢出或控件文字裁切。
- 浏览器交互：知识菜单展示资料收件箱、检索、对话、知识图谱、集合、时间线、知识健康、复习、记忆和访谈；Escape 后焦点返回触发按钮。创作台高级记录分别指向 `/proposals`、`/workflows`、`/artifacts`。
- 视觉证据保存在 `output/playwright/knowledge-menu-*.png`、`output/playwright/authoring-records-*.png`、`output/playwright/dashboard-*.png`、`output/playwright/system-status-*.png` 与 `output/playwright/new-article-*.png`。

## Review And Delivery Notes

- Review 覆盖路由归属、Workspace 门禁、菜单数据单一来源、键盘/焦点、移动端弹层、响应式宽度、深链兼容和既有 Server State/Secret 所有权；未发现剩余 P0/P1 问题。
- 浏览器复核期间修复了首页 `721px-1100px` 固定列组合和 1440 首屏无意义滚动、Search 表单 95px 横向溢出、首页装饰渐变、移动端状态徽章换行、知识 submenu 无 UI 消费方、创作高级记录不可达，以及开发模式 Memory/Interview 主动请求受保护接口产生 403。
- 本结果写入时尚未创建 Git commit 或归档任务；用户已于 2026-08-05 随后明确授权整批提交与推送。该视觉任务仍保留未归档，等待单独整理任务状态。
