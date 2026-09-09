package interviewhttp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	authapp "github.com/CodeZen-Lizhi/zhixu/internal/auth/application"
	authdomain "github.com/CodeZen-Lizhi/zhixu/internal/auth/domain"
	authhttp "github.com/CodeZen-Lizhi/zhixu/internal/auth/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
	"github.com/gin-gonic/gin"
)

func TestInterviewHTTPRoutesMapCommands(t *testing.T) {
	fixture := newInterviewHTTPFixture(t)
	service := &fakeInterviewService{
		startResult: interviewapp.StartResult{Session: fixture.activeSession, Questions: []domain.Question{fixture.pendingQuestion}},
		getResult: interviewapp.Snapshot{
			Session: fixture.activeSession, Questions: []domain.Question{fixture.answeredQuestion}, Turns: []domain.Turn{fixture.turn},
		},
		submitResult: interviewapp.SubmitTurnResult{Turn: fixture.turn},
		completeResult: interviewapp.CompleteResult{
			Session: fixture.completedSession, Report: fixture.report, Path: fixture.path, Steps: []domain.PathStep{fixture.pendingStep},
		},
		pathStatusResult:      interviewapp.PathStatusResult{Path: fixture.pausedPath},
		pathStepResult:        interviewapp.PathStepResult{Path: fixture.stepUpdatedPath, Step: fixture.inProgressStep},
		memoryCandidateResult: interviewapp.MemoryCandidateResult{MemoryID: fixture.memoryID},
	}
	router := protectedInterviewRouter(t, service, defaultInterviewPrincipal())

	t.Run("start", func(t *testing.T) {
		response := serveInterview(t, router, http.MethodPost, "/api/v1/review/interviews", startJSON(fixture), nil)
		if response.Code != http.StatusCreated {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		for _, forbidden := range []string{`"answer_points"`, `"evidence"`} {
			if strings.Contains(response.Body.String(), forbidden) {
				t.Fatalf("start response leaked %s: %s", forbidden, response.Body.String())
			}
		}
		if service.start.WorkspaceID != fixture.workspaceID || service.start.Config.SchemaVersion != fixture.activeSession.Config.SchemaVersion || service.start.Config.Role != fixture.activeSession.Config.Role || len(service.start.Config.Scope.ClaimIDs) != 1 || service.start.Config.Scope.ClaimIDs[0] != fixture.claimID || service.start.IdempotencyKey != "interview-http-test" {
			t.Fatalf("start command=%+v", service.start)
		}
	})

	t.Run("get", func(t *testing.T) {
		response := serveInterview(t, router, http.MethodGet, "/api/v1/review/interviews/"+string(fixture.sessionID)+"?workspace_id="+string(fixture.workspaceID), "", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		if service.getWorkspaceID != fixture.workspaceID || service.getSessionID != fixture.sessionID {
			t.Fatalf("get command workspace=%s session=%s", service.getWorkspaceID, service.getSessionID)
		}
	})

	t.Run("submit turn", func(t *testing.T) {
		body := `{"workspace_id":"` + string(fixture.workspaceID) + `","question_id":"` + string(fixture.questionID) + `","user_answer":"private answer"}`
		response := serveInterview(t, router, http.MethodPost, "/api/v1/review/interviews/"+string(fixture.sessionID)+"/turns", body, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		if service.submit.WorkspaceID != fixture.workspaceID || service.submit.SessionID != fixture.sessionID || service.submit.QuestionID != fixture.questionID || service.submit.UserAnswer != "private answer" || service.submit.IdempotencyKey != "interview-http-test" {
			t.Fatalf("submit command=%+v", service.submit)
		}
	})

	t.Run("complete", func(t *testing.T) {
		body := `{"workspace_id":"` + string(fixture.workspaceID) + `","manual_end":true}`
		response := serveInterview(t, router, http.MethodPost, "/api/v1/review/interviews/"+string(fixture.sessionID)+"/complete", body, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		if service.complete.WorkspaceID != fixture.workspaceID || service.complete.SessionID != fixture.sessionID || !service.complete.ManualEnd || service.complete.IdempotencyKey != "interview-http-test" {
			t.Fatalf("complete command=%+v", service.complete)
		}
	})

	t.Run("update path status", func(t *testing.T) {
		body := `{"workspace_id":"` + string(fixture.workspaceID) + `","expected_version":1,"status":"PAUSED"}`
		response := serveInterview(t, router, http.MethodPut, "/api/v1/review/learning-paths/"+string(fixture.pathID)+"/status", body, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		if service.pathStatus.WorkspaceID != fixture.workspaceID || service.pathStatus.PathID != fixture.pathID || service.pathStatus.ExpectedVersion != 1 || service.pathStatus.Status != domain.PathStatusPaused || service.pathStatus.IdempotencyKey != "interview-http-test" {
			t.Fatalf("path status command=%+v", service.pathStatus)
		}
	})

	t.Run("update path step", func(t *testing.T) {
		body := `{"workspace_id":"` + string(fixture.workspaceID) + `","expected_version":1,"status":"IN_PROGRESS"}`
		response := serveInterview(t, router, http.MethodPut, "/api/v1/review/learning-paths/"+string(fixture.pathID)+"/steps/"+string(fixture.stepID), body, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		if service.pathStep.WorkspaceID != fixture.workspaceID || service.pathStep.PathID != fixture.pathID || service.pathStep.StepID != fixture.stepID || service.pathStep.ExpectedVersion != 1 || service.pathStep.Status != domain.StepStatusInProgress || service.pathStep.IdempotencyKey != "interview-http-test" {
			t.Fatalf("path step command=%+v", service.pathStep)
		}
	})

	t.Run("suggest memory candidate", func(t *testing.T) {
		body := `{"workspace_id":"` + string(fixture.workspaceID) + `"}`
		path := "/api/v1/review/interviews/" + string(fixture.sessionID) + "/learning-paths/" + string(fixture.pathID) + "/steps/" + string(fixture.stepID) + "/memory-candidate"
		response := serveInterview(t, protectedInterviewRouter(t, service, unregisteredWriteInterviewPrincipal()), http.MethodPost, path, body, nil)
		if response.Code != http.StatusCreated {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		if service.memoryCandidate.WorkspaceID != fixture.workspaceID || service.memoryCandidate.SessionID != fixture.sessionID ||
			service.memoryCandidate.PathID != fixture.pathID || service.memoryCandidate.StepID != fixture.stepID ||
			service.memoryCandidate.IdempotencyKey != "interview-http-test" {
			t.Fatalf("memory candidate command=%+v", service.memoryCandidate)
		}
		var result memoryCandidateResponse
		decodeInterviewJSON(t, response, &result)
		if result.MemoryID != string(fixture.memoryID) || result.Replayed {
			t.Fatalf("memory candidate response=%+v", result)
		}
	})
}

func TestInterviewHTTPStartReplayUsesOK(t *testing.T) {
	fixture := newInterviewHTTPFixture(t)
	service := &fakeInterviewService{startResult: interviewapp.StartResult{
		Session: fixture.activeSession, Questions: []domain.Question{fixture.pendingQuestion}, Replayed: true,
	}}
	response := serveInterview(t, protectedInterviewRouter(t, service, defaultInterviewPrincipal()), http.MethodPost, "/api/v1/review/interviews", startJSON(fixture), nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body startResponse
	decodeInterviewJSON(t, response, &body)
	if !body.Replayed {
		t.Fatalf("response=%+v", body)
	}
}

func TestInterviewHTTPListsSessionsWithWorkspaceBoundCursorAndNoAnswerFacts(t *testing.T) {
	fixture := newInterviewHTTPFixture(t)
	service := &fakeInterviewService{listResult: interviewapp.SessionListPage{
		Items: []domain.Session{fixture.activeSession},
		Next:  &interviewapp.SessionCursor{StartedAt: fixture.activeSession.StartedAt, ID: fixture.activeSession.ID},
	}}
	router := protectedInterviewRouter(t, service, defaultInterviewPrincipal())
	path := "/api/v1/review/interviews?workspace_id=" + string(fixture.workspaceID) + "&limit=1"
	response := serveInterview(t, router, http.MethodGet, path, "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if service.listQuery.WorkspaceID != fixture.workspaceID || service.listQuery.Limit != 1 || service.listQuery.After != nil {
		t.Fatalf("list query=%+v", service.listQuery)
	}
	for _, forbidden := range []string{"answer_points", "evidence", "user_answer", "request_hash", "idempotency_key", "receipt", "private answer"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("list response leaked %q: %s", forbidden, response.Body.String())
		}
	}
	var page sessionListResponse
	decodeInterviewJSON(t, response, &page)
	if page.WorkspaceID != string(fixture.workspaceID) || len(page.Items) != 1 || page.Items[0].ID != string(fixture.sessionID) || page.NextCursor == "" {
		t.Fatalf("page=%+v", page)
	}

	response = serveInterview(t, router, http.MethodGet, path+"&cursor="+url.QueryEscape(page.NextCursor), "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("cursor status=%d body=%s", response.Code, response.Body.String())
	}
	if service.listQuery.After == nil || service.listQuery.After.ID != fixture.sessionID || !service.listQuery.After.StartedAt.Equal(fixture.activeSession.StartedAt) {
		t.Fatalf("cursor query=%+v", service.listQuery)
	}

	otherWorkspace := interviewHTTPID(t, 99)
	response = serveInterview(t, router, http.MethodGet, "/api/v1/review/interviews?workspace_id="+string(otherWorkspace)+"&limit=1&cursor="+url.QueryEscape(page.NextCursor), "", nil)
	requireInterviewProblem(t, response, http.StatusBadRequest, errorCodeCursorInvalid, false)
	if service.listCalls != 2 {
		t.Fatalf("cross-workspace cursor reached service: calls=%d", service.listCalls)
	}
}

func TestInterviewHTTPFailsClosedAndRejectsInvalidWire(t *testing.T) {
	fixture := newInterviewHTTPFixture(t)
	baseService := func() *fakeInterviewService {
		return &fakeInterviewService{startResult: interviewapp.StartResult{Session: fixture.activeSession, Questions: []domain.Question{fixture.pendingQuestion}}}
	}

	t.Run("missing principal", func(t *testing.T) {
		service := baseService()
		router := gin.New()
		NewHandler(service, 0).Routes(router.Group("/api/v1"))
		response := serveInterview(t, router, http.MethodPost, "/api/v1/review/interviews", startJSON(fixture), nil)
		requireInterviewProblem(t, response, http.StatusForbidden, errorCodeAuthRequired, false)
		if service.startCalls != 0 {
			t.Fatalf("service was called without a principal")
		}
	})

	tests := []struct {
		name      string
		method    string
		path      string
		body      string
		setup     func(*http.Request)
		principal *authdomain.Principal
	}{
		{
			name: "unknown field", method: http.MethodPost, path: "/api/v1/review/interviews",
			body: strings.TrimSuffix(startJSON(fixture), "}") + `,"score":{"value":1}}`,
		},
		{
			name: "nested duplicate key", method: http.MethodPost, path: "/api/v1/review/interviews",
			body: `{"workspace_id":"` + string(fixture.workspaceID) + `","config":{"schema_version":"interview/v1","role":"backend engineer","scope":{"claim_ids":["` + string(fixture.claimID) + `"],"claim_ids":["` + string(fixture.claimID) + `"]},"difficulty":"INTERMEDIATE","duration_minutes":30,"question_count":1,"max_follow_ups":1}}`,
		},
		{
			name: "wrong media type", method: http.MethodPost, path: "/api/v1/review/interviews", body: startJSON(fixture),
			setup: func(request *http.Request) { request.Header.Set("Content-Type", "text/plain") },
		},
		{
			name: "missing idempotency key", method: http.MethodPost, path: "/api/v1/review/interviews", body: startJSON(fixture),
			setup: func(request *http.Request) { request.Header.Del("Idempotency-Key") },
		},
		{
			name: "duplicate idempotency key", method: http.MethodPost, path: "/api/v1/review/interviews", body: startJSON(fixture),
			setup: func(request *http.Request) { request.Header.Add("Idempotency-Key", "another-key") },
		},
		{
			name: "client supplied score", method: http.MethodPost, path: "/api/v1/review/interviews/" + string(fixture.sessionID) + "/turns",
			body: `{"workspace_id":"` + string(fixture.workspaceID) + `","question_id":"` + string(fixture.questionID) + `","user_answer":"answer","score":{"correctness":1}}`,
		},
		{
			name: "zero path version", method: http.MethodPut, path: "/api/v1/review/learning-paths/" + string(fixture.pathID) + "/status",
			body: `{"workspace_id":"` + string(fixture.workspaceID) + `","expected_version":0,"status":"PAUSED"}`,
		},
		{
			name: "client supplied memory provenance", method: http.MethodPost,
			path:      "/api/v1/review/interviews/" + string(fixture.sessionID) + "/learning-paths/" + string(fixture.pathID) + "/steps/" + string(fixture.stepID) + "/memory-candidate",
			body:      `{"workspace_id":"` + string(fixture.workspaceID) + `","source":{"type":"AGENT","ref":"forged"}}`,
			principal: interviewPrincipalPointer(unregisteredWriteInterviewPrincipal()),
		},
		{
			name: "memory candidate missing idempotency key", method: http.MethodPost,
			path:      "/api/v1/review/interviews/" + string(fixture.sessionID) + "/learning-paths/" + string(fixture.pathID) + "/steps/" + string(fixture.stepID) + "/memory-candidate",
			body:      `{"workspace_id":"` + string(fixture.workspaceID) + `"}`,
			setup:     func(request *http.Request) { request.Header.Del("Idempotency-Key") },
			principal: interviewPrincipalPointer(unregisteredWriteInterviewPrincipal()),
		},
		{
			name: "repeated workspace query", method: http.MethodGet, path: "/api/v1/review/interviews/" + string(fixture.sessionID) + "?workspace_id=" + string(fixture.workspaceID) + "&workspace_id=" + string(fixture.workspaceID),
		},
		{
			name: "list missing workspace", method: http.MethodGet, path: "/api/v1/review/interviews?limit=20",
		},
		{
			name: "list unknown query", method: http.MethodGet, path: "/api/v1/review/interviews?workspace_id=" + string(fixture.workspaceID) + "&status=ACTIVE",
		},
		{
			name: "list limit too large", method: http.MethodGet, path: "/api/v1/review/interviews?workspace_id=" + string(fixture.workspaceID) + "&limit=101",
		},
		{
			name: "list cursor malformed", method: http.MethodGet, path: "/api/v1/review/interviews?workspace_id=" + string(fixture.workspaceID) + "&cursor=not-base64",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := baseService()
			principal := defaultInterviewPrincipal()
			if test.principal != nil {
				principal = *test.principal
			}
			response := serveInterview(t, protectedInterviewRouter(t, service, principal), test.method, test.path, test.body, test.setup)
			if response.Code != http.StatusBadRequest && response.Code != http.StatusUnsupportedMediaType {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control=%q", response.Header().Get("Cache-Control"))
			}
			if service.totalCalls() != 0 {
				t.Fatalf("service was called for invalid input: %d", service.totalCalls())
			}
		})
	}
}

func TestInterviewHTTPRedactsSensitiveFieldsAndRejectsInconsistentResults(t *testing.T) {
	fixture := newInterviewHTTPFixture(t)
	service := &fakeInterviewService{getResult: interviewapp.Snapshot{
		Session: fixture.activeSession, Questions: []domain.Question{fixture.answeredQuestion}, Turns: []domain.Turn{fixture.turn},
	}}
	router := protectedInterviewRouter(t, service, defaultInterviewPrincipal())
	response := serveInterview(t, router, http.MethodGet, "/api/v1/review/interviews/"+string(fixture.sessionID)+"?workspace_id="+string(fixture.workspaceID), "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	for _, forbidden := range []string{
		"answer_points", "user_answer", "private answer", "interview-http-test", strings.Repeat("c", 64), "request_hash", "idempotency_key",
		"index_version_id", "chunk_id", domain.EvidenceSchemaVersion,
	} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, response.Body.String())
		}
	}
	if !strings.Contains(response.Body.String(), `"schema_version":"`+EvidenceSchemaVersion+`"`) {
		t.Fatalf("response did not project public evidence schema %q: %s", EvidenceSchemaVersion, response.Body.String())
	}

	pendingWithTurn := &fakeInterviewService{getResult: interviewapp.Snapshot{
		Session: fixture.activeSession, Questions: []domain.Question{fixture.pendingQuestion}, Turns: []domain.Turn{fixture.turn},
	}}
	response = serveInterview(t, protectedInterviewRouter(t, pendingWithTurn, defaultInterviewPrincipal()), http.MethodGet, "/api/v1/review/interviews/"+string(fixture.sessionID)+"?workspace_id="+string(fixture.workspaceID), "", nil)
	requireInterviewProblem(t, response, http.StatusInternalServerError, errorCodeResultInvalid, false)
	if strings.Contains(response.Body.String(), `"evidence"`) {
		t.Fatalf("inconsistent pending snapshot leaked evidence: %s", response.Body.String())
	}

	broken := fixture.activeSession
	broken.WorkspaceID = interviewHTTPID(t, 99)
	service = &fakeInterviewService{startResult: interviewapp.StartResult{Session: broken, Questions: []domain.Question{fixture.pendingQuestion}}}
	response = serveInterview(t, protectedInterviewRouter(t, service, defaultInterviewPrincipal()), http.MethodPost, "/api/v1/review/interviews", startJSON(fixture), nil)
	requireInterviewProblem(t, response, http.StatusInternalServerError, errorCodeResultInvalid, false)
}

func TestInterviewHTTPErrorMappingAndTimeout(t *testing.T) {
	fixture := newInterviewHTTPFixture(t)
	newStartService := func(err error) *fakeInterviewService {
		return &fakeInterviewService{startResult: interviewapp.StartResult{Session: fixture.activeSession, Questions: []domain.Question{fixture.pendingQuestion}}, startErr: err}
	}
	tests := []struct {
		name      string
		service   *fakeInterviewService
		status    int
		code      string
		retryable bool
	}{
		{
			name: "conflict", service: newStartService(domain.ConflictError(domain.ErrorCodeIdempotencyConflict, "conflict")),
			status: http.StatusConflict, code: domain.ErrorCodeIdempotencyConflict,
		},
		{
			name: "dependency", service: newStartService(domain.UnavailableError(domain.ErrorCodeScorerUnavailable, "unavailable")),
			status: http.StatusServiceUnavailable, code: domain.ErrorCodeScorerUnavailable, retryable: true,
		},
		{
			name: "deadline", service: newStartService(context.DeadlineExceeded),
			status: http.StatusServiceUnavailable, code: domain.ErrorCodeDependencyUnavailable, retryable: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := serveInterview(t, protectedInterviewRouter(t, test.service, defaultInterviewPrincipal()), http.MethodPost, "/api/v1/review/interviews", startJSON(fixture), nil)
			requireInterviewProblem(t, response, test.status, test.code, test.retryable)
		})
	}

	response := serveInterview(t, protectedInterviewRouter(t, nil, defaultInterviewPrincipal()), http.MethodPost, "/api/v1/review/interviews", startJSON(fixture), nil)
	requireInterviewProblem(t, response, http.StatusServiceUnavailable, errorCodeUnavailable, true)

	waiting := &fakeInterviewService{waitForStartContext: true}
	response = serveInterview(t, protectedInterviewRouter(t, waiting, defaultInterviewPrincipal()), http.MethodPost, "/api/v1/review/interviews", startJSON(fixture), nil)
	requireInterviewProblem(t, response, http.StatusServiceUnavailable, domain.ErrorCodeDependencyUnavailable, true)
	if !waiting.startHadDeadline {
		t.Fatal("start service did not receive a deadline-bound context")
	}
}

type fakeInterviewService struct {
	startResult           interviewapp.StartResult
	getResult             interviewapp.Snapshot
	listResult            interviewapp.SessionListPage
	submitResult          interviewapp.SubmitTurnResult
	completeResult        interviewapp.CompleteResult
	pathStepResult        interviewapp.PathStepResult
	pathStatusResult      interviewapp.PathStatusResult
	memoryCandidateResult interviewapp.MemoryCandidateResult

	startErr           error
	getErr             error
	listErr            error
	submitErr          error
	completeErr        error
	pathStepErr        error
	pathStatusErr      error
	memoryCandidateErr error

	waitForStartContext bool
	startHadDeadline    bool

	start           interviewapp.StartCommand
	getWorkspaceID  foundation.ID
	getSessionID    foundation.ID
	listQuery       interviewapp.SessionListQuery
	submit          interviewapp.SubmitTurnCommand
	complete        interviewapp.CompleteCommand
	pathStep        interviewapp.UpdatePathStepCommand
	pathStatus      interviewapp.UpdatePathStatusCommand
	memoryCandidate interviewapp.SuggestMemoryCandidateCommand

	startCalls, getCalls, listCalls, submitCalls, completeCalls, pathStepCalls, pathStatusCalls, memoryCandidateCalls int
}

func (service *fakeInterviewService) Start(ctx context.Context, command interviewapp.StartCommand) (interviewapp.StartResult, error) {
	service.startCalls++
	service.start = command
	if service.waitForStartContext {
		_, service.startHadDeadline = ctx.Deadline()
		<-ctx.Done()
		return interviewapp.StartResult{}, ctx.Err()
	}
	return service.startResult, service.startErr
}

func (service *fakeInterviewService) Get(_ context.Context, workspaceID, sessionID foundation.ID) (interviewapp.Snapshot, error) {
	service.getCalls++
	service.getWorkspaceID, service.getSessionID = workspaceID, sessionID
	return service.getResult, service.getErr
}

func (service *fakeInterviewService) List(_ context.Context, query interviewapp.SessionListQuery) (interviewapp.SessionListPage, error) {
	service.listCalls++
	service.listQuery = query
	return service.listResult, service.listErr
}

func (service *fakeInterviewService) SubmitTurn(_ context.Context, command interviewapp.SubmitTurnCommand) (interviewapp.SubmitTurnResult, error) {
	service.submitCalls++
	service.submit = command
	return service.submitResult, service.submitErr
}

func (service *fakeInterviewService) Complete(_ context.Context, command interviewapp.CompleteCommand) (interviewapp.CompleteResult, error) {
	service.completeCalls++
	service.complete = command
	return service.completeResult, service.completeErr
}

func (service *fakeInterviewService) UpdatePathStep(_ context.Context, command interviewapp.UpdatePathStepCommand) (interviewapp.PathStepResult, error) {
	service.pathStepCalls++
	service.pathStep = command
	return service.pathStepResult, service.pathStepErr
}

func (service *fakeInterviewService) UpdatePathStatus(_ context.Context, command interviewapp.UpdatePathStatusCommand) (interviewapp.PathStatusResult, error) {
	service.pathStatusCalls++
	service.pathStatus = command
	return service.pathStatusResult, service.pathStatusErr
}

func (service *fakeInterviewService) SuggestMemoryCandidate(_ context.Context, command interviewapp.SuggestMemoryCandidateCommand) (interviewapp.MemoryCandidateResult, error) {
	service.memoryCandidateCalls++
	service.memoryCandidate = command
	return service.memoryCandidateResult, service.memoryCandidateErr
}

func (service *fakeInterviewService) totalCalls() int {
	return service.startCalls + service.getCalls + service.listCalls + service.submitCalls + service.completeCalls + service.pathStepCalls + service.pathStatusCalls + service.memoryCandidateCalls
}

type interviewHTTPFixture struct {
	workspaceID      foundation.ID
	claimID          foundation.ID
	sessionID        foundation.ID
	questionID       foundation.ID
	pathID           foundation.ID
	stepID           foundation.ID
	memoryID         foundation.ID
	activeSession    domain.Session
	completedSession domain.Session
	pendingQuestion  domain.Question
	answeredQuestion domain.Question
	turn             domain.Turn
	report           domain.Report
	path             domain.LearningPath
	pendingStep      domain.PathStep
	pausedPath       domain.LearningPath
	stepUpdatedPath  domain.LearningPath
	inProgressStep   domain.PathStep
}

func newInterviewHTTPFixture(t *testing.T) interviewHTTPFixture {
	t.Helper()
	workspaceID := interviewHTTPID(t, 1)
	claimID := interviewHTTPID(t, 2)
	sessionID := interviewHTTPID(t, 3)
	questionID := interviewHTTPID(t, 4)
	turnID := interviewHTTPID(t, 5)
	reportID := interviewHTTPID(t, 6)
	pathID := interviewHTTPID(t, 7)
	stepID := interviewHTTPID(t, 8)
	memoryID := interviewHTTPID(t, 13)
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	answeredAt := now.Add(time.Minute)
	config := domain.Config{
		SchemaVersion: domain.SchemaVersion, Role: "backend engineer", Scope: domain.Scope{ClaimIDs: []foundation.ID{claimID}},
		Difficulty: domain.DifficultyIntermediate, DurationMinutes: 30, QuestionCount: 1, MaxFollowUps: 1,
	}
	evidence := domain.EvidenceRef{
		SchemaVersion: domain.EvidenceSchemaVersion, ClaimID: claimID, IndexVersionID: interviewHTTPID(t, 11), ChunkID: interviewHTTPID(t, 12), SourceVersionID: interviewHTTPID(t, 9), SourceSpanID: interviewHTTPID(t, 10),
		EvidenceHash: strings.Repeat("a", 64), SupportType: "SUPPORTS",
	}
	activeSession := domain.Session{ID: sessionID, WorkspaceID: workspaceID, Config: config, Status: domain.SessionStatusActive, Version: 1, StartedAt: now}
	pendingQuestion := domain.Question{
		ID: questionID, WorkspaceID: workspaceID, SessionID: sessionID, QuestionNo: 1, ClaimID: claimID, Prompt: "Explain idempotency.",
		AnswerPoints: []string{"A repeated request reaches one durable result."}, Evidence: []domain.EvidenceRef{evidence}, Status: domain.QuestionStatusPending,
		Fingerprint: strings.Repeat("b", 64), CreatedAt: now,
	}
	answeredQuestion := pendingQuestion
	answeredQuestion.Status, answeredQuestion.AnsweredAt = domain.QuestionStatusAnswered, &answeredAt
	score := domain.Score{
		SchemaVersion: domain.ScoreSchemaVersion, Correctness: interviewHTTPScoreDimension(0.8), Coverage: interviewHTTPScoreDimension(0.8),
		Boundaries: interviewHTTPScoreDimension(0.8), Clarity: interviewHTTPScoreDimension(0.8), Evidence: []domain.EvidenceRef{evidence},
	}
	turn := domain.Turn{
		ID: turnID, WorkspaceID: workspaceID, SessionID: sessionID, QuestionID: questionID, IdempotencyKey: "interview-http-test", RequestHash: strings.Repeat("c", 64),
		UserAnswer: "private answer", Score: score, ScorerVersion: "deterministic/v1", CreatedAt: answeredAt,
	}
	report := domain.Report{
		ID: reportID, WorkspaceID: workspaceID, SessionID: sessionID, SchemaVersion: domain.ReportSchemaVersion,
		Summary:   domain.ReportSummary{QuestionsTotal: 1, AnsweredTotal: 1, Correctness: 0.8, Coverage: 0.8, Boundaries: 0.8, Clarity: 0.8},
		Strengths: []domain.Finding{{ClaimID: claimID, Detail: "The answer was evidence-backed.", Evidence: []domain.EvidenceRef{evidence}}}, Evidence: []domain.EvidenceRef{evidence},
		Artifact: domain.ArtifactBinding{Kind: interviewapp.ArtifactKindInterviewDocument, ArtifactID: interviewHTTPID(t, 11), RevisionID: interviewHTTPID(t, 12), ArtifactVersion: 1}, CreatedAt: answeredAt,
	}
	path := domain.LearningPath{
		ID: pathID, WorkspaceID: workspaceID, SessionID: sessionID, ReportID: reportID,
		Artifact: domain.ArtifactBinding{Kind: interviewapp.ArtifactKindLearningPath, ArtifactID: interviewHTTPID(t, 13), RevisionID: interviewHTTPID(t, 14), ArtifactVersion: 1},
		Status:   domain.PathStatusActive, Version: 1, CreatedAt: answeredAt, UpdatedAt: answeredAt,
	}
	pendingStep := domain.PathStep{
		ID: stepID, WorkspaceID: workspaceID, PathID: pathID, StepNo: 1, ClaimID: claimID, SourceVersionID: evidence.SourceVersionID, SourceSpanID: evidence.SourceSpanID,
		EvidenceHash: evidence.EvidenceHash, Title: "Review idempotency", Rationale: "Revisit the evidence.", Status: domain.StepStatusPending, Version: 1, CreatedAt: answeredAt, UpdatedAt: answeredAt,
	}
	completedSession := activeSession
	completedSession.Status, completedSession.Version, completedSession.EndedAt = domain.SessionStatusCompleted, 2, &answeredAt
	pausedPath := path
	pausedPath.Status, pausedPath.Version, pausedPath.UpdatedAt = domain.PathStatusPaused, 2, answeredAt.Add(time.Minute)
	stepUpdatedPath := path
	stepUpdatedPath.Version, stepUpdatedPath.UpdatedAt = 2, answeredAt.Add(time.Minute)
	inProgressStep := pendingStep
	inProgressStep.Status, inProgressStep.Version, inProgressStep.UpdatedAt = domain.StepStatusInProgress, 2, answeredAt.Add(time.Minute)
	return interviewHTTPFixture{
		workspaceID: workspaceID, claimID: claimID, sessionID: sessionID, questionID: questionID, pathID: pathID, stepID: stepID, memoryID: memoryID,
		activeSession: activeSession, completedSession: completedSession, pendingQuestion: pendingQuestion, answeredQuestion: answeredQuestion,
		turn: turn, report: report, path: path, pendingStep: pendingStep, pausedPath: pausedPath, stepUpdatedPath: stepUpdatedPath, inProgressStep: inProgressStep,
	}
}

func interviewHTTPScoreDimension(value float64) domain.ScoreDimension {
	return domain.ScoreDimension{Value: value, Rationale: "deterministic score rationale"}
}

func interviewHTTPID(t *testing.T, value int) foundation.ID {
	t.Helper()
	id, err := foundation.ParseID(fmt.Sprintf("00000000-0000-4000-8000-%012x", value))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func startJSON(fixture interviewHTTPFixture) string {
	return `{"workspace_id":"` + string(fixture.workspaceID) + `","config":{"schema_version":"interview/v1","role":"backend engineer","scope":{"claim_ids":["` + string(fixture.claimID) + `"]},"difficulty":"INTERMEDIATE","duration_minutes":30,"question_count":1,"max_follow_ups":1}}`
}

func protectedInterviewRouter(t *testing.T, service Service, principal authdomain.Principal) http.Handler {
	t.Helper()
	authHandler, err := authhttp.NewHandler(interviewHTTPAuthService{principal: principal}, authhttp.Options{SecureCookie: true, AllowedOrigins: []string{"https://app.example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	protected := router.Group("/api/v1")
	protected.Use(authHandler.Middleware)
	handler := NewHandler(service, 25*time.Millisecond)
	handler.Routes(protected)
	protectedV2 := router.Group("/api/v2")
	protectedV2.Use(authHandler.Middleware)
	handler.RoutesV2(protectedV2)
	return router
}

func defaultInterviewPrincipal() authdomain.Principal {
	return authdomain.Principal{
		Kind: authdomain.PrincipalAPIToken, ID: foundation.ID("10000000-0000-4000-8000-000000000099"),
		Scopes: []capability.Capability{capability.ReadLocal, capability.WriteProposal},
	}
}

func unregisteredWriteInterviewPrincipal() authdomain.Principal {
	principal := defaultInterviewPrincipal()
	principal.Scopes = []capability.Capability{capability.ReadLocal, capability.WriteKnowledge, capability.WriteProposal}
	return principal
}

func interviewPrincipalPointer(principal authdomain.Principal) *authdomain.Principal {
	return &principal
}

func serveInterview(t *testing.T, handler http.Handler, method, path, body string, setup func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer interview-token")
	request.Header.Set("Content-Type", "application/json")
	if method != http.MethodGet {
		request.Header.Set("Idempotency-Key", "interview-http-test")
	}
	if setup != nil {
		setup(request)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeInterviewJSON(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}

func requireInterviewProblem(t *testing.T, response *httptest.ResponseRecorder, status int, code string, retryable bool) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status=%d want=%d body=%s", response.Code, status, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control=%q", response.Header().Get("Cache-Control"))
	}
	var problem httpapi.Problem
	decodeInterviewJSON(t, response, &problem)
	if problem.ErrorCode != code || problem.Retryable != retryable {
		t.Fatalf("problem=%+v want code=%s retryable=%t", problem, code, retryable)
	}
}

type interviewHTTPAuthService struct{ principal authdomain.Principal }

func (service interviewHTTPAuthService) ExchangeBootstrap(context.Context, string) (authapp.SessionCredential, error) {
	return authapp.SessionCredential{}, nil
}

func (service interviewHTTPAuthService) AuthenticateSession(context.Context, string, string, bool) (authdomain.Principal, error) {
	return service.principal, nil
}

func (service interviewHTTPAuthService) CurrentSession(context.Context, string, string, bool) (authdomain.SessionInfo, error) {
	return authdomain.SessionInfo{}, nil
}

func (service interviewHTTPAuthService) RotateSession(context.Context, authdomain.Principal, string, string, bool) (authapp.SessionCredential, error) {
	return authapp.SessionCredential{}, nil
}

func (service interviewHTTPAuthService) AuthenticateAPIToken(context.Context, string) (authdomain.Principal, error) {
	return service.principal, nil
}

func (service interviewHTTPAuthService) CreateAPIToken(context.Context, authdomain.Principal, string, []capability.Capability, time.Duration) (authapp.APITokenCredential, error) {
	return authapp.APITokenCredential{}, nil
}

func (service interviewHTTPAuthService) ListAPITokens(context.Context, authdomain.Principal, authdomain.APITokenListQuery) (authdomain.APITokenListPage, error) {
	return authdomain.APITokenListPage{}, nil
}

func (service interviewHTTPAuthService) RevokeSession(context.Context, authdomain.Principal, foundation.ID) error {
	return nil
}

func (service interviewHTTPAuthService) RevokeAPIToken(context.Context, authdomain.Principal, foundation.ID) error {
	return nil
}

var _ Service = (*fakeInterviewService)(nil)
