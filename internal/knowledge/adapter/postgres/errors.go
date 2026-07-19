package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	errorCodeDatabaseUnavailable = "KNOWLEDGE_STORAGE_UNAVAILABLE"
	errorCodeStorageRetryable    = "KNOWLEDGE_STORAGE_RETRYABLE"
	errorCodeStorageConsistency  = "KNOWLEDGE_STORAGE_CONSISTENCY"
	errorCodeWorkspaceNotFound   = "KNOWLEDGE_WORKSPACE_NOT_FOUND"
	errorCodeTopicNotFound       = "KNOWLEDGE_TOPIC_NOT_FOUND"
	errorCodeClaimNotFound       = "KNOWLEDGE_CLAIM_NOT_FOUND"
	errorCodeRelationNotFound    = "KNOWLEDGE_RELATION_NOT_FOUND"
	errorCodeConflictNotFound    = "KNOWLEDGE_CONFLICT_NOT_FOUND"
)

func classify(err error, fallbackCode string) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, fallbackCode, false, err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "40001", "40P01", "55P03":
			return foundation.NewError(foundation.ErrorRetryableFailure, errorCodeStorageRetryable, true, err)
		case "23505":
			return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeVersionConflict, false, err)
		case "23502", "23503", "23514", "55000":
			return foundation.NewError(foundation.ErrorConsistencyViolation, errorCodeStorageConsistency, false, err)
		case "57P01", "08000", "08003", "08006":
			return foundation.NewError(foundation.ErrorRetryableFailure, errorCodeStorageRetryable, true, err)
		}
		if strings.HasPrefix(pgErr.Code, "22") {
			return foundation.NewError(foundation.ErrorConsistencyViolation, errorCodeStorageConsistency, false, err)
		}
	}
	return foundation.NewError(foundation.ErrorDependencyUnavailable, fallbackCode, true, err)
}

func notFound(code string, err error) error {
	return foundation.NewError(foundation.ErrorNotFound, code, false, err)
}

func versionConflict(err error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeVersionConflict, false, err)
}

func idempotencyConflict(err error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeIdempotencyConflict, false, err)
}

func consistency(code string, err error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, err)
}
