package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
)

func gormToolsNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound)
}

func classifyGORMTools(ctx context.Context, cause error) error {
	if cause == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(cause, &classified) {
		return cause
	}
	if contextCause := gormToolsContextCause(ctx, cause); contextCause != nil {
		if errors.Is(contextCause, context.Canceled) {
			return foundation.NewError(foundation.ErrorNonRetryableFailure, ErrorCodeDatabaseCancelled, false, contextCause)
		}
		return foundation.NewError(foundation.ErrorRetryableFailure, ErrorCodeDatabaseTimeout, true, contextCause)
	}
	switch platformpostgres.SQLState(cause) {
	case "40001", "40P01", "55P03":
		return foundation.NewError(foundation.ErrorRetryableFailure, ErrorCodeDatabaseUnavailable, true, cause)
	case "23505":
		return idempotencyConflict(cause)
	case "23503", "23514", "55000":
		return consistency(cause)
	}
	if errors.Is(cause, sql.ErrTxDone) {
		return gormToolsUnavailable(cause)
	}
	return gormToolsUnavailable(cause)
}

func classifyGORMToolsReceiptWrite(ctx context.Context, cause error) error {
	if platformpostgres.SQLState(cause) == "23505" {
		return receiptConflict(cause)
	}
	return classifyGORMTools(ctx, cause)
}

func gormToolsContextCause(ctx context.Context, cause error) error {
	if ctx != nil && ctx.Err() != nil {
		contextCause := context.Cause(ctx)
		if contextCause == nil {
			return ctx.Err()
		}
		if !errors.Is(contextCause, ctx.Err()) {
			return errors.Join(ctx.Err(), contextCause)
		}
		return contextCause
	}
	if errors.Is(cause, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return nil
}

func gormToolsUnavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeDatabaseUnavailable, true, cause)
}
