// Package domain defines the Memory lifecycle and its privacy invariants.
package domain

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeInvalid indicates invalid Memory input or persisted state.
	ErrorCodeInvalid = "MEMORY_INVALID"
	// ErrorCodeNotFound hides Memory records outside the caller's scope.
	ErrorCodeNotFound = "MEMORY_NOT_FOUND"
	// ErrorCodeOwnerDenied indicates that the acting principal is not the owner.
	ErrorCodeOwnerDenied = "MEMORY_OWNER_DENIED"
	// ErrorCodeStateConflict indicates an invalid lifecycle transition.
	ErrorCodeStateConflict = "MEMORY_STATE_CONFLICT"
	// ErrorCodeVersionConflict indicates an optimistic-lock conflict.
	ErrorCodeVersionConflict = "MEMORY_VERSION_CONFLICT"
	// ErrorCodeIdempotencyConflict indicates a reused key with a different request.
	ErrorCodeIdempotencyConflict = "MEMORY_IDEMPOTENCY_CONFLICT"
	// ErrorCodeExpired indicates that an expired Memory cannot become effective.
	ErrorCodeExpired = "MEMORY_EXPIRED"
	// ErrorCodeUnavailable indicates an unavailable persistence dependency.
	ErrorCodeUnavailable = "MEMORY_DEPENDENCY_UNAVAILABLE"
	// ErrorCodePersistenceInvalid indicates corrupt durable Memory state.
	ErrorCodePersistenceInvalid = "MEMORY_PERSISTENCE_INVALID"
	// ErrorCodeResultInconsistent indicates a repository result that violates the contract.
	ErrorCodeResultInconsistent = "MEMORY_RESULT_INCONSISTENT"
)

func invalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeInvalid, false, errors.New(message))
}

func stateConflict(message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeStateConflict, false, errors.New(message))
}

func expired(message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeExpired, false, errors.New(message))
}

func ownerDenied(message string) error {
	return foundation.NewError(foundation.ErrorPermissionDenied, ErrorCodeOwnerDenied, false, errors.New(message))
}

// InvalidError creates a stable invalid-input error for callers that validate boundaries.
func InvalidError(message string) error { return invalid(message) }

// StateConflictError creates a stable lifecycle conflict error.
func StateConflictError(message string) error { return stateConflict(message) }

// ExpiredError creates a stable expiration error.
func ExpiredError(message string) error { return expired(message) }

// OwnerDeniedError creates a stable owner-scope error.
func OwnerDeniedError(message string) error { return ownerDenied(message) }
