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

## Focused PostgreSQL Fixture

The existing `internal/changecontrol/adapter/postgres/repository_integration_test.go`
now includes `TestGORMRepositoryProposalApprovalAndIdempotency`. It provisions a
disposable database through the shared `testdb` factory, constructs the staged
GORM repository from one platform Pool, and covers the minimal main path:
Proposal create, exact idempotent replay, conflicting replay with no extra
Revision, Approval, exact Approval replay, and the single-row Approval invariant.
This is intentionally narrower than the legacy matrix and does not add
response-loss, EXPLAIN, or indiscriminate race scenarios.

The test was executed against a disposable Testcontainers PostgreSQL database with:

```text
go test -mod=vendor -tags=integration -run '^TestGORMRepositoryProposalApprovalAndIdempotency$' ./internal/changecontrol/adapter/postgres -count=1 -timeout 120s
```

Result: PASS (about 11 seconds). The fixture uses the shared `testdb` factory
and one platform Pool. The separate Approval Dispatch fixture below covers the
same-Pool Workflow/River and Model Settings scoped fence path.

The existing `internal/changecontrol/adapter/approvaldispatchpostgres/repository_integration_test.go`
now also includes `TestGORMApprovalDispatchAtomicallyBindsWorkflowAndRiver`.
It composes Audit GORM, a fixed-key Model Settings GORM repository, Events GORM,
Change Control GORM as the Workflow cancellation-safety hook, Workflow GORM
scoped River runtime, and Approval Dispatch GORM from one platform Pool. The
test verifies the first approved dispatch, an exact replay without safety
observations, Proposal binding, and exactly one Approval/Run/Node/Outbox/River
row set. It passed against disposable Testcontainers PostgreSQL with:

```text
go test -mod=vendor -tags=integration -run '^TestGORMApprovalDispatchAtomicallyBindsWorkflowAndRiver$' ./internal/changecontrol/adapter/approvaldispatchpostgres -count=1 -timeout 180s
```

Result: PASS (about 8 seconds after the final hook fix). No no-op fence, second pool, or production
composition change was used.

The existing `internal/changecontrol/adapter/postgres/repository_integration_test.go`
also includes `TestGORMScopedKnowledgeProposalUsesCallerTransaction`. It verifies
typed Knowledge Proposal create/exact replay plus immutable Revision 1 read in
the caller scope, the canonical current Revision pointer, committed visibility,
explicit callback-error rollback, and rejection of nil, foreign-type, and stale
scopes. It passed
against disposable Testcontainers PostgreSQL with:

```text
go test -mod=vendor -tags=integration -run '^TestGORM(RepositoryProposalApprovalAndIdempotency|ScopedKnowledgeProposalUsesCallerTransaction)$' ./internal/changecontrol/adapter/postgres -count=1 -timeout 180s
```

Result: PASS (about 13 seconds after the final scope assertions).

## Lean Gate Result And Final Boundary

The focused Testcontainers fixtures now prove the staged Proposal/Approval path,
same-Pool approved Dispatch chain, and scoped Knowledge Proposal commit/rollback.
The full historical TODO 9 matrix remains intentionally out of scope under the
2026-09-01 lean-test policy: response-loss, broad lock competition, EXPLAIN,
full legacy/GORM parity, and indiscriminate integration race are not rerun
unless their directly changed mechanism requires them. Production API/Worker
composition remains on legacy constructors, and the Final constructor/legacy
deletion gate remains separate from this child.

## Final Trellis Check (2026-09-05)

The final review corrected the focused Approval Dispatch fixture so the Change
Control GORM repository is installed as Workflow's scoped cancellation-safety
hook before the runtime is constructed. The replay command now contains only
the Workspace and persisted Approval binding, which directly verifies that a
complete replay does not require safety observations. The scoped Knowledge
fixture now also covers exact create replay, the canonical current Revision
pointer, and nil/foreign/stale scope rejection in the same lean scenario.

Passed after the fixes:

```text
go test -mod=vendor ./internal/changecontrol/... -count=1 -timeout 60s
go vet -mod=vendor ./internal/changecontrol/... ./internal/platform/postgres ./internal/events/... ./internal/workflow/... ./internal/modelsettings/... ./internal/audit/...
go test -mod=vendor -tags=integration -run '^TestGORM(RepositoryProposalApprovalAndIdempotency|ScopedKnowledgeProposalUsesCallerTransaction)$' ./internal/changecontrol/adapter/postgres -count=1 -timeout 180s
go test -mod=vendor -tags=integration -run '^TestGORMScopedKnowledgeProposalUsesCallerTransaction$' ./internal/changecontrol/adapter/postgres -count=1 -timeout 180s
go test -mod=vendor -tags=integration -run '^TestGORMApprovalDispatchAtomicallyBindsWorkflowAndRiver$' ./internal/changecontrol/adapter/approvaldispatchpostgres -count=1 -timeout 180s
python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-changecontrol-migration
gofmt -d internal/changecontrol/adapter/postgres/repository_integration_test.go internal/changecontrol/adapter/approvaldispatchpostgres/repository_integration_test.go
git diff --check
```

The forbidden API/import scan remained empty: no independent `gorm.Open`,
schema mutation, implicit association API, root GORM transaction, production
GORM composition, or database implementation type entered Domain/Application.
No P0/P1/P2 finding remains in the reviewed Change Control scope.
