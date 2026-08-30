//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	platformscheduler "github.com/CodeZen-Lizhi/zhixu/internal/platform/scheduler"
	reviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const reviewIntegrationQuestionRefKey = "review-question-ref-integration-key-2026"

func TestReviewRepositoryAnswerScheduleAtomicAndIdempotent(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newReviewTestDatabase(t, ctx)
	defer cleanup()
	seedReviewEvidence(t, ctx, pool)
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
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
	responseLossRepository, err := NewRepository(commitResponseLossDB{DB: pool})
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, cleanup := newReviewTestDatabase(t, ctx)
	defer cleanup()
	fixture := createReviewDraftFixture(t, ctx, pool, "review-interview-boundary")
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, cleanup := newReviewTestDatabase(t, ctx)
	defer cleanup()
	fixture := createReviewDraftFixture(t, ctx, pool, "answer-card-aba")

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
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
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
	blocking := &answerABABlockingRepository{Repository: repository, started: started, release: release}
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
	ctx := context.Background()
	pool, cleanup := newReviewTestDatabase(t, ctx)
	defer cleanup()
	seedReviewEvidence(t, ctx, pool)

	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
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
	t.Run("approval commits before quarantine invalidates the card", func(t *testing.T) {
		ctx := context.Background()
		pool, cleanup := newReviewTestDatabase(t, ctx)
		defer cleanup()
		fixture := createReviewDraftFixture(t, ctx, pool, "approval-first")
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
		pool, cleanup := newReviewTestDatabase(t, ctx)
		defer cleanup()
		fixture := createReviewDraftFixture(t, ctx, pool, "quarantine-first")

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
	t.Run("approved card is durably invalidated", func(t *testing.T) {
		ctx := context.Background()
		pool, cleanup := newReviewTestDatabase(t, ctx)
		defer cleanup()
		fixture := createReviewDraftFixture(t, ctx, pool, "conflict-after-approval")
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
		pool, cleanup := newReviewTestDatabase(t, ctx)
		defer cleanup()
		fixture := createReviewDraftFixture(t, ctx, pool, "conflict-before-approval")
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

const reviewQuarantineAttemptSQL = `INSERT INTO ingestion.attempt(
	id,workspace_id,source_version_id,status,security_status,failure_stage,error_code,retryable,
	parser_id,parser_version,parser_config_hash,chunk_strategy_version,schema_version,
	idempotency_key,attempt_number,warnings,started_at,completed_at,version
) VALUES($1,$2,'71000000-0000-4000-8000-000000000004','validating','quarantined','security',
	'SOURCE_QUARANTINED',false,'text','v1',repeat('3',64),'chunk/v1','v1',$3,1,'[]',$4,$4,1)`

type reviewDraftFixture struct {
	service     *reviewapp.Service
	workspaceID foundation.ID
	card        domain.Card
	now         time.Time
}

func createReviewDraftFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, keyPrefix string) reviewDraftFixture {
	t.Helper()
	seedReviewEvidence(t, ctx, pool)
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
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
	return reviewDraftFixture{service: service, workspaceID: workspaceID, card: card.Value, now: now}
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
	ctx := context.Background()
	pool, cleanup := newReviewTestDatabase(t, ctx)
	defer cleanup()
	seedReviewEvidence(t, ctx, pool)

	workspaceID := foundation.ID("71000000-0000-4000-8000-000000000001")
	claimID := foundation.ID("71000000-0000-4000-8000-000000000007")
	evidence := []domain.EvidenceBinding{{
		SchemaVersion: domain.EvidenceSchemaVersionV1,
		ClaimID:       claimID, SourceVersionID: foundation.ID("71000000-0000-4000-8000-000000000004"),
		SourceSpanID: foundation.ID("71000000-0000-4000-8000-000000000006"), EvidenceHash: reviewHash("claim-source"),
	}}
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	fsrs, err := platformscheduler.NewFSRSAdapter()
	if err != nil {
		t.Fatal(err)
	}
	bypass := &answerEligibilityBypassRepository{Repository: repository}
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
	ctx := context.Background()
	pool, cleanup := newReviewTestDatabase(t, ctx)
	defer cleanup()
	seedReviewEvidence(t, ctx, pool)

	workspaceID := foundation.ID("71000000-0000-4000-8000-000000000001")
	claimID := foundation.ID("71000000-0000-4000-8000-000000000007")
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
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
	ctx := context.Background()
	pool, cleanup := newReviewTestDatabase(t, ctx)
	defer cleanup()
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

	repository, err := NewRepository(pool)
	if err != nil {
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
	ctx := context.Background()
	pool, cleanup := newReviewTestDatabase(t, ctx)
	defer cleanup()
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
	repository, err := NewRepository(pool)
	if err != nil {
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
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL for Review PostgreSQL integration test")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	name := "zhixu_review_" + strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	pool, err := pgxpool.New(ctx, parsed.String())
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier)
		admin.Close()
		t.Fatal(err)
	}
	if err := platformmigration.MigrateAtlas(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool, func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
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
		{`INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'review','/tmp/review','/tmp/review',$2,'test',1,$2,$2)`, []any{workspaceID, now}},
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

type commitResponseLossDB struct{ DB }

type answerEligibilityBypassRepository struct{ *Repository }

type answerABABlockingRepository struct {
	*Repository
	started chan<- reviewapp.SubmitAnswerRecord
	release <-chan struct{}
}

func (repository *answerABABlockingRepository) SubmitAnswer(ctx context.Context, record reviewapp.SubmitAnswerRecord) (reviewapp.AnswerResult, error) {
	repository.started <- record
	select {
	case <-repository.release:
		return repository.Repository.SubmitAnswer(ctx, record)
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
