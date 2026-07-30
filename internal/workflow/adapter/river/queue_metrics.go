package riveradapter

import (
	"context"
	"errors"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
)

// QueueDepthQuerier 是读取 River 可立即执行队列深度所需的最小数据库边界。
type QueueDepthQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// QueueDepth 返回目标 queue 中 River state=available 的精确数量。
func QueueDepth(ctx context.Context, database QueueDepthQuerier, queue string) (int64, error) {
	queue = strings.TrimSpace(queue)
	if database == nil || queue == "" {
		return 0, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_QUEUE_METRIC_INVALID", false, errors.New("queue metric dependencies are invalid"))
	}
	var depth int64
	if err := database.QueryRow(ctx, `SELECT count(*) FROM workflow.river_job WHERE queue=$1 AND state='available' AND scheduled_at<=CURRENT_TIMESTAMP`, queue).Scan(&depth); err != nil {
		return 0, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_QUEUE_METRIC_QUERY_FAILED", true, err)
	}
	if depth < 0 {
		return 0, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_QUEUE_METRIC_RESULT_INVALID", false, errors.New("queue depth is negative"))
	}
	return depth, nil
}

// RunningJobCount 返回目标 queue 中已经 claim 且仍在执行的 River Job 数量。
func RunningJobCount(ctx context.Context, database QueueDepthQuerier, queue string) (int64, error) {
	queue = strings.TrimSpace(queue)
	if database == nil || queue == "" {
		return 0, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_QUEUE_METRIC_INVALID", false, errors.New("queue metric dependencies are invalid"))
	}
	var count int64
	if err := database.QueryRow(ctx, `SELECT count(*) FROM workflow.river_job WHERE queue=$1 AND state='running'`, queue).Scan(&count); err != nil {
		return 0, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_QUEUE_METRIC_QUERY_FAILED", true, err)
	}
	if count < 0 {
		return 0, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_QUEUE_METRIC_RESULT_INVALID", false, errors.New("running job count is negative"))
	}
	return count, nil
}
