package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	reviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/domain"
	"gorm.io/gorm"
)

// StartSession creates a deck-bound REVIEW session with workspace-scoped replay.
func (repository *GORMRepository) StartSession(ctx context.Context, session domain.Session, requestHash string) (result reviewapp.CommandResult[domain.Session], returnErr error) {
	if session.SessionType != domain.SessionTypeReview || session.DeckID == nil {
		return result, domain.InvalidError(domain.ErrorCodeSessionInvalid, "review session requires REVIEW type and a deck")
	}
	if err := domain.ValidateSession(session); err != nil {
		return result, err
	}
	if err := validateCommandBinding(session.IdempotencyKey, requestHash); err != nil {
		return result, err
	}

	callbackSucceeded := false
	returnErr = repository.within(ctx, func(callbackCtx context.Context, database *gorm.DB) error {
		if err := gormReviewLockWorkspace(callbackCtx, database, session.WorkspaceID); err != nil {
			return err
		}
		existing, found, err := gormReviewLoadSessionByKey(callbackCtx, database, session.WorkspaceID, session.IdempotencyKey, requestHash)
		if err != nil {
			return err
		}
		if found {
			if err := validateReviewSessionBinding(existing); err != nil {
				return err
			}
			result = reviewapp.CommandResult[domain.Session]{Value: existing, Replayed: true}
			callbackSucceeded = true
			return nil
		}

		row, err := gormReviewRawRow(callbackCtx, database, `SELECT id::text
FROM learning.review_deck
WHERE workspace_id=?::uuid AND id=?::uuid AND status='ACTIVE'
FOR UPDATE`, string(session.WorkspaceID), string(*session.DeckID))
		if err != nil {
			return gormReviewClassify(callbackCtx, err, domain.ErrorCodeDependencyUnavailable)
		}
		var activeDeckID string
		if err := row.Scan(&activeDeckID); err != nil {
			if gormReviewNoRows(err) {
				return conflict(domain.ErrorCodeDeckArchived, "session deck is not active")
			}
			return gormReviewClassify(callbackCtx, err, domain.ErrorCodeDependencyUnavailable)
		}

		if _, err := gormReviewExec(callbackCtx, database, `INSERT INTO learning.review_session(
    id,workspace_id,deck_id,session_type,status,config,idempotency_key,request_hash,started_at
) VALUES (?::uuid,?::uuid,?::uuid,?,?,?,?,?,?)`,
			string(session.ID), string(session.WorkspaceID), string(*session.DeckID), string(session.SessionType),
			string(session.Status), reviewJSONB(session.Config), session.IdempotencyKey, requestHash, session.StartedAt.UTC()); err != nil {
			return gormReviewClassify(callbackCtx, err, domain.ErrorCodeSessionInvalid)
		}
		result = reviewapp.CommandResult[domain.Session]{Value: session}
		callbackSucceeded = true
		return nil
	})
	if returnErr == nil {
		return result, nil
	}
	if callbackSucceeded {
		if recovered, found, recoveryErr := gormReviewLoadSessionByKey(ctx, repository.database, session.WorkspaceID, session.IdempotencyKey, requestHash); recoveryErr == nil && found {
			if bindingErr := validateReviewSessionBinding(recovered); bindingErr == nil {
				return reviewapp.CommandResult[domain.Session]{Value: recovered, Replayed: true}, nil
			}
		}
	}
	return reviewapp.CommandResult[domain.Session]{}, gormReviewClassify(ctx, returnErr, domain.ErrorCodeDependencyUnavailable)
}

// GetSession returns any valid workspace-scoped session shell.
func (repository *GORMRepository) GetSession(ctx context.Context, workspaceID, sessionID foundation.ID) (domain.Session, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Session{}, err
	}
	return gormReviewLoadSession(ctx, repository.database, workspaceID, sessionID, false)
}

// CompleteSession closes one deck-bound REVIEW session and stores its receipt atomically.
func (repository *GORMRepository) CompleteSession(ctx context.Context, record reviewapp.CompleteSessionRecord) (result reviewapp.CommandResult[domain.Session], returnErr error) {
	if err := validateCommandBinding(record.IdempotencyKey, record.RequestHash); err != nil {
		return result, err
	}
	if record.Status != domain.SessionStatusCompleted && record.Status != domain.SessionStatusCancelled {
		return result, invalid("session terminal status is invalid")
	}

	callbackSucceeded := false
	returnErr = repository.within(ctx, func(callbackCtx context.Context, database *gorm.DB) error {
		if err := gormReviewLockWorkspace(callbackCtx, database, record.WorkspaceID); err != nil {
			return err
		}
		existing, found, err := gormReviewLoadSessionCommand(callbackCtx, database, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, "COMPLETE_SESSION")
		if err != nil {
			return err
		}
		if found {
			if err := validateReviewSessionBinding(existing); err != nil {
				return err
			}
			result = reviewapp.CommandResult[domain.Session]{Value: existing, Replayed: true}
			callbackSucceeded = true
			return nil
		}

		session, err := gormReviewLoadSession(callbackCtx, database, record.WorkspaceID, record.SessionID, true)
		if err != nil {
			return err
		}
		if err := validateReviewSessionBinding(session); err != nil {
			return err
		}
		if session.Status != domain.SessionStatusActive {
			return conflict(domain.ErrorCodeSessionClosed, "session is already closed")
		}
		at := record.At.UTC()
		if at.IsZero() {
			at = time.Now().UTC()
		}
		rowsAffected, err := gormReviewExec(callbackCtx, database, `UPDATE learning.review_session
SET status=?,ended_at=?
WHERE workspace_id=?::uuid AND id=?::uuid AND status='ACTIVE'`,
			string(record.Status), at, string(record.WorkspaceID), string(record.SessionID))
		if err != nil {
			return gormReviewClassify(callbackCtx, err, domain.ErrorCodeSessionInvalid)
		}
		if rowsAffected != 1 {
			return conflict(domain.ErrorCodeSessionClosed, "session is already closed")
		}
		session.Status, session.EndedAt = record.Status, &at
		response, err := encodeJSON(session)
		if err != nil {
			return err
		}
		if err := gormReviewInsertCommand(callbackCtx, database, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, "COMPLETE_SESSION", session.ID, 1, response, at); err != nil {
			return err
		}
		result = reviewapp.CommandResult[domain.Session]{Value: session}
		callbackSucceeded = true
		return nil
	})
	if returnErr == nil {
		return result, nil
	}
	if callbackSucceeded {
		if recovered, found, recoveryErr := gormReviewLoadSessionCommand(ctx, repository.database, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, "COMPLETE_SESSION"); recoveryErr == nil && found {
			return reviewapp.CommandResult[domain.Session]{Value: recovered, Replayed: true}, nil
		}
	}
	return reviewapp.CommandResult[domain.Session]{}, gormReviewClassify(ctx, returnErr, domain.ErrorCodeDependencyUnavailable)
}

// GetSchedule returns the current schedule for one workspace-scoped card.
func (repository *GORMRepository) GetSchedule(ctx context.Context, workspaceID, cardID foundation.ID) (domain.Schedule, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Schedule{}, err
	}
	return gormReviewLoadSchedule(ctx, repository.database, workspaceID, cardID, false)
}

// CheckAnswerEligibility fails closed unless the card is in the exact server-ranked due set.
func (repository *GORMRepository) CheckAnswerEligibility(ctx context.Context, workspaceID, deckID, cardID foundation.ID, now time.Time) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	if now.IsZero() {
		return domain.InvalidError(domain.ErrorCodeScheduleInvalid, "answer eligibility time is invalid")
	}
	return gormReviewCheckAnswerEligibility(ctx, repository.database, workspaceID, deckID, cardID, now)
}

// FindAnswerReplay returns an immutable answer snapshot before mutable state is read.
func (repository *GORMRepository) FindAnswerReplay(ctx context.Context, workspaceID foundation.ID, idempotencyKey, requestHash string) (reviewapp.AnswerResult, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return reviewapp.AnswerResult{}, false, err
	}
	if err := validateCommandBinding(idempotencyKey, requestHash); err != nil {
		return reviewapp.AnswerResult{}, false, err
	}
	value, found, err := gormReviewLoadAnswerByKey(ctx, repository.database, workspaceID, idempotencyKey, requestHash)
	if err != nil {
		return reviewapp.AnswerResult{}, false, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	return value, found, nil
}

// SubmitAnswer inserts the immutable Answer and advances its Schedule in one transaction.
func (repository *GORMRepository) SubmitAnswer(ctx context.Context, record reviewapp.SubmitAnswerRecord) (result reviewapp.AnswerResult, returnErr error) {
	if err := domain.ValidateAnswer(record.Answer); err != nil {
		return result, err
	}
	if record.Answer.CardID == nil || record.ExpectedCardVersion < 1 || !validHash(record.ExpectedCardFingerprint) || record.ExpectedScheduleVersion < 1 || !validHash(record.RequestHash) {
		return result, invalid("answer record is invalid")
	}

	callbackSucceeded := false
	returnErr = repository.within(ctx, func(callbackCtx context.Context, database *gorm.DB) error {
		if err := gormReviewLockWorkspace(callbackCtx, database, record.Answer.WorkspaceID); err != nil {
			return err
		}
		existing, found, err := gormReviewLoadAnswerByKey(callbackCtx, database, record.Answer.WorkspaceID, record.Answer.IdempotencyKey, record.RequestHash)
		if err != nil {
			return err
		}
		if found {
			result = existing
			callbackSucceeded = true
			return nil
		}

		session, err := gormReviewLoadSession(callbackCtx, database, record.Answer.WorkspaceID, record.Answer.SessionID, true)
		if err != nil {
			return err
		}
		if err := validateReviewSessionBinding(session); err != nil {
			return err
		}
		if session.Status != domain.SessionStatusActive {
			return conflict(domain.ErrorCodeSessionClosed, "session is not active")
		}
		card, err := gormReviewLoadCard(callbackCtx, database, record.Answer.WorkspaceID, *record.Answer.CardID, true)
		if err != nil {
			return err
		}
		if card.Version != record.ExpectedCardVersion || card.Fingerprint != record.ExpectedCardFingerprint {
			return conflict(domain.ErrorCodeCardStateConflict, "card version did not match scored content")
		}
		if card.Status != domain.CardStatusApproved || *session.DeckID != card.DeckID {
			return conflict(domain.ErrorCodeCardInactive, "card is not active in this session")
		}
		if card.ClaimID == nil {
			return domain.ConflictError(domain.ErrorCodeEvidenceStale, "card claim is missing")
		}
		if err := gormReviewVerifyEvidence(callbackCtx, database, card.WorkspaceID, *card.ClaimID, card.Evidence, true); err != nil {
			return err
		}
		schedule, err := gormReviewLoadSchedule(callbackCtx, database, record.Answer.WorkspaceID, *record.Answer.CardID, true)
		if err != nil {
			return err
		}
		if schedule.Paused {
			return conflict(domain.ErrorCodeScheduleConflict, "card schedule is paused")
		}
		if schedule.Version != record.ExpectedScheduleVersion {
			return conflict(domain.ErrorCodeScheduleConflict, "schedule version did not match")
		}
		if err := gormReviewCheckAnswerEligibility(callbackCtx, database, record.Answer.WorkspaceID, card.DeckID, *record.Answer.CardID, record.Answer.CreatedAt); err != nil {
			return err
		}

		next := schedule
		next.DueAt = record.ScheduleDecision.DueAt.UTC()
		next.IntervalDays = record.ScheduleDecision.IntervalDays
		next.Stability = record.ScheduleDecision.Stability
		next.Difficulty = record.ScheduleDecision.Difficulty
		next.SchedulerVersion = record.ScheduleDecision.SchedulerVersion
		next.LastReviewedAt = &record.Answer.CreatedAt
		next.Version = schedule.Version + 1
		if err := domain.ValidateSchedule(next); err != nil {
			return err
		}
		score, err := encodeJSON(record.Answer.Score)
		if err != nil {
			return err
		}
		feedback, err := encodeJSON(record.Answer.Feedback)
		if err != nil {
			return err
		}
		snapshot, err := encodeJSON(next)
		if err != nil {
			return err
		}

		if _, err := gormReviewExec(callbackCtx, database, `INSERT INTO learning.review_answer(
    id,workspace_id,session_id,card_id,question_ref,idempotency_key,user_answer,scorer_version,
    score,feedback,rating,schedule_snapshot,request_hash,created_at
) VALUES (?::uuid,?::uuid,?::uuid,?::uuid,?,?,?,?,?::jsonb,?::jsonb,?,?::jsonb,?,?)`,
			string(record.Answer.ID), string(record.Answer.WorkspaceID), string(record.Answer.SessionID), string(*record.Answer.CardID),
			record.Answer.QuestionRef, record.Answer.IdempotencyKey, record.Answer.UserAnswer, record.Answer.ScorerVersion,
			reviewJSONB(score), reviewJSONB(feedback), int(record.Answer.Rating), reviewJSONB(snapshot), record.RequestHash, record.Answer.CreatedAt.UTC()); err != nil {
			return gormReviewClassify(callbackCtx, err, domain.ErrorCodeAnswerInvalid)
		}
		rowsAffected, err := gormReviewExec(callbackCtx, database, `UPDATE learning.review_schedule
SET due_at=?,interval_days=?,stability=?,difficulty=?,last_reviewed_at=?,scheduler_version=?,version=?
WHERE workspace_id=?::uuid AND card_id=?::uuid AND version=?`,
			next.DueAt, next.IntervalDays, next.Stability, next.Difficulty, next.LastReviewedAt, next.SchedulerVersion, next.Version,
			string(schedule.WorkspaceID), string(schedule.CardID), schedule.Version)
		if err != nil {
			return gormReviewClassify(callbackCtx, err, domain.ErrorCodeScheduleConflict)
		}
		if rowsAffected != 1 {
			return conflict(domain.ErrorCodeScheduleConflict, "schedule CAS did not match")
		}
		result = reviewapp.AnswerResult{Answer: record.Answer, Schedule: next}
		callbackSucceeded = true
		return nil
	})
	if returnErr == nil {
		return result, nil
	}
	if callbackSucceeded {
		if recovered, found, recoveryErr := gormReviewLoadAnswerByKey(ctx, repository.database, record.Answer.WorkspaceID, record.Answer.IdempotencyKey, record.RequestHash); recoveryErr == nil && found {
			return recovered, nil
		}
	}
	return reviewapp.AnswerResult{}, gormReviewClassify(ctx, returnErr, domain.ErrorCodeDependencyUnavailable)
}

func gormReviewLoadSessionByKey(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, key, hash string) (domain.Session, bool, error) {
	row, err := gormReviewRawRow(ctx, database, `SELECT id::text,workspace_id::text,deck_id::text,session_type,status,config,
       idempotency_key,request_hash,started_at,ended_at
FROM learning.review_session
WHERE workspace_id=?::uuid AND idempotency_key=?`, string(workspaceID), key)
	if err != nil {
		return domain.Session{}, false, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	var session domain.Session
	var deckID *string
	var config []byte
	var status, sessionType, existingHash string
	err = row.Scan(&session.ID, &session.WorkspaceID, &deckID, &sessionType, &status, &config,
		&session.IdempotencyKey, &existingHash, &session.StartedAt, &session.EndedAt)
	if gormReviewNoRows(err) {
		return domain.Session{}, false, nil
	}
	if err != nil {
		return domain.Session{}, false, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	if existingHash != hash {
		return domain.Session{}, false, conflict(domain.ErrorCodeAnswerIdempotencyConflict, "session idempotency key is bound to another request")
	}
	if deckID != nil {
		parsed := foundation.ID(*deckID)
		session.DeckID = &parsed
	}
	session.Config = append([]byte(nil), config...)
	session.SessionType, session.Status = domain.SessionType(sessionType), domain.SessionStatus(status)
	if err := domain.ValidateSession(session); err != nil {
		return domain.Session{}, false, persistenceInvalid("persisted review session replay is invalid", err)
	}
	return session, true, nil
}

func gormReviewLoadSessionCommand(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, key, hash, commandType string) (domain.Session, bool, error) {
	receipt, found, err := gormReviewLoadCommand(ctx, database, workspaceID, key)
	if err != nil || !found {
		return domain.Session{}, false, err
	}
	if receipt.RequestHash != hash || receipt.CommandType != commandType {
		return domain.Session{}, false, conflict(domain.ErrorCodeAnswerIdempotencyConflict, "idempotency key is bound to another request")
	}
	if receipt.AggregateVersion != 1 {
		return domain.Session{}, false, persistenceInvalid("review session receipt aggregate version is invalid", nil)
	}
	var value domain.Session
	if err := json.Unmarshal(receipt.Response, &value); err != nil {
		return domain.Session{}, false, persistenceInvalid("review session receipt cannot be decoded", err)
	}
	value.IdempotencyKey = key
	if err := domain.ValidateSession(value); err != nil || value.WorkspaceID != workspaceID || value.ID != receipt.AggregateID || value.Status == domain.SessionStatusActive || value.EndedAt == nil {
		return domain.Session{}, false, persistenceInvalid("review session receipt binding is invalid", err)
	}
	if err := validateReviewSessionBinding(value); err != nil {
		return domain.Session{}, false, err
	}
	return value, true, nil
}

func gormReviewLoadAnswerByKey(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, key, hash string) (reviewapp.AnswerResult, bool, error) {
	row, err := gormReviewRawRow(ctx, database, `SELECT id::text,workspace_id::text,session_id::text,card_id::text,question_ref,
       idempotency_key,user_answer,scorer_version,score,feedback,rating,schedule_snapshot,request_hash,created_at
FROM learning.review_answer
WHERE workspace_id=?::uuid AND idempotency_key=?`, string(workspaceID), key)
	if err != nil {
		return reviewapp.AnswerResult{}, false, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	var answer domain.Answer
	var cardID *string
	var score, feedback, snapshot []byte
	var existingHash string
	var rating *int
	err = row.Scan(&answer.ID, &answer.WorkspaceID, &answer.SessionID, &cardID, &answer.QuestionRef,
		&answer.IdempotencyKey, &answer.UserAnswer, &answer.ScorerVersion, &score, &feedback, &rating, &snapshot, &existingHash, &answer.CreatedAt)
	if gormReviewNoRows(err) {
		return reviewapp.AnswerResult{}, false, nil
	}
	if err != nil {
		return reviewapp.AnswerResult{}, false, gormReviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	if existingHash != hash {
		return reviewapp.AnswerResult{}, false, conflict(domain.ErrorCodeAnswerIdempotencyConflict, "answer idempotency key is bound to another request")
	}
	if cardID != nil {
		parsed := foundation.ID(*cardID)
		answer.CardID = &parsed
	}
	if rating == nil || len(snapshot) == 0 {
		return reviewapp.AnswerResult{}, false, persistenceInvalid("legacy review answer has no replayable rating or schedule snapshot", nil)
	}
	answer.Rating = domain.Rating(*rating)
	if err := json.Unmarshal(score, &answer.Score); err != nil {
		return reviewapp.AnswerResult{}, false, persistenceInvalid("review answer score cannot be decoded", err)
	}
	if err := json.Unmarshal(feedback, &answer.Feedback); err != nil {
		return reviewapp.AnswerResult{}, false, persistenceInvalid("review answer feedback cannot be decoded", err)
	}
	var schedule domain.Schedule
	if err := json.Unmarshal(snapshot, &schedule); err != nil {
		return reviewapp.AnswerResult{}, false, persistenceInvalid("review answer schedule snapshot cannot be decoded", err)
	}
	if err := domain.ValidateAnswer(answer); err != nil {
		return reviewapp.AnswerResult{}, false, persistenceInvalid("persisted review answer replay is invalid", err)
	}
	if err := domain.ValidateSchedule(schedule); err != nil || answer.CardID == nil || schedule.WorkspaceID != answer.WorkspaceID || schedule.CardID != *answer.CardID {
		return reviewapp.AnswerResult{}, false, persistenceInvalid("review answer schedule snapshot binding is invalid", err)
	}
	session, err := gormReviewLoadSession(ctx, database, answer.WorkspaceID, answer.SessionID, false)
	if err != nil {
		return reviewapp.AnswerResult{}, false, err
	}
	if err := validateReviewSessionBinding(session); err != nil {
		return reviewapp.AnswerResult{}, false, err
	}
	return reviewapp.AnswerResult{Answer: answer, Schedule: schedule, Replayed: true}, true, nil
}
