//go:build integration

package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
)

func TestWorkerHealthAffectedChangeCompositionConsumesTypedOutboxExactlyOnce(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a PostgreSQL admin database")
	}
	ctx := context.Background()
	pool := newMigratedWorkerTestPool(t, databaseURL)
	components, err := newWorkerComponents(pool, config.Defaults(), slog.New(slog.NewTextHandler(io.Discard, nil)), observability.NewMemoryMetrics())
	if err != nil {
		t.Fatal(err)
	}
	if components.healthAffected == nil {
		t.Fatal("worker typed affected-change dispatcher is unavailable")
	}

	workspaceID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	topicID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	rootPath := "/tmp/worker-health-affected-" + string(workspaceID)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
		VALUES($1,'worker-health-affected',$2,$2,$3,'test',1,$3,$3)`, string(workspaceID), rootPath, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO core.topic(
		id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at)
		VALUES($1,$2,'worker affected topic','worker affected topic','','ACTIVE',1,$3,$3)`, string(topicID), string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO core.knowledge_command_receipt(
		workspace_id,idempotency_key,request_hash,command_type,aggregate_type,aggregate_id,aggregate_version,created_at)
		VALUES($1,'worker-health-affected', $2,'topic.create','TOPIC',$3,1,$4)`,
		string(workspaceID), strings.Repeat("a", 64), string(topicID), now); err != nil {
		t.Fatal(err)
	}

	first, err := components.healthAffected.DispatchBatch(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if first.Processed != 1 || first.Published != 1 || first.Deferred != 0 || first.Poisoned != 0 {
		t.Fatalf("first affected-change dispatch=%+v", first)
	}
	second, err := components.healthAffected.DispatchBatch(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if second.Processed != 0 || second.Published != 0 || second.Deferred != 0 || second.Poisoned != 0 {
		t.Fatalf("replayed affected-change dispatch=%+v", second)
	}

	var scans, runs, nodes, jobs, published, legacy int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM ops.health_scan WHERE workspace_id=$1),
		(SELECT count(*) FROM workflow.run WHERE workspace_id=$1),
		(SELECT count(*) FROM workflow.node_run node JOIN workflow.run run ON run.id=node.run_id WHERE run.workspace_id=$1),
		(SELECT count(*) FROM workflow.river_job job JOIN workflow.node_run node ON job.args->>'node_run_id'=node.id::text
		 JOIN workflow.run run ON run.id=node.run_id WHERE run.workspace_id=$1),
		(SELECT count(*) FROM ops.health_affected_change_outbox WHERE workspace_id=$1 AND published_at IS NOT NULL),
		(SELECT count(*) FROM workflow.outbox_event WHERE workspace_id=$1 AND event_type='health.scan.requested')`,
		string(workspaceID)).Scan(&scans, &runs, &nodes, &jobs, &published, &legacy); err != nil {
		t.Fatal(err)
	}
	if scans != 1 || runs != 1 || nodes != 1 || jobs != 1 || published != 1 || legacy != 0 {
		t.Fatalf("scans=%d runs=%d nodes=%d jobs=%d published=%d legacy=%d", scans, runs, nodes, jobs, published, legacy)
	}
}
