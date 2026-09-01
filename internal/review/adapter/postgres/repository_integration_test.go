//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	platformscheduler "github.com/CodeZen-Lizhi/zhixu/internal/platform/scheduler"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	reviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	gormlogger "gorm.io/gorm/logger"
)

const reviewIntegrationQuestionRefKey = "review-question-ref-integration-key-2026"

func TestReviewRepositoryAnswerScheduleAtomicAndIdempotent(t *testing.T) {
	runReviewIntegrationVariants(t, testReviewRepositoryAnswerScheduleAtomicAndIdempotent)
}

func testReviewRepositoryAnswerScheduleAtomicAndIdempotent(t *testing.T, variant reviewIntegrationVariant) {
	ctx := context.Background()
	repository, platform, pool := variant.open(t)
	seedReviewEvidence(t, ctx, pool)
	fsrs, err := platformscheduler.NewFSRSAdapter()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	service, err := reviewapp.NewService(repository, repository, reviewapp.NewDeterministicScorer(), fsrs, reviewIntegrationQuestionRefKey, foundation.NewUUIDGenerator(nil), foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := foundation.ID("71000000-0000-4000-8000-000000000001")
	claimID := foundation.ID("71000000-0000-4000-8000-000000000007")
	evidence := []domain.EvidenceBinding{{SchemaVersion: domain.EvidenceSchemaVersionV1, ClaimID: claimID, SourceVersionID: foundation.ID("71000000-0000-4000-8000-000000000004"), SourceSpanID: foundation.ID("71000000-0000-4000-8000-000000000006"), EvidenceHash: reviewHash("claim-source")}}
	deckResult, err := service.CreateDeck(ctx, reviewapp.CreateDeckCommand{WorkspaceID: workspaceID, Name: "M8 Review", DailyLimit: 20, IdempotencyKey: "deck-create-1"})
	if err != nil {
		t.Fatal(err)
	}
	deckReplay, err := service.CreateDeck(ctx, reviewapp.CreateDeckCommand{WorkspaceID: workspaceID, Name: "M8 Review", DailyLimit: 20, IdempotencyKey: "deck-create-1"})
	if err != nil || !deckReplay.Replayed || deckReplay.Value.ID != deckResult.Value.ID {
		t.Fatalf("deck replay=%+v err=%v", deckReplay, err)
	}
	cardResult, err := service.CreateCard(ctx, reviewapp.CreateCardCommand{WorkspaceID: workspaceID, DeckID: deckResult.Value.ID, ClaimID: claimID, Question: "为什么答题与调度必须同事务？", AnswerPoints: []string{"重复提交不得推进两次"}, Evidence: evidence, CardType: domain.CardTypeShortAnswer, Difficulty: 0.5, ModelVersion: "manual", IdempotencyKey: "card-create-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateCard(ctx, reviewapp.CreateCardCommand{WorkspaceID: workspaceID, DeckID: deckResult.Value.ID, ClaimID: claimID, Question: "为什么答题与调度必须同事务？", AnswerPoints: []string{"重复提交不得推进两次"}, Evidence: evidence, CardType: domain.CardTypeShortAnswer, Difficulty: 0.5, ModelVersion: "model/v2", IdempotencyKey: "card-create-1"}); reviewPersistenceErrorCode(err) != domain.ErrorCodeAnswerIdempotencyConflict {
		t.Fatalf("same card idempotency key ignored model version: %v", err)
	}
	approved, err := service.ApproveCard(ctx, reviewapp.CardDecisionCommand{WorkspaceID: workspaceID, CardID: cardResult.Value.ID, ExpectedVersion: 1, IdempotencyKey: "card-approve-1"})
	if err != nil || approved.Value.Status != domain.CardStatusApproved {
		t.Fatalf("approve=%+v err=%v", approved, err)
	}
	approvalReplay, err := service.ApproveCard(ctx, reviewapp.CardDecisionCommand{WorkspaceID: workspaceID, CardID: cardResult.Value.ID, ExpectedVersion: 1, IdempotencyKey: "card-approve-1"})
	if err != nil || !approvalReplay.Replayed || approvalReplay.Value.ID != approved.Value.ID || approvalReplay.Value.Version != approved.Value.Version {
		t.Fatalf("approval replay=%+v err=%v original=%+v", approvalReplay, err, approved)
	}
	if _, err := pool.Exec(ctx, `UPDATE learning.review_card SET claim_id=NULL WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(cardResult.Value.ID)); err == nil {
		t.Fatal("review card claim_id accepted NULL")
	}
	sessionResult, err := service.StartSession(ctx, reviewapp.StartSessionCommand{WorkspaceID: workspaceID, DeckID: &deckResult.Value.ID, SessionType: domain.SessionTypeReview, IdempotencyKey: "session-start-1"})
	if err != nil {
		t.Fatal(err)
	}
	questionRef := reviewDueQuestionRef(t, ctx, service, workspaceID, sessionResult.Value.ID, deckResult.Value.ID, cardResult.Value.ID)
	answerCommand := reviewapp.SubmitAnswerCommand{WorkspaceID: workspaceID, SessionID: sessionResult.Value.ID, CardID: cardResult.Value.ID, QuestionRef: questionRef, UserAnswer: "同一事务确保原子性，重复提交不得推进两次", Rating: domain.RatingGood, IdempotencyKey: "answer-submit-1"}
	type answerAttempt struct {
		result reviewapp.AnswerResult
		err    error
	}
	startAnswer := make(chan struct{})
	answerAttempts := make(chan answerAttempt, 2)
	for range 2 {
		go func() {
			<-startAnswer
			result, submitErr := service.SubmitAnswer(ctx, answerCommand)
			answerAttempts <- answerAttempt{result: result, err: submitErr}
		}()
	}
	close(startAnswer)
	firstAttempt, secondAttempt := <-answerAttempts, <-answerAttempts
	if firstAttempt.err != nil || secondAttempt.err != nil {
		t.Fatalf("concurrent same-key answers failed: first=%v second=%v", firstAttempt.err, secondAttempt.err)
	}
	answer := firstAttempt.result
	if answer.Replayed {
		answer = secondAttempt.result
	}
	if firstAttempt.result.Answer.ID != secondAttempt.result.Answer.ID || firstAttempt.result.Schedule.Version != secondAttempt.result.Schedule.Version || firstAttempt.result.Replayed == secondAttempt.result.Replayed {
		t.Fatalf("concurrent same-key answers diverged: first=%+v second=%+v", firstAttempt.result, secondAttempt.result)
	}
	if answer.Answer.ScorerVersion != reviewapp.NewDeterministicScorer().Version() || answer.Answer.Score.SchemaVersion != domain.ScoreSchemaVersionV1 || len(answer.Answer.Score.Evidence) != len(evidence) || answer.Answer.Score.Evidence[0] != evidence[0] || answer.Answer.Score.Coverage.Value != 1 {
		t.Fatalf("server scorer=%q score=%+v does not reflect the approved card evidence", answer.Answer.ScorerVersion, answer.Answer.Score)
	}
	replay, err := service.SubmitAnswer(ctx, answerCommand)
	if err != nil || !replay.Replayed || replay.Schedule.Version != answer.Schedule.Version || replay.Answer.ID != answer.Answer.ID || replay.Answer.ScorerVersion != answer.Answer.ScorerVersion {
		t.Fatalf("answer replay=%+v err=%v original=%+v", replay, err, answer)
	}
	var answerCount int
	var scheduleVersion int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.review_answer WHERE workspace_id=$1 AND idempotency_key='answer-submit-1'`, string(workspaceID)).Scan(&answerCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT version FROM learning.review_schedule WHERE workspace_id=$1 AND card_id=$2`, string(workspaceID), string(cardResult.Value.ID)).Scan(&scheduleVersion); err != nil {
		t.Fatal(err)
	}
	if answerCount != 1 || scheduleVersion != 2 {
		t.Fatalf("answer_count=%d schedule_version=%d", answerCount, scheduleVersion)
	}
	if _, err := pool.Exec(ctx, `UPDATE learning.review_answer SET user_answer=$3 WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(answer.Answer.ID), strings.Repeat("x", domain.MaxUserAnswerBytes+1)); reviewPostgresErrorCode(err) != "55000" {
		t.Fatalf("review answer mutation error=%v", err)
	}
	staleAnswer := answer.Answer
	staleAnswer.ID = foundation.ID("71000000-0000-4000-8000-000000000009")
	staleAnswer.IdempotencyKey = "answer-stale-1"
	if _, err := repository.SubmitAnswer(ctx, reviewapp.SubmitAnswerRecord{Answer: staleAnswer, ExpectedCardVersion: cardResult.Value.Version, ExpectedCardFingerprint: cardResult.Value.Fingerprint, ExpectedScheduleVersion: 1, ScheduleDecision: domain.ScheduleDecision{DueAt: now.Add(24 * time.Hour), IntervalDays: 1, Stability: 1, Difficulty: 0.5, SchedulerVersion: domain.SchedulerVersionFSRSV1}, RequestHash: reviewHash("stale-answer")}); err == nil {
		t.Fatal("stale schedule version was accepted")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.review_answer WHERE workspace_id=$1 AND idempotency_key='answer-stale-1'`, string(workspaceID)).Scan(&answerCount); err != nil {
		t.Fatal(err)
	}
	if answerCount != 0 {
		t.Fatalf("stale answer escaped transaction rollback: count=%d", answerCount)
	}
	completeCommand := reviewapp.CompleteSessionCommand{WorkspaceID: workspaceID, SessionID: sessionResult.Value.ID, IdempotencyKey: "session-complete-1"}
	completed, err := service.CompleteSession(ctx, completeCommand)
	if err != nil || completed.Value.Status != domain.SessionStatusCompleted || completed.Value.EndedAt == nil || completed.Replayed {
		t.Fatalf("complete=%+v err=%v", completed, err)
	}
	completedReplay, err := service.CompleteSession(ctx, completeCommand)
	if err != nil || !completedReplay.Replayed || completedReplay.Value.ID != completed.Value.ID || completedReplay.Value.Status != completed.Value.Status || completedReplay.Value.EndedAt == nil || !completedReplay.Value.EndedAt.Equal(*completed.Value.EndedAt) {
		t.Fatalf("complete replay=%+v err=%v original=%+v", completedReplay, err, completed)
	}
	if _, err := service.CompleteSession(ctx, reviewapp.CompleteSessionCommand{WorkspaceID: workspaceID, SessionID: sessionResult.Value.ID, Cancelled: true, IdempotencyKey: completeCommand.IdempotencyKey}); reviewPersistenceErrorCode(err) != domain.ErrorCodeAnswerIdempotencyConflict {
		t.Fatalf("same completion key accepted another payload: %v", err)
	}
	var receiptType string
	var receiptCount int
	if err := pool.QueryRow(ctx, `SELECT command_type,count(*) OVER () FROM learning.review_command WHERE workspace_id=$1 AND idempotency_key=$2`, string(workspaceID), completeCommand.IdempotencyKey).Scan(&receiptType, &receiptCount); err != nil {
		t.Fatal(err)
	}
	if receiptType != "COMPLETE_SESSION" || receiptCount != 1 {
		t.Fatalf("complete receipt type=%q count=%d", receiptType, receiptCount)
	}
	responseLossSession, err := service.StartSession(ctx, reviewapp.StartSessionCommand{WorkspaceID: workspaceID, DeckID: &deckResult.Value.ID, SessionType: domain.SessionTypeReview, IdempotencyKey: "session-start-response-loss"})
	if err != nil {
		t.Fatal(err)
	}
	responseLossRepository, err := reviewResponseLossRepository(t, variant, repository, platform, errors.New("simulated completion response loss"))
	if err != nil {
		t.Fatal(err)
	}
	responseLoss, err := responseLossRepository.CompleteSession(ctx, reviewapp.CompleteSessionRecord{WorkspaceID: workspaceID, SessionID: responseLossSession.Value.ID, Status: domain.SessionStatusCompleted, IdempotencyKey: "session-complete-response-loss", RequestHash: reviewHash("session-complete-response-loss"), At: now})
	if err != nil || !responseLoss.Replayed || responseLoss.Value.Status != domain.SessionStatusCompleted || responseLoss.Value.EndedAt == nil {
		t.Fatalf("completion response-loss recovery=%+v err=%v", responseLoss, err)
	}
	persistedResponseLoss, err := repository.GetSession(ctx, workspaceID, responseLossSession.Value.ID)
	if err != nil || persistedResponseLoss.Status != domain.SessionStatusCompleted || persistedResponseLoss.EndedAt == nil {
		t.Fatalf("response-loss completion was not durable: session=%+v err=%v", persistedResponseLoss, err)
	}
	reset, err := service.ChangeDeckSchedule(ctx, reviewapp.DeckScheduleCommand{WorkspaceID: workspaceID, DeckID: deckResult.Value.ID, ExpectedVersion: 1, IdempotencyKey: "deck-reset-1"}, reviewapp.DeckScheduleReset)
	if err != nil || reset.Value.Status != domain.DeckStatusActive || reset.Value.Version != 2 {
		t.Fatalf("reset=%+v err=%v", reset, err)
	}
	var resetDifficulty float64
	var resetDueAt time.Time
	if err := pool.QueryRow(ctx, `SELECT version,difficulty,due_at FROM learning.review_schedule WHERE workspace_id=$1 AND card_id=$2`, string(workspaceID), string(cardResult.Value.ID)).Scan(&scheduleVersion, &resetDifficulty, &resetDueAt); err != nil {
		t.Fatal(err)
	}
	if scheduleVersion != 3 || resetDifficulty != cardResult.Value.Difficulty || !resetDueAt.Equal(now) {
		t.Fatalf("reset schedule version=%d difficulty=%f due_at=%s", scheduleVersion, resetDifficulty, resetDueAt)
	}
	closedReplay, err := service.SubmitAnswer(ctx, answerCommand)
	if err != nil || !closedReplay.Replayed || closedReplay.Answer.ID != answer.Answer.ID || closedReplay.Schedule.Version != answer.Schedule.Version {
		t.Fatalf("closed answer replay=%+v err=%v original=%+v", closedReplay, err, answer)
	}
	if err := pool.QueryRow(ctx, `SELECT version FROM learning.review_schedule WHERE workspace_id=$1 AND card_id=$2`, string(workspaceID), string(cardResult.Value.ID)).Scan(&scheduleVersion); err != nil {
		t.Fatal(err)
	}
	if scheduleVersion != 3 {
		t.Fatalf("answer replay changed current schedule version: %d", scheduleVersion)
	}
	answerCommand.UserAnswer = "另一份答案"
	if _, err := service.SubmitAnswer(ctx, answerCommand); err == nil {
		t.Fatal("same answer idempotency key accepted a different request")
	}
	if _, err := pool.Exec(ctx, `UPDATE learning.review_command SET response=jsonb_set(response,'{id}',to_jsonb($3::text)) WHERE workspace_id=$1 AND idempotency_key=$2`, string(workspaceID), "deck-create-1", "71000000-0000-4000-8000-000000000099"); reviewPostgresErrorCode(err) != "55000" {
		t.Fatalf("review command mutation error=%v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM learning.review_command WHERE workspace_id=$1 AND idempotency_key=$2`, string(workspaceID), "deck-create-1"); reviewPostgresErrorCode(err) != "55000" {
		t.Fatalf("review command deletion error=%v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE learning.review_answer SET schedule_snapshot='{}'::jsonb WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(answer.Answer.ID)); reviewPostgresErrorCode(err) != "55000" {
		t.Fatalf("review answer snapshot mutation error=%v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM learning.review_answer WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(answer.Answer.ID)); reviewPostgresErrorCode(err) != "55000" {
		t.Fatalf("review answer deletion error=%v", err)
	}
	if deckReplay, err := service.CreateDeck(ctx, reviewapp.CreateDeckCommand{WorkspaceID: workspaceID, Name: "M8 Review", DailyLimit: 20, IdempotencyKey: "deck-create-1"}); err != nil || !deckReplay.Replayed || deckReplay.Value.ID != deckResult.Value.ID {
		t.Fatalf("append-only deck replay=%+v err=%v", deckReplay, err)
	}
	answerCommand.UserAnswer = "同一事务确保原子性，重复提交不得推进两次"
	if replay, err := service.SubmitAnswer(ctx, answerCommand); err != nil || !replay.Replayed || replay.Answer.ID != answer.Answer.ID {
		t.Fatalf("append-only answer replay=%+v err=%v", replay, err)
	}
}

func TestReviewRepositoryRejectsInterviewSessionOperations(t *testing.T) {
	runReviewIntegrationVariants(t, testReviewRepositoryRejectsInterviewSessionOperations)
}

func testReviewRepositoryRejectsInterviewSessionOperations(t *testing.T, variant reviewIntegrationVariant) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := createReviewDraftFixtureForVariant(t, ctx, variant, "review-interview-boundary")
	repository := fixture.repository
	pool := fixture.pool
	approved, err := fixture.service.ApproveCard(ctx, reviewapp.CardDecisionCommand{
		WorkspaceID: fixture.workspaceID, CardID: fixture.card.ID, ExpectedVersion: fixture.card.Version,
		IdempotencyKey: "review-interview-boundary-approve",
	})
	if err != nil {
		t.Fatal(err)
	}
	interviewSessionID := foundation.ID("71000000-0000-4000-8000-000000000050")
	if _, err := pool.Exec(ctx, `INSERT INTO learning.review_session(id,workspace_id,deck_id,session_type,status,config,idempotency_key,request_hash,started_at)
		VALUES($1,$2,NULL,'INTERVIEW','ACTIVE','{"schema_version":"interview/v1"}','review-interview-boundary-shell',$3,$4)`,
		string(interviewSessionID), string(fixture.workspaceID), reviewHash("review-interview-boundary-shell"), fixture.now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning.interview_session(session_id,workspace_id,domain_schema_version,version,follow_up_count,created_at,updated_at)
		VALUES($1,$2,'interview/v1',1,0,$3,$3)`, string(interviewSessionID), string(fixture.workspaceID), fixture.now); err != nil {
		t.Fatal(err)
	}
	dimension := domain.ScoreDimension{Value: 1, Rationale: "server-scored review answer"}
	cardID := approved.Value.ID
	answer := domain.Answer{
		ID:             foundation.ID("71000000-0000-4000-8000-000000000051"),
		WorkspaceID:    fixture.workspaceID,
		SessionID:      interviewSessionID,
		CardID:         &cardID,
		QuestionRef:    "interview-shell:1",
		IdempotencyKey: "review-interview-boundary-answer",
		UserAnswer:     "must not advance FSRS",
		Rating:         domain.RatingGood,
		ScorerVersion:  "review-test/v1",
		Score: domain.Score{
			SchemaVersion: domain.ScoreSchemaVersionV1,
			Correctness:   dimension, Coverage: dimension, Boundaries: dimension, Clarity: dimension, Confidence: dimension,
			Evidence: approved.Value.Evidence,
		},
		Feedback:  map[string]any{"schema_version": domain.ScoreSchemaVersionV1},
		CreatedAt: fixture.now,
	}
	_, err = repository.SubmitAnswer(ctx, reviewapp.SubmitAnswerRecord{
		Answer: answer, ExpectedCardVersion: approved.Value.Version, ExpectedCardFingerprint: approved.Value.Fingerprint, ExpectedScheduleVersion: 1,
		ScheduleDecision: domain.ScheduleDecision{
			DueAt: fixture.now.Add(24 * time.Hour), IntervalDays: 1, Stability: 1, Difficulty: 0.5,
			SchedulerVersion: domain.SchedulerVersionFSRSV1,
		},
		RequestHash: reviewHash("review-interview-boundary-answer"),
	})
	if reviewPersistenceErrorCode(err) != domain.ErrorCodeSessionTypeConflict {
		t.Fatalf("interview shell advanced Review schedule: %v", err)
	}
	_, err = repository.CompleteSession(ctx, reviewapp.CompleteSessionRecord{
		WorkspaceID: fixture.workspaceID, SessionID: interviewSessionID, Status: domain.SessionStatusCompleted,
		IdempotencyKey: "review-interview-boundary-complete", RequestHash: reviewHash("review-interview-boundary-complete"), At: fixture.now,
	})
	if reviewPersistenceErrorCode(err) != domain.ErrorCodeSessionTypeConflict {
		t.Fatalf("Review completion closed interview shell: %v", err)
	}
	var sessionStatus string
	var answerCount int
	var scheduleVersion int64
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT status FROM learning.review_session WHERE workspace_id=$1 AND id=$2),
		(SELECT count(*) FROM learning.review_answer WHERE workspace_id=$1 AND session_id=$2),
		(SELECT version FROM learning.review_schedule WHERE workspace_id=$1 AND card_id=$3)`,
		string(fixture.workspaceID), string(interviewSessionID), string(cardID)).Scan(&sessionStatus, &answerCount, &scheduleVersion); err != nil {
		t.Fatal(err)
	}
	if sessionStatus != string(domain.SessionStatusActive) || answerCount != 0 || scheduleVersion != 1 {
		t.Fatalf("interview boundary changed state: session=%s answers=%d schedule_version=%d", sessionStatus, answerCount, scheduleVersion)
	}
}

func TestReviewRepositoryRejectsAnswerAfterCardEditReapproveABA(t *testing.T) {
	runReviewIntegrationVariants(t, testReviewRepositoryRejectsAnswerAfterCardEditReapproveABA)
}

func testReviewRepositoryRejectsAnswerAfterCardEditReapproveABA(t *testing.T, variant reviewIntegrationVariant) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := createReviewDraftFixtureForVariant(t, ctx, variant, "answer-card-aba")
	pool := fixture.pool

	approved, err := fixture.service.ApproveCard(ctx, reviewapp.CardDecisionCommand{
		WorkspaceID: fixture.workspaceID, CardID: fixture.card.ID, ExpectedVersion: fixture.card.Version,
		IdempotencyKey: "answer-card-aba-approve-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := fixture.service.StartSession(ctx, reviewapp.StartSessionCommand{
		WorkspaceID: fixture.workspaceID, DeckID: &approved.Value.DeckID,
		SessionType: domain.SessionTypeReview, IdempotencyKey: "answer-card-aba-session",
	})
	if err != nil {
		t.Fatal(err)
	}
	repository := fixture.repository
	fsrs, err := platformscheduler.NewFSRSAdapter()
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan reviewapp.SubmitAnswerRecord, 1)
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	blocking := &answerABABlockingRepository{reviewIntegrationRepository: repository, started: started, release: release}
	answerService, err := reviewapp.NewService(blocking, repository, reviewapp.NewDeterministicScorer(), fsrs, reviewIntegrationQuestionRefKey, foundation.NewUUIDGenerator(nil), foundation.FixedClock{Value: fixture.now})
	if err != nil {
		t.Fatal(err)
	}
	questionRef := reviewDueQuestionRef(t, ctx, answerService, fixture.workspaceID, session.Value.ID, approved.Value.DeckID, approved.Value.ID)

	answerResult := make(chan error, 1)
	go func() {
		_, submitErr := answerService.SubmitAnswer(ctx, reviewapp.SubmitAnswerCommand{
			WorkspaceID: fixture.workspaceID, SessionID: session.Value.ID, CardID: approved.Value.ID,
			QuestionRef: questionRef, UserAnswer: "the scored card version must remain current",
			Rating: domain.RatingGood, IdempotencyKey: "answer-card-aba-submit",
		})
		answerResult <- submitErr
	}()

	var staleRecord reviewapp.SubmitAnswerRecord
	select {
	case staleRecord = <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if staleRecord.ExpectedCardVersion != approved.Value.Version || staleRecord.ExpectedScheduleVersion != 1 {
		t.Fatalf("stale answer versions card=%d schedule=%d", staleRecord.ExpectedCardVersion, staleRecord.ExpectedScheduleVersion)
	}
	claimID := *approved.Value.ClaimID
	edited, err := fixture.service.EditCard(ctx, reviewapp.EditCardCommand{
		WorkspaceID: fixture.workspaceID, CardID: approved.Value.ID, ExpectedVersion: approved.Value.Version,
		ClaimID: claimID, Question: approved.Value.Question + " (edited)", AnswerPoints: approved.Value.AnswerPoints,
		Evidence: approved.Value.Evidence, CardType: approved.Value.CardType, Difficulty: approved.Value.Difficulty,
		ModelVersion: approved.Value.ModelVersion, IdempotencyKey: "answer-card-aba-edit",
	})
	if err != nil {
		t.Fatal(err)
	}
	reapproved, err := fixture.service.ApproveCard(ctx, reviewapp.CardDecisionCommand{
		WorkspaceID: fixture.workspaceID, CardID: edited.Value.ID, ExpectedVersion: edited.Value.Version,
		IdempotencyKey: "answer-card-aba-approve-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	released = true
	if err := <-answerResult; reviewPersistenceErrorCode(err) != domain.ErrorCodeCardStateConflict {
		t.Fatalf("stale scored answer error=%v", err)
	}

	var answerCount int
	var cardStatus string
	var cardVersion, scheduleVersion int64
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM learning.review_answer WHERE workspace_id=$1 AND idempotency_key='answer-card-aba-submit'),
		card.status,card.version,schedule.version
		FROM learning.review_card card
		JOIN learning.review_schedule schedule ON schedule.workspace_id=card.workspace_id AND schedule.card_id=card.id
		WHERE card.workspace_id=$1 AND card.id=$2`, string(fixture.workspaceID), string(approved.Value.ID)).Scan(&answerCount, &cardStatus, &cardVersion, &scheduleVersion); err != nil {
		t.Fatal(err)
	}
	if answerCount != 0 || cardStatus != string(domain.CardStatusApproved) || cardVersion != reapproved.Value.Version || scheduleVersion != 1 {
		t.Fatalf("stale scored answer changed state answers=%d card=%s/v%d schedule=v%d", answerCount, cardStatus, cardVersion, scheduleVersion)
	}
}

func TestReviewRepositoryConcurrentScheduleCommandsRemainAtomic(t *testing.T) {
	runReviewIntegrationVariants(t, testReviewRepositoryConcurrentScheduleCommandsRemainAtomic)
}

func testReviewRepositoryConcurrentScheduleCommandsRemainAtomic(t *testing.T, variant reviewIntegrationVariant) {
	ctx := context.Background()
	repository, _, pool := variant.open(t)
	seedReviewEvidence(t, ctx, pool)

	var err error
	fsrs, err := platformscheduler.NewFSRSAdapter()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	service, err := reviewapp.NewService(repository, repository, reviewapp.NewDeterministicScorer(), fsrs, reviewIntegrationQuestionRefKey, foundation.NewUUIDGenerator(nil), foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := foundation.ID("71000000-0000-4000-8000-000000000001")
	claimID := foundation.ID("71000000-0000-4000-8000-000000000007")
	evidence := []domain.EvidenceBinding{{
		SchemaVersion: domain.EvidenceSchemaVersionV1, ClaimID: claimID,
		SourceVersionID: foundation.ID("71000000-0000-4000-8000-000000000004"),
		SourceSpanID:    foundation.ID("71000000-0000-4000-8000-000000000006"),
		EvidenceHash:    reviewHash("claim-source"),
	}}
	deck, err := service.CreateDeck(ctx, reviewapp.CreateDeckCommand{WorkspaceID: workspaceID, Name: "Concurrent Review", DailyLimit: 20, IdempotencyKey: "concurrent-deck-create"})
	if err != nil {
		t.Fatal(err)
	}
	card, err := service.CreateCard(ctx, reviewapp.CreateCardCommand{
		WorkspaceID: workspaceID, DeckID: deck.Value.ID, ClaimID: claimID,
		Question: "How are concurrent schedules serialized?", AnswerPoints: []string{"workspace lock and schedule CAS"},
		Evidence: evidence, CardType: domain.CardTypeShortAnswer, Difficulty: 0.5, ModelVersion: "manual", IdempotencyKey: "concurrent-card-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApproveCard(ctx, reviewapp.CardDecisionCommand{WorkspaceID: workspaceID, CardID: card.Value.ID, ExpectedVersion: 1, IdempotencyKey: "concurrent-card-approve"}); err != nil {
		t.Fatal(err)
	}
	session, err := service.StartSession(ctx, reviewapp.StartSessionCommand{WorkspaceID: workspaceID, DeckID: &deck.Value.ID, SessionType: domain.SessionTypeReview, IdempotencyKey: "concurrent-session-start"})
	if err != nil {
		t.Fatal(err)
	}
	questionRef := reviewDueQuestionRef(t, ctx, service, workspaceID, session.Value.ID, deck.Value.ID, card.Value.ID)

	type concurrentResult struct{ err error }
	start := make(chan struct{})
	results := make(chan concurrentResult, 2)
	for index := range 2 {
		index := index
		go func() {
			<-start
			_, submitErr := service.SubmitAnswer(ctx, reviewapp.SubmitAnswerCommand{
				WorkspaceID: workspaceID, SessionID: session.Value.ID, CardID: card.Value.ID,
				QuestionRef: questionRef, UserAnswer: "workspace lock and schedule CAS", Rating: domain.RatingGood,
				IdempotencyKey: "concurrent-answer-" + string(rune('1'+index)),
			})
			results <- concurrentResult{err: submitErr}
		}()
	}
	close(start)
	first, second := <-results, <-results
	successes, conflicts := 0, 0
	for _, result := range []concurrentResult{first, second} {
		switch reviewPersistenceErrorCode(result.err) {
		case "":
			if result.err != nil {
				t.Fatalf("unexpected concurrent answer error: %v", result.err)
			}
			successes++
		case domain.ErrorCodeScheduleConflict, domain.ErrorCodeQuestionStale:
			conflicts++
		default:
			t.Fatalf("unexpected concurrent answer error: %v", result.err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("different-key concurrent answers successes=%d conflicts=%d", successes, conflicts)
	}

	reset, err := service.ChangeDeckSchedule(ctx, reviewapp.DeckScheduleCommand{WorkspaceID: workspaceID, DeckID: deck.Value.ID, ExpectedVersion: 1, IdempotencyKey: "concurrent-reset-before-race"}, reviewapp.DeckScheduleReset)
	if err != nil || reset.Value.Version != 2 {
		t.Fatalf("prepare concurrent reset=%+v err=%v", reset, err)
	}
	questionRef = reviewDueQuestionRef(t, ctx, service, workspaceID, session.Value.ID, deck.Value.ID, card.Value.ID)
	start = make(chan struct{})
	resetResult := make(chan error, 1)
	submitResult := make(chan error, 1)
	go func() {
		<-start
		_, commandErr := service.ChangeDeckSchedule(ctx, reviewapp.DeckScheduleCommand{WorkspaceID: workspaceID, DeckID: deck.Value.ID, ExpectedVersion: 2, IdempotencyKey: "concurrent-reset-race"}, reviewapp.DeckScheduleReset)
		resetResult <- commandErr
	}()
	go func() {
		<-start
		_, commandErr := service.SubmitAnswer(ctx, reviewapp.SubmitAnswerCommand{
			WorkspaceID: workspaceID, SessionID: session.Value.ID, CardID: card.Value.ID,
			QuestionRef: questionRef, UserAnswer: "workspace lock and schedule CAS", Rating: domain.RatingGood,
			IdempotencyKey: "concurrent-answer-reset-race",
		})
		submitResult <- commandErr
	}()
	close(start)
	if err := <-resetResult; err != nil {
		t.Fatalf("concurrent reset failed: %v", err)
	}
	submitErr := <-submitResult
	if code := reviewPersistenceErrorCode(submitErr); submitErr != nil && code != domain.ErrorCodeScheduleConflict && code != domain.ErrorCodeQuestionStale {
		t.Fatalf("concurrent reset/submit error=%v", submitErr)
	}

	var answerCount int
	var scheduleVersion int64
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM learning.review_answer WHERE workspace_id=$1 AND card_id=$2),
		(SELECT version FROM learning.review_schedule WHERE workspace_id=$1 AND card_id=$2)`, string(workspaceID), string(card.Value.ID)).Scan(&answerCount, &scheduleVersion); err != nil {
		t.Fatal(err)
	}
	expectedAnswers := 1
	if submitErr == nil {
		expectedAnswers++
	}
	if answerCount != expectedAnswers || scheduleVersion != int64(3+expectedAnswers) {
		t.Fatalf("reset/submit left partial state: answers=%d schedule_version=%d submit_err=%v", answerCount, scheduleVersion, submitErr)
	}
}

func TestReviewRepositoryApprovalAndQuarantineSerializeByWorkspace(t *testing.T) {
	runReviewIntegrationVariants(t, testReviewRepositoryApprovalAndQuarantineSerializeByWorkspace)
}

func testReviewRepositoryApprovalAndQuarantineSerializeByWorkspace(t *testing.T, variant reviewIntegrationVariant) {
	t.Run("approval commits before quarantine invalidates the card", func(t *testing.T) {
		ctx := context.Background()
		fixture := createReviewDraftFixtureForVariant(t, ctx, variant, "approval-first")
		pool := fixture.pool
		if _, err := pool.Exec(ctx, `UPDATE core.source_version SET security_status='quarantined' WHERE workspace_id=$1 AND id='71000000-0000-4000-8000-000000000004'`, string(fixture.workspaceID)); reviewPostgresErrorCode(err) != "55000" {
			t.Fatalf("source version mutation error=%v", err)
		}

		approvalTx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = approvalTx.Rollback(context.Background()) }()
		var lockedWorkspace string
		if err := approvalTx.QueryRow(ctx, `SELECT id::text FROM core.workspace WHERE id=$1 FOR UPDATE`, string(fixture.workspaceID)).Scan(&lockedWorkspace); err != nil {
			t.Fatal(err)
		}
		if _, err := approvalTx.Exec(ctx, `UPDATE learning.review_card SET status='APPROVED',version=2,updated_at=$3 WHERE workspace_id=$1 AND id=$2 AND status='DRAFT' AND version=1`, string(fixture.workspaceID), string(fixture.card.ID), fixture.now); err != nil {
			t.Fatal(err)
		}
		if _, err := approvalTx.Exec(ctx, `INSERT INTO learning.review_schedule(card_id,workspace_id,due_at,interval_days,stability,difficulty,last_reviewed_at,scheduler_version,paused,version) VALUES($1,$2,$3,0,0,$4,NULL,'fsrs/v1',false,1)`, string(fixture.card.ID), string(fixture.workspaceID), fixture.now, fixture.card.Difficulty); err != nil {
			t.Fatal(err)
		}

		started := make(chan struct{})
		quarantineResult := make(chan error, 1)
		go func() {
			close(started)
			_, insertErr := pool.Exec(ctx, reviewQuarantineAttemptSQL, "71000000-0000-4000-8000-000000000031", string(fixture.workspaceID), "approval-first-quarantine", fixture.now.Add(time.Second))
			quarantineResult <- insertErr
		}()
		<-started
		if err := approvalTx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-quarantineResult; err != nil {
			t.Fatal(err)
		}
		assertInvalidatedReviewCard(t, ctx, pool, fixture.workspaceID, fixture.card.ID)
	})

	t.Run("quarantine commits before approval is rechecked", func(t *testing.T) {
		ctx := context.Background()
		fixture := createReviewDraftFixtureForVariant(t, ctx, variant, "quarantine-first")
		pool := fixture.pool

		quarantineTx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = quarantineTx.Rollback(context.Background()) }()
		var lockedWorkspace string
		if err := quarantineTx.QueryRow(ctx, `SELECT id::text FROM core.workspace WHERE id=$1 FOR UPDATE`, string(fixture.workspaceID)).Scan(&lockedWorkspace); err != nil {
			t.Fatal(err)
		}
		if _, err := quarantineTx.Exec(ctx, reviewQuarantineAttemptSQL, "71000000-0000-4000-8000-000000000032", string(fixture.workspaceID), "quarantine-first-attempt", fixture.now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}

		started := make(chan struct{})
		approvalResult := make(chan error, 1)
		go func() {
			close(started)
			_, approveErr := fixture.service.ApproveCard(ctx, reviewapp.CardDecisionCommand{
				WorkspaceID: fixture.workspaceID, CardID: fixture.card.ID, ExpectedVersion: fixture.card.Version,
				IdempotencyKey: "quarantine-first-approve",
			})
			approvalResult <- approveErr
		}()
		<-started
		if err := quarantineTx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-approvalResult; reviewPersistenceErrorCode(err) != domain.ErrorCodeEvidenceStale {
			t.Fatalf("approval after quarantine error=%v", err)
		}
		var status string
		var scheduleCount int
		if err := pool.QueryRow(ctx, `SELECT status,(SELECT count(*) FROM learning.review_schedule WHERE workspace_id=$1 AND card_id=$2) FROM learning.review_card WHERE workspace_id=$1 AND id=$2`, string(fixture.workspaceID), string(fixture.card.ID)).Scan(&status, &scheduleCount); err != nil {
			t.Fatal(err)
		}
		if status != string(domain.CardStatusDraft) || scheduleCount != 0 {
			t.Fatalf("failed approval left state status=%s schedules=%d", status, scheduleCount)
		}
	})
}

func TestReviewRepositoryHighConflictInvalidatesOrBlocksApproval(t *testing.T) {
	runReviewIntegrationVariants(t, testReviewRepositoryHighConflictInvalidatesOrBlocksApproval)
}

func testReviewRepositoryHighConflictInvalidatesOrBlocksApproval(t *testing.T, variant reviewIntegrationVariant) {
	t.Run("approved card is durably invalidated", func(t *testing.T) {
		ctx := context.Background()
		fixture := createReviewDraftFixtureForVariant(t, ctx, variant, "conflict-after-approval")
		pool := fixture.pool
		if _, err := fixture.service.ApproveCard(ctx, reviewapp.CardDecisionCommand{
			WorkspaceID: fixture.workspaceID, CardID: fixture.card.ID, ExpectedVersion: fixture.card.Version,
			IdempotencyKey: "conflict-after-approval-approve",
		}); err != nil {
			t.Fatal(err)
		}
		conflictID := createReviewHighConflict(t, ctx, pool, fixture.workspaceID, fixture.now, "conflict-after-approval")
		var status, reason string
		var scheduleCount int
		if err := pool.QueryRow(ctx, `SELECT status,invalidation_reason,(SELECT count(*) FROM learning.review_schedule WHERE workspace_id=$1 AND card_id=$2) FROM learning.review_card WHERE workspace_id=$1 AND id=$2`, string(fixture.workspaceID), string(fixture.card.ID)).Scan(&status, &reason, &scheduleCount); err != nil {
			t.Fatal(err)
		}
		if status != string(domain.CardStatusInvalidated) || reason != "HIGH_SEVERITY_CONFLICT:"+conflictID || scheduleCount != 0 {
			t.Fatalf("high conflict state status=%s reason=%s schedules=%d", status, reason, scheduleCount)
		}
	})

	t.Run("existing conflict blocks later approval", func(t *testing.T) {
		ctx := context.Background()
		fixture := createReviewDraftFixtureForVariant(t, ctx, variant, "conflict-before-approval")
		pool := fixture.pool
		createReviewHighConflict(t, ctx, pool, fixture.workspaceID, fixture.now, "conflict-before-approval")
		if _, err := fixture.service.ApproveCard(ctx, reviewapp.CardDecisionCommand{
			WorkspaceID: fixture.workspaceID, CardID: fixture.card.ID, ExpectedVersion: fixture.card.Version,
			IdempotencyKey: "conflict-before-approval-approve",
		}); reviewPersistenceErrorCode(err) != domain.ErrorCodeEvidenceStale {
			t.Fatalf("approval with unresolved high conflict error=%v", err)
		}
		var status string
		var scheduleCount int
		if err := pool.QueryRow(ctx, `SELECT status,(SELECT count(*) FROM learning.review_schedule WHERE workspace_id=$1 AND card_id=$2) FROM learning.review_card WHERE workspace_id=$1 AND id=$2`, string(fixture.workspaceID), string(fixture.card.ID)).Scan(&status, &scheduleCount); err != nil {
			t.Fatal(err)
		}
		if status != string(domain.CardStatusDraft) || scheduleCount != 0 {
			t.Fatalf("blocked approval left state status=%s schedules=%d", status, scheduleCount)
		}
	})
}

func TestReviewRepositoryPostgreSQLTODO9Gate(t *testing.T) {
	runReviewIntegrationVariants(t, testReviewRepositoryPostgreSQLTODO9Gate)
}

func testReviewRepositoryPostgreSQLTODO9Gate(t *testing.T, variant reviewIntegrationVariant) {
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	repository, _, pool := variant.open(t)
	seedReviewEvidence(t, ctx, pool)

	workspaceID := foundation.ID("71000000-0000-4000-8000-000000000001")
	claimID := foundation.ID("71000000-0000-4000-8000-000000000007")
	sourceVersionID := foundation.ID("71000000-0000-4000-8000-000000000004")
	sourceSpanID := foundation.ID("71000000-0000-4000-8000-000000000006")

	fsrs, err := platformscheduler.NewFSRSAdapter()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	service, err := reviewapp.NewService(repository, repository, reviewapp.NewDeterministicScorer(), fsrs, reviewIntegrationQuestionRefKey, foundation.NewUUIDGenerator(nil), foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatal(err)
	}
	deck, err := service.CreateDeck(ctx, reviewapp.CreateDeckCommand{
		WorkspaceID: workspaceID, Name: "TODO9 bounded review", DailyLimit: 1000, IdempotencyKey: "todo9-deck-create",
	})
	if err != nil {
		t.Fatal(err)
	}

	// The first batch is also the cardinality used for the due and invalidation
	// plans. Keeping it in one statement makes the N+1 assertion meaningful.
	seedReviewApprovedBatch(t, ctx, pool, workspaceID, deck.Value.ID, claimID, sourceVersionID, sourceSpanID, "todo9-claim", 201, now)
	counter := installReviewStatementCounter(t, repository)
	counter.reset()
	due, err := repository.ListDue(ctx, workspaceID, &deck.Value.ID, now, 200)
	if err != nil || len(due) != 200 {
		t.Fatalf("bounded due query count=%d err=%v", len(due), err)
	}
	if counter != nil && counter.statements() != 1 {
		t.Fatalf("GORM due query statements=%d, want 1", counter.statements())
	}
	if counter != nil {
		t.Logf("GORM due query statements=%d", counter.statements())
	}

	assertReviewDuePlan(t, ctx, pool, workspaceID, deck.Value.ID, now)
	assertReviewInvalidationPlan(t, ctx, pool, workspaceID, claimID, sourceVersionID, sourceSpanID, now)

	invalidationCases := []struct {
		name          string
		claimID       *foundation.ID
		sourceVersion *foundation.ID
		sourceSpan    *foundation.ID
	}{
		{name: "claim", claimID: &claimID},
		{name: "source-version", sourceVersion: &sourceVersionID},
		{name: "claim-source-version", claimID: &claimID, sourceVersion: &sourceVersionID},
		{name: "source-span", sourceSpan: &sourceSpanID},
		{name: "claim-source-span", claimID: &claimID, sourceSpan: &sourceSpanID},
	}
	for index, invalidationCase := range invalidationCases {
		prefix := "todo9-" + invalidationCase.name
		if index > 0 {
			seedReviewApprovedBatch(t, ctx, pool, workspaceID, deck.Value.ID, claimID, sourceVersionID, sourceSpanID, prefix, 201, now)
		}
		counter.reset()
		first := reviewapp.InvalidateCardsRecord{
			WorkspaceID: workspaceID, ClaimID: invalidationCase.claimID, SourceVersionID: invalidationCase.sourceVersion,
			SourceSpanID: invalidationCase.sourceSpan, Reason: "TODO9_" + strings.ToUpper(strings.ReplaceAll(invalidationCase.name, "-", "_")),
			IdempotencyKey: prefix + "-1", RequestHash: reviewHash(prefix + "-request"), At: now.Add(time.Duration(index+1) * time.Second), BatchSize: reviewapp.MaxInvalidationBatchSize,
		}
		firstResult, err := repository.InvalidateCards(ctx, first)
		if err != nil || firstResult.InvalidatedCount != 200 || !firstResult.HasMore || firstResult.Replayed {
			t.Fatalf("%s first invalidation=%+v err=%v", invalidationCase.name, firstResult, err)
		}
		if counter != nil && counter.statements() > 5 {
			t.Fatalf("GORM %s invalidation statements=%d, want <=5", invalidationCase.name, counter.statements())
		}
		if counter != nil {
			t.Logf("GORM %s invalidation statements=%d", invalidationCase.name, counter.statements())
		}
		replay, err := repository.InvalidateCards(ctx, first)
		if err != nil || !replay.Replayed || replay.InvalidatedCount != 200 || !replay.HasMore {
			t.Fatalf("%s invalidation replay=%+v err=%v", invalidationCase.name, replay, err)
		}
		second := first
		second.IdempotencyKey = prefix + "-2"
		second.RequestHash = reviewHash(prefix + "-request-2")
		second.At = second.At.Add(time.Second)
		secondResult, err := repository.InvalidateCards(ctx, second)
		if err != nil || secondResult.InvalidatedCount != 1 || secondResult.HasMore || secondResult.Replayed {
			t.Fatalf("%s final invalidation=%+v err=%v", invalidationCase.name, secondResult, err)
		}
		assertReviewInvalidationBatchState(t, ctx, pool, workspaceID, prefix, 201)
	}

	assertReviewRowsCancellationAndPoolReuse(t, ctx, variant, repository, pool, workspaceID, deck.Value.ID)
	assertReviewSQLTxDoneClassification(t, ctx, variant, repository, workspaceID, claimID)
}

func seedReviewApprovedBatch(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID, deckID, claimID, sourceVersionID, sourceSpanID foundation.ID,
	prefix string,
	count int,
	now time.Time,
) {
	t.Helper()
	if count < 1 {
		t.Fatalf("Review batch count=%d, want positive", count)
	}
	query := `
WITH inserted AS (
    INSERT INTO learning.review_card(
        id,workspace_id,deck_id,claim_id,question,answer_points,evidence,card_type,difficulty,
        status,fingerprint,model_version,version,created_at,updated_at
    )
    SELECT gen_random_uuid(),$1::uuid,$2::uuid,$3::uuid,
           $4 || '-' || series::text,
           '["batch answer"]'::jsonb,
           jsonb_build_array(jsonb_build_object(
               'schema_version','review-evidence/v1',
               'claim_id',$3::text,
               'source_version_id',$5::text,
               'source_span_id',$6::text,
               'evidence_hash',$7::text
		   )),
		   'SHORT_ANSWER',0.5,'APPROVED',encode(sha256(convert_to($4 || '-' || series::text,'UTF8')),'hex'),'todo9-batch',1,$8,$8
    FROM generate_series(1,$9::integer) AS series
    RETURNING id,workspace_id
)
INSERT INTO learning.review_schedule(
    card_id,workspace_id,due_at,interval_days,stability,difficulty,last_reviewed_at,
    scheduler_version,paused,version
)
SELECT id,workspace_id,$8,0,0,0.5,NULL,'fsrs/v1',false,1
FROM inserted`
	tag, err := pool.Exec(ctx, query,
		string(workspaceID), string(deckID), string(claimID), prefix,
		string(sourceVersionID), string(sourceSpanID), reviewHash("claim-source"), now.UTC(), count,
	)
	if err != nil {
		t.Fatalf("seed Review approved batch prefix=%q count=%d: %v", prefix, count, err)
	}
	if tag.RowsAffected() != int64(count) {
		t.Fatalf("seed Review approved batch prefix=%q rows=%d, want %d", prefix, tag.RowsAffected(), count)
	}
}

func assertReviewDuePlan(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, deckID foundation.ID, now time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `ANALYZE learning.review_card, learning.review_schedule, learning.review_card_evidence_selector`); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan=off`); err != nil {
		t.Fatal(err)
	}
	plan := explainReviewQuery(t, ctx, tx, rankedDueCardsSQL+`
SELECT card_id
FROM due
WHERE deck_position <= GREATEST(daily_limit-answered_today,0)
ORDER BY due_at ASC,card_id ASC
LIMIT $4`, string(workspaceID), now.UTC(), string(deckID), reviewapp.MaxInvalidationBatchSize)
	if plan.ActualRows < float64(reviewapp.MaxInvalidationBatchSize) {
		t.Fatalf("Review due EXPLAIN actual rows=%f, want at least %d", plan.ActualRows, reviewapp.MaxInvalidationBatchSize)
	}
	if !reviewPlanUsesAnyIndex(plan, "idx_learning_review_due", "idx_learning_review_card_active_deck") {
		t.Fatalf("Review due EXPLAIN did not use a bounded due/card index: %+v", plan)
	}
	t.Logf("Review due EXPLAIN actual_rows=%.0f indexes=%v", plan.ActualRows, reviewPlanIndexNames(plan))
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
}

func assertReviewInvalidationPlan(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID, claimID, sourceVersionID, sourceSpanID foundation.ID,
	now time.Time,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `ANALYZE learning.review_card, learning.review_card_evidence_selector`); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan=off; SET LOCAL enable_nestloop=off`); err != nil {
		t.Fatal(err)
	}
	plan := explainReviewQuery(t, ctx, tx,
		invalidationStatementPrefix+invalidationBySourceVersionTargets+invalidationStatementSuffix,
		string(workspaceID), nil, string(sourceVersionID), nil,
		reviewapp.MaxInvalidationBatchSize+1, reviewapp.MaxInvalidationBatchSize,
		"TODO9_EXPLAIN", now.UTC(),
	)
	if !reviewPlanUsesAnyIndex(plan,
		"idx_learning_review_card_evidence_selector_lookup",
		"idx_learning_review_card_evidence_selector_claim_lookup",
	) {
		t.Fatalf("Review invalidation EXPLAIN did not use a selector lookup index: %+v", plan)
	}
	t.Logf("Review invalidation EXPLAIN actual_rows=%.0f indexes=%v", plan.ActualRows, reviewPlanIndexNames(plan))
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
}

func assertReviewInvalidationBatchState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID, prefix string, want int) {
	t.Helper()
	pattern := prefix + "-%"
	var total, invalidated, approved int
	if err := pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE status='INVALIDATED'),count(*) FILTER (WHERE status='APPROVED')
FROM learning.review_card
WHERE workspace_id=$1 AND question LIKE $2`, string(workspaceID), pattern).Scan(&total, &invalidated, &approved); err != nil {
		t.Fatal(err)
	}
	if total != want || invalidated != want || approved != 0 {
		t.Fatalf("Review invalidation cards prefix=%q total=%d invalidated=%d approved=%d, want %d/ %d/0", prefix, total, invalidated, approved, want, want)
	}

	var schedules, selectors int
	if err := pool.QueryRow(ctx, `SELECT
    (SELECT count(*)
       FROM learning.review_schedule schedule
       JOIN learning.review_card card ON card.workspace_id=schedule.workspace_id AND card.id=schedule.card_id
      WHERE card.workspace_id=$1 AND card.question LIKE $2),
    (SELECT count(*)
       FROM learning.review_card_evidence_selector selector
       JOIN learning.review_card card ON card.workspace_id=selector.workspace_id AND card.id=selector.card_id
      WHERE card.workspace_id=$1 AND card.question LIKE $2)`, string(workspaceID), pattern).Scan(&schedules, &selectors); err != nil {
		t.Fatal(err)
	}
	if schedules != 0 || selectors != 0 {
		t.Fatalf("Review invalidation projections prefix=%q schedules=%d selectors=%d, want 0/0", prefix, schedules, selectors)
	}

	var cardEvents, scheduleEvents, timelineEvents int
	if err := pool.QueryRow(ctx, `SELECT
    (SELECT count(*)
       FROM ops.server_event event
       JOIN learning.review_card card ON card.workspace_id=event.workspace_id AND event.resource_ref='review_card:'||card.id::text
      WHERE card.workspace_id=$1 AND card.question LIKE $2 AND event.event_type='review.card.updated'),
    (SELECT count(*)
       FROM ops.server_event event
       JOIN learning.review_card card ON card.workspace_id=event.workspace_id AND event.resource_ref='review_schedule:'||card.id::text
      WHERE card.workspace_id=$1 AND card.question LIKE $2 AND event.event_type='review.schedule.deleted'),
    (SELECT count(*)
       FROM ops.timeline_projection_outbox outbox
       JOIN learning.review_card card ON card.workspace_id=outbox.workspace_id AND outbox.aggregate_id=card.id
      WHERE card.workspace_id=$1 AND card.question LIKE $2 AND outbox.event_type='REVIEW_CARD_INVALIDATED' AND outbox.aggregate_type='REVIEW_CARD')`, string(workspaceID), pattern).Scan(&cardEvents, &scheduleEvents, &timelineEvents); err != nil {
		t.Fatal(err)
	}
	if cardEvents != want || scheduleEvents != want || timelineEvents != want {
		t.Fatalf("Review invalidation trigger projections prefix=%q card_sse=%d schedule_sse=%d timeline=%d, want %d/%d/%d", prefix, cardEvents, scheduleEvents, timelineEvents, want, want, want)
	}

	var healthCount int
	var healthStatus string
	if err := pool.QueryRow(ctx, `SELECT count(*),COALESCE(max(status),'')
FROM ops.health_issue
WHERE workspace_id=$1 AND type='REVIEW_INVALIDATED' AND target_type='CLAIM'
  AND target_id=(SELECT claim_id FROM learning.review_card WHERE workspace_id=$1 AND question LIKE $2 LIMIT 1)`, string(workspaceID), pattern).Scan(&healthCount, &healthStatus); err != nil {
		t.Fatal(err)
	}
	if healthCount != 1 || (healthStatus != "OPEN" && healthStatus != "REOPENED") {
		t.Fatalf("Review invalidation Health projection prefix=%q count=%d status=%q", prefix, healthCount, healthStatus)
	}

	var healthTimelineCount int
	if err := pool.QueryRow(ctx, `SELECT count(*)
FROM ops.timeline_projection_outbox outbox
JOIN ops.health_issue issue ON issue.workspace_id=outbox.workspace_id AND outbox.aggregate_id=issue.id
WHERE issue.workspace_id=$1 AND issue.type='REVIEW_INVALIDATED' AND outbox.aggregate_type='HEALTH_ISSUE'
  AND outbox.event_type='HEALTH_ISSUE_DETECTED'`, string(workspaceID)).Scan(&healthTimelineCount); err != nil {
		t.Fatal(err)
	}
	if healthTimelineCount < 1 {
		t.Fatalf("Review invalidation Health Timeline projection is missing")
	}
}

func assertReviewRowsCancellationAndPoolReuse(
	t *testing.T,
	ctx context.Context,
	variant reviewIntegrationVariant,
	repository reviewIntegrationRepository,
	pool *pgxpool.Pool,
	workspaceID, deckID foundation.ID,
) {
	t.Helper()
	blocker, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			blocker.Release()
		}
	}()
	blockerTx, err := blocker.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blockerTx.Rollback(context.Background()) }()
	if _, err := blockerTx.Exec(ctx, `LOCK TABLE learning.review_card IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	baselineAcquired := pool.Stat().AcquiredConns()

	cancelCause := errors.New("Review list canceled by caller")
	queryCtx, cancelQuery := context.WithCancelCause(ctx)
	cancelResult := make(chan error, 1)
	go func() {
		_, listErr := repository.ListCards(queryCtx, workspaceID, deckID, 2)
		cancelResult <- listErr
	}()
	waitForBlockedReviewList(t, ctx, pool)
	cancelQuery(cancelCause)
	select {
	case listErr := <-cancelResult:
		if !errors.Is(listErr, context.Canceled) {
			t.Fatalf("blocked Review list cancellation error=%v", listErr)
		}
		if variant.name == "gorm" && !errors.Is(listErr, cancelCause) {
			t.Fatalf("blocked GORM Review list did not preserve cancellation cause: %v", listErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocked Review list did not return after cancellation")
	}
	waitForReviewAcquiredConnections(t, pool, baselineAcquired)

	deadlineCtx, deadlineCancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer deadlineCancel()
	deadlineResult := make(chan error, 1)
	go func() {
		_, listErr := repository.ListCards(deadlineCtx, workspaceID, deckID, 2)
		deadlineResult <- listErr
	}()
	waitForBlockedReviewList(t, ctx, pool)
	select {
	case listErr := <-deadlineResult:
		if !errors.Is(listErr, context.DeadlineExceeded) {
			t.Fatalf("blocked Review list deadline error=%v", listErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocked Review list did not return after deadline")
	}
	waitForReviewAcquiredConnections(t, pool, baselineAcquired)

	if err := blockerTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	blocker.Release()
	released = true
	waitForReviewAcquiredConnections(t, pool, 0)
	var one int
	if err := pool.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil || one != 1 {
		t.Fatalf("Review shared pool was not reusable: one=%d err=%v", one, err)
	}
}

func waitForBlockedReviewList(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var blocked bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (
    SELECT 1
    FROM pg_stat_activity
    WHERE datname=current_database() AND pid<>pg_backend_pid()
      AND wait_event_type='Lock' AND state='active'
      AND query LIKE '%learning.review_card%'
)`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("blocked Review list was not observed in pg_stat_activity")
}

func waitForReviewAcquiredConnections(t *testing.T, pool *pgxpool.Pool, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for pool.Stat().AcquiredConns() != want && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if acquired := pool.Stat().AcquiredConns(); acquired != want {
		t.Fatalf("Review shared pool acquired connections=%d, want %d", acquired, want)
	}
}

func assertReviewSQLTxDoneClassification(
	t *testing.T,
	ctx context.Context,
	variant reviewIntegrationVariant,
	repository reviewIntegrationRepository,
	workspaceID, claimID foundation.ID,
) {
	t.Helper()
	if variant.name != "gorm" {
		return
	}
	gormRepository, ok := repository.(*GORMRepository)
	if !ok || gormRepository == nil {
		t.Fatal("Review GORM repository has unexpected type")
	}
	copyRepository := *gormRepository
	copyRepository.unitOfWork = reviewErrTxDoneUnitOfWork{}
	_, err := (&copyRepository).InvalidateCards(ctx, reviewapp.InvalidateCardsRecord{
		WorkspaceID:    workspaceID,
		ClaimID:        &claimID,
		Reason:         "TODO9_TX_DONE",
		IdempotencyKey: "todo9-tx-done",
		RequestHash:    reviewHash("todo9-tx-done"),
		At:             time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC),
		BatchSize:      1,
	})
	if err == nil {
		t.Fatal("GORM sql.ErrTxDone classification returned nil")
	}
	if !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("GORM sql.ErrTxDone classification lost sentinel: %v", err)
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		t.Fatalf("GORM sql.ErrTxDone classification is not a foundation error: %v", err)
	}
	if classified.Kind != foundation.ErrorDependencyUnavailable || classified.Code != domain.ErrorCodeDependencyUnavailable || !classified.Retryable {
		t.Fatalf("GORM sql.ErrTxDone classification=%+v", classified)
	}
}

type reviewErrTxDoneUnitOfWork struct{}

func (reviewErrTxDoneUnitOfWork) Within(context.Context, foundation.TransactionOptions, foundation.TransactionFunc) error {
	return sql.ErrTxDone
}

const reviewQuarantineAttemptSQL = `INSERT INTO ingestion.attempt(
	id,workspace_id,source_version_id,status,security_status,failure_stage,error_code,retryable,
	parser_id,parser_version,parser_config_hash,chunk_strategy_version,schema_version,
	idempotency_key,attempt_number,warnings,started_at,completed_at,version
) VALUES($1,$2,'71000000-0000-4000-8000-000000000004','validating','quarantined','security',
	'SOURCE_QUARANTINED',false,'text','v1',repeat('3',64),'chunk/v1','v1',$3,1,'[]',$4,$4,1)`

type reviewDraftFixture struct {
	service     *reviewapp.Service
	repository  reviewIntegrationRepository
	pool        *pgxpool.Pool
	workspaceID foundation.ID
	card        domain.Card
	now         time.Time
}

func createReviewDraftFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, keyPrefix string) reviewDraftFixture {
	t.Helper()
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	return createReviewDraftFixtureWithRepository(t, ctx, pool, repository, keyPrefix)
}

func createReviewDraftFixtureForVariant(t *testing.T, ctx context.Context, variant reviewIntegrationVariant, keyPrefix string) reviewDraftFixture {
	t.Helper()
	repository, _, pool := variant.open(t)
	return createReviewDraftFixtureWithRepository(t, ctx, pool, repository, keyPrefix)
}

func createReviewDraftFixtureWithRepository(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	repository reviewIntegrationRepository,
	keyPrefix string,
) reviewDraftFixture {
	t.Helper()
	seedReviewEvidence(t, ctx, pool)
	fsrs, err := platformscheduler.NewFSRSAdapter()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	service, err := reviewapp.NewService(repository, repository, reviewapp.NewDeterministicScorer(), fsrs, reviewIntegrationQuestionRefKey, foundation.NewUUIDGenerator(nil), foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := foundation.ID("71000000-0000-4000-8000-000000000001")
	claimID := foundation.ID("71000000-0000-4000-8000-000000000007")
	deck, err := service.CreateDeck(ctx, reviewapp.CreateDeckCommand{WorkspaceID: workspaceID, Name: keyPrefix, DailyLimit: 20, IdempotencyKey: keyPrefix + "-deck"})
	if err != nil {
		t.Fatal(err)
	}
	card, err := service.CreateCard(ctx, reviewapp.CreateCardCommand{
		WorkspaceID: workspaceID, DeckID: deck.Value.ID, ClaimID: claimID,
		Question: "When does evidence become invalid?", AnswerPoints: []string{"on quarantine"},
		Evidence: []domain.EvidenceBinding{{
			SchemaVersion: domain.EvidenceSchemaVersionV1, ClaimID: claimID,
			SourceVersionID: foundation.ID("71000000-0000-4000-8000-000000000004"),
			SourceSpanID:    foundation.ID("71000000-0000-4000-8000-000000000006"),
			EvidenceHash:    reviewHash("claim-source"),
		}},
		CardType: domain.CardTypeShortAnswer, Difficulty: 0.5, ModelVersion: "manual", IdempotencyKey: keyPrefix + "-card",
	})
	if err != nil {
		t.Fatal(err)
	}
	return reviewDraftFixture{service: service, repository: repository, pool: pool, workspaceID: workspaceID, card: card.Value, now: now}
}

func createReviewHighConflict(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID, now time.Time, keyPrefix string) string {
	t.Helper()
	const (
		claimID       = "71000000-0000-4000-8000-000000000033"
		claimSourceID = "71000000-0000-4000-8000-000000000034"
		conflictID    = "71000000-0000-4000-8000-000000000035"
	)
	setup := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO core.claim(id,workspace_id,statement,normalized_statement,applicability,applicability_schema_version,applicability_hash,status,confidence_score,confidence_factors,fingerprint,version,created_at,updated_at)
			VALUES($1,$2,'Competing review claim','competing review claim','{}','knowledge-applicability/v1',$3,'SUGGESTED',0.8,'{}',$4,1,$5,$5)`, []any{claimID, string(workspaceID), reviewHash("applicability"), reviewHash(keyPrefix + "-claim"), now}},
		{`INSERT INTO core.claim_source(id,workspace_id,claim_id,source_version_id,source_span_id,support_type,reason,evidence_hash,created_at)
			VALUES($1,$2,$3,'71000000-0000-4000-8000-000000000004','71000000-0000-4000-8000-000000000006','SUPPORTS','competing review evidence',$4,$5)`, []any{claimSourceID, string(workspaceID), claimID, reviewHash(keyPrefix + "-evidence"), now}},
		{`UPDATE core.claim SET status='CONFIRMED',version=2,updated_at=$3 WHERE workspace_id=$1 AND id=$2`, []any{string(workspaceID), claimID, now.Add(time.Second)}},
	}
	for _, statement := range setup {
		if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	statements := []struct {
		query string
		args  []any
	}{
		{`SELECT id::text FROM core.workspace WHERE id=$1 FOR UPDATE`, []any{string(workspaceID)}},
		{`INSERT INTO core.conflict(id,workspace_id,status,severity,summary,applicability_assessment,applicability_hash,fingerprint,version,created_at,updated_at)
			VALUES($1,$2,'OPEN','HIGH','Review claims conflict','EXACT',$3,$4,1,$5,$5)`, []any{conflictID, string(workspaceID), reviewHash("applicability"), reviewHash(keyPrefix + "-conflict"), now.Add(3 * time.Second)}},
		{`INSERT INTO core.conflict_member(conflict_id,claim_id,workspace_id,applicability,applicability_schema_version,applicability_hash,position_summary,created_at)
			VALUES($1,'71000000-0000-4000-8000-000000000007',$2,'{}','knowledge-applicability/v1',$3,'original review claim',$4)`, []any{conflictID, string(workspaceID), reviewHash("applicability"), now.Add(3 * time.Second)}},
		{`INSERT INTO core.conflict_member(conflict_id,claim_id,workspace_id,applicability,applicability_schema_version,applicability_hash,position_summary,created_at)
			VALUES($1,$2,$3,'{}','knowledge-applicability/v1',$4,'competing review claim',$5)`, []any{conflictID, claimID, string(workspaceID), reviewHash("applicability"), now.Add(3 * time.Second)}},
		{`UPDATE core.claim SET status='DISPUTED',version=version+1,updated_at=$2 WHERE workspace_id=$1 AND id='71000000-0000-4000-8000-000000000007'`, []any{string(workspaceID), now.Add(3 * time.Second)}},
		{`UPDATE core.claim SET status='DISPUTED',version=3,updated_at=$3 WHERE workspace_id=$1 AND id=$2`, []any{string(workspaceID), claimID, now.Add(3 * time.Second)}},
	}
	for index, statement := range statements {
		if index == 0 {
			var lockedWorkspace string
			if err := tx.QueryRow(ctx, statement.query, statement.args...).Scan(&lockedWorkspace); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if _, err := tx.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	var originalStatus, competingStatus string
	if err := tx.QueryRow(ctx, `SELECT
		(SELECT status FROM core.claim WHERE workspace_id=$1 AND id='71000000-0000-4000-8000-000000000007'),
		(SELECT status FROM core.claim WHERE workspace_id=$1 AND id=$2)`, string(workspaceID), claimID).Scan(&originalStatus, &competingStatus); err != nil {
		t.Fatal(err)
	}
	if originalStatus != "DISPUTED" || competingStatus != "DISPUTED" {
		t.Fatalf("conflict claim states original=%s competing=%s", originalStatus, competingStatus)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return conflictID
}

func assertInvalidatedReviewCard(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, cardID foundation.ID) {
	t.Helper()
	var status, reason string
	var scheduleCount int
	if err := pool.QueryRow(ctx, `SELECT status,invalidation_reason,(SELECT count(*) FROM learning.review_schedule WHERE workspace_id=$1 AND card_id=$2) FROM learning.review_card WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(cardID)).Scan(&status, &reason, &scheduleCount); err != nil {
		t.Fatal(err)
	}
	if status != string(domain.CardStatusInvalidated) || !strings.HasPrefix(reason, "SOURCE_QUARANTINED:") || scheduleCount != 0 {
		t.Fatalf("quarantine state status=%s reason=%s schedules=%d", status, reason, scheduleCount)
	}
}

func TestReviewRepositorySubmitAnswerRejectsCardsOutsideServerDueSet(t *testing.T) {
	runReviewIntegrationVariants(t, testReviewRepositorySubmitAnswerRejectsCardsOutsideServerDueSet)
}

func testReviewRepositorySubmitAnswerRejectsCardsOutsideServerDueSet(t *testing.T, variant reviewIntegrationVariant) {
	ctx := context.Background()
	repository, _, pool := variant.open(t)
	seedReviewEvidence(t, ctx, pool)

	workspaceID := foundation.ID("71000000-0000-4000-8000-000000000001")
	claimID := foundation.ID("71000000-0000-4000-8000-000000000007")
	evidence := []domain.EvidenceBinding{{
		SchemaVersion: domain.EvidenceSchemaVersionV1,
		ClaimID:       claimID, SourceVersionID: foundation.ID("71000000-0000-4000-8000-000000000004"),
		SourceSpanID: foundation.ID("71000000-0000-4000-8000-000000000006"), EvidenceHash: reviewHash("claim-source"),
	}}
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	fsrs, err := platformscheduler.NewFSRSAdapter()
	if err != nil {
		t.Fatal(err)
	}
	bypass := &answerEligibilityBypassRepository{reviewIntegrationRepository: repository}
	service, err := reviewapp.NewService(bypass, repository, reviewapp.NewDeterministicScorer(), fsrs, reviewIntegrationQuestionRefKey, foundation.NewUUIDGenerator(nil), foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatal(err)
	}
	deck, err := service.CreateDeck(ctx, reviewapp.CreateDeckCommand{WorkspaceID: workspaceID, Name: "Bounded due deck", DailyLimit: 1, IdempotencyKey: "bounded-deck-create"})
	if err != nil {
		t.Fatal(err)
	}
	cards := make([]domain.Card, 0, 2)
	for index := 0; index < 2; index++ {
		created, createErr := service.CreateCard(ctx, reviewapp.CreateCardCommand{
			WorkspaceID: workspaceID, DeckID: deck.Value.ID, ClaimID: claimID,
			Question: "Bounded due question " + string(rune('A'+index)), AnswerPoints: []string{"server due eligibility"},
			Evidence: evidence, CardType: domain.CardTypeShortAnswer, Difficulty: 0.5, ModelVersion: "manual",
			IdempotencyKey: "bounded-card-create-" + string(rune('1'+index)),
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		approved, approveErr := service.ApproveCard(ctx, reviewapp.CardDecisionCommand{
			WorkspaceID: workspaceID, CardID: created.Value.ID, ExpectedVersion: created.Value.Version,
			IdempotencyKey: "bounded-card-approve-" + string(rune('1'+index)),
		})
		if approveErr != nil {
			t.Fatal(approveErr)
		}
		cards = append(cards, approved.Value)
	}
	session, err := service.StartSession(ctx, reviewapp.StartSessionCommand{WorkspaceID: workspaceID, DeckID: &deck.Value.ID, SessionType: domain.SessionTypeReview, IdempotencyKey: "bounded-session-start"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE learning.review_deck SET daily_limit=2 WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(deck.Value.ID)); err != nil {
		t.Fatal(err)
	}
	allDue, err := service.ListDue(ctx, workspaceID, session.Value.ID, &deck.Value.ID, 20)
	if err != nil || len(allDue) != 2 {
		t.Fatalf("question-ref fixture due cards=%d err=%v", len(allDue), err)
	}
	questionRefs := make(map[foundation.ID]string, len(allDue))
	for _, item := range allDue {
		questionRefs[item.Card.ID] = item.QuestionRef
	}
	if _, err := pool.Exec(ctx, `UPDATE learning.review_deck SET daily_limit=1 WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(deck.Value.ID)); err != nil {
		t.Fatal(err)
	}
	due, err := service.ListDue(ctx, workspaceID, session.Value.ID, &deck.Value.ID, 20)
	if err != nil || len(due) != 1 {
		t.Fatalf("daily-limited due cards=%d err=%v", len(due), err)
	}
	visible := due[0].Card
	hidden := cards[0]
	if hidden.ID == visible.ID {
		hidden = cards[1]
	}
	if _, err := service.SubmitAnswer(ctx, reviewapp.SubmitAnswerCommand{
		WorkspaceID: workspaceID, SessionID: session.Value.ID, CardID: hidden.ID, QuestionRef: questionRefs[hidden.ID],
		UserAnswer: "server due eligibility", Rating: domain.RatingGood, IdempotencyKey: "bounded-hidden-answer",
	}); reviewPersistenceErrorCode(err) != domain.ErrorCodeScheduleConflict {
		t.Fatalf("card beyond daily limit was accepted: %v", err)
	}
	if _, err := service.SubmitAnswer(ctx, reviewapp.SubmitAnswerCommand{
		WorkspaceID: workspaceID, SessionID: session.Value.ID, CardID: visible.ID, QuestionRef: due[0].QuestionRef,
		UserAnswer: "server due eligibility", Rating: domain.RatingGood, IdempotencyKey: "bounded-visible-answer",
	}); err != nil {
		t.Fatalf("visible card answer failed: %v", err)
	}
	if due, err = service.ListDue(ctx, workspaceID, session.Value.ID, &deck.Value.ID, 20); err != nil || len(due) != 0 {
		t.Fatalf("answered-today quota remained available: count=%d err=%v", len(due), err)
	}
	if _, err := service.SubmitAnswer(ctx, reviewapp.SubmitAnswerCommand{
		WorkspaceID: workspaceID, SessionID: session.Value.ID, CardID: hidden.ID, QuestionRef: questionRefs[hidden.ID],
		UserAnswer: "server due eligibility", Rating: domain.RatingGood, IdempotencyKey: "bounded-hidden-after-answer",
	}); reviewPersistenceErrorCode(err) != domain.ErrorCodeScheduleConflict {
		t.Fatalf("card beyond cumulative daily limit was accepted: %v", err)
	}

	futureAt := now.Add(time.Hour)
	if _, err := pool.Exec(ctx, `UPDATE learning.review_schedule SET due_at=$3 WHERE workspace_id=$1 AND card_id=ANY($2::uuid[])`, string(workspaceID), []string{string(cards[0].ID), string(cards[1].ID)}, futureAt); err != nil {
		t.Fatal(err)
	}
	if due, err = service.ListDue(ctx, workspaceID, session.Value.ID, &deck.Value.ID, 20); err != nil || len(due) != 0 {
		t.Fatalf("future schedules remained due: count=%d err=%v", len(due), err)
	}
	if _, err := service.SubmitAnswer(ctx, reviewapp.SubmitAnswerCommand{
		WorkspaceID: workspaceID, SessionID: session.Value.ID, CardID: hidden.ID, QuestionRef: questionRefs[hidden.ID],
		UserAnswer: "server due eligibility", Rating: domain.RatingGood, IdempotencyKey: "bounded-future-answer",
	}); reviewPersistenceErrorCode(err) != domain.ErrorCodeScheduleConflict {
		t.Fatalf("future-due card was accepted: %v", err)
	}
	if due, err = repository.ListDue(ctx, workspaceID, &deck.Value.ID, now.Add(24*time.Hour), 20); err != nil || len(due) != 1 {
		t.Fatalf("UTC next-day quota did not reopen: count=%d err=%v", len(due), err)
	}

	var answerCount int
	var minVersion, maxVersion int64
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM learning.review_answer WHERE workspace_id=$1 AND session_id=$2),
		min(version),max(version)
		FROM learning.review_schedule WHERE workspace_id=$1 AND card_id=ANY($3::uuid[])`,
		string(workspaceID), string(session.Value.ID), []string{string(cards[0].ID), string(cards[1].ID)}).Scan(&answerCount, &minVersion, &maxVersion); err != nil {
		t.Fatal(err)
	}
	if answerCount != 1 || minVersion != 1 || maxVersion != 2 {
		t.Fatalf("rejected answers changed learning state: answers=%d schedule_versions=%d..%d", answerCount, minVersion, maxVersion)
	}
}

func TestReviewRepositoryIgnoresLegacyEvidenceQuote(t *testing.T) {
	runReviewIntegrationVariants(t, testReviewRepositoryIgnoresLegacyEvidenceQuote)
}

func testReviewRepositoryIgnoresLegacyEvidenceQuote(t *testing.T, variant reviewIntegrationVariant) {
	ctx := context.Background()
	repository, _, pool := variant.open(t)
	seedReviewEvidence(t, ctx, pool)

	workspaceID := foundation.ID("71000000-0000-4000-8000-000000000001")
	claimID := foundation.ID("71000000-0000-4000-8000-000000000007")
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	fsrs, err := platformscheduler.NewFSRSAdapter()
	if err != nil {
		t.Fatal(err)
	}
	service, err := reviewapp.NewService(repository, repository, reviewapp.NewDeterministicScorer(), fsrs, reviewIntegrationQuestionRefKey, foundation.NewUUIDGenerator(nil), foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatal(err)
	}
	deck, err := service.CreateDeck(ctx, reviewapp.CreateDeckCommand{WorkspaceID: workspaceID, Name: "Legacy quote", DailyLimit: 10, IdempotencyKey: "legacy-quote-deck"})
	if err != nil {
		t.Fatal(err)
	}
	evidence := []domain.EvidenceBinding{{
		SchemaVersion: domain.EvidenceSchemaVersionV1,
		ClaimID:       claimID, SourceVersionID: foundation.ID("71000000-0000-4000-8000-000000000004"),
		SourceSpanID: foundation.ID("71000000-0000-4000-8000-000000000006"), EvidenceHash: reviewHash("claim-source"),
	}}
	card, err := service.CreateCard(ctx, reviewapp.CreateCardCommand{
		WorkspaceID: workspaceID, DeckID: deck.Value.ID, ClaimID: claimID,
		Question: "Should legacy quote metadata affect review eligibility?", AnswerPoints: []string{"No"},
		Evidence: evidence, CardType: domain.CardTypeShortAnswer, Difficulty: 0.5, ModelVersion: "manual", IdempotencyKey: "legacy-quote-card",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE learning.review_card SET evidence=jsonb_set(evidence,'{0,quote}','123'::jsonb) WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(card.Value.ID)); err != nil {
		t.Fatal(err)
	}
	approved, err := service.ApproveCard(ctx, reviewapp.CardDecisionCommand{
		WorkspaceID: workspaceID, CardID: card.Value.ID, ExpectedVersion: card.Value.Version, IdempotencyKey: "legacy-quote-approve",
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.StartSession(ctx, reviewapp.StartSessionCommand{WorkspaceID: workspaceID, DeckID: &deck.Value.ID, SessionType: domain.SessionTypeReview, IdempotencyKey: "legacy-quote-session"})
	if err != nil {
		t.Fatal(err)
	}
	due, err := service.ListDue(ctx, workspaceID, session.Value.ID, &deck.Value.ID, 20)
	if err != nil || len(due) != 1 || due[0].Card.ID != approved.Value.ID {
		t.Fatalf("legacy quote changed due eligibility: count=%d err=%v", len(due), err)
	}
	var persistedEvidence string
	if err := pool.QueryRow(ctx, `SELECT evidence::text FROM learning.review_card WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(card.Value.ID)).Scan(&persistedEvidence); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(persistedEvidence, `"quote"`) {
		t.Fatalf("legacy quote was unexpectedly rewritten: %s", persistedEvidence)
	}
}

func TestReviewRepositoryDueQueriesFailClosedForMalformedLegacyEvidence(t *testing.T) {
	runReviewIntegrationVariants(t, testReviewRepositoryDueQueriesFailClosedForMalformedLegacyEvidence)
}

func testReviewRepositoryDueQueriesFailClosedForMalformedLegacyEvidence(t *testing.T, variant reviewIntegrationVariant) {
	ctx := context.Background()
	repository, _, pool := variant.open(t)
	seedReviewEvidence(t, ctx, pool)

	const (
		workspaceID = "71000000-0000-4000-8000-000000000001"
		claimID     = "71000000-0000-4000-8000-000000000007"
		deckID      = "71000000-0000-4000-8000-000000000040"
	)
	malformedCards := []struct {
		id          string
		evidence    string
		fingerprint string
	}{
		{id: "71000000-0000-4000-8000-000000000041", evidence: `[{"legacy":true,"source_version_id":"not-a-uuid"}]`, fingerprint: strings.Repeat("9", 64)},
		{id: "71000000-0000-4000-8000-000000000042", evidence: `{"legacy":true,"source_version_id":"not-a-uuid"}`, fingerprint: strings.Repeat("8", 64)},
	}
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO learning.review_deck(
		id,workspace_id,name,scope,status,daily_limit,scheduler_version,version,created_at,updated_at
	) VALUES($1,$2,'Malformed legacy evidence','{}','ACTIVE',20,'fsrs/v1',1,$3,$3)`, deckID, workspaceID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE learning.review_card
		DROP CONSTRAINT review_card_evidence_check,
		DROP CONSTRAINT learning_review_card_evidence_nonempty`); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for _, malformed := range malformedCards {
		if _, err := tx.Exec(ctx, `INSERT INTO learning.review_card(
			id,workspace_id,deck_id,claim_id,question,answer_points,evidence,card_type,difficulty,
			status,fingerprint,model_version,version,created_at,updated_at
		) VALUES($1,$2,$3,$4,'Can malformed evidence be due?','["No"]',$5,
			'SHORT_ANSWER',0.5,'APPROVED',$6,'legacy',1,$7,$7)`,
			malformed.id, workspaceID, deckID, claimID, malformed.evidence, malformed.fingerprint, now); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO learning.review_schedule(
			card_id,workspace_id,due_at,interval_days,stability,difficulty,last_reviewed_at,scheduler_version,paused,version
		) VALUES($1,$2,$3,0,0,0.5,NULL,'fsrs/v1',false,1)`, malformed.id, workspaceID, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE learning.review_card
		ADD CONSTRAINT review_card_evidence_check
		CHECK (jsonb_typeof(evidence) = 'array') NOT VALID,
		ADD CONSTRAINT learning_review_card_evidence_nonempty
		CHECK (jsonb_array_length(evidence) > 0) NOT VALID`); err != nil {
		t.Fatal(err)
	}

	parsedWorkspaceID := foundation.ID(workspaceID)
	parsedDeckID := foundation.ID(deckID)
	due, err := repository.ListDue(ctx, parsedWorkspaceID, &parsedDeckID, now, 20)
	if err != nil || len(due) != 0 {
		t.Fatalf("malformed evidence due cards=%d err=%v", len(due), err)
	}
	for _, malformed := range malformedCards {
		if err := repository.CheckAnswerEligibility(ctx, parsedWorkspaceID, parsedDeckID, foundation.ID(malformed.id), now); reviewPersistenceErrorCode(err) != domain.ErrorCodeScheduleConflict {
			t.Fatalf("malformed evidence %s eligibility error=%v", malformed.id, err)
		}
	}
}

func TestReviewRepositoryRejectsRefutingEvidenceAndSupersededClaim(t *testing.T) {
	runReviewIntegrationVariants(t, testReviewRepositoryRejectsRefutingEvidenceAndSupersededClaim)
}

func testReviewRepositoryRejectsRefutingEvidenceAndSupersededClaim(t *testing.T, variant reviewIntegrationVariant) {
	ctx := context.Background()
	repository, _, pool := variant.open(t)
	seedReviewEvidence(t, ctx, pool)

	workspaceID := foundation.ID("71000000-0000-4000-8000-000000000001")
	claimID := foundation.ID("71000000-0000-4000-8000-000000000007")
	supporting := []domain.EvidenceBinding{{SchemaVersion: domain.EvidenceSchemaVersionV1, ClaimID: claimID, SourceVersionID: foundation.ID("71000000-0000-4000-8000-000000000004"), SourceSpanID: foundation.ID("71000000-0000-4000-8000-000000000006"), EvidenceHash: reviewHash("claim-source")}}
	refuting := []domain.EvidenceBinding{{SchemaVersion: domain.EvidenceSchemaVersionV1, ClaimID: claimID, SourceVersionID: supporting[0].SourceVersionID, SourceSpanID: supporting[0].SourceSpanID, EvidenceHash: reviewHash("claim-refutes")}}
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO core.claim_source(id,workspace_id,claim_id,source_version_id,source_span_id,support_type,reason,evidence_hash,created_at)
		VALUES('71000000-0000-4000-8000-000000000009',$1,$2,$3,$4,'REFUTES','counter evidence',$5,$6)`, string(workspaceID), string(claimID), string(refuting[0].SourceVersionID), string(refuting[0].SourceSpanID), refuting[0].EvidenceHash, now); err != nil {
		t.Fatal(err)
	}
	if err := repository.VerifyCardEvidence(ctx, workspaceID, claimID, refuting); reviewPersistenceErrorCode(err) != domain.ErrorCodeEvidenceStale {
		t.Fatalf("refuting evidence was accepted as a standard answer source: %v", err)
	}
	fsrs, err := platformscheduler.NewFSRSAdapter()
	if err != nil {
		t.Fatal(err)
	}
	service, err := reviewapp.NewService(repository, repository, reviewapp.NewDeterministicScorer(), fsrs, reviewIntegrationQuestionRefKey, foundation.NewUUIDGenerator(nil), foundation.FixedClock{Value: now})
	if err != nil {
		t.Fatal(err)
	}
	deck, err := service.CreateDeck(ctx, reviewapp.CreateDeckCommand{WorkspaceID: workspaceID, Name: "Stale Claim Deck", DailyLimit: 20, IdempotencyKey: "stale-deck-create"})
	if err != nil {
		t.Fatal(err)
	}
	card, err := service.CreateCard(ctx, reviewapp.CreateCardCommand{WorkspaceID: workspaceID, DeckID: deck.Value.ID, ClaimID: claimID, Question: "哪个证据支持该 Claim？", AnswerPoints: []string{"仅 SUPPORTS 证据可作为标准答案"}, Evidence: supporting, CardType: domain.CardTypeShortAnswer, Difficulty: 0.5, ModelVersion: "manual", IdempotencyKey: "stale-card-create"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApproveCard(ctx, reviewapp.CardDecisionCommand{WorkspaceID: workspaceID, CardID: card.Value.ID, ExpectedVersion: 1, IdempotencyKey: "stale-card-approve"}); err != nil {
		t.Fatal(err)
	}
	session, err := service.StartSession(ctx, reviewapp.StartSessionCommand{WorkspaceID: workspaceID, DeckID: &deck.Value.ID, SessionType: domain.SessionTypeReview, IdempotencyKey: "stale-session-start"})
	if err != nil {
		t.Fatal(err)
	}
	due, err := service.ListDue(ctx, workspaceID, session.Value.ID, &deck.Value.ID, 20)
	if err != nil || len(due) != 1 {
		t.Fatalf("due before Claim invalidation=%d err=%v", len(due), err)
	}
	invalidatedAt := now.Add(2 * time.Second)
	if _, err := pool.Exec(ctx, `UPDATE core.claim SET status='SUPERSEDED',version=version+1,updated_at=$3 WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(claimID), invalidatedAt); err != nil {
		t.Fatal(err)
	}
	due, err = service.ListDue(ctx, workspaceID, session.Value.ID, &deck.Value.ID, 20)
	if err != nil || len(due) != 0 {
		t.Fatalf("superseded Claim remained due: count=%d err=%v", len(due), err)
	}
	persistedCard, err := repository.GetCard(ctx, workspaceID, card.Value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persistedCard.Status != domain.CardStatusInvalidated || persistedCard.InvalidationReason != "CLAIM_SUPERSEDED" || persistedCard.InvalidatedAt == nil || !persistedCard.InvalidatedAt.Equal(invalidatedAt) {
		t.Fatalf("superseded Claim did not durably invalidate card: %+v", persistedCard)
	}
	if _, err := service.SubmitAnswer(ctx, reviewapp.SubmitAnswerCommand{WorkspaceID: workspaceID, SessionID: session.Value.ID, CardID: card.Value.ID, QuestionRef: "stale-card:1", UserAnswer: "历史答案", Rating: domain.RatingGood, IdempotencyKey: "stale-claim-answer"}); reviewPersistenceErrorCode(err) != domain.ErrorCodeCardInactive {
		t.Fatalf("superseded Claim accepted an answer: %v", err)
	}
	var answerCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.review_answer WHERE workspace_id=$1 AND idempotency_key='stale-claim-answer'`, string(workspaceID)).Scan(&answerCount); err != nil {
		t.Fatal(err)
	}
	var scheduleCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.review_schedule WHERE workspace_id=$1 AND card_id=$2`, string(workspaceID), string(card.Value.ID)).Scan(&scheduleCount); err != nil {
		t.Fatal(err)
	}
	if answerCount != 0 || scheduleCount != 0 {
		t.Fatalf("invalidated card retained learning state: answers=%d schedules=%d", answerCount, scheduleCount)
	}
}

func newReviewTestDatabase(t *testing.T, ctx context.Context) (*pgxpool.Pool, func()) {
	t.Helper()
	fixture := testdb.Require(t, testdb.Config{
		ExternalAdminURL: strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL")),
		Availability:     testdb.FailWhenUnavailable,
		MaxConns:         16,
	})
	platform := fixture.Pool()
	if platform == nil || platform.DB() == nil {
		t.Fatal("shared PostgreSQL fixture did not provide a platform pool")
	}
	return platform.DB(), func() {
		_ = fixture.Close(context.Background())
	}
}

func seedReviewEvidence(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	now := time.Date(2026, 7, 23, 8, 0, 0, 0, time.UTC)
	workspaceID := "71000000-0000-4000-8000-000000000001"
	contentHash := reviewHash("content")
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO core.workspace(
			id,name,root_path,root_fingerprint,binding_version,git_repository_path,git_checked_at,
			status,availability,availability_reason,availability_checked_at,version,created_at,updated_at
		) VALUES($1,'review','/tmp/review',$2,1,'/tmp/review',$3,'inactive','available',NULL,$3,1,$3,$3)`, []any{workspaceID, reviewHash("review-workspace-root"), now}},
		{`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at) VALUES('71000000-0000-4000-8000-000000000002',$1,$2,4,$3,$4)`, []any{workspaceID, contentHash, ".knowledge/sources/" + contentHash, now}},
		{`INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at) VALUES('71000000-0000-4000-8000-000000000003',$1,'text','review.txt','review.txt',$2)`, []any{workspaceID, now}},
		{`INSERT INTO core.source_version(id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at) VALUES('71000000-0000-4000-8000-000000000004','71000000-0000-4000-8000-000000000003',$1,'71000000-0000-4000-8000-000000000002',$2,4,'text/plain','review.txt','pending',$3)`, []any{workspaceID, contentHash, now}},
		{`INSERT INTO ingestion.parse_projection(id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,warnings,created_at) VALUES('71000000-0000-4000-8000-000000000005',$1,'71000000-0000-4000-8000-000000000002','text','v1',$2,'v1',$3,'[]',$4)`, []any{workspaceID, reviewHash("parser"), reviewHash("normalized"), now}},
		{`INSERT INTO ingestion.source_span(id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,selector,excerpt_hash,parser_version,schema_version,created_at) VALUES('71000000-0000-4000-8000-000000000006',$1,'71000000-0000-4000-8000-000000000002','71000000-0000-4000-8000-000000000005','paragraph',1,1,0,4,'{}',$2,'v1','v1',$3)`, []any{workspaceID, reviewHash("excerpt"), now}},
		{`INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at) VALUES('71000000-0000-4000-8000-000000000004','71000000-0000-4000-8000-000000000005',$1,$2)`, []any{workspaceID, now}},
		{`INSERT INTO core.claim(id,workspace_id,statement,normalized_statement,applicability,applicability_schema_version,applicability_hash,status,confidence_score,confidence_factors,fingerprint,version,created_at,updated_at) VALUES('71000000-0000-4000-8000-000000000007',$1,'Answer 与 Schedule 同事务','answer schedule same tx','{}','knowledge-applicability/v1',$2,'SUGGESTED',0.9,'{}',$3,1,$4,$4)`, []any{workspaceID, reviewHash("applicability"), reviewHash("claim"), now}},
		{`INSERT INTO core.claim_source(id,workspace_id,claim_id,source_version_id,source_span_id,support_type,reason,evidence_hash,created_at) VALUES('71000000-0000-4000-8000-000000000008',$1,'71000000-0000-4000-8000-000000000007','71000000-0000-4000-8000-000000000004','71000000-0000-4000-8000-000000000006','SUPPORTS','review fixture',$2,$3)`, []any{workspaceID, reviewHash("claim-source"), now}},
		{`UPDATE core.claim SET status='CONFIRMED',version=2,updated_at=$2 WHERE workspace_id=$1 AND id='71000000-0000-4000-8000-000000000007'`, []any{workspaceID, now.Add(time.Second)}},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func reviewHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func reviewDueQuestionRef(t *testing.T, ctx context.Context, service *reviewapp.Service, workspaceID, sessionID, deckID, cardID foundation.ID) string {
	t.Helper()
	due, err := service.ListDue(ctx, workspaceID, sessionID, &deckID, 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range due {
		if item.Card.ID == cardID {
			return item.QuestionRef
		}
	}
	t.Fatalf("card %s has no signed due question reference", cardID)
	return ""
}

func reviewPersistenceErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}

func reviewPostgresErrorCode(err error) string {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		return postgresError.Code
	}
	return ""
}

type reviewIntegrationRepository interface {
	reviewapp.Repository
	reviewapp.EvidenceVerifier
}

type reviewIntegrationVariant struct {
	name string
}

func runReviewIntegrationVariants(t *testing.T, scenario func(*testing.T, reviewIntegrationVariant)) {
	t.Helper()
	for _, name := range []string{"legacy-pgx", "gorm"} {
		variant := reviewIntegrationVariant{name: name}
		t.Run(name, func(t *testing.T) {
			scenario(t, variant)
		})
	}
}

func (variant reviewIntegrationVariant) open(t *testing.T) (reviewIntegrationRepository, *platformpostgres.Pool, *pgxpool.Pool) {
	t.Helper()
	fixture := testdb.Require(t, testdb.Config{
		ExternalAdminURL: strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL")),
		Availability:     testdb.FailWhenUnavailable,
		MaxConns:         16,
	})
	platform := fixture.Pool()
	if platform == nil || platform.DB() == nil {
		t.Fatal("shared PostgreSQL fixture did not provide a platform pool")
	}
	var (
		repository reviewIntegrationRepository
		err        error
	)
	switch variant.name {
	case "legacy-pgx":
		repository, err = NewRepository(platform.DB())
	case "gorm":
		repository, err = NewGORMRepository(platform)
	default:
		t.Fatalf("unknown Review integration variant %q", variant.name)
	}
	if err != nil {
		t.Fatal(err)
	}
	return repository, platform, platform.DB()
}

func reviewResponseLossRepository(
	t *testing.T,
	variant reviewIntegrationVariant,
	repository reviewIntegrationRepository,
	platform *platformpostgres.Pool,
	cause error,
) (reviewIntegrationRepository, error) {
	t.Helper()
	if platform == nil || platform.DB() == nil {
		return nil, errors.New("review response-loss platform is unavailable")
	}
	switch variant.name {
	case "legacy-pgx":
		return NewRepository(commitResponseLossDB{DB: platform.DB()})
	case "gorm":
		gormRepository, ok := repository.(*GORMRepository)
		if !ok || gormRepository == nil {
			return nil, errors.New("review GORM response-loss repository has unexpected type")
		}
		lossy := *gormRepository
		lossy.unitOfWork = reviewCommitResponseLossUnitOfWork{delegate: gormRepository.unitOfWork, cause: cause}
		return &lossy, nil
	default:
		return nil, fmt.Errorf("unknown Review integration variant %q", variant.name)
	}
}

type reviewCommitResponseLossUnitOfWork struct {
	delegate foundation.UnitOfWork
	cause    error
}

func (unitOfWork reviewCommitResponseLossUnitOfWork) Within(
	ctx context.Context,
	options foundation.TransactionOptions,
	work foundation.TransactionFunc,
) error {
	if err := unitOfWork.delegate.Within(ctx, options, work); err != nil {
		return err
	}
	return unitOfWork.cause
}

type reviewStatementCounter struct {
	count atomic.Int64
}

func (counter *reviewStatementCounter) LogMode(gormlogger.LogLevel) gormlogger.Interface {
	return counter
}

func (*reviewStatementCounter) Info(context.Context, string, ...any)  {}
func (*reviewStatementCounter) Warn(context.Context, string, ...any)  {}
func (*reviewStatementCounter) Error(context.Context, string, ...any) {}

func (counter *reviewStatementCounter) Trace(context.Context, time.Time, func() (string, int64), error) {
	counter.count.Add(1)
}

func (counter *reviewStatementCounter) reset() {
	if counter != nil {
		counter.count.Store(0)
	}
}

func (counter *reviewStatementCounter) statements() int64 {
	if counter == nil {
		return 0
	}
	return counter.count.Load()
}

func installReviewStatementCounter(t *testing.T, repository reviewIntegrationRepository) *reviewStatementCounter {
	t.Helper()
	gormRepository, ok := repository.(*GORMRepository)
	if !ok {
		return nil
	}
	if gormRepository.database == nil || gormRepository.database.Config == nil {
		t.Fatal("Review GORM repository has no logger configuration")
	}
	original := gormRepository.database.Config.Logger
	counter := &reviewStatementCounter{}
	gormRepository.database.Config.Logger = counter
	t.Cleanup(func() {
		gormRepository.database.Config.Logger = original
	})
	return counter
}

type reviewExplainPlan struct {
	NodeType     string              `json:"Node Type"`
	RelationName string              `json:"Relation Name"`
	IndexName    string              `json:"Index Name"`
	ActualRows   float64             `json:"Actual Rows"`
	Plans        []reviewExplainPlan `json:"Plans"`
}

type reviewExplainQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func explainReviewQuery(t *testing.T, ctx context.Context, querier reviewExplainQuerier, query string, args ...any) reviewExplainPlan {
	t.Helper()
	var raw []byte
	if err := querier.QueryRow(ctx, `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, COSTS OFF, SUMMARY OFF, TIMING OFF) `+query, args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var documents []struct {
		Plan reviewExplainPlan `json:"Plan"`
	}
	if err := json.Unmarshal(raw, &documents); err != nil || len(documents) != 1 {
		t.Fatalf("invalid Review EXPLAIN document: err=%v plan=%s", err, raw)
	}
	return documents[0].Plan
}

func reviewPlanUsesAnyIndex(plan reviewExplainPlan, indexNames ...string) bool {
	for _, indexName := range indexNames {
		if plan.IndexName == indexName {
			return true
		}
	}
	for _, child := range plan.Plans {
		if reviewPlanUsesAnyIndex(child, indexNames...) {
			return true
		}
	}
	return false
}

func reviewPlanIndexNames(plan reviewExplainPlan) []string {
	indexNames := make([]string, 0)
	if plan.IndexName != "" {
		indexNames = append(indexNames, plan.IndexName)
	}
	for _, child := range plan.Plans {
		indexNames = append(indexNames, reviewPlanIndexNames(child)...)
	}
	return indexNames
}

type commitResponseLossDB struct{ DB }

type answerEligibilityBypassRepository struct{ reviewIntegrationRepository }

type answerABABlockingRepository struct {
	reviewIntegrationRepository
	started chan<- reviewapp.SubmitAnswerRecord
	release <-chan struct{}
}

func (repository *answerABABlockingRepository) SubmitAnswer(ctx context.Context, record reviewapp.SubmitAnswerRecord) (reviewapp.AnswerResult, error) {
	repository.started <- record
	select {
	case <-repository.release:
		return repository.reviewIntegrationRepository.SubmitAnswer(ctx, record)
	case <-ctx.Done():
		return reviewapp.AnswerResult{}, ctx.Err()
	}
}

func (*answerEligibilityBypassRepository) CheckAnswerEligibility(context.Context, foundation.ID, foundation.ID, foundation.ID, time.Time) error {
	return nil
}

func (database commitResponseLossDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := database.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return commitResponseLossTx{Tx: tx}, nil
}

type commitResponseLossTx struct{ pgx.Tx }

func (tx commitResponseLossTx) Commit(ctx context.Context) error {
	if err := tx.Tx.Commit(ctx); err != nil {
		return err
	}
	return errors.New("simulated completion response loss")
}
