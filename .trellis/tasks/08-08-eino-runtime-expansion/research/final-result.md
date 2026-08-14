# Eino Runtime 分层迁移历史收口结果

> 历史记录：本文件记录的是“按能力逐项 Go/No-Go、默认 direct”的上一版收口。2026-08-08 用户明确要求 Eino/eino-ext 成为正式生产主路径；当前执行合同以本任务最新 PRD/design/implement 和已接受的 ADR-0027 为准。本文件不再表示当前计划或完成状态。

## 实际结论

| 路线 | 结果 | 生产变化 |
|---|---|---|
| Embedding | 条件 Go | OpenAI-Compatible/Ollama 新增 Eino Adapter 与独立 `direct|eino` selector，默认 `direct` |
| 完整 RAG | No-Go | 保留 `RAGWorkflowExecutor`/`RAGExecutor`，继续复用已采用的 Eino components |
| Tool/ReAct | 框架 PoC PASS，生产 No-Go | 不新增无消费者的 Tool-loop、Agent bridge 或 selector |
| Token Streaming | 真实 Eino Stream PoC PASS，生产 No-Go | 不新增 Token API；持久阶段 SSE 不变 |
| Checkpoint/Interrupt | PoC PASS，生产 No-Go | 仅保留隔离 PoC，不接入 River/PostgreSQL 恢复 |
| ADK/Agentic | 生产 No-Go | 无产品消费者；Beta/alpha 路径不进入默认生产运行时 |

No-Go 的原因分别是净收益、产品入口和恢复所有权，不是 Eino 缺少相应能力。所有业务事实源、权限、
Evidence/Citation、Tool 审批/写回、Attempt/lease/fence 和持久终态继续由项目拥有。

## 质量证据

- 根模块：`go test ./...`、`go vet ./...`、受影响包 `go test -race`、vendor 模式入口构建、
  `go mod tidy -diff`、`go mod verify` 通过。
- Compose：runtime contract、runtime check 与 static models check 通过。
- PoC 模块：`go test -race ./...`、`go vet ./...`、`go mod tidy -diff` 通过；Checkpoint/Streaming
  额外执行 20 次 race 压测通过。
- 六个 Trellis 任务的 context validate、最终 Go review、Trellis check 与 `git diff --check` 通过。
- ADR-0025、ADR-0026、后端 Eino specs、README、架构/部署/测试/观测文档、PoC 报告和面试材料已同步。

## 历史记录中的发布前剩余门禁（已被 2026-08-11 决策取代）

- OpenAI-Compatible 与 Ollama 必须按目标环境分别执行真实 Embedding Provider smoke；一个 Provider 失败不影响
  另一个，也不能用离线 fixture 代替。
- 当时要求在默认切换和旧 direct 实现删除前保留一个完整发布观察周期并验证 selector 回滚；该要求已被 2026-08-11
  用户决定取代。当前生产不保留 direct selector/回滚入口，稳定观察作为独立质量证据继续执行，需要恢复旧实现时使用 Git 历史。
- 本次没有真实 Docker stack 启动或外部 Provider 调用；这些是发布验证，不是本地实现完成度。
