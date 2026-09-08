package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"gorm.io/gorm"
)

// GORMRepository combines ordinary GORM operations with the narrow native
// capabilities that require COPY or physical PostgreSQL session identity.
type GORMRepository struct {
	database   *gorm.DB
	unitOfWork foundation.UnitOfWork
	native     retrievalNativeCapabilities
}

var (
	_ application.Store               = (*GORMRepository)(nil)
	_ application.SourceRefreshLocker = (*GORMRepository)(nil)
)

// NewGORMRepository derives the GORM root and UoW from one platform Pool.
func NewGORMRepository(pool *platformpostgres.Pool) (*GORMRepository, error) {
	database, unitOfWork, err := gormRetrievalDependencies(pool)
	if err != nil {
		return nil, err
	}
	native, err := newRetrievalNativeCapabilities(pool)
	if err != nil {
		return nil, err
	}
	return &GORMRepository{database: database, unitOfWork: unitOfWork, native: native}, nil
}

func gormRetrievalDependencies(pool *platformpostgres.Pool) (*gorm.DB, foundation.UnitOfWork, error) {
	if pool == nil {
		return nil, nil, gormRetrievalUnavailable("RETRIEVAL_DATABASE_UNAVAILABLE", errors.New("retrieval PostgreSQL pool is unavailable"))
	}
	database, err := pool.GORM()
	if err != nil {
		return nil, nil, gormRetrievalUnavailable("RETRIEVAL_DATABASE_UNAVAILABLE", errors.New("retrieval GORM database is unavailable"))
	}
	unitOfWork, err := pool.UnitOfWork()
	if err != nil {
		return nil, nil, gormRetrievalUnavailable("RETRIEVAL_DATABASE_UNAVAILABLE", errors.New("retrieval unit of work is unavailable"))
	}
	if !validGORMRetrievalDatabase(database) || nilGORMRetrievalDependency(unitOfWork) {
		return nil, nil, gormRetrievalUnavailable("RETRIEVAL_DATABASE_UNAVAILABLE", errors.New("retrieval GORM dependencies are unavailable"))
	}
	return database, unitOfWork, nil
}

func (repository *GORMRepository) ready(ctx context.Context) error {
	if repository == nil || !validGORMRetrievalDatabase(repository.database) ||
		nilGORMRetrievalDependency(repository.unitOfWork) ||
		nilGORMRetrievalDependency(repository.native.manifest) ||
		nilGORMRetrievalDependency(repository.native.snapshot) ||
		nilGORMRetrievalDependency(repository.native.refresh) {
		return gormRetrievalUnavailable("RETRIEVAL_DATABASE_UNAVAILABLE", errors.New("retrieval GORM repository is unavailable"))
	}
	if ctx == nil {
		return foundation.NewError(foundation.ErrorInvalidInput, "RETRIEVAL_CONTEXT_INVALID", false, errors.New("retrieval context is nil"))
	}
	return nil
}

func (repository *GORMRepository) BeginIndex(
	ctx context.Context,
	build domain.IndexBuild,
) (domain.IndexVersionResult, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.IndexVersionResult{}, err
	}
	result, err := repository.native.BeginIndex(ctx, build)
	if err != nil {
		return domain.IndexVersionResult{}, classifyGORMRetrieval(ctx, err, "RETRIEVAL_INDEX_TRANSACTION_FAILED")
	}
	return result, nil
}

func (repository *GORMRepository) BeginWorkspaceSnapshot(
	ctx context.Context,
	command domain.WorkspaceSnapshotCommand,
) (domain.WorkspaceSnapshotResult, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.WorkspaceSnapshotResult{}, err
	}
	result, err := repository.native.BeginWorkspaceSnapshot(ctx, command)
	if err != nil {
		return domain.WorkspaceSnapshotResult{}, classifyGORMRetrieval(ctx, err, "REINDEX_SNAPSHOT_TRANSACTION_FAILED")
	}
	return result, nil
}

func (repository *GORMRepository) AcquireSourceRefresh(
	ctx context.Context,
	workspaceID foundation.ID,
) (application.SourceRefreshLease, error) {
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	lease, err := repository.native.AcquireSourceRefresh(ctx, workspaceID)
	if err != nil {
		return nil, classifyGORMRetrieval(ctx, err, "SOURCE_REFRESH_LOCK_FAILED")
	}
	return lease, nil
}

func (repository *GORMRepository) within(
	ctx context.Context,
	options foundation.TransactionOptions,
	transactionCode string,
	commitCode string,
	work func(context.Context, *gorm.DB, foundation.TransactionScope) error,
) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	return withinGORMRetrievalWithCommitCode(ctx, repository.unitOfWork, options, transactionCode, func() string {
		return commitCode
	}, work)
}

func (repository *GORMRepository) withinWithCommitCode(
	ctx context.Context,
	options foundation.TransactionOptions,
	transactionCode string,
	commitCode func() string,
	work func(context.Context, *gorm.DB, foundation.TransactionScope) error,
) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	return withinGORMRetrievalWithCommitCode(ctx, repository.unitOfWork, options, transactionCode, commitCode, work)
}

func withinGORMRetrieval(
	ctx context.Context,
	unitOfWork foundation.UnitOfWork,
	options foundation.TransactionOptions,
	transactionCode string,
	commitCode string,
	work func(context.Context, *gorm.DB, foundation.TransactionScope) error,
) error {
	return withinGORMRetrievalWithCommitCode(ctx, unitOfWork, options, transactionCode, func() string {
		return commitCode
	}, work)
}

func withinGORMRetrievalWithCommitCode(
	ctx context.Context,
	unitOfWork foundation.UnitOfWork,
	options foundation.TransactionOptions,
	transactionCode string,
	commitCode func() string,
	work func(context.Context, *gorm.DB, foundation.TransactionScope) error,
) error {
	if ctx == nil {
		return foundation.NewError(foundation.ErrorInvalidInput, "RETRIEVAL_CONTEXT_INVALID", false, errors.New("retrieval context is nil"))
	}
	if nilGORMRetrievalDependency(unitOfWork) {
		return gormRetrievalUnavailable(transactionCode, errors.New("retrieval unit of work is unavailable"))
	}
	if commitCode == nil {
		return foundation.NewError(foundation.ErrorInvalidInput, "RETRIEVAL_COMMIT_CODE_INVALID", false, errors.New("retrieval commit code callback is nil"))
	}
	if work == nil {
		return foundation.NewError(foundation.ErrorInvalidInput, "RETRIEVAL_TRANSACTION_CALLBACK_INVALID", false, errors.New("retrieval transaction callback is nil"))
	}

	callbackSucceeded := false
	err := unitOfWork.Within(ctx, options, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		transaction, unwrapErr := platformpostgres.GORMTransaction(scope)
		if unwrapErr != nil {
			return gormRetrievalUnavailable(transactionCode, errors.New("retrieval scoped transaction is unavailable"))
		}
		if err := work(callbackCtx, transaction.WithContext(callbackCtx), scope); err != nil {
			return err
		}
		callbackSucceeded = true
		return nil
	})
	if err == nil {
		return nil
	}
	if callbackSucceeded {
		return classifyGORMRetrieval(ctx, err, commitCode())
	}
	return classifyGORMRetrieval(ctx, err, transactionCode)
}

func validGORMRetrievalDatabase(database *gorm.DB) bool {
	return database != nil && database.Config != nil && database.Statement != nil &&
		database.Config.ConnPool != nil && database.Statement.ConnPool != nil && database.Error == nil
}

func nilGORMRetrievalDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func gormRetrievalRawRow(ctx context.Context, database *gorm.DB, query string, arguments ...any) (*sql.Row, error) {
	if ctx == nil || !validGORMRetrievalDatabase(database) {
		return nil, errors.New("retrieval GORM row query is unavailable")
	}
	statement := database.WithContext(ctx).Raw(query, arguments...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	row := statement.Row()
	if row == nil {
		return nil, errors.New("retrieval GORM query returned no row handle")
	}
	return row, nil
}

func gormRetrievalRawRows(ctx context.Context, database *gorm.DB, query string, arguments ...any) (*sql.Rows, error) {
	if ctx == nil || !validGORMRetrievalDatabase(database) {
		return nil, errors.New("retrieval GORM rows query is unavailable")
	}
	statement := database.WithContext(ctx).Raw(query, arguments...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	rows, err := statement.Rows()
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return nil, errors.New("retrieval GORM query returned no rows handle")
	}
	return rows, nil
}

func gormRetrievalExec(ctx context.Context, database *gorm.DB, query string, arguments ...any) (int64, error) {
	if ctx == nil || !validGORMRetrievalDatabase(database) {
		return 0, errors.New("retrieval GORM statement is unavailable")
	}
	result := database.WithContext(ctx).Exec(query, arguments...)
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

func withGORMRetrievalRows(
	ctx context.Context,
	database *gorm.DB,
	queryCode string,
	scanCode string,
	query string,
	arguments []any,
	consume func(*sql.Rows) error,
) error {
	if consume == nil {
		return foundation.NewError(foundation.ErrorInvalidInput, "RETRIEVAL_ROWS_CALLBACK_INVALID", false, errors.New("retrieval rows callback is nil"))
	}
	rows, err := gormRetrievalRawRows(ctx, database, query, arguments...)
	if err != nil {
		return classifyGORMRetrieval(ctx, err, queryCode)
	}
	consumeErr := consume(rows)
	rowsErr := rows.Err()
	closeErr := rows.Close()
	if consumeErr != nil {
		return classifyGORMRetrieval(ctx, consumeErr, scanCode)
	}
	if rowsErr != nil {
		return classifyGORMRetrieval(ctx, rowsErr, scanCode)
	}
	if closeErr != nil {
		return classifyGORMRetrieval(ctx, closeErr, scanCode)
	}
	return nil
}

func gormRetrievalNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound)
}

func classifyGORMRetrieval(ctx context.Context, cause error, code string) error {
	if cause == nil {
		return nil
	}
	var classified *foundation.Error
	if contextCause := gormRetrievalContextCause(ctx, cause); contextCause != nil {
		if errors.As(cause, &classified) && classified != nil && classified.Code != "" {
			code = classified.Code
		}
		if errors.Is(contextCause, context.Canceled) {
			return foundation.NewError(foundation.ErrorNonRetryableFailure, code, false, contextCause)
		}
		return foundation.NewError(foundation.ErrorRetryableFailure, code, true, contextCause)
	}
	if errors.As(cause, &classified) {
		return cause
	}
	if gormRetrievalNoRows(cause) {
		return notFound(code, cause)
	}
	if state := platformpostgres.SQLState(cause); state != "" {
		switch state {
		case "40001", "40P01", "55P03":
			return foundation.NewError(foundation.ErrorRetryableFailure, code, true, cause)
		case "23505":
			return conflict(code, cause)
		case "23503", "23514", "55000":
			return consistency(code, cause)
		}
		return gormRetrievalUnavailable(code, cause)
	}
	if errors.Is(cause, sql.ErrTxDone) {
		return gormRetrievalUnavailable(code, cause)
	}
	return gormRetrievalUnavailable(code, fmt.Errorf("retrieval database operation failed: %T", cause))
}

func gormRetrievalContextCause(ctx context.Context, cause error) error {
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

func gormRetrievalUnavailable(code string, cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, cause)
}
