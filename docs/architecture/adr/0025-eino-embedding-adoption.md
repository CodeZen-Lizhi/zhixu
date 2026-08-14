---
status: superseded
---

# Eino OpenAI-Compatible/Ollama Embedding 可回滚采用

> **已被 ADR-0027 取代，不是当前操作手册。** 下文的 `direct|eino` selector 与切回 direct 只记录迁移当时的
> 历史决策；当前生产仅允许 Eino，恢复旧实现只能使用 Git 发布记录。Adapter 隔离、wire 校验、安全传输和
> 持久模型身份边界仍有效。

ADR-0024 将 Eino 的生产采用限定在 Chat、Callback telemetry 与 Structured Output 短 Graph，并要求 Embedding
另过独立门禁。项目现有 `EmbeddingModel` Port、安全 HTTP transport、Configured Embedder Factory 与 direct
OpenAI-Compatible/Ollama Adapter 已提供稳定合同；本决策只在这些边界后增加可灰度的 Eino 内部实现。

## Historical Decision (superseded)

- 新增进程级 `EmbeddingImplementation=direct|eino`，默认 `direct`。该选择器不改变 Provider、Model、Adapter
  identity、Embedding Config Hash 或 managed model revision；API、Worker 与 modelctl 使用相同冻结值。
- OpenAI-Compatible 与 Ollama 都保留 direct Adapter，并新增 Eino-backed Adapter。任一 Provider 的 Eino 路径
  未通过发布门禁时，只把该环境的 selector 切回 `direct`，不迁移数据库或重建持久身份。
- Eino/Provider SDK 类型只允许位于 `internal/platform/models`。领域、检索 Application、Workflow、HTTP API、
  PostgreSQL 与 River 继续只依赖项目 `EmbeddingModel`、`EmbeddingContract` 和 `EmbedResult`。
- 两个 Eino Adapter 复用项目安全 HTTP client、Endpoint 校验、redirect 禁止、DNS/IP 约束、TLS 下限、请求限制、
  有界状态错误体和稳定脱敏错误分类；不启用 SDK retry、fallback 或隐式认证。
- 项目 RoundTripper 使用调用 Context 保存请求级 wire 响应，不在共享 Adapter 保存响应元数据。wire 响应先通过
  Content-Type、字节上限、单 JSON 文档和 Provider 专项合同，再重新封装给 SDK。
- OpenAI-Compatible wire 合同校验 model，并按 `data.index` 重排；缺失、重复和越界 index 必须拒绝。
  Ollama 请求显式发送 `truncate=false`，响应必须校验 model、数量并保留 wire 顺序。
- SDK 返回的 `float64` 向量必须与 wire 捕获的 `float32` 向量逐项一致，然后进入既有维度、有限值、归一化和
  batch/input 限制。任何分歧都是 consistency violation，不得静默采用其中一份结果。
- Eino Embedding callback 暴露原始文本与向量，因此本阶段不注册全局 callback；Embedding telemetry 必须另立
  只接收稳定脱敏摘要的调用级边界后才能启用。

## Historical Contract Baseline

- OpenAI-Compatible direct/Eino 与 Ollama direct/Eino 四组合必须通过同一共享 contract suite，保持请求、
  `EmbeddingContract`、向量顺序、归一化和稳定错误分类等价。
- 故障门禁覆盖 Content-Type、响应体上限、尾随 JSON、状态码、redirect、网络、timeout、caller cancel、
  敏感错误体、OpenAI index/model 与 Ollama truncate/model/count。
- 并发测试必须证明请求级 wire 捕获没有串扰；race detector 必须通过。
- 配置、factory、managed settings overlay、进程冻结和安全格式化必须覆盖 default/YAML/env/invalid selector。

## Dependency Baseline

- 根模块继续由 Eino core `v0.9.13` 提供 MVS 基线，并精确锁定
  `github.com/cloudwego/eino-ext/components/embedding/openai` 与
  `github.com/cloudwego/eino-ext/components/embedding/ollama` 为
  `v0.0.0-20260803030130-90a15623ddb6`（commit `90a15623ddb66465aea01fbe8c63ecc9d267acc1`）。
- Ollama extension 引入 `github.com/ollama/ollama v0.9.6`。`go.mod`、`go.sum`、`vendor/modules.txt` 与 vendor
  源必须保持一致；依赖升级重新执行合同、race、vet、license、体积和真实 Provider 门禁。

## Historical Consequences

- Eino Embedding 只获得 Provider Adapter 内的 SDK 调用权，不获得检索编排、持久化、重试、版本身份、审计或
  写回所有权。
- 离线合同通过只说明两条实现路径在受控协议 fixture 上等价，不等于真实 Provider、模型质量或生产灰度通过。
  启用某 Provider 的 `eino` 前仍需使用生产 Adapter 做真实协议 smoke，并保留 `direct` 回滚观测。
- Retriever、Rerank、完整 RAG Graph、Streaming、ToolsNode 与 Checkpoint 继续由各自独立决策和门禁约束。

## Related Decisions

- [ADR-0007](0007-no-langchain-core-dependency.md)：领域核心不绑定通用 Agent Framework。
- [ADR-0013](0013-eino-adoption-gate.md)：Eino 原始采用门禁与不可替换边界。
- [ADR-0024](0024-layered-eino-adoption.md)：Chat、Callback 与短 Graph 的分层采用。
- [ADR-0027](0027-eino-primary-ai-runtime.md)：当前 Eino-only 生产决策与 Git-based 恢复路径。
