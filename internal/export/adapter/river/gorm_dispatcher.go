package river

import (
	"context"
	"errors"
	"reflect"

	exportapp "github.com/CodeZen-Lizhi/zhixu/internal/export/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowriver "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	riverlib "github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

type scopedExportJobInserter interface {
	InsertTx(context.Context, foundation.TransactionScope, Args, workflowriver.InsertOptions) (workflowriver.JobReceipt, error)
}

// GORMDispatcher 通过共享 Unit of Work 在 opaque scope 内执行围栏检查和 River 入队。
type GORMDispatcher struct {
	unitOfWork foundation.UnitOfWork
	inserter   scopedExportJobInserter
}

var _ exportapp.Dispatcher = (*GORMDispatcher)(nil)

// NewGORMTransactionalDispatcher 创建通过共享事务 scope 检查围栏并入队的 Export 投递器。
func NewGORMTransactionalDispatcher(
	database *platformpostgres.Pool,
	client *workflowriver.Client,
	fence workflowriver.ScopedEnqueueFence,
) (*GORMDispatcher, error) {
	if database == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "EXPORT_RIVER_DATABASE_UNAVAILABLE", true, errors.New("export River database is unavailable"))
	}
	if client == nil || client.Queue() == "" || isNilExportRiverDependency(fence) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "EXPORT_RIVER_DISPATCHER_UNAVAILABLE", true, errors.New("export GORM River dependencies are unavailable"))
	}
	unitOfWork, err := database.UnitOfWork()
	if err != nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "EXPORT_RIVER_DATABASE_UNAVAILABLE", true, err)
	}
	inserter, err := workflowriver.NewScopedTypedJobInserter(database, client, ValidateArgs, fence)
	if err != nil {
		return nil, err
	}
	return newGORMTransactionalDispatcher(unitOfWork, inserter)
}

func newGORMTransactionalDispatcher(unitOfWork foundation.UnitOfWork, inserter scopedExportJobInserter) (*GORMDispatcher, error) {
	if isNilExportRiverDependency(unitOfWork) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "EXPORT_RIVER_DATABASE_UNAVAILABLE", true, errors.New("export River unit of work is unavailable"))
	}
	if isNilExportRiverDependency(inserter) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "EXPORT_RIVER_DISPATCHER_UNAVAILABLE", true, errors.New("export scoped River inserter is unavailable"))
	}
	return &GORMDispatcher{unitOfWork: unitOfWork, inserter: inserter}, nil
}

// Dispatch 在一个 GORM Unit of Work 内投递唯一 Export River Job。
func (dispatcher *GORMDispatcher) Dispatch(ctx context.Context, workspaceID, exportID foundation.ID) error {
	if dispatcher == nil || isNilExportRiverDependency(dispatcher.unitOfWork) || isNilExportRiverDependency(dispatcher.inserter) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, "EXPORT_RIVER_TRANSACTIONAL_DISPATCHER_UNAVAILABLE", true, errors.New("export GORM River dispatcher is unavailable"))
	}
	args, err := NewArgs(workspaceID, exportID)
	if err != nil {
		return err
	}
	return dispatcher.dispatchTx(ctx, args)
}

func (dispatcher *GORMDispatcher) dispatchTx(ctx context.Context, args Args) error {
	callbackStarted := false
	callbackSucceeded := false
	err := dispatcher.unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		callbackStarted = true
		receipt, insertErr := dispatcher.inserter.InsertTx(callbackCtx, scope, args, workflowriver.InsertOptions{UniqueStates: exportUniqueOpts().ByState})
		if insertErr != nil {
			return insertErr
		}
		if receipt.JobID < 1 {
			return foundation.NewError(foundation.ErrorConsistencyViolation, "EXPORT_RIVER_DISPATCH_RESULT_INVALID", false, errors.New("export River insert returned no persisted job"))
		}
		callbackSucceeded = true
		return nil
	})
	if err == nil {
		return nil
	}
	if callbackSucceeded {
		return foundation.NewError(foundation.ErrorRetryableFailure, "EXPORT_RIVER_TRANSACTION_COMMIT_FAILED", true, err)
	}
	if !callbackStarted {
		return foundation.NewError(foundation.ErrorRetryableFailure, "EXPORT_RIVER_TRANSACTION_BEGIN_FAILED", true, err)
	}
	return err
}

func isNilExportRiverDependency(value any) bool {
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

func exportUniqueOpts() riverlib.UniqueOpts {
	return riverlib.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
		rivertype.JobStateAvailable,
		rivertype.JobStatePending,
		rivertype.JobStateRunning,
		rivertype.JobStateScheduled,
		rivertype.JobStateRetryable,
	}}
}
