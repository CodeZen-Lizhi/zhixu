# OpenAPI Generator 与 Zod 兼容性研究

## 结论

采用 `typescript-fetch` 作为唯一生成器，使用本地 npm wrapper 固定生成器版本，并在生成前增加
一个确定性、可报告的 OpenAPI 3.1 兼容投影。投影只服务于生成器，不改写权威
`api/openapi/openapi.json`，也不承担业务不变量；高风险联合类型和不可信响应仍由 API 边界的 Zod
Schema 或现有严格 Decoder fail closed。

## 已验证事实

- Context7 的 OpenAPI Generator 文档确认 `typescript-fetch` 支持 `configuration`、自定义
  `fetchApi`/middleware、`useSingleRequestParameter`、`modelPropertyNaming`、`stringEnums` 和
  `withoutRuntimeChecks`。Zod 文档确认 `safeParse` 与 discriminated union 可用于边界 fail closed。
- 本地 npm registry 实测可解析 `@openapitools/openapi-generator-cli@2.40.1` 和 `zod@4.4.3`。生成器
  wrapper 已实测选择 `7.24.0`；不依赖全局安装、远程浮动 `npx` 或 `latest`。
- 直接以当前契约生成会得到单一 `DefaultApi`，原因是 189 个 operation 没有 tag；生成 API 必须先
  建立稳定 tag catalog 并重新启用 Spectral `operation-tags` 规则。
- 当前契约包含字符串、数字、布尔和 null `const`，空对象、开放 map、递归 JSON 和复杂 `oneOf`。
  默认生成的公共模型会出现 `any` 或不兼容的布尔 enum；这些结果不能直接进入前端 Domain UI Model。
- 将字符串/数字 `const` 投影为单值 enum、将布尔 `const` 补充为 `type: boolean`、为开放 JSON 建立
  递归 `JSONValue`、为 `WorkflowTopicOutlineSection` 拆成支持/缺证据的两个命名分支，并以
  `stringEnums=false` 生成后，公共模型不再出现 `any`，且通过仓库严格 TypeScript 选项编译。
- 生成 runtime 在 `exactOptionalPropertyTypes` 下有 5 类模板问题：configuration setter、middleware
  可选回调、RequestInit 的 credentials、错误对象的可选 response，以及 `FetchError.cause` 的
  `override`。这些应由受版本控制的 `runtime.mustache` 小补丁解决，而不是放宽项目编译选项。
- 生成的 `JSONApiResponse.value()` 通过 `Response.json()` 读取普通响应；需要重复键检测或更严格语义
  的关键响应必须使用 Raw response 进入现有 strict JSON parser，再交给 Zod。生成的 SSE 方法不取代
  现有原生 EventSource/流式 Adapter。

## 生成配置基线

建议固定如下选项，并将其写入仓库配置而非命令行散落参数：

```text
generator: typescript-fetch
generatorVersion: 7.24.0
modelPropertyNaming: original
useSingleRequestParameter: true
supportsES6: true
stringEnums: false
withoutRuntimeChecks: true
global-property: apiDocs=false,modelDocs=false,apiTests=false,modelTests=false
type-mappings: Null=null
```

`withoutRuntimeChecks=true` 只关闭生成器自己的浅运行时检查，不能被解释成取消项目边界验证；所有
需要 fail closed 的响应继续由 Adapter/strict JSON/Zod 负责。生成目录需单独 lint 例外，禁止其内部
实现细节的 `any` 泄漏到应用层。

## Normalizer 边界

Normalizer 必须读取权威契约、校验版本和输入结构、按稳定排序输出临时生成输入，并打印变换报告。
它只能执行已列入 manifest 的兼容变换：`const` 的生成器类型补全、开放 JSON 的 `JSONValue` 映射、
`WorkflowTopicOutlineSection` 的命名分支拆分和 null 类型映射。遇到未知 `oneOf`、未登记的开放 schema、
无法安全表达的 boolean schema 或新增变换时直接失败，不能静默丢字段或改业务语义。

权威 OpenAPI 仍由 Spectral、项目 checker、路由盘点和 oasdiff 负责；Normalizer 输出不替代这些门禁，
也不作为第二份可手改的契约事实源。生成结果需要在本地重复生成无 diff，并由 CI 对生成输入和提交产物
执行 drift check。

## Zod 使用边界

首批 Schema 只覆盖 Problem/认证会话、模型设置状态机、Workflow/Proposal、Graph/Timeline/Review/
Interview/Organizing 的高风险联合类型，以及 URL/持久缓存恢复。普通响应若仅需 OpenAPI 已表达的
字段约束，不再维护完整平行 Schema。所有 Schema 使用 `safeParse`，失败统一映射为不含原始正文、Token
或 CSRF 的稳定 `INVALID_RESPONSE`/`INVALID_CACHE` 错误。

## 残余风险

- OpenAPI 3.1 与生成器仍有 beta/`allOf`/`oneOf` 警告；实现阶段必须把警告清单纳入生成 gate，不能
  通过降低规则或类型断言消除。
- 生成 runtime 属于第三方模板，升级时必须重新执行严格编译、API 生成 drift 和关键浏览器 smoke；
  任何模板补丁都要和 generator 版本成对审查。
- 生成器无法表达跨字段 `if/then`、`dependentRequired` 和有限集合语义；这些保持在 OpenAPI 的
  领域 checker、Zod 或 Domain projection 中，并在模块 manifest 标注唯一 owner。
