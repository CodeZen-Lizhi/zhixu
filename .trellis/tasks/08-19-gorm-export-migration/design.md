# Export GORM 迁移设计

## 边界

- 新增 `internal/export/adapter/postgres/GORMRepository`，实现现有 `export/application.Repository`。
- 生产 Composition、`cmd/**`、River dispatcher 和 legacy `Repository` 保持不变；TODO 9 前仅 staged。
- GORM 只出现在 PostgreSQL Adapter；Application/Domain 继续使用原有端口。

## 事务与数据流

- 通过共享 `platform/postgres.Pool.GORM()` 和 `Pool.UnitOfWork()` 获取 root/UoW。
- 每个原子操作在 `UnitOfWork.Within` 内取得 `platformpostgres.GORMTransaction(scope)`，使用参数化 Raw/Exec SQL。
- 事务 SQL 保持 legacy 的 DB-time、锁顺序、CAS、幂等和稳定 keyset 分页；`SELECT clock_timestamp()` 与 `FOR UPDATE SKIP LOCKED` 不改语义。
- 生命周期 Event 使用 `eventsapplication.ScopedAppender.AppendScoped`；下载统计使用 `auditapplication.ScopedAppender.AppendScoped`，均不提交/回滚 caller scope。
- Event/Audit appender 缺失时按 legacy 依赖不可用语义失败；禁止跨连接或异步补偿。

## 映射与错误

- Persistence Model 不暴露给 Domain；沿用显式列清单和现有 `scanJob`/JSON 编解码，避免隐式 GORM Model、AutoMigrate 或软删除。
- GORM `Raw` 返回 `database/sql` 行/行集，扫描同时接受 `sql.ErrNoRows`；错误分类保留 Export 错误码，并补充 context cause、`sql.ErrTxDone` 和 PostgreSQL SQLSTATE 映射。
- 每个 staged 方法在 callback 内完成所有状态与 side-fact 写入，UoW 负责 commit/rollback；成功 callback 后提交失败仍返回 retryable。

## 回滚与门禁

- 回滚边界仅为新增 GORM Adapter 文件及 child 任务记录；不回滚 Schema、不修改历史迁移、不删除 legacy。
- TODO 9 缺少真实 PostgreSQL DSN 时只能运行现有单元/编译/静态检查，不能标记 child 完成或切换生产。
