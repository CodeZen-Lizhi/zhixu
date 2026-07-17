package httphealth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	workflowruntime "github.com/CodeZen-Lizhi/zhixu/internal/workflow/runtime"
)

func TestHealthHandlerLivenessIsIndependentOfReadiness(t *testing.T) {
	handler := NewHandler(workflowruntime.NewReadiness())
	recorder := request(t, handler, http.MethodGet, "/livez")
	assertResponse(t, recorder, http.StatusOK, response{Status: "live", Code: codeLive, Version: workflowruntime.ReadinessVersion})
}

func TestHealthHandlerReadinessFailureMatrix(t *testing.T) {
	allReady := workflowruntime.ReadinessSnapshot{
		DatabaseOK:     true,
		RiverSchemaOK:  true,
		RiverStarted:   true,
		DefinitionsOK:  true,
		ExecutorsOK:    true,
		DependenciesOK: true,
	}
	tests := []struct {
		name     string
		mutate   func(*workflowruntime.ReadinessSnapshot)
		wantCode string
	}{
		{"database", func(s *workflowruntime.ReadinessSnapshot) { s.DatabaseOK = false }, workflowruntime.CodeDatabaseUnavailable},
		{"river schema", func(s *workflowruntime.ReadinessSnapshot) { s.RiverSchemaOK = false }, workflowruntime.CodeRiverSchemaUnavailable},
		{"river client", func(s *workflowruntime.ReadinessSnapshot) { s.RiverStarted = false }, workflowruntime.CodeRiverNotStarted},
		{"definitions", func(s *workflowruntime.ReadinessSnapshot) { s.DefinitionsOK = false }, workflowruntime.CodeDefinitionsUnavailable},
		{"executors", func(s *workflowruntime.ReadinessSnapshot) { s.ExecutorsOK = false }, workflowruntime.CodeExecutorsUnavailable},
		{"dependencies", func(s *workflowruntime.ReadinessSnapshot) { s.DependenciesOK = false }, workflowruntime.CodeDependenciesUnavailable},
		{"shutdown", func(s *workflowruntime.ReadinessSnapshot) { s.ShuttingDown = true }, workflowruntime.CodeShuttingDown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := allReady
			test.mutate(&snapshot)
			snapshot.Code = test.wantCode
			handler := NewHandler(staticProvider{snapshot: snapshot})
			recorder := request(t, handler, http.MethodGet, "/readyz")
			assertResponse(t, recorder, http.StatusServiceUnavailable, response{Status: "not_ready", Code: test.wantCode, Version: workflowruntime.ReadinessVersion})
		})
	}
}

func TestHealthHandlerReadyContract(t *testing.T) {
	readiness := workflowruntime.NewReadiness()
	readiness.SetDatabaseOK(true)
	readiness.SetRiverSchemaOK(true)
	readiness.SetRiverStarted(true)
	readiness.SetDefinitionsOK(true)
	readiness.SetExecutorsOK(true)
	readiness.SetDependenciesOK(true)

	recorder := request(t, NewHandler(readiness), http.MethodGet, "/readyz")
	assertResponse(t, recorder, http.StatusOK, response{Status: "ready", Code: workflowruntime.CodeReady, Version: workflowruntime.ReadinessVersion})
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
}

func TestHealthHandlerMethodAndUnknownPathContract(t *testing.T) {
	handler := NewHandler(workflowruntime.NewReadiness())
	method := request(t, handler, http.MethodPost, "/readyz")
	assertResponse(t, method, http.StatusMethodNotAllowed, response{Status: "error", Code: codeMethodNotAllowed, Version: workflowruntime.ReadinessVersion})
	if got := method.Header().Get("Allow"); got != http.MethodGet {
		t.Fatalf("Allow = %q", got)
	}

	unknown := request(t, handler, http.MethodGet, "/jobs")
	assertResponse(t, unknown, http.StatusNotFound, response{Status: "error", Code: codeNotFound, Version: workflowruntime.ReadinessVersion})
}

func TestHealthHandlerDoesNotExposeProviderDetails(t *testing.T) {
	sensitive := "postgres://user:password@db/zhixu payload=/secret/workspace credential=token"
	handler := NewHandler(staticProvider{snapshot: workflowruntime.ReadinessSnapshot{Code: sensitive}})
	recorder := request(t, handler, http.MethodGet, "/readyz")
	assertResponse(t, recorder, http.StatusServiceUnavailable, response{Status: "not_ready", Code: codeReadinessUnavailable, Version: workflowruntime.ReadinessVersion})
	if strings.Contains(recorder.Body.String(), sensitive) || strings.Contains(recorder.Body.String(), "password") || strings.Contains(recorder.Body.String(), "credential") {
		t.Fatalf("health response leaked provider details: %s", recorder.Body.String())
	}
}

func TestHealthHandlerNilProviderIsNotReady(t *testing.T) {
	recorder := request(t, NewHandler(nil), http.MethodGet, "/readyz")
	assertResponse(t, recorder, http.StatusServiceUnavailable, response{Status: "not_ready", Code: codeReadinessUnavailable, Version: workflowruntime.ReadinessVersion})
}

type staticProvider struct {
	snapshot workflowruntime.ReadinessSnapshot
}

func (p staticProvider) Snapshot() workflowruntime.ReadinessSnapshot { return p.snapshot }

func request(t *testing.T, handler http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(method, path, nil))
	return recorder
}

func assertResponse(t *testing.T, recorder *httptest.ResponseRecorder, wantStatus int, want response) {
	t.Helper()
	if recorder.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, wantStatus, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	var got response
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got != want {
		t.Fatalf("response = %+v, want %+v", got, want)
	}
}
