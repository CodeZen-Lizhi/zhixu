# M7-02 Architecture Decisions

## D1 Candidate Ownership

选择 Graph Module 内独立 `SemanticLinkCandidate` 聚合和 Application service。

- Graph 架构本来拥有 `DiscoverCandidates`。
- Candidate 是发现/审阅状态，不是 Knowledge 正式事实。
- 现有 Graph Query Service/Handler 保持只读，Candidate 使用独立 port/handler/readiness，避免模型故障影响正式图。

未选择：写入 `core.relation.SUGGESTED`。原因是 Relation 在当前代码中明确为正式 Knowledge 聚合，且 Graph/Eligibility 已依赖它的状态语义。

## D2 Typed Proposal Expansion

选择向现有 Change Control Aggregate 做 Expand，而不是建立第二套审批系统。

- `proposal.proposal_type` 默认 `file_patch`，历史行无行为变化。
- Revision 增加 typed `target_refs/base_versions/change_set/evidence_refs/schema_version`。
- file patch 与 knowledge change 使用互斥的数据库约束，不允许假路径/Hash/正文。
- Approval 身份、Revision/change hash 绑定复用；dispatch/apply 按 Proposal Type 路由。
- `file_patch` 继续现有 Safe Writeback/Git/Reindex；`knowledge_change` Relation Proposal 进入 Knowledge apply UoW。

未选择：单独 `relation_proposal` + `relation_approval` 表。原因是它会形成第二套 Proposal/Approval 事实源，破坏 ADR-0008。

## D3 Fingerprint And Reopen

Candidate 每次评估是不可变 fingerprint 快照；状态和 append-only decisions 记录用户处理。fingerprint v1 绑定：

```text
schema + workspace
+ canonical source ref/version
+ canonical target ref/version
+ proposed relation type
+ sorted evidence semantic hashes
+ sorted discovery methods
+ actual index/embedding/rerank versions
+ actual model/prompt/schema or rule version
```

相同 fingerprint 唯一。重要输入变化产生新 Candidate，并通过 `reopened_from_candidate_id` 关联最近被抑制记录；旧记录不改写。

## D4 First Release Scan Scope

首版真实支持单节点发现和 Topic scan。目录需要稳定 Document/目录对象，Smart Collection 需要 M7-03 Query AST；这两项只保留版本化 scope discriminator，不创建假执行器。

## D5 Formal Relation Apply

Candidate Confirm 与 Proposal Approval 是两个动作。Confirm 创建 ready-for-review Relation Proposal；Approval 重新验证 Candidate fingerprint、端点版本和 Evidence，再通过 Knowledge command 幂等写入 canonical Confirmed Relation，confirmation ref 使用 Approval ID。任何基线漂移进入 needs_revision，Candidate 只显示 Proposal 状态，不声称正式关系已存在。

## D6 Topic-scoped Candidate Membership Projection

真实 Topic scan 生成同一 Topic 成员之间的 Claim↔Claim Candidate，而旧列表查询把 `node_type=TOPIC` 解释为
Candidate 端点必须直接等于 Topic。结果是 Scan 已 `SUCCEEDED` 且 `candidate_count>0`，Topic 页面却稳定返回空
列表。这是跨层契约与集成覆盖缺口，不是 River、持久化或轮询时序故障。

选择保留现有 Topic node scope，并定义为：直接引用 Topic 的 Candidate，或两端 Claim 都通过正式
CLAIM→TOPIC/BELONGS_TO/CONFIRMED Relation 属于该 Topic。PostgreSQL 使用两个 `EXISTS` 读取 Knowledge 事实，
避免 membership JOIN 复制 Candidate 行；Domain/HTTP/前端只校验 Topic 下允许 Claim-pair 形态，Claim scope 仍
精确匹配端点。未选择 Workspace 全量 fallback，因为它会混入其他 Topic；也未增加 scan_id 绑定，因为 Candidate
当前不保存 scan 归属，Topic membership 已是可验证且可跨刷新恢复的正式事实。

预防机制包括真实 PostgreSQL 正/负 membership 回归、真实 Claim-pair 前端 fixture、OpenAPI scope 描述和
桌面/移动浏览器 smoke。后续修改 Candidate scope 时必须同时核对 discovery 产物形态、Repository 过滤、Domain
page validation、前端 decoder 与真实 API 浏览器结果。
