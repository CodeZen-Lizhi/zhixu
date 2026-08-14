package eino

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

type schedulerContractOutput struct {
	Value string `json:"value"`
}

type schedulerContractOutcome struct {
	result agentapplication.StructuredRunResult
	err    error
}

func TestStructuredPhaseSchedulerPreservesStructuredContract(t *testing.T) {
	tests := []struct {
		name      string
		steps     []agentapplication.DeterministicChatStep
		wantCall  int
		wantPhase domain.ModelCallPhase
		wantOut   string
		wantUsage domain.TokenUsage
	}{
		{
			name:     "initial success",
			steps:    []agentapplication.DeterministicChatStep{{Response: schedulerResponse(testSchedulerModel(), " \n{\"value\":\"initial\"}\t", 2, 3)}},
			wantCall: 1, wantPhase: domain.ModelCallInitial, wantOut: " \n{\"value\":\"initial\"}\t",
			wantUsage: domain.TokenUsage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5},
		},
		{
			name: "repair success",
			steps: []agentapplication.DeterministicChatStep{
				{Response: schedulerResponse(testSchedulerModel(), `{}`, 1, 1)},
				{Response: schedulerResponse(testSchedulerModel(), `{"value":"repair"}`, 2, 2)},
			},
			wantCall: 2, wantPhase: domain.ModelCallRepair, wantOut: `{"value":"repair"}`,
			wantUsage: domain.TokenUsage{InputTokens: 3, OutputTokens: 3, TotalTokens: 6},
		},
		{
			name: "reduced success",
			steps: []agentapplication.DeterministicChatStep{
				{Response: schedulerResponse(testSchedulerModel(), `{}`, 1, 1)},
				{Response: schedulerResponse(testSchedulerModel(), `{}`, 2, 1)},
				{Response: schedulerResponse(testSchedulerModel(), `{"value":"reduced"}`, 3, 2)},
			},
			wantCall: 3, wantPhase: domain.ModelCallReduced, wantOut: `{"value":"reduced"}`,
			wantUsage: domain.TokenUsage{InputTokens: 6, OutputTokens: 4, TotalTokens: 10},
		},
		{
			name: "validation exhaustion",
			steps: []agentapplication.DeterministicChatStep{
				{Response: schedulerResponse(testSchedulerModel(), `not-json`, 1, 1)},
				{Response: schedulerResponse(testSchedulerModel(), `still-not-json`, 1, 1)},
				{Response: schedulerResponse(testSchedulerModel(), `{}`, 1, 1)},
			},
			wantCall: 3, wantUsage: domain.TokenUsage{InputTokens: 3, OutputTokens: 3, TotalTokens: 6},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outcome := executeSchedulerContract(t, context.Background(), agentapplication.DefaultRunBudget(), test.steps...)
			if outcome.result.CallCount != test.wantCall {
				t.Fatalf("call count=%d want=%d", outcome.result.CallCount, test.wantCall)
			}
			if outcome.result.Usage != test.wantUsage {
				t.Fatalf("usage=%+v want=%+v", outcome.result.Usage, test.wantUsage)
			}
			wantResponseBytes := int64(0)
			for index := 0; index < test.wantCall; index++ {
				wantResponseBytes += int64(len(test.steps[index].Response.Content))
			}
			if outcome.result.ResponseBytes != wantResponseBytes || outcome.result.RequestBytes <= 0 {
				t.Fatalf("bytes=(%d,%d) want response=%d", outcome.result.RequestBytes, outcome.result.ResponseBytes, wantResponseBytes)
			}
			if test.wantPhase != "" && outcome.result.Phase != test.wantPhase {
				t.Fatalf("phase=%q want=%q", outcome.result.Phase, test.wantPhase)
			}
			if test.wantOut != "" && string(outcome.result.Output) != test.wantOut {
				t.Fatalf("output=%q want=%q", outcome.result.Output, test.wantOut)
			}
			if test.wantPhase == "" && errorCode(outcome.err) != domain.ErrorCodeValidationExhausted {
				t.Fatalf("exhaustion code=%q err=%v", errorCode(outcome.err), outcome.err)
			}
		})
	}
}

func TestStructuredPhaseSchedulerMapsGraphFailuresToStableProjectErrors(t *testing.T) {
	t.Run("invoke", func(t *testing.T) {
		graphErr := errors.New("graph invocation failed")
		scheduler := &StructuredPhaseScheduler{invoke: func(context.Context, *phaseGraphState) (*phaseGraphState, error) {
			return nil, graphErr
		}}
		model := agentapplication.NewDeterministicChatModel()
		runner := newRunnerWithContractScheduler(t, model, scheduler)
		result, err := runner.Run(context.Background(), testSchedulerRequest())
		if errorCode(err) != graphInvokeCode || errorKind(err) != foundation.ErrorNonRetryableFailure || !errors.Is(err, graphErr) || result.CallCount != 0 || model.CallCount() != 0 {
			t.Fatalf("error=%v code=%q kind=%q result=%+v calls=%d", err, errorCode(err), errorKind(err), result, model.CallCount())
		}
	})

	t.Run("output state", func(t *testing.T) {
		scheduler := &StructuredPhaseScheduler{invoke: func(context.Context, *phaseGraphState) (*phaseGraphState, error) {
			return &phaseGraphState{}, nil
		}}
		model := agentapplication.NewDeterministicChatModel()
		runner := newRunnerWithContractScheduler(t, model, scheduler)
		_, err := runner.Run(context.Background(), testSchedulerRequest())
		if errorCode(err) != graphOutputCode || errorKind(err) != foundation.ErrorConsistencyViolation || model.CallCount() != 0 {
			t.Fatalf("error=%v code=%q kind=%q calls=%d", err, errorCode(err), errorKind(err), model.CallCount())
		}
	})
}

func TestStructuredPhaseSchedulerIsReusableAcrossConcurrentRuns(t *testing.T) {
	scheduler, err := NewStructuredPhaseScheduler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	model := &concurrentSchedulerModel{}
	runner := newRunnerWithContractScheduler(t, model, scheduler)

	const runs = 32
	errorsCh := make(chan error, runs)
	var wait sync.WaitGroup
	for index := 0; index < runs; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, runErr := runner.Run(context.Background(), testSchedulerRequest())
			if runErr == nil && (result.CallCount != 1 || result.Phase != domain.ModelCallInitial || string(result.Output) != `{"value":"concurrent"}`) {
				runErr = fmt.Errorf("unexpected result: %+v", result)
			}
			errorsCh <- runErr
		}()
	}
	wait.Wait()
	close(errorsCh)
	for runErr := range errorsCh {
		if runErr != nil {
			t.Fatal(runErr)
		}
	}
	if model.CallCount() != runs {
		t.Fatalf("model calls=%d want=%d", model.CallCount(), runs)
	}
}

func TestStructuredPhaseSchedulerPreservesProviderAndContextErrors(t *testing.T) {
	providerErr := foundation.NewError(foundation.ErrorRetryableFailure, "CHAT_PROVIDER_RATE_LIMITED", true, errors.New("provider unavailable"))
	provider := executeSchedulerContract(t, context.Background(), agentapplication.DefaultRunBudget(), agentapplication.DeterministicChatStep{Err: providerErr})
	if provider.err != providerErr || !errors.Is(provider.err, providerErr) {
		t.Fatalf("provider error=%#v want exact foundation error", provider.err)
	}
	if provider.result.CallCount != 1 {
		t.Fatalf("provider call count=%d want=1", provider.result.CallCount)
	}

	ctx, cancel := context.WithCancel(context.Background())
	model := agentapplication.NewDeterministicChatModel(agentapplication.DeterministicChatStep{WaitForCancel: true})
	runner := newContractRunner(t, model, agentapplication.DefaultRunBudget())
	done := make(chan error, 1)
	go func() { _, err := runner.Run(ctx, testSchedulerRequest()); done <- err }()
	<-model.Started()
	cancel()
	cancelErr := <-done
	if errorCode(cancelErr) != agentapplication.ErrorCodeOperationCancelled || !errors.Is(cancelErr, context.Canceled) {
		t.Fatalf("cancel error=%v", cancelErr)
	}

	budget := agentapplication.DefaultRunBudget()
	budget.Timeout = 20 * time.Millisecond
	deadline := executeSchedulerContract(t, context.Background(), budget, agentapplication.DeterministicChatStep{WaitForCancel: true})
	if errorCode(deadline.err) != agentapplication.ErrorCodeOperationDeadline || !errors.Is(deadline.err, context.DeadlineExceeded) {
		t.Fatalf("deadline error=%v", deadline.err)
	}
}

func TestStructuredPhaseSchedulerPreservesAllBudgets(t *testing.T) {
	tests := []struct {
		name   string
		budget agentapplication.RunBudget
		steps  []agentapplication.DeterministicChatStep
		code   string
		calls  int
	}{
		{
			name: "request bytes", budget: func() agentapplication.RunBudget {
				b := agentapplication.DefaultRunBudget()
				b.MaxRequestBytes = 1
				return b
			}(),
			steps: []agentapplication.DeterministicChatStep{{Response: schedulerResponse(testSchedulerModel(), `{"value":"ok"}`, 1, 1)}}, code: "AGENT_REQUEST_BYTE_BUDGET_EXHAUSTED", calls: 0,
		},
		{
			name: "response bytes", budget: func() agentapplication.RunBudget {
				b := agentapplication.DefaultRunBudget()
				b.MaxResponseBytes = 1
				return b
			}(),
			steps: []agentapplication.DeterministicChatStep{{Response: schedulerResponse(testSchedulerModel(), `{"value":"ok"}`, 1, 1)}}, code: "AGENT_RESPONSE_BYTE_BUDGET_EXHAUSTED", calls: 1,
		},
		{
			name: "tokens", budget: func() agentapplication.RunBudget {
				b := agentapplication.DefaultRunBudget()
				b.MaxTotalTokens = 2
				return b
			}(),
			steps: []agentapplication.DeterministicChatStep{{Response: schedulerResponse(testSchedulerModel(), `{"value":"ok"}`, 2, 1)}}, code: "AGENT_TOKEN_BUDGET_EXHAUSTED", calls: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outcome := executeSchedulerContract(t, context.Background(), test.budget, test.steps...)
			if errorCode(outcome.err) != test.code || outcome.result.CallCount != test.calls {
				t.Fatalf("outcome=(%q,%d), want=(%q,%d)", errorCode(outcome.err), outcome.result.CallCount, test.code, test.calls)
			}
		})
	}
}

func TestStructuredPhaseSchedulerDoesNotBypassRecordingChatModel(t *testing.T) {
	inner := agentapplication.NewDeterministicChatModel(
		agentapplication.DeterministicChatStep{Response: schedulerResponse(testSchedulerModel(), `{}`, 1, 1)},
		agentapplication.DeterministicChatStep{Response: schedulerResponse(testSchedulerModel(), `{"value":"recorded"}`, 2, 2)},
	)
	repository := &schedulerRecordingRepository{}
	recorded, err := agentapplication.NewRecordingChatModel(agentapplication.RecordingChatModelDependencies{
		Model: inner, Repository: repository,
		WorkspaceID: "81000000-0000-4000-8000-000000000001", ModelRunID: "81000000-0000-4000-8000-000000000002",
		IDs: &schedulerRecordingIDs{next: 10}, Clock: foundation.FixedClock{Value: time.Date(2026, 8, 6, 1, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := newContractRunner(t, recorded, agentapplication.DefaultRunBudget())
	result, err := runner.Run(context.Background(), testSchedulerRequest())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.CallCount != 2 || inner.CallCount() != 2 || len(repository.calls) != 2 {
		t.Fatalf("result calls=%d inner calls=%d persisted calls=%d", result.CallCount, inner.CallCount(), len(repository.calls))
	}
	for index, phase := range []domain.ModelCallPhase{domain.ModelCallInitial, domain.ModelCallRepair} {
		call := repository.calls[index]
		if call.CallNo != index+1 || call.Phase != phase || call.Status != domain.ModelCallSucceeded || call.ResponseBytes <= 0 || call.Usage.TotalTokens == 0 {
			t.Fatalf("persisted call[%d]=%+v", index, call)
		}
	}
}

func TestStructuredPhaseSchedulerPreservesModelCallPersistenceUnknown(t *testing.T) {
	inner := agentapplication.NewDeterministicChatModel(agentapplication.DeterministicChatStep{
		Response: schedulerResponse(testSchedulerModel(), `{"value":"unknown"}`, 1, 1),
	})
	repository := &schedulerRecordingRepository{completeErr: errors.New("commit response lost")}
	recorded, err := agentapplication.NewRecordingChatModel(agentapplication.RecordingChatModelDependencies{
		Model: inner, Repository: repository,
		WorkspaceID: "81000000-0000-4000-8000-000000000001", ModelRunID: "81000000-0000-4000-8000-000000000002",
		IDs: &schedulerRecordingIDs{next: 20}, Clock: foundation.FixedClock{Value: time.Date(2026, 8, 6, 1, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := newContractRunner(t, recorded, agentapplication.DefaultRunBudget())
	result, err := runner.Run(context.Background(), testSchedulerRequest())
	if errorCode(err) != agentapplication.ErrorCodeModelCallPersistenceUnknown ||
		errorKind(err) != foundation.ErrorManualRecoveryRequired || result.CallCount != 1 || inner.CallCount() != 1 || len(repository.calls) != 1 {
		t.Fatalf("error=%v result=%+v provider_calls=%d persisted_calls=%d", err, result, inner.CallCount(), len(repository.calls))
	}
}

func executeSchedulerContract(t *testing.T, ctx context.Context, budget agentapplication.RunBudget, steps ...agentapplication.DeterministicChatStep) schedulerContractOutcome {
	t.Helper()
	model := agentapplication.NewDeterministicChatModel(steps...)
	runner := newContractRunner(t, model, budget)
	result, err := runner.Run(ctx, testSchedulerRequest())
	return schedulerContractOutcome{result: result, err: err}
}

func newContractRunner(t *testing.T, model agentapplication.ChatModel, budget agentapplication.RunBudget) *agentapplication.StructuredRunner {
	t.Helper()
	catalog := testSchedulerCatalog(t)
	scheduler, err := NewStructuredPhaseScheduler(context.Background())
	if err != nil {
		t.Fatalf("NewStructuredPhaseScheduler() error = %v", err)
	}
	runner, err := agentapplication.NewStructuredRunnerWithScheduler(model, catalog, budget, scheduler)
	if err != nil {
		t.Fatalf("runner construction error = %v", err)
	}
	return runner
}

func newRunnerWithContractScheduler(t *testing.T, model agentapplication.ChatModel, scheduler agentapplication.StructuredPhaseScheduler) *agentapplication.StructuredRunner {
	t.Helper()
	runner, err := agentapplication.NewStructuredRunnerWithScheduler(model, testSchedulerCatalog(t), agentapplication.DefaultRunBudget(), scheduler)
	if err != nil {
		t.Fatalf("runner construction error = %v", err)
	}
	return runner
}

func testSchedulerCatalog(t *testing.T) *agentapplication.RuntimeCatalog {
	t.Helper()
	catalog := agentapplication.NewRuntimeCatalog()
	prompt := agentapplication.PromptDefinition{
		Ref: domain.PromptRef{ID: "answer", Version: "v1"}, System: "return one object",
		InitialInstruction: "initial", RepairInstruction: "repair", ReducedInstruction: "reduced",
	}
	full := testSchedulerSchema("answer", false)
	reduced := testSchedulerSchema("answer-reduced", false)
	profile := agentapplication.ModelProfile{Ref: domain.ModelProfileRef{ID: "default", Version: "v1"}, Model: testSchedulerModel(), Timeout: time.Second, MaxOutputTokens: 1024}
	for _, err := range []error{catalog.RegisterPrompt(prompt), catalog.RegisterSchema(full), catalog.RegisterSchema(reduced), catalog.RegisterProfile(profile)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := catalog.Freeze(); err != nil {
		t.Fatal(err)
	}
	return catalog
}

func testSchedulerRequest() agentapplication.StructuredRunRequest {
	return agentapplication.StructuredRunRequest{
		ProfileRef: domain.ModelProfileRef{ID: "default", Version: "v1"}, PromptRef: domain.PromptRef{ID: "answer", Version: "v1"},
		SchemaRef: domain.SchemaRef{ID: "answer", Version: "v1"}, ReducedSchemaRef: domain.SchemaRef{ID: "answer-reduced", Version: "v1"},
		Input: []byte(`{"question":"what"}`),
	}
}

func testSchedulerSchema(id string, allowEmpty bool) agentapplication.SchemaDefinition {
	return agentapplication.SchemaDefinition{Ref: domain.SchemaRef{ID: id, Version: "v1"}, JSONSchema: []byte(`{"type":"object"}`), Decode: func(raw []byte) (json.RawMessage, error) {
		_, err := domain.DecodeStrict(raw, domain.DefaultDecodeLimits(), func(value schedulerContractOutput) error {
			if !allowEmpty && value.Value == "" {
				return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeStructuredOutputInvalid, false, errors.New("value required"))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		return append(json.RawMessage(nil), raw...), nil
	}}
}

func testSchedulerModel() domain.ModelRef {
	return domain.ModelRef{AdapterName: "openai-compatible", AdapterVersion: "v1", ModelID: "test-model", ModelVersion: "test-model-v1"}
}

func schedulerResponse(model domain.ModelRef, content string, inputTokens, outputTokens int64) agentapplication.ChatResponse {
	return agentapplication.ChatResponse{Model: model, Content: []byte(content), Usage: domain.TokenUsage{InputTokens: inputTokens, OutputTokens: outputTokens, TotalTokens: inputTokens + outputTokens}}
}

func errorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}

func errorKind(err error) foundation.ErrorKind {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Kind
	}
	return ""
}

type concurrentSchedulerModel struct {
	mu    sync.Mutex
	calls int
}

func (model *concurrentSchedulerModel) Chat(ctx context.Context, request agentapplication.ChatRequest) (agentapplication.ChatResponse, error) {
	if err := ctx.Err(); err != nil {
		return agentapplication.ChatResponse{}, err
	}
	model.mu.Lock()
	model.calls++
	model.mu.Unlock()
	return schedulerResponse(request.Model, `{"value":"concurrent"}`, 1, 1), nil
}

func (model *concurrentSchedulerModel) CallCount() int {
	model.mu.Lock()
	defer model.mu.Unlock()
	return model.calls
}

type schedulerRecordingRepository struct {
	mu          sync.Mutex
	calls       []domain.ModelCall
	completeErr error
}

func (repository *schedulerRecordingRepository) CreateModelRun(context.Context, domain.ModelRun) (domain.ModelRun, bool, error) {
	return domain.ModelRun{}, false, errors.New("unused")
}
func (repository *schedulerRecordingRepository) GetModelRun(context.Context, foundation.ID, foundation.ID) (agentapplication.ModelRunRecord, error) {
	return agentapplication.ModelRunRecord{}, errors.New("unused")
}
func (repository *schedulerRecordingRepository) StartModelCall(_ context.Context, _ foundation.ID, call domain.ModelCall) (domain.ModelCall, bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if err := domain.ValidateModelCall(call); err != nil {
		return domain.ModelCall{}, false, err
	}
	repository.calls = append(repository.calls, call)
	return call, false, nil
}
func (repository *schedulerRecordingRepository) CompleteModelCall(_ context.Context, command agentapplication.CompleteModelCallCommand) (domain.ModelCall, bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.completeErr != nil {
		return domain.ModelCall{}, false, repository.completeErr
	}
	if err := domain.ValidateModelCall(command.Call); err != nil {
		return domain.ModelCall{}, false, err
	}
	if command.Call.CallNo < 1 || command.Call.CallNo > len(repository.calls) {
		return domain.ModelCall{}, false, errors.New("call number out of range")
	}
	repository.calls[command.Call.CallNo-1] = command.Call
	return command.Call, false, nil
}
func (repository *schedulerRecordingRepository) FinalizeModelRun(context.Context, agentapplication.FinalizeModelRunCommand) (domain.ModelRun, bool, error) {
	return domain.ModelRun{}, false, errors.New("unused")
}
func (repository *schedulerRecordingRepository) MarkStaleModelCallsUnknown(context.Context, agentapplication.UnknownRecoveryQuery) ([]domain.ModelCall, error) {
	return nil, errors.New("unused")
}
func (repository *schedulerRecordingRepository) MarkStaleModelRunsUnknown(context.Context, agentapplication.UnknownRecoveryQuery) ([]domain.ModelRun, error) {
	return nil, errors.New("unused")
}

type schedulerRecordingIDs struct{ next int }

func (ids *schedulerRecordingIDs) New() (foundation.ID, error) {
	value, err := foundation.ParseID(fmt.Sprintf("81000000-0000-4000-8000-%012d", ids.next))
	ids.next++
	return value, err
}
