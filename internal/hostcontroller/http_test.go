package hostcontroller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeStore struct {
	mu           sync.Mutex
	state        State
	operation    Operation
	workspace    Workspace
	beginCalls   int
	lastCommand  SwitchCommand
	removeCalls  int
	availability int
}

func (store *fakeStore) State(context.Context) (State, error) { return store.state, nil }

func (store *fakeStore) BeginSwitch(_ context.Context, command SwitchCommand) (Operation, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.beginCalls++
	store.lastCommand = command
	return store.operation, nil
}

func (store *fakeStore) Operation(context.Context, string) (Operation, error) {
	return store.operation, nil
}

func (store *fakeStore) CheckAvailability(context.Context, string, int64) (Workspace, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.availability++
	return store.workspace, nil
}

func (store *fakeStore) RemoveWorkspace(context.Context, string, int64) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.removeCalls++
	return nil
}

type fixedBackend struct{ value *url.URL }

func (backend fixedBackend) LocateRuntime(context.Context) (RuntimeAccess, error) {
	return RuntimeAccess{
		Status:      RuntimeAccessReady,
		WorkspaceID: "10000000-0000-4000-8000-000000000001",
		PollAfterMS: 1000,
		Backend:     backend.value,
	}, nil
}

type recordingBackend struct {
	access RuntimeAccess
	err    error
	calls  int
}

func (backend *recordingBackend) LocateRuntime(context.Context) (RuntimeAccess, error) {
	backend.calls++
	return backend.access, backend.err
}

func newHTTPFixture(t *testing.T, store Store, backend BackendLocator, static http.Handler) (*Handler, string) {
	t.Helper()
	bootstrap := "bootstrap_token_abcdefghijklmnopqrstuvwxyz0123456789"
	authority, err := NewSessionAuthority("instance_token_abcdefghijklmnopqrstuvwxyz0123456789", bootstrap, time.Hour)
	if err != nil {
		t.Fatalf("NewSessionAuthority() error: %v", err)
	}
	handler, err := NewHandler(HandlerOptions{
		Authority: authority, Store: store, Backend: backend, Static: static,
		ExpectedHost: "127.0.0.1:8080", Origin: "http://127.0.0.1:8080",
	})
	if err != nil {
		t.Fatalf("NewHandler() error: %v", err)
	}
	return handler, bootstrap
}

func exchangeControlSession(t *testing.T, handler http.Handler, bootstrap string) (*http.Cookie, SessionCredential) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/control/v1/sessions", nil)
	request.Host = "127.0.0.1:8080"
	request.Header.Set("Origin", "http://127.0.0.1:8080")
	request.Header.Set("Authorization", "Bearer "+bootstrap)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("session exchange status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var credential SessionCredential
	if err := json.Unmarshal(recorder.Body.Bytes(), &credential); err != nil {
		t.Fatalf("decode session credential: %v", err)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("session cookies=%d, want 1", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != ControlSessionCookie || cookie.Path != "/control/" || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("unsafe control cookie: %+v", cookie)
	}
	return cookie, credential
}

func authorizedRequest(method, path string, body io.Reader, cookie *http.Cookie, credential SessionCredential) *http.Request {
	request := httptest.NewRequest(method, "http://127.0.0.1:8080"+path, body)
	request.Host = "127.0.0.1:8080"
	request.AddCookie(cookie)
	request.Header.Set("Origin", "http://127.0.0.1:8080")
	request.Header.Set(ControlCSRFHeader, credential.CSRFToken)
	request.Header.Set("Idempotency-Key", "request-1")
	request.Header.Set("If-Match", `"7"`)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

func assertNoStoreWithoutCORS(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control=%q, want no-store", recorder.Header().Get("Cache-Control"))
	}
	for key := range recorder.Header() {
		if strings.HasPrefix(strings.ToLower(key), "access-control-allow-") {
			t.Fatalf("unexpected CORS response header %q", key)
		}
	}
}

func TestAnonymousDashboardUsesOnlyStaticAssets(t *testing.T) {
	t.Parallel()
	locator := &recordingBackend{err: errors.New("must not be called")}
	store := &fakeStore{}
	handler, _ := newHTTPFixture(t, store, locator, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/html")
		_, _ = writer.Write([]byte("dashboard-spa"))
	}))
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/dashboard", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || recorder.Body.String() != "dashboard-spa" || locator.calls != 0 {
		t.Fatalf("status=%d locator calls=%d body=%q", recorder.Code, locator.calls, recorder.Body.String())
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.beginCalls != 0 || store.availability != 0 || store.removeCalls != 0 {
		t.Fatalf("anonymous dashboard reached mutations: begin=%d availability=%d remove=%d", store.beginCalls, store.availability, store.removeCalls)
	}
}

func TestPublicRuntimeAccessProjection(t *testing.T) {
	t.Parallel()
	backendURL, err := url.Parse("http://127.0.0.1:32100")
	if err != nil {
		t.Fatalf("parse backend URL: %v", err)
	}

	tests := []struct {
		name       string
		access     RuntimeAccess
		wantStatus string
		wantID     string
	}{
		{
			name:       "waiting",
			access:     RuntimeAccess{Status: RuntimeAccessWaiting, PollAfterMS: 1500},
			wantStatus: `"waiting"`,
			wantID:     "null",
		},
		{
			name:       "ready",
			access:     RuntimeAccess{Status: RuntimeAccessReady, WorkspaceID: runtimeWorkspaceID, PollAfterMS: 1000, Backend: backendURL},
			wantStatus: `"ready"`,
			wantID:     `"` + runtimeWorkspaceID + `"`,
		},
		{
			name:       "unavailable",
			access:     RuntimeAccess{Status: RuntimeAccessUnavailable, PollAfterMS: 5000},
			wantStatus: `"unavailable"`,
			wantID:     "null",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			locator := &recordingBackend{access: test.access}
			handler, _ := newHTTPFixture(t, &fakeStore{}, locator, http.NotFoundHandler())
			request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/host/v1/runtime", nil)
			request.Host = "127.0.0.1:8080"
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "application/json" || locator.calls != 1 {
				t.Fatalf("status=%d content-type=%q calls=%d body=%s", recorder.Code, recorder.Header().Get("Content-Type"), locator.calls, recorder.Body.String())
			}
			assertNoStoreWithoutCORS(t, recorder)
			var body map[string]json.RawMessage
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode runtime response: %v", err)
			}
			if len(body) != 3 || string(body["status"]) != test.wantStatus || string(body["active_workspace_id"]) != test.wantID || string(body["poll_after_ms"]) != fmt.Sprint(test.access.PollAfterMS) {
				t.Fatalf("unexpected runtime response: %s", recorder.Body.String())
			}
			for _, forbidden := range []string{"root_path", "operation", "backend", "controller", "csrf", "token"} {
				if strings.Contains(strings.ToLower(recorder.Body.String()), forbidden) {
					t.Fatalf("runtime response leaked %q: %s", forbidden, recorder.Body.String())
				}
			}
		})
	}
}

func TestPublicRuntimeAccessChecksHostBeforeDependencies(t *testing.T) {
	t.Parallel()
	locator := &recordingBackend{err: errors.New("must not be called")}
	handler, _ := newHTTPFixture(t, &fakeStore{}, locator, http.NotFoundHandler())
	request := httptest.NewRequest(http.MethodGet, "http://localhost:8080/host/v1/runtime", nil)
	request.Host = "localhost:8080"
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden || locator.calls != 0 || !strings.Contains(recorder.Body.String(), `"code":"HOST_REQUEST_INVALID"`) {
		t.Fatalf("status=%d calls=%d body=%s", recorder.Code, locator.calls, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), request.Host) || strings.Contains(recorder.Body.String(), "127.0.0.1:8080") {
		t.Fatalf("host response reflected host details: %s", recorder.Body.String())
	}
	assertNoStoreWithoutCORS(t, recorder)
}

func TestPublicRuntimeAccessHidesDependencyErrors(t *testing.T) {
	t.Parallel()
	locator := &recordingBackend{err: &Fault{
		Code:        "PRIVATE_FAILURE",
		Message:     "failed under /Users/private/knowledge",
		OperationID: "private-operation-id",
		FieldErrors: map[string]string{"root_path": "/Users/private/knowledge"},
		Status:      http.StatusTeapot,
	}}
	handler, _ := newHTTPFixture(t, &fakeStore{}, locator, http.NotFoundHandler())
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/host/v1/runtime", nil)
	request.Host = "127.0.0.1:8080"
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable || locator.calls != 1 || !strings.Contains(recorder.Body.String(), `"code":"RUNTIME_ACCESS_UNAVAILABLE"`) {
		t.Fatalf("status=%d calls=%d body=%s", recorder.Code, locator.calls, recorder.Body.String())
	}
	for _, secret := range []string{"PRIVATE_FAILURE", "/Users/private", "private-operation-id", "root_path"} {
		if strings.Contains(recorder.Body.String(), secret) {
			t.Fatalf("runtime problem leaked %q: %s", secret, recorder.Body.String())
		}
	}
	assertNoStoreWithoutCORS(t, recorder)
}

func TestPublicRuntimeAccessRejectsInvalidLocatorProjection(t *testing.T) {
	t.Parallel()
	backendURL, err := url.Parse("http://127.0.0.1:32100")
	if err != nil {
		t.Fatalf("parse backend URL: %v", err)
	}
	tests := []RuntimeAccess{
		{Status: RuntimeAccessStatus("private"), PollAfterMS: 1000},
		{Status: RuntimeAccessReady, WorkspaceID: runtimeWorkspaceID, PollAfterMS: 1000},
		{Status: RuntimeAccessReady, WorkspaceID: "private-workspace", PollAfterMS: 1000, Backend: backendURL},
		{Status: RuntimeAccessWaiting, WorkspaceID: runtimeWorkspaceID, PollAfterMS: 1000},
		{Status: RuntimeAccessUnavailable, PollAfterMS: runtimeAccessPollMinMS - 1},
		{Status: RuntimeAccessUnavailable, PollAfterMS: runtimeAccessPollMaxMS + 1},
	}

	for index, access := range tests {
		locator := &recordingBackend{access: access}
		handler, _ := newHTTPFixture(t, &fakeStore{}, locator, http.NotFoundHandler())
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/host/v1/runtime", nil)
		request.Host = "127.0.0.1:8080"
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), `"code":"RUNTIME_ACCESS_UNAVAILABLE"`) {
			t.Fatalf("case %d status=%d body=%s", index, recorder.Code, recorder.Body.String())
		}
		assertNoStoreWithoutCORS(t, recorder)
	}
}

func TestHostNamespaceNeverFallsBackToSPA(t *testing.T) {
	t.Parallel()
	locator := &recordingBackend{access: RuntimeAccess{Status: RuntimeAccessWaiting, PollAfterMS: 1000}}
	handler, _ := newHTTPFixture(t, &fakeStore{}, locator, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/html")
		_, _ = writer.Write([]byte("SPA"))
	}))

	for _, path := range []string{"/host", "/host/", "/host/v1", "/host/v1/unknown"} {
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080"+path, nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNotFound || recorder.Header().Get("Content-Type") != "application/problem+json" || strings.Contains(recorder.Body.String(), "SPA") || !strings.Contains(recorder.Body.String(), `"code":"HOST_ROUTE_NOT_FOUND"`) {
			t.Fatalf("%s status=%d content-type=%q body=%s", path, recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
		}
		assertNoStoreWithoutCORS(t, recorder)
	}
	if locator.calls != 0 {
		t.Fatalf("unknown host routes called locator %d times", locator.calls)
	}
	request := httptest.NewRequest(http.MethodHead, "http://127.0.0.1:8080/host/v1/unknown", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound || recorder.Body.Len() != 0 {
		t.Fatalf("HEAD unknown status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	assertNoStoreWithoutCORS(t, recorder)
}

func TestPublicRuntimeAccessMethodAndHeadSemantics(t *testing.T) {
	t.Parallel()
	locator := &recordingBackend{access: RuntimeAccess{Status: RuntimeAccessWaiting, PollAfterMS: 1000}}
	handler, _ := newHTTPFixture(t, &fakeStore{}, locator, http.NotFoundHandler())

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions} {
		request := httptest.NewRequest(method, "http://127.0.0.1:8080/host/v1/runtime", nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusMethodNotAllowed || !strings.Contains(recorder.Body.String(), `"code":"HOST_METHOD_NOT_ALLOWED"`) {
			t.Fatalf("%s status=%d body=%s", method, recorder.Code, recorder.Body.String())
		}
		assertNoStoreWithoutCORS(t, recorder)
	}
	if locator.calls != 0 {
		t.Fatalf("invalid methods called locator %d times", locator.calls)
	}

	request := httptest.NewRequest(http.MethodHead, "http://127.0.0.1:8080/host/v1/runtime", nil)
	request.Host = "127.0.0.1:8080"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.Len() != 0 || locator.calls != 1 {
		t.Fatalf("HEAD status=%d calls=%d body=%q", recorder.Code, locator.calls, recorder.Body.String())
	}
	assertNoStoreWithoutCORS(t, recorder)

	locator.err = errors.New("runtime unavailable")
	request = httptest.NewRequest(http.MethodHead, "http://127.0.0.1:8080/host/v1/runtime", nil)
	request.Host = "127.0.0.1:8080"
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable || recorder.Body.Len() != 0 || locator.calls != 2 {
		t.Fatalf("failed HEAD status=%d calls=%d body=%q", recorder.Code, locator.calls, recorder.Body.String())
	}
	assertNoStoreWithoutCORS(t, recorder)
}

func TestAnonymousControlMutationsNeverReachStore(t *testing.T) {
	t.Parallel()
	store := &fakeStore{state: State{StateVersion: 7}}
	handler, _ := newHTTPFixture(t, store, UnavailableBackend{}, http.NotFoundHandler())
	tests := []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodPost, path: "/control/v1/workspace-switches", body: `{"target_kind":"registered","workspace_id":"workspace-1"}`},
		{method: http.MethodPost, path: "/control/v1/workspaces/workspace-1/availability-checks", body: `{}`},
		{method: http.MethodDelete, path: "/control/v1/workspaces/workspace-1"},
	}

	for _, test := range tests {
		var body io.Reader
		if test.body != "" {
			body = strings.NewReader(test.body)
		}
		request := httptest.NewRequest(test.method, "http://127.0.0.1:8080"+test.path, body)
		request.Host = "127.0.0.1:8080"
		request.Header.Set("Origin", "http://127.0.0.1:8080")
		request.Header.Set(ControlCSRFHeader, "anonymous-csrf")
		request.Header.Set("If-Match", `"7"`)
		request.Header.Set("Idempotency-Key", "anonymous-request")
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status=%d body=%s", test.method, test.path, recorder.Code, recorder.Body.String())
		}
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.beginCalls != 0 || store.availability != 0 || store.removeCalls != 0 {
		t.Fatalf("anonymous mutations reached store: begin=%d availability=%d remove=%d", store.beginCalls, store.availability, store.removeCalls)
	}
}

func TestControlStateRequiresSessionWithoutPathLeak(t *testing.T) {
	t.Parallel()
	privatePath := "/Users/private/knowledge"
	store := &fakeStore{state: State{
		StateVersion:     7,
		Runtime:          RuntimeState{Status: RuntimeReady, API: ProcessState{Status: ProcessReady}, Worker: ProcessState{Status: ProcessReady}},
		ActiveWorkspace:  &Workspace{WorkspaceID: "workspace-1", Name: "Private", RootPath: privatePath, Availability: AvailabilityAvailable},
		RecentWorkspaces: []Workspace{}, PollAfterMS: 1000,
	}}
	handler, bootstrap := newHTTPFixture(t, store, UnavailableBackend{}, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("static"))
	}))

	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/control/v1/state", nil)
	request.Host = "127.0.0.1:8080"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized || strings.Contains(recorder.Body.String(), privatePath) {
		t.Fatalf("anonymous state status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	cookie, _ := exchangeControlSession(t, handler, bootstrap)
	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/control/v1/state", nil)
	request.Host = "127.0.0.1:8080"
	request.AddCookie(cookie)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"7"` || !strings.Contains(recorder.Body.String(), privatePath) {
		t.Fatalf("authenticated state status=%d etag=%q body=%s", recorder.Code, recorder.Header().Get("ETag"), recorder.Body.String())
	}
}

func TestSessionExchangeRequiresExactOriginAndDoesNotConsumeOnFailure(t *testing.T) {
	t.Parallel()
	store := &fakeStore{state: State{StateVersion: 1}}
	handler, bootstrap := newHTTPFixture(t, store, UnavailableBackend{}, http.NotFoundHandler())
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/control/v1/sessions", nil)
	request.Host = "127.0.0.1:8080"
	request.Header.Set("Origin", "http://localhost:8080")
	request.Header.Set("Authorization", "Bearer "+bootstrap)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("wrong origin status=%d", recorder.Code)
	}
	exchangeControlSession(t, handler, bootstrap)
}

func TestUnsafeSwitchIsStrictVersionedAndIdempotent(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 31, 8, 0, 0, 0, time.UTC)
	store := &fakeStore{
		state:     State{StateVersion: 7},
		operation: Operation{OperationID: "operation-1", Phase: "validating", Retryable: false, StartedAt: now, UpdatedAt: now},
	}
	handler, bootstrap := newHTTPFixture(t, store, UnavailableBackend{}, http.NotFoundHandler())
	cookie, credential := exchangeControlSession(t, handler, bootstrap)
	body := `{"target_kind":"registered","workspace_id":"workspace-1"}`

	for range 2 {
		request := authorizedRequest(http.MethodPost, "/control/v1/workspace-switches", strings.NewReader(body), cookie, credential)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusAccepted || !strings.Contains(recorder.Body.String(), "operation-1") {
			t.Fatalf("switch status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	}
	store.mu.Lock()
	if store.beginCalls != 1 || store.lastCommand.WorkspaceID != "workspace-1" || store.lastCommand.ExpectedStateVersion != 7 ||
		store.lastCommand.ControllerInstanceID == "" || store.lastCommand.IdempotencyKey != "request-1" || len(store.lastCommand.RequestHash) != 64 {
		t.Fatalf("store calls=%d command=%+v", store.beginCalls, store.lastCommand)
	}
	store.mu.Unlock()

	request := authorizedRequest(http.MethodPost, "/control/v1/workspace-switches", strings.NewReader(`{"target_kind":"registered","workspace_id":"workspace-2"}`), cookie, credential)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "IDEMPOTENCY_KEY_REUSED") {
		t.Fatalf("idempotency conflict status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	request = authorizedRequest(http.MethodPost, "/control/v1/workspace-switches", strings.NewReader(body), cookie, credential)
	request.Header.Del(ControlCSRFHeader)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status=%d", recorder.Code)
	}
}

func TestNewSwitchCarriesCanonicalFingerprintAndExplicitGitChoice(t *testing.T) {
	t.Parallel()
	store := &fakeStore{
		state:     State{StateVersion: 7},
		operation: Operation{OperationID: "operation-new", Phase: "validating", StartedAt: time.Now(), UpdatedAt: time.Now()},
	}
	handler, bootstrap := newHTTPFixture(t, store, UnavailableBackend{}, http.NotFoundHandler())
	cookie, credential := exchangeControlSession(t, handler, bootstrap)
	root := t.TempDir()
	body, err := json.Marshal(map[string]any{
		"target_kind": "new", "name": "Notes", "root_path": root, "initialize_git": false,
	})
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	request := authorizedRequest(http.MethodPost, "/control/v1/workspace-switches", strings.NewReader(string(body)), cookie, credential)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("new switch status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	store.mu.Lock()
	command := store.lastCommand
	store.mu.Unlock()
	if command.RootPath == "" || len(command.RootFingerprint) != 64 || command.BindingVersion != 1 || command.InitializeGit {
		t.Fatalf("new switch command=%+v", command)
	}

	missingChoice, err := json.Marshal(map[string]any{"target_kind": "new", "name": "Notes", "root_path": root})
	if err != nil {
		t.Fatalf("encode missing choice request: %v", err)
	}
	request = authorizedRequest(http.MethodPost, "/control/v1/workspace-switches", strings.NewReader(string(missingChoice)), cookie, credential)
	request.Header.Set("Idempotency-Key", "request-2")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing initialize_git status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestControlOperationEndpointsUseExactWire(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 31, 8, 0, 0, 0, time.UTC)
	store := &fakeStore{
		state:     State{StateVersion: 7},
		operation: Operation{OperationID: "operation-1", Phase: "preparing", StartedAt: now, UpdatedAt: now},
		workspace: Workspace{WorkspaceID: "workspace-1", Name: "Docs", RootPath: "/tmp/docs", Availability: AvailabilityAvailable},
	}
	handler, bootstrap := newHTTPFixture(t, store, UnavailableBackend{}, http.NotFoundHandler())
	cookie, credential := exchangeControlSession(t, handler, bootstrap)

	get := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/control/v1/workspace-switches/operation-1", nil)
	get.Host = "127.0.0.1:8080"
	get.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, get)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"operation"`) {
		t.Fatalf("operation GET status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	availability := authorizedRequest(http.MethodPost, "/control/v1/workspaces/workspace-1/availability-checks", strings.NewReader(`{}`), cookie, credential)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, availability)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"workspace"`) {
		t.Fatalf("availability status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	remove := authorizedRequest(http.MethodDelete, "/control/v1/workspaces/workspace-1", nil, cookie, credential)
	remove.Header.Set("Idempotency-Key", "delete-1")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, remove)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("remove status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestBusinessProxyIsReadyOnlyAndNeverFallsBackToSPA(t *testing.T) {
	t.Parallel()
	store := &fakeStore{state: State{StateVersion: 1}}
	handler, _ := newHTTPFixture(t, store, UnavailableBackend{}, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/html")
		_, _ = writer.Write([]byte("SPA"))
	}))

	for _, path := range []string{"/api/v1/system/status", "/readyz"} {
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080"+path, nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusServiceUnavailable || recorder.Header().Get("Content-Type") != "application/problem+json" || recorder.Header().Get(runtimeStatusHeader) != runtimeStatusUnavailable || strings.Contains(recorder.Body.String(), "SPA") {
			t.Fatalf("%s status=%d content-type=%q body=%s", path, recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/v2/private", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound || recorder.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("unknown API status=%d content-type=%q", recorder.Code, recorder.Header().Get("Content-Type"))
	}
	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/workbench", nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "SPA" {
		t.Fatalf("SPA status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestBusinessProxyRejectsInvalidReadyProjection(t *testing.T) {
	t.Parallel()
	backendURL, err := url.Parse("http://127.0.0.1:32100")
	if err != nil {
		t.Fatalf("parse backend URL: %v", err)
	}
	locator := &recordingBackend{access: RuntimeAccess{Status: RuntimeAccessReady, PollAfterMS: 1000, Backend: backendURL}}
	handler, _ := newHTTPFixture(t, &fakeStore{}, locator, http.NotFoundHandler())
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/v1/system/status", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable || recorder.Header().Get(runtimeStatusHeader) != runtimeStatusUnavailable || locator.calls != 1 {
		t.Fatalf("status=%d runtime-status=%q calls=%d body=%s", recorder.Code, recorder.Header().Get(runtimeStatusHeader), locator.calls, recorder.Body.String())
	}
}

func TestBusinessProxyMarksOnlyControllerRuntimeFailures(t *testing.T) {
	t.Parallel()
	backendServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set(runtimeStatusHeader, runtimeStatusUnavailable)
		writeProblem(writer, responseProblem(http.StatusServiceUnavailable, "BUSINESS_DEPENDENCY_UNAVAILABLE", "业务依赖暂不可用", true, nil, ""))
	}))
	backendURL, err := url.Parse(backendServer.URL)
	if err != nil {
		backendServer.Close()
		t.Fatalf("parse backend URL: %v", err)
	}
	handler, _ := newHTTPFixture(t, &fakeStore{}, fixedBackend{value: backendURL}, http.NotFoundHandler())

	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/v1/system/status", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable || recorder.Header().Get(runtimeStatusHeader) != "" || !strings.Contains(recorder.Body.String(), "BUSINESS_DEPENDENCY_UNAVAILABLE") {
		backendServer.Close()
		t.Fatalf("business 503 status=%d runtime-status=%q body=%s", recorder.Code, recorder.Header().Get(runtimeStatusHeader), recorder.Body.String())
	}

	backendServer.Close()
	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/v1/system/status", nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadGateway || recorder.Header().Get(runtimeStatusHeader) != runtimeStatusUnavailable || !strings.Contains(recorder.Body.String(), `"code":"RUNTIME_PROXY_FAILED"`) {
		t.Fatalf("proxy failure status=%d runtime-status=%q body=%s", recorder.Code, recorder.Header().Get(runtimeStatusHeader), recorder.Body.String())
	}
}

func TestBusinessProxyStripsOnlyControllerCredentials(t *testing.T) {
	t.Parallel()
	var backendHeaders http.Header
	var backendHost string
	backendServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		backendHeaders = request.Header.Clone()
		backendHost = request.Host
		http.SetCookie(writer, &http.Cookie{Name: ControlSessionCookie, Value: "replace", Path: "/control/"})
		http.SetCookie(writer, &http.Cookie{Name: "zhixu_session", Value: "business", Path: "/"})
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: ok\n\n"))
	}))
	defer backendServer.Close()
	backendURL, err := url.Parse(backendServer.URL)
	if err != nil {
		t.Fatalf("parse backend URL: %v", err)
	}
	store := &fakeStore{state: State{StateVersion: 1}}
	handler, bootstrap := newHTTPFixture(t, store, fixedBackend{value: backendURL}, http.NotFoundHandler())
	controlCookie, _ := exchangeControlSession(t, handler, bootstrap)

	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/v1/events", nil)
	request.Host = "127.0.0.1:8080"
	request.Header.Set("Origin", "http://127.0.0.1:8080")
	request.Header.Set("Authorization", "Bearer "+bootstrap)
	request.Header.Set(ControlCSRFHeader, "must-not-leak")
	request.AddCookie(controlCookie)
	request.AddCookie(&http.Cookie{Name: "zhixu_session", Value: "business"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("proxy status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if backendHost != "127.0.0.1:8080" || backendHeaders.Get("Origin") != "http://127.0.0.1:8080" {
		t.Fatalf("business Host/Origin changed: host=%q origin=%q", backendHost, backendHeaders.Get("Origin"))
	}
	if backendHeaders.Get("Authorization") != "" || backendHeaders.Get(ControlCSRFHeader) != "" || strings.Contains(backendHeaders.Get("Cookie"), ControlSessionCookie) {
		t.Fatalf("controller credential leaked: %v", backendHeaders)
	}
	if !strings.Contains(backendHeaders.Get("Cookie"), "zhixu_session=business") {
		t.Fatalf("business cookie was stripped: %q", backendHeaders.Get("Cookie"))
	}
	setCookies := recorder.Header().Values("Set-Cookie")
	if len(setCookies) != 1 || !strings.HasPrefix(setCookies[0], "zhixu_session=") {
		t.Fatalf("controller response cookie was not filtered: %v", setCookies)
	}
}

func TestNormalBusinessAuthorizationIsPreserved(t *testing.T) {
	t.Parallel()
	received := ""
	backendServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		received = request.Header.Get("Authorization")
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer backendServer.Close()
	backendURL, _ := url.Parse(backendServer.URL)
	handler, _ := newHTTPFixture(t, &fakeStore{state: State{StateVersion: 1}}, fixedBackend{value: backendURL}, http.NotFoundHandler())
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/v1/system/status", nil)
	request.Header.Set("Authorization", "Bearer business_token_abcdefghijklmnopqrstuvwxyz")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent || received != "Bearer business_token_abcdefghijklmnopqrstuvwxyz" {
		t.Fatalf("status=%d Authorization=%q", recorder.Code, received)
	}
}

func TestBusinessProxyStripsBootstrapFromRepeatedAuthorizationHeaders(t *testing.T) {
	t.Parallel()
	received := []string(nil)
	backendServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		received = append(received, request.Header.Values("Authorization")...)
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer backendServer.Close()
	backendURL, _ := url.Parse(backendServer.URL)
	handler, bootstrap := newHTTPFixture(t, &fakeStore{state: State{StateVersion: 1}}, fixedBackend{value: backendURL}, http.NotFoundHandler())
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/v1/system/status", nil)
	request.Header.Add("Authorization", "Bearer business_token_abcdefghijklmnopqrstuvwxyz")
	request.Header.Add("Authorization", "Bearer "+bootstrap)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent || len(received) != 0 {
		t.Fatalf("status=%d Authorization=%q", recorder.Code, received)
	}
}

func TestProblemShapeDoesNotExposeInternalError(t *testing.T) {
	t.Parallel()
	response := cachedControllerError(fmt.Errorf("secret path /Users/private"))
	if strings.Contains(string(response.Body), "/Users/private") {
		t.Fatalf("problem leaked internal error: %s", response.Body)
	}
}
