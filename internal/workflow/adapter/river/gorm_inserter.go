package riveradapter

import (
	"context"
	"database/sql"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	riverlib "github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// ScopedJobInserter is the transaction-safe Node job port for GORM-backed
// adapters. Unlike the legacy port, it cannot accept an arbitrary transaction.
type ScopedJobInserter interface {
	InsertTx(context.Context, foundation.TransactionScope, NodeJobArgs, InsertOptions) (JobReceipt, error)
}

// ScopedEnqueueFence validates enqueue admission in the same opaque scope.
type ScopedEnqueueFence interface {
	CheckEnqueue(context.Context, foundation.TransactionScope) error
}

type sqlRiverInsertClient interface {
	InsertTx(context.Context, *sql.Tx, riverlib.JobArgs, *riverlib.InsertOpts) (*rivertype.JobInsertResult, error)
}

type sqlTransactionResolver func(foundation.TransactionScope) (*sql.Tx, error)

// ScopedTypedJobInserter inserts one validated River job through a GORM-backed
// opaque transaction scope.
type ScopedTypedJobInserter[T riverlib.JobArgs] struct {
	client    sqlRiverInsertClient
	queue     string
	validate  func(T) error
	fence     ScopedEnqueueFence
	resolveTx sqlTransactionResolver
}

// NewScopedTypedJobInserter constructs an insert-only database/sql producer
// that shares the platform pgx pool and the existing River client's schema.
func NewScopedTypedJobInserter[T riverlib.JobArgs](database *platformpostgres.Pool, client *Client, validate func(T) error, fence ScopedEnqueueFence) (*ScopedTypedJobInserter[T], error) {
	if database == nil {
		return nil, jobError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_DATABASE_MISSING", errors.New("PostgreSQL database is nil"))
	}
	if client == nil || isNilRiverDependency(client.insert) || client.Queue() == "" || client.Schema() == "" {
		return nil, jobError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_CLIENT_MISSING", errors.New("River client is nil"))
	}
	if validate == nil {
		return nil, jobError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_JOB_VALIDATOR_MISSING", errors.New("River job validator is nil"))
	}
	if isNilRiverDependency(fence) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_SCOPED_ENQUEUE_FENCE_MISSING", true, errors.New("scoped enqueue fence is unavailable"))
	}
	driver, err := database.RiverSQLDriver()
	if err != nil {
		return nil, jobError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_DATABASE_MISSING", err)
	}
	inner, err := riverlib.NewClient(driver, &riverlib.Config{Logger: client.logger, Schema: client.Schema()})
	if err != nil {
		return nil, jobError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_CLIENT_INVALID", err)
	}
	return &ScopedTypedJobInserter[T]{
		client:    inner,
		queue:     client.Queue(),
		validate:  validate,
		fence:     fence,
		resolveTx: platformpostgres.SQLTransaction,
	}, nil
}

// InsertTx validates and inserts one typed job in the caller's opaque scope.
func (i *ScopedTypedJobInserter[T]) InsertTx(ctx context.Context, scope foundation.TransactionScope, args T, options InsertOptions) (JobReceipt, error) {
	if i == nil || i.client == nil || i.resolveTx == nil {
		return JobReceipt{}, jobError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_INSERTER_MISSING", errors.New("River job inserter is nil"))
	}
	if i.validate == nil {
		return JobReceipt{}, jobError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_JOB_VALIDATOR_MISSING", errors.New("River job validator is nil"))
	}
	if err := i.validate(args); err != nil {
		return JobReceipt{}, err
	}
	tx, err := i.resolveTx(scope)
	if err != nil || tx == nil {
		if err == nil {
			err = errors.New("resolved database/sql transaction is nil")
		}
		return JobReceipt{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_TRANSACTION_INVALID", true, err)
	}
	if isNilRiverDependency(i.fence) {
		return JobReceipt{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_SCOPED_ENQUEUE_FENCE_MISSING", true, errors.New("scoped enqueue fence is unavailable"))
	}
	if err := i.fence.CheckEnqueue(ctx, scope); err != nil {
		return JobReceipt{}, err
	}
	opts, err := buildInsertOptions(ctx, i.queue, options)
	if err != nil {
		return JobReceipt{}, err
	}
	result, err := i.client.InsertTx(ctx, tx, args, opts)
	if err != nil {
		return JobReceipt{}, foundation.NewError(foundation.ErrorRetryableFailure, "WORKFLOW_RIVER_JOB_INSERT_FAILED", true, err)
	}
	if result == nil || result.Job == nil || result.Job.ID < 1 {
		return JobReceipt{}, jobError(foundation.ErrorConsistencyViolation, "WORKFLOW_RIVER_JOB_RESULT_INVALID", errors.New("River insert returned no persisted job"))
	}
	return JobReceipt{JobID: result.Job.ID, Duplicate: result.UniqueSkippedAsDuplicate}, nil
}

// ScopedRiverJobInserter is the NodeJobArgs wrapper around the scoped generic
// inserter.
type ScopedRiverJobInserter struct {
	typed *ScopedTypedJobInserter[NodeJobArgs]
}

var _ ScopedJobInserter = (*ScopedRiverJobInserter)(nil)

// NewScopedJobInserter constructs the GORM transaction-safe Node job inserter.
func NewScopedJobInserter(database *platformpostgres.Pool, client *Client, fence ScopedEnqueueFence) (ScopedJobInserter, error) {
	typed, err := NewScopedTypedJobInserter(database, client, ValidateNodeJobArgs, fence)
	if err != nil {
		return nil, err
	}
	return &ScopedRiverJobInserter{typed: typed}, nil
}

// InsertTx inserts one validated Node job through the opaque transaction scope.
func (i *ScopedRiverJobInserter) InsertTx(ctx context.Context, scope foundation.TransactionScope, args NodeJobArgs, options InsertOptions) (JobReceipt, error) {
	if i == nil || i.typed == nil {
		return JobReceipt{}, jobError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_INSERTER_MISSING", errors.New("River job inserter is nil"))
	}
	return i.typed.InsertTx(ctx, scope, args, options)
}
