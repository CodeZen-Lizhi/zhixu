# M2 Eino 隔离 PoC

## Goal

在不污染正式领域模型、Workflow 状态机和主 `go.mod` 的前提下，验证 Eino 能否作为可替换的 Agent/Application 边缘实现。PoC 必须产生可重复测试、门禁报告和明确的采用/回退结论。

## Requirements

- 使用独立 `poc/eino` Go module，首轮固定稳定版 Eino `v0.9.12` 与 OpenAI 扩展 `v0.1.13`；不采用预发布版本。
- 项目自有输入、输出、错误、权限和 Node 执行接口不得暴露 Eino 类型。
- 分组验证 Chat、Embedding、Retriever/Rerank 接入、Streaming 取消与资源释放、Structured Output/有限修复、Tool Calling 权限隔离、Callback/Trace、错误分类、限流和 Node Executor 集成。
- 单元与 Contract Test 使用确定性 Fake；真实 OpenAI-Compatible 测试仅通过显式环境变量启用，缺少凭据时明确 Skip，不得走假成功路径。
- 使用 `ToolCallingChatModel.WithTools`，禁止采用已弃用且可能原地修改模型的 `BindTools`。
- 所有外部调用必须有 Context、超时和可验证的资源关闭；错误必须映射为项目自有错误类别。
- 任一关键门禁失败时结论为“不采用”，正式实现继续使用直接 OpenAI-Compatible Adapter，调用方契约不变。

## Acceptance Criteria

- [ ] `poc/eino` 独立模块可在无真实模型凭据时完成 `go test -race ./...`、`go vet ./...`。
- [ ] Chat Graph 的正常输出、模型错误和 Context 取消均有测试。
- [ ] Streaming 能验证取消后停止消费并关闭 StreamReader，无 goroutine 泄漏证据。
- [ ] Structured Output 覆盖合法结果、一次有限修复成功、修复耗尽失败。
- [ ] Tool Calling 覆盖允许、拒绝、未知工具和参数校验失败，权限拒绝发生在真实执行前。
- [ ] Callback/Trace 能关联项目 `request_id/workflow_run_id/node_run_id`，且日志不包含 API Key、Authorization 或原始 Secret。
- [ ] Embedding、Retriever/Rerank 和 Node Executor 通过项目自有接口完成 Contract Test。
- [ ] 并发调用不依赖可变共享 Tool 绑定，限流与超时行为可重复验证。
- [ ] 可选真实模型 Smoke 通过环境变量运行，并清楚记录未运行原因或结果。
- [ ] 形成 `report.md`，逐项给出 PASS/FAIL、证据、采用结论、残余风险和正式集成影响。
- [ ] 主 `go.mod`、领域对象、Proposal/Approval 和 Workflow 持久化不依赖 Eino。

## Out of Scope

- 不实现正式 AI 业务功能、RAG、Proposal、River Worker 或持久化 Workflow。
- 不把 PoC API 暴露为产品接口，不在 Web 页面提供演示入口。
- 不因 PoC 引入第二套领域模型、权限系统、Trace ID 或错误码。
