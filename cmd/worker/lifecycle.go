package main

import (
	"context"
	"errors"
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

type lifecycleState uint8

const (
	lifecycleIdle lifecycleState = iota
	lifecycleStarting
	lifecycleRunning
	lifecycleStopping
	lifecycleStopped
)

// lifecycleController owns the single River start/stop decision. The first
// shutdown event wins; later signal or fatal events only wait for that result.
type lifecycleController struct {
	client riverLifecycle

	mu       sync.Mutex
	state    lifecycleState
	mode     shutdownMode
	stopErr  error
	stopDone chan struct{}
}

func newLifecycleController(client riverLifecycle) (*lifecycleController, error) {
	if client == nil {
		return nil, errors.New("river lifecycle client is nil")
	}
	return &lifecycleController{client: client, state: lifecycleIdle, stopDone: make(chan struct{})}, nil
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

	if err := c.client.Start(ctx); err != nil {
		c.mu.Lock()
		c.state = lifecycleStopped
		c.stopErr = err
		close(c.stopDone)
		c.mu.Unlock()
		return err
	}
	c.mu.Lock()
	c.state = lifecycleRunning
	c.mu.Unlock()
	return nil
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
		var err error
		if mode == shutdownEmergency {
			err = c.client.StopAndCancel(ctx)
		} else {
			err = c.client.Stop(ctx)
		}
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
