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
checklist and the compact implementation contract. Production remains legacy,
PRD AC remains unchecked and task status remains `in_progress`.

## TODO 9 Blind Spots

`ZHIXU_TEST_DATABASE_URL` is absent. The integration command above only compiles
the tagged suite and existing tests construct the pgx repository; it does not
execute the staged GORM implementation. The following remain unproven and block
completion, production wiring and archival:

- real pgx-stdlib/GORM placeholder, UUID, JSONB and array binding;
- trigger/FK/check behavior and actual SQLSTATE/constraint error chains;
- Workspace/Card/Session/Schedule lock competition and CAS rollback;
- Answer, Schedule and receipt atomicity under same/different-key concurrency;
- commit-response-loss exact recovery through the planned UoW wrapper;
- Review versus Interview shell behavior, ABA and schedule-command races;
- Due and five-selector invalidation plans, index usage, trigger projections,
  bounded statement count, connection release and cancel/deadline behavior.

TODO 9 must run the existing integration file as paired legacy/GORM subtests on
separate migrated disposable databases before any acceptance criterion is marked.
