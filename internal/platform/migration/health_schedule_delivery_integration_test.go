//go:build integration

package migration

import (
	"context"
	"strings"
	"testing"

	projectmigrations "github.com/CodeZen-Lizhi/zhixu/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestHealthScheduleDeliveryMigrationSchemaAndEmptyDownUp(t *testing.T) {
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

	assertHealthScheduleDeliveryMigrationShape(t, ctx, pool)

	provider := migrationProvider(t, pool)
	if _, err := provider.DownTo(ctx, 26); err != nil {
		t.Fatalf("00027 empty Down failed: %v", err)
	}
	assertHealthScheduleDeliveryMigrationAbsent(t, ctx, pool)

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("00027 Up after Down failed: %v", err)
	}
	assertHealthScheduleDeliveryMigrationShape(t, ctx, pool)
}

func TestHealthScheduleDeliveryMigrationGuardsPendingStateAndConfiguration(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	migrateSmartCollectionHealthTestDatabase(t, ctx, pool)

	const (
		workspaceID = "f5a00000-0000-4000-8000-000000000001"
		scheduleID  = "f5b00000-0000-4000-8000-000000000001"
	)
	insertSemanticLinkWorkspace(t, ctx, pool, workspaceID, "health-schedule-delivery")
	insertHealthScheduleDeliveryFixture(t, ctx, pool, workspaceID, scheduleID)

	if _, err := pool.Exec(ctx, `WITH database_clock AS (
		SELECT clock_timestamp() AS now
	)
	UPDATE ops.health_schedule
	SET pending_due_at=next_run_at,
	    dispatch_lease_until=database_clock.now + interval '2 minutes',
	    next_run_at=database_clock.now + interval '1 day',
	    version=version+1,
	    updated_at=database_clock.now
	FROM database_clock
	WHERE id=$1 AND workspace_id=$2`, scheduleID, workspaceID); err != nil {
		t.Fatal(err)
	}

	_, err := pool.Exec(ctx, `UPDATE ops.health_schedule
		SET max_items=max_items+1,version=version+1,updated_at=clock_timestamp()
		WHERE id=$1 AND workspace_id=$2`, scheduleID, workspaceID)
	assertPostgresCode(t, err, "55000")

	_, err = pool.Exec(ctx, `UPDATE ops.health_schedule
		SET cadence='DISABLED',next_run_at=NULL,version=version+1,updated_at=clock_timestamp()
		WHERE id=$1 AND workspace_id=$2`, scheduleID, workspaceID)
	assertPostgresCode(t, err, "55000")

	_, err = migrationProvider(t, pool).DownTo(ctx, 26)
	assertPostgresCode(t, err, "55000")

	if _, err := pool.Exec(ctx, `UPDATE ops.health_schedule
		SET last_run_at=pending_due_at,pending_due_at=NULL,dispatch_lease_until=NULL,
		    version=version+1,updated_at=clock_timestamp()
		WHERE id=$1 AND workspace_id=$2`, scheduleID, workspaceID); err != nil {
		t.Fatalf("acknowledge missed due: %v", err)
	}
	_, err = migrationProvider(t, pool).DownTo(ctx, 26)
	assertPostgresCode(t, err, "55000")
}

func assertHealthScheduleDeliveryMigrationShape(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var columns, constraint, timeConstraint, index, schemaMeta int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema='ops' AND table_name='health_schedule'
		  AND column_name IN ('pending_due_at','dispatch_lease_until')
		  AND data_type='timestamp with time zone'`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint
		WHERE conname='ops_health_schedule_dispatch_binding'
		  AND conrelid='ops.health_schedule'::regclass
		  AND contype='c' AND convalidated`).Scan(&constraint); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint
		WHERE conname='ops_health_schedule_time_order'
		  AND conrelid='ops.health_schedule'::regclass
		  AND contype='c' AND convalidated
		  AND position('last_run_at >= created_at' IN pg_get_constraintdef(oid))=0`).Scan(&timeConstraint); err != nil {
		t.Fatal(err)
	}
	var predicate string
	if err := pool.QueryRow(ctx, `SELECT pg_get_expr(index_row.indpred,index_row.indrelid)
		FROM pg_index index_row
		JOIN pg_class index_class ON index_class.oid=index_row.indexrelid
		JOIN pg_namespace namespace ON namespace.oid=index_class.relnamespace
		WHERE namespace.nspname='ops'
		  AND index_class.relname='idx_ops_health_schedule_pending_lease'`).Scan(&predicate); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(predicate, "pending_due_at IS NOT NULL") {
		index = 1
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.schema_meta
		WHERE key='health_schedule_delivery' AND value='m7-03'`).Scan(&schemaMeta); err != nil {
		t.Fatal(err)
	}
	if columns != 2 || constraint != 1 || timeConstraint != 1 || index != 1 || schemaMeta != 1 {
		t.Fatalf("00027 shape columns=%d constraint=%d time_constraint=%d index=%d schema_meta=%d predicate=%q", columns, constraint, timeConstraint, index, schemaMeta, predicate)
	}
}

func assertHealthScheduleDeliveryMigrationAbsent(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var columns, constraint, index, schemaMeta int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema='ops' AND table_name='health_schedule'
		  AND column_name IN ('pending_due_at','dispatch_lease_until')`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint
		WHERE conname='ops_health_schedule_dispatch_binding'`).Scan(&constraint); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes
		WHERE schemaname='ops' AND indexname='idx_ops_health_schedule_pending_lease'`).Scan(&index); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.schema_meta
		WHERE key='health_schedule_delivery'`).Scan(&schemaMeta); err != nil {
		t.Fatal(err)
	}
	if columns != 0 || constraint != 0 || index != 0 || schemaMeta != 0 {
		t.Fatalf("00027 Down columns=%d constraint=%d index=%d schema_meta=%d", columns, constraint, index, schemaMeta)
	}
}

func insertHealthScheduleDeliveryFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, scheduleID string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO ops.health_schedule(
		id,workspace_id,scope_type,scope_ref,scope_version,scope_schema_version,scope_hash,
		cadence,cron_expression,timezone,max_items,next_run_at,last_run_at,version,created_at,updated_at
	) VALUES(
		$1,$2,'WORKSPACE',$2,1,'health-scope/workspace/v1',NULL,
		'DAILY',NULL,'UTC',100,clock_timestamp() - interval '3 days',NULL,1,clock_timestamp(),clock_timestamp()
	)`, scheduleID, workspaceID); err != nil {
		t.Fatal(err)
	}
}
