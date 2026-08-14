---
status: accepted
---

# Eino 作为正式生产 AI Runtime

ADR-0024、ADR-0025 已分别验证 Eino Chat、Callback、Structured Scheduler 和双 Provider Embedding。
ADR-0026 在当时没有产品消费者时，对完整 RAG、Tool/ReAct 和 Token Streaming 作出生产 No-Go。用户现已明确
批准把这些能力接入现有 `/chat` RAG，因此本决策取代 ADR-0026 中这三项 No-Go；Checkpoint/Interrupt 的生产
No-Go 与 River/PostgreSQL 唯一持久恢复事实源继续有效。

## Decision

- Eino core 固定使用 `v0.9.13`，eino-ext 各 module 固定精确版本；不采用 v0.10 alpha、AgenticMessage beta
  或未固定版本的实验路径。
- Eino/eino-ext 成为 Chat、Embedding、Structured Scheduler、RAG 内层 Graph、Tool Agent 和最终正文 Stream
  的唯一生产实现。初始化失败 fail closed，不允许静默回退到 direct。
- 合并 `dev` 后保留 `chat_api_style=responses` 的历史 revision 与数据库 Down guard，但固定的
  Eino/eino-ext OpenAI component 只实现 Chat Completions。当前制品对 Responses 在构造前明确拒绝；不得恢复旧 direct
  adapter，也不得把 Responses revision 静默改走 Chat Completions。需要重新开放时须单独交付完整的 Eino
  Generate/Stream/WithTools Responses component 和真实 Provider 门禁。
- 首个真实消费者是现有 `/chat` RAG，不新增通用 Agent API/页面。Eino Graph 负责一次 NodeAttempt 内的节点连接、
  branch 和 loop；经典 `schema.Message` ChatModelAgent/ToolsNode 负责受控只读 ReAct。
- Agent event 和嵌套 MessageStream 只在 Worker 内部消费。对浏览器公开的正文来自不绑定工具的独立 Eino
  `ChatModel.Stream`，并以 phase=`ANSWER` 记录，避免泄露中间模型消息或尾部 ToolCalls。
- 项目 Port/Adapter 隔离 Eino 类型。Domain、Application Port、HTTP/OpenAPI DTO、River Job Args 和 PostgreSQL
  Schema 不依赖 Eino 类型或序列化格式。
- 2026-08-11 用户明确决定立即删除旧 direct Chat、Embedding 和 scheduler 实现及其部署选择面；默认配置、Compose、
  Worker 和测试只使用 Eino。需要找回历史实现时通过 Git 历史恢复，不再保留运行时 direct 回滚开关或双实现。
  历史 Eino→direct→Eino 演练仅作为迁移证据归档，不再是当前部署能力。2026-08-10 的真实 Ollama 复验暴露并验证
  修复了 Attempt budget 与慢模型超时问题。2026-08-11 当前树的 `make eino-live-smoke` 已通过外部 HTTPS
  OpenAI-Compatible Chat/Embedding、原生 Ollama Embedding 及三类结构化阶段；host-relay 外部 Chat + 本地 Ollama
  Embedding 的完整 smoke 也已通过 Eino Graph/Agent/ToolsNode/Stream、metadata、REVIEW、PostgreSQL draft、API 和
  桌面/移动浏览器终态。host-relay 不证明容器直连外部 HTTPS 网络路径；真实稳定观察仍是独立发布质量证据，
  但不构成保留 direct 实现的前置条件。不得提供运行时 fallback。
- `RAG_REAL_PROVIDER_TRANSPORT=direct` 继续表示 Compose 容器直连 Provider 的网络路径；它不是旧 AI Runtime，且与
  `host-relay` 网络路径一起保留并接受 real-provider smoke 合同验证。

## Ownership Boundary

Eino 正式拥有通用 AI Runtime 能力：Provider SDK、Generate/Stream/WithTools、进程内 Graph、ReAct loop、
ToolsNode dispatch 和 Callback telemetry。项目继续唯一拥有：

- PostgreSQL/River delivery、Run/Node/Attempt、lease/fence、retry、Outbox、Human Task、补偿和业务恢复；
- Workspace、PostgreSQL FTS/pgvector/RRF、Active Index、Evidence/Citation/Faithfulness 和原子 Answer 发布；
- ModelRun/ModelCall、NodeAttempt 级调用/Token/时间预算与稳定错误；
- Tool Catalog、Authorization、Capability、Schema、幂等、result receipt、Approval、Proposal 和 Safe Writeback；
- Conversation/Memory、持久阶段事件、短期草稿状态和最终 Answer 事实。

Eino Graph state、callback 和 checkpoint 都不能成为上述业务事实的第二来源。

## Runtime Contracts

- 每次 Eino Generate/Stream 都必须先创建项目 ModelCall STARTED，并在 EOF、错误或取消后归约为项目终态。
  Eino ModelRetryConfig/ModelFailoverConfig 保持 nil；Workflow 是重试的唯一决策者。
- NodeAttempt 使用一本项目 `RunBudgetLedger`，在每轮 Agent 前预留最终 ANSWER、metadata envelope 和 REVIEW
  的调用及输入/输出 Token；Provider usage 缺失时按预占上限扣账。同一 Attempt deadline 也约束整条 v2 执行
  Context，保证已经开始的 Graph、Stream 和 Provider 调用不会越过应用层硬上限。
- Tool schema 只来自服务端冻结 Catalog，首版顺序执行 `ReadSource`、`ValidateCitation` 等只读工具；所有调用仍经
  `ExecutionService`。模型不能获得身份、Capability、Credential、Approval 或可信写回入口。
- Agent、最终 ANSWER 与 metadata 的 Provider 输入只含 `E*/C*/T*` 投影；Tool schema 也只接受短引用。
  项目 bridge 在执行前恢复完整 Citation tuple，验证正式 Tool receipt 后再把输出脱敏为短引用结果。
  ModelRun、Workspace、Citation/Claim/Topic、Source/Span UUID、时间戳和 content hash 都保留在项目边界。
- 同一活跃 Attempt 可以按 attempt+call_no replay 完整 Tool result receipt。lease reclaim 创建新 Attempt 后不把
  非确定性 Agent ordinal 强行匹配旧调用，只允许重新执行只读调用并建立新的审计和预算事实。
- Token 草稿使用独立、短 TTL、generation+sequence 的 PostgreSQL log。session 受 Attempt/fence CAS 保护，
  `COMPLETED` 只表示 EOF 待验证；最终 Answer 提交与 `PUBLISHED` 同事务，验证失败与 `ABORTED` 同事务。
  草稿不进入持久 Server Event、Evidence/Citation、业务 Audit 或最终 Answer cache。

## Consequences

- 生产 RAG 将删除 `RAGExecutor` 的通用 phase switch/branch 路由职责，但保留它作为检索、证据、引用、忠实度和
  finalizer 所需的五个显式领域节点容器；`RAGExecutionScheduler` 的 Eino Graph（`AddBranch`）组合这些节点。
  自定义 Component 仍是框架的正式扩展方式。
- API/Worker 分进程下的真流需要项目拥有的 fenced draft log、SSE 和前端 reducer；Eino 负责 Provider 流与进程内
  传播，不替代跨进程交付和最终事实。
- 生产部署不再提供 rollback selector、direct Worker 或 Eino→direct overlay。应用版本与配置均由 Git/发布制品记录恢复；
  PostgreSQL/River 的已持久事实继续按现有恢复 Runbook 处理，不通过切换 AI Runtime 实现恢复。
- 由于当前 ModelCall 不保存完整 Provider 响应正文，本决策不宣称模型调用跨进程 exactly-once。若未来要求跨
  Attempt Agent replay，必须另立决策并持久化版本化 transcript 和稳定逻辑调用身份。

## Related Decisions

- [ADR-0024](0024-layered-eino-adoption.md)：Chat、Callback 与短 Graph 分层采用。
- [ADR-0025](0025-eino-embedding-adoption.md)：OpenAI-Compatible/Ollama Embedding 采用。
- [ADR-0026](0026-eino-runtime-expansion-gates.md)：被本决策部分取代的历史门禁；Checkpoint 结论继续有效。
