package application

import (
	"context"
	"time"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

// SynthesisValidatedGenerationFence proves that the exact durable generation
// has passed the runtime's real semantic model validation. A caller-supplied
// boolean cannot replace this transaction-scoped journal check.
type SynthesisValidatedGenerationFence interface {
	VerifyValidatedSynthesisGenerationScoped(context.Context, foundation.TransactionScope, SynthesisGenerationInput, SynthesisGenerationResult) error
}

// SynthesisCandidateIDs are server allocations. Exact replay returns the first
// committed allocation, independently of allocations made by a later delivery.
type SynthesisCandidateIDs struct {
	NoteID            foundation.ID
	DocumentID        foundation.ID
	RevisionID        foundation.ID
	ArticleRevisionID foundation.ID
	ReservationID     foundation.ID
}

type SynthesisApplyRecord struct {
	Input      SynthesisGenerationInput
	Generation SynthesisGenerationResult
	Candidates []SynthesisCandidateIDs
	AppliedAt  time.Time
}

// SynthesisPublicationCommand is a durable Authoring reservation to complete
// after the candidate transaction. It grants no approval or file access.
type SynthesisPublicationCommand struct {
	NoteID            foundation.ID `json:"note_id"`
	RevisionID        foundation.ID `json:"revision_id"`
	DocumentID        foundation.ID `json:"document_id"`
	ArticleRevisionID foundation.ID `json:"article_revision_id"`
	ContentHash       string        `json:"content_hash"`
	IdempotencyKey    string        `json:"idempotency_key"`
}

type SynthesisApplyResult struct {
	ProcessingID foundation.ID                 `json:"processing_id"`
	RevisionIDs  []foundation.ID               `json:"revision_ids"`
	Changed      bool                          `json:"changed"`
	Publications []SynthesisPublicationCommand `json:"publications"`
	Replayed     bool                          `json:"replayed"`
}

// SynthesisStore owns the atomic candidate/ArticleRevision/reservation receipt.
// Source processing and model journals remain the Workflow runtime's facts.
type SynthesisStore interface {
	ApplySynthesisGeneration(context.Context, SynthesisApplyRecord) (SynthesisApplyResult, error)
	LookupSynthesisApplyResult(context.Context, foundation.ID, foundation.ID) (SynthesisApplyResult, bool, error)
	ListSynthesisCandidates(context.Context, foundation.ID) ([]SynthesisGenerationNote, error)
	ListSynthesisNotes(context.Context, SynthesisListQuery) (SynthesisNotePage, error)
	GetSynthesisNote(context.Context, foundation.ID, foundation.ID) (SynthesisNoteDetail, error)
	GetSynthesisRevision(context.Context, foundation.ID, foundation.ID, foundation.ID) (domain.SynthesisRevision, error)
	ListSynthesisRevisions(context.Context, SynthesisRevisionListQuery) (SynthesisRevisionPage, error)
	ReadPublishedSynthesisNote(context.Context, foundation.ID, foundation.ID) (domain.SynthesisNoteSnapshot, error)
}

// SynthesisPublicationPublisher is implemented by the existing Authoring Service.
type SynthesisPublicationPublisher interface {
	PublishArticleRevision(context.Context, authoringapp.PublishCommand) (authoringapp.PublishResult, error)
}

type SynthesisDependencies struct {
	Store        SynthesisStore
	Sources      SynthesisSourceReader
	Publications SynthesisPublicationPublisher
	IDs          foundation.IDGenerator
	Clock        foundation.Clock
}
