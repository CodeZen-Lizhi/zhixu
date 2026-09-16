package organizinghttp

import (
	"context"
	"net/http"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"github.com/gin-gonic/gin"
)

type anchorSuggestionView struct {
	Title    string                      `json:"title"`
	Kind     string                      `json:"kind"`
	Scope    *domain.AnchorScope         `json:"scope"`
	Reason   string                      `json:"reason"`
	Evidence []domain.SynthesisSourceRef `json:"evidence"`
}
type anchorRecommendationView struct {
	ID                   foundation.ID                     `json:"id"`
	WorkspaceID          foundation.ID                     `json:"workspace_id"`
	NoteID               foundation.ID                     `json:"note_id"`
	AnchorID             *foundation.ID                    `json:"anchor_id"`
	BasisRevisionID      foundation.ID                     `json:"basis_revision_id"`
	ExpectedScopeVersion *int64                            `json:"expected_scope_version"`
	Kind                 string                            `json:"kind"`
	Status               domain.AnchorRecommendationStatus `json:"status"`
	Recommendation       *anchorSuggestionView             `json:"recommendation"`
	ProposalID           *foundation.ID                    `json:"proposal_id"`
	ErrorCode            *string                           `json:"error_code"`
	Retryable            bool                              `json:"retryable"`
	Version              int64                             `json:"version"`
	CreatedAt            time.Time                         `json:"created_at"`
	UpdatedAt            time.Time                         `json:"updated_at"`
}

func projectAnchorRecommendation(request app.AnchorRecommendationRequest) (anchorRecommendationView, error) {
	view := anchorRecommendationView{ID: request.ID, WorkspaceID: request.WorkspaceID, NoteID: request.NoteID, BasisRevisionID: request.BasisRevisionID, Kind: request.Kind, Status: request.Status, Retryable: request.Retryable, Version: request.Version, CreatedAt: request.CreatedAt, UpdatedAt: request.UpdatedAt}
	invalid := func() (anchorRecommendationView, error) { return anchorRecommendationView{}, synthesisInvalidResult() }
	if _, err := app.BindAnchorModelOutput([]byte(`{"recommendation":null,"no_recommendation":true}`), request); err != nil {
		return invalid()
	}
	if request.Version < 1 || request.CreatedAt.IsZero() || request.UpdatedAt.Before(request.CreatedAt) {
		return invalid()
	}
	if request.AnchorID != "" {
		id, version := request.AnchorID, request.ExpectedScopeVersion
		view.AnchorID, view.ExpectedScopeVersion = &id, &version
	}
	if request.ProposalID != "" {
		if _, err := parseID(string(request.ProposalID)); err != nil {
			return invalid()
		}
		id := request.ProposalID
		view.ProposalID = &id
	}
	if request.ErrorCode != "" {
		if (domain.SynthesisFailure{Code: request.ErrorCode}).Validate() != nil {
			return invalid()
		}
		code := request.ErrorCode
		view.ErrorCode = &code
	}
	switch request.Status {
	case domain.AnchorRecommendationPending, domain.AnchorRecommendationRunning:
		if len(request.ModelOutput) != 0 || request.ModelRunID != "" || view.ProposalID != nil || view.ErrorCode != nil || request.Retryable {
			return invalid()
		}
	case domain.AnchorRecommendationSucceeded, domain.AnchorRecommendationNoRecommendation:
		if _, err := parseID(string(request.ModelRunID)); err != nil || view.ErrorCode != nil || request.Retryable {
			return invalid()
		}
		output, err := app.BindAnchorModelOutput(request.ModelOutput, request)
		if err != nil || output.NoRecommendation != (request.Status == domain.AnchorRecommendationNoRecommendation) {
			return invalid()
		}
		if output.Recommendation != nil {
			r := output.Recommendation
			view.Recommendation = &anchorSuggestionView{Title: r.Title, Kind: r.Kind, Scope: r.Scope, Reason: r.Reason, Evidence: r.Evidence}
		}
		if request.Status == domain.AnchorRecommendationNoRecommendation && view.ProposalID != nil || request.Status == domain.AnchorRecommendationSucceeded && (request.Kind == app.AnchorInitialScopeRecommendation) != (view.ProposalID == nil) {
			return invalid()
		}
	case domain.AnchorRecommendationFailed, domain.AnchorRecommendationRecoveryRequired:
		if view.ErrorCode == nil || view.ProposalID != nil || request.Status == domain.AnchorRecommendationRecoveryRequired && request.Retryable {
			return invalid()
		}
	default:
		return invalid()
	}
	return view, nil
}

func (h *AnchorHandler) recommendationRoutes(router gin.IRouter) {
	base := "/workspaces/:workspace_id/synthesis/anchor-recommendations"
	router.POST(base, httpapi.GinHandler(h.requestRecommendation))
	router.GET(base, httpapi.GinHandler(h.listRecommendations))
	router.GET(base+"/:request_id", httpapi.GinHandler(h.getRecommendation))
	router.POST(base+"/:request_id/retry", httpapi.GinHandler(h.retryRecommendation))
}
func (h *AnchorHandler) recommendationService(w http.ResponseWriter) (app.AnchorRecommendationService, bool) {
	if !h.available(w) {
		return nil, false
	}
	service, ok := h.service.(app.AnchorRecommendationService)
	if !ok || nilDependency(service) {
		writeProblem(w, http.StatusServiceUnavailable, "ANCHOR_RECOMMENDATION_UNAVAILABLE", "范围分析服务暂不可用", true)
		return nil, false
	}
	return service, true
}
func (h *AnchorHandler) requestRecommendation(w http.ResponseWriter, r *http.Request) {
	workspace, key, ok := commandRoute(w, r)
	if !ok {
		return
	}
	input, err := decodeJSON[app.RequestInitialAnchorRecommendationCommand](r, 4096, 256, 8)
	if err != nil {
		writeError(w, err)
		return
	}
	input.WorkspaceID, input.IdempotencyKey = workspace, key
	if err := input.Validate(); err != nil {
		writeError(w, err)
		return
	}
	service, ok := h.recommendationService(w)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	result, err := service.RequestInitialAnchorRecommendation(ctx, input)
	if err != nil {
		writeError(w, err)
		return
	}
	if result.WorkspaceID != workspace || result.NoteID != input.NoteID || result.BasisRevisionID != input.BasisRevisionID || result.Kind != app.AnchorInitialScopeRecommendation {
		writeError(w, synthesisInvalidResult())
		return
	}
	view, err := projectAnchorRecommendation(result)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, view)
}
func (h *AnchorHandler) getRecommendation(w http.ResponseWriter, r *http.Request) {
	workspace, id, ok := queryRoute(w, r, "request_id")
	if !ok {
		return
	}
	service, ok := h.recommendationService(w)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	result, err := service.GetAnchorRecommendation(ctx, workspace, id)
	if err != nil {
		writeError(w, err)
		return
	}
	if result.WorkspaceID != workspace || result.ID != id {
		writeError(w, synthesisInvalidResult())
		return
	}
	view, err := projectAnchorRecommendation(result)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}
func (h *AnchorHandler) listRecommendations(w http.ResponseWriter, r *http.Request) {
	parsed, err := parseAnchorList(r, true)
	if err != nil {
		writeError(w, err)
		return
	}
	service, ok := h.recommendationService(w)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	result, err := service.ListAnchorRecommendations(ctx, app.AnchorRecommendationListQuery{WorkspaceID: parsed.WorkspaceID, NoteID: parsed.NoteID, AfterID: parsed.AfterID, Limit: parsed.Limit})
	if err != nil {
		writeError(w, err)
		return
	}
	if result.Items == nil || len(result.Items) > parsed.Limit {
		writeError(w, synthesisInvalidResult())
		return
	}
	items := make([]anchorRecommendationView, 0, len(result.Items))
	last := parsed.AfterID
	for _, request := range result.Items {
		if request.WorkspaceID != parsed.WorkspaceID || parsed.NoteID != "" && request.NoteID != parsed.NoteID || request.ID <= last {
			writeError(w, synthesisInvalidResult())
			return
		}
		view, err := projectAnchorRecommendation(request)
		if err != nil {
			writeError(w, err)
			return
		}
		items = append(items, view)
		last = request.ID
	}
	if !validAnchorNext(result.NextAfterID, last, len(items), parsed.Limit) {
		writeError(w, synthesisInvalidResult())
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Items       []anchorRecommendationView `json:"items"`
		NextAfterID *foundation.ID             `json:"next_after_id"`
	}{items, result.NextAfterID})
}
func (h *AnchorHandler) retryRecommendation(w http.ResponseWriter, r *http.Request) {
	workspace, key, ok := commandRoute(w, r)
	if !ok {
		return
	}
	id, err := parseID(r.PathValue("request_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	input, err := decodeJSON[struct {
		ExpectedVersion int64 `json:"expected_version"`
	}](r, 1024, 128, 4)
	if err != nil {
		writeError(w, err)
		return
	}
	command := app.RetryAnchorRecommendationCommand{WorkspaceID: workspace, RequestID: id, ExpectedVersion: input.ExpectedVersion, IdempotencyKey: key}
	if err := command.Validate(); err != nil {
		writeError(w, err)
		return
	}
	service, ok := h.recommendationService(w)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	result, err := service.RetryAnchorRecommendation(ctx, command)
	if err != nil {
		writeError(w, err)
		return
	}
	if result.ID != id || result.WorkspaceID != workspace {
		writeError(w, synthesisInvalidResult())
		return
	}
	view, err := projectAnchorRecommendation(result)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, view)
}
