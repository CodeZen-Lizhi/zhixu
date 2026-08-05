//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositoryBeginWorkspaceSnapshotSelectsLatestEligibleSourcesAndReplays(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	now := time.Date(2026, 7, 18, 1, 0, 0, 0, time.UTC)
	workspaceID := seedSnapshotWorkspace(t, ctx, database.DB(), 1, now)
	target := seedSnapshotSourceVersion(t, ctx, database.DB(), workspaceID, 10, 1, true, now, now, 1)
	seedSnapshotChunkWithStrategy(t, ctx, database.DB(), target, 999, "legacy-v0", "v1", now)
	older := seedSnapshotSourceVersion(t, ctx, database.DB(), workspaceID, 20, 1, true, now.Add(-2*time.Hour), now.Add(-2*time.Hour), 1)
	latest := seedSnapshotSourceVersionForSource(t, ctx, database.DB(), workspaceID, older.SourceID, 21, true, now.Add(-time.Hour), now.Add(-time.Hour), 1)
	excluded := seedSnapshotSourceVersion(t, ctx, database.DB(), workspaceID, 30, 1, false, now, now, 0)
	shared := seedSnapshotSourceSharingProjection(t, ctx, database.DB(), workspaceID, 31, target, now)

	command := snapshotCommand(workspaceID, 100, target, "snapshot-first", now.Add(time.Minute))
	command.PageSize = 1
	created, err := repository.BeginWorkspaceSnapshot(ctx, command)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			t.Logf("snapshot cause: %s", pgErr.Error())
		}
		t.Fatal(err)
	}
	if !created.Created || created.SourceCount != 4 || created.ChunkCount != 2 || created.ExcludedSourceCount != 1 {
		t.Fatalf("BeginWorkspaceSnapshot()=%#v", created)
	}
	assertSnapshotSource(t, ctx, database.DB(), created.IndexVersion.ID, target.SourceID, target.VersionID, target.ProjectionID, "included")
	assertSnapshotSource(t, ctx, database.DB(), created.IndexVersion.ID, latest.SourceID, latest.VersionID, latest.ProjectionID, "included")
	assertSnapshotSource(t, ctx, database.DB(), created.IndexVersion.ID, excluded.SourceID, "", "", "excluded")
	assertSnapshotSource(t, ctx, database.DB(), created.IndexVersion.ID, shared.SourceID, shared.VersionID, shared.ProjectionID, "included")

	replayCommand := command
	replayCommand.IndexVersion.ID = snapshotID(101)
	replayCommand.IndexVersion.CreatedAt = replayCommand.IndexVersion.CreatedAt.Add(time.Hour)
	replayCommand.IndexVersion.UpdatedAt = replayCommand.IndexVersion.CreatedAt
	replayCommand.IndexVersion.FusionConfig = []byte(`{"k":60,"method":"rrf"}`)
	replayed, err := repository.BeginWorkspaceSnapshot(ctx, replayCommand)
	if err != nil || !replayed.Replayed || replayed.IndexVersion.ID != created.IndexVersion.ID {
		t.Fatalf("BeginWorkspaceSnapshot(replay)=%#v, %v", replayed, err)
	}
	mutations := []func(*domain.ProcessingContract){
		func(value *domain.ProcessingContract) { value.ParserID = "other-parser" },
		func(value *domain.ProcessingContract) { value.ParserVersion = "v2" },
		func(value *domain.ProcessingContract) { value.ParserConfigHash = strings.Repeat("e", 64) },
		func(value *domain.ProcessingContract) { value.ChunkStrategyVersion = "structure-v2" },
		func(value *domain.ProcessingContract) { value.SchemaVersion = "v2" },
	}
	for index, mutate := range mutations {
		conflicting := replayCommand
		contract := *replayCommand.IndexVersion.ProcessingContract
		mutate(&contract)
		conflicting.IndexVersion.ProcessingContract = &contract
		conflicting.IndexVersion.ID = snapshotID(150 + index)
		if _, err := repository.BeginWorkspaceSnapshot(ctx, conflicting); !retrievalErrorCode(err, "REINDEX_SNAPSHOT_IDEMPOTENCY_CONFLICT") {
			t.Fatalf("processing contract mutation %d error=%#v", index, err)
		}
	}
}

func TestRepositoryBeginWorkspaceSnapshotPersistsHybridBindingAndPendingVectors(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	now := time.Date(2026, 7, 18, 1, 20, 0, 0, time.UTC)
	workspaceID := seedSnapshotWorkspace(t, ctx, database.DB(), 8, now)
	target := seedSnapshotSourceVersion(t, ctx, database.DB(), workspaceID, 32, 1, true, now, now, 2)
	embedding := domain.EmbeddingVersion{
		ID: snapshotID(310), Provider: "openai", AdapterName: "compatible", AdapterVersion: "v1", Model: "embed-v1",
		Dimensions: 3, Normalization: domain.NormalizationL2, DistanceMetric: domain.DistanceCosine,
		ConfigHash: strings.Repeat("9", 64), CreatedAt: now,
	}
	if _, err := repository.RegisterEmbeddingVersion(ctx, embedding); err != nil {
		t.Fatal(err)
	}
	command := snapshotCommand(workspaceID, 311, target, "snapshot-hybrid", now.Add(time.Minute))
	command.IndexVersion.EmbeddingVersionID = &embedding.ID
	command.IndexVersion.DegradedCapabilities = nil
	fusion, err := domain.CanonicalRRFConfig(domain.RRFConfig{
		SchemaVersion: domain.RRFFusionSchemaVersionV1, Method: domain.FusionMethodRRF, K: 60,
		LexicalCandidateLimit: 200, VectorCandidateLimit: 200, FusedCandidateLimit: 200, RerankCandidateLimit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	command.IndexVersion.FusionConfig = fusion
	created, err := repository.BeginWorkspaceSnapshot(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if created.IndexVersion.EmbeddingVersionID == nil || *created.IndexVersion.EmbeddingVersionID != embedding.ID ||
		len(created.IndexVersion.DegradedCapabilities) != 0 || created.ChunkCount != 2 {
		t.Fatalf("hybrid snapshot=%#v", created)
	}
	if _, err := repository.BuildLexical(ctx, domain.LexicalBuildCommand{
		WorkspaceID: workspaceID, IndexVersionID: created.IndexVersion.ID, ExpectedIndexVersion: 1, At: now.Add(2 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	var pending int64
	if err := database.QueryRow(ctx, `SELECT count(*) FROM retrieval.chunk_projection
		WHERE index_version_id=$1 AND embedding_version_id=$2 AND lexical_status='ready' AND vector_status='pending'`,
		string(created.IndexVersion.ID), string(embedding.ID)).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != created.ChunkCount {
		t.Fatalf("pending vectors=%d want=%d", pending, created.ChunkCount)
	}

	replay := command
	replay.IndexVersion.ID = snapshotID(312)
	replay.IndexVersion.CreatedAt = replay.IndexVersion.CreatedAt.Add(time.Hour)
	replay.IndexVersion.UpdatedAt = replay.IndexVersion.CreatedAt
	replayed, err := repository.BeginWorkspaceSnapshot(ctx, replay)
	if err != nil || !replayed.Replayed || replayed.IndexVersion.ID != created.IndexVersion.ID {
		t.Fatalf("hybrid replay=%#v err=%v", replayed, err)
	}
	otherEmbeddingID := snapshotID(313)
	replay.IndexVersion.EmbeddingVersionID = &otherEmbeddingID
	if _, err := repository.BeginWorkspaceSnapshot(ctx, replay); !retrievalErrorCode(err, "REINDEX_SNAPSHOT_IDEMPOTENCY_CONFLICT") {
		t.Fatalf("embedding mutation error=%#v", err)
	}
	replay = command
	replay.IndexVersion.ID = snapshotID(314)
	replay.IndexVersion.FusionConfig, err = domain.CanonicalRRFConfig(domain.RRFConfig{
		SchemaVersion: domain.RRFFusionSchemaVersionV1, Method: domain.FusionMethodRRF, K: 61,
		LexicalCandidateLimit: 200, VectorCandidateLimit: 200, FusedCandidateLimit: 200, RerankCandidateLimit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.BeginWorkspaceSnapshot(ctx, replay); !retrievalErrorCode(err, "REINDEX_SNAPSHOT_IDEMPOTENCY_CONFLICT") {
		t.Fatalf("fusion mutation error=%#v", err)
	}
	replay = command
	replay.IndexVersion.ID = snapshotID(315)
	replay.IndexVersion.EmbeddingVersionID = nil
	replay.IndexVersion.DegradedCapabilities = []domain.DegradedCapability{domain.DegradedVector}
	replay.IndexVersion.FusionConfig = []byte(`{"method":"fts_only"}`)
	if _, err := repository.BeginWorkspaceSnapshot(ctx, replay); !retrievalErrorCode(err, "REINDEX_SNAPSHOT_IDEMPOTENCY_CONFLICT") {
		t.Fatalf("hybrid-to-v1 mutation error=%#v", err)
	}
}

func TestRepositoryBeginWorkspaceSnapshotIncrementallyReplacesTargetsWithoutLosingSources(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	now := time.Date(2026, 7, 18, 1, 30, 0, 0, time.UTC)
	workspaceID := seedSnapshotWorkspace(t, ctx, database.DB(), 3, now)
	firstA := seedSnapshotSourceVersion(t, ctx, database.DB(), workspaceID, 50, 1, true, now, now, 1)
	seedSnapshotChunkWithStrategy(t, ctx, database.DB(), firstA, 998, "legacy-v0", "v1", now)
	firstB := seedSnapshotSourceVersion(t, ctx, database.DB(), workspaceID, 60, 1, true, now, now, 1)

	first := mustCreateAndActivateSnapshot(t, ctx, repository, snapshotCommand(workspaceID, 120, firstA, "snapshot-incremental-a1", now.Add(time.Minute)), 200, now.Add(2*time.Minute))
	assertSnapshotSource(t, ctx, database.DB(), first.ID, firstA.SourceID, firstA.VersionID, firstA.ProjectionID, "included")
	assertSnapshotSource(t, ctx, database.DB(), first.ID, firstB.SourceID, firstB.VersionID, firstB.ProjectionID, "included")

	secondB := seedSnapshotSourceVersionForSource(t, ctx, database.DB(), workspaceID, firstB.SourceID, 61, true, now.Add(3*time.Minute), now.Add(3*time.Minute), 1)
	second := mustCreateAndActivateSnapshot(t, ctx, repository, snapshotCommand(workspaceID, 121, secondB, "snapshot-incremental-b2", now.Add(4*time.Minute)), 201, now.Add(5*time.Minute))
	assertSnapshotSource(t, ctx, database.DB(), second.ID, firstA.SourceID, firstA.VersionID, firstA.ProjectionID, "included")
	assertSnapshotSource(t, ctx, database.DB(), second.ID, secondB.SourceID, secondB.VersionID, secondB.ProjectionID, "included")

	secondA := seedSnapshotSourceVersionForSource(t, ctx, database.DB(), workspaceID, firstA.SourceID, 51, true, now.Add(6*time.Minute), now.Add(6*time.Minute), 1)
	thirdCommand := snapshotCommand(workspaceID, 122, secondA, "snapshot-incremental-a2", now.Add(7*time.Minute))
	thirdCommand.PageSize = 1
	third := mustCreateAndActivateSnapshot(t, ctx, repository, thirdCommand, 202, now.Add(8*time.Minute))
	assertSnapshotSource(t, ctx, database.DB(), third.ID, secondA.SourceID, secondA.VersionID, secondA.ProjectionID, "included")
	assertSnapshotSource(t, ctx, database.DB(), third.ID, secondB.SourceID, secondB.VersionID, secondB.ProjectionID, "included")
}

func TestRepositoryBeginWorkspaceSnapshotBatchTargetsRemoveSources(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	now := time.Date(2026, 8, 4, 9, 0, 0, 0, time.UTC)
	workspaceID := seedSnapshotWorkspace(t, ctx, database.DB(), 701, now)
	firstA := seedSnapshotSourceVersion(t, ctx, database.DB(), workspaceID, 702, 1, true, now, now, 1)
	firstB := seedSnapshotSourceVersion(t, ctx, database.DB(), workspaceID, 703, 1, true, now, now, 1)
	firstC := seedSnapshotSourceVersion(t, ctx, database.DB(), workspaceID, 704, 1, true, now, now, 1)
	first := mustCreateAndActivateSnapshot(t, ctx, repository,
		snapshotCommand(workspaceID, 705, firstA, "batch-targets-first", now.Add(time.Minute)), 706, now.Add(2*time.Minute))
	assertSnapshotSource(t, ctx, database.DB(), first.ID, firstA.SourceID, firstA.VersionID, firstA.ProjectionID, "included")
	assertSnapshotSource(t, ctx, database.DB(), first.ID, firstB.SourceID, firstB.VersionID, firstB.ProjectionID, "included")
	assertSnapshotSource(t, ctx, database.DB(), first.ID, firstC.SourceID, firstC.VersionID, firstC.ProjectionID, "included")

	secondA := seedSnapshotSourceVersionForSource(t, ctx, database.DB(), workspaceID, firstA.SourceID, 707, true, now.Add(3*time.Minute), now.Add(3*time.Minute), 1)
	secondC := seedSnapshotSourceVersionForSource(t, ctx, database.DB(), workspaceID, firstC.SourceID, 708, true, now.Add(3*time.Minute), now.Add(3*time.Minute), 1)
	command := snapshotCommand(workspaceID, 709, secondA, "batch-targets-second", now.Add(4*time.Minute))
	command.TargetSourceID = ""
	command.TargetSourceVersionID = ""
	command.TargetParseProjectionID = ""
	command.Targets = []domain.SnapshotTarget{
		{SourceID: secondA.SourceID, SourceVersionID: secondA.VersionID, ParseProjectionID: secondA.ProjectionID},
		{SourceID: secondC.SourceID, SourceVersionID: secondC.VersionID, ParseProjectionID: secondC.ProjectionID},
	}
	command.RemovedSourceIDs = []foundation.ID{firstB.SourceID}
	second := mustCreateAndActivateSnapshot(t, ctx, repository, command, 710, now.Add(5*time.Minute))
	assertSnapshotSource(t, ctx, database.DB(), second.ID, secondA.SourceID, secondA.VersionID, secondA.ProjectionID, "included")
	assertSnapshotSource(t, ctx, database.DB(), second.ID, secondC.SourceID, secondC.VersionID, secondC.ProjectionID, "included")
	var removedCount int
	if err := database.QueryRow(ctx, `SELECT count(*) FROM retrieval.index_manifest_source
		WHERE index_version_id=$1 AND source_id=$2`, string(second.ID), string(firstB.SourceID)).Scan(&removedCount); err != nil {
		t.Fatal(err)
	}
	if removedCount != 0 {
		t.Fatalf("removed source persisted in batch snapshot: %d", removedCount)
	}
}

func TestRepositoryBeginWorkspaceSnapshotExcludesTombstonedSourcesFromIncrementalBase(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	workspaceID := seedSnapshotWorkspace(t, ctx, database.DB(), 720, now)
	firstA := seedSnapshotSourceVersion(t, ctx, database.DB(), workspaceID, 721, 1, true, now, now, 1)
	firstB := seedSnapshotSourceVersion(t, ctx, database.DB(), workspaceID, 722, 1, true, now, now, 1)
	_ = mustCreateAndActivateSnapshot(t, ctx, repository,
		snapshotCommand(workspaceID, 723, firstA, "tombstone-incremental-first", now.Add(time.Minute)), 724, now.Add(2*time.Minute))
	if _, err := database.DB().Exec(ctx, `UPDATE core.source SET removed_at=$1 WHERE id=$2`, now.Add(3*time.Minute), string(firstB.SourceID)); err != nil {
		t.Fatal(err)
	}
	secondA := seedSnapshotSourceVersionForSource(t, ctx, database.DB(), workspaceID, firstA.SourceID, 725, true,
		now.Add(4*time.Minute), now.Add(4*time.Minute), 1)
	created, err := repository.BeginWorkspaceSnapshot(ctx,
		snapshotCommand(workspaceID, 726, secondA, "tombstone-incremental-second", now.Add(5*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	assertSnapshotSource(t, ctx, database.DB(), created.IndexVersion.ID, secondA.SourceID, secondA.VersionID, secondA.ProjectionID, "included")
	var tombstonedCount int
	if err := database.DB().QueryRow(ctx, `SELECT count(*) FROM retrieval.index_manifest_source WHERE index_version_id=$1 AND source_id=$2`,
		string(created.IndexVersion.ID), string(firstB.SourceID)).Scan(&tombstonedCount); err != nil {
		t.Fatal(err)
	}
	if tombstonedCount != 0 || created.SourceCount != 1 {
		t.Fatalf("tombstoned source leaked into incremental snapshot: count=%d result=%#v", tombstonedCount, created)
	}

	_, err = repository.BeginWorkspaceSnapshot(ctx,
		snapshotCommand(workspaceID, 727, firstB, "tombstone-target", now.Add(6*time.Minute)))
	if !retrievalErrorCode(err, "REINDEX_TARGET_PROJECTION_INVALID") {
		t.Fatalf("tombstoned target error=%v", err)
	}
}

func TestRepositoryBeginWorkspaceSnapshotRebuildsAllSourcesFromLegacyActive(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	now := time.Date(2026, 7, 18, 1, 45, 0, 0, time.UTC)
	workspaceID := seedSnapshotWorkspace(t, ctx, database.DB(), 4, now)
	target := seedSnapshotSourceVersion(t, ctx, database.DB(), workspaceID, 70, 1, true, now, now, 1)
	other := seedSnapshotSourceVersion(t, ctx, database.DB(), workspaceID, 80, 1, true, now, now, 1)
	legacyChunk := snapshotManifestChunk(t, ctx, database.DB(), target)
	legacy := createReadyIndex(t, ctx, repository, workspaceID, legacyChunk, string(snapshotID(130)), "legacy-active", now.Add(time.Minute))
	if _, err := repository.Activate(ctx, domain.ActivationCommand{
		ActivationID: snapshotID(210), WorkspaceID: workspaceID, TargetIndexVersionID: legacy.ID,
		ExpectedTargetVersion: legacy.Version, IdempotencyKey: "activate-legacy", ReasonCode: "BUILD_VERIFIED", At: now.Add(4 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	created, err := repository.BeginWorkspaceSnapshot(ctx, snapshotCommand(workspaceID, 131, target, "snapshot-after-legacy", now.Add(5*time.Minute)))
	if err != nil || created.SourceCount != 2 || created.ChunkCount != 2 {
		t.Fatalf("snapshot after legacy=%#v, %v", created, err)
	}
	assertSnapshotSource(t, ctx, database.DB(), created.IndexVersion.ID, target.SourceID, target.VersionID, target.ProjectionID, "included")
	assertSnapshotSource(t, ctx, database.DB(), created.IndexVersion.ID, other.SourceID, other.VersionID, other.ProjectionID, "included")
}

func TestRepositoryBeginWorkspaceSnapshotRebuildsAllSourcesWhenProcessingContractChanges(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	now := time.Date(2026, 7, 18, 1, 55, 0, 0, time.UTC)
	workspaceID := seedSnapshotWorkspace(t, ctx, database.DB(), 6, now)
	firstA := seedSnapshotSourceVersion(t, ctx, database.DB(), workspaceID, 91, 1, true, now, now, 1)
	firstB := seedSnapshotSourceVersion(t, ctx, database.DB(), workspaceID, 92, 1, true, now, now, 1)
	_ = mustCreateAndActivateSnapshot(t, ctx, repository, snapshotCommand(workspaceID, 141, firstA, "snapshot-contract-v1", now.Add(time.Minute)), 220, now.Add(2*time.Minute))

	contractV2 := domain.ProcessingContract{
		ParserID: "goldmark", ParserVersion: "v2", ParserConfigHash: strings.Repeat("e", 64),
		ChunkStrategyVersion: "structure-v2", SchemaVersion: "v2",
	}
	secondA := seedSnapshotSourceVersionForSourceContract(t, ctx, database.DB(), workspaceID, firstA.SourceID, 93, true,
		now.Add(3*time.Minute), now.Add(3*time.Minute), 1, contractV2)
	command := snapshotCommand(workspaceID, 142, secondA, "snapshot-contract-v2", now.Add(4*time.Minute))
	command.IndexVersion.ProcessingContract = &contractV2
	created, err := repository.BeginWorkspaceSnapshot(ctx, command)
	if err != nil || created.SourceCount != 2 || created.ExcludedSourceCount != 1 || created.ChunkCount != 1 {
		t.Fatalf("contract rebuild=%#v, %v", created, err)
	}
	assertSnapshotSource(t, ctx, database.DB(), created.IndexVersion.ID, secondA.SourceID, secondA.VersionID, secondA.ProjectionID, "included")
	assertSnapshotSource(t, ctx, database.DB(), created.IndexVersion.ID, firstB.SourceID, "", "", "excluded")
}

func TestRepositoryBeginWorkspaceSnapshotCapacityFailureLeavesNoPartialIndex(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	now := time.Date(2026, 7, 18, 2, 0, 0, 0, time.UTC)
	workspaceID := seedSnapshotWorkspace(t, ctx, database.DB(), 2, now)
	target := seedSnapshotSourceVersion(t, ctx, database.DB(), workspaceID, 40, 1, true, now, now, 2)

	command := snapshotCommand(workspaceID, 110, target, "snapshot-capacity", now.Add(time.Minute))
	command.PageSize = 1
	command.MaxChunks = 1
	if _, err := repository.BeginWorkspaceSnapshot(ctx, command); !retrievalErrorCode(err, "REINDEX_SNAPSHOT_CAPACITY_EXCEEDED") {
		t.Fatalf("capacity error=%#v", err)
	}
	var indexes, sourceRows, chunkRows int
	if err := database.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM retrieval.index_version WHERE workspace_id=$1 AND idempotency_key=$2),
		(SELECT count(*) FROM retrieval.index_manifest_source WHERE index_version_id=$3),
		(SELECT count(*) FROM retrieval.index_manifest_chunk WHERE index_version_id=$3)`,
		string(workspaceID), command.IndexVersion.IdempotencyKey, string(command.IndexVersion.ID)).Scan(&indexes, &sourceRows, &chunkRows); err != nil {
		t.Fatal(err)
	}
	if indexes != 0 || sourceRows != 0 || chunkRows != 0 {
		t.Fatalf("partial snapshot persisted indexes=%d sources=%d chunks=%d", indexes, sourceRows, chunkRows)
	}

	seedSnapshotSourceVersion(t, ctx, database.DB(), workspaceID, 41, 1, true, now, now, 1)
	sourceCommand := snapshotCommand(workspaceID, 111, target, "snapshot-source-capacity", now.Add(2*time.Minute))
	sourceCommand.PageSize = 1
	sourceCommand.MaxSources = 1
	if _, err := repository.BeginWorkspaceSnapshot(ctx, sourceCommand); !retrievalErrorCode(err, "REINDEX_SNAPSHOT_CAPACITY_EXCEEDED") {
		t.Fatalf("source capacity error=%#v", err)
	}
	if err := database.QueryRow(ctx, `SELECT count(*) FROM retrieval.index_version WHERE id=$1`, string(sourceCommand.IndexVersion.ID)).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if indexes != 0 {
		t.Fatalf("source capacity persisted partial index count=%d", indexes)
	}
}

func TestRepositoryBeginWorkspaceSnapshotRejectsTargetWithoutSuccessfulCurrentProjection(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	now := time.Date(2026, 7, 18, 2, 30, 0, 0, time.UTC)
	workspaceID := seedSnapshotWorkspace(t, ctx, database.DB(), 5, now)
	target := seedSnapshotSourceVersion(t, ctx, database.DB(), workspaceID, 90, 1, false, now, now, 0)
	command := snapshotCommand(workspaceID, 140, target, "snapshot-invalid-target", now.Add(time.Minute))
	if _, err := repository.BeginWorkspaceSnapshot(ctx, command); !retrievalErrorCode(err, "REINDEX_TARGET_PROJECTION_INVALID") {
		t.Fatalf("target projection error=%#v", err)
	}
	var count int
	if err := database.QueryRow(ctx, `SELECT count(*) FROM retrieval.index_version WHERE id=$1`, string(command.IndexVersion.ID)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("invalid target persisted index count=%d", count)
	}
}

func TestDatabaseRejectsReadyWhenIncludedSourceUsesAnotherProcessingContract(t *testing.T) {
	_, database, ctx := newRetrievalTestRepository(t)
	now := time.Date(2026, 7, 18, 2, 45, 0, 0, time.UTC)
	workspaceID := seedSnapshotWorkspace(t, ctx, database.DB(), 7, now)
	source := seedSnapshotSourceVersion(t, ctx, database.DB(), workspaceID, 94, 1, true, now, now, 1)
	indexID := snapshotID(143)
	if _, err := database.DB().Exec(ctx, `INSERT INTO retrieval.index_version(
		id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
		source_snapshot_ref,manifest_hash,expected_chunk_count,source_manifest_hash,expected_source_count,
		source_parser_id,source_parser_version,source_parser_config_hash,source_chunk_strategy_version,source_schema_version,
		idempotency_key,status,degraded_capabilities,version,created_at,updated_at
	) VALUES($1,$2,'postgres-simple','v1',$3,'{}','reindex-v1:wrong-contract',$4,0,$5,1,
		'goldmark','v2',$6,'structure-v2','v2','wrong-contract-ready','building','["vector"]',1,$7,$7)`,
		string(indexID), string(workspaceID), strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64),
		strings.Repeat("e", 64), now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().Exec(ctx, `INSERT INTO retrieval.index_manifest_source(
		index_version_id,workspace_id,source_id,source_version_id,parse_projection_id,selection_status,created_at
	) VALUES($1,$2,$3,$4,$5,'included',$6)`, string(indexID), string(workspaceID), string(source.SourceID),
		string(source.VersionID), string(source.ProjectionID), now); err != nil {
		t.Fatal(err)
	}
	_, err := database.DB().Exec(ctx, `UPDATE retrieval.index_version SET status='ready',version=2,updated_at=$2 WHERE id=$1`, string(indexID), now.Add(time.Minute))
	var pgErr *pgconn.PgError
	if err == nil || !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("wrong-contract ready error=%v", err)
	}
}

type snapshotSourceFixture struct {
	SourceID     foundation.ID
	VersionID    foundation.ID
	ProjectionID foundation.ID
	ArtifactID   foundation.ID
	ContentHash  string
}

func seedSnapshotWorkspace(t *testing.T, ctx context.Context, database *pgxpool.Pool, ordinal int, now time.Time) foundation.ID {
	t.Helper()
	workspaceID := snapshotID(ordinal)
	root := fmt.Sprintf("/tmp/reindex-snapshot-%d", ordinal)
	if _, err := database.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
	) VALUES($1,$2,$3,$3,$4,'active',1,$4,$4)`, string(workspaceID), fmt.Sprintf("snapshot-%d", ordinal), root, now); err != nil {
		t.Fatal(err)
	}
	return workspaceID
}

func seedSnapshotSourceVersion(
	t *testing.T,
	ctx context.Context,
	database *pgxpool.Pool,
	workspaceID foundation.ID,
	sourceOrdinal, versionOrdinal int,
	successful bool,
	capturedAt, attemptStartedAt time.Time,
	chunkCount int,
) snapshotSourceFixture {
	t.Helper()
	sourceID := snapshotID(sourceOrdinal)
	if _, err := database.Exec(ctx, `INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at)
		VALUES($1,$2,'text',$3,$3,$4)`, string(sourceID), string(workspaceID), fmt.Sprintf("notes/%d.txt", sourceOrdinal), capturedAt); err != nil {
		t.Fatal(err)
	}
	return seedSnapshotSourceVersionForSource(t, ctx, database, workspaceID, sourceID, sourceOrdinal*100+versionOrdinal, successful, capturedAt, attemptStartedAt, chunkCount)
}

func seedSnapshotSourceVersionForSource(
	t *testing.T,
	ctx context.Context,
	database *pgxpool.Pool,
	workspaceID, sourceID foundation.ID,
	ordinal int,
	successful bool,
	capturedAt, attemptStartedAt time.Time,
	chunkCount int,
) snapshotSourceFixture {
	return seedSnapshotSourceVersionForSourceContract(t, ctx, database, workspaceID, sourceID, ordinal, successful,
		capturedAt, attemptStartedAt, chunkCount, domain.ProcessingContract{
			ParserID: "goldmark", ParserVersion: "v1", ParserConfigHash: strings.Repeat("d", 64),
			ChunkStrategyVersion: "structure-v1", SchemaVersion: "v1",
		})
}

func seedSnapshotSourceVersionForSourceContract(
	t *testing.T,
	ctx context.Context,
	database *pgxpool.Pool,
	workspaceID, sourceID foundation.ID,
	ordinal int,
	successful bool,
	capturedAt, attemptStartedAt time.Time,
	chunkCount int,
	contract domain.ProcessingContract,
) snapshotSourceFixture {
	t.Helper()
	artifactID := snapshotID(ordinal*10 + 1)
	versionID := snapshotID(ordinal*10 + 2)
	projectionID := snapshotID(ordinal*10 + 3)
	spanID := snapshotID(ordinal*10 + 4)
	attemptID := snapshotID(ordinal*10 + 5)
	content := fmt.Sprintf("snapshot content %d", ordinal)
	artifactHash := snapshotHash("artifact", ordinal)
	normalizedHash := snapshotHash("normalized", ordinal)
	if _, err := database.Exec(ctx, `INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at)
		VALUES($1,$2,$3,$4,$5,$6)`, string(artifactID), string(workspaceID), artifactHash, int64(len(content)), ".knowledge/sources/"+artifactHash, capturedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(ctx, `INSERT INTO core.source_version(
		id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at
	) VALUES($1,$2,$3,$4,$5,$6,'text/plain',$7,'passed',$8)`, string(versionID), string(sourceID), string(workspaceID), string(artifactID), artifactHash,
		int64(len(content)), fmt.Sprintf("notes/%d.txt", ordinal), capturedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(ctx, `INSERT INTO ingestion.parse_projection(
		id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,created_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, string(projectionID), string(workspaceID), string(artifactID),
		contract.ParserID, contract.ParserVersion, contract.ParserConfigHash, contract.SchemaVersion, normalizedHash, capturedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(ctx, `INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at)
		VALUES($1,$2,$3,$4)`, string(versionID), string(projectionID), string(workspaceID), capturedAt); err != nil {
		t.Fatal(err)
	}
	status := "parse_failed"
	securityStatus := "passed"
	projection := any(nil)
	if successful {
		status = "chunked"
		projection = string(projectionID)
	}
	if _, err := database.Exec(ctx, `INSERT INTO ingestion.attempt(
		id,workspace_id,source_version_id,parse_projection_id,status,security_status,failure_stage,error_code,retryable,
		parser_id,parser_version,parser_config_hash,chunk_strategy_version,schema_version,idempotency_key,attempt_number,
		started_at,completed_at,version
	) VALUES($1,$2,$3,$4,$5,$6,'','',false,$7,$8,$9,$10,$11,$12,1,$13,$13,1)`,
		string(attemptID), string(workspaceID), string(versionID), projection, status, securityStatus,
		contract.ParserID, contract.ParserVersion, contract.ParserConfigHash, contract.ChunkStrategyVersion, contract.SchemaVersion,
		fmt.Sprintf("snapshot-attempt-%d", ordinal), attemptStartedAt); err != nil {
		t.Fatal(err)
	}
	if chunkCount > 0 {
		if _, err := database.Exec(ctx, `INSERT INTO ingestion.source_span(
			id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,
			excerpt_hash,parser_version,schema_version,created_at
		) VALUES($1,$2,$3,$4,'paragraph',1,1,0,$5,$6,$7,$8,$9)`, string(spanID), string(workspaceID),
			string(artifactID), string(projectionID), int64(len(content)), snapshotHash("span", ordinal),
			contract.ParserVersion, contract.SchemaVersion, capturedAt); err != nil {
			t.Fatal(err)
		}
	}
	for sequence := range chunkCount {
		chunkID := snapshotID(ordinal*100 + sequence + 1)
		chunkContent := fmt.Sprintf("chunk %d/%d", ordinal, sequence)
		if _, err := database.Exec(ctx, `INSERT INTO ingestion.canonical_chunk(
			id,workspace_id,parse_projection_id,sequence,heading_path,content,content_hash,source_span_id,byte_count,rune_count,
			parser_version,chunk_strategy_version,schema_version,atomic_oversized,status,created_at
		) VALUES($1,$2,$3,$4,'[]',$5,$6,$7,$8,$8,$9,$10,$11,false,'active',$12)`,
			string(chunkID), string(workspaceID), string(projectionID), sequence, chunkContent, snapshotHash("chunk", ordinal*100+sequence),
			string(spanID), int64(len(chunkContent)), contract.ParserVersion, contract.ChunkStrategyVersion, contract.SchemaVersion, capturedAt); err != nil {
			t.Fatal(err)
		}
	}
	return snapshotSourceFixture{SourceID: sourceID, VersionID: versionID, ProjectionID: projectionID, ArtifactID: artifactID, ContentHash: artifactHash}
}

func seedSnapshotSourceSharingProjection(
	t *testing.T,
	ctx context.Context,
	database *pgxpool.Pool,
	workspaceID foundation.ID,
	ordinal int,
	shared snapshotSourceFixture,
	now time.Time,
) snapshotSourceFixture {
	t.Helper()
	sourceID := snapshotID(ordinal)
	versionID := snapshotID(ordinal*10 + 2)
	attemptID := snapshotID(ordinal*10 + 5)
	location := fmt.Sprintf("notes/shared-%d.txt", ordinal)
	if _, err := database.Exec(ctx, `INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at)
		VALUES($1,$2,'text',$3,$3,$4)`, string(sourceID), string(workspaceID), location, now); err != nil {
		t.Fatal(err)
	}
	var size int64
	if err := database.QueryRow(ctx, `SELECT byte_size FROM core.content_artifact WHERE id=$1`, string(shared.ArtifactID)).Scan(&size); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(ctx, `INSERT INTO core.source_version(
		id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at
	) VALUES($1,$2,$3,$4,$5,$6,'text/plain',$7,'passed',$8)`, string(versionID), string(sourceID), string(workspaceID), string(shared.ArtifactID), shared.ContentHash, size, location, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(ctx, `INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at)
		VALUES($1,$2,$3,$4)`, string(versionID), string(shared.ProjectionID), string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(ctx, `INSERT INTO ingestion.attempt(
		id,workspace_id,source_version_id,parse_projection_id,status,security_status,failure_stage,error_code,retryable,
		parser_id,parser_version,parser_config_hash,chunk_strategy_version,schema_version,idempotency_key,attempt_number,
		started_at,completed_at,version
	) VALUES($1,$2,$3,$4,'chunked','passed','','',false,'goldmark','v1',$5,'structure-v1','v1',$6,1,$7,$7,1)`,
		string(attemptID), string(workspaceID), string(versionID), string(shared.ProjectionID), strings.Repeat("d", 64),
		fmt.Sprintf("snapshot-shared-%d", ordinal), now); err != nil {
		t.Fatal(err)
	}
	return snapshotSourceFixture{SourceID: sourceID, VersionID: versionID, ProjectionID: shared.ProjectionID, ArtifactID: shared.ArtifactID, ContentHash: shared.ContentHash}
}

func seedSnapshotChunkWithStrategy(
	t *testing.T,
	ctx context.Context,
	database *pgxpool.Pool,
	fixture snapshotSourceFixture,
	ordinal int,
	strategy, schema string,
	now time.Time,
) {
	t.Helper()
	var workspaceID, spanID foundation.ID
	if err := database.QueryRow(ctx, `SELECT workspace_id::text,source_span_id::text FROM ingestion.canonical_chunk
		WHERE parse_projection_id=$1 ORDER BY sequence,id LIMIT 1`, string(fixture.ProjectionID)).Scan(&workspaceID, &spanID); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf("legacy strategy chunk %d", ordinal)
	if _, err := database.Exec(ctx, `INSERT INTO ingestion.canonical_chunk(
		id,workspace_id,parse_projection_id,sequence,heading_path,content,content_hash,source_span_id,byte_count,rune_count,
		parser_version,chunk_strategy_version,schema_version,atomic_oversized,status,created_at
	) VALUES($1,$2,$3,0,'[]',$4,$5,$6,$7,$7,'v1',$8,$9,false,'active',$10)`,
		string(snapshotID(ordinal)), string(workspaceID), string(fixture.ProjectionID), content, snapshotHash("legacy-chunk", ordinal),
		string(spanID), int64(len(content)), strategy, schema, now); err != nil {
		t.Fatal(err)
	}
}

func mustCreateAndActivateSnapshot(
	t *testing.T,
	ctx context.Context,
	repository *Repository,
	command domain.WorkspaceSnapshotCommand,
	activationOrdinal int,
	at time.Time,
) domain.IndexVersion {
	t.Helper()
	created, err := repository.BeginWorkspaceSnapshot(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.BuildLexical(ctx, domain.LexicalBuildCommand{
		WorkspaceID: command.IndexVersion.WorkspaceID, IndexVersionID: created.IndexVersion.ID,
		ExpectedIndexVersion: 1, At: at,
	}); err != nil {
		t.Fatal(err)
	}
	ready, err := repository.TransitionIndex(ctx, domain.IndexTransition{
		WorkspaceID: command.IndexVersion.WorkspaceID, IndexVersionID: created.IndexVersion.ID,
		ExpectedVersion: 1, Status: domain.IndexStatusReady, At: at.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	activation := domain.ActivationCommand{
		ActivationID: snapshotID(activationOrdinal), WorkspaceID: command.IndexVersion.WorkspaceID,
		TargetIndexVersionID: ready.ID, ExpectedTargetVersion: ready.Version,
		IdempotencyKey: fmt.Sprintf("activate-snapshot-%d", activationOrdinal), ReasonCode: "BUILD_VERIFIED", At: at.Add(2 * time.Second),
	}
	current, currentErr := repository.GetActive(ctx, command.IndexVersion.WorkspaceID)
	if currentErr == nil {
		activation.ExpectedCurrentIndexVersionID = &current.ID
		activation.ExpectedCurrentVersion = &current.Version
	} else if !retrievalErrorKind(currentErr, foundation.ErrorNotFound) {
		t.Fatal(currentErr)
	}
	activated, err := repository.Activate(ctx, activation)
	if err != nil {
		t.Fatal(err)
	}
	return activated.ActiveIndexVersion
}

func snapshotManifestChunk(t *testing.T, ctx context.Context, database *pgxpool.Pool, fixture snapshotSourceFixture) domain.ManifestChunk {
	t.Helper()
	var chunk domain.ManifestChunk
	if err := database.QueryRow(ctx, `SELECT id::text,workspace_id::text,content_hash,sequence,parser_version,
		chunk_strategy_version,schema_version,created_at FROM ingestion.canonical_chunk
		WHERE parse_projection_id=$1 ORDER BY sequence,id LIMIT 1`, string(fixture.ProjectionID)).Scan(
		&chunk.ChunkID, &chunk.WorkspaceID, &chunk.ContentHash, &chunk.Sequence, &chunk.ParserVersion,
		&chunk.ChunkStrategyVersion, &chunk.SchemaVersion, &chunk.CreatedAt,
	); err != nil {
		t.Fatal(err)
	}
	return chunk
}

func snapshotCommand(workspaceID foundation.ID, indexOrdinal int, target snapshotSourceFixture, key string, now time.Time) domain.WorkspaceSnapshotCommand {
	return domain.WorkspaceSnapshotCommand{
		IndexVersion: domain.IndexVersion{
			ID: snapshotID(indexOrdinal), WorkspaceID: workspaceID,
			TokenizerID: "postgres-simple", TokenizerVersion: "v1", TokenizerConfigHash: strings.Repeat("1", 64),
			FusionConfig: []byte(`{"method":"rrf","k":60}`), SourceSnapshotRef: "reindex-v1:" + string(snapshotID(indexOrdinal+1000)),
			IdempotencyKey: key, Status: domain.IndexStatusBuilding,
			DegradedCapabilities: []domain.DegradedCapability{domain.DegradedVector}, Version: 1, CreatedAt: now, UpdatedAt: now,
			ProcessingContract: &domain.ProcessingContract{
				ParserID: "goldmark", ParserVersion: "v1", ParserConfigHash: strings.Repeat("d", 64),
				ChunkStrategyVersion: "structure-v1", SchemaVersion: "v1",
			},
		},
		TargetSourceID: target.SourceID, TargetSourceVersionID: target.VersionID, TargetParseProjectionID: target.ProjectionID,
		PageSize: 1000, MaxSources: 10_000, MaxChunks: 500_000,
	}
}

func assertSnapshotSource(t *testing.T, ctx context.Context, database *pgxpool.Pool, indexID, sourceID, versionID, projectionID foundation.ID, status string) {
	t.Helper()
	var actualStatus string
	var actualVersion, actualProjection *string
	if err := database.QueryRow(ctx, `SELECT selection_status,source_version_id::text,parse_projection_id::text
		FROM retrieval.index_manifest_source WHERE index_version_id=$1 AND source_id=$2`, string(indexID), string(sourceID)).Scan(&actualStatus, &actualVersion, &actualProjection); err != nil {
		t.Fatal(err)
	}
	if actualStatus != status || optionalFixtureID(actualVersion) != versionID || optionalFixtureID(actualProjection) != projectionID {
		t.Fatalf("source %s status=%s version=%v projection=%v", sourceID, actualStatus, actualVersion, actualProjection)
	}
}

func optionalFixtureID(value *string) foundation.ID {
	if value == nil {
		return ""
	}
	return foundation.ID(*value)
}

func snapshotID(ordinal int) foundation.ID {
	return foundation.ID(fmt.Sprintf("a0000000-0000-4000-8000-%012x", ordinal))
}

func snapshotHash(label string, ordinal int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", label, ordinal)))
	return hex.EncodeToString(sum[:])
}

func retrievalErrorCode(err error, code string) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Code == code
}
