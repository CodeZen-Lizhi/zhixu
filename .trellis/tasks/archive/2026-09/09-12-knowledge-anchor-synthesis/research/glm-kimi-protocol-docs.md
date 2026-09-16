# Research: GLM 与 Kimi 的官方接口协议

- Query: GLM、Kimi 是否只能使用 OpenAI Chat Completions；核对当前官方 Chat/Responses 证据。
- Scope: external；仅公开官方文档，无凭据、模型请求、代码或服务变更。
- Date: 2026-09-16

## Findings

- **GLM（智谱/Z.ai）**：官方明确提供 Chat Completions；当前核对的 Z.ai API 文档及智谱/Z.ai 完整文档索引未找到公开 Responses 接口声明，因此结论是“Chat 已确认，Responses 尚未确认”，不能写成“绝不支持 Responses”。直接依据：[Chat Completion](https://docs.z.ai/api-reference/llm/chat-completion)，请求示例为 `https://api.z.ai/api/paas/v4/chat/completions`；另检查 [Z.ai 文档索引](https://docs.z.ai/llms.txt)、[智谱文档索引](https://docs.bigmodel.cn/llms.txt)。
- **Kimi**：已经明确同时提供 OpenAI Chat Completions 和 OpenAI Responses，不能说只支持 Chat。官方 [API 概述](https://platform.kimi.ai/docs/api/overview) 的 Protocol Compatibility 表分别列出 `/v1/chat/completions`、`/v1/responses`，另外还有 Anthropic Messages；中文 [Responses API](https://platform.kimi.com/docs/api/responses) 提供 `https://api.moonshot.cn/v1/responses` 与 `client.responses.create(...)` 的实际文档示例，示例模型为 `kimi-k3`。
- 补充核对 [Z.ai 官方 OpenAPI](https://docs.z.ai/openapi.json) 的 paths：与 chat/response 匹配的路径仅 `/paas/v4/chat/completions`。这加强了“当前公开接口文档只确认 Chat”的证据，仍不是未公开能力的否定证明。

### Files / code / specs

- 本次没有生产代码变动；只新增本研究文件。
- 相关既有研究：`research/responses-protocol-scope.md`，记录项目自身目前只实现 Chat 的事实；供应商提供 Responses 不代表项目已支持。
- 相关规范：`.trellis/spec/backend/eino-chat-adapter.md`，其当前协议限制与供应商接口能力应分开陈述。

## Caveats / Not Found

- 搜索引擎旧摘要仍把 Kimi 描述成仅 Chat 兼容；直接打开现行官方页已出现三种协议，以当前页面为准。
- 没有把文档能力外推为所有历史型号、地区、账号、套餐或第三方中转网关都支持。Kimi 此页以 K3 为例，未验证其他型号。
- OpenAI-compatible 不等于 OpenAI 全部参数、Schema、工具和 reasoning 档位完全相同；本次只确认协议存在性。
- 未请求任何模型，不构成用户当前网关可用性证明；DeepSeek 由主会话独立核对，不在本次重复研究范围。
