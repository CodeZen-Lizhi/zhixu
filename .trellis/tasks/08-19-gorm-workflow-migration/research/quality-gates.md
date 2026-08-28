# Workflow GORM 实施质量门禁

本摘要从 backend quality guidelines 提取本任务必须完整注入的规则；主 agent 规划时已完整读取原 spec。

- 公开 Application/Domain API 不含 pgx、GORM、database/sql 或 `any`；legacy allowlist 不扩张。
- 一个业务动作只有一个事务 owner；caller-owned scoped 方法不 begin/commit/rollback，不 fallback root。
- 事务内锁、状态写、Outbox、River job 和 Hook 使用同一 live scope；固定锁序、DB time、CAS 与 exact replay 不重排。
- root/scoped 入口校验 nil context、typed-nil dependency、非法/失效 scope；context 保留 sentinel 与 custom cause。
- Raw SQL 全参数化；JSONB carrier、nullable scan、Rows Close/Err、no-row 与 SQLSTATE 显式处理。
- 未知错误不泄漏 SQL、参数、JSON、owner、内部路径或凭据；日志使用项目 logger 且不输出 bind values。
- 不使用 AutoMigrate、Migrator、Preload、Association、Save，不引入 N+1、Offset 或无界列表。
- 代码修改后执行局部 unit/race/vet、integration compile、consumer compile、vendor/module、task validate、gofmt/diff check。
- 真实 PostgreSQL 不可用时明确盲区，不能勾 PRD AC、切生产、删 legacy、完成或归档。
