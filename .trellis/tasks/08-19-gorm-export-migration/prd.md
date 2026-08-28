# Export Repository 迁移到 GORM

## Goal

迁移 Export PostgreSQL Repository，保持 Job 生命周期、Audit/Event 原子写和清理下载语义。

## Requirements

- 仅迁移 internal/export/adapter/postgres 及直接 dispatcher 适配，使用统一 GORM 事务 Port。
- 保持 Job 生命周期、幂等/租约、Audit/Event 原子写、下载清理和失败恢复语义。
- River 事务插入必须通过官方 database/sql 互操作适配，不在业务层直接绑定 pgx.Tx；保留 Worker/listener 现状。
- 使用现有测试和局部编译；TODO 9 前不得切换生产实现。

## Acceptance Criteria

- [ ] Export Repository 读写迁移到 GORM，Job 状态与清理行为不变。
- [ ] 业务写入与 River job insert 在同一事务中提交/回滚，且 Worker 可继续消费。
- [ ] 不新增跨模块具体 postgres/pgx 依赖，错误、取消和日志检查通过。
- [ ] git diff --check、受影响包编译及现有相关测试通过。

- [ ] TODO 9 不可用时仅保留行为基线或未接入 Composition 的实现，不得勾选完成或归档；TODO 3 仅阻断 Final。
 
## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.

## Dependencies

- 依赖 gorm-platform-transaction-foundation、gorm-workflow-migration、gorm-events-migration、gorm-audit-migration。
