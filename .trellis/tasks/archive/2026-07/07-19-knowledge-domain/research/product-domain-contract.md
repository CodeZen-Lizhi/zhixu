# Research: Topic / Claim / Relation / Evidence / Conflict 产品领域契约

- Query: 从产品与领域文档提取 Topic、Claim、Relation、Evidence、Conflict 的权威需求、术语、不变量、用户流程、验收标准，并识别冲突与缺口，为 M5-05 Knowledge Domain 规划提供事实边界。
- Scope: internal
- Date: 2026-07-19

## Findings

### 1. 任务与权威来源

- 当前任务目标是实现 Topic、Claim、Relation、Evidence、Conflict 的统一领域模型、持久化约束和可验证业务规则，并为 M6-02 Agent 引用校验及 M7 Graph/Collection 提供事实边界；但任务自身 `prd.md` 的 Requirements 和 Acceptance Criteria 仍为 TBD（`.trellis/tasks/07-19-knowledge-domain/task.json:4-6`，`.trellis/tasks/07-19-knowledge-domain/prd.md:3-13`）。
- 项目后端规范明确把产品不变量和验收指向 `docs/product/PRD.md`，领域术语指向 `docs/architecture/CONTEXT.md`，模块、API、数据库、安全和测试分别指向对应架构文档（`.trellis/spec/backend/index.md:45-55`）。因此本研究以 PRD + CONTEXT 为产品/术语权威，以 accepted ADR 和架构文档约束实现边界。
- 父任务将 M5-05 定义为 `internal/knowledge/**` + `migrations/**`，依赖 M3-03/M5-02，验收关键词为“五分类关系、条件冲突、证据可达、对称去重”，主要风险是形成“关系第二事实源”（`.trellis/tasks/07-16-product-delivery/implement.md:43-49`）。父设计进一步要求正式 Claim/Relation 有可达 Evidence、对称 Relation 规范化去重，并要求领域规则与数据库约束共同保护（`.trellis/tasks/07-16-product-delivery/design.md:91-110`）。

### 2. 统一领域术语

| 术语 | 权威含义 | 明确排除 |
|---|---|---|
| Topic | 用于组织知识的主题或概念 | 不是标签或目录（`docs/architecture/CONTEXT.md:47-49`） |
| Claim | 带适用条件、可被证据支持或反驳的最小知识主张 | 不是摘要、Chunk 或观点标签（`docs/architecture/CONTEXT.md:51-53`） |
| Relation | 两个知识节点之间带明确类型的关系 | 不是向量相似或图谱边候选（`docs/architecture/CONTEXT.md:55-57`） |
| Relation Evidence | 支撑 Relation 的来源、理由、适用条件和确认信息 | 不是相似度分数（`docs/architecture/CONTEXT.md:59-61`） |
| Conflict | 两个或多个 Claim 在相同或相近适用条件下无法同时成立的持续性知识对象 | 不是错误提示或重复（`docs/architecture/CONTEXT.md:63-65`） |
| Provenance | 来源链路，包括原始资料、位置、版本和形成过程 | 不是单个 URL 或备注（`docs/architecture/CONTEXT.md:67-69`） |

- Chunk 是确定性内容片段而非知识事实，Source Span 是不可变原始内容范围；不能用 Chunk 代替 Claim，也不能用当前工作树文本代替稳定证据（`docs/architecture/CONTEXT.md:37-43`）。
- 正式知识必须经过用户批准、写入正式文件、产生版本记录并通过验证；候选 Claim/Relation 在完成批准和写回前不是正式知识（`docs/architecture/CONTEXT.md:137-149`）。
- PostgreSQL 保存 Topic/Claim/Relation/Conflict 等领域状态，但 Markdown + Git 才是正式文章内容事实源；数据库不能成为正式文章唯一原件（`docs/architecture/data-architecture.md:41-50`，`docs/architecture/adr/0003-markdown-git-source-of-truth.md:5-18`）。

### 3. 聚合边界和模块所有权

- Knowledge Context 拥有 Topic、Claim、Relation、Conflict 和 Provenance；Graph & Health 只拥有关系视图、候选关联、Health Issue 和影响分析（`docs/architecture/domain-model.md:29-40`）。
- Knowledge Aggregate 的根按命令目标可以是 Topic、Claim 或 Conflict；内部实体包括 Claim Source、Relation、Relation Evidence、Conflict Member（`docs/architecture/domain-model.md:94-109`）。
- Knowledge Module 的公开能力是 `ConfirmClaim`、`SuggestRelation`、`ConfirmRelation`、`OpenConflict`、`ResolveConflict`，并隐藏 Claim applicability、关系不变量、冲突状态和 Provenance 校验（`docs/architecture/module-architecture.md:113-128`）。
- Knowledge Module 控制 Claim/Relation/Conflict 的事务；HTTP Handler 不得开启跨模块事务，跨模块后续处理通过 Outbox/Event 最终一致完成（`docs/architecture/module-architecture.md:364-375`）。
- Domain Module 只能依赖 shared kernel，禁止依赖 HTTP、pgx/sqlc、模型 SDK 或其他模块内部包（`docs/architecture/module-architecture.md:282-301`）。正式 Relation 始终归 Knowledge Module 管理，即使将来更换 Graph DB 也只能替换 Graph Query Adapter（`docs/architecture/module-architecture.md:394-401`）。

### 4. 核心实体数据契约

#### Topic

- 产品字段：`id/name/aliases/description/status`（`docs/product/PRD.md:3246-3255`）。
- 数据库设计补充 `workspace_id/normalized_name`，并把 aliases 指定为 JSONB，但未列 description（`docs/architecture/database-design.md:232-242`）。
- 用户可手工创建、合并同义 Topic、设置别名、父子或前置关系；删除 Topic 不得级联删除 Document/Claim，必须先重分类或明确成为无 Topic（`docs/product/PRD.md:2796-2802`）。Topic 重命名必须检查别名和同名 Topic，且通过 Proposal（`docs/product/PRD.md:2752-2757`）。

#### Claim 与 Claim Source

- Claim 产品字段：`id/statement/normalized_statement/applicability/status/confidence`；Claim Source 必须绑定 `claim_id/source_span_id/source_version_id/support_type`（`docs/product/PRD.md:3256-3270`）。
- 数据库设计补充 `workspace_id/version`，将 confidence 改为 `confidence_factors JSONB`（`docs/architecture/database-design.md:243-252`）。
- Confirmed Claim 必须有有效 Provenance（`docs/architecture/domain-model.md:105-109`）。同一 statement 在不同 applicability 下不能直接判冲突，必须先比较适用条件（`docs/architecture/domain-model.md:374-376`）。
- Claim 过长或包含多个结论时必须拆分后重分析；Source 引用无法精确定位时禁止创建 Proposal（`docs/product/PRD.md:1250-1255`）。

#### Relation 与 Relation Evidence

- 产品 Relation 字段：两端 node type/id、relation type、status、confidence、valid_from/valid_to；Relation Evidence 绑定 `relation_id/source_span_id/source_version_id/reason/evidence_hash/model_version/confirmed_by`（`docs/product/PRD.md:3272-3293`）。
- 数据库设计补充 `workspace_id/version`，将 `model_version` 表达为 `model_run_ref`，但只列 `source_span_ref` 而未明确 `source_version_id`（`docs/architecture/database-design.md:254-277`）。
- 正式 Relation 必须有证据且经过确认（`docs/product/PRD.md:4179-4193`）；Confirmed Relation 必须同时有证据和确认方式（`docs/architecture/domain-model.md:105-109`）。
- 对称关系 DUPLICATES、CONFLICTS_WITH 必须按稳定 ID 排序并阻止反向重复（`docs/architecture/database-design.md:278-281`）。多态节点类型必须来自受控注册表，Knowledge Module 在同一事务内验证两端存在、同 Workspace、生命周期允许、类型组合兼容、自环禁止和证据要求（`docs/architecture/database-design.md:282-287`）。
- 目标对象不物理删除；归档、替代或失效通过生命周期状态表达。目标失效后 Relation 保留历史、生成 Health Issue，不能继续成为无来源的有效边（`docs/architecture/database-design.md:286-287`）。

#### Conflict 与 Conflict Member

- Conflict 字段：`id/topic_id/status/severity/summary/resolution/created_at/resolved_at`；成员绑定 `conflict_id/claim_id/applicability/position_summary`（`docs/product/PRD.md:3295-3311`）。数据库设计补充 `workspace_id/version`，topic_id 可空（`docs/architecture/database-design.md:290-306`）。
- Conflict 是 2..N Claim 的持续对象，输入还必须包含 Source Spans、Applicability、Severity 和 Affected Objects（`docs/architecture/workflows/09-conflict-resolution.md:14-20`）。
- Conflict 不能通过覆盖 Claim 静默消失（`docs/architecture/domain-model.md:105-109`）；调查记录追加 Query、Tool Calls、New Evidence、Analyst Note、Model Version，不覆盖旧记录（`docs/architecture/workflows/09-conflict-resolution.md:41-49`）。

### 5. 状态机与业务不变量

#### Claim 状态

- `SUGGESTED`：AI 抽取候选；`CONFIRMED`：来自批准知识且证据有效；`DISPUTED`：参与未解决 Conflict；`SUPERSEDED`：被更准确 Claim 替代；`DEPRECATED`：不再适用但保留历史；`INVALID`：证据失效或确认错误（`docs/product/PRD.md:578-585`）。
- 明示转移为 Suggested→Confirmed/Invalid，Confirmed→Disputed/Superseded/Deprecated，Disputed→Confirmed/Superseded（`docs/architecture/domain-model.md:282-294`）。

#### Relation 状态

- `SUGGESTED`、`CONFIRMED`、`REJECTED`、`STALE`、`DEPRECATED`；CONFIRMED 来自用户批准或明确 Markdown 链接，STALE 表示证据 Revision 变化需重新验证（`docs/product/PRD.md:539-545`）。
- 向量相似度不得直接创建 Confirmed Relation，AI confidence 也不等于用户确认（`docs/architecture/data-architecture.md:260-266`，`docs/architecture/domain-model.md:390-395`）。

#### Conflict 状态

- `OPEN`、`INVESTIGATING`、`RESOLUTION_PROPOSED`、`RESOLVED`、`ACCEPTED_DIVERGENCE`、`DEFERRED`（`docs/product/PRD.md:587-594`）。
- 明示转移为 Open→Investigating→ResolutionProposed→Resolved/AcceptedDivergence，Investigating↔Deferred（`docs/architecture/domain-model.md:296-307`）。
- Evidence 缺失时不能进入 Resolution Ready；目标版本变化必须重新调查；网页失败保持 Investigating（`docs/architecture/workflows/09-conflict-resolution.md:79-83`）。

### 6. “五分类关系分析”与“正式 Relation 类型”必须分层

- 关系分析输出五类：`NEW`、`COMPLEMENTARY`、`DUPLICATE`、`CONFLICT`、`LOW_CONFIDENCE`。NEW 表示未检索到等价/直接相关 Claim；LOW_CONFIDENCE 表示证据、上下文或分类间隔不足；它们是分析结果，不天然是正式图谱边（`docs/product/PRD.md:1158-1205`）。
- 正式 Relation 类型是 `CITES/DERIVED_FROM/BELONGS_TO/SUPPORTS/COMPLEMENTS/DUPLICATES/CONFLICTS_WITH/PREREQUISITE_OF/VERSION_OF/IMPACTS`（`docs/product/PRD.md:1720-1731`，`docs/architecture/domain-model.md:245-258`）。
- 推荐的契约解释（由文档事实推导，仍需任务 PRD 明确）：
  - NEW：创建新知识 Proposal，不创建 Relation；
  - COMPLEMENTARY：可建议 `COMPLEMENTS` 或补充 Proposal；
  - DUPLICATE：可建议 `DUPLICATES`/合并 Proposal；
  - CONFLICT：必须创建/补充 Conflict，可能同时建议 `CONFLICTS_WITH`；
  - LOW_CONFIDENCE：进入人工处理，不创建正式 Relation。
  处理依据见 `docs/product/PRD.md:1160-1205`，但“分析分类到正式 Relation/Conflict 命令”的精确映射尚未被文档锁定。
- 每次分析必须比较主题、适用条件、结论、证据/时间和表达差异，输出类型、理由、引用和置信度，再由 Review Agent 验证证据，校验后才创建 Proposal 或 Human Task（`docs/product/PRD.md:1207-1221`）。
- 置信度必须综合检索相关性、证据数量、来源质量、Claim 完整度、双 Agent 一致性和 applicability；阈值变化必须版本化（`docs/product/PRD.md:1223-1234`）。

### 7. Evidence / Provenance 可达性与安全边界

- Source Span 不可变；Claim Source 和 Relation Evidence 必须额外保存 `source_version_id` 来选择具体导入 Provenance（`docs/product/PRD.md:3238-3244`）。这意味着 Evidence 的可达性最少要能证明 Workspace → Source Version → Content Artifact → Parse Projection → Source Span。
- Evidence API 必须验证完整绑定，从不可变 Artifact 读取并复核 hash、大小和 byte range，最多返回 4 KiB UTF-8 excerpt；不得读取当前工作树路径，跨 Workspace/错绑/不存在统一返回 Not Found（`docs/architecture/api-and-events.md:166-173`）。
- 安全文档进一步要求调用方不能提交文件路径；任何绑定或 hash/范围损坏都必须 fail closed，不能回退到工作树（`docs/architecture/security.md:107-121`）。Search/Evidence SQL 必须参数化并带 Workspace 约束，distance operator 只能由白名单选择固定模板（`docs/architecture/security.md:147-156`）。
- 模型只接收最小证据窗口，本地敏感 Topic 可禁止云模型，Full Prompt Debug 默认关闭（`docs/architecture/security.md:165-170`）。
- 正式知识/关系写入的唯一 seam 是 Proposal + Approval；AI 对关系、Topic 和版本的正式改变必须有 Evidence、Diff、目标基线和回滚计划（`docs/architecture/adr/0008-proposal-approval-write-seam.md:5-12`）。

### 8. 主要用户流程

#### Claim 抽取与关系分析

1. 输入 Source Version、Document、Chunk、Workspace 现有 Topic/Claim/Relation 和用户范围（`docs/product/PRD.md:1140-1146`）。
2. 抽取 Topic/Claim/术语/别名/前置知识/来源位置（`docs/product/PRD.md:1148-1156`）。
3. 混合检索候选、Rerank、比较 applicability/结论/证据，输出五分类与证据（`docs/product/PRD.md:1207-1219`）。
4. Review Agent 校验证据，之后创建 Proposal 或 Human Task；低置信度不得自动进入正式知识（`docs/product/PRD.md:1220-1221,1257-1262`）。

#### Relation 候选确认

1. 从标题/别名/术语/Claim 相似/共同 Topic/共同 Source/RAG 共现生成候选（`docs/product/PRD.md:1853-1860`）。
2. 排除已有正式关系和证据未变化的已拒绝候选，保存 Candidate Fingerprint（`docs/product/PRD.md:1862-1869`）。
3. 用户确认、改类型后确认、忽略或标记误报；确认前不得进入正式图谱（`docs/product/PRD.md:1871-1889,1904-1909`）。
4. 修改操作创建独立 Relation Proposal；图谱操作不能绕过 Proposal（`docs/product/PRD.md:1818-1819,1834-1839,1897-1902`）。

#### Conflict 调查与解决

1. Relation Analysis、RAG、用户标记或 Health Scan 打开 Conflict（`docs/architecture/workflows/09-conflict-resolution.md:7-20`）。
2. 比较 Claims/conditions/sources，追加调查和 Evidence；旧调查记录不可覆盖（`docs/architecture/workflows/09-conflict-resolution.md:22-49`）。
3. 解决方式：选一个 Claim 并废弃另一个、不同条件下 Accepted Divergence、综合成新 Claim 并 supersede 旧 Claim、证据不足则保持 Open/Deferred（`docs/product/PRD.md:2859-2864`）。
4. 形成 Resolution Proposal，经 Approval + Writeback 后触发影响分析；下游只生成报告/Proposal，不自动级联改写 Graph/RAG Eval/Artifact/Review Card/Health（`docs/architecture/workflows/09-conflict-resolution.md:29-38,69-77`）。

#### Topic 生命周期

1. Create/Alias/Merge/Parent/Prerequisite/Delete 前重分类（`docs/architecture/workflows/10-knowledge-lifecycle.md:62-68`）。
2. 重命名检查同名和别名，所有正式生命周期变化通过 Proposal，并从 Git/Timeline 追溯（`docs/product/PRD.md:2743-2757,2804-2809`）。

### 9. 持久化与一致性要求

- PostgreSQL 负责非空、基础类型/枚举、外键、唯一键、版本字段、同 Workspace 关联；Knowledge Module + 同模块事务负责 Claim/Relation/Conflict 语义和状态机（`docs/architecture/database-design.md:770-785`）。
- Relation Confirm 与 Knowledge Event 在同一数据库事务内（`docs/architecture/database-design.md:753-761`）。可变聚合使用 version 乐观锁（`docs/architecture/database-design.md:763-769`）。
- Relation 两端不存在、跨 Workspace、类型组合非法、自环或对称反向重复时不能成为有效关系（`docs/architecture/database-design.md:824-832`）。
- Topic/Claim 可从 Provenance 部分重建，但确认历史必须备份；Confirmed Relation 依赖 Approval/Audit，不能由向量重建（`docs/architecture/data-architecture.md:81-95`）。正式 v1.0 不引入图数据库，以免正式 Relation 双写；PostgreSQL Relation 表是正式关系存储，Graph 仅为查询投影（`docs/architecture/adr/0005-no-graph-database-v1.md:5-12`）。
- 导出至少包含 Topic/Claim/Relation JSONL 与 schema_version/Workspace ID（`docs/architecture/data-architecture.md:248-258`）。当前导出清单没有 Conflict，见缺口部分。

### 10. 验收与测试契约

- 五类关系分析均要有固定样本；每个判断有双方证据；Conflict 不得误作覆盖/合并；LOW_CONFIDENCE 不自动进入正式知识（`docs/product/PRD.md:1257-1262`）。
- Relation Evaluation 必须覆盖五分类 Precision/Recall/F1、Conflict→Duplicate 高风险错误、Evidence Support 和 Low Confidence Appropriateness（`docs/architecture/testing-and-evaluation.md:246-251`）；PRD 还要求单独统计 Conflict→Duplicate 高风险误判和证据支持率（`docs/product/PRD.md:3838-3854`）。
- 单元测试至少覆盖状态机和 Relation Applicability（`docs/architecture/testing-and-evaluation.md:27-37`）。PostgreSQL 测试需覆盖迁移、唯一/并发约束和 Explain（`docs/architecture/testing-and-evaluation.md:50-60`）。
- Graph 验收包括 Relation Accuracy、Candidate Precision@K/Recall、Ignored Candidate Reappearance、Path Correctness（`docs/architecture/testing-and-evaluation.md:262-268`）；正式关系必须可打开证据，AI 候选与正式关系可区分（`docs/product/PRD.md:1834-1839`）。
- Conflict 验收：不可被普通覆盖消失、条件差异可表达、解决有 Proposal、下游影响可追踪（`docs/architecture/workflows/09-conflict-resolution.md:85-90`）；产品验收还要求保留调查历史和触发影响分析（`docs/product/PRD.md:2888-2893`）。
- 数据库验收必须为所有外键/唯一约束提供故障测试，并验证 Relation 跨 Workspace、非法组合、自环、对称反向重复均被拒绝（`docs/architecture/database-design.md:824-835`）。
- 容量基线为 100,000 Claim、500,000 Relation；局部图谱一跳 P95 ≤ 1.5 秒，且 500,000 Relation 局部图谱属于性能测试（`docs/product/PRD.md:3650-3669,4047-4054`）。避免 N+1 Relation Evidence 和图谱逐节点查询（`docs/architecture/performance.md:46-73`）。
- 顶层产品验收对应 AC-10、AC-18、AC-19、AC-22、AC-35、AC-36；最终演示必须展示互补/冲突判断、Proposal 审批、图谱新关系/冲突状态以及 RAG 引用/冲突说明（`docs/product/PRD.md:4451-4504`）。

### 11. 关键冲突与未决缺口

#### P0：实施前必须裁决

1. **分析分类与正式 Relation 类型同名但不是同一枚举。** PRD 10.6 把 NEW/COMPLEMENTARY/DUPLICATE/CONFLICT/LOW_CONFIDENCE 称为“关系类型”，PRD 10.12 又定义十种正式 Relation 类型（`docs/product/PRD.md:1158-1205,1720-1731`）。若共用一个 enum，会把 NEW/LOW_CONFIDENCE 错写成图谱边，或让 COMPLEMENTARY/DUPLICATE/CONFLICT 与 COMPLEMENTS/DUPLICATES/CONFLICTS_WITH 混用。需要独立的 Analysis Outcome/Classification 与 RelationType，并定义映射和何时创建 Conflict。
2. **Relation 两端的权威范围冲突。** 领域类图写成 Relation 恰好连接两个 Claim（`docs/architecture/domain-model.md:227-235`），但 PRD/数据库使用多态 node type/id，允许 Topic、Claim、Document、Article Revision 等（`docs/product/PRD.md:3272-3283`，`docs/architecture/database-design.md:282-286`）。必须明确：领域 Relation 是否为通用多态边，还是 Claim Relation 与 Graph Projection Link 两类对象；否则会形成第二事实源。
3. **缺少 RelationType × source/target node type 兼容矩阵。** 数据库设计要求模块验证类型组合，但没有列出每种类型允许的端点组合（`docs/architecture/database-design.md:284-286,831`）。例如 BELONGS_TO 的 target 必须是 Topic，但 CITES、DERIVED_FROM、VERSION_OF 的合法端点没有完整契约。
4. **Claim Source / Topic Claim 持久化明细缺失。** ER 图声明 `CLAIM_SOURCE` 和 `TOPIC_CLAIM`（`docs/architecture/database-design.md:20-35`），但数据库字段章节没有定义这两张关联表、唯一键、Workspace 同域约束、support_type 枚举、版本/删除策略。Claim 的 Provenance 可达性无法仅靠当前数据库文档实现。
5. **Evidence 引用字段不一致。** PRD 要求 Claim Source/Relation Evidence 同时保存 source_span_id + source_version_id（`docs/product/PRD.md:3240-3244,3265-3270,3285-3293`）；数据库 Relation Evidence 只写 source_span_ref（`docs/architecture/database-design.md:268-276`），且未描述 Claim Source。需明确 source_span_ref 是否复合引用；否则无法选择具体导入 Provenance。

#### P1：需要在任务 PRD/design 锁定

6. **Evidence 不是统一实体。** 权威术语只有 Claim Source 与 Relation Evidence，没有独立通用 Evidence 聚合；Search Evidence Item 也是检索 read model。任务标题中的 “Evidence” 应明确是共享 immutable provenance value/reference、RelationEvidence 实体，还是新增领域实体，避免把检索结果、Proposal evidence 和关系证据混为一表。
7. **Topic 字段与唯一性未闭合。** PRD 有 description，数据库设计没有；数据库有 normalized_name，PRD 没有；Topic 重命名要求检查同名/别名，但没有定义 Workspace 内 normalized name/alias 的冲突规则、大小写/Unicode 规范、Merge 后 alias 保留和状态枚举（`docs/product/PRD.md:3248-3255,2752-2757`，`docs/architecture/database-design.md:234-242`）。
8. **Claim confidence 事实源不清。** PRD 是 scalar confidence，数据库是 confidence_factors JSONB（`docs/product/PRD.md:3256-3264`，`docs/architecture/database-design.md:243-252`）。需要定义 scalar 是否为 factors 的版本化派生值，以及 CONFIRMED 是否允许仅凭模型 confidence。
9. **Relation confirmation contract 不完整。** Relation 状态允许明确 Markdown 链接直接成为 CONFIRMED（`docs/product/PRD.md:539-545`），而 ADR-0008 要求 AI 的正式改变走 Proposal（`docs/architecture/adr/0008-proposal-approval-write-seam.md:5-12`）。需要定义 `confirmed_by/confirmation_method` 枚举、显式链接的 source-derived 例外、用户审批绑定以及 STALE 后重确认流程。
10. **Relation 状态转移未画出完整状态机。** 文档只有状态列表，没有 SUGGESTED→CONFIRMED/REJECTED、CONFIRMED→STALE/DEPRECATED、STALE→CONFIRMED/DEPRECATED 等合法命令、版本检查和幂等契约（`docs/product/PRD.md:539-545`）。
11. **Conflict 与 Claim DISPUTED/Relation CONFLICTS_WITH 的原子一致性未定义。** 文档要求创建 Conflict、保留 Claim、可能调整关系状态，但未明确 OpenConflict 是否同事务把成员 Claim 标为 DISPUTED、是否必须创建/确认 CONFLICTS_WITH、Accepted Divergence 后如何撤销/保留历史边。
12. **Conflict 去重规则缺失。** 没有定义同一 Claim 集合 + applicability 的稳定 fingerprint/唯一性；Relation 对称去重有规则，但 Conflict 重复打开可能产生多个持续对象。
13. **Conflict 调查记录缺少数据模型。** 流程要求 append-only Query/Tool Calls/New Evidence/Analyst Note/Model Version（`docs/architecture/workflows/09-conflict-resolution.md:41-49`），PRD/数据库字段仅有 Conflict 和 Conflict Member，没有 Investigation/Resolution Revision 表、版本绑定或审计引用。
14. **Topic 层级/前置关系建模不明确。** 产品允许父子或前置关系（`docs/product/PRD.md:2796-2802`）；正式 Relation 已有 BELONGS_TO 和 PREREQUISITE_OF，但没有说明 Topic parent 是否使用哪一种 RelationType、是否允许环、Merge 时如何重定向。
15. **删除/失效的跨对象规则未细化。** 文档要求目标不物理删除、Relation 保留历史并生成 Health Issue（`docs/architecture/database-design.md:286-287`），但 Topic/Claim/Conflict 各自的 soft-delete/status、外键 on-delete、Evidence 保留期限和重分类事务未锁定。

#### P2：验收与接口覆盖缺口

16. **OpenAPI 尚无 Knowledge 写命令和详细 Graph wire 契约。** API 文档只列 graph global/neighborhood/path/relation details/candidates（`docs/architecture/api-and-events.md:194-200`），当前 OpenAPI 搜索未发现 Topic/Claim/Relation/Conflict 资源或命令 Schema。若 M5-05 只交付领域+Repository，应明确 API 非范围；否则需补稳定错误、ETag/Version、Idempotency-Key 和 Problem 映射。
17. **测试文档缺少 Topic/Claim/Conflict Repository 的明确集成矩阵。** 已有 Relation Applicability、AI Relation Evaluation 和通用 DB 约束要求，但没有逐聚合列出 create/replay/version conflict/cross-workspace/evidence damage/illegal transition/Conflict duplicate 的具体场景。
18. **Conflict 未包含在领域元数据导出清单。** 数据导出列 Topic/Claim/Relation JSONL，但未列 Conflict/Conflict Member/调查历史（`docs/architecture/data-architecture.md:248-258`）。这与 Conflict 必须持续跟踪、保留历史以及数据库需备份领域状态的要求存在恢复覆盖缺口。
19. **性能目标缺少领域写入和证据加载预算。** 产品有 500k Relation 局部图谱 P95 与禁止 N+1，但未给 ConfirmRelation/OpenConflict 批量/并发吞吐、Relation Evidence 延迟加载分页和 Conflict member 上限。

### 12. 建议写入任务 PRD 的最小验收合同

以下是从现有权威文档归纳出的最小可执行验收，不是新增产品范围：

1. 五分类 Analysis Outcome 与正式 RelationType 使用不同类型；NEW/LOW_CONFIDENCE 永不落为正式 Relation。
2. ConfirmClaim 在同一 Workspace 内验证 Claim Source → Source Version → Source Span → Content Artifact 可达，损坏或错绑 fail closed。
3. ConfirmRelation 验证端点存在/同 Workspace/类型兼容/非自环/证据可达/确认方式有效；对称关系反向重放只得到同一 Relation。
4. Relation 的 SUGGESTED/CONFIRMED/REJECTED/STALE/DEPRECATED 转移、乐观锁和幂等行为有纯领域测试与 PostgreSQL 集成测试。
5. OpenConflict 至少接受 2 个不同 Claim，比较 applicability，保留成员来源；同条件冲突不可被普通覆盖/合并消除。
6. Accepted Divergence 必须补齐不同 applicability；ResolveConflict 必须形成 Proposal/历史记录，并只发布下游影响事件/报告，不直接改写下游对象。
7. Topic name/alias 规范化、同名冲突、Merge alias 保留、Delete 前重分类和层级防环规则可验证。
8. 所有领域写命令带 Workspace、Idempotency Key/命令身份和 expected version；跨 Workspace、非法状态、证据错绑、对称重复均有稳定领域错误。
9. Repository 约束测试覆盖外键/唯一/CHECK/乐观锁/同 Workspace；单元测试覆盖状态机、applicability 和类型兼容矩阵。
10. Graph/Agent 只能消费 Knowledge Module 的 Confirmed read model/event，不直接写第二套 Relation 或把向量候选冒充正式关系。

## Files Found

- `.trellis/tasks/07-19-knowledge-domain/task.json` — 当前 M5-05 任务元数据与目标。
- `.trellis/tasks/07-19-knowledge-domain/prd.md` — 当前任务 PRD，需求和验收仍为 TBD。
- `.trellis/tasks/07-16-product-delivery/design.md` — 父任务领域模型及正式 Claim/Relation Evidence 不变量。
- `.trellis/tasks/07-16-product-delivery/implement.md` — M5-05 依赖、影响路径、验收关键词和下游依赖。
- `.trellis/spec/backend/index.md` — 后端规范入口及产品/领域/数据库/API/安全/测试事实源映射。
- `.trellis/spec/backend/database-guidelines.md` — 数据库约束、参数化查询、事务与迁移通用规范。
- `.trellis/spec/backend/quality-guidelines.md` — 后端验证和质量门禁通用规范。
- `.trellis/spec/guides/cross-layer-thinking-guide.md` — 跨层数据流、边界契约和单一解码/事实源要求。
- `docs/product/PRD.md` — 产品术语、状态、字段、用户流程、性能和最终验收的主要权威来源。
- `docs/architecture/CONTEXT.md` — 统一领域语言及明确反例。
- `docs/architecture/domain-model.md` — 聚合、关系语义、状态机、命令和边缘场景。
- `docs/architecture/module-architecture.md` — Knowledge/Graph 模块职责、依赖、事务 seam。
- `docs/architecture/data-architecture.md` — 数据所有权、事实源、恢复、导出和不变量。
- `docs/architecture/database-design.md` — 逻辑实体、字段、Relation 多态引用、约束责任和数据库验收。
- `docs/architecture/api-and-events.md` — Evidence 可打开资源、Graph 查询概览、命令/错误/版本通用契约。
- `docs/architecture/security.md` — Workspace 隔离、Evidence fail-closed、参数化 SQL、模型最小披露。
- `docs/architecture/testing-and-evaluation.md` — Relation/RAG/Graph 评测与测试金字塔。
- `docs/architecture/performance.md` — Relation Evidence/图查询性能和 N+1 禁止项。
- `docs/architecture/requirements-traceability.md` — PRD 10.6/10.12/10.24 到架构与流程文档的追踪。
- `docs/architecture/workflows/09-conflict-resolution.md` — Conflict 创建、调查、解决、下游和验收。
- `docs/architecture/workflows/10-knowledge-lifecycle.md` — Topic 生命周期、并发和验收。
- `docs/architecture/adr/0003-markdown-git-source-of-truth.md` — accepted：Markdown/Git 为正式文章事实源。
- `docs/architecture/adr/0004-postgresql-pgvector.md` — accepted：PostgreSQL + pgvector 统一关系/检索/事务数据。
- `docs/architecture/adr/0005-no-graph-database-v1.md` — accepted：v1 不引入图数据库，防止 Relation 双写。
- `docs/architecture/adr/0008-proposal-approval-write-seam.md` — accepted：正式知识/关系/Topic 写入必须通过 Proposal/Approval。
- `docs/architecture/adr/0011-retrieve-latest-approved-revision.md` — accepted：默认关系分析只用最新批准版本。
- `docs/architecture/adr/0012-version-workflows-prompts-schemas.md` — accepted：模型输出相关 Definition/Prompt/Schema 必须版本化。
- `docs/architecture/adr/0013-eino-adoption-gate.md` — accepted：模型框架输出仍须经过 Evidence、Permission 和领域不变量校验。

## Code Patterns

- 当前仓库未发现 `internal/knowledge/**`，也未发现 Topic/Claim/Relation/Conflict 领域迁移；父任务将这些路径明确列为 M5-05 待实现范围（`.trellis/tasks/07-16-product-delivery/implement.md:47`）。
- 现有可复用边界是 Retrieval 的不可变 Evidence Reference：公开 Source Version/Span 由 Workspace 绑定、不可变 Artifact 读取和统一错误语义保护；M5-05 的 Claim Source/Relation Evidence 应复用这一证据身份链，而不是读取文件路径（`docs/architecture/api-and-events.md:166-173`，`docs/architecture/security.md:119-121`）。
- 项目既有领域架构要求 public Interface + hidden implementation，领域类型不泄露 pgx/sqlc/HTTP/模型 SDK（`docs/architecture/module-architecture.md:15-21,282-301`）。
- 对称 Relation 的推荐持久化模式是写入前按稳定 ID 规范化，并由唯一索引兜底；语义验证仍属于 Knowledge Module（`docs/architecture/database-design.md:278-287,747-759`）。

## External References

- 本研究为 internal scope；未使用外部文档或网络资料。
- 相关技术选择均由仓库内 accepted ADR 锁定，未引入外部版本假设。

## Related Specs

- `.trellis/spec/backend/index.md:15-32` — 开发前必须读取任务与架构、搜索既有术语、保持单一事实源、领域不依赖基础设施。
- `.trellis/spec/backend/database-guidelines.md` — M5-05 迁移、参数化 SQL、事务、约束和集成测试应遵循的数据库规范。
- `.trellis/spec/backend/quality-guidelines.md` — 状态机、边界、错误和集成验证门禁。
- `.trellis/spec/guides/cross-layer-thinking-guide.md:19-51` — 必须定义 Domain↔Repository↔API/Graph/Agent 的输入、输出、错误和验证责任。

## Caveats / Not Found

- 当前任务 `prd.md` 尚未把上述权威需求转成任务级 Requirements/Acceptance Criteria；本文件只能提供研究证据，不能替代规划裁决。
- 未发现 `internal/knowledge/**` 或对应数据库迁移，故无法核对真实代码/Schema 与文档的一致性。
- 未发现 Topic/Claim/Relation/Conflict 的 OpenAPI wire Schema；当前 API 文档只有 Graph 查询能力概览。
- 未找到 RelationType × NodeType 完整兼容矩阵、Topic 状态机、Conflict fingerprint/调查记录模型、Relation 完整状态转移、Claim Source/Topic Claim 表结构。
- “Evidence” 在不同上下文中至少指 Relation Evidence、Claim Source、Search Evidence Item 和 Proposal evidence；权威文档没有统一通用 Evidence 实体，实施前必须明确任务范围。
