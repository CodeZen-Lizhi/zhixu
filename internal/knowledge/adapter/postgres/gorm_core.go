package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
)

// GORMRepository is the staged Knowledge persistence implementation.
// Production composition remains on Repository until the PostgreSQL gate passes.
type GORMRepository struct {
	database   *gorm.DB
	unitOfWork foundation.UnitOfWork
}

type gormKnowledgeErrorClassifier func(context.Context, error, string) error

// NewGORMRepository derives the GORM root and transaction boundary from one
// platform Pool. It opens no connection and performs no schema mutation.
func NewGORMRepository(pool *platformpostgres.Pool) (*GORMRepository, error) {
	if pool == nil {
		return nil, knowledgeGORMUnavailable(errors.New("knowledge PostgreSQL pool is unavailable"))
	}
	database, err := pool.GORM()
	if err != nil {
		return nil, knowledgeGORMUnavailable(fmt.Errorf("knowledge GORM database is unavailable: %w", err))
	}
	unitOfWork, err := pool.UnitOfWork()
	if err != nil {
		return nil, knowledgeGORMUnavailable(fmt.Errorf("knowledge GORM unit of work is unavailable: %w", err))
	}
	if !validKnowledgeGORMDatabase(database) || isNilKnowledgeGORMDependency(unitOfWork) {
		return nil, knowledgeGORMUnavailable(errors.New("knowledge GORM dependencies are unavailable"))
	}
	return &GORMRepository{database: database, unitOfWork: unitOfWork}, nil
}

func (repository *GORMRepository) ready(ctx context.Context) error {
	if repository == nil || !validKnowledgeGORMDatabase(repository.database) || isNilKnowledgeGORMDependency(repository.unitOfWork) {
		return knowledgeGORMUnavailable(errors.New("knowledge GORM repository is unavailable"))
	}
	if ctx == nil {
		return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeRequestInvalid, false, errors.New("knowledge context is nil"))
	}
	return nil
}

func (repository *GORMRepository) within(
	ctx context.Context,
	options foundation.TransactionOptions,
	transactionCode string,
	commitCode string,
	classifier gormKnowledgeErrorClassifier,
	work func(context.Context, foundation.TransactionScope, *gorm.DB) error,
) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	return withinGORMKnowledge(ctx, repository.unitOfWork, options, transactionCode, commitCode, classifier, work)
}

func withinGORMKnowledge(
	ctx context.Context,
	unitOfWork foundation.UnitOfWork,
	options foundation.TransactionOptions,
	transactionCode string,
	commitCode string,
	classifier gormKnowledgeErrorClassifier,
	work func(context.Context, foundation.TransactionScope, *gorm.DB) error,
) error {
	if ctx == nil || isNilKnowledgeGORMDependency(unitOfWork) || work == nil || classifier == nil || transactionCode == "" || commitCode == "" {
		return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeRequestInvalid, false, errors.New("knowledge transaction boundary is invalid"))
	}
	callbackSucceeded := false
	err := unitOfWork.Within(ctx, options, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		transaction, unwrapErr := platformpostgres.GORMTransaction(scope)
		if unwrapErr != nil {
			return knowledgeGORMUnavailable(fmt.Errorf("knowledge scoped transaction is unavailable: %w", unwrapErr))
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

func validKnowledgeGORMDatabase(database *gorm.DB) bool {
	return database != nil && database.Config != nil && database.Statement != nil &&
		database.Config.ConnPool != nil && database.Statement.ConnPool != nil && database.Error == nil
}

func gormKnowledgeRawRow(ctx context.Context, database *gorm.DB, query string, arguments ...any) (*sql.Row, error) {
	if ctx == nil || !validKnowledgeGORMDatabase(database) {
		return nil, errors.New("knowledge GORM row query is unavailable")
	}
	statement := database.WithContext(ctx).Raw(query, arguments...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	row := statement.Row()
	if row == nil {
		return nil, errors.New("knowledge GORM row query returned no row handle")
	}
	return row, nil
}

func gormKnowledgeRawRows(ctx context.Context, database *gorm.DB, query string, arguments ...any) (*sql.Rows, error) {
	if ctx == nil || !validKnowledgeGORMDatabase(database) {
		return nil, errors.New("knowledge GORM rows query is unavailable")
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
		return nil, errors.New("knowledge GORM rows query returned no rows handle")
	}
	return rows, nil
}

func gormKnowledgeExec(ctx context.Context, database *gorm.DB, query string, arguments ...any) (int64, error) {
	if ctx == nil || !validKnowledgeGORMDatabase(database) {
		return 0, errors.New("knowledge GORM statement is unavailable")
	}
	result := database.WithContext(ctx).Exec(query, arguments...)
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

func gormKnowledgeNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound)
}

func classifyGORMKnowledge(ctx context.Context, err error, fallbackCode string) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		// UnitOfWork may add a transaction boundary wrapper around callback
		// errors. Return the project envelope itself so callers retain the
		// established foundation.Error contract and its original cause chain.
		return classified
	}
	if cause := knowledgeGORMContextCause(ctx, err); cause != nil {
		if errors.Is(cause, context.Canceled) {
			return foundation.NewError(foundation.ErrorNonRetryableFailure, fallbackCode, false, cause)
		}
		if errors.Is(cause, context.DeadlineExceeded) {
			return foundation.NewError(foundation.ErrorRetryableFailure, fallbackCode, true, cause)
		}
		return foundation.NewError(foundation.ErrorDependencyUnavailable, fallbackCode, false, cause)
	}
	if errors.Is(err, sql.ErrTxDone) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, fallbackCode, true, err)
	}
	return classify(err, fallbackCode)
}

func classifyGORMRelationApply(ctx context.Context, err error, fallbackCode string) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified
	}
	cause := knowledgeGORMContextCause(ctx, err)
	if cause != nil {
		if errors.Is(cause, context.Canceled) {
			return foundation.NewError(foundation.ErrorNonRetryableFailure, fallbackCode, false, cause)
		}
		if errors.Is(cause, context.DeadlineExceeded) {
			return foundation.NewError(foundation.ErrorRetryableFailure, fallbackCode, true, cause)
		}
	}
	if errors.Is(err, sql.ErrTxDone) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, fallbackCode, true, err)
	}
	return relationApplyClassify(err, fallbackCode)
}

func knowledgeGORMContextCause(ctx context.Context, err error) error {
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

func knowledgeGORMUnavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, errorCodeDatabaseUnavailable, true, cause)
}

func isNilKnowledgeGORMDependency(value any) bool {
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
