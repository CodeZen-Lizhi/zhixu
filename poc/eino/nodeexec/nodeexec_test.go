package nodeexec

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/poc/eino/contract"
)

type flowFunc func(context.Context, Input) (Output, error)

func (f flowFunc) Run(ctx context.Context, input Input) (Output, error) { return f(ctx, input) }

func TestExecutorSuccess(t *testing.T) {
	executor, _ := New(flowFunc(func(_ context.Context, input Input) (Output, error) {
		if input.WorkflowRunID != "workflow-1" || input.NodeRunID != "node-1" {
			t.Fatalf("input=%#v", input)
		}
		return Output{Payload: []byte(`{"answer":"ok"}`)}, nil
	}))
	output, err := executor.Execute(context.Background(), Input{WorkflowRunID: "workflow-1", NodeRunID: "node-1", Payload: []byte(`{"question":"ready"}`)})
	if err != nil || string(output.Payload) != `{"answer":"ok"}` {
		t.Fatalf("output=%s err=%v", output.Payload, err)
	}
}

func TestExecutorPreservesRetryableError(t *testing.T) {
	want := &contract.Error{Kind: contract.ErrorRetryableFailure, Operation: "fake", Retryable: true, Cause: errors.New("rate limited")}
	executor, _ := New(flowFunc(func(context.Context, Input) (Output, error) { return Output{}, want }))
	_, err := executor.Execute(context.Background(), validInput())
	assertError(t, err, contract.ErrorRetryableFailure, true)
	if !errors.Is(err, want.Cause) {
		t.Fatalf("error=%v", err)
	}
}

func TestExecutorPreservesNonRetryableError(t *testing.T) {
	want := &contract.Error{Kind: contract.ErrorNonRetryableFailure, Operation: "fake", Cause: errors.New("schema rejected")}
	executor, _ := New(flowFunc(func(context.Context, Input) (Output, error) { return Output{}, want }))
	_, err := executor.Execute(context.Background(), validInput())
	assertError(t, err, contract.ErrorNonRetryableFailure, false)
}

func TestExecutorCancellation(t *testing.T) {
	started := make(chan struct{})
	executor, _ := New(flowFunc(func(ctx context.Context, _ Input) (Output, error) {
		close(started)
		<-ctx.Done()
		return Output{}, ctx.Err()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := executor.Execute(ctx, validInput()); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		assertError(t, err, contract.ErrorNonRetryableFailure, false)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("executor did not stop")
	}
}

func validInput() Input {
	return Input{WorkflowRunID: "workflow-1", NodeRunID: "node-1", Payload: []byte(`{}`)}
}
func assertError(t *testing.T, err error, kind contract.ErrorKind, retryable bool) {
	t.Helper()
	var classified *contract.Error
	if !errors.As(err, &classified) {
		t.Fatalf("error type=%T", err)
	}
	if classified.Kind != kind || classified.Retryable != retryable {
		t.Fatalf("error=%#v", classified)
	}
}
