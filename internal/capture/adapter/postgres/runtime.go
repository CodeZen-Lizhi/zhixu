package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const captureReturning = `id::text,workspace_id::text,kind,display_name,original_location,
	original_url,original_input_hash,source_id::text,latest_source_version_id::text,status,
	fetch_status,ingestion_status,index_status,profile_status,failure_stage,error_code,retryable,
	version,captured_at,updated_at`

const attemptReturning = `id::text,workspace_id::text,capture_id::text,source_version_id::text,
	workflow_run_id::text,ingestion_attempt_id::text,index_version_id::text,profile_id::text,
	attempt_number,stage,status,error_code,retryable,started_at,completed_at,version`

// ClaimNext takes one due Capture event with a DB-time lease.
func (repository *Repository) ClaimNext(ctx context.Context, owner string, leaseDuration time.Duration) (captureapp.OutboxLease, bool, error) {
	owner = strings.TrimSpace(owner)
	if repository == nil || repository.db == nil || owner == "" || len(owner) > 256 || leaseDuration <= 0 {
		return captureapp.OutboxLease{}, false, invalid("CAPTURE_OUTBOX_CLAIM_INVALID", "capture outbox claim is invalid")
	}
	var lease captureapp.OutboxLease
	var id, workspaceID, captureID string
	err := repository.db.QueryRow(ctx, `
		WITH candidate AS (
			SELECT id FROM ops.capture_outbox
			WHERE published_at IS NULL AND poisoned_at IS NULL AND available_at<=clock_timestamp()
			  AND (lease_expires_at IS NULL OR lease_expires_at<=clock_timestamp())
			ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT 1
		)
		UPDATE ops.capture_outbox AS event
		SET lease_owner=$1,
			lease_expires_at=clock_timestamp()+($2::bigint*interval '1 millisecond'),
			attempt_count=event.attempt_count+1,version=event.version+1,updated_at=clock_timestamp()
		FROM candidate WHERE event.id=candidate.id
		RETURNING event.id::text,event.workspace_id::text,event.capture_id::text,event.attempt_count,
			event.version,event.lease_expires_at`, owner, leaseDuration.Milliseconds()).
		Scan(&id, &workspaceID, &captureID, &lease.AttemptCount, &lease.Version, &lease.LeaseUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return captureapp.OutboxLease{}, false, nil
	}
	if err != nil {
		return captureapp.OutboxLease{}, false, classify(err, "CAPTURE_OUTBOX_CLAIM_FAILED")
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

// MarkPublished completes an owned outbox lease after a Workflow start or exact replay.
func (repository *Repository) MarkPublished(ctx context.Context, lease captureapp.OutboxLease, workflowRunID foundation.ID) error {
	if err := validateLease(lease); err != nil || !validID(workflowRunID) {
		return invalid("CAPTURE_OUTBOX_COMPLETE_INVALID", "capture outbox completion is invalid")
	}
	tag, err := repository.db.Exec(ctx, `
		UPDATE ops.capture_outbox
		SET published_at=clock_timestamp(),lease_owner=NULL,lease_expires_at=NULL,
			payload=jsonb_set(payload,'{workflow_run_id}',to_jsonb($5::text),true),
			version=version+1,updated_at=clock_timestamp()
		WHERE id=$1 AND workspace_id=$2 AND lease_owner=$3 AND version=$4
		  AND lease_expires_at>clock_timestamp() AND published_at IS NULL AND poisoned_at IS NULL`,
		string(lease.ID), string(lease.WorkspaceID), lease.Owner, lease.Version, string(workflowRunID))
	if err != nil {
		return classify(err, "CAPTURE_OUTBOX_COMPLETE_FAILED")
	}
	if tag.RowsAffected() != 1 {
		return foundation.NewError(foundation.ErrorVersionConflict, "CAPTURE_OUTBOX_LEASE_LOST", true, errors.New("capture outbox lease was lost"))
	}
	return nil
}

// Reschedule releases an owned outbox lease for a bounded retry.
func (repository *Repository) Reschedule(ctx context.Context, lease captureapp.OutboxLease, code string, delay time.Duration) error {
	code = strings.TrimSpace(code)
	if validateLease(lease) != nil || !validErrorCode(code) || delay <= 0 {
		return invalid("CAPTURE_OUTBOX_RETRY_INVALID", "capture outbox retry is invalid")
	}
	tag, err := repository.db.Exec(ctx, `
		UPDATE ops.capture_outbox
		SET available_at=clock_timestamp()+($5::bigint*interval '1 millisecond'),last_error_code=$4,
			lease_owner=NULL,lease_expires_at=NULL,version=version+1,updated_at=clock_timestamp()
		WHERE id=$1 AND workspace_id=$2 AND lease_owner=$3 AND version=$6
		  AND published_at IS NULL AND poisoned_at IS NULL`,
		string(lease.ID), string(lease.WorkspaceID), lease.Owner, code, delay.Milliseconds(), lease.Version)
	if err != nil {
		return classify(err, "CAPTURE_OUTBOX_RETRY_FAILED")
	}
	if tag.RowsAffected() != 1 {
		return foundation.NewError(foundation.ErrorVersionConflict, "CAPTURE_OUTBOX_LEASE_LOST", true, errors.New("capture outbox lease was lost"))
	}
	return nil
}

// Poison makes a non-retryable outbox failure visible for manual recovery.
func (repository *Repository) Poison(ctx context.Context, lease captureapp.OutboxLease, code string) error {
	code = strings.TrimSpace(code)
	if validateLease(lease) != nil || !validErrorCode(code) {
		return invalid("CAPTURE_OUTBOX_POISON_INVALID", "capture outbox poison request is invalid")
	}
	tag, err := repository.db.Exec(ctx, `
		UPDATE ops.capture_outbox
		SET poisoned_at=clock_timestamp(),manual_recovery_required=true,last_error_code=$4,
			lease_owner=NULL,lease_expires_at=NULL,version=version+1,updated_at=clock_timestamp()
		WHERE id=$1 AND workspace_id=$2 AND lease_owner=$3 AND version=$5
		  AND published_at IS NULL AND poisoned_at IS NULL`,
		string(lease.ID), string(lease.WorkspaceID), lease.Owner, code, lease.Version)
	if err != nil {
		return classify(err, "CAPTURE_OUTBOX_POISON_FAILED")
	}
	if tag.RowsAffected() != 1 {
		return foundation.NewError(foundation.ErrorVersionConflict, "CAPTURE_OUTBOX_LEASE_LOST", true, errors.New("capture outbox lease was lost"))
	}
	return nil
}

// BeginAttempt creates one idempotent Capture checkpoint row for a Workflow attempt.
func (repository *Repository) BeginAttempt(ctx context.Context, request captureapp.BeginAttemptRequest) (domain.Capture, domain.ProcessingAttempt, error) {
	if !validID(request.ID) || !validID(request.WorkspaceID) || !validID(request.CaptureID) ||
		!validID(request.WorkflowRunID) || request.AttemptNumber < 1 || request.StartedAt.IsZero() {
		return domain.Capture{}, domain.ProcessingAttempt{}, invalid("CAPTURE_ATTEMPT_BEGIN_INVALID", "capture processing attempt is invalid")
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classify(err, "CAPTURE_ATTEMPT_BEGIN_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	capture, err := lockCapture(ctx, tx, request.WorkspaceID, request.CaptureID)
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, err
	}
	stage := domain.AttemptStageIngestion
	if capture.Kind == domain.KindURL && capture.LatestSourceVersionID == "" {
		stage = domain.AttemptStageFetch
	}
	var insertedID string
	insertErr := tx.QueryRow(ctx, `
		INSERT INTO ops.capture_attempt(
			id,workspace_id,capture_id,source_version_id,workflow_run_id,attempt_number,
			stage,status,error_code,retryable,started_at,version
		) VALUES($1,$2,$3,NULLIF($4,'')::uuid,$5,$6,$7,'RUNNING','',false,$8,1)
		ON CONFLICT (capture_id,workflow_run_id,attempt_number) DO NOTHING RETURNING id::text`,
		string(request.ID), string(request.WorkspaceID), string(request.CaptureID),
		string(capture.LatestSourceVersionID), string(request.WorkflowRunID), request.AttemptNumber,
		string(stage), request.StartedAt.UTC()).Scan(&insertedID)
	created := insertErr == nil
	if insertErr != nil && !errors.Is(insertErr, pgx.ErrNoRows) {
		return domain.Capture{}, domain.ProcessingAttempt{}, classify(insertErr, "CAPTURE_ATTEMPT_BEGIN_FAILED")
	}
	attempt, err := scanAttempt(tx.QueryRow(ctx, `SELECT `+attemptReturning+`
		FROM ops.capture_attempt WHERE capture_id=$1 AND workflow_run_id=$2 AND attempt_number=$3 FOR UPDATE`,
		string(request.CaptureID), string(request.WorkflowRunID), request.AttemptNumber))
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classify(err, "CAPTURE_ATTEMPT_QUERY_FAILED")
	}
	if attempt.ID != request.ID && created {
		return domain.Capture{}, domain.ProcessingAttempt{}, inconsistent("CAPTURE_ATTEMPT_BINDING_INVALID", "capture attempt identity drifted")
	}
	if attempt.WorkspaceID != request.WorkspaceID || attempt.CaptureID != request.CaptureID ||
		attempt.WorkflowRunID != request.WorkflowRunID || attempt.AttemptNumber != request.AttemptNumber {
		return domain.Capture{}, domain.ProcessingAttempt{}, foundation.NewError(foundation.ErrorVersionConflict, "CAPTURE_ATTEMPT_REPLAY_CONFLICT", false, errors.New("capture attempt is bound to another workflow"))
	}
	if created {
		status := domain.StatusProcessing
		fetchStatus := capture.FetchStatus
		ingestionStatus := domain.StageRunning
		if stage == domain.AttemptStageFetch {
			status = domain.StatusFetching
			fetchStatus = domain.StageRunning
			ingestionStatus = domain.StagePending
		}
		capture, err = scanCapture(tx.QueryRow(ctx, `UPDATE core.capture SET
			status=$3,fetch_status=$4,ingestion_status=$5,index_status='PENDING',profile_status='PENDING',
			failure_stage='',error_code='',retryable=false,version=version+1,updated_at=$6
			WHERE workspace_id=$1 AND id=$2 AND version=$7 RETURNING `+captureReturning,
			string(capture.WorkspaceID), string(capture.ID), string(status), string(fetchStatus),
			string(ingestionStatus), request.StartedAt.UTC(), capture.Version))
		if err != nil {
			return domain.Capture{}, domain.ProcessingAttempt{}, classify(err, "CAPTURE_ATTEMPT_BEGIN_FAILED")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classify(err, "CAPTURE_ATTEMPT_BEGIN_FAILED")
	}
	return capture, attempt, nil
}

// MaterializeURL atomically registers fetched bytes and advances the Capture to ingestion.
func (repository *Repository) MaterializeURL(ctx context.Context, request captureapp.MaterializeURLRequest) (domain.Capture, domain.ProcessingAttempt, error) {
	if request.Attempt.Validate() != nil || request.Attempt.Stage != domain.AttemptStageFetch ||
		request.ExpectedCaptureVersion < 1 || !validID(request.ArtifactID) || !validID(request.SourceVersionID) ||
		!validHash(request.ContentHash) || request.ByteSize < 1 || strings.TrimSpace(request.MediaType) == "" ||
		strings.TrimSpace(request.OriginalContentLocation) == "" || strings.TrimSpace(request.ManagedLocation) == "" || request.CapturedAt.IsZero() {
		return domain.Capture{}, domain.ProcessingAttempt{}, invalid("CAPTURE_URL_MATERIALIZATION_INVALID", "capture URL materialization is invalid")
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classify(err, "CAPTURE_URL_MATERIALIZATION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	capture, attempt, err := lockProcessing(ctx, tx, request.Attempt, request.ExpectedCaptureVersion)
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, err
	}
	if capture.Kind != domain.KindURL || capture.LatestSourceVersionID != "" || capture.FetchStatus != domain.StageRunning {
		return domain.Capture{}, domain.ProcessingAttempt{}, foundation.NewError(foundation.ErrorVersionConflict, "CAPTURE_URL_ALREADY_MATERIALIZED", false, errors.New("capture URL cannot be materialized from its current state"))
	}
	registration := workspacedomain.SourceRegistration{
		Source: workspacedomain.Source{
			ID: capture.SourceID, WorkspaceID: capture.WorkspaceID, Type: "quick_capture_url",
			LogicalName: capture.DisplayName, OriginalLocation: capture.OriginalLocation, CreatedAt: capture.CapturedAt,
		},
		Artifact: workspacedomain.ContentArtifact{
			ID: request.ArtifactID, WorkspaceID: capture.WorkspaceID, ContentHash: request.ContentHash,
			ByteSize: request.ByteSize, ManagedLocation: request.ManagedLocation, CreatedAt: request.CapturedAt,
		},
		Version: workspacedomain.SourceVersion{
			ID: request.SourceVersionID, SourceID: capture.SourceID, ContentArtifactID: request.ArtifactID,
			ContentHash: request.ContentHash, ByteSize: request.ByteSize, MediaType: request.MediaType,
			OriginalContentLocation: request.OriginalContentLocation, SecurityStatus: "pending", CapturedAt: request.CapturedAt,
		},
	}
	registered, err := repository.workspaceWriter.RegisterSourceVersion(ctx, tx, registration)
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, err
	}
	if registered.Source.ID != capture.SourceID || registered.Version.ID != request.SourceVersionID {
		return domain.Capture{}, domain.ProcessingAttempt{}, inconsistent("CAPTURE_URL_SOURCE_BINDING_INVALID", "workspace URL registration returned a different binding")
	}
	capture, err = scanCapture(tx.QueryRow(ctx, `UPDATE core.capture SET
		latest_source_version_id=$3,status='PROCESSING',fetch_status='READY',ingestion_status='RUNNING',
		index_status='PENDING',profile_status='PENDING',failure_stage='',error_code='',retryable=false,
		version=version+1,updated_at=$4 WHERE workspace_id=$1 AND id=$2 AND version=$5 RETURNING `+captureReturning,
		string(capture.WorkspaceID), string(capture.ID), string(request.SourceVersionID), request.CapturedAt.UTC(), capture.Version))
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classify(err, "CAPTURE_URL_MATERIALIZATION_FAILED")
	}
	attempt, err = scanAttempt(tx.QueryRow(ctx, `UPDATE ops.capture_attempt SET
		source_version_id=$2,stage='INGESTION',version=version+1
		WHERE id=$1 AND version=$3 AND status='RUNNING' RETURNING `+attemptReturning,
		string(attempt.ID), string(request.SourceVersionID), attempt.Version))
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classify(err, "CAPTURE_URL_MATERIALIZATION_FAILED")
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classify(err, "CAPTURE_URL_MATERIALIZATION_FAILED")
	}
	return capture, attempt, nil
}

// MarkRefreshRunning persists a resumable ingestion checkpoint.
func (repository *Repository) MarkRefreshRunning(ctx context.Context, requestAttempt domain.ProcessingAttempt, expectedCaptureVersion int64, at time.Time) (domain.Capture, domain.ProcessingAttempt, error) {
	return repository.updateRunningStage(ctx, requestAttempt, expectedCaptureVersion, domain.AttemptStageIngestion, at)
}

// MarkProfileRunning persists successful ingestion/index bindings before model work.
func (repository *Repository) MarkProfileRunning(ctx context.Context, requestAttempt domain.ProcessingAttempt, expectedCaptureVersion int64, at time.Time) (domain.Capture, domain.ProcessingAttempt, error) {
	return repository.updateRunningStage(ctx, requestAttempt, expectedCaptureVersion, domain.AttemptStageProfile, at)
}

func (repository *Repository) updateRunningStage(ctx context.Context, requested domain.ProcessingAttempt, expectedCaptureVersion int64, stage domain.AttemptStage, at time.Time) (domain.Capture, domain.ProcessingAttempt, error) {
	if requested.Validate() != nil || requested.Status != domain.AttemptStatusRunning || expectedCaptureVersion < 1 || at.IsZero() {
		return domain.Capture{}, domain.ProcessingAttempt{}, invalid("CAPTURE_STAGE_UPDATE_INVALID", "capture stage update is invalid")
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classify(err, "CAPTURE_STAGE_UPDATE_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	capture, attempt, err := lockProcessing(ctx, tx, requested, expectedCaptureVersion)
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, err
	}
	if stage == domain.AttemptStageIngestion {
		capture, err = scanCapture(tx.QueryRow(ctx, `UPDATE core.capture SET
			status='PROCESSING',ingestion_status='RUNNING',index_status='PENDING',profile_status='PENDING',
			failure_stage='',error_code='',retryable=false,version=version+1,updated_at=$3
			WHERE workspace_id=$1 AND id=$2 AND version=$4 RETURNING `+captureReturning,
			string(capture.WorkspaceID), string(capture.ID), at.UTC(), capture.Version))
	} else {
		capture, err = scanCapture(tx.QueryRow(ctx, `UPDATE core.capture SET
			status='PROCESSING',profile_status='RUNNING',failure_stage='',error_code='',retryable=false,
			version=version+1,updated_at=$3 WHERE workspace_id=$1 AND id=$2 AND version=$4 RETURNING `+captureReturning,
			string(capture.WorkspaceID), string(capture.ID), at.UTC(), capture.Version))
	}
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classify(err, "CAPTURE_STAGE_UPDATE_FAILED")
	}
	attempt, err = scanAttempt(tx.QueryRow(ctx, `UPDATE ops.capture_attempt SET stage=$2,version=version+1
		WHERE id=$1 AND version=$3 AND status='RUNNING' RETURNING `+attemptReturning,
		string(attempt.ID), string(stage), attempt.Version))
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classify(err, "CAPTURE_STAGE_UPDATE_FAILED")
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classify(err, "CAPTURE_STAGE_UPDATE_FAILED")
	}
	return capture, attempt, nil
}

// MarkRefreshReady freezes successful ingestion and an eligible immutable index identity.
func (repository *Repository) MarkRefreshReady(ctx context.Context, request captureapp.RefreshCheckpoint) (domain.Capture, domain.ProcessingAttempt, error) {
	if request.Attempt.Validate() != nil || request.Attempt.Stage != domain.AttemptStageIngestion ||
		request.Attempt.Status != domain.AttemptStatusRunning || request.ExpectedCaptureVersion < 1 || !validID(request.IngestionAttemptID) ||
		!validID(request.ParseProjectionID) || !validID(request.IndexVersionID) || request.UpdatedAt.IsZero() {
		return domain.Capture{}, domain.ProcessingAttempt{}, invalid("CAPTURE_REFRESH_CHECKPOINT_INVALID", "capture refresh checkpoint is invalid")
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classify(err, "CAPTURE_REFRESH_CHECKPOINT_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	capture, attempt, err := lockProcessing(ctx, tx, request.Attempt, request.ExpectedCaptureVersion)
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, err
	}
	if err := validateRefreshCheckpointBinding(ctx, tx, capture, attempt, request); err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, err
	}
	capture, err = scanCapture(tx.QueryRow(ctx, `UPDATE core.capture SET
		status='PROCESSING',ingestion_status='READY',index_status='READY',profile_status='RUNNING',
		failure_stage='',error_code='',retryable=false,version=version+1,updated_at=$3
		WHERE workspace_id=$1 AND id=$2 AND version=$4 RETURNING `+captureReturning,
		string(capture.WorkspaceID), string(capture.ID), request.UpdatedAt.UTC(), capture.Version))
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classify(err, "CAPTURE_REFRESH_CHECKPOINT_FAILED")
	}
	attempt, err = scanAttempt(tx.QueryRow(ctx, `UPDATE ops.capture_attempt SET
		ingestion_attempt_id=$2,index_version_id=$3,stage='PROFILE',version=version+1
		WHERE id=$1 AND version=$4 AND status='RUNNING' RETURNING `+attemptReturning,
		string(attempt.ID), string(request.IngestionAttemptID), string(request.IndexVersionID), attempt.Version))
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classify(err, "CAPTURE_REFRESH_CHECKPOINT_FAILED")
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classify(err, "CAPTURE_REFRESH_CHECKPOINT_FAILED")
	}
	return capture, attempt, nil
}

// CompleteDegraded preserves a safe original while recording an unavailable profile projection.
func (repository *Repository) CompleteDegraded(ctx context.Context, request captureapp.DegradedCompletion) (domain.Capture, domain.ProcessingAttempt, error) {
	if request.Attempt.Validate() != nil || request.ExpectedCaptureVersion < 1 || !validID(request.ProfileID) ||
		!request.IngestionStatus.Valid() || !request.IndexStatus.Valid() ||
		(request.ProfileStatus != domain.ProfileStatusCapabilityUnavailable && request.ProfileStatus != domain.ProfileStatusFailed) ||
		!validErrorCode(request.Code) || request.CompletedAt.IsZero() {
		return domain.Capture{}, domain.ProcessingAttempt{}, invalid("CAPTURE_DEGRADED_COMPLETION_INVALID", "capture degraded completion is invalid")
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classify(err, "CAPTURE_DEGRADED_COMPLETION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	capture, attempt, err := lockProcessing(ctx, tx, request.Attempt, request.ExpectedCaptureVersion)
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, err
	}
	if capture.LatestSourceVersionID == "" {
		return domain.Capture{}, domain.ProcessingAttempt{}, inconsistent("CAPTURE_DEGRADED_SOURCE_MISSING", "degraded capture has no source version")
	}
	profile, err := upsertUnavailableProfile(ctx, tx, request, capture)
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, err
	}
	profileStage := domain.StageCapabilityUnavailable
	if request.ProfileStatus == domain.ProfileStatusFailed {
		profileStage = domain.StageFailed
	}
	failureStage := string(domain.AttemptStageProfile)
	if request.IngestionStatus != domain.StageReady && request.IngestionStatus != domain.StageNotApplicable {
		failureStage = string(domain.AttemptStageIngestion)
	}
	capture, err = scanCapture(tx.QueryRow(ctx, `UPDATE core.capture SET
		status='READY_DEGRADED',ingestion_status=$3,index_status=$4,profile_status=$5,
		failure_stage=$6,error_code=$7,retryable=$8,version=version+1,updated_at=$9
		WHERE workspace_id=$1 AND id=$2 AND version=$10 RETURNING `+captureReturning,
		string(capture.WorkspaceID), string(capture.ID), string(request.IngestionStatus), string(request.IndexStatus),
		string(profileStage), failureStage, request.Code, request.Retryable, request.CompletedAt.UTC(), capture.Version))
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classify(err, "CAPTURE_DEGRADED_COMPLETION_FAILED")
	}
	attempt, err = scanAttempt(tx.QueryRow(ctx, `UPDATE ops.capture_attempt SET
		profile_id=$2,stage='COMPLETE',status='SUCCEEDED',completed_at=$3,version=version+1
		WHERE id=$1 AND version=$4 AND status='RUNNING' RETURNING `+attemptReturning,
		string(attempt.ID), string(profile.ID), request.CompletedAt.UTC(), attempt.Version))
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classify(err, "CAPTURE_DEGRADED_COMPLETION_FAILED")
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classify(err, "CAPTURE_DEGRADED_COMPLETION_FAILED")
	}
	return capture, attempt, nil
}

// CompleteAttempt completes a Capture after a durable READY or STALE Profile revision.
func (repository *Repository) CompleteAttempt(ctx context.Context, request captureapp.ReadyCompletion) (domain.Capture, domain.ProcessingAttempt, error) {
	if request.Attempt.Validate() != nil || request.ExpectedCaptureVersion < 1 || !validID(request.ProfileID) || request.CompletedAt.IsZero() {
		return domain.Capture{}, domain.ProcessingAttempt{}, invalid("CAPTURE_COMPLETION_INVALID", "capture completion is invalid")
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classify(err, "CAPTURE_COMPLETION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	capture, attempt, err := lockProcessing(ctx, tx, request.Attempt, request.ExpectedCaptureVersion)
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, err
	}
	var profileStatus string
	if err := tx.QueryRow(ctx, `SELECT status FROM learning.document_knowledge_profile
		WHERE id=$1 AND workspace_id=$2 AND capture_id=$3 AND source_version_id=$4 FOR SHARE`,
		string(request.ProfileID), string(capture.WorkspaceID), string(capture.ID), string(capture.LatestSourceVersionID)).Scan(&profileStatus); err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classify(err, "CAPTURE_PROFILE_QUERY_FAILED")
	}
	if profileStatus != string(domain.ProfileStatusReady) && profileStatus != string(domain.ProfileStatusStale) {
		return domain.Capture{}, domain.ProcessingAttempt{}, inconsistent("CAPTURE_PROFILE_NOT_READY", "capture profile is not ready")
	}
	profileStage := domain.StageReady
	if profileStatus == string(domain.ProfileStatusStale) {
		profileStage = domain.StageStale
	}
	status := domain.StatusReady
	failureStage := ""
	errorCode := ""
	if request.VectorDegraded {
		status = domain.StatusReadyDegraded
		failureStage = string(domain.AttemptStageIndex)
		errorCode = "CAPTURE_VECTOR_CAPABILITY_UNAVAILABLE"
	}
	capture, err = scanCapture(tx.QueryRow(ctx, `UPDATE core.capture SET
		status=$3,profile_status=$4,failure_stage=$5,error_code=$6,retryable=false,
		version=version+1,updated_at=$7 WHERE workspace_id=$1 AND id=$2 AND version=$8 RETURNING `+captureReturning,
		string(capture.WorkspaceID), string(capture.ID), string(status), string(profileStage), failureStage, errorCode,
		request.CompletedAt.UTC(), capture.Version))
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classify(err, "CAPTURE_COMPLETION_FAILED")
	}
	attempt, err = scanAttempt(tx.QueryRow(ctx, `UPDATE ops.capture_attempt SET
		profile_id=$2,stage='COMPLETE',status='SUCCEEDED',completed_at=$3,version=version+1
		WHERE id=$1 AND version=$4 AND status='RUNNING' RETURNING `+attemptReturning,
		string(attempt.ID), string(request.ProfileID), request.CompletedAt.UTC(), attempt.Version))
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classify(err, "CAPTURE_COMPLETION_FAILED")
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classify(err, "CAPTURE_COMPLETION_FAILED")
	}
	return capture, attempt, nil
}

func validateRefreshCheckpointBinding(
	ctx context.Context,
	tx pgx.Tx,
	capture domain.Capture,
	attempt domain.ProcessingAttempt,
	request captureapp.RefreshCheckpoint,
) error {
	if capture.LatestSourceVersionID == "" || attempt.SourceVersionID != capture.LatestSourceVersionID {
		return inconsistent("CAPTURE_REFRESH_CHECKPOINT_BINDING_INVALID", "capture attempt is outside the latest source version")
	}
	var valid bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1
		FROM ingestion.attempt AS ingestion_attempt
		JOIN core.source_version AS source_version
		  ON source_version.id=ingestion_attempt.source_version_id
		 AND source_version.workspace_id=ingestion_attempt.workspace_id
		JOIN ingestion.parse_projection AS parse_projection
		  ON parse_projection.id=ingestion_attempt.parse_projection_id
		 AND parse_projection.workspace_id=ingestion_attempt.workspace_id
		 AND parse_projection.content_artifact_id=source_version.content_artifact_id
		JOIN ingestion.source_version_projection AS source_projection
		  ON source_projection.source_version_id=source_version.id
		 AND source_projection.parse_projection_id=parse_projection.id
		 AND source_projection.workspace_id=ingestion_attempt.workspace_id
		JOIN retrieval.index_version AS index_version
		  ON index_version.id=$5 AND index_version.workspace_id=ingestion_attempt.workspace_id
		JOIN retrieval.index_manifest_source AS manifest_source
		  ON manifest_source.index_version_id=index_version.id
		 AND manifest_source.workspace_id=index_version.workspace_id
		 AND manifest_source.source_id=$6
		 AND manifest_source.source_version_id=source_version.id
		 AND manifest_source.parse_projection_id=parse_projection.id
		 AND manifest_source.selection_status='included'
		WHERE ingestion_attempt.id=$1
		  AND ingestion_attempt.workspace_id=$2
		  AND ingestion_attempt.source_version_id=$3
		  AND ingestion_attempt.parse_projection_id=$4
		  AND ingestion_attempt.status='chunked'
		  AND ingestion_attempt.security_status='passed'
		  AND index_version.status IN ('ready','active','retiring')
		)`, string(request.IngestionAttemptID), string(capture.WorkspaceID), string(capture.LatestSourceVersionID),
		string(request.ParseProjectionID), string(request.IndexVersionID), string(capture.SourceID)).Scan(&valid)
	if err != nil {
		return classify(err, "CAPTURE_REFRESH_CHECKPOINT_FAILED")
	}
	if !valid {
		return inconsistent("CAPTURE_REFRESH_CHECKPOINT_BINDING_INVALID", "capture ingestion and index checkpoint chain is inconsistent")
	}
	return nil
}

// FailAttempt persists the exact failed layer without erasing completed lower layers.
func (repository *Repository) FailAttempt(ctx context.Context, request captureapp.AttemptFailure) (domain.Capture, domain.ProcessingAttempt, error) {
	if request.Attempt.Validate() != nil || request.ExpectedCaptureVersion < 1 || !request.Stage.Valid() ||
		!validErrorCode(request.Code) || request.FailedAt.IsZero() {
		return domain.Capture{}, domain.ProcessingAttempt{}, invalid("CAPTURE_ATTEMPT_FAILURE_INVALID", "capture attempt failure is invalid")
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classify(err, "CAPTURE_ATTEMPT_FAILURE_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	capture, attempt, err := lockProcessing(ctx, tx, request.Attempt, request.ExpectedCaptureVersion)
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, err
	}
	status := domain.StatusProcessingFailed
	setClause := "ingestion_status='FAILED'"
	switch request.Stage {
	case domain.AttemptStageFetch:
		status = domain.StatusFetchFailed
		setClause = "fetch_status='FAILED'"
	case domain.AttemptStageIndex:
		setClause = "index_status='FAILED'"
	case domain.AttemptStageProfile:
		status = domain.StatusReadyDegraded
		setClause = "profile_status='FAILED'"
	}
	query := fmt.Sprintf(`UPDATE core.capture SET status=$3,%s,failure_stage=$4,error_code=$5,retryable=$6,
		version=version+1,updated_at=$7 WHERE workspace_id=$1 AND id=$2 AND version=$8 RETURNING %s`, setClause, captureReturning)
	capture, err = scanCapture(tx.QueryRow(ctx, query, string(capture.WorkspaceID), string(capture.ID),
		string(status), string(request.Stage), request.Code, request.Retryable, request.FailedAt.UTC(), capture.Version))
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classify(err, "CAPTURE_ATTEMPT_FAILURE_FAILED")
	}
	attempt, err = scanAttempt(tx.QueryRow(ctx, `UPDATE ops.capture_attempt SET
		stage=$2,status='FAILED',error_code=$3,retryable=$4,completed_at=$5,version=version+1
		WHERE id=$1 AND version=$6 AND status='RUNNING' RETURNING `+attemptReturning,
		string(attempt.ID), string(request.Stage), request.Code, request.Retryable, request.FailedAt.UTC(), attempt.Version))
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classify(err, "CAPTURE_ATTEMPT_FAILURE_FAILED")
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classify(err, "CAPTURE_ATTEMPT_FAILURE_FAILED")
	}
	return capture, attempt, nil
}

func lockCapture(ctx context.Context, tx pgx.Tx, workspaceID, captureID foundation.ID) (domain.Capture, error) {
	capture, err := scanCapture(tx.QueryRow(ctx, captureSelect+` WHERE capture.workspace_id=$1 AND capture.id=$2 FOR UPDATE`, string(workspaceID), string(captureID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Capture{}, foundation.NewError(foundation.ErrorNotFound, "CAPTURE_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.Capture{}, classify(err, "CAPTURE_QUERY_FAILED")
	}
	return capture, nil
}

func lockProcessing(ctx context.Context, tx pgx.Tx, requested domain.ProcessingAttempt, expectedCaptureVersion int64) (domain.Capture, domain.ProcessingAttempt, error) {
	capture, err := lockCapture(ctx, tx, requested.WorkspaceID, requested.CaptureID)
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, err
	}
	if capture.Version != expectedCaptureVersion {
		return domain.Capture{}, domain.ProcessingAttempt{}, foundation.NewError(foundation.ErrorVersionConflict, "CAPTURE_VERSION_CONFLICT", true, errors.New("capture version changed"))
	}
	attempt, err := scanAttempt(tx.QueryRow(ctx, `SELECT `+attemptReturning+` FROM ops.capture_attempt
		WHERE id=$1 AND workspace_id=$2 AND capture_id=$3 FOR UPDATE`, string(requested.ID), string(requested.WorkspaceID), string(requested.CaptureID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Capture{}, domain.ProcessingAttempt{}, foundation.NewError(foundation.ErrorNotFound, "CAPTURE_ATTEMPT_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.Capture{}, domain.ProcessingAttempt{}, classify(err, "CAPTURE_ATTEMPT_QUERY_FAILED")
	}
	if attempt.Version != requested.Version || attempt.WorkflowRunID != requested.WorkflowRunID || attempt.Status != domain.AttemptStatusRunning {
		return domain.Capture{}, domain.ProcessingAttempt{}, foundation.NewError(foundation.ErrorVersionConflict, "CAPTURE_ATTEMPT_VERSION_CONFLICT", true, errors.New("capture attempt changed"))
	}
	return capture, attempt, nil
}

func upsertUnavailableProfile(ctx context.Context, tx pgx.Tx, request captureapp.DegradedCompletion, capture domain.Capture) (domain.Profile, error) {
	_, err := tx.Exec(ctx, `INSERT INTO learning.document_knowledge_profile(
		id,workspace_id,capture_id,source_version_id,status,error_code,retryable,version,created_at,updated_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,1,$8,$8)
	ON CONFLICT (workspace_id,source_version_id) DO NOTHING`,
		string(request.ProfileID), string(capture.WorkspaceID), string(capture.ID), string(capture.LatestSourceVersionID),
		string(request.ProfileStatus), request.Code, request.Retryable, request.CompletedAt.UTC())
	if err != nil {
		return domain.Profile{}, classify(err, "CAPTURE_PROFILE_CREATE_FAILED")
	}
	profile, err := scanProfile(tx.QueryRow(ctx, `SELECT id::text,workspace_id::text,capture_id::text,source_version_id::text,
		current_revision_id::text,status,error_code,retryable,version,created_at,updated_at
		FROM learning.document_knowledge_profile WHERE workspace_id=$1 AND source_version_id=$2 FOR UPDATE`,
		string(capture.WorkspaceID), string(capture.LatestSourceVersionID)))
	if err != nil {
		return domain.Profile{}, classify(err, "CAPTURE_PROFILE_QUERY_FAILED")
	}
	if profile.CaptureID != capture.ID || (profile.ID != request.ProfileID && profile.Status == domain.ProfileStatusReady) {
		return domain.Profile{}, foundation.NewError(foundation.ErrorVersionConflict, "CAPTURE_PROFILE_REPLAY_CONFLICT", false, errors.New("capture profile is already bound to another result"))
	}
	if profile.Status != request.ProfileStatus || profile.ErrorCode != request.Code || profile.Retryable != request.Retryable {
		profile, err = scanProfile(tx.QueryRow(ctx, `UPDATE learning.document_knowledge_profile SET
			status=$3,error_code=$4,retryable=$5,version=version+1,updated_at=$6
			WHERE id=$1 AND workspace_id=$2 AND version=$7 AND status<>'READY'
			RETURNING id::text,workspace_id::text,capture_id::text,source_version_id::text,
				current_revision_id::text,status,error_code,retryable,version,created_at,updated_at`,
			string(profile.ID), string(profile.WorkspaceID), string(request.ProfileStatus), request.Code,
			request.Retryable, request.CompletedAt.UTC(), profile.Version))
		if err != nil {
			return domain.Profile{}, profileCASFailure(err, "CAPTURE_PROFILE_UPDATE_FAILED",
				"profile changed before degraded completion")
		}
	}
	return profile, nil
}

func scanAttempt(row rowScanner) (domain.ProcessingAttempt, error) {
	var attempt domain.ProcessingAttempt
	var id, workspaceID, captureID, workflowRunID, stage, status string
	var sourceVersionID, ingestionAttemptID, indexVersionID, profileID sql.NullString
	if err := row.Scan(&id, &workspaceID, &captureID, &sourceVersionID, &workflowRunID,
		&ingestionAttemptID, &indexVersionID, &profileID, &attempt.AttemptNumber, &stage,
		&status, &attempt.ErrorCode, &attempt.Retryable, &attempt.StartedAt, &attempt.CompletedAt, &attempt.Version); err != nil {
		return domain.ProcessingAttempt{}, err
	}
	var err error
	for value, target := range map[string]*foundation.ID{
		id: &attempt.ID, workspaceID: &attempt.WorkspaceID, captureID: &attempt.CaptureID, workflowRunID: &attempt.WorkflowRunID,
	} {
		*target, err = foundation.ParseID(value)
		if err != nil {
			return domain.ProcessingAttempt{}, err
		}
	}
	for value, target := range map[sql.NullString]*foundation.ID{
		sourceVersionID: &attempt.SourceVersionID, ingestionAttemptID: &attempt.IngestionAttemptID,
		indexVersionID: &attempt.IndexVersionID, profileID: &attempt.ProfileID,
	} {
		if value.Valid {
			*target, err = foundation.ParseID(value.String)
			if err != nil {
				return domain.ProcessingAttempt{}, err
			}
		}
	}
	attempt.Stage = domain.AttemptStage(stage)
	attempt.Status = domain.AttemptStatus(status)
	if err := attempt.Validate(); err != nil {
		return domain.ProcessingAttempt{}, err
	}
	return attempt, nil
}

func scanProfile(row rowScanner) (domain.Profile, error) {
	var profile domain.Profile
	var id, workspaceID, captureID, sourceVersionID, status string
	var revisionID sql.NullString
	if err := row.Scan(&id, &workspaceID, &captureID, &sourceVersionID, &revisionID, &status,
		&profile.ErrorCode, &profile.Retryable, &profile.Version, &profile.CreatedAt, &profile.UpdatedAt); err != nil {
		return domain.Profile{}, err
	}
	var err error
	for value, target := range map[string]*foundation.ID{
		id: &profile.ID, workspaceID: &profile.WorkspaceID, captureID: &profile.CaptureID, sourceVersionID: &profile.SourceVersionID,
	} {
		*target, err = foundation.ParseID(value)
		if err != nil {
			return domain.Profile{}, err
		}
	}
	if revisionID.Valid {
		profile.CurrentRevisionID, err = foundation.ParseID(revisionID.String)
		if err != nil {
			return domain.Profile{}, err
		}
	}
	profile.Status = domain.ProfileStatus(status)
	return profile, nil
}

func validateLease(lease captureapp.OutboxLease) error {
	if !validID(lease.ID) || !validID(lease.WorkspaceID) || !validID(lease.CaptureID) ||
		strings.TrimSpace(lease.Owner) == "" || lease.AttemptCount < 1 || lease.Version < 2 || lease.LeaseUntil.IsZero() {
		return errors.New("capture outbox lease is invalid")
	}
	return nil
}

func validErrorCode(code string) bool {
	return code == strings.TrimSpace(code) && code != "" && len(code) <= 128 && !strings.ContainsAny(code, "\r\n\x00")
}

var _ captureapp.OutboxStore = (*Repository)(nil)
var _ captureapp.ProcessingRepository = (*Repository)(nil)
