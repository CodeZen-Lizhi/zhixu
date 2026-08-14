# Eino 生产 AI Runtime 契约

## 1. Scope / Trigger

修改完整 RAG 编排、Eino Graph/ADK/ToolsNode、Token Streaming、draft persistence/SSE、`/chat` Definition 或
Eino runtime 配置时必须读取本规范。Chat、Embedding 与五类 Structured Scheduler 的具体 Adapter
合同分别见 `eino-chat-adapter.md`、`eino-embedding-adapter.md` 和 `eino-structured-scheduler.md`。

ADR-0027 已使 Eino 成为正式生产唯一 AI Runtime。历史 ADR-0026 的 RAG、Tool/ReAct 与 Token Streaming
No-Go 仅保留为决策历史，不再是当前生产结论；Checkpoint 例外，仍只允许 PoC。

## 2. Ownership And Public Ports

- Eino 类型只能出现在 `internal/platform/models` 和 `internal/agent/adapter/eino`；Application、Domain、HTTP/OpenAPI、
  PostgreSQL/River DTO 与 migration 不得引用 Eino 类型或序列化格式。
- 项目 `RAGExecutionScheduler`、`AgentRuntime`、`AnswerStreamRuntime`、`DraftStreamStore`、`ModelCallRecorder` 与
  `RunBudgetLedger` 是稳定 Port/事实边界。Eino adapter 只收发有界项目 DTO。
- Eino 负责进程内 Chat、Embedding、Structured Graph、RAG 内层 Graph、classic `ChatModelAgent`/`ToolsNode` 和
  final-answer Stream。Eino 自动 retry/failover 必须关闭。
- 项目继续拥有 River delivery、Workflow Run/Node/Attempt、lease/fence、retry/replay、PostgreSQL、检索、
  Evidence/Citation/Faithfulness、权限、审批、Tool receipt 和最终 Answer 发布。
- Eino checkpoint 不进入生产持久化或恢复链路。重启、lease reclaim、新 Attempt 与 Human Task 都只能从项目持久事实恢复。

## 3. `/chat` RAG v2

Eino 配置下，新 `/chat` Question 注册 RAG v2。Worker 可保留 v1 Definition，只用于既有持久 v1 replay；该回放
由 Eino-backed replay Definition 消费，不是旧 direct Runtime，也不得成为新 dispatch 入口。

```text
Project claim/freeze -> Eino RAG Graph -> plan/retrieval/evidence branches
  -> classic ChatModelAgent <-> ToolsNode (read-only, sequential)
  -> tool-free ChatModel.Stream final answer
  -> metadata envelope -> citation/faithfulness -> project Finalizer
```

- Agent 只获得服务端冻结的只读工具。每次调用仍经 `ExecutionService`、allowlist、Schema、Capability、Attempt、
  lease/fence、idempotency、receipt 与审计；写/维护/可信写回工具永不进入 Chat allowlist，模型只能生成 Proposal。
- Provider tool-call ID 是不可信的 Eino 会话内关联值。Adapter 必须用 `ToolCallMiddlewares` 在项目 Tool 执行前校验
  ID 非空、UTF-8、最多 256 bytes 且本次 Run 不重复，只在私有 Context/State 中临时关联项目 `CallNo`；事件消费器
  必须再校验 Assistant ToolCall 与 ToolMessage 的 ID/工具名一一对应，拒绝未知、重复、错名和结束悬空。Provider ID
  不得进入 Application/Domain/数据库 DTO、持久化、日志或 metric label，也不得通过传给项目 `AgentToolInvoker` 的
  `context.Context` 隐式穿透；项目调用 Context 只能保留当前取消/deadline 和进入 AgentRuntime 前已有的项目值。项目
  `call_no`/ToolCall ID 始终是权威身份。事件校验失败时必须先取消本次 Agent 派生 Context，再排空 iterator 并关闭
  尚未消费的嵌套 MessageStream，不能让后台模型或工具在 Runtime 返回后继续执行。
- 同一个 Agent allowlist 不得同时包含同名工具的多个版本；Eino `ToolsNode` 以名称索引，Provider 也只返回名称，版本歧义
  必须在任何模型调用和预算扣减前失败关闭。ModelCall canonical 文档必须把 Provider tool-call ID 映射成本次文档内的顺序引用，
  并把 Eino `ToolInfo` 投影成项目拥有的 `name/description/parameters`；Eino `Extra`、未知 ToolChoice 和 call-time Model 覆盖
  不得被静默忽略，也不得进入持久 hash。
- Application 保留完整 `RAGGenerationContext` 服务端事实；Adapter 为 Agent、ANSWER 和 metadata 构造同一套仅含问题、
  excerpt 与 `E*/C*/T*` 的模型 DTO。模型 Tool schema 也只接受短引用；bridge 恢复完整 Citation tuple 后执行，
  再把 Tool result 投影为短引用结果。任何服务端 UUID、Citation/Claim/Topic ID、ModelRun、时间戳或 content hash
  都不得进入这三类 Provider 输入或 ToolMessage。
- 一个 NodeAttempt 只有一个项目 `RunBudgetLedger`。它限制总模型调用、Agent iteration、Tool Call、token 和时间，
  并为 ANSWER、metadata 和 REVIEW 保留额度。同一 deadline 必须同时约束 v2 执行 Context，使已启动的 Graph、Stream
  和 Provider 调用按时取消；不能只在下一次 Ledger 授权时发现超时。Provider 的 completion/output usage 可以包含
  不出现在响应正文中的 reasoning token，因此单次 `MaxOutputTokens`/reservation 不是实际计费用量的等值上限。
  Ledger 必须持久并结算 Provider 返回的完整 usage：差额只允许使用尚未预占的 Attempt 总预算，不能侵占其他
  in-flight 或 ANSWER、metadata、REVIEW 预留；若差额突破剩余总预算则仍 fail closed。ADK 最大迭代只接受 Ledger
  的当次授权，不能成为第二预算源。
- Agent 的中间 assistant/tool 消息、Tool arguments 和 Tool result 不得发布给浏览器。Agent 完结后单独调用不绑定工具的
  `ChatModel.Stream` 生成最终正文；`Invoke` 或单帧包装不构成真流。

## 4. Draft Stream And Publication

- Worker 对每个 Attempt 创建带 workspace/answer/run/node/attempt/fence/generation 的短期 draft session。bounded、
  non-blocking queue 将 final stream 合并成 PostgreSQL chunks；队列满或存储超时标记 `DEGRADED`，但 consumer 仍消费并关闭
  Provider stream。首次失败必须立即用独立短超时尝试状态转换，不能等 Provider EOF；禁止无限缓存或为草稿再次调用模型。
- `ANSWER` 保持逐帧低延迟，不能为预知尾部 ToolCalls 完整缓存 Provider 流。每个 Provider frame 必须在 draft append 前先拒绝
  nil、ToolCalls、非法 UTF-8 和越界内容；非法 frame 自身正文不得写入，ModelCall 以稳定 `AGENT_ANSWER_STREAM_INVALID`
  归约为 `FAILED`。若前序合法草稿已可见，Executor 仍必须将整个 session 归约为 `ABORTED`，SSE reset/end；metadata、
  Evidence/Citation、正式 Answer 与业务审计正文不得从该流发布。
- SSE 独立于 durable `/api/v1/events`：`GET /api/v1/answers/{answer_id}/stream` 支持 `Last-Event-ID=generation:sequence`、
  `chunk/reset/end` 和 heartbeat。SSE 只读取当前 generation；浏览器断开不取消 Workflow。
- `ACTIVE`、`COMPLETED`、`DEGRADED` 都不是正式 Answer。EOF 后仍必须通过 metadata、Citation/Faithfulness 和项目 Finalizer。
  Finalizer 在同一 PostgreSQL 事务提交 Answer 时把 session 变为 `PUBLISHED`；校验失败、取消或异常变为 `ABORTED`。
  Finalize 返回错误时先用独立短超时 Lookup 判定是否发生 commit response-loss；只有确认没有 receipt 后才按原 Attempt fence
  尝试 `ABORTED` 并终结 ModelRun，任一状态不确定都返回人工恢复。浏览器草稿状态不得写入 Answer cache，最终 REST Answer
  必须替换或丢弃草稿。
- TTL cleanup 只删除过期 draft 数据；正式 Answer、Evidence、Audit 与 durable event 不从 draft 推导正文。

## 5. Defaults, Failure And Recovery

- Chat、Embedding 和五个 Structured Scheduler 固定使用 Eino。Eino 构造或运行失败必须 fail closed，不提供第二套 Runtime 回退。
- 全局 `ToolRuntimeMode` 的安全默认可以是 `disabled`；正式 Compose Eino overlay 必须显式设为 `enabled`，并只组合可用的
  只读工具。Eino Chat + RAG scheduler + tool runtime 不能混合降级：任何一项缺失，v2 Worker 必须 fail closed。
- 不再提供 direct selector、direct Worker 或 Eino→direct overlay。需要恢复历史实现时通过 Git 发布记录恢复兼容版本；
  PostgreSQL/River 已持久 Run、Job、Attempt 和 v1 replay 事实继续按现有恢复流程处理。
  2026-08-10 较早的一次真实 Ollama Compose 运行曾成功执行 `PLAN -> AGENT -> ANSWER` 并持久化至少 30 个
  draft chunk，随后暴露旧的三阶段预算被错误用于整个 Attempt，在 metadata 前命中 10 分钟 deadline；预算修正后的
  两次复验又在 `AGENT` 命中单次 300 秒 `MODEL_CHAT_TIMEOUT`。这些是历史失败证据。2026-08-11 当前树已分别通过
  外部 HTTPS OpenAI-Compatible Chat/Embedding 与原生 Ollama Embedding live gate，以及 host-relay 外部 Chat +
  本地 Ollama Embedding 的完整 Provider/metadata/REVIEW/桌面和移动浏览器终态。host-relay 不证明容器直连外部
  HTTPS 网络路径；真实稳定观察仍需独立记录，但不影响 Eino-only 部署。

### Runtime Observability

- 正式 Worker 必须把同一个项目 `Metrics` 实例注入 Eino Agent、Answer Stream、RAG Graph 和 RAG Finalizer 边界。
- 首正文 frame 记录 `agent.answer.first_token.duration_ms`；EOF/错误记录 `agent.answer.completion.duration_ms` 与
  `agent.answer.result_total`；draft sink 首次失败记录一次 `agent.draft.degradation_total`，且不能停止 drain Provider 流。
- 成功 Agent Run 记录 `agent.runtime.iterations`、`agent.runtime.tool_calls`，所有已开始的 Agent Run 记录
  `agent.runtime.result_total`。Graph 固定节点记录 `agent.rag.graph_node.result_total`。
- `rag.outcome_total` 只能在 v2 Finalizer 成功、receipt 绑定校验通过且不是 replay 后记录；不得在临时 Proposal
  或 Eino Graph terminal 节点提前记录。
- label 只能使用注册表中的稳定 `result`、`outcome`、`node_kind` 和 `error_code`。Workspace、Answer、Provider、
  Model、Endpoint、Prompt、正文和 Tool 输入输出禁止进入 metric label。Exporter error/panic 不得改变业务结果。
- 指标接线通过本地测试不代表稳定观察期通过；真实 Provider 灰度数据仍必须单独留证。
- 稳定观察证据可使用 `deploy/eino_stable_observation_verify.py`：启动/每日 manifest 使用 strict canonical JSON
  和连续 SHA-256 链，发布物、配置、查询和阈值不可漂移，且连续 7 个自然日、至少 100 个非 replay v2 终态、
  阈值和事故检查全部通过。它是独立发布质量证据，不是删除某个 Runtime 的授权门禁。
- 只有受保护 Collector 作业直接查询真实 Metrics/Trace 后签发、且签发者角色和 32-128 bytes 私有 HMAC key
  SHA-256 均匹配仓库固定 `deploy/eino_stable_observation_trust.json` 的 attestation，才允许验收器输出 `passed`。
  固定策略默认 `unconfigured` 且正式 CLI 不提供覆盖口；无 attestation、自建 key、人工 manifest、日志或截图只能
  得到 `incomplete`，不得宣称稳定观察完成。
- 采集器的查询面固定为 Prometheus-compatible Metrics API 与 Tempo-compatible Trace Search API；OTLP Collector 只做
  ingest/转换。`deploy/eino_stable_observation_backend_trust.json` 同时冻结两个后端 URL 和四个受保护 URL/token 文件的
  path digest。每份 collector evidence 用同一受保护 HMAC 绑定完整 manifest；每日续写、最终 attestation 和独立 verifier
  都必须逐份复核 evidence 目录、签名和 manifest 链接。任意手工聚合、替换 endpoint/token 文件或缺失 evidence 都
  fail closed。
- Collector evidence 必须用 v2 合同记录受 HMAC 保护的实际 `collected_at`：启动探测在首个窗口前完成；每日查询只能
  在对应窗口结束后的同一观察时区自然日内完成。过期窗口不得集中补采，独立 verifier 即使面对重新签名的 evidence
  也必须拒绝逾期时间戳。
- 正式观察开始前必须有零网络、零写入 preflight，共享 `start` 的 query/threshold/backend trust/attestation trust、
  key、受保护 URL/token 文件、空归档、时区和发布 hash 校验。`start` 必须有序依赖并再次复用同一校验；preflight
  必须对最长 366 天归档所需的全部本地午夜做 UTC 往返与歧义检查，任一午夜不存在或出现两次时在联网前拒绝；
  不得访问后端、写 evidence 或输出任何 Secret/URL/hash，且不能作为 Collector 可达或观察开始证据。

## 6. Required Evidence

- Adapter/Graph：Eino-only Composition、构造失败 fail closed、无自动 fallback、Eino 类型隔离。
- Live gate 聚合：完整环境检查必须先于任何 Provider 请求；即使调用方使用 `make -j`，缺任一 Chat、Embedding、
  PLAN、metadata 或 REVIEW 配置也不得启动其他 `go test`。离线合同必须同时锁定六个正式目标和该并行负测。
- 完整浏览器门禁必须显式选择 `ollama|openai-compatible`；外部模式复用受保护的 `ZHIXU_EINO_LIVE_*`
  Chat/OpenAI-Compatible Embedding 配置。脱敏 preflight 必须先验证 app/worker 均为 static model settings + Eino，
  Provider/Model/Endpoint/Credential 装配与已校验输入精确一致，且比较过程不得输出 Endpoint/Credential。preflight
  还不得访问 Provider、调用 `curl/git/go/node/npm` 或执行有状态 Compose 子命令；离线合同
  必须用命令守卫证明只允许 `docker compose version/config`。preflight PASS 不能替代真实 Provider 调用。
- Agent：多轮模型->只读工具->模型，冻结 schema/allowlist、预算、顺序 call number、receipt、权限、replay 与拒绝写工具。
- Budget：用 reasoning-inclusive `output usage > 单次 reservation` 的真实形状证明未预占总额度可吸收差额；同时证明
  下游 reservation 与 Attempt 总 token 上限不可被借用，缺失 usage 仍按预占额度扣账。
- Stream：多帧、EOF、错误、取消、reader close、背压/degraded、generation reset、SSE cursor、TTL、最终 Answer 替换草稿。
- 浏览器 gate：桌面/移动端必须从首次草稿到正式终态持续拒绝当次 auth 动态 session/CSRF canary，并检查正式 Answer；
  Playwright 不得 clone/buffer 原始 SSE。回执只接受精确字段集合和有界整数，失败诊断只输出固定分类/计数，不得回显
  console、pageerror、DOM、Provider body 或 Playwright 原始行。
- Database：session/chunk 状态机、attempt/fence CAS、Finalizer `PUBLISHED` 原子性、response-loss、lease reclaim 与 SQL 约束。
- 发布：真实 Provider、浏览器端到端、Compose/Eino overlay 和稳定观察期必须单独执行。早期真实 Ollama Compose
  到 ANSWER 多帧草稿以及后续 AGENT timeout 都只是历史部分证据。2026-08-11 当前树已独立通过六项真实 Provider
  live gate，并通过 host-relay 外部 Chat + 本地 Ollama Embedding 的 metadata、REVIEW、draft/Answer 和桌面/移动
  浏览器终态；该结果不能替代容器直连外部 HTTPS 网络路径或真实稳定观察，两者仍不得标记 PASS。

## 7. Wrong vs Correct

| Wrong | Correct |
|---|---|
| 直接把 Eino SDK 类型传到 HTTP/数据库 | 用项目 Port/DTO 隔离 Eino 类型 |
| 把 Agent 中间流或 Tool output 发到浏览器 | 只发布独立无工具最终 Answer draft |
| EOF 就把草稿当正式答案 | 通过 metadata、证据门禁与 Finalizer 后原子 `PUBLISHED` |
| Eino 出错时静默调用第二套 Runtime | fail closed；通过 Git 发布记录恢复兼容版本 |
| 用旧实现接手在途 v2 | 按 PostgreSQL/River Attempt、lease/fence 和项目恢复流程处理 |
| 用 Eino checkpoint 恢复 lease reclaim | PostgreSQL/River 保持唯一跨进程恢复事实 |
| 把完整 `RAGGenerationContext` 或 Citation tuple 发给 Provider | Adapter 只发 `E*/C*/T*` 投影，完整身份只留在服务端 bindings |
| 因 `output usage > MaxOutputTokens` 丢弃 reasoning-inclusive usage 或直接判 Provider 不一致 | 持久完整 usage；只用未预占的 Attempt 总预算吸收差额，保护全部下游预留和总上限 |

## 8. Legacy Tool Replay Boundary

### 1. Scope / Trigger

修改 `internal/tools/adapter/workflow`、Worker Tool Runtime 组合、Eino RAG Tool bridge 或迁移/回滚流程时，
必须保持历史 `agent-rag@1` 回放与新的 Eino Tool Calling 完全分界。

### 2. Signatures

- 新模型调用：`RAGAgentToolBridge.Invoke(context.Context, AgentToolInvocation) (AgentToolResult, error)`。
- 共享执行事实源：`ExecutionService.Execute(context.Context, ExecuteToolCommand) (ToolExecutionResult, error)`。
- 历史回放构造：`NewReplayRegisteredDefinition(ContractCatalog) (workflowdomain.RegisteredDefinition, error)`。

### 3. Contracts

- `agent-rag@1` 是迁移前单节点 Definition，只能由 Worker 注册以消费已经持久化的 Run；它不是模型 Agent loop，
  不得成为 `/chat` 或任何新 dispatcher 的生产入口。
- `ToolRequestV1`、`PersistedToolInvocationV1` 和 `ToolResultV1` 是项目安全 DTO/回放格式，不能从 Eino 类型直接
  穿透到 Workflow、PostgreSQL 或 HTTP。
- 新 `/chat` v2 的模型 Tool Call 必须经 Eino `ChatModelAgent`/`ToolsNode`、冻结 v2 allowlist、
  `RAGAgentToolBridge` 和同一 `ExecutionService`；写工具仍不可达。

### 4. Validation & Error Matrix

| 条件 | 必须结果 |
|---|---|
| API/新 dispatcher 尝试解析或启动 `agent-rag@1` | Definition/Executor 不可达，稳定拒绝 |
| Worker 回放缺少冻结 Contract、ExecutionService 或可信 Claim 身份 | readiness/执行 fail closed |
| Eino Agent 请求未知、写入或越权 Tool | 在 bridge/ExecutionService 拒绝，不创建成功 receipt |
| 历史回放输入含 `reason`、未知字段或内容型未绑定参数 | strict decoder 拒绝 |

### 5. Good / Base / Bad Cases

- Good：已存在的 `agent-rag@1` River Job 由 Worker 解码、校验 Claim 后回放，结果只写脱敏摘要。
- Base：没有历史 `agent-rag@1` Job 时，Eino v2 完全不依赖该 Definition；新 Tool Call 仍走 bridge。
- Bad：把 `NewRegisteredDefinition` 或 `ToolRequestV1` 放进 Eino Agent scheduler，或为方便而让 API 注册旧 Definition。

### 6. Tests Required

- API registry 测试断言 `agent-rag@1` 不可解析且不暴露旧 Executor。
- Worker composition/readiness 测试断言旧 Definition 仅在 Tool Runtime enabled 时作为回放依赖注册。
- Eino RAG integration/Compose smoke 断言模型->只读 Tool->模型闭环走 `RAGAgentToolBridge`，并产生一次项目 receipt。
- 历史 River replay 测试断言严格输入、response-loss replay 和 lease/Attempt 身份不变。

### 7. Wrong vs Correct

#### Wrong

把 `agent-rag@1` 的持久 Node 当成新的模型 ReAct 调度器，或者为了删除旧代码而让历史 River Job 变成不可回放。

#### Correct

Eino 接管新的模型轮次和 Tool dispatch；项目保留最小、可审计的旧 DTO/Executor 作为显式历史回放兼容，直到历史
Run 排空并完成独立删除门禁。

## 9. Model-visible Short-reference Contract

### 1. Scope / Trigger

修改 `RAGGenerationContext`、Agent/ANSWER/metadata 输入、metadata v2、`RAGAgentToolBridge`、ReadSource、
ValidateCitation 或 Provider fixture 时，必须保持本节的模型可见边界。

### 2. Signatures

- Application Port：`RAGGenerationPort.Generate(context.Context, RAGGenerationRequest) (RAGGenerationResult, error)`。
- Adapter projection：`buildRAGMetadataContext(RAGGenerationContext) (ragMetadataContextInput, RAGAnswerMetadataBindings, error)`。
- Tool scope：`RAGAgentToolBridge.BindMetadataScope(RAGAnswerMetadataBindings) (*RAGAgentToolBridge, error)`。
- Server composition：`RAGAnswerMetadataResultV2.ComposeRAGAnswerV2(modelRunRef, finalText, bindings)`。

### 3. Contracts

- Application 的 `RAGGenerationContext` 保留完整且类型化的项目事实；只有 workflow Adapter 可把它转换为模型 DTO。
- Agent 与 ANSWER 输入只包含 request、`E*` excerpt、`C*` applicability/evidence refs、`T*` name/evidence refs；
  metadata 额外接收同一次完整 Stream 正文，但不接收 `model_run_ref`。
- metadata v2 输出不含正文、hash、ModelRun、Citation/Claim/Topic、UUID 或时间戳；项目以 bindings 恢复这些身份。
- metadata v2 的 REDUCED 阶段使用独立 `agent.rag-answer-metadata-refusal/v2`：模型只输出严格拒答 payload，不能回显
  `model_run_ref`；通过后由 Adapter 以当前项目 ModelRun 绑定成公开 `agent.refusal/v1`。不得把旧的身份型拒答 Schema
  作为 metadata 的 ReducedSchemaRef。
- ReadSource 模型参数为 `evidence_ref`，ValidateCitation 参数为 `evidence_refs + conflict_refs`；底层
  `ExecutionService` 仍接收原 v2 完整参数并写正式 receipt。Tool result 返回模型前必须删除完整 tuple/hash。
- 当前 `/chat` 把已绑定的只读工具设为 Eino `ReturnDirectly`：一次 AGENT ModelCall 产生 tool call，ToolsNode 执行后
  将脱敏 JSON 交给独立 tool-free ANSWER Stream，不在 Agent 内重复综合。通用 Agent Runtime 必须继续覆盖空
  `ReturnDirectlyTools` 时的多轮 ReAct，不能把产品优化误写成框架不支持循环。
- 非 nil 空 `conflicts` 是合法“无冲突”；clone 必须保持 nil 与非 nil 空集合的区别。nil 表示输入缺失，不能静默归一化。
- Ollama 0.9.6 的 grammar 对复杂精确 alternation/nested condition 不稳定；JSON Schema 只负责 `E/C/T[1-9][0-9]*`
  词法形状，Go strict decoder/bindings 负责数量上限、顺序、唯一性、闭包和权限。
- OpenAI-Compatible Provider wire 不依赖 `uniqueItems`：部分严格 Schema 实现会拒绝该关键字。PLAN、metadata、
  metadata REDUCED、REVIEW 与 RAG Tool 参数只保留数组类型和数量边界，唯一性必须由相应 Go strict decoder/
  bindings 再次强制；不得以兼容性为由删除服务端去重。

### 4. Validation & Error Matrix

| 条件 | 必须结果 |
|---|---|
| Provider 输入出现 Workspace/ModelRun/Citation/Claim/Topic/Source/Span 身份 | 生成前拒绝或测试失败，不允许发送 |
| ReadSource 请求未知 `E*` | `AGENT_RAG_TOOL_REFERENCE_DENIED`，不调用 ExecutionService |
| ValidateCitation 请求未知/重复/交叠 `E*/C*` | 稳定拒绝，不创建成功 receipt |
| Tool 输出 tuple 与绑定不一致 | `AGENT_RAG_TOOL_RESULT_INVALID`，不把原始输出交给模型 |
| metadata 引用未知、重复或未覆盖的 `E*/C*/T*` | strict decode/Compose 失败，不发布 Answer |
| `conflicts=nil` | 输入无效 |
| `conflicts=[]` | 合法无冲突 |

### 5. Good / Base / Bad Cases

- Good：模型以 `E1` 调 ReadSource，项目恢复 Source Version/Span 执行，模型仅收到 `{"evidence_ref":"E1","excerpt":"..."}`。
- Base：无冲突时输入固定 `conflicts=[]`，metadata 返回空 positions/summary，服务端仍恢复 Citation 与 Topic。
- Bad：为省一次映射而把完整 Citation JSON、`model_run_ref` 或 raw Tool result 放进 Agent/ANSWER/metadata 消息。

### 6. Tests Required

- 三类 Provider 请求均断言不含完整身份，只含已绑定 `E*/C*/T*`；Tool schema 与 ToolMessage 做相同断言。
- Tool bridge 覆盖 E/C 展开、结果脱敏、跨 Workspace、未知/重复/交叠 ref、输出漂移、底层错误和并发隔离。
- metadata domain 覆盖未知/重复/越权 ref、C* 精确覆盖、Topic/Citation 闭包、nil/空 clone 与服务端 Compose。
- fixture 录制真实 Eino wire；live gate 用真实 Provider 覆盖至少 `E1/E2 + C1/C2 + T1` 并执行 Compose。

### 7. Wrong vs Correct

#### Wrong

把项目 DTO 直接 `json.Marshal` 给 Agent，或让模型用 UUID 调 Tool，再依赖提示词要求它不要泄露。

#### Correct

先在 Adapter 生成每次调用独占的短引用表；模型只操作短引用，项目在可信边界内展开、执行、验证、脱敏并最终组合。
