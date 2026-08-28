# Model Settings Repository 迁移到 GORM

## Goal

新增未接生产 Composition 的 Model Settings GORM Repository，保持双进程激活、固定锁序、数据库时间、跨 owner 原子事实和 raw guard；TODO 9 后由 Final 统一切线。

## Requirements

- 在 `internal/modelsettings/adapter/postgres` 新增 staged `GORMRepository`，并在 `internal/modelsettings/runtime` 新增独立 GORM Bootstrap；保留 legacy Repository/Bootstrap 和全部 `cmd/**` 接线。
- GORM Bootstrap 只接收同一个 `*platformpostgres.Pool`，从该 Pool 构造 Model Settings、Audit、Local Runtime 的 GORM/scoped 实现；不得暴露 pgx、另开连接池或把 legacy Audit DB 混入 GORM 路径。
- Revision+Audit、Activation+Local Runtime、Workflow enqueue fence 必须通过 `foundation.TransactionScope` 在同一事务内执行；禁止第二事务、提交后补偿、`any` 或 no-op fence。
- 保持双进程激活、固定锁序、数据库时间、版本/CAS、raw guard 和凭据保护语义。
- 保留锁与事务边界、错误分类、取消传播、敏感日志和 schema 约束；不得弱化 raw guard。
- 当前 staged 实施不修改 migration、测试或 `cmd/**`；使用现有测试与局部编译。TODO 9 执行时只允许原位参数化既有 integration fixture，不新增测试文件；在此之前不得切换生产组合、删除 legacy pgx 或标记任务完成。

## Acceptance Criteria

- [ ] Model Settings Repository 读写迁移到 GORM，激活/版本和 guard 行为不变。
- [ ] 双进程竞争、固定锁序、数据库时间和回滚路径可验证。
- [ ] Revision+Audit、Activation+Local Runtime、Workflow enqueue fence 的同事务提交/回滚可验证。
- [ ] 凭据不出现在日志或公共契约，错误/取消检查通过。
- [ ] git diff --check、受影响包编译及现有相关测试通过。

- [ ] TODO 9 不可用时仅保留行为基线或未接入 Composition 的实现，不得勾选完成或归档；TODO 3 仅阻断 Final。
 
## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.

## Dependencies

- 依赖 gorm-platform-transaction-foundation、gorm-localmodelruntime-migration、gorm-audit-migration；Workflow scoped River inserter 已由 Foundation 提供并要求本模块实现 scoped enqueue fence。
