package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
	"github.com/jackc/pgx/v5"
)

// Get 读取一个 Interview 的全部可恢复事实。
func (r *Repository) Get(ctx context.Context, workspaceID, sessionID foundation.ID) (interviewapp.Snapshot, error) {
	return loadSnapshot(ctx, r.db, workspaceID, sessionID, false)
}

// GetPath 读取一个 Learning Path 及其步骤。
func (r *Repository) GetPath(ctx context.Context, workspaceID, pathID foundation.ID) (interviewapp.PathSnapshot, error) {
	path, steps, err := loadPath(ctx, r.db, workspaceID, pathID, false)
	if err != nil {
		return interviewapp.PathSnapshot{}, err
	}
	return interviewapp.PathSnapshot{Path: path, Steps: steps}, nil
}

// FindStartReplay 查找 START receipt。
func (r *Repository) FindStartReplay(ctx context.Context, workspaceID foundation.ID, key, requestHash string) (interviewapp.StartResult, bool, error) {
	receipt, found, err := loadReceipt(ctx, r.db, workspaceID, key, requestHash, "START")
	if err != nil || !found {
		return interviewapp.StartResult{}, found, err
	}
	result, err := decodeStartResult(receipt.Response)
	if err != nil {
		return interviewapp.StartResult{}, false, err
	}
	result.Replayed = true
	return result, true, nil
}

// FindSubmitReplay 查找 SUBMIT receipt。
func (r *Repository) FindSubmitReplay(ctx context.Context, workspaceID foundation.ID, key, requestHash string) (interviewapp.SubmitTurnResult, bool, error) {
	receipt, found, err := loadReceipt(ctx, r.db, workspaceID, key, requestHash, "SUBMIT")
	if err != nil || !found {
		return interviewapp.SubmitTurnResult{}, found, err
	}
	result, err := decodeSubmitResult(receipt.Response)
	if err != nil {
		return interviewapp.SubmitTurnResult{}, false, err
	}
	result.Replayed = true
	return result, true, nil
}

// FindCompleteReplay 查找 COMPLETE receipt。
func (r *Repository) FindCompleteReplay(ctx context.Context, workspaceID foundation.ID, key, requestHash string) (interviewapp.CompleteResult, bool, error) {
	receipt, found, err := loadReceipt(ctx, r.db, workspaceID, key, requestHash, "COMPLETE")
	if err != nil || !found {
		return interviewapp.CompleteResult{}, found, err
	}
	result, err := decodeCompleteResult(receipt.Response)
	if err != nil {
		return interviewapp.CompleteResult{}, false, err
	}
	result.Replayed = true
	return result, true, nil
}

// BeginComplete 在 Session 行锁下优先恢复终态或同键 reservation，再冻结当前快照。
func (r *Repository) BeginComplete(ctx context.Context, record interviewapp.BeginCompleteRecord) (interviewapp.BeginCompleteResult, error) {
	if err := validateBeginCompleteRecord(record); err != nil {
		return interviewapp.BeginCompleteResult{}, err
	}
	return withTransaction(ctx, r.db, func(tx pgx.Tx) (interviewapp.BeginCompleteResult, error) {
		if err := lockCommand(ctx, tx, record.WorkspaceID, record.IdempotencyKey); err != nil {
			return interviewapp.BeginCompleteResult{}, classify(err, domain.ErrorCodeDependencyUnavailable)
		}
		session, err := loadSession(ctx, tx, record.WorkspaceID, record.SessionID, true)
		if err != nil {
			return interviewapp.BeginCompleteResult{}, err
		}
		if terminal, found, err := loadTerminalCompleteReplay(ctx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash); err != nil {
			return interviewapp.BeginCompleteResult{}, err
		} else if found {
			return interviewapp.BeginCompleteResult{Terminal: terminal}, nil
		}

		reservation, found, err := loadCompletionReservationBySession(ctx, tx, record.WorkspaceID, record.SessionID, true)
		if err != nil {
			return interviewapp.BeginCompleteResult{}, err
		}
		if found {
			if reservation.IdempotencyKey != record.IdempotencyKey {
				if reservation.Status == interviewapp.CompletionReservationPending {
					return interviewapp.BeginCompleteResult{}, domain.ConflictError(domain.ErrorCodeCompletionPending, "interview completion is reserved by another idempotency key")
				}
				return interviewapp.BeginCompleteResult{}, domain.ConflictError(domain.ErrorCodeIdempotencyConflict, "interview completion reservation belongs to another idempotency key")
			}
			if reservation.RequestHash != record.RequestHash || reservation.ManualEnd != record.ManualEnd {
				return interviewapp.BeginCompleteResult{}, domain.ConflictError(domain.ErrorCodeIdempotencyConflict, "interview completion idempotency key is bound to another request")
			}
			switch reservation.Status {
			case interviewapp.CompletionReservationCompleted:
				return interviewapp.BeginCompleteResult{}, persistenceInvalid("completed interview reservation is missing its terminal receipt", nil)
			case interviewapp.CompletionReservationPending:
				if session.Status != domain.SessionStatusActive || session.Version != reservation.SnapshotVersion {
					return interviewapp.BeginCompleteResult{}, persistenceInvalid("pending interview completion snapshot drifted", nil)
				}
			case interviewapp.CompletionReservationAbandoned:
				if session.Status != domain.SessionStatusActive {
					return interviewapp.BeginCompleteResult{}, domain.ConflictError(domain.ErrorCodeSessionClosed, "interview session is closed")
				}
				if _, err := tx.Exec(ctx, `
					UPDATE learning.interview_completion_reservation
					   SET snapshot_version=$3,artifact_digest=NULL,prepared_at=NULL,status='PENDING',
					       completed_at=NULL,abandoned_at=NULL,created_at=statement_timestamp(),updated_at=statement_timestamp()
					 WHERE workspace_id=$1 AND session_id=$2 AND status='ABANDONED'`,
					string(record.WorkspaceID), string(record.SessionID), session.Version); err != nil {
					return interviewapp.BeginCompleteResult{}, classify(err, domain.ErrorCodePersistenceInvalid)
				}
				reservation, found, err = loadCompletionReservationBySession(ctx, tx, record.WorkspaceID, record.SessionID, true)
				if err != nil || !found {
					if err == nil {
						err = persistenceInvalid("reactivated interview completion reservation is missing", nil)
					}
					return interviewapp.BeginCompleteResult{}, err
				}
			default:
				return interviewapp.BeginCompleteResult{}, persistenceInvalid("interview completion reservation status is invalid", nil)
			}
		} else {
			if existing, keyFound, err := loadCompletionReservationByKey(ctx, tx, record.WorkspaceID, record.IdempotencyKey, true); err != nil {
				return interviewapp.BeginCompleteResult{}, err
			} else if keyFound {
				_ = existing
				return interviewapp.BeginCompleteResult{}, domain.ConflictError(domain.ErrorCodeIdempotencyConflict, "interview completion idempotency key belongs to another session")
			}
			if session.Status != domain.SessionStatusActive {
				return interviewapp.BeginCompleteResult{}, domain.ConflictError(domain.ErrorCodeSessionClosed, "interview session is closed")
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO learning.interview_completion_reservation(
					workspace_id,session_id,idempotency_key,request_hash,manual_end,snapshot_version,
					artifact_digest,status,created_at,updated_at
				) VALUES($1,$2,$3,$4,$5,$6,NULL,'PENDING',statement_timestamp(),statement_timestamp())`,
				string(record.WorkspaceID), string(record.SessionID), record.IdempotencyKey,
				record.RequestHash, record.ManualEnd, session.Version); err != nil {
				return interviewapp.BeginCompleteResult{}, classify(err, domain.ErrorCodeIdempotencyConflict)
			}
			reservation, found, err = loadCompletionReservationBySession(ctx, tx, record.WorkspaceID, record.SessionID, true)
			if err != nil || !found {
				if err == nil {
					err = persistenceInvalid("created interview completion reservation is missing", nil)
				}
				return interviewapp.BeginCompleteResult{}, err
			}
		}

		snapshot, err := loadSnapshot(ctx, tx, record.WorkspaceID, record.SessionID, true)
		if err != nil {
			return interviewapp.BeginCompleteResult{}, err
		}
		if snapshot.Session.Status != domain.SessionStatusActive || snapshot.Session.Version != reservation.SnapshotVersion {
			return interviewapp.BeginCompleteResult{}, persistenceInvalid("interview completion snapshot is not frozen at the reservation version", nil)
		}
		if domain.OrderedPendingQuestion(snapshot.Questions) != nil && !record.ManualEnd {
			return interviewapp.BeginCompleteResult{}, domain.ConflictError(domain.ErrorCodeQuestionOrderConflict, "interview has unanswered questions")
		}
		return interviewapp.BeginCompleteResult{Reservation: reservation, Snapshot: snapshot}, nil
	})
}

// PrepareComplete 持久化完整 Artifact digest，并恢复同 digest 的 ORPHANED hold。
func (r *Repository) PrepareComplete(ctx context.Context, record interviewapp.PrepareCompleteRecord) (interviewapp.PrepareCompleteResult, error) {
	if err := validatePrepareCompleteRecord(record); err != nil {
		return interviewapp.PrepareCompleteResult{}, err
	}
	return withTransaction(ctx, r.db, func(tx pgx.Tx) (interviewapp.PrepareCompleteResult, error) {
		if err := lockCommand(ctx, tx, record.WorkspaceID, record.IdempotencyKey); err != nil {
			return interviewapp.PrepareCompleteResult{}, classify(err, domain.ErrorCodeDependencyUnavailable)
		}
		session, err := loadSession(ctx, tx, record.WorkspaceID, record.SessionID, true)
		if err != nil {
			return interviewapp.PrepareCompleteResult{}, err
		}
		if terminal, found, err := loadTerminalCompleteReplay(ctx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash); err != nil {
			return interviewapp.PrepareCompleteResult{}, err
		} else if found {
			return interviewapp.PrepareCompleteResult{Terminal: terminal}, nil
		}
		reservation, found, err := loadCompletionReservationBySession(ctx, tx, record.WorkspaceID, record.SessionID, true)
		if err != nil {
			return interviewapp.PrepareCompleteResult{}, err
		}
		if !found {
			return interviewapp.PrepareCompleteResult{}, persistenceInvalid("interview completion reservation is missing", nil)
		}
		if err := matchCompletionReservation(reservation, record.IdempotencyKey, record.RequestHash, record.ManualEnd, record.SnapshotVersion); err != nil {
			return interviewapp.PrepareCompleteResult{}, err
		}
		if reservation.Status == interviewapp.CompletionReservationAbandoned {
			return interviewapp.PrepareCompleteResult{}, domain.ConflictError(domain.ErrorCodeCompletionAbandoned, "interview completion reservation was abandoned")
		}
		if reservation.Status != interviewapp.CompletionReservationPending || session.Status != domain.SessionStatusActive || session.Version != reservation.SnapshotVersion {
			return interviewapp.PrepareCompleteResult{}, domain.ConflictError(domain.ErrorCodeQuestionOrderConflict, "interview completion snapshot changed")
		}
		if reservation.ArtifactDigest != "" && reservation.ArtifactDigest != record.ArtifactDigest {
			return interviewapp.PrepareCompleteResult{}, domain.ConflictError(domain.ErrorCodeCompletionArtifactConflict, "interview completion reservation has another artifact digest")
		}
		if reservation.ArtifactDigest == "" {
			if _, err := tx.Exec(ctx, `
				UPDATE learning.interview_completion_reservation
				   SET artifact_digest=$3,prepared_at=statement_timestamp(),updated_at=statement_timestamp()
				 WHERE workspace_id=$1 AND session_id=$2 AND status='PENDING' AND artifact_digest IS NULL`,
				string(record.WorkspaceID), string(record.SessionID), record.ArtifactDigest); err != nil {
				return interviewapp.PrepareCompleteResult{}, classify(err, domain.ErrorCodePersistenceInvalid)
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE learning.artifact_visibility_hold
			   SET disposition='ORPHANED'
			 WHERE workspace_id=$1 AND owner_type='INTERVIEW_COMPLETE' AND owner_id=$2
			   AND disposition='ACTIVE' AND attempt_digest IS NOT NULL AND attempt_digest<>$3`,
			string(record.WorkspaceID), string(record.SessionID), record.ArtifactDigest); err != nil {
			return interviewapp.PrepareCompleteResult{}, classify(err, domain.ErrorCodePersistenceInvalid)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE learning.artifact_visibility_hold AS candidate
			   SET disposition='ACTIVE'
			 WHERE candidate.workspace_id=$1
			   AND candidate.owner_type='INTERVIEW_COMPLETE'
			   AND candidate.owner_id=$2
			   AND candidate.disposition='ORPHANED'
			   AND candidate.attempt_digest=$3
			   AND NOT EXISTS (
			       SELECT 1
			         FROM learning.artifact_visibility_hold AS active
			        WHERE active.workspace_id=candidate.workspace_id
			          AND active.owner_type=candidate.owner_type
			          AND active.owner_id=candidate.owner_id
			          AND active.owner_role=candidate.owner_role
			          AND active.disposition='ACTIVE'
			   )`, string(record.WorkspaceID), string(record.SessionID), record.ArtifactDigest); err != nil {
			return interviewapp.PrepareCompleteResult{}, classify(err, domain.ErrorCodePersistenceInvalid)
		}
		reservation, found, err = loadCompletionReservationBySession(ctx, tx, record.WorkspaceID, record.SessionID, true)
		if err != nil || !found {
			if err == nil {
				err = persistenceInvalid("prepared interview completion reservation is missing", nil)
			}
			return interviewapp.PrepareCompleteResult{}, err
		}
		if reservation.ArtifactDigest != record.ArtifactDigest {
			return interviewapp.PrepareCompleteResult{}, persistenceInvalid("prepared interview completion digest drifted", nil)
		}
		return interviewapp.PrepareCompleteResult{Reservation: reservation}, nil
	})
}

// FindPathStepReplay 查找 PATH_STEP receipt。
func (r *Repository) FindPathStepReplay(ctx context.Context, workspaceID foundation.ID, key, requestHash string) (interviewapp.PathStepResult, bool, error) {
	receipt, found, err := loadReceipt(ctx, r.db, workspaceID, key, requestHash, "PATH_STEP")
	if err != nil || !found {
		return interviewapp.PathStepResult{}, found, err
	}
	result, err := decodePathStepResult(receipt.Response)
	if err != nil {
		return interviewapp.PathStepResult{}, false, err
	}
	result.Replayed = true
	return result, true, nil
}

// FindPathStatusReplay 查找 PATH_STATUS receipt。
func (r *Repository) FindPathStatusReplay(ctx context.Context, workspaceID foundation.ID, key, requestHash string) (interviewapp.PathStatusResult, bool, error) {
	receipt, found, err := loadReceipt(ctx, r.db, workspaceID, key, requestHash, "PATH_STATUS")
	if err != nil || !found {
		return interviewapp.PathStatusResult{}, found, err
	}
	result, err := decodePathStatusResult(receipt.Response)
	if err != nil {
		return interviewapp.PathStatusResult{}, false, err
	}
	result.Replayed = true
	return result, true, nil
}

// Start 原子写入 review_session shell、Interview Session、初始题目和 START receipt。
func (r *Repository) Start(ctx context.Context, record interviewapp.StartRecord) (interviewapp.StartResult, error) {
	if err := validateStartRecord(record); err != nil {
		return interviewapp.StartResult{}, err
	}
	result, err := withTransaction(ctx, r.db, func(tx pgx.Tx) (interviewapp.StartResult, error) {
		if err := lockCommand(ctx, tx, record.Session.WorkspaceID, record.IdempotencyKey); err != nil {
			return interviewapp.StartResult{}, classify(err, domain.ErrorCodeDependencyUnavailable)
		}
		if receipt, found, err := loadReceipt(ctx, tx, record.Session.WorkspaceID, record.IdempotencyKey, record.RequestHash, "START"); err != nil {
			return interviewapp.StartResult{}, err
		} else if found {
			result, err := decodeStartResult(receipt.Response)
			if err != nil {
				return interviewapp.StartResult{}, err
			}
			result.Replayed = true
			return result, nil
		}
		config, err := encodeJSON(record.Session.Config)
		if err != nil {
			return interviewapp.StartResult{}, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO learning.review_session(id,workspace_id,deck_id,session_type,status,config,idempotency_key,request_hash,started_at,ended_at)
			VALUES($1,$2,NULL,'INTERVIEW','ACTIVE',$3,$4,$5,$6,NULL)`,
			string(record.Session.ID), string(record.Session.WorkspaceID), config, record.IdempotencyKey, record.RequestHash, record.Session.StartedAt.UTC()); err != nil {
			return interviewapp.StartResult{}, classify(err, domain.ErrorCodeIdempotencyConflict)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO learning.interview_session(session_id,workspace_id,domain_schema_version,version,follow_up_count,created_at,updated_at)
			VALUES($1,$2,$3,$4,$5,$6,$7)`, string(record.Session.ID), string(record.Session.WorkspaceID), domain.SchemaVersion, record.Session.Version, record.Session.FollowUpCount, record.Session.StartedAt.UTC(), record.Session.StartedAt.UTC()); err != nil {
			return interviewapp.StartResult{}, classify(err, domain.ErrorCodePersistenceInvalid)
		}
		for _, question := range record.Questions {
			if err := insertQuestion(ctx, tx, question); err != nil {
				return interviewapp.StartResult{}, err
			}
		}
		result := interviewapp.StartResult{Session: record.Session, Questions: append([]domain.Question(nil), record.Questions...)}
		if err := insertReceipt(ctx, tx, record.Session.WorkspaceID, record.IdempotencyKey, record.RequestHash, "START", record.Session.ID, result, record.Session.StartedAt); err != nil {
			return interviewapp.StartResult{}, err
		}
		return result, nil
	})
	if err != nil {
		if replay, found, replayErr := r.FindStartReplay(ctx, record.Session.WorkspaceID, record.IdempotencyKey, record.RequestHash); replayErr == nil && found {
			return replay, nil
		}
	}
	return result, err
}

// Submit 原子保存 Interview Turn、可选追问和 SUBMIT receipt，不写 review_answer/review_schedule。
func (r *Repository) Submit(ctx context.Context, record interviewapp.SubmitRecord) (interviewapp.SubmitTurnResult, error) {
	if err := validateSubmitRecord(record); err != nil {
		return interviewapp.SubmitTurnResult{}, err
	}
	result, err := withTransaction(ctx, r.db, func(tx pgx.Tx) (interviewapp.SubmitTurnResult, error) {
		if err := lockCommand(ctx, tx, record.WorkspaceID, record.IdempotencyKey); err != nil {
			return interviewapp.SubmitTurnResult{}, classify(err, domain.ErrorCodeDependencyUnavailable)
		}
		if receipt, found, err := loadReceipt(ctx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, "SUBMIT"); err != nil {
			return interviewapp.SubmitTurnResult{}, err
		} else if found {
			result, err := decodeSubmitResult(receipt.Response)
			if err != nil {
				return interviewapp.SubmitTurnResult{}, err
			}
			result.Replayed = true
			return result, nil
		}
		session, err := loadSession(ctx, tx, record.WorkspaceID, record.SessionID, true)
		if err != nil {
			return interviewapp.SubmitTurnResult{}, err
		}
		if reservation, found, err := loadCompletionReservationBySession(ctx, tx, record.WorkspaceID, record.SessionID, false); err != nil {
			return interviewapp.SubmitTurnResult{}, err
		} else if found && reservation.Status == interviewapp.CompletionReservationPending {
			return interviewapp.SubmitTurnResult{}, domain.ConflictError(domain.ErrorCodeCompletionPending, "interview completion is pending")
		}
		if session.Status != domain.SessionStatusActive {
			return interviewapp.SubmitTurnResult{}, domain.ConflictError(domain.ErrorCodeSessionClosed, "interview session is closed")
		}
		if !domain.SubmitAllowedAt(session, record.Turn.CreatedAt) {
			return interviewapp.SubmitTurnResult{}, domain.ConflictError(domain.ErrorCodeSessionExpired, "interview session deadline has passed")
		}
		if session.Version != record.ExpectedVersion {
			return interviewapp.SubmitTurnResult{}, domain.ConflictError(domain.ErrorCodeQuestionOrderConflict, "interview session version changed")
		}
		questions, err := loadQuestions(ctx, tx, record.WorkspaceID, record.SessionID)
		if err != nil {
			return interviewapp.SubmitTurnResult{}, err
		}
		current := domain.OrderedPendingQuestion(questions)
		if current == nil || current.ID != record.Question.ID || current.Fingerprint != record.Question.Fingerprint {
			return interviewapp.SubmitTurnResult{}, domain.ConflictError(domain.ErrorCodeQuestionOrderConflict, "interview question order changed")
		}
		if record.FollowUp != nil {
			if session.FollowUpCount >= session.Config.MaxFollowUps || record.FollowUp.ParentQuestionID == nil || *record.FollowUp.ParentQuestionID != current.ID ||
				record.FollowUp.QuestionNo != current.QuestionNo || record.FollowUp.FollowUpNo != current.FollowUpNo+1 || record.FollowUp.ClaimID != current.ClaimID {
				return interviewapp.SubmitTurnResult{}, domain.ConflictError(domain.ErrorCodeQuestionOrderConflict, "interview follow-up budget or chain changed")
			}
		}
		if err := domain.ValidateTurn(record.Turn, *current); err != nil {
			return interviewapp.SubmitTurnResult{}, err
		}
		score, err := encodeJSON(record.Turn.Score)
		if err != nil {
			return interviewapp.SubmitTurnResult{}, err
		}
		decision, err := encodeJSON(record.Turn.Decision)
		if err != nil {
			return interviewapp.SubmitTurnResult{}, err
		}
		if _, err := tx.Exec(ctx, `
				INSERT INTO learning.interview_turn(id,workspace_id,session_id,question_id,idempotency_key,request_hash,user_answer,score,decision,scorer_version,created_at)
				VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, string(record.Turn.ID), string(record.Turn.WorkspaceID), string(record.Turn.SessionID), string(record.Turn.QuestionID), record.Turn.IdempotencyKey, record.Turn.RequestHash, record.Turn.UserAnswer, score, decision, record.Turn.ScorerVersion, record.Turn.CreatedAt.UTC()); err != nil {
			return interviewapp.SubmitTurnResult{}, classify(err, domain.ErrorCodeIdempotencyConflict)
		}
		updated, err := tx.Exec(ctx, `UPDATE learning.interview_question SET status='ANSWERED',answered_at=$4 WHERE workspace_id=$1 AND session_id=$2 AND id=$3 AND status='PENDING'`, string(record.WorkspaceID), string(record.SessionID), string(record.Question.ID), record.Turn.CreatedAt.UTC())
		if err != nil {
			return interviewapp.SubmitTurnResult{}, classify(err, domain.ErrorCodePersistenceInvalid)
		}
		if updated.RowsAffected() != 1 {
			return interviewapp.SubmitTurnResult{}, domain.ConflictError(domain.ErrorCodeQuestionOrderConflict, "interview question was already answered")
		}
		if record.FollowUp != nil {
			if err := insertQuestion(ctx, tx, *record.FollowUp); err != nil {
				return interviewapp.SubmitTurnResult{}, err
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
			return interviewapp.SubmitTurnResult{}, err
		}
		updated, err = tx.Exec(ctx, `UPDATE learning.interview_session SET version=version+1,follow_up_count=$4,updated_at=$5 WHERE workspace_id=$1 AND session_id=$2 AND version=$3`, string(record.WorkspaceID), string(record.SessionID), session.Version, newFollowUpCount, record.Turn.CreatedAt.UTC())
		if err != nil {
			return interviewapp.SubmitTurnResult{}, classify(err, domain.ErrorCodePersistenceInvalid)
		}
		if updated.RowsAffected() != 1 {
			return interviewapp.SubmitTurnResult{}, domain.ConflictError(domain.ErrorCodeQuestionOrderConflict, "interview session CAS did not match")
		}
		questions, err = loadQuestions(ctx, tx, record.WorkspaceID, record.SessionID)
		if err != nil {
			return interviewapp.SubmitTurnResult{}, err
		}
		next := domain.OrderedPendingQuestion(questions)
		if !sameIDPointer(record.Turn.Decision.NextQuestionID, idFromQuestion(next)) || (record.FollowUp != nil) != record.Turn.Decision.FollowUpCreated || !sameIDPointer(record.Turn.Decision.FollowUpQuestionID, idFromQuestion(record.FollowUp)) {
			return interviewapp.SubmitTurnResult{}, domain.ConflictError(domain.ErrorCodeQuestionOrderConflict, "interview turn decision drifted")
		}
		result := interviewapp.SubmitTurnResult{Turn: record.Turn, FollowUp: cloneQuestion(record.FollowUp), NextQuestion: cloneQuestion(next)}
		if err := insertReceipt(ctx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, "SUBMIT", record.SessionID, result, record.Turn.CreatedAt); err != nil {
			return interviewapp.SubmitTurnResult{}, err
		}
		return result, nil
	})
	if err != nil {
		if replay, found, replayErr := r.FindSubmitReplay(ctx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash); replayErr == nil && found {
			return replay, nil
		}
	}
	return result, err
}

// Complete 原子关闭 Session、写 Report/Path/steps 与 COMPLETE receipt。
func (r *Repository) Complete(ctx context.Context, record interviewapp.CompleteRecord) (interviewapp.CompleteResult, error) {
	if err := validateCompleteRecord(record); err != nil {
		return interviewapp.CompleteResult{}, err
	}
	result, err := withTransaction(ctx, r.db, func(tx pgx.Tx) (interviewapp.CompleteResult, error) {
		if err := lockCommand(ctx, tx, record.WorkspaceID, record.IdempotencyKey); err != nil {
			return interviewapp.CompleteResult{}, classify(err, domain.ErrorCodeDependencyUnavailable)
		}
		session, err := loadSession(ctx, tx, record.WorkspaceID, record.SessionID, true)
		if err != nil {
			return interviewapp.CompleteResult{}, err
		}
		if receipt, found, err := loadReceipt(ctx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, "COMPLETE"); err != nil {
			return interviewapp.CompleteResult{}, err
		} else if found {
			result, err := decodeCompleteResult(receipt.Response)
			if err != nil {
				return interviewapp.CompleteResult{}, err
			}
			result.Replayed = true
			return result, nil
		}
		reservation, found, err := loadCompletionReservationBySession(ctx, tx, record.WorkspaceID, record.SessionID, true)
		if err != nil {
			return interviewapp.CompleteResult{}, err
		}
		if !found {
			return interviewapp.CompleteResult{}, persistenceInvalid("interview completion reservation is missing", nil)
		}
		if err := matchCompletionReservation(reservation, record.IdempotencyKey, record.RequestHash, record.ManualEnd, record.ExpectedVersion); err != nil {
			return interviewapp.CompleteResult{}, err
		}
		if reservation.Status == interviewapp.CompletionReservationAbandoned {
			return interviewapp.CompleteResult{}, domain.ConflictError(domain.ErrorCodeCompletionAbandoned, "interview completion reservation was abandoned")
		}
		if reservation.Status != interviewapp.CompletionReservationPending || reservation.ArtifactDigest != record.ArtifactDigest {
			return interviewapp.CompleteResult{}, domain.ConflictError(domain.ErrorCodeCompletionArtifactConflict, "interview completion artifact digest changed")
		}
		if session.Status != domain.SessionStatusActive {
			return interviewapp.CompleteResult{}, domain.ConflictError(domain.ErrorCodeSessionClosed, "interview session is closed")
		}
		if session.Version != reservation.SnapshotVersion || session.Version != record.ExpectedVersion {
			return interviewapp.CompleteResult{}, domain.ConflictError(domain.ErrorCodeQuestionOrderConflict, "interview session version changed")
		}
		questions, err := loadQuestions(ctx, tx, record.WorkspaceID, record.SessionID)
		if err != nil {
			return interviewapp.CompleteResult{}, err
		}
		if domain.OrderedPendingQuestion(questions) != nil && !record.ManualEnd {
			return interviewapp.CompleteResult{}, domain.ConflictError(domain.ErrorCodeQuestionOrderConflict, "interview has unanswered questions")
		}
		if record.ManualEnd {
			if _, err := tx.Exec(ctx, `UPDATE learning.interview_question SET status='SKIPPED' WHERE workspace_id=$1 AND session_id=$2 AND status='PENDING'`, string(record.WorkspaceID), string(record.SessionID)); err != nil {
				return interviewapp.CompleteResult{}, classify(err, domain.ErrorCodePersistenceInvalid)
			}
		}
		if err := reconcileCompletionArtifactVisibility(ctx, tx, record); err != nil {
			return interviewapp.CompleteResult{}, err
		}
		reportRaw, err := encodeJSON(record.Report)
		if err != nil {
			return interviewapp.CompleteResult{}, err
		}
		reportRaw, err = canonicalJSON(reportRaw)
		if err != nil {
			return interviewapp.CompleteResult{}, persistenceInvalid("canonicalize interview report", err)
		}
		reportHash := hashBytes(reportRaw)
		if _, err := tx.Exec(ctx, `
			INSERT INTO learning.interview_report(id,workspace_id,session_id,domain_schema_version,report,report_hash,artifact_id,artifact_revision_id,artifact_version,created_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, string(record.Report.ID), string(record.Report.WorkspaceID), string(record.Report.SessionID), record.Report.SchemaVersion, reportRaw, reportHash, string(record.Report.Artifact.ArtifactID), string(record.Report.Artifact.RevisionID), record.Report.Artifact.ArtifactVersion, record.Report.CreatedAt.UTC()); err != nil {
			return interviewapp.CompleteResult{}, classify(err, domain.ErrorCodePersistenceInvalid)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO learning.interview_learning_path(id,workspace_id,session_id,report_id,artifact_id,artifact_revision_id,artifact_version,status,version,created_at,updated_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, string(record.Path.ID), string(record.Path.WorkspaceID), string(record.Path.SessionID), string(record.Path.ReportID), string(record.Path.Artifact.ArtifactID), string(record.Path.Artifact.RevisionID), record.Path.Artifact.ArtifactVersion, string(record.Path.Status), record.Path.Version, record.Path.CreatedAt.UTC(), record.Path.UpdatedAt.UTC()); err != nil {
			return interviewapp.CompleteResult{}, classify(err, domain.ErrorCodePersistenceInvalid)
		}
		for _, step := range record.Steps {
			if err := insertPathStep(ctx, tx, step); err != nil {
				return interviewapp.CompleteResult{}, err
			}
		}
		updated, err := tx.Exec(ctx, `UPDATE learning.review_session SET status='COMPLETED',ended_at=$3 WHERE workspace_id=$1 AND id=$2 AND status='ACTIVE'`, string(record.WorkspaceID), string(record.SessionID), record.At.UTC())
		if err != nil {
			return interviewapp.CompleteResult{}, classify(err, domain.ErrorCodePersistenceInvalid)
		}
		if updated.RowsAffected() != 1 {
			return interviewapp.CompleteResult{}, domain.ConflictError(domain.ErrorCodeSessionClosed, "interview review shell is no longer active")
		}
		updated, err = tx.Exec(ctx, `UPDATE learning.interview_session SET version=version+1,updated_at=$4 WHERE workspace_id=$1 AND session_id=$2 AND version=$3`, string(record.WorkspaceID), string(record.SessionID), session.Version, record.At.UTC())
		if err != nil {
			return interviewapp.CompleteResult{}, classify(err, domain.ErrorCodePersistenceInvalid)
		}
		if updated.RowsAffected() != 1 {
			return interviewapp.CompleteResult{}, domain.ConflictError(domain.ErrorCodeQuestionOrderConflict, "interview session CAS did not match")
		}
		if err := releaseCompletionArtifactVisibility(ctx, tx, record); err != nil {
			return interviewapp.CompleteResult{}, err
		}
		endedAt := record.At.UTC()
		session.Status = domain.SessionStatusCompleted
		session.Version++
		session.EndedAt = &endedAt
		result := interviewapp.CompleteResult{Session: session, Report: record.Report, Path: record.Path, Steps: append([]domain.PathStep(nil), record.Steps...)}
		if err := insertReceipt(ctx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, "COMPLETE", record.SessionID, result, record.At); err != nil {
			return interviewapp.CompleteResult{}, err
		}
		updated, err = tx.Exec(ctx, `
			UPDATE learning.interview_completion_reservation
			   SET report_id=$5,report_artifact_id=$6,report_artifact_revision_id=$7,report_artifact_version=$8,
			       path_id=$9,path_artifact_id=$10,path_artifact_revision_id=$11,path_artifact_version=$12,
			       status='COMPLETED',completed_at=statement_timestamp(),updated_at=statement_timestamp()
			 WHERE workspace_id=$1 AND session_id=$2 AND status='PENDING'
			   AND snapshot_version=$3 AND artifact_digest=$4`,
			string(record.WorkspaceID), string(record.SessionID), record.ExpectedVersion, record.ArtifactDigest,
			string(record.Report.ID), string(record.Report.Artifact.ArtifactID), string(record.Report.Artifact.RevisionID), record.Report.Artifact.ArtifactVersion,
			string(record.Path.ID), string(record.Path.Artifact.ArtifactID), string(record.Path.Artifact.RevisionID), record.Path.Artifact.ArtifactVersion)
		if err != nil {
			return interviewapp.CompleteResult{}, classify(err, domain.ErrorCodePersistenceInvalid)
		}
		if updated.RowsAffected() != 1 {
			return interviewapp.CompleteResult{}, domain.ConflictError(domain.ErrorCodeCompletionArtifactConflict, "interview completion reservation changed")
		}
		return result, nil
	})
	if err != nil {
		if replay, found, replayErr := r.FindCompleteReplay(ctx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash); replayErr == nil && found {
			return replay, nil
		}
	}
	return result, err
}

// reconcileCompletionArtifactVisibility 绑定当前尝试并精确验证两个 ACTIVE hold。
func reconcileCompletionArtifactVisibility(ctx context.Context, tx pgx.Tx, record interviewapp.CompleteRecord) error {
	if _, err := tx.Exec(ctx, `
		UPDATE learning.artifact_visibility_hold AS candidate
		   SET disposition='ACTIVE'
		 WHERE candidate.workspace_id=$1
		   AND candidate.owner_type='INTERVIEW_COMPLETE'
		   AND candidate.owner_id=$2
		   AND candidate.disposition='ORPHANED'
		   AND candidate.attempt_digest=$5
		   AND ((candidate.owner_role='REPORT' AND candidate.artifact_id=$3) OR (candidate.owner_role='PATH' AND candidate.artifact_id=$4))
		   AND NOT EXISTS (
		       SELECT 1 FROM learning.artifact_visibility_hold AS active
		        WHERE active.workspace_id=candidate.workspace_id
		          AND active.owner_type=candidate.owner_type
		          AND active.owner_id=candidate.owner_id
		          AND active.owner_role=candidate.owner_role
		          AND active.disposition='ACTIVE'
		   )`, string(record.WorkspaceID), string(record.SessionID), string(record.Report.Artifact.ArtifactID),
		string(record.Path.Artifact.ArtifactID), record.ArtifactDigest); err != nil {
		return classify(err, domain.ErrorCodePersistenceInvalid)
	}
	rows, err := tx.Query(ctx, `
		SELECT artifact_id::text,owner_role,attempt_digest
		  FROM learning.artifact_visibility_hold
		 WHERE workspace_id=$1 AND owner_type='INTERVIEW_COMPLETE' AND owner_id=$2 AND disposition='ACTIVE'
		 ORDER BY owner_role,artifact_id
		 FOR UPDATE`, string(record.WorkspaceID), string(record.SessionID))
	if err != nil {
		return classify(err, domain.ErrorCodePersistenceInvalid)
	}
	defer rows.Close()
	matched := 0
	for rows.Next() {
		var artifactID, role string
		var digest *string
		if err := rows.Scan(&artifactID, &role, &digest); err != nil {
			return classify(err, domain.ErrorCodePersistenceInvalid)
		}
		if digest == nil || *digest != record.ArtifactDigest ||
			(role == "REPORT" && artifactID != string(record.Report.Artifact.ArtifactID)) ||
			(role == "PATH" && artifactID != string(record.Path.Artifact.ArtifactID)) ||
			(role != "REPORT" && role != "PATH") {
			return persistenceInvalid("interview completion has a mismatched active artifact visibility hold", nil)
		}
		matched++
	}
	if err := rows.Err(); err != nil {
		return classify(err, domain.ErrorCodePersistenceInvalid)
	}
	if matched != 2 {
		return persistenceInvalid("interview completion requires exactly two active artifact visibility holds", nil)
	}
	return nil
}

// releaseCompletionArtifactVisibility 只删除当前 digest 精确绑定的两个 ACTIVE hold。
func releaseCompletionArtifactVisibility(ctx context.Context, tx pgx.Tx, record interviewapp.CompleteRecord) error {
	deleted, err := tx.Exec(ctx, `
		DELETE FROM learning.artifact_visibility_hold
		WHERE workspace_id=$1
		  AND owner_type='INTERVIEW_COMPLETE'
		  AND owner_id=$2
		  AND disposition='ACTIVE'
		  AND attempt_digest=$5
		  AND (
			(owner_role='REPORT' AND artifact_id=$3)
			OR (owner_role='PATH' AND artifact_id=$4)
		  )`,
		string(record.WorkspaceID), string(record.SessionID),
		string(record.Report.Artifact.ArtifactID), string(record.Path.Artifact.ArtifactID), record.ArtifactDigest)
	if err != nil {
		return classify(err, domain.ErrorCodePersistenceInvalid)
	}
	if deleted.RowsAffected() != 2 {
		return persistenceInvalid("interview completion artifact visibility holds are incomplete", nil)
	}
	return nil
}

// UpdatePathStep 原子更新一条步骤和它所属 Path 的版本。
func (r *Repository) UpdatePathStep(ctx context.Context, record interviewapp.UpdatePathStepRecord) (interviewapp.PathStepResult, error) {
	if err := validatePathStepRecord(record); err != nil {
		return interviewapp.PathStepResult{}, err
	}
	result, err := withTransaction(ctx, r.db, func(tx pgx.Tx) (interviewapp.PathStepResult, error) {
		if err := lockCommand(ctx, tx, record.WorkspaceID, record.IdempotencyKey); err != nil {
			return interviewapp.PathStepResult{}, classify(err, domain.ErrorCodeDependencyUnavailable)
		}
		if receipt, found, err := loadReceipt(ctx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, "PATH_STEP"); err != nil {
			return interviewapp.PathStepResult{}, err
		} else if found {
			result, err := decodePathStepResult(receipt.Response)
			if err != nil {
				return interviewapp.PathStepResult{}, err
			}
			result.Replayed = true
			return result, nil
		}
		path, steps, err := loadPath(ctx, tx, record.WorkspaceID, record.PathID, true)
		if err != nil {
			return interviewapp.PathStepResult{}, err
		}
		if path.Version != record.ExpectedVersion || path.Status != domain.PathStatusActive {
			return interviewapp.PathStepResult{}, domain.ConflictError(domain.ErrorCodePathInvalid, "learning path is not active at expected version")
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
			return interviewapp.PathStepResult{}, domain.ConflictError(domain.ErrorCodePathInvalid, "learning path step transition is invalid")
		}
		updated, err := tx.Exec(ctx, `UPDATE learning.interview_learning_path_step SET status=$5,version=version+1,updated_at=$6 WHERE workspace_id=$1 AND path_id=$2 AND id=$3 AND version=$4`, string(record.WorkspaceID), string(record.PathID), string(record.StepID), current.Version, string(record.Status), record.At.UTC())
		if err != nil {
			return interviewapp.PathStepResult{}, classify(err, domain.ErrorCodePersistenceInvalid)
		}
		if updated.RowsAffected() != 1 {
			return interviewapp.PathStepResult{}, domain.ConflictError(domain.ErrorCodePathInvalid, "learning path step version changed")
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
		updated, err = tx.Exec(ctx, `UPDATE learning.interview_learning_path SET status=$4,version=version+1,updated_at=$5 WHERE workspace_id=$1 AND id=$2 AND version=$3`, string(record.WorkspaceID), string(record.PathID), path.Version, string(path.Status), record.At.UTC())
		if err != nil {
			return interviewapp.PathStepResult{}, classify(err, domain.ErrorCodePersistenceInvalid)
		}
		if updated.RowsAffected() != 1 {
			return interviewapp.PathStepResult{}, domain.ConflictError(domain.ErrorCodePathInvalid, "learning path version changed")
		}
		path.Version++
		path.UpdatedAt = record.At.UTC()
		result := interviewapp.PathStepResult{Path: path, Step: *current}
		if err := insertReceipt(ctx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, "PATH_STEP", path.SessionID, result, record.At); err != nil {
			return interviewapp.PathStepResult{}, err
		}
		return result, nil
	})
	if err != nil {
		if replay, found, replayErr := r.FindPathStepReplay(ctx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash); replayErr == nil && found {
			return replay, nil
		}
	}
	return result, err
}

// UpdatePathStatus 原子暂停、恢复或完成一条 Learning Path。
func (r *Repository) UpdatePathStatus(ctx context.Context, record interviewapp.UpdatePathStatusRecord) (interviewapp.PathStatusResult, error) {
	if err := validatePathStatusRecord(record); err != nil {
		return interviewapp.PathStatusResult{}, err
	}
	result, err := withTransaction(ctx, r.db, func(tx pgx.Tx) (interviewapp.PathStatusResult, error) {
		if err := lockCommand(ctx, tx, record.WorkspaceID, record.IdempotencyKey); err != nil {
			return interviewapp.PathStatusResult{}, classify(err, domain.ErrorCodeDependencyUnavailable)
		}
		if receipt, found, err := loadReceipt(ctx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, "PATH_STATUS"); err != nil {
			return interviewapp.PathStatusResult{}, err
		} else if found {
			result, err := decodePathStatusResult(receipt.Response)
			if err != nil {
				return interviewapp.PathStatusResult{}, err
			}
			result.Replayed = true
			return result, nil
		}
		path, steps, err := loadPath(ctx, tx, record.WorkspaceID, record.PathID, true)
		if err != nil {
			return interviewapp.PathStatusResult{}, err
		}
		if path.Version != record.ExpectedVersion || !validPathTransition(path.Status, record.Status) ||
			(record.Status == domain.PathStatusCompleted && !domain.AllPathStepsTerminal(steps)) {
			return interviewapp.PathStatusResult{}, domain.ConflictError(domain.ErrorCodePathInvalid, "learning path transition is invalid")
		}
		updated, err := tx.Exec(ctx, `UPDATE learning.interview_learning_path SET status=$4,version=version+1,updated_at=$5 WHERE workspace_id=$1 AND id=$2 AND version=$3`, string(record.WorkspaceID), string(record.PathID), path.Version, string(record.Status), record.At.UTC())
		if err != nil {
			return interviewapp.PathStatusResult{}, classify(err, domain.ErrorCodePersistenceInvalid)
		}
		if updated.RowsAffected() != 1 {
			return interviewapp.PathStatusResult{}, domain.ConflictError(domain.ErrorCodePathInvalid, "learning path version changed")
		}
		path.Status, path.Version, path.UpdatedAt = record.Status, path.Version+1, record.At.UTC()
		result := interviewapp.PathStatusResult{Path: path}
		if err := insertReceipt(ctx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, "PATH_STATUS", path.SessionID, result, record.At); err != nil {
			return interviewapp.PathStatusResult{}, err
		}
		return result, nil
	})
	if err != nil {
		if replay, found, replayErr := r.FindPathStatusReplay(ctx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash); replayErr == nil && found {
			return replay, nil
		}
	}
	return result, err
}

func insertQuestion(ctx context.Context, tx pgx.Tx, question domain.Question) error {
	if err := domain.ValidateQuestion(question); err != nil {
		return err
	}
	answerPoints, err := encodeJSON(question.AnswerPoints)
	if err != nil {
		return err
	}
	evidence, err := encodeJSON(question.Evidence)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO learning.interview_question(id,workspace_id,session_id,question_no,follow_up_no,parent_question_id,claim_id,topic_id,prompt,answer_points,evidence,status,fingerprint,created_at,answered_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, string(question.ID), string(question.WorkspaceID), string(question.SessionID), question.QuestionNo, question.FollowUpNo, nullableID(question.ParentQuestionID), string(question.ClaimID), nullableID(question.TopicID), question.Prompt, answerPoints, evidence, string(question.Status), question.Fingerprint, question.CreatedAt.UTC(), nullableTime(question.AnsweredAt))
	if err != nil {
		return classify(err, domain.ErrorCodePersistenceInvalid)
	}
	return nil
}

func insertPathStep(ctx context.Context, tx pgx.Tx, step domain.PathStep) error {
	if err := domain.ValidatePathStep(step); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO learning.interview_learning_path_step(id,workspace_id,path_id,step_no,claim_id,topic_id,source_version_id,source_span_id,evidence_hash,title,rationale,status,version,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, string(step.ID), string(step.WorkspaceID), string(step.PathID), step.StepNo, string(step.ClaimID), nullableID(step.TopicID), string(step.SourceVersionID), string(step.SourceSpanID), step.EvidenceHash, step.Title, step.Rationale, string(step.Status), step.Version, step.CreatedAt.UTC(), step.UpdatedAt.UTC())
	if err != nil {
		return classify(err, domain.ErrorCodePersistenceInvalid)
	}
	return nil
}

func validateStartRecord(record interviewapp.StartRecord) error {
	if err := domain.ValidateSession(record.Session); err != nil {
		return err
	}
	if record.Session.Status != domain.SessionStatusActive || len(record.Questions) != record.Session.Config.QuestionCount || !validHash(record.RequestHash) {
		return domain.InvalidError(domain.ErrorCodeConfigInvalid, "interview start record is invalid")
	}
	for index, question := range record.Questions {
		if question.SessionID != record.Session.ID || question.WorkspaceID != record.Session.WorkspaceID || question.QuestionNo != index+1 || question.FollowUpNo != 0 {
			return domain.InvalidError(domain.ErrorCodeQuestionInvalid, "interview initial question binding is invalid")
		}
		if err := domain.ValidateQuestion(question); err != nil {
			return err
		}
	}
	return domain.ValidateIdempotencyKey(record.IdempotencyKey)
}

func validateSubmitRecord(record interviewapp.SubmitRecord) error {
	if !validID(record.WorkspaceID) || !validID(record.SessionID) || record.ExpectedVersion < 1 || !validHash(record.RequestHash) || record.Question.WorkspaceID != record.WorkspaceID || record.Question.SessionID != record.SessionID || record.Turn.WorkspaceID != record.WorkspaceID || record.Turn.SessionID != record.SessionID {
		return domain.InvalidError(domain.ErrorCodeTurnInvalid, "interview submit record is invalid")
	}
	if record.FollowUp != nil && (record.FollowUp.WorkspaceID != record.WorkspaceID || record.FollowUp.SessionID != record.SessionID) {
		return domain.InvalidError(domain.ErrorCodeQuestionInvalid, "interview follow-up binding is invalid")
	}
	return domain.ValidateIdempotencyKey(record.IdempotencyKey)
}

// validateBeginCompleteRecord 验证 reservation Begin 的完整请求绑定。
func validateBeginCompleteRecord(record interviewapp.BeginCompleteRecord) error {
	if !validID(record.WorkspaceID) || !validID(record.SessionID) || !validHash(record.RequestHash) {
		return domain.InvalidError(domain.ErrorCodeReportInvalid, "interview completion begin record is invalid")
	}
	return domain.ValidateIdempotencyKey(record.IdempotencyKey)
}

// validatePrepareCompleteRecord 验证 digest Prepare 的冻结请求绑定。
func validatePrepareCompleteRecord(record interviewapp.PrepareCompleteRecord) error {
	if !validID(record.WorkspaceID) || !validID(record.SessionID) || !validHash(record.RequestHash) ||
		record.SnapshotVersion < 1 || !validHash(record.ArtifactDigest) {
		return domain.InvalidError(domain.ErrorCodeReportInvalid, "interview completion prepare record is invalid")
	}
	return domain.ValidateIdempotencyKey(record.IdempotencyKey)
}

// matchCompletionReservation 核对跨阶段不可变 reservation binding。
func matchCompletionReservation(reservation interviewapp.CompletionReservation, key, requestHash string, manualEnd bool, snapshotVersion int64) error {
	if reservation.IdempotencyKey != key || reservation.RequestHash != requestHash || reservation.ManualEnd != manualEnd {
		return domain.ConflictError(domain.ErrorCodeIdempotencyConflict, "interview completion reservation binding changed")
	}
	if reservation.SnapshotVersion != snapshotVersion {
		return domain.ConflictError(domain.ErrorCodeQuestionOrderConflict, "interview completion snapshot version changed")
	}
	return nil
}

// loadTerminalCompleteReplay 在当前事务内恢复 COMPLETE receipt。
func loadTerminalCompleteReplay(ctx context.Context, db rowQueryer, workspaceID foundation.ID, key, requestHash string) (*interviewapp.CompleteResult, bool, error) {
	receipt, found, err := loadReceipt(ctx, db, workspaceID, key, requestHash, "COMPLETE")
	if err != nil || !found {
		return nil, found, err
	}
	result, err := decodeCompleteResult(receipt.Response)
	if err != nil {
		return nil, false, err
	}
	result.Replayed = true
	return &result, true, nil
}

func validateCompleteRecord(record interviewapp.CompleteRecord) error {
	if !validID(record.WorkspaceID) || !validID(record.SessionID) || record.ExpectedVersion < 1 || !validHash(record.RequestHash) || !validHash(record.ArtifactDigest) || record.At.IsZero() || record.Report.WorkspaceID != record.WorkspaceID || record.Report.SessionID != record.SessionID || record.Path.WorkspaceID != record.WorkspaceID || record.Path.SessionID != record.SessionID || record.Path.ReportID != record.Report.ID || record.Report.Artifact.ArtifactID == record.Path.Artifact.ArtifactID {
		return domain.InvalidError(domain.ErrorCodeReportInvalid, "interview completion record is invalid")
	}
	if err := domain.ValidateReport(record.Report); err != nil {
		return err
	}
	if err := domain.ValidateLearningPath(record.Path); err != nil {
		return err
	}
	for _, step := range record.Steps {
		if step.WorkspaceID != record.WorkspaceID || step.PathID != record.Path.ID {
			return domain.InvalidError(domain.ErrorCodePathInvalid, "learning path step does not bind its path")
		}
		if err := domain.ValidatePathStep(step); err != nil {
			return err
		}
	}
	return domain.ValidateIdempotencyKey(record.IdempotencyKey)
}

func validatePathStepRecord(record interviewapp.UpdatePathStepRecord) error {
	if !validID(record.WorkspaceID) || !validID(record.PathID) || !validID(record.StepID) || record.ExpectedVersion < 1 || !validHash(record.RequestHash) || record.At.IsZero() {
		return domain.InvalidError(domain.ErrorCodePathInvalid, "learning path step record is invalid")
	}
	return domain.ValidateIdempotencyKey(record.IdempotencyKey)
}

func validatePathStatusRecord(record interviewapp.UpdatePathStatusRecord) error {
	if !validID(record.WorkspaceID) || !validID(record.PathID) || record.ExpectedVersion < 1 || !validHash(record.RequestHash) || record.At.IsZero() {
		return domain.InvalidError(domain.ErrorCodePathInvalid, "learning path status record is invalid")
	}
	return domain.ValidateIdempotencyKey(record.IdempotencyKey)
}

func decodeStartResult(raw []byte) (interviewapp.StartResult, error) {
	var result interviewapp.StartResult
	if err := decodeJSON(raw, &result); err != nil {
		return interviewapp.StartResult{}, persistenceInvalid("decode interview start receipt", err)
	}
	if err := domain.ValidateSession(result.Session); err != nil {
		return interviewapp.StartResult{}, persistenceInvalid("validate interview start receipt session", err)
	}
	if len(result.Questions) != result.Session.Config.QuestionCount {
		return interviewapp.StartResult{}, persistenceInvalid("interview start receipt question count is invalid", nil)
	}
	for _, question := range result.Questions {
		if err := domain.ValidateQuestion(question); err != nil {
			return interviewapp.StartResult{}, persistenceInvalid("validate interview start receipt question", err)
		}
	}
	return result, nil
}

func decodeSubmitResult(raw []byte) (interviewapp.SubmitTurnResult, error) {
	var result interviewapp.SubmitTurnResult
	if err := decodeJSON(raw, &result); err != nil {
		return interviewapp.SubmitTurnResult{}, persistenceInvalid("decode interview submit receipt", err)
	}
	if result.FollowUp != nil {
		if err := domain.ValidateQuestion(*result.FollowUp); err != nil {
			return interviewapp.SubmitTurnResult{}, persistenceInvalid("validate interview submit receipt follow-up", err)
		}
	}
	if result.NextQuestion != nil {
		if err := domain.ValidateQuestion(*result.NextQuestion); err != nil {
			return interviewapp.SubmitTurnResult{}, persistenceInvalid("validate interview submit receipt next question", err)
		}
	}
	return result, nil
}

func decodeCompleteResult(raw []byte) (interviewapp.CompleteResult, error) {
	var result interviewapp.CompleteResult
	if err := decodeJSON(raw, &result); err != nil {
		return interviewapp.CompleteResult{}, persistenceInvalid("decode interview complete receipt", err)
	}
	if err := domain.ValidateSession(result.Session); err != nil {
		return interviewapp.CompleteResult{}, persistenceInvalid("validate interview complete receipt session", err)
	}
	if err := domain.ValidateReport(result.Report); err != nil {
		return interviewapp.CompleteResult{}, persistenceInvalid("validate interview complete receipt report", err)
	}
	if err := domain.ValidateLearningPath(result.Path); err != nil {
		return interviewapp.CompleteResult{}, persistenceInvalid("validate interview complete receipt path", err)
	}
	for _, step := range result.Steps {
		if err := domain.ValidatePathStep(step); err != nil {
			return interviewapp.CompleteResult{}, persistenceInvalid("validate interview complete receipt step", err)
		}
	}
	return result, nil
}

func decodePathStepResult(raw []byte) (interviewapp.PathStepResult, error) {
	var result interviewapp.PathStepResult
	if err := decodeJSON(raw, &result); err != nil {
		return interviewapp.PathStepResult{}, persistenceInvalid("decode interview path step receipt", err)
	}
	if err := domain.ValidateLearningPath(result.Path); err != nil {
		return interviewapp.PathStepResult{}, persistenceInvalid("validate interview path step receipt path", err)
	}
	if err := domain.ValidatePathStep(result.Step); err != nil {
		return interviewapp.PathStepResult{}, persistenceInvalid("validate interview path step receipt step", err)
	}
	return result, nil
}

func decodePathStatusResult(raw []byte) (interviewapp.PathStatusResult, error) {
	var result interviewapp.PathStatusResult
	if err := decodeJSON(raw, &result); err != nil {
		return interviewapp.PathStatusResult{}, persistenceInvalid("decode interview path status receipt", err)
	}
	if err := domain.ValidateLearningPath(result.Path); err != nil {
		return interviewapp.PathStatusResult{}, persistenceInvalid("validate interview path status receipt path", err)
	}
	return result, nil
}

func nullableID(value *foundation.ID) any {
	if value == nil {
		return nil
	}
	return string(*value)
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC()
}

func hashBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func cloneQuestion(question *domain.Question) *domain.Question {
	if question == nil {
		return nil
	}
	copy := *question
	copy.AnswerPoints = append([]string(nil), question.AnswerPoints...)
	copy.Evidence = append([]domain.EvidenceRef(nil), question.Evidence...)
	return &copy
}

func idFromQuestion(question *domain.Question) *foundation.ID {
	if question == nil {
		return nil
	}
	id := question.ID
	return &id
}

func sameIDPointer(left, right *foundation.ID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func validStepTransition(from, to domain.StepStatus) bool {
	if from == to {
		return true
	}
	switch from {
	case domain.StepStatusPending:
		return to == domain.StepStatusInProgress || to == domain.StepStatusCompleted || to == domain.StepStatusSkipped
	case domain.StepStatusInProgress:
		return to == domain.StepStatusCompleted || to == domain.StepStatusSkipped
	default:
		return false
	}
}

func validPathTransition(from, to domain.PathStatus) bool {
	if from == to {
		return true
	}
	return (from == domain.PathStatusActive && (to == domain.PathStatusPaused || to == domain.PathStatusCompleted)) ||
		(from == domain.PathStatusPaused && to == domain.PathStatusActive)
}

var _ interviewapp.Store = (*Repository)(nil)
var _ interviewapp.QuestionSource = (*Repository)(nil)
