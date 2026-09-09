package postgres

import (
	"context"
	"errors"
	"reflect"
	"time"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// ReservePublicationScoped protects the generated candidate's creation-to-Proposal
// window in the same transaction as its semantic projection.
func (repository *GORMRepository) ReservePublicationScoped(ctx context.Context, scope foundation.TransactionScope, record authoringapp.ReservePublicationRecord) (authoringapp.PublicationPreparation, error) {
	tx, err := repository.generatedScope(ctx, scope)
	if err != nil {
		return authoringapp.PublicationPreparation{}, err
	}
	if err := validateReservePublicationRecord(record); err != nil {
		return authoringapp.PublicationPreparation{}, err
	}
	return gormReservePublication(ctx, tx, record)
}

// RetireGeneratedPublicationScoped proves generated ownership, fences the old
// Proposal, and closes its publication with the existing needs_revision rule.
func (repository *GORMRepository) RetireGeneratedPublicationScoped(ctx context.Context, scope foundation.TransactionScope, record authoringapp.RetireGeneratedPublicationRecord, retirer authoringapp.GeneratedProposalRetirer) (authoringapp.RetireGeneratedPublicationResult, error) {
	tx, err := repository.generatedScope(ctx, scope)
	if err != nil {
		return authoringapp.RetireGeneratedPublicationResult{}, err
	}
	request := record.Request
	hash, err := domain.ComputeGeneratedPublicationRetirementRequestHash(request)
	if err != nil {
		return authoringapp.RetireGeneratedPublicationResult{}, err
	}
	if record.RequestHash != hash || !validGeneratedKey(record.IdempotencyKey) || record.RetiredAt.IsZero() {
		return authoringapp.RetireGeneratedPublicationResult{}, generatedInvalid("generated retirement command binding is invalid")
	}
	if nilGeneratedRetirer(retirer) {
		return authoringapp.RetireGeneratedPublicationResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "AUTHORING_GENERATED_RETIRER_UNAVAILABLE", false, errors.New("generated publication retirer is unavailable"))
	}
	if err := gormLockCommand(ctx, tx, request.WorkspaceID, "generated-retirement:"+record.IdempotencyKey); err != nil {
		return authoringapp.RetireGeneratedPublicationResult{}, err
	}
	var receipt gormGeneratedRetirementRecord
	err = tx.Select(generatedRetirementColumns).Where("workspace_id=? AND idempotency_key=?", string(request.WorkspaceID), record.IdempotencyKey).Take(&receipt).Error
	if err == nil {
		if receipt.RequestHash != record.RequestHash || receipt.request() != request {
			return authoringapp.RetireGeneratedPublicationResult{}, idempotencyConflict(errors.New("generated retirement key is bound to another request"))
		}
		binding, found, err := gormLoadPublicationID(ctx, tx, request.WorkspaceID, request.PublicationID, false)
		if err != nil {
			return authoringapp.RetireGeneratedPublicationResult{}, err
		}
		if !found || binding.DocumentID != request.DocumentID || binding.ArticleRevisionID != request.ArticleRevisionID ||
			binding.Status != domain.PublicationClosed || binding.ErrorCode != publicationNeedsRevision {
			return authoringapp.RetireGeneratedPublicationResult{}, inconsistent("generated retirement receipt has no exact closed publication")
		}
		return authoringapp.RetireGeneratedPublicationResult{Publication: binding, Replayed: true}, nil
	}
	if !gormNoRows(err) {
		return authoringapp.RetireGeneratedPublicationResult{}, classifyGenerated(ctx, err, "AUTHORING_GENERATED_RETIREMENT_QUERY_FAILED")
	}
	if err := gormLockGeneratedOrigin(ctx, tx, request.WorkspaceID, request.OriginKind, request.OriginID); err != nil {
		return authoringapp.RetireGeneratedPublicationResult{}, err
	}
	document, err := gormLoadDocument(ctx, tx, request.WorkspaceID, request.DocumentID, true)
	if err != nil {
		return authoringapp.RetireGeneratedPublicationResult{}, err
	}
	if err := gormVerifyGeneratedDocument(ctx, tx, request.WorkspaceID, request.DocumentID, request.OriginKind, request.OriginID); err != nil {
		return authoringapp.RetireGeneratedPublicationResult{}, err
	}
	revision, err := gormLoadLatestRevision(ctx, tx, request.WorkspaceID, request.DocumentID, true)
	if err != nil {
		return authoringapp.RetireGeneratedPublicationResult{}, err
	}
	if document.Version != request.ExpectedDocumentVersion || revision.ID != request.ArticleRevisionID ||
		revision.CreatedByType != "AGENT" || revision.SourceVersionID != "" ||
		(document.Lifecycle != domain.DocumentDraft && document.Lifecycle != domain.DocumentPublished) {
		return authoringapp.RetireGeneratedPublicationResult{}, generatedOriginConflict("generated retirement document baseline changed")
	}
	var generated gormGeneratedRevisionRecord
	err = tx.Select(generatedRevisionColumns).Where("article_revision_id=? AND workspace_id=?", string(request.ArticleRevisionID), string(request.WorkspaceID)).Take(&generated).Error
	if gormNoRows(err) || err == nil && (generated.DocumentID != string(request.DocumentID) || generated.OriginKind != string(request.OriginKind) ||
		generated.OriginID != string(request.OriginID) || generated.OriginRevisionID != string(request.OriginRevisionID) ||
		generated.ProjectionHash != request.ProjectionHash || generated.ContentHash != revision.ContentHash) {
		return authoringapp.RetireGeneratedPublicationResult{}, generatedOriginConflict("generated retirement semantic origin changed")
	}
	if err != nil {
		return authoringapp.RetireGeneratedPublicationResult{}, classifyGenerated(ctx, err, "AUTHORING_GENERATED_RETIREMENT_ORIGIN_QUERY_FAILED")
	}
	binding, found, err := gormLoadPublicationID(ctx, tx, request.WorkspaceID, request.PublicationID, true)
	if err != nil {
		return authoringapp.RetireGeneratedPublicationResult{}, err
	}
	if !found || binding.DocumentID != request.DocumentID || binding.ArticleRevisionID != request.ArticleRevisionID {
		return authoringapp.RetireGeneratedPublicationResult{}, notFound(errors.New("generated publication binding was not found"))
	}
	if binding.Status != domain.PublicationPending || revision.Status != domain.RevisionDraft || binding.ContentHash != revision.ContentHash || binding.TargetPath != document.CanonicalPath {
		return authoringapp.RetireGeneratedPublicationResult{}, generatedPublicationBusy("generated publication no longer has a pending candidate binding")
	}
	reservation, found, err := gormLoadReservation(ctx, tx, request.WorkspaceID, binding.ReservationID, false)
	if err != nil {
		return authoringapp.RetireGeneratedPublicationResult{}, err
	}
	if !found || !reservationMatchesBinding(reservation, binding) || reservation.Status != authoringapp.PublicationReservationClosed || validateReservationShape(reservation) != nil {
		return authoringapp.RetireGeneratedPublicationResult{}, inconsistent("generated retirement reservation does not match its binding")
	}
	at := record.RetiredAt.UTC().Truncate(time.Microsecond)
	if at.Before(binding.UpdatedAt) || at.Before(document.UpdatedAt) {
		return authoringapp.RetireGeneratedPublicationResult{}, generatedInvalid("generated retirement time precedes its candidate")
	}
	if err := retirer.RetireGeneratedProposalScoped(ctx, scope, authoringapp.GeneratedProposalRetirement{
		WorkspaceID: request.WorkspaceID, ProposalID: binding.ProposalID, ProposalRevisionID: binding.ProposalRevisionID,
		ExpectedProposalVersion: request.ExpectedProposalVersion, ProposalIdempotencyKey: reservation.ProposalIdempotencyKey,
		TargetPath: binding.TargetPath, TargetMode: binding.TargetMode, BaseVersion: reservation.BaseVersion,
		ContentHash: binding.ContentHash, RetiredAt: at,
	}); err != nil {
		return authoringapp.RetireGeneratedPublicationResult{}, err
	}
	changed := false
	if err := gormMarkPublication(ctx, tx, binding, domain.PublicationClosed, publicationNeedsRevision, "", nil, at, &changed); err != nil {
		return authoringapp.RetireGeneratedPublicationResult{}, err
	}
	if !changed {
		return authoringapp.RetireGeneratedPublicationResult{}, inconsistent("generated retirement did not close the pending binding")
	}
	receipt = gormGeneratedRetirementRecord{
		WorkspaceID: string(request.WorkspaceID), IdempotencyKey: record.IdempotencyKey, RequestHash: record.RequestHash,
		PublicationID: string(request.PublicationID), DocumentID: string(request.DocumentID), ArticleRevisionID: string(request.ArticleRevisionID),
		OriginKind: string(request.OriginKind), OriginID: string(request.OriginID), OriginRevisionID: string(request.OriginRevisionID), ProjectionHash: request.ProjectionHash,
		ExpectedDocumentVersion: request.ExpectedDocumentVersion, ExpectedProposalVersion: request.ExpectedProposalVersion, CreatedAt: at,
	}
	if err := tx.Create(&receipt).Error; err != nil {
		return authoringapp.RetireGeneratedPublicationResult{}, classifyGenerated(ctx, err, "AUTHORING_GENERATED_RETIREMENT_CREATE_FAILED")
	}
	binding.Status, binding.ErrorCode = domain.PublicationClosed, publicationNeedsRevision
	binding.Version++
	binding.UpdatedAt = at
	return authoringapp.RetireGeneratedPublicationResult{Publication: binding}, nil
}

func (receipt gormGeneratedRetirementRecord) request() domain.GeneratedPublicationRetirementRequest {
	return domain.GeneratedPublicationRetirementRequest{WorkspaceID: foundation.ID(receipt.WorkspaceID), DocumentID: foundation.ID(receipt.DocumentID),
		ArticleRevisionID: foundation.ID(receipt.ArticleRevisionID), PublicationID: foundation.ID(receipt.PublicationID),
		OriginKind: domain.GeneratedOriginKind(receipt.OriginKind), OriginID: foundation.ID(receipt.OriginID), OriginRevisionID: foundation.ID(receipt.OriginRevisionID),
		ProjectionHash: receipt.ProjectionHash, ExpectedDocumentVersion: receipt.ExpectedDocumentVersion, ExpectedProposalVersion: receipt.ExpectedProposalVersion}
}

func nilGeneratedRetirer(retirer authoringapp.GeneratedProposalRetirer) bool {
	if retirer == nil {
		return true
	}
	value := reflect.ValueOf(retirer)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
