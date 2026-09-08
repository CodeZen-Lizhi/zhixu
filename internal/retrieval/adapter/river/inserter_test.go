package river

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
)

type inserterTestScope struct{ identity int }

func (inserterTestScope) TransactionScope() {}

type typedInserterFunc func(context.Context, foundation.TransactionScope, Args, riveradapter.InsertOptions) (riveradapter.JobReceipt, error)

func (f typedInserterFunc) InsertTx(ctx context.Context, tx foundation.TransactionScope, args Args, options riveradapter.InsertOptions) (riveradapter.JobReceipt, error) {
	return f(ctx, tx, args, options)
}

func TestApplicationInserterMapsOnlyDeliveryIdentity(t *testing.T) {
	t.Parallel()
	var got Args
	transport := &ScopedInserter{typed: typedInserterFunc(func(_ context.Context, _ foundation.TransactionScope, args Args, _ riveradapter.InsertOptions) (riveradapter.JobReceipt, error) {
		got = args
		return riveradapter.JobReceipt{JobID: 43, Duplicate: true}, nil
	})}
	inserter := &ScopedApplicationInserter{transport: transport}
	receipt, err := inserter.InsertScoped(context.Background(), &inserterTestScope{identity: 1}, application.ReindexJob{
		DeliveryID: "10000000-0000-4000-8000-000000000001", DispatchNo: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != (Args{SchemaVersion: JobSchemaVersion, DeliveryID: "10000000-0000-4000-8000-000000000001", DispatchNo: 7}) || receipt.JobID != 43 || !receipt.Duplicate {
		t.Fatalf("args=%#v receipt=%#v", got, receipt)
	}
}

func TestInserterInsertsValidatedArgsWithUTCSchedule(t *testing.T) {
	args, err := NewArgs("10000000-0000-4000-8000-000000000001", 3)
	if err != nil {
		t.Fatal(err)
	}
	tx := &inserterTestScope{identity: 1}
	scheduledAt := time.Date(2026, time.July, 18, 10, 30, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	var (
		gotTx      foundation.TransactionScope
		gotArgs    Args
		gotOptions riveradapter.InsertOptions
	)
	inserter := &ScopedInserter{typed: typedInserterFunc(func(_ context.Context, transaction foundation.TransactionScope, inserted Args, options riveradapter.InsertOptions) (riveradapter.JobReceipt, error) {
		gotTx = transaction
		gotArgs = inserted
		gotOptions = options
		return riveradapter.JobReceipt{JobID: 41}, nil
	})}

	receipt, err := inserter.InsertScoped(context.Background(), tx, args, scheduledAt)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.JobID != 41 || receipt.Duplicate {
		t.Fatalf("receipt=%+v", receipt)
	}
	if gotTx != tx || gotArgs != args {
		t.Fatalf("tx=%#v args=%#v", gotTx, gotArgs)
	}
	if !gotOptions.ScheduledAt.Equal(scheduledAt.UTC()) || gotOptions.ScheduledAt.Location() != time.UTC {
		t.Fatalf("scheduled_at=%s location=%s", gotOptions.ScheduledAt, gotOptions.ScheduledAt.Location())
	}
}

func TestInserterPreservesDuplicateReceipt(t *testing.T) {
	args, err := NewArgs("10000000-0000-4000-8000-000000000001", 1)
	if err != nil {
		t.Fatal(err)
	}
	inserter := &ScopedInserter{typed: typedInserterFunc(func(context.Context, foundation.TransactionScope, Args, riveradapter.InsertOptions) (riveradapter.JobReceipt, error) {
		return riveradapter.JobReceipt{JobID: 42, Duplicate: true}, nil
	})}

	receipt, err := inserter.InsertScoped(context.Background(), &inserterTestScope{identity: 1}, args, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.JobID != 42 || !receipt.Duplicate {
		t.Fatalf("receipt=%+v", receipt)
	}
}

func TestInserterPreservesSharedInserterError(t *testing.T) {
	args, err := NewArgs("10000000-0000-4000-8000-000000000001", 1)
	if err != nil {
		t.Fatal(err)
	}
	expected := foundation.NewError(foundation.ErrorRetryableFailure, "WORKFLOW_RIVER_JOB_INSERT_FAILED", true, errors.New("injected insert failure"))
	inserter := &ScopedInserter{typed: typedInserterFunc(func(context.Context, foundation.TransactionScope, Args, riveradapter.InsertOptions) (riveradapter.JobReceipt, error) {
		return riveradapter.JobReceipt{}, expected
	})}

	_, err = inserter.InsertScoped(context.Background(), &inserterTestScope{identity: 1}, args, time.Time{})
	if !errors.Is(err, expected) || errorCode(err) != "WORKFLOW_RIVER_JOB_INSERT_FAILED" {
		t.Fatalf("error=%v", err)
	}
}

func TestInserterRejectsInvalidArgsBeforeTransaction(t *testing.T) {
	called := false
	inserter := &ScopedInserter{typed: typedInserterFunc(func(context.Context, foundation.TransactionScope, Args, riveradapter.InsertOptions) (riveradapter.JobReceipt, error) {
		called = true
		return riveradapter.JobReceipt{}, nil
	})}

	_, err := inserter.InsertScoped(context.Background(), nil, Args{}, time.Time{})
	if errorCode(err) != "REINDEX_JOB_SCHEMA_INVALID" {
		t.Fatalf("error=%v", err)
	}
	if called {
		t.Fatal("invalid args reached shared inserter")
	}
}

func TestNewInserterPreservesNilClientError(t *testing.T) {
	_, err := NewScopedInserter(&platformpostgres.Pool{}, nil, nil)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "WORKFLOW_RIVER_CLIENT_MISSING" || classified.Kind != foundation.ErrorDependencyUnavailable {
		t.Fatalf("error=%v", err)
	}
}

func TestNilInserterValidatesArgsBeforePreservingSharedError(t *testing.T) {
	var inserter *ScopedInserter
	if _, err := inserter.InsertScoped(context.Background(), nil, Args{}, time.Time{}); errorCode(err) != "REINDEX_JOB_SCHEMA_INVALID" {
		t.Fatalf("invalid args error=%v", err)
	}

	args, err := NewArgs("10000000-0000-4000-8000-000000000001", 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = inserter.InsertScoped(context.Background(), nil, args, time.Time{})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "WORKFLOW_RIVER_INSERTER_MISSING" || classified.Kind != foundation.ErrorDependencyUnavailable {
		t.Fatalf("error=%v", err)
	}
}
