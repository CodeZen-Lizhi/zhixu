package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/memory/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

func classify(err error) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return foundation.NewError(foundation.ErrorNonRetryableFailure, domain.ErrorCodeUnavailable, false, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return unavailable(err)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound(err)
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23505":
			return idempotencyConflict(err)
		case "23503", "23514", "23502", "22P02":
			return inconsistent(err)
		case "40001", "40P01", "55P03", "08000", "08003", "08006", "57P01", "57014":
			return foundation.NewError(foundation.ErrorRetryableFailure, domain.ErrorCodeUnavailable, true, err)
		case "55000":
			return inconsistent(err)
		}
	}
	return unavailable(err)
}
