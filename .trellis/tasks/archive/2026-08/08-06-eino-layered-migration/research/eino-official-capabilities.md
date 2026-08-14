# Eino 官方能力核对

> 2026-08-08 修订：本文的“规划结论”是首次迁移前的历史判断，已被同目录修订版 `prd.md`、`design.md` 和
> `implement.md` 取代。最新复核确认 Eino 具备完整 RAG 编排、OpenAI/原生 Ollama Embedding、ToolsNode/ReAct、
> Streaming 和 Interrupt/Checkpoint；后续是否生产采用由项目合同、版本和恢复所有权门禁决定，不能再把当时未实施
> 写成框架能力缺失。下文保留用于追溯首次迁移的证据和约束。

> 状态：本文件记录实施前的官方能力和版本探针。最终生产范围与门禁结果以
> `docs/architecture/adr/0024-layered-eino-adoption.md`、`implement.md` 和
> `stage2-gate-and-stage3-stage4-decisions.md` 为准。

## 2026-08-08 能力复核增量

| 能力 | 官方能力事实 | 当前项目决策 |
|---|---|---|
| Embedding | `eino-ext` 同时提供 OpenAI 与原生 Ollama 组件 | 按 Provider 先做可行性门禁；两者只返回 vectors，不回显 response model，OpenAI 还不暴露 item index；Ollama 必须保持项目 `truncate=false` |
| RAG | Workflow 可编排确定性 DAG，Graph 可表达循环/动态分支；Workflow 不支持 cycle | 先比较“现有 `RAGExecutor` + Eino Components”和完整 Eino Workflow 的净收益，不预设必须全迁 |
| Retriever/Indexer | Core 有接口，ext 有多种外部存储实现 | 官方无项目 PostgreSQL FTS/pgvector/RRF/Active Index 等价实现；保留 `SearchService`/事务，只做 Eino bridge |
| Tool/ReAct | ToolsNode 与 ChatModelAgent 可承担 schema、dispatch 和循环 | 没有产品 PRD、生产消费者、持久 Attempt 和专用 ADR 时继续 No-Go；权限、receipt、Proposal/Approval/Safe Writeback 仍归项目 |
| Streaming | Graph/ChatModel 支持真实 Stream/Transform | 新 Token API 可采用；现有 durable phase SSE 不替换，禁止单帧 fake stream |
| Interrupt/Checkpoint | 支持 Interrupt/Resume 和调用方提供的 CheckPointStore | 首轮只允许同一 Worker 进程、同一活跃 Attempt 内 PoC；Worker/Human Task/Attempt 变化后旧 checkpoint 失效，并定义 TTL/Delete、加密和敏感数据生命周期 |

生产 core 固定 [`v0.9.13`](https://github.com/cloudwego/eino/releases/tag/v0.9.13)。复核时 Embedding 候选 pseudo-version 对应 commit
[`90a15623ddb66465aea01fbe8c63ecc9d267acc1`](https://github.com/cloudwego/eino-ext/commit/90a15623ddb66465aea01fbe8c63ecc9d267acc1)：
OpenAI/Ollama Embedding 模块分别声明 core `v0.7.13`/`v0.6.0`，因此“官方已有组件”不等于能直接并入当前根模块。
采用前必须逐个通过精确版本编译、API、race、Provider 合同、响应上限和 vendor 门禁。

精确源码：[OpenAI Embedder](https://github.com/cloudwego/eino-ext/blob/90a15623ddb66465aea01fbe8c63ecc9d267acc1/components/embedding/openai/embedding.go)、
[Ollama Embedder](https://github.com/cloudwego/eino-ext/blob/90a15623ddb66465aea01fbe8c63ecc9d267acc1/components/embedding/ollama/embedding.go)、
[OpenAI module](https://github.com/cloudwego/eino-ext/blob/90a15623ddb66465aea01fbe8c63ecc9d267acc1/components/embedding/openai/go.mod)、
[Ollama module](https://github.com/cloudwego/eino-ext/blob/90a15623ddb66465aea01fbe8c63ecc9d267acc1/components/embedding/ollama/go.mod)。

## 来源与版本探针

官方文档通过 Context7 查询：

- Eino 组件参考：<https://github.com/cloudwego/eino/blob/main/_autodocs/components-reference.md>
- Eino 核心接口：<https://github.com/cloudwego/eino/blob/main/components/model/interface.go>
- Eino Extension OpenAI：<https://github.com/cloudwego/eino-ext/tree/main/components/model/openai>
- Eino Checkpoint/Interrupt：<https://github.com/cloudwego/eino/blob/main/_autodocs/configuration.md>

截至 2026-08-06 的本机 Go module 查询结果：

```text
github.com/cloudwego/eino                 v0.9.13  (2026-07-22)
github.com/cloudwego/eino-ext/components/model/openai v0.1.13 (2026-04-16)
github.com/cloudwego/eino-ext/components/embedding/openai v0.0.0-20260803030130-90a15623ddb6
```

实施阶段已重新解析并在根模块锁定 Eino core `v0.9.13` 与 OpenAI extension `v0.1.13`，同时核对许可证和
vendor 体积。Embedding 扩展仍是 pseudo-version，未进入本任务生产依赖；未来采用前必须重新执行同类门禁。

## 能力与接入含义

### ChatModel

Eino `BaseChatModel` 提供 `Generate` 和 `Stream`；`ToolCallingChatModel.WithTools` 返回新的实例，不修改共享基础模型，适合并发请求按调用绑定只读工具。OpenAI 扩展支持 `BaseURL`、自定义 `HTTPClient`、JSON Schema ResponseFormat、usage、Stream，以及 `WithRequestPayloadModifier`/`WithResponseMessageModifier`。

### 动态 Structured Output 的限制

Eino OpenAI 扩展的 `ResponseFormat` 位于 `ChatModelConfig`，而知序的 Schema 在每一次 `ChatRequest` 中冻结，且 `INITIAL/REPAIR/REDUCED` 可能使用不同 Schema。官方 `model.Option` 没有通用 ResponseFormat 选项，因此不能简单在一个静态模型上切换 Schema。

可行方案是生产 Adapter 在每次 `Generate` 时使用 `WithRequestPayloadModifier` 注入当前已验证的 JSON Schema，并使用 `WithResponseMessageModifier` 校验原始响应中的 model、choice、finish reason、tool calls 和 usage；或者保留直接 HTTP Adapter 直到该方案完成真实兼容测试。不能为了使用 Graph 放宽项目严格 Schema 契约。

### Streaming

`schema.StreamReader` 是 read-once，调用方必须在正常结束和提前返回时恰好关闭；官方文档明确未关闭可能造成 pipeline goroutine leak。Eino 负责 Reader 生命周期，Provider 取消仍需由 Adapter 用 Context 和 `Close` 验证。知序当前 SSE 是持久事件通知，不是 Token SSE，因此内部 StreamReader 接入不等于修改现有 SSE 协议。

### Graph/Chain

`compose.Graph`/`Chain` 适合把短流程的输入输出连成可测试节点。建议只把模型节点和调用项目 Port 的无副作用节点放进 Graph；Graph 输出必须回到现有 Domain Schema、Evidence、Citation 和 Finalizer。

### Checkpoint/Interrupt

Eino 的 Runner 可通过调用方提供的 `CheckPointStore` 保存 checkpoint，并支持 Interrupt/Resume。它不自动替代业务持久化，也不保证外部副作用 exactly-once；恢复后节点可能再次执行。因此本项目不能同时把 Eino checkpoint 和 PostgreSQL Workflow 当作两本恢复账，PostgreSQL/River 必须继续是唯一事实源。人工审批应由项目 Human Task 状态驱动，Eino Interrupt 仅可作为 AI 子图内部信号。

### Embedding/Retriever/Indexer

官方接口提供 `Embedder.EmbedStrings`、`Retriever.Retrieve`、`Indexer.Store` 和 `schema.Document`。这些接口适合做 Adapter，但官方示例多面向外部向量库/检索服务。知序的 Active Index、Workspace/Approved Evidence 过滤、FTS+pgvector 并行、RRF、provenance 和降级不应被 Eino Retriever 重写。

## 规划结论

1. 第一阶段采用 Eino OpenAI ChatModel + 项目 `ChatModel` 包装器。
2. 第二阶段采用 Eino Callback/Graph，但回调不替换 Model Run/Call 事实表，Graph 不替换 RAGExecutor 或 River。
3. Embedding 先保留项目 Adapter；需要 Eino 组件时只写 Port bridge，并等待稳定扩展版本和真实 Provider 合同测试。
4. Rerank 保留项目 Port；仓库目前没有生产 Rerank Provider，接 Eino 不能凭空产生供应商实现。
