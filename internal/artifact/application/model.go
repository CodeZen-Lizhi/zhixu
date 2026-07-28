package application

import (
	"context"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	maxIdempotencyKeyBytes = 128
	maxListLimit           = 100
	defaultListLimit       = 50
)

// CommandType is the stable receipt discriminator for Artifact writes.
type CommandType string

const (
	CommandPlan           CommandType = "PLAN"
	CommandSubmitOutline  CommandType = "SUBMIT_OUTLINE"
	CommandApproveOutline CommandType = "APPROVE_OUTLINE"
	CommandStartRevision  CommandType = "START_REVISION"
	CommandRecordSection  CommandType = "RECORD_SECTION"
	CommandApproveDraft   CommandType = "APPROVE_DRAFT"
	CommandExportMarkdown CommandType = "EXPORT_MARKDOWN"
	CommandPublish        CommandType = "PUBLISH"
)

// Dependencies are the explicit dependencies for Artifact command handling.
// Export and publication dependencies are optional until their corresponding
// commands are requested; all other dependencies are required at startup.
type Dependencies struct {
	Repository Repository
	Evidence   CitationVerifier
	Exporter   MarkdownExporter
	Publisher  PublicationCreator
	IDs        foundation.IDGenerator
	Clock      foundation.Clock
}

// CommandService coordinates Artifact state transitions and durable receipts.
type CommandService struct{ dependencies Dependencies }

// QueryService exposes workspace-scoped Artifact reads without write ports.
type QueryService struct{ repository Repository }

// PlanCommand creates an isolated Artifact and its first empty revision.
type PlanCommand struct {
	WorkspaceID     foundation.ID
	Type            string
	Title           string
	ScopeDefinition string
	IdempotencyKey  string
	VisibilityHold  *VisibilityHold
}

// VisibilityHoldOwnerType identifies the workflow that temporarily owns an
// Artifact before it can become visible through public queries.
type VisibilityHoldOwnerType string

const (
	// VisibilityHoldOwnerInterviewComplete binds an Artifact to one Interview
	// completion transaction.
	VisibilityHoldOwnerInterviewComplete VisibilityHoldOwnerType = "INTERVIEW_COMPLETE"
	// VisibilityHoldOwnerLearningPathCreate binds the single Artifact produced
	// from one persisted Review Answer to its path creation reservation.
	VisibilityHoldOwnerLearningPathCreate VisibilityHoldOwnerType = "LEARNING_PATH_CREATE"
)

// VisibilityHoldOwnerRole identifies one Artifact produced by its owner.
type VisibilityHoldOwnerRole string

const (
	// VisibilityHoldRoleReport identifies the Interview report Artifact.
	VisibilityHoldRoleReport VisibilityHoldOwnerRole = "REPORT"
	// VisibilityHoldRolePath identifies one learning-path Artifact.
	VisibilityHoldRolePath VisibilityHoldOwnerRole = "PATH"
)

// VisibilityHold keeps an internally constructed Artifact out of public
// reads until its owning transaction persists the corresponding binding.
type VisibilityHold struct {
	OwnerType VisibilityHoldOwnerType `json:"owner_type"`
	OwnerID   foundation.ID           `json:"owner_id"`
	OwnerRole VisibilityHoldOwnerRole `json:"owner_role"`
	// AttemptDigest 绑定已 Prepare 的 Interview Completion 或 Review Path 内容摘要。
	AttemptDigest string `json:"attempt_digest"`
}

// SubmitOutlinePersistentCommand is the durable form of outline submission.
type SubmitOutlinePersistentCommand struct {
	WorkspaceID     foundation.ID
	ArtifactID      foundation.ID
	ExpectedVersion int64
	Outline         []domain.OutlineSection
	IdempotencyKey  string
}

// RevisionPersistentCommand identifies a versioned Artifact transition.
type RevisionPersistentCommand struct {
	WorkspaceID     foundation.ID
	ArtifactID      foundation.ID
	ExpectedVersion int64
	IdempotencyKey  string
}

// CitationInput is the untrusted retrieval tuple supplied by a caller or an
// Agent. Verified fields are intentionally absent: the server reconstructs
// them through CitationVerifier.
type CitationInput struct {
	IndexVersionID  foundation.ID
	ChunkID         foundation.ID
	SourceVersionID foundation.ID
	SourceSpanID    foundation.ID
}

// SectionInput carries a section body and untrusted citation identities.
type SectionInput struct {
	Key       string
	Title     string
	Content   string
	Citations []CitationInput
	Coverage  domain.Coverage
}

// RecordSectionPersistentCommand creates an immutable section revision.
type RecordSectionPersistentCommand struct {
	RevisionPersistentCommand
	Section  SectionInput
	Creator  domain.CreatorType
	Metadata *domain.GenerationMetadata
}

// ExportRecord is a durable managed Markdown output bound to one revision.
type ExportRecord struct {
	ID              foundation.ID
	WorkspaceID     foundation.ID
	ArtifactID      foundation.ID
	RevisionID      foundation.ID
	ArtifactVersion int64
	RevisionNo      int64
	RevisionHash    string
	OutputPath      string
	OutputHash      string
	OutputSize      int64
	ExportedAt      time.Time
}

// ExportSnapshot is the immutable input given to the managed Markdown writer.
type ExportSnapshot struct {
	ExportID foundation.ID
	State    State
}

// ManagedExport is the writer's result. Its path must be the deterministic,
// workspace-managed path for ExportSnapshot.ExportID and the frozen revision.
type ManagedExport struct {
	OutputPath string
	OutputHash string
	OutputSize int64
}

// MarkdownExporter writes an Artifact snapshot outside the database
// transaction. Implementations must use atomic replacement and mode 0600.
type MarkdownExporter interface {
	Export(context.Context, ExportSnapshot) (ManagedExport, error)
}

// PublicationRecord binds the exact external Proposal created for a revision.
type PublicationRecord struct {
	WorkspaceID     foundation.ID
	ArtifactID      foundation.ID
	RevisionID      foundation.ID
	ArtifactVersion int64
	RevisionNo      int64
	ContentHash     string
	ProposalID      foundation.ID
	IdempotencyKey  string
	CreatedAt       time.Time
}

// PublicationCreator is the only Artifact seam that may ask Change Control to
// create a PUBLISH_ARTIFACT Proposal. It never writes a formal Document.
type PublicationCreator interface {
	CreateArtifactPublication(context.Context, domain.PublicationRequest, string) (foundation.ID, error)
}

// CommandResult is the exact durable response bound to an idempotency receipt.
type CommandResult struct {
	State          State
	CommandVersion int64
	RequestHash    string
	CommandType    CommandType
	Replayed       bool
	Export         *ExportRecord
	Publication    *PublicationRecord
}

// ArtifactCursor is a persistent keyset boundary, not an HTTP cursor token.
type ArtifactCursor struct {
	UpdatedAt time.Time
	ID        foundation.ID
}

// ListQuery returns a stable Workspace-scoped Artifact page.
type ListQuery struct {
	WorkspaceID foundation.ID
	Limit       int
	After       *ArtifactCursor
}

// ArtifactPage is a stable keyset page ordered by updated_at DESC, id DESC.
type ArtifactPage struct {
	Items []State
	Next  *ArtifactCursor
}

// CommandBinding is the validated receipt identity shared by repository writes.
type CommandBinding struct {
	WorkspaceID     foundation.ID
	ArtifactID      foundation.ID
	IdempotencyKey  string
	RequestHash     string
	CommandType     CommandType
	ExpectedVersion int64
}

// CreateRecord atomically persists the first Artifact revision and its receipt.
type CreateRecord struct {
	Binding        CommandBinding
	State          State
	VisibilityHold *VisibilityHold
}

// TransitionRecord atomically persists a CAS transition, optional new revision,
// optional export/publication fact and its receipt.
type TransitionRecord struct {
	Binding           CommandBinding
	CurrentRevisionID foundation.ID
	State             State
	NewRevision       bool
	Export            *ExportRecord
	Publication       *PublicationRecord
}

// Repository is the Artifact persistence and receipt boundary.
type Repository interface {
	FindCommand(context.Context, CommandBinding) (CommandResult, bool, error)
	// ProbeExternalTransition reads the current state while rejecting an existing
	// reservation owned by a different binding. It must not create a reservation.
	ProbeExternalTransition(context.Context, CommandBinding) (State, error)
	ReserveExternalTransition(context.Context, CommandBinding) (State, error)
	Create(context.Context, CreateRecord) (CommandResult, error)
	Transition(context.Context, TransitionRecord) (CommandResult, error)
	// GetCommandState is an internal raw read used only to continue a command
	// against an Artifact that may still have a visibility hold.
	GetCommandState(context.Context, foundation.ID, foundation.ID) (State, error)
	Get(context.Context, foundation.ID, foundation.ID) (State, error)
	List(context.Context, ListQuery) (ArtifactPage, error)
	ListSectionGenerations(context.Context, foundation.ID, foundation.ID) (SectionGenerationSnapshot, error)
	GetExport(context.Context, foundation.ID, foundation.ID, foundation.ID) (ExportRecord, error)
}

// CitationVerifier reconstructs trusted Citation values from immutable server
// evidence. It must reject missing, cross-workspace, stale or ineligible input.
type CitationVerifier interface {
	VerifyCitations(context.Context, foundation.ID, []CitationInput) ([]domain.Citation, error)
}
