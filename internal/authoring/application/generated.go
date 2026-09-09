package application

import (
	"context"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// GeneratedRevisionRecord is an internal, immutable generation command.
type GeneratedRevisionRecord struct {
	Request        domain.GeneratedRevisionRequest
	IdempotencyKey string
	RequestHash    string
	CreatedAt      time.Time
}

// GeneratedRevisionResult returns the original snapshots even after later publication.
type GeneratedRevisionResult struct {
	Document domain.Document
	Revision domain.ArticleRevision
	Replayed bool
}

// RetireGeneratedPublicationRecord invalidates exactly one unapproved generated candidate.
type RetireGeneratedPublicationRecord struct {
	Request        domain.GeneratedPublicationRetirementRequest
	IdempotencyKey string
	RequestHash    string
	RetiredAt      time.Time
}

// RetireGeneratedPublicationResult retains the old immutable binding, now CLOSED.
type RetireGeneratedPublicationResult struct {
	Publication domain.PublicationBinding
	Replayed    bool
}

// GeneratedProposalRetirement contains only Authoring-verified persisted Proposal facts.
type GeneratedProposalRetirement struct {
	WorkspaceID             foundation.ID
	ProposalID              foundation.ID
	ProposalRevisionID      foundation.ID
	ExpectedProposalVersion int64
	ProposalIdempotencyKey  string
	TargetPath              string
	TargetMode              domain.ProposalTargetMode
	BaseVersion             string
	ContentHash             string
	RetiredAt               time.Time
}

// GeneratedProposalRetirer is a trusted internal Change Control participant.
// Implementations lock the exact Proposal/Revision and accept only ready_for_review.
type GeneratedProposalRetirer interface {
	RetireGeneratedProposalScoped(context.Context, foundation.TransactionScope, GeneratedProposalRetirement) error
}

// GeneratedRepository participates in the caller's UoW. It never commits the scope.
// The caller writes its semantic projection and reservation before that scope commits.
type GeneratedRepository interface {
	AppendGeneratedRevisionScoped(context.Context, foundation.TransactionScope, GeneratedRevisionRecord) (GeneratedRevisionResult, error)
	RetireGeneratedPublicationScoped(context.Context, foundation.TransactionScope, RetireGeneratedPublicationRecord, GeneratedProposalRetirer) (RetireGeneratedPublicationResult, error)
	ReservePublicationScoped(context.Context, foundation.TransactionScope, ReservePublicationRecord) (PublicationPreparation, error)
}

// GeneratedDocumentState is an exact locked baseline for the generated owner.
// Publication remains Authoring's binding; ProposalVersion is only a retirement CAS.
type GeneratedDocumentState struct {
	Document        domain.Document
	Revision        domain.ArticleRevision
	Publication     *domain.PublicationBinding
	ProposalVersion int64
}

type GeneratedDocumentReader interface {
	ReadGeneratedDocumentScoped(context.Context, foundation.TransactionScope, foundation.ID, foundation.ID, foundation.ID) (GeneratedDocumentState, error)
}
