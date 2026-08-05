package domain

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeDraftInvalid 表示整理草稿字段或状态不合法。
	ErrorCodeDraftInvalid = "ORGANIZING_DRAFT_INVALID"
	// ErrorCodeMaterialInvalid 表示材料判别联合、版本或证据绑定不合法。
	ErrorCodeMaterialInvalid = "ORGANIZING_MATERIAL_INVALID"
	// ErrorCodeTemplateInvalid 表示模板声明超出受控 Schema 或固定策略。
	ErrorCodeTemplateInvalid = "ORGANIZING_TEMPLATE_INVALID"
	// ErrorCodeSnapshotInvalid 表示确认快照没有冻结完整输入身份。
	ErrorCodeSnapshotInvalid = "ORGANIZING_SNAPSHOT_INVALID"
	// ErrorCodeVersionConflict 表示草稿或模板版本已变化。
	ErrorCodeVersionConflict = "ORGANIZING_VERSION_CONFLICT"
)

func invalid(code, message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, code, false, errors.New(message))
}

func conflict(message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeVersionConflict, false, errors.New(message))
}
