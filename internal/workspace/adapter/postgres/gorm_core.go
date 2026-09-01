package workspacepostgres

import (
	"context"
	"database/sql"
	"errors"

	auditapplication "github.com/CodeZen-Lizhi/zhixu/internal/audit/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"github.com/jackc/pgx/v5"
	"gorm.io/gorm"
)

// GORMRepository is the staged Workspace implementation backed by one shared
// platform Pool. Production composition remains on Repository until TODO 9.
type GORMRepository struct {
	database   *gorm.DB
	unitOfWork foundation.UnitOfWork
	grants     RootGrantResolver
	audit      auditapplication.ScopedAppender
	managed    bool
}

// GORMRepositoryOption configures staged process-local Workspace dependencies.
type GORMRepositoryOption func(*GORMRepository) error

// WithGORMRootGrantResolver gates root-bearing reads through the process grant.
func WithGORMRootGrantResolver(resolver RootGrantResolver, managed bool) GORMRepositoryOption {
	return func(repository *GORMRepository) error {
		if nilRepositoryDependency(resolver) {
			return errors.New("workspace root grant resolver is nil")
		}
		repository.grants = resolver
		repository.managed = managed
		return nil
	}
}

// WithGORMScopedAuditAppender binds root rebinding to the caller's GORM scope.
func WithGORMScopedAuditAppender(appender auditapplication.ScopedAppender) GORMRepositoryOption {
	return func(repository *GORMRepository) error {
		if nilRepositoryDependency(appender) {
			return errors.New("workspace scoped audit appender is nil")
		}
		if !nilRepositoryDependency(repository.audit) {
			return errors.New("workspace scoped audit appender is duplicated")
		}
		repository.audit = appender
		return nil
	}
}

// NewGORMRepository constructs staged Workspace persistence from one Pool.
func NewGORMRepository(pool *platformpostgres.Pool, options ...GORMRepositoryOption) (*GORMRepository, error) {
	if pool == nil {
		return nil, gormWorkspaceUnavailable(errors.New("workspace PostgreSQL pool is unavailable"))
	}
	database, err := pool.GORM()
	if err != nil {
		return nil, gormWorkspaceUnavailable(errors.New("workspace GORM database is unavailable"))
	}
	unitOfWork, err := pool.UnitOfWork()
	if err != nil {
		return nil, gormWorkspaceUnavailable(errors.New("workspace GORM unit of work is unavailable"))
	}
	repository := &GORMRepository{database: database, unitOfWork: unitOfWork}
	for _, option := range options {
		if option == nil {
			return nil, gormWorkspaceOptionInvalid(errors.New("workspace GORM repository option is nil"))
		}
		if err := option(repository); err != nil {
			return nil, gormWorkspaceOptionInvalid(err)
		}
	}
	if !validWorkspaceGORMDatabase(repository.database) || nilRepositoryDependency(repository.unitOfWork) {
		return nil, gormWorkspaceUnavailable(errors.New("workspace GORM transaction boundary is unavailable"))
	}
	return repository, nil
}

func (repository *GORMRepository) ready(ctx context.Context) error {
	if repository == nil || !validWorkspaceGORMDatabase(repository.database) || nilRepositoryDependency(repository.unitOfWork) {
		return gormWorkspaceUnavailable(errors.New("workspace GORM repository is unavailable"))
	}
	if ctx == nil {
		return gormWorkspaceRequestInvalid(errors.New("workspace context is nil"))
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
		return gormWorkspaceRequestInvalid(errors.New("workspace transaction callback is nil"))
	}
	return repository.unitOfWork.Within(ctx, options, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		transaction, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return gormWorkspaceUnavailable(errors.New("workspace scoped transaction is unavailable"))
		}
		return work(callbackCtx, transaction.WithContext(callbackCtx), scope)
	})
}

func validWorkspaceGORMDatabase(database *gorm.DB) bool {
	return database != nil && database.Config != nil && database.Statement != nil &&
		database.Config.ConnPool != nil && database.Statement.ConnPool != nil && database.Error == nil
}

func gormWorkspaceRawRow(database *gorm.DB, query string, arguments ...any) (*sql.Row, error) {
	if !validWorkspaceGORMDatabase(database) {
		return nil, errors.New("workspace GORM database is unavailable")
	}
	statement := database.Raw(query, arguments...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	row := statement.Row()
	if row == nil {
		return nil, errors.New("workspace GORM query returned no row handle")
	}
	return row, nil
}

func gormWorkspaceRows(database *gorm.DB, query string, arguments ...any) (*sql.Rows, error) {
	if !validWorkspaceGORMDatabase(database) {
		return nil, errors.New("workspace GORM database is unavailable")
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
		return nil, errors.New("workspace GORM query returned no rows handle")
	}
	return rows, nil
}

func gormWorkspaceNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound)
}

func classifyGORMWorkspace(ctx context.Context, err error, fallbackCode string) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if cause := gormWorkspaceContextCause(ctx); cause != nil {
		if errors.Is(cause, context.Canceled) {
			return foundation.NewError(foundation.ErrorNonRetryableFailure, fallbackCode, false, cause)
		}
		return foundation.NewError(foundation.ErrorRetryableFailure, fallbackCode, true, cause)
	}
	if errors.Is(err, context.Canceled) {
		return foundation.NewError(foundation.ErrorNonRetryableFailure, fallbackCode, false, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return foundation.NewError(foundation.ErrorRetryableFailure, fallbackCode, true, err)
	}
	if gormWorkspaceNoRows(err) {
		return classify(pgx.ErrNoRows, fallbackCode)
	}
	if errors.Is(err, sql.ErrTxDone) {
		return gormWorkspaceUnavailable(errors.New("workspace transaction is no longer active"))
	}
	return classify(err, fallbackCode)
}

func classifyGORMControl(ctx context.Context, err error, fallbackCode string) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if cause := gormWorkspaceContextCause(ctx); cause != nil {
		if errors.Is(cause, context.Canceled) {
			return foundation.NewError(foundation.ErrorNonRetryableFailure, fallbackCode, false, cause)
		}
		return foundation.NewError(foundation.ErrorRetryableFailure, fallbackCode, true, cause)
	}
	if errors.Is(err, context.Canceled) {
		return foundation.NewError(foundation.ErrorNonRetryableFailure, fallbackCode, false, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return foundation.NewError(foundation.ErrorRetryableFailure, fallbackCode, true, err)
	}
	if gormWorkspaceNoRows(err) {
		return classifyControl(pgx.ErrNoRows, fallbackCode)
	}
	if errors.Is(err, sql.ErrTxDone) {
		return controlUnavailable(errors.New("workspace transaction is no longer active"))
	}
	return classifyControl(err, fallbackCode)
}

func gormWorkspaceContextCause(ctx context.Context) error {
	if ctx == nil || ctx.Err() == nil {
		return nil
	}
	cause := context.Cause(ctx)
	if cause == nil {
		return ctx.Err()
	}
	if !errors.Is(cause, ctx.Err()) {
		return errors.Join(ctx.Err(), cause)
	}
	return cause
}

func gormWorkspaceUnavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKSPACE_DATABASE_UNAVAILABLE", true, cause)
}

func gormWorkspaceOptionInvalid(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, "WORKSPACE_REPOSITORY_OPTION_INVALID", false, cause)
}

func gormWorkspaceRequestInvalid(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeRegistryInvalid, false, cause)
}
