package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	authhttp "github.com/CodeZen-Lizhi/zhixu/internal/auth/http"
	modelsettingsapplication "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	modelsettingsdomain "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	modelsettingshttp "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/http"
)

type routerModelSettingsManager struct{}

func (routerModelSettingsManager) Snapshot(context.Context) (modelsettingsdomain.Snapshot, error) {
	settings := modelsettingsdomain.CanonicalDisabledSettings()
	summary := modelsettingsdomain.SettingsSummary{Settings: settings}
	return modelsettingsdomain.Snapshot{
		DesiredSettings: summary,
		ActiveSettings:  summary,
		Runtime: modelsettingsdomain.RuntimeSummaries{
			API:    modelsettingsdomain.RuntimeSummary{Phase: modelsettingsdomain.RuntimePhaseActive, Fresh: true},
			Worker: modelsettingsdomain.RuntimeSummary{Phase: modelsettingsdomain.RuntimePhaseActive, Fresh: true},
		},
		Rollout:             modelsettingsdomain.RolloutState{Phase: modelsettingsdomain.RolloutPhaseIdle, Version: 1},
		ChatCapability:      modelsettingsdomain.CapabilityDisabled,
		EmbeddingCapability: modelsettingsdomain.CapabilityDisabled,
	}, nil
}

func (manager routerModelSettingsManager) Save(ctx context.Context, command modelsettingsapplication.SaveCommand) (modelsettingsdomain.Snapshot, error) {
	snapshot, err := manager.Snapshot(ctx)
	if err != nil {
		return modelsettingsdomain.Snapshot{}, err
	}
	if command.ExpectedRevision != 0 {
		return modelsettingsdomain.Snapshot{}, errors.New("unexpected model settings revision")
	}
	snapshot.DesiredRevision = 1
	snapshot.DesiredSettings = modelsettingsdomain.SettingsSummary{Settings: command.Settings}
	snapshot.RestartRequired = true
	return snapshot, nil
}

func (routerModelSettingsManager) Test(_ context.Context, command modelsettingsapplication.TestCommand) (modelsettingsapplication.TestResult, error) {
	if command.Target != modelsettingsapplication.ConnectionTargetChat {
		return modelsettingsapplication.TestResult{}, errors.New("unexpected model settings target")
	}
	return modelsettingsapplication.TestResult{
		Target: command.Target, Provider: string(command.Draft.Settings.Chat.Provider), Model: command.Draft.Settings.Chat.Model,
	}, nil
}

func TestRouterRegistersSessionOnlyModelSettingsRoutes(t *testing.T) {
	router := newModelSettingsRouter(t)
	tests := []struct {
		name     string
		method   string
		path     string
		body     string
		contains string
	}{
		{name: "get", method: http.MethodGet, path: "/api/v1/settings/models", contains: `"desired_settings"`},
		{name: "put", method: http.MethodPut, path: "/api/v1/settings/models", body: disabledModelSettingsUpdateBody(), contains: `"desired_revision":1`},
		{name: "test", method: http.MethodPost, path: "/api/v1/settings/models/test", body: chatModelSettingsTestBody(), contains: `"status":"ok"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			request.AddCookie(&http.Cookie{Name: authhttp.SessionCookieName, Value: "session-cookie"})
			if test.body != "" {
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Origin", "https://app.example.test")
				request.Header.Set(authhttp.CSRFHeaderName, "csrf-token")
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), test.contains) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			assertModelSettingsNoStore(t, response)
		})
	}
}

func TestRouterModelSettingsAuthenticationFailsClosed(t *testing.T) {
	router := newModelSettingsRouter(t)
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		setup  func(*http.Request)
		status int
		code   string
	}{
		{name: "anonymous get", method: http.MethodGet, path: "/api/v1/settings/models", status: http.StatusUnauthorized, code: "AUTH_UNAUTHORIZED"},
		{name: "anonymous put", method: http.MethodPut, path: "/api/v1/settings/models", body: disabledModelSettingsUpdateBody(), status: http.StatusUnauthorized, code: "AUTH_UNAUTHORIZED"},
		{name: "anonymous test", method: http.MethodPost, path: "/api/v1/settings/models/test", body: chatModelSettingsTestBody(), status: http.StatusUnauthorized, code: "AUTH_UNAUTHORIZED"},
		{name: "bearer get", method: http.MethodGet, path: "/api/v1/settings/models", setup: withModelSettingsBearer, status: http.StatusForbidden, code: "MODEL_SETTINGS_SESSION_REQUIRED"},
		{name: "bearer put", method: http.MethodPut, path: "/api/v1/settings/models", body: disabledModelSettingsUpdateBody(), setup: withModelSettingsBearer, status: http.StatusForbidden, code: "MODEL_SETTINGS_SESSION_REQUIRED"},
		{name: "bearer test", method: http.MethodPost, path: "/api/v1/settings/models/test", body: chatModelSettingsTestBody(), setup: withModelSettingsBearer, status: http.StatusForbidden, code: "MODEL_SETTINGS_SESSION_REQUIRED"},
		{name: "put missing origin", method: http.MethodPut, path: "/api/v1/settings/models", body: disabledModelSettingsUpdateBody(), setup: withModelSettingsSession, status: http.StatusForbidden, code: "AUTH_CSRF_REJECTED"},
		{name: "test wrong origin", method: http.MethodPost, path: "/api/v1/settings/models/test", body: chatModelSettingsTestBody(), setup: withWrongModelSettingsOrigin, status: http.StatusForbidden, code: "AUTH_CSRF_REJECTED"},
		{name: "put missing csrf", method: http.MethodPut, path: "/api/v1/settings/models", body: disabledModelSettingsUpdateBody(), setup: withModelSettingsOrigin, status: http.StatusForbidden, code: "AUTH_CSRF_REJECTED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			if test.body != "" {
				request.Header.Set("Content-Type", "application/json")
			}
			if test.setup != nil {
				test.setup(request)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.status || !strings.Contains(response.Body.String(), `"error_code":"`+test.code+`"`) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			assertModelSettingsNoStore(t, response)
		})
	}
}

func TestRouterModelSettingsMethodFailureIsNoStore(t *testing.T) {
	router := newModelSettingsRouter(t)
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/settings/models", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	assertModelSettingsNoStore(t, response)
}

func newModelSettingsRouter(t *testing.T) http.Handler {
	t.Helper()
	settingsHandler, err := modelsettingshttp.NewHandler(routerModelSettingsManager{}, modelsettingshttp.Options{RequireSession: true})
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(Dependencies{
		Version: "model-settings-router-test", Database: fakePinger{}, AuthRequired: true,
		Auth: readyAuthHandler(t), AuthCheck: func(context.Context) error { return nil }, ModelSettings: settingsHandler,
	})
}

func withModelSettingsBearer(request *http.Request) {
	request.Header.Set("Authorization", "Bearer model-settings-token")
}

func withModelSettingsSession(request *http.Request) {
	request.AddCookie(&http.Cookie{Name: authhttp.SessionCookieName, Value: "session-cookie"})
}

func withModelSettingsOrigin(request *http.Request) {
	withModelSettingsSession(request)
	request.Header.Set("Origin", "https://app.example.test")
}

func withWrongModelSettingsOrigin(request *http.Request) {
	withModelSettingsSession(request)
	request.Header.Set("Origin", "https://other.example.test")
	request.Header.Set(authhttp.CSRFHeaderName, "csrf-token")
}

func disabledModelSettingsUpdateBody() string {
	return `{"expected_revision":0,"chat":{"provider":"disabled","base_url":"","model":"","model_version":"","adapter_version":"v1","api_key":{"action":"clear"}},"embedding":{"provider":"disabled","base_url":"","model":"","dimensions":0,"normalization":"l2","distance_metric":"cosine","api_key":{"action":"clear"}}}`
}

func chatModelSettingsTestBody() string {
	return `{"target":"chat","chat":{"provider":"openai-compatible","base_url":"https://chat.example.test/v1","model":"chat-v1","model_version":"2026-07","adapter_version":"v1","api_key":{"action":"clear"}}}`
}

func assertModelSettingsNoStore(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q", got)
	}
}
