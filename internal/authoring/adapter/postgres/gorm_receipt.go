package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"gorm.io/gorm"
)

func gormLoadReceipt(ctx context.Context, tx *gorm.DB, workspaceID foundation.ID, key string) (commandReceipt, bool, error) {
	var receipt commandReceipt
	row, err := gormRow(tx.WithContext(ctx), `SELECT workspace_id::text,idempotency_key,request_hash,command_type,COALESCE(working_draft_id::text,''),expected_version,COALESCE(result_draft_version,0),COALESCE(document_id::text,''),COALESCE(document_version,0),COALESCE(article_revision_id::text,''),COALESCE(revision_no,0),COALESCE(parent_revision_id::text,''),response_draft_created_at,response_document_created_at,COALESCE(response_document_lifecycle,''),COALESCE(response_published_revision_id::text,''),COALESCE(response_title,''),COALESCE(response_target_path,''),COALESCE(response_content_hash,''),created_at FROM authoring.working_draft_command WHERE workspace_id=? AND idempotency_key=?`, string(workspaceID), key)
	if err != nil {
		return receipt, false, classifyGORM(ctx, err, "AUTHORING_RECEIPT_QUERY_FAILED")
	}
	err = row.Scan(&receipt.WorkspaceID, &receipt.IdempotencyKey, &receipt.RequestHash, &receipt.CommandType, &receipt.DraftID, &receipt.ExpectedVersion, &receipt.ResultDraftVersion, &receipt.DocumentID, &receipt.DocumentVersion, &receipt.RevisionID, &receipt.RevisionNo, &receipt.ParentRevisionID, &receipt.DraftCreatedAt, &receipt.DocumentCreatedAt, &receipt.DocumentLifecycle, &receipt.CurrentPublishedRevisionID, &receipt.Title, &receipt.TargetPath, &receipt.ContentHash, &receipt.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return commandReceipt{}, false, nil
	}
	if err != nil {
		return receipt, false, classifyGORM(ctx, err, "AUTHORING_RECEIPT_QUERY_FAILED")
	}
	receipt.CreatedAt = receipt.CreatedAt.UTC()
	return receipt, true, nil
}

func (receipt commandReceipt) gormFreezeResult(ctx context.Context, tx *gorm.DB) (authoringapp.FreezeResult, error) {
	if receipt.CommandType != authoringapp.CommandFreeze || !receipt.DraftCreatedAt.Valid || !receipt.DocumentCreatedAt.Valid || !validID(receipt.DraftID) || !validID(receipt.DocumentID) || !validID(receipt.RevisionID) || receipt.ResultDraftVersion != receipt.ExpectedVersion+1 || receipt.DocumentVersion < 1 || receipt.RevisionNo < 1 {
		return authoringapp.FreezeResult{}, inconsistent("freeze receipt is invalid")
	}
	var content string
	var createdAt time.Time
	row, e := gormRow(tx.WithContext(ctx), `SELECT content,created_at FROM core.article_revision WHERE id=? AND workspace_id=? AND document_id=? AND revision_no=? AND content_hash=?`, string(receipt.RevisionID), string(receipt.WorkspaceID), string(receipt.DocumentID), receipt.RevisionNo, receipt.ContentHash)
	if e != nil {
		return authoringapp.FreezeResult{}, classifyGORM(ctx, e, "AUTHORING_REVISION_QUERY_FAILED")
	}
	if e = row.Scan(&content, &createdAt); e != nil {
		if gormNoRows(e) {
			return authoringapp.FreezeResult{}, inconsistent("freeze receipt revision binding is missing")
		}
		return authoringapp.FreezeResult{}, classifyGORM(ctx, e, "AUTHORING_REVISION_QUERY_FAILED")
	}
	draft := domain.WorkingDraft{ID: receipt.DraftID, WorkspaceID: receipt.WorkspaceID, DocumentID: receipt.DocumentID, Title: receipt.Title, TargetPath: receipt.TargetPath, Body: content, Status: domain.WorkingDraftEditing, Version: receipt.ResultDraftVersion, CreatedAt: receipt.DraftCreatedAt.Time.UTC(), UpdatedAt: receipt.CreatedAt}
	document := domain.Document{ID: receipt.DocumentID, WorkspaceID: receipt.WorkspaceID, CanonicalPath: receipt.TargetPath, Title: receipt.Title, Lifecycle: domain.DocumentLifecycle(receipt.DocumentLifecycle), CurrentPublishedRevisionID: receipt.CurrentPublishedRevisionID, Version: receipt.DocumentVersion, CreatedAt: receipt.DocumentCreatedAt.Time.UTC(), UpdatedAt: receipt.CreatedAt}
	revision := domain.ArticleRevision{ID: receipt.RevisionID, WorkspaceID: receipt.WorkspaceID, DocumentID: receipt.DocumentID, ParentRevisionID: receipt.ParentRevisionID, RevisionNo: receipt.RevisionNo, Content: content, ContentHash: receipt.ContentHash, Status: domain.RevisionDraft, OptimizationMode: "NONE", CreatedByType: "USER", CreatedAt: createdAt.UTC()}
	if draft.Validate() != nil || document.Validate() != nil || revision.Validate() != nil {
		return authoringapp.FreezeResult{}, inconsistent("freeze receipt snapshot is invalid")
	}
	return authoringapp.FreezeResult{Draft: draft, Document: document, Revision: revision}, nil
}
