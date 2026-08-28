# Local Model Runtime 持久化迁移到 GORM

## Goal

迁移 Local Model Runtime PostgreSQL 生命周期持久化与事务端口，保持锁和凭据语义；命令接线由 Final 负责。

## Requirements

- 迁移 internal/localmodelruntime/lifecycle_postgres.go、lifecycle_store.go 及其持久化映射；该模块无独立 adapter/postgres 目录，命令 Composition 统一由 Final 子任务负责。
- 保持本地模型生命周期、锁、租约、凭据初始化和进程重启恢复语义。
- 新增以 `foundation.TransactionScope` 为边界的 scoped lifecycle Port/Store，保持未来 Model Settings 能在 caller-owned transaction 中访问运行时事实；现有 `TxLifecycle/WithTx(pgx.Tx)` 因 Model Settings 尚未迁移暂时保留为 legacy 兼容面，不在本任务强行改签或跨事务适配。保留错误分类、取消、日志脱敏和数据库时间，不在本任务修改 cmd/**。
- 受限 runtime role 只能通过迁移授予的 `SECURITY DEFINER` 命令函数和只读投影访问；本任务不改 `cmd/local-model-runtime-credential-init` 的管理员 pgx、`ALTER ROLE` 或凭据文件安全边界，该命令属于 Final 的安全 allowlist。
- 使用现有测试与局部编译；TODO 9 前不得切换生产实现。

## Acceptance Criteria

- [ ] Local Model Runtime 生命周期持久化迁移到 GORM，锁/凭据/恢复行为不变。
- [ ] Final/TODO 9 验收时，命令与后台 worker 使用同一连接/事务配置；本 child 不修改 Composition，也不创建第二个物理池。
- [ ] 错误、取消、日志和回滚检查通过。
- [ ] git diff --check、受影响包编译及现有相关测试通过。

- [ ] TODO 9 不可用时仅保留行为基线或未接入 Composition 的实现，不得勾选完成或归档；TODO 3 仅阻断 Final。

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.

## Dependencies

- 依赖 gorm-platform-transaction-foundation；Model Settings 在本任务完成后接入相关运行时契约。
