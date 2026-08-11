package learningpathhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	authapp "github.com/CodeZen-Lizhi/zhixu/internal/auth/application"
	authdomain "github.com/CodeZen-Lizhi/zhixu/internal/auth/domain"
	authhttp "github.com/CodeZen-Lizhi/zhixu/internal/auth/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	pathapp "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/application"
	pathdomain "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/domain"
	"github.com/gin-gonic/gin"
)

func TestRoutesRegisterCanonicalGinPaths(t *testing.T) {
	engine := gin.New()
	NewHandler(nil, time.Second).Routes(engine)

	want := map[string]struct{}{
		http.MethodPost + " /review/answers/:answer_id/learning-path":               {},
		http.MethodGet + " /review/answers/:answer_id/learning-path":                {},
		http.MethodPut + " /review/answers/:answer_id/learning-path/status":         {},
		http.MethodPut + " /review/answers/:answer_id/learning-path/steps/:step_id": {},
	}
	got := make(map[string]struct{}, len(engine.Routes()))
	for _, route := range engine.Routes() {
		got[route.Method+" "+route.Path] = struct{}{}
	}
	if len(got) != len(want) {
		t.Fatalf("route count=%d want=%d routes=%v", len(got), len(want), engine.Routes())
	}
	for route := range want {
		if _, ok := got[route]; !ok {
			t.Fatalf("missing route %s; routes=%v", route, engine.Routes())
		}
	}
}

func TestRoutesPassCanonicalPathValuesToService(t *testing.T) {
	fixture := newLearningPathHTTPFixture()
	service := &fakeLearningPathService{
		createResult: pathapp.Result{Path: fixture.path},
		getResult:    pathapp.Result{Path: fixture.path},
		statusResult: pathapp.Result{Path: fixture.path},
		stepResult:   pathapp.StepResult{Path: fixture.path, Step: fixture.step},
	}
	router := protectedLearningPathRouter(t, service)

	tests := []struct {
		name   string
		method string
		path   string
		body   string
		status int
	}{
		{
			name:   "create",
			method: http.MethodPost,
			path:   "/api/v1/review/answers/" + string(fixture.answerID) + "/learning-path",
			body:   `{"workspace_id":"` + string(fixture.workspaceID) + `"}`,
			status: http.StatusCreated,
		},
		{
			name:   "get",
			method: http.MethodGet,
			path:   "/api/v1/review/answers/" + string(fixture.answerID) + "/learning-path?workspace_id=" + string(fixture.workspaceID),
			status: http.StatusOK,
		},
		{
			name:   "update status",
			method: http.MethodPut,
			path:   "/api/v1/review/answers/" + string(fixture.answerID) + "/learning-path/status",
			body:   `{"workspace_id":"` + string(fixture.workspaceID) + `","expected_version":3,"status":"PAUSED"}`,
			status: http.StatusOK,
		},
		{
			name:   "update step",
			method: http.MethodPut,
			path:   "/api/v1/review/answers/" + string(fixture.answerID) + "/learning-path/steps/" + string(fixture.stepID),
			body:   `{"workspace_id":"` + string(fixture.workspaceID) + `","expected_version":4,"status":"IN_PROGRESS"}`,
			status: http.StatusOK,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := serveLearningPath(t, router, test.method, test.path, test.body)
			if response.Code != test.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.status, response.Body.String())
			}
		})
	}

	if service.create.WorkspaceID != fixture.workspaceID || service.create.ReviewAnswerID != fixture.answerID {
		t.Fatalf("create command=%+v", service.create)
	}
	if service.getWorkspaceID != fixture.workspaceID || service.getAnswerID != fixture.answerID || service.getCalls != 3 {
		t.Fatalf("get calls=%d workspace=%s answer=%s", service.getCalls, service.getWorkspaceID, service.getAnswerID)
	}
	if service.status.WorkspaceID != fixture.workspaceID || service.status.PathID != fixture.pathID || service.status.ExpectedVersion != 3 || service.status.Status != pathdomain.StatusPaused {
		t.Fatalf("status command=%+v", service.status)
	}
	if service.step.WorkspaceID != fixture.workspaceID || service.step.PathID != fixture.pathID || service.step.StepID != fixture.stepID || service.step.ExpectedVersion != 4 || service.step.Status != pathdomain.StepStatusInProgress {
		t.Fatalf("step command=%+v", service.step)
	}
}

func TestRoutesFailClosedWhenServiceIsUnavailable(t *testing.T) {
	fixture := newLearningPathHTTPFixture()
	response := serveLearningPath(t, protectedLearningPathRouter(t, nil), http.MethodGet,
		"/api/v1/review/answers/"+string(fixture.answerID)+"/learning-path?workspace_id="+string(fixture.workspaceID), "")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want=%d body=%s", response.Code, http.StatusServiceUnavailable, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control=%q", response.Header().Get("Cache-Control"))
	}
	var problem httpapi.Problem
	if err := json.NewDecoder(response.Body).Decode(&problem); err != nil {
		t.Fatal(err)
	}
	if problem.ErrorCode != "LEARNING_PATH_HTTP_UNAVAILABLE" || !problem.Retryable {
		t.Fatalf("problem=%+v", problem)
	}
}

type learningPathHTTPFixture struct {
	workspaceID foundation.ID
	answerID    foundation.ID
	pathID      foundation.ID
	stepID      foundation.ID
	path        pathdomain.Path
	step        pathdomain.Step
}

func newLearningPathHTTPFixture() learningPathHTTPFixture {
	workspaceID := foundation.ID("10000000-0000-4000-8000-000000000001")
	answerID := foundation.ID("10000000-0000-4000-8000-000000000002")
	pathID := foundation.ID("10000000-0000-4000-8000-000000000003")
	stepID := foundation.ID("10000000-0000-4000-8000-000000000004")
	return learningPathHTTPFixture{
		workspaceID: workspaceID,
		answerID:    answerID,
		pathID:      pathID,
		stepID:      stepID,
		path: pathdomain.Path{
			ID:             pathID,
			WorkspaceID:    workspaceID,
			ReviewAnswerID: &answerID,
		},
		step: pathdomain.Step{ID: stepID, WorkspaceID: workspaceID, PathID: pathID},
	}
}

type fakeLearningPathService struct {
	createResult   pathapp.Result
	getResult      pathapp.Result
	statusResult   pathapp.Result
	stepResult     pathapp.StepResult
	create         pathapp.CreateReviewCommand
	getWorkspaceID foundation.ID
	getAnswerID    foundation.ID
	getCalls       int
	status         pathapp.UpdatePathStatusCommand
	step           pathapp.UpdateStepCommand
}

func (service *fakeLearningPathService) CreateForReview(_ context.Context, command pathapp.CreateReviewCommand) (pathapp.Result, error) {
	service.create = command
	return service.createResult, nil
}

func (service *fakeLearningPathService) GetForReviewAnswer(_ context.Context, workspaceID, answerID foundation.ID) (pathapp.Result, error) {
	service.getWorkspaceID = workspaceID
	service.getAnswerID = answerID
	service.getCalls++
	return service.getResult, nil
}

func (service *fakeLearningPathService) UpdateStatus(_ context.Context, command pathapp.UpdatePathStatusCommand) (pathapp.Result, error) {
	service.status = command
	return service.statusResult, nil
}

func (service *fakeLearningPathService) UpdateStep(_ context.Context, command pathapp.UpdateStepCommand) (pathapp.StepResult, error) {
	service.step = command
	return service.stepResult, nil
}

func protectedLearningPathRouter(t *testing.T, service Service) http.Handler {
	t.Helper()
	authHandler, err := authhttp.NewHandler(learningPathAuthService{principal: learningPathPrincipal()}, authhttp.Options{
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

func learningPathPrincipal() authdomain.Principal {
	return authdomain.Principal{
		Kind:   authdomain.PrincipalAPIToken,
		ID:     foundation.ID("10000000-0000-4000-8000-000000000099"),
		Scopes: []capability.Capability{capability.ReadLocal, capability.WriteProposal},
	}
}

func serveLearningPath(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer learning-path-test")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		request.Header.Set("Idempotency-Key", "learning-path-http-test")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

type learningPathAuthService struct{ principal authdomain.Principal }

func (service learningPathAuthService) ExchangeBootstrap(context.Context, string) (authapp.SessionCredential, error) {
	return authapp.SessionCredential{}, nil
}

func (service learningPathAuthService) AuthenticateSession(context.Context, string, string, bool) (authdomain.Principal, error) {
	return service.principal, nil
}

func (service learningPathAuthService) CurrentSession(context.Context, string, string, bool) (authdomain.SessionInfo, error) {
	return authdomain.SessionInfo{}, nil
}

func (service learningPathAuthService) RotateSession(context.Context, authdomain.Principal, string, string, bool) (authapp.SessionCredential, error) {
	return authapp.SessionCredential{}, nil
}

func (service learningPathAuthService) AuthenticateAPIToken(context.Context, string) (authdomain.Principal, error) {
	return service.principal, nil
}

func (service learningPathAuthService) CreateAPIToken(context.Context, authdomain.Principal, string, []capability.Capability, time.Duration) (authapp.APITokenCredential, error) {
	return authapp.APITokenCredential{}, nil
}

func (service learningPathAuthService) ListAPITokens(context.Context, authdomain.Principal, authdomain.APITokenListQuery) (authdomain.APITokenListPage, error) {
	return authdomain.APITokenListPage{}, nil
}

func (service learningPathAuthService) RevokeSession(context.Context, authdomain.Principal, foundation.ID) error {
	return nil
}

func (service learningPathAuthService) RevokeAPIToken(context.Context, authdomain.Principal, foundation.ID) error {
	return nil
}

var _ Service = (*fakeLearningPathService)(nil)
var _ authhttp.Service = learningPathAuthService{}
