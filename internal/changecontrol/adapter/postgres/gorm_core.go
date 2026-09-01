package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	eventsapplication "github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
)

// GORMRepository is the staged Change Control persistence implementation.
// Production composition remains on Repository until the PostgreSQL gate passes.
type GORMRepository struct {
	database   *gorm.DB
	unitOfWork foundation.UnitOfWork
	events     eventsapplication.ScopedAppender
}

type gormChangeErrorClassifier func(context.Context, error, string) error

// NewGORMRepository derives the GORM root and transaction boundary from one
// platform Pool. It opens no connection and performs no schema mutation.
func NewGORMRepository(pool *platformpostgres.Pool, appenders ...eventsapplication.ScopedAppender) (*GORMRepository, error) {
	if pool == nil {
		return nil, changeControlGORMUnavailable(errors.New("change control PostgreSQL pool is unavailable"))
	}
	if len(appenders) > 1 {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, "CHANGE_CONTROL_EVENT_APPENDER_INVALID", false, errors.New("only one change control event appender may be configured"))
	}
	database, err := pool.GORM()
	if err != nil {
		return nil, changeControlGORMUnavailable(fmt.Errorf("change control GORM database is unavailable: %w", err))
	}
	unitOfWork, err := pool.UnitOfWork()
	if err != nil {
		return nil, changeControlGORMUnavailable(fmt.Errorf("change control GORM unit of work is unavailable: %w", err))
	}
	if !validChangeGORMDatabase(database) || isNilChangeControlDependency(unitOfWork) {
		return nil, changeControlGORMUnavailable(errors.New("change control GORM dependencies are unavailable"))
	}
	var events eventsapplication.ScopedAppender
	if len(appenders) == 1 {
		if isNilChangeControlDependency(appenders[0]) {
			return nil, foundation.NewError(foundation.ErrorInvalidInput, "CHANGE_CONTROL_EVENT_APPENDER_INVALID", false, errors.New("change control event appender is nil"))
		}
		events = appenders[0]
	}
	return &GORMRepository{database: database, unitOfWork: unitOfWork, events: events}, nil
}

func (repository *GORMRepository) ready(ctx context.Context) error {
	if repository == nil || !validChangeGORMDatabase(repository.database) || isNilChangeControlDependency(repository.unitOfWork) {
		return changeControlGORMUnavailable(errors.New("change control GORM repository is unavailable"))
	}
	if ctx == nil {
		return foundation.NewError(foundation.ErrorInvalidInput, "CHANGE_CONTROL_CONTEXT_INVALID", false, errors.New("change control context is nil"))
	}
	return nil
}

func (repository *GORMRepository) within(
	ctx context.Context,
	options foundation.TransactionOptions,
	transactionCode string,
	commitCode string,
	classifier gormChangeErrorClassifier,
	work func(context.Context, foundation.TransactionScope, *gorm.DB) error,
) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	if work == nil {
		return foundation.NewError(foundation.ErrorInvalidInput, "CHANGE_CONTROL_TRANSACTION_INVALID", false, errors.New("change control transaction callback is nil"))
	}
	if transactionCode == "" || commitCode == "" || classifier == nil {
		return foundation.NewError(foundation.ErrorInvalidInput, "CHANGE_CONTROL_TRANSACTION_INVALID", false, errors.New("change control transaction error policy is invalid"))
	}
	callbackSucceeded := false
	err := repository.unitOfWork.Within(ctx, options, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		transaction, unwrapErr := platformpostgres.GORMTransaction(scope)
		if unwrapErr != nil {
			return changeControlGORMUnavailable(fmt.Errorf("change control scoped transaction is unavailable: %w", unwrapErr))
		}
		workErr := work(callbackCtx, scope, transaction.WithContext(callbackCtx))
		callbackSucceeded = workErr == nil
		return workErr
	})
	if callbackSucceeded {
		return classifier(ctx, err, commitCode)
	}
	return classifier(ctx, err, transactionCode)
}

func validChangeGORMDatabase(database *gorm.DB) bool {
	return database != nil && database.Config != nil && database.Statement != nil &&
		database.Config.ConnPool != nil && database.Statement.ConnPool != nil && database.Error == nil
}

func gormChangeRawRow(ctx context.Context, database *gorm.DB, query string, arguments ...any) (*sql.Row, error) {
	if ctx == nil || !validChangeGORMDatabase(database) {
		return nil, errors.New("change control GORM row query is unavailable")
	}
	statement := database.WithContext(ctx).Raw(query, arguments...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	row := statement.Row()
	if row == nil {
		return nil, errors.New("change control GORM row query returned no row handle")
	}
	return row, nil
}

func gormChangeRawRows(ctx context.Context, database *gorm.DB, query string, arguments ...any) (*sql.Rows, error) {
	if ctx == nil || !validChangeGORMDatabase(database) {
		return nil, errors.New("change control GORM rows query is unavailable")
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
		return nil, errors.New("change control GORM rows query returned no rows handle")
	}
	return rows, nil
}

func gormChangeExec(ctx context.Context, database *gorm.DB, query string, arguments ...any) (int64, error) {
	if ctx == nil || !validChangeGORMDatabase(database) {
		return 0, errors.New("change control GORM statement is unavailable")
	}
	result := database.WithContext(ctx).Exec(query, arguments...)
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

func gormChangeNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound)
}

func classifyGORMChange(ctx context.Context, err error, fallbackCode string) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if cause := changeControlGORMContextCause(ctx, err); cause != nil {
		return foundation.NewError(foundation.ErrorNonRetryableFailure, fallbackCode, false, cause)
	}
	if errors.Is(err, sql.ErrTxDone) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, fallbackCode, true, err)
	}
	return classify(err, fallbackCode)
}

func classifyGORMAuthorization(ctx context.Context, err error, fallbackCode string) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if cause := changeControlGORMContextCause(ctx, err); cause != nil {
		return foundation.NewError(foundation.ErrorNonRetryableFailure, fallbackCode, false, cause)
	}
	if errors.Is(err, sql.ErrTxDone) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, fallbackCode, true, err)
	}
	return classifyAuthorization(err, fallbackCode)
}

func classifyGORMWriteback(ctx context.Context, err error, fallbackCode string) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if cause := changeControlGORMContextCause(ctx, err); cause != nil {
		return foundation.NewError(foundation.ErrorNonRetryableFailure, fallbackCode, false, cause)
	}
	if errors.Is(err, sql.ErrTxDone) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, fallbackCode, true, err)
	}
	return classifyWriteback(err, fallbackCode)
}

func changeControlGORMContextCause(ctx context.Context, err error) error {
	causes := make([]error, 0, 2)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		causes = append(causes, err)
	}
	if ctx != nil && ctx.Err() != nil {
		cause := context.Cause(ctx)
		if cause == nil {
			cause = ctx.Err()
		} else if !errors.Is(cause, ctx.Err()) {
			cause = errors.Join(ctx.Err(), cause)
		}
		causes = append(causes, cause)
	}
	if len(causes) == 0 {
		return nil
	}
	return errors.Join(causes...)
}

func changeControlGORMUnavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, "CHANGE_CONTROL_DATABASE_UNAVAILABLE", true, cause)
}
