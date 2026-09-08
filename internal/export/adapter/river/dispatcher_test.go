package river

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowriver "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	"github.com/riverqueue/river/rivertype"
)

func TestExportDispatchUniquenessAllowsRecoveryAfterCompletedNoOp(t *testing.T) {
	t.Parallel()

	options := exportUniqueOpts()
	want := []rivertype.JobState{
		rivertype.JobStateAvailable,
		rivertype.JobStatePending,
		rivertype.JobStateRunning,
		rivertype.JobStateScheduled,
		rivertype.JobStateRetryable,
	}
	if !options.ByArgs || !slices.Equal(options.ByState, want) {
		t.Fatalf("Export unique options = %#v", options)
	}
	if slices.Contains(options.ByState, rivertype.JobStateCompleted) {
		t.Fatal("completed no-op transport still blocks durable Export recovery")
	}
}

func TestGORMDispatcherRequiresDatabaseAndClient(t *testing.T) {
	t.Parallel()

	if _, err := NewGORMTransactionalDispatcher(nil, nil, nil); exportDispatcherErrorCode(err) != "EXPORT_RIVER_DATABASE_UNAVAILABLE" {
		t.Fatalf("nil database error = %v", err)
	}
	if _, err := NewGORMTransactionalDispatcher(&platformpostgres.Pool{}, nil, nil); exportDispatcherErrorCode(err) != "EXPORT_RIVER_DISPATCHER_UNAVAILABLE" {
		t.Fatalf("nil client error = %v", err)
	}
}

func TestGORMDispatcherFailsClosedWhenPartiallyConstructed(t *testing.T) {
	t.Parallel()

	tx := &dispatcherTx{}
	dispatcher := &GORMDispatcher{unitOfWork: tx}
	err := dispatcher.Dispatch(context.Background(), foundation.ID("10000000-0000-4000-8000-000000000001"), foundation.ID("20000000-0000-4000-8000-000000000002"))
	if exportDispatcherErrorCode(err) != "EXPORT_RIVER_TRANSACTIONAL_DISPATCHER_UNAVAILABLE" {
		t.Fatalf("partial dispatcher error = %v", err)
	}
	if tx.committed || tx.rolledBack {
		t.Fatalf("partial dispatcher touched transaction: committed=%t rolled_back=%t", tx.committed, tx.rolledBack)
	}
}

func TestGORMDispatcherCommitsFencedInsertWithExportUniqueness(t *testing.T) {
	t.Parallel()

	tx := &dispatcherTx{}
	var gotOptions workflowriver.InsertOptions
	dispatcher := &GORMDispatcher{
		unitOfWork: tx,
		inserter: scopedExportJobInserterFunc(func(_ context.Context, gotTransaction foundation.TransactionScope, args Args, options workflowriver.InsertOptions) (workflowriver.JobReceipt, error) {
			if gotTransaction != tx {
				t.Fatalf("transaction = %#v", gotTransaction)
			}
			if err := ValidateArgs(args); err != nil {
				t.Fatal(err)
			}
			gotOptions = options
			return workflowriver.JobReceipt{JobID: 71}, nil
		}),
	}
	if err := dispatcher.Dispatch(context.Background(), foundation.ID("10000000-0000-4000-8000-000000000001"), foundation.ID("20000000-0000-4000-8000-000000000002")); err != nil {
		t.Fatal(err)
	}
	if !tx.committed || tx.rolledBack {
		t.Fatalf("transaction committed=%t rolled_back=%t", tx.committed, tx.rolledBack)
	}
	if !slices.Equal(gotOptions.UniqueStates, exportUniqueOpts().ByState) {
		t.Fatalf("unique states = %#v", gotOptions.UniqueStates)
	}
}

func TestGORMDispatcherDoesNotCommitRejectedInsert(t *testing.T) {
	t.Parallel()

	tx := &dispatcherTx{}
	wantErr := errors.New("rollout is draining")
	dispatcher := &GORMDispatcher{
		unitOfWork: tx,
		inserter: scopedExportJobInserterFunc(func(context.Context, foundation.TransactionScope, Args, workflowriver.InsertOptions) (workflowriver.JobReceipt, error) {
			return workflowriver.JobReceipt{}, wantErr
		}),
	}
	err := dispatcher.Dispatch(context.Background(), foundation.ID("10000000-0000-4000-8000-000000000001"), foundation.ID("20000000-0000-4000-8000-000000000002"))
	if !errors.Is(err, wantErr) || tx.committed || !tx.rolledBack {
		t.Fatalf("error=%v committed=%t rolled_back=%t", err, tx.committed, tx.rolledBack)
	}
}

func TestGORMDispatcherClassifiesTransactionFailures(t *testing.T) {
	t.Parallel()

	workspaceID := foundation.ID("10000000-0000-4000-8000-000000000001")
	exportID := foundation.ID("20000000-0000-4000-8000-000000000002")
	t.Run("begin", func(t *testing.T) {
		dispatcher := &GORMDispatcher{
			unitOfWork: dispatcherUnitOfWorkFunc(func(context.Context, foundation.TransactionOptions, foundation.TransactionFunc) error {
				return errors.New("database unavailable")
			}),
			inserter: scopedExportJobInserterFunc(func(context.Context, foundation.TransactionScope, Args, workflowriver.InsertOptions) (workflowriver.JobReceipt, error) {
				t.Fatal("inserter called after transaction begin failure")
				return workflowriver.JobReceipt{}, nil
			}),
		}
		if err := dispatcher.Dispatch(context.Background(), workspaceID, exportID); exportDispatcherErrorCode(err) != "EXPORT_RIVER_TRANSACTION_BEGIN_FAILED" {
			t.Fatalf("begin error = %v", err)
		}
	})

	t.Run("commit", func(t *testing.T) {
		tx := &dispatcherTx{commitErr: errors.New("commit result unknown")}
		dispatcher := &GORMDispatcher{
			unitOfWork: tx,
			inserter: scopedExportJobInserterFunc(func(context.Context, foundation.TransactionScope, Args, workflowriver.InsertOptions) (workflowriver.JobReceipt, error) {
				return workflowriver.JobReceipt{JobID: 72}, nil
			}),
		}
		if err := dispatcher.Dispatch(context.Background(), workspaceID, exportID); exportDispatcherErrorCode(err) != "EXPORT_RIVER_TRANSACTION_COMMIT_FAILED" {
			t.Fatalf("commit error = %v", err)
		}
		if !tx.committed || !tx.rolledBack {
			t.Fatalf("commit failure transaction committed=%t rolled_back=%t", tx.committed, tx.rolledBack)
		}
	})
}

func TestGORMTransactionalDispatcherCommitsScopedInsertWithExportUniqueness(t *testing.T) {
	t.Parallel()

	scope := &dispatcherScope{}
	committed := false
	var gotOptions workflowriver.InsertOptions
	dispatcher, err := newGORMTransactionalDispatcher(
		dispatcherUnitOfWorkFunc(func(ctx context.Context, _ foundation.TransactionOptions, work foundation.TransactionFunc) error {
			if err := work(ctx, scope); err != nil {
				return err
			}
			committed = true
			return nil
		}),
		scopedExportJobInserterFunc(func(_ context.Context, gotScope foundation.TransactionScope, args Args, options workflowriver.InsertOptions) (workflowriver.JobReceipt, error) {
			if gotScope != scope {
				t.Fatalf("transaction scope = %#v", gotScope)
			}
			if err := ValidateArgs(args); err != nil {
				t.Fatal(err)
			}
			gotOptions = options
			return workflowriver.JobReceipt{JobID: 81}, nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Dispatch(context.Background(), foundation.ID("10000000-0000-4000-8000-000000000001"), foundation.ID("20000000-0000-4000-8000-000000000002")); err != nil {
		t.Fatal(err)
	}
	if !committed || !slices.Equal(gotOptions.UniqueStates, exportUniqueOpts().ByState) {
		t.Fatalf("committed=%t unique states=%#v", committed, gotOptions.UniqueStates)
	}
}

func TestGORMTransactionalDispatcherRollsBackRejectedInsert(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("rollout is draining")
	committed := false
	dispatcher, err := newGORMTransactionalDispatcher(
		dispatcherUnitOfWorkFunc(func(ctx context.Context, _ foundation.TransactionOptions, work foundation.TransactionFunc) error {
			if err := work(ctx, &dispatcherScope{}); err != nil {
				return err
			}
			committed = true
			return nil
		}),
		scopedExportJobInserterFunc(func(context.Context, foundation.TransactionScope, Args, workflowriver.InsertOptions) (workflowriver.JobReceipt, error) {
			return workflowriver.JobReceipt{}, wantErr
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	err = dispatcher.Dispatch(context.Background(), foundation.ID("10000000-0000-4000-8000-000000000001"), foundation.ID("20000000-0000-4000-8000-000000000002"))
	if !errors.Is(err, wantErr) || committed {
		t.Fatalf("error=%v committed=%t", err, committed)
	}
}

func TestGORMTransactionalDispatcherClassifiesTransactionFailures(t *testing.T) {
	t.Parallel()

	workspaceID := foundation.ID("10000000-0000-4000-8000-000000000001")
	exportID := foundation.ID("20000000-0000-4000-8000-000000000002")
	inserter := scopedExportJobInserterFunc(func(context.Context, foundation.TransactionScope, Args, workflowriver.InsertOptions) (workflowriver.JobReceipt, error) {
		return workflowriver.JobReceipt{JobID: 82}, nil
	})

	t.Run("begin", func(t *testing.T) {
		dispatcher, err := newGORMTransactionalDispatcher(
			dispatcherUnitOfWorkFunc(func(context.Context, foundation.TransactionOptions, foundation.TransactionFunc) error {
				return errors.New("database unavailable")
			}),
			inserter,
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := dispatcher.Dispatch(context.Background(), workspaceID, exportID); exportDispatcherErrorCode(err) != "EXPORT_RIVER_TRANSACTION_BEGIN_FAILED" {
			t.Fatalf("begin error = %v", err)
		}
	})

	t.Run("commit", func(t *testing.T) {
		dispatcher, err := newGORMTransactionalDispatcher(
			dispatcherUnitOfWorkFunc(func(ctx context.Context, _ foundation.TransactionOptions, work foundation.TransactionFunc) error {
				if err := work(ctx, &dispatcherScope{}); err != nil {
					return err
				}
				return errors.New("commit result unknown")
			}),
			inserter,
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := dispatcher.Dispatch(context.Background(), workspaceID, exportID); exportDispatcherErrorCode(err) != "EXPORT_RIVER_TRANSACTION_COMMIT_FAILED" {
			t.Fatalf("commit error = %v", err)
		}
	})
}

type dispatcherUnitOfWorkFunc func(context.Context, foundation.TransactionOptions, foundation.TransactionFunc) error

func (fn dispatcherUnitOfWorkFunc) Within(ctx context.Context, options foundation.TransactionOptions, work foundation.TransactionFunc) error {
	return fn(ctx, options, work)
}

type scopedExportJobInserterFunc func(context.Context, foundation.TransactionScope, Args, workflowriver.InsertOptions) (workflowriver.JobReceipt, error)

func (fn scopedExportJobInserterFunc) InsertTx(ctx context.Context, scope foundation.TransactionScope, args Args, options workflowriver.InsertOptions) (workflowriver.JobReceipt, error) {
	return fn(ctx, scope, args, options)
}

type dispatcherScope struct{}

func (*dispatcherScope) TransactionScope() {}

type dispatcherTx struct {
	committed  bool
	commitErr  error
	rolledBack bool
}

func (*dispatcherTx) TransactionScope() {}

func (tx *dispatcherTx) Within(ctx context.Context, _ foundation.TransactionOptions, work foundation.TransactionFunc) error {
	if err := work(ctx, tx); err != nil {
		tx.rolledBack = true
		return err
	}
	tx.committed = true
	if tx.commitErr != nil {
		tx.rolledBack = true
	}
	return tx.commitErr
}

func exportDispatcherErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}
