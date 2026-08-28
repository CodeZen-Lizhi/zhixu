# Retrieval Repository 迁移到 GORM

## Goal

在不改变 Retrieval 业务语义、性能基线和生产接线的前提下，为 Retrieval 建立未接生产
Composition 的 GORM sibling。普通查询和事务迁入共享 GORM/UoW；只有协议级 COPY、临时表和
会话级锁保留可审计的原生 pgx 边界，为 TODO 9 真实 PostgreSQL parity 与 Final 统一切线做好准备。

## Background

- Retrieval 当前同时拥有 Index/Embedding、Snapshot、Vector、Search/Evidence、Delivery、Dispatcher、
  Processor Context、Completion 与 Regression PostgreSQL 路径。
- `platformpostgres.Pool` 已从一个物理 pgx pool 派生 database/sql facade、GORM root 和
  `foundation.UnitOfWork`；Retrieval 不需要也不得再建第二个连接池。
- pgvector-go 已实现 `sql.Scanner`/`driver.Valuer`。固定向量操作符、CTE、数组、JSONB、DB time、
  行锁和 transaction advisory lock 都可通过 GORM Raw/Exec 保留，不构成 pgx 例外。
- Snapshot、Source Refresh lease 和 manifest COPY 依赖 pgx 协议或物理 session identity，不能挂接到
  GORM 的 `*sql.Tx`，必须保留窄 native capability。
- 当前生产 API/Worker/Organizing 均使用 legacy pgx constructor；真实 PostgreSQL 仍依赖外部
  `ZHIXU_TEST_DATABASE_URL`，TODO 9 Testcontainers factory 尚未落地。

## Requirements

### R1. Scope And Compatibility

- 本 child 只拥有 `internal/retrieval/**` 及本任务规划/验证工件；不修改 `cmd/**`、migration、
  Workflow/Change Control/Organizing concrete Adapter 或生产 selector。
- 新增 staged sibling，保留 legacy Repository、Search、Delivery、Dispatcher、River inserter 和所有
  生产构造。TODO 9 前不得双写、双读、fallback、删除 pgx 基线或切换 Composition。
- Domain/Application 不导入 GORM、database/sql 或 pgx；legacy `transaction any` 保留兼容，但新路径必须
  使用 `foundation.TransactionScope` 的 additive scoped contract。

### R2. Single Pool And Transaction Ownership

- 所有 staged constructor 接收完整 `*platformpostgres.Pool`，从同一实例取得 GORM root、UoW、
  database/sql transaction 与受审计 native capability；禁止 `gorm.Open`、第二 pool 和独立 DSN 重连。
- Store-owned 多语句事务只由 `foundation.UnitOfWork.Within` 提交/回滚；caller-owned scoped 方法只解包
  live scope，不自行 Begin/Commit/Rollback，也不 fallback 到 root。
- 保持现有 isolation、read-only、锁序、DB clock、CAS、idempotency、response-loss 和 deferred-trigger
  closure；成功 callback 后 commit error 必须与事务内错误分开分类。

### R3. Audited Native pgx Allowlist

- `ManifestCopier/BeginIndex`：metadata insert、manifest `CopyFrom` 与 commit 保持一个窄 pgx transaction。
- `SnapshotBuilder`：固定一条 `*pgxpool.Conn`，保持 session advisory lock、RepeatableRead、临时 stage、
  source/chunk COPY、commit、unlock；unlock 失败必须 hijack-close。
- `SourceRefreshLease`：固定连接上的 try-lock/retry/release，跨业务事务持锁；取消和 unlock 失败仍释放或
  淘汰物理连接。
- 普通查询、事务锁、pgvector、JSONB/array、River producer 不得进入 native allowlist。

### R4. Ordinary GORM Paths

- Index/Embedding、lexical/vector build、activation/rollback、Search/Evidence、Delivery、Processor Context、
  Regression 与 Retrieval-owned Completion 状态使用参数化 GORM Raw/Exec。
- 保持批量/集合 SQL、上限、稳定排序、workspace predicate、`LIMIT+1`、ordinality 和资源 Close/Err；
  禁止逐行 ORM/N+1、Association/Preload/Save、隐式时间、soft delete 和 AutoMigrate/Migrator。
- JSONB 使用校验后返回 string 的 Valuer；数组使用单一 `pq.Array` binding；pgvector 使用锁定依赖的
  Scanner/Valuer；距离操作符只允许现有内部枚举三分支。
- Persistence record 位于 Adapter，显式映射 schema/columns/null/time/version；scan 后复用 Domain
  validation，损坏数据 fail closed。

### R5. Scoped Cross-Module Contracts

- Retrieval Application 新增 parallel scoped Dispatcher/Job contract，使用 opaque scope；legacy
  `JobInserter(any)` 不改。公平 first/retry 调度规则保持单一事实源。
- Dispatcher 依赖 consumer-owned Workflow outbox claim/publish 与 Change Control binding verifier；
  Completion 依赖 consumer-owned Change Control lock/complete capability。接口可在 Retrieval 定义，
  concrete 实现必须回各 owner task，Retrieval child 不跨模块落实现。
- 上述 concrete capability 当前不存在；本 child 先交付接口和可独立编译的 consumer边界，Dispatcher/
  Completion完整GORM实现明确阻塞到owner task交付，禁止用跨schema mutation或空壳假装完成。
- Outer UoW 始终由 Retrieval 拥有；owner collaborator 只在同 scope 内锁、校验或 CAS，不得嵌套事务。
- `ProcessorContext` 保留为 Retrieval-owned read-only integration projection：只执行现有固定跨 schema
  SELECT，不新增 mutation；真实 binding closure 继续由 migration/trigger 与 TODO 9 验证。
- Search sibling 提供 caller-owned scoped SourceVersion batch read，供后续 Organizing owner task替换
  `pgx.Tx` concrete 依赖；本 child 不修改 Organizing。

### R6. River Boundary

- 事务内 producer 复用 Workflow 已有 official `riverdatabasesql` scoped typed inserter和 Model Settings
  scoped enqueue fence；业务事实、outbox publish 与 River job 必须同 scope commit/rollback。
- Worker/listener/migrator 继续使用 `riverpgxv5`，不属于普通 Retrieval Repository 迁移。
- Final pgx allowlist分为Retrieval Repository三项native capability与平台River runtime例外；两者分别审计，
  不能用River例外放宽普通Repository。

### R7. Error, Context And Security

- nil context fail closed；每条 Raw/Exec 使用 caller/callback context。
- 保留 `context.Canceled`、`context.DeadlineExceeded` 和 distinct `context.Cause`；cancel 分类优先于 deadline。
- no-row、`sql.ErrTxDone`、现有 SQLSTATE/constraint/retryability 与 legacy 错误码等价，并保留可
  `errors.As` 的 `*pgconn.PgError` cause。
- 日志和错误不得泄露 DSN、SQL 参数、Source/Chunk 内容、向量、JSON payload、credential 或原始数据库消息。

### R8. Verification And Completion Gate

- 静态阶段运行 Retrieval unit/race/vet、integration compile、受影响 Composition compile、module/diff
  checks，以及独立 Go Review、SQL Review、Trellis Check；结果写入 `research/static-validation.md`。
- TODO 9 到位后，在现有 integration 文件原位参数化 legacy/GORM，使用独立迁移数据库和同一个
  platform Pool，验证真 PostgreSQL/pgvector/COPY/session lock/River/Completion/EXPLAIN。
- 500k Chunk、384 维、HNSW/IVFFlat、30 samples、P95 <= 2s、recall >= 0.95 继续走显式外部 DSN
  capacity gate；小容器 fixture 不得替代容量证据。
- TODO 9、owner scoped concrete 实现和跨模块 fault smoke 未完成时，PRD AC 保持未勾选、task保持
  `in_progress`，不得归档或宣称生产等价。

## Acceptance Criteria

- [ ] AC1：普通 Retrieval Repository/Search/Evidence/Delivery/Regression 路径有 GORM sibling，保持现有
  SQL、事务、错误、批量和 Domain validation，并且生产仍使用 legacy pgx。
- [ ] AC2：COPY、Snapshot temp/session lock、Source Refresh session lease 被收敛为 3 个有 owner、接口、
  原因、测试和退出条件的 native pgx capability；其他新 GORM 文件无 pgx 数据访问，`pgconn`仅用于
  SQLSTATE错误分类。
- [ ] AC3：Dispatcher 使用 opaque scoped contract 与 official database/sql River producer；Completion、
  Workflow outbox 与 Change Control 通过稳定 scoped Port参与同一 Retrieval UoW，不保留新 `any` seam。
- [ ] AC4：真实 PostgreSQL legacy/GORM parity覆盖 pgvector、COPY、锁、SQLSTATE、context、回滚、
  response-loss、River pgx worker interoperability和连接释放。
- [ ] AC5：实际 GORM Raw SQL通过现有 EXPLAIN/index与500k容量门禁，无N+1和性能退化。
- [ ] AC6：Go Review、SQL Review、Trellis Check、局部 test/race/vet/compile、module与
  `git diff --check`有可复核记录，且未修改生产Composition/migration。

## Out Of Scope

- TODO 9 Testcontainers-Go factory、依赖版本和容器 image/tag。
- `cmd/**`、生产 Composition、运行时 selector、legacy 删除与全仓 pgx 收口；这些归 Final child。
- Workflow/Change Control/Organizing scoped concrete Adapter 实现；由对应模块任务拥有。
- 生产 HNSW/IVFFlat DDL或在线物理索引切换；当前 Repository 的“索引切换”是业务 IndexVersion状态机。
- 修改已发布 migration、trigger、权限、River worker/listener/migrator。

## Dependencies

- `08-19-gorm-platform-transaction-foundation`
- `08-19-gorm-workflow-migration`：scoped typed River producer与未来Workflow outbox concrete Port。
- `08-19-gorm-changecontrol-migration`：未来Reindex binding/completion concrete Port。
- `08-19-gorm-modelsettings-migration`：scoped enqueue fence。
- TODO 9：真实 PostgreSQL/pgvector自动 fixture和Final parity gate。
