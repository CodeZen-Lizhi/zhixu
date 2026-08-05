package application

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeDependencyUnavailable 表示 Organizing 依赖未完整组装。
	ErrorCodeDependencyUnavailable = "ORGANIZING_DEPENDENCY_UNAVAILABLE"
	// ErrorCodeRepositoryUnavailable 表示 Organizing PostgreSQL owner 不可用。
	ErrorCodeRepositoryUnavailable = "ORGANIZING_REPOSITORY_UNAVAILABLE"
	// ErrorCodeRequestInvalid 表示命令或查询形状不合法。
	ErrorCodeRequestInvalid = "ORGANIZING_REQUEST_INVALID"
	// ErrorCodeIdempotencyInvalid 表示幂等键不合法。
	ErrorCodeIdempotencyInvalid = "ORGANIZING_IDEMPOTENCY_KEY_INVALID"
	// ErrorCodeIdempotencyConflict 表示同一 Workspace key 已绑定不同请求。
	ErrorCodeIdempotencyConflict = "ORGANIZING_IDEMPOTENCY_CONFLICT"
	// ErrorCodeNotFound 隐藏不存在与跨 Workspace 资源的差异。
	ErrorCodeNotFound = "ORGANIZING_NOT_FOUND"
	// ErrorCodeResultInvalid 表示依赖返回了不满足绑定的结果。
	ErrorCodeResultInvalid = "ORGANIZING_RESULT_INVALID"
	// ErrorCodeMaterialStale 表示确认前材料或 Evidence 已漂移。
	ErrorCodeMaterialStale = "ORGANIZING_MATERIAL_STALE"
	// ErrorCodeTemplateReadOnly 表示内置模板不允许原地修改。
	ErrorCodeTemplateReadOnly = "ORGANIZING_TEMPLATE_READ_ONLY"
	// ErrorCodeOutboxLeaseLost 表示旧 Worker 已失去 Start Outbox fence。
	ErrorCodeOutboxLeaseLost = "ORGANIZING_START_OUTBOX_LEASE_LOST"
)

func invalid(code, message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, code, false, errors.New(message))
}

func inconsistent(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeResultInvalid, false, errors.New(message))
}
