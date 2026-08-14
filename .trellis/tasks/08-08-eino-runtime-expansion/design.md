# Eino AI Runtime 正式生产替换设计

## 1. 最终所有权

```text
HTTP API / River delivery
          |
          v
Project Runtime Claim -> PostgreSQL Run/NodeAttempt + lease/fence
          |
          v
Project Workflow Executor
          |
          v
Project AI Runtime Port
          |
          v
Eino Adapter: Graph / ChatModelAgent / ToolsNode / Stream
   |                 |                    |
   v                 v                    v
eino-ext Model   Project Tool Bridge   Project Domain Nodes
                                      Retrieval/Evidence/Finalizer
```

Eino 是正式的进程内 AI Runtime。River/PostgreSQL 是正式的跨进程持久执行 Runtime。两者不是主备关系，也不保存同一份 checkpoint。

## 2. 包边界

- `internal/agent/application` 定义项目 DTO 与 `RAGExecutionScheduler`、`AgentRuntime`、`AnswerStreamRuntime`、`DraftStreamStore` 等 Port，不 import Eino。
- `internal/agent/adapter/eino` 持有 `compose.Graph`、`adk.ChatModelAgent`、`compose.ToolsNode`、`schema.Message`、callback 和 stream reader 生命周期。
- `internal/platform/models` 构造并加固 eino-ext Chat/Embedding；项目 transport 继续负责 URL、SSRF/redirect、响应大小、Content-Type、错误脱敏和 Provider 契约。
- `internal/tools/application.ExecutionService` 仍是所有 Tool 的唯一正式执行入口。
- HTTP/OpenAPI、前端和 PostgreSQL 只认识项目定义的 stream/session/chunk、Answer 和 Tool receipt，不认识 Eino 类型。

通过依赖检查禁止 `internal/*/domain`、非 Eino application 包、`api/openapi` 和 migration SQL 出现 `cloudwego/eino` import 或其 wire 字段。

## 3. Chat 与 Embedding

- Chat 正式使用 eino-ext OpenAI ChatModel 的 `Generate`、`Stream` 和 `WithTools`；结构化调用与 Tool Agent 使用同一 hardened client 和稳定错误映射。
- 当前严格结构化 Chat Port 继续拒绝意外 provider `tool_calls`；Tool Agent 使用独立的 Eino adapter 路径显式绑定工具，避免放宽所有模型调用。
- Embedding 正式使用 eino-ext OpenAI/Ollama Embedder。项目 wrapper 只补足框架返回值没有携带的 model/index/order、`truncate=false`、响应上限和稳定错误合同，不自行实现向量计算或 Provider SDK。
- Eino callback 只产生脱敏 trace/metric。把现有 `RecordingChatModel` 的 STARTED/CAS/UNKNOWN 生命周期抽成项目 `ModelCallRecorder`，再由 Eino adapter 内的 recording model 同时包装 `Generate`、`Stream` 和 `WithTools`；ADK 的 ModelRetryConfig/ModelFailoverConfig 必须为空。
- ModelCall 新增可重复 `AGENT` phase 和单次 `ANSWER` phase：RAG 合法序列为可选 PLAN、一个或多个 AGENT、ANSWER、既有结构化 INITIAL/REPAIR/REDUCED，以及可选 REVIEW。数据库继续以 call_no 串行化单 Run 调用，并保持最多一个 STARTED。请求/响应 hash 基于项目规范化消息、工具 schema、tool calls 和 usage，不依赖 Eino JSON 序列化格式；Eino `ToolInfo` 只投影为项目 `name/description/parameters`，Provider tool-call ID 只映射为单份 canonical 文档内的顺序引用。Eino `Extra`、未知 ToolChoice 或 call-time Model 覆盖必须失败关闭。

## 4. RAG 内层 Graph

`RAGWorkflowExecutor` 继续负责 receipt replay、冻结 Conversation/Memory、创建 ModelRun、处理 NodeAttempt 和原子 finalization。它调用 `RAGExecutor.Execute`；`RAGExecutor` 负责五个显式项目领域节点并把节点事实交给 `RAGExecutionScheduler`，生产 scheduler 是启动时编译一次、由 Eino Graph 的 `AddBranch` 负责路由的实现。

```text
START
  -> validate/freeze project input
  -> query-plan (Eino Chat)
  -> clarification branch --------------------------> terminal proposal
  -> project retrieval (FTS/pgvector/RRF)
  -> eligibility + evidence/topic projection
  -> insufficient-evidence branch ------------------> refusal proposal
  -> Eino Agent tool call -> Eino ToolsNode -> direct-return tool result
  -> Eino tool-free final answer Stream
  -> structured metadata envelope using E*/C*/T* short references
  -> citation + faithfulness validation
  -> completed/refused branch
  -> END (project RAGProposal)
```

- 正式 v2 RAG 的 PLAN 固定使用 Prompt `rag-query-plan/v5` 与 Provider Schema `agent.rag-query-plan/v2`；无论共享 Profile 上限为何，单次 PLAN 输出都收紧到最多 256 tokens。v5 明确服务端已经绑定授权 Workspace 与完整 scope，并将逐字引用、引用证据和格式要求视为下游回答约束，避免把可检索问题误判为缺少范围。
- Tool Agent 的内部轮次只负责一次短引用 `ReadSource@2` 选择，单轮上限为 `min(Profile, 96)`；工具成功后使用 Eino `ReturnDirectly`，由独立 tool-free ANSWER Stream 综合，不在 Agent 内重复调用模型。通用 Agent Runtime 保留无 `ReturnDirectly` 的多轮 ReAct 能力。对外最终 ANSWER 保留冻结 Profile 上限，不用一个无条件的 1024 上限截断 detailed 输出。metadata 与 Faithfulness REVIEW 按必需记录数和输入规模估算单次输出预算；metadata 下限 256，REVIEW 下限 1024（覆盖将内部 reasoning 计入 `max_tokens` 的 Provider），两者只受冻结 Profile 和项目全局硬上限收紧；不再用 2048 固定 ceiling 破坏合法高基数 metadata/REVIEW。
- Provider 输出是只含 `i/r/d/q/s` 五个必填短键的无身份、扁平、严格对象：`i` 为 intent，`r` 为 rewrites，`d` 为 clarification reason，`q` 为 clarification question，`s` 为 suggested scopes。输出不得包含 `result_type`、`schema_id`、`schema_version`、`model_run_ref`、`payload` 或任何布尔字段（包括 `requires_clarification`）；项目以 `r` 是否为空确定性派生 clarification。
- 项目严格解码 Provider v2 wire，再由 Compose 注入冻结的可信 ModelRunRef，组装为既有 canonical domain `RAGQueryPlanResult` v1；Provider wire 版本不改变持久领域结果版本。旧 Prompt `rag-query-plan/v3` 与 Schema `agent.rag-query-plan/v1` 只为历史 v1 RAG 执行/回放保留，不进入正式 v2 新流量。
- Graph state 是 adapter 私有结构，只含本次 NodeAttempt 的有界项目 DTO，不持久化 Eino state。
- Retrieval、eligibility、evidence、citation、faithfulness 和 finalizer 继续调用现有项目服务；Eino 负责节点连接、branch、loop 和 stream propagation。
- 每个领域节点继续在成功边界写项目 RAG progress；Eino callback 不代替 progress 事实。
- 迁移顺序分两步但始终只有一条生产 RAG：先由 Eino Graph 接管五个领域节点之间的路由，再在同一 Graph 的生成节点中接入 Agent、tool-free final stream 和 metadata envelope。`RAGExecutor` 不再拥有通用 phase switch/branch；它仍是领域节点容器。Tool/Streaming 未完成不能阻塞核心 Graph 先落地，也不能留下 direct/Eino 两套长期生产流程。
- 五类 `INITIAL -> REPAIR -> REDUCED` 通用结构化阶段继续由启动时编译的 Eino Graph 调度。生产 Composition 不再读取实现 selector，也不再编译或保留 direct scheduler；测试替身只能通过项目 Port 注入。

## 5. Tool Agent

首版使用 Eino v0.9.13 经典 `schema.Message` ChatModelAgent/ToolsNode，不使用 AgenticMessage beta。

- Agent adapter 接收项目 `AgentRunRequest`，由已 Claim 的 NodeAttempt 注入可信身份、允许工具版本和预算。
- Tool schema 从项目冻结 Catalog 转换为 Eino `ToolInfo`；模型只提供 tool name、arguments 和 provider call ID。由于 Eino `ToolsNode`
  以工具名索引且 Provider 不返回版本，同一个 Agent allowlist 不得同时包含同名工具的多个版本。
- Agent、最终 ANSWER 和 metadata 只接收问题、摘录及 `E*/C*/T*` 投影。`ReadSource`/`ValidateCitation` 的模型 schema 也只接受这些短引用；项目 bridge 在调用 `ExecutionService` 前恢复完整 Citation tuple，并把结果重新投影为短引用、布尔值、稳定 reason 和 excerpt。Workspace、ModelRun、Citation/Claim/Topic、Source/Span UUID 与时间戳不进入这三类 Provider 请求或 ToolMessage。
- Tool wrapper 把调用交给 `ExecutionService.Execute`。服务再次校验 Workspace、Workflow、Attempt、lease/fence、
  allowlist、Capability、Schema、idempotency 和 receipt。Eino `ToolCallMiddlewares` 在项目 Tool 执行前校验
  Provider tool-call ID 非空、UTF-8、最多 256 bytes 且本次 Run 不重复，并只在 Adapter 私有 Context/State 中临时
  关联项目 `CallNo`；事件消费器再校验 Assistant ToolCall 与 ToolMessage 的 ID/工具名一一对应、无未知/重复结果且
  结束无悬空。调用项目 `AgentToolInvoker` 前，Adapter 还会重建只保留当前取消/deadline 与原始项目值的 Context，
  防止 Eino ToolCallID 通过 Context 越界。Provider ID 不进入 Application/Domain/数据库 DTO、持久化、日志或指标，
  持久身份仍是项目 `call_no`/ToolCall ID。
- 首版只允许顺序执行的只读工具。当前 `/chat` 固定使用 `ReadSource`、`ValidateCitation`；两者已经具备同一活跃
  Attempt 内的可重放 receipt。`SearchKnowledge` 会扩大冻结 Evidence 集并需要动态短引用、持久 result receipt、
  加密和保留策略，必须作为独立产品能力另行立项，通过后才可加入；它不属于当前 Eino Tool Calling 替换的完成条件。
  `ReadGitStatus` 不进入 RAG Chat 白名单。
- `ApplyApprovedPatch`、`CreateGitCommit`、RebuildIndex 等写/维护工具永不进入 Chat Agent allowlist。模型需要写入时只能生成 Proposal，批准后的执行由独立 Workflow 完成。
- 每个 NodeAttempt 只有一本项目拥有的 `RunBudgetLedger`，配置 `MaxTotalModelCalls`、`MaxAgentIterations`、`MaxToolCalls`、总输入/输出 Token、deadline，以及 ANSWER、各次 metadata envelope、REVIEW 的 phase 级 `ReservedInputTokens/ReservedOutputTokens`。进入下一轮 Agent 前先按该轮最大输入/输出 Token 预占，并确认下游 reservations 仍完整，结束后按 Provider 的真实 usage 结算并释放差额；Provider 不返回 usage 时按预占额度扣账。reasoning Provider 会把不可见推理计入 completion/output usage，实际值可以高于请求的 `MaxOutputTokens` 或单次预占；该差额只能使用尚未预占的 Attempt 总预算，不能借用其他 in-flight 或下游 reservations，突破剩余总预算仍 fail closed。ModelCall 始终持久完整 usage，不把 reasoning token 从审计/计费事实中删除。同一 deadline 同时生成 v2 Attempt Context，已启动的 Eino Graph、Agent、Stream、metadata、REVIEW 和 Provider 请求也必须在到期时取消；草稿 Seal/Complete/Abort 仅使用独立、固定 5 秒的无取消终结窗口。已有 ModelCall 会在同一 Attempt 重入时阻止不安全的 Provider replay，lease reclaim 则创建使用新 Ledger 的新 Attempt，不在旧 Attempt 内恢复非确定性对话。ADK `MaxIterations` 只能取 Ledger 当次授权值，不能成为第二预算源。
- Eino 多工具并行能力首版关闭，保证 call number、receipt、限流和审计顺序确定；后续只读并行需单独用并发与 replay 证据开放。

## 6. 真流式输出

现有 `/api/v1/events` 继续只传持久阶段/终态事件。Token 草稿使用独立 endpoint 和独立短期存储，避免把正文混进 durable event log。

建议链路：

```text
ChatModelAgent.Run -> AsyncIterator[AgentEvent]
  -> drain internal MessageOutput.MessageStream (never publish)
  -> tool-free eino-ext ChatModel.Stream for final answer
  -> Worker bounded collector + final message concat
  -> bounded/coalesced PostgreSQL draft chunks
  -> GET /api/v1/answers/{id}/stream SSE
  -> browser draft state
  -> final REST Answer replaces/discards draft
```

- `ChatModelAgent.Run` 返回 `AsyncIterator[AgentEvent]`。唯一 consumer 必须持续读取每个 event，并拥有、消费和关闭其中的 `MessageOutput.MessageStream`；该 iterator 使用无界内部通道，因此消费路径不得等待 PostgreSQL 或浏览器。事件校验失败时先取消本次 Agent 派生 Context，再排空 iterator 并关闭尚未消费的嵌套流，防止后台模型或工具在 Runtime 返回后继续执行。Agent 中间 assistant/tool 消息、ToolCalls 和参数全部只在 Worker 内部使用，不向浏览器发布。
- ADK 无法在模型流开始时证明该轮不会在尾部产生 ToolCalls。为保证不泄露中间轮，Agent 结束后由同一 Eino Graph 调用一个不绑定工具的 `ChatModel.Stream` 生成最终 Markdown；只有这个 `ANSWER` 流进入 draft sink。流结束后，Eino 结构化节点以“最终正文 + 冻结的 E*/C*/T* 投影”生成版本化内部 metadata envelope。该 envelope 不含正文、正文 hash、ModelRun 或服务端身份，只生成 assertions、短引用 conflict positions、topic refs 和 follow-ups；项目用同一次已完整消费的 Stream 与服务端 bindings 确定性组装正式 RAG v2。Citation/Faithfulness 失败时丢弃草稿并发布拒答/失败事实。
- `ANSWER` 仍保持逐帧低延迟，不为预知尾部 ToolCalls 缓冲完整 Provider 流。每个 frame 在写 draft 前必须先拒绝 nil、ToolCalls、非法 UTF-8 和越界内容；非法 frame 的正文绝不写入。若 Provider 在前序合法 frame 后才发送 ToolCalls，ModelCall 必须以稳定 `AGENT_ANSWER_STREAM_INVALID` 失败，Executor 随即把整个 session 归约为 `ABORTED`；SSE 对已看见的未验证草稿发送 reset/end，metadata、Evidence/Citation、正式 Answer 和业务审计正文均不得发布。
- metadata 的 REDUCED 阶段使用独立的无身份 `agent.rag-answer-metadata-refusal/v2` Schema；模型只生成 `RefusalPayload`，Adapter 严格解码后才注入当前 ModelRunRef 并转换为公开 `agent.refusal/v1`。这条路径也不得把 ModelRun 或服务端 UUID 放进第三次 Provider 请求。
- Draft session 状态为 `ACTIVE | COMPLETED | DEGRADED | PUBLISHED | ABORTED | SUPERSEDED`，并绑定 Workspace、Answer、Workflow Run、NodeAttempt、lease fence、generation 和 sequence。`BeginDraftSession` 在数据库事务中用数据库时间校验当前 Runtime Claim，原子 supersede 旧的未终结 session 并创建更高 generation；每次 append/close 都用 Attempt+fence 和允许的源状态做 CAS。`COMPLETED` 只表示 Provider EOF、仍待 Citation/Faithfulness/finalizer，绝不表示正式答案。最终 Answer 的原子提交必须在同一数据库事务将 session 转为 `PUBLISHED`；校验失败、取消或终态失败转为 `ABORTED`。旧 Worker 在 lease reclaim 后不能继续发布，API 只读取当前 generation，旧 `Last-Event-ID` 必须收到 reset。
- Worker 在 final stream consumer 与 PostgreSQL writer 之间使用固定帧数和字节数的非阻塞队列。consumer 始终继续消费/关闭 Provider stream，并在受控的最终正文上限内完成 concat；writer 小窗口合并、短超时批量写。队列满或存储超时后立即用独立短超时 CAS 将 session 标记 `DEGRADED`、停止 enqueue 并继续生成最终 Answer，不能等 Provider EOF，禁止阻塞 Provider、无限缓存或为了草稿重复调用。
- 浏览器断开不取消异步 Workflow；显式 Workflow cancel、Worker shutdown 或 Node deadline 才取消 Eino context。所有正常、错误、取消路径都关闭 StreamReader。
- SSE 支持 `Last-Event-ID` 在 TTL 窗口内恢复；只有 session 仍属于当前 Runtime Claim 时，才在 `ACTIVE/COMPLETED/DEGRADED` 返回当前 generation 草稿。lease 已失效但新 Attempt 尚未 begin 时也必须 reset/end。`PUBLISHED` 只发 end/refetch，`ABORTED/SUPERSEDED` 发 reset/end 且不再返回正文；过期后客户端回查正式 Answer。前端草稿与 Answer 使用不同 reducer 状态，草稿永不写入 TanStack Query 的 Answer cache，并使用与正式 Answer 相同的纯文本/安全 Markdown 渲染边界。
- 真实浏览器 gate 由 auth 流程动态取得 session/CSRF canary，只在进程内比较；桌面与移动端从首次草稿到正式终态持续扫描当前 DOM，正式 Answer 也必须不含 canary。Draft SSE 还通过透传式字节扫描验证 canary 不出现在网络响应中，扫描器只保留不超过最长 canary 的尾部窗口与布尔状态，不复制或记录正文。Playwright 回执采用精确字段集合，shell 对两端连续非空 SSE chunk 事件数和延迟做整数/顺序校验；失败诊断只输出固定分类和计数，不回显原始 console、pageerror、DOM 或 Playwright 行。

## 7. Retry、恢复与事实源

- River 重投递先查 final Answer、ModelCall 和 ToolCall receipt；最终 Answer 必须 replay。同一活跃 Attempt 的重复 delivery 按 attempt+call_no replay 保存完整结果的 ToolCall。
- Finalizer 返回错误后必须先在独立短超时 Context 查询相同 publication binding；已提交 receipt 按 response-loss replay，未查到
  receipt 才按原 Attempt fence 尝试把 draft 归约为 `ABORTED` 并把 ModelRun 归约为失败，任一清理结果不确定都进入人工恢复。
- 当前 ModelCall 只持久化 hash/bytes/usage，不保存 Provider 响应正文。已有成功 Call 但丢失内存结果时不得仅凭 hash 伪造响应，也不得宣称 exactly-once；同一执行进入 UNKNOWN/人工恢复，只有项目 Workflow 明确建立新业务 retry 时才允许新 Attempt 再调用 Provider，并创建新的审计事实。
- lease reclaim 会创建新 NodeAttempt；不恢复旧 Eino graph checkpoint，也不把旧 Attempt 的 call_no 与新一轮非确定性 Agent 对话强行对应。新 Attempt 从项目持久事实重新构造，可能重新执行只读模型/工具调用，每次都建立新 ModelCall/ToolCall 并扣新预算；首版禁止模型触达写/不可逆工具，因此不会借此重复副作用。若未来要求跨 Attempt 精确 replay，必须另行持久化版本化 Agent transcript 和稳定逻辑调用身份。
- Eino error 在 adapter 边界映射为项目稳定 error code/retryable；不得把 Provider body、URL、prompt 或 tool output 放进错误消息。
- Eino callback 的 stream copy 必须消费并关闭；global callback 只在初始化期注册。

## 8. 配置与发布

- 默认值、`.env.example` 和 Compose 全部切到 Eino。未知值或 Eino 构造失败使进程 readiness fail closed。
- 2026-08-11 决定删除旧 direct Chat/Embedding/scheduler、进程级 selector、回滚脚本和 Compose overlay；生产配置只表达 Eino。需要恢复旧实现时使用 Git 历史或兼容发布制品，不在运行时切换 AI 实现，也不提供静默 fallback。
- `RAG_REAL_PROVIDER_TRANSPORT=direct` 是容器到 Provider 的网络传输路径，不是旧 AI Runtime；它与 `host-relay` 传输模式继续保留，并由 real-provider smoke 合同覆盖。
- 发布指标已接入正式 Worker：`agent.answer.*` 覆盖首 Token/完成延迟与结果，`agent.draft.degradation_total`
  覆盖草稿降级，`agent.runtime.*` 覆盖 Agent 迭代/Tool Call，`agent.rag.graph_node.result_total` 覆盖 Graph
  节点错误，`rag.outcome_total` 只在 v2 Finalizer 非 replay 提交后覆盖拒答率；业务 ID 和 Provider/Model 不做 metric label。
  正式 OTLP/HTTP Metrics+Trace Provider 已在 API/Worker Composition 注入，`required` 模式没有 concrete exporter
  时 fail closed；本地接收器合同只证明导出协议，不能作为发布观察。指标可用不等于稳定观察已完成，真实灰度观察仍是独立门禁，执行标准见
  [`Eino Runtime 稳定发布观察 Runbook`](../../../docs/architecture/runbooks/eino-stable-observation.md)。
- 稳定观察归档由 `deploy/eino_stable_observation_verify.py` 做机器验收：canonical JSON、连续自然日、SHA-256 链、
  发布/配置/查询/阈值不漂移、事故和性能阈值均 fail closed；attestation 的签发者和 HMAC key
  指纹必须匹配仓库固定、默认 `unconfigured` 的 `deploy/eino_stable_observation_trust.json`。正式 CLI 不允许覆盖
  trust policy；没有经独立审批配置的受保护 Collector 信任根时只能返回 `incomplete`，自建 key 和人工 manifest
  不能判 PASS。Prometheus/Tempo endpoint 与四个 URL/token 文件路径由独立 backend trust 冻结；每份采集 evidence
  用相同受保护 key 绑定完整 manifest。每日续写、最终签发和独立 verifier 都重验 evidence 目录、摘要与 HMAC，
  签发后删改 evidence 也不能维持 `passed`。
- Collector evidence v2 额外把真实 `collected_at` 纳入 HMAC：启动探测必须早于首个观察窗口；每日查询必须在对应窗口
  结束后的同一观察时区自然日内完成。采集器和独立 verifier 双重拒绝逾期历史补采，不能在窗口末尾集中回填 7 天。
- `eino-stable-observation-preflight` 在真实查询和归档写入前复用 `start` 的全部本地 readiness 校验：固定策略、受保护
  backend/key 文件、空归档、时区和发布 hash。它禁止网络与写入，成功只表示输入可安全启动；`start` 仍必须再次复核
  同一不变量后才查询 Prometheus/Tempo。时区校验覆盖最长 366 天归档所需的全部午夜，使用 UTC 往返和 `fold` 拒绝
  不存在或歧义的 00:00，避免观察启动数日后才因午夜切换中断。
- 历史 Eino→direct→Eino 演练仅作为迁移证据归档；当前恢复路径是 Git 发布记录。2026-08-10 的真实 Ollama 复验暴露并验证修复了 Attempt budget 与慢模型超时问题；2026-08-12 当前树以外部 OpenAI-Compatible Chat host relay + 原生 Ollama Embedding 实际完成 Eino Graph/Agent/ToolsNode/ANSWER Stream、metadata、REVIEW、PostgreSQL draft、API 及桌面/移动浏览器多帧终态。将网络扫描改为不向页面暴露 HttpOnly session 原值的双 32-bit 滚动指纹后，最终复验中桌面/移动分别在正式发布前观察到 2/8 个连续非空草稿 SSE chunk 事件；回执中的首次草稿观察延迟不冒充 Provider 首 Token 指标。独立 `make eino-live-smoke` 同时通过外部 HTTPS OpenAI-Compatible Chat/Embedding 和 Ollama Embedding。host-relay 不代表容器直连外部 HTTPS 网络路径已通过；Runbook 要求的真实稳定观察仍是独立发布质量证据，但不构成保留 direct 实现的前置条件。

## 9. 已确认的产品入口

首版把 Tool Calling 和 Token Streaming 接入现有 `/chat` RAG，不新建通用 Agent API/页面。OpenAPI 与前端范围以现有 Chat/Answer 生命周期为边界；未来通用 Agent 产品必须另立 PRD，不得借本次迁移扩大工具或权限范围。

## 10. ADR

实施前新增 ADR-0027，明确取代 ADR-0026 中 RAG/Tool/Streaming 的生产 No-Go；ADR-0024 的分层所有权、ADR-0025 的 Embedding 合同以及 River/PostgreSQL 事实源继续有效。
