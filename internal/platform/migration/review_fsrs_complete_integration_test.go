//go:build integration

package migration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestReviewFSRSCompleteMigrationBackfillsQuarantinedEvidenceAndRemovesSchedule(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if err := provider.UpTo(ctx, 43); err != nil {
		t.Fatalf("prepare migrations through 00043: %v", err)
	}

	seedReviewFSRSBackfillFixture(t, ctx, pool)
	if err := provider.UpTo(ctx, 44); err != nil {
		t.Fatalf("00044 migration failed: %v", err)
	}
	assertMigrationVersion(t, ctx, pool, 44)

	var status string
	var reason *string
	var invalidatedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT status,invalidation_reason,invalidated_at FROM learning.review_card WHERE id='74000000-0000-4000-8000-000000000009'`).Scan(&status, &reason, &invalidatedAt); err != nil {
		t.Fatal(err)
	}
	if status != "INVALIDATED" || reason == nil || *reason != "SOURCE_QUARANTINED:74000000-0000-4000-8000-000000000004" || invalidatedAt == nil || invalidatedAt.IsZero() {
		t.Fatalf("card invalidation status=%q reason=%v invalidated_at=%v", status, reason, invalidatedAt)
	}
	var scheduleCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.review_schedule WHERE workspace_id='74000000-0000-4000-8000-000000000001' AND card_id='74000000-0000-4000-8000-000000000009'`).Scan(&scheduleCount); err != nil {
		t.Fatal(err)
	}
	if scheduleCount != 0 {
		t.Fatalf("quarantined card retained %d schedule rows", scheduleCount)
	}
}

func seedReviewFSRSBackfillFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	now := time.Date(2026, 7, 27, 8, 0, 0, 0, time.UTC)
	hash := strings.Repeat("a", 64)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES('74000000-0000-4000-8000-000000000001','review-fsrs-backfill','/tmp/review-fsrs-backfill','/tmp/review-fsrs-backfill',$1,'test',1,$1,$1)`, []any{now}},
		{`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at) VALUES('74000000-0000-4000-8000-000000000002','74000000-0000-4000-8000-000000000001',$1,4,'.knowledge/sources/' || $1,$2)`, []any{hash, now}},
		{`INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at) VALUES('74000000-0000-4000-8000-000000000003','74000000-0000-4000-8000-000000000001','text','review-fsrs.txt','review-fsrs.txt',$1)`, []any{now}},
		{`INSERT INTO core.source_version(id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at) VALUES('74000000-0000-4000-8000-000000000004','74000000-0000-4000-8000-000000000003','74000000-0000-4000-8000-000000000001','74000000-0000-4000-8000-000000000002',$1,4,'text/plain','review-fsrs.txt','passed',$2)`, []any{hash, now}},
		{`INSERT INTO ingestion.parse_projection(id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,warnings,created_at) VALUES('74000000-0000-4000-8000-000000000005','74000000-0000-4000-8000-000000000001','74000000-0000-4000-8000-000000000002','text','v1',$1,'v1',$2,'[]',$3)`, []any{strings.Repeat("b", 64), strings.Repeat("c", 64), now}},
		{`INSERT INTO ingestion.source_span(id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,selector,excerpt_hash,parser_version,schema_version,created_at) VALUES('74000000-0000-4000-8000-000000000006','74000000-0000-4000-8000-000000000001','74000000-0000-4000-8000-000000000002','74000000-0000-4000-8000-000000000005','paragraph',1,1,0,4,'{}',$1,'v1','v1',$2)`, []any{strings.Repeat("d", 64), now}},
		{`INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at) VALUES('74000000-0000-4000-8000-000000000004','74000000-0000-4000-8000-000000000005','74000000-0000-4000-8000-000000000001',$1)`, []any{now}},
		{`INSERT INTO core.claim(id,workspace_id,statement,normalized_statement,applicability,applicability_schema_version,applicability_hash,status,confidence_score,confidence_factors,fingerprint,version,created_at,updated_at) VALUES('74000000-0000-4000-8000-000000000007','74000000-0000-4000-8000-000000000001','review migration evidence','review migration evidence','{}','knowledge-applicability/v1',$1,'SUGGESTED',0.9,'{}',$2,1,$3,$3)`, []any{strings.Repeat("e", 64), strings.Repeat("f", 64), now}},
		{`INSERT INTO core.claim_source(id,workspace_id,claim_id,source_version_id,source_span_id,support_type,reason,evidence_hash,created_at) VALUES('74000000-0000-4000-8000-000000000008','74000000-0000-4000-8000-000000000001','74000000-0000-4000-8000-000000000007','74000000-0000-4000-8000-000000000004','74000000-0000-4000-8000-000000000006','SUPPORTS','review migration fixture',$1,$2)`, []any{strings.Repeat("1", 64), now}},
		{`UPDATE core.claim SET status='CONFIRMED',version=version+1,updated_at=$1 WHERE id='74000000-0000-4000-8000-000000000007'`, []any{now.Add(time.Second)}},
		{`INSERT INTO learning.review_deck(id,workspace_id,name,scope,status,daily_limit,scheduler_version,version,created_at,updated_at) VALUES('74000000-0000-4000-8000-000000000010','74000000-0000-4000-8000-000000000001','review migration deck','{}','ACTIVE',20,'fsrs/v1',1,$1,$1)`, []any{now}},
		{`INSERT INTO learning.review_card(id,workspace_id,deck_id,claim_id,question,answer_points,evidence,card_type,difficulty,status,fingerprint,model_version,version,created_at,updated_at) VALUES('74000000-0000-4000-8000-000000000009','74000000-0000-4000-8000-000000000001','74000000-0000-4000-8000-000000000010','74000000-0000-4000-8000-000000000007','why is this card invalidated?','["quarantined evidence"]',$1,'SHORT_ANSWER',0.5,'APPROVED',$2,'manual',1,$3,$3)`, []any{`[{"schema_version":"review-evidence/v1","claim_id":"74000000-0000-4000-8000-000000000007","source_version_id":"74000000-0000-4000-8000-000000000004","source_span_id":"74000000-0000-4000-8000-000000000006","evidence_hash":"1111111111111111111111111111111111111111111111111111111111111111"}]`, strings.Repeat("2", 64), now}},
		{`INSERT INTO learning.review_schedule(card_id,workspace_id,due_at,interval_days,stability,difficulty,last_reviewed_at,scheduler_version,paused,version) VALUES('74000000-0000-4000-8000-000000000009','74000000-0000-4000-8000-000000000001',$1,0,0,0.5,NULL,'fsrs/v1',false,1)`, []any{now}},
		{`INSERT INTO ingestion.attempt(id,workspace_id,source_version_id,status,security_status,failure_stage,error_code,retryable,parser_id,parser_version,parser_config_hash,chunk_strategy_version,schema_version,idempotency_key,attempt_number,warnings,started_at,completed_at,version) VALUES('74000000-0000-4000-8000-000000000011','74000000-0000-4000-8000-000000000001','74000000-0000-4000-8000-000000000004','validating','quarantined','security','SOURCE_QUARANTINED',false,'text','v1',$1,'chunk/v1','v1','review-fsrs-backfill-attempt',1,'[]',$2,$2,1)`, []any{strings.Repeat("3", 64), now.Add(2 * time.Second)}},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}
