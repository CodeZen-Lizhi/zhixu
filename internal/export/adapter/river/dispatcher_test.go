package river

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowriver "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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

func TestTransactionalDispatcherRequiresDatabaseAndClient(t *testing.T) {
	t.Parallel()

	if _, err := NewTransactionalDispatcher(nil, nil); exportDispatcherErrorCode(err) != "EXPORT_RIVER_DATABASE_UNAVAILABLE" {
		t.Fatalf("nil database error = %v", err)
	}
	if _, err := NewTransactionalDispatcher(&pgxpool.Pool{}, nil); exportDispatcherErrorCode(err) != "EXPORT_RIVER_DISPATCHER_UNAVAILABLE" {
		t.Fatalf("nil client error = %v", err)
	}
}

func TestTransactionalDispatcherFailsClosedWhenPartiallyConstructed(t *testing.T) {
	t.Parallel()

	tx := &dispatcherTx{}
	dispatcher := &Dispatcher{database: transactionBeginnerFunc(func(context.Context) (pgx.Tx, error) { return tx, nil })}
	err := dispatcher.Dispatch(context.Background(), foundation.ID("10000000-0000-4000-8000-000000000001"), foundation.ID("20000000-0000-4000-8000-000000000002"))
	if exportDispatcherErrorCode(err) != "EXPORT_RIVER_TRANSACTIONAL_DISPATCHER_UNAVAILABLE" {
		t.Fatalf("partial dispatcher error = %v", err)
	}
	if tx.committed || tx.rolledBack {
		t.Fatalf("partial dispatcher touched transaction: committed=%t rolled_back=%t", tx.committed, tx.rolledBack)
	}
}

func TestTransactionalDispatcherCommitsFencedInsertWithExportUniqueness(t *testing.T) {
	t.Parallel()

	tx := &dispatcherTx{}
	var gotOptions workflowriver.InsertOptions
	dispatcher := &Dispatcher{
		database: transactionBeginnerFunc(func(context.Context) (pgx.Tx, error) { return tx, nil }),
		inserter: exportJobInserterFunc(func(_ context.Context, gotTransaction any, args Args, options workflowriver.InsertOptions) (workflowriver.JobReceipt, error) {
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
	if !tx.committed || !tx.rolledBack {
		t.Fatalf("transaction committed=%t rolled_back=%t", tx.committed, tx.rolledBack)
	}
	if !slices.Equal(gotOptions.UniqueStates, exportUniqueOpts().ByState) {
		t.Fatalf("unique states = %#v", gotOptions.UniqueStates)
	}
}

func TestTransactionalDispatcherDoesNotCommitRejectedInsert(t *testing.T) {
	t.Parallel()

	tx := &dispatcherTx{}
	wantErr := errors.New("rollout is draining")
	dispatcher := &Dispatcher{
		database: transactionBeginnerFunc(func(context.Context) (pgx.Tx, error) { return tx, nil }),
		inserter: exportJobInserterFunc(func(context.Context, any, Args, workflowriver.InsertOptions) (workflowriver.JobReceipt, error) {
			return workflowriver.JobReceipt{}, wantErr
		}),
	}
	err := dispatcher.Dispatch(context.Background(), foundation.ID("10000000-0000-4000-8000-000000000001"), foundation.ID("20000000-0000-4000-8000-000000000002"))
	if !errors.Is(err, wantErr) || tx.committed || !tx.rolledBack {
		t.Fatalf("error=%v committed=%t rolled_back=%t", err, tx.committed, tx.rolledBack)
	}
}

func TestTransactionalDispatcherClassifiesTransactionFailures(t *testing.T) {
	t.Parallel()

	workspaceID := foundation.ID("10000000-0000-4000-8000-000000000001")
	exportID := foundation.ID("20000000-0000-4000-8000-000000000002")
	t.Run("begin", func(t *testing.T) {
		dispatcher := &Dispatcher{
			database: transactionBeginnerFunc(func(context.Context) (pgx.Tx, error) {
				return nil, errors.New("database unavailable")
			}),
			inserter: exportJobInserterFunc(func(context.Context, any, Args, workflowriver.InsertOptions) (workflowriver.JobReceipt, error) {
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
		dispatcher := &Dispatcher{
			database: transactionBeginnerFunc(func(context.Context) (pgx.Tx, error) { return tx, nil }),
			inserter: exportJobInserterFunc(func(context.Context, any, Args, workflowriver.InsertOptions) (workflowriver.JobReceipt, error) {
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

type transactionBeginnerFunc func(context.Context) (pgx.Tx, error)

func (fn transactionBeginnerFunc) Begin(ctx context.Context) (pgx.Tx, error) { return fn(ctx) }

type exportJobInserterFunc func(context.Context, any, Args, workflowriver.InsertOptions) (workflowriver.JobReceipt, error)

func (fn exportJobInserterFunc) InsertTx(ctx context.Context, transaction any, args Args, options workflowriver.InsertOptions) (workflowriver.JobReceipt, error) {
	return fn(ctx, transaction, args, options)
}

type dispatcherTx struct {
	pgx.Tx
	committed  bool
	commitErr  error
	rolledBack bool
}

func (tx *dispatcherTx) Commit(context.Context) error {
	tx.committed = true
	return tx.commitErr
}

func (tx *dispatcherTx) Rollback(context.Context) error {
	tx.rolledBack = true
	return nil
}

func exportDispatcherErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}
