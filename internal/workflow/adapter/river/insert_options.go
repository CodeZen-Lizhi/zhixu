package riveradapter

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	riverlib "github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// InsertOptions contains the bounded per-dispatch transport options exposed by
// the project. Metadata, tags, and arbitrary payload fields are intentionally
// not accepted at this boundary.
type InsertOptions struct {
	ScheduledAt time.Time
	// UniqueStates 覆盖 River 默认唯一状态集合；nil 保留 River 默认语义。
	UniqueStates []rivertype.JobState
}

// JobReceipt is the stable Application-owned result of transactional insertion.
type JobReceipt = application.JobReceipt

func buildInsertOptions(ctx context.Context, queue string, options InsertOptions) (*riverlib.InsertOpts, error) {
	metadata, err := encodeTraceMetadata(ctx)
	if err != nil {
		return nil, err
	}
	uniqueOpts := riverlib.UniqueOpts{ByArgs: true}
	if len(options.UniqueStates) > 0 {
		uniqueOpts.ByState = append([]rivertype.JobState(nil), options.UniqueStates...)
	}
	opts := &riverlib.InsertOpts{Metadata: metadata, Queue: queue, UniqueOpts: uniqueOpts}
	if !options.ScheduledAt.IsZero() {
		opts.ScheduledAt = options.ScheduledAt.UTC()
	}
	return opts, nil
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
