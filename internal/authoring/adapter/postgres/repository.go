package postgres

import (
	"database/sql"
	"errors"
	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"strings"
	"time"
)

func scanDocument(row interface{ Scan(...any) error }) (domain.Document, error) {
	var document domain.Document
	err := row.Scan(
		&document.ID, &document.WorkspaceID, &document.CanonicalPath, &document.Title, &document.Lifecycle,
		&document.CurrentPublishedRevisionID, &document.Version, &document.CreatedAt, &document.UpdatedAt)
	if gormNoRows(err) {
		return domain.Document{}, notFound(err)
	}
	if err != nil {
		return domain.Document{}, classify(err, "AUTHORING_DOCUMENT_QUERY_FAILED")
	}
	document.CreatedAt = document.CreatedAt.UTC()
	document.UpdatedAt = document.UpdatedAt.UTC()
	if err := document.Validate(); err != nil {
		return domain.Document{}, inconsistent("stored document is invalid")
	}
	return document, nil
}

const draftColumns = `id::text,workspace_id::text,COALESCE(document_id::text,''),title,target_path,body,status,version,created_at,updated_at`

func scanDraft(row interface{ Scan(...any) error }) (domain.WorkingDraft, error) {
	var draft domain.WorkingDraft
	if err := row.Scan(&draft.ID, &draft.WorkspaceID, &draft.DocumentID, &draft.Title, &draft.TargetPath,
		&draft.Body, &draft.Status, &draft.Version, &draft.CreatedAt, &draft.UpdatedAt); err != nil {
		return domain.WorkingDraft{}, err
	}
	draft.CreatedAt = draft.CreatedAt.UTC()
	draft.UpdatedAt = draft.UpdatedAt.UTC()
	if err := draft.Validate(); err != nil {
		return domain.WorkingDraft{}, inconsistent("stored working draft is invalid")
	}
	return draft, nil
}

const revisionColumns = `id::text,workspace_id::text,document_id::text,COALESCE(source_version_id::text,''),
	COALESCE(parent_revision_id::text,''),revision_no,content,content_hash,status,optimization_mode,
	COALESCE(git_commit,''),created_by_type,created_at`

func scanRevision(row interface{ Scan(...any) error }) (domain.ArticleRevision, error) {
	var revision domain.ArticleRevision
	if err := row.Scan(&revision.ID, &revision.WorkspaceID, &revision.DocumentID, &revision.SourceVersionID,
		&revision.ParentRevisionID, &revision.RevisionNo, &revision.Content, &revision.ContentHash,
		&revision.Status, &revision.OptimizationMode, &revision.GitCommit, &revision.CreatedByType,
		&revision.CreatedAt); err != nil {
		if gormNoRows(err) {
			return domain.ArticleRevision{}, err
		}
		return domain.ArticleRevision{}, classify(err, "AUTHORING_REVISION_QUERY_FAILED")
	}
	revision.CreatedAt = revision.CreatedAt.UTC()
	if err := revision.Validate(); err != nil {
		return domain.ArticleRevision{}, inconsistent("stored article revision is invalid")
	}
	return revision, nil
}

type commandReceipt struct {
	WorkspaceID                foundation.ID
	IdempotencyKey             string
	RequestHash                string
	CommandType                string
	DraftID                    foundation.ID
	ExpectedVersion            int64
	ResultDraftVersion         int64
	DocumentID                 foundation.ID
	DocumentVersion            int64
	RevisionID                 foundation.ID
	RevisionNo                 int
	ParentRevisionID           foundation.ID
	DraftCreatedAt             sql.NullTime
	DocumentCreatedAt          sql.NullTime
	DocumentLifecycle          string
	CurrentPublishedRevisionID foundation.ID
	Title                      string
	TargetPath                 string
	ContentHash                string
	CreatedAt                  time.Time
}

func (receipt commandReceipt) matches(binding authoringapp.CommandBinding) error {
	if receipt.WorkspaceID != binding.WorkspaceID || receipt.IdempotencyKey != binding.IdempotencyKey ||
		receipt.RequestHash != binding.RequestHash || receipt.CommandType != binding.CommandType ||
		receipt.ExpectedVersion != binding.ExpectedVersion ||
		(binding.CommandType != authoringapp.CommandCreate && receipt.DraftID != binding.DraftID) {
		return idempotencyConflict(errors.New("idempotency key is bound to another authoring request"))
	}
	return nil
}

func (receipt commandReceipt) createResult() (authoringapp.CreateResult, error) {
	if receipt.CommandType != authoringapp.CommandCreate || !receipt.DraftCreatedAt.Valid ||
		!validID(receipt.DraftID) || receipt.ResultDraftVersion != 1 {
		return authoringapp.CreateResult{}, inconsistent("create receipt is invalid")
	}
	draft, err := domain.NewBlankWorkingDraft(receipt.DraftID, receipt.WorkspaceID, receipt.DraftCreatedAt.Time.UTC())
	if err != nil {
		return authoringapp.CreateResult{}, inconsistent("create receipt draft is invalid")
	}
	return authoringapp.CreateResult{Draft: draft}, nil
}

func (receipt commandReceipt) updateResult(title, targetPath, body string) (authoringapp.UpdateResult, error) {
	if receipt.CommandType != authoringapp.CommandUpdate || !receipt.DraftCreatedAt.Valid ||
		!validID(receipt.DraftID) || receipt.ResultDraftVersion != receipt.ExpectedVersion+1 {
		return authoringapp.UpdateResult{}, inconsistent("update receipt is invalid")
	}
	draft := domain.WorkingDraft{
		ID: receipt.DraftID, WorkspaceID: receipt.WorkspaceID, DocumentID: receipt.DocumentID,
		Title: title, TargetPath: targetPath, Body: body, Status: domain.WorkingDraftEditing,
		Version: receipt.ResultDraftVersion, CreatedAt: receipt.DraftCreatedAt.Time.UTC(), UpdatedAt: receipt.CreatedAt,
	}
	if err := draft.Validate(); err != nil {
		return authoringapp.UpdateResult{}, inconsistent("update receipt draft is invalid")
	}
	return authoringapp.UpdateResult{Draft: draft}, nil
}

func validateCreateRecord(record authoringapp.CreateRecord) error {
	if err := validateBinding(record.Binding, authoringapp.CommandCreate, false); err != nil {
		return err
	}
	if err := record.Draft.Validate(); err != nil || record.Draft.ID != record.Binding.DraftID ||
		record.Draft.WorkspaceID != record.Binding.WorkspaceID || record.Draft.Version != 1 ||
		record.Draft.DocumentID != "" || record.Draft.Title != "" || record.Draft.TargetPath != "" || record.Draft.Body != "" {
		return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeDraftInvalid, false, errors.New("create record is invalid"))
	}
	return nil
}

func validateUpdateRecord(record authoringapp.UpdateRecord) error {
	if err := validateBinding(record.Binding, authoringapp.CommandUpdate, true); err != nil {
		return err
	}
	identity := domain.WorkingDraft{ID: record.Binding.DraftID, WorkspaceID: record.Binding.WorkspaceID}
	hash, err := domain.ComputeUpdateRequestHash(identity, record.Binding.ExpectedVersion, record.Title, record.TargetPath, record.Body)
	if err != nil || hash != record.Binding.RequestHash || record.UpdatedAt.IsZero() {
		return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeDraftInvalid, false, errors.New("update record is invalid"))
	}
	return nil
}

func validateFreezeRecord(record authoringapp.FreezeRecord) error {
	if err := validateBinding(record.Binding, authoringapp.CommandFreeze, true); err != nil {
		return err
	}
	hash, err := domain.ComputeFreezeRequestHash(record.Binding.WorkspaceID, record.Binding.DraftID, record.Binding.ExpectedVersion)
	if err != nil || hash != record.Binding.RequestHash || !validID(record.CandidateDocumentID) ||
		!validID(record.RevisionID) || record.FrozenAt.IsZero() {
		return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeFreezeInvalid, false, errors.New("freeze record is invalid"))
	}
	return nil
}

func validateBinding(binding authoringapp.CommandBinding, commandType string, expectedVersion bool) error {
	if !validID(binding.WorkspaceID) || !validID(binding.DraftID) || binding.CommandType != commandType ||
		binding.IdempotencyKey == "" || binding.IdempotencyKey != strings.TrimSpace(binding.IdempotencyKey) ||
		len(binding.IdempotencyKey) > authoringapp.MaxIdempotencyKeyBytes || strings.ContainsAny(binding.IdempotencyKey, "\r\n\x00") ||
		!validHash(binding.RequestHash) || (expectedVersion && binding.ExpectedVersion < 1) ||
		(!expectedVersion && binding.ExpectedVersion != 0) {
		return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeDraftInvalid, false, errors.New("authoring command binding is invalid"))
	}
	return nil
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

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}
