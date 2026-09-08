# Research: Retrieval Go API、事务接线与 pgx 边界

- Query: 盘点 `internal/retrieval` 的 Go API、Application/Domain ports、PostgreSQL adapter 类型与方法、跨模块依赖、生产/测试构造点及 pgx 泄漏；判断 staged GORM 与 native allowlist，并核对 Workflow/ChangeControl 已有 scoped ports。
- Scope: internal
- Date: 2026-08-21

## Findings

### 结论摘要

1. Retrieval 的 Domain 与绝大多数 Application port 已经是驱动无关的；唯一明确穿透事务实现的公开应用端口是 `application.JobInserter.InsertTx(context.Context, any, ...)`。PostgreSQL adapter 自身则广泛以 `pgx.Row`、`pgx.Rows`、`pgx.Tx` 和 `pgxpool.Conn` 为接口边界。
2. 真正必须保留 native pgx 的生产路径只有三类：Workspace Snapshot 的 pinned connection + session advisory lock + temp table + `CopyFrom`，Source Refresh 跨事务生命周期的 session advisory lock，以及 `BeginIndex` 的 `CopyFrom` 批量 manifest 写入。pgvector 距离运算、CTE、`FOR UPDATE`、事务级 advisory lock、`set_config(..., true)`、数组/JSONB 都是 PostgreSQL-specific，但不要求 pgx，可通过 GORM `Raw`/`Exec` 和 `foundation.UnitOfWork` 保留。
3. River worker/listener 继续走 `riverpgxv5`；事务内生产者可以复用已经存在的 `workflow/adapter/river.ScopedTypedJobInserter`，它从 `foundation.TransactionScope` 解析 `*sql.Tx` 并使用 River 官方 `database/sql` adapter。Retrieval 现有 `any -> pgx.Tx` 端口不应进入 GORM 新路径。
4. `CompleteReindexTx` 与 `DispatcherStore` 不是单纯的 ORM 改写：它们在同一事务中直接锁/改 Workflow 与 ChangeControl 表。现有 scoped ports 不覆盖“发布 Workflow outbox”或“完成 Reindex writeback/proposal”语义。若遵守模块所有权规范，必须先由对应 owner 增加 scoped port；否则只能把这些跨 owner SQL 明确列为临时 compatibility allowlist，不能伪装成已完成边界迁移。
5. 当前任务是 `planning`，只有 `prd.md`，没有 `design.md`/`implement.md`。在设计冻结 native allowlist、owner scoped ports、锁顺序和 legacy/GORM 双轨构造前，不具备直接进入实现的 Trellis 前置条件。

### 任务与依赖事实

- `.trellis/tasks/08-19-gorm-retrieval-migration/task.json:1`：任务状态为 `planning`，优先级 P0，父任务为 `08-18-gorm-data-access-migration`。
- `.trellis/tasks/08-19-gorm-retrieval-migration/prd.md:1`：范围限定为 `internal/retrieval/adapter/postgres`，要求保留 pgvector、COPY、临时表、会话锁、索引切换、批处理和 River 语义；生产 Composition 切换在 TODO 9 之前禁止。
- PRD 明确依赖 Platform transaction foundation、Workflow GORM 和 ChangeControl GORM；当前仓库已有这些基础，但没有覆盖 Retrieval 完成/派发所需的全部 owner scoped capability。

### Files found

#### Retrieval PostgreSQL adapter 生产文件

- `internal/retrieval/adapter/postgres/repository.go`：核心 Repository、embedding/index/projection/activation/getter 与 `BeginIndex` COPY。
- `internal/retrieval/adapter/postgres/activation_tx.go`：同事务激活、回滚、replay 逻辑。
- `internal/retrieval/adapter/postgres/snapshot.go`：Workspace snapshot，包含 pinned connection、session lock、temp table、分页与 COPY。
- `internal/retrieval/adapter/postgres/source_refresh_lock.go`：跨多个业务事务持有的 session advisory lock lease。
- `internal/retrieval/adapter/postgres/vector_build.go`：有界向量页加载与批次提交。
- `internal/retrieval/adapter/postgres/search.go`：Active index、lexical/vector candidate 查询与 pgvector 距离操作符白名单。
- `internal/retrieval/adapter/postgres/evidence.go`：Source Version/Span、Citation 与 Provenance 单条/批量绑定查询。
- `internal/retrieval/adapter/postgres/regression.go`：Snapshot 结构回归检查。
- `internal/retrieval/adapter/postgres/delivery_runtime.go`：Reindex delivery claim/lease/checkpoint/fail 状态机。
- `internal/retrieval/adapter/postgres/delivery_scans.go`：Delivery/Attempt row scanner。
- `internal/retrieval/adapter/postgres/processor_context.go`：Delivery、Workflow outbox、ChangeControl commit/writeback 与 Ingestion checkpoint 绑定加载。
- `internal/retrieval/adapter/postgres/dispatcher.go`：首次/重试派发、River 入队、Workflow outbox 发布与 ChangeControl binding 校验。
- `internal/retrieval/adapter/postgres/completion.go`：Reindex 完成事务、索引激活、delivery 完成及 ChangeControl writeback/proposal 更新。
- `internal/retrieval/adapter/postgres/scans.go`：公共 index/embedding/manifest scanner 与 JSON 编解码。
- `internal/retrieval/adapter/postgres/errors.go`：pgx/pgconn 错误分类。

#### 关键平台及相邻模块文件

- `internal/platform/postgres/pool.go:26`：一个 Pool 同时拥有 `*pgxpool.Pool`、`*sql.DB`、`*gorm.DB`；`GORM()` 在 `:157` 暴露 GORM root。
- `internal/platform/postgres/transaction.go:26`：GORM `UnitOfWork`；`GORMTransaction` 在 `:70`、`SQLTransaction` 在 `:79` 从 opaque scope 解析当前事务。
- `internal/foundation/transaction.go:23`：`TransactionScope` 与 `UnitOfWork` 的驱动无关契约。
- `internal/workflow/adapter/river/gorm_inserter.go:14`：scoped River producer 及 enqueue fence；`InsertTx` 在 `:74` 使用 caller-owned scope。
- `internal/workflow/adapter/postgres/gorm_core.go:57`：Workflow GORM runtime 从同一 Platform Pool 构造 scoped River producer。
- `internal/workflow/application/scoped_runtime.go:13`：Workflow runtime/cancellation/terminal/control scoped ports。
- `internal/changecontrol/adapter/postgres/gorm_scoped_knowledge_proposal.go:39`：ChangeControl 已有 scoped knowledge proposal create/read。
- `internal/changecontrol/adapter/postgres/gorm_writeback.go:26`：ChangeControl 已有 scoped cancellation safety read。
- `internal/modelsettings/adapter/postgres/gorm_runtime.go:308`：ModelSettings 已实现 River scoped enqueue fence。
- `internal/organizing/adapter/owner/transaction_fence.go:29`：Organizing 仍把 opaque `any` 断言为 `pgx.Tx`，并在 `:111` 直接构造 Retrieval SearchRepository。

### Application 与 Domain ports

Retrieval Domain/Contract 只暴露 `foundation.ID`、时间、JSON、枚举和业务值；未发现 `pgx`、`gorm`、`database/sql` 类型。业务数据契约包括 embedding/index/manifest/projection/activation/snapshot/search/evidence/delivery/completion/regression/vector build，以及 `internal/retrieval/contract/reindex.go` 的稳定 Reindex payload/binding。

Application 驱动端口如下：

| Port | 方法/职责 | 驱动泄漏判断 |
| --- | --- | --- |
| `application.Store` | Register/BeginIndex/BeginWorkspaceSnapshot/BuildLexical/SaveVectorBatch/Transition/Activate/Rollback/GetEmbedding/GetIndex/GetByIdempotency/GetBuildStatus/GetActive | 驱动无关；但一个大接口混合了普通 GORM 候选与 native snapshot/COPY/session-lock 能力，见 `application/service.go:18-33` |
| `application.SearchStore` | LoadActiveSearchIndex/SearchLexical/SearchVector | 驱动无关，见 `application/search.go:20-28` |
| `EvidenceReferenceStore` | LoadSourceVersionReference/LoadSourceSpanReference | 驱动无关，见 `application/evidence_reference.go:21-27` |
| `SourceVersionReferenceBatchStore` | LoadSourceVersionReferences | 驱动无关，见 `application/evidence_reference.go:29-33` |
| `CitationEvidenceStore` | LoadCitationSourceSpanReferences | 驱动无关，见 `application/evidence_reference.go:35-39` |
| `ProvenanceCitationStore` | ResolveProvenanceCitationReferences | 驱动无关，见 `application/evidence_reference.go:41-44` |
| `VectorBuildStore` | LoadVectorBuildPage/CommitVectorBuildBatch | 驱动无关，见 `application/vector_builder.go:16-20` |
| `RegressionStore` | RunSnapshotRegression | 驱动无关，见 `application/regression.go:13-16` |
| `CompletionStore` | CompleteReindexTx | 驱动无关，但名字承诺 store 自己拥有完整事务，见 `application/completion.go:18-21` |
| `DeliveryRuntimePort` | Claim/Heartbeat/Checkpoint/Fail | 驱动无关，见 `application/delivery_runtime.go:32-42` |
| `ProcessorContextLoader` | LoadProcessorContext | 驱动无关；实现跨 Workflow/CC/Core/Ingestion schema 读取，见 `application/processor.go:73-76` |
| `ProcessorCapture/Ingestion/Retrieval/Vector/RegressionPort` | Processor 对相邻 owner 和 Retrieval use case 的最小接口 | 驱动无关，见 `application/processor.go:78-104` |
| `DispatcherStore` | DispatchOne | 本身驱动无关，见 `application/dispatcher.go:43-46` |
| `JobInserter` | `InsertTx(context.Context, any, ReindexJob)` | **事务驱动泄漏**：`any` 实际要求 `pgx.Tx`，见 `application/dispatcher.go:38-41` |
| `SourceRefreshRetrieval/Lease/Locker` | Snapshot/Build/Ready/Activate/GetActive 与跨事务 lease | 驱动无关；Lease 的语义要求 pinned DB session，见 `application/source_refresher.go:23-40` |

因此，新路径不需要重写 Domain。Application 层最小必要契约变化是为 River 增加 `foundation.TransactionScope` 版本，或者将 scoped inserter 保持为 PostgreSQL adapter 的私有 consumer interface；legacy `JobInserter(any)` 在最终生产切换前仍需保留。

### PostgreSQL adapter 类型与导出方法清单

#### Core Repository

- `DB`：`QueryRow(...)->pgx.Row`、`Begin(...)->pgx.Tx`，见 `repository.go:17-20`。
- `Repository`：持有 `DB`，构造器 `NewRepository`，见 `repository.go:23-33`。
- 方法：`RegisterEmbeddingVersion` (`repository.go:36`)、`GetEmbeddingVersion` (`:99`)、`BeginIndex` (`:111`)、`BuildLexical` (`:182`)、`SaveVectorBatch` (`:235`)、`TransitionIndex` (`:413`)、`Activate` (`:449`)、`RollbackActivate` (`:454`)、`GetIndex` (`:485`)、`GetIndexByIdempotencyKey` (`:497`)、`GetActive` (`:509`)、`GetBuildStatus` (`:521`)。
- 分文件方法：`BeginWorkspaceSnapshot` (`snapshot.go:23`)、`AcquireSourceRefresh` (`source_refresh_lock.go:33`)、`LoadVectorBuildPage` (`vector_build.go:18`)、`CommitVectorBuildBatch` (`:154`)、`RunSnapshotRegression` (`regression.go:18`)、`CompleteReindexTx` (`completion.go:42`)。
- 内部事务类型/函数：`activationLockedIndexes` 与 `activateTx` (`activation_tx.go:11-18`)；completion identity/proposal/execution carriers (`completion.go:18-38`)；`snapshotConnectionPool` (`snapshot.go:18`) 与 `sourceStageRow` (`:396`)；`sourceRefreshConnectionPool`/`sourceRefreshLease` (`source_refresh_lock.go:21-30`)。

#### Search/Evidence Repository

- `SearchDB`：`QueryRow`/`Query`/`Begin` 返回 pgx types，见 `search.go:26-30`。
- `SearchRepository`、`NewSearchRepository`，见 `search.go:33-45`。
- 搜索方法：`LoadActiveSearchIndex` (`search.go:48`)、`SearchLexical` (`:83`)、`SearchVector` (`:119`)。
- Evidence 方法：`LoadSourceVersionReference` (`evidence.go:23`)、`LoadSourceVersionReferences` (`:90`)、`LoadSourceSpanReference` (`:185`)、`LoadCitationSourceSpanReference` (`:302`，兼容单条 helper)、`LoadCitationSourceSpanReferences` (`:317`)、`ResolveProvenanceCitationReferences` (`:371`)。
- 内部 carriers：`searchCandidateScan` (`search.go:253`)、`searchProvenanceJSON` (`:351`)、`storedCitationReference` (`evidence.go:432`)。

#### Delivery/Processor Repository

- `DeliveryRepository`、`NewDeliveryRepository`，见 `delivery_runtime.go:16-29`。
- 方法：`Claim` (`delivery_runtime.go:32`)、`Heartbeat` (`:141`)、`Checkpoint` (`:168`)、`Fail` (`:231`)、`LoadProcessorContext` (`processor_context.go:22`)。
- 内部函数类型 `deliveryMutation` 显式接收 `pgx.Tx`，见 `delivery_runtime.go:262`；`committedReplayMatcher` 在 `:263`。
- Delivery/Attempt scanners 位于 `delivery_scans.go:21` 与 `:49`，当前参数是 pgx row interface。

#### Dispatcher

- `DispatcherDB`：只暴露 `Begin(...)->pgx.Tx`，见 `dispatcher.go:26-28`。
- `ReindexRiverInserter`：`InsertTx(context.Context, any, reindexriver.Args, river.InsertOptions)`，见 `dispatcher.go:31-33`。
- `DispatcherStore`、`NewDispatcher`、`NewDispatcherWithConsumer`，见 `dispatcher.go:36-55`。
- `riverJobInserter.InsertTx` 把 Application `any` 继续传给 River adapter，见 `dispatcher.go:59-68`。
- `DispatchOne` 是公开 store 方法，见 `dispatcher.go:71`；`dispatchFirst` (`:85`) 与 `dispatchRetry` (`:147`) 拥有 pgx transaction lifecycle。
- 内部 carriers：`selectedOutbox` (`dispatcher.go:189`) 与 `writebackBinding` (`:232`)。

#### 公共 scanner/error helper

- `errors.go:11-29` 只识别 `pgx.ErrNoRows` 与 `*pgconn.PgError`。
- `scans.go:18-24` 的 index/embedding scanner 直接接收 `pgx.Row`；delivery/evidence 也有同类 pgx scanner。
- `marshalDegradedCapabilities` 返回原始 `[]byte` JSONB (`scans.go:190-195`)；切 database/sql 时必须使用显式 `driver.Valuer`，避免 `[]byte` 被当作 `bytea`。

### pgx 泄漏与兼容保留点

#### Application/public 构造边界

- `application.JobInserter` 的 `any` 是最大泄漏：Retrieval River adapter 与 Workflow legacy River adapter 最终都要求 `pgx.Tx`；Workflow legacy assertion 位于 `internal/workflow/adapter/river/inserter.go:115-132`。
- `NewRepository(DB)`、`NewSearchRepository(SearchDB)`、`NewDeliveryRepository(DB, ...)`、`NewDispatcher(DispatcherDB, ...)` 都公开 pgx-shaped interface。即使参数类型不是具体 `*pgxpool.Pool`，调用者也必须实现 pgx row/tx 语义。
- `internal/organizing/adapter/owner/transaction_fence.go:29-39` 将外部 transaction `any` 断言为 `pgx.Tx`；随后在 `:111-135` 以这个 tx 构造 Retrieval SearchRepository 并读批量 Evidence。该调用链意味着 legacy Search constructor 不能在 Retrieval TODO 9 前删除。

#### 私有 helper

- transaction callbacks、mutation function、scanners 广泛使用 `pgx.Tx`/`pgx.Row`/`pgx.Rows`。新 GORM 文件不应复制一个完整 pgx-shaped facade；scanner 可收敛为仅含 `Scan(...any) error` 的私有接口，使 legacy 与 GORM 共用值映射。
- 新错误分类需同时覆盖 `sql.ErrNoRows`、`gorm.ErrRecordNotFound`、`sql.ErrTxDone`、context cancel/deadline/custom cause，同时继续从 `*pgconn.PgError` 读取 SQLSTATE。

### 必须保留的 native pgx allowlist

| Native path | 必须保留原因 | 精确证据 | 建议隔离 |
| --- | --- | --- | --- |
| Workspace Snapshot | `pg_advisory_lock` 是 session lock，后续 temp table 和 COPY 必须固定在同一物理连接；同时要求 repeatable-read snapshot | acquire/lock/RR 在 `snapshot.go:23-44`；cleanup/unlock 在 `:137-151`；temp tables 在 `:156-169`；source stage COPY 在 `:424-434`；manifest source COPY 在 `:579-629`；manifest chunk COPY 在 `:631-669` | `native_snapshot.go`，只接收受控 `*pgxpool.Pool`/connection provider |
| Source Refresh lease | lease 跨多个独立业务事务生命周期，必须一直占用同一 connection；Release 还处理被取消 ctx 与 `Hijack` | pool/lease 在 `source_refresh_lock.go:21-30`；acquire 在 `:32-64`；release/unlock/hijack 在 `:77-113` | `native_source_refresh_lock.go` |
| BeginIndex manifest COPY | 单事务创建 index 后通过 `CopyFrom` 写 manifest；GORM 无对应 bulk protocol | transaction 在 `repository.go:123-178`，COPY 在 `:157-173` | `native_manifest_copy.go`，由 staged composite Store 委托 |
| River worker/listener | 这是 River runtime 接线，不是 ORM Repository；PRD 要求继续 pgx | 现有 worker 使用 legacy River pgx runtime；GORM 只替换事务内 producer | 保持现有 `riverpgxv5` worker/listener，不计入 Retrieval Repository 的 ordinary query allowlist |

以上 allowlist 不包括下列 PostgreSQL 特性，因为它们可安全保留为 GORM Raw SQL：

- `pg_advisory_xact_lock`：生命周期受 caller-owned transaction 控制。
- `FOR UPDATE`、CTE、`unnest`、JSONB、数组、DB clock/CAS。
- `set_config('pg_trgm.similarity_threshold', ..., true)`：`true` 是 transaction-local，可在 GORM UoW 中保持，见 `search.go:83-115`。
- pgvector `<=>`、`<#>`、`<->`：`search.go:235-250` 只从内部 enum 选择固定片段，向量参数可走 `driver.Valuer`。
- 业务“index switching”：实际是 index_version/activation 状态锁与 CAS，不是动态 DDL，见 `repository.go:413-483` 与 `activation_tx.go:18`。

`vendor/github.com/pgvector/pgvector-go/vector.go:93` 实现 `sql.Scanner`，`:108` 实现 `driver.Valuer`，因此 pgvector 本身不是保留 pgx 的理由。

### 可 staged GORM 的路径

#### 第一批：低跨模块风险

- Embedding/Index 普通 CRUD 与 getter：Register/Get Embedding、Get Index/Get By Key/Get Active/Get Build Status。
- Index transition/activation/rollback：保留显式 `FOR UPDATE`、version CAS、DB time 和 replay 规则，用 UoW 包住完整事务。
- BuildLexical、SaveVectorBatch、Load/Commit VectorBuild：用固定 Raw SQL 和显式 array/vector/JSON carrier，保留批次上限和幂等 CAS。
- Search：`LoadActiveSearchIndex`、Lexical、Vector。向量距离符号继续使用已有固定白名单，不接收外部 identifier。
- Evidence：单条与批量 Source Version/Span/Citation/Provenance；现有 SQL 使用 `unnest WITH ORDINALITY` 和 JSON aggregation，可以通过 database/sql rows 扫描保留顺序与有界性。
- Regression：repeatable-read/UoW + fixed Raw SQL。

#### 第二批：状态机但 owner 仍在 Retrieval

- Delivery Claim/Heartbeat/Checkpoint/Fail：使用 GORM UoW，保留 DB-time、`FOR UPDATE`、lease generation/fence、response-loss replay。
- ProcessorContext：技术上可直接 GORM Raw，但它跨 Workflow/ChangeControl/Core/Ingestion 读取，应先明确这些 read joins 是允许的 consumer projection，还是要走 owner scoped/read port。

#### 第三批：必须先解决 scoped owner seam

- Dispatcher：GORM UoW + `ScopedTypedJobInserter[reindexriver.Args]` 可替换 pgx job insertion；但 Workflow outbox claim/publish 和 ChangeControl binding 校验仍直接 SQL。
- Completion：Retrieval 可以拥有外层 GORM UoW和自身表变更，但 ChangeControl writeback/proposal 锁与更新必须委托给同 scope 的 owner capability；Workflow/CC/Core/Ingestion binding validation 也需明确 owner contract。
- Native Snapshot/COPY/session lock：保留 pgx 子模块，由一个 composite Repository 实现现有 monolithic `application.Store`，不能要求普通 GORM root 假装支持 session/COPY。

### 跨模块 SQL 与 scoped port 缺口

| Retrieval path | 当前跨 owner 行为 | 当前已有 scoped port 是否覆盖 | 结论 |
| --- | --- | --- | --- |
| `dispatcher.go` | first path 锁/读 Workflow outbox，插 River job，再更新 `workflow.outbox_event.published_at` (`:85-145`)；读取 CC writeback/proposal/commit binding (`:239-268`) | Workflow 有 `ScopedRuntimeStarter`、runtime hooks 和 scoped River producer，但没有“claim/publish reindex outbox” port | 若所有权规范是硬约束，需要 Workflow 新增 scoped outbox dispatch port；否则列为临时跨 owner SQL allowlist |
| `processor_context.go` | 单快照读取 Workflow outbox、CC writeback/commit、Core source version、Ingestion attempt (`:22-208`，核心 join 在 `:125-128`) | 没有覆盖此 Reindex binding projection 的 scoped/read port | 设计需明确它是 Retrieval-owned read model，还是拆成 owner verifier；不能在实现中临时猜测 |
| `completion.go` | 同事务锁 proposal/execution，激活 Retrieval index，完成 delivery，直接更新 CC execution/proposal (`:54-159`)；再 join Workflow/CC/Core/Ingestion 校验事实 (`:259-321`) | CC 现有 scoped proposal create/read 与 cancellation guard 不覆盖 Reindex completion | **P0 缺口**：新增 CC scoped completion/fence capability，至少封装 lock/validate/update/replay；必要时再加 Workflow binding verifier |

当前可复用 scoped ports：

- Workflow `ScopedRuntimeStarter` 及 cancellation/terminal/control hooks：`internal/workflow/application/scoped_runtime.go:13-37`；实现 `StartScoped` 在 `internal/workflow/adapter/postgres/gorm_runtime_start.go:69`。
- Workflow `ScopedWorkspaceAnalysisExecutionFence`：`internal/workflow/application/scoped_workspace_analysis_execution_fence.go:52-57`，语义仅覆盖 Workspace Analysis execution，不应错误复用到 Reindex。
- Workflow River `ScopedJobInserter`/`ScopedEnqueueFence`：`internal/workflow/adapter/river/gorm_inserter.go:14-29`；generic constructor 在 `:41-70`，insert 在 `:74-108`。
- ChangeControl `CreateKnowledgeChangeProposalScoped`/`GetInitialKnowledgeChangeProposalScoped`：`internal/changecontrol/adapter/postgres/gorm_scoped_knowledge_proposal.go:39-153`，语义不是完成 Reindex writeback。
- ChangeControl `SafeToCancelWorkflowNodeScoped`：`internal/changecontrol/adapter/postgres/gorm_writeback.go:26-55`，只做 cancellation safety read。
- ModelSettings scoped enqueue fence：`internal/modelsettings/adapter/postgres/gorm_runtime.go:308-335`；可直接作为 Retrieval scoped River producer 的 fence。

建议 completion 业务请求/结果由 ChangeControl application 作为 owner 定义并由其 GORM Repository 实现；Retrieval adapter 只依赖覆盖这些方法的最小 consumer interface。outer UoW 仍由 Retrieval `CompleteReindexTx` 拥有。契约必须显式冻结锁顺序、current/replay/stale 结果和版本号，禁止 scoped 实现自行 begin/commit/rollback。若任务范围严格禁止修改 ChangeControl，则该 capability 必须先由依赖任务交付，Retrieval 任务只能停在 legacy/GORM 双轨未切生产状态。

### 生产构造点

当前生产 Composition 全部传 `*pgxpool.Pool`，未发现 Retrieval GORM 构造：

- Worker source-processing：`cmd/worker/main.go:3327` 的 `newSourceProcessingComponents`；Repository/Service/Regression 在 `:3360-3369`，VectorBuilder/恢复在 `:3373` 与 `:3391-3405`，SourceRefresher 在 `:3406-3421`。
- Worker Reindex runtime：`cmd/worker/main.go:3494` 的 `newReindexComponents`；Delivery 在 `:3505-3511`，Processor 在 `:3513-3519`，Completion 在 `:3520-3523`，legacy River inserter 在 `:3543-3545`，Dispatcher 在 `:3547-3555`。
- API Retrieval Search/HTTP：`cmd/api/main.go:945-973`（Organizing handler）、`:1468-1499`（Retrieval handler），SearchRepository 构造点在 `:960`、`:1478`、`:1578`。
- Worker 其他 SearchRepository 构造点：`cmd/worker/main.go:2292`、`:2780`、`:3113`。
- Organizing 事务内构造：`internal/organizing/adapter/owner/transaction_fence.go:111-135`。

PRD 要求 TODO 9 之前不修改生产 Composition；因此 staged 构造器必须与 legacy 并存，先由既有 integration tests 直接构造。

### Staged 构造契约（建议冻结到 design）

建议所有新构造器只接收共享 `*platformpostgres.Pool`，由 adapter 内部一次取得 GORM root、UoW、database/sql transaction resolver 和受审计的 native pgx pool；不要让 composition 同时传 `*gorm.DB`、`*pgxpool.Pool` 与 `foundation.UnitOfWork`，否则无法证明它们来自同一物理池：

```go
NewGORMRepository(pool *platformpostgres.Pool) (*GORMRepository, error)
NewGORMSearchRepository(pool *platformpostgres.Pool) (*GORMSearchRepository, error)
NewGORMDeliveryRepository(pool *platformpostgres.Pool, ids foundation.IDGenerator) (*GORMDeliveryRepository, error)
NewGORMDispatcherStore(pool *platformpostgres.Pool, ids foundation.IDGenerator, outbox ScopedReindexOutbox) (*GORMDispatcherStore, error)
NewGORMCompletionRepository(pool *platformpostgres.Pool, completion ScopedReindexCompletion) (*GORMCompletionRepository, error)
```

- `GORMRepository` 内部组合 ordinary GORM implementation 与 private native snapshot/COPY/lease collaborators，并通过 forwarding 继续满足现有 `application.Store`；native collaborator 只能由同一个 Pool 的 `DB()` 构造。
- `GORMSearchRepository` 同时实现普通 `SearchStore`/Evidence ports 与 caller-owned scope read；普通调用使用 root/自有 UoW，scoped 方法只调用 `platformpostgres.GORMTransaction(scope)`。
- Dispatcher 不宜由一个构造器同时隐藏 Store、River client 和 application Dispatcher：分别构造 `GORMDispatcherStore`、`ScopedTypedJobInserter[reindexriver.Args]`，最后交给 `application.NewDispatcher`。typed inserter 必须接收现有 schema-scoped `*workflowriver.Client` 和 ModelSettings `ScopedEnqueueFence`，以继承相同 queue/schema 配置。
- Completion repository 拥有唯一 outer UoW；`ScopedReindexCompletion` 是 caller-owned collaborator，不能自行 begin/commit/rollback。
- 上述签名是建议设计，不是现存 API；生产 composition 保持 legacy，直到 parity 与 TODO 9 gate 完成。

### Organizing 所需 scoped Retrieval read

精确调用不是泛化的 Citation read，而是 `LoadSourceVersionReferences`：Organizing 在 caller-owned confirmation transaction 中先锁 Core/Ingestion/Retrieval facts，然后在 `internal/organizing/adapter/owner/transaction_fence.go:111-135` 用同一个 `pgx.Tx` 构造 SearchRepository，按 `MaxSourceVersionBatchSize` 批量加载 Source Version references 并校验 content hash/availability。

Retrieval staged implementation至少需要提供下面的 scope-preserving 方法；接口可以由 Organizing consumer 本地定义，GORM SearchRepository 以结构化方法满足，避免 Retrieval 反向依赖 Organizing：

```go
LoadSourceVersionReferencesScoped(
    context.Context,
    foundation.TransactionScope,
    foundation.ID,
    []foundation.ID,
) ([]retrievaldomain.SourceVersionReference, error)
```

该方法只解析 live scope、执行现有有界 batch query、保留请求顺序，不拥有 transaction lifecycle。若严格执行 table owner 边界，还应由 Retrieval 提供 `LockActiveSourceManifestsScoped` 或更深的 frozen-source verifier，因为 Organizing 当前在 `transaction_fence.go:152-170` 直接锁 `retrieval.index_version/index_manifest_source`。Core/Ingestion 的 `FOR SHARE` 同样属于其各自 owner，需由 Organizing 迁移任务统一处理。

仅在 Retrieval 增加 scoped read 仍不能切换生产：Organizing 的 `FrozenMaterialFence.VerifyFrozen` 当前接收 `any` 并在 `transaction_fence.go:32-40` 断言 `pgx.Tx`。Organizing 必须先改为 `foundation.TransactionScope`/Platform UoW caller；在此之前保留 legacy `NewSearchRepository(tx)` 是必要兼容路径。

### ChangeControl completion owner boundary（建议冻结到 design）

最小 owner-clean 契约不能只是一个 `MarkCompletedScoped`：现有锁顺序是 Workspace transaction lock -> CC proposal -> CC execution -> Retrieval delivery/attempt/index (`completion.go:54-84`)，并依赖 locked CC facts做 first/replay validation。因此建议由 ChangeControl application 暴露两阶段、同 scope 的 capability：

1. `LockReindexCompletionScoped`：按固定顺序锁 proposal 与 writeback execution，返回 workspace/workflow/node/revision/approval/target/result/git/status/cleanup/version 等不可变 completion facts，并区分 current/replay/conflict。
2. `CompleteReindexScoped`：在同一 scope 内用上一步返回的 expected versions 将 execution/proposal 从 `verifying` CAS 到 `completed`，校验各一行受影响；不得提交或回滚。

Retrieval outer UoW 的顺序应固定为：取得 Workspace xact lock -> 调 CC lock capability -> 锁 Retrieval delivery/attempt/index -> 校验 fence/lease/binding -> 激活 index 并完成 Retrieval delivery -> 调 CC complete capability -> 由 outer UoW commit。这样保留 `completion.go:59-160` 的锁顺序、原子性和 replay 语义，同时不再直接 mutate `change_control.*`。

`validateFirstCompletion` 的 Workflow/CC/Core/Ingestion join (`completion.go:259-321`) 仍需 design 决定：短期可作为明确的 read-only compatibility projection；完整 owner-clean 方案则还要 Workflow/CC 提供 scoped binding verifier。无论选择哪条，直接把 `completion.go:142-159` 的 CC UPDATE 改写成 GORM Raw 都不满足 owner boundary。

### 测试构造点与可复用覆盖

无需新增测试文件，可在以下既有测试中增加 legacy/GORM variant，并保持每个 variant 独立数据库：

- Core Repository：`internal/retrieval/adapter/postgres/repository_integration_test.go:576`。
- Snapshot/native COPY：`snapshot_integration_test.go`、`source_refresh_lock_integration_test.go`。
- Vector build：`vector_build_integration_test.go`。
- Search：`search_integration_test.go:24,286,315`；connection-local 配置用例在 `:106`。
- Evidence：`evidence_integration_test.go:23,67,95,119,190,205`，unit fake 在 `evidence_test.go`。
- Regression：`regression_integration_test.go`。
- Delivery/Processor：`delivery_runtime_integration_test.go`、`processor_context_integration_test.go`。
- Dispatcher：`dispatcher_integration_test.go`。
- Completion/response-loss：`completion_integration_test.go`；legacy fault wrappers 在 `:26` 与 `:209-251`，GORM variant 应改为在 `foundation.UnitOfWork`/commit boundary 注入 response loss，而非伪造 pgx DB。
- HTTP 全路径：`internal/retrieval/http/handler_postgres_integration_test.go:43-51`。
- 容量 benchmark：`capacity_benchmark_integration_test.go:416`；动态 HNSW/IVFFlat DDL 仅在 integration benchmark `:505` 之后出现，不是生产 Repository 迁移内容。

### 建议文件拆分

为使 native allowlist 可审计、每个模块可独立开发/验收，建议新增文件而不改写 legacy 文件：

1. `gorm_core.go`：Pool/UoW、scope -> GORM/SQL transaction resolver、ready/context/error stage、窄 `Scan` interface、SQLSTATE/no-row/context 分类、JSON/array/vector carrier。禁止自行提交 caller-owned scope。
2. `gorm_repository.go`：Embedding、Index getter/transition/activation、BuildLexical 等普通 Repository 方法。
3. `gorm_vector.go`：SaveVectorBatch、LoadVectorBuildPage、CommitVectorBuildBatch。
4. `gorm_search.go`：Active index、lexical/vector search、transaction-local pg_trgm config、固定 pgvector operator。
5. `gorm_evidence.go`：Source Version/Span/Citation/Provenance 单条与批量查询。
6. `gorm_delivery.go`：Claim/Heartbeat/Checkpoint/Fail 状态机。
7. `gorm_processor_context.go`：Reindex binding projection；实现前先冻结跨 owner read policy。
8. `gorm_regression.go`：repeatable-read regression。
9. `gorm_dispatcher.go`：caller-owned UoW、scoped River producer、first/retry replay；依赖新的 Workflow scoped outbox capability或明确 compatibility allowlist。
10. `gorm_completion.go`：Retrieval outer UoW、自有 delivery/index 变化、CC/Workflow scoped collaborators；禁止直接以 GORM Raw 重写 owner 表 mutation。
11. `native_snapshot.go`：唯一承载 pinned connection、temp table、snapshot COPY 的 native adapter。
12. `native_source_refresh_lock.go`：唯一承载 session advisory lock lease。
13. `native_manifest_copy.go`：`BeginIndex` manifest `CopyFrom`。

现有 `application.Store` 是 monolithic port，建议 staged `GORMRepository` 组合 ordinary GORM subrepository 与上述 native collaborators 后继续实现完整 Store；不要为兼容它发明 `any` 或双驱动万能 DB interface。Search、Delivery、Dispatcher、Completion 保持独立构造器，减少单个 Repository 的事务责任。

### 建议开发顺序/任务拆分

每一项可作为“一个模块一个任务”的独立实现与验收单元：

1. **基础层**：`gorm_core.go` + scanner/error/value carriers；只做驱动适配和 unit/integration harness。
2. **Search/Evidence**：只读 Raw SQL、pgvector 与 batch ordering，风险最低且能验证 database/sql 参数 carrier。
3. **Core Index/Embedding**：普通 CRUD、transition/activation、lexical。
4. **Vector/Regression**：有界 batch、RR/CAS 与 response-loss。
5. **Delivery/Processor Context**：delivery state machine；同时决定跨 owner read projection policy。
6. **Native allowlist 提取**：Snapshot、Source Refresh lock、BeginIndex COPY；只移动驱动职责，保持旧行为。
7. **Dispatcher**：先补 Workflow scoped outbox port，再接 scoped River producer。
8. **Completion**：先补 ChangeControl scoped completion port/锁顺序，再改 outer UoW。
9. **双实现 parity 与生产 wiring**：所有既有 integration suites 跑 legacy/GORM variant；最后一个任务才改 `cmd/api`/`cmd/worker` Composition。

其中 1-6 可以在未改生产 wiring 时 staged；7-8 被 scoped owner capability 阻塞；9 必须满足 PRD TODO 9 gate。

### Code patterns

- 推荐沿用 Platform UoW：`internal/platform/postgres/transaction.go:26-68` 创建 transaction，`GORMTransaction`/`SQLTransaction` 仅解析当前 live scope (`:70-87`)。
- 推荐沿用 Workflow GORM construction：同一 Pool 创建 repository 与 scoped River producer，见 `internal/workflow/adapter/postgres/gorm_core.go:57-82`。
- 推荐沿用 generic River typed inserter：验证 typed args、检查 enqueue fence、通过 official database/sql client 插入，见 `internal/workflow/adapter/river/gorm_inserter.go:41-108`。
- 推荐沿用 caller-owned scoped method 约束：`StartScoped` 只从 scope 取得 transaction，不提交/回滚，见 `internal/workflow/adapter/postgres/gorm_runtime_start.go:68-95`。
- 避免照搬 legacy `any -> pgx.Tx`：`internal/retrieval/application/dispatcher.go:38-41` 与 `internal/workflow/adapter/river/inserter.go:115-132` 是待淘汰兼容路径，不是 GORM 模板。

### External references

本次结论只依赖仓库内锁定的一手源码与 vendor 实现，没有使用外部二手资料。相关锁定版本：

- `go.mod`：Go 1.25.4、`pgx/v5` 5.10.0、`pgvector-go` 0.4.0、River/`riverdatabasesql` 0.40.0、GORM 1.31.2、GORM PostgreSQL driver 1.6.2。
- `vendor/github.com/pgvector/pgvector-go/vector.go:93-115`：官方 vendored Vector 的 `sql.Scanner`/`driver.Valuer` 实现。
- `internal/workflow/adapter/river/gorm_inserter.go:41-108`：仓库内已经验证的 River official database/sql scoped producer 用法。

### Related specs

- `.trellis/workflow.md`：planning/design/implementation/verification phase 约束；当前 task 仍在 planning。
- `.trellis/spec/backend/database-guidelines.md`：transaction owner、scoped collaborator、response-loss、锁顺序、driver-native allowlist 要求。
- `.trellis/spec/backend/directory-structure.md`：模块边界与 owner/consumer 依赖方向。
- `.trellis/spec/backend/error-handling.md`：稳定 error kind/code、context cause 与 SQLSTATE 分类。
- `.trellis/spec/backend/logging-guidelines.md`：adapter 不应泄漏敏感 payload/SQL 参数。
- `.trellis/spec/backend/quality-guidelines.md`：integration parity、最小改动和验证要求。
- `.trellis/spec/backend/eino-embedding-adapter.md`：Embedding contract/version 绑定约束。
- `.trellis/spec/guides/code-reuse-thinking-guide.md`：优先复用 Platform UoW 和现有 scoped River producer。
- `.trellis/spec/guides/cross-layer-thinking-guide.md`：跨 owner 数据流与 transaction scope 必须显式。

## Caveats / Not Found

- 当前 task 目录未找到 `design.md` 或 `implement.md`；因此本文给出的是实现前证据和拆分建议，不代表 scoped contract/锁顺序已经获批。
- 未找到可直接完成 Retrieval Reindex 的 ChangeControl scoped port，也未找到 Workflow scoped outbox claim/publish port。现有同名/相近 scoped ports 的业务语义不同，不能强行复用。
- `ProcessorContext`、Search/Evidence/Snapshot 的跨 Core/Ingestion/Workflow/CC read joins 是否属于允许的 consumer projection，现有 task 文档未明确；设计需要列出 read-only ownership allowlist。
- Organizing 仍依赖 transaction-scoped legacy SearchRepository。只迁 Retrieval adapter 而不提供兼容 constructor/scoped Retrieval read port，会造成跨模块编译或事务语义回归。
- 本研究未运行测试、构建或 benchmark，因为任务是只读 API/wiring 盘点；所有行为判断来自源码、项目 spec、PRD 与 vendored dependency 实现。
