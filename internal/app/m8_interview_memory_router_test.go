package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	memoryapp "github.com/CodeZen-Lizhi/zhixu/internal/memory/application"
	memorydomain "github.com/CodeZen-Lizhi/zhixu/internal/memory/domain"
	memoryhttp "github.com/CodeZen-Lizhi/zhixu/internal/memory/http"
	reviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/application"
	reviewdomain "github.com/CodeZen-Lizhi/zhixu/internal/review/domain"
	reviewhttp "github.com/CodeZen-Lizhi/zhixu/internal/review/http"
	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	interviewdomain "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
	interviewhttp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/http"
	pathapp "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/application"
	pathhttp "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/http"
)

func TestRouterOmitsM8RoutesWhenHandlersAreNil(t *testing.T) {
	const workspaceID = "92000000-0000-4000-8000-000000000001"
	const sessionID = "92000000-0000-4000-8000-000000000002"
	router := NewRouter(Dependencies{Version: "m8-nil-routes-test"})
	for _, path := range []string{
		"/api/v1/memories?workspace_id=" + workspaceID,
		"/api/v1/review/interviews/" + sessionID + "?workspace_id=" + workspaceID,
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "NOT_FOUND") {
			t.Fatalf("route=%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestRouterRegistersM8MemoryAndInterviewRoutesFailClosed(t *testing.T) {
	const workspaceID = "92000000-0000-4000-8000-000000000001"
	const sessionID = "92000000-0000-4000-8000-000000000002"
	router := NewRouter(Dependencies{
		Version:      "m8-routes-test",
		Auth:         readyAuthHandler(t),
		AuthRequired: true,
		Memory:       memoryhttp.NewHandler(nil, time.Second),
		Interview:    interviewhttp.NewHandler(nil, time.Second),
	})
	for _, test := range []struct {
		name string
		path string
		code string
	}{
		{name: "memory", path: "/api/v1/memories?workspace_id=" + workspaceID, code: "MEMORY_HTTP_UNAVAILABLE"},
		{name: "interview", path: "/api/v1/review/interviews/" + sessionID + "?workspace_id=" + workspaceID, code: "INTERVIEW_HTTP_UNAVAILABLE"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			request.Header.Set("Authorization", "Bearer test-token")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), test.code) {
				t.Fatalf("route=%s status=%d body=%s", test.path, response.Code, response.Body.String())
			}
		})
	}
}

func TestRouterAuthProtectsM8MemoryAndInterviewRoutes(t *testing.T) {
	const workspaceID = "92000000-0000-4000-8000-000000000001"
	const sessionID = "92000000-0000-4000-8000-000000000002"
	router := NewRouter(Dependencies{
		Version:      "m8-auth-test",
		Auth:         readyAuthHandler(t),
		AuthRequired: true,
		Memory:       memoryhttp.NewHandler(nil, time.Second),
		Interview:    interviewhttp.NewHandler(nil, time.Second),
	})
	for _, path := range []string{
		"/api/v1/memories?workspace_id=" + workspaceID,
		"/api/v1/review/interviews/" + sessionID + "?workspace_id=" + workspaceID,
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "AUTH_UNAUTHORIZED") {
			t.Fatalf("route=%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestRouterInvokesAvailableM8Handlers(t *testing.T) {
	const workspaceID = "92000000-0000-4000-8000-000000000001"
	const sessionID = "92000000-0000-4000-8000-000000000002"
	memoryService := &m8MemoryRouterService{}
	interviewService := &m8InterviewRouterService{}
	router := NewRouter(Dependencies{
		Version:      "m8-available-routes-test",
		Auth:         readyAuthHandler(t),
		AuthRequired: true,
		Memory:       memoryhttp.NewHandler(memoryService, time.Second),
		Interview:    interviewhttp.NewHandler(interviewService, time.Second),
	})
	for _, test := range []struct {
		path       string
		wantStatus int
		wantCode   string
	}{
		{path: "/api/v1/memories?workspace_id=" + workspaceID, wantStatus: http.StatusOK},
		{path: "/api/v1/review/interviews/" + sessionID + "?workspace_id=" + workspaceID, wantStatus: http.StatusNotFound, wantCode: interviewdomain.ErrorCodeSessionNotFound},
	} {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		request.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != test.wantStatus || test.wantCode != "" && !strings.Contains(response.Body.String(), test.wantCode) {
			t.Fatalf("route=%s status=%d body=%s", test.path, response.Code, response.Body.String())
		}
	}
	if !memoryService.listed || !interviewService.loaded {
		t.Fatalf("memory listed=%t interview loaded=%t", memoryService.listed, interviewService.loaded)
	}
}

func TestRouterSystemStatusProjectsM8CapabilitiesStrictly(t *testing.T) {
	readyDependencies := func() Dependencies {
		return Dependencies{
			Version:      "m8-capabilities-test",
			Database:     fakePinger{},
			Review:       reviewhttp.NewHandler(m8ReviewRouterService{}, time.Second),
			LearningPath: pathhttp.NewHandler(m8LearningPathRouterService{}, time.Second),
			Memory:       memoryhttp.NewHandler(&m8MemoryRouterService{}, time.Second),
			Interview:    interviewhttp.NewHandler(&m8InterviewRouterService{}, time.Second),
		}
	}
	tests := []struct {
		name       string
		configure  func(*Dependencies)
		capability string
		status     string
	}{
		{name: "review available", configure: func(*Dependencies) {}, capability: "review", status: "ready"},
		{name: "memory available", configure: func(*Dependencies) {}, capability: "memory", status: "ready"},
		{name: "interview available", configure: func(*Dependencies) {}, capability: "interview", status: "ready"},
		{name: "review nil", configure: func(deps *Dependencies) { deps.Review = nil }, capability: "review", status: "unavailable"},
		{name: "review unavailable", configure: func(deps *Dependencies) { deps.Review = reviewhttp.NewHandler(nil, time.Second) }, capability: "review", status: "unavailable"},
		{name: "learning path nil", configure: func(deps *Dependencies) { deps.LearningPath = nil }, capability: "review", status: "unavailable"},
		{name: "learning path unavailable", configure: func(deps *Dependencies) { deps.LearningPath = pathhttp.NewHandler(nil, time.Second) }, capability: "review", status: "unavailable"},
		{name: "memory nil", configure: func(deps *Dependencies) { deps.Memory = nil }, capability: "memory", status: "unavailable"},
		{name: "memory unavailable", configure: func(deps *Dependencies) { deps.Memory = memoryhttp.NewHandler(nil, time.Second) }, capability: "memory", status: "unavailable"},
		{name: "interview nil", configure: func(deps *Dependencies) { deps.Interview = nil }, capability: "interview", status: "unavailable"},
		{name: "interview unavailable", configure: func(deps *Dependencies) { deps.Interview = interviewhttp.NewHandler(nil, time.Second) }, capability: "interview", status: "unavailable"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps := readyDependencies()
			test.configure(&deps)
			response := httptest.NewRecorder()
			NewRouter(deps).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/system/status", nil))
			if response.Code != http.StatusOK {
				t.Fatalf("system status=%d body=%s", response.Code, response.Body.String())
			}
			fragment := `"` + test.capability + `":{"status":"` + test.status + `"}`
			if !strings.Contains(response.Body.String(), fragment) {
				t.Fatalf("system status missing %s: %s", fragment, response.Body.String())
			}
		})
	}
}

type m8MemoryRouterService struct{ listed bool }

func (*m8MemoryRouterService) CreateCandidate(context.Context, memoryapp.CreateCandidateCommand) (memoryapp.CommandResult, error) {
	return memoryapp.CommandResult{}, nil
}

func (*m8MemoryRouterService) Confirm(context.Context, memoryapp.TransitionCommand) (memoryapp.CommandResult, error) {
	return memoryapp.CommandResult{}, nil
}

func (*m8MemoryRouterService) Edit(context.Context, memoryapp.EditCommand) (memoryapp.CommandResult, error) {
	return memoryapp.CommandResult{}, nil
}

func (*m8MemoryRouterService) Pause(context.Context, memoryapp.TransitionCommand) (memoryapp.CommandResult, error) {
	return memoryapp.CommandResult{}, nil
}

func (*m8MemoryRouterService) Resume(context.Context, memoryapp.TransitionCommand) (memoryapp.CommandResult, error) {
	return memoryapp.CommandResult{}, nil
}

func (*m8MemoryRouterService) Delete(context.Context, memoryapp.TransitionCommand) (memoryapp.CommandResult, error) {
	return memoryapp.CommandResult{}, nil
}

func (*m8MemoryRouterService) Get(context.Context, memoryapp.Scope, foundation.ID) (memorydomain.Memory, error) {
	return memorydomain.Memory{}, nil
}

func (service *m8MemoryRouterService) List(_ context.Context, _ memoryapp.ListQuery) (memoryapp.ListPage, error) {
	service.listed = true
	return memoryapp.ListPage{Items: []memorydomain.Memory{}}, nil
}

type m8InterviewRouterService struct{ loaded bool }

func (*m8InterviewRouterService) Start(context.Context, interviewapp.StartCommand) (interviewapp.StartResult, error) {
	return interviewapp.StartResult{}, nil
}

func (service *m8InterviewRouterService) Get(_ context.Context, _ foundation.ID, _ foundation.ID) (interviewapp.Snapshot, error) {
	service.loaded = true
	return interviewapp.Snapshot{}, interviewdomain.NotFoundError(interviewdomain.ErrorCodeSessionNotFound, "interview session was not found")
}

func (*m8InterviewRouterService) List(context.Context, interviewapp.SessionListQuery) (interviewapp.SessionListPage, error) {
	return interviewapp.SessionListPage{}, nil
}

type m8ReviewRouterService struct{}

type m8LearningPathRouterService struct{}

func (m8LearningPathRouterService) CreateForReview(context.Context, pathapp.CreateReviewCommand) (pathapp.Result, error) {
	return pathapp.Result{}, nil
}

func (m8LearningPathRouterService) GetForReviewAnswer(context.Context, foundation.ID, foundation.ID) (pathapp.Result, error) {
	return pathapp.Result{}, nil
}

func (m8LearningPathRouterService) UpdateStatus(context.Context, pathapp.UpdatePathStatusCommand) (pathapp.Result, error) {
	return pathapp.Result{}, nil
}

func (m8LearningPathRouterService) UpdateStep(context.Context, pathapp.UpdateStepCommand) (pathapp.StepResult, error) {
	return pathapp.StepResult{}, nil
}

func (m8ReviewRouterService) CreateDeck(context.Context, reviewapp.CreateDeckCommand) (reviewapp.CommandResult[reviewdomain.Deck], error) {
	return reviewapp.CommandResult[reviewdomain.Deck]{}, nil
}

func (m8ReviewRouterService) GetDeck(context.Context, foundation.ID, foundation.ID) (reviewdomain.Deck, error) {
	return reviewdomain.Deck{}, nil
}

func (m8ReviewRouterService) ListDecks(context.Context, foundation.ID, int) (reviewapp.DeckPage, error) {
	return reviewapp.DeckPage{}, nil
}

func (m8ReviewRouterService) ListCards(context.Context, foundation.ID, foundation.ID, int) (reviewapp.CardPage, error) {
	return reviewapp.CardPage{}, nil
}

func (m8ReviewRouterService) CreateCard(context.Context, reviewapp.CreateCardCommand) (reviewapp.CommandResult[reviewdomain.Card], error) {
	return reviewapp.CommandResult[reviewdomain.Card]{}, nil
}

func (m8ReviewRouterService) EditCard(context.Context, reviewapp.EditCardCommand) (reviewapp.CommandResult[reviewdomain.Card], error) {
	return reviewapp.CommandResult[reviewdomain.Card]{}, nil
}

func (m8ReviewRouterService) ApproveCard(context.Context, reviewapp.CardDecisionCommand) (reviewapp.CommandResult[reviewdomain.Card], error) {
	return reviewapp.CommandResult[reviewdomain.Card]{}, nil
}

func (m8ReviewRouterService) RejectCard(context.Context, reviewapp.CardDecisionCommand) (reviewapp.CommandResult[reviewdomain.Card], error) {
	return reviewapp.CommandResult[reviewdomain.Card]{}, nil
}

func (m8ReviewRouterService) InvalidateCard(context.Context, reviewapp.CardDecisionCommand) (reviewapp.CommandResult[reviewdomain.Card], error) {
	return reviewapp.CommandResult[reviewdomain.Card]{}, nil
}

func (m8ReviewRouterService) InvalidateCards(context.Context, reviewapp.InvalidateCardsCommand) (reviewapp.InvalidationResult, error) {
	return reviewapp.InvalidationResult{}, nil
}

func (m8ReviewRouterService) ListDue(context.Context, foundation.ID, foundation.ID, *foundation.ID, int) ([]reviewapp.DueCard, error) {
	return nil, nil
}

func (m8ReviewRouterService) StartSession(context.Context, reviewapp.StartSessionCommand) (reviewapp.CommandResult[reviewdomain.Session], error) {
	return reviewapp.CommandResult[reviewdomain.Session]{}, nil
}

func (m8ReviewRouterService) CompleteSession(context.Context, reviewapp.CompleteSessionCommand) (reviewapp.CommandResult[reviewdomain.Session], error) {
	return reviewapp.CommandResult[reviewdomain.Session]{}, nil
}

func (m8ReviewRouterService) SubmitAnswer(context.Context, reviewapp.SubmitAnswerCommand) (reviewapp.AnswerResult, error) {
	return reviewapp.AnswerResult{}, nil
}

func (m8ReviewRouterService) ChangeDeckSchedule(context.Context, reviewapp.DeckScheduleCommand, reviewapp.DeckScheduleAction) (reviewapp.CommandResult[reviewdomain.Deck], error) {
	return reviewapp.CommandResult[reviewdomain.Deck]{}, nil
}

func (*m8InterviewRouterService) SubmitTurn(context.Context, interviewapp.SubmitTurnCommand) (interviewapp.SubmitTurnResult, error) {
	return interviewapp.SubmitTurnResult{}, nil
}

func (*m8InterviewRouterService) Complete(context.Context, interviewapp.CompleteCommand) (interviewapp.CompleteResult, error) {
	return interviewapp.CompleteResult{}, nil
}

func (*m8InterviewRouterService) UpdatePathStep(context.Context, interviewapp.UpdatePathStepCommand) (interviewapp.PathStepResult, error) {
	return interviewapp.PathStepResult{}, nil
}

func (*m8InterviewRouterService) UpdatePathStatus(context.Context, interviewapp.UpdatePathStatusCommand) (interviewapp.PathStatusResult, error) {
	return interviewapp.PathStatusResult{}, nil
}

func (*m8InterviewRouterService) SuggestMemoryCandidate(context.Context, interviewapp.SuggestMemoryCandidateCommand) (interviewapp.MemoryCandidateResult, error) {
	return interviewapp.MemoryCandidateResult{}, nil
}
