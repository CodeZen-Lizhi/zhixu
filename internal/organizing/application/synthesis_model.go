package application

import (
	"context"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

const (
	DefaultSynthesisListLimit      = 20
	MaxSynthesisListLimit          = 100
	MaxSynthesisCandidateNotes     = 24
	MaxSynthesisGeneratedNotes     = 8
	MaxSynthesisSourceExcerptBytes = 16 * 1024
	MaxSynthesisSourceInputBytes   = 256 * 1024
	MaxSynthesisModelOutputBytes   = 256 * 1024
)

// SynthesisSourceExcerpt is temporary model input opened and hash-checked by
// the original source owner. Text must not enter queue args, events or logs.
type SynthesisSourceExcerpt struct {
	Reference domain.SynthesisSourceRef
	Text      string
}

// SynthesisSourceView distinguishes an unavailable historical tuple from an
// available exact excerpt. A reader never redirects it to the latest version.
type SynthesisSourceView struct {
	Reference    domain.SynthesisSourceRef
	Availability domain.MaterialAvailability
	Text         string
}

// SynthesisSourceReader excludes derived synthesis Documents using trusted
// provenance and verifies all owner tuples before returning original excerpts.
type SynthesisSourceReader interface {
	ReadSynthesisSource(context.Context, domain.SynthesisSourceVersion) ([]SynthesisSourceExcerpt, error)
	OpenSynthesisSource(context.Context, domain.SynthesisSourceRef) (SynthesisSourceView, error)
}

// SynthesisSourceFence revalidates new references in the same transaction as
// the AGENT ArticleRevision, semantic projection and processing receipt.
type SynthesisSourceFence interface {
	VerifySynthesisSourcesScoped(context.Context, foundation.TransactionScope, foundation.ID, []domain.SynthesisSourceRef) error
}

// SynthesisSourceReadySink appends/replays a bounded, payload-free notification
// inside the successful ingestion transaction. It does not call the model.
type SynthesisSourceReadySink interface {
	AppendSynthesisSourceReadyScoped(context.Context, foundation.TransactionScope, domain.SynthesisSourceReady) error
}

// SynthesisGenerationNote is a current candidate frozen before a model call.
// Existing note titles, keys, aliases and item text are not model-editable.
type SynthesisGenerationNote struct {
	Note     domain.SynthesisNote
	Revision domain.SynthesisRevision
}

// SynthesisGenerationInput binds a bounded batch to one durable model attempt.
// The adapter exposes only local Nnnn/Innn/Snnn labels to the Provider, never
// authoritative identities, publication targets or authorization controls.
type SynthesisGenerationInput struct {
	ProcessingID  foundation.ID
	SourceEvent   domain.SynthesisSourceReady
	WorkflowRunID foundation.ID
	NodeRunID     foundation.ID
	NodeAttemptID foundation.ID
	RequestHash   string
	Notes         []SynthesisGenerationNote
	Sources       []SynthesisSourceExcerpt
}

// SynthesisGeneratedNote is a server-bound delta. Empty NoteID/BaseRevisionID
// means a new topic whose IDs will be assigned by the application. For an
// existing note, all metadata and BaseRevisionID must match the frozen input.
// New Item IDs are allocated by the server after strict Provider decoding.
type SynthesisGeneratedNote struct {
	NoteID         foundation.ID
	BaseRevisionID foundation.ID
	TopicKey       string
	Title          string
	Aliases        []string
	Delta          domain.SynthesisDelta
}

// SynthesisGenerationResult is persisted under the exact attempt binding before
// candidate application. ModelRunID and hashes are supplied by trusted runtime.
type SynthesisGenerationResult struct {
	ModelRunID  foundation.ID
	RequestHash string
	OutputHash  string
	Notes       []SynthesisGeneratedNote
}

// SynthesisGenerator uses the existing StructuredRunner/RecordingChatModel and
// Eino scheduler. The caller must also run the semantic validator before apply.
type SynthesisGenerator interface {
	GenerateSynthesis(context.Context, SynthesisGenerationInput) (SynthesisGenerationResult, error)
}

// SynthesisSemanticValidator verifies each new assertion/alternative/resolution
// is supported by its supplied excerpts, and gap sources are contextual. Tuple
// validity alone is not semantic support; no model-supplied Verified flag exists.
type SynthesisSemanticValidator interface {
	ValidateSynthesisSemantics(context.Context, SynthesisGenerationInput, SynthesisGenerationResult) error
}

type SynthesisProcessingStatus string

const (
	SynthesisProcessingPending          SynthesisProcessingStatus = "PENDING"
	SynthesisProcessingRunning          SynthesisProcessingStatus = "RUNNING"
	SynthesisProcessingSucceeded        SynthesisProcessingStatus = "SUCCEEDED"
	SynthesisProcessingNoChange         SynthesisProcessingStatus = "NO_CHANGE"
	SynthesisProcessingSkipped          SynthesisProcessingStatus = "SKIPPED"
	SynthesisProcessingFailed           SynthesisProcessingStatus = "FAILED"
	SynthesisProcessingRecoveryRequired SynthesisProcessingStatus = "RECOVERY_REQUIRED"
)

// SynthesisProcessing is the source consumption ledger projection. The store
// owns CAS, exact replay and unknown-result recovery; a no-change result still
// has a terminal receipt and must never enqueue another generation on delivery.
type SynthesisProcessing struct {
	ID            foundation.ID
	SourceEvent   domain.SynthesisSourceReady
	WorkflowRunID foundation.ID
	ModelRunID    foundation.ID
	RequestHash   string
	Status        SynthesisProcessingStatus
	RevisionIDs   []foundation.ID
	Failure       *domain.SynthesisFailure
	Version       int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
	CompletedAt   *time.Time
}

// SynthesisPublicationRef is a read projection of the Authoring publication
// binding. It cannot authorize, publish or retire a revision itself.
type SynthesisPublicationRef struct {
	RevisionID         foundation.ID
	ArticleRevisionID  foundation.ID
	ProposalID         foundation.ID
	ProposalRevisionID foundation.ID
	ContentHash        string
}

type SynthesisNoteDetail struct {
	Note              domain.SynthesisNote
	CurrentRevision   *domain.SynthesisRevision
	PublishedRevision *domain.SynthesisRevision
	Publication       *SynthesisPublicationRef
	LatestProcessing  *SynthesisProcessing
}

// SynthesisRevisionSummary omits semantic items and delta from list responses.
type SynthesisRevisionSummary struct {
	ID                foundation.ID
	RevisionNo        int64
	ArticleRevisionID foundation.ID
	ArticleRevisionNo int64
	ContentHash       string
	CreatedAt         time.Time
}

// SynthesisNoteSummary keeps the bounded list independent of note body size.
// PublishedRevision is projected from the Authoring published pointer.
type SynthesisNoteSummary struct {
	Note              domain.SynthesisNote
	CurrentRevision   *SynthesisRevisionSummary
	PublishedRevision *SynthesisRevisionSummary
	Publication       *SynthesisPublicationRef
	ItemCount         int
	ConflictCount     int
	GapCount          int
	OpenGapCount      int
}

// SynthesisListQuery uses Workspace-bound (updated_at,id) keyset pagination.
type SynthesisListQuery struct {
	WorkspaceID foundation.ID
	Limit       int
	BeforeTime  *time.Time
	BeforeID    foundation.ID
}

type SynthesisNotePage struct {
	Items    []SynthesisNoteSummary
	NextTime *time.Time
	NextID   foundation.ID
}

type SynthesisRevisionListQuery struct {
	WorkspaceID      foundation.ID
	NoteID           foundation.ID
	BeforeRevisionNo int64
	Limit            int
}

type SynthesisRevisionPage struct {
	Items                []domain.SynthesisRevision
	NextBeforeRevisionNo int64
}

// RetrySynthesisCommand is an explicit retry of a known failed consumption,
// never a request to regenerate all history or retry an unknown Provider result.
type RetrySynthesisCommand struct {
	WorkspaceID     foundation.ID
	ProcessingID    foundation.ID
	ExpectedVersion int64
	IdempotencyKey  string
}

type RetrySynthesisResult struct {
	Processing SynthesisProcessing
	Replayed   bool
}

// SynthesisNoteSnapshotReader returns only a revision whose exact Authoring
// published identity/hash is proven, and deep-copies the frozen semantic items.
type SynthesisNoteSnapshotReader interface {
	ReadPublishedSynthesisNote(context.Context, foundation.ID, foundation.ID) (domain.SynthesisNoteSnapshot, error)
}
