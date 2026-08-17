//go:build integration

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agenteino "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/eino"
	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
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
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	platformfilesystem "github.com/CodeZen-Lizhi/zhixu/internal/platform/filesystem"
	retrievalpostgres "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/postgres"
	retrievalworkspace "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/workspace"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	toolcatalog "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	toolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/postgres"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	workflowhttp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/http"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/jackc/pgx/v5/pgxpool"
)

const workspaceAnalysisSmokeAnswer = "The approved recovery evidence requires durable replay."

// TestPublicConversationRunsThroughRiverWorkspaceAnalysis exercises the public
// Question mode through the actual PostgreSQL/River six-node workflow. The
// deterministic model is only the Provider boundary; Git, retrieval, tools,
// receipts, candidate persistence and final publication remain production code.
func TestPublicConversationRunsThroughRiverWorkspaceAnalysis(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL for the Workspace Analysis Conversation integration gate")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	pool := newMigratedWorkerTestPool(t, baseURL)
	root := t.TempDir()
	seedWorkspaceAnalysisGitRepository(t, ctx, root)
	seedWorkspaceAnalysisConversationKnowledge(t, ctx, pool, root)

	model := &workspaceAnalysisRequestAwareModel{}
	router, workerClient := newWorkspaceAnalysisConversationIntegrationRuntime(t, pool, model, nil)
	server := httptest.NewServer(router)
	defer server.Close()
	if err := workerClient.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = workerClient.Stop(stopCtx)
	}()

	conversation := doRAGJSON(t, ctx, http.MethodPost, server.URL+"/api/v1/conversations", "workspace-analysis-conversation-create", map[string]any{
		"workspace_id": ragSmokeWorkspaceID, "title": "Workspace Analysis integration",
	}, http.StatusCreated)
	conversationID := mustRAGString(t, conversation, "id")
	questionBody := map[string]any{
		"workspace_id": ragSmokeWorkspaceID,
		"question":     "Approved recovery replays durable facts without duplicating provider work.",
		"scope":        map[string]any{"retrieval_mode": "keyword"},
		"answer_depth": "standard", "output_format": "markdown", "mode": "workspace_analysis",
	}
	accepted := doRAGJSON(t, ctx, http.MethodPost, server.URL+"/api/v1/conversations/"+conversationID+"/questions", "workspace-analysis-question-once", questionBody, http.StatusAccepted)
	answerID := mustRAGNestedString(t, accepted, "answer", "id")
	waitForWorkspaceAnalysisAnswer(t, ctx, pool, server.URL, answerID)
	answer := doRAGGET(t, ctx, server.URL+"/api/v1/answers/"+answerID+"?workspace_id="+string(ragSmokeWorkspaceID), http.StatusOK)
	assertCompletedWorkspaceAnalysisAnswer(t, answer)
	waitForWorkspaceAnalysisWorkflowCompleted(t, ctx, pool, foundation.ID(mustRAGNestedString(t, answer, "workflow", "run_id")))
	assertWorkspaceAnalysisDurableChain(t, ctx, pool, foundation.ID(answerID))

	providerCalls := model.CallCount()
	replayed := doRAGJSON(t, ctx, http.MethodPost, server.URL+"/api/v1/conversations/"+conversationID+"/questions", "workspace-analysis-question-once", questionBody, http.StatusOK)
	if mustRAGNestedString(t, replayed, "answer", "id") != answerID || model.CallCount() != providerCalls {
		t.Fatalf("idempotency replay changed answer or repeated provider: replay=%v calls=%d want=%d", replayed, model.CallCount(), providerCalls)
	}
	assertWorkspaceAnalysisDurableChain(t, ctx, pool, foundation.ID(answerID))
}

// TestPublicConversationWorkspaceAnalysisReceiptLossLeaseReclaimReusesGitReceipt
// proves the recovery boundary that cannot be covered by a normal idempotency
// replay: the ReadGitStatus receipt commits, but the Workflow node completion
// response is lost. A real River redelivery must reclaim the expired lease and
// reuse the original logical Tool operation without observing Git a second time.
func TestPublicConversationWorkspaceAnalysisReceiptLossLeaseReclaimReusesGitReceipt(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL for the Workspace Analysis receipt-loss integration gate")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	pool := newMigratedWorkerTestPool(t, baseURL)
	root := t.TempDir()
	seedWorkspaceAnalysisGitRepository(t, ctx, root)
	seedWorkspaceAnalysisConversationKnowledge(t, ctx, pool, root)

	model := &workspaceAnalysisRequestAwareModel{}
	completionLoss := &failFirstWorkspaceAnalysisCompletion{}
	gitExecutions := &workspaceAnalysisToolExecutionCounter{}
	router, workerClient := newWorkspaceAnalysisConversationIntegrationRuntime(t, pool, model, &workspaceAnalysisConversationRuntimeFault{
		decorateCoordinator: completionLoss.decorate,
		toolExecutions:      gitExecutions,
	})
	server := httptest.NewServer(router)
	defer server.Close()
	if err := workerClient.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = workerClient.Stop(stopCtx)
	}()

	conversation := doRAGJSON(t, ctx, http.MethodPost, server.URL+"/api/v1/conversations", "workspace-analysis-receipt-loss-conversation", map[string]any{
		"workspace_id": ragSmokeWorkspaceID, "title": "Workspace Analysis receipt loss",
	}, http.StatusCreated)
	conversationID := mustRAGString(t, conversation, "id")
	questionBody := map[string]any{
		"workspace_id": ragSmokeWorkspaceID,
		"question":     "Approved recovery replays durable facts without duplicating provider work.",
		"scope":        map[string]any{"retrieval_mode": "keyword"},
		"answer_depth": "standard", "output_format": "markdown", "mode": "workspace_analysis",
	}
	accepted := doRAGJSON(t, ctx, http.MethodPost, server.URL+"/api/v1/conversations/"+conversationID+"/questions", "workspace-analysis-receipt-loss-question", questionBody, http.StatusAccepted)
	answerID := foundation.ID(mustRAGNestedString(t, accepted, "answer", "id"))

	firstOutput := completionLoss.waitForFirstCompletion(t, ctx)
	assertWorkspaceAnalysisReceiptLossBeforeReclaim(t, ctx, pool, answerID, firstOutput)
	waitForWorkspaceAnalysisAnswer(t, ctx, pool, server.URL, string(answerID))
	answer := doRAGGET(t, ctx, server.URL+"/api/v1/answers/"+string(answerID)+"?workspace_id="+string(ragSmokeWorkspaceID), http.StatusOK)
	assertCompletedWorkspaceAnalysisAnswer(t, answer)
	waitForWorkspaceAnalysisWorkflowCompleted(t, ctx, pool, foundation.ID(mustRAGNestedString(t, answer, "workflow", "run_id")))
	assertWorkspaceAnalysisDurableChain(t, ctx, pool, answerID)
	assertWorkspaceAnalysisReceiptLossRecovery(t, ctx, pool, answerID, completionLoss, gitExecutions)
}

func seedWorkspaceAnalysisGitRepository(t *testing.T, ctx context.Context, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("Workspace analysis integration repository.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "--quiet", root},
		{"-C", root, "config", "user.email", "workspace-analysis@example.test"},
		{"-C", root, "config", "user.name", "Workspace Analysis Integration"},
		{"-C", root, "add", "README.md"},
		{"-C", root, "commit", "--quiet", "-m", "initial workspace analysis fixture"},
	} {
		command := exec.CommandContext(ctx, "git", args...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
		}
	}
}

// seedWorkspaceAnalysisConversationKnowledge creates the same bounded
// retrieval provenance as the RAG fixture, but with the verified Workspace
// state required by ReadGitStatus@2.
func seedWorkspaceAnalysisConversationKnowledge(t *testing.T, ctx context.Context, pool *pgxpool.Pool, root string) {
	t.Helper()
	content := []byte("Approved recovery replays durable facts without duplicating provider work.")
	digest := sha256.Sum256(content)
	contentHash := hex.EncodeToString(digest[:])
	at := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	managedLocation := filepath.Join(".knowledge", "sources", contentHash)
	artifactPath := filepath.Join(root, managedLocation)
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifactPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	parserHash, normalizedHash := composeRAGHash("rag-parser"), composeRAGHash("rag-normalized")
	statements := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO core.workspace(id,name,root_path,root_fingerprint,binding_version,git_repository_path,git_checked_at,status,availability,availability_reason,availability_checked_at,version,created_at,updated_at) VALUES($1,'workspace-analysis-conversation-smoke',$2,repeat('a',64),1,$2,$3,'active','available',NULL,$3,1,$3,$3)`, []any{string(ragSmokeWorkspaceID), root, at}},
		{`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at) VALUES($1,$2,$3,$4,$5,$6)`, []any{string(ragSmokeArtifactID), string(ragSmokeWorkspaceID), contentHash, int64(len(content)), managedLocation, at}},
		{`INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at) VALUES($1,$2,'text','Recovery','docs/recovery.txt',$3)`, []any{string(ragSmokeSourceID), string(ragSmokeWorkspaceID), at}},
		{`INSERT INTO core.source_version(id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at) VALUES($1,$2,$3,$4,$5,$6,'text/plain','docs/recovery.txt','passed',$7)`, []any{string(ragSmokeSourceVersionID), string(ragSmokeSourceID), string(ragSmokeWorkspaceID), string(ragSmokeArtifactID), contentHash, int64(len(content)), at}},
		{`INSERT INTO ingestion.parse_projection(id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,warnings,created_at) VALUES($1,$2,$3,'text','v1',$4,'v1',$5,'[]',$6)`, []any{string(ragSmokeProjectionID), string(ragSmokeWorkspaceID), string(ragSmokeArtifactID), parserHash, normalizedHash, at}},
		{`INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at) VALUES($1,$2,$3,$4)`, []any{string(ragSmokeSourceVersionID), string(ragSmokeProjectionID), string(ragSmokeWorkspaceID), at}},
		{`INSERT INTO ingestion.attempt(id,workspace_id,source_version_id,parse_projection_id,status,security_status,failure_stage,error_code,retryable,parser_id,parser_version,parser_config_hash,chunk_strategy_version,schema_version,idempotency_key,attempt_number,started_at,completed_at,version) VALUES('a1400000-0000-4000-8000-000000000012',$1,$2,$3,'chunked','passed','','',false,'text','v1',$4,'structure-v1','v1','workspace-analysis-smoke-ingestion',1,$5,$5,1)`, []any{string(ragSmokeWorkspaceID), string(ragSmokeSourceVersionID), string(ragSmokeProjectionID), parserHash, at}},
		{`INSERT INTO ingestion.source_span(id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,selector,excerpt_hash,parser_version,schema_version,created_at) VALUES($1,$2,$3,$4,'paragraph',1,1,0,$5,'{"kind":"paragraph"}'::jsonb,$6,'v1','v1',$7)`, []any{string(ragSmokeSpanID), string(ragSmokeWorkspaceID), string(ragSmokeArtifactID), string(ragSmokeProjectionID), int64(len(content)), contentHash, at}},
		{`INSERT INTO ingestion.canonical_chunk(id,workspace_id,parse_projection_id,sequence,heading_path,content,content_hash,source_span_id,byte_count,rune_count,parser_version,chunk_strategy_version,schema_version,atomic_oversized,status,created_at) VALUES($1,$2,$3,0,'["Recovery"]',$4,$5,$6,$7,$7,'v1','structure-v1','v1',false,'active',$8)`, []any{string(ragSmokeChunkID), string(ragSmokeWorkspaceID), string(ragSmokeProjectionID), string(content), contentHash, string(ragSmokeSpanID), int64(len(content)), at}},
		{`INSERT INTO retrieval.index_version(id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,version,created_at,updated_at,source_manifest_hash,expected_source_count,source_parser_id,source_parser_version,source_parser_config_hash,source_chunk_strategy_version,source_schema_version) VALUES($1,$2,'postgres-simple','v1',$3,'{}','workspace-analysis-smoke',$4,1,'workspace-analysis-smoke','building','["vector"]',1,$5,$5,$6,1,'text','v1',$3,'structure-v1','v1')`, []any{string(ragSmokeIndexID), string(ragSmokeWorkspaceID), parserHash, composeRAGHash("rag-manifest"), at, composeRAGHash("rag-source-manifest")}},
		{`INSERT INTO retrieval.index_manifest_chunk(index_version_id,chunk_id,workspace_id,content_hash,sequence,parser_version,chunk_strategy_version,schema_version,created_at) VALUES($1,$2,$3,$4,0,'v1','structure-v1','v1',$5)`, []any{string(ragSmokeIndexID), string(ragSmokeChunkID), string(ragSmokeWorkspaceID), contentHash, at}},
		{`INSERT INTO retrieval.index_manifest_source(index_version_id,workspace_id,source_id,source_version_id,parse_projection_id,selection_status,created_at) VALUES($1,$2,$3,$4,$5,'included',$6)`, []any{string(ragSmokeIndexID), string(ragSmokeWorkspaceID), string(ragSmokeSourceID), string(ragSmokeSourceVersionID), string(ragSmokeProjectionID), at}},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	retrievalRepository, err := retrievalpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := retrievalRepository.BuildLexical(ctx, retrievaldomain.LexicalBuildCommand{WorkspaceID: ragSmokeWorkspaceID, IndexVersionID: ragSmokeIndexID, ExpectedIndexVersion: 1, At: at.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	service, err := retrievalapplication.NewService(retrievalapplication.Dependencies{Store: retrievalRepository, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.FixedClock{Value: at.Add(2 * time.Second)}})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := service.Ready(ctx, retrievalapplication.TransitionRequest{WorkspaceID: ragSmokeWorkspaceID, IndexVersionID: ragSmokeIndexID, ExpectedVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Activate(ctx, retrievalapplication.ActivateRequest{WorkspaceID: ragSmokeWorkspaceID, TargetIndexVersionID: ragSmokeIndexID, ExpectedTargetVersion: ready.Version, IdempotencyKey: "workspace-analysis-smoke:activate", ReasonCode: "SMOKE"}); err != nil {
		t.Fatal(err)
	}
	seedComposeRAGKnowledge(t, ctx, pool, ragSmokeWorkspaceID, ragSmokeSourceVersionID, ragSmokeSpanID)
}

func newWorkspaceAnalysisConversationIntegrationRuntime(
	t *testing.T,
	pool *pgxpool.Pool,
	model workspaceAnalysisIntegrationModel,
	fault *workspaceAnalysisConversationRuntimeFault,
) (http.Handler, *riveradapter.Client) {
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
	workspaceRepository, err := workspacepostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	searchRepository, err := retrievalpostgres.NewSearchRepository(pool)
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
	searchService, err := retrievalapplication.NewSearchService(searchRepository, nil, nil)
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
	toolRepository, err := toolpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	toolContracts, err := toolcatalog.NewFrozenContractRegistry()
	if err != nil {
		t.Fatal(err)
	}
	toolRegistry := toolsapplication.NewExecutionRegistry()
	workspaceTools, err := newWorkspaceAnalysisToolExecutors(workspaceRepository, toolRepository, searchService, searchRepository, evidenceReference, eligibility)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range workspaceTools {
		if fault != nil {
			if fault.toolExecutions != nil && item.ref == (toolsdomain.ToolRef{Name: "ReadGitStatus", Version: 2}) {
				item.executor = fault.toolExecutions.wrap(item.ref, item.executor)
			}
			if fault.decorateTool != nil {
				item.executor = fault.decorateTool(item.ref, item.executor)
			}
		}
		contract, resolveErr := toolContracts.ResolveContract(item.ref)
		if resolveErr != nil {
			t.Fatal(resolveErr)
		}
		if err := toolRegistry.RegisterContract(contract); err != nil {
			t.Fatal(err)
		}
		if err := toolRegistry.RegisterExecutor(item.ref, item.executor); err != nil {
			t.Fatal(err)
		}
	}
	if err := toolRegistry.Freeze(); err != nil {
		t.Fatal(err)
	}
	toolExecution, err := toolsapplication.NewExecutionService(toolRegistry, toolRepository, toolRepository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	modelRef := agentdomain.ModelRef{AdapterName: "integration", AdapterVersion: "v1", ModelID: "workspace-analysis", ModelVersion: "v1"}
	catalog, err := agentworkflow.NewRuntimeCatalog(agentworkflow.CatalogOptions{Model: modelRef, Timeout: 2 * time.Second, MaxOutputTokens: int(agentapplication.WorkspaceAnalysisV1SynthesisMaxOutputTokens)})
	if err != nil {
		t.Fatal(err)
	}
	finalizerStore, err := conversationpostgres.NewWorkspaceAnalysisFinalizer(pool, events, foundation.NewUUIDGenerator(nil))
	if err != nil {
		t.Fatal(err)
	}
	finalizer, err := agentworkflow.NewWorkspaceAnalysisFinalizerWithMetrics(finalizerStore, nil)
	if err != nil {
		t.Fatal(err)
	}
	draftStreams, err := conversationpostgres.NewDraftStreamRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	candidateRuntime, err := agenteino.NewWorkspaceAnalysisCandidateStreamRuntime(model)
	if err != nil {
		t.Fatal(err)
	}
	modelOperations := agentapplication.WorkspaceAnalysisModelOperationRepository(agentRepository)
	if fault != nil && fault.modelOperationRepository != nil {
		fault.modelOperationRepository.Repository = agentRepository
		modelOperations = fault.modelOperationRepository
	}
	planner, err := agentapplication.NewWorkspaceAnalysisRetrievalPlanRunner(agentapplication.WorkspaceAnalysisRetrievalPlanRunnerDependencies{Model: model, Catalog: catalog, Repository: agentRepository, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	candidateDrafts, err := agentworkflow.NewWorkspaceAnalysisCandidateDraftCoordinator(agentworkflow.WorkspaceAnalysisCandidateDraftCoordinatorDependencies{Store: draftStreams, Loader: draftStreams})
	if err != nil {
		t.Fatal(err)
	}
	synthesis, err := agentapplication.NewWorkspaceAnalysisSynthesisRunner(agentapplication.WorkspaceAnalysisSynthesisRunnerDependencies{Runtime: candidateRuntime, Catalog: catalog, Repository: modelOperations, Drafts: candidateDrafts, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	review, err := agentapplication.NewWorkspaceAnalysisReviewRunner(agentapplication.WorkspaceAnalysisReviewRunnerDependencies{Model: model, Catalog: catalog, Repository: modelOperations, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}})
	if err != nil {
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
	cancellationTerminal, err := conversationpostgres.NewWorkspaceAnalysisCancellationTerminalHook(events, foundation.NewUUIDGenerator(nil))
	if err != nil {
		t.Fatal(err)
	}
	runtimeRepository, err := workflowpostgres.NewRuntimeRepositoryWithHooks(pool, inserter, workflowpostgres.RuntimeRepositoryHooks{Terminal: cancellationTerminal})
	if err != nil {
		t.Fatal(err)
	}
	components := agentWorkflowComponents{}
	components.workspaceInspect, err = agentworkflow.NewWorkspaceAnalysisInspectExecutor(agentworkflow.WorkspaceAnalysisInspectExecutorDependencies{Context: conversationRepository, Runs: agentRepository, Tools: toolExecution, Finalizer: finalizer, Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	components.workspaceRetrieve, err = agentworkflow.NewWorkspaceAnalysisRetrieveExecutor(agentworkflow.WorkspaceAnalysisRetrieveExecutorDependencies{Context: conversationRepository, Runs: agentRepository, Inputs: runtimeRepository, Stages: runtimeRepository, Retrieval: searchRepository, Planner: planner, Tools: toolExecution, Receipts: toolRepository, Finalizer: finalizer, Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	components.workspaceRead, err = agentworkflow.NewWorkspaceAnalysisReadEvidenceExecutor(agentworkflow.WorkspaceAnalysisReadEvidenceExecutorDependencies{Context: conversationRepository, Runs: agentRepository, Inputs: runtimeRepository, Stages: runtimeRepository, Receipts: toolRepository, Tools: toolExecution, Finalizer: finalizer, Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	components.workspaceSynthesize, err = agentworkflow.NewWorkspaceAnalysisSynthesizeExecutor(agentworkflow.WorkspaceAnalysisSynthesizeExecutorDependencies{Context: conversationRepository, Runs: agentRepository, Inputs: runtimeRepository, Stages: runtimeRepository, Evidence: toolRepository, Synthesis: synthesis, Finalizer: finalizer, Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	components.workspaceValidate, err = agentworkflow.NewWorkspaceAnalysisValidateCitationsExecutor(agentworkflow.WorkspaceAnalysisValidateCitationsExecutorDependencies{Context: conversationRepository, Runs: agentRepository, Inputs: runtimeRepository, Stages: runtimeRepository, Authority: toolRepository, Receipts: toolRepository, Tools: toolExecution, Finalizer: finalizer, Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	components.workspaceReview, err = agentworkflow.NewWorkspaceAnalysisReviewPublishExecutor(agentworkflow.WorkspaceAnalysisReviewPublishExecutorDependencies{Context: conversationRepository, Runs: agentRepository, Inputs: runtimeRepository, Stages: runtimeRepository, Candidates: agentRepository, Evidence: toolRepository, Validation: toolRepository, Review: review, Finalizer: finalizer, Clock: foundation.SystemClock{}})
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
	if fault != nil && fault.decorateInspect != nil {
		items := []struct {
			key      string
			executor workflowapplication.Executor
		}{
			{conversationworkflow.WorkspaceAnalysisNodeInspectWorkspace, fault.decorateInspect(components.workspaceInspect)},
			{conversationworkflow.WorkspaceAnalysisNodeRetrieveEvidence, components.workspaceRetrieve},
			{conversationworkflow.WorkspaceAnalysisNodeReadEvidence, components.workspaceRead},
			{conversationworkflow.WorkspaceAnalysisNodeSynthesizeAnswer, components.workspaceSynthesize},
			{conversationworkflow.WorkspaceAnalysisNodeValidateCitations, components.workspaceValidate},
			{conversationworkflow.WorkspaceAnalysisNodeReviewPublish, components.workspaceReview},
		}
		for _, item := range items {
			node, found := workerWorkspaceAnalysisNode(item.key)
			if !found {
				t.Fatalf("workspace analysis node %s is missing", item.key)
			}
			if err := executors.Register(node.Kind, node.InputSchemaVersion, item.executor); err != nil {
				t.Fatal(err)
			}
		}
	} else if err := registerWorkerWorkspaceAnalysisExecutors(executors, components); err != nil {
		t.Fatal(err)
	}
	if err := executors.Freeze(); err != nil {
		t.Fatal(err)
	}
	definitions, err := workflowapplication.NewDefinitionRegistry(validation, executors, toolContracts)
	if err != nil {
		t.Fatal(err)
	}
	if err := definitions.Register(conversationworkflow.RegisteredWorkspaceAnalysisDefinition()); err != nil {
		t.Fatal(err)
	}
	if err := definitions.Freeze(); err != nil {
		t.Fatal(err)
	}
	coordinator, err := workflowapplication.NewRuntimeCoordinator(runtimeRepository)
	if err != nil {
		t.Fatal(err)
	}
	var runtime riveradapter.RuntimeExecutionCoordinator = coordinator
	if fault != nil && fault.decorateCoordinator != nil {
		runtime = fault.decorateCoordinator(coordinator)
	}
	runtimeWorker, err := riveradapter.NewRuntimeNodeWorker(executors, runtime, "workspace-analysis-conversation-integration", 5*time.Second, time.Second)
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
	var starter agentapplication.WorkspaceAnalysisRunStarter = newWorkspaceAnalysisIntegrationRunStarter(t, agentRepository, catalog)
	if fault != nil && fault.decorateRunStarter != nil {
		starter = fault.decorateRunStarter(starter)
	}
	dispatcher, err := conversationpostgres.NewQuestionDispatcherWithWorkspaceAnalysis(pool, runtimeRepository, events, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, starter)
	if err != nil {
		t.Fatal(err)
	}
	service, err := conversationapplication.NewService(conversationapplication.Dependencies{Repository: conversationRepository, QuestionDispatcher: dispatcher, FeedbackRepository: conversationRepository, WorkspaceAnalysisTimelineReader: conversationRepository, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	conversationHandler := conversationhttp.NewHandler(service, conversationhttp.NewCursorCodec())
	eventHandler := eventshttp.NewHandler(events, eventshttp.StreamConfig{PollInterval: 10 * time.Millisecond, HeartbeatInterval: 100 * time.Millisecond})
	workflowRepository, err := workflowpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	workflowService, err := workflowapplication.NewRuntimeService(workflowRepository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, workflowapplication.RuntimeDependencies{Definitions: definitions, Starter: runtimeRepository, State: runtimeRepository})
	if err != nil {
		t.Fatal(err)
	}
	router := app.NewRouter(app.Dependencies{Version: "workspace-analysis-integration", Database: pool, Conversation: conversationHandler, Workflow: workflowhttp.NewHandler(workflowService), Events: eventHandler, RAGEnabled: true, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	return router, workerClient
}

func newWorkspaceAnalysisIntegrationRunStarter(t *testing.T, repository *agentpostgres.Repository, catalog *agentapplication.RuntimeCatalog) *agentapplication.WorkspaceAnalysisRunService {
	t.Helper()
	if repository == nil || catalog == nil {
		t.Fatal("workspace analysis integration dependencies are unavailable")
	}
	tools, err := toolcatalog.WorkspaceAnalysisToolCatalogSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	starter, err := agentapplication.NewWorkspaceAnalysisRunService(repository, foundation.NewUUIDGenerator(nil), agentapplication.WorkspaceAnalysisRunStartConfig{
		DefinitionHash: conversationworkflow.RegisteredWorkspaceAnalysisDefinition().GraphHash, ToolCatalogHash: tools.Hash, ConfigRevision: 1,
		Timeouts:                        agentapplication.WorkspaceAnalysisV1Timeouts{PlanModelTimeout: 2 * time.Second, SynthesisModelTimeout: 2 * time.Second, ReviewModelTimeout: 2 * time.Second, GitToolTimeout: tools.ReadGitStatusTimeout, SearchToolTimeout: tools.SearchKnowledgeTimeout, SourceReadToolTimeout: tools.ReadSourceTimeout, ValidateCitationToolTimeout: tools.ValidateCitationTimeout},
		SynthesisProfileMaxOutputTokens: int(agentapplication.WorkspaceAnalysisV1SynthesisMaxOutputTokens),
		RuntimeLimits:                   agentapplication.WorkspaceAnalysisRuntimeLimits{RiverJobTimeout: cfg.WorkerJobTimeout, LeaseDuration: cfg.WorkflowLeaseDuration, HeartbeatInterval: cfg.WorkflowHeartbeatInterval},
	})
	if err != nil {
		t.Fatal(err)
	}
	return starter
}

func waitForWorkspaceAnalysisAnswer(t *testing.T, ctx context.Context, pool *pgxpool.Pool, serverURL, answerID string) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		answer := doRAGGET(t, ctx, serverURL+"/api/v1/answers/"+answerID+"?workspace_id="+string(ragSmokeWorkspaceID), http.StatusOK)
		if answer["publication_status"] == "completed" {
			return
		}
		if workflow, ok := answer["workflow"].(map[string]any); ok {
			status, _ := workflow["status"].(string)
			if status == "failed" || status == "cancelled" {
				var runStatus, reason string
				_ = pool.QueryRow(ctx, `SELECT status,COALESCE(termination_reason,'') FROM agent.workspace_analysis_run WHERE answer_id=$1`, answerID).Scan(&runStatus, &reason)
				var nodes, tools string
				_ = pool.QueryRow(ctx, `SELECT COALESCE(string_agg(node_key||':'||status||':'||COALESCE(error_code,''),',' ORDER BY node_key),'') FROM workflow.node_run WHERE run_id=$1`, workflow["run_id"]).Scan(&nodes)
				_ = pool.QueryRow(ctx, `SELECT COALESCE(string_agg(requested_tool_name||'@'||tool_version::text||':'||status||':'||COALESCE(error_code,''),',' ORDER BY call_no),'') FROM workflow.tool_call WHERE workflow_run_id=$1`, workflow["run_id"]).Scan(&tools)
				t.Fatalf("workspace analysis workflow terminated as %s run=%s reason=%s nodes=%s tools=%s answer=%v", status, runStatus, reason, nodes, tools, answer)
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
}

func assertCompletedWorkspaceAnalysisAnswer(t *testing.T, answer map[string]any) {
	t.Helper()
	if answer["publication_status"] != "completed" || answer["result_type"] != "workspace_analysis" {
		t.Fatalf("workspace analysis answer=%v", answer)
	}
	result, ok := answer["result"].(map[string]any)
	if !ok {
		t.Fatalf("workspace analysis result=%T", answer["result"])
	}
	payload, ok := result["payload"].(map[string]any)
	if !ok || payload["answer_markdown"] != workspaceAnalysisSmokeAnswer {
		t.Fatalf("workspace analysis payload=%v", payload)
	}
}

func waitForWorkspaceAnalysisWorkflowCompleted(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workflowRunID foundation.ID) {
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
			t.Fatalf("workspace analysis workflow reached unexpected terminal state %s", status)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
}

func assertWorkspaceAnalysisDurableChain(t *testing.T, ctx context.Context, pool *pgxpool.Pool, answerID foundation.ID) {
	t.Helper()
	var runStatus, workflowStatus, terminationReason string
	var nodeCount, receiptCount, candidateCount, modelCallCount int
	if err := pool.QueryRow(ctx, `
		SELECT analysis.status,workflow.status,COALESCE(analysis.termination_reason,''),
			(SELECT count(*) FROM workflow.node_run WHERE run_id=analysis.workflow_run_id),
			(SELECT count(*) FROM workflow.tool_result_receipt WHERE workflow_run_id=analysis.workflow_run_id),
			(SELECT count(*) FROM agent.workspace_analysis_candidate WHERE analysis_run_id=analysis.id),
			(SELECT count(*) FROM agent.workspace_analysis_operation operation JOIN agent.model_call call ON call.id=operation.model_call_id WHERE operation.analysis_run_id=analysis.id)
		FROM agent.workspace_analysis_run analysis
		JOIN workflow.run workflow ON workflow.id=analysis.workflow_run_id
		WHERE analysis.answer_id=$1`, string(answerID)).Scan(&runStatus, &workflowStatus, &terminationReason, &nodeCount, &receiptCount, &candidateCount, &modelCallCount); err != nil {
		t.Fatal(err)
	}
	if runStatus != string(agentdomain.WorkspaceAnalysisRunSucceeded) || workflowStatus != string(workflowdomain.RunStatusSucceeded) || terminationReason != string(agentdomain.WorkspaceAnalysisRunCompleted) || nodeCount != 6 || receiptCount != 4 || candidateCount != 1 || modelCallCount != 3 {
		t.Fatalf("workspace analysis durable chain run=%s workflow=%s reason=%s nodes=%d receipts=%d candidates=%d model_calls=%d", runStatus, workflowStatus, terminationReason, nodeCount, receiptCount, candidateCount, modelCallCount)
	}
}

func assertWorkspaceAnalysisReceiptLossBeforeReclaim(t *testing.T, ctx context.Context, pool *pgxpool.Pool, answerID foundation.ID, output json.RawMessage) {
	t.Helper()
	if len(output) == 0 || !strings.Contains(string(output), `"tool_receipt_id"`) {
		t.Fatalf("injected completion did not receive the canonical inspect output: %s", output)
	}
	var operationStatus, callStatus, reservationStatus, nodeStatus, attemptStatus string
	var firstAttemptID, latestAttemptID, receiptAttemptID string
	err := pool.QueryRow(ctx, `
		SELECT operation.status,call.status,reservation.status,node.status,attempt.status,
			operation.first_node_attempt_id::text,operation.latest_node_attempt_id::text,receipt.node_attempt_id::text
		FROM agent.workspace_analysis_run AS analysis
		JOIN workflow.node_run AS node ON node.run_id=analysis.workflow_run_id AND node.node_key='inspect_workspace'
		JOIN agent.workspace_analysis_operation AS operation ON operation.analysis_run_id=analysis.id AND operation.node_run_id=node.id
		JOIN workflow.tool_call AS call ON call.id=operation.tool_call_id
		JOIN agent.workspace_analysis_budget_reservation AS reservation ON reservation.operation_id=operation.id
		JOIN workflow.tool_result_receipt AS receipt ON receipt.id=operation.result_id
		JOIN workflow.node_attempt AS attempt ON attempt.id=operation.first_node_attempt_id
		WHERE analysis.answer_id=$1`, string(answerID)).Scan(
		&operationStatus, &callStatus, &reservationStatus, &nodeStatus, &attemptStatus,
		&firstAttemptID, &latestAttemptID, &receiptAttemptID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if operationStatus != "SUCCEEDED" || callStatus != "SUCCEEDED" || reservationStatus != "SETTLED" ||
		nodeStatus != "running" || attemptStatus != "running" || firstAttemptID != latestAttemptID || firstAttemptID != receiptAttemptID {
		t.Fatalf("receipt loss checkpoint operation=%s call=%s reservation=%s node=%s attempt=%s first=%s latest=%s receipt_attempt=%s",
			operationStatus, callStatus, reservationStatus, nodeStatus, attemptStatus, firstAttemptID, latestAttemptID, receiptAttemptID)
	}
}

func assertWorkspaceAnalysisReceiptLossRecovery(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	answerID foundation.ID,
	completionLoss *failFirstWorkspaceAnalysisCompletion,
	gitExecutions *workspaceAnalysisToolExecutionCounter,
) {
	t.Helper()
	if !completionLoss.outputsMatch() {
		t.Fatal("replacement delivery did not complete the same canonical inspect output")
	}
	if got := gitExecutions.count(toolsdomain.ToolRef{Name: "ReadGitStatus", Version: 2}); got != 1 {
		t.Fatalf("ReadGitStatus executor calls=%d want=1", got)
	}

	var attempts, leaseLost, succeeded, firstRiverAttempt, replacementRiverAttempt int
	if err := pool.QueryRow(ctx, `
		SELECT count(*),count(*) FILTER (WHERE attempt.status='lease_lost'),count(*) FILTER (WHERE attempt.status='succeeded'),
			min(attempt.river_job_attempt),max(attempt.river_job_attempt)
		FROM agent.workspace_analysis_run AS analysis
		JOIN workflow.node_run AS node ON node.run_id=analysis.workflow_run_id AND node.node_key='inspect_workspace'
		JOIN workflow.node_attempt AS attempt ON attempt.node_run_id=node.id
		WHERE analysis.answer_id=$1`, string(answerID)).Scan(&attempts, &leaseLost, &succeeded, &firstRiverAttempt, &replacementRiverAttempt); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || leaseLost != 1 || succeeded != 1 || replacementRiverAttempt <= firstRiverAttempt {
		t.Fatalf("inspect replacement attempts=%d lease_lost=%d succeeded=%d river_attempts=%d->%d", attempts, leaseLost, succeeded, firstRiverAttempt, replacementRiverAttempt)
	}

	var toolCalls, receipts, reservations, settledToolCalls, searchCalls int
	var firstAttemptID, latestAttemptID, callAttemptID, receiptAttemptID string
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM workflow.tool_call AS call WHERE call.workflow_run_id=analysis.workflow_run_id AND call.requested_tool_name='ReadGitStatus' AND call.tool_version=2),
			(SELECT count(*) FROM workflow.tool_result_receipt AS receipt WHERE receipt.workflow_run_id=analysis.workflow_run_id AND receipt.tool_name='ReadGitStatus' AND receipt.tool_version=2),
			(SELECT count(*) FROM agent.workspace_analysis_budget_reservation AS reservation WHERE reservation.operation_id=operation.id),
			analysis.settled_tool_calls,
			(SELECT count(*) FROM workflow.tool_call AS call WHERE call.workflow_run_id=analysis.workflow_run_id AND call.requested_tool_name='SearchKnowledge' AND call.tool_version=2),
			operation.first_node_attempt_id::text,operation.latest_node_attempt_id::text,call.node_attempt_id::text,receipt.node_attempt_id::text
		FROM agent.workspace_analysis_run AS analysis
		JOIN agent.workspace_analysis_operation AS operation ON operation.analysis_run_id=analysis.id AND operation.node_key='inspect_workspace' AND operation.operation_kind='GIT_STATUS'
		JOIN workflow.tool_call AS call ON call.id=operation.tool_call_id
		JOIN workflow.tool_result_receipt AS receipt ON receipt.id=operation.result_id
		WHERE analysis.answer_id=$1`, string(answerID)).Scan(
		&toolCalls, &receipts, &reservations, &settledToolCalls, &searchCalls,
		&firstAttemptID, &latestAttemptID, &callAttemptID, &receiptAttemptID,
	); err != nil {
		t.Fatal(err)
	}
	if toolCalls != 1 || receipts != 1 || reservations != 1 || settledToolCalls != 4 || searchCalls != 1 ||
		firstAttemptID != callAttemptID || firstAttemptID != receiptAttemptID || latestAttemptID == firstAttemptID {
		t.Fatalf("receipt recovery git_calls=%d receipts=%d reservations=%d settled_tool_calls=%d search_calls=%d first=%s latest=%s call_attempt=%s receipt_attempt=%s",
			toolCalls, receipts, reservations, settledToolCalls, searchCalls, firstAttemptID, latestAttemptID, callAttemptID, receiptAttemptID)
	}
}

type workspaceAnalysisConversationRuntimeFault struct {
	decorateCoordinator      func(*workflowapplication.RuntimeCoordinator) riveradapter.RuntimeExecutionCoordinator
	decorateTool             func(toolsdomain.ToolRef, toolsapplication.Executor) toolsapplication.Executor
	decorateInspect          func(workflowapplication.Executor) workflowapplication.Executor
	decorateRunStarter       func(agentapplication.WorkspaceAnalysisRunStarter) agentapplication.WorkspaceAnalysisRunStarter
	modelOperationRepository *workspaceAnalysisCancelBeforeCandidateFinalizer
	toolExecutions           *workspaceAnalysisToolExecutionCounter
}

type workspaceAnalysisIntegrationModel interface {
	agentapplication.ChatModel
	einomodel.ToolCallingChatModel
}

type workspaceAnalysisToolExecutionCounter struct {
	mu     sync.Mutex
	counts map[toolsdomain.ToolRef]int
}

func (counter *workspaceAnalysisToolExecutionCounter) wrap(ref toolsdomain.ToolRef, delegate toolsapplication.Executor) toolsapplication.Executor {
	return workspaceAnalysisCountingToolExecutor{counter: counter, ref: ref, delegate: delegate}
}

func (counter *workspaceAnalysisToolExecutionCounter) count(ref toolsdomain.ToolRef) int {
	counter.mu.Lock()
	defer counter.mu.Unlock()
	return counter.counts[ref]
}

type workspaceAnalysisCountingToolExecutor struct {
	counter  *workspaceAnalysisToolExecutionCounter
	ref      toolsdomain.ToolRef
	delegate toolsapplication.Executor
}

func (executor workspaceAnalysisCountingToolExecutor) Execute(ctx context.Context, request toolsapplication.ExecutorRequest) (toolsapplication.ExecutorResult, error) {
	executor.counter.mu.Lock()
	if executor.counter.counts == nil {
		executor.counter.counts = make(map[toolsdomain.ToolRef]int)
	}
	executor.counter.counts[executor.ref]++
	executor.counter.mu.Unlock()
	return executor.delegate.Execute(ctx, request)
}

type failFirstWorkspaceAnalysisCompletion struct {
	delegate riveradapter.RuntimeExecutionCoordinator
	first    json.RawMessage
	second   json.RawMessage
	ready    chan struct{}
	mu       sync.Mutex
	calls    int
}

func (fault *failFirstWorkspaceAnalysisCompletion) decorate(delegate *workflowapplication.RuntimeCoordinator) riveradapter.RuntimeExecutionCoordinator {
	fault.mu.Lock()
	defer fault.mu.Unlock()
	fault.delegate = delegate
	fault.ready = make(chan struct{})
	return fault
}

func (fault *failFirstWorkspaceAnalysisCompletion) Claim(ctx context.Context, command workflowapplication.ClaimCommand) (workflowapplication.ClaimResult, error) {
	return fault.delegate.Claim(ctx, command)
}

func (fault *failFirstWorkspaceAnalysisCompletion) Heartbeat(ctx context.Context, command workflowapplication.HeartbeatCommand) (workflowapplication.HeartbeatResult, error) {
	return fault.delegate.Heartbeat(ctx, command)
}

func (fault *failFirstWorkspaceAnalysisCompletion) Complete(ctx context.Context, command workflowapplication.CompleteDeliveryCommand) (workflowapplication.DeliveryTransitionResult, error) {
	fault.mu.Lock()
	fault.calls++
	if fault.calls == 1 {
		fault.first = append(json.RawMessage(nil), command.Output...)
		close(fault.ready)
		fault.mu.Unlock()
		return workflowapplication.DeliveryTransitionResult{}, foundation.NewError(
			foundation.ErrorRetryableFailure,
			"WORKSPACE_ANALYSIS_NODE_COMPLETION_RESPONSE_LOST",
			true,
			errors.New("injected response loss after canonical receipt commit"),
		)
	}
	if fault.calls == 2 {
		fault.second = append(json.RawMessage(nil), command.Output...)
	}
	fault.mu.Unlock()
	return fault.delegate.Complete(ctx, command)
}

func (fault *failFirstWorkspaceAnalysisCompletion) Fail(ctx context.Context, command workflowapplication.FailDeliveryCommand) (workflowapplication.DeliveryTransitionResult, error) {
	return fault.delegate.Fail(ctx, command)
}

func (fault *failFirstWorkspaceAnalysisCompletion) waitForFirstCompletion(t *testing.T, ctx context.Context) json.RawMessage {
	t.Helper()
	select {
	case <-fault.ready:
		fault.mu.Lock()
		defer fault.mu.Unlock()
		return append(json.RawMessage(nil), fault.first...)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		return nil
	}
}

func (fault *failFirstWorkspaceAnalysisCompletion) outputsMatch() bool {
	fault.mu.Lock()
	defer fault.mu.Unlock()
	return fault.calls >= 2 && string(fault.first) == string(fault.second)
}

type workspaceAnalysisRequestAwareModel struct {
	mu    sync.Mutex
	calls int
}

func (model *workspaceAnalysisRequestAwareModel) Chat(_ context.Context, request agentapplication.ChatRequest) (agentapplication.ChatResponse, error) {
	model.mu.Lock()
	model.calls++
	model.mu.Unlock()
	var output any
	switch request.Phase {
	case agentdomain.ModelCallPlan:
		output = agentdomain.RAGQueryPlanProviderResultV2{Intent: "approved recovery evidence", Rewrites: []string{"approved recovery"}, ClarificationReason: "", ClarificationQuestion: "", SuggestedScopes: []string{}}
	case agentdomain.ModelCallReview:
		modelRunRef := workspaceAnalysisModelRunRef(request)
		if modelRunRef == "" {
			return agentapplication.ChatResponse{}, errors.New("workspace analysis candidate model_run_ref is missing")
		}
		output = agentdomain.FaithfulnessReviewResult{ResultType: agentdomain.ResultTypeFaithfulnessReview, SchemaID: agentdomain.FaithfulnessReviewSchemaID, SchemaVersion: agentdomain.OutputSchemaVersionV1, ModelRunRef: modelRunRef, Payload: agentdomain.FaithfulnessReviewPayload{Passed: true, Summary: "The candidate is supported by E1.", Items: []agentdomain.FaithfulnessReviewItem{{AssertionID: "@answer/conclusion", Verdict: agentdomain.FaithfulnessSupported, CitationIDs: []string{"E1"}, Reason: "E1 contains the recovery requirement."}}}}
	default:
		return agentapplication.ChatResponse{}, errors.New("unexpected Workspace Analysis model phase: " + string(request.Phase))
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return agentapplication.ChatResponse{}, err
	}
	return agentapplication.ChatResponse{Model: request.Model, Content: encoded, Usage: agentdomain.TokenUsage{InputTokens: 20, OutputTokens: 10, TotalTokens: 30}}, nil
}

func (model *workspaceAnalysisRequestAwareModel) Stream(_ context.Context, messages []*schema.Message, options ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	model.mu.Lock()
	model.calls++
	model.mu.Unlock()
	resolved := einomodel.GetCommonOptions(nil, options...)
	if len(messages) == 0 || resolved.ToolChoice == nil || *resolved.ToolChoice != schema.ToolChoiceForbidden {
		return nil, errors.New("workspace analysis synthesis stream contract drifted")
	}
	content := `{"result_type":"workspace_analysis_candidate","schema_id":"agent.workspace-analysis-candidate","schema_version":"1","payload":{"answer_markdown":"The approved recovery evidence requires durable replay.","citation_refs":["E1"],"proposal_suggestion":null}}`
	reader, writer := schema.Pipe[*schema.Message](2)
	go func() {
		defer writer.Close()
		writer.Send(&schema.Message{Role: schema.Assistant, Content: content[:len(content)/2]}, nil)
		writer.Send(&schema.Message{Content: content[len(content)/2:]}, nil)
		writer.Send(&schema.Message{ResponseMeta: &schema.ResponseMeta{FinishReason: "stop", Usage: &schema.TokenUsage{PromptTokens: 20, CompletionTokens: 10, TotalTokens: 30}}}, nil)
	}()
	return reader, nil
}

func (model *workspaceAnalysisRequestAwareModel) Generate(_ context.Context, _ []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	return nil, errors.New("workspace analysis fixture must not call Eino Generate")
}

func (model *workspaceAnalysisRequestAwareModel) WithTools(tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	if len(tools) != 0 {
		return nil, errors.New("workspace analysis candidate stream unexpectedly received tools")
	}
	return model, nil
}

func (model *workspaceAnalysisRequestAwareModel) CallCount() int {
	model.mu.Lock()
	defer model.mu.Unlock()
	return model.calls
}

func workspaceAnalysisModelRunRef(request agentapplication.ChatRequest) foundation.ID {
	for _, message := range request.Messages {
		if ref := findRAGString(message.Content, "model_run_ref"); ref != "" {
			parsed, err := foundation.ParseID(ref)
			if err == nil {
				return parsed
			}
		}
	}
	return ""
}

var _ einomodel.ToolCallingChatModel = (*workspaceAnalysisRequestAwareModel)(nil)
