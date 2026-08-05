package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
)

type workerOutboxStore struct {
	lease       domain.OutboxLease
	poisoned    int
	rescheduled int
	delay       time.Duration
}

func (store *workerOutboxStore) ClaimNext(context.Context, string, time.Duration) (domain.OutboxLease, bool, error) {
	return store.lease, true, nil
}

func (*workerOutboxStore) MarkPublished(context.Context, domain.OutboxLease) error { return nil }

func (store *workerOutboxStore) Reschedule(_ context.Context, _ domain.OutboxLease, _ string, delay time.Duration) error {
	store.rescheduled++
	store.delay = delay
	return nil
}

func (store *workerOutboxStore) Poison(context.Context, domain.OutboxLease, string) error {
	store.poisoned++
	return nil
}

type failingIDGenerator struct{ err error }

func (generator failingIDGenerator) New() (foundation.ID, error) { return "", generator.err }

func TestWorkerPoisonsRetryableDeliveryAtAttemptLimit(t *testing.T) {
	processErr := foundation.NewError(foundation.ErrorRetryableFailure, domain.ErrorCodeOffline, true, errors.New("offline"))
	store := &workerOutboxStore{lease: workerLease(maxOutboxAttempts)}
	worker := workerWithIDError(t, store, processErr)
	worked, err := worker.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("worked=%t err=%v", worked, err)
	}
	if store.poisoned != 1 || store.rescheduled != 0 {
		t.Fatalf("poisoned=%d rescheduled=%d", store.poisoned, store.rescheduled)
	}
}

func TestWorkerLeavesCanceledDeliveryForLeaseRecovery(t *testing.T) {
	store := &workerOutboxStore{lease: workerLease(1)}
	worker := workerWithIDError(t, store, context.Canceled)
	worked, err := worker.RunOnce(context.Background())
	if !worked || !errors.Is(err, context.Canceled) {
		t.Fatalf("worked=%t err=%v", worked, err)
	}
	if store.poisoned != 0 || store.rescheduled != 0 {
		t.Fatalf("canceled delivery was mutated: poisoned=%d rescheduled=%d", store.poisoned, store.rescheduled)
	}
}

func TestWorkerUsesBoundedExponentialRetry(t *testing.T) {
	processErr := foundation.NewError(foundation.ErrorRetryableFailure, domain.ErrorCodeOffline, true, errors.New("offline"))
	store := &workerOutboxStore{lease: workerLease(8)}
	worker := workerWithIDError(t, store, processErr)
	if worked, err := worker.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("worked=%t err=%v", worked, err)
	}
	if store.rescheduled != 1 || store.poisoned != 0 || store.delay != maxOutboxRetryDelay {
		t.Fatalf("rescheduled=%d poisoned=%d delay=%s", store.rescheduled, store.poisoned, store.delay)
	}
}

func workerWithIDError(t *testing.T, outbox OutboxStore, processErr error) *Worker {
	t.Helper()
	runExecutor := &RunExecutor{
		configs: configStoreStub{}, runs: newExecutorStore(domain.RunPending), remote: &remoteStub{},
		locker: immediateGitLocker{}, ids: failingIDGenerator{err: processErr}, lease: time.Minute,
	}
	worker, err := NewWorker(outbox, runExecutor, &FollowupExecutor{}, "worker-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func workerLease(attemptCount int) domain.OutboxLease {
	return domain.OutboxLease{
		ID: executorAttemptID, WorkspaceID: executorWorkspaceID, RunID: executorRunID,
		Kind: domain.OutboxExecuteRun, Owner: "worker-1", AttemptCount: attemptCount,
		Version: 2, LeaseUntil: time.Now().Add(time.Minute),
	}
}
