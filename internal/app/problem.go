package app

import (
	"net/http"

	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
)

// Problem is the stable API error envelope shared by readiness and API
// handlers. Details are intentionally structured and must not contain secrets.
type Problem = httpapi.Problem

func writeJSON(w http.ResponseWriter, status int, value any) {
	httpapi.WriteJSON(w, status, value)
}

func writeProblem(w http.ResponseWriter, status int, code, message string, retryable bool, details map[string]any) {
	httpapi.WriteProblem(w, status, code, message, retryable, details)
}
