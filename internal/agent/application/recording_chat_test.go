package application

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestRecordingChatModelContinuesCallNumbersForReviewWithoutQueryingMax(t *testing.T) {
	modelRef := domain.ModelRef{AdapterName: "test-adapter", AdapterVersion: "v1", ModelID: "test-model", ModelVersion: "v1"}
	response := ChatResponse{Model: modelRef, Content: []byte(`{"ok":true}`), Usage: domain.TokenUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}
	inner := NewDeterministicChatModel(
		DeterministicChatStep{Response: response}, DeterministicChatStep{Response: response},
		DeterministicChatStep{Response: response}, DeterministicChatStep{Response: response},
	)
	repository := &recordingRepository{}
	ids := &recordingIDs{next: 10}
	dependencies := RecordingChatModelDependencies{
		Model: inner, Repository: repository,
		WorkspaceID: "81000000-0000-4000-8000-000000000001", ModelRunID: "81000000-0000-4000-8000-000000000002",
		IDs: ids, Clock: foundation.FixedClock{Value: time.Date(2026, 7, 19, 1, 0, 0, 0, time.UTC)},
	}
	generation, err := NewRecordingChatModel(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	expected := make([]ChatRequest, 0, 4)
	for _, phase := range []domain.ModelCallPhase{domain.ModelCallInitial, domain.ModelCallRepair, domain.ModelCallReduced} {
		request := recordingRequest(modelRef, phase)
		expected = append(expected, request)
		if _, err := generation.Chat(context.Background(), request); err != nil {
			t.Fatalf("generation phase=%s err=%v", phase, err)
		}
	}
	dependencies.StartingCallNo = 4
	reviewer, err := NewRecordingChatModel(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	reviewRequest := recordingRequest(modelRef, domain.ModelCallReview)
	reviewRequest.ProfileRef = domain.ModelProfileRef{ID: "review-profile", Version: "v2"}
	reviewRequest.PromptRef = domain.PromptRef{ID: "faithfulness-review", Version: "v3"}
	reviewRequest.SchemaRef = domain.SchemaRef{ID: domain.FaithfulnessReviewSchemaID, Version: domain.OutputSchemaVersionV1}
	reviewRequest.MaxOutputTokens = 8
	expected = append(expected, reviewRequest)
	if _, err := reviewer.Chat(context.Background(), reviewRequest); err != nil {
		t.Fatal(err)
	}
	if repository.getCalls != 0 || len(repository.calls) != 4 {
		t.Fatalf("getCalls=%d calls=%d", repository.getCalls, len(repository.calls))
	}
	for index, call := range repository.calls {
		if call.CallNo != index+1 || call.Status != domain.ModelCallSucceeded || call.RequestHash == "" || call.ResponseHash == "" {
			t.Fatalf("call[%d]=%+v", index, call)
		}
		request := expected[index]
		if call.Model != request.Model || call.Profile != request.ProfileRef || call.Prompt != request.PromptRef ||
			call.Schema != request.SchemaRef || call.MaxOutputTokens != request.MaxOutputTokens {
			t.Fatalf("call[%d] runtime refs=%+v request=%+v", index, call, request)
		}
	}
}

func TestRecordingChatModelPersistsPlanBeforeGenerationWithStableCallNumbers(t *testing.T) {
	modelRef := domain.ModelRef{AdapterName: "test-adapter", AdapterVersion: "v1", ModelID: "test-model", ModelVersion: "v1"}
	response := ChatResponse{Model: modelRef, Content: []byte(`{"ok":true}`), Usage: domain.TokenUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}
	repository := &recordingRepository{}
	model, err := NewRecordingChatModel(RecordingChatModelDependencies{
		Model: NewDeterministicChatModel(
			DeterministicChatStep{Response: response},
			DeterministicChatStep{Response: response},
		),
		Repository:  repository,
		WorkspaceID: "81000000-0000-4000-8000-000000000001",
		ModelRunID:  "81000000-0000-4000-8000-000000000002",
		IDs:         &recordingIDs{next: 30},
		Clock:       foundation.FixedClock{Value: time.Date(2026, 7, 19, 1, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatal(err)
	}

	plan := recordingRequest(modelRef, domain.ModelCallPlan)
	plan.PromptRef = domain.PromptRef{ID: "rag-query-plan", Version: "v1"}
	plan.SchemaRef = domain.SchemaRef{ID: domain.RAGQueryPlanSchemaID, Version: domain.OutputSchemaVersionV1}
	initial := recordingRequest(modelRef, domain.ModelCallInitial)
	initial.PromptRef = domain.PromptRef{ID: "rag-answer", Version: "v2"}
	initial.SchemaRef = domain.SchemaRef{ID: domain.RAGAnswerSchemaID, Version: domain.OutputSchemaVersionV2}
	for _, request := range []ChatRequest{plan, initial} {
		if _, err := model.Chat(context.Background(), request); err != nil {
			t.Fatalf("phase=%s err=%v", request.Phase, err)
		}
	}

	if len(repository.calls) != 2 {
		t.Fatalf("calls=%d", len(repository.calls))
	}
	for index, phase := range []domain.ModelCallPhase{domain.ModelCallPlan, domain.ModelCallInitial} {
		call := repository.calls[index]
		if call.CallNo != index+1 || call.Phase != phase || call.Status != domain.ModelCallSucceeded {
			t.Fatalf("call[%d]=%+v", index, call)
		}
	}
}

func TestRecordingChatModelPersistsAgentLoopAndAnswerWithStableCallNumbers(t *testing.T) {
	modelRef := domain.ModelRef{AdapterName: "test-adapter", AdapterVersion: "v1", ModelID: "test-model", ModelVersion: "v1"}
	response := ChatResponse{Model: modelRef, Content: []byte(`{"ok":true}`), Usage: domain.TokenUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}
	phases := []domain.ModelCallPhase{
		domain.ModelCallPlan,
		domain.ModelCallAgent,
		domain.ModelCallAgent,
		domain.ModelCallAnswer,
		domain.ModelCallInitial,
	}
	steps := make([]DeterministicChatStep, len(phases))
	for index := range steps {
		steps[index] = DeterministicChatStep{Response: response}
	}
	repository := &recordingRepository{}
	model, err := NewRecordingChatModel(RecordingChatModelDependencies{
		Model:       NewDeterministicChatModel(steps...),
		Repository:  repository,
		WorkspaceID: "81000000-0000-4000-8000-000000000001",
		ModelRunID:  "81000000-0000-4000-8000-000000000002",
		IDs:         &recordingIDs{next: 40},
		Clock:       foundation.FixedClock{Value: time.Date(2026, 8, 8, 1, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, phase := range phases {
		if _, err := model.Chat(context.Background(), recordingRequest(modelRef, phase)); err != nil {
			t.Fatalf("phase=%s err=%v", phase, err)
		}
	}

	if len(repository.calls) != len(phases) {
		t.Fatalf("calls=%d want=%d", len(repository.calls), len(phases))
	}
	for index, phase := range phases {
		call := repository.calls[index]
		if call.CallNo != index+1 || call.Phase != phase || call.Status != domain.ModelCallSucceeded {
			t.Fatalf("call[%d]=%+v", index, call)
		}
	}
}

func TestRecordingChatModelSharesLedgerCallNumbersAcrossPlanMetadataAndReview(t *testing.T) {
	modelRef := domain.ModelRef{AdapterName: "test-adapter", AdapterVersion: "v1", ModelID: "test-model", ModelVersion: "v1"}
	workspaceID := foundation.ID("81000000-0000-4000-8000-000000000001")
	modelRunID := foundation.ID("81000000-0000-4000-8000-000000000002")
	config := validRunBudgetLedgerConfig()
	config.NodeAttemptID = "81000000-0000-4000-8000-000000000003"
	config.ModelRunID = modelRunID
	ledger, err := NewRunBudgetLedger(config)
	if err != nil {
		t.Fatal(err)
	}
	repository := &recordingRepository{}
	ids := &recordingIDs{next: 50}
	recorder, err := NewModelCallRecorder(ModelCallRecorderDependencies{
		Repository: repository, WorkspaceID: workspaceID, ModelRunID: modelRunID, IDs: ids,
		Clock: foundation.FixedClock{Value: time.Date(2026, 8, 8, 1, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := ChatResponse{Model: modelRef, Content: []byte(`{"ok":true}`), Usage: domain.TokenUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}
	newModel := func() *RecordingChatModel {
		t.Helper()
		model, newErr := NewRecordingChatModel(RecordingChatModelDependencies{
			Model:       NewDeterministicChatModel(DeterministicChatStep{Response: response}),
			WorkspaceID: workspaceID, ModelRunID: modelRunID, Recorder: recorder, BudgetLedger: ledger, MaxInputTokens: 10,
		})
		if newErr != nil {
			t.Fatal(newErr)
		}
		return model
	}
	for _, phase := range []domain.ModelCallPhase{domain.ModelCallPlan, domain.ModelCallInitial, domain.ModelCallReview} {
		request := recordingRequest(modelRef, phase)
		request.MaxOutputTokens = 10
		if _, err := newModel().Chat(context.Background(), request); err != nil {
			t.Fatalf("phase=%s err=%v", phase, err)
		}
	}
	if len(repository.calls) != 3 {
		t.Fatalf("calls=%d", len(repository.calls))
	}
	for index, phase := range []domain.ModelCallPhase{domain.ModelCallPlan, domain.ModelCallInitial, domain.ModelCallReview} {
		call := repository.calls[index]
		if call.CallNo != index+1 || call.Phase != phase || call.Status != domain.ModelCallSucceeded {
			t.Fatalf("call[%d]=%+v", index, call)
		}
	}
	snapshot := ledger.Snapshot()
	if snapshot.ModelCalls != 3 || snapshot.InputTokens != 3 || snapshot.OutputTokens != 3 || snapshot.InFlightInput != 0 || snapshot.InFlightOutput != 0 {
		t.Fatalf("ledger=%+v", snapshot)
	}
}

func TestRecordingChatModelSettlesReasoningUsageWithoutFailingSucceededCall(t *testing.T) {
	modelRef := domain.ModelRef{AdapterName: "test-adapter", AdapterVersion: "v1", ModelID: "test-model", ModelVersion: "v1"}
	workspaceID := foundation.ID("81000000-0000-4000-8000-000000000001")
	modelRunID := foundation.ID("81000000-0000-4000-8000-000000000002")
	config := validRunBudgetLedgerConfig()
	config.NodeAttemptID = "81000000-0000-4000-8000-000000000003"
	config.ModelRunID = modelRunID
	config.MaxInputTokens = 10_000
	config.MaxOutputTokens = 10_000
	ledger, err := NewRunBudgetLedger(config)
	if err != nil {
		t.Fatal(err)
	}
	repository := &recordingRepository{}
	recorder, err := NewModelCallRecorder(ModelCallRecorderDependencies{
		Repository: repository, WorkspaceID: workspaceID, ModelRunID: modelRunID, IDs: &recordingIDs{next: 58},
		Clock: foundation.FixedClock{Value: time.Date(2026, 8, 8, 1, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatal(err)
	}
	usage := domain.TokenUsage{InputTokens: 391, OutputTokens: 1_276, TotalTokens: 1_667}
	model, err := NewRecordingChatModel(RecordingChatModelDependencies{
		Model: NewDeterministicChatModel(DeterministicChatStep{Response: ChatResponse{
			Model: modelRef, Content: []byte(`{"ok":true}`), Usage: usage,
		}}),
		WorkspaceID: workspaceID, ModelRunID: modelRunID, Recorder: recorder, BudgetLedger: ledger, MaxInputTokens: 512,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := recordingRequest(modelRef, domain.ModelCallPlan)
	request.MaxOutputTokens = 512
	if _, err := model.Chat(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(repository.calls) != 1 || repository.calls[0].Status != domain.ModelCallSucceeded || repository.calls[0].Usage != usage {
		t.Fatalf("calls = %+v", repository.calls)
	}
	snapshot := ledger.Snapshot()
	if snapshot.InputTokens != usage.InputTokens || snapshot.OutputTokens != usage.OutputTokens ||
		snapshot.InFlightInput != 0 || snapshot.InFlightOutput != 0 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestRecordingChatModelRejectsLedgerForAnotherModelRun(t *testing.T) {
	workspaceID := foundation.ID("81000000-0000-4000-8000-000000000001")
	modelRunID := foundation.ID("81000000-0000-4000-8000-000000000002")
	ledger := newTestRunBudgetLedger(t, RunBudgetLedgerConfig{})
	recorder, err := NewModelCallRecorder(ModelCallRecorderDependencies{
		Repository: &recordingRepository{}, WorkspaceID: workspaceID, ModelRunID: modelRunID, IDs: &recordingIDs{next: 60},
		Clock: foundation.FixedClock{Value: time.Date(2026, 8, 8, 1, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRecordingChatModel(RecordingChatModelDependencies{
		Model: NewDeterministicChatModel(), WorkspaceID: workspaceID, ModelRunID: modelRunID,
		Recorder: recorder, BudgetLedger: ledger, MaxInputTokens: 10,
	}); errorCode(err) != ErrorCodeModelCallPersistenceUnknown {
		t.Fatalf("err=%v code=%q", err, errorCode(err))
	}
}

func TestRecordingChatModelRejectsUndersizedDownstreamInputReservation(t *testing.T) {
	modelRef := domain.ModelRef{AdapterName: "test-adapter", AdapterVersion: "v1", ModelID: "test-model", ModelVersion: "v1"}
	workspaceID := foundation.ID("81000000-0000-4000-8000-000000000001")
	modelRunID := foundation.ID("81000000-0000-4000-8000-000000000002")
	config := validRunBudgetLedgerConfig()
	config.NodeAttemptID = "81000000-0000-4000-8000-000000000003"
	config.ModelRunID = modelRunID
	config.DownstreamReservations[RunBudgetPhaseInitial] = RunBudgetReservation{
		ModelCalls: 1, ReservedInputTokens: 9, ReservedOutputTokens: 10,
	}
	ledger, err := NewRunBudgetLedger(config)
	if err != nil {
		t.Fatal(err)
	}
	repository := &recordingRepository{}
	recorder, err := NewModelCallRecorder(ModelCallRecorderDependencies{
		Repository: repository, WorkspaceID: workspaceID, ModelRunID: modelRunID, IDs: &recordingIDs{next: 65},
		Clock: foundation.FixedClock{Value: time.Date(2026, 8, 8, 1, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatal(err)
	}
	model, err := NewRecordingChatModel(RecordingChatModelDependencies{
		Model: NewDeterministicChatModel(), WorkspaceID: workspaceID, ModelRunID: modelRunID,
		Recorder: recorder, BudgetLedger: ledger, MaxInputTokens: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := recordingRequest(modelRef, domain.ModelCallInitial)
	request.MaxOutputTokens = 10
	if _, err := model.Chat(context.Background(), request); errorCode(err) != ErrorCodeRunBudgetLedgerSettlement {
		t.Fatalf("Chat() error = %v, code = %q", err, errorCode(err))
	}
	if len(repository.calls) != 0 {
		t.Fatalf("provider call was recorded despite an undersized reservation: %+v", repository.calls)
	}
}

func TestRecordingChatModelChargesFullReservationWhenProviderUsageIsMissing(t *testing.T) {
	modelRef := domain.ModelRef{AdapterName: "test-adapter", AdapterVersion: "v1", ModelID: "test-model", ModelVersion: "v1"}
	workspaceID := foundation.ID("81000000-0000-4000-8000-000000000001")
	modelRunID := foundation.ID("81000000-0000-4000-8000-000000000002")
	config := validRunBudgetLedgerConfig()
	config.NodeAttemptID = "81000000-0000-4000-8000-000000000003"
	config.ModelRunID = modelRunID
	ledger, err := NewRunBudgetLedger(config)
	if err != nil {
		t.Fatal(err)
	}
	repository := &recordingRepository{}
	recorder, err := NewModelCallRecorder(ModelCallRecorderDependencies{
		Repository: repository, WorkspaceID: workspaceID, ModelRunID: modelRunID, IDs: &recordingIDs{next: 70},
		Clock: foundation.FixedClock{Value: time.Date(2026, 8, 8, 1, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatal(err)
	}
	providerErr := errors.New("provider failed without usage")
	model, err := NewRecordingChatModel(RecordingChatModelDependencies{
		Model:       NewDeterministicChatModel(DeterministicChatStep{Err: providerErr}),
		WorkspaceID: workspaceID, ModelRunID: modelRunID, Recorder: recorder, BudgetLedger: ledger, MaxInputTokens: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := recordingRequest(modelRef, domain.ModelCallPlan)
	request.MaxOutputTokens = 10
	if _, err := model.Chat(context.Background(), request); !errors.Is(err, providerErr) {
		t.Fatalf("Chat() error = %v", err)
	}
	snapshot := ledger.Snapshot()
	if snapshot.ModelCalls != 1 || snapshot.InputTokens != 10 || snapshot.OutputTokens != 10 ||
		snapshot.InFlightInput != 0 || snapshot.InFlightOutput != 0 {
		t.Fatalf("missing usage did not charge the full authorization: %+v", snapshot)
	}
	if len(repository.calls) != 1 || repository.calls[0].Status != domain.ModelCallFailed {
		t.Fatalf("recorded calls = %+v", repository.calls)
	}
}

func recordingRequest(model domain.ModelRef, phase domain.ModelCallPhase) ChatRequest {
	return ChatRequest{
		Phase:      phase,
		ProfileRef: domain.ModelProfileRef{ID: "profile", Version: "v1"},
		PromptRef:  domain.PromptRef{ID: "prompt", Version: "v1"},
		SchemaRef:  domain.SchemaRef{ID: "schema", Version: "v1"},
		Model:      model, Messages: []ChatMessage{{Role: MessageRoleSystem, Content: "system"}},
		OutputSchema: []byte(`{"type":"object"}`), MaxOutputTokens: 16,
	}
}

type recordingRepository struct {
	calls    []domain.ModelCall
	getCalls int
}

func (repository *recordingRepository) CreateModelRun(context.Context, domain.ModelRun) (domain.ModelRun, bool, error) {
	return domain.ModelRun{}, false, errors.New("unused")
}

func (repository *recordingRepository) GetModelRun(context.Context, foundation.ID, foundation.ID) (ModelRunRecord, error) {
	repository.getCalls++
	return ModelRunRecord{}, errors.New("unused")
}

func (repository *recordingRepository) StartModelCall(_ context.Context, _ foundation.ID, call domain.ModelCall) (domain.ModelCall, bool, error) {
	if err := domain.ValidateModelCall(call); err != nil {
		return domain.ModelCall{}, false, err
	}
	repository.calls = append(repository.calls, call)
	return call, false, nil
}

func (repository *recordingRepository) CompleteModelCall(_ context.Context, command CompleteModelCallCommand) (domain.ModelCall, bool, error) {
	if err := domain.ValidateModelCall(command.Call); err != nil {
		return domain.ModelCall{}, false, err
	}
	repository.calls[command.Call.CallNo-1] = command.Call
	return command.Call, false, nil
}

func (repository *recordingRepository) FinalizeModelRun(context.Context, FinalizeModelRunCommand) (domain.ModelRun, bool, error) {
	return domain.ModelRun{}, false, errors.New("unused")
}

func (repository *recordingRepository) MarkStaleModelCallsUnknown(context.Context, UnknownRecoveryQuery) ([]domain.ModelCall, error) {
	return nil, errors.New("unused")
}

func (repository *recordingRepository) MarkStaleModelRunsUnknown(context.Context, UnknownRecoveryQuery) ([]domain.ModelRun, error) {
	return nil, errors.New("unused")
}

type recordingIDs struct{ next int }

func (ids *recordingIDs) New() (foundation.ID, error) {
	value := foundation.ID(fmt.Sprintf("81000000-0000-4000-8000-%012d", ids.next))
	ids.next++
	return foundation.ParseID(string(value))
}
