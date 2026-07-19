package domain

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeDefinitionInvalid 表示 Tool Definition 不满足冻结契约。
	ErrorCodeDefinitionInvalid = "TOOL_DEFINITION_INVALID"
	// ErrorCodeRequestInvalid 表示 Tool Request wire 契约非法。
	ErrorCodeRequestInvalid = "TOOL_REQUEST_INVALID"
	// ErrorCodeCallInvalid 表示 Tool Call 持久事实不一致。
	ErrorCodeCallInvalid = "TOOL_CALL_INVALID"
	// ErrorCodeCallTransitionInvalid 表示 Tool Call 状态迁移非法。
	ErrorCodeCallTransitionInvalid = "TOOL_CALL_TRANSITION_INVALID"
	// ErrorCodeIdempotencyBindingInvalid 表示幂等绑定不完整。
	ErrorCodeIdempotencyBindingInvalid = "TOOL_IDEMPOTENCY_BINDING_INVALID"
	// ErrorCodeExecutionIdentityInvalid 表示服务端执行身份不完整。
	ErrorCodeExecutionIdentityInvalid = "TOOL_EXECUTION_IDENTITY_INVALID"
)

func invalid(code, message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, code, false, errors.New(message))
}

func inconsistent(code, message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, errors.New(message))
}

func versionConflict(code, message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, code, false, errors.New(message))
}
