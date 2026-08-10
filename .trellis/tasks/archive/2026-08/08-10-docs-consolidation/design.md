# 文档体系收敛设计

## 1. 设计原则

本次优化采用“逻辑事实源收敛”，不采用简单按文件数量删除。一个事实只能有一个权威归属；章节可以拆文件，但不能重复维护同一需求、状态、契约或决策理由。

## 2. 目标信息架构

```text
docs/
  README.md                         # 唯一总入口与维护规则
  user-guide.md                     # 面向用户的完整功能与典型操作流程
  requirements.md                   # 产品范围、需求编号、验收标准
  roadmap.md                        # 高层里程碑、依赖和未交付方向
  operations.md                     # 安装、配置、运行、升级、备份、恢复、排障
  architecture/
    README.md                       # 当前架构摘要与章节入口
    system-design.md                # 系统边界、模块、接口与适配器、技术基线
    domain-and-data.md              # 术语、领域模型、事实源、表设计原则
    application-contracts.md        # API/SSE/前端/配置边界；精确 wire 引用 OpenAPI
    ai-runtime.md                   # 检索、Agent、持久工作流与业务流程
    quality.md                      # 安全、工具权限、可观测、性能、测试与评测
    adr/                            # 一项关键决策一份短记录，保留编号与历史
```

目标是 11 个左右的常规长期 Markdown，加现有 ADR。ADR 是独立决策记录，不计为重复的架构说明；其索引并入架构入口或继续保留一个极短索引，具体在迁移时以链接清晰度决定。

## 3. 事实源边界

| 信息 | 权威源 | 文档中的表达 |
|---|---|---|
| 用户价值与使用方式 | `docs/user-guide.md` | 全部用户可见功能、入口、主要操作、结果与限制，不写技术实现 |
| 产品范围与验收 | `docs/requirements.md` | 稳定需求/AC，不写里程碑日志 |
| 当前任务状态 | `.trellis/tasks/` | `docs/roadmap.md` 只链接，不复制检查框 |
| 当前架构 | `docs/architecture/*` | 原则、边界、数据流和关键不变量 |
| 技术选择理由 | `docs/architecture/adr/*` | 架构正文只引用结论 |
| HTTP wire | `api/openapi/openapi.json` | 架构只说明风格和边界 |
| 数据库物理结构 | `migrations/*.sql` | 架构保留概念 ER、所有权和事务原则 |
| 运行命令与配置 | CLI/Compose/配置实现 | `docs/operations.md` 提供受支持操作 |
| 工程规范 | `AGENTS.md`、`.trellis/spec/`、`CONTRIBUTING.md` | `docs/` 不维护平行标准 |
| 时间点报告 | Trellis task `research/` | 不进入长期文档导航 |

## 4. 内容迁移策略

### 产品文档

- 从 `PRD.md` 提取全部用户可见功能及端到端使用闭环到 `user-guide.md`。
- 将稳定需求、优先级、非目标、需求编号和验收标准收敛到 `requirements.md`。
- 将逻辑数据模型、接口原则、测试策略和交付顺序迁入对应架构章节或 `roadmap.md`。
- 将阶段交付说明迁入相关 Trellis 任务；不能确认任务归属时，保存在本任务 research 映射中，不继续污染需求事实源。
- `PRD-outline.md` 仅迁移正式 PRD 未覆盖的独有信息，随后移除。
- 需求优化清单保留用户需求和验收边界；技术替换任务进入路线图/Trellis，原文件作为本任务研究证据保留到任务归档。

### 架构文档

- `CONTEXT.md` 与 `domain-model.md` 合并为 `domain-and-data.md` 的术语和领域章节。
- `data-architecture.md` 与 `database-design.md` 合并到同一逻辑章节；表级精确事实改为迁移引用。
- `system-context.md`、`module-architecture.md`、`interfaces-and-adapters.md`、`technology-stack.md` 合并为 `system-design.md`。
- `api-and-events.md`、`frontend-architecture.md` 和配置的应用边界合并为 `application-contracts.md`；精确端点不重复 OpenAPI。
- `retrieval-architecture.md`、`agent-rag-architecture.md`、`workflow-engine.md` 与十份业务 workflow 合并为 `ai-runtime.md`，产品流程和技术执行流各保留一次。
- `security.md`、`tool-security.md`、`observability.md`、`performance.md`、`testing-and-evaluation.md` 合并为 `quality.md`，保留威胁、门禁和验收基线，删除里程碑日志。
- `configuration.md`、`deployment.md` 和五份 runbook 的操作内容合并为 `operations.md`；架构所需部署拓扑留在 `system-design.md`。
- `implementation-plan.md` 只保留高层未完成路线到 `roadmap.md`；`implementation-checklist.md` 的完成状态由 Trellis 接管。
- `requirements-traceability.md` 由需求 ID、架构引用和任务验收代替，不再人工维护矩阵。

### 过程文档

- `code-review-standard.md` 中仍有效的人工贡献规则收敛到根目录 `CONTRIBUTING.md`，自动化和 AI 规范继续以已有配置、`AGENTS.md` 和 `.trellis/spec/` 为准。
- `code-review-report-2026-08-10.md` 原文迁入本任务 `research/`；未解决项只保留可追踪引用，不把时间点结论写入长期文档。

## 5. 兼容与可恢复

- 优先使用 `git mv` 保留可追踪历史，但合并多源文件时以最终内容映射为主要证据。
- 删除旧路径前一次性更新 README、AGENTS、`.trellis/spec/`、活跃任务上下文和其他长期 Markdown 引用。
- 不机械改写 `.trellis/tasks/archive/` 或 research 中的旧路径与旧行号；这些内容是历史证据，由迁移映射提供查找桥梁。
- 不长期保留重定向空壳；迁移提交和 `research/document-inventory.md` 提供旧路径到新路径的查找入口。
- 任何被误删文本可从迁移前 Git 基线恢复；未提交的用户修改先通过 `git diff` 纳入迁移，再处理原文件。

## 6. 主要风险

1. **语义丢失**：相似段落包含不同约束。通过逐文件映射和关键词复核控制。
2. **巨型文档**：过度合并降低可读性。通过少量互斥架构章节控制。
3. **链接断裂**：项目规范大量引用旧路径。迁移后执行全仓旧路径搜索和本地链接检查。
4. **用户改动被覆盖**：当前工作区有相关脏改。实施时先保存逐文件 diff，并在最终 review 中逐项核对。
5. **目标/现状再次混合**：需求只写目标，Trellis 只写状态，架构只写当前有效设计，ADR 只写理由。

## 7. 回滚

- 文档迁移按“新结构与映射”“内容迁移”“旧文件移除与链接更新”分批执行和检查。
- 未经用户明确授权不提交；工作区级回滚依赖精确文件恢复或 Git 历史，禁止使用破坏性 reset/checkout。
- 任一阶段发现无法证明内容已迁移时，保留旧文档并记录阻塞，不为达到数量指标强行删除。
