//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"gorm.io/gorm"
)

func TestGORMSourceReadyTransactions(t *testing.T) {
	fixture := testdb.Require(t, testdb.Config{Availability: testdb.FailWhenUnavailable, MaxConns: 8})
	pool := fixture.Pool()
	database, err := pool.GORM()
	if err != nil {
		t.Fatal(err)
	}
	unitOfWork, err := pool.UnitOfWork()
	if err != nil {
		t.Fatal(err)
	}
	outbox, err := workflowpostgres.NewGORMSourceReadyOutbox(pool)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewGORMRepositoryWithSourceReady(pool, outbox)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()

	t.Run("dependencies and live scope", func(t *testing.T) {
		var missing *failAfterSourceReadyAppend
		if _, err := NewGORMRepositoryWithSourceReady(pool, missing); err == nil {
			t.Fatal("accepted typed-nil source-ready appender")
		}
		if _, err := NewGORMRepositoryWithSourceReady(pool, nil); err == nil {
			t.Fatal("accepted missing source-ready appender")
		}
		if _, err := NewGORMRepositoryWithSourceReady(nil, outbox); err == nil {
			t.Fatal("accepted missing pool")
		}
		if _, _, err := outbox.ClaimSourceReadyScoped(ctx, nil); err == nil {
			t.Fatal("accepted missing scope")
		}
		var closed foundation.TransactionScope
		if err := unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(_ context.Context, scope foundation.TransactionScope) error {
			closed = scope
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := outbox.ClaimSourceReadyScoped(ctx, closed); err == nil {
			t.Fatal("accepted completed scope")
		}
	})

	var firstFact workflowapplication.SourceReadyOutboxFact
	var firstSource domain.SourceReady
	t.Run("append failure rolls back attempt and event", func(t *testing.T) {
		firstSource = seedSourceReadyFixture(t, database, repository, "7f100001", true)
		attempt := sourceReadyChunkingAttempt(t, repository, firstSource)
		wantVersion := attempt.Version
		failure := errors.New("injected failure after durable source-ready append")
		failingRepository, err := NewGORMRepositoryWithSourceReady(pool, &failAfterSourceReadyAppend{delegate: outbox, failure: failure})
		if err != nil {
			t.Fatal(err)
		}
		transition := sourceReadyCompletion(attempt, firstSource.OccurredAt)
		result, err := failingRepository.TransitionAttempt(ctx, transition)
		if !errors.Is(err, failure) || result.ID != "" {
			t.Fatalf("failed transition = %#v, %v", result, err)
		}
		persisted, err := repository.GetAttempt(ctx, attempt.ID)
		if err != nil || persisted.Status != domain.AttemptChunking || persisted.Version != wantVersion || persisted.CompletedAt != nil {
			t.Fatalf("rolled-back attempt = %#v, %v", persisted, err)
		}
		assertSourceReadyCount(t, database, firstSource.SourceVersionID, 0)
		var sourceCount int
		if err := database.Raw(`SELECT count(*) FROM core.source_version WHERE id=?`, string(firstSource.SourceVersionID)).Scan(&sourceCount).Error; err != nil || sourceCount != 1 {
			t.Fatalf("original source retained = %d, %v", sourceCount, err)
		}
		projection, err := repository.GetProjection(ctx, firstSource.ParseProjectionID, "v1", "v1")
		if err != nil || len(projection.Chunks) != 1 {
			t.Fatalf("projection retained = %#v, %v", projection, err)
		}
		result, err = repository.TransitionAttempt(ctx, transition)
		if err != nil || result.Status != domain.AttemptChunked || result.Version != wantVersion+1 {
			t.Fatalf("retry completion = %#v, %v", result, err)
		}
		assertSourceReadyCount(t, database, firstSource.SourceVersionID, 1)
		firstFact = claimSourceReadyFixture(t, unitOfWork, outbox)
		firstSource.IngestionAttemptID = attempt.ID
		if firstFact.EventID == "" || firstFact.Ready != firstSource {
			t.Fatalf("verified ready tuple = %#v, want %#v", firstFact, firstSource)
		}
		if _, err := repository.TransitionAttempt(ctx, transition); !classifiedAs(err, foundation.ErrorVersionConflict) {
			t.Fatalf("stale transition error = %v", err)
		}
	})

	t.Run("reparse keeps first notification and rejects changed binding", func(t *testing.T) {
		attempt := sourceReadyChunkingAttempt(t, repository, firstSource)
		if _, err := repository.TransitionAttempt(ctx, sourceReadyCompletion(attempt, firstSource.OccurredAt.Add(time.Second))); err != nil {
			t.Fatal(err)
		}
		assertSourceReadyCount(t, database, firstSource.SourceVersionID, 1)
		fact := claimSourceReadyFixture(t, unitOfWork, outbox)
		if fact != firstFact {
			t.Fatalf("reparse replaced first notification: %#v", fact)
		}
		changed := firstSource
		changed.ContentHash = strings.Repeat("b", 64)
		err := unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
			return outbox.AppendSourceReadyScoped(ctx, scope, changed)
		})
		assertSourceReadyErrorCode(t, err, "SOURCE_READY_OUTBOX_BINDING_CONFLICT")
		assertSourceReadyCount(t, database, firstSource.SourceVersionID, 1)
	})

	t.Run("publish requires exact binding and shares caller rollback", func(t *testing.T) {
		wrong := firstFact
		wrong.Ready.WorkspaceID = "7f100099-0000-4000-8000-000000000001"
		err := unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
			return outbox.PublishSourceReadyScoped(ctx, scope, wrong)
		})
		assertSourceReadyErrorCode(t, err, "SOURCE_READY_OUTBOX_PUBLISH_CONFLICT")
		failure := errors.New("consumer workflow start did not commit")
		err = unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
			fact, found, err := outbox.ClaimSourceReadyScoped(ctx, scope)
			if err != nil || !found || fact != firstFact {
				return fmt.Errorf("claim before rollback: found=%t, err=%w", found, err)
			}
			if err := outbox.PublishSourceReadyScoped(ctx, scope, fact); err != nil {
				return err
			}
			return failure
		})
		if !errors.Is(err, failure) {
			t.Fatal(err)
		}
		if fact := claimSourceReadyFixture(t, unitOfWork, outbox); fact != firstFact {
			t.Fatalf("rollback consumed event: %#v", fact)
		}
		publishSourceReadyFixture(t, unitOfWork, outbox, firstFact)
		err = unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
			return outbox.PublishSourceReadyScoped(ctx, scope, firstFact)
		})
		assertSourceReadyErrorCode(t, err, "SOURCE_READY_OUTBOX_PUBLISH_CONFLICT")
	})

	t.Run("failed quarantined cancelled and invalid attempts do not notify", func(t *testing.T) {
		source := seedSourceReadyFixture(t, database, repository, "7f100002", true)
		for _, terminal := range []struct {
			status   domain.AttemptStatus
			security domain.SecurityStatus
		}{
			{domain.AttemptParseFailed, domain.SecurityPending},
			{domain.AttemptCancelled, domain.SecurityPending},
			{domain.AttemptValidating, domain.SecurityQuarantined},
		} {
			attempt := sourceReadyInitialAttempt(t, repository, source)
			if _, err := repository.TransitionAttempt(ctx, domain.AttemptTransition{
				ID: attempt.ID, ExpectedVersion: attempt.Version, Status: terminal.status,
				SecurityStatus: terminal.security, CompletedAt: &source.OccurredAt,
			}); err != nil {
				t.Fatal(err)
			}
		}
		attempt := sourceReadyChunkingAttempt(t, repository, source)
		invalid := sourceReadyCompletion(attempt, source.OccurredAt)
		invalid.SecurityStatus = domain.SecurityQuarantined
		if _, err := repository.TransitionAttempt(ctx, invalid); err == nil {
			t.Fatal("accepted security-quarantined chunked attempt")
		}
		invalid = sourceReadyCompletion(attempt, source.OccurredAt)
		invalid.ParseProjectionID = nil
		if _, err := repository.TransitionAttempt(ctx, invalid); err == nil {
			t.Fatal("accepted chunked attempt without projection")
		}
		invalid = sourceReadyCompletion(attempt, source.OccurredAt)
		invalid.ParseProjectionID = &firstSource.ParseProjectionID
		if _, err := repository.TransitionAttempt(ctx, invalid); err == nil {
			t.Fatal("accepted projection from another workspace")
		}
		persisted, err := repository.GetAttempt(ctx, attempt.ID)
		if err != nil || persisted.Status != domain.AttemptChunking || persisted.Version != attempt.Version {
			t.Fatalf("invalid transition changed attempt = %#v, %v", persisted, err)
		}
		assertSourceReadyCount(t, database, source.SourceVersionID, 0)
	})

	t.Run("missing source provenance rolls back ready transition", func(t *testing.T) {
		source := seedSourceReadyFixture(t, database, repository, "7f100003", false)
		attempt := sourceReadyChunkingAttempt(t, repository, source)
		_, err := repository.TransitionAttempt(ctx, sourceReadyCompletion(attempt, source.OccurredAt))
		assertSourceReadyErrorCode(t, err, "INGESTION_SOURCE_READY_BINDING_INVALID")
		persisted, err := repository.GetAttempt(ctx, attempt.ID)
		if err != nil || persisted.Status != domain.AttemptChunking || persisted.Version != attempt.Version {
			t.Fatalf("unverified source transition = %#v, %v", persisted, err)
		}
		assertSourceReadyCount(t, database, source.SourceVersionID, 0)
	})

	t.Run("cancellation preserves cause and rolls back both writes", func(t *testing.T) {
		source := seedSourceReadyFixture(t, database, repository, "7f100007", true)
		attempt := sourceReadyChunkingAttempt(t, repository, source)
		cancelledCtx, cancel := context.WithCancelCause(t.Context())
		defer cancel(nil)
		cause := errors.New("source-ready test caller stopped")
		cancelledRepository, err := NewGORMRepositoryWithSourceReady(pool, &failAfterSourceReadyAppend{
			delegate: outbox, failure: cause, cancel: cancel,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = cancelledRepository.TransitionAttempt(cancelledCtx, sourceReadyCompletion(attempt, source.OccurredAt))
		var failure *foundation.Error
		if !errors.Is(err, cause) || !errors.Is(err, context.Canceled) || !errors.As(err, &failure) ||
			failure.Kind != foundation.ErrorNonRetryableFailure || failure.Retryable {
			t.Fatalf("cancelled transition error = %v", err)
		}
		persisted, err := repository.GetAttempt(ctx, attempt.ID)
		if err != nil || persisted.Status != domain.AttemptChunking || persisted.Version != attempt.Version {
			t.Fatalf("cancelled attempt = %#v, %v", persisted, err)
		}
		assertSourceReadyCount(t, database, source.SourceVersionID, 0)
		if _, err := repository.TransitionAttempt(ctx, sourceReadyCompletion(attempt, source.OccurredAt)); err != nil {
			t.Fatal(err)
		}
		fact := claimSourceReadyFixture(t, unitOfWork, outbox)
		if fact.Ready.IngestionAttemptID != attempt.ID {
			t.Fatalf("cancellation retry changed identity: %#v", fact)
		}
		publishSourceReadyFixture(t, unitOfWork, outbox, fact)
	})

	t.Run("commit response loss preserves source and exact replay", func(t *testing.T) {
		source := seedSourceReadyFixture(t, database, repository, "7f100004", true)
		attempt := sourceReadyChunkingAttempt(t, repository, source)
		responseLost := errors.New("injected committed response loss")
		repository.unitOfWork = sourceReadyCommitResponseLoss{UnitOfWork: unitOfWork, failure: responseLost}
		defer func() { repository.unitOfWork = unitOfWork }()
		result, err := repository.TransitionAttempt(ctx, sourceReadyCompletion(attempt, source.OccurredAt))
		if !errors.Is(err, responseLost) || result.ID != "" {
			t.Fatalf("unknown commit result = %#v, %v", result, err)
		}
		assertSourceReadyErrorCode(t, err, "INGESTION_ATTEMPT_TRANSITION_COMMIT_FAILED")
		repository.unitOfWork = unitOfWork
		// Process resumes through this same idempotent CreateAttempt boundary.
		retry := attempt
		retry.ID, err = (foundation.UUIDGenerator{}).New()
		if err != nil {
			t.Fatal(err)
		}
		retry.Status, retry.SecurityStatus = domain.AttemptValidating, domain.SecurityPending
		retry.ParseProjectionID, retry.CompletedAt, retry.Version = nil, nil, 1
		replayed, err := repository.CreateAttempt(ctx, retry)
		if err != nil || replayed.Created || replayed.Attempt.ID != attempt.ID || replayed.Attempt.Status != domain.AttemptChunked {
			t.Fatalf("committed attempt replay = %#v, %v", replayed, err)
		}
		assertSourceReadyCount(t, database, source.SourceVersionID, 1)
		fact := claimSourceReadyFixture(t, unitOfWork, outbox)
		if fact.Ready.IngestionAttemptID != attempt.ID || fact.Ready.SourceVersionID != source.SourceVersionID {
			t.Fatalf("committed notification = %#v", fact)
		}
		publishSourceReadyFixture(t, unitOfWork, outbox, fact)
	})

	t.Run("concurrent reparses append once and claims skip locked rows", func(t *testing.T) {
		source := seedSourceReadyFixture(t, database, repository, "7f100005", true)
		first := sourceReadyChunkingAttempt(t, repository, source)
		second := sourceReadyChunkingAttempt(t, repository, source)
		results := make(chan error, 2)
		for _, attempt := range []domain.AttemptRecord{first, second} {
			go func() {
				_, err := repository.TransitionAttempt(ctx, sourceReadyCompletion(attempt, source.OccurredAt))
				results <- err
			}()
		}
		var failures []error
		for range 2 {
			if err := <-results; err != nil {
				failures = append(failures, err)
			}
		}
		if len(failures) != 0 {
			t.Fatal(errors.Join(failures...))
		}
		assertSourceReadyCount(t, database, source.SourceVersionID, 1)
		later := seedSourceReadyFixture(t, database, repository, "7f100006", true)
		later.OccurredAt = later.OccurredAt.Add(time.Second)
		attempt := sourceReadyChunkingAttempt(t, repository, later)
		if _, err := repository.TransitionAttempt(ctx, sourceReadyCompletion(attempt, later.OccurredAt)); err != nil {
			t.Fatal(err)
		}
		rollback := errors.New("release first consumer claim")
		var locked workflowapplication.SourceReadyOutboxFact
		err := unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
			fact, found, err := outbox.ClaimSourceReadyScoped(ctx, scope)
			if err != nil || !found || fact.Ready.SourceVersionID != source.SourceVersionID {
				return fmt.Errorf("FIFO claim: found=%t, err=%w", found, err)
			}
			locked = fact
			// A separate transaction/connection represents the second consumer
			// while the first consumer still owns its row lock.
			otherCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			err = unitOfWork.Within(otherCtx, foundation.TransactionOptions{}, func(ctx context.Context, otherScope foundation.TransactionScope) error {
				next, found, err := outbox.ClaimSourceReadyScoped(ctx, otherScope)
				if err != nil || !found || next.EventID == locked.EventID || next.Ready.SourceVersionID != later.SourceVersionID {
					return fmt.Errorf("skip-locked claim: found=%t, err=%w", found, err)
				}
				return outbox.PublishSourceReadyScoped(ctx, otherScope, next)
			})
			if err != nil {
				return err
			}
			return rollback
		})
		if !errors.Is(err, rollback) {
			t.Fatal(err)
		}
		if fact := claimSourceReadyFixture(t, unitOfWork, outbox); fact != locked {
			t.Fatalf("first claim was lost: %#v", fact)
		}
		publishSourceReadyFixture(t, unitOfWork, outbox, locked)
		if err := unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
			_, found, err := outbox.ClaimSourceReadyScoped(ctx, scope)
			if found {
				return errors.New("published notification was claimed again")
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	})
}

func seedSourceReadyFixture(t *testing.T, database *gorm.DB, repository *GORMRepository, prefix string, provenance bool) domain.SourceReady {
	t.Helper()
	ctx := t.Context()
	hashCharacter := prefix[len(prefix)-1:]
	workspaceID, artifactID, versionID := seedSourceVersionExec(t, ctx, func(ctx context.Context, query string, args ...any) error {
		return database.WithContext(ctx).Exec(query, args...).Error
	}, prefix, hashCharacter)
	projectionID := mustID(t, prefix+"-0000-4000-8000-000000000005")
	now := time.Date(2026, 9, 9, 3, 0, 0, 123456000, time.UTC)
	projection := domain.ParseProjection{
		ID: projectionID, WorkspaceID: workspaceID, ContentArtifactID: artifactID,
		ParserID: "source-ready-test", ParserVersion: "v1", ParserConfigHash: strings.Repeat("a", 64),
		SchemaVersion: "v1", NormalizedContentHash: strings.Repeat("b", 64), CreatedAt: now,
	}
	if provenance {
		spanID := mustID(t, prefix+"-0000-4000-8000-000000000006")
		_, err := repository.SaveProjection(ctx, domain.ProjectionWrite{
			SourceVersionID: versionID, Projection: projection, CreatedAt: now,
			Spans: []domain.SourceSpan{{
				ID: spanID, SpanType: "paragraph", StartLine: 1, EndLine: 1, StartByte: 0, EndByte: 4,
				ExcerptHash: strings.Repeat("a", 64), ParserVersion: "v1", SchemaVersion: "v1",
			}},
			Chunks: []domain.CanonicalChunk{{
				ID: mustID(t, prefix+"-0000-4000-8000-000000000007"), Sequence: 0, Content: "test",
				ContentHash: strings.Repeat("a", 64), SourceSpanID: spanID, ByteCount: 4, RuneCount: 4,
				ParserVersion: "v1", ChunkStrategyVersion: "v1", SchemaVersion: "v1", Status: "active",
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
	} else {
		// The schema permits an Attempt/projection binding without the
		// source-version provenance row; Source-ready must reject that gap.
		if err := database.WithContext(ctx).Exec(`INSERT INTO ingestion.parse_projection
			(id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,created_at)
			VALUES(?,?,?,?,?,?,?,?,?)`, string(projection.ID), string(workspaceID), string(artifactID), projection.ParserID,
			projection.ParserVersion, projection.ParserConfigHash, projection.SchemaVersion, projection.NormalizedContentHash, now).Error; err != nil {
			t.Fatal(err)
		}
	}
	return domain.SourceReady{
		WorkspaceID: workspaceID, SourceID: mustID(t, prefix+"-0000-4000-8000-000000000003"),
		SourceVersionID: versionID, ContentArtifactID: artifactID, ParseProjectionID: projectionID,
		ContentHash: strings.Repeat(hashCharacter, 64), OccurredAt: now.Add(time.Second),
	}
}

func sourceReadyInitialAttempt(t *testing.T, repository *GORMRepository, source domain.SourceReady) domain.AttemptRecord {
	t.Helper()
	id, err := (foundation.UUIDGenerator{}).New()
	if err != nil {
		t.Fatal(err)
	}
	created, err := repository.CreateAttempt(t.Context(), domain.AttemptRecord{
		Attempt: domain.Attempt{
			ID: id, WorkspaceID: source.WorkspaceID, SourceVersionID: source.SourceVersionID,
			Status: domain.AttemptValidating, SecurityStatus: domain.SecurityPending,
			ParserID: "source-ready-test", ParserVersion: "v1", ParserConfigHash: strings.Repeat("a", 64),
			ChunkStrategyVersion: "v1", SchemaVersion: "v1", IdempotencyKey: string(id), AttemptNumber: 1,
		},
		StartedAt: source.OccurredAt.Add(-time.Second), Version: 1,
	})
	if err != nil || !created.Created {
		t.Fatalf("create source-ready attempt = %#v, %v", created, err)
	}
	return created.Attempt
}

func sourceReadyChunkingAttempt(t *testing.T, repository *GORMRepository, source domain.SourceReady) domain.AttemptRecord {
	t.Helper()
	attempt := sourceReadyInitialAttempt(t, repository, source)
	for _, status := range []domain.AttemptStatus{domain.AttemptParsing, domain.AttemptParsed, domain.AttemptChunking} {
		transition := domain.AttemptTransition{ID: attempt.ID, ExpectedVersion: attempt.Version, Status: status, SecurityStatus: domain.SecurityPassed}
		if status != domain.AttemptParsing {
			transition.ParseProjectionID = &source.ParseProjectionID
		}
		var err error
		attempt, err = repository.TransitionAttempt(t.Context(), transition)
		if err != nil {
			t.Fatal(err)
		}
	}
	return attempt
}

func sourceReadyCompletion(attempt domain.AttemptRecord, at time.Time) domain.AttemptTransition {
	return domain.AttemptTransition{
		ID: attempt.ID, ExpectedVersion: attempt.Version, Status: domain.AttemptChunked,
		SecurityStatus: domain.SecurityPassed, ParseProjectionID: attempt.ParseProjectionID, CompletedAt: &at,
	}
}

func assertSourceReadyCount(t *testing.T, database *gorm.DB, versionID foundation.ID, want int) {
	t.Helper()
	var count int
	err := database.WithContext(t.Context()).Raw(`SELECT count(*) FROM workflow.outbox_event
		WHERE event_type=? AND payload->>'source_version_id'=?`, workflowapplication.SourceReadyEventType, string(versionID)).Scan(&count).Error
	if err != nil || count != want {
		t.Fatalf("source-ready count = %d, want %d, error = %v", count, want, err)
	}
}

func claimSourceReadyFixture(t *testing.T, unitOfWork foundation.UnitOfWork, outbox workflowapplication.ScopedSourceReadyOutbox) workflowapplication.SourceReadyOutboxFact {
	t.Helper()
	var result workflowapplication.SourceReadyOutboxFact
	err := unitOfWork.Within(t.Context(), foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		fact, found, err := outbox.ClaimSourceReadyScoped(ctx, scope)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("expected a source-ready notification")
		}
		result = fact
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func publishSourceReadyFixture(t *testing.T, unitOfWork foundation.UnitOfWork, outbox workflowapplication.ScopedSourceReadyOutbox, fact workflowapplication.SourceReadyOutboxFact) {
	t.Helper()
	if err := unitOfWork.Within(t.Context(), foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		return outbox.PublishSourceReadyScoped(ctx, scope, fact)
	}); err != nil {
		t.Fatal(err)
	}
}

func assertSourceReadyErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var failure *foundation.Error
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("error = %v, want %s", err, code)
	}
}

type failAfterSourceReadyAppend struct {
	delegate domain.SourceReadyAppender
	failure  error
	cancel   context.CancelCauseFunc
}

func (appender *failAfterSourceReadyAppend) AppendSourceReadyScoped(ctx context.Context, scope foundation.TransactionScope, ready domain.SourceReady) error {
	if err := appender.delegate.AppendSourceReadyScoped(ctx, scope, ready); err != nil {
		return err
	}
	if appender.cancel != nil {
		appender.cancel(appender.failure)
	}
	return appender.failure
}

type sourceReadyCommitResponseLoss struct {
	foundation.UnitOfWork
	failure error
}

func (unit sourceReadyCommitResponseLoss) Within(ctx context.Context, options foundation.TransactionOptions, work foundation.TransactionFunc) error {
	if err := unit.UnitOfWork.Within(ctx, options, work); err != nil {
		return err
	}
	return unit.failure
}
