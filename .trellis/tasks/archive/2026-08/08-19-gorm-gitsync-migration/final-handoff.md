# Git Sync GORM Child Handoff (2026-08-27)

## Completed

- Existing PostgreSQL integration scenarios run against both legacy pgx and staged GORM repositories.
- Both paths use the shared Testcontainers factory; no child-owned container or database lifecycle remains.
- Real PostgreSQL checks passed for configuration replay/fencing, active-run concurrency, trigger constraints,
  automatic candidates, event redaction, secret clearing, lease reclaim, Poison/result-unknown, Attempt/Run CAS,
  Outbox and independent index follow-up.
- `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)` checks passed for Run keyset, automatic candidate and Outbox claim indexes.

## Final child responsibilities

Production Composition replacement, legacy pgx removal, `cmd/**` changes, Atlas ownership and full cross-module gates
remain deferred to the TODO 10 Final child. This child changes no migration, dependency or production entry point.

## Verification

See `research/todo9-postgres-evidence-2026-08-27.md` for commands and results.
