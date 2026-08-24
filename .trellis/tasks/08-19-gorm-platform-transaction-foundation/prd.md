# GORM 平台与事务基础

## Goal

统一 GORM 版本、配置、连接、事务 Port、River 互操作、错误翻译、日志脱敏和 pgx allowlist。

## Requirements

- 建立统一 GORM 版本、postgres driver、共享连接配置和事务 Port；覆盖 internal/platform/postgres 与 gitoperation 的受控底层边界，通过 stdlib 复用现有 pgxpool，不创建第二个物理连接池。
- 提供不泄漏 GORM、database/sql、pgx 或 `any` 的新 opaque transaction scope，并为平台边界保留受控 unwrap；接入官方 riverdatabasesql 插入器，保留现有 River pgx Worker/listener。旧事务接口由各模块 child 迁移，本任务不跨模块原地替换。
- 统一错误翻译、日志脱敏、超时/取消和 pgx allowlist 规则；产出 River/事务调用点矩阵，覆盖 JobInserter、EnqueueFence、CheckEnqueue、Workflow runtime、Export dispatcher、Retrieval inserter/dispatcher，并记录 Spike 的原子性、连接池和取消行为结果。
- 本任务只交付基础设施与兼容性验证，不迁移业务模块、不切换生产 Composition；TODO 9 通过前不得宣称模块完成。

## Acceptance Criteria

- [ ] 所有新增依赖和连接配置可由受影响包编译，单一物理池、事务提交/回滚和关闭生命周期有可复核证据。
- [ ] 新 GORM 事务路径以 opaque scope 调用 River database/sql inserter；Worker/listener 继续使用 pgx，listener 能力差异和 TODO 9 原子性验证项被明确记录，类型边界与调用点矩阵阻止新路径误用旧 pgx inserter。
- [ ] 新增公共 Port/Scope 不包含 GORM、database/sql、pgx 或 `any`；现存旧接口列入模块迁移矩阵，错误和日志契约通过现有质量检查。
- [ ] git diff --check 及现有相关测试/局部编译通过，TODO 9 前置门禁与未覆盖风险记录在任务日志。

- [ ] TODO 9 不可用时仅保留行为基线或未接入 Composition 的实现，不得勾选完成或归档；TODO 3 仅阻断 Final。

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.

## Dependencies

- 无；所有模块迁移依赖本任务稳定的连接配置、事务 Port 和 River 互操作结论。
