# Review Core Staged GORM Static Validation

Date: 2026-08-20

## Result

The staged Review Core GORM adapter is implemented and passes the static-stage
quality gate. It is intentionally not connected to production. The child remains
`in_progress`, every PRD acceptance criterion and every TODO 9 item remains
unchecked, and the existing pgx repository remains the production implementation.

## Scoped Changes

Only these Review Core implementation files were added:

- `internal/review/adapter/postgres/gorm_core.go`
- `internal/review/adapter/postgres/gorm_model.go`
- `internal/review/adapter/postgres/gorm_queries.go`
- `internal/review/adapter/postgres/gorm_repository.go`
- `internal/review/adapter/postgres/gorm_session.go`
- `internal/review/adapter/postgres/gorm_invalidation.go`
- `internal/review/adapter/postgres/gorm_deck_schedule.go`

The staged type implements all 21 `application.Repository` methods and
`application.EvidenceVerifier`. No Review legacy file, Application/Domain port,
test, migration, `cmd/**`, module or vendor file belongs to this child change.
The wider dirty worktree contains concurrent changes in several of those areas;
they were preserved and are not attributed to Review Core.

## Static Verification

The following commands passed after the final fix:

```text
go test -mod=vendor ./internal/review/... -count=1 -timeout 60s
go test -race -mod=vendor ./internal/review/... -count=1 -timeout 60s
go vet -mod=vendor ./internal/review/... ./internal/platform/postgres
go test -mod=vendor -tags=integration -run '^$' ./internal/review/adapter/postgres -count=1 -timeout 60s
go test -mod=vendor -run '^$' ./cmd/api -count=1 -timeout 60s
go list -mod=vendor ./internal/review/... ./cmd/api
go mod verify
python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-review-core-migration
gofmt -d internal/review/adapter/postgres/gorm_*.go
git diff --check -- internal/review/adapter/postgres .trellis/tasks/08-19-gorm-review-core-migration
git diff --no-index --check /dev/null <each-new-file>
```

`go mod tidy -diff` was run read-only and returned exit 1 because it reports the
existing worktree-wide `go.sum` normalization drift, including sqlite checksums.
The output was not applied and this child did not modify `go.mod`, `go.sum` or
vendor. `go mod verify` and vendor-mode compile/list checks passed.

Static scans confirmed:

- `cmd/api/main.go` still constructs `reviewpostgres.NewRepository(pool)`;
- no production reference to `NewGORMRepository` exists;
- Review Application/Domain expose no GORM, pgx or `database/sql` type;
- staged files contain no `AutoMigrate`, `Migrator`, root `Transaction`,
  `SQLTransaction`, independent pool, `gorm.Open` or `SKIP LOCKED`;
- SQL identifiers are package-owned fixed text; JSONB uses a validated string
  carrier and evidence arrays use `pq.Array` single binds;
- all Row/Rows/Exec helpers receive caller context and Rows paths check iteration
  and close errors without partial return.

Trellis validation passes with the expected warnings that three shared backend
specs exceed the 32 KiB injection limit. `research/implementation-contract.md`
is now included in both manifests to provide the complete child-specific rules
despite that truncation; the shared specs remain authoritative.

## Independent Review

### Go Review

No P0/P1 was found. One P2 was fixed: constructor and transaction-scope unwrap
errors discarded their underlying cause. `gorm_core.go` now wraps those errors
with `%w`, retaining stable external Review error codes while restoring
`errors.Is/As` and diagnostic causality. The full static gate passed again.

### SQL And Transaction Review

No P0-P2 code defect was found. The reviewer checked every fixed SQL statement
against the pgx baseline, including bind count/order, Workspace-first locks,
receipt/Answer replay, Card/Schedule CAS, `pq.Array`, JSONB, the three-bind Due
CTE and all five bounded invalidation selectors. A documentation drift was fixed:
SubmitAnswer locks Evidence before Schedule in both legacy and staged paths.

### Trellis Check

No P0/P1 scope or status defect was found. The missing evidence/checklist and
oversized-context mitigation were fixed by this record, the completed static
checklist and the compact implementation contract. Production remains legacy;
the child can be archived independently after the TODO 9 evidence below, while
production composition remains blocked on the Review Interview, Review Learning
Path and Final convergence tasks.

## TODO 9 PostgreSQL Completion Evidence

Date: 2026-08-31

The existing `repository_integration_test.go` now runs every one of its 11
scenarios through an in-file `legacy-pgx` / `gorm` factory. Each variant opens
its own fully migrated disposable PostgreSQL database through `testdb.Require`.
The GORM variant receives one `platformpostgres.Pool` and derives `DB()`,
`GORM()` and `UnitOfWork()` from that same object; no second pool is opened.

The paired race runs passed for:

- Answer, Schedule and receipt atomicity, same-key replay and different-key
  contention, plus real-commit response-loss recovery;
- REVIEW/INTERVIEW shell separation, Card edit/reapprove ABA and Deck schedule
  command races;
- Approval/quarantine and high-conflict serialization;
- server-authoritative Due eligibility, UTC daily limit and future Schedule;
- legacy/malformed Evidence, REFUTES Evidence and SUPERSEDED Claim behavior;
- all five bounded invalidation selectors, replay and final tail batch;
- caller cancel/deadline, connection release and shared-pool reuse;
- real SQLSTATE constraints and GORM `sql.ErrTxDone` classification.

The TODO 9 race gate reported these concrete values for both implementations:

- Due returned exactly 200 rows and the analyzed plan used
  `idx_learning_review_due`; the GORM path issued exactly 1 SQL statement.
- Source-version invalidation used
  `idx_learning_review_card_evidence_selector_lookup`; the plan also retained
  the Card impact seek index. `enable_seqscan=off` and, for the small selector
  fixture only, `enable_nestloop=off` are local EXPLAIN settings and do not
  alter production SQL or session defaults.
- Claim, source-version, claim+source-version, source-span and
  claim+source-span invalidation each issued 4 GORM statements, below the
  bounded limit of 5; no per-Card query was observed.
- Every first batch returned `200 + has_more=true`, exact replay returned the
  same result, and the tail batch returned `1 + has_more=false`.
- Trigger cleanup removed all Schedules and evidence selectors; Review SSE,
  Health and Timeline projections were present with the expected cardinality.

The literal package-wide command was also executed:

```text
go test -race -mod=vendor -tags=integration -count=1 -p 1 -timeout 60s ./internal/review/adapter/postgres
```

It reached the package timeout while the third scenario was applying Atlas to
another fresh container. The stack was in `testdb.AtlasMigration`, not in a
test assertion, repository call or race report. Because 11 scenarios each own
two independent migrated databases, the same command was split by test name so
every invocation stayed within the repository's 60-second test policy. All 11
legacy/GORM pairs passed with `-race -p 1 -timeout 60s`.

Final static verification after the integration changes passed:

```text
go test -mod=vendor ./internal/review/... -count=1 -timeout 60s
go test -race -mod=vendor ./internal/review/... -count=1 -timeout 60s
go vet -mod=vendor ./internal/review/... ./internal/platform/postgres
go test -mod=vendor -tags=integration -run '^$' ./internal/review/adapter/postgres -count=1 -timeout 60s
go test -mod=vendor -run '^$' ./cmd/api -count=1 -timeout 60s
go list -mod=vendor ./internal/review/... ./cmd/api
go mod verify
go mod tidy -diff
```

`go mod tidy -diff` now exits successfully with no output. Gofmt and scoped
`git diff --check` are clean. Static scans still show no AutoMigrate, Migrator,
independent pool, root GORM transaction, dynamic identifier, `SKIP LOCKED` or
production `NewGORMRepository` wiring.

## Final Convergence Handoff

- Review Interview and Review Learning Path must complete their own paired
  PostgreSQL gates before the Review composition is changed.
- Final composition must replace the single `reviewpostgres.NewRepository(pool)`
  construction in `cmd/api/main.go` with the approved shared-pool GORM
  constructors; no selector, dual write or fallback is allowed.
- Legacy Review Core files may be deleted only after the Final task proves all
  Review ports, API/worker wiring, rollback and startup behavior against the
  unified composition. This child does not modify `cmd/**` or delete legacy.
- Child rollback is limited to the seven staged GORM files and the paired test
  factory/logging added to the existing integration file.
