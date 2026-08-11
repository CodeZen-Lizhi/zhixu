package application

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
)

// StartActivation fixes an exact target or returns the existing same-target operation.
func (service *Service) StartActivation(ctx context.Context, command StartActivationCommand) (StartActivationResult, error) {
	if err := service.readyActivation(ctx); err != nil {
		return StartActivationResult{}, err
	}
	if command.FreshWithin == 0 {
		command.FreshWithin = service.runtimeStaleAfter
	}
	if !validID(command.RolloutID) || command.TargetRevision < 0 ||
		command.ExpectedDesiredRevision != command.TargetRevision || command.ExpectedStateVersion <= 0 ||
		!validLease(command.LeaseDuration) || !validFreshness(command.FreshWithin) {
		return StartActivationResult{}, invalid(errors.New("activation start command is invalid"))
	}
	return service.activations.StartActivation(ctx, command)
}

// RenewActivation renews one live activation under exact phase/version CAS.
func (service *Service) RenewActivation(ctx context.Context, command RenewActivationCommand) (domain.RolloutState, error) {
	if err := service.readyActivation(ctx); err != nil {
		return domain.RolloutState{}, err
	}
	if !validID(command.RolloutID) || !domain.ActiveActivationPhase(command.ExpectedPhase) ||
		command.ExpectedVersion <= 0 || !validLease(command.LeaseDuration) {
		return domain.RolloutState{}, invalid(errors.New("activation renewal command is invalid"))
	}
	return service.activations.RenewActivation(ctx, command)
}

// AdvanceActivation advances preparing to arming after both roles are ready.
func (service *Service) AdvanceActivation(ctx context.Context, command AdvanceActivationCommand) (domain.RolloutState, error) {
	if err := service.readyActivation(ctx); err != nil {
		return domain.RolloutState{}, err
	}
	if !validID(command.RolloutID) || command.ExpectedVersion <= 0 || !validLease(command.LeaseDuration) ||
		!validFreshness(command.FreshWithin) {
		return domain.RolloutState{}, invalid(errors.New("activation advance command is invalid"))
	}
	if command.ExpectedPhase != domain.RolloutPhasePreparing || command.NextPhase != domain.RolloutPhaseArming {
		return domain.RolloutState{}, invalid(errors.New("activation advance transition is invalid"))
	}
	if err := domain.ValidateActivationTransition(command.ExpectedPhase, command.NextPhase); err != nil {
		return domain.RolloutState{}, err
	}
	return service.activations.AdvanceActivation(ctx, command)
}

// FailActivation records a failure only on the pre-commit side.
func (service *Service) FailActivation(ctx context.Context, command FailActivationCommand) (domain.RolloutState, error) {
	if err := service.readyActivation(ctx); err != nil {
		return domain.RolloutState{}, err
	}
	if !validID(command.RolloutID) || command.ExpectedVersion <= 0 ||
		(command.ExpectedPhase != domain.RolloutPhasePreparing && command.ExpectedPhase != domain.RolloutPhaseArming) ||
		!canonicalErrorCode(command.ErrorCode) {
		return domain.RolloutState{}, invalid(errors.New("activation failure command is invalid"))
	}
	return service.activations.FailActivation(ctx, command)
}

// CommitActivation performs the one-way arming-to-activating commit.
func (service *Service) CommitActivation(ctx context.Context, command CommitActivationCommand) (domain.RolloutState, error) {
	if err := service.readyActivation(ctx); err != nil {
		return domain.RolloutState{}, err
	}
	if !validID(command.RolloutID) || command.ExpectedVersion <= 0 || !validFreshness(command.FreshWithin) ||
		!validLease(command.LeaseDuration) {
		return domain.RolloutState{}, invalid(errors.New("activation commit command is invalid"))
	}
	return service.activations.CommitActivation(ctx, command)
}

// AcknowledgeActivation persists serving ownership and participant activation atomically.
func (service *Service) AcknowledgeActivation(ctx context.Context, command ActivationAcknowledgement) (domain.ParticipantRecord, error) {
	if err := service.readyActivation(ctx); err != nil {
		return domain.ParticipantRecord{}, err
	}
	if !validID(command.RolloutID) || !domain.ValidRuntimeRole(command.Role) || !validID(command.InstanceID) ||
		command.TargetRevision < 0 || command.ExpectedStateVersion <= 0 || command.ExpectedParticipantVersion <= 0 {
		return domain.ParticipantRecord{}, invalid(errors.New("activation acknowledgement is invalid"))
	}
	return service.activations.AcknowledgeActivation(ctx, command)
}

// FinalizeActivation clears a post-commit operation only after both acknowledgements are fresh.
func (service *Service) FinalizeActivation(ctx context.Context, command FinalizeActivationCommand) (domain.RolloutState, error) {
	if err := service.readyActivation(ctx); err != nil {
		return domain.RolloutState{}, err
	}
	if !validID(command.RolloutID) || command.ExpectedVersion <= 0 || !validFreshness(command.FreshWithin) {
		return domain.RolloutState{}, invalid(errors.New("activation finalize command is invalid"))
	}
	return service.activations.FinalizeActivation(ctx, command)
}

// RecoverActivation fails expired pre-commit work and renews expired post-commit work.
func (service *Service) RecoverActivation(ctx context.Context, command RecoverActivationCommand) (domain.ActivationRecovery, error) {
	if err := service.readyActivation(ctx); err != nil {
		return domain.ActivationRecovery{}, err
	}
	if !validLease(command.LeaseDuration) {
		return domain.ActivationRecovery{}, invalid(errors.New("activation recovery command is invalid"))
	}
	return service.activations.RecoverActivation(ctx, command)
}

// RegisterParticipant claims or replaces one stale participant binding.
func (service *Service) RegisterParticipant(ctx context.Context, registration ParticipantRegistration) (domain.ParticipantRecord, error) {
	if err := service.readyParticipant(ctx); err != nil {
		return domain.ParticipantRecord{}, err
	}
	if registration.StaleAfter == 0 {
		registration.StaleAfter = service.runtimeStaleAfter
	}
	if !validParticipantIdentity(registration.RolloutID, registration.Role, registration.InstanceID, registration.TargetRevision) ||
		(registration.InitialPhase != domain.ParticipantPhasePreparing && registration.InitialPhase != domain.ParticipantPhaseActivated) ||
		!validFreshness(registration.StaleAfter) {
		return domain.ParticipantRecord{}, invalid(errors.New("participant registration is invalid"))
	}
	return service.participants.RegisterParticipant(ctx, registration)
}

// HeartbeatParticipant renews one exact participant owner and version.
func (service *Service) HeartbeatParticipant(ctx context.Context, heartbeat ParticipantHeartbeat) (domain.ParticipantRecord, error) {
	if err := service.readyParticipant(ctx); err != nil {
		return domain.ParticipantRecord{}, err
	}
	if !validParticipantIdentity(heartbeat.RolloutID, heartbeat.Role, heartbeat.InstanceID, heartbeat.TargetRevision) ||
		!domain.ValidParticipantPhase(heartbeat.ExpectedPhase) || heartbeat.ExpectedVersion <= 0 {
		return domain.ParticipantRecord{}, invalid(errors.New("participant heartbeat is invalid"))
	}
	return service.participants.HeartbeatParticipant(ctx, heartbeat)
}

// TransitionParticipant changes one role-local candidate phase under ownership CAS.
func (service *Service) TransitionParticipant(ctx context.Context, command ParticipantTransitionCommand) (domain.ParticipantRecord, error) {
	if err := service.readyParticipant(ctx); err != nil {
		return domain.ParticipantRecord{}, err
	}
	if !validParticipantIdentity(command.RolloutID, command.Role, command.InstanceID, command.TargetRevision) || command.ExpectedVersion <= 0 {
		return domain.ParticipantRecord{}, invalid(errors.New("participant transition command is invalid"))
	}
	if err := domain.ValidateParticipantTransition(command.ExpectedPhase, command.NextPhase); err != nil {
		return domain.ParticipantRecord{}, err
	}
	if command.NextPhase == domain.ParticipantPhaseActivated {
		return domain.ParticipantRecord{}, invalid(errors.New("participant activation requires atomic acknowledgement"))
	}
	if command.NextPhase == domain.ParticipantPhaseFailed {
		if !canonicalErrorCode(command.ErrorCode) {
			return domain.ParticipantRecord{}, invalid(errors.New("participant failure code is invalid"))
		}
	} else if command.ErrorCode != "" || command.ErrorRetryable {
		return domain.ParticipantRecord{}, invalid(errors.New("participant non-failure diagnostic is invalid"))
	}
	return service.participants.TransitionParticipant(ctx, command)
}

func (service *Service) readyActivation(ctx context.Context) error {
	if err := service.ready(ctx); err != nil {
		return err
	}
	if nilInterface(service.activations) {
		return unavailable(errors.New("model settings activation store is unavailable"))
	}
	return nil
}

func (service *Service) readyParticipant(ctx context.Context) error {
	if err := service.ready(ctx); err != nil {
		return err
	}
	if nilInterface(service.participants) {
		return unavailable(errors.New("model settings participant store is unavailable"))
	}
	return nil
}

func validParticipantIdentity(rolloutID foundation.ID, role domain.RuntimeRole, instanceID foundation.ID, targetRevision int64) bool {
	return validID(rolloutID) && domain.ValidRuntimeRole(role) && validID(instanceID) && targetRevision >= 0
}

func validFreshness(duration time.Duration) bool {
	return ValidRuntimeFreshWithin(duration)
}
