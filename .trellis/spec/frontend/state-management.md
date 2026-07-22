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
- Workspace 切换先停止旧连接/请求，再清理旧 Collection/Health cache；旧回调不得写入新 Workspace。
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