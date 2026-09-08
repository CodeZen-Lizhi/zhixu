// Package postgres 实现 Agent Model Run/Call 的 PostgreSQL 持久化边界。
package postgres

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
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
