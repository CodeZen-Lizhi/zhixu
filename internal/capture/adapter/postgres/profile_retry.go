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

const (
	profileRetryCommandType          = "RETRY_PROFILE"
	profileRetryReceiptSchemaVersion = "capture-profile-retry-receipt/v1"
)

type profileRetryReceipt struct {
	SchemaVersion   string                            `json:"schema_version"`
	WorkspaceID     foundation.ID                     `json:"workspace_id"`
	SourceVersionID foundation.ID                     `json:"source_version_id"`
	ExpectedVersion int64                             `json:"expected_version"`
	IdempotencyKey  string                            `json:"idempotency_key"`
	RequestHash     string                            `json:"request_hash"`
	CommandType     string                            `json:"command_type"`
	Profile         persistedProfileReceipt           `json:"profile"`
	Revision        *persistedProfileRevisionReceipt  `json:"revision,omitempty"`
	Evidence        []persistedProfileEvidenceReceipt `json:"evidence,omitempty"`
}

type persistedProfileReceipt struct {
	ID                foundation.ID        `json:"id"`
	WorkspaceID       foundation.ID        `json:"workspace_id"`
	CaptureID         foundation.ID        `json:"capture_id"`
	SourceVersionID   foundation.ID        `json:"source_version_id"`
	CurrentRevisionID foundation.ID        `json:"current_revision_id,omitempty"`
	Status            domain.ProfileStatus `json:"status"`
	ErrorCode         string               `json:"error_code,omitempty"`
	Retryable         bool                 `json:"retryable"`
	Version           int64                `json:"version"`
	CreatedAt         string               `json:"created_at"`
	UpdatedAt         string               `json:"updated_at"`
}

type persistedProfileRevisionReceipt struct {
	ID                    foundation.ID         `json:"id"`
	ProfileID             foundation.ID         `json:"profile_id"`
	WorkspaceID           foundation.ID         `json:"workspace_id"`
	SourceVersionID       foundation.ID         `json:"source_version_id"`
	ParseProjectionID     foundation.ID         `json:"parse_projection_id"`
	IndexVersionID        foundation.ID         `json:"index_version_id"`
	ModelRunID            foundation.ID         `json:"model_run_id"`
	ModelSettingsRevision *int64                `json:"model_settings_revision"`
	PromptVersion         string                `json:"prompt_version"`
	SchemaVersion         string                `json:"schema_version"`
	Content               domain.ProfileContent `json:"content"`
	ContentDigest         string                `json:"content_digest"`
	CreatedAt             string                `json:"created_at"`
}

type persistedProfileEvidenceReceipt struct {
	RevisionID      foundation.ID `json:"revision_id"`
	WorkspaceID     foundation.ID `json:"workspace_id"`
	SourceVersionID foundation.ID `json:"source_version_id"`
	SourceSpanID    foundation.ID `json:"source_span_id"`
	CreatedAt       string        `json:"created_at"`
}

// ReplayProfileRetry returns the exact durable profile state bound to a repeated retry command.
func (repository *ProfileRepository) ReplayProfileRetry(ctx context.Context, binding captureapp.ProfileRetryBinding) (captureapp.ProfileRetryResult, bool, error) {
	if repository == nil || repository.db == nil || !validProfileRetryBinding(binding) {
		return captureapp.ProfileRetryResult{}, false, invalid("CAPTURE_PROFILE_RETRY_INVALID", "profile retry binding is invalid")
	}
	return replayProfileRetry(ctx, repository.db, binding)
}

// ScheduleProfileRetry resets only derived processing state and atomically appends normal Capture processing work.
func (repository *ProfileRepository) ScheduleProfileRetry(ctx context.Context, record captureapp.ProfileRetryRecord) (captureapp.ProfileRetryResult, error) {
	if repository == nil || repository.db == nil || !validProfileRetryRecord(record) {
		return captureapp.ProfileRetryResult{}, invalid("CAPTURE_PROFILE_RETRY_INVALID", "profile retry record is invalid")
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return captureapp.ProfileRetryResult{}, classify(err, "CAPTURE_PROFILE_RETRY_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	profile, found, err := loadProfileForUpdate(ctx, tx, record.Binding.WorkspaceID, record.Binding.SourceVersionID)
	if err != nil {
		return captureapp.ProfileRetryResult{}, classify(err, "CAPTURE_PROFILE_RETRY_QUERY_FAILED")
	}
	if !found {
		return captureapp.ProfileRetryResult{}, foundation.NewError(foundation.ErrorNotFound, "CAPTURE_PROFILE_NOT_FOUND", false, pgx.ErrNoRows)
	}
	if replay, found, replayErr := replayProfileRetry(ctx, tx, record.Binding); replayErr != nil {
		return captureapp.ProfileRetryResult{}, replayErr
	} else if found {
		return replay, nil
	}
	if profile.Version != record.Binding.ExpectedVersion {
		return captureapp.ProfileRetryResult{}, foundation.NewError(foundation.ErrorVersionConflict, "CAPTURE_PROFILE_VERSION_CONFLICT", false,
			errors.New("profile version changed"))
	}
	retryableFailure := profile.Status == domain.ProfileStatusFailed && profile.Retryable
	capabilityUnavailable := profile.Status == domain.ProfileStatusCapabilityUnavailable && !profile.Retryable
	staleRevision := profile.Status == domain.ProfileStatusStale && profile.CurrentRevisionID != ""
	if !retryableFailure && !capabilityUnavailable && !staleRevision {
		return captureapp.ProfileRetryResult{}, foundation.NewError(foundation.ErrorVersionConflict, "CAPTURE_PROFILE_RETRY_NOT_ALLOWED", false,
			errors.New("profile is not failed, unavailable, or stale"))
	}
	capture, err := lockCapture(ctx, tx, profile.WorkspaceID, profile.CaptureID)
	if err != nil {
		return captureapp.ProfileRetryResult{}, err
	}
	failedCapture := capture.Status == domain.StatusReadyDegraded && capture.ProfileStatus == domain.StageFailed && capture.Retryable
	unavailableCapture := capture.Status == domain.StatusReadyDegraded && capture.ProfileStatus == domain.StageCapabilityUnavailable && !capture.Retryable
	staleCapture := (capture.Status == domain.StatusReady || capture.Status == domain.StatusReadyDegraded) &&
		capture.ProfileStatus == domain.StageStale && !capture.Retryable
	if capture.LatestSourceVersionID != profile.SourceVersionID ||
		(retryableFailure && !failedCapture) || (capabilityUnavailable && !unavailableCapture) || (staleRevision && !staleCapture) {
		return captureapp.ProfileRetryResult{}, inconsistent("CAPTURE_PROFILE_RETRY_STATE_INVALID", "capture does not match the rebuildable profile state")
	}
	view, err := loadProfileView(ctx, tx, profile)
	if err != nil {
		return captureapp.ProfileRetryResult{}, err
	}
	profile, err = scanProfile(tx.QueryRow(ctx, `UPDATE learning.document_knowledge_profile SET
		status='PENDING',error_code='',retryable=false,version=version+1,updated_at=GREATEST(updated_at,$3)
		WHERE id=$1 AND workspace_id=$2 AND version=$4
		RETURNING id::text,workspace_id::text,capture_id::text,source_version_id::text,
			current_revision_id::text,status,error_code,retryable,version,created_at,updated_at`,
		string(profile.ID), string(profile.WorkspaceID), record.CreatedAt.UTC(), record.Binding.ExpectedVersion))
	if err != nil {
		return captureapp.ProfileRetryResult{}, profileCASFailure(err, "CAPTURE_PROFILE_VERSION_CONFLICT",
			"profile changed before retry scheduling")
	}
	capture, err = scanCapture(tx.QueryRow(ctx, `UPDATE core.capture SET
		status='SOURCE_SAVED',ingestion_status='READY',index_status='READY',profile_status='PENDING',
		failure_stage='',error_code='',retryable=false,version=version+1,updated_at=GREATEST(updated_at,$3)
		WHERE workspace_id=$1 AND id=$2 AND version=$4 RETURNING `+captureReturning,
		string(capture.WorkspaceID), string(capture.ID), record.CreatedAt.UTC(), capture.Version))
	if err != nil {
		return captureapp.ProfileRetryResult{}, profileCASFailure(err, "CAPTURE_PROFILE_RETRY_CAPTURE_UPDATE_FAILED",
			"capture changed before profile retry scheduling")
	}
	if err := insertProfileRetryOutbox(ctx, tx, record, capture); err != nil {
		return captureapp.ProfileRetryResult{}, err
	}
	view.Profile = profile
	result := captureapp.ProfileRetryResult{View: view}
	encoded, err := encodeProfileRetryReceipt(record.Binding, result)
	if err != nil {
		return captureapp.ProfileRetryResult{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.capture_command(
		workspace_id,idempotency_key,request_hash,command_type,capture_id,capture_version,response,created_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`,
		string(record.Binding.WorkspaceID), record.Binding.IdempotencyKey, record.Binding.RequestHash,
		profileRetryCommandType, string(capture.ID), capture.Version, encoded, record.CreatedAt.UTC()); err != nil {
		if !isUniqueViolation(err) {
			return captureapp.ProfileRetryResult{}, classify(err, "CAPTURE_PROFILE_RETRY_RECEIPT_INSERT_FAILED")
		}
		_ = tx.Rollback(ctx)
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
	if err := tx.Commit(ctx); err != nil {
		return captureapp.ProfileRetryResult{}, classify(err, "CAPTURE_PROFILE_RETRY_COMMIT_FAILED")
	}
	return result, nil
}

func replayProfileRetry(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, binding captureapp.ProfileRetryBinding) (captureapp.ProfileRetryResult, bool, error) {
	if !validProfileRetryBinding(binding) {
		return captureapp.ProfileRetryResult{}, false, invalid("CAPTURE_PROFILE_RETRY_INVALID", "profile retry binding is invalid")
	}
	commandBinding := captureapp.CommandBinding{
		WorkspaceID: binding.WorkspaceID, IdempotencyKey: binding.IdempotencyKey,
		RequestHash: binding.RequestHash, CommandType: profileRetryCommandType,
	}
	raw, found, err := loadCommandReceipt(ctx, db, commandBinding)
	if err != nil || !found {
		return captureapp.ProfileRetryResult{}, found, err
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

func insertProfileRetryOutbox(ctx context.Context, tx pgx.Tx, record captureapp.ProfileRetryRecord, capture domain.Capture) error {
	payload, err := json.Marshal(map[string]string{
		"capture_id": string(capture.ID), "workspace_id": string(capture.WorkspaceID),
	})
	if err != nil {
		return inconsistent("CAPTURE_PROFILE_RETRY_OUTBOX_ENCODING_FAILED", "profile retry outbox payload could not be encoded")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ops.capture_outbox(
		id,workspace_id,capture_id,schema_version,event_type,event_key,payload,available_at,
		attempt_count,manual_recovery_required,version,created_at,updated_at
	) VALUES($1,$2,$3,'capture-process-event/v1','capture.process_requested',$4,$5,$6,0,false,1,$6,$6)`,
		string(record.OutboxID), string(capture.WorkspaceID), string(capture.ID), record.EventKey, payload, record.CreatedAt.UTC()); err != nil {
		return classify(err, "CAPTURE_PROFILE_RETRY_OUTBOX_INSERT_FAILED")
	}
	return nil
}

func encodeProfileRetryReceipt(binding captureapp.ProfileRetryBinding, result captureapp.ProfileRetryResult) ([]byte, error) {
	if !validPersistedProfileView(result.View) {
		return nil, inconsistent("CAPTURE_PROFILE_RETRY_RECEIPT_INVALID", "profile retry result is invalid")
	}
	profile := result.View.Profile
	var revision *persistedProfileRevisionReceipt
	if result.View.Revision != nil {
		persisted := persistProfileRevisionReceipt(*result.View.Revision)
		revision = &persisted
	}
	evidence := make([]persistedProfileEvidenceReceipt, len(result.View.Evidence))
	for index, item := range result.View.Evidence {
		evidence[index] = persistProfileEvidenceReceipt(item)
	}
	encoded, err := json.Marshal(profileRetryReceipt{
		SchemaVersion: profileRetryReceiptSchemaVersion, WorkspaceID: binding.WorkspaceID, SourceVersionID: binding.SourceVersionID,
		ExpectedVersion: binding.ExpectedVersion, IdempotencyKey: binding.IdempotencyKey, RequestHash: binding.RequestHash,
		CommandType: profileRetryCommandType, Profile: persistProfileReceipt(profile), Revision: revision, Evidence: evidence,
	})
	if err != nil {
		return nil, inconsistent("CAPTURE_PROFILE_RETRY_RECEIPT_ENCODING_FAILED", "profile retry receipt could not be encoded")
	}
	return encoded, nil
}

func decodeProfileRetryReceipt(raw []byte) (profileRetryReceipt, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var receipt profileRetryReceipt
	if err := decoder.Decode(&receipt); err != nil {
		return profileRetryReceipt{}, inconsistent("CAPTURE_PROFILE_RETRY_RECEIPT_INVALID", "profile retry receipt is not valid JSON")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return profileRetryReceipt{}, inconsistent("CAPTURE_PROFILE_RETRY_RECEIPT_INVALID", "profile retry receipt contains trailing JSON")
	}
	if receipt.SchemaVersion != profileRetryReceiptSchemaVersion || !validID(receipt.WorkspaceID) || !validID(receipt.SourceVersionID) ||
		receipt.ExpectedVersion < 1 || strings.TrimSpace(receipt.IdempotencyKey) == "" || !validHash(receipt.RequestHash) ||
		receipt.CommandType != profileRetryCommandType {
		return profileRetryReceipt{}, inconsistent("CAPTURE_PROFILE_RETRY_RECEIPT_INVALID", "profile retry receipt identity is invalid")
	}
	return receipt, nil
}

func persistProfileReceipt(profile domain.Profile) persistedProfileReceipt {
	return persistedProfileReceipt{
		ID: profile.ID, WorkspaceID: profile.WorkspaceID, CaptureID: profile.CaptureID, SourceVersionID: profile.SourceVersionID,
		CurrentRevisionID: profile.CurrentRevisionID, Status: profile.Status, ErrorCode: profile.ErrorCode, Retryable: profile.Retryable,
		Version: profile.Version, CreatedAt: profile.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: profile.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func restoreProfileReceipt(value persistedProfileReceipt) (domain.Profile, error) {
	createdAt, err := time.Parse(time.RFC3339Nano, value.CreatedAt)
	if err != nil {
		return domain.Profile{}, err
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, value.UpdatedAt)
	if err != nil {
		return domain.Profile{}, err
	}
	profile := domain.Profile{
		ID: value.ID, WorkspaceID: value.WorkspaceID, CaptureID: value.CaptureID, SourceVersionID: value.SourceVersionID,
		CurrentRevisionID: value.CurrentRevisionID, Status: value.Status, ErrorCode: value.ErrorCode, Retryable: value.Retryable,
		Version: value.Version, CreatedAt: createdAt, UpdatedAt: updatedAt,
	}
	if err := profile.Validate(); err != nil {
		return domain.Profile{}, err
	}
	return profile, nil
}

func persistProfileRevisionReceipt(revision domain.ProfileRevision) persistedProfileRevisionReceipt {
	return persistedProfileRevisionReceipt{
		ID: revision.ID, ProfileID: revision.ProfileID, WorkspaceID: revision.WorkspaceID,
		SourceVersionID: revision.SourceVersionID, ParseProjectionID: revision.ParseProjectionID,
		IndexVersionID: revision.IndexVersionID, ModelRunID: revision.ModelRunID,
		ModelSettingsRevision: revision.ModelSettingsRevision, PromptVersion: revision.PromptVersion,
		SchemaVersion: revision.SchemaVersion, Content: revision.Content, ContentDigest: revision.ContentDigest,
		CreatedAt: revision.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func restoreProfileRevisionReceipt(value persistedProfileRevisionReceipt) (domain.ProfileRevision, error) {
	createdAt, err := time.Parse(time.RFC3339Nano, value.CreatedAt)
	if err != nil {
		return domain.ProfileRevision{}, err
	}
	revision := domain.ProfileRevision{
		ID: value.ID, ProfileID: value.ProfileID, WorkspaceID: value.WorkspaceID,
		SourceVersionID: value.SourceVersionID, ParseProjectionID: value.ParseProjectionID,
		IndexVersionID: value.IndexVersionID, ModelRunID: value.ModelRunID,
		ModelSettingsRevision: value.ModelSettingsRevision, PromptVersion: value.PromptVersion,
		SchemaVersion: value.SchemaVersion, Content: value.Content, ContentDigest: value.ContentDigest,
		CreatedAt: createdAt,
	}
	if err := revision.Validate(); err != nil {
		return domain.ProfileRevision{}, err
	}
	return revision, nil
}

func persistProfileEvidenceReceipt(evidence domain.ProfileEvidence) persistedProfileEvidenceReceipt {
	return persistedProfileEvidenceReceipt{
		RevisionID: evidence.RevisionID, WorkspaceID: evidence.WorkspaceID,
		SourceVersionID: evidence.SourceVersionID, SourceSpanID: evidence.SourceSpanID,
		CreatedAt: evidence.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func restoreProfileEvidenceReceipt(value persistedProfileEvidenceReceipt) (domain.ProfileEvidence, error) {
	createdAt, err := time.Parse(time.RFC3339Nano, value.CreatedAt)
	if err != nil {
		return domain.ProfileEvidence{}, err
	}
	return domain.ProfileEvidence{
		RevisionID: value.RevisionID, WorkspaceID: value.WorkspaceID,
		SourceVersionID: value.SourceVersionID, SourceSpanID: value.SourceSpanID, CreatedAt: createdAt,
	}, nil
}

func restoreProfileRetryView(receipt profileRetryReceipt) (captureapp.ProfileView, error) {
	profile, err := restoreProfileReceipt(receipt.Profile)
	if err != nil {
		return captureapp.ProfileView{}, err
	}
	view := captureapp.ProfileView{Profile: profile, Evidence: make([]domain.ProfileEvidence, len(receipt.Evidence))}
	if receipt.Revision != nil {
		revision, restoreErr := restoreProfileRevisionReceipt(*receipt.Revision)
		if restoreErr != nil {
			return captureapp.ProfileView{}, restoreErr
		}
		view.Revision = &revision
	}
	for index, item := range receipt.Evidence {
		view.Evidence[index], err = restoreProfileEvidenceReceipt(item)
		if err != nil {
			return captureapp.ProfileView{}, err
		}
	}
	if !validPersistedProfileView(view) {
		return captureapp.ProfileView{}, errors.New("profile retry view is invalid")
	}
	return view, nil
}

func validPersistedProfileView(view captureapp.ProfileView) bool {
	if view.Profile.Validate() != nil {
		return false
	}
	if view.Profile.CurrentRevisionID == "" {
		return view.Revision == nil && len(view.Evidence) == 0
	}
	if view.Revision == nil || view.Revision.Validate() != nil || view.Revision.ID != view.Profile.CurrentRevisionID ||
		view.Revision.ProfileID != view.Profile.ID || view.Revision.WorkspaceID != view.Profile.WorkspaceID ||
		view.Revision.SourceVersionID != view.Profile.SourceVersionID || len(view.Evidence) == 0 {
		return false
	}
	seen := make(map[foundation.ID]struct{}, len(view.Evidence))
	for _, evidence := range view.Evidence {
		if evidence.RevisionID != view.Revision.ID || evidence.WorkspaceID != view.Profile.WorkspaceID ||
			evidence.SourceVersionID != view.Profile.SourceVersionID || !validID(evidence.SourceSpanID) ||
			!samePersistedTime(evidence.CreatedAt, view.Revision.CreatedAt) {
			return false
		}
		if _, duplicate := seen[evidence.SourceSpanID]; duplicate {
			return false
		}
		seen[evidence.SourceSpanID] = struct{}{}
	}
	return sameFoundationIDs(profileContentEvidence(view.Revision.Content), evidenceSpanIDs(view.Evidence))
}

func validProfileRetryBinding(binding captureapp.ProfileRetryBinding) bool {
	return validID(binding.WorkspaceID) && validID(binding.SourceVersionID) && binding.ExpectedVersion > 0 &&
		strings.TrimSpace(binding.IdempotencyKey) != "" && len(binding.IdempotencyKey) <= 128 &&
		validHash(binding.RequestHash)
}

func validProfileRetryRecord(record captureapp.ProfileRetryRecord) bool {
	return validProfileRetryBinding(record.Binding) && validID(record.OutboxID) && strings.TrimSpace(record.EventKey) != "" &&
		len(record.EventKey) <= 256 && !record.CreatedAt.IsZero()
}

var _ captureapp.ProfileRetryScheduler = (*ProfileRepository)(nil)
