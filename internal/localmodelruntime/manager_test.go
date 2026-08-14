package localmodelruntime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagerConcurrentEnsureStartsOneChild(t *testing.T) {
	runner := &fakeChildRunner{}
	manager := newTestManager(t, runner, time.Second)

	const callers = 32
	var group sync.WaitGroup
	group.Add(callers)
	errorsFound := make(chan error, callers)
	start := make(chan struct{})
	for range callers {
		go func() {
			defer group.Done()
			<-start
			errorsFound <- manager.Ensure(context.Background())
		}()
	}
	close(start)
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatalf("Ensure() error = %v", err)
		}
	}
	if runner.startCalls.Load() != 1 {
		t.Fatalf("start calls = %d, want 1", runner.startCalls.Load())
	}
	child := runner.child(0)
	if manager.Snapshot().State != StateRunning || manager.Snapshot().Generation != 1 {
		t.Fatalf("snapshot = %+v", manager.Snapshot())
	}
	child.exit(nil)
	waitForState(t, manager, StateStopped)
	if child.waitCalls.Load() != 1 {
		t.Fatalf("Wait() calls = %d, want 1", child.waitCalls.Load())
	}
}

func TestManagerSnapshotDoesNotBlockWhileChildStarts(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	runner := &blockingChildRunner{started: started, release: release, child: newFakeChild(1, true)}
	manager := newTestManager(t, runner, time.Second)
	ensureResult := make(chan error, 1)
	go func() { ensureResult <- manager.Ensure(context.Background()) }()
	<-started

	snapshotResult := make(chan Snapshot, 1)
	go func() { snapshotResult <- manager.Snapshot() }()
	select {
	case snapshot := <-snapshotResult:
		if snapshot.State != StateStarting {
			t.Fatalf("snapshot = %+v", snapshot)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Snapshot() blocked while child runner was starting")
	}
	close(release)
	if err := <-ensureResult; err != nil {
		t.Fatal(err)
	}
	runner.child.exit(nil)
}

func TestManagerStopUsesTermThenProcessGroupKillAndWaitsOnce(t *testing.T) {
	runner := &fakeChildRunner{newChild: func(index int) *fakeChild {
		return newFakeChild(index+1, false)
	}}
	manager := newTestManager(t, runner, 10*time.Millisecond)
	if err := manager.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	child := runner.child(0)
	child.exitOnKill = true

	if err := manager.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if child.termCalls.Load() != 1 || child.killCalls.Load() != 1 || child.waitCalls.Load() != 1 {
		t.Fatalf("term=%d kill=%d wait=%d", child.termCalls.Load(), child.killCalls.Load(), child.waitCalls.Load())
	}
	if got := manager.Snapshot(); got.State != StateStopped || got.Failed {
		t.Fatalf("snapshot = %+v", got)
	}
	if err := manager.Stop(context.Background()); err != nil {
		t.Fatalf("repeated Stop() error = %v", err)
	}
	if child.termCalls.Load() != 1 || child.killCalls.Load() != 1 || child.waitCalls.Load() != 1 {
		t.Fatalf("repeated stop changed calls: term=%d kill=%d wait=%d", child.termCalls.Load(), child.killCalls.Load(), child.waitCalls.Load())
	}
}

func TestManagerCancelledStopStillKillsAndReapsChild(t *testing.T) {
	runner := &fakeChildRunner{newChild: func(index int) *fakeChild {
		child := newFakeChild(index+1, false)
		child.exitOnKill = true
		return child
	}}
	manager := newTestManager(t, runner, time.Hour)
	if err := manager.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	child := runner.child(0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := manager.Stop(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop() error = %v, want context.Canceled", err)
	}
	if child.termCalls.Load() != 1 || child.killCalls.Load() != 1 || child.waitCalls.Load() != 1 {
		t.Fatalf("term=%d kill=%d wait=%d", child.termCalls.Load(), child.killCalls.Load(), child.waitCalls.Load())
	}
	if manager.Snapshot().State != StateStopped {
		t.Fatalf("snapshot = %+v", manager.Snapshot())
	}
}

func TestManagerStopReturnsWhenProcessGroupKillFails(t *testing.T) {
	killErr := errors.New("fixture kill failed")
	runner := &fakeChildRunner{newChild: func(index int) *fakeChild {
		child := newFakeChild(index+1, false)
		child.killErr = killErr
		return child
	}}
	manager := newTestManager(t, runner, time.Hour)
	if err := manager.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := make(chan error, 1)
	go func() { result <- manager.Stop(ctx) }()
	select {
	case err := <-result:
		if !errors.Is(err, killErr) || !errors.Is(err, context.Canceled) {
			t.Fatalf("Stop() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Stop() blocked after process-group kill failed")
	}
	runner.child(0).exit(nil)
}

func TestManagerUnexpectedCrashAllowsFreshEnsure(t *testing.T) {
	runner := &fakeChildRunner{}
	manager := newTestManager(t, runner, time.Second)
	if err := manager.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := runner.child(0)
	crashErr := errors.New("fixture crash")
	first.exit(crashErr)
	waitForState(t, manager, StateStopped)
	if !errors.Is(manager.LastError(), crashErr) {
		t.Fatalf("last error = %v", manager.LastError())
	}

	if err := manager.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure() after crash error = %v", err)
	}
	if runner.startCalls.Load() != 2 || manager.Snapshot().Generation != 2 {
		t.Fatalf("start calls=%d snapshot=%+v", runner.startCalls.Load(), manager.Snapshot())
	}
	runner.child(1).exit(nil)
}

func TestManagerGenerationFloorSurvivesSupervisorRestart(t *testing.T) {
	runner := &fakeChildRunner{}
	manager := newTestManager(t, runner, time.Second)
	if err := manager.SetGenerationFloor(41); err != nil {
		t.Fatal(err)
	}
	if err := manager.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := manager.Snapshot().Generation; got != 42 {
		t.Fatalf("first generation = %d, want 42", got)
	}
	runner.child(0).exit(nil)
	waitForState(t, manager, StateStopped)
	if err := manager.SetGenerationFloor(4); err != nil {
		t.Fatal(err)
	}
	if err := manager.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := manager.Snapshot().Generation; got != 43 {
		t.Fatalf("second generation = %d, want 43", got)
	}
	runner.child(1).exit(nil)
}

func TestManagerUnexpectedCleanExitIsStillFailure(t *testing.T) {
	runner := &fakeChildRunner{}
	manager := newTestManager(t, runner, time.Second)
	if err := manager.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	runner.child(0).exit(nil)
	waitForState(t, manager, StateStopped)
	if !manager.Snapshot().Failed || manager.LastError() == nil {
		t.Fatalf("snapshot=%+v last error=%v", manager.Snapshot(), manager.LastError())
	}
}

func TestManagerCloseRejectsLaterEnsure(t *testing.T) {
	runner := &fakeChildRunner{}
	manager := newTestManager(t, runner, time.Second)
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.Ensure(context.Background()); !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("Ensure() error = %v, want ErrManagerClosed", err)
	}
}

func TestManagerStopCancelsInferenceBoundToChildGeneration(t *testing.T) {
	runner := &fakeChildRunner{}
	manager := newTestManager(t, runner, time.Second)
	if err := manager.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if manager.Snapshot().Ready {
		t.Fatal("new child was exposed before readiness verification")
	}
	if err := manager.SetInferenceReady(true); err != nil {
		t.Fatal(err)
	}
	inferenceContext, generation, release, err := manager.acquireInference(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if generation != 1 || !manager.generationAvailable(generation) {
		t.Fatalf("generation=%d snapshot=%+v", generation, manager.Snapshot())
	}
	if err := manager.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-inferenceContext.Done():
	case <-time.After(time.Second):
		t.Fatal("inference context was not cancelled by Stop")
	}
	if manager.generationAvailable(generation) {
		t.Fatal("stopped child generation remains available")
	}
}

func TestManagerReadinessCanRevokeLiveInference(t *testing.T) {
	runner := &fakeChildRunner{}
	manager := newTestManager(t, runner, time.Second)
	if err := manager.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := manager.acquireInference(context.Background()); !errors.Is(err, ErrChildUnavailable) {
		t.Fatalf("acquire before ready error = %v", err)
	}
	if err := manager.SetInferenceReady(true); err != nil {
		t.Fatal(err)
	}
	inferenceContext, _, release, err := manager.acquireInference(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := manager.SetInferenceReady(false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-inferenceContext.Done():
	case <-time.After(time.Second):
		t.Fatal("readiness revocation did not cancel in-flight inference")
	}
	if manager.Snapshot().Ready {
		t.Fatal("readiness revocation was not published")
	}
	runner.child(0).exit(nil)
}

func newTestManager(t *testing.T, runner ChildRunner, grace time.Duration) *Manager {
	t.Helper()
	manager, err := NewManager(runner, grace)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func waitForState(t *testing.T, manager *Manager, state State) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if manager.Snapshot().State == state {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("state = %q, want %q", manager.Snapshot().State, state)
}

type fakeChildRunner struct {
	startCalls atomic.Int32
	mu         sync.Mutex
	children   []*fakeChild
	newChild   func(int) *fakeChild
}

type blockingChildRunner struct {
	started chan struct{}
	release chan struct{}
	child   *fakeChild
}

func (runner *blockingChildRunner) Start(context.Context) (Child, error) {
	close(runner.started)
	<-runner.release
	return runner.child, nil
}

func (runner *fakeChildRunner) Start(context.Context) (Child, error) {
	index := int(runner.startCalls.Add(1)) - 1
	child := newFakeChild(index+1, true)
	if runner.newChild != nil {
		child = runner.newChild(index)
	}
	runner.mu.Lock()
	runner.children = append(runner.children, child)
	runner.mu.Unlock()
	return child, nil
}

func (runner *fakeChildRunner) child(index int) *fakeChild {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.children[index]
}

type fakeChild struct {
	pid        int
	exited     chan struct{}
	exitOnce   sync.Once
	exitErr    error
	exitOnTerm bool
	exitOnKill bool
	killErr    error
	termCalls  atomic.Int32
	killCalls  atomic.Int32
	waitCalls  atomic.Int32
}

func newFakeChild(pid int, exitOnTerm bool) *fakeChild {
	return &fakeChild{pid: pid, exited: make(chan struct{}), exitOnTerm: exitOnTerm}
}

func (child *fakeChild) PID() int { return child.pid }

func (child *fakeChild) SignalTerm() error {
	child.termCalls.Add(1)
	if child.exitOnTerm {
		child.exit(nil)
	}
	return nil
}

func (child *fakeChild) KillProcessGroup() error {
	child.killCalls.Add(1)
	if child.killErr != nil {
		return child.killErr
	}
	if child.exitOnKill {
		child.exit(errors.New("fixture killed"))
	}
	return nil
}

func (child *fakeChild) Wait() error {
	child.waitCalls.Add(1)
	<-child.exited
	return child.exitErr
}

func (child *fakeChild) exit(err error) {
	child.exitOnce.Do(func() {
		child.exitErr = err
		close(child.exited)
	})
}
