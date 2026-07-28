package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	reviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/domain"
	"github.com/jackc/pgx/v5"
)

// StartSession 创建会话并依赖 Workspace-scoped idempotency_key 重放。
func (repository *Repository) StartSession(ctx context.Context, session domain.Session, requestHash string) (reviewapp.CommandResult[domain.Session], error) {
	if session.SessionType != domain.SessionTypeReview || session.DeckID == nil {
		return reviewapp.CommandResult[domain.Session]{}, domain.InvalidError(domain.ErrorCodeSessionInvalid, "review session requires REVIEW type and a deck")
	}
	if err := domain.ValidateSession(session); err != nil {
		return reviewapp.CommandResult[domain.Session]{}, err
	}
	if err := validateCommandBinding(session.IdempotencyKey, requestHash); err != nil {
		return reviewapp.CommandResult[domain.Session]{}, err
	}
	tx, err := repository.begin(ctx)
	if err != nil {
		return reviewapp.CommandResult[domain.Session]{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	defer rollback(tx)
	if err := lockWorkspace(ctx, tx, session.WorkspaceID); err != nil {
		return reviewapp.CommandResult[domain.Session]{}, err
	}
	var existing domain.Session
	var existingConfig []byte
	var existingStatus, existingType string
	var existingDeckID *string
	var existingHash string
	err = tx.QueryRow(ctx, `SELECT id::text,workspace_id::text,deck_id::text,session_type,status,config,idempotency_key,request_hash,started_at,ended_at FROM learning.review_session WHERE workspace_id=$1 AND idempotency_key=$2`, string(session.WorkspaceID), session.IdempotencyKey).Scan(&existing.ID, &existing.WorkspaceID, &existingDeckID, &existingType, &existingStatus, &existingConfig, &existing.IdempotencyKey, &existingHash, &existing.StartedAt, &existing.EndedAt)
	if err == nil {
		if existingHash != requestHash {
			return reviewapp.CommandResult[domain.Session]{}, conflict(domain.ErrorCodeAnswerIdempotencyConflict, "session idempotency key is bound to another request")
		}
		if existingDeckID != nil {
			parsed := foundation.ID(*existingDeckID)
			existing.DeckID = &parsed
		}
		existing.Status, existing.SessionType = domain.SessionStatus(existingStatus), domain.SessionType(existingType)
		existing.Config = append([]byte(nil), existingConfig...)
		if err := domain.ValidateSession(existing); err != nil {
			return reviewapp.CommandResult[domain.Session]{}, persistenceInvalid("persisted review session replay is invalid", err)
		}
		return reviewapp.CommandResult[domain.Session]{Value: existing, Replayed: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return reviewapp.CommandResult[domain.Session]{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	deckID := any(nil)
	if session.DeckID != nil {
		var activeDeckID string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM learning.review_deck WHERE workspace_id=$1 AND id=$2 AND status='ACTIVE' FOR UPDATE`, string(session.WorkspaceID), string(*session.DeckID)).Scan(&activeDeckID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return reviewapp.CommandResult[domain.Session]{}, conflict(domain.ErrorCodeDeckArchived, "session deck is not active")
			}
			return reviewapp.CommandResult[domain.Session]{}, classify(err, domain.ErrorCodeDependencyUnavailable)
		}
		deckID = string(*session.DeckID)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO learning.review_session(id,workspace_id,deck_id,session_type,status,config,idempotency_key,request_hash,started_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, string(session.ID), string(session.WorkspaceID), deckID, string(session.SessionType), string(session.Status), session.Config, session.IdempotencyKey, requestHash, session.StartedAt.UTC()); err != nil {
		return reviewapp.CommandResult[domain.Session]{}, classify(err, domain.ErrorCodeSessionInvalid)
	}
	if err := tx.Commit(ctx); err != nil {
		if value, found, recoveryErr := loadSessionByKey(ctx, repository.db, session.WorkspaceID, session.IdempotencyKey, requestHash); recoveryErr == nil && found {
			return reviewapp.CommandResult[domain.Session]{Value: value, Replayed: true}, nil
		}
		return reviewapp.CommandResult[domain.Session]{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	return reviewapp.CommandResult[domain.Session]{Value: session}, nil
}

// GetSession 返回 Workspace-scoped 会话。
func (repository *Repository) GetSession(ctx context.Context, workspaceID, sessionID foundation.ID) (domain.Session, error) {
	if repository == nil || nilValue(repository.db) {
		return domain.Session{}, domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "review database is unavailable")
	}
	return loadSession(ctx, repository.db, workspaceID, sessionID, false)
}

// CompleteSession 以终态结束会话，并与命令 receipt 在同一事务中持久化。
func (repository *Repository) CompleteSession(ctx context.Context, record reviewapp.CompleteSessionRecord) (reviewapp.CommandResult[domain.Session], error) {
	if err := validateCommandBinding(record.IdempotencyKey, record.RequestHash); err != nil {
		return reviewapp.CommandResult[domain.Session]{}, err
	}
	if record.Status != domain.SessionStatusCompleted && record.Status != domain.SessionStatusCancelled {
		return reviewapp.CommandResult[domain.Session]{}, invalid("session terminal status is invalid")
	}
	tx, err := repository.begin(ctx)
	if err != nil {
		return reviewapp.CommandResult[domain.Session]{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	defer rollback(tx)
	if err := lockWorkspace(ctx, tx, record.WorkspaceID); err != nil {
		return reviewapp.CommandResult[domain.Session]{}, err
	}
	if value, found, err := loadSessionCommand(ctx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, "COMPLETE_SESSION"); err != nil || found {
		if err == nil {
			err = validateReviewSessionBinding(value)
		}
		return reviewapp.CommandResult[domain.Session]{Value: value, Replayed: found}, err
	}
	session, err := loadSession(ctx, tx, record.WorkspaceID, record.SessionID, true)
	if err != nil {
		return reviewapp.CommandResult[domain.Session]{}, err
	}
	if err := validateReviewSessionBinding(session); err != nil {
		return reviewapp.CommandResult[domain.Session]{}, err
	}
	if session.Status != domain.SessionStatusActive {
		return reviewapp.CommandResult[domain.Session]{}, conflict(domain.ErrorCodeSessionClosed, "session is already closed")
	}
	at := record.At.UTC()
	if at.IsZero() {
		at = time.Now().UTC()
	}
	commandTag, err := tx.Exec(ctx, `UPDATE learning.review_session SET status=$3,ended_at=$4 WHERE workspace_id=$1 AND id=$2 AND status='ACTIVE'`, string(record.WorkspaceID), string(record.SessionID), string(record.Status), at)
	if err != nil {
		return reviewapp.CommandResult[domain.Session]{}, classify(err, domain.ErrorCodeSessionInvalid)
	}
	if commandTag.RowsAffected() != 1 {
		return reviewapp.CommandResult[domain.Session]{}, conflict(domain.ErrorCodeSessionClosed, "session is already closed")
	}
	session.Status, session.EndedAt = record.Status, &at
	response, err := encodeJSON(session)
	if err != nil {
		return reviewapp.CommandResult[domain.Session]{}, err
	}
	if err := insertCommand(ctx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, "COMPLETE_SESSION", session.ID, 1, response, at); err != nil {
		return reviewapp.CommandResult[domain.Session]{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		if value, found, recoveryErr := loadSessionCommand(ctx, repository.db, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, "COMPLETE_SESSION"); recoveryErr == nil && found {
			return reviewapp.CommandResult[domain.Session]{Value: value, Replayed: true}, nil
		}
		return reviewapp.CommandResult[domain.Session]{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	return reviewapp.CommandResult[domain.Session]{Value: session}, nil
}

// GetSchedule 返回单卡片调度状态。
func (repository *Repository) GetSchedule(ctx context.Context, workspaceID, cardID foundation.ID) (domain.Schedule, error) {
	if repository == nil || nilValue(repository.db) {
		return domain.Schedule{}, domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "review database is unavailable")
	}
	return loadSchedule(ctx, repository.db, workspaceID, cardID, false)
}

// FindAnswerReplay 在读取当前 Session、Card 或 Schedule 前返回历史答题快照。
func (repository *Repository) FindAnswerReplay(ctx context.Context, workspaceID foundation.ID, idempotencyKey, requestHash string) (reviewapp.AnswerResult, bool, error) {
	if repository == nil || nilValue(repository.db) {
		return reviewapp.AnswerResult{}, false, domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "review database is unavailable")
	}
	if err := validateCommandBinding(idempotencyKey, requestHash); err != nil {
		return reviewapp.AnswerResult{}, false, err
	}
	value, found, err := loadAnswerByKey(ctx, repository.db, workspaceID, idempotencyKey, requestHash)
	if err != nil {
		return reviewapp.AnswerResult{}, false, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	return value, found, nil
}

// SubmitAnswer 在一个事务中插入 Answer 并 CAS 更新 Schedule；重复键返回历史快照。
func (repository *Repository) SubmitAnswer(ctx context.Context, record reviewapp.SubmitAnswerRecord) (reviewapp.AnswerResult, error) {
	if err := domain.ValidateAnswer(record.Answer); err != nil {
		return reviewapp.AnswerResult{}, err
	}
	if record.Answer.CardID == nil || record.ExpectedCardVersion < 1 || !validHash(record.ExpectedCardFingerprint) || record.ExpectedScheduleVersion < 1 || !validHash(record.RequestHash) {
		return reviewapp.AnswerResult{}, invalid("answer record is invalid")
	}
	tx, err := repository.begin(ctx)
	if err != nil {
		return reviewapp.AnswerResult{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	defer rollback(tx)
	if err := lockWorkspace(ctx, tx, record.Answer.WorkspaceID); err != nil {
		return reviewapp.AnswerResult{}, err
	}
	var existingAnswer domain.Answer
	var existingScore, existingFeedback, snapshot []byte
	var existingHash string
	var existingRating *int
	err = tx.QueryRow(ctx, `SELECT id::text,workspace_id::text,session_id::text,card_id::text,question_ref,idempotency_key,user_answer,scorer_version,score,feedback,rating,schedule_snapshot,request_hash,created_at FROM learning.review_answer WHERE workspace_id=$1 AND idempotency_key=$2`, string(record.Answer.WorkspaceID), record.Answer.IdempotencyKey).Scan(&existingAnswer.ID, &existingAnswer.WorkspaceID, &existingAnswer.SessionID, &existingAnswer.CardID, &existingAnswer.QuestionRef, &existingAnswer.IdempotencyKey, &existingAnswer.UserAnswer, &existingAnswer.ScorerVersion, &existingScore, &existingFeedback, &existingRating, &snapshot, &existingHash, &existingAnswer.CreatedAt)
	if err == nil {
		if existingHash != record.RequestHash {
			return reviewapp.AnswerResult{}, conflict(domain.ErrorCodeAnswerIdempotencyConflict, "answer idempotency key is bound to another request")
		}
		if existingRating == nil || len(snapshot) == 0 {
			return reviewapp.AnswerResult{}, persistenceInvalid("legacy review answer has no replayable rating or schedule snapshot", nil)
		}
		existingAnswer.Rating = domain.Rating(*existingRating)
		if err := json.Unmarshal(existingScore, &existingAnswer.Score); err != nil {
			return reviewapp.AnswerResult{}, persistenceInvalid("review answer score cannot be decoded", err)
		}
		if err := json.Unmarshal(existingFeedback, &existingAnswer.Feedback); err != nil {
			return reviewapp.AnswerResult{}, persistenceInvalid("review answer feedback cannot be decoded", err)
		}
		var schedule domain.Schedule
		if err := json.Unmarshal(snapshot, &schedule); err != nil {
			return reviewapp.AnswerResult{}, persistenceInvalid("review answer schedule snapshot cannot be decoded", err)
		}
		if err := domain.ValidateAnswer(existingAnswer); err != nil {
			return reviewapp.AnswerResult{}, persistenceInvalid("persisted review answer replay is invalid", err)
		}
		if err := domain.ValidateSchedule(schedule); err != nil || existingAnswer.CardID == nil || schedule.WorkspaceID != existingAnswer.WorkspaceID || schedule.CardID != *existingAnswer.CardID {
			return reviewapp.AnswerResult{}, persistenceInvalid("review answer schedule snapshot binding is invalid", err)
		}
		existingSession, err := loadSession(ctx, tx, existingAnswer.WorkspaceID, existingAnswer.SessionID, false)
		if err != nil {
			return reviewapp.AnswerResult{}, err
		}
		if err := validateReviewSessionBinding(existingSession); err != nil {
			return reviewapp.AnswerResult{}, err
		}
		return reviewapp.AnswerResult{Answer: existingAnswer, Schedule: schedule, Replayed: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return reviewapp.AnswerResult{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	session, err := loadSession(ctx, tx, record.Answer.WorkspaceID, record.Answer.SessionID, true)
	if err != nil {
		return reviewapp.AnswerResult{}, err
	}
	if err := validateReviewSessionBinding(session); err != nil {
		return reviewapp.AnswerResult{}, err
	}
	if session.Status != domain.SessionStatusActive {
		return reviewapp.AnswerResult{}, conflict(domain.ErrorCodeSessionClosed, "session is not active")
	}
	card, err := loadCard(ctx, tx, record.Answer.WorkspaceID, *record.Answer.CardID, true)
	if err != nil {
		return reviewapp.AnswerResult{}, err
	}
	if card.Version != record.ExpectedCardVersion || card.Fingerprint != record.ExpectedCardFingerprint {
		return reviewapp.AnswerResult{}, conflict(domain.ErrorCodeCardStateConflict, "card version did not match scored content")
	}
	if card.Status != domain.CardStatusApproved || *session.DeckID != card.DeckID {
		return reviewapp.AnswerResult{}, conflict(domain.ErrorCodeCardInactive, "card is not active in this session")
	}
	if card.ClaimID == nil {
		return reviewapp.AnswerResult{}, domain.ConflictError(domain.ErrorCodeEvidenceStale, "card claim is missing")
	}
	if err := verifyEvidence(ctx, tx, card.WorkspaceID, *card.ClaimID, card.Evidence, true); err != nil {
		return reviewapp.AnswerResult{}, err
	}
	schedule, err := loadSchedule(ctx, tx, record.Answer.WorkspaceID, *record.Answer.CardID, true)
	if err != nil {
		return reviewapp.AnswerResult{}, err
	}
	if schedule.Paused {
		return reviewapp.AnswerResult{}, conflict(domain.ErrorCodeScheduleConflict, "card schedule is paused")
	}
	if schedule.Version != record.ExpectedScheduleVersion {
		return reviewapp.AnswerResult{}, conflict(domain.ErrorCodeScheduleConflict, "schedule version did not match")
	}
	if err := checkAnswerEligibility(ctx, tx, record.Answer.WorkspaceID, card.DeckID, *record.Answer.CardID, record.Answer.CreatedAt); err != nil {
		return reviewapp.AnswerResult{}, err
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
		return reviewapp.AnswerResult{}, err
	}
	score, err := encodeJSON(record.Answer.Score)
	if err != nil {
		return reviewapp.AnswerResult{}, err
	}
	feedback, err := encodeJSON(record.Answer.Feedback)
	if err != nil {
		return reviewapp.AnswerResult{}, err
	}
	snapshot, err = encodeJSON(next)
	if err != nil {
		return reviewapp.AnswerResult{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO learning.review_answer(id,workspace_id,session_id,card_id,question_ref,idempotency_key,user_answer,scorer_version,score,feedback,rating,schedule_snapshot,request_hash,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, string(record.Answer.ID), string(record.Answer.WorkspaceID), string(record.Answer.SessionID), string(*record.Answer.CardID), record.Answer.QuestionRef, record.Answer.IdempotencyKey, record.Answer.UserAnswer, record.Answer.ScorerVersion, score, feedback, int(record.Answer.Rating), snapshot, record.RequestHash, record.Answer.CreatedAt.UTC()); err != nil {
		return reviewapp.AnswerResult{}, classify(err, domain.ErrorCodeAnswerInvalid)
	}
	commandTag, err := tx.Exec(ctx, `UPDATE learning.review_schedule SET due_at=$4,interval_days=$5,stability=$6,difficulty=$7,last_reviewed_at=$8,scheduler_version=$9,version=$10 WHERE workspace_id=$1 AND card_id=$2 AND version=$3`, string(schedule.WorkspaceID), string(schedule.CardID), schedule.Version, next.DueAt, next.IntervalDays, next.Stability, next.Difficulty, next.LastReviewedAt, next.SchedulerVersion, next.Version)
	if err != nil {
		return reviewapp.AnswerResult{}, classify(err, domain.ErrorCodeScheduleConflict)
	}
	if commandTag.RowsAffected() != 1 {
		return reviewapp.AnswerResult{}, conflict(domain.ErrorCodeScheduleConflict, "schedule CAS did not match")
	}
	if err := tx.Commit(ctx); err != nil {
		if value, found, recoveryErr := loadAnswerByKey(ctx, repository.db, record.Answer.WorkspaceID, record.Answer.IdempotencyKey, record.RequestHash); recoveryErr == nil && found {
			return value, nil
		}
		return reviewapp.AnswerResult{}, classify(err, domain.ErrorCodeDependencyUnavailable)
	}
	return reviewapp.AnswerResult{Answer: record.Answer, Schedule: next}, nil
}

func validateReviewSessionBinding(session domain.Session) error {
	if !domain.IsDeckBoundReviewSession(session) {
		return conflict(domain.ErrorCodeSessionTypeConflict, "review operation requires a deck-bound REVIEW session")
	}
	return nil
}

func loadSessionByKey(ctx context.Context, db rowQuerier, workspaceID foundation.ID, key, hash string) (domain.Session, bool, error) {
	var session domain.Session
	var deckID *string
	var config []byte
	var status, sessionType, existingHash string
	err := db.QueryRow(ctx, `SELECT id::text,workspace_id::text,deck_id::text,session_type,status,config,idempotency_key,request_hash,started_at,ended_at FROM learning.review_session WHERE workspace_id=$1 AND idempotency_key=$2`, string(workspaceID), key).Scan(&session.ID, &session.WorkspaceID, &deckID, &sessionType, &status, &config, &session.IdempotencyKey, &existingHash, &session.StartedAt, &session.EndedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Session{}, false, nil
	}
	if err != nil {
		return domain.Session{}, false, err
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
	if err := validateReviewSessionBinding(session); err != nil {
		return domain.Session{}, false, err
	}
	return session, true, nil
}

func loadSessionCommand(ctx context.Context, db rowQuerier, workspaceID foundation.ID, key, hash, commandType string) (domain.Session, bool, error) {
	receipt, found, err := loadCommand(ctx, db, workspaceID, key)
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

func loadAnswerByKey(ctx context.Context, db rowQuerier, workspaceID foundation.ID, key, hash string) (reviewapp.AnswerResult, bool, error) {
	var answer domain.Answer
	var score, feedback, snapshot []byte
	var existingHash string
	var rating *int
	err := db.QueryRow(ctx, `SELECT id::text,workspace_id::text,session_id::text,card_id::text,question_ref,idempotency_key,user_answer,scorer_version,score,feedback,rating,schedule_snapshot,request_hash,created_at FROM learning.review_answer WHERE workspace_id=$1 AND idempotency_key=$2`, string(workspaceID), key).Scan(&answer.ID, &answer.WorkspaceID, &answer.SessionID, &answer.CardID, &answer.QuestionRef, &answer.IdempotencyKey, &answer.UserAnswer, &answer.ScorerVersion, &score, &feedback, &rating, &snapshot, &existingHash, &answer.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return reviewapp.AnswerResult{}, false, nil
	}
	if err != nil {
		return reviewapp.AnswerResult{}, false, err
	}
	if existingHash != hash {
		return reviewapp.AnswerResult{}, false, conflict(domain.ErrorCodeAnswerIdempotencyConflict, "answer idempotency key is bound to another request")
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
	session, err := loadSession(ctx, db, answer.WorkspaceID, answer.SessionID, false)
	if err != nil {
		return reviewapp.AnswerResult{}, false, err
	}
	if err := validateReviewSessionBinding(session); err != nil {
		return reviewapp.AnswerResult{}, false, err
	}
	return reviewapp.AnswerResult{Answer: answer, Schedule: schedule, Replayed: true}, true, nil
}
