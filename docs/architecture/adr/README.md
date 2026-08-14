# Architecture Decision Records

| ADR | 决策 | 状态 |
|---|---|---|
| [0001](0001-modular-monolith.md) | 模块化单体 | accepted |
| [0002](0002-local-first-web.md) | 本地优先 Web 应用 | accepted |
| [0003](0003-markdown-git-source-of-truth.md) | Markdown + Git 事实源 | accepted |
| [0004](0004-postgresql-pgvector.md) | PostgreSQL + pgvector | accepted |
| [0005](0005-no-graph-database-v1.md) | v1 不引入图数据库 | accepted |
| [0006](0006-postgres-durable-workflow.md) | PostgreSQL 持久化工作流 | accepted |
| [0007](0007-no-langchain-core-dependency.md) | 核心不依赖 LangChain | accepted |
| [0008](0008-proposal-approval-write-seam.md) | 唯一正式写入 seam | accepted |
| [0009](0009-git-cli-adapter.md) | Git CLI Adapter | accepted |
| [0010](0010-sse-for-server-events.md) | SSE 任务事件 | accepted |
| [0011](0011-retrieve-latest-approved-revision.md) | 默认只检索最新批准版本 | accepted |
| [0012](0012-version-workflows-prompts-schemas.md) | Workflow/Prompt/Schema 版本化 | accepted |
| [0013](0013-eino-adoption-gate.md) | Eino 采用门禁与 M2 不采用结论（对应生产结论已由 ADR-0027 取代） | accepted |
| [0014](0014-single-user-authentication.md) | 单用户 Session、API Token 与写授权分离 | accepted |
| [0015](0015-river-goose-runtime.md) | River/Goose Runtime 版本、迁移和兼容边界 | accepted |
| [0016](0016-capacity-performance-baseline.md) | 确定性容量数据集与可审计性能门禁 | accepted |
| [0017](0017-exact-workspace-root-grant.md) | Host Controller 与精确 Workspace Root Grant | superseded in delivery by 0020 |
| [0018](0018-workspace-root-identity.md) | Workspace Root 与 Workspace 身份边界 | accepted |
| [0019](0019-mature-framework-first.md) | 成熟框架优先与自研例外门禁 | accepted |
| [0020](0020-docker-direct-web-and-one-shot-workspace-control.md) | Docker 固定 Web 入口与一次性 Workspace Control | accepted |
| [0021](0021-stable-network-namespace-anchors.md) | 稳定 Network Namespace Anchor 保障 Docker 项目重启 | accepted |
| [0022](0022-model-runtime-hot-activation.md) | 模型运行时无容器重启热应用 | accepted |
| [0023](0023-managed-local-ollama-runtime.md) | 主 Compose 小型管理容器按需运行本地 Ollama | accepted |
| [0024](0024-layered-eino-adoption.md) | Eino 分层采用、Chat/Callback 与 Structured Output 短 Graph 灰度（对应生产结论已由 ADR-0027 取代） | superseded |
| [0025](0025-eino-embedding-adoption.md) | Eino OpenAI-Compatible/Ollama Embedding 可回滚采用（对应生产结论已由 ADR-0027 取代） | superseded |
| [0026](0026-eino-runtime-expansion-gates.md) | Eino RAG、Tool、Streaming、Checkpoint 扩展门禁结论（前三项已由 ADR-0027 取代） | superseded |
| [0027](0027-eino-primary-ai-runtime.md) | Eino 正式生产 AI Runtime，接入 `/chat` RAG（当前） | accepted |
