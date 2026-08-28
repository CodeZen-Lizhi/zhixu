# Research: Retrieval PostgreSQL SQL/schema/performance and native boundaries

- Query: 盘点 Retrieval 的 PostgreSQL 数据模型、pgvector 查询、COPY、临时表、连接级 advisory lock、逻辑索引构建/切换、River 事务、SQLSTATE、目标索引、EXPLAIN 与容量门禁；划定 GORM Raw 与 audited pgx allowlist，给出单 Pool/固定连接方式、TODO 9 真 PostgreSQL 验收矩阵和最小 staged 文件范围。
- Scope: internal
- Date: 2026-08-21

## Findings

### 1. 结论摘要

Retrieval 不能按“全部 ORM 化”迁移。普通 CRUD、状态迁移和复杂但固定的 PostgreSQL SQL，应通过同一个 `platformpostgres.Pool` 暴露的 GORM root/UoW 执行；只有依赖 pgx 协议或物理连接身份的路径保留原生 pgx。

| 路径 | 新实现边界 | 依据 |
| --- | --- | --- |
| embedding/index version 普通 CRUD | GORM API 或 GORM Raw | `internal/retrieval/adapter/postgres/repository.go:41`、`:76`、`:183` |
| lexical/vector build、search、evidence、delivery、regression、activation/completion SQL | GORM Raw/Exec，事务内使用 opaque UoW scope | SQL 是固定 CTE、`unnest`、锁、DB clock、vector operator 或 deferred-trigger closure；不需要 pgx 协议能力 |
| manifest/source/chunk bulk copy | audited pgx | `pgx.CopyFrom` 是协议级能力，且当前与 metadata insert 同处一个 pgx transaction：`repository.go:110`、`:157`；`snapshot.go:579` |
| snapshot temp tables | audited pgx | 临时表和快照事务必须落在同一物理连接：`snapshot.go:18`、`:54`、`:156` |
| session advisory lock | audited pgx pinned connection | lock/unlock 必须在同一 session；失败时必须淘汰物理连接：`snapshot.go:137`、`source_refresh_lock.go:21`、`:77` |
| transaction advisory lock | GORM Raw | `pg_advisory_xact_lock` 只要求同一数据库事务，不要求暴露 pgx：`repository.go:471` |
| River transaction-scoped enqueue | official `riverdatabasesql` scoped inserter | Foundation 已从 opaque scope 解析同一个 `*sql.Tx`：`internal/workflow/adapter/river/gorm_inserter.go:31`、`:70` |
| River worker/listener runtime | 保留 `riverpgxv5` | 这是父任务允许的运行时 pgx 边界；GORM 迁移只改变 producer 的 transaction integration |
| PostgreSQL physical ANN index DDL | 不属于当前生产迁移 | 当前“index build/swap”是业务 `index_version` 状态切换；HNSW/IVFFlat 只在容量基准测试临时创建：`capacity_benchmark_integration_test.go:505` |

允许的原生 pgx 白名单应收敛为三个可命名能力，而不是允许整个 Repository 继续持有 pgx：

1. `ManifestCopier`：在 pgx transaction 内执行 manifest/source/chunk `CopyFrom`。
2. `SnapshotBuilder`：固定一条 `*pgxpool.Conn`，覆盖 session lock、repeatable-read transaction、temp stage、COPY、commit 和 unlock。
3. `SourceRefreshLease`：固定一条 `*pgxpool.Conn`，覆盖 try-lock/retry、业务 lease 生命周期和 unlock/hijack-close。

每项白名单都需要 owner、原因、接口、调用点、真库测试和退出条件。普通查询、状态更新、`FOR UPDATE`、`SKIP LOCKED`、xact advisory lock、pgvector operator 和 JSON/array cast 均不构成继续暴露 pgx 的理由。

### 2. 已核对的任务与平台约束

- 当前任务要求 ordinary repository 迁入 GORM，同时保留 pgvector、COPY、临时表、session advisory lock、索引版本切换、批处理和 River 原子性：`.trellis/tasks/08-19-gorm-retrieval-migration/prd.md:9`。
- pgx 例外必须是 audited allowlist，且 River 事务内生产必须走 official database/sql integration：`.trellis/tasks/08-19-gorm-retrieval-migration/prd.md:10`、`:11`。
- TODO 9 未通过前，不得切生产 composition、删除 legacy 或完成/归档模块：`.trellis/tasks/08-19-gorm-retrieval-migration/prd.md:12`、`:21`。
- 父任务允许复杂 PostgreSQL SQL 使用 GORM Raw，但禁止 Domain/Application 暴露 GORM、database/sql、pgx 或 `any` transaction handle：`.trellis/tasks/08-18-gorm-data-access-migration/prd.md:20`、`:25`、`:26`。
- Foundation 已建立一个 pgx pool 对应 database/sql facade 与 GORM root：`internal/platform/postgres/pool.go:26`、`:47`、`:157`；GORM 使用 `stdlib.OpenDBFromPool`，没有第二连接池：`internal/platform/postgres/gorm.go:18`。
- UoW 在 GORM transaction 内取得同一个 underlying `*sql.Tx`，对上层只暴露 opaque live scope：`internal/platform/postgres/transaction.go:26`、`:54`、`:70`。
- 平台已在 `AfterConnect` 为每条 pgx connection 注册 pgvector 类型：`internal/platform/postgres/pool.go:102`。database/sql/GORM 的 vector 参数仍可使用 `pgvector.Vector` 的 `sql.Scanner`/`driver.Valuer`：`vendor/github.com/pgvector/pgvector-go/vector.go:15`、`:89`、`:104`。

### 3. PostgreSQL 数据模型与数据库不变量

#### 3.1 Embedding 与业务索引版本

- `retrieval.embedding_version` 固化 provider、adapter、model、dimensions、normalization、distance 与 config contract；同一 contract 唯一：`migrations/00014_retrieval_index_foundation.sql:9`。
- `retrieval.index_version` 以 workspace 为边界，记录 embedding/tokenizer/fusion、manifest hash/count、idempotency、status、degraded、version 和时间戳：`00014_retrieval_index_foundation.sql:23`。
- active index 通过 workspace partial unique index 维持单一性，不是应用层 best effort：`00014_retrieval_index_foundation.sql:72`。
- activation receipt、target/previous 状态与 deferred constraint trigger 形成 commit-time closure；违反时抛 `55000`：`00014_retrieval_index_foundation.sql:155`、`:604`。

#### 3.2 Manifest、projection 与 source snapshot

- manifest chunk 用 `(workspace_id, index_version_id, sequence)` 保持稳定序列：`00014_retrieval_index_foundation.sql:79`。
- projection 同时包含 `tsvector` 与不限定维度的 `vector`，lexical/vector state 由 CHECK 约束封闭：`00014_retrieval_index_foundation.sql:101`。
- source manifest 对 index/version/source 有严格 inclusion/exclusion shape；exact manifest/chunk 由 composite FK 绑定：`migrations/00015_reindex_consumer.sql:78`、`:86`。
- statement-level manifest validation 明确替代逐行 trigger，使 COPY 保持有界：`00015_reindex_consumer.sql:286`。
- delivery、attempt、lease、checkpoint 和 writeback 由 CHECK、partial unique、deferred FK 与 commit-time closure 约束：`00015_reindex_consumer.sql:134`、`:222`、`:233`、`:650`。
- embedding cache 的主键是 workspace、embedding version 和 content hash：`migrations/00016_embedding_hybrid_search.sql:3`；statement trigger 校验维度、norm、normalization 和 immutable identity，违规抛 `23514`：`00016_embedding_hybrid_search.sql:13`。
- completion closure 跨 Workflow、ChangeControl、Core、Ingestion、manifest 和 activation 校验，并在 commit 阶段抛 `55000`：`00016_embedding_hybrid_search.sql:57`。

因此迁移不能把一次 legacy transaction 拆成多个 GORM transaction，也不能把 deferred-trigger 所需的跨表更新拆成 transaction 外的 River/owner 写入。workspace predicate、逻辑删除/状态、锁顺序和 DB clock 都是 SQL contract 的一部分。

### 4. 必须使用 GORM Raw 的 PostgreSQL SQL

以下 SQL 不适合用链式 ORM 拼装，但可以且应该通过 GORM Raw/Exec 执行；参数继续绑定，只有代码拥有的有限模板可插值。

| 模块 | 必须保留的 SQL 语义 | 证据 |
| --- | --- | --- |
| lexical build | `INSERT ... SELECT`、FTS 生成、写后计数/状态验证 | `repository.go:181` |
| vector batch save | 精确集合加锁、`unnest` set update、vector cast、readback | `repository.go:315`、`:375` |
| activation | target/previous `FOR UPDATE`、receipt replay、retire/activate 原子切换 | `activation_tx.go:20` |
| vector page/load/commit | bounded `LIMIT+1`、cache `unnest` insert/readback、projection set update | `vector_build.go:17`、`:153` |
| lexical/vector search | transaction-local threshold、CTE、rank/fusion、exact pgvector distance operator | `search.go:82`、`:118` |
| evidence | `unnest ... WITH ORDINALITY`、JSON aggregation、稳定输入顺序与上限 | `evidence.go:34` |
| dispatcher | workspace xact lock、outbox binding、delivery state、River enqueue/publish 同事务 | `dispatcher.go:134` |
| delivery runtime | `SKIP LOCKED`、DB clock lease、fence/CAS、reclaim/checkpoint | `delivery_runtime.go:24` |
| processor context | 一次性跨表 projection 与 drift/fence 所需事实 | `processor_context.go:31` |
| regression | 锁定 index、manifest/projection hash 与 fail-closed 判定 | `regression.go:24` |
| completion | 固定 lock order、lease recheck、activation、delivery/attempt 与 CC closure 同事务 | `completion.go:42`、`:259` |

#### 参数化与动态 SQL规则

- GORM SQL 使用 `?` placeholder，由 driver 重写；不能机械复制 pgx 的 `$1` 风格后再字符串替换。
- JSONB、UUID/text arrays、nullable UUID/time 和 vector 使用显式 carrier/`driver.Valuer`/`sql.Scanner`；JSONB 与 arrays 在 SQL 中明确 `?::jsonb`、`?::uuid[]`、`?::text[]`。现有 sibling 模式见 `internal/changecontrol/adapter/postgres/gorm_model.go:1`、`internal/graph/adapter/postgres/gorm_model.go:1`。
- vector distance 的 `<=>`、`<#>`、`<->` 只能由内部 distance enum 进入固定 whitelist；当前 renderer 正是三分支：`search.go:235`。workspace、IDs、query vector、filters、times 全部作为 bound args。
- 动态 filter clause 也只能由代码拥有的固定片段和稳定参数顺序组成：`search.go:191`。任何用户输入不得进入 identifier、operator 或 ORDER BY 文本。
- GORM model 必须显式 `TableName`/column tags；不使用 AutoMigrate。平台 GORM 配置刻意关闭 TranslateError，保留 PostgreSQL 原始错误链：`internal/platform/postgres/gorm.go:34`。
- Set-based SQL 必须保持一批一次 statement/有限 statement 数；不能改成逐 chunk `Save`/`Updates`，否则产生 N+1、改变锁窗口并破坏容量假设。

### 5. 必须保留的 audited pgx allowlist

#### 5.1 COPY 与 transaction ownership

`BeginIndex` 当前在同一个 pgx transaction 内创建 index metadata，再 `CopyFrom` manifest：`repository.go:110`、`:157`。GORM UoW 的 underlying transaction 是 `*sql.Tx`；pgx `CopyFrom` 不能附着到这个 transaction。不能先用 GORM 开事务，再从 pool 取得另一条 pgx connection 做 COPY，否则原子性和可见性都会错误。

可行的边界只有两类：

1. 保留整个“metadata insert + manifest COPY + commit”作为窄的 native pgx collaborator；上层只传 typed request/result，不见 pgx transaction。
2. 将 COPY 改为 GORM/database/sql batch insert。该方案会放弃现有 COPY 性能与 statement-trigger 设计，不满足当前 PRD，除非经过容量证据和 ADR 重新批准。

当前任务应选 1。snapshot 的 source/chunk COPY 同理：`snapshot.go:424`、`:579`。

#### 5.2 临时表、repeatable-read snapshot 与固定连接

Snapshot 必须在一条 pinned `*pgxpool.Conn` 上完成以下生命周期：

1. 从共享的 `platformpostgres.Pool` 派生原生 pgx capability，并 acquire 一条 connection：`snapshot.go:18`。
2. 在该 connection 上取得 session advisory lock。
3. 在同一 connection 上 begin repeatable-read transaction，创建 `ON COMMIT DROP` temp stages：`snapshot.go:54`、`:156`。
4. 在同一 transaction 中 page/hash/count、容量门禁、metadata insert、source/chunk COPY。
5. commit 后仍在原 pinned connection 上 unlock。
6. unlock 失败时 `Hijack` 并关闭 physical connection，绝不能把可能仍持锁的 session 放回 pool：`snapshot.go:137`。

这里的关键不是“用一个 DSN”，而是“同一个 Pool、同一条物理 connection”。GORM transaction 不提供 session identity API，因此这个流程必须整体保留 audited pgx，而不是在中间穿插 GORM root。

#### 5.3 Source refresh session lease

`SourceRefreshLease` 同样 acquire 一条 connection，`pg_try_advisory_lock` 失败后每 100ms 重试并尊重 context：`source_refresh_lock.go:21`。Release 必须在原 connection unlock；失败则 hijack-close：`source_refresh_lock.go:77`。它与 snapshot 使用不同 namespace/key，现有集成测试验证此隔离。

#### 5.4 不应列入 pgx 白名单的能力

- `FOR UPDATE`、`SKIP LOCKED`、transaction advisory lock。
- pgvector distance、casts、`unnest`、CTE、JSONB aggregation。
- ordinary rows scan、CRUD、transaction begin/commit/rollback。
- River producer transaction insert。
- PostgreSQL error inspection；`*pgconn.PgError` 仍可从 GORM/database/sql error chain 中 `errors.As`。

### 6. 单 Pool、transaction scope 与 River 原子性

唯一合法 connection topology 是：

```text
platformpostgres.Pool (one pgxpool.Pool)
  -> pgx native capability: only COPY/pinned-session allowlist
  -> stdlib database/sql facade
       -> GORM root/UoW
       -> riverdatabasesql driver / transaction-scoped producer
```

- 禁止 Retrieval 新建第二个 pgx pool、第二个 `sql.DB` 或独立 GORM connection；这会破坏 pool budget、scope identity 和 transaction atomicity。
- GORM repository 构造器应接受完整 Platform Pool/UoW 所需 typed capability，而不是仅接裸 `*gorm.DB` 后再偷偷打开 pgx pool。
- Native collaborator 也必须由同一 Platform Pool 提供，不能从环境变量重连。
- 当前 opaque scope 证明“live transaction”，但未携带可比较的 pool affinity；同 Pool 必须通过构造/Composition 和 TODO 9 fixture 保证。若需要运行时拒绝 foreign active scope，Foundation 需另立契约；Retrieval 不应自行反射内部 scope。
- Workflow 已有 `ScopedTypedJobInserter`，从 opaque scope 解出同一 `*sql.Tx`，调用 official `riverdatabasesql`：`internal/workflow/adapter/river/gorm_inserter.go:31`、`:70`。Retrieval producer 应复用此模式。
- Retrieval 当前 Application `JobInserter.InsertTx` 仍接受 `any` transaction，River adapter 再断言 pgx：`internal/retrieval/application/dispatcher.go:38`、`internal/retrieval/adapter/river/inserter.go:1`。若新路径继续传 `any`，就违反父任务的 no-driver/no-any contract。
- 因而最小范围必须补一个 scoped dispatcher/producer contract；legacy constructor/port 保留直到 Final，不做破坏性替换。
- River worker/listener 可继续 `riverpgxv5`；producer 的 database/sql transaction 和 worker 的 pgx runtime 共用相同数据库 schema，不要求相同 connection。

### 7. 逻辑 index build/swap 与物理 PostgreSQL index

当前 Retrieval 的“build/swap”是业务 `index_version` 生命周期：building -> active/retiring/failed，并由 activation receipt、partial unique 和 deferred triggers 收口。它不是在线执行 `CREATE INDEX CONCURRENTLY` 或 rename/swap PostgreSQL index。

生产 migrations 没有 HNSW/IVFFlat index。小规模 exact search 集成测试显式验证三个 operator 并拒绝 ANN plan：`search_integration_test.go:301`。HNSW/IVFFlat 只由容量 benchmark 动态创建/删除：`capacity_benchmark_integration_test.go:505`；identifier 已由代码生成且 access method 是固定 whitelist。

因此本任务不要新增 physical ANN DDL，也不要把测试 benchmark 的 index build/drop 混进 repository。若未来生产引入 ANN，需单独 ADR/migration，明确：pgvector method/opclass、dimension、lists/m/ef、`CREATE INDEX CONCURRENTLY` 的 transaction 限制、失败恢复、磁盘/锁预算、recall 与 P95 门禁。

### 8. SQLSTATE、context 与 no-row 映射

legacy mapping 是迁移 parity 基准：`internal/retrieval/adapter/postgres/errors.go:11`。

| 条件 | Retrieval 语义 | 迁移要求 |
| --- | --- | --- |
| `sql.ErrNoRows` / `gorm.ErrRecordNotFound` / pgx no rows | not found | 统一进入同一 domain/foundation not-found 语义 |
| `40001`, `40P01`, `55P03` | retryable | 保持 legacy；不要扩大 retry set |
| `23505` | conflict | 保持约束名/operation 上下文，不依赖 GORM TranslateError |
| `23503`, `23514`, `55000` | consistency/invariant failure | 保留 `*pgconn.PgError` cause 和 SQLSTATE |
| `sql.ErrTxDone` | transaction lifecycle failure | 作为 dependency/transaction failure，并保留 cause |
| `context.Canceled`, `context.DeadlineExceeded` | caller cancellation/deadline | 必须让 `errors.Is` 继续命中，不能包装丢 sentinel |
| `57014` | 当前没有独立业务分类 | 若 cause chain 已含 caller context，context 语义优先；否则保持 legacy fallback，未经产品/契约批准不要擅自标成 retryable |

平台 GORM `TranslateError=false`：`internal/platform/postgres/gorm.go:44`。因此 classifier 应先检查 context/cause，再 no-row，再 `errors.As(..., *pgconn.PgError)` 读取 Code；Foundation Error 会 unwrap cause：`internal/foundation/error.go:20`。不要按错误字符串或 constraint message 匹配。

### 9. 目标索引与 EXPLAIN 门禁

#### 9.1 生产索引清单

迁移后相同 SQL 应继续命中或至少可利用以下 migration-owned indexes：

- `uq_retrieval_index_version_active`
- `idx_retrieval_index_version_workspace_status`
- `idx_retrieval_manifest_workspace_index_sequence`
- `idx_retrieval_projection_workspace_index_chunk`
- `idx_retrieval_projection_search_vector`
- `idx_ingestion_canonical_chunk_content_trgm`
- `idx_retrieval_index_activation_workspace_created`
- `idx_workflow_outbox_reindex_unpublished`
- `uq_retrieval_source_manifest_version`
- `idx_retrieval_source_manifest_workspace_index_source`
- `idx_ingestion_attempt_reindex_selection`
- `uq_reindex_delivery_workspace_blocking`
- `idx_reindex_delivery_retry_due`
- `idx_reindex_delivery_writeback_execution`
- `idx_reindex_delivery_attempt_delivery_started`

定义分别位于 `00014_retrieval_index_foundation.sql:72`、`:91`、`:146`、`00015_reindex_consumer.sql:3`、`:121`、`:222`、`:233`。

#### 9.2 EXPLAIN 验收

- 对 GORM Raw 的实际 SQL 做 `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, SETTINGS)`；不能只 EXPLAIN legacy 文本后假定迁移 SQL 等价。
- FTS/trigram structural checks 应覆盖 GIN indexes；workspace/index/chunk、active version、source manifest、delivery retry/claim 应覆盖相应 B-tree/partial unique indexes。
- 小 fixture 可在独立 session 设置 `enable_seqscan=off` 验证 index 可选性，但该结果不能宣称 500k 数据下性能合格。
- vector exact query 应验证 `<=>`、`<#>`、`<->` 与 stable rank；普通集成矩阵不要求 ANN plan。
- 同时记录 statement count，确保 batch/save/evidence/search 没有 N+1；rows 必须及时 close，cancel/deadline 后 connection 可回池。
- 现有 schema/index marker 位于 `repository_integration_test.go:96`、`search_integration_test.go:81`；迁移应在相同真库 fixture 中扩展 legacy/GORM 双实现断言。

### 10. 容量门禁

- Snapshot domain hard limits：page default 1,000、page max 10,000、sources max 10,000、chunks max 500,000：`internal/retrieval/domain/snapshot.go:11`。Snapshot 在写入前执行上限检查：`snapshot.go:75`；现有测试要求超限无 partial state：`snapshot_integration_test.go:304`。
- 项目容量基线是 500,000 chunks、Retrieval P95 <= 2s：`internal/capacity/baseline.go:17`。
- Formal benchmark 固定 dimension 384、5 次 warmup、30 samples、query timeout 10s、recall >= 0.95，并分别测 HNSW 与 IVFFlat：`capacity_benchmark_integration_test.go:31`、`:295`、`:455`。
- benchmark 还对 lexical/vector 输出 JSON EXPLAIN artifacts 并要求 FTS/trigram indexes：`capacity_benchmark_integration_test.go:669`、`:1076`。
- Formal 500k benchmark 需要 explicit external DSN 与 artifact directory：`capacity_benchmark_integration_test.go:180`。TODO 9 的 routine container parity 与 formal capacity/fault run 是两个门禁，不能用小容器 fixture 替代 500k/P95/recall 证据。

### 11. TODO 9 真 PostgreSQL/pgvector 验收矩阵

TODO 9 的权威定义在 `docs/roadmap.md:115`：由 Testcontainers-Go 自动创建、迁移和销毁 production-compatible PostgreSQL/pgvector；同时保留 explicit external DSN 用于性能、故障注入和远程 CI。两个模式必须复用正式 migrations 与核心 assertions；Docker 不可用时明确 skip/actionable error，不能 fake pass。

当前仓库没有 Testcontainers-Go dependency；Retrieval fixture 只读取 `ZHIXU_TEST_DATABASE_URL`，创建独立 database，应用正式 migrations，再打开一个 `platformpostgres.Pool`：`repository_integration_test.go:520`。本次研究环境该变量未设置，因此没有运行真库测试或 benchmark。

建议的最小矩阵：

| 维度 | Container core parity | External DSN/formal |
| --- | --- | --- |
| 数据库 | production-compatible PostgreSQL + pgvector，正式 migrations | 同版本族/扩展，正式 migrations |
| 隔离 | 每 variant 独立 disposable DB；唯一命名支持并行 | 独立 DB/schema，显式确认允许 destructive fixture cleanup |
| 实现 | legacy 与 staged GORM 分别运行同一 assertion set，不在同一状态库互相污染 | GORM candidate 为主，必要时 legacy baseline |
| GORM/pgvector | vector scan/value、三种 exact operator、dimension/norm trigger | 384-dim formal vector workload |
| COPY/native | BeginIndex manifest COPY；snapshot temp stage/source/chunk COPY；commit/rollback 无 partial | 500k snapshot/batch throughput、pool pressure |
| session lock | 竞争、取消、释放、snapshot/source-refresh namespace 隔离、unlock failure 后物理连接不复用 | connection loss/kill/backend termination fault injection |
| transaction | GORM UoW commit/rollback/deferred trigger；foreign/non-live scope failure | serialization/deadlock/lock timeout and retry policy evidence |
| River | database/sql scoped insert 与业务 state 同 commit/rollback；pgx worker 能消费 | response loss/restart/reclaim under realistic worker runtime |
| dispatcher | first/retry replay、workspace serialization、FIFO/SKIP LOCKED、poison/duplicate | contention/connection saturation |
| completion | lock order、lease recheck、activation/CC/delivery closure、fault rollback | lock wait, deadlock observation, failure recovery |
| error mapping | real `23505/23503/23514/55000/40001/40P01/55P03` cause chains；cancel/deadline/no-row/tx-done | forced backend cancel/connection failures，记录未知 SQLSTATE |
| EXPLAIN | actual GORM Raw SQL；production indexes 可选；stable statement count/no N+1 | `ANALYZE, BUFFERS, JSON, SETTINGS` artifacts |
| capacity | hard-limit/no-partial 行为；中小数据 structural plan checks | 500k chunks、384 dims、30 samples、P95 <=2s、recall >=0.95、HNSW+IVFFlat |
| cleanup/logging | 正常/失败后 container、DB、locks、temp tables 清理；日志不含 password/full DSN | artifact summary、cleanup failure 可行动，敏感 DSN 脱敏 |

应扩展现有 Retrieval integration files，而不是为本迁移创建平行测试体系；项目规则在未明确授权时不新增测试文件。TODO 9 的 shared factory 属于 roadmap/父任务基础设施，不能在 Retrieval task 内私建另一套 container factory。

### 12. 最小 staged 文件范围

任务目前只有 PRD，尚无 design/implement；应先把以下边界冻结到设计，再开发。

#### Retrieval PostgreSQL adapter 内

- `gorm_core.go`：single Pool/UoW wiring、Raw helper、no-row/context/SQLSTATE classifier。
- `gorm_model.go`：显式 table/column models 与 JSONB/array/vector/nullable carriers。
- `gorm_repository.go`：ordinary embedding/index CRUD、lexical build、transition/activation 的 GORM/Raw 实现。
- `gorm_vector.go`：vector page/cache/commit set-based SQL。
- `gorm_search.go`、`gorm_evidence.go`：实际 Raw search/evidence SQL 与固定 renderer。
- `gorm_delivery.go`、`gorm_regression.go`：delivery/lease/checkpoint 与 regression Raw paths。
- `gorm_processor_context.go`：仅在 cross-owner read policy 冻结后加入。
- `gorm_dispatcher.go`、`gorm_completion.go`：仅在 scoped producer 与 Workflow/CC owner capability 冻结后加入。
- `native_manifest_copy.go`：窄的 metadata + manifest COPY transaction collaborator。
- `native_snapshot.go`：pinned connection snapshot/temp/COPY/session lock collaborator。
- `native_source_refresh_lock.go`：pinned connection session lease collaborator。

文件可按仓库最终设计合并，但能力边界不能重新揉成一个同时暴露 GORM、pgx pool 和 arbitrary transaction 的大 Repository。Legacy 文件保持不动，直到 TODO 9 与 Final gate。

#### 严格 PRD 目录外但契约上不可避免的最小补充

- `internal/retrieval/application/dispatcher.go`：新增 supplemental scoped dispatcher/job producer contract/constructor，消除新路径的 `any` transaction；保留 legacy port。
- `internal/retrieval/adapter/river/inserter.go` 或 sibling `gorm_inserter.go`：适配 official database/sql scoped inserter；不得再断言 pgx Tx。
- 若 owner 规则禁止 Retrieval 直接更新 Workflow outbox/ChangeControl proposal/execution，则需由对应模块补 scoped capabilities；否则必须在 task design 中明确批准临时 compatibility Raw SQL、测试与退出任务。

当前 Dispatcher 与 Completion 直接跨 owner SQL：`dispatcher.go:134`、`completion.go:259`。只在 `internal/retrieval/adapter/postgres` 新增文件，无法同时满足“opaque GORM scope + official River database/sql + no-any/no-driver leakage”。这是范围冲突，不应通过隐藏 `any` 或复制 transaction unwrap 规避。

#### 明确不进入本阶段

- `cmd` production composition switch。
- migrations/physical ANN DDL。
- Domain contract 改写。
- legacy 删除或默认 constructor 替换。
- TODO 9 shared container infrastructure 的私有 Retrieval 版本。

### 13. Files found

- `.trellis/tasks/08-19-gorm-retrieval-migration/prd.md`：Retrieval 子任务范围、allowlist、River、TODO 9 门禁。
- `.trellis/tasks/08-18-gorm-data-access-migration/prd.md`：父任务 GORM/opaque scope/no-any/Final contract。
- `.trellis/tasks/08-18-gorm-data-access-migration/design.md`：GORM Raw、single pool/UoW、River database/sql 与 pgx allowlist 设计。
- `docs/roadmap.md`：TODO 9 Testcontainers-Go + external DSN 双模式定义。
- `internal/platform/postgres/{pool,gorm,transaction,river}.go`：单 pgx pool、database/sql/GORM facade、opaque UoW、River driver。
- `internal/retrieval/adapter/postgres/repository.go`：ordinary repository、manifest COPY、lexical/vector state、xact lock。
- `internal/retrieval/adapter/postgres/{snapshot,source_refresh_lock}.go`：pinned session lock、temp tables、COPY、unlock/hijack。
- `internal/retrieval/adapter/postgres/{search,evidence,vector_build}.go`：pgvector/FTS、batch evidence、embedding cache paths。
- `internal/retrieval/adapter/postgres/{dispatcher,delivery_runtime,processor_context,regression,completion}.go`：River/outbox/delivery/fence/completion 原子数据流。
- `internal/retrieval/adapter/postgres/errors.go`：legacy SQLSTATE classification。
- `migrations/00014_retrieval_index_foundation.sql`：index/projection/activation schema、indexes 与 deferred trigger。
- `migrations/00015_reindex_consumer.sql`：source manifest/delivery/attempt schema、COPY-friendly statement triggers 与 closure。
- `migrations/00016_embedding_hybrid_search.sql`：embedding cache、vector validation 和 full completion closure。
- `internal/retrieval/adapter/postgres/*_integration_test.go`：现有 explicit-DSN 真库行为矩阵、EXPLAIN 与容量基准。
- `internal/capacity/baseline.go`：500k/P95 项目容量基线。
- `vendor/github.com/pgvector/pgvector-go/vector.go`：vector database/sql Scanner/Valuer 能力。

### 14. Code patterns

- Single pool facade：`internal/platform/postgres/pool.go:26`。
- GORM over pgx stdlib pool：`internal/platform/postgres/gorm.go:18`。
- Opaque GORM/sql transaction scope：`internal/platform/postgres/transaction.go:26`。
- Official River database/sql scoped insert：`internal/workflow/adapter/river/gorm_inserter.go:70`。
- Native manifest COPY transaction：`internal/retrieval/adapter/postgres/repository.go:110`。
- Pinned connection lifecycle：`internal/retrieval/adapter/postgres/snapshot.go:18`。
- Unlock failure quarantine：`internal/retrieval/adapter/postgres/snapshot.go:137`。
- Exact pgvector operator whitelist：`internal/retrieval/adapter/postgres/search.go:235`。
- Set-based vector write/readback：`internal/retrieval/adapter/postgres/repository.go:315`。
- Deferred cross-layer completion closure：`migrations/00016_embedding_hybrid_search.sql:57`。
- Explicit-DSN disposable database fixture：`internal/retrieval/adapter/postgres/repository_integration_test.go:520`。
- Formal capacity/EXPLAIN gate：`internal/retrieval/adapter/postgres/capacity_benchmark_integration_test.go:180`、`:669`。

### 15. External references and versions

本研究未使用外网资料；版本和 API 能力均来自仓库锁定依赖与 vendored source：

- Go `1.25.4`：`go.mod:3`。
- `github.com/jackc/pgx/v5 v5.10.0`：`go.mod`。
- `github.com/pgvector/pgvector-go v0.4.0`：`go.mod`；database/sql carrier 见 vendored `vector.go`。
- `github.com/riverqueue/river v0.40.0`、`riverdatabasesql v0.40.0`、`riverpgxv5 v0.40.0`：`go.mod`。
- `gorm.io/gorm v1.31.2`、`gorm.io/driver/postgres v1.6.2`：`go.mod`。

### 16. Related specs and review rules

- `.trellis/workflow.md`：任务 phase、研究持久化和 implementation/check gates。
- `.trellis/spec/backend/database-guidelines.md:785`：M6 GORM migration、explicit models、Raw SQL、opaque UoW、native exception inventory。
- `.trellis/spec/backend/database-guidelines.md:906`、`:938`：Retrieval exact/ANN、EXPLAIN 与容量声明边界。
- `.trellis/spec/backend/error-handling.md`：context sentinel、cause preservation、SQLSTATE classification。
- `.trellis/spec/backend/quality-guidelines.md:13`：当前 integration 使用 explicit DSN，尚未采用 Testcontainers-Go。
- `.trellis/spec/backend/quality-guidelines.md:94`：小 fixture/EXPLAIN 不能替代 formal capacity evidence。
- `/Users/zhenglizhi/.agents/skills/go-review/references/go-api-data-review.md`：API/data access/transaction/SQL 审查。
- `/Users/zhenglizhi/.agents/skills/go-review/references/go-concurrency-performance-security.md`：context、锁、连接、资源生命周期。
- `/Users/zhenglizhi/.agents/skills/go-review/references/go-performance-review.md`：N+1、批量、allocation 与容量证据。
- `/Users/zhenglizhi/.agents/skills/sql-code-review/SKILL.md`：参数化、workspace/权限、事务、索引、批量与动态 SQL 审查维度。

## Caveats / Not Found

- 当前 task 只有 PRD，没有 design/implement；上述文件拆分是最小 staged 建议，不是已批准实现方案。
- `ZHIXU_TEST_DATABASE_URL` 在本次研究环境中未设置；未运行 Retrieval 真 PostgreSQL integration tests 或容量 benchmark。
- 仓库尚未引入 Testcontainers-Go；TODO 9 是独立 roadmap 基础设施任务，Retrieval 不能在本任务内宣称已满足。
- 未发现生产 migration 中的 HNSW/IVFFlat index。当前 production search 是 exact vector search；benchmark 的临时 ANN DDL 不能被误认为生产 index swap。
- Foundation opaque scope 当前没有公开 pool-affinity identity；同 Pool 只能先由构造/Composition/fixture 保证，运行时拒绝 foreign pool scope 需要 Foundation 层设计。
- Dispatcher/Completion 仍直接写 Workflow/ChangeControl owned tables，且 Application River port 暴露 `any` transaction。这两个契约问题必须在 task design 中冻结；否则“只改 postgres adapter”与父任务 no-any/owner scope 目标冲突。
- PostgreSQL SQLSTATE `57014` 未在 legacy Retrieval classifier 中单独定义。研究不建议顺便扩展 retry 语义；应保持 context-first 与 legacy fallback，除非另有批准的行为契约。
- 现有容量基准要求显式 external DSN 和 artifacts；routine Testcontainers run 不足以证明 500k、P95 或 recall 合格。
