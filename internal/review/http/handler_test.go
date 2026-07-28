package reviewhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	reviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/domain"
	"github.com/go-chi/chi/v5"
)

const (
	testWorkspaceID = "10000000-0000-4000-8000-000000000001"
	testDeckID      = "10000000-0000-4000-8000-000000000002"
	testCardID      = "10000000-0000-4000-8000-000000000003"
	testClaimID     = "10000000-0000-4000-8000-000000000004"
	testSourceID    = "10000000-0000-4000-8000-000000000005"
	testSpanID      = "10000000-0000-4000-8000-000000000006"
	testSessionID   = "10000000-0000-4000-8000-000000000007"
)

func TestRoutesBindWorkspaceAndInvokeReviewCommands(t *testing.T) {
	fake := newFakeService()
	router := chi.NewRouter()
	NewHandler(fake, time.Second).Routes(router)

	tests := []struct {
		name   string
		method string
		path   string
		body   string
		status int
	}{
		{"list decks", http.MethodGet, "/review/decks?workspace_id=" + testWorkspaceID + "&limit=1", "", http.StatusOK},
		{"get deck", http.MethodGet, "/review/decks/" + testDeckID + "?workspace_id=" + testWorkspaceID, "", http.StatusOK},
		{"list cards", http.MethodGet, "/review/decks/" + testDeckID + "/cards?workspace_id=" + testWorkspaceID + "&limit=1", "", http.StatusOK},
		{"create deck", http.MethodPost, "/review/decks", `{"workspace_id":"` + testWorkspaceID + `","name":"Go","scope":{},"daily_limit":10}`, http.StatusCreated},
		{"create card", http.MethodPost, "/review/decks/" + testDeckID + "/cards", createCardJSON(), http.StatusCreated},
		{"approve card", http.MethodPost, "/review/cards/" + testCardID + "/approve", decisionJSON(), http.StatusCreated},
		{"reject card", http.MethodPost, "/review/cards/" + testCardID + "/reject", decisionJSON(), http.StatusCreated},
		{"invalidate card", http.MethodPost, "/review/cards/" + testCardID + "/invalidate", decisionJSON(), http.StatusCreated},
		{"due cards", http.MethodGet, "/review/due?workspace_id=" + testWorkspaceID + "&session_id=" + testSessionID + "&deck_id=" + testDeckID + "&limit=1", "", http.StatusOK},
		{"start session", http.MethodPost, "/review/sessions", `{"workspace_id":"` + testWorkspaceID + `","deck_id":"` + testDeckID + `","session_type":"REVIEW","config":{}}`, http.StatusCreated},
		{"complete session", http.MethodPost, "/review/sessions/" + testSessionID + "/complete", `{"workspace_id":"` + testWorkspaceID + `","cancelled":false}`, http.StatusOK},
		{"pause schedule", http.MethodPost, "/review/decks/" + testDeckID + "/schedule/pause", scheduleJSON(), http.StatusOK},
		{"resume schedule", http.MethodPost, "/review/decks/" + testDeckID + "/schedule/resume", scheduleJSON(), http.StatusOK},
		{"reset schedule", http.MethodPost, "/review/decks/" + testDeckID + "/schedule/reset", scheduleJSON(), http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			if test.body != "" {
				request.Header.Set("Content-Type", "application/json")
			}
			if test.method == http.MethodPost {
				request.Header.Set("Idempotency-Key", "review-http-test")
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.status, response.Body.String())
			}
		})
	}
	if fake.createCard.WorkspaceID != testID(testWorkspaceID) || fake.createCard.DeckID != testID(testDeckID) || fake.decision.ExpectedVersion != 1 {
		t.Fatalf("commands lost workspace or expected version: %#v %#v", fake.createCard, fake.decision)
	}
	if fake.scheduleAction != reviewapp.DeckScheduleReset || fake.complete.WorkspaceID != testID(testWorkspaceID) || fake.complete.SessionID != testID(testSessionID) || fake.complete.IdempotencyKey != "review-http-test" {
		t.Fatalf("schedule/session command binding is invalid: %#v %#v", fake.scheduleAction, fake.complete)
	}
	if fake.startSession.SessionType != domain.SessionTypeReview || fake.startSession.DeckID == nil || *fake.startSession.DeckID != testID(testDeckID) {
		t.Fatalf("start session command escaped Review deck binding: %#v", fake.startSession)
	}
	if fake.dueWorkspaceID != testID(testWorkspaceID) || fake.dueSessionID != testID(testSessionID) || fake.dueDeckID == nil || *fake.dueDeckID != testID(testDeckID) || fake.dueLimit != 1 {
		t.Fatalf("due query binding is invalid: workspace=%s session=%s deck=%v limit=%d", fake.dueWorkspaceID, fake.dueSessionID, fake.dueDeckID, fake.dueLimit)
	}
}

func TestStrictJSONAndCASAreRejectedBeforeService(t *testing.T) {
	fake := newFakeService()
	router := chi.NewRouter()
	NewHandler(fake, time.Second).Routes(router)

	tests := []struct {
		name string
		path string
		body string
	}{
		{"unknown field", "/review/decks", `{"workspace_id":"` + testWorkspaceID + `","name":"Go","scope":{},"daily_limit":10,"extra":true}`},
		{"duplicate field", "/review/decks", `{"workspace_id":"` + testWorkspaceID + `","workspace_id":"` + testWorkspaceID + `","name":"Go","scope":{},"daily_limit":10}`},
		{"missing expected version", "/review/cards/" + testCardID + "/approve", `{"workspace_id":"` + testWorkspaceID + `","reason":"ok"}`},
		{"command query parameter", "/review/decks?workspace_id=" + testWorkspaceID, `{"workspace_id":"` + testWorkspaceID + `","name":"Go","scope":{},"daily_limit":10}`},
		{"invalid limit", "/review/decks?workspace_id=" + testWorkspaceID + "&limit=201", ""},
		{"invalid workspace UUID", "/review/decks?workspace_id=not-a-uuid", ""},
		{"client evidence quote", "/review/decks/" + testDeckID + "/cards", strings.Replace(createCardJSON(), `"}],"card_type"`, `","quote":"untrusted"}],"card_type"`, 1)},
		{"review session missing deck", "/review/sessions", `{"workspace_id":"` + testWorkspaceID + `","session_type":"REVIEW","config":{}}`},
		{"interview through review session endpoint", "/review/sessions", `{"workspace_id":"` + testWorkspaceID + `","deck_id":"` + testDeckID + `","session_type":"INTERVIEW","config":{}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			method := http.MethodPost
			if test.body == "" {
				method = http.MethodGet
			}
			request := httptest.NewRequest(method, test.path, strings.NewReader(test.body))
			if test.body != "" {
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Idempotency-Key", "review-http-test")
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", response.Code, response.Body.String())
			}
		})
	}
	if fake.createDeckCalls != 0 || fake.createCard.ClaimID != "" || fake.decisionCalls != 0 || fake.startSessionCalls != 0 {
		t.Fatalf("invalid input reached service: create_deck=%d create_card=%+v decision=%d start_session=%d", fake.createDeckCalls, fake.createCard, fake.decisionCalls, fake.startSessionCalls)
	}
}

func TestReviewSessionEndpointsRejectInterviewResponses(t *testing.T) {
	fake := newFakeService()
	fake.session.SessionType = domain.SessionTypeInterview
	fake.session.DeckID = nil
	router := chi.NewRouter()
	NewHandler(fake, time.Second).Routes(router)

	tests := []struct {
		name string
		path string
		body string
	}{
		{name: "start", path: "/review/sessions", body: `{"workspace_id":"` + testWorkspaceID + `","deck_id":"` + testDeckID + `","session_type":"REVIEW","config":{}}`},
		{name: "complete", path: "/review/sessions/" + testSessionID + "/complete", body: `{"workspace_id":"` + testWorkspaceID + `","cancelled":false}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "review-session-response-check")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusInternalServerError {
				t.Fatalf("status=%d want=500 body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestMissingServiceFailsClosedForCardDecision(t *testing.T) {
	router := chi.NewRouter()
	NewHandler(nil, time.Second).Routes(router)
	request := httptest.NewRequest(http.MethodPost, "/review/cards/"+testCardID+"/approve", strings.NewReader(decisionJSON()))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "review-http-test")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", response.Code, response.Body.String())
	}
}

func TestSubmitAnswerUsesServerScoreAndRejectsScoreIngress(t *testing.T) {
	fake := newFakeService()
	router := chi.NewRouter()
	NewHandler(fake, time.Second).Routes(router)
	body := `{"workspace_id":"` + testWorkspaceID + `","card_id":"` + testCardID + `","question_ref":"card:1","user_answer":"answer","rating":3}`
	request := httptest.NewRequest(http.MethodPost, "/review/sessions/"+testSessionID+"/answers", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "review-http-test")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", response.Code, response.Body.String())
	}
	if fake.submit.WorkspaceID != testID(testWorkspaceID) || fake.submit.SessionID != testID(testSessionID) || fake.submit.CardID != testID(testCardID) || fake.submit.QuestionRef != "card:1" || fake.submit.UserAnswer != "answer" || fake.submit.Rating != domain.RatingGood {
		t.Fatalf("submit command = %+v", fake.submit)
	}
	var result struct {
		Answer struct {
			Score struct {
				Evidence []struct {
					SourceVersionHref string `json:"source_version_href"`
					SourceSpanHref    string `json:"source_span_href"`
				} `json:"evidence"`
			} `json:"score"`
		} `json:"answer"`
		Replayed bool `json:"replayed"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Replayed || len(result.Answer.Score.Evidence) != 1 || strings.Contains(response.Body.String(), `"quote"`) ||
		result.Answer.Score.Evidence[0].SourceVersionHref != "/api/v1/workspaces/"+testWorkspaceID+"/source-versions/"+testSourceID ||
		result.Answer.Score.Evidence[0].SourceSpanHref != "/api/v1/workspaces/"+testWorkspaceID+"/source-versions/"+testSourceID+"/spans/"+testSpanID {
		t.Fatalf("answer response = %s", response.Body.String())
	}

	fake.answerResult.Replayed = true
	request = httptest.NewRequest(http.MethodPost, "/review/sessions/"+testSessionID+"/answers", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "review-http-test")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("replay status = %d, want 200: %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/review/sessions/"+testSessionID+"/answers", strings.NewReader(`{"workspace_id":"`+testWorkspaceID+`","card_id":"`+testCardID+`","question_ref":"card:1","user_answer":"answer","rating":3,"score":{}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "review-http-test")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("untrusted score status = %d, want 400: %s", response.Code, response.Body.String())
	}

	for name, invalidBody := range map[string]string{
		"missing required fields": `{}`,
		"invalid workspace":       `{"workspace_id":"invalid","card_id":"` + testCardID + `","question_ref":"card:1","user_answer":"answer","rating":3}`,
		"empty question ref":      `{"workspace_id":"` + testWorkspaceID + `","card_id":"` + testCardID + `","question_ref":"","user_answer":"answer","rating":3}`,
		"invalid rating":          `{"workspace_id":"` + testWorkspaceID + `","card_id":"` + testCardID + `","question_ref":"card:1","user_answer":"answer","rating":5}`,
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/review/sessions/"+testSessionID+"/answers", strings.NewReader(invalidBody))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "review-http-test")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestDueCardsHideAnswersAndEvidenceBeforeSubmission(t *testing.T) {
	fake := newFakeService()
	router := chi.NewRouter()
	NewHandler(fake, time.Second).Routes(router)
	request := httptest.NewRequest(http.MethodGet, "/review/due?workspace_id="+testWorkspaceID+"&session_id="+testSessionID+"&deck_id="+testDeckID+"&limit=1", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "answer_points") || strings.Contains(response.Body.String(), "evidence") {
		t.Fatalf("due response leaked protected content: %s", response.Body.String())
	}
	var payload struct {
		Items []struct {
			Card map[string]any `json:"card"`
		} `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 || payload.Items[0].Card["id"] != testCardID || payload.Items[0].Card["question"] != "Why?" || len(payload.Items[0].Card) != 8 {
		t.Fatalf("due card projection = %#v", payload.Items)
	}
}

func TestCompleteSessionMapsReplayAndFailureContracts(t *testing.T) {
	tests := []struct {
		name      string
		result    reviewapp.CommandResult[domain.Session]
		err       error
		wantCode  int
		wantError string
	}{
		{
			name:     "exact replay",
			result:   reviewapp.CommandResult[domain.Session]{Value: newFakeService().session, Replayed: true},
			wantCode: http.StatusOK,
		},
		{
			name:      "same key changes payload",
			err:       domain.ConflictError(domain.ErrorCodeAnswerIdempotencyConflict, "bound to another request"),
			wantCode:  http.StatusConflict,
			wantError: domain.ErrorCodeAnswerIdempotencyConflict,
		},
		{
			name:      "already closed",
			err:       domain.ConflictError(domain.ErrorCodeSessionClosed, "session is already closed"),
			wantCode:  http.StatusConflict,
			wantError: domain.ErrorCodeSessionClosed,
		},
		{
			name:      "not a review session",
			err:       domain.ConflictError(domain.ErrorCodeSessionTypeConflict, "not a deck-bound review session"),
			wantCode:  http.StatusConflict,
			wantError: domain.ErrorCodeSessionTypeConflict,
		},
		{
			name:      "workspace absent",
			err:       domain.NotFoundError(domain.ErrorCodeWorkspaceNotFound, "review workspace was not found"),
			wantCode:  http.StatusNotFound,
			wantError: domain.ErrorCodeWorkspaceNotFound,
		},
		{
			name:      "session absent",
			err:       domain.NotFoundError(domain.ErrorCodeSessionNotFound, "review session was not found"),
			wantCode:  http.StatusNotFound,
			wantError: domain.ErrorCodeSessionNotFound,
		},
		{
			name:      "database unavailable",
			err:       domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "database is unavailable"),
			wantCode:  http.StatusServiceUnavailable,
			wantError: domain.ErrorCodeDependencyUnavailable,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeService()
			fake.completeResult = test.result
			if fake.completeResult.Value.ID == "" {
				fake.completeResult.Value = fake.session
			}
			fake.completeErr = test.err
			router := chi.NewRouter()
			NewHandler(fake, time.Second).Routes(router)

			request := httptest.NewRequest(http.MethodPost, "/review/sessions/"+testSessionID+"/complete", strings.NewReader(`{"workspace_id":"`+testWorkspaceID+`","cancelled":false}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "complete-session-http-1")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.wantCode {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.wantCode, response.Body.String())
			}
			if fake.complete.IdempotencyKey != "complete-session-http-1" || fake.complete.Cancelled {
				t.Fatalf("complete command=%+v", fake.complete)
			}
			if test.wantError == "" {
				return
			}
			var problem struct {
				ErrorCode string `json:"error_code"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil || problem.ErrorCode != test.wantError {
				t.Fatalf("problem=%+v err=%v want=%s", problem, err, test.wantError)
			}
		})
	}
}

type fakeService struct {
	deck              domain.Deck
	card              domain.Card
	session           domain.Session
	schedule          domain.Schedule
	answerResult      reviewapp.AnswerResult
	createDeck        reviewapp.CreateDeckCommand
	createCard        reviewapp.CreateCardCommand
	editCard          reviewapp.EditCardCommand
	decision          reviewapp.CardDecisionCommand
	invalidation      reviewapp.InvalidateCardsCommand
	complete          reviewapp.CompleteSessionCommand
	submit            reviewapp.SubmitAnswerCommand
	startSession      reviewapp.StartSessionCommand
	completeResult    reviewapp.CommandResult[domain.Session]
	completeErr       error
	submitErr         error
	scheduleAction    reviewapp.DeckScheduleAction
	dueWorkspaceID    foundation.ID
	dueSessionID      foundation.ID
	dueDeckID         *foundation.ID
	dueLimit          int
	createDeckCalls   int
	decisionCalls     int
	startSessionCalls int
}

func newFakeService() *fakeService {
	now := time.Date(2026, 7, 25, 8, 0, 0, 0, time.UTC)
	workspaceID, deckID, cardID := testID(testWorkspaceID), testID(testDeckID), testID(testCardID)
	claimID := testID(testClaimID)
	deck := domain.Deck{ID: deckID, WorkspaceID: workspaceID, Name: "Go", Scope: json.RawMessage(`{}`), Status: domain.DeckStatusActive, DailyLimit: 10, SchedulerVersion: domain.SchedulerVersionFSRSV1, Version: 1, CreatedAt: now, UpdatedAt: now}
	card := domain.Card{ID: cardID, WorkspaceID: workspaceID, DeckID: deckID, ClaimID: &claimID, Question: "Why?", AnswerPoints: []string{"Because."}, Evidence: []domain.EvidenceBinding{{SchemaVersion: domain.EvidenceSchemaVersionV1, ClaimID: claimID, SourceVersionID: testID(testSourceID), SourceSpanID: testID(testSpanID), EvidenceHash: strings.Repeat("a", 64)}}, CardType: domain.CardTypeShortAnswer, Difficulty: 0.5, Status: domain.CardStatusApproved, ModelVersion: "manual", Version: 2, CreatedAt: now, UpdatedAt: now}
	fingerprint, err := domain.ComputeCardFingerprint(card)
	if err != nil {
		panic(err)
	}
	card.Fingerprint = fingerprint
	sessionID := testID(testSessionID)
	session := domain.Session{ID: sessionID, WorkspaceID: workspaceID, DeckID: &deckID, SessionType: domain.SessionTypeReview, Status: domain.SessionStatusCompleted, Config: json.RawMessage(`{}`), IdempotencyKey: "review-http-test", StartedAt: now, EndedAt: &now}
	schedule := domain.Schedule{CardID: cardID, WorkspaceID: workspaceID, DueAt: now, Difficulty: 0.5, SchedulerVersion: domain.SchedulerVersionFSRSV1, Version: 1}
	dimension := domain.ScoreDimension{Value: 1, Rationale: "由服务器答案要点覆盖"}
	answer := domain.Answer{
		ID:             testID("10000000-0000-4000-8000-000000000008"),
		WorkspaceID:    workspaceID,
		SessionID:      sessionID,
		CardID:         &cardID,
		QuestionRef:    "card:1",
		IdempotencyKey: "review-http-test",
		UserAnswer:     "answer",
		Rating:         domain.RatingGood,
		ScorerVersion:  "review-test-scorer/v1",
		Score: domain.Score{
			SchemaVersion: domain.ScoreSchemaVersionV1,
			Correctness:   dimension,
			Coverage:      dimension,
			Boundaries:    dimension,
			Clarity:       dimension,
			Confidence:    dimension,
			Evidence: []domain.EvidenceBinding{{
				SchemaVersion:   domain.EvidenceSchemaVersionV1,
				ClaimID:         claimID,
				SourceVersionID: testID(testSourceID),
				SourceSpanID:    testID(testSpanID),
				EvidenceHash:    strings.Repeat("a", 64),
			}},
		},
		Feedback:  map[string]any{"schema_version": domain.ScoreSchemaVersionV1},
		CreatedAt: now,
	}
	nextSchedule := schedule
	nextSchedule.Version++
	return &fakeService{deck: deck, card: card, session: session, schedule: schedule, answerResult: reviewapp.AnswerResult{Answer: answer, Schedule: nextSchedule}}
}

func (fake *fakeService) CreateDeck(_ context.Context, command reviewapp.CreateDeckCommand) (reviewapp.CommandResult[domain.Deck], error) {
	fake.createDeckCalls++
	fake.createDeck = command
	return reviewapp.CommandResult[domain.Deck]{Value: fake.deck}, nil
}
func (fake *fakeService) GetDeck(context.Context, foundation.ID, foundation.ID) (domain.Deck, error) {
	return fake.deck, nil
}
func (fake *fakeService) ListDecks(context.Context, foundation.ID, int) (reviewapp.DeckPage, error) {
	return reviewapp.DeckPage{Items: []domain.Deck{fake.deck}}, nil
}
func (fake *fakeService) ListCards(context.Context, foundation.ID, foundation.ID, int) (reviewapp.CardPage, error) {
	return reviewapp.CardPage{Items: []domain.Card{fake.card}}, nil
}
func (fake *fakeService) CreateCard(_ context.Context, command reviewapp.CreateCardCommand) (reviewapp.CommandResult[domain.Card], error) {
	fake.createCard = command
	return reviewapp.CommandResult[domain.Card]{Value: fake.card}, nil
}
func (fake *fakeService) EditCard(_ context.Context, command reviewapp.EditCardCommand) (reviewapp.CommandResult[domain.Card], error) {
	fake.editCard = command
	return reviewapp.CommandResult[domain.Card]{Value: fake.card}, nil
}
func (fake *fakeService) ApproveCard(_ context.Context, command reviewapp.CardDecisionCommand) (reviewapp.CommandResult[domain.Card], error) {
	fake.decisionCalls++
	fake.decision = command
	return reviewapp.CommandResult[domain.Card]{Value: fake.card}, nil
}
func (fake *fakeService) RejectCard(_ context.Context, command reviewapp.CardDecisionCommand) (reviewapp.CommandResult[domain.Card], error) {
	fake.decisionCalls++
	fake.decision = command
	return reviewapp.CommandResult[domain.Card]{Value: fake.card}, nil
}
func (fake *fakeService) InvalidateCard(_ context.Context, command reviewapp.CardDecisionCommand) (reviewapp.CommandResult[domain.Card], error) {
	fake.decisionCalls++
	fake.decision = command
	return reviewapp.CommandResult[domain.Card]{Value: fake.card}, nil
}
func (fake *fakeService) InvalidateCards(_ context.Context, command reviewapp.InvalidateCardsCommand) (reviewapp.InvalidationResult, error) {
	fake.invalidation = command
	return reviewapp.InvalidationResult{}, nil
}
func (fake *fakeService) ListDue(_ context.Context, workspaceID, sessionID foundation.ID, deckID *foundation.ID, limit int) ([]reviewapp.DueCard, error) {
	fake.dueWorkspaceID = workspaceID
	fake.dueSessionID = sessionID
	fake.dueDeckID = deckID
	fake.dueLimit = limit
	return []reviewapp.DueCard{{Card: fake.card, Schedule: fake.schedule, QuestionRef: "review-question/v1.test-reference"}}, nil
}
func (fake *fakeService) StartSession(_ context.Context, command reviewapp.StartSessionCommand) (reviewapp.CommandResult[domain.Session], error) {
	fake.startSessionCalls++
	fake.startSession = command
	return reviewapp.CommandResult[domain.Session]{Value: fake.session}, nil
}
func (fake *fakeService) CompleteSession(_ context.Context, command reviewapp.CompleteSessionCommand) (reviewapp.CommandResult[domain.Session], error) {
	fake.complete = command
	if fake.completeErr != nil {
		return reviewapp.CommandResult[domain.Session]{}, fake.completeErr
	}
	if fake.completeResult.Value.ID != "" {
		return fake.completeResult, nil
	}
	return reviewapp.CommandResult[domain.Session]{Value: fake.session}, nil
}
func (fake *fakeService) SubmitAnswer(_ context.Context, command reviewapp.SubmitAnswerCommand) (reviewapp.AnswerResult, error) {
	fake.submit = command
	if fake.submitErr != nil {
		return reviewapp.AnswerResult{}, fake.submitErr
	}
	return fake.answerResult, nil
}
func (fake *fakeService) ChangeDeckSchedule(_ context.Context, _ reviewapp.DeckScheduleCommand, action reviewapp.DeckScheduleAction) (reviewapp.CommandResult[domain.Deck], error) {
	fake.scheduleAction = action
	return reviewapp.CommandResult[domain.Deck]{Value: fake.deck}, nil
}

func testID(value string) foundation.ID { return foundation.ID(value) }
func createCardJSON() string {
	return `{"workspace_id":"` + testWorkspaceID + `","claim_id":"` + testClaimID + `","question":"Why?","answer_points":["Because."],"evidence":[{"schema_version":"review-evidence/v1","claim_id":"` + testClaimID + `","source_version_id":"` + testSourceID + `","source_span_id":"` + testSpanID + `","evidence_hash":"` + strings.Repeat("a", 64) + `"}],"card_type":"SHORT_ANSWER","difficulty":0.5,"model_version":"manual"}`
}
func decisionJSON() string {
	return `{"workspace_id":"` + testWorkspaceID + `","expected_version":1,"reason":"ok"}`
}
func scheduleJSON() string { return `{"workspace_id":"` + testWorkspaceID + `","expected_version":1}` }
