# 完整 Eino RAG Graph 覆盖率与净收益

> 历史记录：该 No-Go 基于“必须净减代码才采用”的旧门禁。2026-08-08 用户明确要求 Eino 成为正式生产 Runtime；当前执行合同以本任务最新 PRD/design/implement 为准。本文件仅保留领域不变量和风险证据，不再决定是否迁移。

## 结论

**No-Go：不新增完整 Eino RAG Workflow/Graph，也不新增无消费者的 Eino Retriever bridge。**

Eino 能表达该流程，但当前复杂度主要来自项目领域与持久化不变量。把这些 Port 包成 Lambda 能得到一张 Graph，却不会删除不变量，反而新增 DTO 映射、Graph state、selector 和双路径测试。生产继续采用 `RAGExecutor`，并复用已通过门禁的 Eino Chat、Embedding 和 Structured Scheduler。

## 冻结矩阵

| 强制需求 | 权重 | 路径 A：现有 Executor + Eino components | 路径 B：Eino 原生已证明覆盖 |
|---|---:|---:|---:|
| receipt replay、Memory/ModelRun、原子 terminal finalization | 20 | 20 | 0 |
| FTS/pgvector/RRF/Active Index 与 retrieval tuple | 20 | 20 | 0 |
| Evidence、provenance、citation、faithfulness、refusal | 20 | 20 | 0 |
| Schema、预算、Model Run/Call 审计 | 15 | 15 | 15 |
| Progress/SSE、取消、错误与 manual recovery | 15 | 15 | 5 |
| Selector、可观测性与回滚 | 10 | 10 | 5 |
| **合计** | **100** | **100** | **25** |

`25/100` 不是框架理论能力上限。若新增项目 Lambda/bridge，纸面功能可接近 100，但这些 wrapper 仍由项目实现和维护，不构成框架原生覆盖或代码删除。

## 代码证据

- `internal/agent/adapter/workflow/rag_executor.go` 拥有 terminal receipt replay、冻结 Context/Memory、ModelRun/RecordingChatModel 和原子 finalizer。
- `internal/agent/application/rag.go` 拥有请求/Scope 校验、Clarification、rewrite retrieval、Index/Embedding version 与 degradation 漂移拒绝、Evidence Eligibility、Conflict、Topic allowlist、Schema/ModelRun 绑定、Citation/Faithfulness 和唯一终态。
- `internal/agent/adapter/eino/scheduler.go` 已用 Eino Graph 承担通用的 `INITIAL -> REPAIR -> REDUCED` 调度；RAG 测试已证明该 scheduler 进入真实执行路径。
- 官方 Eino 没有项目 PostgreSQL FTS/pgvector/RRF/Active Index 的等价 Retriever，Checkpoint 也不能替代 River/PostgreSQL 恢复事实。

## 净收益

当前核心相关生产代码约 1,047 行：`rag.go` 565 行、外层 workflow executor 332 行、Eino phase scheduler 150 行。完整 Graph 预计只能替换约 100-180 行顺序连接代码；Evidence、retrieval consistency、progress、finalization 等仍需保留。

路径 B 保守新增 450-800 行生产代码与至少 500 行测试：Graph state/node/branch、Retriever document 映射、direct/Eino selector、固定 fixture、PostgreSQL/River 重投递和 terminal response-loss 对照。代码量、失败面和回滚成本均为净增加。

## 重开条件

- 出现两个以上需要共享相同 RAG 子图的真实生产消费者；
- 能明确删除一段项目通用调度，而不是把业务 Port 改写成 Lambda；
- direct/Eino 固定 fixture、真实 PostgreSQL/River 重投递、terminal response-loss、取消和 progress 等价门禁都有预算与负责人。

## 验证

`go test ./internal/agent/application ./internal/agent/adapter/eino ./internal/agent/adapter/workflow` 通过，证明当前项目 Executor、已采用的 Eino phase Graph 与真实 RAG Workflow 组合仍保持绿灯。由于本门禁为 No-Go，没有新增生产 selector、Retriever bridge 或平行 Graph，也不虚报未执行的 PostgreSQL/River 新路径测试。
