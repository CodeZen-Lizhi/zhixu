package domain

import (
	"errors"
	"fmt"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// RolloutPhase is the persisted model activation state machine.
type RolloutPhase string

const (
	RolloutPhaseIdle       RolloutPhase = "idle"
	RolloutPhasePreparing  RolloutPhase = "preparing"
	RolloutPhaseArming     RolloutPhase = "arming"
	RolloutPhaseActivating RolloutPhase = "activating"
	RolloutPhaseFailed     RolloutPhase = "failed"

	// Legacy restart phases remain compile-time compatibility symbols while
	// modelctl and the process-replacement runtime are retired in later phases.
	RolloutPhaseValidating RolloutPhase = "validating"
	RolloutPhaseDraining   RolloutPhase = "draining"
	RolloutPhaseApplying   RolloutPhase = "applying"
	RolloutPhaseVerifying  RolloutPhase = "verifying"
)

// RuntimeRole identifies a process that must apply the same revision.
type RuntimeRole string

const (
	RuntimeRoleAPI    RuntimeRole = "api"
	RuntimeRoleWorker RuntimeRole = "worker"
)

// RuntimePhase is one role's persisted lifecycle projection.
type RuntimePhase string

const (
	RuntimePhaseActive      RuntimePhase = "active"
	RuntimePhaseQuiescing   RuntimePhase = "quiescing"
	RuntimePhaseQuiesced    RuntimePhase = "quiesced"
	RuntimePhasePrepared    RuntimePhase = "prepared"
	RuntimePhaseVerifying   RuntimePhase = "verifying"
	RuntimePhaseUnavailable RuntimePhase = "unavailable"
)

// Capability is the active model capability projection.
type Capability string

const (
	CapabilityDisabled    Capability = "disabled"
	CapabilityConfigured  Capability = "configured"
	CapabilityUnavailable Capability = "unavailable"
)

// RolloutState is the singleton rollout lease and immutable target binding.
type RolloutState struct {
	ID                     foundation.ID
	TargetRevision         int64
	PreviousActiveRevision int64
	Phase                  RolloutPhase
	LeaseExpiresAt         time.Time
	LastErrorCode          string
	Version                int64
}

// RuntimeRecord is an internal ownership record. InstanceID is never returned by HTTP.
type RuntimeRecord struct {
	Role            RuntimeRole
	InstanceID      foundation.ID
	AppliedRevision int64
	RolloutID       *foundation.ID
	Phase           RuntimePhase
	AppliedAt       time.Time
	HeartbeatAt     time.Time
	Fresh           bool
}

// RuntimeSummary is safe to expose through Settings.
type RuntimeSummary struct {
	AppliedRevision int64
	Phase           RuntimePhase
	Fresh           bool
}

// RuntimeSummaries contains both mandatory roles without a stringly typed map.
type RuntimeSummaries struct {
	API    RuntimeSummary
	Worker RuntimeSummary
}

// ParticipantPhase is one role's candidate-generation activation phase.
type ParticipantPhase string

const (
	ParticipantPhasePreparing ParticipantPhase = "preparing"
	ParticipantPhasePrepared  ParticipantPhase = "prepared"
	ParticipantPhaseArmed     ParticipantPhase = "armed"
	ParticipantPhaseActivated ParticipantPhase = "activated"
	ParticipantPhaseFailed    ParticipantPhase = "failed"
	ParticipantPhaseAborted   ParticipantPhase = "aborted"
	ParticipantPhaseRetired   ParticipantPhase = "retired"
)

// ParticipantRecord is the internal candidate owner and CAS projection.
type ParticipantRecord struct {
	RolloutID      foundation.ID
	Role           RuntimeRole
	InstanceID     foundation.ID
	TargetRevision int64
	Phase          ParticipantPhase
	HeartbeatAt    time.Time
	Version        int64
	LastErrorCode  string
	ErrorRetryable bool
	PreparedAt     time.Time
	ActivatedAt    time.Time
	RetiredAt      time.Time
	Fresh          bool
}

// ParticipantSummary is safe to expose without process identity.
type ParticipantSummary struct {
	Present        bool
	TargetRevision int64
	Phase          ParticipantPhase
	Fresh          bool
	LastErrorCode  string
	ErrorRetryable bool
}

// ParticipantSummaries contains both mandatory activation roles.
type ParticipantSummaries struct {
	API    ParticipantSummary
	Worker ParticipantSummary
}

// ActivationCommitSide determines the only safe recovery direction.
type ActivationCommitSide string

const (
	ActivationCommitSideNone       ActivationCommitSide = "none"
	ActivationCommitSidePreCommit  ActivationCommitSide = "pre_commit"
	ActivationCommitSidePostCommit ActivationCommitSide = "post_commit"
)

// ActivationRecoveryAction records how an expired durable operation converged.
type ActivationRecoveryAction string

const (
	ActivationRecoveryNone              ActivationRecoveryAction = "none"
	ActivationRecoveryFailedPreCommit   ActivationRecoveryAction = "failed_pre_commit"
	ActivationRecoveryAdoptedPostCommit ActivationRecoveryAction = "adopted_post_commit"
)

// ActivationRecovery is the durable result of one recovery pass.
type ActivationRecovery struct {
	State  RolloutState
	Action ActivationRecoveryAction
}

// Snapshot is the complete non-secret Settings read model.
type Snapshot struct {
	DesiredRevision     int64
	ActiveRevision      int64
	DesiredSettings     SettingsSummary
	ActiveSettings      SettingsSummary
	Runtime             RuntimeSummaries
	Rollout             RolloutState
	Participants        ParticipantSummaries
	ApplyRequired       bool
	RestartRequired     bool
	ChatCapability      Capability
	EmbeddingCapability Capability
}

// ValidActivationPhase reports phases accepted by the hot-activation schema.
func ValidActivationPhase(phase RolloutPhase) bool {
	switch phase {
	case RolloutPhaseIdle, RolloutPhasePreparing, RolloutPhaseArming, RolloutPhaseActivating, RolloutPhaseFailed:
		return true
	default:
		return false
	}
}

// ActiveActivationPhase reports whether a durable activation owns the mutation gate.
func ActiveActivationPhase(phase RolloutPhase) bool {
	return phase == RolloutPhasePreparing || phase == RolloutPhaseArming || phase == RolloutPhaseActivating
}

// CommitSide returns the recovery side implied by the durable phase.
func (state RolloutState) CommitSide() ActivationCommitSide {
	switch state.Phase {
	case RolloutPhasePreparing, RolloutPhaseArming, RolloutPhaseFailed:
		return ActivationCommitSidePreCommit
	case RolloutPhaseActivating:
		return ActivationCommitSidePostCommit
	default:
		return ActivationCommitSideNone
	}
}

// ValidateActivationTransition owns the legal global hot-activation transitions.
func ValidateActivationTransition(from, to RolloutPhase) error {
	valid := (from == RolloutPhaseIdle || from == RolloutPhaseFailed) && to == RolloutPhasePreparing ||
		from == RolloutPhasePreparing && (to == RolloutPhaseArming || to == RolloutPhaseFailed) ||
		from == RolloutPhaseArming && (to == RolloutPhaseActivating || to == RolloutPhaseFailed) ||
		from == RolloutPhaseActivating && to == RolloutPhaseIdle
	if !valid {
		return foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeActivationConflict, false, errors.New("model settings activation phase transition is invalid"))
	}
	return nil
}

// ValidParticipantPhase reports phases accepted by the participant history table.
func ValidParticipantPhase(phase ParticipantPhase) bool {
	switch phase {
	case ParticipantPhasePreparing, ParticipantPhasePrepared, ParticipantPhaseArmed, ParticipantPhaseActivated,
		ParticipantPhaseFailed, ParticipantPhaseAborted, ParticipantPhaseRetired:
		return true
	default:
		return false
	}
}

// ValidateParticipantTransition owns the legal same-owner participant transitions.
func ValidateParticipantTransition(from, to ParticipantPhase) error {
	preCommitTerminal := to == ParticipantPhaseFailed || to == ParticipantPhaseAborted
	valid := from == ParticipantPhasePreparing && (to == ParticipantPhasePrepared || preCommitTerminal) ||
		from == ParticipantPhasePrepared && (to == ParticipantPhaseArmed || preCommitTerminal) ||
		from == ParticipantPhaseArmed && (to == ParticipantPhaseActivated || preCommitTerminal) ||
		from == ParticipantPhaseActivated && to == ParticipantPhaseRetired
	if !valid {
		return foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeParticipantConflict, false, errors.New("model settings participant phase transition is invalid"))
	}
	return nil
}

// ValidateRolloutTransition owns the legacy restart phase advances.
// Deprecated: use ValidateActivationTransition for hot activation.
func ValidateRolloutTransition(from, to RolloutPhase) error {
	valid := from == RolloutPhaseValidating && to == RolloutPhaseDraining ||
		from == RolloutPhaseDraining && to == RolloutPhaseApplying ||
		from == RolloutPhaseApplying && to == RolloutPhaseVerifying
	if !valid {
		return foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeRolloutConflict, false, errors.New("rollout phase transition is invalid"))
	}
	return nil
}

func ValidRuntimeRole(role RuntimeRole) bool {
	return role == RuntimeRoleAPI || role == RuntimeRoleWorker
}

func ValidRuntimePhase(phase RuntimePhase) bool {
	switch phase {
	case RuntimePhaseActive, RuntimePhaseQuiescing, RuntimePhaseQuiesced, RuntimePhasePrepared, RuntimePhaseVerifying, RuntimePhaseUnavailable:
		return true
	default:
		return false
	}
}

// ValidateRuntimeTransition owns the process-driven runtime phase transitions.
func ValidateRuntimeTransition(from, to RuntimePhase) error {
	valid := from == RuntimePhaseActive && to == RuntimePhaseQuiescing ||
		from == RuntimePhaseQuiescing && to == RuntimePhaseQuiesced ||
		from == RuntimePhasePrepared && to == RuntimePhaseVerifying
	if !valid {
		return foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeRuntimeConflict, false, errors.New("runtime phase transition is invalid"))
	}
	return nil
}

func (state RolloutState) String() string {
	return fmt.Sprintf("RolloutState{configured:%t target_revision:%d previous_active_revision:%d phase:%q version:%d error_code:%q}",
		state.ID != "", state.TargetRevision, state.PreviousActiveRevision, state.Phase, state.Version, state.LastErrorCode)
}
func (state RolloutState) GoString() string { return state.String() }
