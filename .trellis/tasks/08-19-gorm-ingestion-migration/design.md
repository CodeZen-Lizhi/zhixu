# Ingestion GORM Repository 设计

## 1. 边界与架构

本 child 采用 staged 双实现。现有 Service 继续只依赖 `domain.Repository`；legacy pgx Repository 保持生产接线，新 GORM Repository 只由 TODO 9 的真实 PostgreSQL 门禁构造。

```text
Ingestion Application
        |
        v
domain.Repository
        |---------------- Repository     -> pgx DB (production baseline)
        `---------------- GORMRepository -> shared Pool.GORM() (staged)
```

GORM、`database/sql`、pgx 和 persistence record 均留在 `internal/ingestion/adapter/postgres`。不改 Domain/Application/HTTP/Workflow，不新增跨模块事务 Port。

## 2. Schema 与所有权

GORM model 只做映射，不拥有 Schema：

- `00007_ingestion.sql`：`attempt`、`parse_projection`、`source_version_projection`、`source_span`、`canonical_chunk`，以及幂等、状态转移、归属和不可变触发器。
- `00015_reindex_consumer.sql`：Retrieval 对 Projection/Provenance/Chunk 的复合唯一键和外键依赖。
- `00068_capture_profile.sql`：`source_span.evidence_kind`、`derived_excerpt` 与 evidence shape 约束。

任何 GORM tag 都不能被用来生成/修复 Schema；`AutoMigrate`、Migrator、association save、soft delete 全部禁止。

## 3. 文件与类型

### `gorm_repository.go`

- 定义 `GORMRepository{database *gorm.DB}`、`NewGORMRepository` 和 `var _ domain.Repository`。
- 构造器与每次操作拒绝 nil/零值/无 ConnPool/已有 Error 的 GORM root 和 nil context，避免 `WithContext` panic。
- 实现五个 Repository 方法；所有 SQL/Builder 都从 `database.WithContext(ctx)` 或事务回调的 `tx.WithContext(ctx)` 发起。

### `gorm_model.go`

- Attempt/Projection 继续通过显式 Raw SQL 列清单写入和扫描，不保留只为映射 Raw SQL 而存在的未使用 GORM record。
- 仅为 Clause/批量写入定义 adapter-private `provenanceGORMRecord`、`spanGORMRecord`、`chunkGORMRecord`；每个 record 使用 schema-qualified `TableName`、显式 column tag、UUID string、nullable pointer，并关闭 GORM 自动 created/updated time。
- 定义 adapter-private JSONB carrier，实现明确的 `driver.Valuer`/`sql.Scanner`；它只承载已由现有 marshal helper 生成的 JSON，防止裸 `[]byte` 被映射为 `bytea`。
- 提取 pgx/GORM 共用的 record-to-domain mapper、JSON 解码、UTC 和 ID 校验；不复制 Domain 规则。

### `gorm_queries.go`

- 集中放置 GORM `?` 参数化 SQL：Attempt insert/get/CAS、Projection insert/get、精确 `RETURNING` 查询；legacy pgx `$n` SQL 继续保留在 `repository.go`。
- GORM SQL 与 legacy pgx SQL 只允许占位符和文件组织不同，列清单、冲突键、过滤和排序必须一致。
- Builder/Clause 只用于表达更清晰且不会改变 SQL 原语的 provenance/batch insert；动态外部值永不拼进 SQL。

### `repository.go`

- 保留 `Repository`、`NewRepository(DB)` 与 pgx 接口。
- 只在确实消除两实现漂移时复用 query/scanner/validator/classifier；不改变 legacy 公开签名和生产构造。

### Existing Integration Test

- 静态阶段不改测试。
- TODO 9 可原位扩展 `repository_integration_test.go`，但 pgx 与 GORM 使用两套独立 fixture：legacy 继续在 pgx transaction 内 seed/执行/rollback；GORM 从共享 `Pool.GORM()` 开启外层 GORM transaction，在该 transaction 内 seed 并构造 Repository，最终 rollback。
- GORM Repository 自身的 `Transaction` 在测试外层 transaction 中通过 GORM savepoint 执行；Foundation 保持 `DisableNestedTransaction=false`。构造器必须接受有效 root 或 transaction clone，不能只接受 root。
- 两条路径不得共享未提交 seed，也不得假设 pgx transaction 对 `database/sql` facade 可见；分别使用唯一 ID/时间夹具，比较规范化后的领域结果和错误，而不是跨事务复用行。
- 不新建测试文件、不独立 `gorm.Open`；测试结束必须证明两套 fixture 都无数据残留。

## 4. 操作设计

### 4.1 CreateAttempt

1. 校验 context、GORM root 和现有 Domain record。
2. 执行单条 `INSERT ... ON CONFLICT DO NOTHING RETURNING`，不使用 GORM `FirstOrCreate`。
3. 有返回行时映射 `Created=true`。
4. 无行时按完整幂等唯一键查询既有记录，并复核 `AttemptNumber`、`WorkflowRunID`。
5. 不匹配返回 `INGESTION_IDEMPOTENCY_CONFLICT`；匹配返回 `Created=false`。

选择 Raw SQL 是为了保持冲突路径、数据库触发器和返回事实精确，不让 GORM 自动选择字段或隐式更新。

### 4.2 GetAttempt / TransitionAttempt

- `GetAttempt` 使用显式列 Raw `Row().Scan`，将 `sql.ErrNoRows` 映射现有 NotFound。
- `TransitionAttempt` 先读取当前 record 并执行现有 Domain transition validation，再用一条列白名单 SQL：
  `UPDATE ... SET ..., version=version+1 WHERE id=? AND version=? RETURNING ...`。
- 0 行是 version conflict；不做先写后查、不使用 `Save`、不允许全局更新。

### 4.3 GetProjection

1. 使用 `database.Transaction` 建立一致读取边界。
2. 精确读取 Projection；无行返回现有 NotFound。
3. 一次读取全部 Spans，固定 `ORDER BY start_byte,id`。
4. 一次读取 Chunks，固定 `ORDER BY sequence,id`：Strategy/Schema 都非空时精确过滤，两者都为空时保持 legacy 的全 Strategy 分支；一空一非空时也保持现有精确条件行为。
5. 每个 Rows 结果显式 Close、逐行 Scan、检查 `rows.Err()`；任一映射失败回滚读取事务并 fail closed。

不使用 GORM preload/association，避免隐式查询、N+1 和策略过滤遗漏。

### 4.4 SaveProjection

所有步骤必须使用同一个 `tx`：

1. `tx.Raw` 尝试按 Projection 唯一键 insert-returning；冲突无行时精确读取既有 Projection。
2. 通过显式冲突目标对 `source_version_projection` 执行 insert-do-nothing，只追加 Provenance。
3. 若 Projection 新建：
   - 将每个 Span 的 Workspace/Artifact/Projection 重绑定到持久化 Projection，默认空 Evidence Kind 为 `raw_bytes`；
   - 校验所有 Chunk 只引用本次 Span，按 candidate Span ID 映射到持久化 ID；
   - 先批量写入 Spans，再批量写入 Chunks；依赖数据库 trigger 复核归属和版本。
4. 若 Projection 已存在：
   - 一次读取稳定排序的 Spans，以现有 `spanKey` 将输入 Span 映射到持久化 ID；
   - 一次读取目标 Strategy/Schema Chunks，复核已存在项与请求的 sequence/hash/count/version 契约；
   - 批量插入缺失 Chunks，精确 `ON CONFLICT (...strategy/schema/sequence) DO NOTHING`；
   - 冲突后重新读取一次完整目标集合并再次执行确定性复核，防止并发写入不同内容被静默接受。
5. 只有完整集合映射成功才从事务回调返回 nil 并 commit；任一错误由 GORM Transaction 回滚。

Projection/Span/Chunk 不使用 `Save` 或 `OnConflict DoUpdates`，因为数据库明确禁止更新这些事实。

## 5. 批量策略

定义私有 `const projectionBatchSize = 500`。选择该值是为了将每条 16 列记录控制在约 8,000 个绑定参数内，显著低于 PostgreSQL 参数上限，同时避免超大单 statement；TODO 9 以代表性大 fixture 验证后才可调整。

- 新 Projection Spans：`tx.CreateInBatches(spanRecords, projectionBatchSize)`，冲突即错误并回滚。
- 新 Projection Chunks：在 Span 映射完成后 `tx.CreateInBatches(chunkRecords, projectionBatchSize)`；新 Projection 不吞冲突。
- 复用 Projection 的缺失 Chunks：`tx.Clauses(clause.OnConflict{Columns: exactKey, DoNothing:true}).CreateInBatches(...)`，随后一次重读和完整复核。
- 空 slice 不发 SQL，返回非 nil 空结果以保持现有语义。

Foundation 配置 `SkipDefaultTransaction=true`，所以 `CreateInBatches` 自身不能提供跨批次原子性；以上调用必须位于外层 `database.Transaction`。任何 helper 接收的都是事务 `*gorm.DB`，禁止意外回到 root。

## 6. Persistence Mapping

| 数据 | 映射约束 |
| --- | --- |
| UUID | record 内用 string，写前来自已验证 `foundation.ID`，读后继续 `foundation.ParseID` |
| warnings | canonical JSON array；空值保持 `[]`，解码后执行现有 Warning 校验 |
| selector | canonical JSON object；禁止 ORM serializer 改变对象形状 |
| heading_path | canonical JSON array；保持顺序和空数组 |
| created/completed time | 显式传入/扫描并转 UTC；不依赖 GORM `NowFunc`、autoCreateTime/autoUpdateTime |
| nullable IDs/time | 使用 pointer/Null carrier 明确区分 NULL 与零值 |
| content | 只作为参数传给数据库；不进入日志、错误或 GORM SQL logger |

Source Span 需映射 `evidence_kind` 和 `derived_excerpt`，不能只按初始 `00007` 字段集建模。

## 7. Error Contract

- no-row helper 同时识别 `sql.ErrNoRows`、`pgx.ErrNoRows`、`gorm.ErrRecordNotFound`，再由操作上下文决定 NotFound、idempotent miss 或 version conflict。
- `*pgconn.PgError` 保持 SQLSTATE 分类；Foundation `TranslateError=false`，不能用 GORM generic duplicated-key 错误取代稳定 Ingestion code。
- 共享入口固定为 `classify(ctx context.Context, err error, fallback string)`：先检查 `errors.Is(err, context.Canceled/DeadlineExceeded)`；若只得到 `sql.ErrTxDone` 且 `ctx.Err()!=nil`，以 `ctx.Err()` 作为 cause；再保留非 context 的已有 classified error，最后处理 no-row/SQLSTATE/fallback。
- context canceled/deadline 继续使用调用点稳定 fallback code，但返回非重试错误并可 `errors.Is` 到原 cause，使 Application 稳定归约为 `INGESTION_CANCELLED`。
- error wrapper 使用 `%w`/可 Unwrap 类型保留 context、SQLSTATE 和底层 cause；Error string 只包含稳定操作码。
- commit error 单独映射 `INGESTION_PROJECTION_COMMIT_FAILED`；不得在 defer rollback 或后续查询中覆盖首个失败。只有 `ctx.Err()!=nil` 时才把 `sql.ErrTxDone` 还原为 context cause，其他 `ErrTxDone` 仍按 dependency failure 处理。
- nil/invalid GORM root 返回 dependency error，不 panic；nil context fail closed。

如果复用 classifier 时发现 legacy 未显式区分 context code，本 child 只补充 cause 保真并保持外部错误种类；任何错误码行为调整必须先写入基线并在 TODO 9 对两实现共同验证，不能让 staged 路径单独漂移。

## 8. 并发、一致性与性能

- Attempt 并发创建由唯一键仲裁，冲突后必须复核业务绑定；CAS 并发转移只允许一个 version 胜出。
- Projection 并发创建由唯一键仲裁，输家读取同一持久化 identity；Provenance 追加幂等。
- Chunk 并发写按 Strategy/Schema/Sequence 仲裁；冲突后重读和确定性比较，错误内容返回 consistency failure，不做覆盖。
- 一次 `SaveProjection` 的 SQL 复杂度为固定查询数加 `ceil(spans/500) + ceil(chunks/500)` 批次，不按每行发一条 SQL。
- 读取固定为 Projection + Spans + 指定 Chunks，不按 Span hydrate Chunk。
- TODO 9 在现有 integration test 内为共享 GORM transaction 派生一个只计数、不调用 SQL formatter 回调且不保存 SQL/参数的 test logger。实现后分别固化 new/reuse 路径的固定查询常数 `Cnew`/`Creuse`，断言 statement 上界为对应常数加 Span/Chunk 批次数。
- 可由约束、trigger 或 context 稳定触发的 transaction 半失败必须实测回滚。数据库连接在 commit 时断开的精确故障不为此 child 增加生产 hook；它作为 Foundation commit/response-loss 门禁的继承证据和本 child 明示盲区记录，不能伪称已在模块测试注入。

## 9. 发布、回滚与门禁

- 阶段一：只新增 staged GORM 路径；回滚可删除新增文件并恢复本 child 的共享 helper 提取，生产不受影响。
- 阶段二：TODO 9 在 migrated disposable PostgreSQL 上比较 pgx/GORM 行为和执行特征；通过后本 child 才可完成，但仍不改 `cmd/**`。
- 阶段三：Final child 统一把 API/Worker/烟测 Composition 切到共享 GORM root 并删除 legacy allowlist；失败时统一回退构造，不涉及 Schema/数据回滚。

真实 PostgreSQL 是 `ON CONFLICT RETURNING`、trigger、JSONB carrier、批量插入、并发和 transaction rollback 的最终证据。静态编译、DryRun SQL 或 mock 均不能替代 TODO 9。
