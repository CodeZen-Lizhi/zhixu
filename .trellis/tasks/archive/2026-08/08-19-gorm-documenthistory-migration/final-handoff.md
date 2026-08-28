# Document History Final Composition Handoff

## Staged Constructor

Final must resolve the shared GORM root from the existing `*platformpostgres.Pool`
and pass it to `postgres.NewGORMRepository(...)`. It must use the same Pool that
owns the physical pgx pool and `database/sql` view; do not introduce a raw DSN,
separate GORM root, second pool, runtime selector, shadow read, dual write, or
fallback path.

## Current Boundary

`cmd/api/main.go` still constructs the legacy `postgres.NewRepository(pool)`.
This child has only validated the staged GORM repository through the shared
Testcontainers fixture. It does not change production selection, migrations,
Git history behavior, Change Control ownership, or the legacy pgx constructor.

## Final Work

- In the Final composition child, replace the Document History construction in
  `cmd/api/main.go` with the GORM repository derived from the existing platform
  Pool, preserving the same `application.DocumentReader` dependency exposed to
  the service and HTTP layers.
- Keep the legacy pgx implementation until all 28 module children and TODO 3
  have passed their Final gates. Then remove legacy-only Document History query
  plumbing and direct pgx dependencies in one reviewed composition cleanup.
- Retain the existing integration fixture as the production-path regression
  suite. The completed child proves legacy/GORM parity, one-statement 50-commit
  mapping, error classification, and actual use of
  `idx_authoring_publication_history_git` plus `uq_proposal_commit_git` on the
  5,000-row PostgreSQL fixture.

## Rollback

Before legacy deletion, rollback is a single Final composition change back to
`postgres.NewRepository(pool)`. Do not roll back migrations or durable facts,
add a second connection lifecycle, or conceal parity failures with fallback or
dual-write behavior.
