// Package postgres 实现 Tool Call 与 Workflow Tool Policy 的 PostgreSQL Adapter。
package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	// ErrorCodeDatabaseUnavailable 表示 Tool PostgreSQL 依赖暂不可用。
	ErrorCodeDatabaseUnavailable = "TOOL_DATABASE_UNAVAILABLE"
	// ErrorCodeDatabaseCancelled 表示 Tool PostgreSQL 操作被调用方取消。
	ErrorCodeDatabaseCancelled = "TOOL_DATABASE_CANCELLED"
	// ErrorCodeDatabaseTimeout 表示 Tool PostgreSQL 操作超过调用方 deadline。
	ErrorCodeDatabaseTimeout = "TOOL_DATABASE_TIMEOUT"
	// ErrorCodeCallNotFound 表示指定 Workspace 下不存在 Tool Call。
	ErrorCodeCallNotFound = "TOOL_CALL_NOT_FOUND"
	// ErrorCodeCallVersionConflict 表示 Tool Call 已由其他事务归约或结果不同。
	ErrorCodeCallVersionConflict = "TOOL_CALL_VERSION_CONFLICT"
	// ErrorCodeIdempotencyConflict 表示同一执行或副作用幂等键绑定了不同请求。
	ErrorCodeIdempotencyConflict = "TOOL_IDEMPOTENCY_CONFLICT"
	// ErrorCodeContextStale 表示 Workflow/Node/Attempt/lease 身份不再有效。
	ErrorCodeContextStale = "TOOL_CONTEXT_STALE"
	// ErrorCodeWorkflowBindingDenied 表示 Tool contract 不允许当前 Workflow 版本。
	ErrorCodeWorkflowBindingDenied = "TOOL_WORKFLOW_BINDING_DENIED"
	// ErrorCodeToolNotAllowed 表示当前 Node allowlist 未包含精确 Tool 版本。
	ErrorCodeToolNotAllowed = "TOOL_NOT_ALLOWED"
	// ErrorCodePermissionDenied 表示当前 Node 未声明 Tool 要求的精确 Capability。
	ErrorCodePermissionDenied = "TOOL_PERMISSION_DENIED"
	// ErrorCodePersistenceConsistency 表示数据库事实违反 Tool 持久化不变量。
	ErrorCodePersistenceConsistency = "TOOL_PERSISTENCE_CONSISTENCY"
)

func classify(cause error) error {
	if cause == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(cause, &classified) {
		return cause
	}
	switch {
	case errors.Is(cause, context.Canceled):
		return foundation.NewError(foundation.ErrorNonRetryableFailure, ErrorCodeDatabaseCancelled, false, cause)
	case errors.Is(cause, context.DeadlineExceeded):
		return foundation.NewError(foundation.ErrorRetryableFailure, ErrorCodeDatabaseTimeout, true, cause)
	}
	var pgErr *pgconn.PgError
	if errors.As(cause, &pgErr) {
		switch pgErr.Code {
		case "40001", "40P01", "55P03":
			return foundation.NewError(foundation.ErrorRetryableFailure, ErrorCodeDatabaseUnavailable, true, cause)
		case "23505":
			return foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeIdempotencyConflict, false, cause)
		case "23503", "23514", "55000":
			return consistency(cause)
		}
	}
	return foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeDatabaseUnavailable, true, cause)
}

func notFound(cause error) error {
	return foundation.NewError(foundation.ErrorNotFound, ErrorCodeCallNotFound, false, cause)
}

func stale(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeContextStale, false, cause)
}

func idempotencyConflict(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeIdempotencyConflict, false, cause)
}

func versionConflict(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeCallVersionConflict, false, cause)
}

func denied(code string, cause error) error {
	return foundation.NewError(foundation.ErrorPermissionDenied, code, false, cause)
}

func consistency(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodePersistenceConsistency, false, cause)
}
