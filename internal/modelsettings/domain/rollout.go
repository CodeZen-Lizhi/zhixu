package domain

import (
	"errors"
	"fmt"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// RolloutPhase is the persisted restart state machine.
type RolloutPhase string

const (
	RolloutPhaseIdle       RolloutPhase = "idle"
	RolloutPhaseValidating RolloutPhase = "validating"
	RolloutPhaseDraining   RolloutPhase = "draining"
	RolloutPhaseApplying   RolloutPhase = "applying"
	RolloutPhaseVerifying  RolloutPhase = "verifying"
	RolloutPhaseFailed     RolloutPhase = "failed"
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

// Snapshot is the complete non-secret Settings read model.
type Snapshot struct {
	DesiredRevision     int64
	ActiveRevision      int64
	DesiredSettings     SettingsSummary
	ActiveSettings      SettingsSummary
	Runtime             RuntimeSummaries
	Rollout             RolloutState
	RestartRequired     bool
	ChatCapability      Capability
	EmbeddingCapability Capability
}

// ValidateRolloutTransition owns the only legal non-terminal rollout phase advances.
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
