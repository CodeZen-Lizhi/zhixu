# Memory GORM Migration Baseline

## Repository Surface

| Method | Current persistence contract |
| --- | --- |
| `FindCommand` | command advisory lock, receipt `FOR UPDATE`, exact binding replay |
| `CreateCandidate` | exact replay, Interview provenance replay, workspace lock, Memory + audit + receipt transaction |
| `Mutate` | owner-scoped row lock, full snapshot/version CAS, DB-time expiry check, audit + receipt transaction |
| `Get` | exact Workspace + stable owner + ID, unauthorized hidden as NotFound |
| `List` | optional type/status arrays, `(updated_at,id)` descending keyset, Limit+1 |
| `LoadEffective` | ACTIVE + confirmation + DB-time expiry + Workspace/owner/task filtering at SQL root |
| `ExpireDue` | one DB timestamp, bounded `FOR UPDATE SKIP LOCKED`, per-row CAS + audit in one transaction |

The public port is `internal/memory/application/model.go:98`; the legacy implementation is `internal/memory/adapter/postgres/repository.go:37-325`.

## Schema And Lock Facts

- Base aggregate: `migrations/00034_learning_ops_auth.sql:220`.
- owner/effective/expiry indexes: `migrations/00045_m8_memory.sql:102-109`; receipt/audit tables and lifecycle triggers: `migrations/00045_m8_memory.sql:111-275`.
- stable owner, source/identity immutability and SSE projection: `migrations/00048_m8_memory_stable_owner.sql`.
- `memory_command` append-only guard: `migrations/00050_m8_interview_history_hardening.sql:16-19`.
- Interview semantic uniqueness/provenance: `migrations/00054_m8_interview_memory_candidate_idempotency.sql` and structured step binding in `migrations/00059_m8_interview_memory_integrity.sql:1-312`.
- one audit per aggregate version plus lifecycle/aggregate trigger: `migrations/00059_m8_interview_memory_integrity.sql:313-383`.
- command lock: `repository.go:476`; Interview provenance lock: `repository.go:486`; this order is a contract.
- command receipt is locked in `codec.go:92-112`; Interview semantic receipt joins and locks Memory/Command in `repository.go:518-543`.

## Callers And Production Wiring

- API: `cmd/api/main.go:426`, with `memorypostgres.NewRepository(pool)` at `cmd/api/main.go:1166`; Interview reuses the same service.
- Worker: `cmd/worker/main.go:1557`; expiry maintenance calls the Repository through the service.
- RAG and tools integration: `cmd/worker/rag_conversation_integration_test.go:245`, `cmd/worker/tool_composition_integration_test.go:214`.
- migration hardening integration: `internal/platform/migration/m8_interview_history_hardening_integration_test.go:109`.
- effective Memory consumers: `internal/agent/adapter/memory/loader.go:35` and `internal/review/interview/adapter/memory/loader.go:35`.

All remain legacy until the Final child.

## Existing Verification

- `repository_integration_test.go:27`: lifecycle, receipt replay, scope/task/expiry, audit guards.
- `repository_integration_test.go:118`: eight concurrent Confirms, one non-replayed result, stable receipt/audit counts.
- `repository_integration_test.go:186`: Interview exact/semantic replay, same-key conflict, eight-way concurrent provenance, one Candidate/audit and per-key receipts.
- `codec_test.go`: canonical snapshot round-trip and corrupt/duplicate/version-drift fail-closed behavior.
- Application/Domain/HTTP tests cover lifecycle, owner, expiry, response-loss recovery, cursor/auth/input behavior but cannot prove database locks or triggers.

The fixture at `repository_integration_test.go:390` creates a disposable database and runs all migrations, but currently returns only legacy `*Repository` and `*pgxpool.Pool`.

## TODO 9 Blind Spots

- GORM/database/sql advisory locks and fixed client-key -> provenance lock order.
- Raw JSONB and PostgreSQL array binding through the shared runtime `Pool.GORM()` root.
- GORM transaction rollback on audit/receipt/commit failure and request cancellation.
- trigger SQLSTATE propagation and no-row distinction.
- concurrent `ExpireDue` / `FOR UPDATE SKIP LOCKED`; no current repository integration test covers it.
- legacy/GORM behavior parity using isolated databases and shared platform Pool lifecycle.

`ZHIXU_TEST_DATABASE_URL` is currently unavailable, so these remain completion blockers rather than inferred successes.
