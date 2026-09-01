# Organizing Repository 迁移到 GORM

## Goal

迁移 Organizing PostgreSQL Repository 与 Owner/Workflow 事务 Adapter，保持 Serializable snapshot 和终态结果。

## Requirements

- 仅迁移 internal/organizing/adapter/postgres 及 Owner/Workflow transaction adapter，使用统一 GORM 事务 Port。
- 保持 Serializable snapshot、Owner fence、终态结果、知识/检索聚合和重试语义。
- 移除对知识/检索具体 postgres adapter 与 pgx.Tx 的业务层耦合，平台边界仅保留审计 allowlist。
- 使用现有测试与局部编译；TODO 9 工厂已可用，但本 child 不得切换生产组合或删除旧实现。

## Acceptance Criteria

- [ ] Organizing Repository 与事务 adapter 迁移到 GORM，Serializable/终态行为不变。
- [ ] Owner fence 和 Knowledge/Retrieval 协作通过稳定 Port，公共层不再断言具体 pgx.Tx。
- [ ] 冲突重试、回滚、错误/取消/日志检查通过。
- [ ] git diff --check、受影响包编译及现有相关测试通过。

- [ ] TODO 9 工厂可用；本 child 仍不得切换生产组合或删除 legacy，TODO 3 仅阻断 Final。

## 2026-09-01 测试范围调整

按父任务精简政策，Organizing 保留 Owner/Workflow 主路径和直接改动的 Serializable/终态事务代表场景；重试、response-loss、跨模块端到端与容量专项仅按风险触发。
 
## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.

## Dependencies

- 依赖 gorm-platform-transaction-foundation、gorm-knowledge-migration、gorm-retrieval-migration、gorm-workflow-migration；Owner/Workflow 具体 Adapter 依赖必须在本任务内收敛为稳定 Port。
