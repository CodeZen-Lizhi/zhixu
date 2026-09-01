# Review Interview GORM Migration Baseline

## Scope Snapshot

- Legacy owner: `internal/review/interview/adapter/postgres`.
- Production constructors: `cmd/api/main.go:1200`, `cmd/worker/main.go:1899`; both call legacy `NewRepository(pool)` and remain unchanged before Final.
- Legacy files: `repository.go`, `commands.go`, `codec.go`, `errors.go`; approximately 2.1k production lines plus existing tests.
- Current child status at planning: no GORM implementation, no production selector, no Interview Application/Domain database-library imports.
- Workspace is broadly dirty from the parent migration; this child owns only its task directory and new Interview staged adapter files. Unrelated changes must be preserved.

## Public Surface

`application.Store` has 15 methods:

- replay: `FindStartReplay`, `FindSubmitReplay`, `FindCompleteReplay`, `FindPathStepReplay`, `FindPathStatusReplay`;
- command: `Start`, `Submit`, `BeginComplete`, `PrepareComplete`, `Complete`, `UpdatePathStep`, `UpdatePathStatus`;
- read: `Get`, `List`, `GetPath`.

The same Repository also implements `application.QuestionSource.Select` and exposes `AbandonStaleCompletions` for Worker maintenance. A staged GORM sibling must cover all three surfaces without changing their Go contracts.

## Schema Evidence

- `migrations/00046_m8_interview_learning_path.sql`: Interview Session/Question/Turn/Report/Path/Step/command base schema and initial guards.
- `00056_m8_interview_evidence_provenance.sql`: strict evidence/provenance and Review/Interview shell separation.
- `00057_m8_interview_completion_reservation.sql`: completion reservation, Artifact holds and maintenance protocol.
- `00059_m8_interview_memory_integrity.sql`: Interview memory/provenance integrity and no-keepalive hardening.
- `00060_review_shared_learning_path.sql`: shared `learning.learning_path(_step)` base relations; `learning.interview_learning_path(_step)` become auto-updatable `origin_type='INTERVIEW'` views with local check option.

The Interview adapter must continue using the compatibility views. Direct shared-table writes would bypass the owner boundary and make origin filtering an application concern.

## Transaction And Lock Baseline

- Start: transaction advisory key -> receipt -> parent shell/session/questions -> receipt.
- Submit: advisory -> receipt -> Interview Session + Review shell `FOR UPDATE` -> completion reservation fence -> Turn/Question/Session CAS -> receipt.
- Begin/Prepare/Complete: advisory -> Session/shell `FOR UPDATE` -> reservation `FOR UPDATE`; Complete additionally locks both holds and atomically writes Report/Path/Steps, terminal shell/session, receipt and reservation.
- Path commands: advisory -> receipt -> Path/Steps locks -> version CAS -> receipt.
- Maintenance: reservation candidate `FOR UPDATE SKIP LOCKED` -> ABANDONED -> matching holds ORPHANED; intentionally no Session lock.

All command receipts are append-only and must be checked before mutable-state decisions. Existing response-loss recovery performs a root receipt lookup after an ambiguous transaction/commit result.

## Completion Contract

- Begin freezes the current Session snapshot version and creates/reopens a PENDING reservation; it does not set a digest or touch holds.
- Prepare freezes one Artifact digest, orphans mismatched ACTIVE holds and may reactivate same-digest ORPHANED holds when that role has no ACTIVE hold.
- The external Artifact bridge creates REPORT/PATH artifacts and holds after Prepare.
- Complete verifies all frozen inputs and Artifact tuples, requires exactly two matching ACTIVE holds, writes terminal facts, deletes both holds, appends receipt, and marks reservation COMPLETED in one transaction.
- Missing, duplicate, stale or mismatched holds/artifacts/digests must roll back every Interview write.
- Maintenance can mark stale PENDING reservations ABANDONED and matching holds ORPHANED. A later result for the old attempt is rejected; the same command can reopen with the next attempt.

## Existing Verification

- `repository_integration_test.go`: active-index Question selection, Start/Submit/Complete replay, missing-hold rollback, shell/provenance guards, zero FSRS writes, Path commands and stable keyset List.
- `completion_reservation_integration_test.go`: stale reservation abandonment, hold orphaning and same-command next-attempt recovery.
- `codec_test.go`: codec/row error behavior.
- Integration tests create disposable migrated databases but currently construct only legacy `NewRepository(*pgxpool.Pool)`.

## Known Gaps Before TODO 9

- `ZHIXU_TEST_DATABASE_URL` is absent, so no real PostgreSQL GORM query, array/JSONB binding, trigger, lock or SQLSTATE has run.
- Existing tests have no GORM implementation factory.
- Existing pgx commit-response-loss double cannot wrap Foundation/GORM UoW; TODO 9 needs a same-package UoW wrapper that returns an injected error after a successful real `Within`.
- Existing integration cases do not fully prove deterministic two-client races for Submit/Complete/maintenance or connection release on every GORM Row/Rows path.
