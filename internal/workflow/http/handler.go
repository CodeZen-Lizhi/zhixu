// Package workflowhttp exposes durable Workflow operations over HTTP.
package workflowhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"github.com/go-chi/chi/v5"
)

// Service 定义 Workflow HTTP 所需的最小应用层契约。
type Service interface {
	Start(context.Context, application.StartCommand) (domain.Run, error)
	Get(context.Context, foundation.ID) (domain.Run, error)
	SubmitRuntimeHumanDecision(context.Context, application.HumanDecisionCommand) (application.HumanTransitionResult, error)
	Pause(context.Context, application.RunControlCommand) (application.RunControlResult, error)
	Resume(context.Context, application.RunControlCommand) (application.RunControlResult, error)
	Cancel(context.Context, application.RunControlCommand) (application.RunControlResult, error)
}

// Handler 负责 Workflow 协议解析、响应编码和错误映射。
type Handler struct{ service Service }

// NewHandler 创建 Workflow HTTP Handler。
func NewHandler(service Service) *Handler { return &Handler{service: service} }

// Routes 注册 Workflow 资源路由。
func (h *Handler) Routes(router chi.Router) {
	router.Post("/workspaces/{workspaceID}/workflows", h.start)
	router.Get("/workflows/{runID}", h.detail)
	router.Post("/workflows/{runID}/human-tasks/{taskID}/decision", h.submitHumanDecision)
	router.Post("/workflows/{runID}/pause", h.pause)
	router.Post("/workflows/{runID}/resume", h.resume)
	router.Post("/workflows/{runID}/cancel", h.cancel)
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
	ID           string          `json:"id"`
	WorkspaceID  string          `json:"workspace_id"`
	DefinitionID string          `json:"definition_id"`
	Status       string          `json:"status"`
	Input        json.RawMessage `json:"input"`
	Output       json.RawMessage `json:"output,omitempty"`
	Version      int64           `json:"version"`
	CreatedAt    string          `json:"created_at"`
	UpdatedAt    string          `json:"updated_at"`
	CompletedAt  *string         `json:"completed_at,omitempty"`
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
	WorkflowRunID string `json:"workflow_run_id"`
	Status        string `json:"status"`
	Version       int64  `json:"version"`
	StatusURL     string `json:"status_url"`
}

func (h *Handler) start(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := foundation.ParseID(chi.URLParam(r, "workspaceID"))
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
	run, err := h.service.Start(r.Context(), application.StartCommand{
		WorkspaceID: workspaceID, DefinitionKey: request.DefinitionKey, DefinitionVersion: request.DefinitionVersion,
		Graph: request.Graph, Input: request.Input, FirstNodeKey: request.FirstNodeKey, FirstNodeType: request.FirstNodeType, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, startResponse{WorkflowRunID: string(run.ID), Status: string(run.Status), StatusURL: "/api/v1/workflows/" + string(run.ID)})
}

func (h *Handler) detail(w http.ResponseWriter, r *http.Request) {
	runID, err := foundation.ParseID(chi.URLParam(r, "runID"))
	if err != nil {
		writeError(w, err)
		return
	}
	if h == nil || h.service == nil {
		writeProblem(w, http.StatusServiceUnavailable, "WORKFLOW_SERVICE_UNAVAILABLE", "Workflow 服务暂不可用", true, nil)
		return
	}
	run, err := h.service.Get(r.Context(), runID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toRunResponse(run))
}

func (h *Handler) submitHumanDecision(w http.ResponseWriter, r *http.Request) {
	taskID, err := foundation.ParseID(chi.URLParam(r, "taskID"))
	if err != nil {
		writeError(w, err)
		return
	}
	runID, err := foundation.ParseID(chi.URLParam(r, "runID"))
	if err != nil {
		writeError(w, err)
		return
	}
	var request humanDecisionRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, err)
		return
	}
	if h == nil || h.service == nil {
		writeProblem(w, http.StatusServiceUnavailable, "WORKFLOW_SERVICE_UNAVAILABLE", "Workflow 服务暂不可用", true, nil)
		return
	}
	result, err := h.service.SubmitRuntimeHumanDecision(r.Context(), application.HumanDecisionCommand{RunID: runID, TaskID: taskID, TargetVersion: request.TargetVersion, Decision: request.Decision})
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
	runID, err := foundation.ParseID(chi.URLParam(r, "runID"))
	if err != nil {
		writeError(w, err)
		return
	}
	if h == nil || h.service == nil {
		writeProblem(w, http.StatusServiceUnavailable, "WORKFLOW_SERVICE_UNAVAILABLE", "Workflow 服务暂不可用", true, nil)
		return
	}
	var request controlRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, err)
		return
	}
	result, err := execute(r.Context(), application.RunControlCommand{WorkflowRunID: runID, ExpectedVersion: request.ExpectedVersion, IdempotencyKey: strings.TrimSpace(r.Header.Get("Idempotency-Key"))})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, controlResponse{WorkflowRunID: string(result.WorkflowRunID), Status: string(result.Status), Version: result.Version, StatusURL: result.StatusURL})
}

func decodeJSON(r *http.Request, target any) error {
	if err := httpapi.DecodeJSON(r, target); err != nil {
		return foundation.NewError(foundation.ErrorInvalidInput, "INVALID_JSON", false, err)
	}
	return nil
}

func toRunResponse(run domain.Run) runResponse {
	response := runResponse{ID: string(run.ID), WorkspaceID: string(run.WorkspaceID), DefinitionID: string(run.DefinitionID), Status: string(run.Status), Input: run.Input, Output: run.Output, Version: run.Version, CreatedAt: run.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: run.UpdatedAt.UTC().Format(time.RFC3339Nano)}
	if run.CompletedAt != nil {
		value := run.CompletedAt.UTC().Format(time.RFC3339Nano)
		response.CompletedAt = &value
	}
	return response
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
