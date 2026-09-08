package postgres

import (
	"database/sql"
	"errors"
	"strings"

	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const captureReturning = `id::text,workspace_id::text,kind,display_name,original_location,
	original_url,original_input_hash,source_id::text,latest_source_version_id::text,status,
	fetch_status,ingestion_status,index_status,profile_status,failure_stage,error_code,retryable,
	version,captured_at,updated_at`

const attemptReturning = `id::text,workspace_id::text,capture_id::text,source_version_id::text,
	workflow_run_id::text,ingestion_attempt_id::text,index_version_id::text,profile_id::text,
	attempt_number,stage,status,error_code,retryable,started_at,completed_at,version`

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
