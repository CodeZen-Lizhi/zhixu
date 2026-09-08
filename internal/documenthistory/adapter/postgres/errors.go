package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/documenthistory/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"gorm.io/gorm"
)

func documentQueryError(err error) error {
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound) {
		return foundation.NewError(foundation.ErrorNotFound, application.ErrorCodeNotFound, false, errors.New("document was not found"))
	}
	return classify(err, "DOCUMENT_HISTORY_DOCUMENT_QUERY_FAILED")
}

func dependencyUnavailable(err error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, application.ErrorCodeGitUnavailable, true, err)
}

func invalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, application.ErrorCodeInvalid, false, errors.New(message))
}

func classify(err error, code string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return foundation.NewError(foundation.ErrorNonRetryableFailure, code, false, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return foundation.NewError(foundation.ErrorRetryableFailure, code, true, err)
	}
	return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, err)
}
