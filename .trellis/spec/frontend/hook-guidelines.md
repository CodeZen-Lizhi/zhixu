# 前端 Hook 规范

> 将 Hook 定义为组件与稳定应用边界之间的适配层。

## 适用范围

适用于 Query、Command、URL State、Draft 和 SSE 生命周期 Hook。仓库当前没有 Hook 实现，M1 必须用真实代码验证命名、位置和测试模式。

## 已确认事实

- `docs/architecture/frontend-architecture.md` 将 Server State 交给 TanStack Query，将可分享的筛选、排序、当前对象和图谱中心节点交给 URL。
- Local Draft 仅保存 Proposal 编辑、Diff 选择、Artifact 大纲等交互状态；权威 Proposal Revision 由服务端持久化。
- SSE 报告 Workflow 和 Index 事件并触发定向 Query Invalidation，不是事实源。
- `docs/architecture/api-and-events.md` 要求通过 Last-Event-ID 重连；事件超出保留窗口时重新查询资源。

## Hook 职责

- Query Hook 定义稳定 Query Key，调用强类型 API 边界，并按需返回 Domain UI Projection。
- Command Hook 负责同一逻辑提交复用 Idempotency Key、传入 Version/ETag、处理 Mutation 生命周期和定向失效。
- URL Hook 通过唯一 Schema 解析和序列化可分享状态。
- SSE Hook 管理连接生命周期，并委托 `events` 边界处理 Envelope 解码和失效映射。
- Local Interaction Hook 可协调焦点、选择或未保存编辑状态，但不得成为第二套 Server Cache。

## 数据查询与命令

- Query Key 必须包含 Workspace 边界和所有影响结果的输入。
- 分页遵守服务端 Cursor 契约，组件不得为 Cursor Endpoint 自创 Offset/Page 语义。
- Mutation 成功只失效相关 Query Family，禁止默认清空全部 Cache。
- Workspace 切换或登出时清理 Workspace 范围缓存。
- Source 全文和敏感 Evidence 不得长期缓存。
- `202` 只表示 Workflow 已接收，不表示完成；Hook 暴露 Workflow Reference，由持久化 Workflow Query 决定进度。
- Retry 遵守服务端 Retryable/Error 契约，不盲目重试非幂等命令。
- Review 提交等受保护命令的同一逻辑重试必须复用 Idempotency Key。

## 命名和接口

- 自定义 Hook 以 `use` 开头，按产品操作命名，不按传输机制命名。
- Query/Command Key 和常量在 Feature 或明确共享边界中只有一个 Owner。
- 可选输入较多时使用小型 Options Object，避免过长位置参数。
- 返回底层 Query/Mutation 状态及少量派生值，不得用布尔值隐藏错误详情。

## 禁止模式

- TanStack Query 已负责 Server State 时，默认用 `useEffect` 拉取数据。
- 仅为渲染而把服务端结果复制到 Local State。
- 在多个 Hook 中重复解析或强转原始 API/SSE 字段。
- 仅凭 SSE 到达判断持久化完成，不重新查询权威状态。
- 每个组件创建独立 EventSource，或 Scope 变化时不关闭订阅。
- 同一命令重试时生成新的 Idempotency Key。
- Query Key 缺少 Workspace ID。
- 通过关闭依赖告警掩盖未设计清楚的 Effect 生命周期。

## 验证

M1 前执行：

```bash
rg -n 'TanStack Query|URL State|SSE|Last-Event-ID|Idempotency' docs/architecture/frontend-architecture.md docs/architecture/api-and-events.md
git diff --check
```

M1 后，Hook Test 必须覆盖 Query Key 隔离、Cursor 推进、Mutation Invalidation、幂等键复用、SSE 重连、事件窗口过期、取消和清理。

## M1 待代码验证

M1 必须建立 Query Key Factory、Generated Client Wrapper、URL Schema、EventSource 实现、取消策略和 Hook Test Harness，之后用真实 Hook 链接替代规划描述。

## M6-04 RAG Query 与恢复约束

- Conversation/Turn 使用 Workspace scoped Infinite Query；活动时间重排或 cursor-expired 时必须先把 Conversation
  列表 exact reset 到第一页，再沿新 cursor 加载，禁止用旧 cursor 重查多页。
- Turn 首屏分页不能承担“最新执行状态”恢复；使用 `latest=true` 的单条有界 Turn 投影找到最新 Answer，
  并按 Question ID 替换分页中的旧投影。
- SSE 断线时 pending Answer 最多轮询 12 次、每次 2 秒；成功与失败请求都消耗预算，Answer ID 变化才重置。
- Create/Question/Feedback 的一次逻辑重试复用 Idempotency-Key；成功后才生成下一命令 Key。
