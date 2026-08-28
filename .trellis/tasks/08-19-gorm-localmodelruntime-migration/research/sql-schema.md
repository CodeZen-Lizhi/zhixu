# Local Model Runtime SQL/Schema 研究

## Schema and permissions

`migrations/00080_managed_ollama_runtime.sql` owns runtime/hold/operation tables, the revision requirements view, triggers and Down guards. The runtime role has read-only table/view grants and EXECUTE on ten fixed `SECURITY DEFINER` command functions. A GORM adapter must invoke those functions with parameterized `SELECT` statements; direct table writes would violate the role boundary.

## Transaction facts

- Manager claim/heartbeat, demand publication, active recovery, operation claim/attempt/progress/terminal, expiry sweep, hold changes and runtime CAS are single function calls whose trigger and DB-time semantics must remain database-owned.
- Test preparation and probe methods are multi-statement owned transactions. Probe completion releases the hold in the same transaction; `ReadDemand` is `REPEATABLE READ READ ONLY` across runtime, settings state, holds, operations and revision requirements.
- Scoped activation preparation is caller-owned: insert/replay operation, lock/read operation, create/verify hold, and complete/fail operation must use the caller transaction and never commit or rollback.

## SQL review requirements

Use GORM Raw/Exec with `?` placeholders and explicit `::uuid`, `::interval`, `::jsonb` casts. Use guarded `Row()`/`Rows()` and check `Rows.Err`/`Close`; recognize `sql.ErrNoRows` as well as legacy `pgx.ErrNoRows`. Preserve `clock_timestamp()`, CAS predicates, idempotency keys, trigger SQLSTATE and `*pgconn.PgError` causes. No dynamic identifiers, AutoMigrate, ORM callbacks, or second pool.
