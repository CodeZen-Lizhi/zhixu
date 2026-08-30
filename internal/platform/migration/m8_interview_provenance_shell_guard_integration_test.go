//go:build integration

package migration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestM8InterviewProvenanceShellGuardMigrationSupportsRepeatedUp(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)

	if err := provider.UpTo(ctx, 56); err != nil {
		t.Fatalf("00056 Up: %v", err)
	}
	if err := provider.UpTo(ctx, 56); err != nil {
		t.Fatalf("00056 repeated Up: %v", err)
	}
	assertMigrationVersion(t, ctx, pool, 56)
	assertM8InterviewProvenanceShellGuardShape(t, ctx, pool)

	if err := provider.UpTo(ctx, 56); err != nil {
		t.Fatalf("00056 re-Up: %v", err)
	}
	assertM8InterviewProvenanceShellGuardShape(t, ctx, pool)
}

func TestM8InterviewQuestionEvidenceRejectsMalformedJSONWithoutEvaluationErrors(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)

	if err := provider.UpTo(ctx, 56); err != nil {
		t.Fatalf("00056 Up: %v", err)
	}
	workspaceID, sessionID, claimID := seedM8InterviewQuestionShell(t, ctx, pool)
	for index, evidence := range []string{`"scalar"`, `[42]`, `[{}]`} {
		_, err := pool.Exec(ctx, `INSERT INTO learning.interview_question(
			id,workspace_id,session_id,question_no,follow_up_no,parent_question_id,claim_id,topic_id,
			prompt,answer_points,evidence,status,fingerprint,created_at,answered_at
		) VALUES($1,$2,$3,$4,0,NULL,$5,NULL,'Malformed evidence','["point"]'::jsonb,$6::jsonb,
			'PENDING',repeat('f',64),$7,NULL)`,
			m8InterviewProvenanceID(10+index), workspaceID, sessionID, index+1, claimID, evidence,
			time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC))
		assertPostgresCode(t, err, "23514")
	}
}

func seedM8InterviewQuestionShell(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (string, string, string) {
	t.Helper()
	now := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	workspaceID := m8InterviewProvenanceID(1)
	sessionID := m8InterviewProvenanceID(2)
	claimID := m8InterviewProvenanceID(3)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
			VALUES($1,'m8 provenance','/tmp/m8-provenance','/tmp/m8-provenance',$2,'test',1,$2,$2)`, []any{workspaceID, now}},
		{`INSERT INTO core.claim(
			id,workspace_id,statement,normalized_statement,applicability,applicability_schema_version,
			applicability_hash,status,confidence_factors,fingerprint,version,created_at,updated_at
		) VALUES($1,$2,'Malformed evidence must fail closed.','malformed evidence must fail closed.','{}',
			'knowledge-applicability/v1',repeat('a',64),'SUGGESTED','{}',repeat('b',64),1,$3,$3)`, []any{claimID, workspaceID, now}},
		{`INSERT INTO learning.review_session(
			id,workspace_id,deck_id,session_type,status,config,idempotency_key,request_hash,started_at
		) VALUES($1,$2,NULL,'INTERVIEW','ACTIVE','{"schema_version":"interview/v1"}'::jsonb,
			'm8-provenance-session',repeat('c',64),$3)`, []any{sessionID, workspaceID, now}},
		{`INSERT INTO learning.interview_session(
			session_id,workspace_id,domain_schema_version,version,follow_up_count,created_at,updated_at
		) VALUES($1,$2,'interview/v1',1,0,$3,$3)`, []any{sessionID, workspaceID, now}},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	return workspaceID, sessionID, claimID
}

func m8InterviewProvenanceID(value int) string {
	return fmt.Sprintf("00000000-0000-0000-0000-%012d", value)
}

func assertM8InterviewProvenanceShellGuardShape(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*)
		FROM pg_trigger
		WHERE NOT tgisinternal
		  AND tgname = ANY($1::text[])`, []string{
		"trg_learning_interview_question_evidence",
		"trg_learning_interview_session_binding",
		"trg_learning_review_session_learning_binding",
		"trg_learning_review_answer_session_binding",
	}).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("00056 trigger count=%d want=4", count)
	}
}
