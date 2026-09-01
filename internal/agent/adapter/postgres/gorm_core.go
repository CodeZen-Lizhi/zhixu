package postgres

import (
	"context"
	"database/sql"
	"errors"
	"reflect"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// GORMRepository is the staged Agent persistence implementation. Production
// composition remains on Repository until the PostgreSQL parity gate passes.
type GORMRepository struct {
	database   *gorm.DB
	unitOfWork foundation.UnitOfWork
}

// NewGORMRepository derives the GORM root and transaction boundary from one
// platform Pool so staged operations cannot accidentally use a second pool.
func NewGORMRepository(pool *platformpostgres.Pool) (*GORMRepository, error) {
	if pool == nil {
		return nil, gormUnavailable(errors.New("agent PostgreSQL pool is unavailable"))
	}
	database, err := pool.GORM()
	if err != nil {
		return nil, gormUnavailable(errors.Join(errors.New("agent GORM database is unavailable"), err))
	}
	unitOfWork, err := pool.UnitOfWork()
	if err != nil {
		return nil, gormUnavailable(errors.Join(errors.New("agent GORM unit of work is unavailable"), err))
	}
	if !validAgentGORMDatabase(database) || nilAgentDependency(unitOfWork) {
		return nil, gormUnavailable(errors.New("agent GORM dependencies are unavailable"))
	}
	return &GORMRepository{database: database, unitOfWork: unitOfWork}, nil
}

func (repository *GORMRepository) ready(ctx context.Context) error {
	if repository == nil || !validAgentGORMDatabase(repository.database) || nilAgentDependency(repository.unitOfWork) {
		return gormUnavailable(errors.New("agent GORM repository is unavailable"))
	}
	if ctx == nil {
		return foundation.NewError(
			foundation.ErrorInvalidInput,
			domain.ErrorCodeModelRunInvalid,
			false,
			errors.New("agent context is nil"),
		)
	}
	return nil
}

func (repository *GORMRepository) readyModelCall(ctx context.Context) error {
	if repository == nil || !validAgentGORMDatabase(repository.database) || nilAgentDependency(repository.unitOfWork) {
		return gormUnavailable(errors.New("agent GORM repository is unavailable"))
	}
	if ctx == nil {
		return foundation.NewError(
			foundation.ErrorInvalidInput,
			domain.ErrorCodeModelCallInvalid,
			false,
			errors.New("agent model call context is nil"),
		)
	}
	return nil
}

func (repository *GORMRepository) within(
	ctx context.Context,
	options foundation.TransactionOptions,
	work func(context.Context, *gorm.DB, foundation.TransactionScope) error,
) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	if work == nil {
		return foundation.NewError(
			foundation.ErrorInvalidInput,
			domain.ErrorCodeModelRunInvalid,
			false,
			errors.New("agent transaction callback is nil"),
		)
	}
	err := repository.unitOfWork.Within(ctx, options, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		transaction, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return gormUnavailable(errors.Join(errors.New("agent scoped transaction is unavailable"), err))
		}
		return work(callbackCtx, transaction.WithContext(callbackCtx), scope)
	})
	return classifyGORM(ctx, err)
}

func validAgentGORMDatabase(database *gorm.DB) bool {
	return database != nil && database.Config != nil && database.Statement != nil &&
		database.Config.ConnPool != nil && database.Statement.ConnPool != nil && database.Error == nil
}

func nilAgentDependency(value any) bool {
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

func gormRawRow(database *gorm.DB, query string, arguments ...any) (*sql.Row, error) {
	if !validAgentGORMDatabase(database) {
		return nil, errors.New("agent GORM database is unavailable")
	}
	statement := database.Raw(query, arguments...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	row := statement.Row()
	if row == nil {
		return nil, errors.New("agent GORM query returned no row handle")
	}
	return row, nil
}

func gormRawRows(database *gorm.DB, query string, arguments ...any) (*sql.Rows, error) {
	if !validAgentGORMDatabase(database) {
		return nil, errors.New("agent GORM database is unavailable")
	}
	statement := database.Raw(query, arguments...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	rows, err := statement.Rows()
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return nil, errors.New("agent GORM query returned no rows handle")
	}
	return rows, nil
}

func gormNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound)
}

func classifyGORM(ctx context.Context, cause error) error {
	if cause == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(cause, &classified) {
		return cause
	}
	if contextCause := agentContextCause(ctx, cause); contextCause != nil {
		if errors.Is(contextCause, context.Canceled) {
			return foundation.NewError(foundation.ErrorNonRetryableFailure, "AGENT_DATABASE_CANCELLED", false, contextCause)
		}
		return foundation.NewError(foundation.ErrorRetryableFailure, "AGENT_DATABASE_TIMEOUT", true, contextCause)
	}
	var postgresError *pgconn.PgError
	if errors.As(cause, &postgresError) {
		switch postgresError.Code {
		case "40001", "40P01", "55P03":
			return foundation.NewError(foundation.ErrorRetryableFailure, ErrorCodeDatabaseUnavailable, true, cause)
		case "23505":
			return foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeRuntimeReplayConflict, false, cause)
		case "23503", "23514", "55000":
			return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeRuntimeConsistency, false, cause)
		}
	}
	if errors.Is(cause, sql.ErrTxDone) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeDatabaseUnavailable, true, cause)
	}
	return foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeDatabaseUnavailable, true, cause)
}

func agentContextCause(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		cause := context.Cause(ctx)
		if cause == nil {
			return ctx.Err()
		}
		if !errors.Is(cause, ctx.Err()) {
			return errors.Join(ctx.Err(), cause)
		}
		return cause
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return nil
}

func gormUnavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeDatabaseUnavailable, true, cause)
}
