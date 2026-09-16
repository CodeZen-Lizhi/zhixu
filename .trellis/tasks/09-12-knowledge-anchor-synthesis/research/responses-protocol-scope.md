# Research: OpenAI Responses 协议接入范围

- Query: 用户说明本地网关使用 OpenAI Responses，而项目请求 Chat Completions；研究现状、最低完整适配范围及可复用 Eino 组件，供范围建议使用。
- Scope: mixed；只读生产代码、规范、已锁定 vendor 与官方上游源码。未修改代码/依赖/服务/设置，未读取凭据或调用用户网关。
- Date: 2026-09-16

## Findings

### 结论与授权边界

1. 当前生产不是“已有 Responses、尚未勾选”，而是明确拒绝该协议。数据库、HTTP DTO、URL 规范化和历史配置能表示 `responses`，但静态配置校验、结构化 Chat 构造、普通/工具/流式 Runtime 均拒绝实际使用；前端选项也被禁用。
2. 用户纠正的协议差异与现有代码一致，足以停止继续拿 Chat Completions 请求重复测试该网关。但本研究没有访问网关，不能单凭代码把先前 503 的唯一原因和 Responses 接口最终可用性判定为已验证。
3. 官方 Eino 已有 `components/model/agenticopenai` 的 `NewResponsesModel`，无需换掉 Eino，也不宜自行写一套通用 Responses SDK/流解析。项目当前没有引入这个模块。其接口为 `AgenticModel`，与当前 `ToolCallingChatModel` 不能直接替换，必须完成项目边界适配。
4. 建议提出独立的“补齐 Responses 聊天协议支持”范围调整：包含结构化融合、按功能思考强度、问答工具调用和最终流式回答，以及配置/安全/失败/审计契约验证。不能只打通一个 Generate smoke 就开放整个模型设置选项。
5. 本轮明确授权是更新本地服务并启用 BGE；BGE 属于 Embedding 协议。此报告不把该授权解释成已批准新增 Responses 实现。当前 PRD 第 64 行仍明确“不扩展到 Responses API”；主会话应按用户已要求的先提范围建议方式处理。

### 现有文件与代码证据

| 文件 | 作用与关键位置 |
| --- | --- |
| `go.mod:7`、`:10`、`:67`、`:110` | 当前 Eino `v0.9.13`、Chat 扩展 `model/openai v0.1.13`、ACL `v0.1.17`、旧 SDK fork `go-openai v0.1.2`。 |
| `vendor/github.com/cloudwego/eino-ext/libs/acl/openai/chat_model.go:751`、`:853` | 生产依赖实际调用 `CreateChatCompletion` / `CreateChatCompletionStream`。 |
| `internal/platform/config/config.go:915` | 配置层直接拒绝 `chat_api_style=responses`。 |
| `internal/platform/models/chat_http.go:220`、`:231` | 路径枚举支持 `/v1/responses`，但 `validateEinoChatAPIStyle` 拒绝构造；这是预留/历史表示能力，不是运行支持。 |
| `internal/platform/models/eino_chat.go:35`、`:112` | 结构化模型先拒绝协议，之后固定创建 `*einoopenai.ChatModel`；每请求生成独立 Schema、payload、wire 校验和 callback。 |
| `internal/platform/models/chat_protocol.go:31`、`:70` | 当前结构化请求是 `messages`、`response_format.json_schema`、`reasoning_effort`；响应要求 `choices`、`finish_reason=stop` 与 Chat usage。不能仅换 URL。 |
| `internal/platform/models/eino_runtime_chat.go:33`、`:81`、`:127`、`:146`、`:164` | Runtime 对外持有 `ToolCallingChatModel`，提供 Generate、Stream、不可变 WithTools；构造仍固定 Chat 扩展。 |
| `internal/platform/models/eino_runtime_chat.go:254`、`:461` | Runtime wire 校验依赖 Chat chunk 的 model、choices/finish/usage；冻结模型和 effort 覆盖任意调用级漂移。当前 SSE 校验不能直接处理 Responses 事件。 |
| `internal/platform/models/runtime.go:146`、`:177`、`:198`、`:327` | Runtime builder 仍返回具体 `*EinoRuntimeChatModel`；每有效 effort 共享一组结构化/Runtime capability，按稳定功能选择。需将协议选择接入该 factory，不绕开功能绑定。 |
| `internal/modelsettings/runtime/models.go:245`、`:403`、`:423` | revision overlay 包含 APIStyle；ChatFor/RuntimeChatFor 和各有效强度连接探测已接好，可复用。 |
| `internal/agent/adapter/eino/runtime_model.go:534`、`:596`、`:670` | 现有工具 choice、消息 canonical projection、Extra 白名单与 Provider tool ID 规范化以 `schema.Message` 为基础；不能把 Agentic 的全部 Extra 或原始响应塞进持久记录。 |
| `internal/agent/adapter/eino/agent_runtime.go`、`answer_stream.go` | 当前 classic ADK/ToolsNode 与 tool-free 最终答案流；领域工具许可、回执、预算、草稿和正式发布门禁不可因协议切换变化。 |
| `cmd/worker/main.go:3098`、`:3114`、`:3266`、`:3290`、`:4094` | 生产组织、问答、工作区分析、画像/融合等使用冻结功能对应的 capability；直接为某次 smoke 新建模型不代表这些路径接通。 |
| `atlas/migrations/00078_model_settings_chat_api_style.sql:2` | 已有 `chat_api_style IN ('chat_completions','responses')`；只新增协议支持通常无需给该字段补新迁移，不能改历史 revision。 |
| `web/src/features/settings/ModelSettingsPanel.tsx:755` | “Responses API（仅历史配置）”选项 disabled；须在实现与验证完整后才启用。 |
| `internal/organizing/adapter/agent/synthesis_live_test.go:219` | 当前 live helper只显式读取 URL/key/model/version/timeout，没有 Responses APIStyle 入口；后续真实语义验收也要通过生产 factory 按协议选择。 |

### 可复用的官方实现与依赖变化

检索来源为 Context7 的 CloudWeGo 官方仓库/文档索引、官方 GitHub 页面，以及 Go module proxy 提供的上游已发布模块；未运行 `go get`、修改 `go.mod` 或写 vendor。

- 官方模块：`github.com/cloudwego/eino-ext/components/model/agenticopenai v0.2.2`。
- 已核对版本列表、`.mod`、`.info` 和版本 zip 内源码。版本时间 `2026-06-11T02:54:47Z`，tag `components/model/agenticopenai/v0.2.2`，commit `20b2a55477df4e2e078c51596deb019aa54ab133`。
- 模块要求 Go 1.22、Eino `v0.9.5`、`openai-go/v3 v3.35.0`、ACL `v0.1.18-0.20260527084435-846f52bd97c6`。项目 Eino 已是更高的 `v0.9.13`，不代表编译/行为兼容已经验证；引入时还会提升现有 Chat 扩展共享的 ACL，必须检查 Chat Completions 回归及真实依赖 diff。
- `ResponsesConfig` 支持自定义 `HTTPClient`、BaseURL、MaxRetries、Text、Reasoning、Store、MaxTokens、工具、流式。这些允许复用项目受限 transport 和关闭 SDK retry。
- `ResponsesModel.Generate` / `Stream` 操作 `[]*schema.AgenticMessage`；工具通过 `model.WithTools` 调用选项提供，不提供旧接口的 WithTools 方法。
- `option.go:103`、`:111` 提供 `WithResponsesReasoning` / `WithResponsesText`，适合每次调用私有的 Schema 和冻结后有效 effort。
- `responses_model.go:603` 将 MaxTokens 映射成 `max_output_tokens`；`:609` 写 `reasoning`，`:612` 写 `text`。现有 Chat 顶层 `reasoning_effort` / `response_format` 不能原样搬过去。
- `responses_model.go:529` 拒绝旧 `model.Options.ToolChoice`；`:731` 读取 `AgenticToolChoice`，其中 forbidden 映射 `none`。桥接时必须翻译，尤其最终答案流禁工具路径。
- `responses_convertor.go:2253` 将 usage/status/error/incomplete 信息投影到 AgenticResponseMeta；该版本的 metadata 不包含 Provider 回显 model。为保留现有 model echo 校验，应在项目 HTTP transport 层观察/校验实际 wire envelope；不能只校验 SDK message。
- `responses_event_convertor.go:50` 至 `:68` 在 created/in_progress/completed/incomplete/failed 都发出 metadata。当前 Runtime 的“usage 只允许一次”不能直接套用；只有经过校验的最终 usage 应进入既有记录，非 completed 不得视作成功结束。
- `responses_model.go:118` 的 auto-cache 可强制服务端 Store；接入建议明确 `MaxRetries=0`、`Store=false`、`EnableAutoCache=false`，保留项目拥有的调用与恢复事实，不自动引入远端 conversation/cache 依赖。
- `responses_convertor.go:906` / `:2128` 保留 reasoning item、encrypted content、item ID 等后续工具轮次所需协议信息。必须设计为运行内私有状态与项目可持久事实分离；不得丢弃后期待多轮仍正确，也不得透传到用户正文/日志/持久 DTO。

成熟方案优先判断：此官方组件已有发布、文档和测试，静态能力覆盖本需求的通用协议部分，优于自研 HTTP/SSE SDK。是否满足全部项目强制约束仍需下面的桥接验证；本研究未做“80% 已通过”或批准依赖的结论。

### 建议的最小完整变更范围

1. **Factory 与模型契约**：按已保存 APIStyle 选择对应 Eino 组件，Chat Completions 保持原路径；冻结实际 `/v1/responses`、模型版本、adapter version、revision 与有效 effort。仅在这一层拆出公共能力接口/协议专用实现，避免业务层出现 SDK 类型。
2. **结构化调用**：Responses 的 input、严格 `text.format` JSON Schema、`max_output_tokens`、`reasoning.effort`；输出仅接受完整 assistant 文本结果，拒绝 refusal、unexpected tool、failed/incomplete、错误 model、缺失/不一致 usage；继续使用现有 Schema/语义审查和 ModelCall 记录。Schema/response state 必须请求隔离。
3. **工具与流式**：优先在允许 Eino 类型的 Adapter 层建立有限的 Message ↔ AgenticMessage 桥接，复用官方 Generate/Stream/工具转换/SSE；保证 function-call ID 与 tool result 精确对应、不可变工具绑定、工具选择翻译、多轮所需协议元数据、逐帧正文、取消与关闭。若桥接无法无损维护这些合同，再提出局部 Agentic ADK 改造；不默认升级整个编排系统。
4. **成本与冻结**：功能覆盖 → 全局 → Provider 默认的选择规则保持，Responses wire 映射到 `reasoning.effort`；显式空值省略强度，不能由 SDK option 改写。旧任务使用旧 revision；新协议不自动调高 token 预算或增加重试。
5. **安全传输与 SDK 边界**：继续既有 endpoint/DNS/redirect 限制、请求/响应字节上限、错误分类脱敏、精确模型回显校验和 callback 摘要；复用 SDK 处理一般协议，不另写并行协议转换服务。Provider-specific 输出只能经过项目白名单投影。
6. **设置/UI/live helper**：解除已实现协议的拒绝条件和 disabled UI，连接测试走相同 factory；补协议选择的 live 配置。现有 DB/API 枚举可复用，更新能力描述与规范；不重写历史模型设置记录。

不属于上述范围的新增产品能力：OpenAI 内置 web/file search、MCP、远程 conversation 管理、多模态、后台 response 任务、新增 Provider 路由或自动成本策略。官方组件支持不等于本项目需要开放。

### 本地网关接入还有一个独立部署边界

- `internal/modelsettings/runtime/models.go:365` 对 managed 的 openai-compatible Chat 要求 HTTPS，只有既有固定 `http://127.0.0.1:11434` 例外。当前用户的 `http://127.0.0.1:8084/v1` 不满足 managed 设置规则。
- `internal/platform/models/model_transport.go:138` 的 static 模型 transport 支持明确 loopback HTTP；但 Docker 容器内的 `127.0.0.1` 指容器网络空间，不能据宿主机 live 测试推断部署后可连通。
- 现有 `deploy/compose.yml:155` / `:230` 只为内部本地模型转发 11434；`deploy/compose.rag-host-relay-smoke.yml` 有 host relay smoke 先例。
- 因此“应用中启用用户当前 Responses 网关”的后续方案还要明确合法的 HTTPS 接入或受限本地 relay 配置。不能为省事放开任意内网 URL/SSRF 边界。此处是查明的独立配置/部署限制，不妨碍主会话现在启用外部 HTTPS BGE。

### 必要验证与完成界线

| 验证 | 能证明的要求 |
| --- | --- |
| 实际 HTTP fixture 的两种协议、完整 URL/目录输入、wrong endpoint、model echo、usage、refusal、incomplete/failed、限额、取消/超时/401/429/503、无额外请求 | 协议和项目错误/预算/传输契约正确，Responses 不会静默落回 Chat。 |
| 两个不同功能 + 继承 + 显式模型默认，在保存并解析 revision 后分别执行结构化与 Runtime 调用 | UI/持久设置经真实 factory 到 Responses `reasoning.effort`，旧 revision 不漂移。 |
| 两轮真实工具交互（assistant call → tool result → final），碎片化参数、多工具/错关联拒绝、最终禁工具 | 桥接没有丢协议状态或绕过工具权限；不能只测一轮 tool JSON。 |
| 真实 Responses SSE fixture，created/progress/delta/done/completed/error/incomplete、早 EOF、重复 usage、取消、并发流和 schema | 逐帧展示与最终状态正确，无假流式、重复 token 计费、错误流发布或 goroutine/resource 泄漏。 |
| 一个实际问答生产链路至最终答案/引用审查，并验证刷新/读取一致 | 协议接入贯穿 Worker、工具、草稿与正式结果，非单独模型调用成功。 |
| 用户网关的有界 live probe，然后现有融合与当前全文补源质量用例，并人工检查产物 | 网关实际兼容与主笔记语义质量；离线 fixture 和连接测试不能代替。 |
| 针对 Adapter 的 race/vet、API/Worker 编译、前端必要类型检查/设置页面流程、依赖变化与 Chat 旧路径回归 | 增加官方模块和桥接未破坏既有生产能力。 |

实施之前应更新 PRD/设计的协议边界；完成之后更新相关 specs。以上是研究建议，没有执行实现门禁或宣称验收通过。

### External references

- [CloudWeGo agenticopenai 官方说明](https://github.com/cloudwego/eino-ext/blob/main/components/model/agenticopenai/README.md)：两类 API、AgenticModel、配置与扩展。
- [已核对的 v0.2.2 Responses 源码](https://github.com/cloudwego/eino-ext/blob/20b2a55477df4e2e078c51596deb019aa54ab133/components/model/agenticopenai/responses_model.go)：构造、选项、Generate/Stream、禁工具与存储行为；引用行号对应此 commit。
- [该版本输出转换](https://github.com/cloudwego/eino-ext/blob/20b2a55477df4e2e078c51596deb019aa54ab133/components/model/agenticopenai/responses_convertor.go) 与 [流事件转换](https://github.com/cloudwego/eino-ext/blob/20b2a55477df4e2e078c51596deb019aa54ab133/components/model/agenticopenai/responses_event_convertor.go)。
- [v0.2.2 模块依赖清单](https://proxy.golang.org/github.com/cloudwego/eino-ext/components/model/agenticopenai/@v/v0.2.2.mod) 与 [版本身份](https://proxy.golang.org/github.com/cloudwego/eino-ext/components/model/agenticopenai/@v/v0.2.2.info)。
- [Eino v0.9 agentic runtime 迁移说明](https://www.cloudwego.io/docs/eino/release_notes_and_migration/eino_v0.9._agentic-runtime/)：AgenticMessage/ContentBlock 与原有 Message 路径的区别。

### Related specs

- `.trellis/spec/backend/eino-chat-adapter.md`：生产 Eino-only、Responses 当前 fail-closed、wire/usage/错误/观测/强度合同。
- `.trellis/spec/backend/eino-runtime-adoption-gates.md`：Eino 类型范围、工具权限、模型调用预算、流式草稿/正式结果隔离、Provider ID 处理。
- `.trellis/spec/backend/model-settings-runtime.md`：不可变 revision、按功能强度、热激活与旧任务。
- `.trellis/spec/backend/config-loading.md`：static/managed overlay 和配置校验。
- `docs/architecture/adr/0019-mature-framework-first.md`：通用能力复用优先、强制约束不可被覆盖率掩盖。

## Caveats / Not Found

- 未找到当前项目已集成的 Responses 生产 adapter 或可直接使用的 Message/AgenticMessage 完整桥接；这与现有明确拒绝测试一致。
- Context7 返回的一个旧片段仍写 `agenticopenai.New/Config`；最终 API 判断使用固定 v0.2.2 源码里的 `NewResponsesModel/ResponsesConfig`，不照抄索引片段。
- GitHub contents API 未认证调用返回 rate limit 403；改用官方公开页面与固定版本 module zip 只读核对，未使用凭据或绕过限制。
- 未运行新模块编译、race、网络 smoke 或真实模型语义检查；静态源码支持不能证明网关/模型接受 strict schema、所有 effort 档位、工具多轮或 SSE。
- 本报告不修改本地服务；BGE 实际激活由主会话单独处理。
