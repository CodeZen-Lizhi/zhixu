# Research: Agent 框架与 Chat Completions / Responses

- Query: 常见开源 Agent 框架使用什么接口；Agent 是否必须 Responses；本项目怎样保留跨模型通用性。
- Scope: mixed；LangChain/LangGraph、OpenAI Agents SDK、既有 Eino 接入；不评估行业市场份额，不改实现或运行配置。
- Date: 2026-09-16

## Findings

### 结论

1. **Agent 并不天然要求 Responses。** 普通函数工具调用、多轮工具执行、流式回答、结构化输出均可以由 Chat Completions 支撑，前提是具体服务商和模型实现了所需能力。框架名称里的 `ChatModel` 不等于底层只允许 Chat Completions。
2. **不能说开源 Agent 项目“都用 Chat”或“多数用 Chat”。** 本次只核对三个框架，没有覆盖率或部署量数据。已查到 OpenAI Agents SDK 默认推荐 Responses，LangChain 生态也有默认走 Responses 的 Agent 产品；这是足以否定“都用 Chat”的直接反例。
3. **本项目的业务需求不强制 Responses。** 资料画像、标签、主笔记融合、溯源核验和本地检索工具都已有 Chat 路径；无需仅因它是 Agent 项目就迁移协议。不过，选定模型、网关或特定高级功能可能另有 Responses 要求，不能反过来承诺任何模型都能走 Chat。
4. **建议保持现有 Chat Completions 兼容路径作为本轮基线，先核实用户最终选择的服务是否提供满足项目要求的 Chat endpoint。** 若能够提供且能力检查通过，本项目不必新增 Responses。如果用户必须使用只开放 Responses 的模型/网关，再单独批准接入适配；不用要求用户手写消息转换，也不应静默尝试不同接口。此处是方案建议，不代表用户已批准新增协议或自动探测。

### 框架对照

| 框架/组件 | 已核实的接口行为 | 对本项目的含义 |
| --- | --- | --- |
| LangChain `ChatOpenAI` | 同一类支持两种接口；`use_responses_api` 可显式指定，未指定时按模型和请求参数推断。官方文档明确 Responses 专属功能会触发路由。当前上游源码还对部分模型名及 GPT-6 带工具请求选择 Responses。普通 Chat 兼容 endpoint 可通过自定义 `base_url` 接入。 | 通用上层模型能力可以与具体 wire 协议分离；“自动选择”是有规则的适配，不能由一个模型品牌或任意 URL 推断万能兼容。 |
| LangGraph | 是编排和持久执行框架，可独立于 LangChain 使用；模型接口由节点/模型适配器决定。 | 不能问“LangGraph 固定用哪个 endpoint”；它不要求节点只调 Responses。 |
| LangChain Deep Agents | 官方 changelog 中 `deepagents v0.4.0`（2026-02-10）开始对 `openai:` 模型字符串默认使用 Responses。此行为属于 Deep Agents，不能误写成全部 LangChain 或 LangGraph 的统一默认值。 | “开源 Agent 都用 Chat”的反例；默认值也随产品和版本变化。 |
| OpenAI Agents SDK（Python） | 官方文档默认推荐 `OpenAIResponsesModel`，同时提供 `OpenAIChatCompletionsModel`；可用 `set_default_openai_api("chat_completions")` 改默认 API。非 OpenAI 服务有 Chat 模型接入示例。Chat adapter 本身实现 tools、output schema 和 streaming。 | 即便官方 Agent SDK 也不把普通 Agent 与 Responses 绑定；OpenAI 专有的托管工具等能力另有约束。 |
| Eino（本项目现用版本） | 复用 `responses-protocol-scope.md`：项目 Eino `v0.9.13` + `model/openai v0.1.13` 使用 Chat Completions，已具备 Generate/Stream/WithTools 和结构化调用。Eino 上游另有 `agenticopenai v0.2.2` 提供 Responses，但其 `AgenticModel/AgenticMessage` 接口不能直接替换项目现有 `ToolCallingChatModel/Message`。 | Eino 本身不是 Responses 障碍，但本项目目前没有完成那一条协议适配；上游存在组件不等于本产品已经支持。 |

### Chat 能力与通用性的边界

- 已核对 OpenAI Agents SDK 当前 Chat adapter：`stream_response` 在 `openai_chatcompletions.py:425`；`:638` 把输出 schema 转成 `response_format`；`:640` 转换函数工具；`:737` 调用 `chat.completions.create`。这直接证明标准 Agent 工具循环、结构化返回及流式不是 Responses 独占能力。
- 不能从“支持 `/chat/completions`”推出“本项目全部功能都可用”。需另行验证严格 JSON Schema、工具选择/多轮、stream/usage、模型回显和思考参数；有些服务只实现兼容协议的子集或额外字段。本研究没有用新 Provider 运行这些验证。
- 当前项目主笔记与问答上下文、知识检索、溯源、审批及预算均由项目管理，不依赖 OpenAI 远端 conversation 或内置 file search；Responses 专有托管能力不能作为本次业务必需项。
- “通用模型设置”适合表达项目需要的能力和服务连接，内部适配协议差异。它不能保证所有品牌、所有模型、所有参数都兼容；更不应该把 `Responses only` 设置施加给只支持 Chat 的服务。
- 不能承诺用户原网关换一个 URL 或请求字段就正常。该网关的可用性由主会话单独核实，本研究没有访问它。

### Files found / Code patterns

- `research/responses-protocol-scope.md`：复用的 Eino 固定版本调查、Responses 接入成本和现有拒绝路径；未重复调查其上游源码。
- `internal/platform/models/eino_runtime_chat.go:79`：当前 Generate/Stream/WithTools adapter 构造；`:146` 流式、`:164` 不可变工具绑定。
- `internal/platform/models/eino_chat.go:35`：现用 Eino OpenAI 结构化 adapter 构造；`:87` 同生产路径的连接检查。
- `internal/platform/models/chat_protocol.go:40`、`:100`：现有 `response_format.json_schema` 输出合同。
- `prd.md:64`：当前规划明确没有扩大到 Responses；新增协议须作为后续范围建议，不能由调查自动变成实施。
- `.trellis/spec/backend/eino-runtime-adoption-gates.md` 第 2、3 节：项目持久状态/权限/溯源/预算与 Eino 运行时的所有权边界。

### External references / Versions

以下均为官方文档或官方仓库；检索截至 2026-09-16。使用 `find-docs` 的 Context7 `library` → `docs` 流程，每个库各两次调用；再用原始官方文档/源码确认接口行为。

- [LangChain ChatOpenAI integration](https://docs.langchain.com/oss/python/integrations/chat/openai)：Responses 从 `langchain-openai>=0.3.9` 可用，按功能路由；工具与结构化输出。
- [LangChain provider 概念与兼容 endpoint](https://docs.langchain.com/oss/python/concepts/providers-and-models)：第三方 Chat-compatible 基本接入与非标准字段限制。
- [LangChain 当前 ChatOpenAI 源码](https://github.com/langchain-ai/langchain/blob/master/libs/partners/openai/langchain_openai/chat_models/base.py)：本次读取的 `use_responses_api` 在 1206 行，`_use_responses_api` 在 1924 行，底层判定在 4470 行；这是当前 master 快照，行号可漂移，不代表固定发行版。
- [LangGraph overview](https://docs.langchain.com/oss/python/langgraph/overview)：模型无关的编排边界，可脱离 LangChain 使用。
- [LangChain changelog](https://docs.langchain.com/oss/python/releases/changelog)：2026-02-10 `deepagents v0.4.0` 的 OpenAI Responses 默认值。
- [OpenAI Agents SDK Models](https://openai.github.io/openai-agents-python/models/)：默认推荐 Responses、可选 Chat、跨 Provider 能力差异和 Responses-only 功能。
- [OpenAI Agents SDK 配置源文档](https://github.com/openai/openai-agents-python/blob/main/docs/config.md)：切换默认 API 的配置入口。
- [OpenAI Agents SDK Chat adapter 源码](https://github.com/openai/openai-agents-python/blob/main/src/agents/models/openai_chatcompletions.py)：Chat 工具、schema 和流实现；本次核对 main，未声明固定发布版本。
- [Eino 官方 agenticopenai](https://github.com/cloudwego/eino-ext/blob/main/components/model/agenticopenai/README.md)：Responses 组件；版本证据与具体约束引用既有 `responses-protocol-scope.md`。

### Related specs

- `.trellis/spec/backend/eino-chat-adapter.md`：当前 Chat-only、严格 wire 校验和 Responses fail-closed 合同。
- `.trellis/spec/backend/eino-runtime-adoption-gates.md`：工具/预算/审批/持久恢复边界。
- `.trellis/spec/backend/model-settings-runtime.md`：不可变设置版本及按功能强度配置。
- `docs/architecture/adr/0019-mature-framework-first.md`：复用既有框架，不能仅为换协议而重写整套 Agent。

## Caveats / Not Found

- 没有开源 Agent 实际部署份额数据，不能断言某协议占行业多数。
- 不评估所有模型的兼容性；DeepSeek、GLM、Kimi、OpenAI 指定型号的官方支持情况由主会话另查。本报告不拿品牌名代替 API 能力矩阵。
- LangChain、Agents SDK 当前在线文档与 main/master 会变化；已注明本次观察时间，未修改本项目依赖或将最新源码当作已安装能力。
- 不把“框架可接入”写成“本项目已接入”，也不把“能结构化返回”写成“指定第三方模型通过了本项目 strict schema 及语义验收”。
- 无代码/配置/服务/依赖变更，无用户网关请求；唯一写入为本研究文件。
