# 通用模型设置与协议选择建议

2026-09-16。用户要求先调研，再判断本项目是否需要 Responses；明确撤回“设置必须同时支持两种”的预设。本文仅记录研究和建议，不批准或实施新增协议、自动探测、网关转换或依赖升级。

## 对本项目的判断

- 当前业务需求没有强制依赖 Responses。文件简介/标签、主笔记融合、当前全文补源审查、检索工具和流式问答都有既有 Chat Completions 路径。状态、知识检索、溯源与发布由项目管理，不依赖 OpenAI 的托管 file search 或远端 conversation。
- 建议保留 Chat Completions 作为当前通用兼容基础。只有选定模型/网关只支持 Responses，或所需具体能力限定该协议时，才有必要补充 Responses 适配。并非所有模型任意选择一种都可用。
- “通用设置”是统一连接配置和功能能力，上层业务通过统一接口使用模型；不代表每家服务的请求字段都完全相同。应由已验证的 Provider/SDK 适配处理差异，不能要求用户手写格式转换。
- 本次不建议仅为猜测接口而增加自动探测或失败后自动换协议。任何未来接入方案都需明确能力、适配范围和验证结果，再决定设置里要暴露哪些选项。

## 官方文档观察

| 对象 | 已确认的事实 | 限制 |
| --- | --- | --- |
| OpenAI | 继续支持 Chat Completions，推荐新 OpenAI 集成使用 Responses；GPT-6 Astra 的工具调用明确要求 Responses。 | OpenAI 的产品建议不是全部第三方服务的兼容标准；不能外推所有型号。 |
| 用户指定的 GPT-5.6 Luna | 官方模型页将 Chat Completions 和 Responses 均标记为支持。 | 模型官方支持两种，不代表用户本地网关也开放两种；未据此宣称全部实际调用已通过。 |
| DeepSeek | 当前官方文档已提供 Chat Completions 与 Responses，Responses 示例使用 `deepseek-flash`。 | Responses 有兼容子集限制，例如不支持远端会话状态；不代表旧型号和第三方网关都支持。 |
| Kimi | 当前官方协议对照列出 Chat Completions、Responses；Responses 示例使用 `kimi-k3`。 | 未验证用户的具体套餐、历史型号及中转服务。 |
| GLM / Z.ai | 官方文档和 OpenAPI 明确提供 Chat Completions。 | 本次未找到公开 Responses 声明，不把未找到写成永远不支持。 |

因此“Responses 只有 OpenAI、国产模型都只有 Chat”不符合当前官方文档。另一方面，这些证据也不足以选择 Responses 作为本产品唯一接口。

## 开源 Agent 的实际做法

- LangChain 的 ChatOpenAI 支持两种协议，依据配置、模型和所用功能选择；LangGraph 本身是编排框架，不固定模型 HTTP 协议。
- OpenAI Agents SDK 默认推荐 Responses，同时提供 Chat Completions 模型适配器。
- 本项目已安装的 Eino OpenAI 扩展实际调用 Chat Completions；上游另有 Responses 组件，但尚未完成本项目接口桥接。
- 这三个样本可以说明 Agent 没有统一强制协议；没有行业部署份额数据，不能据此断言“多数开源项目”选择哪个。

## 现有实现的真实兼容边界

`internal/platform/models/chat_protocol.go` 的结构化调用发送 `response_format.type=json_schema` 和 `strict=true`。即使某服务支持 Chat Completions，也需要继续确认它支持项目所需的严格结构化输出、工具多轮、流式返回、usage 和思考参数。当前没有所有 DeepSeek/GLM/Kimi 型号的实测结果，不能承诺只改地址和模型名就能使用全部功能。

当前用户提供的本地网关被用户说明为 Responses 接口；这属于该接入通道的要求，不是本项目业务必须采用 Responses 的证据。一次有界 Responses 最小请求返回 HTTP 502 / `Upstream access forbidden`；先前 Chat 请求返回 503。因此不能声称换协议就已解决连通问题，真实主笔记语义质量仍待验收。

BGE 是独立的 Embeddings 接口，不参与 Chat 与 Responses 的选择。

## 证据与延伸研究

- [OpenAI 迁移说明](https://developers.openai.com/api/docs/guides/migrate-to-responses)
- [OpenAI GPT-6 Astra 指南](https://developers.openai.com/api/docs/guides/latest-model)
- [OpenAI GPT-5.6 Luna 模型页](https://developers.openai.com/api/docs/models/gpt-5.6-luna)：核对实际 HTML，两个端点均为启用样式，非将页面同时列出的灰色不支持端点误判为支持。
- [DeepSeek Responses](https://api-docs.deepseek.com/guides/responses_api/) 与 [Chat Completions](https://api-docs.deepseek.com/api/create-chat-completion/)
- [Kimi 协议概述](https://platform.kimi.ai/docs/api/overview) 与 [Responses](https://platform.kimi.com/docs/api/responses)
- [Z.ai Chat Completions](https://docs.z.ai/api-reference/llm/chat-completion)
- [LangChain ChatOpenAI](https://docs.langchain.com/oss/python/integrations/chat/openai)
- [OpenAI Agents SDK Models](https://openai.github.io/openai-agents-python/models/) 与 [OpenAI 平台的模型/Provider 指南](https://developers.openai.com/api/docs/guides/agents/models)
- 本任务 `agent-protocol-comparison.md`、`glm-kimi-protocol-docs.md`、`responses-protocol-scope.md` 分别记录框架、供应商和项目接入成本的细节。后者描述“若决定接入”的范围，不是实施决定。
