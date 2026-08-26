# TODO 9：Testcontainers-Go 数据库集成测试工厂

## Goal

在不阻断当前 GORM staged 迁移的前提下，交付一个统一管理 PostgreSQL/pgvector
集成测试生命周期的 Testcontainers-Go 工厂。完成后，TODO 10 的每个模块自行用该
工厂完成自身的 legacy/GORM 业务回归；TODO 9 不代替任何业务模块验收，也不做生产切换。

## Background And Confirmed Facts

- 当前生产迁移入口仍是 `internal/platform/migration.Runner`，使用 Goose，并同时处理项目迁移和 River Schema；仓库 `migrations/` 当前有 92 个 SQL 文件。
- 当前约 86 个 Go 测试文件直接读取 `ZHIXU_TEST_DATABASE_URL`，约 199 个 integration test 文件存在，fixture 生命周期和迁移调用点分散。
- TODO 10 已建立共享 GORM/事务基础，但其 PRD 规定 TODO 9 完成前只能保留 staged 实现，不能切生产、删除 legacy 或归档模块。
- 当前 `internal/platform/testdb` 已有工厂原型：容器启动、Goose 迁移、共享 `platformpostgres.Pool`、外部 URL 和 idempotent close 均已实现；真实 smoke 已验证空库迁移与 River 表。
- 用户明确：TODO 9 是测试工厂功能，不应因 TODO 10 尚未做完各自的真实 PostgreSQL 回归而保持未关闭。模块测试归各 TODO 10 child 所有。

## Requirements

### R1. 单一 Testcontainers 工厂

- 采用单一工厂创建生产兼容的 PostgreSQL/pgvector 容器，负责镜像、SQL 健康等待、正式迁移、日志摘要和清理。
- 每个 fixture 提供一个已迁移、同源的 `platformpostgres.Pool`；GORM、pgx、River 与 Unit of Work 都从它取得能力。业务测试不得自行复制容器启动、数据库命名或清理逻辑。
- 容器模式不设置固定容器名或宿主机端口，不启用容器复用；并行 fixture 必须相互独立。
- 外部 DSN 模式只接受具备 `CREATE/DROP DATABASE` 权限的 admin URL，由工厂生成唯一临时数据库、执行同一迁移、关闭连接后 `DROP DATABASE ... WITH (FORCE)`；不得直接迁移调用方指定的共享数据库。
- Docker 不可用时，调用方显式选择 skip 或取得可操作错误；不允许静默 fallback 到共享数据库，日志和错误不得输出密码或完整 DSN。

### R2. TODO 10 接入契约

- 提供面向 `testing.TB` 的 helper，统一 provider 可用性处理和 `t.Cleanup`；同时保留返回错误的低层 `Open`，使故障注入测试能断言错误语义。
- helper 的默认策略是 Docker 不可用即明确失败；需要跳过的测试必须在调用处显式选择 skip 策略。
- TODO 10 child 只负责把其现有集成测试迁移到工厂并验证业务行为；TODO 9 只提供迁移说明和最小使用样例，不批量修改现有 86 个环境变量 fixture。

## Out Of Scope

- TODO 3 的 Atlas baseline、迁移入口切换、drift apply/inspect 和 Goose 删除。
- 迁移任何业务 Repository、在本任务内切换 API/Worker 生产 Composition，或修改领域契约、数据库业务语义、已发布迁移内容。
- 一次性重写全部 86 个 `ZHIXU_TEST_DATABASE_URL` fixture；后续模块按统一 fixture 契约逐步接入。
- TODO 10 的 Repository、GORM commit/rollback、River 原子入队、pgvector 查询和业务约束回归。

## Acceptance Criteria

- [x] AC1：从无数据库状态启动 Testcontainers PostgreSQL/pgvector，SQL 健康检查通过，使用当前正式 Goose 迁移入口完成 Schema 初始化；`vector` 扩展、River 表和项目 migration history 可查询。
- [x] AC2：容器 fixture 返回唯一 `platformpostgres.Pool`，可取得 pgx DB、GORM root 和 Unit of Work；本任务只验证该能力可用，不在此验收任何 Repository 业务行为。
- [x] AC3：两个并行 fixture 的容器/连接 URL/数据互不污染，关闭一次或多次均不泄漏资源；迁移失败后容器也会被终止。
- [x] AC4：外部 admin DSN 模式创建唯一临时数据库、执行与容器模式相同的迁移、关闭后强制删除该临时数据库；它绝不迁移或删除 admin URL 指向的数据库。
- [x] AC5：Docker 不可用的 skip/fail 策略可由测试调用方明确选择；错误和诊断均不泄露密码、完整 DSN、绝对路径或高敏参数。
- [x] AC6：提供稳定的调用示例和 Makefile 入口；TODO 10 child 能逐个自行迁移自身测试，但 TODO 9 不依赖任何 child 的业务回归才可关闭。

## Notes

- TODO 3 是独立前置：其完成后只替换本工厂的 migration callback，不改变 fixture 生命周期或 TODO 9 的关闭状态。
- 当前 Docker Desktop 的 Ryuk provider 超时需单独修复或在本地显式设置 `TESTCONTAINERS_RYUK_DISABLED=true`；不得将禁用 Ryuk 设为项目默认。
