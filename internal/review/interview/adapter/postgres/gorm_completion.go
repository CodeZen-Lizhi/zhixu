package postgres

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
	"gorm.io/gorm"
)

func (repository *GORMRepository) FindCompleteReplay(ctx context.Context, workspaceID foundation.ID, key, requestHash string) (interviewapp.CompleteResult, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return interviewapp.CompleteResult{}, false, err
	}
	value, found, err := gormInterviewLoadReceipt(ctx, repository.database, workspaceID, key, requestHash, "COMPLETE")
	if err != nil || !found {
		return interviewapp.CompleteResult{}, found, err
	}
	result, err := decodeCompleteResult(value.Response)
	if err != nil {
		return interviewapp.CompleteResult{}, false, err
	}
	result.Replayed = true
	return result, true, nil
}

func (repository *GORMRepository) BeginComplete(ctx context.Context, record interviewapp.BeginCompleteRecord) (interviewapp.BeginCompleteResult, error) {
	if err := repository.ready(ctx); err != nil {
		return interviewapp.BeginCompleteResult{}, err
	}
	if err := validateBeginCompleteRecord(record); err != nil {
		return interviewapp.BeginCompleteResult{}, err
	}
	if record.ClaimOnly {
		if err := repository.requireClaimSession(ctx, record.WorkspaceID, record.SessionID); err != nil {
			return interviewapp.BeginCompleteResult{}, err
		}
	}
	var result interviewapp.BeginCompleteResult
	err := repository.within(ctx, func(callbackCtx context.Context, transaction *gorm.DB) error {
		if err := gormInterviewLockCommand(callbackCtx, transaction, record.WorkspaceID, record.IdempotencyKey); err != nil {
			return err
		}
		session, err := gormInterviewLoadSession(callbackCtx, transaction, record.WorkspaceID, record.SessionID, true)
		if err != nil {
			return err
		}
		if terminal, found, err := gormInterviewLoadTerminalCompleteReplay(callbackCtx, transaction, record.WorkspaceID, record.IdempotencyKey, record.RequestHash); err != nil {
			return err
		} else if found {
			result.Terminal = terminal
			return nil
		}

		reservation, found, err := gormInterviewLoadCompletionReservationBySession(callbackCtx, transaction, record.WorkspaceID, record.SessionID, true)
		if err != nil {
			return err
		}
		if found {
			if reservation.IdempotencyKey != record.IdempotencyKey {
				if reservation.Status == interviewapp.CompletionReservationPending {
					return domain.ConflictError(domain.ErrorCodeCompletionPending, "interview completion is reserved by another idempotency key")
				}
				return domain.ConflictError(domain.ErrorCodeIdempotencyConflict, "interview completion reservation belongs to another idempotency key")
			}
			if reservation.RequestHash != record.RequestHash || reservation.ManualEnd != record.ManualEnd {
				return domain.ConflictError(domain.ErrorCodeIdempotencyConflict, "interview completion idempotency key is bound to another request")
			}
			switch reservation.Status {
			case interviewapp.CompletionReservationCompleted:
				return persistenceInvalid("completed interview reservation is missing its terminal receipt", nil)
			case interviewapp.CompletionReservationPending:
				if session.Status != domain.SessionStatusActive || session.Version != reservation.SnapshotVersion {
					return persistenceInvalid("pending interview completion snapshot drifted", nil)
				}
			case interviewapp.CompletionReservationAbandoned:
				if session.Status != domain.SessionStatusActive {
					return domain.ConflictError(domain.ErrorCodeSessionClosed, "interview session is closed")
				}
				updated, err := gormInterviewExec(callbackCtx, transaction, `
					UPDATE learning.interview_completion_reservation
					   SET snapshot_version=?,artifact_digest=NULL,prepared_at=NULL,status='PENDING',
					       completed_at=NULL,abandoned_at=NULL,created_at=statement_timestamp(),updated_at=statement_timestamp()
					 WHERE workspace_id=?::uuid AND session_id=?::uuid AND status='ABANDONED'`,
					session.Version, string(record.WorkspaceID), string(record.SessionID))
				if err != nil {
					return gormInterviewClassify(callbackCtx, err, domain.ErrorCodePersistenceInvalid)
				}
				if updated != 1 {
					return domain.ConflictError(domain.ErrorCodeCompletionPending, "interview completion reservation changed")
				}
				reservation, found, err = gormInterviewLoadCompletionReservationBySession(callbackCtx, transaction, record.WorkspaceID, record.SessionID, true)
				if err != nil || !found {
					if err == nil {
						err = persistenceInvalid("reactivated interview completion reservation is missing", nil)
					}
					return err
				}
			default:
				return persistenceInvalid("interview completion reservation status is invalid", nil)
			}
		} else {
			if _, keyFound, err := gormInterviewLoadCompletionReservationByKey(callbackCtx, transaction, record.WorkspaceID, record.IdempotencyKey, true); err != nil {
				return err
			} else if keyFound {
				return domain.ConflictError(domain.ErrorCodeIdempotencyConflict, "interview completion idempotency key belongs to another session")
			}
			if session.Status != domain.SessionStatusActive {
				return domain.ConflictError(domain.ErrorCodeSessionClosed, "interview session is closed")
			}
			if _, err := gormInterviewExec(callbackCtx, transaction, `
				INSERT INTO learning.interview_completion_reservation(
					workspace_id,session_id,idempotency_key,request_hash,manual_end,snapshot_version,
					artifact_digest,status,created_at,updated_at
				) VALUES(?::uuid,?::uuid,?,?,?, ?,NULL,'PENDING',statement_timestamp(),statement_timestamp())`,
				string(record.WorkspaceID), string(record.SessionID), record.IdempotencyKey, record.RequestHash, record.ManualEnd, session.Version); err != nil {
				return gormInterviewClassify(callbackCtx, err, domain.ErrorCodeIdempotencyConflict)
			}
			reservation, found, err = gormInterviewLoadCompletionReservationBySession(callbackCtx, transaction, record.WorkspaceID, record.SessionID, true)
			if err != nil || !found {
				if err == nil {
					err = persistenceInvalid("created interview completion reservation is missing", nil)
				}
				return err
			}
		}

		snapshot, err := gormInterviewLoadSnapshot(callbackCtx, transaction, record.WorkspaceID, record.SessionID, true)
		if err != nil {
			return err
		}
		if snapshot.Session.Status != domain.SessionStatusActive || snapshot.Session.Version != reservation.SnapshotVersion {
			return persistenceInvalid("interview completion snapshot is not frozen at the reservation version", nil)
		}
		if domain.OrderedPendingQuestion(snapshot.Questions) != nil && !record.ManualEnd {
			return domain.ConflictError(domain.ErrorCodeQuestionOrderConflict, "interview has unanswered questions")
		}
		result = interviewapp.BeginCompleteResult{Reservation: reservation, Snapshot: snapshot}
		return nil
	})
	if err != nil {
		return interviewapp.BeginCompleteResult{}, err
	}
	return result, nil
}

func (repository *GORMRepository) PrepareComplete(ctx context.Context, record interviewapp.PrepareCompleteRecord) (interviewapp.PrepareCompleteResult, error) {
	if err := repository.ready(ctx); err != nil {
		return interviewapp.PrepareCompleteResult{}, err
	}
	if err := validatePrepareCompleteRecord(record); err != nil {
		return interviewapp.PrepareCompleteResult{}, err
	}
	if record.ClaimOnly {
		if err := repository.requireClaimSession(ctx, record.WorkspaceID, record.SessionID); err != nil {
			return interviewapp.PrepareCompleteResult{}, err
		}
	}
	var result interviewapp.PrepareCompleteResult
	err := repository.within(ctx, func(callbackCtx context.Context, transaction *gorm.DB) error {
		if err := gormInterviewLockCommand(callbackCtx, transaction, record.WorkspaceID, record.IdempotencyKey); err != nil {
			return err
		}
		session, err := gormInterviewLoadSession(callbackCtx, transaction, record.WorkspaceID, record.SessionID, true)
		if err != nil {
			return err
		}
		if terminal, found, err := gormInterviewLoadTerminalCompleteReplay(callbackCtx, transaction, record.WorkspaceID, record.IdempotencyKey, record.RequestHash); err != nil {
			return err
		} else if found {
			result.Terminal = terminal
			return nil
		}
		reservation, found, err := gormInterviewLoadCompletionReservationBySession(callbackCtx, transaction, record.WorkspaceID, record.SessionID, true)
		if err != nil {
			return err
		}
		if !found {
			return persistenceInvalid("interview completion reservation is missing", nil)
		}
		if err := matchCompletionReservation(reservation, record.IdempotencyKey, record.RequestHash, record.ManualEnd, record.SnapshotVersion); err != nil {
			return err
		}
		if reservation.Status == interviewapp.CompletionReservationAbandoned {
			return domain.ConflictError(domain.ErrorCodeCompletionAbandoned, "interview completion reservation was abandoned")
		}
		if reservation.Status != interviewapp.CompletionReservationPending || session.Status != domain.SessionStatusActive || session.Version != reservation.SnapshotVersion {
			return domain.ConflictError(domain.ErrorCodeQuestionOrderConflict, "interview completion snapshot changed")
		}
		if reservation.ArtifactDigest != "" && reservation.ArtifactDigest != record.ArtifactDigest {
			return domain.ConflictError(domain.ErrorCodeCompletionArtifactConflict, "interview completion reservation has another artifact digest")
		}
		if reservation.ArtifactDigest == "" {
			updated, err := gormInterviewExec(callbackCtx, transaction, `
				UPDATE learning.interview_completion_reservation
				   SET artifact_digest=?,prepared_at=statement_timestamp(),updated_at=statement_timestamp()
				 WHERE workspace_id=?::uuid AND session_id=?::uuid AND status='PENDING' AND artifact_digest IS NULL`,
				record.ArtifactDigest, string(record.WorkspaceID), string(record.SessionID))
			if err != nil {
				return gormInterviewClassify(callbackCtx, err, domain.ErrorCodePersistenceInvalid)
			}
			if updated != 1 {
				return domain.ConflictError(domain.ErrorCodeCompletionArtifactConflict, "interview completion digest changed")
			}
		}
		if _, err := gormInterviewExec(callbackCtx, transaction, `
			UPDATE learning.artifact_visibility_hold
			   SET disposition='ORPHANED'
			 WHERE workspace_id=?::uuid AND owner_type='INTERVIEW_COMPLETE' AND owner_id=?::uuid
			   AND disposition='ACTIVE' AND attempt_digest IS NOT NULL AND attempt_digest<>?`,
			string(record.WorkspaceID), string(record.SessionID), record.ArtifactDigest); err != nil {
			return gormInterviewClassify(callbackCtx, err, domain.ErrorCodePersistenceInvalid)
		}
		if _, err := gormInterviewExec(callbackCtx, transaction, `
			UPDATE learning.artifact_visibility_hold AS candidate
			   SET disposition='ACTIVE'
			 WHERE candidate.workspace_id=?::uuid
			   AND candidate.owner_type='INTERVIEW_COMPLETE'
			   AND candidate.owner_id=?::uuid
			   AND candidate.disposition='ORPHANED'
			   AND candidate.attempt_digest=?
			   AND NOT EXISTS (
			       SELECT 1
			         FROM learning.artifact_visibility_hold AS active
			        WHERE active.workspace_id=candidate.workspace_id
			          AND active.owner_type=candidate.owner_type
			          AND active.owner_id=candidate.owner_id
			          AND active.owner_role=candidate.owner_role
			          AND active.disposition='ACTIVE'
			   )`, string(record.WorkspaceID), string(record.SessionID), record.ArtifactDigest); err != nil {
			return gormInterviewClassify(callbackCtx, err, domain.ErrorCodePersistenceInvalid)
		}
		reservation, found, err = gormInterviewLoadCompletionReservationBySession(callbackCtx, transaction, record.WorkspaceID, record.SessionID, true)
		if err != nil || !found {
			if err == nil {
				err = persistenceInvalid("prepared interview completion reservation is missing", nil)
			}
			return err
		}
		if reservation.ArtifactDigest != record.ArtifactDigest {
			return persistenceInvalid("prepared interview completion digest drifted", nil)
		}
		result.Reservation = reservation
		return nil
	})
	if err != nil {
		return interviewapp.PrepareCompleteResult{}, err
	}
	return result, nil
}

func (repository *GORMRepository) Complete(ctx context.Context, record interviewapp.CompleteRecord) (interviewapp.CompleteResult, error) {
	if err := repository.ready(ctx); err != nil {
		return interviewapp.CompleteResult{}, err
	}
	if err := validateCompleteRecord(record); err != nil {
		return interviewapp.CompleteResult{}, err
	}
	if record.ClaimOnly {
		if err := repository.requireClaimSession(ctx, record.WorkspaceID, record.SessionID); err != nil {
			return interviewapp.CompleteResult{}, err
		}
	}
	var result interviewapp.CompleteResult
	err := repository.within(ctx, func(callbackCtx context.Context, transaction *gorm.DB) error {
		if err := gormInterviewLockCommand(callbackCtx, transaction, record.WorkspaceID, record.IdempotencyKey); err != nil {
			return err
		}
		session, err := gormInterviewLoadSession(callbackCtx, transaction, record.WorkspaceID, record.SessionID, true)
		if err != nil {
			return err
		}
		if replay, found, err := gormInterviewLoadReceipt(callbackCtx, transaction, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, "COMPLETE"); err != nil {
			return err
		} else if found {
			result, err = decodeCompleteResult(replay.Response)
			if err == nil {
				result.Replayed = true
			}
			return err
		}
		reservation, found, err := gormInterviewLoadCompletionReservationBySession(callbackCtx, transaction, record.WorkspaceID, record.SessionID, true)
		if err != nil {
			return err
		}
		if !found {
			return persistenceInvalid("interview completion reservation is missing", nil)
		}
		if err := matchCompletionReservation(reservation, record.IdempotencyKey, record.RequestHash, record.ManualEnd, record.ExpectedVersion); err != nil {
			return err
		}
		if reservation.Status == interviewapp.CompletionReservationAbandoned {
			return domain.ConflictError(domain.ErrorCodeCompletionAbandoned, "interview completion reservation was abandoned")
		}
		if reservation.Status != interviewapp.CompletionReservationPending || reservation.ArtifactDigest != record.ArtifactDigest {
			return domain.ConflictError(domain.ErrorCodeCompletionArtifactConflict, "interview completion artifact digest changed")
		}
		if session.Status != domain.SessionStatusActive {
			return domain.ConflictError(domain.ErrorCodeSessionClosed, "interview session is closed")
		}
		if session.Version != reservation.SnapshotVersion || session.Version != record.ExpectedVersion {
			return domain.ConflictError(domain.ErrorCodeQuestionOrderConflict, "interview session version changed")
		}
		questions, err := gormInterviewLoadQuestions(callbackCtx, transaction, record.WorkspaceID, record.SessionID)
		if err != nil {
			return err
		}
		if domain.OrderedPendingQuestion(questions) != nil && !record.ManualEnd {
			return domain.ConflictError(domain.ErrorCodeQuestionOrderConflict, "interview has unanswered questions")
		}
		if record.ManualEnd {
			if _, err := gormInterviewExec(callbackCtx, transaction, `
				UPDATE learning.interview_question
				   SET status='SKIPPED'
				 WHERE workspace_id=?::uuid AND session_id=?::uuid AND status='PENDING'`,
				string(record.WorkspaceID), string(record.SessionID)); err != nil {
				return gormInterviewClassify(callbackCtx, err, domain.ErrorCodePersistenceInvalid)
			}
		}
		if err := gormInterviewReconcileCompletionArtifactVisibility(callbackCtx, transaction, record); err != nil {
			return err
		}
		reportRaw, err := encodeJSON(record.Report)
		if err != nil {
			return err
		}
		reportRaw, err = canonicalJSON(reportRaw)
		if err != nil {
			return persistenceInvalid("canonicalize interview report", err)
		}
		reportHash := hashBytes(reportRaw)
		if _, err := gormInterviewExec(callbackCtx, transaction, `
			INSERT INTO learning.interview_report(
				id,workspace_id,session_id,domain_schema_version,report,report_hash,artifact_id,artifact_revision_id,artifact_version,created_at
			) VALUES(?::uuid,?::uuid,?::uuid,?,?::jsonb,?,?::uuid,?::uuid,?,?::timestamptz)`,
			string(record.Report.ID), string(record.Report.WorkspaceID), string(record.Report.SessionID), record.Report.SchemaVersion,
			interviewJSONB(reportRaw), reportHash, string(record.Report.Artifact.ArtifactID), string(record.Report.Artifact.RevisionID),
			record.Report.Artifact.ArtifactVersion, record.Report.CreatedAt.UTC()); err != nil {
			return gormInterviewClassify(callbackCtx, err, domain.ErrorCodePersistenceInvalid)
		}
		if _, err := gormInterviewExec(callbackCtx, transaction, `
			INSERT INTO learning.interview_learning_path(
				id,workspace_id,session_id,report_id,artifact_id,artifact_revision_id,artifact_version,status,version,created_at,updated_at
			) VALUES(?::uuid,?::uuid,?::uuid,?::uuid,?::uuid,?::uuid,?,?,?,?::timestamptz,?::timestamptz)`,
			string(record.Path.ID), string(record.Path.WorkspaceID), string(record.Path.SessionID), string(record.Path.ReportID),
			string(record.Path.Artifact.ArtifactID), string(record.Path.Artifact.RevisionID), record.Path.Artifact.ArtifactVersion,
			string(record.Path.Status), record.Path.Version, record.Path.CreatedAt.UTC(), record.Path.UpdatedAt.UTC()); err != nil {
			return gormInterviewClassify(callbackCtx, err, domain.ErrorCodePersistenceInvalid)
		}
		for _, step := range record.Steps {
			if err := gormInterviewInsertPathStep(callbackCtx, transaction, step); err != nil {
				return err
			}
		}
		updated, err := gormInterviewExec(callbackCtx, transaction, `
			UPDATE learning.review_session
			   SET status='COMPLETED',ended_at=?::timestamptz
			 WHERE workspace_id=?::uuid AND id=?::uuid AND status='ACTIVE'`,
			record.At.UTC(), string(record.WorkspaceID), string(record.SessionID))
		if err != nil {
			return gormInterviewClassify(callbackCtx, err, domain.ErrorCodePersistenceInvalid)
		}
		if updated != 1 {
			return domain.ConflictError(domain.ErrorCodeSessionClosed, "interview review shell is no longer active")
		}
		updated, err = gormInterviewExec(callbackCtx, transaction, `
			UPDATE learning.interview_session
			   SET version=version+1,updated_at=?::timestamptz
			 WHERE workspace_id=?::uuid AND session_id=?::uuid AND version=?`,
			record.At.UTC(), string(record.WorkspaceID), string(record.SessionID), session.Version)
		if err != nil {
			return gormInterviewClassify(callbackCtx, err, domain.ErrorCodePersistenceInvalid)
		}
		if updated != 1 {
			return domain.ConflictError(domain.ErrorCodeQuestionOrderConflict, "interview session CAS did not match")
		}
		if err := gormInterviewReleaseCompletionArtifactVisibility(callbackCtx, transaction, record); err != nil {
			return err
		}
		endedAt := record.At.UTC()
		session.Status = domain.SessionStatusCompleted
		session.Version++
		session.EndedAt = &endedAt
		result = interviewapp.CompleteResult{Session: session, Report: record.Report, Path: record.Path, Steps: append([]domain.PathStep(nil), record.Steps...)}
		if err := gormInterviewInsertReceipt(callbackCtx, transaction, record.WorkspaceID, record.IdempotencyKey,
			record.RequestHash, "COMPLETE", record.SessionID, result, record.At); err != nil {
			return err
		}
		updated, err = gormInterviewExec(callbackCtx, transaction, `
			UPDATE learning.interview_completion_reservation
			   SET report_id=?::uuid,report_artifact_id=?::uuid,report_artifact_revision_id=?::uuid,report_artifact_version=?,
			       path_id=?::uuid,path_artifact_id=?::uuid,path_artifact_revision_id=?::uuid,path_artifact_version=?,
			       status='COMPLETED',completed_at=statement_timestamp(),updated_at=statement_timestamp()
			 WHERE workspace_id=?::uuid AND session_id=?::uuid AND status='PENDING'
			   AND snapshot_version=? AND artifact_digest=?`,
			string(record.Report.ID), string(record.Report.Artifact.ArtifactID), string(record.Report.Artifact.RevisionID), record.Report.Artifact.ArtifactVersion,
			string(record.Path.ID), string(record.Path.Artifact.ArtifactID), string(record.Path.Artifact.RevisionID), record.Path.Artifact.ArtifactVersion,
			string(record.WorkspaceID), string(record.SessionID), record.ExpectedVersion, record.ArtifactDigest)
		if err != nil {
			return gormInterviewClassify(callbackCtx, err, domain.ErrorCodePersistenceInvalid)
		}
		if updated != 1 {
			return domain.ConflictError(domain.ErrorCodeCompletionArtifactConflict, "interview completion reservation changed")
		}
		return nil
	})
	if err != nil {
		if replay, found, replayErr := repository.FindCompleteReplay(ctx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash); replayErr == nil && found {
			return replay, nil
		}
	}
	return result, err
}

func (repository *GORMRepository) AbandonStaleCompletions(ctx context.Context, batchSize int) (interviewapp.CompletionMaintenanceResult, error) {
	if err := repository.ready(ctx); err != nil {
		return interviewapp.CompletionMaintenanceResult{}, err
	}
	if batchSize < 1 || batchSize > interviewapp.MaxCompletionMaintenanceBatch {
		return interviewapp.CompletionMaintenanceResult{}, domain.InvalidError(domain.ErrorCodeConfigInvalid, "interview completion maintenance batch is invalid")
	}
	row, err := gormInterviewRawRow(ctx, repository.database, `
		WITH maintenance_clock AS MATERIALIZED (
			SELECT clock_timestamp() AS now
		), candidates AS MATERIALIZED (
			SELECT reservation.workspace_id,reservation.session_id
			  FROM learning.interview_completion_reservation AS reservation
			 CROSS JOIN maintenance_clock
			 WHERE reservation.status='PENDING'
			   AND reservation.updated_at <= maintenance_clock.now - interval '24 hours'
			 ORDER BY reservation.updated_at,reservation.workspace_id,reservation.session_id
			 LIMIT ?
			 FOR UPDATE OF reservation SKIP LOCKED
		), abandoned AS (
			UPDATE learning.interview_completion_reservation AS reservation
			   SET status='ABANDONED',abandoned_at=maintenance_clock.now,updated_at=maintenance_clock.now
			  FROM candidates,maintenance_clock
			 WHERE reservation.workspace_id=candidates.workspace_id
			   AND reservation.session_id=candidates.session_id
			   AND reservation.status='PENDING'
			 RETURNING reservation.workspace_id,reservation.session_id,reservation.artifact_digest
		), orphaned AS (
			UPDATE learning.artifact_visibility_hold AS hold
			   SET attempt_digest=COALESCE(hold.attempt_digest,abandoned.artifact_digest),
			       disposition='ORPHANED'
			  FROM abandoned
			 WHERE hold.workspace_id=abandoned.workspace_id
			   AND hold.owner_type='INTERVIEW_COMPLETE'
			   AND hold.owner_id=abandoned.session_id
			   AND hold.disposition='ACTIVE'
			 RETURNING hold.artifact_id
		)
		SELECT (SELECT count(*) FROM abandoned),(SELECT count(*) FROM orphaned)`, batchSize)
	if err != nil {
		return interviewapp.CompletionMaintenanceResult{}, gormInterviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	var result interviewapp.CompletionMaintenanceResult
	if err := row.Scan(&result.AbandonedReservations, &result.OrphanedHolds); err != nil {
		return interviewapp.CompletionMaintenanceResult{}, gormInterviewClassify(ctx, err, domain.ErrorCodeDependencyUnavailable)
	}
	return result, nil
}

var _ interviewapp.Store = (*GORMRepository)(nil)
var _ interviewapp.QuestionSource = (*GORMRepository)(nil)
