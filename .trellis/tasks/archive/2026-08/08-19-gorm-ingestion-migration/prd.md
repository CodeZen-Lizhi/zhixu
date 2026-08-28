# Ingestion Repository 迁移到 GORM

## Goal

在不改变 Ingestion 领域端口、数据库 Schema 或生产 Composition 的前提下，为五个 Repository 操作实现可验证的 staged GORM 路径，并保持 Attempt 幂等/CAS、Projection 不可变、Provenance 绑定、Chunk Strategy 隔离和批量事务语义。

## Confirmed Baseline

- Repository 端口由 `CreateAttempt`、`GetAttempt`、`TransitionAttempt`、`GetProjection`、`SaveProjection` 五个方法组成。
- `migrations/00007_ingestion.sql` 定义核心表、唯一键和不可变/状态触发器；`00015_reindex_consumer.sql` 增加 Retrieval 使用的复合唯一键/外键；`00068_capture_profile.sql` 增加 Source Span evidence 字段和形状约束。
- API、Worker 启动/热更新以及 Change Control 集成烟测仍构造 legacy pgx Repository。TODO 9 前这些调用点全部保持不变。
- 平台 Foundation 已提供共享 `Pool.GORM()`；其 GORM 配置关闭默认写事务，因此任何多批次写入必须显式放入一个外层事务。
- 现有 PostgreSQL integration test 只覆盖 pgx 路径，并依赖 `ZHIXU_TEST_DATABASE_URL`；当前无该环境时不能把编译或静态检查当成 TODO 9 证据。

## Requirements

### R1. Scope And Staged Boundary

- 本 child 只修改 `internal/ingestion/adapter/postgres` 和本任务文档；TODO 9 获得真实数据库后可原位扩展现有 integration test，但不新增测试文件。
- 在同包新增 `GORMRepository` / `NewGORMRepository(*gorm.DB)`，实现现有 `domain.Repository`；保留 `Repository` / `NewRepository(DB)` 和 pgx 行为基线。
- TODO 9 前不得修改 `cmd/api`、`cmd/worker` 或跨模块烟测构造，不得删除 legacy pgx 实现，不得完成或归档本 child。
- Production Composition 的统一切换和 legacy 删除归 Final child；本 child 不双写、不运行时 fallback。

### R2. Attempt Idempotency And State CAS

- `CreateAttempt` 保持现有完整幂等唯一键：Source Version、Parser ID/Version/Config Hash、Chunk Strategy、Schema Version 和 Idempotency Key。
- 唯一冲突后必须读取既有 Attempt，并复核 `AttemptNumber` 与 `WorkflowRunID`；绑定不一致继续返回 `INGESTION_IDEMPOTENCY_CONFLICT`，不能把任意冲突当作成功重放。
- `TransitionAttempt` 继续先验证 Domain 状态转移，再以 `WHERE id = ? AND version = ?` 的单语句列白名单 CAS 更新，且只允许 `version = version + 1`；0 行继续映射 `INGESTION_ATTEMPT_VERSION_CONFLICT`。
- 不使用 GORM `Save`、全字段更新或自动时间覆盖 Attempt 状态机。

### R3. Projection Atomicity And Immutability

- `SaveProjection` 必须在一个显式 PostgreSQL/GORM 事务内完成或回滚 Parse Projection、Source Version Provenance、全部 Source Spans 和目标 Strategy/Schema Chunks。
- Parse Projection 继续按 `content_artifact_id + parser_id + parser_version + parser_config_hash + schema_version` 幂等复用；Provenance 继续按既有唯一键追加，不更新或删除已有绑定。
- 新 Projection 的 Span/Chunk 归属必须在写入前重绑定到持久化 Projection；Chunk 只能引用本次写入或已复核属于该 Projection 的 Span。
- 复用 Projection 时必须只读取目标 Chunk Strategy/Schema，比较持久化集合与请求的确定性内容，并仅补齐缺失集合；冲突后重新读取并再次复核，不能静默接受不同内容。
- `GetProjection` 在同一读取事务中返回 Projection、按 `start_byte,id` 排序的 Spans，以及按 `sequence,id` 排序的 Chunks；Strategy/Schema 都非空时精确过滤，两者都为空时保持 legacy 的全 Strategy 兼容语义。
- Parse Projection、Provenance、Span、Chunk 均遵守 insert-only 数据库触发器；不得通过 ORM association、upsert-update 或软删除绕过不可变性。

### R4. Bounded Batch Writes

- GORM 新 Projection 路径不得为每个 Span/Chunk 单独发出 SQL；使用显式固定批次（初始 `projectionBatchSize = 500`）进行 `CreateInBatches` 或等价参数化批量写入。
- 多个批次必须全部运行在 `database.Transaction` 的同一个 `tx` 上；任一批次、复核或 commit 失败均回滚整个 Projection 写入。
- Chunk 的并发幂等插入只允许对精确唯一键 `parse_projection_id + chunk_strategy_version + schema_version + sequence` 使用 `DO NOTHING`，之后一次有界重读并校验完整集合。
- Statement 数可随 `ceil(rows / batchSize)` 增长，但不得随每一条 Span/Chunk 线性增长；不得引入 N+1 读取。

### R5. Mapping And Public Boundaries

- GORM、`database/sql` 与 pgx 只能存在于 PostgreSQL Adapter；Domain/Application/HTTP 公共契约不变且不暴露具体数据库类型。
- Persistence record 显式声明 schema-qualified 表名、列名、UUID、JSONB、nullable 时间和关闭自动时间；不使用 association、`SELECT *`、soft delete 或 GORM 自动时间生成持久事实。
- Warnings、Selector、Heading Path 保持现有 JSON 形状和 fail-closed 解码；批量 record 使用明确的 JSONB `driver.Valuer`/scanner 或等价单值 carrier，禁止让裸 `[]byte` 被当成 `bytea`。
- 所有写入时间继续使用调用方提供且转为 UTC 的时间；扫描后继续通过现有 ID、Domain validator 和 UTC mapper 校验。

### R6. Errors, Context And Logs

- `sql.ErrNoRows`、`pgx.ErrNoRows` 与 `gorm.ErrRecordNotFound` 在相同操作中映射为与 legacy 一致的 NotFound、idempotent miss 或 CAS conflict，不能依赖 `Raw().Scan` 的零行默认行为。
- 保持 SQLSTATE 分类：`23505` 为 `INGESTION_RECORD_CONFLICT`/版本冲突；`23503`、`23514`、`22P02` 为一致性错误；`40001`、`40P01` 为可重试失败；`55000` 为不可变错误。
- 所有调用使用传入 context；共享分类入口必须接收 `ctx`，在分类数据库/commit 错误前优先检查直接 context cause，并在 `sql.ErrTxDone` 且 `ctx.Err()!=nil` 时还原该 cause。取消和 deadline 必须保留 `errors.Is`、使用非重试语义，不能改用 `context.Background()` 或吞掉事务错误。
- 错误、日志和 GORM logger 不得包含原始 Source/Chunk 内容、JSON payload、SQL 参数、DSN 或数据库返回的敏感明细。

### R7. Schema, Connection And Dependency Constraints

- Schema 只以现有 migrations 为事实源；本 child 不新增/修改 migration，不调用 `AutoMigrate`、Migrator 或自动外键生成。
- GORM root 只能来自 Foundation 的共享 `platformpostgres.Pool.GORM()`；不得独立 `gorm.Open`、创建第二连接池或新增事务抽象。
- 不新增第三方依赖；沿用仓库固定的 GORM/PostgreSQL driver 和 vendor 模式。

### R8. Verification And Completion Gate

- 静态阶段运行受影响包 test/race、vet、integration compile-only、API/Worker compile-only、vendor/module、Trellis validate 和 `git diff --check`，并执行 Go、SQL 和 Trellis Review。
- TODO 9 必须在 migrated disposable PostgreSQL 上从共享 Pool 构造 GORM Repository，使用彼此隔离且各自可回滚的 pgx/GORM fixture 验证 legacy/GORM 等价、并发幂等、CAS、事务半失败回滚、策略隔离、不可变触发器、批量 statement 边界和 context 行为。
- TODO 9 不可用时，只能交付未接 Composition 的 staged 实现和静态证据；所有 Acceptance Criteria 保持未勾选，任务保持 `in_progress`。

## Out Of Scope

- 修改 Ingestion Domain/Application/API/Workflow 语义或错误码。
- 修改 Parser、Chunker、Source/Capture/Indexing 行为。
- Schema 迁移、数据回填、索引调整、AutoMigrate。
- TODO 9 前切换 API/Worker/跨模块烟测，或删除 pgx 路径。
- 新建测试文件、全仓测试、性能优化到本模块以外。

## Acceptance Criteria

- [x] `GORMRepository` 完整实现五个现有 Repository 方法，Domain/Application 无数据库实现泄漏，legacy 生产路径保持可用。
- [x] Attempt 创建重放、绑定冲突、合法状态转移和 version CAS 与 pgx 基线一致。
- [x] Projection、Provenance、Span、Chunk 在一个显式事务中原子写入；复用、并发冲突、回滚和不可变性行为可复核。
- [x] Span/Chunk 使用有界批量写入，不退化为逐条 SQL；目标 Chunk Strategy/Schema 隔离、稳定排序和确定性复核成立。（实测 Cnew=4+ceil(spans/500)+ceil(chunks/500)，Creuse=5）
- [x] JSONB、UUID、nullable/UTC 时间、no-row、SQLSTATE、context cause 和日志脱敏与既有契约一致。
- [x] 未引入 migration、AutoMigrate、第二连接池、新依赖、双写或 runtime fallback；API/Worker Composition 仍由 Final child 统一切换。
- [x] 局部 test/race、vet、integration/API/Worker 编译、vendor/module、Trellis validate、Go/SQL Review 和 `git diff --check` 通过并记录。
- [x] TODO 9 真实 PostgreSQL 门禁全部通过；在此之前不得勾选完成、归档或声称生产已迁移。TODO 3 仅阻断 Final。

## Dependencies

- 直接依赖 `08-19-gorm-platform-transaction-foundation`。
- 生产切换依赖全部模块完成、TODO 9 与 Final child；本模块不依赖 Events/Audit/Workflow 的事务 Port。
