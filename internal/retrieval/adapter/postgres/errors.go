package postgres

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func classify(err error, code string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound(code, err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "40001", "40P01", "55P03":
			return foundation.NewError(foundation.ErrorRetryableFailure, code, true, err)
		case "23505":
			return conflict(code, err)
		case "23503", "23514", "55000":
			return consistency(code, err)
		}
	}
	return dependency(code, err)
}
func notFound(code string, err error) error {
	return foundation.NewError(foundation.ErrorNotFound, code, false, err)
}
func conflict(code string, err error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, code, false, err)
}
func consistency(code string, err error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, err)
}
func dependency(code string, err error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, err)
}
