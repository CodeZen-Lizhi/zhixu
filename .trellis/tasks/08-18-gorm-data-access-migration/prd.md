# TODO 10：应用数据访问层迁移到 GORM

## Goal

在不改变现有 Schema、领域契约、事务原子性、并发控制和错误语义的前提下，将应用 PostgreSQL Repository 按模块迁移到统一的 GORM 数据访问边界。父任务负责共享契约、模块任务图和最终集成验收；每个可独立验证的模块由一个子任务迁移、验收和回滚。

## Background

- 路线图将 TODO 10 定义为 P0、高风险、80–120 人天；Testcontainers 是模块迁移验收前置，Atlas 是最终切换前置，见 `docs/roadmap.md:108-124`。
- 当前 `go.mod` 使用 `github.com/jackc/pgx/v5 v5.10.0`，尚未引入 GORM。
- 静态盘点发现 29 个 PostgreSQL 持久化 owner：27 个标准 `adapter/postgres` 目录、1 个 Approval Dispatch PostgreSQL Adapter，以及 1 个没有独立 Adapter 目录的 Local Model Runtime owner。27 个顶层目录的生产 Go 代码直接依赖 pgx，其中 26 个属于业务或运行时模块，另一个是 `internal/platform`。
- `review` 下的 Core、Interview、Learning Path 各有独立 PostgreSQL Repository；`changecontrol` 下的 Approval Dispatch 与主体 Repository 共享同一领域事务边界。
- API、Worker、Workspace CLI/Probe 当前共享 `internal/platform/postgres.Pool` 暴露的 `*pgxpool.Pool`。多个模块通过 opaque `transaction any` 传递实际 `pgx.Tx`，用于跨 Repository、Event/Audit、Workflow/River 原子写入。
- GORM v1.31.2 与 PostgreSQL Driver v1.6.2 的官方接口基于 `database/sql` 风格 `gorm.ConnPool`。River v0.40.0 另有官方 `riverdatabasesql` Driver，明确用于 GORM/Bun 并接收 `*sql.Tx`；统一基础任务必须验证它与现有 `riverpgxv5` Worker、同一 pgxpool 和现有事务语义的兼容性，禁止模块自行实现不同桥接。

详细盘点和拟议子任务图见 `research/repository-inventory.md`。

## Requirements

### R1. 父子任务结构

- TODO 10 父任务只拥有源需求、共享技术契约、子任务映射、依赖顺序、跨模块验收和最终收口，不直接承载某个领域模块的迁移实现。
- 建立一个先行的 GORM 平台与事务基础子任务，统一依赖版本、连接/连接池、Logger、NamingStrategy、PrepareStmt、SkipDefaultTransaction、NowFunc、错误翻译、事务与 Savepoint、既有测试 Fixture 接入规范和 pgx allowlist；不默认新增测试文件。
- 每个具有独立 Repository/Store 边界的领域模块建立一个子任务；子任务必须能够独立实施、验证、回滚和归档。
- 建立最终 Composition/pgx 收口子任务，统一切换 API、Worker 和命令入口，并验证 allowlist 之外不再存在 pgx 生产依赖。
- 父子关系不替代依赖关系；每个子任务 PRD/Design 必须显式记录其基础任务、共享模块和外部 TODO 依赖。

### R2. GORM 与模型边界

- GORM 只进入 Adapter/Infrastructure 和 Composition Root；Domain/Application/Public API 不暴露 `*gorm.DB`、GORM Model、Clause 或 GORM 错误类型。
- Persistence Model 与领域实体分离，显式声明逻辑 Schema、表名、列名、nullable、时间和版本映射。
- 禁止 `gorm.Model`、隐式复数表名、隐式软删除、隐式自动时间戳、关联级联写入和 Hook 副作用改变现有行为。
- 常规 CRUD、条件查询、分页和批量优先使用 GORM API；复杂 CTE、窗口、图、pgvector、锁和性能 SQL 可使用受控 Raw/Exec/Clauses，但仍由模块 Repository 管理。
- Atlas 是最终唯一 Schema 事实源；所有生产、测试和命令入口禁止 GORM `AutoMigrate` 或通过 `Migrator` 修改 Schema。

### R3. 事务与底层能力

- 项目自有 Unit of Work/transaction port 必须保持现有隔离级别、只读事务、Savepoint、跨 Repository 原子提交、Outbox、River 入队和 response-loss replay。
- 先行基础任务必须优先用官方 `riverdatabasesql` 做可执行 Spike，证明 GORM 与 River 的同事务方案，并维护 JobInserter、EnqueueFence、CheckEnqueue、Workflow runtime、Export dispatcher、Retrieval inserter/dispatcher 的调用点矩阵和迁移 owner；不得通过异步补偿、第二连接事务或吞错伪装原子提交。
- 若保持现有 River 原子入队需要调整为持久 Outbox，必须作为独立设计决策锁定等价恢复、幂等和发布边界，不能由单个模块顺带改变。
- pgx 仅允许保留在批准的底层能力：GORM PostgreSQL Driver、River/`riverpgxv5`、Atlas/迁移、连接级 advisory lock、COPY/类型注册、Local Model Runtime 管理角色 credential bootstrap，以及经证据证明无法由 GORM 完整覆盖的能力；credential bootstrap 必须单独完成 SQL/security review。
- 每个 pgx 例外必须有 owner、接口、原因和测试；普通业务 Repository 不得直接取得 `pgxpool.Pool` 或 `pgx.Tx`。

### R4. 分模块迁移与回滚

- 每个模块先冻结现有查询、写入、错误、事务、锁、分页、幂等和性能基线，再迁移实现。
- Final 为每个模块维护一个明确 Composition 切换点并替换生产实现；模块 child 不增加运行时双 Repository selector 或双写路径。
- 回滚以模块级提交/revert 和兼容数据库结构为基础；迁移不得要求回滚 Schema，也不得改写已发布迁移。
- 跨模块事务按共同契约和明确顺序迁移；发现一个模块无法独立验收时，必须回到父任务调整边界，而不是把依赖隐含进实现。

### R5. 外部依赖门禁

- TODO 9 Testcontainers-Go 工厂已交付，提供模块迁移完成所需的真实 PostgreSQL/pgvector、事务、锁、River 和约束验收环境。
- TODO 9 交付前允许开展共享基础设计、可执行互操作 Spike、行为基线和模块实现；当前各模块仍须完成自身真实 PostgreSQL 门禁，且不得切换生产 Composition 或删除原实现。
- TODO 3 Atlas 完成前，不执行 TODO 10 的最终全仓切换和旧迁移路径清理。
- TODO 9/TODO 3 是独立路线图任务，不作为 TODO 10 的模块子任务重复实现。

### R6. 模块子任务统一验收模板

- 模块 child 保持 Application/Domain 接口与外部 API 行为不变，并在 TODO 9 后以真实 PostgreSQL 证明 GORM Repository 等价；所有生产入口切换统一由 Final child 完成。
- 现有 Workspace/权限、稳定分页、幂等、乐观锁、唯一约束、错误码、数据库时间和 response-loss 语义保持不变。
- 真实 PostgreSQL 测试覆盖模块的读写、事务、并发、回放和失败矩阵；性能敏感 SQL 有 EXPLAIN 或容量对等证据。
- 无 N+1、隐式预加载、无界查询、循环远程调用或逐条写入退化；日志不泄露 Credential、正文、完整 DSN、绝对路径或高敏参数。
- 模块 child 锁定需由 Final 删除的 legacy pgx 清单；Final 完成后模块内 pgx 生产依赖清零，或只剩父任务批准的底层 allowlist并由静态检查锁定。
- 定向 Go test/race/vet、受影响集成门禁、`go mod tidy -diff` 与 `git diff --check` 通过；涉及 SQL 的变更完成 Go Review 和 SQL Review。

## Acceptance Criteria

- [ ] AC1：父任务维护完整、无重复的 PostgreSQL 模块清单；每个独立模块恰好映射到一个迁移子任务，跨模块依赖和执行波次可追踪。
- [ ] AC2：GORM 平台与事务基础子任务完成，并证明显式配置、连接生命周期、错误翻译、事务/Savepoint、pgvector 和 River 原子性方案；不存在第二连接池竞争或事务降级。
- [ ] AC3：每个模块子任务均满足 R6，并拥有独立验证证据和回滚点；父任务不以“部分模块已迁移”宣称 TODO 10 完成。
- [ ] AC4：API、Worker、Workspace CLI/Probe 和测试 Composition 使用同一批准的数据访问入口；模块迁移期间没有长期双写、双读或运行时 selector。
- [ ] AC5：Domain/Application 不导入 GORM 或 pgx；普通业务 Repository 不直接依赖 pgx，残留 pgx 与父任务 allowlist 完全一致且被静态门禁保护。
- [ ] AC6：生产、测试和命令入口均不能调用 GORM `AutoMigrate`/`Migrator` 修改 Schema；Persistence Model 与 Atlas 批准结构一致。
- [ ] AC7：全量真实 PostgreSQL、pgvector、River、事务并发、锁、Outbox、Workflow 和 response-loss replay 门禁通过，关键 SQL 无性能退化。
- [ ] AC8：TODO 9 已提供 Testcontainers 验收门禁、TODO 3 已完成 Atlas 唯一 Schema 事实源后，最终 Composition/pgx 收口子任务完成，TODO 10 才可归档。

## Key Decisions

- `Review Core`、`Review Interview`、`Review Learning Path` 各自拥有独立 Repository 和验收边界，分别建立三个模块子任务；另设 Review 集成验收，不把三者重新合并成一个实现任务。
- TODO 9 的 Testcontainers 工厂已由 `109d2cb4` 交付。它解除环境前置，但模块仍须在各自既有 integration fixture 中完成真实 PostgreSQL 门禁后才可声明完成；生产切换和旧实现删除仍只属于 Final。
- TODO 3 是最终全仓收口门禁，而不是所有模块开始编码的门禁。
- 父任务采用一个共享基础任务、每模块一个迁移任务、一个最终 Composition/pgx 收口任务的结构。

## Out Of Scope

- 在本父任务内实现 TODO 9 Testcontainers-Go 或 TODO 3 Atlas。
- 改变数据库 Schema、领域状态机、权限、幂等、错误码、API/事件契约或用户可见行为。
- 为迁移方便把 GORM Model 暴露给 Domain/Application，或把跨模块事务移到 HTTP Handler。
- 强制从依赖树删除 pgx、River 或 PostgreSQL 专属能力；目标是收敛普通业务代码的直接依赖。
- 一次性全仓替换、无行为基线的大范围机械改写，或长期维护 pgx/GORM 两套生产实现。
