# GORM 平台与事务基础

## Goal

统一 GORM 版本、配置、连接、事务 Port、River 互操作、错误翻译、日志脱敏和 pgx allowlist。

## Requirements

- 建立统一 GORM 版本、postgres driver、共享连接配置和事务 Port；覆盖 internal/platform/postgres 与 gitoperation 的受控底层边界，通过 stdlib 复用现有 pgxpool，不创建第二个物理连接池。
- 提供不泄漏 GORM、database/sql、pgx 或 `any` 的新 opaque transaction scope，并为平台边界保留受控 unwrap；接入官方 riverdatabasesql 插入器，保留现有 River pgx Worker/listener。旧事务接口由各模块 child 迁移，本任务不跨模块原地替换。
- 统一错误翻译、日志脱敏、超时/取消和 pgx allowlist 规则；产出 River/事务调用点矩阵，覆盖 JobInserter、EnqueueFence、CheckEnqueue、Workflow runtime、Export dispatcher、Retrieval inserter/dispatcher，并记录 Spike 的原子性、连接池和取消行为结果。
- 本任务只交付基础设施与兼容性验证，不迁移业务模块、不切换生产 Composition；真实 PostgreSQL 使用统一 `testdb` Testcontainers/Atlas 工厂验收。

## Acceptance Criteria

- [x] 受影响包与命令入口在当前工作树编译通过，单一物理池、事务提交/回滚、并发压力和幂等关闭生命周期有真实数据库证据。
- [x] 新 GORM 事务路径以 opaque scope 调用 River database/sql inserter；围栏与业务探针同事务回滚，现有 pgx Worker 可消费并完成 Job。
- [x] 新增公共 Port/Scope 不包含 GORM、database/sql、pgx 或 `any`；legacy 调用点均有 owner，错误、取消 cause、SQLSTATE 与日志边界通过检查。
- [x] `git diff --check`、race test、vet、vendor 编译、依赖一致性及拆分后的真实 PostgreSQL/River 测试通过。
- [x] 生产 Composition、模块 legacy 删除和 TODO3/Final 收口未提前执行；目标环境容量与真实网络断链验证作为发布残余风险记录。

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.

## Dependencies

- 无；所有模块迁移依赖本任务已稳定的连接配置、事务 Port 和 River 互操作结论。
