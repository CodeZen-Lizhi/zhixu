# Conversation GORM 迁移设计

## 边界与依据

用户于 2026-09-08 明确要求完成 TODO 10 的全部未完成子任务。本设计细化父任务已批准的等价迁移方案，不改变产品行为或验收语义。

范围是 `internal/conversation/adapter/postgres`，以及直接参与事务的 Application Port。现有 `repository.go`、`conversations.go`、`turns.go`、`feedback.go` 拥有列表和命令；`dispatch.go` 拥有 Question、Workflow Start 与 Workspace Analysis 的原子创建；`finalizer.go`、`draft_stream.go`、`workspace_analysis_*` 拥有终态、草稿和审计。保持现有校验、错误码、分页、SQL 锁顺序和 response-loss 恢复。

## 目标实现

- 从单个 `platformpostgres.Pool` 获取 GORM root 与 `foundation.UnitOfWork`，不新建连接池。Persistence Model 显式映射现有 Atlas 表、列、nullable 和时间，不使用 AutoMigrate。
- 常规 CRUD 使用 GORM Model/Query API；关联投影、批量、行锁、数据库时间、幂等和复杂状态转换保留参数化 GORM Raw/Exec。复用现有 codec/领域校验，不实现通用 SQL 转译器或 pgx 兼容驱动。
- 同事务协作者使用 `eventsapplication.ScopedAppender`、Agent 已有 `ScopedModelRunStore`、`ScopedWorkspaceAnalysisRunStarter`、Workflow `ScopedRuntimeStarter` 和 scoped control/terminal hooks。事务参数统一为 `foundation.TransactionScope`，不得把 scope 转回旧 pgx Tx 或另开事务。
- 按原顺序锁定 Conversation、Answer、ModelRun/WorkspaceAnalysis、Draft 和 Workflow Attempt；Draft PUBLISHED、正式 Answer、模型终态、事件与 Audit 同事务提交。保留 pending/terminal 的精确重放、lease fence、取消和未知提交结果的查证。
- GORM 实现先独立验收。2026-09-08 主会话放行 Final 后，本 owner 已删除 Conversation legacy 和过渡端口；API/Worker 构造由主会话统一替换。

## 依赖、验证与回滚

依赖 Foundation、Events/Audit、Agent/Tools 与 Workflow 已有 scoped 契约；新增 owner 能力由 owner 提供，不直接 import 其他 PostgreSQL Repository。使用原有 Repository/dispatch/finalizer/draft integration fixture 接入共享 Testcontainers Pool；主读写场景和跨 owner 提交/回滚必验，取消/response-loss 按直接改动风险选择现有场景。保持 Schema 不变；回滚以 Adapter 和 Final 接线的版本边界为单位。
