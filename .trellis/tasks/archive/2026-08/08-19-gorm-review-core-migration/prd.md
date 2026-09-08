# Review Core Repository 迁移到 GORM

> **当前交付状态（2026-09-08）**：`completed`，已归档；最终代码 [cb935655](https://github.com/CodeZen-Lizhi/zhixu/commit/cb93565561498674cda1dc1230fed587fba66075) 已推送至 `origin/dev`，尚未部署。
> TODO 10 全部 30 个子任务已完成，最新实现与验证见 [Final 验收记录](../../2026-09/08-19-gorm-composition-pgx-convergence/research/final-acceptance.md)。下文 staged、待 Final 或未提交的描述保留其原阶段事实，现存 M9 历史升级限制仍有效。

## Goal

迁移 Review Core PostgreSQL Repository，保持 Session、Card/Schedule、失效与并发提交语义。

## Requirements

- 仅迁移 `internal/review/adapter/postgres`，保持 Review Core 与 Interview、Learning Path 的边界。
- 新增未接生产的 `GORMRepository`，从同一个 `platformpostgres.Pool` 取得 GORM root 与 Unit of Work；保留 legacy `Repository`、`NewRepository` 和 `cmd/**` wiring 到 Final。
- 保持 Session、Card/Schedule、失效、并发提交、数据库时间和唯一约束语义。
- 保留现有错误分类、幂等/乐观并发条件、事务回滚和上下文取消；不把 Interview/Learning Path 逻辑并入本任务。
- 所有写命令继续先锁 `core.workspace`；Answer、Score、Schedule 与 receipt 同一事务，复杂 due/evidence/invalidation SQL 保留参数化 Raw SQL。
- 不修改 Schema、migration、Application/Domain Port、`cmd/**` 或生产 Composition，不使用 `AutoMigrate`、association、hook、隐式时间或第二连接池。
- 当前 staged 阶段不修改测试；TODO 9 可用后只原位参数化既有 integration fixture，使用独立数据库成对验证 legacy/GORM，不新增测试文件。

## Acceptance Criteria

- [x] Review Core Repository 读写迁移到 GORM，Session/Card/Schedule 对外行为不变。
- [x] 并发提交、失效和重复请求场景与迁移前一致，事务失败可回滚。
- [x] Review Core 公共契约不泄漏底层类型，错误和日志检查通过。
- [x] git diff --check、受影响包编译及现有相关测试通过。
- [x] TODO 9 已通过独立 Testcontainers 数据库的 legacy/GORM 成对门禁；实现仍未接入 Composition，TODO 3 仅阻断 Final。

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.

## Dependencies

- 依赖 gorm-platform-transaction-foundation；与 Review Interview、Review Learning Path 分开验收。
