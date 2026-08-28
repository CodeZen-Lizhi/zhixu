# Memory Repository 迁移到 GORM

## Goal

在不改变 Memory 领域端口、数据库 Schema 或生产 Composition 的前提下，为现有七个 Repository 操作实现可验证的 staged GORM 路径，并保持命令回放、Interview 双层幂等、CAS 生命周期、append-only 历史、有效上下文和并发过期处理语义。

## Confirmed Baseline

- `application.Repository` 由 `FindCommand` / `CreateCandidate` / `Mutate` / `Get` / `List` / `LoadEffective` / `ExpireDue` 七个方法组成，Application/Domain 没有暴露 pgx 类型。
- `learning.memory` 是 version CAS 更新的聚合行；`learning.memory_command` 与 `learning.memory_audit` 才是 append-only 历史。Schema 由 `00034` / `00045` / `00048` / `00050` / `00054` / `00059` / `00060` 等已发布 migration 共同定义。
- 一个可变命令在同一 PostgreSQL 事务中执行 advisory lock、receipt replay、聚合读取/更新、audit insert 和 receipt insert；Interview Candidate 还必须在 client-key lock 之后获取 provenance lock。
- `List` 使用 `(updated_at,id) DESC` keyset 和 `Limit+1`；`LoadEffective` 在 SQL 根查询中同时限定 ACTIVE、confirmation、数据库时间、Workspace、stable owner 与 task scope。
- `ExpireDue` 使用 `clock_timestamp()` 与 `FOR UPDATE SKIP LOCKED` 有界取数，逐聚合进行 version CAS 和 audit，但整批是一个事务。
- API、Worker、RAG/工具组合测试与 migration 回放测试仍构造 legacy pgx Repository。TODO 9 前这些调用点全部保持不变。
- 既有 PostgreSQL integration test 现通过 `internal/platform/testdb` 创建隔离容器数据库；每个顶层契约场景只创建一个 shared `platformpostgres.Pool`，其中 legacy/GORM 成对从同一 Pool 构造，并以不同 UUID/Workspace 命名空间隔离 seed。静态检查仍不能替代真实 PostgreSQL 证据。

## Requirements

### R1. Scope And Staged Boundary

- 本 child 只修改 `internal/memory/adapter/postgres` 与本任务文档；TODO 9 获得真实数据库后可原位扩展现有 integration test，但不新建测试文件。
- 在同包新增 `GORMRepository` / `NewGORMRepository(*gorm.DB, foundation.UnitOfWork)` 并完整实现现有 `application.Repository`；保留 `Repository` / `NewRepository(DB)` 作为 pgx 行为基线。根 GORM 用于只读查询，锁和写事务必须通过 Foundation Unit of Work 建立。
- TODO 9 前不得修改 `cmd/api`、`cmd/worker`、跨模块集成构造或 migration，不得删除 legacy 实现，不得完成或归档本 child。
- Production Composition 切换与 legacy 删除统一归 Final child；本 child 不引入双写、运行时 selector 或失败 fallback。

### R2. Command Transactions And Lock Order

- `FindCommand`、`CreateCandidate`、`Mutate` 在 Foundation `UnitOfWork.Within` 建立的显式 GORM/PostgreSQL 事务内执行；`ExpireDue` 也使用同一边界。这四条路径使用 `foundation.TransactionOptions{}`（默认隔离、非只读）对齐 legacy `Begin(ctx)`；事务函数返回后必须完成 commit 或 rollback，包括 request context 取消后的锁释放。
- 每个命令必须先按 `(workspace_id,idempotency_key)` 获取 `pg_advisory_xact_lock`，再以 `FOR UPDATE` 读取 exact receipt；同 key 不同请求必须在其他副作用前返回 `MEMORY_IDEMPOTENCY_CONFLICT`。
- Interview Candidate 固定锁顺序为 client-key lock -> exact replay -> provenance advisory lock -> semantic replay；不得调换、仅依赖 unique violation 或用 step-derived key 替代客户幂等 key。
- 不同 key 但同一 Interview step 与同一请求必须复用原 Candidate snapshot/Memory ID，只新增本 key receipt；并发下只能有一条 Candidate 与一条 `CANDIDATE_CREATED` audit。
- 全部 lock、receipt replay、Interview join/`FOR UPDATE OF m,c` 继续使用参数化 Raw SQL，不使用 GORM association 或跨连接事务。

### R3. Aggregate CAS, Receipt And Audit Atomicity

- Candidate 创建顺序保持为：workspace `FOR KEY SHARE` -> explicit-column Memory insert/`RETURNING` -> audit insert -> command receipt insert -> commit。
- Mutation 顺序保持为：owner-scoped Memory `FOR UPDATE` -> 完整 snapshot 及 expected version 复核 -> 数据库时间过期校验 -> `WHERE workspace/owner/id/version` CAS `UPDATE ... RETURNING` -> audit insert -> receipt insert -> commit。
- `learning.memory` 不得使用 GORM `Save`、association、默认时间或忽略 nil/零值的 struct update；source、owner、type 等不变式交给既有列白名单与数据库触发器双重保护。
- `memory_command` 与 `memory_audit` 只能 insert，不使用 upsert-update、soft delete 或 ORM hook；audit 必须在聚合行到达新 version/status 后写入，并与 `(workspace_id,memory_id,memory_version)` 唯一约束及 aggregate guard 一致。
- 任一 Memory/audit/receipt 写入、结果复核或 commit 失败时，同一事务的其余写入全部回滚；不得吞错或把部分结果当作成功。
- 继续复用现有 canonical content、receipt snapshot codec、`scanMemory` 及 Domain validator，保持 UTC 微秒、严格 JSON 与持久化损坏 fail-closed。

### R4. Read, Pagination, Effective Context And Expiry

- `Get` 必须按 Workspace + stable owner + Memory ID 精确限定；不存在与越权统一返回 `MEMORY_NOT_FOUND`。
- `List` 保持可选 type/status 过滤、`(updated_at,id) < (?,?)`、`ORDER BY updated_at DESC,id DESC`、`Limit+1` 与最后返回项 cursor；GORM 数组参数必须作为单个 PostgreSQL array 绑定，不得展开为多个 placeholder。
- `LoadEffective` 必须在有界根查询中同时过滤 ACTIVE、完整 confirmation、`expires_at > clock_timestamp()`、Workspace、owner 及 global/matching task scope，保持 `updated_at DESC,id DESC`；不允许全量读取后内存过滤或 N+1。
- `ExpireDue` 在一个显式事务中获取一次数据库时间，以 `FOR UPDATE SKIP LOCKED` 选取不超过 `domain.MaxListLimit` 的稳定批次，再逐项 CAS + audit；不并发化循环、不把更新移到事务外。

### R5. Mapping, Errors, Context And Logs

- GORM、`database/sql` 与 pgx 只能出现在 PostgreSQL Adapter；Domain/Application/HTTP 公共契约不变且不暴露具体数据库类型。
- Persistence record 显式声明 schema-qualified 表名、列、UUID、JSONB、nullable 时间与禁用自动时间；不使用 `gorm.Model`、`DeletedAt`、`SELECT *` 或隐式 association。
- Memory content 和 receipt response 使用明确的 JSONB `driver.Valuer`/scanner 或等价单值 carrier；不将裸 `[]byte` 交给 GORM 推断为 `bytea`。
- `sql.ErrNoRows`、`pgx.ErrNoRows` 与 `gorm.ErrRecordNotFound` 按操作语义分别映射为 miss、NotFound 或 CAS conflict，不依赖 `Raw().Scan` 零行默认。transaction/rows/commit 错误分类在保留已有 `foundation.Error` 后必须先检查原始 `ctx.Err()`，再处理 `sql.ErrTxDone`、no-row 与 SQLSTATE。
- 保持现有 SQLSTATE 精确分类：`23505` 幂等冲突；`23503/23514/23502/22P02/55000` 一致性错误；`40001/40P01/55P03/08000/08003/08006/57P01/57014` 可重试。不把它扩展为整个 `08` 类。取消保留 non-retryable 与 `errors.Is`；deadline 暂保留 legacy retryable unavailable 语义，不在 ORM 迁移中顺带改变。
- 所有数据库操作使用传入 context；错误、日志与 GORM logger 不得包含 Memory content、receipt JSON、SQL 参数、DSN 或数据库返回的敏感明细。

### R6. Schema, Connection And Dependency Constraints

- Schema 只以已有 migrations 为事实源；本 child 不新增/修改 migration，不调用 `AutoMigrate`、Migrator 或自动外键生成。
- GORM root 与 Unit of Work 只能分别来自同一 Foundation `platformpostgres.Pool.GORM()` / `Pool.UnitOfWork()`；不独立 `gorm.Open`、不直接在模块内调用 root `Transaction`、不创建第二连接池或新事务抽象。
- 不新增第三方依赖；PostgreSQL array 可复用仓库已固定且 vendored 的 `github.com/lib/pq`，GORM/PostgreSQL driver 沿用 Foundation 版本。

### R7. Verification And Completion Gate

- 静态阶段运行 Memory 相关 test/race、vet、integration compile-only、API/Worker compile-only、vendor/module、Trellis validate 与 `git diff --check`，并执行 Go、SQL 和 Trellis Review。
- TODO 9 必须在 migrated disposable PostgreSQL 上从共享 Pool 构造 pgx 与 GORM Repository；每个顶层场景仅一个 fixture，路径使用隔离 seed 验证生命周期、exact/semantic replay、并发锁顺序、CAS、半失败回滚、append-only trigger、keyset/effective 过滤、`SKIP LOCKED` 与 context 行为等价。
- TODO 9 真实 PostgreSQL 门禁已通过；生产 Composition、legacy 删除与 TODO 3 仍仅由 Final child 负责。本 child 已具备归档条件。

## Out Of Scope

- 修改 Memory Domain/Application/HTTP/Agent/Interview 语义、错误码或对外 API。
- Schema 迁移、数据回填、索引调整、AutoMigrate 或修改已发布 migration。
- TODO 9 前切换 API/Worker/跨模块测试 Composition，或删除 pgx 路径。
- 新建测试文件、全仓测试、在本模块顺带改变 deadline 产品语义或优化其他模块。

## Acceptance Criteria

- [x] `GORMRepository` 完整实现七个现有 Repository 方法，Domain/Application 无数据库实现泄漏，legacy 生产路径保持可用。
- [x] client-key exact replay、Interview provenance semantic replay、双层锁顺序与并发精确一次语义与 pgx 基线一致。
- [x] Candidate/Mutation 的聚合行、append-only audit 和 receipt 在一个显式事务中原子提交；CAS、DB time 与失败回滚可复核。
- [x] Get/List/LoadEffective/ExpireDue 保持 Workspace/owner 隔离、keyset、task/expiry 过滤、稳定顺序、有界读取和 `SKIP LOCKED` 并发语义。
- [x] JSONB、PostgreSQL array、UUID、nullable/UTC 时间、no-row、SQLSTATE、context cause 与日志脱敏与既有契约一致。
- [x] 未引入 migration、AutoMigrate、第二连接池、新依赖、双写或 runtime fallback；API/Worker Composition 仍由 Final child 统一切换。
- [x] 局部 test/race、vet、integration/API/Worker 编译、vendor/module、Trellis validate、Go/SQL Review 和 `git diff --check` 通过并记录。
- [x] TODO 9 真实 PostgreSQL 门禁全部通过；此项不代表生产已迁移。TODO 3 仅阻断 Final。

## Dependencies

- 直接依赖 `08-19-gorm-platform-transaction-foundation`。
- 生产切换依赖全部模块完成、TODO 9 与 Final child；Memory 本身不依赖 Events/Audit/Workflow 的跨 Repository transaction port。
