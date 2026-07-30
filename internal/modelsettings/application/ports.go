// Package application coordinates model settings validation, persistence, and rollout control.
package application

import (
	"context"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
)

// Validator is the single configured-factory validation seam used before persistence.
type Validator interface {
	ValidateModelSettings(context.Context, domain.Settings, domain.SecretConfiguration) error
}

// ValidatorFunc adapts a function into the validation seam.
type ValidatorFunc func(context.Context, domain.Settings, domain.SecretConfiguration) error

func (function ValidatorFunc) ValidateModelSettings(ctx context.Context, settings domain.Settings, secrets domain.SecretConfiguration) error {
	return function(ctx, settings, secrets)
}

// SecretSealer protects credentials with revision- and target-bound AEAD.
type SecretSealer interface {
	Seal(domain.Secret, domain.SecretContext) (domain.EncryptedSecret, error)
	Open(domain.EncryptedSecret, domain.SecretContext) (domain.Secret, error)
}

// SaveCommand atomically appends a desired revision using optimistic concurrency.
type SaveCommand struct {
	ExpectedRevision int64
	Settings         domain.Settings
	ChatSecret       domain.SecretAction
	EmbeddingSecret  domain.SecretAction
	CreatedBy        string
}

// ModelSettingsAuditAction is the fixed append-only audit action for a settings revision.
type ModelSettingsAuditAction string

const (
	// ModelSettingsAuditActionUpdated records one committed desired revision.
	ModelSettingsAuditActionUpdated ModelSettingsAuditAction = "model_settings.update"
)

// ModelSettingsChange is the deliberately redacted audit summary for one committed revision.
// It must never grow endpoint, credential, encrypted credential, or draft fields.
type ModelSettingsChange struct {
	Action                 ModelSettingsAuditAction
	Revision               int64
	ChatProvider           domain.ChatProvider
	EmbeddingProvider      domain.EmbeddingProvider
	ChatKeyConfigured      bool
	EmbeddingKeyConfigured bool
}

// SettingsAuditAppender appends a redacted model-settings fact in the caller's transaction.
type SettingsAuditAppender interface {
	AppendModelSettingsChangeTx(context.Context, any, ModelSettingsChange) error
}

// DraftCommand resolves keep/replace/clear for a non-persistent connection test.
type DraftCommand struct {
	ExpectedRevision int64
	Settings         domain.Settings
	ChatSecret       domain.SecretAction
	EmbeddingSecret  domain.SecretAction
}

// ConnectionTarget identifies the capability exercised by a settings connection test.
type ConnectionTarget string

const (
	// ConnectionTargetChat selects the configured Chat provider.
	ConnectionTargetChat ConnectionTarget = "chat"
	// ConnectionTargetEmbedding selects the configured Embedding provider.
	ConnectionTargetEmbedding ConnectionTarget = "embedding"
)

// TestCommand resolves and exercises one non-persistent settings draft.
type TestCommand struct {
	Target ConnectionTarget
	Draft  DraftCommand
}

// TestResult is the non-secret identity returned after a successful provider request.
type TestResult struct {
	Target   ConnectionTarget
	Provider string
	Model    string
}

// ResolvedConnectionTester is the provider boundary used only after the manager owns a resolved draft.
type ResolvedConnectionTester interface {
	TestResolvedConnection(context.Context, ConnectionTarget, domain.ResolvedSettings) error
}

// BeginRolloutCommand fixes desired as the target under a database lease.
type BeginRolloutCommand struct {
	RolloutID     foundation.ID
	LeaseDuration time.Duration
}

// AdvanceRolloutCommand advances one legal phase while renewing the lease.
type AdvanceRolloutCommand struct {
	RolloutID     foundation.ID
	ExpectedPhase domain.RolloutPhase
	NextPhase     domain.RolloutPhase
	LeaseDuration time.Duration
}

// RenewRolloutCommand extends the lease without changing the target or phase.
type RenewRolloutCommand struct {
	RolloutID     foundation.ID
	ExpectedPhase domain.RolloutPhase
	LeaseDuration time.Duration
}

// FailRolloutCommand restores the previous active revision and keeps a safe diagnostic code.
type FailRolloutCommand struct {
	RolloutID foundation.ID
	ErrorCode string
}

// CommitRolloutCommand atomically publishes target only after both roles are fresh and prepared.
type CommitRolloutCommand struct {
	RolloutID   foundation.ID
	FreshWithin time.Duration
}

// RuntimeRegistration claims one role after the fixed revision has been loaded successfully.
type RuntimeRegistration struct {
	Role            domain.RuntimeRole
	InstanceID      foundation.ID
	AppliedRevision int64
	RolloutID       *foundation.ID
	Phase           domain.RuntimePhase
}

// RuntimeHeartbeat is a strict role/instance/rollout ownership CAS.
type RuntimeHeartbeat struct {
	Role       domain.RuntimeRole
	InstanceID foundation.ID
	RolloutID  *foundation.ID
}

// RuntimePhaseCommand changes phase only for the current role owner.
type RuntimePhaseCommand struct {
	Role          domain.RuntimeRole
	InstanceID    foundation.ID
	RolloutID     *foundation.ID
	ExpectedPhase domain.RuntimePhase
	NextPhase     domain.RuntimePhase
}

// RevisionStore owns desired revisions and transient credential resolution.
type RevisionStore interface {
	Snapshot(context.Context, time.Duration) (domain.Snapshot, error)
	SaveDesired(context.Context, SaveCommand) (domain.Snapshot, error)
	ResolveDraft(context.Context, DraftCommand) (domain.ResolvedSettings, error)
	LoadRevision(context.Context, int64) (domain.ResolvedSettings, error)
}

// RolloutStore owns the leased desired-to-active state machine.
type RolloutStore interface {
	BeginRollout(context.Context, BeginRolloutCommand) (domain.RolloutState, error)
	RenewRollout(context.Context, RenewRolloutCommand) (domain.RolloutState, error)
	AdvanceRollout(context.Context, AdvanceRolloutCommand) (domain.RolloutState, error)
	FailRollout(context.Context, FailRolloutCommand) (domain.RolloutState, error)
	CommitRollout(context.Context, CommitRolloutCommand) (domain.RolloutState, error)
	RecoverExpiredRollout(context.Context) (domain.RolloutState, bool, error)
}

// RuntimeStore owns applied revision and process heartbeat records.
type RuntimeStore interface {
	RegisterRuntime(context.Context, RuntimeRegistration) (domain.RuntimeRecord, error)
	HeartbeatRuntime(context.Context, RuntimeHeartbeat) (domain.RuntimeRecord, error)
	SetRuntimePhase(context.Context, RuntimePhaseCommand) (domain.RuntimeRecord, error)
}

// SettingsManager is the only model-settings surface required by HTTP.
// Test keeps ResolvedSettings and transient credential destruction inside the module.
type SettingsManager interface {
	Snapshot(context.Context) (domain.Snapshot, error)
	Save(context.Context, SaveCommand) (domain.Snapshot, error)
	Test(context.Context, TestCommand) (TestResult, error)
}

// RevisionLoader is the credential-bearing revision boundary required by the runtime loader.
type RevisionLoader interface {
	Snapshot(context.Context) (domain.Snapshot, error)
	LoadRevision(context.Context, int64) (domain.ResolvedSettings, error)
}

// RuntimeController is the narrow process ownership and heartbeat surface.
type RuntimeController interface {
	Snapshot(context.Context) (domain.Snapshot, error)
	RegisterRuntime(context.Context, RuntimeRegistration) (domain.RuntimeRecord, error)
	HeartbeatRuntime(context.Context, RuntimeHeartbeat) (domain.RuntimeRecord, error)
	SetRuntimePhase(context.Context, RuntimePhaseCommand) (domain.RuntimeRecord, error)
}
