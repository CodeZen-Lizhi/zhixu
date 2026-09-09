package domain

import (
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	SynthesisProcessorVersion = "synthesis/v1"
	SynthesisRevisionSchema   = "synthesis-revision/v1"
	SynthesisRendererVersion  = "synthesis-markdown/v1"

	MaxSynthesisItems            = 128
	MaxSynthesisOperations       = 64
	MaxSynthesisSources          = 256
	MaxSynthesisStatementSources = 32
	MaxSynthesisAlternatives     = 4
	MaxSynthesisTextBytes        = 4096
	MaxSynthesisContextBytes     = 2048
	MaxSynthesisTitleBytes       = 512
	MaxSynthesisTopicKeyBytes    = 256
	MaxSynthesisAliases          = 16
	MaxSynthesisMarkdownBytes    = 2 * 1024 * 1024
	MaxSynthesisRevisionNo       = 1<<31 - 1
)

// SynthesisStatus describes preparation of the current candidate, not an
// independent publication lifecycle. Authoring owns the published pointer.
type SynthesisStatus string

const (
	SynthesisQueued                SynthesisStatus = "QUEUED"
	SynthesisGenerating            SynthesisStatus = "GENERATING"
	SynthesisPendingApproval       SynthesisStatus = "PENDING_APPROVAL"
	SynthesisReady                 SynthesisStatus = "READY"
	SynthesisFailed                SynthesisStatus = "FAILED"
	SynthesisConflict              SynthesisStatus = "CONFLICT"
	SynthesisCapabilityUnavailable SynthesisStatus = "CAPABILITY_UNAVAILABLE"
	SynthesisRecoveryRequired      SynthesisStatus = "RECOVERY_REQUIRED"
)

// SynthesisFailure contains only a stable, non-sensitive error and its recovery
// policy. An uncertain Provider or commit result is never automatically retryable.
type SynthesisFailure struct {
	Code      string `json:"code"`
	Retryable bool   `json:"retryable"`
}

// SynthesisNote is a Workspace-owned knowledge point and its Authoring identity.
// CurrentRevisionID may be empty before the first nonempty delta is committed.
type SynthesisNote struct {
	ID                foundation.ID     `json:"id"`
	WorkspaceID       foundation.ID     `json:"workspace_id"`
	DocumentID        foundation.ID     `json:"document_id"`
	TopicKey          string            `json:"topic_key"`
	Title             string            `json:"title"`
	Aliases           []string          `json:"aliases"`
	CurrentRevisionID foundation.ID     `json:"current_revision_id,omitempty"`
	Version           int64             `json:"version"`
	Status            SynthesisStatus   `json:"status"`
	WorkflowRunID     foundation.ID     `json:"workflow_run_id,omitempty"`
	Failure           *SynthesisFailure `json:"failure"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

// SynthesisSourceVersion binds original immutable bytes to one parser
// projection. It deliberately has no formal Claim or active Index identity.
type SynthesisSourceVersion struct {
	WorkspaceID       foundation.ID `json:"workspace_id"`
	SourceID          foundation.ID `json:"source_id"`
	SourceVersionID   foundation.ID `json:"source_version_id"`
	ContentArtifactID foundation.ID `json:"content_artifact_id"`
	ParseProjectionID foundation.ID `json:"parse_projection_id"`
	ContentHash       string        `json:"content_hash"`
}

// SynthesisSourceRef preserves the complete original source tuple. Title is an
// owner-supplied display snapshot; it cannot select or redirect a source.
type SynthesisSourceRef struct {
	Source       SynthesisSourceVersion `json:"source"`
	SourceSpanID foundation.ID          `json:"source_span_id"`
	ExcerptHash  string                 `json:"excerpt_hash"`
	Title        string                 `json:"title"`
}

// SynthesisSourceReady is appended in the chunked + security-passed ingestion
// transaction. Its processing key ignores delivery/attempt IDs and binds the
// source version, projection and processor version instead.
type SynthesisSourceReady struct {
	ID                 foundation.ID          `json:"id"`
	Source             SynthesisSourceVersion `json:"source"`
	IngestionAttemptID foundation.ID          `json:"ingestion_attempt_id"`
	ProcessorVersion   string                 `json:"processor_version"`
	CreatedAt          time.Time              `json:"created_at"`
}

// SynthesisStatement is a source-supported assertion and its stated conditions.
// Empty Applicability means the sources do not specify conditions.
type SynthesisStatement struct {
	Text          string               `json:"text"`
	Applicability string               `json:"applicability"`
	Sources       []SynthesisSourceRef `json:"sources"`
}

// SynthesisConflictContent keeps all supported alternatives without selecting
// a winner. Alternative positions are immutable and are valid support targets.
type SynthesisConflictContent struct {
	Subject      string               `json:"subject"`
	Alternatives []SynthesisStatement `json:"alternatives"`
}

// SynthesisGapContent is explicitly a question, not a supported assertion.
// Sources explain its context. Resolution can only be added with support; the
// original question and context remain available in every later revision.
type SynthesisGapContent struct {
	Question   string               `json:"question"`
	Context    string               `json:"context"`
	Sources    []SynthesisSourceRef `json:"sources"`
	Resolution *SynthesisStatement  `json:"resolution"`
}

type SynthesisItemKind string

const (
	SynthesisFactItem     SynthesisItemKind = "FACT"
	SynthesisConflictItem SynthesisItemKind = "CONFLICT"
	SynthesisGapItem      SynthesisItemKind = "GAP"
)

// SynthesisItem is a strict tagged union. IDs are allocated by the server and
// survive exact duplicates, additional support and gap resolution.
type SynthesisItem struct {
	ID       foundation.ID             `json:"id"`
	Kind     SynthesisItemKind         `json:"kind"`
	Fact     *SynthesisStatement       `json:"fact"`
	Conflict *SynthesisConflictContent `json:"conflict"`
	Gap      *SynthesisGapContent      `json:"gap"`
}

type SynthesisOperationKind string

const (
	SynthesisAddFact     SynthesisOperationKind = "ADD_FACT"
	SynthesisAddSupport  SynthesisOperationKind = "ADD_SUPPORT"
	SynthesisAddConflict SynthesisOperationKind = "ADD_CONFLICT"
	SynthesisAddGap      SynthesisOperationKind = "ADD_GAP"
	SynthesisResolveGap  SynthesisOperationKind = "RESOLVE_GAP"
)

// SynthesisOperation has five closed shapes. ADD_* item operations contain
// only Item. ADD_SUPPORT contains TargetItemID, Sources and (only for a conflict)
// AlternativeIndex. RESOLVE_GAP contains TargetItemID and Resolution. There is
// intentionally no arbitrary replace, delete, path, permission or version field.
type SynthesisOperation struct {
	Kind             SynthesisOperationKind `json:"kind"`
	Item             *SynthesisItem         `json:"item,omitempty"`
	TargetItemID     foundation.ID          `json:"target_item_id,omitempty"`
	AlternativeIndex *int                   `json:"alternative_index,omitempty"`
	Sources          []SynthesisSourceRef   `json:"sources,omitempty"`
	Resolution       *SynthesisStatement    `json:"resolution,omitempty"`
}

// SynthesisDelta contains only server-bound, validated identities. Provider
// responses use request-local labels and must be resolved before constructing it.
type SynthesisDelta struct {
	Operations []SynthesisOperation `json:"operations"`
}

// SynthesisDeltaResult is a pure projection. Changed=false forbids creation of
// another ArticleRevision, SynthesisRevision or Proposal; the processing receipt
// still records successful consumption of that source event.
type SynthesisDeltaResult struct {
	Items   []SynthesisItem `json:"items"`
	Changed bool            `json:"changed"`
}

// SynthesisRevision is an immutable semantic projection of an exact AGENT
// ArticleRevision. It stores no independently editable Markdown or publish flag.
// Hash is the complete projection digest used by the Authoring origin binding.
type SynthesisRevision struct {
	ID                foundation.ID   `json:"id"`
	WorkspaceID       foundation.ID   `json:"workspace_id"`
	NoteID            foundation.ID   `json:"note_id"`
	DocumentID        foundation.ID   `json:"document_id"`
	ArticleRevisionID foundation.ID   `json:"article_revision_id"`
	RevisionNo        int64           `json:"revision_no"`
	ArticleRevisionNo int64           `json:"article_revision_no"`
	ParentRevisionID  foundation.ID   `json:"parent_revision_id,omitempty"`
	Title             string          `json:"title"`
	RendererVersion   string          `json:"renderer_version"`
	ContentHash       string          `json:"content_hash"`
	Hash              string          `json:"hash"`
	Items             []SynthesisItem `json:"items"`
	Delta             SynthesisDelta  `json:"delta"`
	SourceEventID     foundation.ID   `json:"source_event_id"`
	WorkflowRunID     foundation.ID   `json:"workflow_run_id"`
	ModelRunID        foundation.ID   `json:"model_run_id"`
	CreatedAt         time.Time       `json:"created_at"`
}

// SynthesisNoteSnapshot is the immutable material frozen by Interview or other
// consumers. Its reader must first prove this exact revision was published via
// Authoring. The snapshot has no dependency on Interview or formal Claim types.
type SynthesisNoteSnapshot struct {
	WorkspaceID       foundation.ID   `json:"workspace_id"`
	NoteID            foundation.ID   `json:"note_id"`
	RevisionID        foundation.ID   `json:"revision_id"`
	RevisionNo        int64           `json:"revision_no"`
	DocumentID        foundation.ID   `json:"document_id"`
	ArticleRevisionID foundation.ID   `json:"article_revision_id"`
	ArticleRevisionNo int64           `json:"article_revision_no"`
	ContentHash       string          `json:"content_hash"`
	ProjectionHash    string          `json:"projection_hash"`
	Title             string          `json:"title"`
	RendererVersion   string          `json:"renderer_version"`
	Items             []SynthesisItem `json:"items"`
}

// SynthesisLabelledSource is a request-local, owner-verified source binding.
// The label has the canonical form S001..S256 and is never a persisted identity.
type SynthesisLabelledSource struct {
	Label     string             `json:"label"`
	Reference SynthesisSourceRef `json:"reference"`
}
