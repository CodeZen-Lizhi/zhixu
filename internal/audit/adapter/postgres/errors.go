package postgres

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
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
	if auditNoRows(err) {
		return foundation.NewError(foundation.ErrorNotFound, domain.ErrorCodeEventNotFound, false, errors.New("audit event was not found"))
	}
	switch platformpostgres.SQLState(err) {
	case "23503", "23514":
		return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeEventInvalid, false, errors.New("audit database constraint rejected event"))
	case "23505":
		return appendConflict()
	case "40001", "40P01":
		return foundation.NewError(foundation.ErrorRetryableFailure, domain.ErrorCodeStoreUnavailable, true, errors.New("audit database transaction should be retried"))
	}
	// Do not wrap or expose a driver error string: it may contain SQL values,
	// DSNs, Cookie/Token data, or absolute paths supplied by a caller.
	return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeStoreUnavailable, true, fmt.Errorf("audit database operation failed: %T", err))
}

func auditNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound)
}
