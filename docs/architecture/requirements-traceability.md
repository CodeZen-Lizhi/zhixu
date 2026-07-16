# PRD—架构需求追踪矩阵

## 1. 目标

证明正式 PRD 的每个核心功能在架构、流程、数据和测试文档中都有落点。

## 2. 功能追踪

| PRD | 功能 | 主要架构文档 | 流程/质量 |
|---|---|---|---|
| 10.1 | Workspace | system-context、module、data | lifecycle、security |
| 10.2 | Inbox/导入 | data、module、workflow-engine | workflow 01 |
| 10.3 | 解析/分块 | retrieval、interfaces | workflow 01 |
| 10.4 | 混合检索 | retrieval、database | RAG eval |
| 10.5 | 文章优化 | agent-rag、domain | workflow 02 |
| 10.6 | Claim/关系分析 | domain、agent-rag | workflow 01/09 |
| 10.7 | Proposal | domain、module、database | workflow 03 |
| 10.8 | Approval | domain、api、tool-security | workflow 03 |
| 10.9 | 写回/Git | data、tool-security、ADR-0009 | workflow 03、Runbook |
| 10.10 | RAG | retrieval、agent-rag | workflow 04、eval |
| 10.11 | Artifact | module、agent-rag | workflow 05 |
| 10.12 | 图谱 | domain、database、performance | workflow 06 |
| 10.13 | 语义反链 | retrieval、agent-rag | workflow 06 |
| 10.14 | Smart Collection | module、database、api | workflow 08 input |
| 10.15 | 知识健康 | domain、module、database | workflow 07 |
| 10.16 | 时间线/影响 | domain、data、observability | workflow 09/10 |
| 10.17 | Memory | domain、agent-rag、database | security/eval |
| 10.18 | Review/面试 | domain、module、database | workflow 08 |
| 10.19 | Workflow | workflow-engine、database | workflow 01–10 |
| 10.20 | Tool Calling | interfaces、tool-security | security tests |
| 10.21 | 可观测/审计 | observability、database | testing |
| 10.22 | 设置/维护 | deployment、data、api | Runbooks |
| 10.23 | 生命周期 | domain、data、module | workflow 10 |
| 10.24 | Conflict | domain、agent-rag | workflow 09 |

## 3. 非功能追踪

| 需求 | 文档 |
|---|---|
| 数据所有权 | data-architecture、ADR-0003 |
| 可恢复 | workflow-engine、runbooks |
| 安全 | security、tool-security |
| 性能 | performance、retrieval |
| 可观测 | observability |
| AI 质量 | testing-and-evaluation、agent-rag |
| 可替换 | interfaces-and-adapters |
| 部署 | deployment |

## 4. 验收 seam

| Seam | 架构文档 |
|---|---|
| 知识变更闭环 | workflow 01/03、data、workflow-engine |
| 文章优化 | workflow 02、agent-rag |
| RAG | workflow 04、retrieval、evaluation |
| Artifact | workflow 05、agent-rag |
| 图谱/健康 | workflow 06/07、database、performance |
| 集合/复习 | workflow 08、domain、evaluation |

## 5. 变更规则

- PRD 新增 Must 功能时必须新增追踪行。
- 架构文档删除能力前必须检查对应 PRD。
- 验收失败需要定位到流程、模块、数据或质量文档中的责任点。

