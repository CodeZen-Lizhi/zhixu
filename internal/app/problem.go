package app

import (
	"encoding/json"
	"net/http"
)

// Problem is the stable API error envelope shared by readiness and API
// handlers. Details are intentionally structured and must not contain secrets.
type Problem struct {
	ErrorCode     string         `json:"error_code"`
	Message       string         `json:"message"`
	Retryable     bool           `json:"retryable"`
	WorkflowRunID string         `json:"workflow_run_id,omitempty"`
	Details       map[string]any `json:"details,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeProblem(w http.ResponseWriter, status int, code, message string, retryable bool, details map[string]any) {
	writeJSON(w, status, Problem{ErrorCode: code, Message: message, Retryable: retryable, Details: details})
}
