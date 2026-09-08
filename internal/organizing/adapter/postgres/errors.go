// Package postgres persists Organizing drafts, templates and workflow inputs.
package postgres

import (
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

func invalid(err error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, organizingapp.ErrorCodeRequestInvalid, false, err)
}

func notFound(err error) error {
	return foundation.NewError(foundation.ErrorNotFound, organizingapp.ErrorCodeNotFound, false, err)
}

func versionConflict(err error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, organizingdomain.ErrorCodeVersionConflict, false, err)
}

func idempotencyConflict(err error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, organizingapp.ErrorCodeIdempotencyConflict, false, err)
}

func inconsistent(err error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, organizingapp.ErrorCodeResultInvalid, false, err)
}

func unavailable(err error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, organizingapp.ErrorCodeRepositoryUnavailable, true, err)
}

func leaseLost(err error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, organizingapp.ErrorCodeOutboxLeaseLost, false, err)
}
