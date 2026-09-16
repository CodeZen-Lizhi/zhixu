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
	// SnapshotText 是已验证的历史证据，不能作为当前生成输入。
	SnapshotText string
	Reference    domain.SynthesisSourceRef
	Availability domain.MaterialAvailability
	Text         string
}

// SynthesisAnchorBinding 是一次生成中附加到锚点候选的不可变准入证明。
// AllowedSources 仅包含在所记录范围修订下已接受的精确片段，
// 不能根据主题标签或来源标题推断。
type SynthesisAnchorBinding struct {
	AnchorID       foundation.ID               `json:"anchor_id"`
	ScopeVersion   int64                       `json:"scope_version"`
	Scope          domain.AnchorScope          `json:"scope"`
	AllowedSources []domain.SynthesisSourceRef `json:"allowed_sources"`
}

func (binding *SynthesisAnchorBinding) Validate(workspaceID foundation.ID) error {
	if binding == nil {
		return nil
	}
	if !validID(binding.AnchorID) || binding.ScopeVersion < 1 || binding.Scope.Validate() != nil || len(binding.AllowedSources) < 1 || len(binding.AllowedSources) > domain.MaxSynthesisSources {
		return invalid(ErrorCodeSynthesisGenerationInvalid, "synthesis anchor admission binding is invalid")
	}
	seen := make(map[string]bool, len(binding.AllowedSources))
	for _, ref := range binding.AllowedSources {
		if ref.Validate() != nil || ref.Source.WorkspaceID != workspaceID || seenRef(seen, ref) {
			return invalid(ErrorCodeSynthesisGenerationInvalid, "synthesis anchor admission source is invalid")
		}
		seen[refKey(ref)] = true
	}
	return nil
}

func refKey(ref domain.SynthesisSourceRef) string {
	key, _ := ref.IdentityKey()
	return key
}

func seenRef(seen map[string]bool, ref domain.SynthesisSourceRef) bool {
	return seen[refKey(ref)]
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
	PublicationID foundation.ID `json:"publication_id,omitempty"`
	Note          domain.SynthesisNote
	Revision      domain.SynthesisRevision
	Supplements   []SynthesisSourceSupplement `json:"Supplements,omitempty"`
	Anchor        *SynthesisAnchorBinding     `json:"anchor,omitempty"`
}

// SynthesisGenerationInput binds a bounded batch to one durable model attempt.
// The adapter exposes only local Nnnn/Innn/Snnn labels to the Provider, never
// authoritative identities, publication targets or authorization controls.
type SynthesisGenerationInput struct {
	GenerationPromptVersion string                       `json:"generation_prompt_version,omitempty"`
	SemanticPromptVersion   string                       `json:"semantic_prompt_version,omitempty"`
	BodyRefresh             *SynthesisBodyRefreshBinding `json:",omitempty"`
	BodyRefreshRevisions    []domain.SynthesisRevision   `json:",omitempty"`
	Goal                    *SynthesisGoalBinding        `json:",omitempty"`
	ProcessingID            foundation.ID
	SourceEvent             domain.SynthesisSourceReady
	WorkflowRunID           foundation.ID
	NodeRunID               foundation.ID
	NodeAttemptID           foundation.ID
	RequestHash             string
	Notes                   []SynthesisGenerationNote
	Sources                 []SynthesisSourceExcerpt
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
	BodyRefreshRequestID foundation.ID `json:",omitempty"`
	GoalRequestID        foundation.ID `json:",omitempty"`
	ID                   foundation.ID
	SourceEvent          domain.SynthesisSourceReady
	WorkflowRunID        foundation.ID
	ModelRunID           foundation.ID
	RequestHash          string
	Status               SynthesisProcessingStatus
	RevisionIDs          []foundation.ID
	Failure              *domain.SynthesisFailure
	Version              int64
	CreatedAt            time.Time
	UpdatedAt            time.Time
	CompletedAt          *time.Time
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
	RemergeSourceRevisionID foundation.ID
	ID                      foundation.ID
	RevisionNo              int64
	ArticleRevisionID       foundation.ID
	ArticleRevisionNo       int64
	ContentHash             string
	CreatedAt               time.Time
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

// SynthesisOriginalPromptVersion 标识输入契约，不受后续提示修订影响。
// 尤其是正文输入必须保留其正文 Schema。
func SynthesisOriginalPromptVersion(input SynthesisGenerationInput) string {
	if input.BodyRefresh != nil {
		return SynthesisBodyRefreshPromptVersion
	}
	if input.Goal != nil {
		return SynthesisGoalPromptVersion
	}
	for _, note := range input.Notes {
		if note.PublicationID != "" {
			return SynthesisBodyPromptVersion
		}
	}
	for _, note := range input.Notes {
		if note.Anchor != nil {
			return SynthesisAnchoredPromptVersion
		}
	}
	return SynthesisLegacyPromptVersion
}

// SynthesisSourceIdentityGenerationVersion 标识历史来源身份生成提示。
// 正文刷新保留其原有的 v5 生成器。
func SynthesisSourceIdentityGenerationVersion(original string) string {
	switch original {
	case SynthesisLegacyPromptVersion:
		return SynthesisSourceIdentityLegacyPromptVersion
	case SynthesisAnchoredPromptVersion:
		return SynthesisSourceIdentityAnchoredPromptVersion
	case SynthesisGoalPromptVersion:
		return SynthesisSourceIdentityGoalPromptVersion
	case SynthesisBodyPromptVersion:
		return SynthesisSourceIdentityBodyPromptVersion
	default:
		return ""
	}
}

func ValidSynthesisPromptVersions(original, generation, semantic string) bool {
	if semantic == SynthesisFusionExistingSemanticPromptVersion {
		return original == SynthesisAnchoredPromptVersion && generation == SynthesisGenerationFormatFusionAnchoredPromptVersion || original == SynthesisBodyPromptVersion && generation == SynthesisGenerationFormatFusionBodyPromptVersion
	}
	if semantic == SynthesisFusionSemanticPromptVersion {
		return original == SynthesisAnchoredPromptVersion && (generation == SynthesisFusionAnchoredPromptVersion || generation == SynthesisGenerationFormatFusionAnchoredPromptVersion) || original == SynthesisBodyPromptVersion && (generation == SynthesisFusionBodyPromptVersion || generation == SynthesisGenerationFormatFusionBodyPromptVersion)
	}
	if generation == "" && (semantic == "" || semantic == SynthesisSemanticFormatPromptVersion) {
		return true
	}
	if semantic != SynthesisSourceIdentitySemanticPromptVersion {
		return false
	}
	if original == SynthesisBodyRefreshPromptVersion {
		return generation == ""
	}
	expected := SynthesisSourceIdentityGenerationVersion(original)
	return expected != "" && (generation == expected || generation == SynthesisGenerationFormatVersion(original))
}

func SynthesisPromptVersion(stage SynthesisModelStage, input SynthesisGenerationInput) string {
	if stage == SynthesisModelGenerate && input.GenerationPromptVersion != "" {
		return input.GenerationPromptVersion
	}
	if stage == SynthesisModelValidate && input.SemanticPromptVersion != "" {
		return input.SemanticPromptVersion
	}
	return SynthesisOriginalPromptVersion(input)
}

// LatestSynthesisPromptVersions 仅用于准备新的不可变请求。
// 已保存请求始终保留其明确冻结的版本。
func LatestSynthesisPromptVersions(original string, fusion bool) (generation, semantic string) {
	if fusion {
		switch original {
		case SynthesisAnchoredPromptVersion:
			return SynthesisGenerationFormatFusionAnchoredPromptVersion, SynthesisFusionExistingSemanticPromptVersion
		case SynthesisBodyPromptVersion:
			return SynthesisGenerationFormatFusionBodyPromptVersion, SynthesisFusionExistingSemanticPromptVersion
		default:
			return "", SynthesisFusionExistingSemanticPromptVersion // 版本组合无效，拒绝处理。
		}
	}
	return SynthesisGenerationFormatVersion(original), SynthesisSourceIdentitySemanticPromptVersion
}

func ValidSynthesisGenerationPromptContract(input SynthesisGenerationInput) bool {
	if !ValidSynthesisPromptVersions(SynthesisOriginalPromptVersion(input), input.GenerationPromptVersion, input.SemanticPromptVersion) {
		return false
	}
	if !IsSynthesisFusionSemanticVersion(input.SemanticPromptVersion) {
		return true
	}
	return len(input.Notes) == 1 && ValidSynthesisFusionTarget(input.SourceEvent.Fusion, input.Notes[0].Note.ID, input.Notes[0].Anchor)
}

// ValidSynthesisFusionTarget 仅检查冻结的目标和准入身份。
// 范围与来源元组的有效性由输入 Validate 独立检查。
func ValidSynthesisFusionTarget(trigger *domain.SynthesisFusionTrigger, noteID foundation.ID, anchor *SynthesisAnchorBinding) bool {
	if trigger == nil || anchor == nil || trigger.NoteID != noteID || trigger.AnchorID != anchor.AnchorID || trigger.ScopeVersion != anchor.ScopeVersion || len(trigger.AllowedSources) != len(anchor.AllowedSources) {
		return false
	}
	allowed := make(map[domain.SynthesisSourceRef]bool, len(anchor.AllowedSources))
	for _, ref := range anchor.AllowedSources {
		allowed[ref] = true
	}
	for _, ref := range trigger.AllowedSources {
		if !allowed[ref] {
			return false
		}
	}
	return true
}

// SynthesisGenerationFormatVersion 选择明确传输格式的不可变生成器版本。
// 刷新操作保留其独立的原有生成器。
func SynthesisGenerationFormatVersion(original string) string {
	switch original {
	case SynthesisLegacyPromptVersion:
		return SynthesisGenerationFormatLegacyPromptVersion
	case SynthesisAnchoredPromptVersion:
		return SynthesisGenerationFormatAnchoredPromptVersion
	case SynthesisGoalPromptVersion:
		return SynthesisGenerationFormatGoalPromptVersion
	case SynthesisBodyPromptVersion:
		return SynthesisGenerationFormatBodyPromptVersion
	default:
		return ""
	}
}

// IsSynthesisFusionSemanticVersion 标识绑定到一个已准入目标的契约。
func IsSynthesisFusionSemanticVersion(version string) bool {
	return version == SynthesisFusionSemanticPromptVersion || version == SynthesisFusionExistingSemanticPromptVersion
}
