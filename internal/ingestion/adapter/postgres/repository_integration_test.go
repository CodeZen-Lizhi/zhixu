package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositoryAttemptProjectionLifecycle(t *testing.T) {
	repository, tx, ctx := integrationRepository(t)
	workspaceID, artifactID, versionID := seedSourceVersion(t, ctx, tx, "71000000", "a")
	now := time.Date(2026, 7, 17, 2, 0, 0, 0, time.UTC)
	definitionID := mustID(t, "71500000-0000-4000-8000-000000000001")
	workflowRunID := mustID(t, "71500000-0000-4000-8000-000000000002")
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at) VALUES($1,$2,'ingestion-test',1,'{}',$3)`, string(definitionID), string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at) VALUES($1,$2,$3,'pending','{}',1,$4,$4)`, string(workflowRunID), string(workspaceID), string(definitionID), now); err != nil {
		t.Fatal(err)
	}
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
	if _, err := tx.Exec(ctx, `UPDATE ingestion.parse_projection SET parser_version='changed' WHERE id=$1`, string(projectionID)); err == nil {
		t.Fatal("immutable parse projection update succeeded")
	}
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

func integrationRepository(t *testing.T) (*Repository, pgx.Tx, context.Context) {
	t.Helper()
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	repository, err := NewRepository(tx)
	if err != nil {
		t.Fatal(err)
	}
	return repository, tx, ctx
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
		{`INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at) VALUES($1,$2,$3,$3,$4,'test', $4,$4)`, []any{string(workspaceID), prefix, root, now}},
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
