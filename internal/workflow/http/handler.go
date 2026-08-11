// Package workflowhttp exposes durable Workflow operations over HTTP.
package workflowhttp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	authhttp "github.com/CodeZen-Lizhi/zhixu/internal/auth/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"github.com/gin-gonic/gin"
)

// Service 定义 Workflow HTTP 所需的最小应用层契约。
type Service interface {
	Start(context.Context, application.StartCommand) (domain.Run, error)
	Get(context.Context, foundation.ID) (domain.Run, error)
	GetPendingHumanTask(context.Context, foundation.ID) (domain.HumanTask, bool, error)
	SubmitRuntimeHumanDecision(context.Context, application.HumanDecisionCommand) (application.HumanTransitionResult, error)
	Pause(context.Context, application.RunControlCommand) (application.RunControlResult, error)
	Resume(context.Context, application.RunControlCommand) (application.RunControlResult, error)
	Cancel(context.Context, application.RunControlCommand) (application.RunControlResult, error)
}

type listService interface {
	ListRuns(context.Context, domain.RunListQuery) ([]domain.RunListItem, bool, error)
}

// HumanTaskReviewProjector optionally adds a bounded owner review projection to
// a pending Human Task. required=false leaves non-owned tasks unchanged.
type HumanTaskReviewProjector interface {
	ProjectHumanTaskReview(context.Context, domain.Run, domain.HumanTask) (review json.RawMessage, required bool, err error)
}

// Handler 负责 Workflow 协议解析、响应编码和错误映射。
type Handler struct {
	service         Service
	reviewProjector HumanTaskReviewProjector
}

// NewHandler 创建 Workflow HTTP Handler。
func NewHandler(service Service, reviewProjectors ...HumanTaskReviewProjector) *Handler {
	handler := &Handler{service: service}
	if len(reviewProjectors) > 0 {
		handler.reviewProjector = reviewProjectors[0]
	}
	return handler
}

// Routes 注册 Workflow 资源路由。
func (h *Handler) Routes(router gin.IRouter) {
	router.POST("/workspaces/:workspace_id/workflows", httpapi.GinHandler(h.start))
	router.GET("/workspaces/:workspace_id/workflows", httpapi.GinHandler(h.list))
	router.GET("/workflows/:run_id", httpapi.GinHandler(h.detail))
	router.POST("/workflows/:run_id/human-tasks/:task_id/decision", httpapi.GinHandler(h.submitHumanDecision))
	router.POST("/workflows/:run_id/pause", httpapi.GinHandler(h.pause))
	router.POST("/workflows/:run_id/resume", httpapi.GinHandler(h.resume))
	router.POST("/workflows/:run_id/cancel", httpapi.GinHandler(h.cancel))
}

type runPageResponse struct {
	Items      []runListResponse `json:"items"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

const (
	workflowCursorVersion = 1
	workflowCursorKind    = "workflow_list"
)

type workflowListCursor struct {
	Version     int    `json:"version"`
	Kind        string `json:"kind"`
	WorkspaceID string `json:"workspace_id"`
	At          string `json:"at"`
	ID          string `json:"id"`
	Filter      string `json:"filter"`
}

type runListResponse struct {
	ID                string  `json:"id"`
	WorkspaceID       string  `json:"workspace_id"`
	DefinitionKey     string  `json:"definition_key"`
	DefinitionVersion int64   `json:"definition_version"`
	Status            string  `json:"status"`
	Version           int64   `json:"version"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
	CompletedAt       *string `json:"completed_at,omitempty"`
	WaitingForHuman   bool    `json:"waiting_for_human"`
	PauseRequested    bool    `json:"pause_requested"`
	CancelRequested   bool    `json:"cancel_requested"`
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(listService)
	if !ok {
		writeProblem(w, http.StatusServiceUnavailable, "WORKFLOW_LIST_UNAVAILABLE", "Workflow 列表暂不可用", true, nil)
		return
	}
	workspaceID, err := foundation.ParseID(r.PathValue("workspace_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	allowed := map[string]struct{}{"cursor": {}, "limit": {}, "status": {}}
	for key, values := range r.URL.Query() {
		if _, ok := allowed[key]; !ok {
			writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_LIST_FILTER_INVALID", false, fmt.Errorf("unknown query parameter %q", key)))
			return
		}
		if len(values) != 1 {
			writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_LIST_FILTER_INVALID", false, fmt.Errorf("query parameter %q must appear once", key)))
			return
		}
	}
	status := domain.RunStatus(strings.TrimSpace(r.URL.Query().Get("status")))
	if status != "" && !validRunListStatus(status) {
		writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_LIST_FILTER_INVALID", false, errors.New("workflow status filter is invalid")))
		return
	}
	filterJSON, _ := json.Marshal(struct {
		Status domain.RunStatus `json:"status,omitempty"`
	}{Status: status})
	limit := 30
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, scanErr := strconv.Atoi(raw)
		if scanErr != nil {
			writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_LIST_LIMIT_INVALID", false, scanErr))
			return
		}
		limit = parsed
	}
	if limit < 1 || limit > 100 {
		writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_LIST_LIMIT_INVALID", false, errors.New("limit must be between 1 and 100")))
		return
	}
	var cursorTime *time.Time
	var cursorID foundation.ID
	if raw := strings.TrimSpace(r.URL.Query().Get("cursor")); raw != "" {
		if len(raw) > 2048 {
			writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_CURSOR_INVALID", false, errors.New("cursor is too long")))
			return
		}
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(raw)
		if decodeErr != nil {
			writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_CURSOR_INVALID", false, decodeErr))
			return
		}
		var cursor workflowListCursor
		decoder := json.NewDecoder(strings.NewReader(string(decoded)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&cursor) != nil || decoder.Decode(&struct{}{}) != io.EOF || cursor.Version != workflowCursorVersion || cursor.Kind != workflowCursorKind || cursor.WorkspaceID != string(workspaceID) || cursor.Filter != string(filterJSON) {
			writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_CURSOR_INVALID", false, errors.New("cursor payload is invalid")))
			return
		}
		parsed, parseErr := time.Parse(time.RFC3339Nano, cursor.At)
		if parseErr != nil {
			writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_CURSOR_INVALID", false, parseErr))
			return
		}
		id, idErr := foundation.ParseID(cursor.ID)
		if idErr != nil {
			writeError(w, idErr)
			return
		}
		cursorTime, cursorID = &parsed, id
	}
	items, hasMore, err := service.ListRuns(r.Context(), domain.RunListQuery{WorkspaceID: workspaceID, Status: status, CursorTime: cursorTime, CursorID: cursorID, Limit: limit})
	if err != nil {
		writeError(w, err)
		return
	}
	response := runPageResponse{Items: make([]runListResponse, len(items))}
	for index, item := range items {
		response.Items[index] = runListResponse{ID: string(item.ID), WorkspaceID: string(item.WorkspaceID), DefinitionKey: item.DefinitionKey, DefinitionVersion: item.DefinitionVersion, Status: string(item.Status), Version: item.Version, CreatedAt: item.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: item.UpdatedAt.UTC().Format(time.RFC3339Nano), WaitingForHuman: item.WaitingForHuman, PauseRequested: item.PauseRequestedAt != nil, CancelRequested: item.CancelRequestedAt != nil}
		if item.CompletedAt != nil {
			completed := item.CompletedAt.UTC().Format(time.RFC3339Nano)
			response.Items[index].CompletedAt = &completed
		}
	}
	if hasMore && len(items) > 0 {
		tail := items[len(items)-1]
		payload, _ := json.Marshal(workflowListCursor{Version: workflowCursorVersion, Kind: workflowCursorKind, WorkspaceID: string(workspaceID), At: tail.UpdatedAt.UTC().Format(time.RFC3339Nano), ID: string(tail.ID), Filter: string(filterJSON)})
		response.NextCursor = base64.RawURLEncoding.EncodeToString(payload)
	}
	writeJSON(w, http.StatusOK, response)
}

func validRunListStatus(status domain.RunStatus) bool {
	switch status {
	case domain.RunStatusPending, domain.RunStatusRunning, domain.RunStatusWaitingForHuman, domain.RunStatusRetryWait, domain.RunStatusPaused, domain.RunStatusSucceeded, domain.RunStatusFailed, domain.RunStatusCancelled:
		return true
	default:
		return false
	}
}

type startRequest struct {
	DefinitionKey     string          `json:"definition_key"`
	DefinitionVersion int64           `json:"definition_version"`
	Graph             json.RawMessage `json:"graph"`
	Input             json.RawMessage `json:"input"`
	FirstNodeKey      string          `json:"first_node_key"`
	FirstNodeType     string          `json:"first_node_type"`
}

type startResponse struct {
	WorkflowRunID string `json:"workflow_run_id"`
	Status        string `json:"status"`
	StatusURL     string `json:"status_url"`
}

type runResponse struct {
	ID              string                    `json:"id"`
	WorkspaceID     string                    `json:"workspace_id"`
	DefinitionID    string                    `json:"definition_id"`
	Status          string                    `json:"status"`
	Input           json.RawMessage           `json:"input"`
	Output          json.RawMessage           `json:"output,omitempty"`
	Version         int64                     `json:"version"`
	CreatedAt       string                    `json:"created_at"`
	UpdatedAt       string                    `json:"updated_at"`
	CompletedAt     *string                   `json:"completed_at,omitempty"`
	PauseRequested  bool                      `json:"pause_requested"`
	CancelRequested bool                      `json:"cancel_requested"`
	HumanTask       *pendingHumanTaskResponse `json:"human_task"`
}

type pendingHumanTaskResponse struct {
	ID                  string          `json:"id"`
	RunID               string          `json:"run_id"`
	NodeRunID           string          `json:"node_run_id"`
	Status              string          `json:"status"`
	ExpectedInputSchema json.RawMessage `json:"expected_input_schema"`
	TargetVersion       int64           `json:"target_version"`
	ExpiresAt           *string         `json:"expires_at"`
	CreatedAt           string          `json:"created_at"`
	Review              json.RawMessage `json:"review"`
}

type humanDecisionRequest struct {
	TargetVersion int64           `json:"target_version"`
	Decision      json.RawMessage `json:"decision"`
}

type humanTaskResponse struct {
	ID            string          `json:"id"`
	RunID         string          `json:"run_id"`
	NodeRunID     string          `json:"node_run_id"`
	Status        string          `json:"status"`
	TargetVersion int64           `json:"target_version"`
	Decision      json.RawMessage `json:"decision,omitempty"`
	SubmittedAt   *string         `json:"submitted_at,omitempty"`
}

type controlRequest struct {
	ExpectedVersion int64 `json:"expected_version"`
}

type controlResponse struct {
	WorkflowRunID   string `json:"workflow_run_id"`
	Status          string `json:"status"`
	Version         int64  `json:"version"`
	StatusURL       string `json:"status_url"`
	PauseRequested  bool   `json:"pause_requested"`
	CancelRequested bool   `json:"cancel_requested"`
}

func (h *Handler) start(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := foundation.ParseID(r.PathValue("workspace_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	var request startRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, err)
		return
	}
	if h == nil || h.service == nil {
		writeProblem(w, http.StatusServiceUnavailable, "WORKFLOW_SERVICE_UNAVAILABLE", "Workflow 服务暂不可用", true, nil)
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" || len(idempotencyKey) > 128 {
		writeError(w, foundation.NewError(foundation.ErrorInvalidInput, "IDEMPOTENCY_KEY_REQUIRED", false, errors.New("workflow start requires an Idempotency-Key header")))
		return
	}
	startCommand := application.StartCommand{
		WorkspaceID: workspaceID, DefinitionKey: request.DefinitionKey, DefinitionVersion: request.DefinitionVersion,
		Graph: request.Graph, Input: request.Input, FirstNodeKey: request.FirstNodeKey, FirstNodeType: request.FirstNodeType, IdempotencyKey: idempotencyKey,
	}
	startCommand.CallerCapabilities = callerCapabilities(r.Context())
	run, err := h.service.Start(r.Context(), startCommand)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, startResponse{WorkflowRunID: string(run.ID), Status: string(run.Status), StatusURL: "/api/v1/workflows/" + string(run.ID)})
}

func (h *Handler) detail(w http.ResponseWriter, r *http.Request) {
	runID, err := foundation.ParseID(r.PathValue("run_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := workflowWorkspaceID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if h == nil || h.service == nil {
		writeProblem(w, http.StatusServiceUnavailable, "WORKFLOW_SERVICE_UNAVAILABLE", "Workflow 服务暂不可用", true, nil)
		return
	}
	run, err := h.ownedRun(r.Context(), runID, workspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	task, found, err := h.service.GetPendingHumanTask(r.Context(), runID)
	if err != nil {
		writeError(w, err)
		return
	}
	response := toRunResponse(run)
	if found {
		mapped, mapErr := toPendingHumanTaskResponse(task, run)
		if mapErr != nil {
			writeError(w, mapErr)
			return
		}
		if h.reviewProjector != nil {
			review, required, reviewErr := h.reviewProjector.ProjectHumanTaskReview(r.Context(), run, task)
			if reviewErr != nil {
				writeError(w, reviewErr)
				return
			}
			if (required && len(review) == 0) || (!required && len(review) != 0) || (len(review) != 0 && !json.Valid(review)) {
				writeError(w, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_HUMAN_TASK_REVIEW_INVALID", false, errors.New("workflow human task review projection is invalid")))
				return
			}
			mapped.Review = append(json.RawMessage(nil), review...)
		}
		response.HumanTask = &mapped
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) submitHumanDecision(w http.ResponseWriter, r *http.Request) {
	taskID, err := foundation.ParseID(r.PathValue("task_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	runID, err := foundation.ParseID(r.PathValue("run_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := workflowWorkspaceID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if h == nil || h.service == nil {
		writeProblem(w, http.StatusServiceUnavailable, "WORKFLOW_SERVICE_UNAVAILABLE", "Workflow 服务暂不可用", true, nil)
		return
	}
	run, err := h.ownedRun(r.Context(), runID, workspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	if h.reviewProjector != nil {
		task, found, taskErr := h.service.GetPendingHumanTask(r.Context(), runID)
		if taskErr != nil {
			writeError(w, taskErr)
			return
		}
		if !found || task.ID != taskID {
			writeError(w, foundation.NewError(foundation.ErrorNotFound, "HUMAN_TASK_NOT_FOUND", false, errors.New("human task was not found")))
			return
		}
		review, required, reviewErr := h.reviewProjector.ProjectHumanTaskReview(r.Context(), run, task)
		if reviewErr != nil {
			writeError(w, reviewErr)
			return
		}
		if (required && len(review) == 0) || (!required && len(review) != 0) || (len(review) != 0 && !json.Valid(review)) {
			writeError(w, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_HUMAN_TASK_REVIEW_INVALID", false, errors.New("workflow human task review projection is invalid")))
			return
		}
	}
	var request humanDecisionRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, err)
		return
	}
	result, err := h.service.SubmitRuntimeHumanDecision(r.Context(), application.HumanDecisionCommand{
		RunID: runID, TaskID: taskID, TargetVersion: request.TargetVersion, Decision: request.Decision,
		CallerCapabilities: callerCapabilities(r.Context()),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toHumanResponse(result.Task))
}

func (h *Handler) pause(w http.ResponseWriter, r *http.Request) {
	h.control(w, r, func(ctx context.Context, command application.RunControlCommand) (application.RunControlResult, error) {
		return h.service.Pause(ctx, command)
	})
}

func (h *Handler) resume(w http.ResponseWriter, r *http.Request) {
	h.control(w, r, func(ctx context.Context, command application.RunControlCommand) (application.RunControlResult, error) {
		return h.service.Resume(ctx, command)
	})
}

func (h *Handler) cancel(w http.ResponseWriter, r *http.Request) {
	h.control(w, r, func(ctx context.Context, command application.RunControlCommand) (application.RunControlResult, error) {
		return h.service.Cancel(ctx, command)
	})
}

func (h *Handler) control(w http.ResponseWriter, r *http.Request, execute func(context.Context, application.RunControlCommand) (application.RunControlResult, error)) {
	runID, err := foundation.ParseID(r.PathValue("run_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	workspaceID, err := workflowWorkspaceID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if h == nil || h.service == nil {
		writeProblem(w, http.StatusServiceUnavailable, "WORKFLOW_SERVICE_UNAVAILABLE", "Workflow 服务暂不可用", true, nil)
		return
	}
	if _, err := h.ownedRun(r.Context(), runID, workspaceID); err != nil {
		writeError(w, err)
		return
	}
	var request controlRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, err)
		return
	}
	result, err := execute(r.Context(), application.RunControlCommand{
		WorkflowRunID: runID, ExpectedVersion: request.ExpectedVersion,
		IdempotencyKey: strings.TrimSpace(r.Header.Get("Idempotency-Key")), CallerCapabilities: callerCapabilities(r.Context()),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, controlResponse{WorkflowRunID: string(result.WorkflowRunID), Status: string(result.Status), Version: result.Version, StatusURL: result.StatusURL, PauseRequested: result.PauseRequested, CancelRequested: result.CancelRequested})
}

func workflowWorkspaceID(r *http.Request) (foundation.ID, error) {
	raw := strings.TrimSpace(r.Header.Get("X-Workspace-ID"))
	if raw == "" {
		return "", foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_WORKSPACE_REQUIRED", false, errors.New("X-Workspace-ID header is required"))
	}
	workspaceID, err := foundation.ParseID(raw)
	if err != nil {
		return "", foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_WORKSPACE_INVALID", false, err)
	}
	return workspaceID, nil
}

func (h *Handler) ownedRun(ctx context.Context, runID, workspaceID foundation.ID) (domain.Run, error) {
	run, err := h.service.Get(ctx, runID)
	if err != nil {
		return domain.Run{}, err
	}
	if run.WorkspaceID != workspaceID {
		return domain.Run{}, foundation.NewError(foundation.ErrorNotFound, "WORKFLOW_NOT_FOUND", false, errors.New("workflow run was not found"))
	}
	return run, nil
}

func callerCapabilities(ctx context.Context) []capability.Capability {
	principal, authenticated := authhttp.PrincipalFromContext(ctx)
	if !authenticated {
		return nil
	}
	return append([]capability.Capability{}, principal.Scopes...)
}

func decodeJSON(r *http.Request, target any) error {
	if err := httpapi.DecodeJSON(r, target); err != nil {
		return foundation.NewError(foundation.ErrorInvalidInput, "INVALID_JSON", false, err)
	}
	return nil
}

func toRunResponse(run domain.Run) runResponse {
	response := runResponse{ID: string(run.ID), WorkspaceID: string(run.WorkspaceID), DefinitionID: string(run.DefinitionID), Status: string(run.Status), Input: run.Input, Output: run.Output, Version: run.Version, CreatedAt: run.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: run.UpdatedAt.UTC().Format(time.RFC3339Nano), PauseRequested: run.PauseRequestedAt != nil, CancelRequested: run.CancelRequestedAt != nil}
	if run.CompletedAt != nil {
		value := run.CompletedAt.UTC().Format(time.RFC3339Nano)
		response.CompletedAt = &value
	}
	return response
}

func toPendingHumanTaskResponse(task domain.HumanTask, run domain.Run) (pendingHumanTaskResponse, error) {
	var schema map[string]json.RawMessage
	if task.ID == "" || task.RunID != run.ID || task.NodeRunID == "" || task.Status != domain.HumanTaskPending ||
		task.TargetVersion < 1 || task.CreatedAt.IsZero() || json.Unmarshal(task.ExpectedInputSchema, &schema) != nil ||
		(task.ExpiresAt != nil && task.ExpiresAt.Before(task.CreatedAt)) {
		return pendingHumanTaskResponse{}, foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_HUMAN_TASK_RESULT_INVALID", false, errors.New("workflow pending human task is invalid"))
	}
	response := pendingHumanTaskResponse{ID: string(task.ID), RunID: string(task.RunID), NodeRunID: string(task.NodeRunID),
		Status: string(task.Status), ExpectedInputSchema: task.ExpectedInputSchema, TargetVersion: task.TargetVersion,
		CreatedAt: task.CreatedAt.UTC().Format(time.RFC3339Nano)}
	if task.ExpiresAt != nil {
		value := task.ExpiresAt.UTC().Format(time.RFC3339Nano)
		response.ExpiresAt = &value
	}
	return response, nil
}

func toHumanResponse(task domain.HumanTask) humanTaskResponse {
	response := humanTaskResponse{ID: string(task.ID), RunID: string(task.RunID), NodeRunID: string(task.NodeRunID), Status: string(task.Status), TargetVersion: task.TargetVersion, Decision: task.Decision}
	if task.SubmittedAt != nil {
		value := task.SubmittedAt.UTC().Format(time.RFC3339Nano)
		response.SubmittedAt = &value
	}
	return response
}

func writeJSON(w http.ResponseWriter, status int, value any) { httpapi.WriteJSON(w, status, value) }
func writeProblem(w http.ResponseWriter, status int, code, message string, retryable bool, details map[string]any) {
	httpapi.WriteProblem(w, status, code, message, retryable, details)
}

func writeError(w http.ResponseWriter, err error) {
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		writeProblem(w, http.StatusInternalServerError, "INTERNAL_ERROR", "服务处理失败", false, nil)
		return
	}
	status := http.StatusInternalServerError
	switch classified.Kind {
	case foundation.ErrorInvalidInput:
		status = http.StatusBadRequest
	case foundation.ErrorNotFound:
		status = http.StatusNotFound
	case foundation.ErrorVersionConflict, foundation.ErrorConsistencyViolation, foundation.ErrorManualRecoveryRequired:
		status = http.StatusConflict
	case foundation.ErrorDependencyUnavailable, foundation.ErrorRetryableFailure:
		status = http.StatusServiceUnavailable
	case foundation.ErrorPermissionDenied:
		status = http.StatusForbidden
	}
	message := "请求处理失败"
	if strings.TrimSpace(classified.Code) != "" {
		message = "请求未完成：" + classified.Code
	}
	writeProblem(w, status, classified.Code, message, classified.Retryable, nil)
}

var _ Service = (*application.Service)(nil)
