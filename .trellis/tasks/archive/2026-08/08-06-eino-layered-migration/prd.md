# Eino 分层迁移实施

## Goal

形成可执行、可分阶段回滚的 Eino 正式接入方案，按门禁完成 Chat Adapter、Callback/Trace 和 Structured Output 短 Graph，并完成只读 Tool Calling Go/No-Go：让 Eino 承担受项目 Port 约束的通用模型能力，同时保持知序现有业务规则、持久工作流、安全写回和审计事实源不变。

## User Value

- 正式满足项目采用 Eino 的技术方向，不再把“未实现”误写成框架不适用。
- 复用框架成熟的通用 AI 能力，减少自维护 Provider/Graph/Tool glue code，同时不牺牲现有可靠性边界。
- 形成面试中可解释的技术决策：使用 Eino，但能说明框架边界、持久化恢复和安全控制为什么仍由项目拥有。

## Confirmed Facts

- 阶段开始前根模块没有 Eino，`poc/eino` 独立锁定 Eino `v0.9.12` 与 OpenAI extension `v0.1.13`；旧 PoC 只完整验证了部分 Chat Graph、ToolsNode 和 Callback 能力。
- 项目已经通过 `agentapplication.ChatModel`、`retrievalapplication.Embedder/Reranker` 和 Application Port 隔离 Provider/框架类型，具备渐进迁移入口。
- `StructuredRunner`、`RecordingChatModel`、`RAGExecutor`、`SearchService`、Workflow/Tool/Change Control 分别拥有严格预算、审计、证据、检索、恢复、权限和副作用不变量。
- 当前产品 SSE 是持久阶段通知，不是 Token Stream；当前没有生产 Rerank Provider，RAG 也不是任意 ReAct Tool Loop。
- ADR-0019 已部分 supersede ADR-0013 的“不正式采用”结论；ADR-0013 关于领域、持久 Workflow、权限、审批和安全写回的边界继续有效。

## Requirements

- 盘点当前所有模型调用、结构化输出、RAG、Embedding、Tool Calling、流式输出、Callback/Trace 和 AI 工作流入口及直接调用方。
- 将现有实现逐项划分为“替换为 Eino”“通过项目 Port 适配 Eino”“保留自研”“暂不迁移”四类，并说明原因。
- 明确 Eino 与 PostgreSQL、River、领域服务、权限、Evidence/Citation、Proposal/Approval、Safe Writeback、Model Run/Call 审计之间的所有权边界。
- 设计渐进迁移顺序、兼容开关、双实现对照、回滚路径和旧代码删除条件，禁止一次性整体重写。
- 给出每阶段的影响文件、依赖变化、测试/评测门禁、风险和单人工作量估算。
- 使用当前 Eino 官方能力与项目真实代码作为依据，不沿用“未实现即失败”的旧 PoC 结论。
- 用户已在阶段 0/1 完成后明确要求继续开发至任务完成；当前实施范围允许按既定门禁推进阶段 2-5。阶段 3 必须先完成 Graph Go/No-Go；阶段 4 必须先证明存在可信的持久 Attempt 入口，Go 时也只允许只读 Tool Calling bridge。所有阶段都必须保留 direct/项目自有路径与独立回滚能力。

## Acceptance Criteria

- [x] 迁移矩阵覆盖仓库内全部正式 AI 调用链，并为每项给出目标归属和保留理由。
- [x] 架构设计明确只有 PostgreSQL/River 拥有持久工作流状态，Eino 不成为第二事实源。
- [x] Eino 不得绕过现有权限、工具执行、证据校验、引用校验、审批或安全写回服务。
- [x] 第一阶段能够在保持现有项目 Port 和 API 行为不变的前提下接入、灰度和回滚 Eino ChatModel。
- [x] 后续阶段分别覆盖 Callback/Trace、Structured Output 短 Graph 和只读 Tool Calling 的门禁；Embedding/Retriever/Rerank、完整 RAG Graph 和流式 Token 根据收益、产品需求及 Provider 条件另立任务。
- [x] 每阶段都有可观察的完成条件、相关验证命令、故障测试和旧实现删除前置条件。
- [x] 产出 `design.md` 和 `implement.md`，完成需求收敛检查，并在用户批准后开始阶段 0/1 实施。
- [x] 阶段 0/1 离线合同、race/vet、vendor 构建与 Compose 合同通过，`direct` 仍为默认实现。
- [x] 阶段 2 Callback/Trace 只写脱敏 telemetry，不替代 Model Run/Call、Audit 或 Workflow Progress 事务事实。
- [x] 完成阶段 3 Go/No-Go 证据；五个消费者通过计数包装器证明实际调用 Eino Graph，并通过 direct/Eino 等价、race、真实 PostgreSQL/River 重投递与 Model Call 审计门禁。
- [x] 完成阶段 4 Go/No-Go：当前缺少可信持久 Attempt 入口和独立 Tool Calling model contract，因此本任务 No-Go；未来实现仍须经过项目 Registry、Policy、Capability、lease/fence 和 receipt，写工具继续走 Proposal/Approval/Safe Writeback。
- [x] 阶段 5 只按实际通过的能力更新文档、回滚说明和面试材料；真实 Provider 协议 smoke 已记录为 PASS，`direct` 仍作为灰度默认和回滚路径且未删除。
- [x] Eino Chat 与 RAG Structured Scheduler 在真实 Compose/River 闭环中通过；Workspace 由 Host Controller exact-root Grant 激活，公开业务链路和 exact replay 通过且测试资源完整清理。
- [x] 使用真实 OpenAI-Compatible Provider 对生产 Eino Adapter 完成 live smoke；本地 Ollama `0.32.6` + `qwen3:0.6b` 连续通过两次，默认仍按受控灰度策略保持 `direct`。

## Out of Scope

- Eino Embedding/Retriever/Rerank、完整 RAG Graph、Token Streaming、ADK、Checkpoint/Interrupt 和长期 Agent 状态；这些能力需要后续独立任务与门禁。
- SQL、数据库 Schema、前端和既有 HTTP API 行为变更。
- 使用 Eino 替换 PostgreSQL、River、Human Task、Proposal/Approval、Safe Writeback 或项目领域模型。
- 为了“框架化”重写当前没有产品收益的功能。
