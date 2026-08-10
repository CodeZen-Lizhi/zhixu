# 前端类型安全

> 定义前端契约的编译期和运行时所有权。

## 适用范围

适用于 TypeScript、API/SSE 边界、URL 输入、Form 和 Domain UI Projection。仓库已有 TypeScript 工具链；
M6-D Search 使用不新增依赖的手写严格 Decoder。Generated Client 与通用 Runtime Validation 方案仍需
后续任务统一，不能因此弱化当前 `unknown` 边界。

## 已确认事实

- `docs/architecture/system-design.md` 选择 TypeScript。
- `docs/architecture/application-contracts.md` 要求生成 OpenAPI、检查 Breaking Change、Generated Client 仅位于前端边缘，并使用 Problem Details、Cursor Pagination、ETag/Version 和强类型 SSE Envelope。
- `docs/architecture/application-contracts.md` 将 Generated/Typed API Client 与 Domain UI Model 分离。
- 当前 Search Decoder 不依赖 Runtime Validation Library；不得在规范中虚构未安装包或 Generator。

## 类型所有权

- Wire Type 由 OpenAPI Client 生成结果拥有，只停留在 API 边界。
- SSE Envelope 和 Event Payload 解码由 Events 边界拥有。
- Domain UI Model 是供 Feature 和 Component 使用的稳定、与传输无关类型。
- Component Props 和 Local Form Type 与 Owner 共置，只有真实公共边界才共享。
- Workflow Status、Error Code 等稳定产品值使用契约派生的穷尽 Union/Enum，不使用自由字符串。

## 运行时校验

编译期类型不能校验 Network、URL、Storage、Upload Metadata 或 SSE 输入；这些值在唯一边界 Owner 校验和归一化前均视为 `unknown`。

- API Decoder 将 Problem Details 和 Wire DTO 映射为强类型结果。
- SSE Decoder 分发前校验 `id`、`type`、`occurred_at`、`workspace_id`、`resource_ref` 和 Payload Shape。
- URL Parser 提供显式默认值，并拒绝或归一化无效 Filter、Cursor 和 Object ID。
- Form 在构造 Command 前校验用户输入，同时保留服务端 Field Error。
- Date/Time 在线路边界保持序列化字符串，再显式格式化显示；不得假设浏览器解析语义。

若后续引入 Runtime Validation Library，必须先写入 Manifest/Lockfile 并通过回归；当前 Search Decoder
使用项目现有 TypeScript 能力实现同等严格边界，不因此引入未批准依赖。

### M6-D Search Decoder 契约

`web/src/api/search.ts` 是 `POST /api/v1/search` 的唯一当前 wire owner：

- Request 只接受 UUID Workspace、trim 后非空且最大 8 KiB Query、`keyword|semantic|hybrid`、
  Source/SourceVersion/path/time filters、opaque cursor 与 `limit=1..100`，并映射为 snake_case JSON。
- Response 从 `unknown` 开始，严格校验顶层与嵌套对象、UUID、64 位小写 Hash、RFC3339、整数范围、
  非有限数、相对 POSIX path、href、nullable stage 和 optional `next_cursor`。
- `requested_mode/effective_mode` 只接受三种已知 mode；Index degradation 只接受 `vector`，Query
  degradation 只接受 `vector|rerank`。未知值必须拒绝，不能默认 Keyword 或忽略。
- Vector stage 类型固定为 `{rank, distance}`。禁止投影为 similarity 或与 lexical/fusion score 共用
  含糊类型；未来 UI 显示解释必须同时保留 Embedding Version/Distance 语义。
- Cursor 是 opaque string 且可能因 API 重启失效；客户端只把 400 invalid/409 stale 转成可重启第一页的
  显式错误，不解析或持久化 Cursor 内部 payload。
- Provenance 必须同时具备 `source_version_href` 与 `source_span_href`；缺失或类型错误时整个响应失败，
  Component 不接收不可打开 Evidence。
- OpenAPI 声明为 array 的响应字段必须始终编码为 JSON 数组；根级 Chunk 的空 `heading_path` 必须是
  `[]` 而不是 `null`，否则严格 Decoder 应拒绝该响应。后端映射与前端测试必须共同覆盖空集合。

## 必须模式

- 启用 M1 工具链支持的最严格 TypeScript 配置，并记录任何例外。
- 对 Workflow、Proposal、Source、Conflict、Health、Review 和 Event State 做穷尽处理。
- ID、Version、Cursor、Hash 和 Event ID 保持 Opaque Value，不转换为展示数字。
- 两个消费者读取同一无类型 Payload 时，先集中归一化。
- 显式 Narrow Error，不假设 Catch Value 是 `Error`。
- Generated File 只读，并可从权威契约复现。
- Search Feature/Component 只消费解码后的 `SearchResponse`；禁止再次访问原始 snake_case DTO。

## 禁止模式

- 应用代码中显式或隐式 `any`。
- Double Assertion、未校验 `as` 或 Non-null Assertion 绕过边界校验。
- 手写重复 Generated API DTO。
- Component 直接 Switch 原始传输字符串且无穷尽 Domain Projection。
- 多个消费者独立解析同一 SSE 或 URL 字段。
- 仅因 Loading 为 false 就假设 Optional Data 存在。
- 手工编辑 Generated Client 输出。

## 验证

M1 前执行：

```bash
rg -n 'OpenAPI|Generated Client|Problem Details|SSE|TypeScript' docs/architecture/application-contracts.md docs/architecture/system-design.md
git diff --check
```

M1 后，Canonical Frontend Gate 必须运行 Generated Client Drift Check、Type Check、Lint、Unit Test 和 Production Build；边界测试覆盖无效 API、SSE、URL 和 Form 输入。

M6-D 还必须运行 Search Decoder 单测，覆盖正常/空结果、三模式/降级、vector distance、非法 UUID/时间/
Hash/非有限分数、未知 mode/capability、缺失 href、空 `heading_path` 数组、错误 cursor/Problem 类型和请求序列化。只有实际命令
结果可声明通过。

## 当前待统一项

OpenAPI Generator、通用 Runtime Validator、Error Narrowing Helper 与跨 Feature Status Union 生成方式仍待
后续任务统一；Manifest 和 Lockfile 证明前不得增加包名或版本。当前 Search 手写 Decoder 是明确边界，
不是允许其他 Feature 复制 DTO/Decoder 的先例。

## M6-04 Conversation/RAG Wire Contract

- `web/src/api/conversation.ts` 是 Conversation、Question、Turn、Answer、Feedback 与 RAG 结果的唯一传输边界；
  Feature/Component 禁止再次解析 snake_case、枚举、UUID、时间或 Problem。
- Answer 必须解码为 `pending|completed|refused|clarification_required` 四态判别联合；状态与 result/result_type、
  citation、retrieval summary 不一致时拒绝整个响应，禁止静默补默认值。
- `current_stage` 只接受六个持久 RAG 阶段或 null；Answer ETag 同时绑定 Answer version、Workflow version 和
  stage，200/304 都必须校验该格式。
- `web/src/events/**` 是唯一 SSE Envelope、原生 MessageEvent、committed cursor 与恢复 owner；浏览器标准实现拥有
  UTF-8、frame、heartbeat 和多行 data 解析，项目边界不重复解析 raw SSE。MessageEvent data 在 JSON.parse 前限长，
  再严格校验 Envelope、`lastEventId === id`、Workspace 和单调性；未知或非法 payload 不进入 Feature，成功处理事件后
  才推进项目 committed Last-Event-ID。

## Scenario: M7-03 Collection / Health Wire Boundary

### 1. Scope / Trigger

- 修改 Collection/Health API DTO、OpenAPI、strict decoder、query/hash/cursor、page/scan revision、
  Issue/Scan/Decision/Schedule UI 时应用。

### 2. Signatures

- `decodeCollectionList/Detail/Preview/Results` 与 `decodeHealthSummary/Issues/Scan/Decision/Schedule` 必须接收 `unknown`，
  返回领域 UI model 或 `INVALID_RESPONSE`。
- `CollectionResultPage` 必须区分 `revisionHash`（页面/cursor）与 `scanRevisionHash`（durable scan binding）；两者都是
  必填 64 位十六进制 hash。

### 3. Contracts

- 必须校验 `workspace_id`、资源 ID、`query_hash`、read-model revision、version、cursor、枚举、时间和数组上限；未知/重复字段拒绝。
- Collection result 首项为严格 `TOPIC|CLAIM` union；三视图不得接收 wire DTO 或重复解码。
- Collection list/result 最多 100 项；result 还必须满足 `exact_count >= items.length`、`(object_type,id)` 唯一，
  saved response 的 `collection_id/query_hash` 与请求完全一致。
- Collection detail 启动 SMART_COLLECTION Health scan 时必须提交 `scanRevisionHash`；页面展示和 next cursor 仍使用
  `revisionHash`。禁止缺字段时互相 fallback，因为 Health hydration 可改变 page revision 而不改变 scan membership。
- Health Issue/Scan/Detector Coverage/Decision/Schedule 状态必须穷尽；unavailable、partial、failed、reopened、deferred 和
  response-loss 不得被转成 empty/succeeded。

### 4. Validation & Error Matrix

| 条件 | 结果 |
|---|---|
| Workspace/route/query hash 漂移 | `INVALID_RESPONSE` |
| `revision_hash` 或 `scan_revision_hash` 缺失、非法或被互相替代 | `INVALID_RESPONSE`；不得启用 Scan |
| cursor invalid/stale | 保留稳定 `ApiError`，由 Query 恢复第一页 |
| unknown enum/field/duplicate key | 拒绝整个响应 |
| unavailable capability | typed unavailable reason，不构造假对象 |

### 5. Good / Base / Bad Cases

- Good：raw JSON 只在 `web/src/api/{collections,health}.ts` 进入 camelCase domain model；Health scan payload 显式使用
  `result.scanRevisionHash`。
- Base：空数组保持 `[]`；cursor opaque，不解析/持久化内部 payload。
- Bad：组件 `as CollectionResult`、以 `query_hash` 缺失时默认空字符串、把 `revisionHash` 当作 scan revision，或把
  `HealthScan.status` 任意 cast 成终态。

### 6. Tests Required

- normal/empty/unknown/duplicate/mismatch/invalid cursor/Problem/Abort decoder；query key、three-view refs、scan recovery、
  Workspace isolation 和 browser smoke。
- 双 revision 回归使用不同 hash fixture，断言 decoder 保留 `scanRevisionHash`、缺字段 fail closed、Health scan payload
  不读取 `revisionHash`；真实浏览器 smoke 必须贯穿 Collection detail → Health scan 接受与跳转。

### 7. Wrong vs Correct

```text
Wrong: `readModelRevision: result.revisionHash`，或 `scan_revision_hash` 缺失时 fallback 到 `revision_hash`。
Correct: unknown -> strict decoder 保留两个必填 hash；页面/cursor 使用 `revisionHash`，durable scan 只使用
`scanRevisionHash`。
```

## Scenario: M7-02 Semantic Link Candidate Wire And UI State

### 1. Scope / Trigger

- 修改 `web/src/api/semantic-links.ts`、Candidate query/mutation、Topic scan、Graph URL 绑定、Candidate panel 或
  system status 时，必须应用本契约。
- Candidate/Scan/Proposal 是独立服务端事实；正式 Graph node/edge model 不得包含 Candidate edge。

### 2. Signatures

- 唯一 wire owner：`web/src/api/semantic-links.ts`；Graph canonical helper 复用 `web/src/api/graph.ts`。
- 公共类型包括 `SemanticLinkCandidate`、`SemanticLinkCandidatePage`、`SemanticLinkScan`、
  `SemanticLinkDecisionResult` 和 typed `knowledge_change` Proposal 判别分支。
- `/graph` URL 可保存 `candidate_scan_id`；query key 必须绑定 Workspace、中心节点 scope、过滤器和 cursor。

### 3. Contracts

- 所有 HTTP success body 从 `unknown` 严格解码；`CLAIM` scope 的 Candidate source/target 必须精确绑定请求节点，
  `TOPIC` scope 只额外接受直接 Topic 端点或 Claim↔Claim 形态，双方正式 membership 由后端 Repository 保证。
  其余 scope 漂移、自环、Relation Type 不兼容、非 canonical 对称端点、未知 enum/字段和非法 Evidence href
  都必须拒绝。
- FAILED Scan 必须带 `{stage,code,retryable}`；非 FAILED Scan 禁止携带 error。System Status 必须独立解码
  `graph` 与 `semantic_links`，Candidate unavailable 不能让正式 Graph 状态消失。
- Start scan 的响应丢失重试必须复用原 mutation variables 和原 Idempotency-Key；已有终态 Scan A 不能让新
  Scan B 的重试退回 A 或重新调用 key factory。
- URL 的 `candidate_scan_id` 只恢复 Candidate scan；它不属于 Graph query identity，不能重置当前详情、锁定节点
  或固定布局。Candidate panel 刷新后从服务端 Scan/Candidate 状态恢复。
- Mutation 只有服务端确认后才显示 Proposal/Decision 成功；error、conflict、pending 和 recovery 各自显式，
  不能用 optimistic local state 冒充正式 Relation。

### 4. Validation & Error Matrix

| Condition | Required result |
|---|---|
| Claim scope 不精确、Topic scope 既非直接端点也非 Claim pair、canonical endpoint 或 Relation compatibility 不匹配 | `INVALID_RESPONSE`，不渲染部分卡片 |
| FAILED scan 缺 error，或非 FAILED scan 带 error | 拒绝整个响应 |
| Start response-loss | 相同 request body + Idempotency-Key 重试，不创建新命令 |
| Candidate service unavailable | Candidate panel 可恢复错误；Graph canvas/query 继续可用 |
| `candidate_scan_id` 变化 | 更新 Candidate 绑定，不重置 Graph selection/layout identity |
| Confirm/Ignore/Defer 冲突 | 保留服务端错误与重新获取入口，不显示假成功 |

### 5. Good / Base / Bad Cases

- Good：严格 decoder 产生 Domain UI model，Scan 刷新恢复，Candidate 卡片展示 Evidence/版本/重开原因；正式
  canvas 只接收 canonical Relation。
- Base：无 Candidate 或 Semantic/RAG unsupported 时展示明确 empty/capability 状态，确定性 Rule 结果仍可审阅。
- Bad：组件本地 cast snake_case、scan 重试生成新 key、把 Candidate 画成正式边、用 scan ID 作为整个 Graph
  workspace identity，或只靠颜色区分候选/正式关系。

### 6. Tests Required

- API decoder：正常/空/未知字段、Claim 精确 scope、Topic Claim-pair scope、自环、Relation compatibility、
  canonical 对称端点、Scan error 判别联合、typed Proposal、Problem 和 system status 独立能力。
- Query/Component：Workspace key、cursor、response-loss 同 key、刷新恢复、mutation invalidation、全部决策、
  focus loop、移动端、候选不进入 GraphCanvas、scan ID 不重置详情/布局。
- Canonical `lint/typecheck/test/build`，并在真实 API 支撑的桌面与 390x844 移动 `/graph` 检查零横向溢出、
  零 console warning/error 和 URL scan 恢复。

### 7. Wrong vs Correct

```text
Wrong: scan POST 失败后调用 start factory 生成新 Idempotency-Key；Candidate scan ID 加入 Graph identity。
Correct: 重试 mutation.variables；scan ID 只恢复 Candidate server state，Graph selection/layout 保持不变。

Wrong: Topic scan 产出 Claim pair 后把 node scope 校验全部关闭，或在浏览器复制 BELONGS_TO 查询规则。
Correct: Claim scope 精确校验 `(type,id)`；Topic scope 只允许直接 Topic/Claim-pair 形态，membership 由后端保证。
```

## Scenario: M9 Business API And Proposal Review Boundary

### 1. Scope / Trigger

- 修改 `web/src/api/business.ts`、Inbox/Proposal/Workflow 详情、current-content、审批或 Workflow 控制时应用。
- Network JSON、路由 ID 和 Active Workspace 在进入 Feature 前必须形成绑定后的 Domain UI Model。

### 2. Signatures

```ts
getProposal(workspaceId, proposalId, signal?)
getProposalCurrentContent(workspaceId, proposalId, signal?)
getWorkflow(workspaceId, workflowId, signal?)
decideProposal(proposalId, { revisionId, changeHash, decision, proposalType })
controlWorkflow(workflowId, action, expectedVersion)
```

- `ProposalCurrentContent` 响应必须包含 `proposal_id + workspace_id + target_path + content + current_hash + base_hash + base_hash_match`。
- Proposal 是 `file_patch | knowledge_change | publish_artifact | downstream_update` 判别联合；组件只能消费 camelCase 联合分支，不能读取 snake_case 或自行猜类型。

### 3. Contracts

- UUID、64 位小写 SHA-256、RFC3339、正安全整数、Page Cursor 长度、状态枚举和数组上下限全部运行时校验。
- `knowledge_change` 固定 `schema_version=knowledge-relation-change/v1`、`operation=CREATE_RELATION`、Target Ref 类型、Node/Relation 枚举和 Evidence/版本数量。
- `downstream_update` 固定 `schema_version=impact-downstream-update/v1`、`risk_level=HIGH`，必须完整冻结 source report/event、target/action、owner binding；approved 分支不得携带 Workflow、Git 或写回字段。
- 详情响应的 `workspace_id` 和资源 ID 必须与 Active Workspace/路由 ID 精确一致；Query Key 包含 Workspace 不能替代响应绑定。
- 首次批准直接调用 Approval 命令。现有 `apply-preflight` 是 approved Proposal 的写回前检查，不是 pre-decision endpoint；不得在 `/approvals` 前调用。
- File Patch 批准仍由服务端 Approval 命令重新校验 Revision、Change Hash、当前文件 Hash 与 Git；UI 的 Diff/current-content 只提供审阅和提前禁用，不是安全授权。
- Proposal 详情内的 Approval snapshot 必须绑定当前 `proposal_id + revision_id + change_hash`；历史 Revision 的决定不得进入当前审阅模型。
- Proposal detail 的 `approval` 是必需 nullable 字段：必须显式存在且只能是 `null` 或合法 Approval object；字段缺失必须 fail closed。Proposal summary 的 `approval` 可省略，但一旦出现就不得为 `null`。
- 新产生的 `file_patch` approved 响应必须完整包含 Git 与 Safe Writeback Workflow 绑定；历史持久快照允许 `workflow_run_id=NULL`。有 Git 基线但无 Workflow 的快照映射为可安全重派发状态；Git 基线也缺失的历史快照只能只读并进入人工恢复。任一半截 Workflow ID/URL、已有 Workflow 却缺 Git、`knowledge_change` approved 或 rejected 携带写回字段时仍必须拒绝整个响应。`proposalType` 只用于在严格网络边界选择对应响应契约，不进入请求 JSON。
- fetch 或响应体读取阶段的 `AbortError` 必须保持原始 identity，供 TanStack Query 正确识别取消；不得包装成可重试 `NETWORK_ERROR` 或无效 JSON。

### 4. Validation & Error Matrix

| Condition | Required result |
|---|---|
| 响应 UUID/hash/time/version/enum/const 非法 | `BusinessApiError(INVALID_RESPONSE)`，不渲染部分事实 |
| Proposal/Workflow/current-content Workspace 或资源 ID 不匹配 | fail closed，详情与写按钮不可用 |
| Knowledge Change 数组为空/超限、Base Version 不等于 2 | 拒绝整个 Proposal |
| Downstream Update binding/schema/risk 不一致，或携带执行字段 | 拒绝整个 Proposal；不显示 Apply 命令 |
| ready Proposal 首次批准 | 调用 `/approvals`；不得先调用 post-approval preflight |
| Approval/Workflow 409 | 保留错误并重新查询，不显示 optimistic success |
| File Patch current hash 漂移 | 禁用批准；服务端命令仍以 409/needs_revision 兜底 |

### 5. Good / Base / Bad Cases

- Good：Active Workspace B 打开 A 的旧详情 URL 时 decoder 拒绝；各 Proposal 分支使用自己的严格 UI，`downstream_update` 可审批但明确不可应用。
- Base：正文读取不可用时仍可驳回，但批准保持禁用；未交付编辑后批准/暂缓/三方合并只显示能力说明。
- Bad：只因 Query Key 含 Workspace 就信任全局详情；组件 `as Proposal`；把 Apply Preflight 放在首次 Approval 前；非法 knowledge payload 仍进入 Relation Diff。

### 6. Tests Required

- API：Workspace/ID 绑定、UUID/hash/RFC3339/整数、状态、cursor、未知字段、Knowledge const/数组边界、Problem/network/Abort。
- Component：首次 file patch 与 knowledge change Approval、不调用 post-approval preflight、Hash 漂移、409 回查、高风险确认、驳回可用和焦点恢复。
- Canonical：M9 ESLint、TypeScript、Vitest、Production Build；全量门禁被其他并行任务阻断时必须列出具体文件和错误，不得把定向通过写成全量通过。

### 7. Wrong vs Correct

```text
Wrong: getProposal(id) 不校验 workspace_id；点击批准先 POST apply-preflight。
Correct: getProposal(expectedWorkspace,id) fail-closed；首次批准直接调用服务端 Approval 原子安全门。

Wrong: 只检查 JSON primitive，接受 schema_version="1"、operation="create"、fingerprint="fp"。
Correct: 按 OpenAPI 校验 knowledge-relation-change/v1、CREATE_RELATION、UUID/hash/版本与数组界限。
```

## Scenario: M9 Export Wire Boundary

### 1. Scope / Trigger

- 修改 Collection Export、Settings 附件导出、`export.*` SSE、OpenAPI 或下载响应时应用。
- `web/src/api/exports.ts` 只拥有 `COLLECTION + MARKDOWN|METADATA_JSON`；
  `web/src/api/attachment-exports.ts` 只拥有 `WORKSPACE_ATTACHMENTS + ATTACHMENTS_ZIP`。两个 wire owner
  不共享可选字段堆叠，也不接受 `EVALUATION_JSON|AUDIT_JSON`。

### 2. Signatures

```ts
createAttachmentExport({ workspaceId, idempotencyKey, expiresInSeconds? })
listAttachmentExports(workspaceId, { cursor?, limit? }, signal?)
getAttachmentExport(workspaceId, exportId, signal?)
downloadAttachmentExport(job, signal?): Promise<Blob>
```

- Attachment create body 固定 `ATTACHMENTS_ZIP`、`attachment-export/v1`、`workspace-attachments/v1`、
  `RAW_USER_OWNED`；不得携带 Collection、fields、redaction 或 include-sensitive 字段。

### 3. Contracts

- 两个客户端都从 `unknown` 严格解码 Create Result、Job、List、Problem 与下载响应；拒绝未知/重复字段、
  非法 UUID/RFC3339/hash/integer/cursor/enum，以及 Workspace/resource/download URL 跨绑定。
- Collection Job 必须绑定 Collection ID/version/query hash、revision/count 和两种公开 kind；附件 Job 必须绑定
  `scope_kind=WORKSPACE_ATTACHMENTS`、root contract、raw policy、manifest hash、entry count/bytes 和 ZIP hash/size。
- Job 穷尽 `PENDING|RUNNING|SUCCEEDED|FAILED|EXPIRED|CANCELLED`，并按 scope 校验状态字段组合；只有成功附件
  Job 能携带 manifest/archive binding 与 download URL。空字符串、缺字段或跨 scope 字段都必须拒绝整个响应。
- 下载只经 `authFetch`，并校验 kind 对应 Content-Type、受控 ASCII filename、Content-Length、
  `private, no-store`、`nosniff` 和 Blob size；非 2xx 先严格解码 Problem，`410` 不得变为空 Blob。
- `AbortError` 保持原 identity。Workspace 切换时迟到的 create/list/get/download 响应不得写入新 Workspace cache。

### 4. Validation & Error Matrix

| 条件 | 必须结果 |
|---|---|
| scope/kind/schema/root/policy 组合漂移 | `AttachmentExportApiError(INVALID_RESPONSE)` |
| success 缺 manifest/archive binding，或非 success 携带下载事实 | 拒绝整个响应 |
| Workspace/Export ID/download URL 不匹配 | fail closed，不显示下载按钮 |
| `400/403/404/409/410/500/503` Problem 字段非法 | `INVALID_RESPONSE`，不构造近似错误 |
| Workspace switch / route unmount | abort 请求和 mutation，迟到回调不能恢复旧 cache |

### 5. Good / Base / Bad Cases

- Good：Settings 只消费严格附件 Job，成功后用受控 Blob 下载并回查服务端统计。
- Base：空列表保持 `[]`；gate 关闭显示 typed unavailable；`FAILED/EXPIRED` 保留历史并允许新建。
- Bad：把附件塞进 Collection `ExportJob` 的 optional 字段、组件拼 download URL、把 `available:false` 当成功，
  或 Workspace A 的迟到 mutation 写入 B 的 cache。

### 6. Tests Required

- Decoder/client：normal/empty/unknown/duplicate、UUID/time/hash/enum/status、scope binding、`200/202`、Problem、
  Abort、headers/Blob size 和跨 Workspace URL。
- Query/Component：Workspace key、cursor、same-key retry、active/terminal polling、SSE invalidation、切换 Abort、
  迟到 mutation 隔离、失败/过期新建和下载后回查。
- 真实浏览器覆盖 Collection 与附件两个入口、刷新/重启恢复、ZIP 下载解包、权限/跨 Workspace/tamper/expiry，
  并验证桌面与 390x844。

### 7. Wrong vs Correct

```text
Wrong: 一个含大量 optional 字段的 ExportJob 同时表示 Collection 和附件，未知 kind 用默认值继续渲染。
Correct: 两个 wire owner 各自解码穷尽 scope；任何跨 scope 字段、未知 kind 或 binding 漂移都拒绝整个响应。
```

## Scenario: M7 Timeline / Impact Wire Boundary

### 1. Scope / Trigger

- 修改 `web/src/api/timeline.ts`、Timeline/Impact Query、页面、路由或 downstream Proposal 创建时应用。
- `web/src/api/timeline.ts` 是 Event、Impact Report/Object 与创建响应的唯一 wire owner；组件不得解析 raw JSON。

### 2. Contracts

- Event 以 `knowledge-event/v1|v2` 判别；只有 v2 接受 operator/owner binding，且 `ARTIFACT_GENERATED`、
  `REVIEW_CARD_INVALIDATED` 必须是 v2 并携带对应 owner snapshot。
- Report 以 `impact-report/v1|v2` 判别；v2 固定 `impact-analysis/v2` 并严格解码 supersession、Artifact/Review
  binding。Workspace、路由 ID、source event/report/target binding 任一漂移都拒绝整个响应。
- 创建响应只接受正式 `downstream_update`、`HIGH` risk、`impact-downstream-update/v1` 和合法 replay/status 组合；
  UUID、RFC3339、正整数、64 位小写 hash、枚举、nullable/optional 与未知字段全部运行时校验。
- 只有服务端结构化 correlation 或已知 owner 类型可产生导航；Commit、Evidence、Review Card 等无正式 detail owner
  的引用只显示类型化文本，不从 `source_ref` 猜 URL。

### 3. Tests Required

- 覆盖 v1/v2 normal/empty、未知/重复字段、非法 binding/hash/time/version/enum、Workspace/route mismatch、
  cursor/Problem/Abort/network unknown，以及 downstream create/replay/status/risk/schema 组合。
- `INVALID_RESPONSE` 不渲染部分事实；网络未知重试必须复用原 mutation variables 与 Idempotency-Key。
