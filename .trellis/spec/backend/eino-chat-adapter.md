# Eino Chat Adapter 契约

## 1. Scope / Trigger

修改以下任一范围时必须读取本规范：`internal/platform/models` 的 Chat Adapter/factory/runtime、
`internal/platform/config` 的 Chat 配置、managed model settings overlay、`deploy/compose.yml` 的模型配置、
Eino/OpenAI extension 版本或 vendor 内容。

本规范覆盖 Eino-backed OpenAI-Compatible Chat Adapter，以及阶段 2 的调用级 Callback/Trace telemetry。
Structured Output 短 Graph 由 [`eino-structured-scheduler.md`](./eino-structured-scheduler.md) 单独约束；
Embedding、Retriever、Rerank、ToolsNode、Token Streaming、Checkpoint/Interrupt 和持久 Agent 状态不在本规范范围。

## 2. Signatures

稳定构造与调用入口：

```go
func NewConfiguredChatModel(config.Config, ...ModelTelemetry) (agentapplication.ChatModel, error)
func NewConfiguredModelRuntime(config.Config, ...ModelTelemetry) (*ModelRuntime, error)
func NewOpenAICompatibleChatModel(OpenAIChatOptions) (*OpenAICompatibleChatModel, error)
func NewModelTelemetry(observability.Tracer, observability.Metrics) ModelTelemetry
func NewEinoOpenAIChatModel(OpenAIChatOptions, ...ModelTelemetry) (*EinoOpenAIChatModel, error)
func (model *EinoOpenAIChatModel) Chat(context.Context, agentapplication.ChatRequest) (agentapplication.ChatResponse, error)
func (model *EinoOpenAIChatModel) Contract() ChatContract
```

实现选择器为 `ZHIXU_CHAT_IMPLEMENTATION=direct|eino`，YAML 字段为 `chat_implementation`；缺省值必须是
`direct`。选择器只在 Composition Root/factory 生效，不能进入领域 DTO、HTTP API 或持久记录的模型身份。

## 3. Contracts

- `direct` 与 `eino` 必须实现同一个项目 `ChatModel`/`ChatContract`，产生相同 wire payload、项目响应、usage
  和稳定错误分类；实现选择不是 Provider、Model 或 Adapter version 身份。
- Chat Eino/Provider SDK 类型只允许存在于 `internal/platform/models` Adapter 内。Application、Domain、Workflow、
  Model Run/Call、HTTP API、PostgreSQL 和 River 不得导入这些类型；短 Graph 的 Eino 类型只允许位于
  `internal/agent/adapter/eino`。
- API、Worker 与 model settings connection test 通过同一个 factory/runtime 构造 Chat capability；managed
  settings overlay 必须保留进程级 `ChatImplementation`，不能由数据库 revision 暗改实现。
- BaseURL 目录、以 `/v1` 结尾和完整 `/v1/chat/completions` 三种输入都只能生成一个 chat completions
  suffix，且不得保留 query、fragment、userinfo 或不安全远程 HTTP。
- Eino SDK 内部使用中性模型名，规避底层 SDK 对 OpenAI 历史模型名的本地 denylist；项目 payload modifier
  必须把冻结的真实 `ModelID` 写到 wire body，Provider 回显仍按项目 `ModelVersion` 校验。
- 动态 JSON Schema、max tokens、响应捕获和交叉校验状态必须是单次调用私有状态；共享 Adapter 上禁止保存
  可变 Schema、Tool 列表或 response metadata。
- Eino callback handler 必须由每次 `Chat` 通过调用 Context 的 `callbacks.InitCallbacks` 独立注入，只实现
  `OnStart/OnEnd/OnError`；禁止使用并发不安全的 `AppendGlobalHandlers` 或其他全局可变 handler。
- Callback handler 不读取、修改或序列化 Eino input/output/raw error。只允许发射稳定 `component=eino_chat`、
  项目 Model Call phase、`success|failure|cancelled`、稳定项目 `error_code`、耗时和 Context correlation。
- Trace operation 固定为 `model.chat.generate`；Metrics 固定为 `model.chat.duration_ms` 与
  `model.chat.result_total`。Correlation ID 只进入 Trace，不得成为 Metrics label。
- Callback telemetry 必须复用项目 Tracer/Metrics 和 fail-closed Redactor；Exporter 关闭、返回错误或 panic
  均不得改变 Chat response/error。SDK raw error 只能先映射为项目稳定错误，再作为 code 完成 telemetry。
- Callback 是观测旁路，不创建或更新 Model Run/Call、Audit、Workflow Progress、River 或 PostgreSQL 事实；
  `RecordingChatModel` 继续位于 Eino Adapter 外层。
- 请求先通过项目校验和字节上限。成功响应必须有 JSON Content-Type、合法 UTF-8、单一严格 JSON 文档、
  单 choice、assistant、`finish_reason=stop`、无 refusal/tool call、准确 model echo 和非空且加总一致的 usage。
- Eino SDK 返回的 `RawBody` 可能去掉 JSON 文档首尾空白。wire 层仍严格校验原始字节；跨层摘要只比较
  `bytes.TrimSpace` 后的完整 JSON envelope，不得因此接受尾随第二个 JSON 文档。
- Adapter 不启用 SDK 自动 retry、fallback、stream、tool 或 checkpoint。Credential 只保留在发起认证请求所需
  的私有 Adapter/SDK client，不进入 Runtime contract、日志、错误、`String` 或 `GoString`。

## 4. Validation & Error Matrix

| 情况 | Error kind | 稳定 code | Retryable |
|---|---|---|---|
| 配置/实现选择无效 | `InvalidInput` | `MODEL_CHAT_CONFIG_INVALID` | false |
| capability disabled | `DependencyUnavailable` | `MODEL_CHAT_CAPABILITY_UNAVAILABLE` | false |
| 请求字段或字节上限无效 | 项目现有 request validation kind | `MODEL_CHAT_REQUEST_INVALID` 或既有 Agent code | false |
| 调用方取消 | `NonRetryableFailure` | `MODEL_CHAT_CANCELLED` | false |
| deadline/transport timeout | `RetryableFailure` | `MODEL_CHAT_TIMEOUT` | true |
| Provider 429 | `RetryableFailure` | `MODEL_CHAT_RATE_LIMITED` | true |
| Provider 502/503/504 | `RetryableFailure` | `MODEL_CHAT_PROVIDER_UNAVAILABLE` | true |
| Provider 401/403 | `NonRetryableFailure` | `MODEL_CHAT_UNAUTHORIZED` | false |
| redirect | `NonRetryableFailure` | `MODEL_CHAT_REDIRECT_REJECTED` | false |
| 其他非 2xx Provider 状态 | `NonRetryableFailure` | `MODEL_CHAT_REJECTED` | false |
| 未分类 transport/SDK 失败 | `NonRetryableFailure` | `MODEL_CHAT_REQUEST_FAILED` | false |
| model echo 不一致 | `ConsistencyViolation` | `MODEL_CHAT_RESPONSE_MODEL_MISMATCH` | false |
| 其他响应合同不一致 | `ConsistencyViolation` | `MODEL_CHAT_RESPONSE_INVALID` | false |
| Callback/Tracer/Metrics 失败 | 不改变 Chat 分类 | 不新增业务错误码 | false |

状态和错误体最多读取既定上限，分类错误不得包含 Endpoint、API Key、Authorization、Prompt、Provider 原始错误体
或 SDK 内部对象。

## 5. Good / Base / Bad Cases

| 类别 | 必须覆盖的例子 | 期望 |
|---|---|---|
| Good | direct/Eino 同一消息、Schema、响应和 usage | wire body、`ChatContract`、`ChatResponse` 等价 |
| Good | 目录、`/v1`、完整 endpoint | 都只请求一次正确 `/v1/chat/completions` |
| Good | SDK denylist 中的历史兼容模型名 | wire body 保留真实模型名且两实现都到达 Provider |
| Base | 32 个并发请求使用不同 `INITIAL/REPAIR/REDUCED` Schema | 无串扰，race 通过 |
| Base | 32 个并发 callback 使用不同 correlation/phase | Span 精确隔离，Metric 无高基数 correlation |
| Base | JSON envelope 有首尾空白 | 两实现都接受且响应等价 |
| Bad | 无效 UTF-8、尾随 JSON、重复/未知字段、空或多 choice | fail closed 为响应合同错误 |
| Bad | usage 缺失、全零、加总错误，model/finish/refusal/tool 不符 | fail closed，model mismatch 使用独立 code |
| Bad | oversize、redirect、401、429、5xx、取消、deadline | 有界读取、无内部重试、稳定分类 |
| Bad | transport/Provider 错误包含 canary secret | 对外错误与格式化输出不含 secret |
| Bad | callback input/output/raw error 含 Key、Header、Cookie、Prompt、正文或 Endpoint | telemetry 仅含稳定摘要；Chat 结果不变 |

## 6. Tests Required

- `internal/platform/models/eino_chat_contract_test.go`：双实现 fixture、BaseURL、legacy model、状态码、严格响应、
  request/response 上限、取消/超时、redirect、并发 Schema、错误体上限和脱敏。
- `internal/platform/models/eino_chat_live_test.go`：复用 `ZHIXU_EINO_LIVE_*` 显式 opt-in 配置调用生产 Adapter；
  Provider 回显与请求别名不同时用 `ZHIXU_EINO_LIVE_MODEL_VERSION` 冻结实际版本。
- `internal/platform/models/chat_factory_test.go`：缺省 `direct`、显式 `eino`、未知选择器 fail closed。
- `internal/platform/models/eino_callback_test.go`：start/end/error、Context correlation、并发隔离、取消、
  稳定错误码、敏感字段和 telemetry failure/noop 旁路语义。
- `internal/platform/observability/metrics_test.go`：模型指标 registry、固定 component/phase/result/error labels，
  拒绝未知和高基数 label。
- `internal/platform/config/chat_config_test.go`：YAML/env/default/invalid selector 与安全格式化。
- `internal/modelsettings/runtime/models_test.go`：managed revision 不覆盖进程级实现选择。
- 修改 Adapter 或 SDK 版本后至少运行相关单测、`go test -race ./internal/platform/models`、相关 `go vet`、
  vendor 模式 API/Worker 构建、`go mod tidy -diff`、`make compose-check`、PoC race/vet 和 `git diff --check`。
- 默认切换到 `eino` 前必须使用生产 Adapter 完成真实 OpenAI-Compatible Provider smoke；没有凭据时记录 SKIP，
  不得写成 PASS。

## 7. Wrong vs Correct

| Wrong | Correct |
|---|---|
| 用 `ChatAdapterVersion` 表示 direct/Eino | 使用独立进程级 `ChatImplementation`，模型身份保持不变 |
| 把调用方模型名交给 SDK 预检 | SDK 使用中性内部模型名，项目 payload 写真实 wire 模型名 |
| 在共享 Eino model 上修改 Schema | 每次 `Generate` 使用 request-level option 和私有 call state |
| 相信 SDK 已完整验证响应 | transport 先执行项目严格 wire 校验，再交叉校验 Eino message/usage/raw body |
| 记录 SDK error/raw response 便于排障 | 返回稳定脱敏错误，仅保存项目允许的审计事实 |
| 用 `AppendGlobalHandlers` 注册进程共享 callback | 每次 `Chat` 通过 Context 注入独立 handler |
| Callback 写 Model Call/Audit/Workflow Progress | Callback 只写 Trace/Metrics，事务事实继续由项目 owner 写入 |
| 由 Adapter 自动重试 429/5xx | Adapter 单次调用并分类，Workflow 决定 Node Attempt 重试 |
| 用 Eino Graph/Checkpoint 替换 River/PostgreSQL | Eino 仅为 Adapter/短流程能力，项目持久状态仍是唯一事实源 |
