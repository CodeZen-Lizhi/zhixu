# 模型连接测试真实错误反馈

## Goal

让管理员在模型设置页用当前未保存草稿执行真实、最小、无持久化的 Chat 或 Embedding 探针，并直接看到足以定位问题的 Provider/网络诊断，而不是统一的“Provider 拒绝请求或返回无效响应”。

用户价值：可以区分 API Key、模型名、限流、Provider 参数、DNS、TLS、连接和响应契约问题，不必通过容器日志或开发者工具猜测失败原因。

## Background

- 当前 `POST /api/v1/settings/models/test` 已使用表单草稿和瞬时 Secret 发起真实请求，测试本身不保存设置。
- Chat 使用固定结构化请求，Embedding 使用固定非敏感文本；两者都会经过正式 Adapter。
- `internal/platform/models/chat_http.go` 与 `embedding_http.go` 在非 2xx 时丢弃 Provider 响应体，并把传输根因收敛为通用 Cause。
- `internal/modelsettings/http/handler.go` 的 `writeTestError` 再把 Provider/响应类错误统一映射为通用中文提示。
- 前端 `web/src/api/model-settings.ts` 已支持 Problem `details`，但当前只允许 `current_revision`；设置页只显示 `error.message`。
- 安全契约禁止回显 API Key、Authorization、完整 Endpoint、任意原始模型响应、内部堆栈和不受限正文。
- 已确认 Embedding URL 会对以 `/v1` 结尾的 Base URL 再追加 `/v1/embeddings`，形成 `/v1/v1/embeddings` 假失败。

## Requirements

- R1：Chat 测试使用固定、非敏感、大小有界的最小请求，通过正式 OpenAI-compatible Chat Adapter 调用当前草稿；Responses 探针必须显式发送 `store:false`，不得读取业务对话或持久化测试输入/响应。
- R2：Embedding 测试使用固定字符串 `test`，通过正式 Embedding Adapter 调用当前草稿；不得写入索引或持久化向量。
- R3：Provider 返回非 2xx 时，测试 API 必须在安全结构化详情中保留实际上游 HTTP 状态、可识别的 Provider 错误码、错误类型、错误消息和请求 ID；字段缺失时省略，不伪造。
- R4：DNS、TCP、TLS、超时、取消和响应读取失败必须返回可区分的测试阶段、稳定项目错误码和安全的底层原因摘要；例如当前 TLS `EOF` 不能再只显示为“Provider 拒绝”。
- R5：本地测试 API 保持项目自身 HTTP 语义（Provider 401 不冒充知序 Session 401），通过 `details.provider_http_status` 表达上游状态。
- R6：任何错误路径都不得返回或记录 API Key、Authorization、完整 Endpoint、请求正文、响应原文、HTML 网关页、内部堆栈或超限字段。Provider 字段必须限长、校验 Unicode/控制字符，并对当前 Credential 做精确脱敏。
- R7：不支持或无法安全解析的 Provider 响应返回实际 HTTP 状态及“响应详情无法安全解析”的明确说明，不退回当前模糊文案，也不回显原始正文。
- R8：前端严格解码新的测试诊断 `details`，在对应 Chat/Embedding 测试按钮旁显示阶段、Provider HTTP 状态、错误码、消息、请求 ID 和可重试性；普通保存/冲突错误继续沿用既有展示。
- R9：修复 OpenAI-compatible Embedding Base URL 拼接，使 `.../v1` 与不含版本段的 Base URL 都只生成一个 `/v1/embeddings`。
- R10：成功结果继续显示实际 target/provider/model，明确“草稿测试通过，尚未保存或生效”。
- R11：测试结果与错误继续携带 `Cache-Control: no-store`；测试不改变 desired/active revision、Secret 存储、runtime 或 rollout。
- R12：Chat 配置必须显式选择 OpenAI-compatible 调用接口 `chat_completions|responses`，不得根据 Provider、模型名或响应形态猜测接口；既有配置和静态配置缺省为 `chat_completions`。
- R13：Chat 测试与正式运行必须使用同一已选接口。`chat_completions` 调用 `/v1/chat/completions`，`responses` 调用 `/v1/responses`；Base URL 已包含完整匹配端点时不得重复追加版本段或路径。
- R14：Responses API 测试发送固定最小非流式 `{model,input:"test",store:false}`，允许响应中存在 reasoning/tool 等非 message output，只要求最终 `completed` 且至少一个 assistant `output_text`；不得回显生成正文。
- R15：成功结果返回安全固定的接口类型、端点路径和毫秒耗时，页面明确显示“草稿测试通过，尚未保存或生效”。失败继续返回安全真实上游诊断。
- R16：接口类型进入 desired/active revision、运行时快照和审计摘要，但不得破坏已有加密 Secret 的 AAD 兼容性；迁移必须为既有行回填 `chat_completions`。

## Out of Scope

- 不在本任务中为 Docker 增加代理、VPN/TUN 兼容、通用 HTTP CONNECT 或新的外网 Relay。
- 不修改生产模型调用的重试策略、模型选择、向量索引契约或配置应用流程。
- 不把测试接口做成任意 URL、任意 Header、任意 Body 的通用 API 调试器。
- 不向浏览器返回完整 Provider 原始响应或内部 Go 错误链。
- 不根据模型名称自动选择 Chat Completions 或 Responses API，不做失败后的跨接口静默重试。
- 不在本任务加入流式测试开关；测试保持与当前正式非流式调用一致。

## Acceptance Criteria

- [ ] AC1：模拟 OpenAI-compatible Chat `401` JSON 错误时，页面显示 `Provider HTTP 401`、Provider 错误码/消息/请求 ID；知序测试 API 自身不会触发 Session 401 语义。
- [ ] AC2：模拟 Embedding `400`、`429`、`5xx` JSON 错误时，页面分别显示真实上游状态和可用诊断字段，并保留正确 retryable 分类。
- [ ] AC3：当前已复现的传输 `EOF` 在页面显示为连接/TLS 阶段及 `EOF` 安全摘要，不再显示通用 Provider 拒绝文案。
- [ ] AC4：DNS、timeout、cancel、非 JSON、非法 Unicode、超大正文、错误 Content-Type 和无字段 JSON 均有确定且可区分的安全结果。
- [ ] AC5：安全负测把 API Key、Bearer 值、Endpoint 和 canary 放入 Provider 错误各字段后，API 响应、错误字符串和 UI 均不包含这些值。
- [ ] AC6：Chat/Embedding 探针只发送固定非敏感输入，不保存测试请求、响应或 Secret，不推进任何 revision。
- [ ] AC7：Base URL `https://example.test/compatible-mode/v1` 的 Embedding 请求路径精确为 `/compatible-mode/v1/embeddings`，非 `/v1/v1/embeddings`。
- [ ] AC8：现有模型设置 API 严格解码、冲突回查、Secret keep/replace/clear、成功测试与保存流程无回归。
- [ ] AC9：OpenAPI 描述新的安全诊断字段、字段上限和 `no-store` 契约，生成/漂移检查通过。
- [ ] AC10：受影响 Go/前端测试、前端 typecheck/build、`git diff --check` 和安全审查通过。
- [ ] AC11：界面可明确选择 `Chat Completions` 或 `Responses API`，保存、读取、应用、回滚和 active summary 全链路保持该值；旧 revision 显示 `Chat Completions`。
- [ ] AC12：两种接口的 fixture 均验证精确请求路径和最小探针；Responses fixture 含 reasoning output item 时仍能正确判定成功。
- [ ] AC13：成功反馈显示 `/v1/chat/completions` 或 `/v1/responses` 与非负耗时；不暴露 Base URL、响应正文或 Secret。
- [ ] AC14：正式 Chat Adapter 根据同一配置走对应协议，并把两种响应统一为既有结构化 ChatResult 契约；无跨协议猜测或 fallback。
