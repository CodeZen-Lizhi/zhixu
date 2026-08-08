---
status: accepted
---

# Eino 分层采用、Chat/Callback 与 Structured Output 短 Graph 灰度

项目需要正式采用 Eino 的通用模型能力，同时不能改变知序已经验证的持久工作流、安全传输、模型调用审计、领域校验和安全写回边界。ADR-0013 的 M2 结论基于当时未闭合的 PoC 门禁；当前采用范围收敛为可由项目 Port 隔离和回滚的 Chat Provider Adapter、只写脱敏 Trace/Metrics 的调用级 Callback telemetry，以及 `StructuredRunner` 内部无状态三阶段短 Graph。

## Decision

- 本 ADR 仅 supersede ADR-0013 中“主模块不正式采用 Eino”的结论。ADR-0013 关于 Domain、PostgreSQL/River Workflow、Proposal/Approval、Tool Permission、Write Authorization、Evidence 和领域不变量的限制继续有效。
- 根模块锁定 Eino core `v0.9.13` 与 Eino OpenAI extension `v0.1.13`。二者采用 Apache-2.0 License；Embedding extension、ADK、Checkpoint/Interrupt、ToolsNode、Token Streaming 和长期 Agent 状态不进入本阶段生产依赖或运行路径。
- Eino OpenAI ChatModel 只作为 `internal/platform/models` 内部实现，并继续实现项目自有 `agentapplication.ChatModel` 与 `ChatContract`。Eino/Provider SDK 类型不得进入 Domain、Application、Workflow 输入、Model Run/Call 或 HTTP API。
- `direct|eino` 是进程级实现选择，不是 Provider/Model 身份。选择器不得复用 `ChatAdapterVersion`，API 与 Worker 必须从相同冻结配置构造唯一 `ModelRuntime`。真实 Provider smoke 已通过，但默认仍按发布策略保持 `direct`；`eino` 显式灰度启用，回滚只切回 `direct`。
- Eino Adapter 必须复用项目的安全 HTTP client：远程仅 HTTPS、仅精确 loopback relay 可用 HTTP、禁止 redirect、逐连接 DNS/IP 校验、TLS 1.2 下限和 `Proxy=nil`。Credential 只进入构造出的私有 Adapter，不进入 Runtime contract、日志、错误或格式化输出。
- 每次调用先执行项目 `ValidateChatRequest`，使用请求级 payload modifier 注入冻结 JSON Schema，并保持 `max_tokens`、消息、Schema 名称和严格模式与 direct Adapter 等价；共享模型上不保存可变 Schema 或 Tool 列表。
- 成功与错误响应均由项目 transport wrapper 有界读取。成功响应必须是 `application/json`，并继续执行严格 JSON、单 choice、assistant、`finish_reason=stop`、无 refusal/tool call、model echo、完整 usage、总量和响应字节校验；Eino 输出仍是不受信候选。
- 已知 `message.reasoning`/`reasoning_content` 只允许 string/null，严格解码后立即丢弃，不进入项目响应、持久审计或 telemetry；其他未知 envelope/message 字段继续拒绝。
- Adapter 不启用自动 retry、fallback model、stream、tool 或 checkpoint。取消、deadline、429、502/503/504、401/403、redirect、Provider 拒绝和响应合同错误继续映射为既有稳定项目错误，由 Workflow 决定 Node Attempt 重试。
- `RecordingChatModel` 保持在 Eino Adapter 外层，Model Run/Call 与 append-only Audit 继续是项目事实；阶段 2 Eino callback 只作为脱敏 telemetry 旁路，不写任何事务事实。
- `StructuredRunner` 新增项目自有 `StructuredPhaseScheduler` Port；direct 实现仍是默认，Eino `compose.Graph` 只在
  `internal/agent/adapter/eino` 驱动 `INITIAL -> REPAIR -> REDUCED`。Graph 节点只能调用 Application 的
  `StructuredPhaseRun.Advance`，严格 decoder、原始响应字节闭包、冻结 Schema、token/byte/time budget、错误和
  三次硬上限继续由项目代码拥有。
- RAG、Relation、Artifact、Capture 和 Organizing 使用五个独立 `direct|eino` Worker selector 灰度。selector
  不进入 Provider/Model/Adapter 身份、managed model revision、Workflow input 或持久记录；任一消费者可单独切回 direct。
- 短 Graph 不使用 checkpoint、自动 retry、持久 state、Tool、DB/file/Git 或外部副作用。所有模型调用仍进入
  `RecordingChatModel`，PostgreSQL/River 继续是持久工作流唯一事实源。
- 阶段 4 的只读 Tool Calling 本任务 No-Go：当前没有可从生产 Chat/RAG 提供持久 Attempt、lease/fence 和确定性
  `call_no` 的可信入口，现有 Chat 合同也明确拒绝 `ToolCalls`。在独立 Tool Calling model Port 和持久 Tool-loop
  Workflow Node 获批前，不注册 Eino ToolsNode。

## Contract Baseline

- 同一 request/response fixture 在 direct 与 Eino 实现上必须得到相同的 `ChatContract`、`ChatResponse`、usage 与稳定错误分类。
- 合同测试必须覆盖 BaseURL 为目录、`/v1` 和完整 `/v1/chat/completions` 三种形式，禁止 SDK 重复追加路径。
- Eino SDK 内部模型名不得触发底层 OpenAI SDK 的历史模型 denylist；wire payload 必须继续携带项目冻结的真实 ModelID，并与 direct Adapter 对照。
- 动态 Schema 测试必须覆盖 `INITIAL/REPAIR/REDUCED` 形态及并发隔离；任一请求不得观察到其他请求的 Schema。
- 故障测试必须覆盖 request/response 上限、缺 usage、model mismatch、finish reason、tool call、未知 message 字段、错误 reasoning 类型、429/5xx/401、取消、deadline、redirect 和敏感错误体。
- API、Worker 与 Model Settings connection test 继续通过同一 factory/runtime 构造 Adapter，且 Runtime 不保存原始 `config.Config`。
- Callback handler 必须由每次 `Chat` 通过 Context 独立注入，禁止全局 handler；它不得读取 input/output/raw error，只发射固定 component、phase、result、稳定 error code、耗时和已有 Trace correlation。Exporter error/panic 不得改变 Chat response/error。

## Dependency Baseline

- `go list -m all` 的模块图从阶段前的 108 个模块增加到 173 个；新增生产 direct dependency 只有 Eino core 与 OpenAI extension，ACL/OpenAI SDK 等均由 `go mod tidy` 记录为 indirect dependency。
- 本地 `du -sk vendor` 从 16080 KiB 增至 37888 KiB（增加 21808 KiB）。`vendor/modules.txt` 精确记录 Eino `v0.9.13`、OpenAI extension `v0.1.13` 与 ACL `v0.1.17`。
- Eino core vendor 中携带 `LICENSE-APACHE`，底层 `go-openai` 携带 Apache-2.0 `LICENSE`；Eino extension/ACL 源文件同样声明 Apache-2.0。依赖升级必须重新核对许可证与体积。
- CI 已以根 `go.sum` 作为 Go cache key；Dockerfile 已复制根 `vendor/` 并使用 `-mod=vendor` 构建，因此本阶段无需为 Eino 增加第二份 cache、module download 或镜像构建路径。

## Considered Options

- 让 Eino 接管 Workflow、RAG、审批、Tool 执行和恢复状态。
- 一次性移除 direct Adapter 并默认切换 Eino。
- 在既有项目 Port 后增加可灰度、可回滚的 Eino Chat Adapter。
- 在 `StructuredRunner` 内增加固定短 Graph，同时保持五个消费者独立回滚。
- 继续永久隔离 PoC，不在主模块采用 Eino。

## Consequences

- 根模块和 vendor 增加 Eino、OpenAI extension 及其间接依赖；CI/Docker 继续以根 `go.sum` 和 `vendor/` 为唯一构建输入。
- 生产 Eino Adapter 已用本地 Ollama `0.32.6` + `qwen3:0.6b` 连续通过两次真实 OpenAI-Compatible smoke；该结果只证明协议兼容，不是模型质量、生产灰度或删除回退实现的证据。默认仍为 `direct`，可显式选择 `eino` 进行受控灰度。
- Framework 升级必须重新通过同一合同、race、vet、Provider smoke 和错误注入门禁。升级失败或 Provider 兼容性漂移时切回 `direct`，不迁移数据库或持久工作流状态。
- 调用级 Callback/Trace 与 Structured Output 三阶段短 Graph 已通过离线聚焦门禁；五个消费者的计数包装器证明实际进入 Eino scheduler，真实 PostgreSQL/River RAG direct/Eino 门禁也证明完成 Node 的 transport 重投递不会重复 Provider、Model Call、Attempt 或业务终态。Graph 只获得进程内阶段调度权，不获得业务编排、持久化或审计所有权。
- Embedding/Retriever/Rerank、完整 RAG Graph、Token Streaming、Checkpoint/Interrupt、只读 Tool Calling 和旧实现删除仍需独立门禁；本 ADR 不提前授权这些能力。

## Related Decisions

- [ADR-0006](0006-postgres-durable-workflow.md)：PostgreSQL 持久化 Workflow。
- [ADR-0007](0007-no-langchain-core-dependency.md)：领域核心不绑定通用 Agent Framework。
- [ADR-0012](0012-version-workflows-prompts-schemas.md)：Workflow、Prompt、Schema 版本化。
- [ADR-0013](0013-eino-adoption-gate.md)：Eino 原始采用门禁与继续有效的不可替换边界。
