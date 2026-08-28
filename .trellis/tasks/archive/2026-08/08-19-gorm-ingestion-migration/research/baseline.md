# Ingestion GORM 迁移基线

## 结论

Ingestion 可独立实现 staged GORM Repository，但不能机械改写为 ORM CRUD。Attempt 的幂等/状态机和 Projection 的不可变/归属依赖显式 SQL、唯一键与 PostgreSQL trigger；`SaveProjection` 还必须把 Projection、Provenance、Spans、目标 Strategy Chunks 放在同一事务中。当前生产和实库测试均走 pgx，因此 TODO 9 前只能交付未接线实现。

## 端口与调用点

| 事实 | 位置 | 迁移约束 |
| --- | --- | --- |
| 五方法 `domain.Repository` | `internal/ingestion/domain/repository.go` | 不改签名，不泄漏 GORM/sql/pgx |
| pgx Repository/DB 接口 | `internal/ingestion/adapter/postgres/repository.go` | staged 期间保留 |
| API Composition | `cmd/api/main.go:561` | TODO 9 前不改 |
| Worker 构造 | `cmd/worker/main.go:3339` | TODO 9 前不改 |
| Worker 启动/热更新调用链 | `cmd/worker/main.go:1525,2015` | Final 统一切换时复核 |
| Change Control River fault smoke | `internal/changecontrol/application/reindex_river_fault_smoke_integration_test.go:75` | TODO 9 前继续 legacy |
| pgx integration fixture | `internal/ingestion/adapter/postgres/repository_integration_test.go` | TODO 9 原位扩展共享 Pool/GORM，不新建文件 |

## 行为矩阵

| 操作 | 当前行为 | GORM staged 必须保持 |
| --- | --- | --- |
| CreateAttempt | insert-do-nothing-returning；冲突后按完整唯一键读取并复核 Attempt Number/Workflow Run | 单条 insert + 精确重读；不能 FirstOrCreate/Save |
| GetAttempt | 精确 ID 查询，no-row -> NotFound | `Row().Scan`，兼容 sql/pgx/GORM no-row |
| TransitionAttempt | 先 Domain validate，再按 ID/version CAS，列白名单并 version+1 | 单条 Raw UPDATE RETURNING，0 行 -> version conflict |
| GetProjection | 同一 tx 读取 Projection、稳定 Spans；版本都非空时读目标 Chunks，都为空时读全部策略 | 无 association/N+1，保持 legacy 双空兼容 |
| SaveProjection/new | 一个 tx 依次写 Projection、Provenance、逐条 Spans/Chunks | 一个显式 GORM tx；Spans/Chunks 改为有界批次 |
| SaveProjection/reuse | 读取 Spans/目标 Chunks，比较确定性集合，补齐新策略 | stable span identity、精确冲突目标、冲突后重读复核 |

## Schema 事实

### `00007_ingestion.sql`

- `parse_projection` 唯一键：Content Artifact + Parser ID/Version/Config Hash + Schema Version。
- `attempt` 幂等唯一键：Source Version + Parser contract + Chunk Strategy + Schema + Idempotency Key。
- `canonical_chunk` 唯一键：Parse Projection + Chunk Strategy + Schema + Sequence。
- Trigger 交叉校验 Workspace、Artifact、Projection、Span、Parser/Schema 归属；Attempt 只允许合法转移和 version+1。
- Projection、Provenance、Span、Chunk 更新/删除被拒绝。

### Later Migrations

- `00015_reindex_consumer.sql` 为 Retrieval manifest 增加对 Projection/Provenance/Chunk 精确复合键的依赖；字段、唯一键和写入顺序不能改变。
- `00068_capture_profile.sql` 给 `source_span` 增加 `evidence_kind` 与 `derived_excerpt`，要求 raw/derived 两种形状；GORM model 必须包含现行字段而非只复制初始 migration。

## SQL 与数据映射风险

1. GORM Raw 使用 `?`，现有 pgx SQL 使用 `$n`；两套 SQL 必须集中并逐项校验参数数量/列顺序。
2. `Raw().Scan` 零行不可靠；精确查询和 insert-do-nothing-returning 必须通过 `Row().Scan`/RowsAffected 显式识别 no-row。
3. Warnings、Selector、Heading Path 是 JSONB。批量 model 不能用会被驱动当成 `bytea` 的裸 `[]byte`；使用明确 Valuer/Scanner 和 `type:jsonb`。
4. Attempt、Projection、Span、Chunk 都不适合 GORM `Save`；它可能写零值、自动时间或触发不可变 update。
5. 新 Projection 的 candidate Span ID 当前就是持久化 ID；复用 Projection 时必须用稳定 `spanKey` 映射输入身份，不能假设 ID 相同。
6. 并发 Chunk `DO NOTHING` 后必须重读并比较内容，不能把唯一键相同但内容不同当作成功。

## 批量与事务事实

- 当前 pgx 新 Projection 路径按每个 Span/Chunk 发 SQL，是迁移时明确要收敛的性能边界。
- vendored GORM 支持 `CreateInBatches` 和 `clause.OnConflict{DoNothing:true}`。
- Foundation 配置 `SkipDefaultTransaction=true`；`CreateInBatches` 不能单独保证多个 batch 原子，必须由 `database.Transaction` 包住所有 batch 和前后查询。
- 初始批次选 500：每行最多约 16 列，单 statement 约 8,000 参数，留有足够 PostgreSQL 参数预算。TODO 9 用 5,000+5,000 fixture 验证 statement 数、连接占用和事务时长。

## Error Baseline

| 条件 | 当前分类 |
| --- | --- |
| no row | 操作相关 NotFound/idempotent miss/version conflict |
| `23505` | `INGESTION_RECORD_CONFLICT` / VersionConflict |
| `23503`, `23514`, `22P02` | `INGESTION_DATA_INVALID` / ConsistencyViolation |
| `40001`, `40P01` | RetryableFailure |
| `55000` | `INGESTION_IMMUTABLE` / ConsistencyViolation |
| other DB/commit | 操作稳定 code，保留底层 cause |

GORM staged 必须保留 `errors.Is(context.Canceled/DeadlineExceeded)`。共享 classifier 需接收 `ctx`，在数据库错误分类前检查直接 context cause；仅当返回 `sql.ErrTxDone` 且 `ctx.Err()!=nil` 时改以该 context cause 包装。legacy/GORM 必须共同使用该入口，不能只让新路径改变 API/Workflow 归约。

## Existing Tests

`repository_integration_test.go` 已覆盖：

- derived evidence replay；
- Attempt 幂等冲突与 Created 语义；
- Projection 复用和第二 Chunk Strategy；
- GetProjection Strategy 过滤；
- Attempt 合法 CAS 与 stale conflict；
- 不可变 update 拒绝；
- 跨 Workspace、非法状态和 Parser contract mismatch。

Application/Domain/HTTP/Workflow 快速测试覆盖状态推进和协议，但不执行 GORM SQL。现有 integration fixture 构造 `NewRepository(pgx.Tx)`，其 seed 位于未提交 pgx transaction；共享 `Pool.GORM()` 无法读取该 transaction。TODO 9 必须为 GORM 路径另开外层 GORM transaction、在其中 seed/执行并 rollback，Repository 内部 transaction 走 nested savepoint。未配置 `ZHIXU_TEST_DATABASE_URL` 时跳过真实数据库。

## TODO 9 缺口

- GORM `ON CONFLICT ... RETURNING` 的 no-row 行为和 SQLSTATE error chain。
- JSONB Valuer/Scanner 与批量 insert 的真实 pgx stdlib 绑定。
- 多 batch 外层 transaction 的半失败回滚；commit-time connection/response loss 继承 Foundation 门禁，在本 child 明示为不可稳定注入盲区。
- Attempt/Projection/Chunk 并发赢家、冲突后确定性复核。
- Source Span 现行 evidence 字段、所有 trigger 和 Retrieval 复合依赖。
- cancel/deadline/deadlock/serialization error cause 与连接释放。
- 只计数、不保存 SQL/参数的 GORM test logger，以及 5,000 Spans + 5,000 Chunks 的 statement 上界、事务时长和目标查询索引证据。

在这些证据取得前，本 child 只能保持 planning 或 `in_progress` staged 状态，PRD Acceptance Criteria 不得勾选。
