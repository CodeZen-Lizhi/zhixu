package workspacepostgres

import (
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

func authorizeRuntime(
	state domain.ControlState,
	operation *domain.SwitchOperation,
	workspace domain.Workspace,
	registration application.RuntimeRegistration,
) error {
	if workspace.Availability != domain.WorkspaceAvailabilityAvailable || !workspace.RemovedAt.IsZero() ||
		workspace.RootFingerprint != registration.RootFingerprint ||
		workspace.BindingVersion != registration.BindingVersion {
		return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeRuntimeBindingMismatch, false, errors.New("workspace runtime root binding differs from Registry"))
	}
	if registration.OperationID == nil {
		if operation != nil || !sameOptionalID(state.ActiveWorkspaceID, registration.WorkspaceID) ||
			state.GrantGeneration != registration.GrantGeneration ||
			(registration.Phase != domain.RuntimePhaseActive && registration.Phase != domain.RuntimePhaseUnavailable) {
			return runtimeConflict(errors.New("workspace active runtime grant is not authorized"))
		}
		if state.OperationID != nil && state.OperationPhase != domain.SwitchPhaseValidating &&
			state.OperationPhase != domain.SwitchPhaseQuiescing &&
			state.OperationPhase != domain.SwitchPhaseActivating &&
			state.OperationPhase != domain.SwitchPhaseRecovering {
			return runtimeConflict(errors.New("workspace active runtime is no longer authorized during switch"))
		}
		return nil
	}
	if operation == nil || state.OperationID == nil || *state.OperationID != operation.ID ||
		operation.Result != "" || *registration.OperationID != operation.ID {
		return runtimeConflict(errors.New("workspace candidate runtime operation is not authorized"))
	}
	switch registration.Phase {
	case domain.RuntimePhaseQuiescing, domain.RuntimePhaseQuiesced:
		if operation.PreviousWorkspaceID == nil || registration.WorkspaceID != *operation.PreviousWorkspaceID ||
			registration.GrantGeneration != operation.GrantGeneration-1 ||
			(state.OperationPhase != domain.SwitchPhaseQuiescing && state.OperationPhase != domain.SwitchPhaseRevoking) {
			return runtimeConflict(errors.New("workspace quiescing runtime grant is not authorized"))
		}
	case domain.RuntimePhasePrepared, domain.RuntimePhaseVerifying:
		targetPhase := state.OperationPhase == domain.SwitchPhaseApplyingGrant || state.OperationPhase == domain.SwitchPhasePreparing ||
			state.OperationPhase == domain.SwitchPhaseVerifying || state.OperationPhase == domain.SwitchPhaseCommitting ||
			state.OperationPhase == domain.SwitchPhaseActivating
		recoveryPhase := state.OperationPhase == domain.SwitchPhaseRollingBack || state.OperationPhase == domain.SwitchPhaseRecovering
		if targetPhase {
			if registration.WorkspaceID != operation.TargetWorkspaceID || registration.GrantGeneration != operation.GrantGeneration {
				return runtimeConflict(errors.New("workspace target runtime grant is not authorized"))
			}
		} else if recoveryPhase {
			if operation.PreviousWorkspaceID == nil || registration.WorkspaceID != *operation.PreviousWorkspaceID ||
				registration.GrantGeneration != operation.RecoveryGeneration {
				return runtimeConflict(errors.New("workspace recovery runtime grant is not authorized"))
			}
		} else {
			return runtimeConflict(errors.New("workspace prepared runtime phase is not authorized"))
		}
	default:
		return runtimeConflict(errors.New("workspace operation-bound runtime phase is invalid"))
	}
	return nil
}

func authorizeRuntimeTransition(
	state domain.ControlState,
	operation *domain.SwitchOperation,
	record domain.RuntimeRecord,
	command application.RuntimePhaseCommand,
) (*foundation.ID, error) {
	if err := domain.ValidateRuntimeTransition(command.ExpectedPhase, command.NextPhase); err != nil {
		return nil, err
	}
	if command.NextPhase == domain.RuntimePhaseUnavailable {
		return nil, nil
	}
	if operation == nil || command.OperationID == nil || state.OperationID == nil ||
		*state.OperationID != operation.ID || *command.OperationID != operation.ID || operation.Result != "" {
		return nil, runtimeConflict(errors.New("workspace runtime transition operation is not active"))
	}
	switch {
	case command.ExpectedPhase == domain.RuntimePhaseActive && command.NextPhase == domain.RuntimePhaseQuiescing:
		if record.OperationID != nil || state.OperationPhase != domain.SwitchPhaseQuiescing ||
			operation.PreviousWorkspaceID == nil || record.WorkspaceID != *operation.PreviousWorkspaceID ||
			record.GrantGeneration != operation.GrantGeneration-1 {
			return nil, runtimeConflict(errors.New("workspace runtime cannot begin quiescing"))
		}
		return command.OperationID, nil
	case command.ExpectedPhase == domain.RuntimePhaseQuiescing && command.NextPhase == domain.RuntimePhaseQuiesced:
		if !equalOptionalIDs(record.OperationID, command.OperationID) || state.OperationPhase != domain.SwitchPhaseQuiescing {
			return nil, runtimeConflict(errors.New("workspace runtime cannot finish quiescing"))
		}
		return command.OperationID, nil
	case (command.ExpectedPhase == domain.RuntimePhaseQuiescing || command.ExpectedPhase == domain.RuntimePhaseQuiesced) &&
		command.NextPhase == domain.RuntimePhaseActive:
		if !equalOptionalIDs(record.OperationID, command.OperationID) || state.OperationPhase != domain.SwitchPhaseQuiescing ||
			!sameOptionalID(state.ActiveWorkspaceID, record.WorkspaceID) {
			return nil, runtimeConflict(errors.New("workspace runtime cannot cancel quiescing"))
		}
		return nil, nil
	case command.ExpectedPhase == domain.RuntimePhasePrepared && command.NextPhase == domain.RuntimePhaseVerifying:
		if !equalOptionalIDs(record.OperationID, command.OperationID) || state.OperationPhase != domain.SwitchPhaseVerifying {
			return nil, runtimeConflict(errors.New("workspace runtime cannot begin verification"))
		}
		return command.OperationID, nil
	case (command.ExpectedPhase == domain.RuntimePhasePrepared || command.ExpectedPhase == domain.RuntimePhaseVerifying) &&
		command.NextPhase == domain.RuntimePhaseActive:
		activationPhase := state.OperationPhase == domain.SwitchPhaseActivating || state.OperationPhase == domain.SwitchPhaseRecovering
		if !equalOptionalIDs(record.OperationID, command.OperationID) || !activationPhase ||
			!sameOptionalID(state.ActiveWorkspaceID, record.WorkspaceID) || state.GrantGeneration != record.GrantGeneration {
			return nil, runtimeConflict(errors.New("workspace runtime cannot activate grant"))
		}
		return nil, nil
	default:
		return nil, runtimeConflict(errors.New("workspace runtime phase transition is not authorized"))
	}
}

func runtimeHeartbeatFresh(heartbeat, now time.Time, freshWithin time.Duration) bool {
	return !heartbeat.Before(now.Add(-freshWithin)) && !heartbeat.After(now)
}
