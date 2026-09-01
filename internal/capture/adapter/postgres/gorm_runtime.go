package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"gorm.io/gorm"
)

// ClaimNext takes one due Capture event with a database-time lease.
func (repository *GORMRepository) ClaimNext(ctx context.Context, owner string, leaseDuration time.Duration) (captureapp.OutboxLease, bool, error) {
	owner = strings.TrimSpace(owner)
	if err := repository.ready(ctx); err != nil {
		return captureapp.OutboxLease{}, false, err
	}
	if owner == "" || len(owner) > 256 || leaseDuration <= 0 {
		return captureapp.OutboxLease{}, false, invalid("CAPTURE_OUTBOX_CLAIM_INVALID", "capture outbox claim is invalid")
	}
	row, err := gormCaptureRawRow(repository.database.WithContext(ctx), `WITH candidate AS (
		SELECT id FROM ops.capture_outbox
		WHERE published_at IS NULL AND poisoned_at IS NULL AND available_at<=clock_timestamp()
		  AND (lease_expires_at IS NULL OR lease_expires_at<=clock_timestamp())
		ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT 1
	)
	UPDATE ops.capture_outbox AS event
	SET lease_owner=?,lease_expires_at=clock_timestamp()+(?::bigint*interval '1 millisecond'),
		attempt_count=event.attempt_count+1,version=event.version+1,updated_at=clock_timestamp()
	FROM candidate WHERE event.id=candidate.id
	RETURNING event.id::text,event.workspace_id::text,event.capture_id::text,event.attempt_count,event.version,event.lease_expires_at`, owner, leaseDuration.Milliseconds())
	if err != nil {
		return captureapp.OutboxLease{}, false, classifyGORMCapture(ctx, err, "CAPTURE_OUTBOX_CLAIM_FAILED")
	}
	var lease captureapp.OutboxLease
	var id, workspaceID, captureID string
	if err = row.Scan(&id, &workspaceID, &captureID, &lease.AttemptCount, &lease.Version, &lease.LeaseUntil); gormCaptureNoRows(err) {
		return captureapp.OutboxLease{}, false, nil
	} else if err != nil {
		return captureapp.OutboxLease{}, false, classifyGORMCapture(ctx, err, "CAPTURE_OUTBOX_CLAIM_FAILED")
	}
	if lease.ID, err = foundation.ParseID(id); err != nil {
		return captureapp.OutboxLease{}, false, inconsistent("CAPTURE_OUTBOX_LEASE_INVALID", "capture outbox id is invalid")
	}
	if lease.WorkspaceID, err = foundation.ParseID(workspaceID); err != nil {
		return captureapp.OutboxLease{}, false, inconsistent("CAPTURE_OUTBOX_LEASE_INVALID", "capture outbox workspace is invalid")
	}
	if lease.CaptureID, err = foundation.ParseID(captureID); err != nil {
		return captureapp.OutboxLease{}, false, inconsistent("CAPTURE_OUTBOX_LEASE_INVALID", "capture outbox capture is invalid")
	}
	lease.Owner = owner
	return lease, true, nil
}

func (repository *GORMRepository) MarkPublished(ctx context.Context, lease captureapp.OutboxLease, workflowRunID foundation.ID) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	if validateLease(lease) != nil || !validID(workflowRunID) {
		return invalid("CAPTURE_OUTBOX_COMPLETE_INVALID", "capture outbox completion is invalid")
	}
	result := repository.database.WithContext(ctx).Exec(`UPDATE ops.capture_outbox SET
		published_at=clock_timestamp(),lease_owner=NULL,lease_expires_at=NULL,
		payload=jsonb_set(payload,'{workflow_run_id}',to_jsonb(?::text),true),version=version+1,updated_at=clock_timestamp()
		WHERE id=? AND workspace_id=? AND lease_owner=? AND version=? AND lease_expires_at>clock_timestamp() AND published_at IS NULL AND poisoned_at IS NULL`,
		string(workflowRunID), string(lease.ID), string(lease.WorkspaceID), lease.Owner, lease.Version)
	if result.Error != nil {
		return classifyGORMCapture(ctx, result.Error, "CAPTURE_OUTBOX_COMPLETE_FAILED")
	}
	if result.RowsAffected != 1 {
		return foundation.NewError(foundation.ErrorVersionConflict, "CAPTURE_OUTBOX_LEASE_LOST", true, errors.New("capture outbox lease was lost"))
	}
	return nil
}

func (repository *GORMRepository) Reschedule(ctx context.Context, lease captureapp.OutboxLease, code string, delay time.Duration) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	code = strings.TrimSpace(code)
	if validateLease(lease) != nil || !validErrorCode(code) || delay <= 0 {
		return invalid("CAPTURE_OUTBOX_RETRY_INVALID", "capture outbox retry is invalid")
	}
	result := repository.database.WithContext(ctx).Exec(`UPDATE ops.capture_outbox SET
		available_at=clock_timestamp()+(?::bigint*interval '1 millisecond'),last_error_code=?,lease_owner=NULL,lease_expires_at=NULL,version=version+1,updated_at=clock_timestamp()
		WHERE id=? AND workspace_id=? AND lease_owner=? AND version=? AND published_at IS NULL AND poisoned_at IS NULL`,
		delay.Milliseconds(), code, string(lease.ID), string(lease.WorkspaceID), lease.Owner, lease.Version)
	if result.Error != nil {
		return classifyGORMCapture(ctx, result.Error, "CAPTURE_OUTBOX_RETRY_FAILED")
	}
	if result.RowsAffected != 1 {
		return foundation.NewError(foundation.ErrorVersionConflict, "CAPTURE_OUTBOX_LEASE_LOST", true, errors.New("capture outbox lease was lost"))
	}
	return nil
}

func (repository *GORMRepository) Poison(ctx context.Context, lease captureapp.OutboxLease, code string) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	code = strings.TrimSpace(code)
	if validateLease(lease) != nil || !validErrorCode(code) {
		return invalid("CAPTURE_OUTBOX_POISON_INVALID", "capture outbox poison request is invalid")
	}
	result := repository.database.WithContext(ctx).Exec(`UPDATE ops.capture_outbox SET
		poisoned_at=clock_timestamp(),manual_recovery_required=true,last_error_code=?,lease_owner=NULL,lease_expires_at=NULL,version=version+1,updated_at=clock_timestamp()
		WHERE id=? AND workspace_id=? AND lease_owner=? AND version=? AND published_at IS NULL AND poisoned_at IS NULL`,
		code, string(lease.ID), string(lease.WorkspaceID), lease.Owner, lease.Version)
	if result.Error != nil {
		return classifyGORMCapture(ctx, result.Error, "CAPTURE_OUTBOX_POISON_FAILED")
	}
	if result.RowsAffected != 1 {
		return foundation.NewError(foundation.ErrorVersionConflict, "CAPTURE_OUTBOX_LEASE_LOST", true, errors.New("capture outbox lease was lost"))
	}
	return nil
}

func (repository *GORMRepository) BeginAttempt(ctx context.Context, request captureapp.BeginAttemptRequest) (domain.Capture, domain.ProcessingAttempt, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, err
	}
	if !validID(request.ID) || !validID(request.WorkspaceID) || !validID(request.CaptureID) || !validID(request.WorkflowRunID) || request.AttemptNumber < 1 || request.StartedAt.IsZero() {
		return domain.Capture{}, domain.ProcessingAttempt{}, invalid("CAPTURE_ATTEMPT_BEGIN_INVALID", "capture processing attempt is invalid")
	}
	var capture domain.Capture
	var attempt domain.ProcessingAttempt
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, tx *gorm.DB, _ foundation.TransactionScope) error {
		var err error
		capture, err = gormLockCapture(callbackCtx, tx, request.WorkspaceID, request.CaptureID)
		if err != nil {
			return err
		}
		stage := domain.AttemptStageIngestion
		if capture.Kind == domain.KindURL && capture.LatestSourceVersionID == "" {
			stage = domain.AttemptStageFetch
		}
		row, err := gormCaptureRawRow(tx, `INSERT INTO ops.capture_attempt(id,workspace_id,capture_id,source_version_id,workflow_run_id,attempt_number,stage,status,error_code,retryable,started_at,version)
			VALUES(?,?,?,NULLIF(?,'')::uuid,?,?,?,'RUNNING','',false,?,1) ON CONFLICT (capture_id,workflow_run_id,attempt_number) DO NOTHING RETURNING id::text`, string(request.ID), string(request.WorkspaceID), string(request.CaptureID), string(capture.LatestSourceVersionID), string(request.WorkflowRunID), request.AttemptNumber, string(stage), request.StartedAt.UTC())
		if err != nil {
			return classifyGORMCapture(callbackCtx, err, "CAPTURE_ATTEMPT_BEGIN_FAILED")
		}
		var insertedID string
		insertErr := row.Scan(&insertedID)
		created := insertErr == nil
		if insertErr != nil && !gormCaptureNoRows(insertErr) {
			return classifyGORMCapture(callbackCtx, insertErr, "CAPTURE_ATTEMPT_BEGIN_FAILED")
		}
		row, err = gormCaptureRawRow(tx, `SELECT `+attemptReturning+` FROM ops.capture_attempt WHERE capture_id=? AND workflow_run_id=? AND attempt_number=? FOR UPDATE`, string(request.CaptureID), string(request.WorkflowRunID), request.AttemptNumber)
		if err != nil {
			return classifyGORMCapture(callbackCtx, err, "CAPTURE_ATTEMPT_QUERY_FAILED")
		}
		attempt, err = scanAttempt(row)
		if err != nil {
			return classifyGORMCapture(callbackCtx, err, "CAPTURE_ATTEMPT_QUERY_FAILED")
		}
		if attempt.ID != request.ID && created {
			return inconsistent("CAPTURE_ATTEMPT_BINDING_INVALID", "capture attempt identity drifted")
		}
		if attempt.WorkspaceID != request.WorkspaceID || attempt.CaptureID != request.CaptureID || attempt.WorkflowRunID != request.WorkflowRunID || attempt.AttemptNumber != request.AttemptNumber {
			return foundation.NewError(foundation.ErrorVersionConflict, "CAPTURE_ATTEMPT_REPLAY_CONFLICT", false, errors.New("capture attempt is bound to another workflow"))
		}
		if created {
			status, fetchStatus, ingestionStatus := domain.StatusProcessing, capture.FetchStatus, domain.StageRunning
			if stage == domain.AttemptStageFetch {
				status, fetchStatus, ingestionStatus = domain.StatusFetching, domain.StageRunning, domain.StagePending
			}
			row, err = gormCaptureRawRow(tx, `UPDATE core.capture SET status=?,fetch_status=?,ingestion_status=?,index_status='PENDING',profile_status='PENDING',failure_stage='',error_code='',retryable=false,version=version+1,updated_at=? WHERE workspace_id=? AND id=? AND version=? RETURNING `+captureReturning, string(status), string(fetchStatus), string(ingestionStatus), request.StartedAt.UTC(), string(capture.WorkspaceID), string(capture.ID), capture.Version)
			if err != nil {
				return classifyGORMCapture(callbackCtx, err, "CAPTURE_ATTEMPT_BEGIN_FAILED")
			}
			capture, err = scanCapture(row)
			if err != nil {
				return classifyGORMCapture(callbackCtx, err, "CAPTURE_ATTEMPT_BEGIN_FAILED")
			}
		}
		return nil
	})
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classifyGORMCapture(ctx, err, "CAPTURE_ATTEMPT_BEGIN_FAILED")
	}
	return capture, attempt, nil
}

func (repository *GORMRepository) MaterializeURL(ctx context.Context, request captureapp.MaterializeURLRequest) (domain.Capture, domain.ProcessingAttempt, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, err
	}
	if request.Attempt.Validate() != nil || request.Attempt.Stage != domain.AttemptStageFetch || request.ExpectedCaptureVersion < 1 || !validID(request.ArtifactID) || !validID(request.SourceVersionID) || !validHash(request.ContentHash) || request.ByteSize < 1 || strings.TrimSpace(request.MediaType) == "" || strings.TrimSpace(request.OriginalContentLocation) == "" || strings.TrimSpace(request.ManagedLocation) == "" || request.CapturedAt.IsZero() {
		return domain.Capture{}, domain.ProcessingAttempt{}, invalid("CAPTURE_URL_MATERIALIZATION_INVALID", "capture URL materialization is invalid")
	}
	var capture domain.Capture
	var attempt domain.ProcessingAttempt
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, tx *gorm.DB, scope foundation.TransactionScope) error {
		var err error
		capture, attempt, err = gormLockProcessing(callbackCtx, tx, request.Attempt, request.ExpectedCaptureVersion)
		if err != nil {
			return err
		}
		if capture.Kind != domain.KindURL || capture.LatestSourceVersionID != "" || capture.FetchStatus != domain.StageRunning {
			return foundation.NewError(foundation.ErrorVersionConflict, "CAPTURE_URL_ALREADY_MATERIALIZED", false, errors.New("capture URL cannot be materialized from its current state"))
		}
		registration := workspacedomain.SourceRegistration{Source: workspacedomain.Source{ID: capture.SourceID, WorkspaceID: capture.WorkspaceID, Type: "quick_capture_url", LogicalName: capture.DisplayName, OriginalLocation: capture.OriginalLocation, CreatedAt: capture.CapturedAt}, Artifact: workspacedomain.ContentArtifact{ID: request.ArtifactID, WorkspaceID: capture.WorkspaceID, ContentHash: request.ContentHash, ByteSize: request.ByteSize, ManagedLocation: request.ManagedLocation, CreatedAt: request.CapturedAt}, Version: workspacedomain.SourceVersion{ID: request.SourceVersionID, SourceID: capture.SourceID, ContentArtifactID: request.ArtifactID, ContentHash: request.ContentHash, ByteSize: request.ByteSize, MediaType: request.MediaType, OriginalContentLocation: request.OriginalContentLocation, SecurityStatus: "pending", CapturedAt: request.CapturedAt}}
		registered, err := repository.workspaceWriter.RegisterSourceVersionScoped(callbackCtx, scope, registration)
		if err != nil {
			return err
		}
		if registered.Source.ID != capture.SourceID || registered.Version.ID != request.SourceVersionID {
			return inconsistent("CAPTURE_URL_SOURCE_BINDING_INVALID", "workspace URL registration returned a different binding")
		}
		row, err := gormCaptureRawRow(tx, `UPDATE core.capture SET latest_source_version_id=?,status='PROCESSING',fetch_status='READY',ingestion_status='RUNNING',index_status='PENDING',profile_status='PENDING',failure_stage='',error_code='',retryable=false,version=version+1,updated_at=? WHERE workspace_id=? AND id=? AND version=? RETURNING `+captureReturning, string(request.SourceVersionID), request.CapturedAt.UTC(), string(capture.WorkspaceID), string(capture.ID), capture.Version)
		if err != nil {
			return classifyGORMCapture(callbackCtx, err, "CAPTURE_URL_MATERIALIZATION_FAILED")
		}
		capture, err = scanCapture(row)
		if err != nil {
			return classifyGORMCapture(callbackCtx, err, "CAPTURE_URL_MATERIALIZATION_FAILED")
		}
		row, err = gormCaptureRawRow(tx, `UPDATE ops.capture_attempt SET source_version_id=?,stage='INGESTION',version=version+1 WHERE id=? AND version=? AND status='RUNNING' RETURNING `+attemptReturning, string(request.SourceVersionID), string(attempt.ID), attempt.Version)
		if err != nil {
			return classifyGORMCapture(callbackCtx, err, "CAPTURE_URL_MATERIALIZATION_FAILED")
		}
		attempt, err = scanAttempt(row)
		if err != nil {
			return classifyGORMCapture(callbackCtx, err, "CAPTURE_URL_MATERIALIZATION_FAILED")
		}
		return nil
	})
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classifyGORMCapture(ctx, err, "CAPTURE_URL_MATERIALIZATION_FAILED")
	}
	return capture, attempt, nil
}

func (repository *GORMRepository) MarkRefreshRunning(ctx context.Context, attempt domain.ProcessingAttempt, expectedCaptureVersion int64, at time.Time) (domain.Capture, domain.ProcessingAttempt, error) {
	return repository.gormUpdateRunningStage(ctx, attempt, expectedCaptureVersion, domain.AttemptStageIngestion, at)
}
func (repository *GORMRepository) MarkProfileRunning(ctx context.Context, attempt domain.ProcessingAttempt, expectedCaptureVersion int64, at time.Time) (domain.Capture, domain.ProcessingAttempt, error) {
	return repository.gormUpdateRunningStage(ctx, attempt, expectedCaptureVersion, domain.AttemptStageProfile, at)
}

func (repository *GORMRepository) gormUpdateRunningStage(ctx context.Context, requested domain.ProcessingAttempt, expectedCaptureVersion int64, stage domain.AttemptStage, at time.Time) (domain.Capture, domain.ProcessingAttempt, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, err
	}
	if requested.Validate() != nil || requested.Status != domain.AttemptStatusRunning || expectedCaptureVersion < 1 || at.IsZero() {
		return domain.Capture{}, domain.ProcessingAttempt{}, invalid("CAPTURE_STAGE_UPDATE_INVALID", "capture stage update is invalid")
	}
	var capture domain.Capture
	var attempt domain.ProcessingAttempt
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, tx *gorm.DB, _ foundation.TransactionScope) error {
		var err error
		capture, attempt, err = gormLockProcessing(callbackCtx, tx, requested, expectedCaptureVersion)
		if err != nil {
			return err
		}
		query := `UPDATE core.capture SET status='PROCESSING',ingestion_status='RUNNING',index_status='PENDING',profile_status='PENDING',failure_stage='',error_code='',retryable=false,version=version+1,updated_at=? WHERE workspace_id=? AND id=? AND version=? RETURNING ` + captureReturning
		if stage == domain.AttemptStageProfile {
			query = `UPDATE core.capture SET status='PROCESSING',profile_status='RUNNING',failure_stage='',error_code='',retryable=false,version=version+1,updated_at=? WHERE workspace_id=? AND id=? AND version=? RETURNING ` + captureReturning
		}
		row, err := gormCaptureRawRow(tx, query, at.UTC(), string(capture.WorkspaceID), string(capture.ID), capture.Version)
		if err != nil {
			return classifyGORMCapture(callbackCtx, err, "CAPTURE_STAGE_UPDATE_FAILED")
		}
		capture, err = scanCapture(row)
		if err != nil {
			return classifyGORMCapture(callbackCtx, err, "CAPTURE_STAGE_UPDATE_FAILED")
		}
		row, err = gormCaptureRawRow(tx, `UPDATE ops.capture_attempt SET stage=?,version=version+1 WHERE id=? AND version=? AND status='RUNNING' RETURNING `+attemptReturning, string(stage), string(attempt.ID), attempt.Version)
		if err != nil {
			return classifyGORMCapture(callbackCtx, err, "CAPTURE_STAGE_UPDATE_FAILED")
		}
		attempt, err = scanAttempt(row)
		if err != nil {
			return classifyGORMCapture(callbackCtx, err, "CAPTURE_STAGE_UPDATE_FAILED")
		}
		return nil
	})
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classifyGORMCapture(ctx, err, "CAPTURE_STAGE_UPDATE_FAILED")
	}
	return capture, attempt, nil
}

func (repository *GORMRepository) MarkRefreshReady(ctx context.Context, request captureapp.RefreshCheckpoint) (domain.Capture, domain.ProcessingAttempt, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, err
	}
	if request.Attempt.Validate() != nil || request.Attempt.Stage != domain.AttemptStageIngestion || request.Attempt.Status != domain.AttemptStatusRunning || request.ExpectedCaptureVersion < 1 || !validID(request.IngestionAttemptID) || !validID(request.ParseProjectionID) || !validID(request.IndexVersionID) || request.UpdatedAt.IsZero() {
		return domain.Capture{}, domain.ProcessingAttempt{}, invalid("CAPTURE_REFRESH_CHECKPOINT_INVALID", "capture refresh checkpoint is invalid")
	}
	var capture domain.Capture
	var attempt domain.ProcessingAttempt
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, tx *gorm.DB, _ foundation.TransactionScope) error {
		var err error
		capture, attempt, err = gormLockProcessing(callbackCtx, tx, request.Attempt, request.ExpectedCaptureVersion)
		if err != nil {
			return err
		}
		if err = gormValidateRefreshCheckpointBinding(callbackCtx, tx, capture, attempt, request); err != nil {
			return err
		}
		row, err := gormCaptureRawRow(tx, `UPDATE core.capture SET status='PROCESSING',ingestion_status='READY',index_status='READY',profile_status='RUNNING',failure_stage='',error_code='',retryable=false,version=version+1,updated_at=? WHERE workspace_id=? AND id=? AND version=? RETURNING `+captureReturning, request.UpdatedAt.UTC(), string(capture.WorkspaceID), string(capture.ID), capture.Version)
		if err != nil {
			return classifyGORMCapture(callbackCtx, err, "CAPTURE_REFRESH_CHECKPOINT_FAILED")
		}
		capture, err = scanCapture(row)
		if err != nil {
			return classifyGORMCapture(callbackCtx, err, "CAPTURE_REFRESH_CHECKPOINT_FAILED")
		}
		row, err = gormCaptureRawRow(tx, `UPDATE ops.capture_attempt SET ingestion_attempt_id=?,index_version_id=?,stage='PROFILE',version=version+1 WHERE id=? AND version=? AND status='RUNNING' RETURNING `+attemptReturning, string(request.IngestionAttemptID), string(request.IndexVersionID), string(attempt.ID), attempt.Version)
		if err != nil {
			return classifyGORMCapture(callbackCtx, err, "CAPTURE_REFRESH_CHECKPOINT_FAILED")
		}
		attempt, err = scanAttempt(row)
		if err != nil {
			return classifyGORMCapture(callbackCtx, err, "CAPTURE_REFRESH_CHECKPOINT_FAILED")
		}
		return nil
	})
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classifyGORMCapture(ctx, err, "CAPTURE_REFRESH_CHECKPOINT_FAILED")
	}
	return capture, attempt, nil
}

func (repository *GORMRepository) CompleteDegraded(ctx context.Context, request captureapp.DegradedCompletion) (domain.Capture, domain.ProcessingAttempt, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, err
	}
	if request.Attempt.Validate() != nil || request.ExpectedCaptureVersion < 1 || !validID(request.ProfileID) || !request.IngestionStatus.Valid() || !request.IndexStatus.Valid() || (request.ProfileStatus != domain.ProfileStatusCapabilityUnavailable && request.ProfileStatus != domain.ProfileStatusFailed) || !validErrorCode(request.Code) || request.CompletedAt.IsZero() {
		return domain.Capture{}, domain.ProcessingAttempt{}, invalid("CAPTURE_DEGRADED_COMPLETION_INVALID", "capture degraded completion is invalid")
	}
	var capture domain.Capture
	var attempt domain.ProcessingAttempt
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, tx *gorm.DB, _ foundation.TransactionScope) error {
		var err error
		capture, attempt, err = gormLockProcessing(callbackCtx, tx, request.Attempt, request.ExpectedCaptureVersion)
		if err != nil {
			return err
		}
		if capture.LatestSourceVersionID == "" {
			return inconsistent("CAPTURE_DEGRADED_SOURCE_MISSING", "degraded capture has no source version")
		}
		profile, err := gormUpsertUnavailableProfile(callbackCtx, tx, request, capture)
		if err != nil {
			return err
		}
		profileStage := domain.StageCapabilityUnavailable
		if request.ProfileStatus == domain.ProfileStatusFailed {
			profileStage = domain.StageFailed
		}
		failureStage := string(domain.AttemptStageProfile)
		if request.IngestionStatus != domain.StageReady && request.IngestionStatus != domain.StageNotApplicable {
			failureStage = string(domain.AttemptStageIngestion)
		}
		row, err := gormCaptureRawRow(tx, `UPDATE core.capture SET status='READY_DEGRADED',ingestion_status=?,index_status=?,profile_status=?,failure_stage=?,error_code=?,retryable=?,version=version+1,updated_at=? WHERE workspace_id=? AND id=? AND version=? RETURNING `+captureReturning, string(request.IngestionStatus), string(request.IndexStatus), string(profileStage), failureStage, request.Code, request.Retryable, request.CompletedAt.UTC(), string(capture.WorkspaceID), string(capture.ID), capture.Version)
		if err != nil {
			return classifyGORMCapture(callbackCtx, err, "CAPTURE_DEGRADED_COMPLETION_FAILED")
		}
		capture, err = scanCapture(row)
		if err != nil {
			return classifyGORMCapture(callbackCtx, err, "CAPTURE_DEGRADED_COMPLETION_FAILED")
		}
		row, err = gormCaptureRawRow(tx, `UPDATE ops.capture_attempt SET profile_id=?,stage='COMPLETE',status='SUCCEEDED',completed_at=?,version=version+1 WHERE id=? AND version=? AND status='RUNNING' RETURNING `+attemptReturning, string(profile.ID), request.CompletedAt.UTC(), string(attempt.ID), attempt.Version)
		if err != nil {
			return classifyGORMCapture(callbackCtx, err, "CAPTURE_DEGRADED_COMPLETION_FAILED")
		}
		attempt, err = scanAttempt(row)
		if err != nil {
			return classifyGORMCapture(callbackCtx, err, "CAPTURE_DEGRADED_COMPLETION_FAILED")
		}
		return nil
	})
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classifyGORMCapture(ctx, err, "CAPTURE_DEGRADED_COMPLETION_FAILED")
	}
	return capture, attempt, nil
}

func (repository *GORMRepository) CompleteAttempt(ctx context.Context, request captureapp.ReadyCompletion) (domain.Capture, domain.ProcessingAttempt, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, err
	}
	if request.Attempt.Validate() != nil || request.ExpectedCaptureVersion < 1 || !validID(request.ProfileID) || request.CompletedAt.IsZero() {
		return domain.Capture{}, domain.ProcessingAttempt{}, invalid("CAPTURE_COMPLETION_INVALID", "capture completion is invalid")
	}
	var capture domain.Capture
	var attempt domain.ProcessingAttempt
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, tx *gorm.DB, _ foundation.TransactionScope) error {
		var err error
		capture, attempt, err = gormLockProcessing(callbackCtx, tx, request.Attempt, request.ExpectedCaptureVersion)
		if err != nil {
			return err
		}
		var profileStatus string
		row, err := gormCaptureRawRow(tx, `SELECT status FROM learning.document_knowledge_profile WHERE id=? AND workspace_id=? AND capture_id=? AND source_version_id=? FOR SHARE`, string(request.ProfileID), string(capture.WorkspaceID), string(capture.ID), string(capture.LatestSourceVersionID))
		if err != nil {
			return classifyGORMCapture(callbackCtx, err, "CAPTURE_PROFILE_QUERY_FAILED")
		}
		if err = row.Scan(&profileStatus); err != nil {
			return classifyGORMCapture(callbackCtx, err, "CAPTURE_PROFILE_QUERY_FAILED")
		}
		if profileStatus != string(domain.ProfileStatusReady) && profileStatus != string(domain.ProfileStatusStale) {
			return inconsistent("CAPTURE_PROFILE_NOT_READY", "capture profile is not ready")
		}
		profileStage := domain.StageReady
		if profileStatus == string(domain.ProfileStatusStale) {
			profileStage = domain.StageStale
		}
		status, failureStage, errorCode := domain.StatusReady, "", ""
		if request.VectorDegraded {
			status, failureStage, errorCode = domain.StatusReadyDegraded, string(domain.AttemptStageIndex), "CAPTURE_VECTOR_CAPABILITY_UNAVAILABLE"
		}
		row, err = gormCaptureRawRow(tx, `UPDATE core.capture SET status=?,profile_status=?,failure_stage=?,error_code=?,retryable=false,version=version+1,updated_at=? WHERE workspace_id=? AND id=? AND version=? RETURNING `+captureReturning, string(status), string(profileStage), failureStage, errorCode, request.CompletedAt.UTC(), string(capture.WorkspaceID), string(capture.ID), capture.Version)
		if err != nil {
			return classifyGORMCapture(callbackCtx, err, "CAPTURE_COMPLETION_FAILED")
		}
		capture, err = scanCapture(row)
		if err != nil {
			return classifyGORMCapture(callbackCtx, err, "CAPTURE_COMPLETION_FAILED")
		}
		row, err = gormCaptureRawRow(tx, `UPDATE ops.capture_attempt SET profile_id=?,stage='COMPLETE',status='SUCCEEDED',completed_at=?,version=version+1 WHERE id=? AND version=? AND status='RUNNING' RETURNING `+attemptReturning, string(request.ProfileID), request.CompletedAt.UTC(), string(attempt.ID), attempt.Version)
		if err != nil {
			return classifyGORMCapture(callbackCtx, err, "CAPTURE_COMPLETION_FAILED")
		}
		attempt, err = scanAttempt(row)
		if err != nil {
			return classifyGORMCapture(callbackCtx, err, "CAPTURE_COMPLETION_FAILED")
		}
		return nil
	})
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classifyGORMCapture(ctx, err, "CAPTURE_COMPLETION_FAILED")
	}
	return capture, attempt, nil
}

func (repository *GORMRepository) FailAttempt(ctx context.Context, request captureapp.AttemptFailure) (domain.Capture, domain.ProcessingAttempt, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, err
	}
	if request.Attempt.Validate() != nil || request.ExpectedCaptureVersion < 1 || !request.Stage.Valid() || !validErrorCode(request.Code) || request.FailedAt.IsZero() {
		return domain.Capture{}, domain.ProcessingAttempt{}, invalid("CAPTURE_ATTEMPT_FAILURE_INVALID", "capture attempt failure is invalid")
	}
	var capture domain.Capture
	var attempt domain.ProcessingAttempt
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, tx *gorm.DB, _ foundation.TransactionScope) error {
		var err error
		capture, attempt, err = gormLockProcessing(callbackCtx, tx, request.Attempt, request.ExpectedCaptureVersion)
		if err != nil {
			return err
		}
		status, setClause := domain.StatusProcessingFailed, "ingestion_status='FAILED'"
		switch request.Stage {
		case domain.AttemptStageFetch:
			status, setClause = domain.StatusFetchFailed, "fetch_status='FAILED'"
		case domain.AttemptStageIndex:
			setClause = "index_status='FAILED'"
		case domain.AttemptStageProfile:
			status, setClause = domain.StatusReadyDegraded, "profile_status='FAILED'"
		}
		query := fmt.Sprintf(`UPDATE core.capture SET status=?,%s,failure_stage=?,error_code=?,retryable=?,version=version+1,updated_at=? WHERE workspace_id=? AND id=? AND version=? RETURNING %s`, setClause, captureReturning)
		row, err := gormCaptureRawRow(tx, query, string(status), string(request.Stage), request.Code, request.Retryable, request.FailedAt.UTC(), string(capture.WorkspaceID), string(capture.ID), capture.Version)
		if err != nil {
			return classifyGORMCapture(callbackCtx, err, "CAPTURE_ATTEMPT_FAILURE_FAILED")
		}
		capture, err = scanCapture(row)
		if err != nil {
			return classifyGORMCapture(callbackCtx, err, "CAPTURE_ATTEMPT_FAILURE_FAILED")
		}
		row, err = gormCaptureRawRow(tx, `UPDATE ops.capture_attempt SET stage=?,status='FAILED',error_code=?,retryable=?,completed_at=?,version=version+1 WHERE id=? AND version=? AND status='RUNNING' RETURNING `+attemptReturning, string(request.Stage), request.Code, request.Retryable, request.FailedAt.UTC(), string(attempt.ID), attempt.Version)
		if err != nil {
			return classifyGORMCapture(callbackCtx, err, "CAPTURE_ATTEMPT_FAILURE_FAILED")
		}
		attempt, err = scanAttempt(row)
		if err != nil {
			return classifyGORMCapture(callbackCtx, err, "CAPTURE_ATTEMPT_FAILURE_FAILED")
		}
		return nil
	})
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classifyGORMCapture(ctx, err, "CAPTURE_ATTEMPT_FAILURE_FAILED")
	}
	return capture, attempt, nil
}

func gormLockCapture(ctx context.Context, tx *gorm.DB, workspaceID, captureID foundation.ID) (domain.Capture, error) {
	row, err := gormCaptureRawRow(tx.WithContext(ctx), captureSelect+` WHERE capture.workspace_id=? AND capture.id=? FOR UPDATE`, string(workspaceID), string(captureID))
	if err != nil {
		return domain.Capture{}, classifyGORMCapture(ctx, err, "CAPTURE_QUERY_FAILED")
	}
	capture, err := scanCapture(row)
	if gormCaptureNoRows(err) {
		return domain.Capture{}, foundation.NewError(foundation.ErrorNotFound, "CAPTURE_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.Capture{}, classifyGORMCapture(ctx, err, "CAPTURE_QUERY_FAILED")
	}
	return capture, nil
}

func gormLockProcessing(ctx context.Context, tx *gorm.DB, requested domain.ProcessingAttempt, expectedCaptureVersion int64) (domain.Capture, domain.ProcessingAttempt, error) {
	capture, err := gormLockCapture(ctx, tx, requested.WorkspaceID, requested.CaptureID)
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, err
	}
	if capture.Version != expectedCaptureVersion {
		return domain.Capture{}, domain.ProcessingAttempt{}, foundation.NewError(foundation.ErrorVersionConflict, "CAPTURE_VERSION_CONFLICT", true, errors.New("capture version changed"))
	}
	row, err := gormCaptureRawRow(tx.WithContext(ctx), `SELECT `+attemptReturning+` FROM ops.capture_attempt WHERE id=? AND workspace_id=? AND capture_id=? FOR UPDATE`, string(requested.ID), string(requested.WorkspaceID), string(requested.CaptureID))
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classifyGORMCapture(ctx, err, "CAPTURE_ATTEMPT_QUERY_FAILED")
	}
	attempt, err := scanAttempt(row)
	if gormCaptureNoRows(err) {
		return domain.Capture{}, domain.ProcessingAttempt{}, foundation.NewError(foundation.ErrorNotFound, "CAPTURE_ATTEMPT_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classifyGORMCapture(ctx, err, "CAPTURE_ATTEMPT_QUERY_FAILED")
	}
	if attempt.Version != requested.Version || attempt.WorkflowRunID != requested.WorkflowRunID || attempt.Status != domain.AttemptStatusRunning {
		return domain.Capture{}, domain.ProcessingAttempt{}, foundation.NewError(foundation.ErrorVersionConflict, "CAPTURE_ATTEMPT_VERSION_CONFLICT", true, errors.New("capture attempt changed"))
	}
	return capture, attempt, nil
}

func gormValidateRefreshCheckpointBinding(ctx context.Context, tx *gorm.DB, capture domain.Capture, attempt domain.ProcessingAttempt, request captureapp.RefreshCheckpoint) error {
	if capture.LatestSourceVersionID == "" || attempt.SourceVersionID != capture.LatestSourceVersionID {
		return inconsistent("CAPTURE_REFRESH_CHECKPOINT_BINDING_INVALID", "capture attempt is outside the latest source version")
	}
	row, err := gormCaptureRawRow(tx.WithContext(ctx), `SELECT EXISTS (
		SELECT 1 FROM ingestion.attempt AS ingestion_attempt
		JOIN core.source_version AS source_version ON source_version.id=ingestion_attempt.source_version_id AND source_version.workspace_id=ingestion_attempt.workspace_id
		JOIN ingestion.parse_projection AS parse_projection ON parse_projection.id=ingestion_attempt.parse_projection_id AND parse_projection.workspace_id=ingestion_attempt.workspace_id AND parse_projection.content_artifact_id=source_version.content_artifact_id
		JOIN ingestion.source_version_projection AS source_projection ON source_projection.source_version_id=source_version.id AND source_projection.parse_projection_id=parse_projection.id AND source_projection.workspace_id=ingestion_attempt.workspace_id
		JOIN retrieval.index_version AS index_version ON index_version.id=? AND index_version.workspace_id=ingestion_attempt.workspace_id
		JOIN retrieval.index_manifest_source AS manifest_source ON manifest_source.index_version_id=index_version.id AND manifest_source.workspace_id=index_version.workspace_id AND manifest_source.source_id=? AND manifest_source.source_version_id=source_version.id AND manifest_source.parse_projection_id=parse_projection.id AND manifest_source.selection_status='included'
		WHERE ingestion_attempt.id=? AND ingestion_attempt.workspace_id=? AND ingestion_attempt.source_version_id=? AND ingestion_attempt.parse_projection_id=? AND ingestion_attempt.status='chunked' AND ingestion_attempt.security_status='passed' AND index_version.status IN ('ready','active','retiring'))`, string(request.IndexVersionID), string(capture.SourceID), string(request.IngestionAttemptID), string(capture.WorkspaceID), string(capture.LatestSourceVersionID), string(request.ParseProjectionID))
	if err != nil {
		return classifyGORMCapture(ctx, err, "CAPTURE_REFRESH_CHECKPOINT_FAILED")
	}
	var valid bool
	if err = row.Scan(&valid); err != nil {
		return classifyGORMCapture(ctx, err, "CAPTURE_REFRESH_CHECKPOINT_FAILED")
	}
	if !valid {
		return inconsistent("CAPTURE_REFRESH_CHECKPOINT_BINDING_INVALID", "capture ingestion and index checkpoint chain is inconsistent")
	}
	return nil
}

func gormUpsertUnavailableProfile(ctx context.Context, tx *gorm.DB, request captureapp.DegradedCompletion, capture domain.Capture) (domain.Profile, error) {
	result := tx.WithContext(ctx).Exec(`INSERT INTO learning.document_knowledge_profile(id,workspace_id,capture_id,source_version_id,status,error_code,retryable,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,1,?,?) ON CONFLICT (workspace_id,source_version_id) DO NOTHING`, string(request.ProfileID), string(capture.WorkspaceID), string(capture.ID), string(capture.LatestSourceVersionID), string(request.ProfileStatus), request.Code, request.Retryable, request.CompletedAt.UTC(), request.CompletedAt.UTC())
	if result.Error != nil {
		return domain.Profile{}, classifyGORMCapture(ctx, result.Error, "CAPTURE_PROFILE_CREATE_FAILED")
	}
	row, err := gormCaptureRawRow(tx.WithContext(ctx), `SELECT id::text,workspace_id::text,capture_id::text,source_version_id::text,current_revision_id::text,status,error_code,retryable,version,created_at,updated_at FROM learning.document_knowledge_profile WHERE workspace_id=? AND source_version_id=? FOR UPDATE`, string(capture.WorkspaceID), string(capture.LatestSourceVersionID))
	if err != nil {
		return domain.Profile{}, classifyGORMCapture(ctx, err, "CAPTURE_PROFILE_QUERY_FAILED")
	}
	profile, err := scanProfile(row)
	if err != nil {
		return domain.Profile{}, classifyGORMCapture(ctx, err, "CAPTURE_PROFILE_QUERY_FAILED")
	}
	if profile.CaptureID != capture.ID || (profile.ID != request.ProfileID && profile.Status == domain.ProfileStatusReady) {
		return domain.Profile{}, foundation.NewError(foundation.ErrorVersionConflict, "CAPTURE_PROFILE_REPLAY_CONFLICT", false, errors.New("capture profile is already bound to another result"))
	}
	if profile.Status != request.ProfileStatus || profile.ErrorCode != request.Code || profile.Retryable != request.Retryable {
		row, err = gormCaptureRawRow(tx.WithContext(ctx), `UPDATE learning.document_knowledge_profile SET status=?,error_code=?,retryable=?,version=version+1,updated_at=? WHERE id=? AND workspace_id=? AND version=? AND status<>'READY' RETURNING id::text,workspace_id::text,capture_id::text,source_version_id::text,current_revision_id::text,status,error_code,retryable,version,created_at,updated_at`, string(request.ProfileStatus), request.Code, request.Retryable, request.CompletedAt.UTC(), string(profile.ID), string(profile.WorkspaceID), profile.Version)
		if err != nil {
			return domain.Profile{}, classifyGORMCapture(ctx, err, "CAPTURE_PROFILE_UPDATE_FAILED")
		}
		profile, err = scanProfile(row)
		if gormCaptureNoRows(err) {
			return domain.Profile{}, foundation.NewError(foundation.ErrorVersionConflict, "CAPTURE_PROFILE_UPDATE_FAILED", true, errors.New("profile changed before degraded completion"))
		}
		if err != nil {
			return domain.Profile{}, classifyGORMCapture(ctx, err, "CAPTURE_PROFILE_UPDATE_FAILED")
		}
	}
	return profile, nil
}
