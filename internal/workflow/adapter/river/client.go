package riveradapter

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	riverlib "github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

// Client owns the River client configured for the workflow schema. Production
// process lifecycle wiring remains the responsibility of M4-D.
type Client struct {
	inner  *riverlib.Client[pgx.Tx]
	schema string
}

// NewClient constructs either an insert-only client (workers is nil) or a
// one-worker runtime client. In both cases all SQL is explicitly schema scoped.
func NewClient(pool *pgxpool.Pool, workers *Workers) (*Client, error) {
	if pool == nil {
		return nil, jobError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_POOL_MISSING", errors.New("PostgreSQL pool is nil"))
	}
	config := &riverlib.Config{Schema: WorkflowSchema}
	if workers != nil {
		innerWorkers, err := workers.riverWorkers()
		if err != nil {
			return nil, err
		}
		config.Workers = innerWorkers
		config.Queues = map[string]riverlib.QueueConfig{
			riverlib.QueueDefault: {MaxWorkers: 1},
		}
	}
	inner, err := riverlib.NewClient(riverpgxv5.New(pool), config)
	if err != nil {
		return nil, jobError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_CLIENT_INVALID", err)
	}
	return &Client{inner: inner, schema: WorkflowSchema}, nil
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
	if c == nil || c.inner == nil {
		return jobError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_CLIENT_MISSING", errors.New("River client is nil"))
	}
	if err := c.inner.Start(ctx); err != nil {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_CLIENT_START_FAILED", true, err)
	}
	return nil
}

// Stop gracefully stops a started River client.
func (c *Client) Stop(ctx context.Context) error {
	if c == nil || c.inner == nil {
		return jobError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_CLIENT_MISSING", errors.New("River client is nil"))
	}
	if err := c.inner.Stop(ctx); err != nil {
		return foundation.NewError(foundation.ErrorRetryableFailure, "WORKFLOW_RIVER_CLIENT_STOP_FAILED", true, err)
	}
	return nil
}

// Stopped is closed after the underlying River client has fully stopped.
func (c *Client) Stopped() <-chan struct{} {
	if c == nil || c.inner == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return c.inner.Stopped()
}
