package domain

import (
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// MaxPageLimit 是 Conversation 和 Turn 列表允许的最大页大小。
	MaxPageLimit = 100
)

// ConversationCursor 保存最近活动时间与稳定 ID 的列表边界。
type ConversationCursor struct {
	LastActivityAt time.Time
	ID             foundation.ID
}

// Validate 校验 Conversation cursor 的完整边界。
func (cursor ConversationCursor) Validate() error {
	id, err := foundation.ParseID(string(cursor.ID))
	if err != nil || id != cursor.ID || cursor.LastActivityAt.IsZero() {
		return invalid(ErrorCodeCursorInvalid, "conversation cursor is invalid", err)
	}
	return nil
}

// TurnCursor 保存 Question ordinal 与稳定 ID 的列表边界。
type TurnCursor struct {
	Ordinal    int64
	QuestionID foundation.ID
}

// Validate 校验 Turn cursor 的完整边界。
func (cursor TurnCursor) Validate() error {
	id, err := foundation.ParseID(string(cursor.QuestionID))
	if err != nil || id != cursor.QuestionID || cursor.Ordinal < 1 {
		return invalid(ErrorCodeCursorInvalid, "turn cursor is invalid", err)
	}
	return nil
}

// ValidatePageLimit 校验所有 Conversation 查询共享的页大小上限。
func ValidatePageLimit(limit int) error {
	if limit < 1 || limit > MaxPageLimit {
		return invalid(ErrorCodeCursorInvalid, "page limit is invalid", nil)
	}
	return nil
}
