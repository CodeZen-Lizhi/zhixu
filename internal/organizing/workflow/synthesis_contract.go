package workflow

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

const (
	SynthesisDefinitionKey                       = "organizing.synthesis-note"
	SynthesisDefinitionVersion             int64 = 1
	SynthesisInputSchemaVersion                  = 1
	SynthesisOutputSchemaVersion                 = 1
	SynthesisPrepareNodeKind                     = "organizing.synthesis.prepare"
	SynthesisGenerateNodeKind                    = "organizing.synthesis.generate"
	SynthesisValidateNodeKind                    = "organizing.synthesis.validate"
	SynthesisApplyNodeKind                       = "organizing.synthesis.apply"
	SynthesisMaxAttempts                         = 10
	SynthesisMaxExecutionAge                     = 30 * time.Minute
	ErrorCodeSynthesisExecutionInvalid           = "SYNTHESIS_EXECUTION_INVALID"
	ErrorCodeSynthesisExecutionUnavailable       = "SYNTHESIS_EXECUTION_UNAVAILABLE"
	ErrorCodeSynthesisExecutionConflict          = "SYNTHESIS_EXECUTION_CONFLICT"
	ErrorCodeSynthesisExecutionBudget            = "SYNTHESIS_EXECUTION_BUDGET_EXHAUSTED"
	ErrorCodeSynthesisProcessingNotFound         = "SYNTHESIS_PROCESSING_NOT_FOUND"
	ErrorCodeSynthesisRetryUnsafe                = "SYNTHESIS_RETRY_UNSAFE"
	ErrorCodeSynthesisInputStale                 = "SYNTHESIS_INPUT_STALE"
)

// SynthesisRegisteredDefinitions is additive: historical manual Organizing
// definitions are unchanged. Model attempts never use Workflow automatic retry;
// a user may explicitly retry a known failure, but never an uncertain call.
func SynthesisRegisteredDefinitions() []workflowdomain.RegisteredDefinition {
	read := []workflowdomain.Permission{workflowdomain.PermissionReadLocal}
	node := func(kind string, previous string, write bool, retry workflowdomain.RetryPolicy) workflowdomain.NodeDefinition {
		var dependencies []string
		if previous != "" {
			dependencies = []string{previous}
		}
		permissions := append([]workflowdomain.Permission(nil), read...)
		if write {
			permissions = append(permissions, workflowdomain.PermissionWriteProposal)
		}
		sort.Slice(permissions, func(i, j int) bool { return permissions[i] < permissions[j] })
		return workflowdomain.NodeDefinition{Key: kind, Kind: kind, Dependencies: dependencies,
			InputSchemaVersion: SynthesisInputSchemaVersion, OutputSchemaVersion: SynthesisOutputSchemaVersion,
			RequiredPermissions: permissions, RetryPolicy: retry}
	}
	readRetry := workflowdomain.RetryPolicy{MaxRetries: 3, BaseDelay: time.Second, MaxDelay: 10 * time.Second}
	// Publication retries recover an immutable reservation; they never retry a
	// Provider call or authorize a file write. The execution deadline also bounds them.
	applyRetry := workflowdomain.RetryPolicy{MaxRetries: 30, BaseDelay: time.Second, MaxDelay: 30 * time.Second}
	nodes := []workflowdomain.NodeDefinition{
		node(SynthesisPrepareNodeKind, "", false, readRetry),
		node(SynthesisGenerateNodeKind, SynthesisPrepareNodeKind, false, workflowdomain.RetryPolicy{}),
		node(SynthesisValidateNodeKind, SynthesisGenerateNodeKind, false, workflowdomain.RetryPolicy{}),
		node(SynthesisApplyNodeKind, SynthesisValidateNodeKind, true, applyRetry),
	}
	// Keep this fixed graph canonical even before registry construction. The
	// transaction fence is assembled first to break the terminal-hook cycle.
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Key < nodes[j].Key })
	legacy := workflowdomain.RegisteredDefinition{Key: SynthesisDefinitionKey, Version: SynthesisDefinitionVersion,
		InputSchemaVersion: SynthesisInputSchemaVersion, Graph: workflowdomain.CanonicalGraph{Nodes: nodes}}
	manuscriptNodes := append([]workflowdomain.NodeDefinition(nil), nodes...)
	for i := range manuscriptNodes {
		if manuscriptNodes[i].Kind == SynthesisApplyNodeKind {
			manuscriptNodes[i].Dependencies = []string{SynthesisMergeReviewNodeKind}
		}
	}
	manuscriptNodes = append(manuscriptNodes, node(SynthesisMergeReviewNodeKind, SynthesisValidateNodeKind, false, readRetry))
	sort.Slice(manuscriptNodes, func(i, j int) bool { return manuscriptNodes[i].Key < manuscriptNodes[j].Key })
	return []workflowdomain.RegisteredDefinition{legacy, {Key: SynthesisDefinitionKey, Version: SynthesisManuscriptDefinitionVersion, InputSchemaVersion: SynthesisInputSchemaVersion, Graph: workflowdomain.CanonicalGraph{Nodes: manuscriptNodes}}}
}

func SynthesisExecutorNodeKinds() []string {
	return []string{SynthesisPrepareNodeKind, SynthesisGenerateNodeKind, SynthesisValidateNodeKind, SynthesisMergeReviewNodeKind, SynthesisApplyNodeKind}
}

// SynthesisStartInput is the complete queue-safe input. Recovery only restores
// an already committed application receipt; it does not generate from old text.
type SynthesisStartInput struct {
	BodyRefreshRequestID foundation.ID `json:"body_refresh_request_id,omitempty"`
	GoalRequestID        foundation.ID `json:"goal_request_id,omitempty"`
	ProcessingID         foundation.ID `json:"processing_id"`
	ExecutionNo          int           `json:"execution_no"`
	ApplyRecovery        bool          `json:"apply_recovery"`
}

// SynthesisFrozenNote keeps only the candidate identity and frozen metadata.
// Item text remains in the owner's immutable revision and is reopened by ID.
type SynthesisFrozenNote struct {
	PublicationID foundation.ID                             `json:"publication_id,omitempty"`
	Note          organizingdomain.SynthesisNote            `json:"note"`
	RevisionID    foundation.ID                             `json:"revision_id"`
	RevisionHash  string                                    `json:"revision_hash"`
	Supplements   []organizingapp.SynthesisSourceSupplement `json:"supplements,omitempty"`
	Anchor        *organizingapp.SynthesisAnchorBinding     `json:"anchor,omitempty"`
}

// SynthesisFrozenInput contains no source excerpts. Its hash binds the exact
// source catalogue and immutable candidate revisions across Worker restarts.
type SynthesisFrozenInput struct {
	GenerationPromptVersion string                                     `json:"generation_prompt_version,omitempty"`
	SemanticPromptVersion   string                                     `json:"semantic_prompt_version,omitempty"`
	BodyRefresh             *organizingapp.SynthesisBodyRefreshBinding `json:"body_refresh,omitempty"`
	Goal                    *organizingapp.SynthesisGoalBinding        `json:"goal,omitempty"`
	ProcessingID            foundation.ID                              `json:"processing_id"`
	WorkflowRunID           foundation.ID                              `json:"workflow_run_id"`
	SourceEvent             organizingdomain.SynthesisSourceReady      `json:"source_event"`
	Notes                   []SynthesisFrozenNote                      `json:"notes"`
	Sources                 []organizingdomain.SynthesisSourceRef      `json:"sources"`
	RequestHash             string                                     `json:"request_hash"`
}

// SynthesisExecution is reloaded for every delivered node. Model steps own the
// exact accepted results; Workflow outputs carry only their receipt identifiers.
type SynthesisExecution struct {
	// HumanWaitDuration 从真实已完成的合并 HumanTask 读取。
	HumanWaitDuration    time.Duration
	BodyRefreshRequestID foundation.ID
	GoalRequestID        foundation.ID
	Processing           organizingapp.SynthesisProcessing
	WorkflowRunID        foundation.ID
	ExecutionNo          int
	ApplyRecovery        bool
	Input                *SynthesisFrozenInput
	Generation           *organizingapp.SynthesisModelStepRecord
	Semantic             *organizingapp.SynthesisModelStepRecord
	Applied              *organizingapp.SynthesisApplyResult
	CreatedAt            time.Time
}

type SynthesisCreateProcessing struct {
	Processing organizingapp.SynthesisProcessing
	Start      workflowapp.RuntimeStartResult
}

type SynthesisRetryRecord struct {
	Command       organizingapp.RetrySynthesisCommand
	RequestHash   string
	Start         workflowapp.RuntimeStartResult
	ApplyRecovery bool
	CreatedAt     time.Time
}

// SynthesisProcessingStore owns only source/execution facts; Source readiness
// stays in the existing Workflow outbox and River remains the only job queue.
type SynthesisProcessingStore interface {
	FindSynthesisProcessingScoped(context.Context, foundation.TransactionScope, organizingdomain.SynthesisSourceReady) (organizingapp.SynthesisProcessing, bool, error)
	HasActiveSynthesisProcessingScoped(context.Context, foundation.TransactionScope, foundation.ID) (bool, error)
	CreateSynthesisProcessingScoped(context.Context, foundation.TransactionScope, SynthesisCreateProcessing) error
	RecordSkippedSynthesisSourceScoped(context.Context, foundation.TransactionScope, organizingapp.SynthesisProcessing) error
	GetSynthesisProcessing(context.Context, foundation.ID, foundation.ID) (organizingapp.SynthesisProcessing, error)
	LoadSynthesisExecution(context.Context, foundation.ID, foundation.ID, foundation.ID) (SynthesisExecution, error)
	FreezeSynthesisInput(context.Context, workflowapp.ExecutionContext, SynthesisFrozenInput) (SynthesisFrozenInput, error)
	PrepareSynthesisApplication(context.Context, workflowapp.ExecutionContext, foundation.ID) error
	RecordSynthesisApplication(context.Context, workflowapp.ExecutionContext, organizingapp.SynthesisApplyResult) error
}

type SynthesisRetryStore interface {
	GetSynthesisProcessing(context.Context, foundation.ID, foundation.ID) (organizingapp.SynthesisProcessing, error)
	FindSynthesisRetryScoped(context.Context, foundation.TransactionScope, organizingapp.RetrySynthesisCommand, string) (organizingapp.RetrySynthesisResult, bool, error)
	LockSynthesisProcessingForRetryScoped(context.Context, foundation.TransactionScope, organizingapp.RetrySynthesisCommand) (organizingapp.SynthesisProcessing, int, error)
	RecordSynthesisRetryScoped(context.Context, foundation.TransactionScope, SynthesisRetryRecord) (organizingapp.RetrySynthesisResult, error)
}

type SynthesisProvenanceGate interface {
	IsGeneratedSynthesisSourceScoped(context.Context, foundation.TransactionScope, organizingdomain.SynthesisSourceVersion) (bool, error)
}

type SynthesisCandidateOwner interface {
	ListCandidates(context.Context, foundation.ID) ([]organizingapp.SynthesisGenerationNote, error)
	GetSynthesisRevision(context.Context, foundation.ID, foundation.ID, foundation.ID) (organizingdomain.SynthesisRevision, error)
	ApplyGeneration(context.Context, organizingapp.SynthesisGenerationInput, organizingapp.SynthesisGenerationResult) (organizingapp.SynthesisApplyResult, error)
	RecoverAppliedGeneration(context.Context, foundation.ID, foundation.ID) (organizingapp.SynthesisApplyResult, bool, error)
}

type SynthesisAppliedResultReader interface {
	LookupSynthesisApplyResultScoped(context.Context, foundation.TransactionScope, foundation.ID, foundation.ID) (organizingapp.SynthesisApplyResult, bool, error)
}

// SynthesisExecutionModel makes the trusted attempt explicit and leaves the
// generic Application model interfaces free of Workflow and adapter types.
type SynthesisExecutionModel interface {
	GenerateSynthesisForExecution(context.Context, workflowapp.ExecutionContext, organizingapp.SynthesisGenerationInput) (organizingapp.SynthesisGenerationResult, error)
	ValidateSynthesisSemanticsForExecution(context.Context, workflowapp.ExecutionContext, organizingapp.SynthesisGenerationInput, organizingapp.SynthesisGenerationResult) (organizingapp.SynthesisSemanticReceipt, error)
}

type SynthesisNodeReceipt struct {
	SchemaVersion int             `json:"schema_version"`
	ProcessingID  foundation.ID   `json:"processing_id"`
	WorkflowRunID foundation.ID   `json:"workflow_run_id"`
	Phase         string          `json:"phase"`
	RequestHash   string          `json:"request_hash,omitempty"`
	RevisionIDs   []foundation.ID `json:"revision_ids,omitempty"`
}

func (input SynthesisFrozenInput) ComputeHash() (string, error) {
	input.RequestHash = ""
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	return hashBytes(encoded), nil
}

func (input SynthesisFrozenInput) Validate() error {
	if !organizingapp.ValidSynthesisPromptVersions(input.OriginalPromptVersion(), input.GenerationPromptVersion, input.SemanticPromptVersion) {
		return synthesisInvalid("synthesis frozen semantic prompt version is invalid")
	}
	if organizingapp.IsSynthesisFusionSemanticVersion(input.SemanticPromptVersion) && (len(input.Notes) != 1 || !organizingapp.ValidSynthesisFusionTarget(input.SourceEvent.Fusion, input.Notes[0].Note.ID, input.Notes[0].Anchor)) {
		return synthesisInvalid("synthesis frozen fusion target differs from admission")
	}
	if !validID(input.ProcessingID) || !validID(input.WorkflowRunID) || input.SourceEvent.Validate() != nil ||
		len(input.Notes) > organizingapp.MaxSynthesisCandidateNotes || len(input.Sources) < 1 || len(input.Sources) > organizingdomain.MaxSynthesisSources || !validHash(input.RequestHash) {
		return synthesisInvalid("synthesis frozen input is invalid")
	}
	if input.BodyRefresh != nil {
		if input.Goal != nil || input.SourceEvent.Fusion != nil || len(input.Notes) != 1 || input.Notes[0].Note.ID != input.BodyRefresh.Request.NoteID || input.BodyRefresh.Validate(input.SourceEvent.Source.WorkspaceID) != nil {
			return synthesisInvalid("synthesis frozen body refresh binding is invalid")
		}
	}
	if input.Goal != nil {
		if input.SourceEvent.Fusion != nil || len(input.Notes) != 0 {
			return synthesisInvalid("goal generation cannot mix fusion or existing candidates")
		}
		if err := input.Goal.Validate(input.SourceEvent.Source.WorkspaceID, input.Sources); err != nil {
			return err
		}
	}
	seen := make(map[foundation.ID]bool, len(input.Notes))
	for _, note := range input.Notes {
		if note.PublicationID != "" && !validID(note.PublicationID) {
			return synthesisInvalid("synthesis frozen publication binding is invalid")
		}
		if note.Note.Validate() != nil || note.Note.WorkspaceID != input.SourceEvent.Source.WorkspaceID ||
			!validID(note.RevisionID) || note.Note.CurrentRevisionID != note.RevisionID || !validHash(note.RevisionHash) || seen[note.Note.ID] {
			return synthesisInvalid("synthesis frozen candidate binding is invalid")
		}
		if err := note.Anchor.Validate(input.SourceEvent.Source.WorkspaceID); err != nil {
			return err
		}
		seen[note.Note.ID] = true
		if len(note.Supplements) > organizingdomain.MaxSynthesisSources {
			return synthesisInvalid("synthesis supplement budget exceeded")
		}
		ids := map[foundation.ID]bool{}
		for _, supplement := range note.Supplements {
			if supplement.Validate() != nil || ids[supplement.ID] || supplement.WorkspaceID != input.SourceEvent.Source.WorkspaceID || supplement.NoteID != note.Note.ID {
				return synthesisInvalid("synthesis frozen supplement binding is invalid")
			}
			ids[supplement.ID] = true
		}
	}
	available := false
	refs := make(map[string]bool, len(input.Sources))
	for _, source := range input.Sources {
		if source.Validate() != nil || source.Source.WorkspaceID != input.SourceEvent.Source.WorkspaceID {
			return synthesisInvalid("synthesis frozen source binding is invalid")
		}
		key, _ := source.IdentityKey()
		if refs[key] {
			return synthesisInvalid("synthesis frozen source is duplicated")
		}
		refs[key] = true
		available = available || source.Source == input.SourceEvent.Source
	}
	for _, note := range input.Notes {
		if note.Anchor == nil {
			continue
		}
		if input.BodyRefresh != nil && len(note.Anchor.AllowedSources) != len(input.Sources) {
			return synthesisInvalid("refresh frozen evidence must exactly match admission")
		}
		for _, allowed := range note.Anchor.AllowedSources {
			if input.BodyRefresh == nil && allowed.Source != input.SourceEvent.Source {
				return synthesisInvalid("synthesis frozen anchor source is not incoming")
			}
			key, _ := allowed.IdentityKey()
			if !refs[key] {
				return synthesisInvalid("synthesis frozen anchor source is missing")
			}
		}
	}
	if input.Goal != nil {
		// PostgreSQL 存储的 JSONB 会在分隔符后加入空格。对仅含字符串和整数的此快照，
		// 带缩进 JSON 是保守的大小上限，避免略低于 1 MiB 的紧凑编码随后因 JSONB 展开而失败。
		encoded, err := json.MarshalIndent(input, "", " ")
		if err != nil {
			return synthesisInvalid("synthesis goal snapshot cannot be encoded")
		}
		if len(encoded) > 1<<20 {
			return workflowError(foundation.ErrorNonRetryableFailure, organizingapp.ErrorCodeSynthesisModelInputTooLarge, false, "complete goal snapshot exceeds the persistence budget")
		}
	}
	hash, err := input.ComputeHash()
	if err != nil || hash != input.RequestHash || !available && input.Goal == nil && input.BodyRefresh == nil {
		return synthesisInvalid("synthesis frozen input hash is invalid")
	}
	return nil
}

func synthesisInvalid(message string) error {
	return workflowError(foundation.ErrorConsistencyViolation, ErrorCodeSynthesisExecutionInvalid, false, message)
}

func SynthesisStartIdempotencyKey(processingID foundation.ID, executionNo int) string {
	encoded, _ := json.Marshal(SynthesisStartInput{ProcessingID: processingID, ExecutionNo: executionNo})
	return "synthesis-start:" + hashBytes(encoded)
}

// OriginalPromptVersion 取决于完整的冻结候选集合，
// 不取决于最新注册的运行时版本或准入前考虑的集合。
func (input SynthesisFrozenInput) OriginalPromptVersion() string {
	if input.BodyRefresh != nil {
		return organizingapp.SynthesisBodyRefreshPromptVersion
	}
	if input.Goal != nil {
		return organizingapp.SynthesisGoalPromptVersion
	}
	for _, note := range input.Notes {
		if note.PublicationID != "" {
			return organizingapp.SynthesisBodyPromptVersion
		}
	}
	for _, note := range input.Notes {
		if note.Anchor != nil {
			return organizingapp.SynthesisAnchoredPromptVersion
		}
	}
	return organizingapp.SynthesisLegacyPromptVersion
}

func (input *SynthesisFrozenInput) freezeSourceIdentityPromptVersions() {
	input.GenerationPromptVersion, input.SemanticPromptVersion = organizingapp.LatestSynthesisPromptVersions(input.OriginalPromptVersion(), input.SourceEvent.Fusion != nil)
}
