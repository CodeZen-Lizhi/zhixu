package domain

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeDraftInvalid 表示 Working Draft 字段或状态不合法。
	ErrorCodeDraftInvalid = "AUTHORING_DRAFT_INVALID"
	// ErrorCodeTargetPathInvalid 表示目标 Markdown 路径不能安全发布。
	ErrorCodeTargetPathInvalid = "AUTHORING_TARGET_PATH_INVALID"
	// ErrorCodeFreezeInvalid 表示当前 Working Draft 不能冻结为 Article Revision。
	ErrorCodeFreezeInvalid = "AUTHORING_FREEZE_INVALID"
	// ErrorCodeVersionConflict 表示 Working Draft 版本已经变化。
	ErrorCodeVersionConflict = "AUTHORING_DRAFT_VERSION_CONFLICT"
	// ErrorCodePublicationInvalid 表示发布身份或状态不满足冻结契约。
	ErrorCodePublicationInvalid = "AUTHORING_PUBLICATION_INVALID"
)

func invalid(code, message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, code, false, errors.New(message))
}

func conflict(message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeVersionConflict, false, errors.New(message))
}
