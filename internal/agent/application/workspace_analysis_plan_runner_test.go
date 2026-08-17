package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestWorkspaceAnalysisRetrievalPlanRunnerAuthorizesBeforeProviderAndFinalizesCanonicalResult(t *testing.T) {
	events := []string{}
	repository := &workspaceAnalysisPlanRepository{events: &events}
	model := &workspaceAnalysisPlanModel{
		events: &events,
		response: ChatResponse{
			Model: workspaceAnalysisPlanModelRef(), Content: workspaceAnalysisPlanProviderDocument(t, "find workspace policy"),
			Usage: domain.TokenUsage{InputTokens: 23, OutputTokens: 7, TotalTokens: 30},
		},
	}
	runner := newWorkspaceAnalysisPlanRunner(t, model, repository)
	request := workspaceAnalysisPlanRequest()

	result, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !reflect.DeepEqual(events, []string{"authorize", "provider", "finalize_result"}) {
		t.Fatalf("events = %#v", events)
	}
	if model.CallCount() != 1 || repository.authorizeCalls != 1 || repository.finalizeResultCalls != 1 ||
		repository.finalizeCallCalls != 0 || repository.loadCalls != 0 {
		t.Fatalf("calls = provider:%d authorize:%d finalize_result:%d finalize_call:%d load:%d",
			model.CallCount(), repository.authorizeCalls, repository.finalizeResultCalls,
			repository.finalizeCallCalls, repository.loadCalls)
	}
	providerCall := model.Calls()[0]
	if providerCall.Phase != domain.ModelCallPlan || providerCall.SchemaRef != (domain.SchemaRef{ID: domain.WorkspaceAnalysisPlanSchemaID, Version: domain.OutputSchemaVersionV1}) ||
		providerCall.MaxOutputTokens != int(WorkspaceAnalysisV1PlanMaxOutputTokens) || len(providerCall.Messages) != 3 ||
		strings.Contains(providerCall.Messages[2].Content, "model_run_ref") || strings.Contains(providerCall.Messages[2].Content, string(request.AnalysisRunID)) {
		t.Fatalf("provider call = %#v", providerCall)
	}
	requestDocument, err := json.Marshal(providerCall)
	if err != nil {
		t.Fatal(err)
	}
	authorized := repository.authorizedCommands[0]
	if !reflect.DeepEqual(authorized.RequestDocument, requestDocument) ||
		authorized.Call.RequestHash != workspaceAnalysisSHA256(requestDocument) ||
		authorized.Call.RequestBytes != int64(len(requestDocument)) || authorized.Call.CallNo != 1 ||
		authorized.Call.Phase != domain.ModelCallPlan || authorized.Call.MaxOutputTokens != int(WorkspaceAnalysisV1PlanMaxOutputTokens) {
		t.Fatalf("authorized command = %#v", authorized)
	}
	if result.Plan.ModelRunRef != result.Run.ID || result.ModelResult.ModelRunID != result.Run.ID ||
		result.ModelResult.ModelCallID != result.Call.ID || result.ModelResult.DocumentHash != result.Call.ResponseHash ||
		result.Usage != (domain.TokenUsage{InputTokens: 23, OutputTokens: 7, TotalTokens: 30}) || result.Replayed {
		t.Fatalf("result = %#v", result)
	}
	canonical, err := json.Marshal(result.Plan)
	if err != nil || !reflect.DeepEqual([]byte(result.ModelResult.Document), canonical) {
		t.Fatalf("canonical result = %s, %v", result.ModelResult.Document, err)
	}
	finalized := repository.finalizeResultCommands[0]
	if finalized.Call.Usage != result.Usage || finalized.Result.DocumentHash != workspaceAnalysisSHA256(canonical) ||
		finalized.Result.ModelRunID != authorized.Run.ID || finalized.Run.FinalResultType != domain.ResultTypeWorkspaceAnalysisPlan {
		t.Fatalf("finalized command = %#v", finalized)
	}
}

func TestWorkspaceAnalysisRetrievalPlanRunnerSettlesCallWhenCancellationWinsResultPublication(t *testing.T) {
	repository := &workspaceAnalysisPlanRepository{
		finalizeResultErr: foundation.NewError(
			foundation.ErrorVersionConflict,
			ErrorCodeWorkspaceAnalysisModelCancellationConflict,
			false,
			errors.New("cancel requested before plan publication"),
		),
	}
	model := &workspaceAnalysisPlanModel{response: ChatResponse{
		Model: workspaceAnalysisPlanModelRef(), Content: workspaceAnalysisPlanProviderDocument(t, "find workspace policy"),
		Usage: domain.TokenUsage{InputTokens: 23, OutputTokens: 7, TotalTokens: 30},
	}}
	runner := newWorkspaceAnalysisPlanRunner(t, model, repository)

	_, err := runner.Run(context.Background(), workspaceAnalysisPlanRequest())
	if err == nil {
		t.Fatal("Run unexpectedly published a plan after cancellation")
	}
	if model.CallCount() != 1 || repository.finalizeResultCalls != 1 || repository.finalizeCallCalls != 1 {
		t.Fatalf("calls = provider:%d result:%d failure:%d", model.CallCount(), repository.finalizeResultCalls, repository.finalizeCallCalls)
	}
	if _, found := WorkspaceAnalysisModelTerminalEvidenceFromError(err); !found {
		t.Fatalf("Run error did not retain settled model terminal evidence: %v", err)
	}
	if command := repository.finalizeCallCommands[0]; command.Call.Status != domain.ModelCallFailed ||
		command.Run.Status != domain.ModelRunFailed || command.Call.ErrorCode != ErrorCodeOperationCancelled {
		t.Fatalf("settlement command = %#v", command)
	}
}

func TestWorkspaceAnalysisRetrievalPlanRunnerReplacementReusesPersistedResultWithoutProvider(t *testing.T) {
	repository := &workspaceAnalysisPlanRepository{}
	repository.authorize = func(command AuthorizeWorkspaceAnalysisModelCallCommand) (WorkspaceAnalysisModelAuthorizationResult, error) {
		persisted := command
		persisted.OperationID = workspaceAnalysisPlanTestID(810)
		persisted.ReservationID = workspaceAnalysisPlanTestID(811)
		persisted.Run.ID = workspaceAnalysisPlanTestID(812)
		persisted.Run.NodeAttemptID = workspaceAnalysisPlanTestID(813)
		persisted.Call.ID = workspaceAnalysisPlanTestID(814)
		persisted.Call.ModelRunID = persisted.Run.ID
		authorized, result := workspaceAnalysisPlanSuccessfulReplay(t, persisted)
		repository.loadedResult = result
		return authorized, nil
	}
	model := &workspaceAnalysisPlanModel{}
	runner := newWorkspaceAnalysisPlanRunner(t, model, repository)
	request := workspaceAnalysisPlanRequest()

	result, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Replayed || result.Plan.ModelRunRef != result.Run.ID || result.ModelResult.ID != repository.loadedResult.ID ||
		result.Run.NodeAttemptID == request.Identity.NodeAttemptID || result.ModelResult.NodeAttemptID != result.Run.NodeAttemptID ||
		model.CallCount() != 0 || repository.loadCalls != 1 || repository.finalizeCallCalls != 0 || repository.finalizeResultCalls != 0 {
		t.Fatalf("result/calls = %#v provider:%d load:%d finalize:%d/%d", result, model.CallCount(), repository.loadCalls,
			repository.finalizeCallCalls, repository.finalizeResultCalls)
	}
}

func TestWorkspaceAnalysisRetrievalPlanRunnerFindsFrozenCheckpointBeforePlanning(t *testing.T) {
	request := workspaceAnalysisPlanRequest()
	query := WorkspaceAnalysisRetrievalPlanCheckpointQuery{
		WorkspaceID: request.Identity.WorkspaceID, WorkflowRunID: request.Identity.WorkflowRunID,
		AnalysisRunID: request.AnalysisRunID, NodeRunID: request.Identity.NodeRunID,
	}
	repository := &workspaceAnalysisPlanRepository{
		checkpointFound: true,
		checkpoint: WorkspaceAnalysisRetrievalPlanCheckpoint{
			OperationID: workspaceAnalysisPlanTestID(70), ModelRunID: workspaceAnalysisPlanTestID(71),
			ModelCallID: workspaceAnalysisPlanTestID(72), Retrieval: request.Retrieval,
			RequestHash: hashHex('a'), Status: domain.WorkspaceAnalysisOperationStarted,
		},
	}
	runner := newWorkspaceAnalysisPlanRunner(t, &workspaceAnalysisPlanModel{}, repository)
	checkpoint, found, err := runner.FindWorkspaceAnalysisRetrievalPlanCheckpoint(context.Background(), query)
	if err != nil || !found || checkpoint != repository.checkpoint || repository.checkpointCalls != 1 ||
		repository.checkpointQuery != query {
		t.Fatalf("checkpoint=%#v found=%t calls=%d query=%#v err=%v",
			checkpoint, found, repository.checkpointCalls, repository.checkpointQuery, err)
	}

	repository.checkpoint.RequestHash = "invalid"
	if _, _, err := runner.FindWorkspaceAnalysisRetrievalPlanCheckpoint(context.Background(), query); err == nil {
		t.Fatal("invalid persisted checkpoint unexpectedly accepted")
	}
}

func TestWorkspaceAnalysisRetrievalPlanRunnerNeverCallsProviderForNonCreatedDisposition(t *testing.T) {
	tests := []struct {
		name        string
		disposition WorkspaceAnalysisModelAuthorizationDisposition
		status      domain.ModelCallStatus
		runStatus   domain.ModelRunStatus
		code        string
		wantCode    string
		wantKind    foundation.ErrorKind
	}{
		{name: "reconcile", disposition: WorkspaceAnalysisModelAuthorizationReconcile, status: domain.ModelCallStarted, runStatus: domain.ModelRunRunning, wantCode: ErrorCodeModelCallReplayUnsafe, wantKind: foundation.ErrorManualRecoveryRequired},
		{name: "failed", disposition: WorkspaceAnalysisModelAuthorizationReplayFailure, status: domain.ModelCallFailed, runStatus: domain.ModelRunFailed, code: "MODEL_PROVIDER_FAILED", wantCode: "MODEL_PROVIDER_FAILED", wantKind: foundation.ErrorNonRetryableFailure},
		{name: "unknown", disposition: WorkspaceAnalysisModelAuthorizationTerminateUnknown, status: domain.ModelCallUnknown, runStatus: domain.ModelRunUnknown, code: "MODEL_PROVIDER_RESULT_UNKNOWN", wantCode: "MODEL_PROVIDER_RESULT_UNKNOWN", wantKind: foundation.ErrorManualRecoveryRequired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &workspaceAnalysisPlanRepository{}
			persistedOperationID := workspaceAnalysisPlanTestID(901)
			repository.authorize = func(command AuthorizeWorkspaceAnalysisModelCallCommand) (WorkspaceAnalysisModelAuthorizationResult, error) {
				result := WorkspaceAnalysisModelAuthorizationResult{
					Run: command.Run, Call: command.Call, OperationID: persistedOperationID,
					ReservationID: command.ReservationID, Disposition: test.disposition,
				}
				if test.status != domain.ModelCallStarted {
					completedAt := command.Call.StartedAt.Add(time.Second)
					result.Call.Status, result.Call.ErrorCode, result.Call.Version = test.status, test.code, 2
					result.Call.CompletedAt, result.Call.LatencyMillis = &completedAt, 1000
					result.Run.Status, result.Run.FinalErrorCode, result.Run.Version = test.runStatus, test.code, 2
					result.Run.UpdatedAt, result.Run.CompletedAt = completedAt, &completedAt
				}
				return result, nil
			}
			model := &workspaceAnalysisPlanModel{}
			runner := newWorkspaceAnalysisPlanRunner(t, model, repository)
			_, err := runner.Run(context.Background(), workspaceAnalysisPlanRequest())
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Code != test.wantCode || classified.Kind != test.wantKind {
				t.Fatalf("Run error = %#v", err)
			}
			if test.disposition == WorkspaceAnalysisModelAuthorizationReconcile {
				if _, found := WorkspaceAnalysisModelTerminalEvidenceFromError(err); found {
					t.Fatal("reconciliation unexpectedly exposed terminal evidence")
				}
			} else {
				assertWorkspaceAnalysisModelTerminalEvidence(t, err, persistedOperationID, test.status, test.runStatus, test.wantKind, test.wantCode)
			}
			if model.CallCount() != 0 || repository.finalizeCallCalls != 0 || repository.finalizeResultCalls != 0 || repository.loadCalls != 0 {
				t.Fatalf("calls = provider:%d finalize:%d/%d load:%d", model.CallCount(), repository.finalizeCallCalls,
					repository.finalizeResultCalls, repository.loadCalls)
			}
		})
	}
}

func TestWorkspaceAnalysisRetrievalPlanRunnerRefusesAuthorizationHashAndSchemaDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*WorkspaceAnalysisModelAuthorizationResult)
	}{
		{name: "hash", mutate: func(result *WorkspaceAnalysisModelAuthorizationResult) { result.Call.RequestHash = hashHex('d') }},
		{name: "schema", mutate: func(result *WorkspaceAnalysisModelAuthorizationResult) {
			result.Run.Schema = domain.SchemaRef{ID: domain.RAGQueryPlanSchemaID, Version: domain.OutputSchemaVersionV1}
			result.Run.ReducedSchema = result.Run.Schema
			result.Call.Schema = result.Run.Schema
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &workspaceAnalysisPlanRepository{}
			repository.authorize = func(command AuthorizeWorkspaceAnalysisModelCallCommand) (WorkspaceAnalysisModelAuthorizationResult, error) {
				result := WorkspaceAnalysisModelAuthorizationResult{
					Run: command.Run, Call: command.Call, OperationID: command.OperationID,
					ReservationID: command.ReservationID, Disposition: WorkspaceAnalysisModelAuthorizationCreated,
				}
				test.mutate(&result)
				return result, nil
			}
			model := &workspaceAnalysisPlanModel{}
			runner := newWorkspaceAnalysisPlanRunner(t, model, repository)
			_, err := runner.Run(context.Background(), workspaceAnalysisPlanRequest())
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Code != ErrorCodeWorkspaceAnalysisModelAuthorizationInvalid {
				t.Fatalf("Run error = %#v", err)
			}
			if model.CallCount() != 0 || repository.finalizeCallCalls != 0 || repository.finalizeResultCalls != 0 {
				t.Fatalf("calls = provider:%d finalize:%d/%d", model.CallCount(), repository.finalizeCallCalls, repository.finalizeResultCalls)
			}
		})
	}
}

func TestWorkspaceAnalysisRetrievalPlanRunnerRejectsMalformedOrIdentityLeakingOutput(t *testing.T) {
	canonical := string(workspaceAnalysisPlanProviderDocument(t, "find workspace policy"))
	request := workspaceAnalysisPlanRequest()
	tests := map[string][]byte{
		"identity field":     []byte(`{"i":"find workspace policy","r":["workspace policy"],"d":"","q":"","s":[],"model_run_ref":"9d000000-0000-4000-8000-000000000001"}`),
		"identity value":     workspaceAnalysisPlanProviderDocument(t, string(request.Identity.WorkspaceID)),
		"retrieval identity": workspaceAnalysisPlanProviderDocument(t, string(request.Retrieval.IndexVersionID)),
		"unknown field":      []byte(`{"i":"find workspace policy","r":["workspace policy"],"d":"","q":"","s":[],"x":true}`),
		"null required list": []byte(`{"i":"find workspace policy","r":null,"d":"","q":"","s":[]}`),
		"duplicate":          []byte(`{"i":"one","i":"two","r":["workspace policy"],"d":"","q":"","s":[]}`),
		"oversize":           bytes.Repeat([]byte("x"), int(domain.MaxWorkspaceAnalysisModelResultBytes)+1),
		"trailing":           []byte(canonical + `{}`),
	}
	for name, output := range tests {
		t.Run(name, func(t *testing.T) {
			repository := &workspaceAnalysisPlanRepository{}
			model := &workspaceAnalysisPlanModel{response: ChatResponse{
				Model: workspaceAnalysisPlanModelRef(), Content: output,
				Usage: domain.TokenUsage{InputTokens: 11, OutputTokens: 3, TotalTokens: 14},
			}}
			runner := newWorkspaceAnalysisPlanRunner(t, model, repository)
			_, err := runner.Run(context.Background(), request)
			if err == nil {
				t.Fatal("invalid provider output unexpectedly succeeded")
			}
			if model.CallCount() != 1 || repository.finalizeCallCalls != 1 || repository.finalizeResultCalls != 0 {
				t.Fatalf("calls = provider:%d finalize_call:%d finalize_result:%d", model.CallCount(),
					repository.finalizeCallCalls, repository.finalizeResultCalls)
			}
			terminal := repository.finalizeCallCommands[0].Call
			if terminal.Status != domain.ModelCallFailed || terminal.Usage != (domain.TokenUsage{InputTokens: 11, OutputTokens: 3, TotalTokens: 14}) ||
				terminal.ResponseHash != workspaceAnalysisSHA256(output) || terminal.ResponseBytes != int64(len(output)) {
				t.Fatalf("terminal = %#v", terminal)
			}
		})
	}
}

func TestWorkspaceAnalysisRetrievalPlanRunnerAcceptsEquivalentProviderJSONAndPersistsCanonicalResult(t *testing.T) {
	tests := map[string][]byte{
		"leading whitespace": []byte(" \n" + string(workspaceAnalysisPlanProviderDocument(t, "find workspace policy")) + "\n"),
		"key order":          []byte(`{"r":["workspace policy"],"i":"find workspace policy","s":[],"q":"","d":""}`),
	}
	for name, output := range tests {
		t.Run(name, func(t *testing.T) {
			repository := &workspaceAnalysisPlanRepository{}
			model := &workspaceAnalysisPlanModel{response: ChatResponse{
				Model: workspaceAnalysisPlanModelRef(), Content: output,
				Usage: domain.TokenUsage{InputTokens: 11, OutputTokens: 3, TotalTokens: 14},
			}}
			runner := newWorkspaceAnalysisPlanRunner(t, model, repository)

			result, err := runner.Run(context.Background(), workspaceAnalysisPlanRequest())
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			canonical, err := json.Marshal(result.Plan)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(result.ModelResult.Document, canonical) || bytes.Equal(result.ModelResult.Document, output) ||
				result.Call.ResponseHash != workspaceAnalysisSHA256(canonical) || result.Call.ResponseBytes != int64(len(canonical)) ||
				model.CallCount() != 1 || repository.finalizeResultCalls != 1 || repository.finalizeCallCalls != 0 {
				t.Fatalf("result/calls = %#v provider:%d finalize:%d/%d", result, model.CallCount(),
					repository.finalizeCallCalls, repository.finalizeResultCalls)
			}
		})
	}
}

func TestWorkspaceAnalysisRetrievalPlanRunnerClassifiesProviderFailureAndUnknown(t *testing.T) {
	tests := []struct {
		name       string
		provider   error
		wantKind   foundation.ErrorKind
		wantCode   string
		wantStatus domain.ModelCallStatus
		wantRun    domain.ModelRunStatus
		wantUsage  domain.TokenUsage
	}{
		{
			name: "known failure", provider: foundation.NewError(foundation.ErrorRetryableFailure, "MODEL_PROVIDER_UNAVAILABLE", true, errors.New("unavailable")),
			wantKind: foundation.ErrorRetryableFailure, wantCode: "MODEL_PROVIDER_UNAVAILABLE",
			wantStatus: domain.ModelCallFailed, wantRun: domain.ModelRunFailed,
			wantUsage: domain.TokenUsage{InputTokens: 13, OutputTokens: 2, TotalTokens: 15},
		},
		{
			name: "unknown", provider: foundation.NewError(foundation.ErrorManualRecoveryRequired, "MODEL_PROVIDER_RESULT_UNKNOWN", false, errors.New("unknown")),
			wantKind: foundation.ErrorManualRecoveryRequired, wantCode: "MODEL_PROVIDER_RESULT_UNKNOWN",
			wantStatus: domain.ModelCallUnknown, wantRun: domain.ModelRunUnknown, wantUsage: domain.TokenUsage{},
		},
		{
			name: "unclassified outcome", provider: errors.New("transport ended without a classified outcome"),
			wantKind: foundation.ErrorManualRecoveryRequired, wantCode: ErrorCodeModelCallPersistenceUnknown,
			wantStatus: domain.ModelCallUnknown, wantRun: domain.ModelRunUnknown, wantUsage: domain.TokenUsage{},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &workspaceAnalysisPlanRepository{}
			model := &workspaceAnalysisPlanModel{
				response: ChatResponse{
					Model: workspaceAnalysisPlanModelRef(), Content: []byte(`{"partial":true}`),
					Usage: domain.TokenUsage{InputTokens: 13, OutputTokens: 2, TotalTokens: 15},
				},
				err: test.provider,
			}
			runner := newWorkspaceAnalysisPlanRunner(t, model, repository)
			_, err := runner.Run(context.Background(), workspaceAnalysisPlanRequest())
			var got *foundation.Error
			if !errors.As(err, &got) || got.Code != test.wantCode || got.Kind != test.wantKind {
				t.Fatalf("Run error = %#v", err)
			}
			terminal := repository.finalizeCallCommands[0]
			if terminal.Call.Status != test.wantStatus || terminal.Run.Status != test.wantRun || terminal.Call.Usage != test.wantUsage {
				t.Fatalf("terminal = %#v", terminal)
			}
			if test.wantStatus == domain.ModelCallUnknown && (terminal.Call.ResponseHash != "" || terminal.Call.ResponseBytes != 0) {
				t.Fatalf("unknown terminal retained response = %#v", terminal.Call)
			}
			assertWorkspaceAnalysisModelTerminalEvidence(t, err, terminal.OperationID, test.wantStatus, test.wantRun, test.wantKind, test.wantCode)
		})
	}
}

func TestWorkspaceAnalysisRetrievalPlanRunnerReturnsUnknownWhenFailureFinalizationIsUnconfirmed(t *testing.T) {
	repository := &workspaceAnalysisPlanRepository{finalizeCallErr: errors.New("failure commit acknowledgement lost")}
	model := &workspaceAnalysisPlanModel{
		response: ChatResponse{
			Model: workspaceAnalysisPlanModelRef(), Content: []byte(`{"partial":true}`),
			Usage: domain.TokenUsage{InputTokens: 13, OutputTokens: 2, TotalTokens: 15},
		},
		err: foundation.NewError(
			foundation.ErrorRetryableFailure,
			"MODEL_PROVIDER_UNAVAILABLE",
			true,
			errors.New("unavailable"),
		),
	}
	runner := newWorkspaceAnalysisPlanRunner(t, model, repository)

	_, err := runner.Run(context.Background(), workspaceAnalysisPlanRequest())
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorManualRecoveryRequired ||
		classified.Code != ErrorCodeModelCallPersistenceUnknown {
		t.Fatalf("Run error = %#v", err)
	}
	if _, found := WorkspaceAnalysisModelTerminalEvidenceFromError(err); found {
		t.Fatal("unconfirmed failure finalization unexpectedly exposed terminal evidence")
	}
	if model.CallCount() != 1 || repository.finalizeCallCalls != 1 || repository.finalizeResultCalls != 0 ||
		repository.finalizeCallCommands[0].Call.Status != domain.ModelCallFailed ||
		repository.finalizeCallCommands[0].Call.Usage != (domain.TokenUsage{InputTokens: 13, OutputTokens: 2, TotalTokens: 15}) {
		t.Fatalf("calls = provider:%d finalize:%d/%d command:%#v", model.CallCount(), repository.finalizeCallCalls,
			repository.finalizeResultCalls, repository.finalizeCallCommands)
	}
}

func TestWorkspaceAnalysisRetrievalPlanRunnerCancellationAfterAuthorizationSkipsProvider(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	repository := &workspaceAnalysisPlanRepository{}
	repository.authorize = func(command AuthorizeWorkspaceAnalysisModelCallCommand) (WorkspaceAnalysisModelAuthorizationResult, error) {
		cancel()
		return WorkspaceAnalysisModelAuthorizationResult{
			Run: command.Run, Call: command.Call, OperationID: command.OperationID,
			ReservationID: command.ReservationID, Disposition: WorkspaceAnalysisModelAuthorizationCreated,
		}, nil
	}
	model := &workspaceAnalysisPlanModel{}
	runner := newWorkspaceAnalysisPlanRunner(t, model, repository)

	_, err := runner.Run(ctx, workspaceAnalysisPlanRequest())
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != ErrorCodeOperationCancelled ||
		classified.Kind != foundation.ErrorNonRetryableFailure {
		t.Fatalf("Run error = %#v", err)
	}
	if model.CallCount() != 0 || repository.finalizeCallCalls != 1 || repository.finalizeResultCalls != 0 ||
		repository.finalizeCallCommands[0].Call.Status != domain.ModelCallFailed {
		t.Fatalf("calls = provider:%d finalize:%d/%d terminal:%#v", model.CallCount(), repository.finalizeCallCalls,
			repository.finalizeResultCalls, repository.finalizeCallCommands)
	}
}

func TestWorkspaceAnalysisRetrievalPlanRunnerRecoversCommitResponseLossWithoutSecondProviderCall(t *testing.T) {
	repository := &workspaceAnalysisPlanRepository{finalizeResultErr: errors.New("commit acknowledgement lost")}
	model := &workspaceAnalysisPlanModel{response: ChatResponse{
		Model: workspaceAnalysisPlanModelRef(), Content: workspaceAnalysisPlanProviderDocument(t, "find workspace policy"),
		Usage: domain.TokenUsage{InputTokens: 17, OutputTokens: 5, TotalTokens: 22},
	}}
	runner := newWorkspaceAnalysisPlanRunner(t, model, repository)

	request := workspaceAnalysisPlanRequest()
	result, err := runner.Run(context.Background(), request)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorManualRecoveryRequired ||
		classified.Code != ErrorCodeModelCallPersistenceUnknown {
		t.Fatalf("Run error = %#v", err)
	}
	if _, found := WorkspaceAnalysisModelTerminalEvidenceFromError(err); found {
		t.Fatal("unconfirmed successful finalization unexpectedly exposed terminal evidence")
	}
	if !reflect.DeepEqual(result, WorkspaceAnalysisRetrievalPlanResult{}) || model.CallCount() != 1 || repository.finalizeResultCalls != 1 ||
		repository.finalizeCallCalls != 0 || repository.loadCalls != 0 {
		t.Fatalf("result/calls = %#v provider:%d finalize:%d/%d load:%d", result, model.CallCount(),
			repository.finalizeCallCalls, repository.finalizeResultCalls, repository.loadCalls)
	}
	if repository.finalizeResultCommands[0].Call.Usage != (domain.TokenUsage{InputTokens: 17, OutputTokens: 5, TotalTokens: 22}) {
		t.Fatalf("usage = %#v", repository.finalizeResultCommands[0].Call.Usage)
	}

	committed := repository.finalizeResultCommands[0]
	repository.loadedResult = committed.Result
	repository.finalizeResultErr = nil
	repository.authorize = func(command AuthorizeWorkspaceAnalysisModelCallCommand) (WorkspaceAnalysisModelAuthorizationResult, error) {
		return WorkspaceAnalysisModelAuthorizationResult{
			Run: committed.Run, Call: committed.Call, OperationID: committed.OperationID,
			ReservationID: committed.ReservationID, Disposition: WorkspaceAnalysisModelAuthorizationReuseResult,
		}, nil
	}
	replacement := request
	replacement.Identity.NodeAttemptID = workspaceAnalysisPlanTestID(820)
	replacement.Identity.LeaseOwner = "worker-b"
	replacement.Identity.LeaseFence++

	replayed, err := runner.Run(context.Background(), replacement)
	if err != nil {
		t.Fatalf("replacement Run: %v", err)
	}
	if !replayed.Replayed || replayed.ModelResult.ID != committed.Result.ID || replayed.Run.ID != committed.Run.ID ||
		replayed.Run.NodeAttemptID != request.Identity.NodeAttemptID || model.CallCount() != 1 || repository.authorizeCalls != 2 ||
		repository.finalizeResultCalls != 1 || repository.finalizeCallCalls != 0 || repository.loadCalls != 1 {
		t.Fatalf("replay/calls = %#v provider:%d authorize:%d finalize:%d/%d load:%d", replayed, model.CallCount(),
			repository.authorizeCalls, repository.finalizeResultCalls, repository.finalizeCallCalls, repository.loadCalls)
	}
	if repository.authorizedCommands[0].Call.RequestHash != repository.authorizedCommands[1].Call.RequestHash ||
		repository.authorizedCommands[0].Run.ID == repository.authorizedCommands[1].Run.ID ||
		repository.authorizedCommands[0].Call.ID == repository.authorizedCommands[1].Call.ID {
		t.Fatalf("replacement authorization candidates drifted = %#v", repository.authorizedCommands)
	}
}

type workspaceAnalysisPlanRepository struct {
	authorize         func(AuthorizeWorkspaceAnalysisModelCallCommand) (WorkspaceAnalysisModelAuthorizationResult, error)
	finalizeResultErr error
	finalizeCallErr   error
	loadErr           error
	loadedResult      domain.WorkspaceAnalysisModelResult
	checkpoint        WorkspaceAnalysisRetrievalPlanCheckpoint
	checkpointFound   bool
	checkpointErr     error
	events            *[]string

	authorizeCalls         int
	finalizeResultCalls    int
	finalizeCallCalls      int
	loadCalls              int
	checkpointCalls        int
	checkpointQuery        WorkspaceAnalysisRetrievalPlanCheckpointQuery
	authorizedCommands     []AuthorizeWorkspaceAnalysisModelCallCommand
	finalizeResultCommands []FinalizeWorkspaceAnalysisModelResultCommand
	finalizeCallCommands   []FinalizeWorkspaceAnalysisModelCallCommand
}

func (repository *workspaceAnalysisPlanRepository) FindWorkspaceAnalysisRetrievalPlanCheckpoint(
	_ context.Context,
	query WorkspaceAnalysisRetrievalPlanCheckpointQuery,
) (WorkspaceAnalysisRetrievalPlanCheckpoint, bool, error) {
	repository.checkpointCalls++
	repository.checkpointQuery = query
	return repository.checkpoint, repository.checkpointFound, repository.checkpointErr
}

func (repository *workspaceAnalysisPlanRepository) AuthorizeWorkspaceAnalysisModelCall(
	_ context.Context,
	command AuthorizeWorkspaceAnalysisModelCallCommand,
) (WorkspaceAnalysisModelAuthorizationResult, error) {
	repository.authorizeCalls++
	repository.authorizedCommands = append(repository.authorizedCommands, command)
	repository.record("authorize")
	if repository.authorize != nil {
		return repository.authorize(command)
	}
	return WorkspaceAnalysisModelAuthorizationResult{
		Run: command.Run, Call: command.Call, OperationID: command.OperationID,
		ReservationID: command.ReservationID, Disposition: WorkspaceAnalysisModelAuthorizationCreated,
	}, nil
}

func (repository *workspaceAnalysisPlanRepository) FinalizeWorkspaceAnalysisModelCall(
	_ context.Context,
	command FinalizeWorkspaceAnalysisModelCallCommand,
) (WorkspaceAnalysisModelMutationResult, error) {
	repository.finalizeCallCalls++
	repository.finalizeCallCommands = append(repository.finalizeCallCommands, command)
	repository.record("finalize_call")
	if repository.finalizeCallErr != nil {
		return WorkspaceAnalysisModelMutationResult{}, repository.finalizeCallErr
	}
	return WorkspaceAnalysisModelMutationResult{
		Run: command.Run, Call: command.Call, OperationID: command.OperationID, ReservationID: command.ReservationID,
	}, nil
}

func (repository *workspaceAnalysisPlanRepository) FinalizeWorkspaceAnalysisModelResult(
	_ context.Context,
	command FinalizeWorkspaceAnalysisModelResultCommand,
) (WorkspaceAnalysisModelMutationResult, error) {
	repository.finalizeResultCalls++
	repository.finalizeResultCommands = append(repository.finalizeResultCommands, command)
	repository.record("finalize_result")
	if repository.finalizeResultErr != nil {
		return WorkspaceAnalysisModelMutationResult{}, repository.finalizeResultErr
	}
	result := command.Result
	return WorkspaceAnalysisModelMutationResult{
		Run: command.Run, Call: command.Call, Result: &result,
		OperationID: command.OperationID, ReservationID: command.ReservationID,
	}, nil
}

func (repository *workspaceAnalysisPlanRepository) LoadWorkspaceAnalysisModelResult(
	_ context.Context,
	_ WorkspaceAnalysisModelResultQuery,
) (domain.WorkspaceAnalysisModelResult, error) {
	repository.loadCalls++
	repository.record("load")
	if repository.loadErr != nil {
		return domain.WorkspaceAnalysisModelResult{}, repository.loadErr
	}
	result := repository.loadedResult
	result.Document = append(json.RawMessage(nil), repository.loadedResult.Document...)
	return result, nil
}

func (repository *workspaceAnalysisPlanRepository) FinalizeWorkspaceAnalysisModelCandidate(
	_ context.Context,
	_ FinalizeWorkspaceAnalysisModelCandidateCommand,
) (WorkspaceAnalysisModelMutationResult, error) {
	return WorkspaceAnalysisModelMutationResult{}, errors.New("unexpected candidate finalization")
}

func (repository *workspaceAnalysisPlanRepository) LoadWorkspaceAnalysisCandidate(
	_ context.Context,
	_ WorkspaceAnalysisCandidateQuery,
) (domain.WorkspaceAnalysisCandidate, error) {
	return domain.WorkspaceAnalysisCandidate{}, errors.New("unexpected candidate load")
}

func (repository *workspaceAnalysisPlanRepository) record(event string) {
	if repository.events != nil {
		*repository.events = append(*repository.events, event)
	}
}

type workspaceAnalysisPlanModel struct {
	mu       sync.Mutex
	response ChatResponse
	err      error
	calls    []ChatRequest
	events   *[]string
}

func (model *workspaceAnalysisPlanModel) Chat(_ context.Context, request ChatRequest) (ChatResponse, error) {
	model.mu.Lock()
	defer model.mu.Unlock()
	model.calls = append(model.calls, cloneChatRequest(request))
	if model.events != nil {
		*model.events = append(*model.events, "provider")
	}
	return cloneChatResponse(model.response), model.err
}

func (model *workspaceAnalysisPlanModel) CallCount() int {
	model.mu.Lock()
	defer model.mu.Unlock()
	return len(model.calls)
}

func (model *workspaceAnalysisPlanModel) Calls() []ChatRequest {
	model.mu.Lock()
	defer model.mu.Unlock()
	result := make([]ChatRequest, len(model.calls))
	for index := range model.calls {
		result[index] = cloneChatRequest(model.calls[index])
	}
	return result
}

type workspaceAnalysisPlanIDGenerator struct {
	next int
}

func (generator *workspaceAnalysisPlanIDGenerator) New() (foundation.ID, error) {
	generator.next++
	return workspaceAnalysisPlanTestID(100 + generator.next), nil
}

func newWorkspaceAnalysisPlanRunner(
	t *testing.T,
	model ChatModel,
	repository WorkspaceAnalysisRetrievalPlanRepository,
) *WorkspaceAnalysisRetrievalPlanRunner {
	t.Helper()
	runner, err := NewWorkspaceAnalysisRetrievalPlanRunner(WorkspaceAnalysisRetrievalPlanRunnerDependencies{
		Model: model, Catalog: workspaceAnalysisPlanCatalog(t), Repository: repository,
		IDs: &workspaceAnalysisPlanIDGenerator{}, Clock: foundation.FixedClock{Value: workspaceAnalysisPlanNow()},
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func workspaceAnalysisPlanCatalog(t *testing.T) *RuntimeCatalog {
	t.Helper()
	prompt := PromptDefinition{
		Ref:    domain.PromptRef{ID: "workspace-analysis-plan", Version: "v1"},
		System: "produce a bounded identityless retrieval plan", InitialInstruction: "return one strict plan",
		RepairInstruction: "unused", ReducedInstruction: "unused",
	}
	schema := SchemaDefinition{
		Ref:        domain.SchemaRef{ID: domain.WorkspaceAnalysisPlanSchemaID, Version: domain.OutputSchemaVersionV1},
		JSONSchema: []byte(`{"type":"object","additionalProperties":false,"required":["i","r","d","q","s"],"properties":{"i":{"type":"string"},"r":{"type":"array","items":{"type":"string"}},"d":{"type":"string"},"q":{"type":"string"},"s":{"type":"array","items":{"type":"string"}}}}`),
		Decode: func(raw []byte) (json.RawMessage, error) {
			if _, err := domain.DecodeRAGQueryPlanProviderV2(raw, domain.DefaultDecodeLimits()); err != nil {
				return nil, err
			}
			return append(json.RawMessage(nil), raw...), nil
		},
	}
	profile := ModelProfile{
		Ref:   domain.ModelProfileRef{ID: "workspace-analysis-plan", Version: "v1"},
		Model: workspaceAnalysisPlanModelRef(), Timeout: time.Minute, MaxOutputTokens: 1024,
	}
	catalog := NewRuntimeCatalog()
	for _, register := range []func() error{
		func() error { return catalog.RegisterPrompt(prompt) },
		func() error { return catalog.RegisterSchema(schema) },
		func() error { return catalog.RegisterProfile(profile) },
		catalog.Freeze,
	} {
		if err := register(); err != nil {
			t.Fatal(err)
		}
	}
	return catalog
}

func workspaceAnalysisPlanRequest() WorkspaceAnalysisRetrievalPlanRequest {
	return WorkspaceAnalysisRetrievalPlanRequest{
		Identity: WorkspaceAnalysisModelExecutionIdentity{
			WorkspaceID: workspaceAnalysisPlanTestID(1), DefinitionID: workspaceAnalysisPlanTestID(2),
			DefinitionVersion: 1, DefinitionHash: hashHex('a'), WorkflowRunID: workspaceAnalysisPlanTestID(3),
			NodeKey: domain.WorkspaceAnalysisOperationNodeRetrieveEvidence, NodeRunID: workspaceAnalysisPlanTestID(4),
			NodeAttemptID: workspaceAnalysisPlanTestID(5), LeaseOwner: "worker-a", LeaseFence: 1,
		},
		AnalysisRunID: workspaceAnalysisPlanTestID(6), Retrieval: domain.RetrievalRef{IndexVersionID: workspaceAnalysisPlanTestID(7)},
		ProfileRef: domain.ModelProfileRef{ID: "workspace-analysis-plan", Version: "v1"},
		PromptRef:  domain.PromptRef{ID: "workspace-analysis-plan", Version: "v1"},
		Input:      []byte(`{"question":"where is the workspace policy?","history":[]}`),
	}
}

func workspaceAnalysisPlanSuccessfulReplay(
	t *testing.T,
	command AuthorizeWorkspaceAnalysisModelCallCommand,
) (WorkspaceAnalysisModelAuthorizationResult, domain.WorkspaceAnalysisModelResult) {
	t.Helper()
	plan := domain.WorkspaceAnalysisPlanResult{
		ResultType: domain.ResultTypeWorkspaceAnalysisPlan, SchemaID: domain.WorkspaceAnalysisPlanSchemaID,
		SchemaVersion: domain.OutputSchemaVersionV1, ModelRunRef: command.Run.ID,
		Payload: domain.RAGQueryPlanPayload{Intent: "find policy", Rewrites: []string{"where is the workspace policy?"}, SuggestedScopes: []string{}},
	}
	document, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	completedAt := command.Call.StartedAt.Add(time.Second)
	call := command.Call
	call.Status, call.ResponseHash, call.ResponseBytes = domain.ModelCallSucceeded, workspaceAnalysisSHA256(document), int64(len(document))
	call.Usage = domain.TokenUsage{InputTokens: 19, OutputTokens: 6, TotalTokens: 25}
	call.LatencyMillis, call.Version, call.CompletedAt = 1000, 2, &completedAt
	run := command.Run
	run.Status, run.FinalResultType, run.Version = domain.ModelRunSucceeded, domain.ResultTypeWorkspaceAnalysisPlan, 2
	run.UpdatedAt, run.CompletedAt = completedAt, &completedAt
	result := domain.WorkspaceAnalysisModelResult{
		ID: workspaceAnalysisPlanTestID(900), WorkspaceID: command.Identity.WorkspaceID,
		AnalysisRunID: command.OperationKey.AnalysisRunID, OperationID: command.OperationID,
		NodeAttemptID: command.Run.NodeAttemptID, ModelRunID: command.Run.ID, ModelCallID: command.Call.ID,
		OperationKind: domain.WorkspaceAnalysisOperationRetrievalPlan, Schema: command.Run.Schema,
		Document: document, DocumentHash: call.ResponseHash, DocumentBytes: int64(len(document)), CreatedAt: completedAt,
	}
	return WorkspaceAnalysisModelAuthorizationResult{
		Run: run, Call: call, OperationID: command.OperationID, ReservationID: command.ReservationID,
		Disposition: WorkspaceAnalysisModelAuthorizationReuseResult,
	}, result
}

func workspaceAnalysisPlanProviderDocument(t *testing.T, intent string) []byte {
	t.Helper()
	document, err := json.Marshal(domain.RAGQueryPlanProviderResultV2{
		Intent: intent, Rewrites: []string{"workspace policy"}, ClarificationReason: "",
		ClarificationQuestion: "", SuggestedScopes: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func workspaceAnalysisPlanModelRef() domain.ModelRef {
	return domain.ModelRef{
		AdapterName: "openai-compatible", AdapterVersion: "v1", ModelID: "workspace-plan-test", ModelVersion: "2026-08-16",
	}
}

func workspaceAnalysisPlanNow() time.Time {
	return time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
}

func workspaceAnalysisPlanTestID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("9d000000-0000-4000-8000-%012d", value))
}
