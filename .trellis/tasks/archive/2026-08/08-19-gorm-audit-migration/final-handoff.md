# Audit Final Composition Handoff

## Staged Constructor

Final must construct `postgres.NewGORMStore(pool)` from the same
`*platformpostgres.Pool` that provides every caller's `UnitOfWork`. The staged
Audit Store owns no separate DSN, `database/sql` handle, pgx pool, transaction
or container lifecycle.

## Current Boundary

API, Worker and other production composition points still use legacy
`postgres.NewStore` / `NewRepository` and the legacy `AppendTx(any)` Port. This
child does not change `cmd/**`, production selection, migrations, dual-write,
shadow-read or fallback behavior.

## Final Work

- Switch every Audit constructor and caller-owned transaction path together to
  the single-Pool GORM Store and `ScopedAppender` / `ScopedReader` ports.
- Preserve the scoped transaction ownership: `AppendScoped` and `GetScoped`
  join the caller scope and never begin, commit, rollback or fall back to a
  root connection.
- Run the cross-owner Workspace rebind/history and Tools refusal/Audit
  transactions after those owners are switched; they must preserve the same
  `transaction_id` and deferred-FK closure.
- Remove legacy pgx constructors, `AppendTx(any)` and direct pgx dependencies
  only after all production composition and cross-module PostgreSQL gates pass.

## Rollback

Before legacy removal, restore legacy constructors as one Final composition
change. Do not roll back migrations or durable Audit rows, introduce a second
pool, or hide parity failures with dual writes or fallback paths.
