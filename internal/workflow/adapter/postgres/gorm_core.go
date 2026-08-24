package workflowpostgres

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	modelsettingsapplication "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// GORMRepository is the staged implementation of the legacy-compatible
// Workflow repository surface. Production composition remains on Repository.
type GORMRepository struct {
	database   *gorm.DB
	unitOfWork foundation.UnitOfWork
}

// GORMRuntimeRepositoryHooks contains only scoped lifecycle hooks. Legacy
// pgx-backed hooks are intentionally not accepted by the GORM runtime.
type GORMRuntimeRepositoryHooks struct {
	CancellationSafety      application.ScopedCancellationSafetyGuard
	Terminal                application.ScopedWorkflowTerminalHook
	Control                 application.ScopedWorkflowControlHook
	ModelRuntimeFreshWithin time.Duration
}

// GORMRuntimeRepository is the staged GORM Runtime state machine.
type GORMRuntimeRepository struct {
	database                *gorm.DB
	unitOfWork              foundation.UnitOfWork
	jobs                    riveradapter.ScopedJobInserter
	cancellation            application.ScopedCancellationSafetyGuard
	terminal                application.ScopedWorkflowTerminalHook
	control                 application.ScopedWorkflowControlHook
	modelRuntimeFreshWithin time.Duration
}

// NewGORMRepository derives the GORM root and UoW from one platform pool.
func NewGORMRepository(pool *platformpostgres.Pool) (*GORMRepository, error) {
	database, unitOfWork, err := gormWorkflowDependencies(pool)
	if err != nil {
		return nil, err
	}
	return &GORMRepository{database: database, unitOfWork: unitOfWork}, nil
}

// NewGORMRuntimeRepositoryWithHooks constructs an insert-only scoped River
// producer from the same platform pool as the Runtime transaction boundary.
func NewGORMRuntimeRepositoryWithHooks(
	pool *platformpostgres.Pool,
	riverOptions riveradapter.Options,
	enqueueFence riveradapter.ScopedEnqueueFence,
	hooks GORMRuntimeRepositoryHooks,
) (*GORMRuntimeRepository, error) {
	if pool == nil || pool.DB() == nil {
		return nil, gormWorkflowUnavailable("WORKFLOW_RUNTIME_DATABASE_UNAVAILABLE", errors.New("workflow PostgreSQL pool is unavailable"))
	}
	if riverOptions.EnqueueFence != nil {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_RIVER_OPTIONS_INVALID", false, errors.New("legacy and scoped enqueue fences cannot be mixed"))
	}
	if nilGORMWorkflowDependency(enqueueFence) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_SCOPED_ENQUEUE_FENCE_MISSING", true, errors.New("workflow scoped enqueue fence is unavailable"))
	}
	client, err := riveradapter.NewClientWithOptions(pool.DB(), nil, riverOptions)
	if err != nil {
		return nil, err
	}
	jobs, err := riveradapter.NewScopedJobInserter(pool, client, enqueueFence)
	if err != nil {
		return nil, err
	}
	return newGORMRuntimeRepository(pool, jobs, hooks)
}

func newGORMRuntimeRepository(pool *platformpostgres.Pool, jobs riveradapter.ScopedJobInserter, hooks GORMRuntimeRepositoryHooks) (*GORMRuntimeRepository, error) {
	database, unitOfWork, err := gormWorkflowDependencies(pool)
	if err != nil {
		return nil, err
	}
	if nilGORMWorkflowDependency(jobs) {
		return nil, gormWorkflowUnavailable("WORKFLOW_RUNTIME_DATABASE_UNAVAILABLE", errors.New("workflow scoped job inserter is unavailable"))
	}
	if hooks.CancellationSafety != nil && nilGORMWorkflowDependency(hooks.CancellationSafety) {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_SCOPED_CANCELLATION_GUARD_INVALID", false, errors.New("workflow scoped cancellation guard is nil"))
	}
	if hooks.Terminal != nil && nilGORMWorkflowDependency(hooks.Terminal) {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_SCOPED_TERMINAL_HOOK_INVALID", false, errors.New("workflow scoped terminal hook is nil"))
	}
	if hooks.Control != nil && nilGORMWorkflowDependency(hooks.Control) {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_SCOPED_CONTROL_HOOK_INVALID", false, errors.New("workflow scoped control hook is nil"))
	}
	if hooks.ModelRuntimeFreshWithin == 0 {
		hooks.ModelRuntimeFreshWithin = modelsettingsapplication.DefaultRuntimeFreshWithin
	}
	if !modelsettingsapplication.ValidRuntimeFreshWithin(hooks.ModelRuntimeFreshWithin) {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_MODEL_RUNTIME_FRESHNESS_INVALID", false, errors.New("workflow model runtime freshness policy is invalid"))
	}
	return &GORMRuntimeRepository{
		database: database, unitOfWork: unitOfWork, jobs: jobs,
		cancellation: hooks.CancellationSafety, terminal: hooks.Terminal, control: hooks.Control,
		modelRuntimeFreshWithin: hooks.ModelRuntimeFreshWithin,
	}, nil
}

func gormWorkflowDependencies(pool *platformpostgres.Pool) (*gorm.DB, foundation.UnitOfWork, error) {
	if pool == nil {
		return nil, nil, gormWorkflowUnavailable("WORKFLOW_DATABASE_UNAVAILABLE", errors.New("workflow PostgreSQL pool is unavailable"))
	}
	database, err := pool.GORM()
	if err != nil {
		return nil, nil, gormWorkflowUnavailable("WORKFLOW_DATABASE_UNAVAILABLE", errors.New("workflow GORM database is unavailable"))
	}
	unitOfWork, err := pool.UnitOfWork()
	if err != nil {
		return nil, nil, gormWorkflowUnavailable("WORKFLOW_DATABASE_UNAVAILABLE", errors.New("workflow unit of work is unavailable"))
	}
	if !validGORMWorkflowDatabase(database) || nilGORMWorkflowDependency(unitOfWork) {
		return nil, nil, gormWorkflowUnavailable("WORKFLOW_DATABASE_UNAVAILABLE", errors.New("workflow GORM dependencies are unavailable"))
	}
	return database, unitOfWork, nil
}

func (repository *GORMRepository) ready(ctx context.Context) error {
	if repository == nil || !validGORMWorkflowDatabase(repository.database) || nilGORMWorkflowDependency(repository.unitOfWork) {
		return gormWorkflowUnavailable("WORKFLOW_DATABASE_UNAVAILABLE", errors.New("workflow GORM repository is unavailable"))
	}
	if ctx == nil {
		return foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_CONTEXT_INVALID", false, errors.New("workflow context is nil"))
	}
	return nil
}

func (repository *GORMRuntimeRepository) ready(ctx context.Context) error {
	if repository == nil || !validGORMWorkflowDatabase(repository.database) || nilGORMWorkflowDependency(repository.unitOfWork) || nilGORMWorkflowDependency(repository.jobs) {
		return gormWorkflowUnavailable("WORKFLOW_RUNTIME_DATABASE_UNAVAILABLE", errors.New("workflow GORM runtime is unavailable"))
	}
	if ctx == nil {
		return foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_CONTEXT_INVALID", false, errors.New("workflow context is nil"))
	}
	return nil
}

func (repository *GORMRepository) within(ctx context.Context, options foundation.TransactionOptions, work func(context.Context, *gorm.DB, foundation.TransactionScope) error) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	return withinGORMWorkflow(ctx, repository.unitOfWork, options, work)
}

func (repository *GORMRuntimeRepository) within(ctx context.Context, options foundation.TransactionOptions, work func(context.Context, *gorm.DB, foundation.TransactionScope) error) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	return withinGORMWorkflow(ctx, repository.unitOfWork, options, work)
}

func withinGORMWorkflow(ctx context.Context, unitOfWork foundation.UnitOfWork, options foundation.TransactionOptions, work func(context.Context, *gorm.DB, foundation.TransactionScope) error) error {
	if work == nil {
		return foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_TRANSACTION_CALLBACK_INVALID", false, errors.New("workflow transaction callback is nil"))
	}
	return unitOfWork.Within(ctx, options, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		transaction, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return gormWorkflowUnavailable("WORKFLOW_TRANSACTION_UNAVAILABLE", err)
		}
		return work(callbackCtx, transaction.WithContext(callbackCtx), scope)
	})
}

func validGORMWorkflowDatabase(database *gorm.DB) bool {
	return database != nil && database.Config != nil && database.Statement != nil &&
		database.Config.ConnPool != nil && database.Statement.ConnPool != nil && database.Error == nil
}

func nilGORMWorkflowDependency(value any) bool {
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

func gormWorkflowRawRow(database *gorm.DB, query string, arguments ...any) (*sql.Row, error) {
	if !validGORMWorkflowDatabase(database) {
		return nil, errors.New("workflow GORM database is unavailable")
	}
	statement := database.Raw(query, arguments...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	row := statement.Row()
	if row == nil {
		return nil, errors.New("workflow GORM query returned no row handle")
	}
	return row, nil
}

func gormWorkflowRawRows(database *gorm.DB, query string, arguments ...any) (*sql.Rows, error) {
	if !validGORMWorkflowDatabase(database) {
		return nil, errors.New("workflow GORM database is unavailable")
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
		return nil, errors.New("workflow GORM query returned no rows handle")
	}
	return rows, nil
}

func gormWorkflowNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound)
}

func classifyGORMWorkflow(ctx context.Context, cause error, code string) error {
	if cause == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(cause, &classified) {
		return cause
	}
	if contextCause := gormWorkflowContextCause(ctx, cause); contextCause != nil {
		if errors.Is(contextCause, context.Canceled) {
			return foundation.NewError(foundation.ErrorNonRetryableFailure, code, false, contextCause)
		}
		return foundation.NewError(foundation.ErrorRetryableFailure, code, true, contextCause)
	}
	var postgresError *pgconn.PgError
	if errors.As(cause, &postgresError) {
		switch postgresError.Code {
		case "23505":
			return foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_CONFLICT", false, cause)
		case "23503":
			return foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_REFERENCE_INVALID", false, cause)
		case "23514", "22P02":
			return foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_DATA_INVALID", false, cause)
		case "40001", "40P01":
			return foundation.NewError(foundation.ErrorRetryableFailure, code, true, cause)
		}
		return gormWorkflowUnavailable(code, cause)
	}
	if errors.Is(cause, sql.ErrTxDone) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, cause)
	}
	return gormWorkflowUnavailable(code, cause)
}

func gormWorkflowContextCause(ctx context.Context, cause error) error {
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
		return cause
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return cause
	}
	return nil
}

func gormWorkflowUnavailable(code string, cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, cause)
}
