# PostgreSQL integration fixture

`testdb` is the shared lifecycle owner for real PostgreSQL/pgvector tests. A
fixture runs the current Atlas/River migration against an isolated database and
returns the existing `platformpostgres.Pool`; GORM, pgx, River and Unit of Work
must all be obtained from that pool.

## Container mode

Use `Require` from a test and select the provider policy at the call site:

```go
fixture := testdb.Require(t, testdb.Config{
    Availability: testdb.FailWhenUnavailable,
})
pool := fixture.Pool()
```

`FailWhenUnavailable` is the default. A test that is optional on a developer
machine must explicitly use `SkipWhenUnavailable`. The helper registers an
idempotent cleanup, so child tests must not terminate the container themselves.
The factory uses `pgvector/pgvector:pg16`, random mapped ports, and no container
reuse or fixed container names.

## External admin mode

`ExternalAdminURL` is an admin connection, not a test target URL. The role must
be allowed to create and drop databases. The factory generates a random
`zhixu_test_*` database, applies the same migration, and drops only that
database on close:

```go
fixture := testdb.Require(t, testdb.Config{
    ExternalAdminURL: os.Getenv("ZHIXU_TEST_DATABASE_URL"),
})
```

The database named by `ExternalAdminURL` is never migrated or deleted. Existing
child fixtures must map their admin DSN to `ExternalAdminURL`; they must not
pass a shared application database as a direct migration target.

For migration-failure tests or non-`testing.TB` callers, use
`testdb.Open(ctx, config)` and assert the returned error. Specialized tests may
inject a different `MigrationFunc`; the fixture lifecycle and cleanup contract
do not change.

## Verification

The factory-only Docker tests are intentionally opt-in:

```bash
make testcontainers-integration
```

When the local Testcontainers reaper cannot start, run the command with
`TESTCONTAINERS_RYUK_DISABLED=true` for a local smoke. Do not disable Ryuk in
the repository default or CI configuration.
