# Eino Embedding Adapter 契约

## 1. Scope / Trigger

修改以下任一范围时必须读取本规范：`internal/platform/models` 的 Embedding Adapter/factory、
`internal/platform/config` 的 Embedding 配置、managed model settings overlay、`deploy/compose.yml` 的模型配置、
Eino OpenAI/Ollama Embedding extension、Ollama SDK 或 vendor 内容。

本规范覆盖 OpenAI-Compatible 与 Ollama 两个 Provider 的 Eino 实现、项目 wire 校验和发布质量门禁。
Retriever、Rerank、完整 RAG Graph、Token Streaming、ToolsNode、Checkpoint/Interrupt 和持久 Agent 状态不在本规范范围。

## 2. Signatures

稳定构造与调用入口：

```go
func NewConfiguredEmbedder(config.Config) (retrievalapplication.Embedder, error)
func NewEinoOpenAICompatibleEmbedder(OpenAIEmbeddingOptions) (*EinoEmbedder, error)
func NewEinoOllamaEmbedder(OllamaEmbeddingOptions) (*EinoEmbedder, error)
func (embedder *EinoEmbedder) Embed(context.Context, retrievalapplication.EmbedRequest) (retrievalapplication.EmbedResult, error)
func (embedder *EinoEmbedder) Contract() retrievaldomain.EmbeddingContract
```

生产实现固定为 Eino；不再提供 `ZHIXU_EMBEDDING_IMPLEMENTATION` 或 `embedding_implementation` 选择器。实现只在 Composition Root/factory 生效，不能进入领域
DTO、HTTP API、Embedding Config Hash 或持久记录的 Provider/Model 身份。

## 3. Contracts

- OpenAI-Compatible 与 Ollama Eino Adapter 必须实现同一个项目 `Embedder`/`EmbeddingContract`，
  保持请求、向量顺序、归一化和稳定错误分类一致。
- Eino/Provider SDK 类型只允许存在于 `internal/platform/models`。Application、Domain、Retrieval Workflow、
  HTTP API、PostgreSQL 和 River 不得导入这些类型。
- API、Worker 与 model settings connection test 通过同一个 Configured Embedder Factory 构造 capability；managed
  settings overlay 不得引入进程级实现选择。
- 两个 Eino 构造器必须复用项目共享的 Endpoint、Credential、HTTP client 和 `EmbeddingContract` 校验。
  远程 OpenAI-Compatible 只允许 HTTPS；Ollama 仅精确 loopback 可用 HTTP。禁止 redirect，逐连接校验 DNS/IP，
  TLS 不低于 1.2，默认 transport 不使用代理。
- OpenAI-Compatible `BaseURL` 可配置为服务根、带网关前缀的子路径、以 `/v1` 结尾的 API 根或完整
  `/v1/embeddings` endpoint（均允许尾随 `/`）；Adapter 必须保留网关前缀并只产生一次 `/v1/embeddings`。
  Ollama 继续在配置的服务根/子路径下使用唯一 `/api/embed`，完整 endpoint 也必须保持幂等。
- Eino RoundTripper 只通过调用 Context 保存单次请求的 wire 捕获状态，共享 Adapter 上不得保存 response、index
  或向量元数据。并发调用之间不能观察或覆盖对方状态。
- 请求仍先执行项目 batch、单输入和整批字节上限。响应必须是 JSON Content-Type、有界字节和单一可解码
  JSON 文档；状态错误体只做有界读取并映射为项目稳定错误，不能进入 SDK error 或对外文本。
- OpenAI-Compatible 成功响应必须校验 model，并按 `data.index` 重排到输入顺序。缺失、重复、负数或越界 index
  全部 fail closed；不能相信 SDK 返回顺序。
- Ollama Eino 请求必须显式发送 `truncate=false`；响应必须校验 model、向量数量并保留 wire 顺序。构造器拒绝
  Ollama cloud endpoint 与进程级 `OLLAMA_AUTH` 隐式签名，认证不能绕过项目配置和日志边界。
- SDK `float64` 结果必须逐项等于 wire 捕获的 `float32` 向量，再转换为项目结果并执行维度、有限值与配置的
  normalization 校验。SDK/wire 数量、维度或值不一致属于 consistency violation。
- wire 已通过项目校验后，SDK 因额外字段或内部解码失败也属于 consistency violation，不能误报为可重试网络故障。
- Eino Embedding extension 的 callback input/output 包含原始文本与向量；生产代码禁止注册全局 Eino callback
  handler，也不得把带 Eino callback manager 的 Context 传入 Embedder。未来 telemetry 必须先另立脱敏边界。
- Adapter 不启用 SDK retry、fallback 或隐式认证。Credential 只保留在私有 SDK/client，不进入 Runtime contract、
  日志、错误、`String` 或 `GoString`。
- 根模块保留 Eino core `v0.9.13` 的 MVS 结果；OpenAI/Ollama Embedding extension 精确固定为
  `v0.0.0-20260803030130-90a15623ddb6`（commit `90a15623ddb66465aea01fbe8c63ecc9d267acc1`）。
  升级必须同步 `go.mod`、`go.sum`、vendor、license 与全部门禁。

## 4. Validation & Error Matrix

| 情况 | Error kind | 稳定 code | Retryable |
|---|---|---|---|
| 配置/实现选择无效 | `InvalidInput` | `MODEL_EMBEDDING_CONFIG_INVALID` | false |
| 请求为空、超 batch/bytes | `InvalidInput` | `RETRIEVAL_EMBED_REQUEST_INVALID` | false |
| 调用方取消 | `NonRetryableFailure` | `MODEL_EMBEDDING_CANCELLED` | false |
| deadline/transport timeout | `RetryableFailure` | `MODEL_EMBEDDING_TIMEOUT` | true |
| 其他网络失败 | `RetryableFailure` | `MODEL_EMBEDDING_REQUEST_FAILED` | true |
| Provider 429 | `RetryableFailure` | `MODEL_EMBEDDING_RATE_LIMITED` | true |
| Provider 502/503/504 | `RetryableFailure` | `MODEL_EMBEDDING_UNAVAILABLE` | true |
| Provider 401/403 | `NonRetryableFailure` | `MODEL_EMBEDDING_UNAUTHORIZED` | false |
| redirect 或其他非 2xx | `NonRetryableFailure` | `MODEL_EMBEDDING_REJECTED` | false |
| model/index/count/order、JSON、SDK 解码、维度或向量不一致 | `ConsistencyViolation` | `RETRIEVAL_EMBED_RESULT_INVALID` | false |

错误不得包含 Endpoint、API Key、Authorization、输入正文、Provider 原始错误体或 SDK 内部对象。

## 5. Good / Base / Bad Cases

| 类别 | 必须覆盖的例子 | 期望 |
|---|---|---|
| Good | 两个 Provider 的 Eino Adapter 输入与响应 | wire payload、`EmbeddingContract`、向量和 normalization 符合项目合同 |
| Good | OpenAI 返回乱序 `data.index` | 按 index 恢复输入顺序，SDK/wire 结果一致 |
| Good | Ollama 批量返回 | wire 明确 `truncate=false`，model/count/order 一致 |
| Base | 48 个并发请求使用不同向量 | 无 capture 串扰，race 通过 |
| Base | supplied HTTP client 有更短 timeout | 保留调用方 timeout，不被 Adapter 放宽 |
| Bad | OpenAI index 缺失、重复、负数、越界或 model mismatch | fail closed 为结果合同错误 |
| Bad | Ollama model/count 不符，或启用隐式 auth/cloud signing | fail closed，不发送不受控认证请求 |
| Bad | 非 JSON Content-Type、尾随 JSON、oversize、无效向量 | 有界读取并返回结果合同错误 |
| Bad | redirect、401、429、5xx、网络、取消、deadline | 无内部 retry，保持稳定分类 |
| Bad | transport/Provider 错误包含 canary secret | 对外错误与格式化输出不含 secret |

## 6. Tests Required

- `internal/platform/models/embedding_contract_test.go`：两个 Provider 共享 request/status/response/body-limit/timeout/
  cancel/network/redirect 合同，以及安全格式化与敏感错误体。
- `internal/platform/models/eino_embedding_test.go`：OpenAI index/model，Ollama truncate/model/count/order，
  SDK/wire 交叉校验、wire 成功后的 SDK 解码错误分类、并发 capture、隐式 Ollama auth/cloud 拒绝和 supplied client timeout。
- `internal/platform/models/embedding_factory_test.go`：两个 Provider 的 Eino 构造、未知配置 fail closed 与安全格式化。
- `internal/platform/config/embedding_implementation_test.go`：退休选择字段不可用与安全格式化。
- `internal/modelsettings/runtime/models_test.go`：managed revision 不覆盖 Runtime 实现，runtime 只构造一次 Eino Adapter。
- 修改 Adapter 或 SDK 版本后至少运行相关单测、`go test -race`、相关 `go vet`、vendor 模式 API/Worker 构建、
  `go mod tidy -diff`、Compose 配置合同、vendor 一致性和 `git diff --check`。
- 真实 Provider smoke 与发布观察仍是发布验收；离线 fixture 不能替代该门禁，也不得因生产实现固定为 Eino 而宣称该验收已完成。
  2026-08-11 当前树已分别通过外部 HTTPS OpenAI-Compatible Embedding live gate，以及 host-relay 完整
  smoke 使用的原生 Ollama Embedding。host-relay 证据不代表容器直连外部 HTTPS Embedding 网络路径已通过；
  真实稳定观察仍须独立执行。

## 7. Wrong vs Correct

| Wrong | Correct |
|---|---|
| 用 Adapter version 或 Config Hash 表示 Runtime 实现 | 生产固定 Eino，持久模型身份保持不变 |
| 在共享 Adapter 字段保存最后一次响应 | 通过调用 Context 保存请求级 wire capture |
| 相信 OpenAI SDK 返回顺序 | wire 层严格校验并按 `data.index` 重排，再与 SDK 结果交叉核对 |
| 让 Ollama SDK 使用默认 truncate/auth | 显式 `truncate=false`，拒绝 cloud 与隐式签名配置 |
| 只校验 SDK 结果或只信 wire 结果 | 两者逐项一致后再执行项目维度、有限值和 normalization 校验 |
| 记录 SDK error/raw response 便于排障 | 返回稳定脱敏错误，不记录输入、Credential 或原始 Provider body |
| 由 Adapter 自动重试 429/5xx | Adapter 单次调用并分类，由 Workflow 决定重试 Node Attempt |
| Eino Embedding 直接进入检索领域 | Eino 只位于项目 `EmbeddingModel` Port 后的 Infrastructure Adapter |
