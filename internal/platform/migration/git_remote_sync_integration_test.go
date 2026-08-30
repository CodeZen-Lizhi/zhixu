//go:build integration

package migration

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestGitRemoteSyncMigrationUpRepeatSchemaAndFacts(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)

	if err := provider.UpTo(ctx, 76); err != nil {
		t.Fatalf("migrate through 00076: %v", err)
	}
	if err := provider.UpTo(ctx, 76); err != nil {
		t.Fatalf("replay 00076: %v", err)
	}
	assertGitRemoteSyncMigrationObjects(t, ctx, pool, true)

	now := time.Date(2026, 8, 4, 5, 0, 0, 0, time.UTC)
	const workspaceID = "76000000-0000-4000-8000-000000000001"
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,root_fingerprint,binding_version,git_repository_path,git_checked_at,
		status,availability,availability_reason,availability_checked_at,version,created_at,updated_at)
		VALUES($1,'git-sync-migration','/tmp/git-sync-migration',repeat('7',64),1,'/tmp/git-sync-migration',$2,
		'inactive','available',NULL,$2,1,$2,$2)`, workspaceID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ops.git_remote_config_revision(
		workspace_id,revision,configured,normalized_url,branch,auto_sync,token_configured,
		actor,config_created_at,created_at)
		VALUES($1,1,false,NULL,NULL,false,false,'test:migration',$2,$2)`, workspaceID, now); err != nil {
		t.Fatal(err)
	}
}

func TestGitRemoteSyncMigrationEnforcesProjectionAndRunStates(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	if err := migrationProvider(t, pool).UpTo(ctx, 76); err != nil {
		t.Fatalf("migrate through 00076: %v", err)
	}

	now := time.Date(2026, 8, 4, 6, 0, 0, 0, time.UTC)
	const workspaceID = "76100000-0000-4000-8000-000000000001"
	insertGitRemoteSyncWorkspace(t, ctx, pool, workspaceID, now)

	_, err := pool.Exec(ctx, `INSERT INTO ops.git_remote_config(
		workspace_id,configured,normalized_url,branch,auto_sync,token_configured,revision,created_at,updated_at
	) VALUES($1,false,NULL,NULL,false,false,1,$2,$2)`, workspaceID, now)
	assertPostgresCode(t, err, "55000")

	if _, err := pool.Exec(ctx, `INSERT INTO ops.git_remote_config_revision(
		workspace_id,revision,configured,normalized_url,branch,auto_sync,token_configured,
		actor,config_created_at,created_at
	) VALUES($1,1,false,NULL,NULL,false,false,'test:migration',$2,$2)`, workspaceID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ops.git_remote_config(
		workspace_id,configured,normalized_url,branch,auto_sync,token_configured,revision,created_at,updated_at
	) VALUES($1,false,NULL,NULL,false,false,1,$2,$2)`, workspaceID, now); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE ops.git_remote_config_revision
		SET actor='mutated' WHERE workspace_id=$1 AND revision=1`, workspaceID)
	assertPostgresCode(t, err, "55000")

	const runID = "76100000-0000-4000-8000-000000000002"
	insertPendingGitRemoteSyncRun(t, ctx, pool, runID, workspaceID, "first-run", now)
	_, err = pool.Exec(ctx, `INSERT INTO ops.git_sync_run(
		id,workspace_id,config_revision,remote_url,branch,trigger,idempotency_key,request_hash,status,direction,
		failure_class,index_status,version,created_at,updated_at
	) VALUES(
		'76100000-0000-4000-8000-000000000003',$1,1,'https://git.example.test/acme/knowledge.git','main',
		'MANUAL','second-run',repeat('b',64),'PENDING','UNKNOWN','NONE','NOT_REQUIRED',1,$2,$2
	)`, workspaceID, now)
	assertPostgresCode(t, err, "23505")

	_, err = pool.Exec(ctx, `UPDATE ops.git_sync_run
		SET status='SUCCEEDED',direction='NONE',verified_head_oid=repeat('a',40),verified_remote_oid=repeat('a',40),
			completed_at=$2,updated_at=$2,version=2
		WHERE id=$1`, runID, now.Add(time.Minute))
	assertPostgresCode(t, err, "55000")
}

func TestWorkspaceGitCaptureMigrationUpRepeatSchemaAndConstraints(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)

	if err := provider.UpTo(ctx, 77); err != nil {
		t.Fatalf("migrate through 00077: %v", err)
	}
	if err := provider.UpTo(ctx, 77); err != nil {
		t.Fatalf("replay 00077: %v", err)
	}
	assertGitCaptureMigrationObjects(t, ctx, pool, true)

	now := time.Date(2026, 8, 4, 7, 0, 0, 0, time.UTC)
	const workspaceID = "77000000-0000-4000-8000-000000000001"
	insertGitRemoteSyncWorkspace(t, ctx, pool, workspaceID, now)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace_git_capture_checkpoint(
		workspace_id,completed_head_oid,version,created_at,updated_at
	) VALUES($1,repeat('a',40),1,$2,$2)`, workspaceID, now); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(ctx, `UPDATE core.workspace_git_capture_checkpoint
		SET completed_head_oid=repeat('b',40),version=2,updated_at=$2 WHERE workspace_id=$1`, workspaceID, now.Add(time.Minute))
	assertPostgresCode(t, err, "55000")
}

func assertGitRemoteSyncMigrationObjects(t *testing.T, ctx context.Context, queryer *pgxpool.Pool, expected bool) {
	t.Helper()
	for _, object := range []string{
		"ops.git_remote_config",
		"ops.git_remote_config_revision",
		"ops.git_sync_run",
		"ops.git_sync_outbox",
		"ops.uq_git_sync_outbox_run_pending_index",
		"change_control.idx_writeback_execution_auto_sync_candidates",
	} {
		var exists bool
		if err := queryer.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, object).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != expected {
			t.Fatalf("object %s exists=%t, want %t", object, exists, expected)
		}
	}
}

func assertGitCaptureMigrationObjects(t *testing.T, ctx context.Context, queryer *pgxpool.Pool, expected bool) {
	t.Helper()
	var tableExists bool
	if err := queryer.QueryRow(ctx, `SELECT to_regclass('core.workspace_git_capture_checkpoint') IS NOT NULL`).Scan(&tableExists); err != nil {
		t.Fatal(err)
	}
	if tableExists != expected {
		t.Fatalf("workspace git capture checkpoint exists=%t, want %t", tableExists, expected)
	}
	var columnExists bool
	if err := queryer.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema='core' AND table_name='source' AND column_name='removed_at'
	)`).Scan(&columnExists); err != nil {
		t.Fatal(err)
	}
	if columnExists != expected {
		t.Fatalf("core.source.removed_at exists=%t, want %t", columnExists, expected)
	}
}

func insertGitRemoteSyncWorkspace(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID string, now time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,root_fingerprint,binding_version,git_repository_path,git_checked_at,
		status,availability,availability_reason,availability_checked_at,version,created_at,updated_at)
		VALUES($1,'git-sync-constraints',$2,repeat('7',64),1,$2,$3,
		'inactive','available',NULL,$3,1,$3,$3)`, workspaceID, "/tmp/git-sync-"+workspaceID, now); err != nil {
		t.Fatal(err)
	}
}

func insertPendingGitRemoteSyncRun(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runID, workspaceID, idempotencyKey string, now time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO ops.git_sync_run(
		id,workspace_id,config_revision,remote_url,branch,trigger,idempotency_key,request_hash,status,direction,
		failure_class,index_status,version,created_at,updated_at
	) VALUES($1,$2,1,'https://git.example.test/acme/knowledge.git','main','MANUAL',$3,repeat('a',64),
		'PENDING','UNKNOWN','NONE','NOT_REQUIRED',1,$4,$4)`, runID, workspaceID, idempotencyKey, now); err != nil {
		t.Fatal(err)
	}
}
