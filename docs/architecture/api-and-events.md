# API 与事件架构

## 1. 目标

定义 Web UI 与 Go API 的命令、查询、异步任务和实时事件契约。

## 2. 风格

- REST 用于资源查询和命令提交。
- SSE 用于单向任务状态推送。
- 长任务返回 202 + Workflow Run ID。
- 写命令要求 Idempotency-Key。
- 修改要求 Version/ETag。
- 统一 Problem Details 风格错误。

## 3. API 分组

```text
/api/v1/workspaces
/api/v1/sources
/api/v1/documents
/api/v1/search
/api/v1/conversations
/api/v1/proposals
/api/v1/approvals
/api/v1/graph
/api/v1/collections
/api/v1/exports
/api/v1/health
/api/v1/artifacts
/api/v1/review
/api/v1/workflows
/api/v1/settings
/api/v1/auth
/api/v1/events
```

## 4. 命令与查询分离

Query：

- 无业务副作用。
- 可缓存。
- 分页。
- 返回 read model。

Command：

- 校验版本。
- 返回业务结果或 Workflow Run。
- 记录审计。
- 使用 Idempotency Key。

## 5. 异步命令

响应：

```json
{
  "workflow_run_id": "uuid",
  "status": "PENDING",
  "status_url": "/api/v1/workflows/uuid"
}
```

客户端不假设创建任务即成功完成。

## 6. 版本冲突

请求带：

- If-Match/Version。
- Target Revision。
- Change Hash（审批）。

冲突返回：

- current_version。
- expected_version。
- conflict_type。
- resolution_actions。

## 7. 错误模型

```json
{
  "error_code": "CONSISTENCY_CONFLICT",
  "message": "目标文件在审批后发生变化",
  "retryable": false,
  "workflow_run_id": "uuid",
  "details": {}
}
```

错误码稳定，message 可本地化。

## 8. 分页

- 默认 Cursor Pagination。
- 稳定排序字段 + ID。
- limit 有最大值。
- 图谱邻居使用 cursor。
- Collection 大结果禁止无分页。
- Search Cursor 是单独的有界结果窗口契约：服务端每次重算规范请求的 top-100，Cursor v1 使用
  API 进程内随机 HMAC-SHA256 密钥签名，并绑定规范请求、页大小、Active Index、完整有序结果指纹
  和下一 offset。签名/请求不匹配返回 invalid；Index 或完整结果变化返回 stale；进程重启后旧
  Cursor 失效。它不是通用列表游标、持久 Search Session 或授权凭据。

## 9. SSE

事件：

- workflow.status.changed。
- workflow.node.changed。
- human_task.created。
- proposal.ready。
- index.activated。
- export.*。
- health.scan.completed。
- review.scored。
- conversation.created。
- answer.pending。
- rag.plan.started / rag.plan.completed。
- rag.retrieval.started / rag.retrieval.completed。
- rag.validation.started / rag.validation.completed。
- answer.completed / answer.refused / answer.clarification_required。

事件字段：

- id。
- type。
- occurred_at。
- workspace_id。
- resource_ref。
- payload_summary。
- schema_version。

M6-04 已实现 `GET /api/v1/events?workspace_id=...`。Wire 使用持久单调序号作为 `id`，Envelope
包含 `id/type/occurred_at/workspace_id/resource_ref/resource_version/payload_summary/schema_version`；
heartbeat 使用 SSE comment，不承载业务数据。`Last-Event-ID` 必须是正十进制序号，非法/未来游标
返回 400，早于保留窗口返回 409 和 `details.action=refetch`。未提供游标时从连接建立水位开始，
不会补发全部历史。

## 10. 断线恢复

- 客户端保存 Last-Event-ID。
- 重连时服务端补发保留窗口内事件。
- 超出窗口则客户端重新查询资源状态。
- SSE 不是事实源。

## 11. 主要契约

### Source Import

- 输入：文件/URL/文本、处理策略。
- 输出：Source ID、Workflow Run。

M5 已落地的同步摄取命令为 `POST /api/v1/source-versions/{source_version_id}/ingestion-attempts`：

- 必须携带 `Idempotency-Key` 和 `attempt_number`。
- 服务端只读取已捕获的 Content Artifact，不读取用户原始路径。
- 首次完成返回 201；同一幂等键重放返回已持久化 Attempt/Projection 摘要（200）。
- 响应中的 `chunked` 仅表示 Ingestion 投影完成，不表示 FTS、Embedding 或 `ready`。
- 超过长任务阈值的异步 Workflow 仍由 Workflow API/Worker 负责；该命令不把同步返回包装成异步成功。

### Search

- `POST /api/v1/search`；无业务副作用，不要求 `Idempotency-Key`，不得创建 Workflow。
- 输入：必填 `workspace_id`、`query`；可选 `retrieval_mode=keyword|semantic|hybrid`、
  `filters.source_ids|source_version_ids|path_prefixes|captured_at_from|captured_at_before`、
  opaque `cursor` 和 `limit`。默认 Hybrid、默认 limit 20，公开范围为 `1..100`。
- 输出：Workspace、requested/effective mode、Active Index Version、可选 Embedding Version、
  Index degraded capabilities、单次 Query degradations、Evidence Items 与可选 `next_cursor`。
- Evidence stage 字段分型：Lexical 返回 rank/score/FTS/trigram 原始值；Vector 返回 rank 与原始
  `distance`，不得冒充 similarity；Fusion 返回 rank/score；Rerank 仅在真实执行时返回
  rank/score/model version。
- Embedding disabled 或 FTS-only Active 时，Keyword 正常；Hybrid 返回 Keyword 结果并显式标记
  vector/rerank degraded；Semantic 返回 `503 RETRIEVAL_SEMANTIC_UNAVAILABLE`。零命中返回
  `200` 与 `items=[]`，不转换成错误或假语义成功。

可打开 Evidence 资源：

- `GET /api/v1/workspaces/{workspace_id}/source-versions/{source_version_id}`。
- `GET /api/v1/workspaces/{workspace_id}/source-versions/{source_version_id}/spans/{source_span_id}`。
- Source Version 响应不返回 Workspace 绝对根路径或 Content Artifact locator。
- Span 必须验证 Workspace → Source Version → Content Artifact → Parse Projection → Span 绑定，
  从不可变 Artifact 读取并复核 Hash/大小/字节范围，最多返回 4 KiB UTF-8 excerpt；不得读取当前
  工作树路径。跨 Workspace、版本/Span 不关联和不存在均按相同 Not Found 契约处理。

### RAG

M6-04 已实现以下公开契约，精确 Schema、状态码、Problem 和 405 以 `api/openapi/openapi.json` 为准：

- `POST /api/v1/conversations`：严格 JSON + `Idempotency-Key`，首次 201、精确重放 200。
- `GET /api/v1/conversations?workspace_id=&cursor=&limit=`：按最近活动时间稳定分页。
- `GET /api/v1/conversations/{conversation_id}?workspace_id=`：Conversation + weak ETag。
- `POST /api/v1/conversations/{conversation_id}/questions`：Question、Scope、`answer_depth`、
  `output_format`；首次 202、精确重放 200，并返回 Answer 与 `status_url`。
- `GET /api/v1/conversations/{conversation_id}/turns?workspace_id=&cursor=&limit=&latest=`：按
  `(ordinal,id)` 恢复 Question/Answer 投影；`latest=true` 返回最新一条恢复投影。
- `GET /api/v1/answers/{answer_id}?workspace_id=`：四态发布结果（pending/completed/refused/
  clarification_required）、Workflow 当前阶段、RAG v2 结果、可打开 Citation 和 retrieval summary。
- `POST /api/v1/answers/{answer_id}/feedback`：五类 append-only 反馈；首次 201、精确重放 200。

Question 的持久 Workflow Input 只含稳定 ID、版本和 Hash；正文与有界历史由 Conversation 事实源加载。
同一 Conversation 同时只允许一个非终态 Answer Workflow。事实性 Answer 经过 Query Plan、Retrieval、
Knowledge Eligibility、结构化生成、Citation 与 Faithfulness 门禁后才原子发布；Provider/数据库故障保留为
Workflow 失败，不伪装成业务 Refusal。`allow_original_sources/allow_web` 当前没有可发布资格时显式拒绝，
不会静默忽略。

### M9 Workspace 业务列表

M9-01 为页面增加三个 Workspace-scoped 只读投影，详情仍由各领域已有接口拥有：

- `GET /api/v1/workspaces/{workspace_id}/source-versions`：按 security/ingestion/workflow/index/mime 筛选，
  返回 Source Version、最新 Ingestion Attempt、Workflow 和 Active Index 选择状态。
- `GET /api/v1/workspaces/{workspace_id}/proposals`：按 status、proposal_type、risk、created_after 筛选，
  返回最新 Revision 与可选 Approval summary。
- `GET /api/v1/workspaces/{workspace_id}/workflows`：按 status 筛选，返回持久 Run、Definition 和 waiting-human 摘要。

三者均使用 `limit=1..100` 和最大 2048 字节 opaque keyset cursor。Cursor 必须绑定版本、资源 kind、Workspace、
规范化筛选和最后 `(time,id)`；损坏、跨资源、跨 Workspace 或跨筛选复用返回 400，不能静默回到第一页。
这些列表不创建 Dashboard 第二事实表，也不把 Workspace ID/cursor 当成认证凭据。

### Approval

- 输入：proposal_revision、approved_change_hash、action。
- 输出：Approval + Apply Workflow。
- Proposal detail 的 `approval` 字段必须始终显式存在：未决定时为 `null`，决定后为与当前
  Proposal/Revision/Change Hash 绑定的 Approval snapshot。Proposal summary 仍允许省略该字段，但不得返回显式 `null`。

### Workflow Control

`POST /api/v1/workflows/{run_id}/pause|resume|cancel` 是版本化控制命令：

- Header 必须有 `Idempotency-Key`（1..128）；Body 为 `{ "expected_version": <positive integer> }`，不接受客户端 `workspace_id`。
- 成功返回 `{workflow_run_id,status,version,status_url}`；相同命令键和请求 hash 重放返回持久结果。
- 稳定错误包括 `IDEMPOTENCY_KEY_REQUIRED`、`WORKFLOW_CONTROL_INVALID`、`WORKFLOW_RUN_NOT_FOUND`、`WORKFLOW_VERSION_CONFLICT`、`WORKFLOW_CONTROL_CONFLICT`。
- Workspace 绑定由 Run 持久化事实解析，不能由请求体覆盖。

### Graph

Graph v1 是 Knowledge Topic/Claim/Relation 的只读 PostgreSQL 投影，不是第二事实源。正式节点端点只接受
`TOPIC|CLAIM`；默认关系状态是 `CONFIRMED`，只有显式过滤时才返回并标记 `STALE`。`SUGGESTED`、
`REJECTED` 和 `DEPRECATED` 不会伪装成正式图。七个正式 Graph Query API 不创建、确认或修改 Relation。
M7-02 另设 Candidate/Scan 命令边界；Candidate 确认只创建 typed Proposal，Approval 后才由 Knowledge 写 Relation。

当前 OpenAPI 3.1 定义七个正式 Graph Query 端点和五个 Candidate/Scan 端点：

| 方法与路径 | 语义 |
|---|---|
| `POST /api/v1/graph/global` | 以严格 JSON 提交 Workspace、过滤器、limit 和可选 cursor，分页返回有界 Topic cluster summary。 |
| `POST /api/v1/graph/neighborhood` | 以严格 JSON 提交中心节点、方向、深度和硬预算；depth=1 支持 cursor，depth=2/3 返回单个有界完整层快照。 |
| `POST /api/v1/graph/path` | 以严格 JSON 提交两个端点、方向、Relation Type 和访问预算，返回已证明的确定性最短路径或 `status=not_found`。 |
| `GET /api/v1/graph/nodes?workspace_id=&query=&limit=` | 在服务端搜索 Active Topic name/alias 与 Confirmed/Disputed Claim statement；默认 20，最大 50。 |
| `GET /api/v1/graph/nodes/{node_type}/{node_id}?workspace_id=` | 返回一个 Workspace-scoped Topic 或 Claim 判别节点。 |
| `GET /api/v1/graph/relations/{relation_id}?workspace_id=` | 返回 Relation 本体、确认/有效期和 Evidence count/fingerprint/href，不返回 Evidence 正文。 |
| `GET /api/v1/graph/relations/{relation_id}/evidence?workspace_id=&cursor=&limit=` | 按需分页返回每条 Evidence 自有的 reason、applicability、provenance 和可打开 Source/Span href；默认 20，最大 100。 |
| `GET /api/v1/graph/candidates?workspace_id=&node_type=&node_id=&cursor=&limit=` | 按 Workspace 和可选节点 scope 分页返回可审阅 Candidate；`CLAIM` 是精确端点 scope，`TOPIC` 还包含双方都以正式 `CONFIRMED BELONGS_TO` 归属该 Topic 的 Claim pair；支持状态、关系类型、置信度与重开原因过滤。 |
| `GET /api/v1/graph/candidates/{candidate_id}?workspace_id=` | 返回 Candidate、双方版本摘要、Evidence、决策历史摘要和 Proposal 绑定。 |
| `POST /api/v1/graph/candidates/{candidate_id}/decisions` | 使用 `Idempotency-Key` 与 `expected_version` 执行 Confirm/改类型 Confirm/Ignore/False Positive/Defer/Resume。 |
| `POST /api/v1/graph/candidate-scans` | 接受 TOPIC 或已冻结的 SMART_COLLECTION scope。Smart Collection 必须绑定 collection ID/version/query hash/read-model revision；返回 `202 + workflow_run_id + status_url`，相同 key 精确重放且漂移 fail closed。 |
| `GET /api/v1/graph/candidate-scans/{scan_id}?workspace_id=` | 返回持久进度、计数、checkpoint、终态和安全错误摘要。 |

前三个正式 Graph `POST` 只是为复杂过滤和遍历参数提供结构化请求体，仍是无业务副作用的 Query，不要求
`Idempotency-Key`。四个正式 Graph `GET` 的 `workspace_id` 必填且只能出现一次；未知或重复 query key、未知 JSON
字段、非法 UUID/enum/depth/limit/budget 均明确失败。Global、Neighborhood、Path 响应只携带
Evidence summary/href；前端打开 Relation 详情并明确展开 Evidence 后才请求 Evidence page。

Candidate decision/scan 的 POST 是有副作用命令，必须严格 JSON 并使用 `Idempotency-Key`；跨 Workspace 或不可见
统一 Not Found。Candidate 依赖缺失返回独立 503，`system.status.semantic_links` 与 `graph` 分开报告，不能把
Candidate 故障伪装为 Graph 故障或空候选。

Topic scope 不等于 Workspace 全量，也不依赖 Candidate 的 discovery method 猜测归属。PostgreSQL Repository
使用两侧 Claim 的正式 membership 事实筛选；公共响应只允许直接 Topic 端点或 Claim-pair 形态，前端不得自行
重建 `BELONGS_TO` 事实。

Global、depth=1 Neighborhood 和 Relation Evidence 使用独立的 opaque result-window cursor。Cursor v1
由 API 进程生命周期内的随机密钥进行 HMAC-SHA256 签名，并绑定 query kind、Workspace、规范请求
hash、结果 fingerprint、limit 和 offset：篡改、跨 Workspace/查询、请求参数变化或进程重启返回
`400 GRAPH_CURSOR_INVALID`；结果变化返回 `409 GRAPH_CURSOR_STALE`。Cursor 不是授权凭据或持久
session。

### Collection 与 Knowledge Health

M7-03 的公共契约由 OpenAPI 3.1、`internal/collection/http`、`internal/health/http` 和前端 strict decoder 共同约束：

- Collection：Workspace-scoped list/get/create/update/archive、validate、preview、results。命令要求唯一
  `Idempotency-Key`，update/archive 要求 expected version；results 返回与当前 saved execution 绑定的
  `query_hash`、read-model revision、精确 count、同一有序 refs 和 opaque cursor。
- Health：summary、issues/detail、decision、repair proposal、scan start/status、schedule get/put。Scan 返回
  `202 + health_scan_id + workflow_run_id + status_url`；Issue Evidence 由详情按需加载，Decision/Schedule PUT
  使用幂等键和 CAS。
- 所有 JSON body 拒绝未知/重复字段；query/header 的重复值、非法 UUID/enum/time/limit/cursor/content-type
  明确失败。跨 Workspace 与不存在统一 Not Found；Problem 不回显 Query、SQL、DSN、绝对路径、Evidence 原文
  或 cursor key。
- `system.status.collections` 与 `knowledge_health` 独立于 `graph` 和 `semantic_links`；
  `health.scan.completed` SSE 只触发 Query invalidation，刷新后仍从 REST Scan/Issue 投影恢复。
- Tag、Review/Directory、Review Deck、Artifact 与无真实 apply seam 的 repair 均返回 capability unavailable，
  不能用空结果或假 Proposal 冒充成功。

失败语义保持可区分：

- 不存在、跨 Workspace 或不在正式投影中的节点/Relation 返回相同 404，避免资源枚举；Path 端点不存在
  也返回 404，但在完整预算内证明“没有路径”返回 200 和 `status=not_found`。
- Path 的访问预算耗尽返回 `422 GRAPH_QUERY_BUDGET_EXCEEDED`，不返回未经证明的 partial path；
  Neighborhood 的 node/edge/frontier/result-window 截断通过 200 响应的 `meta.truncated/reason` 暴露，并
  只保留完整层。
- Handler 或 PostgreSQL deadline 超时返回可重试的 `503 GRAPH_QUERY_TIMEOUT`；请求取消返回不可重试的
  `503 GRAPH_QUERY_CANCELLED`；真实 Graph 依赖未组装时路由仍保留并返回
  `503 GRAPH_DEPENDENCY_UNAVAILABLE`。
- 投影不一致返回 `409 GRAPH_PROJECTION_INCONSISTENT`；未知内部错误返回 500。空 Global、空 Evidence
  和 no-path 不伪装成依赖故障。

### Smart Collection Export

M9-03 已交付 Smart Collection 的可恢复异步 Export Job；公开范围只有 `MARKDOWN` 与 `METADATA_JSON`，
输出 schema 为 `export/v1`：

- `POST /api/v1/exports` 必须有唯一 `Idempotency-Key`，请求绑定 Workspace、Collection ID/version、query hash、
  kind、安全字段、脱敏策略和 TTL。首次返回 `202 + Location`，完全相同的 replay 返回原 Job（200）；同 key
  不同请求或当前 Collection binding 冲突返回 `409 EXPORT_IDEMPOTENCY_CONFLICT`。`dispatch_pending` 只说明
  transport 尚待恢复，不是 Job 终态。
- `GET /api/v1/exports/{export_id}?workspace_id=...` 返回严格 Job 投影；
  `GET /api/v1/workspaces/{workspace_id}/exports?collection_id=...&limit=...&cursor=...` 按 Collection 使用
  `created_at,id` 稳定 keyset cursor 恢复历史；两者均不暴露文件路径、staging locator、lease 或 request hash。
- `GET /api/v1/exports/{export_id}/download?workspace_id=...` 只在 `SUCCEEDED` 后返回固定 Content-Type、
  attachment 文件名、Content-Length、`Cache-Control: private, no-store` 与 `X-Content-Type-Options: nosniff`。
  未完成返回 `409 EXPORT_RESULT_NOT_READY`，到期返回 `410 EXPORT_EXPIRED`，hash/size/path 不能证明一致时
  返回 `500 EXPORT_RESULT_INCONSISTENT`，不返回部分文件。
- `PENDING/RUNNING/SUCCEEDED/FAILED/EXPIRED` 是当前生命周期；`CANCELLED` 只读兼容历史。`export.*` Server Event
  使用 `resource_ref=export_job:<id>`，只使前端失效并重新读取 Job/List，不能携带或替代结果事实。
- 默认导出为 `MASKED`；`FULL + include_sensitive` 只允许有 `READ_LOCAL` 的认证 Session/API Token 主体。附件、
  `EVALUATION_JSON`、`AUDIT_JSON`、CSV/XLSX 和字段映射没有 M9-03 公开端点或内容源，不能由客户端模拟。

### Timeline 与 Impact Analysis

M7-04 与遗留收口的公开查询由 `internal/knowledge/http` 提供，正式下游 Proposal 由
`internal/changecontrol/http` 提供。Timeline 是 Workspace-scoped 只读投影，Impact 是显式分析命令：

- `GET /api/v1/workspaces/{workspace_id}/timeline` 支持 `event_type`（可重复）、`aggregate_type`、`aggregate_id`、
  `source_event_ref`、UTC 时间范围、`limit` 和绑定查询条件的 opaque HMAC cursor；详情使用同一 Workspace predicate。
  Cursor 不能跨 Workspace、过滤器或页大小重放，稳定排序为 `(occurred_at DESC, id DESC)`。
- `POST /api/v1/workspaces/{workspace_id}/timeline/{event_id}/impact-analysis` 必须携带唯一
  `Idempotency-Key` 和严格空 JSON 对象；当前策略只在 Artifact selector backfill 完成后创建一份不可变
  `impact-report/v2`，首次返回 `201`，精确重放返回同一报告（`200`）。既有 `impact-report/v1` 保持可读且不可变，
  `GET /api/v1/workspaces/{workspace_id}/impact-reports/{report_id}` 按 ID 返回历史报告。
- `POST /api/v1/workspaces/{workspace_id}/impact-reports/{report_id}/proposals` 接受严格的
  `target_type + target_id + action` 和 `Idempotency-Key`，只允许从最新 READY v2 报告中的精确 Artifact/Review Card
  owner binding 创建 `HIGH` 风险、`impact-downstream-update/v1` 的 `downstream_update` Proposal；首次返回 `201`，
  精确重放返回 `200`。stale report/target、跨 Workspace、unsupported action 或 owner 漂移全部 fail closed。
- Timeline Event 由 Proposal/Approval/Commit/Knowledge/Impact 与 Artifact/Review owner 事务的最小 Outbox 异步投影；
  没有客户端写 Event 的 API。投影故障不会伪装成领域命令失败，也不会自动级联修改下游对象。
- `downstream_update` 批准只记录 decision；所有 Apply/Preflight/Workflow/Write Authorization/Writeback 入口稳定返回
  `409 DOWNSTREAM_UPDATE_APPLY_UNAVAILABLE`，不得创建执行副作用或把 Approval 显示成目标已更新。

### Review Answer

- Due 查询：输入 Workspace、活动且 Deck-bound 的 Review Session 与可选 Deck；输出签发绑定
  Session/Card/Schedule 快照的 opaque `question_ref`。
- Answer 输入：session、card、`question_ref`、answer、rating、idempotency key。
- 输出：score、feedback、schedule。
- Review lifecycle invalidation 每次返回最多 200 张 Card 的 `invalidated_count` 与 `has_more`；调用方在
  `has_more=true` 时使用新 Idempotency-Key 继续，单个 receipt 不保存完整 Card 列表。

## 12. 安全

- 本地模式默认只绑定 localhost；浏览器仍使用 Cookie Session，并检查 Origin/CSRF，不能把回环地址当作身份。
- 自托管模式必须使用 HTTPS 和单用户认证。
- Web UI 使用可轮换、可撤销的 Cookie Session；Cookie 必须采用 HttpOnly、SameSite，并在 HTTPS 下使用 Secure。
- 非浏览器自动化使用可撤销、限 Scope、可过期的 API Token；Token 不进入 URL Query，服务端只保存不可逆摘要或等价安全表示。
- API Token 不替代浏览器 CSRF 防护，也不得获得超出其 Scope 的能力。
- 登录身份与普通 Capability Authorization 只证明调用者可以发起操作；Apply Knowledge/Git Write 仍要求绑定 Proposal、Approval、Change Hash、Target Version 和 Expiry 的一次性 Write Authorization。
- Session/API Token 撤销不追溯改变已完成审计；撤销后所有后续请求必须失败。
- 不从 URL Query 传 Secret。
- 文件下载通过 Object ID。

Smart Collection Export 下载再次验证 Workspace 受控路径、symlink、SHA-256 与 size；通过后才在同一
PostgreSQL 事务中增加下载统计并追加 actor-bound `export.download` Audit。该 Audit 仅说明服务端已准备开始
返回结果，不证明浏览器完整接收。导出和 Problem/Event/日志均不得回显 Secret、绝对路径或 staging locator。

M6-D 当前只完成 Search/Evidence 的 Workspace 数据隔离，并保持 API loopback 部署。正式 Auth、
Session、API Token、CSRF/Origin 与 Capability Middleware 仍属于 M10；在这些门禁落地前不得把
`workspace_id`、回环来源或 Search Cursor 当作已认证身份，也不得将自托管公网入口描述为安全可交付。

M6-03 不暴露通用 `/tools/{name}:execute` HTTP API。Tool 只能由服务端持久 Workflow Node 间接执行；
Agent Tool Request 不能携带 Workspace、Capability、Approval、Credential、path、command 或 Git args。
Conversation/RAG API、SSE 与反馈已由 M6-04 落地；正式 Session/API Token/CSRF/Capability Middleware 仍由 M10 提供。

认证 API 至少提供登录、登出、当前 Session、Session 轮换，以及 API Token 创建、列出元数据和撤销能力。创建 Token 时明文只返回一次；响应和日志不得再次暴露完整 Token。

## 13. API 可观测性

- request_id。
- trace_id。
- workflow_run_id。
- error_code。
- latency。
- response size。

## 14. OpenAPI

- OpenAPI 3.1 JSON 是 API wire 契约事实源；Search、Graph、Source Version、Source Span、Cursor、Evidence、
  Score/Distance、Degradation 和 Problem 必须声明完整 Schema 与 405。
- CI 校验 Breaking Change。
- Generated Client 只在前端边缘，领域模块不依赖。
- `web/src/api/search.ts` 是当前 Search wire 的严格 Decoder/Client 边界；它必须把网络 JSON 当作
  `unknown`，拒绝未知 mode/capability、非有限分数、非法 UUID/时间、缺失 href 和错误 cursor 类型，
  Feature/Component 不得直接断言原始响应。
- `web/src/api/conversation.ts`（Conversation/RAG JSON）与 `web/src/events/**`（SSE fetch-stream）分别是
  M6-04 的唯一严格 wire owner；Feature/Component 不得重复解析，SSE 只能定向失效 Query，最终状态必须回查。
- `web/src/api/graph.ts` 是 Graph 7 个端点的唯一严格 decoder/client 边界；Global/Local/Path 使用
  TanStack Query 持有 Server State，Relation Evidence 只能在用户展开 Relation 详情后按需请求。
- `web/src/api/exports.ts` 是 Collection Export 的唯一严格 decoder/client 边界；它只接受
  `MARKDOWN|METADATA_JSON`，校验 Job 的 Workspace/Collection/version/query hash、生命周期字段、Problem 与下载
  header/Blob，Feature 不得直接断言原始响应或用新标签页绕过认证和 `410` 处理。

## 15. 测试

- Contract Test。
- Version Conflict。
- Idempotency。
- Pagination Stability。
- SSE Reconnect。
- Error Mapping。
- Session Rotation/Revocation。
- CSRF/Origin。
- API Token Scope/Expiry/Revocation。
- 登录授权不能绕过 Approval Write Authorization。
- Export：严格 JSON/Idempotency、同 key replay、Collection-filtered cursor、Job 状态、prepared/lease/TTL crash
  recovery、下载 header/hash/size、actor Audit、过期/cleanup、`export.*` SSE invalidation 与真实 API/Worker/Vite
  浏览器闭环。该门禁仅覆盖 Collection Markdown/Metadata JSON，附件与 AC-33 全量验收保持 deferred。
- Search Handler：严格 JSON、默认值、三种模式、统一过滤、top-100 分页、Cursor 篡改/跨请求/
  stale/重启失效、零命中、显式降级和稳定 Problem 映射。
- 真实 PostgreSQL HTTP：Workspace 隔离、三模式/过滤、Source Version/Span 可打开与 404 防枚举、
  FTS/trigram/三种 vector distance operator 的 `EXPLAIN (FORMAT JSON)` 基线。
- 真实 River fault 与 Compose API smoke：Approved Proposal → Safe Writeback → Reindex → 唯一 Active/
  Completion → Search 命中新正文 → 打开 Evidence。上述 smoke 是 M6-D 归档门禁，不能以单元测试、
  readiness 或内存 Fake 代替。
- `make graph-integration`：真实 PostgreSQL 上从公共 HTTP 执行 Global→Local→Path→Evidence，并验证
  response-loss replay、cursor stale、Workspace 防枚举和 timeout 映射。
- `make graph-smoke`：启动真实 API 进程执行相同最小闭环，验证响应不泄露数据库 URL、绝对路径、
  managed storage 字段或未公开来源正文，并验证 API/fixture 日志不记录 Claim statement、Evidence reason
  或 provenance 正文 canary；成功幂等清理已提交 fixture，失败时保留权限为 0700 的诊断目录。
- `make graph-benchmark`：确定性生成 20,000 Active Topic、100,000 Confirmed IMPACTS Relation 和
  100,000 Relation Evidence 参考拓扑，执行 5 次预热、30 次一跳采样和 Neighborhood/Path/Evidence
  EXPLAIN，p95 门槛为 1.5 秒且每次一跳查询固定 6 条数据库 statement。Mixed Topic/Claim 与
  BELONGS_TO 正确性由 integration/smoke 覆盖；产物默认写入 `tmp/graph-benchmark/` 且文件权限为 0600；
  M10 才在 claim-heavy/mixed 拓扑、500,000 Relation 和正式资源预算下执行最终 P95/FPS 门禁。
