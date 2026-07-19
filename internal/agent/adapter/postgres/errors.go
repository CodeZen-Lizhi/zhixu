// Package postgres 实现 Agent Model Run/Call 的 PostgreSQL 持久化边界。
package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	ErrorCodeDatabaseUnavailable    = "AGENT_DATABASE_UNAVAILABLE"
	ErrorCodeRuntimeNotFound        = "AGENT_RUNTIME_NOT_FOUND"
	ErrorCodeRuntimeConflict        = "AGENT_RUNTIME_VERSION_CONFLICT"
	ErrorCodeRuntimeReplayConflict  = "AGENT_RUNTIME_IDEMPOTENCY_CONFLICT"
	ErrorCodeRuntimeConsistency     = "AGENT_RUNTIME_CONSISTENCY"
	ErrorCodeModelCallResultUnknown = "AGENT_MODEL_CALL_RESULT_UNKNOWN"
	ErrorCodeModelRunResultUnknown  = "AGENT_MODEL_RUN_RESULT_UNKNOWN"
)

func classify(cause error) error {
	if cause == nil {
		return nil
	}
	switch {
	case errors.Is(cause, context.Canceled):
		return foundation.NewError(foundation.ErrorNonRetryableFailure, "AGENT_DATABASE_CANCELLED", false, context.Canceled)
	case errors.Is(cause, context.DeadlineExceeded):
		return foundation.NewError(foundation.ErrorRetryableFailure, "AGENT_DATABASE_TIMEOUT", true, context.DeadlineExceeded)
	}
	var pgErr *pgconn.PgError
	if errors.As(cause, &pgErr) {
		switch pgErr.Code {
		case "40001", "40P01", "55P03":
			return foundation.NewError(foundation.ErrorRetryableFailure, ErrorCodeDatabaseUnavailable, true, errors.New("agent database operation is retryable"))
		case "23505":
			return foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeRuntimeReplayConflict, false, errors.New("agent runtime identity already exists"))
		case "23503", "23514", "55000":
			return consistency(errors.New("agent runtime database invariant failed"))
		}
	}
	return foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeDatabaseUnavailable, true, errors.New("agent database operation failed"))
}

func notFound() error {
	return foundation.NewError(foundation.ErrorNotFound, ErrorCodeRuntimeNotFound, false, errors.New("agent runtime record was not found"))
}

func versionConflict() error {
	return foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeRuntimeConflict, false, errors.New("agent runtime version changed"))
}

func replayConflict() error {
	return foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeRuntimeReplayConflict, false, errors.New("agent runtime replay binding differs"))
}

func consistency(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeRuntimeConsistency, false, cause)
}
