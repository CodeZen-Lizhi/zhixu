package river

import (
	"context"
	"encoding/json"
	"errors"

	exportapp "github.com/CodeZen-Lizhi/zhixu/internal/export/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	workflowriver "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	riverlib "github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// Dispatcher 在 Export 事实提交后以 Args 唯一键投递 River Job。
type Dispatcher struct{ client *workflowriver.Client }

var _ exportapp.Dispatcher = (*Dispatcher)(nil)

// NewDispatcher 创建 insert-only Export 投递器。
func NewDispatcher(client *workflowriver.Client) (*Dispatcher, error) {
	if client == nil || client.Queue() == "" {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "EXPORT_RIVER_DISPATCHER_UNAVAILABLE", true, errors.New("export River client is unavailable"))
	}
	return &Dispatcher{client: client}, nil
}

// Dispatch 投递一个只含 Workspace/Export identity 的唯一 River Job。
func (dispatcher *Dispatcher) Dispatch(ctx context.Context, workspaceID, exportID foundation.ID) error {
	if dispatcher == nil || dispatcher.client == nil {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, "EXPORT_RIVER_DISPATCHER_UNAVAILABLE", true, errors.New("export River dispatcher is unavailable"))
	}
	args, err := NewArgs(workspaceID, exportID)
	if err != nil {
		return err
	}
	metadata, err := encodeTraceMetadata(ctx)
	if err != nil {
		return err
	}
	result, err := dispatcher.client.Insert(ctx, args, &riverlib.InsertOpts{
		Queue: dispatcher.client.Queue(), Metadata: metadata, UniqueOpts: exportUniqueOpts(),
	})
	if err != nil {
		return err
	}
	if result == nil || result.Job == nil || result.Job.ID < 1 {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "EXPORT_RIVER_DISPATCH_RESULT_INVALID", false, errors.New("export River insert returned no persisted job"))
	}
	return nil
}

func exportUniqueOpts() riverlib.UniqueOpts {
	return riverlib.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
		rivertype.JobStateAvailable,
		rivertype.JobStatePending,
		rivertype.JobStateRunning,
		rivertype.JobStateScheduled,
		rivertype.JobStateRetryable,
	}}
}

func encodeTraceMetadata(ctx context.Context) ([]byte, error) {
	metadata := observability.EncodeTraceMetadata(ctx)
	if len(metadata) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, foundation.NewError(foundation.ErrorConsistencyViolation, "EXPORT_RIVER_TRACE_METADATA_INVALID", false, err)
	}
	return encoded, nil
}
