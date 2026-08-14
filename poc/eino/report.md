# Eino PoC 门禁报告

日期：2026-07-16

> **历史记录，不是当前发布依据。** 本报告冻结 ADR-0013/M2 的 2026-07-16 门禁结论和 2026-08-08
> 扩展复验回填。当前生产决策以 [ADR-0022](../../docs/architecture/adr/0022-eino-primary-ai-runtime.md)
> 和 [Eino Runtime 发布门禁](../../.trellis/spec/backend/eino-runtime-adoption-gates.md) 为准：Eino 已是
> Chat、Embedding、Structured/RAG、Tool Calling 与 Streaming 的唯一部署路径；2026-08-11 当前树已分别通过
> 外部 HTTPS Chat/Embedding live gate 与 host-relay Provider/browser 终态。容器直连外部 HTTPS 网络路径和真实
> 稳定观察仍未完成；旧 direct 生产实现与部署选择面已删除。下文的默认 `direct`、生产 No-Go、“不进入主
> `go.mod`”“主模块无 Eino”和 Provider `SKIP` 都只描述当时状态。

## 2026-08-08 历史扩展复验回填

| 路线 | 框架/实现证据 | 2026-08-08 当时结论 |
|---|---|---|
| Chat/Callback/短 Graph | 主模块合同、race、真实 Ollama Chat smoke 与 PostgreSQL/River 组合门禁通过 | 已按 ADR-0019 可回滚采用，默认 direct |
| OpenAI-Compatible/Ollama Embedding | 主模块 direct/Eino 四组合、wire 校验、race、vet、Compose/vendor 门禁通过 | 已按 ADR-0020 条件采用，默认 direct；真实 Provider smoke 后灰度 |
| 完整 RAG Graph | 能力可表达；实际净收益评估为原生覆盖 25/100，包装 Port 会净增实现与测试 | 生产 No-Go，保留项目 Executor，不建 Retriever bridge |
| ToolsNode/ReAct | `tooltrace` 实际 Eino ToolsNode PASS | 能力 PASS；无产品消费者/持久合同，生产 No-Go |
| Streaming | `streaming/eino_stream_test.go` 实际 `StreamableLambda`→Compile→顶层 Stream 多帧、取消、Close PASS | 能力 PASS；无 Token API/前端消费者，生产 No-Go |
| Checkpoint/Interrupt | `checkpoint` 实际 StatefulInterrupt/ResumeWithData、Attempt/Fence、AES-GCM、TTL/Delete、篡改/并发 PASS | PoC PASS；跨重启/reclaim/HITL 不兼容，生产 No-Go |
| ADK/Agentic | 经典 Message 的 Graph/Tool 证据存在；未验证生产多 Agent 消费者 | Agentic/Beta 与 v0.10 alpha 不进入默认路径 |

这些复验不会改写下方 2026-07-16 的历史表。当时决策以 ADR-0021 为准；该 ADR 后续已被 ADR-0022 的正式生产采用决策取代。

## 结论

本报告时点的结论为**不正式采用 Eino**。

Eino `v0.9.12` 的 Chat Graph、ToolsNode 和 Callback 能在独立 module 中工作，OpenAI 扩展 `v0.1.13` 也能编译并完成配置校验；但 Streaming、Structured Output、Embedding/Retriever/Rerank 和 River Node 集成尚未全部经过真实 Eino 组件边界，真实 OpenAI-Compatible Provider Smoke 也因缺少显式凭据而未运行。

按照 ADR-0013，任一关键门禁未通过就继续使用直接 OpenAI-Compatible Adapter。当前 PoC 保留为证据和后续复验入口，不进入主 `go.mod` 或正式业务代码。

## 门禁结果

| 门禁 | 结果 | 证据或缺口 |
|---|---|---|
| Chat/Graph 正常输出、失败、取消 | PASS | `chatgraph` 实际使用 Eino Graph 与 BaseChatModel |
| ToolsNode 权限、未知工具、参数校验 | PASS | `tooltrace` 实际使用 Eino ToolsNode，权限拒绝发生在 Handler 前 |
| Callback/Trace 与 Secret 脱敏 | PASS | 实际使用 Eino Callback；覆盖 Bearer/Basic/Cookie/显式 Secret |
| `WithTools` 并发不修改基础模型 | PARTIAL | 验证了接口不可变用法，但未对 OpenAI 扩展真实模型并发绑定 |
| Streaming 取消、Close、资源回收 | FAIL | helper/race 测试通过，但尚未接入 Eino `schema.StreamReader` 实际链路 |
| Structured Output/一次有限修复 | FAIL | 项目 JSON helper 测试通过，但未接入 Eino 模型/Graph 输出链路 |
| Embedding/Retriever/Rerank | FAIL | 项目 Contract Pipeline 通过，但未实例化 Eino 组件 Adapter |
| Node Executor 集成边界 | FAIL | 项目 Node Contract 通过，但 River 尚未引入，未完成 River Node 集成 |
| 限流、超时、错误分类 | PASS | 未知错误默认不可重试；写入结果未知进入 `manual_recovery` |
| OpenAI 扩展编译与配置校验 | PASS | `live` 默认离线测试与版本锁定 |
| 真实 Provider Chat Smoke | SKIP | 当前环境未配置 `ZHIXU_EINO_LIVE_API_KEY/MODEL` |
| 主模块与领域框架隔离 | PASS | 主 `go.mod` 无 Eino；Eino 类型仅位于 `poc/eino` |

## 验证

```bash
cd poc/eino
go test -race ./...
go vet ./...
```

结果：现有离线测试全部通过。测试通过只证明已覆盖的 PoC 行为，不代表上述 FAIL/SKIP 门禁已满足。

## 已验证的安全约束

- 未知 Provider/Handler 错误默认不可自动重试。
- `WRITE_PROPOSAL` 执行结果未知时返回 `manual_recovery`，避免重复副作用。
- Trace 不记录 API Key、Authorization、Cookie 或已知原始 Secret。
- HTTP BaseURL 仅允许 loopback；远程 Provider 必须使用 HTTPS。

## 后续复验条件

以下是 2026-07-16 当时列出的生产重开条件；除 Checkpoint 外，相关产品决策和生产实现已由 ADR-0022 取代：

1. Embedding：分别用目标 OpenAI-Compatible 与 Ollama Provider 完成生产 Adapter smoke、灰度和回滚观察。
2. 完整 RAG：只有出现可复用消费者、达到至少 80% 加权覆盖并能删除真实重复调度时重开。
3. Tool/ReAct：先有产品 PRD、工具白名单、预算、持久 Attempt、审批终态和独立 ADR。
4. Token Streaming：先有 Token API/前端消费者、草稿/最终态、断线、内容安全和回滚合同，再做真实
   Provider 到 HTTP/浏览器的首帧、背压和取消门禁。
5. Checkpoint：先设计 PostgreSQL 授权的 fenced one-time handoff、持久 Store、密钥轮换、拓扑/serializer
   兼容和副作用幂等；不能用 PoC 快照替代 River/PostgreSQL。

## 历史回滚

隔离 PoC 路线当时没有生产回滚动作。Chat、Embedding 和 Structured Scheduler 曾分别使用 selector 切回 direct；
数据库、API 业务合同和持久身份不迁移。`poc/eino` 保留为升级与能力回归门禁，不再作为“主模块未采用 Eino”的证据。
