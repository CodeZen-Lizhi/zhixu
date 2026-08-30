//go:build integration

package migration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestM8InterviewCompletionReservationMigrationSupportsRepeatedUp(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)

	if err := provider.UpTo(ctx, 57); err != nil {
		t.Fatalf("00057 Up: %v", err)
	}
	if err := provider.UpTo(ctx, 57); err != nil {
		t.Fatalf("00057 repeated Up: %v", err)
	}
	assertMigrationVersion(t, ctx, pool, 57)
	assertM8InterviewCompletionReservationShape(t, ctx, pool)

	if err := provider.UpTo(ctx, 57); err != nil {
		t.Fatalf("00057 re-Up: %v", err)
	}
	assertM8InterviewCompletionReservationShape(t, ctx, pool)
}

func TestM8InterviewCompletionReservationMigrationBackfillsGuardsAndSerializesLateHolds(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabaseWithMaxConns(t, ctx, 4)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if err := provider.UpTo(ctx, 56); err != nil {
		t.Fatalf("migrate through 00056: %v", err)
	}

	now := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	workspaceID := "57000000-0000-4000-8000-000000000001"
	sessionID := "57000000-0000-4000-8000-000000000002"
	reportArtifactID := "57000000-0000-4000-8000-000000000003"
	reportRevisionID := "57000000-0000-4000-8000-000000000004"
	pathArtifactID := "57000000-0000-4000-8000-000000000005"
	pathRevisionID := "57000000-0000-4000-8000-000000000006"
	wrongArtifactID := "57000000-0000-4000-8000-000000000007"
	wrongRevisionID := "57000000-0000-4000-8000-000000000008"
	attemptDigest := strings.Repeat("a", 64)
	wrongDigest := strings.Repeat("b", 64)

	seedM8CompletionWorkspaceAndSession(t, ctx, pool, workspaceID, sessionID, now)
	seedM8CompletionArtifact(t, ctx, pool, workspaceID, sessionID, reportArtifactID, reportRevisionID, "INTERVIEW_DOC", attemptDigest, now)
	seedM8CompletionArtifact(t, ctx, pool, workspaceID, sessionID, pathArtifactID, pathRevisionID, "LEARNING_PATH", attemptDigest, now)
	seedM8CompletionArtifact(t, ctx, pool, workspaceID, sessionID, wrongArtifactID, wrongRevisionID, "LEARNING_PATH", wrongDigest, now)
	if _, err := pool.Exec(ctx, `INSERT INTO learning.artifact_visibility_hold(
		workspace_id,artifact_id,owner_type,owner_id,owner_role,created_at
	) VALUES($1,$2,'INTERVIEW_COMPLETE',$3,'REPORT',$4)`, workspaceID, reportArtifactID, sessionID, now); err != nil {
		t.Fatalf("seed legacy visibility hold: %v", err)
	}

	if err := provider.UpTo(ctx, 57); err != nil {
		t.Fatalf("00057 legacy Up: %v", err)
	}
	var legacyDigest, legacyDisposition string
	if err := pool.QueryRow(ctx, `SELECT attempt_digest,disposition
		FROM learning.artifact_visibility_hold
		WHERE workspace_id=$1 AND artifact_id=$2`, workspaceID, reportArtifactID).Scan(&legacyDigest, &legacyDisposition); err != nil {
		t.Fatal(err)
	}
	if legacyDigest != attemptDigest || legacyDisposition != "ORPHANED" {
		t.Fatalf("legacy hold digest=%q disposition=%q", legacyDigest, legacyDisposition)
	}

	seedM8CompletionReservation(t, ctx, pool, workspaceID, sessionID, "completion-current", attemptDigest, now)
	if _, err := pool.Exec(ctx, `UPDATE learning.artifact_visibility_hold
		SET attempt_digest=NULL,disposition='ACTIVE'
		WHERE workspace_id=$1 AND artifact_id=$2`, workspaceID, reportArtifactID); err != nil {
		t.Fatalf("restore legacy hold: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning.artifact_visibility_hold(
		workspace_id,artifact_id,owner_type,owner_id,owner_role,attempt_digest,created_at
	) VALUES($1,$2,'INTERVIEW_COMPLETE',$3,'PATH',NULL,$4)`, workspaceID, pathArtifactID, sessionID, now); err != nil {
		t.Fatalf("insert normalized active hold: %v", err)
	}
	var normalizedDigest string
	if err := pool.QueryRow(ctx, `SELECT attempt_digest
		FROM learning.artifact_visibility_hold
		WHERE workspace_id=$1 AND artifact_id=$2`, workspaceID, pathArtifactID).Scan(&normalizedDigest); err != nil {
		t.Fatal(err)
	}
	if normalizedDigest != attemptDigest {
		t.Fatalf("normalized digest=%q want=%q", normalizedDigest, attemptDigest)
	}

	_, err := pool.Exec(ctx, `UPDATE learning.artifact_visibility_hold
		SET owner_role='REPORT'
		WHERE workspace_id=$1 AND artifact_id=$2`, workspaceID, pathArtifactID)
	assertPostgresCode(t, err, "23514")
	_, err = pool.Exec(ctx, `UPDATE learning.artifact_visibility_hold
		SET artifact_id=$3
		WHERE workspace_id=$1 AND artifact_id=$2`, workspaceID, pathArtifactID, wrongArtifactID)
	assertPostgresCode(t, err, "23514")

	lateSessionID := "57000000-0000-4000-8000-000000000012"
	lateArtifactID := "57000000-0000-4000-8000-000000000013"
	lateRevisionID := "57000000-0000-4000-8000-000000000014"
	seedM8CompletionSession(t, ctx, pool, workspaceID, lateSessionID, now.Add(time.Minute))
	seedM8CompletionArtifact(t, ctx, pool, workspaceID, lateSessionID, lateArtifactID, lateRevisionID, "INTERVIEW_DOC", attemptDigest, now.Add(time.Minute))
	seedM8CompletionReservation(t, ctx, pool, workspaceID, lateSessionID, "completion-late", attemptDigest, now.Add(time.Minute))

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE learning.interview_completion_reservation
		SET status='ABANDONED',abandoned_at=statement_timestamp(),updated_at=statement_timestamp()
		WHERE workspace_id=$1 AND session_id=$2`, workspaceID, lateSessionID); err != nil {
		t.Fatal(err)
	}
	insertResult := make(chan error, 1)
	go func() {
		_, insertErr := pool.Exec(ctx, `INSERT INTO learning.artifact_visibility_hold(
			workspace_id,artifact_id,owner_type,owner_id,owner_role,attempt_digest,created_at
		) VALUES($1,$2,'INTERVIEW_COMPLETE',$3,'REPORT',NULL,$4)`, workspaceID, lateArtifactID, lateSessionID, now.Add(time.Minute))
		insertResult <- insertErr
	}()
	select {
	case earlyErr := <-insertResult:
		t.Fatalf("late hold did not wait for reservation lock: %v", earlyErr)
	case <-time.After(150 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case insertErr := <-insertResult:
		assertPostgresCode(t, insertErr, "23514")
	case <-time.After(3 * time.Second):
		t.Fatal("late hold remained blocked after maintenance commit")
	}
}

func assertM8InterviewCompletionReservationShape(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var tables, triggers, constraints int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema='learning' AND table_name='interview_completion_reservation'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger
		WHERE NOT tgisinternal AND tgname='trg_learning_interview_artifact_visibility_hold_guard'`).Scan(&triggers); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint
		WHERE conname = ANY($1::text[])`, []string{
		"learning_artifact_visibility_hold_active_digest_check",
		"learning_interview_completion_binding_shape_check",
		"learning_interview_completion_status_shape_check",
	}).Scan(&constraints); err != nil {
		t.Fatal(err)
	}
	if tables != 1 || triggers != 1 || constraints != 3 {
		t.Fatalf("00057 shape tables=%d triggers=%d constraints=%d", tables, triggers, constraints)
	}
}

func seedM8CompletionWorkspaceAndSession(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, sessionID string, now time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
	) VALUES($1,'m8-completion','/tmp/m8-completion','/tmp/m8-completion',$2,'test',1,$2,$2)`, workspaceID, now); err != nil {
		t.Fatal(err)
	}
	seedM8CompletionSession(t, ctx, pool, workspaceID, sessionID, now)
}

func seedM8CompletionSession(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, sessionID string, now time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO learning.review_session(
		id,workspace_id,deck_id,session_type,status,config,idempotency_key,request_hash,started_at,ended_at
	) VALUES($1,$2,NULL,'INTERVIEW','ACTIVE','{"schema_version":"interview/v1"}'::jsonb,$3,repeat('1',64),$4,NULL)`,
		sessionID, workspaceID, "completion-session-"+sessionID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning.interview_session(
		session_id,workspace_id,domain_schema_version,version,follow_up_count,created_at,updated_at
	) VALUES($1,$2,'interview/v1',1,0,$3,$3)`, sessionID, workspaceID, now); err != nil {
		t.Fatal(err)
	}
}

func seedM8CompletionArtifact(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID, sessionID, artifactID, revisionID, artifactType, attemptDigest string,
	now time.Time,
) {
	t.Helper()
	planKey := "iv1:" + sessionID + ":" + artifactType + ":" + attemptDigest + ":p"
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO learning.artifact(
			id,workspace_id,artifact_type,title,scope,status,version,created_at,updated_at,
			domain_schema_version,scope_definition,source_coverage,current_revision_id
		) VALUES($1,$2,$3,'Completion Artifact','{}','DRAFT',1,$4,$4,'artifact/v1','{}','[]',$5)`,
			artifactID, workspaceID, artifactType, now, revisionID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO learning.artifact_revision(
			id,artifact_id,workspace_id,revision_no,status,outline,sections,coverage,missing,conflicts,
			content_markdown,provenance,created_at,domain_schema_version,content_hash,created_by_type
		) VALUES($1,$2,$3,1,'SNAPSHOT','[]','[]','[]','[]','[]','','{}',$4,'artifact-revision/v1',repeat('2',64),'AGENT')`,
			revisionID, artifactID, workspaceID, now); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO learning.artifact_command(
			workspace_id,idempotency_key,request_hash,command_type,artifact_id,artifact_version,response,created_at
		) VALUES($1,$2,repeat('3',64),'PLAN',$3,1,'{}'::jsonb,$4)`, workspaceID, planKey, artifactID, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func seedM8CompletionReservation(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID, sessionID, idempotencyKey, attemptDigest string,
	now time.Time,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO learning.interview_completion_reservation(
		workspace_id,session_id,idempotency_key,request_hash,manual_end,snapshot_version,
		artifact_digest,status,created_at,prepared_at,updated_at
	) VALUES($1,$2,$3,repeat('4',64),false,1,$4,'PENDING',$5,$5,$5)`,
		workspaceID, sessionID, idempotencyKey, attemptDigest, now); err != nil {
		t.Fatal(err)
	}
}
