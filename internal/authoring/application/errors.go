package application

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeIdempotencyKeyInvalid 表示命令缺失安全的单个幂等键。
	ErrorCodeIdempotencyKeyInvalid = "AUTHORING_IDEMPOTENCY_KEY_INVALID"
	// ErrorCodeIdempotencyConflict 表示同一 Workspace key 已绑定其他请求。
	ErrorCodeIdempotencyConflict = "AUTHORING_IDEMPOTENCY_CONFLICT"
	// ErrorCodeNotFound 隐藏不存在与跨 Workspace 资源的差异。
	ErrorCodeNotFound = "AUTHORING_DRAFT_NOT_FOUND"
	// ErrorCodePathConflict 表示 canonical path 已被另一 Document 占用。
	ErrorCodePathConflict = "AUTHORING_TARGET_PATH_CONFLICT"
	// ErrorCodePublicationConflict 表示 Document 已存在其他未完成发布或 Revision 已绑定。
	ErrorCodePublicationConflict = "AUTHORING_PUBLICATION_CONFLICT"
	// ErrorCodeRepositoryUnavailable 表示 Authoring 数据库依赖不可用。
	ErrorCodeRepositoryUnavailable = "AUTHORING_REPOSITORY_UNAVAILABLE"
	// ErrorCodeResultInvalid 表示持久层返回的绑定不满足应用契约。
	ErrorCodeResultInvalid = "AUTHORING_RESULT_INVALID"
	// ErrorCodePublicationTargetParentNotFound 表示 CREATE_ONLY 的父目录不存在，Proposal 尚未创建。
	ErrorCodePublicationTargetParentNotFound = "WRITEBACK_TARGET_PARENT_NOT_FOUND"
)

func invalid(code, message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, code, false, errors.New(message))
}

func inconsistent(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeResultInvalid, false, errors.New(message))
}
