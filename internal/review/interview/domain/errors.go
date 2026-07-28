// Package domain 定义证据约束的 Interview 与 Learning Path 领域事实。
package domain

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeConfigInvalid 表示面试配置不满足受控边界。
	ErrorCodeConfigInvalid = "INTERVIEW_CONFIG_INVALID"
	// ErrorCodeEvidenceInvalid 表示题目未绑定确认 Claim 的 SUPPORTS Evidence。
	ErrorCodeEvidenceInvalid = "INTERVIEW_EVIDENCE_INVALID"
	// ErrorCodeQuestionInvalid 表示题目快照不合法。
	ErrorCodeQuestionInvalid = "INTERVIEW_QUESTION_INVALID"
	// ErrorCodeQuestionNotFound 表示面试内不存在题目。
	ErrorCodeQuestionNotFound = "INTERVIEW_QUESTION_NOT_FOUND"
	// ErrorCodeQuestionOrderConflict 表示当前题目顺序或版本已漂移。
	ErrorCodeQuestionOrderConflict = "INTERVIEW_QUESTION_ORDER_CONFLICT"
	// ErrorCodeSessionNotFound 表示 Workspace 内不存在面试会话。
	ErrorCodeSessionNotFound = "INTERVIEW_SESSION_NOT_FOUND"
	// ErrorCodeSessionClosed 表示终态会话不可继续作答。
	ErrorCodeSessionClosed = "INTERVIEW_SESSION_CLOSED"
	// ErrorCodeSessionExpired 表示新回答已经超过冻结配置派生的截止时间。
	ErrorCodeSessionExpired = "INTERVIEW_SESSION_EXPIRED"
	// ErrorCodeCompletionPending 表示 Session 已被另一条未完成 Completion reservation 冻结。
	ErrorCodeCompletionPending = "INTERVIEW_COMPLETION_PENDING"
	// ErrorCodeCompletionAbandoned 表示 Completion reservation 已被维护任务放弃，必须重新 Begin。
	ErrorCodeCompletionAbandoned = "INTERVIEW_COMPLETION_ABANDONED"
	// ErrorCodeCompletionArtifactConflict 表示同一 reservation 绑定了不同 Artifact digest 或产物。
	ErrorCodeCompletionArtifactConflict = "INTERVIEW_COMPLETION_ARTIFACT_CONFLICT"
	// ErrorCodeTurnInvalid 表示用户作答或评分结果不合法。
	ErrorCodeTurnInvalid = "INTERVIEW_TURN_INVALID"
	// ErrorCodeScoreInvalid 表示服务端评分未完整绑定证据。
	ErrorCodeScoreInvalid = "INTERVIEW_SCORE_INVALID"
	// ErrorCodeScorerUnavailable 表示受控评分器不可用。
	ErrorCodeScorerUnavailable = "INTERVIEW_SCORER_UNAVAILABLE"
	// ErrorCodeReportInvalid 表示报告或学习路径事实不合法。
	ErrorCodeReportInvalid = "INTERVIEW_REPORT_INVALID"
	// ErrorCodePathInvalid 表示学习路径或步骤状态不合法。
	ErrorCodePathInvalid = "INTERVIEW_PATH_INVALID"
	// ErrorCodeIdempotencyConflict 表示同一幂等键绑定了不同请求。
	ErrorCodeIdempotencyConflict = "INTERVIEW_IDEMPOTENCY_CONFLICT"
	// ErrorCodePersistenceInvalid 表示持久化快照无法安全重建。
	ErrorCodePersistenceInvalid = "INTERVIEW_PERSISTENCE_INVALID"
	// ErrorCodeDependencyUnavailable 表示持久化或 Artifact 依赖不可用。
	ErrorCodeDependencyUnavailable = "INTERVIEW_DEPENDENCY_UNAVAILABLE"
)

func invalid(code, message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, code, false, errors.New(message))
}

func conflict(code, message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, code, false, errors.New(message))
}

func notFound(code, message string) error {
	return foundation.NewError(foundation.ErrorNotFound, code, false, errors.New(message))
}

func unavailable(code, message string) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, errors.New(message))
}

// InvalidError 创建稳定的 Interview 输入错误。
func InvalidError(code, message string) error { return invalid(code, message) }

// ConflictError 创建稳定的 Interview 冲突错误。
func ConflictError(code, message string) error { return conflict(code, message) }

// NotFoundError 创建稳定的 Interview 未找到错误。
func NotFoundError(code, message string) error { return notFound(code, message) }

// UnavailableError 创建稳定的 Interview 依赖错误。
func UnavailableError(code, message string) error { return unavailable(code, message) }
