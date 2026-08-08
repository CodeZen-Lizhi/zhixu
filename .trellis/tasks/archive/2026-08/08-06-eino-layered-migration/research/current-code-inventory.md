# 当前代码与迁移入口盘点

> 状态：这是 ADR-0019 和生产实现落地前的代码快照。当前结果以 ADR-0019、`implement.md` 和
> `stage2-gate-and-stage3-stage4-decisions.md` 为准；下文“当前直接发送”“尚未完成”“根 go.mod 没有 Eino”
> 均描述迁移前基线，不代表当前工作树。

## 结论

仓库已经把 Provider、AI 应用编排、检索应用服务和持久工作流分成 Port。迁移的最小正确入口是 Composition Root 构造的 `ChatModel` Adapter；不需要把 Eino 类型下沉到 Domain，也不需要重写 PostgreSQL/River。

## 当前调用链

```text
config/model settings
  -> internal/platform/models.NewConfiguredModelRuntime
  -> internal/platform/models.NewConfiguredChatModel
  -> agent/application.ChatModel
  -> agent/application.StructuredRunner
  -> relation / RAG / artifact / capture profile / organizing generators
  -> Model Run/Call + domain schema/evidence/finalizer
```

证据：

- `internal/agent/application/chat.go:53-75` 定义供应商无关的 `ChatRequest`、`ChatResponse` 和单次调用 `ChatModel`；调用方不应看到 SDK 类型。
- `internal/agent/application/runner.go:88-199` 由 `StructuredRunner` 独占 `INITIAL -> REPAIR -> REDUCED` 三次上限、累计 request/response/token/time budget，以及严格 Schema 原文校验。
- `internal/platform/models/chat_factory.go:11-24` 是 Chat Adapter 的 Composition Root；`internal/platform/models/chat_openai.go:29-85` 当前直接发送 OpenAI-Compatible 请求，并严格校验模型回显、单 choice、`finish_reason=stop`、无 Tool Call 和 usage。
- `internal/platform/models/runtime.go:133-170` 在 API/Worker 共同构造一次冻结的 Chat/Embedding capability；切换实现必须保持这一生命周期和无 Credential 长驻约束。

## 共享 StructuredRunner 的生产消费者

这些模块都依赖项目 `agentapplication.ChatModel`，因此不应各自接 Eino：

| 模块 | 入口 | 迁移建议 |
|---|---|---|
| 关系评估 | `internal/agent/adapter/workflow/executor.go:24-140` | 先无改动复用 Eino-backed ChatModel；后续可把纯模型节点包成 Graph |
| RAG | `internal/agent/adapter/workflow/rag_executor.go:20-250` | 第一阶段只替换底层 ChatModel；Graph 另阶段包住短流程，保留 RAGExecutor |
| Artifact 生成 | `internal/artifact/workflow/executor.go:32-381` | 先无改动复用 Adapter；Citation/Finalizer 不迁移 |
| Capture Profile | `internal/capture/profile/generator.go:34-180` | 先无改动复用 Adapter；画像持久化和证据版本不迁移 |
| Organizing 生成 | `internal/organizing/workflow/generation.go:35-295` | 先无改动复用 Adapter；模板/人工确认不迁移 |

## RAG 的真实边界

- `internal/agent/application/rag.go:152-322` 明确 RAGExecutor 只生成 terminal proposal，不拥有持久化终结；它依次调用 Plan、Search、Eligibility、Topic、Structured Runner、Citation/Faithfulness Publisher 和 Progress Port。
- `internal/retrieval/application/search.go:65-303` 拥有 Active Index 读取、Keyword/Vector 并行检索、RRF、相邻 Chunk 去重、可选 Rerank、降级及版本校验。
- `internal/agent/adapter/retrieval/adapter.go` 将 SearchResult 扩展成证据引用；这不是通用 Retriever 可替代的黑盒。

因此 Eino Graph 节点最多调用这些项目 Port；不能直接读取 Knowledge SQL，也不能自己决定“证据合格”或发布答案。

## Tool 与工作流边界

- `docs/architecture/tool-security.md:1-18,80-125` 要求 Tool 请求经过 Registry、Schema、持久 Workflow Policy、Capability、lease/fence 和 Executor；Eino 只能把模型 Tool Call 转成项目 `ToolRequest`。
- `docs/architecture/workflow-engine.md:8-12,90-150` 要求 PostgreSQL 拥有 Run/Node/Attempt、lease、retry、Human Task、Outbox、补偿和恢复；River 只负责投递/领取。
- `docs/architecture/adr/0013-eino-adoption-gate.md:9-16` 已经规定 Eino 不能成为 Domain、Workflow、Proposal/Approval 或 Write Authorization 的事实源。

## 现有 PoC 的可复用资产与缺口

`poc/eino/report.md:15-28` 记录 Chat Graph、ToolsNode、Callback/Trace 和基础错误分类已通过；实际 Eino `schema.StreamReader` 链路、Eino Structured Output、Eino Embedding/Retriever/Rerank、River Node 和真实 Provider smoke 尚未完成。`poc/eino` 的 contract/bridge 代码可作为测试样例，不能直接当生产实现。

## 构建与依赖影响

- 根 `go.mod` 当前没有 Eino；PoC 独立 module 使用 `github.com/cloudwego/eino v0.9.12` 和 OpenAI 扩展 `v0.1.13`。
- `deploy/Dockerfile:8-16` 使用根 `vendor` 构建 API/Worker；正式接入必须同步根 `go.mod`、`go.sum`、`vendor/`、`vendor/modules.txt`。
- `.github/workflows/ci.yml:33-39` 和 `Makefile:34-40` 当前把 PoC 作为独立 module 门禁；迁移后应增加主模块 Eino Adapter 的测试，并保留 PoC 直到生产门禁闭合。
