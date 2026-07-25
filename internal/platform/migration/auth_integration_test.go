//go:build integration

package migration

import (
	"context"
	"testing"
	"time"

	projectmigrations "github.com/CodeZen-Lizhi/zhixu/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLearningOpsAuthMigrationUpRepeatDownPreservesSchemas(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	runner, err := NewRunner(pool, projectmigrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runner.Up(ctx); err != nil {
		t.Fatalf("repeated migration Up failed: %v", err)
	}
	var applied int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.goose_db_version WHERE version_id = 34 AND is_applied`).Scan(&applied); err != nil {
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
	if _, err := provider.DownTo(ctx, 33); err != nil {
		t.Fatalf("00034 Down failed: %v", err)
	}
	for _, schema := range []string{"learning", "ops", "auth", "core"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname=$1)`, schema).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("Down removed pre-existing schema %s", schema)
		}
	}
	for _, table := range []string{"auth.session", "auth.api_token", "learning.artifact", "ops.audit_event"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Fatalf("Down left %s behind", table)
		}
	}
	if _, err := provider.UpTo(ctx, 34); err != nil {
		t.Fatalf("00034 Up after Down failed: %v", err)
	}
	if _, err := provider.UpTo(ctx, 34); err != nil {
		t.Fatalf("repeated 00034 Up failed: %v", err)
	}
}

func TestLearningOpsAuthMigrationGuardsNonEmptyDown(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 34); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO auth.session(
		id,token_hash,csrf_hash,user_label,scopes,created_at,last_seen_at,expires_at
	) VALUES(
		'81000000-0000-4000-8000-000000000002',repeat('a',64),repeat('b',64),'owner','["READ_LOCAL"]'::jsonb,
		$1::timestamptz,$1::timestamptz,$1::timestamptz + interval '1 hour'
	)`, now); err != nil {
		t.Fatal(err)
	}
	_, err := provider.DownTo(ctx, 33)
	assertPostgresCode(t, err, "55000")
	assertAuthMigrationVersion(t, ctx, pool, 34)
	var sessions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM auth.session`).Scan(&sessions); err != nil || sessions != 1 {
		t.Fatalf("guarded Down sessions=%d err=%v", sessions, err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM auth.session`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 33); err != nil {
		t.Fatalf("empty 00034 Down failed: %v", err)
	}
	assertAuthMigrationVersion(t, ctx, pool, 33)
	if _, err := provider.UpTo(ctx, 34); err != nil {
		t.Fatalf("00034 Up after guarded Down failed: %v", err)
	}
}

func assertAuthMigrationVersion(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want int64) {
	t.Helper()
	version, err := migrationProvider(t, pool).GetDBVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != want {
		t.Fatalf("migration version=%d want=%d", version, want)
	}
}
