# 120 discovery-failure 实施与最终验证

2026-09-15，impact-ui。本切片范围为 ScanPage 目录读取失败边界、跨页 binding P2 修复及限定验收；不修改 Authoring、119/120/121 SQL、atlas.sum 或 schema.sql。

## 最终实现

- `internal/platform/filesystem/source_discovery.go`：目录 ReadDir 的第二次错误回调先于 cursor/limit 处理，移除失败目录的 ReadDirectories 标记；第二回调不递增 visited。
- `internal/platform/filesystem/source_presence_test.go`：真实不可读目录 limit=1、after==目录、limit=2 不重复计数、权限恢复后空目录成功恢复。实际执行 PASS，未跳过。
- `web/src/features/workspace/WorkspacePage.tsx`：拼接前比较所有页的 workspace_id/binding_version；不一致时隐藏全部记录、提示刷新、隐藏更多按钮，不显示虚假的空列表成功提示。刷新执行 exact resetQueries，从空 cursor 重读。
- `web/src/features/workspace/WorkspacePage.test.tsx`：A binding 页1/B binding 页2 组件回归，断言旧新记录均不混显；刷新后只展示 B 首页，并验证请求 cursor 为空。

## 最终 Web 证据

修复后最后一次执行（18:01:36）：

```sh
cd web
npm test -- src/features/workspace/WorkspacePage.test.tsx src/api/workspace.test.ts
```

**2 个文件、11 项测试 PASS**，耗时 938ms。原始日志：[discovery-failure-web-final.log](discovery-failure-web-final.log)。先前修复后 `npm run typecheck`、限定两组件文件 ESLint 已通过；本次按 main 要求仅重跑定向 Web 测试。

## PG 正式目录证据

最终限定命令：

```sh
go test -tags=integration ./internal/workspace/adapter/postgres -run '^TestDiscoveryFailurePersistsRecoversAndFencesRoot$' -count=1
```

最近执行 **PASS，7.705s**。测试使用 testdb.Require 默认 AtlasMigration，未设置 overlay、未排除任何迁移；当次磁盘正式目录含 119、120、121，已读取 atlas.sum 中 `00121_manuscript_publication_baselines.sql`，当次 checksum 为 `h1:RmOMqx7tVKDpu0OY3muDPbFeDf7tx7ECNfXxhr2XyFs=`。此证据只覆盖当次快照，不追认为覆盖之后 main 更新的 121 checksum。没有把历史 121 checksum mismatch 归为 120 问题。

PG 原始输出保留在当前 impact-ui 会话工具记录：exec session 39290，输出 chunk `8cd097`；没有另存磁盘原始 PG 日志。清理前后此前正式目录限定测试也分别通过，清理后为 11.487s。

## 修复后真实浏览器证据

Playwright session `discovery120p2` 使用实际 WorkspacePage、真实 PostgreSQL fixture、真实扫描与 discovery-failures HTTP handler；active-workspace 路由由 fixture 提供。正式目录迁移，未使用 overlay。真实 unreadable.md 权限失败后，在浏览器点击重新扫描、刷新记录，再整页刷新，确认已恢复及恢复时间保持为 2026/9/15 17:58:50。

保留的 DOM 快照（仓库根目录相对路径）：

- `.playwright-cli/page-2026-09-15T09-58-51-752Z.yml`：手动重扫后已恢复。
- `.playwright-cli/page-2026-09-15T09-58-59-574Z.yml`：点击刷新记录后的快照。
- 整页刷新完成后的已恢复 DOM 在会话工具输出 chunk `f354e2`；`09-59-22-080Z.yml` 仅是刷新时加载态，不作为恢复完成证据。
- 浏览器后台集成 `TestDiscoveryBrowserResume` PASS，137.73s（工具输出 chunk `f68a4c`）。其临时测试文件已删除。

**截图限制：本次没有保存 PNG 截图，只有上述 DOM 快照和工具输出，不将其冒称截图。** 按本轮“只核对定向 Web 测试”的边界未重启浏览器补拍。真实目录 rebind 竞态由组件回归覆盖，未做真实 rebind 浏览器时间线。

## 清理与交付边界

- 已关闭 discovery120、discovery120p2 浏览器及本轮 Vite/HTTP 服务，测试容器由 fixture 清理。
- 已删除 `web/.discovery-smoke/`、`discovery_browser_resume_integration_test.go`、两个 /tmp 浏览器信号目录；原集成测试内旧浏览器临时分支亦已移除。
- 最后核对临时入口不存在，62750/62751 无监听；无本轮 Playwright 挂起调用。
- 无 git commit/push。独立审查文档的开放状态由 impact-check/main 复核关闭；本文件只记录实施与实际证据。
