package riveradapter

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"sync/atomic"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/operability"
	"github.com/jackc/pgx/v5/pgxpool"
	riverlib "github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

const (
	defaultMaxWorkers           = 1
	defaultRescueStuckJobsAfter = time.Hour
	defaultSoftStopTimeout      = 30 * time.Second
)

type clientLifecycle interface {
	Start(context.Context) error
	Stop(context.Context) error
	StopAndCancel(context.Context) error
	Stopped() <-chan struct{}
}

type queueControlClient interface {
	QueuePause(context.Context, string, *riverlib.QueuePauseOpts) error
	QueueResume(context.Context, string, *riverlib.QueuePauseOpts) error
}

// Client owns the River client configured for the workflow schema. Production
// process lifecycle wiring remains the responsibility of cmd/worker.
type Client struct {
	generation atomic.Uint64
	insert     riverInsertClient
	lifecycle  clientLifecycle
	queueCtl   queueControlClient
	fence      EnqueueFence
	queue      string
	schema     string
	started    atomic.Bool
}

// DefaultOptions preserves the existing default queue, concurrency, job
// timeout, and rescue cadence while adding a finite soft-stop boundary.
// Production composition may override them through NewClientWithOptions.
func DefaultOptions() Options {
	return Options{
		JobTimeout:           riverlib.JobTimeoutDefault,
		MaxWorkers:           defaultMaxWorkers,
		Queue:                riverlib.QueueDefault,
		RescueStuckJobsAfter: defaultRescueStuckJobsAfter,
		SoftStopTimeout:      defaultSoftStopTimeout,
	}
}

// NewClient constructs either an insert-only client (workers is nil) or a
// runtime client using safe defaults. All SQL is explicitly schema scoped.
func NewClient(pool *pgxpool.Pool, workers *Workers) (*Client, error) {
	return NewClientWithOptions(pool, workers, DefaultOptions())
}

// NewClientWithOptions constructs a schema-scoped River client using validated
// project-owned operability settings.
func NewClientWithOptions(pool *pgxpool.Pool, workers *Workers, options Options) (*Client, error) {
	if pool == nil {
		return nil, jobError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_POOL_MISSING", errors.New("PostgreSQL pool is nil"))
	}
	config, err := buildRiverConfig(workers, options)
	if err != nil {
		return nil, err
	}
	inner, err := riverlib.NewClient(riverpgxv5.New(pool), config)
	if err != nil {
		return nil, jobError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_CLIENT_INVALID", err)
	}
	return &Client{insert: inner, lifecycle: inner, queueCtl: inner, fence: options.EnqueueFence, queue: options.Queue, schema: WorkflowSchema}, nil
}

// Queue returns the queue shared by this client's producers and consumers.
func (c *Client) Queue() string {
	if c == nil {
		return ""
	}
	return c.queue
}

// Schema returns the explicitly configured River PostgreSQL schema.
func (c *Client) Schema() string {
	if c == nil {
		return ""
	}
	return c.schema
}

// Start starts River's delivery runtime. The adapter only exposes the lifecycle
// boundary; cmd/worker owns production start, signal handling, and shutdown.
func (c *Client) Start(ctx context.Context) error {
	if c == nil || c.lifecycle == nil {
		return jobError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_CLIENT_MISSING", errors.New("River client is nil"))
	}
	if err := c.lifecycle.Start(ctx); err != nil {
		c.started.Store(false)
		return foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_CLIENT_START_FAILED", true, err)
	}
	generation := c.generation.Add(1)
	c.started.Store(true)
	if stopped := c.lifecycle.Stopped(); stopped != nil {
		go func() {
			<-stopped
			if c.generation.Load() == generation {
				c.started.Store(false)
			}
		}()
	}
	return nil
}

// Started reports whether the most recent successful Start is still running.
func (c *Client) Started() bool {
	return c != nil && c.started.Load()
}

// Stop gracefully stops a started River client.
func (c *Client) Stop(ctx context.Context) error {
	if c == nil || c.lifecycle == nil {
		return jobError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_CLIENT_MISSING", errors.New("River client is nil"))
	}
	c.generation.Add(1)
	c.started.Store(false)
	if err := c.lifecycle.Stop(ctx); err != nil {
		return foundation.NewError(foundation.ErrorRetryableFailure, "WORKFLOW_RIVER_CLIENT_STOP_FAILED", true, err)
	}
	return nil
}

// StopAndCancel immediately cancels active job contexts and waits for River to
// stop. cmd/worker restricts this boundary to fatal invariant shutdowns.
func (c *Client) StopAndCancel(ctx context.Context) error {
	if c == nil || c.lifecycle == nil {
		return jobError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_CLIENT_MISSING", errors.New("River client is nil"))
	}
	c.generation.Add(1)
	c.started.Store(false)
	if err := c.lifecycle.StopAndCancel(ctx); err != nil {
		return foundation.NewError(foundation.ErrorRetryableFailure, "WORKFLOW_RIVER_CLIENT_STOP_AND_CANCEL_FAILED", true, err)
	}
	return nil
}

// Stopped is closed after the underlying River client has fully stopped.
func (c *Client) Stopped() <-chan struct{} {
	if c == nil || c.lifecycle == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return c.lifecycle.Stopped()
}

// PauseQueue 持久暂停当前配置队列的新 Job claim；已运行 Job 不会被取消。
func (c *Client) PauseQueue(ctx context.Context) error {
	if c == nil || c.queueCtl == nil || c.queue == "" {
		return jobError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_QUEUE_CONTROL_MISSING", errors.New("River queue control is unavailable"))
	}
	if err := c.queueCtl.QueuePause(ctx, c.queue, nil); err != nil {
		return foundation.NewError(foundation.ErrorRetryableFailure, "WORKFLOW_RIVER_QUEUE_PAUSE_FAILED", true, err)
	}
	return nil
}

// ResumeQueue 恢复当前配置队列的新 Job claim。
func (c *Client) ResumeQueue(ctx context.Context) error {
	if c == nil || c.queueCtl == nil || c.queue == "" {
		return jobError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_QUEUE_CONTROL_MISSING", errors.New("River queue control is unavailable"))
	}
	if err := c.queueCtl.QueueResume(ctx, c.queue, nil); err != nil {
		return foundation.NewError(foundation.ErrorRetryableFailure, "WORKFLOW_RIVER_QUEUE_RESUME_FAILED", true, err)
	}
	return nil
}

// Options contains the project-owned River runtime tuning seam. It deliberately
// excludes River-specific types from workflow domain and application packages.
type Options struct {
	JobTimeout           time.Duration
	MaxWorkers           int
	Queue                string
	RescueStuckJobsAfter time.Duration
	SoftStopTimeout      time.Duration
	Logger               *slog.Logger
	// EnqueueFence 在同一 PostgreSQL transaction 中阻止 rollout drain 后的新入队。
	EnqueueFence EnqueueFence
}

func buildRiverConfig(workers *Workers, options Options) (*riverlib.Config, error) {
	if err := validateOptions(options); err != nil {
		return nil, err
	}
	config := &riverlib.Config{
		JobTimeout:           options.JobTimeout,
		Logger:               options.Logger,
		RescueStuckJobsAfter: options.RescueStuckJobsAfter,
		Schema:               WorkflowSchema,
		SoftStopTimeout:      options.SoftStopTimeout,
	}
	if workers == nil {
		return config, nil
	}
	innerWorkers, err := workers.riverWorkers()
	if err != nil {
		return nil, err
	}
	config.Queues = map[string]riverlib.QueueConfig{
		options.Queue: {MaxWorkers: options.MaxWorkers},
	}
	config.Workers = innerWorkers
	return config, nil
}

func validateOptions(options Options) error {
	if err := operability.ValidateRiverOptions(options.Queue, options.MaxWorkers, options.JobTimeout, options.RescueStuckJobsAfter, options.SoftStopTimeout); err != nil {
		return optionsError(err)
	}
	if options.EnqueueFence != nil {
		value := reflect.ValueOf(options.EnqueueFence)
		if value.Kind() == reflect.Pointer && value.IsNil() {
			return optionsError(errors.New("enqueue fence is nil"))
		}
	}
	return nil
}

func optionsError(cause error) error {
	return jobError(foundation.ErrorInvalidInput, "WORKFLOW_RIVER_OPTIONS_INVALID", cause)
}
