//go:build integration

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agenteino "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/eino"
	agentknowledge "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/knowledge"
	agentmemory "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/memory"
	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentretrieval "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/retrieval"
	agentworkflow "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/workflow"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/app"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	conversationpostgres "github.com/CodeZen-Lizhi/zhixu/internal/conversation/adapter/postgres"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationhttp "github.com/CodeZen-Lizhi/zhixu/internal/conversation/http"
	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	eventshttp "github.com/CodeZen-Lizhi/zhixu/internal/events/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgepostgres "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/adapter/postgres"
	knowledgeapplication "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	memoryapplication "github.com/CodeZen-Lizhi/zhixu/internal/memory/application"
	memorydomain "github.com/CodeZen-Lizhi/zhixu/internal/memory/domain"
	platformfilesystem "github.com/CodeZen-Lizhi/zhixu/internal/platform/filesystem"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	retrievalpostgres "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/postgres"
	retrievalworkspace "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/workspace"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	toolcatalog "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	toolretrieval "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/retrieval"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

const (
	ragSmokeWorkspaceID     foundation.ID = "a1400000-0000-4000-8000-000000000001"
	ragSmokeSourceID        foundation.ID = "a1400000-0000-4000-8000-000000000002"
	ragSmokeArtifactID      foundation.ID = "a1400000-0000-4000-8000-000000000003"
	ragSmokeSourceVersionID foundation.ID = "a1400000-0000-4000-8000-000000000004"
	ragSmokeProjectionID    foundation.ID = "a1400000-0000-4000-8000-000000000005"
	ragSmokeSpanID          foundation.ID = "a1400000-0000-4000-8000-000000000006"
	ragSmokeChunkID         foundation.ID = "a1400000-0000-4000-8000-000000000007"
	ragSmokeIndexID         foundation.ID = "a1400000-0000-4000-8000-000000000008"
	ragSmokeClaimID         foundation.ID = "a1400000-0000-4000-8000-000000000009"
	ragSmokeTopicID         foundation.ID = "a1400000-0000-4000-8000-000000000010"
	ragSmokeRelationID      foundation.ID = "a1400000-0000-4000-8000-000000000011"
	ragSmokeFinalAnswer                   = "Approved recovery requires replaying durable facts."
)

// TestPublicConversationRunsThroughRiverRAGAndFeedback proves the full durable product path without bypassing the finalizer.
func TestPublicConversationRunsThroughRiverRAGAndFeedback(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	runRAGConversationIntegration(t, baseURL, newRAGIntegrationScenario(t))
}

type ragIntegrationScenario struct {
	structuredScheduler agentapplication.StructuredPhaseScheduler
	ragScheduler        agentapplication.RAGExecutionScheduler
	tracking            *ragIntegrationSchedulerTracking
	expectedPhases      []agentdomain.ModelCallPhase
}

func newRAGIntegrationScenario(t *testing.T) ragIntegrationScenario {
	t.Helper()
	structured, err := agenteino.NewStructuredPhaseScheduler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rag, err := agenteino.NewRAGExecutionScheduler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tracking := &ragIntegrationSchedulerTracking{}
	return ragIntegrationScenario{
		structuredScheduler: &ragIntegrationTrackingStructuredScheduler{delegate: structured, tracking: tracking},
		ragScheduler:        &ragIntegrationTrackingRAGScheduler{delegate: rag, tracking: tracking},
		tracking:            tracking,
		expectedPhases: []agentdomain.ModelCallPhase{
			agentdomain.ModelCallPlan, agentdomain.ModelCallAgent,
			agentdomain.ModelCallAnswer, agentdomain.ModelCallInitial, agentdomain.ModelCallReview,
		},
	}
}

func runRAGConversationIntegration(
	t *testing.T,
	baseURL string,
	scenario ragIntegrationScenario,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	database := newMigratedWorkerTestPool(t, baseURL)
	pool := database.DB()
	seedRAGConversationKnowledge(t, ctx, database, t.TempDir())

	model := &ragRequestAwareModel{}
	router, workerClient, runtimeWorker, eventStore := newRAGConversationIntegrationRuntime(t, database, model, scenario)
	server := httptest.NewServer(router)
	defer server.Close()
	if err := workerClient.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		_ = workerClient.Stop(stopCtx)
	}()

	conversation := doRAGJSON(t, ctx, http.MethodPost, server.URL+"/api/v1/conversations", "rag-conversation-create", map[string]any{
		"workspace_id": ragSmokeWorkspaceID, "title": "RAG integration",
	}, http.StatusCreated)
	conversationID := mustRAGString(t, conversation, "id")
	watermark, err := eventStore.CurrentWatermark(ctx, ragSmokeWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}

	questionBody := map[string]any{
		"workspace_id": ragSmokeWorkspaceID, "question": "What does the approved recovery evidence require?",
		"scope": map[string]any{"retrieval_mode": "keyword"}, "answer_depth": "standard", "output_format": "markdown",
	}
	accepted := doRAGJSON(t, ctx, http.MethodPost, server.URL+"/api/v1/conversations/"+conversationID+"/questions", "rag-question-once", questionBody, http.StatusAccepted)
	answerID := mustRAGNestedString(t, accepted, "answer", "id")
	waitForRAGAnswerHTTP(t, ctx, pool, model, server.URL, answerID)
	answer := doRAGGET(t, ctx, server.URL+"/api/v1/answers/"+answerID+"?workspace_id="+string(ragSmokeWorkspaceID), http.StatusOK)
	assertCompletedRAGAnswer(t, answer)
	workflowRunID := foundation.ID(mustRAGNestedString(t, answer, "workflow", "run_id"))
	waitForRAGWorkflowCompleted(t, ctx, pool, workflowRunID)
	assertRAGWorkflowAudit(t, ctx, pool, workflowRunID, scenario.expectedPhases)

	providerCalls := model.CallCount()
	if providerCalls != len(scenario.expectedPhases) {
		t.Fatalf("provider calls=%d want=%d phases=%v", providerCalls, len(scenario.expectedPhases), scenario.expectedPhases)
	}
	replayed := doRAGJSON(t, ctx, http.MethodPost, server.URL+"/api/v1/conversations/"+conversationID+"/questions", "rag-question-once", questionBody, http.StatusOK)
	if mustRAGNestedString(t, replayed, "answer", "id") != answerID || model.CallCount() != providerCalls {
		t.Fatalf("exact replay changed answer or repeated provider: replay=%v calls=%d", replayed, model.CallCount())
	}
	beforeRedelivery := ragKnowledgeAndAnswerSnapshot(t, ctx, pool, answerID)
	redeliverCompletedRAGNode(t, ctx, pool, runtimeWorker, workflowRunID)
	afterRedelivery := ragKnowledgeAndAnswerSnapshot(t, ctx, pool, answerID)
	if beforeRedelivery != afterRedelivery || model.CallCount() != providerCalls {
		t.Fatalf("River redelivery changed durable result or repeated provider: before=%+v after=%+v calls=%d", beforeRedelivery, afterRedelivery, model.CallCount())
	}
	if scenario.tracking != nil && (scenario.tracking.structuredCalls.Load() != 1 || scenario.tracking.ragCalls.Load() != 1) {
		t.Fatalf("Eino scheduler calls structured=%d rag=%d want=1/1", scenario.tracking.structuredCalls.Load(), scenario.tracking.ragCalls.Load())
	}
	assertRAGWorkflowAudit(t, ctx, pool, workflowRunID, scenario.expectedPhases)
	assertRAGEinoRuntimeArtifacts(t, ctx, pool, workflowRunID, foundation.ID(answerID))
	assertRAGDraftStreamEnd(t, ctx, server.URL, answerID)

	assertRAGSSEReplay(t, ctx, server.URL, watermark, answerID)
	before := ragKnowledgeAndAnswerSnapshot(t, ctx, pool, answerID)
	citationID := mustRAGCitationID(t, answer)
	feedbackBody := map[string]any{"workspace_id": ragSmokeWorkspaceID, "feedback_type": "irrelevant_citation", "citation_id": citationID, "comment": "integration evaluation signal"}
	feedback := doRAGJSON(t, ctx, http.MethodPost, server.URL+"/api/v1/answers/"+answerID+"/feedback", "rag-feedback-once", feedbackBody, http.StatusCreated)
	if mustRAGString(t, feedback, "answer_id") != answerID {
		t.Fatalf("feedback=%v", feedback)
	}
	doRAGJSON(t, ctx, http.MethodPost, server.URL+"/api/v1/answers/"+answerID+"/feedback", "rag-feedback-once", feedbackBody, http.StatusOK)
	after := ragKnowledgeAndAnswerSnapshot(t, ctx, pool, answerID)
	if before.answerHash != after.answerHash || before.claimCount != after.claimCount || before.topicCount != after.topicCount || before.feedbackCount != 0 || after.feedbackCount != 1 {
		t.Fatalf("feedback mutated answer/knowledge or duplicated: before=%+v after=%+v", before, after)
	}
}

type ragIntegrationSchedulerTracking struct {
	structuredCalls atomic.Int64
	ragCalls        atomic.Int64
}

type ragIntegrationTrackingStructuredScheduler struct {
	delegate agentapplication.StructuredPhaseScheduler
	tracking *ragIntegrationSchedulerTracking
}

func (scheduler *ragIntegrationTrackingStructuredScheduler) Schedule(ctx context.Context, run *agentapplication.StructuredPhaseRun) error {
	scheduler.tracking.structuredCalls.Add(1)
	return scheduler.delegate.Schedule(ctx, run)
}

type ragIntegrationTrackingRAGScheduler struct {
	delegate agentapplication.RAGExecutionScheduler
	tracking *ragIntegrationSchedulerTracking
}

func (scheduler *ragIntegrationTrackingRAGScheduler) Schedule(ctx context.Context, run *agentapplication.RAGPhaseRun) error {
	scheduler.tracking.ragCalls.Add(1)
	return scheduler.delegate.Schedule(ctx, run)
}

func newRAGConversationIntegrationRuntime(
	t *testing.T,
	pool *platformpostgres.Pool,
	model *ragRequestAwareModel,
	scenario ragIntegrationScenario,
) (http.Handler, *riveradapter.Client, *riveradapter.RuntimeNodeWorker, *eventspostgres.GORMStore) {
	t.Helper()
	events, err := eventspostgres.NewGORMStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	conversationRepository, err := conversationpostgres.NewGORMRepository(pool, events)
	if err != nil {
		t.Fatal(err)
	}
	agentRepository, err := agentpostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	memoryRepository := newWorkerTestMemoryRepository(t, pool)
	memoryService, err := memoryapplication.NewService(memoryapplication.Dependencies{
		Repository: memoryRepository,
		IDs:        foundation.NewUUIDGenerator(nil),
		Clock:      foundation.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	memoryOwner := memorydomain.SingleUserOwner()
	memoryLoader, err := agentmemory.NewLoader(memoryService, memoryOwner)
	if err != nil {
		t.Fatal(err)
	}
	knowledgeRepository, err := knowledgepostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	eligibility, err := knowledgeapplication.NewEvidenceEligibilityService(knowledgeRepository)
	if err != nil {
		t.Fatal(err)
	}
	formalClaims, err := knowledgeapplication.NewFormalClaimReader(knowledgeRepository)
	if err != nil {
		t.Fatal(err)
	}
	knowledgeAdapter, err := agentknowledge.NewAdapter(eligibility, formalClaims)
	if err != nil {
		t.Fatal(err)
	}
	workspaceRepository, err := workspacepostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	searchRepository, err := retrievalpostgres.NewGORMSearchRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	searchService, err := retrievalapplication.NewSearchService(searchRepository, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	artifactReader, err := retrievalworkspace.NewReader(workspaceRepository, platformfilesystem.Scanner{Options: platformfilesystem.ScanOptions{MaxBytes: platformfilesystem.DefaultMaxBytes}})
	if err != nil {
		t.Fatal(err)
	}
	evidenceReference, err := retrievalapplication.NewEvidenceReferenceService(searchRepository, artifactReader)
	if err != nil {
		t.Fatal(err)
	}
	retrievalAdapter, err := agentretrieval.NewAdapter(searchService, evidenceReference)
	if err != nil {
		t.Fatal(err)
	}
	topicService, err := knowledgeapplication.NewEvidenceTopicService(knowledgeRepository)
	if err != nil {
		t.Fatal(err)
	}
	topicAdapter, err := agentknowledge.NewTopicAdapter(topicService)
	if err != nil {
		t.Fatal(err)
	}
	finalizer, err := conversationpostgres.NewGORMAnswerFinalizer(pool, agentRepository, events, foundation.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	progress, err := agentpostgres.NewGORMRAGProgressStore(pool, events)
	if err != nil {
		t.Fatal(err)
	}
	modelRef := agentdomain.ModelRef{AdapterName: "integration", AdapterVersion: "v1", ModelID: "request-aware", ModelVersion: "v1"}
	catalog, err := agentworkflow.NewRuntimeCatalog(agentworkflow.CatalogOptions{Model: modelRef, Timeout: 10 * time.Second, MaxOutputTokens: 2048})
	if err != nil {
		t.Fatal(err)
	}
	draftStreams, err := conversationpostgres.NewGORMDraftStreamRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	agentRuntime, err := agenteino.NewAgentRuntime(model)
	if err != nil {
		t.Fatal(err)
	}
	answerStream, err := agenteino.NewAnswerStreamRuntime(model)
	if err != nil {
		t.Fatal(err)
	}
	toolContracts, err := toolcatalog.NewFrozenContractRegistry()
	if err != nil {
		t.Fatal(err)
	}
	readSourceExecutor, err := toolretrieval.NewReadSourceExecutor(evidenceReference)
	if err != nil {
		t.Fatal(err)
	}
	validateCitationExecutor, err := toolretrieval.NewValidateCitationExecutor(evidenceReference, eligibility)
	if err != nil {
		t.Fatal(err)
	}
	executionRegistry := toolsapplication.NewExecutionRegistry()
	for _, item := range []struct {
		ref      toolsdomain.ToolRef
		executor toolsapplication.Executor
	}{
		{ref: toolsdomain.ToolRef{Name: "ReadSource", Version: 2}, executor: readSourceExecutor},
		{ref: toolsdomain.ToolRef{Name: "ValidateCitation", Version: 2}, executor: validateCitationExecutor},
	} {
		contract, resolveErr := toolContracts.ResolveContract(item.ref)
		if resolveErr != nil {
			t.Fatal(resolveErr)
		}
		if err := executionRegistry.RegisterContract(contract); err != nil {
			t.Fatal(err)
		}
		if err := executionRegistry.RegisterExecutor(item.ref, item.executor); err != nil {
			t.Fatal(err)
		}
	}
	if err := executionRegistry.Freeze(); err != nil {
		t.Fatal(err)
	}
	toolRepository := newWorkerTestToolRepository(t, pool)
	toolExecution, err := toolsapplication.NewExecutionService(
		executionRegistry, toolRepository, toolRepository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
	)
	if err != nil {
		t.Fatal(err)
	}
	ragExecutor, err := agentworkflow.NewRAGWorkflowExecutor(agentworkflow.RAGWorkflowExecutorDependencies{
		Model: model, Scheduler: scenario.structuredScheduler, RAGScheduler: scenario.ragScheduler,
		Catalog: catalog, Repository: agentRepository, Snapshots: agentRepository,
		Memory: memoryLoader, MemoryOwner: agentapplication.MemoryOwnerRef{Kind: string(memoryOwner.Kind), ID: memoryOwner.ID},
		Context: conversationRepository,
		Search:  retrievalAdapter, Retrieval: retrievalAdapter, Eligibility: knowledgeAdapter, Topics: topicAdapter,
		Finalizer: finalizer, Progress: progress, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}, Budget: agentapplication.DefaultRunBudget(),
		AgentRuntime: agentRuntime, AnswerStream: answerStream, ToolContracts: toolContracts, ToolExecution: toolExecution,
		DraftStreams: draftStreams,
	})
	if err != nil {
		t.Fatal(err)
	}
	validation, err := workflowapplication.NewValidationCatalog([]int{1}, capability.All())
	if err != nil {
		t.Fatal(err)
	}
	executors, err := workflowapplication.NewExecutorRegistry(validation)
	if err != nil {
		t.Fatal(err)
	}
	if err := executors.Register(agentworkflow.RAGWorkflowNodeKind, agentworkflow.RAGWorkflowInputSchemaVersion, ragExecutor); err != nil {
		t.Fatal(err)
	}
	if err := executors.Freeze(); err != nil {
		t.Fatal(err)
	}
	runtimeRepository, err := workflowpostgres.NewGORMRuntimeRepositoryWithHooks(
		pool, riveradapter.DefaultOptions(), riveradapter.NewStaticScopedEnqueueFence(), workflowpostgres.GORMRuntimeRepositoryHooks{},
	)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := workflowapplication.NewRuntimeCoordinator(runtimeRepository)
	if err != nil {
		t.Fatal(err)
	}
	runtimeWorker, err := riveradapter.NewRuntimeNodeWorker(executors, coordinator, "rag-conversation-integration", 10*time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	workers := riveradapter.NewWorkers()
	if err := riveradapter.AddRuntimeWorkerSafely(workers, runtimeWorker); err != nil {
		t.Fatal(err)
	}
	workerClient, err := riveradapter.NewClient(pool.DB(), workers)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := conversationpostgres.NewGORMQuestionDispatcher(pool, runtimeRepository, events, foundation.NewUUIDGenerator(nil), foundation.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := conversationapplication.NewService(conversationapplication.Dependencies{Repository: conversationRepository, QuestionDispatcher: dispatcher, FeedbackRepository: conversationRepository, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	conversationHandler := conversationhttp.NewHandler(service, conversationhttp.NewCursorCodec())
	draftStreamHandler := conversationhttp.NewDraftStreamHandler(draftStreams, conversationhttp.DraftStreamConfig{PollInterval: 10 * time.Millisecond, HeartbeatInterval: 100 * time.Millisecond})
	eventHandler := eventshttp.NewHandler(events, eventshttp.StreamConfig{PollInterval: 10 * time.Millisecond, HeartbeatInterval: 100 * time.Millisecond})
	router := app.NewRouter(app.Dependencies{Version: "rag-integration", Database: pool, Conversation: conversationHandler, DraftStream: draftStreamHandler, Events: eventHandler, RAGEnabled: true, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	return router, workerClient, runtimeWorker, events
}

func assertRAGWorkflowAudit(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workflowRunID foundation.ID,
	expectedPhases []agentdomain.ModelCallPhase,
) {
	t.Helper()
	var modelRunStatus, finalResultType, phases, callStatuses string
	var callCount int
	if err := pool.QueryRow(ctx, `
		SELECT run.status,COALESCE(run.final_result_type,''),count(call.id),
			COALESCE(string_agg(call.phase::text,',' ORDER BY call.call_no),''),
			COALESCE(string_agg(call.status::text,',' ORDER BY call.call_no),'')
		FROM agent.model_run run
		LEFT JOIN agent.model_call call ON call.model_run_id=run.id
		WHERE run.workflow_run_id=$1
		GROUP BY run.id,run.status,run.final_result_type`, string(workflowRunID)).Scan(
		&modelRunStatus, &finalResultType, &callCount, &phases, &callStatuses,
	); err != nil {
		t.Fatal(err)
	}
	wantPhaseValues := make([]string, len(expectedPhases))
	wantStatusValues := make([]string, len(expectedPhases))
	for index, phase := range expectedPhases {
		wantPhaseValues[index] = string(phase)
		wantStatusValues[index] = string(agentdomain.ModelCallSucceeded)
	}
	wantPhases := strings.Join(wantPhaseValues, ",")
	wantCallStatuses := strings.Join(wantStatusValues, ",")
	if modelRunStatus != string(agentdomain.ModelRunSucceeded) || finalResultType != agentdomain.ResultTypeRAGAnswer ||
		callCount != len(expectedPhases) || phases != wantPhases || callStatuses != wantCallStatuses {
		t.Fatalf("model audit status=%s result=%s calls=%d phases=%s call_statuses=%s", modelRunStatus, finalResultType, callCount, phases, callStatuses)
	}

	var runStatus, nodeStatus, attemptStatus string
	var attemptCount int
	if err := pool.QueryRow(ctx, `
		SELECT run.status,node.status,attempt.status,count(*) OVER ()
		FROM workflow.run run
		JOIN workflow.node_run node ON node.run_id=run.id
		JOIN workflow.node_attempt attempt ON attempt.node_run_id=node.id
		WHERE run.id=$1
		ORDER BY attempt.attempt_no DESC
		LIMIT 1`, string(workflowRunID)).Scan(&runStatus, &nodeStatus, &attemptStatus, &attemptCount); err != nil {
		t.Fatal(err)
	}
	if runStatus != string(workflowdomain.RunStatusSucceeded) || nodeStatus != string(workflowdomain.NodeStatusSucceeded) ||
		attemptStatus != string(workflowdomain.AttemptStatusSucceeded) || attemptCount != 1 {
		t.Fatalf("workflow audit run=%s node=%s attempt=%s attempt_count=%d", runStatus, nodeStatus, attemptStatus, attemptCount)
	}
}

func waitForRAGWorkflowCompleted(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workflowRunID foundation.ID) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		var status string
		if err := pool.QueryRow(ctx, `SELECT status FROM workflow.run WHERE id=$1`, string(workflowRunID)).Scan(&status); err != nil {
			t.Fatal(err)
		}
		switch workflowdomain.RunStatus(status) {
		case workflowdomain.RunStatusSucceeded:
			return
		case workflowdomain.RunStatusFailed, workflowdomain.RunStatusCancelled:
			t.Fatalf("workflow %s terminated as %s after answer publication", workflowRunID, status)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
}

func assertRAGEinoRuntimeArtifacts(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workflowRunID foundation.ID,
	answerID foundation.ID,
) {
	t.Helper()
	var toolName, toolStatus, responseHash, resultRef string
	var toolVersion int64
	var callNo, toolCallCount int
	var responseBytes int64
	if err := pool.QueryRow(ctx, `
		SELECT requested_tool_name,tool_version,status,response_hash,response_bytes,result_ref,call_no,count(*) OVER ()
		FROM workflow.tool_call WHERE workflow_run_id=$1`, string(workflowRunID)).Scan(
		&toolName, &toolVersion, &toolStatus, &responseHash, &responseBytes, &resultRef, &callNo, &toolCallCount,
	); err != nil {
		t.Fatal(err)
	}
	if toolName != "ReadSource" || toolVersion != 2 || toolStatus != string(toolsdomain.CallSucceeded) ||
		len(responseHash) != 64 || responseBytes < 1 || resultRef != "source-span:"+string(ragSmokeSpanID) || callNo != 1 || toolCallCount != 1 {
		t.Fatalf("tool call name=%s version=%d status=%s hash=%s bytes=%d result=%s call_no=%d count=%d", toolName, toolVersion, toolStatus, responseHash, responseBytes, resultRef, callNo, toolCallCount)
	}

	var draftStatus, draftContent string
	var chunkCount int
	if err := pool.QueryRow(ctx, `
		SELECT session.status,count(chunk.sequence),COALESCE(string_agg(chunk.content,'' ORDER BY chunk.sequence),'')
		FROM agent.answer_draft_session AS session
		LEFT JOIN agent.answer_draft_chunk AS chunk ON chunk.session_id=session.id
		WHERE session.workspace_id=$1 AND session.answer_id=$2
		GROUP BY session.id,session.status,session.generation
		ORDER BY session.generation DESC LIMIT 1`, string(ragSmokeWorkspaceID), string(answerID)).Scan(
		&draftStatus, &chunkCount, &draftContent,
	); err != nil {
		t.Fatal(err)
	}
	if draftStatus != string(agentapplication.DraftStreamPublished) || chunkCount < 2 || draftContent != ragSmokeFinalAnswer {
		t.Fatalf("draft status=%s chunks=%d content=%q", draftStatus, chunkCount, draftContent)
	}
}

func assertRAGDraftStreamEnd(t *testing.T, ctx context.Context, serverURL, answerID string) {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		serverURL+"/api/v1/answers/"+answerID+"/stream?workspace_id="+string(ragSmokeWorkspaceID), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "event: end") ||
		!strings.Contains(string(body), `"status":"PUBLISHED"`) || !strings.Contains(string(body), `"action":"refetch"`) {
		t.Fatalf("draft SSE status=%d body=%s", response.StatusCode, body)
	}
}

func redeliverCompletedRAGNode(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	worker *riveradapter.RuntimeNodeWorker,
	workflowRunID foundation.ID,
) {
	t.Helper()
	if worker == nil {
		t.Fatal("runtime worker is nil")
	}
	var nodeRunID foundation.ID
	var dispatchNo, riverJobAttempt int
	var riverJobID int64
	if err := pool.QueryRow(ctx, `
		SELECT node.id,node.dispatch_no,attempt.river_job_id,attempt.river_job_attempt
		FROM workflow.node_run node
		JOIN workflow.node_attempt attempt ON attempt.node_run_id=node.id
		WHERE node.run_id=$1
		ORDER BY attempt.attempt_no DESC
		LIMIT 1`, string(workflowRunID)).Scan(&nodeRunID, &dispatchNo, &riverJobID, &riverJobAttempt); err != nil {
		t.Fatal(err)
	}
	job := &river.Job[riveradapter.NodeJobArgs]{
		JobRow: &rivertype.JobRow{ID: riverJobID, Attempt: riverJobAttempt + 1},
		Args: riveradapter.NodeJobArgs{
			SchemaVersion: riveradapter.NodeJobSchemaVersion,
			NodeRunID:     nodeRunID,
			DispatchNo:    dispatchNo,
		},
	}
	if err := worker.Work(ctx, job); err != nil {
		t.Fatalf("redeliver completed RAG node: %v", err)
	}
}

type ragRequestAwareModel struct {
	mu            sync.Mutex
	calls         int
	generateCalls int
}

func (model *ragRequestAwareModel) Chat(_ context.Context, request agentapplication.ChatRequest) (agentapplication.ChatResponse, error) {
	model.mu.Lock()
	model.calls++
	model.mu.Unlock()
	var modelRunRef foundation.ID
	metadataV2 := request.Phase == agentdomain.ModelCallInitial &&
		request.SchemaRef == (agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataSchemaID, Version: agentdomain.OutputSchemaVersionV2})
	planV2 := request.Phase == agentdomain.ModelCallPlan &&
		request.SchemaRef == (agentdomain.SchemaRef{ID: agentdomain.RAGQueryPlanSchemaID, Version: agentdomain.OutputSchemaVersionV2})
	if !metadataV2 && !planV2 {
		var err error
		modelRunRef, err = ragModelRunRef(request)
		if err != nil {
			return agentapplication.ChatResponse{}, err
		}
	}
	var output any
	switch request.Phase {
	case agentdomain.ModelCallPlan:
		if planV2 {
			output = agentdomain.RAGQueryPlanProviderResultV2{Intent: "approved recovery behavior", Rewrites: []string{"approved recovery"}, SuggestedScopes: []string{}}
		} else {
			output = agentdomain.RAGQueryPlanResult{ResultType: agentdomain.ResultTypeRAGQueryPlan, SchemaID: agentdomain.RAGQueryPlanSchemaID, SchemaVersion: agentdomain.OutputSchemaVersionV1, ModelRunRef: modelRunRef, Payload: agentdomain.RAGQueryPlanPayload{Intent: "approved recovery behavior", Rewrites: []string{"approved recovery"}, SuggestedScopes: []string{}}}
		}
	case agentdomain.ModelCallInitial:
		if request.SchemaRef.ID == agentdomain.RAGAnswerMetadataSchemaID {
			switch request.SchemaRef.Version {
			case agentdomain.OutputSchemaVersionV1:
				topicID, topicName, citationID, err := ragAllowedTopicFromRequest(request)
				if err != nil {
					return agentapplication.ChatResponse{}, err
				}
				assertions := []agentdomain.Assertion{{ID: "recovery-assertion", Text: "Recovery replays durable facts without duplicating provider work.", Kind: agentdomain.AssertionFactual, CitationIDs: []string{citationID}}}
				citations := []agentdomain.Citation{{ID: citationID, WorkspaceID: ragSmokeWorkspaceID, IndexVersionID: ragSmokeIndexID, ChunkID: ragSmokeChunkID, SourceVersionID: ragSmokeSourceVersionID, SourceSpanID: ragSmokeSpanID}}
				topics := []agentdomain.RelatedTopic{{TopicID: topicID, Name: topicName, CitationIDs: []string{citationID}}}
				answerSHA, err := ragAnswerSHAFromRequest(request)
				if err != nil {
					return agentapplication.ChatResponse{}, err
				}
				output = agentdomain.RAGAnswerMetadataResult{
					ResultType: agentdomain.ResultTypeRAGAnswerMetadata, SchemaID: agentdomain.RAGAnswerMetadataSchemaID,
					SchemaVersion: agentdomain.OutputSchemaVersionV1, ModelRunRef: modelRunRef,
					Payload: agentdomain.RAGAnswerMetadataPayload{
						AnswerSHA256: answerSHA, Assertions: assertions, Citations: citations,
						ConflictPositions: []agentdomain.ConflictPosition{}, RelatedTopics: topics,
						FollowUpQuestions: []string{"How is replay kept idempotent?"},
					},
				}
			case agentdomain.OutputSchemaVersionV2:
				refs, err := ragMetadataReferencesFromRequest(request)
				if err != nil {
					return agentapplication.ChatResponse{}, err
				}
				positions := make([]agentdomain.RAGAnswerMetadataConflictPositionV2, 0, len(refs.conflicts))
				for _, conflictRef := range refs.conflicts {
					positions = append(positions, agentdomain.RAGAnswerMetadataConflictPositionV2{
						ConflictRef: conflictRef, Position: "The supplied approved evidence describes this disputed position.",
					})
				}
				conflictSummary := ""
				if len(positions) > 0 {
					conflictSummary = "The approved evidence contains multiple disputed positions."
				}
				output = agentdomain.RAGAnswerMetadataResultV2{
					ResultType: agentdomain.ResultTypeRAGAnswerMetadata, SchemaID: agentdomain.RAGAnswerMetadataSchemaID,
					SchemaVersion: agentdomain.OutputSchemaVersionV2,
					Payload: agentdomain.RAGAnswerMetadataPayloadV2{
						Assertions: []agentdomain.RAGAnswerMetadataAssertionV2{{
							ID: "recovery-assertion", Text: "Recovery replays durable facts without duplicating provider work.",
							Kind: agentdomain.AssertionFactual, EvidenceRefs: refs.evidence,
						}},
						ConflictPositions: positions, ConflictSummary: conflictSummary,
						RelatedTopicRefs: refs.topics, FollowUpQuestions: []string{"How is replay kept idempotent?"},
					},
				}
			default:
				return agentapplication.ChatResponse{}, fmt.Errorf("unsupported rag metadata schema version %q", request.SchemaRef.Version)
			}
		} else {
			topicID, topicName, citationID, err := ragAllowedTopicFromRequest(request)
			if err != nil {
				return agentapplication.ChatResponse{}, err
			}
			assertions := []agentdomain.Assertion{{ID: "recovery-assertion", Text: "Recovery replays durable facts without duplicating provider work.", Kind: agentdomain.AssertionFactual, CitationIDs: []string{citationID}}}
			citations := []agentdomain.Citation{{ID: citationID, WorkspaceID: ragSmokeWorkspaceID, IndexVersionID: ragSmokeIndexID, ChunkID: ragSmokeChunkID, SourceVersionID: ragSmokeSourceVersionID, SourceSpanID: ragSmokeSpanID}}
			topics := []agentdomain.RelatedTopic{{TopicID: topicID, Name: topicName, CitationIDs: []string{citationID}}}
			output = agentdomain.RAGAnswerResultV2{
				ResultType: agentdomain.ResultTypeRAGAnswer, SchemaID: agentdomain.RAGAnswerSchemaID,
				SchemaVersion: agentdomain.OutputSchemaVersionV2, ModelRunRef: modelRunRef,
				Payload: agentdomain.RAGAnswerPayloadV2{
					RAGAnswerPayload: agentdomain.RAGAnswerPayload{
						Conclusion: ragSmokeFinalAnswer, Assertions: assertions, Citations: citations,
						ConflictPositions: []agentdomain.ConflictPosition{},
					},
					RelatedTopics: topics, FollowUpQuestions: []string{"How is replay kept idempotent?"},
				},
			}
		}
	case agentdomain.ModelCallReview:
		citationID, err := ragCitationIDFromRequest(request)
		if err != nil {
			return agentapplication.ChatResponse{}, err
		}
		output = agentdomain.FaithfulnessReviewResult{ResultType: agentdomain.ResultTypeFaithfulnessReview, SchemaID: agentdomain.FaithfulnessReviewSchemaID, SchemaVersion: agentdomain.OutputSchemaVersionV1, ModelRunRef: modelRunRef, Payload: agentdomain.FaithfulnessReviewPayload{Passed: true, Summary: "All factual statements are supported.", Items: []agentdomain.FaithfulnessReviewItem{
			{AssertionID: "recovery-assertion", Verdict: agentdomain.FaithfulnessSupported, CitationIDs: []string{citationID}, Reason: "The approved excerpt states the recovery invariant."},
			{AssertionID: "@answer/conclusion", Verdict: agentdomain.FaithfulnessSupported, CitationIDs: []string{citationID}, Reason: "The conclusion is supported by the same approved excerpt."},
		}}}
	default:
		return agentapplication.ChatResponse{}, fmt.Errorf("unexpected RAG model phase %q", request.Phase)
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return agentapplication.ChatResponse{}, err
	}
	return agentapplication.ChatResponse{Model: request.Model, Content: encoded, Usage: agentdomain.TokenUsage{InputTokens: 20, OutputTokens: 10, TotalTokens: 30}}, nil
}

func (model *ragRequestAwareModel) Generate(_ context.Context, messages []*schema.Message, options ...einomodel.Option) (*schema.Message, error) {
	model.mu.Lock()
	model.calls++
	model.generateCalls++
	callNo := model.generateCalls
	model.mu.Unlock()
	if callNo != 1 {
		return nil, fmt.Errorf("unexpected Eino Agent Generate call %d", callNo)
	}
	resolved := einomodel.GetCommonOptions(nil, options...)
	if len(messages) != 2 || len(resolved.Tools) != 2 {
		return nil, fmt.Errorf("first Eino Agent call messages=%d tools=%d", len(messages), len(resolved.Tools))
	}
	return &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			ID: "rag-read-source-1", Type: "function",
			Function: schema.FunctionCall{
				Name:      "ReadSource",
				Arguments: `{"evidence_ref":"E1"}`,
			},
		}},
		ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls", Usage: &schema.TokenUsage{PromptTokens: 20, CompletionTokens: 5, TotalTokens: 25}},
	}, nil
}

func (model *ragRequestAwareModel) Stream(_ context.Context, messages []*schema.Message, options ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	model.mu.Lock()
	model.calls++
	model.mu.Unlock()
	resolved := einomodel.GetCommonOptions(nil, options...)
	if len(messages) != 2 || resolved.ToolChoice == nil || *resolved.ToolChoice != schema.ToolChoiceForbidden {
		return nil, fmt.Errorf("unexpected Eino answer stream messages=%d options=%+v", len(messages), resolved)
	}
	reader, writer := schema.Pipe[*schema.Message](1)
	go func() {
		defer writer.Close()
		writer.Send(&schema.Message{Role: schema.Assistant, Content: "Approved recovery requires "}, nil)
		// Keep the Provider frames distinct long enough to exercise two durable chunks.
		time.Sleep(100 * time.Millisecond)
		writer.Send(&schema.Message{Content: "replaying durable facts."}, nil)
		writer.Send(&schema.Message{ResponseMeta: &schema.ResponseMeta{
			FinishReason: "stop", Usage: &schema.TokenUsage{PromptTokens: 25, CompletionTokens: 8, TotalTokens: 33},
		}}, nil)
	}()
	return reader, nil
}

func (model *ragRequestAwareModel) WithTools(tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	if len(tools) != 2 || tools[0] == nil || tools[1] == nil ||
		tools[0].Name != "ReadSource" || tools[1].Name != "ValidateCitation" {
		return nil, fmt.Errorf("unexpected Eino Agent tool set")
	}
	return model, nil
}

func (model *ragRequestAwareModel) CallCount() int {
	model.mu.Lock()
	defer model.mu.Unlock()
	return model.calls
}

var _ einomodel.ToolCallingChatModel = (*ragRequestAwareModel)(nil)

func ragModelRunRef(request agentapplication.ChatRequest) (foundation.ID, error) {
	for index := len(request.Messages) - 1; index >= 0; index-- {
		if found := findRAGString(request.Messages[index].Content, "model_run_ref"); found != "" {
			return foundation.ParseID(found)
		}
	}
	return "", fmt.Errorf("model_run_ref missing from phase %q", request.Phase)
}

func ragAnswerSHAFromRequest(request agentapplication.ChatRequest) (string, error) {
	for index := len(request.Messages) - 1; index >= 0; index-- {
		if found := findRAGString(request.Messages[index].Content, "answer_sha256"); len(found) == 64 {
			return found, nil
		}
	}
	return "", fmt.Errorf("answer_sha256 missing from phase %q", request.Phase)
}

type ragMetadataReferences struct {
	evidence  []string
	conflicts []string
	topics    []string
}

func ragMetadataReferencesFromRequest(request agentapplication.ChatRequest) (ragMetadataReferences, error) {
	for index := len(request.Messages) - 1; index >= 0; index-- {
		references, found, err := findRAGMetadataReferences(decodeRAGMessageJSON(request.Messages[index].Content))
		if err != nil {
			return ragMetadataReferences{}, err
		}
		if found {
			return references, nil
		}
	}
	return ragMetadataReferences{}, fmt.Errorf("metadata aliases missing from phase %q", request.Phase)
}

func findRAGMetadataReferences(value any) (ragMetadataReferences, bool, error) {
	switch typed := value.(type) {
	case map[string]any:
		evidence, evidenceOK := typed["evidence"]
		conflicts, conflictsOK := typed["conflicts"]
		topics, topicsOK := typed["related_topics"]
		if evidenceOK && conflictsOK && topicsOK {
			evidenceRefs, err := ragMetadataAliasList(evidence, "E", true)
			if err != nil {
				return ragMetadataReferences{}, true, err
			}
			conflictRefs, err := ragMetadataAliasList(conflicts, "C", false)
			if err != nil || len(conflictRefs) == 1 {
				return ragMetadataReferences{}, true, fmt.Errorf("metadata conflict aliases are invalid")
			}
			topicRefs, err := ragMetadataAliasList(topics, "T", true)
			if err != nil {
				return ragMetadataReferences{}, true, err
			}
			return ragMetadataReferences{evidence: evidenceRefs, conflicts: conflictRefs, topics: topicRefs}, true, nil
		}
		for _, nested := range typed {
			if references, found, err := findRAGMetadataReferences(nested); found || err != nil {
				return references, found, err
			}
		}
	case []any:
		for _, nested := range typed {
			if references, found, err := findRAGMetadataReferences(nested); found || err != nil {
				return references, found, err
			}
		}
	}
	return ragMetadataReferences{}, false, nil
}

func ragMetadataAliasList(raw any, prefix string, required bool) ([]string, error) {
	items, ok := raw.([]any)
	if !ok || (required && len(items) == 0) {
		return nil, fmt.Errorf("metadata %s aliases are missing", prefix)
	}
	refs := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("metadata %s alias is invalid", prefix)
		}
		ref, _ := item["ref"].(string)
		if !ragMetadataAlias(ref, prefix) {
			return nil, fmt.Errorf("metadata %s alias is invalid", prefix)
		}
		if _, duplicate := seen[ref]; duplicate {
			return nil, fmt.Errorf("metadata %s alias is duplicated", prefix)
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	return refs, nil
}

func ragMetadataAlias(ref, prefix string) bool {
	if !strings.HasPrefix(ref, prefix) || len(ref) <= len(prefix) || ref[len(prefix)] == '0' {
		return false
	}
	number, err := strconv.Atoi(ref[len(prefix):])
	return err == nil && number > 0 && ref == prefix+strconv.Itoa(number)
}

func ragCitationIDFromRequest(request agentapplication.ChatRequest) (string, error) {
	for index := len(request.Messages) - 1; index >= 0; index-- {
		document := any(request.Messages[index].Content)
		if found := findRAGFirstListString(document, "citation_ids"); strings.HasPrefix(found, "cite-") {
			return found, nil
		}
		if found := findRAGString(document, "id"); strings.HasPrefix(found, "cite-") {
			return found, nil
		}
		if found := findRAGString(document, "citation_id"); found != "" {
			return found, nil
		}
	}
	return "", fmt.Errorf("citation id missing from phase %q", request.Phase)
}

func findRAGFirstListString(value any, key string) string {
	switch typed := value.(type) {
	case map[string]any:
		if list, ok := typed[key].([]any); ok && len(list) > 0 {
			if found, ok := list[0].(string); ok {
				return found
			}
		}
		for _, nested := range typed {
			if found := findRAGFirstListString(nested, key); found != "" {
				return found
			}
		}
	case []any:
		for _, nested := range typed {
			if found := findRAGFirstListString(nested, key); found != "" {
				return found
			}
		}
	case string:
		if decoded := decodeRAGMessageJSON(typed); decoded != nil {
			return findRAGFirstListString(decoded, key)
		}
	}
	return ""
}

func ragAllowedTopicFromRequest(request agentapplication.ChatRequest) (foundation.ID, string, string, error) {
	for index := len(request.Messages) - 1; index >= 0; index-- {
		value := decodeRAGMessageJSON(request.Messages[index].Content)
		if value == nil {
			continue
		}
		var visit func(any) (foundation.ID, string, string, bool)
		visit = func(candidate any) (foundation.ID, string, string, bool) {
			switch typed := candidate.(type) {
			case map[string]any:
				if topics, ok := typed["related_topics"].(map[string]any); ok {
					for rawID, rawTopic := range topics {
						topic, ok := rawTopic.(map[string]any)
						if !ok {
							continue
						}
						name, _ := topic["name"].(string)
						citations, _ := topic["citation_ids"].([]any)
						if id, err := foundation.ParseID(rawID); err == nil && name != "" && len(citations) > 0 {
							if citation, ok := citations[0].(string); ok {
								return id, name, citation, true
							}
						}
					}
				}
				for _, nested := range typed {
					if id, name, citation, ok := visit(nested); ok {
						return id, name, citation, true
					}
				}
			case []any:
				for _, nested := range typed {
					if id, name, citation, ok := visit(nested); ok {
						return id, name, citation, true
					}
				}
			}
			return "", "", "", false
		}
		if id, name, citation, ok := visit(value); ok {
			return id, name, citation, nil
		}
	}
	return "", "", "", fmt.Errorf("related topic allowlist missing from initial request")
}

func decodeRAGMessageJSON(content string) any {
	if start := strings.IndexByte(content, '{'); start >= 0 {
		content = content[start:]
	}
	var value any
	if json.Unmarshal([]byte(content), &value) != nil {
		return nil
	}
	return value
}

func findRAGString(value any, key string) string {
	switch typed := value.(type) {
	case map[string]any:
		if found, ok := typed[key].(string); ok && found != "" {
			return found
		}
		for _, nested := range typed {
			if found := findRAGString(nested, key); found != "" {
				return found
			}
		}
	case []any:
		for _, nested := range typed {
			if found := findRAGString(nested, key); found != "" {
				return found
			}
		}
	case string:
		var nested any
		candidate := typed
		if start := strings.IndexByte(candidate, '{'); start >= 0 {
			candidate = candidate[start:]
		}
		if json.Unmarshal([]byte(candidate), &nested) == nil {
			return findRAGString(nested, key)
		}
	}
	return ""
}

type ragSnapshot struct {
	answerHash    string
	claimCount    int
	topicCount    int
	feedbackCount int
}

func ragKnowledgeAndAnswerSnapshot(t *testing.T, ctx context.Context, pool *pgxpool.Pool, answerID string) ragSnapshot {
	t.Helper()
	var result ragSnapshot
	if err := pool.QueryRow(ctx, `SELECT result_hash FROM agent.answer WHERE id=$1`, answerID).Scan(&result.answerHash); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.claim WHERE workspace_id=$1`, string(ragSmokeWorkspaceID)).Scan(&result.claimCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.topic WHERE workspace_id=$1`, string(ragSmokeWorkspaceID)).Scan(&result.topicCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent.answer_feedback WHERE answer_id=$1`, answerID).Scan(&result.feedbackCount); err != nil {
		t.Fatal(err)
	}
	return result
}

func waitForRAGAnswerHTTP(t *testing.T, ctx context.Context, pool *pgxpool.Pool, model *ragRequestAwareModel, serverURL, answerID string) {
	t.Helper()
	deadline := time.NewTicker(20 * time.Millisecond)
	defer deadline.Stop()
	for {
		answer := doRAGGET(t, ctx, serverURL+"/api/v1/answers/"+answerID+"?workspace_id="+string(ragSmokeWorkspaceID), http.StatusOK)
		if answer["publication_status"] == "completed" {
			return
		}
		if workflow, ok := answer["workflow"].(map[string]any); ok {
			if status, _ := workflow["status"].(string); status == "failed" || status == "cancelled" {
				var code, summary *string
				_ = pool.QueryRow(ctx, `SELECT error_code,error_summary FROM workflow.node_run WHERE run_id=$1`, workflow["run_id"]).Scan(&code, &summary)
				var modelStatus, finalError string
				var modelCallCount int
				queryErr := pool.QueryRow(ctx, `SELECT status,COALESCE(error_code,''),(SELECT count(*) FROM agent.model_call call WHERE call.model_run_id=run.id) FROM agent.model_run run WHERE run.workflow_run_id=$1`, workflow["run_id"]).Scan(&modelStatus, &finalError, &modelCallCount)
				var eventTypes string
				_ = pool.QueryRow(ctx, `SELECT string_agg(event_type,',' ORDER BY seq) FROM ops.server_event WHERE workflow_run_id=$1`, workflow["run_id"]).Scan(&eventTypes)
				t.Fatalf("RAG workflow terminated as %q code=%s summary=%s model_query=%v model_status=%s model_error=%s model_calls=%d events=%s provider_calls=%d: %v", status, optionalRAGText(code), optionalRAGText(summary), queryErr, modelStatus, finalError, modelCallCount, eventTypes, model.CallCount(), answer)
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-deadline.C:
		}
	}
}

func optionalRAGText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func assertCompletedRAGAnswer(t *testing.T, answer map[string]any) {
	t.Helper()
	if answer["publication_status"] != "completed" || answer["result_type"] != "rag_answer" {
		t.Fatalf("answer=%v", answer)
	}
	result, ok := answer["result"].(map[string]any)
	if !ok {
		t.Fatalf("answer result=%T", answer["result"])
	}
	payload, ok := result["payload"].(map[string]any)
	if !ok || len(payload["related_topics"].([]any)) != 1 || len(payload["follow_up_questions"].([]any)) != 1 {
		t.Fatalf("payload=%v", payload)
	}
	citations, ok := answer["citations"].([]any)
	if !ok || len(citations) != 1 || !strings.Contains(citations[0].(map[string]any)["href"].(string), string(ragSmokeSpanID)) {
		t.Fatalf("citations=%v", answer["citations"])
	}
}

func assertRAGSSEReplay(t *testing.T, parent context.Context, serverURL string, after int64, answerID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/api/v1/events?workspace_id=%s", serverURL, ragSmokeWorkspaceID), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Last-Event-ID", fmt.Sprint(after))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("SSE status=%d body=%s", response.StatusCode, body)
	}
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var envelope map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &envelope); err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(envelope)
		if strings.Contains(string(encoded), answerID) && envelope["type"] == "answer.completed" {
			return
		}
		for _, secret := range []string{"approved recovery evidence", filepath.Clean(os.TempDir())} {
			if secret != "." && strings.Contains(string(encoded), secret) {
				t.Fatalf("SSE leaked %q: %s", secret, encoded)
			}
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		t.Fatal(err)
	}
	t.Fatal("answer.completed was not replayed through public SSE")
}

func doRAGJSON(t *testing.T, ctx context.Context, method, target, idempotencyKey string, body any, want int) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", idempotencyKey)
	return executeRAGRequest(t, request, want)
}

func doRAGGET(t *testing.T, ctx context.Context, target string, want int) map[string]any {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	return executeRAGRequest(t, request, want)
}

func executeRAGRequest(t *testing.T, request *http.Request, want int) map[string]any {
	t.Helper()
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != want {
		t.Fatalf("%s %s status=%d want=%d body=%s", request.Method, request.URL, response.StatusCode, want, raw)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return document
}

func mustRAGString(t *testing.T, document map[string]any, key string) string {
	t.Helper()
	value, ok := document[key].(string)
	if !ok || value == "" {
		t.Fatalf("%q missing from %v", key, document)
	}
	return value
}

func mustRAGNestedString(t *testing.T, document map[string]any, parent, key string) string {
	t.Helper()
	nested, ok := document[parent].(map[string]any)
	if !ok {
		t.Fatalf("%q missing from %v", parent, document)
	}
	return mustRAGString(t, nested, key)
}

func mustRAGCitationID(t *testing.T, answer map[string]any) string {
	t.Helper()
	citations, ok := answer["citations"].([]any)
	if !ok || len(citations) != 1 {
		t.Fatalf("citations=%v", answer["citations"])
	}
	return mustRAGString(t, citations[0].(map[string]any), "id")
}
