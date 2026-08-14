# Eino RAG Graph 正式迁移

## Goal

把当前 `RAGExecutor` 拥有的通用顺序、分支和循环正式迁入 Eino Graph，使 Eino 成为生产 RAG 内层编排器；保留外层 River/PostgreSQL Workflow 及全部检索、证据和发布领域不变量。

## 当前状态

Eino Graph 已成为 `/chat` RAG 的生产内层编排器。项目 Port 名称为 `RAGExecutionScheduler`；`RAGExecutor` 保留五个显式领域节点，Graph 负责连接和分支。Agent、tool-free final stream、metadata、PostgreSQL/River replay 和 Finalizer 已由本地合同/集成门禁覆盖；历史 Eino→direct→Eino 回滚演练已归档，不代表当前部署能力。2026-08-11 当前树已通过外部 OpenAI-Compatible Chat host relay + 原生 Ollama Embedding 的真实 Provider 桌面/移动浏览器闭环，覆盖 PLAN、AGENT、ANSWER、metadata、REVIEW 和正式终态；该证据不冒充容器直连外部 HTTPS 网络路径。稳定观察独立执行，旧 direct 按父任务同日决策删除。

## Requirements

- `RAGWorkflowExecutor` 继续拥有 receipt replay、Context/Memory freeze、ModelRun、NodeAttempt 和原子 finalizer。
- 使用不含 Eino 类型的 `RAGExecutionScheduler` Port，生产实现为启动时编译的 Eino Graph。
- Graph 覆盖 Query Plan、clarification branch、Retrieval、Eligibility/Evidence、Eino Agent/tool-free final stream、metadata、Citation/Faithfulness 和 terminal proposal；不再维护 A/B 两套长期生产 Answer 子图。
- PostgreSQL FTS、pgvector、RRF、active-index、Evidence/Citation/Faithfulness 继续调用项目服务；它们作为 Eino 自定义节点是正式框架用法，不复制其领域实现。
- 从 `RAGExecutor` 提取领域步骤后删除其大流程顺序编排，禁止长期保留 direct/Eino 两套生产 RAG。
- Graph state 只在当前 NodeAttempt 内存在；不使用 Eino checkpoint 替代 River/PostgreSQL。
- RAG progress 仍由项目节点记录，Eino callback 只做观测。
- Graph 接收 NodeAttempt 级共享 `RunBudgetLedger`；Query Plan、Answer、metadata 和 REVIEW 均从同一本账扣减，后续 Agent 只能使用扣除下游预留后的额度。

## Acceptance Criteria

- [x] 启动时编译 RAG Graph，生产 `RAGWorkflowExecutor` 通过 `RAGExecutionScheduler` 调用该 Graph。
- [x] Query Plan、三种终态、retrieval degradation、active-index/embedding version 漂移和 evidence projection 与现有固定 fixture 等价。
- [x] Citation/Faithfulness、ModelRun/Call、progress、finalizer、response-loss 和 replay 不变量保持不变。
- [x] 真实 PostgreSQL/River duplicate delivery、lease reclaim、取消和 terminal response-loss 测试通过。
- [x] Eino 类型未进入 Application/Domain/Workflow input/output/PostgreSQL。
- [x] 旧 direct scheduler 和 direct Chat/Embedding 实现不再作为生产回滚路径；历史回滚演练保留为迁移证据，恢复依赖 Git 发布记录。真实 Provider/稳定观察仍是独立发布质量门禁。
- [x] 当前 Graph 已同时承载 Agent、tool-free final stream 和 metadata；没有第二条长期生产 RAG。

## Superseded Decision

`research/rag-coverage-and-net-benefit.md` 的 No-Go 是在“只有净减代码才迁移”的旧约束下形成的历史记录。用户现已明确选择框架标准化和 Eino-primary，本任务按生产迁移执行；历史材料仅用于提醒哪些领域不变量不能误删。
