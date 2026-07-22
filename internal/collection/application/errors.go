package application

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeRequestInvalid 表示命令或查询参数非法。
	ErrorCodeRequestInvalid = "COLLECTION_REQUEST_INVALID"
	// ErrorCodeNotFound 表示 Workspace 内不存在目标集合。
	ErrorCodeNotFound = "COLLECTION_NOT_FOUND"
	// ErrorCodeVersionConflict 表示 expected version 或唯一命名冲突。
	ErrorCodeVersionConflict = "COLLECTION_VERSION_CONFLICT"
	// ErrorCodeIdempotencyConflict 表示幂等键复用了不同 payload。
	ErrorCodeIdempotencyConflict = "COLLECTION_IDEMPOTENCY_CONFLICT"
	// ErrorCodeArchivedImmutable 表示归档集合不可修改。
	ErrorCodeArchivedImmutable = "COLLECTION_ARCHIVED_IMMUTABLE"
	// ErrorCodeResultInconsistent 表示持久化结果违反了命令绑定。
	ErrorCodeResultInconsistent = "COLLECTION_RESULT_INCONSISTENT"
	// ErrorCodeCursorInvalid 表示 cursor 被篡改、跨请求或无法解码。
	ErrorCodeCursorInvalid = "COLLECTION_CURSOR_INVALID"
	// ErrorCodeCursorStale 表示 cursor 绑定的事实修订已经变化。
	ErrorCodeCursorStale = "COLLECTION_CURSOR_STALE"
	// ErrorCodeDependencyUnavailable 表示 Collection 持久化依赖不可用。
	ErrorCodeDependencyUnavailable = "COLLECTION_DEPENDENCY_UNAVAILABLE"
	// ErrorCodeQueryTimeout 表示 Collection read model 查询超过数据库或调用方时限。
	ErrorCodeQueryTimeout = "COLLECTION_QUERY_TIMEOUT"
)

func requestInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeRequestInvalid, false, errors.New(message))
}

func cursorInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeCursorInvalid, false, errors.New(message))
}

func notFound(message string) error {
	return foundation.NewError(foundation.ErrorNotFound, ErrorCodeNotFound, false, errors.New(message))
}

func versionConflict(code, message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, code, false, errors.New(message))
}

func archivedImmutable(message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeArchivedImmutable, false, errors.New(message))
}

func resultInconsistent(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeResultInconsistent, false, errors.New(message))
}
