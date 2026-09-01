package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sync"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	auditapplication "github.com/CodeZen-Lizhi/zhixu/internal/audit/application"
	eventsapplication "github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"gorm.io/gorm"
)

// GORMRepository is the staged Tools persistence implementation. Production
// composition remains on Repository until the PostgreSQL parity gate passes.
type GORMRepository struct {
	database       *gorm.DB
	unitOfWork     foundation.UnitOfWork
	policySnapshot workflowapplication.ScopedToolExecutionPolicySnapshot
	recoveryFence  workflowapplication.ScopedToolCallRecoveryFence
	// recoveryCursor rotates bounded stale scans across calls. It is runtime
	// scheduling state only; durable Tool/Workflow facts remain authoritative.
	recoveryMu     sync.Mutex
	recoveryCursor *gormStaleRecoveryCandidate
}

// GORMWorkspaceAnalysisRepository owns the Tools outer transaction while
// Workflow, Agent, Event, and Audit facts remain behind scoped ports.
type GORMWorkspaceAnalysisRepository struct {
	database       *gorm.DB
	unitOfWork     foundation.UnitOfWork
	executionFence workflowapplication.ScopedWorkspaceAnalysisExecutionFence
	participant    agentapplication.ScopedWorkspaceAnalysisToolParticipant
	refusalStore   agentapplication.ScopedWorkspaceAnalysisToolRefusalStore
	authority      agentapplication.ScopedWorkspaceAnalysisToolAuthorityReader
	events         eventsapplication.ScopedAppender
	audit          *auditapplication.Recorder
}

// NewGORMRepository derives the GORM root and UoW from one platform Pool.
// The Workflow scoped collaborators participate in that same opaque scope.
func NewGORMRepository(
	pool *platformpostgres.Pool,
	policySnapshot workflowapplication.ScopedToolExecutionPolicySnapshot,
	recoveryFence workflowapplication.ScopedToolCallRecoveryFence,
) (*GORMRepository, error) {
	database, unitOfWork, err := gormToolsDependencies(pool)
	if err != nil {
		return nil, err
	}
	if nilGORMToolsDependency(policySnapshot) || nilGORMToolsDependency(recoveryFence) {
		return nil, gormToolsUnavailable(errors.New("Tools scoped Workflow dependency is unavailable"))
	}
	return &GORMRepository{
		database:       database,
		unitOfWork:     unitOfWork,
		policySnapshot: policySnapshot,
		recoveryFence:  recoveryFence,
	}, nil
}

// NewGORMWorkspaceAnalysisRepository derives all database access from one
// Pool and rejects every missing scoped collaborator at composition time.
func NewGORMWorkspaceAnalysisRepository(
	pool *platformpostgres.Pool,
	executionFence workflowapplication.ScopedWorkspaceAnalysisExecutionFence,
	participant agentapplication.ScopedWorkspaceAnalysisToolParticipant,
	refusalStore agentapplication.ScopedWorkspaceAnalysisToolRefusalStore,
	authority agentapplication.ScopedWorkspaceAnalysisToolAuthorityReader,
	events eventsapplication.ScopedAppender,
	audit *auditapplication.Recorder,
) (*GORMWorkspaceAnalysisRepository, error) {
	database, unitOfWork, err := gormToolsDependencies(pool)
	if err != nil {
		return nil, err
	}
	if nilGORMToolsDependency(executionFence) || nilGORMToolsDependency(participant) ||
		nilGORMToolsDependency(refusalStore) || nilGORMToolsDependency(authority) ||
		nilGORMToolsDependency(events) || nilGORMToolsDependency(audit) {
		return nil, gormToolsUnavailable(errors.New("Workspace Analysis GORM dependency is unavailable"))
	}
	return &GORMWorkspaceAnalysisRepository{
		database: database, unitOfWork: unitOfWork, executionFence: executionFence,
		participant: participant, refusalStore: refusalStore, authority: authority, events: events, audit: audit,
	}, nil
}

func gormToolsDependencies(pool *platformpostgres.Pool) (*gorm.DB, foundation.UnitOfWork, error) {
	if pool == nil {
		return nil, nil, gormToolsUnavailable(errors.New("Tools PostgreSQL pool is unavailable"))
	}
	database, err := pool.GORM()
	if err != nil {
		return nil, nil, gormToolsUnavailable(errors.New("Tools GORM database is unavailable"))
	}
	unitOfWork, err := pool.UnitOfWork()
	if err != nil {
		return nil, nil, gormToolsUnavailable(errors.New("Tools GORM unit of work is unavailable"))
	}
	if !validGORMToolsDatabase(database) || nilGORMToolsDependency(unitOfWork) {
		return nil, nil, gormToolsUnavailable(errors.New("Tools GORM dependencies are unavailable"))
	}
	return database, unitOfWork, nil
}

func (repository *GORMRepository) ready(ctx context.Context) error {
	if repository == nil || !validGORMToolsDatabase(repository.database) ||
		nilGORMToolsDependency(repository.unitOfWork) ||
		nilGORMToolsDependency(repository.policySnapshot) ||
		nilGORMToolsDependency(repository.recoveryFence) {
		return gormToolsUnavailable(errors.New("Tools GORM repository is unavailable"))
	}
	if ctx == nil {
		return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeDatabaseUnavailable, false, errors.New("Tools context is nil"))
	}
	return nil
}

func (repository *GORMWorkspaceAnalysisRepository) ready(ctx context.Context) error {
	if repository == nil || !validGORMToolsDatabase(repository.database) || nilGORMToolsDependency(repository.unitOfWork) ||
		nilGORMToolsDependency(repository.executionFence) || nilGORMToolsDependency(repository.participant) ||
		nilGORMToolsDependency(repository.refusalStore) || nilGORMToolsDependency(repository.authority) ||
		nilGORMToolsDependency(repository.events) || nilGORMToolsDependency(repository.audit) {
		return gormToolsUnavailable(errors.New("Workspace Analysis GORM repository is unavailable"))
	}
	if ctx == nil {
		return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeDatabaseUnavailable, false, errors.New("Workspace Analysis context is nil"))
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
	return withinGORMTools(ctx, repository.unitOfWork, options, work)
}

func (repository *GORMWorkspaceAnalysisRepository) within(
	ctx context.Context,
	options foundation.TransactionOptions,
	work func(context.Context, *gorm.DB, foundation.TransactionScope) error,
) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	return withinGORMTools(ctx, repository.unitOfWork, options, work)
}

func (repository *GORMRepository) readWithin(
	ctx context.Context,
	options foundation.TransactionOptions,
	work func(context.Context, *gorm.DB, foundation.TransactionScope) error,
) error {
	return repository.within(ctx, options, work)
}

func (repository *GORMWorkspaceAnalysisRepository) readWithin(
	ctx context.Context,
	options foundation.TransactionOptions,
	work func(context.Context, *gorm.DB, foundation.TransactionScope) error,
) error {
	return repository.within(ctx, options, work)
}

type gormToolsTransactionStage uint8

const (
	gormToolsTransactionCallback gormToolsTransactionStage = iota + 1
	gormToolsTransactionCommit
)

type gormToolsTransactionError struct {
	stage gormToolsTransactionStage
	cause error
}

func (err *gormToolsTransactionError) Error() string {
	if err == nil || err.cause == nil {
		return "Tools GORM transaction failed"
	}
	return err.cause.Error()
}

func (err *gormToolsTransactionError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

func gormToolsCommitFailure(err error) bool {
	var transactionError *gormToolsTransactionError
	return errors.As(err, &transactionError) && transactionError.stage == gormToolsTransactionCommit
}

func withinGORMTools(
	ctx context.Context,
	unitOfWork foundation.UnitOfWork,
	options foundation.TransactionOptions,
	work func(context.Context, *gorm.DB, foundation.TransactionScope) error,
) error {
	if ctx == nil {
		return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeDatabaseUnavailable, false, errors.New("Tools transaction context is nil"))
	}
	if nilGORMToolsDependency(unitOfWork) {
		return gormToolsUnavailable(errors.New("Tools GORM unit of work is unavailable"))
	}
	if work == nil {
		return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeDatabaseUnavailable, false, errors.New("Tools transaction callback is nil"))
	}

	callbackFailed := false
	err := unitOfWork.Within(ctx, options, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		transaction, transactionErr := platformpostgres.GORMTransaction(scope)
		if transactionErr != nil {
			callbackFailed = true
			return gormToolsUnavailable(transactionErr)
		}
		if callbackErr := work(callbackCtx, transaction.WithContext(callbackCtx), scope); callbackErr != nil {
			callbackFailed = true
			return &gormToolsTransactionError{stage: gormToolsTransactionCallback, cause: callbackErr}
		}
		return nil
	})
	if err == nil {
		return nil
	}
	if callbackFailed {
		var callbackErr *gormToolsTransactionError
		if errors.As(err, &callbackErr) {
			return callbackErr.cause
		}
		return err
	}
	return &gormToolsTransactionError{stage: gormToolsTransactionCommit, cause: err}
}

func validGORMToolsDatabase(database *gorm.DB) bool {
	return database != nil && database.Config != nil && database.Statement != nil &&
		database.Config.ConnPool != nil && database.Statement.ConnPool != nil && database.Error == nil
}

func nilGORMToolsDependency(value any) bool {
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

func gormToolsRawRow(database *gorm.DB, query string, arguments ...any) (*sql.Row, error) {
	if !validGORMToolsDatabase(database) {
		return nil, errors.New("Tools GORM database is unavailable")
	}
	statement := database.Raw(query, arguments...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	row := statement.Row()
	if row == nil {
		return nil, errors.New("Tools GORM query returned no row handle")
	}
	return row, nil
}

func gormToolsRawRows(database *gorm.DB, query string, arguments ...any) (*sql.Rows, error) {
	if !validGORMToolsDatabase(database) {
		return nil, errors.New("Tools GORM database is unavailable")
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
		return nil, errors.New("Tools GORM query returned no rows handle")
	}
	return rows, nil
}

func gormToolsCloseRows(ctx context.Context, rows *sql.Rows, cause error) error {
	if rows == nil {
		return classifyGORMTools(ctx, cause)
	}
	if closeErr := rows.Close(); closeErr != nil {
		cause = errors.Join(cause, closeErr)
	}
	return classifyGORMTools(ctx, cause)
}

func gormToolsExec(database *gorm.DB, query string, arguments ...any) error {
	if !validGORMToolsDatabase(database) {
		return errors.New("Tools GORM database is unavailable")
	}
	result := database.Exec(query, arguments...)
	if result == nil {
		return fmt.Errorf("Tools GORM Exec returned no result for %T", database)
	}
	return result.Error
}
