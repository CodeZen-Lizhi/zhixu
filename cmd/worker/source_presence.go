package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/filesystem"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

type localSourcePresenceRepository interface {
	domain.ActiveWorkspaceRepository
	domain.SourcePresenceRepository
}

// 仅 Worker 的串行生产者循环拥有此游标。越过
// 仍存在的路径，可防止前面的分页阻塞后续缺失文件。
type localSourcePresenceReconciler struct {
	repository localSourcePresenceRepository
	workspace  foundation.ID
	after      foundation.ID
}

func reconcileLocalSourcePresence(ctx context.Context, logger *slog.Logger, reconciler *localSourcePresenceReconciler, timeout time.Duration) {
	if reconciler == nil || reconciler.repository == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	workspace, err := reconciler.repository.GetActiveWorkspace(ctx)
	if err != nil {
		code, retryable := captureDispatchFailure(err)
		logger.Warn("local source presence workspace unavailable", "error_code", code, "retryable", retryable)
		return
	}
	if reconciler.workspace != workspace.ID {
		reconciler.workspace = workspace.ID
		reconciler.after = ""
	}
	const batchSize = 100
	page, err := reconciler.repository.ReconcileLocalSourcePresence(ctx, workspace.ID, reconciler.after, batchSize, filesystem.Scanner{})
	if page.After != "" {
		reconciler.after = page.After
	}
	if err != nil {
		code, retryable := captureDispatchFailure(err)
		logger.Warn("local source presence check failed", "error_code", code, "retryable", retryable)
		return
	}
	if page.Checked < batchSize {
		reconciler.after = ""
	}
	if page.Removed > 0 {
		logger.Info("local source absence detected", "removed", page.Removed)
	}
}
