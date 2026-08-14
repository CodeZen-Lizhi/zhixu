# Eino 分层迁移实施（能力复核修订版）

> 修订日期：2026-08-08。本文以 Eino `v0.9.13`、官方文档、官方源码、`eino-ext` 模块和项目当前代码为依据，修正原计划中把“本阶段未采用”误写成“框架不具备能力”的判断。

> ADR 编号冲突已解决：`0024-layered-eino-adoption.md` 是 Chat/Callback/短 Graph 基线，
> `0025-eino-embedding-adoption.md` 是 Embedding 扩展采用，`0026-eino-runtime-expansion-gates.md`
> 记录完整 RAG、Tool、Streaming、Checkpoint/ADK 的实际门禁结论。

## Goal

让 Eino 接管通用 AI Runtime：Provider 适配、进程内 Graph/Workflow、流式处理、通用 Tool Calling/ReAct 和 Callback；知序继续拥有领域规则、持久工作流、数据一致性、权限、审计、证据和安全写回。

迁移必须渐进、可比较、可灰度、可回滚。使用项目 Port 或自定义 Eino Component 接入现有业务能力，属于框架推荐的扩展方式，不视为继续自研同类框架。

## User Value

- 复用 Eino 已提供的通用能力，减少 Provider 协议、Graph 调度、流式拼装和工具循环的重复维护。
- 保留知序已经验证的 PostgreSQL/River 恢复协议、检索语义、安全和审计边界。
- 对外能够准确说明：项目不是“为了展示能力全部自研”，也不是“引入框架后把业务可靠性交给框架”。

## Confirmed Facts

- 当前分支已完成 Eino OpenAI-Compatible Chat Adapter、调用级 Callback/Trace 和 `INITIAL/REPAIR/REDUCED` 短 Graph；`direct` 仍是默认实现和回滚路径。
- Eino 官方提供 Chain、Graph、Workflow、ChatModel、Embedding、Retriever、Indexer、Transformer、ToolsNode、ReAct/ADK、Streaming 和 Interrupt/Checkpoint。完整 RAG、Embedding、Tool Calling 和 Streaming 不能再按“框架不支持”排除。
- Eino Workflow 适合确定性 DAG，Graph/ADK 适合有循环的 Agent；Workflow 不支持 cycle，不能承载 ReAct 循环。
- `eino-ext` 已有 OpenAI Embedding 和原生 Ollama Embedding；OpenAI-Compatible `BaseURL` 也被官方实现支持。但两个 Embedding 组件的 `EmbedStrings` 都只向上返回向量，不回显 Provider response model；OpenAI 组件还不暴露响应 item 的 `index`。是否替换必须按 Provider 分别通过顺序、数量、维度、model、`truncate=false` 和错误合同门禁。
- 官方 Retriever/Indexer 当前没有 PostgreSQL/pgvector 实现，也不包含项目的 FTS、RRF、Active Index、Workspace/Evidence 过滤和 provenance 语义；现有 `SearchService` 与索引事务继续保留，并适配为 Eino Component。
- 官方 `ScoreReranker` 只根据已有 score 做位置重排，不是 cross-encoder 或模型语义 rerank；项目 `Reranker` Port 与真实 Provider 选择继续保留。
- ToolsNode、ReAct、ADK Interrupt/Resume 的能力真实存在；它们不负责项目的授权、审批、lease/fence、幂等、receipt、未知结果恢复和安全写回。
- Eino Checkpoint 是 Graph/Agent 执行快照，`CheckpointStore` 仅为用户实现的 Get/Set 存储接口；它不是 River/Temporal 类 durable workflow engine，也不拥有会话持久化。
- 当前产品 SSE 是持久阶段事件，不是 Token Stream；未来新增 Token Streaming 时应直接使用 Eino 的端到端 Stream 路径，现有 SSE 仍保留。
- 生产基线锁定 Eino `v0.9.13`；`v0.10` alpha、Agentic OpenAI/`AgenticMessage` 和长时间 Checkpoint 恢复只允许隔离 PoC。`eino-ext` 各组件是独立 Go module，必须逐模块锁定版本。复核时最新 Embedding pseudo-version 中，OpenAI/Ollama 模块分别依赖 Eino core `v0.7.13`/`v0.6.0`，不能未经编译与合同门禁直接并入当前 `v0.9.13` 根模块。

## 2026-08-08 实施回填

- **Embedding 条件 Go**：OpenAI-Compatible 与 Ollama 均已增加 Eino-backed Adapter，四组合离线合同、
  并发/race、vet、Compose、vendor 和依赖门禁通过；默认仍为 `direct`，两个目标 Provider 分别完成真实 smoke
  后才允许灰度。
- **完整 RAG 生产 No-Go**：Eino 能表达流程，但原生加权覆盖只有 25/100；用 Lambda 包装项目 Port 会净增
  代码和失败面，不能删除 Evidence/Citation、检索 tuple、Model facts、replay 和 finalizer 等主要复杂度。
  保留 `RAGWorkflowExecutor`/`RAGExecutor`，不创建无消费者的 Retriever bridge。
- **Tool/ReAct 能力 PASS、生产 No-Go**：真实 Eino ToolsNode PoC 通过；当前没有产品入口、工具白名单、
  循环终态、持久 Attempt 和审批合同，所以不新增生产 Tool-loop。
- **Streaming 能力 PASS、生产 No-Go**：真实 Eino `compose.Stream` 多帧、取消和提前 Close PoC 通过；当前
  没有 Token API/前端消费者，持久阶段 SSE 保持原样。
- **Checkpoint PoC PASS、生产 No-Go**：实际 Interrupt/Resume、Attempt/Fence、加密、TTL/Delete、篡改和
  并发门禁通过；重启/reclaim/Human Task 会结束旧 Attempt，没有 fenced handoff 时不接入生产恢复。
- **ADK/Agentic 生产 No-Go**：没有多 Agent 产品消费者；经典 `schema.Message` 能力证据保留在隔离 PoC，
  Agentic/Beta 与 `v0.10` alpha 不进入默认路径。

## Requirements

- 保留已经完成的 Chat、Callback 和 Structured Output 短 Graph，不回退现有合同和灰度能力。
- 按 OpenAI-Compatible 与 Ollama 两个 Provider 分别评估 Eino Embedding；只有精确版本兼容和项目合同均通过时才替换对应 transport，继续实现项目 `retrievalapplication.Embedder`。
- 比较“保留现有 `RAGExecutor`，只复用已独立通过门禁的 Eino Chat/Embedding”和“由 Eino Workflow/Graph 接管完整内层编排并引入 Retriever bridge”两种方案；只有后者在重复代码、延迟、可观测性和失败语义上有可验证净收益且业务合同等价时才迁移。
- 只有完整 RAG Eino 编排获得 Go 后，才把现有 PostgreSQL FTS/pgvector/RRF/Active Index 检索实现适配为其 Eino Retriever；不重写 SQL、索引或召回融合规则。
- Tool/ReAct 保持 No-Go，直到存在明确产品 PRD、生产调用方、工具白名单、终止条件、审批路径、可信持久 Node Attempt 和独立采用 ADR。全部满足后才评估用 Eino ChatModelAgent/ToolsNode 承担通用循环与 dispatch；项目 Tool bridge 继续拥有授权、幂等、receipt、审计和副作用治理。
- 有明确产品入口且扩展采用 ADR 已授权 Token API/持久终态/回滚边界时，以 Eino Stream 实现 Token Streaming；禁止用单帧 fake stream 宣称获得真实首 Token 延迟。
- ADK 多 Agent、Agentic OpenAI、Checkpoint/Interrupt 的框架能力已确认存在；首轮只做隔离 PoC/灰度，原因是版本和恢复所有权风险，不是能力缺失。除非新的 ADR 明确唯一恢复事实源，否则不得替代 PostgreSQL/River。
- 所有新增 Eino 路径必须有 direct/现有实现对照、独立选择器、真实 Provider 或真实 PostgreSQL/River 门禁和明确回滚条件。
- 所有强制安全、数据一致性、隐私、部署、许可证和运行时兼容条件先逐项通过；再按实现前冻结的需求权重计算覆盖率，低于 80% 或薄适配后的净收益不足时不为“全量 Eino”强行迁移。

## Acceptance Criteria

### 已完成基线与本次修订

- [x] Eino Chat Adapter、Callback/Trace 和 Structured Output 短 Graph 已接入项目 Port，并保留 direct 回滚路径。
- [x] PostgreSQL/River、Model Run/Call、Evidence/Citation、Proposal/Approval 和 Safe Writeback 仍是项目事实源。
- [x] 真实 OpenAI-Compatible Provider smoke、direct/Eino 短 Graph 合同等价，以及“现有 `RAGWorkflowExecutor` + Eino Structured Scheduler”的 PostgreSQL/River 重投递门禁已通过；Eino Retriever 和完整 RAG Workflow 尚未实施。
- [x] 修订后的能力矩阵不再把“尚未实施”写成“Eino 不支持”，并明确直接采用、适配采用、项目保留和 PoC 四类边界。
- [x] `design.md` 与 `implement.md` 给出后续能力分支、依赖关系、Go/No-Go、回滚、版本策略和单人工作量。

### 后续路线门禁结果

- [x] Embedding core/ext 编译、批量、顺序、数量、维度、model、`truncate=false`、归一化、取消、错误和
  响应上限离线门禁通过；实现可独立切回 `direct`，不改变持久身份。
- [x] ADR-0025 接受 Embedding 扩展采用，ADR-0026 接受其余路线的实际 Go/No-Go 结论。
- [x] 完整 RAG 完成覆盖率与净收益对照并判定 No-Go；未创建 Retriever bridge、平行 Graph 或 selector。
- [x] Tool/ReAct 完成真实消费者核对并判定生产 No-Go；ToolsNode 机械能力由实际 Eino PoC 证明。
- [x] Token Streaming 完成消费者和生命周期核对并判定生产 No-Go；实际 Eino Stream PoC 证明多帧、取消和 Close。
- [x] Checkpoint/Interrupt 完成同进程、同活跃 Attempt PoC；跨重启/reclaim/HITL 明确拒绝，生产采用 No-Go。
- [x] 每条 No-Go 路线都没有新增生产 selector、Schema 或第二事实源；direct/项目实现保持原样。
- [ ] OpenAI-Compatible 与 Ollama 在各自目标环境的真实 Embedding Provider smoke、生产灰度和一个发布周期
  观察仍是发布门禁，不属于离线实现完成度。

## Out of Scope

- 使用 Eino 替换 PostgreSQL/River durable workflow、lease/fence、Outbox、Human Task、事务和会话持久化。
- 使用 Eino 替换项目权限、Evidence/Citation、Proposal/Approval、Safe Writeback、Model Run/Call 或领域模型。
- 用官方 ScoreReranker 冒充模型语义 rerank，或为接 Eino 重写已验证的 PostgreSQL 检索内核。
- 在首轮迁移中使用 `v0.10` alpha、Agentic OpenAI/`AgenticMessage` 或把 Eino Checkpoint 作为长时间业务恢复方案。
- 未经产品需求批准新增 Token Streaming API/前端交互，或一次性删除全部 direct 回滚实现。

## Research Basis

- [Eino Overview](https://www.cloudwego.io/docs/eino/overview/)
- [Chain and Graph Orchestration](https://www.cloudwego.io/docs/eino/core_modules/chain_and_graph_orchestration/)
- [Workflow Orchestration Framework](https://www.cloudwego.io/docs/eino/core_modules/chain_and_graph_orchestration/workflow_orchestration_framework/)
- [Embedding Guide](https://www.cloudwego.io/docs/eino/core_modules/components/embedding_guide/)
- [OpenAI Embedding Source at `90a1562`](https://github.com/cloudwego/eino-ext/blob/90a15623ddb66465aea01fbe8c63ecc9d267acc1/components/embedding/openai/embedding.go)
- [Ollama Embedding Source at `90a1562`](https://github.com/cloudwego/eino-ext/blob/90a15623ddb66465aea01fbe8c63ecc9d267acc1/components/embedding/ollama/embedding.go)
- [OpenAI Embedding module dependencies at `90a1562`](https://github.com/cloudwego/eino-ext/blob/90a15623ddb66465aea01fbe8c63ecc9d267acc1/components/embedding/openai/go.mod)
- [Ollama Embedding module dependencies at `90a1562`](https://github.com/cloudwego/eino-ext/blob/90a15623ddb66465aea01fbe8c63ecc9d267acc1/components/embedding/ollama/go.mod)
- [Retriever Guide](https://www.cloudwego.io/docs/eino/core_modules/components/retriever_guide/)
- [ToolsNode Guide](https://www.cloudwego.io/docs/eino/core_modules/components/tools_node_guide/)
- [Checkpoint and Interrupt](https://www.cloudwego.io/docs/eino/core_modules/chain_and_graph_orchestration/checkpoint_interrupt/)
- [Checkpoint interfaces at Eino `v0.9.13`](https://github.com/cloudwego/eino/blob/v0.9.13/internal/core/interrupt.go#L27-L40)
- [ChatModelAgent retry/failover config at Eino `v0.9.13`](https://github.com/cloudwego/eino/blob/v0.9.13/adk/chatmodel.go#L403-L414)
- [Memory and Session](https://www.cloudwego.io/docs/eino/quick_start/chapter_03_memory_and_session/)
- [Eino v0.9.13](https://github.com/cloudwego/eino/releases/tag/v0.9.13)
