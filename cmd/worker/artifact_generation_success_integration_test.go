//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	agenteino "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/eino"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/app"
	artifactpostgres "github.com/CodeZen-Lizhi/zhixu/internal/artifact/adapter/postgres"
	artifactapplication "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactdomain "github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	artifacthttp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/http"
	artifactworkflow "github.com/CodeZen-Lizhi/zhixu/internal/artifact/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	platformmodels "github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	retrievalpostgres "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/postgres"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	workflowhttp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/http"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

const artifactGenerationSentinel = "artifact-generation-success-sentinel"

// TestArtifactGenerationSucceedsThroughPublicHTTPAndRiverWorker exercises the public Artifact command,
// durable River delivery, immutable Artifact finalization, and retrieval isolation as one product flow.
func TestArtifactGenerationSucceedsThroughPublicHTTPAndRiverWorker(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	ctx, cancel := context.WithTimeout(t.Context(), 55*time.Second)
	defer cancel()
	database := newMigratedWorkerTestPool(t, databaseURL)
	pool := database.DB()
	seedRAGConversationKnowledge(t, ctx, database, t.TempDir())

	model := &artifactGenerationRequestAwareModel{}
	router, workerClient := newArtifactGenerationSuccessRuntime(t, database, model)
	server := httptest.NewServer(router)
	defer server.Close()
	if err := workerClient.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		if err := workerClient.Stop(stopCtx); err != nil {
			t.Errorf("stop River worker: %v", err)
		}
	}()

	client := server.Client()
	documentsBefore := artifactGenerationCount(t, ctx, pool, `SELECT count(*) FROM core.document WHERE workspace_id=$1`, string(ragSmokeWorkspaceID))
	planned := artifactGenerationPOST(t, ctx, client, server.URL+"/api/v1/artifacts", "artifact-generation-plan", map[string]any{
		"workspace_id": string(ragSmokeWorkspaceID),
		"type":         "study-guide", "title": "Approved recovery", "scope_definition": "Replay durable facts without duplicating provider work.",
	}, http.StatusCreated)
	artifact := artifactGenerationObject(t, planned, "artifact")
	artifactID := artifactGenerationString(t, artifact, "id")
	if artifactGenerationString(t, artifact, "status") != string(artifactdomain.StatusPlanning) || artifactGenerationInt(t, artifact, "version") != 1 {
		t.Fatalf("planned artifact=%v", planned)
	}

	outlined := artifactGenerationPOST(t, ctx, client, server.URL+"/api/v1/artifacts/"+artifactID+"/outline", "artifact-generation-outline", map[string]any{
		"workspace_id": string(ragSmokeWorkspaceID), "expected_version": 1,
		"outline": []map[string]any{{"key": "recovery", "title": "Recovery"}},
	}, http.StatusOK)
	if got := artifactGenerationString(t, artifactGenerationObject(t, outlined, "artifact"), "status"); got != string(artifactdomain.StatusOutlineReview) {
		t.Fatalf("outline status=%s response=%v", got, outlined)
	}
	approved := artifactGenerationPOST(t, ctx, client, server.URL+"/api/v1/artifacts/"+artifactID+"/outline/approve", "artifact-generation-approve", map[string]any{
		"workspace_id": string(ragSmokeWorkspaceID), "expected_version": 2,
	}, http.StatusOK)
	approvedArtifact := artifactGenerationObject(t, approved, "artifact")
	if artifactGenerationString(t, approvedArtifact, "status") != string(artifactdomain.StatusGenerating) || artifactGenerationInt(t, approvedArtifact, "version") != 3 {
		t.Fatalf("approved outline=%v", approved)
	}

	generationBody := map[string]any{"workspace_id": string(ragSmokeWorkspaceID), "expected_version": 3, "section_key": "recovery"}
	started := artifactGenerationPOST(t, ctx, client, server.URL+"/api/v1/artifacts/"+artifactID+"/sections/generate", "artifact-generation-success", generationBody, http.StatusAccepted)
	if started["replayed"] != false || artifactGenerationString(t, started, "status") != string(artifactapplication.SectionGenerationPending) || artifactGenerationInt(t, started, "version") != 1 {
		t.Fatalf("started generation=%v", started)
	}
	workflowRunID := artifactGenerationString(t, started, "workflow_run_id")
	if artifactGenerationString(t, started, "status_url") != "/api/v1/workflows/"+workflowRunID {
		t.Fatalf("status URL binding=%v", started)
	}

	waitForArtifactGenerationWorkflow(t, ctx, pool, foundation.ID(workflowRunID))
	workflow := artifactGenerationGET(t, ctx, client, server.URL+artifactGenerationString(t, started, "status_url"), http.StatusOK)
	if artifactGenerationString(t, workflow, "id") != workflowRunID || artifactGenerationString(t, workflow, "status") != string(workflowdomain.RunStatusSucceeded) {
		t.Fatalf("completed workflow response=%v", workflow)
	}
	completed := artifactGenerationLoad(t, ctx, pool, artifactGenerationString(t, started, "generation_id"))
	if completed.status != artifactapplication.SectionGenerationCompleted || completed.version != 2 || completed.modelRunID == "" || completed.recordedRevisionID == "" || completed.recordedArtifactVersion != 4 {
		t.Fatalf("completed generation=%+v", completed)
	}
	if calls := model.CallCount(); calls != 1 {
		t.Fatalf("model calls=%d want=1", calls)
	}
	var modelRunStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM agent.model_run WHERE id=$1 AND workspace_id=$2`, string(completed.modelRunID), string(ragSmokeWorkspaceID)).Scan(&modelRunStatus); err != nil || modelRunStatus != string(agentdomain.ModelRunSucceeded) {
		t.Fatalf("persisted model run status=%q err=%v", modelRunStatus, err)
	}
	if calls := artifactGenerationCount(t, ctx, pool, `SELECT count(*) FROM agent.model_call WHERE model_run_id=$1 AND status=$2`, string(completed.modelRunID), string(agentdomain.ModelCallSucceeded)); calls != 1 {
		t.Fatalf("persisted model calls=%d want=1", calls)
	}

	state := artifactGenerationGET(t, ctx, client, fmt.Sprintf("%s/api/v1/artifacts/%s?workspace_id=%s", server.URL, artifactID, ragSmokeWorkspaceID), http.StatusOK)
	completedArtifact := state
	revision := artifactGenerationObject(t, completedArtifact, "revision")
	sections, ok := revision["sections"].([]any)
	if !ok || len(sections) != 1 {
		t.Fatalf("generated revision sections=%v", revision["sections"])
	}
	section, ok := sections[0].(map[string]any)
	if !ok || artifactGenerationString(t, section, "key") != "recovery" || artifactGenerationString(t, section, "content") != artifactGenerationSentinel {
		t.Fatalf("generated section=%v", sections[0])
	}
	coverage := artifactGenerationObject(t, section, "coverage")
	if artifactGenerationString(t, coverage, "status") != string(artifactdomain.CoverageCovered) || len(artifactGenerationList(t, section, "citations")) != 1 {
		t.Fatalf("generated coverage=%v section=%v", coverage, section)
	}
	citation, ok := artifactGenerationList(t, section, "citations")[0].(map[string]any)
	if !ok || citation["verified"] != true || artifactGenerationString(t, citation, "source_version_id") != string(ragSmokeSourceVersionID) || artifactGenerationString(t, citation, "source_span_id") != string(ragSmokeSpanID) {
		t.Fatalf("generated citation=%v", citation)
	}
	if artifactGenerationString(t, completedArtifact, "current_revision_id") != string(completed.recordedRevisionID) || artifactGenerationInt(t, completedArtifact, "version") != 4 || artifactGenerationInt(t, revision, "revision_no") != 4 {
		t.Fatalf("immutable revision binding artifact=%v revision=%v generation=%+v", completedArtifact, revision, completed)
	}
	if count := artifactGenerationCount(t, ctx, pool, `SELECT count(*) FROM learning.artifact_revision WHERE artifact_id=$1 AND workspace_id=$2`, artifactID, string(ragSmokeWorkspaceID)); count != 4 {
		t.Fatalf("artifact revision count=%d want=4", count)
	}

	replayed := artifactGenerationPOST(t, ctx, client, server.URL+"/api/v1/artifacts/"+artifactID+"/sections/generate", "artifact-generation-success", generationBody, http.StatusAccepted)
	if replayed["replayed"] != true || artifactGenerationString(t, replayed, "generation_id") != artifactGenerationString(t, started, "generation_id") || artifactGenerationString(t, replayed, "workflow_run_id") != workflowRunID || artifactGenerationString(t, replayed, "status") != string(artifactapplication.SectionGenerationCompleted) {
		t.Fatalf("generation replay=%v start=%v", replayed, started)
	}
	if calls := model.CallCount(); calls != 1 {
		t.Fatalf("generation replay called model=%d times", calls)
	}

	searchRepository, err := retrievalpostgres.NewGORMSearchRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	searchService, err := retrievalapplication.NewSearchService(searchRepository, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := searchService.Search(ctx, retrievaldomain.SearchRequest{WorkspaceID: ragSmokeWorkspaceID, Query: artifactGenerationSentinel, Mode: retrievaldomain.SearchModeKeyword, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 0 {
		t.Fatalf("artifact body leaked into default keyword retrieval: %+v", result.Items)
	}
	if documentsAfter := artifactGenerationCount(t, ctx, pool, `SELECT count(*) FROM core.document WHERE workspace_id=$1`, string(ragSmokeWorkspaceID)); documentsAfter != documentsBefore {
		t.Fatalf("generation changed formal document count: before=%d after=%d", documentsBefore, documentsAfter)
	}
}

func newArtifactGenerationSuccessRuntime(t *testing.T, pool *platformpostgres.Pool, model agentapplication.ChatModel) (http.Handler, *riveradapter.Client) {
	t.Helper()
	workspaces, err := workspacepostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	agentRepository, terminal, err := newArtifactGenerationAgent(pool)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workflowpostgres.NewGORMRuntimeRepositoryWithHooks(pool, riveradapter.DefaultOptions(), riveradapter.NewStaticScopedEnqueueFence(), workflowpostgres.GORMRuntimeRepositoryHooks{Terminal: terminal})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.ChatProvider = config.ChatProviderOpenAICompatible
	cfg.ChatBaseURL = "http://127.0.0.1:1/v1"
	cfg.ChatAPIKey = "integration-only"
	cfg.ChatModel = "artifact-generation-integration"
	cfg.ChatModelVersion = "v1"
	contract := platformmodels.ChatContract{
		Provider: "integration",
		Model:    agentdomain.ModelRef{AdapterName: "integration", AdapterVersion: "v1", ModelID: cfg.ChatModel, ModelVersion: cfg.ChatModelVersion},
		Timeout:  10 * time.Second, MaxRequestBytes: cfg.ChatMaxRequestBytes, MaxResponseBytes: cfg.ChatMaxResponseBytes,
	}
	scheduler, err := agenteino.NewStructuredPhaseScheduler(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	components, err := newArtifactWorkflowComponents(pool, workspaces, runtime, agentRepository, terminal, model, contract, scheduler, foundation.NewUUIDGenerator(nil), foundation.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	if components.generation == nil || components.executor == nil || !components.capability.available {
		t.Fatalf("artifact workflow components=%+v", components)
	}
	catalog, err := workflowapplication.NewValidationCatalog([]int{1}, capability.All())
	if err != nil {
		t.Fatal(err)
	}
	executors, err := workflowapplication.NewExecutorRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := executors.Register(artifactworkflow.NodeKind, artifactworkflow.InputSchemaVersion, components.executor); err != nil {
		t.Fatal(err)
	}
	if err := executors.Freeze(); err != nil {
		t.Fatal(err)
	}
	coordinator, err := workflowapplication.NewRuntimeCoordinator(runtime)
	if err != nil {
		t.Fatal(err)
	}
	runtimeWorker, err := riveradapter.NewRuntimeNodeWorker(executors, coordinator, "artifact-generation-success", 10*time.Second, time.Second)
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

	artifactRepository, err := artifactpostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	commands, err := artifactapplication.NewCommandService(artifactapplication.Dependencies{Repository: artifactRepository, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	queries, err := artifactapplication.NewQueryService(artifactRepository)
	if err != nil {
		t.Fatal(err)
	}
	workflowRepository, err := workflowpostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	workflowService, err := workflowapplication.NewService(workflowRepository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	artifactHandler := artifacthttp.NewHandler(commands, queries, 10*time.Second, components.generation)
	router := app.NewRouter(app.Dependencies{
		Version: "artifact-generation-success", Database: pool, Artifact: artifactHandler, Workflow: workflowhttp.NewHandler(workflowService),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return router, workerClient
}

type artifactGenerationRequestAwareModel struct {
	mu    sync.Mutex
	calls int
}

func (model *artifactGenerationRequestAwareModel) Chat(_ context.Context, request agentapplication.ChatRequest) (agentapplication.ChatResponse, error) {
	if request.Phase != agentdomain.ModelCallInitial || request.PromptRef != artifactworkflow.PromptRef() || request.SchemaRef != artifactworkflow.SchemaRef() {
		return agentapplication.ChatResponse{}, fmt.Errorf("unexpected artifact generation request: phase=%s prompt=%+v schema=%+v", request.Phase, request.PromptRef, request.SchemaRef)
	}
	model.mu.Lock()
	model.calls++
	model.mu.Unlock()
	output, err := json.Marshal(artifactworkflow.SectionGenerationEnvelope{
		ResultType: agentdomain.ResultTypeArtifactSection, SchemaID: artifactworkflow.SchemaID, SchemaVersion: artifactworkflow.RuntimeVersion,
		Payload: artifactworkflow.SectionGenerationPayload{CoverageStatus: artifactdomain.CoverageCovered, Content: artifactGenerationSentinel, CitationLabels: []string{"citation-001"}, Gaps: []artifactworkflow.GapOutput{}},
	})
	if err != nil {
		return agentapplication.ChatResponse{}, err
	}
	return agentapplication.ChatResponse{Model: request.Model, Content: output, Usage: agentdomain.TokenUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}, nil
}

func (model *artifactGenerationRequestAwareModel) CallCount() int {
	model.mu.Lock()
	defer model.mu.Unlock()
	return model.calls
}

type artifactGenerationRecord struct {
	status                  artifactapplication.SectionGenerationStatus
	version                 int64
	modelRunID              foundation.ID
	recordedRevisionID      foundation.ID
	recordedArtifactVersion int64
}

func artifactGenerationLoad(t *testing.T, ctx context.Context, pool *pgxpool.Pool, generationID string) artifactGenerationRecord {
	t.Helper()
	var record artifactGenerationRecord
	if err := pool.QueryRow(ctx, `SELECT status,version,COALESCE(model_run_id::text,''),COALESCE(recorded_revision_id::text,''),COALESCE(recorded_artifact_version,0)
		FROM learning.artifact_section_generation WHERE id=$1 AND workspace_id=$2`, generationID, string(ragSmokeWorkspaceID)).Scan(
		&record.status, &record.version, &record.modelRunID, &record.recordedRevisionID, &record.recordedArtifactVersion); err != nil {
		t.Fatal(err)
	}
	return record
}

func waitForArtifactGenerationWorkflow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runID foundation.ID) {
	t.Helper()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		var status workflowdomain.RunStatus
		var nodeStatus workflowdomain.NodeStatus
		var code, summary *string
		if err := pool.QueryRow(ctx, `SELECT r.status,n.status,n.error_code,n.error_summary FROM workflow.run r JOIN workflow.node_run n ON n.run_id=r.id WHERE r.id=$1`, string(runID)).Scan(&status, &nodeStatus, &code, &summary); err != nil {
			t.Fatal(err)
		}
		if status == workflowdomain.RunStatusSucceeded {
			return
		}
		if workflowdomain.IsTerminalRunStatus(status) {
			t.Fatalf("artifact workflow=%s status=%s node=%s code=%s summary=%s diagnostics=%s", runID, status, nodeStatus, artifactGenerationOptional(code), artifactGenerationOptional(summary), artifactGenerationFailureDiagnostics(ctx, pool, runID))
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
}

func artifactGenerationFailureDiagnostics(ctx context.Context, pool *pgxpool.Pool, runID foundation.ID) string {
	parts := make([]string, 0, 4)
	var generationStatus, failureClass, generationCode, generationSummary, generationModelRunID string
	if err := pool.QueryRow(ctx, `SELECT status,COALESCE(failure_class,''),COALESCE(error_code,''),COALESCE(error_summary,''),COALESCE(model_run_id::text,'')
		FROM learning.artifact_section_generation WHERE workflow_run_id=$1`, string(runID)).Scan(
		&generationStatus, &failureClass, &generationCode, &generationSummary, &generationModelRunID,
	); err != nil {
		parts = append(parts, "generation_query="+err.Error())
	} else {
		parts = append(parts, fmt.Sprintf("generation(status=%s class=%s code=%s summary=%s model_run=%s)", generationStatus, failureClass, generationCode, generationSummary, generationModelRunID))
	}

	rows, err := pool.Query(ctx, `SELECT mr.id::text,mr.status,mr.output_schema_id,COALESCE(mr.error_code,''),mr.version,
		COALESCE(mc.call_no,0),COALESCE(mc.phase,''),COALESCE(mc.status,''),COALESCE(mc.output_schema_id,''),COALESCE(mc.error_code,'')
		FROM agent.model_run mr
		LEFT JOIN agent.model_call mc ON mc.model_run_id=mr.id
		WHERE mr.workflow_run_id=$1
		ORDER BY mr.started_at,mr.id,mc.call_no`, string(runID))
	if err != nil {
		parts = append(parts, "model_query="+err.Error())
		return strings.Join(parts, " ")
	}
	defer rows.Close()
	modelFacts := make([]string, 0, 3)
	for rows.Next() {
		var modelRunID, modelRunStatus, modelRunSchema, modelRunCode string
		var modelRunVersion int64
		var callNo int
		var callPhase, callStatus, callSchema, callCode string
		if scanErr := rows.Scan(&modelRunID, &modelRunStatus, &modelRunSchema, &modelRunCode, &modelRunVersion, &callNo, &callPhase, &callStatus, &callSchema, &callCode); scanErr != nil {
			parts = append(parts, "model_scan="+scanErr.Error())
			return strings.Join(parts, " ")
		}
		modelFacts = append(modelFacts, fmt.Sprintf("run=%s/%s/v%d/schema=%s/code=%s call=%d/%s/%s/schema=%s/code=%s", modelRunID, modelRunStatus, modelRunVersion, modelRunSchema, modelRunCode, callNo, callPhase, callStatus, callSchema, callCode))
	}
	if rows.Err() != nil {
		parts = append(parts, "model_rows="+rows.Err().Error())
	} else if len(modelFacts) == 0 {
		parts = append(parts, "model_runs=none")
	} else {
		parts = append(parts, "model_facts="+strings.Join(modelFacts, ","))
	}
	return strings.Join(parts, " ")
}

func artifactGenerationPOST(t *testing.T, ctx context.Context, client *http.Client, target, idempotencyKey string, body any, want int) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", idempotencyKey)
	return artifactGenerationRequest(t, client, request, want)
}

func artifactGenerationGET(t *testing.T, ctx context.Context, client *http.Client, target string, want int) map[string]any {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Workspace-ID", string(ragSmokeWorkspaceID))
	return artifactGenerationRequest(t, client, request, want)
}

func artifactGenerationRequest(t *testing.T, client *http.Client, request *http.Request, want int) map[string]any {
	t.Helper()
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != want {
		t.Fatalf("%s %s status=%d want=%d body=%s", request.Method, request.URL, response.StatusCode, want, body)
	}
	var value map[string]any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("decode response: %v body=%s", err, body)
	}
	return value
}

func artifactGenerationObject(t *testing.T, value map[string]any, key string) map[string]any {
	t.Helper()
	result, ok := value[key].(map[string]any)
	if !ok {
		t.Fatalf("%q object missing from %v", key, value)
	}
	return result
}

func artifactGenerationList(t *testing.T, value map[string]any, key string) []any {
	t.Helper()
	result, ok := value[key].([]any)
	if !ok {
		t.Fatalf("%q list missing from %v", key, value)
	}
	return result
}

func artifactGenerationString(t *testing.T, value map[string]any, key string) string {
	t.Helper()
	result, ok := value[key].(string)
	if !ok || strings.TrimSpace(result) == "" {
		t.Fatalf("%q string missing from %v", key, value)
	}
	return result
}

func artifactGenerationInt(t *testing.T, value map[string]any, key string) int64 {
	t.Helper()
	result, ok := value[key].(float64)
	if !ok || result != float64(int64(result)) {
		t.Fatalf("%q integer missing from %v", key, value)
	}
	return int64(result)
}

func artifactGenerationCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, arguments ...any) int64 {
	t.Helper()
	var count int64
	if err := pool.QueryRow(ctx, query, arguments...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func artifactGenerationOptional(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
