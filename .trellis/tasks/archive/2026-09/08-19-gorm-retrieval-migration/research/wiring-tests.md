# Research: Retrieval 构造、跨模块装配与 TODO 9 验收

- Query: 盘点 Retrieval 的生产/测试构造点、API/Worker 调用、Workflow/River/Change Control 依赖、现有 integration/benchmark/EXPLAIN/fault tests，并提出不修改 `cmd/**` 的 staged GORM 装配与 TODO 9 可执行验证矩阵。
- Scope: internal
- Date: 2026-08-21

## Findings

### 1. 结论

1. TODO 9 是本迁移的真实数据库验收与生产 Composition 硬门禁，不是 Retrieval 子任务中的一个普通编码步骤。它要求 Testcontainers-Go 自动创建、正式迁移、隔离和销毁生产兼容 PostgreSQL/pgvector，并保留互斥的外部 DSN 模式（`docs/roadmap.md:115-120`）。父任务明确规定 TODO 9 前可以提交 staged GORM 实现，但不得切生产、删除 legacy、勾选完成或归档（`.trellis/tasks/08-18-gorm-data-access-migration/prd.md:53-65`；本任务 `prd.md:9-21`）。
2. 当前 TODO 9 尚未落地：`go.mod`、`go.sum`、`vendor/modules.txt` 均没有 Testcontainers 依赖，仓库搜索也没有 Testcontainers factory；质量规范仍确认真库测试依赖显式 `ZHIXU_TEST_DATABASE_URL`（`.trellis/spec/backend/quality-guidelines.md:13`）。本次环境中 Docker CLI/daemon 可用（server 29.4.0），但 `ZHIXU_TEST_DATABASE_URL` 与 `ZHIXU_RETRIEVAL_BENCHMARK_ARTIFACT_DIR` 均未设置，因此现有 Retrieval integration/benchmark 入口会 skip，不能形成 GORM 等价证据。
3. 生产构造分成四个 PostgreSQL surface：`Repository`（索引构建、Snapshot、Activation、Completion）、`SearchRepository`（Search/Evidence）、`DeliveryRepository`（lease/checkpoint/fence）、`Dispatcher`（Workflow Outbox -> Delivery -> River）。没有 Retrieval `NewStore` 符号；`application.Store` 是由 `*postgres.Repository` 实现的接口（`internal/retrieval/application/service.go:18-33`）。
4. API 只构造只读 `SearchRepository`；Worker 同时构造 Search、Repository、Delivery 和 Dispatcher。生产 Worker 的 River runtime/worker/listener 必须继续使用 `riverpgxv5`；只有调用方事务内的 job producer 改用 `riverdatabasesql.InsertTx`（`internal/platform/postgres/river.go:9-15`，`internal/workflow/adapter/river/client.go:69-84`）。
5. 不能把现有 `application.JobInserter.InsertTx(..., any, ...)` 直接复用于 GORM scope。Foundation/Workflow 已锁定新路径只能公开 `foundation.TransactionScope`，并通过同一 `platformpostgres.Pool` 解出 `*sql.Tx` 给 `riverdatabasesql`（`internal/workflow/adapter/river/gorm_inserter.go:14-23,41-108`）。Retrieval staged 实现需要并行 scoped Dispatcher port/constructor，legacy `any` 路径保留给现有生产；否则会把 GORM scope 塞进 legacy `any` 接口，违反父任务事务边界。
6. 另有两个必须显式处理的跨 owner 依赖：Organizing 当前在自己的 `pgx.Tx` 中直接构造 Retrieval `SearchRepository(tx)`；Completion/Dispatcher 当前直接锁、读写 Workflow 与 Change Control 表。前者应由 Organizing child 改接 Retrieval scoped verifier；后者要么由父设计明确批准为 Retrieval 的跨 schema Raw SQL allowlist，要么由 Workflow/Change Control 提供 caller-owned `TransactionScope` port。不能在 Retrieval GORM 路径里开启第二事务或 fallback 到 legacy。

### 2. 任务与依赖状态

| Task | 当前状态 | 对 Retrieval 的约束 |
| --- | --- | --- |
| `gorm-platform-transaction-foundation` | `in_progress` | 已提供单一 Pool、GORM root、UoW、opaque scope 与 database/sql River driver；真库互操作仍由 TODO 9 验收。 |
| `gorm-workflow-migration` | `in_progress` | 已有 scoped typed River producer；Worker/listener 保持 pgx。 |
| `gorm-changecontrol-migration` | `in_progress` | staged GORM owner 已存在，但当前设计只明确 Graph 的 scoped Knowledge Proposal port，尚未发现供 Retrieval Completion 更新 Execution/Proposal 的 scoped owner port。 |
| `gorm-modelsettings-migration` | `in_progress` | `GORMRepository.CheckEnqueue(ctx, TransactionScope)` 已实现，是新 River producer 的 scoped fence（`internal/modelsettings/adapter/postgres/gorm_runtime.go:308-335`）。 |
| `gorm-retrieval-migration` | `planning` | 本任务；TODO 9 前只允许未接生产的 sibling GORM 实现。 |
| `gorm-organizing-migration` | `planning` | 负责替换 Organizing 事务内的 legacy `NewSearchRepository(pgx.Tx)` 消费点。 |

Foundation 当前能拒绝 nil、错误类型和失活 scope，但 scope 没有 Pool identity，无法识别“另一 Pool 的仍活跃 scope”（`internal/platform/postgres/transaction.go:18-22,51-67,89-97`；`.trellis/tasks/08-19-gorm-modelsettings-migration/design.md:12`）。因此 staged constructor 必须从一个完整 `*platformpostgres.Pool` 派生所有 GORM/UoW/native/River 依赖；同池 affinity 是 Composition 与 TODO 9 的硬断言，不能宣称已由运行时类型检查证明。

### 3. Retrieval PostgreSQL 构造器及其职责

| Constructor | 定义与输入 | 实现接口/职责 | 当前事务形状 |
| --- | --- | --- | --- |
| `postgres.NewRepository(DB)` | `internal/retrieval/adapter/postgres/repository.go:16-33` | `application.Store`，并由同一 concrete repository 实现 Regression、source refresh、Completion 等扩展端口 | `DB.Begin -> pgx.Tx`；`BeginIndex` 使用 `CopyFrom`（同文件 `157-166`） |
| `postgres.NewSearchRepository(SearchDB)` | `internal/retrieval/adapter/postgres/search.go:25-45` | `application.SearchStore`、Evidence reference reads | lexical search 开短事务并 `SET LOCAL`；vector 使用固定 pgvector operator/raw query（同文件 `82-165,238-249`） |
| `postgres.NewDeliveryRepository(DB, IDs)` | `internal/retrieval/adapter/postgres/delivery_runtime.go:15-29` | Delivery claim/heartbeat/checkpoint/fail、Processor context | 每次 mutation 独立短 `pgx.Tx`，DB clock + lease/fence |
| `postgres.NewDispatcher(DispatcherDB, IDs, ReindexRiverInserter)` | `internal/retrieval/adapter/postgres/dispatcher.go:25-57` | 返回 `*application.Dispatcher`；first/retry 公平派发 | Store 开短 `pgx.Tx`，同事务 claim/persist Delivery/mark outbox/insert River |
| `postgres.NewDispatcherWithConsumer(...)` | `internal/retrieval/adapter/postgres/dispatcher.go:49-57` | 自定义稳定 consumer identity | 仅由 `NewDispatcher` 调用；未发现其他构造点 |
| `application.NewDispatcher(Store, JobInserter)` | `internal/retrieval/application/dispatcher.go:38-69` | Runner 使用的 `BatchDispatcher` | legacy `JobInserter` 的 transaction 是 `any` |

`Repository` 不是简单 CRUD：Worker 把同一实例注入 `Service`、`RegressionService`、`VectorRecoveryBuilder`/`VectorBuilder`、`SourceRefresher` 与 `CompletionService`。`DeliveryRepository` 同时供 `DeliveryRuntime` 和 `Processor` 使用，Completion 则回到 `Repository`。staged GORM constructor 不能只迁移 `application.Store` 的表面方法而遗漏同一 concrete type 上的这些端口。

### 4. 生产 Composition 全量调用点

#### API

| 调用点 | 下游用途 |
| --- | --- |
| `cmd/api/main.go:960` | `newOrganizingHandler`：Search/Evidence 注入 Organizing owner。 |
| `cmd/api/main.go:1478` | `newRetrievalHandler`：公开 Search + Evidence HTTP handler。 |
| `cmd/api/main.go:1578` | `newArtifactCitationVerifier`：Artifact citation/evidence 校验。 |

API 没有构造写入型 Retrieval Repository、Delivery 或 Dispatcher；三个入口都是 `NewSearchRepository(pool)`。

#### Worker

| 调用点 | 下游用途 |
| --- | --- |
| `cmd/worker/main.go:2292` | Tool runtime 的 Retrieval Search/Evidence 能力。 |
| `cmd/worker/main.go:2780` | Conversation/Agent RAG Search。 |
| `cmd/worker/main.go:3113` | Workspace analysis Search/Evidence。 |
| `cmd/worker/main.go:3360` | `newSourceProcessingComponents` 的写入型 Repository；供索引 Service、Regression、Vector build/recovery、Source refresh。 |
| `cmd/worker/main.go:3505` | `newReindexComponents` 的 DeliveryRepository；供 DeliveryRuntime、Processor。 |
| `cmd/worker/main.go:3547` | `newReindexComponents` 的 Dispatcher；使用 `reindexriver.NewInserter(insertClient)`。 |

`sourceProcessingComponents.store` 当前是 concrete `*retrievalpostgres.Repository`（`cmd/worker/main.go:3316-3325`），不是窄 Application interface。Final 切线时要么把字段窄化为其实际消费端口集合，要么一次性换成 GORM sibling concrete type；Retrieval child 不应提前修改 `cmd/**`。

Worker 把 Reindex Worker 注册进现有 `riverpgxv5` worker bundle（`cmd/worker/main.go:1918-1935`）。生命周期固定先启动 Dispatcher、再启动 River，关闭时先停 Dispatcher、再停 River（`cmd/worker/lifecycle.go:131-161,172-202`）；readiness 还要求 `ReindexDispatcherStarted=true`（`internal/workflow/runtime/readiness.go:47-59,120-123,150-178`）。Final 不得因 producer 改成 database/sql 而改变这些 consumer/lifecycle 语义。

#### 生产跨模块直连点

- `internal/organizing/adapter/owner/transaction_fence.go:29-64,66-135`：owner adapter 接收 `any` 并断言 `pgx.Tx`，锁定 Source/Ingestion/Active Manifest 后在 `:111` 直接调用 `retrievalpostgres.NewSearchRepository(tx)`。这是 Retrieval Search 的第七个生产构造点，但不在 `cmd/**`；由 Organizing child 迁移为 caller-owned scope 消费。
- `internal/changecontrol/application/writeback_service.go:795-814`：Change Control 生成稳定 `retrieval.revision.reindex_requested` Outbox；event type/schema/binding 由 `internal/retrieval/contract/reindex.go` 拥有。
- `internal/retrieval/adapter/postgres/dispatcher.go:85-166,236-284`：Dispatcher 读取/发布 Workflow Outbox，并联接 Change Control execution/proposal/commit binding。
- `internal/retrieval/adapter/postgres/completion.go:41-166,169-210`：Completion 在一个事务中锁定并更新 Retrieval Activation/Delivery、Change Control Writeback Execution/Proposal；这是不可拆分的跨 owner 原子边界。

### 5. 测试中的构造调用清单

以下为对 `cmd`、`internal/retrieval`、`internal/organizing`、`internal/changecontrol`、`internal/agent` 的精确符号搜索结果；共享 fixture 后的测试不会重复显示 constructor。

#### 外部包/Composition 测试

| Constructor | 调用点 |
| --- | --- |
| `retrievalpostgres.NewRepository` | `cmd/worker/workspace_analysis_conversation_integration_test.go:234`; `cmd/worker/rag_compose_fixture_integration_test.go:231`; `internal/retrieval/http/handler_postgres_integration_test.go:43`; `internal/changecontrol/application/reindex_river_fault_smoke_integration_test.go:96` |
| `retrievalpostgres.NewSearchRepository` | `cmd/worker/workspace_analysis_conversation_integration_test.go:278`; `cmd/worker/rag_conversation_integration_test.go:282`; `cmd/worker/artifact_generation_success_integration_test.go:164`; `internal/agent/adapter/workflow/executor_integration_test.go:120`; `internal/retrieval/http/handler_postgres_integration_test.go:47`; `internal/changecontrol/application/reindex_river_fault_smoke_integration_test.go:681` |
| `retrievalpostgres.NewDeliveryRepository` | `internal/changecontrol/application/reindex_river_fault_smoke_integration_test.go:137` |
| `retrievalpostgres.NewDispatcher` | `internal/changecontrol/application/reindex_river_fault_smoke_integration_test.go:164` |

#### `internal/retrieval/adapter/postgres` 同包测试

| Constructor | 调用点 |
| --- | --- |
| `NewRepository` | shared fixture `repository_integration_test.go:576`; 第二连接 session-lock 用例 `source_refresh_lock_integration_test.go:54`; Completion response-loss/delay/逐阶段 fault wrapper `completion_integration_test.go:26,209,221,251` |
| `NewSearchRepository` | Evidence integration `evidence_integration_test.go:23,67,95,119,190,205`; Evidence unit fake `evidence_test.go:19,62,105,150,230,241`; Search integration `search_integration_test.go:24,106,286,315`; capacity method pool `capacity_benchmark_integration_test.go:416` |
| `NewDeliveryRepository` | Processor context `processor_context_integration_test.go:20,50,74,93`; Delivery runtime `delivery_runtime_integration_test.go:22,88,132,165,206,303`; Dispatcher retry assertion `dispatcher_integration_test.go:178`; Completion fixture `completion_integration_test.go:315` |
| `NewDispatcher` | Dispatcher integration `dispatcher_integration_test.go:42,63,80,103,107,139,164,260,291,315,329`; Completion fixture `completion_integration_test.go:304` |

### 6. Workflow/River/Change Control 数据流

```text
Change Control Safe Writeback
  -> workflow.outbox_event(retrieval.revision.reindex_requested)
  -> Retrieval Dispatcher short transaction
       lock/select Workflow Outbox
       validate Change Control writeback binding
       create/replay retrieval.reindex_delivery
       mark Outbox published
       insert workflow.river_job in the same transaction
  -> riverpgxv5 Retrieval Worker
  -> Delivery claim/checkpoints + Snapshot/Index/Regression
  -> Completion short transaction
       Retrieval Activation + Delivery success
       Change Control Execution + Proposal completed
  -> Search/Evidence reads active Retrieval index
```

Current River adapter uses a stable three-field payload (`schema_version`, `delivery_id`, `dispatch_no`) and stable kind `retrieval_reindex_delivery_v1` (`internal/retrieval/adapter/river/args.go:14-52`). Its inserter currently accepts `any` and delegates to the legacy Workflow typed pgx inserter (`internal/retrieval/adapter/river/inserter.go:11-69`).

The approved GORM path already exists at the platform/Workflow layer:

- one `platformpostgres.Pool` owns the physical pgx pool, its database/sql facade and GORM root (`internal/platform/postgres/pool.go:25-43,57-75,157-177`);
- UoW creates one GORM transaction and captures its underlying `*sql.Tx` in an opaque active scope (`internal/platform/postgres/transaction.go:35-87`);
- `NewScopedTypedJobInserter` builds an insert-only `riverdatabasesql` client from that same Pool, checks the Model Settings scoped fence in the same scope, then calls River `InsertTx` (`internal/workflow/adapter/river/gorm_inserter.go:41-108`);
- vendored River v0.40.0 explicitly supports wrapping `*sql.Tx` (`vendor/github.com/riverqueue/river/riverdriver/riverdatabasesql/river_database_sql_driver.go:37-48,94-106`; `vendor/github.com/riverqueue/river/client.go:1857`), while the database/sql driver has no listener (`river_database_sql_driver.go:63-65,90-92`).

### 7. 已批准的 native pgx 边界

| 能力 | 代码证据 | staged 处理 |
| --- | --- | --- |
| physical pool + pgvector type registration | `internal/platform/postgres/pool.go:47-75` | 平台 allowlist；所有 surface 从同一个 Pool 派生。 |
| manifest/chunk bulk COPY | `repository.go:157-166`; `snapshot.go:425-437,617-619,660` | 保留窄 native bulk capability；不把 `pgx.Tx` 暴露给 Application。 |
| session advisory source-refresh lock | `source_refresh_lock.go:34-101` | 保留专用 `pgxpool.Conn`，release/hijack/close 语义不变。 |
| repeatable-read Snapshot + temp tables + session lock | `snapshot.go:121-183` | 保留专用连接与 native transaction；temp table 和 COPY 必须在同一 session。 |
| Worker/listener/runtime | `internal/workflow/adapter/river/client.go:69-84`; `cmd/worker/main.go:1924-1935` | 继续 `riverpgxv5`，不使用 `riverdatabasesql` 启 Worker。 |

`FOR UPDATE SKIP LOCKED`、transaction advisory lock、固定 pgvector operators 和其他复杂 SQL 可以留在 Repository 管理的 GORM `Raw`/`Exec`/`Clauses` 中，不因此自动成为 native pgx allowlist。当前生产代码没有运行时创建/切换 HNSW/IVFFlat DDL；capacity test 会临时创建两种 ANN index。任务中的“索引切换”主要是 active IndexVersion/Activation 事务，不应误写成生产 DDL 切换。

### 8. 不修改 `cmd/**` 的 staged GORM 装配

#### 8.1 Constructor 形状

建议新增 sibling，不替换/改名现有构造器：

```go
NewGORMRepository(pool *platformpostgres.Pool, completionOwner ScopedCompletionOwner) (*GORMRepository, error)
NewGORMSearchRepository(pool *platformpostgres.Pool) (*GORMSearchRepository, error)
NewGORMDeliveryRepository(pool *platformpostgres.Pool, ids foundation.IDGenerator) (*GORMDeliveryRepository, error)
NewGORMDispatcher(
    pool *platformpostgres.Pool,
    ids foundation.IDGenerator,
    options workflowriver.Options,
    fence workflowriver.ScopedEnqueueFence,
) (application.BatchDispatcher, error)
```

具体命名可随实现收敛，但有四项不可变约束：

1. constructor 接收完整 `*platformpostgres.Pool`，在内部同时派生 `GORM()`、`UnitOfWork()`、native capability 和 River SQL producer；不得让调用方分别传 `*gorm.DB`、UoW、`*pgxpool.Pool`，否则同池 affinity 无法审计。
2. `GORMRepository` 组合普通 GORM query/UoW 与极窄 native Snapshot/COPY/session-lock helper；native helper 只服务明确 allowlist，普通方法不能回到 legacy `DB`。
3. `NewGORMDispatcher` 按 Workflow 已建立的模式，拒绝 `options.EnqueueFence` legacy fence，内部用 `pool.DB()` 建只用于 schema/queue metadata 的 pgx Client，再用同一 Pool 构造 `NewScopedTypedJobInserter[reindexriver.Args]`。Final 另外从同池/同 options 创建 `riverpgxv5` runtime Client。
4. 所有 sibling 在 TODO 9 前只由 compile/unit fixture 或未来 TODO 9 factory 构造；现有 `cmd/api`、`cmd/worker` 和 legacy constructor 均不改，因此无双写、双读、selector 或 fallback。

#### 8.2 Dispatcher 的必要 scoped port

现有 `application.JobInserter`/`DispatcherStore` 把 transaction 定义为 `any`（`internal/retrieval/application/dispatcher.go:38-46`），现有 PostgreSQL adapter 再把它断言为 pgx transaction（`internal/retrieval/adapter/postgres/dispatcher.go:25-33,59-67`）。直接把 `foundation.TransactionScope` 当作 `any` 传入虽然能编译，但违反 Foundation 任务已锁定的新事务契约，也容易误接 legacy inserter。

推荐在 Retrieval Application 增加并行的 `ScopedJobInserter`、`ScopedDispatcherStore` 和 `ScopedDispatcher`（均只暴露 `foundation.TransactionScope`），并让 legacy/scoped Dispatcher 共享 package-private 的 first/retry 公平调度函数。`ScopedDispatcher` 仍实现现有稳定 `BatchDispatcher`，所以 Runtime Runner 无需改。此为 additive staged contract，legacy 不变；但它超出当前 PRD “仅迁移 `adapter/postgres`”的字面范围，实施前应由父任务确认边界。若严格禁止触碰 Application，则 River GORM 原子路径无法在不使用 `any` 或复制 Application 公平规则的前提下完成，届时只能先 stage Repository/Search/Delivery 并保持本任务未完成。

#### 8.3 Search/Organizing scoped 消费

`GORMSearchRepository` 应同时提供 caller-owned scope 的只读方法（或一个窄 `ScopedFrozenSearchVerifier`），内部调用 `platformpostgres.GORMTransaction(scope)`，不创建新事务。Organizing child 用该能力替换 `NewSearchRepository(pgx.Tx)`；根级 API/Worker Search 继续使用现有 `SearchStore` 方法。Retrieval child 不修改 Organizing 或 `cmd/**`。

#### 8.4 Change Control/Workflow owner 边界

当前 GORM Change Control staged 代码未发现 Retrieval Completion 所需的 scoped owner capability；Workflow staged 代码也未发现 Retrieval Outbox publish 的 scoped consumer capability。实现前需要父任务在下面两种方案中明确一种，不能静默混用：

- preferred：Change Control 提供 caller-owned scope 的 lock/validate/complete port，Workflow 提供 scoped Outbox claim/publish port；Retrieval 的 UoW 仍是唯一 commit owner；
- explicit allowlist：父设计明确批准 Retrieval 继续以 GORM Raw/Exec 管理现有跨 schema SQL，并以 TODO 9 fault/concurrency test 锁定它。此方案保留既有 ownership 债务，但行为改动最小。

无论选择哪种，禁止 Completion 先提交 Retrieval、再单独提交 Change Control；禁止 Dispatcher 先发布 Outbox、再另事务插入 River。

### 9. 现有真实 PostgreSQL、EXPLAIN、容量和故障证据

#### Fixture 现状

- `internal/retrieval/adapter/postgres/repository_integration_test.go:520-580`：读取外部 DSN，创建唯一数据库，运行正式 migration，打开完整 `platformpostgres.Pool`，清理数据库；最后固定构造 legacy `NewRepository(database.DB())`。
- `internal/retrieval/http/handler_postgres_integration_test.go:800-856`：重复实现另一套 HTTP 数据库创建/迁移/清理逻辑。
- 两者在 DSN 缺失时 `t.Skip`（分别 `:522-525`、`:802-805`）。TODO 9 应由单一 factory 收敛容器/外部 DSN 模式；Retrieval 测试只申请隔离且已迁移的 Pool，不复制容器管理。

#### Adapter integration 覆盖

| 文件 | 已有行为门禁 |
| --- | --- |
| `repository_integration_test.go` | FTS-only/hybrid build、ready/activate/replay、embedding conflict、degraded、vector batch rollback、并发 replacement/rollback activation、workspace/immutable DB constraints、activation receipt；并断言关键索引。 |
| `snapshot_integration_test.go` | first/incremental/batch/remove/tombstone/legacy active、processing contract change、capacity rollback、invalid target、DB constraint。 |
| `search_integration_test.go` | active-only/filter/provenance、legacy FTS、cosine/IP/L2 固定 operator、exact EXPLAIN。 |
| `evidence_integration_test.go` | bound/derived refs、latest security、cross-workspace fail closed、损坏 metadata、500 citations 输入顺序与批量读取。 |
| `vector_build_integration_test.go` | embedding cache commit/replay、conflict rollback、commit response-loss 不重复 provider。 |
| `delivery_runtime_integration_test.go` | claim/lease/fence/checkpoint replay、expired reclaim、old owner、response-loss、ingestion binding、frozen checkpoint、failure class/terminal replay。 |
| `dispatcher_integration_test.go` | first/replay/response-loss、insert failure rollback、并发唯一 Delivery/Job、workspace blocking、retry generation、FIFO/SKIP LOCKED、poison/unbound、duplicate receipt rollback。 |
| `completion_integration_test.go` | Activation/Completion 原子与 replay、prerequisites、hybrid/version mismatch、full/latest fence、DB lease、lock-wait/final mutation recheck、每个 mutation stage fault rollback。 |
| `processor_context_integration_test.go` | strict-ready、stale/committed、mapping drift、expired lease。 |
| `regression_integration_test.go` | replay、structural mismatch、v2 complete/degraded、versioned failure。 |
| `model_settings_provenance_integration_test.go` | persisted embedding model-settings revision provenance/constraint。 |
| `source_refresh_lock_integration_test.go` | 同 Workspace session lock 阻塞至 release。 |

#### HTTP、EXPLAIN 与容量

- `internal/retrieval/http/handler_postgres_integration_test.go:41` 的 `TestPostgresRouterSearchEvidenceCursorAndExplain` 覆盖真实 Router、Search/Evidence/Cursor、workspace isolation 与生产 SQL EXPLAIN。
- Repository 的 `assertExplainUsesIndex` 在 `repository_integration_test.go:697-727` 强制 `enable_seqscan=off` 并断言命名索引；Search integration 在 `search_integration_test.go:22-90,301-365,512-532` 同时锁定 lexical plan 和 exact vector operator，且 exact plan 不得出现 HNSW/IVFFlat。
- `capacity_benchmark_integration_test.go:180-181` 的 `TestRetrievalCapacityBenchmark` 是 500k Chunk、HNSW/IVFFlat ANN recall、P95 与 `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, SETTINGS)` 门禁（EXPLAIN 在 `:691`、`:1098`）。它需要外部 DSN 与 artifact dir，适合继续作为显式外部模式，不应默认塞进每次短时容器测试。

#### River/跨模块 fault smoke

唯一公开入口是 `internal/changecontrol/application/approval_dispatch_river_smoke_integration_test.go:48` 的 `TestApprovalDispatchRealRiverSafeWritebackSmoke`；它在 `:298-299` 调用 `runReindexRiverFaultSmoke`。helper 位于 `reindex_river_fault_smoke_integration_test.go:50`，构造 legacy Retrieval Repository/Delivery/Dispatcher/River Worker，并注入 checkpoint、vector、ready、completion response-loss，最终断言唯一 Activation/Active/Completion、River retry 和公开 Search/Evidence（关键构造 `:96,137,164,681`，终态断言 `:570-586`）。它是 GORM Final 前不可缺少的端到端原子/恢复门禁，不是一个独立 `TestReindex...` 可执行入口。

### 10. TODO 9 可执行验证矩阵

#### Stage A：TODO 9 前可执行的静态/非真库门禁

| Gate | 命令 | 必须结果 |
| --- | --- | --- |
| Retrieval unit | `go test -count=1 -timeout=60s ./internal/retrieval/...` | 非 integration 包通过；不能把 integration skip 算成真库绿色。 |
| Race | `go test -race -count=1 -timeout=60s ./internal/retrieval/domain ./internal/retrieval/application ./internal/retrieval/contract ./internal/retrieval/adapter/river ./internal/retrieval/runtime` | 无 race；若超时按包拆分。 |
| Integration compile | `go test -tags=integration -run '^$' ./internal/retrieval/... ./internal/changecontrol/application ./internal/agent/adapter/workflow ./cmd/api ./cmd/worker` | staged/legacy 构造与所有受影响 Composition 测试可编译。 |
| Vet/module | `go vet ./internal/retrieval/...`；`go mod tidy -diff` | 无新增静态问题或 module drift。 |
| Schema guard | `rg -n 'AutoMigrate|\.Migrator\(' internal/retrieval cmd` | 无生产/测试 schema mutation。 |
| Allowlist guard | `rg -n 'github.com/jackc/pgx|\*pgxpool\.|pgx\.Tx|CopyFrom' internal/retrieval/adapter/postgres --glob '*.go'` | 仅批准的 pool registration、COPY/temp/session lock、legacy staged 文件；Final 后残留与父 allowlist一致。 |
| Diff hygiene | `git diff --check` | 由主 agent 执行；research agent 不运行 git。 |

#### Stage B：TODO 9 factory 到位后的短时真实 PostgreSQL parity

现有 integration 应在原文件内参数化 `legacy`/`gorm`，每个实现使用独立、正式迁移后的数据库；同一个子测试内 GORM root、UoW、native helper、Model Settings fence、River producer/consumer 全部来自同一 `platformpostgres.Pool`。核心命令：

```bash
go test -tags=integration -count=1 -timeout=60s ./internal/retrieval/adapter/postgres
go test -tags=integration -run '^TestPostgresRouterSearchEvidenceCursorAndExplain$' -count=1 -timeout=60s ./internal/retrieval/http
```

必须逐项证明：

| 轴 | 真实断言 |
| --- | --- |
| Repository parity | GORM/legacy 对相同 migration schema 的读写、错误 kind/code、no-row、SQLSTATE constraint、idempotent replay 等价。 |
| COPY/Snapshot/session | 500k 上限不等于默认生成 500k；短 fixture 覆盖 COPY rollback、temp table 同 session、RepeatableRead、锁 release/hijack/cancel、第二连接竞争。 |
| pgvector/Search | 三种固定 operator、维度/版本 binding、active-only、workspace isolation、lexical threshold、exact EXPLAIN 与命名索引不退化。 |
| Delivery/Dispatcher | DB-time lease、old-owner fence、SKIP LOCKED/FIFO/fairness、duplicate receipt、insert failure/commit response-loss、同 workspace 并发。 |
| GORM scope | nil/异构/失活 scope fail closed；同池 GORM write + River job 同 commit/rollback；另池 active scope 当前无法由 Foundation 识别，测试应记录为 Composition 约束而不是伪造“已拒绝”。 |
| River interoperability | `riverdatabasesql` producer 提交的 job 能被同 Pool/schema/queue 的 `riverpgxv5` Worker 消费；rollback 时 Worker 永不可见；cancel/timeout/close/pool starvation 不泄漏连接。 |
| Completion | Retrieval Activation/Delivery 与 Change Control Execution/Proposal 同提交、逐 stage failure 全回滚、response-loss exact replay、lock wait 后重检 lease/fence。 |
| Observability/security | 日志无密码、完整 DSN、credential/source/body；错误保留稳定 kind/code，不打印 GORM SQL 参数中的敏感值。 |

#### Stage C：跨模块 fault smoke

```bash
go test -tags=integration \
  -run '^TestApprovalDispatchRealRiverSafeWritebackSmoke$' \
  -count=1 -timeout=180s ./internal/changecontrol/application
```

该用例自身使用 120 秒 context（`approval_dispatch_river_smoke_integration_test.go:55`），所以命令 timeout 应高于 120 秒。GORM parity 应让此入口在完整 GORM staged Composition 下再跑一轮，覆盖 Change Control -> Workflow Outbox -> Retrieval Dispatcher -> database/sql River insert -> pgx Worker -> Completion -> HTTP Search/Evidence；不得只替换 Search Repository 或只断言 readiness。

#### Stage D：显式外部 DSN 的容量/EXPLAIN 门禁

```bash
ZHIXU_TEST_DATABASE_URL='<redacted external DSN>' \
ZHIXU_RETRIEVAL_BENCHMARK_ARTIFACT_DIR='<artifact dir>' \
go test -tags=integration \
  -run '^TestRetrievalCapacityBenchmark$' \
  -count=1 -timeout=30m ./internal/retrieval/adapter/postgres
```

此门禁比较 legacy/GORM 的 500k Hybrid、HNSW/IVFFlat recall、P50/P95、plan/index/settings/buffer 证据。factory 必须保证外部 DSN 模式与容器模式互斥，测试/日志不得回显完整 DSN。30 分钟只是有界执行建议，实际预算由 TODO 9/CI 基线确认后锁定，不能把未运行结果写成通过。

#### Final 生产切线前判定

只有 Stage A-D 均有实际命令证据，且 Foundation、Workflow、Model Settings scoped fence、Change Control owner boundary、Organizing scoped verifier 已落地后，Final 才能：

1. 把 API 三处 Search、Worker 三处 Search + Repository/Delivery/Dispatcher、Organizing transaction consumer 一次性切到批准的 GORM 构造；
2. 保留一个 `riverpgxv5` runtime Client，使用同 Pool/Schema/Queue 的 database/sql scoped producer；
3. 删除 legacy constructor/call sites 或把批准 native path 收进静态 allowlist；
4. 不引入运行时 selector、双写、双读或 silent fallback。

### 11. Files found

- `docs/roadmap.md`：TODO 9 的唯一正式目标、factory 边界与失败语义。
- `.trellis/tasks/08-18-gorm-data-access-migration/prd.md`：TODO 9/Final Composition/River/真实 PostgreSQL 统一门禁。
- `.trellis/tasks/08-19-gorm-retrieval-migration/prd.md`：Retrieval 范围、native/River 要求和 TODO 9 阻断。
- `.trellis/tasks/08-19-gorm-{platform-transaction-foundation,workflow-migration,changecontrol-migration,modelsettings-migration,organizing-migration}/`：依赖 task 的 staged/scoped 设计与状态。
- `.trellis/spec/backend/{database-guidelines,quality-guidelines}.md`：数据库、集成、EXPLAIN、fault smoke 和审查规范。
- `internal/platform/postgres/{pool,transaction,river}.go`：共享 Pool、GORM UoW/scope、River database/sql driver。
- `internal/workflow/adapter/river/{client,gorm_inserter}.go`：pgx runtime 与 scoped SQL producer。
- `internal/workflow/adapter/postgres/gorm_core.go`：同 Pool staged Workflow constructor 参考模式。
- `internal/modelsettings/adapter/postgres/gorm_runtime.go`：scoped enqueue fence。
- `internal/retrieval/adapter/postgres/{repository,search,snapshot,source_refresh_lock,delivery_runtime,dispatcher,completion,processor_context}.go`：全部 Retrieval PostgreSQL surface、native boundary 与跨 schema transaction。
- `internal/retrieval/adapter/river/{args,inserter,worker}.go`：稳定 River payload、legacy inserter 与 pgx worker。
- `internal/retrieval/application/{service,search,evidence_reference,delivery_runtime,processor,completion,dispatcher}.go`：生产注入接口和 legacy `any` transaction gap。
- `cmd/api/main.go`、`cmd/worker/{main,lifecycle}.go`、`internal/workflow/runtime/readiness.go`：生产 Composition、worker lifecycle/readiness。
- `internal/organizing/adapter/owner/transaction_fence.go`：Organizing transaction 内 Retrieval Search concrete 构造。
- `internal/changecontrol/application/{writeback_service,approval_dispatch_river_smoke_integration_test,reindex_river_fault_smoke_integration_test}.go`：Outbox producer 与真实 River fault smoke。
- `internal/retrieval/adapter/postgres/*_integration_test.go`、`internal/retrieval/http/handler_postgres_integration_test.go`：现有 DB/EXPLAIN/capacity parity 资产与重复 fixture。

### 12. External references / versions

本研究不依赖二手网页；版本与 API 均从项目锁定依赖及 vendored upstream source 核对：

- Go `1.25.4`（`go.mod:3`）。
- pgx/v5 `v5.10.0`、pgvector-go `v0.4.0`（`go.mod:14,18-19`）。
- River core、`riverdatabasesql`、`riverpgxv5` 均为 `v0.40.0`（`go.mod:22-25`）。
- GORM `v1.31.2`、GORM PostgreSQL driver `v1.6.2`（`go.mod:42-43`）。
- Testcontainers-Go：未在 `go.mod`/`go.sum`/`vendor/modules.txt` 找到，版本和容器 image/tag 尚未由 TODO 9 锁定，本文不猜测。

### 13. Related specs

- `.trellis/spec/backend/database-guidelines.md:27-36,65-79`：参数化、批量、事务/Outbox、SKIP LOCKED、pgvector、Testcontainers/EXPLAIN。
- `.trellis/spec/backend/quality-guidelines.md:6-14,37-42,67-77`：只有实际命令可证明门禁，真实 PostgreSQL HTTP/River fault/Compose smoke 必须重复。
- `.trellis/spec/guides/cross-layer-thinking-guide.md`：跨 Application/Adapter/Composition 数据流必须端到端验证。
- `.trellis/spec/guides/code-reuse-thinking-guide.md`：复用平台 Pool/UoW/Workflow typed inserter，不再自建连接池或 River transaction adapter。

## Caveats / Not Found

1. 未发现 TODO 9 task/factory、Testcontainers dependency、容器 image/tag 或 CI 入口；Docker daemon 可用不等于 TODO 9 已完成。
2. 本次未运行 Go tests/integration/benchmark：研究 agent 只读业务代码，且当前 DSN/artifact env 未设置；integration skip 不能作为通过证据。
3. 未发现 Retrieval GORM sibling 文件；当前四个 PostgreSQL surface 全部仍是 pgx。
4. 未发现 Retrieval `NewStore` constructor；只有 `application.Store` interface 和 `NewRepository` concrete constructor。
5. Foundation scope 没有 Pool identity，无法在运行时拒绝另一 Pool 的活跃 scope；同池只能由 constructor/Composition/TODO 9 fixture 保证。
6. Retrieval scoped Dispatcher port 与 Change Control Completion scoped owner port 尚未存在；这是 staged River/Completion 实现前需要父任务确认的真实设计缺口。
7. 当前 capacity benchmark 的实际耗时和资源预算未在本环境执行验证；`30m` 是验证矩阵中的有界建议，不是已确认 CI SLA。
