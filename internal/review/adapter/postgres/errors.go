package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/domain"
)

func classify(err error, fallback string) error {
	if err == nil {
		return nil
	}
	var existing *foundation.Error
	if errors.As(err, &existing) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return foundation.NewError(foundation.ErrorNonRetryableFailure, fallback, false, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, fallback, true, err)
	}
	switch platformpostgres.SQLState(err) {
	case "23505":
		constraint := platformpostgres.ConstraintName(err)
		if strings.Contains(constraint, "review_command") || strings.Contains(constraint, "review_answer") || strings.Contains(constraint, "review_session") {
			return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeAnswerIdempotencyConflict, false, err)
		}
		return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeCardStateConflict, false, err)
	case "23503", "23514", "23502":
		return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeEvidenceStale, false, err)
	case "40001", "40P01", "55P03", "08000", "08003", "08006", "57P01":
		return foundation.NewError(foundation.ErrorRetryableFailure, domain.ErrorCodeDependencyUnavailable, true, err)
	case "57014":
		return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeDependencyUnavailable, true, err)
	}
	return foundation.NewError(foundation.ErrorDependencyUnavailable, fallback, true, err)
}

func invalid(message string) error        { return domain.InvalidError(domain.ErrorCodeCardInvalid, message) }
func notFound(code, message string) error { return domain.NotFoundError(code, message) }
func conflict(code, message string) error { return domain.ConflictError(code, message) }

func persistenceInvalid(message string, cause error) error {
	if cause == nil {
		cause = errors.New(message)
	}
	return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodePersistenceInvalid, false, cause)
}
