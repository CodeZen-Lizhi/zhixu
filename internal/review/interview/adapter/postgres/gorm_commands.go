package postgres

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
	"gorm.io/gorm"
)

func (repository *GORMRepository) FindStartReplay(ctx context.Context, workspaceID foundation.ID, key, requestHash string) (interviewapp.StartResult, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return interviewapp.StartResult{}, false, err
	}
	value, found, err := gormInterviewLoadReceipt(ctx, repository.database, workspaceID, key, requestHash, "START")
	if err != nil || !found {
		return interviewapp.StartResult{}, found, err
	}
	result, err := decodeStartResult(value.Response)
	if err != nil {
		return interviewapp.StartResult{}, false, err
	}
	result.Replayed = true
	return result, true, nil
}

func (repository *GORMRepository) FindSubmitReplay(ctx context.Context, workspaceID foundation.ID, key, requestHash string) (interviewapp.SubmitTurnResult, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return interviewapp.SubmitTurnResult{}, false, err
	}
	value, found, err := gormInterviewLoadReceipt(ctx, repository.database, workspaceID, key, requestHash, "SUBMIT")
	if err != nil || !found {
		return interviewapp.SubmitTurnResult{}, found, err
	}
	result, err := decodeSubmitResult(value.Response)
	if err != nil {
		return interviewapp.SubmitTurnResult{}, false, err
	}
	result.Replayed = true
	return result, true, nil
}

func (repository *GORMRepository) FindPathStepReplay(ctx context.Context, workspaceID foundation.ID, key, requestHash string) (interviewapp.PathStepResult, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return interviewapp.PathStepResult{}, false, err
	}
	value, found, err := gormInterviewLoadReceipt(ctx, repository.database, workspaceID, key, requestHash, "PATH_STEP")
	if err != nil || !found {
		return interviewapp.PathStepResult{}, found, err
	}
	result, err := decodePathStepResult(value.Response)
	if err != nil {
		return interviewapp.PathStepResult{}, false, err
	}
	result.Replayed = true
	return result, true, nil
}

func (repository *GORMRepository) FindPathStatusReplay(ctx context.Context, workspaceID foundation.ID, key, requestHash string) (interviewapp.PathStatusResult, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return interviewapp.PathStatusResult{}, false, err
	}
	value, found, err := gormInterviewLoadReceipt(ctx, repository.database, workspaceID, key, requestHash, "PATH_STATUS")
	if err != nil || !found {
		return interviewapp.PathStatusResult{}, found, err
	}
	result, err := decodePathStatusResult(value.Response)
	if err != nil {
		return interviewapp.PathStatusResult{}, false, err
	}
	result.Replayed = true
	return result, true, nil
}

func (repository *GORMRepository) Start(ctx context.Context, record interviewapp.StartRecord) (interviewapp.StartResult, error) {
	if err := repository.ready(ctx); err != nil {
		return interviewapp.StartResult{}, err
	}
	if err := validateStartRecord(record); err != nil {
		return interviewapp.StartResult{}, err
	}
	var result interviewapp.StartResult
	err := repository.within(ctx, func(callbackCtx context.Context, transaction *gorm.DB) error {
		var err error
		result, err = gormInterviewStart(callbackCtx, transaction, record)
		return err
	})
	if err != nil {
		if replay, found, replayErr := repository.FindStartReplay(ctx, record.Session.WorkspaceID, record.IdempotencyKey, record.RequestHash); replayErr == nil && found {
			return replay, nil
		}
	}
	return result, err
}

func gormInterviewStart(ctx context.Context, transaction *gorm.DB, record interviewapp.StartRecord) (interviewapp.StartResult, error) {
	var result interviewapp.StartResult
	err := func(callbackCtx context.Context) error {
		if err := gormInterviewLockCommand(callbackCtx, transaction, record.Session.WorkspaceID, record.IdempotencyKey); err != nil {
			return err
		}
		if replay, found, err := gormInterviewLoadReceipt(callbackCtx, transaction, record.Session.WorkspaceID, record.IdempotencyKey, record.RequestHash, "START"); err != nil {
			return err
		} else if found {
			result, err = decodeStartResult(replay.Response)
			if err == nil {
				result.Replayed = true
			}
			return err
		}
		config, err := encodeJSON(record.Session.Config)
		if err != nil {
			return err
		}
		if _, err := gormInterviewExec(callbackCtx, transaction, `
			INSERT INTO learning.review_session(
				id,workspace_id,deck_id,session_type,status,config,idempotency_key,request_hash,started_at,ended_at
			) VALUES(?::uuid,?::uuid,NULL,'INTERVIEW','ACTIVE',?::jsonb,?,?,?::timestamptz,NULL)`,
			string(record.Session.ID), string(record.Session.WorkspaceID), interviewJSONB(config), record.IdempotencyKey,
			record.RequestHash, record.Session.StartedAt.UTC()); err != nil {
			return gormInterviewClassify(callbackCtx, err, domain.ErrorCodeIdempotencyConflict)
		}
		if _, err := gormInterviewExec(callbackCtx, transaction, `
			INSERT INTO learning.interview_session(
				session_id,workspace_id,domain_schema_version,version,follow_up_count,created_at,updated_at
			) VALUES(?::uuid,?::uuid,?,?,?,?::timestamptz,?::timestamptz)`,
			string(record.Session.ID), string(record.Session.WorkspaceID), domain.SchemaVersion, record.Session.Version,
			record.Session.FollowUpCount, record.Session.StartedAt.UTC(), record.Session.StartedAt.UTC()); err != nil {
			return gormInterviewClassify(callbackCtx, err, domain.ErrorCodePersistenceInvalid)
		}
		for _, question := range record.Questions {
			if err := gormInterviewInsertQuestion(callbackCtx, transaction, question); err != nil {
				return err
			}
		}
		result = interviewapp.StartResult{Session: record.Session, Questions: append([]domain.Question(nil), record.Questions...)}
		return gormInterviewInsertReceipt(callbackCtx, transaction, record.Session.WorkspaceID, record.IdempotencyKey,
			record.RequestHash, "START", record.Session.ID, result, record.Session.StartedAt)
	}(ctx)
	return result, err
}

func (repository *GORMRepository) Submit(ctx context.Context, record interviewapp.SubmitRecord) (interviewapp.SubmitTurnResult, error) {
	if err := repository.ready(ctx); err != nil {
		return interviewapp.SubmitTurnResult{}, err
	}
	if err := validateSubmitRecord(record); err != nil {
		return interviewapp.SubmitTurnResult{}, err
	}
	if record.ClaimOnly {
		if err := repository.requireClaimSession(ctx, record.WorkspaceID, record.SessionID); err != nil {
			return interviewapp.SubmitTurnResult{}, err
		}
	}
	var result interviewapp.SubmitTurnResult
	err := repository.within(ctx, func(callbackCtx context.Context, transaction *gorm.DB) error {
		if err := gormInterviewLockCommand(callbackCtx, transaction, record.WorkspaceID, record.IdempotencyKey); err != nil {
			return err
		}
		if replay, found, err := gormInterviewLoadReceipt(callbackCtx, transaction, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, "SUBMIT"); err != nil {
			return err
		} else if found {
			result, err = decodeSubmitResult(replay.Response)
			if err == nil {
				result.Replayed = true
			}
			return err
		}
		session, err := gormInterviewLoadSession(callbackCtx, transaction, record.WorkspaceID, record.SessionID, true)
		if err != nil {
			return err
		}
		if reservation, found, err := gormInterviewLoadCompletionReservationBySession(callbackCtx, transaction, record.WorkspaceID, record.SessionID, false); err != nil {
			return err
		} else if found && reservation.Status == interviewapp.CompletionReservationPending {
			return domain.ConflictError(domain.ErrorCodeCompletionPending, "interview completion is pending")
		}
		if session.Status != domain.SessionStatusActive {
			return domain.ConflictError(domain.ErrorCodeSessionClosed, "interview session is closed")
		}
		if !domain.SubmitAllowedAt(session, record.Turn.CreatedAt) {
			return domain.ConflictError(domain.ErrorCodeSessionExpired, "interview session deadline has passed")
		}
		if session.Version != record.ExpectedVersion {
			return domain.ConflictError(domain.ErrorCodeQuestionOrderConflict, "interview session version changed")
		}
		questions, err := gormInterviewLoadQuestions(callbackCtx, transaction, record.WorkspaceID, record.SessionID)
		if err != nil {
			return err
		}
		current := domain.OrderedPendingQuestion(questions)
		if current == nil || current.ID != record.Question.ID || current.Fingerprint != record.Question.Fingerprint {
			return domain.ConflictError(domain.ErrorCodeQuestionOrderConflict, "interview question order changed")
		}
		if record.FollowUp != nil && (session.FollowUpCount >= session.Config.MaxFollowUps || record.FollowUp.ParentQuestionID == nil ||
			*record.FollowUp.ParentQuestionID != current.ID || record.FollowUp.QuestionNo != current.QuestionNo ||
			record.FollowUp.FollowUpNo != current.FollowUpNo+1 || record.FollowUp.ClaimID != current.ClaimID || !domain.SameNoteSource(record.FollowUp.NoteSource, current.NoteSource)) {
			return domain.ConflictError(domain.ErrorCodeQuestionOrderConflict, "interview follow-up budget or chain changed")
		}
		if err := domain.ValidateTurn(record.Turn, *current); err != nil {
			return err
		}
		score, err := encodeJSON(record.Turn.Score)
		if err != nil {
			return err
		}
		decision, err := encodeJSON(record.Turn.Decision)
		if err != nil {
			return err
		}
		if _, err := gormInterviewExec(callbackCtx, transaction, `
			INSERT INTO learning.interview_turn(
				id,workspace_id,session_id,question_id,idempotency_key,request_hash,user_answer,score,decision,scorer_version,created_at
			) VALUES(?::uuid,?::uuid,?::uuid,?::uuid,?,?,?,?::jsonb,?::jsonb,?,?::timestamptz)`,
			string(record.Turn.ID), string(record.Turn.WorkspaceID), string(record.Turn.SessionID), string(record.Turn.QuestionID),
			record.Turn.IdempotencyKey, record.Turn.RequestHash, record.Turn.UserAnswer, interviewJSONB(score), interviewJSONB(decision),
			record.Turn.ScorerVersion, record.Turn.CreatedAt.UTC()); err != nil {
			return gormInterviewClassify(callbackCtx, err, domain.ErrorCodeIdempotencyConflict)
		}
		updated, err := gormInterviewExec(callbackCtx, transaction, `
			UPDATE learning.interview_question
			   SET status='ANSWERED',answered_at=?::timestamptz
			 WHERE workspace_id=?::uuid AND session_id=?::uuid AND id=?::uuid AND status='PENDING'`,
			record.Turn.CreatedAt.UTC(), string(record.WorkspaceID), string(record.SessionID), string(record.Question.ID))
		if err != nil {
			return gormInterviewClassify(callbackCtx, err, domain.ErrorCodePersistenceInvalid)
		}
		if updated != 1 {
			return domain.ConflictError(domain.ErrorCodeQuestionOrderConflict, "interview question was already answered")
		}
		if record.FollowUp != nil {
			if err := gormInterviewInsertQuestion(callbackCtx, transaction, *record.FollowUp); err != nil {
				return err
			}
		}
		newFollowUpCount := session.FollowUpCount
		if record.FollowUp != nil {
			newFollowUpCount++
		}
		nextSession := session
		nextSession.Version++
		nextSession.FollowUpCount = newFollowUpCount
		if err := domain.ValidateSession(nextSession); err != nil {
			return err
		}
		updated, err = gormInterviewExec(callbackCtx, transaction, `
			UPDATE learning.interview_session
			   SET version=version+1,follow_up_count=?,updated_at=?::timestamptz
			 WHERE workspace_id=?::uuid AND session_id=?::uuid AND version=?`,
			newFollowUpCount, record.Turn.CreatedAt.UTC(), string(record.WorkspaceID), string(record.SessionID), session.Version)
		if err != nil {
			return gormInterviewClassify(callbackCtx, err, domain.ErrorCodePersistenceInvalid)
		}
		if updated != 1 {
			return domain.ConflictError(domain.ErrorCodeQuestionOrderConflict, "interview session CAS did not match")
		}
		questions, err = gormInterviewLoadQuestions(callbackCtx, transaction, record.WorkspaceID, record.SessionID)
		if err != nil {
			return err
		}
		next := domain.OrderedPendingQuestion(questions)
		if !sameIDPointer(record.Turn.Decision.NextQuestionID, idFromQuestion(next)) ||
			(record.FollowUp != nil) != record.Turn.Decision.FollowUpCreated ||
			!sameIDPointer(record.Turn.Decision.FollowUpQuestionID, idFromQuestion(record.FollowUp)) {
			return domain.ConflictError(domain.ErrorCodeQuestionOrderConflict, "interview turn decision drifted")
		}
		result = interviewapp.SubmitTurnResult{Turn: record.Turn, FollowUp: cloneQuestion(record.FollowUp), NextQuestion: cloneQuestion(next)}
		return gormInterviewInsertReceipt(callbackCtx, transaction, record.WorkspaceID, record.IdempotencyKey,
			record.RequestHash, "SUBMIT", record.SessionID, result, record.Turn.CreatedAt)
	})
	if err != nil {
		if replay, found, replayErr := repository.FindSubmitReplay(ctx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash); replayErr == nil && found {
			return replay, nil
		}
	}
	return result, err
}

func (repository *GORMRepository) UpdatePathStep(ctx context.Context, record interviewapp.UpdatePathStepRecord) (interviewapp.PathStepResult, error) {
	if err := repository.ready(ctx); err != nil {
		return interviewapp.PathStepResult{}, err
	}
	if err := validatePathStepRecord(record); err != nil {
		return interviewapp.PathStepResult{}, err
	}
	if record.ClaimOnly {
		if err := repository.requireClaimPath(ctx, record.WorkspaceID, record.PathID); err != nil {
			return interviewapp.PathStepResult{}, err
		}
	}
	var result interviewapp.PathStepResult
	err := repository.within(ctx, func(callbackCtx context.Context, transaction *gorm.DB) error {
		if err := gormInterviewLockCommand(callbackCtx, transaction, record.WorkspaceID, record.IdempotencyKey); err != nil {
			return err
		}
		if replay, found, err := gormInterviewLoadReceipt(callbackCtx, transaction, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, "PATH_STEP"); err != nil {
			return err
		} else if found {
			result, err = decodePathStepResult(replay.Response)
			if err == nil {
				result.Replayed = true
			}
			return err
		}
		path, steps, err := gormInterviewLoadPath(callbackCtx, transaction, record.WorkspaceID, record.PathID, true)
		if err != nil {
			return err
		}
		if path.Version != record.ExpectedVersion || path.Status != domain.PathStatusActive {
			return domain.ConflictError(domain.ErrorCodePathInvalid, "learning path is not active at expected version")
		}
		var current *domain.PathStep
		for index := range steps {
			if steps[index].ID == record.StepID {
				value := steps[index]
				current = &value
				break
			}
		}
		if current == nil || !validStepTransition(current.Status, record.Status) {
			return domain.ConflictError(domain.ErrorCodePathInvalid, "learning path step transition is invalid")
		}
		updated, err := gormInterviewExec(callbackCtx, transaction, `
			UPDATE learning.interview_learning_path_step
			   SET status=?,version=version+1,updated_at=?::timestamptz
			 WHERE workspace_id=?::uuid AND path_id=?::uuid AND id=?::uuid AND version=?`,
			string(record.Status), record.At.UTC(), string(record.WorkspaceID), string(record.PathID), string(record.StepID), current.Version)
		if err != nil {
			return gormInterviewClassify(callbackCtx, err, domain.ErrorCodePersistenceInvalid)
		}
		if updated != 1 {
			return domain.ConflictError(domain.ErrorCodePathInvalid, "learning path step version changed")
		}
		for index := range steps {
			if steps[index].ID == record.StepID {
				steps[index].Status = record.Status
				steps[index].Version++
				steps[index].UpdatedAt = record.At.UTC()
				current = &steps[index]
			}
		}
		if domain.AllPathStepsTerminal(steps) {
			path.Status = domain.PathStatusCompleted
		}
		updated, err = gormInterviewExec(callbackCtx, transaction, `
			UPDATE learning.interview_learning_path
			   SET status=?,version=version+1,updated_at=?::timestamptz
			 WHERE workspace_id=?::uuid AND id=?::uuid AND version=?`,
			string(path.Status), record.At.UTC(), string(record.WorkspaceID), string(record.PathID), path.Version)
		if err != nil {
			return gormInterviewClassify(callbackCtx, err, domain.ErrorCodePersistenceInvalid)
		}
		if updated != 1 {
			return domain.ConflictError(domain.ErrorCodePathInvalid, "learning path version changed")
		}
		path.Version++
		path.UpdatedAt = record.At.UTC()
		result = interviewapp.PathStepResult{Path: path, Step: *current}
		return gormInterviewInsertReceipt(callbackCtx, transaction, record.WorkspaceID, record.IdempotencyKey,
			record.RequestHash, "PATH_STEP", path.SessionID, result, record.At)
	})
	if err != nil {
		if replay, found, replayErr := repository.FindPathStepReplay(ctx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash); replayErr == nil && found {
			return replay, nil
		}
	}
	return result, err
}

func (repository *GORMRepository) UpdatePathStatus(ctx context.Context, record interviewapp.UpdatePathStatusRecord) (interviewapp.PathStatusResult, error) {
	if err := repository.ready(ctx); err != nil {
		return interviewapp.PathStatusResult{}, err
	}
	if err := validatePathStatusRecord(record); err != nil {
		return interviewapp.PathStatusResult{}, err
	}
	var result interviewapp.PathStatusResult
	err := repository.within(ctx, func(callbackCtx context.Context, transaction *gorm.DB) error {
		if err := gormInterviewLockCommand(callbackCtx, transaction, record.WorkspaceID, record.IdempotencyKey); err != nil {
			return err
		}
		if replay, found, err := gormInterviewLoadReceipt(callbackCtx, transaction, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, "PATH_STATUS"); err != nil {
			return err
		} else if found {
			result, err = decodePathStatusResult(replay.Response)
			if err == nil {
				result.Replayed = true
			}
			return err
		}
		path, steps, err := gormInterviewLoadPath(callbackCtx, transaction, record.WorkspaceID, record.PathID, true)
		if err != nil {
			return err
		}
		if path.Version != record.ExpectedVersion || !validPathTransition(path.Status, record.Status) ||
			(record.Status == domain.PathStatusCompleted && !domain.AllPathStepsTerminal(steps)) {
			return domain.ConflictError(domain.ErrorCodePathInvalid, "learning path transition is invalid")
		}
		updated, err := gormInterviewExec(callbackCtx, transaction, `
			UPDATE learning.interview_learning_path
			   SET status=?,version=version+1,updated_at=?::timestamptz
			 WHERE workspace_id=?::uuid AND id=?::uuid AND version=?`,
			string(record.Status), record.At.UTC(), string(record.WorkspaceID), string(record.PathID), path.Version)
		if err != nil {
			return gormInterviewClassify(callbackCtx, err, domain.ErrorCodePersistenceInvalid)
		}
		if updated != 1 {
			return domain.ConflictError(domain.ErrorCodePathInvalid, "learning path version changed")
		}
		path.Status, path.Version, path.UpdatedAt = record.Status, path.Version+1, record.At.UTC()
		result = interviewapp.PathStatusResult{Path: path}
		return gormInterviewInsertReceipt(callbackCtx, transaction, record.WorkspaceID, record.IdempotencyKey,
			record.RequestHash, "PATH_STATUS", path.SessionID, result, record.At)
	})
	if err != nil {
		if replay, found, replayErr := repository.FindPathStatusReplay(ctx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash); replayErr == nil && found {
			return replay, nil
		}
	}
	return result, err
}
