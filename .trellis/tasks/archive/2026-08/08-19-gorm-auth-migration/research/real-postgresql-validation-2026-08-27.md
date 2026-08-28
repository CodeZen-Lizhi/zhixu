# Auth TODO 9 Real PostgreSQL Validation

## Scope

Only `internal/auth/adapter/postgres/repository_integration_test.go` changed. The
existing integration suite now calls `testdb.Require` and receives one migrated
`platformpostgres.Pool` per top-level scenario. Each scenario builds both:

- legacy: `NewRepository(pool)` after `pool := platformPool.DB()`;
- GORM: `NewGORMRepository(platformPool.GORM())`.

The fixture remains the sole owner of Testcontainers provisioning, project
migrations, shared Pool construction, and cleanup. No production Composition,
legacy implementation, migration, Schema, or `cmd/**` file changed.

## Real PostgreSQL Results

Docker/Testcontainers was available through OrbStack. The following all passed
with `-race`; every command showed both `legacy` and `gorm` subtests:

```text
TestServicePostgreSQLCredentialLifecycleUsesDatabaseClockAndNeverPersistsPlaintext
TestServicePostgreSQLAuthenticateRevokeRacesFailClosedAfterCommit
TestServicePostgreSQLConcurrentSessionRotationAllowsOneWinner
TestServicePostgreSQLAPITokenKeysetPaginationUsesUUIDTieBreaker
TestServicePostgreSQLAPITokenExpiredRevokedAndCorruptScopesFailClosed
TestRepositoryPostgreSQLAuthLifecycleAndFailClosedReads
TestRepositoryPostgreSQLSessionRotationIsAtomicAndConcurrentAuthenticationUpdatesLastSeen
```

The gates cover PostgreSQL-owned issue time and microsecond TTL, hash-only
credential persistence, revoke/authenticate races, one-winner concurrent
rotation, 101-row UUID keyset ordering, expired/revoked/corrupt scope fail
closed behavior, 23505 conflict mapping, and CTE rollback after a duplicate
new credential hash.

## Commands

```text
go test -race -mod=vendor -tags=integration -count=1 -p 1 ./internal/auth/adapter/postgres -timeout 5m
go test -mod=vendor -tags=integration -run '^$' ./internal/auth/adapter/postgres -count=1 -timeout 60s
go vet -mod=vendor ./internal/auth/adapter/postgres
go test -mod=vendor ./internal/auth/... -count=1 -timeout 60s
python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-auth-migration
git diff --check
```

All commands passed. `task.py validate` emitted only pre-existing context-size
and code-reference warnings in the child JSONL files; validation itself passed.

## Review

- Go review: PASS. The test callback keeps `testing.T` ownership, uses the
  shared Pool without opening a parallel one, and executes existing concurrent
  tests under `-race`.
- SQL review: PASS. The test SQL remains parameterized; the real suite verifies
  fixed keyset ordering, conflict mapping, row rollback, and no plaintext
  credential persistence. No dynamic SQL, N+1 loop, migration or index change
  was introduced.
- Trellis review: PASS. Scope is confined to the existing fixture and this
  child evidence; the child does not claim production Composition or TODO 3
  completion.

## Final Handoff

The Auth child is complete and awaits main-session review before archival. The
final Composition task remains responsible for switching `cmd/api` to GORM and
for any later legacy removal; those actions were intentionally not performed.
