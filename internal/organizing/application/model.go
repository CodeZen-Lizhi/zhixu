// Package application coordinates Organizing owner ports without querying other modules' private tables.
package application

import (
	"context"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

const (
	// CommandCreateDraft identifies a Suggested Material Set create receipt.
	CommandCreateDraft = "CREATE_DRAFT"
	// CommandUpdateDraft identifies an intent and Template Revision CAS receipt.
	CommandUpdateDraft = "UPDATE_DRAFT"
	// CommandAddMaterial identifies a material add CAS receipt.
	CommandAddMaterial = "ADD_MATERIAL"
	// CommandRemoveMaterial identifies a material remove CAS receipt.
	CommandRemoveMaterial = "REMOVE_MATERIAL"
	// CommandSetMaterialSelection identifies an explicit material authorization CAS receipt.
	CommandSetMaterialSelection = "SET_MATERIAL_SELECTION"
	// CommandReplaceSuggestions identifies a bounded suggestion replacement receipt.
	CommandReplaceSuggestions = "REPLACE_SUGGESTIONS"
	// CommandConfirmDraft identifies an immutable Snapshot receipt.
	CommandConfirmDraft = "CONFIRM_DRAFT"
	// CommandCreateTemplate identifies a custom template create receipt.
	CommandCreateTemplate = "CREATE_TEMPLATE"
	// CommandCloneTemplate identifies a built-in clone receipt.
	CommandCloneTemplate = "CLONE_TEMPLATE"
	// CommandReviseTemplate identifies an append-only template revision receipt.
	CommandReviseTemplate = "REVISE_TEMPLATE"
	// MaxIdempotencyKeyBytes is the bounded Workspace-scoped command key size.
	MaxIdempotencyKeyBytes = 128
	// DefaultMaterialSearchLimit is the default number of same-page material matches.
	DefaultMaterialSearchLimit = 12
	// MaxMaterialSearchLimit bounds every owner-backed material search.
	MaxMaterialSearchLimit = 25
	// MaxMaterialSearchQueryBytes bounds normalized material search text.
	MaxMaterialSearchQueryBytes = 256
)

// CommandBinding is the canonical identity shared by Organizing command receipts.
type CommandBinding struct {
	WorkspaceID     foundation.ID
	AggregateID     foundation.ID
	IdempotencyKey  string
	RequestHash     string
	CommandType     string
	ExpectedVersion int64
}

// MaterialSelector is the untrusted identity-only request passed to owner resolvers.
type MaterialSelector struct {
	Kind              domain.MaterialKind `json:"kind"`
	SourceVersionID   foundation.ID       `json:"source_version_id,omitempty"`
	DocumentID        foundation.ID       `json:"document_id,omitempty"`
	ArticleRevisionID foundation.ID       `json:"article_revision_id,omitempty"`
	ClaimID           foundation.ID       `json:"claim_id,omitempty"`
	CollectionID      foundation.ID       `json:"collection_id,omitempty"`
}

// MaterialCandidate is one bounded, owner-resolved candidate before Organizing assigns local identity.
type MaterialCandidate struct {
	Reference    domain.MaterialRef
	Title        string
	Reasons      []domain.SuggestionReasonCode
	Origin       domain.MaterialOrigin
	Availability domain.MaterialAvailability
	Score        float64
}

// FrozenMaterialFence revalidates frozen owner facts inside the caller-owned confirmation transaction.
// The transaction remains opaque so the application layer does not depend on pgx.
type FrozenMaterialFence interface {
	VerifyFrozen(context.Context, any, foundation.ID, []domain.MaterialRef) error
}

// MaterialResolver resolves identities in bounded batches and revalidates exact refs for confirmation.
type MaterialResolver interface {
	FrozenMaterialFence
	Resolve(context.Context, foundation.ID, []MaterialSelector) ([]MaterialCandidate, error)
	Freeze(context.Context, foundation.ID, []domain.MaterialRef) ([]domain.MaterialRef, error)
}

// SuggestionQuery contains one bounded Suggested Material Set request.
type SuggestionQuery struct {
	WorkspaceID foundation.ID
	Intent      string
	Limit       int
}

// SuggestionProvider returns explainable, owner-hydrated candidates without persisting authorization.
type SuggestionProvider interface {
	Suggest(context.Context, SuggestionQuery) ([]MaterialCandidate, error)
}

// MaterialSearchQuery identifies one bounded Workspace-scoped search for a single material kind.
type MaterialSearchQuery struct {
	WorkspaceID foundation.ID
	Query       string
	Kind        domain.MaterialKind
	Limit       int
}

// MaterialSearchHit is an identity-only owner result that must be resolved again when added.
type MaterialSearchHit struct {
	Selector     MaterialSelector
	Title        string
	Availability domain.MaterialAvailability
}

// MaterialSearchPage is one complete bounded search response.
type MaterialSearchPage struct {
	WorkspaceID foundation.ID
	Query       string
	Kind        domain.MaterialKind
	Items       []MaterialSearchHit
}

// MaterialSearchProvider searches owner read models without persisting or authorizing a material.
type MaterialSearchProvider interface {
	SearchMaterials(context.Context, MaterialSearchQuery) ([]MaterialSearchHit, error)
}

// CreateDraftCommand creates one recoverable Draft.
type CreateDraftCommand struct {
	WorkspaceID    foundation.ID
	Intent         string
	IdempotencyKey string
}

// UpdateDraftCommand updates the intent and selected Template Revision under expected-version CAS.
type UpdateDraftCommand struct {
	WorkspaceID        foundation.ID
	DraftID            foundation.ID
	ExpectedVersion    int64
	Intent             string
	TemplateRevisionID foundation.ID
	IdempotencyKey     string
}

// AddMaterialCommand resolves and adds exactly one candidate under Draft CAS.
type AddMaterialCommand struct {
	WorkspaceID     foundation.ID
	DraftID         foundation.ID
	ExpectedVersion int64
	Selector        MaterialSelector
	IdempotencyKey  string
}

// RemoveMaterialCommand removes one Draft-owned material under CAS.
type RemoveMaterialCommand struct {
	WorkspaceID     foundation.ID
	DraftID         foundation.ID
	MaterialID      foundation.ID
	ExpectedVersion int64
	IdempotencyKey  string
}

// SetMaterialSelectionCommand explicitly includes or excludes one Draft material under CAS.
type SetMaterialSelectionCommand struct {
	WorkspaceID     foundation.ID
	DraftID         foundation.ID
	MaterialID      foundation.ID
	ExpectedVersion int64
	Selected        bool
	IdempotencyKey  string
}

// SuggestCommand replaces system suggestions while retaining user-added candidates.
type SuggestCommand struct {
	WorkspaceID     foundation.ID
	DraftID         foundation.ID
	ExpectedVersion int64
	Limit           int
	IdempotencyKey  string
}

// ConfirmCommand explicitly authorizes exact selected inputs for one Template Revision.
type ConfirmCommand struct {
	WorkspaceID        foundation.ID
	DraftID            foundation.ID
	ExpectedVersion    int64
	TemplateRevisionID foundation.ID
	IdempotencyKey     string
}

// DraftResult is an exact command response or replay.
type DraftResult struct {
	Draft    domain.Draft `json:"draft"`
	Replayed bool         `json:"replayed"`
}

// ConfirmResult contains the immutable Snapshot and pending Start Outbox identity.
type ConfirmResult struct {
	Draft    domain.Draft    `json:"draft"`
	Snapshot domain.Snapshot `json:"snapshot"`
	OutboxID foundation.ID   `json:"outbox_id"`
	Replayed bool            `json:"replayed"`
}

// CreateDraftRecord contains one version-1 Draft and its receipt binding.
type CreateDraftRecord struct {
	Binding CommandBinding
	Draft   domain.Draft
}

// UpdateDraftRecord contains one intent and Template Revision CAS transition.
type UpdateDraftRecord struct {
	Binding            CommandBinding
	Intent             string
	TemplateRevisionID foundation.ID
	UpdatedAt          time.Time
}

// ReplaceMaterialsRecord contains one full Draft material replacement under CAS.
type ReplaceMaterialsRecord struct {
	Binding   CommandBinding
	Materials []domain.DraftMaterial
	UpdatedAt time.Time
}

// ConfirmRecord atomically writes Draft terminal state, Snapshot materials, receipt and Start Outbox.
type ConfirmRecord struct {
	Binding             CommandBinding
	Fence               FrozenMaterialFence
	SnapshotID          foundation.ID
	SnapshotMaterialIDs []foundation.ID
	OutboxID            foundation.ID
	TemplateID          foundation.ID
	TemplateRevisionID  foundation.ID
	TemplateHash        string
	FrozenMaterials     []domain.MaterialRef
	ConfirmedAt         time.Time
}

// DraftRepository owns mutable Draft and immutable Snapshot transactions.
type DraftRepository interface {
	FindDraftCommand(context.Context, CommandBinding) (DraftResult, bool, error)
	FindConfirmCommand(context.Context, CommandBinding) (ConfirmResult, bool, error)
	CreateDraft(context.Context, CreateDraftRecord) (DraftResult, error)
	GetDraft(context.Context, foundation.ID, foundation.ID) (domain.Draft, error)
	UpdateDraft(context.Context, UpdateDraftRecord) (DraftResult, error)
	ReplaceMaterials(context.Context, ReplaceMaterialsRecord) (DraftResult, error)
	ConfirmDraft(context.Context, ConfirmRecord) (ConfirmResult, error)
	GetSnapshot(context.Context, foundation.ID, foundation.ID) (domain.Snapshot, error)
}

// TemplateDetail binds a stable Template to its current immutable Revision.
type TemplateDetail struct {
	Template domain.Template         `json:"template"`
	Revision domain.TemplateRevision `json:"revision"`
}

// TemplateListQuery is a bounded Workspace view that also includes global built-ins.
type TemplateListQuery struct {
	WorkspaceID foundation.ID
	Kind        domain.TemplateKind
	Limit       int
}

// TemplatePage is a bounded stable template list.
type TemplatePage struct {
	Items []TemplateDetail `json:"items"`
}

// CreateTemplateCommand creates a Workspace custom template Revision 1.
type CreateTemplateCommand struct {
	WorkspaceID    foundation.ID
	Declaration    domain.TemplateDeclaration
	IdempotencyKey string
}

// CloneTemplateCommand clones one readable built-in/current Revision into a Workspace template.
type CloneTemplateCommand struct {
	WorkspaceID      foundation.ID
	SourceTemplateID foundation.ID
	Name             string
	IdempotencyKey   string
}

// ReviseTemplateCommand appends one immutable custom Template Revision under CAS.
type ReviseTemplateCommand struct {
	WorkspaceID     foundation.ID
	TemplateID      foundation.ID
	ExpectedVersion int64
	Declaration     domain.TemplateDeclaration
	IdempotencyKey  string
}

// TemplateResult is an exact template command response or replay.
type TemplateResult struct {
	Detail   TemplateDetail `json:"template"`
	Replayed bool           `json:"replayed"`
}

// CreateTemplateRecord atomically writes Template, Revision 1 and receipt.
type CreateTemplateRecord struct {
	Binding  CommandBinding
	Template domain.Template
	Revision domain.TemplateRevision
}

// ReviseTemplateRecord atomically appends a Revision and moves the custom current pointer by CAS.
type ReviseTemplateRecord struct {
	Binding  CommandBinding
	Template domain.Template
	Revision domain.TemplateRevision
}

// TemplateRepository owns template identities and append-only revisions.
type TemplateRepository interface {
	FindTemplateCommand(context.Context, CommandBinding) (TemplateResult, bool, error)
	CreateTemplate(context.Context, CreateTemplateRecord) (TemplateResult, error)
	ReviseTemplate(context.Context, ReviseTemplateRecord) (TemplateResult, error)
	GetTemplate(context.Context, foundation.ID, foundation.ID) (TemplateDetail, error)
	GetTemplateRevision(context.Context, foundation.ID, foundation.ID) (TemplateDetail, error)
	ListTemplates(context.Context, TemplateListQuery) (TemplatePage, error)
}

// StartOutboxStatus is the durable Workflow-start delivery state.
type StartOutboxStatus string

const (
	// StartPending is due or waiting for retry.
	StartPending StartOutboxStatus = "PENDING"
	// StartStarted has an immutable Run binding.
	StartStarted StartOutboxStatus = "STARTED"
	// StartPoisoned requires manual recovery.
	StartPoisoned StartOutboxStatus = "POISONED"
)

// Valid reports whether status is one of the persisted dispatch states.
func (status StartOutboxStatus) Valid() bool {
	return status == StartPending || status == StartStarted || status == StartPoisoned
}

// RunProjection is the Snapshot-scoped dispatch state with optional immutable Run facts.
type RunProjection struct {
	WorkspaceID   foundation.ID
	SnapshotID    foundation.ID
	Status        StartOutboxStatus
	AttemptCount  int
	LastErrorCode string
	UpdatedAt     time.Time
	Binding       *domain.RunBinding
	Result        *domain.RunResult
}

// Valid checks state shape and all Workspace/Snapshot owner bindings.
func (projection RunProjection) Valid() bool {
	if !validID(projection.WorkspaceID) || !validID(projection.SnapshotID) || !projection.Status.Valid() ||
		projection.AttemptCount < 0 || projection.AttemptCount > 1000 || projection.UpdatedAt.IsZero() ||
		projection.LastErrorCode != strings.TrimSpace(projection.LastErrorCode) || len(projection.LastErrorCode) > 128 ||
		(projection.Status != StartPending && projection.AttemptCount < 1) {
		return false
	}
	if projection.Status != StartStarted {
		if projection.Binding != nil || projection.Result != nil {
			return false
		}
		return projection.Status != StartPoisoned || projection.LastErrorCode != ""
	}
	if projection.Binding == nil || projection.Binding.Validate() != nil ||
		projection.Binding.WorkspaceID != projection.WorkspaceID || projection.Binding.SnapshotID != projection.SnapshotID {
		return false
	}
	if projection.Result == nil {
		return true
	}
	return projection.Result.Validate() == nil && projection.Result.WorkspaceID == projection.WorkspaceID &&
		projection.Result.SnapshotID == projection.SnapshotID && projection.Result.RunBindingID == projection.Binding.ID &&
		projection.Result.WorkflowRunID == projection.Binding.WorkflowRunID
}

// StartOutboxLease is a DB-time fenced claim for one Snapshot start.
type StartOutboxLease struct {
	ID                 foundation.ID
	WorkspaceID        foundation.ID
	SnapshotID         foundation.ID
	TemplateRevisionID foundation.ID
	TemplateKind       domain.TemplateKind
	Owner              string
	AttemptCount       int
	Version            int64
	LeaseUntil         time.Time
}

// CompleteStartRecord closes one outbox lease and inserts/replays the immutable Run binding.
type CompleteStartRecord struct {
	Lease             StartOutboxLease
	BindingID         foundation.ID
	WorkflowRunID     foundation.ID
	DefinitionKey     string
	DefinitionVersion int64
	StartedAt         time.Time
}

// RetryStartRecord releases one lease for a bounded retry.
type RetryStartRecord struct {
	Lease     StartOutboxLease
	ErrorCode string
	Delay     time.Duration
}

// PoisonStartRecord closes one lease for manual recovery.
type PoisonStartRecord struct {
	Lease     StartOutboxLease
	ErrorCode string
}

// BindRunResultRecord appends one exact Workflow result to an existing immutable Run binding.
type BindRunResultRecord struct {
	Result domain.RunResult
}

// StartRepository owns DB-time outbox lease/retry and immutable Run bindings.
type StartRepository interface {
	ClaimStart(context.Context, string, time.Duration) (StartOutboxLease, bool, error)
	CompleteStart(context.Context, CompleteStartRecord) (domain.RunBinding, bool, error)
	RetryStart(context.Context, RetryStartRecord) error
	PoisonStart(context.Context, PoisonStartRecord) error
	GetRunProjection(context.Context, foundation.ID, foundation.ID) (RunProjection, error)
	GetRunBinding(context.Context, foundation.ID, foundation.ID) (domain.RunBinding, error)
	BindRunResult(context.Context, BindRunResultRecord) (domain.RunResult, bool, error)
	GetRunResult(context.Context, foundation.ID, foundation.ID) (domain.RunResult, error)
}
