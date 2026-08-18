# TODO 8：前端 OpenAPI 客户端与 Zod 接入

## Goal

基于已经通过 Spectral、项目契约检查、Gin 路由盘点和 oasdiff 兼容性门禁的 OpenAPI 3.1
契约，为前端建立可确定性再生成的 `typescript-fetch` 客户端，并在不削弱现有认证、安全和严格
运行时校验的前提下，逐步消除重复的手写请求、DTO 与字段解析代码。

用户价值是让 API 字段演进能够在契约检查、生成代码漂移和前端类型检查阶段尽早失败，同时继续在
真正不可信或高风险的数据边界 fail closed，避免把网络响应直接带入 Store 或组件。

## Background

- TODO 7 已交付：`api/openapi/openapi.json` 由锁定版本的 Spectral、项目 checker、Gin runtime
  inventory 和 oasdiff breaking-change gate 保护；TODO 8 只能消费这些门禁通过的契约。
- 路线图与原需求明确指定 OpenAPI Generator `typescript-fetch` 和 Zod，属于已批准技术选型；本任务
  不平行引入另一套客户端生成器或运行时 Schema 框架。
- `web/src/api/` 当前有 26 个生产 API 模块、约 18,947 行代码，包含大量手写 DTO、wire-to-domain
  映射和严格 Decoder。
- OpenAPI 当前有 189 个 operation，全部具有唯一 `operationId`，但没有任何 operation tag，也没有
  顶层 tag catalog；直接生成会失去稳定的领域 API 分组。TODO 7 的研究已把补齐稳定领域 tags 列为
  TODO 8 前置，当前 `.spectral.yaml` 也明确把 `operation-tags` 留给 TODO 8 治理。
- 前端生产请求当前统一经过 `web/src/api/auth.ts` 的 `authFetch`；它负责同源 Cookie、CSRF、
  `Accept`/`Content-Type` 和 401 Session 失效通知。现有 API 模块在其上分别实现网络错误、Problem
  Details、JSON 解析和领域错误映射。
- 前端当前未依赖 Zod；`web/src/shared/codec.ts` 提供 UUID、Record、AbortError 和精确字段集合等
  手写校验原语。
- 生成代码必须与领域 UI Model 隔离；SSE、流式正文、Blob 下载和其他生成器不能完整表达的边界可
  保留最小项目 Adapter。

## Requirements

- R1：锁定 OpenAPI Generator 及 `typescript-fetch` 生成配置、模板行为和升级方式；生成目录不得
  手工修改或承载业务逻辑。
- R1a：为全部 189 个 operation 建立稳定、单一所有者的领域 tag 和顶层 tag catalog，并重新启用
  `operation-tags` 门禁；tag 必须服务于生成客户端的模块边界，不能批量填充无意义占位值。
- R2：提供单一、本地与 CI 一致的生成命令；同一 OpenAPI 和锁定工具链必须得到确定性结果，CI
  必须拒绝契约与已提交生成代码漂移。
- R3：生成客户端通过单一项目 Transport Adapter 接入现有 API base URL、同源 Cookie、API Token、
  CSRF、Workspace、请求 ID、AbortSignal/取消、超时、上传/下载和统一错误映射；不得绕过现有 401
  Session 失效语义。
- R4：生成的 wire 类型和请求客户端不得直接成为组件或 Store 的领域模型；feature API 边界负责
  wire-to-domain 映射、稳定错误状态和 TanStack Query 接入。
- R5：Zod 只用于关键不可信响应、持久缓存恢复和高风险判别联合边界；校验必须 fail closed，错误
  不得包含响应正文、Token、CSRF 或其他敏感数据。普通、已由 OpenAPI 契约保护且无额外不变量的
  响应不得重复维护整套平行 Schema。
- R6：迁移必须按 feature/module 进行；每个模块先证明请求、响应、错误、取消和缓存行为对等，再
  删除对应的手写 Transport、重复 DTO 和字段 Decoder，不允许长期保留两套生产调用路径。
- R7：认证、CSRF、Workspace、Problem Details、请求 ID、取消、超时、文件上传/下载保持兼容；
  SSE 与流式正文继续遵守现有专用边界，不因生成器接入而退回手写帧解析。
- R8：实现期间不得通过放宽 OpenAPI 门禁、类型断言、`any`、静默 fallback 或吞掉解析错误来适配
  生成结果；生成器暴露的契约缺口应修复到 OpenAPI 或显式记录为受控 Adapter 边界。
- R9：工具和运行时依赖进入项目现有 npm manifest/lockfile 与 CI/Makefile 入口，升级可审计且不会
  随执行环境自动漂移。
- R10：本次交付覆盖现有全部 26 个生产 API 模块。每个模块必须完成生成客户端迁移，或经验证被
  归类为 SSE、流式正文、Blob/文件传输等保留专用 Adapter；不得把普通 JSON API 留作未排期后续。

## Acceptance Criteria

- [x] AC1：清理依赖后执行一条受支持命令即可从 `api/openapi/openapi.json` 重建全部生成文件；重复
  执行没有 diff，手改生成文件或修改契约后未再生成会被 CI 稳定拒绝。
- [x] AC1a：189 个 operation 均具有稳定领域 tag，生成 API 不退化为单一 `DefaultApi`；Spectral
  `operation-tags` 重新启用并能拒绝后续遗漏。
- [x] AC2：OpenAPI Generator、模板和 Zod 版本均由仓库 manifest/lock/config 锁定，并有明确升级与
  再生成说明；生成目录中没有人工业务逻辑。
- [x] AC3：代表性安全请求证明 Cookie/API Token、CSRF、Workspace、请求 ID、AbortSignal、超时和 401
  失效语义与迁移前一致；Problem Details 映射为稳定前端错误且不泄露响应正文。
- [x] AC4：普通 JSON、multipart 上传、Blob 下载以及保留的 SSE/流式边界都有明确所有者；生成器不能
  覆盖的能力由最小 Adapter 承担，不复制通用请求逻辑。
- [x] AC5：所有纳入本任务迁移的模块只保留一条生产请求路径，不再重复声明可由 OpenAPI 生成的 wire
  DTO，也不再手写仅做字段改名的解析；确有额外领域不变量的 Decoder/Zod Schema 有边界说明。
- [x] AC6：选定的关键不可信响应和持久缓存恢复在进入 Store/组件前通过 Zod 严格校验；缺字段、未知
  判别值、非法 UUID/时间/范围或超量集合均 fail closed，并映射为稳定、无正文泄露的错误状态。
- [x] AC7：既有前端 lint、typecheck、相关测试、build 和 OpenAPI 契约检查通过；受影响的关键浏览器
  流程证明认证、Workspace 切换、取消和至少一个高风险联合类型没有行为回归。
- [x] AC8：路线图、前端类型安全规范和开发文档记录生成、漂移检查、禁止手改、Adapter/Zod 边界及
  实际迁移范围；只有本任务约定范围全部完成并通过验收后，TODO 8 才标记完成。
- [x] AC9：现有 26 个生产 API 模块逐项进入迁移清单并有最终状态；普通 JSON 请求全部使用生成
  客户端，保留专用 Adapter 的模块逐项记录生成器缺口和最小保留边界，不存在未归类或延期模块。

## Out of Scope

- 替换 Spectral、oasdiff、项目 OpenAPI checker 或批准的 OpenAPI 基线治理流程。
- 用生成类型替代全部领域 UI Model，或为每个普通响应维护与 OpenAPI 重复的完整 Zod Schema。
- 重写 EventSource/SSE 协议、流式 AI 正文协议、TanStack Query 本身或后端业务行为。
- 借本任务改变认证、授权、CSRF、Workspace 隔离、错误文案或产品页面交互语义。
- 在没有行为对等证据时一次性切换全部模块，或长期以 feature flag/双路径维持两套客户端。

## Scope Decision

- 用户已确认本次完成全部迁移。任务采用分批实施和验收以控制风险，但分批不缩小最终范围；所有
  批次完成前，TODO 8 保持未完成。

## Confirmed Technical Constraints

- 生成器兼容性实测记录在 `research/generator-compatibility.md`。当前契约含 OpenAPI 3.1、`const`、
  空对象和递归 JSON 字段，不能直接把默认生成输出当作最终公共类型；必须使用可审计的生成输入
  normalizer、锁定的 runtime template 和严格 TypeScript 编译门禁。
- 计划使用仓库本地 npm wrapper `@openapitools/openapi-generator-cli@2.40.1`，其 generator 版本
  固定为 `7.24.0`；`web` 运行时引入 `zod@4.4.3`。具体版本、升级和回滚方式以研究记录、锁文件
  和后续实现变更为准，不使用全局安装或浮动 `npx`。
- 生成结果放在 API 边界目录并视为只读。普通 JSON 使用生成 API，SSE/流式正文、multipart 和
  Blob 仅由最小 Transport/媒体 Adapter 补足生成器无法表达的行为；这些 Adapter 不能重新实现
  一套通用请求客户端。
- 迁移按模块分批落地，但最终验收必须逐项列出 26 个模块的生产 owner、生成 API、保留 Adapter
  和删除的旧路径；任何普通 JSON 模块都不能以“后续迁移”状态交付。
