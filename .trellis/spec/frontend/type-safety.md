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
