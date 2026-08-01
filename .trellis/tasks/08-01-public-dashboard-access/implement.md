# 首页公开访问与控制操作授权拆分实施清单

> 用户已审阅最终规划并明确批准实施；任务已进入 `in_progress`。本文件按实际代码和验证结果逐项更新。

## 1. Ordered Implementation

| ID | 工作 | 主要影响文件 | 验收 | 状态 |
| --- | --- | --- | --- | --- |
| T01 | 固化完整状态矩阵，并把 `BackendLocator` 深化为单次 State+backend discovery 的 RuntimeAccess locator | `internal/hostcontroller/types.go` 及直接 table tests | waiting/ready/unavailable、terminal operation、Active 缺失/unavailable、backend failure 与代理门一致 | [x] |
| T02 | 实现独立 `hostRouter()`、`GET/HEAD /host/v1/runtime`、安全头、poll clamp、canary 错误脱敏和代理 runtime-invalid header | `internal/hostcontroller/http.go`, `internal/hostcontroller/http_test.go` | 整个 `/host` 不回落 HTML；无 Session 最小字段；路径/错误/凭证零泄漏；控制写接口不变 | [x] |
| T03 | 新增严格前端 runtime-access client | `web/src/api/runtime-access.ts` 及测试 | 严格 Schema、UUID/状态组合、Abort/Problem/轮询边界通过 | [x] |
| T04 | 建立 `RuntimeAccessProvider/Boundary` 与共享 invalidation channel；Direct 模式保留 storage adapter，Controller 模式忽略 storage 恢复/事件 | `web/src/app/runtime-access-context.tsx`, `web/src/app/active-workspace.ts`, `web/src/api/auth.ts`, `web/src/events/server-events.ts`, cache/SSE tests | stale/storage event 不切换；proxy header 立即 fail closed；乱序响应无效；A 清理后才发布 B | [x] |
| T05 | 把常驻 HostControlProvider 收窄为惰性控制面 Owner，并保证原始 Token 每个 App 生命周期最多交换一次 | `web/src/app/host-control-context.tsx` 及测试 | Controller 401 不清业务 runtime；普通路由不探测控制 Session；路由往返不重放 Token；命令保护不变 | [x] |
| T06 | 重组 Controller 路由：`/`、`/workspace` 走控制面，其余路由走 runtime + business auth | `web/src/app/App.tsx`, `web/src/app/App.controller.test.tsx`, route tests | 普通深链不显示控制门禁；`/#control -> /dashboard -> /workspace` 交换一次且 Cookie 恢复成功 | [x] |
| T07 | 清理 Dashboard 无必要宿主路径 tooltip，保持设置/导航到控制页的授权语义；Controller 模式设置页不直接改写 Active Workspace | `web/src/features/business/DashboardPage.tsx`, `web/src/features/settings/SettingsPage.tsx` 相关测试 | 公共投影和普通首页 DOM 无隐藏宿主路径；切换动作只进入 `/workspace`；Direct 模式清除行为兼容 | [x] |
| T08 | 更新安全、Workspace 与前端状态规范 | `.trellis/spec/backend/workspace-root-grant.md`, `.trellis/spec/frontend/state-management.md`, `.trellis/spec/frontend/component-guidelines.md`, 必要架构文档 | 文档权限矩阵和可执行测试与代码一致 | [x] |
| T09 | 完成多浏览器、认证模式、重启与切换浏览器验收 | Controller Playwright/fixture 或新增 focused E2E | AC1-AC10 有可复核证据，桌面/移动无异常 console/network | [ ] |
| T10 | 勾选 8 月 1 日需求优化清单 TODO 1 | `docs/product/2026-08-01-requirement-optimization-list.md` | 仅在全部验证通过后标记完成并记录最终边界 | [ ] |

## 2. Implementation Order and Checkpoints

### Checkpoint A: Backend Contract

1. 先写完整矛盾状态矩阵和匿名泄漏回归测试。
2. 提取纯 state projection，并用一次 State read + 条件 backend discovery 组成 RuntimeAccess locator；业务代理与公开 handler 共用 locator。
3. 增加独立 `hostRouter()`，覆盖整个 `/host` namespace、HEAD/404/405、poll clamp 和错误 canary；不改 `/control/v1/state` 及 mutation 注册。
4. 给 Host 代理自产生的 runtime 502/503 添加专用 unavailable header，并运行 focused Go tests确认业务错误 503 不被误标。

### Checkpoint B: Frontend Authority

1. 先写 runtime client decoder tests。
2. 实现 RuntimeAccessProvider 的请求 epoch、Abort、轮询、proxy invalidation subscription 和 fail-closed 退避转换。
3. 把 Active Workspace 发布/清理职责从 HostControlProvider 移交给 RuntimeAccessProvider。
4. 验证 Query/SSE 清理完成前不会发布新 Workspace。

### Checkpoint C: Route Composition

1. 保持 HostControlProvider 常驻但只在控制路由激活，顶层隔离控制页面与业务页面。
2. 保持 Direct 模式和启动器 `/#control=...` 兼容，锁定路由往返只交换一次。
3. 覆盖业务 auth required/disabled、普通深链、waiting/unavailable 和 Controller Session 失效。

### Checkpoint D: Documentation and Browser Gate

1. 同步 Trellis specs 和 8 月 1 日需求文档。
2. 构建 Controller 前端并运行桌面/移动浏览器。
3. 使用两个隔离浏览器上下文验证多浏览器；分别使用受控 fixture 验证 Session 401、Controller 进程重启、`zhixu restart` 和 A -> B。
4. 检查 DOM、响应、console、network、storage，不得出现控制凭证或公共宿主路径。

## 3. Validation Commands

按改动范围逐级执行；预计超时或依赖 Docker 的门禁单独记录结果：

```bash
go test ./internal/hostcontroller ./internal/webassets ./cmd/hostcontroller

cd web
npm run lint
npm run typecheck
npm run test -- --run src/api/runtime-access.test.ts src/app/App.controller.test.tsx src/app/host-control-context.test.tsx
npm run build
```

浏览器门禁使用项目现有 Playwright 配置或 focused Controller fixture，覆盖 `1440x900` 与 `390x844`。最终执行：

```bash
git diff --check
```

## 4. Review Gates

- 使用 `go-review` 审查 Go/HTTP/安全边界，重点检查共享 readiness、Host、错误脱敏和凭证代理。
- 使用 `code-review-and-quality` 审查跨层数据流、React Provider 生命周期、缓存/SSE 清理和测试缺口。
- 若涉及 SQL、迁移或 Repository，立即停止并重新评估；本设计不需要数据库变更。
- 所有 Review 明确缺陷在任务范围内修复后重新验证。

## 5. Risk and Rollback Points

| 风险 | 防护 | 回滚点 |
| --- | --- | --- |
| 公开接口泄漏完整控制状态 | 专用 DTO、严格字段测试、绝不序列化 `State` | 删除独立 handler/route |
| readiness 与代理判断漂移 | 共享纯函数和同一矩阵测试 | 恢复旧 `StateBackend` 判定 |
| stale Workspace 短暂渲染 | 先清空/取消/移除/关闭，再发布新 ID | 恢复旧 Provider 组合 |
| Controller 失效误登出业务用户 | 两个 Provider 独立，控制 401 不调用业务清理 | 回滚路由组合 |
| Direct 模式回归 | Controller 分支内新增 Provider，DirectApp 保持原组合 | 单独回滚 ControllerApp |

本任务无数据库迁移、无持久格式变化；代码回滚不需要数据回滚。

## 6. Planning Exit Gate

- [x] 用户选择业务工作台范围方案 B。
- [x] PRD 无阻塞产品问题。
- [x] `design.md` 与 `implement.md` 已形成。
- [x] 后端与前端研究已持久化。
- [x] `implement.jsonl` 与 `check.jsonl` 已配置真实上下文。
- [x] 用户审阅最终规划摘要并明确批准进入实施。
- [x] `task.py start` 成功，任务状态为 `in_progress`。
