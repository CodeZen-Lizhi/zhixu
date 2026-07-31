# 工作台 UX 优化实施计划

## Preconditions

1. 读取前端层规范、当前任务 PRD/Design 和相关组件测试。
2. 保留 AppShell 的路由、Auth、SSE 与 Workspace state owner；不复制或重建任何后端状态。

## Implementation Steps

1. 在 `AppShell` 中建立任务分组导航，未连接 Workspace 时只显示必要入口，修正 root Workspace 页的面包屑和同步状态文案。
2. 为 `SystemStatusPage` 增加紧凑展示模式，保留完整诊断模式，并使紧凑文案区分 optional RAG disabled 与服务 degraded。
3. 重组 `WorkspacePage`：缩短首屏标题，将创建/打开表单置于首要位置，接入紧凑状态摘要，新增创建/打开后的扫描和进入工作台引导，以及扫描后的资料/工作台 CTA。
4. 调整 `styles.css`，保证桌面 `1440x900` 和移动 `390x844` 的主操作可见、导航易读、无横向滚动，并避免标题单字孤行。
5. 更新 AppShell、SystemStatus、Workspace 页面测试，覆盖新状态与 CTA。

## Validation

1. `npm run lint --prefix web`
2. `npm run typecheck --prefix web`
3. `npm run test --prefix web -- --runInBand` 不适用 Vitest；改为运行受影响测试文件或 `npm run test --prefix web`。
4. `npm run build --prefix web`
5. `git diff --check`
6. 启动 Vite 并以真实本地 API 代理运行 Playwright CLI：
   - 桌面：确认连接主操作在首屏、状态摘要可读、没有 console error/warning。
   - 移动：确认主操作路径、导航 Sheet、无横向溢出、没有 console error/warning。
   - 截图保存在 `output/playwright/`。

## Review and Rollback

- 复核无 route、API contract、storage key、Auth/SSE owner 回归。
- UI 优化只涉及前端；若出现回归可逐文件回退，不影响数据库和 Workspace 数据。
