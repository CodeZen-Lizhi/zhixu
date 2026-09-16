package main

import (
	"context"
	"errors"
	"log/slog"

	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
)

// 回填仅调度已有文件。常规 Capture 发件箱负责
// 执行、重试、模型可用性以及对外可见的进度。
func backfillCaptureSources(ctx context.Context, logger *slog.Logger, scheduler captureapp.SourceBackfillScheduler) (int, error) {
	if scheduler == nil {
		return 0, errors.New("capture source backfill is unavailable")
	}
	count, err := scheduler.BackfillSources(ctx, captureDispatchBatchSize)
	if err != nil {
		code, retryable := captureDispatchFailure(err)
		logger.Warn("capture source backfill failed", "error_code", code, "retryable", retryable)
	} else if count > 0 {
		logger.Info("capture source backfill scheduled", "scheduled", count)
	}
	return count, err
}
