package domain

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeWorkspaceNotFound 表示 Review 命令指定的 Workspace 不存在。
	ErrorCodeWorkspaceNotFound = "REVIEW_WORKSPACE_NOT_FOUND"
	// ErrorCodeDeckInvalid 表示牌组输入不合法。
	ErrorCodeDeckInvalid = "REVIEW_DECK_INVALID"
	// ErrorCodeDeckNotFound 表示 Workspace 内不存在牌组。
	ErrorCodeDeckNotFound = "REVIEW_DECK_NOT_FOUND"
	// ErrorCodeDeckArchived 表示归档牌组不可修改。
	ErrorCodeDeckArchived = "REVIEW_DECK_ARCHIVED"
	// ErrorCodeCardInvalid 表示卡片输入不合法。
	ErrorCodeCardInvalid = "REVIEW_CARD_INVALID"
	// ErrorCodeCardNotFound 表示 Workspace 内不存在卡片。
	ErrorCodeCardNotFound = "REVIEW_CARD_NOT_FOUND"
	// ErrorCodeCardInactive 表示卡片未审批或已失效。
	ErrorCodeCardInactive = "REVIEW_CARD_INACTIVE"
	// ErrorCodeCardStateConflict 表示卡片状态或版本不匹配。
	ErrorCodeCardStateConflict = "REVIEW_CARD_STATE_CONFLICT"
	// ErrorCodeEvidenceInvalid 表示证据绑定不合法。
	ErrorCodeEvidenceInvalid = "REVIEW_EVIDENCE_INVALID"
	// ErrorCodeEvidenceMissing 表示卡片缺少正式知识证据。
	ErrorCodeEvidenceMissing = "REVIEW_EVIDENCE_MISSING"
	// ErrorCodeEvidenceDuplicate 表示卡片包含重复证据。
	ErrorCodeEvidenceDuplicate = "REVIEW_EVIDENCE_DUPLICATE"
	// ErrorCodeEvidenceStale 表示证据或 Claim 已失效。
	ErrorCodeEvidenceStale = "REVIEW_EVIDENCE_STALE"
	// ErrorCodeInvalidationInvalid 表示卡片失效原因或元数据不合法。
	ErrorCodeInvalidationInvalid = "REVIEW_INVALIDATION_INVALID"
	// ErrorCodeScheduleInvalid 表示调度状态不合法。
	ErrorCodeScheduleInvalid = "REVIEW_SCHEDULE_INVALID"
	// ErrorCodeScheduleConflict 表示调度 CAS 不匹配。
	ErrorCodeScheduleConflict = "REVIEW_SCHEDULE_CONFLICT"
	// ErrorCodeSchedulerUnavailable 表示调度器不可用或版本不匹配。
	ErrorCodeSchedulerUnavailable = "REVIEW_SCHEDULER_UNAVAILABLE"
	// ErrorCodeSessionInvalid 表示会话输入不合法。
	ErrorCodeSessionInvalid = "REVIEW_SESSION_INVALID"
	// ErrorCodeSessionNotFound 表示 Workspace 内不存在会话。
	ErrorCodeSessionNotFound = "REVIEW_SESSION_NOT_FOUND"
	// ErrorCodeSessionClosed 表示终态会话不能继续答题。
	ErrorCodeSessionClosed = "REVIEW_SESSION_CLOSED"
	// ErrorCodeSessionTypeConflict 表示 Review 操作命中了非 Review 或未绑定 Deck 的会话。
	ErrorCodeSessionTypeConflict = "REVIEW_SESSION_TYPE_CONFLICT"
	// ErrorCodeAnswerInvalid 表示答题输入不合法。
	ErrorCodeAnswerInvalid = "REVIEW_ANSWER_INVALID"
	// ErrorCodeAnswerIdempotencyConflict 表示幂等键已绑定另一请求。
	ErrorCodeAnswerIdempotencyConflict = "REVIEW_ANSWER_IDEMPOTENCY_CONFLICT"
	// ErrorCodeQuestionStale 表示答题引用不再匹配当前卡片或调度快照。
	ErrorCodeQuestionStale = "REVIEW_QUESTION_STALE"
	// ErrorCodeScoreInvalid 表示评分结构不合法。
	ErrorCodeScoreInvalid = "REVIEW_SCORE_INVALID"
	// ErrorCodeScoringUnavailable 表示可信服务端评分器暂不可用。
	ErrorCodeScoringUnavailable = "REVIEW_SCORING_UNAVAILABLE"
	// ErrorCodeJSONInvalid 表示范围或配置 JSON 不合法。
	ErrorCodeJSONInvalid = "REVIEW_JSON_INVALID"
	// ErrorCodePersistenceInvalid 表示 Review 持久化快照无法安全编码或解码。
	ErrorCodePersistenceInvalid = "REVIEW_PERSISTENCE_INVALID"
	// ErrorCodeDependencyUnavailable 表示 Review 持久化依赖不可用。
	ErrorCodeDependencyUnavailable = "REVIEW_DEPENDENCY_UNAVAILABLE"
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

// InvalidError 创建 Review 领域的稳定输入错误。
func InvalidError(code, message string) error { return invalid(code, message) }

// ConflictError 创建 Review 领域的稳定冲突错误。
func ConflictError(code, message string) error { return conflict(code, message) }

// NotFoundError 创建 Review 领域的稳定未找到错误。
func NotFoundError(code, message string) error { return notFound(code, message) }

// UnavailableError 创建 Review 领域的稳定依赖错误。
func UnavailableError(code, message string) error { return unavailable(code, message) }
