# GORM 最终接线与 pgx 收口设计

## 目标及执行前置

本任务落实 TODO 10 的最终代码交付：28 个业务模块和 Foundation 验收后，统一 API、Worker、CLI/Probe 与测试构造，清理旧业务 pgx 实现并建立可执行边界门禁。TODO 9 Testcontainers 与 TODO 3 Atlas 已交付。用户 2026-09-08 授权完成所有未完成子任务，并随后授权提交、推送和状态同步；最终代码已推送，目标环境部署未执行。

## 连接和事务

保留 `internal/platform/postgres` 的唯一 pgxpool；GORM 和 `database/sql` wrapper 由该 Pool 创建，UnitOfWork 提供 `foundation.TransactionScope`。业务 Repository 统一使用 GORM 构造；River Worker/listener 使用官方 pgx driver，事务 enqueue 使用官方 database/sql driver，不能另建连接池或异步补偿。退出先停止 Worker/后台消费者，再由 Pool 关闭 wrapper 和物理池。

## 接线和兼容

- 以各 child `final-handoff.md`、实际 exported 构造和 `cmd/**` 调用为事实源，建立入口/模块/事务参与者映射。先完成模块门禁，再执行接线变更。
- Events/Audit、Workspace、ModelSettings、Workflow 与领域消费者全部接入 scoped Port；移除生产路径对旧 `transaction any`、pgx Tx/Pool 和运行时双 Repository selector 的依赖。
- Application/Domain 不引入 GORM；GORM model 显式表列、时间和 nullable 映射保持已验收结构。复杂查询继续通过 GORM Raw/Exec，Atlas 仍为唯一 Schema owner。
- 逐模块删除旧 Repository、旧 helper 和失效 fixture；保留被 GORM 复用的纯 codec/领域校验。已有测试统一指向最终构造，不用屏蔽编译或跳过测试隐藏回归。
- pgx 只保留在 Platform 物理连接/类型注册、River Worker/listener、Atlas/migration、连接级锁和经过证明的 credential bootstrap 等底层能力。普通 GORM 错误分类通过标准错误链/SQLSTATE 接口完成，不要求业务 import pgconn。

## 门禁与回滚

新增或扩展仓库既有静态门禁，校验 owner 覆盖、pgx/GORM 层级、AutoMigrate/Migrator 禁止项和入口残留。针对全部受影响入口运行局部构建与现有组合测试；分组运行关联实库事务/River/锁/恢复用例，每条测试显式 60 秒超时，超出范围按用户全局执行约定处理。对 credential bootstrap 进行独立 SQL/security review。

不修改 Schema、发布迁移或业务 API。回滚由本轮 Adapter/Composition 代码变更整体或按依赖闭包恢复，保持相同 Atlas 版本和数据；发布时需同一版本 API/Worker/CLI，并先停止旧 Worker 再启动新组合。本轮保存可审查 diff 和验证记录，不实际发布。
