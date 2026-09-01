package postgres

import (
	"context"
	"encoding/json"
	"strings"

	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"gorm.io/gorm"
)

// ReplayCreate returns the exact durable result bound to an idempotency key.
func (repository *GORMRepository) ReplayCreate(ctx context.Context, binding captureapp.CommandBinding) (captureapp.CreateResult, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return captureapp.CreateResult{}, false, err
	}
	return replayCreateGORM(ctx, repository.database.WithContext(ctx), binding)
}

// Create atomically persists Workspace content facts, Capture, outbox event,
// and command receipt through the shared platform UnitOfWork.
func (repository *GORMRepository) Create(ctx context.Context, record captureapp.CreateRecord) (captureapp.CreateResult, error) {
	if err := repository.ready(ctx); err != nil {
		return captureapp.CreateResult{}, err
	}
	if err := validateCreateRecord(record); err != nil {
		return captureapp.CreateResult{}, err
	}

	callbackSucceeded := false
	replayEligible := false
	operationCode := "CAPTURE_TRANSACTION_FAILED"
	var result captureapp.CreateResult
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, transaction *gorm.DB, scope foundation.TransactionScope) error {
		var (
			persistedSource    workspacedomain.Source
			persistedVersionID foundation.ID
			writeErr           error
		)
		if record.Registration == nil {
			persistedSource, writeErr = repository.workspaceWriter.RegisterSourceScoped(callbackCtx, scope, record.Source)
		} else {
			registered, registrationErr := repository.workspaceWriter.RegisterSourceVersionScoped(callbackCtx, scope, *record.Registration)
			writeErr = registrationErr
			persistedSource = registered.Source
			persistedVersionID = registered.Version.ID
		}
		if writeErr != nil {
			operationCode = "CAPTURE_SOURCE_REGISTER_FAILED"
			return writeErr
		}
		if persistedSource.ID != record.Capture.SourceID || persistedSource.WorkspaceID != record.Capture.WorkspaceID ||
			(record.Registration != nil && persistedVersionID != record.Capture.LatestSourceVersionID) {
			return inconsistent("CAPTURE_SOURCE_BINDING_INVALID", "workspace registration returned a different capture binding")
		}

		operationCode = "CAPTURE_INSERT_FAILED"
		persisted, insertErr := insertCaptureGORM(callbackCtx, transaction, record.Capture)
		if insertErr != nil {
			replayEligible = isUniqueViolation(insertErr)
			return insertErr
		}

		operationCode = "CAPTURE_OUTBOX_INSERT_FAILED"
		if outboxErr := insertOutboxGORM(callbackCtx, transaction, record, persisted); outboxErr != nil {
			return outboxErr
		}

		result = captureapp.CreateResult{Capture: persisted}
		encoded, encodeErr := encodeReceipt(record.Binding, result)
		if encodeErr != nil {
			return encodeErr
		}
		operationCode = "CAPTURE_RECEIPT_INSERT_FAILED"
		receiptErr := transaction.Exec(`INSERT INTO core.capture_command(
			workspace_id,idempotency_key,request_hash,command_type,capture_id,capture_version,response,created_at
		) VALUES(?,?,?,?,?,?,?::jsonb,?)`,
			string(record.Binding.WorkspaceID), record.Binding.IdempotencyKey, record.Binding.RequestHash,
			record.Binding.CommandType, string(persisted.ID), persisted.Version, captureJSONB(encoded), record.CreatedAt.UTC(),
		).Error
		if receiptErr != nil {
			replayEligible = isUniqueViolation(receiptErr)
			return receiptErr
		}

		callbackSucceeded = true
		return nil
	})
	if err == nil {
		return result, nil
	}
	if callbackSucceeded {
		return captureapp.CreateResult{}, classifyGORMCapture(ctx, err, "CAPTURE_COMMIT_FAILED")
	}
	if replayEligible && isUniqueViolation(err) {
		replayed, found, replayErr := repository.ReplayCreate(ctx, record.Binding)
		if replayErr != nil {
			return captureapp.CreateResult{}, replayErr
		}
		if found {
			return replayed, nil
		}
		return captureapp.CreateResult{}, foundation.NewError(
			foundation.ErrorVersionConflict,
			"CAPTURE_IDEMPOTENCY_CONFLICT",
			false,
			err,
		)
	}
	return captureapp.CreateResult{}, classifyGORMCapture(ctx, err, operationCode)
}

// Get returns one Capture only when it belongs to the requested Workspace.
func (repository *GORMRepository) Get(ctx context.Context, workspaceID, captureID foundation.ID) (domain.Capture, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Capture{}, err
	}
	if !validID(workspaceID) || !validID(captureID) {
		return domain.Capture{}, invalid("CAPTURE_QUERY_INVALID", "capture query identity is invalid")
	}
	row, err := gormCaptureRawRow(repository.database.WithContext(ctx), captureSelect+` WHERE capture.workspace_id=? AND capture.id=?`, string(workspaceID), string(captureID))
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

// List returns a stable Workspace-scoped Capture page.
func (repository *GORMRepository) List(ctx context.Context, query captureapp.ListQuery) (captureapp.Page, error) {
	if err := repository.ready(ctx); err != nil {
		return captureapp.Page{}, err
	}
	if !validID(query.WorkspaceID) || query.Limit < 1 || query.Limit > 100 ||
		(query.Kind != "" && !query.Kind.Valid()) || (query.Status != "" && !query.Status.Valid()) ||
		(query.After != nil && (!validID(query.After.ID) || query.After.CapturedAt.IsZero())) {
		return captureapp.Page{}, invalid("CAPTURE_LIST_INVALID", "capture list query is invalid")
	}

	arguments := []any{string(query.WorkspaceID)}
	conditions := []string{"capture.workspace_id=?"}
	if query.Kind != "" {
		conditions = append(conditions, "capture.kind=?")
		arguments = append(arguments, string(query.Kind))
	}
	if query.Status != "" {
		conditions = append(conditions, "capture.status=?")
		arguments = append(arguments, string(query.Status))
	}
	if query.After != nil {
		conditions = append(conditions, "(capture.captured_at,capture.id)<(?,?)")
		arguments = append(arguments, query.After.CapturedAt.UTC(), string(query.After.ID))
	}
	arguments = append(arguments, query.Limit+1)
	rows, err := gormCaptureRows(repository.database.WithContext(ctx), captureSelect+` WHERE `+strings.Join(conditions, " AND ")+` ORDER BY capture.captured_at DESC,capture.id DESC LIMIT ?`, arguments...)
	if err != nil {
		return captureapp.Page{}, classifyGORMCapture(ctx, err, "CAPTURE_LIST_FAILED")
	}
	defer rows.Close()

	items := make([]domain.Capture, 0, query.Limit+1)
	for rows.Next() {
		capture, scanErr := scanCapture(rows)
		if scanErr != nil {
			return captureapp.Page{}, classifyGORMCapture(ctx, scanErr, "CAPTURE_LIST_SCAN_FAILED")
		}
		items = append(items, capture)
	}
	if err := rows.Err(); err != nil {
		return captureapp.Page{}, classifyGORMCapture(ctx, err, "CAPTURE_LIST_FAILED")
	}

	page := captureapp.Page{Items: items}
	if len(items) > query.Limit {
		page.Items = items[:query.Limit]
		last := page.Items[len(page.Items)-1]
		page.Next = &captureapp.Cursor{CapturedAt: last.CapturedAt, ID: last.ID}
	}
	return page, nil
}

func replayCreateGORM(ctx context.Context, database *gorm.DB, binding captureapp.CommandBinding) (captureapp.CreateResult, bool, error) {
	row, err := gormCaptureRawRow(database, `SELECT request_hash,command_type,response::text
		FROM core.capture_command WHERE workspace_id=? AND idempotency_key=?`, string(binding.WorkspaceID), binding.IdempotencyKey)
	if err != nil {
		return captureapp.CreateResult{}, false, classifyGORMCapture(ctx, err, "CAPTURE_RECEIPT_QUERY_FAILED")
	}
	var requestHash, commandType string
	var raw captureJSONB
	if err := row.Scan(&requestHash, &commandType, &raw); gormCaptureNoRows(err) {
		return captureapp.CreateResult{}, false, nil
	} else if err != nil {
		return captureapp.CreateResult{}, false, classifyGORMCapture(ctx, err, "CAPTURE_RECEIPT_QUERY_FAILED")
	}
	if requestHash != binding.RequestHash || commandType != binding.CommandType {
		return captureapp.CreateResult{}, false, idempotencyConflict()
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

func insertCaptureGORM(ctx context.Context, transaction *gorm.DB, capture domain.Capture) (domain.Capture, error) {
	row, err := gormCaptureRawRow(transaction.WithContext(ctx), `INSERT INTO core.capture(
		id,workspace_id,kind,display_name,original_location,original_url,original_input_hash,
		source_id,latest_source_version_id,status,fetch_status,ingestion_status,index_status,
		profile_status,failure_stage,error_code,retryable,version,captured_at,updated_at
	) VALUES(?,?,?,?,?,NULLIF(?,''),NULLIF(?,''),?,NULLIF(?,'')::uuid,?,?,?,?,?,?,?,?,?,?,?)
	RETURNING `+captureReturning,
		string(capture.ID), string(capture.WorkspaceID), string(capture.Kind), capture.DisplayName,
		capture.OriginalLocation, capture.OriginalURL, capture.OriginalInputHash, string(capture.SourceID),
		string(capture.LatestSourceVersionID), string(capture.Status), string(capture.FetchStatus),
		string(capture.IngestionStatus), string(capture.IndexStatus), string(capture.ProfileStatus),
		capture.FailureStage, capture.ErrorCode, capture.Retryable, capture.Version,
		capture.CapturedAt.UTC(), capture.UpdatedAt.UTC(),
	)
	if err != nil {
		return domain.Capture{}, err
	}
	return scanCapture(row)
}

func insertOutboxGORM(ctx context.Context, transaction *gorm.DB, record captureapp.CreateRecord, capture domain.Capture) error {
	payload, err := json.Marshal(map[string]string{
		"capture_id": string(capture.ID), "workspace_id": string(capture.WorkspaceID),
	})
	if err != nil {
		return inconsistent("CAPTURE_OUTBOX_ENCODING_FAILED", "capture outbox payload could not be encoded")
	}
	return transaction.WithContext(ctx).Exec(`INSERT INTO ops.capture_outbox(
		id,workspace_id,capture_id,schema_version,event_type,event_key,payload,available_at,
		attempt_count,manual_recovery_required,version,created_at,updated_at
	) VALUES(?,?,?,'capture-process-event/v1','capture.process_requested',?,?::jsonb,?,0,false,1,?,?)`,
		string(record.OutboxID), string(capture.WorkspaceID), string(capture.ID), record.EventKey,
		captureJSONB(payload), record.CreatedAt.UTC(), record.CreatedAt.UTC(), record.CreatedAt.UTC(),
	).Error
}
