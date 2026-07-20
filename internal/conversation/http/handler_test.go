package conversationhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/go-chi/chi/v5"
)

const (
	testWorkspaceID    = foundation.ID("11111111-1111-4111-8111-111111111111")
	testConversationID = foundation.ID("22222222-2222-4222-8222-222222222222")
	testAnswerID       = foundation.ID("33333333-3333-4333-8333-333333333333")
)

func TestSubmitQuestionUsesNestedScopeAndDefaults(t *testing.T) {
	t.Parallel()
	service := &fakeService{}
	router := testRouter(service)
	body := `{"workspace_id":"` + string(testWorkspaceID) + `","question":"如何验证？","scope":{"allow_web":true}}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/conversations/"+string(testConversationID)+"/questions", strings.NewReader(body))
	request.Header.Set("Idempotency-Key", "question-1")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if service.question.Request.Scope.RetrievalMode != "" || !service.question.Request.Scope.AllowWeb {
		t.Fatalf("request = %#v", service.question.Request)
	}
	if !strings.Contains(recorder.Body.String(), `"status_url":"/api/v1/answers/`) {
		t.Fatalf("missing answer status url: %s", recorder.Body.String())
	}
}

func TestCommandsRequireIdempotencyAndStrictJSON(t *testing.T) {
	t.Parallel()
	router := testRouter(&fakeService{})
	tests := []struct{ name, body, key, code string }{
		{name: "missing key", body: `{"workspace_id":"` + string(testWorkspaceID) + `"}`, code: "IDEMPOTENCY_KEY_REQUIRED"},
		{name: "unknown field", body: `{"workspace_id":"` + string(testWorkspaceID) + `","unknown":true}`, key: "create-1", code: "INVALID_JSON"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/v1/conversations", strings.NewReader(test.body))
			request.Header.Set("Idempotency-Key", test.key)
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), test.code) {
				t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestGetConversationETagAndNotModified(t *testing.T) {
	t.Parallel()
	service := &fakeService{conversation: validConversation()}
	router := testRouter(service)
	path := "/api/v1/conversations/" + string(testConversationID) + "?workspace_id=" + string(testWorkspaceID)
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("If-None-Match", `W/"3"`)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotModified || recorder.Header().Get("ETag") != `W/"3"` || recorder.Body.Len() != 0 {
		t.Fatalf("response = %d %#v %s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestAnswerETagIncludesWorkflowVersion(t *testing.T) {
	t.Parallel()
	service := &fakeService{answer: validAnswerView()}
	router := testRouter(service)
	path := "/api/v1/answers/" + string(testAnswerID) + "?workspace_id=" + string(testWorkspaceID)
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("If-None-Match", `W/"answer-1-workflow-6-stage-none"`)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `W/"answer-1-workflow-7-stage-none"` {
		t.Fatalf("response = %d %#v %s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestAnswerReturnsCurrentStageAndUsesItInETag(t *testing.T) {
	t.Parallel()
	view := validAnswerView()
	stage := application.RAGCurrentStageValidationCompleted
	view.CurrentStage = &stage
	router := testRouter(&fakeService{answer: view})
	path := "/api/v1/answers/" + string(testAnswerID) + "?workspace_id=" + string(testWorkspaceID)
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("If-None-Match", `W/"answer-1-workflow-7-stage-none"`)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `W/"answer-1-workflow-7-stage-validation.completed"` ||
		!strings.Contains(recorder.Body.String(), `"current_stage":"validation.completed"`) {
		t.Fatalf("response = %d %#v %s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestListTurnsLatestBuildsBoundedRecoveryQuery(t *testing.T) {
	t.Parallel()
	service := &fakeService{}
	router := testRouter(service)
	path := "/api/v1/conversations/" + string(testConversationID) + "/turns?workspace_id=" + string(testWorkspaceID) + "&latest=true"
	request := httptest.NewRequest(http.MethodGet, path, nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !service.turnsQuery.Latest || service.turnsQuery.Limit != 1 || service.turnsQuery.Cursor != nil {
		t.Fatalf("response=%d query=%#v body=%s", recorder.Code, service.turnsQuery, recorder.Body.String())
	}
}

func TestNotFoundIsAntiEnumerationProblem(t *testing.T) {
	t.Parallel()
	service := &fakeService{err: foundation.NewError(foundation.ErrorNotFound, "CONVERSATION_NOT_FOUND", false, errors.New("secret database detail"))}
	router := testRouter(service)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/conversations/"+string(testConversationID)+"?workspace_id="+string(testWorkspaceID), nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound || strings.Contains(recorder.Body.String(), "secret database detail") || !strings.Contains(recorder.Body.String(), "CONVERSATION_NOT_FOUND") {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestFeedbackUsesFeedbackType(t *testing.T) {
	t.Parallel()
	service := &fakeService{}
	router := testRouter(service)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/answers/"+string(testAnswerID)+"/feedback", strings.NewReader(`{"workspace_id":"`+string(testWorkspaceID)+`","feedback_type":"helpful"}`))
	request.Header.Set("Idempotency-Key", "feedback-1")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated || service.feedback.Request.Type != conversationdomain.FeedbackHelpful {
		t.Fatalf("response=%d %s request=%#v", recorder.Code, recorder.Body.String(), service.feedback.Request)
	}
}

func TestCommandsRejectNonJSONMediaType(t *testing.T) {
	t.Parallel()
	router := testRouter(&fakeService{})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/conversations", strings.NewReader(`{"workspace_id":"`+string(testWorkspaceID)+`"}`))
	request.Header.Set("Idempotency-Key", "create-media-type")
	request.Header.Set("Content-Type", "text/plain")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnsupportedMediaType || !strings.Contains(recorder.Body.String(), "UNSUPPORTED_MEDIA_TYPE") {
		t.Fatalf("response=%d %s", recorder.Code, recorder.Body.String())
	}
}

func TestCommandsAcceptCaseInsensitiveJSONMediaType(t *testing.T) {
	t.Parallel()
	router := testRouter(&fakeService{})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/conversations", strings.NewReader(`{"workspace_id":"`+string(testWorkspaceID)+`"}`))
	request.Header.Set("Idempotency-Key", "create-media-type-case")
	request.Header.Set("Content-Type", "Application/JSON; Charset=UTF-8")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("response=%d %s", recorder.Code, recorder.Body.String())
	}
}

func testRouter(service Service) http.Handler {
	router := chi.NewRouter()
	router.Route("/api/v1", NewHandler(service, NewCursorCodec()).Routes)
	return router
}

type fakeService struct {
	question     application.SubmitQuestionCommand
	feedback     application.SubmitFeedbackCommand
	conversation conversationdomain.Conversation
	answer       application.AnswerView
	turnsQuery   application.ListTurnsQuery
	err          error
}

func (service *fakeService) CreateConversation(context.Context, application.CreateConversationCommand) (application.CreateConversationResult, error) {
	value := validConversation()
	return application.CreateConversationResult{Conversation: value}, service.err
}
func (service *fakeService) SubmitQuestion(_ context.Context, command application.SubmitQuestionCommand) (application.SubmitQuestionResult, error) {
	service.question = command
	now := time.Now().UTC()
	return application.SubmitQuestionResult{Question: conversationdomain.Question{ID: foundation.ID("44444444-4444-4444-8444-444444444444"), Request: command.Request, Ordinal: 1, CreatedAt: now}, Answer: conversationdomain.Answer{ID: testAnswerID, WorkspaceID: testWorkspaceID, ConversationID: testConversationID, QuestionID: foundation.ID("44444444-4444-4444-8444-444444444444"), WorkflowRunID: foundation.ID("55555555-5555-4555-8555-555555555555"), PublicationStatus: conversationdomain.AnswerPublicationPending, Version: 1, CreatedAt: now, UpdatedAt: now}, Workflow: application.WorkflowRunView{RunID: foundation.ID("55555555-5555-4555-8555-555555555555"), Status: "pending", Version: 1, UpdatedAt: now}}, service.err
}
func (service *fakeService) SubmitFeedback(_ context.Context, command application.SubmitFeedbackCommand) (application.SubmitFeedbackResult, error) {
	service.feedback = command
	return application.SubmitFeedbackResult{Feedback: conversationdomain.AnswerFeedback{ID: foundation.ID("66666666-6666-4666-8666-666666666666"), Request: command.Request, CreatedAt: time.Now().UTC()}}, service.err
}
func (service *fakeService) ListConversations(context.Context, application.ListConversationsQuery) (application.ConversationPage, error) {
	return application.ConversationPage{}, service.err
}
func (service *fakeService) GetConversation(context.Context, foundation.ID, foundation.ID) (conversationdomain.Conversation, error) {
	if service.conversation.ID == "" {
		service.conversation = validConversation()
	}
	return service.conversation, service.err
}
func (service *fakeService) ListTurns(_ context.Context, query application.ListTurnsQuery) (application.TurnPage, error) {
	service.turnsQuery = query
	return application.TurnPage{}, service.err
}
func (service *fakeService) GetAnswer(context.Context, foundation.ID, foundation.ID) (application.AnswerView, error) {
	return service.answer, service.err
}

func validAnswerView() application.AnswerView {
	now := time.Date(2026, 7, 20, 1, 2, 3, 0, time.UTC)
	return application.AnswerView{
		Answer:   conversationdomain.Answer{ID: testAnswerID, WorkspaceID: testWorkspaceID, ConversationID: testConversationID, QuestionID: foundation.ID("44444444-4444-4444-8444-444444444444"), WorkflowRunID: foundation.ID("55555555-5555-4555-8555-555555555555"), PublicationStatus: conversationdomain.AnswerPublicationPending, Version: 1, CreatedAt: now, UpdatedAt: now},
		Workflow: application.WorkflowRunView{RunID: foundation.ID("55555555-5555-4555-8555-555555555555"), Status: "running", Version: 7, UpdatedAt: now},
	}
}

func validConversation() conversationdomain.Conversation {
	now := time.Date(2026, 7, 20, 1, 2, 3, 0, time.UTC)
	return conversationdomain.Conversation{ID: testConversationID, WorkspaceID: testWorkspaceID, Status: conversationdomain.ConversationStatusOpen, Version: 3, LastActivityAt: now, CreatedAt: now, UpdatedAt: now}
}

func decodeBody(t *testing.T, recorder *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(recorder.Body.Bytes(), target); err != nil {
		t.Fatal(err)
	}
}
