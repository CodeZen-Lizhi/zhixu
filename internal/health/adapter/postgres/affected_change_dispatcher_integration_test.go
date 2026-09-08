//go:build integration && testcontainers

package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAffectedChangeDispatcherConcurrentClaimCreatesOneRuntime(t *testing.T) {
	ctx, platform, workspaceID := newAffectedChangeTestWorkspace(t, "concurrent")
	pool := platform.DB()
	runtime := newAffectedChangeTestRuntime(t, platform)
	repository, _, _ := newAffectedChangeTestRepository(t, platform, runtime, time.Minute)
	insertAffectedChangeReceipt(t, ctx, pool, workspaceID, "concurrent")

	start := make(chan struct{})
	results := make(chan affectedDispatchCall, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			result, found, err := repository.DispatchNext(ctx)
			results <- affectedDispatchCall{result: result, found: found, err: err}
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	found, published := 0, 0
	var result healthapp.AffectedChangeDispatchResult
	for call := range results {
		if call.err != nil {
			t.Fatal(call.err)
		}
		if call.found {
			found++
			result = call.result
		}
		if call.result.Outcome == healthapp.AffectedChangeDispatchPublished {
			published++
		}
	}
	if found != 1 || published != 1 {
		t.Fatalf("found=%d published=%d result=%#v", found, published, result)
	}
	assertAffectedChangeRuntimeCounts(t, ctx, pool, workspaceID, result, 1)
}

func TestAffectedChangeDispatcherRuntimeFailureRollsBackAndRestartPublishes(t *testing.T) {
	ctx, platform, workspaceID := newAffectedChangeTestWorkspace(t, "runtime-failure")
	pool := platform.DB()
	workingRuntime := newAffectedChangeTestRuntime(t, platform)
	failingRepository, _, registry := newAffectedChangeTestRepository(t, platform, affectedChangeFailRuntime{}, time.Minute)
	insertAffectedChangeReceipt(t, ctx, pool, workspaceID, "runtime-failure")
	if _, found, err := failingRepository.DispatchNext(ctx); !found || err == nil {
		t.Fatalf("found=%v err=%v", found, err)
	}
	var attempts, scans int
	if err := pool.QueryRow(ctx, `SELECT attempt_count FROM ops.health_affected_change_outbox WHERE workspace_id=$1`, string(workspaceID)).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.health_scan WHERE workspace_id=$1`, string(workspaceID)).Scan(&scans); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 || scans != 0 {
		t.Fatalf("attempts=%d scans=%d", attempts, scans)
	}

	planner, err := healthapp.NewAffectedChangePlanner(registry)
	if err != nil {
		t.Fatal(err)
	}
	events, err := eventspostgres.NewGORMStore(platform)
	if err != nil {
		t.Fatal(err)
	}
	scanRepository, err := NewGORMScanRepository(platform, workingRuntime, events, foundation.NewUUIDGenerator(nil), foundation.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewGORMAffectedChangeDispatchRepository(scanRepository, planner)
	if err != nil {
		t.Fatal(err)
	}
	result, found, err := restarted.DispatchNext(ctx)
	if err != nil || !found || result.Outcome != healthapp.AffectedChangeDispatchPublished {
		t.Fatalf("result=%#v found=%v err=%v", result, found, err)
	}
	assertAffectedChangeRuntimeCounts(t, ctx, pool, workspaceID, result, 1)
}

func TestAffectedChangeDispatcherDefersActiveWorkspaceScanThenBindsOwnScan(t *testing.T) {
	ctx, platform, workspaceID := newAffectedChangeTestWorkspace(t, "active-scan")
	pool := platform.DB()
	runtime := newAffectedChangeTestRuntime(t, platform)
	repository, scans, registry := newAffectedChangeTestRepository(t, platform, runtime, 5*time.Millisecond)
	service, err := healthapp.NewScanService(scans, scans)
	if err != nil {
		t.Fatal(err)
	}
	scope := domain.ScanScope{Type: domain.ScanScopeTypeWorkspace, Ref: workspaceID, Version: 1, SchemaVersion: "health-scope/workspace/v1"}
	manual, err := service.Start(ctx, healthapp.ScanStartCommand{
		WorkspaceID: workspaceID, Scope: scope,
		Coverage: registry.Coverage(healthapp.Scope{WorkspaceID: workspaceID, Type: scope.Type, Ref: scope.Ref, Version: scope.Version}),
		MaxItems: 100, IdempotencyKey: "manual-active-scan", PreventScopeConcurrency: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	insertAffectedChangeReceipt(t, ctx, pool, workspaceID, "active-scan")
	deferred, found, err := repository.DispatchNext(ctx)
	if err != nil || !found || deferred.Outcome != healthapp.AffectedChangeDispatchDeferred || deferred.ScanID != "" {
		t.Fatalf("deferred=%#v found=%v err=%v", deferred, found, err)
	}
	var errorCode string
	var boundScan *string
	if err := pool.QueryRow(ctx, `SELECT last_error_code,bound_scan_id::text
		FROM ops.health_affected_change_outbox WHERE workspace_id=$1`, string(workspaceID)).Scan(&errorCode, &boundScan); err != nil {
		t.Fatal(err)
	}
	if errorCode != healthapp.ErrorCodeAffectedChangeScanActive || boundScan != nil {
		t.Fatalf("error_code=%q bound_scan=%v", errorCode, boundScan)
	}
	if _, err := service.Finish(ctx, healthapp.ScanTerminal{
		ScanID: manual.Scan.ID, WorkspaceID: workspaceID, ExpectedVersion: manual.Scan.Version, Status: domain.ScanStatusCancelled,
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	published, found, err := repository.DispatchNext(ctx)
	if err != nil || !found || published.Outcome != healthapp.AffectedChangeDispatchPublished || published.ScanID == manual.Scan.ID {
		t.Fatalf("published=%#v found=%v err=%v", published, found, err)
	}
	assertAffectedChangeRuntimeCounts(t, ctx, pool, workspaceID, published, 2)
}

func TestAffectedChangeDispatcherPersistsSchemaAndSourcePoison(t *testing.T) {
	tests := []struct {
		name string
		set  string
		code string
	}{
		{name: "schema", set: "schema_version=2", code: healthapp.ErrorCodeAffectedChangeSchemaUnsupported},
		{name: "source", set: "source_key='missing-source-binding'", code: healthapp.ErrorCodeAffectedChangeSourceBindingInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, platform, workspaceID := newAffectedChangeTestWorkspace(t, "poison-"+test.name)
			pool := platform.DB()
			runtime := newAffectedChangeTestRuntime(t, platform)
			repository, _, _ := newAffectedChangeTestRepository(t, platform, runtime, time.Minute)
			insertAffectedChangeReceipt(t, ctx, pool, workspaceID, "poison-"+test.name)
			connection, err := pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := connection.Exec(ctx, `SET session_replication_role=replica`); err != nil {
				connection.Release()
				t.Fatal(err)
			}
			if _, err := connection.Exec(ctx, `UPDATE ops.health_affected_change_outbox SET `+test.set+` WHERE workspace_id=$1`, string(workspaceID)); err != nil {
				connection.Release()
				t.Fatal(err)
			}
			if _, err := connection.Exec(ctx, `SET session_replication_role=origin`); err != nil {
				connection.Release()
				t.Fatal(err)
			}
			connection.Release()

			result, found, dispatchErr := repository.DispatchNext(ctx)
			var classified *foundation.Error
			if !found || result.Outcome != healthapp.AffectedChangeDispatchPoisoned || !errors.As(dispatchErr, &classified) || classified.Kind != foundation.ErrorManualRecoveryRequired || classified.Code != test.code {
				t.Fatalf("result=%#v found=%v err=%v classified=%#v", result, found, dispatchErr, classified)
			}
			var manual bool
			var poisonedAt *time.Time
			var lastError string
			var publishedAt *time.Time
			if err := pool.QueryRow(ctx, `SELECT manual_recovery_required,poisoned_at,last_error_code,published_at
				FROM ops.health_affected_change_outbox WHERE workspace_id=$1`, string(workspaceID)).Scan(&manual, &poisonedAt, &lastError, &publishedAt); err != nil {
				t.Fatal(err)
			}
			if !manual || poisonedAt == nil || lastError != test.code || publishedAt != nil {
				t.Fatalf("manual=%v poisoned=%v last_error=%q published=%v", manual, poisonedAt, lastError, publishedAt)
			}
		})
	}
}

func TestAffectedChangeDispatcherRecoversCommitResponseLoss(t *testing.T) {
	ctx, platform, workspaceID := newAffectedChangeTestWorkspace(t, "commit-loss")
	pool := platform.DB()
	runtime := newAffectedChangeTestRuntime(t, platform)
	repository, _, _ := newAffectedChangeTestRepository(t, platform, runtime, time.Minute)
	lossDB := &healthCommitLossUnitOfWork{UnitOfWork: repository.database.unitOfWork}
	repository.database.unitOfWork = lossDB
	insertAffectedChangeReceipt(t, ctx, pool, workspaceID, "commit-loss")
	result, found, err := repository.DispatchNext(ctx)
	if err != nil || !found || result.Outcome != healthapp.AffectedChangeDispatchPublished || !lossDB.lost.Load() {
		t.Fatalf("result=%#v found=%v lost=%v err=%v", result, found, lossDB.lost.Load(), err)
	}
	assertAffectedChangeRuntimeCounts(t, ctx, pool, workspaceID, result, 1)
}

func TestAffectedChangeDispatcherPublishesLexicalDegradation(t *testing.T) {
	ctx, platform, workspaceID := newAffectedChangeTestWorkspace(t, "lexical-degradation")
	pool := platform.DB()
	runtime := newAffectedChangeTestRuntime(t, platform)
	repository, _, _ := newAffectedChangeTestRepository(t, platform, runtime, time.Minute)
	indexID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	chunkID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	func() {
		connection, acquireErr := pool.Acquire(ctx)
		if acquireErr != nil {
			t.Fatal(acquireErr)
		}
		defer connection.Release()
		if _, roleErr := connection.Exec(ctx, `SET session_replication_role=replica`); roleErr != nil {
			t.Fatal(roleErr)
		}
		defer func() {
			if _, roleErr := connection.Exec(context.Background(), `SET session_replication_role=origin`); roleErr != nil {
				t.Errorf("restore lexical fixture trigger role: %v", roleErr)
			}
		}()
		if _, insertErr := connection.Exec(ctx, `INSERT INTO retrieval.index_version(
		id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
		source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,
		degraded_capabilities,version,created_at,updated_at)
		VALUES($1,$2,'simple','v1',$3,'{}','lexical-degradation',$4,1,$5,'building','["vector"]',1,$6,$6)`,
			string(indexID), string(workspaceID), strings.Repeat("1", 64), strings.Repeat("2", 64), "lexical-"+string(indexID), now); insertErr != nil {
			t.Fatal(insertErr)
		}
		if _, insertErr := connection.Exec(ctx, `INSERT INTO retrieval.chunk_projection(
		index_version_id,chunk_id,workspace_id,embedding_version_id,search_vector,embedding,
		token_count,lexical_status,vector_status,failure_code,created_at,updated_at)
		VALUES($1,$2,$3,NULL,NULL,NULL,0,'pending','disabled',NULL,clock_timestamp(),clock_timestamp())`,
			string(indexID), string(chunkID), string(workspaceID)); insertErr != nil {
			t.Fatal(insertErr)
		}
	}()
	if _, err := pool.Exec(ctx, `UPDATE retrieval.chunk_projection
		SET lexical_status='failed',failure_code='LEXICAL_BUILD_FAILED',updated_at=clock_timestamp()
		WHERE index_version_id=$1 AND chunk_id=$2`, string(indexID), string(chunkID)); err != nil {
		t.Fatal(err)
	}

	result, found, err := repository.DispatchNext(ctx)
	if err != nil || !found || result.Outcome != healthapp.AffectedChangeDispatchPublished {
		t.Fatalf("result=%#v found=%v err=%v", result, found, err)
	}
	assertAffectedChangeRuntimeCounts(t, ctx, pool, workspaceID, result, 1)
	var eventType string
	if err := pool.QueryRow(ctx, `SELECT event_type FROM ops.health_affected_change_outbox WHERE workspace_id=$1`, string(workspaceID)).Scan(&eventType); err != nil {
		t.Fatal(err)
	}
	if eventType != string(healthapp.AffectedChangeEventLexicalDegraded) {
		t.Fatalf("event_type=%q", eventType)
	}
}

type affectedDispatchCall struct {
	result healthapp.AffectedChangeDispatchResult
	found  bool
	err    error
}

type affectedChangeTestDetector struct{}

func (affectedChangeTestDetector) Descriptor() healthapp.Descriptor {
	return healthapp.Descriptor{
		ID: "health.detector.affected", Version: "detector/v1", IssueType: domain.IssueTypeOrphan,
		SupportedScopes: []domain.ScanScopeType{domain.ScanScopeTypeWorkspace},
		SupportedTarget: []domain.ObjectType{domain.ObjectTypeTopic}, DefaultSeverity: domain.SeverityLow,
	}
}

func (affectedChangeTestDetector) ScanPage(context.Context, healthapp.PageRequest) (healthapp.Page, error) {
	return healthapp.Page{Complete: true}, nil
}

func newAffectedChangeTestWorkspace(t *testing.T, suffix string) (context.Context, *platformpostgres.Pool, foundation.ID) {
	t.Helper()
	ctx := context.Background()
	platform := requireHealthIntegrationPlatform(t)
	pool := platform.DB()
	workspaceID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	rootPath := "/tmp/health-affected-" + suffix + "-" + string(workspaceID)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
		VALUES($1,$2,$3,$3,$4,'inactive',1,$4,$4)`, string(workspaceID), "health-affected-"+suffix, rootPath, now); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupHealthIntegrationWorkspace(t, pool, workspaceID) })
	return ctx, platform, workspaceID
}

func newAffectedChangeTestRuntime(t *testing.T, pool *platformpostgres.Pool) workflowapp.ScopedRuntimeStarter {
	t.Helper()
	runtime, err := workflowpostgres.NewGORMRuntimeRepositoryWithHooks(pool, riveradapter.DefaultOptions(), healthGORMAllowEnqueueFence{}, workflowpostgres.GORMRuntimeRepositoryHooks{})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func newAffectedChangeTestRepository(t *testing.T, pool *platformpostgres.Pool, runtime workflowapp.ScopedRuntimeStarter, backoff time.Duration) (*GORMAffectedChangeDispatchRepository, *GORMScanRepository, *healthapp.Registry) {
	t.Helper()
	registry, err := healthapp.NewRegistry([]healthapp.Detector{affectedChangeTestDetector{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	planner, err := healthapp.NewAffectedChangePlanner(registry)
	if err != nil {
		t.Fatal(err)
	}
	events, err := eventspostgres.NewGORMStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	scans, err := NewGORMScanRepository(pool, runtime, events, foundation.NewUUIDGenerator(nil), foundation.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewGORMAffectedChangeDispatchRepository(scans, planner)
	if err != nil {
		t.Fatal(err)
	}
	repository.core.backoff = backoff
	return repository, scans, registry
}

func insertAffectedChangeReceipt(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID, suffix string) {
	t.Helper()
	aggregateID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO core.topic(
		id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at)
		VALUES($1,$2,$3,$3,'','ACTIVE',1,clock_timestamp(),clock_timestamp())`,
		string(aggregateID), string(workspaceID), "affected "+suffix); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO core.knowledge_command_receipt(
		workspace_id,idempotency_key,request_hash,command_type,aggregate_type,aggregate_id,aggregate_version,created_at)
		VALUES($1,$2,$3,'topic.create','TOPIC',$4,1,clock_timestamp())`,
		string(workspaceID), "health-affected-"+suffix, strings.Repeat("a", 64), string(aggregateID)); err != nil {
		t.Fatal(err)
	}
}

func assertAffectedChangeRuntimeCounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID, result healthapp.AffectedChangeDispatchResult, wantScans int) {
	t.Helper()
	var scans, runs, nodes, jobs, published int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM ops.health_scan WHERE workspace_id=$1),
		(SELECT count(*) FROM workflow.run WHERE workspace_id=$1),
		(SELECT count(*) FROM workflow.node_run node JOIN workflow.run run ON run.id=node.run_id WHERE run.workspace_id=$1),
		(SELECT count(*) FROM workflow.river_job job JOIN workflow.node_run node ON job.args->>'node_run_id'=node.id::text
		 JOIN workflow.run run ON run.id=node.run_id WHERE run.workspace_id=$1),
		(SELECT count(*) FROM ops.health_affected_change_outbox WHERE workspace_id=$1 AND published_at IS NOT NULL)`,
		string(workspaceID)).Scan(&scans, &runs, &nodes, &jobs, &published); err != nil {
		t.Fatal(err)
	}
	if scans != wantScans || runs != wantScans || nodes != wantScans || jobs != wantScans || published != 1 {
		t.Fatalf("scans=%d runs=%d nodes=%d jobs=%d published=%d", scans, runs, nodes, jobs, published)
	}
	if result.Outcome == healthapp.AffectedChangeDispatchPublished {
		var boundScan, boundRun string
		if err := pool.QueryRow(ctx, `SELECT bound_scan_id::text,bound_workflow_run_id::text
			FROM ops.health_affected_change_outbox WHERE workspace_id=$1 AND published_at IS NOT NULL`, string(workspaceID)).Scan(&boundScan, &boundRun); err != nil {
			t.Fatal(err)
		}
		if boundScan != string(result.ScanID) || boundRun != string(result.WorkflowRunID) {
			t.Fatalf("bound scan=%s run=%s result=%#v", boundScan, boundRun, result)
		}
	}
}

type affectedChangeFailRuntime struct{}

func (affectedChangeFailRuntime) StartScoped(context.Context, foundation.TransactionScope, workflowapp.RuntimeStartRequest) (workflowapp.RuntimeStartResult, error) {
	return workflowapp.RuntimeStartResult{}, foundation.NewError(foundation.ErrorRetryableFailure, "HEALTH_AFFECTED_RUNTIME_INJECTED", true, errors.New("injected runtime failure"))
}
