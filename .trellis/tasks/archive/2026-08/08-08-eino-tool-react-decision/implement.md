# Eino Tool Calling 与 Agent 正式迁移实施

1. 以现有 `/chat` RAG 冻结终态和只读工具白名单；不新增通用 Agent 产品入口。
2. 定义项目 AgentRuntime DTO/Port、NodeAttempt 级共享 Budget Ledger 和稳定错误；增加 Eino import boundary。
3. 扩展 ModelCall domain/SQL/repository：可重复 `AGENT` phase、合法前驱、流式 terminal、canonical tool-call hash 和硬调用上限。
4. 新增 Eino ToolCalling Chat adapter 与 recording model，验证流式 tool-call ID/name/arguments 合并与严格错误映射。
5. 用经典 ChatModelAgent/ToolsNode 实现 Eino AgentRuntime，关闭 retry/failover 和工具并行；唯一 consumer 必须持续 drain `AsyncIterator[AgentEvent]`，并消费、关闭每个嵌套 MessageStream。
6. 从项目 Catalog 生成 ToolInfo，并将 wrapper 唯一连接到 `ExecutionService`。
7. 多 call number、受限 provider call ID 校验/临时关联、同 Attempt result receipt/replay 和 lease reclaim 下的新 Attempt 只读重做已完成。`SearchKnowledge` 的 durable result receipt 是独立开放门禁；完成前继续不进入模型 allowlist，不阻塞当前 `ReadSource@2`/`ValidateCitation@2` 产品路径。
8. 接入 RAG Graph，并在 Agent 后增加不绑定工具的 Eino final-answer Stream；此阶段可先在 Worker 内完整消费，浏览器 draft sink 由 Streaming 子任务接入。
9. 执行共享预算、真实 PostgreSQL/River、Provider、取消、重投递、lease reclaim 和 response-loss 测试。
10. 删除已无消费者的自定义 ToolRequest 调度，更新安全/架构/运维文档。
