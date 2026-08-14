# Eino 分层迁移执行计划（能力复核修订版）

## 开发环境与状态

- 基线与 PR 目标：`dev`。
- 开发分支：`codex/eino-layered-migration`。
- 独立 worktree：`/Users/zhenglizhi/GolandProjects/zhixu-eino-layered-migration`。
- 生产框架基线：Eino core `v0.9.13`；`v0.10` alpha 不进入生产迁移。
- ADR 编号冲突已解决：ADR-0019 是基线分层采用，ADR-0020 是 Embedding 采用，ADR-0021 是其余
  Runtime 路线的扩展门禁结论。
- 当前已完成 Chat、Callback/Trace、Structured Output 短 Graph、双 Provider Embedding Adapter，以及
  ToolsNode/Stream/Checkpoint 的隔离能力验证；下面同时记录实际结果和未来重开条件。
- 后续内容是能力路线图，不是线性阶段：每个能力建立独立 Trellis 新任务，按各自前置条件获得授权、灰度和回滚，单项 No-Go 不阻塞其他能力。
- 路线中的已勾选项是本次实际完成证据；生产 No-Go 路线下的未勾选项只在未来产品/架构条件满足后重开，
  不代表当前任务未完成。
- Eino 基线 ADR 只授权已完成基线。任何后续能力写生产代码前必须由新的扩展采用 ADR 明确授权范围，并保留 PostgreSQL/River 和项目业务不变量。

依赖关系：路线 A（Embedding）与路线 B（RAG 决策）可并行，B 可继续使用现有 direct Embedder；路线 C（Tool/ReAct）只依赖独立产品 PRD、可信持久入口和专用 ADR，不依赖 A/B；路线 D（Token Streaming）由产品需求与扩展采用 ADR 共同触发；路线 E 的隔离进程内 PoC 可独立开展，跨进程或生产 HITL Checkpoint 必须先有明确消费者和 fenced handoff ADR；路线 F 按单项能力分别收口。

## 已完成基线：阶段 0-3

### 阶段 0：架构和合同冻结

- [x] Eino 基线 ADR 正式采用 Eino，同时保留 ADR-0013 的 Domain、Workflow、Permission 和 Writeback 边界。
- [x] 冻结 direct Chat 请求、响应、usage、错误、Schema 和并发合同。
- [x] 锁定 Eino core `v0.9.13`、OpenAI Chat extension `v0.1.13` 与 vendor/许可证基线。

### 阶段 1：Eino Chat Adapter

- [x] 使用 Eino OpenAI ChatModel 实现项目 `ChatModel`，复用安全 HTTP client 和严格 wire 校验。
- [x] 保留 `direct|eino` 选择器、项目稳定错误、Model Run/Call 和 direct 回滚路径。
- [x] 通过离线合同、并发/race、Compose 和真实 Ollama OpenAI-Compatible smoke。

### 阶段 2：Callback/Trace

- [x] Eino callback 接入项目 observability，只写脱敏 telemetry。
- [x] Callback 不读取 Prompt/响应正文/raw error，不替代 Audit、Model Run/Call 或 Workflow Progress。

### 阶段 3：Structured Output 短 Graph

- [x] Eino Graph 通过项目 `StructuredPhaseScheduler` Port 驱动 `INITIAL/REPAIR/REDUCED`。
- [x] 五个消费者 selector 独立灰度；decoder、预算、错误和审计仍归项目所有。
- [x] direct/Eino 短 Graph 等价，以及“现有 `RAGWorkflowExecutor` + Eino Structured Scheduler”的真实 PostgreSQL/River 重投递和 Compose RAG 门禁通过；Eino Retriever 与完整 RAG Workflow 尚未实施。

历史 Stage 4 的结论修订为：**当时实现 No-Go，但 Eino 能力 Go**。没有落地 ToolsNode 的原因是项目缺少可信持久 Tool-loop Node、独立 Tool Calling 合同和确定性调用身份，不是 Eino 不支持 Tool Calling/ReAct。

## 路线 A：Eino Embedding 可行性与 Adapter

目标：按 Provider 判断 Eino extension 能否替换机械 transport，保留项目严格 Embedding 合同；不是预设 OpenAI 与 Ollama 都必须迁移。

- [x] ADR-0020 接受双 Provider Embedding 的可回滚内部实现，默认仍为 `direct`。
- [x] 精确固定 commit `90a15623ddb66465aea01fbe8c63ecc9d267acc1`，验证两个 ext 与 core `v0.9.13`
  的编译、API、race、vendor 和运行时合同兼容。
- [x] OpenAI-Compatible 与 Ollama 均增加 Eino-backed Embedder，实现现有 `retrievalapplication.Embedder`。
- [x] 复用安全 HTTP client、Endpoint、Credential、超时和稳定错误，并由有界验证 RoundTripper 保护成功/错误响应。
- [x] 输入继续通过 `ValidateEmbedRequest`，输出继续通过 `ValidateEmbedResult`；Eino 类型不进入 Application/Domain。
- [x] direct/Eino 四组合覆盖批量、乱序/缺失/重复/越界、维度/有限值、归一化、model、状态码、取消、
  deadline、oversize、SDK/wire 一致性和并发 capture。
- [x] Ollama native `/api/embed` 显式发送 `truncate=false`，并拒绝隐式 Authorization/cloud signing。
- [ ] 对每个计划 Go 的 Provider 运行真实 smoke，确认批量顺序、数量、维度、model/truncate 合同和错误体；一个 Provider No-Go 不连带否定另一个。
- [x] 单独锁定每个 Eino Embedding module 的精确 pseudo-version、commit、许可证与 vendor 变化。
- [x] 新增独立 `direct|eino` 内部选择器，缺省 direct；API、Worker 与 modelctl 使用同一 factory/进程值。

Go 门禁：按 Provider 分别判定。精确 core/ext 组合兼容，Eino Adapter 能观察或可靠保证项目要求的顺序、数量、维度、模型身份、Ollama `truncate=false` 和响应上限，direct/Eino 合同完全等价。

No-Go：目标 Provider 会乱序或返回必须校验但 Eino API 丢失的字段。此时只保留该 Provider 的窄 direct transport，不否定 Eino Embedding 抽象，也不扩大自研范围。

## 路线 B：RAG 编排决策、Retriever Bridge 与候选 Workflow

目标：先证明完整 Eino 编排比“现有 `RAGExecutor` + Eino Components”有净收益，再决定是否由 Eino 负责编排确定性 RAG 内层流程。决策评估 2-3 人日；获得 Go 后实现 8-12 人日。

- [x] 冻结强制需求和权重，Eino 现有原生覆盖计算为 25/100，未达到 80% Go 门槛。
- [x] 比较删除/新增代码、重复调度、失败语义、可观测性、测试和回滚成本；完整 Graph 为净增加。
- [x] 记录完整 RAG 生产 No-Go，保留 `RAGWorkflowExecutor`/`RAGExecutor`，继续复用已采用 Eino Components。
- [x] 因路径 B No-Go，不创建无消费者的 Eino Retriever bridge、Indexer bridge 或 RAG selector。
- [x] Workspace、Evidence、Active Index、检索模式、RRF、TopK、score 和 provenance 合同继续由项目实现拥有。

以下步骤只在未来重新达到覆盖率与净收益门禁后执行，不属于本次未完成项：
- [ ] 用 Eino Workflow 组合 Query Plan、Retrieve、Eligibility/Topic、Answer、Citation、Faithfulness、Refusal/Proposal；若确实需要 cycle 或动态分支，再使用 Graph。
- [ ] 模型节点继续进入项目 Chat/Structured Output/RecordingChatModel 路径，不能绕过预算、Schema 和 Model Run/Call。
- [ ] Eino Runnable 只返回 `RAGTerminalProposal`；Memory Snapshot、Model Run 生命周期、exact replay、失败恢复和 Answer finalization 仍由外层 `RAGWorkflowExecutor` 完成。
- [ ] 保留现有 `RAGExecutor` 作为 direct 路径，新增独立 `direct|eino` RAG orchestrator selector。
- [ ] 固定 fixture 对照查询计划、候选顺序、证据 eligibility、引用、faithfulness、拒答、progress、模型调用次数和最终 proposal。
- [ ] 运行真实 PostgreSQL/pgvector/River direct/Eino 对照，覆盖 transport 重投递、取消、Provider 失败、检索降级和 finalizer response-loss。
- [ ] 运行现有公开 HTTP、SSE、Citation、Feedback 和 Compose RAG smoke，确认 API 和持久事件合同不变。
- [ ] 只有确定真实语义 Rerank Provider 后，才把项目 `Reranker` Port 包装为自定义 Eino Transformer；用 query-dependent score 合同验证，禁止用官方 ScoreReranker 替代。该可选项不阻塞 RAG 主路线。

Go 门禁：全部强制约束通过且达到至少 80% 加权覆盖；Eino 接管节点拓扑、数据流和分支后实质删除了重复调度代码，延迟和可观测性不退化，项目没有再维护一套并行 Graph；领域结果、错误/取消、审计、恢复和持久终态与 direct 基线等价。此路线可使用现有 direct Embedder，不受路线 A No-Go 阻塞。

No-Go：为了使用 Eino 必须复制 `SearchService`、放宽 Evidence/Workspace 边界、改变 Model Call 事实或让 Graph 直接 finalization。此时回到桥接设计，不以 Lambda 数量为理由绕过领域契约。

## 路线 C：产品条件下的持久 Tool-loop 与 Eino ReAct/ToolsNode

进入条件：已有获批产品 PRD，明确生产调用方、允许工具白名单、终态、总预算、审批路径和用户可见失败语义；并有新的 Tool-loop ADR。缺少任一条件时保持 No-Go，不创建无消费者的持久 Node。

目标：条件满足后使用 Eino 承担通用模型-工具循环；产品/安全合同设计 3-5 人日，实现与验证 8-12 人日。

- [x] 核对产品 PRD、HTTP、Workflow 注册和前端，确认当前没有真实模型 Tool-loop 消费者。
- [x] `poc/eino/tooltrace` 的实际 ToolsNode schema/dispatch/race 通过，证明框架能力可用。
- [x] 记录生产 No-Go，不新增持久 Tool-loop Node、Tool bridge、selector 或 Safe Writeback 直达路径。

以下步骤只在产品 PRD、可信 Attempt 和独立 ADR 全部具备后执行：

- [ ] 先冻结具体调用方、工具白名单、终止状态、Approval/Proposal 流程和回滚方式，再新增独立持久 Workflow Tool-loop Node，冻结 Node Attempt、Workspace、Model Run、总预算和确定性 `call_no`。
- [ ] 定义项目 Tool Calling model Port/contract，允许 ToolCalls，并与现有严格结构化 Chat 合同分离。
- [ ] 使用经典 Eino ChatModelAgent，或 Graph + ToolsNode；设置最大迭代、总 token/time/tool-call 预算、取消和终止条件。
- [ ] 首个生产路径显式禁用 Eino `ModelRetryConfig` 和 `ModelFailoverConfig`；项目 Workflow 是唯一 retry owner，所有模型调用必须经过 `RecordingChatModel` 形成 Model Call 与费用事实。未来启用框架 retry/failover 必须另立 ADR 并保持同一审计路径。
- [ ] 将项目 Registry 中获准的工具映射为 Eino Tool；`Info` 只暴露脱敏 schema，不暴露 Capability token、路径或 Credential。
- [ ] `InvokableRun` 从持久 Attempt 重新解析 Authorization、Capability、lease/fence、幂等键和调用身份，再进入项目 ExecutionService。
- [ ] 第一阶段只读、顺序执行；覆盖模型重复调用、未知工具、非法 JSON、参数 schema、部分成功、取消、超时和 response-loss replay。
- [ ] Tool result 继续持久化 receipt；未知结果保持 `UNKNOWN/manual_recovery`，不能由 Agent 自行重试为成功。
- [ ] 模型侧写能力最多生成 Candidate/Proposal；批准后由独立项目 Workflow 获取一次性 Write Authorization 并调用 trusted Safe Writeback Executor。Eino Tool 不得直接写文件/Git/正式知识，也不得直接调用 Safe Writeback bridge。
- [ ] 通过门禁后再评估 Eino 并行 ToolsNode；Interrupt/Resume 只进入路线 E 的进程内 PoC，不作为项目 Approval/Human Task 的替代路径。

Go 门禁：Eino 确实接管 ReAct/Tool dispatch，项目 bridge 对每次工具执行提供可重放的授权、幂等、receipt 和审计证据。

No-Go：没有明确产品消费者/合同、新 ADR、可信 Attempt/lease/call identity，或模型循环能绕过项目 Policy。回滚为不注册 ToolsNode，不影响现有 Tool Worker。

## 路线 D：Token Streaming（5-8 人日，产品条件）

进入条件：产品确认需要逐 Token 输出，且新的扩展采用 ADR 已明确 Token API、持久终态、敏感内容和回滚边界；仅有现有阶段 SSE 或仅有产品意向均不触发本路线。

- [x] 核对产品/API/前端与 SSE owner，确认当前没有逐 Token 消费者，durable phase SSE 是独立事实流。
- [x] 用实际 Eino `compose.Stream` 编译链验证多帧、取消与提前 Close 释放 producer，不再依赖自定义 helper 代替框架证据。
- [x] 记录生产 No-Go，不新增 Token API、前端草稿状态或 selector。

以下步骤只在产品入口和独立 ADR 获批后执行：

- [ ] 设计独立 Token Stream API 与前端状态，不修改 durable phase SSE 的恢复语义。
- [ ] 从 Eino ChatModel/Graph `Stream` 或 `Transform` 贯通到 HTTP；禁止 `Invoke` 后包装单帧 fake stream。
- [ ] 定义 partial token、final answer、refusal、usage、error 和 cancel 的边界；最终持久 Answer 仍由项目 finalizer 原子发布。
- [ ] 覆盖首帧、慢消费者、背压、客户端取消、Provider 流内错误、UTF-8、响应上限、Reader Close 和 goroutine 泄漏。
- [ ] Callback 流副本必须被消费并关闭，且 telemetry 不记录 Token 正文。
- [ ] 桌面和移动浏览器验证重连、取消、并发对话、阶段 SSE 与 Token Stream 不互相覆盖。

Go 门禁：真实 Provider 能持续产生多帧，取消能传递到底层且没有资源泄漏；不能只以 UI 动画或 fake stream 判定通过。

## 路线 E：ADK 与 Checkpoint/Interrupt PoC（3-5 人日）

目标：验证能力和边界，不直接替换业务 Workflow。

- [x] 使用 Eino v0.9.13 实际 Graph、`StatefulInterrupt`、`ExtractInterruptInfo` 和 `ResumeWithData` 完成
  同一进程、同一活跃 Attempt 的中断/带数据恢复。
- [x] Checkpoint ID 绑定 Workflow Run、Node Run、Attempt 和 Fence；不同 Attempt/Fence 无法读取或恢复。
- [x] Store 实现 AES-256-GCM/AAD、8 MiB 上限、最长 15 分钟 TTL、显式 Delete、篡改拒绝、取消和并发隔离。
- [x] 核对真实 Runtime Claim：重启/reclaim/Human Task 均不满足同一活跃 Attempt，因此跨这些边界明确失效。
- [x] ADR-0021 记录 PoC PASS、生产 No-Go；根模块、数据库、River Worker 和生产 Composition 未接入 checkpoint。
- [x] ADK/Agentic 当前无产品消费者，经典 `schema.Message` 证据保留在隔离 PoC，Agentic/Beta 不进入默认路径。

未来若确需跨进程或 HITL，再另立 ADR 设计 PostgreSQL 授权的 fenced one-time handoff、持久 Store、密钥轮换、
拓扑/serializer 兼容和副作用幂等；这些不是本次未完成项。

## 路线 F：逐能力灰度、旧实现删除与收口（基础 3-5 人日）

- [x] 已获准的 Chat、Embedding 和五个 scheduler 使用独立 selector；RAG/Tool/Token/Checkpoint No-Go 路线不创建 selector。
- [x] 记录 Provider、框架/extension 精确版本、离线/真实门禁范围和回滚边界；没有把未运行的生产指标写成 PASS。
- [ ] 至少经过一个发布周期且 direct 回滚未被触发后，逐能力决定是否删除旧 transport/orchestrator。
- [x] 本轮不删除 direct transport/orchestrator；全部回滚路径保留，数据库无需迁移或回滚。
- [x] 用实际门禁结果更新 ADR、后端 Eino specs、部署文档、技术栈说明、PoC 报告和面试材料；历史 ADR 保留原时点语义。

## 影响文件地图

| 路线 | 主要影响范围 | 计划改动 |
|---|---|---|
| A | `internal/platform/models/embedding_*`、factory/runtime/config、Model Settings、`go.mod`/vendor | Provider-specific Eino Embedding feasibility、Adapter、独立 selector、合同与 live smoke |
| B | `internal/agent/adapter/eino`、`internal/agent/adapter/workflow/rag_executor.go`、`internal/agent/application/rag.go`、Retrieval Adapter、Worker Composition | RAG 净收益决策；Go 后才实现 Retriever bridge、完整 Workflow、direct/Eino 等价门禁 |
| C | `internal/agent`、`internal/tools`、`internal/workflow`、模型合同、Worker Composition | 产品/ADR Go 后才新增持久 Tool-loop Node、Eino Agent/ToolsNode bridge、权限/receipt/replay |
| D | Conversation/API streaming、observability、Web 对话状态 | 产品批准后的端到端 Token Stream，与 durable phase SSE 并存 |
| E | `poc/eino`、可选 Agent Adapter、research/ADR | ADK/Checkpoint 同进程/同 Attempt 的 Interrupt/Resume PoC，不进入业务事实源 |
| F | ADR、`.trellis/spec/backend`、部署/回滚文档、旧实现文件 | 各路线独立灰度、删除门禁和文档收口 |

## 验证命令基线

每条路线只增加与改动对应的测试；以下命令是共同下限：

```bash
go test ./internal/platform/models ./internal/retrieval/...
go test ./internal/agent/... ./internal/tools/... ./internal/workflow/...
go test -race ./internal/platform/models ./internal/retrieval/... ./internal/agent/adapter/eino ./internal/tools/...
go vet ./internal/platform/models ./internal/retrieval/... ./internal/agent/... ./internal/tools/...
go test ./cmd/api ./cmd/worker
make openapi-check
make compose-check
make compose-rag-smoke
cd poc/eino && go test -race ./... && go vet ./...
git diff --check
```

Embedding、Tool 和 Streaming 的生产采用必须补充各自真实 Provider、真实 PostgreSQL/River 或浏览器门禁；Checkpoint 路线 E 只允许声明进程内 PoC 结论。Fake、readiness 和文档说明不能替代任何被宣称为生产可用的真实链路。

## 回滚点

| 能力 | 触发条件 | 回滚动作 |
|---|---|---|
| Chat Adapter | wire/usage/error/Provider 不等价 | Chat selector 切回 direct |
| Structured Graph | 阶段、预算、调用次数、错误或审计漂移 | 对应消费者 scheduler 切回 direct |
| Embedding | 顺序、数量、维度、model 或错误合同漂移 | Embedding selector 切回 direct；不重建业务 Schema |
| 完整 RAG | 检索、Evidence/Citation、proposal、replay 或 finalization 漂移 | RAG orchestrator 切回现有 `RAGExecutor` |
| 语义 Rerank | query-dependent score、顺序或降级语义漂移 | 禁用自定义 Transformer，恢复项目无 Rerank/现有 Port 路径 |
| Tool/ReAct | 权限、幂等、receipt、UNKNOWN 或循环预算失效 | 不注册 Eino Agent/ToolsNode，保留现有 Tool Worker |
| Token Stream | 泄漏、取消、流错误或 UI 一致性失败 | 关闭 Token API；durable phase SSE 保持可用 |
| Checkpoint | 双账、快照不兼容、进程/owner/Attempt 变化后复用或副作用重放 | 禁用 Eino checkpoint；旧 checkpoint 失效并清理，由 PostgreSQL/River 决定新 Attempt |

## 预计工作量

以下是**工程人日**，不重复计算已经完成的 Chat、Callback 和短 Graph，也不把等待审批、供应商响应或发布观察当作开发人日：

- 共用扩展采用 ADR 与版本基线：1-2 人日。
- 路线 A：Provider/ext 可行性 2-3 人日；至少一个 Provider Go 后，首个生产 Adapter 与合同/真实 smoke 另 4-6 人日，第二个 Provider 若也 Go 再增加 2-3 人日。两端都 No-Go 时止于可行性报告。
- 路线 B：RAG 两方案净收益决策 2-3 人日；完整 Workflow 获得 Go 后，Retriever bridge、实现与闭环验证另 8-12 人日。
- 可选语义 Rerank：真实 Provider 已选定后，Eino Transformer 适配与合同测试另 2-4 人日；Provider 本身接入另估，不计入核心路线。
- 路线 C：只有明确产品消费者时才计入；产品/安全技术合同 3-5 人日，持久 Tool-loop 与 Eino ReAct/ToolsNode 实现 8-12 人日。不含产品决策等待。
- 路线 D：5-8 人日，仅在产品需求与扩展采用 ADR 都批准 Token Streaming 后实施。
- 路线 E：3-5 人日，仅为隔离 ADK/Checkpoint PoC；生产采用需另估。
- 路线 F：已 Go 的 A/B 共同灰度、回滚观测与文档收口基础 3-5 人日；每增加一个独立产品路线，另预留 1-2 人日收口。

推荐的核心 AI Runtime 路线只包含 ADR、A 和 B 的决策/Go 分支及 A/B 收口：至少一个 Embedding Provider Go 时约 **20-31 人日**，两个 Provider 都 Go 时约 **22-34 人日**；若某项 No-Go，则只计算其评估成本。未获产品批准的 Tool/ReAct 不计入核心迁移。

日历周期不能直接由人日相除：生产默认值切换和旧实现删除之间至少保留一个完整发布观察周期，外加 ADR 审批、Provider 联调和可能的 ext 兼容修复。单人实施核心路线按 **2-3 个自然月**安排更可信；“三个月”是合理排期，但不是 31 人日以内必然完成全部可选路线的承诺。
