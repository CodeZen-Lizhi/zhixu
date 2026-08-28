//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	"github.com/jackc/pgx/v5"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestRepositoryPersistsDerivedEvidence(t *testing.T) {
	runIngestionRepositoryVariants(t, func(t *testing.T, repository domain.Repository, exec ingestionExec, _ ingestionCount, ctx context.Context) {
		workspaceID, artifactID, versionID := seedSourceVersionExec(t, ctx, exec, "7d000000", "d")
		now := time.Date(2026, 8, 2, 7, 0, 0, 0, time.UTC)
		derived := "Java AI PDF evidence"
		digest := sha256.Sum256([]byte(derived))
		spanID := mustID(t, "7d000000-0000-4000-8000-000000000011")
		write := domain.ProjectionWrite{
			SourceVersionID: versionID,
			Projection: domain.ParseProjection{
				ID: mustID(t, "7d000000-0000-4000-8000-000000000012"), WorkspaceID: workspaceID,
				ContentArtifactID: artifactID, ParserID: "pdf", ParserVersion: "pdf-v1",
				ParserConfigHash: strings.Repeat("a", 64), SchemaVersion: "parse-v1",
				NormalizedContentHash: strings.Repeat("b", 64), CreatedAt: now,
			},
			Spans: []domain.SourceSpan{{
				ID: spanID, SpanType: "document", StartLine: 1, EndLine: 1, StartByte: 0, EndByte: 4,
				Selector:    map[string]string{"format": "pdf", "page_start": "1", "page_end": "1"},
				ExcerptHash: hex.EncodeToString(digest[:]), EvidenceKind: domain.EvidenceDerivedText,
				DerivedExcerpt: derived, ParserVersion: "pdf-v1", SchemaVersion: "parse-v1",
			}},
			Chunks: []domain.CanonicalChunk{{
				ID: mustID(t, "7d000000-0000-4000-8000-000000000013"), Sequence: 0, Content: derived,
				ContentHash: hex.EncodeToString(digest[:]), SourceSpanID: spanID, ByteCount: int64(len(derived)), RuneCount: int64(len(derived)),
				ParserVersion: "pdf-v1", ChunkStrategyVersion: "structure-v1", SchemaVersion: "parse-v1", Status: "active",
			}},
			CreatedAt: now,
		}
		result, err := repository.SaveProjection(ctx, write)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Spans) != 1 || result.Spans[0].EvidenceKind != domain.EvidenceDerivedText || result.Spans[0].DerivedExcerpt != derived || result.Spans[0].Selector["page_start"] != "1" {
			t.Fatalf("derived span = %#v", result.Spans)
		}
		write.Projection.ID = mustID(t, "7d000000-0000-4000-8000-000000000014")
		replayed, err := repository.SaveProjection(ctx, write)
		if err != nil || replayed.Created || len(replayed.Spans) != 1 || replayed.Spans[0].DerivedExcerpt != derived || replayed.Spans[0].Selector["format"] != "pdf" {
			t.Fatalf("replayed projection = %#v, %v", replayed, err)
		}
	})
}

func TestRepositoryAttemptProjectionLifecycle(t *testing.T) {
	runIngestionRepositoryVariants(t, func(t *testing.T, repository domain.Repository, exec ingestionExec, _ ingestionCount, ctx context.Context) {
		workspaceID, artifactID, versionID := seedSourceVersionExec(t, ctx, exec, "71000000", "a")
		now := time.Date(2026, 7, 17, 2, 0, 0, 0, time.UTC)
		definitionID := mustID(t, "71500000-0000-4000-8000-000000000001")
		workflowRunID := mustID(t, "71500000-0000-4000-8000-000000000002")
		seedWorkflowExec(t, ctx, exec, workspaceID, definitionID, workflowRunID, now)
		attempt := domain.AttemptRecord{Attempt: domain.Attempt{
			ID: mustID(t, "72000000-0000-4000-8000-000000000001"), WorkspaceID: workspaceID, SourceVersionID: versionID,
			WorkflowRunID: &workflowRunID,
			Status:        domain.AttemptValidating, SecurityStatus: domain.SecurityPending, ParserID: "goldmark",
			ParserVersion: "1.8.4", ParserConfigHash: strings.Repeat("b", 64), ChunkStrategyVersion: "v1",
			SchemaVersion: "v1", IdempotencyKey: "request-1", AttemptNumber: 1,
		}, StartedAt: now, Version: 1}
		first, err := repository.CreateAttempt(ctx, attempt)
		if err != nil || !first.Created {
			t.Fatalf("CreateAttempt() = %#v, %v", first, err)
		}
		loadedAttempt, err := repository.GetAttempt(ctx, first.Attempt.ID)
		if err != nil || loadedAttempt.ID != first.Attempt.ID || loadedAttempt.WorkflowRunID == nil || *loadedAttempt.WorkflowRunID != workflowRunID || loadedAttempt.Version != 1 {
			t.Fatalf("GetAttempt() = %#v, %v", loadedAttempt, err)
		}
		if _, err := repository.GetAttempt(ctx, mustID(t, "72000000-0000-4000-8000-000000000099")); !classifiedAs(err, foundation.ErrorNotFound) {
			t.Fatalf("missing GetAttempt() error = %#v", err)
		}
		attempt.ID = mustID(t, "72000000-0000-4000-8000-000000000002")
		reused, err := repository.CreateAttempt(ctx, attempt)
		if err != nil || reused.Created || reused.Attempt.ID != first.Attempt.ID {
			t.Fatalf("duplicate CreateAttempt() = %#v, %v", reused, err)
		}
		attempt.AttemptNumber = 2
		if _, err := repository.CreateAttempt(ctx, attempt); !classifiedAs(err, foundation.ErrorVersionConflict) {
			t.Fatalf("idempotency attempt-number conflict = %#v", err)
		}
		attempt.AttemptNumber = 1
		attempt.WorkflowRunID = nil
		if _, err := repository.CreateAttempt(ctx, attempt); !classifiedAs(err, foundation.ErrorVersionConflict) {
			t.Fatalf("idempotency workflow conflict = %#v", err)
		}
		attempt.WorkflowRunID = &workflowRunID

		spanID := mustID(t, "73000000-0000-4000-8000-000000000001")
		projectionWrite := domain.ProjectionWrite{
			SourceVersionID: versionID,
			Projection: domain.ParseProjection{
				ID: mustID(t, "74000000-0000-4000-8000-000000000001"), WorkspaceID: workspaceID,
				ContentArtifactID: artifactID, ParserID: "goldmark", ParserVersion: "1.8.4",
				ParserConfigHash: strings.Repeat("b", 64), SchemaVersion: "v1",
				NormalizedContentHash: strings.Repeat("c", 64), CreatedAt: now,
			},
			Spans: []domain.SourceSpan{{
				ID: spanID, SpanType: "paragraph", StartLine: 1, EndLine: 1, StartByte: 0, EndByte: 4,
				ExcerptHash: strings.Repeat("d", 64), ParserVersion: "1.8.4", SchemaVersion: "v1",
			}},
			Chunks: []domain.CanonicalChunk{{
				ID: mustID(t, "75000000-0000-4000-8000-000000000001"), Sequence: 0, Content: "test",
				ContentHash: strings.Repeat("e", 64), SourceSpanID: spanID, ByteCount: 4, RuneCount: 4,
				ParserVersion: "1.8.4", ChunkStrategyVersion: "v1", SchemaVersion: "v1", Status: "active",
			}},
			CreatedAt: now,
		}
		projection, err := repository.SaveProjection(ctx, projectionWrite)
		if err != nil || !projection.Created || len(projection.Spans) != 1 || len(projection.Chunks) != 1 {
			t.Fatalf("SaveProjection() = %#v, %v", projection, err)
		}
		projectionWrite.Projection.ID = mustID(t, "74000000-0000-4000-8000-000000000002")
		duplicate, err := repository.SaveProjection(ctx, projectionWrite)
		if err != nil || duplicate.Created || duplicate.Projection.ID != projection.Projection.ID || len(duplicate.Chunks) != 1 {
			t.Fatalf("duplicate SaveProjection() = %#v, %v", duplicate, err)
		}
		v2SpanID := mustID(t, "74000000-0000-4000-8000-000000000003")
		v2ChunkID := mustID(t, "75000000-0000-4000-8000-000000000003")
		v2Write := projectionWrite
		v2Write.Spans = []domain.SourceSpan{{
			ID: v2SpanID, SpanType: "paragraph", StartLine: 1, EndLine: 1, StartByte: 0, EndByte: 4,
			ExcerptHash: strings.Repeat("d", 64), ParserVersion: "1.8.4", SchemaVersion: "v1",
		}}
		v2Write.Chunks = []domain.CanonicalChunk{{
			ID: v2ChunkID, Sequence: 0, Content: "test", ContentHash: strings.Repeat("e", 64), SourceSpanID: v2SpanID,
			ByteCount: 4, RuneCount: 4, ParserVersion: "1.8.4", ChunkStrategyVersion: "v2", SchemaVersion: "v1", Status: "active",
		}}
		v2, err := repository.SaveProjection(ctx, v2Write)
		if err != nil || v2.Created || len(v2.Chunks) != 1 || v2.Chunks[0].ChunkStrategyVersion != "v2" {
			t.Fatalf("second chunk strategy = %#v, %v", v2, err)
		}
		loadedV2, err := repository.GetProjection(ctx, projection.Projection.ID, "v2", "v1")
		if err != nil || len(loadedV2.Chunks) != 1 || loadedV2.Chunks[0].ChunkStrategyVersion != "v2" {
			t.Fatalf("GetProjection(v2) = %#v, %v", loadedV2, err)
		}
		allStrategies, err := repository.GetProjection(ctx, projection.Projection.ID, "", "")
		if err != nil || len(allStrategies.Chunks) != 2 {
			t.Fatalf("GetProjection(all strategies) = %#v, %v", allStrategies, err)
		}
		strategyOnly, err := repository.GetProjection(ctx, projection.Projection.ID, "v2", "")
		if err != nil || len(strategyOnly.Chunks) != 0 {
			t.Fatalf("GetProjection(strategy without schema) = %#v, %v", strategyOnly, err)
		}

		projectionID := projection.Projection.ID
		parsed, err := repository.TransitionAttempt(ctx, domain.AttemptTransition{
			ID: first.Attempt.ID, ExpectedVersion: 1, Status: domain.AttemptParsing, SecurityStatus: domain.SecurityPassed,
		})
		if err != nil || parsed.Version != 2 {
			t.Fatalf("TransitionAttempt(parsing) = %#v, %v", parsed, err)
		}
		parsed, err = repository.TransitionAttempt(ctx, domain.AttemptTransition{
			ID: first.Attempt.ID, ExpectedVersion: 2, Status: domain.AttemptParsed, SecurityStatus: domain.SecurityPassed,
			ParseProjectionID: &projectionID,
		})
		if err != nil || parsed.Status != domain.AttemptParsed || parsed.ParseProjectionID == nil {
			t.Fatalf("TransitionAttempt(parsed) = %#v, %v", parsed, err)
		}
		if _, err := repository.TransitionAttempt(ctx, domain.AttemptTransition{
			ID: first.Attempt.ID, ExpectedVersion: 2, Status: domain.AttemptChunking, SecurityStatus: domain.SecurityPassed,
			ParseProjectionID: &projectionID,
		}); !classifiedAs(err, foundation.ErrorVersionConflict) {
			t.Fatalf("stale transition error = %#v", err)
		}
		if err := exec(ctx, `UPDATE ingestion.parse_projection SET parser_version='changed' WHERE id=?`, string(projectionID)); err == nil {
			t.Fatal("immutable parse projection update succeeded")
		}
	})
}

func TestRepositoryConcurrentCreateAttemptIdempotency(t *testing.T) {
	for _, variant := range []string{"legacy", "gorm"} {
		t.Run(variant, func(t *testing.T) {
			ctx := context.Background()
			fixture := testdb.Require(t, testdb.Config{Availability: testdb.FailWhenUnavailable, MaxConns: 16})
			pool := fixture.Pool()
			workspaceID, _, versionID := seedCommittedSourceVersion(t, ctx, pool, "7e000000", "e")
			repositories := []domain.Repository{
				integrationRepositoryForVariant(t, variant, pool),
				integrationRepositoryForVariant(t, variant, pool),
			}
			attempt := domain.AttemptRecord{Attempt: domain.Attempt{
				ID: mustID(t, "7e500000-0000-4000-8000-000000000002"), WorkspaceID: workspaceID, SourceVersionID: versionID,
				Status: domain.AttemptValidating, SecurityStatus: domain.SecurityPending,
				ParserID: "goldmark", ParserVersion: "1.8.4", ParserConfigHash: strings.Repeat("f", 64),
				ChunkStrategyVersion: "v1", SchemaVersion: "v1", IdempotencyKey: "concurrent-request", AttemptNumber: 1,
			}, StartedAt: time.Date(2026, 7, 17, 5, 0, 0, 0, time.UTC), Version: 1}
			results := make(chan domain.AttemptResult, len(repositories))
			errorsCh := make(chan error, len(repositories))
			start := make(chan struct{})
			var workers sync.WaitGroup
			for _, repository := range repositories {
				workers.Add(1)
				go func(repository domain.Repository) {
					defer workers.Done()
					<-start
					result, err := repository.CreateAttempt(ctx, attempt)
					if err != nil {
						errorsCh <- err
						return
					}
					results <- result
				}(repository)
			}
			close(start)
			workers.Wait()
			close(results)
			close(errorsCh)
			for err := range errorsCh {
				t.Fatalf("concurrent CreateAttempt() error = %v", err)
			}
			created := 0
			var persistedID foundation.ID
			for result := range results {
				if result.Created {
					created++
				}
				if persistedID == "" {
					persistedID = result.Attempt.ID
				}
				if result.Attempt.ID != persistedID {
					t.Fatalf("concurrent CreateAttempt() IDs differ: %s and %s", persistedID, result.Attempt.ID)
				}
			}
			if created != 1 {
				t.Fatalf("concurrent CreateAttempt() created=%d, want exactly one", created)
			}
		})
	}
}

func TestRepositoryConcurrentTransitionAttemptCAS(t *testing.T) {
	for _, variant := range []string{"legacy", "gorm"} {
		t.Run(variant, func(t *testing.T) {
			ctx := context.Background()
			fixture := testdb.Require(t, testdb.Config{Availability: testdb.FailWhenUnavailable, MaxConns: 16})
			pool := fixture.Pool()
			workspaceID, _, versionID := seedCommittedSourceVersion(t, ctx, pool, "7f000000", "f")
			repositories := []domain.Repository{
				integrationRepositoryForVariant(t, variant, pool),
				integrationRepositoryForVariant(t, variant, pool),
			}
			attempt := domain.AttemptRecord{Attempt: domain.Attempt{
				ID: mustID(t, "7f500000-0000-4000-8000-000000000001"), WorkspaceID: workspaceID, SourceVersionID: versionID,
				Status: domain.AttemptValidating, SecurityStatus: domain.SecurityPending,
				ParserID: "goldmark", ParserVersion: "1.8.4", ParserConfigHash: strings.Repeat("a", 64),
				ChunkStrategyVersion: "v1", SchemaVersion: "v1", IdempotencyKey: "concurrent-transition", AttemptNumber: 1,
			}, StartedAt: time.Date(2026, 7, 17, 6, 0, 0, 0, time.UTC), Version: 1}
			created, err := repositories[0].CreateAttempt(ctx, attempt)
			if err != nil || !created.Created {
				t.Fatalf("CreateAttempt() = %#v, %v", created, err)
			}

			results := make(chan domain.AttemptRecord, len(repositories))
			errorsCh := make(chan error, len(repositories))
			start := make(chan struct{})
			var workers sync.WaitGroup
			for _, repository := range repositories {
				workers.Add(1)
				go func(repository domain.Repository) {
					defer workers.Done()
					<-start
					record, err := repository.TransitionAttempt(ctx, domain.AttemptTransition{
						ID: created.Attempt.ID, ExpectedVersion: 1, Status: domain.AttemptParsing, SecurityStatus: domain.SecurityPassed,
					})
					if err != nil {
						errorsCh <- err
						return
					}
					results <- record
				}(repository)
			}
			close(start)
			workers.Wait()
			close(results)
			close(errorsCh)

			succeeded := 0
			for result := range results {
				succeeded++
				if result.Status != domain.AttemptParsing || result.SecurityStatus != domain.SecurityPassed || result.Version != 2 {
					t.Fatalf("concurrent TransitionAttempt() success = %#v", result)
				}
			}
			conflicted := 0
			for err := range errorsCh {
				if !classifiedAs(err, foundation.ErrorVersionConflict) {
					t.Fatalf("concurrent TransitionAttempt() error = %#v", err)
				}
				conflicted++
			}
			if succeeded != 1 || conflicted != 1 {
				t.Fatalf("concurrent TransitionAttempt() successes=%d conflicts=%d, want 1/1", succeeded, conflicted)
			}
			persisted, err := repositories[0].GetAttempt(ctx, created.Attempt.ID)
			if err != nil || persisted.Status != domain.AttemptParsing || persisted.SecurityStatus != domain.SecurityPassed || persisted.Version != 2 {
				t.Fatalf("GetAttempt() after concurrent CAS = %#v, %v", persisted, err)
			}
		})
	}
}

func TestRepositoryConcurrentSaveProjectionIdempotency(t *testing.T) {
	for _, variant := range []string{"legacy", "gorm"} {
		t.Run(variant, func(t *testing.T) {
			ctx := context.Background()
			fixture := testdb.Require(t, testdb.Config{Availability: testdb.FailWhenUnavailable, MaxConns: 16})
			pool := fixture.Pool()
			workspaceID, artifactID, versionID := seedCommittedSourceVersion(t, ctx, pool, "81000000", "8")
			repositories := []domain.Repository{
				integrationRepositoryForVariant(t, variant, pool),
				integrationRepositoryForVariant(t, variant, pool),
			}
			spanID := mustID(t, "81000000-0000-4000-8000-000000000011")
			write := domain.ProjectionWrite{
				SourceVersionID: versionID,
				Projection: domain.ParseProjection{
					ID: mustID(t, "81000000-0000-4000-8000-000000000012"), WorkspaceID: workspaceID,
					ContentArtifactID: artifactID, ParserID: "goldmark", ParserVersion: "1.8.4",
					ParserConfigHash: strings.Repeat("a", 64), SchemaVersion: "v1",
					NormalizedContentHash: strings.Repeat("b", 64), CreatedAt: time.Date(2026, 7, 17, 7, 0, 0, 0, time.UTC),
				},
				Spans: []domain.SourceSpan{{
					ID: spanID, SpanType: "paragraph", StartLine: 1, EndLine: 1, StartByte: 0, EndByte: 4,
					ExcerptHash: strings.Repeat("c", 64), ParserVersion: "1.8.4", SchemaVersion: "v1",
				}},
				Chunks: []domain.CanonicalChunk{{
					ID: mustID(t, "81000000-0000-4000-8000-000000000013"), Sequence: 0, Content: "test",
					ContentHash: strings.Repeat("d", 64), SourceSpanID: spanID, ByteCount: 4, RuneCount: 4,
					ParserVersion: "1.8.4", ChunkStrategyVersion: "v1", SchemaVersion: "v1", Status: "active",
				}},
				CreatedAt: time.Date(2026, 7, 17, 7, 0, 0, 0, time.UTC),
			}

			results := make(chan domain.ProjectionResult, len(repositories))
			errorsCh := make(chan error, len(repositories))
			start := make(chan struct{})
			var workers sync.WaitGroup
			for _, repository := range repositories {
				workers.Add(1)
				go func(repository domain.Repository) {
					defer workers.Done()
					<-start
					result, err := repository.SaveProjection(ctx, write)
					if err != nil {
						errorsCh <- err
						return
					}
					results <- result
				}(repository)
			}
			close(start)
			workers.Wait()
			close(results)
			close(errorsCh)
			for err := range errorsCh {
				t.Fatalf("concurrent SaveProjection() error = %#v", err)
			}

			created := 0
			var projectionID foundation.ID
			for result := range results {
				if result.Created {
					created++
				}
				if projectionID == "" {
					projectionID = result.Projection.ID
				}
				if result.Projection.ID != projectionID || len(result.Spans) != 1 || len(result.Chunks) != 1 {
					t.Fatalf("concurrent SaveProjection() result = %#v", result)
				}
			}
			if created != 1 {
				t.Fatalf("concurrent SaveProjection() created=%d, want exactly one", created)
			}
			loaded, err := repositories[0].GetProjection(ctx, projectionID, "v1", "v1")
			if err != nil || len(loaded.Spans) != 1 || len(loaded.Chunks) != 1 {
				t.Fatalf("GetProjection() after concurrent save = %#v, %v", loaded, err)
			}
			assertRowCount(t, ctx, pool, 1, `SELECT count(*) FROM ingestion.parse_projection WHERE id=$1`, string(projectionID))
			assertRowCount(t, ctx, pool, 1, `SELECT count(*) FROM ingestion.source_version_projection WHERE source_version_id=$1 AND parse_projection_id=$2`, string(versionID), string(projectionID))
			assertRowCount(t, ctx, pool, 1, `SELECT count(*) FROM ingestion.source_span WHERE parse_projection_id=$1`, string(projectionID))
			assertRowCount(t, ctx, pool, 1, `SELECT count(*) FROM ingestion.canonical_chunk WHERE parse_projection_id=$1`, string(projectionID))
		})
	}
}

func TestRepositorySaveProjectionRollsBackFailedChunkBatch(t *testing.T) {
	runIngestionRepositoryVariants(t, func(t *testing.T, repository domain.Repository, exec ingestionExec, count ingestionCount, ctx context.Context) {
		workspaceID, artifactID, versionID := seedSourceVersionExec(t, ctx, exec, "82000000", "8")
		projectionID := mustID(t, "82000000-0000-4000-8000-000000000011")
		spanID := mustID(t, "82000000-0000-4000-8000-000000000012")
		write := domain.ProjectionWrite{
			SourceVersionID: versionID,
			Projection: domain.ParseProjection{
				ID: projectionID, WorkspaceID: workspaceID, ContentArtifactID: artifactID,
				ParserID: "goldmark", ParserVersion: "1.8.4", ParserConfigHash: strings.Repeat("a", 64),
				SchemaVersion: "v1", NormalizedContentHash: strings.Repeat("b", 64), CreatedAt: time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC),
			},
			Spans: []domain.SourceSpan{{
				ID: spanID, SpanType: "paragraph", StartLine: 1, EndLine: 1, StartByte: 0, EndByte: 4,
				ExcerptHash: strings.Repeat("c", 64), ParserVersion: "1.8.4", SchemaVersion: "v1",
			}},
			Chunks: []domain.CanonicalChunk{
				{ID: mustID(t, "82000000-0000-4000-8000-000000000013"), Sequence: 0, Content: "good", ContentHash: strings.Repeat("d", 64), SourceSpanID: spanID, ByteCount: 4, RuneCount: 4, ParserVersion: "1.8.4", ChunkStrategyVersion: "v1", SchemaVersion: "v1", Status: "active"},
				{ID: mustID(t, "82000000-0000-4000-8000-000000000014"), Sequence: 1, Content: "bad", ContentHash: "invalid", SourceSpanID: spanID, ByteCount: 3, RuneCount: 3, ParserVersion: "1.8.4", ChunkStrategyVersion: "v1", SchemaVersion: "v1", Status: "active"},
			},
			CreatedAt: time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC),
		}
		if _, err := repository.SaveProjection(ctx, write); !classifiedAs(err, foundation.ErrorConsistencyViolation) {
			t.Fatalf("SaveProjection() failed chunk batch error = %#v", err)
		}
		assertInTransactionRowCount(t, ctx, count, 0, `SELECT count(*) FROM ingestion.parse_projection WHERE id=?`, string(projectionID))
		assertInTransactionRowCount(t, ctx, count, 0, `SELECT count(*) FROM ingestion.source_version_projection WHERE source_version_id=? AND parse_projection_id=?`, string(versionID), string(projectionID))
		assertInTransactionRowCount(t, ctx, count, 0, `SELECT count(*) FROM ingestion.source_span WHERE parse_projection_id=?`, string(projectionID))
		assertInTransactionRowCount(t, ctx, count, 0, `SELECT count(*) FROM ingestion.canonical_chunk WHERE parse_projection_id=?`, string(projectionID))
	})
}

func TestRepositoryRejectsConflictingDeterministicProjection(t *testing.T) {
	runIngestionRepositoryVariants(t, func(t *testing.T, repository domain.Repository, exec ingestionExec, _ ingestionCount, ctx context.Context) {
		workspaceID, artifactID, versionID := seedSourceVersionExec(t, ctx, exec, "83000000", "8")
		spanID := mustID(t, "83000000-0000-4000-8000-000000000011")
		write := domain.ProjectionWrite{
			SourceVersionID: versionID,
			Projection: domain.ParseProjection{
				ID: mustID(t, "83000000-0000-4000-8000-000000000012"), WorkspaceID: workspaceID, ContentArtifactID: artifactID,
				ParserID: "goldmark", ParserVersion: "1.8.4", ParserConfigHash: strings.Repeat("a", 64),
				SchemaVersion: "v1", NormalizedContentHash: strings.Repeat("b", 64), CreatedAt: time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC),
			},
			Spans: []domain.SourceSpan{{
				ID: spanID, SpanType: "paragraph", StartLine: 1, EndLine: 1, StartByte: 0, EndByte: 4,
				ExcerptHash: strings.Repeat("c", 64), ParserVersion: "1.8.4", SchemaVersion: "v1",
			}},
			Chunks: []domain.CanonicalChunk{{
				ID: mustID(t, "83000000-0000-4000-8000-000000000013"), Sequence: 0, Content: "original",
				ContentHash: strings.Repeat("d", 64), SourceSpanID: spanID, ByteCount: 8, RuneCount: 8,
				ParserVersion: "1.8.4", ChunkStrategyVersion: "v1", SchemaVersion: "v1", Status: "active",
			}},
			CreatedAt: time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC),
		}
		created, err := repository.SaveProjection(ctx, write)
		if err != nil || !created.Created {
			t.Fatalf("initial SaveProjection() = %#v, %v", created, err)
		}

		conflicting := write
		conflicting.Projection.ID = mustID(t, "83000000-0000-4000-8000-000000000014")
		conflicting.Chunks = append([]domain.CanonicalChunk(nil), write.Chunks...)
		conflicting.Chunks[0].ID = mustID(t, "83000000-0000-4000-8000-000000000015")
		conflicting.Chunks[0].Content = "changed"
		conflicting.Chunks[0].ContentHash = strings.Repeat("e", 64)
		conflicting.Chunks[0].ByteCount = 7
		conflicting.Chunks[0].RuneCount = 7
		if _, err := repository.SaveProjection(ctx, conflicting); !classifiedWithCode(err, foundation.ErrorDependencyUnavailable, "INGESTION_CHUNK_CREATE_FAILED") {
			t.Fatalf("conflicting deterministic SaveProjection() error = %#v", err)
		}
		loaded, err := repository.GetProjection(ctx, created.Projection.ID, "v1", "v1")
		if err != nil || len(loaded.Chunks) != 1 || loaded.Chunks[0].Content != "original" || loaded.Chunks[0].ContentHash != strings.Repeat("d", 64) {
			t.Fatalf("GetProjection() after conflicting replay = %#v, %v", loaded, err)
		}
	})
}

func TestRepositoryRejectsInvalidStateAndCrossWorkspace(t *testing.T) {
	repository, tx, ctx := integrationRepository(t)
	_, _, versionID := seedSourceVersion(t, ctx, tx, "76000000", "f")
	otherWorkspaceID, otherArtifactID, _ := seedSourceVersion(t, ctx, tx, "77000000", "1")
	now := time.Date(2026, 7, 17, 3, 0, 0, 0, time.UTC)
	_, err := repository.SaveProjection(ctx, domain.ProjectionWrite{
		SourceVersionID: versionID,
		Projection: domain.ParseProjection{
			ID: mustID(t, "79000000-0000-4000-8000-000000000001"), WorkspaceID: otherWorkspaceID,
			ContentArtifactID: otherArtifactID, ParserID: "text", ParserVersion: "v1",
			ParserConfigHash: strings.Repeat("3", 64), SchemaVersion: "v1",
			NormalizedContentHash: strings.Repeat("4", 64), CreatedAt: now,
		},
		CreatedAt: now,
	})
	if !classifiedAs(err, foundation.ErrorConsistencyViolation) {
		t.Fatalf("cross workspace error = %#v", err)
	}
	// Constraint failures abort PostgreSQL transactions; use a fresh disposable
	// transaction for the independent invalid-state assertion.
	repository2, tx2, ctx2 := integrationRepository(t)
	workspaceID2, _, versionID2 := seedSourceVersion(t, ctx2, tx2, "78000000", "2")
	_, err = repository2.CreateAttempt(ctx2, domain.AttemptRecord{Attempt: domain.Attempt{
		ID: mustID(t, "78000000-0000-4000-8000-000000000005"), WorkspaceID: workspaceID2, SourceVersionID: versionID2,
		Status: domain.AttemptParsing, SecurityStatus: domain.SecurityPending, ParserID: "text", ParserVersion: "v1",
		ParserConfigHash: strings.Repeat("2", 64), ChunkStrategyVersion: "v1", SchemaVersion: "v1",
		IdempotencyKey: "invalid", AttemptNumber: 1,
	}, StartedAt: now, Version: 1})
	if !classifiedAs(err, foundation.ErrorConsistencyViolation) {
		t.Fatalf("invalid state error = %#v", err)
	}
}

func TestRepositoryRejectsAttemptProjectionParserContractMismatch(t *testing.T) {
	repository, tx, ctx := integrationRepository(t)
	workspaceID, artifactID, versionID := seedSourceVersion(t, ctx, tx, "7a000000", "a")
	now := time.Date(2026, 7, 17, 4, 0, 0, 0, time.UTC)
	projection, err := repository.SaveProjection(ctx, domain.ProjectionWrite{
		SourceVersionID: versionID,
		Projection: domain.ParseProjection{
			ID: mustID(t, "7b000000-0000-4000-8000-000000000001"), WorkspaceID: workspaceID,
			ContentArtifactID: artifactID, ParserID: "goldmark", ParserVersion: "1.8.4",
			ParserConfigHash: strings.Repeat("a", 64), SchemaVersion: "parse-v1",
			NormalizedContentHash: strings.Repeat("b", 64), CreatedAt: now,
		}, CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.CreateAttempt(ctx, domain.AttemptRecord{Attempt: domain.Attempt{
		ID: mustID(t, "7c000000-0000-4000-8000-000000000001"), WorkspaceID: workspaceID, SourceVersionID: versionID,
		Status: domain.AttemptParsed, SecurityStatus: domain.SecurityPassed,
		ParserID: "other-parser", ParserVersion: "1", ParserConfigHash: strings.Repeat("c", 64), ChunkStrategyVersion: "structure-v1", SchemaVersion: "parse-v1",
		IdempotencyKey: "contract-mismatch", AttemptNumber: 1,
	}, ParseProjectionID: &projection.Projection.ID, StartedAt: now, Version: 1})
	if !classifiedAs(err, foundation.ErrorConsistencyViolation) {
		t.Fatalf("parser contract mismatch error = %#v", err)
	}
}

// TestGORMRepositorySaveProjectionStatementBounds 用只计数的 GORM logger 固化
// SaveProjection 的 statement 上界：新建为 Cnew + ceil(spans/500) +
// ceil(chunks/500)，完整复用时无批次写入。logger 不展开或保存 SQL 与参数。
// TestGORMRepositoryCancellationAndDeadlockClassification 覆盖 caller 取消/过期
// deadline 的 sentinel 与 cause 保留、sql.ErrTxDone 还原、真实死锁的 retryable
// 分类以及失败路径的连接释放。40001 与 40P01 共用 gormClassify 的 retryable
// 分支，本测试以真实 deadlock 覆盖该分支。
func TestGORMRepositoryCancellationAndDeadlockClassification(t *testing.T) {
	ctx := context.Background()
	fixture := testdb.Require(t, testdb.Config{Availability: testdb.FailWhenUnavailable, MaxConns: 16})
	pool := fixture.Pool()
	root, err := pool.GORM()
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewGORMRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	workspaceID, _, versionID := seedCommittedSourceVersion(t, ctx, pool, "76400000", "f")
	now := time.Date(2026, 7, 17, 7, 0, 0, 0, time.UTC)
	newAttempt := func(id string, key string) domain.AttemptRecord {
		return domain.AttemptRecord{Attempt: domain.Attempt{
			ID: mustID(t, id), WorkspaceID: workspaceID, SourceVersionID: versionID,
			Status: domain.AttemptValidating, SecurityStatus: domain.SecurityPending,
			ParserID: "goldmark", ParserVersion: "1.8.4", ParserConfigHash: strings.Repeat("a", 64),
			ChunkStrategyVersion: "v1", SchemaVersion: "v1", IdempotencyKey: key, AttemptNumber: 1,
		}, StartedAt: now, Version: 1}
	}
	attemptA := newAttempt("76450000-0000-4000-8000-000000000001", "cancel-a")
	attemptB := newAttempt("76450000-0000-4000-8000-000000000002", "cancel-b")
	for _, record := range []domain.AttemptRecord{attemptA, attemptB} {
		created, err := repository.CreateAttempt(ctx, record)
		if err != nil || !created.Created {
			t.Fatalf("CreateAttempt() = %#v, %v", created, err)
		}
	}

	// 预先取消：sentinel 与自定义 cause 都必须保留。
	cancelledCtx, cancel := context.WithCancelCause(ctx)
	cancelCause := errors.New("ingestion integration cancellation")
	cancel(cancelCause)
	if _, err := repository.GetAttempt(cancelledCtx, attemptA.ID); !classifiedAs(err, foundation.ErrorNonRetryableFailure) {
		t.Fatalf("cancelled GetAttempt() error = %#v", err)
	} else if !errors.Is(err, context.Canceled) || !errors.Is(err, cancelCause) {
		t.Fatalf("cancelled GetAttempt() lost sentinel or cause: %v", err)
	}

	// 过期 deadline：保留 DeadlineExceeded sentinel。
	deadlineCtx, deadlineCancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer deadlineCancel()
	if _, err := repository.GetAttempt(deadlineCtx, attemptA.ID); !classifiedAs(err, foundation.ErrorNonRetryableFailure) {
		t.Fatalf("deadline GetAttempt() error = %#v", err)
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline GetAttempt() lost sentinel: %v", err)
	}

	// sql.ErrTxDone：外层事务已回滚。ctx 同时取消时必须还原 caller cause；
	// ctx 存活时归为 dependency unavailable，不 panic、不泄漏原始驱动错误。
	doneTx := root.WithContext(ctx).Begin()
	doneRepository, err := NewGORMRepository(doneTx)
	if err != nil {
		t.Fatal(err)
	}
	if err := doneTx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	doneCancelledCtx, doneCancel := context.WithCancelCause(ctx)
	doneCause := errors.New("ingestion tx done cancellation")
	doneCancel(doneCause)
	if _, err := doneRepository.GetAttempt(doneCancelledCtx, attemptA.ID); !errors.Is(err, context.Canceled) || !errors.Is(err, doneCause) {
		t.Fatalf("tx-done cancelled GetAttempt() lost sentinel or cause: %v", err)
	}
	if _, err := doneRepository.GetAttempt(ctx, attemptA.ID); !classifiedAs(err, foundation.ErrorDependencyUnavailable) {
		t.Fatalf("tx-done GetAttempt() error = %#v", err)
	}

	// 真实死锁：两个外层事务以相反顺序 CAS 两条 Attempt，PostgreSQL 死锁检测
	// 必须终止其中一方，失败方分类为 retryable，另一方两个 transition 都成功。
	type transitionOutcome struct{ firstErr, secondErr error }
	outcomes := make(chan transitionOutcome, 2)
	firstDone := make(chan struct{})
	secondReady := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		outcome := transitionOutcome{}
		txA := root.WithContext(ctx).Begin()
		defer func() { _ = txA.Rollback() }()
		repositoryA, err := NewGORMRepository(txA)
		if err != nil {
			outcome.firstErr = err
			outcomes <- outcome
			return
		}
		_, outcome.firstErr = repositoryA.TransitionAttempt(ctx, domain.AttemptTransition{
			ID: attemptA.ID, ExpectedVersion: 1, Status: domain.AttemptParsing, SecurityStatus: domain.SecurityPassed,
		})
		close(firstDone)
		<-secondReady
		if outcome.firstErr == nil {
			_, outcome.secondErr = repositoryA.TransitionAttempt(ctx, domain.AttemptTransition{
				ID: attemptB.ID, ExpectedVersion: 1, Status: domain.AttemptParsing, SecurityStatus: domain.SecurityPassed,
			})
		}
		outcomes <- outcome
	}()
	go func() {
		defer workers.Done()
		outcome := transitionOutcome{}
		txB := root.WithContext(ctx).Begin()
		defer func() { _ = txB.Rollback() }()
		repositoryB, err := NewGORMRepository(txB)
		if err != nil {
			outcome.firstErr = err
			outcomes <- outcome
			return
		}
		<-firstDone
		_, outcome.firstErr = repositoryB.TransitionAttempt(ctx, domain.AttemptTransition{
			ID: attemptB.ID, ExpectedVersion: 1, Status: domain.AttemptParsing, SecurityStatus: domain.SecurityPassed,
		})
		close(secondReady)
		if outcome.firstErr == nil {
			_, outcome.secondErr = repositoryB.TransitionAttempt(ctx, domain.AttemptTransition{
				ID: attemptA.ID, ExpectedVersion: 1, Status: domain.AttemptParsing, SecurityStatus: domain.SecurityPassed,
			})
		}
		outcomes <- outcome
	}()
	workers.Wait()
	close(outcomes)
	deadlocked, succeeded := 0, 0
	for outcome := range outcomes {
		if outcome.firstErr != nil {
			t.Fatalf("first TransitionAttempt() error = %#v", outcome.firstErr)
		}
		switch {
		case outcome.secondErr == nil:
			succeeded++
		case classifiedAs(outcome.secondErr, foundation.ErrorRetryableFailure):
			deadlocked++
		default:
			t.Fatalf("deadlock loser error = %#v", outcome.secondErr)
		}
	}
	if deadlocked != 1 || succeeded != 1 {
		t.Fatalf("deadlock outcomes: deadlocked=%d succeeded=%d, want 1/1", deadlocked, succeeded)
	}

	if acquired := pool.DB().Stat().AcquiredConns(); acquired != 0 {
		t.Fatalf("failure paths leaked shared pool connections: acquired=%d", acquired)
	}
}

// TestGORMRepositoryBlockedQueryCancellation 验证事务内阻塞查询被 caller 取消时
// 的分类、cause 保留、快速返回与连接释放，阻塞用排他表锁确定性制造。
func TestGORMRepositoryBlockedQueryCancellation(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	fixture := testdb.Require(t, testdb.Config{Availability: testdb.FailWhenUnavailable, MaxConns: 16})
	pool := fixture.Pool()
	workspaceID, artifactID, versionID := seedCommittedSourceVersion(t, ctx, pool, "76500000", "f")
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)

	blocker, err := pool.DB().Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Release()
	blockerTx, err := blocker.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blockerTx.Rollback(context.Background()) }()
	if _, err := blockerTx.Exec(ctx, `LOCK TABLE ingestion.parse_projection IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	baselineAcquired := pool.DB().Stat().AcquiredConns()

	root, err := pool.GORM()
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewGORMRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	spanID := mustID(t, "76550000-0000-4000-8000-000000000001")
	write := domain.ProjectionWrite{
		SourceVersionID: versionID,
		Projection: domain.ParseProjection{
			ID: mustID(t, "76560000-0000-4000-8000-000000000001"), WorkspaceID: workspaceID,
			ContentArtifactID: artifactID, ParserID: "goldmark", ParserVersion: "1.8.4",
			ParserConfigHash: strings.Repeat("b", 64), SchemaVersion: "v1",
			NormalizedContentHash: strings.Repeat("c", 64), CreatedAt: now,
		},
		Spans: []domain.SourceSpan{{
			ID: spanID, SpanType: "paragraph", StartLine: 1, EndLine: 1, StartByte: 0, EndByte: 4,
			ExcerptHash: strings.Repeat("d", 64), ParserVersion: "1.8.4", SchemaVersion: "v1",
		}},
		Chunks: []domain.CanonicalChunk{{
			ID: mustID(t, "76570000-0000-4000-8000-000000000001"), Sequence: 0, Content: "test",
			ContentHash: strings.Repeat("e", 64), SourceSpanID: spanID, ByteCount: 4, RuneCount: 4,
			ParserVersion: "1.8.4", ChunkStrategyVersion: "v1", SchemaVersion: "v1", Status: "active",
		}},
		CreatedAt: now,
	}
	queryCtx, cancel := context.WithCancelCause(ctx)
	cancelCause := errors.New("ingestion blocked query cancellation")
	result := make(chan error, 1)
	go func() {
		_, saveErr := repository.SaveProjection(queryCtx, write)
		result <- saveErr
	}()
	waitForBlockedIngestionStatement(t, ctx, pool)
	cancel(cancelCause)
	select {
	case saveErr := <-result:
		if !classifiedAs(saveErr, foundation.ErrorNonRetryableFailure) {
			t.Fatalf("blocked SaveProjection() error = %#v", saveErr)
		}
		if !errors.Is(saveErr, context.Canceled) || !errors.Is(saveErr, cancelCause) {
			t.Fatalf("blocked SaveProjection() lost sentinel or cause: %v", saveErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocked SaveProjection did not return after cancellation")
	}
	if err := blockerTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	connectionDeadline := time.Now().Add(2 * time.Second)
	for pool.DB().Stat().AcquiredConns() > baselineAcquired && time.Now().Before(connectionDeadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if acquired := pool.DB().Stat().AcquiredConns(); acquired != baselineAcquired {
		t.Fatalf("shared pool acquired connections=%d after cancellation, want baseline %d", acquired, baselineAcquired)
	}
	var one int
	if err := pool.DB().QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil || one != 1 {
		t.Fatalf("shared pool connection was not reusable: one=%d err=%v", one, err)
	}
}

// waitForBlockedIngestionStatement 条件等待本数据库内出现锁等待中的活动查询。
func waitForBlockedIngestionStatement(t *testing.T, ctx context.Context, pool *platformpostgres.Pool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var blocked bool
		err := pool.DB().QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE datname=current_database() AND pid <> pg_backend_pid()
				AND wait_event_type='Lock' AND state='active'
		)`).Scan(&blocked)
		if err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("blocked ingestion statement was not observed in pg_stat_activity")
}

func TestGORMRepositorySaveProjectionStatementBounds(t *testing.T) {
	ctx := context.Background()
	fixture := testdb.Require(t, testdb.Config{Availability: testdb.FailWhenUnavailable, MaxConns: 16})
	pool := fixture.Pool()
	root, err := pool.GORM()
	if err != nil {
		t.Fatal(err)
	}
	counter := &ingestionStatementCounter{}
	repository, err := NewGORMRepository(root.Session(&gorm.Session{Logger: counter}))
	if err != nil {
		t.Fatal(err)
	}
	const volume = 5000
	// content_artifact 不可变且 byte_size 必须覆盖全部 Span 的 end_byte，
	// 因此不复用固定 byte_size=4 的 seed helper，直接以足够容量播种。
	workspaceID, artifactID, versionID := seedIngestionStatementBoundsFixture(t, ctx, pool, "76000000", volume*4)
	now := time.Date(2026, 7, 17, 5, 0, 0, 0, time.UTC)

	spans := make([]domain.SourceSpan, 0, volume)
	chunks := make([]domain.CanonicalChunk, 0, volume)
	for i := 0; i < volume; i++ {
		spanID := foundation.ID(fmt.Sprintf("76100000-0000-4000-8000-%012x", i+1))
		spans = append(spans, domain.SourceSpan{
			ID: spanID, SpanType: "paragraph", StartLine: int32(i + 1), EndLine: int32(i + 1),
			StartByte: int64(i * 4), EndByte: int64(i*4 + 4),
			ExcerptHash: fmt.Sprintf("%064x", i+1), ParserVersion: "1.8.4", SchemaVersion: "v1",
		})
		chunks = append(chunks, domain.CanonicalChunk{
			ID: foundation.ID(fmt.Sprintf("76200000-0000-4000-8000-%012x", i+1)), Sequence: int32(i),
			Content: fmt.Sprintf("chunk-%d", i), ContentHash: fmt.Sprintf("%064x", volume+i+1),
			SourceSpanID: spanID, ByteCount: 4, RuneCount: 4,
			ParserVersion: "1.8.4", ChunkStrategyVersion: "v1", SchemaVersion: "v1", Status: "active",
		})
	}
	write := domain.ProjectionWrite{
		SourceVersionID: versionID,
		Projection: domain.ParseProjection{
			ID: mustID(t, "76300000-0000-4000-8000-000000000001"), WorkspaceID: workspaceID,
			ContentArtifactID: artifactID, ParserID: "goldmark", ParserVersion: "1.8.4",
			ParserConfigHash: strings.Repeat("b", 64), SchemaVersion: "v1",
			NormalizedContentHash: strings.Repeat("c", 64), CreatedAt: now,
		},
		Spans: spans, Chunks: chunks, CreatedAt: now,
	}

	// 新建路径固定语句：INSERT projection、INSERT provenance、SELECT spans、
	// SELECT chunks 共 4 条，外加按 500 行分批的 Span/Chunk INSERT。
	const newFixedStatements = 4
	wantBatches := int64((volume + projectionBatchSize - 1) / projectionBatchSize)
	counter.reset()
	created, err := repository.SaveProjection(ctx, write)
	if err != nil || !created.Created {
		t.Fatalf("SaveProjection(new) = %#v, %v", created, err)
	}
	newStatements := counter.statements()
	if newStatements < 2*wantBatches {
		t.Errorf("SaveProjection(new) statements = %d, 低于分批下界 %d", newStatements, 2*wantBatches)
	}
	if bound := int64(newFixedStatements) + 2*wantBatches; newStatements > bound {
		t.Errorf("SaveProjection(new) statements = %d, 超过上界 Cnew(%d)+%d+%d = %d", newStatements, newFixedStatements, wantBatches, wantBatches, bound)
	}

	// 复用路径：同一 contract 换 Projection ID，Span/Chunk 已存在，无批次写入；
	// 固定语句为 INSERT projection 冲突无行、SELECT by contract、INSERT provenance
	// DoNothing、SELECT spans、SELECT chunks 共 5 条。
	const reuseFixedStatements = 5
	write.Projection.ID = mustID(t, "76300000-0000-4000-8000-000000000002")
	counter.reset()
	reused, err := repository.SaveProjection(ctx, write)
	if err != nil || reused.Created || reused.Projection.ID != created.Projection.ID {
		t.Fatalf("SaveProjection(reuse) = %#v, %v", reused, err)
	}
	if len(reused.Spans) != volume || len(reused.Chunks) != volume {
		t.Fatalf("SaveProjection(reuse) spans/chunks = %d/%d, want %d", len(reused.Spans), len(reused.Chunks), volume)
	}
	if reuseStatements := counter.statements(); reuseStatements > reuseFixedStatements {
		t.Errorf("SaveProjection(reuse) statements = %d, 超过上界 Creuse(%d)", reuseStatements, reuseFixedStatements)
	}
}

// ingestionStatementCounter 只统计 GORM 执行的 statement 数，不记录 SQL 文本、
// 参数或行数，满足批量写入性能证据的脱敏要求。
type ingestionStatementCounter struct {
	count atomic.Int64
}

func (counter *ingestionStatementCounter) LogMode(gormlogger.LogLevel) gormlogger.Interface {
	return counter
}

func (counter *ingestionStatementCounter) Info(context.Context, string, ...any)  {}
func (counter *ingestionStatementCounter) Warn(context.Context, string, ...any)  {}
func (counter *ingestionStatementCounter) Error(context.Context, string, ...any) {}

func (counter *ingestionStatementCounter) Trace(context.Context, time.Time, func() (string, int64), error) {
	counter.count.Add(1)
}

func (counter *ingestionStatementCounter) reset() {
	counter.count.Store(0)
}

func (counter *ingestionStatementCounter) statements() int64 {
	return counter.count.Load()
}

// seedIngestionStatementBoundsFixture 以指定的 artifact byte_size 播种
// Workspace/Artifact/Source/Version，支撑大体积 Span 的 statement 上界测量。
func seedIngestionStatementBoundsFixture(t *testing.T, ctx context.Context, pool *platformpostgres.Pool, prefix string, byteSize int64) (foundation.ID, foundation.ID, foundation.ID) {
	t.Helper()
	workspaceID := mustID(t, prefix+"-0000-4000-8000-000000000001")
	artifactID := mustID(t, prefix+"-0000-4000-8000-000000000002")
	sourceID := mustID(t, prefix+"-0000-4000-8000-000000000003")
	versionID := mustID(t, prefix+"-0000-4000-8000-000000000004")
	now := time.Date(2026, 7, 17, 1, 0, 0, 0, time.UTC)
	hash := strings.Repeat("f", 64)
	root := "/tmp/ingestion-" + prefix
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO core.workspace(
			id,name,root_path,root_fingerprint,binding_version,git_repository_path,git_checked_at,
			status,availability,availability_reason,availability_checked_at,version,created_at,updated_at
		) VALUES($1,$2,$3,$4,1,$3,$5,'inactive','available',NULL,$5,1,$5,$5)`, []any{
			string(workspaceID), prefix, root, hash, now,
		}},
		{`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at) VALUES($1,$2,$3,$4,$5,$6)`, []any{string(artifactID), string(workspaceID), hash, byteSize, ".knowledge/sources/" + hash, now}},
		{`INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at) VALUES($1,$2,'text',$3,$3,$4)`, []any{string(sourceID), string(workspaceID), prefix + ".txt", now}},
		{`INSERT INTO core.source_version(id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at) VALUES($1,$2,$3,$4,$5,$6,'text/plain',$7,'pending',$8)`, []any{string(versionID), string(sourceID), string(workspaceID), string(artifactID), hash, byteSize, prefix + ".txt", now}},
	}
	for _, statement := range statements {
		if _, err := pool.DB().Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	return workspaceID, artifactID, versionID
}

func integrationRepository(t *testing.T) (*Repository, pgx.Tx, context.Context) {
	t.Helper()
	ctx := context.Background()
	fixture := testdb.Require(t, testdb.Config{Availability: testdb.FailWhenUnavailable, MaxConns: 16})
	pool := fixture.Pool()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Errorf("rollback legacy integration transaction: %v", err)
		}
	})
	repository, err := NewRepository(tx)
	if err != nil {
		t.Fatal(err)
	}
	return repository, tx, ctx
}

func integrationRepositoryForVariant(t *testing.T, variant string, pool *platformpostgres.Pool) domain.Repository {
	t.Helper()
	switch variant {
	case "legacy":
		repository, err := NewRepository(pool.DB())
		if err != nil {
			t.Fatal(err)
		}
		return repository
	case "gorm":
		root, err := pool.GORM()
		if err != nil {
			t.Fatal(err)
		}
		repository, err := NewGORMRepository(root)
		if err != nil {
			t.Fatal(err)
		}
		return repository
	default:
		t.Fatalf("unknown repository variant %q", variant)
		return nil
	}
}

func seedCommittedSourceVersion(t *testing.T, ctx context.Context, pool *platformpostgres.Pool, prefix, hashCharacter string) (foundation.ID, foundation.ID, foundation.ID) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	committed := false
	t.Cleanup(func() {
		if !committed {
			if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
				t.Errorf("rollback committed-source seed transaction: %v", err)
			}
		}
	})
	workspaceID, artifactID, versionID := seedSourceVersion(t, ctx, tx, prefix, hashCharacter)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	committed = true
	return workspaceID, artifactID, versionID
}

func assertRowCount(t *testing.T, ctx context.Context, pool *platformpostgres.Pool, want int, query string, arguments ...any) {
	t.Helper()
	var actual int
	if err := pool.DB().QueryRow(ctx, query, arguments...).Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if actual != want {
		t.Fatalf("row count query %q = %d, want %d", query, actual, want)
	}
}

func assertInTransactionRowCount(t *testing.T, ctx context.Context, count ingestionCount, want int, query string, arguments ...any) {
	t.Helper()
	actual, err := count(ctx, query, arguments...)
	if err != nil {
		t.Fatal(err)
	}
	if actual != want {
		t.Fatalf("transaction row count query %q = %d, want %d", query, actual, want)
	}
}

type ingestionExec func(context.Context, string, ...any) error
type ingestionCount func(context.Context, string, ...any) (int, error)

func runIngestionRepositoryVariants(t *testing.T, scenario func(*testing.T, domain.Repository, ingestionExec, ingestionCount, context.Context)) {
	t.Helper()
	for _, variant := range []string{"legacy", "gorm"} {
		t.Run(variant, func(t *testing.T) {
			ctx := context.Background()
			fixture := testdb.Require(t, testdb.Config{Availability: testdb.FailWhenUnavailable, MaxConns: 16})
			pool := fixture.Pool()
			var repository domain.Repository
			var exec ingestionExec
			var count ingestionCount
			var rollback func()
			switch variant {
			case "legacy":
				tx, err := pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				repository, err = NewRepository(tx)
				if err != nil {
					t.Fatal(err)
				}
				exec = func(execCtx context.Context, query string, args ...any) error {
					_, err := tx.Exec(execCtx, rebindIngestionQuery(query), args...)
					return err
				}
				count = func(queryCtx context.Context, query string, args ...any) (int, error) {
					var result int
					err := tx.QueryRow(queryCtx, rebindIngestionQuery(query), args...).Scan(&result)
					return result, err
				}
				rollback = func() {
					if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
						t.Errorf("rollback legacy integration transaction: %v", err)
					}
				}
			case "gorm":
				root, err := pool.GORM()
				if err != nil {
					t.Fatal(err)
				}
				gormTx := root.WithContext(ctx).Begin()
				if gormTx.Error != nil {
					t.Fatal(gormTx.Error)
				}
				repository, err = NewGORMRepository(gormTx)
				if err != nil {
					t.Fatal(err)
				}
				exec = func(execCtx context.Context, query string, args ...any) error {
					return gormTx.WithContext(execCtx).Exec(query, args...).Error
				}
				count = func(queryCtx context.Context, query string, args ...any) (int, error) {
					var result int
					err := gormTx.WithContext(queryCtx).Raw(query, args...).Scan(&result).Error
					return result, err
				}
				rollback = func() {
					if err := gormTx.Rollback().Error; err != nil {
						t.Errorf("rollback GORM integration transaction: %v", err)
					}
				}
			}
			t.Cleanup(rollback)
			scenario(t, repository, exec, count, ctx)
		})
	}
}

func rebindIngestionQuery(query string) string {
	var builder strings.Builder
	argument := 1
	for _, character := range query {
		if character == '?' {
			_, _ = fmt.Fprintf(&builder, "$%d", argument)
			argument++
			continue
		}
		builder.WriteRune(character)
	}
	return builder.String()
}

func seedWorkflowExec(t *testing.T, ctx context.Context, exec ingestionExec, workspaceID, definitionID, workflowRunID foundation.ID, now time.Time) {
	t.Helper()
	if err := exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at) VALUES(?,?, 'ingestion-test',1,'{}',?)`, string(definitionID), string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	if err := exec(ctx, `INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at) VALUES(?,?,?,'pending','{}',1,?,?)`, string(workflowRunID), string(workspaceID), string(definitionID), now, now); err != nil {
		t.Fatal(err)
	}
}

func seedSourceVersionExec(t *testing.T, ctx context.Context, exec ingestionExec, prefix, hashCharacter string) (foundation.ID, foundation.ID, foundation.ID) {
	t.Helper()
	workspaceID := mustID(t, prefix+"-0000-4000-8000-000000000001")
	artifactID := mustID(t, prefix+"-0000-4000-8000-000000000002")
	sourceID := mustID(t, prefix+"-0000-4000-8000-000000000003")
	versionID := mustID(t, prefix+"-0000-4000-8000-000000000004")
	now := time.Date(2026, 7, 17, 1, 0, 0, 0, time.UTC)
	hash := strings.Repeat(hashCharacter, 64)
	root := "/tmp/ingestion-" + prefix
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO core.workspace(
			id,name,root_path,root_fingerprint,binding_version,git_repository_path,git_checked_at,
			status,availability,availability_reason,availability_checked_at,version,created_at,updated_at
		) VALUES(?,?,?, ?,1,?,?,'inactive','available',NULL,?,1,?,?)`, []any{
			string(workspaceID), prefix, root, strings.Repeat(hashCharacter, 64), root, now, now, now, now,
		}},
		{`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at) VALUES(?,?,?,4,?,?)`, []any{string(artifactID), string(workspaceID), hash, ".knowledge/sources/" + hash, now}},
		{`INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at) VALUES(?,?, 'text',?, ?,?)`, []any{string(sourceID), string(workspaceID), prefix + ".txt", prefix + ".txt", now}},
		{`INSERT INTO core.source_version(id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at) VALUES(?,?,?,?,?,4,'text/plain',?,'pending',?)`, []any{string(versionID), string(sourceID), string(workspaceID), string(artifactID), hash, prefix + ".txt", now}},
	}
	for _, statement := range statements {
		if err := exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	return workspaceID, artifactID, versionID
}

func seedSourceVersion(t *testing.T, ctx context.Context, tx pgx.Tx, prefix, hashCharacter string) (foundation.ID, foundation.ID, foundation.ID) {
	t.Helper()
	workspaceID := mustID(t, prefix+"-0000-4000-8000-000000000001")
	artifactID := mustID(t, prefix+"-0000-4000-8000-000000000002")
	sourceID := mustID(t, prefix+"-0000-4000-8000-000000000003")
	versionID := mustID(t, prefix+"-0000-4000-8000-000000000004")
	now := time.Date(2026, 7, 17, 1, 0, 0, 0, time.UTC)
	hash := strings.Repeat(hashCharacter, 64)
	root := "/tmp/ingestion-" + prefix
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO core.workspace(
			id,name,root_path,root_fingerprint,binding_version,git_repository_path,git_checked_at,
			status,availability,availability_reason,availability_checked_at,version,created_at,updated_at
		) VALUES($1,$2,$3,$4,1,$3,$5,'inactive','available',NULL,$5,1,$5,$5)`, []any{
			string(workspaceID), prefix, root, strings.Repeat(hashCharacter, 64), now,
		}},
		{`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at) VALUES($1,$2,$3,4,$4,$5)`, []any{string(artifactID), string(workspaceID), hash, ".knowledge/sources/" + hash, now}},
		{`INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at) VALUES($1,$2,'text',$3,$3,$4)`, []any{string(sourceID), string(workspaceID), prefix + ".txt", now}},
		{`INSERT INTO core.source_version(id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at) VALUES($1,$2,$3,$4,$5,4,'text/plain',$6,'pending',$7)`, []any{string(versionID), string(sourceID), string(workspaceID), string(artifactID), hash, prefix + ".txt", now}},
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	return workspaceID, artifactID, versionID
}

func mustID(t *testing.T, value string) foundation.ID {
	t.Helper()
	id, err := foundation.ParseID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func classifiedAs(err error, kind foundation.ErrorKind) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Kind == kind
}

func classifiedWithCode(err error, kind foundation.ErrorKind, code string) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Kind == kind && classified.Code == code
}
