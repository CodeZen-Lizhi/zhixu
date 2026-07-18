package main

import (
	"context"
	"errors"
	"reflect"
	"sync"
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
