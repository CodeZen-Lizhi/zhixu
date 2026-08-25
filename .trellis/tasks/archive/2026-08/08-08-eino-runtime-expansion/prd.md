# Eino AI Runtime 正式生产替换

## Goal

使用 Eino 和 eino-ext 正式替换当前自研的模型调用、Embedding、Tool Calling、流式输出和 Agent 通用编排，使 Eino 成为生产默认且最终唯一的通用 AI Runtime；项目继续拥有领域 Workflow、权限、审批、证据链及 PostgreSQL/River 持久化，并通过项目 Port/Adapter 隔离全部 Eino 类型。

## Product Decision

- 此任务是生产迁移，不再以 PoC、可选 Eino selector 或 No-Go 研究作为完成条件。
- 旧 RAG、Tool/ReAct、Token Streaming No-Go 结论是当时缺少产品授权时的历史判断；用户已明确要求正式采用 Eino，因此这些结论被本 PRD 取代。
- 用户已确认首个真实消费者使用现有 `/chat` RAG：Eino Graph 承担内层 RAG/Agent 编排，Eino Tool Calling 提供受控只读工具，Eino Stream 输出明确标记的生成中草稿。

## Requirements

- 固定 Eino core `v0.9.13`；eino-ext 各 module 固定精确版本，不采用 v0.10 alpha 或 Agentic beta 路径。
- Eino/eino-ext 成为 Chat、Embedding、结构化阶段调度、RAG 内层 Graph、Tool Agent 和 Streaming 的唯一生产实现；初始化失败必须 fail closed，不提供第二套 Runtime 回退。
- 保留现有项目 Port、请求/响应限制、安全 HTTP transport、稳定错误码、ModelRun/ModelCall、EmbeddingContract 与领域校验。保留这些合同不是重写 Eino，而是约束第三方 Provider 和保护项目事实。
- 由 Eino Graph 负责 RAG 阶段连接、分支和循环路由；`RAGExecutor` 保留为五个显式领域节点的容器并执行项目事实，PostgreSQL FTS、pgvector、RRF、active-index、Evidence/Citation/Faithfulness 仍由项目节点实现。
- 使用经典 `schema.Message` 的 Eino ChatModelAgent/ToolsNode 承担 ReAct 循环和 tool dispatch；模型只能调用服务端冻结的只读工具或创建 Proposal，不能取得身份、Capability、Credential、审批令牌或直接执行可信写回。
- Tool 执行继续统一经过项目 `ExecutionService`、持久 `workflow.tool_call`、allowlist、Schema、权限、幂等、receipt 和审计；批准后的副作用继续由独立领域 Workflow 执行。
- 从 Eino `Stream` 贯通 Provider、Worker、跨进程传输、API SSE 和前端草稿状态；不得用 `Invoke` 或单帧包装冒充流式。
- 流式草稿不是 Answer 事实。最终 Answer 仍必须经过结构化输出、引用、忠实度和原子发布门禁；刷新和终态恢复仍以 PostgreSQL/REST 为准。
- 真实浏览器门禁必须在桌面/移动端从首次草稿到正式终态持续拒绝动态 session/CSRF canary，并检查正式 Answer；回执只接受精确字段集合和有界整数，失败诊断不得回显原始 Playwright 正文或凭据。
- ANSWER Provider frame 必须先通过 nil/ToolCalls/UTF-8/大小校验再写入 draft；包含 ToolCalls 的非法 frame 自身正文不得进入 sink，ModelCall 归约为 `FAILED`。若非法 frame 出现在已发布的合法草稿帧之后，前序内容仍只是未验证草稿，整个 session 必须 `ABORTED`，SSE reset/end，且不得生成 metadata、Evidence/Citation、正式 Answer 或业务审计正文。
- River/PostgreSQL 继续拥有 delivery、Run/Node/Attempt、lease/fence、retry、outbox、会话状态、Human Task 和业务恢复；Eino checkpoint 不进入生产持久化链路。
- 所有 Eino 内部模型调用必须进入同一本 NodeAttempt 级 `RunBudgetLedger` 和项目 ModelCall 记录；Ledger 同时限制总模型调用、Agent 迭代、Tool Call、Token、时间，并按 phase 最大输入/输出 Token 为最终正文、结构化 envelope 和 REVIEW 预留额度。同一 Attempt deadline 还必须作为执行 Context 的硬截止时间，取消已经开始的 Eino Graph、Stream 和 Provider 调用，不能只拒绝下一次 Ledger 授权。Eino 自动 model retry/failover 默认禁用。
- 扩展 ModelCall 领域/数据库合同以支持可重复的 `AGENT` 轮次、单次 `ANSWER` 真流和流式生命周期；RAG 合法顺序为可选 `PLAN -> AGENT* -> ANSWER -> INITIAL/REPAIR/REDUCED -> REVIEW`，其中后三阶段生成不含正文的 metadata envelope，REDUCED 使用无身份 `agent.rag-answer-metadata-refusal/v2`，由项目侧注入 ModelRunRef 后再形成公开拒答。每次 Generate/Stream 均先写 STARTED，流在 EOF 后以规范化 assistant/tool-call 响应 hash、usage 和耗时完成，错误/取消/结果不确定继续按 FAILED/UNKNOWN 归约。
- Eino 类型只允许出现在基础设施和 `internal/agent/adapter/eino`；不得进入 Domain、Application Port、HTTP/OpenAPI DTO、PostgreSQL Schema 或 River Job Args。
- 2026-08-11 用户明确决定立即删除旧 direct 实现、selector、回滚脚本与部署 overlay；需要找回历史实现时使用 Git 历史，
  不以稳定观察或双实现演练作为删除前置条件。历史 v1 持久合同继续由 Eino-backed replay Definition 消费。

## Acceptance Criteria

- [x] 默认配置、`.env.example`、Compose 和 Worker 组合均使用 Eino；生产调用不经过自研 Provider client 或旧 scheduler。
- [x] OpenAI-Compatible Chat 和 OpenAI-Compatible/Ollama Embedding 的 Eino 路径通过共享合同、真实 Provider smoke、取消/超时和错误脱敏测试；2026-08-11 `make eino-live-smoke` 已实际执行外部 HTTPS OpenAI-Compatible Chat/Embedding、原生 Ollama Embedding、Query Plan、metadata 和 Faithfulness REVIEW 六个生产入口。
- [x] 五类 Structured Scheduler 均由 Eino Graph 运行，旧 scheduler 不再是生产实现。
- [x] 生产 RAG 内层从 Query Plan 到终态 Proposal 由编译后的 Eino Graph 编排，`RAGExecutor` 继续提供领域节点，外层 River replay/finalizer 和领域门禁保持在项目 Workflow 中。
- [x] 选定产品入口使用真实 Eino ChatModelAgent/ToolsNode 完成模型 tool-call→只读工具→独立 Eino ANSWER Stream 闭环；当前 `ReadSource@2` 使用 Eino `ReturnDirectly` 避免 Agent 内重复综合，通用 Agent Runtime 另有多轮 ReAct 回归。冻结只读工具、权限、预算、幂等、receipt、重投递和审计已由生产组合测试、真实 PostgreSQL/River 与当前工作树的 `make compose-rag-smoke` 覆盖。
- [x] ModelCall domain/SQL 支持 `PLAN -> AGENT* -> ANSWER -> INITIAL/REPAIR/REDUCED -> REVIEW` 的合法顺序；共享 Ledger 能证明下游预留调用不会被 Agent 耗尽，重复 Agent 轮、流式 EOF/错误/取消、call_no 上限和 canonical request/response hash 已有本地数据库与 race 覆盖。
- [x] Provider→Worker→API→浏览器的多帧 Token 流通过真实 Provider 端到端测试；2026-08-12 当前工作树以 `RAG_REAL_PROVIDER_TRANSPORT=host-relay` 和 `RAG_REAL_PROVIDER_EMBEDDING_KIND=ollama` 实际完成外部 OpenAI-Compatible Chat、原生 Ollama Embedding、Eino Graph/Agent/ToolsNode/ANSWER Stream、metadata、REVIEW、PostgreSQL draft、API SSE、正式 Answer 及桌面/移动浏览器替换闭环。将 draft SSE 网络扫描改为不向页面暴露 HttpOnly session 原值的滚动指纹后，最终复验中正式 Answer 发布前桌面端和移动端分别观察到 2/8 个连续非空 SSE chunk 事件。该证据不冒充容器直连外部 HTTPS Chat/Embedding 网络路径。
- [x] 未验证草稿不能进入 Answer、持久 Server Event、Evidence/Citation 或业务审计事实；最终发布仍只有 completed/refused/clarification_required 三类业务终态。
- [x] Eino 类型隔离检查通过，Domain/Application/OpenAPI/River/PostgreSQL 不依赖 Eino 类型或序列化格式。
- [x] River duplicate delivery、lease reclaim 和 terminal response-loss 保持明确的 fail-closed 语义：已提交的最终 Answer 必须 replay；同一活跃 Attempt 内有完整 result receipt 的 ToolCall 必须 replay。lease reclaim 创建新 Attempt 后，由于未持久化完整 Agent transcript，允许只读模型/工具调用重新执行并形成新的记录与预算，写/不可逆工具仍不可达；只有 hash/usage、没有响应正文的成功 ModelCall 不宣称 exactly-once。
- [x] 历史 Eino→direct→Eino rollback/drain 演练已归档为迁移证据：暂停新入队、排空在途 v2、direct 只创建 v1、恢复 Eino 后重新创建 v2；该证据不代表当前部署仍提供 direct Runtime。
- [x] 旧 direct Chat/Embedding/scheduler、selector、legacy metric、专用 Claim exclusion/drain guard 和部署回滚入口全部删除；历史 v1 持久回放保持 Eino-backed。稳定观察不是删除前置条件。
- [x] Go/前端/SQL review、相关测试、race、vet、OpenAPI、Compose、浏览器桌面/移动 UI 烟测和 `git diff --check` 全部通过；2026-08-12 当前树已完成真实 Provider 桌面/移动多帧终态、全仓与高风险 race 门禁，并在独立 PostgreSQL 18 + pgvector 实例中验证 River replay/fence、草稿与 Finalizer 事务合同。
- [x] ADR、架构、运维、README、项目介绍和面试材料已同步为 Eino-only 生产路径；历史 ADR/演练作为迁移证据保留，材料明确区分已通过的外部 Provider live gate、host-relay 浏览器闭环、尚未证明的容器直连外部 TLS 路径和真实稳定观察。
- [ ] 受保护 Collector 已在真实 Prometheus/Tempo 查询面连续采集至少 7 个完整自然日、累计至少 100 个非 replay RAG v2 终态；固定阈值、backend trust、逐份 evidence HMAC 和最终 attestation 均经独立 verifier 验收为 `passed`。（独立发布质量证据；不阻塞本任务归档，当前仍为 `incomplete`。）

## Current Baseline

- 已完成并有本地证据：Chat、Embedding、五类 Structured Scheduler、Compose/Worker 默认使用 Eino；Eino Graph 驱动 RAG 路由，`RAGExecutor` 保留领域节点；classic Agent/ToolsNode、tool-free final Stream、短引用 metadata envelope、PostgreSQL draft/SSE、Finalizer 和前端草稿路径已实现；ModelCall/RunBudgetLedger、Eino 类型隔离及真实 PostgreSQL/River `make rag-integration` 已通过。Agent、ANSWER 和 metadata 三类模型输入只携带 `E*/C*/T*` 投影，Tool bridge 在项目侧恢复完整 Citation tuple 并脱敏模型可见结果。
- 当前工作树的 `make compose-rag-smoke` 使用真实进程和 Eino Runtime fixture，覆盖 direct-return Agent Tool Calling、`ReadSource@2` receipt、最终多帧 Stream、metadata、Review、原子 `PUBLISHED` 与 exact replay；其精确 ModelCall 序列为 `PLAN,AGENT,ANSWER,INITIAL,REVIEW`。早期真实 Ollama 复验暴露并验证修复了 Attempt budget 与慢模型超时问题；2026-08-12 当前树最终以外部 OpenAI-Compatible Chat host relay 和原生 Ollama Embedding 完成同一 Eino 链路，且最终指纹化网络扫描版本的桌面/移动浏览器分别在正式发布前观察到 2/8 个连续非空草稿 SSE chunk 事件。
- 历史回滚门禁证据已归档：旧树曾由 `make compose-rag-rollback-smoke` 真实执行 managed modelctl 的 preflight/drain/quiesce/prepared/commit，完成 Eino→direct→Eino；该脚本、overlay、专用 Claim exclusion/drain guard 和排空索引已按 2026-08-11 决策删除，当前恢复依赖 Git 发布记录，不再切换 AI 实现。正常 managed revision drain 与真实 PostgreSQL/River 竞态合同继续保留。
- 已实现正式 OTLP/HTTP Metrics+Trace Provider，并在 API/Worker Composition 注入；本地接收器合同验证了真实导出路径，但不把本地接收器当作发布观察证据。稳定观察的不可伪造门禁见 [`Eino Runtime 稳定发布观察 Runbook`](../../../docs/architecture/runbooks/eino-stable-observation.md)：受保护采集器固定 Prometheus/Tempo 查询、阈值与 backend trust，以逐份 HMAC evidence 绑定完整 manifest，并由 `make eino-stable-observation-verify` 独立复核 evidence、连续窗口、样本、hash chain 和最终 attestation。阈值、backend trust 与 attestation trust policy 仍为 `unconfigured`，真实观察尚未执行。
- 完整浏览器门禁支持 `direct|host-relay` 网络传输和独立 Chat/Embedding Provider 选择。离线 preflight 已证明缺配置/HTTP URL fail closed、app/worker Eino 装配一致且不输出 endpoint/Credential；2026-08-12 完整 gate 又实际证明 host-relay 外部 Chat + 本地 Ollama Embedding 能通过 metadata、REVIEW、草稿发布及桌面/移动浏览器多帧终态。浏览器回执记录首次草稿观察延迟；Provider 首 Token 延迟由 `agent.answer.first_token.duration_ms` 指标记录，二者不得混称。
- `make eino-live-smoke` 已独立证明当前生产 Adapter 对外部 HTTPS OpenAI-Compatible Chat/Embedding、原生 Ollama Embedding 及三类结构化阶段的真实协议兼容。host-relay 浏览器成功不等于容器直连外部 HTTPS 网络路径通过；真实稳定观察也仍未执行。稳定观察作为独立发布质量证据，不决定是否保留第二套 Runtime，旧 direct 已按 2026-08-11 决策删除；本任务已按用户决定归档为 `completed`，不等待该观察窗口。

## Completion Decision (2026-08-25)

本任务的 Eino-only 生产迁移、真实 Provider/live gate、浏览器闭环和既定质量门禁已经完成并归档为 `completed`。按用户决定，连续 7 天/100 个终态的稳定观察不再作为本任务完成或删除旧 direct 实现的前置条件；它仍是独立的发布质量证据，必须由受保护 Collector 和 verifier 真实采集，当前不得宣称 `passed`。

## Non-Goals

- 不用 Eino 替代 River/PostgreSQL durable workflow。
- 不把 Eino checkpoint 作为跨进程或跨 Attempt 的恢复事实。
- 不把 `ApplyApprovedPatch`、`CreateGitCommit` 等可信写回直接暴露给模型。
- 不采用 v0.10 alpha、AgenticMessage beta 或未固定版本的实验 API。
