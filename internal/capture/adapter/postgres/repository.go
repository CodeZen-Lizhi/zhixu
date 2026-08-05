// Package postgres persists Capture orchestration facts in PostgreSQL.
package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const receiptSchemaVersion = "capture-create-receipt/v1"

// Repository owns Capture state while delegating Source writes to the Workspace transaction writer.
type Repository struct {
	db              *pgxpool.Pool
	workspaceWriter workspacepostgres.TransactionWriter
}

// NewRepository creates a PostgreSQL Capture repository.
func NewRepository(db *pgxpool.Pool) (*Repository, error) {
	if db == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "CAPTURE_REPOSITORY_UNAVAILABLE", true, errors.New("capture database is required"))
	}
	return &Repository{db: db, workspaceWriter: workspacepostgres.TransactionWriter{}}, nil
}

// ReplayCreate returns the exact durable result bound to an idempotency key.
func (repository *Repository) ReplayCreate(ctx context.Context, binding captureapp.CommandBinding) (captureapp.CreateResult, bool, error) {
	return replayCreate(ctx, repository.db, binding)
}

// Create atomically persists Workspace content facts, Capture, outbox event, and command receipt.
func (repository *Repository) Create(ctx context.Context, record captureapp.CreateRecord) (captureapp.CreateResult, error) {
	if err := validateCreateRecord(record); err != nil {
		return captureapp.CreateResult{}, err
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return captureapp.CreateResult{}, classify(err, "CAPTURE_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var persistedSource workspacedomain.Source
	var persistedVersionID foundation.ID
	if record.Registration == nil {
		persistedSource, err = repository.workspaceWriter.RegisterSource(ctx, tx, record.Source)
	} else {
		registered, registerErr := repository.workspaceWriter.RegisterSourceVersion(ctx, tx, *record.Registration)
		err = registerErr
		persistedSource = registered.Source
		persistedVersionID = registered.Version.ID
	}
	if err != nil {
		return captureapp.CreateResult{}, err
	}
	if persistedSource.ID != record.Capture.SourceID || persistedSource.WorkspaceID != record.Capture.WorkspaceID ||
		(record.Registration != nil && persistedVersionID != record.Capture.LatestSourceVersionID) {
		return captureapp.CreateResult{}, inconsistent("CAPTURE_SOURCE_BINDING_INVALID", "workspace registration returned a different capture binding")
	}

	persisted, err := insertCapture(ctx, tx, record.Capture)
	if err != nil {
		return repository.replayAfterConflict(ctx, record.Binding, err, "CAPTURE_INSERT_FAILED")
	}
	if err := insertOutbox(ctx, tx, record, persisted); err != nil {
		return captureapp.CreateResult{}, classify(err, "CAPTURE_OUTBOX_INSERT_FAILED")
	}
	result := captureapp.CreateResult{Capture: persisted}
	encoded, err := encodeReceipt(record.Binding, result)
	if err != nil {
		return captureapp.CreateResult{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO core.capture_command(
			workspace_id,idempotency_key,request_hash,command_type,capture_id,capture_version,response,created_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`,
		string(record.Binding.WorkspaceID), record.Binding.IdempotencyKey, record.Binding.RequestHash,
		record.Binding.CommandType, string(persisted.ID), persisted.Version, encoded, record.CreatedAt.UTC(),
	); err != nil {
		return repository.replayAfterConflict(ctx, record.Binding, err, "CAPTURE_RECEIPT_INSERT_FAILED")
	}
	if err := tx.Commit(ctx); err != nil {
		return captureapp.CreateResult{}, classify(err, "CAPTURE_COMMIT_FAILED")
	}
	return result, nil
}

func (repository *Repository) replayAfterConflict(ctx context.Context, binding captureapp.CommandBinding, cause error, code string) (captureapp.CreateResult, error) {
	if !isUniqueViolation(cause) {
		return captureapp.CreateResult{}, classify(cause, code)
	}
	result, found, err := replayCreate(ctx, repository.db, binding)
	if err != nil {
		return captureapp.CreateResult{}, err
	}
	if found {
		return result, nil
	}
	return captureapp.CreateResult{}, foundation.NewError(foundation.ErrorVersionConflict, "CAPTURE_IDEMPOTENCY_CONFLICT", false, cause)
}

// Get returns one Capture only when it belongs to the requested Workspace.
func (repository *Repository) Get(ctx context.Context, workspaceID, captureID foundation.ID) (domain.Capture, error) {
	if !validID(workspaceID) || !validID(captureID) {
		return domain.Capture{}, invalid("CAPTURE_QUERY_INVALID", "capture query identity is invalid")
	}
	capture, err := scanCapture(repository.db.QueryRow(ctx, captureSelect+` WHERE capture.workspace_id=$1 AND capture.id=$2`, string(workspaceID), string(captureID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Capture{}, foundation.NewError(foundation.ErrorNotFound, "CAPTURE_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.Capture{}, classify(err, "CAPTURE_QUERY_FAILED")
	}
	return capture, nil
}

// List returns a stable Workspace-scoped Capture page.
func (repository *Repository) List(ctx context.Context, query captureapp.ListQuery) (captureapp.Page, error) {
	if !validID(query.WorkspaceID) || query.Limit < 1 || query.Limit > 100 ||
		(query.Kind != "" && !query.Kind.Valid()) || (query.Status != "" && !query.Status.Valid()) ||
		(query.After != nil && (!validID(query.After.ID) || query.After.CapturedAt.IsZero())) {
		return captureapp.Page{}, invalid("CAPTURE_LIST_INVALID", "capture list query is invalid")
	}
	arguments := []any{string(query.WorkspaceID)}
	conditions := []string{"capture.workspace_id=$1"}
	if query.Kind != "" {
		arguments = append(arguments, string(query.Kind))
		conditions = append(conditions, fmt.Sprintf("capture.kind=$%d", len(arguments)))
	}
	if query.Status != "" {
		arguments = append(arguments, string(query.Status))
		conditions = append(conditions, fmt.Sprintf("capture.status=$%d", len(arguments)))
	}
	if query.After != nil {
		arguments = append(arguments, query.After.CapturedAt.UTC(), string(query.After.ID))
		conditions = append(conditions, fmt.Sprintf("(capture.captured_at,capture.id)<($%d,$%d)", len(arguments)-1, len(arguments)))
	}
	arguments = append(arguments, query.Limit+1)
	rows, err := repository.db.Query(ctx, captureSelect+` WHERE `+strings.Join(conditions, " AND ")+fmt.Sprintf(` ORDER BY capture.captured_at DESC,capture.id DESC LIMIT $%d`, len(arguments)), arguments...)
	if err != nil {
		return captureapp.Page{}, classify(err, "CAPTURE_LIST_FAILED")
	}
	defer rows.Close()
	items := make([]domain.Capture, 0, query.Limit+1)
	for rows.Next() {
		capture, scanErr := scanCapture(rows)
		if scanErr != nil {
			return captureapp.Page{}, classify(scanErr, "CAPTURE_LIST_SCAN_FAILED")
		}
		items = append(items, capture)
	}
	if err := rows.Err(); err != nil {
		return captureapp.Page{}, classify(err, "CAPTURE_LIST_FAILED")
	}
	page := captureapp.Page{Items: items}
	if len(items) > query.Limit {
		page.Items = items[:query.Limit]
		last := page.Items[len(page.Items)-1]
		page.Next = &captureapp.Cursor{CapturedAt: last.CapturedAt, ID: last.ID}
	}
	return page, nil
}

const captureSelect = `SELECT
	capture.id::text,capture.workspace_id::text,capture.kind,capture.display_name,
	capture.original_location,capture.original_url,capture.original_input_hash,
	capture.source_id::text,capture.latest_source_version_id::text,capture.status,
	capture.fetch_status,capture.ingestion_status,capture.index_status,capture.profile_status,
	capture.failure_stage,capture.error_code,capture.retryable,capture.version,
	capture.captured_at,capture.updated_at
FROM core.capture AS capture`

func insertCapture(ctx context.Context, tx pgx.Tx, capture domain.Capture) (domain.Capture, error) {
	return scanCapture(tx.QueryRow(ctx, `
		INSERT INTO core.capture(
			id,workspace_id,kind,display_name,original_location,original_url,original_input_hash,
			source_id,latest_source_version_id,status,fetch_status,ingestion_status,index_status,
			profile_status,failure_stage,error_code,retryable,version,captured_at,updated_at
		) VALUES($1,$2,$3,$4,$5,NULLIF($6,''),NULLIF($7,''),$8,NULLIF($9,'')::uuid,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
		RETURNING id::text,workspace_id::text,kind,display_name,original_location,original_url,
			original_input_hash,source_id::text,latest_source_version_id::text,status,fetch_status,
			ingestion_status,index_status,profile_status,failure_stage,error_code,retryable,version,captured_at,updated_at`,
		string(capture.ID), string(capture.WorkspaceID), string(capture.Kind), capture.DisplayName,
		capture.OriginalLocation, capture.OriginalURL, capture.OriginalInputHash, string(capture.SourceID),
		string(capture.LatestSourceVersionID), string(capture.Status), string(capture.FetchStatus),
		string(capture.IngestionStatus), string(capture.IndexStatus), string(capture.ProfileStatus),
		capture.FailureStage, capture.ErrorCode, capture.Retryable, capture.Version,
		capture.CapturedAt.UTC(), capture.UpdatedAt.UTC(),
	))
}

func insertOutbox(ctx context.Context, tx pgx.Tx, record captureapp.CreateRecord, capture domain.Capture) error {
	payload, err := json.Marshal(map[string]string{
		"capture_id": string(capture.ID), "workspace_id": string(capture.WorkspaceID),
	})
	if err != nil {
		return inconsistent("CAPTURE_OUTBOX_ENCODING_FAILED", "capture outbox payload could not be encoded")
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO ops.capture_outbox(
			id,workspace_id,capture_id,schema_version,event_type,event_key,payload,available_at,
			attempt_count,manual_recovery_required,version,created_at,updated_at
		) VALUES($1,$2,$3,'capture-process-event/v1','capture.process_requested',$4,$5,$6,0,false,1,$6,$6)`,
		string(record.OutboxID), string(capture.WorkspaceID), string(capture.ID), record.EventKey,
		payload, record.CreatedAt.UTC(),
	)
	return err
}

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

func replayCreate(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, binding captureapp.CommandBinding) (captureapp.CreateResult, bool, error) {
	raw, found, err := loadCommandReceipt(ctx, db, binding)
	if err != nil || !found {
		return captureapp.CreateResult{}, found, err
	}
	receipt, err := decodeReceipt(raw)
	if err != nil {
		return captureapp.CreateResult{}, false, err
	}
	if receipt.WorkspaceID != binding.WorkspaceID || receipt.IdempotencyKey != binding.IdempotencyKey ||
		receipt.RequestHash != binding.RequestHash || receipt.CommandType != binding.CommandType {
		return captureapp.CreateResult{}, false, idempotencyConflict()
	}
	capture := restoreCapture(receipt.Capture)
	if err := capture.Validate(); err != nil || capture.WorkspaceID != binding.WorkspaceID {
		return captureapp.CreateResult{}, false, inconsistent("CAPTURE_RECEIPT_INVALID", "capture receipt has an invalid result binding")
	}
	return captureapp.CreateResult{Capture: capture, Replayed: true}, true, nil
}

func loadCommandReceipt(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, binding captureapp.CommandBinding) ([]byte, bool, error) {
	var requestHash, commandType string
	var raw []byte
	err := db.QueryRow(ctx, `SELECT request_hash,command_type,response FROM core.capture_command
		WHERE workspace_id=$1 AND idempotency_key=$2`, string(binding.WorkspaceID), binding.IdempotencyKey).
		Scan(&requestHash, &commandType, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, classify(err, "CAPTURE_RECEIPT_QUERY_FAILED")
	}
	if requestHash != binding.RequestHash || commandType != binding.CommandType {
		return nil, false, idempotencyConflict()
	}
	return raw, true, nil
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

func classify(err error, code string) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23505":
			return foundation.NewError(foundation.ErrorVersionConflict, code, false, err)
		case "23503", "23514", "55000":
			return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, err)
		case "40001", "40P01", "55P03":
			return foundation.NewError(foundation.ErrorRetryableFailure, code, true, err)
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, code, false, err)
	}
	return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, err)
}

func isUniqueViolation(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "23505"
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

var _ captureapp.Repository = (*Repository)(nil)
