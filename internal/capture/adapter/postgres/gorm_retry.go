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

// ReplayRetry returns the exact durable Capture reset bound to an idempotency key.
func (repository *GORMRepository) ReplayRetry(ctx context.Context, binding captureapp.CommandBinding) (captureapp.RetryResult, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return captureapp.RetryResult{}, false, err
	}
	return gormReplayRetry(ctx, repository.database.WithContext(ctx), binding)
}

// ScheduleRetry atomically resets a retryable terminal Capture, appends an
// outbox event, and freezes its receipt in the same UnitOfWork.
func (repository *GORMRepository) ScheduleRetry(ctx context.Context, record captureapp.RetryRecord) (captureapp.RetryResult, error) {
	if err := repository.ready(ctx); err != nil {
		return captureapp.RetryResult{}, err
	}
	if err := validateRetryRecord(record); err != nil {
		return captureapp.RetryResult{}, err
	}

	var result captureapp.RetryResult
	callbackSucceeded := false
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, transaction *gorm.DB, _ foundation.TransactionScope) error {
		capture, err := gormLockCaptureForRetry(callbackCtx, transaction, record.Binding.WorkspaceID, record.CaptureID)
		if err != nil {
			return err
		}
		if replay, found, replayErr := gormReplayRetry(callbackCtx, transaction, record.Binding); replayErr != nil {
			return replayErr
		} else if found {
			result = replay
			callbackSucceeded = true
			return nil
		}
		if capture.Version != record.ExpectedVersion {
			return foundation.NewError(foundation.ErrorVersionConflict, "CAPTURE_VERSION_CONFLICT", false,
				errors.New("capture version changed"))
		}
		queued, err := queuedRetry(capture, record.CreatedAt)
		if err != nil {
			return err
		}

		row, err := gormCaptureRawRow(transaction, `UPDATE core.capture SET
			status=?,fetch_status=?,ingestion_status='PENDING',index_status='PENDING',profile_status='PENDING',
			failure_stage='',error_code='',retryable=false,version=version+1,updated_at=?
			WHERE workspace_id=? AND id=? AND version=? RETURNING `+captureReturning,
			string(queued.Status), string(queued.FetchStatus), record.CreatedAt.UTC(),
			string(capture.WorkspaceID), string(capture.ID), capture.Version)
		if err != nil {
			return classifyGORMCapture(callbackCtx, err, "CAPTURE_RETRY_UPDATE_FAILED")
		}
		queued, err = scanCapture(row)
		if err != nil {
			return classifyGORMCapture(callbackCtx, err, "CAPTURE_RETRY_UPDATE_FAILED")
		}
		if err := gormInsertRetryOutbox(callbackCtx, transaction, record, queued); err != nil {
			return err
		}
		result = captureapp.RetryResult{Capture: queued}
		encoded, err := encodeRetryReceipt(record.Binding, result)
		if err != nil {
			return err
		}
		if err := transaction.Exec(`INSERT INTO core.capture_command(
			workspace_id,idempotency_key,request_hash,command_type,capture_id,capture_version,response,created_at
		) VALUES(?,?,?,?,?,?,?::jsonb,?)`,
			string(record.Binding.WorkspaceID), record.Binding.IdempotencyKey, record.Binding.RequestHash,
			record.Binding.CommandType, string(queued.ID), queued.Version, captureJSONB(encoded), record.CreatedAt.UTC()).Error; err != nil {
			return classifyGORMCapture(callbackCtx, err, "CAPTURE_RETRY_RECEIPT_INSERT_FAILED")
		}
		callbackSucceeded = true
		return nil
	})
	if err != nil {
		code := "CAPTURE_RETRY_TRANSACTION_FAILED"
		if callbackSucceeded {
			code = "CAPTURE_RETRY_COMMIT_FAILED"
		}
		return captureapp.RetryResult{}, classifyGORMCapture(ctx, err, code)
	}
	return result, nil
}

func gormLockCaptureForRetry(ctx context.Context, database *gorm.DB, workspaceID, captureID foundation.ID) (domain.Capture, error) {
	row, err := gormCaptureRawRow(database.WithContext(ctx), captureSelect+` WHERE capture.workspace_id=? AND capture.id=? FOR UPDATE`,
		string(workspaceID), string(captureID))
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

func gormInsertRetryOutbox(ctx context.Context, transaction *gorm.DB, record captureapp.RetryRecord, capture domain.Capture) error {
	payload, err := json.Marshal(map[string]string{
		"capture_id": string(capture.ID), "workspace_id": string(capture.WorkspaceID),
	})
	if err != nil {
		return inconsistent("CAPTURE_OUTBOX_ENCODING_FAILED", "capture retry outbox payload could not be encoded")
	}
	if err := transaction.WithContext(ctx).Exec(`INSERT INTO ops.capture_outbox(
		id,workspace_id,capture_id,schema_version,event_type,event_key,payload,available_at,
		attempt_count,manual_recovery_required,version,created_at,updated_at
	) VALUES(?,?,?,'capture-process-event/v1','capture.process_requested',?,?::jsonb,?,0,false,1,?,?)`,
		string(record.OutboxID), string(capture.WorkspaceID), string(capture.ID), record.EventKey,
		captureJSONB(payload), record.CreatedAt.UTC(), record.CreatedAt.UTC(), record.CreatedAt.UTC()).Error; err != nil {
		return classifyGORMCapture(ctx, err, "CAPTURE_RETRY_OUTBOX_INSERT_FAILED")
	}
	return nil
}

func gormReplayRetry(ctx context.Context, database *gorm.DB, binding captureapp.CommandBinding) (captureapp.RetryResult, bool, error) {
	if !validID(binding.WorkspaceID) || binding.IdempotencyKey == "" || !validHash(binding.RequestHash) || binding.CommandType != "RETRY_CAPTURE" {
		return captureapp.RetryResult{}, false, invalid("CAPTURE_RETRY_BINDING_INVALID", "capture retry binding is invalid")
	}
	row, err := gormCaptureRawRow(database.WithContext(ctx), `SELECT request_hash,command_type,response
		FROM core.capture_command WHERE workspace_id=? AND idempotency_key=?`,
		string(binding.WorkspaceID), binding.IdempotencyKey)
	if err != nil {
		return captureapp.RetryResult{}, false, classifyGORMCapture(ctx, err, "CAPTURE_RECEIPT_QUERY_FAILED")
	}
	var requestHash, commandType string
	var raw captureJSONB
	if err := row.Scan(&requestHash, &commandType, &raw); gormCaptureNoRows(err) {
		return captureapp.RetryResult{}, false, nil
	} else if err != nil {
		return captureapp.RetryResult{}, false, classifyGORMCapture(ctx, err, "CAPTURE_RECEIPT_QUERY_FAILED")
	}
	if requestHash != binding.RequestHash || commandType != binding.CommandType {
		return captureapp.RetryResult{}, false, idempotencyConflict()
	}
	receipt, err := decodeRetryReceipt(raw)
	if err != nil {
		return captureapp.RetryResult{}, false, err
	}
	if receipt.WorkspaceID != binding.WorkspaceID || receipt.IdempotencyKey != binding.IdempotencyKey ||
		receipt.RequestHash != binding.RequestHash || receipt.CommandType != binding.CommandType {
		return captureapp.RetryResult{}, false, idempotencyConflict()
	}
	capture := restoreCapture(receipt.Capture)
	if err := capture.Validate(); err != nil || capture.WorkspaceID != binding.WorkspaceID {
		return captureapp.RetryResult{}, false, inconsistent("CAPTURE_RETRY_RECEIPT_INVALID", "capture retry receipt has an invalid result binding")
	}
	return captureapp.RetryResult{Capture: capture, Replayed: true}, true, nil
}
