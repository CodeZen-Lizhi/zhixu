# Tool/ReAct 产品门禁

> 历史记录：该 No-Go 基于当时没有生产消费者。2026-08-08 用户已明确要求正式采用 Eino Tool Calling/Agent；当前执行合同以本任务最新 PRD/design/implement 为准。

## 结论

**No-Go：当前不接入生产 Eino ChatModelAgent/ToolsNode。** 原因是产品合同和调用方不存在，不是 Eino 能力不足。

## 仓库证据

- `docs/architecture/tool-security.md` 明确当前没有公共 Tool Execute API；可执行 Tool 只接受服务端持久 Workflow/Node/Attempt 身份。
- `docs/architecture/agent-rag-architecture.md` 明确生产 RAG 是 retrieval-first 单节点 Workflow，仍未开放通用模型 Tool Loop，预算也未实现。
- `internal/agent/adapter/workflow/rag_executor.go`、`internal/artifact/workflow/executor.go` 和 `internal/organizing/workflow/generation.go` 都明确是无 Tool Loop 路径。
- `poc/eino/tooltrace` 已证明 Eino ToolsNode 的 schema/dispatch 机械能力，因此本结论不能表述为“框架不支持”。

## 缺失的进入条件

1. 没有具体页面、API 或内部 Workflow 需要模型多轮选择工具。
2. 没有该产品入口的允许工具版本集合、最大轮数、调用/Token/时间预算和终态合同。
3. 没有把模型 Tool Request 转成持久 Invocation/Attempt 的生产 Node；现有 Safe Writeback 只接受已批准、强绑定的服务端授权。
4. 没有为 ChatModelAgent 内部模型调用定义 Recording、重试和 failover 事实边界。

## 重开条件

新增具体产品 PRD和入口，并冻结只读/Proposal 工具白名单、持久 Attempt、审批/终态、预算、审计及独立采用 ADR 后，优先采用经典 `schema.Message` ChatModelAgent/ToolsNode；批准后的写回仍由独立项目 Workflow 执行。

## 验证

- `rg` 只找到产品/架构文档明确“不提供公共 `/tools/{name}:execute` API”和“未开放通用模型 Tool Loop”，没有 OpenAPI、HTTP route 或前端消费者。
- `go test -race ./tooltrace` 在 `poc/eino` 通过，证明 Eino ToolsNode 的通用 schema/dispatch 能力仍可用；No-Go 只针对缺少生产产品合同。
