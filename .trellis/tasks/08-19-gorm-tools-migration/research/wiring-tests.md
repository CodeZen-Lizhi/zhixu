# Tools Wiring And Test Research

## Existing Test Assets

The repository already contains integration coverage that a separately authorized TODO 9 shared verification task
should parameterize in place:

- Tools repository integration: policy/refusal/start/CAS/unknown/timeline, insert guard, stale recovery,
  Trusted Write, heartbeat race and legacy commit-response-loss.
- Workspace Analysis refusal integration.
- Platform migration integration: authorization, same-attempt reconcile, replacement unknown, result receipt,
  receipt failure/private binding, execution, budget/proof/deadline/refusal/publication closure.
- Worker integration: Tool execution and Workspace Analysis conversation paths. These require a Final composition
  seam before they can run a GORM variant; they are inventory here, not a Tools-child acceptance gate.

The Tools child does not modify these tests. No new test file is required in the static phase. Existing tests currently
use pgx fixtures and skip without `ZHIXU_TEST_DATABASE_URL`.

Before any Tools product implementation, the Workflow task must deliver policy/recovery scoped Ports and the Agent
task must deliver Tool participant/refusal/authority scoped Ports. TODO 9 cannot substitute fakes for these concrete
owner adapters.

## TODO 9 Fixture Contract

Each legacy/GORM variant uses an independent disposable database:

1. Open a migration pool and apply the current migration set.
2. Close the migration pool.
3. Open exactly one `platformpostgres.Pool` for the variant.
4. Legacy gets `Pool.DB()`; GORM Tools, Workflow, Agent, Events, Audit and UoW are constructed from that same Pool.
5. Test-only pgx may seed fixtures, but GORM behavior reads committed rows through its production-style constructor.
6. Teardown closes only the platform Pool and verifies no acquired connection remains.

An active scope from another Pool cannot currently be rejected by Foundation. Tests therefore prove correct
single-Pool composition, while the platform limitation remains explicit.

## Response-Loss Injection

The existing legacy harness wraps pgx transaction commit. The GORM variant instead replaces the repository's private
UoW field from the same package: run a real `Within`, let the commit succeed, then return an injected error. The same
Repository call must immediately start a fresh UoW transaction, verify the exact durable Tool/Receipt/Operation/
Refusal closure, and return canonical replay. If the closure cannot be proven, it must return the documented
commit/unknown error. No public weak transaction seam is added for testing.

## Static Gate

Before TODO 9:

```text
go test -mod=vendor ./internal/tools/... -count=1 -timeout 60s
go test -race -mod=vendor ./internal/tools/... -count=1 -timeout 60s
go vet -mod=vendor ./internal/tools/... ./internal/platform/postgres
go test -mod=vendor -tags=integration -run '^$' ./internal/tools/... ./internal/platform/migration -count=1 -timeout 60s
go test -mod=vendor ./cmd/api ./cmd/worker -run '^$' -count=1 -timeout 60s
go list -mod=vendor ./internal/tools/...
go mod verify
git diff --check
```

Also scan for production GORM constructor references, `gorm.Open`, root `Transaction`, DDL/AutoMigrate, pgx data
access, Workflow/Agent owner SQL in Tools staged files, dynamic input SQL, unsafe JSONB/bytea binding and
unbounded/N+1 queries.

## Final Handoff

Production constructor replacement remains a Final concern. Final must construct Tools, Workflow, Agent, Events,
Audit and River dependencies from the same Pool, add the testable Worker composition variant, switch both basic and
Workspace Analysis Worker paths, then remove legacy pgx seams only after TODO 9 and Worker fault gates pass.
