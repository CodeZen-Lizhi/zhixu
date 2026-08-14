# Eino Embedding 正式生产替换设计

## 1. 生产调用链

```text
Config / Managed Model Settings
              |
              v
NewConfiguredEmbedder (fixed=eino)
              |
              v
Project Eino Adapter
              |
              v
bounded validating RoundTripper
              |
              v
eino-ext OpenAI / Ollama Embedder
              |
              v
project ValidateEmbedResult
```

生产不存在“Eino 失败后同请求自动 direct”的路径，也不存在 AI implementation selector。需要恢复历史版本时通过 Git 发布记录回到兼容制品；`RAG_REAL_PROVIDER_TRANSPORT=direct` 仅表示网络传输方式，仍与 host-relay 保留。

## 2. Wire Validation

Eino ext 负责构造请求、Provider client 和 component lifecycle。项目 RoundTripper 复用既有安全 transport，并在 SDK 消费响应前：

- 校验 status、Content-Type、最大响应字节和单个 JSON 值；
- 解析最小 wire envelope，捕获响应 model；
- OpenAI 校验每个 index 唯一且完整，按 index 得到权威顺序；
- Ollama 校验响应数量与 wire 顺序；
- 将原始响应重新封装给 SDK，避免私有字段进入领域层。

Adapter 调用 `EmbedStrings` 后把 SDK `float64` 转为项目 `float32`，与 wire 捕获的向量逐项核对，再以配置模型构造 `EmbedResult` 并进入现有领域校验。任何不一致都归类为 consistency violation。项目不实现向量推理、Provider 请求协议或 SDK 重试。

## 3. HTTP And Errors

- Eino `HTTPClient` 使用复制后的项目 client，Transport 包装现有 safe transport，禁止 redirect 的策略保持不变。
- provider 非 2xx、超时、取消、网络错误沿用现有 error code 与 retryability。
- wrapper 错误不包含 URL、API key、输入文本或响应正文。
- 并发请求的 wire capture 跟随单次 request，不保存到共享 adapter 字段。

## 4. Compatibility And Removal

- root: `github.com/cloudwego/eino v0.9.13`。
- OpenAI/Ollama ext: `v0.0.0-20260803030130-90a15623ddb6`。
- Go MVS 选择 root core v0.9.13；通过编译和 contract tests 验证接口兼容。
- ext 升级属于契约变更，必须重跑全部 Provider suite。
- 旧 direct 仅作为历史迁移证据，不进入当前部署；删除它不改变 EmbeddingContract、ConfigHash 或索引版本。
