package main

import (
	"context"
	"log/slog"
	"time"

	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
)

func reconcileSynthesisBodyRefreshRequests(ctx context.Context, logger *slog.Logger, reconciler app.SynthesisBodyRefreshReconciler, timeout time.Duration) {
	if reconciler == nil {
		return
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	count, err := reconciler.ReconcileSynthesisBodyRefreshRequests(bounded, app.MaxSynthesisListLimit)
	if err != nil {
		logger.Warn("synthesis body refresh request reconciliation failed", "error_code", "SYNTHESIS_BODY_REFRESH_UNAVAILABLE")
	} else if count > 0 {
		logger.Info("synthesis body refresh requests recorded", "count", count)
	}
}
