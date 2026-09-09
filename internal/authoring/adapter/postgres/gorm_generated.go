package postgres

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
)

var _ authoringapp.GeneratedRepository = (*GORMRepository)(nil)

// AppendGeneratedRevisionScoped writes AGENT provenance and its immutable receipt
// inside the caller's transaction. No Working Draft or publication pointer changes.
func (repository *GORMRepository) AppendGeneratedRevisionScoped(ctx context.Context, scope foundation.TransactionScope, record authoringapp.GeneratedRevisionRecord) (authoringapp.GeneratedRevisionResult, error) {
	tx, err := repository.generatedScope(ctx, scope)
	if err != nil {
		return authoringapp.GeneratedRevisionResult{}, err
	}
	request := record.Request
	hash, err := domain.ComputeGeneratedRevisionRequestHash(request)
	if err != nil {
		return authoringapp.GeneratedRevisionResult{}, err
	}
	if !validGeneratedKey(record.IdempotencyKey) || record.RequestHash != hash || record.CreatedAt.IsZero() {
		return authoringapp.GeneratedRevisionResult{}, generatedInvalid("generated revision command binding is invalid")
	}
	if err := gormLockCommand(ctx, tx, request.WorkspaceID, "generated-revision:"+record.IdempotencyKey); err != nil {
		return authoringapp.GeneratedRevisionResult{}, err
	}
	var receipt gormGeneratedRevisionRecord
	err = tx.Select(generatedRevisionColumns).Where("workspace_id=? AND idempotency_key=?", string(request.WorkspaceID), record.IdempotencyKey).Take(&receipt).Error
	if err == nil {
		if receipt.RequestHash != record.RequestHash || !generatedReceiptMatchesRequest(receipt, request) {
			return authoringapp.GeneratedRevisionResult{}, idempotencyConflict(errors.New("generated revision key is bound to another request"))
		}
		return gormGeneratedRevisionReplay(ctx, tx, receipt)
	}
	if !gormNoRows(err) {
		return authoringapp.GeneratedRevisionResult{}, classifyGenerated(ctx, err, "AUTHORING_GENERATED_RECEIPT_QUERY_FAILED")
	}
	if err := gormLockGeneratedOrigin(ctx, tx, request.WorkspaceID, request.OriginKind, request.OriginID); err != nil {
		return authoringapp.GeneratedRevisionResult{}, err
	}
	var existing *domain.Document
	var parent *domain.ArticleRevision
	if request.ExpectedDocumentVersion > 0 {
		document, err := gormLoadDocument(ctx, tx, request.WorkspaceID, request.DocumentID, true)
		if err != nil {
			return authoringapp.GeneratedRevisionResult{}, err
		}
		if err := gormVerifyGeneratedDocument(ctx, tx, request.WorkspaceID, request.DocumentID, request.OriginKind, request.OriginID); err != nil {
			return authoringapp.GeneratedRevisionResult{}, err
		}
		latest, err := gormLoadLatestRevision(ctx, tx, request.WorkspaceID, request.DocumentID, true)
		if err != nil {
			return authoringapp.GeneratedRevisionResult{}, err
		}
		if latest.ID != request.ParentRevisionID {
			return authoringapp.GeneratedRevisionResult{}, generatedOriginConflict("generated parent is not the latest article revision")
		}
		if err := gormVerifyGeneratedParent(ctx, tx, request, latest); err != nil {
			return authoringapp.GeneratedRevisionResult{}, err
		}
		existing, parent = &document, &latest
	}
	document, revision, err := domain.PrepareGeneratedRevision(request, existing, parent, record.CreatedAt)
	if err != nil {
		return authoringapp.GeneratedRevisionResult{}, err
	}
	if existing != nil {
		if err := gormRequireNoGeneratedPublication(ctx, tx, request.WorkspaceID, request.DocumentID); err != nil {
			return authoringapp.GeneratedRevisionResult{}, err
		}
		changed := tx.Model(&gormDocumentRecord{}).Where("id=? AND workspace_id=? AND version=?", string(document.ID), string(document.WorkspaceID), request.ExpectedDocumentVersion).
			Updates(map[string]any{"title": document.Title, "version": document.Version, "updated_at": document.UpdatedAt})
		if changed.Error != nil {
			return authoringapp.GeneratedRevisionResult{}, classifyGenerated(ctx, changed.Error, "AUTHORING_GENERATED_DOCUMENT_UPDATE_FAILED")
		}
		if changed.RowsAffected != 1 {
			return authoringapp.GeneratedRevisionResult{}, generatedOriginConflict("generated document version changed")
		}
	} else {
		model := gormDocumentRecord{ID: string(document.ID), WorkspaceID: string(document.WorkspaceID), CanonicalPath: document.CanonicalPath,
			Title: document.Title, LifecycleStatus: string(document.Lifecycle), Version: document.Version, CreatedAt: document.CreatedAt, UpdatedAt: document.UpdatedAt}
		if err := tx.Create(&model).Error; err != nil {
			return authoringapp.GeneratedRevisionResult{}, classifyGenerated(ctx, err, "AUTHORING_GENERATED_DOCUMENT_CREATE_FAILED")
		}
		origin := gormGeneratedDocumentRecord{DocumentID: string(document.ID), WorkspaceID: string(document.WorkspaceID),
			OriginKind: string(request.OriginKind), OriginID: string(request.OriginID), CreatedAt: document.CreatedAt}
		if err := tx.Create(&origin).Error; err != nil {
			return authoringapp.GeneratedRevisionResult{}, classifyGenerated(ctx, err, "AUTHORING_GENERATED_ORIGIN_CREATE_FAILED")
		}
	}
	article := gormArticleRevisionRecord{ID: string(revision.ID), WorkspaceID: string(revision.WorkspaceID), DocumentID: string(revision.DocumentID),
		ParentRevisionID: generatedNullableID(revision.ParentRevisionID), RevisionNo: revision.RevisionNo,
		Content: revision.Content, ContentHash: revision.ContentHash, Status: string(revision.Status),
		OptimizationMode: revision.OptimizationMode, CreatedByType: revision.CreatedByType, CreatedAt: revision.CreatedAt}
	if err := tx.Create(&article).Error; err != nil {
		return authoringapp.GeneratedRevisionResult{}, classifyGenerated(ctx, err, "AUTHORING_GENERATED_REVISION_CREATE_FAILED")
	}
	receipt = gormGeneratedRevisionRecord{ArticleRevisionID: string(revision.ID), WorkspaceID: string(revision.WorkspaceID), DocumentID: string(revision.DocumentID),
		OriginKind: string(request.OriginKind), OriginID: string(request.OriginID), OriginRevisionID: string(request.OriginRevisionID), ProjectionHash: request.ProjectionHash,
		IdempotencyKey: record.IdempotencyKey, RequestHash: record.RequestHash, ExpectedDocumentVersion: request.ExpectedDocumentVersion, DocumentVersion: document.Version,
		RevisionNo: revision.RevisionNo, ParentRevisionID: generatedNullableID(revision.ParentRevisionID), ContentHash: revision.ContentHash,
		Title: document.Title, TargetPath: document.CanonicalPath, DocumentCreatedAt: document.CreatedAt, DocumentLifecycle: string(document.Lifecycle),
		PublishedRevisionID: generatedNullableID(document.CurrentPublishedRevisionID), CreatedAt: revision.CreatedAt}
	if err := tx.Create(&receipt).Error; err != nil {
		return authoringapp.GeneratedRevisionResult{}, classifyGenerated(ctx, err, "AUTHORING_GENERATED_RECEIPT_CREATE_FAILED")
	}
	return authoringapp.GeneratedRevisionResult{Document: document, Revision: revision}, nil
}

func (repository *GORMRepository) generatedScope(ctx context.Context, scope foundation.TransactionScope) (*gorm.DB, error) {
	if err := repository.ready(); err != nil {
		return nil, err
	}
	if ctx == nil {
		return nil, generatedInvalid("generated authoring context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, classifyGORM(ctx, err, "AUTHORING_GENERATED_TRANSACTION_FAILED")
	}
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeGeneratedInvalid, false, err)
	}
	return tx.WithContext(ctx), nil
}

func gormLockGeneratedOrigin(ctx context.Context, tx *gorm.DB, workspaceID foundation.ID, kind domain.GeneratedOriginKind, originID foundation.ID) error {
	return gormLockCommand(ctx, tx, workspaceID, "generated-origin:"+string(kind)+":"+string(originID))
}

func gormVerifyGeneratedDocument(ctx context.Context, tx *gorm.DB, workspaceID, documentID foundation.ID, kind domain.GeneratedOriginKind, originID foundation.ID) error {
	var origin gormGeneratedDocumentRecord
	err := tx.Select("document_id,workspace_id,origin_kind,origin_id,created_at").Where("document_id=? AND workspace_id=?", string(documentID), string(workspaceID)).Take(&origin).Error
	if gormNoRows(err) || err == nil && (origin.OriginKind != string(kind) || origin.OriginID != string(originID)) {
		return generatedOriginConflict("document is not owned by this generated origin")
	}
	return classifyGenerated(ctx, err, "AUTHORING_GENERATED_ORIGIN_QUERY_FAILED")
}

func gormVerifyGeneratedParent(ctx context.Context, tx *gorm.DB, request domain.GeneratedRevisionRequest, parent domain.ArticleRevision) error {
	var receipt gormGeneratedRevisionRecord
	err := tx.Select(generatedRevisionColumns).Where("article_revision_id=? AND workspace_id=?", string(parent.ID), string(request.WorkspaceID)).Take(&receipt).Error
	if gormNoRows(err) || err == nil && (receipt.DocumentID != string(request.DocumentID) || receipt.OriginKind != string(request.OriginKind) ||
		receipt.OriginID != string(request.OriginID) || receipt.RevisionNo != parent.RevisionNo || receipt.ContentHash != parent.ContentHash) {
		return generatedOriginConflict("parent revision is not owned by this generated origin")
	}
	return classifyGenerated(ctx, err, "AUTHORING_GENERATED_PARENT_QUERY_FAILED")
}

func gormRequireNoGeneratedPublication(ctx context.Context, tx *gorm.DB, workspaceID, documentID foundation.ID) error {
	row, err := gormRow(tx, `SELECT EXISTS (
		SELECT 1 FROM authoring.document_publication_reservation WHERE workspace_id=? AND document_id=? AND status=?
	) OR EXISTS (
		SELECT 1 FROM authoring.document_publication_binding WHERE workspace_id=? AND document_id=? AND status IN (?,?)
	)`, string(workspaceID), string(documentID), string(authoringapp.PublicationReservationPending),
		string(workspaceID), string(documentID), string(domain.PublicationPending), string(domain.PublicationRecoveryRequired))
	if err != nil {
		return classifyGenerated(ctx, err, "AUTHORING_GENERATED_PUBLICATION_QUERY_FAILED")
	}
	var busy bool
	if err := row.Scan(&busy); err != nil {
		return classifyGenerated(ctx, err, "AUTHORING_GENERATED_PUBLICATION_QUERY_FAILED")
	}
	if busy {
		return generatedPublicationBusy("generated document has an unfinished publication")
	}
	return nil
}

func generatedReceiptMatchesRequest(receipt gormGeneratedRevisionRecord, request domain.GeneratedRevisionRequest) bool {
	return receipt.WorkspaceID == string(request.WorkspaceID) && receipt.DocumentID == string(request.DocumentID) &&
		receipt.ArticleRevisionID == string(request.ArticleRevisionID) && receipt.OriginKind == string(request.OriginKind) &&
		receipt.OriginID == string(request.OriginID) && receipt.OriginRevisionID == string(request.OriginRevisionID) &&
		receipt.ProjectionHash == request.ProjectionHash && receipt.ExpectedDocumentVersion == request.ExpectedDocumentVersion &&
		receipt.RevisionNo == request.RevisionNo && generatedID(receipt.ParentRevisionID) == request.ParentRevisionID &&
		receipt.Title == request.Title && receipt.TargetPath == request.TargetPath && receipt.ContentHash == domain.ComputeContentHash(request.Content)
}

func gormGeneratedRevisionReplay(ctx context.Context, tx *gorm.DB, receipt gormGeneratedRevisionRecord) (authoringapp.GeneratedRevisionResult, error) {
	revision, err := gormLoadRevision(ctx, tx, foundation.ID(receipt.WorkspaceID), foundation.ID(receipt.DocumentID), foundation.ID(receipt.ArticleRevisionID), false)
	if err != nil {
		return authoringapp.GeneratedRevisionResult{}, err
	}
	if revision.CreatedByType != "AGENT" || revision.SourceVersionID != "" || revision.ContentHash != receipt.ContentHash ||
		revision.RevisionNo != receipt.RevisionNo || revision.ParentRevisionID != generatedID(receipt.ParentRevisionID) ||
		!revision.CreatedAt.Equal(receipt.CreatedAt) || receipt.DocumentVersion != receipt.ExpectedDocumentVersion+1 {
		return authoringapp.GeneratedRevisionResult{}, inconsistent("generated revision receipt does not match the immutable article")
	}
	revision.Status, revision.GitCommit = domain.RevisionDraft, ""
	document := domain.Document{ID: revision.DocumentID, WorkspaceID: revision.WorkspaceID, Title: receipt.Title, CanonicalPath: receipt.TargetPath,
		Lifecycle: domain.DocumentLifecycle(receipt.DocumentLifecycle), CurrentPublishedRevisionID: generatedID(receipt.PublishedRevisionID),
		Version: receipt.DocumentVersion, CreatedAt: receipt.DocumentCreatedAt.UTC(), UpdatedAt: receipt.CreatedAt.UTC()}
	if document.Validate() != nil || revision.Validate() != nil {
		return authoringapp.GeneratedRevisionResult{}, inconsistent("generated revision receipt snapshot is invalid")
	}
	return authoringapp.GeneratedRevisionResult{Document: document, Revision: revision, Replayed: true}, nil
}

func generatedNullableID(id foundation.ID) *string {
	if id == "" {
		return nil
	}
	value := string(id)
	return &value
}

func generatedID(id *string) foundation.ID {
	if id == nil {
		return ""
	}
	return foundation.ID(*id)
}

func validGeneratedKey(key string) bool {
	return key != "" && key == strings.TrimSpace(key) && len(key) <= authoringapp.MaxIdempotencyKeyBytes &&
		utf8.ValidString(key) && !strings.ContainsAny(key, "\r\n\x00")
}

func generatedInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeGeneratedInvalid, false, errors.New(message))
}

func generatedOriginConflict(message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeGeneratedOriginConflict, false, errors.New(message))
}

func generatedPublicationBusy(message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeGeneratedPublicationBusy, false, errors.New(message))
}

func classifyGenerated(ctx context.Context, err error, fallbackCode string) error {
	if platformpostgres.SQLState(err) == "23505" {
		switch platformpostgres.ConstraintName(err) {
		case "uq_authoring_generated_revision_key", "generated_publication_retirement_pkey":
			return idempotencyConflict(err)
		case "generated_document_pkey", "uq_authoring_generated_document_origin", "generated_article_revision_pkey",
			"uq_authoring_generated_revision_origin", "generated_publication_retirement_publication_id_key", "document_pkey", "article_revision_pkey":
			return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeGeneratedOriginConflict, false, err)
		}
	}
	return classifyGORM(ctx, err, fallbackCode)
}
