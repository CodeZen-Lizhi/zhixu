package organizinghttp

import (
	"context"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"net/http"
)

type synthesisGoalSelectionResponse struct {
	ID          foundation.ID           `json:"id"`
	WorkspaceID foundation.ID           `json:"workspace_id"`
	RequestID   foundation.ID           `json:"request_id"`
	Status      app.GoalSelectionStatus `json:"status"`
	ErrorCode   *string                 `json:"error_code"`
	Retryable   bool                    `json:"retryable"`
	Version     int64                   `json:"version"`
	CreatedAt   string                  `json:"created_at"`
	UpdatedAt   string                  `json:"updated_at"`
}

func toSynthesisGoalSelection(s app.SynthesisGoalSelection, w, g foundation.ID) (synthesisGoalSelectionResponse, error) {
	var result synthesisGoalSelectionResponse
	if !validResponseID(s.ID) || s.WorkspaceID != w || s.RequestID != g || s.Version < 1 || s.CreatedAt.IsZero() || s.UpdatedAt.Before(s.CreatedAt) {
		return result, synthesisInvalidResult()
	}
	switch s.Status {
	case app.GoalSelectionPending, app.GoalSelectionRunning, app.GoalSelectionSucceeded:
		if s.ErrorCode != "" || s.Retryable {
			return result, synthesisInvalidResult()
		}
	case app.GoalSelectionFailed:
		if s.ErrorCode == "" {
			return result, synthesisInvalidResult()
		}
	case app.GoalSelectionRecoveryRequired:
		if s.ErrorCode == "" || s.Retryable {
			return result, synthesisInvalidResult()
		}
	default:
		return result, synthesisInvalidResult()
	}
	var code *string
	if s.ErrorCode != "" {
		if !validErrorCode(s.ErrorCode) {
			return result, synthesisInvalidResult()
		}
		code = &s.ErrorCode
	}
	return synthesisGoalSelectionResponse{s.ID, w, g, s.Status, code, s.Retryable, s.Version, formatTime(s.CreatedAt), formatTime(s.UpdatedAt)}, nil
}

func (h *SynthesisHandler) goalSelectionsAvailable(w http.ResponseWriter) bool {
	if !h.goalsAvailable(w) {
		return false
	}
	if nilDependency(h.goalSelections) {
		writeProblem(w, http.StatusServiceUnavailable, "SYNTHESIS_GOAL_HTTP_UNAVAILABLE", "知识筛选服务暂不可用", true)
		return false
	}
	return true
}

func (h *SynthesisHandler) listGoalSelections(w http.ResponseWriter, r *http.Request) {
	q, err := parseAnchorList(r, false)
	if err != nil {
		writeError(w, err)
		return
	}
	goal, err := parseID(r.PathValue("goal_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if !h.goalSelectionsAvailable(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	page, err := h.goalSelections.ListGoalSelections(ctx, app.SynthesisGoalSelectionListQuery{WorkspaceID: q.WorkspaceID, RequestID: goal, AfterID: q.AfterID, Limit: q.Limit})
	if err != nil {
		writeError(w, err)
		return
	}
	items := make([]synthesisGoalSelectionResponse, len(page.Items))
	previous := q.AfterID
	if len(items) > q.Limit {
		writeError(w, synthesisInvalidResult())
		return
	}
	for i, s := range page.Items {
		if s.ID <= previous {
			writeError(w, synthesisInvalidResult())
			return
		}
		previous = s.ID
		items[i], err = toSynthesisGoalSelection(s, q.WorkspaceID, goal)
		if err != nil {
			writeError(w, err)
			return
		}
	}
	if page.NextAfterID != "" && (len(items) != q.Limit || page.NextAfterID != previous) {
		writeError(w, synthesisInvalidResult())
		return
	}
	writeJSON(w, http.StatusOK, struct {
		WorkspaceID foundation.ID                    `json:"workspace_id"`
		RequestID   foundation.ID                    `json:"request_id"`
		Items       []synthesisGoalSelectionResponse `json:"items"`
		NextAfterID *string                          `json:"next_after_id"`
	}{q.WorkspaceID, goal, items, optionalID(page.NextAfterID)})
}

func (h *SynthesisHandler) retryGoalSelection(w http.ResponseWriter, r *http.Request) {
	workspace, id, key, ok := identifiedCommandRoute(w, r, "selection_id")
	if !ok {
		return
	}
	goal, err := parseID(r.PathValue("goal_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	input, err := decodeJSON[expectedVersionRequest](r, maxSmallRequestBodyBytes, 4096, 0)
	if err != nil {
		writeError(w, err)
		return
	}
	if input.ExpectedVersion < 1 {
		writeError(w, requestInvalid("selection version is invalid"))
		return
	}
	if !h.goalSelectionsAvailable(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	current, err := h.goalSelections.GetGoalSelection(ctx, workspace, id)
	if err != nil {
		writeError(w, err)
		return
	}
	if current.WorkspaceID != workspace || current.ID != id || current.RequestID != goal {
		writeProblem(w, http.StatusNotFound, "SYNTHESIS_GOAL_SELECTION_NOT_FOUND", "未找到知识筛选记录", false)
		return
	}
	result, err := h.goalSelections.RetryGoalSelection(ctx, app.RetryGoalSelectionCommand{WorkspaceID: workspace, SelectionID: id, ExpectedVersion: input.ExpectedVersion, IdempotencyKey: key})
	if err != nil {
		writeError(w, err)
		return
	}
	wire, err := toSynthesisGoalSelection(result, workspace, goal)
	if err != nil || result.ID != id || result.Version <= input.ExpectedVersion {
		writeError(w, synthesisInvalidResult())
		return
	}
	writeJSON(w, http.StatusAccepted, wire)
}
