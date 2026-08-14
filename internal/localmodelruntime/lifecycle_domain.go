package localmodelruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// RuntimeMode identifies the deployment-owned lifecycle implementation.
type RuntimeMode string

const (
	RuntimeModeManaged        RuntimeMode = "managed"
	RuntimeModeExternalStatic RuntimeMode = "external-static"
)

// RuntimePhase is the durable supervisor/serve state machine.
type RuntimePhase string

const (
	RuntimePhaseStopped  RuntimePhase = "stopped"
	RuntimePhaseStarting RuntimePhase = "starting"
	RuntimePhasePulling  RuntimePhase = "pulling"
	RuntimePhaseChecking RuntimePhase = "checking"
	RuntimePhaseReady    RuntimePhase = "ready"
	RuntimePhaseStopping RuntimePhase = "stopping"
	RuntimePhaseFailed   RuntimePhase = "failed"
)

// OperationKind identifies the owner of a preparation operation.
type OperationKind string

const (
	OperationKindActivation     OperationKind = "activation"
	OperationKindActiveRecovery OperationKind = "active_recovery"
	OperationKindTest           OperationKind = "test"
)

// OperationPhase is intentionally broader than the supervisor's serve phase:
// a ready operation still needs a production probe before a test can succeed.
type OperationPhase string

const (
	OperationPhaseQueued     OperationPhase = "queued"
	OperationPhaseStarting   OperationPhase = "starting"
	OperationPhaseChecking   OperationPhase = "checking"
	OperationPhasePulling    OperationPhase = "pulling"
	OperationPhaseVerifying  OperationPhase = "verifying"
	OperationPhaseReady      OperationPhase = "ready"
	OperationPhaseProbing    OperationPhase = "probing"
	OperationPhaseSucceeded  OperationPhase = "succeeded"
	OperationPhaseFailed     OperationPhase = "failed"
	OperationPhaseSuperseded OperationPhase = "superseded"
)

// HoldKind identifies the lifecycle reference that keeps a model demand live.
type HoldKind string

const (
	HoldKindGeneration  HoldKind = "generation"
	HoldKindTest        HoldKind = "test"
	HoldKindPreparation HoldKind = "preparation"
)

// ModelRef is a bounded, non-secret Ollama model name.
type ModelRef string

// Requirement is the canonical, de-duplicated model set used by the runtime.
// Hash is empty for an empty requirement and otherwise SHA-256 of the same
// versioned newline representation emitted by modelsettings/domain. The
// persisted model set remains explicit so a hash cannot be used as a
// substitute for input validation.
type Requirement struct {
	Models []ModelRef
	Hash   string
}

// NewRequirement validates, sorts, de-duplicates, and hashes model references.
func NewRequirement(models []ModelRef) (Requirement, error) {
	unique := make(map[ModelRef]struct{}, len(models))
	for _, model := range models {
		model = ModelRef(strings.TrimSpace(string(model)))
		if err := validateModelRef(model); err != nil {
			return Requirement{}, err
		}
		unique[model] = struct{}{}
	}
	canonical := make([]ModelRef, 0, len(unique))
	for model := range unique {
		canonical = append(canonical, model)
	}
	sort.Slice(canonical, func(i, j int) bool { return canonical[i] < canonical[j] })
	result := Requirement{Models: canonical}
	if len(canonical) == 0 {
		return result, nil
	}
	refs := make([]string, len(canonical))
	for i, model := range canonical {
		refs[i] = string(model)
	}
	encoded := "managed-ollama-requirement/v1\n" + strings.Join(refs, "\n")
	digest := sha256.Sum256([]byte(encoded))
	result.Hash = hex.EncodeToString(digest[:])
	return result, nil
}

// ParseRequirement verifies a persisted model set and hash together.
func ParseRequirement(models []ModelRef, hash string) (Requirement, error) {
	requirement, err := NewRequirement(models)
	if err != nil {
		return Requirement{}, err
	}
	if requirement.Hash != hash {
		if len(requirement.Models) == 0 && hash == "" {
			return requirement, nil
		}
		return Requirement{}, fmt.Errorf("local model requirement hash does not match model set")
	}
	return requirement, nil
}

func validateModelRef(model ModelRef) error {
	value := string(model)
	if value == "" {
		return errors.New("local model reference is empty")
	}
	if !utf8.ValidString(value) || strings.IndexFunc(value, func(r rune) bool {
		return r == '\u0000' || r == '\n' || r == '\r' || r == '\t' || r == '\u007f' || (r >= 0x80 && r <= 0x9f)
	}) >= 0 {
		return errors.New("local model reference contains invalid control characters")
	}
	if len(value) > 128 {
		return errors.New("local model reference is too long")
	}
	return nil
}

// ManagerClaimCommand claims or renews the singleton supervisor lease.
type ManagerClaimCommand struct {
	OwnerID       foundation.ID
	LeaseDuration time.Duration
	StaleAfter    time.Duration
}

// ManagerHeartbeatCommand renews an exact manager owner/version.
type ManagerHeartbeatCommand struct {
	OwnerID         foundation.ID
	OwnerEpoch      int64
	ExpectedVersion int64
	LeaseDuration   time.Duration
}

// IntentReadCommand authorizes a supervisor demand read against its current
// manager lease. FreshWithin is checked with PostgreSQL time.
type IntentReadCommand struct {
	OwnerID         foundation.ID
	OwnerEpoch      int64
	ExpectedVersion int64
	FreshWithin     time.Duration
}

// ManagerLease is the database-time ownership projection.
type ManagerLease struct {
	OwnerID            foundation.ID
	OwnerEpoch         int64
	RequirementVersion int64
	Version            int64
	HeartbeatAt        time.Time
	LeaseExpiresAt     time.Time
	Mode               RuntimeMode
}

// DemandSource identifies why a model must remain available.
type DemandSource struct {
	Kind        string
	Revision    int64
	OperationID *foundation.ID
	HoldID      *foundation.ID
	Requirement Requirement
}

// DemandSnapshot is one repeatable-read projection of effective local demand.
type DemandSnapshot struct {
	Requirement          Requirement
	RequirementVersion   int64
	SettingsStateVersion int64
	Sources              []DemandSource
	Holds                []HoldRecord
	Operations           []OperationRecord
	Runtime              RuntimeRecord
	RolloutPhase         string
	MayStop              bool
}

// DemandCASCommand publishes a freshly computed requirement under manager
// fencing. It is separate from ReadDemand so reads remain side-effect free.
type DemandCASCommand struct {
	OwnerID                      foundation.ID
	OwnerEpoch                   int64
	ExpectedVersion              int64
	ExpectedRequirementVersion   int64
	ExpectedSettingsStateVersion int64
	Requirement                  Requirement
}

// HoldAcquireCommand creates or replays one exact hold.
type HoldAcquireCommand struct {
	HoldID          foundation.ID
	Kind            HoldKind
	OwnerID         foundation.ID
	OwnerEpoch      int64
	Role            string
	InstanceID      *foundation.ID
	Revision        *int64
	RolloutID       *foundation.ID
	OperationID     *foundation.ID
	Requirement     Requirement
	LeaseDuration   time.Duration
	ExpectedVersion int64
}

// HoldRenewCommand renews an exact hold with owner/version CAS.
type HoldRenewCommand struct {
	HoldID          foundation.ID
	OwnerID         foundation.ID
	OwnerEpoch      int64
	ExpectedVersion int64
	LeaseDuration   time.Duration
}

// HoldReleaseCommand releases a hold exactly once. A zero expected version
// allows an idempotent replay after the hold was already released.
type HoldReleaseCommand struct {
	HoldID          foundation.ID
	OwnerID         foundation.ID
	OwnerEpoch      int64
	ExpectedVersion int64
}

// HoldRecord is a durable generation/test/preparation reference.
type HoldRecord struct {
	HoldID         foundation.ID
	Kind           HoldKind
	OwnerID        foundation.ID
	OwnerEpoch     int64
	Role           string
	InstanceID     *foundation.ID
	Revision       *int64
	RolloutID      *foundation.ID
	OperationID    *foundation.ID
	Requirement    Requirement
	LeaseExpiresAt time.Time
	Version        int64
	ReleasedAt     *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// OperationClaimCommand claims the worker lease for one operation previously
// seeded by an API/Worker transaction. It cannot create lifecycle demand.
type OperationClaimCommand struct {
	OperationID     foundation.ID
	Kind            OperationKind
	IdempotencyKey  string
	RequestHash     string
	TargetRevision  *int64
	RolloutID       *foundation.ID
	Requirement     Requirement
	OwnerID         foundation.ID
	OwnerEpoch      int64
	LeaseDuration   time.Duration
	ExpectedVersion int64
}

// PullAttemptCommand atomically consumes one persisted pull budget entry for
// an operation already claimed by the current manager. It cannot create work.
type PullAttemptCommand struct {
	OperationID     foundation.ID
	OwnerID         foundation.ID
	OwnerEpoch      int64
	ExpectedVersion int64
	LeaseDuration   time.Duration
}

// OperationExpirySweepCommand terminates operations that exceeded the fixed
// database-time deadline and supersedes recovery work no longer bound to the
// active revision. It is manager-fenced so a stale supervisor cannot release a
// current operation hold.
type OperationExpirySweepCommand struct {
	OwnerID    foundation.ID
	OwnerEpoch int64
}

// ActiveRecoveryCommand asks PostgreSQL to derive a recovery operation from
// the exact active model-settings revision. The caller cannot supply models.
type ActiveRecoveryCommand struct {
	OwnerID                      foundation.ID
	OwnerEpoch                   int64
	ExpectedSettingsStateVersion int64
	LeaseDuration                time.Duration
}

// ActiveRecoveryCheckCommand verifies that one in-flight recovery is still
// bound to the current active revision and a fresh manager lease.
type ActiveRecoveryCheckCommand struct {
	OperationID foundation.ID
	OwnerID     foundation.ID
	OwnerEpoch  int64
}

// OperationProgressCommand records monotonic preparation progress.
type OperationProgressCommand struct {
	OperationID     foundation.ID
	OwnerID         foundation.ID
	OwnerEpoch      int64
	ExpectedVersion int64
	ExpectedPhase   OperationPhase
	NextPhase       OperationPhase
	CompletedModels []ModelRef
	CompletedBytes  int64
	TotalBytes      *int64
	ProgressKnown   bool
	ResolvedModels  []ResolvedModel
	LeaseDuration   time.Duration
}

// OperationClaimOwner is the persisted worker lease attached to a running
// operation. It is intentionally separate from manager ownership so a manager
// restart can reclaim work after the DB-time lease expires.
type OperationClaimOwner struct {
	OwnerID         foundation.ID
	OwnerEpoch      int64
	ExpectedVersion int64
	LeaseDuration   time.Duration
}

// ResolvedModel records the verified local manifest without storing raw API
// output or credentials.
type ResolvedModel struct {
	Model      ModelRef `json:"model"`
	Digest     string   `json:"digest"`
	Bytes      int64    `json:"bytes"`
	TotalBytes *int64   `json:"total_bytes,omitempty"`
}

// OperationTerminalCommand completes supervisor-owned preparation. Ready hands
// the operation back to its caller for the production probe; all other allowed
// phases are terminal.
type OperationTerminalCommand struct {
	OperationID     foundation.ID
	OwnerID         foundation.ID
	OwnerEpoch      int64
	ExpectedVersion int64
	Phase           OperationPhase
	ErrorCode       string
	Retryable       bool
	CompletedModels []ModelRef
	CompletedBytes  int64
	TotalBytes      *int64
	LeaseDuration   time.Duration
}

// OperationRecord is the durable, bounded preparation projection.
type OperationRecord struct {
	OperationID     foundation.ID
	Kind            OperationKind
	IdempotencyKey  string
	RequestHash     string
	TargetRevision  *int64
	RolloutID       *foundation.ID
	Requirement     Requirement
	ResolvedModels  []ResolvedModel
	Phase           OperationPhase
	ClaimOwnerID    *foundation.ID
	ClaimOwnerEpoch int64
	ClaimExpiresAt  *time.Time
	Version         int64
	CompletedModels []ModelRef
	CompletedBytes  int64
	TotalBytes      *int64
	ProgressKnown   bool
	AttemptNo       int
	LastProgressAt  *time.Time
	ErrorCode       string
	Retryable       bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
	TerminalAt      *time.Time
}

// PullAttemptResult couples the durable operation update with the DB-time
// budget that remains for the pull about to start.
type PullAttemptResult struct {
	Operation OperationRecord
	Remaining time.Duration
}

// ActivationPreparationCommand describes the immutable facts seeded by a
// model-settings activation transaction. The operation is inserted queued and
// without a worker claim; the preparation hold keeps its model demand live.
type ActivationPreparationCommand struct {
	OperationID    foundation.ID
	HoldID         foundation.ID
	RolloutID      foundation.ID
	TargetRevision int64
	IdempotencyKey string
	RequestHash    string
	Requirement    Requirement
	OwnerID        foundation.ID
	OwnerEpoch     int64
	LeaseDuration  time.Duration
}

// ActivationPreparation is the atomic operation/hold pair returned by a seed
// or replay.
type ActivationPreparation struct {
	Operation OperationRecord
	Hold      HoldRecord
}

// TestPreparationCommand describes a durable connection-test operation and
// its matching test hold. The operation is inserted queued and is claimed by
// the resident supervisor, not by the API request.
type TestPreparationCommand struct {
	OperationID    foundation.ID
	HoldID         foundation.ID
	TargetRevision *int64
	IdempotencyKey string
	RequestHash    string
	Requirement    Requirement
	OwnerID        foundation.ID
	OwnerEpoch     int64
	LeaseDuration  time.Duration
}

// TestPreparation is the atomic operation/hold pair returned by a test seed.
type TestPreparation struct {
	Operation OperationRecord
	Hold      HoldRecord
}

// TestProbeClaimCommand assigns the production probe to one API request.
// OwnerID must be random per request and distinct from the deterministic hold
// owner. The database lease lets a later replay recover if that request disappears.
type TestProbeClaimCommand struct {
	OperationID     foundation.ID
	OwnerID         foundation.ID
	OwnerEpoch      int64
	ExpectedVersion int64
	LeaseDuration   time.Duration
}

// TestProbeCompletionCommand closes or abandons a claimed production probe.
// Abandon returns probing to ready without releasing the durable demand hold.
type TestProbeCompletionCommand struct {
	OperationID     foundation.ID
	OwnerID         foundation.ID
	OwnerEpoch      int64
	ExpectedVersion int64
	ErrorCode       string
	Retryable       bool
	Abandon         bool
}

// RuntimePhaseCommand performs one owner/version guarded phase transition.
type RuntimePhaseCommand struct {
	OwnerID         foundation.ID
	OwnerEpoch      int64
	ExpectedVersion int64
	// The transition is fenced against this exact settings singleton version.
	ExpectedSettingsStateVersion int64
	ExpectedPhase                RuntimePhase
	NextPhase                    RuntimePhase
	RequirementHash              string
	ReadyHash                    string
	ChildEpoch                   int64
	ErrorCode                    string
	Retryable                    bool
}

// IntentSnapshot is the supervisor-facing effective requirement projection.
// It deliberately contains no settings endpoint, credential, or raw provider
// payload.
type IntentSnapshot struct {
	RequirementVersion int64
	Requirement        Requirement
	RolloutPhase       string
	Fresh              bool
	RuntimeVersion     int64
}

// RuntimeRecord is the durable singleton runtime projection.
type RuntimeRecord struct {
	Mode               RuntimeMode
	OwnerID            *foundation.ID
	OwnerEpoch         int64
	HeartbeatAt        *time.Time
	LeaseExpiresAt     *time.Time
	RequirementVersion int64
	Requirement        Requirement
	Phase              RuntimePhase
	ReadyHash          string
	ChildEpoch         int64
	ErrorCode          string
	Retryable          bool
	Version            int64
	UpdatedAt          time.Time
}

// ValidOperationKind reports whether an operation kind is persistable.
func ValidOperationKind(kind OperationKind) bool {
	switch kind {
	case OperationKindActivation, OperationKindActiveRecovery, OperationKindTest:
		return true
	default:
		return false
	}
}

// ValidHoldKind reports whether a hold kind is persistable.
func ValidHoldKind(kind HoldKind) bool {
	switch kind {
	case HoldKindGeneration, HoldKindTest, HoldKindPreparation:
		return true
	default:
		return false
	}
}

// ValidRuntimePhase reports whether a phase is persistable.
func ValidRuntimePhase(phase RuntimePhase) bool {
	switch phase {
	case RuntimePhaseStopped, RuntimePhaseStarting, RuntimePhasePulling,
		RuntimePhaseChecking, RuntimePhaseReady, RuntimePhaseStopping, RuntimePhaseFailed:
		return true
	default:
		return false
	}
}

// ValidOperationPhase reports whether an operation phase is persistable.
func ValidOperationPhase(phase OperationPhase) bool {
	switch phase {
	case OperationPhaseQueued, OperationPhaseStarting, OperationPhaseChecking,
		OperationPhasePulling, OperationPhaseVerifying, OperationPhaseReady,
		OperationPhaseProbing, OperationPhaseSucceeded, OperationPhaseFailed,
		OperationPhaseSuperseded:
		return true
	default:
		return false
	}
}

func terminalOperationPhase(phase OperationPhase) bool {
	return phase == OperationPhaseSucceeded || phase == OperationPhaseFailed || phase == OperationPhaseSuperseded
}

func validID(id foundation.ID) bool {
	_, err := foundation.ParseID(string(id))
	return err == nil
}
