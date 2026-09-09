# WP2 路由内容恢复收尾（2026-09-08）

按产品父任务 `research/lean-closeout-2026-09-08.md` 的精简范围完成检查。未重跑全仓前端测试、浏览器矩阵或性能治理，不据此宣称原 WP3/WP5/WP6 已实现。

## 已交付行为

- `web/src/routes/RouteContentBoundary.tsx` 使用 React Error Boundary 隔离内容区 render/lazy rejection；既有 Suspense 继续负责加载状态。
- `AppShell` 只将内容区的 Suspense/Outlet 放进边界。主导航、退出登录、连接状态与 Quick Capture 保留；Auth、Workspace 和 Event Store 的权威 Provider 在边界外。
- `location.key` 变化只清除错误，不以导航 key 重挂健康页面，因此同页 URL 筛选变化保留本地输入。Workspace ID 作为边界 key，在作用域变化时重建内容树。
- “重试页面”显式重挂失败内容；lazy 已拒绝的 Promise 不会被 React 自动重新导入，保留“刷新页面”由用户重新加载资源。无自动刷新或无限重试。
- 恢复区域有具名 `alert`、文字说明和原生按钮；出现时焦点落到“重试页面”。
- `main.tsx` 使用 root `onCaughtError`。通过 `instanceof RouteErrorBoundary` 识别本边界，只记录固定 `ROUTE_CONTENT_ERROR`；不记录异常正文、stack、URL 或敏感内容。其他边界沿用自身错误报告行为。
- 业务 API Problem 继续由原 Feature owner 处理；此边界不增加对事件处理器或任意未捕获异步错误的保证。健康页面导航的草稿保留，与失败内容被卸载后的重建是不同生命周期。

## 必要验证

| 验证 | 结果与证据来源 |
|---|---|
| AppShell / AppRoutes / Boundary / Auth / Workspace 定向行为测试 | 61 项通过，复用本轮实施代理交接的有效结果；检查代理未修改这些代码，未重复执行 |
| `npm run lint --prefix web` | 检查代理执行，通过 |
| `npm run build --prefix web` | 检查代理执行，通过；脚本先执行 `tsc --noEmit`，再构建 Vite production bundle |
| TypeScript | 构建内 `tsc --noEmit` 通过，亦复用此前独立 typecheck 通过结果 |
| `git diff --check`（受影响文件） | 通过 |

现有测试覆盖渲染失败、安全日志、焦点、显式重试、lazy 拒绝与用户刷新、保留 shell 操作、导航清错、Workspace 清错，以及同页 URL 变化保留输入。检查未发现需要继续修复的行为或安全缺陷。构建保留原有超过 500 kB chunk 的提示；完整拆包与预算治理按用户要求移出本轮。

必要前端检查已完成；未执行 commit、push 或部署。
