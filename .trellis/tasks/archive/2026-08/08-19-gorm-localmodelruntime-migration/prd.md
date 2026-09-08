# Local Model Runtime 持久化迁移到 GORM

> **当前交付状态（2026-09-08）**：`completed`，已归档；最终代码 [cb935655](https://github.com/CodeZen-Lizhi/zhixu/commit/cb93565561498674cda1dc1230fed587fba66075) 已推送至 `origin/dev`，尚未部署。
> TODO 10 全部 30 个子任务已完成，最新实现与验证见 [Final 验收记录](../../2026-09/08-19-gorm-composition-pgx-convergence/research/final-acceptance.md)。下文 staged、待 Final 或未提交的描述保留其原阶段事实，现存 M9 历史升级限制仍有效。

## Goal

迁移 Local Model Runtime PostgreSQL 生命周期持久化与事务端口，保持锁和凭据语义；命令接线由 Final 负责。

## Requirements

- 迁移 internal/localmodelruntime/lifecycle_postgres.go、lifecycle_store.go 及其持久化映射；该模块无独立 adapter/postgres 目录，命令 Composition 统一由 Final 子任务负责。
- 保持本地模型生命周期、锁、租约、凭据初始化和进程重启恢复语义。
- 新增以 `foundation.TransactionScope` 为边界的 scoped lifecycle Port/Store，保持未来 Model Settings 能在 caller-owned transaction 中访问运行时事实；现有 `TxLifecycle/WithTx(pgx.Tx)` 因 Model Settings 尚未迁移暂时保留为 legacy 兼容面，不在本任务强行改签或跨事务适配。保留错误分类、取消、日志脱敏和数据库时间，不在本任务修改 cmd/**。
- 受限 runtime role 只能通过迁移授予的 `SECURITY DEFINER` 命令函数和只读投影访问；本任务不改 `cmd/local-model-runtime-credential-init` 的管理员 pgx、`ALTER ROLE` 或凭据文件安全边界，该命令属于 Final 的安全 allowlist。
- 使用现有测试与局部编译；TODO 9 前不得切换生产实现。

## Acceptance Criteria

- [x] Local Model Runtime 生命周期持久化迁移到 GORM，锁/凭据/恢复行为不变。
- [x] TODO 9 验收时，staged GORM 使用同一完整 `platformpostgres.Pool` 的连接/事务配置；本 child 不修改 Composition，也不创建第二个物理池。生产命令接线仍由 Final 负责。
- [x] 错误、取消、日志和回滚检查通过。
- [x] git diff --check、受影响包编译及现有相关测试通过。

- [x] TODO 9 已完成；本 child 仍不切换 Composition，TODO 3 仅阻断 Final。

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.

## Dependencies

- 依赖 gorm-platform-transaction-foundation；Model Settings 在本任务完成后接入相关运行时契约。
