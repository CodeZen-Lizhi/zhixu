package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestWorkspaceAnalysisModelAuthorizationRequestRequiresExactSlotAndProfile(t *testing.T) {
	run, call := workspaceAnalysisModelRequestFixture()
	actualRun, actualCall := run, call
	actualRun.ID = workspaceAnalysisModelTestID(20)
	actualRun.NodeAttemptID = workspaceAnalysisModelTestID(21)
	actualRun.Status = domain.ModelRunSucceeded
	actualRun.Version = 2
	actualCall.ID = workspaceAnalysisModelTestID(22)
	actualCall.ModelRunID = actualRun.ID
	actualCall.Status = domain.ModelCallSucceeded
	actualCall.Version = 2
	if !sameWorkspaceAnalysisModelAuthorizationRequest(run, call, actualRun, actualCall) {
		t.Fatal("replacement attempt with the same provider request was rejected")
	}

	tests := []struct {
		name   string
		mutate func(*domain.ModelRun, *domain.ModelCall)
	}{
		{name: "workspace", mutate: func(value *domain.ModelRun, _ *domain.ModelCall) {
			value.WorkspaceID = workspaceAnalysisModelTestID(30)
		}},
		{name: "workflow run", mutate: func(value *domain.ModelRun, _ *domain.ModelCall) {
			value.WorkflowRunID = workspaceAnalysisModelTestID(31)
		}},
		{name: "node run", mutate: func(value *domain.ModelRun, _ *domain.ModelCall) { value.NodeRunID = workspaceAnalysisModelTestID(32) }},
		{name: "model", mutate: func(value *domain.ModelRun, _ *domain.ModelCall) { value.Model.ModelVersion = "model-v2" }},
		{name: "run profile", mutate: func(value *domain.ModelRun, _ *domain.ModelCall) { value.Profile.Version = "profile-v2" }},
		{name: "prompt", mutate: func(value *domain.ModelRun, _ *domain.ModelCall) { value.Prompt.Version = "prompt-v2" }},
		{name: "schema", mutate: func(value *domain.ModelRun, _ *domain.ModelCall) { value.Schema.Version = "v2" }},
		{name: "reduced schema", mutate: func(value *domain.ModelRun, _ *domain.ModelCall) { value.ReducedSchema.Version = "v2" }},
		{name: "retrieval", mutate: func(value *domain.ModelRun, _ *domain.ModelCall) {
			value.Retrieval.IndexVersionID = workspaceAnalysisModelTestID(33)
		}},
		{name: "memory", mutate: func(value *domain.ModelRun, _ *domain.ModelCall) {
			value.MemoryContext.Digest = strings.Repeat("9", 64)
		}},
		{name: "phase", mutate: func(_ *domain.ModelRun, value *domain.ModelCall) { value.Phase = domain.ModelCallReview }},
		{name: "call profile", mutate: func(_ *domain.ModelRun, value *domain.ModelCall) { value.Profile.Version = "profile-v2" }},
		{name: "max output", mutate: func(_ *domain.ModelRun, value *domain.ModelCall) { value.MaxOutputTokens++ }},
		{name: "request hash", mutate: func(_ *domain.ModelRun, value *domain.ModelCall) { value.RequestHash = strings.Repeat("b", 64) }},
		{name: "request bytes", mutate: func(_ *domain.ModelRun, value *domain.ModelCall) { value.RequestBytes++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changedRun, changedCall := actualRun, actualCall
			test.mutate(&changedRun, &changedCall)
			if sameWorkspaceAnalysisModelAuthorizationRequest(run, call, changedRun, changedCall) {
				t.Fatal("request drift was accepted")
			}
		})
	}
}

func TestWorkspaceAnalysisModelAdmissionUsesDBTimeAndExactFrozenBudget(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	run := domain.WorkspaceAnalysisRun{
		DeadlineAt: now.Add(10 * time.Minute),
		Timeouts: domain.WorkspaceAnalysisV1Timeouts{
			PlanModelTimeout: 2 * time.Minute, SynthesisModelTimeout: 5 * time.Minute,
			ReviewModelTimeout: 3 * time.Minute,
		},
		Limits: domain.WorkspaceAnalysisBudgetLimits{Amount: domain.WorkspaceAnalysisBudgetAmount{
			ModelCalls: 3, InputTokens: domain.WorkspaceAnalysisV1MaxRunInputTokens,
			OutputTokens: domain.WorkspaceAnalysisV1MaxRunOutputTokens,
		}},
	}
	locked := workspaceAnalysisModelLocks{analysisRun: run}
	locked.fence.databaseNow = now
	command := application.AuthorizeWorkspaceAnalysisModelCallCommand{
		OperationKey: domain.WorkspaceAnalysisOperationKey{Kind: domain.WorkspaceAnalysisOperationRetrievalPlan},
		Call:         domain.ModelCall{MaxOutputTokens: int(domain.WorkspaceAnalysisV1PlanMaxOutputTokens)},
	}
	if err := validateWorkspaceAnalysisModelAdmission(locked, command); err != nil {
		t.Fatalf("first exact authorization rejected: %v", err)
	}

	command.OperationKey.Kind = domain.WorkspaceAnalysisOperationAnswerSynthesis
	command.Call.MaxOutputTokens = int(domain.WorkspaceAnalysisV1SynthesisMaxOutputTokens)
	if err := validateWorkspaceAnalysisModelAdmission(locked, command); err != nil {
		t.Fatalf("exact synthesis reservation rejected: %v", err)
	}
	command.Call.MaxOutputTokens--
	if err := validateWorkspaceAnalysisModelAdmission(locked, command); err == nil {
		t.Fatal("non-exact synthesis reservation was accepted")
	}

	command.OperationKey.Kind = domain.WorkspaceAnalysisOperationRetrievalPlan
	command.Call.MaxOutputTokens = int(domain.WorkspaceAnalysisV1PlanMaxOutputTokens)
	locked.analysisRun.DeadlineAt = now.Add(run.Timeouts.PlanModelTimeout + domain.WorkspaceAnalysisV1DurableCompletionMargin)
	if err := validateWorkspaceAnalysisModelAdmission(locked, command); err != nil {
		t.Fatalf("exact durable-completion boundary rejected: %v", err)
	}
	locked.analysisRun.DeadlineAt = locked.analysisRun.DeadlineAt.Add(-time.Nanosecond)
	if err := validateWorkspaceAnalysisModelAdmission(locked, command); !workspaceAnalysisPreAuthorizationDeadlineError(err) {
		t.Fatalf("exhausted DB-time deadline err=%v", err)
	}

	command.OperationKey.Kind = "UNSUPPORTED"
	if err := validateWorkspaceAnalysisModelAdmission(locked, command); !workspaceAnalysisModelAuthorizationConflictError(err) {
		t.Fatalf("invalid model timeout was marked as pre-authorization deadline: %v", err)
	}

	command.OperationKey.Kind = domain.WorkspaceAnalysisOperationRetrievalPlan
	locked.analysisRun = run
	locked.analysisRun.DeadlineAt = now
	locked.analysisRun.Reserved.ModelCalls = run.Limits.Amount.ModelCalls
	if err := validateWorkspaceAnalysisModelAdmission(locked, command); !workspaceAnalysisModelAuthorizationConflictError(err) {
		t.Fatalf("exhausted model-call budget err=%v", err)
	}
	locked.analysisRun = run
	locked.analysisRun.DeadlineAt = now
	locked.analysisRun.Settled.InputTokens = run.Limits.Amount.InputTokens - domain.WorkspaceAnalysisV1MaxInputTokensPerModelCall + 1
	if err := validateWorkspaceAnalysisModelAdmission(locked, command); !workspaceAnalysisModelAuthorizationConflictError(err) {
		t.Fatalf("exhausted input-token budget err=%v", err)
	}
}

func workspaceAnalysisPreAuthorizationDeadlineError(err error) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) &&
		classified.Kind == foundation.ErrorNonRetryableFailure &&
		classified.Code == domain.ErrorCodeWorkspaceAnalysisPreAuthorizationDeadline &&
		!classified.Retryable && errors.Is(err, context.DeadlineExceeded)
}

func workspaceAnalysisModelAuthorizationConflictError(err error) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) &&
		classified.Kind == foundation.ErrorVersionConflict &&
		classified.Code == application.ErrorCodeWorkspaceAnalysisModelAuthorizationInvalid &&
		!classified.Retryable && !errors.Is(err, context.DeadlineExceeded)
}

func TestWorkspaceAnalysisModelFailureClosureAcceptsFailureRefusalAndUnknownOnly(t *testing.T) {
	tests := []struct {
		name              string
		callStatus        domain.ModelCallStatus
		runStatus         domain.ModelRunStatus
		resultType        string
		errorCode         string
		reservationStatus domain.WorkspaceAnalysisBudgetReservationStatus
		operationStatus   domain.WorkspaceAnalysisOperationStatus
	}{
		{
			name: "failed", callStatus: domain.ModelCallFailed, runStatus: domain.ModelRunFailed,
			errorCode: "MODEL_PROVIDER_FAILED", reservationStatus: domain.WorkspaceAnalysisBudgetSettled,
			operationStatus: domain.WorkspaceAnalysisOperationFailed,
		},
		{
			name: "refused", callStatus: domain.ModelCallSucceeded, runStatus: domain.ModelRunRefused,
			resultType: domain.ResultTypeRefusal, errorCode: string(domain.WorkspaceAnalysisRunModelRefused),
			reservationStatus: domain.WorkspaceAnalysisBudgetSettled, operationStatus: domain.WorkspaceAnalysisOperationFailed,
		},
		{
			name: "unknown", callStatus: domain.ModelCallUnknown, runStatus: domain.ModelRunUnknown,
			errorCode:         string(domain.WorkspaceAnalysisRunResultUnknown),
			reservationStatus: domain.WorkspaceAnalysisBudgetUnknownCharged, operationStatus: domain.WorkspaceAnalysisOperationUnknown,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			locked, command := workspaceAnalysisModelFailureClosureFixture(test.callStatus, test.runStatus,
				test.resultType, test.errorCode, test.reservationStatus, test.operationStatus)
			if err := validateWorkspaceAnalysisModelStoredClosure(nil, nil, locked); err != nil {
				t.Fatalf("valid closure rejected: %v", err)
			}
			result, err := replayWorkspaceAnalysisModelCallTerminal(locked, command)
			if err != nil || !result.Replayed || result.OperationID != locked.operation.id ||
				result.ReservationID != locked.reservation.ID {
				t.Fatalf("terminal replay=%#v err=%v", result, err)
			}

			drifted := locked
			drifted.reservation = workspaceAnalysisModelReservationCopy(locked.reservation)
			drifted.reservation.Status = domain.WorkspaceAnalysisBudgetReserved
			if err := validateWorkspaceAnalysisModelStoredClosure(nil, nil, drifted); err == nil {
				t.Fatal("reservation drift was accepted")
			}

			drifted = locked
			partialResultKind := domain.WorkspaceAnalysisOperationResultModelCall
			drifted.operation.resultKind = &partialResultKind
			if err := validateWorkspaceAnalysisModelStoredClosure(nil, nil, drifted); err == nil {
				t.Fatal("partial terminal result drift was accepted")
			}
		})
	}
}

func TestWorkspaceAnalysisModelImmutableResultEqualityIsExact(t *testing.T) {
	createdAt := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	result := domain.WorkspaceAnalysisModelResult{
		ID: workspaceAnalysisModelTestID(41), WorkspaceID: workspaceAnalysisModelTestID(1),
		AnalysisRunID: workspaceAnalysisModelTestID(2), OperationID: workspaceAnalysisModelTestID(3),
		NodeAttemptID: workspaceAnalysisModelTestID(4), ModelRunID: workspaceAnalysisModelTestID(5),
		ModelCallID: workspaceAnalysisModelTestID(6), OperationKind: domain.WorkspaceAnalysisOperationRetrievalPlan,
		Schema:   domain.SchemaRef{ID: domain.WorkspaceAnalysisPlanSchemaID, Version: domain.OutputSchemaVersionV1},
		Document: json.RawMessage(`{"result":"exact"}`), DocumentHash: strings.Repeat("a", 64),
		DocumentBytes: 18, CreatedAt: createdAt,
	}
	if !sameWorkspaceAnalysisModelResult(result, result) {
		t.Fatal("identical model result differs")
	}
	driftedResult := result
	driftedResult.Document = json.RawMessage(`{"result":"other"}`)
	if sameWorkspaceAnalysisModelResult(result, driftedResult) {
		t.Fatal("model result document drift was accepted")
	}

	candidate := domain.WorkspaceAnalysisCandidate{
		ID: workspaceAnalysisModelTestID(51), WorkspaceID: workspaceAnalysisModelTestID(1),
		AnalysisRunID: workspaceAnalysisModelTestID(2), AnswerID: workspaceAnalysisModelTestID(7),
		SynthesisOperationID: workspaceAnalysisModelTestID(8), NodeAttemptID: workspaceAnalysisModelTestID(4),
		SynthesisModelRunID: workspaceAnalysisModelTestID(5), SchemaID: domain.WorkspaceAnalysisCandidateSchemaID,
		SchemaVersion: domain.WorkspaceAnalysisCandidateSchemaVersion, Document: json.RawMessage(`{"candidate":"exact"}`),
		DocumentHash: strings.Repeat("b", 64), DocumentBytes: 21, CreatedAt: createdAt,
	}
	if !sameWorkspaceAnalysisCandidate(candidate, candidate) {
		t.Fatal("identical candidate differs")
	}
	driftedCandidate := candidate
	driftedCandidate.AnswerID = workspaceAnalysisModelTestID(9)
	if sameWorkspaceAnalysisCandidate(candidate, driftedCandidate) {
		t.Fatal("candidate authority drift was accepted")
	}
}

func workspaceAnalysisModelRequestFixture() (domain.ModelRun, domain.ModelCall) {
	revision := int64(7)
	run := domain.ModelRun{
		ID: workspaceAnalysisModelTestID(5), WorkspaceID: workspaceAnalysisModelTestID(1),
		WorkflowRunID: workspaceAnalysisModelTestID(2), NodeRunID: workspaceAnalysisModelTestID(3),
		NodeAttemptID: workspaceAnalysisModelTestID(4), ModelSettingsRevision: &revision,
		Model:         domain.ModelRef{AdapterName: "adapter", AdapterVersion: "v1", ModelID: "model", ModelVersion: "model-v1"},
		Profile:       domain.ModelProfileRef{ID: "profile", Version: "profile-v1"},
		Prompt:        domain.PromptRef{ID: "prompt", Version: "prompt-v1"},
		Schema:        domain.SchemaRef{ID: domain.WorkspaceAnalysisPlanSchemaID, Version: domain.OutputSchemaVersionV1},
		ReducedSchema: domain.SchemaRef{ID: domain.WorkspaceAnalysisPlanSchemaID, Version: domain.OutputSchemaVersionV1},
		Retrieval:     domain.RetrievalRef{IndexVersionID: workspaceAnalysisModelTestID(6)},
		Status:        domain.ModelRunRunning, Version: 1,
	}
	call := domain.ModelCall{
		ID: workspaceAnalysisModelTestID(7), ModelRunID: run.ID, CallNo: 1, Phase: domain.ModelCallPlan,
		Model: run.Model, Profile: run.Profile, Prompt: run.Prompt, Schema: run.Schema,
		MaxOutputTokens: int(domain.WorkspaceAnalysisV1PlanMaxOutputTokens), RequestHash: strings.Repeat("a", 64),
		RequestBytes: 2, Status: domain.ModelCallStarted, Version: 1,
	}
	return run, call
}

func workspaceAnalysisModelFailureClosureFixture(
	callStatus domain.ModelCallStatus,
	runStatus domain.ModelRunStatus,
	resultType string,
	errorCode string,
	reservationStatus domain.WorkspaceAnalysisBudgetReservationStatus,
	operationStatus domain.WorkspaceAnalysisOperationStatus,
) (workspaceAnalysisModelLocks, application.FinalizeWorkspaceAnalysisModelCallCommand) {
	run, call := workspaceAnalysisModelRequestFixture()
	at := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	run.CreatedAt, run.UpdatedAt, run.CompletedAt = at.Add(-time.Minute), at, &at
	run.Status, run.FinalResultType, run.FinalErrorCode, run.Version = runStatus, resultType, errorCode, 2
	call.StartedAt, call.CompletedAt = at.Add(-time.Minute), &at
	call.Status, call.ErrorCode, call.Version = callStatus, errorCode, 2
	if callStatus == domain.ModelCallSucceeded {
		call.ErrorCode, call.ResponseHash, call.ResponseBytes = "", strings.Repeat("c", 64), 16
		call.Usage = domain.TokenUsage{InputTokens: 8, OutputTokens: 4, TotalTokens: 12}
	}
	reservationID := workspaceAnalysisModelTestID(11)
	reservation := domain.WorkspaceAnalysisBudgetReservation{
		ID: reservationID, ModelCallID: &call.ID, Status: reservationStatus,
		Reserved: domain.WorkspaceAnalysisBudgetAmount{
			ModelCalls: 1, InputTokens: domain.WorkspaceAnalysisV1MaxInputTokensPerModelCall,
			OutputTokens: domain.WorkspaceAnalysisV1PlanMaxOutputTokens,
		},
		Settled: domain.WorkspaceAnalysisBudgetAmount{
			ModelCalls: 1, InputTokens: call.Usage.InputTokens, OutputTokens: call.Usage.OutputTokens,
		},
		CreatedAt: call.StartedAt, SettledAt: &at,
	}
	if reservationStatus == domain.WorkspaceAnalysisBudgetUnknownCharged {
		reservation.Settled = reservation.Reserved
	}
	operationError := errorCode
	locked := workspaceAnalysisModelLocks{
		operation: workspaceAnalysisModelOperationRecord{
			id: workspaceAnalysisModelTestID(10), status: operationStatus, errorCode: &operationError,
		},
		reservation: &reservation, modelRun: &run, modelCall: &call,
	}
	command := application.FinalizeWorkspaceAnalysisModelCallCommand{
		OperationID: locked.operation.id, ReservationID: reservationID,
		ExpectedCallVersion: 1, ExpectedRunVersion: 1, Call: call, Run: run,
	}
	return locked, command
}

func workspaceAnalysisModelReservationCopy(value *domain.WorkspaceAnalysisBudgetReservation) *domain.WorkspaceAnalysisBudgetReservation {
	copy := *value
	return &copy
}

func workspaceAnalysisModelTestID(seed int) foundation.ID {
	return foundation.ID(fmt.Sprintf("d0000000-0000-4000-8000-%012d", seed))
}
