package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const retryReceiptSchemaVersion = "capture-retry-receipt/v1"

type retryReceipt struct {
	SchemaVersion  string           `json:"schema_version"`
	WorkspaceID    foundation.ID    `json:"workspace_id"`
	IdempotencyKey string           `json:"idempotency_key"`
	RequestHash    string           `json:"request_hash"`
	CommandType    string           `json:"command_type"`
	Capture        persistedCapture `json:"capture"`
}

// ReplayRetry returns the exact durable Capture reset bound to an idempotency key.
func (repository *Repository) ReplayRetry(ctx context.Context, binding captureapp.CommandBinding) (captureapp.RetryResult, bool, error) {
	if repository == nil || repository.db == nil {
		return captureapp.RetryResult{}, false, foundation.NewError(foundation.ErrorDependencyUnavailable, "CAPTURE_REPOSITORY_UNAVAILABLE", true, errors.New("capture database is required"))
	}
	return replayRetry(ctx, repository.db, binding)
}

// ScheduleRetry atomically resets a retryable terminal Capture, appends an outbox event, and freezes its receipt.
func (repository *Repository) ScheduleRetry(ctx context.Context, record captureapp.RetryRecord) (captureapp.RetryResult, error) {
	if err := validateRetryRecord(record); err != nil {
		return captureapp.RetryResult{}, err
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return captureapp.RetryResult{}, classify(err, "CAPTURE_RETRY_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	capture, err := lockCapture(ctx, tx, record.Binding.WorkspaceID, record.CaptureID)
	if err != nil {
		return captureapp.RetryResult{}, err
	}
	if replay, found, replayErr := replayRetry(ctx, tx, record.Binding); replayErr != nil {
		return captureapp.RetryResult{}, replayErr
	} else if found {
		return replay, nil
	}
	if capture.Version != record.ExpectedVersion {
		return captureapp.RetryResult{}, foundation.NewError(foundation.ErrorVersionConflict, "CAPTURE_VERSION_CONFLICT", false, errors.New("capture version changed"))
	}
	queued, err := queuedRetry(capture, record.CreatedAt)
	if err != nil {
		return captureapp.RetryResult{}, err
	}
	queued, err = scanCapture(tx.QueryRow(ctx, `UPDATE core.capture SET
		status=$3,fetch_status=$4,ingestion_status='PENDING',index_status='PENDING',profile_status='PENDING',
		failure_stage='',error_code='',retryable=false,version=version+1,updated_at=$5
		WHERE workspace_id=$1 AND id=$2 AND version=$6 RETURNING `+captureReturning,
		string(capture.WorkspaceID), string(capture.ID), string(queued.Status), string(queued.FetchStatus),
		record.CreatedAt.UTC(), capture.Version))
	if err != nil {
		return captureapp.RetryResult{}, classify(err, "CAPTURE_RETRY_UPDATE_FAILED")
	}
	if err := insertRetryOutbox(ctx, tx, record, queued); err != nil {
		return captureapp.RetryResult{}, err
	}
	result := captureapp.RetryResult{Capture: queued}
	encoded, err := encodeRetryReceipt(record.Binding, result)
	if err != nil {
		return captureapp.RetryResult{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.capture_command(
		workspace_id,idempotency_key,request_hash,command_type,capture_id,capture_version,response,created_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`,
		string(record.Binding.WorkspaceID), record.Binding.IdempotencyKey, record.Binding.RequestHash,
		record.Binding.CommandType, string(queued.ID), queued.Version, encoded, record.CreatedAt.UTC()); err != nil {
		return captureapp.RetryResult{}, classify(err, "CAPTURE_RETRY_RECEIPT_INSERT_FAILED")
	}
	if err := tx.Commit(ctx); err != nil {
		return captureapp.RetryResult{}, classify(err, "CAPTURE_RETRY_COMMIT_FAILED")
	}
	return result, nil
}

func queuedRetry(capture domain.Capture, at time.Time) (domain.Capture, error) {
	if at.IsZero() {
		return domain.Capture{}, invalid("CAPTURE_RETRY_RECORD_INVALID", "capture retry time is invalid")
	}
	if !capture.Retryable {
		return domain.Capture{}, foundation.NewError(foundation.ErrorVersionConflict, "CAPTURE_RETRY_NOT_ALLOWED", false, errors.New("capture failure is not retryable"))
	}
	switch capture.Status {
	case domain.StatusFetchFailed:
		if capture.Kind != domain.KindURL || capture.LatestSourceVersionID != "" {
			return domain.Capture{}, inconsistent("CAPTURE_RETRY_STATE_INVALID", "failed URL capture has an invalid source binding")
		}
		capture.Status = domain.StatusReceived
		capture.FetchStatus = domain.StagePending
	case domain.StatusProcessingFailed, domain.StatusReadyDegraded:
		if !validID(capture.LatestSourceVersionID) {
			return domain.Capture{}, inconsistent("CAPTURE_RETRY_STATE_INVALID", "processing retry requires a source version")
		}
		capture.Status = domain.StatusSourceSaved
	default:
		return domain.Capture{}, foundation.NewError(foundation.ErrorVersionConflict, "CAPTURE_RETRY_NOT_ALLOWED", false, errors.New("capture is not in a retryable terminal state"))
	}
	return capture, nil
}

func insertRetryOutbox(ctx context.Context, tx pgx.Tx, record captureapp.RetryRecord, capture domain.Capture) error {
	payload, err := json.Marshal(map[string]string{
		"capture_id": string(capture.ID), "workspace_id": string(capture.WorkspaceID),
	})
	if err != nil {
		return inconsistent("CAPTURE_OUTBOX_ENCODING_FAILED", "capture retry outbox payload could not be encoded")
	}
	_, err = tx.Exec(ctx, `INSERT INTO ops.capture_outbox(
		id,workspace_id,capture_id,schema_version,event_type,event_key,payload,available_at,
		attempt_count,manual_recovery_required,version,created_at,updated_at
	) VALUES($1,$2,$3,'capture-process-event/v1','capture.process_requested',$4,$5,$6,0,false,1,$6,$6)`,
		string(record.OutboxID), string(capture.WorkspaceID), string(capture.ID), record.EventKey,
		payload, record.CreatedAt.UTC())
	if err != nil {
		return classify(err, "CAPTURE_RETRY_OUTBOX_INSERT_FAILED")
	}
	return nil
}

func replayRetry(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, binding captureapp.CommandBinding) (captureapp.RetryResult, bool, error) {
	if !validID(binding.WorkspaceID) || binding.IdempotencyKey == "" || !validHash(binding.RequestHash) || binding.CommandType != "RETRY_CAPTURE" {
		return captureapp.RetryResult{}, false, invalid("CAPTURE_RETRY_BINDING_INVALID", "capture retry binding is invalid")
	}
	raw, found, err := loadCommandReceipt(ctx, db, binding)
	if err != nil || !found {
		return captureapp.RetryResult{}, found, err
	}
	receipt, err := decodeRetryReceipt(raw)
	if err != nil {
		return captureapp.RetryResult{}, false, err
	}
	if receipt.WorkspaceID != binding.WorkspaceID || receipt.IdempotencyKey != binding.IdempotencyKey ||
		receipt.RequestHash != binding.RequestHash || receipt.CommandType != binding.CommandType {
		return captureapp.RetryResult{}, false, foundation.NewError(foundation.ErrorVersionConflict, "CAPTURE_IDEMPOTENCY_CONFLICT", false, errors.New("capture retry key is bound to a different request"))
	}
	capture := restoreCapture(receipt.Capture)
	if err := capture.Validate(); err != nil || capture.WorkspaceID != binding.WorkspaceID {
		return captureapp.RetryResult{}, false, inconsistent("CAPTURE_RETRY_RECEIPT_INVALID", "capture retry receipt has an invalid result binding")
	}
	return captureapp.RetryResult{Capture: capture, Replayed: true}, true, nil
}

func encodeRetryReceipt(binding captureapp.CommandBinding, result captureapp.RetryResult) ([]byte, error) {
	encoded, err := json.Marshal(retryReceipt{
		SchemaVersion: retryReceiptSchemaVersion, WorkspaceID: binding.WorkspaceID,
		IdempotencyKey: binding.IdempotencyKey, RequestHash: binding.RequestHash,
		CommandType: binding.CommandType, Capture: persistCapture(result.Capture),
	})
	if err != nil {
		return nil, inconsistent("CAPTURE_RETRY_RECEIPT_ENCODING_FAILED", "capture retry receipt could not be encoded")
	}
	return encoded, nil
}

func decodeRetryReceipt(raw []byte) (retryReceipt, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var receipt retryReceipt
	if err := decoder.Decode(&receipt); err != nil {
		return retryReceipt{}, inconsistent("CAPTURE_RETRY_RECEIPT_INVALID", "capture retry receipt is not valid JSON")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return retryReceipt{}, inconsistent("CAPTURE_RETRY_RECEIPT_INVALID", "capture retry receipt contains trailing JSON")
	}
	if receipt.SchemaVersion != retryReceiptSchemaVersion || !validID(receipt.WorkspaceID) ||
		strings.TrimSpace(receipt.IdempotencyKey) == "" || !validHash(receipt.RequestHash) || receipt.CommandType != "RETRY_CAPTURE" {
		return retryReceipt{}, inconsistent("CAPTURE_RETRY_RECEIPT_INVALID", "capture retry receipt identity is invalid")
	}
	return receipt, nil
}

func validateRetryRecord(record captureapp.RetryRecord) error {
	if !validID(record.Binding.WorkspaceID) || record.Binding.IdempotencyKey == "" ||
		!validHash(record.Binding.RequestHash) || record.Binding.CommandType != "RETRY_CAPTURE" ||
		!validID(record.CaptureID) || record.ExpectedVersion < 1 || !validID(record.OutboxID) ||
		strings.TrimSpace(record.EventKey) == "" || len(record.EventKey) > 256 || record.CreatedAt.IsZero() {
		return invalid("CAPTURE_RETRY_RECORD_INVALID", "capture retry record is invalid")
	}
	return nil
}

var _ captureapp.RetryScheduler = (*Repository)(nil)
