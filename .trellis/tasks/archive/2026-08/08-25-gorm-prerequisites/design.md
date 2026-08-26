# TODO 9：Testcontainers 工厂设计

## 1. 责任边界

TODO 9 只拥有 PostgreSQL 测试环境的生命周期。它不拥有任何 Repository 的业务
断言，也不迁移现有测试文件。TODO 10 child 在需要真实 PostgreSQL 时自行调用工厂，
并仍对自身的 legacy/GORM 等价、锁、事务、River、约束和查询计划负责。

```text
TODO 10 child test
       |
       v
testdb.Require / testdb.Open
       |
       +-- container mode: Testcontainers PostgreSQL/pgvector
       |       -> migration-only Pool -> Goose migration -> shared platform Pool
       |
       +-- external-admin mode: unique temporary database
               -> migration-only Pool -> Goose migration -> shared platform Pool
       |
       v
same platformpostgres.Pool
       +-- pgx DB()
       +-- GORM()
       +-- UnitOfWork()
       +-- River database/sql or pgx integration
```

`platformpostgres.Pool` 是唯一物理连接池事实源。工厂不得额外构造 GORM、pgx
或 River 的独立池；也不得调用 GORM Schema mutation API。

## 2. 对外契约

保留可组合的低层 `Open(ctx, Config) (*Fixture, error)`，供故障注入和非
`testing.TB` 场景使用；新增面向测试的 `Require(t, Config) *Fixture`：

- 调用 `t.Helper()`；
- 容器模式按显式 availability policy 检查 provider；
- 成功后注册 `t.Cleanup`，不能由业务测试重复写关闭/终止逻辑；
- 默认 Docker 不可用即 fail，只有显式 `SkipWhenUnavailable` 才可 skip；
- 返回的 Fixture 只暴露 `Pool()` 和脱敏诊断，不暴露完整 DSN。

`Config` 将由当前含义模糊的 `ExternalURL` 改为 `ExternalAdminURL`。这是一项
刻意的破坏性命名调整：它表示可创建和删除临时数据库的 admin 连接，而不是可被
直接迁移的目标数据库。当前没有该原型的调用方，因此不保留会误用共享数据库的兼容
别名。

## 3. Provisioning 与清理

### 容器模式

1. 通过 `postgrescontainer.Run` 启动 `pgvector/pgvector:pg16`，不指定固定容器名、
   宿主机端口或 reuse。
2. 使用 PostgreSQL SQL wait strategy（`SELECT 1`）和有界启动 timeout，而非只依赖
   `pg_isready` 或日志。
3. 获取内部使用的连接 URL，打开 migration-only Pool，执行当前
   `migration.Runner` 的完整 Goose/River 迁移，再关闭 migration Pool。
4. 用同一 URL 只打开一次完整 `platformpostgres.Pool`；Ping 成功后返回 Fixture。
5. Close 先关闭 platform Pool，再使用独立、有限时间的 cleanup context 终止容器。

### 外部 admin 模式

1. 校验并脱敏 `ExternalAdminURL`，打开 admin Pool。
2. 以 cryptographically-random suffix 生成 `zhixu_test_` 前缀的数据库名，使用
   `pgx.Identifier` 作为唯一 SQL identifier 边界，执行 `CREATE DATABASE`。
3. 将 URL 指向新数据库，执行与容器模式完全相同的 migration/open 流程。
4. Close 或中途失败时，先关闭所有新库连接，再由 admin Pool 执行
   `DROP DATABASE <generated> WITH (FORCE)`；admin URL 指向的数据库绝不被迁移或删除。

失败路径统一使用同一 cleanup 函数，保留原始错误为主错误；cleanup 错误只作为附加
诊断。完整 URL、密码和绝对路径不进入错误、日志或测试输出。

## 4. 并行、可观测性与本地限制

- 每个容器 fixture 独占容器和 Docker 随机映射端口；每个 external fixture 独占随机
  数据库名，因此可安全 `t.Parallel`。
- 禁止容器复用。测试速度优化由 future CI cache/镜像预拉取解决，不能共享数据面。
- 诊断只包含 mode、镜像、redacted host/database、container ID（若有）和 cleanup
  状态；不暴露密码或 DSN。
- 当前 Docker Desktop 的 Ryuk provider 有启动超时证据。本地可显式设置
  `TESTCONTAINERS_RYUK_DISABLED=true` 后运行 smoke；仓库、Makefile 和 CI 默认不关闭
  Ryuk。CI 应修复 provider/reaper 可用性并把失败作为可操作错误暴露。

## 5. 自身验证与后续接入

TODO 9 的真实容器测试仅证明工厂能力：空库迁移、pgvector/River/schema 可用、共享
Pool 可取得、并行隔离、外部 admin 隔离、migration failure cleanup、Close 幂等和
skip/fail 策略。它不写任何业务 Repository 的回归。

TODO 10 child 后续仅在改动自己的集成 fixture 时接入 `testdb.Require`，并维护自己的
业务断言和测试生命周期。当前 86 个 `ZHIXU_TEST_DATABASE_URL` 调用点不在 TODO 9
范围内；该变量在 child 接入时可映射为 `ExternalAdminURL`，而非目标数据库 URL。

TODO 3 完成后只替换 `MigrationFunc` 的默认实现为 Atlas，不改变 Fixture API、并行
隔离或 cleanup 契约。
