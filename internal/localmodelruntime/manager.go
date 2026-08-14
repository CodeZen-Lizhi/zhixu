// Package localmodelruntime owns the single on-demand Ollama child process and
// the narrow inference proxy exposed by the local model runtime container.
package localmodelruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// State is the manager's in-process view of the Ollama child lifecycle.
type State string

const (
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
)

var (
	// ErrManagerClosed reports that the manager has completed its final stop.
	ErrManagerClosed = errors.New("local model runtime manager is closed")
	// ErrChildUnavailable reports that no live Ollama child can serve inference.
	ErrChildUnavailable = errors.New("local model runtime child is unavailable")
)

// Child is one started Ollama process. Wait must be called exactly once by the
// manager; SignalTerm and KillProcessGroup must not reap the process.
type Child interface {
	PID() int
	SignalTerm() error
	KillProcessGroup() error
	Wait() error
}

// ChildRunner starts the fixed Ollama child implementation.
type ChildRunner interface {
	// Start may perform blocking process creation, so callers serialize it.
	Start(context.Context) (Child, error)
}

// ChildController is the reconciler's narrow process and readiness seam.
// Manager implements it; tests can substitute an in-memory controller without
// constructing operating-system processes.
type ChildController interface {
	SetGenerationFloor(uint64) error
	Ensure(context.Context) error
	Stop(context.Context) error
	Snapshot() Snapshot
	SetInferenceReady(bool) error
}

var _ ChildController = (*Manager)(nil)

// Snapshot describes manager state without exposing process identifiers.
type Snapshot struct {
	State      State
	Generation uint64
	Ready      bool
	Failed     bool
}

// Manager serializes Ensure and Stop around one child process.
type Manager struct {
	opMu sync.Mutex
	mu   sync.Mutex

	runner    ChildRunner
	stopGrace time.Duration
	state     State
	child     *managedChild
	closed    bool
	nextGen   uint64
	ready     bool
	lastErr   error
}

type managedChild struct {
	generation       uint64
	process          Child
	done             chan struct{}
	nextInference    uint64
	inferenceCancels map[uint64]context.CancelCauseFunc
}

// NewManager constructs a manager that owns at most one child at a time.
func NewManager(runner ChildRunner, stopGrace time.Duration) (*Manager, error) {
	if runner == nil {
		return nil, errors.New("local model runtime child runner is required")
	}
	if stopGrace <= 0 {
		return nil, errors.New("local model runtime stop grace must be positive")
	}
	return &Manager{runner: runner, stopGrace: stopGrace, state: StateStopped}, nil
}

// SetGenerationFloor restores the last durable child generation before a new
// child starts. The floor can only advance while no child exists.
func (manager *Manager) SetGenerationFloor(floor uint64) error {
	if manager == nil {
		return ErrManagerClosed
	}
	if floor >= uint64(^uint64(0)>>1) {
		return errors.New("local model runtime child generation is exhausted")
	}

	manager.opMu.Lock()
	defer manager.opMu.Unlock()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return ErrManagerClosed
	}
	if manager.state != StateStopped || manager.child != nil {
		return errors.New("local model runtime generation floor requires a stopped child")
	}
	if floor > manager.nextGen {
		manager.nextGen = floor
	}
	return nil
}

// Ensure starts the Ollama child when absent. Concurrent and repeated calls
// converge on the same child generation.
func (manager *Manager) Ensure(ctx context.Context) error {
	if manager == nil {
		return ErrManagerClosed
	}
	if ctx == nil {
		return errors.New("local model runtime ensure context is required")
	}

	manager.opMu.Lock()
	defer manager.opMu.Unlock()

	for {
		manager.mu.Lock()
		if manager.closed {
			manager.mu.Unlock()
			return ErrManagerClosed
		}
		child := manager.child
		state := manager.state
		if child != nil && state != StateStopping {
			manager.mu.Unlock()
			return nil
		}
		if child != nil {
			done := child.done
			manager.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if err := ctx.Err(); err != nil {
			manager.mu.Unlock()
			return err
		}
		if manager.nextGen >= uint64(^uint64(0)>>1) {
			manager.mu.Unlock()
			return errors.New("local model runtime child generation is exhausted")
		}

		manager.state = StateStarting
		manager.mu.Unlock()
		process, err := manager.runner.Start(ctx)
		if err != nil {
			manager.mu.Lock()
			manager.state = StateStopped
			startErr := fmt.Errorf("start local model runtime child: %w", err)
			manager.lastErr = startErr
			manager.mu.Unlock()
			return startErr
		}
		if process == nil || process.PID() <= 0 {
			if process != nil {
				_ = process.SignalTerm()
				_ = process.KillProcessGroup()
				_ = process.Wait()
			}
			manager.mu.Lock()
			manager.state = StateStopped
			invalidErr := errors.New("local model runtime child runner returned an invalid process")
			manager.lastErr = invalidErr
			manager.mu.Unlock()
			return invalidErr
		}

		manager.mu.Lock()
		manager.nextGen++
		child = &managedChild{
			generation:       manager.nextGen,
			process:          process,
			done:             make(chan struct{}),
			inferenceCancels: make(map[uint64]context.CancelCauseFunc),
		}
		manager.child = child
		manager.state = StateRunning
		manager.ready = false
		manager.lastErr = nil
		manager.mu.Unlock()
		go manager.waitChild(child)
		return nil
	}
}

// Stop terminates the child and waits for it to be reaped. It first sends TERM
// only to the serve PID, then kills the child's process group after the grace.
func (manager *Manager) Stop(ctx context.Context) error {
	if manager == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("local model runtime stop context is required")
	}

	manager.opMu.Lock()
	defer manager.opMu.Unlock()
	return manager.stop(ctx)
}

func (manager *Manager) stop(ctx context.Context) error {
	manager.mu.Lock()
	child := manager.child
	if child == nil {
		manager.state = StateStopped
		manager.ready = false
		manager.mu.Unlock()
		return nil
	}
	manager.state = StateStopping
	manager.ready = false
	manager.cancelInferenceLocked(child)
	manager.mu.Unlock()

	termErr := normalizeProcessGone(child.process.SignalTerm())
	if termErr != nil {
		termErr = fmt.Errorf("terminate local model runtime child: %w", termErr)
	}
	if termErr != nil {
		killErr := normalizeProcessGone(child.process.KillProcessGroup())
		if killErr != nil {
			return errors.Join(termErr, wrapKillError(killErr))
		}
		return errors.Join(termErr, manager.waitKilledChild(child, nil))
	}

	grace := time.NewTimer(manager.stopGrace)
	defer grace.Stop()
	select {
	case <-child.done:
		return nil
	case <-ctx.Done():
		killErr := normalizeProcessGone(child.process.KillProcessGroup())
		if killErr != nil {
			return errors.Join(wrapKillError(killErr), ctx.Err())
		}
		return manager.waitKilledChild(child, ctx.Err())
	case <-grace.C:
	}

	killErr := normalizeProcessGone(child.process.KillProcessGroup())
	if killErr != nil {
		killErr = wrapKillError(killErr)
	}
	select {
	case <-child.done:
		return killErr
	case <-ctx.Done():
		if killErr != nil {
			return errors.Join(killErr, ctx.Err())
		}
		return manager.waitKilledChild(child, ctx.Err())
	}
}

func (manager *Manager) waitKilledChild(child *managedChild, cancellationErr error) error {
	waitLimit := manager.stopGrace
	if waitLimit < time.Second {
		waitLimit = time.Second
	}
	timer := time.NewTimer(waitLimit)
	defer timer.Stop()
	select {
	case <-child.done:
		return cancellationErr
	case <-timer.C:
		return errors.Join(cancellationErr, errors.New("local model runtime child did not exit after process-group kill"))
	}
}

// Close performs the manager's final stop and rejects later Ensure calls.
func (manager *Manager) Close(ctx context.Context) error {
	if manager == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("local model runtime close context is required")
	}

	manager.opMu.Lock()
	defer manager.opMu.Unlock()
	manager.mu.Lock()
	manager.closed = true
	manager.mu.Unlock()
	return manager.stop(ctx)
}

// Snapshot returns a race-safe manager state projection.
func (manager *Manager) Snapshot() Snapshot {
	if manager == nil {
		return Snapshot{State: StateStopped, Failed: true}
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	var generation uint64
	if manager.child != nil {
		generation = manager.child.generation
	}
	return Snapshot{State: manager.state, Generation: generation, Ready: manager.ready, Failed: manager.lastErr != nil}
}

// SetInferenceReady publishes whether the current child generation may receive
// inference traffic. A new child stays unavailable until the control loop has
// verified the exact required model set.
func (manager *Manager) SetInferenceReady(ready bool) error {
	if manager == nil {
		return ErrManagerClosed
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return ErrManagerClosed
	}
	if manager.state != StateRunning || manager.child == nil {
		if !ready {
			manager.ready = false
			return nil
		}
		return ErrChildUnavailable
	}
	manager.ready = ready
	if !ready {
		manager.cancelInferenceLocked(manager.child)
	}
	return nil
}

// LastError returns the internal child lifecycle error for the control loop.
// It must not be serialized or returned from the inference HTTP boundary.
func (manager *Manager) LastError() error {
	if manager == nil {
		return ErrManagerClosed
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.lastErr
}

func (manager *Manager) acquireInference(ctx context.Context) (context.Context, uint64, func(), error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.state != StateRunning || manager.child == nil || !manager.ready {
		return nil, 0, nil, ErrChildUnavailable
	}
	child := manager.child
	requestContext, cancelRequest := context.WithCancelCause(ctx)
	child.nextInference++
	inferenceID := child.nextInference
	child.inferenceCancels[inferenceID] = cancelRequest
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			manager.mu.Lock()
			delete(child.inferenceCancels, inferenceID)
			manager.mu.Unlock()
			cancelRequest(context.Canceled)
		})
	}
	return requestContext, child.generation, release, nil
}

func (manager *Manager) generationAvailable(generation uint64) bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.state == StateRunning && manager.ready && manager.child != nil && manager.child.generation == generation
}

func (manager *Manager) waitChild(child *managedChild) {
	waitErr := child.process.Wait()
	manager.mu.Lock()
	manager.cancelInferenceLocked(child)
	if manager.child == child {
		wasStopping := manager.state == StateStopping
		manager.child = nil
		manager.state = StateStopped
		manager.ready = false
		if !wasStopping {
			manager.lastErr = errors.New("local model runtime child exited unexpectedly")
			if waitErr != nil {
				manager.lastErr = fmt.Errorf("local model runtime child exited unexpectedly: %w", waitErr)
			}
		} else {
			manager.lastErr = nil
		}
	}
	close(child.done)
	manager.mu.Unlock()
}

func (manager *Manager) cancelInferenceLocked(child *managedChild) {
	for inferenceID, cancel := range child.inferenceCancels {
		cancel(ErrChildUnavailable)
		delete(child.inferenceCancels, inferenceID)
	}
}

func normalizeProcessGone(err error) error {
	if errors.Is(err, ErrChildUnavailable) {
		return nil
	}
	return err
}

func wrapKillError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("kill local model runtime child process group: %w", err)
}
