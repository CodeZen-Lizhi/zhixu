# Eino Tool Calling 与 Agent 正式迁移

## Goal

使用 Eino v0.9.13 经典 ChatModelAgent/ToolsNode 正式替换自定义 ToolRequest 调度和通用 Agent 循环，并接入一个真实产品 Workflow；项目继续拥有工具权限、幂等、receipt、审计、审批和可信执行。

## 当前状态

Eino classic `ChatModelAgent`/`ToolsNode` 已接入 `/chat` RAG 的生产组合；真实 Provider smoke 已覆盖 `PLAN -> AGENT -> ANSWER`、模型调用 `ReadSource@2`、receipt、预算、重投递和至少 30 个持久草稿 chunk。2026-08-11 当前树又通过外部 OpenAI-Compatible Chat host relay + 原生 Ollama Embedding 的 metadata、REVIEW 与桌面/移动浏览器正式终态闭环；该证据不冒充容器直连外部 HTTPS 网络路径。历史 Eino→direct→Eino 回滚演练已归档，稳定观察独立执行，旧 direct 已按父任务同日决策从生产部署面删除。

## Requirements

- Eino 承担模型轮次、provider 原生 `tool_calls` 合并、ReAct loop 和 ToolsNode dispatch。
- 项目 `AgentRuntime` Port 不暴露 `schema.Message`、`ToolCall` 或 ADK event。
- 所有模型调用经过项目 recording/budget；Eino ModelRetry/ModelFailover 关闭。
- ModelCall domain/SQL 增加可重复 `AGENT` phase 和流式完成语义，不能把多轮 ReAct 冒充为一次 INITIAL 或只依赖 callback 计数。
- Tool schema 只从冻结项目 Catalog 生成，调用只进入 `ExecutionService`。
- 可信 Workspace/Workflow/Attempt/lease/fence/Capability 由服务端注入，模型和 Eino context 都不能覆盖。
- 首版只允许顺序只读工具；写工具只能产生 Proposal，不能执行 Safe Writeback。
- 每个 Tool Call 使用项目 attempt+call_no 身份并保存 STARTED/terminal receipt；provider call ID 只在当前内存对话中关联 ToolMessage。同一活跃 Attempt 的重复 delivery 必须 replay 成功 receipt；lease reclaim 后的新 Attempt 不强行匹配旧的非确定性 Agent 轮次，允许重新执行只读调用并形成新记录。
- 首个消费者已冻结为现有 `/chat` RAG，不在本任务新建通用 Agent API/页面。

## Acceptance Criteria

- [x] Eino ToolCallingChatModel、ChatModelAgent 和 ToolsNode 在生产 Worker 组合中真实运行。
- [x] `/chat` RAG 完成模型→只读工具→模型的多轮闭环，并由真实 Compose smoke 验证。
- [x] unknown tool、无效参数、伪造身份/权限/审批、预算耗尽、取消和 provider tool-call 分片均 fail closed。
- [x] provider tool-call ID 在 Eino Adapter 内校验非空、UTF-8、长度和 Run 内唯一性，并与项目 `CallNo` 临时关联；ID 不进入 Application/Domain、持久化、日志或指标。
- [x] ModelCall 与 ToolCall 数量、费用、耗时、receipt 和错误可按 NodeAttempt 审计。
- [x] NodeAttempt 级共享 Budget Ledger 限制总模型调用、Agent 迭代、Tool Call、Token 和时间，并为下游 phase 预留额度。
- [x] 多轮 `AGENT` ModelCall 的 phase/call_no、STARTED 唯一性、stream EOF/错误/取消与 canonical tool-call hash 通过 domain/SQL/repository 测试。
- [x] PostgreSQL/River duplicate delivery 和 response-loss 在同一活跃 Attempt 内 replay 完整 Tool result receipt；lease reclaim 后只读调用可安全重做，写/不可逆工具仍不可达。
- [x] 写工具从模型 allowlist 不可达；Proposal/Approval/Safe Writeback 边界保持不变。
- [x] 旧的模型 Tool 调度已迁移到 Eino `ChatModelAgent`/`ToolsNode`。`ToolRequestV1` 仍作为项目
  `ExecutionService` 的安全 DTO，`agent-rag@1` 仅由 Worker 保留用于迁移前已持久化 Run 的回放；
  API/dispatcher 不注册它，也没有新的生产者。调用方和保留原因已写入
  `internal/tools/adapter/workflow/contract.go`、`cmd/worker/main.go` 与架构文档。

## Superseded Decision

旧 `research/product-gate.md` 的生产 No-Go 是基于“没有产品消费者”。用户现已明确要求正式 Tool Calling；本任务必须绑定选定入口并实施，不能再以 PoC PASS 作为完成。
