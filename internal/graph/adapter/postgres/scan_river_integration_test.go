//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphworkflow "github.com/CodeZen-Lizhi/zhixu/internal/graph/adapter/workflow"
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

func TestSemanticLinkTopicScanRunsThroughRealRiverToCandidate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := newSemanticLinkScanTestPool(t, ctx)
	workspaceID, topicID := seedSemanticLinkRiverFixture(t, ctx, pool)
	command, registry, coordinator := newSemanticLinkRiverRuntime(t, pool)

	runtimeWorker, err := riveradapter.NewRuntimeNodeWorker(registry, coordinator, "semantic-link-river-success", 10*time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	workers := riveradapter.NewWorkers()
	if err := riveradapter.AddRuntimeWorkerSafely(workers, runtimeWorker); err != nil {
		t.Fatal(err)
	}
	workerClient, err := riveradapter.NewClient(pool, workers)
	if err != nil {
		t.Fatal(err)
	}
	if err := workerClient.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		_ = workerClient.Stop(stopCtx)
	}()

	started, err := command.StartTopicScan(ctx, graphapp.SemanticLinkTopicScanRequest{
		WorkspaceID: workspaceID, TopicID: topicID, IdempotencyKey: "semantic-link-river-success",
	})
	if err != nil {
		t.Fatal(err)
	}
	if started.Replayed || started.StatusURL != "/api/v1/workflows/"+string(started.Scan.WorkflowRunID) {
		t.Fatalf("start=%+v", started)
	}
	if err := waitForSemanticLinkRiverScan(ctx, pool, started.Scan.ID, graphdomain.SemanticLinkScanStatusSucceeded, workflowdomain.RunStatusSucceeded); err != nil {
		t.Fatal(err)
	}
	assertSemanticLinkRiverFacts(t, ctx, pool, workspaceID, started.Scan.ID, 3, 3)

	replayed, err := command.StartTopicScan(ctx, graphapp.SemanticLinkTopicScanRequest{
		WorkspaceID: workspaceID, TopicID: topicID, IdempotencyKey: "semantic-link-river-success",
	})
	if err != nil || !replayed.Replayed || replayed.Scan.ID != started.Scan.ID || replayed.Scan.WorkflowRunID != started.Scan.WorkflowRunID {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
}

func TestSemanticLinkTopicScanReplaysAfterWorkflowCompletionResponseLoss(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := newSemanticLinkScanTestPool(t, ctx)
	workspaceID, topicID := seedSemanticLinkRiverFixture(t, ctx, pool)
	command, registry, coordinator := newSemanticLinkRiverRuntime(t, pool)

	started, err := command.StartTopicScan(ctx, graphapp.SemanticLinkTopicScanRequest{
		WorkspaceID: workspaceID, TopicID: topicID, IdempotencyKey: "semantic-link-completion-response-loss",
	})
	if err != nil {
		t.Fatal(err)
	}
	var nodeID foundation.ID
	var dispatchNo int
	var jobID int64
	if err := pool.QueryRow(ctx, `
		SELECT node.id,node.dispatch_no,job.id
		FROM workflow.node_run node
		JOIN workflow.river_job job
		  ON job.kind=$2
		 AND job.args->>'node_run_id'=node.id::text
		 AND (job.args->>'dispatch_no')::integer=node.dispatch_no
		WHERE node.run_id=$1`, string(started.Scan.WorkflowRunID), riveradapter.NodeJobKind).Scan(&nodeID, &dispatchNo, &jobID); err != nil {
		t.Fatal(err)
	}
	args, err := riveradapter.NewNodeJobArgs(nodeID, dispatchNo)
	if err != nil {
		t.Fatal(err)
	}
	job := &river.Job[riveradapter.NodeJobArgs]{JobRow: &rivertype.JobRow{ID: jobID, Attempt: 1}, Args: args}
	lossy := &failFirstSemanticLinkCompletion{delegate: coordinator}
	runtimeWorker, err := riveradapter.NewRuntimeNodeWorker(registry, lossy, "semantic-link-response-loss", 10*time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtimeWorker.Work(ctx, job); err == nil {
		t.Fatal("injected workflow completion response loss was not returned")
	}
	if err := runtimeWorker.Work(ctx, job); err != nil {
		t.Fatal(err)
	}
	if !lossy.outputsMatch() {
		t.Fatal("scan terminal replay changed the canonical workflow output")
	}
	if err := waitForSemanticLinkRiverScan(ctx, pool, started.Scan.ID, graphdomain.SemanticLinkScanStatusSucceeded, workflowdomain.RunStatusSucceeded); err != nil {
		t.Fatal(err)
	}
	assertSemanticLinkRiverFacts(t, ctx, pool, workspaceID, started.Scan.ID, 3, 3)
}

func TestSemanticLinkTopicScanCancellationConvergesWithWorkflow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := newSemanticLinkScanTestPool(t, ctx)
	workspaceID, topicID := seedSemanticLinkRiverFixture(t, ctx, pool)
	blocking := &blockingSemanticLinkPageSource{entered: make(chan struct{})}
	command, registry, coordinator := newSemanticLinkRiverRuntime(t, pool, func(delegate graphapp.SemanticLinkTopicScanPageSource) graphapp.SemanticLinkTopicScanPageSource {
		blocking.delegate = delegate
		return blocking
	})

	runtimeWorker, err := riveradapter.NewRuntimeNodeWorker(registry, coordinator, "semantic-link-river-cancel", 3*time.Second, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	workers := riveradapter.NewWorkers()
	if err := riveradapter.AddRuntimeWorkerSafely(workers, runtimeWorker); err != nil {
		t.Fatal(err)
	}
	workerClient, err := riveradapter.NewClient(pool, workers)
	if err != nil {
		t.Fatal(err)
	}
	if err := workerClient.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		_ = workerClient.Stop(stopCtx)
	}()

	started, err := command.StartTopicScan(ctx, graphapp.SemanticLinkTopicScanRequest{
		WorkspaceID: workspaceID, TopicID: topicID, IdempotencyKey: "semantic-link-river-cancel",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-blocking.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var runVersion int64
	if err := pool.QueryRow(ctx, `SELECT version FROM workflow.run WHERE id=$1`, string(started.Scan.WorkflowRunID)).Scan(&runVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Cancel(ctx, workflowapplication.RunControlCommand{
		WorkflowRunID: started.Scan.WorkflowRunID, ExpectedVersion: runVersion, IdempotencyKey: "cancel-semantic-link-scan",
	}); err != nil {
		t.Fatal(err)
	}
	if err := waitForSemanticLinkRiverScan(ctx, pool, started.Scan.ID, graphdomain.SemanticLinkScanStatusCancelled, workflowdomain.RunStatusCancelled); err != nil {
		t.Fatal(err)
	}
	var candidates int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM graph.semantic_link_candidate WHERE workspace_id=$1`, string(workspaceID)).Scan(&candidates); err != nil || candidates != 0 {
		t.Fatalf("candidates=%d err=%v", candidates, err)
	}
}

func TestSemanticLinkTopicScanRiverRecoversFromRetryablePageFault(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := newSemanticLinkScanTestPool(t, ctx)
	workspaceID, topicID := seedSemanticLinkRiverFixture(t, ctx, pool)
	faulting := &faultingSemanticLinkPageSource{remainingFailures: 1}
	command, registry, coordinator := newSemanticLinkRiverRuntime(t, pool, func(delegate graphapp.SemanticLinkTopicScanPageSource) graphapp.SemanticLinkTopicScanPageSource {
		faulting.delegate = delegate
		return faulting
	})
	workerClient := startSemanticLinkTestWorker(t, ctx, pool, registry, coordinator, "semantic-link-river-retry")
	defer stopSemanticLinkTestWorker(workerClient)

	started, err := command.StartTopicScan(ctx, graphapp.SemanticLinkTopicScanRequest{
		WorkspaceID: workspaceID, TopicID: topicID, IdempotencyKey: "semantic-link-river-retry",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := waitForSemanticLinkRiverScan(ctx, pool, started.Scan.ID, graphdomain.SemanticLinkScanStatusSucceeded, workflowdomain.RunStatusSucceeded); err != nil {
		t.Fatal(err)
	}
	if faulting.callCount() < 2 {
		t.Fatalf("page calls=%d want retry plus recovery", faulting.callCount())
	}
	assertSemanticLinkRiverFacts(t, ctx, pool, workspaceID, started.Scan.ID, 3, 3)
}

func TestSemanticLinkTopicScanRiverPersistsFaultAfterRetryBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := newSemanticLinkScanTestPool(t, ctx)
	workspaceID, topicID := seedSemanticLinkRiverFixture(t, ctx, pool)
	faulting := &faultingSemanticLinkPageSource{alwaysFail: true}
	command, registry, coordinator := newSemanticLinkRiverRuntime(t, pool, func(delegate graphapp.SemanticLinkTopicScanPageSource) graphapp.SemanticLinkTopicScanPageSource {
		faulting.delegate = delegate
		return faulting
	})
	workerClient := startSemanticLinkTestWorker(t, ctx, pool, registry, coordinator, "semantic-link-river-final-fault")
	defer stopSemanticLinkTestWorker(workerClient)

	started, err := command.StartTopicScan(ctx, graphapp.SemanticLinkTopicScanRequest{
		WorkspaceID: workspaceID, TopicID: topicID, IdempotencyKey: "semantic-link-river-final-fault",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := waitForSemanticLinkRiverScan(ctx, pool, started.Scan.ID, graphdomain.SemanticLinkScanStatusFailed, workflowdomain.RunStatusFailed); err != nil {
		t.Fatal(err)
	}
	var errorCode string
	var processedNodes, candidateCount int64
	if err := pool.QueryRow(ctx, `SELECT last_error->>'code',processed_nodes,candidate_count FROM graph.semantic_link_scan WHERE id=$1`, string(started.Scan.ID)).Scan(&errorCode, &processedNodes, &candidateCount); err != nil {
		t.Fatal(err)
	}
	if faulting.callCount() != 4 || errorCode != "SEMANTIC_PROVIDER_TIMEOUT" || processedNodes != 0 || candidateCount != 0 {
		t.Fatalf("calls=%d code=%s processed=%d candidates=%d", faulting.callCount(), errorCode, processedNodes, candidateCount)
	}
}

func newSemanticLinkRiverRuntime(t *testing.T, pool *pgxpool.Pool, wrappers ...func(graphapp.SemanticLinkTopicScanPageSource) graphapp.SemanticLinkTopicScanPageSource) (*graphapp.SemanticLinkScanCommandService, *workflowapplication.ExecutorRegistry, *workflowapplication.RuntimeCoordinator) {
	t.Helper()
	insertClient, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(insertClient)
	if err != nil {
		t.Fatal(err)
	}
	runtimeRepository, err := workflowpostgres.NewRuntimeRepository(pool, inserter, NewSemanticLinkScanCancellationGuard())
	if err != nil {
		t.Fatal(err)
	}
	scanRepository, err := NewSemanticLinkScanRepository(pool, runtimeRepository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	scanService, err := graphapp.NewSemanticLinkScanService(scanRepository, scanRepository)
	if err != nil {
		t.Fatal(err)
	}
	planner, err := NewSemanticLinkTopicScanPlanner(pool)
	if err != nil {
		t.Fatal(err)
	}
	command, err := graphapp.NewSemanticLinkScanCommandService(planner, scanService)
	if err != nil {
		t.Fatal(err)
	}
	graphRepository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	pageSource, err := NewSemanticLinkTopicScanPageRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	var source graphapp.SemanticLinkTopicScanPageSource = pageSource
	if len(wrappers) > 1 {
		t.Fatal("semantic link River test accepts at most one page source wrapper")
	}
	if len(wrappers) == 1 {
		source = wrappers[0](source)
	}
	writer, err := NewSemanticLinkDiscoveryCandidateWriter(pool, graphRepository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	pageExecutor, err := graphapp.NewSemanticLinkTopicScanExecutor(source, graphapp.NewSemanticLinkDiscoveryService(nil, nil), writer)
	if err != nil {
		t.Fatal(err)
	}
	scanExecutor, err := graphworkflow.NewSemanticLinkScanExecutor(scanRepository, scanService, pageExecutor, foundation.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := workflowapplication.NewValidationCatalog([]int{1}, capability.All())
	if err != nil {
		t.Fatal(err)
	}
	registry, err := workflowapplication.NewExecutorRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(graphapp.SemanticLinkScanNodeKind, graphapp.SemanticLinkScanInputSchemaVersion, scanExecutor); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	coordinator, err := workflowapplication.NewRuntimeCoordinator(runtimeRepository)
	if err != nil {
		t.Fatal(err)
	}
	return command, registry, coordinator
}

func seedSemanticLinkRiverFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (foundation.ID, foundation.ID) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	now := time.Now().UTC().Add(-time.Minute)
	workspaceID := seedGraphWorkspace(t, ctx, tx, now)
	provenance := seedGraphProvenance(t, ctx, tx, workspaceID, now)
	topicID := seedGraphTopic(t, ctx, tx, workspaceID, "River semantic links", "river semantic links", now)
	statement := "Durable semantic links preserve reviewed knowledge"
	first := seedGraphClaim(t, ctx, tx, workspaceID, statement, knowledge.ClaimStatusConfirmed, nil, now)
	second := seedGraphClaim(t, ctx, tx, workspaceID, statement, knowledge.ClaimStatusConfirmed, nil, now.Add(time.Microsecond))
	third := seedGraphClaim(t, ctx, tx, workspaceID, statement, knowledge.ClaimStatusConfirmed, nil, now.Add(2*time.Microsecond))
	for index, claimID := range []foundation.ID{first, second, third} {
		seedScanClaimSource(t, ctx, tx, workspaceID, claimID, provenance, index, now)
		seedScanMembership(t, ctx, tx, workspaceID, claimID, topicID, provenance, index, now)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return workspaceID, topicID
}

func waitForSemanticLinkRiverScan(ctx context.Context, pool *pgxpool.Pool, scanID foundation.ID, scanWant graphdomain.SemanticLinkScanStatus, runWant workflowdomain.RunStatus) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		var scanStatus graphdomain.SemanticLinkScanStatus
		var runStatus workflowdomain.RunStatus
		var errorCode *string
		err := pool.QueryRow(ctx, `
			SELECT scan.status,run.status,node.error_code
			FROM graph.semantic_link_scan scan
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

func assertSemanticLinkRiverFacts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, scanID foundation.ID, wantCandidates int, wantProcessed int64) {
	t.Helper()
	var candidates, formalRelations int
	var output []byte
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM graph.semantic_link_candidate WHERE workspace_id=$1 AND status='ACTIVE'`, string(workspaceID)).Scan(&candidates); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.relation WHERE workspace_id=$1 AND source_node_type='CLAIM' AND target_node_type='CLAIM'`, string(workspaceID)).Scan(&formalRelations); err != nil {
		t.Fatal(err)
	}
	var evidenceCount, distinctEvidenceIDs int
	if err := pool.QueryRow(ctx, `
		SELECT count(*),count(DISTINCT evidence.id)
		FROM graph.semantic_link_candidate_evidence evidence
		JOIN graph.semantic_link_candidate candidate ON candidate.id=evidence.candidate_id AND candidate.workspace_id=evidence.workspace_id
		WHERE candidate.workspace_id=$1`, string(workspaceID)).Scan(&evidenceCount, &distinctEvidenceIDs); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT node.output FROM workflow.node_run node JOIN graph.semantic_link_scan scan ON scan.workflow_run_id=node.run_id WHERE scan.id=$1`, string(scanID)).Scan(&output); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		ScanID         foundation.ID                      `json:"scan_id"`
		Status         graphdomain.SemanticLinkScanStatus `json:"status"`
		ProcessedCount int64                              `json:"processed_count"`
		CandidateCount int64                              `json:"candidate_count"`
	}
	if err := json.Unmarshal(output, &decoded); err != nil {
		t.Fatal(err)
	}
	if candidates != wantCandidates || evidenceCount != wantCandidates*2 || distinctEvidenceIDs != evidenceCount || formalRelations != 0 || decoded.ScanID != scanID || decoded.Status != graphdomain.SemanticLinkScanStatusSucceeded || decoded.ProcessedCount != wantProcessed || decoded.CandidateCount != int64(wantCandidates) {
		t.Fatalf("candidates=%d evidence=%d distinct_evidence=%d formal_relations=%d output=%+v", candidates, evidenceCount, distinctEvidenceIDs, formalRelations, decoded)
	}
}

type failFirstSemanticLinkCompletion struct {
	delegate *workflowapplication.RuntimeCoordinator
	mu       sync.Mutex
	calls    int
	first    json.RawMessage
	second   json.RawMessage
}

type blockingSemanticLinkPageSource struct {
	delegate graphapp.SemanticLinkTopicScanPageSource
	entered  chan struct{}
	once     sync.Once
}

func (source *blockingSemanticLinkPageSource) LoadPage(ctx context.Context, request graphapp.SemanticLinkTopicScanPageRequest) (graphapp.SemanticLinkTopicScanPage, error) {
	source.once.Do(func() { close(source.entered) })
	<-ctx.Done()
	return graphapp.SemanticLinkTopicScanPage{}, ctx.Err()
}

type faultingSemanticLinkPageSource struct {
	delegate          graphapp.SemanticLinkTopicScanPageSource
	remainingFailures int
	alwaysFail        bool
	mu                sync.Mutex
	calls             int
}

func (source *faultingSemanticLinkPageSource) LoadPage(ctx context.Context, request graphapp.SemanticLinkTopicScanPageRequest) (graphapp.SemanticLinkTopicScanPage, error) {
	source.mu.Lock()
	source.calls++
	fail := source.alwaysFail || source.remainingFailures > 0
	if source.remainingFailures > 0 {
		source.remainingFailures--
	}
	source.mu.Unlock()
	if fail {
		return graphapp.SemanticLinkTopicScanPage{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "SEMANTIC_PROVIDER_TIMEOUT", true, errors.New("injected semantic provider timeout"))
	}
	return source.delegate.LoadPage(ctx, request)
}

func (source *faultingSemanticLinkPageSource) callCount() int {
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.calls
}

func startSemanticLinkTestWorker(t *testing.T, ctx context.Context, pool *pgxpool.Pool, registry *workflowapplication.ExecutorRegistry, coordinator *workflowapplication.RuntimeCoordinator, owner string) *riveradapter.Client {
	t.Helper()
	runtimeWorker, err := riveradapter.NewRuntimeNodeWorker(registry, coordinator, owner, 10*time.Second, time.Second)
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

func stopSemanticLinkTestWorker(client *riveradapter.Client) {
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = client.Stop(ctx)
}

func (coordinator *failFirstSemanticLinkCompletion) Claim(ctx context.Context, command workflowapplication.ClaimCommand) (workflowapplication.ClaimResult, error) {
	return coordinator.delegate.Claim(ctx, command)
}

func (coordinator *failFirstSemanticLinkCompletion) Heartbeat(ctx context.Context, command workflowapplication.HeartbeatCommand) (workflowapplication.HeartbeatResult, error) {
	return coordinator.delegate.Heartbeat(ctx, command)
}

func (coordinator *failFirstSemanticLinkCompletion) Complete(ctx context.Context, command workflowapplication.CompleteDeliveryCommand) (workflowapplication.DeliveryTransitionResult, error) {
	coordinator.mu.Lock()
	coordinator.calls++
	call := coordinator.calls
	if call == 1 {
		coordinator.first = append(json.RawMessage(nil), command.Output...)
		coordinator.mu.Unlock()
		return workflowapplication.DeliveryTransitionResult{}, foundation.NewError(foundation.ErrorRetryableFailure, "SEMANTIC_LINK_WORKFLOW_COMPLETION_RESPONSE_LOST", true, errors.New("injected response loss before workflow completion"))
	}
	coordinator.second = append(json.RawMessage(nil), command.Output...)
	coordinator.mu.Unlock()
	return coordinator.delegate.Complete(ctx, command)
}

func (coordinator *failFirstSemanticLinkCompletion) Fail(ctx context.Context, command workflowapplication.FailDeliveryCommand) (workflowapplication.DeliveryTransitionResult, error) {
	return coordinator.delegate.Fail(ctx, command)
}

func (coordinator *failFirstSemanticLinkCompletion) outputsMatch() bool {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return coordinator.calls == 2 && string(coordinator.first) == string(coordinator.second)
}

var _ riveradapter.RuntimeExecutionCoordinator = (*failFirstSemanticLinkCompletion)(nil)
