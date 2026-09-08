//go:build integration && testcontainers

package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
)

type healthGORMAllowEnqueueFence struct{}

type healthGORMRecordingScopedVerifier struct {
	calls   int
	scope   foundation.TransactionScope
	binding healthapp.SmartCollectionBinding
}

func (healthGORMAllowEnqueueFence) CheckEnqueue(context.Context, foundation.TransactionScope) error {
	return nil
}

// TestGORMHealthAdaptersUseOneRealPostgresPool exercises the Health
// adapters against a migrated Testcontainers database. The same platform Pool
// supplies pgx seed queries, GORM transactions, scoped Events and River SQL.
func TestGORMHealthAdaptersUseOneRealPostgresPool(t *testing.T) {
	platform := requireHealthIntegrationPlatform(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	ids := foundation.NewUUIDGenerator(nil)
	workspaceID, err := ids.New()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	rootPath := "/tmp/health-gorm-" + string(workspaceID)
	if _, err := platform.DB().Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
		VALUES($1,'health-gorm-closed-loop',$2,$2,$3,'inactive',1,$3,$3)`, string(workspaceID), rootPath, now); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupHealthIntegrationWorkspace(t, platform.DB(), workspaceID) })

	events, err := eventspostgres.NewGORMStore(platform)
	if err != nil {
		t.Fatalf("construct GORM event store: %v", err)
	}
	runtime, err := workflowpostgres.NewGORMRuntimeRepositoryWithHooks(
		platform,
		riveradapter.DefaultOptions(),
		healthGORMAllowEnqueueFence{},
		workflowpostgres.GORMRuntimeRepositoryHooks{},
	)
	if err != nil {
		t.Fatalf("construct GORM workflow runtime: %v", err)
	}
	scanRepository, err := NewGORMScanRepository(platform, runtime, events, ids, foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatalf("construct GORM health scan repository: %v", err)
	}
	scanService, err := healthapp.NewScanService(scanRepository, scanRepository)
	if err != nil {
		t.Fatal(err)
	}
	start, err := scanService.Start(ctx, healthapp.ScanStartCommand{
		WorkspaceID: workspaceID,
		Scope:       domain.ScanScope{Type: domain.ScanScopeTypeWorkspace, Ref: workspaceID, Version: 1, SchemaVersion: "health-scope/workspace/v1"},
		Coverage:    []domain.DetectorCoverage{{DetectorID: "health.detector.orphan", DetectorVersion: "detector/v1", Status: domain.DetectorCoverageStatusPending}},
		MaxItems:    100, IdempotencyKey: "health-gorm-start-1",
	})
	if err != nil {
		t.Fatalf("GORM scan start: %v", err)
	}
	if start.Replayed || start.Scan.Status != domain.ScanStatusPending || start.Scan.WorkflowRunID == "" {
		t.Fatalf("unexpected GORM scan result: %#v", start)
	}
	loadedScan, err := scanRepository.Get(ctx, workspaceID, start.Scan.ID)
	if err != nil || loadedScan.ID != start.Scan.ID {
		t.Fatalf("GORM scan read: scan=%#v err=%v", loadedScan, err)
	}
	watermark, err := events.CurrentWatermark(ctx, workspaceID)
	if err != nil || watermark < 1 {
		t.Fatalf("GORM event watermark=%d err=%v", watermark, err)
	}
	var riverJobs int
	if err := platform.DB().QueryRow(ctx, `SELECT count(*)
		FROM workflow.river_job job
		WHERE job.args->>'node_run_id' IN (
			SELECT id::text FROM workflow.node_run WHERE run_id=$1
		)`, string(start.Scan.WorkflowRunID)).Scan(&riverJobs); err != nil {
		t.Fatalf("River SQL visibility through shared pool: %v", err)
	}
	if riverJobs != 1 {
		var totalJobs, nodeRows int
		_ = platform.DB().QueryRow(ctx, `SELECT count(*) FROM workflow.river_job`).Scan(&totalJobs)
		_ = platform.DB().QueryRow(ctx, `SELECT count(*) FROM workflow.node_run WHERE run_id=$1`, string(start.Scan.WorkflowRunID)).Scan(&nodeRows)
		t.Fatalf("River jobs=%d want=1 (total=%d nodes=%d run=%s)", riverJobs, totalJobs, nodeRows, start.Scan.WorkflowRunID)
	}

	scheduleRepository, err := NewGORMScheduleRepository(platform, ids, time.Minute)
	if err != nil {
		t.Fatalf("construct GORM schedule repository: %v", err)
	}
	dueAt := now.Add(-time.Minute)
	schedule, err := scheduleRepository.Create(ctx, healthapp.ScheduleCreateCommand{
		WorkspaceID: workspaceID,
		Scope:       domain.ScanScope{Type: domain.ScanScopeTypeWorkspace, Ref: workspaceID, Version: 1, SchemaVersion: "health-scope/workspace/v1"},
		Cadence:     domain.ScheduleCadenceDaily, Timezone: "UTC", MaxItems: 100, NextRunAt: &dueAt,
		IdempotencyKey: "health-gorm-schedule-1",
	})
	if err != nil {
		t.Fatalf("GORM schedule create: %v", err)
	}
	loadedSchedule, err := scheduleRepository.Get(ctx, workspaceID, schedule.ID)
	if err != nil || loadedSchedule.ID != schedule.ID || loadedSchedule.NextRunAt == nil {
		t.Fatalf("GORM schedule read: schedule=%#v err=%v", loadedSchedule, err)
	}

	issueWorkspaceID, err := ids.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupHealthIntegrationWorkspace(t, platform.DB(), issueWorkspaceID) })
	topicID, definitionID, runID, issueScanID := newHealthGORMIDs(t, ids)
	seedTx, err := platform.DB().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = seedTx.Rollback(context.Background()) }()
	if _, err := platform.DB().Exec(ctx, `DELETE FROM core.workspace WHERE id=$1`, string(issueWorkspaceID)); err != nil {
		t.Fatalf("prepare GORM issue workspace %s: %v", issueWorkspaceID, err)
	}
	seedIssueRepositoryFacts(t, ctx, seedTx, issueWorkspaceID, topicID, definitionID, runID, issueScanID, now)
	if err := seedTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	issueID, err := ids.New()
	if err != nil {
		t.Fatal(err)
	}
	issueRepository, err := NewGORMIssueRepository(platform, ids)
	if err != nil {
		t.Fatalf("construct GORM issue repository: %v", err)
	}
	createdIssue, outcome, err := issueRepository.UpsertObservation(ctx, issueWorkspaceID, issueScanID, issueID, repositoryObservation(topicID, strings.Repeat("a", 64), "GORM issue observation"), nil, now)
	if err != nil || outcome == domain.ObservationOutcomeUnchanged || createdIssue.ID != issueID {
		t.Fatalf("GORM issue write: issue=%#v outcome=%s err=%v", createdIssue, outcome, err)
	}
	readRepository, err := NewGORMReadRepository(platform)
	if err != nil {
		t.Fatalf("construct GORM read repository: %v", err)
	}
	snapshot, err := readRepository.GetIssue(ctx, issueWorkspaceID, issueID)
	if err != nil || snapshot.Issue.ID != issueID || snapshot.LatestObservation.ID == "" {
		t.Fatalf("GORM issue read: snapshot=%#v err=%v", snapshot, err)
	}
}

// TestGORMScanRepositoryPassesSmartBindingThroughScopedVerifier proves that
// Health callback passes the active caller-owned GORM scope to Collection.
func TestGORMScanRepositoryPassesSmartBindingThroughScopedVerifier(t *testing.T) {
	platform := requireHealthIntegrationPlatform(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	ids := foundation.NewUUIDGenerator(nil)
	workspaceID, err := ids.New()
	if err != nil {
		t.Fatal(err)
	}
	collectionID, err := ids.New()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	events, err := eventspostgres.NewGORMStore(platform)
	if err != nil {
		t.Fatalf("construct GORM event store: %v", err)
	}
	runtime, err := workflowpostgres.NewGORMRuntimeRepositoryWithHooks(
		platform,
		riveradapter.DefaultOptions(),
		healthGORMAllowEnqueueFence{},
		workflowpostgres.GORMRuntimeRepositoryHooks{},
	)
	if err != nil {
		t.Fatalf("construct GORM workflow runtime: %v", err)
	}
	verifier := &healthGORMRecordingScopedVerifier{}
	repository, err := NewGORMScanRepository(platform, runtime, events, verifier, ids, foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatalf("construct scoped GORM health scan repository: %v", err)
	}
	binding := healthapp.SmartCollectionBinding{
		WorkspaceID: workspaceID, CollectionID: collectionID, CollectionVersion: 1,
		QueryHash: strings.Repeat("a", 64), ReadModelRevision: strings.Repeat("b", 64), ExactCount: 0,
	}
	if err := repository.database.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, transaction healthTransaction) error {
		return repository.core.verifier.VerifyBindingScoped(ctx, transaction.scope, binding)
	}); err != nil {
		t.Fatalf("verify and commit scoped Collection binding: %v", err)
	}
	if verifier.calls != 1 || verifier.scope == nil || verifier.binding != binding {
		t.Fatalf("scoped verifier calls=%d scope=%T binding=%+v want=%+v", verifier.calls, verifier.scope, verifier.binding, binding)
	}
}

func (verifier *healthGORMRecordingScopedVerifier) VerifyBindingScoped(ctx context.Context, scope foundation.TransactionScope, binding healthapp.SmartCollectionBinding) error {
	database, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return err
	}
	if err := database.WithContext(ctx).Exec("SELECT 1").Error; err != nil {
		return err
	}
	verifier.calls++
	verifier.scope = scope
	verifier.binding = binding
	return nil
}

func newHealthGORMIDs(t *testing.T, generator foundation.IDGenerator) (foundation.ID, foundation.ID, foundation.ID, foundation.ID) {
	t.Helper()
	values := make([]foundation.ID, 4)
	for index := range values {
		value, err := generator.New()
		if err != nil {
			t.Fatal(err)
		}
		values[index] = value
	}
	return values[0], values[1], values[2], values[3]
}

var _ riveradapter.ScopedEnqueueFence = healthGORMAllowEnqueueFence{}
