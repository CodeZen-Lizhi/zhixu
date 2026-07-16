package ingestionhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/ingestion/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
	"github.com/go-chi/chi/v5"
)

const testSourceVersionID = foundation.ID("81000000-0000-4000-8000-000000000001")

func TestHandlerProcessesAndReturnsProjectionSummary(t *testing.T) {
	service := &fakeService{result: application.ProcessResult{Attempt: domain.AttemptRecord{Attempt: domain.Attempt{
		ID: testSourceVersionID, AttemptNumber: 1, Status: domain.AttemptChunked, SecurityStatus: domain.SecurityPassed, Warnings: []domain.Warning{{Code: "W"}},
	}, ParseProjectionID: idPointer("81000000-0000-4000-8000-000000000002")}, Projection: domain.ProjectionResult{Created: true, Chunks: []domain.CanonicalChunk{{}}}}, created: true}
	recorder := serve(t, service, http.MethodPost, "/source-versions/"+string(testSourceVersionID)+"/ingestion-attempts", `{"attempt_number":1}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var payload processResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status != string(domain.AttemptChunked) || payload.ChunkCount != 1 || payload.WarningCount != 1 || service.request.IdempotencyKey != "ingest-1" {
		t.Fatalf("payload/request = %#v / %#v", payload, service.request)
	}
}

func TestHandlerRequiresIdempotencyAndMapsErrors(t *testing.T) {
	service := &fakeService{err: foundation.NewError(foundation.ErrorVersionConflict, "INGESTION_ATTEMPT_VERSION_CONFLICT", false, errors.New("conflict"))}
	recorder := serveWithHeaders(t, service, http.MethodPost, "/source-versions/"+string(testSourceVersionID)+"/ingestion-attempts", `{"attempt_number":1}`, nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing idempotency status = %d", recorder.Code)
	}
	recorder = serveWithHeaders(t, service, http.MethodPost, "/source-versions/"+string(testSourceVersionID)+"/ingestion-attempts", `{"attempt_number":1}`, map[string]string{"Idempotency-Key": "ingest-1"})
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "INGESTION_ATTEMPT_VERSION_CONFLICT") {
		t.Fatalf("mapped error = %d %s", recorder.Code, recorder.Body.String())
	}
	service.err = foundation.NewError(foundation.ErrorNonRetryableFailure, "INGESTION_CANCELLED", false, context.Canceled)
	recorder = serve(t, service, http.MethodPost, "/source-versions/"+string(testSourceVersionID)+"/ingestion-attempts", `{"attempt_number":1}`)
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "INGESTION_CANCELLED") {
		t.Fatalf("cancelled mapping = %d %s", recorder.Code, recorder.Body.String())
	}
	service.err = context.Canceled
	recorder = serve(t, service, http.MethodPost, "/source-versions/"+string(testSourceVersionID)+"/ingestion-attempts", `{"attempt_number":1}`)
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "INGESTION_CANCELLED") {
		t.Fatalf("raw cancellation mapping = %d %s", recorder.Code, recorder.Body.String())
	}
}

type fakeService struct {
	result  application.ProcessResult
	err     error
	created bool
	request application.ProcessRequest
}

func (f *fakeService) Process(_ context.Context, request application.ProcessRequest) (application.ProcessResult, error) {
	f.request = request
	f.result.AttemptCreated = f.created
	return f.result, f.err
}

func serve(t *testing.T, service Service, method, path, body string) *httptest.ResponseRecorder {
	return serveWithHeaders(t, service, method, path, body, map[string]string{"Idempotency-Key": "ingest-1"})
}

func serveWithHeaders(t *testing.T, service Service, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	router := chi.NewRouter()
	NewHandler(service).Routes(router)
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func idPointer(value string) *foundation.ID {
	id := foundation.ID(value)
	return &id
}
