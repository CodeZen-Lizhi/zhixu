# Local Model Runtime 测试与接线

## Production wiring to keep legacy

- `cmd/local-model-runtime/main.go` constructs the legacy store from `database.DB()`.
- `internal/modelsettings/runtime/bootstrap.go` injects the legacy `TxLifecycle` into the legacy Model Settings adapter.
- `cmd/local-model-runtime-credential-init` remains the privileged pgx/admin role and credential-file boundary owned by Final.

## TODO 9 fixture

Each implementation subtest must use an isolated migrated database. After migration, close the raw migration pool and open a complete `platformpostgres.Pool`; use `Pool.DB()` for legacy and `Pool.GORM()`/`Pool.UnitOfWork()` for GORM. Never mix an outer uncommitted pgx fixture transaction with a GORM implementation.

Required real-PostgreSQL cases: manager stale takeover and future lease, ReadDemand snapshot consistency, test probe claim/abandon/expiry/hold release, operation idempotency/CAS/budget/recovery, runtime trigger errors, scoped activation commit/rollback visibility, role/function privileges, cancellation cause, rows/connection release and EXPLAIN. `ZHIXU_TEST_DATABASE_URL` is currently unavailable, so these remain TODO 9.
