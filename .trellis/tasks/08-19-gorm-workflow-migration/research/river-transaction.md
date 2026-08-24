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

## 阻断

Model Settings 当前 enqueue fence 只接受 `pgx.Tx`。GORM Runtime 在 scoped fence 未交付前不能保持 rollout drain，因此不得接生产、不得使用 no-op。
