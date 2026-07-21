# M7-02 Semantic Link Candidates

## Goal

把尚未显式建立 Relation、但有可追踪证据支持的 Topic/Claim 关联转化为可审阅的语义候选：用户可以查看双方摘要、证据、建议关系、理由、置信度和发现方式，并选择确认、修改关系类型后确认、忽略、标记误报或稍后处理。

候选确认只创建独立的 typed Relation Proposal；只有 Proposal 经过统一 Approval 并由 Knowledge 写入边界成功应用后，才产生正式 Relation。候选、模型输出和扫描失败均不得污染或阻塞已交付的正式 Graph 查询。

## Authoritative Sources And Confirmed Facts

- 产品范围与验收：`docs/product/PRD.md` 10.13、14.6、14.7、17.6、AC-19。
- 候选流程和 fingerprint：`docs/architecture/workflows/06-graph-and-semantic-links.md`。
- 唯一写入 seam：`docs/architecture/adr/0008-proposal-approval-write-seam.md`。
- 当前正式事实边界：`internal/knowledge/**`、`migrations/00017_knowledge_domain.sql`。
- 当前 Graph v1：`internal/graph/**`、`web/src/features/graph/**`，只投影 Topic/Claim 与正式 Relation。
- 当前 Change Control Proposal 是文件补丁专用模型；历史 Knowledge 任务明确将 typed Knowledge Proposal 的 Expand 迁移归入 M7-02，不能用假路径、假 Hash 或假正文冒充关系变更。
- 当前 Retrieval 可提供 lexical/semantic/hybrid 候选、Evidence 与冻结的 Index/Embedding/Rerank 版本；Agent `RelationAnalyzer` 可对已选 Claim/Evidence 做结构化关系判断，但当前没有候选生命周期、忽略记录、批量扫描或候选级评测。

冲突处理：最终产品叙述包含 Document/Topic/Claim 候选，但已落地 Relation 端点和 Graph v1 只注册 `TOPIC|CLAIM`。本任务以真实领域约束为安全边界，只交付 Topic/Claim；Document 必须等 Document 聚合与 Relation 端点契约真实落地后 additive 扩展，不能用路径或文本 ID 伪装。

## Requirements

### CAND-01 Independent candidate boundary

- `Semantic Link Candidate` 是 Graph 模块拥有的待审阅发现记录，不是 `core.relation`、`RelationAssessment`、Search Candidate 或 Proposal。
- Candidate 未经 Proposal/Approval 应用前不得写入 `core.relation`，不得进入 Global/Local/Path 正式图，也不得被 Knowledge Eligibility 当作正式证据。
- Graph 的七个既有只读端点保持无副作用；候选查询、扫描和决策使用独立 Application/HTTP seam，依赖缺失时单独 fail closed。
- Candidate 保存 Workspace、规范端点、端点版本快照、建议 Relation Type、证据引用/哈希、双方摘要/对应段落、理由、置信度、发现方式和生成版本；不复制完整来源正文、Credential、绝对路径或瞬时 Provider 请求。

### CAND-02 Discovery scope and sources

- 首版只发现当前 Workspace 内生命周期允许的 Topic/Claim 端点，禁止自环、跨 Workspace 和 Relation Type 不兼容组合。
- 必须支持文档明确的六类发现信号：标题/别名、术语、Claim 语义相似、共同 Topic、同 Source、RAG 共召回；每个候选明确记录实际命中的方式，未实现的信号不能静默标记为已执行。
- 发现流程先排除已有 `CONFIRMED|STALE` canonical Relation 和相同未终结 Relation Proposal，再对有界候选执行关系分类与 Evidence 校验。
- `NEW`、`LOW_CONFIDENCE` 不创建关系候选；`COMPLEMENTARY|DUPLICATE` 可映射关系类型，`CONFLICT` 只生成可审阅候选并保留 Conflict 语义，不直接选择胜者或确认关系。
- 单节点发现和批量扫描均有服务端硬上限，禁止逐节点数据库查询、逐候选远程调用和无分页全量返回。

### CAND-03 Stable fingerprint and deduplication

- Candidate fingerprint 使用版本化 canonical payload，至少绑定 Workspace、规范端点、双方 Node Version、建议 Relation Type、排序后的 Evidence semantic hash 集合、实际 Model/Rule Version。
- 实际参与发现/判断的 Index、Embedding、Rerank、Prompt、Schema 等版本必须冻结并参与生成版本绑定；未使用的可选版本保持缺失，不能填充“latest”或运行时默认值。
- 同一 Workspace 的相同 fingerprint 并发或重复生成最终只能得到同一 Candidate；同一请求重放返回持久结果，不创建重复记录。
- 忽略或误报的 fingerprint 在端点版本、证据和生成版本均未变化时不得再次作为 Active 推荐返回。
- fingerprint 变化后创建新的评估快照并关联上一候选；若上一候选为 Ignored/False Positive，新的 Active 候选必须带 `CONTENT_CHANGED` 重开原因，并向用户显示“因内容变化重新评估”。

### CAND-04 Candidate decisions

- 支持 `ACTIVE`、`DEFERRED`、`IGNORED`、`FALSE_POSITIVE`、`PROPOSAL_CREATED`、`SUPERSEDED` 受控状态，以及 Confirm、Confirm With Relation Type、Ignore、False Positive、Defer、Resume 的合法迁移。
- Ignore、False Positive 必须保存规范化非空原因；Defer 必须保存可选的恢复时间或明确的手工恢复语义。
- 决策命令必须带 `Idempotency-Key` 和 `expected_version`；精确重放返回同一结果，同键不同载荷、过期版本、非法状态迁移和跨 Workspace 均明确失败。
- 决策历史 append-only；Candidate 当前状态只是可验证投影，不能覆盖或删除既有理由、fingerprint 或 Proposal 绑定。
- 已创建 Proposal 的 Candidate 不能再次创建第二个 Proposal；Proposal 被拒绝不会把原 fingerprint 重新变成 Active，后续重新分析必须由新 fingerprint 触发。

### CAND-05 Typed Relation Proposal and formal write

- Change Control 以 Expand 方式新增 typed Proposal：历史 `file_patch` 行和 HTTP 行为保持兼容；新增 `knowledge_change` Relation Proposal 使用结构化 target refs、base versions、change set、evidence refs 和 schema version，禁止出现假文件字段。
- 每次 Candidate Confirm，包括批量确认中的每一项，都创建独立 Relation Proposal；选择的新 Relation Type 必须重新校验端点兼容并进入 Proposal change hash。
- Relation Proposal 的 change hash 必须绑定 Candidate fingerprint、端点/版本、Relation Type、Evidence refs、风险和回滚计划；Approval 继续绑定唯一 Revision 与 approved change hash。
- Relation Proposal Approval 不得错误派发文件 Safe Writeback；批准后由 typed Knowledge apply seam 幂等创建/确认唯一 canonical Relation，并使用 Approval ID 作为 `USER_APPROVAL` confirmation reference。
- Knowledge apply 必须重新检查端点版本、Evidence 可达性、已有 Relation/Proposal 和 Candidate 绑定；基线变化进入 `needs_revision`，不得覆盖或假成功。
- 正式 Relation 写入失败时 Proposal 保持可恢复的明确状态；Candidate 不得伪装为已正式确认。

### CAND-06 Asynchronous bounded scanning

- 用户可以按 Topic 发起首版扫描；目录和 Smart Collection scope 只有在其稳定对象/查询契约可用时 additive 扩展，首版不得用路径或任意 JSON 伪装已支持。
- 扫描通过现有 Workflow/River 运行，HTTP 返回 `202 + workflow_run_id + status_url`；`status_url` 必须指向带 Workspace 的 Candidate Scan 业务投影而非通用 Workflow URL，刷新后可恢复进度，River 不是业务事实源。
- Scan 保存稳定 scope、版本、fingerprint、总数/已处理/候选/忽略/失败计数和终态；相同 scope/version/idempotency key 精确重放。
- 扫描按有界页读取节点、批量检索/分类/持久化，支持取消、超时、失败重试和 response-loss replay；单个候选失败不得被记作成功，模型不可用不得降级成空候选。
- 批量确认只编排多个独立 Confirm 命令和独立 Proposal；一项失败不得把其他项伪装为同一 Proposal 或一个不可追踪的大变更。

### CAND-07 API and frontend experience

- OpenAPI 提供候选分页查询、单节点发现/重评、扫描创建/查询、Candidate 决策和 Relation Proposal 查询/审批所需的严格契约；所有未知字段、非法 enum/UUID/limit/cursor 均明确失败。
- 候选列表支持按状态、Relation Type、置信度和重开原因过滤/分组，使用稳定 cursor + limit；`CLAIM` scope 精确匹配 Candidate 端点，`TOPIC` scope 还包含直接 Topic Candidate 与双方都通过正式 `CONFIRMED BELONGS_TO` 归属该 Topic 的 Claim pair；跨 Workspace、不存在或不可见统一 Not Found。
- `/graph` 保留正式图查询，在独立候选面板中展示 Candidate 卡片；视觉上使用标签、线型/图标和文字区分候选与正式 Relation，不能只靠颜色。
- 卡片展示双方摘要、对应 Evidence 段落、建议关系、理由、置信度、发现方式、版本和重开原因；提供确认、改类型确认、忽略、误报、稍后处理和恢复动作。
- 所有 mutation 显示 pending、成功、冲突和可恢复错误；刷新后从服务端事实恢复，不能把乐观 UI 当作已创建 Proposal 或正式 Relation。
- 桌面和移动视口无重叠/横向溢出；键盘可打开候选、选择动作、填写原因、关闭对话框并恢复焦点。

### CAND-08 Security, failure isolation and performance

- 所有 SQL 参数化并绑定 Workspace；枚举、排序和 scope 使用白名单；日志不得记录 Claim 正文、Evidence reason/snippet、绝对路径、数据库 URL 或模型原始响应。
- 当前正式 Auth/Session/CSRF 仍归 M10，本任务保持 loopback 部署边界并预留 Capability，不把 `workspace_id` 当身份。
- Candidate/Model/Retrieval/Workflow 依赖不可用时返回稳定、可重试的独立错误；Graph Global/Local/Path/Detail/Evidence 仍可用且 readiness/status 能区分 Graph 与 Candidate 能力。
- 单页默认 20、最大 100；单节点最多持久化 100 个候选；扫描每批最多 100 个节点、每节点最多 100 个检索候选，实际远程批次遵守 Provider 上限。
- 候选查询使用 `(workspace,status,updated_at,id)` 与 fingerprint/端点索引，不允许 N+1 加载 Evidence、Decision 或 Proposal 摘要。

### CAND-09 Evaluation and delivery evidence

- 新增固定候选 Gold Set 与离线门禁，至少计算 Relation Type Accuracy、Evidence Support Rate、Candidate Precision@K、Candidate Recall、Ignored Candidate Reappearance Rate。
- 评测固定 Dataset、Model/Prompt/Schema/Index/Embedding/Rerank/Rule/Workflow 版本；Provider 未配置时使用确定性 Fake 验证管线，不能把 Fake 分数冒充真实模型质量。
- 单元、真实 PostgreSQL 集成、公共 HTTP、Workflow/River fault、OpenAPI、前端和浏览器 smoke 覆盖正常、边界、失败和重放路径。

## Acceptance Criteria

- [x] AC-01：Candidate、Relation、RelationAssessment、SearchCandidate、Proposal 是不同类型/表；未确认 Candidate 不出现在正式 Graph 或 Knowledge Eligibility。
- [x] AC-02：六类发现方式均有真实实现或显式 unsupported 状态；首版 Topic/Claim 端点、关系兼容、已有正式关系排除和有界批处理有测试。
- [x] AC-03：fingerprint 对输入顺序稳定，对 Node Version、Relation Type、Evidence、Model/Rule/Embedding/Prompt/Schema 的任一参与版本变化敏感；并发生成仅一条记录。
- [x] AC-04：Ignore/False Positive/Defer/Resume/Confirm/改类型 Confirm 的状态机、原因、CAS、幂等冲突和 append-only 决策历史通过单元与真实 PostgreSQL 测试。
- [x] AC-05：相同 fingerprint 被忽略后不重复 Active；内容/证据变化生成新 Candidate、关联旧记录并展示“因内容变化重新评估”。
- [x] AC-06：Confirm 与批量 Confirm 为每个 Candidate 创建独立 `knowledge_change` Relation Proposal；历史 `file_patch` API、Approval Dispatch、Safe Writeback 和 Git 流程回归不变。
- [x] AC-07：Relation Proposal Approval 绑定 change hash，重新校验端点版本/Evidence 后幂等写入一条 canonical Confirmed Relation；基线漂移进入 needs_revision，不能直接写 Graph 或文件。
- [x] AC-08：Topic scan 通过 Workflow/River 异步、可取消/重试/恢复；response-loss 不重复 Candidate/Proposal，模型/检索失败不伪装为空结果或成功。
- [x] AC-09：严格 OpenAPI 和前端 decoder 覆盖候选查询、扫描、决策、Proposal/Approval；cursor 篡改/stale、跨 Workspace、非法输入、503 与冲突语义明确。
- [x] AC-10：`/graph` 候选面板在桌面/移动与键盘操作下完成查看证据、确认/改类型确认、忽略、误报、稍后和重开流程；正式图画布不混入候选边。
- [x] AC-11：Candidate 依赖不可用时既有七个 Graph endpoint 和正式 `/graph` 查询仍通过；system status/readiness 不把候选故障误报为 Graph 故障。
- [x] AC-12：候选 Gold Set 输出五项冻结指标；Ignored Candidate Reappearance Rate 在未变化样本为 0，模型质量与确定性管线结果明确区分。
- [x] AC-13：迁移空库 Up、重复 Up、上一版本升级、one-step Down→Up、业务数据 guarded Down、并发唯一约束和回滚兼容测试通过。
- [x] AC-14：Go race/vet/tidy、受影响集成、`make test`、OpenAPI、前端 lint/typecheck/test/build、候选 smoke/fault/eval、浏览器 smoke、task validate、主 Agent 与独立 Go/SQL/前端 review 全部通过。
- [x] AC-15：产品、领域、数据库、API、Graph workflow、前端、测试、部署、Trellis spec 和父任务状态与实际实现同步，无假接口、假数据、静默降级或未说明范围扩大。

## Out Of Scope

- 扩展 Document/Source/Conflict/Artifact 为 Relation 端点；首版只支持已落地的 Topic/Claim。
- Smart Collection 和目录 scope 的真实扫描；分别依赖 M7-03 的 Collection Query AST 与稳定 Document/目录对象契约。
- Graph 布局/过滤视图跨刷新保存、Health Issue、Timeline 和 Impact Analysis；归 M7-03/M7-04。
- 自动接受全部高置信度候选、自动修改文件/Git、未审批写 Relation，或将模型置信度等同用户确认。
- 新图数据库、独立向量数据库、Redis/Kafka、通用 Proposal DSL 或未被当前 Relation 变更需要的 Change Control 重构。
- M10 正式认证/CSRF/Capability、500,000 Relation 最终容量与完整 append-only Audit；本任务保留兼容 seam 和局部门禁。

## Blocking Questions

无。Document、目录和 Smart Collection 的范围冲突已按当前真实领域对象保守收敛；其余实现选择由本任务设计和仓库既有约束确定。
