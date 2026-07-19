# M5-05 Knowledge Domain

## Goal

实现 Topic、Claim、Claim Source、Relation、Relation Evidence、Conflict 的稳定领域模型、同步 Application 边界和 PostgreSQL 持久化约束，为 M6-02 Agent 的结构化关系判断、引用校验与拒答，以及 M7 Graph/Collection 的事实查询提供唯一、可验证、可扩展的知识事实边界。

## Background And Confirmed Facts

- Knowledge Context 拥有 Topic、Claim、Relation、Conflict 与 Provenance；Graph/Health 只能消费这些事实形成查询投影，不能维护第二套正式 Relation。
- 关系分析五分类 `NEW | COMPLEMENTARY | DUPLICATE | CONFLICT | LOW_CONFIDENCE` 与正式 `RelationType` 是不同概念。`NEW`、`LOW_CONFIDENCE` 不能落成 Relation。
- Source Version、Parse Projection 和 Source Span 已由 Ingestion 提供不可变 Provenance；M6-D 已证明 Workspace → Source Version → Content Artifact → Projection → Span 的 fail-closed 打开链路。
- Claim Source、Relation Evidence、Search Evidence、Proposal Evidence 的语义不同。本任务不新增“万能 Evidence 聚合”或通用 Evidence 表。
- 正式关系存储为 PostgreSQL Relation；`BELONGS_TO` 是 Topic–Claim 正式归属的唯一写入事实，不再新增可独立写入的 `topic_claim` 第二事实源。
- 当前 Change Control Proposal 是文件补丁模型，批准后固定进入 Safe Writeback/Git/Reindex。它不能通过假路径、假 Hash 或假正文冒充知识关系 Proposal；typed Knowledge Proposal 归 M7-02 扩展。
- 下一份迁移为 `00017_knowledge_domain.sql`。现有迁移测试仍把 `00016` 当最新版本，必须同步更新 Down 顺序和 guarded Down 验证。
- 当前不存在 `internal/knowledge`、Knowledge HTTP/OpenAPI、Graph/Agent/Collection 实现或共享数据库 testkit。

## Requirements

### R1. Unified Language And Module Boundary

- 新增精确术语 `Relation Assessment`，表示一次分析产生的五分类结果；它不是正式 Relation，也不是数据库边。
- Domain 只依赖 `internal/foundation` 和 Go 标准库；不得依赖 HTTP、pgx、River、Retrieval DTO、模型 SDK、文件系统或其他模块内部实现。
- Application 通过小型 Port 编排 Provenance 可达性、确认方式和事务命令；PostgreSQL Adapter 隐藏 SQL、事务和数据库错误。
- 本任务只交付 Domain/Application/PostgreSQL 与迁移，不新增公共 HTTP、OpenAPI、Workflow Node、Agent、Graph、Collection 或 UI 空壳。

### R2. Topic Contract

- Topic 包含 Workspace、名称、规范化名称、别名、描述、状态、版本和时间。
- Topic 状态冻结为 `ACTIVE | MERGED | DEPRECATED`。`MERGED` 必须记录目标 Topic；`DEPRECATED` 保留历史且不得物理删除。
- 名称和别名使用 NFC、Unicode case-fold、首尾空白清理和内部空白折叠生成稳定规范值；同一 Workspace 内任一活动/历史 Topic 的名称或别名不得互相冲突。
- 本任务实现创建与读取所需规则；完整 Merge 重定向、层级防环和生命周期 UI 留给 M7-04，但持久模型必须兼容 `merged_into_topic_id`。

### R3. Claim And Provenance Contract

- Claim 包含 statement、normalized statement、版本化 Applicability、Applicability Hash、状态、confidence score/factors、版本和时间。
- Claim 状态冻结为 `SUGGESTED | CONFIRMED | DISPUTED | SUPERSEDED | DEPRECATED | INVALID`，合法转移由单一领域状态机定义并由数据库最小镜像保护。
- Applicability v1 是最大 16 KiB 的 canonical JSON object；Domain 拒绝重复键、非法 UTF-8、非 object、非有限数字、过深嵌套和尾随 JSON，并计算稳定 SHA-256。
- Confirmed Claim 至少有一个 `SUPPORTS` Claim Source；仅有模型置信度、检索分数、Refutes 来源或无来源内容不能 Confirm。
- Claim Source 是不可变语义实体，包含 Workspace、Claim、Source Version、Source Span、`SUPPORTS | REFUTES`、理由、Evidence Hash、可选 model run reference 和时间。
- Claim Source 必须证明 Workspace → Source Version → Content Artifact → Source Version Projection → Source Span 完整绑定；错绑、跨 Workspace 或损坏必须 fail closed。
- 相同 statement + applicability 的重复命令必须幂等重放；不同 payload 使用同一幂等键或同一 fingerprint 必须返回稳定 Version Conflict，不静默覆盖。

### R4. Relation Contract

- 正式 RelationType 冻结为 `CITES | DERIVED_FROM | BELONGS_TO | SUPPORTS | COMPLEMENTS | DUPLICATES | CONFLICTS_WITH | PREREQUISITE_OF | VERSION_OF | IMPACTS`。
- M5-05 只注册当前真实存在的 `TOPIC`、`CLAIM` 两种 NodeType；未来 NodeType 必须在对象与查询契约真实落地后扩展，不预建空壳类型。
- 端点兼容矩阵冻结为：
  - `CITES`、`DERIVED_FROM`、`SUPPORTS`、`CONFLICTS_WITH`：Claim → Claim；
  - `BELONGS_TO`：Claim → Topic；
  - `COMPLEMENTS`、`DUPLICATES`、`PREREQUISITE_OF`、`VERSION_OF`：同类型 Topic → Topic 或 Claim → Claim；
  - `IMPACTS`：当前注册的 Topic/Claim 任意组合。
- Relation 必须验证端点存在、同 Workspace、生命周期允许、类型组合兼容、禁止自环。
- `DUPLICATES`、`CONFLICTS_WITH` 是对称关系，必须按 `(node_type,node_id)` 稳定排序；正反输入重放只能得到同一 Relation，数据库唯一约束防止并发反向重复。
- Relation 状态冻结为 `SUGGESTED | CONFIRMED | REJECTED | STALE | DEPRECATED`，合法转移、乐观锁和幂等规则由领域状态机定义。
- Confirmed Relation 至少有一个可达 Relation Evidence，并带 `USER_APPROVAL | SOURCE_DERIVED` 确认方式和非空确认引用；M5-05 只冻结验证 Port，不宣称现有文件 Proposal 已完成 Relation Approval。
- Relation Evidence 是不可变语义实体，包含 Source Version、Source Span、reason、Evidence Hash、Applicability、可选 model run reference 和确认信息；不能保存瞬时 Search rank/score、snippet 或当前工作树路径。

### R5. Relation Assessment Mapping

- `NEW`：只表示新知识候选，不创建 Relation。
- `COMPLEMENTARY`：可映射为 `COMPLEMENTS` Suggested Relation。
- `DUPLICATE`：可映射为 `DUPLICATES` Suggested Relation。
- `CONFLICT`：必须产生/补充 Conflict 命令，可同时建议 `CONFLICTS_WITH`，但不得直接 Confirm。
- `LOW_CONFIDENCE`：进入人工处理或拒答，不创建 Relation/Conflict 正式事实。
- 映射函数必须是纯领域函数，非法或缺字段输入返回稳定错误；M6-02 才负责模型输出、Schema Repair、阈值和评测。

### R6. Conflict Contract

- Conflict 是至少两个不同 Claim 的持续对象，包含 Workspace、可选 Topic、状态、严重度、摘要、Applicability Assessment、稳定 fingerprint、版本和时间。
- Conflict 状态冻结为 `OPEN | INVESTIGATING | RESOLUTION_PROPOSED | RESOLVED | ACCEPTED_DIVERGENCE | DEFERRED`。
- Applicability Assessment 只支持：
  - `EXACT`：所有成员 Applicability Hash 完全相同；
  - `REVIEWED_OVERLAP`：Hash 不完全相同，但必须保存非空人工/版本化分析理由。
- Domain 不根据 JSON 不相等自动推断“冲突”或“条件分歧”；不确定时应由 M6-02 输出 LOW_CONFIDENCE。
- OpenConflict 必须在同一 PostgreSQL 事务创建 Conflict、全部成员，并把可争议的 Confirmed Claim 标记为 DISPUTED；任何失败全部回滚。
- fingerprint 由 Workspace、排序后的 Claim ID 集合、Applicability Assessment/Hash 计算；相同未终结冲突幂等重放，不能重复打开。
- `ACCEPTED_DIVERGENCE` 必须有至少两个不同 Applicability Hash 和非空 resolution；普通 Claim 覆盖、Relation 变化或来源更新不能删除 Conflict。
- 完整调查记录、Resolution Proposal、影响分析和下游报告归 M7-04；持久模型预留不可变 resolution reference，不伪造尚不存在的工作流。

### R7. Persistence, Concurrency And Query Seam

- 新增 `core.topic`、`core.topic_alias`、`core.claim`、`core.claim_source`、`core.relation`、`core.relation_evidence`、`core.conflict`、`core.conflict_member` 和必要的 knowledge command receipt；不新增通用 Evidence 或可独立写入的 topic_claim。
- 所有表带 Workspace 作用域、显式状态/CHECK、RESTRICT 外键、必要的 `(id,workspace_id)` 复合唯一、version 和时间顺序约束。
- 可变聚合使用 `expected_version` CAS；所有写命令带 Workspace、幂等键、request hash。相同请求返回 replay，不同绑定返回 Version Conflict。
- Provenance、状态机、端点兼容、对称排序、Confirmed Evidence 和 Conflict member count 由 Domain + Transaction + PostgreSQL constraint/trigger 分层保护。
- 查询使用显式列和参数化 SQL；批量读取 Claims/Relations/Evidence 必须有最大上限、稳定排序，不提供无界列表或 N+1 API。
- 迁移 Up 可从空库和现有 00016 前向执行；空数据 Down 可执行，有任一 Knowledge 业务数据时以 SQLSTATE `55000` 拒绝破坏性降级。

### R8. Documentation And Downstream Compatibility

- 同步 `docs/architecture/CONTEXT.md`、领域模型、数据库设计、测试文档和 backend spec，记录 Relation Assessment、Applicability v1、NodeType 兼容矩阵、Evidence 语义分离及 BELONGS_TO 唯一事实源。
- M6-D Search/Evidence 现有 wire 和行为不得改变；本任务不返回 Topic/Claim/Relation/Conflict 空壳字段。
- M6-02、M7-01、M7-03、M8-02 必须只能通过 Knowledge Application/Repository 公开 seam 消费 Confirmed read model，不能直接写表或复制 Relation。

## Acceptance Criteria

- [x] `RelationAssessment` 与 `RelationType` 是不同 Go 类型；五分类映射测试证明 NEW/LOW_CONFIDENCE 永不创建 Relation。
- [x] Topic/Claim/Relation/Conflict 状态机、规范化、Applicability canonicalization、兼容矩阵、对称端点和 fingerprint 纯领域测试覆盖正常、边界与非法路径。
- [x] Confirmed Claim 缺少 SUPPORTS Claim Source、Confirmed Relation 缺少 Evidence/确认方式时，Domain、Application 和 PostgreSQL 均 fail closed。
- [x] Claim Source/Relation Evidence 的 Source Version + Source Span 完整绑定、跨 Workspace、错 Projection、损坏引用均通过真实 PostgreSQL 集成测试。
- [x] 对称 Relation 正向、反向和并发插入最终只有一个正式事实；自环、非法 NodeType/RelationType 组合和跨 Workspace 端点被拒绝。
- [x] OpenConflict 原子创建至少两个成员并将相关 Confirmed Claim 标为 DISPUTED；重复 fingerprint、少成员、不同 Workspace 和不合法 Applicability Assessment 被拒绝且无半事务。
- [x] Claim/Relation/Conflict 乐观锁、幂等重放、同键不同 payload、非法状态转移和 response-loss replay 有确定性测试。
- [x] `00017` 空库 Up、重复 Up、空数据 Down→Up、有业务数据 guarded Down、迁移版本 17 和旧迁移顺序回归全部通过。
- [x] 批量查询有固定上限、稳定排序和单批 Evidence 加载；测试或查询断言证明没有逐 Relation/Claim N+1。
- [x] Ingestion、Retrieval Search/Evidence、Change Control 和 Workflow 回归测试通过；Compose migrate 后 API/Worker readiness 保持正常。
- [x] 领域、数据库、测试和 backend spec 与实现同步，无占位项、第二事实源、假 Proposal、假 Evidence 或未说明的兼容性变化。
- [x] Go race、关键包 `-count=20`、全仓 integration、vet、`make test`、`go mod tidy -diff`、Docker/Compose smoke、主 Agent go-review/sql-code-review 和独立审查通过。

## Out Of Scope

- Knowledge 公共 HTTP/OpenAPI、前端页面、SSE 和权限 Middleware。
- Agent 模型调用、Prompt/Schema、Relation Candidate、Citation/Faithfulness、拒答、AI Eval；归 M6-02。
- Graph 邻居/路径/布局、Semantic Candidate、忽略 fingerprint、typed Relation Proposal；归 M7-01/M7-02。
- Smart Collection、Health Issue、Timeline、Impact Analysis、Conflict Investigation log；归 M7-03/M7-04。
- Artifact/Review/FSRS/Interview/Memory；归 M8。
- 正式认证、Capability、50 万 Relation P95、备份恢复产品化和 Audit；归 M10。
- 图数据库、通用 Evidence 表、独立 topic_claim 写入表、修改历史迁移或把现有文件 Proposal 伪装成知识 Proposal。
