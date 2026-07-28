package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/domain"
)

const reviewTestQuestionRefKey = "review-question-ref-test-key-2026-07"

func TestApproveCardVerifiesEvidenceAndCreatesDueSchedule(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	repository := reviewFakeRepository{card: reviewTestCard(t, now)}
	evidence := &reviewFakeEvidence{}
	service, err := NewService(&repository, evidence, NewDeterministicScorer(), reviewFakeScheduler{}, reviewTestQuestionRefKey, &reviewIDGenerator{}, foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ApproveCard(context.Background(), CardDecisionCommand{WorkspaceID: repository.card.WorkspaceID, CardID: repository.card.ID, ExpectedVersion: 1, IdempotencyKey: "approve-card-1"})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.calls != 1 || repository.lastDecision.InitialSchedule == nil || !repository.lastDecision.InitialSchedule.DueAt.Equal(now) {
		t.Fatalf("evidence_calls=%d decision=%+v", evidence.calls, repository.lastDecision)
	}
	if result.Value.Status != domain.CardStatusApproved {
		t.Fatalf("approved status=%s", result.Value.Status)
	}
}

func TestNewServiceRejectsInvalidScorerVersion(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	for _, version := range []string{"", " review-test/v1 ", strings.Repeat("v", domain.MaxScorerVersionBytes+1)} {
		repository := reviewFakeRepository{}
		service, err := NewService(&repository, &reviewFakeEvidence{}, reviewVersionedScorer{version: version}, reviewFakeScheduler{}, reviewTestQuestionRefKey, &reviewIDGenerator{}, foundation.FixedClock{Value: now})
		if err == nil || service != nil {
			t.Fatalf("invalid scorer version %q produced service=%#v err=%v", version, service, err)
		}
	}
}

func TestNewServiceRejectsInvalidQuestionRefKey(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	for _, key := range []string{"", "short", " " + reviewTestQuestionRefKey + " "} {
		repository := reviewFakeRepository{}
		service, err := NewService(&repository, &reviewFakeEvidence{}, NewDeterministicScorer(), reviewFakeScheduler{}, key, &reviewIDGenerator{}, foundation.FixedClock{Value: now})
		if err == nil || service != nil {
			t.Fatalf("invalid question ref key %q produced service=%#v err=%v", key, service, err)
		}
	}
}

func TestQuestionRefSharedKeySurvivesServiceRecreation(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	repository := reviewFakeRepository{card: reviewTestCard(t, now), session: reviewTestSession(now), schedule: reviewTestSchedule(now)}
	repository.card.Status = domain.CardStatusApproved
	issuer, err := NewService(&repository, &reviewFakeEvidence{}, NewDeterministicScorer(), reviewFakeScheduler{}, reviewTestQuestionRefKey, &reviewIDGenerator{}, foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := NewService(&repository, &reviewFakeEvidence{}, NewDeterministicScorer(), reviewFakeScheduler{}, reviewTestQuestionRefKey, &reviewIDGenerator{}, foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatal(err)
	}
	questionRef := reviewTestQuestionRef(t, issuer, repository.session.ID, repository.card, repository.schedule)
	if _, err := consumer.SubmitAnswer(context.Background(), SubmitAnswerCommand{
		WorkspaceID: repository.card.WorkspaceID, SessionID: repository.session.ID, CardID: repository.card.ID,
		QuestionRef: questionRef, UserAnswer: "答复", Rating: domain.RatingGood, IdempotencyKey: "answer-after-restart",
	}); err != nil || repository.submitCalls != 1 {
		t.Fatalf("shared-key submit err=%v submit_calls=%d", err, repository.submitCalls)
	}

	rotated, err := NewService(&repository, &reviewFakeEvidence{}, NewDeterministicScorer(), reviewFakeScheduler{}, "different-review-question-ref-key-2026", &reviewIDGenerator{}, foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := rotated.questionRefs.verify(questionRef, repository.session.ID, repository.card, repository.schedule); reviewErrorCode(err) != domain.ErrorCodeQuestionStale {
		t.Fatalf("rotated key error=%v", err)
	}
}

func TestApproveCardReturnsReplayBeforeCurrentStateValidation(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	card := reviewTestCard(t, now)
	card.Status = domain.CardStatusApproved
	card.Version = 2
	repository := reviewFakeRepository{
		card:                    card,
		cardDecisionReplay:      CommandResult[domain.Card]{Value: card, Replayed: true},
		cardDecisionReplayFound: true,
	}
	evidence := &reviewFakeEvidence{}
	service, err := NewService(&repository, evidence, NewDeterministicScorer(), reviewFakeScheduler{}, reviewTestQuestionRefKey, &reviewIDGenerator{}, foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ApproveCard(context.Background(), CardDecisionCommand{WorkspaceID: card.WorkspaceID, CardID: card.ID, ExpectedVersion: 1, IdempotencyKey: "approve-card-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Replayed || result.Value.ID != card.ID || repository.getCardCalls != 0 || evidence.calls != 0 {
		t.Fatalf("result=%+v get_card_calls=%d evidence_calls=%d", result, repository.getCardCalls, evidence.calls)
	}
}

func TestApproveCardRechecksReplayAfterConcurrentStateChange(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	card := reviewTestCard(t, now)
	card.Status = domain.CardStatusApproved
	card.Version = 2
	repository := reviewFakeRepository{
		card:                    card,
		cardDecisionReplay:      CommandResult[domain.Card]{Value: card, Replayed: true},
		cardDecisionReplayFound: true,
		cardDecisionReplayAfter: 2,
	}
	service, err := NewService(&repository, &reviewFakeEvidence{}, NewDeterministicScorer(), reviewFakeScheduler{}, reviewTestQuestionRefKey, &reviewIDGenerator{}, foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ApproveCard(context.Background(), CardDecisionCommand{WorkspaceID: card.WorkspaceID, CardID: card.ID, ExpectedVersion: 1, IdempotencyKey: "approve-card-1"})
	if err != nil || !result.Replayed || result.Value.ID != card.ID || repository.cardDecisionReplayCalls != 2 {
		t.Fatalf("result=%+v err=%v replay_calls=%d", result, err, repository.cardDecisionReplayCalls)
	}
}

func TestSubmitAnswerRejectsScoreEvidenceOutsideCard(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	repository := reviewFakeRepository{card: reviewTestCard(t, now), session: reviewTestSession(now), schedule: reviewTestSchedule(now)}
	repository.card.Status = domain.CardStatusApproved
	score := reviewTestScore(repository.card.Evidence)
	score.Evidence[0].EvidenceHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	service, err := NewService(&repository, &reviewFakeEvidence{}, &reviewFakeScorer{score: score}, reviewFakeScheduler{}, reviewTestQuestionRefKey, &reviewIDGenerator{}, foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.SubmitAnswer(context.Background(), SubmitAnswerCommand{WorkspaceID: repository.card.WorkspaceID, SessionID: repository.session.ID, CardID: repository.card.ID, QuestionRef: reviewTestQuestionRef(t, service, repository.session.ID, repository.card, repository.schedule), UserAnswer: "答复", Rating: domain.RatingGood, IdempotencyKey: "answer-1"})
	if err == nil || repository.submitCalls != 0 {
		t.Fatalf("foreign score evidence err=%v submit_calls=%d", err, repository.submitCalls)
	}
}

func TestSubmitAnswerRejectsScoreEvidenceWithForgedSourceTuple(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	repository := reviewFakeRepository{card: reviewTestCard(t, now), session: reviewTestSession(now), schedule: reviewTestSchedule(now)}
	repository.card.Status = domain.CardStatusApproved
	score := reviewTestScore(repository.card.Evidence)
	score.Evidence[0].SourceSpanID = foundation.ID("80000000-0000-4000-8000-000000000009")
	service, err := NewService(&repository, &reviewFakeEvidence{}, &reviewFakeScorer{score: score}, reviewFakeScheduler{}, reviewTestQuestionRefKey, &reviewIDGenerator{}, foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.SubmitAnswer(context.Background(), SubmitAnswerCommand{WorkspaceID: repository.card.WorkspaceID, SessionID: repository.session.ID, CardID: repository.card.ID, QuestionRef: reviewTestQuestionRef(t, service, repository.session.ID, repository.card, repository.schedule), UserAnswer: "答复", Rating: domain.RatingGood, IdempotencyKey: "answer-1"})
	if err == nil || repository.submitCalls != 0 {
		t.Fatalf("forged score evidence err=%v submit_calls=%d", err, repository.submitCalls)
	}
}

func TestSubmitAnswerRejectsCardOutsideServerDueSetBeforeScoring(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	repository := reviewFakeRepository{
		card:           reviewTestCard(t, now),
		session:        reviewTestSession(now),
		schedule:       reviewTestSchedule(now),
		eligibilityErr: domain.ConflictError(domain.ErrorCodeScheduleConflict, "card is outside the server due set"),
	}
	repository.card.Status = domain.CardStatusApproved
	scorer := &reviewFakeScorer{score: reviewTestScore(repository.card.Evidence)}
	service, err := NewService(&repository, &reviewFakeEvidence{}, scorer, reviewFakeScheduler{}, reviewTestQuestionRefKey, &reviewIDGenerator{}, foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.SubmitAnswer(context.Background(), SubmitAnswerCommand{
		WorkspaceID: repository.card.WorkspaceID, SessionID: repository.session.ID, CardID: repository.card.ID,
		QuestionRef: reviewTestQuestionRef(t, service, repository.session.ID, repository.card, repository.schedule), UserAnswer: "答复", Rating: domain.RatingGood, IdempotencyKey: "answer-not-due",
	})
	if reviewErrorCode(err) != domain.ErrorCodeScheduleConflict || repository.eligibilityCalls != 1 || repository.eligibilityDeckID != repository.card.DeckID || scorer.calls != 0 || repository.getScheduleCalls != 1 || repository.submitCalls != 0 {
		t.Fatalf("err=%v eligibility=%d scorer=%d schedule=%d submit=%d", err, repository.eligibilityCalls, scorer.calls, repository.getScheduleCalls, repository.submitCalls)
	}
}

func TestSubmitAnswerPassesExpectedCardAndScheduleVersions(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	repository := reviewFakeRepository{card: reviewTestCard(t, now), session: reviewTestSession(now), schedule: reviewTestSchedule(now)}
	repository.card.Status = domain.CardStatusApproved
	service, err := NewService(&repository, &reviewFakeEvidence{}, NewDeterministicScorer(), reviewFakeScheduler{}, reviewTestQuestionRefKey, &reviewIDGenerator{}, foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.SubmitAnswer(context.Background(), SubmitAnswerCommand{WorkspaceID: repository.card.WorkspaceID, SessionID: repository.session.ID, CardID: repository.card.ID, QuestionRef: reviewTestQuestionRef(t, service, repository.session.ID, repository.card, repository.schedule), UserAnswer: "答复", Rating: domain.RatingGood, IdempotencyKey: "answer-1"})
	if err != nil {
		t.Fatal(err)
	}
	if repository.lastAnswer.ExpectedCardVersion != repository.card.Version || repository.lastAnswer.ExpectedScheduleVersion != repository.schedule.Version ||
		repository.lastAnswer.Answer.ScorerVersion != deterministicScorerVersion || result.Answer.ScorerVersion != deterministicScorerVersion ||
		result.Schedule.Version != repository.schedule.Version+1 || repository.submitCalls != 1 {
		t.Fatalf("record=%+v result=%+v", repository.lastAnswer, result)
	}
}

func TestSubmitAnswerFailsClosedWhenScorerIsUnavailable(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	repository := reviewFakeRepository{card: reviewTestCard(t, now), session: reviewTestSession(now), schedule: reviewTestSchedule(now)}
	repository.card.Status = domain.CardStatusApproved
	service, err := NewService(&repository, &reviewFakeEvidence{}, &reviewFakeScorer{err: errors.New("scorer offline")}, reviewFakeScheduler{}, reviewTestQuestionRefKey, &reviewIDGenerator{}, foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.SubmitAnswer(context.Background(), SubmitAnswerCommand{
		WorkspaceID:    repository.card.WorkspaceID,
		SessionID:      repository.session.ID,
		CardID:         repository.card.ID,
		QuestionRef:    reviewTestQuestionRef(t, service, repository.session.ID, repository.card, repository.schedule),
		UserAnswer:     "答复",
		Rating:         domain.RatingGood,
		IdempotencyKey: "answer-scorer-unavailable",
	})
	if reviewErrorCode(err) != domain.ErrorCodeScoringUnavailable || repository.submitCalls != 0 || repository.getScheduleCalls != 1 {
		t.Fatalf("err=%v code=%s submit_calls=%d schedule_calls=%d", err, reviewErrorCode(err), repository.submitCalls, repository.getScheduleCalls)
	}
}

func TestEditCardRevalidatesEvidenceAndReturnsDraft(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	current := reviewTestCard(t, now)
	current.Status = domain.CardStatusApproved
	current.Version = 2
	repository := reviewFakeRepository{card: current}
	evidence := &reviewFakeEvidence{}
	service, err := NewService(&repository, evidence, NewDeterministicScorer(), reviewFakeScheduler{}, reviewTestQuestionRefKey, &reviewIDGenerator{}, foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatal(err)
	}
	claimID := *current.ClaimID
	result, err := service.EditCard(context.Background(), EditCardCommand{
		WorkspaceID:     current.WorkspaceID,
		CardID:          current.ID,
		ExpectedVersion: current.Version,
		ClaimID:         claimID,
		Question:        "编辑后的问题",
		AnswerPoints:    []string{"编辑后的答案要点"},
		Evidence:        current.Evidence,
		CardType:        domain.CardTypeShortAnswer,
		Difficulty:      0.4,
		ModelVersion:    "manual",
		IdempotencyKey:  "card-edit-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.calls != 1 || result.Value.Status != domain.CardStatusDraft || result.Value.Version != current.Version+1 || repository.lastEdit.Card.Status != domain.CardStatusDraft {
		t.Fatalf("evidence_calls=%d result=%+v record=%+v", evidence.calls, result.Value, repository.lastEdit)
	}
}

func TestSubmitAnswerReturnsReplayBeforeMutableStateValidation(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	card := reviewTestCard(t, now)
	card.Status = domain.CardStatusInvalidated
	session := reviewTestSession(now)
	session.Status = domain.SessionStatusCompleted
	session.EndedAt = &now
	replayed := AnswerResult{
		Answer:   domain.Answer{ID: foundation.ID("80000000-0000-4000-8000-000000000008")},
		Schedule: domain.Schedule{CardID: card.ID, WorkspaceID: card.WorkspaceID, Version: 4},
		Replayed: true,
	}
	repository := reviewFakeRepository{
		card:              card,
		session:           session,
		schedule:          reviewTestSchedule(now),
		answerReplay:      replayed,
		answerReplayFound: true,
	}
	service, err := NewService(&repository, &reviewFakeEvidence{}, NewDeterministicScorer(), reviewFakeScheduler{}, reviewTestQuestionRefKey, &reviewIDGenerator{}, foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.SubmitAnswer(context.Background(), SubmitAnswerCommand{WorkspaceID: card.WorkspaceID, SessionID: session.ID, CardID: card.ID, QuestionRef: reviewTestQuestionRef(t, service, session.ID, card, repository.schedule), UserAnswer: "历史答复", Rating: domain.RatingGood, IdempotencyKey: "answer-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Replayed || result.Answer.ID != replayed.Answer.ID || repository.getSessionCalls != 0 || repository.getCardCalls != 0 || repository.getScheduleCalls != 0 || repository.submitCalls != 0 {
		t.Fatalf("result=%+v session=%d card=%d schedule=%d submit=%d", result, repository.getSessionCalls, repository.getCardCalls, repository.getScheduleCalls, repository.submitCalls)
	}
}

func TestSubmitAnswerRechecksReplayAfterConcurrentSessionClose(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	card := reviewTestCard(t, now)
	session := reviewTestSession(now)
	session.Status = domain.SessionStatusCompleted
	session.EndedAt = &now
	replayed := AnswerResult{
		Answer:   domain.Answer{ID: foundation.ID("80000000-0000-4000-8000-000000000008")},
		Schedule: domain.Schedule{CardID: card.ID, WorkspaceID: card.WorkspaceID, Version: 4},
		Replayed: true,
	}
	repository := reviewFakeRepository{
		card:              card,
		session:           session,
		schedule:          reviewTestSchedule(now),
		answerReplay:      replayed,
		answerReplayFound: true,
		answerReplayAfter: 2,
	}
	service, err := NewService(&repository, &reviewFakeEvidence{}, NewDeterministicScorer(), reviewFakeScheduler{}, reviewTestQuestionRefKey, &reviewIDGenerator{}, foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.SubmitAnswer(context.Background(), SubmitAnswerCommand{WorkspaceID: card.WorkspaceID, SessionID: session.ID, CardID: card.ID, QuestionRef: reviewTestQuestionRef(t, service, session.ID, card, repository.schedule), UserAnswer: "历史答复", Rating: domain.RatingGood, IdempotencyKey: "answer-1"})
	if err != nil || !result.Replayed || result.Answer.ID != replayed.Answer.ID || repository.answerReplayCalls != 2 {
		t.Fatalf("result=%+v err=%v replay_calls=%d", result, err, repository.answerReplayCalls)
	}
}

func TestStartSessionLetsRepositoryReplayAfterDeckPaused(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	deckID := foundation.ID("80000000-0000-4000-8000-000000000004")
	session := reviewTestSession(now)
	repository := reviewFakeRepository{
		deck:               domain.Deck{ID: deckID, Status: domain.DeckStatusPaused},
		startSessionResult: CommandResult[domain.Session]{Value: session, Replayed: true},
	}
	service, err := NewService(&repository, &reviewFakeEvidence{}, NewDeterministicScorer(), reviewFakeScheduler{}, reviewTestQuestionRefKey, &reviewIDGenerator{}, foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.StartSession(context.Background(), StartSessionCommand{WorkspaceID: session.WorkspaceID, DeckID: &deckID, SessionType: domain.SessionTypeReview, IdempotencyKey: session.IdempotencyKey})
	if err != nil || !result.Replayed || result.Value.ID != session.ID || repository.getDeckCalls != 0 {
		t.Fatalf("result=%+v err=%v get_deck_calls=%d", result, err, repository.getDeckCalls)
	}
}

func TestStartSessionRejectsInterviewOrMissingDeck(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	deckID := foundation.ID("80000000-0000-4000-8000-000000000004")
	tests := []struct {
		name        string
		deckID      *foundation.ID
		sessionType domain.SessionType
	}{
		{name: "interview", sessionType: domain.SessionTypeInterview},
		{name: "missing deck", sessionType: domain.SessionTypeReview},
		{name: "interview with deck", deckID: &deckID, sessionType: domain.SessionTypeInterview},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := reviewFakeRepository{}
			service, err := NewService(&repository, &reviewFakeEvidence{}, NewDeterministicScorer(), reviewFakeScheduler{}, reviewTestQuestionRefKey, &reviewIDGenerator{}, foundation.FixedClock{Value: now})
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.StartSession(context.Background(), StartSessionCommand{
				WorkspaceID: foundation.ID("80000000-0000-4000-8000-000000000003"), DeckID: test.deckID,
				SessionType: test.sessionType, IdempotencyKey: "invalid-session-start",
			})
			if reviewErrorCode(err) != domain.ErrorCodeSessionInvalid || repository.startSessionCalls != 0 {
				t.Fatalf("err=%v code=%s start_calls=%d", err, reviewErrorCode(err), repository.startSessionCalls)
			}
		})
	}
}

func TestReviewOperationsRejectInterviewOrUnboundSession(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		session domain.Session
	}{
		{name: "interview", session: func() domain.Session {
			value := reviewTestSession(now)
			value.SessionType = domain.SessionTypeInterview
			value.DeckID = nil
			return value
		}()},
		{name: "review without deck", session: func() domain.Session {
			value := reviewTestSession(now)
			value.DeckID = nil
			return value
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := reviewFakeRepository{card: reviewTestCard(t, now), session: test.session, schedule: reviewTestSchedule(now)}
			repository.card.Status = domain.CardStatusApproved
			scorer := &reviewFakeScorer{score: reviewTestScore(repository.card.Evidence)}
			service, err := NewService(&repository, &reviewFakeEvidence{}, scorer, reviewFakeScheduler{}, reviewTestQuestionRefKey, &reviewIDGenerator{}, foundation.FixedClock{Value: now})
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.SubmitAnswer(context.Background(), SubmitAnswerCommand{
				WorkspaceID: repository.card.WorkspaceID, SessionID: test.session.ID, CardID: repository.card.ID,
				QuestionRef: reviewTestQuestionRef(t, service, repository.session.ID, repository.card, repository.schedule), UserAnswer: "答复", Rating: domain.RatingGood, IdempotencyKey: "invalid-session-answer",
			})
			if reviewErrorCode(err) != domain.ErrorCodeSessionTypeConflict || repository.getCardCalls != 0 || scorer.calls != 0 || repository.submitCalls != 0 {
				t.Fatalf("submit err=%v card_calls=%d scorer_calls=%d submit_calls=%d", err, repository.getCardCalls, scorer.calls, repository.submitCalls)
			}
			_, err = service.CompleteSession(context.Background(), CompleteSessionCommand{
				WorkspaceID: test.session.WorkspaceID, SessionID: test.session.ID, IdempotencyKey: "invalid-session-complete",
			})
			if reviewErrorCode(err) != domain.ErrorCodeSessionTypeConflict || repository.completeCalls != 0 {
				t.Fatalf("complete err=%v complete_calls=%d", err, repository.completeCalls)
			}
		})
	}
}

func TestCompleteSessionUsesDurableIdempotencyRecord(t *testing.T) {
	now := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	session := reviewTestSession(now)
	repository := reviewFakeRepository{session: session}
	service, err := NewService(&repository, &reviewFakeEvidence{}, NewDeterministicScorer(), reviewFakeScheduler{}, reviewTestQuestionRefKey, &reviewIDGenerator{}, foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CompleteSession(context.Background(), CompleteSessionCommand{WorkspaceID: session.WorkspaceID, SessionID: session.ID, IdempotencyKey: "session-complete-1"}); err != nil {
		t.Fatal(err)
	}
	if repository.lastComplete.Status != domain.SessionStatusCompleted || repository.lastComplete.IdempotencyKey != "session-complete-1" || len(repository.lastComplete.RequestHash) != 64 || !repository.lastComplete.At.Equal(now) {
		t.Fatalf("complete record=%+v", repository.lastComplete)
	}
	if _, err := service.CompleteSession(context.Background(), CompleteSessionCommand{WorkspaceID: session.WorkspaceID, SessionID: session.ID}); err == nil {
		t.Fatal("missing complete idempotency key was accepted")
	}
}

type reviewFakeRepository struct {
	deck                    domain.Deck
	card                    domain.Card
	session                 domain.Session
	schedule                domain.Schedule
	cardDecisionReplay      CommandResult[domain.Card]
	cardDecisionReplayFound bool
	cardDecisionReplayAfter int
	cardDecisionReplayCalls int
	cardCreateReplay        CommandResult[domain.Card]
	cardCreateReplayFound   bool
	cardEditReplay          CommandResult[domain.Card]
	cardEditReplayFound     bool
	answerReplay            AnswerResult
	answerReplayFound       bool
	answerReplayAfter       int
	answerReplayCalls       int
	startSessionResult      CommandResult[domain.Session]
	startSessionCalls       int
	lastDecision            CardDecisionRecord
	lastEdit                EditCardRecord
	lastInvalidation        InvalidateCardsRecord
	lastAnswer              SubmitAnswerRecord
	lastComplete            CompleteSessionRecord
	completeCalls           int
	getCardCalls            int
	getDeckCalls            int
	getSessionCalls         int
	getScheduleCalls        int
	eligibilityCalls        int
	eligibilityDeckID       foundation.ID
	eligibilityErr          error
	submitCalls             int
}

func (repository *reviewFakeRepository) CreateDeck(context.Context, domain.Deck, string, string) (CommandResult[domain.Deck], error) {
	return CommandResult[domain.Deck]{}, nil
}
func (repository *reviewFakeRepository) GetDeck(context.Context, foundation.ID, foundation.ID) (domain.Deck, error) {
	repository.getDeckCalls++
	return repository.deck, nil
}
func (repository *reviewFakeRepository) ListDecks(context.Context, foundation.ID, int) (DeckPage, error) {
	return DeckPage{}, nil
}
func (repository *reviewFakeRepository) ListCards(context.Context, foundation.ID, foundation.ID, int) (CardPage, error) {
	return CardPage{Items: []domain.Card{repository.card}}, nil
}
func (repository *reviewFakeRepository) CreateCard(context.Context, domain.Card, string, string) (CommandResult[domain.Card], error) {
	return CommandResult[domain.Card]{}, nil
}
func (repository *reviewFakeRepository) FindCardCreateReplay(context.Context, foundation.ID, string, string) (CommandResult[domain.Card], bool, error) {
	return repository.cardCreateReplay, repository.cardCreateReplayFound, nil
}
func (repository *reviewFakeRepository) GetCard(context.Context, foundation.ID, foundation.ID) (domain.Card, error) {
	repository.getCardCalls++
	return repository.card, nil
}
func (repository *reviewFakeRepository) FindCardEditReplay(context.Context, foundation.ID, string, string) (CommandResult[domain.Card], bool, error) {
	return repository.cardEditReplay, repository.cardEditReplayFound, nil
}
func (repository *reviewFakeRepository) EditCard(_ context.Context, record EditCardRecord) (CommandResult[domain.Card], error) {
	repository.lastEdit = record
	return CommandResult[domain.Card]{Value: record.Card}, nil
}
func (repository *reviewFakeRepository) FindCardDecisionReplay(context.Context, foundation.ID, string, string) (CommandResult[domain.Card], bool, error) {
	repository.cardDecisionReplayCalls++
	if repository.cardDecisionReplayAfter > 0 && repository.cardDecisionReplayCalls < repository.cardDecisionReplayAfter {
		return CommandResult[domain.Card]{}, false, nil
	}
	return repository.cardDecisionReplay, repository.cardDecisionReplayFound, nil
}
func (repository *reviewFakeRepository) DecideCard(_ context.Context, record CardDecisionRecord) (CommandResult[domain.Card], error) {
	repository.lastDecision = record
	card := repository.card
	card.Status = record.TargetStatus
	card.Version++
	return CommandResult[domain.Card]{Value: card}, nil
}
func (repository *reviewFakeRepository) InvalidateCards(_ context.Context, record InvalidateCardsRecord) (InvalidationResult, error) {
	repository.lastInvalidation = record
	return InvalidationResult{}, nil
}
func (repository *reviewFakeRepository) ListDue(context.Context, foundation.ID, *foundation.ID, time.Time, int) ([]DueCard, error) {
	return nil, nil
}
func (repository *reviewFakeRepository) StartSession(context.Context, domain.Session, string) (CommandResult[domain.Session], error) {
	repository.startSessionCalls++
	return repository.startSessionResult, nil
}
func (repository *reviewFakeRepository) GetSession(context.Context, foundation.ID, foundation.ID) (domain.Session, error) {
	repository.getSessionCalls++
	return repository.session, nil
}
func (repository *reviewFakeRepository) CompleteSession(_ context.Context, record CompleteSessionRecord) (CommandResult[domain.Session], error) {
	repository.completeCalls++
	repository.lastComplete = record
	return CommandResult[domain.Session]{Value: repository.session}, nil
}
func (repository *reviewFakeRepository) GetSchedule(context.Context, foundation.ID, foundation.ID) (domain.Schedule, error) {
	repository.getScheduleCalls++
	return repository.schedule, nil
}
func (repository *reviewFakeRepository) CheckAnswerEligibility(_ context.Context, _, deckID, _ foundation.ID, _ time.Time) error {
	repository.eligibilityCalls++
	repository.eligibilityDeckID = deckID
	return repository.eligibilityErr
}
func (repository *reviewFakeRepository) FindAnswerReplay(context.Context, foundation.ID, string, string) (AnswerResult, bool, error) {
	repository.answerReplayCalls++
	if repository.answerReplayAfter > 0 && repository.answerReplayCalls < repository.answerReplayAfter {
		return AnswerResult{}, false, nil
	}
	return repository.answerReplay, repository.answerReplayFound, nil
}
func (repository *reviewFakeRepository) SubmitAnswer(_ context.Context, record SubmitAnswerRecord) (AnswerResult, error) {
	repository.submitCalls++
	repository.lastAnswer = record
	next := repository.schedule
	next.Version++
	next.DueAt = record.ScheduleDecision.DueAt
	return AnswerResult{Answer: record.Answer, Schedule: next}, nil
}
func (repository *reviewFakeRepository) ChangeDeckSchedule(context.Context, DeckScheduleRecord) (CommandResult[domain.Deck], error) {
	return CommandResult[domain.Deck]{}, nil
}

type reviewFakeEvidence struct{ calls int }

func (evidence *reviewFakeEvidence) VerifyCardEvidence(context.Context, foundation.ID, foundation.ID, []domain.EvidenceBinding) error {
	evidence.calls++
	return nil
}

type reviewFakeScorer struct {
	score domain.Score
	err   error
	calls int
	input ScoreInput
}

type reviewVersionedScorer struct{ version string }

func (scorer reviewVersionedScorer) Version() string { return scorer.version }
func (reviewVersionedScorer) Score(context.Context, ScoreInput) (domain.Score, error) {
	return domain.Score{}, nil
}

func (scorer *reviewFakeScorer) Version() string { return "review-test-scorer/v1" }
func (scorer *reviewFakeScorer) Score(_ context.Context, input ScoreInput) (domain.Score, error) {
	scorer.calls++
	scorer.input = input
	return scorer.score, scorer.err
}

type reviewFakeScheduler struct{}

func (reviewFakeScheduler) Version() string { return domain.SchedulerVersionFSRSV1 }
func (reviewFakeScheduler) Next(input domain.ScheduleInput) (domain.ScheduleDecision, error) {
	return domain.ScheduleDecision{DueAt: input.Now.Add(48 * time.Hour), IntervalDays: 2, Stability: 2, Difficulty: 0.5, SchedulerVersion: domain.SchedulerVersionFSRSV1}, nil
}

type reviewIDGenerator struct{ next int }

func (generator *reviewIDGenerator) New() (foundation.ID, error) {
	generator.next++
	return foundation.ID("90000000-0000-4000-8000-000000000001"), nil
}

func reviewTestCard(t *testing.T, now time.Time) domain.Card {
	t.Helper()
	claimID := foundation.ID("80000000-0000-4000-8000-000000000001")
	card := domain.Card{ID: foundation.ID("80000000-0000-4000-8000-000000000002"), WorkspaceID: foundation.ID("80000000-0000-4000-8000-000000000003"), DeckID: foundation.ID("80000000-0000-4000-8000-000000000004"), ClaimID: &claimID, Question: "解释幂等调度", AnswerPoints: []string{"同一次答题只推进一次"}, Evidence: []domain.EvidenceBinding{{SchemaVersion: domain.EvidenceSchemaVersionV1, ClaimID: claimID, SourceVersionID: foundation.ID("80000000-0000-4000-8000-000000000005"), SourceSpanID: foundation.ID("80000000-0000-4000-8000-000000000006"), EvidenceHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}, CardType: domain.CardTypeShortAnswer, Difficulty: 0.5, Status: domain.CardStatusDraft, ModelVersion: "manual", Version: 1, CreatedAt: now, UpdatedAt: now}
	fingerprint, err := domain.ComputeCardFingerprint(card)
	if err != nil {
		t.Fatal(err)
	}
	card.Fingerprint = fingerprint
	return card
}

func reviewTestSession(now time.Time) domain.Session {
	deckID := foundation.ID("80000000-0000-4000-8000-000000000004")
	return domain.Session{ID: foundation.ID("80000000-0000-4000-8000-000000000007"), WorkspaceID: foundation.ID("80000000-0000-4000-8000-000000000003"), DeckID: &deckID, SessionType: domain.SessionTypeReview, Status: domain.SessionStatusActive, Config: json.RawMessage(`{}`), IdempotencyKey: "session-1", StartedAt: now}
}

func reviewTestSchedule(now time.Time) domain.Schedule {
	return domain.Schedule{CardID: foundation.ID("80000000-0000-4000-8000-000000000002"), WorkspaceID: foundation.ID("80000000-0000-4000-8000-000000000003"), DueAt: now, IntervalDays: 0, Stability: 0, Difficulty: 0.5, SchedulerVersion: domain.SchedulerVersionFSRSV1, Version: 3}
}

func reviewTestQuestionRef(t *testing.T, service *Service, sessionID foundation.ID, card domain.Card, schedule domain.Schedule) string {
	t.Helper()
	questionRef, err := service.questionRefs.issue(sessionID, card, schedule)
	if err != nil {
		t.Fatal(err)
	}
	return questionRef
}

func reviewTestScore(evidence []domain.EvidenceBinding) domain.Score {
	dimension := domain.ScoreDimension{Value: 0.8, Rationale: "由卡片证据支持"}
	return domain.Score{SchemaVersion: domain.ScoreSchemaVersionV1, Correctness: dimension, Coverage: dimension, Boundaries: dimension, Clarity: dimension, Confidence: dimension, Evidence: append([]domain.EvidenceBinding(nil), evidence...)}
}

func reviewErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}
