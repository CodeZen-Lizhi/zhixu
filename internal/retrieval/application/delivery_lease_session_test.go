package application

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

func TestDeliveryLeaseSessionSerializesHeartbeatAndCheckpoint(t *testing.T) {
	runtime := &leaseSessionRuntime{}
	initial := domain.DeliveryFence{
		DeliveryID: runtimeID(1), DispatchNo: 1, AttemptID: runtimeID(2),
		AttemptNo: 1, Owner: "worker-a", DeliveryVersion: 4,
	}
	session, err := NewDeliveryLeaseSession(runtime, initial)
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errorsOut := make(chan error, 2)
	go func() {
		<-start
		_, err := session.Heartbeat(context.Background(), time.Minute)
		errorsOut <- err
	}()
	go func() {
		<-start
		_, err := session.Checkpoint(context.Background(), domain.DeliveryCheckpoint{
			Stage: domain.DeliveryCheckpointSourceCaptured, SourceVersionID: runtimeID(3),
		})
		errorsOut <- err
	}()
	close(start)
	for range 2 {
		if err := <-errorsOut; err != nil {
			t.Fatal(err)
		}
	}

	snapshot := session.Snapshot()
	if runtime.maxConcurrent != 1 {
		t.Fatalf("runtime max concurrency = %d", runtime.maxConcurrent)
	}
	if snapshot.Disposition != DeliveryLeaseActive || snapshot.Fence.DeliveryVersion != initial.DeliveryVersion+2 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if got := runtime.versions(); len(got) != 2 || got[0] != initial.DeliveryVersion || got[1] != initial.DeliveryVersion+1 {
		t.Fatalf("runtime request versions = %v", got)
	}
}

func TestDeliveryLeaseSessionStopsMutationsAfterStaleOrCommitted(t *testing.T) {
	initial := domain.DeliveryFence{
		DeliveryID: runtimeID(1), DispatchNo: 1, AttemptID: runtimeID(2),
		AttemptNo: 1, Owner: "worker-a", DeliveryVersion: 4,
	}
	for _, test := range []struct {
		name        string
		disposition DeliveryMutationDisposition
		want        DeliveryLeaseDisposition
	}{
		{name: "stale", disposition: DeliveryMutationStale, want: DeliveryLeaseStale},
		{name: "committed", disposition: DeliveryMutationCommitted, want: DeliveryLeaseCommitted},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := &leaseSessionRuntime{nextDisposition: test.disposition}
			session, err := NewDeliveryLeaseSession(runtime, initial)
			if err != nil {
				t.Fatal(err)
			}
			result, err := session.Heartbeat(context.Background(), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if result.Disposition != test.want || result.Fence != (domain.DeliveryFence{}) {
				t.Fatalf("result = %#v", result)
			}
			if _, err := session.Checkpoint(context.Background(), domain.DeliveryCheckpoint{
				Stage: domain.DeliveryCheckpointSourceCaptured, SourceVersionID: runtimeID(3),
			}); err != nil {
				t.Fatal(err)
			}
			if runtime.calls != 1 {
				t.Fatalf("runtime calls = %d", runtime.calls)
			}
		})
	}
}

func TestDeliveryLeaseSessionSerializesFailureAndCommitsSession(t *testing.T) {
	initial := domain.DeliveryFence{
		DeliveryID: runtimeID(1), DispatchNo: 1, AttemptID: runtimeID(2),
		AttemptNo: 1, Owner: "worker-a", DeliveryVersion: 4,
	}
	runtime := &leaseSessionRuntime{nextDisposition: DeliveryMutationCommitted}
	session, err := NewDeliveryLeaseSession(runtime, initial)
	if err != nil {
		t.Fatal(err)
	}
	failure := domain.DeliveryFailure{
		Class: domain.DeliveryFailureRetryable, ErrorKind: foundation.ErrorRetryableFailure,
		Code: "REINDEX_TEMPORARY_FAILURE", Summary: "temporary processor failure",
	}
	result, err := session.Fail(context.Background(), failure, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != DeliveryLeaseCommitted || result.Fence != (domain.DeliveryFence{}) {
		t.Fatalf("failure result = %#v", result)
	}
	if _, err := session.Heartbeat(context.Background(), time.Minute); err != nil {
		t.Fatal(err)
	}
	if runtime.calls != 1 {
		t.Fatalf("runtime calls = %d", runtime.calls)
	}
}

func TestNewDeliveryLeaseSessionRejectsIncompleteDependencies(t *testing.T) {
	validFence := domain.DeliveryFence{
		DeliveryID: runtimeID(1), DispatchNo: 1, AttemptID: runtimeID(2),
		AttemptNo: 1, Owner: "worker-a", DeliveryVersion: 4,
	}
	if _, err := NewDeliveryLeaseSession(nil, validFence); leaseSessionErrorCode(err) != "REINDEX_DELIVERY_LEASE_SESSION_UNAVAILABLE" {
		t.Fatalf("nil runtime error = %v", err)
	}
	if _, err := NewDeliveryLeaseSession(&leaseSessionRuntime{}, domain.DeliveryFence{}); leaseSessionErrorCode(err) != "REINDEX_DELIVERY_LEASE_SESSION_INVALID" {
		t.Fatalf("invalid fence error = %v", err)
	}
}

func leaseSessionErrorCode(err error) string {
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		return ""
	}
	return classified.Code
}

type leaseSessionRuntime struct {
	mu              sync.Mutex
	calls           int
	concurrent      int
	maxConcurrent   int
	requestVersions []int64
	nextDisposition DeliveryMutationDisposition
}

func (runtime *leaseSessionRuntime) Heartbeat(_ context.Context, command DeliveryHeartbeatCommand) (DeliveryMutationResult, error) {
	return runtime.mutate(command.Fence)
}

func (runtime *leaseSessionRuntime) Checkpoint(_ context.Context, command DeliveryCheckpointCommand) (DeliveryMutationResult, error) {
	return runtime.mutate(command.Fence)
}

func (runtime *leaseSessionRuntime) Fail(_ context.Context, command DeliveryFailureCommand) (DeliveryMutationResult, error) {
	return runtime.mutate(command.Fence)
}

func (runtime *leaseSessionRuntime) mutate(fence domain.DeliveryFence) (DeliveryMutationResult, error) {
	runtime.mu.Lock()
	runtime.calls++
	runtime.concurrent++
	if runtime.concurrent > runtime.maxConcurrent {
		runtime.maxConcurrent = runtime.concurrent
	}
	runtime.requestVersions = append(runtime.requestVersions, fence.DeliveryVersion)
	disposition := runtime.nextDisposition
	runtime.nextDisposition = ""
	runtime.mu.Unlock()
	time.Sleep(time.Millisecond)
	runtime.mu.Lock()
	runtime.concurrent--
	runtime.mu.Unlock()
	if disposition == DeliveryMutationStale || disposition == DeliveryMutationCommitted {
		return DeliveryMutationResult{Disposition: disposition}, nil
	}
	fence.DeliveryVersion++
	return DeliveryMutationResult{Disposition: DeliveryMutationApplied, Fence: fence}, nil
}

func (runtime *leaseSessionRuntime) versions() []int64 {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return append([]int64(nil), runtime.requestVersions...)
}
