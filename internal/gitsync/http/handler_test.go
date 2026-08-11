package http

import (
	"context"
	"errors"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
)

const (
	httpWorkspaceID foundation.ID = "10000000-0000-4000-8000-000000000001"
	httpRunID       foundation.ID = "20000000-0000-4000-8000-000000000001"
	httpIndexID     foundation.ID = "30000000-0000-4000-8000-000000000001"
)

type serviceStub struct {
	config      domain.RemoteConfig
	run         domain.SyncRun
	saveErr     error
	saveCalls   int
	createCalls int
}

func (service *serviceStub) GetConfig(context.Context, foundation.ID) (domain.RemoteConfig, error) {
	return service.config, nil
}

func (service *serviceStub) SaveConfig(_ context.Context, command application.SaveConfigCommand) (application.ConfigReceipt, error) {
	service.saveCalls++
	value := command.SecretAction.Value.Bytes()
	clear(value)
	if service.saveErr != nil {
		return application.ConfigReceipt{}, service.saveErr
	}
	return application.ConfigReceipt{Config: service.config}, nil
}

func (service *serviceStub) RemoveConfig(context.Context, application.RemoveConfigCommand) (application.ConfigReceipt, error) {
	return application.ConfigReceipt{Config: service.config}, nil
}

func (service *serviceStub) TestConfig(_ context.Context, command application.TestConfigCommand) (application.TestConfigResult, error) {
	return application.TestConfigResult{RemoteURL: command.RemoteURL, Branch: command.Branch}, nil
}

func (service *serviceStub) GetStatus(context.Context, foundation.ID) (application.StatusSnapshot, error) {
	return application.StatusSnapshot{Config: service.config, CurrentRun: &service.run}, nil
}

func (service *serviceStub) CreateRun(context.Context, application.CreateRunCommand) (application.RunReceipt, error) {
	service.createCalls++
	return application.RunReceipt{Run: service.run}, nil
}

func (service *serviceStub) RetryRun(context.Context, application.RetryRunCommand) (application.RunReceipt, error) {
	return application.RunReceipt{Run: service.run}, nil
}

func (service *serviceStub) GetRun(context.Context, foundation.ID, foundation.ID) (domain.SyncRun, error) {
	return service.run, nil
}

func (service *serviceStub) ListRuns(context.Context, application.ListRunsCommand) (application.PublicRunPage, error) {
	return application.PublicRunPage{Items: []domain.SyncRun{service.run}}, nil
}

func TestHandlerRejectsAmbiguousCommandWire(t *testing.T) {
	service := newHTTPServiceStub()
	path := "/workspaces/" + string(httpWorkspaceID) + "/git-remote"
	tests := []struct {
		name    string
		body    string
		headers map[string][]string
		path    string
		status  int
		code    string
	}{
		{name: "duplicate nested field", body: `{"expected_revision":0,"remote_url":"https://git.example.test/a.git","branch":"main","auto_sync":false,"token":{"action":"replace","action":"clear","value":"secret"}}`, headers: commandHeaders(), path: path, status: stdhttp.StatusBadRequest, code: errorInvalidJSON},
		{name: "unknown field", body: `{"expected_revision":0,"remote_url":"https://git.example.test/a.git","branch":"main","auto_sync":false,"token":{"action":"replace","value":"secret"},"extra":true}`, headers: commandHeaders(), path: path, status: stdhttp.StatusBadRequest, code: errorInvalidJSON},
		{name: "duplicate content type", body: `{}`, headers: map[string][]string{"Content-Type": {"application/json", "application/json"}, "Idempotency-Key": {"save-1"}}, path: path, status: stdhttp.StatusUnsupportedMediaType, code: errorUnsupportedType},
		{name: "duplicate idempotency key", body: `{}`, headers: map[string][]string{"Content-Type": {"application/json"}, "Idempotency-Key": {"save-1", "save-2"}}, path: path, status: stdhttp.StatusBadRequest, code: domain.ErrorCodeInvalid},
		{name: "unknown query", body: `{}`, headers: commandHeaders(), path: path + "?extra=1", status: stdhttp.StatusBadRequest, code: domain.ErrorCodeInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := serveHTTP(t, NewHandler(service, time.Second), stdhttp.MethodPut, test.path, test.body, test.headers)
			if response.Code != test.status || !strings.Contains(response.Body.String(), `"error_code":"`+test.code+`"`) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	if service.saveCalls != 0 {
		t.Fatalf("ambiguous requests reached service %d times", service.saveCalls)
	}
}

func TestHandlerNeverReturnsTokenOrInternalErrorCause(t *testing.T) {
	const secret = "token-that-must-never-leak"
	service := newHTTPServiceStub()
	path := "/workspaces/" + string(httpWorkspaceID) + "/git-remote"
	body := `{"expected_revision":0,"remote_url":"https://git.example.test/a.git","branch":"main","auto_sync":false,"token":{"action":"replace","value":"` + secret + `"}}`
	response := serveHTTP(t, NewHandler(service, time.Second), stdhttp.MethodPut, path, body, commandHeaders())
	if response.Code != stdhttp.StatusCreated || strings.Contains(response.Body.String(), secret) || strings.Contains(response.Body.String(), `"token"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	service.saveErr = foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, errors.New(secret))
	response = serveHTTP(t, NewHandler(service, time.Second), stdhttp.MethodPut, path, body, map[string][]string{
		"Content-Type": {"application/json"}, "Idempotency-Key": {"save-2"},
	})
	if response.Code != stdhttp.StatusServiceUnavailable || strings.Contains(response.Body.String(), secret) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHandlerProjectsIndependentIndexIdentityAndAcceptsEmptyRunBody(t *testing.T) {
	service := newHTTPServiceStub()
	getPath := "/workspaces/" + string(httpWorkspaceID) + "/git-sync/runs/" + string(httpRunID)
	response := serveHTTP(t, NewHandler(service, time.Second), stdhttp.MethodGet, getPath, "", nil)
	if response.Code != stdhttp.StatusOK || !strings.Contains(response.Body.String(), `"index_status":"SUCCEEDED"`) ||
		!strings.Contains(response.Body.String(), `"index_version_id":"`+string(httpIndexID)+`"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	createPath := "/workspaces/" + string(httpWorkspaceID) + "/git-sync/runs"
	response = serveHTTP(t, NewHandler(service, time.Second), stdhttp.MethodPost, createPath, "", map[string][]string{"Idempotency-Key": {"manual-1"}})
	if response.Code != stdhttp.StatusAccepted || service.createCalls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, service.createCalls, response.Body.String())
	}
}

func TestUnavailableHandlerFailsClosed(t *testing.T) {
	path := "/workspaces/" + string(httpWorkspaceID) + "/git-remote"
	response := serveHTTP(t, NewHandler(nil, time.Second), stdhttp.MethodGet, path, "", nil)
	if response.Code != stdhttp.StatusServiceUnavailable || !strings.Contains(response.Body.String(), errorUnavailable) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func newHTTPServiceStub() *serviceStub {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	completed := now.Add(time.Second)
	return &serviceStub{
		config: domain.RemoteConfig{
			WorkspaceID: httpWorkspaceID, Configured: true, RemoteURL: "https://git.example.test/a.git",
			Branch: "main", TokenConfigured: true, Revision: 1, CreatedAt: now, UpdatedAt: now,
		},
		run: domain.SyncRun{
			ID: httpRunID, WorkspaceID: httpWorkspaceID, ConfigRevision: 1,
			RemoteURL: "https://git.example.test/a.git", Branch: "main", Trigger: domain.TriggerManual,
			Status: domain.RunSucceeded, Direction: domain.DirectionPull, FailureClass: domain.FailureNone,
			ExpectedHeadOID: strings.Repeat("1", 40), ExpectedRemoteOID: strings.Repeat("2", 40),
			VerifiedHeadOID: strings.Repeat("2", 40), VerifiedRemoteOID: strings.Repeat("2", 40),
			IndexStatus: domain.IndexSucceeded, IndexVersionID: httpIndexID, Version: 5,
			CreatedAt: now, UpdatedAt: completed, CompletedAt: &completed,
		},
	}
}

func commandHeaders() map[string][]string {
	return map[string][]string{"Content-Type": {"application/json"}, "Idempotency-Key": {"save-1"}}
}

func serveHTTP(t *testing.T, handler *Handler, method, path, body string, headers map[string][]string) *httptest.ResponseRecorder {
	t.Helper()
	router := gin.New()
	handler.Routes(router)
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	for key, values := range headers {
		request.Header[key] = values
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
