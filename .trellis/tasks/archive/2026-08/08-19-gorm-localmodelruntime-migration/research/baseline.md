# Local Model Runtime 基线

## Public surface

- `LifecycleStore` 聚合 manager、demand、operation、hold、runtime；`TestPreparationStore` 另提供 test operation/probe。
- `TxLifecycle`/`WithTx(pgx.Tx)` 的四个 activation-preparation 方法由 Model Settings PostgreSQL Repository 持有；调用点在 activation seed、complete、lease-expiry recovery、read/verify。
- `cmd/local-model-runtime` 通过 `platformpostgres.Open` 后构造 legacy `NewPostgresStore(database.DB())`；API/Worker 经 Model Settings bootstrap 使用同一 legacy boundary。

## Stage boundary

本 child 不改 Model Settings、cmd、migration、credential-init 或测试。新增 `GORMStore` 和 scoped `WithScope` 供后续 Model Settings child；legacy `pgx.Tx` API 是临时、明确 owner 的 allowlist，直到该 child 完成同一 UoW 切换。

## Existing evidence

`internal/platform/migration/managed_ollama_integration_test.go` 已覆盖 migration constraints/down guard、least-privilege role、pull budget/deadline、active recovery、CAS、hold、runtime phase。现有测试固定构造 legacy store，未执行 GORM path；无真实 PG 环境时只可做编译门禁。
