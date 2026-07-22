# M7-03 Smart Collection 与健康扫描

## Goal

把当前 Topic、Claim、Relation、Conflict、Source/Index 等正式事实组织为可保存、可分页、可验证的动态集合，并通过持久化扫描持续发现、去重、解释和处理知识质量问题。

Smart Collection 只保存版本化查询和视图配置，不复制知识对象；Health 只创建 Issue 或可审阅 Proposal，不在扫描、查询或页面中直接修改正式知识。

## Authoritative Sources

- 产品范围与最终验收：`docs/product/PRD.md:1951-2150,3428-3450,4425-4441,4542-4543`。
- Collection/Health 数据约束：`docs/architecture/database-design.md:566-609`。
- 模块边界：`docs/architecture/module-architecture.md:172-185,217-231`。
- 健康扫描流程：`docs/architecture/workflows/07-knowledge-health.md`。
- API、前端、性能和测试：`docs/architecture/api-and-events.md`、`frontend-architecture.md`、`performance.md`、`testing-and-evaluation.md`。
- 已落地事实：`migrations/00017_knowledge_domain.sql`、`migrations/00024_semantic_link_candidates.sql`、`internal/knowledge/**`、`internal/graph/**`、`internal/retrieval/**`、`internal/workflow/**`。

## Confirmed Baseline

- Knowledge 是 Topic、Claim、Relation、Conflict 和 Evidence 的唯一事实源；Graph 已提供只读 PostgreSQL projection，Collection/Health 不得另建可独立写入的知识副本。
- 当前正式知识端点只有 `TOPIC|CLAIM`。Document、目录、Review Card/状态和标签的稳定领域契约尚未落地；对应 Collection 字段或 scan scope 必须返回稳定 capability-unavailable，不能伪装为空结果。
- Semantic Link 已有 durable Topic scan、River 投递、幂等、取消、response-loss 和 fault smoke；`SMART_COLLECTION` 仅是预留 enum，运行时仍 fail closed。
- 仓库不存在 `internal/collection`、`internal/health`、对应迁移、OpenAPI 路由或前端页面；系统依赖健康页不是知识健康页。
- 公共列表统一 cursor + limit，Collection 首屏 P95 目标为 2 秒；健康扫描必须分页、低优先级且不持有长事务。

以上 Confirmed Baseline 记录任务创建时的缺口；本任务实现已补齐 `internal/collection`、`internal/health`、迁移、
OpenAPI、生产 composition 与 Web 页面。未落地的 Document/Directory/Tag/Review owner 仍按下文 capability-unavailable
契约处理，不能将 baseline 的历史“尚未存在”误读为当前实现状态。

## Requirements

### COLL-01 Saved query is not a fact source

- Collection 保存名称、描述、版本化 Query AST、视图类型/配置、生命周期和乐观锁版本，不保存可独立修改的 Topic/Claim/Relation 副本。
- 每次新执行都从正式 read model 计算结果；删除或归档 Collection 不删除知识。可丢弃的 count/snapshot 元数据必须明确标记为 cache，不能被读取为业务事实。
- 同一 Collection ID 可供 Semantic Link、Health、Artifact 和 Review 作为稳定 scope；消费者必须绑定 Collection version/query hash，不能接受任意 JSON scope。

### COLL-02 Versioned bounded Query AST

- `collection-query/v1` 使用判别明确的 `group|predicate` AST；group 只允许 `AND|OR`，最大嵌套深度 3、最多 64 个节点、单个 `IN` 最多 100 个值、序列化上限 32 KiB。
- 字段、运算符、值类型和适用对象类型由唯一 registry 决定；SQL 只从白名单模板生成并参数化，禁止把字段、方向或用户文本直接拼进 SQL。
- 本任务必须真实支持当前已有事实上的 `object_type`、`topic_id`、`status`、`created_at`、`updated_at`、`confidence`、`relation_type`、`health_issue_type`、`source_type`、`file_path` 和 `text`。Topic membership 只认正式 `CONFIRMED BELONGS_TO`。
- `tag`、`review_status`、Document/目录等尚无 owner 的字段必须返回 `COLLECTION_FIELD_UNAVAILABLE`；未知字段、非法 operator/enum、类型不匹配、已删除引用和超深 AST 分别明确失败，不能静默忽略。
- 排序和分组使用独立白名单；最多三个排序键，最后总以 `(object_type,id)` 提供确定性 tie-break。

### COLL-03 Dynamic execution, count and cursor

- ad-hoc preview 与 saved execution 复用同一个 compiler、PostgreSQL read model 和结果 validator；列表、表格、卡片只改变展示，不得各自实现过滤或排序。
- 结果项首版是严格 `TOPIC|CLAIM` 判别联合，携带稳定 ref、标题/摘要、状态、Topic、来源、关系、Health 摘要和更新时间；Evidence/全文不进入列表响应。
- Preview 返回精确 count 或明确 timeout/budget failure；公共结果默认 25、最大 100，禁止无分页。
- opaque cursor 绑定 schema、Workspace、Collection/version 或 canonical ad-hoc query、排序、limit、read-model revision vector 和最后一项 key。篡改、跨 Workspace/查询、Collection 变更、事实变化或进程重启分别返回 invalid/stale，不重排旧页伪装成功。
- Collection 首屏在 100,000 Claim/500,000 Relation 最终容量由 M10 复验；本任务至少提供确定性参考 fixture、EXPLAIN 和 P95 <= 2 秒的本地门禁，证明无逐结果 N+1。

### COLL-04 Collection lifecycle and views

- Create/update/archive 命令要求 Idempotency-Key 与 expected version；精确重放返回原 receipt，不同 payload 复用 key 明确冲突。
- view type 只允许 `LIST|TABLE|COMPACT_CARD`；view config 只保存注册列、固定列、排序、分组和密度，不支持公式字段、自由布局或通用数据库关联编辑。
- 内置集合使用版本化只读 definition；只有依赖事实已落地的 definition 可执行。依赖 Review/岗位等尚未实现能力的内置项必须显示 unavailable reason，不可返回误导性空集合。

### COLL-05 Bounded actions and downstream contract

- Collection 可发起 Semantic Link scan 和 Health scan；两者必须绑定 Collection ID、version、query hash 和当次 read-model revision，并走各自 durable Workflow。
- 扩展现有 Candidate Scan 的 `SMART_COLLECTION` scope，真实读取集合内当前 Topic/Claim；exact replay、取消、retry exhaustion 和 response-loss 不得创建重复 Candidate 或正式 Relation。
- Review Deck、Artifact 等尚未落地的批量动作返回独立 capability-unavailable；任何写知识动作只能创建 Proposal/Workflow，不能直接写 Knowledge。

### HEALTH-01 Issue model and evidence

- Health Issue 类型冻结为 `ORPHAN|DUPLICATE|CONFLICT|STALE|MISSING_SOURCE|LOW_CONFIDENCE|BROKEN_REFERENCE|INDEX_ERROR|SUPERSEDED_USAGE|REVIEW_INVALIDATED`；严重度为 `CRITICAL|HIGH|MEDIUM|LOW`，规则可提升但 Agent 不得降低 CRITICAL。
- Issue 必须包含 Workspace、稳定 target ref、detector ID/version、当前 fingerprint、severity、evidence summary/refs、状态、first detected、last verified、version 和可用 repair options；Evidence 必须可验证且跨 Workspace fail closed。
- 当前事实可支持的 detector 必须真实执行；`REVIEW_INVALIDATED` 在 Review owner 未落地前以 scan coverage 中的 unavailable detector 明确暴露，不能把未执行写成零问题。

### HEALTH-02 Identity, fingerprint and lifecycle

- `identity_hash` 绑定 schema + Workspace + issue type + canonical target + detector ID，用于找到同一个逻辑问题；`fingerprint` 额外绑定排序后的 evidence hashes、target object versions 和 detector version。
- 同一 identity + 相同 fingerprint 只更新 `last_verified_at`，不重复建 Issue；ignored/false-positive 在 fingerprint 未变时保持静默。
- fingerprint 变化时复用原 Issue ID、追加 observation/history，并进入 `REOPENED`；不得用新行刷屏或丢失此前决策原因。
- 状态至少支持 `OPEN|ACKNOWLEDGED|DEFERRED|PROPOSAL_CREATED|RESOLVED|IGNORED|FALSE_POSITIVE|REOPENED`。Decision 使用 expected version、Idempotency-Key 和 CAS；Ignore/False Positive 必须有规范化原因，Defer 必须有未来时间。
- 一次完整扫描未再发现的 active Issue 只能在对应 detector/scope 确认完整覆盖后自动 `RESOLVED`；partial/failed/cancelled scan 不得错误解决 Issue。

### HEALTH-03 Durable scoped scans

- 支持手动 Workspace、Topic 和 Smart Collection scope；Directory 在稳定对象契约缺失时明确 unavailable。
- Scan 是 PostgreSQL 业务事实并绑定 versioned Workflow/River job；状态至少为 `PENDING|RUNNING|SUCCEEDED|PARTIAL|FAILED|CANCELLED`，保存 detector coverage、分页 checkpoint、processed/created/reopened/resolved/unchanged/failed counts 和稳定错误摘要。
- detector 分页/批量执行，单 detector 失败独立记录；只有全部请求 detector 完整覆盖才可 SUCCEEDED。相同 scope/version/idempotency key 精确重放，同 scope 同 fingerprint 不并发。
- 支持取消、retry exhaustion、worker restart、completion response-loss 和断点恢复；River 只负责投递，不成为 scan/Issue 事实源。

### HEALTH-04 Scheduling and affected-scope triggers

- 定时维护默认关闭，支持 DAILY、WEEKLY 和校验后的 Cron；保存 scope、最大处理数、timezone、next run 和 version。
- missed schedule 只补跑一次；同一 schedule 上次仍运行时不并发；Health queue 低优先级但不能永久饥饿。
- Knowledge/Relation/Conflict/Index 的已提交变化通过事件或 Outbox 请求受影响 scope scan；触发失败不得回滚已提交知识，也不得伪装 scan 成功。

### HEALTH-05 Decisions and repair Proposal

- Issue 页面支持查看 Evidence、确认、忽略、误报、暂缓、重扫和创建修复 Proposal。
- `CreateRepairProposal` 只展示并调用已存在真实执行 seam 的 typed repair option；Proposal 与 Issue/fingerprint/target versions 绑定。owner 尚未实现的修复必须明确 unavailable，不创建不可执行的假 Proposal。
- 任何修复都经过 Proposal -> Approval -> owner apply；scan/decision 本身不得写 Topic、Claim、Relation、Conflict、文件或索引。

### API-01 Strict public contract and isolation

- OpenAPI 增加 Workspace-scoped Collection CRUD/validate/preview/results、Health summary/issues/scan/schedule/decision/repair endpoints；长 scan 返回 `202 + health_scan_id + workflow_run_id + status_url`。
- 严格 JSON 拒绝未知/重复字段、非法 UUID/enum/time/limit/cursor/Content-Type；跨 Workspace 与不存在统一 Not Found，错误不回显 Query、SQL、DSN、路径、Evidence 原文或 cursor key。
- `system.status.collections` 与 `knowledge_health` 独立于 `graph`、`semantic_links` 和进程 readiness；单能力不可用不能拖垮无关查询。
- `health.scan.completed` SSE 只触发 refetch/invalidation，刷新后仍以 REST scan/Issue 投影恢复。

### WEB-01 Real Collection and Knowledge Health experience

- 新增 `/collections`、`/collections/:id`、`/health` 真实页面；系统依赖状态组件保持独立，不冒充知识健康。
- `web/src/api/collections.ts` 与 `health.ts` 分别作为唯一 unknown-to-domain decoder/client；TanStack Query key 必含 Workspace 和 canonical request，URL 只持有可恢复过滤/排序/view/selection state。
- Collection 页面提供列表、Query Builder、实时 count、三视图、列选择、排序/分组和有界批量动作；三视图消费同一个结果 page。
- Health 页面提供 open count、severity 分布、趋势、last scan、Issue 列表、lazy Evidence、decision/repair、scan 恢复和 coverage/partial 状态。
- Loading、empty、invalid、stale cursor、partial、dependency unavailable、conflict 和 retryable failure 分开显示；桌面/移动无横向溢出，键盘、焦点恢复、dialog trap、非纯颜色状态和 screen reader 文案通过。

### QUALITY-01 Verification and review

- 覆盖 AST canonicalization/深度/白名单、fingerprint/identity、Issue 状态机、severity、cursor、幂等、CAS 和 scan completion 的确定性单测。
- 真实 PostgreSQL 覆盖 migration、约束、Workspace 隔离、查询语义、EXPLAIN、Issue 去重/reopen/resolve；真实 River 覆盖 retry/cancel/response-loss；公共 HTTP 和真实浏览器贯穿 Collection -> Health -> repair/scan 入口。
- 执行 Go race/vet/tidy、前端 lint/typecheck/test/build、OpenAPI、migration、integration、fault/browser smoke、secret/body scan、task validate 和 diff check。
- Go/SQL/frontend 跨层独立审查最多两轮，所有 P0-P2 在归档前关闭。

## Acceptance Criteria

- [x] AC-01：`collection-query/v1` 的 field/operator registry、canonical hash、最大三层/64 nodes/32 KiB 和 fail-closed 校验有完整单测；所有 SQL 标识来自白名单且值参数化。
- [x] AC-02：Collection create/update/archive/list/get 幂等、CAS、Workspace 隔离和动态结果通过真实 PostgreSQL；删除 Collection 不删除 Knowledge。
- [x] AC-03：Preview/count 和 saved results 复用同一 read model；LIST/TABLE/COMPACT_CARD 返回相同有序 refs，cursor 正常/篡改/跨请求/事实变化/进程重启语义通过。
- [x] AC-04：当前稳定字段真实可查询；缺 owner 的 tag/review/directory 明确 unavailable，不产生假空结果。
- [x] AC-05：Collection-scoped Semantic Link scan 真实运行并恢复 Candidate；绑定 Collection version/query/revision，replay/cancel/fault 不重复 Candidate 或写 Relation。
- [x] AC-06：Health detector registry 对可用 detector 真实执行，对 unavailable detector 显示 coverage；Workspace/Topic/Collection scan 的 durable 状态、checkpoint 和部分失败语义通过。
- [x] AC-07：同 identity/fingerprint 不重复；同 identity 新 fingerprint 复用 Issue 并 REOPENED；忽略在证据变化前保持；完整扫描才能自动 resolve。
- [x] AC-08：Issue decision 的 expected version/idempotency/reason/defer 规则通过；每个 Issue 有 Evidence 和真实 repair option 或明确 unavailable，任何修复不绕过 Proposal。
- [x] AC-09：定时扫描默认关闭，daily/weekly/cron、missed once、同 scope 不并发和受影响 scope 触发有持久化/故障测试。
- [x] AC-10：OpenAPI、HTTP、Problem、SSE、system status 和生产 API/Worker composition 可运行，Collection/Health 故障与 Graph/RAG 隔离。
- [x] AC-11：`/collections`、`/collections/:id`、`/health` 完成严格 decoder、URL/query cache、三视图、Query Builder、overview、lazy Evidence、决策与 scan 恢复；桌面/移动浏览器 smoke 无 console error/overflow。
- [x] AC-12：参考 fixture 下 Collection 首屏 P95 <= 2 秒且 EXPLAIN 命中预期索引；Health 分页查询无逐对象 N+1，取消及时释放资源。
- [x] AC-13：全量 Go/Web/Make/OpenAPI/migration/integration/fault/browser/secret/Trellis 门禁通过，规范和权威文档与实际行为一致。
- [x] AC-14：主 Agent Go/general/SQL review 与独立 backend/frontend 跨层 review 无未关闭 P0-P2；工作提交、归档和 journal 完成且不 push。

## Out Of Scope

- Timeline/Impact Analysis（M7-04）。
- Review Deck/Card/FSRS、Artifact 生成及其最终 Collection 批量动作（M8）。
- 通用 Document/目录对象、标签 owner 与目录 scope；在相应领域落地前保持显式 unavailable。
- Excel/CSV、公式字段、自由布局、通用数据库设计器和写知识型表格编辑。
- 最终 100k Claim/500k Relation 跨拓扑容量与正式认证（M10）；本任务仍需完成自己的参考 P95/EXPLAIN 和 Workspace 隔离。

## Resolved Decisions

1. Health 使用稳定 `identity_hash` 找回逻辑 Issue，使用包含 evidence/object/detector version 的 `fingerprint` 判断 unchanged/reopen；这样同时满足去重、忽略保持和规则变化重开。
2. Collection 与 Health 分属 `learning`/`ops` 持久事实，读取 Knowledge canonical tables；不创建可独立写入的知识副本。
3. Smart Collection cursor 使用 read-model revision vector + keyset，不持久化无限结果集；事实变化返回 stale，刷新第一页得到新结果。
4. 本任务真实启用 `SMART_COLLECTION` scan；Directory/tag/review 能力等待 owner，不用任意路径/JSON 或零结果假装支持。
5. 只有存在真实 apply seam 的修复动作才创建 typed Proposal；其余 option 明确 unavailable，避免审批后无法执行的假 Proposal。

未定义阈值、budget 和 timeout 已在 design 中按现有 Graph/Workflow 的保守上限冻结，并由真实测试收紧；不得放宽为无界。

## M7-03 完成证据（2026-07-22）

- 后端：`go test -race -count=1 ./...`、`go vet ./...`、`go mod tidy -diff`、`make test` 通过。
- 真实 PostgreSQL/River：`make collection-health-integration`、`make collection-health-fault-smoke`、
  `make collection-health-benchmark` 通过；覆盖 migration、Collection/Health/SMART_COLLECTION、schedule、
  affected-change outbox、API/Worker composition、EXPLAIN 与 response-loss/restart/cancel。
- 前端/浏览器：`npm run lint/typecheck/test/build --prefix web` 通过（37 files / 377 tests）；真实 API/Worker/Vite
  `make collection-health-browser-smoke` 通过，覆盖 Collection 三视图、Health Evidence/Decision/Scan、桌面/移动、
  零 console warning/error 和无横向溢出。
- 契约与安全：`make openapi-check`、`make collection-health-secret-scan`、changed deploy scripts `bash -n` 和
  `npm audit --audit-level=high` 通过。DOMPurify moderate advisory 与 `monaco-editor` breaking upgrade 风险保留，
  不影响 high gate。
- 文档/spec：权威 `docs/`、`.trellis/spec/backend/**`、`.trellis/spec/frontend/**` 已同步；
  `python3 ./.trellis/scripts/task.py validate 07-21-smart-collection-health` 通过。
- Review：主 Agent 已执行 `go-review`、`code-review-and-quality`、`sql-code-review`；backend/SQL reviewer
  两轮复验与 frontend/cross-layer reviewer 最终复验均未发现未关闭 P0-P2。主审发现的跨 Workspace 反向 move
  锁顺序风险已通过 UUID 固定排序和真实 PostgreSQL 并发回归关闭。
- AC-14 已由主 Agent `go-review`、`code-review-and-quality`、`sql-code-review` 与独立 backend/frontend 复验关闭；工作提交 `9ac2a9d`、Trellis 归档提交 `b0078bb` 已创建，journal 在本轮 Finish 中记录完成；
  不把 M7-04、Tag/Review/Directory owner、认证、Artifact/Review 批量动作或 M10 最终容量认证宣称为本任务完成。

## Post-archive correction（2026-07-22）

- 归档后的 canonical browser smoke 首轮真实暴露 `POST /api/v1/health/scans -> 409 HEALTH_SCAN_SCOPE_STALE`；页面未跳转，
  因而不能把归档后的复验描述为“首次即通过”。
- 根因是 Collection `revision_hash` 正确包含 Health hydration revision，但 Web 错把它当 durable scan membership
  revision；Health 写出的 Issue 会推进该 hash，使扫描被自身输出稳定打断。
- follow-up 保留 `revision_hash` 的 cursor/page 语义，并为 result/preview 增加必填 `scan_revision_hash`。两个 hash
  从同一 O(1) Workspace revision-vector 查询计算；仅 Query 引用 `health_issue_type` 时 scan hash 包含 Health revision。
- 回归证明 Health 输出会改变 page hash，但不会改变无 Health membership Query 的 scan hash；fresh migrated DB 的
  integration/fault/benchmark、全量 Go/Web/OpenAPI、安全门禁和真实 API/Worker/Vite browser smoke 已重新通过。
