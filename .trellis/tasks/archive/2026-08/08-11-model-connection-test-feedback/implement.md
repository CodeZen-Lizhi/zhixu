# 模型连接测试真实错误反馈实施计划

## 1. Adapter 诊断与回归测试

- [x] 先为 Chat/Embedding 添加失败测试：Provider 401/400/429/5xx JSON、请求 ID、非 JSON/超限/非法字段、Credential/Endpoint canary、DNS/TLS/EOF/timeout/cancel。
- [x] 在 `internal/platform/models` 增加仅含安全字段的诊断 Cause 与提取/脱敏 helper。
- [x] 非 2xx 时有界读取并提取真实 Provider 诊断，同时保留现有 foundation kind/code/retryable。
- [x] 传输失败解包 URL/network/TLS Cause，生成不含 URL/Secret 的阶段和摘要。
- [x] 修复 Embedding `/v1` 路径拼接并覆盖 versioned/unversioned/already-complete Base URL。

## 2. Model Settings HTTP 契约

- [x] 扩展 test Handler：只对连接测试读取安全诊断并写入固定 `details`；Provider 状态不覆盖本地认证状态。
- [x] 更新 Handler 测试，覆盖 target 绑定、真实诊断字段、no-store、冲突与 Secret/Endpoint/原始 body 不泄漏。
- [x] 更新 OpenAPI 的测试 Problem schema、描述与字段限制，运行漂移检查。

## 3. Frontend 严格解码与展示

- [x] 扩展 `ModelSettingsApiError` 的测试诊断类型和严格 decoder，保留 `current_revision` 冲突 details。
- [x] 更新 `TestFeedback`，按 target 显示阶段、Provider HTTP、错误码、消息、请求 ID、可重试性；无诊断时显示稳定错误码而非模糊文案。
- [x] 添加 API codec 与组件测试，覆盖真实 Provider 错误、TLS EOF、未知字段、超长字段和 Secret canary。

## 4. Validation And Review

- [x] `gofmt` 受影响 Go 文件。
- [x] 运行受影响 Go 单元测试：`go test ./internal/platform/models ./internal/modelsettings/application ./internal/modelsettings/http`。
- [x] 运行 `npm run test --prefix web -- model-settings ModelSettingsPanel` 或仓库支持的等价定向命令。
- [x] 运行 `npm run typecheck --prefix web`、`npm run build --prefix web`、`make openapi-check`。
- [x] 运行 `git diff --check`。
- [x] 使用 `go-review`、`code-review-and-quality`，并对 Provider 正文/Secret/HTTP details 追加安全审查。
- [x] 重建真实 Compose 页面并复测：Chat 明确显示 `response_validation`；Embedding 使用临时无效 Key 收到真实 Provider 401，页面显示上游状态、错误码、类型、消息与请求 ID。重建 `proxy/firewall` 后原 TLS EOF 未再复现。

Review 追加修复：收到 HTTP 响应后读取正文发生 `unexpected EOF` 时保持 `response_read` 阶段，不再误报为 TLS，并补充回归测试。

真实 Chat 复测追加修复：连接探针改为固定 plain `user:test`，省略 `response_format` 与 `max_tokens`，避免 Qwen thinking token 截断导致假失败；正式 `Chat()` payload/校验不变。响应校验失败新增固定 `validation_reason`，不携带 Provider 正文。

最终浏览器验收：重建 Compose 后，使用已保存但不读取/回显的 DashScope 凭据和 `qwen3.7-plus` 草稿，实际点击“测试对话连接”，页面返回“当前草稿连接测试通过”；测试未保存或应用配置。

## 5. Explicit Chat API Style

- [x] 新增 `chat_completions|responses` 领域枚举、静态配置默认值和数据库迁移；更新 revision repository、运行时快照及集成测试，旧行回填 `chat_completions` 且既有 Secret AAD 保持兼容。
- [x] 为 OpenAI-compatible Chat Adapter 增加 Responses API 请求/响应映射；正式调用和 connection probe 均由显式 API style 选择，不按模型名猜测、不跨接口 fallback。
- [x] 覆盖两种 Base URL 路径形态、最小 probe payload、Responses reasoning output、completed/empty/incomplete/usage/model 校验和安全错误诊断。
- [x] 扩展 TestResult、Handler/OpenAPI，成功时返回固定 `api_style`、`endpoint_path`、`latency_ms`，继续 `no-store` 且不回显正文、URL 或 Secret。
- [x] 前端增加 API 接口选择控件，并贯通严格 decoder、草稿保存、active summary、测试成功反馈和组件测试。
- [x] 运行迁移、Go、前端、OpenAPI、diff 检查和对应 Review；重建服务后浏览器真实验证 DashScope `qwen3.7-plus` 的 Chat Completions（8636ms）与 Responses API（6080ms）均成功，控制台无错误且未保存测试草稿。

Review 追加修复：正式 Responses 请求和连接探针均显式发送 `store:false`，避免 Provider 默认存储业务或测试输入；正式响应要求全部 assistant message 状态为 `completed`。迁移 Down 在存在 `responses` revision 时持有排他锁并以 `55000` 拒绝，避免协议选择静默丢失。

Phase 2.2 Review 追加修复：诊断过滤扩展到 Unicode control/format 字符；前端错误响应以大小写不敏感方式拦截本次 Secret 与 Endpoint；OpenAPI 固定 Chat `api_style`/`endpoint_path` 配对并记录 UTF-8 字节上限；迁移测试断言 guarded Down 后协议列仍存在。

## Risky Files / Rollback Points

- `internal/platform/models/chat_http.go`、`embedding_http.go`：共享生产 Adapter，必须保证普通调用方仍只看到稳定错误字符串。
- `internal/modelsettings/http/handler.go`：系统 Secret 管理边界，details 必须只来自安全诊断。
- `web/src/api/model-settings.ts`：严格 Problem decoder，冲突 details 与测试 details 必须区分。
- `api/openapi/openapi.json`：契约变更需与实现、测试同步。
