# Health Repository GORM 迁移设计

## 边界

- 只新增 Health PostgreSQL Adapter 的 staged GORM 实现与 Collection membership 的 scoped 适配；不修改 `cmd/**`、Schema、迁移文件或删除 legacy pgx 实现。
- Application/Domain 继续只依赖现有 Health ports。GORM、`database/sql`、River 和 PostgreSQL SQL 只出现在 Adapter/Infrastructure。
- 生产 Composition 仍由 Final child 统一切换；TODO 9 未提供真实 PostgreSQL 门禁前，本 child 只能保留未接入实现并保持 `in_progress`。

## 事务与协作

- GORM Repository 从同一 `platform/postgres.Pool` 获取共享 GORM root 和 `foundation.UnitOfWork`；所有多语句不变量在该 UoW 内完成。
- Scan start 使用 Repeatable Read，并通过 Workflow `ScopedRuntimeStarter`、Events `ScopedAppender` 和 River `ScopedJobInserter` 参与同一 `foundation.TransactionScope`。不再把 `pgx.Tx` 传入 Health 新路径。
- Scan completion、Workflow cancel、affected-change dispatch 和 Schedule dispatch 保持既有锁顺序、CAS、commit-response-loss recovery 与 context cause；只在 Adapter 内通过 GORM `Raw/Exec/Clauses` 使用参数化 PostgreSQL SQL。
- Collection membership 首选 `ScopedDurableScanBindingVerifier`；成员分页继续调用 Collection application durable-scan port。Health 不重新编译 Collection 查询，也不把 Collection PostgreSQL 类型带入新的 scoped 路径。
- `FOR UPDATE SKIP LOCKED`、数据库时间、显式 workspace 条件和唯一/幂等 receipt 约束原样保留。River worker/listener 仍由 Workflow owner 管理，Health 只消费 scoped enqueue port。

## 错误与生命周期

- 所有入口先校验 nil context、ID、分页和依赖；底层 `sql.ErrNoRows`/`gorm.ErrRecordNotFound` 映射为现有 Health error code，取消、deadline、`sql.ErrTxDone` 保留 `errors.Is` 链和 caller cause。
- UoW 负责 commit/rollback，scoped 方法不提交、不回滚、不回退 root DB；rows/rows.Close 由 Adapter 负责。
- 不启用 AutoMigrate、Migrator、隐式软删除、关联保存或默认时间戳；数据库时间继续使用 `clock_timestamp()`/现有 SQL。

## 回滚与交付

- 回滚边界是本 child 新增的 Health GORM Adapter 文件与 task 记录；不回滚 Schema，也不删除 legacy。
- 生产切换、`cmd/**` 构造、legacy 删除和 TODO 9/TODO 3 完成声明全部留给 Final/外部任务。
