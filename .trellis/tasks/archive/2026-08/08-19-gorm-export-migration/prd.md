# Export Repository 迁移到 GORM

## Goal

分阶段实现 Export PostgreSQL Repository 与 River dispatcher 的 GORM 路径，保持 Job 生命周期、side fact、清理、下载和恢复语义，为 Final child 的生产切换提供已验证实现。

## Requirements

- 仅修改 `internal/export/adapter/postgres`、直接 River dispatcher 适配和相关测试；Application/Domain 公共契约保持不变。
- 复用现有显式 SQL 与统一 `foundation.UnitOfWork`，保持 DB-time、幂等、租约、CAS、稳定分页、Audit/Event 原子写、清理、下载和失败恢复语义。
- GORM 公共构造器不得暴露 `*gorm.DB`、`*sql.Tx`、`pgx.Tx` 或 `any`；legacy pgx 依赖只能留在私有兼容适配层。
- River 围栏检查与 job insert 必须通过官方 `database/sql` 互操作适配在同一 UoW 内提交或回滚，现有 pgx Worker/listener 保持可消费。
- Export Create 与 River enqueue 继续遵循现有“先提交业务事实、再投递并由 PENDING 恢复”的契约，本 child 不新增跨 Repository/Dispatcher 的伪原子事务。
- 本 child 只提供 staged 实现；生产 Composition 切换与 legacy 删除统一留给 Final child。

## Acceptance Criteria

- [x] Export Repository 的 GORM staged 路径通过 legacy/GORM 双实现真实 PostgreSQL 行为测试，Job 状态、side fact、清理和下载行为一致。
- [x] River 围栏与唯一 job insert 同 UoW 提交/回滚，现有 pgx Worker 可消费首次和 completed 后的恢复 job。
- [x] 公共 GORM 契约保持强类型，未新增跨模块 pgx 依赖；错误分类、context cause、连接释放和 fail-closed 行为通过检查。
- [x] `git diff --check`、受影响包 race test、vet、编译、依赖校验和定向集成测试通过。
- [x] 生产 Composition 和 legacy 实现保持不变，交由 Final child 统一切换和删除。

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.

## Dependencies

- 已使用 `gorm-platform-transaction-foundation`、`gorm-workflow-migration`、`gorm-events-migration`、`gorm-audit-migration` 提供的 UoW、scoped inserter 和 scoped appender。
