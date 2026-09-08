package postgres

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"time"

	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const receiptSchemaVersion = "capture-create-receipt/v1"

const captureSelect = `SELECT
	capture.id::text,capture.workspace_id::text,capture.kind,capture.display_name,
	capture.original_location,capture.original_url,capture.original_input_hash,
	capture.source_id::text,capture.latest_source_version_id::text,capture.status,
	capture.fetch_status,capture.ingestion_status,capture.index_status,capture.profile_status,
	capture.failure_stage,capture.error_code,capture.retryable,capture.version,
	capture.captured_at,capture.updated_at
FROM core.capture AS capture`

type captureReceipt struct {
	SchemaVersion  string           `json:"schema_version"`
	WorkspaceID    foundation.ID    `json:"workspace_id"`
	IdempotencyKey string           `json:"idempotency_key"`
	RequestHash    string           `json:"request_hash"`
	CommandType    string           `json:"command_type"`
	Capture        persistedCapture `json:"capture"`
}

type persistedCapture struct {
	ID                    foundation.ID      `json:"id"`
	WorkspaceID           foundation.ID      `json:"workspace_id"`
	Kind                  domain.Kind        `json:"kind"`
	DisplayName           string             `json:"display_name"`
	OriginalLocation      string             `json:"original_location"`
	OriginalURL           string             `json:"original_url,omitempty"`
	OriginalInputHash     string             `json:"original_input_hash,omitempty"`
	SourceID              foundation.ID      `json:"source_id"`
	LatestSourceVersionID foundation.ID      `json:"latest_source_version_id,omitempty"`
	Status                domain.Status      `json:"status"`
	FetchStatus           domain.StageStatus `json:"fetch_status"`
	IngestionStatus       domain.StageStatus `json:"ingestion_status"`
	IndexStatus           domain.StageStatus `json:"index_status"`
	ProfileStatus         domain.StageStatus `json:"profile_status"`
	FailureStage          string             `json:"failure_stage,omitempty"`
	ErrorCode             string             `json:"error_code,omitempty"`
	Retryable             bool               `json:"retryable"`
	Version               int64              `json:"version"`
	CapturedAt            time.Time          `json:"captured_at"`
	UpdatedAt             time.Time          `json:"updated_at"`
}

func encodeReceipt(binding captureapp.CommandBinding, result captureapp.CreateResult) ([]byte, error) {
	receipt := captureReceipt{
		SchemaVersion: receiptSchemaVersion, WorkspaceID: binding.WorkspaceID,
		IdempotencyKey: binding.IdempotencyKey, RequestHash: binding.RequestHash,
		CommandType: binding.CommandType, Capture: persistCapture(result.Capture),
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return nil, inconsistent("CAPTURE_RECEIPT_ENCODING_FAILED", "capture receipt could not be encoded")
	}
	return encoded, nil
}

func idempotencyConflict() error {
	return foundation.NewError(foundation.ErrorVersionConflict, "CAPTURE_IDEMPOTENCY_CONFLICT", false,
		errors.New("capture idempotency key is bound to a different request"))
}

func decodeReceipt(raw []byte) (captureReceipt, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var receipt captureReceipt
	if err := decoder.Decode(&receipt); err != nil {
		return captureReceipt{}, inconsistent("CAPTURE_RECEIPT_INVALID", "capture receipt is not valid JSON")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return captureReceipt{}, inconsistent("CAPTURE_RECEIPT_INVALID", "capture receipt contains trailing JSON")
	}
	if receipt.SchemaVersion != receiptSchemaVersion || !validID(receipt.WorkspaceID) ||
		receipt.IdempotencyKey == "" || !validHash(receipt.RequestHash) || receipt.CommandType == "" {
		return captureReceipt{}, inconsistent("CAPTURE_RECEIPT_INVALID", "capture receipt identity is invalid")
	}
	return receipt, nil
}

func persistCapture(capture domain.Capture) persistedCapture {
	return persistedCapture{
		ID: capture.ID, WorkspaceID: capture.WorkspaceID, Kind: capture.Kind, DisplayName: capture.DisplayName,
		OriginalLocation: capture.OriginalLocation, OriginalURL: capture.OriginalURL,
		OriginalInputHash: capture.OriginalInputHash, SourceID: capture.SourceID,
		LatestSourceVersionID: capture.LatestSourceVersionID, Status: capture.Status,
		FetchStatus: capture.FetchStatus, IngestionStatus: capture.IngestionStatus,
		IndexStatus: capture.IndexStatus, ProfileStatus: capture.ProfileStatus,
		FailureStage: capture.FailureStage, ErrorCode: capture.ErrorCode, Retryable: capture.Retryable,
		Version: capture.Version, CapturedAt: capture.CapturedAt.UTC(), UpdatedAt: capture.UpdatedAt.UTC(),
	}
}

func restoreCapture(capture persistedCapture) domain.Capture {
	return domain.Capture{
		ID: capture.ID, WorkspaceID: capture.WorkspaceID, Kind: capture.Kind, DisplayName: capture.DisplayName,
		OriginalLocation: capture.OriginalLocation, OriginalURL: capture.OriginalURL,
		OriginalInputHash: capture.OriginalInputHash, SourceID: capture.SourceID,
		LatestSourceVersionID: capture.LatestSourceVersionID, Status: capture.Status,
		FetchStatus: capture.FetchStatus, IngestionStatus: capture.IngestionStatus,
		IndexStatus: capture.IndexStatus, ProfileStatus: capture.ProfileStatus,
		FailureStage: capture.FailureStage, ErrorCode: capture.ErrorCode, Retryable: capture.Retryable,
		Version: capture.Version, CapturedAt: capture.CapturedAt.UTC(), UpdatedAt: capture.UpdatedAt.UTC(),
	}
}

type rowScanner interface{ Scan(...any) error }

func scanCapture(row rowScanner) (domain.Capture, error) {
	var capture domain.Capture
	var id, workspaceID, kind, sourceID, status string
	var latestVersionID sql.NullString
	var originalURL, inputHash sql.NullString
	var fetchStatus, ingestionStatus, indexStatus, profileStatus string
	if err := row.Scan(
		&id, &workspaceID, &kind, &capture.DisplayName, &capture.OriginalLocation,
		&originalURL, &inputHash, &sourceID, &latestVersionID, &status,
		&fetchStatus, &ingestionStatus, &indexStatus, &profileStatus,
		&capture.FailureStage, &capture.ErrorCode, &capture.Retryable, &capture.Version,
		&capture.CapturedAt, &capture.UpdatedAt,
	); err != nil {
		return domain.Capture{}, err
	}
	var err error
	if capture.ID, err = foundation.ParseID(id); err != nil {
		return domain.Capture{}, err
	}
	if capture.WorkspaceID, err = foundation.ParseID(workspaceID); err != nil {
		return domain.Capture{}, err
	}
	if capture.SourceID, err = foundation.ParseID(sourceID); err != nil {
		return domain.Capture{}, err
	}
	if latestVersionID.Valid {
		if capture.LatestSourceVersionID, err = foundation.ParseID(latestVersionID.String); err != nil {
			return domain.Capture{}, err
		}
	}
	capture.Kind = domain.Kind(kind)
	capture.Status = domain.Status(status)
	capture.FetchStatus = domain.StageStatus(fetchStatus)
	capture.IngestionStatus = domain.StageStatus(ingestionStatus)
	capture.IndexStatus = domain.StageStatus(indexStatus)
	capture.ProfileStatus = domain.StageStatus(profileStatus)
	capture.OriginalURL = originalURL.String
	capture.OriginalInputHash = inputHash.String
	if err := capture.Validate(); err != nil {
		return domain.Capture{}, err
	}
	return capture, nil
}

func validateCreateRecord(record captureapp.CreateRecord) error {
	if err := record.Capture.Validate(); err != nil {
		return err
	}
	if record.Binding.WorkspaceID != record.Capture.WorkspaceID || record.Binding.IdempotencyKey == "" ||
		!validHash(record.Binding.RequestHash) || record.Binding.CommandType == "" || !validID(record.OutboxID) ||
		record.EventKey == "" || record.CreatedAt.IsZero() || record.Source.ID != record.Capture.SourceID ||
		record.Source.WorkspaceID != record.Capture.WorkspaceID || record.Source.OriginalLocation != record.Capture.OriginalLocation {
		return invalid("CAPTURE_CREATE_RECORD_INVALID", "capture create record is invalid")
	}
	if record.Registration != nil && (record.Registration.Source.ID != record.Source.ID ||
		record.Registration.Version.ID != record.Capture.LatestSourceVersionID) {
		return invalid("CAPTURE_CREATE_RECORD_INVALID", "capture registration binding is invalid")
	}
	if record.Registration == nil && record.Capture.LatestSourceVersionID != "" {
		return invalid("CAPTURE_CREATE_RECORD_INVALID", "materialized capture registration is missing")
	}
	return nil
}

func validID(value foundation.ID) bool {
	_, err := foundation.ParseID(string(value))
	return err == nil
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func invalid(code, message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, code, false, errors.New(message))
}

func inconsistent(code, message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, errors.New(message))
}
