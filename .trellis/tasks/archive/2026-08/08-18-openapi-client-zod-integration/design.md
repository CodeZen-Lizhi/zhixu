# TODO 8 技术设计

## 1. 目标架构

数据流统一为：

```text
OpenAPI 3.1
  -> contract gates
  -> deterministic generator normalizer
  -> generated wire models + tagged typescript-fetch APIs
  -> Transport Adapter (base URL, auth, CSRF, workspace, abort, timeout, errors)
  -> feature API owner (wire-to-domain, optional strict JSON/Zod)
  -> TanStack Query / Event Store
  -> Feature / Component
```

`api/openapi/openapi.json` 是 HTTP wire 的唯一事实源；`web/src/api/generated/` 是只读生成产物；
`web/src/api/<feature>.ts` 仍是每个模块唯一的请求、错误和 domain projection owner。生成 wire 类型
不得从 feature、store 或组件导入。现有 `web/src/events/**` 继续拥有原生 EventSource、SSE 重连、
流式正文和事件状态，不把帧解析复制到生成客户端。

## 2. 契约与生成层

1. 为 189 个 operation 建立显式 operation-to-tag manifest 和顶层 tag catalog。catalog 以领域所有权
   划分，候选集合为 `Auth`、`Workspaces`、`System`、`Health`、`ModelSettings`、`Captures`、
   `Authoring`、`DocumentHistory`、`SourceSpans`、`Organizing`、`GitSync`、`Business`、
   `BusinessRevisions`、`Conversation`、`Timeline`、`Search`、`Graph`、`Collections`、
   `SemanticLinks`、`Review`、`Interview`、`Memory`、`Artifacts`、`Exports`、
   `AttachmentExports` 和 `Events`。每个 operation 必须恰好一个 tag；实际归属以 manifest 和
   后端路径/模块 owner 审查为准，不允许生成器自动猜测。
2. 在 `api/openapi` 增加锁定的 generator wrapper、配置、normalizer、runtime template 和
   `openapi-generate`/`openapi-generate-check` 入口。权威契约先执行现有四层 gate，再由 normalizer
   生成临时输入。输出目录清空后重建，CI 在干净工作树中比较无 diff。
3. 配置使用研究记录中的 `typescript-fetch` 参数。生成 runtime 只保留必要的小补丁以通过
   `exactOptionalPropertyTypes`/`noImplicitOverride`；补丁文件必须有 generator 版本说明和升级检查。
4. 生成 API 使用按 tag 分组的 class。生产 owner 统一调用 `.Raw()`，把 HTTP status/header 与
   不可信 body 留给项目边界；feature API owner 再执行既有严格 JSON 读取、领域 binding，以及
   被风险清单选中的 Zod `safeParse`。

## 3. Transport Adapter

新增单一共享 Transport 层，生成 `Configuration.fetchApi` 指向该层：

- 解析同源 API base URL，默认 `credentials: include`，保留现有 Cookie/CSRF/API Token 条件。
- 复用现有 CSRF、Workspace 和 401 Session invalidation 逻辑；所有 unsafe request 的 header、
  `Idempotency-Key`、ETag/版本和 OpenAPI 声明的 request id 由生成参数或 `initOverrides` 传入。
- 将 `AbortSignal`、超时、上传 FormData、下载 Blob、204 空响应和网络错误保持为明确分支；不把
  AbortError 转成普通服务失败。
- 统一识别 `Problem Details`，输出稳定的前端错误 code/status/requestId，日志和测试错误序列化
  绝不包含响应正文、Token、CSRF 或未经允许的 header。
- 仅在 Transport 这一层负责通用 fetch 行为。各模块只负责 operation 参数、wire-to-domain、
  额外不变量和 Query key，禁止再次包装一套 `fetch`/`authFetch`。

## 4. Zod 与领域边界

Zod Schema 的 owner 与风险边界绑定：

- Zod 必须验证共享 Problem 与 Session/API Token 等认证边界，拒绝未知字段、非法 UUID/RFC3339、
  重复 scope 和超量集合，且校验错误不保留原始响应。
- Model Settings、Workflow/Proposal、Graph/Timeline/Review/Interview/Organizing 等模块已经拥有完整的
  strict decoder 和大量领域 fixture；这些 owner 继续校验判别联合、跨字段 binding 与集合上限，不为
  同一合同再平行复制完整 Zod schema。
- OpenAPI 已完整表达且无额外领域不变量的普通 wire 仍通过模块轻量 projection；生成类型只提供
  编译期输入，不直接证明网络响应可信。
- 所有校验失败都 fail closed，转换为稳定错误，不把 `unknown` 或部分对象写入 Query cache、Store
  或组件。跨字段 `if/then`、`dependentRequired` 等约束由已登记的 Decoder/Zod owner 维护。
- Domain UI Model 保持已有命名、派生状态和错误语义；生成 wire 的 snake_case、cursor、Problem
  和原始时间格式不向 UI 泄漏。

## 5. 媒体与专用边界

| 类型 | 生成客户端责任 | 保留项目 Adapter |
| --- | --- | --- |
| 普通 JSON | operation、参数、wire response | 仅 domain projection/必要 Zod |
| multipart capture upload | FormData operation 与声明 header | CSRF/进度/错误映射的薄层 |
| Markdown/JSON export download | 请求和状态码 | Blob/文本 content disposition 与安全文件名 |
| ZIP attachment download | 请求和状态码 | Blob、大小上限、文件名和下载触发 |
| SSE / answer draft stream | 不作为 Feature 调用生成方法 | 现有 EventSource/fetch stream、重连、帧和 Abort |
| 204 revoke | Void response | 仅把无正文成功映射为领域结果 |

## 6. 全量迁移分批

迁移以“先共用基础设施、后按边界逐批切换”为顺序；每批完成自身行为对等和定向门禁后才能进入
下一批。最终必须删除旧的普通 JSON 请求路径。

| 批次 | 模块 | 主要风险/保留边界 |
| --- | --- | --- |
| A 基础与安全 | `auth.ts`、`active-workspace.ts`、`workspace.ts`、`system-status.ts` | Cookie、CSRF、401、Workspace 单一事实源、Problem |
| B 采集与编写 | `captures.ts`、`authoring.ts`、`document-history.ts`、`source-spans.ts` | multipart upload、严格正文/证据映射 |
| C 知识发现 | `search.ts`、`graph.ts`、`semantic-links.ts`、`collections.ts`、`health.ts` | 高风险联合、cursor、URL/cache、Evidence lazy boundary |
| D 工作流与变更 | `business.ts`、`business-revisions.ts`、`conversation.ts`、`timeline.ts` | 状态机、SSE invalidation、版本/幂等 |
| E 运维与配置 | `organizing.ts`、`git-sync.ts`、`model-settings.ts` | 任务状态、secret 生命周期、重试和冲突 |
| F 学习与输出 | `review.ts`、`interview.ts`、`memory.ts`、`artifacts.ts`、`exports.ts`、`attachment-exports.ts` | 判别联合、Blob/Markdown/ZIP、最小媒体 Adapter |

以上 26 个文件覆盖当前 `web/src/api` 的全部生产模块。`web/src/events/server-events.ts` 和
`web/src/events/answer-draft-stream.ts` 不属于遗漏模块，而是 R7 明确保留的 SSE/流式 owner；最终
清单仍要记录其与生成 operation 的对应关系。

## 7. 兼容、回滚与失败策略

- 每批先以现有 API 测试和受控 fixture 记录请求 headers、body、错误、取消、缓存和用户可观察结果，
  再替换实现；对比失败停止该批，不以双写或静默 fallback 继续。
- 生成切换按模块文件提交，generated 产物、manifest、lock、模板和模块代码一起变更，便于回滚到
  上一个完整批次。不得提交半生成、不可编译或普通 JSON 双路径状态。
- 若 generator 新版本改变 wire 类型，先回滚 generator/template/normalizer 组合并修复契约或
  显式 Adapter；禁止用 `as`、`any`、放宽 tsconfig 或吞异常恢复绿灯。
- 失败错误保留 HTTP status、稳定 code 和 requestId；敏感响应内容仅在内存中用于必要解析，不进入
  日志、Query key、Storage、URL、DOM 或测试快照。

## 8. 完成定义

完成必须同时满足 PRD 的 AC1-AC9：生成可复现、189 个 operation 有 tag、26 个生产模块全部归属、
普通 JSON 仅一条生成路径、专用 Adapter 有清单、Zod 高风险边界 fail closed、前端/OpenAPI/浏览器
门禁通过，并更新 roadmap、类型安全规范和开发文档。否则任务仍为 planning/in progress。
