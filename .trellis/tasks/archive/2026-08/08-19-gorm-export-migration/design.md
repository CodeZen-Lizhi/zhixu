# Export GORM 迁移设计

## 边界

- 新增 `internal/export/adapter/postgres.GORMRepository` 和 `internal/export/adapter/river.GORMDispatcher`，分别实现现有 `export/application.Repository` 与 `Dispatcher`。
- 生产 Composition、`cmd/**` 和 legacy 实现保持不变；本 child 只交付可验证的 staged 路径，Final child 负责统一切换和删除 legacy。
- GORM 只出现在 PostgreSQL/Workflow Adapter；Application/Domain 继续使用原有端口。

## Repository 结构

- 保留一份显式 Export SQL 核心，通过私有 `exportDatabase`、`exportTransaction`、`exportRows` 和 `exportRow` 接口隔离驱动差异。
- legacy `DB`/pgx transaction 只存在于 `legacy_adapter.go`；GORM 路径由 `gorm_adapter.go` 将 `Pool.GORM()` 与 `Pool.UnitOfWork()` 适配到私有 SQL 核心。
- `NewGORMRepository` 只接收 `*platformpostgres.Pool` 和强类型 `GORMOption`。私有 side-fact bridge 可携带 opaque transaction，但不向公共契约泄漏具体事务类型或 `any`。
- SQL 保持 legacy 的 DB-time、锁顺序、CAS、幂等和稳定 keyset 分页；`clock_timestamp()`、`FOR UPDATE`、`SKIP LOCKED` 及显式列清单不改变语义。

## Side Fact 与错误

- 生命周期 Event 使用 `eventsapplication.ScopedAppender.AppendScoped`；下载 Audit 使用 `auditapplication.ScopedAppender.AppendScoped`，两者都复用 caller scope 且不自行提交或回滚。
- Event 延续 legacy 的可选语义；未配置时 Job 写入仍成功且不生成 Event。`RecordDownload` 需要 Audit 时，缺少 appender 按 dependency unavailable 失败并回滚计数。
- 扫描同时接受 `sql.ErrNoRows` 与 legacy pgx 映射；错误分类保留 Export code，并覆盖 context cause、`sql.ErrTxDone` 和 PostgreSQL SQLSTATE。
- callback 成功但 commit 返回错误时标记 retryable；Create 通过幂等键精确重放，Download 通过 Audit ID 防止重复计数和重复 Audit。
- 所有 rows 在方法内关闭并检查迭代错误；坏数据返回完整错误，不返回部分结果。

## River Dispatcher

- `GORMDispatcher` 通过 `foundation.UnitOfWork` 调用 Workflow 的 scoped typed inserter；围栏检查和 River insert 使用同一个 `database/sql` transaction scope。
- 唯一状态策略与 legacy 一致：active duplicate 不新增 job，completed 后允许恢复 job；现有 pgx Worker/listener 无需迁移即可消费。
- begin 与 commit 失败分别映射为 retryable Export River 错误；围栏拒绝或 insert 失败会回滚同一事务内的全部写入。
- Repository Create 与 Dispatcher 之间仍采用既有 PENDING/恢复流程，不宣称跨两个端口的数据库原子性。

## 映射与错误

- Persistence Model 不暴露给 Domain；沿用显式列清单和现有 `scanJob`/JSON 编解码，不使用隐式 GORM Model、`AutoMigrate`、Migrator 或软删除。
- PostgreSQL 数组参数在 GORM/database/sql 边界显式转换；SQL 始终参数化，不拼接外部输入。

## 回滚与门禁

- 回滚边界是新增 GORM Adapter、共享私有适配层及 child 测试/记录；没有 Schema、历史迁移、依赖或生产 Composition 变更。
- 使用项目 `testdb` Testcontainers fixture 在真实 PostgreSQL 上比较 legacy/GORM；定向覆盖生命周期、并发、DB-time CAS、附件能力、清理、游标、取消、坏数据、真实查询计划、post-commit replay 和 pgx Worker 消费。
- EXPLAIN 复用生产 SQL 常量并关闭 seqscan，只证明目标索引可被规划器选择；生产规模下的容量与时延验证不属于本 child。
