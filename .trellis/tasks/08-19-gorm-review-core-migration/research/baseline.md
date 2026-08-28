# Review Core GORM Migration Baseline

## Scope

- Owner: `internal/review/adapter/postgres` only.
- Public persistence surface: 21 `application.Repository` methods plus `application.EvidenceVerifier`.
- Production constructor: `cmd/api/main.go` currently calls legacy `reviewpostgres.NewRepository(pool)`; staged work must not change it.
- Shared table boundary: Review Core and Interview share `learning.review_session`; generic `GetSession` may return either valid shell, while Review write/replay paths require REVIEW sessions bound to a Deck.

## Owned And Read Tables

- Owned: `learning.review_deck`, `review_card`, `review_schedule`, `review_session`, `review_answer`, `review_command`.
- Trigger-maintained read projection: `learning.review_card_evidence_selector`.
- Read/lock dependencies: `core.workspace`, `core.claim`, `core.claim_source`, `core.conflict`, `core.conflict_member`, `core.source_version`, `ingestion.attempt`.
- Authoritative migrations: `00034`, `00035`, `00044`, `00047`, `00049`, `00052`, `00055`, `00056`, `00058`, `00060`, `00062`; staged GORM must map them, not modify them.

## Fixed Invariants

- Every write first locks the Workspace row `FOR UPDATE`.
- Command replay is checked before mutable state; command/answer facts are append-only and workspace-scoped.
- Answer insert, trusted Score/Feedback snapshot and Schedule version CAS are one transaction.
- APPROVED Card and Schedule cardinality is protected by deferred triggers; non-approved Card status removes Schedule through database triggers.
- Evidence verification uses confirmed Claim, conflict exclusion, Source/Span/Hash cardinality and `FOR SHARE` locks.
- Due query owns daily limits, UTC day boundaries, eligibility, evidence safety and stable order.
- Invalidation is an indexed bounded CTE with five static selector shapes; no `SKIP LOCKED`, JSON full scan or row loop.
- Commit ambiguity is resolved only by exact receipt/answer lookup after the transaction returns an error.

## Existing Evidence And Blind Spots

- Existing integration coverage includes Answer/Schedule atomicity and idempotency, shell guard, edit/reapprove ABA, concurrent schedule commands, Approval vs quarantine/high conflict, due eligibility, malformed legacy evidence and lifecycle invalidation.
- Tests are gated by `ZHIXU_TEST_DATABASE_URL`; it is unavailable in the current environment.
- Existing tests construct legacy `NewRepository(*pgxpool.Pool)` only. Static compile/race/vet cannot prove GORM placeholder/cast, SQLSTATE, lock, trigger, commit ambiguity or connection-release parity.
- Therefore the child may deliver a staged adapter but must remain `in_progress`, preserve production pgx wiring and leave all PRD AC unchecked until TODO 9.
