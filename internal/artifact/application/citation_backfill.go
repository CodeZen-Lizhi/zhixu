package application

import (
	"context"
	"errors"
	"reflect"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// MaxCitationBackfillWorkspaceBatch 限制单次 Worker 循环领取的 Workspace 数量。
	MaxCitationBackfillWorkspaceBatch = 25
	// MaxCitationBackfillRevisionBatch 限制单个 Workspace 短事务处理的 Revision 数量。
	MaxCitationBackfillRevisionBatch = 100
)

// CitationBackfillResult 描述一个 Workspace 的 selector 回填或复核进展。
type CitationBackfillResult struct {
	WorkspaceID        foundation.ID
	ProcessedRevisions int
	ProcessedSelectors int
	ValidatedRevisions int
	Completed          bool
}

// CitationBackfillBatchResult 汇总一次有界 Worker 循环。
type CitationBackfillBatchResult struct {
	Workspaces         int
	ProcessedRevisions int
	ProcessedSelectors int
	ValidatedRevisions int
	Completed          int
}

// CitationBackfillPort 每次在一个短事务内推进一个 Workspace 的历史 selector 回填。
type CitationBackfillPort interface {
	BackfillCitationSelectors(context.Context, int) (CitationBackfillResult, bool, error)
}

// CitationBackfillDispatcher 提供有界 Workspace 循环，不拥有数据库事务。
type CitationBackfillDispatcher struct {
	port CitationBackfillPort
}

// NewCitationBackfillDispatcher 构造 Artifact citation selector 回填调度器。
func NewCitationBackfillDispatcher(port CitationBackfillPort) (*CitationBackfillDispatcher, error) {
	if citationBackfillPortNil(port) {
		return nil, unavailable("artifact citation backfill port is unavailable")
	}
	return &CitationBackfillDispatcher{port: port}, nil
}

// DispatchBatch 最多推进 workspaceLimit 个 Workspace，每个事务最多处理 revisionLimit 条 Revision。
func (dispatcher *CitationBackfillDispatcher) DispatchBatch(ctx context.Context, workspaceLimit, revisionLimit int) (CitationBackfillBatchResult, error) {
	if dispatcher == nil || citationBackfillPortNil(dispatcher.port) {
		return CitationBackfillBatchResult{}, unavailable("artifact citation backfill dispatcher is unavailable")
	}
	if ctx == nil || workspaceLimit < 1 || workspaceLimit > MaxCitationBackfillWorkspaceBatch || revisionLimit < 1 || revisionLimit > MaxCitationBackfillRevisionBatch {
		return CitationBackfillBatchResult{}, requestInvalid("artifact citation backfill batch request is invalid")
	}
	var batch CitationBackfillBatchResult
	for batch.Workspaces < workspaceLimit {
		result, found, err := dispatcher.port.BackfillCitationSelectors(ctx, revisionLimit)
		if found {
			batch.Workspaces++
			batch.ProcessedRevisions += result.ProcessedRevisions
			batch.ProcessedSelectors += result.ProcessedSelectors
			batch.ValidatedRevisions += result.ValidatedRevisions
			if result.Completed {
				batch.Completed++
			}
		}
		if err != nil {
			return batch, err
		}
		if !found {
			break
		}
	}
	return batch, nil
}

func citationBackfillPortNil(value any) bool {
	if value == nil {
		return true
	}
	kind := reflect.TypeOf(value).Kind()
	return (kind == reflect.Chan || kind == reflect.Func || kind == reflect.Interface || kind == reflect.Map || kind == reflect.Pointer || kind == reflect.Slice) && reflect.ValueOf(value).IsNil()
}

// NewCitationBackfillFailure 返回已持久化 FAILED marker 的非重试数据错误。
func NewCitationBackfillFailure(cause error) error {
	if cause == nil {
		cause = errors.New("artifact citation selector backfill failed")
	}
	return foundation.NewError(foundation.ErrorManualRecoveryRequired, ErrorCodeCitationBackfillFailed, false, cause)
}
