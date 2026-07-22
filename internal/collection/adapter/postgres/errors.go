package postgres

import (
	"context"
	"errors"
	"strings"

	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5/pgconn"
)

func requestInvalid(err error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, collectionapp.ErrorCodeRequestInvalid, false, err)
}
func notFound(err error) error {
	return foundation.NewError(foundation.ErrorNotFound, collectionapp.ErrorCodeNotFound, false, err)
}
func idempotencyConflict(err error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, collectionapp.ErrorCodeIdempotencyConflict, false, err)
}
func versionConflict(code string, err error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, code, false, err)
}
func archivedImmutable(err error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, collectionapp.ErrorCodeArchivedImmutable, false, err)
}
func unavailable(err error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, collectionapp.ErrorCodeDependencyUnavailable, true, err)
}
func inconsistent(err error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, collectionapp.ErrorCodeResultInconsistent, false, err)
}

func classify(err error) error {
	if err == nil {
		return nil
	}
	var existing *foundation.Error
	if errors.As(err, &existing) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return foundation.NewError(foundation.ErrorNonRetryableFailure, collectionapp.ErrorCodeDependencyUnavailable, false, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, collectionapp.ErrorCodeQueryTimeout, true, err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			if strings.Contains(pgErr.ConstraintName, "active_name") {
				return versionConflict("COLLECTION_NAME_CONFLICT", err)
			}
			return idempotencyConflict(err)
		case "23503", "23514", "23502":
			return inconsistent(err)
		case "57014":
			// PostgreSQL statement_timeout 与客户端取消都使用 query_canceled；
			// Collection read model 对两者统一暴露可安全重试的查询超时契约。
			return foundation.NewError(foundation.ErrorDependencyUnavailable, collectionapp.ErrorCodeQueryTimeout, true, err)
		case "40001", "40P01", "55P03", "08000", "08003", "08006", "57P01":
			return foundation.NewError(foundation.ErrorRetryableFailure, collectionapp.ErrorCodeDependencyUnavailable, true, err)
		case "55000":
			return archivedImmutable(err)
		}
	}
	return unavailable(err)
}
