---
status: accepted
---

# Eino 仅在 PoC 通过后作为可替换边缘实现

项目需要验证 Eino 是否能降低 Chat、Embedding、Streaming、Structured Output、Tool Calling 和可观测回调的集成成本，但不能让未经验证的框架成为领域或持久化执行模型的事实源。

## Decision

- Eino 只能位于 Agent/Application 的短流程编排边缘，或 Adapter/Infrastructure 内部。
- Domain、Workflow 状态机与持久化、Proposal/Approval、Tool Permission 和 Approval Write Authorization 不依赖 Eino 类型、Graph 或运行时。
- River/PostgreSQL 继续拥有 Workflow Run、Node Run、租约、重试、Human Task、补偿和恢复状态。
- 所有 Eino 输出仍需经过领域 Schema、Evidence、Permission 和业务不变量校验。
- 只有 PoC 验证 Chat、Embedding、Retriever/Rerank 接入、Streaming 取消与资源释放、Structured Output/有限修复、Tool Calling 权限隔离、Callback/Trace、错误分类、限流和 River Node 集成后，才能在 Manifest、Lockfile 和 CI 中锁定经验证版本。
- 任一关键门禁失败时，默认回退直接 OpenAI-Compatible Adapter；调用方 Interface 和领域契约不变。

## Considered Options

- 将 Eino 作为核心 Agent/Workflow 框架。
- 通过稳定 Interface 在边缘采用 Eino，并设置 PoC 门禁。
- 完全不评估 Eino，只保留直接 HTTP Adapter。

## Consequences

- M2 必须先提供可重复 PoC 报告和 Contract Test，当前 ADR 不锁定任何未经验证版本。
- Composition Root 负责选择 Eino Adapter 或直接 Adapter。
- 需要维护少量项目自有 Model、Retrieval、Tool 和 Workflow Interface。
- 框架升级或回退不需要迁移领域对象、Proposal 或 Workflow 持久化状态。

## Related Decisions

- [ADR-0006](0006-postgres-durable-workflow.md)：PostgreSQL 持久化 Workflow。
- [ADR-0007](0007-no-langchain-core-dependency.md)：核心不依赖通用 Agent Framework。
- [ADR-0012](0012-version-workflows-prompts-schemas.md)：Workflow、Prompt、Schema 版本化。
