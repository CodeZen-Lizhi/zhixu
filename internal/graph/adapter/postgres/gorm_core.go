package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
)

// GORMRepository 从共享 Pool 读取 canonical Knowledge 并持久化 Graph 事实。
type GORMRepository struct {
	database         *gorm.DB
	unitOfWork       foundation.UnitOfWork
	statementTimeout time.Duration
}

type gormGraphTransactionStage uint8

const (
	gormGraphTransactionStageCallback gormGraphTransactionStage = iota
	gormGraphTransactionStageCommit
)

type gormGraphErrorClassifier func(context.Context, error, gormGraphTransactionStage) error

// NewGORMRepository derives both database handles from one complete platform
// Pool. It opens no connection and owns no transaction lifecycle itself.
func NewGORMRepository(pool *platformpostgres.Pool) (*GORMRepository, error) {
	if pool == nil {
		return nil, unavailable(errors.New("graph PostgreSQL pool is unavailable"))
	}
	database, err := pool.GORM()
	if err != nil {
		return nil, unavailable(fmt.Errorf("graph GORM database is unavailable: %w", err))
	}
	unitOfWork, err := pool.UnitOfWork()
	if err != nil {
		return nil, unavailable(fmt.Errorf("graph GORM unit of work is unavailable: %w", err))
	}
	if !validGraphGORMDatabase(database) || nilGraphGORMDependency(unitOfWork) {
		return nil, unavailable(errors.New("graph GORM dependencies are unavailable"))
	}
	return &GORMRepository{
		database:         database,
		unitOfWork:       unitOfWork,
		statementTimeout: defaultStatementTimeout,
	}, nil
}

func (repository *GORMRepository) ready(ctx context.Context) error {
	if repository == nil || !validGraphGORMDatabase(repository.database) || nilGraphGORMDependency(repository.unitOfWork) {
		return unavailable(errors.New("graph GORM repository is unavailable"))
	}
	if ctx == nil {
		return graphGORMInvalid(errors.New("graph context is nil"))
	}
	return nil
}

// within keeps callback/begin failures distinct from a commit result failure.
// Business siblings choose their stable error family through classifier.
func (repository *GORMRepository) within(
	ctx context.Context,
	options foundation.TransactionOptions,
	classifier gormGraphErrorClassifier,
	work func(context.Context, foundation.TransactionScope, *gorm.DB) error,
) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	if classifier == nil || work == nil {
		return graphGORMInvalid(errors.New("graph GORM transaction boundary is invalid"))
	}

	callbackSucceeded := false
	err := repository.unitOfWork.Within(ctx, options, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		transaction, unwrapErr := platformpostgres.GORMTransaction(scope)
		if unwrapErr != nil {
			return fmt.Errorf("graph scoped transaction is unavailable: %w", unwrapErr)
		}
		workErr := work(callbackCtx, scope, transaction.WithContext(callbackCtx))
		callbackSucceeded = workErr == nil
		return workErr
	})
	stage := gormGraphTransactionStageCallback
	if callbackSucceeded {
		stage = gormGraphTransactionStageCommit
	}
	return classifier(ctx, err, stage)
}

// gormReadSnapshot executes a complete Graph query in one repeatable-read,
// read-only transaction with the legacy transaction-local timeout.
func (repository *GORMRepository) gormReadSnapshot(ctx context.Context, work func(context.Context, *gorm.DB) error) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	if work == nil {
		return graphGORMInvalid(errors.New("graph GORM read callback is nil"))
	}
	timeout, err := graphGORMStatementTimeout(repository.statementTimeout)
	if err != nil {
		return graphGORMInvalid(err)
	}
	return repository.within(
		ctx,
		foundation.TransactionOptions{Isolation: foundation.TransactionIsolationRepeatableRead, ReadOnly: true},
		func(classifierCtx context.Context, cause error, _ gormGraphTransactionStage) error {
			return classifyGORM(classifierCtx, cause)
		},
		func(callbackCtx context.Context, _ foundation.TransactionScope, transaction *gorm.DB) error {
			if _, execErr := gormGraphExec(callbackCtx, transaction, setLocalStatementTimeoutSQL, sql.Named("p1", timeout)); execErr != nil {
				return execErr
			}
			return work(callbackCtx, transaction)
		},
	)
}

func graphGORMStatementTimeout(timeout time.Duration) (string, error) {
	if timeout < time.Millisecond {
		return "", errors.New("graph statement timeout must be at least one millisecond")
	}
	return strconv.FormatInt(timeout.Milliseconds(), 10) + "ms", nil
}

func validGraphGORMDatabase(database *gorm.DB) bool {
	return database != nil && database.Config != nil && database.Statement != nil &&
		database.Config.ConnPool != nil && database.Statement.ConnPool != nil && database.Error == nil
}

func nilGraphGORMDependency(value any) bool {
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

func gormGraphNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound)
}

func classifyGORM(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if cause := graphGORMContextCause(ctx, err); cause != nil {
		if errors.Is(cause, context.Canceled) {
			return foundation.NewError(foundation.ErrorNonRetryableFailure, graphdomain.ErrorCodeQueryCanceled, false, cause)
		}
		return foundation.NewError(foundation.ErrorDependencyUnavailable, graphdomain.ErrorCodeQueryTimeout, true, cause)
	}
	if errors.Is(err, sql.ErrTxDone) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, graphdomain.ErrorCodeDependencyUnavailable, true, err)
	}
	return classify(err)
}

func graphGORMContextCause(ctx context.Context, err error) error {
	hasCancellation := errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
	if ctx != nil && ctx.Err() != nil {
		hasCancellation = true
	}
	if !hasCancellation {
		return nil
	}

	causes := []error{err}
	if ctx != nil && ctx.Err() != nil {
		contextCause := context.Cause(ctx)
		if contextCause == nil {
			contextCause = ctx.Err()
		}
		if errors.Is(contextCause, ctx.Err()) {
			causes = append(causes, contextCause)
		} else {
			causes = append(causes, ctx.Err(), contextCause)
		}
	}
	return errors.Join(causes...)
}

func graphGORMInvalid(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, graphdomain.ErrorCodeRequestInvalid, false, cause)
}
