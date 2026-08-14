package domain

import (
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	ErrorCodeRegistryInvalid              = "WORKSPACE_REGISTRY_INVALID"
	ErrorCodeMigrationRequired            = "WORKSPACE_MIGRATION_REQUIRED"
	ErrorCodeIdentityConflict             = "WORKSPACE_IDENTITY_CONFLICT"
	ErrorCodeActiveRemoveConflict         = "WORKSPACE_ACTIVE_REMOVE_CONFLICT"
	ErrorCodeSwitchInProgress             = "WORKSPACE_SWITCH_IN_PROGRESS"
	ErrorCodeSwitchIdempotencyConflict    = "WORKSPACE_SWITCH_IDEMPOTENCY_CONFLICT"
	ErrorCodeControlStateConflict         = "WORKSPACE_CONTROL_STATE_CONFLICT"
	ErrorCodeSwitchPhaseConflict          = "WORKSPACE_SWITCH_PHASE_CONFLICT"
	ErrorCodeSwitchLeaseHeld              = "WORKSPACE_SWITCH_LEASE_HELD"
	ErrorCodeSwitchLeaseExpired           = "WORKSPACE_SWITCH_LEASE_EXPIRED"
	ErrorCodeRuntimeConflict              = "WORKSPACE_RUNTIME_CONFLICT"
	ErrorCodeRuntimeNotPrepared           = "WORKSPACE_RUNTIME_NOT_PREPARED"
	ErrorCodeRuntimeMutationConflict      = "WORKSPACE_RUNTIME_MUTATION_CONFLICT"
	ErrorCodeControlCorrupt               = "WORKSPACE_CONTROL_CORRUPT"
	ErrorCodeControlDatabaseUnavailable   = "WORKSPACE_CONTROL_DATABASE_UNAVAILABLE"
	ErrorCodeWorkspaceNotFound            = "WORKSPACE_NOT_FOUND"
	ErrorCodeWorkspaceVersionConflict     = "WORKSPACE_VERSION_CONFLICT"
	ErrorCodeSwitchNotFound               = "WORKSPACE_SWITCH_NOT_FOUND"
	ErrorCodeRuntimeHeartbeatStale        = "WORKSPACE_RUNTIME_HEARTBEAT_STALE"
	ErrorCodeRuntimeBindingMismatch       = "WORKSPACE_RUNTIME_BINDING_MISMATCH"
	ErrorCodeWorkspaceAlreadyRemoved      = "WORKSPACE_ALREADY_REMOVED"
	ErrorCodeWorkspaceAvailabilityInvalid = "WORKSPACE_AVAILABILITY_INVALID"
	ErrorCodeBindingRebindInvalid         = "WORKSPACE_REBIND_INVALID"
	ErrorCodeBindingRebindConflict        = "WORKSPACE_REBIND_CONFLICT"
)

// SwitchPhase is the durable single-grant operation phase.
type SwitchPhase string

const (
	SwitchPhaseValidating    SwitchPhase = "validating"
	SwitchPhaseQuiescing     SwitchPhase = "quiescing"
	SwitchPhaseRevoking      SwitchPhase = "revoking"
	SwitchPhaseApplyingGrant SwitchPhase = "applying_grant"
	SwitchPhasePreparing     SwitchPhase = "preparing"
	SwitchPhaseVerifying     SwitchPhase = "verifying"
	SwitchPhaseCommitting    SwitchPhase = "committing"
	SwitchPhaseActivating    SwitchPhase = "activating"
	SwitchPhaseRollingBack   SwitchPhase = "rolling_back"
	SwitchPhaseRecovering    SwitchPhase = "recovering"
)

// SwitchResult is the immutable terminal outcome of a switch operation.
type SwitchResult string

const (
	SwitchResultSucceeded  SwitchResult = "succeeded"
	SwitchResultRejected   SwitchResult = "rejected"
	SwitchResultCancelled  SwitchResult = "cancelled"
	SwitchResultRolledBack SwitchResult = "rolled_back"
	SwitchResultFailed     SwitchResult = "failed"
)

// RuntimeRole identifies a process that must hold the same root grant.
type RuntimeRole string

const (
	RuntimeRoleAPI    RuntimeRole = "api"
	RuntimeRoleWorker RuntimeRole = "worker"
)

// RuntimePhase is one process's persisted root-grant lifecycle.
type RuntimePhase string

const (
	RuntimePhaseActive      RuntimePhase = "active"
	RuntimePhaseQuiescing   RuntimePhase = "quiescing"
	RuntimePhaseQuiesced    RuntimePhase = "quiesced"
	RuntimePhasePrepared    RuntimePhase = "prepared"
	RuntimePhaseVerifying   RuntimePhase = "verifying"
	RuntimePhaseUnavailable RuntimePhase = "unavailable"
)

// ControlState is the authoritative singleton for active identity and operation ownership.
type ControlState struct {
	ActiveWorkspaceID         *foundation.ID
	ResumeWorkspaceID         *foundation.ID
	GrantGeneration           int64
	StateVersion              int64
	OperationID               *foundation.ID
	OperationPhase            SwitchPhase
	TargetWorkspaceID         *foundation.ID
	PreviousActiveWorkspaceID *foundation.ID
	ControllerLeaseOwnerID    *foundation.ID
	ControllerLeaseExpiresAt  time.Time
	LastErrorCode             string
	UpdatedAt                 time.Time
}

// SwitchOperation freezes target identity, generations, idempotency and lease state.
type SwitchOperation struct {
	ID                    foundation.ID
	ControllerInstanceID  foundation.ID
	IdempotencyKey        string
	RequestHash           string
	PreviousWorkspaceID   *foundation.ID
	TargetWorkspaceID     foundation.ID
	TargetRootFingerprint string
	TargetBindingVersion  int64
	GrantGeneration       int64
	RecoveryGeneration    int64
	ExpectedStateVersion  int64
	Phase                 SwitchPhase
	Result                SwitchResult
	LeaseOwnerID          *foundation.ID
	LeaseExpiresAt        time.Time
	HeartbeatAt           time.Time
	DeadlineAt            time.Time
	ErrorCode             string
	Version               int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
	CompletedAt           time.Time
}

// RuntimeRecord binds one process instance to an exact Workspace root generation.
type RuntimeRecord struct {
	Role            RuntimeRole
	InstanceID      foundation.ID
	WorkspaceID     foundation.ID
	OperationID     *foundation.ID
	GrantGeneration int64
	RootFingerprint string
	BindingVersion  int64
	Phase           RuntimePhase
	StartedAt       time.Time
	HeartbeatAt     time.Time
	Version         int64
	Fresh           bool
}

// ControlSnapshot is the complete persistence view used by one-shot Workspace coordination.
type ControlSnapshot struct {
	State     ControlState
	Active    *Workspace
	Registry  []Workspace
	Operation *SwitchOperation
	Runtimes  []RuntimeRecord
}

// RootBinding is a host-validated, canonical directory identity.
type RootBinding struct {
	CanonicalPath     string
	GitRepositoryPath string
	Fingerprint       string
	BindingVersion    int64
}

// WorkspaceReservation is the only ordinary command that can create a root identity.
type WorkspaceReservation struct {
	ID      foundation.ID
	Name    string
	Binding RootBinding
	Git     GitBaseline
	Now     time.Time
}

// WorkspaceResolution reports whether Registry reused an existing identity.
type WorkspaceResolution struct {
	Workspace Workspace
	Reused    bool
}

// AvailabilityUpdate changes grant eligibility without changing root identity.
type AvailabilityUpdate struct {
	WorkspaceID     foundation.ID
	ExpectedVersion int64
	Availability    WorkspaceAvailability
	Reason          string
	CheckedAt       time.Time
}

// WorkspaceRemoval soft-removes one inactive recent identity.
type WorkspaceRemoval struct {
	WorkspaceID     foundation.ID
	ExpectedVersion int64
	RemovedAt       time.Time
}

// WorkspaceBindingMigration is the explicit, fail-closed replacement of a
// physical root identity while preserving the logical Workspace ID.
type WorkspaceBindingMigration struct {
	ID                   foundation.ID
	ControllerInstanceID foundation.ID
	IdempotencyKey       string
	WorkspaceID          foundation.ID
	CanonicalRoot        string
	OldRootFingerprint   string
	NewRootFingerprint   string
}

// WorkspaceBindingMigrationResult reports the persisted post-rebind identity.
type WorkspaceBindingMigrationResult struct {
	Workspace Workspace
	Changed   bool
}

// ValidSwitchPhase reports whether phase is persistable.
func ValidSwitchPhase(phase SwitchPhase) bool {
	switch phase {
	case SwitchPhaseValidating, SwitchPhaseQuiescing, SwitchPhaseRevoking,
		SwitchPhaseApplyingGrant, SwitchPhasePreparing, SwitchPhaseVerifying,
		SwitchPhaseCommitting, SwitchPhaseActivating, SwitchPhaseRollingBack,
		SwitchPhaseRecovering:
		return true
	default:
		return false
	}
}

// ValidSwitchResult reports whether result is terminal and persistable.
func ValidSwitchResult(result SwitchResult) bool {
	switch result {
	case SwitchResultSucceeded, SwitchResultRejected, SwitchResultCancelled,
		SwitchResultRolledBack, SwitchResultFailed:
		return true
	default:
		return false
	}
}

// ValidateSwitchTransition owns legal non-terminal phase changes.
func ValidateSwitchTransition(from, to SwitchPhase) error {
	valid := from == to ||
		from == SwitchPhaseValidating && to == SwitchPhaseQuiescing ||
		from == SwitchPhaseQuiescing && to == SwitchPhaseRevoking ||
		from == SwitchPhaseRevoking && to == SwitchPhaseApplyingGrant ||
		from == SwitchPhaseApplyingGrant && to == SwitchPhasePreparing ||
		from == SwitchPhasePreparing && to == SwitchPhaseVerifying ||
		from == SwitchPhaseVerifying && to == SwitchPhaseCommitting ||
		from == SwitchPhaseCommitting && to == SwitchPhaseActivating ||
		rollbackSource(from) && to == SwitchPhaseRollingBack ||
		from == SwitchPhaseRollingBack && to == SwitchPhaseRecovering
	if !valid {
		return foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeSwitchPhaseConflict, false, errors.New("workspace switch phase transition is invalid"))
	}
	return nil
}

// ValidateTerminalTransition ensures result is compatible with the final phase.
func ValidateTerminalTransition(phase SwitchPhase, result SwitchResult, errorCode string) error {
	valid := result == SwitchResultSucceeded && phase == SwitchPhaseActivating && errorCode == "" ||
		result == SwitchResultRejected && phase == SwitchPhaseValidating && validErrorCode(errorCode) ||
		result == SwitchResultCancelled && (phase == SwitchPhaseValidating || phase == SwitchPhaseQuiescing) && errorCode == "" ||
		(result == SwitchResultRolledBack || result == SwitchResultFailed) && phase == SwitchPhaseRecovering && validErrorCode(errorCode)
	if !valid {
		return foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeSwitchPhaseConflict, false, errors.New("workspace switch terminal transition is invalid"))
	}
	return nil
}

// ValidRuntimeRole reports whether role participates in root grant readiness.
func ValidRuntimeRole(role RuntimeRole) bool {
	return role == RuntimeRoleAPI || role == RuntimeRoleWorker
}

// ValidRuntimePhase reports whether phase is persistable.
func ValidRuntimePhase(phase RuntimePhase) bool {
	switch phase {
	case RuntimePhaseActive, RuntimePhaseQuiescing, RuntimePhaseQuiesced,
		RuntimePhasePrepared, RuntimePhaseVerifying, RuntimePhaseUnavailable:
		return true
	default:
		return false
	}
}

// ValidateRuntimeTransition owns process-driven phase changes for one instance.
func ValidateRuntimeTransition(from, to RuntimePhase) error {
	valid := from == to ||
		from == RuntimePhaseActive && to == RuntimePhaseQuiescing ||
		from == RuntimePhaseQuiescing && to == RuntimePhaseQuiesced ||
		(from == RuntimePhaseQuiescing || from == RuntimePhaseQuiesced) && to == RuntimePhaseActive ||
		from == RuntimePhasePrepared && to == RuntimePhaseVerifying ||
		(from == RuntimePhasePrepared || from == RuntimePhaseVerifying) && to == RuntimePhaseActive ||
		to == RuntimePhaseUnavailable
	if !valid {
		return foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeRuntimeConflict, false, errors.New("workspace runtime phase transition is invalid"))
	}
	return nil
}

func rollbackSource(phase SwitchPhase) bool {
	switch phase {
	case SwitchPhaseRevoking, SwitchPhaseApplyingGrant, SwitchPhasePreparing,
		SwitchPhaseVerifying, SwitchPhaseCommitting, SwitchPhaseActivating:
		return true
	default:
		return false
	}
}

func validErrorCode(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character != '_' && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}
