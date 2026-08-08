# 前端质量规范

> 定义 React 应用的 Review 和验证门禁。

## 适用范围

适用于前端实现、测试、生成契约和浏览器行为。仓库已有前端 Manifest、Lockfile 与可执行命令；
M6-D 当前交付严格 Search API Decoder/Client，不实现 Search 页面。

## 已确认事实

- `docs/architecture/technology-stack.md` 选择 React、TypeScript、Vite、TanStack Query、React Router、Vitest、Testing Library 和 Playwright，但未锁定版本。
- `docs/architecture/testing-and-evaluation.md` 要求 Component Test、Route Integration、SSE Reconnect、Diff、Graph Accessibility、Virtualized List、Browser E2E、安全测试和性能分析。
- `docs/architecture/frontend-architecture.md` 要求 Diff/Evidence/Status 组件测试、Command → Workflow → SSE 集成、最高层 Seam E2E、键盘/颜色/焦点可访问性、路由拆包和大数据策略。
- 产品失败必须明确暴露，禁止用空页面或表面成功隐藏 Workflow 或受保护写入失败。

## 必须模式

- 改动保持 Feature Scope，并遵守 Routes → Features → Domain UI/API 边界。
- 显式表达 Loading、Empty、Error、Degraded、Waiting for Human、Version Conflict 和 Manual Recovery。
- 使用 Typed API Boundary、稳定 Query Key、Cursor Pagination、Version/ETag 和 Idempotency 契约。
- 清理 Markdown/HTML Preview，并安全显示外部链接目标。
- 对相应 Feature 实施 Route-level Code Splitting、大表虚拟滚动、Graph 增量加载、大 Diff 分段和 Artifact Chapter Lazy Load。
- 测试断言用户可观察行为和可访问语义，不依赖私有实现细节。
- 确定性前端测试使用受控 API/SSE Fixture；真实 AI 行为属于独立 Evaluation Suite。
- Search 网络响应只能由 `web/src/api/search.ts` 从 `unknown` 严格解码；Feature/Component 必须保留
  requested/effective mode、degradation、Evidence href、Cursor 与 vector `distance`，不能静默归一为假成功。
- Active Workspace 只能由 Workspace API 边界严格解码 `GET /api/v1/workspaces/active`；浏览器存储、URL、SSE 和旧 Query
  cache 都不能成为 Workspace 身份事实源。A -> B 必须先 Abort/停止 SSE/清理 A cache 与草稿投影，再发布 B；迟到响应不得回写。

## 禁止模式

- Silent Fallback、Fake Success、吞异常或用通用 Empty State 表达真实失败。
- Route Component 中的业务逻辑、随意类型强转、重复 Transport Decoder 或 Feature 深层导入。
- 无边界 List/Graph 查询、Collection 无分页或长期缓存大段 Source Text。
- Unsafe HTML、Secret 进入 Browser Storage/Log/URL，或由 UI 做授权决策。
- Approval、Diff、Evidence、Workflow、Graph 或 Recovery 仅做 Snapshot Test。
- 测试 Mock 被测单元，或删除目标行为后测试仍能通过。
- 新增依赖或版本但没有锁定 Manifest 和相关回归检查。
- 把 `distance` 改名为 similarity、忽略未知 mode/capability、缺失 href 仍渲染 Evidence，或解析
  HMAC Cursor 内部结构并长期持久化。
- 从 localStorage 恢复 Active Workspace、在浏览器选择宿主机 mount、用旧 Workspace 缓存掩盖 Active API 失败，
  或把删除宿主机控制凭证误实现为删除业务 Auth/CSRF/API Token。

## 测试要求

- Unit Test 覆盖纯 Projection、Reducer、Query Key、Formatter、Validation 和状态转换。
- Component Test 覆盖 Diff、Evidence、Status、Form、键盘、焦点和全部用户可见状态。
- Integration Test 覆盖 Route Loader/Param、Command Accepted、Workflow ID 恢复、SSE Invalidation/Reconnect、Cursor Pagination 和 Version Conflict。
- Playwright E2E 覆盖 `docs/architecture/testing-and-evaluation.md` 定义的六条最高层 Seam，并在适用时使用确定性 Fixture 和 Fake Model。
- Security Test 覆盖 XSS/Markdown Sanitization、客户端可观察的 CSRF/Origin 行为、Secret Redaction 和 Unauthorized Write 展示。
- Performance Check 覆盖大表、Graph Interaction、Route Bundle 和分段 Diff，基于文档容量假设执行。
- M6-D Search Decoder 单测覆盖成功/空结果、请求 snake_case 映射、三模式/显式降级、vector distance、
  UUID/Hash/RFC3339、NaN/Inf、未知 mode/capability、缺失 href、错误 cursor 与 Problem；不得只测 happy path。
- M6-D 至少执行 `npm run lint --prefix web`、`npm run typecheck --prefix web`、
  `npm run test --prefix web` 和 `npm run build --prefix web`；只有实际输出可标记通过。
- Workspace 启动边界测试必须覆盖 strict Active 响应、零个/多个/不匹配、认证顺序、重连、A -> B cache/SSE/草稿清理、
  迟到响应、无控制 fragment/Cookie 的多浏览器访问和 `/workspace` 无宿主路径 mutation。

## Review 清单

- 每类状态是否只有一个 Owner，刷新后是否可恢复？
- API 和 SSE 是否只在边界解码一次？
- Workspace、Cursor、Version、Idempotency 和 Event ID 是否完整保留？
- UI 是否展示真实服务端结果和可操作错误？
- 关键流程是否支持键盘和可见焦点？
- 状态是否不依赖颜色，Diff 是否有文本说明？
- List、Graph Expansion 和 Source Content 是否有边界？
- 测试是否覆盖正常、边界、失败、重连和重复提交？
- 生成产物是否可复现，依赖变更是否锁定？
- Search 是否只有一个 Decoder owner，并拒绝未知字段语义、非有限分数和不可打开 Evidence？
- Cursor invalid/stale 是否显式要求从第一页重启，而不是静默复用旧结果？
- UI 是否没有把 M6-D Workspace 隔离误当作 M10 Auth/CSRF/Capability 已完成？
- Active Workspace 是否只有服务端 API 一个 Owner，切换时是否先清理旧作用域且业务认证保持独立？

## 验证

M1 前执行：

```bash
rg -n 'Vitest|Testing Library|Playwright|Accessibility|SSE Reconnect|Virtualized Lists' docs/architecture/technology-stack.md docs/architecture/testing-and-evaluation.md
git diff --check
```

M1 后运行 Canonical Frontend 的锁定安装验证、Lint、Type Check、Unit/Integration Test、Production Build 和选定 Playwright Smoke；M1 必须记录确切命令，不依赖开发者全局工具。

## 当前后续门禁

Coverage/Bundle Budget、通用 API/SSE Fixture 与 M9 Search 页面 Component/Route/Browser 测试仍待后续任务。
M6-D 只交付 Decoder/Client，不得用其单测声称 Search UI 已完成。

## Scenario: M7-01 Graph Frontend Quality Gate

### 1. Scope / Trigger

- 修改 `web/src/api/graph.ts`、Graph query/query key、URL state、view model/layout、`/graph` 组件或样式时，
  必须应用本门禁。
- Graph 是跨层只读查询 UI；组件不得重新解释 wire、解析 cursor、执行授权决策或引入 Relation 写状态。

### 2. Signatures

- 唯一 wire owner：`web/src/api/graph.ts`；网络 JSON 从 `unknown` 严格解码后才能进入 Feature。
- 路由：`/graph`；模式为 `global|local|path`，节点类型为 `TOPIC|CLAIM`，画布上限为 60 node/100 edge。
- 查询入口对应三条 POST（Global/Neighborhood/Path）和四条 GET（Node Search/Detail、Relation Detail/Evidence）；
  Relation Evidence query 只有详情中显式展开后才 enabled。

### 3. Contracts

- decoder 校验完整 discriminated union、Workspace/请求 binding、稳定顺序、端点闭包、path 连续性、Evidence
  href、有限数值、RFC3339/UUID、数组上限和 Problem；未知字段或语义漂移必须失败，不能丢字段后继续渲染。
- Query Key 必须包含 Workspace 与规范请求；集合顺序、显式默认值和等价 RFC3339 时刻不得制造第二份缓存。
  Global/depth-1 使用 cursor infinite query，depth 2/3 是单个快照，Abort signal 必须传到 fetch。
- Relation Evidence 只在详情显式展开时启用；折叠、切换 Relation/Workspace 或详情卸载时，按
  `workspace + relation` 清除所有 Evidence 分页缓存，但保留 Relation detail，防止重开先回放旧事实。
- URL 只保存可恢复查询状态；非法、重复冲突或跨字段冲突值恢复到明确安全默认。锁定坐标、固定布局、选择和
  drawer 开关不得写入 URL、Browser Storage 或 Server State。
- 超过 60 node/100 edge 或布局不一致时返回完整列表；No Path、Truncated、Timeout/Cancel、Stale、Empty 和
  Network/Decode Error 必须保持不同用户语义。共同 Topic 建议不是 path。
- compact drawer 必须约束焦点、支持 Escape、关闭后恢复触发焦点；节点类型和 Relation 状态不能只靠颜色。

### 4. Validation & Error Matrix

| Condition | Required result |
|---|---|
| 无 Active Workspace | 显示可操作入口且 Graph 网络请求数为 0 |
| 节点搜索不足 2 或超过 256 UTF-8 bytes | query 保持 idle，不发送近似请求 |
| success JSON 未知/缺失字段、顺序或端点不一致 | `GraphApiError(INVALID_RESPONSE)`，不渲染部分事实 |
| cursor stale、timeout/cancel 或请求失败 | 显式错误与重试/从第一页恢复，不显示 Empty/Fake Success |
| Path `not_found` | 显示 explored count 和可选共同 Topic 建议，不生成边 |
| 画布超限或布局损坏 | 强制完整列表 fallback，节点/关系仍可键盘选择 |
| Relation 详情未展开 Evidence | Evidence 请求数为 0；展开后才分页请求 |
| Evidence 折叠后再次展开 | 旧 Evidence 不得先渲染；应重新从第一页请求 |
| 移动 drawer 关闭 | Escape/按钮均关闭，焦点返回原触发控件 |

### 5. Good / Base / Bad Cases

- Good：URL 恢复规范请求，TanStack Query 返回严格 domain UI model；有界画布与完整列表共享选择语义，
  Relation Evidence 只在详情中按需加载，关闭后不会回放旧分页事实。
- Base：没有结果时展示模式对应 Empty；超过视觉上限仍能用完整列表完成检查，不要求浏览器绘制全部边。
- Bad：组件 `as GraphResponse` 强转、在前端解析 HMAC cursor、只渲染前 60 个节点却隐藏其余事实、打开
  Relation 详情即预取全部 Evidence，或用颜色区分 Topic/Claim/STALE。

### 6. Tests Required

- API/Query：7 endpoint strict decoder/encoder、Problem/network/Abort、Workspace query key、规范过滤、
  Global/depth-1 cursor、depth 2/3 snapshot、Search gating、Evidence lazy pagination 和折叠/切换缓存清理。
- Projection/Component/Route：分页去重、60/100 fallback、确定性 Global/Local/Path 布局、端点不被裁切、
  URL round-trip、三模式、锁定/固定布局、详情选择、全部显式状态、键盘和移动 drawer 焦点闭环。
- Canonical 命令：

```bash
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web
npm run build --prefix web
```

- 浏览器必须实际打开真实 API 支撑的 `/graph`，分别以 1440x900 和 390x844 验证 Global/Local/Path、URL
  恢复、列表、固定布局、Evidence lazy load、Escape/焦点恢复、无横向溢出和零 console warning/error。
- 归档还需执行后端 `make graph-integration`、`make graph-smoke`、`make graph-benchmark` 与全仓门禁；前端
  mock 测试不能替代真实 PostgreSQL/API。
- 500,000 Relation 的 FPS/最终交互预算和正式 Auth/CSRF/Capability 仍由 M10 验收。

### 7. Wrong vs Correct

```text
Wrong: 画布布局失败后显示空白；Relation drawer 打开即请求全部 Evidence；移动端只缩小桌面 panel。
Correct: 布局失败切完整列表；Evidence 由用户展开后分页；移动 drawer 约束焦点并在关闭后恢复触发控件。

Wrong: Evidence drawer 收起后保留分页缓存，重开时先显示旧页再后台刷新。
Correct: 详情生命周期结束即按 Workspace/Relation 清除 Evidence 分页缓存，重开只显示新第一页结果。

Wrong: Workspace query key 和 URL 能隔离数据，因此把页面标记为已认证。
Correct: Workspace/URL/cursor 只用于查询绑定；认证、Session、CSRF 与 Capability 等待 M10。
```

## Scenario: M7-03 Collection / Knowledge Health Frontend Quality Gate

### 1. Scope / Trigger

- 修改 `web/src/api/collections.ts`、`health.ts`、Collection/Health feature、Workspace cache boundary、Vite proxy、OpenAPI
  contract 或浏览器 smoke 时执行。

### 2. Signatures

- 唯一 wire owners：Collection 与 Health client/decoder；Feature 只消费 domain UI model。
- Query keys 必须包含 Workspace、canonical request、Collection/version/query hash 或 Scan/Issue identity。

### 3. Contracts

- `query_hash`、read-model revision、cursor、resource ID 和 Workspace 必须互相绑定；未知/重复 JSON 字段和漂移响应拒绝。
- `/collections`、`/collections/:id` 三视图消费同一个 result page；`/health` 的 summary/issues/scan/decision/repair/schedule
  均从 REST 恢复，SSE 只做定向 invalidation。
- URL 只保存可恢复 view/filter/sort/group/selection/scan ID；cursor 不进入 URL/Storage。Workspace 切换清理旧 cache。
- `/collections` 列表必须消费 `next_cursor`；Collection/Workspace/version/query hash 或 Issue filter 变化必须从第一页开始。
- Smart Collection Health Scan 在 `exact_count <= 5000` 时可启动，超过该容量必须禁用并显示具体原因，不能假装已启动。
- Loading、Empty、Invalid、Stale、Partial、Unavailable、Conflict、Retryable Failure 分开显示；移动端无横向溢出、
  长 detector ID 可换行、关键操作支持键盘和焦点恢复。

### 4. Validation & Error Matrix

| Failure | Required result |
|---|---|
| Workspace/resource/query hash mismatch | `INVALID_RESPONSE`，不渲染部分事实 |
| 400 invalid / 409 stale cursor | 明确恢复第一页，不当 Empty |
| unavailable capability | 文本化说明和下一步，不显示假成功 |
| decision/repair/schedule 409 | 保留服务端错误并回查，不 optimistic 成功 |
| mobile 390×844 overflow/console warning | browser gate 失败 |

### 5. Good / Base / Bad Cases

- Good：LIST/TABLE/CARD refs 完全一致，Evidence 按需加载，刷新/Workspace switch 后由 Query cache 恢复正确事实。
- Base：Tag/Review/Directory/Artifact unavailable 显示独立状态；开发 Vite proxy 仅用于同源本地 smoke。
- Bad：组件重新过滤结果、读取 raw snake_case、把 SSE payload 当 Scan 终态或保存 opaque cursor。

### 6. Tests Required

- decoder/query key/URL/cache/component tests；lint/typecheck/test/build；真实 API desktop 1440×900 与 mobile 390×844 smoke，
  zero console warning/error、zero horizontal overflow、focus/dialog recovery。

### 7. Wrong vs Correct

```text
Wrong: Collection 三视图各自请求/过滤，Health 通过本地 state 直接显示 scan succeeded。
Correct: 三视图只投影同一 page；SSE 失效 Query 后以 REST Scan/Issue 状态渲染。
```

## Scenario: M7-02 Semantic Link Frontend Quality Gate

### 1. Scope / Trigger

- 修改 Candidate decoder/query/panel、Topic scan、Graph URL/cache boundary 或 Semantic Link system status 时执行。

### 2. Signatures

- `web/src/api/semantic-links.ts`、`semantic-link-queries.ts`、`SemanticLinkCandidatePanel.tsx` 与 `/graph`。
- Canonical 命令：前端 lint、typecheck、全部 test、production build；浏览器桌面与 390x844 移动 smoke。

### 3. Contracts

- Candidate 与 formal Relation 视觉、类型和数据流分离；Candidate 永不进入 GraphCanvas edge。
- Topic scope 的测试数据必须使用真实 Claim↔Claim scan 产物；decoder 只放宽该形态，不能把 node scope 关闭或
  退化为 Workspace 全量 Candidate。
- Vitest 只收集 `src/` 内单元/组件测试，Playwright 只收集 `e2e/` 浏览器 smoke；两套 runner 不得因共享
  `*.spec.ts` 命名互相加载对方的环境前置条件。
- mutation pending/success/conflict/recovery 来自服务端事实，scan response-loss 复用原 Idempotency-Key。
- `candidate_scan_id` 可恢复 scan，但不能重置 Graph detail、selection、locked position 或 fixed layout。
- 当前 Candidate scope 为 Topic 时，恢复的 Scan `scope.topic_id` 必须与当前 Topic 相同；跨 Topic 的旧 scan
  立即隐藏并清除 URL 绑定，不能用 Topic A 的进度驱动 Topic B 的候选刷新。
- status 同时保留 `graph` 与 `semantic_links`，任一未知/缺失字段都由严格 decoder 拒绝。
- `/graph` 位于 `.workbench__main` 内时，桌面画布只能扩展到该主栏左右边界：以父内容宽度和
  `--workbench-content-gutter` 计算补偿，禁止在嵌套页面使用 `100vw` 或其他完整视口宽度，避免 rail
  与主栏 padding 共同造成 document 横向溢出。

### 4. Validation & Error Matrix

| Failure | Required result |
|---|---|
| Candidate/Scan response 语义漂移 | `INVALID_RESPONSE`，不显示部分卡片 |
| Start response-loss | 同 key 重试；factory 不再次调用 |
| Candidate unavailable | panel 显示可恢复错误，Graph 仍可操作 |
| 移动端 dialog/drawer 关闭 | 焦点恢复，页面无横向溢出 |

### 5. Good / Base / Bad Cases

- Good：刷新由 URL+服务端恢复 scan，所有决策可键盘完成，正式图不混入候选边。
- Base：空列表/unsupported capability 显式展示，不是假成功。
- Bad：组件 cast 原始 JSON、错误重试生成新 key、只测桌面、或以颜色单独表达 Candidate 状态。

### 6. Tests Required

- Decoder、Claim 精确 scope、Topic Claim-pair scope、query key/cursor、response-loss、mutation invalidation、
  Graph cache identity、跨 Topic scan 恢复拒绝、全部决策、focus loop。
- 浏览器验证真实 API、scan URL 恢复、system status、桌面/移动 overflow 与 console；断言 document、
  `.graph-page`、`.semantic-link-panel` 的 `scrollWidth` 和实际左右边界，且桌面 `.graph-page` 与
  `.workbench__main` 左右对齐；后端 integration/fault/eval 仍必需。

### 7. Wrong vs Correct

```text
Wrong: Candidate 面板测试通过就宣称完整 Graph 交付，或在 `.workbench__main` 内用 `100vw` 让 Graph 越过 rail。
Correct: 前端全量门禁加真实 API 浏览器；scan ID 只影响 Candidate server state，Graph 宽度相对主栏计算。
```

## Scenario: M9 Export Frontend Quality Gate

### 1. Scope / Trigger

- 修改 `web/src/api/{exports,attachment-exports}.ts`、Collection/Settings Export UI、Workspace cache/SSE、下载、
  OpenAPI 或 `deploy/export-browser-smoke.sh` 时应用。
- 当前门禁覆盖 Collection `MARKDOWN|METADATA_JSON` 和 Workspace `ATTACHMENTS_ZIP`；
  `EVALUATION_JSON|AUDIT_JSON` 继续不得出现在公开 UI/API。

### 2. Signatures

- Collection wire/query/panel 与 Workspace attachment wire/query/panel 分开拥有；共同只复用 `authFetch`、Problem、
  SSE invalidation 和通用 UI primitives。
- 真实门禁：`deploy/export-browser-smoke.sh`，要求显式 `ZHIXU_TEST_DATABASE_URL` 并启动 API、Worker、Vite。

### 3. Contracts

- 网络 JSON 只经对应 strict decoder；组件不得重新解析 wire、生成 download URL 或把 `dispatch_pending` 当终态。
- REST 恢复、2 秒 active polling、terminal stop、same-key response-loss、SSE invalidation 与 Workspace Abort 必须有测试。
- 下载经 `authFetch` 严格校验 header/Blob/binding；只有 `SUCCEEDED` 显示下载，`FAILED|EXPIRED` 提供新建出口。
- Settings 必须展示 `RAW_USER_OWNED`，不得声称二进制已脱敏；控制面不得显示服务器路径或附件完整文件名列表。
- 浏览器 smoke 使用真实二进制/嵌套附件，完成刷新与服务重启恢复、ZIP 解包、manifest/hash/逐字节比对、
  权限/跨 Workspace/unsafe/源变化/tamper/expiry/cleanup，并检查桌面和 390x844 的键盘、焦点、overflow、console/network。

### 4. Validation & Error Matrix

| Failure | Required result |
|---|---|
| strict decoder/binding/header/Blob 失败 | 显式 error，不渲染 partial Job 或假下载 |
| gate unavailable | Settings 显示可恢复 unavailable，Collection Export 不退化 |
| Workspace 切换时请求仍在途 | abort/reset；迟到回调不能写 cache |
| `FAILED|EXPIRED` | 保留历史并允许新 key；不得复用旧 key 创建第二个意图 |
| 390x844 overflow、console warning/error 或异常请求 | browser gate 失败 |

### 5. Good / Base / Bad Cases

- Good：真实 Worker 生成 ZIP，Settings 刷新/重启后恢复并下载，浏览器和 shell 解包验证同一 archive binding。
- Base：空附件目录生成 zero-entry ZIP；无历史任务展示 Empty；gate 关闭展示 unavailable。
- Bad：mock fetch 截图代替真实服务、直链下载、旧 Workspace mutation 写 cache、或 `available:false` 文件冒充未交付 kind。

### 6. Tests Required

- API：strict JSON、scope/kind/status/time/hash/binding、Problem、Abort、下载 header/Blob size。
- Query/Component：Workspace cache/Abort/late callback、cursor、same-key retry、polling/SSE、历史状态、下载错误和 a11y。
- Canonical：`npm run lint --prefix web`、`npm run typecheck --prefix web`、`npm run test --prefix web`、
  `npm run build --prefix web`、`make openapi-check`、`git diff --check`。
- Dynamic：`ZHIXU_TEST_DATABASE_URL="$ZHIXU_TEST_DATABASE_URL" bash deploy/export-browser-smoke.sh`。

### 7. Wrong vs Correct

```text
Wrong: 只跑 API decoder 单测或只打开 Settings，就声明附件导出与 AC-33 完成。
Correct: 真实 PostgreSQL/API/Worker/Vite 生成并恢复 ZIP，浏览器下载后解包/hash，cleanup 后源附件保持不变。
```

## Scenario: M8 Review, Interview And Memory Frontend Boundary

### 1. Scope / Trigger

- 修改 `web/src/api/review.ts`、`web/src/features/review/**`、`web/src/api/interview.ts`、
  `web/src/features/interview/**`、`web/src/api/memory.ts`、`web/src/features/memory/**`、相关路由、Workspace cache
  或 SSE Event Store 时应用。
- 当前范围为 Review Deck/Session、Review/Interview 共享 Learning Path 和 Memory 生命周期；各客户端均以 REST 读取为事实源，
  `review.*`、`learning_path.*`、`interview.*`、`memory.*` SSE 只做当前 Workspace 的 Query invalidation。
- Review Path 客户端、Query 和 `/review/session?answer=...` UI 已落地；后端在数据库依赖可用时组装真实 Service/Handler，
  readiness 同时要求 Review 与 Learning Path 可用，持久化契约和 Worker maintenance 调用已静态收口。页面仍必须把实际
  503/Problem 显示为可恢复错误；未取得真实 PostgreSQL/API/Vite/浏览器证据时，不能把静态接线描述为动态闭环已验证。

### 2. Signatures

```ts
listReviewDecks(workspaceId, signal?)
listReviewDue(workspaceId, sessionId, deckId?, signal?)
submitReviewAnswer({ workspaceId, sessionId, cardId, questionRef, userAnswer, rating, idempotencyKey })
getReviewLearningPath(workspaceId, answerId, signal?)
createReviewLearningPath({ workspaceId, answerId, idempotencyKey })
updateReviewLearningPathStatus({ workspaceId, answerId, expectedVersion, status, idempotencyKey })
updateReviewLearningPathStep({ workspaceId, answerId, stepId, expectedVersion, status, idempotencyKey })
listMemories({ workspaceId, types?, statuses?, cursor?, limit? }, signal?)
createMemoryCandidate({ workspaceId, type, content, taskScopeId?, expiresAt?, idempotencyKey })
confirmMemory({ workspaceId, memoryId, expectedVersion, idempotencyKey })
startInterview({ workspaceId, config, idempotencyKey })
submitInterviewTurn({ workspaceId, sessionId, questionId, userAnswer, idempotencyKey })
completeInterview({ workspaceId, sessionId, manualEnd, idempotencyKey })
suggestInterviewMemoryCandidate({ workspaceId, sessionId, pathId, stepId, idempotencyKey })
updateLearningPathStep({ workspaceId, pathId, stepId, expectedVersion, status, idempotencyKey })
```

### 3. Contracts

- `review.ts`、`interview.ts` 和 `memory.ts` 是唯一 HTTP/JSON decoder owner；组件只消费 camelCase domain model。
- Due 请求必须携带活动 Review `session_id`；Query key 同时绑定 Workspace、Session 与 Deck。
  Due Card 是脱敏投影，只允许问题、类型、难度、状态和版本。`answer_points`、完整 Card `evidence` 或任何未知字段出现时必须拒绝响应。
- 浏览器提交 Review 只包含 answer、rating、Card/Session binding 与 Idempotency-Key；Score、答案要点、证据和 Schedule
  只能从服务端 Answer result 读取，`scorer_version` 是必需的不可变审计字段。重试必须复用同一个 mutation variables 与 key；
  未提交的 `question_ref` 因 key 轮换、不同 key 实例或 local `disabled` 随机 key 的 API 重启失效时必须重新读取 due，
  不能在浏览器生成或持久化签名；显式/派生 key 的同 key 重启不要求主动丢弃 due。
- Review Path 创建请求只能携带 Workspace、Answer ID 与 Idempotency-Key；gap、Score、Evidence、Citation 和 Artifact binding
  全部由服务端从不可变 Answer 派生。Decoder 必须严格要求 `origin_type=REVIEW`、`artifact.kind=LEARNING_PATH`，并校验
  Workspace/Answer/Path/Step、时间、版本、状态和唯一 step_no；Query key 固定为
  `['review',workspaceId,'answers',answerId,'learning-path']`。
- `/review/session?answer=...` 只能恢复该 Answer 的 Path；创建条件提示可以基于已返回 Score 决定是否显示按钮，但最终
  actionable 判定和创建裁决属于服务端。Path/Step mutation 必须复用原 variables/key 处理 response loss，并在成功后更新、
  失效同一精确 Query key。Step 响应可包含初始态 `PENDING`，但 mutation target 只能是
  `IN_PROGRESS|COMPLETED|SKIPPED`；API 类型和 OpenAPI 不得公开必然失败的 `PENDING` 写入。
- Memory 响应可以只读展示 source type/ref，但任何命令都不得提交 `owner`、`confirmed_by` 或 source type/ref，编辑和
  状态转换也不能修改 provenance。用户 HTTP 创建固定为 USER candidate；只有 `confirm` 能将 Candidate 变为 ACTIVE，
  INTERVIEW provenance 只能由服务端从 Path 步骤派生。EPISODIC 必须有到期时间，
  表单 JSON 也必须用 API 边界的严格 parser，拒绝重复 key、数组根、空对象、超深或超限对象。content 以 UTF-8 bytes
  限制为 4096，source ref 为 512 bytes；可选 `task_scope_id`/`expires_at` 省略合法，但显式 `null` 必须拒绝。
- Interview 只能走独立 Session/Question/Turn/Report/Path wire；前端不得把它映射为 Review Answer、FSRS Schedule 或
  Artifact 可见性状态。Artifact binding、Path step/status 与完成报告必须同时通过严格 decoder 与 Workspace/ID binding。
- Interview 难度由服务端受证据约束的 prompt policy 决定；页面只展示配置。剩余时间必须从服务端 `started_at` 和
  `duration_minutes` 恢复计算，归零后禁答但保留 `manual_end=true` 完成入口。连续追问按服务端 question chain 渲染；
  SKIPPED 主问题链生成的 gap/path 不能被本地去掉或改写。
- Interview Candidate 按原客户端 key 提交；`replayed=true` 既可能是同 key 重试，也可能是另一个 key 对同一 Path step 的
  语义复用。UI 只表达“该步骤已有待确认候选”，不得误称为本次 key 的精确重放。
- Query key 固定以 Workspace 开头，切换 Workspace 时取消并清除 Review/Interview/Memory 缓存，并由 REST 回查恢复事实。
- `AuthProvider` 返回 `authenticated + disabled` 时没有可绑定的用户主体；Memory 与 Interview 路由必须在功能组件挂载前
  显示明确不可用状态，不能先执行 Query/Mutation 再把服务端 `403` 当成普通页面错误。`required + authenticated` 行为保持不变。

### 4. Validation & Error Matrix

| Condition | Required result |
| --- | --- |
| Due 响应携带答案要点、证据或未知字段 | `ReviewApiError(INVALID_RESPONSE)`，答题前不渲染内容 |
| Scorer 不可用、响应丢失或 409 | 显示服务端错误；可重试时复用原 key，不能本地推进 Schedule |
| key 轮换、不同 key 实例或 local 随机 key 重启后旧 `question_ref` 失效 | 丢弃旧 due 投影并刷新；不得本地构造替代签名 |
| Memory 响应包含 owner/confirmed_by 或 Workspace/ID 漂移 | `MemoryApiError(INVALID_RESPONSE)`，不渲染部分事实 |
| Memory 命令携带 source/owner/confirmed_by | `MemoryApiError(INVALID_REQUEST)`，浏览器不发起可伪造或修改 provenance 的命令 |
| EPISODIC 无 expires_at、ACTIVE/PAUSED 无 confirmed_at | 严格 decoder 拒绝生命周期矛盾 |
| content/source 超 UTF-8 byte 上限，或可选非 null 字段显式为 `null` | `MemoryApiError(INVALID_REQUEST/INVALID_RESPONSE)`，不发出或不渲染部分数据 |
| Interview wire 与 Session/Question/Path binding 不一致，或包含 Review Schedule | `InterviewApiError(INVALID_RESPONSE)`，不渲染或本地推进学习状态 |
| Interview deadline 归零 | 禁止新 Submit，允许使用稳定 key 的 manual completion；刷新后仍从服务端时间事实恢复 |
| Interview Candidate 返回 `replayed=true` | 显示既有候选可前往确认；不推断一定是同 key 重放，不再创建本地 Candidate |
| Review Path 响应 origin 不是 REVIEW、Answer/Path/Step binding 漂移、Artifact 不是 LEARNING_PATH 或含未知字段 | `ReviewApiError(INVALID_RESPONSE)`；不渲染部分 Path |
| Review Path 创建表单尝试提交 gap/Score/Evidence/Artifact | API client 不提供这些字段；请求仅发送 `workspace_id`，不让浏览器成为来源事实 owner |
| Review Path 后端 503、Problem 或持久化不可用 | 显示可恢复错误并保留原 mutation variables；不得本地生成 Path 或把路由存在解释为成功 |
| 无 Active Workspace | 不发 Review/Interview/Memory 请求，显示可操作的 Workspace gate |
| development `disabled` 打开 Memory、Interview 或 Interview Session 深链 | 保留原 URL 和单一页面标题；功能组件不挂载，受保护 Query/Mutation 请求数为 0 |
| 390x844 横向溢出或 console warning/error | 浏览器门禁失败 |

### 5. Good / Base / Bad Cases

- Good：Review 提交后才显示五维评分、遗漏、错误和可打开证据；服务端 Path 可用时按 Answer 恢复并推进共享 Path；Interview
  逐题恢复且不触碰 FSRS；Memory Candidate 只在 Confirm 后成为 ACTIVE。
- Base：没有 due Card 或匹配 Memory 时展示明确空态；认证/服务不可用显示可恢复错误而非成功。
- Bad：组件直接 `JSON.parse` Memory 内容并断言为命令类型、在答题前预取完整 Card、从 SSE payload 拼 Score/Path、
  浏览器自行构造 Review gap/Evidence，或将 owner 放进请求/列表投影。

### 6. Tests Required

- API：严格 JSON、未知/重复字段、Workspace/resource binding、脱敏 Due Card、Review Answer/Path/Step 与 REVIEW origin、
  Interview Session/Question/Path、Problem/Abort、EPISODIC 生命周期、Memory 身份字段响应拒绝、source 响应只读解码与
  命令 provenance 字段拒绝。
- Query/Component：Workspace key/cache cleanup、same-key response-loss retry、SSE invalidation/recovery、Candidate Confirm、
  pause/resume/delete、Review answer result、Answer URL Path 恢复/创建条件/状态与步骤、`learning_path.*` 精确失效、Interview
  完成与 Path 状态、Loading/Empty/Error/Conflict；路由测试还必须证明 development `disabled` 下三条身份型深链不挂载功能组件。
- Canonical：`npm run lint --prefix web`、`npm run typecheck --prefix web`、`npm run test --prefix web`、
  `npm run build --prefix web`、`git diff --check`；真实 Vite 在桌面和 `390x844` 检查路由、Workspace gate、overflow
  与 console。具有可用 Workspace 时，还必须完成真实 Candidate Confirm、一次 Review answer 路径和一次 Interview/Path
  恢复路径。

### 7. Wrong vs Correct

```text
Wrong: Due endpoint 返回完整 Card，组件仅把答案区 CSS 隐藏。
Correct: decoder 只接受最小 Due 投影；包含答案要点或证据即 fail closed。

Wrong: Memory 表单用 JSON.parse 后 as MemoryJsonObject 直接发请求。
Correct: 表单调用 memory API 导出的严格 parser；解析和请求使用同一大小、深度与重复字段约束。

Wrong: 浏览器发送 `source.type=INTERVIEW`，或把 Interview Turn 提交给 Review Answer 端点。
Correct: 任何用户 Memory 命令都不含 provenance；INTERVIEW 来源由服务端持久 Path 步骤派生，Interview 与 Review 只共享受控读取合同。

Wrong: 浏览器把评分转换成 gap/Evidence 后创建 Review Path，或收到 503 后本地伪造成功 Path。
Correct: 客户端只提交 Workspace/Answer/key；严格解码服务端 REVIEW origin Path，失败保持显式且用 REST 重试恢复。

Wrong: development `disabled` 仍挂载 Memory/Interview 页面，等待受保护接口返回 403 后再显示错误。
Correct: 路由壳先检查认证模式；没有用户主体时直接显示不可用状态，只有 `required + authenticated` 才挂载数据 Hook。
```

## Scenario: M7 Timeline / Impact Quality Gate

### 1. Required Coverage

- API tests 覆盖 Event/Report v1/v2、Artifact/Review binding、supersession、strict unknown/duplicate fields、
  Workspace/resource mismatch、Problem、Abort 与 network unknown。
- Query tests 覆盖 Workspace-bound keys、canonical filters/cursor、cache cleanup、Idempotency-Key response-loss retry、
  Report/Event/Proposal 精确失效，并证明 Impact 成功不会竞速失效异步 projector 的 Timeline list。
- Component/route tests 覆盖 `/timeline`、`/timeline/:eventId` 的 loading、empty、404、409、503、invalid response、
  unknown result、重试、分页、筛选、只读引用、正式 Proposal 导航与 approved-unavailable 表达。
- Canonical gate 为 `npm run lint --prefix web`、`npm run typecheck --prefix web`、Timeline 定向 Vitest、
  `npm run build --prefix web` 与 `git diff --check`。发布前用真实 API/Worker/Vite 在桌面和 `390x844` 检查
  无横向溢出、文字遮挡、console error 或失败请求。

### 2. Prohibited Shortcuts

- 不用 system status、空数组、disabled 控件或本地对象冒充 Timeline/Impact/Proposal 成功。
- 不从任意 `source_ref` 猜路由，不把原始 SSE 到达当成 Timeline projector 已完成。
- 不把 `downstream_update` 的 Approval 显示成 Apply；UI 隐藏命令不能替代服务端 fail-closed guard。
