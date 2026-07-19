package domain

import (
	"strconv"
)

// MaxReplayPageSize 是单次持久事件读取允许的最大条数。
const MaxReplayPageSize = 100

// ReplayCursor 表示客户端最后确认收到的持久事件序号。
type ReplayCursor struct {
	AfterSeq int64
}

// ParseReplayCursor 解析规范、正十进制 Last-Event-ID。
func ParseReplayCursor(value string) (ReplayCursor, error) {
	sequence, err := strconv.ParseInt(value, 10, 64)
	if err != nil || sequence <= 0 || strconv.FormatInt(sequence, 10) != value {
		return ReplayCursor{}, invalid(ErrorCodeCursorInvalid, "SSE replay cursor is invalid", err)
	}
	return ReplayCursor{AfterSeq: sequence}, nil
}

// ValidateReplayCursor 根据当前水位和最早保留序号判定游标是否可补发。
func ValidateReplayCursor(cursor ReplayCursor, currentWatermark int64, earliestRetained *int64) error {
	if cursor.AfterSeq <= 0 {
		return invalid(ErrorCodeCursorInvalid, "SSE replay cursor is invalid", nil)
	}
	if currentWatermark < 0 || (earliestRetained != nil && *earliestRetained <= 0) {
		return inconsistent(ErrorCodeReplayStateInvalid, "SSE replay watermark is inconsistent")
	}
	if cursor.AfterSeq > currentWatermark {
		return invalid(ErrorCodeCursorFuture, "SSE replay cursor is ahead of the current watermark", nil)
	}
	if earliestRetained == nil || cursor.AfterSeq < *earliestRetained {
		return conflict(ErrorCodeCursorExpired, "SSE replay cursor is outside the retention window")
	}
	return nil
}
