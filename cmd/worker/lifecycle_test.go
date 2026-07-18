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
	controller, err := newLifecycleController(client, &fakeDispatcherLifecycle{})
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
	controller, err := newLifecycleController(client, &fakeDispatcherLifecycle{})
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
	controller, err := newLifecycleController(client, &fakeDispatcherLifecycle{})
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
	controller, err := newLifecycleController(&fakeRiverLifecycle{startErr: startFailure}, &fakeDispatcherLifecycle{})
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Start(context.Background()); !errors.Is(err, startFailure) {
		t.Fatalf("start err=%v", err)
	}

	stopFailure := errors.New("stop failed")
	controller, err = newLifecycleController(&fakeRiverLifecycle{stopErr: stopFailure}, &fakeDispatcherLifecycle{})
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

func TestLifecycleControllerStartsDispatcherBeforeRiverAndStopsItFirst(t *testing.T) {
	var order []string
	dispatcher := &fakeDispatcherLifecycle{order: &order}
	client := &fakeRiverLifecycle{order: &order}
	controller, err := newLifecycleController(client, dispatcher)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := controller.Shutdown(context.Background(), shutdownGraceful); err != nil {
		t.Fatal(err)
	}
	want := []string{"dispatcher:start", "river:start", "dispatcher:stop", "river:stop"}
	if len(order) != len(want) {
		t.Fatalf("order=%v", order)
	}
	for index := range want {
		if order[index] != want[index] {
			t.Fatalf("order=%v want=%v", order, want)
		}
	}
}

func TestLifecycleControllerStopsDispatcherWhenRiverStartFails(t *testing.T) {
	startFailure := errors.New("river start failed")
	var order []string
	dispatcher := &fakeDispatcherLifecycle{order: &order}
	client := &fakeRiverLifecycle{order: &order, startErr: startFailure}
	controller, err := newLifecycleController(client, dispatcher)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Start(context.Background()); !errors.Is(err, startFailure) {
		t.Fatalf("start err=%v", err)
	}
	want := []string{"dispatcher:start", "river:start", "dispatcher:stop"}
	if len(order) != len(want) {
		t.Fatalf("order=%v", order)
	}
	for index := range want {
		if order[index] != want[index] {
			t.Fatalf("order=%v want=%v", order, want)
		}
	}
}

func TestLifecycleControllerStillStopsRiverWhenDispatcherStopFails(t *testing.T) {
	stopFailure := errors.New("dispatcher stop failed")
	dispatcher := &fakeDispatcherLifecycle{stopErr: stopFailure}
	client := &fakeRiverLifecycle{}
	controller, err := newLifecycleController(client, dispatcher)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := controller.Shutdown(context.Background(), shutdownEmergency); !errors.Is(err, stopFailure) {
		t.Fatalf("shutdown err=%v", err)
	}
	if dispatcher.stopCalls != 1 || client.cancelCalls != 1 {
		t.Fatalf("dispatcher=%+v client=%+v", dispatcher, client)
	}
}

func TestLifecycleControllerRejectsTypedNilDependencies(t *testing.T) {
	var client *fakeRiverLifecycle
	if _, err := newLifecycleController(client, &fakeDispatcherLifecycle{}); err == nil {
		t.Fatal("typed nil River lifecycle accepted")
	}
	var dispatcher *fakeDispatcherLifecycle
	if _, err := newLifecycleController(&fakeRiverLifecycle{}, dispatcher); err == nil {
		t.Fatal("typed nil Dispatcher lifecycle accepted")
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
	order              *[]string
}

func (f *fakeRiverLifecycle) Start(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls++
	if f.order != nil {
		*f.order = append(*f.order, "river:start")
	}
	return f.startErr
}

func (f *fakeRiverLifecycle) Stop(ctx context.Context) error {
	f.mu.Lock()
	f.stopCalls++
	if f.order != nil {
		*f.order = append(*f.order, "river:stop")
	}
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
	if f.order != nil {
		*f.order = append(*f.order, "river:cancel")
	}
	return f.cancelErr
}

type fakeDispatcherLifecycle struct {
	startCalls int
	stopCalls  int
	startErr   error
	stopErr    error
	order      *[]string
}

func (f *fakeDispatcherLifecycle) Start(context.Context) error {
	f.startCalls++
	if f.order != nil {
		*f.order = append(*f.order, "dispatcher:start")
	}
	return f.startErr
}

func (f *fakeDispatcherLifecycle) Stop(context.Context) error {
	f.stopCalls++
	if f.order != nil {
		*f.order = append(*f.order, "dispatcher:stop")
	}
	return f.stopErr
}
