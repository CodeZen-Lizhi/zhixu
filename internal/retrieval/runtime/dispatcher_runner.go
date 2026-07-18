package runtime

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	maxBatchSize      = 1_000
	maxErrorBackoff   = time.Minute
	maxPollInterval   = time.Minute
	runnerFailed      = "REINDEX_DISPATCHER_RUNNER_FAILED"
	runnerInvalid     = "REINDEX_DISPATCHER_RUNNER_INVALID"
	runnerStarted     = "REINDEX_DISPATCHER_RUNNER_ALREADY_STARTED"
	runnerStopTimeout = "REINDEX_DISPATCHER_RUNNER_STOP_TIMEOUT"
)

// Dispatcher 是 Runner 每轮调用的最小批量派发边界。
type Dispatcher interface {
	DispatchBatch(context.Context, int) error
}

// Options 配置 Dispatcher Runner 的有界轮询与退避参数。
type Options struct {
	// PollInterval 是成功派发后开始下一轮派发前的等待时间。
	PollInterval time.Duration
	// BatchSize 是每轮允许 Dispatcher 领取的最大记录数。
	BatchSize int
	// ErrorBackoff 是瞬时失败后重新派发前的等待时间。
	ErrorBackoff time.Duration
}

// Runner 在后台串行执行短事务 Dispatcher，并对外暴露 readiness 与 fatal 错误。
type Runner struct {
	dispatcher Dispatcher
	options    Options
	errors     chan error

	started atomic.Bool
	mu      sync.Mutex
	state   runnerState
	stop    chan struct{}
	done    chan struct{}
	gen     uint64
}

type runnerState uint8

const (
	runnerIdle runnerState = iota
	runnerRunning
	runnerStopping
)

// NewRunner 创建 Dispatcher Runner；依赖和参数在 Start 时统一 fail-fast 校验。
func NewRunner(dispatcher Dispatcher, options Options) *Runner {
	return &Runner{
		dispatcher: dispatcher,
		options:    options,
		errors:     make(chan error, 1),
	}
}

// Start 启动后台派发；首轮立即派发，成功后才进入轮询等待。
func (r *Runner) Start(ctx context.Context) error {
	if err := r.validateStart(ctx); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state != runnerIdle {
		return runnerError(foundation.ErrorVersionConflict, runnerStarted, false, errors.New("dispatcher runner is already active"))
	}
	r.gen++
	generation := r.gen
	stop := make(chan struct{})
	done := make(chan struct{})
	r.stop = stop
	r.done = done
	r.state = runnerRunning
	r.started.Store(true)
	parentDone := ctx.Done()
	go r.run(context.WithoutCancel(ctx), parentDone, generation, stop, done)
	if parentDone != nil {
		go r.watchParent(parentDone, generation, done)
	}
	return nil
}

// Stop 原子阻止新一轮派发并等待当前短事务结束；它不会取消正在执行的 Dispatcher 调用。
func (r *Runner) Stop(ctx context.Context) error {
	if r == nil {
		return runnerError(foundation.ErrorInvalidInput, runnerInvalid, false, errors.New("dispatcher runner is nil"))
	}
	if ctx == nil {
		return runnerError(foundation.ErrorInvalidInput, runnerInvalid, false, errors.New("stop context is nil"))
	}

	r.mu.Lock()
	switch r.state {
	case runnerIdle:
		r.started.Store(false)
		r.mu.Unlock()
		return nil
	case runnerRunning:
		r.state = runnerStopping
		r.started.Store(false)
		close(r.stop)
	case runnerStopping:
		// 另一个 Stop 已经发出停止信号；共同等待同一代运行结束。
	}
	done := r.done
	r.mu.Unlock()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return runnerError(foundation.ErrorRetryableFailure, runnerStopTimeout, true, ctx.Err())
	}
}

// Started 报告 Runner 是否仍允许开始新一轮派发。
func (r *Runner) Started() bool {
	return r != nil && r.started.Load()
}

// Errors 返回只承载稳定非重试错误的 fatal 通道。
func (r *Runner) Errors() <-chan error {
	if r == nil {
		return nil
	}
	return r.errors
}

func (r *Runner) run(ctx context.Context, parentDone <-chan struct{}, generation uint64, stop <-chan struct{}, done chan struct{}) {
	defer r.finish(generation, done)
	for {
		if stopped(stop, parentDone) {
			return
		}

		err := r.dispatcher.DispatchBatch(ctx, r.options.BatchSize)
		if err != nil {
			if transientDispatchError(err) {
				if !waitForNext(stop, parentDone, r.options.ErrorBackoff) {
					return
				}
				continue
			}
			r.started.Store(false)
			r.reportFatal(stableFatalError(err))
			return
		}
		if !waitForNext(stop, parentDone, r.options.PollInterval) {
			return
		}
	}
}

func (r *Runner) watchParent(parentDone <-chan struct{}, generation uint64, done <-chan struct{}) {
	select {
	case <-parentDone:
		r.mu.Lock()
		if r.gen == generation && r.done == done {
			r.started.Store(false)
		}
		r.mu.Unlock()
	case <-done:
	}
}

func (r *Runner) finish(generation uint64, done chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gen != generation || r.done != done {
		return
	}
	r.started.Store(false)
	close(done)
	r.state = runnerIdle
}

func (r *Runner) reportFatal(err error) {
	select {
	case r.errors <- err:
	default:
	}
}

func (r *Runner) validateStart(ctx context.Context) error {
	if r == nil {
		return runnerError(foundation.ErrorInvalidInput, runnerInvalid, false, errors.New("dispatcher runner is nil"))
	}
	if ctx == nil || ctx.Err() != nil {
		return runnerError(foundation.ErrorInvalidInput, runnerInvalid, false, errors.New("start context is unavailable"))
	}
	if nilInterface(r.dispatcher) {
		return runnerError(foundation.ErrorInvalidInput, runnerInvalid, false, errors.New("dispatcher is nil"))
	}
	if r.options.PollInterval <= 0 || r.options.PollInterval > maxPollInterval {
		return runnerError(foundation.ErrorInvalidInput, runnerInvalid, false, errors.New("poll interval is outside the supported range"))
	}
	if r.options.BatchSize <= 0 || r.options.BatchSize > maxBatchSize {
		return runnerError(foundation.ErrorInvalidInput, runnerInvalid, false, errors.New("batch size is outside the supported range"))
	}
	if r.options.ErrorBackoff <= 0 || r.options.ErrorBackoff > maxErrorBackoff {
		return runnerError(foundation.ErrorInvalidInput, runnerInvalid, false, errors.New("error backoff is outside the supported range"))
	}
	return nil
}

func transientDispatchError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Retryable
}

func stableFatalError(err error) error {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	return runnerError(foundation.ErrorNonRetryableFailure, runnerFailed, false, err)
}

func stopped(stop, parentDone <-chan struct{}) bool {
	select {
	case <-stop:
		return true
	case <-parentDone:
		return true
	default:
		return false
	}
}

func waitForNext(stop, parentDone <-chan struct{}, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-stop:
		return false
	case <-parentDone:
		return false
	case <-timer.C:
		return true
	}
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func runnerError(kind foundation.ErrorKind, code string, retryable bool, cause error) error {
	return foundation.NewError(kind, code, retryable, cause)
}
