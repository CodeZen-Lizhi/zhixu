package eino

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	"github.com/cloudwego/eino/adk"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func TestAgentRuntimeUsesEinoReActAndProjectToolBridge(t *testing.T) {
	backend := &scriptedRuntimeModel{generate: []func([]*schema.Message, ...einomodel.Option) (*schema.Message, error){
		func(messages []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
			options := einomodel.GetCommonOptions(nil, opts...)
			if len(messages) != 2 || len(options.Tools) != 1 || options.Tools[0].Name != "ReadSource" || options.MaxTokens == nil || *options.MaxTokens != 10 {
				t.Fatalf("first messages=%d options=%+v", len(messages), options)
			}
			return &schema.Message{
				Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "provider-call-1", Type: "function", Function: schema.FunctionCall{Name: "ReadSource", Arguments: `{"source_id":"source-1"}`}}},
				ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls", Usage: &schema.TokenUsage{PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3}},
			}, nil
		},
		func(messages []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
			if len(messages) != 4 || messages[2].Role != schema.Assistant || messages[3].Role != schema.Tool || messages[3].ToolCallID != "provider-call-1" || messages[3].Content != `{"excerpt":"approved"}` {
				t.Fatalf("second messages=%#v", messages)
			}
			return &schema.Message{
				Role: schema.Assistant, Content: "Evidence is ready.",
				ResponseMeta: &schema.ResponseMeta{FinishReason: "stop", Usage: &schema.TokenUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}},
			}, nil
		},
	}}
	runtime, err := NewAgentRuntime(backend)
	if err != nil {
		t.Fatal(err)
	}
	repository := &runtimeRecordingRepository{}
	ledger, recorder := newRuntimeTestLedgerAndRecorder(t, repository, 7, 2, 1)
	invoker := &runtimeToolInvoker{}
	result, err := runtime.Run(context.Background(), agentapplication.AgentRunRequest{
		Model: runtimeTestModelRef(), Profile: runtimeTestProfileRef(), Prompt: runtimeTestPromptRef(), Schema: runtimeTestSchemaRef(),
		Messages: []agentapplication.AgentMessage{{Role: agentapplication.AgentMessageSystem, Content: "Use approved evidence only."}, {Role: agentapplication.AgentMessageUser, Content: "Answer the question."}},
		Tools: []agentapplication.AgentToolSpec{{
			Ref: toolsdomain.ToolRef{Name: "ReadSource", Version: 2}, Name: "ReadSource", Description: "Read approved source",
			InputSchema: []byte(`{"type":"object","additionalProperties":false,"required":["source_id"],"properties":{"source_id":{"type":"string"}}}`),
		}},
		MaxIterations: 2, MaxInputTokens: 10, MaxOutputTokens: 10,
		Budget: ledger, Recorder: recorder, ToolInvoker: invoker,
	})
	if err != nil {
		t.Fatalf("run error=%v cause=%v", err, errors.Unwrap(err))
	}
	if result.FinalText != "Evidence is ready." || result.Iterations != 2 || result.ToolCalls != 1 ||
		result.Usage != (agentdomain.TokenUsage{InputTokens: 5, OutputTokens: 3, TotalTokens: 8}) {
		t.Fatalf("result=%+v", result)
	}
	if invoker.calls != 1 || invoker.last.Ref != (toolsdomain.ToolRef{Name: "ReadSource", Version: 2}) || invoker.last.CallNo != 1 || invoker.last.Reason != agentToolReason {
		t.Fatalf("invoker=%+v", invoker)
	}
	if len(repository.calls) != 2 {
		t.Fatalf("model calls=%+v", repository.calls)
	}
	for index, call := range repository.calls {
		if call.CallNo != index+1 || call.Phase != agentdomain.ModelCallAgent || call.Status != agentdomain.ModelCallSucceeded || call.RequestHash == "" || call.ResponseHash == "" {
			t.Fatalf("call[%d]=%+v", index, call)
		}
	}
	snapshot := ledger.Snapshot()
	if snapshot.ModelCalls != 2 || snapshot.AgentIterations != 2 || snapshot.ToolCalls != 1 || snapshot.InputTokens != 5 || snapshot.OutputTokens != 3 {
		t.Fatalf("budget=%+v", snapshot)
	}
}

func TestAgentRuntimeReturnsDirectlyAfterConfiguredTool(t *testing.T) {
	backend := &scriptedRuntimeModel{generate: []func([]*schema.Message, ...einomodel.Option) (*schema.Message, error){
		func(messages []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
			options := einomodel.GetCommonOptions(nil, opts...)
			if len(messages) != 2 || len(options.Tools) != 1 || options.Tools[0].Name != "ReadSource" {
				t.Fatalf("messages=%d options=%+v", len(messages), options)
			}
			return &schema.Message{
				Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "provider-call-1", Type: "function", Function: schema.FunctionCall{Name: "ReadSource", Arguments: `{"source_id":"source-1"}`}}},
				ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls", Usage: &schema.TokenUsage{PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3}},
			}, nil
		},
	}}
	runtime, err := NewAgentRuntime(backend)
	if err != nil {
		t.Fatal(err)
	}
	repository := &runtimeRecordingRepository{}
	ledger, recorder := newRuntimeTestLedgerAndRecorder(t, repository, 6, 2, 1)
	invoker := &runtimeToolInvoker{}
	result, err := runtime.Run(context.Background(), agentapplication.AgentRunRequest{
		Model: runtimeTestModelRef(), Profile: runtimeTestProfileRef(), Prompt: runtimeTestPromptRef(), Schema: runtimeTestSchemaRef(),
		Messages: []agentapplication.AgentMessage{{Role: agentapplication.AgentMessageSystem, Content: "Use approved evidence only."}, {Role: agentapplication.AgentMessageUser, Content: "Answer the question."}},
		Tools: []agentapplication.AgentToolSpec{{
			Ref: toolsdomain.ToolRef{Name: "ReadSource", Version: 2}, Name: "ReadSource", Description: "Read approved source",
			InputSchema: []byte(`{"type":"object","additionalProperties":false,"required":["source_id"],"properties":{"source_id":{"type":"string"}}}`),
		}},
		ReturnDirectlyTools: []string{"ReadSource"},
		MaxIterations:       2, MaxInputTokens: 10, MaxOutputTokens: 10,
		Budget: ledger, Recorder: recorder, ToolInvoker: invoker,
	})
	if err != nil {
		t.Fatalf("run error=%v cause=%v", err, errors.Unwrap(err))
	}
	if result.FinalText != `{"excerpt":"approved"}` || result.Iterations != 1 || result.ToolCalls != 1 ||
		result.Usage != (agentdomain.TokenUsage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}) {
		t.Fatalf("result=%+v", result)
	}
	if invoker.calls != 1 || len(repository.calls) != 1 || repository.calls[0].Status != agentdomain.ModelCallSucceeded {
		t.Fatalf("invoker=%+v calls=%+v", invoker, repository.calls)
	}
}

func TestAgentRuntimeRejectsMultipleVersionsOfOneToolName(t *testing.T) {
	backend := &scriptedRuntimeModel{}
	runtime, err := NewAgentRuntime(backend)
	if err != nil {
		t.Fatal(err)
	}
	repository := &runtimeRecordingRepository{}
	ledger, recorder := newRuntimeTestLedgerAndRecorder(t, repository, 6, 1, 1)
	_, err = runtime.Run(context.Background(), agentapplication.AgentRunRequest{
		Model: runtimeTestModelRef(), Profile: runtimeTestProfileRef(), Prompt: runtimeTestPromptRef(), Schema: runtimeTestSchemaRef(),
		Messages: []agentapplication.AgentMessage{
			{Role: agentapplication.AgentMessageSystem, Content: "Use approved evidence only."},
			{Role: agentapplication.AgentMessageUser, Content: "Answer the question."},
		},
		Tools: []agentapplication.AgentToolSpec{
			{Ref: toolsdomain.ToolRef{Name: "ReadSource", Version: 1}, Name: "ReadSource", Description: "Read approved source v1", InputSchema: []byte(`{"type":"object"}`)},
			{Ref: toolsdomain.ToolRef{Name: "ReadSource", Version: 2}, Name: "ReadSource", Description: "Read approved source v2", InputSchema: []byte(`{"type":"object"}`)},
		},
		MaxIterations: 1, MaxInputTokens: 10, MaxOutputTokens: 10,
		Budget: ledger, Recorder: recorder, ToolInvoker: &runtimeToolInvoker{},
	})
	if errorCode(err) != agentapplication.ErrorCodeAgentRuntimeInvalid || errorKind(err) != foundation.ErrorConsistencyViolation {
		t.Fatalf("error=%v kind=%s code=%q", err, errorKind(err), errorCode(err))
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.generate) != 0 || len(repository.calls) != 0 || ledger.Snapshot().ModelCalls != 0 {
		t.Fatalf("invalid tool versions reached runtime: calls=%d records=%d budget=%+v", len(backend.generate), len(repository.calls), ledger.Snapshot())
	}
}

func TestAgentRuntimeBindsSequentialProviderCallsToProjectCallNumbers(t *testing.T) {
	backend := &scriptedRuntimeModel{generate: []func([]*schema.Message, ...einomodel.Option) (*schema.Message, error){
		func([]*schema.Message, ...einomodel.Option) (*schema.Message, error) {
			return &schema.Message{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{
					{ID: "provider-call-1", Type: "function", Function: schema.FunctionCall{Name: "ReadSource", Arguments: `{}`}},
					{ID: "provider-call-2", Type: "function", Function: schema.FunctionCall{Name: "ValidateCitation", Arguments: `{}`}},
				},
				ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls", Usage: &schema.TokenUsage{PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3}},
			}, nil
		},
		func(messages []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
			if len(messages) != 5 || messages[3].ToolCallID != "provider-call-1" || messages[4].ToolCallID != "provider-call-2" {
				t.Fatalf("messages=%#v", messages)
			}
			return &schema.Message{
				Role: schema.Assistant, Content: "Evidence is ready.",
				ResponseMeta: &schema.ResponseMeta{FinishReason: "stop", Usage: &schema.TokenUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}},
			}, nil
		},
	}}
	runtime, err := NewAgentRuntime(backend)
	if err != nil {
		t.Fatal(err)
	}
	repository := &runtimeRecordingRepository{}
	ledger, recorder := newRuntimeTestLedgerAndRecorder(t, repository, 7, 2, 2)
	invoker := &contextCheckingRuntimeToolInvoker{}
	type projectContextKey struct{}
	projectContext, cancel := context.WithTimeout(context.WithValue(context.Background(), projectContextKey{}, "project-value"), time.Minute)
	defer cancel()
	invoker.check = func(ctx context.Context) error {
		if ctx.Value(projectContextKey{}) != "project-value" {
			return errors.New("project context value was not preserved")
		}
		if compose.GetToolCallID(ctx) != "" {
			return errors.New("provider call id crossed the project tool port")
		}
		if _, visible := providerToolCallBindingFromContext(ctx); visible {
			return errors.New("adapter-private provider binding crossed the project tool port")
		}
		if strings.Contains(fmt.Sprintf("%v", ctx), "provider-call-") {
			return errors.New("provider call id remained reachable through context formatting")
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) <= 0 {
			return errors.New("tool execution deadline was not preserved")
		}
		return nil
	}
	result, err := runtime.Run(projectContext, agentapplication.AgentRunRequest{
		Model: runtimeTestModelRef(), Profile: runtimeTestProfileRef(), Prompt: runtimeTestPromptRef(), Schema: runtimeTestSchemaRef(),
		Messages: []agentapplication.AgentMessage{{Role: agentapplication.AgentMessageSystem, Content: "Use approved evidence only."}, {Role: agentapplication.AgentMessageUser, Content: "Answer the question."}},
		Tools: []agentapplication.AgentToolSpec{
			{Ref: toolsdomain.ToolRef{Name: "ReadSource", Version: 2}, Name: "ReadSource", Description: "Read approved source", InputSchema: []byte(`{"type":"object"}`)},
			{Ref: toolsdomain.ToolRef{Name: "ValidateCitation", Version: 2}, Name: "ValidateCitation", Description: "Validate approved citation", InputSchema: []byte(`{"type":"object"}`)},
		},
		MaxIterations: 2, MaxInputTokens: 10, MaxOutputTokens: 10,
		Budget: ledger, Recorder: recorder, ToolInvoker: invoker,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalText != "Evidence is ready." || result.ToolCalls != 2 || len(invoker.history) != 2 ||
		invoker.history[0].CallNo != 1 || invoker.history[1].CallNo != 2 || invoker.contextErr != nil {
		t.Fatalf("result=%+v invocations=%+v context_error=%v", result, invoker.history, invoker.contextErr)
	}
	if strings.Contains(fmt.Sprintf("%+v", invoker.history), "provider-call-") {
		t.Fatalf("project invocation leaked provider ids: %+v", invoker.history)
	}
}

func TestProviderToolCallMiddlewareRejectsInvalidOrDuplicateIDsBeforeExecution(t *testing.T) {
	tests := []struct {
		name string
		id   string
	}{
		{name: "empty", id: " \t"},
		{name: "oversized", id: strings.Repeat("x", maxProviderToolCallIDBytes+1)},
		{name: "invalid utf8", id: string([]byte{0xff})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := &agentRunState{}
			nextCalls := 0
			wrapped := newProviderToolCallMiddleware(state).Invokable(func(context.Context, *compose.ToolInput) (*compose.ToolOutput, error) {
				nextCalls++
				return &compose.ToolOutput{Result: `{}`}, nil
			})
			_, err := wrapped(context.Background(), &compose.ToolInput{Name: "ReadSource", CallID: test.id, Arguments: `{}`})
			if errorCode(err) != agentToolInvocationCode || errorKind(err) != foundation.ErrorConsistencyViolation || nextCalls != 0 {
				t.Fatalf("error=%v kind=%s code=%q next_calls=%d", err, errorKind(err), errorCode(err), nextCalls)
			}
		})
	}

	state := &agentRunState{}
	nextCalls := 0
	wrapped := newProviderToolCallMiddleware(state).Invokable(func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
		nextCalls++
		binding, ok := providerToolCallBindingFromContext(ctx)
		if !ok || binding.id != input.CallID || binding.toolName != input.Name {
			t.Fatalf("binding=%+v ok=%t input=%+v", binding, ok, input)
		}
		return &compose.ToolOutput{Result: `{}`}, nil
	})
	input := &compose.ToolInput{Name: "ReadSource", CallID: "provider-call-1", Arguments: `{}`}
	if _, err := wrapped(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	_, err := wrapped(context.Background(), input)
	if errorCode(err) != agentToolInvocationCode || errorKind(err) != foundation.ErrorConsistencyViolation || nextCalls != 1 {
		t.Fatalf("error=%v kind=%s code=%q next_calls=%d", err, errorKind(err), errorCode(err), nextCalls)
	}
}

func TestProjectToolInvocationContextPreservesCancellationWithoutExecutionValues(t *testing.T) {
	type projectContextKey struct{}
	type executionContextKey struct{}
	projectContext := context.WithValue(context.Background(), projectContextKey{}, "project-value")
	executionWithDeadline, cancelDeadline := context.WithTimeout(context.WithValue(projectContext, executionContextKey{}, "provider-secret-value"), time.Minute)
	defer cancelDeadline()
	executionContext, cancelExecution := context.WithCancel(executionWithDeadline)
	cleanContext, release := newProjectToolInvocationContext(executionContext, projectContext)
	defer release()

	if cleanContext.Value(projectContextKey{}) != "project-value" || cleanContext.Value(executionContextKey{}) != nil ||
		strings.Contains(fmt.Sprintf("%v", cleanContext), "provider-secret-value") {
		t.Fatalf("clean context leaked execution values: %v", cleanContext)
	}
	executionDeadline, executionHasDeadline := executionContext.Deadline()
	cleanDeadline, cleanHasDeadline := cleanContext.Deadline()
	if !executionHasDeadline || !cleanHasDeadline || !cleanDeadline.Equal(executionDeadline) {
		t.Fatalf("execution deadline=%v/%t clean deadline=%v/%t", executionDeadline, executionHasDeadline, cleanDeadline, cleanHasDeadline)
	}
	cancelExecution()
	select {
	case <-cleanContext.Done():
		if !errors.Is(cleanContext.Err(), context.Canceled) {
			t.Fatalf("context error=%v", cleanContext.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("execution cancellation did not reach project tool context")
	}
}

func TestConsumeAgentEventsAssociatesMultipleProviderToolCalls(t *testing.T) {
	iterator := agentEventIterator(
		agentAssistantEvent("", providerToolCall("provider-call-1", "ReadSource"), providerToolCall("provider-call-2", "ValidateCitation")),
		agentToolEvent(`{"excerpt":"one"}`, "provider-call-1", "ReadSource", "ReadSource"),
		agentToolEvent(`{"valid":true}`, "provider-call-2", "ValidateCitation", "ValidateCitation"),
		agentAssistantEvent("Evidence is ready."),
	)
	result, err := consumeAgentEvents(iterator, false, nil)
	if err != nil || result != "Evidence is ready." {
		t.Fatalf("result=%q error=%v", result, err)
	}

	direct, err := consumeAgentEvents(agentEventIterator(
		agentAssistantEvent("", providerToolCall("provider-call-direct", "ReadSource")),
		agentToolEvent(`{"excerpt":"direct"}`, "provider-call-direct", "ReadSource", "ReadSource"),
	), true, nil)
	if err != nil || direct != `{"excerpt":"direct"}` {
		t.Fatalf("direct=%q error=%v", direct, err)
	}
}

func TestConsumeAgentEventsCancelsRunAndClosesQueuedStreamsAfterError(t *testing.T) {
	runContext, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	iterator, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	nestedReader, nestedWriter := schema.Pipe[*schema.Message](0)
	streamClosed := make(chan bool, 1)
	go func() {
		streamClosed <- nestedWriter.Send(&schema.Message{Role: schema.Assistant, Content: "discarded"}, nil)
		nestedWriter.Close()
	}()
	go func() {
		generator.Send(nil)
		<-runContext.Done()
		generator.Send(adk.EventFromMessage(nil, nestedReader, schema.Assistant, ""))
		generator.Close()
	}()

	_, err := consumeAgentEvents(iterator, false, cancelRun)
	if errorCode(err) != agentIteratorOutputCode || errorKind(err) != foundation.ErrorConsistencyViolation {
		t.Fatalf("error=%v kind=%s code=%q", err, errorKind(err), errorCode(err))
	}
	select {
	case closed := <-streamClosed:
		if !closed {
			t.Fatal("queued message stream was consumed instead of being closed after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("queued message stream was not closed after agent cancellation")
	}
}

func TestConsumeAgentEventsRejectsInvalidProviderToolCallAssociations(t *testing.T) {
	canaryID := "provider-secret-call-id"
	tests := []struct {
		name   string
		events []*adk.AgentEvent
	}{
		{name: "empty assistant id", events: []*adk.AgentEvent{agentAssistantEvent("", providerToolCall(" ", "ReadSource"))}},
		{name: "oversized assistant id", events: []*adk.AgentEvent{agentAssistantEvent("", providerToolCall(strings.Repeat("x", maxProviderToolCallIDBytes+1), "ReadSource"))}},
		{name: "duplicate assistant id", events: []*adk.AgentEvent{agentAssistantEvent("", providerToolCall(canaryID, "ReadSource"), providerToolCall(canaryID, "ValidateCitation"))}},
		{name: "unknown result", events: []*adk.AgentEvent{agentToolEvent(`{}`, canaryID, "ReadSource", "ReadSource")}},
		{name: "duplicate result", events: []*adk.AgentEvent{
			agentAssistantEvent("", providerToolCall(canaryID, "ReadSource")),
			agentToolEvent(`{}`, canaryID, "ReadSource", "ReadSource"),
			agentToolEvent(`{}`, canaryID, "ReadSource", "ReadSource"),
		}},
		{name: "tool name mismatch", events: []*adk.AgentEvent{
			agentAssistantEvent("", providerToolCall(canaryID, "ReadSource")),
			agentToolEvent(`{}`, canaryID, "ValidateCitation", "ValidateCitation"),
		}},
		{name: "event and message name mismatch", events: []*adk.AgentEvent{
			agentAssistantEvent("", providerToolCall(canaryID, "ReadSource")),
			agentToolEvent(`{}`, canaryID, "ReadSource", "ValidateCitation"),
		}},
		{name: "reused id in later round", events: []*adk.AgentEvent{
			agentAssistantEvent("", providerToolCall(canaryID, "ReadSource")),
			agentToolEvent(`{}`, canaryID, "ReadSource", "ReadSource"),
			agentAssistantEvent("", providerToolCall(canaryID, "ReadSource")),
		}},
		{name: "unresolved at end", events: []*adk.AgentEvent{
			agentAssistantEvent("", providerToolCall(canaryID, "ReadSource")),
			agentAssistantEvent("premature final"),
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := consumeAgentEvents(agentEventIterator(test.events...), false, nil)
			if errorCode(err) != agentIteratorOutputCode || errorKind(err) != foundation.ErrorConsistencyViolation {
				t.Fatalf("error=%v kind=%s code=%q", err, errorKind(err), errorCode(err))
			}
			if strings.Contains(err.Error(), canaryID) {
				t.Fatalf("safe error leaked provider id: %v", err)
			}
		})
	}
}

func agentEventIterator(events ...*adk.AgentEvent) *adk.AsyncIterator[*adk.AgentEvent] {
	iterator, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	for _, event := range events {
		generator.Send(event)
	}
	generator.Close()
	return iterator
}

func providerToolCall(id, name string) schema.ToolCall {
	return schema.ToolCall{ID: id, Type: "function", Function: schema.FunctionCall{Name: name, Arguments: `{}`}}
}

func agentAssistantEvent(content string, calls ...schema.ToolCall) *adk.AgentEvent {
	return adk.EventFromMessage(&schema.Message{Role: schema.Assistant, Content: content, ToolCalls: calls}, nil, schema.Assistant, "")
}

func agentToolEvent(content, id, messageToolName, eventToolName string) *adk.AgentEvent {
	message := schema.ToolMessage(content, id, schema.WithToolName(messageToolName))
	return adk.EventFromMessage(message, nil, schema.Tool, eventToolName)
}

func TestAnswerStreamRuntimeContinuesAfterDraftSinkDegrades(t *testing.T) {
	backend := &scriptedRuntimeModel{stream: func(messages []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
		options := einomodel.GetCommonOptions(nil, opts...)
		if len(messages) != 2 || options.ToolChoice == nil || *options.ToolChoice != schema.ToolChoiceForbidden || options.MaxTokens == nil || *options.MaxTokens != 10 {
			t.Fatalf("messages=%d options=%+v", len(messages), options)
		}
		return schema.StreamReaderFromArray([]*schema.Message{
			{Role: schema.Assistant, Content: "Final "},
			{Content: "answer."},
			{ResponseMeta: &schema.ResponseMeta{FinishReason: "stop", Usage: &schema.TokenUsage{PromptTokens: 4, CompletionTokens: 2, TotalTokens: 6}}},
		}), nil
	}}
	runtime, err := NewAnswerStreamRuntime(backend)
	if err != nil {
		t.Fatal(err)
	}
	repository := &runtimeRecordingRepository{}
	ledger, recorder := newRuntimeTestLedgerAndRecorder(t, repository, 5, 1, 0)
	sink := &degradingRuntimeSink{}
	result, err := runtime.Stream(context.Background(), agentapplication.AnswerStreamRequest{
		Model: runtimeTestModelRef(), Profile: runtimeTestProfileRef(), Prompt: runtimeTestPromptRef(), Schema: runtimeTestSchemaRef(),
		Messages:       []agentapplication.AgentMessage{{Role: agentapplication.AgentMessageSystem, Content: "Return the final answer."}, {Role: agentapplication.AgentMessageUser, Content: "Use the frozen evidence."}},
		MaxInputTokens: 10, MaxOutputTokens: 10, Budget: ledger, Recorder: recorder,
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "Final answer." || !result.DraftDegraded || result.Usage != (agentdomain.TokenUsage{InputTokens: 4, OutputTokens: 2, TotalTokens: 6}) || sink.calls != 1 {
		t.Fatalf("result=%+v sink=%+v", result, sink)
	}
	if len(repository.calls) != 1 || repository.calls[0].Phase != agentdomain.ModelCallAnswer || repository.calls[0].Status != agentdomain.ModelCallSucceeded {
		t.Fatalf("calls=%+v", repository.calls)
	}
}

func TestAnswerStreamRuntimeSplitsLargeProviderFrameOnUTF8Boundaries(t *testing.T) {
	content := strings.Repeat("界", agentapplication.MaxAnswerStreamChunkBytes/3+10)
	backend := &scriptedRuntimeModel{stream: func(_ []*schema.Message, _ ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
		return schema.StreamReaderFromArray([]*schema.Message{
			{Role: schema.Assistant, Content: content},
			{ResponseMeta: &schema.ResponseMeta{FinishReason: "stop", Usage: &schema.TokenUsage{PromptTokens: 4, CompletionTokens: 2, TotalTokens: 6}}},
		}), nil
	}}
	runtime, err := NewAnswerStreamRuntime(backend)
	if err != nil {
		t.Fatal(err)
	}
	repository := &runtimeRecordingRepository{}
	ledger, recorder := newRuntimeTestLedgerAndRecorder(t, repository, 5, 1, 0)
	sink := &collectingRuntimeSink{}
	result, err := runtime.Stream(context.Background(), agentapplication.AnswerStreamRequest{
		Model: runtimeTestModelRef(), Profile: runtimeTestProfileRef(), Prompt: runtimeTestPromptRef(), Schema: runtimeTestSchemaRef(),
		Messages:       []agentapplication.AgentMessage{{Role: agentapplication.AgentMessageSystem, Content: "Return the final answer."}, {Role: agentapplication.AgentMessageUser, Content: "Use the frozen evidence."}},
		MaxInputTokens: 10, MaxOutputTokens: 10, Budget: ledger, Recorder: recorder,
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != content || result.DraftDegraded || len(sink.chunks) < 2 || strings.Join(sink.chunks, "") != content {
		t.Fatalf("content=%d degraded=%t chunks=%d", len(result.Content), result.DraftDegraded, len(sink.chunks))
	}
	for index, chunk := range sink.chunks {
		if len(chunk) > agentapplication.MaxAnswerStreamChunkBytes || !utf8.ValidString(chunk) {
			t.Fatalf("chunk[%d] bytes=%d", index, len(chunk))
		}
	}
}

func TestAnswerStreamRuntimeRejectsInvalidProviderStreams(t *testing.T) {
	tests := []struct {
		name   string
		stream func([]*schema.Message, ...einomodel.Option) (*schema.StreamReader[*schema.Message], error)
	}{
		{
			name: "nil stream",
			stream: func([]*schema.Message, ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
				return nil, nil
			},
		},
		{
			name: "duplicate usage",
			stream: func([]*schema.Message, ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
				return schema.StreamReaderFromArray([]*schema.Message{
					{Role: schema.Assistant, Content: "answer"},
					{ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 4, CompletionTokens: 2, TotalTokens: 6}}},
					{ResponseMeta: &schema.ResponseMeta{FinishReason: "stop", Usage: &schema.TokenUsage{PromptTokens: 4, CompletionTokens: 2, TotalTokens: 6}}},
				}), nil
			},
		},
		{
			name: "zero usage",
			stream: func([]*schema.Message, ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
				return schema.StreamReaderFromArray([]*schema.Message{
					{Role: schema.Assistant, Content: "answer"},
					{ResponseMeta: &schema.ResponseMeta{FinishReason: "stop", Usage: &schema.TokenUsage{}}},
				}), nil
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := &scriptedRuntimeModel{stream: test.stream}
			runtime, err := NewAnswerStreamRuntime(backend)
			if err != nil {
				t.Fatal(err)
			}
			repository := &runtimeRecordingRepository{}
			ledger, recorder := newRuntimeTestLedgerAndRecorder(t, repository, 5, 1, 0)
			_, err = runtime.Stream(context.Background(), agentapplication.AnswerStreamRequest{
				Model: runtimeTestModelRef(), Profile: runtimeTestProfileRef(), Prompt: runtimeTestPromptRef(), Schema: runtimeTestSchemaRef(),
				Messages:       []agentapplication.AgentMessage{{Role: agentapplication.AgentMessageSystem, Content: "Return the final answer."}, {Role: agentapplication.AgentMessageUser, Content: "Use the frozen evidence."}},
				MaxInputTokens: 10, MaxOutputTokens: 10, Budget: ledger, Recorder: recorder,
			}, nil)
			if errorCode(err) != agentRuntimeOutputCode || errorKind(err) != foundation.ErrorConsistencyViolation {
				t.Fatalf("error=%v kind=%s code=%q", err, errorKind(err), errorCode(err))
			}
			if len(repository.calls) != 1 || repository.calls[0].Status != agentdomain.ModelCallFailed {
				t.Fatalf("calls=%+v", repository.calls)
			}
		})
	}
}

func TestAnswerStreamRuntimeRejectsProviderToolCallsBeforeInvalidFrameDraftWrite(t *testing.T) {
	toolCall := &schema.Message{
		ToolCalls: []schema.ToolCall{{
			ID: "provider-call-1", Type: "function",
			Function: schema.FunctionCall{Name: "ReadSource", Arguments: `{"evidence_ref":"E1"}`},
		}},
	}
	tests := []struct {
		name       string
		frames     []*schema.Message
		wantChunks []string
	}{
		{name: "tool call in first frame", frames: []*schema.Message{{Role: schema.Assistant, Content: "must not reach the draft", ToolCalls: toolCall.ToolCalls}}},
		{
			name: "tool call after valid content frame",
			frames: []*schema.Message{
				{Role: schema.Assistant, Content: "visible unverified draft"},
				{Content: "must not reach the draft", ToolCalls: toolCall.ToolCalls},
			},
			wantChunks: []string{"visible unverified draft"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := &scriptedRuntimeModel{stream: func([]*schema.Message, ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
				return schema.StreamReaderFromArray(test.frames), nil
			}}
			runtime, err := NewAnswerStreamRuntime(backend)
			if err != nil {
				t.Fatal(err)
			}
			repository := &runtimeRecordingRepository{}
			ledger, recorder := newRuntimeTestLedgerAndRecorder(t, repository, 5, 1, 0)
			sink := &collectingRuntimeSink{}

			_, err = runtime.Stream(context.Background(), agentapplication.AnswerStreamRequest{
				Model: runtimeTestModelRef(), Profile: runtimeTestProfileRef(), Prompt: runtimeTestPromptRef(), Schema: runtimeTestSchemaRef(),
				Messages:       []agentapplication.AgentMessage{{Role: agentapplication.AgentMessageSystem, Content: "Return the final answer."}, {Role: agentapplication.AgentMessageUser, Content: "Use the frozen evidence."}},
				MaxInputTokens: 10, MaxOutputTokens: 10, Budget: ledger, Recorder: recorder,
			}, sink)
			if errorCode(err) != agentapplication.ErrorCodeAnswerStreamInvalid || errorKind(err) != foundation.ErrorConsistencyViolation {
				t.Fatalf("error=%v kind=%s code=%q", err, errorKind(err), errorCode(err))
			}
			if !slices.Equal(sink.chunks, test.wantChunks) {
				t.Fatalf("draft chunks=%q want=%q", sink.chunks, test.wantChunks)
			}
			if len(repository.calls) != 1 || repository.calls[0].Status != agentdomain.ModelCallFailed ||
				repository.calls[0].ErrorCode != agentapplication.ErrorCodeAnswerStreamInvalid ||
				repository.calls[0].ResponseHash != "" || repository.calls[0].ResponseBytes != 0 {
				t.Fatalf("calls=%+v", repository.calls)
			}
		})
	}
}

func TestCanonicalRuntimeMessagesAcceptOnlyKnownNonWireMetadata(t *testing.T) {
	type namedRuntimeString string

	message := &schema.Message{
		Role: schema.Assistant, Content: "answer", Name: "assistant-name", ReasoningContent: "reasoning",
		Extra: map[string]any{
			"_eino_msg_id":      namedRuntimeString("message-id"),
			"openai-request-id": namedRuntimeString("request-id"),
			"reasoning-content": "reasoning",
		},
	}
	canonical, err := canonicalRuntimeMessages([]*schema.Message{message}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(canonical) != 1 || canonical[0].Name != "assistant-name" || canonical[0].ReasoningContent != "reasoning" {
		t.Fatalf("canonical=%+v", canonical)
	}

	message.Extra["openai-request-id"] = 123
	if _, err = canonicalRuntimeMessages([]*schema.Message{message}, true); err == nil {
		t.Fatal("expected non-string request ID metadata to fail closed")
	}
	message.Extra["openai-request-id"] = namedRuntimeString("request-id")
	message.Extra["unknown"] = "must not be silently omitted"
	if _, err = canonicalRuntimeMessages([]*schema.Message{message}, true); err == nil {
		t.Fatal("expected unknown metadata to fail closed")
	}
	delete(message.Extra, "unknown")
	message.Extra["reasoning-content"] = "different"
	if _, err = canonicalRuntimeMessages([]*schema.Message{message}, true); err == nil {
		t.Fatal("expected inconsistent reasoning metadata to fail closed")
	}
}

func TestCanonicalRuntimeDocumentsDoNotPersistProviderToolCallIDs(t *testing.T) {
	requestForID := func(providerID string) []*schema.Message {
		return []*schema.Message{
			schema.SystemMessage("Use approved tools."),
			{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{{
					ID: providerID, Type: "function",
					Function: schema.FunctionCall{Name: "ReadSource", Arguments: `{"source_id":"source-1"}`},
				}},
			},
			{Role: schema.Tool, Content: `{"excerpt":"approved"}`, ToolCallID: providerID},
		}
	}

	leftRequest, err := canonicalRuntimeRequest(requestForID("provider-call-left"), nil)
	if err != nil {
		t.Fatal(err)
	}
	rightRequest, err := canonicalRuntimeRequest(requestForID("provider-call-right"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(leftRequest) != string(rightRequest) || strings.Contains(string(leftRequest), "provider-call-") ||
		!strings.Contains(string(leftRequest), `"id":"tool-call-1"`) ||
		!strings.Contains(string(leftRequest), `"tool_call_id":"tool-call-1"`) {
		t.Fatalf("left=%s right=%s", leftRequest, rightRequest)
	}

	responseForID := func(providerID string) *schema.Message {
		return &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID: providerID, Type: "function",
				Function: schema.FunctionCall{Name: "ReadSource", Arguments: `{"source_id":"source-1"}`},
			}},
		}
	}
	leftResponse, err := canonicalRuntimeResponse(responseForID("provider-call-left"))
	if err != nil {
		t.Fatal(err)
	}
	rightResponse, err := canonicalRuntimeResponse(responseForID("provider-call-right"))
	if err != nil {
		t.Fatal(err)
	}
	if string(leftResponse) != string(rightResponse) || strings.Contains(string(leftResponse), "provider-call-") {
		t.Fatalf("left=%s right=%s", leftResponse, rightResponse)
	}

	unknownResult := []*schema.Message{{Role: schema.Tool, Content: "result", ToolCallID: "provider-call-unknown"}}
	if _, err = canonicalRuntimeRequest(unknownResult, nil); errorKind(err) != foundation.ErrorInvalidInput || errorCode(err) != agentRuntimeRequestCode {
		t.Fatalf("unknown result error=%v kind=%s code=%q", err, errorKind(err), errorCode(err))
	}
	duplicateDeclaration := responseForID("provider-call-duplicate")
	duplicateDeclaration.ToolCalls = append(duplicateDeclaration.ToolCalls, duplicateDeclaration.ToolCalls[0])
	if _, err = canonicalRuntimeResponse(duplicateDeclaration); errorKind(err) != foundation.ErrorConsistencyViolation || errorCode(err) != agentRuntimeOutputCode {
		t.Fatalf("duplicate declaration error=%v kind=%s code=%q", err, errorKind(err), errorCode(err))
	}
}

func TestCanonicalRuntimeRequestUsesProjectOwnedToolProjection(t *testing.T) {
	toolInfo := func(inputSchema string) *schema.ToolInfo {
		t.Helper()
		info, err := (&executionTool{spec: agentapplication.AgentToolSpec{
			Ref: toolsdomain.ToolRef{Name: "ReadSource", Version: 2}, Name: "ReadSource",
			Description: "Read approved source", InputSchema: []byte(inputSchema),
		}}).Info(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return info
	}
	messages := []*schema.Message{schema.UserMessage("Use frozen evidence.")}
	leftTool := toolInfo(`{"type":"object","required":["source_id"],"properties":{"source_id":{"type":"string"}}}`)
	rightTool := toolInfo(`{"properties":{"source_id":{"type":"string"}},"required":["source_id"],"type":"object"}`)
	left, err := canonicalRuntimeRequest(messages, []einomodel.Option{
		einomodel.WithTools([]*schema.ToolInfo{leftTool}),
		einomodel.WithToolChoice(schema.ToolChoiceAllowed, "ReadSource"),
	})
	if err != nil {
		t.Fatal(err)
	}
	right, err := canonicalRuntimeRequest(messages, []einomodel.Option{
		einomodel.WithTools([]*schema.ToolInfo{rightTool}),
		einomodel.WithToolChoice(schema.ToolChoiceAllowed, "ReadSource"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(left) != string(right) || !strings.Contains(string(left), `"description":"Read approved source"`) ||
		!strings.Contains(string(left), `"parameters":{`) || strings.Contains(string(left), `"desc":`) ||
		strings.Contains(string(left), `"has_params_one_of":`) || strings.Contains(string(left), `"extra":`) {
		t.Fatalf("left=%s right=%s", left, right)
	}

	leftTool.Extra = map[string]any{"provider-private": "must not be silently omitted"}
	if _, err = canonicalRuntimeRequest(messages, []einomodel.Option{einomodel.WithTools([]*schema.ToolInfo{leftTool})}); errorKind(err) != foundation.ErrorInvalidInput || errorCode(err) != agentRuntimeRequestCode {
		t.Fatalf("tool extra error=%v kind=%s code=%q", err, errorKind(err), errorCode(err))
	}
	if _, err = canonicalRuntimeRequest(messages, []einomodel.Option{einomodel.WithModel("call-time-override")}); errorKind(err) != foundation.ErrorInvalidInput || errorCode(err) != agentRuntimeRequestCode {
		t.Fatalf("model override error=%v kind=%s code=%q", err, errorKind(err), errorCode(err))
	}
	invalidChoice := schema.ToolChoice("provider-specific-choice")
	if _, err = canonicalRuntimeRequest(messages, []einomodel.Option{einomodel.WithToolChoice(invalidChoice)}); errorKind(err) != foundation.ErrorInvalidInput || errorCode(err) != agentRuntimeRequestCode {
		t.Fatalf("tool choice error=%v kind=%s code=%q", err, errorKind(err), errorCode(err))
	}
}

type scriptedRuntimeModel struct {
	mu       sync.Mutex
	generate []func([]*schema.Message, ...einomodel.Option) (*schema.Message, error)
	stream   func([]*schema.Message, ...einomodel.Option) (*schema.StreamReader[*schema.Message], error)
}

func (model *scriptedRuntimeModel) Generate(_ context.Context, messages []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	model.mu.Lock()
	defer model.mu.Unlock()
	if len(model.generate) == 0 {
		return nil, errors.New("unexpected generate")
	}
	step := model.generate[0]
	model.generate = model.generate[1:]
	return step(messages, opts...)
}

func (model *scriptedRuntimeModel) Stream(_ context.Context, messages []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	if model.stream == nil {
		return nil, errors.New("unexpected stream")
	}
	return model.stream(messages, opts...)
}

func (model *scriptedRuntimeModel) WithTools([]*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	return model, nil
}

type runtimeToolInvoker struct {
	calls   int
	last    agentapplication.AgentToolInvocation
	history []agentapplication.AgentToolInvocation
}

func (invoker *runtimeToolInvoker) Invoke(_ context.Context, call agentapplication.AgentToolInvocation) (agentapplication.AgentToolResult, error) {
	invoker.calls++
	invoker.last = call
	invoker.history = append(invoker.history, call)
	return agentapplication.AgentToolResult{Output: []byte(`{"excerpt":"approved"}`)}, nil
}

type contextCheckingRuntimeToolInvoker struct {
	runtimeToolInvoker
	check      func(context.Context) error
	contextErr error
}

func (invoker *contextCheckingRuntimeToolInvoker) Invoke(ctx context.Context, call agentapplication.AgentToolInvocation) (agentapplication.AgentToolResult, error) {
	if invoker.contextErr == nil && invoker.check != nil {
		invoker.contextErr = invoker.check(ctx)
	}
	return invoker.runtimeToolInvoker.Invoke(ctx, call)
}

type degradingRuntimeSink struct{ calls int }

func (sink *degradingRuntimeSink) Append(context.Context, agentapplication.AnswerStreamChunk) error {
	sink.calls++
	return errors.New("draft store unavailable")
}

type collectingRuntimeSink struct{ chunks []string }

func (sink *collectingRuntimeSink) Append(_ context.Context, chunk agentapplication.AnswerStreamChunk) error {
	sink.chunks = append(sink.chunks, chunk.Content)
	return nil
}

type runtimeRecordingRepository struct {
	mu    sync.Mutex
	calls []agentdomain.ModelCall
}

func (repository *runtimeRecordingRepository) CreateModelRun(context.Context, agentdomain.ModelRun) (agentdomain.ModelRun, bool, error) {
	return agentdomain.ModelRun{}, false, errors.New("unused")
}
func (repository *runtimeRecordingRepository) GetModelRun(context.Context, foundation.ID, foundation.ID) (agentapplication.ModelRunRecord, error) {
	return agentapplication.ModelRunRecord{}, errors.New("unused")
}
func (repository *runtimeRecordingRepository) StartModelCall(_ context.Context, _ foundation.ID, call agentdomain.ModelCall) (agentdomain.ModelCall, bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.calls = append(repository.calls, call)
	return call, false, nil
}
func (repository *runtimeRecordingRepository) CompleteModelCall(_ context.Context, command agentapplication.CompleteModelCallCommand) (agentdomain.ModelCall, bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	for index := range repository.calls {
		if repository.calls[index].ID == command.Call.ID {
			repository.calls[index] = command.Call
			return command.Call, false, nil
		}
	}
	return agentdomain.ModelCall{}, false, errors.New("call not started")
}
func (repository *runtimeRecordingRepository) FinalizeModelRun(context.Context, agentapplication.FinalizeModelRunCommand) (agentdomain.ModelRun, bool, error) {
	return agentdomain.ModelRun{}, false, errors.New("unused")
}
func (repository *runtimeRecordingRepository) MarkStaleModelCallsUnknown(context.Context, agentapplication.UnknownRecoveryQuery) ([]agentdomain.ModelCall, error) {
	return nil, errors.New("unused")
}
func (repository *runtimeRecordingRepository) MarkStaleModelRunsUnknown(context.Context, agentapplication.UnknownRecoveryQuery) ([]agentdomain.ModelRun, error) {
	return nil, errors.New("unused")
}

type runtimeIDs struct{ next int }

func (ids *runtimeIDs) New() (foundation.ID, error) {
	value := foundation.ID(fmt.Sprintf("99000000-0000-4000-8000-%012d", ids.next))
	ids.next++
	return foundation.ParseID(string(value))
}

func newRuntimeTestLedgerAndRecorder(t *testing.T, repository *runtimeRecordingRepository, calls, iterations, tools int) (*agentapplication.RunBudgetLedger, *agentapplication.ModelCallRecorder) {
	t.Helper()
	now := time.Date(2026, 8, 9, 8, 0, 0, 0, time.UTC)
	reservations := map[agentapplication.RunBudgetPhase]agentapplication.RunBudgetReservation{}
	for _, phase := range []agentapplication.RunBudgetPhase{
		agentapplication.RunBudgetPhaseAnswer, agentapplication.RunBudgetPhaseInitial, agentapplication.RunBudgetPhaseRepair,
		agentapplication.RunBudgetPhaseReduced, agentapplication.RunBudgetPhaseReview,
	} {
		reservations[phase] = agentapplication.RunBudgetReservation{ModelCalls: 1, ReservedInputTokens: 10, ReservedOutputTokens: 10}
	}
	ledger, err := agentapplication.NewRunBudgetLedger(agentapplication.RunBudgetLedgerConfig{
		NodeAttemptID: "99000000-0000-4000-8000-000000000001", ModelRunID: "99000000-0000-4000-8000-000000000002",
		MaxTotalModelCalls: calls, MaxAgentIterations: iterations, MaxToolCalls: tools,
		MaxInputTokens: int64(calls * 10), MaxOutputTokens: int64(calls * 10), Deadline: now.Add(time.Minute),
		Clock: foundation.FixedClock{Value: now}, DownstreamReservations: reservations,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := agentapplication.NewModelCallRecorder(agentapplication.ModelCallRecorderDependencies{
		Repository: repository, WorkspaceID: "99000000-0000-4000-8000-000000000003", ModelRunID: "99000000-0000-4000-8000-000000000002",
		IDs: &runtimeIDs{next: 10}, Clock: foundation.FixedClock{Value: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	return ledger, recorder
}

func runtimeTestModelRef() agentdomain.ModelRef {
	return agentdomain.ModelRef{AdapterName: "eino-openai", AdapterVersion: "v1", ModelID: "chat", ModelVersion: "chat-v1"}
}
func runtimeTestProfileRef() agentdomain.ModelProfileRef {
	return agentdomain.ModelProfileRef{ID: "profile", Version: "v1"}
}
func runtimeTestPromptRef() agentdomain.PromptRef {
	return agentdomain.PromptRef{ID: "rag-agent", Version: "v1"}
}
func runtimeTestSchemaRef() agentdomain.SchemaRef {
	return agentdomain.SchemaRef{ID: agentdomain.RAGAnswerSchemaID, Version: agentdomain.OutputSchemaVersionV2}
}

var _ einomodel.ToolCallingChatModel = (*scriptedRuntimeModel)(nil)
var _ agentapplication.AgentToolInvoker = (*runtimeToolInvoker)(nil)
