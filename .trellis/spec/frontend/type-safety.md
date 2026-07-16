# 前端类型安全

> 定义前端契约的编译期和运行时所有权。

## 适用范围

适用于 TypeScript、API/SSE 边界、URL 输入、Form 和 Domain UI Projection。仓库当前没有 TypeScript 配置、Generated Client 或 Runtime Validation 依赖；M1 必须建立并验证这些细节，不得弱化以下边界。

## 已确认事实

- `docs/architecture/technology-stack.md` 选择 TypeScript。
- `docs/architecture/api-and-events.md` 要求生成 OpenAPI、检查 Breaking Change、Generated Client 仅位于前端边缘，并使用 Problem Details、Cursor Pagination、ETag/Version 和强类型 SSE Envelope。
- `docs/architecture/frontend-architecture.md` 将 Generated/Typed API Client 与 Domain UI Model 分离。
- 当前仓库未指定 Runtime Validation Library、TypeScript Strict Flag 或 Client Generator，不得在规范中虚构。

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

M1 必须先选择并锁定 Runtime Validation Library，代码才能使用。本规范要求行为，不指定当前不存在的包。

## 必须模式

- 启用 M1 工具链支持的最严格 TypeScript 配置，并记录任何例外。
- 对 Workflow、Proposal、Source、Conflict、Health、Review 和 Event State 做穷尽处理。
- ID、Version、Cursor、Hash 和 Event ID 保持 Opaque Value，不转换为展示数字。
- 两个消费者读取同一无类型 Payload 时，先集中归一化。
- 显式 Narrow Error，不假设 Catch Value 是 `Error`。
- Generated File 只读，并可从权威契约复现。

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

## M1 待代码验证

M1 必须记录 TypeScript Compiler Option、OpenAPI Generator、Runtime Validator、Error Narrowing Helper、Status Union 生成方式和代表性归一化代码；Manifest 和 Lockfile 证明前不得增加包名或版本。
