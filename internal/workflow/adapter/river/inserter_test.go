package riveradapter

import (
	"context"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
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
	}), queue: "workflow_priority"}
	args, _ := NewNodeJobArgs(foundation.ID("10000000-0000-4000-8000-000000000001"), 2)
	var tx pgx.Tx = fakeTx{}
	receipt, err := inserter.InsertTx(context.Background(), tx, args, InsertOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.JobID != 42 || !receipt.Duplicate {
		t.Fatalf("receipt = %+v", receipt)
	}
	if gotOpts == nil || gotOpts.Queue != "workflow_priority" || !gotOpts.UniqueOpts.ByArgs || gotOpts.Metadata != nil {
		t.Fatalf("insert opts = %+v", gotOpts)
	}
}

func TestNewJobInserterUsesClientQueue(t *testing.T) {
	var gotOpts *riverlib.InsertOpts
	client := &Client{
		insert: insertClientFunc(func(_ context.Context, _ pgx.Tx, _ riverlib.JobArgs, opts *riverlib.InsertOpts) (*rivertype.JobInsertResult, error) {
			gotOpts = opts
			return &rivertype.JobInsertResult{Job: &rivertype.JobRow{ID: 43}}, nil
		}),
		queue: "workflow_custom",
	}
	inserter, err := NewJobInserter(client)
	if err != nil {
		t.Fatal(err)
	}
	args, _ := NewNodeJobArgs(foundation.ID("10000000-0000-4000-8000-000000000001"), 2)
	var tx pgx.Tx = fakeTx{}
	if _, err := inserter.InsertTx(context.Background(), tx, args, InsertOptions{}); err != nil {
		t.Fatal(err)
	}
	if gotOpts == nil || gotOpts.Queue != client.Queue() {
		t.Fatalf("insert queue=%q client queue=%q", gotOpts.Queue, client.Queue())
	}
}

func TestJobInserterPropagatesOnlyTraceMetadata(t *testing.T) {
	var gotOpts *riverlib.InsertOpts
	inserter := &RiverJobInserter{client: insertClientFunc(func(_ context.Context, _ pgx.Tx, _ riverlib.JobArgs, opts *riverlib.InsertOpts) (*rivertype.JobInsertResult, error) {
		gotOpts = opts
		return &rivertype.JobInsertResult{Job: &rivertype.JobRow{ID: 44}}, nil
	})}
	trace, err := observability.WithTraceContext(context.Background(), observability.TraceContext{
		TraceID: "0123456789abcdef0123456789abcdef", SpanID: "0123456789abcdef", TraceFlags: "01",
	})
	if err != nil {
		t.Fatal(err)
	}
	args, _ := NewNodeJobArgs(foundation.ID("10000000-0000-4000-8000-000000000001"), 2)
	var tx pgx.Tx = fakeTx{}
	if _, err := inserter.InsertTx(trace, tx, args, InsertOptions{}); err != nil {
		t.Fatal(err)
	}
	if gotOpts == nil || string(gotOpts.Metadata) != `{"traceparent":"00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"}` {
		t.Fatalf("metadata = %s", gotOpts.Metadata)
	}
}

type insertClientFunc func(context.Context, pgx.Tx, riverlib.JobArgs, *riverlib.InsertOpts) (*rivertype.JobInsertResult, error)

func (f insertClientFunc) InsertTx(ctx context.Context, tx pgx.Tx, args riverlib.JobArgs, opts *riverlib.InsertOpts) (*rivertype.JobInsertResult, error) {
	return f(ctx, tx, args, opts)
}

type fakeTx struct{ pgx.Tx }
