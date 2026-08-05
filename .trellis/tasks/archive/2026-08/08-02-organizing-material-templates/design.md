# 材料确认与整理模板：技术设计

## Module

新增 `internal/organizing`，内部划分 Draft/Suggestion、Snapshot、Template 和 Workflow bridge。Organizing 只通过公开端口读取 Retrieval/Profile/Knowledge/Collection，通过 Artifact/Change Control bridge 产生结果。

## Persistent Model

- `organizing_draft`：Workspace、intent、scope、status、version、request binding。
- `organizing_draft_material`：candidate identity、默认/当前选择、召回原因、score/provenance 与 source status。
- `workflow_input_snapshot`：Draft version、Template Revision、canonical hash、created_at。
- `workflow_input_material`：强类型 material ref、version/content hash/evidence binding、顺序。
- `organizing_template` 与 append-only `organizing_template_revision`：声明、schema version、canonical hash。
- `organizing_run_binding`：Snapshot、Workflow Run、result kind/ref。

Draft 是可变草稿，使用 expected version CAS；Snapshot、Template Revision 和 Run binding 不可变。

## Suggestion

Suggestion Service 将用户 intent 规范化后并行请求 Retrieval、Profile 和正式 Knowledge 查询，再以稳定规则去重、合并并生成 reason code。模型可用于补充查询词，但不能直接决定正式性或隐藏来源。

候选分页和批量 hydration 必须有界，避免逐项查询。Source/Document/Claim/Collection material 使用判别联合，不能用一个自由 JSON ref 绕过类型校验。

同页补充搜索按单一 Material kind 查询 Workspace owner read model，只返回 identity/title/availability。用户点击加入后必须重新经过 Material Resolver，不能把搜索命中直接提升为冻结材料。

## Confirmation

确认事务先验证 Draft CAS，再在同一 `SERIALIZABLE` 事务锁定并重新校验每项材料 Workspace、可访问性、版本和 Evidence，冻结 Template Revision 与 canonical Snapshot hash，并写 Workflow start Outbox。重复命令按 Idempotency-Key/request hash 返回同一 Snapshot/Run；并发 `40001` 只在新事务中回查已提交 receipt，不重复 owner fence。

## Template Compiler

Template declaration 使用版本化 JSON Schema，但 Domain 会 canonicalize 为强类型结构。编译器只输出：

- 已注册 Definition ID/version。
- 允许的 Result Kind。
- 章节和表达参数。
- 固定证据、冲突和 GAP policy。

声明中不存在 tool、permission、node、retry、script 或 raw system prompt 字段。未知字段 fail closed。内置模板来自代码/迁移 Catalog，用户只能 clone。

## Workflow And Results

- `TOPIC_ARTICLE`: outline generation → Human Task → section generation → Artifact draft → optional Publish Proposal。
- `MERGE_DOCUMENTS`: comparison → merge draft/diff → Human confirmation → Change Control Merge Proposal。
- `KNOWLEDGE_REPORT`: generation → Artifact draft。
- `INTERVIEW_REVIEW`: generation → Artifact draft。

每个 generation node 从 Snapshot 读取 exact Evidence，通过 Agent structured schema 输出 Citation/Coverage/GAP。Workflow input 不复制正文，只引用 Snapshot ID 和绑定。Document Revision 正文由 Authoring owner 按 Snapshot 顺序批量打开并复核 version/hash，只在有界模型请求中以 `Dnnn` 标签短暂使用；Source Span Evidence 使用 `Ennn`，两者分别投影为 Artifact DocumentSource 与 Citation。

## API And UI

Organizing API 提供 Draft create/get/update-suggestion、Workspace-bound material search、material add/remove/confirm/start、Template list/detail/create/clone/update/revisions 和 Run/result 查询。

前端建立 `web/src/api/organizing.ts` 唯一 strict decoder，Query key 全部绑定 Workspace、Draft/Snapshot/Template/Run。整理页面是材料选择的唯一 UI owner；离开后服务端 Draft 恢复，不用 Browser Storage 建第二状态机。

## Compatibility And Rollback

新增独立 capability。关闭后 Search、Profile、Artifact 和 Proposal 保持可用。Migration additive；Template/Snapshot/Run 不删除。内置模板升级产生新版本，不修改历史运行。

## Risks

- 候选过多/慢：有界召回、批量 hydration、明确 top-k 与分页。
- Snapshot stale：确认事务重验并 fail closed。
- Prompt injection：Source 与自定义指令均为不可信输入，系统 prompt/tool policy 固定。
- 模板自由度漂移：严格 Schema + compiler allowlist + unknown field rejection。
