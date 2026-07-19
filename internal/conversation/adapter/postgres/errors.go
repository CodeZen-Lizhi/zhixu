// Package postgres 实现 Conversation 持久化与有界读模型的 PostgreSQL Adapter。
package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	// ErrorCodeDatabaseUnavailable 表示 Conversation 数据库当前不可用。
	ErrorCodeDatabaseUnavailable = "CONVERSATION_DATABASE_UNAVAILABLE"
	// ErrorCodeWorkspaceNotFound 表示创建会话时 Workspace 不存在。
	ErrorCodeWorkspaceNotFound = "CONVERSATION_WORKSPACE_NOT_FOUND"
	// ErrorCodeConversationNotFound 表示 Conversation 不存在或不属于指定 Workspace。
	ErrorCodeConversationNotFound = "CONVERSATION_NOT_FOUND"
	// ErrorCodeAnswerNotFound 表示 Answer 不存在或不属于指定 Workspace。
	ErrorCodeAnswerNotFound = "CONVERSATION_ANSWER_NOT_FOUND"
	// ErrorCodeIdempotencyConflict 表示幂等键已绑定不同创建请求。
	ErrorCodeIdempotencyConflict = "CONVERSATION_IDEMPOTENCY_CONFLICT"
	// ErrorCodePersistenceInvalid 表示调用方提供的持久化事实不合法。
	ErrorCodePersistenceInvalid = "CONVERSATION_PERSISTENCE_INVALID"
	// ErrorCodePersistenceCorrupt 表示数据库读回事实违反领域不变量。
	ErrorCodePersistenceCorrupt = "CONVERSATION_PERSISTENCE_CORRUPT"
)

func classify(cause error, code string) error {
	if cause == nil {
		return nil
	}
	switch {
	case errors.Is(cause, context.Canceled):
		return foundation.NewError(foundation.ErrorNonRetryableFailure, code, false, context.Canceled)
	case errors.Is(cause, context.DeadlineExceeded):
		return foundation.NewError(foundation.ErrorRetryableFailure, code, true, context.DeadlineExceeded)
	case errors.Is(cause, pgx.ErrNoRows):
		return notFound(code, cause)
	}
	var pgErr *pgconn.PgError
	if errors.As(cause, &pgErr) {
		switch pgErr.Code {
		case "40001", "40P01", "55P03":
			return foundation.NewError(foundation.ErrorRetryableFailure, code, true, cause)
		case "23505":
			return conflict(code, cause)
		case "23503", "23514", "55000":
			return consistency(code, cause)
		}
	}
	return dependency(code, cause)
}

func invalid(code string, cause error) error {
	if cause == nil {
		cause = errors.New("conversation persistence input is invalid")
	}
	return foundation.NewError(foundation.ErrorInvalidInput, code, false, cause)
}

func notFound(code string, cause error) error {
	return foundation.NewError(foundation.ErrorNotFound, code, false, cause)
}

func conflict(code string, cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, code, false, cause)
}

func consistency(code string, cause error) error {
	if cause == nil {
		cause = errors.New("conversation persistence invariant failed")
	}
	return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, cause)
}

func dependency(code string, cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, cause)
}
