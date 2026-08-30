//go:build integration

package migration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestReviewHardeningMigrationBackfillsLegacyRows(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if err := provider.UpTo(ctx, 34); err != nil {
		t.Fatal(err)
	}
	claimID := "72000000-0000-4000-8000-000000000003"
	seedLegacyReviewRows(t, ctx, pool, &claimID)
	if err := provider.UpTo(ctx, 35); err != nil {
		t.Fatalf("00035 legacy upgrade failed: %v", err)
	}
	assertMigrationVersion(t, ctx, pool, 35)

	var sessionHash, answerHash string
	var rating *int
	var snapshot *string
	if err := pool.QueryRow(ctx, `SELECT request_hash FROM learning.review_session WHERE id='72000000-0000-4000-8000-000000000005'`).Scan(&sessionHash); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT request_hash,rating,schedule_snapshot::text FROM learning.review_answer WHERE id='72000000-0000-4000-8000-000000000006'`).Scan(&answerHash, &rating, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(sessionHash) != 64 || len(answerHash) != 64 || rating != nil || snapshot != nil {
		t.Fatalf("session_hash=%q answer_hash=%q rating=%v snapshot=%v", sessionHash, answerHash, rating, snapshot)
	}
}

func TestReviewHardeningMigrationPreservesLegacyNullClaim(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if err := provider.UpTo(ctx, 34); err != nil {
		t.Fatal(err)
	}
	seedLegacyReviewRows(t, ctx, pool, nil)
	if err := provider.UpTo(ctx, 35); err != nil {
		t.Fatalf("00035 rejected a valid 00034 legacy Review Card: %v", err)
	}
	assertMigrationVersion(t, ctx, pool, 35)

	var persistedClaimID *string
	if err := pool.QueryRow(ctx, `SELECT claim_id::text FROM learning.review_card WHERE id='72000000-0000-4000-8000-000000000004'`).Scan(&persistedClaimID); err != nil {
		t.Fatal(err)
	}
	if persistedClaimID != nil {
		t.Fatalf("legacy claim binding changed to %v", persistedClaimID)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning.review_card(id,workspace_id,deck_id,claim_id,question,answer_points,evidence,card_type,difficulty,status,fingerprint,model_version,version,created_at,updated_at)
		VALUES('72000000-0000-4000-8000-000000000007','72000000-0000-4000-8000-000000000001','72000000-0000-4000-8000-000000000002',NULL,'new invalid question','["point"]','[{"legacy":true}]','SHORT_ANSWER',0.5,'DRAFT',$1,'manual',1,$2,$2)`, strings.Repeat("d", 64), time.Date(2026, 7, 23, 8, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("00035 allowed a new Review Card without a Claim")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning.review_answer(id,workspace_id,session_id,card_id,question_ref,idempotency_key,user_answer,score,feedback,request_hash,created_at)
		VALUES('72000000-0000-4000-8000-000000000008','72000000-0000-4000-8000-000000000001','72000000-0000-4000-8000-000000000005','72000000-0000-4000-8000-000000000004','new:1','new-invalid-answer','answer','{}','{}',$1,$2)`, strings.Repeat("e", 64), time.Date(2026, 7, 23, 8, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("00035 allowed a new Review Answer without rating and schedule snapshot")
	}
}

func TestReviewCompleteSessionReceiptMigrationAllowsOnlyForwardCommandType(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if err := provider.UpTo(ctx, 37); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 25, 8, 0, 0, 0, time.UTC)
	workspaceID := "72000000-0000-4000-8000-000000000099"
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
		VALUES($1,'review-complete-command','/tmp/review-complete-command','/tmp/review-complete-command',$2,'test',1,$2,$2)`, workspaceID, now); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO learning.review_command(workspace_id,idempotency_key,request_hash,command_type,aggregate_id,aggregate_version,response,created_at)
		VALUES($1,'complete-session-migration',repeat('a',64),'COMPLETE_SESSION','72000000-0000-4000-8000-000000000098',1,'{}',$2)`
	legacyInsert := `INSERT INTO learning.review_command(workspace_id,idempotency_key,request_hash,command_type,aggregate_id,aggregate_version,response,created_at)
		VALUES($1,'legacy-review-command',repeat('b',64),'CREATE_DECK','72000000-0000-4000-8000-000000000097',1,'{}',$2)`
	if _, err := pool.Exec(ctx, insert, workspaceID, now); err == nil {
		t.Fatal("pre-00038 review command constraint accepted COMPLETE_SESSION")
	}
	if _, err := pool.Exec(ctx, legacyInsert, workspaceID, now); err != nil {
		t.Fatalf("pre-00038 receipt insert failed: %v", err)
	}
	if err := provider.UpTo(ctx, 38); err != nil {
		t.Fatalf("00038 upgrade failed: %v", err)
	}
	var legacyCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.review_command WHERE workspace_id=$1 AND idempotency_key='legacy-review-command'`, workspaceID).Scan(&legacyCount); err != nil || legacyCount != 1 {
		t.Fatalf("00038 lost legacy receipt: count=%d err=%v", legacyCount, err)
	}
	if err := provider.UpTo(ctx, 38); err != nil {
		t.Fatalf("00038 re-upgrade failed: %v", err)
	}
	if _, err := pool.Exec(ctx, insert, workspaceID, now); err != nil {
		t.Fatalf("00038 rejected COMPLETE_SESSION receipt: %v", err)
	}
}

func seedLegacyReviewRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, claimID *string) {
	t.Helper()
	now := time.Date(2026, 7, 23, 8, 0, 0, 0, time.UTC)
	workspaceID := "72000000-0000-4000-8000-000000000001"
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
		VALUES($1,'review-migration','/tmp/review-migration','/tmp/review-migration',$2,'test',1,$2,$2)`, workspaceID, now); err != nil {
		t.Fatal(err)
	}
	if claimID != nil {
		if _, err := pool.Exec(ctx, `INSERT INTO core.claim(id,workspace_id,statement,normalized_statement,applicability,applicability_schema_version,applicability_hash,status,confidence_score,confidence_factors,fingerprint,version,created_at,updated_at)
			VALUES($1,$2,'legacy claim','legacy claim','{}','knowledge-applicability/v1',$3,'SUGGESTED',0.9,'{}',$4,1,$5,$5)`, *claimID, workspaceID, strings.Repeat("a", 64), strings.Repeat("b", 64), now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning.review_deck(id,workspace_id,name,scope,status,daily_limit,scheduler_version,version,created_at,updated_at)
		VALUES('72000000-0000-4000-8000-000000000002',$1,'legacy deck','{}','ACTIVE',20,'fsrs/v1',1,$2,$2)`, workspaceID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning.review_card(id,workspace_id,deck_id,claim_id,question,answer_points,evidence,card_type,difficulty,status,fingerprint,model_version,version,created_at,updated_at)
		VALUES('72000000-0000-4000-8000-000000000004',$1,'72000000-0000-4000-8000-000000000002',$2,'legacy question','["point"]','[{"legacy":true}]','SHORT_ANSWER',0.5,'DRAFT',$3,'manual',1,$4,$4)`, workspaceID, claimID, strings.Repeat("c", 64), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning.review_session(id,workspace_id,deck_id,session_type,status,config,idempotency_key,started_at)
		VALUES('72000000-0000-4000-8000-000000000005',$1,'72000000-0000-4000-8000-000000000002','REVIEW','ACTIVE','{}','legacy-session',$2)`, workspaceID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning.review_answer(id,workspace_id,session_id,card_id,question_ref,idempotency_key,user_answer,score,feedback,created_at)
		VALUES('72000000-0000-4000-8000-000000000006',$1,'72000000-0000-4000-8000-000000000005','72000000-0000-4000-8000-000000000004','legacy:1','legacy-answer','legacy','{}','{}',$2)`, workspaceID, now); err != nil {
		t.Fatal(err)
	}
}
