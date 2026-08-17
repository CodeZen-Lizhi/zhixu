package domain

import (
	"fmt"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ResultReceiptFailureValidatorVersion 是持久失败事实采用的冻结校验器版本。
	ResultReceiptFailureValidatorVersion int64 = 1
	// ResultReceiptFailureMaxObservedOutputBytes 是被拒输出 observation 的最大可记录长度。
	ResultReceiptFailureMaxObservedOutputBytes int64 = 1024 * 1024
	// ResultReceiptFailureMaxObservedBindingBytes 是被拒私有绑定 observation 的最大可记录长度。
	ResultReceiptFailureMaxObservedBindingBytes int64 = 64 * 1024
)

// ResultReceiptFailureCode 是受信回执校验器输出的稳定失败类别。
type ResultReceiptFailureCode string

const (
	// ResultReceiptFailureMissing 表示成功 Call 没有产生可持久化回执。
	ResultReceiptFailureMissing ResultReceiptFailureCode = "MISSING"
	// ResultReceiptFailureHashMismatch 表示 observation 与成功 Call 的响应哈希不一致。
	ResultReceiptFailureHashMismatch ResultReceiptFailureCode = "HASH_MISMATCH"
	// ResultReceiptFailureContractInvalid 表示响应正文不满足 exact Tool 输出合同。
	ResultReceiptFailureContractInvalid ResultReceiptFailureCode = "CONTRACT_INVALID"
	// ResultReceiptFailureBindingInvalid 表示 server-only 身份绑定不满足 exact Tool 合同。
	ResultReceiptFailureBindingInvalid ResultReceiptFailureCode = "BINDING_INVALID"
)

// ResultReceiptFailureDraft 是受信校验器写入失败事实所需的最小 observation。
type ResultReceiptFailureDraft struct {
	ID                   foundation.ID
	OperationID          foundation.ID
	AnalysisRunID        foundation.ID
	FailureCode          ResultReceiptFailureCode
	ObservedOutputHash   string
	ObservedOutputBytes  *int64
	ObservedBindingHash  string
	ObservedBindingBytes *int64
	CheckedAt            time.Time
	CreatedAt            time.Time
}

// ResultReceiptFailure 是与一个成功 Tool Call 一对一绑定的不可变校验失败事实。
// 它只保存 observation 的哈希与长度，不保存可能含敏感信息的被拒文档。
type ResultReceiptFailure struct {
	ID                   foundation.ID
	ToolCallID           foundation.ID
	OperationID          foundation.ID
	AnalysisRunID        foundation.ID
	WorkspaceID          foundation.ID
	WorkflowRunID        foundation.ID
	NodeRunID            foundation.ID
	NodeAttemptID        foundation.ID
	FailureCode          ResultReceiptFailureCode
	ExpectedOutputHash   string
	ObservedOutputHash   string
	ObservedOutputBytes  *int64
	ObservedBindingHash  string
	ObservedBindingBytes *int64
	ValidatorVersion     int64
	CheckedAt            time.Time
	CreatedAt            time.Time
}

// String 仅返回身份、失败类别、哈希与长度，不返回被拒文档。
func (failure ResultReceiptFailure) String() string {
	return fmt.Sprintf(
		"ResultReceiptFailure{id:%s tool_call_id:%s operation_id:%s code:%s expected_output_hash:%s observed_output_hash:%s observed_output_bytes:%s observed_binding_hash:%s observed_binding_bytes:%s validator_version:%d}",
		failure.ID,
		failure.ToolCallID,
		failure.OperationID,
		failure.FailureCode,
		failure.ExpectedOutputHash,
		failure.ObservedOutputHash,
		resultReceiptFailureBytesString(failure.ObservedOutputBytes),
		failure.ObservedBindingHash,
		resultReceiptFailureBytesString(failure.ObservedBindingBytes),
		failure.ValidatorVersion,
	)
}

// GoString 避免 %#v 调试格式绕过安全 String 投影。
func (failure ResultReceiptFailure) GoString() string { return failure.String() }

// NewResultReceiptFailure 从权威成功 Call 派生不可变失败事实。
func NewResultReceiptFailure(
	draft ResultReceiptFailureDraft,
	call ToolCall,
	definition Definition,
) (ResultReceiptFailure, error) {
	if _, err := validateResultReceiptFailureAuthority(call, definition); err != nil {
		return ResultReceiptFailure{}, err
	}
	failure := ResultReceiptFailure{
		ID:                   draft.ID,
		ToolCallID:           call.ID,
		OperationID:          draft.OperationID,
		AnalysisRunID:        draft.AnalysisRunID,
		WorkspaceID:          call.WorkspaceID,
		WorkflowRunID:        call.WorkflowRunID,
		NodeRunID:            call.NodeRunID,
		NodeAttemptID:        call.NodeAttemptID,
		FailureCode:          draft.FailureCode,
		ExpectedOutputHash:   call.ResponseHash,
		ObservedOutputHash:   draft.ObservedOutputHash,
		ObservedOutputBytes:  cloneResultReceiptFailureBytes(draft.ObservedOutputBytes),
		ObservedBindingHash:  draft.ObservedBindingHash,
		ObservedBindingBytes: cloneResultReceiptFailureBytes(draft.ObservedBindingBytes),
		ValidatorVersion:     ResultReceiptFailureValidatorVersion,
		CheckedAt:            draft.CheckedAt.UTC().Truncate(time.Microsecond),
		CreatedAt:            draft.CreatedAt.UTC().Truncate(time.Microsecond),
	}
	if err := validateResultReceiptFailure(failure, call, definition, ErrorCodeResultReceiptFailureInvalid); err != nil {
		return ResultReceiptFailure{}, err
	}
	return failure, nil
}

// ValidateResultReceiptFailure 校验回放失败事实与权威成功 Call、Definition 逐项相等。
func ValidateResultReceiptFailure(failure ResultReceiptFailure, call ToolCall, definition Definition) error {
	if _, err := validateResultReceiptFailureAuthority(call, definition); err != nil {
		return err
	}
	return validateResultReceiptFailure(failure, call, definition, ErrorCodeResultReceiptFailureBindingConflict)
}

func validateResultReceiptFailureAuthority(call ToolCall, definition Definition) (ResultReceiptContract, error) {
	contract, err := validateResultReceiptAuthorityWithin(
		call,
		definition,
		ResultReceiptFailureMaxObservedOutputBytes,
	)
	if err != nil {
		return ResultReceiptContract{}, inconsistent(
			ErrorCodeResultReceiptFailureBindingConflict,
			"tool result receipt failure authority is invalid",
		)
	}
	return contract, nil
}

func validateResultReceiptFailure(
	failure ResultReceiptFailure,
	call ToolCall,
	definition Definition,
	errorCode string,
) error {
	if !validResultReceiptFailureIDs(failure) {
		return invalid(errorCode, "tool result receipt failure identity is invalid")
	}
	if failure.ToolCallID != call.ID || failure.WorkspaceID != call.WorkspaceID ||
		failure.WorkflowRunID != call.WorkflowRunID || failure.NodeRunID != call.NodeRunID ||
		failure.NodeAttemptID != call.NodeAttemptID || failure.ExpectedOutputHash != call.ResponseHash ||
		failure.ValidatorVersion != ResultReceiptFailureValidatorVersion {
		return inconsistent(errorCode, "tool result receipt failure authority binding drifted")
	}
	completedAt := call.CompletedAt.UTC().Truncate(time.Microsecond)
	if failure.CheckedAt.IsZero() || failure.CreatedAt.IsZero() ||
		!failure.CheckedAt.Equal(failure.CheckedAt.UTC().Truncate(time.Microsecond)) ||
		!failure.CreatedAt.Equal(failure.CreatedAt.UTC().Truncate(time.Microsecond)) ||
		failure.CheckedAt.Before(completedAt) || failure.CreatedAt.Before(failure.CheckedAt) {
		return inconsistent(errorCode, "tool result receipt failure chronology drifted")
	}
	if !validResultReceiptFailureShape(failure, call) {
		return inconsistent(errorCode, "tool result receipt failure observation drifted")
	}
	return nil
}

func validResultReceiptFailureIDs(failure ResultReceiptFailure) bool {
	ids := []foundation.ID{
		failure.ID,
		failure.ToolCallID,
		failure.OperationID,
		failure.AnalysisRunID,
		failure.WorkspaceID,
		failure.WorkflowRunID,
		failure.NodeRunID,
		failure.NodeAttemptID,
	}
	seen := make(map[foundation.ID]struct{}, len(ids))
	for _, id := range ids {
		if !canonicalResultReceiptID(id) {
			return false
		}
		if _, duplicate := seen[id]; duplicate {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

func validResultReceiptFailureShape(failure ResultReceiptFailure, call ToolCall) bool {
	outputBytesValid := validResultReceiptFailureBytes(
		failure.ObservedOutputBytes,
		ResultReceiptFailureMaxObservedOutputBytes,
	)
	bindingBytesValid := validResultReceiptFailureBytes(
		failure.ObservedBindingBytes,
		ResultReceiptFailureMaxObservedBindingBytes,
	)
	switch failure.FailureCode {
	case ResultReceiptFailureMissing:
		return failure.ObservedOutputHash == "" && failure.ObservedOutputBytes == nil &&
			failure.ObservedBindingHash == "" && failure.ObservedBindingBytes == nil
	case ResultReceiptFailureHashMismatch:
		return lowerHex(failure.ObservedOutputHash, 64) &&
			failure.ObservedOutputHash != failure.ExpectedOutputHash && outputBytesValid &&
			failure.ObservedBindingHash == "" && failure.ObservedBindingBytes == nil
	case ResultReceiptFailureContractInvalid:
		return failure.ObservedOutputHash == failure.ExpectedOutputHash && outputBytesValid &&
			*failure.ObservedOutputBytes == call.ResponseBytes && failure.ObservedBindingHash == "" &&
			failure.ObservedBindingBytes == nil
	case ResultReceiptFailureBindingInvalid:
		return failure.ObservedOutputHash == failure.ExpectedOutputHash && outputBytesValid &&
			*failure.ObservedOutputBytes == call.ResponseBytes && lowerHex(failure.ObservedBindingHash, 64) &&
			bindingBytesValid
	default:
		return false
	}
}

func validResultReceiptFailureBytes(value *int64, maximum int64) bool {
	return value != nil && *value >= 0 && *value <= maximum
}

func cloneResultReceiptFailureBytes(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func resultReceiptFailureBytesString(value *int64) string {
	if value == nil {
		return "null"
	}
	return fmt.Sprintf("%d", *value)
}
