//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	artifactlearningpath "github.com/CodeZen-Lizhi/zhixu/internal/artifact/adapter/learningpath"
	artifactpostgres "github.com/CodeZen-Lizhi/zhixu/internal/artifact/adapter/postgres"
	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactdomain "github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	reviewdomain "github.com/CodeZen-Lizhi/zhixu/internal/review/domain"
	pathartifact "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/adapter/artifact"
	pathapp "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/application"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type reviewPathFixture struct {
	workspaceID     foundation.ID
	answerID        foundation.ID
	cardID          foundation.ID
	claimID         foundation.ID
	sourceVersionID foundation.ID
	sourceSpanID    foundation.ID
	indexVersionID  foundation.ID
	chunkID         foundation.ID
	contentHash     string
}

func newReviewPathTestDatabase(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	fixture := testdb.Require(t, testdb.Config{MaxConns: 16, Availability: testdb.FailWhenUnavailable})
	platform := fixture.Pool()
	if platform == nil || platform.DB() == nil {
		t.Fatal("shared PostgreSQL fixture did not provide a platform pool")
	}
	return platform.DB()
}

// runReviewPathIntegrationVariants exercises legacy and staged stores against
// isolated migrated databases supplied by the shared platform Pool factory.
func runReviewPathIntegrationVariants(t *testing.T, scenario func(*testing.T, context.Context, *pgxpool.Pool, reviewPathFixture, pathapp.Store)) {
	t.Helper()
	for _, name := range []string{"legacy-pgx", "gorm"} {
		name := name
		t.Run(name, func(t *testing.T) {
			runReviewPathIntegrationVariant(t, name, scenario)
		})
	}
}

func runReviewPathGORMIntegration(t *testing.T, scenario func(*testing.T, context.Context, *pgxpool.Pool, reviewPathFixture, pathapp.Store)) {
	t.Helper()
	runReviewPathIntegrationVariant(t, "gorm", scenario)
}

func runReviewPathIntegrationVariant(
	t *testing.T,
	name string,
	scenario func(*testing.T, context.Context, *pgxpool.Pool, reviewPathFixture, pathapp.Store),
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := testdb.Require(t, testdb.Config{MaxConns: 16, Availability: testdb.FailWhenUnavailable})
	platform := fixture.Pool()
	if platform == nil || platform.DB() == nil {
		t.Fatal("shared PostgreSQL fixture did not provide a platform pool")
	}
	var store pathapp.Store
	switch name {
	case "legacy-pgx":
		legacy, err := NewRepository(platform.DB())
		if err != nil {
			t.Fatal(err)
		}
		store = legacy
	case "gorm":
		if _, err := platform.GORM(); err != nil {
			t.Fatalf("shared PostgreSQL fixture did not provide a GORM root: %v", err)
		}
		if _, err := platform.UnitOfWork(); err != nil {
			t.Fatalf("shared PostgreSQL fixture did not provide a Unit of Work: %v", err)
		}
		gormStore, err := NewGORMRepository(platform)
		if err != nil {
			t.Fatal(err)
		}
		store = gormStore
	default:
		t.Fatalf("unknown Review Learning Path integration variant %q", name)
	}
	scenario(t, ctx, platform.DB(), seedReviewPathFixture(t, ctx, platform.DB()), store)
}

func seedReviewPathFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) reviewPathFixture {
	t.Helper()
	fixture := reviewPathFixture{
		workspaceID:     reviewPathIntegrationID(1),
		answerID:        reviewPathIntegrationID(15),
		cardID:          reviewPathIntegrationID(13),
		claimID:         reviewPathIntegrationID(8),
		sourceVersionID: reviewPathIntegrationID(4),
		sourceSpanID:    reviewPathIntegrationID(6),
		indexVersionID:  reviewPathIntegrationID(10),
		chunkID:         reviewPathIntegrationID(7),
		contentHash:     reviewPathHash("review-path-source-content"),
	}
	now := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	content := "A Review Learning Path closes its reservation and hidden hold atomically."
	evidenceHash := reviewPathHash("review-path-claim-source")
	evidence := reviewdomain.EvidenceBinding{
		SchemaVersion: reviewdomain.EvidenceSchemaVersionV1,
		ClaimID:       fixture.claimID, SourceVersionID: fixture.sourceVersionID,
		SourceSpanID: fixture.sourceSpanID, EvidenceHash: evidenceHash,
	}
	evidenceJSON, err := json.Marshal([]reviewdomain.EvidenceBinding{evidence})
	if err != nil {
		t.Fatal(err)
	}
	dimension := func(value float64, rationale string) reviewdomain.ScoreDimension {
		return reviewdomain.ScoreDimension{Value: value, Rationale: rationale}
	}
	scoreJSON, err := json.Marshal(reviewdomain.Score{
		SchemaVersion: reviewdomain.ScoreSchemaVersionV1,
		Correctness:   dimension(0.40, "The answer omitted the transaction fence."),
		Coverage:      dimension(0.50, "The answer covered only the reservation."),
		Boundaries:    dimension(0.50, "The Artifact visibility boundary was missing."),
		Clarity:       dimension(0.80, "The answer was otherwise clear."),
		Confidence:    dimension(0.70, "The score is supported by the frozen citation."),
		Errors:        []string{"The response did not close the hidden hold."},
		Omissions:     []string{},
		Evidence:      []reviewdomain.EvidenceBinding{evidence},
	})
	if err != nil {
		t.Fatal(err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO core.workspace(
			id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
		) VALUES($1,'review-path-integration','/tmp/review-path-integration','/tmp/review-path-integration',$2,'active',1,$2,$2)`, []any{string(fixture.workspaceID), now}},
		{`INSERT INTO core.content_artifact(
			id,workspace_id,content_hash,byte_size,managed_location,created_at
		) VALUES($1,$2,$3,$4,$5,$6)`, []any{string(reviewPathIntegrationID(2)), string(fixture.workspaceID), fixture.contentHash, len([]byte(content)), ".knowledge/sources/" + fixture.contentHash, now}},
		{`INSERT INTO core.source(
			id,workspace_id,type,logical_name,original_location,created_at
		) VALUES($1,$2,'text','review-path.txt','review-path.txt',$3)`, []any{string(reviewPathIntegrationID(3)), string(fixture.workspaceID), now}},
		{`INSERT INTO core.source_version(
			id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,
			original_content_location,security_status,captured_at
		) VALUES($1,$2,$3,$4,$5,$6,'text/plain','review-path.txt','passed',$7)`, []any{string(fixture.sourceVersionID), string(reviewPathIntegrationID(3)), string(fixture.workspaceID), string(reviewPathIntegrationID(2)), fixture.contentHash, len([]byte(content)), now}},
		{`INSERT INTO ingestion.parse_projection(
			id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,
			schema_version,normalized_content_hash,warnings,created_at
		) VALUES($1,$2,$3,'text','v1',$4,'v1',$5,'[]',$6)`, []any{string(reviewPathIntegrationID(5)), string(fixture.workspaceID), string(reviewPathIntegrationID(2)), reviewPathHash("review-path-parser"), reviewPathHash("review-path-normalized"), now}},
		{`INSERT INTO ingestion.source_span(
			id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,
			start_byte,end_byte,selector,excerpt_hash,parser_version,schema_version,created_at
		) VALUES($1,$2,$3,$4,'paragraph',1,1,0,$5,'{}',$6,'v1','v1',$7)`, []any{string(fixture.sourceSpanID), string(fixture.workspaceID), string(reviewPathIntegrationID(2)), string(reviewPathIntegrationID(5)), len([]byte(content)), reviewPathHash("review-path-excerpt"), now}},
		{`INSERT INTO ingestion.source_version_projection(
			source_version_id,parse_projection_id,workspace_id,created_at
		) VALUES($1,$2,$3,$4)`, []any{string(fixture.sourceVersionID), string(reviewPathIntegrationID(5)), string(fixture.workspaceID), now}},
		{`INSERT INTO ingestion.attempt(
			id,workspace_id,source_version_id,parse_projection_id,status,security_status,parser_id,
			parser_version,parser_config_hash,chunk_strategy_version,schema_version,idempotency_key,
			attempt_number,started_at,completed_at
		) VALUES($1,$2,$3,$4,'chunked','passed','text','v1',$5,'structure-v1','v1',
			'review-path-ingestion',1,$6,$6)`, []any{string(reviewPathIntegrationID(16)), string(fixture.workspaceID), string(fixture.sourceVersionID), string(reviewPathIntegrationID(5)), reviewPathHash("review-path-parser"), now}},
		{`INSERT INTO ingestion.canonical_chunk(
			id,workspace_id,parse_projection_id,sequence,heading_path,content,content_hash,source_span_id,
			byte_count,rune_count,parser_version,chunk_strategy_version,schema_version,atomic_oversized,status,created_at
		) VALUES($1,$2,$3,0,'["Review Path"]',$4,$5,$6,$7,$8,'v1','structure-v1','v1',false,'active',$9)`, []any{string(fixture.chunkID), string(fixture.workspaceID), string(reviewPathIntegrationID(5)), content, fixture.contentHash, string(fixture.sourceSpanID), len([]byte(content)), utf8.RuneCountInString(content), now}},
		{`INSERT INTO core.claim(
			id,workspace_id,statement,normalized_statement,applicability,applicability_schema_version,
			applicability_hash,status,confidence_factors,fingerprint,version,created_at,updated_at
		) VALUES($1,$2,$3,$4,'{}','knowledge-applicability/v1',$5,'SUGGESTED','{}',$6,1,$7,$7)`, []any{string(fixture.claimID), string(fixture.workspaceID), content, strings.ToLower(content), reviewPathHash("review-path-applicability"), reviewPathHash("review-path-claim"), now}},
		{`INSERT INTO core.claim_source(
			id,workspace_id,claim_id,source_version_id,source_span_id,support_type,reason,evidence_hash,created_at
		) VALUES($1,$2,$3,$4,$5,'SUPPORTS','Review Path integration evidence.',$6,$7)`, []any{string(reviewPathIntegrationID(9)), string(fixture.workspaceID), string(fixture.claimID), string(fixture.sourceVersionID), string(fixture.sourceSpanID), evidenceHash, now}},
		{`UPDATE core.claim
			SET status='CONFIRMED',version=2,updated_at=$3
			WHERE workspace_id=$1 AND id=$2`, []any{string(fixture.workspaceID), string(fixture.claimID), now.Add(time.Second)}},
		{`INSERT INTO retrieval.index_version(
			id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
			source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,
			degraded_capabilities,version,created_at,updated_at,source_manifest_hash,
			expected_source_count,source_parser_id,source_parser_version,source_parser_config_hash,
			source_chunk_strategy_version,source_schema_version
		) VALUES($1,$2,'simple','v1',$3,'{}','review-path-source-snapshot',$4,1,
			'review-path-index','building','["vector"]',1,$5,$5,$6,1,'text','v1',$3,'structure-v1','v1')`, []any{string(fixture.indexVersionID), string(fixture.workspaceID), reviewPathHash("review-path-parser"), reviewPathHash("review-path-manifest"), now, reviewPathHash("review-path-source-manifest")}},
		{`INSERT INTO retrieval.index_manifest_chunk(
			index_version_id,chunk_id,workspace_id,content_hash,sequence,parser_version,
			chunk_strategy_version,schema_version,created_at
		) VALUES($1,$2,$3,$4,0,'v1','structure-v1','v1',$5)`, []any{string(fixture.indexVersionID), string(fixture.chunkID), string(fixture.workspaceID), fixture.contentHash, now}},
		{`INSERT INTO retrieval.index_manifest_source(
			index_version_id,workspace_id,source_id,source_version_id,parse_projection_id,
			selection_status,exclusion_code,created_at
		) VALUES($1,$2,$3,$4,$5,'included',NULL,$6)`, []any{string(fixture.indexVersionID), string(fixture.workspaceID), string(reviewPathIntegrationID(3)), string(fixture.sourceVersionID), string(reviewPathIntegrationID(5)), now}},
		{`INSERT INTO retrieval.chunk_projection(
			index_version_id,chunk_id,workspace_id,search_vector,token_count,lexical_status,
			vector_status,created_at,updated_at
		) VALUES($1,$2,$3,to_tsvector('simple',$4),12,'ready','disabled',$5,$5)`, []any{string(fixture.indexVersionID), string(fixture.chunkID), string(fixture.workspaceID), content, now}},
		{`INSERT INTO learning.review_deck(
			id,workspace_id,name,scope,status,daily_limit,scheduler_version,version,created_at,updated_at
		) VALUES($1,$2,'Review Path Deck','{}','ACTIVE',20,'fsrs/v1',1,$3,$3)`, []any{string(reviewPathIntegrationID(12)), string(fixture.workspaceID), now}},
		{`INSERT INTO learning.review_card(
			id,workspace_id,deck_id,claim_id,question,answer_points,evidence,card_type,difficulty,
			status,fingerprint,model_version,version,created_at,updated_at
		) VALUES($1,$2,$3,$4,'How is a Review Path completed atomically?','["Close every durable fact"]',$5,
			'SHORT_ANSWER',0.5,'APPROVED',$6,'manual',1,$7,$7)`, []any{string(fixture.cardID), string(fixture.workspaceID), string(reviewPathIntegrationID(12)), string(fixture.claimID), evidenceJSON, reviewPathHash("review-path-card"), now}},
		{`INSERT INTO learning.review_schedule(
			card_id,workspace_id,due_at,interval_days,stability,difficulty,last_reviewed_at,
			scheduler_version,paused,version
		) VALUES($1,$2,$3,0,0,0.5,NULL,'fsrs/v1',false,1)`, []any{string(fixture.cardID), string(fixture.workspaceID), now}},
		{`INSERT INTO learning.review_session(
			id,workspace_id,deck_id,session_type,status,config,idempotency_key,request_hash,started_at,ended_at
		) VALUES($1,$2,$3,'REVIEW','ACTIVE','{}','review-path-session',$4,$5,NULL)`, []any{string(reviewPathIntegrationID(14)), string(fixture.workspaceID), string(reviewPathIntegrationID(12)), reviewPathHash("review-path-session"), now}},
		{`INSERT INTO learning.review_answer(
			id,workspace_id,session_id,card_id,question_ref,idempotency_key,user_answer,
			scorer_version,score,feedback,rating,schedule_snapshot,request_hash,created_at
		) VALUES($1,$2,$3,$4,'review-path-question-ref','review-path-answer',
			'Only the reservation needs to be completed.','review-deterministic/v1',$5,
			'{"summary":"actionable gap"}',2,'{"version":2}',$6,$7)`, []any{string(fixture.answerID), string(fixture.workspaceID), string(reviewPathIntegrationID(14)), string(fixture.cardID), scoreJSON, reviewPathHash("review-path-answer"), now}},
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("seed Review Path fixture: %v\nSQL: %s", err, statement.query)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	readyAt := now.Add(2 * time.Minute)
	if _, err := pool.Exec(ctx, `UPDATE retrieval.index_version
		SET status='ready',version=2,built_at=$3,updated_at=$3
		WHERE workspace_id=$1 AND id=$2`, string(fixture.workspaceID), string(fixture.indexVersionID), readyAt); err != nil {
		t.Fatal(err)
	}
	activatedAt := now.Add(3 * time.Minute)
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO retrieval.index_activation(
			id,kind,workspace_id,target_index_version_id,target_version,idempotency_key,reason_code,created_at
		) VALUES($1,'activate',$2,$3,3,'review-path-activate','review-path-fixture',$4)`, string(reviewPathIntegrationID(11)), string(fixture.workspaceID), string(fixture.indexVersionID), activatedAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE retrieval.index_version
			SET status='active',version=3,activated_at=$3,updated_at=$3
			WHERE workspace_id=$1 AND id=$2`, string(fixture.workspaceID), string(fixture.indexVersionID), activatedAt); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func newReviewPathProductionBridge(
	t *testing.T,
	db artifactpostgres.DB,
	contentHash string,
) (*pathartifact.Bridge, *artifactpostgres.Repository) {
	t.Helper()
	repository, err := artifactpostgres.NewRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	commands, err := artifactapp.NewCommandService(artifactapp.Dependencies{
		Repository: repository,
		Evidence:   reviewPathCitationVerifier{contentHash: contentHash},
		IDs:        foundation.NewUUIDGenerator(nil),
		Clock:      foundation.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	creator, err := artifactlearningpath.NewCreator(commands)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := pathartifact.NewBridge(creator)
	if err != nil {
		t.Fatal(err)
	}
	return bridge, repository
}

func newReviewPathService(t *testing.T, store pathapp.Store, bridge pathapp.ArtifactBridge) *pathapp.Service {
	t.Helper()
	service, err := pathapp.NewService(pathapp.Dependencies{
		Store: store, ArtifactBridge: bridge, Clock: foundation.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

type reviewPathCitationVerifier struct{ contentHash string }

func (verifier reviewPathCitationVerifier) VerifyCitations(
	_ context.Context,
	_ foundation.ID,
	input []artifactapp.CitationInput,
) ([]artifactdomain.Citation, error) {
	result := make([]artifactdomain.Citation, len(input))
	for index, citation := range input {
		result[index] = artifactdomain.Citation{
			SourceVersionID:     citation.SourceVersionID,
			SourceSpanID:        citation.SourceSpanID,
			VerifiedContentHash: verifier.contentHash,
			Excerpt:             "A Review Learning Path closes its durable facts atomically.",
			Verified:            true,
		}
	}
	return result, nil
}

type reviewPathClosure struct {
	reservation      reviewPathReservationRow
	paths            []reviewPathPathRow
	steps            []reviewPathStepRow
	artifacts        []reviewPathArtifactRow
	pathCommands     []reviewPathCommandRow
	artifactCommands []reviewPathCommandRow
	holds            []reviewPathHoldRow
}

type reviewPathReservationRow struct {
	key, requestHash, sourceDigest, artifactDigest, pathID, artifactID, revisionID, status string
	attemptNo, artifactVersion                                                             int64
}

type reviewPathPathRow struct {
	id, answerID, artifactID, revisionID, status string
	artifactVersion, version                     int64
}

type reviewPathStepRow struct {
	id, pathID, claimID, sourceVersionID, sourceSpanID, evidenceHash, status string
	stepNo                                                                   int
	version                                                                  int64
}

type reviewPathArtifactRow struct {
	id, revisionID, artifactType, status, revisionStatus, revisionHash string
	version, revisionNo, revisionCount                                 int64
}

type reviewPathCommandRow struct {
	key, commandType, aggregateID, response string
	expectedVersion, aggregateVersion       int64
}

type reviewPathHoldRow struct {
	artifactID, ownerID, digest, disposition string
}

func loadReviewPathClosure(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID, answerID foundation.ID,
) reviewPathClosure {
	t.Helper()
	var state reviewPathClosure
	if err := pool.QueryRow(ctx, `SELECT idempotency_key,request_hash,source_snapshot_digest,
		attempt_no,COALESCE(artifact_digest,''),COALESCE(path_id::text,''),
		COALESCE(artifact_id::text,''),COALESCE(artifact_revision_id::text,''),
		COALESCE(artifact_version,0),status
		FROM learning.learning_path_creation_reservation
		WHERE workspace_id=$1 AND review_answer_id=$2`, string(workspaceID), string(answerID)).Scan(
		&state.reservation.key, &state.reservation.requestHash, &state.reservation.sourceDigest,
		&state.reservation.attemptNo, &state.reservation.artifactDigest, &state.reservation.pathID,
		&state.reservation.artifactID, &state.reservation.revisionID,
		&state.reservation.artifactVersion, &state.reservation.status,
	); err != nil {
		t.Fatal(err)
	}

	pathRows, err := pool.Query(ctx, `SELECT id::text,review_answer_id::text,artifact_id::text,
		artifact_revision_id::text,artifact_version,status,version
		FROM learning.learning_path
		WHERE workspace_id=$1 AND review_answer_id=$2 AND origin_type='REVIEW'
		ORDER BY id`, string(workspaceID), string(answerID))
	if err != nil {
		t.Fatal(err)
	}
	for pathRows.Next() {
		var row reviewPathPathRow
		if err := pathRows.Scan(&row.id, &row.answerID, &row.artifactID, &row.revisionID,
			&row.artifactVersion, &row.status, &row.version); err != nil {
			pathRows.Close()
			t.Fatal(err)
		}
		state.paths = append(state.paths, row)
	}
	if err := pathRows.Err(); err != nil {
		pathRows.Close()
		t.Fatal(err)
	}
	pathRows.Close()

	stepRows, err := pool.Query(ctx, `SELECT step.id::text,step.path_id::text,step.claim_id::text,
		step.source_version_id::text,step.source_span_id::text,step.evidence_hash,
		step.step_no,step.status,step.version
		FROM learning.learning_path_step AS step
		JOIN learning.learning_path AS path
		  ON path.workspace_id=step.workspace_id AND path.id=step.path_id
		WHERE path.workspace_id=$1 AND path.review_answer_id=$2 AND path.origin_type='REVIEW'
		ORDER BY step.step_no,step.id`, string(workspaceID), string(answerID))
	if err != nil {
		t.Fatal(err)
	}
	for stepRows.Next() {
		var row reviewPathStepRow
		if err := stepRows.Scan(&row.id, &row.pathID, &row.claimID, &row.sourceVersionID,
			&row.sourceSpanID, &row.evidenceHash, &row.stepNo, &row.status, &row.version); err != nil {
			stepRows.Close()
			t.Fatal(err)
		}
		state.steps = append(state.steps, row)
	}
	if err := stepRows.Err(); err != nil {
		stepRows.Close()
		t.Fatal(err)
	}
	stepRows.Close()

	artifactRows, err := pool.Query(ctx, `SELECT artifact.id::text,
		artifact.current_revision_id::text,artifact.artifact_type,
		artifact.status,artifact.version,revision.status,revision.revision_no,
		revision.content_hash,
		(SELECT count(*) FROM learning.artifact_revision AS item
		 WHERE item.workspace_id=artifact.workspace_id AND item.artifact_id=artifact.id)
	FROM learning.artifact AS artifact
	JOIN learning.artifact_revision AS revision
	  ON revision.workspace_id=artifact.workspace_id
	 AND revision.artifact_id=artifact.id AND revision.id=artifact.current_revision_id
	WHERE artifact.workspace_id=$1 AND artifact.artifact_type=$2
	ORDER BY artifact.id`, string(workspaceID), pathapp.ArtifactKind)
	if err != nil {
		t.Fatal(err)
	}
	for artifactRows.Next() {
		var row reviewPathArtifactRow
		if err := artifactRows.Scan(&row.id, &row.revisionID, &row.artifactType, &row.status,
			&row.version, &row.revisionStatus, &row.revisionNo, &row.revisionHash,
			&row.revisionCount); err != nil {
			artifactRows.Close()
			t.Fatal(err)
		}
		state.artifacts = append(state.artifacts, row)
	}
	if err := artifactRows.Err(); err != nil {
		artifactRows.Close()
		t.Fatal(err)
	}
	artifactRows.Close()

	pathCommandRows, err := pool.Query(ctx, `SELECT command.idempotency_key,command.command_type,
		command.path_id::text,command.expected_version,command.path_version,command.response::text
		FROM learning.learning_path_command AS command
		WHERE command.workspace_id=$1
		  AND command.path_id IN (
			SELECT path.id FROM learning.learning_path AS path
			WHERE path.workspace_id=$1 AND path.review_answer_id=$2 AND path.origin_type='REVIEW'
		  )
		ORDER BY command.created_at,command.idempotency_key`, string(workspaceID), string(answerID))
	if err != nil {
		t.Fatal(err)
	}
	state.pathCommands = scanReviewPathCommandRows(t, pathCommandRows)

	artifactCommandRows, err := pool.Query(ctx, `SELECT command.idempotency_key,
		command.command_type,command.artifact_id::text,
		0::bigint,command.artifact_version,command.response::text
	FROM learning.artifact_command AS command
	JOIN learning.artifact AS artifact
	  ON artifact.workspace_id=command.workspace_id AND artifact.id=command.artifact_id
	WHERE command.workspace_id=$1 AND artifact.artifact_type=$2
	ORDER BY command.artifact_id,command.artifact_version,command.idempotency_key`,
		string(workspaceID), pathapp.ArtifactKind)
	if err != nil {
		t.Fatal(err)
	}
	state.artifactCommands = scanReviewPathCommandRows(t, artifactCommandRows)

	holdRows, err := pool.Query(ctx, `SELECT artifact_id::text,owner_id::text,
		COALESCE(attempt_digest,''),disposition
		FROM learning.artifact_visibility_hold
		WHERE workspace_id=$1 AND owner_type='LEARNING_PATH_CREATE' AND review_answer_id=$2
		ORDER BY artifact_id`, string(workspaceID), string(answerID))
	if err != nil {
		t.Fatal(err)
	}
	for holdRows.Next() {
		var row reviewPathHoldRow
		if err := holdRows.Scan(&row.artifactID, &row.ownerID, &row.digest, &row.disposition); err != nil {
			holdRows.Close()
			t.Fatal(err)
		}
		state.holds = append(state.holds, row)
	}
	if err := holdRows.Err(); err != nil {
		holdRows.Close()
		t.Fatal(err)
	}
	holdRows.Close()
	return state
}

func scanReviewPathCommandRows(t *testing.T, rows pgx.Rows) []reviewPathCommandRow {
	t.Helper()
	defer rows.Close()
	var result []reviewPathCommandRow
	for rows.Next() {
		var row reviewPathCommandRow
		if err := rows.Scan(&row.key, &row.commandType, &row.aggregateID,
			&row.expectedVersion, &row.aggregateVersion, &row.response); err != nil {
			t.Fatal(err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func assertReviewPathTerminalClosure(t *testing.T, state reviewPathClosure, answerID foundation.ID) {
	t.Helper()
	if state.reservation.attemptNo < 1 || state.reservation.key == "" ||
		len(state.reservation.requestHash) != 64 || len(state.reservation.sourceDigest) != 64 ||
		len(state.reservation.artifactDigest) != 64 {
		t.Fatalf("invalid reservation closure: %+v", state.reservation)
	}
	for _, hold := range state.holds {
		if hold.ownerID != string(answerID) || hold.disposition != "ORPHANED" ||
			len(hold.digest) != 64 || hold.artifactID == "" {
			t.Fatalf("invalid terminal hold: %+v", hold)
		}
	}
	for _, artifact := range state.artifacts {
		if artifact.artifactType != string(pathapp.ArtifactKind) || artifact.status != "DRAFT" ||
			artifact.version != 4 || artifact.revisionNo != 4 || artifact.revisionCount != 4 ||
			artifact.revisionStatus != "SNAPSHOT" || len(artifact.revisionHash) != 64 {
			t.Fatalf("invalid Artifact closure: %+v", artifact)
		}
	}
	if len(state.artifactCommands) != len(state.artifacts)*4 {
		t.Fatalf("artifact command count=%d artifacts=%d", len(state.artifactCommands), len(state.artifacts))
	}
	wantArtifactCommands := map[string]int64{
		"PLAN": 1, "SUBMIT_OUTLINE": 2, "APPROVE_OUTLINE": 3, "RECORD_SECTION": 4,
	}
	seenArtifactCommands := make(map[string]map[string]bool, len(state.artifacts))
	for _, command := range state.artifactCommands {
		wantVersion, found := wantArtifactCommands[command.commandType]
		if !found || command.aggregateVersion != wantVersion || command.key == "" ||
			command.aggregateID == "" || !json.Valid([]byte(command.response)) {
			t.Fatalf("invalid Artifact command closure: %+v", command)
		}
		if seenArtifactCommands[command.aggregateID] == nil {
			seenArtifactCommands[command.aggregateID] = make(map[string]bool, 4)
		}
		if seenArtifactCommands[command.aggregateID][command.commandType] {
			t.Fatalf("duplicate Artifact command closure: %+v", command)
		}
		seenArtifactCommands[command.aggregateID][command.commandType] = true
	}

	switch state.reservation.status {
	case string(pathapp.ReservationCompleted):
		assertReviewPathCompletedClosure(t, state, answerID)
	case string(pathapp.ReservationAbandoned):
		assertReviewPathAbandonedClosure(t, state)
	default:
		t.Fatalf("reservation is not terminal: %+v", state.reservation)
	}
}

func assertReviewPathCompletedClosure(t *testing.T, state reviewPathClosure, answerID foundation.ID) {
	t.Helper()
	if len(state.paths) != 1 || len(state.steps) == 0 || len(state.pathCommands) == 0 {
		t.Fatalf("completed closure paths=%d steps=%d commands=%d", len(state.paths), len(state.steps), len(state.pathCommands))
	}
	path := state.paths[0]
	if path.id != state.reservation.pathID || path.answerID != string(answerID) ||
		path.artifactID != state.reservation.artifactID || path.revisionID != state.reservation.revisionID ||
		path.artifactVersion != state.reservation.artifactVersion || path.status != "ACTIVE" || path.version != 1 {
		t.Fatalf("completed Path/reservation binding drifted: reservation=%+v path=%+v", state.reservation, path)
	}
	currentArtifactFound := false
	for _, artifact := range state.artifacts {
		if artifact.id == state.reservation.artifactID {
			currentArtifactFound = artifact.revisionID == state.reservation.revisionID &&
				artifact.version == state.reservation.artifactVersion
		}
	}
	if !currentArtifactFound || len(state.artifacts) != len(state.holds)+1 {
		t.Fatalf("completed Artifact binding is not closed: artifacts=%+v holds=%+v", state.artifacts, state.holds)
	}
	for _, hold := range state.holds {
		if hold.artifactID == state.reservation.artifactID || hold.digest == state.reservation.artifactDigest {
			t.Fatalf("completed attempt retained its current hold: %+v", hold)
		}
	}
	for index, step := range state.steps {
		if step.pathID != path.id || step.stepNo != index+1 || step.claimID == "" ||
			step.sourceVersionID == "" || step.sourceSpanID == "" || len(step.evidenceHash) != 64 ||
			step.status != "PENDING" || step.version != 1 {
			t.Fatalf("invalid completed Path step: %+v", step)
		}
	}
	for _, command := range state.pathCommands {
		if command.commandType != pathapp.CommandTypeCreateReviewPath || command.aggregateID != path.id ||
			command.expectedVersion != 0 || command.aggregateVersion != 1 || !json.Valid([]byte(command.response)) {
			t.Fatalf("invalid Learning Path command closure: %+v", command)
		}
	}
}

func assertReviewPathAbandonedClosure(t *testing.T, state reviewPathClosure) {
	t.Helper()
	if state.reservation.pathID != "" || state.reservation.artifactID != "" ||
		state.reservation.revisionID != "" || state.reservation.artifactVersion != 0 ||
		len(state.paths) != 0 || len(state.steps) != 0 || len(state.pathCommands) != 0 {
		t.Fatalf("abandoned reservation exposed terminal facts: %+v", state)
	}
	if len(state.artifacts) != len(state.holds) {
		t.Fatalf("abandoned Artifact/hold closure drifted: artifacts=%+v holds=%+v", state.artifacts, state.holds)
	}
	for _, hold := range state.holds {
		if hold.digest != state.reservation.artifactDigest {
			t.Fatalf("abandoned hold digest=%s reservation=%s", hold.digest, state.reservation.artifactDigest)
		}
	}
}

func findReviewPathArtifact(state reviewPathClosure, artifactID foundation.ID) (reviewPathArtifactRow, bool) {
	for _, artifact := range state.artifacts {
		if artifact.id == string(artifactID) {
			return artifact, true
		}
	}
	return reviewPathArtifactRow{}, false
}

func reviewPathIntegrationID(number int) foundation.ID {
	return foundation.ID(fmt.Sprintf("8a000000-0000-4000-8000-%012x", number))
}

func reviewPathHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
