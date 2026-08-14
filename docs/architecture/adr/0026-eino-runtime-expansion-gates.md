---
status: superseded
---

# Eino Runtime 扩展门禁结论

> **已被 ADR-0027 在完整 RAG、Tool/ReAct 与 Token Streaming 的生产结论取代。** Checkpoint/Interrupt
> PoC-only 与 PostgreSQL/River 唯一跨进程恢复事实源的结论继续有效。

ADR-0024 已采用 Eino Chat、Callback telemetry 和 Structured Output 短 Graph，ADR-0025 已采用可回滚的
OpenAI-Compatible/Ollama Embedding Adapter。本决策记录其余候选能力的实际门禁结果，避免把“框架具备能力”
误写成“当前产品应该接入”，也避免后续重复实现已经完成的评估。

## Decision

- **完整 RAG Graph：生产 No-Go。** Eino Workflow/Graph 能表达 RAG 流程，但当前复杂度主要来自项目的
  Retrieval tuple、Evidence/Citation、Model Run/Call、exact replay 和原子发布不变量。现有 Eino 原生能力只覆盖
  加权生产路径的 25/100；把项目 Port 全部包装成 Lambda 会增加 Graph state、DTO 映射、selector 和双路径测试，
  不能删除有意义的领域编排。生产继续使用 `RAGWorkflowExecutor`/`RAGExecutor`，并复用已采用的 Eino Chat、
  Embedding 和三阶段 Structured Scheduler；不创建无消费者的 Eino Retriever bridge。
- **Tool/ReAct：框架能力 PASS，生产 No-Go。** `poc/eino/tooltrace` 已验证 Eino ToolsNode 的 schema 和 dispatch。
  当前没有获批的模型 Tool-loop 产品入口、工具白名单、循环终态、持久 NodeAttempt 或审批合同，因此不新增
  ChatModelAgent/ToolsNode 生产路径。未来模型侧写能力最多生成 Candidate/Proposal；批准后的写回仍由独立项目
  Workflow 获取授权后执行。
- **Token Streaming：框架能力 PASS，生产 No-Go。** `poc/eino/streaming` 已通过实际 Eino
  `compose.Stream` 多帧、取消和提前 Close 传播测试。当前 Conversation/RAG 只有可重放的持久阶段 SSE，
  没有逐 Token API、前端草稿状态机或草稿/最终态合同，因此不新增 Token endpoint、selector 或前端状态。
- **Checkpoint/Interrupt：PoC PASS，生产 No-Go。** `poc/eino/checkpoint` 已验证实际 StatefulInterrupt、
  ResumeWithData、Attempt/Fence 绑定、加密、TTL/Delete、篡改拒绝和并发隔离。当前 Worker 重启或 lease reclaim
  会终结旧 Attempt 并创建新 Attempt，Human Task 也会结束当前 Attempt；没有 fenced one-time handoff 时，
  checkpoint 只允许同一进程、同一活跃 Attempt 的无副作用实验，不能接入 River/PostgreSQL 生产恢复。
- **ADK/Agentic：生产 No-Go。** 经典 `schema.Message` 的 Graph/Tools 能力已有隔离证据；当前没有多 Agent
  产品消费者。Agentic OpenAI/`AgenticMessage`、`v0.10` alpha 和长时间 Agent 恢复不进入生产默认路径。

## Ownership Boundary

Eino 负责已获准路径中的通用 Provider SDK、进程内 Graph、Callback 和组件调度。项目继续唯一拥有：

- PostgreSQL/River Workflow、Run/Node/Attempt、lease/fence、retry、Outbox、Human Task 和补偿；
- Workspace、FTS/pgvector/RRF、Active Index、Evidence/Citation、Model Run/Call 和原子发布；
- Tool Registry、Authorization、Capability、幂等、receipt、Approval 和 Safe Writeback；
- Conversation/Memory 持久化、持久阶段 SSE 和最终 Answer 事实。

Eino callback、checkpoint 或 Graph state 都不能成为这些业务事实的第二来源。

## Reopen Conditions

- 完整 RAG：出现可复用的多个真实消费者，且候选方案达到全部强制条件、至少 80% 加权覆盖，并能实际删除
  重复调度代码；之后再补固定 fixture、真实 PostgreSQL/River、取消和 terminal response-loss 对照。
- Tool/ReAct：先有获批产品 PRD、生产入口、工具白名单、最大迭代/token/time 预算、持久 Attempt、审批/终态和
  独立 ADR；首个生产路径禁用框架自动 retry/failover，所有模型调用仍穿过 `RecordingChatModel`。
- Token Streaming：先批准独立 Token API、草稿/最终态、断线、取消、内容安全和回滚合同，再从 Eino
  `Stream`/`Transform` 贯通真实 Provider 到 HTTP 客户端；现有持久 SSE 不改变。
- Checkpoint：先设计 PostgreSQL 授权的 fenced one-time handoff、Store Schema、密钥轮换、清理、拓扑/序列化
  兼容和副作用幂等，再评估某个明确的无副作用 Agent 子流程。

## Consequences

- “当前不接入”不能解释为 Eino 不支持；Tool、Streaming 和 Checkpoint 的框架能力已有实际 PoC 证据。
- No-Go 路线不新增空闲 bridge、selector、API、数据库 Schema 或第二套恢复账，因此没有生产回滚动作。
- direct 实现和项目编排继续保留。Embedding 默认仍为 `direct`，两个 Provider 启用 `eino` 前分别完成真实
  Provider smoke；旧实现删除仍需至少一个发布观察周期和独立审批。

## Related Decisions

- [ADR-0013](0013-eino-adoption-gate.md)：Eino 原始采用门禁和不可替换边界。
- [ADR-0024](0024-layered-eino-adoption.md)：Chat、Callback 与短 Graph 分层采用。
- [ADR-0025](0025-eino-embedding-adoption.md)：双 Provider Embedding 可回滚采用。
