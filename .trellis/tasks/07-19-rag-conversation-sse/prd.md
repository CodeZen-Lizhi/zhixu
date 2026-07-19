# M6-04 RAG Conversation API And SSE

## Goal

把已经完成的 Retrieval、Knowledge Evidence Eligibility、Agent 结构化输出、Citation/Faithfulness 门禁和 Durable Workflow 组装成真实可用的 RAG 会话产品：用户能够创建会话、连续提问、观察真实执行阶段、获得经过校验的 Answer 或明确 Refusal、打开引用并提交反馈；断线或刷新后可以从持久事实恢复。

本任务不是普通聊天壳，也不能用 M6-03 的内部 Tool smoke 代替会话产品。最终用户价值是：回答有来源、冲突不被隐藏、证据不足时不假成功、运行失败能够恢复或解释。

## Authoritative Sources

- 产品目标、查询配置、回答结构、引用、冲突、拒答、多轮会话和反馈：`docs/product/PRD.md:1506-1610`。
- RAG 执行顺序、Streaming 与失败语义：`docs/architecture/workflows/04-rag-question-answering.md:1-102`。
- REST、202、Problem、SSE Envelope 与 Last-Event-ID：`docs/architecture/api-and-events.md:1-225`。
- 前端路由、状态所有权和 RAG 最终事实规则：`docs/architecture/frontend-architecture.md:52-143`。
- Agent/Tool 现状边界：`docs/architecture/agent-rag-architecture.md:285-296`。
- 父任务范围和门禁：`.trellis/tasks/07-16-product-delivery/implement.md:48-69`。

当上述文档与现有实现冲突时，本任务采用以下原则：产品语义以 PRD 为准；已发布的安全和事实源不变量以现有架构及代码为准；缺少的 REST、SSE、Conversation 生命周期和反馈幂等契约由本任务补齐并同步回架构文档。

## Confirmed Current State

- 已有真实 Search/Evidence、Agent RAG/Refusal Schema、Citation/Faithfulness gate、Model Run/Call、Workflow/River 和 Tool Call 安全事实源。
- 当前没有 Conversation、Question、Answer、Feedback 表、Repository、HTTP 路由或 OpenAPI 契约。
- 当前没有 `text/event-stream` Handler、Last-Event-ID 解析或浏览器事件重放存储。
- Worker 仅注册 Relation Assessment Agent executor；现有持久 Tool Workflow 是单次调用，不是通用 Tool Loop。
- `SearchKnowledge` 的内容型参数尚未进入持久 Tool 目录，Provider 原生 `tool_calls` 继续 fail closed。
- 前端只有 `/` 路由，没有 `/chat/:conversationId` 或 RAG Feature。

## Requirements

### RAG-01 Conversation lifecycle and navigation

- 用户可以幂等创建 Conversation，并按 Workspace 分页查看、打开 Conversation。
- Conversation 是短期问答上下文，不自动写入长期 Memory。
- 本期保留 `open/archived` 生命周期的数据契约；页面只要求创建、列表和打开，归档命令留到通用会话管理任务。
- Archived Conversation 拒绝新的 Question，但既有 Question 命令仍按原幂等键稳定 exact replay 或返回幂等冲突。
- 列表按最近活动时间和稳定 ID 排序，禁止无分页返回。

### RAG-02 Question submission and sequential context

- 用户可以在一个 Conversation 内幂等提交 Question；同一幂等键与同一请求精确重放，同键不同请求返回冲突。
- Question、待发布 Answer、Workflow Run、首节点、Outbox 和 River Job 必须在同一数据库事务创建，不能留下半状态。
- 一个 Conversation 同时最多有一个非终态 Answer Workflow。前一个 Workflow 未终态时，新 Question 返回可操作冲突，避免并发分支污染上下文。
- Question 正文和历史只保存在 Conversation 事实源；Workflow Input 只冻结稳定 ID、版本和 Hash，不复制 raw 问题或历史文本。
- 每次执行读取当前 Question 之前最多 8 个已发布 Turn，合计输入不超过 32 KiB；使用的范围和上下文 Hash 随 Question 冻结。
- 当问题语义或范围无法从显式 Scope 与有界历史合理确定时，系统必须发布结构化 Clarification，而不是猜测或把澄清伪装成 Refusal；用户通过后续 Question 回答澄清。

### RAG-03 Query scope and evidence boundary

- Question 支持 Workspace、Source/Source Version、相对路径和捕获时间过滤，默认 `hybrid`。
- Answer options 固定为 `answer_depth=concise|standard|detailed`（默认 `standard`）和 `output_format=markdown|outline`（默认 `markdown`）；两者必须进入 canonical request hash、持久 Question 和生成契约。
- 默认只允许最新批准正式知识发布为事实性回答；Active Index 不等于 Approved Evidence。
- `allow_original_sources` 和 `allow_web` 默认 false。当前缺少可发布的原始/网页 Evidence 资格和 Web Search 契约时，true 必须显式返回稳定的“不支持/不可用”语义，不能静默忽略或伪装已使用。
- Rerank 不可用时可按既有规则显式退化到 RRF；Embedding 不可用时 Hybrid 可显式退化到 Keyword；Semantic 不可用必须失败。

### RAG-04 Retrieval-first RAG execution

- M6-04 v1 使用 retrieval-first 单节点 Workflow，不实现通用模型 Tool Loop。
- 执行顺序至少包括：加载冻结上下文、结构化 Query Plan/澄清判断、1..3 个有界 Query Rewrite、检索与去重、证据检查、结构化生成、Citation 校验、Faithfulness Review、最终发布。
- Search 直接复用 Retrieval Application seam；不得为了调用 Search 把 raw query 写入 `workflow.tool_call`。
- 每个实际阶段开始或完成时产生持久、可重放的摘要事件；禁止用定时百分比伪造进度。
- Query Rewrite、检索范围、请求/有效模式、Index/Embedding 版本、候选/选中/冲突数量和显式降级必须形成 Answer 的有界 `retrieval_summary`，供刷新后“为什么这样回答”面板查询；不得仅存在 SSE 或日志中。

### RAG-05 Validated Answer publication

- 事实型 Answer 必须包含可打开的段落级 Citation；模型推断必须显式标记且不能伪装引用事实。
- Conversation Workflow 使用新增、向后兼容的 `agent.rag-answer/v2`：在 v1 的 conclusion/assertions/citations/conflict 字段上增加服务端验证的 `related_topics` 和 1..5 个有界 `follow_up_questions`；既有 v1 decoder/调用方保持可用。
- Related Topic 必须来自服务端 Knowledge 绑定候选并与实际 Citation 关联，不能信任模型自造 Topic ID/名称。
- Citation identity、Artifact openability、Knowledge eligibility、引用闭包和 Faithfulness 全部通过后，Answer 才能发布为 `completed`。
- 最终 Answer 与对应 Model Run 终态必须原子发布；不能出现 Answer 已完成但 Model Run 仍运行，或反之。
- Answer 只保存最终对外 Envelope 与稳定引用；Prompt、Evidence 原文、原始模型响应、Tool raw I/O 和 Token 明细继续由既有受控事实源负责且不得复制。

### RAG-06 Conflict and refusal semantics

- 冲突 Claim 必须展示不同观点、适用条件、来源和更新时间，不能静默合并或替用户裁决。
- 无相关证据、只有未批准证据、引用不可打开、外部事实未授权、冲突无法条件化或校验耗尽时必须发布结构化 Refusal。
- Provider/数据库/运行时故障不是业务 Refusal；应由 Workflow 暴露 retry/failed/cancelled/unknown 状态和稳定错误。

### RAG-07 Conversation read model and citation opening

- Conversation Turn 查询按稳定 `(ordinal, id)` cursor 分页，返回 Question 与其 Answer 投影。
- Answer 查询返回发布状态、Workflow 状态、版本、最终结果和可打开 Citation href。
- Answer 查询同时返回持久 `retrieval_summary`；Clarification 查询返回缺失信息、澄清问题和可选建议，不返回伪 Answer/Citation。
- 未发布 Answer 的处理状态必须从 Workflow 权威事实投影，不在 Answer 表复制 failed/cancelled/retry 状态机。
- SSE 到达只触发资源回查；最终 Answer 只能来自 Answer/Conversation Query。

### RAG-08 SSE replay and recovery

- `GET /api/v1/events` 以 `text/event-stream` 返回强类型 Envelope，至少包含 `id/type/occurred_at/workspace_id/resource_ref/resource_version/payload_summary/schema_version`。
- 事件使用持久、单调的序号作为 SSE `id`；`Last-Event-ID` 在 24 小时保留窗口内补发。
- 非法或未来游标返回 400；游标早于保留窗口返回 409，客户端必须重新查询资源后无游标重连。
- 无 `Last-Event-ID` 时从连接建立时的当前水位开始，不把全部历史重新发送。
- SSE Payload 只含稳定 ID、状态、版本和计数，不含 Question/Answer 正文、Evidence、绝对路径、Credential 或 Tool 输出。
- 连接需要 heartbeat，并在客户端取消时及时释放数据库/网络资源。

### RAG-09 Feedback evaluation input

- 支持 `helpful`、`incorrect`、`irrelevant_citation`、`broken_citation`、`missing_source` 五类反馈。
- Citation 类反馈必须绑定 Answer 中真实存在的 Citation ID；其他反馈不得伪造 Citation 绑定。
- Feedback append-only 且幂等；同键不同请求冲突。
- Feedback 只进入 Evaluation 输入，不修改正式知识、不创建 Proposal、不改变 Answer。
- M10 身份未落地前，只承诺请求幂等，不宣称“每个用户只能反馈一次”。

### RAG-10 Minimal real frontend

- 提供 `/chat` 和 `/chat/:conversationId` 的真实页面；`/chat` 创建或选择 Conversation 后导航到详情。
- 页面包含 Conversation 列表、问题输入、范围设置、回答深度、输出格式、回答时间线、阶段状态、Clarification、引用面板、冲突/拒答、Related Topic、后续问题、检索摘要和反馈控件。
- API 与 SSE 从 `unknown` 在唯一边界严格解码；Feature/Component 不重复解析 wire payload。
- TanStack Query 拥有 Server State，Router 拥有 Conversation ID，局部组件只拥有未提交输入和面板状态。
- SSE 失联时明确显示重连状态并使用有界轮询回查，不得卡死或显示假完成。
- 桌面和移动端均不溢出；键盘可提交、打开引用和反馈，阶段变化通过适度 live region 通知，不能逐 token 播报。

### RAG-11 Error, security, and performance

- 所有 JSON 输入严格校验；Question 最大 8 KiB UTF-8、无 NUL；Feedback comment 最大 2 KiB；分页最大 100。
- 所有 SQL 参数化并带 Workspace/Conversation 绑定；跨 Workspace 与不存在统一 Not Found，避免枚举。
- 日志、Problem、SSE、Workflow Input/Output、Model Run/Call 和 Feedback 不泄露 Secret、正文、绝对路径或原始 Provider 数据。
- 查询避免 N+1：Turn 列表、Citation 映射、上下文和反馈校验均使用有界批量查询。
- 本任务不把 loopback 或 `workspace_id` 描述为认证；正式 Session/Token/CSRF/Capability 属于 M10。

### RAG-12 Contracts, tests, and delivery

- OpenAPI 必须覆盖 Conversation、Question、Turn、Answer、Feedback 和 SSE，含成功、重放、Problem、405 与 `text/event-stream`。
- 新增迁移需通过空库、重复 Up、空数据 Down/Up、已有业务数据 guarded Down、约束和并发测试。
- 必须覆盖正常、边界和失败路径，包括重复提交、response-loss、跨 Workspace、无证据拒答、Citation/Faithfulness 失败、SSE 重连/超窗和草稿不发布。
- 必须提供 `make rag-integration` 与 `make compose-rag-smoke`，并通过 Go race/vet/tidy、前端 lint/typecheck/test/build、OpenAPI 和最小浏览器烟测。

## Requirement Clarifications And Product Improvements

| ID | Original gap or conflict | Adopted requirement | User value | Impact and compatibility |
|---|---|---|---|---|
| O-01 | 文档使用 Conversation/Question/Answer，但调研初稿曾建议通用 Message | 领域与数据库采用显式 Question/Answer；`Turn` 仅为查询投影 | 状态和发布门禁更清楚，不混入 system/tool transcript | 新模块，无既有 API 兼容风险；术语同步到 `CONTEXT.md` |
| O-02 | 文档未定义并发提问语义；初始迁移曾把 `pending` Answer 误当成活动 Workflow，导致 failed/cancelled 后会话永久不可继续 | 一个 Conversation 同时只允许一个非终态 Answer Workflow；Answer `pending` 仅表示未发布，旧 Workflow 终态后可创建下一 Answer slot | 避免后发问题读取未完成答案，同时保证运行故障不会封死会话 | `00021` 前向迁移以 Conversation 锁和非终态 Run 触发器替代 pending 唯一索引；HTTP 契约不变，未来若支持分支需新显式 Parent Question 契约 |
| O-03 | M6-04 要 SSE，M9-04 又负责统一 Event Store | 本期落 `ops.server_event` 的稳定数据库/API 契约并仅接 RAG/Workflow 生产者；M9 扩展其他领域和共享前端 Store | 当前即可重连，后续不需要迁移第二套事件游标 | M9 继续拥有全站生产者、失效矩阵与单例连接，不重复建表/API |
| O-04 | “流式展示”未明确草稿还是阶段 | v1 只流式发送真实阶段和资源状态，不发送未校验文本 delta | 消除草稿被复制、朗读或误认为事实的风险 | 不破坏最终 Answer；未来文本流需新增明确 draft/retract 契约 |
| O-05 | Feedback 未定义重复、撤回和用户唯一性 | append-only + Idempotency-Key；本期不编辑/撤回、不做用户唯一 | 重试不重复写，也不假装已有身份体系 | M10 有身份后可新增 actor 维度，不改现有反馈事实 |
| O-06 | 原始 Source/Web 选项明确存在，但当前无可发布资格/Web Search 闭环 | 保留请求字段与默认 false；无法真实执行时显式拒绝，不静默忽略 | 用户能区分“未授权”和“能力未配置”，不会误信来源范围 | 后续通过新 Evidence policy/Source ingestion 版本扩展，不弱化 approved-only v1 |
| O-07 | M6-02 RAG v1 缺少 Related Topic、Follow-up 和 Query Summary，且产品要求澄清 | 保留 v1 并新增 RAG v2、Query Plan、Clarification 与 Answer retrieval summary | 页面结构完整且刷新可恢复，不靠临时 UI 拼字段 | Agent catalog/Model Call phase/迁移为 additive version；既有 v1 调用方不变 |

## Acceptance Criteria

- [ ] AC-01：创建、列表、打开 Conversation 和提交 Question 的 HTTP/OpenAPI 契约可运行；首次与精确重放分别返回正确状态，冲突不重复创建记录或 Workflow。
- [x] AC-02：Question、Answer pending、Workflow、Outbox、River Job 和初始 Server Event 原子创建；注入任一步失败时全部回滚，commit response-loss 可精确重放。
- [ ] AC-03：公开 Conversation API 经真实 PostgreSQL/River/Worker 完成至少一条 approved-knowledge RAG Answer v2；事实 assertion 有可打开 Citation，Related Topic 受服务端绑定验证，并返回 1..5 个后续问题。
- [ ] AC-04：无证据、未批准证据、Citation 不可打开、Faithfulness 不通过和校验耗尽样本均得到稳定 Refusal；不得产生 completed Answer。
- [ ] AC-05：冲突 Evidence 生成显式 Conflict positions；缺少完整披露时拒答。
- [ ] AC-06：歧义问题产生 Clarification 并可由下一 Question 继续；Query Plan 的 1..3 个 rewrite 与检索摘要在刷新后仍可查询，且正文不进入 SSE/日志。
- [ ] AC-07：最终 Answer/Refusal/Clarification 与 Model Run 终态原子一致；故障和响应丢失测试不存在双发布、重复 Provider 副作用或半完成事实。
- [ ] AC-08：SSE 使用单调 ID；Last-Event-ID 能补发窗口内事件，非法/未来/超窗游标分别返回稳定 Problem；SSE 内容扫描无正文、Secret 和绝对路径。
- [ ] AC-09：刷新或 SSE 中断后，前端从 Conversation/Answer/Workflow Query 恢复；阶段事件和轮询都不会把 pending 草稿标为完成。
- [ ] AC-10：五类 Feedback 可幂等记录；Citation 类绑定受验证；反馈前后正式 Knowledge/Proposal 数量和 Answer 内容不变。
- [ ] AC-11：`/chat/:conversationId` 在桌面和移动端完成创建会话、提问、澄清、观察阶段、显示 Answer/Refusal、打开 Citation、查看检索摘要/Related Topic/后续问题并提交 Feedback；键盘和焦点可用。
- [ ] AC-12：`go test -race ./...`、`go vet ./...`、`go mod tidy -diff`、`make test`、`make rag-integration`、OpenAPI gate、前端 lint/typecheck/test/build 和 `make compose-rag-smoke` 全部通过。
- [ ] AC-13：API、数据库、事件、配置、运行/回滚文档同步；M6-04 的边界不被描述成 M9 全站 UI、M10 Auth 或 M11 最终发布验收已完成。

## Out Of Scope

- 通用模型 Tool Loop、Provider 原生 `tool_calls` 和公共 `/tools/{name}:execute`。
- 将 Answer 自动转换为 Artifact/Proposal；Artifact 事实与 UI 分别由 M8/M9 落地后再接入。
- Conversation 自动写入长期 Memory，或 Feedback 直接修改正式知识。
- 全站页面矩阵、全站事件生产者和统一 Query invalidation 策略；这些属于 M9。
- Session/API Token、CSRF/Origin、公共 Capability Middleware 和真正的用户唯一反馈；这些属于 M10。
- 50 万 Chunk 的 ANN/P95、真实 Provider 质量阈值、全量 Playwright 和最终交付包；这些属于 M10/M11。

## Blocking Questions

无。仓库事实已足够冻结本期边界，剩余未定义项均采用上述保守、可扩展且显式失败的契约。
