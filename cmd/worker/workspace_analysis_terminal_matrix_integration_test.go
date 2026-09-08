//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/jackc/pgx/v5/pgxpool"
)

type workspaceAnalysisTerminalModelMode string

const (
	workspaceAnalysisTerminalModelDefault      workspaceAnalysisTerminalModelMode = "default"
	workspaceAnalysisTerminalModelEmptySearch  workspaceAnalysisTerminalModelMode = "empty_search"
	workspaceAnalysisTerminalModelRejectReview workspaceAnalysisTerminalModelMode = "reject_review"
	workspaceAnalysisTerminalModelRefuse       workspaceAnalysisTerminalModelMode = "refuse"
	workspaceAnalysisTerminalModelClarify      workspaceAnalysisTerminalModelMode = "clarify"
	workspaceAnalysisTerminalModelFail         workspaceAnalysisTerminalModelMode = "fail"
)

type workspaceAnalysisTerminalToolFault string

const (
	workspaceAnalysisTerminalToolNone            workspaceAnalysisTerminalToolFault = ""
	workspaceAnalysisTerminalToolCitationInvalid workspaceAnalysisTerminalToolFault = "citation_invalid"
	workspaceAnalysisTerminalToolReceiptInvalid  workspaceAnalysisTerminalToolFault = "receipt_invalid"
	workspaceAnalysisTerminalToolUnknown         workspaceAnalysisTerminalToolFault = "unknown"
	workspaceAnalysisTerminalToolFailed          workspaceAnalysisTerminalToolFault = "failed"
)

type workspaceAnalysisTerminalScenario struct {
	name                         string
	reason                       agentdomain.WorkspaceAnalysisRunTerminationReason
	publicationStatus            string
	resultType                   string
	analysisStatus               agentdomain.WorkspaceAnalysisRunStatus
	workflowStatus               workflowdomain.RunStatus
	modelRunRef                  bool
	modelMode                    workspaceAnalysisTerminalModelMode
	toolFault                    workspaceAnalysisTerminalToolFault
	runtimeFailure               bool
	deadline                     bool
	cancel                       bool
	cancelAfterSynthesisProvider bool
	seam                         string
}

// TestPublicConversationWorkspaceAnalysisReachableTerminalMatrixThroughRiver
// drives every operationally reachable frozen-v1 terminal through the public
// Question API, a real PostgreSQL/River runtime, and the production finalizer
// or runtime terminal hook. BUDGET_EXHAUSTED is intentionally excluded because
// no valid v1 operation prefix can request beyond its frozen budget.
func TestPublicConversationWorkspaceAnalysisReachableTerminalMatrixThroughRiver(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))

	scenarios := []workspaceAnalysisTerminalScenario{
		{
			name: "completed", reason: agentdomain.WorkspaceAnalysisRunCompleted,
			publicationStatus: "completed", resultType: "workspace_analysis",
			analysisStatus: agentdomain.WorkspaceAnalysisRunSucceeded, workflowStatus: workflowdomain.RunStatusSucceeded,
			modelRunRef: true, modelMode: workspaceAnalysisTerminalModelDefault, seam: "model:review",
		},
		{
			name: "evidence_insufficient", reason: agentdomain.WorkspaceAnalysisRunEvidenceInsufficient,
			publicationStatus: "refused", resultType: "workspace_analysis_refusal",
			analysisStatus: agentdomain.WorkspaceAnalysisRunRefused, workflowStatus: workflowdomain.RunStatusFailed,
			modelMode: workspaceAnalysisTerminalModelEmptySearch, seam: "model:empty-search",
		},
		{
			name: "citation_invalid", reason: agentdomain.WorkspaceAnalysisRunCitationInvalid,
			publicationStatus: "refused", resultType: "workspace_analysis_refusal",
			analysisStatus: agentdomain.WorkspaceAnalysisRunRefused, workflowStatus: workflowdomain.RunStatusSucceeded,
			toolFault: workspaceAnalysisTerminalToolCitationInvalid, seam: "tool:citation-invalid",
		},
		{
			name: "faithfulness_rejected", reason: agentdomain.WorkspaceAnalysisRunFaithfulnessRejected,
			publicationStatus: "refused", resultType: "workspace_analysis_refusal",
			analysisStatus: agentdomain.WorkspaceAnalysisRunRefused, workflowStatus: workflowdomain.RunStatusSucceeded,
			modelMode: workspaceAnalysisTerminalModelRejectReview, seam: "model:faithfulness-rejected",
		},
		{
			name: "model_refused", reason: agentdomain.WorkspaceAnalysisRunModelRefused,
			publicationStatus: "refused", resultType: "workspace_analysis_refusal",
			analysisStatus: agentdomain.WorkspaceAnalysisRunRefused, workflowStatus: workflowdomain.RunStatusFailed,
			modelRunRef: true, modelMode: workspaceAnalysisTerminalModelRefuse, seam: "model:refused",
		},
		{
			name: "clarification_required", reason: agentdomain.WorkspaceAnalysisRunNeedsClarification,
			publicationStatus: "clarification_required", resultType: "clarification",
			analysisStatus: agentdomain.WorkspaceAnalysisRunClarificationRequired, workflowStatus: workflowdomain.RunStatusFailed,
			modelRunRef: true, modelMode: workspaceAnalysisTerminalModelClarify, seam: "model:clarification",
		},
		{
			name: "receipt_invalid", reason: agentdomain.WorkspaceAnalysisRunReceiptInvalid,
			publicationStatus: "failed", resultType: "workspace_analysis_termination",
			analysisStatus: agentdomain.WorkspaceAnalysisRunFailed, workflowStatus: workflowdomain.RunStatusFailed,
			toolFault: workspaceAnalysisTerminalToolReceiptInvalid, seam: "tool:receipt-invalid",
		},
		{
			name: "result_unknown", reason: agentdomain.WorkspaceAnalysisRunResultUnknown,
			publicationStatus: "failed", resultType: "workspace_analysis_termination",
			analysisStatus: agentdomain.WorkspaceAnalysisRunFailed, workflowStatus: workflowdomain.RunStatusFailed,
			toolFault: workspaceAnalysisTerminalToolUnknown, seam: "tool:unknown",
		},
		{
			name: "deadline_exceeded", reason: agentdomain.WorkspaceAnalysisRunDeadlineExceeded,
			publicationStatus: "failed", resultType: "workspace_analysis_termination",
			analysisStatus: agentdomain.WorkspaceAnalysisRunFailed, workflowStatus: workflowdomain.RunStatusFailed,
			deadline: true, seam: "runtime:expired-run",
		},
		{
			name: "model_failed", reason: agentdomain.WorkspaceAnalysisRunModelFailed,
			publicationStatus: "failed", resultType: "workspace_analysis_termination",
			analysisStatus: agentdomain.WorkspaceAnalysisRunFailed, workflowStatus: workflowdomain.RunStatusFailed,
			modelRunRef: true, modelMode: workspaceAnalysisTerminalModelFail, seam: "model:failed",
		},
		{
			name: "tool_failed", reason: agentdomain.WorkspaceAnalysisRunToolFailed,
			publicationStatus: "failed", resultType: "workspace_analysis_termination",
			analysisStatus: agentdomain.WorkspaceAnalysisRunFailed, workflowStatus: workflowdomain.RunStatusFailed,
			toolFault: workspaceAnalysisTerminalToolFailed, seam: "tool:failed",
		},
		{
			name: "runtime_failed", reason: agentdomain.WorkspaceAnalysisRunRuntimeFailed,
			publicationStatus: "failed", resultType: "workspace_analysis_termination",
			analysisStatus: agentdomain.WorkspaceAnalysisRunFailed, workflowStatus: workflowdomain.RunStatusFailed,
			runtimeFailure: true, seam: "runtime:inspect",
		},
		{
			name: "cancelled", reason: agentdomain.WorkspaceAnalysisRunCancellation,
			publicationStatus: "cancelled", resultType: "workspace_analysis_termination",
			analysisStatus: agentdomain.WorkspaceAnalysisRunCancelled, workflowStatus: workflowdomain.RunStatusCancelled,
			cancel: true, seam: "control:cancel",
		},
		{
			name: "cancelled_after_synthesis_provider_before_candidate_finalization", reason: agentdomain.WorkspaceAnalysisRunCancellation,
			publicationStatus: "cancelled", resultType: "workspace_analysis_termination",
			analysisStatus: agentdomain.WorkspaceAnalysisRunCancelled, workflowStatus: workflowdomain.RunStatusCancelled,
			cancelAfterSynthesisProvider: true, seam: "finalization:cancel-before-candidate",
		},
	}

	for _, scenario := range scenarios {
		scenario := scenario
		t.Run(scenario.name, func(t *testing.T) {
			runWorkspaceAnalysisTerminalScenario(t, baseURL, scenario)
		})
	}
}

func runWorkspaceAnalysisTerminalScenario(t *testing.T, baseURL string, scenario workspaceAnalysisTerminalScenario) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	database := newMigratedWorkerTestPool(t, baseURL)
	pool := database.DB()
	root := t.TempDir()
	seedWorkspaceAnalysisGitRepository(t, ctx, root)
	seedWorkspaceAnalysisConversationKnowledge(t, ctx, database, root)

	probe := &workspaceAnalysisTerminalProbe{}
	model := &workspaceAnalysisTerminalModel{mode: scenario.modelMode, probe: probe}
	fault := &workspaceAnalysisConversationRuntimeFault{}
	var cancellationBlocker *workspaceAnalysisCancellationExecutor
	var candidateFinalizationCancel *workspaceAnalysisCancelBeforeCandidateFinalizer
	if scenario.toolFault != workspaceAnalysisTerminalToolNone {
		fault.decorateTool = workspaceAnalysisTerminalToolDecorator(scenario.toolFault, probe)
	}
	if scenario.runtimeFailure {
		fault.decorateInspect = func(delegate workflowapplication.Executor) workflowapplication.Executor {
			return workspaceAnalysisRuntimeFailureExecutor{delegate: delegate, probe: probe}
		}
	}
	if scenario.cancel {
		cancellationBlocker = &workspaceAnalysisCancellationExecutor{entered: make(chan struct{}), probe: probe}
		fault.decorateInspect = func(workflowapplication.Executor) workflowapplication.Executor {
			return cancellationBlocker
		}
	}
	if scenario.cancelAfterSynthesisProvider {
		candidateFinalizationCancel = &workspaceAnalysisCancelBeforeCandidateFinalizer{pool: pool, probe: probe}
		fault.modelOperationRepository = candidateFinalizationCancel
	}
	if scenario.deadline {
		fault.decorateRunStarter = func(delegate agentapplication.ScopedWorkspaceAnalysisRunStarter) agentapplication.ScopedWorkspaceAnalysisRunStarter {
			return workspaceAnalysisExpiredRunStarter{delegate: delegate, probe: probe}
		}
	}
	if fault.decorateTool == nil && fault.decorateInspect == nil && fault.decorateRunStarter == nil && fault.modelOperationRepository == nil {
		fault = nil
	}
	router, workerClient := newWorkspaceAnalysisConversationIntegrationRuntime(t, database, model, fault)
	server := httptest.NewServer(router)
	defer server.Close()
	if candidateFinalizationCancel != nil {
		candidateFinalizationCancel.setServerURL(server.URL)
	}
	workerStarted := false
	startWorker := func() {
		t.Helper()
		if workerStarted {
			return
		}
		if err := workerClient.Start(ctx); err != nil {
			t.Fatal(err)
		}
		workerStarted = true
	}
	defer func() {
		if !workerStarted {
			return
		}
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = workerClient.Stop(stopCtx)
	}()
	startWorker()

	conversation := doRAGJSON(t, ctx, http.MethodPost, server.URL+"/api/v1/conversations", "workspace-analysis-terminal-"+scenario.name+"-conversation", map[string]any{
		"workspace_id": ragSmokeWorkspaceID, "title": "Workspace Analysis terminal " + scenario.name,
	}, http.StatusCreated)
	conversationID := mustRAGString(t, conversation, "id")
	question := "Approved recovery replays durable facts without duplicating provider work."
	if scenario.modelMode == workspaceAnalysisTerminalModelEmptySearch {
		question = "Which zzterminalmatrixzerozz policy is active?"
	}
	accepted := doRAGJSON(t, ctx, http.MethodPost, server.URL+"/api/v1/conversations/"+conversationID+"/questions", "workspace-analysis-terminal-"+scenario.name+"-question", map[string]any{
		"workspace_id": ragSmokeWorkspaceID,
		"question":     question,
		"scope":        map[string]any{"retrieval_mode": "keyword"},
		"answer_depth": "standard", "output_format": "markdown", "mode": "workspace_analysis",
	}, http.StatusAccepted)
	answerID := mustRAGNestedString(t, accepted, "answer", "id")
	var workflowRunID string
	if err := pool.QueryRow(ctx, `SELECT workflow_run_id::text FROM agent.workspace_analysis_run WHERE answer_id=$1`, answerID).Scan(&workflowRunID); err != nil {
		t.Fatal(err)
	}
	if scenario.cancel {
		cancellationBlocker.waitUntilClaimed(t, ctx)
		var version int64
		var runStatus, nodeStatus, attemptStatus string
		if err := pool.QueryRow(ctx, `SELECT w.version,w.status,n.status,a.status
			FROM workflow.run w
			JOIN workflow.node_run n ON n.run_id=w.id AND n.node_key='inspect_workspace'
			JOIN workflow.node_attempt a ON a.node_run_id=n.id AND a.attempt_no=n.attempt
			WHERE w.id=$1`, workflowRunID).Scan(&version, &runStatus, &nodeStatus, &attemptStatus); err != nil {
			t.Fatal(err)
		}
		if runStatus != string(workflowdomain.RunStatusRunning) || nodeStatus != string(workflowdomain.NodeStatusRunning) || attemptStatus != string(workflowdomain.AttemptStatusRunning) {
			t.Fatalf("cancel checkpoint was not claimed run=%s node=%s attempt=%s", runStatus, nodeStatus, attemptStatus)
		}
		doWorkspaceAnalysisTerminalCancel(t, ctx, server.URL, workflowRunID, version)
		probe.hit("control:cancel")
	}

	answer := waitForWorkspaceAnalysisTerminal(t, ctx, pool, server.URL, answerID, scenario.workflowStatus)
	if !workspaceAnalysisTerminalPublicAnswerMatches(answer, scenario) {
		logWorkspaceAnalysisTerminalDiagnostics(t, ctx, pool, answerID)
	}
	assertWorkspaceAnalysisTerminalPublicAnswer(t, answer, scenario)
	facts := loadWorkspaceAnalysisTerminalFacts(t, ctx, pool, answerID)
	assertWorkspaceAnalysisTerminalPublicModelBinding(t, answer, facts.modelRunID)
	assertWorkspaceAnalysisTerminalFacts(t, facts, scenario)
	assertWorkspaceAnalysisTerminalProof(t, ctx, pool, facts, scenario)
	assertWorkspaceAnalysisTerminalCausality(t, ctx, pool, facts, scenario)
	if scenario.cancelAfterSynthesisProvider {
		candidateFinalizationCancel.assertTriggered(t)
		if got := probe.count("model:stream"); got != 1 {
			t.Fatalf("synthesis Provider calls=%d want=1", got)
		}
		assertWorkspaceAnalysisCancelledSynthesisClosure(t, ctx, pool, facts.analysisRunID)
	}
	if got := probe.count(scenario.seam); got < 1 {
		t.Fatalf("scenario never crossed intended seam %q; probe=%v", scenario.seam, probe.snapshot())
	}
	assertWorkspaceAnalysisTerminalCallsStable(t, ctx, pool, facts.analysisRunID)
}

func workspaceAnalysisTerminalPublicAnswerMatches(answer map[string]any, scenario workspaceAnalysisTerminalScenario) bool {
	if answer["publication_status"] != scenario.publicationStatus || answer["result_type"] != scenario.resultType {
		return false
	}
	result, ok := answer["result"].(map[string]any)
	if !ok {
		return false
	}
	payload, ok := result["payload"].(map[string]any)
	if !ok {
		return false
	}
	switch scenario.resultType {
	case "workspace_analysis_refusal":
		return payload["reason_code"] == string(scenario.reason)
	case "workspace_analysis_termination":
		return payload["termination_reason"] == string(scenario.reason)
	default:
		return true
	}
}

func logWorkspaceAnalysisTerminalDiagnostics(t *testing.T, ctx context.Context, pool *pgxpool.Pool, answerID string) {
	t.Helper()
	var nodes, attempts, operations, modelCalls, toolCalls string
	_ = pool.QueryRow(ctx, `SELECT COALESCE(string_agg(n.node_key||':'||n.status||':'||COALESCE(n.error_code,''),',' ORDER BY n.node_key),'') FROM workflow.node_run n JOIN agent.workspace_analysis_run r ON r.workflow_run_id=n.run_id WHERE r.answer_id=$1`, answerID).Scan(&nodes)
	_ = pool.QueryRow(ctx, `SELECT COALESCE(string_agg(n.node_key||':'||a.status||':'||COALESCE(a.failure_class,'')||':'||COALESCE(a.error_code,''),',' ORDER BY a.started_at),'') FROM workflow.node_attempt a JOIN workflow.node_run n ON n.id=a.node_run_id JOIN agent.workspace_analysis_run r ON r.workflow_run_id=n.run_id WHERE r.answer_id=$1`, answerID).Scan(&attempts)
	_ = pool.QueryRow(ctx, `SELECT COALESCE(string_agg(o.node_key||':'||o.operation_kind||':'||o.status||':'||COALESCE(o.error_code,''),',' ORDER BY o.created_at),'') FROM agent.workspace_analysis_operation o JOIN agent.workspace_analysis_run r ON r.id=o.analysis_run_id WHERE r.answer_id=$1`, answerID).Scan(&operations)
	_ = pool.QueryRow(ctx, `SELECT COALESCE(string_agg(c.phase||':'||c.status||':'||COALESCE(m.error_code,''),',' ORDER BY c.started_at),'') FROM agent.model_call c JOIN agent.model_run m ON m.id=c.model_run_id JOIN agent.workspace_analysis_run r ON r.workflow_run_id=m.workflow_run_id WHERE r.answer_id=$1`, answerID).Scan(&modelCalls)
	_ = pool.QueryRow(ctx, `SELECT COALESCE(string_agg(c.requested_tool_name||':'||c.status||':'||COALESCE(c.error_code,''),',' ORDER BY c.started_at),'') FROM workflow.tool_call c JOIN agent.workspace_analysis_run r ON r.workflow_run_id=c.workflow_run_id WHERE r.answer_id=$1`, answerID).Scan(&toolCalls)
	t.Logf("terminal diagnostics nodes=%s attempts=%s operations=%s models=%s tools=%s", nodes, attempts, operations, modelCalls, toolCalls)
}

type workspaceAnalysisTerminalModel struct {
	mode  workspaceAnalysisTerminalModelMode
	probe *workspaceAnalysisTerminalProbe
}

func (model *workspaceAnalysisTerminalModel) Chat(_ context.Context, request agentapplication.ChatRequest) (agentapplication.ChatResponse, error) {
	response := agentapplication.ChatResponse{
		Model: request.Model, Content: []byte(`{"provider":"terminal"}`),
		Usage: agentdomain.TokenUsage{InputTokens: 20, OutputTokens: 10, TotalTokens: 30},
	}
	switch request.Phase {
	case agentdomain.ModelCallPlan:
		var output agentdomain.RAGQueryPlanProviderResultV2
		switch model.mode {
		case workspaceAnalysisTerminalModelEmptySearch:
			model.probe.hit("model:empty-search")
			output = agentdomain.RAGQueryPlanProviderResultV2{Intent: "find the absent marker", Rewrites: []string{"zzterminalmatrixzerozz"}, ClarificationReason: "", ClarificationQuestion: "", SuggestedScopes: []string{}}
		case workspaceAnalysisTerminalModelRefuse:
			// This is the controlled ChatModel port outcome. Raw Eino
			// message.refusal remains an invalid chat response by contract.
			model.probe.hit("model:refused")
			return response, foundation.NewError(
				foundation.ErrorNonRetryableFailure, string(agentdomain.WorkspaceAnalysisRunModelRefused), false,
				errors.New("injected exact model refusal"),
			)
		case workspaceAnalysisTerminalModelClarify:
			model.probe.hit("model:clarification")
			output = agentdomain.RAGQueryPlanProviderResultV2{Intent: "clarify workspace scope", Rewrites: []string{}, ClarificationReason: "the workspace area is missing", ClarificationQuestion: "Which workspace area should be analyzed?", SuggestedScopes: []string{"internal/agent"}}
		case workspaceAnalysisTerminalModelFail:
			model.probe.hit("model:failed")
			return response, foundation.NewError(
				foundation.ErrorNonRetryableFailure, "WORKSPACE_ANALYSIS_TEST_MODEL_PROVIDER_FAILED", false,
				errors.New("injected model provider failure"),
			)
		default:
			model.probe.hit("model:plan")
			output = agentdomain.RAGQueryPlanProviderResultV2{Intent: "approved recovery evidence", Rewrites: []string{"approved recovery"}, ClarificationReason: "", ClarificationQuestion: "", SuggestedScopes: []string{}}
		}
		encoded, err := json.Marshal(output)
		response.Content = encoded
		return response, err
	case agentdomain.ModelCallReview:
		modelRunRef := workspaceAnalysisModelRunRef(request)
		if modelRunRef == "" {
			return agentapplication.ChatResponse{}, errors.New("workspace analysis candidate model_run_ref is missing")
		}
		passed := model.mode != workspaceAnalysisTerminalModelRejectReview
		verdict := agentdomain.FaithfulnessSupported
		if !passed {
			model.probe.hit("model:faithfulness-rejected")
			verdict = agentdomain.FaithfulnessUnsupported
		} else {
			model.probe.hit("model:review")
		}
		output := agentdomain.FaithfulnessReviewResult{
			ResultType: agentdomain.ResultTypeFaithfulnessReview, SchemaID: agentdomain.FaithfulnessReviewSchemaID,
			SchemaVersion: agentdomain.OutputSchemaVersionV1, ModelRunRef: modelRunRef,
			Payload: agentdomain.FaithfulnessReviewPayload{
				Passed: passed, Summary: "The candidate was checked against E1.",
				Items: []agentdomain.FaithfulnessReviewItem{{AssertionID: "@answer/conclusion", Verdict: verdict, CitationIDs: []string{"E1"}, Reason: "checked against E1"}},
			},
		}
		encoded, err := json.Marshal(output)
		response.Content = encoded
		return response, err
	default:
		return agentapplication.ChatResponse{}, errors.New("unexpected Workspace Analysis model phase: " + string(request.Phase))
	}
}

func (model *workspaceAnalysisTerminalModel) Stream(_ context.Context, messages []*schema.Message, options ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	model.probe.hit("model:stream")
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

func (*workspaceAnalysisTerminalModel) Generate(context.Context, []*schema.Message, ...einomodel.Option) (*schema.Message, error) {
	return nil, errors.New("workspace analysis fixture must not call Eino Generate")
}

func (model *workspaceAnalysisTerminalModel) WithTools(tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	if len(tools) != 0 {
		return nil, errors.New("workspace analysis candidate stream unexpectedly received tools")
	}
	return model, nil
}

var _ workspaceAnalysisIntegrationModel = (*workspaceAnalysisTerminalModel)(nil)

type workspaceAnalysisTerminalProbe struct {
	mu     sync.Mutex
	counts map[string]int
}

func (probe *workspaceAnalysisTerminalProbe) hit(key string) {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if probe.counts == nil {
		probe.counts = make(map[string]int)
	}
	probe.counts[key]++
}

func (probe *workspaceAnalysisTerminalProbe) count(key string) int {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	return probe.counts[key]
}

func (probe *workspaceAnalysisTerminalProbe) snapshot() map[string]int {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	result := make(map[string]int, len(probe.counts))
	for key, value := range probe.counts {
		result[key] = value
	}
	return result
}

func workspaceAnalysisTerminalToolDecorator(
	fault workspaceAnalysisTerminalToolFault,
	probe *workspaceAnalysisTerminalProbe,
) func(toolsdomain.ToolRef, toolsapplication.Executor) toolsapplication.Executor {
	return func(ref toolsdomain.ToolRef, delegate toolsapplication.Executor) toolsapplication.Executor {
		targeted := (ref == (toolsdomain.ToolRef{Name: "ReadGitStatus", Version: 2}) &&
			(fault == workspaceAnalysisTerminalToolReceiptInvalid || fault == workspaceAnalysisTerminalToolUnknown || fault == workspaceAnalysisTerminalToolFailed)) ||
			(ref == (toolsdomain.ToolRef{Name: "ValidateCitation", Version: 3}) && fault == workspaceAnalysisTerminalToolCitationInvalid)
		if !targeted {
			return delegate
		}
		return workspaceAnalysisTerminalToolExecutor{fault: fault, ref: ref, delegate: delegate, probe: probe}
	}
}

type workspaceAnalysisTerminalToolExecutor struct {
	fault    workspaceAnalysisTerminalToolFault
	ref      toolsdomain.ToolRef
	delegate toolsapplication.Executor
	probe    *workspaceAnalysisTerminalProbe
}

func (executor workspaceAnalysisTerminalToolExecutor) Execute(ctx context.Context, request toolsapplication.ExecutorRequest) (toolsapplication.ExecutorResult, error) {
	switch executor.fault {
	case workspaceAnalysisTerminalToolReceiptInvalid:
		executor.probe.hit("tool:receipt-invalid")
		return toolsapplication.ExecutorResult{Output: json.RawMessage(`{"invalid":true}`)}, nil
	case workspaceAnalysisTerminalToolUnknown:
		executor.probe.hit("tool:unknown")
		return toolsapplication.ExecutorResult{}, foundation.NewError(
			foundation.ErrorManualRecoveryRequired, "WORKSPACE_ANALYSIS_TEST_TOOL_RESULT_UNKNOWN", false,
			errors.New("injected tool outcome uncertainty"),
		)
	case workspaceAnalysisTerminalToolFailed:
		executor.probe.hit("tool:failed")
		return toolsapplication.ExecutorResult{}, foundation.NewError(
			foundation.ErrorNonRetryableFailure, "WORKSPACE_ANALYSIS_TEST_TOOL_FAILED", false,
			errors.New("injected tool failure"),
		)
	case workspaceAnalysisTerminalToolCitationInvalid:
		result, err := executor.delegate.Execute(ctx, request)
		if err != nil {
			return toolsapplication.ExecutorResult{}, err
		}
		var output struct {
			Results []struct {
				EvidenceRef string `json:"evidence_ref"`
				ReasonCode  string `json:"reason_code"`
				Valid       bool   `json:"valid"`
			} `json:"results"`
		}
		if err := json.Unmarshal(result.Output, &output); err != nil || len(output.Results) == 0 {
			return toolsapplication.ExecutorResult{}, errors.New("canonical citation output is unavailable")
		}
		output.Results[0].Valid = false
		output.Results[0].ReasonCode = "EVIDENCE_INELIGIBLE"
		result.Output, err = json.Marshal(output)
		if err != nil {
			return toolsapplication.ExecutorResult{}, err
		}
		executor.probe.hit("tool:citation-invalid")
		return result, nil
	default:
		return executor.delegate.Execute(ctx, request)
	}
}

type workspaceAnalysisRuntimeFailureExecutor struct {
	delegate workflowapplication.Executor
	probe    *workspaceAnalysisTerminalProbe
}

type workspaceAnalysisCancellationExecutor struct {
	entered chan struct{}
	once    sync.Once
	probe   *workspaceAnalysisTerminalProbe
}

func (executor *workspaceAnalysisCancellationExecutor) Execute(ctx context.Context, _ workflowapplication.ExecutionContext) (workflowapplication.ExecutionResult, error) {
	executor.once.Do(func() {
		executor.probe.hit("control:claimed")
		close(executor.entered)
	})
	<-ctx.Done()
	return workflowapplication.ExecutionResult{}, ctx.Err()
}

func (executor *workspaceAnalysisCancellationExecutor) waitUntilClaimed(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-executor.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func (executor workspaceAnalysisRuntimeFailureExecutor) Execute(context.Context, workflowapplication.ExecutionContext) (workflowapplication.ExecutionResult, error) {
	executor.probe.hit("runtime:inspect")
	return workflowapplication.ExecutionResult{}, foundation.NewError(
		foundation.ErrorNonRetryableFailure, "WORKSPACE_ANALYSIS_TEST_RUNTIME_FAILED", false,
		errors.New("injected no-call runtime failure"),
	)
}

type workspaceAnalysisExpiredRunStarter struct {
	delegate agentapplication.ScopedWorkspaceAnalysisRunStarter
	probe    *workspaceAnalysisTerminalProbe
}

// The wrapper simulates a run that remained queued past its frozen deadline;
// the production run service still derives and persists every terminal fact.
func (starter workspaceAnalysisExpiredRunStarter) StartWorkspaceAnalysisRunScoped(
	ctx context.Context,
	transaction foundation.TransactionScope,
	command agentapplication.WorkspaceAnalysisRunStartCommand,
) (agentdomain.WorkspaceAnalysisRun, error) {
	starter.probe.hit("runtime:expired-run")
	command.CreatedAt = command.CreatedAt.Add(-2 * agentapplication.WorkspaceAnalysisV1MaxRunDuration)
	return starter.delegate.StartWorkspaceAnalysisRunScoped(ctx, transaction, command)
}

func doWorkspaceAnalysisTerminalCancel(t *testing.T, ctx context.Context, serverURL, workflowRunID string, version int64) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"expected_version": version})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL+"/api/v1/workflows/"+workflowRunID+"/cancel", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "workspace-analysis-terminal-cancel")
	request.Header.Set("X-Workspace-ID", string(ragSmokeWorkspaceID))
	response := executeRAGRequest(t, request, http.StatusOK)
	if response["workflow_run_id"] != workflowRunID || response["cancel_requested"] != true {
		t.Fatalf("cancel response=%v", response)
	}
}

func waitForWorkspaceAnalysisTerminal(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	serverURL, answerID string,
	wantWorkflow workflowdomain.RunStatus,
) map[string]any {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var last map[string]any
	for {
		last = doRAGGET(t, ctx, serverURL+"/api/v1/answers/"+answerID+"?workspace_id="+string(ragSmokeWorkspaceID), http.StatusOK)
		if last["publication_status"] != "pending" {
			var workflowStatus string
			if err := pool.QueryRow(ctx, `SELECT workflow.status FROM workflow.run workflow JOIN agent.workspace_analysis_run analysis ON analysis.workflow_run_id=workflow.id WHERE analysis.answer_id=$1`, answerID).Scan(&workflowStatus); err != nil {
				t.Fatal(err)
			}
			if workflowStatus == string(wantWorkflow) {
				return last
			}
		}
		select {
		case <-ctx.Done():
			var runStatus, reason, nodes string
			_ = pool.QueryRow(context.Background(), `SELECT status,COALESCE(termination_reason,'') FROM agent.workspace_analysis_run WHERE answer_id=$1`, answerID).Scan(&runStatus, &reason)
			_ = pool.QueryRow(context.Background(), `SELECT COALESCE(string_agg(node.node_key||':'||node.status||':'||COALESCE(node.error_code,''),',' ORDER BY node.node_key),'') FROM workflow.node_run node JOIN agent.workspace_analysis_run analysis ON analysis.workflow_run_id=node.run_id WHERE analysis.answer_id=$1`, answerID).Scan(&nodes)
			t.Fatalf("terminal wait: %v answer=%v run=%s reason=%s nodes=%s", ctx.Err(), last, runStatus, reason, nodes)
		case <-ticker.C:
		}
	}
}

func assertWorkspaceAnalysisTerminalPublicAnswer(t *testing.T, answer map[string]any, scenario workspaceAnalysisTerminalScenario) {
	t.Helper()
	if answer["publication_status"] != scenario.publicationStatus || answer["result_type"] != scenario.resultType {
		t.Fatalf("public answer status/type=%v/%v want=%s/%s answer=%v", answer["publication_status"], answer["result_type"], scenario.publicationStatus, scenario.resultType, answer)
	}
	result, ok := answer["result"].(map[string]any)
	if !ok || result["result_type"] != scenario.resultType {
		t.Fatalf("public result=%v", answer["result"])
	}
	modelRunRef, hasModelRunRef := result["model_run_ref"].(string)
	if scenario.modelRunRef != (hasModelRunRef && modelRunRef != "") {
		t.Fatalf("public model_run_ref=%v want_present=%t", result["model_run_ref"], scenario.modelRunRef)
	}
	payload, ok := result["payload"].(map[string]any)
	if !ok {
		t.Fatalf("public payload=%v", result["payload"])
	}
	switch scenario.resultType {
	case "workspace_analysis_refusal":
		if payload["reason_code"] != string(scenario.reason) {
			t.Fatalf("public refusal reason=%v want=%s", payload["reason_code"], scenario.reason)
		}
	case "workspace_analysis_termination":
		if payload["termination_reason"] != string(scenario.reason) {
			t.Fatalf("public termination reason=%v want=%s", payload["termination_reason"], scenario.reason)
		}
	case "clarification":
		if strings.TrimSpace(stringValue(payload["reason"])) == "" || strings.TrimSpace(stringValue(payload["question"])) == "" {
			t.Fatalf("public clarification payload=%v", payload)
		}
	case "workspace_analysis":
		if payload["answer_markdown"] != workspaceAnalysisSmokeAnswer {
			t.Fatalf("public answer payload=%v", payload)
		}
	}
}

func assertWorkspaceAnalysisTerminalPublicModelBinding(t *testing.T, answer map[string]any, modelRunID *string) {
	t.Helper()
	result, ok := answer["result"].(map[string]any)
	if !ok {
		t.Fatalf("public result=%v", answer["result"])
	}
	publicModelRunID, _ := result["model_run_ref"].(string)
	if modelRunID == nil {
		if publicModelRunID != "" {
			t.Fatalf("public model_run_ref=%s want null", publicModelRunID)
		}
		return
	}
	if publicModelRunID != *modelRunID {
		t.Fatalf("public model_run_ref=%s want=%s", publicModelRunID, *modelRunID)
	}
}

type workspaceAnalysisTerminalFacts struct {
	publicationStatus, resultType                          string
	modelRunID                                             *string
	analysisRunID, analysisStatus, reason, workflowStatus  string
	publicationProofs, terminationProofs                   int64
	answerEvents, analysisEvents                           int64
	draftTotal, draftAborted, draftPublished, activeDrafts int64
	activeOperations, startedModelCalls, startedToolCalls  int64
	unboundModelCalls, unboundToolCalls                    int64
	reservedReservations, invalidReservations              int64
	reservedModelCalls, reservedToolCalls                  int64
	reservedSourceReads, reservedInputTokens               int64
	reservedOutputTokens                                   int64
	settledModelCalls, settledToolCalls                    int64
	settledSourceReads, settledInputTokens                 int64
	settledOutputTokens                                    int64
	sumSettledModelCalls, sumSettledToolCalls              int64
	sumSettledSourceReads, sumSettledInputTokens           int64
	sumSettledOutputTokens                                 int64
	costsNull                                              bool
}

func loadWorkspaceAnalysisTerminalFacts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, answerID string) workspaceAnalysisTerminalFacts {
	t.Helper()
	var facts workspaceAnalysisTerminalFacts
	err := pool.QueryRow(ctx, `SELECT
		a.publication_status,a.result_type,a.model_run_id::text,
		r.id::text,r.status,r.termination_reason,w.status,
		(SELECT count(*) FROM agent.workspace_analysis_publication_proof p WHERE p.analysis_run_id=r.id),
		(SELECT count(*) FROM agent.workspace_analysis_termination_proof p WHERE p.analysis_run_id=r.id),
		(SELECT count(*) FROM ops.server_event e WHERE e.workspace_id=a.workspace_id AND e.resource_ref='answer:'||a.id::text AND e.event_type='answer.'||a.publication_status),
		(SELECT count(*) FROM ops.server_event e WHERE e.workspace_id=a.workspace_id AND e.resource_ref='workspace_analysis:'||r.id::text AND e.event_type='workspace_analysis.terminated'),
		(SELECT count(*) FROM agent.answer_draft_session d WHERE d.workspace_id=a.workspace_id AND d.answer_id=a.id),
		(SELECT count(*) FROM agent.answer_draft_session d WHERE d.workspace_id=a.workspace_id AND d.answer_id=a.id AND d.status='ABORTED'),
		(SELECT count(*) FROM agent.answer_draft_session d WHERE d.workspace_id=a.workspace_id AND d.answer_id=a.id AND d.status='PUBLISHED'),
		(SELECT count(*) FROM agent.answer_draft_session d WHERE d.workspace_id=a.workspace_id AND d.answer_id=a.id AND d.status IN ('ACTIVE','COMPLETED','DEGRADED')),
		(SELECT count(*) FROM agent.workspace_analysis_operation o WHERE o.analysis_run_id=r.id AND o.status IN ('PENDING','STARTED')),
		(SELECT count(*) FROM agent.model_call c JOIN agent.model_run m ON m.id=c.model_run_id WHERE m.workflow_run_id=r.workflow_run_id AND c.status='STARTED'),
		(SELECT count(*) FROM workflow.tool_call c WHERE c.workflow_run_id=r.workflow_run_id AND c.status='STARTED'),
		(SELECT count(*) FROM agent.model_call c JOIN agent.model_run m ON m.id=c.model_run_id
		 WHERE m.workflow_run_id=r.workflow_run_id AND NOT EXISTS (
			SELECT 1 FROM agent.workspace_analysis_operation o WHERE o.analysis_run_id=r.id AND o.model_call_id=c.id
		 )),
		(SELECT count(*) FROM workflow.tool_call c WHERE c.workflow_run_id=r.workflow_run_id AND NOT EXISTS (
			SELECT 1 FROM agent.workspace_analysis_operation o WHERE o.analysis_run_id=r.id AND o.tool_call_id=c.id
		 )),
		(SELECT count(*) FROM agent.workspace_analysis_budget_reservation b WHERE b.analysis_run_id=r.id AND b.status='RESERVED'),
		(SELECT count(*) FROM agent.workspace_analysis_budget_reservation b WHERE b.analysis_run_id=r.id AND b.status NOT IN ('SETTLED','UNKNOWN_CHARGED')),
		r.reserved_model_calls,r.reserved_tool_calls,r.reserved_source_reads,r.reserved_input_tokens,r.reserved_output_tokens,
		r.settled_model_calls,r.settled_tool_calls,r.settled_source_reads,r.settled_input_tokens,r.settled_output_tokens,
		COALESCE((SELECT sum(b.settled_model_calls) FROM agent.workspace_analysis_budget_reservation b WHERE b.analysis_run_id=r.id),0),
		COALESCE((SELECT sum(b.settled_tool_calls) FROM agent.workspace_analysis_budget_reservation b WHERE b.analysis_run_id=r.id),0),
		COALESCE((SELECT sum(b.settled_source_reads) FROM agent.workspace_analysis_budget_reservation b WHERE b.analysis_run_id=r.id),0),
		COALESCE((SELECT sum(b.settled_input_tokens) FROM agent.workspace_analysis_budget_reservation b WHERE b.analysis_run_id=r.id),0),
		COALESCE((SELECT sum(b.settled_output_tokens) FROM agent.workspace_analysis_budget_reservation b WHERE b.analysis_run_id=r.id),0),
		r.reserved_cost_microunits IS NULL AND r.settled_cost_microunits IS NULL AND NOT EXISTS (
			SELECT 1 FROM agent.workspace_analysis_budget_reservation b WHERE b.analysis_run_id=r.id AND (b.reserved_cost_microunits IS NOT NULL OR b.settled_cost_microunits IS NOT NULL))
	FROM agent.answer a
	JOIN agent.workspace_analysis_run r ON r.answer_id=a.id AND r.workspace_id=a.workspace_id
	JOIN workflow.run w ON w.id=r.workflow_run_id AND w.workspace_id=r.workspace_id
	WHERE a.id=$1`, answerID).Scan(
		&facts.publicationStatus, &facts.resultType, &facts.modelRunID,
		&facts.analysisRunID, &facts.analysisStatus, &facts.reason, &facts.workflowStatus,
		&facts.publicationProofs, &facts.terminationProofs, &facts.answerEvents, &facts.analysisEvents,
		&facts.draftTotal, &facts.draftAborted, &facts.draftPublished, &facts.activeDrafts,
		&facts.activeOperations, &facts.startedModelCalls, &facts.startedToolCalls,
		&facts.unboundModelCalls, &facts.unboundToolCalls,
		&facts.reservedReservations, &facts.invalidReservations,
		&facts.reservedModelCalls, &facts.reservedToolCalls, &facts.reservedSourceReads,
		&facts.reservedInputTokens, &facts.reservedOutputTokens,
		&facts.settledModelCalls, &facts.settledToolCalls, &facts.settledSourceReads,
		&facts.settledInputTokens, &facts.settledOutputTokens,
		&facts.sumSettledModelCalls, &facts.sumSettledToolCalls, &facts.sumSettledSourceReads,
		&facts.sumSettledInputTokens, &facts.sumSettledOutputTokens, &facts.costsNull,
	)
	if err != nil {
		t.Fatal(err)
	}
	return facts
}

func assertWorkspaceAnalysisTerminalFacts(t *testing.T, facts workspaceAnalysisTerminalFacts, scenario workspaceAnalysisTerminalScenario) {
	t.Helper()
	if facts.publicationStatus != scenario.publicationStatus || facts.resultType != scenario.resultType ||
		facts.analysisStatus != string(scenario.analysisStatus) || facts.reason != string(scenario.reason) ||
		facts.workflowStatus != string(scenario.workflowStatus) || (facts.modelRunID != nil) != scenario.modelRunRef {
		t.Fatalf("terminal facts answer=%s/%s model=%v analysis=%s/%s workflow=%s", facts.publicationStatus, facts.resultType, facts.modelRunID, facts.analysisStatus, facts.reason, facts.workflowStatus)
	}
	wantPublicationProof, wantTerminationProof := int64(0), int64(1)
	if scenario.reason == agentdomain.WorkspaceAnalysisRunCompleted {
		wantPublicationProof, wantTerminationProof = 1, 0
	}
	if facts.publicationProofs != wantPublicationProof || facts.terminationProofs != wantTerminationProof ||
		facts.answerEvents != 1 || facts.analysisEvents != 1 {
		t.Fatalf("proof/events publication=%d termination=%d answer=%d analysis=%d", facts.publicationProofs, facts.terminationProofs, facts.answerEvents, facts.analysisEvents)
	}
	if facts.activeDrafts != 0 || facts.activeOperations != 0 || facts.startedModelCalls != 0 ||
		facts.startedToolCalls != 0 || facts.unboundModelCalls != 0 || facts.unboundToolCalls != 0 ||
		facts.reservedReservations != 0 || facts.invalidReservations != 0 {
		t.Fatalf("active leftovers drafts=%d operations=%d model=%d tool=%d unbound_model=%d unbound_tool=%d reservations=%d invalid=%d",
			facts.activeDrafts, facts.activeOperations, facts.startedModelCalls, facts.startedToolCalls,
			facts.unboundModelCalls, facts.unboundToolCalls, facts.reservedReservations, facts.invalidReservations)
	}
	if scenario.reason == agentdomain.WorkspaceAnalysisRunCompleted {
		if facts.draftTotal != 1 || facts.draftPublished != 1 || facts.draftAborted != 0 {
			t.Fatalf("completed draft total=%d published=%d aborted=%d", facts.draftTotal, facts.draftPublished, facts.draftAborted)
		}
	} else if facts.draftTotal != facts.draftAborted {
		t.Fatalf("terminal draft total=%d aborted=%d published=%d", facts.draftTotal, facts.draftAborted, facts.draftPublished)
	}
	if facts.reservedModelCalls != 0 || facts.reservedToolCalls != 0 || facts.reservedSourceReads != 0 ||
		facts.reservedInputTokens != 0 || facts.reservedOutputTokens != 0 ||
		facts.settledModelCalls != facts.sumSettledModelCalls || facts.settledToolCalls != facts.sumSettledToolCalls ||
		facts.settledSourceReads != facts.sumSettledSourceReads || facts.settledInputTokens != facts.sumSettledInputTokens ||
		facts.settledOutputTokens != facts.sumSettledOutputTokens || !facts.costsNull {
		t.Fatalf("budget closure reserved=%d/%d/%d/%d/%d settled=%d/%d/%d/%d/%d sums=%d/%d/%d/%d/%d costs_null=%t",
			facts.reservedModelCalls, facts.reservedToolCalls, facts.reservedSourceReads, facts.reservedInputTokens, facts.reservedOutputTokens,
			facts.settledModelCalls, facts.settledToolCalls, facts.settledSourceReads, facts.settledInputTokens, facts.settledOutputTokens,
			facts.sumSettledModelCalls, facts.sumSettledToolCalls, facts.sumSettledSourceReads, facts.sumSettledInputTokens, facts.sumSettledOutputTokens, facts.costsNull)
	}
}

func assertWorkspaceAnalysisTerminalProof(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	facts workspaceAnalysisTerminalFacts,
	scenario workspaceAnalysisTerminalScenario,
) {
	t.Helper()
	if scenario.reason == agentdomain.WorkspaceAnalysisRunCompleted {
		return
	}
	var reason string
	var proofModelRunID, terminalAttemptID, operationID *string
	var runtimeTerminalAt *time.Time
	var checkedAt time.Time
	if err := pool.QueryRow(ctx, `SELECT reason,published_model_run_id::text,runtime_terminal_at,checked_at,terminal_node_attempt_id::text,operation_id::text FROM agent.workspace_analysis_termination_proof WHERE analysis_run_id=$1`, facts.analysisRunID).Scan(
		&reason, &proofModelRunID, &runtimeTerminalAt, &checkedAt, &terminalAttemptID, &operationID,
	); err != nil {
		t.Fatal(err)
	}
	if reason != string(scenario.reason) {
		t.Fatalf("proof reason=%s want=%s", reason, scenario.reason)
	}
	runtimeOwned := scenario.reason == agentdomain.WorkspaceAnalysisRunRuntimeFailed ||
		(scenario.reason == agentdomain.WorkspaceAnalysisRunCancellation && !scenario.cancelAfterSynthesisProvider)
	if runtimeOwned != (runtimeTerminalAt != nil) || (runtimeTerminalAt != nil && !runtimeTerminalAt.Equal(checkedAt)) {
		t.Fatalf("proof runtime_terminal_at=%v checked_at=%s runtime_owned=%t", runtimeTerminalAt, checkedAt, runtimeOwned)
	}
	if scenario.reason == agentdomain.WorkspaceAnalysisRunRuntimeFailed {
		if terminalAttemptID == nil || operationID != nil {
			t.Fatalf("runtime proof attempt=%v operation=%v", terminalAttemptID, operationID)
		}
	} else if scenario.reason == agentdomain.WorkspaceAnalysisRunCancellation {
		if terminalAttemptID == nil || operationID != nil {
			t.Fatalf("cancel proof attempt=%v operation=%v", terminalAttemptID, operationID)
		}
	} else if terminalAttemptID == nil {
		t.Fatal("operation finalizer proof is missing its terminal attempt")
	}
	if scenario.reason == agentdomain.WorkspaceAnalysisRunModelRefused {
		if proofModelRunID == nil || facts.modelRunID == nil || *proofModelRunID != *facts.modelRunID {
			t.Fatalf("model refusal proof model=%v answer_model=%v", proofModelRunID, facts.modelRunID)
		}
	} else if proofModelRunID != facts.modelRunID && (proofModelRunID == nil || facts.modelRunID == nil || *proofModelRunID != *facts.modelRunID) {
		t.Fatalf("proof model=%v answer_model=%v", proofModelRunID, facts.modelRunID)
	}
}

type workspaceAnalysisTerminalCauseExpectation struct {
	operationKind     agentdomain.WorkspaceAnalysisOperationKind
	operationStatus   agentdomain.WorkspaceAnalysisOperationStatus
	callKind          agentdomain.WorkspaceAnalysisOperationCallKind
	resultKind        agentdomain.WorkspaceAnalysisOperationResultKind
	modelCallStatus   agentdomain.ModelCallStatus
	modelRunStatus    agentdomain.ModelRunStatus
	modelFinalResult  string
	toolCallStatus    toolsdomain.CallStatus
	artifactKind      string
	exactErrorCode    string
	errorCodeRequired bool
	receiptFailure    string
}

func assertWorkspaceAnalysisTerminalCausality(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	facts workspaceAnalysisTerminalFacts,
	scenario workspaceAnalysisTerminalScenario,
) {
	t.Helper()
	var receiptFailures int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.tool_result_receipt_failure WHERE analysis_run_id=$1`, facts.analysisRunID).Scan(&receiptFailures); err != nil {
		t.Fatal(err)
	}
	wantReceiptFailures := int64(0)
	if scenario.reason == agentdomain.WorkspaceAnalysisRunReceiptInvalid {
		wantReceiptFailures = 1
	}
	if receiptFailures != wantReceiptFailures {
		t.Fatalf("receipt failure facts=%d want=%d", receiptFailures, wantReceiptFailures)
	}
	if scenario.reason == agentdomain.WorkspaceAnalysisRunCompleted {
		assertWorkspaceAnalysisCompletionCausality(t, ctx, pool, facts)
		return
	}
	if scenario.cancelAfterSynthesisProvider {
		return
	}

	expectation, operationOwned := workspaceAnalysisTerminalCauseForReason(scenario.reason)
	if !operationOwned {
		switch scenario.reason {
		case agentdomain.WorkspaceAnalysisRunDeadlineExceeded,
			agentdomain.WorkspaceAnalysisRunRuntimeFailed,
			agentdomain.WorkspaceAnalysisRunCancellation:
		default:
			t.Fatalf("terminal cause expectation is missing for %s", scenario.reason)
		}
		var operations int64
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent.workspace_analysis_operation WHERE analysis_run_id=$1`, facts.analysisRunID).Scan(&operations); err != nil {
			t.Fatal(err)
		}
		if operations != 0 {
			t.Fatalf("pre-operation terminal %s created %d operations", scenario.reason, operations)
		}
		return
	}

	var operationKind, operationStatus, callKind string
	var errorCode, resultKind, resultID, resultHash *string
	var modelCallStatus, modelRunStatus, modelFinalResult, modelRunID *string
	var toolCallStatus, artifactKind, artifactID, artifactHash *string
	var receiptFailureCode, receiptFailureID, proofModelRunID *string
	err := pool.QueryRow(ctx, `SELECT
		o.operation_kind,o.status,o.call_kind,o.error_code,o.result_kind,o.result_id::text,o.result_hash,
		mc.status,mr.status,mr.final_result_type,mr.id::text,tc.status,
		p.artifact_kind,p.artifact_id::text,p.artifact_hash,p.receipt_failure_code,p.receipt_failure_id::text,p.published_model_run_id::text
	FROM agent.workspace_analysis_termination_proof p
	JOIN agent.workspace_analysis_operation o ON o.id=p.operation_id AND o.analysis_run_id=p.analysis_run_id
	LEFT JOIN agent.model_call mc ON mc.id=o.model_call_id
	LEFT JOIN agent.model_run mr ON mr.id=mc.model_run_id
	LEFT JOIN workflow.tool_call tc ON tc.id=o.tool_call_id
	WHERE p.analysis_run_id=$1`, facts.analysisRunID).Scan(
		&operationKind, &operationStatus, &callKind, &errorCode, &resultKind, &resultID, &resultHash,
		&modelCallStatus, &modelRunStatus, &modelFinalResult, &modelRunID, &toolCallStatus,
		&artifactKind, &artifactID, &artifactHash, &receiptFailureCode, &receiptFailureID, &proofModelRunID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if operationKind != string(expectation.operationKind) || operationStatus != string(expectation.operationStatus) || callKind != string(expectation.callKind) ||
		!workspaceAnalysisOptionalStringEqual(resultKind, string(expectation.resultKind)) ||
		!workspaceAnalysisOptionalStringEqual(modelCallStatus, string(expectation.modelCallStatus)) ||
		!workspaceAnalysisOptionalStringEqual(modelRunStatus, string(expectation.modelRunStatus)) ||
		!workspaceAnalysisOptionalStringEqual(modelFinalResult, expectation.modelFinalResult) ||
		!workspaceAnalysisOptionalStringEqual(toolCallStatus, string(expectation.toolCallStatus)) {
		t.Fatalf("terminal cause operation=%s/%s/%s result=%v model=%v/%v/%v tool=%v", operationKind, operationStatus, callKind, resultKind, modelCallStatus, modelRunStatus, modelFinalResult, toolCallStatus)
	}
	if expectation.exactErrorCode != "" {
		if errorCode == nil || *errorCode != expectation.exactErrorCode {
			t.Fatalf("terminal cause error=%v want=%s", errorCode, expectation.exactErrorCode)
		}
	} else if expectation.errorCodeRequired {
		if errorCode == nil || strings.TrimSpace(*errorCode) == "" {
			t.Fatal("terminal cause is missing its operation error code")
		}
	} else if errorCode != nil {
		t.Fatalf("successful terminal cause has error=%v", errorCode)
	}
	if expectation.artifactKind == "" {
		if artifactKind != nil || artifactID != nil || artifactHash != nil || resultID != nil || resultHash != nil {
			t.Fatalf("terminal cause unexpectedly owns artifact=%v/%v/%v result=%v/%v", artifactKind, artifactID, artifactHash, resultID, resultHash)
		}
	} else if artifactKind == nil || *artifactKind != expectation.artifactKind || artifactID == nil || resultID == nil || *artifactID != *resultID || artifactHash == nil || resultHash == nil || *artifactHash != *resultHash {
		t.Fatalf("terminal artifact=%v/%v/%v operation_result=%v/%v", artifactKind, artifactID, artifactHash, resultID, resultHash)
	}
	if expectation.receiptFailure == "" {
		if receiptFailureCode != nil || receiptFailureID != nil {
			t.Fatalf("terminal cause unexpectedly owns receipt failure=%v/%v", receiptFailureCode, receiptFailureID)
		}
	} else {
		if receiptFailureCode == nil || *receiptFailureCode != expectation.receiptFailure || receiptFailureID == nil {
			t.Fatalf("terminal receipt failure=%v/%v want=%s", receiptFailureCode, receiptFailureID, expectation.receiptFailure)
		}
		var matchingFailure int64
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.tool_result_receipt_failure f
			JOIN agent.workspace_analysis_termination_proof p ON p.receipt_failure_id=f.id AND p.analysis_run_id=f.analysis_run_id
			WHERE p.analysis_run_id=$1 AND f.operation_id=p.operation_id AND f.failure_code=$2`, facts.analysisRunID, expectation.receiptFailure).Scan(&matchingFailure); err != nil {
			t.Fatal(err)
		}
		if matchingFailure != 1 {
			t.Fatalf("receipt failure causal binding=%d", matchingFailure)
		}
	}
	if scenario.modelRunRef {
		if modelRunID == nil || proofModelRunID == nil || facts.modelRunID == nil || *modelRunID != *proofModelRunID || *proofModelRunID != *facts.modelRunID {
			t.Fatalf("terminal model binding operation=%v proof=%v answer=%v", modelRunID, proofModelRunID, facts.modelRunID)
		}
	} else if proofModelRunID != nil {
		t.Fatalf("terminal cause published unexpected model=%v", proofModelRunID)
	}
}

// workspaceAnalysisCancelBeforeCandidateFinalizer makes the otherwise tiny
// Provider-return-to-FinalizeCandidate window deterministic. It invokes the
// production HTTP Runtime cancel endpoint, then delegates to the real
// PostgreSQL finalizer so the Runner must observe the persisted cancellation
// fence and settle its existing Call.
type workspaceAnalysisCancelBeforeCandidateFinalizer struct {
	*agentpostgres.GORMWorkspaceAnalysisRepository

	pool  *pgxpool.Pool
	probe *workspaceAnalysisTerminalProbe

	mu         sync.Mutex
	serverURL  string
	triggered  bool
	triggerErr error
}

func (repository *workspaceAnalysisCancelBeforeCandidateFinalizer) setServerURL(serverURL string) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.serverURL = serverURL
}

func (repository *workspaceAnalysisCancelBeforeCandidateFinalizer) FinalizeWorkspaceAnalysisModelCandidate(
	ctx context.Context,
	command agentapplication.FinalizeWorkspaceAnalysisModelCandidateCommand,
) (agentapplication.WorkspaceAnalysisModelMutationResult, error) {
	if err := repository.persistCancelBeforeCandidate(ctx, command.Identity.WorkflowRunID, command.Identity.WorkspaceID); err != nil {
		return agentapplication.WorkspaceAnalysisModelMutationResult{}, err
	}
	return repository.GORMWorkspaceAnalysisRepository.FinalizeWorkspaceAnalysisModelCandidate(ctx, command)
}

func (repository *workspaceAnalysisCancelBeforeCandidateFinalizer) persistCancelBeforeCandidate(
	ctx context.Context,
	workflowRunID, workspaceID foundation.ID,
) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.triggered {
		return repository.triggerErr
	}
	repository.triggered = true
	if repository.GORMWorkspaceAnalysisRepository == nil || repository.pool == nil || repository.probe == nil || repository.serverURL == "" {
		repository.triggerErr = errors.New("candidate finalization cancellation fixture is incomplete")
		return repository.triggerErr
	}

	var version int64
	if err := repository.pool.QueryRow(ctx, `SELECT version FROM workflow.run WHERE id=$1 AND workspace_id=$2`, workflowRunID, workspaceID).Scan(&version); err != nil {
		repository.triggerErr = err
		return err
	}
	body, err := json.Marshal(map[string]any{"expected_version": version})
	if err != nil {
		repository.triggerErr = err
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, repository.serverURL+"/api/v1/workflows/"+string(workflowRunID)+"/cancel", bytes.NewReader(body))
	if err != nil {
		repository.triggerErr = err
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "workspace-analysis-cancel-before-candidate")
	request.Header.Set("X-Workspace-ID", string(workspaceID))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		repository.triggerErr = err
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		repository.triggerErr = errors.New("candidate finalization cancellation endpoint rejected the request")
		return repository.triggerErr
	}
	var result struct {
		WorkflowRunID   string `json:"workflow_run_id"`
		CancelRequested bool   `json:"cancel_requested"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		repository.triggerErr = err
		return err
	}
	if result.WorkflowRunID != string(workflowRunID) || !result.CancelRequested {
		repository.triggerErr = errors.New("candidate finalization cancellation response binding drifted")
		return repository.triggerErr
	}
	repository.probe.hit("finalization:cancel-before-candidate")
	return nil
}

func (repository *workspaceAnalysisCancelBeforeCandidateFinalizer) assertTriggered(t *testing.T) {
	t.Helper()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if !repository.triggered || repository.triggerErr != nil {
		t.Fatalf("candidate finalization cancellation triggered=%t err=%v", repository.triggered, repository.triggerErr)
	}
}

func assertWorkspaceAnalysisCancelledSynthesisClosure(t *testing.T, ctx context.Context, pool *pgxpool.Pool, analysisRunID string) {
	t.Helper()
	var operationStatus, reservationStatus, callStatus, runStatus, errorCode string
	var candidateCount, modelResultCount int64
	err := pool.QueryRow(ctx, `SELECT operation.status,reservation.status,call.status,run.status,COALESCE(operation.error_code,''),
		(SELECT count(*) FROM agent.workspace_analysis_candidate WHERE analysis_run_id=operation.analysis_run_id AND synthesis_operation_id=operation.id),
		(SELECT count(*) FROM agent.workspace_analysis_model_result WHERE operation_id=operation.id)
		FROM agent.workspace_analysis_operation AS operation
		JOIN agent.workspace_analysis_budget_reservation AS reservation ON reservation.operation_id=operation.id
		JOIN agent.model_call AS call ON call.id=operation.model_call_id
		JOIN agent.model_run AS run ON run.id=call.model_run_id
		WHERE operation.analysis_run_id=$1 AND operation.operation_kind='ANSWER_SYNTHESIS'`, analysisRunID).Scan(
		&operationStatus, &reservationStatus, &callStatus, &runStatus, &errorCode, &candidateCount, &modelResultCount,
	)
	if err != nil {
		t.Fatal(err)
	}
	if operationStatus != "FAILED" || reservationStatus != "SETTLED" || callStatus != "FAILED" || runStatus != "FAILED" ||
		errorCode != agentapplication.ErrorCodeOperationCancelled || candidateCount != 0 || modelResultCount != 0 {
		t.Fatalf("cancelled synthesis closure operation=%s reservation=%s call=%s run=%s error=%s candidate=%d result=%d",
			operationStatus, reservationStatus, callStatus, runStatus, errorCode, candidateCount, modelResultCount)
	}
}

func workspaceAnalysisTerminalCauseForReason(reason agentdomain.WorkspaceAnalysisRunTerminationReason) (workspaceAnalysisTerminalCauseExpectation, bool) {
	modelSuccess := func(kind agentdomain.WorkspaceAnalysisOperationKind, finalResult, artifactKind string) workspaceAnalysisTerminalCauseExpectation {
		return workspaceAnalysisTerminalCauseExpectation{
			operationKind: kind, operationStatus: agentdomain.WorkspaceAnalysisOperationSucceeded,
			callKind: agentdomain.WorkspaceAnalysisOperationCallModel, resultKind: agentdomain.WorkspaceAnalysisOperationResultModelCall,
			modelCallStatus: agentdomain.ModelCallSucceeded, modelRunStatus: agentdomain.ModelRunSucceeded,
			modelFinalResult: finalResult, artifactKind: artifactKind,
		}
	}
	toolSuccess := func(kind agentdomain.WorkspaceAnalysisOperationKind, artifactKind string) workspaceAnalysisTerminalCauseExpectation {
		return workspaceAnalysisTerminalCauseExpectation{
			operationKind: kind, operationStatus: agentdomain.WorkspaceAnalysisOperationSucceeded,
			callKind: agentdomain.WorkspaceAnalysisOperationCallTool, resultKind: agentdomain.WorkspaceAnalysisOperationResultToolReceipt,
			toolCallStatus: toolsdomain.CallSucceeded, artifactKind: artifactKind,
		}
	}
	switch reason {
	case agentdomain.WorkspaceAnalysisRunEvidenceInsufficient:
		return toolSuccess(agentdomain.WorkspaceAnalysisOperationKnowledgeSearch, "TOOL_RECEIPT"), true
	case agentdomain.WorkspaceAnalysisRunCitationInvalid:
		return toolSuccess(agentdomain.WorkspaceAnalysisOperationCitationValidation, "TOOL_RECEIPT"), true
	case agentdomain.WorkspaceAnalysisRunFaithfulnessRejected:
		return modelSuccess(agentdomain.WorkspaceAnalysisOperationFaithfulnessReview, agentdomain.ResultTypeFaithfulnessReview, "MODEL_RESULT"), true
	case agentdomain.WorkspaceAnalysisRunNeedsClarification:
		return modelSuccess(agentdomain.WorkspaceAnalysisOperationRetrievalPlan, agentdomain.ResultTypeWorkspaceAnalysisPlan, "MODEL_RESULT"), true
	case agentdomain.WorkspaceAnalysisRunModelRefused:
		return workspaceAnalysisTerminalCauseExpectation{
			operationKind: agentdomain.WorkspaceAnalysisOperationRetrievalPlan, operationStatus: agentdomain.WorkspaceAnalysisOperationFailed,
			callKind: agentdomain.WorkspaceAnalysisOperationCallModel, modelCallStatus: agentdomain.ModelCallSucceeded,
			modelRunStatus: agentdomain.ModelRunRefused, modelFinalResult: agentdomain.ResultTypeRefusal,
			exactErrorCode: string(agentdomain.WorkspaceAnalysisRunModelRefused),
		}, true
	case agentdomain.WorkspaceAnalysisRunReceiptInvalid:
		return workspaceAnalysisTerminalCauseExpectation{
			operationKind: agentdomain.WorkspaceAnalysisOperationGitStatus, operationStatus: agentdomain.WorkspaceAnalysisOperationFailed,
			callKind: agentdomain.WorkspaceAnalysisOperationCallTool, toolCallStatus: toolsdomain.CallSucceeded,
			exactErrorCode: string(agentdomain.WorkspaceAnalysisRunReceiptInvalid), receiptFailure: "CONTRACT_INVALID",
		}, true
	case agentdomain.WorkspaceAnalysisRunResultUnknown:
		return workspaceAnalysisTerminalCauseExpectation{
			operationKind: agentdomain.WorkspaceAnalysisOperationGitStatus, operationStatus: agentdomain.WorkspaceAnalysisOperationUnknown,
			callKind: agentdomain.WorkspaceAnalysisOperationCallTool, toolCallStatus: toolsdomain.CallUnknown, errorCodeRequired: true,
		}, true
	case agentdomain.WorkspaceAnalysisRunModelFailed:
		return workspaceAnalysisTerminalCauseExpectation{
			operationKind: agentdomain.WorkspaceAnalysisOperationRetrievalPlan, operationStatus: agentdomain.WorkspaceAnalysisOperationFailed,
			callKind: agentdomain.WorkspaceAnalysisOperationCallModel, modelCallStatus: agentdomain.ModelCallFailed,
			modelRunStatus: agentdomain.ModelRunFailed, errorCodeRequired: true,
		}, true
	case agentdomain.WorkspaceAnalysisRunToolFailed:
		return workspaceAnalysisTerminalCauseExpectation{
			operationKind: agentdomain.WorkspaceAnalysisOperationGitStatus, operationStatus: agentdomain.WorkspaceAnalysisOperationFailed,
			callKind: agentdomain.WorkspaceAnalysisOperationCallTool, toolCallStatus: toolsdomain.CallFailed, errorCodeRequired: true,
		}, true
	case agentdomain.WorkspaceAnalysisRunDeadlineExceeded, agentdomain.WorkspaceAnalysisRunRuntimeFailed, agentdomain.WorkspaceAnalysisRunCancellation:
		return workspaceAnalysisTerminalCauseExpectation{}, false
	default:
		return workspaceAnalysisTerminalCauseExpectation{}, false
	}
}

func assertWorkspaceAnalysisCompletionCausality(t *testing.T, ctx context.Context, pool *pgxpool.Pool, facts workspaceAnalysisTerminalFacts) {
	t.Helper()
	var synthesisModelRunID string
	var synthesis, review, git, validation int64
	err := pool.QueryRow(ctx, `SELECT c.synthesis_model_run_id::text,
		(SELECT count(*) FROM agent.workspace_analysis_operation o
		 JOIN agent.model_call mc ON mc.id=o.model_call_id JOIN agent.model_run mr ON mr.id=mc.model_run_id
		 WHERE o.id=c.synthesis_operation_id AND o.analysis_run_id=p.analysis_run_id AND o.operation_kind='ANSWER_SYNTHESIS'
		   AND o.status='SUCCEEDED' AND o.result_kind='SYNTHESIS_CANDIDATE' AND o.result_id=p.candidate_id
		   AND mc.status='SUCCEEDED' AND mr.status='SUCCEEDED' AND mr.id=c.synthesis_model_run_id),
		(SELECT count(*) FROM agent.workspace_analysis_model_result r
		 JOIN agent.workspace_analysis_operation o ON o.id=r.operation_id AND o.analysis_run_id=r.analysis_run_id
		 JOIN agent.model_call mc ON mc.id=o.model_call_id JOIN agent.model_run mr ON mr.id=mc.model_run_id
		 WHERE r.id=p.review_model_result_id AND r.analysis_run_id=p.analysis_run_id AND o.operation_kind='FAITHFULNESS_REVIEW'
		   AND o.status='SUCCEEDED' AND o.result_kind='MODEL_RESULT_RECEIPT' AND o.result_id=r.id
		   AND mc.status='SUCCEEDED' AND mr.status='SUCCEEDED' AND mr.final_result_type='faithfulness_review'),
		(SELECT count(*) FROM agent.workspace_analysis_operation o JOIN workflow.tool_call tc ON tc.id=o.tool_call_id
		 WHERE o.analysis_run_id=p.analysis_run_id AND o.operation_kind='GIT_STATUS' AND o.status='SUCCEEDED'
		   AND o.result_kind='TOOL_RESULT_RECEIPT' AND o.result_id=p.git_receipt_id AND tc.status='SUCCEEDED'),
		(SELECT count(*) FROM agent.workspace_analysis_operation o JOIN workflow.tool_call tc ON tc.id=o.tool_call_id
		 WHERE o.analysis_run_id=p.analysis_run_id AND o.operation_kind='CITATION_VALIDATION' AND o.status='SUCCEEDED'
		   AND o.result_kind='TOOL_RESULT_RECEIPT' AND o.result_id=p.validation_receipt_id AND tc.status='SUCCEEDED')
	FROM agent.workspace_analysis_publication_proof p
	JOIN agent.workspace_analysis_candidate c ON c.id=p.candidate_id AND c.analysis_run_id=p.analysis_run_id
	WHERE p.analysis_run_id=$1`, facts.analysisRunID).Scan(&synthesisModelRunID, &synthesis, &review, &git, &validation)
	if err != nil {
		t.Fatal(err)
	}
	if facts.modelRunID == nil || synthesisModelRunID != *facts.modelRunID || synthesis != 1 || review != 1 || git != 1 || validation != 1 {
		t.Fatalf("completion causality model=%s answer_model=%v synthesis=%d review=%d git=%d validation=%d", synthesisModelRunID, facts.modelRunID, synthesis, review, git, validation)
	}
}

func workspaceAnalysisOptionalStringEqual(got *string, want string) bool {
	if want == "" {
		return got == nil
	}
	return got != nil && *got == want
}

func assertWorkspaceAnalysisTerminalCallsStable(t *testing.T, ctx context.Context, pool *pgxpool.Pool, analysisRunID string) {
	t.Helper()
	load := func() (int64, int64) {
		var modelCalls, toolCalls int64
		if err := pool.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM agent.model_call c JOIN agent.model_run m ON m.id=c.model_run_id WHERE m.workflow_run_id=r.workflow_run_id),
			(SELECT count(*) FROM workflow.tool_call c WHERE c.workflow_run_id=r.workflow_run_id)
			FROM agent.workspace_analysis_run r WHERE r.id=$1`, analysisRunID).Scan(&modelCalls, &toolCalls); err != nil {
			t.Fatal(err)
		}
		return modelCalls, toolCalls
	}
	beforeModel, beforeTool := load()
	timer := time.NewTimer(50 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-timer.C:
	}
	afterModel, afterTool := load()
	if beforeModel != afterModel || beforeTool != afterTool {
		t.Fatalf("terminal spawned new calls model=%d->%d tool=%d->%d", beforeModel, afterModel, beforeTool, afterTool)
	}
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}
