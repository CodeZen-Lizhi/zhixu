package application

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestServiceCreatesUpdatesAndFreezesThroughCanonicalBindings(t *testing.T) {
	now := time.Date(2026, 8, 3, 6, 0, 0, 0, time.UTC)
	repository := &serviceRepository{}
	service, err := NewService(Dependencies{
		Repository: repository, IDs: &serviceIDs{next: 10}, Clock: foundation.FixedClock{Value: now}, Proposals: &serviceProposalCreator{},
	})
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := serviceID(1)
	created, err := service.CreateWorkingDraft(context.Background(), CreateCommand{WorkspaceID: workspaceID, IdempotencyKey: "create-1"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Draft.Version != 1 || repository.create.Binding.CommandType != CommandCreate ||
		repository.create.Binding.IdempotencyKey != "create-1" || len(repository.create.Binding.RequestHash) != 64 {
		t.Fatalf("create result=%#v record=%#v", created, repository.create)
	}
	updated, err := service.UpdateWorkingDraft(context.Background(), UpdateCommand{
		WorkspaceID: workspaceID, DraftID: created.Draft.ID, ExpectedVersion: 1,
		Title: "Java AI", TargetPath: "notes/java-ai.md", Body: "# Java AI", IdempotencyKey: "update-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Draft.Version != 2 || repository.update.Binding.CommandType != CommandUpdate ||
		repository.update.Binding.ExpectedVersion != 1 || len(repository.update.Binding.RequestHash) != 64 {
		t.Fatalf("update result=%#v record=%#v", updated, repository.update)
	}
	frozen, err := service.FreezeWorkingDraft(context.Background(), FreezeCommand{
		WorkspaceID: workspaceID, DraftID: created.Draft.ID, ExpectedVersion: 2, IdempotencyKey: "freeze-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if frozen.Draft.Version != 3 || frozen.Revision.RevisionNo != 1 ||
		frozen.Revision.ContentHash != domain.ComputeContentHash("# Java AI") ||
		repository.freeze.Binding.CommandType != CommandFreeze || len(repository.freeze.Binding.RequestHash) != 64 {
		t.Fatalf("freeze result=%#v record=%#v", frozen, repository.freeze)
	}
}

func TestServiceRejectsNoncanonicalKeyAndRepositoryBindingDrift(t *testing.T) {
	now := time.Date(2026, 8, 3, 7, 0, 0, 0, time.UTC)
	repository := &serviceRepository{corruptCreate: true}
	service, err := NewService(Dependencies{
		Repository: repository, IDs: &serviceIDs{next: 20}, Clock: foundation.FixedClock{Value: now}, Proposals: &serviceProposalCreator{},
	})
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := serviceID(2)
	if _, err := service.CreateWorkingDraft(context.Background(), CreateCommand{WorkspaceID: workspaceID, IdempotencyKey: " key "}); !serviceError(err, foundation.ErrorInvalidInput, ErrorCodeIdempotencyKeyInvalid) {
		t.Fatalf("noncanonical key error=%v", err)
	}
	if _, err := service.CreateWorkingDraft(context.Background(), CreateCommand{WorkspaceID: workspaceID, IdempotencyKey: "create-drift"}); !serviceError(err, foundation.ErrorConsistencyViolation, ErrorCodeResultInvalid) {
		t.Fatalf("corrupt result error=%v", err)
	}
}

func TestOverviewAdvertisesOrganizingOnlyOnAnAvailableServiceView(t *testing.T) {
	now := time.Date(2026, 8, 3, 7, 15, 0, 0, time.UTC)
	workspaceID := serviceID(23)
	repository := &serviceRepository{overview: Overview{
		WorkspaceID: workspaceID, RecentDrafts: []WorkingDraftSummary{},
		PendingPublications: []domain.PublicationBinding{}, CompletedDocuments: []domain.Document{},
	}}
	service, err := NewService(Dependencies{
		Repository: repository, IDs: &serviceIDs{next: 24}, Clock: foundation.FixedClock{Value: now}, Proposals: &serviceProposalCreator{},
	})
	if err != nil {
		t.Fatal(err)
	}
	unavailable, err := service.GetOverview(context.Background(), workspaceID)
	if err != nil || unavailable.Organizing.Available || unavailable.Organizing.Reason != "ORGANIZING_NOT_AVAILABLE" {
		t.Fatalf("unavailable overview=%#v err=%v", unavailable.Organizing, err)
	}
	availableService, err := service.WithOrganizingAvailability(OrganizingAvailability{Available: true, Href: "/authoring/organize"})
	if err != nil {
		t.Fatal(err)
	}
	available, err := availableService.GetOverview(context.Background(), workspaceID)
	if err != nil || !available.Organizing.Available || available.Organizing.Href != "/authoring/organize" || available.Organizing.Reason != "" {
		t.Fatalf("available overview=%#v err=%v", available.Organizing, err)
	}
	reloaded, err := service.GetOverview(context.Background(), workspaceID)
	if err != nil || reloaded.Organizing.Available {
		t.Fatalf("owner service was mutated: overview=%#v err=%v", reloaded.Organizing, err)
	}
	if _, err := service.WithOrganizingAvailability(OrganizingAvailability{Available: true}); !serviceError(err, foundation.ErrorInvalidInput, domain.ErrorCodeDraftInvalid) {
		t.Fatalf("invalid availability error=%v", err)
	}
}

func TestSearchArticleRevisionsCanonicalizesAndRejectsWorkspaceDrift(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 3, 7, 20, 0, 0, time.UTC)
	workspaceID, documentID, revisionID := serviceID(30), serviceID(31), serviceID(32)
	document := domain.Document{ID: documentID, WorkspaceID: workspaceID, CanonicalPath: "notes/recovery.md", Title: "Recovery Design",
		Lifecycle: domain.DocumentDraft, Version: 1, CreatedAt: now, UpdatedAt: now}
	repository := &serviceRepository{searchItems: []ArticleRevisionSearchHit{{
		Document: document, RevisionID: revisionID, RevisionNo: 1, ContentHash: domain.ComputeContentHash("# Recovery"),
	}}}
	service, err := NewService(Dependencies{Repository: repository, IDs: &serviceIDs{next: 33}, Clock: foundation.FixedClock{Value: now}, Proposals: &serviceProposalCreator{}})
	if err != nil {
		t.Fatal(err)
	}
	items, err := service.SearchArticleRevisions(context.Background(), ArticleRevisionSearchQuery{WorkspaceID: workspaceID, Query: "  Recovery  ", Limit: 5})
	if err != nil || len(items) != 1 || items[0].RevisionID != revisionID || repository.searchQuery.Query != "Recovery" || repository.searchQuery.WorkspaceID != workspaceID {
		t.Fatalf("items=%#v query=%#v err=%v", items, repository.searchQuery, err)
	}
	repository.searchItems[0].Document.WorkspaceID = serviceID(99)
	_, err = service.SearchArticleRevisions(context.Background(), ArticleRevisionSearchQuery{WorkspaceID: workspaceID, Query: "Recovery", Limit: 5})
	if !serviceError(err, foundation.ErrorConsistencyViolation, ErrorCodeResultInvalid) {
		t.Fatalf("workspace drift error=%v", err)
	}
}

func TestServiceListsOnlyWorkspaceScopedDocumentDrafts(t *testing.T) {
	now := time.Date(2026, 8, 3, 7, 30, 0, 0, time.UTC)
	workspaceID := serviceID(25)
	document := domain.Document{
		ID: serviceID(26), WorkspaceID: workspaceID, CanonicalPath: "notes/draft.md", Title: "Draft",
		Lifecycle: domain.DocumentDraft, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	repository := &serviceRepository{document: &document}
	service, err := NewService(Dependencies{
		Repository: repository, IDs: &serviceIDs{next: 30}, Clock: foundation.FixedClock{Value: now}, Proposals: &serviceProposalCreator{},
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.ListDocumentDrafts(context.Background(), DocumentListQuery{WorkspaceID: workspaceID, Limit: 30})
	if err != nil || len(page.Items) != 1 || page.Items[0] != document {
		t.Fatalf("document draft page=%#v err=%v", page, err)
	}

	published := document
	published.Lifecycle = domain.DocumentPublished
	published.CurrentPublishedRevisionID = serviceID(27)
	repository.document = &published
	if _, err := service.ListDocumentDrafts(context.Background(), DocumentListQuery{WorkspaceID: workspaceID, Limit: 30}); !serviceError(err, foundation.ErrorConsistencyViolation, ErrorCodeResultInvalid) {
		t.Fatalf("non-draft document page error=%v", err)
	}
}

func TestServicePublishesOnlyExactFrozenProposalBinding(t *testing.T) {
	now := time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC)
	workspaceID, documentID, revisionID := serviceID(30), serviceID(31), serviceID(32)
	proposalID, proposalRevisionID := serviceID(33), serviceID(34)
	content := "# Governed publication\n"
	token, err := domain.ComputeAbsenceToken(workspaceID, "notes/governed.md")
	if err != nil {
		t.Fatal(err)
	}
	proposalKey, err := domain.ComputeProposalIdempotencyKey(
		workspaceID, documentID, revisionID, "notes/governed.md", domain.ComputeContentHash(content),
		domain.ProposalTargetCreateOnly, token,
	)
	if err != nil {
		t.Fatal(err)
	}
	reservation := PublicationReservation{
		ID: serviceID(101), WorkspaceID: workspaceID, DocumentID: documentID, ArticleRevisionID: revisionID,
		IdempotencyKey: "publish-1", ProposalIdempotencyKey: proposalKey,
		TargetPath: "notes/governed.md", ContentHash: domain.ComputeContentHash(content),
		TargetMode: domain.ProposalTargetCreateOnly, BaseVersion: token, AbsenceToken: token,
		Status: PublicationReservationPending, CreatedAt: now, UpdatedAt: now,
	}
	requestHash, err := domain.ComputePublishRequestHash(workspaceID, documentID, revisionID)
	if err != nil {
		t.Fatal(err)
	}
	reservation.RequestHash = requestHash
	document := domain.Document{
		ID: documentID, WorkspaceID: workspaceID, CanonicalPath: reservation.TargetPath, Title: "Governed publication",
		Lifecycle: domain.DocumentDraft, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	revision := domain.ArticleRevision{
		ID: revisionID, WorkspaceID: workspaceID, DocumentID: documentID, RevisionNo: 1,
		Content: content, ContentHash: reservation.ContentHash, Status: domain.RevisionDraft,
		OptimizationMode: "NONE", CreatedByType: "USER", CreatedAt: now,
	}
	proposal := PublicationProposalResult{
		ProposalID: proposalID, ProposalRevisionID: proposalRevisionID,
		TargetMode: domain.ProposalTargetCreateOnly, BaseVersion: token,
	}
	publication := domain.PublicationBinding{
		ID: serviceID(102), ReservationID: reservation.ID, WorkspaceID: workspaceID, DocumentID: documentID,
		ArticleRevisionID: revisionID, ProposalID: proposalID, ProposalRevisionID: proposalRevisionID,
		TargetPath: reservation.TargetPath, ContentHash: reservation.ContentHash,
		TargetMode: reservation.TargetMode, AbsenceToken: reservation.AbsenceToken,
		Status: domain.PublicationPending, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	repository := &serviceRepository{
		publicationPreparation: PublicationPreparation{Reservation: reservation, Document: document, Revision: revision},
		publicationResult:      PublishResult{Publication: publication},
	}
	proposalCreator := &serviceProposalCreator{result: proposal}
	service, err := NewService(Dependencies{
		Repository: repository, IDs: &serviceIDs{next: 100}, Clock: foundation.FixedClock{Value: now}, Proposals: proposalCreator,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.PublishArticleRevision(context.Background(), PublishCommand{
		WorkspaceID: workspaceID, DocumentID: documentID, RevisionID: revisionID, IdempotencyKey: reservation.IdempotencyKey,
	})
	if err != nil || result.Publication != publication || repository.reserve.ReservationID != reservation.ID ||
		repository.complete.PublicationID != publication.ID || proposalCreator.request.Content != content ||
		proposalCreator.request.IdempotencyKey != proposalKey {
		t.Fatalf("publish result=%#v reserve=%#v complete=%#v proposal=%#v err=%v",
			result, repository.reserve, repository.complete, proposalCreator.request, err)
	}

	for name, mutate := range map[string]func(*domain.PublicationBinding){
		"reservation": func(value *domain.PublicationBinding) { value.ReservationID = serviceID(90) },
		"proposal":    func(value *domain.PublicationBinding) { value.ProposalID = serviceID(91) },
		"path":        func(value *domain.PublicationBinding) { value.TargetPath = "notes/other.md" },
		"hash":        func(value *domain.PublicationBinding) { value.ContentHash = domain.ComputeContentHash("other") },
	} {
		t.Run(name, func(t *testing.T) {
			drifted := publication
			mutate(&drifted)
			if err := validateCompletedPublication(drifted, PublishBinding{
				WorkspaceID: workspaceID, DocumentID: documentID, RevisionID: revisionID,
			}, reservation, proposal); !serviceError(err, foundation.ErrorConsistencyViolation, ErrorCodeResultInvalid) {
				t.Fatalf("drift error=%v", err)
			}
		})
	}
}

func TestServiceAbandonsOnlyProvenPreProposalFailure(t *testing.T) {
	now := time.Date(2026, 8, 3, 8, 30, 0, 0, time.UTC)
	workspaceID, documentID, revisionID := serviceID(110), serviceID(111), serviceID(112)
	content := "# Missing parent\n"
	token, err := domain.ComputeAbsenceToken(workspaceID, "missing/article.md")
	if err != nil {
		t.Fatal(err)
	}
	requestHash, err := domain.ComputePublishRequestHash(workspaceID, documentID, revisionID)
	if err != nil {
		t.Fatal(err)
	}
	proposalKey, err := domain.ComputeProposalIdempotencyKey(workspaceID, documentID, revisionID, "missing/article.md", domain.ComputeContentHash(content), domain.ProposalTargetCreateOnly, token)
	if err != nil {
		t.Fatal(err)
	}
	reservation := PublicationReservation{
		ID: serviceID(113), WorkspaceID: workspaceID, DocumentID: documentID, ArticleRevisionID: revisionID,
		IdempotencyKey: "missing-parent", RequestHash: requestHash, ProposalIdempotencyKey: proposalKey,
		TargetPath: "missing/article.md", ContentHash: domain.ComputeContentHash(content),
		TargetMode: domain.ProposalTargetCreateOnly, BaseVersion: token, AbsenceToken: token,
		Status: PublicationReservationPending, CreatedAt: now, UpdatedAt: now,
	}
	preparation := PublicationPreparation{
		Reservation: reservation,
		Document:    domain.Document{ID: documentID, WorkspaceID: workspaceID, Title: "Missing parent", CanonicalPath: reservation.TargetPath, Lifecycle: domain.DocumentDraft, Version: 1, CreatedAt: now, UpdatedAt: now},
		Revision:    domain.ArticleRevision{ID: revisionID, WorkspaceID: workspaceID, DocumentID: documentID, RevisionNo: 1, Content: content, ContentHash: reservation.ContentHash, Status: domain.RevisionDraft, OptimizationMode: "NONE", CreatedByType: "USER", CreatedAt: now},
	}
	repository := &serviceRepository{publicationPreparation: preparation}
	creator := &serviceProposalCreator{err: foundation.NewError(foundation.ErrorNotFound, ErrorCodePublicationTargetParentNotFound, false, errors.New("missing parent"))}
	service, err := NewService(Dependencies{Repository: repository, IDs: &serviceIDs{next: 120}, Clock: foundation.FixedClock{Value: now}, Proposals: creator})
	if err != nil {
		t.Fatal(err)
	}
	command := PublishCommand{WorkspaceID: workspaceID, DocumentID: documentID, RevisionID: revisionID, IdempotencyKey: reservation.IdempotencyKey}
	if _, err := service.PublishArticleRevision(context.Background(), command); !serviceError(err, foundation.ErrorNotFound, ErrorCodePublicationTargetParentNotFound) {
		t.Fatalf("deterministic publish error=%v", err)
	}
	if repository.abandon.ReservationID != reservation.ID || repository.abandon.ErrorCode != ErrorCodePublicationTargetParentNotFound || creator.calls != 1 {
		t.Fatalf("abandon=%#v proposal calls=%d", repository.abandon, creator.calls)
	}

	abandonedAt := now
	repository.publicationPreparation = PublicationPreparation{Reservation: PublicationReservation{
		ID: reservation.ID, WorkspaceID: workspaceID, DocumentID: documentID, ArticleRevisionID: revisionID,
		IdempotencyKey: reservation.IdempotencyKey, RequestHash: requestHash, Status: PublicationReservationAbandoned,
		ErrorCode: ErrorCodePublicationTargetParentNotFound, CreatedAt: now, UpdatedAt: now, AbandonedAt: &abandonedAt,
	}}
	if _, err := service.PublishArticleRevision(context.Background(), command); !serviceError(err, foundation.ErrorNotFound, ErrorCodePublicationTargetParentNotFound) {
		t.Fatalf("abandoned replay error=%v", err)
	}
	if creator.calls != 1 {
		t.Fatalf("abandoned replay called proposal creator %d times", creator.calls)
	}
}

func TestServiceKeepsRetryablePublicationFailurePending(t *testing.T) {
	now := time.Date(2026, 8, 3, 8, 45, 0, 0, time.UTC)
	workspaceID, documentID, revisionID := serviceID(130), serviceID(131), serviceID(132)
	token, err := domain.ComputeAbsenceToken(workspaceID, "notes/retry.md")
	if err != nil {
		t.Fatal(err)
	}
	requestHash, err := domain.ComputePublishRequestHash(workspaceID, documentID, revisionID)
	if err != nil {
		t.Fatal(err)
	}
	content := "# Retry\n"
	proposalKey, err := domain.ComputeProposalIdempotencyKey(workspaceID, documentID, revisionID, "notes/retry.md", domain.ComputeContentHash(content), domain.ProposalTargetCreateOnly, token)
	if err != nil {
		t.Fatal(err)
	}
	reservation := PublicationReservation{
		ID: serviceID(133), WorkspaceID: workspaceID, DocumentID: documentID, ArticleRevisionID: revisionID,
		IdempotencyKey: "retryable", RequestHash: requestHash, ProposalIdempotencyKey: proposalKey,
		TargetPath: "notes/retry.md", ContentHash: domain.ComputeContentHash(content), TargetMode: domain.ProposalTargetCreateOnly,
		BaseVersion: token, AbsenceToken: token, Status: PublicationReservationPending, CreatedAt: now, UpdatedAt: now,
	}
	repository := &serviceRepository{publicationPreparation: PublicationPreparation{
		Reservation: reservation,
		Document:    domain.Document{ID: documentID, WorkspaceID: workspaceID, Title: "Retry", CanonicalPath: reservation.TargetPath, Lifecycle: domain.DocumentDraft, Version: 1, CreatedAt: now, UpdatedAt: now},
		Revision:    domain.ArticleRevision{ID: revisionID, WorkspaceID: workspaceID, DocumentID: documentID, RevisionNo: 1, Content: content, ContentHash: reservation.ContentHash, Status: domain.RevisionDraft, OptimizationMode: "NONE", CreatedByType: "USER", CreatedAt: now},
	}}
	creator := &serviceProposalCreator{err: foundation.NewError(foundation.ErrorDependencyUnavailable, "WRITEBACK_TARGET_PARENT_READ_FAILED", true, errors.New("transient read"))}
	service, err := NewService(Dependencies{Repository: repository, IDs: &serviceIDs{next: 140}, Clock: foundation.FixedClock{Value: now}, Proposals: creator})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.PublishArticleRevision(context.Background(), PublishCommand{WorkspaceID: workspaceID, DocumentID: documentID, RevisionID: revisionID, IdempotencyKey: reservation.IdempotencyKey})
	if !serviceError(err, foundation.ErrorDependencyUnavailable, "WRITEBACK_TARGET_PARENT_READ_FAILED") {
		t.Fatalf("retryable publish error=%v", err)
	}
	if repository.abandon.ReservationID != "" || creator.calls != 1 {
		t.Fatalf("retryable failure abandoned=%#v proposal calls=%d", repository.abandon, creator.calls)
	}
}

type serviceRepository struct {
	draft                  domain.WorkingDraft
	document               *domain.Document
	revision               *domain.ArticleRevision
	create                 CreateRecord
	update                 UpdateRecord
	freeze                 FreezeRecord
	reserve                ReservePublicationRecord
	complete               CompletePublicationRecord
	abandon                AbandonPublicationRecord
	publicationPreparation PublicationPreparation
	publicationResult      PublishResult
	overview               Overview
	searchQuery            ArticleRevisionSearchQuery
	searchItems            []ArticleRevisionSearchHit
	corruptCreate          bool
}

func (repository *serviceRepository) Create(_ context.Context, record CreateRecord) (CreateResult, error) {
	repository.create = record
	repository.draft = record.Draft
	result := CreateResult{Draft: record.Draft}
	if repository.corruptCreate {
		result.Draft.WorkspaceID = serviceID(999)
	}
	return result, nil
}

func (repository *serviceRepository) Get(_ context.Context, workspaceID, draftID foundation.ID) (domain.WorkingDraft, error) {
	if repository.draft.WorkspaceID != workspaceID || repository.draft.ID != draftID {
		return domain.WorkingDraft{}, foundation.NewError(foundation.ErrorNotFound, ErrorCodeNotFound, false, errors.New("missing"))
	}
	return repository.draft, nil
}

func (repository *serviceRepository) Update(_ context.Context, record UpdateRecord) (UpdateResult, error) {
	repository.update = record
	next, err := domain.ApplyUpdate(repository.draft, record.Binding.ExpectedVersion, record.Title, record.TargetPath, record.Body, record.UpdatedAt)
	if err != nil {
		return UpdateResult{}, err
	}
	repository.draft = next
	return UpdateResult{Draft: next}, nil
}

func (repository *serviceRepository) List(_ context.Context, query ListQuery) (Page, error) {
	if repository.draft.WorkspaceID != query.WorkspaceID {
		return Page{Items: []domain.WorkingDraft{}}, nil
	}
	return Page{Items: []domain.WorkingDraft{repository.draft}}, nil
}

func (repository *serviceRepository) ListDocuments(_ context.Context, query DocumentListQuery) (DocumentPage, error) {
	if repository.document == nil || repository.document.WorkspaceID != query.WorkspaceID {
		return DocumentPage{Items: []domain.Document{}}, nil
	}
	return DocumentPage{Items: []domain.Document{*repository.document}}, nil
}

func (repository *serviceRepository) Freeze(_ context.Context, record FreezeRecord) (FreezeResult, error) {
	repository.freeze = record
	var currentDocument *domain.Document
	var parent *domain.ArticleRevision
	documentID := record.CandidateDocumentID
	if repository.document != nil {
		currentDocument = repository.document
		parent = repository.revision
		documentID = repository.document.ID
	}
	next, document, revision, err := domain.PrepareFreeze(
		repository.draft, record.Binding.ExpectedVersion, documentID, record.RevisionID,
		currentDocument, parent, record.FrozenAt,
	)
	if err != nil {
		return FreezeResult{}, err
	}
	repository.draft = next
	repository.document = &document
	repository.revision = &revision
	return FreezeResult{Draft: next, Document: document, Revision: revision}, nil
}

func (repository *serviceRepository) ReservePublication(_ context.Context, record ReservePublicationRecord) (PublicationPreparation, error) {
	repository.reserve = record
	if repository.publicationPreparation.Reservation.ID == "" && repository.publicationPreparation.Existing == nil {
		return PublicationPreparation{}, errors.New("unexpected publication reservation")
	}
	return repository.publicationPreparation, nil
}

func (repository *serviceRepository) CompletePublication(_ context.Context, record CompletePublicationRecord) (PublishResult, error) {
	repository.complete = record
	if repository.publicationResult.Publication.ID == "" {
		return PublishResult{}, errors.New("unexpected publication completion")
	}
	return repository.publicationResult, nil
}

func (repository *serviceRepository) AbandonPublication(_ context.Context, record AbandonPublicationRecord) (PublicationReservation, error) {
	repository.abandon = record
	reservation := repository.publicationPreparation.Reservation
	if reservation.ID == "" {
		return PublicationReservation{}, errors.New("unexpected publication abandonment")
	}
	reservation.Status = PublicationReservationAbandoned
	reservation.ErrorCode = record.ErrorCode
	reservation.ClosedAt = nil
	abandonedAt := record.AbandonedAt
	reservation.AbandonedAt = &abandonedAt
	reservation.UpdatedAt = abandonedAt
	return reservation, nil
}

func (repository *serviceRepository) GetDocumentDetail(context.Context, foundation.ID, foundation.ID) (DocumentDetail, error) {
	return DocumentDetail{}, errors.New("unexpected document detail")
}

func (repository *serviceRepository) GetArticleRevisions(context.Context, ArticleRevisionBatchQuery) ([]ArticleRevisionSnapshot, error) {
	return nil, errors.New("unexpected article revision batch")
}

func (repository *serviceRepository) SearchArticleRevisions(_ context.Context, query ArticleRevisionSearchQuery) ([]ArticleRevisionSearchHit, error) {
	repository.searchQuery = query
	return append([]ArticleRevisionSearchHit(nil), repository.searchItems...), nil
}

func (repository *serviceRepository) GetOverview(context.Context, foundation.ID, int) (Overview, error) {
	if repository.overview.WorkspaceID == "" {
		return Overview{}, errors.New("unexpected overview")
	}
	return repository.overview, nil
}

func (repository *serviceRepository) ReconcilePublications(context.Context, ReconcileQuery) (int, error) {
	return 0, nil
}

type serviceProposalCreator struct {
	request PublicationProposal
	result  PublicationProposalResult
	err     error
	calls   int
}

func (creator *serviceProposalCreator) CreatePublicationProposal(_ context.Context, request PublicationProposal) (PublicationProposalResult, error) {
	creator.calls++
	creator.request = request
	if creator.err != nil {
		return PublicationProposalResult{}, creator.err
	}
	if creator.result.ProposalID == "" {
		return PublicationProposalResult{}, errors.New("unexpected publication proposal")
	}
	return creator.result, nil
}

type serviceIDs struct{ next int }

func (ids *serviceIDs) New() (foundation.ID, error) {
	ids.next++
	return serviceID(ids.next), nil
}

func serviceID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("69100000-0000-4000-8000-%012d", value))
}

func serviceError(err error, kind foundation.ErrorKind, code string) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Kind == kind && classified.Code == code
}
