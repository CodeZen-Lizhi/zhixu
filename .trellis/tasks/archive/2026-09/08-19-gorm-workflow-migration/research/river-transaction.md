# Workflow River 事务边界

## 可复用实现

`internal/workflow/adapter/river/gorm_inserter.go` 已提供 `ScopedJobInserter`。它从 live `foundation.TransactionScope` 解出同一个 `*sql.Tx`，通过官方 `riverdatabasesql` InsertTx。GORM Runtime 的公开 factory 只接平台 Pool、River Options 和 scoped fence；它从 `pool.DB()` 内建 insert-only Client，再从同一 Pool 构造此窄接口，不允许调用方注入 Client/producer。

## 必须保留的 pgx 能力

- legacy `JobInserter(any)` 实际只接受 `pgx.Tx`，仍被现有 Runtime 与跨模块代码使用；
- River client、worker、listener、migrator 继续使用 `riverpgxv5`；database/sql driver 不支持 listener；
- GORM scoped producer 仅负责业务事务内插入，不启动 Worker。

## 全部 Workflow 入队点

- Runtime Start；
- retry scheduling；
- successor activation（既有或新 Node）；
- Control Resume 的 node resume。

每个入队必须与对应 Workflow/Attempt/Outbox 状态使用同一 scope。禁止 root DB 另开事务或调用 legacy inserter。

## 2026-09-08 依赖状态

Model Settings GORM Repository 已实现 `CheckEnqueue(ctx, foundation.TransactionScope)`。Workflow
代表性 fixture 从同 Pool 构造 Settings、Audit、GORM Runtime 与 River producer，并实测 StartScoped
持有 singleton `FOR SHARE`、调用方 rollback 后释放；业务 Run/Node/Outbox/Job 同时回滚。
真实 pgx Runtime Worker 已消费 database/sql producer 创建的 Job 并完成节点。Final 继续负责消费
owner 的完整 Hook 接线，不得省略 fence 或换成 no-op。准确命令见 `../final-handoff.md`。
