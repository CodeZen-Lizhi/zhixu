package riveradapter

import (
	"context"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
	riverlib "github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

func TestJobInserterRejectsInvalidArgsBeforeTouchingTransaction(t *testing.T) {
	called := false
	inserter := &RiverJobInserter{client: insertClientFunc(func(context.Context, pgx.Tx, riverlib.JobArgs, *riverlib.InsertOpts) (*rivertype.JobInsertResult, error) {
		called = true
		return nil, nil
	})}
	_, err := inserter.InsertTx(context.Background(), (*pgx.Tx)(nil), NodeJobArgs{}, InsertOptions{})
	if err == nil {
		t.Fatal("invalid args unexpectedly succeeded")
	}
	if called {
		t.Fatal("invalid args reached River client")
	}
}

func TestJobInserterReturnsExistingJobOnUniqueDuplicate(t *testing.T) {
	var gotOpts *riverlib.InsertOpts
	inserter := &RiverJobInserter{client: insertClientFunc(func(_ context.Context, _ pgx.Tx, _ riverlib.JobArgs, opts *riverlib.InsertOpts) (*rivertype.JobInsertResult, error) {
		gotOpts = opts
		return &rivertype.JobInsertResult{Job: &rivertype.JobRow{ID: 42}, UniqueSkippedAsDuplicate: true}, nil
	})}
	args, _ := NewNodeJobArgs(foundation.ID("10000000-0000-4000-8000-000000000001"), 2)
	var tx pgx.Tx = fakeTx{}
	receipt, err := inserter.InsertTx(context.Background(), tx, args, InsertOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.JobID != 42 || !receipt.Duplicate {
		t.Fatalf("receipt = %+v", receipt)
	}
	if gotOpts == nil || !gotOpts.UniqueOpts.ByArgs || gotOpts.Metadata != nil {
		t.Fatalf("insert opts = %+v", gotOpts)
	}
}

type insertClientFunc func(context.Context, pgx.Tx, riverlib.JobArgs, *riverlib.InsertOpts) (*rivertype.JobInsertResult, error)

func (f insertClientFunc) InsertTx(ctx context.Context, tx pgx.Tx, args riverlib.JobArgs, opts *riverlib.InsertOpts) (*rivertype.JobInsertResult, error) {
	return f(ctx, tx, args, opts)
}

type fakeTx struct{ pgx.Tx }
