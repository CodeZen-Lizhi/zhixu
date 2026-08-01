# Research: 方案 B 前端与控制契约规划复核

- Query: 只读复核当前任务的 `prd.md`、`design.md`、`implement.md`，检查方案 B 的 React Provider、路由、Active Workspace、Query/SSE、业务认证与 Controller Token 设计是否符合现有代码。
- Scope: internal
- Date: 2026-08-01

## Findings

### 结论

当前 Provider 分层、业务认证独立性、控制路由隔离和 Direct 模式保留方向与现有代码基本一致，但规划尚有 2 个阻塞问题和 2 个中等级落地缺口。`implement.md:91` 当前勾选的“PRD 无阻塞产品问题”不成立；至少应先解决下述 P0-1 与 P0-2，再批准进入实施。

### P0-1 Blocker: 路由级 HostControlProvider 会在重新进入控制页时重放一次性 Controller Token

证据：

- 启动器固定生成根路由控制链接 `/#control=...`（`zhixu:370-412`）。
- 应用启动时立即从 fragment 取出 token 并把字符串作为 `App` prop 长期保存（`web/src/main.tsx:17-21`；`web/src/api/controller.ts:319-330`）。
- 目标设计把 `HostControlProvider` 放在仅 `/`、`/workspace` 挂载的控制分支中（`design.md:84-97`），因此进入业务路由时 Provider 会卸载，返回控制路由时会创建新实例。
- 每个 Provider 实例都会以传入 prop 初始化新的 `initialTokenRef`，并在建立会话时优先执行 `exchangeControllerSession(token)`，只有没有 token 才读取现有 Cookie Session（`web/src/app/host-control-context.tsx:79-95`、`181-197`）。
- 控制页已有进入 `/dashboard` 的链接（`web/src/features/workspace/ControllerWorkspacePage.tsx:214-224`），所以 `/ -> /dashboard -> /workspace` 是正常路径，不是理论边界。

失败序列：

1. 首次 `/` 挂载 Provider，成功消费一次性 token 并建立 Controller Cookie。
2. 点击“进入工作台”，控制分支卸载。
3. 从设置或链接返回 `/workspace`，新 Provider 再次收到 App 中未清空的同一个 token。
4. 新 Provider 重复交换已经消费的 token，得到 401；现有有效 Cookie 没有机会走 `getControllerSession()`。

建议：

- 在路由分支之上增加一次性交接 owner，而不是把原始 prop 重复传给每次 Provider 挂载。可由 `ControllerApp` 持有 `useRef` 并提供 `takeInitialControllerToken(): string | undefined`，第一次控制分支取得后永久清空；后续挂载拿到 `undefined`，通过 Cookie 恢复控制 Session。
- 备选方案是让 `HostControlProvider` 始终挂载，但这会让普通业务路由继续主动读取控制会话，不如路由上方的轻量 token broker 符合当前解耦目标。
- T06 的影响文件/验收应明确包含 token broker 与 `main.tsx`/`controller.ts` 是否保持不变的决定。
- 新增 StrictMode 路由生命周期测试：`/#control -> /dashboard -> /workspace`，断言 token exchange 总计一次，第二次控制页挂载调用 `getControllerSession` 并成功，而不是显示控制链接失效。

### P0-2 Blocker: 公开投影没有 runtime incarnation，轮询无法保证观察同一 Workspace 的换代

证据：

- 公开响应只有 `status`、`active_workspace_id` 和 `poll_after_ms`（`design.md:43-67`；`prd.md:25-29`）。
- 设计据此断言同一 Workspace 重建“必须经过 unavailable”，因此不需要公开 grant generation（`design.md:107-118`）。这个断言只描述服务端状态经过了 unavailable，不能保证轮询浏览器实际观察到该瞬时状态。
- 服务端权威状态本身用 `GrantGeneration` 隔离 runtime incarnation，API/Worker 记录也绑定 generation（`internal/workspace/domain/control.go:82-109`、`124-137`）。
- 切换失败且恢复旧 Workspace 时，恢复会把同一个 Workspace ID 绑定到新的 `RecoveryGeneration`（`internal/workspace/adapter/postgres/control.go:446-506`）。
- 前端清理契约依赖观察到 ID 为空或变化；Query 清理由 Workspace ID 定位（`web/src/app/workspace-runtime-state.ts:21-33`），SSE 也以 Workspace ID 建立和清理（`web/src/events/event-store.tsx:333-340`）。

可复现的契约缺口：浏览器在相邻两次轮询中看到 `ready/A`，期间服务端经历 `A -> unavailable -> 新 generation 的 A`。快速回滚、后台 Tab 定时器节流或设备休眠后恢复都可能跳过中间状态。因为前后 ID 相同，Provider 不会卸载 A，不会清理旧 Query/SSE，违反 R8/AC4/AC8 的旧 runtime 数据隔离目标。

建议：

- 公开契约增加一个只用于相等性比较、随 effective grant generation 改变但在 Controller 进程重启时保持稳定的 runtime incarnation 信号。可以是经过明确安全评估的 `runtime_epoch` 字段或响应 ETag；前端用 `(workspace_id, runtime_epoch)` 作为 runtime key，但业务请求仍使用真实 Workspace UUID。
- 如果不希望公开原始 generation，必须提供等价、不会漏掉中间转换的持久 incarnation/transition token。单纯缩短轮询、依赖 `visibilitychange` 或依赖业务请求恰好返回 503 都不能证明不漏转换。
- 这会要求有意识地修订 R4/R5、严格 decoder、Provider key 与隐私测试。若使用已有 grant generation 或其稳定投影，不必新增数据库格式；若引入随机持久 epoch，才需要重新评估“无持久格式变化”的承诺。
- 增加两类测试：前端直接从 `ready(A, epoch1)` 跳到 `ready(A, epoch2)` 时必须完整清理再重挂；浏览器后台/恢复后未观察到 unavailable 也必须识别 epoch 变化。

### P1-1 Medium: Runtime proxy race 的主动刷新只有设计承诺，没有实施 seam

证据：

- 设计要求业务请求遇到 `RUNTIME_NOT_READY` 时触发公开投影刷新（`design.md:141-147`）。
- T03/T04 只列 runtime client/Provider，T06 只列路由；实施清单没有纳入共享 REST 或 SSE 错误入口（`implement.md:9-17`、`29-40`）。
- 当前 `authFetch` 的全局通知只处理业务 401（`web/src/api/auth.ts:77-91`、`145-149`）。
- SSE 对 401 会通知业务认证失效，但其他 5xx 只作为可重试连接错误处理，不会通知 runtime Provider（`web/src/events/server-events.ts:563-618`）。
- Host proxy 会产生 `RUNTIME_NOT_READY`、`RUNTIME_BACKEND_INVALID`、`RUNTIME_PROXY_FAILED`（`internal/hostcontroller/http.go:384-408`）。

影响：切换竞态发生时，某个业务页面会先呈现领域 API 错误或 SSE 重连状态，直到下一次公共轮询才卸载业务树；这与设计的“立即 fail closed 并刷新投影”不一致，也容易让各领域 client 各自实现不同的判断。

建议：

- 增加独立于业务认证的 runtime invalidation channel，例如 `subscribeRuntimeInvalidation`，由 `RuntimeAccessProvider` 订阅。
- 在共享 REST fetch 边界和 SSE Problem 解码后，仅对精确 Host proxy runtime error code 发出通知；不要把任意业务 503 误判成 runtime 失效。
- 通知发生时先把 runtime 置为非 ready、停止旧业务树，再立即刷新公开投影；仍保留定时轮询作为恢复机制。
- 将 `web/src/api/auth.ts`（或新的共享 fetch seam）、`web/src/events/server-events.ts` 及对应测试加入 T04/T06 影响面和验收。

### P1-2 Medium: “公开矩阵与 ReadyBackend 一致”需要明确是否包含 CurrentBackend 可用性

证据：

- T01 要求公开矩阵与 `ReadyBackend` 一致，并计划提取共享纯 readiness 判定（`implement.md:9`、`24-27`、`80-82`；`design.md:71-78`）。
- 现有 `StateBackend.ReadyBackend` 不只检查投影状态，还会调用 `Runtime.CurrentBackend`（`internal/hostcontroller/types.go:189-209`）。
- `CurrentBackend` 会实际查询 Compose 端口，可能返回 `RUNTIME_NOT_READY` 或 `RUNTIME_BACKEND_INVALID`（`internal/hostcontroller/compose_driver.go:293-308`）。
- HTTP Handler 同时拥有 `Store` 和 `BackendLocator`（`internal/hostcontroller/http.go:29-53`），但设计没有说明公共 handler 的 ready 分支是否也验证 locator。

影响：如果公共投影只消费 Store state 的纯函数，它可能持续返回 `ready`，而同一时刻每个业务请求都因 backend locator 失败返回 503。R7 允许请求时重查竞态，但不应把持续 locator 故障描述成公开 ready；`design.md:68` 也要求 Runtime 错误脱敏。

建议：

- 把“状态矩阵判定”和“backend locator 验证”写成两个明确阶段。公共投影只有两者都通过才返回 ready；locator 失败映射为脱敏 unavailable/503，业务 proxy 仍逐请求重新执行相同检查。
- 测试分别覆盖 Store ready + locator unavailable、invalid backend URL、两次读取之间的竞态，避免所谓共享纯函数只共享了一半门禁。

### 已对齐且可保留的规划

- Provider 顺序正确：`RuntimeAccessProvider` 位于业务 `AuthProvider` 之外，只有权威 ready 后才建立业务认证、Query 与 SSE（`design.md:80-97`）。这符合当前 `AuthProvider` 先读取 system status、再恢复业务 Session 的实现（`web/src/app/auth-context.tsx:33-105`）。
- 业务认证与 Controller 认证确实独立。当前业务认证只维护业务 CSRF/Session；Controller Cookie/CSRF 有独立 API 和控制 Provider，方案 B 不需要合并凭证。
- 路由方向正确：顶层精确截获 `/`、`/workspace`，其余路径保留在 `AppRoutes`，Direct 模式可继续使用现有同名路由（`web/src/routes/AppRoutes.tsx:32-64`）。
- 当前版 T04 已明确 Direct storage adapter 与 Controller 内存权威源分开，并忽略 Controller 模式 storage 恢复/事件（`implement.md:12`）。这正面覆盖现有 `active-workspace.ts` 会直接从 localStorage 发布 snapshot、监听跨 Tab storage event 的风险（`web/src/app/active-workspace.ts:18-51`）。
- 当前版 T07 已明确 Controller 设置页不能直接 `setActiveWorkspaceId("")`，且 Direct 模式保留现有清除行为（`implement.md:15`）；这与现有设置实现中的直接改写点（`web/src/features/settings/SettingsPage.tsx:28-40`）匹配。
- HostControlProvider 收窄为控制 owner 的方向正确。现有实现确实同时清空 Active、Query 和业务 runtime（`web/src/app/host-control-context.tsx:115-179`），T05 应移除这些职责，但保留 Session/state/CSRF/ETag/idempotency 命令契约。
- T08 指定更新的三份 Trellis spec 正是当前会产生语义漂移的文档：它们仍把完整 Controller state 写成 Active/readiness 唯一来源，必须改成“公共 runtime 投影拥有业务挂载权威，完整 control state 拥有控制页面和命令权威”。

## Files Found

- `.trellis/tasks/08-01-public-dashboard-access/prd.md`：方案 B 范围、R1-R10 与 AC1-AC11。
- `.trellis/tasks/08-01-public-dashboard-access/design.md`：公共投影、Provider 组合、Active/Query/SSE 生命周期与失败恢复设计。
- `.trellis/tasks/08-01-public-dashboard-access/implement.md`：T01-T10 实施顺序、影响文件、验证和回滚点。
- `zhixu`：启动器生成 `/#control=...` 的唯一入口。
- `web/src/main.tsx`：fragment token 的进程级读取点和 App prop 传递。
- `web/src/api/controller.ts`：Controller fragment 消费、Session exchange/read 与 CSRF 命令头。
- `web/src/app/App.tsx`：当前全局 HostControlProvider、控制门禁、Direct/Controller Provider 顺序。
- `web/src/app/host-control-context.tsx`：当前 token/session lifecycle、Active 发布、Query 清理和控制命令 owner。
- `web/src/app/active-workspace.ts`：当前 localStorage-backed external store 与跨 Tab storage event。
- `web/src/app/auth-context.tsx`：独立业务认证恢复与 AuthBoundary。
- `web/src/app/workspace-runtime-state.ts`：Workspace-scoped Query cancel/remove helper。
- `web/src/app/WorkspaceCacheBoundary.tsx`：Workspace ID 改变后的异步缓存清理。
- `web/src/events/server-events.ts`：SSE 401 与 5xx 错误处理。
- `web/src/events/event-store.tsx`：Workspace-bound SSE lifecycle 与卸载清理。
- `web/src/routes/AppRoutes.tsx`：Direct 业务路由注册表。
- `web/src/features/settings/SettingsPage.tsx`：现有设置页直接清空 Active 后导航控制页的行为。
- `web/src/features/workspace/ControllerWorkspacePage.tsx`：控制页到业务页的正常导航路径。
- `internal/hostcontroller/types.go`：StateBackend 的 state + backend 双重 ready 门禁。
- `internal/hostcontroller/http.go`：Host handler seams、业务 proxy 和 runtime 错误码。
- `internal/hostcontroller/compose_driver.go`：实际 backend locator 校验。
- `internal/workspace/domain/control.go`：grant generation、recovery generation 与 runtime binding 模型。
- `internal/workspace/adapter/postgres/control.go`：切换生成新 generation，以及失败后用 recovery generation 恢复旧 Workspace。

## Code Patterns

- Fragment token：首屏读取并从 URL 删除是正确模式，但 token 的“已交接”状态必须跨路由级 Provider 生命周期保存（`web/src/main.tsx:17-21`；`web/src/api/controller.ts:319-338`）。
- 双认证：业务 401 只进入业务 auth invalidation，Controller 401 只清 Controller Session/CSRF；方案 B 应保持这两个通道互不调用（`web/src/api/auth.ts:77-91`；`web/src/app/host-control-context.tsx:199-213`）。
- Active 转换：先发布空 ID，再取消/移除旧 Query，最后发布新 runtime key；Controller 模式不能再从 storage event 直接回写 effective snapshot（`web/src/app/active-workspace.ts:18-51`；`web/src/app/workspace-runtime-state.ts:21-33`）。
- SSE 生命周期：业务树卸载时同步 close connection，再移除旧 Workspace queries；新的 runtime key 必须创建全新 EventStoreProvider（`web/src/events/event-store.tsx:333-340`）。
- Proxy 门禁：Store readiness 和实际 backend locator 都是 ready 的组成部分，公开发现不能代替逐请求重查（`internal/hostcontroller/types.go:197-209`；`internal/hostcontroller/http.go:384-408`）。

## External References

- 无。本次为仓库内规划与现有实现契约复核，不需要外部资料或版本判断。

## Related Specs

- `.trellis/spec/frontend/state-management.md:325-384`：当前完整 Controller state、Active、Query/SSE 和 fragment 生命周期契约；T08 必须拆分 runtime/control 两类 owner。
- `.trellis/spec/backend/workspace-root-grant.md:41-50`：grant generation、ready-only proxy 和 Controller 安全边界；新增公开投影后仍必须保留逐请求 generation/fresh 校验。
- `.trellis/spec/backend/workspace-root-grant.md:76-89`：切换/恢复与真实浏览器验收，包括 A -> B -> A。
- `.trellis/spec/frontend/component-guidelines.md:167-218`：Workspace 入口、根路由和业务 deep link 语义；Controller 模式顶层截获不能破坏 Direct 路由注册表。

## Caveats / Not Found

- 未修改产品代码、规划文档或 spec；只新增本研究记录。
- 未运行测试。本次结论来自静态规划/实现契约复核，阻塞问题应先进入设计和实施清单，再由实现测试验证。
- 按研究角色隔离要求，没有读取 `implement.jsonl` 或 `check.jsonl`。
- 复核期间 `implement.md` 已包含更明确的 Direct storage adapter 和 Settings 行为，本文按当前磁盘版本评估；后续若规划继续更新，应重新核对 P0-1、P0-2 是否已被明确关闭。
