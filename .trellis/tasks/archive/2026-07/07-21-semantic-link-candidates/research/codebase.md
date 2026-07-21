# M7-02 Codebase Research

## Existing Reusable Seams

### Knowledge

- `internal/knowledge/domain/relation.go` 已提供 NodeRef、RelationType 兼容矩阵、对称端点规范化、Relation/Evidence fingerprint 和 Relation 状态机。
- `internal/knowledge/application/commands.go` 已提供 `SuggestRelation`、`ConfirmRelation`、`TransitionRelation`，并通过 Provenance/Confirmation Port 校验正式写入。
- `internal/knowledge/adapter/postgres` 已实现 Workspace-scoped、CAS、幂等 receipt、Evidence 可达性和 canonical Relation 唯一约束。
- 这些能力只用于正式 Relation；Candidate 不能通过 `SuggestRelation` 提前落库。

### Graph

- `internal/graph/domain|application|adapter/postgres|http` 已实现七个只读 Topic/Claim Graph endpoint、HMAC cursor、Workspace 隔离、Evidence lazy load 与性能门禁。
- 架构目录把 `DiscoverCandidates` 归入 Graph Module，但现有代码没有候选类型、Repository、HTTP、Workflow 或 UI。
- 最小变更是在 Graph 模块增加独立 Candidate service/port，不修改现有 Query Service 和七个只读 Handler 方法。

### Retrieval And Agent

- Retrieval Search 提供 keyword/semantic/hybrid、Index/Embedding/Rerank version、Chunk/span/provenance/snippet 与显式 degradation。
- `QueryEmbedder` 当前是单查询接口；批量扫描不能逐候选远程调用，应按节点页和 Provider 上限编排。
- Agent `RelationAnalyzer` 负责 Evidence eligibility、五分类、结构化版本与 action mapping，但不发现候选、不保存状态。
- `agent.model_run` 已冻结 Model/Profile/Prompt/Schema/Retrieval 版本，可作为 Candidate 生成版本的可验证来源。

### Workflow

- Workflow/River 已提供持久 Run/Node/Attempt、lease、heartbeat、retry、cancel、Outbox、response-loss replay。
- Candidate scan 应注册真实 Definition/Executor，Job Args 只携带 Node Run identity/schema，不保存正文或模型文本。

### Change Control

- 当前 `Proposal`/`Revision` 绑定 `target_path/base_hash/content`，Approval Dispatch 固定进入文件 Safe Writeback。
- `change_control.approval` 的 Revision/change hash 绑定可复用，但文件字段和 dispatch 不能复用为 Relation apply。
- 历史 M5-05 研究给出兼容方案：Proposal 增加 `proposal_type`，Revision 增加 typed target/base/change/evidence/schema 字段；文件列改为按 type 条件约束，dispatch 按 type 路由。

## Missing Capabilities

- Candidate aggregate、fingerprint v1、suppression/reopen 和 decision history。
- 候选查询、scan run、workflow definition/executor、River delivery。
- typed Relation Proposal、Relation Approval apply 与候选/Proposal binding。
- 候选 OpenAPI/strict frontend decoder/Graph panel。
- Candidate Precision/Recall、ignored reappearance 评测与真实 smoke。

## Primary Risks

1. 把 Candidate 写进 `core.relation` 会让未批准内容成为 Knowledge 正式聚合。
2. 用 Graph Evidence fingerprint 代替 Candidate fingerprint 会遗漏 Node Version 和 Model/Rule Version。
3. 用假文件字段创建 Relation Proposal 会触发错误的 Safe Writeback/Git 语义。
4. Candidate service 依赖缺失若复用 Graph readiness，会让模型故障拖垮已确认图查询。
5. 批量扫描若逐节点/逐候选调用数据库或模型，会形成 N+1 和不可控成本。
6. 只有五分类评测而无 Candidate recall/ignore 重现评测，无法证明 AC-19。
