package application

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// Service exposes Working Draft, Revision freeze and governed publication boundaries.
type Service struct {
	dependencies Dependencies
}

// NewService validates and freezes the explicit authoring dependencies.
func NewService(dependencies Dependencies) (*Service, error) {
	if dependencies.Repository == nil || dependencies.IDs == nil || dependencies.Clock == nil || dependencies.Proposals == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeRepositoryUnavailable, true, errors.New("authoring dependencies are incomplete"))
	}
	if dependencies.Organizing == (OrganizingAvailability{}) {
		dependencies.Organizing = OrganizingAvailability{Reason: "ORGANIZING_NOT_AVAILABLE"}
	}
	if !validOrganizingAvailability(dependencies.Organizing) {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeDraftInvalid, false, errors.New("authoring organizing availability is invalid"))
	}
	return &Service{dependencies: dependencies}, nil
}

// WithOrganizingAvailability returns an immutable service view for the
// composition result without changing the owner service used by Organizing.
func (service *Service) WithOrganizingAvailability(availability OrganizingAvailability) (*Service, error) {
	if service == nil || !validOrganizingAvailability(availability) {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeDraftInvalid, false, errors.New("authoring organizing availability is invalid"))
	}
	dependencies := service.dependencies
	dependencies.Organizing = availability
	return &Service{dependencies: dependencies}, nil
}

// CreateWorkingDraft persists a server-owned empty v1 Working Draft.
func (service *Service) CreateWorkingDraft(ctx context.Context, command CreateCommand) (CreateResult, error) {
	if err := contextError(ctx); err != nil {
		return CreateResult{}, err
	}
	key, err := normalizeIdempotencyKey(command.IdempotencyKey)
	if err != nil {
		return CreateResult{}, err
	}
	requestHash, err := domain.ComputeCreateRequestHash(command.WorkspaceID)
	if err != nil {
		return CreateResult{}, err
	}
	draftID, err := service.dependencies.IDs.New()
	if err != nil {
		return CreateResult{}, err
	}
	now, err := service.now()
	if err != nil {
		return CreateResult{}, err
	}
	draft, err := domain.NewBlankWorkingDraft(draftID, command.WorkspaceID, now)
	if err != nil {
		return CreateResult{}, err
	}
	result, err := service.dependencies.Repository.Create(ctx, CreateRecord{
		Binding: CommandBinding{
			WorkspaceID: command.WorkspaceID, DraftID: draftID, IdempotencyKey: key,
			RequestHash: requestHash, CommandType: CommandCreate,
		},
		Draft: draft,
	})
	if err != nil {
		return CreateResult{}, err
	}
	if err := validateCreateResult(result, command.WorkspaceID); err != nil {
		return CreateResult{}, err
	}
	return result, nil
}

// GetWorkingDraft reads one Draft only through its Workspace composite identity.
func (service *Service) GetWorkingDraft(ctx context.Context, workspaceID, draftID foundation.ID) (domain.WorkingDraft, error) {
	if err := contextError(ctx); err != nil {
		return domain.WorkingDraft{}, err
	}
	draft, err := service.dependencies.Repository.Get(ctx, workspaceID, draftID)
	if err != nil {
		return domain.WorkingDraft{}, err
	}
	if err := draft.Validate(); err != nil || draft.WorkspaceID != workspaceID || draft.ID != draftID {
		return domain.WorkingDraft{}, inconsistent("repository returned a different working draft binding")
	}
	return draft, nil
}

// UpdateWorkingDraft performs one CAS autosave without creating an Article Revision.
func (service *Service) UpdateWorkingDraft(ctx context.Context, command UpdateCommand) (UpdateResult, error) {
	if err := contextError(ctx); err != nil {
		return UpdateResult{}, err
	}
	key, err := normalizeIdempotencyKey(command.IdempotencyKey)
	if err != nil {
		return UpdateResult{}, err
	}
	identity := domain.WorkingDraft{ID: command.DraftID, WorkspaceID: command.WorkspaceID}
	requestHash, err := domain.ComputeUpdateRequestHash(identity, command.ExpectedVersion, command.Title, command.TargetPath, command.Body)
	if err != nil {
		return UpdateResult{}, err
	}
	now, err := service.now()
	if err != nil {
		return UpdateResult{}, err
	}
	result, err := service.dependencies.Repository.Update(ctx, UpdateRecord{
		Binding: CommandBinding{
			WorkspaceID: command.WorkspaceID, DraftID: command.DraftID, IdempotencyKey: key,
			RequestHash: requestHash, CommandType: CommandUpdate, ExpectedVersion: command.ExpectedVersion,
		},
		Title: command.Title, TargetPath: command.TargetPath, Body: command.Body, UpdatedAt: now,
	})
	if err != nil {
		return UpdateResult{}, err
	}
	if err := result.Draft.Validate(); err != nil || result.Draft.WorkspaceID != command.WorkspaceID ||
		result.Draft.ID != command.DraftID || result.Draft.Version != command.ExpectedVersion+1 ||
		result.Draft.Title != command.Title || result.Draft.TargetPath != command.TargetPath || result.Draft.Body != command.Body {
		return UpdateResult{}, inconsistent("repository returned a different autosave result")
	}
	return result, nil
}

// ListWorkingDrafts returns a stable Workspace-scoped keyset page.
func (service *Service) ListWorkingDrafts(ctx context.Context, query ListQuery) (Page, error) {
	if err := contextError(ctx); err != nil {
		return Page{}, err
	}
	page, err := service.dependencies.Repository.List(ctx, query)
	if err != nil {
		return Page{}, err
	}
	for _, draft := range page.Items {
		if err := draft.Validate(); err != nil || draft.WorkspaceID != query.WorkspaceID {
			return Page{}, inconsistent("repository returned a cross-workspace or invalid draft page")
		}
	}
	return page, nil
}

// ListDocumentDrafts returns a stable Workspace-scoped keyset page of formal DRAFT Documents.
func (service *Service) ListDocumentDrafts(ctx context.Context, query DocumentListQuery) (DocumentPage, error) {
	if err := contextError(ctx); err != nil {
		return DocumentPage{}, err
	}
	page, err := service.dependencies.Repository.ListDocuments(ctx, query)
	if err != nil {
		return DocumentPage{}, err
	}
	for _, document := range page.Items {
		if err := document.Validate(); err != nil || document.WorkspaceID != query.WorkspaceID || document.Lifecycle != domain.DocumentDraft {
			return DocumentPage{}, inconsistent("repository returned a cross-workspace or non-draft document page")
		}
	}
	return page, nil
}

// FreezeWorkingDraft atomically binds a Document and appends one immutable Revision.
func (service *Service) FreezeWorkingDraft(ctx context.Context, command FreezeCommand) (FreezeResult, error) {
	if err := contextError(ctx); err != nil {
		return FreezeResult{}, err
	}
	key, err := normalizeIdempotencyKey(command.IdempotencyKey)
	if err != nil {
		return FreezeResult{}, err
	}
	requestHash, err := domain.ComputeFreezeRequestHash(command.WorkspaceID, command.DraftID, command.ExpectedVersion)
	if err != nil {
		return FreezeResult{}, err
	}
	documentID, err := service.dependencies.IDs.New()
	if err != nil {
		return FreezeResult{}, err
	}
	revisionID, err := service.dependencies.IDs.New()
	if err != nil {
		return FreezeResult{}, err
	}
	now, err := service.now()
	if err != nil {
		return FreezeResult{}, err
	}
	result, err := service.dependencies.Repository.Freeze(ctx, FreezeRecord{
		Binding: CommandBinding{
			WorkspaceID: command.WorkspaceID, DraftID: command.DraftID, IdempotencyKey: key,
			RequestHash: requestHash, CommandType: CommandFreeze, ExpectedVersion: command.ExpectedVersion,
		},
		CandidateDocumentID: documentID, RevisionID: revisionID, FrozenAt: now,
	})
	if err != nil {
		return FreezeResult{}, err
	}
	if err := validateFreezeResult(result, command); err != nil {
		return FreezeResult{}, err
	}
	return result, nil
}

// PublishArticleRevision creates or recovers one Change Control Proposal binding.
func (service *Service) PublishArticleRevision(ctx context.Context, command PublishCommand) (PublishResult, error) {
	if err := contextError(ctx); err != nil {
		return PublishResult{}, err
	}
	key, err := normalizeIdempotencyKey(command.IdempotencyKey)
	if err != nil {
		return PublishResult{}, err
	}
	requestHash, err := domain.ComputePublishRequestHash(command.WorkspaceID, command.DocumentID, command.RevisionID)
	if err != nil {
		return PublishResult{}, err
	}
	reservationID, err := service.dependencies.IDs.New()
	if err != nil {
		return PublishResult{}, err
	}
	publicationID, err := service.dependencies.IDs.New()
	if err != nil {
		return PublishResult{}, err
	}
	now, err := service.now()
	if err != nil {
		return PublishResult{}, err
	}
	binding := PublishBinding{
		WorkspaceID: command.WorkspaceID, DocumentID: command.DocumentID, RevisionID: command.RevisionID,
		IdempotencyKey: key, RequestHash: requestHash,
	}
	preparation, err := service.dependencies.Repository.ReservePublication(ctx, ReservePublicationRecord{
		Binding: binding, ReservationID: reservationID, ReservedAt: now,
	})
	if err != nil {
		return PublishResult{}, err
	}
	if preparation.Reservation.Status == PublicationReservationAbandoned {
		if err := validateAbandonedReservation(preparation.Reservation, binding); err != nil {
			return PublishResult{}, err
		}
		return PublishResult{}, abandonedPublicationError(preparation.Reservation.ErrorCode)
	}
	if err := validatePublicationPreparation(preparation, binding); err != nil {
		return PublishResult{}, err
	}
	if preparation.Existing != nil {
		return PublishResult{Publication: *preparation.Existing, Replayed: true}, nil
	}
	proposal, err := service.dependencies.Proposals.CreatePublicationProposal(ctx, PublicationProposal{
		WorkspaceID:    preparation.Reservation.WorkspaceID,
		TargetPath:     preparation.Reservation.TargetPath,
		TargetMode:     preparation.Reservation.TargetMode,
		BaseVersion:    preparation.Reservation.BaseVersion,
		Content:        preparation.Revision.Content,
		ContentHash:    preparation.Reservation.ContentHash,
		CreatedByType:  preparation.Revision.CreatedByType,
		IdempotencyKey: preparation.Reservation.ProposalIdempotencyKey,
	})
	if err != nil {
		if errorCode, abandon := deterministicPreProposalFailure(err); abandon {
			abandoned, abandonErr := service.dependencies.Repository.AbandonPublication(ctx, AbandonPublicationRecord{
				Binding: binding, ReservationID: preparation.Reservation.ID, ErrorCode: errorCode, AbandonedAt: now,
			})
			if abandonErr != nil {
				return PublishResult{}, abandonErr
			}
			if validateErr := validateAbandonedReservation(abandoned, binding); validateErr != nil {
				return PublishResult{}, validateErr
			}
		}
		return PublishResult{}, err
	}
	if !validID(proposal.ProposalID) || !validID(proposal.ProposalRevisionID) {
		return PublishResult{}, inconsistent("proposal creator returned invalid identities")
	}
	if proposal.TargetMode != preparation.Reservation.TargetMode ||
		proposal.BaseVersion != preparation.Reservation.BaseVersion {
		return PublishResult{}, inconsistent("proposal creator returned a different target binding")
	}
	result, err := service.dependencies.Repository.CompletePublication(ctx, CompletePublicationRecord{
		Binding: binding, ReservationID: preparation.Reservation.ID, PublicationID: publicationID,
		ProposalID: proposal.ProposalID, ProposalRevisionID: proposal.ProposalRevisionID, CompletedAt: now,
	})
	if err != nil {
		return PublishResult{}, err
	}
	if err := validateCompletedPublication(result.Publication, binding, preparation.Reservation, proposal); err != nil {
		return PublishResult{}, err
	}
	result.Replayed = result.Replayed || preparation.Replayed || proposal.Replayed
	return result, nil
}

// GetDocumentDetail reconciles immutable Commit facts before returning a Document snapshot.
func (service *Service) GetDocumentDetail(ctx context.Context, workspaceID, documentID foundation.ID) (DocumentDetail, error) {
	if err := contextError(ctx); err != nil {
		return DocumentDetail{}, err
	}
	if err := service.reconcile(ctx, workspaceID, documentID, 10); err != nil {
		return DocumentDetail{}, err
	}
	detail, err := service.dependencies.Repository.GetDocumentDetail(ctx, workspaceID, documentID)
	if err != nil {
		return DocumentDetail{}, err
	}
	if err := validateDocumentDetail(detail, workspaceID, documentID); err != nil {
		return DocumentDetail{}, err
	}
	return detail, nil
}

// GetArticleRevisions 批量读取 Workspace 内精确且不可变的 Article Revision，并保持请求顺序。
func (service *Service) GetArticleRevisions(ctx context.Context, query ArticleRevisionBatchQuery) ([]ArticleRevisionSnapshot, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if err := validateArticleRevisionBatchQuery(query); err != nil {
		return nil, err
	}
	items, err := service.dependencies.Repository.GetArticleRevisions(ctx, query)
	if err != nil {
		return nil, err
	}
	if len(items) != len(query.Items) {
		return nil, inconsistent("repository returned an incomplete article revision batch")
	}
	for index, item := range items {
		identity := query.Items[index]
		if item.Document.Validate() != nil || item.Revision.Validate() != nil ||
			item.Document.WorkspaceID != query.WorkspaceID || item.Revision.WorkspaceID != query.WorkspaceID ||
			item.Document.ID != identity.DocumentID || item.Revision.DocumentID != identity.DocumentID ||
			item.Revision.ID != identity.RevisionID {
			return nil, inconsistent("repository returned an invalid article revision binding")
		}
	}
	return items, nil
}

// SearchArticleRevisions searches matching Documents and returns each latest immutable Revision identity.
func (service *Service) SearchArticleRevisions(ctx context.Context, query ArticleRevisionSearchQuery) ([]ArticleRevisionSearchHit, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	query.Query = strings.TrimSpace(query.Query)
	if !validID(query.WorkspaceID) || query.Query == "" || !utf8.ValidString(query.Query) ||
		len([]byte(query.Query)) > 256 || strings.ContainsAny(query.Query, "\r\n\x00") ||
		query.Limit < 1 || query.Limit > MaxArticleRevisionSearchLimit {
		return nil, invalid(domain.ErrorCodeDraftInvalid, "article revision search request is invalid")
	}
	repository, ok := service.dependencies.Repository.(ArticleRevisionSearchRepository)
	if !ok || repository == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeRepositoryUnavailable, true, errors.New("article revision search is unavailable"))
	}
	items, err := repository.SearchArticleRevisions(ctx, query)
	if err != nil {
		return nil, err
	}
	if items == nil || len(items) > query.Limit {
		return nil, inconsistent("repository returned an invalid article revision search bound")
	}
	seen := make(map[foundation.ID]struct{}, len(items))
	for _, item := range items {
		if item.Document.Validate() != nil || item.Document.WorkspaceID != query.WorkspaceID || item.Document.Lifecycle == domain.DocumentDeleted ||
			!validID(item.RevisionID) || item.RevisionNo < 1 || !lowerHexHash(item.ContentHash) {
			return nil, inconsistent("repository returned an invalid article revision search hit")
		}
		if _, duplicate := seen[item.Document.ID]; duplicate {
			return nil, inconsistent("repository returned duplicate article revision search hits")
		}
		seen[item.Document.ID] = struct{}{}
	}
	return items, nil
}

func lowerHexHash(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

// GetOverview returns bounded, reconciled Authoring workbench facts.
func (service *Service) GetOverview(ctx context.Context, workspaceID foundation.ID) (Overview, error) {
	if err := contextError(ctx); err != nil {
		return Overview{}, err
	}
	if err := service.reconcile(ctx, workspaceID, "", 100); err != nil {
		return Overview{}, err
	}
	overview, err := service.dependencies.Repository.GetOverview(ctx, workspaceID, 30)
	if err != nil {
		return Overview{}, err
	}
	overview.Organizing = service.dependencies.Organizing
	if err := validateOverview(overview, workspaceID); err != nil {
		return Overview{}, err
	}
	return overview, nil
}

// ReconcilePublications advances bindings only from exact proposal_commit facts.
func (service *Service) ReconcilePublications(ctx context.Context, workspaceID foundation.ID, limit int) (int, error) {
	if err := contextError(ctx); err != nil {
		return 0, err
	}
	if limit < 1 || limit > 100 {
		return 0, invalid(domain.ErrorCodePublicationInvalid, "publication reconcile limit is invalid")
	}
	now, err := service.now()
	if err != nil {
		return 0, err
	}
	return service.dependencies.Repository.ReconcilePublications(ctx, ReconcileQuery{WorkspaceID: workspaceID, Limit: limit, Now: now})
}

func validateCreateResult(result CreateResult, workspaceID foundation.ID) error {
	if err := result.Draft.Validate(); err != nil || result.Draft.WorkspaceID != workspaceID ||
		result.Draft.Version != 1 || result.Draft.DocumentID != "" || result.Draft.Title != "" ||
		result.Draft.TargetPath != "" || result.Draft.Body != "" {
		return inconsistent("repository returned an invalid blank working draft")
	}
	return nil
}

func validateArticleRevisionBatchQuery(query ArticleRevisionBatchQuery) error {
	if !validID(query.WorkspaceID) || len(query.Items) == 0 || len(query.Items) > MaxArticleRevisionBatchSize {
		return invalid(domain.ErrorCodeFreezeInvalid, "article revision batch query is invalid")
	}
	seen := make(map[ArticleRevisionIdentity]struct{}, len(query.Items))
	for _, item := range query.Items {
		if !validID(item.DocumentID) || !validID(item.RevisionID) {
			return invalid(domain.ErrorCodeFreezeInvalid, "article revision batch identity is invalid")
		}
		if _, duplicate := seen[item]; duplicate {
			return invalid(domain.ErrorCodeFreezeInvalid, "article revision batch contains duplicates")
		}
		seen[item] = struct{}{}
	}
	return nil
}

func validateFreezeResult(result FreezeResult, command FreezeCommand) error {
	if err := result.Draft.Validate(); err != nil {
		return inconsistent("repository returned an invalid frozen working draft")
	}
	if err := result.Document.Validate(); err != nil {
		return inconsistent("repository returned an invalid document snapshot")
	}
	if err := result.Revision.Validate(); err != nil {
		return inconsistent("repository returned an invalid article revision")
	}
	if result.Draft.WorkspaceID != command.WorkspaceID || result.Draft.ID != command.DraftID ||
		result.Draft.Version != command.ExpectedVersion+1 || result.Draft.DocumentID != result.Document.ID ||
		result.Document.WorkspaceID != command.WorkspaceID || result.Revision.WorkspaceID != command.WorkspaceID ||
		result.Revision.DocumentID != result.Document.ID || result.Revision.Content != result.Draft.Body ||
		result.Revision.ContentHash != domain.ComputeContentHash(result.Draft.Body) ||
		result.Document.Title != result.Draft.Title || result.Document.CanonicalPath != result.Draft.TargetPath {
		return inconsistent("repository returned a mismatched freeze binding")
	}
	return nil
}

func validatePublicationPreparation(preparation PublicationPreparation, binding PublishBinding) error {
	if preparation.Existing != nil {
		return validatePublication(*preparation.Existing, binding)
	}
	reservation := preparation.Reservation
	if !validID(reservation.ID) || reservation.WorkspaceID != binding.WorkspaceID ||
		reservation.DocumentID != binding.DocumentID || reservation.ArticleRevisionID != binding.RevisionID ||
		reservation.IdempotencyKey != binding.IdempotencyKey || reservation.RequestHash != binding.RequestHash ||
		reservation.ProposalIdempotencyKey == "" || reservation.TargetPath == "" ||
		reservation.ContentHash != preparation.Revision.ContentHash || preparation.Revision.ContentHash != domain.ComputeContentHash(preparation.Revision.Content) ||
		preparation.Document.ID != binding.DocumentID || preparation.Document.WorkspaceID != binding.WorkspaceID ||
		preparation.Revision.ID != binding.RevisionID || preparation.Revision.DocumentID != binding.DocumentID ||
		preparation.Revision.WorkspaceID != binding.WorkspaceID || preparation.Document.CanonicalPath != reservation.TargetPath ||
		reservation.Status != PublicationReservationPending || reservation.UpdatedAt.Before(reservation.CreatedAt) ||
		reservation.ClosedAt != nil ||
		preparation.Revision.Status == domain.RevisionPublished || preparation.Revision.Status == domain.RevisionSuperseded ||
		preparation.Revision.Status == domain.RevisionArchived {
		return inconsistent("repository returned an invalid publication preparation")
	}
	if err := preparation.Document.Validate(); err != nil {
		return inconsistent("repository returned an invalid publication document")
	}
	if err := preparation.Revision.Validate(); err != nil {
		return inconsistent("repository returned an invalid publication revision")
	}
	expectedProposalKey, keyErr := domain.ComputeProposalIdempotencyKey(
		reservation.WorkspaceID,
		reservation.DocumentID,
		reservation.ArticleRevisionID,
		reservation.TargetPath,
		reservation.ContentHash,
		reservation.TargetMode,
		reservation.AbsenceToken,
	)
	if keyErr != nil || reservation.ProposalIdempotencyKey != expectedProposalKey {
		return inconsistent("repository returned an invalid publication proposal identity")
	}
	token, tokenErr := domain.ComputeAbsenceToken(binding.WorkspaceID, reservation.TargetPath)
	if reservation.TargetMode == domain.ProposalTargetCreateOnly {
		if tokenErr != nil || reservation.AbsenceToken != token || reservation.BaseVersion != token ||
			preparation.Document.Lifecycle != domain.DocumentDraft || preparation.Document.CurrentPublishedRevisionID != "" {
			return inconsistent("repository returned an invalid create-only publication")
		}
	} else if reservation.TargetMode == domain.ProposalTargetReplace {
		if reservation.AbsenceToken != "" || preparation.Document.Lifecycle != domain.DocumentPublished ||
			preparation.Document.CurrentPublishedRevisionID == "" || len(reservation.BaseVersion) != 64 {
			return inconsistent("repository returned an invalid replace publication")
		}
	} else {
		return inconsistent("repository returned an invalid publication target mode")
	}
	return nil
}

func validateAbandonedReservation(reservation PublicationReservation, binding PublishBinding) error {
	if reservation.Status != PublicationReservationAbandoned || reservation.WorkspaceID != binding.WorkspaceID ||
		reservation.DocumentID != binding.DocumentID || reservation.ArticleRevisionID != binding.RevisionID ||
		reservation.IdempotencyKey != binding.IdempotencyKey || reservation.RequestHash != binding.RequestHash ||
		reservation.ErrorCode != ErrorCodePublicationTargetParentNotFound || reservation.ClosedAt != nil ||
		reservation.AbandonedAt == nil || reservation.AbandonedAt.Before(reservation.CreatedAt) ||
		reservation.UpdatedAt.Before(*reservation.AbandonedAt) {
		return inconsistent("repository returned an invalid abandoned publication reservation")
	}
	return nil
}

func deterministicPreProposalFailure(err error) (string, bool) {
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Retryable || classified.Kind != foundation.ErrorNotFound {
		return "", false
	}
	if classified.Code == ErrorCodePublicationTargetParentNotFound {
		return classified.Code, true
	}
	return "", false
}

func abandonedPublicationError(errorCode string) error {
	return foundation.NewError(foundation.ErrorNotFound, errorCode, false, errors.New("publication target parent is missing"))
}

func validatePublication(publication domain.PublicationBinding, binding PublishBinding) error {
	if err := publication.Validate(); err != nil || publication.WorkspaceID != binding.WorkspaceID ||
		publication.DocumentID != binding.DocumentID || publication.ArticleRevisionID != binding.RevisionID {
		return inconsistent("repository returned an invalid publication binding")
	}
	return nil
}

func validateCompletedPublication(
	publication domain.PublicationBinding,
	binding PublishBinding,
	reservation PublicationReservation,
	proposal PublicationProposalResult,
) error {
	if err := validatePublication(publication, binding); err != nil {
		return err
	}
	if publication.ReservationID != reservation.ID || publication.ProposalID != proposal.ProposalID ||
		publication.ProposalRevisionID != proposal.ProposalRevisionID ||
		publication.TargetPath != reservation.TargetPath || publication.ContentHash != reservation.ContentHash ||
		publication.TargetMode != reservation.TargetMode || publication.AbsenceToken != reservation.AbsenceToken {
		return inconsistent("repository returned a different completed publication binding")
	}
	return nil
}

func validateDocumentDetail(detail DocumentDetail, workspaceID, documentID foundation.ID) error {
	if err := detail.Document.Validate(); err != nil || detail.Document.WorkspaceID != workspaceID || detail.Document.ID != documentID {
		return inconsistent("repository returned an invalid document detail")
	}
	if detail.CurrentRevision != nil {
		if err := detail.CurrentRevision.Validate(); err != nil || detail.CurrentRevision.WorkspaceID != workspaceID ||
			detail.CurrentRevision.DocumentID != documentID {
			return inconsistent("repository returned an invalid current revision")
		}
	}
	if detail.Publication != nil {
		binding := PublishBinding{WorkspaceID: workspaceID, DocumentID: documentID, RevisionID: detail.Publication.ArticleRevisionID}
		if err := validatePublication(*detail.Publication, binding); err != nil {
			return err
		}
	}
	return nil
}

func validateOverview(overview Overview, workspaceID foundation.ID) error {
	if overview.WorkspaceID != workspaceID || !validOrganizingAvailability(overview.Organizing) ||
		len(overview.RecentDrafts) > 100 || len(overview.PendingPublications) > 100 || len(overview.CompletedDocuments) > 100 {
		return inconsistent("repository returned an invalid authoring overview")
	}
	for _, draft := range overview.RecentDrafts {
		if !validID(draft.ID) || draft.WorkspaceID != workspaceID ||
			(draft.DocumentID != "" && !validID(draft.DocumentID)) || !draft.Status.Valid() ||
			draft.Version < 1 || draft.UpdatedAt.IsZero() {
			return inconsistent("repository returned an invalid overview draft")
		}
	}
	for _, publication := range overview.PendingPublications {
		if err := publication.Validate(); err != nil || publication.WorkspaceID != workspaceID ||
			(publication.Status != domain.PublicationPending && publication.Status != domain.PublicationRecoveryRequired) {
			return inconsistent("repository returned an invalid overview publication")
		}
	}
	for _, document := range overview.CompletedDocuments {
		if err := document.Validate(); err != nil || document.WorkspaceID != workspaceID || document.Lifecycle != domain.DocumentPublished {
			return inconsistent("repository returned an invalid overview document")
		}
	}
	return nil
}

func validOrganizingAvailability(availability OrganizingAvailability) bool {
	if availability.Available {
		return availability.Reason == "" && availability.Href == "/authoring/organize"
	}
	return strings.TrimSpace(availability.Reason) != "" && availability.Href == ""
}

func (service *Service) reconcile(ctx context.Context, workspaceID, documentID foundation.ID, limit int) error {
	now, err := service.now()
	if err != nil {
		return err
	}
	_, err = service.dependencies.Repository.ReconcilePublications(ctx, ReconcileQuery{
		WorkspaceID: workspaceID, DocumentID: documentID, Limit: limit, Now: now,
	})
	return err
}

func normalizeIdempotencyKey(value string) (string, error) {
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) ||
		len(value) > MaxIdempotencyKeyBytes || strings.ContainsAny(value, "\r\n\x00") {
		return "", invalid(ErrorCodeIdempotencyKeyInvalid, "authoring command requires a canonical Idempotency-Key")
	}
	return value, nil
}

func (service *Service) now() (time.Time, error) {
	now := service.dependencies.Clock.Now().UTC()
	if now.IsZero() {
		return time.Time{}, inconsistent("authoring clock returned zero time")
	}
	return now, nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return invalid("AUTHORING_CONTEXT_INVALID", "authoring context is nil")
	}
	if err := ctx.Err(); err != nil {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, "AUTHORING_CONTEXT_DONE", false, err)
	}
	return nil
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}
