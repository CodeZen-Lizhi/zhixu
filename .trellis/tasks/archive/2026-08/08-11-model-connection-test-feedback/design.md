# 模型连接测试真实错误反馈设计

## 1. Boundary

本任务增强 `POST /api/v1/settings/models/test`，并让 Chat 正式运行时遵循同一显式接口类型。正式模型调用仍返回稳定项目错误，不向普通业务 API 传播 Provider 原文。

连接测试继续沿用现有链路：

```text
Settings UI draft
  -> Model Settings HTTP test endpoint
  -> Settings Service resolve transient Secret
  -> ConnectionTester
  -> production Chat/Embedding adapter
  -> Provider
```

Adapter 在错误 Cause 中携带“安全诊断”，`foundation.Error` 继续拥有稳定 kind/code/retryable。只有 Model Settings test Handler 读取诊断并写入 Problem `details`；其他调用方不会自动暴露它。

## 2. Safe Diagnostic Contract

测试错误的 `details` 使用固定字段：

```json
{
  "target": "chat",
  "stage": "provider_response",
  "provider_http_status": 401,
  "provider_error_code": "InvalidApiKey",
  "provider_error_type": "authentication_error",
  "provider_message": "Invalid API key",
  "provider_request_id": "req_xxx"
}
```

字段规则：

- `target`：`chat|embedding`，由 Handler 的已验证请求决定。
- `stage`：固定枚举 `request|dns|connect|tls|provider_response|response_read|response_validation|cancelled|timeout`。
- `provider_http_status`：仅真实上游状态，100..599。
- Provider 文本字段：只从受支持 JSON 形态或请求 ID Header 提取，UTF-8、控制字符、长度和 token 规则受限。
- `transport_error`：从解包后的底层安全 Cause 生成，不使用会包含 URL 的 `url.Error.Error()`；长度受限。
- 空字段省略；不返回原始 body、headers、URL 或 stack。

Provider JSON 提取支持常见形态：

- OpenAI-compatible：`error.code|type|message`，顶层或 Header request ID。
- DashScope-compatible：顶层 `code|message|request_id`。

其他 JSON 只保留 HTTP 状态并标记安全解析失败。响应最多读取一个很小的固定诊断窗口，超出后不做部分 JSON 解析。

## 3. Error Ownership

- `internal/platform/models`：拥有 Provider HTTP/传输诊断的采集、分类和脱敏；诊断 Cause 的 `Error()` 本身保持安全。
- `internal/modelsettings/application`：继续只传递 error，不解析 Provider 类型。
- `internal/modelsettings/http`：只通过窄接口 `errors.As` 读取安全诊断，绑定 target，映射为测试专用 Problem details。
- `web/src/api/model-settings.ts`：严格解码测试诊断，不接受任意 JSON details。
- `ModelSettingsPanel`：把诊断展示为可扫描的测试结果，不显示原始 JSON。

Provider 401/403 仍由知序 API 返回 502，避免 `authFetch` 把它解释成浏览器 Session 失效；真实上游状态显示在 details 中。超时继续使用本地 504。

## 4. Probe Shape

- Chat：通过同一 OpenAI-compatible HTTP Adapter 发送固定 plain `user:test`，不带 `response_format` 或 `max_tokens`；2xx 只校验单个 assistant 非空响应。正式 `Chat()` 的结构化 payload、token 预算与严格响应校验完全不变。
- Chat API style：配置值只允许 `chat_completions|responses`。旧 revision、旧 YAML 和未设置环境变量统一规范化为 `chat_completions`，不基于模型名猜测。
- Responses probe：向 `/v1/responses` 发送 `{model,input:"test",store:false}` 的非流式最小请求，显式禁止 Provider 留存测试输入；只接受 `status=completed` 且至少一个 assistant `output_text`。解析时忽略 reasoning/tool 等其他 output item。
- Embedding：固定单输入 `test`，验证响应模型、数量、维度和归一化契约。
- 成功响应不回显生成正文或向量。

`response_validation` 可附带固定 `validation_reason`（如 `finish_reason_length`、`empty_content`、`missing_usage`），只标识 Adapter 分支，不保留或回显 Provider 正文。

## 5. URL Compatibility

Embedding URL 采用与 Chat 相同的版本段规则：

- Base 已以 `/v1` 结尾：追加 `embeddings`。
- Base 已以 `/v1/embeddings` 结尾：保持不变。
- 其他 Base：追加 `/v1/embeddings`。

不增加静默重试或猜测其他 Provider 路径。

Chat 端点同样按所选接口确定：

- `chat_completions`：`/v1/chat/completions`。
- `responses`：`/v1/responses`。
- Base URL 已以所选完整路径结尾时保持不变；以 `/v1` 结尾时只追加资源段；其他 Base URL 追加完整 `/v1/...`。
- Base URL 明确指向另一种完整 Chat 端点时直接拒绝配置或测试，不做路径替换和静默 fallback。

正式 `Chat()` 将既有 system/user 输入和 JSON Schema 映射到所选协议，并把 Provider 响应归一为既有 `ChatResult`。Responses 输出允许 reasoning item，但结构化文本仍需通过现有 JSON Schema/usage/model 校验。

## 6. Security And Compatibility

- 精确凭据脱敏在 Adapter 读取错误字段后、构造诊断前完成；任何以大小写不敏感方式包含 Credential/Bearer 的字段整体丢弃或替换。
- Endpoint 不进入诊断结构；底层 `url.Error` 先 unwrap 再生成摘要。
- 所有详情有独立最大字节数，非法 Unicode、Unicode control/format 字符直接丢弃。
- 测试响应继续 `Cache-Control: no-store`，前端以大小写不敏感方式检查本次 Secret 和 Endpoint 不得出现在错误响应。
- 成功和保存响应保持兼容；失败 Problem 新增可选 details，旧客户端仍可用顶层稳定字段。
- 成功测试结果新增 `api_style`、`endpoint_path`、`latency_ms`；其中路径只能是固定白名单值，不能由 Base URL 派生后回显。
- 新增数据库列使用 `NOT NULL DEFAULT 'chat_completions'` 回填旧行。Secret AAD 沿用既有 revision/provider/base/model/purpose 上下文，不加入 API style，避免旧密文失效；API style 自身由 revision 行约束和审计保护。

## 7. Rollback

回滚时可整体移除安全诊断 Cause、测试 Problem details 和前端展示，恢复原错误映射；Embedding URL 修复可独立保留。双协议功能回滚时先停止写入 `responses`，把相关 revision 显式改回 `chat_completions`，再移除新增列；已有 Secret 无需重加密。
