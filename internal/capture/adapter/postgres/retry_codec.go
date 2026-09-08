package postgres

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

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
