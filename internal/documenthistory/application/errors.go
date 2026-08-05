package application

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	ErrorCodeInvalid          = "DOCUMENT_HISTORY_INVALID"
	ErrorCodeNotFound         = "DOCUMENT_HISTORY_NOT_FOUND"
	ErrorCodeCursorInvalid    = "DOCUMENT_HISTORY_CURSOR_INVALID"
	ErrorCodeCursorStale      = "DOCUMENT_HISTORY_CURSOR_STALE"
	ErrorCodeGitUnavailable   = "DOCUMENT_HISTORY_GIT_UNAVAILABLE"
	ErrorCodeOutputTooLarge   = "DOCUMENT_HISTORY_OUTPUT_TOO_LARGE"
	ErrorCodeDirty            = "DOCUMENT_RESTORE_DIRTY_WORKTREE"
	ErrorCodeRestoreStale     = "DOCUMENT_RESTORE_STALE"
	ErrorCodeResultInvalid    = "DOCUMENT_HISTORY_RESULT_INVALID"
	ErrorCodeIdempotencyReuse = "IDEMPOTENCY_KEY_REUSED"
)

func invalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeInvalid, false, errors.New(message))
}

func conflict(code, message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, code, false, errors.New(message))
}

func inconsistent(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeResultInvalid, false, errors.New(message))
}
