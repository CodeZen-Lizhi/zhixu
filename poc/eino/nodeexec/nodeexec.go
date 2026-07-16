// Package nodeexec wraps a short AI flow as a project-owned workflow node contract.
package nodeexec

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/poc/eino/contract"
)

const operationExecute = "eino_node.execute"

// Input contains durable workflow correlation and an opaque short-flow payload.
type Input struct {
	WorkflowRunID string
	NodeRunID     string
	Payload       json.RawMessage
}

// Output contains only project-owned node output data.
type Output struct{ Payload json.RawMessage }

// Flow is the replaceable short-running implementation behind the durable node.
type Flow interface {
	Run(ctx context.Context, input Input) (Output, error)
}

// Executor validates the workflow boundary and classifies flow errors.
type Executor struct{ flow Flow }

// New creates a node executor around one short flow.
func New(flow Flow) (*Executor, error) {
	if flow == nil {
		return nil, invalidInput(errors.New("flow is required"))
	}
	return &Executor{flow: flow}, nil
}

// Execute runs a short flow without treating the framework graph as workflow state.
func (e *Executor) Execute(ctx context.Context, input Input) (Output, error) {
	if e == nil || e.flow == nil {
		return Output{}, nonRetryable(errors.New("node executor is not initialized"))
	}
	if strings.TrimSpace(input.WorkflowRunID) == "" {
		return Output{}, invalidInput(errors.New("workflow run id is required"))
	}
	if strings.TrimSpace(input.NodeRunID) == "" {
		return Output{}, invalidInput(errors.New("node run id is required"))
	}
	if !json.Valid(input.Payload) {
		return Output{}, invalidInput(errors.New("payload must be valid JSON"))
	}
	if err := ctx.Err(); err != nil {
		return Output{}, classify(err)
	}
	output, err := e.flow.Run(ctx, Input{WorkflowRunID: input.WorkflowRunID, NodeRunID: input.NodeRunID, Payload: append(json.RawMessage(nil), input.Payload...)})
	if err != nil {
		return Output{}, classify(err)
	}
	if !json.Valid(output.Payload) {
		return Output{}, nonRetryable(errors.New("flow output must be valid JSON"))
	}
	output.Payload = append(json.RawMessage(nil), output.Payload...)
	return output, nil
}

func classify(err error) error {
	var classified *contract.Error
	if errors.As(err, &classified) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return nonRetryable(err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return retryable(err)
	}
	return nonRetryable(err)
}

func invalidInput(cause error) error {
	return &contract.Error{Kind: contract.ErrorInvalidInput, Operation: operationExecute, Cause: cause}
}
func retryable(cause error) error {
	return &contract.Error{Kind: contract.ErrorRetryableFailure, Operation: operationExecute, Retryable: true, Cause: cause}
}
func nonRetryable(cause error) error {
	return &contract.Error{Kind: contract.ErrorNonRetryableFailure, Operation: operationExecute, Cause: cause}
}
