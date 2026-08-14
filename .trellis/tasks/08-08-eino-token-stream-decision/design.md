# Eino Token Streaming 正式迁移设计

## 1. 数据流

```text
ChatModelAgent.Run -> drain internal AgentEvent/MessageStream
       -> tool-free eino-ext final-answer Stream -> Worker collector
       -> bounded/coalesced draft frames -> PostgreSQL short-lived log
       -> authenticated Answer SSE -> frontend draft reducer
       -> durable answer event -> REST refetch -> final Answer replaces draft
```

## 2. Draft Store

Draft session/frame 必须绑定 Workspace、Answer、Workflow Run、NodeAttempt、lease fence、generation 和单调 sequence。Session 状态固定为 `ACTIVE | COMPLETED | DEGRADED | PUBLISHED | ABORTED | SUPERSEDED`，并限制单帧和单 Answer 总字节，设置短 TTL，由有界 cleanup 删除。正文不进入日志、metric、outbox 或 `ops.server_event`。

`BeginDraftSession` 在一个数据库事务内用数据库时间确认 NodeAttempt 仍是当前 lease owner/fence，原子把该 Answer 的旧 ACTIVE/COMPLETED/DEGRADED session 标记为 SUPERSEDED，并创建更高 generation；每个 append 和 close 都以允许的源状态、Attempt、fence 及当前 Runtime Claim 做 CAS。唯一未终结 session 由数据库约束保护。旧 Worker 在 lease reclaim 后只能停止发布和继续清理自己的本地 stream，不能再写 frame 或关闭新 generation。

状态迁移固定为：`ACTIVE -> COMPLETED`（Provider EOF，待门禁）、`ACTIVE -> DEGRADED`（停止草稿传输但正式生成继续）、`ACTIVE/COMPLETED/DEGRADED -> ABORTED`（取消、验证失败或终态失败）、`COMPLETED/DEGRADED -> PUBLISHED`（与最终 Answer 原子提交同一事务）、未终结状态到 `SUPERSEDED`（新 Attempt 接管）。`PUBLISHED/ABORTED/SUPERSEDED` 不允许继续 append。finalizer response-loss 通过已提交 Answer + PUBLISHED 状态幂等回读，不重新发布正文。

当前实现使用有界轮询读取 PostgreSQL，不依赖 `LISTEN/NOTIFY`。API 每次都从表按 generation+sequence 读取，因此多实例和 API 重启不丢恢复能力；后续若加入通知，也只能作不含正文的可选 wakeup。API 只有在 session 仍绑定当前 Runtime Claim 时，才在 ACTIVE/COMPLETED/DEGRADED 返回当前 generation 的草稿；lease 失效而新 Attempt 尚未 begin 时也 reset/end。PUBLISHED 只发 end/refetch，ABORTED/SUPERSEDED 只发 reset/end。`Last-Event-ID` 携带 generation，客户端提交旧 generation cursor 时收到 reset 和当前 watermark，而不是把两次 Attempt 的正文拼接。

## 3. Stream Collector

执行器先以唯一 consumer 持续读取 `ChatModelAgent.Run` 返回的 `AsyncIterator[AgentEvent]`，并拥有、消费和关闭每个 `event.Output.MessageOutput.MessageStream`。该 iterator 内部是无界通道，因此读取路径不能等待 draft store；Agent assistant/tool 中间流、ToolCalls 和参数只在 Worker 内部合并，不对外发布。

Agent 结束后，Graph 发起一个不绑定工具的 Eino `ChatModel.Stream` 作为唯一最终正文流。只有一个 owner 消费该 StreamReader，同时：

- 合并完整终止消息供后续结构化/最终校验；
- 拒绝该 tool-free 调用意外返回的 ToolCalls；
- 把 delta 非阻塞写入固定帧数和字节数的内存队列，由独立 writer 合并过小 Token、使用短超时批量写 draft；
- 在队列满、writer 超时或 draft sink 失败时 CAS 标记 DEGRADED、停止 enqueue，但继续消费并完成最终结果；
- 在所有路径显式 Close。

队列和最终正文均有独立硬字节上限；writer 变慢不能反压 Provider，也不能让 Eino 的无界 iterator、项目队列或 goroutine 无限制增长。不实现增量 JSON 字符串抓取。final-answer Stream 输出普通 Markdown；之后由独立 Eino structured node 生成不含正文、只含正文 hash 和结构字段的版本化 metadata envelope，项目再确定性组合正式 RAG v2，避免手写半截 JSON parser、向用户展示原始 JSON 或让第二次模型调用改写已流出的文本。

## 4. HTTP 与前端

新增 Answer-scoped SSE，认证和 Workspace 绑定与 Answer 查询一致。事件仅表达 phase=`ANSWER` 的 draft delta/reset/end，不承载最终 Answer。前端使用独立 reducer 管理 attempt、generation、sequence、text 和连接状态；终态事件触发 REST 回查并清除 draft。

## 5. 失败语义

- draft transport 失败：记录脱敏 degradation，最终 Answer 可继续。
- model stream 失败：当前 ModelCall 失败，由 Workflow retry policy 决定新 Attempt；旧 draft reset。
- lease reclaim：新 Attempt 的 begin 原子 supersede 旧 session；旧 worker 的后续 append CAS 失败并停止发布。
- validation/refusal/clarification：同一 finalization transaction 把 session 转为 ABORTED，SSE reset/end 且不再返回正文，以最终 REST 事实为准。
- HTTP 断连：只关闭订阅；不取消 Worker。
