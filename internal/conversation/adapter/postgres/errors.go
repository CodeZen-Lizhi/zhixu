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
	// ErrorCodeFeedbackIdempotencyConflict 表示 Answer Feedback 幂等键已绑定不同请求。
	ErrorCodeFeedbackIdempotencyConflict = "CONVERSATION_FEEDBACK_IDEMPOTENCY_CONFLICT"
	// ErrorCodeFeedbackPersistenceInvalid 表示 Feedback 持久化输入不合法。
	ErrorCodeFeedbackPersistenceInvalid = "CONVERSATION_FEEDBACK_PERSISTENCE_INVALID"
	// ErrorCodeFeedbackPersistenceCorrupt 表示 Feedback 持久事实违反领域不变量。
	ErrorCodeFeedbackPersistenceCorrupt = "CONVERSATION_FEEDBACK_PERSISTENCE_CORRUPT"
	// ErrorCodeIdempotencyConflict 表示幂等键已绑定不同创建请求。
	ErrorCodeIdempotencyConflict = "CONVERSATION_IDEMPOTENCY_CONFLICT"
	// ErrorCodeQuestionDispatchUnavailable 表示 Question 原子派发依赖不可用。
	ErrorCodeQuestionDispatchUnavailable = "CONVERSATION_QUESTION_DISPATCH_UNAVAILABLE"
	// ErrorCodeQuestionDispatchInvalid 表示 Question 派发记录不是 canonical 领域请求。
	ErrorCodeQuestionDispatchInvalid = "CONVERSATION_QUESTION_DISPATCH_INVALID"
	// ErrorCodeQuestionIdempotencyConflict 表示 Question 幂等键已绑定不同请求。
	ErrorCodeQuestionIdempotencyConflict = "CONVERSATION_QUESTION_IDEMPOTENCY_CONFLICT"
	// ErrorCodeQuestionActiveWorkflow 表示 Conversation 已有非终态 Answer Workflow。
	ErrorCodeQuestionActiveWorkflow = "CONVERSATION_QUESTION_ACTIVE_WORKFLOW"
	// ErrorCodeQuestionConversationArchived 表示已归档 Conversation 不再接受 Question。
	ErrorCodeQuestionConversationArchived = "CONVERSATION_QUESTION_CONVERSATION_ARCHIVED"
	// ErrorCodeQuestionDispatchCorrupt 表示 Question 到 Workflow/Job 的持久绑定不完整。
	ErrorCodeQuestionDispatchCorrupt = "CONVERSATION_QUESTION_DISPATCH_CORRUPT"
	// ErrorCodeExecutionContextInvalid 表示 Agent 请求的 Conversation 执行绑定不合法。
	ErrorCodeExecutionContextInvalid = "CONVERSATION_EXECUTION_CONTEXT_INVALID"
	// ErrorCodeExecutionContextCorrupt 表示持久 Question、Answer、Workflow 或历史哈希绑定损坏。
	ErrorCodeExecutionContextCorrupt = "CONVERSATION_EXECUTION_CONTEXT_CORRUPT"
	// ErrorCodePersistenceInvalid 表示调用方提供的持久化事实不合法。
	ErrorCodePersistenceInvalid = "CONVERSATION_PERSISTENCE_INVALID"
	// ErrorCodePersistenceCorrupt 表示数据库读回事实违反领域不变量。
	ErrorCodePersistenceCorrupt = "CONVERSATION_PERSISTENCE_CORRUPT"
	// ErrorCodeDraftStreamInvalid 表示短期草稿投影请求不合法。
	ErrorCodeDraftStreamInvalid = "CONVERSATION_DRAFT_STREAM_INVALID"
	// ErrorCodeDraftStreamConflict 表示草稿所属 Attempt 已失去租约或代际已替换。
	ErrorCodeDraftStreamConflict = "CONVERSATION_DRAFT_STREAM_CONFLICT"
	// ErrorCodeDraftStreamUnavailable 表示草稿短期存储暂不可用。
	ErrorCodeDraftStreamUnavailable = "CONVERSATION_DRAFT_STREAM_UNAVAILABLE"
)

func classify(cause error, code string) error {
	if cause == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(cause, &classified) {
		return cause
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
