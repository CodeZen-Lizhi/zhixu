package postgres

import (
	"context"
	"database/sql"
	"errors"
	"reflect"

	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workspaceapp "github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// GORMRepository is the staged Capture implementation backed by one shared
// platform Pool. Production composition remains on Repository until TODO 9.
type GORMRepository struct {
	database        *gorm.DB
	unitOfWork      foundation.UnitOfWork
	workspaceWriter workspaceapp.ScopedSourceWriter
}

// NewGORMRepository constructs staged Capture persistence from one platform
// Pool and a Workspace writer that joins caller-owned transaction scopes.
func NewGORMRepository(pool *platformpostgres.Pool, workspaceWriter workspaceapp.ScopedSourceWriter) (*GORMRepository, error) {
	if pool == nil || nilCaptureDependency(workspaceWriter) {
		return nil, gormCaptureUnavailable(errors.New("capture GORM dependencies are unavailable"))
	}
	database, err := pool.GORM()
	if err != nil {
		return nil, gormCaptureUnavailable(errors.New("capture GORM database is unavailable"))
	}
	unitOfWork, err := pool.UnitOfWork()
	if err != nil {
		return nil, gormCaptureUnavailable(errors.New("capture GORM unit of work is unavailable"))
	}
	repository := &GORMRepository{
		database:        database,
		unitOfWork:      unitOfWork,
		workspaceWriter: workspaceWriter,
	}
	if !validCaptureGORMDatabase(repository.database) || nilCaptureDependency(repository.unitOfWork) {
		return nil, gormCaptureUnavailable(errors.New("capture GORM transaction boundary is unavailable"))
	}
	return repository, nil
}

func (repository *GORMRepository) ready(ctx context.Context) error {
	if repository == nil || !validCaptureGORMDatabase(repository.database) ||
		nilCaptureDependency(repository.unitOfWork) || nilCaptureDependency(repository.workspaceWriter) {
		return gormCaptureUnavailable(errors.New("capture GORM repository is unavailable"))
	}
	if ctx == nil {
		return invalid("CAPTURE_CONTEXT_INVALID", "capture context is nil")
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
		return invalid("CAPTURE_TRANSACTION_INVALID", "capture transaction callback is nil")
	}
	return repository.unitOfWork.Within(ctx, options, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		transaction, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return gormCaptureUnavailable(errors.New("capture scoped transaction is unavailable"))
		}
		return work(callbackCtx, transaction.WithContext(callbackCtx), scope)
	})
}

func validCaptureGORMDatabase(database *gorm.DB) bool {
	return database != nil && database.Config != nil && database.Statement != nil &&
		database.Config.ConnPool != nil && database.Statement.ConnPool != nil && database.Error == nil
}

func nilCaptureDependency(value any) bool {
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

func gormCaptureRawRow(database *gorm.DB, query string, arguments ...any) (*sql.Row, error) {
	if !validCaptureGORMDatabase(database) {
		return nil, errors.New("capture GORM database is unavailable")
	}
	statement := database.Raw(query, arguments...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	row := statement.Row()
	if row == nil {
		return nil, errors.New("capture GORM query returned no row handle")
	}
	return row, nil
}

func gormCaptureRows(database *gorm.DB, query string, arguments ...any) (*sql.Rows, error) {
	if !validCaptureGORMDatabase(database) {
		return nil, errors.New("capture GORM database is unavailable")
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
		return nil, errors.New("capture GORM query returned no rows handle")
	}
	return rows, nil
}

func gormCaptureNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound)
}

func classifyGORMCapture(ctx context.Context, err error, code string) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if cause := gormCaptureContextCause(ctx, err); cause != nil {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, code, false, cause)
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23505":
			return foundation.NewError(foundation.ErrorVersionConflict, code, false, err)
		case "23503", "23514", "55000":
			return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, err)
		case "40001", "40P01", "55P03":
			return foundation.NewError(foundation.ErrorRetryableFailure, code, true, err)
		}
	}
	if errors.Is(err, sql.ErrTxDone) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, err)
	}
	return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, err)
}

func gormCaptureContextCause(ctx context.Context, err error) error {
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

func gormCaptureUnavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, "CAPTURE_REPOSITORY_UNAVAILABLE", true, cause)
}

var _ captureapp.Repository = (*GORMRepository)(nil)
var _ captureapp.RetryScheduler = (*GORMRepository)(nil)
var _ captureapp.OutboxStore = (*GORMRepository)(nil)
var _ captureapp.ProcessingRepository = (*GORMRepository)(nil)
