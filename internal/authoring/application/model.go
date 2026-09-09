// Package application coordinates Working Draft commands and atomic Article Revision freezes.
package application

import (
	"context"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// CommandCreate identifies an empty Working Draft create receipt.
	CommandCreate = "CREATE"
	// CommandUpdate identifies a Working Draft CAS autosave receipt.
	CommandUpdate = "UPDATE"
	// CommandFreeze identifies a Document/Article Revision freeze receipt.
	CommandFreeze = "FREEZE"
	// CommandPublish identifies a frozen Revision publication receipt.
	CommandPublish = "PUBLISH"
	// MaxIdempotencyKeyBytes is the Workspace-scoped key size limit.
	MaxIdempotencyKeyBytes = 128
	// MaxArticleRevisionBatchSize 限制一次不可变 Article Revision 批读数量。
	MaxArticleRevisionBatchSize = 100
)

// CommandBinding is the canonical identity shared by authoring command receipts.
type CommandBinding struct {
	WorkspaceID     foundation.ID
	DraftID         foundation.ID
	IdempotencyKey  string
	RequestHash     string
	CommandType     string
	ExpectedVersion int64
}

// CreateRecord contains the empty v1 Working Draft and its receipt binding.
type CreateRecord struct {
	Binding CommandBinding
	Draft   domain.WorkingDraft
}

// CreateResult is the exact empty Working Draft response for create replay.
type CreateResult struct {
	Draft    domain.WorkingDraft
	Replayed bool
}

// UpdateRecord contains one CAS autosave and its response-loss receipt.
type UpdateRecord struct {
	Binding    CommandBinding
	Title      string
	TargetPath string
	Body       string
	UpdatedAt  time.Time
}

// UpdateResult is the exact Working Draft version produced by an autosave.
type UpdateResult struct {
	Draft    domain.WorkingDraft
	Replayed bool
}

// FreezeRecord contains generated identities for one atomic explicit version save.
type FreezeRecord struct {
	Binding             CommandBinding
	CandidateDocumentID foundation.ID
	RevisionID          foundation.ID
	FrozenAt            time.Time
}

// FreezeResult contains the exact Document and immutable Article Revision produced by Freeze.
type FreezeResult struct {
	Draft    domain.WorkingDraft
	Document domain.Document
	Revision domain.ArticleRevision
	Replayed bool
}

// PublishBinding is the Workspace-scoped idempotency identity for one frozen Revision.
type PublishBinding struct {
	WorkspaceID    foundation.ID
	DocumentID     foundation.ID
	RevisionID     foundation.ID
	IdempotencyKey string
	RequestHash    string
}

// PublicationReservation is the durable pre-Proposal publication identity.
type PublicationReservation struct {
	ID                     foundation.ID
	WorkspaceID            foundation.ID
	DocumentID             foundation.ID
	ArticleRevisionID      foundation.ID
	IdempotencyKey         string
	RequestHash            string
	ProposalIdempotencyKey string
	TargetPath             string
	ContentHash            string
	TargetMode             domain.ProposalTargetMode
	BaseVersion            string
	AbsenceToken           string
	Status                 PublicationReservationStatus
	ErrorCode              string
	CreatedAt              time.Time
	UpdatedAt              time.Time
	ClosedAt               *time.Time
	AbandonedAt            *time.Time
}

// PublicationReservationStatus tracks the durable Proposal creation window.
type PublicationReservationStatus string

const (
	// PublicationReservationPending still permits exact Proposal completion.
	PublicationReservationPending PublicationReservationStatus = "PENDING"
	// PublicationReservationClosed has one immutable publication binding.
	PublicationReservationClosed PublicationReservationStatus = "CLOSED"
	// PublicationReservationAbandoned is a deterministic pre-Proposal failure.
	PublicationReservationAbandoned PublicationReservationStatus = "ABANDONED"
)

// ReservePublicationRecord allocates one durable cross-transaction reservation.
type ReservePublicationRecord struct {
	Binding       PublishBinding
	ReservationID foundation.ID
	ReservedAt    time.Time
}

// AbandonPublicationRecord closes a proven pre-Proposal failure without a binding.
type AbandonPublicationRecord struct {
	Binding       PublishBinding
	ReservationID foundation.ID
	ErrorCode     string
	AbandonedAt   time.Time
}

// PublicationPreparation contains only server-owned facts used to create a Proposal.
type PublicationPreparation struct {
	Reservation PublicationReservation
	Document    domain.Document
	Revision    domain.ArticleRevision
	Existing    *domain.PublicationBinding
	Replayed    bool
}

// PublicationProposal is the narrow Authoring-to-Change-Control command.
type PublicationProposal struct {
	WorkspaceID    foundation.ID
	TargetPath     string
	TargetMode     domain.ProposalTargetMode
	BaseVersion    string
	Content        string
	ContentHash    string
	CreatedByType  string
	IdempotencyKey string
}

// PublicationProposalResult identifies the immutable Proposal revision.
type PublicationProposalResult struct {
	ProposalID         foundation.ID
	ProposalRevisionID foundation.ID
	TargetMode         domain.ProposalTargetMode
	BaseVersion        string
	Replayed           bool
}

// ProposalCreator owns Change Control Proposal creation and target-base verification.
type ProposalCreator interface {
	CreatePublicationProposal(context.Context, PublicationProposal) (PublicationProposalResult, error)
}

// CompletePublicationRecord closes one reservation around the exact Proposal result.
type CompletePublicationRecord struct {
	Binding            PublishBinding
	ReservationID      foundation.ID
	PublicationID      foundation.ID
	ProposalID         foundation.ID
	ProposalRevisionID foundation.ID
	CompletedAt        time.Time
}

// PublishResult is the immutable publication binding returned to HTTP.
type PublishResult struct {
	Publication domain.PublicationBinding
	Replayed    bool
}

// DocumentDetail is the latest frozen Revision and publication state for one Document.
type DocumentDetail struct {
	Document        domain.Document
	CurrentRevision *domain.ArticleRevision
	Publication     *domain.PublicationBinding
}

// ArticleRevisionIdentity 精确标识一个 Document 下的不可变 Revision。
type ArticleRevisionIdentity struct {
	DocumentID foundation.ID
	RevisionID foundation.ID
}

// ArticleRevisionBatchQuery 是 Workspace 内有界、保序的 Revision 批读请求。
type ArticleRevisionBatchQuery struct {
	WorkspaceID foundation.ID
	Items       []ArticleRevisionIdentity
}

// ArticleRevisionSnapshot 返回 Revision 及其当前 Document 生命周期与标题。
type ArticleRevisionSnapshot struct {
	Document domain.Document
	Revision domain.ArticleRevision
}

// MaxArticleRevisionSearchLimit bounds same-page searches used by downstream owners.
const MaxArticleRevisionSearchLimit = 25

// ArticleRevisionSearchQuery searches the latest immutable Revision of matching Documents.
type ArticleRevisionSearchQuery struct {
	WorkspaceID foundation.ID
	Query       string
	Limit       int
}

// ArticleRevisionSearchHit keeps only the exact immutable identity required for later resolution.
type ArticleRevisionSearchHit struct {
	Document    domain.Document
	RevisionID  foundation.ID
	RevisionNo  int
	ContentHash string
}

// ArticleRevisionSearchRepository is the optional bounded Document search capability.
type ArticleRevisionSearchRepository interface {
	SearchArticleRevisions(context.Context, ArticleRevisionSearchQuery) ([]ArticleRevisionSearchHit, error)
}

// WorkingDraftSummary is the body-free projection used by the overview.
type WorkingDraftSummary struct {
	ID          foundation.ID
	WorkspaceID foundation.ID
	DocumentID  foundation.ID
	Title       string
	TargetPath  string
	Status      domain.WorkingDraftStatus
	Version     int64
	UpdatedAt   time.Time
}

// OrganizingAvailability prevents the overview from advertising an unavailable workflow.
type OrganizingAvailability struct {
	Available bool
	Reason    string
	Href      string
}

// Overview is the bounded Authoring workbench projection.
type Overview struct {
	WorkspaceID         foundation.ID
	Organizing          OrganizingAvailability
	RecentDrafts        []WorkingDraftSummary
	PendingPublications []domain.PublicationBinding
	CompletedDocuments  []domain.Document
}

// ReconcileQuery bounds exact proposal_commit reconciliation.
type ReconcileQuery struct {
	WorkspaceID        foundation.ID
	DocumentID         foundation.ID
	ProposalID         foundation.ID
	ProposalRevisionID foundation.ID
	Limit              int
	Now                time.Time
}

// RestoreWritebackCheck binds the last Document-owner preflight performed
// immediately before Safe Writeback first touches the target file.
type RestoreWritebackCheck struct {
	WorkspaceID             foundation.ID
	DocumentID              foundation.ID
	ExpectedDocumentVersion int64
	TargetPath              string
	CurrentContentHash      string
}

// RestorePublicationRecord is the immutable proposal_commit fact used to append
// one new published Article Revision after a restore Git commit succeeds.
type RestorePublicationRecord struct {
	WorkspaceID        foundation.ID
	ProposalID         foundation.ID
	ProposalRevisionID foundation.ID
	WritebackID        foundation.ID
	GitCommit          string
	ResultHash         string
	PublishedAt        time.Time
}

// Cursor is the stable Working Draft list boundary ordered by update time and ID.
type Cursor struct {
	UpdatedAt time.Time
	ID        foundation.ID
}

// ListQuery defines one bounded Workspace-scoped Working Draft page.
type ListQuery struct {
	WorkspaceID foundation.ID
	Status      domain.WorkingDraftStatus
	After       *Cursor
	Limit       int
}

// Page is a stable keyset page of Working Drafts.
type Page struct {
	Items []domain.WorkingDraft
	Next  *Cursor
}

// DocumentCursor is the stable Document Draft list boundary ordered by update time and ID.
type DocumentCursor struct {
	UpdatedAt time.Time
	ID        foundation.ID
}

// DocumentListQuery defines one bounded Workspace-scoped page of formal Document Drafts.
type DocumentListQuery struct {
	WorkspaceID foundation.ID
	After       *DocumentCursor
	Limit       int
}

// DocumentPage is a stable keyset page of formal Document Drafts.
type DocumentPage struct {
	Items []domain.Document
	Next  *DocumentCursor
}

// Repository owns Working Draft CAS, command receipts and atomic Revision Freeze transactions.
type Repository interface {
	Create(context.Context, CreateRecord) (CreateResult, error)
	Get(context.Context, foundation.ID, foundation.ID) (domain.WorkingDraft, error)
	Update(context.Context, UpdateRecord) (UpdateResult, error)
	List(context.Context, ListQuery) (Page, error)
	ListDocuments(context.Context, DocumentListQuery) (DocumentPage, error)
	Freeze(context.Context, FreezeRecord) (FreezeResult, error)
	ReservePublication(context.Context, ReservePublicationRecord) (PublicationPreparation, error)
	AbandonPublication(context.Context, AbandonPublicationRecord) (PublicationReservation, error)
	CompletePublication(context.Context, CompletePublicationRecord) (PublishResult, error)
	GetDocumentDetail(context.Context, foundation.ID, foundation.ID) (DocumentDetail, error)
	GetArticleRevisions(context.Context, ArticleRevisionBatchQuery) ([]ArticleRevisionSnapshot, error)
	GetOverview(context.Context, foundation.ID, int) (Overview, error)
	ReconcilePublications(context.Context, ReconcileQuery) (int, error)
}

// Dependencies are the explicit ports required by the authoring service.
type Dependencies struct {
	Repository Repository
	IDs        foundation.IDGenerator
	Clock      foundation.Clock
	Proposals  ProposalCreator
	Organizing OrganizingAvailability
}

// CreateCommand creates a server-persisted empty Working Draft.
type CreateCommand struct {
	WorkspaceID    foundation.ID
	IdempotencyKey string
}

// UpdateCommand autosaves exact Markdown state with optimistic concurrency.
type UpdateCommand struct {
	WorkspaceID     foundation.ID
	DraftID         foundation.ID
	ExpectedVersion int64
	Title           string
	TargetPath      string
	Body            string
	IdempotencyKey  string
}

// FreezeCommand explicitly appends one immutable Article Revision.
type FreezeCommand struct {
	WorkspaceID     foundation.ID
	DraftID         foundation.ID
	ExpectedVersion int64
	IdempotencyKey  string
}

// PublishCommand creates or recovers a Proposal for one frozen Revision.
type PublishCommand struct {
	WorkspaceID    foundation.ID
	DocumentID     foundation.ID
	RevisionID     foundation.ID
	IdempotencyKey string
}
