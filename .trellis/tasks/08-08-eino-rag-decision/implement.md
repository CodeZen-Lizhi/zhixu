# Eino RAG Graph 正式迁移实施

1. 已冻结现有 RAG 输入、三终态、ModelRun/Call、retrieval tuple、evidence、progress 和 finalizer fixture。
2. 已定义不含 Eino 类型的 `RAGExecutionScheduler` Port 和 adapter 私有 Graph state，并通过 import boundary 检查。
3. 已将 `RAGExecutor` 收口为 Plan/Retrieval/Evidence/Generation/Publication 五个显式领域节点，不改变业务规则。
4. 已在 `internal/agent/adapter/eino` 构建并编译 Eino Graph，使用 `AddBranch` 路由 clarification/refusal/answer terminal。
5. `RAGWorkflowExecutor` 仍调用领域 `RAGExecutor.Execute`，保留 replay/freeze/finalize。
6. 已接入 NodeAttempt 级共享 Budget Ledger，并验证下游 phase 预留。
7. 已通过固定 fixture、race、真实 PostgreSQL/River 重投递、取消、response-loss 和 Compose smoke；2026-08-11
   当前树又完成 host-relay 外部 Chat + 原生 Ollama Embedding 的真实 Provider 桌面/移动浏览器闭环。该证据不冒充
   容器直连外部 HTTPS 网络路径。
8. 旧 direct scheduler 已从生产选择面和部署回滚路径移除；历史 Eino→direct→Eino 演练仅作迁移证据，稳定观察不构成删除前置条件，恢复依赖 Git 历史。
9. AgentRuntime、tool-free final stream、metadata 和 DraftStreamSink 已接入同一 Eino RAG 生产组合，不再是待迁移子图。

验证至少覆盖：

```bash
go test -race ./internal/agent/application ./internal/agent/adapter/eino ./internal/agent/adapter/workflow
go vet ./internal/agent/... ./cmd/worker
git diff --check
```
