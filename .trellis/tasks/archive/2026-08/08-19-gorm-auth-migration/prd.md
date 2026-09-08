# Auth Repository 迁移到 GORM

> **当前交付状态（2026-09-08）**：`completed`，已归档；最终代码 [cb935655](https://github.com/CodeZen-Lizhi/zhixu/commit/cb93565561498674cda1dc1230fed587fba66075) 已推送至 `origin/dev`，尚未部署。
> TODO 10 全部 30 个子任务已完成，最新实现与验证见 [Final 验收记录](../../2026-09/08-19-gorm-composition-pgx-convergence/research/final-acceptance.md)。下文 staged、待 Final 或未提交的描述保留其原阶段事实，现存 M9 历史升级限制仍有效。

## Goal

迁移 Auth PostgreSQL Repository，保持 Session 轮换、撤销、数据库时间和 Credential 安全语义。

## Requirements

- 以平台事务 Port 和统一 GORM 配置执行 Repository 迁移；公共契约不得暴露 GORM、database/sql 或 pgx 类型。
- 保持 Session 轮换、撤销、数据库时间、Credential 哈希/脱敏和唯一约束语义；只将查询/写入实现替换为 GORM。
- 使用现有 schema/migration 作为事实来源，保留错误分类、上下文取消和事务回滚行为；沿用项目既有日志规范。
- 仅在既有测试、局部编译和数据库校验通过后标记完成；TODO 9 通过前允许实现和基线验证，但不得切换生产组合或删除旧实现。

## Acceptance Criteria

- [x] `GORMRepository` 的读写路径全部经过 GORM，领域层和接口调用方行为不变；生产 Composition 仍由 Final 收口任务单独切换。
- [x] Session 轮换/撤销并发场景、数据库时间和敏感字段处理已在真实 PostgreSQL 上与 legacy 路径成对通过。
- [x] 事务失败回滚、错误映射、取消传播和日志脱敏通过既有静态与真实 PostgreSQL 回归校验。
- [x] git diff --check、受影响包编译及既有相关测试通过；TODO 9 门禁状态记录在任务证据。

- [x] TODO 9 真实 PostgreSQL 门禁已通过；本 child 不切换 Composition、不删除 legacy，TODO 3 仅阻断 Final。

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.

## Dependencies

- 依赖 gorm-platform-transaction-foundation。
