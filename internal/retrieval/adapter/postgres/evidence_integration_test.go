//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestEvidenceReferenceStoreLoadsFullyBoundReferences(t *testing.T) {
	_, database, ctx := newRetrievalTestRepository(t)
	repository, err := NewSearchRepository(database.DB())
	if err != nil {
		t.Fatal(err)
	}
	fixture := seedEvidenceReferenceFixture(t, ctx, database.DB())

	sourceVersion, err := repository.LoadSourceVersionReference(ctx, fixture.workspaceID, fixture.sourceVersionID)
	if err != nil {
		t.Fatal(err)
	}
	if sourceVersion.WorkspaceID != fixture.workspaceID || sourceVersion.SourceID != fixture.sourceID ||
		sourceVersion.SourceVersionID != fixture.sourceVersionID || sourceVersion.ContentArtifactID != fixture.artifactID ||
		sourceVersion.SourceType != "file" || sourceVersion.LogicalName != "Evidence" || sourceVersion.RelativePath != "docs/evidence.md" ||
		sourceVersion.ContentHash != fixture.contentHash || sourceVersion.ByteSize != fixture.byteSize ||
		sourceVersion.MediaType != "text/markdown" || sourceVersion.SecurityStatus != "passed" || !sourceVersion.CapturedAt.Equal(fixture.capturedAt) {
		t.Fatalf("source version reference = %#v", sourceVersion)
	}

	span, err := repository.LoadSourceSpanReference(ctx, fixture.workspaceID, fixture.sourceVersionID, fixture.spanID)
	if err != nil {
		t.Fatal(err)
	}
	if span.SourceVersion != sourceVersion || span.ParseProjectionID != fixture.projectionID || span.Span.ID != fixture.spanID ||
		span.Span.StartLine != 1 || span.Span.EndLine != 1 || span.Span.StartByte != 0 || span.Span.EndByte != fixture.byteSize ||
		span.SpanType != "paragraph" || string(span.Selector) != `{"kind": "paragraph"}` ||
		span.ExcerptHash != fixture.contentHash || span.ParserVersion != "goldmark-v1" || span.SchemaVersion != "schema-v1" {
		t.Fatalf("source span reference = %#v", span)
	}
}

func TestEvidenceReferenceStoreHidesCrossWorkspaceAndBindingMisses(t *testing.T) {
	_, database, ctx := newRetrievalTestRepository(t)
	repository, err := NewSearchRepository(database.DB())
	if err != nil {
		t.Fatal(err)
	}
	fixture := seedEvidenceReferenceFixture(t, ctx, database.DB())

	tests := []struct {
		name string
		load func() error
	}{
		{name: "source version cross workspace", load: func() error {
			_, err := repository.LoadSourceVersionReference(ctx, fixture.otherWorkspaceID, fixture.sourceVersionID)
			return err
		}},
		{name: "span cross workspace", load: func() error {
			_, err := repository.LoadSourceSpanReference(ctx, fixture.otherWorkspaceID, fixture.sourceVersionID, fixture.spanID)
			return err
		}},
		{name: "span belongs to another source version", load: func() error {
			_, err := repository.LoadSourceSpanReference(ctx, fixture.workspaceID, fixture.sourceVersionID, fixture.otherSpanID)
			return err
		}},
		{name: "source version missing", load: func() error {
			_, err := repository.LoadSourceVersionReference(ctx, fixture.workspaceID, "95000000-0000-4000-8000-000000000099")
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requireEvidenceError(t, test.load(), foundation.ErrorNotFound, evidenceReferenceNotFoundCode)
		})
	}
}

func TestEvidenceReferenceStoreFailsClosedForDamagedPersistentMetadata(t *testing.T) {
	_, database, ctx := newRetrievalTestRepository(t)
	repository, err := NewSearchRepository(database.DB())
	if err != nil {
		t.Fatal(err)
	}
	fixture := seedEvidenceReferenceFixture(t, ctx, database.DB())

	_, err = repository.LoadSourceVersionReference(ctx, fixture.workspaceID, fixture.damagedSourceVersionID)
	requireEvidenceError(t, err, foundation.ErrorConsistencyViolation, domain.ErrorCodeEvidenceReferenceInvalid)

	_, err = repository.LoadSourceSpanReference(ctx, fixture.workspaceID, fixture.damagedSourceVersionID, fixture.damagedSpanID)
	requireEvidenceError(t, err, foundation.ErrorConsistencyViolation, domain.ErrorCodeEvidenceReferenceInvalid)
}

type evidenceReferenceFixture struct {
	workspaceID            foundation.ID
	otherWorkspaceID       foundation.ID
	sourceID               foundation.ID
	sourceVersionID        foundation.ID
	artifactID             foundation.ID
	projectionID           foundation.ID
	spanID                 foundation.ID
	otherSpanID            foundation.ID
	damagedSourceVersionID foundation.ID
	damagedSpanID          foundation.ID
	contentHash            string
	byteSize               int64
	capturedAt             time.Time
}

func seedEvidenceReferenceFixture(t *testing.T, ctx context.Context, database *pgxpool.Pool) evidenceReferenceFixture {
	t.Helper()
	content := []byte("immutable evidence")
	digest := sha256.Sum256(content)
	hash := hex.EncodeToString(digest[:])
	now := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	fixture := evidenceReferenceFixture{
		workspaceID:            "95000000-0000-4000-8000-000000000001",
		otherWorkspaceID:       "95000000-0000-4000-8000-000000000002",
		sourceID:               "95000000-0000-4000-8000-000000000003",
		artifactID:             "95000000-0000-4000-8000-000000000004",
		sourceVersionID:        "95000000-0000-4000-8000-000000000005",
		projectionID:           "95000000-0000-4000-8000-000000000006",
		spanID:                 "95000000-0000-4000-8000-000000000007",
		otherSpanID:            "95000000-0000-4000-8000-000000000017",
		damagedSourceVersionID: "95000000-0000-4000-8000-000000000025",
		damagedSpanID:          "95000000-0000-4000-8000-000000000027",
		contentHash:            hash,
		byteSize:               int64(len(content)),
		capturedAt:             now,
	}
	statements := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
		  VALUES($1,'evidence','/tmp/evidence','/tmp/evidence',$2,'test',1,$2,$2)`, []any{string(fixture.workspaceID), now}},
		{`INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
		  VALUES($1,'other','/tmp/evidence-other','/tmp/evidence-other',$2,'test',1,$2,$2)`, []any{string(fixture.otherWorkspaceID), now}},
		{`INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at)
		  VALUES($1,$2,'file','Evidence','docs/evidence.md',$3)`, []any{string(fixture.sourceID), string(fixture.workspaceID), now}},
		{`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at)
		  VALUES($1,$2,$3,$4,'.knowledge/sources/' || $3,$5)`, []any{string(fixture.artifactID), string(fixture.workspaceID), hash, fixture.byteSize, now}},
		{`INSERT INTO core.source_version(id,source_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at,content_artifact_id)
		  VALUES($1,$2,$3,$4,'text/markdown','docs/evidence.md','passed',$5,$6)`, []any{string(fixture.sourceVersionID), string(fixture.sourceID), hash, fixture.byteSize, now, string(fixture.artifactID)}},
		{`INSERT INTO ingestion.parse_projection(id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,created_at)
		  VALUES($1,$2,$3,'goldmark','goldmark-v1',$4,'schema-v1',$5,$6)`, []any{string(fixture.projectionID), string(fixture.workspaceID), string(fixture.artifactID), hex64('a'), hex64('b'), now}},
		{`INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at)
		  VALUES($1,$2,$3,$4)`, []any{string(fixture.sourceVersionID), string(fixture.projectionID), string(fixture.workspaceID), now}},
		{`INSERT INTO ingestion.source_span(id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,selector,excerpt_hash,parser_version,schema_version,created_at)
		  VALUES($1,$2,$3,$4,'paragraph',1,1,0,$5,'{"kind": "paragraph"}'::jsonb,$6,'goldmark-v1','schema-v1',$7)`, []any{string(fixture.spanID), string(fixture.workspaceID), string(fixture.artifactID), string(fixture.projectionID), fixture.byteSize, hash, now}},
	}
	for _, statement := range statements {
		if _, err := database.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	seedOtherEvidenceBinding(t, ctx, database, fixture, now)
	seedDamagedEvidenceBinding(t, ctx, database, fixture, now)
	return fixture
}

func seedOtherEvidenceBinding(t *testing.T, ctx context.Context, database *pgxpool.Pool, fixture evidenceReferenceFixture, now time.Time) {
	t.Helper()
	sourceID := "95000000-0000-4000-8000-000000000013"
	artifactID := "95000000-0000-4000-8000-000000000014"
	versionID := "95000000-0000-4000-8000-000000000015"
	projectionID := "95000000-0000-4000-8000-000000000016"
	statements := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at) VALUES($1,$2,'file','Other','docs/other.md',$3)`, []any{sourceID, string(fixture.workspaceID), now}},
		{`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at) VALUES($1,$2,$3,$4,'.knowledge/sources/' || $3,$5)`, []any{artifactID, string(fixture.workspaceID), hex64('c'), int64(1), now}},
		{`INSERT INTO core.source_version(id,source_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at,content_artifact_id) VALUES($1,$2,$3,1,'text/plain','docs/other.md','passed',$4,$5)`, []any{versionID, sourceID, hex64('c'), now, artifactID}},
		{`INSERT INTO ingestion.parse_projection(id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,created_at) VALUES($1,$2,$3,'text','text-v1',$4,'schema-v1',$5,$6)`, []any{projectionID, string(fixture.workspaceID), artifactID, hex64('d'), hex64('e'), now}},
		{`INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at) VALUES($1,$2,$3,$4)`, []any{versionID, projectionID, string(fixture.workspaceID), now}},
		{`INSERT INTO ingestion.source_span(id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,selector,excerpt_hash,parser_version,schema_version,created_at) VALUES($1,$2,$3,$4,'paragraph',1,1,0,1,'{}',$5,'text-v1','schema-v1',$6)`, []any{string(fixture.otherSpanID), string(fixture.workspaceID), artifactID, projectionID, hex64('c'), now}},
	}
	for _, statement := range statements {
		if _, err := database.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func seedDamagedEvidenceBinding(t *testing.T, ctx context.Context, database *pgxpool.Pool, fixture evidenceReferenceFixture, now time.Time) {
	t.Helper()
	sourceID := "95000000-0000-4000-8000-000000000023"
	artifactID := "95000000-0000-4000-8000-000000000024"
	projectionID := "95000000-0000-4000-8000-000000000026"
	statements := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at) VALUES($1,$2,'file','Damaged','docs/damaged.md',$3)`, []any{sourceID, string(fixture.workspaceID), now}},
		{`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at) VALUES($1,$2,$3,1,'.knowledge/sources/' || $3,$4)`, []any{artifactID, string(fixture.workspaceID), hex64('f'), now}},
		{`INSERT INTO core.source_version(id,source_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at,content_artifact_id) VALUES($1,$2,$3,1,'text/plain','docs/different.md','passed',$4,$5)`, []any{string(fixture.damagedSourceVersionID), sourceID, hex64('f'), now, artifactID}},
		{`INSERT INTO ingestion.parse_projection(id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,created_at) VALUES($1,$2,$3,'text','text-v1',$4,'schema-v1',$5,$6)`, []any{projectionID, string(fixture.workspaceID), artifactID, hex64('1'), hex64('2'), now}},
		{`INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at) VALUES($1,$2,$3,$4)`, []any{string(fixture.damagedSourceVersionID), projectionID, string(fixture.workspaceID), now}},
		{`INSERT INTO ingestion.source_span(id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,selector,excerpt_hash,parser_version,schema_version,created_at) VALUES($1,$2,$3,$4,'paragraph',1,1,0,1,'{}',$5,'text-v1','schema-v1',$6)`, []any{string(fixture.damagedSpanID), string(fixture.workspaceID), artifactID, projectionID, hex64('f'), now}},
	}
	for _, statement := range statements {
		if _, err := database.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func hex64(value byte) string {
	buffer := make([]byte, 64)
	for index := range buffer {
		buffer[index] = value
	}
	return string(buffer)
}

func requireEvidenceError(t *testing.T, err error, kind foundation.ErrorKind, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != kind || classified.Code != code {
		t.Fatalf("error = %#v, want kind=%q code=%q", err, kind, code)
	}
}
