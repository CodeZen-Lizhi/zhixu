package postgres

import (
	"context"
	"encoding/json"
	"errors"

	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"gorm.io/gorm"
)

// ReplayProfileRetry returns the exact durable view bound to a retry command.
func (repository *GORMProfileRepository) ReplayProfileRetry(
	ctx context.Context,
	binding captureapp.ProfileRetryBinding,
) (captureapp.ProfileRetryResult, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return captureapp.ProfileRetryResult{}, false, err
	}
	if !validProfileRetryBinding(binding) {
		return captureapp.ProfileRetryResult{}, false, invalid("CAPTURE_PROFILE_RETRY_INVALID", "profile retry binding is invalid")
	}
	return gormReplayProfileRetry(ctx, repository.database.WithContext(ctx), binding)
}

// ScheduleProfileRetry preserves immutable revision facts while atomically
// resetting Profile/Capture state and appending one processing request.
func (repository *GORMProfileRepository) ScheduleProfileRetry(
	ctx context.Context,
	record captureapp.ProfileRetryRecord,
) (captureapp.ProfileRetryResult, error) {
	if err := repository.ready(ctx); err != nil {
		return captureapp.ProfileRetryResult{}, err
	}
	if !validProfileRetryRecord(record) {
		return captureapp.ProfileRetryResult{}, invalid("CAPTURE_PROFILE_RETRY_INVALID", "profile retry record is invalid")
	}

	var result captureapp.ProfileRetryResult
	callbackSucceeded := false
	replayEligible := false
	operationCode := "CAPTURE_PROFILE_RETRY_TRANSACTION_FAILED"
	err := repository.within(ctx, func(callbackCtx context.Context, transaction *gorm.DB, _ foundation.TransactionScope) error {
		profile, found, err := gormLoadProfileBySource(
			callbackCtx, transaction, record.Binding.WorkspaceID, record.Binding.SourceVersionID, true,
		)
		if err != nil {
			operationCode = "CAPTURE_PROFILE_RETRY_QUERY_FAILED"
			return err
		}
		if !found {
			return foundation.NewError(foundation.ErrorNotFound, "CAPTURE_PROFILE_NOT_FOUND", false, errors.New("profile not found"))
		}
		if replay, found, replayErr := gormReplayProfileRetry(callbackCtx, transaction, record.Binding); replayErr != nil {
			return replayErr
		} else if found {
			result = replay
			callbackSucceeded = true
			return nil
		}
		if profile.Version != record.Binding.ExpectedVersion {
			return foundation.NewError(foundation.ErrorVersionConflict, "CAPTURE_PROFILE_VERSION_CONFLICT", false,
				errors.New("profile version changed"))
		}
		retryableFailure := profile.Status == domain.ProfileStatusFailed && profile.Retryable
		capabilityUnavailable := profile.Status == domain.ProfileStatusCapabilityUnavailable && !profile.Retryable
		staleRevision := profile.Status == domain.ProfileStatusStale && profile.CurrentRevisionID != ""
		if !retryableFailure && !capabilityUnavailable && !staleRevision {
			return foundation.NewError(foundation.ErrorVersionConflict, "CAPTURE_PROFILE_RETRY_NOT_ALLOWED", false,
				errors.New("profile is not failed, unavailable, or stale"))
		}

		capture, err := gormLockCapture(callbackCtx, transaction, profile.WorkspaceID, profile.CaptureID)
		if err != nil {
			return err
		}
		failedCapture := capture.Status == domain.StatusReadyDegraded && capture.ProfileStatus == domain.StageFailed && capture.Retryable
		unavailableCapture := capture.Status == domain.StatusReadyDegraded && capture.ProfileStatus == domain.StageCapabilityUnavailable && !capture.Retryable
		staleCapture := (capture.Status == domain.StatusReady || capture.Status == domain.StatusReadyDegraded) &&
			capture.ProfileStatus == domain.StageStale && !capture.Retryable
		if capture.LatestSourceVersionID != profile.SourceVersionID ||
			(retryableFailure && !failedCapture) || (capabilityUnavailable && !unavailableCapture) || (staleRevision && !staleCapture) {
			return inconsistent("CAPTURE_PROFILE_RETRY_STATE_INVALID", "capture does not match the rebuildable profile state")
		}
		view, err := gormLoadProfileView(callbackCtx, transaction, profile)
		if err != nil {
			return err
		}

		operationCode = "CAPTURE_PROFILE_VERSION_CONFLICT"
		row, err := gormCaptureRawRow(transaction, `UPDATE learning.document_knowledge_profile SET
			status='PENDING',error_code='',retryable=false,version=version+1,updated_at=GREATEST(updated_at,?)
			WHERE id=? AND workspace_id=? AND version=? RETURNING `+profileReturning,
			record.CreatedAt.UTC(), string(profile.ID), string(profile.WorkspaceID), record.Binding.ExpectedVersion)
		if err != nil {
			return err
		}
		profile, err = scanProfile(row)
		if err != nil {
			return gormProfileCASFailure(callbackCtx, err, "CAPTURE_PROFILE_VERSION_CONFLICT", "profile changed before retry scheduling")
		}

		operationCode = "CAPTURE_PROFILE_RETRY_CAPTURE_UPDATE_FAILED"
		row, err = gormCaptureRawRow(transaction, `UPDATE core.capture SET
			status='SOURCE_SAVED',ingestion_status='READY',index_status='READY',profile_status='PENDING',
			failure_stage='',error_code='',retryable=false,version=version+1,updated_at=GREATEST(updated_at,?)
			WHERE workspace_id=? AND id=? AND version=? RETURNING `+captureReturning,
			record.CreatedAt.UTC(), string(capture.WorkspaceID), string(capture.ID), capture.Version)
		if err != nil {
			return err
		}
		capture, err = scanCapture(row)
		if err != nil {
			return gormProfileCASFailure(callbackCtx, err, "CAPTURE_PROFILE_RETRY_CAPTURE_UPDATE_FAILED", "capture changed before profile retry scheduling")
		}
		operationCode = "CAPTURE_PROFILE_RETRY_OUTBOX_INSERT_FAILED"
		if err := gormInsertProfileRetryOutbox(callbackCtx, transaction, record, capture); err != nil {
			return err
		}

		view.Profile = profile
		result = captureapp.ProfileRetryResult{View: view}
		encoded, err := encodeProfileRetryReceipt(record.Binding, result)
		if err != nil {
			return err
		}
		operationCode = "CAPTURE_PROFILE_RETRY_RECEIPT_INSERT_FAILED"
		if err := transaction.Exec(`INSERT INTO core.capture_command(
			workspace_id,idempotency_key,request_hash,command_type,capture_id,capture_version,response,created_at
		) VALUES(?,?,?,?,?,?,?::jsonb,?)`,
			string(record.Binding.WorkspaceID), record.Binding.IdempotencyKey, record.Binding.RequestHash,
			profileRetryCommandType, string(capture.ID), capture.Version, captureJSONB(encoded), record.CreatedAt.UTC()).Error; err != nil {
			replayEligible = isUniqueViolation(err)
			return err
		}
		callbackSucceeded = true
		return nil
	})
	if err == nil {
		return result, nil
	}
	if replayEligible && isUniqueViolation(err) {
		replay, found, replayErr := repository.ReplayProfileRetry(ctx, record.Binding)
		if replayErr != nil {
			return captureapp.ProfileRetryResult{}, replayErr
		}
		if found {
			return replay, nil
		}
		return captureapp.ProfileRetryResult{}, foundation.NewError(foundation.ErrorVersionConflict, "CAPTURE_PROFILE_RETRY_CONFLICT", true,
			errors.New("profile retry command conflicted without a durable receipt"))
	}
	if callbackSucceeded {
		operationCode = "CAPTURE_PROFILE_RETRY_COMMIT_FAILED"
	}
	return captureapp.ProfileRetryResult{}, classifyGORMCapture(ctx, err, operationCode)
}

func gormReplayProfileRetry(
	ctx context.Context,
	database *gorm.DB,
	binding captureapp.ProfileRetryBinding,
) (captureapp.ProfileRetryResult, bool, error) {
	if !validProfileRetryBinding(binding) {
		return captureapp.ProfileRetryResult{}, false, invalid("CAPTURE_PROFILE_RETRY_INVALID", "profile retry binding is invalid")
	}
	row, err := gormCaptureRawRow(database.WithContext(ctx), `SELECT request_hash,command_type,response
		FROM core.capture_command WHERE workspace_id=? AND idempotency_key=?`,
		string(binding.WorkspaceID), binding.IdempotencyKey)
	if err != nil {
		return captureapp.ProfileRetryResult{}, false, classifyGORMCapture(ctx, err, "CAPTURE_RECEIPT_QUERY_FAILED")
	}
	var requestHash, commandType string
	var raw captureJSONB
	if err := row.Scan(&requestHash, &commandType, &raw); gormCaptureNoRows(err) {
		return captureapp.ProfileRetryResult{}, false, nil
	} else if err != nil {
		return captureapp.ProfileRetryResult{}, false, classifyGORMCapture(ctx, err, "CAPTURE_RECEIPT_QUERY_FAILED")
	}
	if requestHash != binding.RequestHash || commandType != profileRetryCommandType {
		return captureapp.ProfileRetryResult{}, false, idempotencyConflict()
	}
	receipt, err := decodeProfileRetryReceipt(raw)
	if err != nil {
		return captureapp.ProfileRetryResult{}, false, err
	}
	if receipt.WorkspaceID != binding.WorkspaceID || receipt.SourceVersionID != binding.SourceVersionID ||
		receipt.ExpectedVersion != binding.ExpectedVersion || receipt.IdempotencyKey != binding.IdempotencyKey ||
		receipt.RequestHash != binding.RequestHash || receipt.CommandType != profileRetryCommandType {
		return captureapp.ProfileRetryResult{}, false, idempotencyConflict()
	}
	view, err := restoreProfileRetryView(receipt)
	if err != nil || view.Profile.WorkspaceID != binding.WorkspaceID || view.Profile.SourceVersionID != binding.SourceVersionID {
		return captureapp.ProfileRetryResult{}, false, inconsistent("CAPTURE_PROFILE_RETRY_RECEIPT_INVALID", "profile retry receipt has an invalid result binding")
	}
	return captureapp.ProfileRetryResult{View: view, Replayed: true}, true, nil
}

func gormInsertProfileRetryOutbox(
	ctx context.Context,
	transaction *gorm.DB,
	record captureapp.ProfileRetryRecord,
	capture domain.Capture,
) error {
	payload, err := jsonProfileRetryOutbox(capture)
	if err != nil {
		return err
	}
	return transaction.WithContext(ctx).Exec(`INSERT INTO ops.capture_outbox(
		id,workspace_id,capture_id,schema_version,event_type,event_key,payload,available_at,
		attempt_count,manual_recovery_required,version,created_at,updated_at
	) VALUES(?,?,?,'capture-process-event/v1','capture.process_requested',?,?::jsonb,?,0,false,1,?,?)`,
		string(record.OutboxID), string(capture.WorkspaceID), string(capture.ID), record.EventKey,
		captureJSONB(payload), record.CreatedAt.UTC(), record.CreatedAt.UTC(), record.CreatedAt.UTC()).Error
}

func jsonProfileRetryOutbox(capture domain.Capture) ([]byte, error) {
	payload, err := json.Marshal(map[string]string{
		"capture_id": string(capture.ID), "workspace_id": string(capture.WorkspaceID),
	})
	if err != nil {
		return nil, inconsistent("CAPTURE_PROFILE_RETRY_OUTBOX_ENCODING_FAILED", "profile retry outbox payload could not be encoded")
	}
	return payload, nil
}

var _ captureapp.ProfileRetryScheduler = (*GORMProfileRepository)(nil)
