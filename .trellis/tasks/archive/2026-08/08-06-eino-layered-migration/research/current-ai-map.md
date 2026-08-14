# Research: 当前生产 AI 链路与 Eino 分层迁移地图

- Query: 审计当前生产 AI 链路，确定哪些实现适合替换或包裹为 Eino、哪些项目适配器必须保留，以及 RAG、Artifact、Capture、Organizing 和其他调用方的影响面。
- Scope: internal / mixed（以仓库源码、测试、架构文档和本地 Eino PoC 为主）
- Date: 2026-08-06

> 状态：这是 ADR-0019 与生产实现落地前的审计快照。当前结果以 ADR-0019、ADR-0020、ADR-0021、
> 修订版 `implement.md` 和各 08-08 子任务为准；下文“主模块没有 Eino/未正式采用”的陈述仅代表当时基线，
> 不代表当前工作树。

## Findings

### 1. 当前结论和总边界

主模块目前没有 Eino 依赖：`go.mod`/`go.sum` 中没有 `cloudwego/eino`；Eino 只存在于独立 `poc/eino` module。现行 ADR 明确规定 Eino 只能位于 Agent/Application 的短流程边缘或 Adapter/Infrastructure 内部，Domain、Workflow 持久化、Proposal/Approval、Tool Permission 和写授权不能依赖 Eino 类型；River/PostgreSQL 继续拥有持久化 Workflow，所有框架输出仍要经过项目领域校验（`docs/architecture/adr/0013-eino-adoption-gate.md:11-16`）。

当前正式技术选型仍是项目自有 Application 编排、直接 OpenAI-Compatible Chat Adapter、批量 Embedding Adapter 和 JSON Schema + 领域校验（`docs/architecture/technology-stack.md:64-80`）。ADR 的 M2 结论是 Streaming、真实 Eino Structured Output、Embedding/Retriever/Rerank、River Node 及真实 Provider Smoke 尚未通过门禁，因而主模块不正式采用 Eino（`docs/architecture/adr/0013-eino-adoption-gate.md:20-23`）。

独立 PoC 锁定的基线是 Eino `v0.9.12` 与 OpenAI 扩展 `v0.1.13`（`poc/eino/go.mod:5-8`）。PoC 仅对 Chat Graph、ToolsNode、Callback 和部分配置边界通过；实际 `schema.StreamReader` 链路、Eino Structured Output、Eino Embedding/Retriever/Rerank、River Node 均未通过或未接入（`poc/eino/report.md:5-28`）。因此本文件给出“可在新 ADR/完整门禁后迁移”的分层地图，不把旧 PoC 结论当作已授权的生产实现。

当前生产图可以压缩为：

```text
静态/Managed Model Settings
        |
        v
modelsettings/runtime.Build
        |
        v
platform/models.ModelRuntime  (进程内一次构造、不可变)
        |-------------------------------|
        v                               v
项目 ChatModel + ChatContract       项目 Embedder + EmbeddingRuntimeContract
        |                               |
        v                               v
Agent Structured/Plan/Review         Retrieval SearchService / VectorBuilder
        |                               |
        +--> Relation / RAG /          +--> API、Worker、Artifact、Organizing
             Artifact / Capture /
             Organizing
        |
        v
RecordingChatModel -> ModelRun/ModelCall Repository
        |
        v
各领域验证、Proposal/Answer/Revision Finalizer
        |
        v
River/PostgreSQL Workflow Executor
```

### 2. 稳定项目端口和当前所有权

#### Chat

- `internal/agent/application/chat.go:35-76` 定义项目自有 `MessageRole`、`ChatMessage`、`ChatRequest`、`ChatResponse` 和 `ChatModel`。请求包含 Phase、Profile/Prompt/Schema 引用、模型身份、消息、原始 JSON Schema 和输出 Token 上限；注释明确禁止把 Provider SDK 类型暴露给上层。
- `internal/agent/application/chat.go:78-128` 在 Application 边界校验身份、消息顺序、Schema、内容和 usage，并要求响应回显与请求完全相同的 `ModelRef`。任何 Eino Chat 实现都必须在此端口外转换，不能把 Eino `schema.Message`、Provider response 或错误类型传播到领域层。

#### 结构化短流程

- `internal/agent/application/runner.go:88-107` 的 `StructuredRunner` 持有 `ChatModel`、冻结 `RuntimeCatalog` 和 `RunBudget`。
- `internal/agent/application/runner.go:109-199` 手工实现 INITIAL -> REPAIR -> REDUCED 的固定三次调用：每次创建有界请求、设独立 call deadline、累加请求/响应字节和 Token、调用 `model.Chat`、严格解码，并在第三次后停止；Provider 错误不由 runner 重试。
- `internal/agent/application/query_plan.go:38-108` 的 `QueryPlanner` 是一次 PLAN 调用；它绑定服务端 `ModelRunRef`，严格解码并拒绝引用漂移。
- `internal/agent/application/faithfulness.go:46-134` 的 `FaithfulnessReviewer`/`StructuredFaithfulnessReviewer` 是一次独立 REVIEW 调用；其结果必须匹配被审 Answer 的 `ModelRunRef`，并由后续发布门禁检查覆盖完整性。
- `internal/agent/application/relation.go:36-50` 暴露 `RelationRunner` 端口，明确要求遵守三阶段预算；Relation Analyzer 在调用 runner 前后做正式 Claim、Evidence Eligibility、Applicability 和安全动作映射（`:85-157`）。

#### 记录和持久化

- `internal/agent/application/recording_chat.go:24-65` 定义 `RecordingChatModel`，它装饰任意 `ChatModel`，并为一个 Model Run 分配连续 Call No。
- `internal/agent/application/recording_chat.go:67-141` 在 Provider 前写入 STARTED ModelCall，Provider 返回后用 CAS 写 SUCCEEDED/FAILED；结果持久化不确定时返回 `manual_recovery`，不能自动再调 Provider。
- `internal/agent/application/runtime_repository.go:50-65` 是 ModelRun/ModelCall 的唯一持久化边界。架构文档也明确 Model Run/Call 与 Workflow Run/Node Run 是两套事实，不能互相替代（`docs/architecture/interfaces-and-adapters.md:75-85`）。

#### Embedding 和 Retrieval

- `internal/retrieval/application/embedding.go:13-19` 定义唯一批量 `Embedder` Port；`Contract()` 返回不含密钥的绑定，`Embed()` 必须按输入顺序返回同数量向量，禁止 Adapter 内逐条远程调用。
- `internal/retrieval/application/embedding.go:32-72` 统一校验 batch/input/UTF-8、模型和数量、维度、归一化和向量契约。
- `internal/retrieval/application/search.go:30-83` 的 `QueryEmbedder`/`SearchService` 负责 Active Index、Keyword/Semantic/Hybrid 融合和可选 Rerank；查询只向 Embedder 发一个元素的批次（`:232-252`）。
- `internal/retrieval/application/vector_builder.go:22-67` 把 Embedder Contract 冻结进 VectorBuilder，`:79-176` 每页最多一次批量 Provider 调用，并把缓存和 Projection 终态原子提交。这里是项目自己的索引一致性编排，不是通用 AI 框架编排。

### 3. 建议替换或包裹的层

| 层 | 当前实现和入口 | 建议的 Eino 形态 | 受影响面和硬约束 |
|---|---|---|---|
| Chat Provider Adapter | `internal/platform/models/chat_factory.go:11-24`；`chat_openai.go:29-85`；HTTP 细节在 `chat_http.go:66-139`、`:191-285` | 用 Eino OpenAI-Compatible `ChatModel` 作为内部 transport/model，外包一层项目 `ChatModel`/`ChatContract` 适配器；Factory 仍返回项目接口 | 保留 `ChatContract` 的模型/超时/请求响应上限、JSON Schema strict response format、单次调用语义、usage 和稳定错误分类。现有 `model_transport.go:37-67`、`:111-200` 的禁代理、TLS/重定向、DNS pinning 和地址限制必须继续生效，可作为 Eino HTTPClient 或外层 Transport 注入。 |
| OpenAI-Compatible Embedding | `embedding_factory.go:10-23`；`embedding_openai.go:31-84`；共享 HTTP/校验在 `embedding_http.go:77-216` | 先做 Eino Embedding Adapter 的可选实现，转换回项目 `Embedder`；只有通过现有 Contract Test 才切 Factory | 必须保留批量数量、输入预算、`data.index` 完整且无重复的排序恢复、模型/维度/归一化、错误分类和 response byte limit。Eino 返回类型或 float 精度转换不能改变 `[][]float32` 结果语义。 |
| Ollama Embedding | `embedding_ollama.go:29-67` 调原生 `/api/embed`，显式 `truncate=false` | 暂时保留直接 Adapter；若 Eino 组件能覆盖 native 协议，再另做同一 `Embedder` Port 的 Adapter | Eino OpenAI 扩展并不等于 Ollama native `/api/embed` 兼容；必须验证 `truncate=false`、批量顺序和 Contract hash 后才能替换，不能因为 Chat 兼容就一起切换。 |
| Structured Runner | `internal/agent/application/runner.go:109-199` | 保留 `StructuredRunner`/`RelationRunner` 这类项目 Port，在实现内部或新增同端口实现 `EinoStructuredRunner`；由 Composition Root 选择实现 | Eino 只能负责短流程调用编排，不能取代项目的 Schema Decoder、字节/Token/总超时预算、三次上限、redacted validation code、Provider error passthrough 和 `ModelRunRef` 绑定。当前各领域直接调用 `NewStructuredRunner`，需要注入 runner/factory 或保留同名 facade。 |
| PLAN/REVIEW 单调用 | `query_plan.go:52-108`；`faithfulness.go:70-134` | 分别包成 Eino 单节点/短 graph，再转回 `QueryPlanRunResult`/`FaithfulnessReviewRunResult` | PLAN 的服务端引用绑定、REVIEW 的独立 Schema 和完整覆盖验证必须留在项目边界；不应让 Eino Graph 直接产出可发布 Answer。 |
| RAG 内层短流程（可选、后置） | `internal/agent/adapter/workflow/rag_executor.go:217-281` 在 `executeRun` 内组装 RecordingChat、Planner、Runner、Reviewer、CitationValidator、AnswerPublisher 和 Application `RAGExecutor` | 只考虑把 `executeRun` 中的 PLAN/ANSWER/REVIEW 调用编成 Eino graph，外层 `RAGWorkflowExecutor` 和 Application gates 不变 | 这是最高风险的可选迁移：检索作用域、正式 Evidence Eligibility、Topic 解析、Citation tuple、Faithfulness、拒答和 Answer finalizer 均是业务授权，不可移入 generic Eino RAG。先保持现有 ports，图的输出必须回到 `RAGTerminalProposal` 并再次验证。 |
| Retrieval/Rerank/Search | `SearchService`、`VectorBuilder` 及 `agent/adapter/retrieval` | 当前不替换为 Eino；最多把 Eino Retriever/Reranker 放在项目 Port 后面做独立实验 | PoC 对 Eino Retriever/Rerank 尚未通过；Active Index、租户/Workspace 过滤、RRF、degraded 标记、Citation 身份和持久化 Projection 都由项目实现拥有。 |

### 4. 必须保留在 Eino 外部的适配器和业务层

1. **`RecordingChatModel` 和 ModelRun/Call Repository**：它们是 Provider 调用前后的崩溃恢复栅栏。Eino callback 可以补充 trace/metrics，但不能替换 STARTED、CAS 完成、UNKNOWN 和人工恢复事实；最稳妥的结构是 `RecordingChatModel -> Eino-backed ChatModel`。
2. **RuntimeCatalog/Prompt/Schema/ModelProfile**：`Catalog.Snapshot` 冻结实际运行时引用。每次 ModelRun 必须记录实际 Model/Profile/Prompt/Schema 和 max output tokens；Eino 的 model config 不能成为版本事实源。
3. **ModelRuntime 和 modelsettings/runtime**：`internal/platform/models/runtime.go:126-170` 只构造一次进程级 Chat/Embedding capability；`internal/modelsettings/runtime/models.go:91-109` 的 `Build` 仍是唯一 managed/static composition 入口，`:172-240` 的 ConnectionTester 直接通过这些 Port 做最小请求。Eino 只能替换 Factory 内部，不应绕过 revision、credential scrub 或 capability state。
4. **Retrieval/Knowledge/Topic/Memory 适配器**：`internal/agent/adapter/retrieval/adapter.go:25-44`、`:47-88` 只桥接 Retrieval Application，并展开/校验 Citation（接口断言在 `:242-243`）；`internal/agent/adapter/knowledge/adapter.go:18-75` 和 `topic_adapter.go:13-49` 只暴露最小 Knowledge seam；`internal/agent/adapter/memory/loader.go:14-57` 固定 single-user owner、作用域和分类映射。这些适配器承担权限/事实源边界，不能被 Eino Retriever/Memory 类型取代。
5. **领域验证、发布和持久化 Workflow**：Relation Analyzer、RAG `RAGExecutor`、Artifact/Capture/Organizing Generator、Citation/Faithfulness/Eligibility 校验、Answer/Revision/Proposal Finalizer 都要继续由项目 Application/Domain 负责。`internal/workflow/application/executor_registry.go:55-58` 只接受项目 `Executor`；`internal/workflow/adapter/river/runtime_worker.go:148-171` 从 River claim 后解析项目 Executor 并传递可信 `ExecutionContext`。Eino Graph 不能拥有 lease、retry、Human Task、补偿或 durable node 状态。

### 5. 受影响调用方和组合入口

| 迁移点 | 直接调用方 | 事实依据和影响 |
|---|---|---|
| Chat Adapter Factory | `platformmodels.NewConfiguredModelRuntime`（`internal/platform/models/runtime.go:133-170`）；managed `runtime.Build`（`internal/modelsettings/runtime/models.go:91-109`）；ConnectionTester（`:201-240`） | Factory 变更会影响所有 Chat capability，但上层仍拿到同一 `ChatModel`/`ChatContract`。必须保留 model revision、contract identity 和 disabled 状态。 |
| Chat composition | Worker `newAgentWorkflowComponents`（`cmd/worker/main.go:1950-2065`）、Capture Profile（`:2076-2115`）、Artifact（`:2135-2221`）、Organizing（`:1401-1419`） | Relation、RAG、Capture、Artifact、Organizing 都共享同一个冻结 Chat runtime；不要在各领域各自创建 Eino model。Worker 还用 `agentApplicationBudget` 把单 Adapter limits 放大到三次结构化调用（`:2268-2273`）。 |
| Structured runner implementation | Relation Workflow `internal/agent/adapter/workflow/executor.go:129-170`；RAG `rag_executor.go:217-281`；Artifact `internal/artifact/workflow/executor.go:361-404`；Capture `internal/capture/profile/generator.go:170-230`；Organizing `internal/organizing/workflow/generation.go:240-330` | 这些调用方当前直接 `NewStructuredRunner`。若采用新的 Eino 实现，需要增加共享 runner factory/port 或保持 facade；否则会产生五处不同迁移路径和不一致预算。各自的 ModelRun 绑定、证据输入和终态写入不能移动到 runner。 |
| Embedding Adapter Factory | `internal/platform/models/embedding_factory.go:10-35`、`runtime.go:153-168` | 变更会影响 API/Worker 读取的共享 `models.Embedding().Embedder()`；contract 必须与已持久化 Embedding Version 一致。 |
| Embedding 查询/索引 | SearchService（`internal/retrieval/application/search.go:65-105`、`:232-252`）；VectorBuilder（`vector_builder.go:79-176`）；Worker Source Processing（`cmd/worker/main.go:2292-2370`） | Query 是单批调用，Index build 是每页一次批量调用并原子提交；Eino 只能在 Provider Adapter 内，不应改变批量和事务外调用边界。 |
| Embedding 的业务复用 | Worker Agent RAG/Relation composition `cmd/worker/main.go:2029-2057`；Artifact `:2195-2214`；API Retrieval/Organizing handlers `cmd/api/main.go:477-545`；Worker tool search `cmd/worker/main.go:1709` | 这些调用方只依赖 Retrieval Application/Agent Adapter，不应直接引用 Eino embedding 类型。 |
| RAG graph（后置） | 仅 `RAGWorkflowExecutor.executeRun` 以及它调用的 Application RAG ports | 外层执行仍由 `RAGWorkflowExecutor.Execute` 负责恢复、Memory snapshot、ModelRun 和 finalizer（`rag_executor.go:62-170`）；Eino graph 迁移只会改变短流程内部实现，但会触及 RAG 全套集成测试。 |

### 6. 推荐迁移顺序和停止条件

1. **先解决架构授权**：当前 ADR-0013 明确“不正式采用”。若要把 Eino 放入主模块，先新增/更新 ADR，写明采用版本、回退方式、门禁和依赖边界；不要仅凭 PoC 目录切换 `go.mod`。
2. **先做 Chat Adapter 的无业务行为替换**：实现 Eino-backed adapter，保留现有 `ChatModel`/`ChatContract` 和 `model_transport` 安全层；先跑现有 Chat contract、transport、runtime tests，再切 Factory。只要严格 JSON Schema、usage、model echo、错误分类或安全策略有一项不等价，就保留 direct HTTP。
3. **单独验证 Embedding**：优先 OpenAI-Compatible batch adapter；Ollama native 继续 direct，直到 `truncate=false`、排序、维度、归一化、hash 和错误语义都有 contract test。Search/VectorBuilder 不随 Provider Adapter 一起改。
4. **抽出共享 Runner 选择点**：增加项目拥有的 runner factory/port，或让现有 `StructuredRunner` facade 委托 Eino 实现；一次只迁移一个调用域。每次必须比较三阶段调用次数、Schema/Prompt refs、request hash、ModelCall 序列和最终业务输出。
5. **最后才评估 RAG 内层 graph**：先证明 Planner/Runner/Reviewer 都可通过项目 ports 被 Eino 实现，再把 `executeRun` 内部做有限 graph。任何检索或发布门禁失败都回退到现有 Application `RAGExecutor`。
6. **明确不在本轮迁移的能力**：生产没有 Eino Streaming、Tool Loop、Rerank 或 Eino Retriever 消费者；不要为了“全链路 Eino”添加未请求的协议或持久化改造。

### 7. 现有测试锚点（迁移验收必须保持）

| 领域 | 关键测试文件/行 | 应保持的不变量 |
|---|---|---|
| Chat Adapter/Transport | `internal/platform/models/chat_contract_test.go:22,114,158,178,207,255,270,290,322`；`model_transport_test.go:28,65,89,105,156,190,234,285` | 成功协议、Provider status 不自动重试、redirect/timeout/cancel、malformed response、tool call 拒绝、敏感信息脱敏、DNS/Proxy/TLS/地址限制。 |
| Embedding Adapter | `internal/platform/models/embedding_contract_test.go:34,225,264,288,325`；`embedding_factory_test.go:15,101` | OpenAI `data.index` 恢复、重复/越界 index 拒绝、Ollama batch order 和 `truncate=false`、URL/secret 边界、disabled/unsupported provider。 |
| Shared runtime | `internal/platform/models/runtime_test.go:13,52,90`；`cmd/worker/main_test.go:144,272,400`；`cmd/api/main_test.go:87,266` | 单一冻结 runtime、能力禁用显式化、预算乘以三次调用、API 不重复构造 Factory、Embedding 能力组合不泄漏密钥。 |
| Structured Runner | `internal/agent/application/runner_test.go:19,44,74,96,115,146,162,180,218,258,283` | INITIAL/REPAIR/REDUCED 精确 3 次上限、只传 redacted error code、不转换 decoder 原文、不吞 Provider error、取消/总超时/请求响应 Token budgets、PLAN/REVIEW phase 合法。 |
| PLAN/REVIEW/Recording | `query_plan_test.go:17,37,53,89,114`；`faithfulness_test.go:15,48,72,102,128,151`；`recording_chat_test.go:14,69` | ModelRunRef/Schema identity、独立 REVIEW、完整覆盖、Provider failure no fallback、Call No 连续、ModelCall 先 STARTED 后 CAS terminal。 |
| Relation/RAG | `internal/agent/application/relation_test.go:17,66,83,116,134,173,217,249,265`；`rag_test.go:14,32,47,60,80,95,108,125,179`；`internal/agent/adapter/workflow/executor_test.go:40,79,93,118,132,143,184,218,264` | Eligibility/Claim/Applicability fail closed、五分类安全动作、RAG 检索 tuple/citation/拒答/发布门禁、第三次 reduced schema、ModelRun unknown/finalization unknown。 |
| RAG durable/Memory | `internal/agent/adapter/workflow/rag_executor_test.go:21,56,102,160,180,200`；`internal/agent/adapter/memory/loader_test.go:17,55,99` | 已有终态在 Context/Memory/Provider 前 replay；Memory snapshot 冲突/失败不调 Provider；owner/workspace/task scope 和稳定分类映射。 |
| Artifact | `internal/artifact/workflow/executor_test.go:21,49,105,144,187`；`cmd/worker/artifact_generation_success_integration_test.go:48` | final receipt 先 replay、冻结 source/rebase、只把无争议正式证据送模型、running/no-call replay、原子 revision/ModelRun finalization。 |
| Capture | `internal/capture/profile/generator_test.go:21,42,64,83,101,143,156`；`internal/capture/workflow/executor_test.go:69,211,230,249,273` | profile ready 精确 replay、capability unavailable 持久化、ModelRun/Call/证据绑定、未知 evidence label/shape 拒绝、profile 失败不阻塞 retrieval index。 |
| Organizing | `internal/organizing/workflow/generation_test.go:20,79,150`；`internal/organizing/adapter/postgres/generation_repository_integration_test.go:21` | finalization response loss 不重调模型、历史文档不伪造 retrieval evidence、容量 GAP 可见、Generation/ModelRun 原子绑定。 |
| Workflow/Composition | `cmd/worker/tool_composition_integration_test.go:176`；`cmd/worker/rag_conversation_integration_test.go:69`；`internal/workflow/adapter/river/runtime_worker.go:148-171` | Relation+RAG 共享一套 runtime；公开 RAG 仍经过 River；项目 Executor/ExecutionContext 是唯一节点边界。 |

## Files Found

- `internal/agent/application/chat.go` — 项目自有 Chat request/response/port 及边界校验。
- `internal/agent/application/runner.go` — 三阶段 Structured Runner 和预算/严格解码实现。
- `internal/agent/application/query_plan.go` — 单次 PLAN 调用及 ModelRunRef 绑定。
- `internal/agent/application/faithfulness.go` — 独立 REVIEW runner 和发布前语义校验。
- `internal/agent/application/recording_chat.go` — ModelCall 持久化装饰器。
- `internal/agent/application/runtime_repository.go` — ModelRun/ModelCall 唯一持久化端口。
- `internal/agent/application/relation.go` — RelationRunner/Knowledge 端口和业务闭包。
- `internal/agent/application/rag.go` — Application RAG ports、检索和发布门禁编排。
- `internal/agent/adapter/workflow/executor.go` — Relation durable Workflow Executor。
- `internal/agent/adapter/workflow/rag_executor.go` — RAG durable outer executor 和 `executeRun` 短流程边界。
- `internal/agent/adapter/retrieval/adapter.go` — Retrieval Application 到 Agent Port 的窄适配器。
- `internal/agent/adapter/knowledge/adapter.go` — Knowledge eligibility/claim/action 窄适配器。
- `internal/agent/adapter/knowledge/topic_adapter.go` — RAG Topic 只读适配器。
- `internal/agent/adapter/memory/loader.go` — Memory application 到 Agent context 的 owner/scope 映射。
- `internal/platform/models/chat_factory.go` — Chat Factory composition entry。
- `internal/platform/models/chat_openai.go`、`chat_http.go` — 直接 OpenAI-Compatible Chat adapter、strict response 和 HTTP 契约。
- `internal/platform/models/embedding_factory.go` — OpenAI/Ollama Embedding Factory。
- `internal/platform/models/embedding_openai.go`、`embedding_ollama.go`、`embedding_http.go` — 两种 Embedding provider adapter 和共享 HTTP 校验。
- `internal/platform/models/model_transport.go` — model endpoint、DNS、TLS、Proxy、Redirect 安全 transport。
- `internal/platform/models/runtime.go` — 进程级不可变 Chat/Embedding capability。
- `internal/modelsettings/runtime/models.go` — managed settings overlay、Build、credential scrub 和 connection test。
- `internal/retrieval/application/embedding.go`、`search.go`、`vector_builder.go` — Embedding port、Search 编排和批量向量构建。
- `internal/artifact/workflow/executor.go` — Artifact durable generation 和 runner 调用。
- `internal/capture/profile/generator.go`、`internal/capture/profile/contract.go` — Capture profile generation 和严格 output contract。
- `internal/organizing/workflow/generation.go`、`generation_contract.go`、`executor.go` — Organizing generation durable store、ContentGenerator 和 node dispatch。
- `cmd/worker/main.go` — ModelRuntime、Relation/RAG/Capture/Artifact/Organizing/Embedding composition root。
- `cmd/api/main.go` — API 侧共享 capability 消费入口。
- `internal/workflow/application/executor_registry.go`、`internal/workflow/adapter/river/runtime_worker.go` — 项目 Workflow Executor 和 River durable boundary。
- `docs/architecture/adr/0013-eino-adoption-gate.md` — Eino 采用门禁及“不正式采用”现状。
- `docs/architecture/technology-stack.md`、`docs/architecture/interfaces-and-adapters.md` — AI 选型和 Adapter/Workflow 边界。
- `poc/eino/report.md`、`poc/eino/go.mod` — 已验证/未验证的 Eino PoC 能力和版本。

## Code Patterns

- Provider 由 Factory 创建一次，Capability 同时携带 Adapter 和不含秘密的 Contract；上层只消费项目端口（`internal/platform/models/runtime.go:126-170`）。
- 所有需要模型的 durable generator 先准备/恢复自己的 Attempt 和 ModelRun，再创建 `RecordingChatModel`，最后由自己验证并原子完成领域结果（Artifact `internal/artifact/workflow/executor.go:80-131,361-404`；Capture `internal/capture/profile/generator.go:95-173`；Organizing `internal/organizing/workflow/generation.go:256-330`）。
- RAG 在 Provider 前固定 Memory snapshot 和 ModelRun，并把完整调用交给 Application RAG ports；外层 finalizer 失败进入 manual recovery（`internal/agent/adapter/workflow/rag_executor.go:93-170,217-281`）。
- Retrieval/Knowledge adapters 明确“复用 Application，不实现算法”，并进行身份、顺序和结果完整性校验（`internal/agent/adapter/retrieval/adapter.go:33-44,67-88,148-200`；`knowledge/adapter.go:30-75`）。

## External References

- Eino dependency versions and PoC evidence are repository-local rather than live external docs: `poc/eino/go.mod:5-8`, `poc/eino/report.md:15-28`。
- No external web reference was needed for this inventory. A new implementation must re-verify the exact Eino/OpenAI component API against the selected lockfile before coding; the cached/local PoC is not evidence that the main module's current version supports every required feature.

## Related Specs

- `.trellis/spec/backend/index.md:150` — 当前项目明确记录“未正式采用 Eino”。
- `.trellis/spec/backend/model-settings-runtime.md` — ModelRuntime、managed revision、能力和 credential 生命周期约束。
- `.trellis/spec/backend/artifact-contract.md` — Artifact source/evidence/revision 原子不变量。
- `.trellis/spec/backend/capture-profile-contract.md` — Capture profile strict output、evidence binding 和 degraded capability。
- `.trellis/spec/backend/organizing-contract.md` — Organizing frozen snapshot、generation 和 finalization 约束。
- `.trellis/spec/backend/error-handling.md` — 稳定错误分类、未知结果和人工恢复策略。
- `.trellis/spec/backend/quality-guidelines.md` — 后端验证、数据流和持久化一致性检查要求。
- `.trellis/spec/guides/cross-layer-thinking-guide.md` — 跨层端口和事实源边界。
- `.trellis/spec/guides/code-reuse-thinking-guide.md` — 共享实现与适配器复用判断。

## Caveats / Not Found

- 本研究已被主任务的 `prd.md`、`design.md` 和 `implement.md` 收敛为分层迁移方案；当前仍处于 planning，只有用户确认后才能进入代码实现。
- 研究期间未修改业务代码、`go.mod`、规格或 PoC，也未运行主模块测试；测试行号是验收锚点而不是本次执行结果。
- 主模块当前没有生产 Streaming、Tool Loop、Eino Retriever/Rerank 或 Eino Memory 消费者；不能据此推断这些能力已兼容。PoC 对相关门禁的 FAIL/PARTIAL/SKIP 仍有效。
- Eino OpenAI model 的动态 per-call JSON Schema、项目自定义 HTTP transport、精确 usage/model-version 回显以及错误映射的等价性尚未在主模块验证；这是 Chat Adapter 切换前的关键技术风险。
- Eino Embedding 组件与 Ollama native `/api/embed`、`truncate=false`、项目 `[][]float32`/归一化/Contract hash 的等价性尚未验证；在此之前应保留 direct Ollama adapter。
- 现有五个领域构造器直接调用 `NewStructuredRunner`。若不新增共享 factory/port，Eino runner 迁移会导致 Relation、RAG、Artifact、Capture、Organizing 各自分叉，增加预算和 ModelCall 语义漂移风险。
