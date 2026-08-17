package application

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestWorkspaceAnalysisModelTerminalEvidencePreservesClassifiedErrorAndRedactsOperationID(t *testing.T) {
	operationID := workspaceAnalysisModelTestID(91)
	cause := errors.New("provider failure")
	tests := []struct {
		name       string
		kind       foundation.ErrorKind
		code       string
		callStatus domain.ModelCallStatus
		runStatus  domain.ModelRunStatus
	}{
		{name: "failed", kind: foundation.ErrorRetryableFailure, code: "MODEL_PROVIDER_UNAVAILABLE", callStatus: domain.ModelCallFailed, runStatus: domain.ModelRunFailed},
		{name: "unknown", kind: foundation.ErrorManualRecoveryRequired, code: "MODEL_PROVIDER_RESULT_UNKNOWN", callStatus: domain.ModelCallUnknown, runStatus: domain.ModelRunUnknown},
		{name: "refused", kind: foundation.ErrorNonRetryableFailure, code: "WORKSPACE_ANALYSIS_MODEL_REFUSED", callStatus: domain.ModelCallSucceeded, runStatus: domain.ModelRunRefused},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			original := foundation.NewError(test.kind, test.code, test.kind == foundation.ErrorRetryableFailure, cause)
			err := workspaceAnalysisModelTerminalError(
				original, operationID, domain.ModelCall{Status: test.callStatus}, domain.ModelRun{Status: test.runStatus},
			)
			assertWorkspaceAnalysisModelTerminalEvidence(t, err, operationID, test.callStatus, test.runStatus, test.kind, test.code)
			if !errors.Is(err, cause) {
				t.Fatalf("errors.Is lost original cause: %v", err)
			}
		})
	}
}

func TestWorkspaceAnalysisModelTerminalEvidenceRejectsUnconfirmedState(t *testing.T) {
	var err error = foundation.NewError(foundation.ErrorManualRecoveryRequired, ErrorCodeModelCallPersistenceUnknown, false, errors.New("unknown"))
	err = workspaceAnalysisModelTerminalError(
		err, workspaceAnalysisModelTestID(91), domain.ModelCall{Status: domain.ModelCallStarted}, domain.ModelRun{Status: domain.ModelRunRunning},
	)
	if _, found := WorkspaceAnalysisModelTerminalEvidenceFromError(err); found {
		t.Fatal("unconfirmed terminal state unexpectedly exposed evidence")
	}
}

func assertWorkspaceAnalysisModelTerminalEvidence(
	t *testing.T,
	err error,
	wantOperationID foundation.ID,
	wantCall domain.ModelCallStatus,
	wantRun domain.ModelRunStatus,
	wantKind foundation.ErrorKind,
	wantCode string,
) {
	t.Helper()
	evidence, found := WorkspaceAnalysisModelTerminalEvidenceFromError(err)
	if !found || evidence.OperationID != wantOperationID || evidence.CallStatus != wantCall || evidence.RunStatus != wantRun {
		t.Fatalf("terminal evidence = %#v found=%t", evidence, found)
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != wantKind || classified.Code != wantCode {
		t.Fatalf("classified error = %#v", err)
	}
	encoded, marshalErr := json.Marshal(evidence)
	if marshalErr != nil || bytes.Contains(encoded, []byte(wantOperationID)) {
		t.Fatalf("terminal evidence JSON = %s, %v", encoded, marshalErr)
	}
	formatted := fmt.Sprintf("%v %#v", evidence, evidence)
	if strings.Contains(formatted, string(wantOperationID)) {
		t.Fatalf("terminal evidence format leaked operation id: %s", formatted)
	}
	var logged bytes.Buffer
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("terminal", "evidence", evidence)
	if strings.Contains(logged.String(), string(wantOperationID)) {
		t.Fatalf("terminal evidence log leaked operation id: %s", logged.String())
	}
}

func TestWorkspaceAnalysisModelAuthorizationCommandBindsCanonicalProviderRequest(t *testing.T) {
	valid := workspaceAnalysisModelAuthorizationCommand()
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*AuthorizeWorkspaceAnalysisModelCallCommand)
	}{
		{name: "request bytes", mutate: func(value *AuthorizeWorkspaceAnalysisModelCallCommand) {
			value.RequestDocument = []byte(`{"other":true}`)
		}},
		{name: "request hash", mutate: func(value *AuthorizeWorkspaceAnalysisModelCallCommand) { value.Call.RequestHash = hashHex('c') }},
		{name: "tool slot", mutate: func(value *AuthorizeWorkspaceAnalysisModelCallCommand) {
			value.OperationKey.Kind = domain.WorkspaceAnalysisOperationKnowledgeSearch
		}},
		{name: "node", mutate: func(value *AuthorizeWorkspaceAnalysisModelCallCommand) {
			value.Identity.NodeKey = domain.WorkspaceAnalysisOperationNodeReadEvidence
		}},
		{name: "phase", mutate: func(value *AuthorizeWorkspaceAnalysisModelCallCommand) { value.Call.Phase = domain.ModelCallAnswer }},
		{name: "schema", mutate: func(value *AuthorizeWorkspaceAnalysisModelCallCommand) {
			value.Run.Schema.ID = domain.RAGQueryPlanSchemaID
			value.Run.ReducedSchema = value.Run.Schema
			value.Call.Schema = value.Run.Schema
		}},
		{name: "candidate id reused", mutate: func(value *AuthorizeWorkspaceAnalysisModelCallCommand) { value.ReservationID = value.OperationID }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := valid
			command.RequestDocument = append([]byte(nil), valid.RequestDocument...)
			test.mutate(&command)
			var classified *foundation.Error
			if err := command.Validate(); !errors.As(err, &classified) || classified.Code != ErrorCodeWorkspaceAnalysisModelCommandInvalid && classified.Code != domain.ErrorCodeWorkspaceAnalysisOperationInvalid {
				t.Fatalf("Validate error = %#v", err)
			}
		})
	}
}

func TestWorkspaceAnalysisModelAuthorizationResultAllowsReplacementCandidateIDs(t *testing.T) {
	first := workspaceAnalysisModelAuthorizationCommand()
	revision := int64(7)
	first.Run.ModelSettingsRevision = &revision
	embeddingID := workspaceAnalysisModelTestID(12)
	first.Run.Retrieval.EmbeddingVersionID = &embeddingID
	created := WorkspaceAnalysisModelAuthorizationResult{
		Run: first.Run, Call: first.Call, OperationID: first.OperationID, ReservationID: first.ReservationID,
		Disposition: WorkspaceAnalysisModelAuthorizationCreated,
	}
	if err := created.ValidateFor(first); err != nil {
		t.Fatalf("created ValidateFor: %v", err)
	}

	completedRun, completedCall, _ := workspaceAnalysisModelSuccess(first)
	replacement := first
	replacement.Identity.NodeAttemptID = workspaceAnalysisModelTestID(20)
	replacement.Identity.LeaseFence = 2
	replacement.OperationID = workspaceAnalysisModelTestID(21)
	replacement.ReservationID = workspaceAnalysisModelTestID(22)
	replacement.Run.ID = workspaceAnalysisModelTestID(23)
	replacement.Run.NodeAttemptID = replacement.Identity.NodeAttemptID
	replacement.Call.ID = workspaceAnalysisModelTestID(24)
	replacement.Call.ModelRunID = replacement.Run.ID
	replacementRevision := int64(7)
	replacement.Run.ModelSettingsRevision = &replacementRevision
	replacementEmbeddingID := embeddingID
	replacement.Run.Retrieval.EmbeddingVersionID = &replacementEmbeddingID
	if err := replacement.Validate(); err != nil {
		t.Fatalf("replacement Validate: %v", err)
	}
	reused := WorkspaceAnalysisModelAuthorizationResult{
		Run: completedRun, Call: completedCall, OperationID: first.OperationID, ReservationID: first.ReservationID,
		Disposition: WorkspaceAnalysisModelAuthorizationReuseResult,
	}
	if err := reused.ValidateFor(replacement); err != nil {
		t.Fatalf("replacement ValidateFor: %v", err)
	}
	replacement.Call.RequestHash = hashHex('d')
	if err := reused.ValidateFor(replacement); err == nil {
		t.Fatal("request drift unexpectedly accepted")
	}
}

func TestWorkspaceAnalysisModelAuthorizationResultReplaysFrozenModelRefusal(t *testing.T) {
	command := workspaceAnalysisModelAuthorizationCommand()
	completedAt := command.Call.StartedAt.Add(time.Second)
	response := []byte(`{"result_type":"refusal"}`)
	call := command.Call
	call.Status, call.ResponseHash, call.ResponseBytes = domain.ModelCallSucceeded, workspaceAnalysisSHA256(response), int64(len(response))
	call.Usage = domain.TokenUsage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}
	call.LatencyMillis, call.Version, call.CompletedAt = 1000, 2, &completedAt
	run := command.Run
	run.Status, run.FinalResultType, run.FinalErrorCode, run.Version =
		domain.ModelRunRefused, domain.ResultTypeRefusal, string(domain.WorkspaceAnalysisRunModelRefused), 2
	run.UpdatedAt, run.CompletedAt = completedAt, &completedAt
	result := WorkspaceAnalysisModelAuthorizationResult{
		Run: run, Call: call, OperationID: command.OperationID, ReservationID: command.ReservationID,
		Disposition: WorkspaceAnalysisModelAuthorizationReplayFailure,
	}
	if err := result.ValidateFor(command); err != nil {
		t.Fatalf("ValidateFor: %v", err)
	}
	result.Run.FinalErrorCode = "WORKSPACE_ANALYSIS_MODEL_FAILED"
	if err := result.ValidateFor(command); err == nil {
		t.Fatal("refusal replay with a different reason unexpectedly accepted")
	}
}

func TestWorkspaceAnalysisRetrievalPlanCheckpointRequiresFrozenLogicalFacts(t *testing.T) {
	query := WorkspaceAnalysisRetrievalPlanCheckpointQuery{
		WorkspaceID: workspaceAnalysisModelTestID(1), WorkflowRunID: workspaceAnalysisModelTestID(2),
		AnalysisRunID: workspaceAnalysisModelTestID(3), NodeRunID: workspaceAnalysisModelTestID(4),
	}
	embeddingID := workspaceAnalysisModelTestID(8)
	checkpoint := WorkspaceAnalysisRetrievalPlanCheckpoint{
		OperationID: workspaceAnalysisModelTestID(5), ModelRunID: workspaceAnalysisModelTestID(6),
		ModelCallID: workspaceAnalysisModelTestID(7),
		Retrieval: domain.RetrievalRef{
			IndexVersionID: workspaceAnalysisModelTestID(9), EmbeddingVersionID: &embeddingID,
		},
		RequestHash: hashHex('a'), Status: domain.WorkspaceAnalysisOperationStarted,
	}
	if err := checkpoint.ValidateFor(query); err != nil {
		t.Fatalf("started checkpoint ValidateFor: %v", err)
	}

	succeeded := checkpoint
	succeeded.Status = domain.WorkspaceAnalysisOperationSucceeded
	succeeded.ModelResultID = workspaceAnalysisModelTestID(10)
	succeeded.ResultHash = hashHex('b')
	if err := succeeded.ValidateFor(query); err != nil {
		t.Fatalf("succeeded checkpoint ValidateFor: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*WorkspaceAnalysisRetrievalPlanCheckpointQuery, *WorkspaceAnalysisRetrievalPlanCheckpoint)
	}{
		{name: "query identity reused", mutate: func(query *WorkspaceAnalysisRetrievalPlanCheckpointQuery, _ *WorkspaceAnalysisRetrievalPlanCheckpoint) {
			query.NodeRunID = query.AnalysisRunID
		}},
		{name: "runtime identity reused", mutate: func(_ *WorkspaceAnalysisRetrievalPlanCheckpointQuery, value *WorkspaceAnalysisRetrievalPlanCheckpoint) {
			value.ModelCallID = value.ModelRunID
		}},
		{name: "retrieval missing", mutate: func(_ *WorkspaceAnalysisRetrievalPlanCheckpointQuery, value *WorkspaceAnalysisRetrievalPlanCheckpoint) {
			value.Retrieval = domain.RetrievalRef{}
		}},
		{name: "pending is invisible", mutate: func(_ *WorkspaceAnalysisRetrievalPlanCheckpointQuery, value *WorkspaceAnalysisRetrievalPlanCheckpoint) {
			value.Status = domain.WorkspaceAnalysisOperationPending
		}},
		{name: "success result missing", mutate: func(_ *WorkspaceAnalysisRetrievalPlanCheckpointQuery, value *WorkspaceAnalysisRetrievalPlanCheckpoint) {
			value.ModelResultID = ""
		}},
		{name: "non-success result present", mutate: func(_ *WorkspaceAnalysisRetrievalPlanCheckpointQuery, value *WorkspaceAnalysisRetrievalPlanCheckpoint) {
			value.Status = domain.WorkspaceAnalysisOperationFailed
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidateQuery, candidate := query, succeeded
			test.mutate(&candidateQuery, &candidate)
			if err := candidate.ValidateFor(candidateQuery); err == nil {
				t.Fatal("invalid checkpoint unexpectedly accepted")
			}
		})
	}
}

func TestWorkspaceAnalysisModelResultFinalizationBindsAuthorizedFacts(t *testing.T) {
	authorized := workspaceAnalysisModelAuthorizationCommand()
	run, call, result := workspaceAnalysisModelSuccess(authorized)
	command := FinalizeWorkspaceAnalysisModelResultCommand{
		Identity: authorized.Identity, OperationKey: authorized.OperationKey,
		OperationID: authorized.OperationID, ReservationID: authorized.ReservationID,
		ExpectedCallVersion: authorized.Call.Version, ExpectedRunVersion: authorized.Run.Version,
		Run: run, Call: call, Result: result,
	}
	if err := command.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*FinalizeWorkspaceAnalysisModelResultCommand)
	}{
		{name: "operation", mutate: func(value *FinalizeWorkspaceAnalysisModelResultCommand) {
			value.Result.OperationID = workspaceAnalysisModelTestID(40)
		}},
		{name: "result hash", mutate: func(value *FinalizeWorkspaceAnalysisModelResultCommand) { value.Result.DocumentHash = hashHex('e') }},
		{name: "call hash", mutate: func(value *FinalizeWorkspaceAnalysisModelResultCommand) { value.Call.ResponseHash = hashHex('e') }},
		{name: "run status", mutate: func(value *FinalizeWorkspaceAnalysisModelResultCommand) {
			value.Run.Status = domain.ModelRunFailed
			value.Run.FinalResultType = ""
			value.Run.FinalErrorCode = "MODEL_FAILED"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := command
			candidate.Result.Document = append([]byte(nil), command.Result.Document...)
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("invalid finalization unexpectedly accepted")
			}
		})
	}
}

func TestWorkspaceAnalysisModelCandidateFinalizationBindsAuthorizedFacts(t *testing.T) {
	command := workspaceAnalysisModelAuthorizationCommand()
	command.Identity.NodeKey = domain.WorkspaceAnalysisOperationNodeSynthesizeAnswer
	command.OperationKey.NodeKey = domain.WorkspaceAnalysisOperationNodeSynthesizeAnswer
	command.OperationKey.Kind = domain.WorkspaceAnalysisOperationAnswerSynthesis
	schema := domain.SchemaRef{ID: domain.WorkspaceAnalysisCandidateSchemaID, Version: "1"}
	command.Run.Schema, command.Run.ReducedSchema = schema, schema
	command.Call.Phase, command.Call.Schema = domain.ModelCallAnswer, schema
	command.Call.MaxOutputTokens = int(WorkspaceAnalysisV1SynthesisMaxOutputTokens)

	candidateResult := domain.WorkspaceAnalysisCandidateResult{
		ResultType: domain.ResultTypeWorkspaceAnalysisCandidate, SchemaID: domain.WorkspaceAnalysisCandidateSchemaID,
		SchemaVersion: "1", ModelRunRef: command.Run.ID,
		Payload: domain.WorkspaceAnalysisCandidatePayload{
			AnswerMarkdown: "grounded answer", CitationRefs: []string{"E1"}, ProposalSuggestion: nil,
		},
	}
	document, err := json.Marshal(candidateResult)
	if err != nil {
		t.Fatal(err)
	}
	completedAt := command.Call.StartedAt.Add(time.Second)
	call := command.Call
	call.Status, call.ResponseHash, call.ResponseBytes = domain.ModelCallSucceeded, workspaceAnalysisSHA256(document), int64(len(document))
	call.Usage = domain.TokenUsage{InputTokens: 20, OutputTokens: 8, TotalTokens: 28}
	call.LatencyMillis, call.Version, call.CompletedAt = 1000, 2, &completedAt
	run := command.Run
	run.Status, run.FinalResultType, run.Version = domain.ModelRunSucceeded, domain.ResultTypeWorkspaceAnalysisAnswer, 2
	run.UpdatedAt, run.CompletedAt = completedAt, &completedAt
	candidate := domain.WorkspaceAnalysisCandidate{
		ID: workspaceAnalysisModelTestID(31), WorkspaceID: command.Identity.WorkspaceID,
		AnalysisRunID: command.OperationKey.AnalysisRunID, AnswerID: workspaceAnalysisModelTestID(32),
		SynthesisOperationID: command.OperationID, NodeAttemptID: command.Run.NodeAttemptID,
		SynthesisModelRunID: command.Run.ID, SchemaID: schema.ID, SchemaVersion: 1,
		Document: document, DocumentHash: call.ResponseHash, DocumentBytes: int64(len(document)), CreatedAt: completedAt,
	}
	finalize := FinalizeWorkspaceAnalysisModelCandidateCommand{
		Identity: command.Identity, OperationKey: command.OperationKey,
		OperationID: command.OperationID, ReservationID: command.ReservationID,
		ExpectedCallVersion: command.Call.Version, ExpectedRunVersion: command.Run.Version,
		Run: run, Call: call, Candidate: candidate,
	}
	if err := finalize.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	finalize.Candidate.DocumentHash = hashHex('e')
	if err := finalize.Validate(); err == nil {
		t.Fatal("candidate hash drift unexpectedly accepted")
	}
}

func TestWorkspaceAnalysisCandidateQueryRequiresExactDistinctIDs(t *testing.T) {
	query := WorkspaceAnalysisCandidateQuery{
		WorkspaceID: workspaceAnalysisModelTestID(1), AnalysisRunID: workspaceAnalysisModelTestID(2),
		OperationID: workspaceAnalysisModelTestID(3), ModelRunID: workspaceAnalysisModelTestID(4),
		ModelCallID: workspaceAnalysisModelTestID(5),
	}
	if err := query.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	query.ModelCallID = query.ModelRunID
	if err := query.Validate(); err == nil {
		t.Fatal("reused candidate query id unexpectedly accepted")
	}
}

func TestWorkspaceAnalysisModelFailureFinalizationRequiresMatchingUnknownChargeSemantics(t *testing.T) {
	authorized := workspaceAnalysisModelAuthorizationCommand()
	completedAt := authorized.Call.StartedAt.Add(time.Second)
	call := authorized.Call
	call.Status, call.ErrorCode, call.Version, call.CompletedAt = domain.ModelCallUnknown, "WORKSPACE_ANALYSIS_RESULT_UNKNOWN", 2, &completedAt
	call.LatencyMillis = 1000
	run := authorized.Run
	run.Status, run.FinalErrorCode, run.Version = domain.ModelRunUnknown, call.ErrorCode, 2
	run.UpdatedAt, run.CompletedAt = completedAt, &completedAt
	command := FinalizeWorkspaceAnalysisModelCallCommand{
		Identity: authorized.Identity, OperationKey: authorized.OperationKey,
		OperationID: authorized.OperationID, ReservationID: authorized.ReservationID,
		ExpectedCallVersion: 1, ExpectedRunVersion: 1, Run: run, Call: call,
	}
	if err := command.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	command.Run.Status = domain.ModelRunFailed
	if err := command.Validate(); err == nil {
		t.Fatal("mismatched failure state unexpectedly accepted")
	}
}

func TestWorkspaceAnalysisModelFailureFinalizationAcceptsFrozenModelRefusal(t *testing.T) {
	authorized := workspaceAnalysisModelAuthorizationCommand()
	completedAt := authorized.Call.StartedAt.Add(time.Second)
	response := []byte(`{"result_type":"refusal"}`)
	call := authorized.Call
	call.Status, call.ResponseHash, call.ResponseBytes = domain.ModelCallSucceeded, workspaceAnalysisSHA256(response), int64(len(response))
	call.Usage = domain.TokenUsage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}
	call.LatencyMillis, call.Version, call.CompletedAt = 1000, 2, &completedAt
	run := authorized.Run
	run.Status, run.FinalResultType, run.FinalErrorCode, run.Version =
		domain.ModelRunRefused, domain.ResultTypeRefusal, string(domain.WorkspaceAnalysisRunModelRefused), 2
	run.UpdatedAt, run.CompletedAt = completedAt, &completedAt
	command := FinalizeWorkspaceAnalysisModelCallCommand{
		Identity: authorized.Identity, OperationKey: authorized.OperationKey,
		OperationID: authorized.OperationID, ReservationID: authorized.ReservationID,
		ExpectedCallVersion: 1, ExpectedRunVersion: 1, Run: run, Call: call,
	}
	if err := command.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	command.Run.FinalErrorCode = "WORKSPACE_ANALYSIS_MODEL_FAILED"
	if err := command.Validate(); err == nil {
		t.Fatal("refusal with a different stable reason unexpectedly accepted")
	}
}

func workspaceAnalysisModelAuthorizationCommand() AuthorizeWorkspaceAnalysisModelCallCommand {
	startedAt := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	request := []byte(`{"phase":"PLAN","request":"workspace analysis"}`)
	model := domain.ModelRef{AdapterName: "openai-compatible", AdapterVersion: "v1", ModelID: "model-test", ModelVersion: "2026-08-16"}
	profile := domain.ModelProfileRef{ID: "workspace-analysis-plan", Version: "v1"}
	prompt := domain.PromptRef{ID: "workspace-analysis-plan", Version: "v1"}
	schema := domain.SchemaRef{ID: domain.WorkspaceAnalysisPlanSchemaID, Version: domain.OutputSchemaVersionV1}
	runID := workspaceAnalysisModelTestID(7)
	return AuthorizeWorkspaceAnalysisModelCallCommand{
		Identity: WorkspaceAnalysisModelExecutionIdentity{
			WorkspaceID: workspaceAnalysisModelTestID(1), DefinitionID: workspaceAnalysisModelTestID(2),
			DefinitionVersion: 1, DefinitionHash: hashHex('a'), WorkflowRunID: workspaceAnalysisModelTestID(3),
			NodeKey: domain.WorkspaceAnalysisOperationNodeRetrieveEvidence, NodeRunID: workspaceAnalysisModelTestID(4),
			NodeAttemptID: workspaceAnalysisModelTestID(5), LeaseOwner: "worker-a", LeaseFence: 1,
		},
		OperationKey: domain.WorkspaceAnalysisOperationKey{
			AnalysisRunID: workspaceAnalysisModelTestID(6), NodeKey: domain.WorkspaceAnalysisOperationNodeRetrieveEvidence,
			Kind: domain.WorkspaceAnalysisOperationRetrievalPlan, Ordinal: 1,
		},
		OperationID: workspaceAnalysisModelTestID(8), ReservationID: workspaceAnalysisModelTestID(9),
		Run: domain.ModelRun{
			ID: runID, WorkspaceID: workspaceAnalysisModelTestID(1), WorkflowRunID: workspaceAnalysisModelTestID(3),
			NodeRunID: workspaceAnalysisModelTestID(4), NodeAttemptID: workspaceAnalysisModelTestID(5),
			Model: model, Profile: profile, Prompt: prompt, Schema: schema, ReducedSchema: schema,
			Retrieval: domain.RetrievalRef{IndexVersionID: workspaceAnalysisModelTestID(10)},
			Status:    domain.ModelRunRunning, Version: 1, CreatedAt: startedAt, UpdatedAt: startedAt,
		},
		Call: domain.ModelCall{
			ID: workspaceAnalysisModelTestID(11), ModelRunID: runID, CallNo: 1, Phase: domain.ModelCallPlan,
			Model: model, Profile: profile, Prompt: prompt, Schema: schema,
			MaxOutputTokens: int(WorkspaceAnalysisV1PlanMaxOutputTokens), Status: domain.ModelCallStarted,
			RequestHash: workspaceAnalysisSHA256(request), RequestBytes: int64(len(request)), Version: 1, StartedAt: startedAt,
		},
		RequestDocument: request,
	}
}

func workspaceAnalysisModelSuccess(command AuthorizeWorkspaceAnalysisModelCallCommand) (domain.ModelRun, domain.ModelCall, domain.WorkspaceAnalysisModelResult) {
	plan := domain.WorkspaceAnalysisPlanResult{
		ResultType: domain.ResultTypeWorkspaceAnalysisPlan, SchemaID: domain.WorkspaceAnalysisPlanSchemaID,
		SchemaVersion: domain.OutputSchemaVersionV1, ModelRunRef: command.Run.ID,
		Payload: domain.RAGQueryPlanPayload{
			Intent: "inspect workspace", Rewrites: []string{"inspect workspace"}, SuggestedScopes: []string{},
		},
	}
	document, _ := json.Marshal(plan)
	completedAt := command.Call.StartedAt.Add(time.Second)
	call := command.Call
	call.Status, call.ResponseHash, call.ResponseBytes = domain.ModelCallSucceeded, workspaceAnalysisSHA256(document), int64(len(document))
	call.Usage = domain.TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}
	call.LatencyMillis, call.Version, call.CompletedAt = 1000, 2, &completedAt
	run := command.Run
	run.Status, run.FinalResultType, run.Version = domain.ModelRunSucceeded, domain.ResultTypeWorkspaceAnalysisPlan, 2
	run.UpdatedAt, run.CompletedAt = completedAt, &completedAt
	result := domain.WorkspaceAnalysisModelResult{
		ID: workspaceAnalysisModelTestID(30), WorkspaceID: command.Identity.WorkspaceID,
		AnalysisRunID: command.OperationKey.AnalysisRunID, OperationID: command.OperationID,
		NodeAttemptID: command.Run.NodeAttemptID, ModelRunID: command.Run.ID, ModelCallID: command.Call.ID,
		OperationKind: domain.WorkspaceAnalysisOperationRetrievalPlan, Schema: command.Run.Schema,
		Document: document, DocumentHash: call.ResponseHash, DocumentBytes: int64(len(document)), CreatedAt: completedAt,
	}
	return run, call, result
}

func workspaceAnalysisModelTestID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("8b000000-0000-4000-8000-%012d", value))
}
