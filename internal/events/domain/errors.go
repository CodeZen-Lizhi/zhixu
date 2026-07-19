package domain

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeEventInvalid 表示持久事件身份、版本或保留期绑定损坏。
	ErrorCodeEventInvalid = "SSE_EVENT_INVALID"
	// ErrorCodePayloadSummaryInvalid 表示事件摘要包含未知字段、正文或非法值。
	ErrorCodePayloadSummaryInvalid = "SSE_PAYLOAD_SUMMARY_INVALID"
	// ErrorCodeCursorInvalid 表示 Last-Event-ID 不是规范正十进制序号。
	ErrorCodeCursorInvalid = "SSE_CURSOR_INVALID"
	// ErrorCodeCursorFuture 表示 Last-Event-ID 超过当前 Workspace 水位。
	ErrorCodeCursorFuture = "SSE_CURSOR_FUTURE"
	// ErrorCodeCursorExpired 表示 Last-Event-ID 已早于当前逻辑保留窗口。
	ErrorCodeCursorExpired = "SSE_CURSOR_EXPIRED"
	// ErrorCodeReplayStateInvalid 表示持久水位或保留边界自身不一致。
	ErrorCodeReplayStateInvalid = "SSE_REPLAY_STATE_INVALID"
	// ErrorCodeReplayQueryInvalid 表示内部重放查询的 Workspace、游标或上限非法。
	ErrorCodeReplayQueryInvalid = "SSE_REPLAY_QUERY_INVALID"
	// ErrorCodeStoreUnavailable 表示持久事件查询依赖暂不可用。
	ErrorCodeStoreUnavailable = "SSE_STORE_UNAVAILABLE"
	// ErrorCodeEventCorrupt 表示数据库中的事件投影不满足公开通知契约。
	ErrorCodeEventCorrupt = "SSE_EVENT_CORRUPT"
	// ErrorCodeAppendInvalid 表示待持久化事件的身份、摘要或时间绑定非法。
	ErrorCodeAppendInvalid = "SSE_APPEND_INVALID"
	// ErrorCodeAppendConflict 表示同一来源引用已绑定不同事件事实。
	ErrorCodeAppendConflict = "SSE_APPEND_CONFLICT"
	// ErrorCodeAppendBindingInvalid 表示事件引用的 Workspace 资源绑定不存在或不一致。
	ErrorCodeAppendBindingInvalid = "SSE_APPEND_BINDING_INVALID"
	// ErrorCodeAppendTransactionUnavailable 表示调用方未提供可用的 PostgreSQL 事务。
	ErrorCodeAppendTransactionUnavailable = "SSE_APPEND_TRANSACTION_UNAVAILABLE"
)

func invalid(code, message string, cause error) error {
	if cause == nil {
		cause = errors.New(message)
	}
	return foundation.NewError(foundation.ErrorInvalidInput, code, false, cause)
}

func conflict(code, message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, code, false, errors.New(message))
}

func inconsistent(code, message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, errors.New(message))
}
