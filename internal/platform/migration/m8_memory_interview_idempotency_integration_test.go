//go:build integration

package migration

import (
	"context"
	"testing"
	"time"
)

func TestM8InterviewMemoryCandidateIdempotencyMigrationRepeatUpAndConstraint(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)

	if err := provider.UpTo(ctx, 54); err != nil {
		t.Fatalf("empty 00054 Up failed: %v", err)
	}
	if err := provider.UpTo(ctx, 54); err != nil {
		t.Fatalf("repeated 00054 Up failed: %v", err)
	}
	assertMigrationVersion(t, ctx, pool, 54)
	if err := provider.UpTo(ctx, 54); err != nil {
		t.Fatalf("third 00054 Up failed: %v", err)
	}

	now := time.Date(2026, 7, 28, 14, 0, 0, 0, time.UTC)
	const (
		workspaceID = "94000000-0000-4000-8000-000000000001"
		ownerID     = "00000000-0000-5000-8000-000000000001"
		firstID     = "94000000-0000-4000-8000-000000000002"
		secondID    = "94000000-0000-4000-8000-000000000003"
	)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
		VALUES($1,'memory-candidate-migration','/tmp/memory-candidate-migration','/tmp/memory-candidate-migration',$2,'test',1,$2,$2)`, workspaceID, now); err != nil {
		t.Fatal(err)
	}
	insert := func(id, sourceType, sourceRef string) error {
		_, err := pool.Exec(ctx, `INSERT INTO learning.memory(
			id,workspace_id,owner_principal_kind,owner_principal_id,memory_type,content,source_type,source_ref,task_scope_id,
			status,expires_at,confirmed_at,confirmed_by_principal_kind,confirmed_by_principal_id,version,created_at,updated_at
		) VALUES($1,$2,'USER',$3,'GOAL','{"goal":"practice"}'::jsonb,$4,$5,NULL,'CANDIDATE',NULL,NULL,NULL,NULL,1,$6,$6)`,
			id, workspaceID, ownerID, sourceType, sourceRef, now)
		return err
	}
	if err := insert(firstID, "INTERVIEW", "interview:session:path:step"); err != nil {
		t.Fatalf("first interview candidate insert failed: %v", err)
	}
	assertPostgresCode(t, insert(secondID, "INTERVIEW", "interview:session:path:step"), "23505")
	if err := insert(secondID, "USER", "user:manual"); err != nil {
		t.Fatalf("user candidate should not use Interview provenance uniqueness: %v", err)
	}
}
