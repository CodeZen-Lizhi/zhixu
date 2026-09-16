package main

import (
	"context"
	"log/slog"
	"time"

	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
)

// 模型执行排空期间，发布观察仍保持运行。
func reconcileSynthesisPublicationEvents(ctx context.Context, logger *slog.Logger, reconciler app.SynthesisPublicationEventReconciler, timeout time.Duration) {
	if reconciler == nil {
		return
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	count, err := reconciler.ReconcileSynthesisPublicationEvents(bounded, app.MaxSynthesisListLimit)
	if err != nil {
		logger.Warn("synthesis publication event reconciliation failed", "error_code", "SYNTHESIS_PUBLICATION_EVENT_UNAVAILABLE")
	} else if count > 0 {
		logger.Info("synthesis publication events recorded", "count", count)
	}
}
