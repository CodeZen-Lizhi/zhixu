# Eino PoC 门禁报告

日期：2026-07-16

> 本报告记录 ADR-0013/M2 的历史门禁时点。ADR-0019 已在不改变 Domain、Workflow、Proposal/Approval
> 边界的前提下，授权并实现主模块 Eino Chat Adapter；当前生产默认仍为 `direct`，真实 Provider Smoke
> 仍待完成。下文中的“不正式采用”“不进入主 `go.mod`”“主模块无 Eino”仅描述 2026-07-16 时点。

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

只有完成以下项目后才可重新评估采用：

1. 使用 Eino `schema.StreamReader` 验证取消、Close 和 goroutine 回收。
2. 把 Eino Chat/Graph 输出接入 Structured Output 校验与有限修复。
3. 实现 Eino Embedding/Retriever/Rerank Adapter Contract Test。
4. River 引入后完成真实 Node 集成、重试和人工恢复测试。
5. 使用显式凭据完成 OpenAI-Compatible Chat/Streaming/Tool Calling Smoke。

## 回滚

删除 `poc/eino` 及 Makefile 的 Eino 门禁目标即可；主应用、数据库、API 和正式业务模块不需要迁移。
