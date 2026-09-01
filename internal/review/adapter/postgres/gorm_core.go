package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	reviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/domain"
	"github.com/jackc/pgx/v5"
	"gorm.io/gorm"
)

// GORMRepository is the staged Review Core implementation backed by one
// shared platform Pool. Production composition remains on Repository.
type GORMRepository struct {
	database   *gorm.DB
	unitOfWork foundation.UnitOfWork
}

// NewGORMRepository derives the GORM root and transaction boundary from the
// same platform Pool.
func NewGORMRepository(pool *platformpostgres.Pool) (*GORMRepository, error) {
	if pool == nil {
		return nil, gormReviewUnavailable(errors.New("review PostgreSQL pool is unavailable"))
	}
	database, err := pool.GORM()
	if err != nil {
		return nil, gormReviewUnavailable(fmt.Errorf("review GORM database is unavailable: %w", err))
	}
	unitOfWork, err := pool.UnitOfWork()
	if err != nil {
		return nil, gormReviewUnavailable(fmt.Errorf("review GORM unit of work is unavailable: %w", err))
	}
	if !validGORMReviewDatabase(database) || nilGORMReviewDependency(unitOfWork) {
		return nil, gormReviewUnavailable(errors.New("review GORM dependencies are unavailable"))
	}
	return &GORMRepository{database: database, unitOfWork: unitOfWork}, nil
}

func (repository *GORMRepository) ready(ctx context.Context) error {
	if repository == nil || !validGORMReviewDatabase(repository.database) || nilGORMReviewDependency(repository.unitOfWork) {
		return gormReviewUnavailable(errors.New("review GORM repository is unavailable"))
	}
	if ctx == nil {
		return domain.InvalidError(domain.ErrorCodeCardInvalid, "review context is nil")
	}
	return nil
}

func (repository *GORMRepository) within(ctx context.Context, work func(context.Context, *gorm.DB) error) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	if work == nil {
		return domain.InvalidError(domain.ErrorCodeCardInvalid, "review transaction callback is nil")
	}
	err := repository.unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		transaction, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return gormReviewUnavailable(fmt.Errorf("review scoped transaction is unavailable: %w", err))
		}
		return work(callbackCtx, transaction.WithContext(callbackCtx))
	})
	return gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
}

func validGORMReviewDatabase(database *gorm.DB) bool {
	return database != nil && database.Config != nil && database.Statement != nil &&
		database.Config.ConnPool != nil && database.Statement.ConnPool != nil && database.Error == nil
}

func nilGORMReviewDependency(value any) bool {
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

func gormReviewRawRow(ctx context.Context, database *gorm.DB, query string, arguments ...any) (*sql.Row, error) {
	if ctx == nil || !validGORMReviewDatabase(database) {
		return nil, errors.New("review GORM row query is unavailable")
	}
	statement := database.WithContext(ctx).Raw(query, arguments...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	row := statement.Row()
	if row == nil {
		return nil, errors.New("review GORM query returned no row handle")
	}
	return row, nil
}

func gormReviewRawRows(ctx context.Context, database *gorm.DB, query string, arguments ...any) (*sql.Rows, error) {
	if ctx == nil || !validGORMReviewDatabase(database) {
		return nil, errors.New("review GORM rows query is unavailable")
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
		return nil, errors.New("review GORM query returned no rows handle")
	}
	return rows, nil
}

func gormReviewExec(ctx context.Context, database *gorm.DB, query string, arguments ...any) (int64, error) {
	if ctx == nil || !validGORMReviewDatabase(database) {
		return 0, errors.New("review GORM statement is unavailable")
	}
	result := database.WithContext(ctx).Exec(query, arguments...)
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

func gormReviewNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound)
}

func gormReviewClassify(ctx context.Context, err error, fallbackCode string) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if cause := gormReviewContextCause(ctx, err); cause != nil {
		if errors.Is(cause, context.Canceled) {
			return foundation.NewError(foundation.ErrorNonRetryableFailure, fallbackCode, false, cause)
		}
		return foundation.NewError(foundation.ErrorDependencyUnavailable, fallbackCode, true, cause)
	}
	if errors.Is(err, sql.ErrTxDone) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, fallbackCode, true, err)
	}
	return classify(err, fallbackCode)
}

func gormReviewContextCause(ctx context.Context, err error) error {
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

func gormReviewUnavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeDependencyUnavailable, true, cause)
}

var _ reviewapp.Repository = (*GORMRepository)(nil)
var _ reviewapp.EvidenceVerifier = (*GORMRepository)(nil)
