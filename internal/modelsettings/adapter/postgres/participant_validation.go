package postgres

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
)

func authorizeParticipantRegistration(state stateRecord, registration application.ParticipantRegistration) error {
	if !state.rolloutID.Valid || state.rolloutID.String != string(registration.RolloutID) ||
		!state.targetRevision.Valid || state.targetRevision.Int64 != registration.TargetRevision {
		return participantConflict(errors.New("model settings participant target changed"))
	}
	phase := domain.RolloutPhase(state.phase)
	if registration.InitialPhase == domain.ParticipantPhasePreparing && phase != domain.RolloutPhasePreparing && phase != domain.RolloutPhaseArming {
		return participantConflict(errors.New("model settings participant cannot prepare in current phase"))
	}
	if registration.InitialPhase == domain.ParticipantPhaseActivated && phase != domain.RolloutPhaseActivating {
		return participantConflict(errors.New("model settings participant cannot adopt before commit"))
	}
	return nil
}

func authorizeParticipantTransition(state stateRecord, command application.ParticipantTransitionCommand) error {
	phase := domain.RolloutPhase(state.phase)
	if command.NextPhase == domain.ParticipantPhaseRetired {
		if phase != domain.RolloutPhaseIdle || state.activeRevision != command.TargetRevision {
			return participantConflict(errors.New("model settings participant cannot retire before finalization"))
		}
		return nil
	}
	if !state.rolloutID.Valid || state.rolloutID.String != string(command.RolloutID) ||
		!state.targetRevision.Valid || state.targetRevision.Int64 != command.TargetRevision {
		return participantConflict(errors.New("model settings participant target changed"))
	}
	switch command.NextPhase {
	case domain.ParticipantPhasePrepared:
		if phase != domain.RolloutPhasePreparing && phase != domain.RolloutPhaseArming {
			return participantConflict(errors.New("model settings participant cannot prepare in current phase"))
		}
	case domain.ParticipantPhaseArmed:
		if phase != domain.RolloutPhaseArming {
			return participantConflict(errors.New("model settings participant cannot arm in current phase"))
		}
	case domain.ParticipantPhaseFailed, domain.ParticipantPhaseAborted:
		if phase != domain.RolloutPhasePreparing && phase != domain.RolloutPhaseArming && phase != domain.RolloutPhaseFailed {
			return participantConflict(errors.New("model settings participant cannot abort after commit"))
		}
	}
	return nil
}

func validParticipantIdentity(rolloutID foundation.ID, role domain.RuntimeRole, instanceID foundation.ID, targetRevision int64) bool {
	return validID(rolloutID) && domain.ValidRuntimeRole(role) && validID(instanceID) && targetRevision >= 0
}

func nullableErrorCode(value string) any {
	if value == "" {
		return nil
	}
	return value
}
