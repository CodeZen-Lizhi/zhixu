# Research: M7-01 图谱投影查询的产品与架构需求

- Query: 只读梳理 M7-01 图谱投影查询相关的产品、领域、API、前端、数据库、性能与失败语义，识别明确契约、冲突缺口、验收建议和范围边界。
- Scope: internal
- Date: 2026-07-20

## Findings

### 1. 文件与职责

- `docs/product/PRD.md`：图谱产品目标、用户流程、容量、评测、风险和最终 AC。
- `docs/architecture/workflows/06-graph-and-semantic-links.md`：图谱查询、候选关联、失败与操作流程摘要。
- `docs/architecture/frontend-architecture.md`：Graph 前端状态、分页、布局与渲染边界。
- `docs/architecture/api-and-events.md`：Graph API 能力、Query/Command 分离、分页和错误模型。
- `docs/architecture/performance.md`：容量基线、局部一跳延迟预算、反 N+1 和扩容触发条件。
- `docs/architecture/domain-model.md`：Relation 语义、状态机、命令和 Graph & Health 边界。
- `docs/architecture/database-design.md`：Relation/Relation Evidence 存储、端点约束、索引和事务责任。
- `docs/architecture/adr/0005-no-graph-database-v1.md`：v1 使用 PostgreSQL Relation 表与查询投影，不引入独立图数据库。
- `.trellis/spec/backend/database-guidelines.md`、`.trellis/spec/backend/quality-guidelines.md`、`.trellis/spec/frontend/type-safety.md`、`.trellis/spec/guides/cross-layer-thinking-guide.md`：后续实现需遵守的数据库、质量、类型边界与跨层契约规范；本研究未修改实现。

### 2. 核心术语与不可混淆概念

- **Graph Query Projection**：从正式知识事实构建的只读图谱查询模型；不是新的事实源。正式 v1 图谱建立在 PostgreSQL `Relation` 模型与查询投影上（`docs/product/PRD.md:4191-4194`；`docs/architecture/adr/0005-no-graph-database-v1.md:5-12`）。
- **Knowledge Node**：图谱端点的领域对象。最终产品节点类型包括 Source、Document、Topic、Claim、Conflict、Artifact（`docs/product/PRD.md:1709-1718`），但当前数据库明确只注册 `TOPIC`、`CLAIM`，其他类型须等真实领域对象与查询契约落地后扩展（`docs/architecture/database-design.md:317-325`）。
- **Relation**：节点之间带类型的知识事实或候选事实（`docs/product/PRD.md:423-428`），含方向、状态、置信度、有效期和版本（`docs/product/PRD.md:3286-3297`）。
- **Relation Evidence**：支撑关系的不可变来源定位、理由、证据哈希、模型/确认信息，不等同于 Search Evidence 或 Proposal Evidence（`docs/architecture/database-design.md:290-310`）。
- **Relation Assessment**：NEW、COMPLEMENTARY、DUPLICATE、CONFLICT、LOW_CONFIDENCE 是分析判断，不是正式 RelationType；NEW、LOW_CONFIDENCE 不得落为关系边（`docs/architecture/domain-model.md:249-265`）。
- **Relation 状态**：SUGGESTED、CONFIRMED、REJECTED、STALE、DEPRECATED；证据变化才允许 REJECTED 再次成为 SUGGESTED（`docs/product/PRD.md:539-545`；`docs/architecture/domain-model.md:311-324`）。
- **Candidate**：语义检索和分类产生的待用户处理对象，需用节点版本、关系类型、证据哈希、模型/规则版本形成指纹；内容未变不重复推荐（`docs/architecture/workflows/06-graph-and-semantic-links.md:20-32,54-63`）。
- **Graph Layout / Filter View**：个人视图配置，不是知识关系；删除布局不得改变 Relation（`docs/product/PRD.md:285-289,1820-1824`）。
- **Global Graph**：Topic 聚类、重要节点优先、分页和按需展开的全局视图，禁止全量渲染（`docs/product/PRD.md:1744-1762`）。
- **Local Graph / Neighborhood**：以当前节点为中心，默认一跳，可由用户调整至二或三跳，邻居服务端分页（`docs/product/PRD.md:1764-1770`；`docs/architecture/api-and-events.md:96-102`）。
- **Path Query**：起终点之间的最短路径，可按关系类型限制；每条边必须可展示证据，无路径时不得将相似性伪造为正式路径（`docs/product/PRD.md:1772-1779`）。

### 3. 用户流程

主探索流程：

1. 用户打开 `/graph`，选择全局或局部图谱并应用 Topic、节点/关系类型、来源、时间、确认状态、置信度和健康状态过滤（`docs/product/PRD.md:1744-1762`；`docs/architecture/frontend-architecture.md:88-107`）。
2. 服务端查询 Graph Projection，完成聚类/分页后返回限定数量节点和边；前端分批渲染，布局在前端或 Web Worker 计算（`docs/architecture/workflows/06-graph-and-semantic-links.md:7-18`；`docs/architecture/frontend-architecture.md:133-142,190-196`）。
3. 用户选择节点、关系或路径，按需加载详情、Evidence、Timeline 和 Health；Evidence 不应随所有邻居首屏 N+1 加载（`docs/architecture/workflows/06-graph-and-semantic-links.md:16-18,41-45`；`docs/architecture/performance.md:46-52,69-73`）。
4. 用户继续展开邻居，或选择两个节点执行最短路径查询；无路径时可推荐共同 Topic/相似节点，但须明确不是正式路径（`docs/product/PRD.md:1764-1779`）。
5. 用户从图谱发起建立/修改关系、合并重复、冲突处理或补来源等动作；所有修改类动作只创建 Proposal，不得直接改正式知识（`docs/product/PRD.md:1807-1818`；`docs/architecture/workflows/06-graph-and-semantic-links.md:65-73`）。

候选关联流程：

1. 对目标节点执行 lexical/semantic candidate search。
2. 排除已有正式关系及证据未变化的已拒绝/忽略候选。
3. 分类建议关系，展示双方摘要、对应段落、理由、置信度和发现方式。
4. 保存 Candidate Fingerprint。
5. 用户确认时创建独立 Relation Proposal；忽略时保存指纹与原因；确认前不得进入正式图谱（`docs/product/PRD.md:1862-1907`）。

### 4. M7-01 明确需求

#### 4.1 查询能力

- 提供 global clusters、neighborhood、path、relation details、candidates 五类 Graph Query 能力（`docs/architecture/api-and-events.md:223-230`）。
- Graph Query 必须无业务副作用、可缓存、分页并返回 read model；修改动作属于 Command/Proposal（`docs/architecture/api-and-events.md:37-51`）。
- 邻居查询使用 cursor，稳定排序字段后追加 ID，`limit` 必须有最大值（`docs/architecture/api-and-events.md:96-102`）。
- 全局图谱必须 Topic 聚类、重要节点优先、分页/按需展开，不得一次取全部节点（`docs/product/PRD.md:1744-1751`；`docs/architecture/frontend-architecture.md:133-142`）。
- 局部图谱默认深度 1，允许深度 2/3；每次扩展需返回或可推导新增节点数量（`docs/product/PRD.md:1764-1770`）。
- 路径查询返回最短路径，支持 RelationType 过滤，并保证每条边可追到 Evidence；没有路径时返回空路径/明确状态，不构造相似性边（`docs/product/PRD.md:1772-1779`）。
- Relation detail 至少覆盖两端、类型、证据段落、判断理由、置信度组成、确认状态、关联 Proposal/Workflow（`docs/product/PRD.md:1795-1805`）。
- 节点详情至少覆盖标题/类型、摘要、来源、正式版本、直接关系、Conflict、Health Issue、Timeline 与可执行动作（`docs/product/PRD.md:1781-1793`）。

#### 4.2 数据资格与一致性

- Confirmed Relation 必须有有效证据和确认方式；AI confidence 不等同用户确认（`docs/architecture/domain-model.md:94-109,423-429`）。
- Suggested、Rejected、Deprecated 及无正式绑定 Evidence 默认不得作为已确认知识资格（`docs/architecture/database-design.md:305-311`）。
- Relation 端点必须同 Workspace、存在、处于允许生命周期、类型组合合法且无自环；对称关系使用稳定 ID 排序避免反向重复（`docs/architecture/database-design.md:313-324,940-948`）。
- Relation 确认与 Knowledge Event 在同一事务；事件/投影不是独立事实源（`docs/architecture/database-design.md:869-877`；`docs/architecture/domain-model.md:381-399`）。
- 投影失败必须可恢复，不能覆盖或升级正式事实，也不应回滚已提交的领域事务（`docs/architecture/database-design.md:886-899`）。

#### 4.3 性能与容量

- 验收容量基线：10,000 Document、100,000 Claim、500,000 Relation，单并发用户、1–4 Worker；这是压测基线而非硬限制（`docs/product/PRD.md:3662-3674`；`docs/architecture/performance.md:7-18`）。
- 500,000 Relation 下局部图谱一跳查询 P95 ≤ 1.5 秒；模型耗时单独统计（`docs/product/PRD.md:3676-3689`）。
- 不得逐节点查询或 N+1 加载 Relation Evidence；邻居和 Evidence 应批量/延迟加载（`docs/architecture/performance.md:61-73`）。
- 图谱需设每次扩展上限和路径最大深度，聚类结果/Layout 可缓存；布局不得长时间占用数据库连接（`docs/architecture/performance.md:46-52,83-90,107-120`）。
- v1 不引入图数据库；仅当多跳成为核心且 PostgreSQL 被实测为瓶颈，或复杂图算法不可维护时重评（`docs/architecture/performance.md:145-157`）。

#### 4.4 前端与可访问性

- Graph 属于 TanStack Query Server State；过滤、当前对象、中心节点属于 URL State；SSE 不能作为唯一事实源（`docs/architecture/frontend-architecture.md:51-86`）。
- 节点类型必须使用形状+图标/标签区分，状态使用线型+标签，不能只靠颜色（`docs/product/PRD.md:1718`；`docs/architecture/frontend-architecture.md:133-140`）。
- 加载时展示 skeleton/progress，不能是空白画布；Layout 失败保留列表模式（`docs/product/PRD.md:4356-4367`；`docs/architecture/workflows/06-graph-and-semantic-links.md:75-80`）。

#### 4.5 失败语义

- Query Timeout：提示缩小范围；不得阻塞其他功能（`docs/architecture/workflows/06-graph-and-semantic-links.md:75-80`；`docs/product/PRD.md:1826-1832`）。
- Missing Evidence：Relation 标记/呈现为 STALE，不能继续显示成有可靠证据的 CONFIRMED 边（`docs/architecture/workflows/06-graph-and-semantic-links.md:75-80`）。
- Layout Fail：查询结果仍可用，回退列表模式。
- Candidate Model Down：已确认图谱仍可查询，候选能力单独降级。
- API 错误需使用稳定 `error_code`、用户可理解 message、retryable、阶段/影响/推荐操作；错误信息不得伪装为空结果（`docs/product/PRD.md:649-658`；`docs/architecture/api-and-events.md:82-94`）。

### 5. 冲突与缺口

#### 高优先级

1. **最终六类节点 vs 当前仅 Topic/Claim 端点。** PRD 要求 Source、Document、Topic、Claim、Conflict、Artifact 六类节点，但数据库设计明确 M5-05 仅注册 TOPIC/CLAIM。M7-01 必须明确是只交付 Topic/Claim 投影，还是包含无正式 Relation 端点契约的派生节点；不能通过任意多态表名/文本绕过约束。
2. **“AI 候选与正式关系可区分” vs “候选确认前不进入正式图谱”。** 更安全的解释是 Graph read model 可叠加独立 candidate overlay，但 Candidate 不得伪装为正式 Relation；API schema 应分别返回 confirmed/suggested/candidate 的来源和资格，避免仅靠 `status` 混合两种事实。
3. **API 只有能力名，没有精确契约。** 缺少 endpoint、method、请求参数、响应 schema、节点/边稳定 ID、cursor 编码/失效、默认/最大 limit、排序、深度校验、路径结果和 relation detail 结构。M7-01 需要以 OpenAPI/typed DTO 固化，否则前端无法建立严格 decoder 边界。
4. **性能边界未定量。** 文档要求“每次扩展上限”“路径最大深度”“超过阈值禁止全量展开”，但未给数值；也未定义 1.5 秒是否包含 Evidence、冷/热缓存、HTTP 序列化。至少应在实现/验收中锁定默认与硬上限，并分别测邻居骨架与 Evidence detail。
5. **Projection 新鲜度/重建契约缺失。** 已说明投影可恢复且非事实源，但未定义 Graph Projection 的事件来源、水位/版本、读时过期标记、重放去重键、失败重试和何时对查询可见。

#### 中优先级

6. **路径语义不足。** “最短路径”未说明有向/无向、权重、最大深度、允许状态、是否仅 CONFIRMED、同长度路径返回数量、环处理和超时后的 partial result。
7. **全局聚类语义不足。** 未定义 cluster ID 稳定性、Topic 归属来源、无 Topic 节点如何归类、聚类缓存失效、cluster summary/count 是否受全部过滤器影响。
8. **局部邻居排序不足。** Cursor 需要稳定排序，但未定义按权重、Relation 创建时间、节点标题还是 ID；排序字段变化将影响分页不重不漏。
9. **Evidence 缺失处理存在写副作用疑问。** Query 过程中发现 Missing Evidence 时，“Relation 标记 Stale”是领域状态变更，不能由 Query 直接执行。应由查询返回 stale diagnostic，并通过 Health/Workflow/Command 异步转移状态，或明确由已有投影器负责。
10. **错误码未命名。** 尚无 Graph 专用 timeout、invalid depth/filter、cursor invalid/stale、node/relation not found、projection unavailable/stale、candidate provider unavailable 等稳定错误码及 HTTP 状态。
11. **Workspace 隔离未在 Graph API 章节重申。** 数据库要求同 Workspace，API 仍需明确 workspace 解析、跨 Workspace 统一 Not Found、防止 cursor 跨 Workspace 复用。
12. **布局保存不属于查询核心但与 UI 强相关。** 需求要求保存布局/过滤视图，却未定义 Graph Layout 的版本、节点消失后的容错、用户/Workspace 作用域和并发更新契约。
13. **评测指标无阈值。** 17.6 仅列 Relation Type Accuracy、Evidence Support Rate、Candidate Precision@K/Recall、Ignored Candidate Reappearance Rate（`docs/product/PRD.md:3889-3898`），没有数据集、K 值、基线与通过阈值；这些主要针对语义候选，不足以验收投影查询正确性。

### 6. 验收建议

#### 查询正确性

- 用固定 Workspace 数据集验证 global clusters、depth=1 neighborhood、depth=2/3 扩展、relation detail、path 和 candidates 的稳定快照。
- 验证所有返回节点/边均属于请求 Workspace；跨 Workspace ID 与 cursor 统一不可枚举。
- 验证 cursor 分页在稳定数据下不重不漏，非法、篡改、参数不匹配和投影版本变化有明确契约。
- 验证 RelationType 方向；DUPLICATES/CONFLICTS_WITH 正反输入只形成/返回一条规范边；自环、非法端点组合不出现。
- 默认正式图谱只把 CONFIRMED 作为正式边；SUGGESTED/STALE/DEPRECATED/Candidate 必须以资格字段和可访问样式明确区分。
- Path 只经过允许状态/类型，逐边可反查 Relation Detail 和 Evidence；无路径返回确定性空结果且不生成相似性边。

#### 失败与降级

- 注入查询超时：返回稳定错误/降级提示，不拖垮普通 API；前端提示缩小过滤范围。
- 注入 Evidence 缺失：查询不 N+1、不伪造 Evidence；返回 stale diagnostic，并验证状态修复不由 Query 隐式写入。
- 注入候选模型不可用：neighborhood/global/path/relation detail 仍正常，candidate 明确 unavailable/degraded。
- 注入布局 Worker 异常：保留节点/边列表或可访问详情，不显示空白页面。
- 注入 Graph Projection 重建/落后：返回明确 freshness/degraded 信息，不能把空投影误报为“没有知识关系”。

#### 性能与数据库

- 在 500,000 Relation 数据集上分别测冷/热缓存一跳邻居 P95，目标 ≤ 1.5 秒，并记录 SQL、序列化与网络边界。
- `EXPLAIN (ANALYZE, BUFFERS)` 验证 source/target + relation_type 索引；测试双向邻居组合不会退化成全表扫描。
- 通过 query count/assertion 验证 neighborhood 与 Evidence 无 N+1；Evidence detail 使用批量或单关系按需加载。
- 验证 limit/depth/每次扩展/path max depth 的服务端硬上限，任何请求都不能触发无界递归或全量返回。
- 验证投影重放幂等、重复事件不重复边、RelationConfirmed 与 Knowledge Event 一致、失败投影可恢复。

#### AC 映射

- **AC-18 Graph**：至少拆成全局聚类、局部邻居、路径、Relation Evidence、确认状态五个独立可测项（`docs/product/PRD.md:4484`）。
- **AC-19 Semantic Link**：候选确认/忽略/去重/内容变化后重开，与正式 Graph Projection 分开验收（`docs/product/PRD.md:4485`）。
- **AC-31 Capacity**：500,000 Relation 的一跳查询 P95 ≤ 1.5 秒，并补 UI 分批渲染/FPS 证据（`docs/product/PRD.md:4497`；`docs/architecture/performance.md:168-175`）。

### 7. M7-01 建议范围边界

#### 应纳入

- Graph Query Adapter/Repository 的只读投影查询边界。
- 当前已落地合法节点类型上的 global cluster、neighborhood、path、node/relation detail 查询。
- Workspace 隔离、稳定 cursor、limit/depth/path 上限、确定性排序。
- Relation 状态/资格与 Evidence 摘要或按需详情的 read model。
- 查询超时、not found、invalid filter/cursor、projection degraded 的稳定错误语义。
- 核心 SQL 索引使用、反 N+1 测试及容量数据上的基准入口。

#### 除非任务 PRD 明确要求，否则不应纳入

- 新增独立图数据库或 Relation 双写。
- 扩展 Source/Document/Conflict/Artifact 为正式 Relation 端点（需要独立领域与数据库契约）。
- 语义模型候选生成、离线 Relation Accuracy 评测和批量扫描工作流。
- Relation 创建/确认/修改、Proposal/Approval/写回实现。
- Cytoscape 页面、Web Worker 布局、视觉样式和布局保存完整 UI。
- Health Issue、Timeline、Artifact/Review/Collection 的写命令。
- M10 最终 500,000 Relation 性能门禁本身；M7-01 应提供正确查询与可压测结构，不应声称已完成最终容量交付，除非任务验收另有规定。

### 8. 相关规范

- 数据库层需将结构约束与领域语义分开：数据库负责枚举、唯一性、Workspace 归属和索引；Domain Module 负责 RelationType/端点兼容、证据要求、状态机和禁止自环（`docs/architecture/database-design.md:317-325,886-901`）。
- Graph 查询属于读模型，不得绕过 Proposal/Approval 执行写入（`docs/product/PRD.md:271-283,1807-1818`）。
- 前端 Graph 数据以服务端 Query 为事实源；事件只做失效通知，URL 承载可分享过滤和中心节点状态（`docs/architecture/frontend-architecture.md:51-86`）。

## Caveats / Not Found

- `python3 ./.trellis/scripts/task.py current --source` 返回 `Current task: (none)`；本文件路径依据父任务明确给出的 `.trellis/tasks/07-20-graph-projection-queries`，不是自行猜测。
- 未找到 M7-01 专属 PRD/design/implement 对 Graph endpoint、DTO、数值上限和错误码的最终裁决；上述“验收建议”和“范围边界”是基于全局 PRD/架构的可执行收敛建议，不应冒充已批准契约。
- 未检查业务代码当前实现状态；本研究只回答产品与架构需求，不判断现有代码是否已满足。
- 文档没有为 17.6 指标给出阈值、K 值、固定数据集或基线，也没有为全局图谱定义量化 FPS/交互门槛。
- 文档没有精确规定图谱路径的方向、权重、最大深度和多路径返回策略，也没有规定全局聚类算法与 cluster ID 稳定性。
