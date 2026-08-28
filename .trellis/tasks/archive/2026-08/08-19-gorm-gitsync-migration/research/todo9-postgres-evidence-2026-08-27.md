# Git Sync TODO 9 PostgreSQL Evidence (2026-08-27)

## Fixture and paths

- Each existing integration test uses one `testdb.Require(t, testdb.Config{MaxConns: 8})` fixture.
- The legacy path is constructed from `fixture.Pool().DB()`.
- The GORM path is constructed from the same `platformpostgres.Pool` via `GORM()` and `UnitOfWork()`.
- Legacy and GORM subtests run against that same pool/container; path-specific fixture IDs prevent cross-path
  uniqueness collisions, and legacy outbox rows are finalized before the GORM subtest.
- The factory owns migration, container lifecycle and cleanup; the test does not create or drop databases.

## Real PostgreSQL result

Command:

```text
go test -mod=vendor -tags=integration -race -count=1 -p 1 ./internal/gitsync/adapter/postgres -timeout 12m
```

Result: PASS (`62.215s`). Seven existing tests passed for both `legacy` and `gorm` subtests.

Covered gates include config exact replay/keep binding, active-run race, PostgreSQL trigger and changed-file checks,
automatic candidate completion/replay, server-event redaction, clear-secret fencing, lease expiry/reclaim, Poison and
result-unknown convergence, Run/Attempt CAS transitions, Outbox lease/publish, independent index failure/retry and
follow-up completion.

The same fixture file asserts `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)` for:

- `idx_git_sync_run_workspace_created` (stable Run keyset ordering);
- `idx_writeback_execution_auto_sync_candidates` (bounded automatic candidate LATERAL query);
- `idx_git_sync_outbox_pending` (pending `SKIP LOCKED` claim).

## Static checks

- `go vet -mod=vendor ./internal/gitsync/adapter/postgres`: PASS.
- `go test -mod=vendor -count=1 ./internal/gitsync/...`: PASS.
- `go test -mod=vendor -run '^$' -tags=integration ./internal/gitsync/adapter/postgres`: PASS.
- `python3 .trellis/scripts/task.py validate .trellis/tasks/archive/2026-08/08-19-gorm-gitsync-migration`: PASS (size warnings only).
- `git diff --check`: PASS.

Production Composition, `cmd/**`, migrations, dependencies and the legacy Repository remain unchanged; those changes
belong to the Final child.
