package workflowhttp

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
	service := &fakeService{run: domain.Run{ID: testRunID, WorkspaceID: testWorkspaceID, DefinitionID: testTaskID, Status: domain.StatusRunning, Input: json.RawMessage(`{}`), Version: 2, CreatedAt: now, UpdatedAt: now}}
	recorder := serve(t, service, http.MethodGet, "/api/v1/workflows/"+string(testRunID), "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response runResponse
	decode(t, recorder, &response)
	if response.ID != string(testRunID) || response.Status != string(domain.StatusRunning) || service.getID != testRunID {
		t.Fatalf("response=%#v getID=%q", response, service.getID)
	}
}

func TestSubmitHumanDecisionContract(t *testing.T) {
	now := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	service := &fakeService{task: domain.HumanTask{ID: testTaskID, RunID: testRunID, NodeRunID: testWorkspaceID, Status: domain.HumanTaskSubmitted, TargetVersion: 3, Decision: json.RawMessage(`{"action":"approve"}`), SubmittedAt: &now}}
	recorder := serve(t, service, http.MethodPost, "/api/v1/workflows/"+string(testRunID)+"/human-tasks/"+string(testTaskID)+"/decision", `{"workspace_id":"`+string(testWorkspaceID)+`","target_version":3,"decision":{"action":"approve"}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response humanTaskResponse
	decode(t, recorder, &response)
	if response.Status != string(domain.HumanTaskSubmitted) || service.submitTaskID != testTaskID || service.submitRunID != testRunID || service.submitWorkspaceID != testWorkspaceID {
		t.Fatalf("response=%#v service=%#v", response, service)
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
