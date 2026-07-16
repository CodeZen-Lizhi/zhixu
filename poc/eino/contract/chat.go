package contract

import (
	"context"
	"fmt"
)

// Role identifies the project-level role of a chat message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is the project-owned chat message contract.
type Message struct {
	Role    Role
	Content string
}

// ChatRequest contains the messages for one model invocation.
type ChatRequest struct {
	Messages []Message
}

// ChatResponse is the project-owned result of one model invocation.
type ChatResponse struct {
	Message Message
}

// ChatRunner executes a short chat flow without exposing framework types.
type ChatRunner interface {
	Run(ctx context.Context, request ChatRequest) (ChatResponse, error)
}

// ErrorKind is the stable project classification for adapter failures.
type ErrorKind string

const (
	ErrorInvalidInput          ErrorKind = "invalid_input"
	ErrorPermissionDenied      ErrorKind = "permission_denied"
	ErrorNotFound              ErrorKind = "not_found"
	ErrorManualRecovery        ErrorKind = "manual_recovery"
	ErrorDependencyUnavailable ErrorKind = "dependency_unavailable"
	ErrorRetryableFailure      ErrorKind = "retryable_failure"
	ErrorNonRetryableFailure   ErrorKind = "non_retryable_failure"
)

// Error wraps an adapter failure with a project-owned classification.
type Error struct {
	Kind      ErrorKind
	Operation string
	Retryable bool
	Cause     error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s: %s", e.Operation, e.Kind)
}

// Unwrap preserves errors.Is/errors.As without exposing provider types in the contract.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
