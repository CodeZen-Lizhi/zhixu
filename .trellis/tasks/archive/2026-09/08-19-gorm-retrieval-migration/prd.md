# Retrieval Repository 迁移到 GORM

> **当前交付状态（2026-09-08）**：`completed`，已归档；最终代码 [cb935655](https://github.com/CodeZen-Lizhi/zhixu/commit/cb93565561498674cda1dc1230fed587fba66075) 已推送至 `origin/dev`，尚未部署。
> TODO 10 全部 30 个子任务已完成，最新实现与验证见 [Final 验收记录](../08-19-gorm-composition-pgx-convergence/research/final-acceptance.md)。下文 staged、待 Final 或未提交的描述保留其原阶段事实，现存 M9 历史升级限制仍有效。

## Goal

在不改变 Retrieval 业务语义、性能基线和生产接线的前提下，为 Retrieval 建立未接生产
Composition 的 GORM sibling。普通查询和事务迁入共享 GORM/UoW；只有协议级 COPY、临时表和
会话级锁保留可审计的原生 pgx 边界。使用 TODO 9 Testcontainers 工厂完成本 child 的真实 PostgreSQL 验证，
将生产统一接线和 legacy 删除交给 Final。

## Background

- Retrieval 当前同时拥有 Index/Embedding、Snapshot、Vector、Search/Evidence、Delivery、Dispatcher、
  Processor Context、Completion 与 Regression PostgreSQL 路径。
- `platformpostgres.Pool` 已从一个物理 pgx pool 派生 database/sql facade、GORM root 和
  `foundation.UnitOfWork`；Retrieval 不需要也不得再建第二个连接池。
- pgvector-go 已实现 `sql.Scanner`/`driver.Valuer`。固定向量操作符、CTE、数组、JSONB、DB time、
  行锁和 transaction advisory lock 都可通过 GORM Raw/Exec 保留，不构成 pgx 例外。
- Snapshot、Source Refresh lease 和 manifest COPY 依赖 pgx 协议或物理 session identity，不能挂接到
  GORM 的 `*sql.Tx`，必须保留窄 native capability。
- 本 child 不修改生产 API/Worker constructor。TODO 9 Testcontainers factory 已可用；现有 Retrieval
  fixture 已改用 `testdb.Require`，默认创建独立 PostgreSQL/pgvector，外部 admin DSN 为可选模式。

## Requirements

### R1. Scope And Compatibility

- 本 child 只拥有 `internal/retrieval/**` 及本任务规划/验证工件；不修改 `cmd/**`、migration、
  Workflow/Change Control/Organizing concrete Adapter 或生产 selector。
- 新增 staged sibling，保留 legacy Repository、Search、Delivery、Dispatcher、River inserter 和所有
  生产构造。TODO 9 交付前不得双写、双读、fallback、删除 pgx 基线或切换 Composition；当前这些生产边界仍由 Final 独占。
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
- owner concrete capability 已交付：Workflow `NewGORMReindexOutbox` 负责 claim/publish，Change Control
  `NewGORMRepository` 提供 binding verifier 和两阶段 Completion。Retrieval Dispatcher/Completion
  使用这些 owner 实现，不复制跨 schema mutation。
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

- 按父任务 2026-09-01 精简政策运行 Retrieval 既有 unit/vet、相关 integration、diff/task 校验，
  并做 Go/SQL/Trellis 自检；实际命令和结果写入 `research/static-validation.md`。
- 在现有 integration 文件原位参数化 legacy/GORM；每个变体独立迁移数据库，GORM/UoW/native、
  Workflow/Change Control/Model Settings/River 来自同一个完整 platform Pool。
- 本 child 直接交付 Dispatcher/Completion，必须验证同事务 River rollback、owner rollback 与锁/lease
  不变量；已补 response-loss 和历史重放作为 Completion 直接改动的专项。
- 500k Chunk、384 维、HNSW/IVFFlat、30 samples、P95 <= 2s、recall >= 0.95 仍为显式容量 gate。
  本次没有运行，小容器不提供容量证据；按精简政策该矩阵不再默认阻断本 child。
- 本 child 达到最低证据后可交接；生产 Composition、legacy 删除和最终任务归档由主会话处理。

## Acceptance Criteria

- [x] AC1：普通 Retrieval Repository/Search/Evidence/Delivery/Regression 路径有 GORM sibling，保持现有
  SQL、事务、错误、批量和 Domain validation，并且生产仍使用 legacy pgx。
- [x] AC2：COPY、Snapshot temp/session lock、Source Refresh session lease 被收敛为 3 个有 owner、接口、
  原因、测试和退出条件的 native pgx capability；其他新 GORM 文件无 pgx 数据访问，`pgconn`仅用于
  SQLSTATE错误分类。
- [x] AC3：Dispatcher 使用 opaque scoped contract 与 official database/sql River producer；Completion、
  Workflow outbox 与 Change Control 通过稳定 scoped Port参与同一 Retrieval UoW，不保留新 `any` seam。
- [x] AC4：核心 Retrieval 主路径在真实 PostgreSQL 上有 legacy/GORM 或冻结基线证据；COPY、锁、River、
  Completion 等专项仅在本 child 直接改动时补一条关键原子性/竞争场景。
- [x] AC5：实际 GORM Raw SQL 无 N+1 和无界读取；EXPLAIN/index 与 500k 容量门禁仅由查询/规模风险触发。
- [x] AC6：Go/SQL/Trellis Review、受影响包既有 test/vet、核心 integration、task 校验和
  `git diff --check` 有可复核记录，且未修改生产 Composition/migration。

## 2026-09-01 测试范围调整

按父任务精简政策，Retrieval 不再默认要求全量 pgvector/COPY/锁/response-loss/容量/连接释放矩阵；直接改动的高风险 native capability 仍必须保留代表性专项。

## Out Of Scope

- TODO 9 Testcontainers-Go factory、依赖版本和容器 image/tag。
- `cmd/**`、生产 Composition、运行时 selector、legacy 删除与全仓 pgx 收口；这些归 Final child。
- Workflow/Change Control/Organizing scoped concrete Adapter 实现；由对应模块任务拥有。
- 生产 HNSW/IVFFlat DDL或在线物理索引切换；当前 Repository 的“索引切换”是业务 IndexVersion状态机。
- 修改已发布 migration、trigger、权限、River worker/listener/migrator。

## Dependencies

- `08-19-gorm-platform-transaction-foundation`
- `08-19-gorm-workflow-migration`：scoped typed River producer 与 Workflow outbox concrete Port。
- `08-19-gorm-changecontrol-migration`：Reindex binding/completion concrete Port。
- `08-19-gorm-modelsettings-migration`：scoped enqueue fence。
- TODO 9：真实 PostgreSQL/pgvector自动 fixture和Final parity gate。

## 2026-09-08 最终收口

本模块子阶段验收完成。生产入口切换、旧 pgx 实现及过渡端口清理由 Final 统一完成，最终构造、核心实库与回滚依赖见 `final-handoff.md` 和 Final 的 `research/final-acceptance.md`。容量/全量故障矩阵等未执行项目不计 PASS；不影响已批准精简政策下的模块开发验收。
