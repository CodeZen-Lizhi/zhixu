//go:build integration

package migration

import (
	"context"
	"testing"
)

func TestLearningOpsAuthMigrationRepeatUpAndConstraintGuards(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	runner := newAtlasRunnerForPool(t, pool)
	if err := runner.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runner.Up(ctx); err != nil {
		t.Fatalf("repeated migration Up failed: %v", err)
	}
	var applied int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM atlas_schema_revisions.atlas_schema_revisions WHERE version = '00034'`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("migration 00034 applied=%d want=1", applied)
	}
	for _, table := range []string{"auth.session", "auth.api_token", "learning.artifact", "learning.review_card", "learning.memory", "ops.knowledge_event", "ops.audit_event", "ops.export_job"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("migration did not create %s", table)
		}
	}
	for _, constraint := range []struct {
		table string
		name  string
	}{
		{table: "auth.session", name: "auth_session_distinct_credential_hashes"},
		{table: "auth.session", name: "auth_session_last_seen_order"},
		{table: "auth.session", name: "auth_session_revoked_order"},
		{table: "auth.api_token", name: "auth_api_token_last_used_order"},
		{table: "auth.api_token", name: "auth_api_token_revoked_order"},
	} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid=$1::regclass AND conname=$2)`, constraint.table, constraint.name).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("missing constraint %s on %s", constraint.name, constraint.table)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO auth.session(
		id,token_hash,csrf_hash,user_label,scopes,created_at,last_seen_at,expires_at
	) VALUES(
		'81000000-0000-4000-8000-000000000001',repeat('a',64),repeat('a',64),'owner','["READ_LOCAL"]'::jsonb,
		CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP + interval '1 hour'
	)`); err == nil {
		t.Fatal("session accepted identical token and csrf hashes")
	}

	provider := migrationProvider(t, pool)
	if err := provider.UpTo(ctx, 34); err != nil {
		t.Fatalf("repeat 00034 Up failed: %v", err)
	}
	if err := provider.UpTo(ctx, 34); err != nil {
		t.Fatalf("second repeat 00034 Up failed: %v", err)
	}
}
