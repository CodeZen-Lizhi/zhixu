package checkpoint

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestRunnerInterruptResumeDeletesEncryptedCheckpoint(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC))
	keyer := mustKeyer(t)
	store := mustStore(t, clock)
	runner, err := NewRunner(keyer, store)
	if err != nil {
		t.Fatal(err)
	}
	scope := validScope()
	secret := "checkpoint-sensitive-prompt-7e9547c8"

	interrupt, err := runner.Start(context.Background(), scope, secret)
	if err != nil {
		t.Fatal(err)
	}
	if interrupt.ID == "" || !validCheckpointID(interrupt.CheckpointID) {
		t.Fatalf("interrupt=%#v", interrupt)
	}
	raw := rawCheckpoint(t, store, interrupt.CheckpointID)
	if bytes.Contains(raw, []byte(secret)) || bytes.Contains(raw, []byte(scope.AttemptID)) {
		t.Fatal("encrypted checkpoint contains sensitive plaintext")
	}

	output, err := runner.Resume(context.Background(), scope, interrupt.ID, Approval{Approved: true})
	if err != nil || output != secret {
		t.Fatalf("output=%q err=%v", output, err)
	}
	if _, exists, err := store.Get(context.Background(), interrupt.CheckpointID); err != nil || exists {
		t.Fatalf("completed checkpoint exists=%t err=%v", exists, err)
	}
}

func TestRunnerRejectsDifferentAttemptAndFence(t *testing.T) {
	clock := newFakeClock(time.Now())
	keyer := mustKeyer(t)
	store := mustStore(t, clock)
	runner, err := NewRunner(keyer, store)
	if err != nil {
		t.Fatal(err)
	}
	scope := validScope()
	interrupt, err := runner.Start(context.Background(), scope, "approved payload")
	if err != nil {
		t.Fatal(err)
	}

	changedAttempt := scope
	changedAttempt.AttemptID = "attempt-2"
	if _, err := runner.Resume(context.Background(), changedAttempt, interrupt.ID, Approval{Approved: true}); !errors.Is(err, ErrCheckpointUnavailable) {
		t.Fatalf("changed Attempt error=%v", err)
	}
	changedFence := scope
	changedFence.Fence = "fence-2"
	if _, err := runner.Resume(context.Background(), changedFence, interrupt.ID, Approval{Approved: true}); !errors.Is(err, ErrCheckpointUnavailable) {
		t.Fatalf("changed Fence error=%v", err)
	}
	if output, err := runner.Resume(context.Background(), scope, interrupt.ID, Approval{Approved: true}); err != nil || output != "approved payload" {
		t.Fatalf("original scope output=%q err=%v", output, err)
	}
}

func TestRunnerRejectsWrongInterruptWithoutLosingCheckpoint(t *testing.T) {
	clock := newFakeClock(time.Now())
	keyer := mustKeyer(t)
	store := mustStore(t, clock)
	runner, err := NewRunner(keyer, store)
	if err != nil {
		t.Fatal(err)
	}
	scope := validScope()
	interrupt, err := runner.Start(context.Background(), scope, "approved payload")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := runner.Resume(context.Background(), scope, "wrong-interrupt", Approval{Approved: true}); err == nil {
		t.Fatal("wrong interrupt ID resumed checkpoint")
	}
	if output, err := runner.Resume(context.Background(), scope, interrupt.ID, Approval{Approved: true}); err != nil || output != "approved payload" {
		t.Fatalf("original interrupt output=%q err=%v", output, err)
	}
}

func TestRunnerDoesNotOverwriteOrResumeExpiredCheckpoint(t *testing.T) {
	clock := newFakeClock(time.Now())
	keyer := mustKeyer(t)
	store := mustStore(t, clock)
	runner, err := NewRunner(keyer, store)
	if err != nil {
		t.Fatal(err)
	}
	scope := validScope()
	interrupt, err := runner.Start(context.Background(), scope, "original payload")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Start(context.Background(), scope, "replacement payload"); !errors.Is(err, ErrCheckpointExists) {
		t.Fatalf("duplicate start error=%v", err)
	}
	clock.Advance(2 * time.Minute)
	if _, err := runner.Resume(context.Background(), scope, interrupt.ID, Approval{Approved: true}); !errors.Is(err, ErrCheckpointUnavailable) {
		t.Fatalf("expired resume error=%v", err)
	}
	if rawCheckpointOrNil(store, interrupt.CheckpointID) != nil {
		t.Fatal("expired Runner checkpoint was not deleted")
	}
}

func TestRunnerCancellationDoesNotCreateCheckpoint(t *testing.T) {
	clock := newFakeClock(time.Now())
	keyer := mustKeyer(t)
	store := mustStore(t, clock)
	runner, err := NewRunner(keyer, store)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runner.Start(ctx, validScope(), "payload"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled start error=%v", err)
	}
	id, _ := keyer.ID(validScope())
	if rawCheckpointOrNil(store, id) != nil {
		t.Fatal("canceled start persisted a checkpoint")
	}

	store.lifecycle <- struct{}{}
	waitingCtx, cancelWaiting := context.WithCancel(context.Background())
	waitResult := make(chan error, 1)
	go func() {
		waitResult <- runner.acquire(waitingCtx)
	}()
	cancelWaiting()
	select {
	case err := <-waitResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled lifecycle wait error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled lifecycle wait did not return")
	}
	runner.release()
}

func TestRunnerSerializesConcurrentCheckpointLifecycle(t *testing.T) {
	clock := newFakeClock(time.Now())
	keyer := mustKeyer(t)
	store := mustStore(t, clock)
	runner, err := NewRunner(keyer, store)
	if err != nil {
		t.Fatal(err)
	}
	secondRunner, err := NewRunner(keyer, store)
	if err != nil {
		t.Fatal(err)
	}
	scope := validScope()

	start := make(chan struct{})
	type startResult struct {
		interrupt Interrupt
		err       error
	}
	startResults := make(chan startResult, 2)
	for index := range 2 {
		currentRunner := runner
		if index == 1 {
			currentRunner = secondRunner
		}
		go func() {
			<-start
			interrupt, startErr := currentRunner.Start(context.Background(), scope, "concurrent payload")
			startResults <- startResult{interrupt: interrupt, err: startErr}
		}()
	}
	close(start)
	var started int
	var interrupt Interrupt
	for range 2 {
		result := <-startResults
		switch {
		case result.err == nil:
			started++
			interrupt = result.interrupt
		case errors.Is(result.err, ErrCheckpointExists):
		default:
			t.Fatalf("concurrent Start error=%v", result.err)
		}
	}
	if started != 1 {
		t.Fatalf("successful concurrent Starts=%d, want 1", started)
	}

	resume := make(chan struct{})
	type resumeResult struct {
		output string
		err    error
	}
	resumeResults := make(chan resumeResult, 2)
	for index := range 2 {
		currentRunner := runner
		if index == 1 {
			currentRunner = secondRunner
		}
		go func() {
			<-resume
			output, resumeErr := currentRunner.Resume(context.Background(), scope, interrupt.ID, Approval{Approved: true})
			resumeResults <- resumeResult{output: output, err: resumeErr}
		}()
	}
	close(resume)
	var resumed int
	for range 2 {
		result := <-resumeResults
		switch {
		case result.err == nil && result.output == "concurrent payload":
			resumed++
		case errors.Is(result.err, ErrCheckpointUnavailable):
		default:
			t.Fatalf("concurrent Resume output=%q error=%v", result.output, result.err)
		}
	}
	if resumed != 1 {
		t.Fatalf("successful concurrent Resumes=%d, want 1", resumed)
	}
}

func TestStoreExpiresAndRejectsTamperingOrDifferentAAD(t *testing.T) {
	clock := newFakeClock(time.Now())
	keyer := mustKeyer(t)
	store := mustStore(t, clock)
	id, _ := keyer.ID(validScope())
	if err := store.Set(context.Background(), id, []byte("short-lived secret")); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Minute)
	if _, exists, err := store.Get(context.Background(), id); err != nil || exists {
		t.Fatalf("expired checkpoint exists=%t err=%v", exists, err)
	}
	if rawCheckpointOrNil(store, id) != nil {
		t.Fatal("expired checkpoint was not deleted")
	}

	if err := store.Set(context.Background(), id, []byte("tamper target")); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	entry := store.entries[id]
	entry.sealed[len(entry.sealed)-1] ^= 0xff
	store.entries[id] = entry
	store.mu.Unlock()
	if _, _, err := store.Get(context.Background(), id); !errors.Is(err, ErrCheckpointCorrupt) {
		t.Fatalf("tamper error=%v", err)
	}

	store = mustStore(t, clock)
	if err := store.Set(context.Background(), id, []byte("AAD target")); err != nil {
		t.Fatal(err)
	}
	otherScope := validScope()
	otherScope.Fence = "fence-other"
	otherID, _ := keyer.ID(otherScope)
	store.mu.Lock()
	store.entries[otherID] = store.entries[id]
	store.mu.Unlock()
	if _, _, err := store.Get(context.Background(), otherID); !errors.Is(err, ErrCheckpointCorrupt) {
		t.Fatalf("AAD error=%v", err)
	}
}

func TestStoreHonorsContextAndConcurrentIsolation(t *testing.T) {
	clock := newFakeClock(time.Now())
	keyer := mustKeyer(t)
	store := mustStore(t, clock)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	id, _ := keyer.ID(validScope())
	if err := store.Set(canceled, id, []byte("payload")); !errors.Is(err, context.Canceled) {
		t.Fatalf("set cancellation=%v", err)
	}
	if _, _, err := store.Get(canceled, id); !errors.Is(err, context.Canceled) {
		t.Fatalf("get cancellation=%v", err)
	}
	if err := store.Delete(canceled, id); !errors.Is(err, context.Canceled) {
		t.Fatalf("delete cancellation=%v", err)
	}

	const workers = 64
	var wait sync.WaitGroup
	errorsByWorker := make(chan error, workers)
	for worker := 0; worker < workers; worker++ {
		worker := worker
		wait.Add(1)
		go func() {
			defer wait.Done()
			scope := validScope()
			scope.AttemptID = fmt.Sprintf("attempt-%d", worker)
			checkpointID, err := keyer.ID(scope)
			if err != nil {
				errorsByWorker <- err
				return
			}
			payload := []byte(fmt.Sprintf("payload-%d", worker))
			if err := store.Set(context.Background(), checkpointID, payload); err != nil {
				errorsByWorker <- err
				return
			}
			actual, exists, err := store.Get(context.Background(), checkpointID)
			if err != nil || !exists || !bytes.Equal(actual, payload) {
				errorsByWorker <- fmt.Errorf("worker %d read exists=%t value=%q err=%v", worker, exists, actual, err)
				return
			}
			if err := store.Delete(context.Background(), checkpointID); err != nil {
				errorsByWorker <- err
			}
		}()
	}
	wait.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		t.Error(err)
	}
}

func TestScopeIDsAreOpaqueAndFenceBound(t *testing.T) {
	keyer := mustKeyer(t)
	scope := validScope()
	first, err := keyer.ID(scope)
	if err != nil {
		t.Fatal(err)
	}
	changed := scope
	changed.Fence = "fence-2"
	second, err := keyer.ID(changed)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || bytes.Contains([]byte(first), []byte(scope.WorkflowRunID)) || bytes.Contains([]byte(first), []byte(scope.AttemptID)) {
		t.Fatalf("checkpoint IDs are not opaque and fence-bound: %q %q", first, second)
	}
	invalid := scope
	invalid.AttemptID = " attempt-1"
	if _, err := keyer.ID(invalid); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("invalid scope error=%v", err)
	}
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(now time.Time) *fakeClock { return &fakeClock{now: now.UTC()} }

func (clock *fakeClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *fakeClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.mu.Unlock()
}

func mustKeyer(t *testing.T) *Keyer {
	t.Helper()
	keyer, err := NewKeyer(bytes.Repeat([]byte{0x41}, keyBytes))
	if err != nil {
		t.Fatal(err)
	}
	return keyer
}

func mustStore(t *testing.T, clock *fakeClock) *Store {
	t.Helper()
	store, err := newStore(bytes.Repeat([]byte{0x52}, keyBytes), time.Minute, clock.Now, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func validScope() AttemptScope {
	return AttemptScope{WorkflowRunID: "workflow-1", NodeRunID: "node-1", AttemptID: "attempt-1", Fence: "fence-1"}
}

func rawCheckpoint(t *testing.T, store *Store, checkpointID string) []byte {
	t.Helper()
	raw := rawCheckpointOrNil(store, checkpointID)
	if raw == nil {
		t.Fatal("checkpoint was not persisted")
	}
	return raw
}

func rawCheckpointOrNil(store *Store, checkpointID string) []byte {
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, exists := store.entries[checkpointID]
	if !exists {
		return nil
	}
	return append([]byte(nil), entry.sealed...)
}
