package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowruntime "github.com/CodeZen-Lizhi/zhixu/internal/workflow/runtime"
	"github.com/riverqueue/river/rivertype"
)

func TestStartWorkerRuntimeStartsBeforeResumeAndReadiness(t *testing.T) {
	var order []string
	lifecycle := &fakeWorkerStartupLifecycle{order: &order}
	queue := &fakeWorkerQueueResumer{order: &order}
	health := &fakeWorkerStartupHealth{order: &order}
	readiness := newFakeWorkerStartupReadiness(&order)

	if err := startWorkerRuntime(
		context.Background(), true, time.Second, 2*time.Second,
		lifecycle, queue, readiness, health,
	); err != nil {
		t.Fatal(err)
	}
	want := []string{"lifecycle:start", "queue:resume", "readiness:river_started"}
	if !sameOrder(order, want) {
		t.Fatalf("startup order=%v want=%v", order, want)
	}
	snapshot := readiness.Snapshot()
	if !snapshot.RiverStarted || snapshot.ShuttingDown || lifecycle.shutdownCalls != 0 ||
		health.closeCalls != 0 || health.shutdownCalls != 0 {
		t.Fatalf("snapshot=%#v lifecycle=%#v health=%#v", snapshot, lifecycle, health)
	}
}

func TestStartWorkerRuntimeSkipsResumeWhenQueueMustRemainPaused(t *testing.T) {
	var order []string
	lifecycle := &fakeWorkerStartupLifecycle{order: &order}
	queue := &fakeWorkerQueueResumer{order: &order}
	health := &fakeWorkerStartupHealth{order: &order}
	readiness := newFakeWorkerStartupReadiness(&order)

	if err := startWorkerRuntime(
		context.Background(), false, time.Second, 2*time.Second,
		lifecycle, queue, readiness, health,
	); err != nil {
		t.Fatal(err)
	}
	want := []string{"queue:pause", "lifecycle:start", "readiness:river_started"}
	if !sameOrder(order, want) {
		t.Fatalf("startup order=%v want=%v", order, want)
	}
	snapshot := readiness.Snapshot()
	if queue.pauseCalls != 1 || queue.resumeCalls != 0 || !snapshot.RiverStarted || snapshot.ShuttingDown || lifecycle.shutdownCalls != 0 ||
		health.closeCalls != 0 || health.shutdownCalls != 0 {
		t.Fatalf("snapshot=%#v lifecycle=%#v queue=%#v health=%#v", snapshot, lifecycle, queue, health)
	}
}

func TestStartWorkerRuntimeMissingPausedQueueFailsClosedBeforeStart(t *testing.T) {
	var order []string
	lifecycle := &fakeWorkerStartupLifecycle{order: &order}
	queue := &fakeWorkerQueueResumer{
		order: &order,
		pauseErr: foundation.NewError(
			foundation.ErrorRetryableFailure,
			"WORKFLOW_RIVER_QUEUE_PAUSE_FAILED",
			true,
			rivertype.ErrNotFound,
		),
	}
	health := &fakeWorkerStartupHealth{order: &order}
	readiness := newFakeWorkerStartupReadiness(&order)

	err := startWorkerRuntime(
		context.Background(), false, time.Second, 2*time.Second,
		lifecycle, queue, readiness, health,
	)
	if !errors.Is(err, rivertype.ErrNotFound) {
		t.Fatalf("startup error=%v", err)
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "WORKFLOW_RIVER_QUEUE_PAUSE_FAILED" {
		t.Fatalf("startup classification=%#v error=%v", classified, err)
	}
	want := []string{"queue:pause", "readiness:shutdown", "health:close"}
	if !sameOrder(order, want) {
		t.Fatalf("failure order=%v want=%v", order, want)
	}
	snapshot := readiness.Snapshot()
	if lifecycle.startCalls != 0 || lifecycle.shutdownCalls != 0 || queue.pauseCalls != 1 ||
		queue.resumeCalls != 0 || snapshot.RiverStarted || !snapshot.ShuttingDown || health.closeCalls != 1 {
		t.Fatalf("snapshot=%#v lifecycle=%#v queue=%#v health=%#v", snapshot, lifecycle, queue, health)
	}
}

func TestStartWorkerRuntimeResumeFailureCleansUpWithIndependentDeadline(t *testing.T) {
	var order []string
	shutdownFailure := errors.New("lifecycle shutdown failed")
	healthFailure := errors.New("health shutdown failed")
	lifecycle := &fakeWorkerStartupLifecycle{order: &order, shutdownErr: shutdownFailure}
	processContext, cancelProcess := context.WithCancel(context.Background())
	queue := &fakeWorkerQueueResumer{
		order: &order,
		err: foundation.NewError(
			foundation.ErrorRetryableFailure,
			"WORKFLOW_RIVER_QUEUE_RESUME_FAILED",
			true,
			rivertype.ErrNotFound,
		),
		beforeReturn: func() {
			cancelProcess()
		},
	}
	health := &fakeWorkerStartupHealth{order: &order, closeErr: healthFailure}
	readiness := newFakeWorkerStartupReadiness(&order)

	err := startWorkerRuntime(
		processContext, true, time.Second, 2*time.Second,
		lifecycle, queue, readiness, health,
	)
	if !errors.Is(err, rivertype.ErrNotFound) || !errors.Is(err, shutdownFailure) || !errors.Is(err, healthFailure) {
		t.Fatalf("startup error=%v", err)
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "WORKFLOW_RIVER_QUEUE_RESUME_FAILED" {
		t.Fatalf("startup classification=%#v error=%v", classified, err)
	}
	want := []string{"lifecycle:start", "queue:resume", "readiness:shutdown", "lifecycle:shutdown", "health:close"}
	if !sameOrder(order, want) {
		t.Fatalf("failure order=%v want=%v", order, want)
	}
	snapshot := readiness.Snapshot()
	if snapshot.RiverStarted || !snapshot.ShuttingDown || lifecycle.shutdownCalls != 1 ||
		lifecycle.shutdownMode != shutdownGraceful || lifecycle.shutdownContextErr != nil ||
		!lifecycle.shutdownHasDeadline || health.closeCalls != 1 || health.shutdownCalls != 0 {
		t.Fatalf("snapshot=%#v lifecycle=%#v health=%#v", snapshot, lifecycle, health)
	}
}

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

type fakeWorkerStartupLifecycle struct {
	order               *[]string
	startErr            error
	shutdownErr         error
	startCalls          int
	shutdownCalls       int
	shutdownMode        shutdownMode
	shutdownContextErr  error
	shutdownHasDeadline bool
}

func (lifecycle *fakeWorkerStartupLifecycle) Start(context.Context) error {
	lifecycle.startCalls++
	if lifecycle.order != nil {
		*lifecycle.order = append(*lifecycle.order, "lifecycle:start")
	}
	return lifecycle.startErr
}

func (lifecycle *fakeWorkerStartupLifecycle) Shutdown(ctx context.Context, mode shutdownMode) error {
	lifecycle.shutdownCalls++
	lifecycle.shutdownMode = mode
	lifecycle.shutdownContextErr = ctx.Err()
	_, lifecycle.shutdownHasDeadline = ctx.Deadline()
	if lifecycle.order != nil {
		*lifecycle.order = append(*lifecycle.order, "lifecycle:shutdown")
	}
	return lifecycle.shutdownErr
}

type fakeWorkerQueueResumer struct {
	order        *[]string
	err          error
	pauseErr     error
	beforeReturn func()
	pauseCalls   int
	resumeCalls  int
}

func (queue *fakeWorkerQueueResumer) PauseQueue(context.Context) error {
	queue.pauseCalls++
	if queue.order != nil {
		*queue.order = append(*queue.order, "queue:pause")
	}
	return queue.pauseErr
}

func (queue *fakeWorkerQueueResumer) ResumeQueue(context.Context) error {
	queue.resumeCalls++
	if queue.order != nil {
		*queue.order = append(*queue.order, "queue:resume")
	}
	if queue.beforeReturn != nil {
		queue.beforeReturn()
	}
	return queue.err
}

type fakeWorkerStartupHealth struct {
	order         *[]string
	closeErr      error
	closeCalls    int
	shutdownCalls int
}

type fakeWorkerStartupReadiness struct {
	readiness *workflowruntime.Readiness
	order     *[]string
}

func newFakeWorkerStartupReadiness(order *[]string) *fakeWorkerStartupReadiness {
	return &fakeWorkerStartupReadiness{readiness: workflowruntime.NewReadiness(), order: order}
}

func (readiness *fakeWorkerStartupReadiness) SetRiverStarted(started bool) {
	if readiness.order != nil {
		event := "readiness:river_stopped"
		if started {
			event = "readiness:river_started"
		}
		*readiness.order = append(*readiness.order, event)
	}
	readiness.readiness.SetRiverStarted(started)
}

func (readiness *fakeWorkerStartupReadiness) BeginShutdown() {
	if readiness.order != nil {
		*readiness.order = append(*readiness.order, "readiness:shutdown")
	}
	readiness.readiness.BeginShutdown()
}

func (readiness *fakeWorkerStartupReadiness) Snapshot() workflowruntime.ReadinessSnapshot {
	return readiness.readiness.Snapshot()
}

func (health *fakeWorkerStartupHealth) Close() error {
	health.closeCalls++
	if health.order != nil {
		*health.order = append(*health.order, "health:close")
	}
	return health.closeErr
}

func (health *fakeWorkerStartupHealth) Shutdown(context.Context) error {
	health.shutdownCalls++
	return nil
}

func sameOrder(actual, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	for index := range actual {
		if actual[index] != expected[index] {
			return false
		}
	}
	return true
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
