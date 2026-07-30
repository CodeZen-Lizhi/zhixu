package river

import (
	"context"
	"errors"

	exportapp "github.com/CodeZen-Lizhi/zhixu/internal/export/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowriver "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	riverlib "github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// Dispatcher 在 Export 事实提交后以 Args 唯一键投递 River Job。
type Dispatcher struct {
	database transactionBeginner
	inserter exportJobInserter
}

type transactionBeginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

type exportJobInserter interface {
	InsertTx(context.Context, any, Args, workflowriver.InsertOptions) (workflowriver.JobReceipt, error)
}

var _ exportapp.Dispatcher = (*Dispatcher)(nil)

// NewTransactionalDispatcher 创建在同一事务内执行 rollout 围栏检查与 Export 入队的投递器。
func NewTransactionalDispatcher(database *pgxpool.Pool, client *workflowriver.Client) (*Dispatcher, error) {
	if database == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "EXPORT_RIVER_DATABASE_UNAVAILABLE", true, errors.New("export River database is unavailable"))
	}
	if client == nil || client.Queue() == "" {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "EXPORT_RIVER_DISPATCHER_UNAVAILABLE", true, errors.New("export River client is unavailable"))
	}
	inserter, err := workflowriver.NewTypedJobInserter(client, ValidateArgs)
	if err != nil {
		return nil, err
	}
	return &Dispatcher{database: database, inserter: inserter}, nil
}

// Dispatch 投递一个只含 Workspace/Export identity 的唯一 River Job。
func (dispatcher *Dispatcher) Dispatch(ctx context.Context, workspaceID, exportID foundation.ID) error {
	if dispatcher == nil {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, "EXPORT_RIVER_DISPATCHER_UNAVAILABLE", true, errors.New("export River dispatcher is unavailable"))
	}
	args, err := NewArgs(workspaceID, exportID)
	if err != nil {
		return err
	}
	return dispatcher.dispatchTx(ctx, args)
}

func (dispatcher *Dispatcher) dispatchTx(ctx context.Context, args Args) error {
	if dispatcher.database == nil || dispatcher.inserter == nil {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, "EXPORT_RIVER_TRANSACTIONAL_DISPATCHER_UNAVAILABLE", true, errors.New("export transactional River dispatcher is unavailable"))
	}
	transaction, err := dispatcher.database.Begin(ctx)
	if err != nil {
		return foundation.NewError(foundation.ErrorRetryableFailure, "EXPORT_RIVER_TRANSACTION_BEGIN_FAILED", true, err)
	}
	defer func() { _ = transaction.Rollback(context.Background()) }()
	receipt, err := dispatcher.inserter.InsertTx(ctx, transaction, args, workflowriver.InsertOptions{UniqueStates: exportUniqueOpts().ByState})
	if err != nil {
		return err
	}
	if receipt.JobID < 1 {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "EXPORT_RIVER_DISPATCH_RESULT_INVALID", false, errors.New("export River insert returned no persisted job"))
	}
	if err := transaction.Commit(ctx); err != nil {
		return foundation.NewError(foundation.ErrorRetryableFailure, "EXPORT_RIVER_TRANSACTION_COMMIT_FAILED", true, err)
	}
	return nil
}

func exportUniqueOpts() riverlib.UniqueOpts {
	return riverlib.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
		rivertype.JobStateAvailable,
		rivertype.JobStatePending,
		rivertype.JobStateRunning,
		rivertype.JobStateScheduled,
		rivertype.JobStateRetryable,
	}}
}
