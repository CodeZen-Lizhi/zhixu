# 知序架构文档

> 当前架构：本地优先 Web 应用、Go 模块化单体、Ports and Adapters
>
> 正式知识：Markdown + Git
> 查询与运行状态：PostgreSQL + pgvector

## 1. 架构原则

1. 正式知识写入只有 Proposal → Approval → Safe Writeback 一条路径。
2. 原始 Source Version 与 Content Artifact 不可变。
3. Markdown + Git 是正式知识事实源；数据库和索引不能反向覆盖用户文件。
4. PostgreSQL 查询投影可以重建，Proposal、Approval、Workflow、Audit、Review 与 Memory 等运行历史必须备份。
5. Agent 只生成结构化决策或工具请求，不能直接写文件、数据库或 Git。
6. 长任务进入持久 Workflow；副作用幂等，失败、降级和人工恢复状态显式可见。
7. Graph、Collection、Timeline 和 Search Index 是可重建查询视图，不是第二事实源。
8. 模型、Parser、Git、文件系统、网页和调度器通过 Adapter 接入；领域类型不依赖第三方 SDK。
9. 模块 Interface 同时是调用方边界和 Contract Test seam；模块只能由自身 owner 写入核心事实。
10. 先用容量与故障证据证明 PostgreSQL + pgvector 不足，再评估独立向量库、图数据库、缓存或微服务。

## 2. 章节导航

| 文档 | 唯一职责 |
|---|---|
| [系统设计](system-design.md) | 系统边界、进程与模块、Interface/Adapter、当前技术栈、部署拓扑 |
| [领域与数据](domain-and-data.md) | 统一语言、领域模型、数据所有权、一致性、生命周期和数据库设计原则 |
| [应用契约](application-contracts.md) | HTTP/SSE 语义、前端边界、状态所有权和配置加载契约 |
| [AI 与工作流运行时](ai-runtime.md) | 检索、Agent、RAG、工具、持久工作流和十类业务执行流 |
| [质量架构](quality.md) | 安全、工具权限、可观测、审计、性能、测试与 AI 评测 |
| [Workspace Analysis 发布 Runbook](runbooks/workspace-analysis-rollout.md) | 新模式的迁移、灰度、观测、Worker-only 回滚和生产证据边界 |
| [GORM 发布与回滚](runbooks/gorm-persistence-rollout.md) | 单池接线、pgx 例外、发布顺序、代码回滚及历史升级限制 |
| [ADR 索引](adr/README.md) | 关键选择的历史理由、替代方案和状态 |

安装、启动、升级、备份、恢复和排障见 [运行与恢复手册](../operations.md)。产品范围与稳定验收见 [需求文档](../requirements.md)。

## 3. 关键质量属性

| 属性 | 架构响应 |
|---|---|
| 数据所有权 | Workspace 保存用户文件，Git 保存正式内容历史 |
| 可追溯 | Source、Evidence、Claim、Proposal、Approval、Commit、Workflow 相互关联 |
| 一致性 | 版本 CAS、原子文件替换、Git 证明、Outbox、索引补偿和只读恢复 |
| 可恢复 | 持久节点、Attempt、lease、幂等键、checkpoint、补偿和 Runbook |
| 可解释 | Citation、Applicability、冲突、置信因素、影响分析和稳定错误码 |
| 安全 | 精确 Root Grant、最小 Tool Capability、SSRF/Prompt Injection 防护和一次性写授权 |
| 可替换 | 外部依赖隐藏在深 Adapter 后，Composition Root 负责选择实现 |
| 可评测 | 固定数据集、版本化模型/Prompt/Schema/索引和回归门禁 |

## 4. 契约归属

- 精确 HTTP wire：[OpenAPI](../../api/openapi/openapi.json)。
- 精确数据库结构与迁移顺序：[Atlas migrations](../../atlas/migrations/)。
- 当前依赖版本：`go.mod`、`go.sum`、`web/package.json`、`web/package-lock.json` 和部署镜像文件。
- 当前任务和交付证据：`.trellis/tasks/`；架构正文不维护完成勾选或测试日期。
- 关键决策保持在 [ADR](adr/README.md)，正文只表达当前有效结论。
