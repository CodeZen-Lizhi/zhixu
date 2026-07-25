package application

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const (
	// MaxTimelineProjectionBatch 限制单次 Worker 循环处理的 Timeline 投影数量。
	MaxTimelineProjectionBatch = 100
)

// TimelineProjectionOutcome 是一条可信投影源的持久处理结果。
type TimelineProjectionOutcome string

const (
	// TimelineProjectionProjected 表示新 Knowledge Event 已落库。
	TimelineProjectionProjected TimelineProjectionOutcome = "PROJECTED"
	// TimelineProjectionReplayed 表示源事件已精确投影，当前处理为恢复重放。
	TimelineProjectionReplayed TimelineProjectionOutcome = "REPLAYED"
	// TimelineProjectionPoisoned 表示源绑定漂移，已停止自动重试并等待人工恢复。
	TimelineProjectionPoisoned TimelineProjectionOutcome = "POISONED"
)

// TimelineProjectionResult 描述一次短事务的持久结果。
type TimelineProjectionResult struct {
	SourceID foundation.ID
	EventID  foundation.ID
	Outcome  TimelineProjectionOutcome
}

// TimelineProjectionBatchResult 汇总一次有界投影循环。
type TimelineProjectionBatchResult struct {
	Processed int
	Projected int
	Replayed  int
	Poisoned  int
}

// TimelineProjectionPort 每次在独立短事务内领取并处理一条可信投影源。
type TimelineProjectionPort interface {
	ProjectNext(context.Context) (TimelineProjectionResult, bool, error)
}

// TimelineProjectionDispatcher 提供有界批量循环，不拥有数据库事务。
type TimelineProjectionDispatcher struct {
	port TimelineProjectionPort
}

// NewTimelineProjectionDispatcher 构造 Timeline 投影调度器。
func NewTimelineProjectionDispatcher(port TimelineProjectionPort) (*TimelineProjectionDispatcher, error) {
	if isNil(port) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeTimelineUnavailable, true, errors.New("timeline projection port is unavailable"))
	}
	return &TimelineProjectionDispatcher{port: port}, nil
}

// DispatchBatch 最多处理 limit 条投影源；每条源由 Adapter 独立提交或回滚。
func (dispatcher *TimelineProjectionDispatcher) DispatchBatch(ctx context.Context, limit int) (TimelineProjectionBatchResult, error) {
	if dispatcher == nil || isNil(dispatcher.port) {
		return TimelineProjectionBatchResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeTimelineUnavailable, true, errors.New("timeline projection dispatcher is unavailable"))
	}
	if ctx == nil || limit < 1 || limit > MaxTimelineProjectionBatch {
		return TimelineProjectionBatchResult{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeTimelineInvalid, false, errors.New("timeline projection batch request is invalid"))
	}
	var batch TimelineProjectionBatchResult
	for batch.Processed < limit {
		result, found, err := dispatcher.port.ProjectNext(ctx)
		if found {
			batch.Processed++
			batch.add(result.Outcome)
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

func (result *TimelineProjectionBatchResult) add(outcome TimelineProjectionOutcome) {
	switch outcome {
	case TimelineProjectionProjected:
		result.Projected++
	case TimelineProjectionReplayed:
		result.Replayed++
	case TimelineProjectionPoisoned:
		result.Poisoned++
	}
}
