package application

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestDispatcherAlternatesFirstAndRetryWhileBothHaveWork(t *testing.T) {
	store := &dispatcherStoreFake{available: map[DispatchPath]int{DispatchPathFirst: 3, DispatchPathRetry: 3}}
	dispatcher, err := NewScopedDispatcher(store, dispatcherJobInserterFake{})
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.DispatchBatch(context.Background(), 6); err != nil {
		t.Fatal(err)
	}
	want := []DispatchPath{DispatchPathFirst, DispatchPathRetry, DispatchPathFirst, DispatchPathRetry, DispatchPathFirst, DispatchPathRetry}
	if !reflect.DeepEqual(store.successful, want) {
		t.Fatalf("successful paths=%v, want %v", store.successful, want)
	}
}

func TestDispatcherFallsBackWithoutStarvingAvailablePath(t *testing.T) {
	store := &dispatcherStoreFake{available: map[DispatchPath]int{DispatchPathRetry: 2}}
	dispatcher, err := NewScopedDispatcher(store, dispatcherJobInserterFake{})
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.DispatchBatch(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if want := []DispatchPath{DispatchPathRetry, DispatchPathRetry}; !reflect.DeepEqual(store.successful, want) {
		t.Fatalf("successful paths=%v, want %v", store.successful, want)
	}
}

func TestDispatcherStopsAfterBothPathsAreEmpty(t *testing.T) {
	store := &dispatcherStoreFake{available: map[DispatchPath]int{}}
	dispatcher, err := NewScopedDispatcher(store, dispatcherJobInserterFake{})
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.DispatchBatch(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if want := []DispatchPath{DispatchPathFirst, DispatchPathRetry}; !reflect.DeepEqual(store.calls, want) {
		t.Fatalf("calls=%v, want %v", store.calls, want)
	}
}

func TestDispatcherRejectsInvalidBatchAndDependencies(t *testing.T) {
	if _, err := NewScopedDispatcher(nil, dispatcherJobInserterFake{}); dispatcherErrorCode(err) != "REINDEX_DISPATCHER_DEPENDENCY_UNAVAILABLE" {
		t.Fatalf("nil store error=%v", err)
	}
	var typedNil *dispatcherJobInserterFake
	if _, err := NewScopedDispatcher(&dispatcherStoreFake{}, typedNil); dispatcherErrorCode(err) != "REINDEX_DISPATCHER_DEPENDENCY_UNAVAILABLE" {
		t.Fatalf("typed nil inserter error=%v", err)
	}
	dispatcher, err := NewScopedDispatcher(&dispatcherStoreFake{}, dispatcherJobInserterFake{})
	if err != nil {
		t.Fatal(err)
	}
	for _, batch := range []int{0, maxDispatchBatchSize + 1} {
		if err := dispatcher.DispatchBatch(context.Background(), batch); dispatcherErrorCode(err) != "REINDEX_DISPATCH_BATCH_INVALID" {
			t.Fatalf("batch %d error=%v", batch, err)
		}
	}
}

func TestDispatcherPropagatesStoreFailure(t *testing.T) {
	cause := errors.New("store failed")
	dispatcher, err := NewScopedDispatcher(&dispatcherStoreFake{err: cause}, dispatcherJobInserterFake{})
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.DispatchBatch(context.Background(), 1); !errors.Is(err, cause) {
		t.Fatalf("DispatchBatch() error=%v", err)
	}
}

type dispatcherStoreFake struct {
	available  map[DispatchPath]int
	calls      []DispatchPath
	successful []DispatchPath
	err        error
}

func (f *dispatcherStoreFake) DispatchOneScoped(_ context.Context, path DispatchPath, _ ScopedJobInserter) (bool, error) {
	f.calls = append(f.calls, path)
	if f.err != nil {
		return false, f.err
	}
	if f.available[path] == 0 {
		return false, nil
	}
	f.available[path]--
	f.successful = append(f.successful, path)
	return true, nil
}

type dispatcherJobInserterFake struct{}

func (dispatcherJobInserterFake) InsertScoped(context.Context, foundation.TransactionScope, ReindexJob) (JobReceipt, error) {
	return JobReceipt{JobID: 1}, nil
}

func dispatcherErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}
