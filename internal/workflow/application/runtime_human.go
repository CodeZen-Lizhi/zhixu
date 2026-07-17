package application

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

// RuntimeHumanStatePort owns DB-time Human Task wait and submission transactions.
type RuntimeHumanStatePort interface {
	WaitForHuman(context.Context, HumanWaitTransition) (HumanTransitionResult, error)
	SubmitHuman(context.Context, HumanDecisionTransition) (HumanTransitionResult, error)
}

// HumanWaitCommand binds task creation to the active lease and a relative expiry.
type HumanWaitCommand struct {
	TaskID              foundation.ID
	RunID               foundation.ID
	NodeRunID           foundation.ID
	Fence               domain.LeaseFence
	ExpectedInputSchema json.RawMessage
	TargetVersion       int64
	ExpiresIn           time.Duration
}

// HumanWaitTransition is the validated persistence command.
type HumanWaitTransition = HumanWaitCommand

// HumanDecisionCommand completes one Human Node without accepting Workspace input.
type HumanDecisionCommand struct {
	RunID         foundation.ID
	TaskID        foundation.ID
	TargetVersion int64
	Decision      json.RawMessage
}

// HumanDecisionTransition carries canonical decision JSON to persistence.
type HumanDecisionTransition = HumanDecisionCommand

// HumanTransitionResult returns the durable Human and Workflow projections.
type HumanTransitionResult struct {
	Task    domain.HumanTask
	Run     domain.Run
	Node    domain.NodeRun
	Attempt domain.NodeAttempt
}

// RuntimeHumanCoordinator validates Human contracts outside adapters.
type RuntimeHumanCoordinator struct{ state RuntimeHumanStatePort }

// NewRuntimeHumanCoordinator constructs the Human state coordinator.
func NewRuntimeHumanCoordinator(state RuntimeHumanStatePort) (*RuntimeHumanCoordinator, error) {
	if isNilRuntimeHumanStatePort(state) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_HUMAN_STATE_PORT_MISSING", false, errors.New("workflow human state port is missing"))
	}
	return &RuntimeHumanCoordinator{state: state}, nil
}

// WaitForHuman releases the active lease and creates one durable Human Task.
func (c *RuntimeHumanCoordinator) WaitForHuman(ctx context.Context, command HumanWaitCommand) (HumanTransitionResult, error) {
	if command.TaskID == "" || command.RunID == "" || command.NodeRunID == "" || command.Fence.Owner == "" || command.Fence.AttemptNo < 1 || command.Fence.NodeVersion < 1 || command.TargetVersion < 1 || command.ExpiresIn <= 0 || !objectJSON(command.ExpectedInputSchema) {
		return HumanTransitionResult{}, invalid("HUMAN_TASK_INVALID")
	}
	return c.state.WaitForHuman(ctx, command)
}

// SubmitHuman canonicalizes a decision and completes the Human Node once.
func (c *RuntimeHumanCoordinator) SubmitHuman(ctx context.Context, command HumanDecisionCommand) (HumanTransitionResult, error) {
	if command.RunID == "" || command.TaskID == "" || command.TargetVersion < 1 || !objectJSON(command.Decision) {
		return HumanTransitionResult{}, invalid("HUMAN_DECISION_INVALID")
	}
	canonical, err := canonicalRuntimeInput(command.Decision)
	if err != nil {
		return HumanTransitionResult{}, invalid("HUMAN_DECISION_INVALID")
	}
	command.Decision = canonical
	result, err := c.state.SubmitHuman(ctx, command)
	if err != nil {
		return HumanTransitionResult{}, err
	}
	if result.Task.ID != command.TaskID || result.Task.RunID != command.RunID || result.Task.Status != domain.HumanTaskSubmitted || result.Node.RunID != command.RunID || result.Node.Status != domain.NodeStatusSucceeded || result.Run.ID != command.RunID {
		return HumanTransitionResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "HUMAN_DECISION_RESULT_INVALID", false, errors.New("human decision result binding is invalid"))
	}
	return result, nil
}

func isNilRuntimeHumanStatePort(state RuntimeHumanStatePort) bool {
	if state == nil {
		return true
	}
	value := reflect.ValueOf(state)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
