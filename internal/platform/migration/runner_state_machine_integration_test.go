//go:build integration

package migration

import (
	"context"
	"errors"
	"testing"

	projectmigrations "github.com/CodeZen-Lizhi/zhixu/migrations"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestWorkflowRuntimeStateMachineMigrationCompatibility(t *testing.T) {
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
		t.Fatal(err)
	}

	var version string
	if err := pool.QueryRow(ctx, `SELECT value FROM core.schema_meta WHERE key = 'workflow_runtime_state_machine'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != "m4-b" {
		t.Fatalf("workflow runtime schema version=%q", version)
	}

	workspaceID := "11111111-1111-4111-8111-111111111111"
	definitionID := "22222222-2222-4222-8222-222222222222"
	runID := "33333333-3333-4333-8333-333333333333"
	nodeID := "44444444-4444-4444-8444-444444444444"
	attemptID := "55555555-5555-4555-8555-555555555555"
	queries := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO core.workspace (id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at) VALUES ($1,'migration-test','/tmp/migration-test','/tmp/migration-test',CURRENT_TIMESTAMP,'active',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, []any{workspaceID}},
		{`INSERT INTO workflow.definition (id,workspace_id,key,version,graph,created_at) VALUES ($1,$2,'migration-test',1,'{"nodes":[{"key":"first","type":"deterministic.hash","depends_on":[]}]}',CURRENT_TIMESTAMP)`, []any{definitionID, workspaceID}},
		{`INSERT INTO workflow.run (id,workspace_id,definition_id,status,input,version,created_at,updated_at) VALUES ($1,$2,$3,'pending','{}',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, []any{runID, workspaceID, definitionID}},
		{`INSERT INTO workflow.node_run (id,run_id,node_key,node_type,status,input,version,created_at,updated_at) VALUES ($1,$2,'first','deterministic.hash','pending','{}',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, []any{nodeID, runID}},
		{`INSERT INTO workflow.node_attempt (id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at) VALUES ($1,$2,1,1,0,'delivery-1','owner-1',CURRENT_TIMESTAMP + INTERVAL '1 minute','running',CURRENT_TIMESTAMP)`, []any{attemptID, nodeID}},
	}
	for _, query := range queries {
		if _, err := pool.Exec(ctx, query.sql, query.args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE workflow.node_attempt SET status='succeeded', lease_owner=NULL, lease_until=NULL, ended_at=CURRENT_TIMESTAMP WHERE id=$1`, attemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workflow.node_attempt SET error_code='late' WHERE id=$1`, attemptID); err == nil {
		t.Fatal("completed node_attempt accepted mutation")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55000" {
			t.Fatalf("append-only error=%v", err)
		}
	}

	// 00016/00015/00014 have no Retrieval data and 00013 has no Proposal→Run binding
	// in this fixture; after removing all four, 00012 must reject populated Attempt data.
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	annotated, err := NewLegacyAnnotationFS(projectmigrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, annotated, goose.WithTableName(projectMigrationTable))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Down(ctx); err != nil {
		t.Fatalf("00016 Down rejected empty Embedding Hybrid Search schema: %v", err)
	}
	if _, err := provider.Down(ctx); err != nil {
		t.Fatalf("00015 Down rejected empty Reindex Consumer schema: %v", err)
	}
	if _, err := provider.Down(ctx); err != nil {
		t.Fatalf("00014 Down rejected empty Retrieval schema: %v", err)
	}
	if _, err := provider.Down(ctx); err != nil {
		t.Fatalf("00013 Down rejected unbound runtime fixture: %v", err)
	}
	if _, err := provider.Down(ctx); err == nil {
		t.Fatal("00012 Down accepted node_attempt data")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55000" {
			t.Fatalf("guarded Down error=%v", err)
		}
	}
}

func TestWorkflowRuntimeStateMachineLegacyTerminalRowsRemainReadable(t *testing.T) {
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
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.run WHERE status IN ('succeeded','failed','cancelled')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("unexpected terminal rows=%d", count)
	}
}
