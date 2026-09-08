//go:build integration && testcontainers

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestHealthScanRepositoryRuntimeReplayAdvanceFinishAndCancellation(t *testing.T) {
	ctx := context.Background()
	platform := requireHealthIntegrationPlatform(t)
	pool := platform.DB()
	ids := foundation.NewUUIDGenerator(nil)
	workspaceID, err := ids.New()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	rootPath := "/tmp/health-scan-" + string(workspaceID)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
VALUES($1,'health-scan-runtime',$2,$2,$3,'inactive',1,$3,$3)`, string(workspaceID), rootPath, now); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupHealthIntegrationWorkspace(t, pool, workspaceID) })
	runtime, err := workflowpostgres.NewGORMRuntimeRepositoryWithHooks(platform, riveradapter.DefaultOptions(), healthGORMAllowEnqueueFence{}, workflowpostgres.GORMRuntimeRepositoryHooks{})
	if err != nil {
		t.Fatal(err)
	}
	events, err := eventspostgres.NewGORMStore(platform)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewGORMScanRepository(platform, runtime, events, ids, foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatal(err)
	}
	unitOfWork, err := platform.UnitOfWork()
	if err != nil {
		t.Fatal(err)
	}
	service, err := healthapp.NewScanService(repository, repository)
	if err != nil {
		t.Fatal(err)
	}
	command := healthapp.ScanStartCommand{
		WorkspaceID: workspaceID,
		Scope:       domain.ScanScope{Type: domain.ScanScopeTypeWorkspace, Ref: workspaceID, Version: 1, SchemaVersion: "health-scope/workspace/v1"},
		Coverage:    []domain.DetectorCoverage{{DetectorID: "health.detector.orphan", DetectorVersion: "detector/v1", Status: domain.DetectorCoverageStatusPending}},
		MaxItems:    100, IdempotencyKey: "health-runtime-1",
	}
	started, err := service.Start(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if started.Replayed || started.Scan.Status != domain.ScanStatusPending || started.Scan.WorkflowRunID == "" {
		t.Fatalf("started=%#v", started)
	}
	running, err := service.Advance(ctx, healthapp.ScanProgress{
		ScanID: started.Scan.ID, WorkspaceID: workspaceID, ExpectedVersion: started.Scan.Version,
		DetectorID: "health.detector.orphan", Status: domain.DetectorCoverageStatusRunning,
		Checkpoint: domain.ScanCheckpoint{Page: 1, Cursor: "page-1"}, CountersDelta: domain.ScanCounters{Processed: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := service.Start(ctx, command)
	if err != nil || !replayed.Replayed || replayed.Scan.ID != started.Scan.ID || replayed.Scan.Version != running.Version {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
	covered, err := service.Advance(ctx, healthapp.ScanProgress{
		ScanID: running.ID, WorkspaceID: workspaceID, ExpectedVersion: running.Version,
		DetectorID: "health.detector.orphan", Status: domain.DetectorCoverageStatusSucceeded,
		Checkpoint: running.Checkpoint,
	})
	if err != nil {
		t.Fatal(err)
	}
	finished, err := service.Finish(ctx, healthapp.ScanTerminal{ScanID: covered.ID, WorkspaceID: workspaceID, ExpectedVersion: covered.Version, Status: domain.ScanStatusSucceeded})
	if err != nil || finished.Status != domain.ScanStatusSucceeded || finished.CompletedAt == nil {
		t.Fatalf("finished=%#v err=%v", finished, err)
	}
	loaded, err := service.Get(ctx, workspaceID, finished.ID)
	if err != nil || loaded.Counters.Processed != 4 || loaded.Version != finished.Version {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
	exactReplay, err := service.Finish(ctx, healthapp.ScanTerminal{ScanID: covered.ID, WorkspaceID: workspaceID, ExpectedVersion: covered.Version, Status: domain.ScanStatusSucceeded})
	if err != nil || exactReplay.ID != finished.ID || exactReplay.Version != finished.Version {
		t.Fatalf("terminal replay=%#v err=%v", exactReplay, err)
	}
	assertHealthScanCompletionEvent(t, ctx, pool, finished, "succeeded")

	command.IdempotencyKey = "health-runtime-cancel"
	cancellable, err := service.Start(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	var nodeIDText string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM workflow.node_run WHERE run_id=$1`, string(cancellable.Scan.WorkflowRunID)).Scan(&nodeIDText); err != nil {
		t.Fatal(err)
	}
	nodeID, err := foundation.ParseID(nodeIDText)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := NewGORMScanCancellationGuard(platform, events)
	if err != nil {
		t.Fatal(err)
	}
	var safe bool
	err = unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		var err error
		safe, err = guard.SafeToCancelWorkflowNodeScoped(ctx, scope, nodeID)
		return err
	})
	if err != nil || !safe {
		t.Fatalf("safe=%v err=%v", safe, err)
	}
	err = unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		var err error
		safe, err = guard.SafeToCancelWorkflowNodeScoped(ctx, scope, nodeID)
		return err
	})
	if err != nil || !safe {
		t.Fatalf("cancel replay safe=%v err=%v", safe, err)
	}
	cancelled, err := service.Get(ctx, workspaceID, cancellable.Scan.ID)
	if err != nil || cancelled.Status != domain.ScanStatusCancelled || cancelled.Coverage[0].Status != domain.DetectorCoverageStatusCancelled {
		t.Fatalf("cancelled=%#v err=%v", cancelled, err)
	}
	assertHealthScanCompletionEvent(t, ctx, pool, cancelled, "cancelled")

	command.IdempotencyKey = "health-runtime-event-rollback"
	rollbackScan, err := service.Start(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected health completion event append failure")
	repository.core.events = &healthScanEventAppenderFake{err: injected}
	failure := &domain.FailureSummary{Stage: "detector", Code: "HEALTH_DETECTOR_FAILED"}
	if _, err := service.Finish(ctx, healthapp.ScanTerminal{ScanID: rollbackScan.Scan.ID, WorkspaceID: workspaceID, ExpectedVersion: rollbackScan.Scan.Version, Status: domain.ScanStatusFailed, LastError: failure}); !errors.Is(err, injected) {
		t.Fatalf("finish append failure=%v", err)
	}
	repository.core.events = events
	rolledBack, err := service.Get(ctx, workspaceID, rollbackScan.Scan.ID)
	if err != nil || rolledBack.Status != domain.ScanStatusPending || rolledBack.Version != rollbackScan.Scan.Version || rolledBack.CompletedAt != nil {
		t.Fatalf("rolled back scan=%#v err=%v", rolledBack, err)
	}
	var rolledBackEvents int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.server_event WHERE workspace_id=$1 AND resource_ref=$2`, string(workspaceID), "health_scan:"+string(rollbackScan.Scan.ID)).Scan(&rolledBackEvents); err != nil || rolledBackEvents != 0 {
		t.Fatalf("rolled back completion events=%d err=%v", rolledBackEvents, err)
	}
	failed, err := service.Finish(ctx, healthapp.ScanTerminal{ScanID: rollbackScan.Scan.ID, WorkspaceID: workspaceID, ExpectedVersion: rollbackScan.Scan.Version, Status: domain.ScanStatusFailed, LastError: failure})
	if err != nil {
		t.Fatal(err)
	}
	assertHealthScanCompletionEvent(t, ctx, pool, failed, "failed")

	command.IdempotencyKey = "health-runtime-cancel-event-rollback"
	cancelRollbackScan, err := service.Start(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT id::text FROM workflow.node_run WHERE run_id=$1`, string(cancelRollbackScan.Scan.WorkflowRunID)).Scan(&nodeIDText); err != nil {
		t.Fatal(err)
	}
	nodeID, err = foundation.ParseID(nodeIDText)
	if err != nil {
		t.Fatal(err)
	}
	failingGuard, err := NewGORMScanCancellationGuard(platform, &healthScanEventAppenderFake{err: injected})
	if err != nil {
		t.Fatal(err)
	}
	err = unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		var err error
		safe, err = failingGuard.SafeToCancelWorkflowNodeScoped(ctx, scope, nodeID)
		return err
	})
	if safe || !errors.Is(err, injected) {
		t.Fatalf("cancel append failure safe=%v err=%v", safe, err)
	}
	cancelRolledBack, err := service.Get(ctx, workspaceID, cancelRollbackScan.Scan.ID)
	if err != nil || cancelRolledBack.Status != domain.ScanStatusPending || cancelRolledBack.Version != cancelRollbackScan.Scan.Version {
		t.Fatalf("cancel rolled back scan=%#v err=%v", cancelRolledBack, err)
	}
	err = unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		var err error
		safe, err = guard.SafeToCancelWorkflowNodeScoped(ctx, scope, nodeID)
		return err
	})
	if err != nil || !safe {
		t.Fatalf("cancel retry safe=%v err=%v", safe, err)
	}
	cancelCommitted, err := service.Get(ctx, workspaceID, cancelRollbackScan.Scan.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertHealthScanCompletionEvent(t, ctx, pool, cancelCommitted, "cancelled")
}

func assertHealthScanCompletionEvent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, scan domain.Scan, wantStatus string) {
	t.Helper()
	var (
		count                         int
		eventType, resourceRef        string
		workflowRunID, sourceEventRef string
		payloadRaw                    []byte
		resourceVersion               int64
		occurredAt, expiresAt         time.Time
	)
	if err := pool.QueryRow(ctx, `SELECT count(*) OVER(),event_type,resource_ref,resource_version,workflow_run_id::text,
		source_event_ref,payload_summary,occurred_at,expires_at
		FROM ops.server_event WHERE workspace_id=$1 AND resource_ref=$2 ORDER BY seq`, string(scan.WorkspaceID), "health_scan:"+string(scan.ID)).Scan(
		&count, &eventType, &resourceRef, &resourceVersion, &workflowRunID, &sourceEventRef, &payloadRaw, &occurredAt, &expiresAt,
	); err != nil {
		t.Fatal(err)
	}
	wantSourceEventRef := "health.scan.completed:" + string(scan.ID) + ":v" + strconv.FormatInt(scan.Version, 10)
	if count != 1 || eventType != "health.scan.completed" || resourceRef != "health_scan:"+string(scan.ID) || resourceVersion != scan.Version || workflowRunID != string(scan.WorkflowRunID) || sourceEventRef != wantSourceEventRef {
		t.Fatalf("completion event count=%d type=%q resource=%q version=%d workflow=%q source=%q", count, eventType, resourceRef, resourceVersion, workflowRunID, sourceEventRef)
	}
	if scan.CompletedAt == nil || !occurredAt.Equal(*scan.CompletedAt) || !expiresAt.Equal(occurredAt.Add(24*time.Hour)) {
		t.Fatalf("completion event time occurred=%v expires=%v scan=%v", occurredAt, expiresAt, scan.CompletedAt)
	}
	var payload map[string]any
	if err := json.Unmarshal(payloadRaw, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload) != 1 || payload["status"] != wantStatus {
		t.Fatalf("completion payload=%s", payloadRaw)
	}
	for _, forbidden := range []string{"scope", "query", "evidence", "summary", "text"} {
		if strings.Contains(strings.ToLower(string(payloadRaw)), forbidden) {
			t.Fatalf("completion payload leaked %q: %s", forbidden, payloadRaw)
		}
	}
}

func TestHealthScanRepositoryPreventsConcurrentScopeAcrossIdempotencyKeys(t *testing.T) {
	ctx := context.Background()
	platform := requireHealthIntegrationPlatform(t)
	pool := platform.DB()
	workspaceID, _ := seedHealthRiverFixture(t, ctx, pool)

	runtime, err := workflowpostgres.NewGORMRuntimeRepositoryWithHooks(platform, riveradapter.DefaultOptions(), healthGORMAllowEnqueueFence{}, workflowpostgres.GORMRuntimeRepositoryHooks{})
	if err != nil {
		t.Fatal(err)
	}
	events, err := eventspostgres.NewGORMStore(platform)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewGORMScanRepository(platform, runtime, events, foundation.NewUUIDGenerator(nil), foundation.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := healthapp.NewScanService(repository, repository)
	if err != nil {
		t.Fatal(err)
	}

	command := healthRiverStartCommand(workspaceID, "health-scope-lock-first")
	command.PreventScopeConcurrency = true
	first, err := service.Start(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	command.IdempotencyKey = "health-scope-lock-second"
	if _, err := service.Start(ctx, command); err == nil {
		t.Fatal("expected active scope conflict")
	} else {
		var classified *foundation.Error
		if !errors.As(err, &classified) || classified.Kind != foundation.ErrorVersionConflict || classified.Code != domain.ErrorCodeScanTransitionInvalid {
			t.Fatalf("scope conflict=%v classified=%#v", err, classified)
		}
	}

	var scans, runs, nodes, jobs int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM ops.health_scan WHERE workspace_id=$1),
		(SELECT count(*) FROM workflow.run WHERE workspace_id=$1),
		(SELECT count(*) FROM workflow.node_run node JOIN workflow.run run ON run.id=node.run_id WHERE run.workspace_id=$1),
		(SELECT count(*) FROM workflow.river_job job JOIN workflow.node_run node ON job.args->>'node_run_id'=node.id::text
			JOIN workflow.run run ON run.id=node.run_id WHERE run.workspace_id=$1)`, string(workspaceID)).Scan(&scans, &runs, &nodes, &jobs); err != nil {
		t.Fatal(err)
	}
	if scans != 1 || runs != 1 || nodes != 1 || jobs != 1 {
		t.Fatalf("scan=%d run=%d node=%d river_job=%d first=%#v", scans, runs, nodes, jobs, first)
	}
}

func TestHealthScanFinishRecoversCommitResponseLossWithoutDuplicatingCompletionEvent(t *testing.T) {
	ctx := context.Background()
	platform := requireHealthIntegrationPlatform(t)
	pool := platform.DB()
	workspaceID, _ := seedHealthRiverFixture(t, ctx, pool)
	runtime, err := workflowpostgres.NewGORMRuntimeRepositoryWithHooks(platform, riveradapter.DefaultOptions(), healthGORMAllowEnqueueFence{}, workflowpostgres.GORMRuntimeRepositoryHooks{})
	if err != nil {
		t.Fatal(err)
	}
	events, err := eventspostgres.NewGORMStore(platform)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewGORMScanRepository(platform, runtime, events, foundation.NewUUIDGenerator(nil), foundation.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := healthapp.NewScanService(repository, repository)
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.Start(ctx, healthRiverStartCommand(workspaceID, "health-finish-commit-response-loss"))
	if err != nil {
		t.Fatal(err)
	}
	running, err := service.Advance(ctx, healthapp.ScanProgress{
		ScanID: started.Scan.ID, WorkspaceID: workspaceID, ExpectedVersion: started.Scan.Version,
		DetectorID: healthRiverDetectorID, Status: domain.DetectorCoverageStatusRunning,
	})
	if err != nil {
		t.Fatal(err)
	}
	covered, err := service.Advance(ctx, healthapp.ScanProgress{
		ScanID: running.ID, WorkspaceID: workspaceID, ExpectedVersion: running.Version,
		DetectorID: healthRiverDetectorID, Status: domain.DetectorCoverageStatusSucceeded,
	})
	if err != nil {
		t.Fatal(err)
	}
	lossDB := &healthCommitLossUnitOfWork{UnitOfWork: repository.database.unitOfWork}
	repository.database.unitOfWork = lossDB
	terminal := healthapp.ScanTerminal{ScanID: covered.ID, WorkspaceID: workspaceID, ExpectedVersion: covered.Version, Status: domain.ScanStatusSucceeded}
	finished, err := service.Finish(ctx, terminal)
	if err != nil || !lossDB.lost.Load() || finished.Status != domain.ScanStatusSucceeded {
		t.Fatalf("finished=%#v lost=%v err=%v", finished, lossDB.lost.Load(), err)
	}
	replayed, err := service.Finish(ctx, terminal)
	if err != nil || replayed.ID != finished.ID || replayed.Version != finished.Version {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
	assertHealthScanCompletionEvent(t, ctx, pool, finished, "succeeded")
}

type healthCommitLossUnitOfWork struct {
	foundation.UnitOfWork
	lost atomic.Bool
}

func (database *healthCommitLossUnitOfWork) Within(ctx context.Context, options foundation.TransactionOptions, work foundation.TransactionFunc) error {
	if err := database.UnitOfWork.Within(ctx, options, work); err != nil {
		return err
	}
	if database.lost.CompareAndSwap(false, true) {
		return errors.New("injected health commit response loss")
	}
	return nil
}
