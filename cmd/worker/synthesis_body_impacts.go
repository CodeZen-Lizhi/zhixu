package main

import (
	"context"
	"log/slog"
	"time"

	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
)

// 模型执行排空期间，影响观察仍保持运行。
func reconcileSynthesisBodyImpacts(ctx context.Context, logger *slog.Logger, reconciler app.SynthesisBodyImpactReconciler, timeout time.Duration) {
	if reconciler == nil {
		return
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	count, err := reconciler.ReconcileSynthesisBodyImpacts(bounded, app.MaxSynthesisListLimit)
	if err != nil {
		logger.Warn("synthesis body impact reconciliation failed", "error_code", "SYNTHESIS_BODY_IMPACT_UNAVAILABLE")
	} else if count > 0 {
		logger.Info("synthesis body impacts recorded", "count", count)
	}
}
