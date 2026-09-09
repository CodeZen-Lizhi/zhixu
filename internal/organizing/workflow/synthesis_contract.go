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
	return []workflowdomain.RegisteredDefinition{{Key: SynthesisDefinitionKey, Version: SynthesisDefinitionVersion,
		InputSchemaVersion: SynthesisInputSchemaVersion, Graph: workflowdomain.CanonicalGraph{Nodes: nodes}}}
}

func SynthesisExecutorNodeKinds() []string {
	return []string{SynthesisPrepareNodeKind, SynthesisGenerateNodeKind, SynthesisValidateNodeKind, SynthesisApplyNodeKind}
}

// SynthesisStartInput is the complete queue-safe input. Recovery only restores
// an already committed application receipt; it does not generate from old text.
type SynthesisStartInput struct {
	ProcessingID  foundation.ID `json:"processing_id"`
	ExecutionNo   int           `json:"execution_no"`
	ApplyRecovery bool          `json:"apply_recovery"`
}

// SynthesisFrozenNote keeps only the candidate identity and frozen metadata.
// Item text remains in the owner's immutable revision and is reopened by ID.
type SynthesisFrozenNote struct {
	Note         organizingdomain.SynthesisNote `json:"note"`
	RevisionID   foundation.ID                  `json:"revision_id"`
	RevisionHash string                         `json:"revision_hash"`
}

// SynthesisFrozenInput contains no source excerpts. Its hash binds the exact
// source catalogue and immutable candidate revisions across Worker restarts.
type SynthesisFrozenInput struct {
	ProcessingID  foundation.ID                         `json:"processing_id"`
	WorkflowRunID foundation.ID                         `json:"workflow_run_id"`
	SourceEvent   organizingdomain.SynthesisSourceReady `json:"source_event"`
	Notes         []SynthesisFrozenNote                 `json:"notes"`
	Sources       []organizingdomain.SynthesisSourceRef `json:"sources"`
	RequestHash   string                                `json:"request_hash"`
}

// SynthesisExecution is reloaded for every delivered node. Model steps own the
// exact accepted results; Workflow outputs carry only their receipt identifiers.
type SynthesisExecution struct {
	Processing    organizingapp.SynthesisProcessing
	WorkflowRunID foundation.ID
	ExecutionNo   int
	ApplyRecovery bool
	Input         *SynthesisFrozenInput
	Generation    *organizingapp.SynthesisModelStepRecord
	Semantic      *organizingapp.SynthesisModelStepRecord
	Applied       *organizingapp.SynthesisApplyResult
	CreatedAt     time.Time
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
	if !validID(input.ProcessingID) || !validID(input.WorkflowRunID) || input.SourceEvent.Validate() != nil ||
		len(input.Notes) > organizingapp.MaxSynthesisCandidateNotes || len(input.Sources) < 1 || len(input.Sources) > organizingdomain.MaxSynthesisSources || !validHash(input.RequestHash) {
		return synthesisInvalid("synthesis frozen input is invalid")
	}
	seen := make(map[foundation.ID]bool, len(input.Notes))
	for _, note := range input.Notes {
		if note.Note.Validate() != nil || note.Note.WorkspaceID != input.SourceEvent.Source.WorkspaceID ||
			!validID(note.RevisionID) || note.Note.CurrentRevisionID != note.RevisionID || !validHash(note.RevisionHash) || seen[note.Note.ID] {
			return synthesisInvalid("synthesis frozen candidate binding is invalid")
		}
		seen[note.Note.ID] = true
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
	hash, err := input.ComputeHash()
	if err != nil || hash != input.RequestHash || !available {
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
