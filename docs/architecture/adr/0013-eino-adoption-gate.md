---
status: accepted
---

# Eino 采用门禁与 M2 不采用结论

> `M2 Outcome` 中“主模块不正式采用 Eino”的结论已由
> [ADR-0019](0019-layered-eino-adoption.md) 部分取代；Embedding 后续由
> [ADR-0020](0020-eino-embedding-adoption.md) 采用，其余扩展门禁由
> [ADR-0021](0021-eino-runtime-expansion-gates.md) 收口。**ADR-0022 已在 Chat、Embedding、Structured Scheduler、
> RAG、Tool/ReAct 与 Streaming 的生产采用范围取代本 ADR 的旧结论；**本 ADR 关于 Domain、持久 Workflow、权限、审批、
> Tool 执行和安全写回的边界继续有效。

项目需要验证 Eino 是否能降低 Chat、Embedding、Streaming、Structured Output、Tool Calling 和可观测回调的集成成本，但不能让未经验证的框架成为领域或持久化执行模型的事实源。

## Decision

- Eino 只能位于 Agent/Application 的短流程编排边缘，或 Adapter/Infrastructure 内部。
- Domain、Workflow 状态机与持久化、Proposal/Approval、Tool Permission 和 Approval Write Authorization 不依赖 Eino 类型、Graph 或运行时。
- River/PostgreSQL 继续拥有 Workflow Run、Node Run、租约、重试、Human Task、补偿和恢复状态。
- 所有 Eino 输出仍需经过领域 Schema、Evidence、Permission 和业务不变量校验。
- 只有 PoC 验证 Chat、Embedding、Retriever/Rerank 接入、Streaming 取消与资源释放、Structured Output/有限修复、Tool Calling 权限隔离、Callback/Trace、错误分类、限流和 River Node 集成后，才能在 Manifest、Lockfile 和 CI 中锁定经验证版本。
- 任一关键门禁失败时，默认回退直接 OpenAI-Compatible Adapter；调用方 Interface 和领域契约不变。

## M2 Outcome

M2 PoC 已完成。Streaming、Structured Output、Embedding/Retriever/Rerank、River Node 和真实 Provider Smoke
未全部通过采用门禁，因此主模块不正式采用 Eino。M6-02 使用项目自有 Application 编排和直接
OpenAI-Compatible HTTP Adapter；Ollama 仅通过兼容 endpoint 接入。重新评估 Eino 需要新的 ADR 和完整门禁，
不能把本 ADR 当作当前候选实现授权。

## Considered Options

- 将 Eino 作为核心 Agent/Workflow 框架。
- 通过稳定 Interface 在边缘采用 Eino，并设置 PoC 门禁。
- 完全不评估 Eino，只保留直接 HTTP Adapter。

## Consequences

- M2 必须先提供可重复 PoC 报告和 Contract Test，当前 ADR 不锁定任何未经验证版本。
- M2 时 Composition Root 只构造直接 OpenAI-Compatible Chat Adapter；ADR-0019 后可显式选择 Eino-backed Adapter，默认仍为 direct。
- 需要维护少量项目自有 Model、Retrieval、Tool 和 Workflow Interface。
- 框架升级或回退不需要迁移领域对象、Proposal 或 Workflow 持久化状态。

## Related Decisions

- [ADR-0006](0006-postgres-durable-workflow.md)：PostgreSQL 持久化 Workflow。
- [ADR-0007](0007-no-langchain-core-dependency.md)：核心不依赖通用 Agent Framework。
- [ADR-0012](0012-version-workflows-prompts-schemas.md)：Workflow、Prompt、Schema 版本化。
- [ADR-0019](0019-layered-eino-adoption.md)：Chat、Callback 与短 Graph 的后续分层采用。
- [ADR-0020](0020-eino-embedding-adoption.md)：双 Provider Embedding 的后续可回滚采用。
- [ADR-0021](0021-eino-runtime-expansion-gates.md)：其余 Runtime 扩展路线的实际门禁结论。
