package workspacepostgres

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const controlStateColumns = `active_workspace_id::text,resume_workspace_id::text,
	grant_generation,state_version,operation_id::text,operation_phase,target_workspace_id::text,
	previous_active_workspace_id::text,controller_lease_owner_id::text,
	controller_lease_expires_at,last_error_code,updated_at`

const switchOperationColumns = `id::text,controller_instance_id::text,idempotency_key,request_hash,
	previous_workspace_id::text,target_workspace_id::text,target_root_fingerprint,target_binding_version,
	grant_generation,recovery_generation,expected_state_version,phase,result,lease_owner_id::text,
	lease_expires_at,heartbeat_at,deadline_at,error_code,version,created_at,updated_at,completed_at`

const workspaceRuntimeColumns = `role,instance_id::text,workspace_id::text,operation_id::text,
	grant_generation,root_fingerprint,binding_version,phase,started_at,heartbeat_at,version`

type mutationGate struct {
	OwnerKind      string
	OwnerID        *foundation.ID
	LeaseExpiresAt time.Time
	Version        int64
	UpdatedAt      time.Time
}

func scanControlState(row rowScanner) (domain.ControlState, error) {
	var state domain.ControlState
	var activeID, resumeID, operationID, targetID, previousID, ownerID sql.NullString
	var phase, errorCode sql.NullString
	var lease sql.NullTime
	if err := row.Scan(
		&activeID, &resumeID, &state.GrantGeneration, &state.StateVersion,
		&operationID, &phase, &targetID, &previousID, &ownerID, &lease,
		&errorCode, &state.UpdatedAt,
	); err != nil {
		return domain.ControlState{}, err
	}
	var err error
	if state.ActiveWorkspaceID, err = optionalID(activeID); err != nil {
		return domain.ControlState{}, fmt.Errorf("parse active workspace id: %w", err)
	}
	if state.ResumeWorkspaceID, err = optionalID(resumeID); err != nil {
		return domain.ControlState{}, fmt.Errorf("parse resume workspace id: %w", err)
	}
	if state.OperationID, err = optionalID(operationID); err != nil {
		return domain.ControlState{}, fmt.Errorf("parse workspace operation id: %w", err)
	}
	if state.TargetWorkspaceID, err = optionalID(targetID); err != nil {
		return domain.ControlState{}, fmt.Errorf("parse target workspace id: %w", err)
	}
	if state.PreviousActiveWorkspaceID, err = optionalID(previousID); err != nil {
		return domain.ControlState{}, fmt.Errorf("parse previous workspace id: %w", err)
	}
	if state.ControllerLeaseOwnerID, err = optionalID(ownerID); err != nil {
		return domain.ControlState{}, fmt.Errorf("parse controller lease owner id: %w", err)
	}
	state.OperationPhase = domain.SwitchPhase(phase.String)
	state.LastErrorCode = errorCode.String
	if lease.Valid {
		state.ControllerLeaseExpiresAt = lease.Time
	}
	return state, nil
}

func scanSwitchOperation(row rowScanner) (domain.SwitchOperation, error) {
	var operation domain.SwitchOperation
	var id, controllerID, targetID string
	var previousID, result, ownerID, errorCode sql.NullString
	var lease, completed sql.NullTime
	var phase string
	if err := row.Scan(
		&id, &controllerID, &operation.IdempotencyKey, &operation.RequestHash,
		&previousID, &targetID, &operation.TargetRootFingerprint, &operation.TargetBindingVersion,
		&operation.GrantGeneration, &operation.RecoveryGeneration, &operation.ExpectedStateVersion,
		&phase, &result, &ownerID, &lease, &operation.HeartbeatAt, &operation.DeadlineAt,
		&errorCode, &operation.Version, &operation.CreatedAt, &operation.UpdatedAt, &completed,
	); err != nil {
		return domain.SwitchOperation{}, err
	}
	parsedID, err := foundation.ParseID(id)
	if err != nil {
		return domain.SwitchOperation{}, fmt.Errorf("parse workspace switch id: %w", err)
	}
	parsedControllerID, err := foundation.ParseID(controllerID)
	if err != nil {
		return domain.SwitchOperation{}, fmt.Errorf("parse controller instance id: %w", err)
	}
	parsedTargetID, err := foundation.ParseID(targetID)
	if err != nil {
		return domain.SwitchOperation{}, fmt.Errorf("parse target workspace id: %w", err)
	}
	operation.ID = parsedID
	operation.ControllerInstanceID = parsedControllerID
	operation.TargetWorkspaceID = parsedTargetID
	if operation.PreviousWorkspaceID, err = optionalID(previousID); err != nil {
		return domain.SwitchOperation{}, fmt.Errorf("parse previous workspace id: %w", err)
	}
	if operation.LeaseOwnerID, err = optionalID(ownerID); err != nil {
		return domain.SwitchOperation{}, fmt.Errorf("parse workspace switch lease owner: %w", err)
	}
	operation.Phase = domain.SwitchPhase(phase)
	operation.Result = domain.SwitchResult(result.String)
	operation.ErrorCode = errorCode.String
	if lease.Valid {
		operation.LeaseExpiresAt = lease.Time
	}
	if completed.Valid {
		operation.CompletedAt = completed.Time
	}
	return operation, nil
}

func scanWorkspaceRuntime(row rowScanner) (domain.RuntimeRecord, error) {
	var runtime domain.RuntimeRecord
	var role, instanceID, workspaceID, phase string
	var operationID sql.NullString
	if err := row.Scan(
		&role, &instanceID, &workspaceID, &operationID, &runtime.GrantGeneration,
		&runtime.RootFingerprint, &runtime.BindingVersion, &phase,
		&runtime.StartedAt, &runtime.HeartbeatAt, &runtime.Version,
	); err != nil {
		return domain.RuntimeRecord{}, err
	}
	parsedInstanceID, err := foundation.ParseID(instanceID)
	if err != nil {
		return domain.RuntimeRecord{}, fmt.Errorf("parse workspace runtime instance id: %w", err)
	}
	parsedWorkspaceID, err := foundation.ParseID(workspaceID)
	if err != nil {
		return domain.RuntimeRecord{}, fmt.Errorf("parse workspace runtime workspace id: %w", err)
	}
	runtime.Role = domain.RuntimeRole(role)
	runtime.InstanceID = parsedInstanceID
	runtime.WorkspaceID = parsedWorkspaceID
	runtime.Phase = domain.RuntimePhase(phase)
	if runtime.OperationID, err = optionalID(operationID); err != nil {
		return domain.RuntimeRecord{}, fmt.Errorf("parse workspace runtime operation id: %w", err)
	}
	return runtime, nil
}

func scanMutationGate(row rowScanner) (mutationGate, error) {
	var gate mutationGate
	var ownerKind, ownerID sql.NullString
	var lease sql.NullTime
	if err := row.Scan(&ownerKind, &ownerID, &lease, &gate.Version, &gate.UpdatedAt); err != nil {
		return mutationGate{}, err
	}
	gate.OwnerKind = ownerKind.String
	var err error
	if gate.OwnerID, err = optionalID(ownerID); err != nil {
		return mutationGate{}, fmt.Errorf("parse runtime mutation owner id: %w", err)
	}
	if lease.Valid {
		gate.LeaseExpiresAt = lease.Time
	}
	return gate, nil
}

func optionalID(value sql.NullString) (*foundation.ID, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	parsed, err := foundation.ParseID(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func nullableFoundationID(value *foundation.ID) any {
	if value == nil {
		return nil
	}
	return string(*value)
}

type lockedSwitch struct {
	State     domain.ControlState
	Operation domain.SwitchOperation
	Gate      mutationGate
	Now       time.Time
}

func equalOptionalIDs(left, right *foundation.ID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func validateControlSnapshot(snapshot domain.ControlSnapshot) error {
	state := snapshot.State
	if state.OperationID == nil {
		if snapshot.Operation != nil || state.OperationPhase != "" || state.TargetWorkspaceID != nil ||
			state.PreviousActiveWorkspaceID != nil || state.ControllerLeaseOwnerID != nil {
			return controlCorrupt(errors.New("idle workspace control state retains operation ownership"))
		}
	} else {
		if snapshot.Operation == nil || snapshot.Operation.Result != "" || snapshot.Operation.ID != *state.OperationID ||
			state.OperationPhase != snapshot.Operation.Phase || state.TargetWorkspaceID == nil ||
			*state.TargetWorkspaceID != snapshot.Operation.TargetWorkspaceID ||
			!equalOptionalIDs(state.PreviousActiveWorkspaceID, snapshot.Operation.PreviousWorkspaceID) ||
			state.ControllerLeaseOwnerID == nil || snapshot.Operation.LeaseOwnerID == nil ||
			*state.ControllerLeaseOwnerID != *snapshot.Operation.LeaseOwnerID {
			return controlCorrupt(errors.New("workspace control operation projection is inconsistent"))
		}
	}
	if state.GrantGeneration > 0 && snapshot.Active != nil {
		if snapshot.Active.Status != domain.WorkspaceStatusActive ||
			snapshot.Active.Availability != domain.WorkspaceAvailabilityAvailable || !snapshot.Active.RemovedAt.IsZero() {
			return controlCorrupt(errors.New("active workspace Registry row is not grantable"))
		}
	}
	return nil
}

func sameOptionalID(candidate *foundation.ID, expected foundation.ID) bool {
	return candidate != nil && *candidate == expected
}
