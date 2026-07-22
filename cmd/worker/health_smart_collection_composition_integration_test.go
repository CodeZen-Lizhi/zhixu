//go:build integration

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	collectionpostgres "github.com/CodeZen-Lizhi/zhixu/internal/collection/adapter/postgres"
	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	collectiondomain "github.com/CodeZen-Lizhi/zhixu/internal/collection/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	healthdomain "github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
	healthworkflow "github.com/CodeZen-Lizhi/zhixu/internal/health/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestWorkerHealthSmartCollectionCompositionExecutesDetectorAndFailsClosedOnDrift(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a PostgreSQL admin database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	pool := newMigratedWorkerTestPool(t, databaseURL)
	components, err := newWorkerComponents(pool, config.Defaults(), slog.New(slog.NewTextHandler(io.Discard, nil)), observability.NewMemoryMetrics())
	if err != nil {
		t.Fatal(err)
	}
	if components.healthScanStart == nil || components.healthScan == nil {
		t.Fatal("worker smart-collection health start or executor is unavailable")
	}
	executor, err := components.executors.Resolve(healthapp.HealthScanNodeKind, healthapp.HealthScanInputSchemaVersion)
	if err != nil || executor != components.healthScan {
		t.Fatalf("health executor=%T err=%v", executor, err)
	}

	workspaceID, topicID := seedWorkerHealthCollectionFixture(t, ctx, pool)
	service := newWorkerHealthCollectionService(t, pool)
	created, err := service.Create(ctx, collectionapp.CreateCommand{
		WorkspaceID: workspaceID, Name: "Worker Health Collection", Query: workerEmptyClaimCollectionQuery(),
		ViewType: collectiondomain.ViewTypeList, IdempotencyKey: "worker-health-collection-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := service.PlanDurableScan(ctx, workspaceID, created.Collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	started, err := components.healthScanStart.Start(ctx, workerSmartCollectionStart(workspaceID, binding, "worker-health-smart-success"))
	if err != nil {
		t.Fatal(err)
	}
	execution := workerHealthExecution(t, ctx, pool, started.Scan)
	result, err := executor.Execute(ctx, execution)
	if err != nil || len(result.Output) == 0 {
		t.Fatalf("health smart-collection execute output=%s err=%v", result.Output, err)
	}
	var scanStatus, orphanStatus string
	var orphanPage int64
	if err := pool.QueryRow(ctx, `SELECT scan.status,coverage.status,COALESCE((coverage.checkpoint->>'page')::bigint,0)
		FROM ops.health_scan scan JOIN ops.health_scan_detector coverage ON coverage.scan_id=scan.id AND coverage.workspace_id=scan.workspace_id
		WHERE scan.id=$1 AND scan.workspace_id=$2 AND coverage.detector_id='health.detector.orphan'`, string(started.Scan.ID), string(workspaceID)).Scan(&scanStatus, &orphanStatus, &orphanPage); err != nil {
		t.Fatal(err)
	}
	if scanStatus != string(healthdomain.ScanStatusPartial) || orphanStatus != string(healthdomain.DetectorCoverageStatusSucceeded) || orphanPage < 1 {
		t.Fatalf("scan status=%q orphan status=%q page=%d", scanStatus, orphanStatus, orphanPage)
	}
	assertWorkerHealthCompletionEvent(t, ctx, pool, started.Scan.ID, workspaceID, "partial")

	stableBinding, err := service.PlanDurableScan(ctx, workspaceID, created.Collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	staleStarted, err := components.healthScanStart.Start(ctx, workerSmartCollectionStart(workspaceID, stableBinding, "worker-health-smart-stale"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE core.topic SET version=version+1,updated_at=updated_at+interval '1 second' WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(topicID)); err != nil {
		t.Fatal(err)
	}
	_, err = executor.Execute(ctx, workerHealthExecution(t, ctx, pool, staleStarted.Scan))
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != healthdomain.ErrorCodeScanScopeStale {
		t.Fatalf("stale worker execution error=%v", err)
	}
	var staleStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM ops.health_scan WHERE id=$1 AND workspace_id=$2`, string(staleStarted.Scan.ID), string(workspaceID)).Scan(&staleStatus); err != nil || staleStatus != string(healthdomain.ScanStatusFailed) {
		t.Fatalf("stale scan status=%q err=%v", staleStatus, err)
	}
	assertWorkerHealthCompletionEvent(t, ctx, pool, staleStarted.Scan.ID, workspaceID, "failed")
}

func assertWorkerHealthCompletionEvent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, scanID, workspaceID foundation.ID, wantStatus string) {
	t.Helper()
	var count int
	var status, eventWorkflowRunID, scanWorkflowRunID string
	var eventVersion, scanVersion int64
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM ops.server_event WHERE workspace_id=$1 AND resource_ref=$2),
		event.payload_summary->>'status',event.workflow_run_id::text,scan.workflow_run_id::text,event.resource_version,scan.version
		FROM ops.health_scan scan JOIN ops.server_event event
		  ON event.workspace_id=scan.workspace_id AND event.resource_ref='health_scan:' || scan.id::text
		WHERE scan.id=$3 AND scan.workspace_id=$1`, string(workspaceID), "health_scan:"+string(scanID), string(scanID)).Scan(
		&count, &status, &eventWorkflowRunID, &scanWorkflowRunID, &eventVersion, &scanVersion,
	); err != nil {
		t.Fatal(err)
	}
	if count != 1 || status != wantStatus || eventWorkflowRunID != scanWorkflowRunID || eventVersion != scanVersion {
		t.Fatalf("completion event count=%d status=%q event_run=%q scan_run=%q event_version=%d scan_version=%d", count, status, eventWorkflowRunID, scanWorkflowRunID, eventVersion, scanVersion)
	}
}

func workerHealthExecution(t *testing.T, ctx context.Context, pool *pgxpool.Pool, scan healthdomain.Scan) workflowapp.ExecutionContext {
	t.Helper()
	var nodeRunID string
	var nodeKey, nodeKind string
	var input []byte
	var inputSchemaVersion int
	if err := pool.QueryRow(ctx, `SELECT id::text,node_key,node_type,input,input_schema_version FROM workflow.node_run WHERE run_id=$1`, string(scan.WorkflowRunID)).Scan(&nodeRunID, &nodeKey, &nodeKind, &input, &inputSchemaVersion); err != nil {
		t.Fatal(err)
	}
	definition, err := healthworkflow.RegisteredDefinition()
	if err != nil {
		t.Fatal(err)
	}
	return workflowapp.ExecutionContext{
		WorkspaceID: scan.WorkspaceID, RunID: scan.WorkflowRunID, NodeRunID: foundation.ID(nodeRunID),
		NodeKey: nodeKey, NodeKind: nodeKind, InputSchemaVersion: inputSchemaVersion,
		DefinitionVersion: definition.Version, DefinitionHash: definition.GraphHash, Input: json.RawMessage(input),
	}
}

func workerSmartCollectionStart(workspaceID foundation.ID, binding collectionapp.DurableScanBinding, idempotencyKey string) healthapp.ScanStartCommand {
	return healthapp.ScanStartCommand{
		WorkspaceID: workspaceID,
		Scope: healthdomain.ScanScope{
			Type: healthdomain.ScanScopeTypeSmartCollection, Ref: binding.CollectionID,
			Version: binding.CollectionVersion, SchemaVersion: "health-scope/smart-collection/v1",
			Hash: binding.QueryHash, ReadModelRevision: binding.ReadModelRevision, ExactCount: binding.ExactCount,
		},
		MaxItems: 100, IdempotencyKey: idempotencyKey, PreventScopeConcurrency: true,
	}
}

func newWorkerHealthCollectionService(t *testing.T, pool *pgxpool.Pool) *collectionapp.Service {
	t.Helper()
	repository, err := collectionpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	service, err := collectionapp.NewService(collectionapp.Dependencies{Repository: repository, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func workerEmptyClaimCollectionQuery() collectiondomain.Query {
	return collectiondomain.Query{SchemaVersion: collectiondomain.QuerySchemaVersionV1, Root: collectiondomain.Clause{
		Kind: collectiondomain.ClauseKindGroup, Operator: "AND", Clauses: []collectiondomain.Clause{{
			Kind: collectiondomain.ClauseKindPredicate, Field: "object_type", Operator: "EQ", Value: json.RawMessage(`"CLAIM"`),
		}},
	}}
}

func seedWorkerHealthCollectionFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (foundation.ID, foundation.ID) {
	t.Helper()
	workspaceID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	topicID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	root := "/tmp/worker-health-composition-" + string(workspaceID)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'worker-health-composition',$2,$2,$3,'test',1,$3,$3)`, string(workspaceID), root, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO core.topic(id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at) VALUES($1,$2,'worker health topic','worker health topic','','ACTIVE',1,$3,$3)`, string(topicID), string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	return workspaceID, topicID
}
