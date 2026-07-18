package runtime_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	retrievalruntime "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/runtime"
)

func TestRunnerDispatchesBeforeFirstPollAndUsesConfiguredBatch(t *testing.T) {
	dispatcher := &dispatcherFake{
		results: []dispatchResult{{wait: make(chan struct{})}},
	}
	runner := retrievalruntime.NewRunner(dispatcher, retrievalruntime.Options{
		PollInterval: 500 * time.Millisecond,
		BatchSize:    17,
		ErrorBackoff: 10 * time.Millisecond,
	})
	if err := runner.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	call := receiveCall(t, dispatcher.callsChannel(), 200*time.Millisecond)
	if call.batchSize != 17 {
		t.Fatalf("batch size=%d", call.batchSize)
	}
	close(dispatcher.results[0].wait)
	if err := runner.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if runner.Started() {
		t.Fatal("stopped runner reported started")
	}
}

func TestRunnerPollsAgainAfterSuccessfulDispatch(t *testing.T) {
	dispatcher := &dispatcherFake{}
	runner := retrievalruntime.NewRunner(dispatcher, validOptions())
	if err := runner.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	receiveCall(t, dispatcher.callsChannel(), time.Second)
	receiveCall(t, dispatcher.callsChannel(), time.Second)
	if err := runner.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerBacksOffAndContinuesAfterTransientFailures(t *testing.T) {
	tests := map[string]error{
		"classified retryable": foundation.NewError(foundation.ErrorDependencyUnavailable, "DATABASE_BUSY", true, errors.New("busy")),
		"context cancelled":    context.Canceled,
		"context deadline":     context.DeadlineExceeded,
	}
	for name, transientErr := range tests {
		t.Run(name, func(t *testing.T) {
			dispatcher := &dispatcherFake{results: []dispatchResult{{err: transientErr}}}
			options := validOptions()
			options.PollInterval = time.Second
			options.ErrorBackoff = 30 * time.Millisecond
			runner := retrievalruntime.NewRunner(dispatcher, options)
			if err := runner.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			first := receiveCall(t, dispatcher.callsChannel(), time.Second)
			second := receiveCall(t, dispatcher.callsChannel(), time.Second)
			if elapsed := second.at.Sub(first.at); elapsed < options.ErrorBackoff-5*time.Millisecond {
				t.Fatalf("retry happened after %s, want backoff near %s", elapsed, options.ErrorBackoff)
			}
			select {
			case err := <-runner.Errors():
				t.Fatalf("transient error was reported as fatal: %v", err)
			default:
			}
			if !runner.Started() {
				t.Fatal("runner stopped after transient error")
			}
			if err := runner.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRunnerReportsStableFailureAndStops(t *testing.T) {
	tests := map[string]struct {
		err          error
		fallbackCode string
	}{
		"classified non retryable": {err: foundation.NewError(foundation.ErrorConsistencyViolation, "REINDEX_DISPATCH_INVARIANT_INVALID", false, errors.New("invalid binding"))},
		"unclassified":             {err: errors.New("unknown dispatcher failure"), fallbackCode: "REINDEX_DISPATCHER_RUNNER_FAILED"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			dispatcher := &dispatcherFake{results: []dispatchResult{{err: test.err}}}
			runner := retrievalruntime.NewRunner(dispatcher, validOptions())
			if err := runner.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-runner.Errors():
				if !errors.Is(got, test.err) {
					t.Fatalf("fatal error=%v, want %v", got, test.err)
				}
				if test.fallbackCode != "" {
					assertRunnerError(t, got, foundation.ErrorNonRetryableFailure, test.fallbackCode, false)
				}
			case <-time.After(time.Second):
				t.Fatal("fatal error was not reported")
			}
			waitUntil(t, time.Second, func() bool { return !runner.Started() })
			if calls := dispatcher.callCount(); calls != 1 {
				t.Fatalf("dispatch calls=%d", calls)
			}
			if err := runner.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRunnerStopWaitsWithoutCancellingInflightDispatch(t *testing.T) {
	release := make(chan struct{})
	dispatcher := &dispatcherFake{results: []dispatchResult{{wait: release}}}
	runner := retrievalruntime.NewRunner(dispatcher, validOptions())
	if err := runner.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	call := receiveCall(t, dispatcher.callsChannel(), time.Second)

	stopContext, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := runner.Stop(stopContext)
	assertRunnerError(t, err, foundation.ErrorRetryableFailure, "REINDEX_DISPATCHER_RUNNER_STOP_TIMEOUT", true)
	if runner.Started() {
		t.Fatal("runner remained ready while draining")
	}
	select {
	case <-call.ctx.Done():
		t.Fatalf("stop cancelled in-flight dispatch: %v", call.ctx.Err())
	default:
	}
	close(release)
	if err := runner.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls := dispatcher.callCount(); calls != 1 {
		t.Fatalf("stop allowed another dispatch, calls=%d", calls)
	}
}

func TestRunnerParentCancellationStopsAfterInflightDispatch(t *testing.T) {
	release := make(chan struct{})
	dispatcher := &dispatcherFake{results: []dispatchResult{{wait: release}}}
	runner := retrievalruntime.NewRunner(dispatcher, validOptions())
	parent, cancel := context.WithCancel(context.Background())
	if err := runner.Start(parent); err != nil {
		t.Fatal(err)
	}
	call := receiveCall(t, dispatcher.callsChannel(), time.Second)
	cancel()
	waitUntil(t, time.Second, func() bool { return !runner.Started() })
	select {
	case <-call.ctx.Done():
		t.Fatalf("parent cancellation reached in-flight dispatch: %v", call.ctx.Err())
	default:
	}
	close(release)
	if err := runner.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls := dispatcher.callCount(); calls != 1 {
		t.Fatalf("parent cancellation allowed another dispatch, calls=%d", calls)
	}
}

func TestRunnerConcurrentStopsShareTheSameDrain(t *testing.T) {
	release := make(chan struct{})
	dispatcher := &dispatcherFake{results: []dispatchResult{{wait: release}}}
	runner := retrievalruntime.NewRunner(dispatcher, validOptions())
	if err := runner.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	receiveCall(t, dispatcher.callsChannel(), time.Second)

	const stopperCount = 8
	results := make(chan error, stopperCount)
	for range stopperCount {
		go func() { results <- runner.Stop(context.Background()) }()
	}
	waitUntil(t, time.Second, func() bool { return !runner.Started() })
	assertRunnerError(t, runner.Start(context.Background()), foundation.ErrorVersionConflict, "REINDEX_DISPATCHER_RUNNER_ALREADY_STARTED", false)
	close(release)
	for range stopperCount {
		if err := <-results; err != nil {
			t.Fatalf("concurrent stop: %v", err)
		}
	}
}

func TestRunnerLifecycleIsRestartableAndDuplicateSafe(t *testing.T) {
	dispatcher := &dispatcherFake{}
	runner := retrievalruntime.NewRunner(dispatcher, validOptions())
	if err := runner.Stop(context.Background()); err != nil {
		t.Fatalf("stop before start: %v", err)
	}
	if err := runner.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertRunnerError(t, runner.Start(context.Background()), foundation.ErrorVersionConflict, "REINDEX_DISPATCHER_RUNNER_ALREADY_STARTED", false)
	receiveCall(t, dispatcher.callsChannel(), time.Second)
	if err := runner.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := runner.Stop(context.Background()); err != nil {
		t.Fatalf("duplicate stop: %v", err)
	}
	if err := runner.Start(context.Background()); err != nil {
		t.Fatalf("restart: %v", err)
	}
	receiveCall(t, dispatcher.callsChannel(), time.Second)
	if err := runner.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerStartRejectsInvalidDependenciesAndOptions(t *testing.T) {
	var typedNil *dispatcherFake
	tests := map[string]*retrievalruntime.Runner{
		"nil dispatcher":       retrievalruntime.NewRunner(nil, validOptions()),
		"typed nil dispatcher": retrievalruntime.NewRunner(typedNil, validOptions()),
		"zero poll interval":   retrievalruntime.NewRunner(&dispatcherFake{}, withOptions(func(options *retrievalruntime.Options) { options.PollInterval = 0 })),
		"huge poll interval":   retrievalruntime.NewRunner(&dispatcherFake{}, withOptions(func(options *retrievalruntime.Options) { options.PollInterval = 24 * time.Hour })),
		"zero batch":           retrievalruntime.NewRunner(&dispatcherFake{}, withOptions(func(options *retrievalruntime.Options) { options.BatchSize = 0 })),
		"huge batch":           retrievalruntime.NewRunner(&dispatcherFake{}, withOptions(func(options *retrievalruntime.Options) { options.BatchSize = 1_000_000 })),
		"zero backoff":         retrievalruntime.NewRunner(&dispatcherFake{}, withOptions(func(options *retrievalruntime.Options) { options.ErrorBackoff = 0 })),
		"huge backoff":         retrievalruntime.NewRunner(&dispatcherFake{}, withOptions(func(options *retrievalruntime.Options) { options.ErrorBackoff = 24 * time.Hour })),
	}
	for name, runner := range tests {
		t.Run(name, func(t *testing.T) {
			assertRunnerError(t, runner.Start(context.Background()), foundation.ErrorInvalidInput, "REINDEX_DISPATCHER_RUNNER_INVALID", false)
			if runner.Started() {
				t.Fatal("invalid runner started")
			}
		})
	}

	runner := retrievalruntime.NewRunner(&dispatcherFake{}, validOptions())
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	assertRunnerError(t, runner.Start(cancelled), foundation.ErrorInvalidInput, "REINDEX_DISPATCHER_RUNNER_INVALID", false)
}

func validOptions() retrievalruntime.Options {
	return retrievalruntime.Options{
		PollInterval: 5 * time.Millisecond,
		BatchSize:    10,
		ErrorBackoff: 5 * time.Millisecond,
	}
}

func withOptions(mutate func(*retrievalruntime.Options)) retrievalruntime.Options {
	options := validOptions()
	mutate(&options)
	return options
}

func assertRunnerError(t *testing.T, err error, kind foundation.ErrorKind, code string, retryable bool) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		t.Fatalf("error %v is not classified", err)
	}
	if classified.Kind != kind || classified.Code != code || classified.Retryable != retryable {
		t.Fatalf("error=%+v", classified)
	}
}

func receiveCall(t *testing.T, calls <-chan dispatchCall, timeout time.Duration) dispatchCall {
	t.Helper()
	select {
	case call := <-calls:
		return call
	case <-time.After(timeout):
		t.Fatal("dispatcher was not called")
		return dispatchCall{}
	}
}

func waitUntil(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not satisfied")
}

type dispatchCall struct {
	ctx       context.Context
	batchSize int
	at        time.Time
}

type dispatchResult struct {
	err  error
	wait chan struct{}
}

type dispatcherFake struct {
	mu      sync.Mutex
	calls   chan dispatchCall
	results []dispatchResult
	count   int
}

func (f *dispatcherFake) DispatchBatch(ctx context.Context, batchSize int) error {
	f.mu.Lock()
	if f.calls == nil {
		f.calls = make(chan dispatchCall, 32)
	}
	call := dispatchCall{ctx: ctx, batchSize: batchSize, at: time.Now()}
	index := f.count
	f.count++
	var result dispatchResult
	if index < len(f.results) {
		result = f.results[index]
	}
	f.mu.Unlock()
	f.calls <- call
	if result.wait != nil {
		<-result.wait
	}
	return result.err
}

func (f *dispatcherFake) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.count
}

func (f *dispatcherFake) callsChannel() <-chan dispatchCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = make(chan dispatchCall, 32)
	}
	return f.calls
}
