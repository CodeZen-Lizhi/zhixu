# M7-01 Graph Projection And Queries

## Goal

把现有 Knowledge Topic、Claim、Relation 和 Relation Evidence 事实转化为可探索、可验证、可恢复的只读图谱产品切片：用户可以打开全局 Topic 聚类、围绕 Topic/Claim 查看一至三跳局部图、查询确定性的最短路径、按需打开关系证据，并在 `/graph` 页面得到明确的空、截断、无路径、超时和降级状态。

Graph 是 Knowledge 的查询投影，不是第二事实源；本任务不允许从 Graph API 或页面直接创建、确认或修改 Relation。

## Authoritative Sources

- 图谱产品、交互、性能和最终 AC：`docs/product/PRD.md:1703-1839,3015-3034,3679,3992-4001,4356-4367,4484`。
- 查询、候选、失败语义：`docs/architecture/workflows/06-graph-and-semantic-links.md`。
- PostgreSQL Relation 投影决策：`docs/architecture/adr/0005-no-graph-database-v1.md`。
- 数据与领域边界：`docs/architecture/database-design.md`、`docs/architecture/domain-model.md`。
- API、前端和性能约束：`docs/architecture/api-and-events.md`、`frontend-architecture.md`、`performance.md`。
- 现有事实：`migrations/00017_knowledge_domain.sql`、`internal/knowledge/**`、`api/openapi/openapi.json`、`web/**`。

冲突处理：产品最终节点类型包含 Source/Document/Topic/Claim/Conflict/Artifact，但当前正式 Relation 端点只允许 Topic/Claim。本任务以真实数据库和领域约束为安全边界，只交付 Topic/Claim 正式图；其他节点待对应领域事实和 Relation 端点契约落地后 additive 扩展，并同步修正文档中容易被理解为“本期全支持”的表述。

## Requirements

### GRAPH-01 Unique fact source and vocabulary

- 图节点只使用 Knowledge `NodeRef` 的 `TOPIC|CLAIM`，图边只投影同 Workspace 的 canonical Relation。
- 默认正式图只返回 `CONFIRMED` Relation；`STALE` 可由显式过滤查看且必须明确标记不可靠；`SUGGESTED/REJECTED/DEPRECATED` 不得伪装为正式关系。
- 对称 Relation 保留数据库 canonical identity；响应另带 traversal direction，不能复制反向边形成第二条事实。
- Graph Query 无写副作用。查询发现证据缺失只返回 diagnostic，不在读路径隐式修改 Relation。

### GRAPH-02 Global graph

- 全局入口按 Topic 返回有界 cluster summary，包含 Topic、直接正式 Claim 数、incident confirmed Relation 数和更新时间。`cluster_score = direct_claim_count + incident_relation_count`；Relation 对其涉及的每个 cluster 最多计数一次，空置信度不参与排序计算。
- 默认只展示 Active Topic 和 Confirmed/Disputed Claim 聚合；过滤器至少支持节点类型、Relation Type、Topic、状态、`claim_min_confidence`、`relation_min_confidence` 和更新时间。置信度过滤仅保留非 null 且达到阈值的对应 Claim/Relation，cluster counts/score 在过滤后重算。
- 结果采用稳定、有签名且绑定 Workspace/规范过滤/结果指纹的 cursor 分页；数据变化导致旧 cursor 明确 stale，不得重复、漏项或静默换结果。
- 禁止一次返回整个 Workspace；超过结果窗口时返回 `truncated` 和缩小范围提示。

### GRAPH-03 Local neighborhood

- 以 Topic 或 Claim 为中心，默认 depth=1，允许 1..3。
- 深度 1 支持稳定 cursor 分页；depth 2/3 返回单个有界快照，并强制 node、edge、frontier 和时间预算。
- 返回完整闭包：每条 edge 的两个端点必须在 nodes 中，或明确标为 boundary；响应包含 completed depth、每层新增数量、complete/truncated 和原因。
- 查询同时考虑 Relation source/target 两侧，保留有向语义；禁止逐节点查询和逐边 Evidence 加载。

### GRAPH-04 Shortest path

- 在两个同 Workspace Topic/Claim 间查找无权最短路径，默认 traversal=`BOTH` 的无权连接路径；有向 Relation 可正反探索但响应保留实际 traversal direction，对称 Relation 始终双向。请求可收紧为 `OUTBOUND|INBOUND`，并可限制 Relation Type；最大深度和访问预算有硬上限。
- 相同长度路径收集所有已证明处于最短 hop 数的 meeting candidates，再按完整 `(node refs, relation ids)` path key 字典序确定性选择；不得把“第一条相遇”直接当作全局 tie-break。
- 结果逐边引用稳定 Relation ID 和 evidence href；无路径返回明确 `not_found` 结果，可附双方通过 Confirmed `BELONGS_TO` 共有的 Active Topic 建议，并明确标为 `common_topic_suggestions` 而非路径。语义相似建议留 M7-02。
- 超时或预算耗尽不得返回未经证明为最短的 partial path；返回稳定 Problem 和 explored count。

### GRAPH-05 Node, relation and evidence details

- Node detail 返回类型、标题/正文摘要、状态、版本、置信度/适用条件和直接关系摘要；本期不虚构 Health、Timeline、Document 或 Artifact 数据。
- Relation detail 只返回 Relation 本体拥有的端点、Relation Type、状态、confidence、confirmation、validity、version、Evidence count/fingerprint；每条 Evidence 自己拥有的 reason/applicability 只在 Evidence page 返回，禁止压成虚构单值。
- Relation Evidence 独立 cursor 分页、按需加载，返回不可变 Source Version/Span 标识和现有可打开 href；Global/Neighborhood/Path 响应不得携带原文或全量 Evidence。
- 跨 Workspace 与不存在统一 Not Found，避免枚举。

### GRAPH-06 HTTP, cursor and failure contract

- 提供严格 OpenAPI 3.1 契约和单一 JSON decoder 边界；未知字段、非法 enum/UUID/depth/limit/filter 均明确失败。
- Cursor 使用进程内 HMAC，绑定 schema、Workspace、query kind、规范请求、结果 fingerprint、页大小和 offset；重启、篡改、跨查询或结果变化均返回稳定 invalid/stale 语义。
- Graph 查询设置 HTTP/context 和 PostgreSQL statement timeout；timeout 不占用长事务或阻塞其他 API。
- 缺依赖时路由保留并返回 503；空图、无路径、超时、cursor stale、预算截断和 Evidence 缺失不得互相伪装。

### GRAPH-07 Minimum real frontend

- 新增 `/graph` 真实页面，使用当前 Workspace；无 Workspace 时提供可操作提示。
- 支持 Global/Local/Path 三种模式、服务端节点搜索、图例、URL 持有模式/中心节点/过滤器、TanStack Query 持有 Server State。
- 节点搜索按 Workspace 查询 Active Topic name/alias 与 Confirmed/Disputed Claim statement，输入 2..256 bytes，默认 20、最大 50；规范文本完全匹配优先，其次前缀匹配，最后按 node type/id 稳定排序。它不是只过滤当前已加载页面。
- 首屏只渲染有界节点/边；节点类型使用形状+标签，Relation 状态使用线型+文字，不只靠颜色。
- 选择节点/边打开详情；Relation Evidence 只在打开关系详情时请求。
- 局部视图支持本次会话内锁定节点和固定布局；它们是本地视图状态，不写 Knowledge。布局或画布失败时保留可访问列表模式；Loading 使用 skeleton/progress，空、截断、无路径、超时和 stale 有明确文案。
- 本期不引入未经验证的大型图依赖；可使用有界 SVG/CSS 视图和列表 fallback，后续容量/UI 任务再评估 Cytoscape/Web Worker。

### GRAPH-08 Security and performance

- 所有 SQL 参数化并带 Workspace；Relation Type、Node Type、排序字段使用枚举白名单。
- 默认 limit 25、最大 100；depth 最大 3；path max depth 默认 6、最大 8；单次最多 500 nodes/1000 edges，最终数值可在实现前由真实查询测试收紧但不得放宽为无界。
- Relation Evidence 每页默认 20、最大 100。
- M7-01 必须提供 100,000 Relation/20,000 Node 的确定性生成入口，并对一跳查询执行 5 次预热 + 30 次采样，记录 p95；在项目本地 PostgreSQL 参考环境目标 ≤1.5s。`EXPLAIN (ANALYZE, BUFFERS)` 在该规模证明邻接索引可用；小 fixture 允许优化器合理选择 Seq Scan。M10 才负责 500,000 Relation 最终 P95/FPS 门禁。本期仍须证明不 N+1、取消及时释放资源。
- 仅当 direct SQL 的实测计划无法满足本期门禁时才新增可重建持久投影；不得先验引入 Graph 双写或独立图数据库。

## Acceptance Criteria

- [ ] AC-01：Graph domain/application 使用 Knowledge NodeRef/Relation vocabulary，默认只返回正式 Confirmed 图；Workspace、状态、对称边和端点闭包验证通过。
- [ ] AC-02：真实 PostgreSQL 全局 Topic cluster 按冻结的 cluster_score/updated_at/id 排序并支持过滤和 cursor 分页；稳定数据不重不漏，篡改/跨 Workspace/参数变化/结果变化 cursor 分别得到明确错误。
- [ ] AC-03：局部 depth 1/2/3 在循环、分支和双向 Relation 数据上返回确定性闭包；硬预算、每层计数、截断和取消语义通过，查询无 N+1 Evidence。
- [ ] AC-04：最短路径遵守状态/Relation Type/max depth，收集最短深度 meeting candidates 后按完整 path key 确定性返回逐边可追踪路径；无路径可返回明确的共同 Topic 建议，超时和预算耗尽不伪造 partial/similarity path。
- [ ] AC-05：Node/Relation detail 可查询；Relation Evidence 按需分页并能经现有 Router 打开 Source Version/Span，列表/路径响应不泄露正文、绝对路径或全量 Evidence。
- [ ] AC-06：Graph HTTP、Problem、HMAC cursor、OpenAPI、503 fail-closed 和生产 composition 可运行；严格 JSON、404 防枚举、405、timeout/cancel 测试通过。
- [ ] AC-07：`/graph` 完成 Global/Local/Path、节点搜索、图例、URL 恢复、本地锁定/固定布局、节点/关系选择、Evidence lazy load、列表 fallback，以及 loading/empty/truncated/no-path/error 状态；桌面和移动端无横向溢出，键盘可操作。
- [ ] AC-08：真实 PostgreSQL integration 与公共 HTTP smoke 贯穿 Global→Local→Path→Relation Evidence；跨 Workspace、cursor response-loss/变化和查询超时样本通过。
- [ ] AC-09：100k Relation/20k Node fixture 的一跳查询按 5 次预热 + 30 次采样记录 p95≤1.5s，代表性 SQL 有索引可用的 `EXPLAIN` 证据，无逐节点/逐 Evidence 查询；前端只渲染有界结果。500k 最终 P95/FPS 明确保留给 M10，不宣称提前完成。
- [ ] AC-10：`go test -race ./...`、`go vet ./...`、`go mod tidy -diff`、`make test`、OpenAPI、前端 lint/typecheck/test/build、task validate 和最小 Graph smoke 全部通过。
- [ ] AC-11：产品/API/数据库/前端/测试文档同步 Topic/Claim 首版边界、失败语义、运行与回滚；独立 Go/SQL/frontend 跨层审查无未关闭 P0-P2。

## Out Of Scope

- Source、Document、Conflict、Artifact 作为正式 Relation 端点。
- Semantic Link Candidate 生成、确认/忽略和 fingerprint（M7-02）。
- Health Issue、Knowledge Timeline、Impact Analysis（M7-03/M7-04）。
- Graph 直接创建/修改 Relation；所有修改仍走 Proposal/Approval。
- 跨刷新布局保存、多人视图、Web Worker/Cytoscape 的最终大图体验；本期仅保留会话内锁定/固定。
- 500,000 Relation 最终 P95/FPS 容量验收（M10-03）。

## Blocking Questions

无。缺失数值和语义已按现有安全/性能不变量冻结为保守、可扩展契约；若真实 EXPLAIN 证明需持久 projection，将先回写 design/implement 再实施。
