package domain

import (
	"strings"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeCompletionInvalid 表示 Reindex 完成命令缺少规范身份或完整 Fence。
	ErrorCodeCompletionInvalid = "REINDEX_COMPLETION_INVALID"
)

// CompleteReindexCommand 绑定一次 Reindex 原子完成与唯一 Activation 身份。
// 完成时间和 lease 有效性只由 PostgreSQL 时间决定，调用方不得注入时间。
type CompleteReindexCommand struct {
	WorkspaceID              foundation.ID
	Fence                    DeliveryFence
	ActivationID             foundation.ID
	ActivationIdempotencyKey string
	ActivationReasonCode     string
}

// CompleteReindexResult 返回同一事务提交后的 Delivery、Attempt 与 Active Index 事实。
type CompleteReindexResult struct {
	Delivery             Delivery
	Attempt              DeliveryAttempt
	Activation           Activation
	ActiveIndexVersion   IndexVersion
	PreviousIndexVersion *IndexVersion
	Replayed             bool
}

// ValidateCompleteReindexCommand 校验完成命令自身，不读取数据库可变状态。
func ValidateCompleteReindexCommand(command CompleteReindexCommand) error {
	if !canonicalDeliveryID(command.WorkspaceID) || !canonicalDeliveryID(command.ActivationID) {
		return invalid(ErrorCodeCompletionInvalid, "completion identity is invalid")
	}
	if err := ValidateDeliveryFence(command.Fence); err != nil {
		return invalid(ErrorCodeCompletionInvalid, "completion fence is invalid")
	}
	if !canonicalCompletionText(command.ActivationIdempotencyKey) || !canonicalCompletionText(command.ActivationReasonCode) {
		return invalid(ErrorCodeCompletionInvalid, "completion activation metadata is invalid")
	}
	return nil
}

func canonicalCompletionText(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && utf8.RuneCountInString(value) <= 128
}
