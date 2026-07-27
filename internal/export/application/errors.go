package application

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/export/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func invalid(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeInvalid, false, cause)
}

func forbidden(cause error) error {
	return foundation.NewError(foundation.ErrorPermissionDenied, domain.ErrorCodePermissionDenied, false, cause)
}

func expired(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeExpired, false, cause)
}

func unavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, cause)
}

func resultInvalid(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeResultInvalid, false, cause)
}

func notFound(cause error) error {
	return foundation.NewError(foundation.ErrorNotFound, domain.ErrorCodeNotFound, false, cause)
}

func idempotencyConflict(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeConflict, false, cause)
}

var errNoRows = errors.New("export row was not found")
