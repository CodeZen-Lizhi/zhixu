# ZHIXU / 知序文档中心

本目录保存知序项目的产品需求、领域模型、架构设计、业务工作流、架构决策和运行手册。

## 产品文档

- [正式产品需求文档](product/PRD.md)
- [产品需求大纲](product/PRD-outline.md)

## 架构文档

- [架构文档总入口](architecture/README.md)
- [统一领域语言](architecture/CONTEXT.md)
- [系统上下文与容器架构](architecture/system-context.md)
- [领域模型](architecture/domain-model.md)
- [模块化单体架构](architecture/module-architecture.md)
- [技术栈与依赖选择](architecture/technology-stack.md)
- [进程启动配置架构](architecture/configuration.md)
- [部署架构](architecture/deployment.md)
- [数据架构](architecture/data-architecture.md)
- [数据库设计](architecture/database-design.md)
- [检索与索引架构](architecture/retrieval-architecture.md)
- [Agent 与 RAG 架构](architecture/agent-rag-architecture.md)
- [持久化工作流引擎](architecture/workflow-engine.md)
- [安全架构与威胁模型](architecture/security.md)
- [可观测性与审计架构](architecture/observability.md)
- [测试与 AI 评测](architecture/testing-and-evaluation.md)

## 专项目录

- [业务工作流](architecture/workflows/)
- [架构决策 ADR](architecture/adr/README.md)
- [运行手册 Runbook](architecture/runbooks/)
- [架构落地实施计划](architecture/implementation-plan.md)
- [架构落地任务清单](architecture/implementation-checklist.md)
- [PRD—架构追踪矩阵](architecture/requirements-traceability.md)

## 文档维护规则

1. PRD 是功能范围和验收标准的依据。
2. `architecture/CONTEXT.md` 是领域术语依据。
3. 难以逆转的架构选择记录在 ADR 中。
4. PRD 变化后同步更新架构文档和需求追踪矩阵。
5. 文档中的 Mermaid 图与所属 Markdown 一起维护。
