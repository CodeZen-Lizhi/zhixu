package postgres

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

type receiptExecutionFence struct {
	workflowDefinitionID foundation.ID
	workflowStatus       string
	databaseNow          time.Time
	nodeKey              string
	nodeStatus           string
	nodeAttempt          int64
	nodeOwner            *string
	nodeLease            *time.Time
	attemptStatus        string
	attemptNo            int64
	attemptOwner         *string
	attemptLease         *time.Time
}

type receiptAnalysisRun struct {
	id                foundation.ID
	status            string
	definitionVersion int64
	definitionHash    string
	version           int64
}

type receiptOperation struct {
	id, workspaceID, analysisRunID, workflowRunID, nodeRunID foundation.ID
	nodeKey, callKind, requestHash, status                   string
	version                                                  int64
	firstAttemptID, latestAttemptID, toolCallID              *string
	resultKind, resultID, resultHash                         *string
	errorCode                                                *string
}

type receiptReservation struct {
	id, workspaceID, analysisRunID, operationID                foundation.ID
	callKind, status                                           string
	toolCallID                                                 *string
	reservedModelCalls, reservedToolCalls, reservedSourceReads int64
	reservedInputTokens, reservedOutputTokens                  int64
}

type receiptCompletionLocks struct {
	fence       receiptExecutionFence
	analysisRun receiptAnalysisRun
	operation   receiptOperation
	reservation receiptReservation
	call        domain.ToolCall
}

func validateFinalizeCallWithReceiptCommand(
	command application.FinalizeCallWithReceiptCommand,
) (domain.ToolCall, domain.ResultReceipt, error) {
	if err := validateIdentityCallBinding(command.Identity, command.Call); err != nil {
		return domain.ToolCall{}, domain.ResultReceipt{}, err
	}
	call, err := validateTerminalCommand(command.ExpectedVersion, command.Call, false)
	if err != nil {
		return domain.ToolCall{}, domain.ResultReceipt{}, err
	}
	if call.Status != domain.CallSucceeded || call.CompletedAt == nil || !validID(command.ReceiptID) {
		return domain.ToolCall{}, domain.ResultReceipt{}, foundation.NewError(
			foundation.ErrorInvalidInput,
			domain.ErrorCodeResultReceiptInvalid,
			false,
			errors.New("result receipt completion command is invalid"),
		)
	}
	draft := domain.ResultReceiptDraft{
		ID: command.ReceiptID, Output: append(json.RawMessage(nil), command.Output...), CreatedAt: *call.CompletedAt,
	}
	if command.PrivateBinding != nil {
		draft.PrivateBindingSchema = command.PrivateBinding.Schema
		draft.PrivateBinding = append(json.RawMessage(nil), command.PrivateBinding.Document...)
	}
	receipt, err := domain.NewResultReceipt(draft, call, command.Definition)
	if err != nil {
		return domain.ToolCall{}, domain.ResultReceipt{}, err
	}
	return call, receipt, nil
}

func activeReceiptExecutionFence(
	locked receiptCompletionLocks,
	call domain.ToolCall,
	identity domain.TrustedExecutionIdentity,
) bool {
	return locked.fence.workflowStatus == "running" && locked.analysisRun.status == "running" &&
		receiptExecutionIdentityMatches(locked, call, identity) && locked.fence.nodeStatus == "running" &&
		locked.fence.attemptStatus == "running" && locked.fence.nodeAttempt == locked.fence.attemptNo &&
		locked.fence.attemptNo == identity.LeaseFence && stringPointerValue(locked.operation.latestAttemptID) == string(identity.NodeAttemptID) &&
		canonicalActiveLease(locked.fence.nodeOwner, locked.fence.nodeLease, locked.fence.attemptOwner, locked.fence.attemptLease,
			identity.LeaseOwner, locked.fence.databaseNow)
}

func receiptExecutionIdentityMatches(
	locked receiptCompletionLocks,
	call domain.ToolCall,
	identity domain.TrustedExecutionIdentity,
) bool {
	return identity.DefinitionID == locked.fence.workflowDefinitionID &&
		identity.DefinitionVersion == locked.analysisRun.definitionVersion && identity.DefinitionHash == locked.analysisRun.definitionHash &&
		identity.WorkspaceID == call.WorkspaceID && identity.WorkflowRunID == call.WorkflowRunID &&
		identity.NodeRunID == call.NodeRunID && identity.NodeAttemptID == call.NodeAttemptID &&
		identity.NodeKey == locked.operation.nodeKey && locked.fence.nodeKey == identity.NodeKey
}

func canonicalActiveLease(
	nodeOwner *string,
	nodeLease *time.Time,
	attemptOwner *string,
	attemptLease *time.Time,
	expectedOwner string,
	databaseNow time.Time,
) bool {
	if nodeOwner == nil || attemptOwner == nil || nodeLease == nil || attemptLease == nil ||
		*nodeOwner == "" || *nodeOwner != strings.TrimSpace(*nodeOwner) || *nodeOwner != *attemptOwner || *nodeOwner != expectedOwner {
		return false
	}
	return nodeLease.Equal(*attemptLease) && attemptLease.After(databaseNow)
}

func resultReceiptAtDatabaseCompletion(
	requested domain.ResultReceipt,
	call domain.ToolCall,
	definition domain.Definition,
) (domain.ResultReceipt, error) {
	if call.CompletedAt == nil {
		return domain.ResultReceipt{}, consistency(errors.New("successful receipt call has no database completion time"))
	}
	draft := domain.ResultReceiptDraft{ID: requested.ID, Output: requested.Output, CreatedAt: *call.CompletedAt}
	if requested.PrivateBinding != nil {
		draft.PrivateBindingSchema = requested.PrivateBinding.Schema
		draft.PrivateBinding = requested.PrivateBinding.Document
	}
	return domain.NewResultReceipt(draft, call, definition)
}

func sameRequestedResultReceipt(existing, requested domain.ResultReceipt) bool {
	if existing.ID != requested.ID || existing.ToolCallID != requested.ToolCallID || existing.Tool != requested.Tool ||
		existing.OutputSchema != requested.OutputSchema || existing.DefinitionHash != requested.DefinitionHash ||
		!bytes.Equal(existing.Output, requested.Output) {
		return false
	}
	if existing.PrivateBinding == nil || requested.PrivateBinding == nil {
		return existing.PrivateBinding == nil && requested.PrivateBinding == nil
	}
	return existing.PrivateBinding.Schema == requested.PrivateBinding.Schema &&
		bytes.Equal(existing.PrivateBinding.Document, requested.PrivateBinding.Document)
}

func stringPointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
