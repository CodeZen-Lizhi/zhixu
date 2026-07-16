// Package ingestionhttp exposes the synchronous Ingestion command boundary.
package ingestionhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	"github.com/CodeZen-Lizhi/zhixu/internal/ingestion/application"
	"github.com/go-chi/chi/v5"
)

// Service is the minimum application contract required by this HTTP boundary.
type Service interface {
	Process(context.Context, application.ProcessRequest) (application.ProcessResult, error)
}

// Handler validates request metadata and never exposes parser/pgx types.
type Handler struct{ service Service }

// NewHandler creates an Ingestion HTTP handler.
func NewHandler(service Service) *Handler { return &Handler{service: service} }

// Routes registers the Source Version ingestion command.
func (h *Handler) Routes(router chi.Router) {
	router.Post("/source-versions/{sourceVersionID}/ingestion-attempts", h.process)
}

type processRequest struct {
	WorkflowRunID string `json:"workflow_run_id"`
	AttemptNumber int32  `json:"attempt_number"`
}

type processResponse struct {
	AttemptID         string `json:"attempt_id"`
	AttemptCreated    bool   `json:"attempt_created"`
	AttemptNumber     int32  `json:"attempt_number"`
	Status            string `json:"status"`
	SecurityStatus    string `json:"security_status"`
	FailureStage      string `json:"failure_stage,omitempty"`
	ErrorCode         string `json:"error_code,omitempty"`
	Retryable         bool   `json:"retryable"`
	ProjectionID      string `json:"parse_projection_id,omitempty"`
	ProjectionCreated bool   `json:"projection_created"`
	ChunkCount        int    `json:"chunk_count"`
	WarningCount      int    `json:"warning_count"`
}

func (h *Handler) process(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.service == nil {
		writeProblem(w, http.StatusServiceUnavailable, "INGESTION_SERVICE_UNAVAILABLE", "Ingestion 服务暂不可用", true, nil)
		return
	}
	id, err := foundation.ParseID(chi.URLParam(r, "sourceVersionID"))
	if err != nil {
		writeError(w, err)
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" || len(idempotencyKey) > 128 {
		writeProblem(w, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "必须提供有效的 Idempotency-Key", false, nil)
		return
	}
	var request processRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, err)
		return
	}
	if request.AttemptNumber <= 0 {
		writeProblem(w, http.StatusBadRequest, "INGESTION_REQUEST_INVALID", "attempt_number 必须为正数", false, nil)
		return
	}
	var workflowRunID *foundation.ID
	if strings.TrimSpace(request.WorkflowRunID) != "" {
		parsed, parseErr := foundation.ParseID(request.WorkflowRunID)
		if parseErr != nil {
			writeError(w, parseErr)
			return
		}
		workflowRunID = &parsed
	}
	result, err := h.service.Process(r.Context(), application.ProcessRequest{
		SourceVersionID: id, WorkflowRunID: workflowRunID, IdempotencyKey: idempotencyKey, AttemptNumber: request.AttemptNumber,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if result.AttemptCreated {
		status = http.StatusCreated
	}
	writeJSON(w, status, toResponse(result))
}

func toResponse(result application.ProcessResult) processResponse {
	response := processResponse{
		AttemptID: string(result.Attempt.ID), AttemptCreated: result.AttemptCreated, AttemptNumber: result.Attempt.AttemptNumber,
		Status: string(result.Attempt.Status), SecurityStatus: string(result.Attempt.SecurityStatus), FailureStage: result.Attempt.FailureStage,
		ErrorCode: result.Attempt.ErrorCode, Retryable: result.Attempt.Retryable, ProjectionCreated: result.Projection.Created,
		ChunkCount: len(result.Projection.Chunks), WarningCount: len(result.Attempt.Warnings),
	}
	if result.Attempt.ParseProjectionID != nil {
		response.ProjectionID = string(*result.Attempt.ParseProjectionID)
	}
	return response
}

func decodeJSON(r *http.Request, target any) error {
	if err := httpapi.DecodeJSON(r, target); err != nil {
		return foundation.NewError(foundation.ErrorInvalidInput, "INVALID_JSON", false, err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeProblem(w http.ResponseWriter, status int, code, message string, retryable bool, details map[string]any) {
	httpapi.WriteProblem(w, status, code, message, retryable, details)
}

func writeError(w http.ResponseWriter, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		writeProblem(w, http.StatusServiceUnavailable, "INGESTION_CANCELLED", "请求已取消", false, nil)
		return
	}
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
	case foundation.ErrorPermissionDenied:
		status = http.StatusForbidden
	case foundation.ErrorDependencyUnavailable, foundation.ErrorRetryableFailure:
		status = http.StatusServiceUnavailable
	case foundation.ErrorNonRetryableFailure:
		// Cancellation is a durable, non-retryable attempt outcome but the
		// request boundary still reports service unavailability so clients can
		// distinguish it from malformed input and stop retrying blindly.
		if classified.Code == "INGESTION_CANCELLED" {
			status = http.StatusServiceUnavailable
		}
	}
	writeProblem(w, status, classified.Code, "请求未完成："+classified.Code, classified.Retryable, nil)
}

var _ Service = (*application.Service)(nil)
