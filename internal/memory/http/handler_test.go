package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	authapp "github.com/CodeZen-Lizhi/zhixu/internal/auth/application"
	authdomain "github.com/CodeZen-Lizhi/zhixu/internal/auth/domain"
	authhttp "github.com/CodeZen-Lizhi/zhixu/internal/auth/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	memoryapp "github.com/CodeZen-Lizhi/zhixu/internal/memory/application"
	memorydomain "github.com/CodeZen-Lizhi/zhixu/internal/memory/domain"
	"github.com/gin-gonic/gin"
)

const (
	memoryHTTPWorkspaceID = "61000000-0000-4000-8000-000000000001"
	memoryHTTPID          = "61000000-0000-4000-8000-000000000002"
	memoryHTTPOwnerID     = "61000000-0000-4000-8000-000000000003"
	memoryHTTPTaskID      = "61000000-0000-4000-8000-000000000004"
	memoryHTTPOtherID     = "61000000-0000-4000-8000-000000000005"
)

func TestHandlerRoutesBindAuthenticatedOwnerAndLifecycleCommands(t *testing.T) {
	service := newMemoryHTTPFake(t)
	router := memoryHTTPAuthenticatedRouter(t, service, memoryHTTPPrincipal())

	requests := []struct {
		name   string
		method string
		path   string
		body   string
		status int
	}{
		{
			name:   "create candidate",
			method: http.MethodPost,
			path:   "/api/v1/memories",
			body:   memoryHTTPCandidateWithTaskJSON(),
			status: http.StatusCreated,
		},
		{
			name:   "list",
			method: http.MethodGet,
			path:   "/api/v1/memories?workspace_id=" + memoryHTTPWorkspaceID,
			status: http.StatusOK,
		},
		{
			name:   "detail",
			method: http.MethodGet,
			path:   "/api/v1/memories/" + memoryHTTPID + "?workspace_id=" + memoryHTTPWorkspaceID,
			status: http.StatusOK,
		},
		{
			name:   "confirm",
			method: http.MethodPost,
			path:   "/api/v1/memories/" + memoryHTTPID + "/confirm",
			body:   memoryHTTPVersionJSON(1),
			status: http.StatusOK,
		},
		{
			name:   "edit",
			method: http.MethodPut,
			path:   "/api/v1/memories/" + memoryHTTPID,
			body:   memoryHTTPEditJSON(2),
			status: http.StatusOK,
		},
		{
			name:   "pause",
			method: http.MethodPost,
			path:   "/api/v1/memories/" + memoryHTTPID + "/pause",
			body:   memoryHTTPVersionJSON(2),
			status: http.StatusOK,
		},
		{
			name:   "resume",
			method: http.MethodPost,
			path:   "/api/v1/memories/" + memoryHTTPID + "/resume",
			body:   memoryHTTPVersionJSON(3),
			status: http.StatusOK,
		},
		{
			name:   "delete",
			method: http.MethodDelete,
			path:   "/api/v1/memories/" + memoryHTTPID,
			body:   memoryHTTPVersionJSON(2),
			status: http.StatusOK,
		},
	}
	for _, test := range requests {
		t.Run(test.name, func(t *testing.T) {
			response := memoryHTTPServe(router, test.method, test.path, test.body, true)
			if response.Code != test.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.status, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control=%q", response.Header().Get("Cache-Control"))
			}
			if strings.Contains(response.Body.String(), `"owner"`) || strings.Contains(response.Body.String(), `"confirmed_by"`) {
				t.Fatalf("response leaked a credential principal: %s", response.Body.String())
			}
		})
	}

	wantOwner := memoryHTTPMemoryOwner()
	if service.create.Owner != wantOwner || service.confirm.Scope.Owner != wantOwner || service.edit.Scope.Owner != wantOwner ||
		service.pause.Scope.Owner != wantOwner || service.resume.Scope.Owner != wantOwner || service.delete.Scope.Owner != wantOwner ||
		service.get.Scope.Owner != wantOwner || service.list.Scope.Owner != wantOwner {
		t.Fatalf("authenticated owner was not bound to all calls: %+v", service)
	}
	if service.create.WorkspaceID != memoryHTTPIDValue(memoryHTTPWorkspaceID) || service.confirm.ExpectedVersion != 1 ||
		service.edit.ExpectedVersion != 2 || service.pause.ExpectedVersion != 2 || service.resume.ExpectedVersion != 3 || service.delete.ExpectedVersion != 2 {
		t.Fatalf("command bindings were lost: create=%+v confirm=%+v edit=%+v pause=%+v resume=%+v delete=%+v", service.create, service.confirm, service.edit, service.pause, service.resume, service.delete)
	}
	if service.create.Owner.ID == foundation.ID(memoryHTTPOtherID) || service.create.TaskScopeID == nil || *service.create.TaskScopeID != memoryHTTPIDValue(memoryHTTPTaskID) {
		t.Fatalf("candidate ownership or task scope was not mapped safely: %+v", service.create)
	}
	if service.create.Source != (memorydomain.Source{Type: memorydomain.SourceUser, Ref: userManualSource}) {
		t.Fatalf("candidate source = %+v, want server-owned USER provenance", service.create.Source)
	}
}

func TestHandlerRejectsMissingAuthenticationAndForgedOwner(t *testing.T) {
	service := newMemoryHTTPFake(t)
	plainRouter := gin.New()
	NewHandler(service, time.Second).Routes(plainRouter)
	var router http.Handler = plainRouter

	response := memoryHTTPServe(router, http.MethodPost, "/memories", memoryHTTPCandidateJSON(), false)
	requireMemoryHTTPProblem(t, response, http.StatusForbidden, errorCodeAuthRequired, false)
	if service.calls != 0 {
		t.Fatalf("unauthenticated request reached service: %d", service.calls)
	}

	service = newMemoryHTTPFake(t)
	router = memoryHTTPAuthenticatedRouter(t, service, memoryHTTPPrincipal())
	body := strings.TrimSuffix(memoryHTTPCandidateJSON(), "}") + `,"owner":{"kind":"SESSION","id":"` + memoryHTTPOtherID + `"}}`
	response = memoryHTTPServe(router, http.MethodPost, "/api/v1/memories", body, true)
	requireMemoryHTTPProblem(t, response, http.StatusBadRequest, "INVALID_JSON", false)
	if service.calls != 0 {
		t.Fatalf("forged owner reached service: %d", service.calls)
	}
}

func TestHandlerStrictInputAndReplayContracts(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		path        string
		body        string
		withKey     bool
		contentType bool
		status      int
		code        string
	}{
		{
			name:        "missing idempotency key",
			method:      http.MethodPost,
			path:        "/api/v1/memories",
			body:        memoryHTTPCandidateJSON(),
			contentType: true,
			status:      http.StatusBadRequest,
			code:        memorydomain.ErrorCodeInvalid,
		},
		{
			name:    "wrong media type",
			method:  http.MethodPost,
			path:    "/api/v1/memories",
			body:    memoryHTTPCandidateJSON(),
			withKey: true,
			status:  http.StatusUnsupportedMediaType,
			code:    "UNSUPPORTED_MEDIA_TYPE",
		},
		{
			name:        "unknown object field",
			method:      http.MethodPost,
			path:        "/api/v1/memories",
			body:        strings.TrimSuffix(memoryHTTPCandidateJSON(), "}") + `,"future":true}`,
			withKey:     true,
			contentType: true,
			status:      http.StatusBadRequest,
			code:        "INVALID_JSON",
		},
		{
			name:        "forged agent source",
			method:      http.MethodPost,
			path:        "/api/v1/memories",
			body:        strings.TrimSuffix(memoryHTTPCandidateJSON(), "}") + `,"source":{"type":"AGENT","ref":"agent:forged"}}`,
			withKey:     true,
			contentType: true,
			status:      http.StatusBadRequest,
			code:        "INVALID_JSON",
		},
		{
			name:        "forged interview source",
			method:      http.MethodPost,
			path:        "/api/v1/memories",
			body:        strings.TrimSuffix(memoryHTTPCandidateJSON(), "}") + `,"source":{"type":"INTERVIEW","ref":"interview:forged"}}`,
			withKey:     true,
			contentType: true,
			status:      http.StatusBadRequest,
			code:        "INVALID_JSON",
		},
		{
			name:        "edit cannot replace source",
			method:      http.MethodPut,
			path:        "/api/v1/memories/" + memoryHTTPID,
			body:        strings.TrimSuffix(memoryHTTPEditJSON(2), "}") + `,"source":{"type":"USER","ref":"user:forged"}}`,
			withKey:     true,
			contentType: true,
			status:      http.StatusBadRequest,
			code:        "INVALID_JSON",
		},
		{
			name:        "duplicate nested content field",
			method:      http.MethodPost,
			path:        "/api/v1/memories",
			body:        `{"workspace_id":"` + memoryHTTPWorkspaceID + `","type":"PREFERENCE","content":{"topic":"go","topic":"rust"}}`,
			withKey:     true,
			contentType: true,
			status:      http.StatusBadRequest,
			code:        "INVALID_JSON",
		},
		{
			name:        "empty memory content",
			method:      http.MethodPost,
			path:        "/api/v1/memories",
			body:        `{"workspace_id":"` + memoryHTTPWorkspaceID + `","type":"PREFERENCE","content":{}}`,
			withKey:     true,
			contentType: true,
			status:      http.StatusBadRequest,
			code:        memorydomain.ErrorCodeInvalid,
		},
		{
			name:        "missing expected version",
			method:      http.MethodPost,
			path:        "/api/v1/memories/" + memoryHTTPID + "/confirm",
			body:        `{"workspace_id":"` + memoryHTTPWorkspaceID + `"}`,
			withKey:     true,
			contentType: true,
			status:      http.StatusBadRequest,
			code:        memorydomain.ErrorCodeInvalid,
		},
		{
			name:   "invalid resource id",
			method: http.MethodGet,
			path:   "/api/v1/memories/not-a-uuid?workspace_id=" + memoryHTTPWorkspaceID,
			status: http.StatusBadRequest,
			code:   memorydomain.ErrorCodeInvalid,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newMemoryHTTPFake(t)
			router := memoryHTTPAuthenticatedRouter(t, service, memoryHTTPPrincipal())
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			request.Header.Set("Authorization", "Bearer test-token")
			if test.contentType {
				request.Header.Set("Content-Type", "application/json")
			}
			if test.withKey {
				request.Header.Set("Idempotency-Key", "memory-http-test")
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			requireMemoryHTTPProblem(t, response, test.status, test.code, false)
			if service.calls != 0 {
				t.Fatalf("invalid request reached service: %d", service.calls)
			}
		})
	}

	service := newMemoryHTTPFake(t)
	service.candidateResult.Replayed = true
	router := memoryHTTPAuthenticatedRouter(t, service, memoryHTTPPrincipal())
	response := memoryHTTPServe(router, http.MethodPost, "/api/v1/memories", memoryHTTPCandidateJSON(), true)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"replayed":true`) {
		t.Fatalf("candidate replay status=%d body=%s", response.Code, response.Body.String())
	}

	service = newMemoryHTTPFake(t)
	router = memoryHTTPAuthenticatedRouter(t, service, memoryHTTPPrincipal())
	expiry := "2026-08-01T08:00:00Z"
	body := `{"workspace_id":"` + memoryHTTPWorkspaceID + `","type":"EPISODIC","content":{"topic":"incident"},"expires_at":"` + expiry + `"}`
	response = memoryHTTPServe(router, http.MethodPost, "/api/v1/memories", body, true)
	if response.Code != http.StatusCreated || service.create.ExpiresAt == nil || service.create.ExpiresAt.Format(time.RFC3339) != expiry {
		t.Fatalf("episodic candidate status=%d expires=%v body=%s", response.Code, service.create.ExpiresAt, response.Body.String())
	}
}

func TestHandlerListCursorBindsWorkspaceAndFilters(t *testing.T) {
	service := newMemoryHTTPFake(t)
	service.page = memoryapp.ListPage{
		Items: []memorydomain.Memory{service.candidate},
		Next:  &memoryapp.Cursor{UpdatedAt: service.candidate.UpdatedAt, ID: service.candidate.ID},
	}
	router := memoryHTTPAuthenticatedRouter(t, service, memoryHTTPPrincipal())

	first := memoryHTTPServe(router, http.MethodGet, "/api/v1/memories?workspace_id="+memoryHTTPWorkspaceID+"&type=PREFERENCE&status=CANDIDATE", "", true)
	if first.Code != http.StatusOK {
		t.Fatalf("first list status=%d body=%s", first.Code, first.Body.String())
	}
	var body struct {
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &body); err != nil || body.NextCursor == "" {
		t.Fatalf("decode next cursor err=%v body=%s", err, first.Body.String())
	}

	second := memoryHTTPServe(router, http.MethodGet, "/api/v1/memories?workspace_id="+memoryHTTPWorkspaceID+"&type=PREFERENCE&status=CANDIDATE&cursor="+body.NextCursor, "", true)
	if second.Code != http.StatusOK || service.list.After == nil || service.list.After.ID != service.candidate.ID {
		t.Fatalf("second list status=%d after=%+v body=%s", second.Code, service.list.After, second.Body.String())
	}
	listCalls := service.listCalls
	wrongFilter := memoryHTTPServe(router, http.MethodGet, "/api/v1/memories?workspace_id="+memoryHTTPWorkspaceID+"&type=GOAL&status=CANDIDATE&cursor="+body.NextCursor, "", true)
	requireMemoryHTTPProblem(t, wrongFilter, http.StatusBadRequest, errorCodeCursorInvalid, false)
	if service.listCalls != listCalls {
		t.Fatalf("filter-mismatched cursor reached service: before=%d after=%d", listCalls, service.listCalls)
	}
}

func TestHandlerErrorAndResultValidationMapping(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		status    int
		code      string
		retryable bool
	}{
		{"not found", foundation.NewError(foundation.ErrorNotFound, memorydomain.ErrorCodeNotFound, false, nil), http.StatusNotFound, memorydomain.ErrorCodeNotFound, false},
		{"owner denied", memorydomain.OwnerDeniedError("denied"), http.StatusForbidden, memorydomain.ErrorCodeOwnerDenied, false},
		{"version conflict", foundation.NewError(foundation.ErrorVersionConflict, memorydomain.ErrorCodeVersionConflict, false, nil), http.StatusConflict, memorydomain.ErrorCodeVersionConflict, false},
		{"expired", memorydomain.ExpiredError("expired"), http.StatusConflict, memorydomain.ErrorCodeExpired, false},
		{"dependency", foundation.NewError(foundation.ErrorDependencyUnavailable, memorydomain.ErrorCodeUnavailable, true, nil), http.StatusServiceUnavailable, memorydomain.ErrorCodeUnavailable, true},
		{"persistence invalid", foundation.NewError(foundation.ErrorConsistencyViolation, memorydomain.ErrorCodePersistenceInvalid, false, nil), http.StatusInternalServerError, memorydomain.ErrorCodePersistenceInvalid, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newMemoryHTTPFake(t)
			service.err = test.err
			router := memoryHTTPAuthenticatedRouter(t, service, memoryHTTPPrincipal())
			response := memoryHTTPServe(router, http.MethodGet, "/api/v1/memories/"+memoryHTTPID+"?workspace_id="+memoryHTTPWorkspaceID, "", true)
			requireMemoryHTTPProblem(t, response, test.status, test.code, test.retryable)
		})
	}

	service := newMemoryHTTPFake(t)
	service.getMemory.Owner.ID = memoryHTTPIDValue(memoryHTTPOtherID)
	router := memoryHTTPAuthenticatedRouter(t, service, memoryHTTPPrincipal())
	response := memoryHTTPServe(router, http.MethodGet, "/api/v1/memories/"+memoryHTTPID+"?workspace_id="+memoryHTTPWorkspaceID, "", true)
	requireMemoryHTTPProblem(t, response, http.StatusInternalServerError, errorCodeResultInvalid, false)

	service = newMemoryHTTPFake(t)
	service.preserveCandidateResult = true
	router = memoryHTTPAuthenticatedRouter(t, service, memoryHTTPPrincipal())
	response = memoryHTTPServe(router, http.MethodPost, "/api/v1/memories", memoryHTTPCandidateWithTaskJSON(), true)
	requireMemoryHTTPProblem(t, response, http.StatusInternalServerError, errorCodeResultInvalid, false)

	router = memoryHTTPAuthenticatedRouter(t, nil, memoryHTTPPrincipal())
	response = memoryHTTPServe(router, http.MethodGet, "/api/v1/memories/"+memoryHTTPID+"?workspace_id="+memoryHTTPWorkspaceID, "", true)
	requireMemoryHTTPProblem(t, response, http.StatusServiceUnavailable, errorCodeUnavailable, true)
}

type memoryHTTPFakeService struct {
	candidate               memorydomain.Memory
	candidateResult         memoryapp.CommandResult
	confirmResult           memoryapp.CommandResult
	editResult              memoryapp.CommandResult
	pauseResult             memoryapp.CommandResult
	resumeResult            memoryapp.CommandResult
	deleteResult            memoryapp.CommandResult
	getMemory               memorydomain.Memory
	page                    memoryapp.ListPage
	err                     error
	preserveCandidateResult bool

	create  memoryapp.CreateCandidateCommand
	confirm memoryapp.TransitionCommand
	edit    memoryapp.EditCommand
	pause   memoryapp.TransitionCommand
	resume  memoryapp.TransitionCommand
	delete  memoryapp.TransitionCommand
	get     struct {
		Scope    memoryapp.Scope
		MemoryID foundation.ID
	}
	list      memoryapp.ListQuery
	calls     int
	listCalls int
}

func newMemoryHTTPFake(t *testing.T) *memoryHTTPFakeService {
	t.Helper()
	now := time.Date(2026, 7, 27, 8, 0, 0, 0, time.UTC)
	owner := memoryHTTPMemoryOwner()
	candidate, err := memorydomain.NewCandidate(memorydomain.CandidateInput{
		ID:          memoryHTTPIDValue(memoryHTTPID),
		WorkspaceID: memoryHTTPIDValue(memoryHTTPWorkspaceID),
		Owner:       owner,
		Type:        memorydomain.TypePreference,
		Content:     json.RawMessage(`{"focus":"go"}`),
		Source:      memorydomain.Source{Type: memorydomain.SourceUser, Ref: userManualSource},
		CreatedAt:   now,
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := memorydomain.Confirm(candidate, owner, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	edited, err := memorydomain.Edit(active, owner, memorydomain.EditInput{
		Content: json.RawMessage(`{"focus":"systems"}`),
	}, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	paused, err := memorydomain.Pause(active, owner, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := memorydomain.Resume(paused, owner, now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := memorydomain.Delete(active, owner, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return &memoryHTTPFakeService{
		candidate:       candidate,
		candidateResult: memoryapp.CommandResult{Memory: candidate},
		confirmResult:   memoryapp.CommandResult{Memory: active},
		editResult:      memoryapp.CommandResult{Memory: edited},
		pauseResult:     memoryapp.CommandResult{Memory: paused},
		resumeResult:    memoryapp.CommandResult{Memory: resumed},
		deleteResult:    memoryapp.CommandResult{Memory: deleted},
		getMemory:       candidate,
		page:            memoryapp.ListPage{Items: []memorydomain.Memory{candidate}},
	}
}

func (service *memoryHTTPFakeService) CreateCandidate(_ context.Context, command memoryapp.CreateCandidateCommand) (memoryapp.CommandResult, error) {
	service.calls++
	service.create = command
	result := service.candidateResult
	if !service.preserveCandidateResult {
		result.Memory.Type = command.Type
		result.Memory.Content = cloneJSON(command.Content)
		result.Memory.Source = command.Source
		result.Memory.TaskScopeID = cloneID(command.TaskScopeID)
		result.Memory.ExpiresAt = cloneTime(command.ExpiresAt)
	}
	return result, service.err
}

func (service *memoryHTTPFakeService) Confirm(_ context.Context, command memoryapp.TransitionCommand) (memoryapp.CommandResult, error) {
	service.calls++
	service.confirm = command
	return service.confirmResult, service.err
}

func (service *memoryHTTPFakeService) Edit(_ context.Context, command memoryapp.EditCommand) (memoryapp.CommandResult, error) {
	service.calls++
	service.edit = command
	result := service.editResult
	result.Memory.Content = cloneJSON(command.Content)
	result.Memory.TaskScopeID = cloneID(command.TaskScopeID)
	result.Memory.ExpiresAt = cloneTime(command.ExpiresAt)
	return result, service.err
}

func (service *memoryHTTPFakeService) Pause(_ context.Context, command memoryapp.TransitionCommand) (memoryapp.CommandResult, error) {
	service.calls++
	service.pause = command
	return service.pauseResult, service.err
}

func (service *memoryHTTPFakeService) Resume(_ context.Context, command memoryapp.TransitionCommand) (memoryapp.CommandResult, error) {
	service.calls++
	service.resume = command
	return service.resumeResult, service.err
}

func (service *memoryHTTPFakeService) Delete(_ context.Context, command memoryapp.TransitionCommand) (memoryapp.CommandResult, error) {
	service.calls++
	service.delete = command
	return service.deleteResult, service.err
}

func (service *memoryHTTPFakeService) Get(_ context.Context, scope memoryapp.Scope, memoryID foundation.ID) (memorydomain.Memory, error) {
	service.calls++
	service.get.Scope, service.get.MemoryID = scope, memoryID
	return service.getMemory, service.err
}

func (service *memoryHTTPFakeService) List(_ context.Context, query memoryapp.ListQuery) (memoryapp.ListPage, error) {
	service.calls++
	service.listCalls++
	service.list = query
	return service.page, service.err
}

func memoryHTTPAuthenticatedRouter(t *testing.T, service Service, principal authdomain.Principal) http.Handler {
	t.Helper()
	authHandler, err := authhttp.NewHandler(memoryHTTPAuthService{principal: principal}, authhttp.Options{
		SecureCookie:   true,
		AllowedOrigins: []string{"https://app.example.test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	protected := router.Group("/api/v1")
	protected.Use(authHandler.Middleware)
	NewHandler(service, time.Second).Routes(protected)
	return router
}

func memoryHTTPServe(router http.Handler, method, path, body string, authenticated bool) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if authenticated {
		request.Header.Set("Authorization", "Bearer test-token")
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet && method != http.MethodHead {
		request.Header.Set("Idempotency-Key", "memory-http-test")
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func requireMemoryHTTPProblem(t *testing.T, response *httptest.ResponseRecorder, status int, code string, retryable bool) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status=%d want=%d body=%s", response.Code, status, response.Body.String())
	}
	var problem httpapi.Problem
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil || problem.ErrorCode != code || problem.Retryable != retryable {
		t.Fatalf("problem=%+v err=%v want code=%q retryable=%t raw=%s", problem, err, code, retryable, response.Body.String())
	}
}

func memoryHTTPPrincipal() authdomain.Principal {
	return authdomain.Principal{
		Kind:   authdomain.PrincipalAPIToken,
		ID:     memoryHTTPIDValue(memoryHTTPOwnerID),
		Scopes: []capability.Capability{capability.ReadLocal, capability.WriteKnowledge, capability.WriteProposal},
	}
}

func memoryHTTPMemoryOwner() memorydomain.Principal {
	return memorydomain.Principal{Kind: memorydomain.PrincipalUser, ID: memorydomain.SingleUserOwnerID}
}

func memoryHTTPIDValue(raw string) foundation.ID {
	value, err := foundation.ParseID(raw)
	if err != nil {
		panic(err)
	}
	return value
}

func memoryHTTPCandidateJSON() string {
	return `{"workspace_id":"` + memoryHTTPWorkspaceID + `","type":"PREFERENCE","content":{"focus":"go"}}`
}

func memoryHTTPCandidateWithTaskJSON() string {
	return strings.TrimSuffix(memoryHTTPCandidateJSON(), "}") + `,"task_scope_id":"` + memoryHTTPTaskID + `"}`
}

func memoryHTTPEditJSON(version int64) string {
	return `{"workspace_id":"` + memoryHTTPWorkspaceID + `","expected_version":` + strconvFormatInt(version) + `,"content":{"focus":"systems"}}`
}

func memoryHTTPVersionJSON(version int64) string {
	return `{"workspace_id":"` + memoryHTTPWorkspaceID + `","expected_version":` + strconvFormatInt(version) + `}`
}

func strconvFormatInt(value int64) string {
	return strconv.FormatInt(value, 10)
}

type memoryHTTPAuthService struct{ principal authdomain.Principal }

func (service memoryHTTPAuthService) ExchangeBootstrap(context.Context, string) (authapp.SessionCredential, error) {
	return authapp.SessionCredential{}, nil
}

func (service memoryHTTPAuthService) AuthenticateSession(context.Context, string, string, bool) (authdomain.Principal, error) {
	return service.principal, nil
}

func (service memoryHTTPAuthService) CurrentSession(context.Context, string, string, bool) (authdomain.SessionInfo, error) {
	return authdomain.SessionInfo{}, nil
}

func (service memoryHTTPAuthService) RotateSession(context.Context, authdomain.Principal, string, string, bool) (authapp.SessionCredential, error) {
	return authapp.SessionCredential{}, nil
}

func (service memoryHTTPAuthService) AuthenticateAPIToken(context.Context, string) (authdomain.Principal, error) {
	return service.principal, nil
}

func (service memoryHTTPAuthService) CreateAPIToken(context.Context, authdomain.Principal, string, []capability.Capability, time.Duration) (authapp.APITokenCredential, error) {
	return authapp.APITokenCredential{}, nil
}

func (service memoryHTTPAuthService) ListAPITokens(context.Context, authdomain.Principal, authdomain.APITokenListQuery) (authdomain.APITokenListPage, error) {
	return authdomain.APITokenListPage{}, nil
}

func (service memoryHTTPAuthService) RevokeSession(context.Context, authdomain.Principal, foundation.ID) error {
	return nil
}

func (service memoryHTTPAuthService) RevokeAPIToken(context.Context, authdomain.Principal, foundation.ID) error {
	return nil
}
