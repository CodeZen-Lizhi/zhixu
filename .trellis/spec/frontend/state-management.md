# 前端状态管理

> 为每类前端状态指定唯一 Owner。

## 适用范围

适用于 React 应用持有或观察的状态。当前没有状态管理代码，M1 必须验证具体实现并保持以下所有权规则。

## 已确认事实

`docs/architecture/frontend-architecture.md` 定义四类状态：

1. Document、Proposal、Workflow、Graph 和 Collection 等 Server State 由 TanStack Query 管理。
2. Filter、Sort、Current Object 和 Graph Center Node 等 URL State 由 Router 管理。
3. Proposal 编辑、Diff 选择、Artifact 大纲等 Local Draft 由局部交互管理。
4. Workflow Progress、Human Task 和 Index Activation 等 Event State 由 SSE 通知；SSE 只触发查询失效，不是权威存储。

具有业务意义的 Proposal/Diff Draft 必须持久化为服务端 Proposal Revision；页面刷新后必须能通过 Workflow ID 恢复状态。

## 所有权规则

| 状态分类 | Owner | 持久化与恢复 |
| --- | --- | --- |
| Server Resource | TanStack Query + Typed API | 从 API 重新查询，按 Workspace 隔离 |
| 可分享导航 | React Router URL/Search Params | 通过导航、刷新和 Deep Link 恢复 |
| 本地交互 | 最近的 Component/Feature Hook | 默认可丢弃，必要时提升为 Server Revision |
| 异步事件信号 | Shared SSE Event Store | Last-Event-ID 重连后重新查询资源 |
| 表单校验 | Form/Component 边界 | 由强类型输入和服务端错误派生 |

Global Client State 只用于真正全局且非服务端的关注点，例如 UI Shell Preference 或共享 SSE 连接。引入其他 State Library 必须通过 ADR 或 M1 设计更新证明 React State、URL State 和 TanStack Query 不足。

## Server State

- Query Key 包含 Workspace ID 和稳定的 Resource/Filter 输入。
- Workflow、Approval、Proposal、Review Schedule、Index Activation 和安全决策以服务端为准。
- Mutation 使用服务端 Version/ETag 和 Idempotency 契约。
- Approval、Write Authorization、Git/File State 等不可逆或安全敏感操作禁止 Optimistic Update。
- SSE Handler 失效或刷新资源，不在浏览器重放第二套业务状态机。
- 流式 RAG 的最终 Answer 使用服务端校验结果，不使用临时 Token Stream 作为事实。

## 派生与草稿状态

- 低成本派生状态直接从 Owner 计算，不用 Effect 同步副本。
- 多步 Diff 选择等非平凡局部转换由 Reducer 统一负责，并穷尽处理 Transition 值。
- 未保存 Draft 必须清楚标识；需要跨刷新、审批或恢复的 Draft 通过服务端命令提升。
- 需要分享或浏览器历史的 Filter、Sort、Graph Center 和 Selected Resource 写入 URL。

## 禁止模式

- 单一可变 Global Store 混放 Server Data、URL State 和 Local Draft。
- 将 SSE Payload 作为唯一 Workflow 或 Index State。
- 没有 Draft 边界却把 TanStack Query Data 复制到 Local State。
- 在 Browser Storage 保存 API Key、Authorization Header 或敏感 Source Text。
- Cache Key 缺少 Workspace Scope。
- Client Success State 掩盖服务端失败或 Unknown Operation。
- 为逃避定义 Feature Interface 而引入 Global State。

## 验证

M1 前执行：

```bash
rg -n 'Server State|URL State|Local Draft|Event State|SSE 事件' docs/architecture/frontend-architecture.md
git diff --check
```

M1 后，测试必须证明刷新恢复、Workspace Cache 隔离、URL Round Trip、SSE 重连/回退查询，以及受保护写入没有虚假 Optimistic Success。

## M1 待代码验证

M1 必须记录实际 Query Default、Cache Retention、URL Parsing、Local Draft Reducer、SSE Connection Owner 和 Browser Persistence；Manifest 和代码出现前不得推断额外 State Library 或存储策略。

## M6-04 RAG Event Recovery Contract

- SSE 只提供 typed invalidation hint，不保存 Answer、Workflow 或 current stage 业务终态；刷新与断线恢复必须
  回查 Conversation/Turn/Answer Query。
- 409 expired cursor 必须先完成 Workspace 范围的权威资源回查，成功后才清除 cursor 并无游标重连；
  回查失败保留 cursor，避免把恢复失败伪装成成功。
- `onEvent` 可以异步；只有 handler 成功完成才提交 cursor，失败时重连必须重放同一事件。
- 400 invalid/future cursor 停止自动重连；网络失败使用有界抖动退避，Abort 必须释放 reader 与 fetch。

## Scenario: M7-03 Collection / Knowledge Health State Ownership

### 1. Scope / Trigger

- 修改 Collection/Health Query、Mutation、URL state、Workspace cache、SSE invalidation 或 scan recovery 时应用。

### 2. Signatures

- TanStack Query key：`['collections', workspaceId, canonicalRequest]`、`['collection-results', workspaceId, collectionId, version, queryHash, request]`、
  `['health', workspaceId, canonicalRequest]`。
- URL 只承载 view/filter/sort/group/selection/scan ID；opaque result cursor 仅由 Query 分页持有。

### 3. Contracts

- Collection/Health REST response 是 Server State 唯一事实源；SSE 只触发对应 Query invalidation。
- Workspace 切换先停止旧连接/请求，再清理旧 Collection/Health/business cache；旧回调不得写入新 Workspace。
- LIST/TABLE/COMPACT_CARD 不复制结果 membership；Evidence、Decision、Schedule pending 状态不写入全局 store。
- Saved Collection list 必须消费服务端 `next_cursor`；Workspace、Collection、version、query hash 或 Issue filter
  变化时，下一次请求的 cursor 必须为 undefined。
- Health completion event 同时 invalidates Health 与 Collection result family；所有 invalidation 成功后才推进 SSE cursor。

### 4. Validation & Error Matrix

| 条件 | 结果 |
|---|---|
| query hash/version/revision 不一致 | Query 进入 stale/error，重新从第一页加载 |
| invalid/stale cursor | 清理分页状态并显式恢复第一页 |
| mutation 409/response-loss | 保留服务端错误或使用同 key 回查，不 optimistic 成功 |
| Workspace switch | 旧 cache/事件回调不可见 |

### 5. Good / Base / Bad Cases

- Good：SSE `health.scan.completed` 只 invalidate scan/issues/summary，REST 回查后更新页面。
- Base：没有 Active Workspace 时不发请求，显示明确 empty/unavailable。
- Bad：把 scan event payload 直接写成 succeeded、把 cursor 放 URL/localStorage 或跨 Workspace 复用 Query key。

### 6. Tests Required

- Query key canonicalization、URL round-trip、cursor recovery、Workspace cache isolation、SSE invalidation、response-loss mutation。

### 7. Wrong vs Correct

```text
Wrong: 收到 SSE 后直接 setScan({status:'SUCCEEDED'})。
Correct: 仅 invalidation，重新 GET Scan/Issue projection 后再渲染终态。
```

## Scenario: M9 Unified Workspace SSE Event Store

### 1. Scope / Trigger

- 修改 App Shell、SSE connection lifecycle、Last-Event-ID、Query invalidation/recovery 或 Workspace 切换时应用。
- 全站每个 Active Workspace 只有一个连接 Owner；Feature 不得直接调用 `connectServerEvents` 或创建 EventSource/fetch stream。

### 2. Signatures

```ts
<EventStoreProvider>
useEventStore(): { state; lastEventId?; lastEvent? }
useRegisterWorkspaceRecovery((workspaceId) => Promise<void> | void)
connectServerEvents({ workspaceId, lastEventId?, onEvent, onRecoveryRequired, ... })
```

- `web/src/events/event-store.tsx` 拥有连接；`server-events.ts` 只拥有严格 frame/envelope decoder 和可取消 connector。
- sessionStorage key 固定按 Workspace 保存 Last-Event-ID，不保存事件正文或业务对象。

### 3. Contracts

- Server State 仍由 TanStack Query/API 拥有；SSE 只发 typed invalidation hint，Event Store 不重放 Proposal/Workflow/RAG 状态机。
- `onEvent` 的所有 Query 失效成功完成后才提交 cursor；任一失效失败必须让同一事件在重连后重放。
- 409 expired、400 invalid/future cursor 都先完成 Workspace 权威 Query 回查；成功后清 cursor 并无游标重连，失败保留 cursor 并进入 `recovery_failed`。
- Search 的 opaque cursor 绑定 Active Index 和结果 fingerprint，恢复时禁止用 `refetchQueries(type:"all")` 重放旧窗口。Event Store 先取消并移除 Workspace 下全部 Search Query；当前挂载的 Search Feature 通过 recovery callback 清除 URL cursor，并以原 query/mode/filter 回查无 cursor 首屏。首屏回查失败必须拒绝整个恢复并保留 SSE cursor。
- Search reset 与当前页面 recovery callback 必须先于 Workspace/business 等其他网络回查；后续任一回查失败时旧 cursor 窗口仍保持移除，但 SSE cursor 必须保留。首屏恢复只有一个 Query 请求所有者，callback 必须等待该最终请求成功，不能在随后自动 refetch 失败前提前 resolve。
- Workspace 切换先关闭旧连接，再移除旧 Workspace 的 Workspace/business/RAG 及已注册 Query family cache；旧连接回调不得覆盖新 Workspace 状态。
- 同一个事件对同一 Query family 最多执行一次失效；事件 type、resource ref 与 typed invalidation 只是同一失效判定的不同证据，不能造成重复请求。
- `connecting|open|reconnecting|recovery_failed|closed` 只描述连接，不代表 Workflow/Approval 业务终态。

### 4. Validation & Error Matrix

| Condition | Required result |
|---|---|
| Event Workspace 不匹配、ID 非单调或 payload 非法 | connector fail closed，不提交 cursor |
| Query invalidation 失败 | onEvent reject，session cursor 保持旧值 |
| expired/invalid/future 回查成功 | 删除 Workspace cursor，无 cursor 重连 |
| 权威回查失败 | 保留 cursor，显示 recovery_failed，不伪装已恢复 |
| Search 存在失效的第二页 cursor | 不执行旧 cursor Query；移除旧窗口，挂载页面以无 cursor 首屏回查后才允许清 SSE cursor |
| Search 首屏回查失败 | 保留 SSE cursor 与 recovery_failed；重试不得重新执行已移除的旧 cursor |
| Workspace A→B | A connection close；A 回调无效；B 只建立一个连接 |
| 本地 cursor 损坏 | 先权威回查，再以无 cursor 建立唯一连接 |

### 5. Good / Base / Bad Cases

- Good：一个事件对每个匹配的 Query family 只失效一次后提交 ID；Search recovery 丢弃旧 cursor 窗口并回查规范首屏；切换 Workspace 时旧连接和旧 Workspace-scoped cache 立即失效。
- Base：无 Active Workspace 时状态 closed 且不建立连接；网络断开按有界抖动重连。
- Bad：RAG/Proposal 各建一条连接；收到 SSE 直接写业务 cache；对包含旧 cursor 的全部 Search Query 做 refetch；回查失败仍删除 cursor；旧 Workspace 回调覆盖新状态。

### 6. Tests Required

- 单连接、Workspace switch/cleanup、旧回调隔离、事件 Workspace/单调 ID、Query 失效失败不提交、expired/invalid/future、损坏本地 cursor、recovery_failed 和 Abort cleanup。
- Search recovery 必须证明：旧 cursor query 不再执行且缓存被移除；挂载页面 URL cursor 清除并以同一 query/mode/filter 回查首屏；首屏失败时 SSE cursor 不清除且重试不复活旧窗口。
- 浏览器用真实 API 检查同一 Workspace 网络中无重复 SSE；console 无 warning/error；切换后旧页面事实不可见。

### 7. Wrong vs Correct

```text
Wrong: Feature 收到 workflow.updated 后直接把本地 Run 标为 succeeded，并立即保存 Last-Event-ID。
Correct: 先失效 Workspace-scoped Query 并完成权威读取链，再提交 Event ID；Run 终态只来自 API。

Wrong: cursor invalid 时先删除 sessionStorage，再尝试回查。
Correct: 回查成功后才删除；失败保留旧 cursor 并显式 recovery_failed。

Wrong: SSE 超窗恢复时 refetch Workspace 下所有 Search page，包括绑定旧 Index fingerprint 的 cursor。
Correct: 先取消并移除 Search 窗口；挂载页面通过 recovery callback 只回查无 cursor 的规范首屏，成功后才提交恢复。
```

## Scenario: M10 Browser Authentication State Ownership

### 1. Scope / Trigger

- 修改 `web/src/api/auth.ts`、`auth-context.tsx`、认证页、应用根边界或受保护请求包装时应用。

### 2. Signatures

```ts
authFetch(path, init?): Promise<Response>
<AuthProvider><AuthBoundary>{children}</AuthBoundary></AuthProvider>
useAuth(): { state, signIn, signOut, refresh }
```

### 3. Contracts

- HTTP Cookie 由浏览器保存且始终使用 `credentials: "include"`；客户端不读取或持久化 Session Cookie 与 Bootstrap Token。
- `authFetch` 只对 Cookie 身份的 unsafe 请求补 `X-CSRF-Token`；带 Bearer 的自动化请求不伪造 CSRF。`401` 必须触发 Auth 失效而不是保留受保护 Query cache。
- `AuthProvider` 以 `/api/v1/system/status` 判定 `disabled|required|unavailable`，并在匿名、失效或存储不可用时清理 Query cache。前端状态不证明 Capability 或 Approval。
- 所有认证 success/error body 在 `web/src/api/auth.ts` 从 `unknown` 严格解码；`Problem` 必须只有允许字段，且 `error_code`、`message` 非空、`retryable` 为 boolean，optional `workflow_run_id` 为 UUID、`details` 为 object。可选 Session/API Token 时间字段存在时仍须是有效 RFC3339 日历时间。Token 创建响应只在调用点使用一次明文，后续列表只能消费元数据。
- 所有受保护 REST 请求必须经过 `authFetch`；组件不得用原始 `/api/v1/...` 新标签链接绕过 `401` 失效通知、`VITE_API_BASE_URL` 和 Query cache 清理。

### 4. Validation & Error Matrix

| 条件 | 必须结果 |
| --- | --- |
| `401` 或注销成功 | 清理 CSRF/Query，进入 anonymous，不显示旧 Workspace 事实 |
| 认证状态服务不可用或 CSRF Storage 不可用 | 显式 error/retry，不伪装已认证 |
| success/Problem 字段缺失、未知、`retryable` 非 boolean、optional UUID/时间/details 不合法 | `AuthApiError(INVALID_RESPONSE)`，不渲染部分认证事实 |
| development `disabled` | 渲染开发模式提示；不能由 UI 放宽服务端 Capability |

### 5. Good / Base / Bad Cases

- Good：Bootstrap 输入只在提交时进入认证 API，成功后由 HttpOnly Cookie 建立会话，UI 只保留 CSRF 派生值。
- Base：刷新后先读取系统认证模式，再恢复当前 Session；未登录时不挂载业务工作台。
- Bad：把 Bootstrap/API Token 放入 localStorage、仅因 localhost 绕过 AuthBoundary、401 后继续展示旧 Query、由组件自行拼接认证 Header，或直接打开受保护 `/api/v1` 链接而绕过 Auth 失效路径。

### 6. Tests Required

- API 单测覆盖严格 Session/API Token decoder、Problem（未知字段、非 boolean retryable、错误 UUID/时间/details）、401 失效、unsafe CSRF 注入与 Bearer 优先级；组件测试断言不保留原始受保护 API 跳转。
- Context/App/Shell 测试覆盖 required 匿名、登录、登出、disabled、storage 失败和受保护内容隔离。
- 真实浏览器在桌面与 `390x844` 下验证 disabled 模式工作台/Settings 可用、无 console error、无文本重叠；required 模式由 Compose smoke 验证 Cookie、CSRF、Token 生命周期。

### 7. Wrong vs Correct

```text
Wrong: 把 Bootstrap Token 或 API Token 放进 Browser Storage，并用它判断页面是否已登录。
Correct: Bootstrap 只用于一次交换；浏览器身份由 HttpOnly Cookie 建立，前端只维护可清除的 CSRF 与可查询的 Session 元数据。
```
