# Eino Token Streaming 正式迁移

## Goal

从 eino-ext Provider `Stream` 经 Eino Agent/Graph、Worker、跨进程传输和 API SSE 贯通到真实前端消费者，提供多帧生成中草稿，同时保持 PostgreSQL 最终 Answer 为唯一权威事实。

## 当前状态

Eino tool-free final `Stream`、PostgreSQL draft session/chunk、独立 Draft SSE、前端 draft reducer 和 Finalizer 原子 `PUBLISHED` 已接入 `/chat`。2026-08-12 当前树以外部 OpenAI-Compatible Chat host relay + 原生 Ollama Embedding 实际完成 Provider→Worker→PostgreSQL draft→API SSE→桌面/移动浏览器的多帧草稿、metadata、REVIEW 和正式 Answer 替换闭环；host-relay 只表示测试网络路径，不冒充容器直连外部 HTTPS。

## Requirements

- 使用真实 eino-ext `ChatModel.Stream` 生成不绑定工具的最终正文；不得以 `Invoke`、Collect 后单帧或模拟 chunk 作为生产实现。ChatModelAgent 顶层实际返回 `AsyncIterator[AgentEvent]`，其中的嵌套 `MessageOutput.MessageStream` 只作内部 Agent 消费，不直接暴露给浏览器。
- 消费者已冻结为现有 `/chat` pending Answer；不新建通用 Agent 页面。
- 新建独立 Answer draft SSE，不复用 `/api/v1/events`，不把 Token 正文写入 durable Server Event。
- Worker/API 分进程，通过有界、短 TTL、可按 generation+sequence 恢复的 PostgreSQL draft log 传递；session 状态固定为 `ACTIVE | COMPLETED | DEGRADED | PUBLISHED | ABORTED | SUPERSEDED`，所有 begin/append/close/finalize 都受 NodeAttempt lease fence 保护。可用 LISTEN/NOTIFY 仅作唤醒，不能作为唯一数据通道。
- Tool-call 参数、中间轮消息、Prompt、Evidence 原文和未脱敏错误不得流给浏览器；Agent event 和嵌套 MessageStream 永不对外发布，只流来源 phase=`ANSWER` 的 tool-free final-answer Stream assistant text。
- 最终结构化 Answer 必须绑定完整草稿 hash，再经过 Citation/Faithfulness 和原子 finalizer；`COMPLETED` 仅代表 EOF 待验证，最终 Answer 提交与 session=`PUBLISHED` 同事务。失败、拒答或澄清时转 `ABORTED`，SSE reset/end 且正文不再可恢复。
- 浏览器断连不取消异步 Workflow；显式 Workflow cancel/Node deadline 才取消 Eino context。
- StreamReader 和 callback copy 在 EOF、错误、取消、提前退出时全部关闭。
- final stream consumer 与 PostgreSQL writer 之间只能使用固定帧数和字节数的非阻塞队列；队列满或数据库超时时降级草稿、继续 drain Provider stream 和生成最终 Answer，不得阻塞或无限缓存。

## Acceptance Criteria

- [x] 当前待发布工作树由真实 Provider→Worker→PostgreSQL draft log→API SSE→桌面/移动浏览器收到至少
  两个连续非空 SSE chunk 事件，并完成 metadata、REVIEW 与正式终态替换；2026-08-12 host-relay 外部 Chat + 本地 Ollama Embedding 完整 smoke 通过，最终指纹化网络扫描版本的桌面/移动分别在正式发布前观察到 2/8 个 chunk 事件。
- [x] `BeginDraftSession` 原子校验当前 Runtime Claim 并 supersede 旧 generation；旧 Worker 在 lease reclaim 后无法 append；`Last-Event-ID`、TTL、Attempt 切换、EOF 后门禁失败和 finalizer response-loss 均有确定恢复/reset 语义。
- [x] 慢客户端、慢数据库、缓冲区满和客户端断开不会阻塞或重复 Provider 调用；队列、内存和 goroutine 有界，降级后最终 Answer 仍可完成。
- [x] cancel、deadline、Worker shutdown、Provider stream error 和 malformed chunk 均关闭 reader 且不泄漏 goroutine。
- [x] pending 草稿与 completed/refused/clarification Answer 在前端状态中严格分离；最终 REST 结果完全替换草稿。
- [x] draft 内容受 Workspace/auth、大小、TTL、清理和日志脱敏约束，SQL 与安全审查通过。

## Superseded Decision

旧 `research/product-gate.md` 已证明 Eino 框架流能力，但因当时没有入口而判定生产 No-Go。用户现已要求正式流式输出，本任务必须交付真实端到端消费者，PoC 不再算完成。
