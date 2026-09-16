package main

import (
	"context"
	"log/slog"
	"time"

	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
)

func discoverSynthesisGoals(ctx context.Context, logger *slog.Logger, dispatcher *organizingworkflow.SynthesisGoalCatalogDispatcher, timeout time.Duration) {
	if dispatcher == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	count, err := dispatcher.DispatchBatch(ctx, 4)
	if err != nil {
		code, retryable := captureDispatchFailure(err)
		logger.Warn("synthesis goal catalog discovery failed", "error_code", code, "retryable", retryable)
	} else if count > 0 {
		logger.Info("synthesis goal catalogs advanced", "batches", count)
	}
}

func dispatchGoalSelections(ctx context.Context, logger *slog.Logger, dispatcher *organizingworkflow.GoalSelectionDispatcher, timeout time.Duration) {
	if dispatcher == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	count, err := dispatcher.DispatchBatch(ctx, 8)
	if err != nil {
		code, retryable := captureDispatchFailure(err)
		logger.Warn("synthesis goal selection dispatch failed", "error_code", code, "retryable", retryable)
	} else if count > 0 {
		logger.Info("synthesis goal selections dispatched", "started", count)
	}
}
