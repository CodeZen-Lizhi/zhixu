package application

import (
	"context"
	"encoding/json"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	MaxSynthesisSemanticChecks                 = 512
	ErrorCodeSynthesisModelStepInvalid         = "SYNTHESIS_MODEL_STEP_INVALID"
	SynthesisRuntimeVersion                    = "v1"
	SynthesisDeltaPromptID                     = "synthesis-delta"
	SynthesisSemanticPromptID                  = "synthesis-semantic-review"
	ErrorCodeSynthesisCapabilityUnavailable    = "SYNTHESIS_MODEL_CAPABILITY_UNAVAILABLE"
	ErrorCodeSynthesisModelContextInvalid      = "SYNTHESIS_MODEL_CONTEXT_INVALID"
	ErrorCodeSynthesisModelOutputInvalid       = "SYNTHESIS_MODEL_OUTPUT_INVALID"
	ErrorCodeSynthesisModelInputTooLarge       = "SYNTHESIS_MODEL_INPUT_TOO_LARGE"
	ErrorCodeSynthesisModelReplayUnsafe        = "SYNTHESIS_MODEL_REPLAY_UNSAFE"
	ErrorCodeSynthesisModelFinalizationUnknown = "SYNTHESIS_MODEL_FINALIZATION_UNKNOWN"
	ErrorCodeSynthesisSemanticRejected         = "SYNTHESIS_SEMANTIC_REJECTED"
)

type SynthesisModelStage string

const (
	SynthesisModelGenerate SynthesisModelStage = "GENERATE"
	SynthesisModelValidate SynthesisModelStage = "VALIDATE"
)

type SynthesisModelStepStatus string

const (
	SynthesisModelStepRunning          SynthesisModelStepStatus = "RUNNING"
	SynthesisModelStepReady            SynthesisModelStepStatus = "READY"
	SynthesisModelStepFailed           SynthesisModelStepStatus = "FAILED"
	SynthesisModelStepRecoveryRequired SynthesisModelStepStatus = "RECOVERY_REQUIRED"
)

// SynthesisSemanticReceipt is server-bound proof that a separate model attempt
// reviewed the exact generated output. Rejected reviews are also durable READY
// model results; they never authorize application of the delta.
type SynthesisSemanticReceipt struct {
	ModelRunID           foundation.ID `json:"model_run_id"`
	RequestHash          string        `json:"request_hash"`
	GenerationModelRunID foundation.ID `json:"generation_model_run_id"`
	GenerationOutputHash string        `json:"generation_output_hash"`
	OutputHash           string        `json:"output_hash"`
	CheckCount           int           `json:"check_count"`
	Accepted             bool          `json:"accepted"`
}

// SynthesisModelStepRecord is the immutable request and CAS-controlled recovery
// state. Output is the exact accepted Provider document. Generation retains the
// server-allocated item identities, so a replay must never bind it a second time.
// Source excerpts belong only to the transient request, never to this record.
type SynthesisModelStepRecord struct {
	ID                    foundation.ID
	WorkspaceID           foundation.ID
	ProcessingID          foundation.ID
	WorkflowRunID         foundation.ID
	NodeRunID             foundation.ID
	NodeAttemptID         foundation.ID
	Stage                 SynthesisModelStage
	RequestHash           string
	InputRequestHash      string
	GenerationOutputHash  string
	ModelSettingsRevision *int64
	ModelRunID            foundation.ID
	Status                SynthesisModelStepStatus
	Output                json.RawMessage
	OutputHash            string
	Generation            *SynthesisGenerationResult
	Semantic              *SynthesisSemanticReceipt
	ErrorCode             string
	Retryable             bool
	Version               int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
	CompletedAt           *time.Time
}

type PrepareSynthesisModelStepCommand struct{ Record SynthesisModelStepRecord }

type BindSynthesisModelRunCommand struct {
	WorkspaceID     foundation.ID
	StepID          foundation.ID
	ExpectedVersion int64
	ModelRunID      foundation.ID
}

// CompleteSynthesisModelStepCommand must atomically persist accepted output and
// the bound business result/receipt, then succeed the exact ModelRun in the same
// pool transaction. A commit error remains unknown until an exact replay read.
type CompleteSynthesisModelStepCommand struct {
	WorkspaceID             foundation.ID
	StepID                  foundation.ID
	ExpectedVersion         int64
	ModelRunID              foundation.ID
	ExpectedModelRunVersion int64
	ResultType              string
	Output                  json.RawMessage
	OutputHash              string
	Generation              *SynthesisGenerationResult
	Semantic                *SynthesisSemanticReceipt
	CompletedAt             time.Time
}

type FailSynthesisModelStepCommand struct {
	WorkspaceID             foundation.ID
	StepID                  foundation.ID
	ExpectedVersion         int64
	ModelRunID              foundation.ID
	ExpectedModelRunVersion int64
	ModelRunStatus          agentdomain.ModelRunStatus
	ErrorCode               string
	Retryable               bool
	CompletedAt             time.Time
}

// SynthesisModelStore owns the durable step fence, not a second model-call log.
// Prepare must reject an earlier uncertain attempt for the same node. Lookup
// may replay a READY step across transport attempts, with its original IDs.
type SynthesisModelStore interface {
	LookupReady(context.Context, foundation.ID, foundation.ID, SynthesisModelStage, string) (SynthesisModelStepRecord, bool, error)
	Prepare(context.Context, PrepareSynthesisModelStepCommand) (SynthesisModelStepRecord, bool, error)
	BindModelRun(context.Context, BindSynthesisModelRunCommand) (SynthesisModelStepRecord, error)
	Complete(context.Context, CompleteSynthesisModelStepCommand) (SynthesisModelStepRecord, bool, error)
	Fail(context.Context, FailSynthesisModelStepCommand) error
}

// SynthesisSemanticReviewer exposes the independent model receipt to the
// Workflow owner while preserving the original application validator port.
type SynthesisSemanticReviewer interface {
	ValidateSynthesisSemanticsWithReceipt(context.Context, SynthesisGenerationInput, SynthesisGenerationResult) (SynthesisSemanticReceipt, error)
}
