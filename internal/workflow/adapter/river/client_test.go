package riveradapter

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	riverlib "github.com/riverqueue/river"
)

func TestClientAndMigratorAlwaysUseWorkflowSchema(t *testing.T) {
	if _, err := NewClient(nil, nil); err == nil {
		t.Fatal("nil pool accepted by client")
	}
	if _, err := NewMigrator(nil); err == nil {
		t.Fatal("nil pool accepted by migrator")
	}
}

func TestClientLifecycleTracksStartedAndClassifiesFailures(t *testing.T) {
	t.Run("start stop", func(t *testing.T) {
		lifecycle := newLifecycleFake()
		client := &Client{lifecycle: lifecycle}
		if client.Started() {
			t.Fatal("unstarted client reported started")
		}
		if err := client.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !client.Started() {
			t.Fatal("successful start was not observable")
		}
		if err := client.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		if client.Started() || lifecycle.startCalls != 1 || lifecycle.stopCalls != 1 || lifecycle.cancelCalls != 0 {
			t.Fatalf("client started=%t lifecycle=%+v", client.Started(), lifecycle)
		}
	})

	t.Run("start failure", func(t *testing.T) {
		lifecycle := newLifecycleFake()
		lifecycle.startErr = errors.New("database unavailable")
		client := &Client{lifecycle: lifecycle}
		err := client.Start(context.Background())
		assertClientError(t, err, foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_CLIENT_START_FAILED", true)
		if client.Started() {
			t.Fatal("failed start was observable as started")
		}
	})

	t.Run("stop and cancel failure", func(t *testing.T) {
		lifecycle := newLifecycleFake()
		lifecycle.cancelErr = errors.New("shutdown deadline exceeded")
		client := &Client{lifecycle: lifecycle}
		if err := client.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		err := client.StopAndCancel(context.Background())
		assertClientError(t, err, foundation.ErrorRetryableFailure, "WORKFLOW_RIVER_CLIENT_STOP_AND_CANCEL_FAILED", true)
		if client.Started() || lifecycle.cancelCalls != 1 || lifecycle.stopCalls != 0 {
			t.Fatalf("client started=%t lifecycle=%+v", client.Started(), lifecycle)
		}
	})

	t.Run("stop failure", func(t *testing.T) {
		lifecycle := newLifecycleFake()
		lifecycle.stopErr = errors.New("shutdown deadline exceeded")
		client := &Client{lifecycle: lifecycle}
		if err := client.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		err := client.Stop(context.Background())
		assertClientError(t, err, foundation.ErrorRetryableFailure, "WORKFLOW_RIVER_CLIENT_STOP_FAILED", true)
		if client.Started() {
			t.Fatal("stopping client remained ready after stop failure")
		}
	})

	t.Run("underlying stopped", func(t *testing.T) {
		lifecycle := newLifecycleFake()
		client := &Client{lifecycle: lifecycle}
		if err := client.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		close(lifecycle.stopped)
		deadline := time.Now().Add(time.Second)
		for client.Started() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if client.Started() {
			t.Fatal("closed stopped channel did not clear started state")
		}
	})
}

func TestClientOptionsMapToRiverConfiguration(t *testing.T) {
	options := Options{
		JobTimeout:           15 * time.Minute,
		MaxWorkers:           7,
		Queue:                "workflow_priority",
		RescueStuckJobsAfter: 30 * time.Minute,
		SoftStopTimeout:      45 * time.Second,
	}
	workers := NewWorkers()
	config, err := buildRiverConfig(workers, options)
	if err != nil {
		t.Fatal(err)
	}
	queueConfig, ok := config.Queues[options.Queue]
	if !ok || queueConfig.MaxWorkers != options.MaxWorkers {
		t.Fatalf("queues = %+v", config.Queues)
	}
	if config.JobTimeout != options.JobTimeout || config.RescueStuckJobsAfter != options.RescueStuckJobsAfter || config.SoftStopTimeout != options.SoftStopTimeout {
		t.Fatalf("river config = %+v", config)
	}
	if config.Schema != WorkflowSchema || config.Workers == nil {
		t.Fatalf("schema=%q workers=%v", config.Schema, config.Workers)
	}

	insertConfig, err := buildRiverConfig(nil, options)
	if err != nil {
		t.Fatal(err)
	}
	if insertConfig.Queues != nil || insertConfig.Workers != nil {
		t.Fatalf("insert-only config unexpectedly executes jobs: %+v", insertConfig)
	}
}

func TestClientOptionsRejectInvalidValues(t *testing.T) {
	valid := DefaultOptions()
	tests := map[string]Options{
		"empty queue":             withOptions(valid, func(options *Options) { options.Queue = "" }),
		"invalid queue separator": withOptions(valid, func(options *Options) { options.Queue = "workflow queue" }),
		"long queue":              withOptions(valid, func(options *Options) { options.Queue = strings.Repeat("a", 65) }),
		"queue control":           withOptions(valid, func(options *Options) { options.Queue = "workflow\n" }),
		"zero max workers":        withOptions(valid, func(options *Options) { options.MaxWorkers = 0 }),
		"too many workers":        withOptions(valid, func(options *Options) { options.MaxWorkers = 10_001 }),
		"zero job timeout":        withOptions(valid, func(options *Options) { options.JobTimeout = 0 }),
		"equal rescue timeout":    withOptions(valid, func(options *Options) { options.RescueStuckJobsAfter = options.JobTimeout }),
		"short rescue timeout":    withOptions(valid, func(options *Options) { options.RescueStuckJobsAfter = options.JobTimeout - time.Second }),
		"zero soft stop":          withOptions(valid, func(options *Options) { options.SoftStopTimeout = 0 }),
	}
	for name, options := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := buildRiverConfig(nil, options)
			assertClientError(t, err, foundation.ErrorInvalidInput, "WORKFLOW_RIVER_OPTIONS_INVALID", false)
		})
	}
}

func TestClientUsesSafeDefaults(t *testing.T) {
	options := DefaultOptions()
	if options.Queue != "default" || options.MaxWorkers != 1 || options.JobTimeout != time.Minute || options.RescueStuckJobsAfter != time.Hour || options.SoftStopTimeout <= 0 {
		t.Fatalf("defaults = %+v", options)
	}
	client := &Client{queue: options.Queue, schema: WorkflowSchema}
	if client.Queue() != options.Queue || client.Schema() != WorkflowSchema {
		t.Fatalf("queue=%q schema=%q", client.Queue(), client.Schema())
	}
	if (*Client)(nil).Queue() != "" || (*Client)(nil).Schema() != "" || (*Client)(nil).Started() {
		t.Fatal("nil client probes are not safe")
	}
}

func assertClientError(t *testing.T, err error, kind foundation.ErrorKind, code string, retryable bool) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		t.Fatalf("error %v is not classified", err)
	}
	if classified.Kind != kind || classified.Code != code || classified.Retryable != retryable {
		t.Fatalf("error = %+v", classified)
	}
}

type lifecycleFake struct {
	mu          sync.Mutex
	cancelCalls int
	cancelErr   error
	startCalls  int
	startErr    error
	stopCalls   int
	stopErr     error
	stopped     chan struct{}
}

func newLifecycleFake() *lifecycleFake {
	return &lifecycleFake{stopped: make(chan struct{})}
}

func (f *lifecycleFake) Start(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls++
	return f.startErr
}

func (f *lifecycleFake) Stop(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalls++
	return f.stopErr
}

func (f *lifecycleFake) StopAndCancel(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelCalls++
	return f.cancelErr
}

func (f *lifecycleFake) Stopped() <-chan struct{} {
	return f.stopped
}

func withOptions(base Options, mutate func(*Options)) Options {
	mutate(&base)
	return base
}

type queueControlFake struct {
	paused  string
	resumed string
	err     error
}

func (f *queueControlFake) QueuePause(_ context.Context, queue string, _ *riverlib.QueuePauseOpts) error {
	f.paused = queue
	return f.err
}

func (f *queueControlFake) QueueResume(_ context.Context, queue string, _ *riverlib.QueuePauseOpts) error {
	f.resumed = queue
	return f.err
}

func TestClientPauseAndResumeConfiguredQueue(t *testing.T) {
	t.Parallel()
	control := &queueControlFake{}
	client := &Client{queueCtl: control, queue: "workflow"}
	if err := client.PauseQueue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := client.ResumeQueue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if control.paused != "workflow" || control.resumed != "workflow" {
		t.Fatalf("queue control = pause %q resume %q", control.paused, control.resumed)
	}
}

func TestClientQueueControlFailsClosed(t *testing.T) {
	t.Parallel()
	if err := (&Client{}).PauseQueue(context.Background()); err == nil {
		t.Fatal("PauseQueue succeeded without a controller")
	}
	control := &queueControlFake{err: errors.New("database unavailable")}
	if err := (&Client{queueCtl: control, queue: "workflow"}).ResumeQueue(context.Background()); err == nil {
		t.Fatal("ResumeQueue succeeded after controller failure")
	}
}
