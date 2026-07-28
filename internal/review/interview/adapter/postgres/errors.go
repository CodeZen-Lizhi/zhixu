// Package postgres implements the Interview Store and QuestionSource ports with pgx.
package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
	"github.com/jackc/pgx/v5/pgconn"
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
		return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeDependencyUnavailable, true, err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			if strings.Contains(pgErr.ConstraintName, "interview_command") || strings.Contains(pgErr.ConstraintName, "interview_turn_idempotency") || strings.Contains(pgErr.ConstraintName, "review_session_key") {
				return domain.ConflictError(domain.ErrorCodeIdempotencyConflict, "interview idempotency key is already bound")
			}
			return domain.ConflictError(domain.ErrorCodeQuestionOrderConflict, "interview state already changed")
		case "23503", "23514", "23502", "22001", "22P02":
			return foundation.NewError(foundation.ErrorConsistencyViolation, fallback, false, err)
		case "40001", "40P01", "55P03", "08000", "08003", "08006", "57P01":
			return foundation.NewError(foundation.ErrorRetryableFailure, domain.ErrorCodeDependencyUnavailable, true, err)
		case "57014":
			return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeDependencyUnavailable, true, err)
		}
	}
	return foundation.NewError(foundation.ErrorDependencyUnavailable, fallback, true, err)
}

func persistenceInvalid(message string, cause error) error {
	if cause == nil {
		cause = errors.New(message)
	}
	return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodePersistenceInvalid, false, cause)
}
