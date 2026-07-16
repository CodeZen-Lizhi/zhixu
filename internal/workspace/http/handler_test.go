package workspacehttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"github.com/go-chi/chi/v5"
)

const handlerTestWorkspaceID foundation.ID = "123e4567-e89b-42d3-a456-426614174000"

var handlerTestTime = time.Date(2026, time.July, 16, 4, 30, 0, 0, time.UTC)

func TestHandlerCreateWorkspaceContract(t *testing.T) {
	service := &fakeWorkspaceService{createResult: workspaceResult(true)}
	recorder := serveWorkspaceRequest(t, service, http.MethodPost, "/api/v1/workspaces", `{
		"name":"Product Workspace",
		"root_path":"/workspace",
		"initialize_git":true
	}`)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	requireJSONContentType(t, recorder)
	response := decodeBody[workspaceResponse](t, recorder)
	if service.createCalls != 1 || service.createRequest != (application.CreateWorkspaceRequest{Name: "Product Workspace", RootPath: "/workspace", InitializeGit: true}) {
		t.Fatalf("create request = %#v, calls = %d", service.createRequest, service.createCalls)
	}
	if response.ID != string(handlerTestWorkspaceID) || response.Name != "Product Workspace" || response.RootPath != "/workspace" {
		t.Fatalf("workspace response = %#v", response)
	}
	if response.Status != string(domain.WorkspaceStatusActive) || response.Version != 1 {
		t.Fatalf("workspace status/version = %q/%d", response.Status, response.Version)
	}
	if response.CreatedAt != "2026-07-16T04:30:00.000Z" || response.UpdatedAt != "2026-07-16T04:30:00.000Z" {
		t.Fatalf("workspace timestamps = %q/%q", response.CreatedAt, response.UpdatedAt)
	}
	if !response.Git.Present || !response.Git.Dirty || response.Git.RepositoryPath != "/workspace" || response.Git.Branch != "dev" || response.Git.Head != "abc123" || response.Git.CheckedAt != "2026-07-16T04:30:00.000Z" {
		t.Fatalf("git response = %#v", response.Git)
	}
	if len(response.Warnings) != 1 || response.Warnings[0] != application.WarningGitDirty {
		t.Fatalf("warnings = %#v", response.Warnings)
	}
}

func TestHandlerWorkspaceDetailContract(t *testing.T) {
	service := &fakeWorkspaceService{getResult: workspaceResult(false)}
	recorder := serveWorkspaceRequest(t, service, http.MethodGet, "/api/v1/workspaces/"+string(handlerTestWorkspaceID), "")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	response := decodeBody[workspaceResponse](t, recorder)
	if service.getCalls != 1 || service.getID != handlerTestWorkspaceID {
		t.Fatalf("detail ID = %q, calls = %d", service.getID, service.getCalls)
	}
	if response.ID != string(handlerTestWorkspaceID) || response.Name != "Product Workspace" {
		t.Fatalf("detail response = %#v", response)
	}
}

func TestHandlerWorkspaceScanContract(t *testing.T) {
	service := &fakeWorkspaceService{scanFiles: []domain.ScannedFile{
		{RelativePath: "a.md", ByteSize: 12, ContentHash: "hash-a", MediaType: "text/markdown", SourceID: handlerTestWorkspaceID, SourceVersionID: handlerTestWorkspaceID, ContentArtifactID: handlerTestWorkspaceID, ContentArtifactCreated: true},
		{RelativePath: "nested/b.txt", ByteSize: 8, ContentHash: "hash-b", MediaType: "text/plain", SourceID: handlerTestWorkspaceID, SourceVersionID: handlerTestWorkspaceID, ContentArtifactID: handlerTestWorkspaceID},
	}}
	recorder := serveWorkspaceRequest(t, service, http.MethodPost, "/api/v1/workspaces/"+string(handlerTestWorkspaceID)+"/scan", "")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	response := decodeBody[scanResponse](t, recorder)
	if service.scanCalls != 1 || service.scanID != handlerTestWorkspaceID {
		t.Fatalf("scan ID = %q, calls = %d", service.scanID, service.scanCalls)
	}
	if response.WorkspaceID != string(handlerTestWorkspaceID) || response.Count != 2 || len(response.Files) != 2 {
		t.Fatalf("scan response = %#v", response)
	}
	if response.Files[0] != (scannedFile{RelativePath: "a.md", ByteSize: 12, ContentHash: "hash-a", MediaType: "text/markdown", SourceID: string(handlerTestWorkspaceID), SourceVersionID: string(handlerTestWorkspaceID), ContentArtifactID: string(handlerTestWorkspaceID), ContentArtifactCreated: true}) {
		t.Fatalf("first scanned file = %#v", response.Files[0])
	}
	if response.Files[1] != (scannedFile{RelativePath: "nested/b.txt", ByteSize: 8, ContentHash: "hash-b", MediaType: "text/plain", SourceID: string(handlerTestWorkspaceID), SourceVersionID: string(handlerTestWorkspaceID), ContentArtifactID: string(handlerTestWorkspaceID)}) {
		t.Fatalf("second scanned file = %#v", response.Files[1])
	}
}

func TestHandlerRejectsInvalidJSON(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed", body: `{"name":`},
		{name: "unknown field", body: `{"name":"Workspace","root_path":"/workspace","extra":true}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeWorkspaceService{}
			recorder := serveWorkspaceRequest(t, service, http.MethodPost, "/api/v1/workspaces", test.body)

			problem := requireProblem(t, recorder, http.StatusBadRequest, "INVALID_JSON", false)
			if problem.Message == "" || service.createCalls != 0 {
				t.Fatalf("problem = %#v, create calls = %d", problem, service.createCalls)
			}
		})
	}
}

func TestHandlerRejectsInvalidWorkspaceID(t *testing.T) {
	service := &fakeWorkspaceService{}
	recorder := serveWorkspaceRequest(t, service, http.MethodGet, "/api/v1/workspaces/not-an-id", "")

	requireProblem(t, recorder, http.StatusBadRequest, "INVALID_ID", false)
	if service.getCalls != 0 {
		t.Fatalf("detail service calls = %d, want 0", service.getCalls)
	}
}

func TestHandlerProblemMappingContract(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		status    int
		code      string
		retryable bool
	}{
		{name: "invalid input", err: classifiedError(foundation.ErrorInvalidInput, "BAD_INPUT", false), status: http.StatusBadRequest, code: "BAD_INPUT"},
		{name: "not found", err: classifiedError(foundation.ErrorNotFound, "WORKSPACE_NOT_FOUND", false), status: http.StatusNotFound, code: "WORKSPACE_NOT_FOUND"},
		{name: "version conflict", err: classifiedError(foundation.ErrorVersionConflict, "WORKSPACE_ALREADY_EXISTS", false), status: http.StatusConflict, code: "WORKSPACE_ALREADY_EXISTS"},
		{name: "dependency unavailable", err: classifiedError(foundation.ErrorDependencyUnavailable, "DATABASE_UNAVAILABLE", true), status: http.StatusServiceUnavailable, code: "DATABASE_UNAVAILABLE", retryable: true},
		{name: "consistency violation", err: classifiedError(foundation.ErrorConsistencyViolation, "GIT_REPOSITORY_MISSING", false), status: http.StatusConflict, code: "GIT_REPOSITORY_MISSING"},
		{name: "permission denied", err: classifiedError(foundation.ErrorPermissionDenied, "WORKSPACE_FORBIDDEN", false), status: http.StatusForbidden, code: "WORKSPACE_FORBIDDEN"},
		{name: "non retryable", err: classifiedError(foundation.ErrorNonRetryableFailure, "WORKSPACE_FAILED", false), status: http.StatusInternalServerError, code: "WORKSPACE_FAILED"},
		{name: "unclassified", err: errors.New("secret internal failure"), status: http.StatusInternalServerError, code: "INTERNAL_ERROR"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeWorkspaceService{getErr: test.err}
			recorder := serveWorkspaceRequest(t, service, http.MethodGet, "/api/v1/workspaces/"+string(handlerTestWorkspaceID), "")

			problem := requireProblem(t, recorder, test.status, test.code, test.retryable)
			if strings.Contains(recorder.Body.String(), "secret internal failure") || problem.Message == "" {
				t.Fatalf("unsafe or empty Problem response = %s", recorder.Body.String())
			}
		})
	}
}

func workspaceResult(dirty bool) application.WorkspaceResult {
	return application.WorkspaceResult{
		Workspace: domain.Workspace{
			ID:       handlerTestWorkspaceID,
			Name:     "Product Workspace",
			RootPath: "/workspace",
			Git: domain.GitBaseline{
				RepositoryPath: "/workspace",
				Branch:         "dev",
				Head:           "abc123",
				Dirty:          dirty,
				CheckedAt:      handlerTestTime,
			},
			Status:    domain.WorkspaceStatusActive,
			Version:   1,
			CreatedAt: handlerTestTime,
			UpdatedAt: handlerTestTime,
		},
		Git: domain.GitStatus{
			Present:        true,
			RepositoryPath: "/workspace",
			Branch:         "dev",
			Head:           "abc123",
			Dirty:          dirty,
		},
		Warnings: func() []string {
			if dirty {
				return []string{application.WarningGitDirty}
			}
			return nil
		}(),
	}
}

func classifiedError(kind foundation.ErrorKind, code string, retryable bool) error {
	return foundation.NewError(kind, code, retryable, errors.New("secret internal failure"))
}

func serveWorkspaceRequest(t *testing.T, service Service, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	router := chi.NewRouter()
	router.Route("/api/v1", NewHandler(service).Routes)
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func decodeBody[T any](t *testing.T, recorder *httptest.ResponseRecorder) T {
	t.Helper()
	requireJSONContentType(t, recorder)
	var value T
	if err := json.Unmarshal(recorder.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
	}
	return value
}

func requireProblem(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string, retryable bool) httpapi.Problem {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status = %d, want %d, body = %s", recorder.Code, status, recorder.Body.String())
	}
	problem := decodeBody[httpapi.Problem](t, recorder)
	if problem.ErrorCode != code || problem.Retryable != retryable {
		t.Fatalf("Problem = %#v, want code=%q retryable=%t", problem, code, retryable)
	}
	return problem
}

func requireJSONContentType(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
}

type fakeWorkspaceService struct {
	createResult  application.WorkspaceResult
	createErr     error
	createRequest application.CreateWorkspaceRequest
	createCalls   int
	getResult     application.WorkspaceResult
	getErr        error
	getID         foundation.ID
	getCalls      int
	scanFiles     []domain.ScannedFile
	scanErr       error
	scanID        foundation.ID
	scanCalls     int
}

func (f *fakeWorkspaceService) CreateWorkspace(_ context.Context, request application.CreateWorkspaceRequest) (application.WorkspaceResult, error) {
	f.createCalls++
	f.createRequest = request
	return f.createResult, f.createErr
}

func (f *fakeWorkspaceService) OpenWorkspace(context.Context, string) (application.WorkspaceResult, error) {
	return application.WorkspaceResult{}, nil
}

func (f *fakeWorkspaceService) GetWorkspace(_ context.Context, id foundation.ID) (application.WorkspaceResult, error) {
	f.getCalls++
	f.getID = id
	return f.getResult, f.getErr
}

func (f *fakeWorkspaceService) ScanWorkspace(_ context.Context, id foundation.ID) ([]domain.ScannedFile, error) {
	f.scanCalls++
	f.scanID = id
	return append([]domain.ScannedFile(nil), f.scanFiles...), f.scanErr
}

var _ Service = (*fakeWorkspaceService)(nil)
