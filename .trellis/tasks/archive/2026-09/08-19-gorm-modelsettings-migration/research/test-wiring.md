# Model Settings 测试与接线

## Production wiring to keep legacy

- `cmd/api/main.go`, `cmd/worker/main.go`, `cmd/modelctl/main.go` call legacy `runtime.Bootstrap(database.DB(), ...)`.
- API/Worker register the legacy Repository as pgx River enqueue fence.
- Legacy integration cases continue to construct raw pgx pools. The existing integration file now also contains one `testdb`-backed GORM scenario whose Model Settings, Audit, Local Runtime, River and UnitOfWork dependencies come from the same platform Pool.

## TODO 9 fixture（历史完整清单；现行执行按精简政策）

Each legacy/GORM subtest must use an isolated migrated database. After migration, open one complete `platformpostgres.Pool`; legacy uses `Pool.DB()`, while GORM Model Settings, Audit, Local Runtime and UnitOfWork all come from that same Pool. Never combine an uncommitted pgx fixture transaction with GORM.

Required cases: revision/audit rollback and credential safety; activation state/runtime/participant lock order and recovery; Local Runtime operation/hold atomicity; scoped fence+River rollback/commit; DB-time leases; Snapshot consistency; SQLSTATE/no-row/cancel/commit ambiguity; connection release and EXPLAIN. 其中后半部分是历史完整矩阵；当前 `testdb` 已可在 Docker/Testcontainers 下运行主路径，后续只执行父任务政策要求的主路径及直接改动触发的硬风险项。
