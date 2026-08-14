# Eino Tool Calling 与 Agent 正式迁移设计

## 1. 边界

```text
Business Workflow / claimed NodeAttempt
  -> project AgentRuntime request + trusted identity + budget
  -> Eino ChatModelAgent (classic Message)
  -> eino-ext ToolCallingChatModel.WithTools
  -> Eino ToolsNode
  -> project Tool adapter
  -> ExecutionService -> PostgreSQL ToolCall receipt -> typed executor
```

Eino 只决定“下一轮是否请求哪个允许工具”，不决定调用者身份、权限或写授权。

## 2. Adapter

- `internal/agent/application` 定义 `AgentRunRequest/Result/Event` 与 `AgentRuntime`。
- `internal/agent/adapter/eino` 构造 ChatModelAgent、转换项目消息/工具/终态，并作为唯一 consumer 完整消费 `AsyncIterator[AgentEvent]` 及其中的 `MessageOutput.MessageStream`。
- `internal/platform/models` 提供只在 Eino adapter 内可见的 ToolCallingChatModel，复用 hardened transport。
- project tool wrapper 实现 Eino Tool interface，但只保存项目 `ExecutionService` 和当前 run context；输出被标记为 untrusted data。
- 现有 RecordingChatModel 的持久生命周期抽成不依赖 Eino 的 recorder；Eino recording model 实现 Generate/Stream/WithTools，并为每轮写 `AGENT` ModelCall。Provider tool-call ID 仅在本次对话内关联 ToolMessage，持久 Tool 身份使用项目 call_no/ID。

## 3. 工具与预算

首版顺序执行。`/chat` 建议白名单为 `ReadSource`、`ValidateCitation`；`SearchKnowledge` 在补齐 durable result receipt 后加入。模型不得调用 Git/write/index maintenance 工具。

NodeAttempt 只创建一本项目 `RunBudgetLedger`，字段至少包括 `MaxTotalModelCalls`、`MaxAgentIterations`、`MaxToolCalls`、总输入/输出 Token、deadline、单工具 timeout，以及下游各 phase 的 `ReservedInputTokens/ReservedOutputTokens`。ModelCall SQL 允许重复 `AGENT`，但每轮开始前 Ledger 必须预占该轮上限，并证明 ANSWER、最多三次 metadata envelope 和 REVIEW 的调用与 Token reservation 仍完整；完成后按真实 usage 结算，usage 缺失时扣预占上限。ADK `MaxIterations` 取本次授权值，内部 retry/failover 关闭，不能形成第二预算源。

ChatModelAgent 的所有 event 都是内部执行数据。ADK 在一个 assistant 流开始时不能保证该轮最终没有 ToolCalls，因此 Agent stream 不直接发布到用户；Agent 结束后，同一 RAG Graph 使用不绑定工具的 Eino ChatModel.Stream 生成唯一对外正文。

## 4. 恢复

不持久化 ADK/Graph checkpoint。同一活跃 Attempt 的 duplicate delivery 使用 attempt+call_no replay 完整 Tool result receipt。lease reclaim 创建新 Attempt 后，没有版本化 Agent transcript 就无法证明新一轮第 N 个 tool call 等同于旧一轮第 N 个调用，因此不做跨 Attempt ordinal replay；新 Attempt 可以重新执行只读调用，并生成新的 ModelCall/ToolCall 与预算记录。当前 ModelCall 只有 hash/usage，正文丢失时保持 UNKNOWN/人工恢复或显式新业务 retry；写/不可逆工具不进入首版 allowlist。未来若要求跨 Attempt 精确 replay，必须单独设计持久 transcript 和稳定逻辑调用 ID。
