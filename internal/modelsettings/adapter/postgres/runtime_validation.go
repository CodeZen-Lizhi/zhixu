package postgres

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
)

const defaultRuntimeTakeoverStaleAfter = application.DefaultRuntimeFreshWithin

func runtimeAvailabilityRestoreAuthorized(state stateRecord, revision int64) bool {
	switch domain.RolloutPhase(state.phase) {
	case domain.RolloutPhaseIdle, domain.RolloutPhaseFailed:
		return state.activeRevision == revision
	case domain.RolloutPhasePreparing, domain.RolloutPhaseArming:
		return state.activeRevision == revision && state.previousActive.Valid && state.previousActive.Int64 == revision
	case domain.RolloutPhaseActivating:
		return state.activeRevision == revision && state.targetRevision.Valid && state.targetRevision.Int64 == revision
	default:
		return false
	}
}

func authorizeRuntimeRegistration(state stateRecord, registration application.RuntimeRegistration) error {
	if registration.RolloutID != nil || (registration.Phase != domain.RuntimePhaseActive && registration.Phase != domain.RuntimePhaseUnavailable) {
		return runtimeConflict(errors.New("model settings runtime registration must describe serving ownership"))
	}
	phase := domain.RolloutPhase(state.phase)
	expectedRevision := state.activeRevision
	if phase == domain.RolloutPhasePreparing || phase == domain.RolloutPhaseArming || phase == domain.RolloutPhaseFailed {
		if state.previousActive.Valid {
			expectedRevision = state.previousActive.Int64
		}
	}
	if registration.AppliedRevision != expectedRevision {
		return runtimeConflict(errors.New("model settings serving runtime revision is not authorized"))
	}
	return nil
}

func validRuntimeRegistration(registration application.RuntimeRegistration) bool {
	if !domain.ValidRuntimeRole(registration.Role) || !validID(registration.InstanceID) || registration.AppliedRevision < 0 ||
		!validOptionalID(registration.RolloutID) || !domain.ValidRuntimePhase(registration.Phase) ||
		!validFreshWithin(registration.StaleAfter) {
		return false
	}
	if registration.RolloutID == nil {
		return registration.Phase == domain.RuntimePhaseActive || registration.Phase == domain.RuntimePhaseUnavailable
	}
	return registration.Phase == domain.RuntimePhasePrepared || registration.Phase == domain.RuntimePhaseVerifying
}

func sameOptionalID(left, right *foundation.ID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func validOptionalID(id *foundation.ID) bool { return id == nil || validID(*id) }

func nullableID(id *foundation.ID) any {
	if id == nil {
		return nil
	}
	return string(*id)
}

func prefixedRuntimeColumns(alias string) string {
	return alias + `.role,` + alias + `.instance_id::text,` + alias + `.applied_revision,` +
		alias + `.rollout_id::text,` + alias + `.phase,` + alias + `.applied_at,` + alias + `.heartbeat_at`
}
