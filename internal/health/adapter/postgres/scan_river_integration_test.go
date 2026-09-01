//go:build integration && testcontainers

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	healthworkflowadapter "github.com/CodeZen-Lizhi/zhixu/internal/health/adapter/workflow"
	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

func TestHealthScanRunsThroughRealRiverAndReplays(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := newHealthRiverTestPool(t, ctx)
	workspaceID, topicID := seedHealthRiverFixture(t, ctx, pool)
	detector := newHealthRiverDetector(topicID)
	service, registry, coordinator := newHealthRiverRuntime(t, pool, detector)
	worker := startHealthRiverWorker(t, ctx, pool, registry, coordinator, "health-river-success")
	defer stopHealthRiverWorker(worker)

	started := startHealthRiverScan(t, ctx, service, registry, workspaceID, "health-river-success")
	if err := waitForHealthRiverScan(ctx, pool, started.Scan.ID, domain.ScanStatusSucceeded, workflowdomain.RunStatusSucceeded); err != nil {
		t.Fatal(err)
	}
	assertHealthRiverSuccess(t, ctx, pool, started.Scan.ID, workspaceID, 1, 1)

	replayed, err := service.Start(ctx, healthRiverStartCommand(workspaceID, "health-river-success"))
	if err != nil || !replayed.Replayed || replayed.Scan.ID != started.Scan.ID || replayed.Scan.WorkflowRunID != started.Scan.WorkflowRunID {
		t.Fatalf("replay=%#v err=%v", replayed, err)
	}
}

func TestHealthScanReplaysAfterWorkflowCompletionResponseLoss(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := newHealthRiverTestPool(t, ctx)
	workspaceID, topicID := seedHealthRiverFixture(t, ctx, pool)
	detector := newHealthRiverDetector(topicID)
	service, registry, coordinator := newHealthRiverRuntime(t, pool, detector)
	started := startHealthRiverScan(t, ctx, service, registry, workspaceID, "health-completion-response-loss")
	job := healthRiverJob(t, ctx, pool, started.Scan.WorkflowRunID)

	lossy := &failFirstHealthCompletion{delegate: coordinator}
	worker, err := riveradapter.NewRuntimeNodeWorker(registry, lossy, "health-response-loss", 10*time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Work(ctx, job); err == nil {
		t.Fatal("injected workflow completion response loss was not returned")
	}
	if err := worker.Work(ctx, job); err != nil {
		t.Fatal(err)
	}
	if !lossy.outputsMatch() {
		t.Fatal("health scan terminal replay changed the workflow output")
	}
	if err := waitForHealthRiverScan(ctx, pool, started.Scan.ID, domain.ScanStatusSucceeded, workflowdomain.RunStatusSucceeded); err != nil {
		t.Fatal(err)
	}
	assertHealthRiverSuccess(t, ctx, pool, started.Scan.ID, workspaceID, 1, 1)
}

func TestHealthScanCancellationConvergesWithWorkflow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := newHealthRiverTestPool(t, ctx)
	workspaceID, topicID := seedHealthRiverFixture(t, ctx, pool)
	detector := newHealthRiverDetector(topicID)
	detector.block = true
	detector.entered = make(chan struct{})
	service, registry, coordinator := newHealthRiverRuntime(t, pool, detector)
	worker := startHealthRiverWorkerWithHeartbeat(t, ctx, pool, registry, coordinator, "health-river-cancel", 100*time.Millisecond)
	defer stopHealthRiverWorker(worker)

	started := startHealthRiverScan(t, ctx, service, registry, workspaceID, "health-river-cancel")
	select {
	case <-detector.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var runVersion int64
	if err := pool.QueryRow(ctx, `SELECT version FROM workflow.run WHERE id=$1`, string(started.Scan.WorkflowRunID)).Scan(&runVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Cancel(ctx, workflowapp.RunControlCommand{WorkflowRunID: started.Scan.WorkflowRunID, ExpectedVersion: runVersion, IdempotencyKey: "cancel-health-scan"}); err != nil {
		t.Fatal(err)
	}
	if err := waitForHealthRiverScan(ctx, pool, started.Scan.ID, domain.ScanStatusCancelled, workflowdomain.RunStatusCancelled); err != nil {
		t.Fatal(err)
	}
	assertHealthRiverCompletionEvent(t, ctx, pool, started.Scan.ID, workspaceID, "cancelled")
	var issues int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.health_issue WHERE workspace_id=$1`, string(workspaceID)).Scan(&issues); err != nil || issues != 0 {
		t.Fatalf("issues=%d err=%v", issues, err)
	}
}

func TestHealthScanRealRiverRetriesAndPersistsExhaustion(t *testing.T) {
	t.Run("retry_recovers", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		pool := newHealthRiverTestPool(t, ctx)
		workspaceID, topicID := seedHealthRiverFixture(t, ctx, pool)
		detector := newHealthRiverDetector(topicID)
		detector.remainingFailures = 1
		service, registry, coordinator := newHealthRiverRuntime(t, pool, detector)
		worker := startHealthRiverWorker(t, ctx, pool, registry, coordinator, "health-river-retry")
		defer stopHealthRiverWorker(worker)

		started := startHealthRiverScan(t, ctx, service, registry, workspaceID, "health-river-retry")
		if err := waitForHealthRiverScan(ctx, pool, started.Scan.ID, domain.ScanStatusSucceeded, workflowdomain.RunStatusSucceeded); err != nil {
			t.Fatal(err)
		}
		if detector.callCount() != 2 {
			t.Fatalf("detector calls=%d want=2", detector.callCount())
		}
	})

	t.Run("retry_budget_exhausted", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		pool := newHealthRiverTestPool(t, ctx)
		workspaceID, topicID := seedHealthRiverFixture(t, ctx, pool)
		detector := newHealthRiverDetector(topicID)
		detector.alwaysFail = true
		service, registry, coordinator := newHealthRiverRuntime(t, pool, detector)
		worker := startHealthRiverWorker(t, ctx, pool, registry, coordinator, "health-river-exhausted")
		defer stopHealthRiverWorker(worker)

		started := startHealthRiverScan(t, ctx, service, registry, workspaceID, "health-river-exhausted")
		if err := waitForHealthRiverScan(ctx, pool, started.Scan.ID, domain.ScanStatusFailed, workflowdomain.RunStatusFailed); err != nil {
			t.Fatal(err)
		}
		var errorCode string
		var issues int
		if err := pool.QueryRow(ctx, `SELECT last_error->>'code' FROM ops.health_scan WHERE id=$1`, string(started.Scan.ID)).Scan(&errorCode); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.health_issue WHERE workspace_id=$1`, string(workspaceID)).Scan(&issues); err != nil {
			t.Fatal(err)
		}
		if detector.callCount() != 4 || errorCode != "HEALTH_DETECTOR_SOURCE_TIMEOUT" || issues != 0 {
			t.Fatalf("calls=%d code=%s issues=%d", detector.callCount(), errorCode, issues)
		}
		assertHealthRiverCompletionEvent(t, ctx, pool, started.Scan.ID, workspaceID, "failed")
	})
}

func TestHealthScanCheckpointSurvivesWorkerRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := newHealthRiverTestPool(t, ctx)
	workspaceID, topicID := seedHealthRiverFixture(t, ctx, pool)
	detector := newHealthRiverDetector(topicID)
	detector.twoPages = true
	detector.remainingFailures = 1
	service, registry, coordinator := newHealthRiverRuntime(t, pool, detector)
	started := startHealthRiverScan(t, ctx, service, registry, workspaceID, "health-worker-restart")

	firstWorker, err := riveradapter.NewRuntimeNodeWorker(registry, coordinator, "health-worker-before-restart", 10*time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := firstWorker.Work(ctx, healthRiverJob(t, ctx, pool, started.Scan.WorkflowRunID)); err != nil {
		t.Fatal(err)
	}
	var checkpoint string
	var runStatus workflowdomain.RunStatus
	if err := pool.QueryRow(ctx, `SELECT detector.checkpoint->>'cursor',run.status
		FROM ops.health_scan_detector detector
		JOIN ops.health_scan scan ON scan.id=detector.scan_id
		JOIN workflow.run run ON run.id=scan.workflow_run_id
		WHERE detector.scan_id=$1`, string(started.Scan.ID)).Scan(&checkpoint, &runStatus); err != nil {
		t.Fatal(err)
	}
	if checkpoint != "page-2" || runStatus != workflowdomain.RunStatusRetryWait {
		t.Fatalf("checkpoint=%q run=%s", checkpoint, runStatus)
	}

	_, restartedRegistry, restartedCoordinator := newHealthRiverRuntime(t, pool, detector)
	if err := waitForHealthRiverRetryDue(ctx, pool, started.Scan.WorkflowRunID); err != nil {
		t.Fatal(err)
	}
	secondWorker, err := riveradapter.NewRuntimeNodeWorker(restartedRegistry, restartedCoordinator, "health-worker-after-restart", 10*time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := secondWorker.Work(ctx, healthRiverJob(t, ctx, pool, started.Scan.WorkflowRunID)); err != nil {
		t.Fatal(err)
	}
	if err := waitForHealthRiverScan(ctx, pool, started.Scan.ID, domain.ScanStatusSucceeded, workflowdomain.RunStatusSucceeded); err != nil {
		t.Fatal(err)
	}
	if got, want := detector.cursorsSeen(), []string{"", "page-2", "page-2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cursors=%v want=%v", got, want)
	}
	assertHealthRiverSuccess(t, ctx, pool, started.Scan.ID, workspaceID, 1, 1)
}

func newHealthRiverRuntime(t *testing.T, pool *pgxpool.Pool, detector healthapp.Detector) (*healthapp.ScanService, *workflowapp.ExecutorRegistry, *workflowapp.RuntimeCoordinator) {
	t.Helper()
	insertClient, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(insertClient)
	if err != nil {
		t.Fatal(err)
	}
	events, err := eventspostgres.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	cancellationGuard, err := NewScanCancellationGuard(events)
	if err != nil {
		t.Fatal(err)
	}
	runtimeRepository, err := workflowpostgres.NewRuntimeRepository(pool, inserter, cancellationGuard)
	if err != nil {
		t.Fatal(err)
	}
	scanRepository, err := NewScanRepository(pool, runtimeRepository, events, foundation.NewUUIDGenerator(nil), foundation.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	startService, err := healthapp.NewScanService(scanRepository, scanRepository)
	if err != nil {
		t.Fatal(err)
	}
	stateService, err := healthapp.NewScanStateService(scanRepository)
	if err != nil {
		t.Fatal(err)
	}
	issueRepository, err := NewIssueRepository(pool, foundation.NewUUIDGenerator(nil))
	if err != nil {
		t.Fatal(err)
	}
	detectorRegistry, err := healthapp.NewRegistry([]healthapp.Detector{detector}, nil)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := healthworkflowadapter.NewHealthScanExecutor(detectorRegistry, stateService, issueRepository, foundation.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := workflowapp.NewValidationCatalog([]int{1}, capability.All())
	if err != nil {
		t.Fatal(err)
	}
	registry, err := workflowapp.NewExecutorRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(healthapp.HealthScanNodeKind, healthapp.HealthScanInputSchemaVersion, executor); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	coordinator, err := workflowapp.NewRuntimeCoordinator(runtimeRepository)
	if err != nil {
		t.Fatal(err)
	}
	return startService, registry, coordinator
}

func startHealthRiverScan(t *testing.T, ctx context.Context, service *healthapp.ScanService, registry *workflowapp.ExecutorRegistry, workspaceID foundation.ID, key string) healthapp.ScanStartResult {
	t.Helper()
	result, err := service.Start(ctx, healthRiverStartCommand(workspaceID, key))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func healthRiverStartCommand(workspaceID foundation.ID, key string) healthapp.ScanStartCommand {
	return healthRiverStartCommandForCoverage(workspaceID, key, []domain.DetectorCoverage{{DetectorID: healthRiverDetectorID, DetectorVersion: healthRiverDetectorVersion, Status: domain.DetectorCoverageStatusPending}})
}

func healthRiverStartCommandForCoverage(workspaceID foundation.ID, key string, coverage []domain.DetectorCoverage) healthapp.ScanStartCommand {
	return healthapp.ScanStartCommand{
		WorkspaceID:    workspaceID,
		Scope:          domain.ScanScope{Type: domain.ScanScopeTypeWorkspace, Ref: workspaceID, Version: 1, SchemaVersion: "health-scope/workspace/v1"},
		Coverage:       coverage,
		MaxItems:       100,
		IdempotencyKey: key,
	}
}

func newHealthRiverTestPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	return newHealthIntegrationPool(t)
}

func seedHealthRiverFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (foundation.ID, foundation.ID) {
	t.Helper()
	ids := foundation.NewUUIDGenerator(nil)
	workspaceID, err := ids.New()
	if err != nil {
		t.Fatal(err)
	}
	topicID, err := ids.New()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	rootPath := "/tmp/health-river-" + string(workspaceID)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
		VALUES($1,'health-river-fixture',$2,$2,$3,'inactive',1,$3,$3)`, string(workspaceID), rootPath, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.topic(id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at)
		VALUES($1,$2,'Health River Topic','health river topic','durable health scan fixture','ACTIVE',1,$3,$3)`, string(topicID), string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupHealthIntegrationWorkspace(t, pool, workspaceID) })
	return workspaceID, topicID
}

func startHealthRiverWorker(t *testing.T, ctx context.Context, pool *pgxpool.Pool, registry *workflowapp.ExecutorRegistry, coordinator *workflowapp.RuntimeCoordinator, owner string) *riveradapter.Client {
	t.Helper()
	return startHealthRiverWorkerWithHeartbeat(t, ctx, pool, registry, coordinator, owner, time.Second)
}

func startHealthRiverWorkerWithHeartbeat(t *testing.T, ctx context.Context, pool *pgxpool.Pool, registry *workflowapp.ExecutorRegistry, coordinator riveradapter.RuntimeExecutionCoordinator, owner string, heartbeat time.Duration) *riveradapter.Client {
	t.Helper()
	runtimeWorker, err := riveradapter.NewRuntimeNodeWorker(registry, coordinator, owner, 10*time.Second, heartbeat)
	if err != nil {
		t.Fatal(err)
	}
	workers := riveradapter.NewWorkers()
	if err := riveradapter.AddRuntimeWorkerSafely(workers, runtimeWorker); err != nil {
		t.Fatal(err)
	}
	client, err := riveradapter.NewClient(pool, workers)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return client
}

func stopHealthRiverWorker(client *riveradapter.Client) {
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = client.Stop(ctx)
}

func healthRiverJob(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runID foundation.ID) *river.Job[riveradapter.NodeJobArgs] {
	t.Helper()
	var nodeID foundation.ID
	var dispatchNo int
	var jobID int64
	if err := pool.QueryRow(ctx, `SELECT node.id,node.dispatch_no,job.id
		FROM workflow.node_run node
		JOIN workflow.river_job job ON job.kind=$2
		 AND job.args->>'node_run_id'=node.id::text
		 AND (job.args->>'dispatch_no')::integer=node.dispatch_no
		WHERE node.run_id=$1`, string(runID), riveradapter.NodeJobKind).Scan(&nodeID, &dispatchNo, &jobID); err != nil {
		t.Fatal(err)
	}
	args, err := riveradapter.NewNodeJobArgs(nodeID, dispatchNo)
	if err != nil {
		t.Fatal(err)
	}
	return &river.Job[riveradapter.NodeJobArgs]{JobRow: &rivertype.JobRow{ID: jobID, Attempt: 1}, Args: args}
}

func waitForHealthRiverScan(ctx context.Context, pool *pgxpool.Pool, scanID foundation.ID, scanWant domain.ScanStatus, runWant workflowdomain.RunStatus) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		var scanStatus domain.ScanStatus
		var runStatus workflowdomain.RunStatus
		var errorCode *string
		err := pool.QueryRow(ctx, `SELECT scan.status,run.status,node.error_code
			FROM ops.health_scan scan
			JOIN workflow.run run ON run.id=scan.workflow_run_id
			JOIN workflow.node_run node ON node.run_id=run.id
			WHERE scan.id=$1`, string(scanID)).Scan(&scanStatus, &runStatus, &errorCode)
		if err != nil {
			return err
		}
		if scanStatus == scanWant && runStatus == runWant {
			return nil
		}
		if workflowdomain.IsTerminalRunStatus(runStatus) && runStatus != runWant {
			return fmt.Errorf("scan=%s run=%s error_code=%v want_scan=%s want_run=%s", scanStatus, runStatus, errorCode, scanWant, runWant)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("scan=%s run=%s want_scan=%s want_run=%s: %w", scanStatus, runStatus, scanWant, runWant, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForHealthRiverRetryDue(ctx context.Context, pool *pgxpool.Pool, runID foundation.ID) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		var due bool
		if err := pool.QueryRow(ctx, `SELECT COALESCE(next_attempt_at<=CURRENT_TIMESTAMP,false)
			FROM workflow.node_run WHERE run_id=$1`, string(runID)).Scan(&due); err != nil {
			return err
		}
		if due {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("health scan retry did not become due: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func assertHealthRiverSuccess(t *testing.T, ctx context.Context, pool *pgxpool.Pool, scanID, workspaceID foundation.ID, wantIssues int, wantProcessed int64) {
	t.Helper()
	var issues int
	var output []byte
	var processed, created int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.health_issue WHERE workspace_id=$1`, string(workspaceID)).Scan(&issues); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT processed_count,created_count FROM ops.health_scan WHERE id=$1`, string(scanID)).Scan(&processed, &created); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT node.output FROM workflow.node_run node JOIN ops.health_scan scan ON scan.workflow_run_id=node.run_id WHERE scan.id=$1`, string(scanID)).Scan(&output); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		ScanID         foundation.ID     `json:"scan_id"`
		Status         domain.ScanStatus `json:"status"`
		ProcessedCount int64             `json:"processed_count"`
		CreatedCount   int64             `json:"created_count"`
	}
	if err := json.Unmarshal(output, &decoded); err != nil {
		t.Fatal(err)
	}
	if issues != wantIssues || processed != wantProcessed || created != int64(wantIssues) || decoded.ScanID != scanID || decoded.Status != domain.ScanStatusSucceeded || decoded.ProcessedCount != wantProcessed || decoded.CreatedCount != int64(wantIssues) {
		t.Fatalf("issues=%d processed=%d created=%d output=%+v", issues, processed, created, decoded)
	}
	assertHealthRiverCompletionEvent(t, ctx, pool, scanID, workspaceID, "succeeded")
}

func assertHealthRiverCompletionEvent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, scanID, workspaceID foundation.ID, wantStatus string) {
	t.Helper()
	var count int
	var status, eventRunID, scanRunID string
	var eventVersion, scanVersion int64
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM ops.server_event WHERE workspace_id=$1 AND resource_ref=$2),
		event.payload_summary->>'status',event.workflow_run_id::text,scan.workflow_run_id::text,event.resource_version,scan.version
		FROM ops.health_scan scan JOIN ops.server_event event
		  ON event.workspace_id=scan.workspace_id AND event.resource_ref='health_scan:' || scan.id::text
		WHERE scan.id=$3 AND scan.workspace_id=$1`, string(workspaceID), "health_scan:"+string(scanID), string(scanID)).Scan(
		&count, &status, &eventRunID, &scanRunID, &eventVersion, &scanVersion,
	); err != nil {
		t.Fatal(err)
	}
	if count != 1 || status != wantStatus || eventRunID != scanRunID || eventVersion != scanVersion {
		t.Fatalf("health completion event count=%d status=%q event_run=%q scan_run=%q event_version=%d scan_version=%d", count, status, eventRunID, scanRunID, eventVersion, scanVersion)
	}
}

const (
	healthRiverDetectorID      = "health.detector.river_fixture"
	healthRiverDetectorVersion = "detector/v1"
)

type healthRiverDetector struct {
	descriptor        healthapp.Descriptor
	target            foundation.ID
	remainingFailures int
	alwaysFail        bool
	block             bool
	twoPages          bool
	entered           chan struct{}
	once              sync.Once
	mu                sync.Mutex
	cursors           []string
}

func newHealthRiverDetector(target foundation.ID) *healthRiverDetector {
	return &healthRiverDetector{
		target: target,
		descriptor: healthapp.Descriptor{
			ID: healthRiverDetectorID, Version: healthRiverDetectorVersion, IssueType: domain.IssueTypeOrphan,
			SupportedScopes: []domain.ScanScopeType{domain.ScanScopeTypeWorkspace}, SupportedTarget: []domain.ObjectType{domain.ObjectTypeTopic}, DefaultSeverity: domain.SeverityMedium,
		},
	}
}

func (detector *healthRiverDetector) Descriptor() healthapp.Descriptor { return detector.descriptor }

func (detector *healthRiverDetector) ScanPage(ctx context.Context, request healthapp.PageRequest) (healthapp.Page, error) {
	detector.mu.Lock()
	detector.cursors = append(detector.cursors, request.Cursor)
	block := detector.block
	twoPages := detector.twoPages
	if twoPages && request.Cursor == "" {
		detector.mu.Unlock()
		return healthapp.Page{Findings: []healthapp.FindingFact{detector.finding()}, NextCursor: "page-2", Processed: 1}, nil
	}
	fail := detector.alwaysFail || detector.remainingFailures > 0
	if detector.remainingFailures > 0 {
		detector.remainingFailures--
	}
	detector.mu.Unlock()
	if block {
		detector.once.Do(func() { close(detector.entered) })
		<-ctx.Done()
		return healthapp.Page{}, ctx.Err()
	}
	if fail {
		return healthapp.Page{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "HEALTH_DETECTOR_SOURCE_TIMEOUT", true, errors.New("injected health detector source timeout"))
	}
	if twoPages {
		return healthapp.Page{Complete: true}, nil
	}
	return healthapp.Page{Findings: []healthapp.FindingFact{detector.finding()}, Complete: true, Processed: 1}, nil
}

func (detector *healthRiverDetector) finding() healthapp.FindingFact {
	return healthapp.FindingFact{Target: domain.ObjectRef{Type: domain.ObjectTypeTopic, ID: detector.target}, TargetVersion: 1, Summary: "topic requires a durable health review"}
}

func (detector *healthRiverDetector) callCount() int {
	detector.mu.Lock()
	defer detector.mu.Unlock()
	return len(detector.cursors)
}

func (detector *healthRiverDetector) cursorsSeen() []string {
	detector.mu.Lock()
	defer detector.mu.Unlock()
	return append([]string(nil), detector.cursors...)
}

type failFirstHealthCompletion struct {
	delegate *workflowapp.RuntimeCoordinator
	mu       sync.Mutex
	calls    int
	first    json.RawMessage
	second   json.RawMessage
}

func (coordinator *failFirstHealthCompletion) Claim(ctx context.Context, command workflowapp.ClaimCommand) (workflowapp.ClaimResult, error) {
	return coordinator.delegate.Claim(ctx, command)
}

func (coordinator *failFirstHealthCompletion) Heartbeat(ctx context.Context, command workflowapp.HeartbeatCommand) (workflowapp.HeartbeatResult, error) {
	return coordinator.delegate.Heartbeat(ctx, command)
}

func (coordinator *failFirstHealthCompletion) Complete(ctx context.Context, command workflowapp.CompleteDeliveryCommand) (workflowapp.DeliveryTransitionResult, error) {
	coordinator.mu.Lock()
	coordinator.calls++
	if coordinator.calls == 1 {
		coordinator.first = append(json.RawMessage(nil), command.Output...)
		coordinator.mu.Unlock()
		return workflowapp.DeliveryTransitionResult{}, foundation.NewError(foundation.ErrorRetryableFailure, "HEALTH_WORKFLOW_COMPLETION_RESPONSE_LOST", true, errors.New("injected health workflow completion response loss"))
	}
	coordinator.second = append(json.RawMessage(nil), command.Output...)
	coordinator.mu.Unlock()
	return coordinator.delegate.Complete(ctx, command)
}

func (coordinator *failFirstHealthCompletion) Fail(ctx context.Context, command workflowapp.FailDeliveryCommand) (workflowapp.DeliveryTransitionResult, error) {
	return coordinator.delegate.Fail(ctx, command)
}

func (coordinator *failFirstHealthCompletion) outputsMatch() bool {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return coordinator.calls == 2 && string(coordinator.first) == string(coordinator.second)
}

var _ riveradapter.RuntimeExecutionCoordinator = (*failFirstHealthCompletion)(nil)
