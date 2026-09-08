package postgres

import (
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/memory/domain"
)

func invalid(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeInvalid, false, cause)
}

func notFound(cause error) error {
	return foundation.NewError(foundation.ErrorNotFound, domain.ErrorCodeNotFound, false, cause)
}

func idempotencyConflict(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeIdempotencyConflict, false, cause)
}

func versionConflict(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeVersionConflict, false, cause)
}

func unavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, cause)
}

func inconsistent(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodePersistenceInvalid, false, cause)
}
