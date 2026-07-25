package postgres

import (
	"errors"
	"fmt"

	"github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func domainErrorInvalid(code string, cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, code, false, cause)
}

func domainErrorUnavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeStoreUnavailable, true, cause)
}

func appendConflict() error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeAppendConflict, false, errors.New("audit idempotency key is bound to a different event"))
}

func classifyStoreError(err error) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return foundation.NewError(foundation.ErrorNotFound, domain.ErrorCodeEventNotFound, false, errors.New("audit event was not found"))
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23503", "23514":
			return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeEventInvalid, false, errors.New("audit database constraint rejected event"))
		case "23505":
			return appendConflict()
		case "40001", "40P01":
			return foundation.NewError(foundation.ErrorRetryableFailure, domain.ErrorCodeStoreUnavailable, true, errors.New("audit database transaction should be retried"))
		}
	}
	// Do not wrap or expose a driver error string: it may contain SQL values,
	// DSNs, Cookie/Token data, or absolute paths supplied by a caller.
	return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeStoreUnavailable, true, fmt.Errorf("audit database operation failed: %T", err))
}
