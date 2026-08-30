//go:build integration

package migration

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestM8InterviewArtifactVisibilityMigrationUpAndConstraints(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)

	if err := provider.UpTo(ctx, 53); err != nil {
		t.Fatalf("empty 00053 Up failed: %v", err)
	}
	if err := provider.UpTo(ctx, 53); err != nil {
		t.Fatalf("repeated 00053 Up failed: %v", err)
	}
	assertArtifactMigrationVersion(t, ctx, pool, 53)

	const (
		workspaceID = "93000000-0000-4000-8000-000000000701"
		sessionID   = "93000000-0000-4000-8000-000000000702"
		artifactID  = "93000000-0000-4000-8000-000000000703"
		revisionID  = "93000000-0000-4000-8000-000000000704"
		otherID     = "93000000-0000-4000-8000-000000000705"
		otherRevID  = "93000000-0000-4000-8000-000000000706"
	)
	seedArtifactVisibilityMigrationFixture(t, ctx, pool, workspaceID, sessionID, artifactID, revisionID, otherID, otherRevID)

	insertHold := func(targetArtifactID, ownerType, ownerID, ownerRole string) error {
		_, err := pool.Exec(ctx, `
			INSERT INTO learning.artifact_visibility_hold(
				workspace_id,artifact_id,owner_type,owner_id,owner_role,created_at
			) VALUES($1,$2,$3,$4,$5,CURRENT_TIMESTAMP)`,
			workspaceID, targetArtifactID, ownerType, ownerID, ownerRole)
		return err
	}
	if err := insertHold(artifactID, "INTERVIEW_COMPLETE", sessionID, "REPORT"); err != nil {
		t.Fatalf("insert valid visibility hold: %v", err)
	}
	for _, test := range []struct {
		name       string
		artifactID string
		ownerType  string
		ownerID    string
		ownerRole  string
		code       string
	}{
		{name: "invalid owner type", artifactID: otherID, ownerType: "OTHER", ownerID: sessionID, ownerRole: "PATH", code: "23514"},
		{name: "invalid owner role", artifactID: otherID, ownerType: "INTERVIEW_COMPLETE", ownerID: sessionID, ownerRole: "OTHER", code: "23514"},
		{name: "missing interview owner", artifactID: otherID, ownerType: "INTERVIEW_COMPLETE", ownerID: "93000000-0000-4000-8000-000000000799", ownerRole: "PATH", code: "23503"},
		{name: "duplicate owner role", artifactID: otherID, ownerType: "INTERVIEW_COMPLETE", ownerID: sessionID, ownerRole: "REPORT", code: "23505"},
		{name: "duplicate artifact", artifactID: artifactID, ownerType: "INTERVIEW_COMPLETE", ownerID: sessionID, ownerRole: "PATH", code: "23505"},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertPostgresCode(t, insertHold(test.artifactID, test.ownerType, test.ownerID, test.ownerRole), test.code)
		})
	}
}

func seedArtifactVisibilityMigrationFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID, sessionID, artifactID, revisionID, otherArtifactID, otherRevisionID string,
) {
	t.Helper()
	now := time.Date(2026, 7, 28, 11, 0, 0, 0, time.UTC)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
		VALUES($1,'artifact-visibility-migration','/tmp/artifact-visibility-migration',
			'/tmp/artifact-visibility-migration',$2,'test',1,$2,$2)`, workspaceID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO learning.review_session(
			id,workspace_id,deck_id,session_type,status,config,idempotency_key,request_hash,started_at,ended_at
		) VALUES($1,$2,NULL,'INTERVIEW','ACTIVE','{"schema_version":"interview/v1"}'::jsonb,
			'visibility-migration-session',repeat('a',64),$3,NULL)`,
		sessionID, workspaceID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO learning.interview_session(
			session_id,workspace_id,domain_schema_version,version,follow_up_count,created_at,updated_at
		) VALUES($1,$2,'interview/v1',1,0,$3,$3)`, sessionID, workspaceID, now); err != nil {
		t.Fatal(err)
	}
	for _, artifact := range [][2]string{{artifactID, revisionID}, {otherArtifactID, otherRevisionID}} {
		if _, err := tx.Exec(ctx, `
			INSERT INTO learning.artifact(
				id,workspace_id,artifact_type,title,scope,scope_definition,source_coverage,current_revision_id,
				status,version,domain_schema_version,created_at,updated_at
			) VALUES($1,$2,'INTERVIEW_DOC','held interview artifact','{}','interview completion','[]',$3,
				'PLANNING',1,'artifact/v1',$4,$4)`, artifact[0], workspaceID, artifact[1], now); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO learning.artifact_revision(
				id,artifact_id,workspace_id,revision_no,status,outline,sections,coverage,missing,conflicts,
				content_markdown,provenance,domain_schema_version,content_hash,created_by_type,generation_metadata,created_at
			) VALUES($1,$2,$3,1,'SNAPSHOT','[]','[]','[]','[]','[]','','{}','artifact-revision/v1',
				repeat('a',64),'HUMAN',NULL,$4)`, artifact[1], artifact[0], workspaceID, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}
