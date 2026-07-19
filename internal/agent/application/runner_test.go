package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

type testOutput struct {
	Value string `json:"value"`
}

func TestStructuredRunnerInitialSuccessUsesFrozenContract(t *testing.T) {
	catalog, request, profile := testCatalog(t, time.Second)
	response := testResponse(profile.Model, `{"value":"ok"}`, 2, 3)
	model := NewDeterministicChatModel(DeterministicChatStep{Response: response})
	runner := newTestRunner(t, model, catalog, DefaultRunBudget())

	result, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.CallCount != 1 || result.Phase != domain.ModelCallInitial || string(result.Output) != `{"value":"ok"}` {
		t.Fatalf("result = %+v output=%s", result, result.Output)
	}
	if result.Usage != response.Usage || model.CallCount() != 1 {
		t.Fatalf("usage = %+v calls = %d", result.Usage, model.CallCount())
	}
	call := model.Calls()[0]
	if call.Model != profile.Model || call.ProfileRef != profile.Ref || call.PromptRef != request.PromptRef || call.SchemaRef != request.SchemaRef {
		t.Fatalf("call did not freeze exact references: %+v", call)
	}
	if call.Phase != domain.ModelCallInitial || len(call.Messages) != 3 || !strings.Contains(call.Messages[2].Content, "UNTRUSTED TASK INPUT") {
		t.Fatalf("initial call = %+v", call)
	}
}

func TestStructuredRunnerRepairsWithRedactedErrorOnly(t *testing.T) {
	catalog, request, profile := testCatalog(t, time.Second)
	secretInvalid := "invalid-model-output-secret"
	model := NewDeterministicChatModel(
		DeterministicChatStep{Response: testResponse(profile.Model, secretInvalid, 1, 1)},
		DeterministicChatStep{Response: testResponse(profile.Model, `{"value":"repaired"}`, 2, 2)},
	)
	runner := newTestRunner(t, model, catalog, DefaultRunBudget())

	result, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.CallCount != 2 || result.Phase != domain.ModelCallRepair || string(result.Output) != `{"value":"repaired"}` {
		t.Fatalf("result = %+v output=%s", result, result.Output)
	}
	calls := model.Calls()
	if calls[0].Phase != domain.ModelCallInitial || calls[1].Phase != domain.ModelCallRepair {
		t.Fatalf("phases = %q, %q", calls[0].Phase, calls[1].Phase)
	}
	for _, message := range calls[1].Messages {
		if strings.Contains(message.Content, secretInvalid) {
			t.Fatalf("repair leaked invalid raw response: %q", message.Content)
		}
	}
	if !strings.Contains(calls[1].Messages[len(calls[1].Messages)-1].Content, domain.ErrorCodeStructuredOutputInvalid) {
		t.Fatalf("repair did not receive stable redacted code: %+v", calls[1].Messages)
	}
}

func TestStructuredRunnerUsesReducedSchemaOnThirdResponse(t *testing.T) {
	catalog, request, profile := testCatalog(t, time.Second)
	model := NewDeterministicChatModel(
		DeterministicChatStep{Response: testResponse(profile.Model, `{}`, 1, 1)},
		DeterministicChatStep{Response: testResponse(profile.Model, `{}`, 1, 1)},
		DeterministicChatStep{Response: testResponse(profile.Model, `{"value":"reduced"}`, 1, 1)},
	)
	runner := newTestRunner(t, model, catalog, DefaultRunBudget())

	result, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.CallCount != StructuredCallLimit || result.Phase != domain.ModelCallReduced {
		t.Fatalf("result = %+v", result)
	}
	calls := model.Calls()
	if calls[2].SchemaRef != request.ReducedSchemaRef || calls[0].SchemaRef != request.SchemaRef || calls[1].SchemaRef != request.SchemaRef {
		t.Fatalf("schema refs = %+v", []domain.SchemaRef{calls[0].SchemaRef, calls[1].SchemaRef, calls[2].SchemaRef})
	}
}

func TestStructuredRunnerValidationExhaustedAfterExactlyThreeResponses(t *testing.T) {
	catalog, request, profile := testCatalog(t, time.Second)
	model := NewDeterministicChatModel(
		DeterministicChatStep{Response: testResponse(profile.Model, `not-json`, 1, 1)},
		DeterministicChatStep{Response: testResponse(profile.Model, `still-not-json`, 1, 1)},
		DeterministicChatStep{Response: testResponse(profile.Model, `{}`, 1, 1)},
		DeterministicChatStep{Response: testResponse(profile.Model, `{"value":"must-not-run"}`, 1, 1)},
	)
	runner := newTestRunner(t, model, catalog, DefaultRunBudget())

	result, err := runner.Run(context.Background(), request)
	if errorCode(err) != domain.ErrorCodeValidationExhausted {
		t.Fatalf("Run() code = %q error=%v", errorCode(err), err)
	}
	if result.CallCount != StructuredCallLimit || model.CallCount() != StructuredCallLimit || result.Output != nil {
		t.Fatalf("result = %+v model calls=%d", result, model.CallCount())
	}
}

func TestStructuredRunnerRejectsDecoderTransformation(t *testing.T) {
	catalog := NewRuntimeCatalog()
	prompt := testPrompt()
	transforming := testSchema("answer", "v1", false)
	transforming.Decode = func(raw []byte) (json.RawMessage, error) {
		return json.RawMessage(`{"value":"defaulted"}`), nil
	}
	profile := testProfile(time.Second)
	if err := catalog.RegisterPrompt(prompt); err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterSchema(transforming); err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterProfile(profile); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Freeze(); err != nil {
		t.Fatal(err)
	}
	model := NewDeterministicChatModel(DeterministicChatStep{Response: testResponse(profile.Model, `{"value":"original"}`, 1, 1)})
	runner := newTestRunner(t, model, catalog, DefaultRunBudget())
	_, err := runner.Run(context.Background(), StructuredRunRequest{
		ProfileRef: profile.Ref, PromptRef: prompt.Ref, SchemaRef: transforming.Ref,
		ReducedSchemaRef: transforming.Ref, Input: []byte(`{"question":"what"}`),
	})
	if errorCode(err) != errorCodeDecoderContract || model.CallCount() != 1 {
		t.Fatalf("decoder contract error=%v code=%q calls=%d", err, errorCode(err), model.CallCount())
	}
}

func TestStructuredRunnerPreservesProviderFailureWithoutRetry(t *testing.T) {
	catalog, request, _ := testCatalog(t, time.Second)
	providerErr := foundation.NewError(foundation.ErrorRetryableFailure, "CHAT_PROVIDER_RATE_LIMITED", true, errors.New("provider unavailable"))
	model := NewDeterministicChatModel(DeterministicChatStep{Err: providerErr})
	runner := newTestRunner(t, model, catalog, DefaultRunBudget())

	result, err := runner.Run(context.Background(), request)
	if !errors.Is(err, providerErr) || errorCode(err) != "CHAT_PROVIDER_RATE_LIMITED" || model.CallCount() != 1 || result.CallCount != 1 {
		t.Fatalf("error=%v code=%q calls=%d result=%+v", err, errorCode(err), model.CallCount(), result)
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) || !classified.Retryable || classified.Kind != foundation.ErrorRetryableFailure {
		t.Fatalf("provider classification was not preserved: %#v", classified)
	}
}

func TestStructuredRunnerCancellationStopsCurrentCall(t *testing.T) {
	catalog, request, _ := testCatalog(t, time.Second)
	model := NewDeterministicChatModel(DeterministicChatStep{WaitForCancel: true})
	runner := newTestRunner(t, model, catalog, DefaultRunBudget())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := runner.Run(ctx, request)
		done <- err
	}()
	<-model.Started()
	cancel()
	err := <-done
	if errorCode(err) != ErrorCodeOperationCancelled || !errors.Is(err, context.Canceled) || model.CallCount() != 1 {
		t.Fatalf("cancel error=%v code=%q calls=%d", err, errorCode(err), model.CallCount())
	}
}

func TestStructuredRunnerEnforcesRequestResponseAndTokenBudgets(t *testing.T) {
	t.Run("request", func(t *testing.T) {
		catalog, request, profile := testCatalog(t, time.Second)
		model := NewDeterministicChatModel(DeterministicChatStep{Response: testResponse(profile.Model, `{"value":"ok"}`, 1, 1)})
		budget := DefaultRunBudget()
		budget.MaxRequestBytes = 1
		runner := newTestRunner(t, model, catalog, budget)
		_, err := runner.Run(context.Background(), request)
		if errorCode(err) != errorCodeRequestBudget || model.CallCount() != 0 {
			t.Fatalf("request budget error=%v code=%q calls=%d", err, errorCode(err), model.CallCount())
		}
	})

	t.Run("response", func(t *testing.T) {
		catalog, request, profile := testCatalog(t, time.Second)
		model := NewDeterministicChatModel(DeterministicChatStep{Response: testResponse(profile.Model, `{"value":"ok"}`, 1, 1)})
		budget := DefaultRunBudget()
		budget.MaxResponseBytes = 1
		runner := newTestRunner(t, model, catalog, budget)
		_, err := runner.Run(context.Background(), request)
		if errorCode(err) != errorCodeResponseBudget || model.CallCount() != 1 {
			t.Fatalf("response budget error=%v code=%q calls=%d", err, errorCode(err), model.CallCount())
		}
	})

	t.Run("token", func(t *testing.T) {
		catalog, request, profile := testCatalog(t, time.Second)
		model := NewDeterministicChatModel(DeterministicChatStep{Response: testResponse(profile.Model, `{"value":"ok"}`, 3, 2)})
		budget := DefaultRunBudget()
		budget.MaxTotalTokens = 4
		runner := newTestRunner(t, model, catalog, budget)
		_, err := runner.Run(context.Background(), request)
		if errorCode(err) != errorCodeTokenBudget || model.CallCount() != 1 {
			t.Fatalf("token budget error=%v code=%q calls=%d", err, errorCode(err), model.CallCount())
		}
	})
}

func TestStructuredRunnerBoundsTotalTimeout(t *testing.T) {
	catalog, request, _ := testCatalog(t, time.Second)
	model := NewDeterministicChatModel(DeterministicChatStep{WaitForCancel: true})
	budget := DefaultRunBudget()
	budget.Timeout = 20 * time.Millisecond
	runner := newTestRunner(t, model, catalog, budget)
	started := time.Now()
	_, err := runner.Run(context.Background(), request)
	if errorCode(err) != ErrorCodeOperationDeadline || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error=%v code=%q", err, errorCode(err))
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded timeout took %s", elapsed)
	}
}

func TestDeterministicChatModelStrictlyMatchesRequestAndCopiesCalls(t *testing.T) {
	request := ChatRequest{
		Phase:           domain.ModelCallInitial,
		ProfileRef:      domain.ModelProfileRef{ID: "default", Version: "v1"},
		PromptRef:       domain.PromptRef{ID: "answer", Version: "v1"},
		SchemaRef:       domain.SchemaRef{ID: "answer", Version: "v1"},
		Model:           testModelRef(),
		Messages:        []ChatMessage{{Role: MessageRoleSystem, Content: "system"}},
		OutputSchema:    []byte(`{"type":"object"}`),
		MaxOutputTokens: 100,
	}
	expected := cloneChatRequest(request)
	model := NewDeterministicChatModel(DeterministicChatStep{ExpectedRequest: &expected, Response: testResponse(request.Model, `{}`, 0, 0)})
	request.Messages[0].Content = "different"
	if _, err := model.Chat(context.Background(), request); errorCode(err) != errorCodeFakeRequestMismatch {
		t.Fatalf("fake mismatch code = %q", errorCode(err))
	}
	calls := model.Calls()
	calls[0].Messages[0].Content = "mutated"
	if model.Calls()[0].Messages[0].Content != "different" {
		t.Fatal("fake calls were mutated through returned slice")
	}
}

func TestChatContractAllowsReviewAndRejectsIdentityDrift(t *testing.T) {
	request := ChatRequest{
		Phase:           domain.ModelCallReview,
		ProfileRef:      domain.ModelProfileRef{ID: "review", Version: "v1"},
		PromptRef:       domain.PromptRef{ID: "review", Version: "v1"},
		SchemaRef:       domain.SchemaRef{ID: "review", Version: "v1"},
		Model:           testModelRef(),
		Messages:        []ChatMessage{{Role: MessageRoleSystem, Content: "review policy"}, {Role: MessageRoleUser, Content: "review input"}},
		OutputSchema:    []byte(`{"type":"object"}`),
		MaxOutputTokens: 100,
	}
	if err := ValidateChatRequest(request); err != nil {
		t.Fatalf("review request error = %v", err)
	}
	drifted := testResponse(request.Model, `{}`, 1, 1)
	drifted.Model.ModelVersion = "other-version"
	if err := ValidateChatResponse(request, drifted); errorCode(err) != errorCodeChatResponseMismatched {
		t.Fatalf("model drift code = %q", errorCode(err))
	}
	request.Messages = append(request.Messages, ChatMessage{Role: MessageRoleSystem, Content: "late policy"})
	if err := ValidateChatRequest(request); errorCode(err) != errorCodeChatRequestInvalid {
		t.Fatalf("late system message code = %q", errorCode(err))
	}
}

func TestChatContractAllowsPlanPhase(t *testing.T) {
	request := ChatRequest{
		Phase:           domain.ModelCallPlan,
		ProfileRef:      domain.ModelProfileRef{ID: "plan", Version: "v1"},
		PromptRef:       domain.PromptRef{ID: "rag-query-plan", Version: "v1"},
		SchemaRef:       domain.SchemaRef{ID: "agent.rag-query-plan", Version: "v1"},
		Model:           testModelRef(),
		Messages:        []ChatMessage{{Role: MessageRoleSystem, Content: "plan policy"}, {Role: MessageRoleUser, Content: "bounded context"}},
		OutputSchema:    []byte(`{"type":"object"}`),
		MaxOutputTokens: 100,
	}
	if err := ValidateChatRequest(request); err != nil {
		t.Fatalf("plan request error = %v", err)
	}
}

func testCatalog(t *testing.T, profileTimeout time.Duration) (*RuntimeCatalog, StructuredRunRequest, ModelProfile) {
	t.Helper()
	catalog := NewRuntimeCatalog()
	prompt := testPrompt()
	full := testSchema("answer", "v1", false)
	reduced := testSchema("answer-reduced", "v1", false)
	profile := testProfile(profileTimeout)
	for _, register := range []func() error{
		func() error { return catalog.RegisterPrompt(prompt) },
		func() error { return catalog.RegisterSchema(full) },
		func() error { return catalog.RegisterSchema(reduced) },
		func() error { return catalog.RegisterProfile(profile) },
	} {
		if err := register(); err != nil {
			t.Fatalf("catalog registration error = %v", err)
		}
	}
	if err := catalog.Freeze(); err != nil {
		t.Fatalf("Freeze() error = %v", err)
	}
	return catalog, StructuredRunRequest{
		ProfileRef:       profile.Ref,
		PromptRef:        prompt.Ref,
		SchemaRef:        full.Ref,
		ReducedSchemaRef: reduced.Ref,
		Input:            []byte(`{"question":"what"}`),
	}, profile
}

func testPrompt() PromptDefinition {
	return PromptDefinition{
		Ref:                domain.PromptRef{ID: "answer", Version: "v1"},
		System:             "Follow server policy and return one JSON object.",
		InitialInstruction: "Create the full structured answer.",
		RepairInstruction:  "Correct the output according to the stable validation code.",
		ReducedInstruction: "Return the reduced safe object only.",
	}
}

func testSchema(id, version string, allowEmpty bool) SchemaDefinition {
	return SchemaDefinition{
		Ref:        domain.SchemaRef{ID: id, Version: version},
		JSONSchema: []byte(`{"type":"object"}`),
		Decode: func(raw []byte) (json.RawMessage, error) {
			_, err := domain.DecodeStrict[testOutput](raw, domain.DefaultDecodeLimits(), func(output testOutput) error {
				if !allowEmpty && output.Value == "" {
					return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeStructuredOutputInvalid, false, errors.New("value is required"))
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
			return append(json.RawMessage(nil), raw...), nil
		},
	}
}

func testProfile(timeout time.Duration) ModelProfile {
	return ModelProfile{
		Ref:             domain.ModelProfileRef{ID: "default", Version: "v1"},
		Model:           testModelRef(),
		Timeout:         timeout,
		MaxOutputTokens: 1024,
	}
}

func testModelRef() domain.ModelRef {
	return domain.ModelRef{AdapterName: "openai-compatible", AdapterVersion: "v1", ModelID: "test-model", ModelVersion: "test-model-v1"}
}

func testResponse(model domain.ModelRef, content string, inputTokens, outputTokens int64) ChatResponse {
	return ChatResponse{
		Model:   model,
		Content: []byte(content),
		Usage: domain.TokenUsage{
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			TotalTokens:  inputTokens + outputTokens,
		},
	}
}

func newTestRunner(t *testing.T, model ChatModel, catalog *RuntimeCatalog, budget RunBudget) *StructuredRunner {
	t.Helper()
	runner, err := NewStructuredRunner(model, catalog, budget)
	if err != nil {
		t.Fatalf("NewStructuredRunner() error = %v", err)
	}
	return runner
}
