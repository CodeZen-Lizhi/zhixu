package postgres

import (
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

func validateFinalizeCallWithReceiptFailureCommand(
	command application.FinalizeCallWithReceiptFailureCommand,
) (domain.ToolCall, error) {
	if err := validateIdentityCallBinding(command.Identity, command.Call); err != nil {
		return domain.ToolCall{}, err
	}
	call, err := validateTerminalCommand(command.ExpectedVersion, command.Call, false)
	if err != nil {
		return domain.ToolCall{}, err
	}
	if call.Status != domain.CallSucceeded || call.CompletedAt == nil || !validID(command.Failure.ID) ||
		!validID(command.Failure.OperationID) || !validID(command.Failure.AnalysisRunID) {
		return domain.ToolCall{}, foundation.NewError(
			foundation.ErrorInvalidInput,
			domain.ErrorCodeResultReceiptFailureInvalid,
			false,
			errors.New("result receipt failure completion command is invalid"),
		)
	}
	return call, nil
}

func resultReceiptFailureAtDatabaseCompletion(
	command application.FinalizeCallWithReceiptFailureCommand,
	locked receiptCompletionLocks,
	call domain.ToolCall,
) (domain.ResultReceiptFailure, error) {
	if call.CompletedAt == nil {
		return domain.ResultReceiptFailure{}, consistency(errors.New("successful receipt failure call has no database completion time"))
	}
	if command.Failure.OperationID != locked.operation.id || command.Failure.AnalysisRunID != locked.analysisRun.id {
		return domain.ResultReceiptFailure{}, receiptConflict(errors.New("workspace analysis receipt failure operation binding differs"))
	}
	at := call.CompletedAt.UTC().Truncate(time.Microsecond)
	return domain.NewResultReceiptFailure(domain.ResultReceiptFailureDraft{
		ID:                   command.Failure.ID,
		OperationID:          locked.operation.id,
		AnalysisRunID:        locked.analysisRun.id,
		FailureCode:          command.Failure.FailureCode,
		ObservedOutputHash:   command.Failure.ObservedOutputHash,
		ObservedOutputBytes:  cloneOptionalInt64(command.Failure.ObservedOutputBytes),
		ObservedBindingHash:  command.Failure.ObservedBindingHash,
		ObservedBindingBytes: cloneOptionalInt64(command.Failure.ObservedBindingBytes),
		CheckedAt:            at,
		CreatedAt:            at,
	}, call, command.Definition)
}

func sameRequestedResultReceiptFailure(existing, requested domain.ResultReceiptFailure) bool {
	return existing.ToolCallID == requested.ToolCallID && existing.OperationID == requested.OperationID &&
		existing.AnalysisRunID == requested.AnalysisRunID &&
		existing.WorkspaceID == requested.WorkspaceID && existing.WorkflowRunID == requested.WorkflowRunID &&
		existing.NodeRunID == requested.NodeRunID && existing.NodeAttemptID == requested.NodeAttemptID &&
		existing.FailureCode == requested.FailureCode && existing.ExpectedOutputHash == requested.ExpectedOutputHash &&
		existing.ObservedOutputHash == requested.ObservedOutputHash &&
		optionalInt64Equal(existing.ObservedOutputBytes, requested.ObservedOutputBytes) &&
		existing.ObservedBindingHash == requested.ObservedBindingHash &&
		optionalInt64Equal(existing.ObservedBindingBytes, requested.ObservedBindingBytes) &&
		existing.ValidatorVersion == requested.ValidatorVersion
}

func cloneOptionalInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func optionalInt64Equal(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
