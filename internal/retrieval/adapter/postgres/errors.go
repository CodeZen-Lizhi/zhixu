package postgres

import (
	"database/sql"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

func classify(err error, code string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return notFound(code, err)
	}
	switch platformpostgres.SQLState(err) {
	case "40001", "40P01", "55P03":
		return foundation.NewError(foundation.ErrorRetryableFailure, code, true, err)
	case "23505":
		return conflict(code, err)
	case "23503", "23514", "55000":
		return consistency(code, err)
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
