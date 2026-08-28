# Health Repository 迁移到 GORM

## Goal

迁移 Health PostgreSQL Repository，保持 Scan/Schedule、SKIP LOCKED、Collection 与 River/Event 协作。

## Requirements

- 迁移 internal/health/adapter/postgres 与 internal/health/adapter/collection/membership.go，使用统一 GORM 事务 Port；membership 不得继续直接调用 Collection PostgreSQL adapter 或 pgx.Tx。
- 保持 Scan/Schedule、SKIP LOCKED、租约、重试、Collection 过滤和 River/Event 协作语义。
- 通过官方 River database/sql 互操作处理事务插入；保留 Worker/listener、锁顺序、取消和错误分类。
- 使用现有测试与局部编译；TODO 9 前不得切换生产实现。

## Acceptance Criteria

- [ ] Health Repository 读写迁移到 GORM，扫描/调度和租约行为不变。
- [ ] SKIP LOCKED、重试/释放、Collection 条件和事件/River 原子性可复核。
- [ ] 不新增具体 pgx 依赖，错误、取消和日志检查通过。
- [ ] git diff --check、受影响包编译及现有相关测试通过。

- [ ] TODO 9 不可用时仅保留行为基线或未接入 Composition 的实现，不得勾选完成或归档；TODO 3 仅阻断 Final。

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.

## Dependencies

- 依赖 gorm-platform-transaction-foundation、gorm-collection-migration、gorm-workflow-migration、gorm-events-migration。
