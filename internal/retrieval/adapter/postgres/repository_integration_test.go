//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
)

func TestRepositoryFTSOnlyBuildReadyActivateAndReplay(t *testing.T) {
	for _, implementation := range []string{"gorm"} {
		t.Run(implementation, func(t *testing.T) {
			repository, database, ctx := newRetrievalTestStore(t, implementation)
			now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
			workspaceID, chunk := seedRetrievalChunk(t, ctx, database.DB(), "81000000", false, now)
			build := retrievalBuild(t, workspaceID, "82000000-0000-4000-8000-000000000001", nil, "fts-build", chunk, now)

			created, err := repository.BeginIndex(ctx, build)
			if err != nil || !created.Created {
				t.Fatalf("BeginIndex() = %#v, %v", created, err)
			}
			replayBuild := retrievalBuild(t, workspaceID, "82000000-0000-4000-8000-000000000002", nil, "fts-build", chunk, now.Add(time.Second))
			replayed, err := repository.BeginIndex(ctx, replayBuild)
			if err != nil || !replayed.Replayed || replayed.IndexVersion.ID != created.IndexVersion.ID {
				t.Fatalf("BeginIndex(replay) = %#v, %v", replayed, err)
			}

			lexicalCommand := domain.LexicalBuildCommand{
				WorkspaceID: workspaceID, IndexVersionID: created.IndexVersion.ID,
				ExpectedIndexVersion: 1, At: now.Add(2 * time.Second),
			}
			lexical, err := repository.BuildLexical(ctx, lexicalCommand)
			if err != nil || lexical.InsertedCount != 1 || lexical.ReplayedCount != 0 {
				t.Fatalf("BuildLexical() = %#v, %v", lexical, err)
			}
			lexicalReplay, err := repository.BuildLexical(ctx, lexicalCommand)
			if err != nil || lexicalReplay.InsertedCount != 0 || lexicalReplay.ReplayedCount != 1 {
				t.Fatalf("BuildLexical(replay) = %#v, %v", lexicalReplay, err)
			}

			service, err := application.NewService(application.Dependencies{
				Store: repository, IDs: foundation.NewUUIDGenerator(nil),
				Clock: foundation.FixedClock{Value: now.Add(3 * time.Second)},
			})
			if err != nil {
				t.Fatal(err)
			}
			ready, err := service.Ready(ctx, application.TransitionRequest{
				WorkspaceID: workspaceID, IndexVersionID: created.IndexVersion.ID, ExpectedVersion: 1,
			})
			if err != nil || ready.Status != domain.IndexStatusReady || ready.Version != 2 {
				t.Fatalf("Ready() = %#v, %v", ready, err)
			}
			activationService, err := application.NewService(application.Dependencies{
				Store: repository,
				IDs: &retrievalSequenceIDs{values: []foundation.ID{
					"83000000-0000-4000-8000-000000000001",
					"83000000-0000-4000-8000-000000000002",
				}},
				Clock: foundation.FixedClock{Value: now.Add(4 * time.Second)},
			})
			if err != nil {
				t.Fatal(err)
			}
			activateRequest := application.ActivateRequest{
				WorkspaceID: workspaceID, TargetIndexVersionID: ready.ID, ExpectedTargetVersion: ready.Version,
				IdempotencyKey: "activate-fts", ReasonCode: "BUILD_VERIFIED",
			}
			activated, err := activationService.Activate(ctx, activateRequest)
			if err != nil || activated.ActiveIndexVersion.Status != domain.IndexStatusActive || activated.Replayed {
				t.Fatalf("Activate() = %#v, %v", activated, err)
			}
			activationReplay, err := activationService.Activate(ctx, activateRequest)
			if err != nil || !activationReplay.Replayed || activationReplay.Activation.ID != activated.Activation.ID {
				t.Fatalf("Activate(replay) = %#v, %v", activationReplay, err)
			}
			active, err := repository.GetActive(ctx, workspaceID)
			if err != nil || active.ID != ready.ID || active.Version != 3 {
				t.Fatalf("GetActive() = %#v, %v", active, err)
			}
			assertExplainUsesIndex(t, ctx, database.DB(), "idx_retrieval_projection_search_vector",
				`SELECT chunk_id FROM retrieval.chunk_projection WHERE search_vector @@ plainto_tsquery('simple','alpha')`)
			assertExplainUsesIndex(t, ctx, database.DB(), "idx_ingestion_canonical_chunk_content_trgm",
				`SELECT id FROM ingestion.canonical_chunk WHERE content % 'alpha'`)
			assertExplainUsesIndex(t, ctx, database.DB(), "uq_retrieval_index_version_active",
				`SELECT id FROM retrieval.index_version WHERE workspace_id=$1 AND status='active'`, string(workspaceID))
		})
	}
}

func TestRepositoryHybridVectorBatchReadyAndReplay(t *testing.T) {
	for _, implementation := range []string{"gorm"} {
		t.Run(implementation, func(t *testing.T) {
			repository, database, ctx := newRetrievalTestStore(t, implementation)
			now := time.Date(2026, 7, 17, 11, 0, 0, 0, time.UTC)
			workspaceID, chunk := seedRetrievalChunk(t, ctx, database.DB(), "84000000", false, now)
			embeddingID := foundation.ID("85000000-0000-4000-8000-000000000001")
			embedding, err := repository.RegisterEmbeddingVersion(ctx, domain.EmbeddingVersion{
				ID: embeddingID, Provider: "openai", AdapterName: "compatible", AdapterVersion: "v1",
				Model: "embed-test", Dimensions: 3, Normalization: domain.NormalizationL2,
				DistanceMetric: domain.DistanceCosine, ConfigHash: strings.Repeat("a", 64), CreatedAt: now,
			})
			if err != nil || !embedding.Created {
				t.Fatalf("RegisterEmbeddingVersion() = %#v, %v", embedding, err)
			}
			build := retrievalBuild(t, workspaceID, "86000000-0000-4000-8000-000000000001", &embeddingID, "hybrid-build", chunk, now)
			created, err := repository.BeginIndex(ctx, build)
			if err != nil {
				t.Fatal(err)
			}
			command := domain.LexicalBuildCommand{
				WorkspaceID: workspaceID, IndexVersionID: created.IndexVersion.ID,
				ExpectedIndexVersion: 1, At: now.Add(time.Second),
			}
			if _, err := repository.BuildLexical(ctx, command); err != nil {
				t.Fatal(err)
			}
			var tokenCount int32
			if err := database.QueryRow(ctx, `SELECT token_count FROM retrieval.chunk_projection WHERE index_version_id=$1 AND chunk_id=$2`, string(created.IndexVersion.ID), string(chunk.ChunkID)).Scan(&tokenCount); err != nil {
				t.Fatal(err)
			}
			_, err = database.DB().Exec(ctx, `UPDATE retrieval.chunk_projection
				SET embedding=$1,vector_status='ready',updated_at=$2
				WHERE index_version_id=$3 AND chunk_id=$4`, pgvector.NewVector([]float32{1, 2}), now.Add(2*time.Second), string(created.IndexVersion.ID), string(chunk.ChunkID))
			var pgErr *pgconn.PgError
			if err == nil || !errors.As(err, &pgErr) || pgErr.Code != "23514" {
				t.Fatalf("dimension constraint error=%v", err)
			}
			batch := domain.VectorProjectionBatch{
				WorkspaceID: workspaceID, IndexVersionID: created.IndexVersion.ID, EmbeddingVersionID: embeddingID,
				ExpectedIndexVersion: 1, At: now.Add(2 * time.Second),
				Projections: []domain.VectorProjectionWrite{{
					ChunkID: chunk.ChunkID, Embedding: []float32{1, 0, 0}, TokenCount: tokenCount,
					VectorStatus: domain.VectorStatusReady,
				}},
			}
			skipped := batch
			skipped.Projections = []domain.VectorProjectionWrite{{
				ChunkID: chunk.ChunkID, TokenCount: tokenCount, VectorStatus: domain.VectorStatusSkippedOversized,
				FailureCode: "VECTOR_INPUT_OVERSIZED",
			}}
			if _, err := repository.SaveVectorBatch(ctx, skipped); !retrievalErrorKind(err, foundation.ErrorConsistencyViolation) {
				t.Fatalf("non-oversized skip error=%#v", err)
			}
			vector, err := repository.SaveVectorBatch(ctx, batch)
			if err != nil || vector.InsertedCount != 1 || vector.ReplayedCount != 0 {
				t.Fatalf("SaveVectorBatch() = %#v, %v", vector, err)
			}
			vectorReplay, err := repository.SaveVectorBatch(ctx, batch)
			if err != nil || vectorReplay.InsertedCount != 0 || vectorReplay.ReplayedCount != 1 {
				t.Fatalf("SaveVectorBatch(replay) = %#v, %v", vectorReplay, err)
			}
			if lexicalReplay, err := repository.BuildLexical(ctx, command); err != nil || lexicalReplay.ReplayedCount != 1 {
				t.Fatalf("BuildLexical(after vector) = %#v, %v", lexicalReplay, err)
			}
			batch.Projections[0].TokenCount++
			if _, err := repository.SaveVectorBatch(ctx, batch); !retrievalErrorKind(err, foundation.ErrorVersionConflict) {
				t.Fatalf("token count conflict = %#v", err)
			}
			service, err := application.NewService(application.Dependencies{
				Store: repository, IDs: foundation.NewUUIDGenerator(nil),
				Clock: foundation.FixedClock{Value: now.Add(3 * time.Second)},
			})
			if err != nil {
				t.Fatal(err)
			}
			ready, err := service.Ready(ctx, application.TransitionRequest{
				WorkspaceID: workspaceID, IndexVersionID: created.IndexVersion.ID, ExpectedVersion: 1,
			})
			if err != nil || ready.Status != domain.IndexStatusReady || len(ready.DegradedCapabilities) != 0 {
				t.Fatalf("Ready(hybrid) = %#v, %v", ready, err)
			}
		})
	}
}

func TestRepositoryEmbeddingIdentityConflictWinsOverExistingContractReplay(t *testing.T) {
	repository, _, ctx := newRetrievalTestRepository(t)
	now := time.Date(2026, 7, 17, 11, 30, 0, 0, time.UTC)
	first := domain.EmbeddingVersion{
		ID: "85100000-0000-4000-8000-000000000001", Provider: "openai", AdapterName: "compatible",
		AdapterVersion: "v1", Model: "embed-a", Dimensions: 3, Normalization: domain.NormalizationL2,
		DistanceMetric: domain.DistanceCosine, ConfigHash: strings.Repeat("1", 64), CreatedAt: now,
	}
	second := first
	second.ID = "85100000-0000-4000-8000-000000000002"
	second.Model = "embed-b"
	second.ConfigHash = strings.Repeat("2", 64)
	if _, err := repository.RegisterEmbeddingVersion(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RegisterEmbeddingVersion(ctx, second); err != nil {
		t.Fatal(err)
	}
	if replay, err := repository.RegisterEmbeddingVersion(ctx, first); err != nil || !replay.Replayed || replay.EmbeddingVersion.ID != first.ID {
		t.Fatalf("same ID embedding replay = %#v, %v", replay, err)
	}
	secondReplay := second
	secondReplay.ID = "85100000-0000-4000-8000-000000000003"
	if replay, err := repository.RegisterEmbeddingVersion(ctx, secondReplay); err != nil || !replay.Replayed || replay.EmbeddingVersion.ID != second.ID {
		t.Fatalf("new ID contract replay = %#v, %v", replay, err)
	}
	conflicting := second
	conflicting.ID = first.ID
	if _, err := repository.RegisterEmbeddingVersion(ctx, conflicting); !retrievalErrorKind(err, foundation.ErrorConsistencyViolation) {
		t.Fatalf("same ID with another existing contract error=%#v", err)
	}
}

func TestRepositorySkippedVectorFailureCodeIsImmutableAndReadyIsDegraded(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	now := time.Date(2026, 7, 17, 11, 45, 0, 0, time.UTC)
	workspaceID, chunk := seedRetrievalChunk(t, ctx, database.DB(), "85200000", true, now)
	embeddingID := foundation.ID("85300000-0000-4000-8000-000000000001")
	if _, err := repository.RegisterEmbeddingVersion(ctx, domain.EmbeddingVersion{
		ID: embeddingID, Provider: "openai", AdapterName: "compatible", AdapterVersion: "v1",
		Model: "embed-oversized", Dimensions: 3, Normalization: domain.NormalizationL2,
		DistanceMetric: domain.DistanceCosine, ConfigHash: strings.Repeat("3", 64), CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	created, err := repository.BeginIndex(ctx, retrievalBuild(t, workspaceID, "85400000-0000-4000-8000-000000000001", &embeddingID, "oversized", chunk, now))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.BuildLexical(ctx, domain.LexicalBuildCommand{
		WorkspaceID: workspaceID, IndexVersionID: created.IndexVersion.ID,
		ExpectedIndexVersion: 1, At: now.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	var tokenCount int32
	if err := database.QueryRow(ctx, `SELECT token_count FROM retrieval.chunk_projection WHERE index_version_id=$1 AND chunk_id=$2`, string(created.IndexVersion.ID), string(chunk.ChunkID)).Scan(&tokenCount); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SaveVectorBatch(ctx, domain.VectorProjectionBatch{
		WorkspaceID: workspaceID, IndexVersionID: created.IndexVersion.ID, EmbeddingVersionID: embeddingID,
		ExpectedIndexVersion: 1, At: now.Add(2 * time.Second),
		Projections: []domain.VectorProjectionWrite{{
			ChunkID: chunk.ChunkID, TokenCount: tokenCount, VectorStatus: domain.VectorStatusSkippedOversized,
			FailureCode: "VECTOR_INPUT_OVERSIZED",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	_, err = database.DB().Exec(ctx, `UPDATE retrieval.chunk_projection SET failure_code='DIFFERENT_CODE',updated_at=$1 WHERE index_version_id=$2 AND chunk_id=$3`, now.Add(3*time.Second), string(created.IndexVersion.ID), string(chunk.ChunkID))
	var pgErr *pgconn.PgError
	if err == nil || !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("terminal failure code mutation error=%v", err)
	}
	service, err := application.NewService(application.Dependencies{
		Store: repository, IDs: foundation.NewUUIDGenerator(nil),
		Clock: foundation.FixedClock{Value: now.Add(4 * time.Second)},
	})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := service.Ready(ctx, application.TransitionRequest{
		WorkspaceID: workspaceID, IndexVersionID: created.IndexVersion.ID, ExpectedVersion: 1,
	})
	if err != nil || !domain.HasDegradedCapability(ready.DegradedCapabilities, domain.DegradedVector) {
		t.Fatalf("Ready(oversized) = %#v, %v", ready, err)
	}
}

func TestRepositoryVectorBatchRollsBackEveryRowWhenOneWriteFails(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	now := time.Date(2026, 7, 17, 11, 50, 0, 0, time.UTC)
	workspaceID, firstChunk := seedRetrievalChunk(t, ctx, database.DB(), "85500000", false, now)
	secondChunk := seedAdditionalRetrievalChunk(t, ctx, database.DB(), firstChunk, "85500000-0000-4000-8000-000000000006", 1, now)
	embeddingID := foundation.ID("85600000-0000-4000-8000-000000000001")
	if _, err := repository.RegisterEmbeddingVersion(ctx, domain.EmbeddingVersion{
		ID: embeddingID, Provider: "openai", AdapterName: "compatible", AdapterVersion: "v1",
		Model: "embed-batch", Dimensions: 3, Normalization: domain.NormalizationL2,
		DistanceMetric: domain.DistanceCosine, ConfigHash: strings.Repeat("4", 64), CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	created, err := repository.BeginIndex(ctx, retrievalBuildForChunks(t, workspaceID, "85700000-0000-4000-8000-000000000001", &embeddingID, "batch-rollback", []domain.ManifestChunk{firstChunk, secondChunk}, now))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.BuildLexical(ctx, domain.LexicalBuildCommand{
		WorkspaceID: workspaceID, IndexVersionID: created.IndexVersion.ID,
		ExpectedIndexVersion: 1, At: now.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	tokens := make(map[foundation.ID]int32, 2)
	rows, err := database.DB().Query(ctx, `SELECT chunk_id::text,token_count FROM retrieval.chunk_projection WHERE index_version_id=$1`, string(created.IndexVersion.ID))
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id foundation.ID
		var token int32
		if err := rows.Scan(&id, &token); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		tokens[id] = token
	}
	rows.Close()
	_, err = repository.SaveVectorBatch(ctx, domain.VectorProjectionBatch{
		WorkspaceID: workspaceID, IndexVersionID: created.IndexVersion.ID, EmbeddingVersionID: embeddingID,
		ExpectedIndexVersion: 1, At: now.Add(2 * time.Second),
		Projections: []domain.VectorProjectionWrite{
			{ChunkID: firstChunk.ChunkID, TokenCount: tokens[firstChunk.ChunkID], Embedding: []float32{1, 0, 0}, VectorStatus: domain.VectorStatusReady},
			{ChunkID: secondChunk.ChunkID, TokenCount: tokens[secondChunk.ChunkID], VectorStatus: domain.VectorStatusSkippedOversized, FailureCode: "VECTOR_INPUT_OVERSIZED"},
		},
	})
	if !retrievalErrorKind(err, foundation.ErrorConsistencyViolation) {
		t.Fatalf("mixed vector batch error=%#v", err)
	}
	var pending, withEmbedding int
	if err := database.QueryRow(ctx, `SELECT count(*) FILTER(WHERE vector_status='pending'),count(*) FILTER(WHERE embedding IS NOT NULL) FROM retrieval.chunk_projection WHERE index_version_id=$1`, string(created.IndexVersion.ID)).Scan(&pending, &withEmbedding); err != nil {
		t.Fatal(err)
	}
	if pending != 2 || withEmbedding != 0 {
		t.Fatalf("partial vector batch persisted pending=%d with_embedding=%d", pending, withEmbedding)
	}
}

func TestRepositoryConcurrentReplacementAndRollbackActivation(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	workspaceID, chunk := seedRetrievalChunk(t, ctx, database.DB(), "87000000", false, now)
	first := createReadyIndex(t, ctx, repository, workspaceID, chunk, "88000000-0000-4000-8000-000000000001", "first", now)
	firstActivation, err := repository.Activate(ctx, domain.ActivationCommand{
		ActivationID: "89000000-0000-4000-8000-000000000001", WorkspaceID: workspaceID,
		TargetIndexVersionID: first.ID, ExpectedTargetVersion: first.Version,
		IdempotencyKey: "activate-first", ReasonCode: "BUILD_VERIFIED", At: now.Add(3 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	second := createReadyIndex(t, ctx, repository, workspaceID, chunk, "88000000-0000-4000-8000-000000000002", "second", now.Add(4*time.Second))
	third := createReadyIndex(t, ctx, repository, workspaceID, chunk, "88000000-0000-4000-8000-000000000003", "third", now.Add(4*time.Second))
	currentID := firstActivation.ActiveIndexVersion.ID
	currentVersion := firstActivation.ActiveIndexVersion.Version
	type activationOutcome struct {
		result domain.ActivationResult
		err    error
	}
	outcomes := make(chan activationOutcome, 2)
	for index, target := range []domain.IndexVersion{second, third} {
		go func(index int, target domain.IndexVersion) {
			result, activateErr := repository.Activate(ctx, domain.ActivationCommand{
				ActivationID: foundation.ID(fmt.Sprintf("8a000000-0000-4000-8000-%012d", index+1)),
				WorkspaceID:  workspaceID, TargetIndexVersionID: target.ID, ExpectedTargetVersion: target.Version,
				ExpectedCurrentIndexVersionID: &currentID, ExpectedCurrentVersion: &currentVersion,
				IdempotencyKey: fmt.Sprintf("activate-contender-%d", index+1), ReasonCode: "BUILD_VERIFIED",
				At: now.Add(7 * time.Second),
			})
			outcomes <- activationOutcome{result: result, err: activateErr}
		}(index, target)
	}
	var winner domain.ActivationResult
	var succeeded, conflicted int
	for range 2 {
		outcome := <-outcomes
		if outcome.err == nil {
			succeeded++
			winner = outcome.result
		} else if retrievalErrorKind(outcome.err, foundation.ErrorVersionConflict) {
			conflicted++
		} else {
			t.Fatalf("unexpected concurrent activation error: %v", outcome.err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent activation succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	var activeCount int
	if err := database.QueryRow(ctx, `SELECT count(*) FROM retrieval.index_version WHERE workspace_id=$1 AND status='active'`, string(workspaceID)).Scan(&activeCount); err != nil || activeCount != 1 {
		t.Fatalf("active count=%d, err=%v", activeCount, err)
	}
	rollbackService, err := application.NewService(application.Dependencies{
		Store: repository,
		IDs: &retrievalSequenceIDs{values: []foundation.ID{
			"8b000000-0000-4000-8000-000000000001",
			"8b000000-0000-4000-8000-000000000002",
		}},
		Clock: foundation.FixedClock{Value: now.Add(8 * time.Second)},
	})
	if err != nil {
		t.Fatal(err)
	}
	rollbackRequest := application.RollbackActivateRequest{
		WorkspaceID: workspaceID, TargetIndexVersionID: first.ID,
		ExpectedTargetVersion:         firstActivation.ActiveIndexVersion.Version + 1,
		ExpectedCurrentIndexVersionID: winner.ActiveIndexVersion.ID,
		ExpectedCurrentVersion:        winner.ActiveIndexVersion.Version,
		IdempotencyKey:                "rollback-first", ReasonCode: "REGRESSION_ROLLBACK",
	}
	rolledBack, err := rollbackService.RollbackActivate(ctx, rollbackRequest)
	if err != nil || rolledBack.ActiveIndexVersion.ID != first.ID || rolledBack.ActiveIndexVersion.Status != domain.IndexStatusActive {
		t.Fatalf("RollbackActivate() = %#v, %v", rolledBack, err)
	}
	rollbackReplay, err := rollbackService.RollbackActivate(ctx, rollbackRequest)
	if err != nil || !rollbackReplay.Replayed || rollbackReplay.Activation.ID != rolledBack.Activation.ID {
		t.Fatalf("RollbackActivate(replay) = %#v, %v", rollbackReplay, err)
	}
	delayedService, err := application.NewService(application.Dependencies{
		Store: repository,
		IDs: &retrievalSequenceIDs{values: []foundation.ID{
			"8b000000-0000-4000-8000-000000000003",
		}},
		Clock: foundation.FixedClock{Value: now.Add(9 * time.Second)},
	})
	if err != nil {
		t.Fatal(err)
	}
	delayedCurrentID := winner.PreviousIndexVersion.ID
	delayedCurrentVersion := winner.PreviousIndexVersion.Version - 1
	delayed, err := delayedService.Activate(ctx, application.ActivateRequest{
		WorkspaceID: workspaceID, TargetIndexVersionID: winner.ActiveIndexVersion.ID,
		ExpectedTargetVersion:         winner.ActiveIndexVersion.Version - 1,
		ExpectedCurrentIndexVersionID: &delayedCurrentID, ExpectedCurrentVersion: &delayedCurrentVersion,
		IdempotencyKey: winner.Activation.IdempotencyKey, ReasonCode: winner.Activation.ReasonCode,
	})
	if err != nil || !delayed.Replayed || delayed.ActiveIndexVersion.Status != domain.IndexStatusActive ||
		delayed.ActiveIndexVersion.Version != winner.ActiveIndexVersion.Version || delayed.PreviousIndexVersion == nil ||
		delayed.PreviousIndexVersion.Status != domain.IndexStatusRetiring || delayed.PreviousIndexVersion.Version != winner.PreviousIndexVersion.Version {
		t.Fatalf("delayed Activate replay = %#v, %v", delayed, err)
	}
	_, err = repository.RollbackActivate(ctx, domain.RollbackActivationCommand{
		ActivationID: "8b000000-0000-4000-8000-000000000004", WorkspaceID: workspaceID,
		TargetIndexVersionID: winner.ActiveIndexVersion.ID, ExpectedTargetVersion: winner.ActiveIndexVersion.Version + 1,
		ExpectedCurrentIndexVersionID: rolledBack.ActiveIndexVersion.ID, ExpectedCurrentVersion: rolledBack.ActiveIndexVersion.Version,
		IdempotencyKey: winner.Activation.IdempotencyKey, ReasonCode: winner.Activation.ReasonCode, At: now.Add(10 * time.Second),
	})
	if !retrievalErrorKind(err, foundation.ErrorVersionConflict) {
		t.Fatalf("cross-kind activation replay error=%#v", err)
	}
}

func TestRepositoryDatabaseRejectsCrossWorkspaceAndImmutableWrites(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	now := time.Date(2026, 7, 17, 13, 0, 0, 0, time.UTC)
	workspaceID, chunk := seedRetrievalChunk(t, ctx, database.DB(), "8c000000", false, now)
	otherWorkspaceID, _ := seedRetrievalChunk(t, ctx, database.DB(), "8d000000", false, now)
	ready := createReadyIndex(t, ctx, repository, workspaceID, chunk, "8e000000-0000-4000-8000-000000000001", "ownership", now)

	_, err := database.DB().Exec(ctx, `UPDATE retrieval.index_manifest_chunk SET content_hash=$1 WHERE index_version_id=$2 AND chunk_id=$3`, strings.Repeat("9", 64), string(ready.ID), string(chunk.ChunkID))
	var pgErr *pgconn.PgError
	if err == nil || !errors.As(err, &pgErr) || pgErr.Code != "55000" {
		t.Fatalf("immutable manifest error=%v", err)
	}
	_, err = database.DB().Exec(ctx, `INSERT INTO retrieval.chunk_projection(
		index_version_id,chunk_id,workspace_id,embedding_version_id,search_vector,embedding,token_count,
		lexical_status,vector_status,failure_code,created_at,updated_at)
		VALUES($1,$2,$3,NULL,to_tsvector('simple','cross workspace'),NULL,2,'ready','disabled',NULL,$4,$4)`,
		string(ready.ID), string(chunk.ChunkID), string(otherWorkspaceID), now.Add(3*time.Second))
	if err == nil || !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("cross-workspace projection error=%v", err)
	}
	_, err = database.DB().Exec(ctx, `INSERT INTO retrieval.index_activation(
		id,kind,workspace_id,target_index_version_id,previous_index_version_id,target_version,previous_version,idempotency_key,reason_code,created_at)
		VALUES($1,'activate',$2,$3,NULL,$4,NULL,'cross-workspace','INVALID',$5)`,
		"8f000000-0000-4000-8000-000000000001", string(otherWorkspaceID), string(ready.ID), ready.Version+1, now.Add(4*time.Second))
	if err == nil || !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("cross-workspace activation error=%v", err)
	}
}

func TestRepositoryDatabaseValidatesActivationReceiptReplayAndInitialBinding(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	now := time.Date(2026, 7, 17, 13, 30, 0, 0, time.UTC)
	workspaceID, chunk := seedRetrievalChunk(t, ctx, database.DB(), "90000000", false, now)
	first := createReadyIndex(t, ctx, repository, workspaceID, chunk, "91000000-0000-4000-8000-000000000001", "receipt-first", now)
	activated, err := repository.Activate(ctx, domain.ActivationCommand{
		ActivationID: "92000000-0000-4000-8000-000000000001", WorkspaceID: workspaceID,
		TargetIndexVersionID: first.ID, ExpectedTargetVersion: first.Version,
		IdempotencyKey: "receipt-replay", ReasonCode: "BUILD_VERIFIED", At: now.Add(3 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt := activated.Activation
	_, err = database.DB().Exec(ctx, `INSERT INTO retrieval.index_activation(
		id,kind,workspace_id,target_index_version_id,previous_index_version_id,target_version,previous_version,idempotency_key,reason_code,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING`,
		"92000000-0000-4000-8000-000000000002", string(receipt.Kind), string(receipt.WorkspaceID),
		string(receipt.TargetIndexVersionID), receipt.PreviousIndexVersionID, receipt.TargetVersion,
		receipt.PreviousVersion, receipt.IdempotencyKey, receipt.ReasonCode, receipt.CreatedAt)
	if err != nil {
		t.Fatalf("exact activation receipt replay error=%v", err)
	}
	_, err = database.DB().Exec(ctx, `INSERT INTO retrieval.index_activation(
		id,kind,workspace_id,target_index_version_id,previous_index_version_id,target_version,previous_version,idempotency_key,reason_code,created_at)
		VALUES($1,'rollback',$2,$3,NULL,$4,NULL,$5,$6,$7) ON CONFLICT DO NOTHING`,
		"92000000-0000-4000-8000-000000000003", string(workspaceID), string(first.ID),
		receipt.TargetVersion, receipt.IdempotencyKey, receipt.ReasonCode, now.Add(4*time.Second))
	var pgErr *pgconn.PgError
	if err == nil || !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("activation receipt kind conflict error=%v", err)
	}

	second := createReadyIndex(t, ctx, repository, workspaceID, chunk, "91000000-0000-4000-8000-000000000002", "receipt-second", now.Add(5*time.Second))
	_, err = database.DB().Exec(ctx, `INSERT INTO retrieval.index_activation(
		id,kind,workspace_id,target_index_version_id,previous_index_version_id,target_version,previous_version,idempotency_key,reason_code,created_at)
		VALUES($1,'activate',$2,$3,NULL,$4,NULL,$5,$6,$7)`,
		"92000000-0000-4000-8000-000000000004", string(workspaceID), string(second.ID), second.Version+1,
		"false-initial-activation", "BUILD_VERIFIED", now.Add(8*time.Second))
	if err == nil || !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("initial activation with existing active error=%v", err)
	}
}

type retrievalTestRepository struct {
	*GORMRepository
	*GORMCompletionRepository
}

func newRetrievalTestRepository(t *testing.T, connectionLimits ...int32) (*retrievalTestRepository, *platformpostgres.Pool, context.Context) {
	t.Helper()
	maxConnections := int32(8)
	if len(connectionLimits) > 0 {
		maxConnections = connectionLimits[0]
	}
	fixture := testdb.Require(t, testdb.Config{
		ExternalAdminURL: strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL")),
		Availability:     testdb.FailWhenUnavailable, MaxConns: maxConnections,
	})
	database := fixture.Pool()
	repository, err := NewGORMRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	completion := newCompletionTestStore(t, database, repository).(*GORMCompletionRepository)
	return &retrievalTestRepository{GORMRepository: repository, GORMCompletionRepository: completion}, database, t.Context()
}

type retrievalTestStore interface {
	application.Store
	application.VectorBuildStore
	application.RegressionStore
}

func newRetrievalTestStore(t *testing.T, implementation string, connectionLimits ...int32) (retrievalTestStore, *platformpostgres.Pool, context.Context) {
	t.Helper()
	if implementation != "gorm" {
		t.Fatalf("unsupported retrieval implementation %q", implementation)
	}
	repository, database, ctx := newRetrievalTestRepository(t, connectionLimits...)
	return repository.GORMRepository, database, ctx
}

func seedRetrievalChunk(t *testing.T, ctx context.Context, database *pgxpool.Pool, prefix string, oversized bool, now time.Time) (foundation.ID, domain.ManifestChunk) {
	t.Helper()
	workspaceID := foundation.ID(prefix + "-0000-4000-8000-000000000001")
	artifactID := foundation.ID(prefix + "-0000-4000-8000-000000000002")
	projectionID := foundation.ID(prefix + "-0000-4000-8000-000000000003")
	spanID := foundation.ID(prefix + "-0000-4000-8000-000000000004")
	chunkID := foundation.ID(prefix + "-0000-4000-8000-000000000005")
	content := "alpha beta beta"
	artifactHash := strings.Repeat("b", 64)
	contentHash := strings.Repeat("c", 64)
	batch := &pgx.Batch{}
	batch.Queue(`INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
		VALUES($1,$2,$3,$3,$4,'inactive',1,$4,$4)`, string(workspaceID), "retrieval-"+prefix, "/tmp/retrieval-"+prefix, now)
	batch.Queue(`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at)
		VALUES($1,$2,$3,$4,'.knowledge/sources/' || $3,$5)`, string(artifactID), string(workspaceID), artifactHash, int64(len(content)), now)
	batch.Queue(`INSERT INTO ingestion.parse_projection(id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,created_at)
		VALUES($1,$2,$3,'goldmark','v1',$4,'v1',$5,$6)`, string(projectionID), string(workspaceID), string(artifactID), strings.Repeat("d", 64), strings.Repeat("e", 64), now)
	batch.Queue(`INSERT INTO ingestion.source_span(id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,excerpt_hash,parser_version,schema_version,created_at)
		VALUES($1,$2,$3,$4,'paragraph',1,1,0,$5,$6,'v1','v1',$7)`, string(spanID), string(workspaceID), string(artifactID), string(projectionID), int64(len(content)), strings.Repeat("f", 64), now)
	batch.Queue(`INSERT INTO ingestion.canonical_chunk(id,workspace_id,parse_projection_id,sequence,heading_path,content,content_hash,source_span_id,byte_count,rune_count,parser_version,chunk_strategy_version,schema_version,atomic_oversized,status,created_at)
		VALUES($1,$2,$3,0,'["Heading"]',$4,$5,$6,$7,$7,'v1','structure-v1','v1',$8,'active',$9)`, string(chunkID), string(workspaceID), string(projectionID), content, contentHash, string(spanID), int64(len(content)), oversized, now)
	results := database.SendBatch(ctx, batch)
	for range 5 {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			t.Fatal(err)
		}
	}
	if err := results.Close(); err != nil {
		t.Fatal(err)
	}
	return workspaceID, domain.ManifestChunk{
		ChunkID: chunkID, WorkspaceID: workspaceID, ContentHash: contentHash, Sequence: 0,
		ParserVersion: "v1", ChunkStrategyVersion: "structure-v1", SchemaVersion: "v1", CreatedAt: now,
	}
}

func retrievalBuild(t *testing.T, workspaceID foundation.ID, indexID string, embeddingID *foundation.ID, idempotencyKey string, chunk domain.ManifestChunk, now time.Time) domain.IndexBuild {
	return retrievalBuildForChunks(t, workspaceID, indexID, embeddingID, idempotencyKey, []domain.ManifestChunk{chunk}, now)
}

func retrievalBuildForChunks(t *testing.T, workspaceID foundation.ID, indexID string, embeddingID *foundation.ID, idempotencyKey string, chunks []domain.ManifestChunk, now time.Time) domain.IndexBuild {
	t.Helper()
	indexVersionID := foundation.ID(indexID)
	manifestInput := make([]domain.ManifestChunk, len(chunks))
	for index, chunk := range chunks {
		chunk.IndexVersionID = indexVersionID
		chunk.CreatedAt = now
		manifestInput[index] = chunk
	}
	manifest, manifestHash, err := domain.CanonicalizeManifest(workspaceID, indexVersionID, manifestInput)
	if err != nil {
		t.Fatal(err)
	}
	degraded := []domain.DegradedCapability(nil)
	if embeddingID == nil {
		degraded = []domain.DegradedCapability{domain.DegradedVector}
	}
	return domain.IndexBuild{IndexVersion: domain.IndexVersion{
		ID: indexVersionID, WorkspaceID: workspaceID, EmbeddingVersionID: embeddingID,
		TokenizerID: "postgres-simple", TokenizerVersion: "v1", TokenizerConfigHash: strings.Repeat("1", 64),
		FusionConfig: []byte(`{"method":"rrf","k":60}`), SourceSnapshotRef: "fixture:" + idempotencyKey,
		ManifestHash: manifestHash, ExpectedChunkCount: int64(len(manifest)), IdempotencyKey: idempotencyKey,
		Status: domain.IndexStatusBuilding, DegradedCapabilities: degraded, Version: 1, CreatedAt: now, UpdatedAt: now,
	}, Manifest: manifest}
}

func seedAdditionalRetrievalChunk(t *testing.T, ctx context.Context, database *pgxpool.Pool, first domain.ManifestChunk, chunkID foundation.ID, sequence int32, now time.Time) domain.ManifestChunk {
	t.Helper()
	content := "gamma delta"
	contentHash := strings.Repeat("7", 64)
	if _, err := database.Exec(ctx, `INSERT INTO ingestion.canonical_chunk(
		id,workspace_id,parse_projection_id,sequence,heading_path,content,content_hash,source_span_id,
		byte_count,rune_count,parser_version,chunk_strategy_version,schema_version,atomic_oversized,status,created_at)
		SELECT $1,workspace_id,parse_projection_id,$2,'["Second"]',$3,$4,source_span_id,$5,$5,
			parser_version,chunk_strategy_version,schema_version,false,'active',$6
		FROM ingestion.canonical_chunk WHERE id=$7`, string(chunkID), sequence, content, contentHash, int64(len(content)), now, string(first.ChunkID)); err != nil {
		t.Fatal(err)
	}
	return domain.ManifestChunk{
		ChunkID: chunkID, WorkspaceID: first.WorkspaceID, ContentHash: contentHash, Sequence: sequence,
		ParserVersion: first.ParserVersion, ChunkStrategyVersion: first.ChunkStrategyVersion,
		SchemaVersion: first.SchemaVersion, CreatedAt: now,
	}
}

func createReadyIndex(t *testing.T, ctx context.Context, repository application.Store, workspaceID foundation.ID, chunk domain.ManifestChunk, indexID, key string, now time.Time) domain.IndexVersion {
	t.Helper()
	created, err := repository.BeginIndex(ctx, retrievalBuild(t, workspaceID, indexID, nil, key, chunk, now))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.BuildLexical(ctx, domain.LexicalBuildCommand{
		WorkspaceID: workspaceID, IndexVersionID: created.IndexVersion.ID,
		ExpectedIndexVersion: 1, At: now.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	service, err := application.NewService(application.Dependencies{
		Store: repository, IDs: foundation.NewUUIDGenerator(nil),
		Clock: foundation.FixedClock{Value: now.Add(2 * time.Second)},
	})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := service.Ready(ctx, application.TransitionRequest{
		WorkspaceID: workspaceID, IndexVersionID: created.IndexVersion.ID, ExpectedVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ready
}

func assertExplainUsesIndex(t *testing.T, ctx context.Context, database *pgxpool.Pool, indexName, query string, arguments ...any) {
	t.Helper()
	tx, err := database.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan=off`); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, "EXPLAIN (COSTS OFF) "+query, arguments...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line)
		plan.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.String(), indexName) {
		t.Fatalf("EXPLAIN did not use %s:\n%s", indexName, plan.String())
	}
	t.Logf("EXPLAIN uses %s:\n%s", indexName, plan.String())
}

func retrievalErrorKind(err error, kind foundation.ErrorKind) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Kind == kind
}

type retrievalSequenceIDs struct {
	values []foundation.ID
	next   int
}

func (g *retrievalSequenceIDs) New() (foundation.ID, error) {
	value := g.values[g.next]
	g.next++
	return value, nil
}
