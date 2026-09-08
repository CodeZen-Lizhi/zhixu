# Health Repository 迁移到 GORM

> **当前交付状态（2026-09-08）**：`completed`，已归档；最终代码 [cb935655](https://github.com/CodeZen-Lizhi/zhixu/commit/cb93565561498674cda1dc1230fed587fba66075) 已推送至 `origin/dev`，尚未部署。
> TODO 10 全部 30 个子任务已完成，最新实现与验证见 [Final 验收记录](../../2026-09/08-19-gorm-composition-pgx-convergence/research/final-acceptance.md)。下文 staged、待 Final 或未提交的描述保留其原阶段事实，现存 M9 历史升级限制仍有效。

## Goal

迁移 Health PostgreSQL Repository，保持 Scan/Schedule、SKIP LOCKED、Collection 与 River/Event 协作。

## Requirements

- 迁移 internal/health/adapter/postgres 与 internal/health/adapter/collection/membership.go，使用统一 GORM 事务 Port；membership 不得继续直接调用 Collection PostgreSQL adapter 或 pgx.Tx。
- 保持 Scan/Schedule、SKIP LOCKED、租约、重试、Collection 过滤和 River/Event 协作语义。
- 通过官方 River database/sql 互操作处理事务插入；保留 Worker/listener、锁顺序、取消和错误分类。
- 使用现有测试与局部编译；TODO 9 前不得切换生产实现。

## Acceptance Criteria

- [x] Health Repository 读写迁移到 GORM，扫描/调度和租约行为不变。
- [x] SKIP LOCKED、重试/释放、Collection 条件和事件/River 原子性可复核。
- [x] 不新增具体 pgx 依赖，错误、取消和日志检查通过。
- [x] git diff --check、受影响包编译及现有相关测试通过。

- [x] TODO 9 已由 `gorm-prerequisites` 完成；本 child 仅交付未接入生产 Composition 的 staged 实现，TODO 3 仍仅阻断 Final。

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.

## Dependencies

- 依赖 gorm-platform-transaction-foundation、gorm-collection-migration、gorm-workflow-migration、gorm-events-migration。
