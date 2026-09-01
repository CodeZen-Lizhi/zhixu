package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
	"gorm.io/gorm"
)

// GORMRepository is the staged Interview persistence adapter. Production
// composition remains on the legacy pgx Repository until the TODO 9 gate.
type GORMRepository struct {
	database   *gorm.DB
	unitOfWork foundation.UnitOfWork
}

// NewGORMRepository derives the GORM root and transaction boundary from one
// platform Pool. It opens no additional connection or schema migration path.
func NewGORMRepository(pool *platformpostgres.Pool) (*GORMRepository, error) {
	if pool == nil {
		return nil, gormInterviewUnavailable(errors.New("interview PostgreSQL pool is unavailable"))
	}
	database, err := pool.GORM()
	if err != nil {
		return nil, gormInterviewUnavailable(fmt.Errorf("interview GORM database is unavailable: %w", err))
	}
	unitOfWork, err := pool.UnitOfWork()
	if err != nil {
		return nil, gormInterviewUnavailable(fmt.Errorf("interview GORM unit of work is unavailable: %w", err))
	}
	if !validGORMInterviewDatabase(database) || nilGORMInterviewDependency(unitOfWork) {
		return nil, gormInterviewUnavailable(errors.New("interview GORM dependencies are unavailable"))
	}
	return &GORMRepository{database: database, unitOfWork: unitOfWork}, nil
}

func (repository *GORMRepository) ready(ctx context.Context) error {
	if repository == nil || !validGORMInterviewDatabase(repository.database) || nilGORMInterviewDependency(repository.unitOfWork) {
		return gormInterviewUnavailable(errors.New("interview GORM repository is unavailable"))
	}
	if ctx == nil {
		return domain.InvalidError(domain.ErrorCodeConfigInvalid, "interview context is nil")
	}
	return nil
}

// within is the only transaction boundary for staged Interview write flows.
func (repository *GORMRepository) within(
	ctx context.Context,
	work func(context.Context, *gorm.DB) error,
) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	if work == nil {
		return domain.InvalidError(domain.ErrorCodeConfigInvalid, "interview transaction callback is nil")
	}
	err := repository.unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		transaction, unwrapErr := platformpostgres.GORMTransaction(scope)
		if unwrapErr != nil {
			return gormInterviewUnavailable(fmt.Errorf("interview scoped transaction is unavailable: %w", unwrapErr))
		}
		return work(callbackCtx, transaction.WithContext(callbackCtx))
	})
	return gormInterviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
}

func validGORMInterviewDatabase(database *gorm.DB) bool {
	return database != nil && database.Config != nil && database.Statement != nil &&
		database.Config.ConnPool != nil && database.Statement.ConnPool != nil && database.Error == nil
}

func nilGORMInterviewDependency(value any) bool {
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

func gormInterviewRawRow(ctx context.Context, database *gorm.DB, query string, arguments ...any) (*sql.Row, error) {
	if ctx == nil || !validGORMInterviewDatabase(database) {
		return nil, errors.New("interview GORM row query is unavailable")
	}
	statement := database.WithContext(ctx).Raw(query, arguments...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	row := statement.Row()
	if row == nil {
		return nil, errors.New("interview GORM query returned no row handle")
	}
	return row, nil
}

func gormInterviewRawRows(ctx context.Context, database *gorm.DB, query string, arguments ...any) (*sql.Rows, error) {
	if ctx == nil || !validGORMInterviewDatabase(database) {
		return nil, errors.New("interview GORM rows query is unavailable")
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
		return nil, errors.New("interview GORM query returned no rows handle")
	}
	return rows, nil
}

func gormInterviewExec(ctx context.Context, database *gorm.DB, query string, arguments ...any) (int64, error) {
	if ctx == nil || !validGORMInterviewDatabase(database) {
		return 0, errors.New("interview GORM statement is unavailable")
	}
	result := database.WithContext(ctx).Exec(query, arguments...)
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

func gormInterviewNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound)
}

func gormInterviewClassify(ctx context.Context, err error, fallback string) error {
	if err == nil {
		return nil
	}
	var existing *foundation.Error
	if errors.As(err, &existing) {
		return err
	}
	if cause := gormInterviewContextCause(ctx, err); cause != nil {
		if errors.Is(cause, context.Canceled) {
			return foundation.NewError(foundation.ErrorNonRetryableFailure, fallback, false, cause)
		}
		return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeDependencyUnavailable, true, cause)
	}
	if errors.Is(err, sql.ErrTxDone) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, fallback, true, err)
	}
	return classify(err, fallback)
}

func gormInterviewContextCause(ctx context.Context, err error) error {
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

func gormInterviewUnavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeDependencyUnavailable, true, cause)
}
