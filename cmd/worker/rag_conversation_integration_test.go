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
	"strings"
	"sync"
	"testing"
	"time"

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
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	eventshttp "github.com/CodeZen-Lizhi/zhixu/internal/events/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgepostgres "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/adapter/postgres"
	knowledgeapplication "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	memorypostgres "github.com/CodeZen-Lizhi/zhixu/internal/memory/adapter/postgres"
	memoryapplication "github.com/CodeZen-Lizhi/zhixu/internal/memory/application"
	memorydomain "github.com/CodeZen-Lizhi/zhixu/internal/memory/domain"
	platformfilesystem "github.com/CodeZen-Lizhi/zhixu/internal/platform/filesystem"
	retrievalpostgres "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/postgres"
	retrievalworkspace "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/workspace"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
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
)

// TestPublicConversationRunsThroughRiverRAGAndFeedback proves the product seam rather than a direct finalizer shortcut.
func TestPublicConversationRunsThroughRiverRAGAndFeedback(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL for the RAG Conversation integration gate")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pool := newMigratedWorkerTestPool(t, baseURL)
	seedRAGConversationKnowledge(t, ctx, pool, t.TempDir())

	model := &ragRequestAwareModel{}
	router, workerClient, eventStore := newRAGConversationIntegrationRuntime(t, pool, model)
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

	providerCalls := model.CallCount()
	if providerCalls != 3 {
		t.Fatalf("provider calls=%d want plan+answer+review", providerCalls)
	}
	replayed := doRAGJSON(t, ctx, http.MethodPost, server.URL+"/api/v1/conversations/"+conversationID+"/questions", "rag-question-once", questionBody, http.StatusOK)
	if mustRAGNestedString(t, replayed, "answer", "id") != answerID || model.CallCount() != providerCalls {
		t.Fatalf("exact replay changed answer or repeated provider: replay=%v calls=%d", replayed, model.CallCount())
	}

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

func newRAGConversationIntegrationRuntime(t *testing.T, pool *pgxpool.Pool, model agentapplication.ChatModel) (http.Handler, *riveradapter.Client, *eventspostgres.Store) {
	t.Helper()
	events, err := eventspostgres.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	conversationRepository, err := conversationpostgres.NewRepository(pool, events)
	if err != nil {
		t.Fatal(err)
	}
	agentRepository, err := agentpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	memoryRepository, err := memorypostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
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
	knowledgeRepository, err := knowledgepostgres.NewRepository(pool)
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
	workspaceRepository, err := workspacepostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	searchRepository, err := retrievalpostgres.NewSearchRepository(pool)
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
	finalizer, err := conversationpostgres.NewAnswerFinalizer(pool, agentRepository, events, foundation.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	progress, err := agentpostgres.NewRAGProgressStore(pool, events)
	if err != nil {
		t.Fatal(err)
	}
	modelRef := agentdomain.ModelRef{AdapterName: "integration", AdapterVersion: "v1", ModelID: "request-aware", ModelVersion: "v1"}
	catalog, err := agentworkflow.NewRuntimeCatalog(agentworkflow.CatalogOptions{Model: modelRef, Timeout: 10 * time.Second, MaxOutputTokens: 2048})
	if err != nil {
		t.Fatal(err)
	}
	ragExecutor, err := agentworkflow.NewRAGWorkflowExecutor(agentworkflow.RAGWorkflowExecutorDependencies{
		Model: model, Catalog: catalog, Repository: agentRepository, Snapshots: agentRepository,
		Memory: memoryLoader, MemoryOwner: agentapplication.MemoryOwnerRef{Kind: string(memoryOwner.Kind), ID: memoryOwner.ID},
		Context: conversationRepository,
		Search:  retrievalAdapter, Retrieval: retrievalAdapter, Eligibility: knowledgeAdapter, Topics: topicAdapter,
		Finalizer: finalizer, Progress: progress, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}, Budget: agentapplication.DefaultRunBudget(),
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
	insertClient, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(insertClient)
	if err != nil {
		t.Fatal(err)
	}
	runtimeRepository, err := workflowpostgres.NewRuntimeRepository(pool, inserter)
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
	workerClient, err := riveradapter.NewClient(pool, workers)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := conversationpostgres.NewQuestionDispatcher(pool, runtimeRepository, events, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, conversationworkflow.RegisteredDefinition())
	if err != nil {
		t.Fatal(err)
	}
	service, err := conversationapplication.NewService(conversationapplication.Dependencies{Repository: conversationRepository, QuestionDispatcher: dispatcher, FeedbackRepository: conversationRepository, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	conversationHandler := conversationhttp.NewHandler(service, conversationhttp.NewCursorCodec())
	eventHandler := eventshttp.NewHandler(events, eventshttp.StreamConfig{PollInterval: 10 * time.Millisecond, HeartbeatInterval: 100 * time.Millisecond})
	router := app.NewRouter(app.Dependencies{Version: "rag-integration", Database: pool, Conversation: conversationHandler, Events: eventHandler, RAGEnabled: true, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	return router, workerClient, events
}

type ragRequestAwareModel struct {
	mu    sync.Mutex
	calls int
}

func (model *ragRequestAwareModel) Chat(_ context.Context, request agentapplication.ChatRequest) (agentapplication.ChatResponse, error) {
	model.mu.Lock()
	model.calls++
	model.mu.Unlock()
	modelRunRef, err := ragModelRunRef(request)
	if err != nil {
		return agentapplication.ChatResponse{}, err
	}
	var output any
	switch request.Phase {
	case agentdomain.ModelCallPlan:
		output = agentdomain.RAGQueryPlanResult{ResultType: agentdomain.ResultTypeRAGQueryPlan, SchemaID: agentdomain.RAGQueryPlanSchemaID, SchemaVersion: agentdomain.OutputSchemaVersionV1, ModelRunRef: modelRunRef, Payload: agentdomain.RAGQueryPlanPayload{Intent: "approved recovery behavior", Rewrites: []string{"approved recovery"}, SuggestedScopes: []string{}}}
	case agentdomain.ModelCallInitial:
		topicID, topicName, citationID, err := ragAllowedTopicFromRequest(request)
		if err != nil {
			return agentapplication.ChatResponse{}, err
		}
		output = agentdomain.RAGAnswerResultV2{ResultType: agentdomain.ResultTypeRAGAnswer, SchemaID: agentdomain.RAGAnswerSchemaID, SchemaVersion: agentdomain.OutputSchemaVersionV2, ModelRunRef: modelRunRef, Payload: agentdomain.RAGAnswerPayloadV2{RAGAnswerPayload: agentdomain.RAGAnswerPayload{
			Conclusion: "Approved recovery requires replaying durable facts.",
			Assertions: []agentdomain.Assertion{{ID: "recovery-assertion", Text: "Recovery replays durable facts without duplicating provider work.", Kind: agentdomain.AssertionFactual, CitationIDs: []string{citationID}}},
			Citations:  []agentdomain.Citation{{ID: citationID, WorkspaceID: ragSmokeWorkspaceID, IndexVersionID: ragSmokeIndexID, ChunkID: ragSmokeChunkID, SourceVersionID: ragSmokeSourceVersionID, SourceSpanID: ragSmokeSpanID}}, ConflictPositions: []agentdomain.ConflictPosition{},
		}, RelatedTopics: []agentdomain.RelatedTopic{{TopicID: topicID, Name: topicName, CitationIDs: []string{citationID}}}, FollowUpQuestions: []string{"How is replay kept idempotent?"}}}
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

func (model *ragRequestAwareModel) CallCount() int {
	model.mu.Lock()
	defer model.mu.Unlock()
	return model.calls
}

func ragModelRunRef(request agentapplication.ChatRequest) (foundation.ID, error) {
	for index := len(request.Messages) - 1; index >= 0; index-- {
		if found := findRAGString(request.Messages[index].Content, "model_run_ref"); found != "" {
			return foundation.ParseID(found)
		}
	}
	return "", fmt.Errorf("model_run_ref missing from phase %q", request.Phase)
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
