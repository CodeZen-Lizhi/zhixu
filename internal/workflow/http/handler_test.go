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
	f.submitTaskID, f.submitRunID = command.TaskID, command.RunID
	return application.HumanTransitionResult{Task: f.task, Run: f.run, Node: domain.NodeRun{ID: f.task.NodeRunID, RunID: f.task.RunID, Status: domain.NodeStatusSucceeded}}, f.err
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
