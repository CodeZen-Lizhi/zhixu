package chatgraph_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/poc/eino/chatgraph"
	"github.com/CodeZen-Lizhi/zhixu/poc/eino/contract"
	"github.com/CodeZen-Lizhi/zhixu/poc/eino/fake"
	"github.com/cloudwego/eino/schema"
)

func TestRunnerRunSuccess(t *testing.T) {
	model := fake.NewChatModel(fake.ChatBehavior{Response: "eino-ready"})
	runner, err := chatgraph.New(context.Background(), model)
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}

	response, err := runner.Run(context.Background(), contract.ChatRequest{
		Messages: []contract.Message{
			{Role: contract.RoleSystem, Content: "Reply exactly."},
			{Role: contract.RoleUser, Content: "eino-ready"},
		},
	})
	if err != nil {
		t.Fatalf("run graph: %v", err)
	}
	if response.Message.Role != contract.RoleAssistant {
		t.Fatalf("response role = %q, want %q", response.Message.Role, contract.RoleAssistant)
	}
	if response.Message.Content != "eino-ready" {
		t.Fatalf("response content = %q, want %q", response.Message.Content, "eino-ready")
	}
	if model.CallCount() != 1 {
		t.Fatalf("model call count = %d, want 1", model.CallCount())
	}

	input := model.LastInput()
	if len(input) != 2 {
		t.Fatalf("model input length = %d, want 2", len(input))
	}
	if input[0].Role != schema.System || input[1].Role != schema.User {
		t.Fatalf("model roles = [%q, %q], want [%q, %q]", input[0].Role, input[1].Role, schema.System, schema.User)
	}
}

func TestRunnerRunMapsModelFailure(t *testing.T) {
	modelFailure := errors.New("provider unavailable")
	model := fake.NewChatModel(fake.ChatBehavior{Err: modelFailure})
	runner, err := chatgraph.New(context.Background(), model)
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}

	_, err = runner.Run(context.Background(), contract.ChatRequest{
		Messages: []contract.Message{{Role: contract.RoleUser, Content: "hello"}},
	})
	if err == nil {
		t.Fatal("run graph succeeded, want failure")
	}
	if !errors.Is(err, modelFailure) {
		t.Fatalf("error = %v, want wrapped model failure", err)
	}

	var classified *contract.Error
	if !errors.As(err, &classified) {
		t.Fatalf("error type = %T, want *contract.Error", err)
	}
	if classified.Kind != contract.ErrorNonRetryableFailure {
		t.Fatalf("error kind = %q, want %q", classified.Kind, contract.ErrorNonRetryableFailure)
	}
	if classified.Retryable {
		t.Fatal("unknown provider failure is marked retryable")
	}
}

func TestRunnerRunRejectsUnexpectedModelRole(t *testing.T) {
	model := fake.NewChatModel(fake.ChatBehavior{
		Response:     "not an assistant response",
		ResponseRole: schema.Tool,
	})
	runner, err := chatgraph.New(context.Background(), model)
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}

	_, err = runner.Run(context.Background(), contract.ChatRequest{
		Messages: []contract.Message{{Role: contract.RoleUser, Content: "hello"}},
	})
	if err == nil {
		t.Fatal("run graph succeeded, want unsupported response role failure")
	}

	var classified *contract.Error
	if !errors.As(err, &classified) {
		t.Fatalf("error type = %T, want *contract.Error", err)
	}
	if classified.Kind != contract.ErrorNonRetryableFailure {
		t.Fatalf("error kind = %q, want %q", classified.Kind, contract.ErrorNonRetryableFailure)
	}
}

func TestRunnerRunPropagatesCancellation(t *testing.T) {
	model := fake.NewChatModel(fake.ChatBehavior{WaitForCancel: true})
	runner, err := chatgraph.New(context.Background(), model)
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, runErr := runner.Run(ctx, contract.ChatRequest{
			Messages: []contract.Message{{Role: contract.RoleUser, Content: "wait"}},
		})
		result <- runErr
	}()

	select {
	case <-model.Started():
	case <-time.After(time.Second):
		t.Fatal("model did not start")
	}
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		var classified *contract.Error
		if !errors.As(err, &classified) {
			t.Fatalf("error type = %T, want *contract.Error", err)
		}
		if classified.Kind != contract.ErrorNonRetryableFailure {
			t.Fatalf("error kind = %q, want %q", classified.Kind, contract.ErrorNonRetryableFailure)
		}
		if classified.Retryable {
			t.Fatal("caller cancellation is marked retryable")
		}
	case <-time.After(time.Second):
		t.Fatal("runner did not stop after context cancellation")
	}
}

func TestRunnerRunRejectsInvalidInputBeforeModelCall(t *testing.T) {
	model := fake.NewChatModel(fake.ChatBehavior{Response: "unused"})
	runner, err := chatgraph.New(context.Background(), model)
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}

	_, err = runner.Run(context.Background(), contract.ChatRequest{})
	if err == nil {
		t.Fatal("run graph succeeded, want invalid input")
	}
	var classified *contract.Error
	if !errors.As(err, &classified) {
		t.Fatalf("error type = %T, want *contract.Error", err)
	}
	if classified.Kind != contract.ErrorInvalidInput {
		t.Fatalf("error kind = %q, want %q", classified.Kind, contract.ErrorInvalidInput)
	}
	if model.CallCount() != 0 {
		t.Fatalf("model call count = %d, want 0", model.CallCount())
	}
}
