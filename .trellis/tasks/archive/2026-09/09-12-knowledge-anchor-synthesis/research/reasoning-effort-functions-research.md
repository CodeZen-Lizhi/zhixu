# 按功能配置思考强度：消费者与接线研究

日期：2026-09-16。本文只梳理现有生产调用边界，为“功能覆盖优先于统一默认；未覆盖继承统一默认；显式模型默认不发送强度”提供实施依据。不记录任何模型端点或凭据。

## 结论

推荐提供 8 个稳定、面向用户的功能键，而不是按每一条 Prompt 或每一个工作流节点暴露设置。这样用户能按成本意图调整，内部新增/改版 Prompt 不会改变配置含义。

| 用户看到的功能 | 稳定键 | 当前实际调用范围 | 主要入口 |
| --- | --- | --- | --- |
| 文件简介与标签 | `file_profile` | 新文件的摘要、标签、主题与知识目录画像 | `internal/capture/profile/generator.go:131`、`cmd/worker/main.go:3459` |
| 知识整理 | `knowledge_organization` | 已采集知识的关系判断、区段/内容整理和生成 | `cmd/worker/main.go:3195`、`cmd/worker/main.go:1883`、`cmd/worker/main.go:1940` |
| 主笔记范围与关联建议 | `anchor_scope` | 自动推断主笔记范围、判断新来源是否相关、提出范围调整建议 | `cmd/worker/synthesis_components.go:260`、`internal/organizing/adapter/agent/anchor_model.go:85` |
| 主笔记融合 | `main_note_synthesis` | 目标知识点选择、主笔记生成、生成结果语义核验 | `cmd/worker/synthesis_components.go:202`、`:412`；`internal/organizing/adapter/agent/synthesis_model.go:217` |
| 当前主笔记来源复核 | `manuscript_source_review` | 新来源是否支持**当前全文**，包括手工修改后的现行段落 | `cmd/worker/synthesis_manuscript_source_review_components.go:27`；`internal/organizing/adapter/agent/synthesis_manuscript_source_review_model.go:90` |
| 知识问答 | `knowledge_qna` | 检索计划、只读工具循环、最终流式回答、回答元数据及忠实性复核 | `cmd/worker/main.go:3113`、`:3264`；`internal/agent/adapter/workflow/rag_agent_generation.go:163` |
| 工作区分析 | `workspace_analysis` | 工作区检索/工具决策、候选答案合成、引文核验、发布复核 | `cmd/worker/main.go:3300`；`internal/agent/adapter/workflow/workspace_analysis_v2_decide.go:17` |
| 笔记访谈与复习准备 | `note_interview` | 根据主笔记生成访谈/复习准备内容 | `cmd/worker/synthesis_interview_components.go:78`；`internal/review/interview/application/note_runtime.go:21` |

`main_note_synthesis` 和 `manuscript_source_review` 应保持两个独立配置项：前者是整理和生成，后者的职责是“当前文本是否仍由来源支持”，用户已经明确要求后者重新核对全文。将两者合并会失去最有价值的成本控制点。

## 已验证的运行时事实

1. 目前统一默认强度是 `modelsettingsdomain.ChatSettings.ReasoningEffort`，可取空值、`low`、`medium`、`high`、`xhigh`、`max`。它随不可变模型设置 revision 保存，并被 `runtime.overlay` 写入 `config.Config`：
   - `internal/modelsettings/domain/settings.go:59-70,246-252`
   - `internal/modelsettings/runtime/models.go:224-260`
   - `internal/modelsettings/adapter/postgres/revision_codec.go:15-40`

2. 一个 `platformmodels.ModelRuntime` 当前只构造一对 Chat 适配器：结构化 `ChatCapability` 与 Eino 的 `RuntimeChatCapability`。两者都从同一个 `ChatReasoningEffort` 构造：
   - `internal/platform/models/runtime.go:119-189,213-219`
   - `internal/platform/models/eino_chat.go:34-65`
   - `internal/platform/models/eino_runtime_chat.go:79-110`

3. 结构化调用有稳定的 `PromptRef`，例如采集 profile、主笔记融合/核验、RAG 计划/元数据/忠实性复核；但不应只根据 PromptRef 分流。RAG 工具 Agent、最终答案流和工作区分析循环直接拿到 Eino `ToolCallingChatModel`，其构造时已经冻结强度：
   - `internal/agent/adapter/workflow/rag_agent_generation.go:163-194`
   - `internal/agent/adapter/eino/agent_runtime.go:74-85`
   - `internal/agent/adapter/eino/answer_stream.go:40-67`
   - `internal/agent/adapter/eino/workspace_analysis_loop.go:33-65`

4. Eino runtime 传输会删除每个 SDK 调用携带的 `reasoning_effort`，再写回构造时的强度。因此它已具备“请求不能覆盖冻结配置”的安全边界；新设计应保留它：
   - `internal/platform/models/eino_runtime_chat.go:461-494`

5. 运行中的工作流已持久化 `ModelSettingsRevision`，worker 热更新会重建一代模型组件。只要功能覆盖跟随该不可变 revision，一项任务在运行中不会读到后来保存的设置：
   - `internal/organizing/adapter/agent/synthesis_model.go:188-228`
   - `cmd/worker/synthesis_interview_components.go:78-118`
   - `cmd/worker/main.go:2341-2425`

## 最小可复用接线点

在 `internal/modelsettings/domain` 定义固定枚举 `ReasoningFunction`，并在 `ChatSettings` 中保存功能覆盖。建议值是上表 8 个键，未知键一律拒绝，不能由 Prompt、用户输入或 SDK option 产生。

覆盖值需表达三种不同语义：

| 存储值 | 有效强度 | UI 含义 |
| --- | --- | --- |
| 未设置（UI 显示继承；wire 不保存 `inherit`） | `ChatSettings.ReasoningEffort` | 继承统一默认 |
| 显式空字符串（wire 不保存 `provider_default`） | 空字符串 | 使用模型/服务默认，不发送 `reasoning_effort` |
| `low` / `medium` / `high` / `xhigh` / `max` | 相同值 | 该功能固定强度 |

旧 revision 没有覆盖时，解释为所有功能继承，因此升级后维持原有行为。全局空值仍是统一的“模型默认”，但不能与单功能的“继承统一默认”混为一个状态。

运行时应在一次 `ModelRuntime`/`modelsettingsruntime.Models` 构造中：

1. 根据冻结 revision 计算每个功能的有效强度。
2. 以“有效强度”去重，分别创建并缓存结构化 Chat 与 RuntimeChat 适配器。相同强度的多个功能共享同一对已构造实例。
3. 提供显式选择器，例如 `ChatFor(ReasoningFunction)` 与 `RuntimeChatFor(ReasoningFunction)`；保留现有 `Chat()` / `RuntimeChat()` 仅供健康检查、旧调用或明确的全局默认路径。
4. 在 `cmd/worker/main.go` 的 composition root 将对应 capability 传入每个构造器，而不是在任意业务调用处修改 SDK option：采集、关系/整理、主笔记各 executor、RAG runtime 与工作区分析 runtime 都已有独立构造点。

这不会为每个请求重复创建 Provider，也不需要每个功能维护独立模型、密钥或模型 registry；最多为实际不同的有效强度各创建一对 adapter。每一代 runtime 仍由既有 revision 与生命周期管理关闭。

## 实施影响范围

- 模型设置领域、JSON/HTTP/OpenAPI、PostgreSQL revision 持久化及前端 `ModelSettingsPanel`：新增固定键覆盖与三态显示；当前前端只支持单个 `reasoning_effort`，见 `web/src/api/model-settings.ts:22,482-502,821-840`、`web/src/features/settings/ModelSettingsPanel.tsx:129-145,724`。
- `internal/modelsettings/runtime/models.go` 与 `internal/platform/models/runtime.go`：生成并暴露按功能选择的冻结 capability，关闭所有去重后的资源。
- `cmd/worker/main.go`、`cmd/worker/synthesis_components.go`、`cmd/worker/synthesis_manuscript_source_review_components.go`、`cmd/worker/synthesis_interview_components.go`：在 composition root 绑定各功能的 Chat/RuntimeChat，而不是把统一 `agentComponents.model` 传给所有消费者。
- 关联的静态/API、SQLite/PostgreSQL revision 升级与 worker 热更新测试，重点证明：覆盖优先级、显式模型默认不发送字段、同强度复用、运行中 revision 不漂移、结构化与 RuntimeChat 同时生效。

## 限制与待确认

- 构造处的默认 `temperature=0` 不能单独代表最终 wire；上一轮全局强度实现已在传输层对显式强度/GPT-6省略该参数。本轮复用既有协议适配，不扩大模型支持声明。
- `knowledge_organization` 包含关系判断、artifact 生成与整理生成三个现有消费者。它们目前都由统一 `agentComponents.model` 注入。该分组是面向用户的成本开关；若将来用户确实需要分别控制“关系判断”和“内容生成”，可以增加新固定键，但本轮不应先扩张为按每个内部节点配置。
