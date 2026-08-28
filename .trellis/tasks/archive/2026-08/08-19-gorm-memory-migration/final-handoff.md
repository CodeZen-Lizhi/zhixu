# Memory Final Composition Handoff

## Staged Constructor

Final must derive both `Pool.GORM()` and `Pool.UnitOfWork()` from the same
`*platformpostgres.Pool`, then construct `NewGORMRepository(root, unitOfWork)`.
The adapter must not receive an independently opened root, pool, or transaction.

## Current Boundary

Production API and Worker composition still use legacy `NewRepository(pool)`.
This child did not change `cmd/**`, schema, migrations, cross-module wiring,
or the legacy implementation.

## Final Work And Rollback

- Switch all Memory production constructors together after the remaining
  module and TODO 3 gates are complete.
- Preserve the existing command lock order, CAS, append-only audit/receipt
  writes, and shared Unit of Work transaction ownership.
- Remove legacy pgx construction only in Final after its cross-owner
  PostgreSQL regressions pass. Before then, rollback is a composition-only
  selection of the legacy constructor; do not roll back schema or durable data.
