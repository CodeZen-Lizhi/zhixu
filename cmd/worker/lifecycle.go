package main

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"
)

type shutdownMode string

const (
	shutdownGraceful  shutdownMode = "graceful"
	shutdownEmergency shutdownMode = "emergency"
)

type riverLifecycle interface {
	Start(context.Context) error
	Stop(context.Context) error
	StopAndCancel(context.Context) error
}

type dispatcherLifecycle interface {
	Start(context.Context) error
	Stop(context.Context) error
}

type workerStartupLifecycle interface {
	Start(context.Context) error
	Shutdown(context.Context, shutdownMode) error
}

type workerQueueController interface {
	PauseQueue(context.Context) error
	ResumeQueue(context.Context) error
}

type workerStartupReadiness interface {
	SetRiverStarted(bool)
	BeginShutdown()
}

type workerStartupHealth interface {
	Close() error
}

type lifecycleState uint8

const (
	lifecycleIdle lifecycleState = iota
	lifecycleStarting
	lifecycleRunning
	lifecycleStopping
	lifecycleStopped
)

// lifecycleController owns Dispatcher/River ordering. The first shutdown
// event wins; later signal or fatal events only wait for that result.
type lifecycleController struct {
	client     riverLifecycle
	dispatcher dispatcherLifecycle

	mu       sync.Mutex
	state    lifecycleState
	mode     shutdownMode
	stopErr  error
	stopDone chan struct{}
}

func newLifecycleController(client riverLifecycle, dispatcher dispatcherLifecycle) (*lifecycleController, error) {
	if nilLifecycleDependency(client) || nilLifecycleDependency(dispatcher) {
		return nil, errors.New("worker lifecycle dependencies are nil")
	}
	return &lifecycleController{client: client, dispatcher: dispatcher, state: lifecycleIdle, stopDone: make(chan struct{})}, nil
}

// startWorkerRuntime 返回启动结果，以及失败时是否已确认所有消费者停止。
func startWorkerRuntime(
	processContext context.Context,
	resumeQueue bool,
	queueResumeTimeout time.Duration,
	hardStopTimeout time.Duration,
	lifecycle workerStartupLifecycle,
	queue workerQueueController,
	readiness workerStartupReadiness,
	health workerStartupHealth,
	shutdownContexts ...func() context.Context,
) (bool, error) {
	if !resumeQueue {
		pauseContext, cancelPause := context.WithTimeout(processContext, queueResumeTimeout)
		pauseErr := queue.PauseQueue(pauseContext)
		cancelPause()
		if pauseErr != nil {
			readiness.BeginShutdown()
			return true, errors.Join(pauseErr, health.Close())
		}
	}
	if err := lifecycle.Start(processContext); err != nil {
		readiness.BeginShutdown()
		return false, errors.Join(err, health.Close())
	}
	if resumeQueue {
		resumeContext, cancelResume := context.WithTimeout(processContext, queueResumeTimeout)
		resumeErr := queue.ResumeQueue(resumeContext)
		cancelResume()
		if resumeErr != nil {
			readiness.BeginShutdown()
			var shutdownContext context.Context
			if len(shutdownContexts) > 0 && shutdownContexts[0] != nil {
				// The process owner shares this deadline with background runtime cleanup.
				shutdownContext = shutdownContexts[0]()
			} else {
				var cancelShutdown context.CancelFunc
				shutdownContext, cancelShutdown = context.WithTimeout(context.Background(), hardStopTimeout)
				defer cancelShutdown()
			}
			lifecycleErr := lifecycle.Shutdown(shutdownContext, shutdownGraceful)
			healthErr := health.Close()
			return lifecycleErr == nil, errors.Join(resumeErr, lifecycleErr, healthErr)
		}
	}
	readiness.SetRiverStarted(true)
	return false, nil
}

func nilLifecycleDependency(value any) bool {
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

func (c *lifecycleController) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("river start context is nil")
	}
	c.mu.Lock()
	if c.state != lifecycleIdle {
		c.mu.Unlock()
		return errors.New("river lifecycle already started")
	}
	c.state = lifecycleStarting
	c.mu.Unlock()

	if c.dispatcher != nil {
		if err := c.dispatcher.Start(ctx); err != nil {
			c.finishFailedStart(err)
			return err
		}
	}
	if err := c.client.Start(ctx); err != nil {
		var dispatcherErr error
		if c.dispatcher != nil {
			dispatcherErr = c.dispatcher.Stop(ctx)
		}
		err = errors.Join(err, dispatcherErr)
		c.finishFailedStart(err)
		return err
	}
	c.mu.Lock()
	c.state = lifecycleRunning
	c.mu.Unlock()
	return nil
}

func (c *lifecycleController) finishFailedStart(err error) {
	c.mu.Lock()
	c.state = lifecycleStopped
	c.stopErr = err
	close(c.stopDone)
	c.mu.Unlock()
}

func (c *lifecycleController) Shutdown(ctx context.Context, mode shutdownMode) error {
	if ctx == nil {
		return errors.New("river shutdown context is nil")
	}
	if mode != shutdownGraceful && mode != shutdownEmergency {
		return errors.New("river shutdown mode is invalid")
	}

	c.mu.Lock()
	switch c.state {
	case lifecycleRunning:
		c.state = lifecycleStopping
		c.mode = mode
		c.mu.Unlock()
		var dispatcherErr error
		if c.dispatcher != nil {
			dispatcherErr = c.dispatcher.Stop(ctx)
		}
		var riverErr error
		if mode == shutdownEmergency {
			riverErr = c.client.StopAndCancel(ctx)
		} else {
			riverErr = c.client.Stop(ctx)
		}
		err := errors.Join(dispatcherErr, riverErr)
		c.mu.Lock()
		c.stopErr = err
		c.state = lifecycleStopped
		close(c.stopDone)
		c.mu.Unlock()
		return err
	case lifecycleStopping, lifecycleStopped:
		done := c.stopDone
		c.mu.Unlock()
		select {
		case <-done:
			c.mu.Lock()
			err := c.stopErr
			c.mu.Unlock()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	default:
		c.mu.Unlock()
		return errors.New("river lifecycle is not running")
	}
}

func (c *lifecycleController) Mode() shutdownMode {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mode
}
