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
