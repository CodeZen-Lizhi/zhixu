package postgres

import (
	"errors"
	"math"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

func validateRefusedCommand(command application.RecordRefusedCommand) (domain.ToolCall, error) {
	if err := validateIdentityCallBinding(command.Identity, command.Call); err != nil {
		return domain.ToolCall{}, err
	}
	call := command.Call
	if call.Status != domain.CallRefused || call.Version != 1 || call.IdempotencyKey != "" || call.Retryable {
		return domain.ToolCall{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeCallInvalid, false, errors.New("refused tool call must be version one without execution idempotency"))
	}
	now := time.Unix(1, 0).UTC()
	call.StartedAt, call.CompletedAt, call.DurationMillis = now, &now, 0
	if err := domain.ValidateToolCall(call); err != nil {
		return domain.ToolCall{}, err
	}
	return call, nil
}

func validateStartCommand(command application.StartCallCommand) (domain.ToolCall, error) {
	if err := validateIdentityCallBinding(command.Identity, command.Call); err != nil {
		return domain.ToolCall{}, err
	}
	call := command.Call
	if call.Status != domain.CallStarted || call.Version != 1 || call.Tool == nil ||
		(call.InvocationPolicy == domain.InvocationModelRequestable &&
			(call.SideEffectLevel == domain.SideEffectDomainWrite || call.SideEffectLevel == domain.SideEffectUnknown)) ||
		((call.SideEffectLevel == domain.SideEffectDomainWrite || call.SideEffectLevel == domain.SideEffectUnknown) && call.IdempotencyKey == "") {
		return domain.ToolCall{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeCallInvalid, false, errors.New("started tool call policy is invalid"))
	}
	call.StartedAt, call.CompletedAt, call.DurationMillis = time.Unix(1, 0).UTC(), nil, 0
	if err := domain.ValidateToolCall(call); err != nil {
		return domain.ToolCall{}, err
	}
	return call, nil
}

func validateTerminalCommand(expectedVersion int64, input domain.ToolCall, unknown bool) (domain.ToolCall, error) {
	if expectedVersion < 1 || input.Version != expectedVersion+1 ||
		(unknown && input.Status != domain.CallUnknown) || (!unknown && input.Status != domain.CallSucceeded && input.Status != domain.CallFailed) {
		return domain.ToolCall{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeCallInvalid, false, errors.New("tool call terminal command is invalid"))
	}
	call := input
	started := time.Unix(1, 0).UTC()
	completed := started.Add(time.Millisecond)
	call.StartedAt, call.CompletedAt, call.DurationMillis = started, &completed, 1
	if err := domain.ValidateToolCall(call); err != nil {
		return domain.ToolCall{}, err
	}
	if err := domain.ValidateToolCallTransition(domain.CallStarted, call.Status); err != nil {
		return domain.ToolCall{}, err
	}
	return call, nil
}

func validateIdentityCallBinding(identity domain.TrustedExecutionIdentity, call domain.ToolCall) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	if call.WorkspaceID != identity.WorkspaceID || call.WorkflowRunID != identity.WorkflowRunID ||
		call.NodeRunID != identity.NodeRunID || call.NodeAttemptID != identity.NodeAttemptID || call.CallNo < 1 || call.CallNo > math.MaxInt16 {
		return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeCallInvalid, false, errors.New("tool call does not match trusted execution identity"))
	}
	return nil
}

func timelineLess(left, right domain.ToolCall) bool {
	if !left.StartedAt.Equal(right.StartedAt) {
		return left.StartedAt.Before(right.StartedAt)
	}
	if left.CallNo != right.CallNo {
		return left.CallNo < right.CallNo
	}
	return string(left.ID) < string(right.ID)
}
