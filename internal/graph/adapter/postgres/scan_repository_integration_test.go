//go:build integration

package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	auditpostgres "github.com/CodeZen-Lizhi/zhixu/internal/audit/adapter/postgres"
	collectionpostgres "github.com/CodeZen-Lizhi/zhixu/internal/collection/adapter/postgres"
	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	collectiondomain "github.com/CodeZen-Lizhi/zhixu/internal/collection/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	modelsettingspostgres "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/adapter/postgres"
	modelcrypto "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/crypto"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestGORMSemanticLinkScanCommitsAndRollsBackWorkflowAndRiver(t *testing.T) {
	databaseFixture := testdb.Require(t, testdb.Config{
		ExternalAdminURL: strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL")),
		Availability:     testdb.FailWhenUnavailable,
		MaxConns:         8,
	})
	platform := databaseFixture.Pool()
	if platform == nil || platform.DB() == nil {
		t.Fatal("Graph Scan fixture did not provide a shared platform pool")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	seedTx, err := platform.DB().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = seedTx.Rollback(context.Background()) }()
	fixture := seedGraphFixture(t, ctx, seedTx)
	if err := seedTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	graph, err := NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := modelcrypto.NewSealer(bytes.Repeat([]byte{0x2a}, 32))
	if err != nil {
		t.Fatal(err)
	}
	audit, err := auditpostgres.NewGORMStore(platform)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := modelsettingspostgres.NewGORMRepository(platform,
		modelsettingspostgres.WithGORMSecretSealer(sealer), modelsettingspostgres.WithGORMAuditAppender(audit))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workflowpostgres.NewGORMRuntimeRepositoryWithHooks(platform, riveradapter.DefaultOptions(), settings,
		workflowpostgres.GORMRuntimeRepositoryHooks{CancellationSafety: graph})
	if err != nil {
		t.Fatal(err)
	}
	collections, err := collectionpostgres.NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	collectionService, err := collectionapp.NewService(collectionapp.Dependencies{
		Repository: collections, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	query := collectiondomain.Query{
		SchemaVersion: collectiondomain.QuerySchemaVersionV1,
		Root: collectiondomain.Clause{Kind: collectiondomain.ClauseKindGroup, Operator: string(collectiondomain.GroupOperatorAND), Clauses: []collectiondomain.Clause{{
			Kind: collectiondomain.ClauseKindPredicate, Field: "object_type", Operator: string(collectiondomain.OperatorIN),
			Values: []json.RawMessage{json.RawMessage(`"CLAIM"`), json.RawMessage(`"TOPIC"`)},
		}}},
	}
	created, err := collectionService.Create(ctx, collectionapp.CreateCommand{
		WorkspaceID: fixture.workspaceID, Name: "GORM Graph Scan", Query: query,
		ViewType: collectiondomain.ViewTypeList, IdempotencyKey: "gorm-graph-collection",
	})
	if err != nil {
		t.Fatal(err)
	}
	planner, err := NewGORMSmartCollectionScanPlanner(collectionService)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.PlanSmartCollectionScan(ctx, fixture.workspaceID, created.Collection.ID)
	if err != nil || plan.TotalNodes != 4 {
		t.Fatalf("GORM scan plan count=%d err=%v", plan.TotalNodes, err)
	}
	repository, err := NewGORMSemanticLinkScanRepository(platform, runtime, collections, foundation.NewUUIDGenerator(nil), foundation.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := graphapp.NewSemanticLinkScanService(repository, repository)
	if err != nil {
		t.Fatal(err)
	}
	ruleID := graphapp.SemanticLinkScanRuleID
	command := graphapp.SemanticLinkScanStartCommand{
		WorkspaceID: fixture.workspaceID, Scope: smartCollectionScope(plan), TotalNodes: plan.TotalNodes,
		IdempotencyKey: "gorm-graph-scan",
		Generation: graphdomain.SemanticLinkScanGeneration{
			Rule:            graphdomain.SemanticLinkCandidateGeneration{RuleID: &ruleID, RuleVersion: graphapp.SemanticLinkScanRuleVersion},
			WorkflowVersion: graphapp.SemanticLinkSmartCollectionScanWorkflowGenerationVersion,
		},
	}

	// The failure occurs after Workflow, Outbox, River and Scan writes. All
	// collaborators must roll back through the Graph-owned scope.
	injected := errors.New("injected Graph scan transaction rollback")
	unitOfWork := repository.repository.unitOfWork
	repository.repository.unitOfWork = graphRollbackUnitOfWork{delegate: unitOfWork, err: injected}
	if _, err := service.StartTopicScan(ctx, command); !errors.Is(err, injected) {
		t.Fatalf("GORM scan rollback error=%v", err)
	}
	assertGORMScanWorkspaceFactCounts(t, ctx, platform.DB(), fixture.workspaceID, 0)
	repository.repository.unitOfWork = unitOfWork

	first, err := service.StartTopicScan(ctx, command)
	if err != nil || first.Replayed || first.Scan.Status != graphdomain.SemanticLinkScanStatusPending {
		t.Fatalf("GORM scan start status=%s replayed=%t err=%v", first.Scan.Status, first.Replayed, err)
	}
	assertScanRuntimeFacts(t, ctx, platform.DB(), fixture.workspaceID, first.Scan.WorkflowRunID, first.Scan.ID)
	replayed, err := service.StartTopicScan(ctx, command)
	if err != nil || !replayed.Replayed || replayed.Scan.ID != first.Scan.ID || replayed.Scan.WorkflowRunID != first.Scan.WorkflowRunID {
		t.Fatalf("GORM scan replay err=%v replayed=%t", err, replayed.Replayed)
	}
	conflict := command
	conflict.TotalNodes++
	if _, err := service.StartTopicScan(ctx, conflict); !hasScanCode(err, graphdomain.ErrorCodeSemanticLinkScanTransitionInvalid) {
		t.Fatalf("GORM scan conflicting replay error=%v", err)
	}
	if _, err := repository.Get(ctx, graphTestID(t), first.Scan.ID); !hasScanCode(err, graphdomain.ErrorCodeSemanticLinkScanNotFound) {
		t.Fatalf("GORM scan cross-workspace error=%v", err)
	}

	pages, err := NewGORMSmartCollectionScanPageRepository(platform, collectionService)
	if err != nil {
		t.Fatal(err)
	}
	page, err := pages.LoadPage(ctx, graphapp.SemanticLinkTopicScanPageRequest{
		WorkspaceID: fixture.workspaceID, ScanID: first.Scan.ID, Scope: command.Scope, TotalNodes: command.TotalNodes, Limit: 2,
	})
	if err != nil || page.Complete || page.ProcessedNodes != 2 || page.LastNode == nil || len(page.Pairs) != 5 {
		t.Fatalf("GORM scan page processed=%d pairs=%d err=%v", page.ProcessedNodes, len(page.Pairs), err)
	}
	progress := graphdomain.SemanticLinkScanProgress{
		WorkspaceID: fixture.workspaceID, ScanID: first.Scan.ID, ExpectedVersion: 1,
		Checkpoint: graphdomain.SemanticLinkScanCheckpoint{ProcessedPage: 1, LastNode: page.LastNode}, ProcessedDelta: page.ProcessedNodes,
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, err := repository.AdvancePage(ctx, progress)
			results <- err
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	succeeded, conflicted := 0, 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case hasScanCode(err, graphdomain.ErrorCodeSemanticLinkScanTransitionInvalid):
			conflicted++
		default:
			t.Fatalf("GORM scan concurrent advance error=%v", err)
		}
	}
	advanced, err := repository.Get(ctx, fixture.workspaceID, first.Scan.ID)
	if err != nil || succeeded != 1 || conflicted != 1 || advanced.Version != 2 || advanced.ProcessedNodes != 2 || advanced.Checkpoint.ProcessedPage != 1 {
		t.Fatalf("GORM scan advance success=%d conflict=%d version=%d processed=%d err=%v", succeeded, conflicted, advanced.Version, advanced.ProcessedNodes, err)
	}
	coordinator, err := workflowapplication.NewRuntimeCoordinator(runtime)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := coordinator.Cancel(ctx, workflowapplication.RunControlCommand{
		WorkflowRunID: first.Scan.WorkflowRunID, ExpectedVersion: 1, IdempotencyKey: "gorm-cancel-graph-scan",
	})
	if err != nil || cancelled.Status != workflowdomain.RunStatusCancelled {
		t.Fatalf("GORM workflow cancellation status=%s err=%v", cancelled.Status, err)
	}
	stored, err := repository.Get(ctx, fixture.workspaceID, first.Scan.ID)
	if err != nil || stored.Status != graphdomain.SemanticLinkScanStatusCancelled || stored.Version != 3 || stored.CompletedAt == nil {
		t.Fatalf("GORM scan cancellation status=%s version=%d err=%v", stored.Status, stored.Version, err)
	}

	if _, err := collectionService.Update(ctx, collectionapp.UpdateCommand{
		WorkspaceID: fixture.workspaceID, CollectionID: created.Collection.ID, ExpectedVersion: 1,
		Name: "GORM Graph Scan changed", Query: query, ViewType: collectiondomain.ViewTypeList, IdempotencyKey: "gorm-graph-collection-update",
	}); err != nil {
		t.Fatal(err)
	}
	replayed, err = service.StartTopicScan(ctx, command)
	if err != nil || !replayed.Replayed || replayed.Scan.ID != first.Scan.ID || replayed.Scan.Status != graphdomain.SemanticLinkScanStatusCancelled {
		t.Fatalf("GORM terminal scan replay after Collection drift err=%v", err)
	}
	command.IdempotencyKey = "gorm-graph-scan-after-drift"
	if _, err := service.StartTopicScan(ctx, command); !hasScanCode(err, collectionapp.ErrorCodeCursorStale) {
		t.Fatalf("GORM stale Collection start error=%v", err)
	}
	assertGORMScanWorkspaceFactCounts(t, ctx, platform.DB(), fixture.workspaceID, 1)
}

func assertGORMScanWorkspaceFactCounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID, expected int) {
	t.Helper()
	var definitions, runs, nodes, outbox, jobs, scans int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM workflow.definition WHERE workspace_id=$1),
		(SELECT count(*) FROM workflow.run WHERE workspace_id=$1),
		(SELECT count(*) FROM workflow.node_run n JOIN workflow.run r ON r.id=n.run_id WHERE r.workspace_id=$1),
		(SELECT count(*) FROM workflow.outbox_event WHERE workspace_id=$1),
		(SELECT count(*) FROM workflow.river_job),
		(SELECT count(*) FROM graph.semantic_link_scan WHERE workspace_id=$1)`, string(workspaceID)).Scan(&definitions, &runs, &nodes, &outbox, &jobs, &scans); err != nil {
		t.Fatal(err)
	}
	if definitions != expected || runs != expected || nodes != expected || outbox != expected || jobs != expected || scans != expected {
		t.Fatalf("GORM scan facts definitions=%d runs=%d nodes=%d outbox=%d jobs=%d scans=%d expected=%d", definitions, runs, nodes, outbox, jobs, scans, expected)
	}
}

func TestSemanticLinkScanRepositoryStartReplayConflictAdvanceAndFinish(t *testing.T) {
	ctx := context.Background()
	pool := newSemanticLinkScanTestPool(t)
	seedTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = seedTx.Rollback(context.Background()) }()
	now := time.Date(2026, 7, 21, 3, 0, 0, 0, time.UTC)
	workspaceID := seedGraphWorkspace(t, ctx, seedTx, now)
	topicID := seedGraphTopic(t, ctx, seedTx, workspaceID, "Semantic Scan Topic", "semantic scan topic", now)
	if err := seedTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	repository := newSemanticLinkScanTestRepository(t, ctx, pool)
	scope := graphdomain.SemanticLinkScanScope{Type: graphdomain.SemanticLinkScanScopeTopic, Ref: string(topicID), Version: 1, SchemaVersion: "topic-scan/v1"}
	generation := graphdomain.SemanticLinkScanGeneration{
		Rule:            scanRuleGeneration(t, "semantic-link-scan-rule"),
		WorkflowVersion: "workflow/v1",
	}
	fingerprint, err := graphdomain.ComputeSemanticLinkScanFingerprint(workspaceID, scope, generation)
	if err != nil {
		t.Fatal(err)
	}
	request := graphapp.SemanticLinkScanStartRequest{
		WorkspaceID: workspaceID, Scope: scope, Generation: generation,
		Fingerprint: fingerprint, RequestHash: scanRequestHash(t, workspaceID, scope, generation, fingerprint),
		TotalNodes: 5, IdempotencyKey: "scan-start-1",
		WorkflowDefinitionKey:      graphapp.SemanticLinkScanWorkflowDefinitionKey,
		WorkflowDefinitionVersion:  graphapp.SemanticLinkScanWorkflowDefinitionVersion,
		WorkflowInputSchemaVersion: graphapp.SemanticLinkScanInputSchemaVersion,
	}
	first, err := repository.StartOrReplay(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Replayed || first.Scan.Status != graphdomain.SemanticLinkScanStatusPending || first.Scan.WorkflowRunID == "" || first.Scan.Fingerprint != fingerprint {
		t.Fatalf("start result=%#v", first)
	}
	assertScanRuntimeFacts(t, ctx, pool.Pool, request.WorkspaceID, first.Scan.WorkflowRunID, first.Scan.ID)

	replayed, err := repository.StartOrReplay(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.Scan.ID != first.Scan.ID || replayed.Scan.WorkflowRunID != first.Scan.WorkflowRunID {
		t.Fatalf("replayed=%#v first=%#v", replayed, first)
	}

	conflict := request
	conflict.TotalNodes = 6
	if _, err := repository.StartOrReplay(ctx, conflict); !hasScanCode(err, graphdomain.ErrorCodeSemanticLinkScanTransitionInvalid) {
		t.Fatalf("same-key conflict err=%v", err)
	}

	if other, err := repository.Get(ctx, graphTestID(t), first.Scan.ID); !hasScanCode(err, graphdomain.ErrorCodeSemanticLinkScanNotFound) && err == nil {
		t.Fatalf("cross-workspace get=%#v err=%v", other, err)
	}

	lastNode := knowledgeNodeRef(topicID)
	progress := graphdomain.SemanticLinkScanProgress{
		ScanID: first.Scan.ID, WorkspaceID: workspaceID, ExpectedVersion: 1,
		Checkpoint:     graphdomain.SemanticLinkScanCheckpoint{Cursor: "page-1", ProcessedPage: 1, LastNode: &lastNode},
		ProcessedDelta: 2, CandidateDelta: 1, SuppressedDelta: 1, ReopenedDelta: 0, FailedDelta: 0,
	}
	advanced, err := repository.AdvancePage(ctx, progress)
	if err != nil {
		t.Fatal(err)
	}
	if advanced.Version != 2 || advanced.Status != graphdomain.SemanticLinkScanStatusRunning || advanced.ProcessedNodes != 2 || advanced.CandidateCount != 1 || advanced.SuppressedCount != 1 || advanced.Checkpoint.Cursor != "page-1" {
		t.Fatalf("advanced=%#v", advanced)
	}

	stateService, err := graphapp.NewSemanticLinkScanStateService(repository)
	if err != nil {
		t.Fatal(err)
	}
	terminal := graphdomain.SemanticLinkScanTerminal{
		ScanID: first.Scan.ID, WorkspaceID: workspaceID, ExpectedVersion: 2,
		Status: graphdomain.SemanticLinkScanStatusSucceeded, At: advanced.UpdatedAt.Add(time.Second + 789*time.Nanosecond),
	}
	finished, err := stateService.Finish(ctx, terminal)
	if err != nil {
		t.Fatal(err)
	}
	completedAtMatches := false
	if finished.CompletedAt != nil {
		completedAtDelta := finished.CompletedAt.UTC().Sub(terminal.At.UTC())
		if completedAtDelta < 0 {
			completedAtDelta = -completedAtDelta
		}
		completedAtMatches = completedAtDelta < time.Microsecond
	}
	if finished.Status != graphdomain.SemanticLinkScanStatusSucceeded || !completedAtMatches {
		t.Fatalf("finished=%#v", finished)
	}

	failedScope := scope
	failedGeneration := graphdomain.SemanticLinkScanGeneration{Rule: scanRuleGeneration(t, "semantic-link-scan-rule-failed"), WorkflowVersion: "workflow/v1"}
	failedFingerprint, err := graphdomain.ComputeSemanticLinkScanFingerprint(workspaceID, failedScope, failedGeneration)
	if err != nil {
		t.Fatal(err)
	}
	failedRequest := request
	failedRequest.Generation = failedGeneration
	failedRequest.Fingerprint = failedFingerprint
	failedRequest.RequestHash = scanRequestHash(t, workspaceID, failedScope, failedGeneration, failedFingerprint)
	failedRequest.IdempotencyKey = "scan-start-3"
	failedRequest.TotalNodes = 1
	failed, err := repository.StartOrReplay(ctx, failedRequest)
	if err != nil {
		t.Fatal(err)
	}
	failedTerminal := graphdomain.SemanticLinkScanTerminal{
		ScanID: failed.Scan.ID, WorkspaceID: workspaceID, ExpectedVersion: 1,
		Status: graphdomain.SemanticLinkScanStatusFailed, Error: &graphdomain.SemanticLinkScanError{Stage: "discover", Code: "MODEL_UNAVAILABLE", Retryable: true},
		At: failed.Scan.UpdatedAt.Add(time.Second),
	}
	failedResult, err := repository.Finish(ctx, failedTerminal)
	if err != nil {
		t.Fatal(err)
	}
	if failedResult.Status != graphdomain.SemanticLinkScanStatusFailed || failedResult.LastError == nil || failedResult.LastError.Code != "MODEL_UNAVAILABLE" {
		t.Fatalf("failed result=%#v", failedResult)
	}
	failedReplay, err := repository.StartOrReplay(ctx, failedRequest)
	if err != nil || !failedReplay.Replayed || failedReplay.Scan.ID != failed.Scan.ID || failedReplay.Scan.Status != graphdomain.SemanticLinkScanStatusFailed {
		t.Fatalf("failed replay=%#v err=%v", failedReplay, err)
	}

	restartRequest := failedRequest
	restartRequest.IdempotencyKey = "scan-start-4"
	restarted, err := repository.StartOrReplay(ctx, restartRequest)
	if err != nil || restarted.Replayed || restarted.Scan.ID == failed.Scan.ID || restarted.Scan.Fingerprint != failed.Scan.Fingerprint {
		t.Fatalf("failed restart=%#v err=%v", restarted, err)
	}
	cancelledAt := restarted.Scan.UpdatedAt.Add(time.Second)
	cancelled, err := repository.Finish(ctx, graphdomain.SemanticLinkScanTerminal{
		ScanID: restarted.Scan.ID, WorkspaceID: workspaceID, ExpectedVersion: 1,
		Status: graphdomain.SemanticLinkScanStatusCancelled, At: cancelledAt,
	})
	if err != nil || cancelled.Status != graphdomain.SemanticLinkScanStatusCancelled {
		t.Fatalf("cancelled=%#v err=%v", cancelled, err)
	}
	cancelledReplay, err := repository.StartOrReplay(ctx, restartRequest)
	if err != nil || !cancelledReplay.Replayed || cancelledReplay.Scan.ID != restarted.Scan.ID || cancelledReplay.Scan.Status != graphdomain.SemanticLinkScanStatusCancelled {
		t.Fatalf("cancelled replay=%#v err=%v", cancelledReplay, err)
	}
	secondRestartRequest := failedRequest
	secondRestartRequest.IdempotencyKey = "scan-start-5"
	secondRestart, err := repository.StartOrReplay(ctx, secondRestartRequest)
	if err != nil || secondRestart.Replayed || secondRestart.Scan.ID == restarted.Scan.ID || secondRestart.Scan.Fingerprint != failed.Scan.Fingerprint {
		t.Fatalf("cancelled restart=%#v err=%v", secondRestart, err)
	}
}

func TestSemanticLinkScanRepositoryConcurrentStartAndCommitLossReplay(t *testing.T) {
	ctx := context.Background()
	pool := newSemanticLinkScanTestPool(t)
	seedTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = seedTx.Rollback(context.Background()) }()
	now := time.Date(2026, 7, 21, 4, 0, 0, 0, time.UTC)
	workspaceID := seedGraphWorkspace(t, ctx, seedTx, now)
	topicID := seedGraphTopic(t, ctx, seedTx, workspaceID, "Concurrent Scan Topic", "concurrent scan topic", now)
	if err := seedTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	scope := graphdomain.SemanticLinkScanScope{Type: graphdomain.SemanticLinkScanScopeTopic, Ref: string(topicID), Version: 1, SchemaVersion: "topic-scan/v1"}
	generation := graphdomain.SemanticLinkScanGeneration{Rule: scanRuleGeneration(t, "semantic-link-scan-rule-concurrent"), WorkflowVersion: "workflow/v1"}
	fingerprint, err := graphdomain.ComputeSemanticLinkScanFingerprint(workspaceID, scope, generation)
	if err != nil {
		t.Fatal(err)
	}
	request := graphapp.SemanticLinkScanStartRequest{
		WorkspaceID: workspaceID, Scope: scope, Generation: generation,
		Fingerprint: fingerprint, RequestHash: scanRequestHash(t, workspaceID, scope, generation, fingerprint),
		TotalNodes: 9, IdempotencyKey: "scan-start-concurrent",
		WorkflowDefinitionKey:      graphapp.SemanticLinkScanWorkflowDefinitionKey,
		WorkflowDefinitionVersion:  graphapp.SemanticLinkScanWorkflowDefinitionVersion,
		WorkflowInputSchemaVersion: graphapp.SemanticLinkScanInputSchemaVersion,
	}
	repository := newSemanticLinkScanTestRepository(t, ctx, pool)
	const workers = 6
	results := make(chan graphapp.SemanticLinkScanStartResult, workers)
	errorsCh := make(chan error, workers)
	var waitGroup sync.WaitGroup
	for i := 0; i < workers; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			result, err := repository.StartOrReplay(ctx, request)
			if err != nil {
				errorsCh <- err
				return
			}
			results <- result
		}()
	}
	waitGroup.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatalf("concurrent start err=%v", err)
		}
	}
	created := 0
	var first graphapp.SemanticLinkScanStartResult
	for result := range results {
		if !result.Replayed {
			created++
			first = result
		}
		if first.Scan.ID == "" {
			first = result
		}
		if result.Scan.ID != first.Scan.ID || result.Scan.WorkflowRunID != first.Scan.WorkflowRunID {
			t.Fatalf("concurrent mismatch=%#v first=%#v", result, first)
		}
	}
	if created != 1 {
		t.Fatalf("created=%d", created)
	}
	var scans, runs, jobs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM graph.semantic_link_scan WHERE workspace_id=$1 AND idempotency_key=$2`, string(workspaceID), request.IdempotencyKey).Scan(&scans); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.run WHERE workspace_id=$1 AND idempotency_key=$2`, string(workspaceID), "semantic-link-scan:"+scanRequestKeyDigest(workspaceID, request.IdempotencyKey)).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.river_job WHERE kind=$1 AND args->>'node_run_id' IN (SELECT id::text FROM workflow.node_run WHERE run_id IN (SELECT id FROM workflow.run WHERE workspace_id=$2 AND idempotency_key=$3))`, riveradapter.NodeJobKind, string(workspaceID), "semantic-link-scan:"+scanRequestKeyDigest(workspaceID, request.IdempotencyKey)).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if scans != 1 || runs != 1 || jobs != 1 {
		t.Fatalf("scans=%d runs=%d jobs=%d", scans, runs, jobs)
	}

	lossRepo := newSemanticLinkScanTestRepositoryWithCommitLoss(t, ctx, pool)
	lossRequest := request
	lossRequest.IdempotencyKey = "scan-start-loss"
	lossRequest.TotalNodes = 11
	lossGeneration := graphdomain.SemanticLinkScanGeneration{Rule: scanRuleGeneration(t, "semantic-link-scan-rule-loss"), WorkflowVersion: "workflow/v1"}
	lossFingerprint, err := graphdomain.ComputeSemanticLinkScanFingerprint(workspaceID, scope, lossGeneration)
	if err != nil {
		t.Fatal(err)
	}
	lossRequest.Generation = lossGeneration
	lossRequest.Fingerprint = lossFingerprint
	lossRequest.RequestHash = scanRequestHash(t, workspaceID, scope, lossGeneration, lossFingerprint)
	recovered, err := lossRepo.StartOrReplay(ctx, lossRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !recovered.Replayed || recovered.Scan.ID == "" || recovered.Scan.WorkflowRunID == "" {
		t.Fatalf("recovered=%#v", recovered)
	}
}

func TestSemanticLinkTopicScanPlannerUsesFormalTopicMembership(t *testing.T) {
	ctx := context.Background()
	pool := newSemanticLinkScanTestPool(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	now := time.Now().UTC()
	workspaceID := seedGraphWorkspace(t, ctx, tx, now)
	provenance := seedGraphProvenance(t, ctx, tx, workspaceID, now)
	topicID := seedGraphTopic(t, ctx, tx, workspaceID, "Planner Topic", "planner topic", now)
	confirmed := seedGraphClaim(t, ctx, tx, workspaceID, "Confirmed planner claim", knowledge.ClaimStatusConfirmed, nil, now)
	secondConfirmed := seedGraphClaim(t, ctx, tx, workspaceID, "Second confirmed planner claim", knowledge.ClaimStatusConfirmed, nil, now)
	suggested := seedGraphClaim(t, ctx, tx, workspaceID, "Suggested planner claim", knowledge.ClaimStatusSuggested, nil, now)
	for index, claimID := range []foundation.ID{confirmed, secondConfirmed} {
		seedScanClaimSource(t, ctx, tx, workspaceID, claimID, provenance, index, now)
		seedScanMembership(t, ctx, tx, workspaceID, claimID, topicID, provenance, index, now)
	}
	_ = suggested
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	planner, err := NewGORMSemanticLinkTopicScanPlanner(pool.platform)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.PlanTopicScan(ctx, workspaceID, topicID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.WorkspaceID != workspaceID || plan.TopicID != topicID || plan.TopicVersion != 1 || plan.TotalNodes != 2 {
		t.Fatalf("plan=%#v", plan)
	}
	if _, err := planner.PlanTopicScan(ctx, graphTestID(t), topicID); !hasScanCode(err, graphdomain.ErrorCodeSemanticLinkScanNotFound) {
		t.Fatalf("cross-workspace plan err=%v", err)
	}
}

func TestSemanticLinkTopicScanPagePersistsDeterministicCandidate(t *testing.T) {
	ctx := context.Background()
	pool := newSemanticLinkScanTestPool(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	now := time.Now().UTC().Add(-time.Minute)
	workspaceID := seedGraphWorkspace(t, ctx, tx, now)
	provenance := seedGraphProvenance(t, ctx, tx, workspaceID, now)
	topicID := seedGraphTopic(t, ctx, tx, workspaceID, "Semantic Links", "semantic links", now)
	first := seedGraphClaim(t, ctx, tx, workspaceID, "Semantic links connect verified claims", knowledge.ClaimStatusConfirmed, nil, now)
	second := seedGraphClaim(t, ctx, tx, workspaceID, "Semantic links connect complementary claims", knowledge.ClaimStatusConfirmed, nil, now)
	for index, claimID := range []foundation.ID{first, second} {
		seedScanClaimSource(t, ctx, tx, workspaceID, claimID, provenance, index, now)
		seedScanMembership(t, ctx, tx, workspaceID, claimID, topicID, provenance, index, now)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	pageSource, err := NewGORMSemanticLinkTopicScanPageRepository(pool.platform)
	if err != nil {
		t.Fatal(err)
	}
	candidateRepository, err := NewGORMRepository(pool.platform)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := NewGORMSemanticLinkDiscoveryCandidateWriter(candidateRepository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	executor, err := graphapp.NewSemanticLinkTopicScanExecutor(pageSource, graphapp.NewSemanticLinkDiscoveryService(nil, nil), writer)
	if err != nil {
		t.Fatal(err)
	}
	ruleID := graphapp.SemanticLinkScanRuleID
	result, err := executor.ProcessPage(ctx, graphapp.SemanticLinkTopicScanPageRequest{
		WorkspaceID: workspaceID, ScanID: graphTestID(t),
		Scope: graphdomain.SemanticLinkScanScope{Type: graphdomain.SemanticLinkScanScopeTopic, Ref: string(topicID), Version: 1, SchemaVersion: graphapp.SemanticLinkTopicScanScopeSchemaVersion},
		Generation: graphdomain.SemanticLinkScanGeneration{
			Rule:            graphdomain.SemanticLinkCandidateGeneration{RuleID: &ruleID, RuleVersion: graphapp.SemanticLinkScanRuleVersion},
			WorkflowVersion: graphapp.SemanticLinkScanWorkflowGenerationVersion,
		},
		Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || result.ProcessedNodes != 2 || result.Candidates.Created != 1 || len(result.Discovery.Items) != 1 {
		t.Fatalf("result=%#v", result)
	}
	var candidates int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM graph.semantic_link_candidate WHERE workspace_id=$1 AND status='ACTIVE'`, string(workspaceID)).Scan(&candidates); err != nil {
		t.Fatal(err)
	}
	if candidates != 1 {
		t.Fatalf("candidate count=%d", candidates)
	}
}

func TestSemanticLinkTopicScanPageIncludesBoundedCrossPagePairs(t *testing.T) {
	ctx := context.Background()
	pool := newSemanticLinkScanTestPool(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	now := time.Now().UTC().Add(-time.Minute)
	workspaceID := seedGraphWorkspace(t, ctx, tx, now)
	provenance := seedGraphProvenance(t, ctx, tx, workspaceID, now)
	topicID := seedGraphTopic(t, ctx, tx, workspaceID, "Cross Page Semantic Links", "cross page semantic links", now)
	claimIDs := make([]string, 0, 205)
	for index := 0; index < 205; index++ {
		claimID := seedGraphClaim(t, ctx, tx, workspaceID, "Cross page semantic links preserve bounded recall", knowledge.ClaimStatusConfirmed, nil, now.Add(time.Duration(index)*time.Microsecond))
		claimIDs = append(claimIDs, string(claimID))
		seedScanClaimSource(t, ctx, tx, workspaceID, claimID, provenance, index, now)
		seedScanMembership(t, ctx, tx, workspaceID, claimID, topicID, provenance, index, now)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	sort.Strings(claimIDs)
	source, target := foundation.ID(claimIDs[99]), foundation.ID(claimIDs[100])
	pageSource, err := NewGORMSemanticLinkTopicScanPageRepository(pool.platform)
	if err != nil {
		t.Fatal(err)
	}
	scope := graphdomain.SemanticLinkScanScope{Type: graphdomain.SemanticLinkScanScopeTopic, Ref: string(topicID), Version: 1, SchemaVersion: graphapp.SemanticLinkTopicScanScopeSchemaVersion}
	firstPage, err := pageSource.LoadPage(ctx, graphapp.SemanticLinkTopicScanPageRequest{
		WorkspaceID: workspaceID, ScanID: graphTestID(t), Scope: scope, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if firstPage.Complete || firstPage.ProcessedNodes != 100 || firstPage.NextCursor != claimIDs[99] || len(firstPage.Pairs) != 10_000 {
		t.Fatalf("first page complete=%v processed=%d cursor=%s pairs=%d", firstPage.Complete, firstPage.ProcessedNodes, firstPage.NextCursor, len(firstPage.Pairs))
	}
	if _, err := pool.Exec(ctx, `ANALYZE core.relation; ANALYZE core.claim`); err != nil {
		t.Fatal(err)
	}
	assertSemanticLinkTopicPairPlan(t, ctx, pool.platform, workspaceID, topicID, claimIDs[:100])
	foundBoundaryPair := false
	for _, pair := range firstPage.Pairs {
		key, keyErr := pair.PairKey()
		if keyErr != nil {
			t.Fatal(keyErr)
		}
		if key.Left.ID == source && key.Right.ID == target {
			foundBoundaryPair = true
			break
		}
	}
	if !foundBoundaryPair {
		t.Fatalf("cross-page pair %s -> %s was omitted", source, target)
	}
	secondPage, err := pageSource.LoadPage(ctx, graphapp.SemanticLinkTopicScanPageRequest{
		WorkspaceID: workspaceID, ScanID: graphTestID(t), Scope: scope, Cursor: claimIDs[99], Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if secondPage.Complete || secondPage.ProcessedNodes != 100 || secondPage.NextCursor != claimIDs[199] || len(secondPage.Pairs) != 5_440 {
		t.Fatalf("second page=%#v", secondPage)
	}
	thirdPage, err := pageSource.LoadPage(ctx, graphapp.SemanticLinkTopicScanPageRequest{
		WorkspaceID: workspaceID, ScanID: graphTestID(t), Scope: scope, Cursor: claimIDs[199], Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !thirdPage.Complete || thirdPage.ProcessedNodes != 5 || thirdPage.NextCursor != "" || len(thirdPage.Pairs) != 10 {
		t.Fatalf("third page=%#v", thirdPage)
	}
}

func seedScanClaimSource(t *testing.T, ctx context.Context, tx pgx.Tx, workspaceID, claimID foundation.ID, provenance graphProvenance, index int, now time.Time) {
	t.Helper()
	if _, err := tx.Exec(ctx, `INSERT INTO core.claim_source(id,workspace_id,claim_id,source_version_id,source_span_id,support_type,reason,evidence_hash,created_at) VALUES($1,$2,$3,$4,$5,'SUPPORTS',$6,$7,$8)`,
		string(graphTestID(t)), string(workspaceID), string(claimID), string(provenance.sourceVersionID), string(provenance.sourceSpanID),
		"semantic link evidence", graphHash(fmt.Sprintf("scan-source-%d-%s", index, claimID)), now); err != nil {
		t.Fatal(err)
	}
}

func seedScanMembership(t *testing.T, ctx context.Context, tx pgx.Tx, workspaceID, claimID, topicID foundation.ID, provenance graphProvenance, index int, now time.Time) {
	t.Helper()
	applicability, err := knowledge.ParseApplicability([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	relationID := graphTestID(t)
	if _, err := tx.Exec(ctx, `INSERT INTO core.relation(id,workspace_id,source_node_type,source_node_id,target_node_type,target_node_id,relation_type,status,confidence_score,fingerprint,evidence_fingerprint,version,created_at,updated_at) VALUES($1,$2,'CLAIM',$3,'TOPIC',$4,'BELONGS_TO','SUGGESTED',0.9,$5,$6,1,$7,$7)`,
		string(relationID), string(workspaceID), string(claimID), string(topicID), graphHash(fmt.Sprintf("scan-membership-%d-%s", index, claimID)), graphHash(fmt.Sprintf("scan-membership-set-%d-%s", index, claimID)), now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.relation_evidence(id,workspace_id,relation_id,source_version_id,source_span_id,reason,evidence_hash,applicability,applicability_schema_version,applicability_hash,confirmation_method,confirmed_by,created_at) VALUES($1,$2,$3,$4,$5,'semantic scan membership evidence',$6,$7,$8,$9,'SOURCE_DERIVED','semantic scan fixture',$10)`,
		string(graphTestID(t)), string(workspaceID), string(relationID), string(provenance.sourceVersionID), string(provenance.sourceSpanID),
		graphHash(fmt.Sprintf("scan-membership-evidence-%d-%s", index, claimID)), string(applicability.CanonicalJSON), applicability.SchemaVersion, applicability.Hash, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE core.relation
SET status='CONFIRMED',confirmation_method='SOURCE_DERIVED',confirmation_ref='semantic scan fixture',version=2,updated_at=$3
WHERE id=$1 AND workspace_id=$2`, string(relationID), string(workspaceID), now.Add(time.Microsecond)); err != nil {
		t.Fatal(err)
	}
}

func newSemanticLinkScanTestPool(t *testing.T) *graphTestPool {
	t.Helper()
	return newGraphTestPool(t, 12)
}

func newGraphRuntimeForTest(t *testing.T, platform *platformpostgres.Pool) *workflowpostgres.GORMRuntimeRepository {
	t.Helper()
	graph, err := NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := modelcrypto.NewSealer(bytes.Repeat([]byte{0x2a}, 32))
	if err != nil {
		t.Fatal(err)
	}
	audit, err := auditpostgres.NewGORMStore(platform)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := modelsettingspostgres.NewGORMRepository(platform,
		modelsettingspostgres.WithGORMSecretSealer(sealer), modelsettingspostgres.WithGORMAuditAppender(audit))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workflowpostgres.NewGORMRuntimeRepositoryWithHooks(platform, riveradapter.DefaultOptions(), settings,
		workflowpostgres.GORMRuntimeRepositoryHooks{CancellationSafety: graph})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func newSemanticLinkScanTestRepository(t *testing.T, _ context.Context, pool *graphTestPool) *GORMSemanticLinkScanRepository {
	t.Helper()
	runtime := newGraphRuntimeForTest(t, pool.platform)
	collections, err := collectionpostgres.NewGORMRepository(pool.platform)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewGORMSemanticLinkScanRepository(pool.platform, runtime, collections, foundation.NewUUIDGenerator(nil), foundation.FixedClock{Value: time.Date(2026, 7, 21, 3, 30, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func newSemanticLinkScanTestRepositoryWithCommitLoss(t *testing.T, ctx context.Context, pool *graphTestPool) *GORMSemanticLinkScanRepository {
	t.Helper()
	repository := newSemanticLinkScanTestRepository(t, ctx, pool)
	repository.repository.unitOfWork = &graphCommitLossUnitOfWork{
		delegate: repository.repository.unitOfWork,
		err:      errors.New("injected scan commit response loss"),
	}
	return repository
}

func assertScanRuntimeFacts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, workflowRunID, scanID foundation.ID) {
	t.Helper()
	var runs, nodes, events, jobs, scans int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.run WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(workflowRunID)).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.node_run WHERE run_id=$1`, string(workflowRunID)).Scan(&nodes); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.outbox_event WHERE workspace_id=$1 AND run_id=$2`, string(workspaceID), string(workflowRunID)).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.river_job WHERE kind=$1 AND args->>'node_run_id' IN (SELECT id::text FROM workflow.node_run WHERE run_id=$2)`, riveradapter.NodeJobKind, string(workflowRunID)).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM graph.semantic_link_scan WHERE id=$1 AND workspace_id=$2 AND workflow_run_id=$3`, string(scanID), string(workspaceID), string(workflowRunID)).Scan(&scans); err != nil {
		t.Fatal(err)
	}
	if runs != 1 || nodes != 1 || events != 1 || jobs != 1 || scans != 1 {
		t.Fatalf("runs=%d nodes=%d events=%d jobs=%d scans=%d", runs, nodes, events, jobs, scans)
	}
}

func scanRuleGeneration(t *testing.T, suffix string) graphdomain.SemanticLinkCandidateGeneration {
	t.Helper()
	ruleID := graphTestID(t)
	return graphdomain.SemanticLinkCandidateGeneration{RuleID: &ruleID, RuleVersion: "rules/" + suffix}
}

func scanRequestHash(t *testing.T, workspaceID foundation.ID, scope graphdomain.SemanticLinkScanScope, generation graphdomain.SemanticLinkScanGeneration, fingerprint string) string {
	t.Helper()
	raw, err := json.Marshal(struct {
		Schema      string                                 `json:"schema"`
		WorkspaceID foundation.ID                          `json:"workspace_id"`
		Scope       graphdomain.SemanticLinkScanScope      `json:"scope"`
		Fingerprint string                                 `json:"fingerprint"`
		Generation  graphdomain.SemanticLinkScanGeneration `json:"generation"`
	}{
		Schema: "semantic-link-scan-request/v1", WorkspaceID: workspaceID, Scope: scope, Fingerprint: fingerprint, Generation: generation,
	})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func scanRequestKeyDigest(workspaceID foundation.ID, idempotencyKey string) string {
	sum := sha256.Sum256([]byte(string(workspaceID) + "\x00" + idempotencyKey))
	return hex.EncodeToString(sum[:])
}

func knowledgeNodeRef(id foundation.ID) knowledge.NodeRef {
	return knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: id}
}

func hasScanCode(err error, code string) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Code == code
}
