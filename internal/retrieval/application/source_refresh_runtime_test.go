package application

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestDynamicSourceRefresherHoldsOneLeaseAcrossCompleteOperation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  func(*DynamicSourceRefresher) (any, error)
		want any
	}{
		{
			name: "single",
			run: func(refresher *DynamicSourceRefresher) (any, error) {
				return refresher.Refresh(context.Background(), SourceRefreshRequest{})
			},
			want: SourceRefreshResult{IndexVersionID: foundation.ID("index-single")},
		},
		{
			name: "batch",
			run: func(refresher *DynamicSourceRefresher) (any, error) {
				return refresher.RefreshBatch(context.Background(), BatchSourceRefreshRequest{})
			},
			want: BatchSourceRefreshResult{IndexVersionID: foundation.ID("index-batch"), Reindexed: true},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			lifecycle := &sourceRefreshLifecycle{}
			runner := &sourceRefreshOperationRunnerFake{
				refresh: func(context.Context, SourceRefreshRequest) (SourceRefreshResult, error) {
					recordCompleteSourceRefresh(lifecycle)
					return SourceRefreshResult{IndexVersionID: foundation.ID("index-single")}, nil
				},
				refreshBatch: func(context.Context, BatchSourceRefreshRequest) (BatchSourceRefreshResult, error) {
					recordCompleteSourceRefresh(lifecycle)
					return BatchSourceRefreshResult{IndexVersionID: foundation.ID("index-batch"), Reindexed: true}, nil
				},
			}
			lease := &sourceRefreshOperationLeaseFake{runner: runner, onRelease: func() { lifecycle.add("release") }}
			acquirer := &sourceRefreshOperationAcquirerFake{acquire: func(context.Context, int) (SourceRefreshOperationLease, error) {
				lifecycle.add("acquire")
				return lease, nil
			}}
			refresher, err := NewDynamicSourceRefresher(acquirer)
			if err != nil {
				t.Fatal(err)
			}

			got, err := test.run(refresher)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("result = %#v, want %#v", got, test.want)
			}
			if calls := acquirer.callCount(); calls != 1 {
				t.Fatalf("Acquire() calls = %d", calls)
			}
			if calls, effects := lease.releaseCounts(); calls != 1 || effects != 1 {
				t.Fatalf("Release() calls=%d effects=%d", calls, effects)
			}
			wantLifecycle := []string{"acquire", "ingestion", "vector", "activation", "return", "release"}
			if gotLifecycle := lifecycle.snapshot(); !reflect.DeepEqual(gotLifecycle, wantLifecycle) {
				t.Fatalf("lifecycle = %v, want %v", gotLifecycle, wantLifecycle)
			}
		})
	}
}

func TestDynamicSourceRefresherPreservesRunnerErrorAndReleases(t *testing.T) {
	t.Parallel()

	for _, batch := range []bool{false, true} {
		batch := batch
		t.Run(map[bool]string{false: "single", true: "batch"}[batch], func(t *testing.T) {
			t.Parallel()
			cause := errors.New("classified source refresh failure")
			classified := foundation.NewError(foundation.ErrorConsistencyViolation, "SOURCE_REFRESH_TEST_FAILURE", false, cause)
			runner := &sourceRefreshOperationRunnerFake{
				refresh: func(context.Context, SourceRefreshRequest) (SourceRefreshResult, error) {
					return SourceRefreshResult{}, classified
				},
				refreshBatch: func(context.Context, BatchSourceRefreshRequest) (BatchSourceRefreshResult, error) {
					return BatchSourceRefreshResult{}, classified
				},
			}
			lease := &sourceRefreshOperationLeaseFake{runner: runner}
			refresher := mustDynamicSourceRefresher(t, &sourceRefreshOperationAcquirerFake{
				acquire: func(context.Context, int) (SourceRefreshOperationLease, error) { return lease, nil },
			})

			var err error
			if batch {
				_, err = refresher.RefreshBatch(context.Background(), BatchSourceRefreshRequest{})
			} else {
				_, err = refresher.Refresh(context.Background(), SourceRefreshRequest{})
			}
			if err != classified || !errors.Is(err, cause) {
				t.Fatalf("error = %v, want original classified error", err)
			}
			if calls, effects := lease.releaseCounts(); calls != 1 || effects != 1 {
				t.Fatalf("Release() calls=%d effects=%d", calls, effects)
			}
		})
	}
}

func TestDynamicSourceRefresherReleasesLeaseReturnedWithAcquireError(t *testing.T) {
	t.Parallel()

	cause := errors.New("runtime acquisition fenced")
	classified := foundation.NewError(foundation.ErrorDependencyUnavailable, "MODEL_RUNTIME_SWITCHING", true, cause)
	lease := &sourceRefreshOperationLeaseFake{runner: &sourceRefreshOperationRunnerFake{}}
	acquirer := &sourceRefreshOperationAcquirerFake{acquire: func(context.Context, int) (SourceRefreshOperationLease, error) {
		return lease, classified
	}}
	refresher := mustDynamicSourceRefresher(t, acquirer)

	_, err := refresher.RefreshBatch(context.Background(), BatchSourceRefreshRequest{})
	if err != classified || !errors.Is(err, cause) {
		t.Fatalf("error = %v, want original acquisition error", err)
	}
	if calls := acquirer.callCount(); calls != 1 {
		t.Fatalf("Acquire() calls = %d", calls)
	}
	if calls, effects := lease.releaseCounts(); calls != 1 || effects != 1 {
		t.Fatalf("Release() calls=%d effects=%d", calls, effects)
	}
}

func TestDynamicSourceRefresherFailsClosedForNilRuntimeParts(t *testing.T) {
	t.Parallel()

	if _, err := NewDynamicSourceRefresher(nil); !sourceRefreshRuntimeUnavailableError(err) {
		t.Fatalf("nil acquirer error = %v", err)
	}
	var typedNilAcquirer *sourceRefreshOperationAcquirerFake
	if _, err := NewDynamicSourceRefresher(typedNilAcquirer); !sourceRefreshRuntimeUnavailableError(err) {
		t.Fatalf("typed nil acquirer error = %v", err)
	}
	var nilFacade *DynamicSourceRefresher
	if _, err := nilFacade.Refresh(context.Background(), SourceRefreshRequest{}); !sourceRefreshRuntimeUnavailableError(err) {
		t.Fatalf("nil facade error = %v", err)
	}

	tests := []struct {
		name         string
		lease        func() SourceRefreshOperationLease
		wantReleases int32
	}{
		{name: "nil lease", lease: func() SourceRefreshOperationLease { return nil }},
		{name: "typed nil lease", lease: func() SourceRefreshOperationLease {
			var lease *sourceRefreshOperationLeaseFake
			return lease
		}},
		{name: "nil runner", lease: func() SourceRefreshOperationLease {
			return &sourceRefreshOperationLeaseFake{}
		}, wantReleases: 1},
		{name: "typed nil runner", lease: func() SourceRefreshOperationLease {
			var runner *sourceRefreshOperationRunnerFake
			return &sourceRefreshOperationLeaseFake{runner: runner}
		}, wantReleases: 1},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			lease := test.lease()
			refresher := mustDynamicSourceRefresher(t, &sourceRefreshOperationAcquirerFake{
				acquire: func(context.Context, int) (SourceRefreshOperationLease, error) { return lease, nil },
			})
			_, err := refresher.Refresh(context.Background(), SourceRefreshRequest{})
			if !sourceRefreshRuntimeUnavailableError(err) {
				t.Fatalf("error = %v", err)
			}
			if concrete, ok := lease.(*sourceRefreshOperationLeaseFake); ok && concrete != nil {
				if calls, _ := concrete.releaseCounts(); calls != test.wantReleases {
					t.Fatalf("Release() calls = %d, want %d", calls, test.wantReleases)
				}
			}
		})
	}
}

func TestDynamicSourceRefresherConcurrentOperationsUseIndependentLeases(t *testing.T) {
	const operationCount = 24

	entered := make(chan struct{}, operationCount)
	proceed := make(chan struct{})
	var active atomic.Int32
	var maxActive atomic.Int32
	var earlyRelease atomic.Int32
	var leasesMu sync.Mutex
	leases := make([]*sourceRefreshOperationLeaseFake, 0, operationCount)

	acquirer := &sourceRefreshOperationAcquirerFake{acquire: func(context.Context, int) (SourceRefreshOperationLease, error) {
		var running atomic.Bool
		run := func() {
			running.Store(true)
			current := active.Add(1)
			for observed := maxActive.Load(); current > observed && !maxActive.CompareAndSwap(observed, current); observed = maxActive.Load() {
			}
			entered <- struct{}{}
			<-proceed
			active.Add(-1)
			running.Store(false)
		}
		runner := &sourceRefreshOperationRunnerFake{
			refresh: func(context.Context, SourceRefreshRequest) (SourceRefreshResult, error) {
				run()
				return SourceRefreshResult{}, nil
			},
			refreshBatch: func(context.Context, BatchSourceRefreshRequest) (BatchSourceRefreshResult, error) {
				run()
				return BatchSourceRefreshResult{}, nil
			},
		}
		lease := &sourceRefreshOperationLeaseFake{runner: runner, onRelease: func() {
			if running.Load() {
				earlyRelease.Add(1)
			}
		}}
		leasesMu.Lock()
		leases = append(leases, lease)
		leasesMu.Unlock()
		return lease, nil
	}}
	refresher := mustDynamicSourceRefresher(t, acquirer)

	errorsOut := make(chan error, operationCount)
	var workers sync.WaitGroup
	workers.Add(operationCount)
	for operation := 0; operation < operationCount; operation++ {
		operation := operation
		go func() {
			defer workers.Done()
			if operation%2 == 0 {
				_, err := refresher.Refresh(context.Background(), SourceRefreshRequest{})
				errorsOut <- err
				return
			}
			_, err := refresher.RefreshBatch(context.Background(), BatchSourceRefreshRequest{})
			errorsOut <- err
		}()
	}
	for range operationCount {
		<-entered
	}
	close(proceed)
	workers.Wait()
	close(errorsOut)
	for err := range errorsOut {
		if err != nil {
			t.Fatal(err)
		}
	}

	if calls := acquirer.callCount(); calls != operationCount {
		t.Fatalf("Acquire() calls = %d, want %d", calls, operationCount)
	}
	if got := maxActive.Load(); got != operationCount {
		t.Fatalf("maximum concurrent runners = %d, want %d", got, operationCount)
	}
	if got := earlyRelease.Load(); got != 0 {
		t.Fatalf("leases released while runner active = %d", got)
	}
	leasesMu.Lock()
	defer leasesMu.Unlock()
	if len(leases) != operationCount {
		t.Fatalf("leases = %d, want %d", len(leases), operationCount)
	}
	for index, lease := range leases {
		if calls, effects := lease.releaseCounts(); calls != 1 || effects != 1 {
			t.Fatalf("lease %d Release() calls=%d effects=%d", index, calls, effects)
		}
	}
}

func recordCompleteSourceRefresh(lifecycle *sourceRefreshLifecycle) {
	lifecycle.add("ingestion")
	lifecycle.add("vector")
	lifecycle.add("activation")
	lifecycle.add("return")
}

func mustDynamicSourceRefresher(t *testing.T, acquirer SourceRefreshOperationAcquirer) *DynamicSourceRefresher {
	t.Helper()
	refresher, err := NewDynamicSourceRefresher(acquirer)
	if err != nil {
		t.Fatal(err)
	}
	return refresher
}

func sourceRefreshRuntimeUnavailableError(err error) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Kind == foundation.ErrorDependencyUnavailable &&
		classified.Code == sourceRefreshRuntimeUnavailableCode && classified.Retryable
}

type sourceRefreshOperationRunnerFake struct {
	refresh      func(context.Context, SourceRefreshRequest) (SourceRefreshResult, error)
	refreshBatch func(context.Context, BatchSourceRefreshRequest) (BatchSourceRefreshResult, error)
}

func (runner *sourceRefreshOperationRunnerFake) Refresh(ctx context.Context, request SourceRefreshRequest) (SourceRefreshResult, error) {
	if runner.refresh == nil {
		return SourceRefreshResult{}, nil
	}
	return runner.refresh(ctx, request)
}

func (runner *sourceRefreshOperationRunnerFake) RefreshBatch(ctx context.Context, request BatchSourceRefreshRequest) (BatchSourceRefreshResult, error) {
	if runner.refreshBatch == nil {
		return BatchSourceRefreshResult{}, nil
	}
	return runner.refreshBatch(ctx, request)
}

type sourceRefreshOperationLeaseFake struct {
	runner SourceRefreshOperationRunner

	releaseOnce    sync.Once
	releaseCalls   atomic.Int32
	releaseEffects atomic.Int32
	onRelease      func()
}

func (lease *sourceRefreshOperationLeaseFake) Runner() SourceRefreshOperationRunner {
	return lease.runner
}

func (lease *sourceRefreshOperationLeaseFake) Release() {
	lease.releaseCalls.Add(1)
	lease.releaseOnce.Do(func() {
		lease.releaseEffects.Add(1)
		if lease.onRelease != nil {
			lease.onRelease()
		}
	})
}

func (lease *sourceRefreshOperationLeaseFake) releaseCounts() (int32, int32) {
	return lease.releaseCalls.Load(), lease.releaseEffects.Load()
}

type sourceRefreshOperationAcquirerFake struct {
	mu      sync.Mutex
	calls   int
	acquire func(context.Context, int) (SourceRefreshOperationLease, error)
}

func (acquirer *sourceRefreshOperationAcquirerFake) Acquire(ctx context.Context) (SourceRefreshOperationLease, error) {
	acquirer.mu.Lock()
	acquirer.calls++
	call := acquirer.calls
	acquire := acquirer.acquire
	acquirer.mu.Unlock()
	return acquire(ctx, call)
}

func (acquirer *sourceRefreshOperationAcquirerFake) callCount() int {
	acquirer.mu.Lock()
	defer acquirer.mu.Unlock()
	return acquirer.calls
}

type sourceRefreshLifecycle struct {
	mu     sync.Mutex
	events []string
}

func (lifecycle *sourceRefreshLifecycle) add(event string) {
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	lifecycle.events = append(lifecycle.events, event)
}

func (lifecycle *sourceRefreshLifecycle) snapshot() []string {
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	return append([]string(nil), lifecycle.events...)
}
