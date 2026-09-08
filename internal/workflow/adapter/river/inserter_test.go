package riveradapter

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
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
	client := &inserterFixture{
		insert: insertClientFunc(func(_ context.Context, _ *sql.Tx, args riverlib.JobArgs, opts *riverlib.InsertOpts) (*rivertype.JobInsertResult, error) {
			gotArgs = args
			gotOpts = opts
			return &rivertype.JobInsertResult{Job: &rivertype.JobRow{ID: 41}}, nil
		}),
		queue: "workflow_generic",
		fence: enqueueFenceFunc(func(context.Context, foundation.TransactionScope) error { return nil }),
	}
	validated := false
	inserter, err := newScopedTypedInserterFixture(t, client, func(args genericJobArgs) error {
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
	var tx foundation.TransactionScope = fakeTx{}
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
	client := &inserterFixture{
		insert: insertClientFunc(func(_ context.Context, _ *sql.Tx, _ riverlib.JobArgs, opts *riverlib.InsertOpts) (*rivertype.JobInsertResult, error) {
			gotOpts = opts
			return &rivertype.JobInsertResult{Job: &rivertype.JobRow{ID: 45}}, nil
		}),
		queue: "workflow_custom_unique",
	}
	inserter, err := newScopedTypedInserterFixture(t, client, func(genericJobArgs) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	states := []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStateRunning}
	var tx foundation.TransactionScope = fakeTx{}
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
	inserter := &ScopedTypedJobInserter[genericJobArgs]{
		resolveTx: fixtureSQLTransaction,
		client: insertClientFunc(func(context.Context, *sql.Tx, riverlib.JobArgs, *riverlib.InsertOpts) (*rivertype.JobInsertResult, error) {
			called = true
			return nil, nil
		}),
		validate: func(genericJobArgs) error { return validationErr },
	}
	_, err := inserter.InsertTx(context.Background(), nil, genericJobArgs{}, InsertOptions{})
	if !errors.Is(err, validationErr) {
		t.Fatalf("error = %v", err)
	}
	if called {
		t.Fatal("invalid args reached River client")
	}
}

func TestNewTypedJobInserterRequiresValidator(t *testing.T) {
	client := &inserterFixture{insert: insertClientFunc(func(context.Context, *sql.Tx, riverlib.JobArgs, *riverlib.InsertOpts) (*rivertype.JobInsertResult, error) {
		return nil, nil
	})}
	_, err := newScopedTypedInserterFixture[genericJobArgs](t, client, nil)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "WORKFLOW_RIVER_JOB_VALIDATOR_MISSING" || classified.Kind != foundation.ErrorDependencyUnavailable {
		t.Fatalf("error = %v", err)
	}
}

func TestJobInserterRejectsInvalidArgsBeforeTouchingTransaction(t *testing.T) {
	called := false
	inserter := &ScopedRiverJobInserter{typed: &ScopedTypedJobInserter[NodeJobArgs]{validate: ValidateNodeJobArgs, resolveTx: fixtureSQLTransaction, fence: enqueueFenceFunc(func(context.Context, foundation.TransactionScope) error { return nil }), client: insertClientFunc(func(context.Context, *sql.Tx, riverlib.JobArgs, *riverlib.InsertOpts) (*rivertype.JobInsertResult, error) {
		called = true
		return nil, nil
	})}}
	_, err := inserter.InsertTx(context.Background(), nil, NodeJobArgs{}, InsertOptions{})
	if err == nil {
		t.Fatal("invalid args unexpectedly succeeded")
	}
	if called {
		t.Fatal("invalid args reached River client")
	}
}

func TestJobInserterPreservesValidationBeforeNilReceiverCheck(t *testing.T) {
	var inserter *ScopedRiverJobInserter
	_, err := inserter.InsertTx(context.Background(), nil, NodeJobArgs{}, InsertOptions{})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "WORKFLOW_NODE_JOB_SCHEMA_INVALID" || classified.Kind != foundation.ErrorInvalidInput {
		t.Fatalf("error = %v", err)
	}
}

func TestJobInserterReturnsExistingJobOnUniqueDuplicate(t *testing.T) {
	var gotOpts *riverlib.InsertOpts
	inserter := &ScopedRiverJobInserter{typed: &ScopedTypedJobInserter[NodeJobArgs]{validate: ValidateNodeJobArgs, resolveTx: fixtureSQLTransaction, fence: enqueueFenceFunc(func(context.Context, foundation.TransactionScope) error { return nil }), client: insertClientFunc(func(_ context.Context, _ *sql.Tx, _ riverlib.JobArgs, opts *riverlib.InsertOpts) (*rivertype.JobInsertResult, error) {
		gotOpts = opts
		return &rivertype.JobInsertResult{Job: &rivertype.JobRow{ID: 42}, UniqueSkippedAsDuplicate: true}, nil
	}), queue: "workflow_priority"}}
	args, _ := NewNodeJobArgs(foundation.ID("10000000-0000-4000-8000-000000000001"), 2)
	var tx foundation.TransactionScope = fakeTx{}
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
	client := &inserterFixture{
		insert: insertClientFunc(func(_ context.Context, _ *sql.Tx, _ riverlib.JobArgs, opts *riverlib.InsertOpts) (*rivertype.JobInsertResult, error) {
			gotOpts = opts
			return &rivertype.JobInsertResult{Job: &rivertype.JobRow{ID: 43}}, nil
		}),
		queue: "workflow_custom",
	}
	inserter, err := newScopedNodeInserterFixture(t, client)
	if err != nil {
		t.Fatal(err)
	}
	args, _ := NewNodeJobArgs(foundation.ID("10000000-0000-4000-8000-000000000001"), 2)
	var tx foundation.TransactionScope = fakeTx{}
	if _, err := inserter.InsertTx(context.Background(), tx, args, InsertOptions{}); err != nil {
		t.Fatal(err)
	}
	if gotOpts == nil || gotOpts.Queue != client.Queue() {
		t.Fatalf("insert queue=%q client queue=%q", gotOpts.Queue, client.Queue())
	}
}

func TestJobInserterPropagatesOnlyTraceMetadata(t *testing.T) {
	var gotOpts *riverlib.InsertOpts
	inserter := &ScopedRiverJobInserter{typed: &ScopedTypedJobInserter[NodeJobArgs]{validate: ValidateNodeJobArgs, resolveTx: fixtureSQLTransaction, fence: enqueueFenceFunc(func(context.Context, foundation.TransactionScope) error { return nil }), client: insertClientFunc(func(_ context.Context, _ *sql.Tx, _ riverlib.JobArgs, opts *riverlib.InsertOpts) (*rivertype.JobInsertResult, error) {
		gotOpts = opts
		return &rivertype.JobInsertResult{Job: &rivertype.JobRow{ID: 44}}, nil
	})}}
	trace, err := observability.WithTraceContext(context.Background(), observability.TraceContext{
		TraceID: "0123456789abcdef0123456789abcdef", SpanID: "0123456789abcdef", TraceFlags: "01",
	})
	if err != nil {
		t.Fatal(err)
	}
	args, _ := NewNodeJobArgs(foundation.ID("10000000-0000-4000-8000-000000000001"), 2)
	var tx foundation.TransactionScope = fakeTx{}
	if _, err := inserter.InsertTx(trace, tx, args, InsertOptions{}); err != nil {
		t.Fatal(err)
	}
	if gotOpts == nil || string(gotOpts.Metadata) != `{"traceparent":"00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"}` {
		t.Fatalf("metadata = %s", gotOpts.Metadata)
	}
}

type insertClientFunc func(context.Context, *sql.Tx, riverlib.JobArgs, *riverlib.InsertOpts) (*rivertype.JobInsertResult, error)

func (f insertClientFunc) InsertTx(ctx context.Context, tx *sql.Tx, args riverlib.JobArgs, opts *riverlib.InsertOpts) (*rivertype.JobInsertResult, error) {
	return f(ctx, tx, args, opts)
}

type enqueueFenceFunc func(context.Context, foundation.TransactionScope) error

func (f enqueueFenceFunc) CheckEnqueue(ctx context.Context, tx foundation.TransactionScope) error {
	return f(ctx, tx)
}

func TestTypedJobInserterChecksFenceBeforeRiverInsert(t *testing.T) {
	t.Parallel()
	fenceErr := errors.New("rollout is draining")
	inserted := false
	client := &inserterFixture{
		insert: insertClientFunc(func(context.Context, *sql.Tx, riverlib.JobArgs, *riverlib.InsertOpts) (*rivertype.JobInsertResult, error) {
			inserted = true
			return &rivertype.JobInsertResult{Job: &rivertype.JobRow{ID: 99}}, nil
		}),
		queue: "workflow",
		fence: enqueueFenceFunc(func(context.Context, foundation.TransactionScope) error { return fenceErr }),
	}
	inserter, err := newScopedTypedInserterFixture(t, client, func(genericJobArgs) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	var tx foundation.TransactionScope = fakeTx{}
	if _, err := inserter.InsertTx(context.Background(), tx, genericJobArgs{SchemaVersion: 1}, InsertOptions{}); !errors.Is(err, fenceErr) {
		t.Fatalf("InsertTx error = %v", err)
	}
	if inserted {
		t.Fatal("River insert ran after enqueue fence rejection")
	}

	t.Run("static admission requires a live context and scope", func(t *testing.T) {
		cancellationCause := errors.New("worker stopped")
		cancelled, cancel := context.WithCancelCause(t.Context())
		cancel(cancellationCause)
		deadlineCause := errors.New("enqueue budget exhausted")
		deadline, stopDeadline := context.WithDeadlineCause(t.Context(), time.Now().Add(-time.Second), deadlineCause)
		defer stopDeadline()

		cases := []struct {
			name      string
			ctx       context.Context
			kind      foundation.ErrorKind
			code      string
			retryable bool
			sentinel  error
			cause     error
		}{
			{name: "nil context", kind: foundation.ErrorInvalidInput, code: "WORKFLOW_RIVER_CONTEXT_INVALID"},
			{name: "missing scope", ctx: t.Context(), kind: foundation.ErrorDependencyUnavailable, code: "WORKFLOW_RIVER_TRANSACTION_INVALID", retryable: true},
			{name: "cancel cause", ctx: cancelled, kind: foundation.ErrorNonRetryableFailure, code: "WORKFLOW_RIVER_JOB_INSERT_FAILED", sentinel: context.Canceled, cause: cancellationCause},
			{name: "deadline cause", ctx: deadline, kind: foundation.ErrorRetryableFailure, code: "WORKFLOW_RIVER_JOB_INSERT_FAILED", retryable: true, sentinel: context.DeadlineExceeded, cause: deadlineCause},
		}
		for _, test := range cases {
			t.Run(test.name, func(t *testing.T) {
				err := NewStaticScopedEnqueueFence().CheckEnqueue(test.ctx, nil)
				var classified *foundation.Error
				if !errors.As(err, &classified) || classified.Kind != test.kind || classified.Code != test.code || classified.Retryable != test.retryable {
					t.Fatalf("static admission error = %v", err)
				}
				if test.sentinel != nil && (!errors.Is(err, test.sentinel) || !errors.Is(err, test.cause)) {
					t.Fatalf("static admission lost context cause: %v", err)
				}
			})
		}
	})
}

type fakeTx struct{}

func (fakeTx) TransactionScope() {}

// inserterFixture keeps SQL insertion observable while constructing the public
// scoped producer from a real, lazy platform pool.
type inserterFixture struct {
	insert sqlRiverInsertClient
	queue  string
	fence  ScopedEnqueueFence
}

func (f *inserterFixture) Queue() string { return f.queue }

func fixtureSQLTransaction(foundation.TransactionScope) (*sql.Tx, error) { return &sql.Tx{}, nil }

func newScopedTypedInserterFixture[T riverlib.JobArgs](t *testing.T, fixture *inserterFixture, validate func(T) error) (*ScopedTypedJobInserter[T], error) {
	t.Helper()
	pool, client, fence := scopedInserterDependencies(t, fixture)
	inserter, err := NewScopedTypedJobInserter(pool, client, validate, fence)
	if err != nil {
		return nil, err
	}
	inserter.client = fixture.insert
	inserter.resolveTx = fixtureSQLTransaction
	return inserter, nil
}

func newScopedNodeInserterFixture(t *testing.T, fixture *inserterFixture) (ScopedJobInserter, error) {
	t.Helper()
	pool, client, fence := scopedInserterDependencies(t, fixture)
	inserter, err := NewScopedJobInserter(pool, client, fence)
	if err != nil {
		return nil, err
	}
	inserter.(*ScopedRiverJobInserter).typed.client = fixture.insert
	inserter.(*ScopedRiverJobInserter).typed.resolveTx = fixtureSQLTransaction
	return inserter, nil
}

func scopedInserterDependencies(t *testing.T, fixture *inserterFixture) (*platformpostgres.Pool, *Client, ScopedEnqueueFence) {
	t.Helper()
	pool, err := platformpostgres.Open(t.Context(), "postgres://127.0.0.1:1/workflow_unit?sslmode=disable", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	options := DefaultOptions()
	if fixture.queue != "" {
		options.Queue = fixture.queue
	}
	client, err := NewClientWithOptions(pool.DB(), nil, options)
	if err != nil {
		t.Fatal(err)
	}
	fence := fixture.fence
	if fence == nil {
		fence = enqueueFenceFunc(func(context.Context, foundation.TransactionScope) error { return nil })
	}
	return pool, client, fence
}
