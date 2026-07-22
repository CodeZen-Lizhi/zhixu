//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSemanticLinkScanRepositoryStartReplayConflictAdvanceAndFinish(t *testing.T) {
	ctx := context.Background()
	pool := newSemanticLinkScanTestPool(t, ctx)
	defer pool.Close()
	seedTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
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
	assertScanRuntimeFacts(t, ctx, pool, request.WorkspaceID, first.Scan.WorkflowRunID, first.Scan.ID)

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
	pool := newSemanticLinkScanTestPool(t, ctx)
	defer pool.Close()
	seedTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
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

	lossDB := scanCommitLossDB{pool: pool}
	lossRepo := newSemanticLinkScanTestRepositoryWithDB(t, ctx, lossDB, lossDB.pool)
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
	pool := newSemanticLinkScanTestPool(t, ctx)
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
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
	planner, err := NewSemanticLinkTopicScanPlanner(pool)
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
	pool := newSemanticLinkScanTestPool(t, ctx)
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
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
	pageSource, err := NewSemanticLinkTopicScanPageRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	candidateRepository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := NewSemanticLinkDiscoveryCandidateWriter(pool, candidateRepository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{})
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
	pool := newSemanticLinkScanTestPool(t, ctx)
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
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
	pageSource, err := NewSemanticLinkTopicScanPageRepository(pool)
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
	assertSemanticLinkTopicPairPlan(t, ctx, pool, workspaceID, topicID, claimIDs[:100])
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

type scanCommitLossDB struct{ pool *pgxpool.Pool }

func (d scanCommitLossDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return d.pool.QueryRow(ctx, sql, args...)
}

func (d scanCommitLossDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return d.pool.Query(ctx, sql, args...)
}

func (d scanCommitLossDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return d.pool.Exec(ctx, sql, args...)
}

func (d scanCommitLossDB) BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	tx, err := d.pool.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return scanCommitLossTx{Tx: tx}, nil
}

type scanCommitLossTx struct{ pgx.Tx }

func (tx scanCommitLossTx) Commit(ctx context.Context) error {
	if err := tx.Tx.Commit(ctx); err != nil {
		return err
	}
	return errors.New("injected scan commit response loss")
}

func newSemanticLinkScanTestPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newSemanticLinkScanTestRepository(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *SemanticLinkScanRepository {
	t.Helper()
	client, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(client)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workflowpostgres.NewRuntimeRepository(pool, inserter)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewSemanticLinkScanRepository(pool, runtime, foundation.NewUUIDGenerator(nil), foundation.FixedClock{Value: time.Date(2026, 7, 21, 3, 30, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func newSemanticLinkScanTestRepositoryWithDB(t *testing.T, ctx context.Context, db semanticLinkScanDB, runtimePool *pgxpool.Pool) *SemanticLinkScanRepository {
	t.Helper()
	client, err := riveradapter.NewClient(runtimePool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(client)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workflowpostgres.NewRuntimeRepository(runtimePool, inserter)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewSemanticLinkScanRepository(db, runtime, foundation.NewUUIDGenerator(nil), foundation.FixedClock{Value: time.Date(2026, 7, 21, 4, 30, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
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
