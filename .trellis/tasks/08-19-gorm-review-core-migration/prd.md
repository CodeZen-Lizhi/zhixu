# Review Core Repository 迁移到 GORM

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

- [ ] Review Core Repository 读写迁移到 GORM，Session/Card/Schedule 对外行为不变。
- [ ] 并发提交、失效和重复请求场景与迁移前一致，事务失败可回滚。
- [ ] Review Core 公共契约不泄漏底层类型，错误和日志检查通过。
- [ ] git diff --check、受影响包编译及现有相关测试通过。
- [ ] TODO 9 不可用时仅保留行为基线或未接入 Composition 的实现，不得勾选完成或归档；TODO 3 仅阻断 Final。

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.

## Dependencies

- 依赖 gorm-platform-transaction-foundation；与 Review Interview、Review Learning Path 分开验收。
