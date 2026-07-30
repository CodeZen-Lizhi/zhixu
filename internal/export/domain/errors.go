package domain

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeInvalid 表示导出请求或持久事实无效。
	ErrorCodeInvalid = "EXPORT_REQUEST_INVALID"
	// ErrorCodeNotFound 表示任务不属于请求 Workspace 或不存在。
	ErrorCodeNotFound = "EXPORT_NOT_FOUND"
	// ErrorCodePermissionDenied 表示当前身份无权导出所请求的字段。
	ErrorCodePermissionDenied = "EXPORT_PERMISSION_DENIED"
	// ErrorCodeExpired 表示结果已超过生命周期。
	ErrorCodeExpired = "EXPORT_EXPIRED"
	// ErrorCodeConflict 表示幂等或版本绑定冲突。
	ErrorCodeConflict = "EXPORT_IDEMPOTENCY_CONFLICT"
	// ErrorCodeUnavailable 表示导出依赖暂不可用。
	ErrorCodeUnavailable = "EXPORT_DEPENDENCY_UNAVAILABLE"
	// ErrorCodeResultInvalid 表示生成文件与持久绑定不一致。
	ErrorCodeResultInvalid = "EXPORT_RESULT_INCONSISTENT"
	// ErrorCodeAttachmentRootNotFound 表示固定 attachments/ 源目录不存在。
	ErrorCodeAttachmentRootNotFound = "EXPORT_ATTACHMENT_ROOT_NOT_FOUND"
	// ErrorCodeAttachmentLimitExceeded 表示附件数量、源字节或归档字节超过版本化上限。
	ErrorCodeAttachmentLimitExceeded = "EXPORT_ATTACHMENT_LIMIT_EXCEEDED"
)

func invalid(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeInvalid, false, cause)
}

func notFound(cause error) error {
	return foundation.NewError(foundation.ErrorNotFound, ErrorCodeNotFound, false, cause)
}

func forbidden(cause error) error {
	return foundation.NewError(foundation.ErrorPermissionDenied, ErrorCodePermissionDenied, false, cause)
}

func expired(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeExpired, false, cause)
}

func conflict(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeConflict, false, cause)
}

func unavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeUnavailable, true, cause)
}

func resultInvalid(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeResultInvalid, false, cause)
}

var errJobNotRunnable = errors.New("export job is not runnable")
