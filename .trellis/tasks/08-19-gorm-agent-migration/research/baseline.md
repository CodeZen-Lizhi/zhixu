# Agent GORM 迁移基线

## 持久化面

`internal/agent/adapter/postgres` 的 legacy `Repository` 当前实现七类契约：

1. Model Run/Call 创建、读取、终结与 crash recovery；
2. caller-owned `ModelRunTxFinalizer(any)`；
3. RAG Memory Snapshot；
4. Workspace Analysis Run persistence/loader；
5. Workspace Analysis Worker Capability；
6. Workspace Analysis Model Operation/Checkpoint/Candidate Authority；
7. RAG Progress Event Store。

生产构造分布在 API/Worker、Capture、Artifact、Conversation、Organizing 和集成测试。当前全部使用 legacy `NewRepository` 或 `NewRAGProgressStore`，没有 Agent GORM 构造。

## 跨模块事务

- Capture Profile 在 Capture 事务内读取/锁定 Agent Model Run/Calls 并 CAS 终结；
- Conversation dispatch 在同一 pgx 事务中创建 Workflow/Question/Answer/Event/Workspace Analysis Run 并检查 Capability；
- RAG Progress 在自身短事务中获取 advisory lock 后调用 Events `AppendTx(any)`；
- Workspace Analysis Model Operation 在一个事务中锁定 Workflow lease/fence 与 Agent 多表预算/运行事实。

Legacy Agent Adapter 当前直接查询并锁定 `workflow.run`、`workflow.node_run` 和 `workflow.node_attempt`。GORM staged 路径不能复制这一跨 owner SQL；Workflow child 必须提供同 scope 的 execution fence，负责前三个锁并返回不可变 lease/cancel/definition 快照，Agent 再锁自己的表。缺失/父子 binding 不匹配使用 `found=false`，Agent 保持现有 authorization conflict；其余跨 owner 错误必须翻译为 Agent code，不能直接透传 Workflow code。

因此不能把 `pgx.Tx` 换成独立 GORM root 调用。新路径必须由 `foundation.TransactionScope` 和平台 UoW 保持同一事务，legacy 路径保留到各 consumer 迁移与 Final。Workflow fence 只阻断 Workspace Analysis Model Operation 阶段，不阻断 Model Runtime/Memory。

## 最小阶段

优先落地 Capture 所需三方法 scoped Model Run Finalizer、Artifact/Conversation 所需按 Attempt finder 和 GORM Run/Call 实现；随后迁移 Memory、Workspace Analysis 和 RAG Progress。任何阶段在 TODO 9 前都不接生产 Composition。
