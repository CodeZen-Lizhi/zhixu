# Audit Store 迁移到 GORM

> **当前交付状态（2026-09-08）**：`completed`，已归档；最终代码 [cb935655](https://github.com/CodeZen-Lizhi/zhixu/commit/cb93565561498674cda1dc1230fed587fba66075) 已推送至 `origin/dev`，尚未部署。
> TODO 10 全部 30 个子任务已完成，最新实现与验证见 [Final 验收记录](../../2026-09/08-19-gorm-composition-pgx-convergence/research/final-acceptance.md)。下文 staged、待 Final 或未提交的描述保留其原阶段事实，现存 M9 历史升级限制仍有效。

## Goal

迁移 Audit PostgreSQL Store 与事务追加边界，保持脱敏、精确重放和有界查询。

## Requirements

- 迁移 `internal/audit/adapter/postgres`，并在 `internal/audit/application` 新增基于
  `foundation.TransactionScope` 的稳定追加 Port；保留现有 `transaction any` Port 供尚未迁移的 pgx 调用方使用。
- 新增未接生产 Composition 的 GORM Store；独立追加必须使用共享 Unit of Work，调用方事务追加必须加入同一个 opaque scope，禁止自行开启第二事务。
- 保持脱敏规则、事件精确重放、审计追加顺序、时间范围和有界查询语义。
- 保留参数化过滤、`NULL` Workspace advisory lock、稳定 keyset、分页上限、SQLSTATE、取消和日志规范；不得在 ORM 层绕过审计策略。
- 不修改 migration，不使用 `AutoMigrate`/`Migrator`、association、soft delete 或 GORM Hook；`transaction_id` 继续由 PostgreSQL 默认值拥有。
- 使用现有测试与局部编译；TODO 9 通过后仍不得切换生产组合或删除 legacy Store，二者只属于 Final。

## Acceptance Criteria

- [x] Audit Store 读写和追加路径迁移到 GORM，审计数据格式和查询结果不变。
- [x] 新 scoped Port 不暴露 GORM、`database/sql`、pgx 或 `any`；独立追加与调用方追加保持同事务、精确重放和失败回滚。
- [x] 敏感字段在持久化与日志中仍按既有策略处理，查询具有明确上限、Workspace 隔离和稳定排序。
- [x] 事务失败、并发幂等、取消和错误翻译行为在真实 PostgreSQL 上通过 legacy/GORM 等价检查。
- [x] git diff --check、受影响包编译及现有相关测试通过。

- [x] TODO 9 工厂已用共享 Testcontainers Pool 完成 Audit 实库门禁；生产仍保持 legacy，TODO 3 仅阻断 Final。

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.

## Dependencies

- 依赖 gorm-platform-transaction-foundation；Agent、Tools 等后续模块依赖审计 Port。
