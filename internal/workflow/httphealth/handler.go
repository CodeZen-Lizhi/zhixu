// Package httphealth exposes the worker-only liveness and readiness adapter.
package httphealth

import (
	"encoding/json"
	"net/http"

	workflowruntime "github.com/CodeZen-Lizhi/zhixu/internal/workflow/runtime"
)

const (
	codeLive                 = "WORKER_LIVE"
	codeReadinessUnavailable = "WORKER_READINESS_UNAVAILABLE"
	codeMethodNotAllowed     = "METHOD_NOT_ALLOWED"
	codeNotFound             = "NOT_FOUND"
)

// SnapshotProvider supplies a sanitized workflow runtime readiness snapshot.
type SnapshotProvider interface {
	Snapshot() workflowruntime.ReadinessSnapshot
}

// Handler serves the worker health contract without control endpoints.
type Handler struct {
	readiness SnapshotProvider
}

// NewHandler creates an independent worker health HTTP handler.
func NewHandler(readiness SnapshotProvider) *Handler {
	return &Handler{readiness: readiness}
}

// ServeHTTP routes the worker health endpoints.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeResponse(w, http.StatusMethodNotAllowed, "error", codeMethodNotAllowed)
		return
	}

	switch r.URL.Path {
	case "/livez":
		writeResponse(w, http.StatusOK, "live", codeLive)
	case "/readyz":
		h.serveReady(w)
	default:
		writeResponse(w, http.StatusNotFound, "error", codeNotFound)
	}
}

func (h *Handler) serveReady(w http.ResponseWriter) {
	if h == nil || h.readiness == nil {
		writeResponse(w, http.StatusServiceUnavailable, "not_ready", codeReadinessUnavailable)
		return
	}
	snapshot := h.readiness.Snapshot()
	if snapshot.Ready() {
		writeResponse(w, http.StatusOK, "ready", workflowruntime.CodeReady)
		return
	}
	writeResponse(w, http.StatusServiceUnavailable, "not_ready", publicReadinessCode(snapshot.Code))
}

type response struct {
	Status  string `json:"status"`
	Code    string `json:"code"`
	Version string `json:"version"`
}

func writeResponse(w http.ResponseWriter, status int, state, code string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response{Status: state, Code: code, Version: workflowruntime.ReadinessVersion})
}

func publicReadinessCode(code string) string {
	switch code {
	case workflowruntime.CodeShuttingDown,
		workflowruntime.CodeDatabaseUnavailable,
		workflowruntime.CodeRiverSchemaUnavailable,
		workflowruntime.CodeRiverNotStarted,
		workflowruntime.CodeDefinitionsUnavailable,
		workflowruntime.CodeExecutorsUnavailable,
		workflowruntime.CodeDependenciesUnavailable:
		return code
	default:
		return codeReadinessUnavailable
	}
}
