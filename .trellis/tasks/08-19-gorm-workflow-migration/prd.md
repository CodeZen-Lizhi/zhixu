# Workflow Repository 迁移到 GORM

## Goal

迁移 Workflow PostgreSQL Repository 与 River 事务边界，保持 Runtime/Node/Outbox、租约和终态 Hook。

## Requirements

- 仅迁移 internal/workflow/adapter/postgres 与 River 事务适配，使用统一 GORM 事务 Port。
- 保持 Runtime/Node/Outbox、租约、终态 Hook、幂等和调度状态机语义。
- 在 `internal/workflow/application` 提供 `ScopedWorkspaceAnalysisExecutionFence`：接收 `foundation.TransactionScope` 与 Workspace/Workflow Run/Node Run/Node Attempt 身份，返回 immutable definition/run/node/attempt lease-cancel 快照、`found` 和 error；公开签名不得含 `any`、GORM、database/sql 或 pgx。
- GORM fence Adapter 在 caller-owned scope 内按 Workflow Run -> Node Run -> Node Attempt 执行 `FOR UPDATE`，验证 Workspace 与父子 binding；不得锁 Agent 表、读取 Agent 预算/DB clock、管理事务或 fallback root。
- 任一行缺失或父子 binding 不匹配返回 `found=false,nil`；invalid/scope/context/SQLSTATE/依赖错误使用可识别类别并保留 cause chain，供 Agent owner 翻译，禁止依赖 Workflow 文案解析。
- 为 Tools owner 新增 `ScopedToolExecutionPolicySnapshot` 与 `ScopedToolCallRecoveryFence`：前者在 caller scope
  内按 legacy `FOR SHARE` 返回 Definition/Run/Node/Attempt/DB-time 原始快照，不替 Tools 执行 live admission；
  后者保持 Node Run -> Node Attempt 的 `FOR UPDATE SKIP LOCKED`，显式区分 missing、skipped 与 stale。
- 两个 Tool scoped Port 不访问 `workflow.tool_call` 或 Agent 表，不管理 caller transaction、不缓存 scope、
  不 fallback root；Tools commit response-loss recovery 不得重新调用 live policy/fence。
- 按父任务 River/事务调用点矩阵让 GORM runtime_start/runtime_state 只消费 scoped JobInserter/EnqueueFence；业务事务内 River insert 使用官方 riverdatabasesql，现有 riverpgxv5 Worker/listener/migration 保持兼容。Model Settings scoped EnqueueFence 未交付前不得绕过 drain 或切生产。
- 新增 GORM/scoped Application Port 和 Adapter 公共 API 不得泄漏 `any`、GORM、database/sql 或 pgx；legacy `StartTx(pgx.Tx)` 与 pgx-only Hook 在各消费 owner 迁移前列为临时 allowlist，不改签、不删除。
- 使用现有测试与局部编译；TODO 9 交付前该 child 仅保留 staged 实现。当前仍不得切换生产实现或删除旧 adapter。

## Acceptance Criteria

- [ ] Workflow Repository 读写迁移到 GORM，Runtime/Node/Outbox 和租约行为不变。
- [ ] `ScopedWorkspaceAnalysisExecutionFence` 在同一 scope 内保持 Run -> Node Run -> Node Attempt 锁序、完整快照、`found=false` 与稳定错误合同；不访问 Agent 表、不泄漏数据库类型且不管理 caller transaction。
- [ ] Tool policy snapshot 与 stale recovery fence 保持 legacy `FOR SHARE` / Node -> Attempt `SKIP LOCKED`
      语义，返回原始事实且不访问 Tool Call/Agent owner 表。
- [ ] 业务写入与 River job insert 同事务提交/回滚，Worker 可消费且无重复/丢失语义变化。
- [ ] 新增 GORM/scoped API 不泄漏数据库类型，legacy pgx allowlist 无新增调用；错误、取消、日志和连接池检查通过。
- [ ] git diff --check、受影响包编译及现有相关测试通过。

- [ ] TODO 9 工厂可用；本 child 仍不得切换生产实现或删除 legacy，TODO 3 仅阻断 Final。

## 2026-09-01 测试范围调整

按父任务精简政策，Workflow 保留 Repository/Runtime 主路径及直接改动的 River/Outbox/scoped fence 代表性原子性场景；response-loss、SIGKILL、EXPLAIN、跨 owner 端到端和整包 race 按风险触发。

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.

## Dependencies

- 依赖 gorm-platform-transaction-foundation；Export、Retrieval、Health、Agent、Tools、Conversation、Organizing 依赖 Workflow 的事务/River Port。Agent Workspace Analysis Model Operation 明确依赖本 child 的 `ScopedWorkspaceAnalysisExecutionFence`；Tools 明确依赖本 child 的 Tool policy snapshot 与 recovery fence。
