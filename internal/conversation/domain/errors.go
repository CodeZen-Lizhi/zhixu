// Package domain 定义 RAG Conversation、Question、Answer 与 Feedback 的稳定领域契约。
package domain

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeConversationInvalid 表示 Conversation 身份、状态或生命周期不合法。
	ErrorCodeConversationInvalid = "CONVERSATION_INVALID"
	// ErrorCodeConversationTransitionInvalid 表示 Conversation 生命周期迁移不合法。
	ErrorCodeConversationTransitionInvalid = "CONVERSATION_TRANSITION_INVALID"
	// ErrorCodeQuestionInvalid 表示 Question 文本、Scope 或回答选项不合法。
	ErrorCodeQuestionInvalid = "CONVERSATION_QUESTION_INVALID"
	// ErrorCodeClarificationInvalid 表示 Clarification Envelope 或载荷不合法。
	ErrorCodeClarificationInvalid = "CONVERSATION_CLARIFICATION_INVALID"
	// ErrorCodeContextInvalid 表示会话上下文超界、乱序或绑定损坏。
	ErrorCodeContextInvalid = "CONVERSATION_CONTEXT_INVALID"
	// ErrorCodeAnswerInvalid 表示 Answer 发布结果或生命周期不合法。
	ErrorCodeAnswerInvalid = "CONVERSATION_ANSWER_INVALID"
	// ErrorCodeAnswerTransitionInvalid 表示 Answer 尝试重复发布或进入非发布终态。
	ErrorCodeAnswerTransitionInvalid = "CONVERSATION_ANSWER_TRANSITION_INVALID"
	// ErrorCodeCursorInvalid 表示 Conversation 或 Turn 分页边界不合法。
	ErrorCodeCursorInvalid = "CONVERSATION_CURSOR_INVALID"
	// ErrorCodeRetrievalSummaryInvalid 表示 Answer 的可恢复检索摘要不合法。
	ErrorCodeRetrievalSummaryInvalid = "CONVERSATION_RETRIEVAL_SUMMARY_INVALID"
	// ErrorCodeFeedbackInvalid 表示 Answer Feedback 类型、引用绑定或幂等哈希不合法。
	ErrorCodeFeedbackInvalid = "CONVERSATION_FEEDBACK_INVALID"
)

func invalid(code, message string, cause error) error {
	if cause == nil {
		cause = errors.New(message)
	}
	return foundation.NewError(foundation.ErrorInvalidInput, code, false, cause)
}

func inconsistent(code, message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, errors.New(message))
}

func versionConflict(code, message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, code, false, errors.New(message))
}
