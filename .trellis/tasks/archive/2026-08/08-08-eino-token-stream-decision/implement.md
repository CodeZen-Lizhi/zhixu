# Eino Token Streaming 正式迁移实施

1. 冻结 Answer draft session/frame、六状态状态机、generation+sequence SSE event、终态覆盖、TTL/size/auth 和 reset 合同。
2. 新增 migration、PostgreSQL repository、有界轮询读取与 cleanup；实现 `Begin/Append/Close` 对当前 Runtime Claim、NodeAttempt 和 fence 的事务/CAS 校验，完成 SQL review。`LISTEN/NOTIFY` 保留为可选优化，当前不作为已实现或可靠性前提。
3. 新增项目 DraftStream Port 和 tool-free Eino final-answer Stream collector；保证单 owner、最终 concat、所有路径 Close 与意外 ToolCalls fail closed。
4. 在 collector 与 PostgreSQL writer 间实现固定帧数/字节的非阻塞队列、合并批写和短超时；降级后停止 enqueue 但继续 drain 和 finalization。
5. 完整 drain ChatModelAgent `AsyncIterator[AgentEvent]` 及嵌套 MessageStream，不把 Agent 中间轮发送到 draft sink；再由结构化节点生成不含正文的 hash-bound metadata envelope，项目确定性组合正式 RAG v2。
6. 将 Answer finalizer 与 draft session 收口：成功提交同事务转 PUBLISHED，门禁失败/拒答/澄清转 ABORTED；新增 Answer-scoped SSE handler/OpenAPI，支持 phase=`ANSWER`、generation-aware cursor/reset，保留 durable `/events` 不变。
7. 新增前端 strict decoder、draft reducer 和 `/chat` pending Answer 展示。
8. 覆盖断连/重连、通知丢失、API 重启、旧 Worker lease reclaim、双 Worker、旧 cursor、EOF 后 Citation/Faithfulness 失败、COMPLETED 后重连、finalizer response-loss、取消、慢数据库、队列满、TTL、超限，以及长流内存/goroutine 有界测试。
9. 运行真实 Provider、Worker、API 和桌面/移动浏览器端到端 smoke，记录两端多帧、首次草稿可见延迟和完整完成延迟；Provider 首 Token 延迟由 Worker 指标单独记录，不能用浏览器草稿观察延迟冒充。
