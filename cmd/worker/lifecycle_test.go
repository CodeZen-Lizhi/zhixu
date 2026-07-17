package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestLifecycleControllerFirstShutdownModeWins(t *testing.T) {
	client := &fakeRiverLifecycle{}
	controller, err := newLifecycleController(client)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := controller.Shutdown(context.Background(), shutdownGraceful); err != nil {
		t.Fatal(err)
	}
	if err := controller.Shutdown(context.Background(), shutdownEmergency); err != nil {
		t.Fatal(err)
	}
	if client.startCalls != 1 || client.stopCalls != 1 || client.cancelCalls != 0 || controller.Mode() != shutdownGraceful {
		t.Fatalf("client=%+v mode=%s", client, controller.Mode())
	}
}

func TestLifecycleControllerHonorsHardShutdownContext(t *testing.T) {
	client := &fakeRiverLifecycle{waitForStopContext: true}
	controller, err := newLifecycleController(client)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := controller.Shutdown(ctx, shutdownGraceful); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown err=%v", err)
	}
	if client.stopCalls != 1 || client.cancelCalls != 0 {
		t.Fatalf("client=%+v", client)
	}
}

func TestLifecycleControllerEmergencyShutdownIsExclusive(t *testing.T) {
	client := &fakeRiverLifecycle{}
	controller, err := newLifecycleController(client)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := controller.Shutdown(context.Background(), shutdownEmergency); err != nil {
		t.Fatal(err)
	}
	if client.stopCalls != 0 || client.cancelCalls != 1 || controller.Mode() != shutdownEmergency {
		t.Fatalf("client=%+v mode=%s", client, controller.Mode())
	}
}

func TestLifecycleControllerPropagatesStartAndStopFailure(t *testing.T) {
	startFailure := errors.New("start failed")
	controller, err := newLifecycleController(&fakeRiverLifecycle{startErr: startFailure})
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Start(context.Background()); !errors.Is(err, startFailure) {
		t.Fatalf("start err=%v", err)
	}

	stopFailure := errors.New("stop failed")
	controller, err = newLifecycleController(&fakeRiverLifecycle{stopErr: stopFailure})
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := controller.Shutdown(context.Background(), shutdownGraceful); !errors.Is(err, stopFailure) {
		t.Fatalf("stop err=%v", err)
	}
}

type fakeRiverLifecycle struct {
	mu                 sync.Mutex
	startCalls         int
	stopCalls          int
	cancelCalls        int
	startErr           error
	stopErr            error
	cancelErr          error
	waitForStopContext bool
}

func (f *fakeRiverLifecycle) Start(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls++
	return f.startErr
}

func (f *fakeRiverLifecycle) Stop(ctx context.Context) error {
	f.mu.Lock()
	f.stopCalls++
	wait := f.waitForStopContext
	err := f.stopErr
	f.mu.Unlock()
	if wait {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

func (f *fakeRiverLifecycle) StopAndCancel(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelCalls++
	return f.cancelErr
}
