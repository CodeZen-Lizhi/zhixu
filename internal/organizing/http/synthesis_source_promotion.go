package organizinghttp

import (
	"context"
	"net/http"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
)

type SynthesisSourcePromoter interface {
	PromoteSynthesisSource(context.Context, app.PromoteSynthesisSourceCommand) (app.SynthesisSourcePromotion, error)
}

func (h *SynthesisHandler) promoteSource(w http.ResponseWriter, r *http.Request) {
	workspace, source, key, ok := identifiedCommandRoute(w, r, "source_version_id")
	if !ok {
		return
	}
	if _, err := decodeJSON[struct{}](r, 1024, 128, 0); err != nil {
		writeError(w, err)
		return
	}
	if !h.available(w) {
		return
	}
	if nilDependency(h.sourcePromoter) {
		writeProblem(w, http.StatusServiceUnavailable, "SYNTHESIS_SOURCE_PROMOTION_UNAVAILABLE", "主笔记创建暂不可用", true)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	result, err := h.sourcePromoter.PromoteSynthesisSource(ctx, app.PromoteSynthesisSourceCommand{WorkspaceID: workspace, SourceVersionID: source, IdempotencyKey: key})
	if err != nil {
		writeError(w, err)
		return
	}
	request, err := toSynthesisGoalRequest(result.Request, workspace)
	if err != nil || result.SourceVersionID != source || result.Request.Status != app.SynthesisGoalCatalogReady || result.Request.CatalogBatches != 1 {
		writeError(w, synthesisInvalidResult())
		return
	}
	writeJSON(w, http.StatusAccepted, struct {
		SourceVersionID foundation.ID                `json:"source_version_id"`
		Request         synthesisGoalRequestResponse `json:"request"`
		Replayed        bool                         `json:"replayed"`
	}{source, request, result.Replayed})
}
