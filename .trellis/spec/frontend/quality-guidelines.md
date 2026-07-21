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
- 浏览器验证真实 API、scan URL 恢复、system status、桌面/移动 overflow 与 console；后端 integration/fault/eval 仍必需。

### 7. Wrong vs Correct

```text
Wrong: Candidate 面板测试通过就宣称完整 Graph 交付，或 scan ID 变化时重建整个 workspace cache boundary。
Correct: 前端全量门禁加真实 API 浏览器；scan ID 只影响 Candidate server state。
```
