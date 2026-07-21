# M7-02 Requirements Research

## Sources

- `docs/product/PRD.md` 10.13、14.6、14.7、17.6、AC-19。
- `docs/architecture/workflows/06-graph-and-semantic-links.md`。
- `docs/architecture/adr/0008-proposal-approval-write-seam.md`。
- `docs/architecture/{domain-model,database-design,module-architecture,testing-and-evaluation}.md`。
- `.trellis/tasks/archive/2026-07/07-19-knowledge-domain/**`。
- `.trellis/tasks/archive/2026-07/07-20-graph-projection-queries/**`。

## Confirmed Product Contract

1. 候选漏斗是 `发现 -> 证据审阅 -> 用户决定 -> Relation Proposal -> Approval -> Knowledge Relation`，不是 Graph 直接写边。
2. 卡片必须展示双方摘要、对应段落、建议关系、理由、置信度和发现方式；动作包括确认、改类型确认、忽略、误报、稍后。
3. 忽略绑定双方版本、模型版本、Evidence Hash 和原因；未变化不得重复推荐，重要变化后可重开并明确说明原因。
4. 发现来源为标题/别名、术语、Claim 语义、共同 Topic、同 Source、RAG 共召回。
5. 批量扫描异步，结果按置信度/Relation Type 分组，每个确认项仍创建独立 Proposal。
6. Candidate 模型不可用不得影响已确认 Graph；误报率和候选召回/精度需要离线评测。

## Conflicts And Resolutions

| Conflict | Evidence | Resolution |
|---|---|---|
| 最终描述包含 Document，但当前 Relation/Graph 只注册 Topic/Claim | PRD 与 `migrations/00017_knowledge_domain.sql`、Graph v1 | M7-02 首版只支持 Topic/Claim；Document 后续 additive 扩展 |
| `relation.status=SUGGESTED/REJECTED` 看似可当候选，但 Relation 是正式 Knowledge 聚合 | `internal/knowledge/domain/relation.go` | Candidate 独立持久化；不写 `core.relation` |
| 当前 Change Control 只有文件补丁 Proposal，产品要求 Relation Proposal | `internal/changecontrol/domain/model.go`、00004/00005、历史 M5-05 研究 | Expand 为 typed `knowledge_change` Proposal；历史 `file_patch` 兼容 |
| workflow 草图只写 Confirm/Ignore，PRD 还要求误报/稍后/重开/批量 | workflow 06 与 PRD 10.13 | 本任务补齐完整状态机和 API |
| PRD 要目录/Topic/Smart Collection 扫描，但目录对象与 Collection AST 尚未交付 | 当前代码与 M7-03 任务 | 首版真实 Topic scan；其他 scope 保留 additive 契约，不做假实现 |

## Scope Boundary

- Graph 模块拥有候选发现和候选生命周期；Knowledge 继续拥有唯一正式 Relation；Change Control 拥有 Proposal/Approval。
- 已交付 Graph 查询继续是七个只读端点，候选命令使用独立路由和依赖状态。
- M7-02 需要 typed Relation Proposal 和 Knowledge apply seam；只创建 Proposal 而不能在批准后形成 Relation 不构成完整产品闭环。
- M7-03/M7-04 分别拥有 Collection/Health 和 Timeline/Impact；不得在 M7-02 预建空壳。

## Evaluation Requirements

- Relation Type Accuracy。
- Evidence Support Rate。
- Candidate Precision@K。
- Candidate Recall。
- Ignored Candidate Reappearance Rate。
- 冻结 Dataset、Model、Prompt、Schema、Index、Embedding、Rerank、Rule、Workflow 版本，Fake 与真实 Provider 结果分开报告。
