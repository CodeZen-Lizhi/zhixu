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
