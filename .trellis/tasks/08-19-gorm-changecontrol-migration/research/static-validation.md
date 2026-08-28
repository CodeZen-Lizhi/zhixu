# Change Control Staged GORM Static Validation

Date: 2026-08-20

## Scope Delivered

- Added a staged `GORMRepository` for Proposal, Approval, Authorization, Revision/history/fence and Safe Writeback.
- Added a staged `GORMApprovalDispatchRepository` using `ScopedRuntimeStarter` and optional `ScopedAppender` in the caller-owned scope.
- Added owner-scoped Knowledge Proposal create/initial-read methods for Graph Candidate Confirm. They only use the caller-owned live scope, and never start or finish a transaction.
- Both repositories derive GORM and Unit of Work from one `platformpostgres.Pool`. They do not open a pool, mutate schema or change production composition.
- Legacy pgx repositories, commands, migrations, tests and production constructors remain unchanged by this child.

## Static Contract Evidence

- Proposal conflict and nil-Event Approval replay exit through rollback sentinels before root durable lookup.
- Authorization expiry is committed before returning `WRITE_AUTHORIZATION_EXPIRED`; it is not rolled back with the caller-facing permission error.
- The shared transaction wrapper distinguishes callback failure from commit response loss and restores each legacy transaction/commit error code.
- Revision preserves receipt -> Authorization -> Proposal/Revision -> dependent fence ordering and exact root receipt recovery.
- Writeback preserves deterministic Authorization locks, Proposal-before-Execution checkpoint/publish locking, DB time, CAS and Commit/Outbox exact binding.
- Dispatch keeps Approval, Workflow Run/Node/Outbox/River, revision dispatch and Proposal CAS in one scope. Rejected dispatch creates no Workflow/River facts.
- Explicit typed-nil collaborators fail at construction; omitting the variadic Event appender remains the supported no-Event configuration.
- Context classifiers preserve canceled/deadline/custom causes and legacy retryability. Raw SQL is parameterized; JSONB uses a string `driver.Valuer`, and arrays use `pq.Array`.
- Static scans found no `AutoMigrate`, `Migrator`, `Save`, `Preload`, `Association`, root GORM `Transaction`, independent `gorm.Open` or direct pgx import in the staged files.
- API and Worker still construct `changecontrolpostgres.NewRepository` and `approvaldispatchpostgres.NewApprovalDispatchRepository`.
- Scoped Knowledge Proposal creation writes the canonical current Revision pointer; replay and direct lookup always lock and scan immutable Revision 1, including historical rows with a null or later current pointer.

## Review Findings Fixed

1. Proposal legacy request hashes were initially accepted as new writes. They are now accepted only for exact persisted replay; new records require the current hash.
2. Expired Authorization initially returned an error from inside the UoW and rolled back the persisted expiry. The caller-facing error is now returned after a successful commit.
3. The main UoW initially collapsed begin and commit errors into `CHANGE_CONTROL_TRANSACTION_FAILED`. Each entry point now supplies its legacy transaction and commit codes, and callback-success commit failures are classified correctly.
4. Explicit typed-nil Event appenders initially degraded to the no-Event mode. Constructors now reject them while zero variadic arguments still select the optional mode.
5. Approval Dispatch cancellation initially diverged from the legacy dependency-unavailable/retryable contract. Its classifier now preserves that contract and the context cause chain.
6. The invalid Proposal hash lookup bypassed repository/context readiness. It now fails closed through `ready` before using the root.
7. The scoped Proposal conflict path initially compared mutable Proposal status with the requested initial READY state. That check was removed: exact binding remains strict on type, risk, request hash, idempotency key and immutable Revision 1 change hash, while normal later state transitions remain replayable.

Independent Go and SQL reviews found no remaining P0/P1/P2 after these fixes. Trellis scope/status review confirmed that TODO 9 and Final gates remain open.

## Commands And Results

Passed:

```text
go test -mod=vendor ./internal/changecontrol/... -count=1 -timeout 60s
go test -race -mod=vendor ./internal/changecontrol/... -count=1 -timeout 60s
go vet -mod=vendor ./internal/changecontrol/... ./internal/platform/postgres ./internal/events/... ./internal/workflow/...
go test -mod=vendor -tags=integration -run '^$' ./internal/changecontrol/adapter/postgres ./internal/changecontrol/adapter/approvaldispatchpostgres ./internal/changecontrol/application -count=1 -timeout 60s
go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker -count=1 -timeout 60s
go list -mod=vendor ./internal/changecontrol/...
go mod verify
python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-changecontrol-migration
gofmt -d <staged Change Control files>
git diff --check
```

The scoped prerequisite was additionally checked with the full Change Control unit/race suite, adapter and platform vet, integration compile-only, forbidden API scan and an independent Go review. A manual SQL review verified fixed parameterized statements, Candidate-before-Proposal compatibility, Proposal/Revision row locks, immutable Revision 1 selection, JSONB string binding and absence of nested transaction or root fallback.

`go mod tidy -diff` was run read-only and returned exit 1 for the pre-existing repository-wide `go.sum` normalization drift, plus an unrelated sqlite checksum suggestion. No tidy output was applied and this child changed no dependency files.

## TODO 9 Blockers

`ZHIXU_TEST_DATABASE_URL` is not configured. Compile-only integration checks do not prove PostgreSQL placeholder/cast binding, JSONB/array behavior, trigger/deferred closure, real SQLSTATE, DB time, lock competition/deadlock, commit response loss or cross-owner rollback.

Before production activation, the existing integration files must run legacy/GORM fixtures on separate disposable databases using the single-Pool dependency chain documented in `design.md`. This includes scoped Proposal rollback, lock competition, historical null/current-pointer behavior and real JSONB/SQLSTATE execution. The task therefore remains `in_progress`; PRD acceptance criteria and all TODO 9 items remain unchecked, and no staged constructor is wired into API or Worker.
