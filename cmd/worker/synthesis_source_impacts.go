package main

import (
	"context"
	"log/slog"
	"time"

	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
)

// 可用性观察不调用模型，模型排空期间仍保持运行。
func reconcileSynthesisSourceImpacts(ctx context.Context, logger *slog.Logger, reconciler app.SynthesisSourceImpactReconciler, timeout time.Duration) {
	if reconciler == nil {
		return
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	count, err := reconciler.ReconcileSynthesisSourceImpacts(bounded, 100)
	if err != nil {
		logger.Warn("synthesis source impact reconciliation failed", "error_code", "SYNTHESIS_SOURCE_IMPACT_UNAVAILABLE")
	} else if count > 0 {
		logger.Info("synthesis source impacts recorded", "count", count)
	}
}
