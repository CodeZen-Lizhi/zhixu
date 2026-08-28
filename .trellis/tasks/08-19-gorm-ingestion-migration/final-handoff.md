# Ingestion Final Composition Handoff

## Staged Constructor

Final 从既有 `*platformpostgres.Pool` 取得共享 GORM root，调用
`postgres.NewGORMRepository(gormRoot)`。不得引入裸 DSN、独立 GORM root、
第二连接池、runtime selector、shadow read、双写或 fallback。

## Current Boundary

生产 Composition（`cmd/api` / `cmd/worker`）仍构造 legacy
`postgres.NewRepository(...)`。本 child 只通过共享 Testcontainers fixture
验证 staged GORM Repository，不改生产选择、migration 或 legacy 构造。

## 实库证据（2026-08-28）

- 既有九个成对 legacy/GORM 场景在同一共享 Pool 上通过：五方法字段/UTC/
  nullable/JSON/Created/排序/no-row/错误码对照、GetProjection 三种
  Strategy/Schema 组合、并发 CreateAttempt/TransitionAttempt CAS/
  SaveProjection 幂等、批次失败回滚、immutable trigger。
- 新增 `TestGORMRepositorySaveProjectionStatementBounds`：只计数 GORM
  logger 实测 5,000 Spans + 5,000 Chunks，新建上界
  Cnew=4+ceil(spans/500)+ceil(chunks/500)=24，复用 Creuse=5。
- 新增 `TestGORMRepositoryCancellationAndDeadlockClassification`：caller
  cancel/deadline 保留 sentinel 与自定义 cause（修复了 gormClassify 丢失
  WithCancelCause cause 的缺陷）、`sql.ErrTxDone` 还原、真实 40P01
  deadlock 分类 retryable、连接释放。
- 新增 `TestGORMRepositoryBlockedQueryCancellation`：排他表锁制造确定性
  阻塞，取消后快速返回、连接归还并可复用。
- 全量 `-race -tags "integration testcontainers"` 套件通过（224s）。

## 验证盲区

commit-time connection loss/response loss 无法在模块内稳定注入，继承
Foundation 的验证盲区；未为测试新增生产 transaction hook。40001
serialization 与 40P01 共用 `gormClassify` 的 retryable 分支，40P01 已由
真实死锁覆盖，40001 分支未单独实测。

## Final Work

- 在 Final composition child 中把 `cmd/api`、`cmd/worker` 的 Ingestion
  Repository 构造切换为共享 Pool 派生的 GORM 实现。
- 28 个模块 child 与 TODO 3 全部通过 Final 门禁后，删除 legacy pgx
  实现与直接 pgx 依赖。
- 保留现有 integration fixture 作为生产路径回归套件。
