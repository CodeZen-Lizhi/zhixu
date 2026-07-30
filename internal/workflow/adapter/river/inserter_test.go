package riveradapter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	"github.com/jackc/pgx/v5"
	riverlib "github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

type genericJobArgs struct {
	SchemaVersion int `json:"schema_version"`
}

func (genericJobArgs) Kind() string { return "workflow_generic_test" }

func TestTypedJobInserterValidatesAndInsertsArbitraryJobArgs(t *testing.T) {
	var (
		gotArgs riverlib.JobArgs
		gotOpts *riverlib.InsertOpts
	)
	client := &Client{
		insert: insertClientFunc(func(_ context.Context, _ pgx.Tx, args riverlib.JobArgs, opts *riverlib.InsertOpts) (*rivertype.JobInsertResult, error) {
			gotArgs = args
			gotOpts = opts
			return &rivertype.JobInsertResult{Job: &rivertype.JobRow{ID: 41}}, nil
		}),
		queue: "workflow_generic",
		fence: enqueueFenceFunc(func(context.Context, pgx.Tx) error { return nil }),
	}
	validated := false
	inserter, err := NewTypedJobInserter(client, func(args genericJobArgs) error {
		validated = true
		if args.SchemaVersion != 1 {
			return errors.New("unsupported schema version")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	scheduledAt := time.Date(2026, time.July, 18, 10, 30, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	var tx pgx.Tx = fakeTx{}
	receipt, err := inserter.InsertTx(context.Background(), tx, genericJobArgs{SchemaVersion: 1}, InsertOptions{ScheduledAt: scheduledAt})
	if err != nil {
		t.Fatal(err)
	}
	if !validated {
		t.Fatal("job args validator was not called")
	}
	if receipt.JobID != 41 || receipt.Duplicate {
		t.Fatalf("receipt = %+v", receipt)
	}
	if gotArgs != (genericJobArgs{SchemaVersion: 1}) {
		t.Fatalf("args = %#v", gotArgs)
	}
	if gotOpts == nil || gotOpts.Queue != "workflow_generic" || !gotOpts.UniqueOpts.ByArgs {
		t.Fatalf("insert opts = %+v", gotOpts)
	}
	if !gotOpts.ScheduledAt.Equal(scheduledAt.UTC()) || gotOpts.ScheduledAt.Location() != time.UTC {
		t.Fatalf("scheduled_at = %s", gotOpts.ScheduledAt)
	}
}

func TestTypedJobInserterPreservesCustomUniqueStates(t *testing.T) {
	t.Parallel()

	var gotOpts *riverlib.InsertOpts
	client := &Client{
		insert: insertClientFunc(func(_ context.Context, _ pgx.Tx, _ riverlib.JobArgs, opts *riverlib.InsertOpts) (*rivertype.JobInsertResult, error) {
			gotOpts = opts
			return &rivertype.JobInsertResult{Job: &rivertype.JobRow{ID: 45}}, nil
		}),
		queue: "workflow_custom_unique",
	}
	inserter, err := NewTypedJobInserter(client, func(genericJobArgs) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	states := []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStateRunning}
	var tx pgx.Tx = fakeTx{}
	if _, err := inserter.InsertTx(context.Background(), tx, genericJobArgs{SchemaVersion: 1}, InsertOptions{UniqueStates: states}); err != nil {
		t.Fatal(err)
	}
	states[0] = rivertype.JobStateCancelled
	if gotOpts == nil || len(gotOpts.UniqueOpts.ByState) != 2 || gotOpts.UniqueOpts.ByState[0] != rivertype.JobStateAvailable || gotOpts.UniqueOpts.ByState[1] != rivertype.JobStateRunning {
		t.Fatalf("unique states = %#v", gotOpts)
	}
}

func TestTypedJobInserterRejectsInvalidArgsBeforeTouchingTransaction(t *testing.T) {
	validationErr := errors.New("invalid generic args")
	called := false
	inserter := &TypedJobInserter[genericJobArgs]{
		client: insertClientFunc(func(context.Context, pgx.Tx, riverlib.JobArgs, *riverlib.InsertOpts) (*rivertype.JobInsertResult, error) {
			called = true
			return nil, nil
		}),
		validate: func(genericJobArgs) error { return validationErr },
	}
	_, err := inserter.InsertTx(context.Background(), (*pgx.Tx)(nil), genericJobArgs{}, InsertOptions{})
	if !errors.Is(err, validationErr) {
		t.Fatalf("error = %v", err)
	}
	if called {
		t.Fatal("invalid args reached River client")
	}
}

func TestNewTypedJobInserterRequiresValidator(t *testing.T) {
	client := &Client{insert: insertClientFunc(func(context.Context, pgx.Tx, riverlib.JobArgs, *riverlib.InsertOpts) (*rivertype.JobInsertResult, error) {
		return nil, nil
	})}
	_, err := NewTypedJobInserter[genericJobArgs](client, nil)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "WORKFLOW_RIVER_JOB_VALIDATOR_MISSING" || classified.Kind != foundation.ErrorDependencyUnavailable {
		t.Fatalf("error = %v", err)
	}
}

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

func TestJobInserterPreservesValidationBeforeNilReceiverCheck(t *testing.T) {
	var inserter *RiverJobInserter
	_, err := inserter.InsertTx(context.Background(), nil, NodeJobArgs{}, InsertOptions{})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "WORKFLOW_NODE_JOB_SCHEMA_INVALID" || classified.Kind != foundation.ErrorInvalidInput {
		t.Fatalf("error = %v", err)
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

type enqueueFenceFunc func(context.Context, pgx.Tx) error

func (f enqueueFenceFunc) CheckEnqueue(ctx context.Context, tx pgx.Tx) error {
	return f(ctx, tx)
}

func TestTypedJobInserterChecksFenceBeforeRiverInsert(t *testing.T) {
	t.Parallel()
	fenceErr := errors.New("rollout is draining")
	inserted := false
	client := &Client{
		insert: insertClientFunc(func(context.Context, pgx.Tx, riverlib.JobArgs, *riverlib.InsertOpts) (*rivertype.JobInsertResult, error) {
			inserted = true
			return &rivertype.JobInsertResult{Job: &rivertype.JobRow{ID: 99}}, nil
		}),
		queue: "workflow",
		fence: enqueueFenceFunc(func(context.Context, pgx.Tx) error { return fenceErr }),
	}
	inserter, err := NewTypedJobInserter(client, func(genericJobArgs) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	var tx pgx.Tx = fakeTx{}
	if _, err := inserter.InsertTx(context.Background(), tx, genericJobArgs{SchemaVersion: 1}, InsertOptions{}); !errors.Is(err, fenceErr) {
		t.Fatalf("InsertTx error = %v", err)
	}
	if inserted {
		t.Fatal("River insert ran after enqueue fence rejection")
	}
}

type fakeTx struct{ pgx.Tx }
