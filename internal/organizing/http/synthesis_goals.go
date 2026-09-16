package organizinghttp

import (
	"context"
	"net/http"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
)

type SynthesisGoalCreator interface {
	CreateSynthesisGoal(context.Context, app.CreateSynthesisGoalCommand) (app.SynthesisGoalCreateResult, error)
}

// 目标命令只持久保存意图，发现与模型执行由 Worker 异步负责。
func NewSynthesisHandlerWithGoals(notes SynthesisService, processing SynthesisProcessingService, directory app.KnowledgeDirectoryReader, goals SynthesisGoalCreator, views app.SynthesisGoalViewReader, selections app.SynthesisGoalSelectionCommands, timeout time.Duration, promoters ...SynthesisSourcePromoter) *SynthesisHandler {
	h := NewSynthesisHandlerWithDirectory(notes, processing, directory, timeout)
	h.goals, h.goalViews, h.goalSelections = goals, views, selections
	if len(promoters) == 1 {
		h.sourcePromoter = promoters[0]
	}
	return h
}

func (h *SynthesisHandler) goalsAvailable(w http.ResponseWriter) bool {
	if !h.available(w) {
		return false
	}
	if nilDependency(h.goals) || nilDependency(h.goalViews) {
		writeProblem(w, http.StatusServiceUnavailable, "SYNTHESIS_GOAL_HTTP_UNAVAILABLE", "主笔记目标服务暂不可用", true)
		return false
	}
	return true
}

type synthesisGoalRequestResponse struct {
	ID          foundation.ID `json:"id"`
	WorkspaceID foundation.ID `json:"workspace_id"`
	Goal        string        `json:"goal"`
	Status      string        `json:"status"`
	ErrorCode   *string       `json:"error_code"`
	Version     int64         `json:"version"`
	CreatedAt   string        `json:"created_at"`
	UpdatedAt   string        `json:"updated_at"`
}

type synthesisGoalProgressResponse struct {
	CatalogBatches       int64   `json:"catalog_batches"`
	PreparedBatches      int64   `json:"prepared_batches"`
	Selections           int64   `json:"selections"`
	Pending              int64   `json:"pending"`
	Running              int64   `json:"running"`
	Succeeded            int64   `json:"succeeded"`
	Failed               int64   `json:"failed"`
	RecoveryRequired     int64   `json:"recovery_required"`
	SelectedPoints       int64   `json:"selected_points"`
	Ready                bool    `json:"ready"`
	PreparationFailures  int64   `json:"preparation_failures"`
	PreparationErrorCode *string `json:"preparation_error_code"`
}

type synthesisGoalCandidateResponse struct {
	NoteID     foundation.ID `json:"note_id"`
	RevisionID foundation.ID `json:"revision_id"`
}

type synthesisGoalViewResponse struct {
	Request    synthesisGoalRequestResponse    `json:"request"`
	Progress   synthesisGoalProgressResponse   `json:"progress"`
	Processing *synthesisProcessingResponse    `json:"processing"`
	Candidate  *synthesisGoalCandidateResponse `json:"candidate"`
}

func toSynthesisGoalRequest(r app.SynthesisGoalRequest, workspace foundation.ID) (synthesisGoalRequestResponse, error) {
	if !validResponseID(r.ID) || r.WorkspaceID != workspace || (app.CreateSynthesisGoalCommand{WorkspaceID: workspace, Goal: r.Goal, IdempotencyKey: "response"}).Validate() != nil || r.Version < 1 || r.CatalogBatches < 0 || r.CreatedAt.IsZero() || r.UpdatedAt.Before(r.CreatedAt) || (r.Status != app.SynthesisGoalDiscovering && r.Status != app.SynthesisGoalCatalogReady) {
		return synthesisGoalRequestResponse{}, synthesisInvalidResult()
	}
	var code *string
	if r.ErrorCode != "" {
		if !validErrorCode(r.ErrorCode) {
			return synthesisGoalRequestResponse{}, synthesisInvalidResult()
		}
		code = &r.ErrorCode
	}
	return synthesisGoalRequestResponse{ID: r.ID, WorkspaceID: workspace, Goal: r.Goal, Status: r.Status, ErrorCode: code, Version: r.Version, CreatedAt: formatTime(r.CreatedAt), UpdatedAt: formatTime(r.UpdatedAt)}, nil
}

func toSynthesisGoalView(v app.SynthesisGoalView, workspace foundation.ID) (synthesisGoalViewResponse, error) {
	var result synthesisGoalViewResponse
	r, err := toSynthesisGoalRequest(v.Request, workspace)
	if err != nil {
		return result, err
	}
	p := v.Progress
	if p.Request != v.Request || p.CatalogBatches != v.Request.CatalogBatches || p.CatalogBatches < 0 || p.PreparedBatches < 0 || p.PreparedBatches > p.CatalogBatches || p.Pending < 0 || p.Running < 0 || p.Succeeded < 0 || p.Failed < 0 || p.RecoveryRequired < 0 || p.Selections != p.Pending+p.Running+p.Succeeded+p.Failed+p.RecoveryRequired || v.SelectedPoints < 0 || v.PreparationFailures < 0 || v.PreparationFailures > p.CatalogBatches-p.PreparedBatches || (v.PreparationFailures == 0) != (v.PreparationErrorCode == "") || v.PreparationErrorCode != "" && !validErrorCode(v.PreparationErrorCode) {
		return result, synthesisInvalidResult()
	}
	result.Request = r
	var preparationError *string
	if v.PreparationErrorCode != "" {
		preparationError = &v.PreparationErrorCode
	}
	result.Progress = synthesisGoalProgressResponse{p.CatalogBatches, p.PreparedBatches, p.Selections, p.Pending, p.Running, p.Succeeded, p.Failed, p.RecoveryRequired, v.SelectedPoints, p.Ready(), v.PreparationFailures, preparationError}
	if v.Processing != nil {
		if !p.Ready() || v.SelectedPoints == 0 || v.Processing.GoalRequestID != r.ID || v.Processing.Status == app.SynthesisProcessingSkipped || v.Processing.Status == app.SynthesisProcessingNoChange {
			return result, synthesisInvalidResult()
		}
		processing, err := toSynthesisProcessing(*v.Processing, workspace)
		if err != nil {
			return result, err
		}
		result.Processing = &processing
		if (v.Processing.Status == app.SynthesisProcessingSucceeded) != (v.Candidate != nil) {
			return result, synthesisInvalidResult()
		}
	}
	if v.Candidate != nil {
		if v.Processing == nil || !validResponseID(v.Candidate.NoteID) || !validResponseID(v.Candidate.RevisionID) || len(v.Processing.RevisionIDs) != 1 || v.Processing.RevisionIDs[0] != v.Candidate.RevisionID {
			return result, synthesisInvalidResult()
		}
		result.Candidate = &synthesisGoalCandidateResponse{v.Candidate.NoteID, v.Candidate.RevisionID}
	}
	return result, nil
}

func (h *SynthesisHandler) createGoal(w http.ResponseWriter, r *http.Request) {
	workspace, key, ok := commandRoute(w, r)
	if !ok {
		return
	}
	input, err := decodeJSON[struct {
		Goal string `json:"goal"`
	}](r, 16384, 2048, 0)
	if err != nil {
		writeError(w, err)
		return
	}
	command := app.CreateSynthesisGoalCommand{WorkspaceID: workspace, Goal: input.Goal, IdempotencyKey: key}
	if err := command.Validate(); err != nil {
		writeError(w, err)
		return
	}
	if !h.goalsAvailable(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	result, err := h.goals.CreateSynthesisGoal(ctx, command)
	if err != nil {
		writeError(w, err)
		return
	}
	wire, err := toSynthesisGoalRequest(result.Request, workspace)
	if err != nil || result.Request.Goal != command.Goal {
		writeError(w, synthesisInvalidResult())
		return
	}
	writeJSON(w, http.StatusAccepted, struct {
		Request  synthesisGoalRequestResponse `json:"request"`
		Replayed bool                         `json:"replayed"`
	}{wire, result.Replayed})
}

func (h *SynthesisHandler) getGoal(w http.ResponseWriter, r *http.Request) {
	workspace, id, ok := queryRoute(w, r, "goal_id")
	if !ok || !h.goalsAvailable(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	result, err := h.goalViews.GetSynthesisGoalView(ctx, workspace, id)
	if err != nil {
		writeError(w, err)
		return
	}
	wire, err := toSynthesisGoalView(result, workspace)
	if err != nil || result.Request.ID != id {
		writeError(w, synthesisInvalidResult())
		return
	}
	writeJSON(w, http.StatusOK, wire)
}

func (h *SynthesisHandler) listGoals(w http.ResponseWriter, r *http.Request) {
	position, err := h.parseList(r, "goals")
	if err != nil {
		writeError(w, err)
		return
	}
	if !h.goalsAvailable(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	page, err := h.goalViews.ListSynthesisGoalViews(ctx, app.SynthesisListQuery{WorkspaceID: position.WorkspaceID, Limit: position.Limit, BeforeTime: position.BeforeTime, BeforeID: position.BeforeID})
	if err != nil {
		writeError(w, err)
		return
	}
	response := synthesisPage[synthesisGoalViewResponse]{WorkspaceID: position.WorkspaceID, Items: make([]synthesisGoalViewResponse, len(page.Items))}
	if len(page.Items) > position.Limit {
		writeError(w, synthesisInvalidResult())
		return
	}
	previousTime, previousID := position.BeforeTime, position.BeforeID
	for i, v := range page.Items {
		if !synthesisBefore(v.Request.CreatedAt, v.Request.ID, previousTime, previousID) {
			writeError(w, synthesisInvalidResult())
			return
		}
		response.Items[i], err = toSynthesisGoalView(v, position.WorkspaceID)
		if err != nil {
			writeError(w, err)
			return
		}
		previousTime, previousID = &v.Request.CreatedAt, v.Request.ID
	}
	if !synthesisNextMatches(page.NextTime, page.NextID, previousTime, previousID, len(page.Items), position.Limit) {
		writeError(w, synthesisInvalidResult())
		return
	}
	position.BeforeTime, position.BeforeID = page.NextTime, page.NextID
	response.NextCursor, err = h.encodeCursor(position)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}
