# 前端类型安全

> 定义前端契约的编译期和运行时所有权。

## 适用范围

适用于 TypeScript、API/SSE 边界、URL 输入、Form 和 Domain UI Projection。仓库已有 TypeScript 工具链；
M6-D Search 使用不新增依赖的手写严格 Decoder。Generated Client 与通用 Runtime Validation 方案仍需
后续任务统一，不能因此弱化当前 `unknown` 边界。

## 已确认事实

- `docs/architecture/technology-stack.md` 选择 TypeScript。
- `docs/architecture/api-and-events.md` 要求生成 OpenAPI、检查 Breaking Change、Generated Client 仅位于前端边缘，并使用 Problem Details、Cursor Pagination、ETag/Version 和强类型 SSE Envelope。
- `docs/architecture/frontend-architecture.md` 将 Generated/Typed API Client 与 Domain UI Model 分离。
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
rg -n 'OpenAPI|Generated Client|Problem Details|SSE|TypeScript' docs/architecture/api-and-events.md docs/architecture/frontend-architecture.md docs/architecture/technology-stack.md
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
- `web/src/events/**` 是唯一 SSE Envelope、frame、cursor 与恢复 owner；未知或非法 payload 不进入 Feature，
  成功处理事件后才推进 Last-Event-ID。

## Scenario: M7-03 Collection / Health Wire Boundary

### 1. Scope / Trigger

- 修改 Collection/Health API DTO、OpenAPI、strict decoder、query/hash/cursor、Issue/Scan/Decision/Schedule UI 时应用。

### 2. Signatures

- `decodeCollectionList/Detail/Preview/Results` 与 `decodeHealthSummary/Issues/Scan/Decision/Schedule` 必须接收 `unknown`，
  返回领域 UI model 或 `INVALID_RESPONSE`。

### 3. Contracts

- 必须校验 `workspace_id`、资源 ID、`query_hash`、read-model revision、version、cursor、枚举、时间和数组上限；未知/重复字段拒绝。
- Collection result 首项为严格 `TOPIC|CLAIM` union；三视图不得接收 wire DTO 或重复解码。
- Collection list/result 最多 100 项；result 还必须满足 `exact_count >= items.length`、`(object_type,id)` 唯一，
  saved response 的 `collection_id/query_hash` 与请求完全一致。
- Health Issue/Scan/Detector Coverage/Decision/Schedule 状态必须穷尽；unavailable、partial、failed、reopened、deferred 和
  response-loss 不得被转成 empty/succeeded。

### 4. Validation & Error Matrix

| 条件 | 结果 |
|---|---|
| Workspace/route/query hash 漂移 | `INVALID_RESPONSE` |
| cursor invalid/stale | 保留稳定 `ApiError`，由 Query 恢复第一页 |
| unknown enum/field/duplicate key | 拒绝整个响应 |
| unavailable capability | typed unavailable reason，不构造假对象 |

### 5. Good / Base / Bad Cases

- Good：raw JSON 只在 `web/src/api/{collections,health}.ts` 进入 camelCase domain model。
- Base：空数组保持 `[]`；cursor opaque，不解析/持久化内部 payload。
- Bad：组件 `as CollectionResult`、以 `query_hash` 缺失时默认空字符串、或把 `HealthScan.status` 任意 cast 成终态。

### 6. Tests Required

- normal/empty/unknown/duplicate/mismatch/invalid cursor/Problem/Abort decoder；query key、three-view refs、scan recovery、
  Workspace isolation 和 browser smoke。

### 7. Wrong vs Correct

```text
Wrong: `const data = response as HealthScan`。
Correct: unknown -> strict decoder -> exhaustive domain union -> feature projection。
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
