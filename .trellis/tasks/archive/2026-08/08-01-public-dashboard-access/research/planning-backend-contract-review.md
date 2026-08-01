# Research: 后端运行时投影规划复核

- Query: 只读复核 `prd.md`、`design.md`、`implement.md`，重点检查 `/host/v1/runtime` 合同、共享 readiness、Host/错误脱敏、Controller restart 与现有 Go 架构是否一致。
- Scope: internal
- Date: 2026-08-01

## Findings

### 结论

整体边界方向与现有架构一致：独立匿名只读投影、不开放 `/control/v1/state`、业务代理逐请求复核、控制写接口保持原认证链。进入实施前仍建议修订 3 个高优先级合同缺口和 3 个中优先级可执行性缺口，否则实现很容易出现“浏览器看见 ready、代理却持续 503”、重启验收语义冲突或 `/host` 路径回落 SPA。

### 1. [高] “共享 readiness”目前只能共享状态判定，不能保证与 `ReadyBackend` 最终结果一致

规划位置：

- `design.md:71-78` 要求一个无副作用判定函数同时供公开投影与 `StateBackend.ReadyBackend` 使用。
- `implement.md:9` 要求公开矩阵与 `ReadyBackend` 一致，`implement.md:24-27` 把它作为后端合同检查点。
- `design.md:101`、`prd.md:29` 又把公开投影称为客户端 Active Workspace 的唯一权威来源。

现有代码事实：

- `StateBackend.ReadyBackend` 先读 `State` 并检查投影状态，然后还必须调用 `Runtime.CurrentBackend`：`internal/hostcontroller/types.go:197-208`。
- `Coordinator.State` 只读取持久化快照并投影，不调用 Docker/backend discovery：`internal/hostcontroller/coordinator.go:148-164`。
- runtime heartbeat 在 20 秒窗口内都算 fresh：`internal/hostcontroller/coordinator.go:15-26`、`internal/workspace/adapter/postgres/control.go:69-82`。进程正常退出会把记录改为 unavailable，但崩溃、强制删除或关闭更新失败时仍可能留下短时 fresh 记录：`internal/workspace/runtimegrant/runtime.go:217-238`。
- Controller 先启动 HTTP server，再异步执行 `Reconcile`：`cmd/hostcontroller/main.go:160-183`。`Reconcile` 对已有 Active 调用 `ApplyGrant` 并等待 runtime：`internal/hostcontroller/coordinator.go:99-145`；`ApplyGrant` 会 `--force-recreate` API/Worker：`internal/hostcontroller/compose_driver.go:84-126`。

因此，一个只消费 `State` 的纯函数可以与 `ReadyBackend` 的“状态前置门”一致，却不能与包含 `CurrentBackend` 的最终代理可用性等价。可推导的失败场景是：持久记录仍在 fresh 窗口内但 backend 已不存在，公开端点返回 ready，而业务代理在 `CurrentBackend` 处返回 503。`design.md:147` 当前只写“刷新公开投影”，若刷新仍返回同一个 ready，会形成重复挂载/503，而不是稳定降级。

建议在设计中明确二选一：

1. **推荐，保持最小后端改动：** 把公开合同定义为“authoritative state readiness hint”，把共享范围明确为 `State` 的纯判定；承认 `ReadyBackend` 还有 backend discovery 的最终门。前端收到 `RUNTIME_NOT_READY` 后必须先进入本地 unavailable/退避状态，不能仅立即刷新并在同一 ready 响应上重挂载。`implement.md` 的“一致”改成“状态门一致，代理仍有 backend URL 最终校验”。
2. **若产品要求公开 ready 等价于可代理：** 新增一个原子/单所有者的 runtime-access locator，由它一次完成状态快照、Workspace ID 与 backend discovery；公开 handler 丢弃 URL，`StateBackend` 使用同一结果。不要在 handler 中先 `Store.State`、再单独 `ReadyBackend`，两次状态读取会产生 Workspace 切换竞态。

无论选哪种，都应增加测试：状态为 ready 但 `CurrentBackend` 失败时，公开响应、代理响应和前端降级行为必须有确定合同。

### 2. [高] 共享判定的输入矩阵尚未定义，且 Active availability 会改变现有代理行为

规划位置：

- `design.md:55-60` 给出线上的粗粒度矩阵；`design.md:73-76` 只说提取 pure function。
- `implement.md:9` 只列 `types.go` 和 “waiting/ready/unavailable 矩阵”，没有列出不一致 `State` 形状及兼容性变化。

现有代码事实：

- `projectState` 只有在 durable operation 非终态时投影 `switching`；Active 存在但 runtime 不匹配/不 fresh 时却投影为 `waiting_for_workspace`，不是 unavailable：`internal/hostcontroller/coordinator.go:1002-1030`。
- `Coordinator.State` 在没有 durable operation 时可能附带进程内的 terminal `lastOperation`：`internal/hostcontroller/coordinator.go:148-163`。因此不能用 `State.Operation != nil` 直接表示“有阻塞操作”。
- 当前 `StateBackend` 只要求 Runtime/API/Worker 都 ready，不验证 `ActiveWorkspace != nil`、Workspace ID、availability 或 operation：`internal/hostcontroller/types.go:189-208`。
- `projectState` 的 ready 已折叠了 Active、Workspace ID、grant generation、API/Worker role、active phase 与 freshness：`internal/hostcontroller/coordinator.go:939-948`、`internal/hostcontroller/coordinator.go:1019-1028`，但没有检查 Active availability。
- Active Workspace 可以通过可用性重查被持久化为 unavailable：`internal/hostcontroller/coordinator.go:228-242`；runtime 注册/heartbeat 随后会因 unavailable 被拒绝，但旧 heartbeat 在 fresh 窗口内仍可能暂时存在：`internal/workspace/adapter/postgres/runtime.go:195-218`。

建议在 `design.md` 固化一个基于 `State` 的精确矩阵，至少包括：

| `State` 形状 | 公共状态 | 代理状态门 |
| --- | --- | --- |
| `RuntimeReady` + API/Worker ready + Active 非空 + ID 合法 + Active available + 无非终态阻塞操作 | ready + ID | 通过，继续 `CurrentBackend` |
| Runtime waiting + Active 为空 + 无非终态操作 + 无 recovery failure | waiting + null | 拒绝 |
| Runtime waiting 但 Active 非空（runtime mismatch/stale） | unavailable + null | 拒绝 |
| switching、recovery_failed、任一 process 非 ready、非法/矛盾组合 | unavailable + null | 拒绝 |
| `Operation` 非空但 `Result` 已终态，且 Runtime/Active 完整 ready | 仍可 ready | 通过 |

还应明确：增加 `ActiveWorkspace.Availability == available` 是有意的 fail-closed 行为收紧，不是纯重构。建议给 `StateBackend` 增加直接 table test，覆盖 ready 但 Active 为空、Active unavailable、terminal operation、waiting+Active、非法 UUID；当前未找到 `StateBackend` 的直接单测。

实现位置上，`types.go` 与现有 `StateReader`/`StateBackend` seam 相容；公开 endpoint 不应直接复用 `State` JSON，而应只接收纯判定返回的专用 DTO。

### 3. [高] Controller restart 验收混合了三种不同事件，现有 launcher 不支持“重启期间仍 ready”

规划位置：

- `prd.md:27` 把 Session 过期与 Controller 重启并列；`prd.md:37` 写“旧 Controller Session 失效不再卸载仍 ready 工作台”。
- `design.md:144-147` 同时规定网络/503 立即卸载、Controller restart 后重判。
- `implement.md:17`、`implement.md:42-47` 只写笼统的 restart fixture。

现有代码事实：

- Session authority 完全在进程内，新进程会创建新的 instance、bootstrap authority 和空 session map：`cmd/hostcontroller/main.go:102-108`、`internal/hostcontroller/session.go:35-55`。
- `zhixu restart` 直接调用 `run_up`：`zhixu:432-434`。`run_up` 会先停止 Controller，再停止/删除 Workspace runtime 和 grant override，之后才启动新 Controller：`zhixu:414-430`、`zhixu:316-323`。
- 新 Controller 的 `Reconcile` 会对 durable Active 重新 `ApplyGrant`，而 Compose driver 会 force-recreate API/Worker：`internal/hostcontroller/coordinator.go:99-145`、`internal/hostcontroller/compose_driver.go:103-126`。

所以应把验收拆成三类，避免实现和 E2E 各自解释：

1. **Controller Session 过期或 `/control/v1/session` 401，Controller/runtime 都存活：** 公开投影仍 ready；业务树和业务 Session 不卸载；控制页要求重新授权。
2. **Host Controller 进程短暂不可达/重启：** 按 `design.md:143-145`，网络失败期间业务树必须卸载；进程恢复并重新证明 ready 后可用原业务 Session 重挂载。这里不保证 UI 无中断。
3. **`zhixu restart`：** runtime 被明确撤销并重建，必须观察 unavailable/网络失败后再 ready；只保证 durable Active 恢复和业务 Session 不被主动清除，不保证 runtime 连续在线。

建议把 `prd.md:37` 改成“重启恢复后可用原业务身份重新进入；控制 Session 失效本身不驱逐一个仍由公开投影证明 ready 的工作台”，并在 T09 分别命名 session-expiry、controller-process-restart、launcher-restart（若都在范围）。若只测其中一种，也要在 AC10 中写清 fixture 语义。

### 4. [中] “同一 Workspace runtime 重建必经 unavailable”无法由当前三字段轮询合同保证

规划位置：`design.md:109-119`，尤其 `design.md:119`。

公开合同故意不暴露 grant generation、state version 或 Controller instance：`prd.md:25-26`、`design.md:64-68`。如果 runtime 在两次轮询之间完成 `ready(A) -> unavailable -> ready(A)`，浏览器只能看见两次相同的 `ready + A`，无法证明中间发生过重建，也就无法保证业务树完整卸载/重挂载。Controller/process restart 同样可能在轮询间隙完成。

建议二选一：

1. **推荐，若同一 Workspace cache 可继续复用：** 删除 `design.md:119` 的必然性表述，只保证“观察到非 ready、请求失败或 ID 改变时卸载”；A -> B 仍严格先清 A 后发布 B。
2. **若同 ID 重建必须强制隔离：** 合同需增加不透明、非敏感的 `runtime_epoch`/revision，且在 runtime generation 或 Controller runtime ownership 变化时改变；前端以 `(workspace_id, epoch)` 作为挂载身份。该选择会扩大 R4/R5 的公开字段，必须先修改 PRD，而不能在实现中偷偷加入。

### 5. [中] `/host` 命名空间需要显式顶层 dispatch，否则未知路径会回落 SPA

规划位置：`design.md:69`、`implement.md:10`。

现有 `Handler.ServeHTTP` 只专门分发 `/control` 与业务 API，其余路径全部交给静态 SPA：`internal/hostcontroller/http.go:76-87`；`Handler` 目前也只有 `control http.Handler` 子路由：`internal/hostcontroller/http.go:41-53`。

建议在实施清单明确沿用现有架构新增 `host http.Handler` 和 `hostRouter()`，并让顶层捕获 `path == "/host" || strings.HasPrefix(path, "/host/")`。不能只对精确 `/host/v1/runtime` 写一个 case，否则 `/host/v1/unknown`、`/host/` 会落到 SPA。测试至少覆盖：

- `/host`、`/host/`、`/host/v1`、`/host/v1/unknown` 返回固定 404 Problem，零 Store 调用，绝不含 HTML。
- POST/PUT/PATCH/DELETE/HEAD `/host/v1/runtime` 返回固定 405，零 Store/Runtime mutation。
- GET `/host/v1/runtime` 才读取状态。

这样与现有 `controlRouter` 的 custom NotFound/MethodNotAllowed 模式一致：`internal/hostcontroller/http.go:90-106`。

### 6. [中] Host、Problem 和轮询值还不是可执行的精确合同

规划位置：`design.md:62-69`、`design.md:151-153`、`implement.md:10-11`。

当前规划只说“generic/stable Problem”和“安全区间”，但没有固定：错误 code/message/retryable、wrong Host 的状态码、`poll_after_ms` 的服务端上下界或 clamp 规则。严格 TypeScript decoder 与 HTTP table test 无法据此判断正确实现。

现有 helper 的行为可复用但有一个明确陷阱：

- `responseProblem`/`jsonResponse` 已统一设置 `Cache-Control: no-store`：`internal/hostcontroller/http.go:427-461`。
- `cachedControllerError` 会把 `Fault.Message`、`FieldErrors`、`OperationID` 原样写入响应：`internal/hostcontroller/http.go:436-448`，因此公开 endpoint 不能把 Store/Runtime error 交给 `writeControllerError`。
- `requireHostAndOrigin` 同时要求 Origin，适用于 unsafe 控制请求：`internal/hostcontroller/http.go:352-360`。匿名安全 GET 应只做精确 `request.Host == expectedHost`，不要求 Origin，也不发 CORS allow header。

建议在设计中固定：

1. Host 校验必须在 Store/Runtime 读取前执行；wrong Host 返回一个固定 4xx Problem，不回显收到或期望的 Host。
2. 任意 Store/Runtime 内部错误统一映射成一个固定 503 code/message、固定 retryable 值，不包含 `operation_id`、`field_errors` 或底层文本。测试使用带 `/Users/private`、operation ID、field errors 的 `Fault` canary。
3. success、wrong Host、404、405、503 全部断言 `Cache-Control: no-store` 且不存在任何 `Access-Control-Allow-*`。
4. `active_workspace_id` 字段在三种 200 响应中始终存在；非 ready 明确序列化为 JSON `null`，不能用 `omitempty` 隐去。
5. 为 `poll_after_ms` 定义命名的服务端最小/最大值以及固定或 clamp 策略，并测试 0、负数和超大 Store 值。当前生产投影是 750ms，WaitingStore 是 2000ms：`internal/hostcontroller/coordinator.go:1003`、`internal/hostcontroller/types.go:153-166`；设计样例 1000ms 不能替代边界合同。

### 建议同步到规划文件的最小修改

| 文件/段落 | 建议 |
| --- | --- |
| `prd.md` R6、AC3、AC10 | 区分 session expiry、Controller process outage、`zhixu restart`；明确 outage 期间卸载、恢复后复用业务身份，不承诺无中断 |
| `design.md` 3.1 | 固定 success/error 精确 Schema、Host 检查顺序、Problem code/status、poll 上下界、整个 `/host` namespace 的 404/405 |
| `design.md` 3.2 | 明确共享的是 state predicate 还是包含 backend discovery 的 locator；给出完整矛盾状态矩阵和 availability 行为变化 |
| `design.md` 5 | 删除同 ID 重建“必经 unavailable”的不可观测保证，或正式增加 opaque runtime epoch |
| `design.md` 9 | 定义 `RUNTIME_NOT_READY` 后的本地 unavailable/退避，避免 endpoint 仍 ready 时立即重挂载循环 |
| `implement.md` T01/T02 | 增加 direct `StateBackend` tests、ready+backend failure、全 `/host` namespace、不泄漏 canary、poll clamp |
| `implement.md` T09 | 把 restart fixture 的具体事件、预期中断窗口与恢复断言写清 |

## Files Found

- `.trellis/tasks/08-01-public-dashboard-access/prd.md` - 需求、公开字段边界、重启与验收语义。
- `.trellis/tasks/08-01-public-dashboard-access/design.md` - `/host/v1/runtime`、共享 readiness、前端 authority、重启/缓存设计。
- `.trellis/tasks/08-01-public-dashboard-access/implement.md` - T01/T02/T09、验证命令与 review gate。
- `internal/hostcontroller/types.go` - `State`、`StateReader`、`StateBackend` 及最终 backend discovery。
- `internal/hostcontroller/http.go` - 顶层 dispatch、chi control router、Host/Origin、防泄漏 Problem helper 与业务代理。
- `internal/hostcontroller/coordinator.go` - durable snapshot 投影、20 秒 freshness、reconcile 与 runtime 矩阵。
- `internal/hostcontroller/compose_driver.go` - force-recreate、revoke 与 backend URL discovery。
- `internal/hostcontroller/session.go` - process-local Controller Session 生命周期。
- `internal/workspace/runtimegrant/runtime.go` - heartbeat 与正常关闭时 unavailable 转换。
- `internal/workspace/adapter/postgres/control.go` - runtime fresh 判定和完整 snapshot 读取。
- `internal/workspace/adapter/postgres/runtime.go` - availability/generation/phase 对 runtime 注册的授权约束。
- `cmd/hostcontroller/main.go` - HTTP 启动、异步 reconcile、新 Controller authority 创建。
- `zhixu` - launcher 的 `restart -> up -> revoke runtime -> start Controller` 顺序。

## Code Patterns

- 子命名空间由顶层前缀 dispatch 到独立 chi router，router 自己定义 NotFound/MethodNotAllowed：`internal/hostcontroller/http.go:76-106`。
- 所有 Problem/JSON response 通过统一 helper 写 `no-store`：`internal/hostcontroller/http.go:427-476`。
- 业务代理对任意 locator error 统一返回脱敏 `RUNTIME_NOT_READY`，不外泄底层错误：`internal/hostcontroller/http.go:384-410`。
- 公开 DTO 应从有效状态派生，而不是序列化含 root、operation、instance/version 的 `State`：`internal/hostcontroller/types.go:53-85`。
- generation/freshness 的原始事实只存在于 `ControlSnapshot`/`RuntimeRecord`，目前在 `projectState` 中折叠为 RuntimeState：`internal/workspace/domain/control.go:82-146`、`internal/hostcontroller/coordinator.go:939-948`。

## External References

- 仓库锁定 Go `1.25.4`、`github.com/go-chi/chi/v5 v5.3.1`：`go.mod:3-7`。
- 本次复核不需要外部网络资料；结论均来自当前 worktree 的规划、代码、测试和项目规范。

## Related Specs

- `.trellis/spec/backend/workspace-root-grant.md:41-50` - switch/recovery 顺序、Controller credential、effective Active 与 ready-only proxy。
- `.trellis/spec/backend/workspace-root-grant.md:54-66` - runtime mismatch、recovery failure、Host/Origin/CSRF 错误矩阵。
- `.trellis/spec/backend/workspace-root-grant.md:76-89` - Coordinator/HTTP/真实 Docker + Playwright 必测项。
- `.trellis/spec/frontend/state-management.md` - Controller state、Active Workspace、cache/SSE 生命周期；实施时需同步公开投影的新 authority 语义。

## Caveats / Not Found

- 本次是规划与静态代码复核，没有修改产品代码、规划文件或 spec，也没有启动 Docker/浏览器验证真实 restart 时间线。
- “fresh 记录存在但 backend 已消失”是由 20 秒 freshness、正常关闭可能失败及额外 `CurrentBackend` 门推导出的可达竞态；正常 API/Worker 退出会尽力把记录更新为 unavailable，因此不是每次 restart 都会出现。
- 当前没有 Host Controller 专用的真实 restart Playwright fixture；`implement.md` 仍需明确是重启 Controller 进程还是执行 `zhixu restart`。
- 没有独立 Host Controller OpenAPI；新合同的事实源必须落在 Go DTO/HTTP tests、TypeScript strict decoder tests 与 Trellis spec，三者需保持一致。
