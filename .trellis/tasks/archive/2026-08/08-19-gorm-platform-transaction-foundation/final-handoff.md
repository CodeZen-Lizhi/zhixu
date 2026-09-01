# Foundation Final Handoff

## 已交付

- `platformpostgres.Pool` 以 pgxpool 为唯一物理池，通过官方 stdlib facade 提供共享 GORM root、`foundation.UnitOfWork` 和 River database/sql producer。
- 公共 transaction scope 不暴露 GORM、database/sql、pgx 或 `any`；平台受控 unwrap 对未知、过期或跨平台 scope fail closed。
- GORM producer 只负责事务内 River insert，现有 `riverpgxv5` Worker/listener/migrator 保持唯一运行路径。
- Foundation 真实数据库测试已接入统一 `testdb` Testcontainers/Atlas 工厂，不再手工创建、迁移或清理数据库。

## Final Composition

- Final 必须继续只构造一个 `platformpostgres.Pool`，模块 GORM Repository、UoW、River SQL producer 和 pgx Worker 都从该实例派生。
- 关闭顺序保持 SQL facade/GORM wrapper 先释放、pgxpool 最后关闭；不要为 GORM 或 River producer 按 DSN 新建第二物理池。
- `riverdatabasesql` 不支持 listener，不得用于 Worker、listener、leader election 或 migration。
- 各模块完成后启用全仓 allowlist：业务 Application/Domain 不得出现具体 GORM、database/sql、pgx transaction 或公共 `any` transaction。

## 删除与回滚

- Foundation 自身没有 Schema、migration 或数据回滚；Final 只删除各 owner 的 legacy adapter/构造路径，不删除 pgx Worker/listener/migrator 和获批底层 allowlist。
- 若 Final 切换失败，恢复模块 legacy Composition 即可；共享 Pool 与 UoW 可保留，因为 staged 基础已被多个已归档模块使用。

## 发布门禁

- 当前完整工作树命令入口可编译；纯 `HEAD c7558f0a` 仍缺尚未提交的 Collection scoped verifier，提交/推送前必须由 Collection owner 排好依赖并在纯 Git 快照复跑 API/Worker 编译。
- 目标环境继续验证连接预算、持续压力和真实网络层 COMMIT 断链；确定性 post-commit 错误注入只证明幂等重放，不等同网络代理故障演练。
