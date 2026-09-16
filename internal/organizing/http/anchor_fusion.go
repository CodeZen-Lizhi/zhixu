package organizinghttp

import (
	"context"
	"net/http"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

type anchorFusionView struct {
	ID           foundation.ID               `json:"id"`
	WorkspaceID  foundation.ID               `json:"workspace_id"`
	AnchorID     foundation.ID               `json:"anchor_id"`
	NoteID       foundation.ID               `json:"note_id"`
	ProposalID   foundation.ID               `json:"proposal_id"`
	ScopeVersion int64                       `json:"scope_version"`
	Sources      []domain.SynthesisSourceRef `json:"sources"`
	Status       string                      `json:"status"`
	ProcessingID *foundation.ID              `json:"processing_id"`
	CreatedAt    time.Time                   `json:"created_at"`
	UpdatedAt    time.Time                   `json:"updated_at"`
}

func fusionView(v app.AnchorFusionRequest, workspace, anchor foundation.ID) (anchorFusionView, error) {
	if !validResponseID(v.ID) || v.WorkspaceID != workspace || v.AnchorID != anchor || !validResponseID(v.NoteID) || !validResponseID(v.ProposalID) || v.ScopeVersion < 1 || len(v.AllowedSources) < 1 || len(v.AllowedSources) > domain.MaxSynthesisSources || v.SourceEvent.Validate() != nil || v.SourceEvent.Source.WorkspaceID != workspace || v.CreatedAt.IsZero() || v.UpdatedAt.Before(v.CreatedAt) {
		return anchorFusionView{}, synthesisInvalidResult()
	}
	var processing *foundation.ID
	switch v.Status {
	case "PENDING", "STALE":
		if v.ProcessingID != "" {
			return anchorFusionView{}, synthesisInvalidResult()
		}
	case "DISPATCHED":
		if !validResponseID(v.ProcessingID) {
			return anchorFusionView{}, synthesisInvalidResult()
		}
		processing = &v.ProcessingID
	default:
		return anchorFusionView{}, synthesisInvalidResult()
	}
	seen := map[foundation.ID]bool{}
	for _, ref := range v.AllowedSources {
		if ref.Validate() != nil || ref.Source != v.SourceEvent.Source || seen[ref.SourceSpanID] {
			return anchorFusionView{}, synthesisInvalidResult()
		}
		seen[ref.SourceSpanID] = true
	}
	return anchorFusionView{ID: v.ID, WorkspaceID: v.WorkspaceID, AnchorID: v.AnchorID, NoteID: v.NoteID, ProposalID: v.ProposalID, ScopeVersion: v.ScopeVersion, Sources: v.AllowedSources, Status: v.Status, ProcessingID: processing, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt}, nil
}
func (h *AnchorHandler) listFusion(w http.ResponseWriter, r *http.Request) {
	workspace, anchor, ok := queryRoute(w, r, "anchor_id")
	if !ok || !h.available(w) {
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, requestInvalid("recent fusion requests do not accept query parameters"))
		return
	}
	reader, ok := h.service.(app.AnchorFusionRequestReader)
	if !ok || nilDependency(reader) {
		writeProblem(w, 503, "ANCHOR_UNAVAILABLE", "融合记录暂不可用", true)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	a, err := h.service.GetAnchor(ctx, workspace, anchor)
	if err != nil {
		writeError(w, err)
		return
	}
	if a.Validate() != nil || a.WorkspaceID != workspace || a.ID != anchor {
		writeError(w, synthesisInvalidResult())
		return
	}
	rows, err := reader.ListAnchorFusionRequests(ctx, workspace, anchor, 20)
	if err != nil {
		writeError(w, err)
		return
	}
	if len(rows) > 20 {
		writeError(w, synthesisInvalidResult())
		return
	}
	out := make([]anchorFusionView, 0, len(rows))
	seen := map[foundation.ID]bool{}
	for _, row := range rows {
		v, err := fusionView(row, workspace, anchor)
		if err != nil {
			writeError(w, err)
			return
		}
		if v.NoteID != a.NoteID || seen[v.ID] {
			writeError(w, synthesisInvalidResult())
			return
		}
		seen[v.ID] = true
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, struct {
		WorkspaceID foundation.ID      `json:"workspace_id"`
		AnchorID    foundation.ID      `json:"anchor_id"`
		Items       []anchorFusionView `json:"items"`
	}{workspace, anchor, out})
}
