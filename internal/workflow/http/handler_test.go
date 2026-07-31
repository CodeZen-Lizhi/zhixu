package workflowhttp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	authapplication "github.com/CodeZen-Lizhi/zhixu/internal/auth/application"
	authdomain "github.com/CodeZen-Lizhi/zhixu/internal/auth/domain"
	authhttp "github.com/CodeZen-Lizhi/zhixu/internal/auth/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"github.com/go-chi/chi/v5"
)

const (
	testWorkspaceID foundation.ID = "10000000-0000-4000-8000-000000000001"
	testRunID       foundation.ID = "20000000-0000-4000-8000-000000000001"
	testTaskID      foundation.ID = "30000000-0000-4000-8000-000000000001"
)

func TestStartWorkflowReturnsAcceptedRun(t *testing.T) {
	service := &fakeService{run: domain.Run{ID: testRunID, WorkspaceID: testWorkspaceID, Status: domain.StatusPending}}
	recorder := serve(t, service, http.MethodPost, "/api/v1/workspaces/"+string(testWorkspaceID)+"/workflows", `{"definition_key":"ingest","definition_version":1,"graph":{"nodes":[]},"input":{"source":"a"},"first_node_key":"scan","first_node_type":"deterministic"}`)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response startResponse
	decode(t, recorder, &response)
	if response.WorkflowRunID != string(testRunID) || response.Status != string(domain.StatusPending) || service.start.WorkspaceID != testWorkspaceID || service.start.IdempotencyKey != "test-request" {
		t.Fatalf("response=%#v command=%#v", response, service.start)
	}
}

func TestGetWorkflowRunContract(t *testing.T) {
	now := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	service := &fakeService{run: domain.Run{ID: testRunID, WorkspaceID: testWorkspaceID, DefinitionID: testTaskID, Status: domain.StatusRunning, Input: json.RawMessage(`{}`), Version: 2, CreatedAt: now, UpdatedAt: now, PauseRequestedAt: &now}}
	recorder := serve(t, service, http.MethodGet, "/api/v1/workflows/"+string(testRunID), "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response runResponse
	decode(t, recorder, &response)
	if response.ID != string(testRunID) || response.Status != string(domain.StatusRunning) || !response.PauseRequested || response.CancelRequested || service.getID != testRunID {
		t.Fatalf("response=%#v getID=%q", response, service.getID)
	}
}

func TestWorkflowListBindsStatusToCursor(t *testing.T) {
	now := time.Date(2026, 7, 22, 1, 0, 0, 0, time.UTC)
	service := &fakeService{listItems: []domain.RunListItem{{Run: domain.Run{ID: testRunID, WorkspaceID: testWorkspaceID, Status: domain.RunStatusRunning, CreatedAt: now, UpdatedAt: now, CancelRequestedAt: &now}}}, listHasMore: true}
	first := serve(t, service, http.MethodGet, "/api/v1/workspaces/"+string(testWorkspaceID)+"/workflows?status=running&limit=1", "")
	if first.Code != http.StatusOK || service.listQuery.Status != domain.RunStatusRunning {
		t.Fatalf("status=%d query=%+v body=%s", first.Code, service.listQuery, first.Body.String())
	}
	var page runPageResponse
	decode(t, first, &page)
	if len(page.Items) != 1 || page.Items[0].PauseRequested || !page.Items[0].CancelRequested {
		t.Fatalf("pending projection=%+v", page.Items)
	}
	legacyCursor := cursorWithoutWorkflowKind(t, page.NextCursor)
	legacy := serve(t, service, http.MethodGet, "/api/v1/workspaces/"+string(testWorkspaceID)+"/workflows?status=running&limit=1&cursor="+legacyCursor, "")
	if legacy.Code != http.StatusBadRequest || service.listCalls != 1 {
		t.Fatalf("legacy status=%d calls=%d body=%s", legacy.Code, service.listCalls, legacy.Body.String())
	}
	second := serve(t, service, http.MethodGet, "/api/v1/workspaces/"+string(testWorkspaceID)+"/workflows?status=failed&limit=1&cursor="+page.NextCursor, "")
	if second.Code != http.StatusBadRequest || service.listCalls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", second.Code, service.listCalls, second.Body.String())
	}
}

func cursorWithoutWorkflowKind(t *testing.T, cursor string) string {
	t.Helper()
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(decoded, &payload); err != nil {
		t.Fatal(err)
	}
	delete(payload, "version")
	delete(payload, "kind")
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func TestSubmitHumanDecisionContract(t *testing.T) {
	now := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	service := &fakeService{task: domain.HumanTask{ID: testTaskID, RunID: testRunID, NodeRunID: testWorkspaceID, Status: domain.HumanTaskSubmitted, TargetVersion: 3, Decision: json.RawMessage(`{"action":"approve"}`), SubmittedAt: &now}}
	recorder := serve(t, service, http.MethodPost, "/api/v1/workflows/"+string(testRunID)+"/human-tasks/"+string(testTaskID)+"/decision", `{"target_version":3,"decision":{"action":"approve"}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response humanTaskResponse
	decode(t, recorder, &response)
	if response.Status != string(domain.HumanTaskSubmitted) || service.submitTaskID != testTaskID || service.submitRunID != testRunID {
		t.Fatalf("response=%#v service=%#v", response, service)
	}
}

func TestWorkflowControlContract(t *testing.T) {
	for _, action := range []string{"pause", "resume", "cancel"} {
		t.Run(action, func(t *testing.T) {
			service := &fakeService{controlResult: application.RunControlResult{WorkflowRunID: testRunID, Status: domain.RunStatusPaused, Version: 4, StatusURL: "/api/v1/workflows/" + string(testRunID), PauseRequested: true, CancelRequested: false}}
			recorder := serve(t, service, http.MethodPost, "/api/v1/workflows/"+string(testRunID)+"/"+action, `{"expected_version":3}`)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			var response controlResponse
			decode(t, recorder, &response)
			if response.WorkflowRunID != string(testRunID) || response.Version != 4 || !response.PauseRequested || response.CancelRequested || service.control.WorkflowRunID != testRunID || service.control.ExpectedVersion != 3 || service.control.IdempotencyKey != "test-request" || service.controlAction != action {
				t.Fatalf("response=%+v service=%+v", response, service)
			}
		})
	}
}

func TestWorkflowControlRequiresIdempotencyAndPositiveVersion(t *testing.T) {
	service := &fakeService{}
	router := chi.NewRouter()
	router.Route("/api/v1", func(api chi.Router) { NewHandler(service).Routes(api) })
	request := httptest.NewRequest(http.MethodPost, "/api/v1/workflows/"+string(testRunID)+"/pause", strings.NewReader(`{"expected_version":1}`))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/workflows/"+string(testRunID)+"/pause", strings.NewReader(`{"expected_version":0}`))
	request.Header.Set("Idempotency-Key", "key")
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestWorkflowCommandsCarryBearerPrincipalCapabilities(t *testing.T) {
	principal := authdomain.Principal{
		Kind:   authdomain.PrincipalAPIToken,
		ID:     foundation.ID("40000000-0000-4000-8000-000000000001"),
		Scopes: []capability.Capability{capability.WriteProposal},
	}
	authHandler, err := authhttp.NewHandler(workflowAuthService{principal: principal}, authhttp.Options{
		SecureCookie: true, AllowedOrigins: []string{"https://app.example.test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 23, 8, 0, 0, 0, time.UTC)
	service := &fakeService{
		controlResult: application.RunControlResult{WorkflowRunID: testRunID, Status: domain.RunStatusPaused, Version: 2, StatusURL: "/api/v1/workflows/" + string(testRunID), PauseRequested: true},
		task:          domain.HumanTask{ID: testTaskID, RunID: testRunID, NodeRunID: testWorkspaceID, Status: domain.HumanTaskSubmitted, TargetVersion: 1, Decision: json.RawMessage(`{"approved":true}`), SubmittedAt: &now},
	}
	router := chi.NewRouter()
	router.Route("/api/v1", func(api chi.Router) {
		api.Group(func(protected chi.Router) {
			protected.Use(authHandler.Middleware)
			NewHandler(service).Routes(protected)
		})
	})

	for _, test := range []struct {
		path string
		body string
	}{
		{path: "/api/v1/workflows/" + string(testRunID) + "/pause", body: `{"expected_version":1}`},
		{path: "/api/v1/workflows/" + string(testRunID) + "/human-tasks/" + string(testTaskID) + "/decision", body: `{"target_version":1,"decision":{"approved":true}}`},
	} {
		request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
		request.Header.Set("Authorization", "Bearer low-scope-token")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "bearer-command")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("path=%s status=%d body=%s", test.path, response.Code, response.Body.String())
		}
	}
	if len(service.control.CallerCapabilities) != 1 || service.control.CallerCapabilities[0] != capability.WriteProposal {
		t.Fatalf("control caller capabilities=%v", service.control.CallerCapabilities)
	}
	if len(service.human.CallerCapabilities) != 1 || service.human.CallerCapabilities[0] != capability.WriteProposal {
		t.Fatalf("human caller capabilities=%v", service.human.CallerCapabilities)
	}
}

func TestWorkflowHandlerMapsInvalidAndConflictErrors(t *testing.T) {
	tests := []struct {
		name, path, body, code string
		status                 int
		err                    error
	}{
		{name: "invalid JSON", path: "/api/v1/workspaces/" + string(testWorkspaceID) + "/workflows", body: `{"definition_key":`, status: http.StatusBadRequest, code: "INVALID_JSON"},
		{name: "conflict", path: "/api/v1/workflows/" + string(testRunID), status: http.StatusConflict, code: "WORKFLOW_CONFLICT", err: foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_CONFLICT", false, errors.New("internal"))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeService{err: test.err}
			method := http.MethodGet
			if test.body != "" {
				method = http.MethodPost
			}
			recorder := serve(t, service, method, test.path, test.body)
			if recorder.Code != test.status {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			var problem struct {
				ErrorCode string `json:"error_code"`
			}
			decode(t, recorder, &problem)
			if problem.ErrorCode != test.code {
				t.Fatalf("problem=%#v", problem)
			}
		})
	}
}

type fakeService struct {
	run                       domain.Run
	task                      domain.HumanTask
	err                       error
	start                     application.StartCommand
	getID                     foundation.ID
	submitTaskID, submitRunID foundation.ID
	submitWorkspaceID         foundation.ID
	control                   application.RunControlCommand
	human                     application.HumanDecisionCommand
	controlResult             application.RunControlResult
	controlAction             string
	listItems                 []domain.RunListItem
	listHasMore               bool
	listQuery                 domain.RunListQuery
	listCalls                 int
}

func (f *fakeService) ListRuns(_ context.Context, query domain.RunListQuery) ([]domain.RunListItem, bool, error) {
	f.listCalls++
	f.listQuery = query
	return append([]domain.RunListItem(nil), f.listItems...), f.listHasMore, f.err
}

func (f *fakeService) Pause(_ context.Context, command application.RunControlCommand) (application.RunControlResult, error) {
	return f.controlCall(command, "pause")
}
func (f *fakeService) Resume(_ context.Context, command application.RunControlCommand) (application.RunControlResult, error) {
	return f.controlCall(command, "resume")
}
func (f *fakeService) Cancel(_ context.Context, command application.RunControlCommand) (application.RunControlResult, error) {
	return f.controlCall(command, "cancel")
}
func (f *fakeService) controlCall(command application.RunControlCommand, action string) (application.RunControlResult, error) {
	f.control, f.controlAction = command, action
	if command.IdempotencyKey == "" {
		return application.RunControlResult{}, foundation.NewError(foundation.ErrorInvalidInput, "IDEMPOTENCY_KEY_REQUIRED", false, errors.New("missing key"))
	}
	if command.ExpectedVersion < 1 {
		return application.RunControlResult{}, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_CONTROL_INVALID", false, errors.New("invalid version"))
	}
	return f.controlResult, f.err
}

func (f *fakeService) Start(_ context.Context, command application.StartCommand) (domain.Run, error) {
	f.start = command
	return f.run, f.err
}
func (f *fakeService) Get(_ context.Context, id foundation.ID) (domain.Run, error) {
	f.getID = id
	return f.run, f.err
}
func (f *fakeService) SubmitHumanDecision(_ context.Context, taskID foundation.ID, _ int64, _ json.RawMessage, workspaceID, runID foundation.ID) (domain.HumanTask, error) {
	f.submitTaskID, f.submitWorkspaceID, f.submitRunID = taskID, workspaceID, runID
	return f.task, f.err
}

func (f *fakeService) SubmitRuntimeHumanDecision(_ context.Context, command application.HumanDecisionCommand) (application.HumanTransitionResult, error) {
	f.human = command
	f.submitTaskID, f.submitRunID = command.TaskID, command.RunID
	return application.HumanTransitionResult{Task: f.task, Run: f.run, Node: domain.NodeRun{ID: f.task.NodeRunID, RunID: f.task.RunID, Status: domain.NodeStatusSucceeded}}, f.err
}

type workflowAuthService struct{ principal authdomain.Principal }

func (service workflowAuthService) ExchangeBootstrap(context.Context, string) (authapplication.SessionCredential, error) {
	return authapplication.SessionCredential{}, nil
}

func (service workflowAuthService) AuthenticateSession(context.Context, string, string, bool) (authdomain.Principal, error) {
	return service.principal, nil
}

func (service workflowAuthService) CurrentSession(context.Context, string, string, bool) (authdomain.SessionInfo, error) {
	return authdomain.SessionInfo{}, nil
}

func (service workflowAuthService) RotateSession(context.Context, authdomain.Principal, string, string, bool) (authapplication.SessionCredential, error) {
	return authapplication.SessionCredential{}, nil
}

func (service workflowAuthService) AuthenticateAPIToken(context.Context, string) (authdomain.Principal, error) {
	return service.principal, nil
}

func (service workflowAuthService) CreateAPIToken(context.Context, authdomain.Principal, string, []capability.Capability, time.Duration) (authapplication.APITokenCredential, error) {
	return authapplication.APITokenCredential{}, nil
}

func (service workflowAuthService) ListAPITokens(context.Context, authdomain.Principal, authdomain.APITokenListQuery) (authdomain.APITokenListPage, error) {
	return authdomain.APITokenListPage{}, nil
}

func (service workflowAuthService) RevokeSession(context.Context, authdomain.Principal, foundation.ID) error {
	return nil
}

func (service workflowAuthService) RevokeAPIToken(context.Context, authdomain.Principal, foundation.ID) error {
	return nil
}

func serve(t *testing.T, service Service, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	router := chi.NewRouter()
	router.Route("/api/v1", func(api chi.Router) { NewHandler(service).Routes(api) })
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Idempotency-Key", "test-request")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func decode(t *testing.T, recorder *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.NewDecoder(recorder.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}

var _ Service = (*fakeService)(nil)
