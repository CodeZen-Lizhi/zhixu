package foundation

import "fmt"

// ErrorKind is the stable classification shared by API, workflows and adapters.
type ErrorKind string

const (
	ErrorInvalidInput           ErrorKind = "invalid_input"
	ErrorNotFound               ErrorKind = "not_found"
	ErrorVersionConflict        ErrorKind = "version_conflict"
	ErrorPermissionDenied       ErrorKind = "permission_denied"
	ErrorDependencyUnavailable  ErrorKind = "dependency_unavailable"
	ErrorRetryableFailure       ErrorKind = "retryable_failure"
	ErrorNonRetryableFailure    ErrorKind = "non_retryable_failure"
	ErrorConsistencyViolation   ErrorKind = "consistency_violation"
	ErrorManualRecoveryRequired ErrorKind = "manual_recovery_required"
)

// Error is the project-owned failure envelope. Cause is retained for internal
// errors.Is/As checks but is not included in the public Error string.
type Error struct {
	Kind      ErrorKind
	Code      string
	Retryable bool
	Cause     error
}

// NewError creates a classified project error.
func NewError(kind ErrorKind, code string, retryable bool, cause error) *Error {
	return &Error{Kind: kind, Code: code, Retryable: retryable, Cause: cause}
}

// Error returns a safe stable summary without leaking the underlying cause.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Kind)
}

// Unwrap preserves the internal error chain.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
