//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositorySnapshotRegressionPassesAndReplaysWithoutChangingBuildingIndex(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	fixture := seedSnapshotRegressionFixture(t, ctx, repository, database.DB(), 600)

	first, err := repository.RunSnapshotRegression(ctx, fixture.Command)
	if err != nil {
		t.Fatalf("RunSnapshotRegression() error = %v", err)
	}
	replay, err := repository.RunSnapshotRegression(ctx, fixture.Command)
	if err != nil {
		t.Fatalf("RunSnapshotRegression(replay) error = %v", err)
	}
	if first.Code != domain.SnapshotStructureRegressionV1 || first.Hash == "" || first.Hash != replay.Hash ||
		first.PassedAt.IsZero() || replay.PassedAt.IsZero() {
		t.Fatalf("first=%#v replay=%#v", first, replay)
	}
	assertRegressionIndexState(t, ctx, database.DB(), fixture.Command.IndexVersionID, domain.IndexStatusBuilding, "", 1)
}

func TestRepositorySnapshotRegressionFailsClosedForStructuralMismatch(t *testing.T) {
	repository, database, ctx := newRetrievalTestRepository(t)
	tests := []struct {
		name    string
		ordinal int
		mutate  func(*snapshotRegressionFixture)
	}{
		{"target missing", 610, func(fixture *snapshotRegressionFixture) {
			fixture.Command.TargetSourceID = snapshotID(999_610)
		}},
		{"wrong result hash", 620, func(fixture *snapshotRegressionFixture) {
			fixture.Command.TargetResultHash = snapshotHash("wrong-result", 620)
		}},
		{"missing chunk", 630, func(fixture *snapshotRegressionFixture) {
			manifest := snapshotRegressionManifestChunks(t, ctx, database.DB(), fixture.Command.IndexVersionID)
			seedAdditionalRetrievalChunk(t, ctx, database.DB(), manifest[0], snapshotID(999_630), 99, fixture.Now.Add(time.Minute))
		}},
		{"extra chunk", 640, func(fixture *snapshotRegressionFixture) {
			addSnapshotRegressionExtraChunk(t, ctx, database.DB(), fixture, 641)
		}},
		{"vector not disabled", 650, func(fixture *snapshotRegressionFixture) {
			withReplicaRole(t, ctx, database.DB(), func(connection *pgxpool.Conn) {
				if _, err := connection.Exec(ctx, `UPDATE retrieval.chunk_projection SET vector_status='pending' WHERE index_version_id=$1`, string(fixture.Command.IndexVersionID)); err != nil {
					t.Fatal(err)
				}
			})
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := seedSnapshotRegressionFixture(t, ctx, repository, database.DB(), test.ordinal)
			test.mutate(&fixture)
			_, err := repository.RunSnapshotRegression(ctx, fixture.Command)
			assertSnapshotRegressionIntegrationError(t, err)
			assertRegressionIndexState(t, ctx, database.DB(), fixture.Command.IndexVersionID, domain.IndexStatusFailed,
				domain.ErrorCodeSnapshotStructureRegressionFailed, 2)

			_, replayErr := repository.RunSnapshotRegression(ctx, fixture.Command)
			assertSnapshotRegressionIntegrationError(t, replayErr)
			assertRegressionIndexState(t, ctx, database.DB(), fixture.Command.IndexVersionID, domain.IndexStatusFailed,
				domain.ErrorCodeSnapshotStructureRegressionFailed, 2)
		})
	}
}

type snapshotRegressionFixture struct {
	Command domain.SnapshotRegressionCommand
	Target  snapshotSourceFixture
	Now     time.Time
}

func seedSnapshotRegressionFixture(
	t *testing.T,
	ctx context.Context,
	repository *Repository,
	database *pgxpool.Pool,
	ordinal int,
) snapshotRegressionFixture {
	t.Helper()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC).Add(time.Duration(ordinal) * time.Minute)
	workspaceID := seedSnapshotWorkspace(t, ctx, database, ordinal, now)
	target := seedSnapshotSourceVersion(t, ctx, database, workspaceID, ordinal+10_000, 1, true, now, now, 2)
	command := snapshotCommand(workspaceID, ordinal+20_000, target, fmt.Sprintf("snapshot-regression-%d", ordinal), now.Add(time.Minute))
	created, err := repository.BeginWorkspaceSnapshot(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.BuildLexical(ctx, domain.LexicalBuildCommand{
		WorkspaceID: workspaceID, IndexVersionID: created.IndexVersion.ID,
		ExpectedIndexVersion: created.IndexVersion.Version, At: now.Add(2 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	deliveryID := snapshotID(ordinal + 30_000)
	withReplicaRole(t, ctx, database, func(connection *pgxpool.Conn) {
		if _, err := connection.Exec(ctx, `INSERT INTO retrieval.reindex_delivery(
			id,consumer_name,outbox_event_id,workspace_id,writeback_execution_id,status,dispatch_no,attempt_no,
			version,source_version_id,parse_projection_id,index_version_id,excluded_source_count,
			manual_recovery_required,created_at,updated_at
		) VALUES($1,'reindex-consumer',$2,$3,$4,'processing',1,1,1,$5,$6,$7,$8,false,$9,$9)`,
			string(deliveryID), string(snapshotID(ordinal+30_001)), string(workspaceID), string(snapshotID(ordinal+30_002)),
			string(target.VersionID), string(target.ProjectionID), string(created.IndexVersion.ID), created.ExcludedSourceCount, now); err != nil {
			t.Fatal(err)
		}
	})
	return snapshotRegressionFixture{
		Command: domain.SnapshotRegressionCommand{
			WorkspaceID: workspaceID, DeliveryID: deliveryID, TargetSourceID: target.SourceID,
			TargetSourceVersionID: target.VersionID, TargetResultHash: target.ContentHash,
			TargetParseProjectionID: target.ProjectionID, IndexVersionID: created.IndexVersion.ID,
		},
		Target: target,
		Now:    now,
	}
}

func addSnapshotRegressionExtraChunk(t *testing.T, ctx context.Context, database *pgxpool.Pool, fixture *snapshotRegressionFixture, ordinal int) {
	t.Helper()
	other := seedSnapshotSourceVersion(t, ctx, database, fixture.Command.WorkspaceID, ordinal+40_000, 1, true,
		fixture.Now.Add(time.Minute), fixture.Now.Add(time.Minute), 1)
	extra := snapshotManifestChunk(t, ctx, database, other)
	manifest := append(snapshotRegressionManifestChunks(t, ctx, database, fixture.Command.IndexVersionID), extra)
	for index := range manifest {
		manifest[index].IndexVersionID = fixture.Command.IndexVersionID
		manifest[index].CreatedAt = fixture.Now.Add(2 * time.Minute)
	}
	canonical, manifestHash, err := domain.CanonicalizeManifest(fixture.Command.WorkspaceID, fixture.Command.IndexVersionID, manifest)
	if err != nil {
		t.Fatal(err)
	}
	withReplicaRole(t, ctx, database, func(connection *pgxpool.Conn) {
		if _, err := connection.Exec(ctx, `INSERT INTO retrieval.index_manifest_chunk(
			index_version_id,chunk_id,workspace_id,content_hash,sequence,parser_version,chunk_strategy_version,schema_version,created_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, string(fixture.Command.IndexVersionID), string(extra.ChunkID),
			string(fixture.Command.WorkspaceID), extra.ContentHash, extra.Sequence, extra.ParserVersion,
			extra.ChunkStrategyVersion, extra.SchemaVersion, fixture.Now.Add(2*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if _, err := connection.Exec(ctx, `UPDATE retrieval.index_version SET manifest_hash=$2,expected_chunk_count=$3
			WHERE id=$1`, string(fixture.Command.IndexVersionID), manifestHash, int64(len(canonical))); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := database.Exec(ctx, `INSERT INTO retrieval.chunk_projection(
		index_version_id,chunk_id,workspace_id,embedding_version_id,search_vector,embedding,token_count,
		lexical_status,vector_status,failure_code,created_at,updated_at
	) SELECT $1,c.id,c.workspace_id,NULL,to_tsvector('simple',c.content),NULL,length(to_tsvector('simple',c.content)),
		'ready','disabled',NULL,$3,$3 FROM ingestion.canonical_chunk c WHERE c.id=$2`,
		string(fixture.Command.IndexVersionID), string(extra.ChunkID), fixture.Now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
}

func snapshotRegressionManifestChunks(t *testing.T, ctx context.Context, database *pgxpool.Pool, indexID foundation.ID) []domain.ManifestChunk {
	t.Helper()
	rows, err := database.Query(ctx, `SELECT index_version_id::text,chunk_id::text,workspace_id::text,content_hash,sequence,
		parser_version,chunk_strategy_version,schema_version,created_at
		FROM retrieval.index_manifest_chunk WHERE index_version_id=$1 ORDER BY sequence,chunk_id`, string(indexID))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var result []domain.ManifestChunk
	for rows.Next() {
		var chunk domain.ManifestChunk
		if err := rows.Scan(&chunk.IndexVersionID, &chunk.ChunkID, &chunk.WorkspaceID, &chunk.ContentHash, &chunk.Sequence,
			&chunk.ParserVersion, &chunk.ChunkStrategyVersion, &chunk.SchemaVersion, &chunk.CreatedAt); err != nil {
			t.Fatal(err)
		}
		result = append(result, chunk)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func withReplicaRole(t *testing.T, ctx context.Context, database *pgxpool.Pool, mutate func(*pgxpool.Conn)) {
	t.Helper()
	connection, err := database.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Release()
	if _, err := connection.Exec(ctx, `SET session_replication_role=replica`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = connection.Exec(context.Background(), `SET session_replication_role=origin`) }()
	mutate(connection)
	if _, err := connection.Exec(ctx, `SET session_replication_role=origin`); err != nil {
		t.Fatal(err)
	}
}

func assertSnapshotRegressionIntegrationError(t *testing.T, err error) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorConsistencyViolation ||
		classified.Code != domain.ErrorCodeSnapshotStructureRegressionFailed || classified.Retryable {
		if classified != nil {
			t.Fatalf("regression error = %#v cause=%v", err, classified.Cause)
		}
		t.Fatalf("regression error = %#v", err)
	}
}

func assertRegressionIndexState(t *testing.T, ctx context.Context, database *pgxpool.Pool, indexID foundation.ID, status domain.IndexStatus, failureCode string, version int64) {
	t.Helper()
	var actualStatus domain.IndexStatus
	var actualFailure *string
	var actualVersion int64
	if err := database.QueryRow(ctx, `SELECT status,failure_code,version FROM retrieval.index_version WHERE id=$1`, string(indexID)).Scan(
		&actualStatus, &actualFailure, &actualVersion,
	); err != nil {
		t.Fatal(err)
	}
	actualCode := ""
	if actualFailure != nil {
		actualCode = *actualFailure
	}
	if actualStatus != status || actualCode != failureCode || actualVersion != version {
		t.Fatalf("index status=%s failure=%s version=%d", actualStatus, actualCode, actualVersion)
	}
}
