# docs 文档迁移盘点

## 1. 规模基线

- 总计：66 份 Markdown，16,874 行。
- 架构核心：24 份。
- ADR：21 份（含索引）。
- 业务 workflow：10 份。
- Runbook：5 份。
- 产品：3 份。
- 过程：2 份。
- 总入口：1 份。

## 2. 逐组去向

| 现有路径 | 目标事实源 | 处理 |
|---|---|---|
| `docs/README.md` | `docs/README.md` | 重写为唯一入口 |
| `docs/product/PRD.md` | `user-guide.md`、`requirements.md`、架构章节、`roadmap.md` | 按职责拆分后移除旧路径 |
| `docs/product/PRD-outline.md` | `requirements.md`、`user-guide.md` | 迁移独有内容后移除 |
| `docs/product/2026-08-01-requirement-optimization-list.md` | `requirements.md`、`roadmap.md`、Trellis research/tasks | 保留未提交改动，拆分后移除长期源 |
| `docs/architecture/README.md` | 同路径 | 收敛为架构摘要和章节入口 |
| `docs/architecture/CONTEXT.md` | `domain-and-data.md` | 合并术语 |
| `docs/architecture/domain-model.md` | `domain-and-data.md` | 合并领域模型 |
| `docs/architecture/data-architecture.md` | `domain-and-data.md` | 合并事实源、生命周期和一致性 |
| `docs/architecture/database-design.md` | `domain-and-data.md` | 保留概念模型/约束，精确结构引用迁移 |
| `docs/architecture/system-context.md` | `system-design.md` | 合并系统边界与容器 |
| `docs/architecture/module-architecture.md` | `system-design.md` | 合并模块和依赖方向 |
| `docs/architecture/interfaces-and-adapters.md` | `system-design.md` | 合并接口/Adapter 原则 |
| `docs/architecture/technology-stack.md` | `system-design.md`、ADR | 保留当前未提交修改；现状入架构、理由入 ADR |
| `docs/architecture/api-and-events.md` | `application-contracts.md`、OpenAPI | 保留原则；精确 wire 交给 OpenAPI |
| `docs/architecture/frontend-architecture.md` | `application-contracts.md` | 合并前端边界与状态管理 |
| `docs/architecture/configuration.md` | `application-contracts.md`、`operations.md` | 设计边界与操作说明分离 |
| `docs/architecture/retrieval-architecture.md` | `ai-runtime.md` | 合并检索运行时 |
| `docs/architecture/agent-rag-architecture.md` | `ai-runtime.md` | 合并 Agent/RAG |
| `docs/architecture/workflow-engine.md` | `ai-runtime.md` | 合并持久 Workflow |
| `docs/architecture/workflows/01-*` 至 `10-*` | `ai-runtime.md`、`requirements.md` | 技术流程保留一次，用户验收归需求 |
| `docs/architecture/security.md` | `quality.md` | 合并总体安全 |
| `docs/architecture/tool-security.md` | `quality.md` | 合并工具安全细化 |
| `docs/architecture/observability.md` | `quality.md` | 合并可观测与审计 |
| `docs/architecture/performance.md` | `quality.md` | 合并容量与性能原则 |
| `docs/architecture/testing-and-evaluation.md` | `quality.md`、`requirements.md` | 测试策略归架构，产品验收归需求 |
| `docs/architecture/deployment.md` | `system-design.md`、`operations.md` | 部署拓扑与操作步骤分离 |
| `docs/architecture/runbooks/*.md` | `operations.md` | 合并五份运行手册 |
| `docs/architecture/implementation-plan.md` | `roadmap.md` | 只迁移未来里程碑和依赖 |
| `docs/architecture/implementation-checklist.md` | `.trellis/tasks/` | 当前状态不再长期双轨维护 |
| `docs/architecture/requirements-traceability.md` | 稳定需求 ID、架构链接、Trellis 验收 | 移除人工矩阵 |
| `docs/architecture/adr/0001-*` 至 `0020-*` | 原路径 | 保留独立短记录和编号 |
| `docs/architecture/adr/README.md` | 架构入口或极短 ADR 索引 | 保留当前未提交修改；不复制决策正文 |
| `docs/process/code-review-standard.md` | `CONTRIBUTING.md`、`.trellis/spec/`、CI 配置 | 迁移人工规则后移除 |
| `docs/process/code-review-report-2026-08-10.md` | 本任务 `research/` | 原文归档，不作为长期事实源 |

## 3. 实施前必须复核的脏改

- `docs/architecture/adr/README.md`
- `docs/architecture/technology-stack.md`
- `docs/product/2026-08-01-requirement-optimization-list.md`

这些修改均视为用户内容。实施时必须逐段迁入目标文档或保留原路径，不得以仓库基线覆盖。

## 4. 关键搜索锚点

- 需求与验收：`10.x`、`AC-xx`、Must/Should/Could。
- 里程碑：`M4` 至 `M11` 及子编号，仅用于迁移状态历史，不继续写入需求事实源。
- 决策：`ADR-0001` 至 `ADR-0020`。
- 领域术语：Workspace、Source Version、Document、Claim、Relation、Proposal、Approval、Workflow、Artifact、Review、Memory。
- 精确事实：配置环境变量、CLI 命令、API 路径、错误码、数据库 Schema/表名、迁移编号。

最终验证应证明这些锚点仍能从新入口定位到权威源，或明确由代码契约接管。
