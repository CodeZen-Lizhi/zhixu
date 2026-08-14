# Eino AI Runtime 正式生产替换实施计划

## 0. 启动条件

1. 已确认 Tool Calling 与 Token Streaming 的首个产品入口为现有 `/chat` RAG。
2. 新增 ADR-0022，取代 ADR-0021 的生产 No-Go，冻结 Eino-primary、事实源边界和草稿语义；2026-08-11 再明确删除旧 direct Runtime 与运行时回滚面，Git 历史负责恢复。
3. 将本父任务及 RAG、Tool、Streaming 子任务从 planning 切到 in_progress 后再修改生产代码。

## 1. Runtime 基线与依赖收口（4-6 人日）

1. 锁定 core `v0.9.13` 和每个 eino-ext 精确版本，补依赖/升级合同。
2. 定义不含 Eino 类型的 `RAGExecutionScheduler`、`AgentRuntime`、`AnswerStreamRuntime`、draft store、`RunBudgetLedger` 和模型 recording Port；Ledger 必须支持 phase 级调用与输入/输出 Token reservation。先提供 no-tool/no-draft 的合法实现，使后续能力按依赖逐步接入。
3. 建立 import boundary 检查，禁止 Eino 类型进入 Domain、OpenAPI、River 和 PostgreSQL。
4. 抽取共享 `ModelCallRecorder`，设计可重复 `AGENT`、单次 `ANSWER`、流式 STARTED→terminal、canonical message/tool hash、call_no 上限和错误映射；同步 domain、SQL migration 与 repository tests。

## 2. Chat 与 Embedding 切为 Eino 主路径（3-5 人日）

1. 复用已完成的 Eino Chat/Embedding adapter 和 hardened transport，补 ToolCalling/Stream 所需的独立 Chat adapter。
2. 把 config、`.env.example`、Compose 和 Worker 测试默认值改为 Eino；Eino 初始化失败 fail closed。（已完成）
3. 执行 OpenAI-Compatible/Ollama 本地协议测试与真实目标 Provider smoke。（已完成；2026-08-11 `make eino-live-smoke` 实际通过外部 HTTPS OpenAI-Compatible Chat/Embedding、原生 Ollama Embedding 及 Query Plan、metadata、Faithfulness REVIEW。）
4. 生产只保留 Eino；旧 direct 实现和 selector 不作为运行时回滚路径，需恢复时使用 Git 历史或兼容发布制品。

## 3. Structured Scheduler 与 RAG Graph（8-12 人日）

1. 将五类 Structured Scheduler 固定为 Eino Graph，补齐真实消费者合同并移除 direct scheduler。（默认切换、Eino Graph 路由和生产选择面收口已完成）
2. 保留 `RAGExecutor` 作为 Query Plan、Retrieval、Eligibility、Evidence/Topic、生成、Citation/Faithfulness 五个项目领域节点容器；删除其通用 phase switch/branch，由 `RAGExecutionScheduler` 的 Eino Graph 路由。
3. 正式 v2 RAG PLAN 固定为 Prompt `rag-query-plan/v5`、Provider Schema `agent.rag-query-plan/v2` 和最多 256 tokens 输出；Provider 只返回无身份扁平严格短键 `i/r/d/q/s`，不返回 result/schema、`model_run_ref`、`payload` 或 clarification 布尔值。项目从空 rewrites 派生 clarification，严格解码后由 Compose 注入可信 ModelRunRef，并继续产出 canonical domain `RAGQueryPlanResult` v1。v5 明确服务端 scope 已完整绑定，逐字引用、引用证据和格式要求属于下游回答约束；真实 Provider live gate 用 Compose 同形问题断言必须产生非空 rewrites。（已完成）
4. 旧 Prompt `rag-query-plan/v3` 与 Schema `agent.rag-query-plan/v1` 只为历史 v1 RAG 执行/回放注册；正式 v2 新流量不得选择该组合。（已完成）
5. 收口 phase 输出预算：PLAN 最多 256，Agent 内部短轮最多 96，最终 ANSWER 保留冻结 Profile 上限，metadata/REVIEW 按必需输出基数动态估算并由 Profile 收紧；REVIEW 保留 1024 的 reasoning Provider 最低授权，但不把 1024/2048 当作固定 ceiling 截断合法长答案或高基数严格 JSON。（已完成）
6. 在 `internal/agent/adapter/eino` 构建并启动时编译核心 RAG Graph，先组合现有 Eino structured Answer 节点；该阶段不依赖 Tool Agent 或 draft store。
7. 让 `RAGWorkflowExecutor` 继续拥有 replay、Memory/ModelRun 和 finalizer，并通过 `RAGExecutor.Execute` 调用注入的 `RAGExecutionScheduler`。
8. 验证 clarification/refusal/completed、degradation、active-index 漂移、取消、progress 和 terminal response-loss 等价。
9. 保留 `RAGExecutor.Execute` 作为领域节点容器入口；删除 Runtime 实现不影响领域节点、持久回放和证据链事实。稳定观察继续作为独立发布质量门禁，不是保留第二套 Runtime 的前置条件。

## 4. Eino Tool Agent（7-10 人日）

1. 用经典 `schema.Message` ChatModelAgent/ToolsNode 实现项目 `AgentRuntime`；由 Eino recording model 记录每次 Generate/Stream，禁用 ModelRetry/Failover，并完整消费/关闭 `AsyncIterator[AgentEvent]` 及嵌套 MessageStream。
2. 从冻结项目 Catalog 生成 Eino Tool schema，建立 Tool wrapper 到 `ExecutionService` 的唯一桥接。
   模型 schema 只暴露 `E*/C*` 短引用；bridge 在项目侧恢复完整 Citation tuple，并在返回模型前删除全部服务端身份。
3. 首版只启用顺序只读工具；补项目 call number、受限 provider call ID 校验/临时关联、预算、unknown tool、
   无效参数和取消处理。（已完成；Provider ID 由 Eino Tool middleware 在执行前有界校验并临时关联项目 `CallNo`，
   事件消费结束前再做 ID/工具名一一对应与无悬空复核；传给项目 Tool Port 的 Context 会保留当前取消/deadline 和
   原始项目值但移除 Eino tool-local values；canonical ModelCall 文档把 Provider ID 改为本地顺序引用，同名多版本工具在
   调用前拒绝，ID 不进入项目 DTO、持久化或日志。事件校验失败会取消本次 Agent 派生 Context，并排空 iterator、
   关闭尚未消费的嵌套 MessageStream。）
4. 当前 `/chat` 产品路径固定使用已经具备可重放 receipt 的 `ReadSource@2`/`ValidateCitation@2`；
   `SearchKnowledge` 会扩大冻结 Evidence 集并要求新增动态短引用、持久 result receipt、加密与保留策略，属于独立产品
   开放门禁，不是 Eino Tool Calling 替换的完成条件。未单独立项并完成这些合同前继续不进入模型 allowlist。
5. 将 Agent Runtime 接入核心 RAG Graph；当前 `/chat` 对成功只读工具使用 Eino `ReturnDirectly`，随后调用不绑定工具的 Eino `ChatModel.Stream` 生成最终正文，并先在 Worker 内完整消费，不依赖浏览器流能力。通用 Runtime 继续用独立测试覆盖无 `ReturnDirectly` 的多轮 ReAct。
6. 接入 Attempt 级共享 Budget Ledger，证明 Agent 不会耗尽 ANSWER、metadata envelope 和 REVIEW 的预留调用；验证真实 PostgreSQL/River duplicate delivery、lease reclaim、ToolCall receipt 和 ModelCall recording。
7. 保持写工具不可达；需要写入的结果只创建 Proposal。新 Attempt 可重复只读调用，但不得把旧 call_no 错配到新 Agent transcript。

## 5. Provider 到浏览器的真流（8-12 人日）

1. 定义短期 draft session/chunk PostgreSQL schema、`ACTIVE/COMPLETED/DEGRADED/PUBLISHED/ABORTED/SUPERSEDED` 状态机、generation/sequence/fence、上限、TTL、cleanup 和 SQL 不变量。
2. 实现 `Begin/Append/Close` 的 Runtime Claim + Attempt/fence CAS；lease reclaim 原子 supersede 旧 generation，旧 Worker 不能继续 append。
3. 在 tool-free Eino final-answer `ChatModel.Stream` 上实现唯一 consumer；每个 Provider frame 先拒绝 ToolCalls/非法内容再写 draft，不缓冲整流；用固定帧数/字节的非阻塞队列连接短超时批量 writer，sink 降级时继续 drain、Close 和最终 concat。非法尾帧必须使 ModelCall `FAILED`、session `ABORTED`、SSE reset/end，且不发布 metadata 或正式 Answer。
4. 新增独立 Answer stream SSE endpoint、认证/Workspace 绑定、generation-aware `Last-Event-ID` reset 和终态关闭语义；不改造 durable `/events`。
5. 将 Citation/Faithfulness/finalizer 与 draft 收口：最终 Answer 提交与 `PUBLISHED` 同一 terminalization transaction；验证失败、取消、拒答或澄清的终态提交与 `ABORTED` 同一 transaction，SSE 对后两者分别 end/refetch 或 reset/end。
6. 前端增加独立 draft reducer/client，在 pending Answer 中显示草稿；最终 REST Answer 原子替换或丢弃草稿。
7. 覆盖旧 Worker reclaim 后 append、双 Worker、旧 cursor、EOF 后 Citation 失败、COMPLETED 后重连、finalizer response-loss、断连、显式取消、Worker shutdown、慢存储、队列满、流内错误、TTL、超限和 goroutine/内存有界测试。
8. 真实 Provider + Worker + API + 浏览器验证多帧、首次草稿可见延迟和完整完成延迟，不能用 httptest 单帧代替；Provider 首 Token 延迟由 `agent.answer.first_token.duration_ms` 指标独立记录。（已完成；2026-08-12 当前树以外部 OpenAI-Compatible Chat host relay + 原生 Ollama Embedding 实际通过 Eino Graph/Agent/ToolsNode/ANSWER Stream、metadata、REVIEW、PostgreSQL draft、API SSE 及桌面/移动浏览器多帧终态；最终指纹化网络扫描版本的桌面/移动分别在正式发布前观察到 2/8 个连续非空 SSE chunk 事件。）
   浏览器实现还必须在两端从首次草稿到正式终态持续拒绝动态 session/CSRF canary，正式 Answer 同样检查；draft SSE 网络响应通过有界、不落正文的透传扫描器执行同一 canary 检查；回执使用精确字段集合，失败诊断只输出无正文的固定分类/计数。该代码门禁的后续修改仍需重新执行真实 Provider，历史回执不能替代重跑。
   门禁入口支持独立 Chat/Embedding Provider 选择和 `direct|host-relay` 网络传输；preflight 在不联网、不输出
   endpoint/Credential 的前提下锁定 app/worker Eino 装配。host-relay 成功不冒充容器直连外部 HTTPS 网络路径，
   外部 HTTPS Embedding 由独立 `make eino-live-smoke` 真实门禁证明。

## 6. Eino-only 收口与恢复（4-7 人日）

2026-08-11 用户明确决定删除旧 direct Runtime，不等待稳定观察周期，也不保留 Eino→direct 回滚编排。稳定观察、真实
Provider 和浏览器门禁仍然是发布质量证据；它们用于判断 Eino 版本是否可发布，不再授权保留第二套 AI 实现。

1. 保留首 Token、完成延迟、错误、Agent iteration/Tool Call、Graph node、拒答和 stream degradation 等 Eino
   指标；删除 legacy direct activation metric。正式 OTLP/HTTP Metrics+Trace Provider 继续在 API/Worker Composition
   注入，稳定观察按 [`Eino Runtime 稳定发布观察 Runbook`](../../../docs/architecture/runbooks/eino-stable-observation.md)
   独立验收。受保护采集器固定 Prometheus/Tempo 查询、backend URL/path、阈值和信任根，每份 evidence HMAC 绑定完整
   manifest 及实际 `collected_at`；每日查询只能在对应窗口结束后的同一观察时区自然日内完成，逾期历史补采由采集器和
   独立 verifier 双重拒绝。每日续写、最终签发和 verifier 都重验 evidence。策略默认 `unconfigured` 时返回 `incomplete`，
   不能用本地 fixture、Compose 或人工 manifest 判 PASS。
2. `make eino-stable-observation-preflight` 以零网络、零写入方式复用 `start` 的策略、受保护文件、空归档、时区、key 与
   发布 hash 校验，并对最长 366 天归档所需的全部本地午夜执行 UTC 往返/歧义校验；`start` 有序依赖并再次执行同一
   readiness。preflight 成功不得被记录为 Collector 可达或观察已开始。
3. 删除部署选择面：移除 Chat、Embedding 和五个 Structured Scheduler 的 `direct|eino` Config/YAML/env selector、
   direct 枚举、回滚脚本和 Compose overlay；Compose/runtime contract 对退休 selector 显式 fail closed。`RAG_REAL_PROVIDER_TRANSPORT`
   仍保留 `direct|host-relay` 两种网络传输模式，不能与旧 AI Runtime 混淆。
4. 删除 direct Provider 与编排入口（Go 生产包由对应实现者完成）：移除 direct provider constructor/factory 分支、
   `directStructuredPhaseScheduler`、`directRAGExecutionScheduler`、nil scheduler fallback、direct drain guard、Claim
   exclusion 和 legacy runtime 条件。Eino 构造失败必须 readiness fail closed；测试只可注入 Port test double。
5. 保留持久兼容边界：`agent-rag-answer@1` Definition、graph hash、严格 decoder、Worker resolver、必要的
   `executeRunV1` 和迁移前 `agent-rag@1` Tool replay 合同继续由 Eino Model/Scheduler 消费，直到生产库查询、备份/保留策略
   和迁移证明确认历史执行不再需要。不得删除已完成 Run、ModelCall、Answer、receipt 或审计数据，也不得执行历史 migration down。
6. 将历史 Eino→direct→Eino 演练、旧 ADR/PoC 和稳定观察归档标记为历史证据，不把它们当作当前部署入口；需要回到旧实现时，
   通过 Git 历史或兼容发布制品恢复整套应用版本与配置。
7. 收口 Eino Adapter 合同、历史 v1 Eino-backed replay、Eino-only Composition、Compose/Provider smoke 和文档；删除
   selector/direct rollback 专项测试，保留网络 transport `direct`、数据库/Workflow rollback_plan 等领域语义。
8. 删除后执行受影响 Go、race/vet、SQL/真实 PostgreSQL/River、OpenAPI、前端、Compose、真实 Provider、浏览器门禁和
   `git diff --check`；代码搜索应证明非测试生产路径不存在 direct AI selector、direct Provider constructor、direct scheduler
   或自动 fallback。外部 Provider live gate 和 metadata/REVIEW/browser 终态已完成；真实稳定观察仍按 PRD 记录为独立未完成项。

## 7. 验证门禁

- Go：受影响 package tests、`-race`、`go vet`、`go mod tidy -diff`、vendor build、Go review。
- SQL：migration/repository integration、duplicate/replay/fence/TTL/cleanup，并执行 SQL review。
- API：OpenAPI breaking check、SSE contract、认证/Workspace 隔离、错误脱敏。
- 前端：unit/integration、build、唯一 draft owner、桌面 1440×900 与移动 390×844 浏览器 smoke、console/network 检查。
- 系统：真实 PostgreSQL/River 重投递、Provider live smoke、Worker→API→浏览器端到端流、`git diff --check` 和 Trellis check。

## 8. 依赖关系与工期

```text
ADR/Port/Budget -> Chat+Embedding primary -> Core RAG Graph
                                            -> Tool Agent + final-answer Stream
                                            -> Cross-process draft Streaming
                                            -> Eino-only release and Git-based recovery
```

单人实现预计 40-60 人日，日历时间约 8-12 周；独立稳定发布观察可在 Eino-only 版本上继续执行，不阻塞旧实现删除。三个月仍是合理计划，不应承诺三周内把跨进程流、Tool receipt、RAG 等价、Eino-only 收口和真实发布证据全部做完。

## 9. Bug Analysis: direct-return Agent 合同漂移

### 1. Root Cause Category

- **B - Cross-Layer Contract**：产品 RAG 为只读工具配置 Eino `ReturnDirectly` 后，运行时合法序列变为一次
  `AGENT` ModelCall 后进入独立 `ANSWER` Stream；Compose smoke 和部分文档仍把 Tool Call 次数误等同为
  Agent 模型轮次，继续要求两个 `AGENT`。
- **C - Change Propagation Failure**：Agent 配置、fixture、数据库断言、发布聚合目标和架构材料没有作为同一变更面同步。
- **D - Test Coverage Gap**：通用多轮 ReAct 单测与产品 direct-return 集成测试都存在，但发布脚本使用硬编码旧序列，且
  原 `eino-live-smoke` 没有聚合 PLAN、metadata 和 REVIEW 三个正式阶段门禁。

### 2. Why The First Signal Was Misleading

Compose 首次失败表现为 ModelCall 序列不符，容易误判为 Eino 提前终止或 Ledger 少记一次调用。数据库事实显示
Workflow、Answer、Tool receipt、draft publication 和五次 ModelCall 全部成功，才将根因收敛到测试预期漂移；
不应为了满足旧断言在 Agent 内增加一次无业务价值的模型调用。

### 3. Prevention Mechanisms

| Priority | Mechanism | Specific Action | Status |
|---|---|---|---|
| P0 | Contract test | Compose v2 精确断言 `PLAN,AGENT,ANSWER,INITIAL,REVIEW`，通用 Runtime 单独断言无 `ReturnDirectly` 的两轮 ReAct | DONE |
| P0 | Release gate | `eino-live-smoke` 先串行校验完整环境，再执行 Chat、两类 Embedding、PLAN、metadata 和 REVIEW；离线脚本同时校验六项目标和 `make -j` 缺配置时零 Provider 调用 | DONE |
| P1 | Specification | 在 Eino runtime 与质量规范中区分 Tool execution count、Agent iteration 和 ModelCall phase | DONE |
| P1 | Documentation | 架构、测试与活动任务统一说明产品优化不代表 Eino 不支持循环 | DONE |

### 4. Systematic Expansion

- 任何 `ReturnDirectlyTools`、Tool allowlist、阶段预算或 Graph 分支变化，都必须同时检查 ModelCall 序列、Ledger 预留、
  Compose 断言、观测指标和发布聚合目标。
- 精确序列只属于当前冻结产品配置；框架能力必须由独立通用合同测试证明，不能从一个产品路径反推。
- 发布聚合目标必须有离线依赖合同测试，并覆盖并行调用下的 fail-fast，避免新增 live gate 后总入口继续假绿或在环境不完整时意外联网。

### 5. Knowledge Capture

- [x] 更新 `.trellis/spec/backend/eino-runtime-adoption-gates.md`。
- [x] 更新 `.trellis/spec/backend/quality-guidelines.md`。
- [x] 更新架构、测试、活动任务与 Compose smoke。
- [x] 新增 `deploy/eino-live-smoke-contract.sh`。
- [x] 项目无 `src/templates/markdown/spec/` 镜像目录，无模板同步项。
- [ ] 用户未授权创建 Git commit，本轮不提交。

## 10. Bug Analysis: reasoning-inclusive usage 超出单次预留

### 1. Root Cause Category

- **B - Cross-Layer Contract**：Provider 的 `completion_tokens` 包含不可见 reasoning token，但 Application Ledger
  把请求 `MaxOutputTokens` 的单次 reservation 当成实际计费用量的硬上限。
- **D - Test Coverage Gap**：原测试只覆盖实际 usage 小于预留和突破预留即失败，没有覆盖“高于单次预留、但仍落在
  保留下游额度后的 Attempt 总预算内”的合法真实 Provider 形状。
- **E - Implicit Assumption**：默认假设 `completion_tokens <= max_tokens`；真实 live 数据为 input `391`、output `1276`，
  而 PLAN 请求上限为 `512`，官方 Chat Completions 语义也明确 reasoning 计入 output/completion usage。

### 2. Why The First Signal Was Misleading

ModelCall 已持久为成功，随后 NodeAttempt 才报 `AGENT_RUN_BUDGET_SETTLEMENT_INVALID`，容易误判为 Eino Graph、
Provider schema 或调用顺序错误。用真实 Provider 只记录非敏感 token 计数后，才确认错误发生在成功调用后的 Ledger 结算。

### 3. Prevention Mechanisms

| Priority | Mechanism | Specific Action | Status |
|---|---|---|---|
| P0 | Runtime invariant | 单次预留超额只可使用未预占的 Attempt 总预算；其他 in-flight 与下游 reservations 不可借用 | DONE |
| P0 | Audit | ModelCall 与 Ledger 都按 Provider 完整 usage 结算，不减去 reasoning token | DONE |
| P0 | Regression | 固定 `391/1276` 形状覆盖 Ledger 与 RecordingChatModel，另保留突破剩余总预算的 fail-closed 用例 | DONE |
| P1 | Specification | 同步 Eino Runtime 规范和本设计，区分请求内容上限、单次预留与实际计费用量 | DONE |

### 4. Systematic Expansion

- PLAN、AGENT、ANSWER、metadata 和 REVIEW 共用同一结算规则；不能只在结构化 Chat 绕过错误。
- 调整 Provider token 参数不能替代账本修复：OpenAI-compatible 与 Ollama 支持面不同，完整 usage 仍可能因缓存、reasoning
  或 Provider 计数语义偏离请求上限。
- 任何 usage 兼容修复都必须保留真实计费事实、下游预留和 Attempt 总上限，禁止通过丢弃 usage 或按请求上限截断来假绿。

### 5. Knowledge Capture

- [x] 更新 `.trellis/spec/backend/eino-runtime-adoption-gates.md`。
- [x] 更新活动任务 design/implement 与回归测试。
- [x] 项目无 `src/templates/markdown/spec/` 镜像目录，无模板同步项。
- [ ] 用户未授权创建 Git commit，本轮不提交。
