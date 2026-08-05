// Package postgres persists Organizing drafts, templates and workflow inputs.
package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"github.com/jackc/pgx/v5/pgconn"
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

func classify(err error) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return foundation.NewError(foundation.ErrorNonRetryableFailure, organizingapp.ErrorCodeRepositoryUnavailable, false, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return unavailable(err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			if pgErr.ConstraintName == "command_receipt_pkey" {
				return idempotencyConflict(err)
			}
			return inconsistent(err)
		case "23503", "23514", "23502", "22P02":
			return inconsistent(err)
		case "40001", "40P01", "55P03", "08000", "08003", "08006", "57P01", "57014":
			return foundation.NewError(foundation.ErrorRetryableFailure, organizingapp.ErrorCodeRepositoryUnavailable, true, err)
		case "55000":
			return inconsistent(err)
		}
	}
	return unavailable(err)
}
