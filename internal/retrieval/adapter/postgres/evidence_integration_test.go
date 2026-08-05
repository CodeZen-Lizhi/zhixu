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
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestEvidenceReferenceStoreLoadsFullyBoundReferences(t *testing.T) {
	_, database, ctx := newRetrievalTestRepository(t)
	repository, err := NewSearchRepository(database.DB())
	if err != nil {
		t.Fatal(err)
	}
	fixture := seedEvidenceReferenceFixture(t, ctx, database.DB())
	activateEvidenceReferenceFixture(t, ctx, database.DB(), fixture)

	sourceVersion, err := repository.LoadSourceVersionReference(ctx, fixture.workspaceID, fixture.sourceVersionID)
	if err != nil {
		t.Fatal(err)
	}
	if sourceVersion.WorkspaceID != fixture.workspaceID || sourceVersion.SourceID != fixture.sourceID ||
		sourceVersion.SourceVersionID != fixture.sourceVersionID || sourceVersion.ContentArtifactID != fixture.artifactID ||
		sourceVersion.SourceType != "file" || sourceVersion.LogicalName != "Evidence" || sourceVersion.RelativePath != "docs/evidence.md" ||
		sourceVersion.ContentHash != fixture.contentHash || sourceVersion.ByteSize != fixture.byteSize ||
		sourceVersion.MediaType != "text/markdown" || sourceVersion.SecurityStatus != "passed" ||
		sourceVersion.IngestionStatus != "parsed" || sourceVersion.WorkflowStatus != "running" || sourceVersion.IndexStatus != "included" ||
		!sourceVersion.CapturedAt.Equal(fixture.capturedAt) {
		t.Fatalf("source version reference = %#v", sourceVersion)
	}

	span, err := repository.LoadSourceSpanReference(ctx, fixture.workspaceID, fixture.sourceVersionID, fixture.spanID)
	if err != nil {
		t.Fatal(err)
	}
	if !sameImmutableSourceVersionReference(span.SourceVersion, sourceVersion) ||
		span.SourceVersion.IngestionStatus != "" || span.SourceVersion.WorkflowStatus != "" || span.SourceVersion.IndexStatus != "" ||
		span.ParseProjectionID != fixture.projectionID || span.Span.ID != fixture.spanID ||
		span.Span.StartLine != 1 || span.Span.EndLine != 1 || span.Span.StartByte != 0 || span.Span.EndByte != fixture.byteSize ||
		span.SpanType != "paragraph" || string(span.Selector) != `{"kind": "paragraph"}` ||
		span.ExcerptHash != fixture.contentHash || span.ParserVersion != "goldmark-v1" || span.SchemaVersion != "schema-v1" {
		t.Fatalf("source span reference = %#v", span)
	}
	citation, err := repository.LoadCitationSourceSpanReference(ctx, domain.CitationReferenceQuery{
		WorkspaceID: fixture.workspaceID, IndexVersionID: fixture.indexID, ChunkID: fixture.chunkID,
		SourceVersionID: fixture.sourceVersionID, SourceSpanID: fixture.spanID,
	})
	if err != nil || citation.Span.ID != fixture.spanID || citation.ParseProjectionID != fixture.projectionID {
		t.Fatalf("citation reference=%+v err=%v", citation, err)
	}
}

func TestEvidenceReferenceStoreLoadsDerivedTextEvidence(t *testing.T) {
	_, database, ctx := newRetrievalTestRepository(t)
	repository, err := NewSearchRepository(database.DB())
	if err != nil {
		t.Fatal(err)
	}
	fixture := seedEvidenceReferenceFixture(t, ctx, database.DB())
	derived := "Java AI derived PDF evidence"
	digest := sha256.Sum256([]byte(derived))
	const derivedSpanID foundation.ID = "95000000-0000-4000-8000-000000000030"
	if _, err := database.DB().Exec(ctx, `INSERT INTO ingestion.source_span(
		id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,
		selector,excerpt_hash,evidence_kind,derived_excerpt,parser_version,schema_version,created_at)
		VALUES($1,$2,$3,$4,'document',1,1,0,$5,'{"format":"pdf","page_start":"1","page_end":"1"}',
		$6,'derived_text',$7,'goldmark-v1','schema-v1',$8)`, string(derivedSpanID), string(fixture.workspaceID),
		string(fixture.artifactID), string(fixture.projectionID), fixture.byteSize, hex.EncodeToString(digest[:]), derived, fixture.capturedAt); err != nil {
		t.Fatal(err)
	}
	reference, err := repository.LoadSourceSpanReference(ctx, fixture.workspaceID, fixture.sourceVersionID, derivedSpanID)
	if err != nil {
		t.Fatal(err)
	}
	if reference.EvidenceKind != domain.EvidenceDerivedText || reference.DerivedExcerpt != derived ||
		reference.ExcerptHash != hex.EncodeToString(digest[:]) || reference.Span.StartByte != 0 || reference.Span.EndByte != fixture.byteSize {
		t.Fatalf("derived reference = %#v", reference)
	}
}

func TestEvidenceReferenceStoreUsesLatestAttemptSecurityStatus(t *testing.T) {
	_, database, ctx := newRetrievalTestRepository(t)
	repository, err := NewSearchRepository(database.DB())
	if err != nil {
		t.Fatal(err)
	}
	fixture := seedEvidenceReferenceFixture(t, ctx, database.DB())
	latestStartedAt := fixture.capturedAt.Add(2 * time.Minute)
	if _, err := database.DB().Exec(ctx, `INSERT INTO ingestion.attempt(
		id,workspace_id,source_version_id,status,security_status,parser_id,parser_version,
		parser_config_hash,chunk_strategy_version,schema_version,idempotency_key,attempt_number,started_at,completed_at)
		VALUES('95000000-0000-4000-8000-000000000029',$1,$2,'validating','quarantined','goldmark','goldmark-v1',$3,'chunk-v1','schema-v1','evidence-quarantine',2,$4,$4)`,
		string(fixture.workspaceID), string(fixture.sourceVersionID), hex64('d'), latestStartedAt); err != nil {
		t.Fatal(err)
	}
	reference, err := repository.LoadSourceVersionReference(ctx, fixture.workspaceID, fixture.sourceVersionID)
	if err != nil {
		t.Fatal(err)
	}
	if reference.SecurityStatus != "quarantined" || reference.IngestionStatus != "validating" || reference.WorkflowStatus != "" {
		t.Fatalf("latest attempt status was not projected: %#v", reference)
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
		{name: "citation wrong index", load: func() error {
			_, err := repository.LoadCitationSourceSpanReference(ctx, domain.CitationReferenceQuery{
				WorkspaceID: fixture.workspaceID, IndexVersionID: "95000000-0000-4000-8000-000000000099", ChunkID: fixture.chunkID,
				SourceVersionID: fixture.sourceVersionID, SourceSpanID: fixture.spanID,
			})
			return err
		}},
		{name: "citation wrong chunk", load: func() error {
			_, err := repository.LoadCitationSourceSpanReference(ctx, domain.CitationReferenceQuery{
				WorkspaceID: fixture.workspaceID, IndexVersionID: fixture.indexID, ChunkID: fixture.otherSpanID,
				SourceVersionID: fixture.sourceVersionID, SourceSpanID: fixture.spanID,
			})
			return err
		}},
		{name: "citation cross workspace", load: func() error {
			_, err := repository.LoadCitationSourceSpanReference(ctx, domain.CitationReferenceQuery{
				WorkspaceID: fixture.otherWorkspaceID, IndexVersionID: fixture.indexID, ChunkID: fixture.chunkID,
				SourceVersionID: fixture.sourceVersionID, SourceSpanID: fixture.spanID,
			})
			return err
		}},
		{name: "citation wrong source version", load: func() error {
			_, err := repository.LoadCitationSourceSpanReference(ctx, domain.CitationReferenceQuery{
				WorkspaceID: fixture.workspaceID, IndexVersionID: fixture.indexID, ChunkID: fixture.chunkID,
				SourceVersionID: fixture.damagedSourceVersionID, SourceSpanID: fixture.spanID,
			})
			return err
		}},
		{name: "citation wrong span", load: func() error {
			_, err := repository.LoadCitationSourceSpanReference(ctx, domain.CitationReferenceQuery{
				WorkspaceID: fixture.workspaceID, IndexVersionID: fixture.indexID, ChunkID: fixture.chunkID,
				SourceVersionID: fixture.sourceVersionID, SourceSpanID: fixture.otherSpanID,
			})
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

func TestEvidenceReferenceStoreLoadsFiveHundredCitationsInInputOrderAndFailsClosed(t *testing.T) {
	_, database, ctx := newRetrievalTestRepository(t)
	repository, err := NewSearchRepository(database.DB())
	if err != nil {
		t.Fatal(err)
	}
	fixture := seedEvidenceReferenceFixture(t, ctx, database.DB())
	queries := seedCitationReferenceBatch(t, ctx, database.DB(), fixture, 500)
	for left, right := 0, len(queries)-1; left < right; left, right = left+1, right-1 {
		queries[left], queries[right] = queries[right], queries[left]
	}

	bindings, err := repository.LoadCitationSourceSpanReferences(ctx, queries)
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != len(queries) {
		t.Fatalf("bindings=%d want=%d", len(bindings), len(queries))
	}
	for index, binding := range bindings {
		if binding.Query != queries[index] || binding.Reference.Span.ID != queries[index].SourceSpanID ||
			binding.Reference.SourceVersion.SourceVersionID != queries[index].SourceVersionID {
			t.Fatalf("binding[%d]=%+v want_query=%+v", index, binding, queries[index])
		}
	}

	tests := []struct {
		name      string
		mutate    func(*domain.CitationReferenceQuery)
		kind      foundation.ErrorKind
		errorCode string
	}{
		{name: "workspace", mutate: func(query *domain.CitationReferenceQuery) {
			query.WorkspaceID = "98000000-0000-4000-8000-000000000001"
		}, kind: foundation.ErrorInvalidInput, errorCode: domain.ErrorCodeEvidenceReferenceInvalid},
		{name: "index version", mutate: func(query *domain.CitationReferenceQuery) {
			query.IndexVersionID = "98000000-0000-4000-8000-000000000002"
		}, kind: foundation.ErrorInvalidInput, errorCode: domain.ErrorCodeEvidenceReferenceInvalid},
		{name: "chunk", mutate: func(query *domain.CitationReferenceQuery) {
			query.ChunkID = "98000000-0000-4000-8000-000000000003"
		}, kind: foundation.ErrorNotFound, errorCode: evidenceReferenceNotFoundCode},
		{name: "source version", mutate: func(query *domain.CitationReferenceQuery) {
			query.SourceVersionID = "98000000-0000-4000-8000-000000000004"
		}, kind: foundation.ErrorNotFound, errorCode: evidenceReferenceNotFoundCode},
		{name: "source span", mutate: func(query *domain.CitationReferenceQuery) {
			query.SourceSpanID = "98000000-0000-4000-8000-000000000005"
		}, kind: foundation.ErrorNotFound, errorCode: evidenceReferenceNotFoundCode},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutated := append([]domain.CitationReferenceQuery(nil), queries...)
			test.mutate(&mutated[len(mutated)/2])
			loaded, loadErr := repository.LoadCitationSourceSpanReferences(ctx, mutated)
			if len(loaded) != 0 {
				t.Fatalf("loaded=%d want=0", len(loaded))
			}
			requireEvidenceError(t, loadErr, test.kind, test.errorCode)
		})
	}
}

type evidenceReferenceFixture struct {
	workspaceID            foundation.ID
	otherWorkspaceID       foundation.ID
	sourceID               foundation.ID
	sourceVersionID        foundation.ID
	artifactID             foundation.ID
	projectionID           foundation.ID
	spanID                 foundation.ID
	indexID                foundation.ID
	chunkID                foundation.ID
	otherSpanID            foundation.ID
	damagedSourceVersionID foundation.ID
	damagedSpanID          foundation.ID
	contentHash            string
	content                []byte
	byteSize               int64
	capturedAt             time.Time
}

func seedEvidenceReferenceFixture(t *testing.T, ctx context.Context, database *pgxpool.Pool) evidenceReferenceFixture {
	t.Helper()
	content := []byte(strings.Repeat("immutable evidence ", 32))
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
		indexID:                "95000000-0000-4000-8000-000000000008",
		chunkID:                "95000000-0000-4000-8000-000000000009",
		otherSpanID:            "95000000-0000-4000-8000-000000000017",
		damagedSourceVersionID: "95000000-0000-4000-8000-000000000025",
		damagedSpanID:          "95000000-0000-4000-8000-000000000027",
		contentHash:            hash,
		content:                append([]byte(nil), content...),
		byteSize:               int64(len(content)),
		capturedAt:             now,
	}
	statements := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO core.workspace(
			id,name,root_path,root_fingerprint,binding_version,git_repository_path,git_checked_at,status,
			availability,availability_reason,availability_checked_at,version,created_at,updated_at)
		  VALUES($1,'evidence','/tmp/evidence',$2,1,'/tmp/evidence',$3,'inactive','available',NULL,$3,1,$3,$3)`,
			[]any{string(fixture.workspaceID), hex64('1'), now}},
		{`INSERT INTO core.workspace(
			id,name,root_path,root_fingerprint,binding_version,git_repository_path,git_checked_at,status,
			availability,availability_reason,availability_checked_at,version,created_at,updated_at)
		  VALUES($1,'other','/tmp/evidence-other',$2,1,'/tmp/evidence-other',$3,'inactive','available',NULL,$3,1,$3,$3)`,
			[]any{string(fixture.otherWorkspaceID), hex64('2'), now}},
		{`INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
		  VALUES('95000000-0000-4000-8000-000000000010',$1,'evidence-ingestion',1,'{}',$2)`, []any{string(fixture.workspaceID), now}},
		{`INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at)
		  VALUES('95000000-0000-4000-8000-000000000011',$1,'95000000-0000-4000-8000-000000000010','running','{}',1,$2,$2)`, []any{string(fixture.workspaceID), now}},
		{`INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at)
		  VALUES($1,$2,'file','Evidence','docs/evidence.md',$3)`, []any{string(fixture.sourceID), string(fixture.workspaceID), now}},
		{`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at)
		  VALUES($1,$2,$3,$4,'.knowledge/sources/' || $3,$5)`, []any{string(fixture.artifactID), string(fixture.workspaceID), hash, fixture.byteSize, now}},
		{`INSERT INTO core.source_version(id,source_id,workspace_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at,content_artifact_id)
		  VALUES($1,$2,$3,$4,$5,'text/markdown','docs/evidence.md','passed',$6,$7)`, []any{string(fixture.sourceVersionID), string(fixture.sourceID), string(fixture.workspaceID), hash, fixture.byteSize, now, string(fixture.artifactID)}},
		{`INSERT INTO ingestion.parse_projection(id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,created_at)
		  VALUES($1,$2,$3,'goldmark','goldmark-v1',$4,'schema-v1',$5,$6)`, []any{string(fixture.projectionID), string(fixture.workspaceID), string(fixture.artifactID), hex64('a'), hex64('b'), now}},
		{`INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at)
		  VALUES($1,$2,$3,$4)`, []any{string(fixture.sourceVersionID), string(fixture.projectionID), string(fixture.workspaceID), now}},
		{`INSERT INTO ingestion.attempt(
			id,workspace_id,source_version_id,parse_projection_id,status,security_status,parser_id,parser_version,
			parser_config_hash,chunk_strategy_version,schema_version,idempotency_key,attempt_number,started_at,completed_at)
		  VALUES('95000000-0000-4000-8000-000000000012',$1,$2,$3,'chunked','passed','goldmark','goldmark-v1',$4,'chunk-v1','schema-v1','evidence-indexed',1,$5,$5)`,
			[]any{string(fixture.workspaceID), string(fixture.sourceVersionID), string(fixture.projectionID), hex64('a'), now}},
		{`INSERT INTO ingestion.attempt(
			id,workspace_id,source_version_id,workflow_run_id,parse_projection_id,status,security_status,parser_id,parser_version,
			parser_config_hash,chunk_strategy_version,schema_version,idempotency_key,attempt_number,started_at)
		  VALUES('95000000-0000-4000-8000-000000000018',$1,$2,'95000000-0000-4000-8000-000000000011',$3,'parsed','passed','goldmark','goldmark-v1',$4,'chunk-v1','schema-v1','evidence-latest',1,$5)`,
			[]any{string(fixture.workspaceID), string(fixture.sourceVersionID), string(fixture.projectionID), hex64('a'), now.Add(time.Minute)}},
		{`INSERT INTO ingestion.source_span(id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,selector,excerpt_hash,parser_version,schema_version,created_at)
		  VALUES($1,$2,$3,$4,'paragraph',1,1,0,$5,'{"kind": "paragraph"}'::jsonb,$6,'goldmark-v1','schema-v1',$7)`, []any{string(fixture.spanID), string(fixture.workspaceID), string(fixture.artifactID), string(fixture.projectionID), fixture.byteSize, hash, now}},
		{`INSERT INTO ingestion.canonical_chunk(id,workspace_id,parse_projection_id,sequence,heading_path,content,content_hash,source_span_id,byte_count,rune_count,parser_version,chunk_strategy_version,schema_version,atomic_oversized,status,created_at)
		  VALUES($1,$2,$3,0,'[]',$4,$5,$6,$7,$7,'goldmark-v1','chunk-v1','schema-v1',false,'active',$8)`, []any{string(fixture.chunkID), string(fixture.workspaceID), string(fixture.projectionID), string(content), hash, string(fixture.spanID), fixture.byteSize, now}},
		{`INSERT INTO retrieval.index_version(id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,version,created_at,updated_at,source_manifest_hash,expected_source_count,source_parser_id,source_parser_version,source_parser_config_hash,source_chunk_strategy_version,source_schema_version)
		  VALUES($1,$2,'default','v1',$3,'{}','evidence-fixture',$4,1,'evidence-fixture','building','["vector"]',1,$5,$5,$6,1,'goldmark','goldmark-v1',$3,'chunk-v1','schema-v1')`, []any{string(fixture.indexID), string(fixture.workspaceID), hex64('a'), hex64('b'), now, hex64('c')}},
		{`INSERT INTO retrieval.index_manifest_chunk(index_version_id,chunk_id,workspace_id,content_hash,sequence,parser_version,chunk_strategy_version,schema_version,created_at)
		  VALUES($1,$2,$3,$4,0,'goldmark-v1','chunk-v1','schema-v1',$5)`, []any{string(fixture.indexID), string(fixture.chunkID), string(fixture.workspaceID), hash, now}},
		{`INSERT INTO retrieval.index_manifest_source(index_version_id,workspace_id,source_id,source_version_id,parse_projection_id,selection_status,created_at)
		  VALUES($1,$2,$3,$4,$5,'included',$6)`, []any{string(fixture.indexID), string(fixture.workspaceID), string(fixture.sourceID), string(fixture.sourceVersionID), string(fixture.projectionID), now}},
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

func activateEvidenceReferenceFixture(
	t *testing.T,
	ctx context.Context,
	database *pgxpool.Pool,
	fixture evidenceReferenceFixture,
) {
	t.Helper()
	if _, err := database.Exec(ctx, `INSERT INTO retrieval.chunk_projection(
		index_version_id,chunk_id,workspace_id,search_vector,token_count,lexical_status,vector_status,created_at,updated_at)
		VALUES($1,$2,$3,to_tsvector('simple',$4::text),cardinality(tsvector_to_array(to_tsvector('simple',$4::text))),'ready','disabled',$5,$5)`,
		string(fixture.indexID), string(fixture.chunkID), string(fixture.workspaceID), string(fixture.content), fixture.capturedAt); err != nil {
		t.Fatal(err)
	}
	builtAt := fixture.capturedAt.Add(2 * time.Minute)
	if _, err := database.Exec(ctx, `UPDATE retrieval.index_version
		SET status='ready',version=2,built_at=$2,updated_at=$2 WHERE id=$1`, string(fixture.indexID), builtAt); err != nil {
		t.Fatal(err)
	}
	activatedAt := fixture.capturedAt.Add(3 * time.Minute)
	if err := pgx.BeginFunc(ctx, database, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO retrieval.index_activation(
			id,kind,workspace_id,target_index_version_id,target_version,idempotency_key,reason_code,created_at)
			VALUES('95000000-0000-4000-8000-000000000019','activate',$1,$2,3,'evidence-activate','evidence-reference-test',$3)`,
			string(fixture.workspaceID), string(fixture.indexID), activatedAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE retrieval.index_version
			SET status='active',version=3,activated_at=$2,updated_at=$2 WHERE id=$1`, string(fixture.indexID), activatedAt); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func seedCitationReferenceBatch(
	t *testing.T,
	ctx context.Context,
	database *pgxpool.Pool,
	fixture evidenceReferenceFixture,
	count int,
) []domain.CitationReferenceQuery {
	t.Helper()
	if count < 1 || count > 500 {
		t.Fatalf("citation batch count=%d", count)
	}
	if count-1 > len(fixture.content) {
		t.Fatalf("citation batch count=%d exceeds fixture bytes=%d", count, len(fixture.content))
	}
	queries := make([]domain.CitationReferenceQuery, count)
	queries[0] = domain.CitationReferenceQuery{
		WorkspaceID: fixture.workspaceID, IndexVersionID: fixture.indexID, ChunkID: fixture.chunkID,
		SourceVersionID: fixture.sourceVersionID, SourceSpanID: fixture.spanID,
	}
	batch := &pgx.Batch{}
	for index := 1; index < count; index++ {
		chunkID := foundation.ID(fmt.Sprintf("96%06d-0000-4000-8000-%012d", index, index))
		spanID := foundation.ID(fmt.Sprintf("97%06d-0000-4000-8000-%012d", index, index))
		excerpt := fixture.content[index-1 : index]
		digest := sha256.Sum256(excerpt)
		excerptHash := hex.EncodeToString(digest[:])
		queries[index] = domain.CitationReferenceQuery{
			WorkspaceID: fixture.workspaceID, IndexVersionID: fixture.indexID, ChunkID: chunkID,
			SourceVersionID: fixture.sourceVersionID, SourceSpanID: spanID,
		}
		batch.Queue(`INSERT INTO ingestion.source_span(
			id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,
			selector,excerpt_hash,parser_version,schema_version,created_at)
			VALUES($1,$2,$3,$4,'paragraph',1,1,$5,$6,'{"kind": "paragraph"}'::jsonb,$7,'goldmark-v1','schema-v1',$8)`,
			string(spanID), string(fixture.workspaceID), string(fixture.artifactID), string(fixture.projectionID),
			index-1, index, excerptHash, fixture.capturedAt)
		batch.Queue(`INSERT INTO ingestion.canonical_chunk(
			id,workspace_id,parse_projection_id,sequence,heading_path,content,content_hash,source_span_id,byte_count,rune_count,
			parser_version,chunk_strategy_version,schema_version,atomic_oversized,status,created_at)
			VALUES($1,$2,$3,$4,'[]',$5,$6,$7,$8,$8,'goldmark-v1','chunk-v1','schema-v1',false,'active',$9)`,
			string(chunkID), string(fixture.workspaceID), string(fixture.projectionID), index, string(excerpt), excerptHash,
			string(spanID), int64(len(excerpt)), fixture.capturedAt)
		batch.Queue(`INSERT INTO retrieval.index_manifest_chunk(
			index_version_id,chunk_id,workspace_id,content_hash,sequence,parser_version,chunk_strategy_version,schema_version,created_at)
			VALUES($1,$2,$3,$4,$5,'goldmark-v1','chunk-v1','schema-v1',$6)`,
			string(fixture.indexID), string(chunkID), string(fixture.workspaceID), excerptHash, index, fixture.capturedAt)
	}
	results := database.SendBatch(ctx, batch)
	for range (count - 1) * 3 {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			t.Fatal(err)
		}
	}
	if err := results.Close(); err != nil {
		t.Fatal(err)
	}
	return queries
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
		{`INSERT INTO core.source_version(id,source_id,workspace_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at,content_artifact_id) VALUES($1,$2,$3,$4,1,'text/plain','docs/other.md','passed',$5,$6)`, []any{versionID, sourceID, string(fixture.workspaceID), hex64('c'), now, artifactID}},
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
		{`INSERT INTO core.source_version(id,source_id,workspace_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at,content_artifact_id) VALUES($1,$2,$3,$4,1,'text/plain','docs/different.md','passed',$5,$6)`, []any{string(fixture.damagedSourceVersionID), sourceID, string(fixture.workspaceID), hex64('f'), now, artifactID}},
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

func sameImmutableSourceVersionReference(left, right domain.SourceVersionReference) bool {
	return left.WorkspaceID == right.WorkspaceID && left.SourceID == right.SourceID &&
		left.SourceVersionID == right.SourceVersionID && left.ContentArtifactID == right.ContentArtifactID &&
		left.SourceType == right.SourceType && left.LogicalName == right.LogicalName && left.RelativePath == right.RelativePath &&
		left.ContentHash == right.ContentHash && left.ByteSize == right.ByteSize && left.MediaType == right.MediaType &&
		left.SecurityStatus == right.SecurityStatus && left.CapturedAt.Equal(right.CapturedAt)
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
