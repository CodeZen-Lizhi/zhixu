package postgres

import (
	"context"
	"errors"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

var _ authoringapp.GeneratedDocumentReader = (*GORMRepository)(nil)

// ReadGeneratedDocumentScoped acquires the same origin/document lock order as
// append and retire. It never reads outside the caller's transaction.
func (repository *GORMRepository) ReadGeneratedDocumentScoped(ctx context.Context, scope foundation.TransactionScope, workspaceID, documentID, originID foundation.ID) (authoringapp.GeneratedDocumentState, error) {
	var result authoringapp.GeneratedDocumentState
	if !validID(workspaceID) || !validID(documentID) || !validID(originID) {
		return result, generatedInvalid("generated document identity is invalid")
	}
	tx, err := repository.generatedScope(ctx, scope)
	if err != nil {
		return result, err
	}
	if err := gormLockGeneratedOrigin(ctx, tx, workspaceID, domain.GeneratedOriginSynthesisNote, originID); err != nil {
		return result, err
	}
	result.Document, err = gormLoadDocument(ctx, tx, workspaceID, documentID, true)
	if err != nil {
		return result, err
	}
	if err := gormVerifyGeneratedDocument(ctx, tx, workspaceID, documentID, domain.GeneratedOriginSynthesisNote, originID); err != nil {
		return result, err
	}
	result.Revision, err = gormLoadLatestRevision(ctx, tx, workspaceID, documentID, true)
	if err != nil {
		return result, err
	}
	binding, found, err := gormLoadPublicationRevision(ctx, tx, workspaceID, result.Revision.ID)
	if err != nil {
		return result, err
	}
	if found {
		result.Publication = &binding
		row, err := gormRow(tx, `SELECT version FROM change_control.proposal WHERE id=? AND workspace_id=?`, string(binding.ProposalID), string(workspaceID))
		if err != nil {
			return result, classifyGenerated(ctx, err, "AUTHORING_GENERATED_PROPOSAL_QUERY_FAILED")
		}
		if err = row.Scan(&result.ProposalVersion); err != nil {
			if gormNoRows(err) {
				return result, inconsistent("generated publication has no proposal")
			}
			return result, classifyGenerated(ctx, err, "AUTHORING_GENERATED_PROPOSAL_QUERY_FAILED")
		}
	}
	if result.Revision.DocumentID != result.Document.ID {
		return result, inconsistent("generated document revision binding changed")
	}
	if result.Document.Lifecycle == domain.DocumentDeleted || result.Document.Lifecycle == domain.DocumentArchived {
		return result, notFound(errors.New("generated document is unavailable"))
	}
	return result, nil
}
