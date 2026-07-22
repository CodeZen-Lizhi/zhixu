// Package domain 定义 Smart Collection 查询、排序与视图配置的稳定领域契约。
package domain

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeQueryInvalid 表示 Query AST 结构、值类型或边界不满足冻结约束。
	ErrorCodeQueryInvalid = "COLLECTION_QUERY_INVALID"
	// ErrorCodeFieldUnknown 表示字段未注册。
	ErrorCodeFieldUnknown = "COLLECTION_FIELD_UNKNOWN"
	// ErrorCodeFieldUnavailable 表示字段已知但能力尚未落地。
	ErrorCodeFieldUnavailable = "COLLECTION_FIELD_UNAVAILABLE"
	// ErrorCodeSortInvalid 表示排序字段、方向或数量不满足约束。
	ErrorCodeSortInvalid = "COLLECTION_SORT_INVALID"
	// ErrorCodeViewConfigInvalid 表示视图类型或视图配置不满足约束。
	ErrorCodeViewConfigInvalid = "COLLECTION_VIEW_CONFIG_INVALID"
)

func invalid(code, message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, code, false, errors.New(message))
}
