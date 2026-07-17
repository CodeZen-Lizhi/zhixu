package riveradapter

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/jackc/pgx/v5"
	riverlib "github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// InsertOptions contains the only per-dispatch transport option exposed by the
// project. Metadata, tags, and arbitrary payload fields are intentionally not
// accepted at this boundary.
type InsertOptions struct {
	ScheduledAt time.Time
}

// JobReceipt is the stable Application-owned result of transactional insertion.
type JobReceipt = application.JobReceipt

// JobInserter is the reusable tx-scoped job insertion port. The transaction is
// opaque outside this adapter so Domain and Application do not import pgx.
type JobInserter interface {
	InsertTx(context.Context, any, NodeJobArgs, InsertOptions) (JobReceipt, error)
}

type riverInsertClient interface {
	InsertTx(context.Context, pgx.Tx, riverlib.JobArgs, *riverlib.InsertOpts) (*rivertype.JobInsertResult, error)
}

// RiverJobInserter implements JobInserter with River v0.40's InsertTx.
type RiverJobInserter struct {
	client riverInsertClient
	queue  string
}

var _ JobInserter = (*RiverJobInserter)(nil)

// NewJobInserter constructs a transactional inserter from a schema-scoped client.
func NewJobInserter(client *Client) (JobInserter, error) {
	if client == nil || client.insert == nil {
		return nil, jobError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_CLIENT_MISSING", errors.New("River client is nil"))
	}
	return &RiverJobInserter{client: client.insert, queue: client.Queue()}, nil
}

// InsertTx inserts one uniquely identified node dispatch in the caller's
// transaction. A unique skip is returned as the existing job receipt.
func (i *RiverJobInserter) InsertTx(ctx context.Context, transaction any, args NodeJobArgs, options InsertOptions) (JobReceipt, error) {
	if err := ValidateNodeJobArgs(args); err != nil {
		return JobReceipt{}, err
	}
	if i == nil || i.client == nil {
		return JobReceipt{}, jobError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_INSERTER_MISSING", errors.New("River job inserter is nil"))
	}
	tx, ok := transaction.(pgx.Tx)
	if !ok || tx == nil {
		return JobReceipt{}, jobError(foundation.ErrorInvalidInput, "WORKFLOW_RIVER_TRANSACTION_INVALID", errors.New("transaction is not a pgx transaction"))
	}
	metadata, err := encodeTraceMetadata(ctx)
	if err != nil {
		return JobReceipt{}, err
	}
	opts := &riverlib.InsertOpts{Metadata: metadata, Queue: i.queue, UniqueOpts: riverlib.UniqueOpts{ByArgs: true}}
	if !options.ScheduledAt.IsZero() {
		opts.ScheduledAt = options.ScheduledAt.UTC()
	}
	result, err := i.client.InsertTx(ctx, tx, args, opts)
	if err != nil {
		return JobReceipt{}, foundation.NewError(foundation.ErrorRetryableFailure, "WORKFLOW_RIVER_JOB_INSERT_FAILED", true, err)
	}
	if result == nil || result.Job == nil || result.Job.ID < 1 {
		return JobReceipt{}, jobError(foundation.ErrorConsistencyViolation, "WORKFLOW_RIVER_JOB_RESULT_INVALID", errors.New("River insert returned no persisted job"))
	}
	return JobReceipt{JobID: result.Job.ID, Duplicate: result.UniqueSkippedAsDuplicate}, nil
}

func encodeTraceMetadata(ctx context.Context) ([]byte, error) {
	metadata := observability.EncodeTraceMetadata(ctx)
	if len(metadata) == 0 {
		return nil, nil
	}
	if len(metadata) != 1 {
		return nil, jobError(foundation.ErrorConsistencyViolation, "WORKFLOW_RIVER_TRACE_METADATA_INVALID", errors.New("trace metadata contains unexpected fields"))
	}
	if _, ok := metadata[observability.TraceParentMetadataKey]; !ok {
		return nil, jobError(foundation.ErrorConsistencyViolation, "WORKFLOW_RIVER_TRACE_METADATA_INVALID", errors.New("trace metadata does not contain traceparent"))
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, jobError(foundation.ErrorConsistencyViolation, "WORKFLOW_RIVER_TRACE_METADATA_INVALID", err)
	}
	return encoded, nil
}
