// Package domain 定义共享 Learning Path 的稳定领域错误与模型。
package domain

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodePathInvalid 表示 Learning Path 输入或持久化事实不合法。
	ErrorCodePathInvalid = "LEARNING_PATH_INVALID"
	// ErrorCodePathNotFound 表示 Workspace 内不存在 Learning Path。
	ErrorCodePathNotFound = "LEARNING_PATH_NOT_FOUND"
	// ErrorCodeStepNotFound 表示 Path 内不存在步骤。
	ErrorCodeStepNotFound = "LEARNING_PATH_STEP_NOT_FOUND"
	// ErrorCodeNotActionable 表示评分不存在可执行的学习缺口。
	ErrorCodeNotActionable = "LEARNING_PATH_NOT_ACTIONABLE"
	// ErrorCodeIdempotencyConflict 表示客户端幂等键绑定了另一请求。
	ErrorCodeIdempotencyConflict = "LEARNING_PATH_IDEMPOTENCY_CONFLICT"
	// ErrorCodeStateConflict 表示状态或版本 CAS 已漂移。
	ErrorCodeStateConflict = "LEARNING_PATH_STATE_CONFLICT"
	// ErrorCodeEvidenceStale 表示用于路径的正式 Evidence 已失效。
	ErrorCodeEvidenceStale = "LEARNING_PATH_EVIDENCE_STALE"
	// ErrorCodeReservationPending 表示同一来源正在创建路径。
	ErrorCodeReservationPending = "LEARNING_PATH_RESERVATION_PENDING"
	// ErrorCodeArtifactConflict 表示 Artifact 草稿与预留摘要不一致。
	ErrorCodeArtifactConflict = "LEARNING_PATH_ARTIFACT_CONFLICT"
	// ErrorCodePersistenceInvalid 表示持久化快照无法安全恢复。
	ErrorCodePersistenceInvalid = "LEARNING_PATH_PERSISTENCE_INVALID"
	// ErrorCodeDependencyUnavailable 表示外部持久化或 Artifact 依赖不可用。
	ErrorCodeDependencyUnavailable = "LEARNING_PATH_DEPENDENCY_UNAVAILABLE"
)

// InvalidError 创建稳定的 Learning Path 输入错误。
func InvalidError(code, message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, code, false, errors.New(message))
}

// ConflictError 创建稳定的 Learning Path 冲突错误。
func ConflictError(code, message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, code, false, errors.New(message))
}

// NotFoundError 创建稳定的 Learning Path 未找到错误。
func NotFoundError(code, message string) error {
	return foundation.NewError(foundation.ErrorNotFound, code, false, errors.New(message))
}

// UnavailableError 创建稳定的 Learning Path 依赖错误。
func UnavailableError(code, message string) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, errors.New(message))
}
