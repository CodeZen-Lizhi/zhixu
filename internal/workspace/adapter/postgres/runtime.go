package workspacepostgres

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"github.com/jackc/pgx/v5"
)

// RegisterRuntime claims one role after the exact Workspace binding has loaded.
func (r *Repository) RegisterRuntime(ctx context.Context, registration application.RuntimeRegistration) (domain.RuntimeRecord, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.RuntimeRecord{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	state, err := loadControlState(ctx, tx, `FOR SHARE`)
	if err != nil {
		return domain.RuntimeRecord{}, err
	}
	workspace, err := loadWorkspaceForShare(ctx, tx, registration.WorkspaceID)
	if err != nil {
		return domain.RuntimeRecord{}, err
	}
	var operation *domain.SwitchOperation
	if registration.OperationID != nil {
		persisted, loadErr := loadSwitchOperation(ctx, tx, *registration.OperationID, `FOR SHARE`)
		if loadErr != nil {
			return domain.RuntimeRecord{}, loadErr
		}
		operation = &persisted
	}
	if err := authorizeRuntime(state, operation, workspace, registration); err != nil {
		return domain.RuntimeRecord{}, err
	}
	now, err := workspaceDatabaseNow(ctx, tx)
	if err != nil {
		return domain.RuntimeRecord{}, err
	}
	record, err := scanWorkspaceRuntime(tx.QueryRow(ctx, `INSERT INTO ops.workspace_runtime(
role,instance_id,workspace_id,operation_id,grant_generation,root_fingerprint,binding_version,
phase,started_at,heartbeat_at,version)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$9,1)
ON CONFLICT(role) DO UPDATE SET
instance_id=EXCLUDED.instance_id,
workspace_id=EXCLUDED.workspace_id,
operation_id=EXCLUDED.operation_id,
grant_generation=EXCLUDED.grant_generation,
root_fingerprint=EXCLUDED.root_fingerprint,
binding_version=EXCLUDED.binding_version,
phase=EXCLUDED.phase,
started_at=CASE WHEN ops.workspace_runtime.instance_id=EXCLUDED.instance_id
    THEN ops.workspace_runtime.started_at ELSE EXCLUDED.started_at END,
heartbeat_at=EXCLUDED.heartbeat_at,
version=ops.workspace_runtime.version+1
WHERE ops.workspace_runtime.instance_id<>EXCLUDED.instance_id
   OR (ops.workspace_runtime.workspace_id=EXCLUDED.workspace_id
       AND ops.workspace_runtime.operation_id IS NOT DISTINCT FROM EXCLUDED.operation_id
       AND ops.workspace_runtime.grant_generation=EXCLUDED.grant_generation
       AND ops.workspace_runtime.root_fingerprint=EXCLUDED.root_fingerprint
       AND ops.workspace_runtime.binding_version=EXCLUDED.binding_version
       AND ops.workspace_runtime.phase=EXCLUDED.phase)
RETURNING `+workspaceRuntimeColumns,
		string(registration.Role), string(registration.InstanceID), string(registration.WorkspaceID),
		nullableFoundationID(registration.OperationID), registration.GrantGeneration,
		registration.RootFingerprint, registration.BindingVersion, string(registration.Phase), now))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RuntimeRecord{}, runtimeConflict(errors.New("workspace runtime role ownership changed"))
	}
	if err != nil {
		return domain.RuntimeRecord{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	if err := commitWorkspaceTx(ctx, tx); err != nil {
		return domain.RuntimeRecord{}, err
	}
	return record, nil
}

// HeartbeatRuntime renews one exact role, instance, operation and version owner.
func (r *Repository) HeartbeatRuntime(ctx context.Context, heartbeat application.RuntimeHeartbeat) (domain.RuntimeRecord, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.RuntimeRecord{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	state, err := loadControlState(ctx, tx, `FOR SHARE`)
	if err != nil {
		return domain.RuntimeRecord{}, err
	}
	record, err := loadRuntimeForUpdate(ctx, tx, heartbeat.Role)
	if err != nil {
		return domain.RuntimeRecord{}, err
	}
	if record.InstanceID != heartbeat.InstanceID || record.Version != heartbeat.ExpectedRuntimeVersion ||
		!equalOptionalIDs(record.OperationID, heartbeat.OperationID) {
		return domain.RuntimeRecord{}, runtimeConflict(errors.New("workspace runtime heartbeat ownership changed"))
	}
	workspace, err := loadWorkspaceForShare(ctx, tx, record.WorkspaceID)
	if err != nil {
		return domain.RuntimeRecord{}, err
	}
	var operation *domain.SwitchOperation
	if record.OperationID != nil {
		persisted, loadErr := loadSwitchOperation(ctx, tx, *record.OperationID, `FOR SHARE`)
		if loadErr != nil {
			return domain.RuntimeRecord{}, loadErr
		}
		operation = &persisted
	}
	if err := authorizeRuntime(state, operation, workspace, application.RuntimeRegistration{
		Role: record.Role, InstanceID: record.InstanceID, WorkspaceID: record.WorkspaceID,
		OperationID: record.OperationID, GrantGeneration: record.GrantGeneration,
		RootFingerprint: record.RootFingerprint, BindingVersion: record.BindingVersion, Phase: record.Phase,
	}); err != nil {
		return domain.RuntimeRecord{}, err
	}
	now, err := workspaceDatabaseNow(ctx, tx)
	if err != nil {
		return domain.RuntimeRecord{}, err
	}
	updated, err := scanWorkspaceRuntime(tx.QueryRow(ctx, `UPDATE ops.workspace_runtime
SET heartbeat_at=$4,version=version+1
WHERE role=$1 AND instance_id=$2 AND version=$3
RETURNING `+workspaceRuntimeColumns,
		string(record.Role), string(record.InstanceID), record.Version, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RuntimeRecord{}, runtimeConflict(errors.New("workspace runtime heartbeat version changed"))
	}
	if err != nil {
		return domain.RuntimeRecord{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	if err := commitWorkspaceTx(ctx, tx); err != nil {
		return domain.RuntimeRecord{}, err
	}
	return updated, nil
}

// SetRuntimePhase atomically binds and unbinds a process from its controlling switch.
func (r *Repository) SetRuntimePhase(ctx context.Context, command application.RuntimePhaseCommand) (domain.RuntimeRecord, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.RuntimeRecord{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	state, err := loadControlState(ctx, tx, `FOR SHARE`)
	if err != nil {
		return domain.RuntimeRecord{}, err
	}
	record, err := loadRuntimeForUpdate(ctx, tx, command.Role)
	if err != nil {
		return domain.RuntimeRecord{}, err
	}
	if record.InstanceID != command.InstanceID || record.Version != command.ExpectedRuntimeVersion ||
		record.Phase != command.ExpectedPhase {
		return domain.RuntimeRecord{}, runtimeConflict(errors.New("workspace runtime phase ownership changed"))
	}
	var operation *domain.SwitchOperation
	if command.OperationID != nil {
		persisted, loadErr := loadSwitchOperation(ctx, tx, *command.OperationID, `FOR SHARE`)
		if loadErr != nil {
			return domain.RuntimeRecord{}, loadErr
		}
		operation = &persisted
	}
	nextOperationID, err := authorizeRuntimeTransition(state, operation, record, command)
	if err != nil {
		return domain.RuntimeRecord{}, err
	}
	now, err := workspaceDatabaseNow(ctx, tx)
	if err != nil {
		return domain.RuntimeRecord{}, err
	}
	updated, err := scanWorkspaceRuntime(tx.QueryRow(ctx, `UPDATE ops.workspace_runtime
SET operation_id=$4,phase=$5,heartbeat_at=$6,version=version+1
WHERE role=$1 AND instance_id=$2 AND version=$3
RETURNING `+workspaceRuntimeColumns,
		string(record.Role), string(record.InstanceID), record.Version,
		nullableFoundationID(nextOperationID), string(command.NextPhase), now))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RuntimeRecord{}, runtimeConflict(errors.New("workspace runtime phase version changed"))
	}
	if err != nil {
		return domain.RuntimeRecord{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	if err := commitWorkspaceTx(ctx, tx); err != nil {
		return domain.RuntimeRecord{}, err
	}
	return updated, nil
}

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

func verifySwitchRuntimes(
	ctx context.Context,
	tx pgx.Tx,
	operation domain.SwitchOperation,
	workspaceID foundation.ID,
	generation int64,
	prepared bool,
	now time.Time,
	freshWithin time.Duration,
) error {
	rows, err := tx.Query(ctx, `SELECT `+workspaceRuntimeColumns+`
FROM ops.workspace_runtime WHERE role IN ('api','worker') ORDER BY role FOR UPDATE`)
	if err != nil {
		return classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	defer rows.Close()
	seen := map[domain.RuntimeRole]bool{}
	for rows.Next() {
		runtime, scanErr := scanWorkspaceRuntime(rows)
		if scanErr != nil {
			return classifyControl(scanErr, domain.ErrorCodeControlDatabaseUnavailable)
		}
		validPhase := runtime.Phase == domain.RuntimePhaseActive && runtime.OperationID == nil
		if prepared {
			validPhase = (runtime.Phase == domain.RuntimePhasePrepared || runtime.Phase == domain.RuntimePhaseVerifying) &&
				runtime.OperationID != nil && *runtime.OperationID == operation.ID
		}
		if runtime.WorkspaceID != workspaceID || runtime.GrantGeneration != generation || !validPhase ||
			!runtimeHeartbeatFresh(runtime.HeartbeatAt, now, freshWithin) {
			return runtimeNotPrepared(errors.New("workspace runtime is not fresh for the required grant"))
		}
		seen[runtime.Role] = true
	}
	if err := rows.Err(); err != nil {
		return classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	if !seen[domain.RuntimeRoleAPI] || !seen[domain.RuntimeRoleWorker] || len(seen) != 2 {
		return runtimeNotPrepared(errors.New("both workspace runtime roles are required"))
	}
	return nil
}

func runtimeHeartbeatFresh(heartbeat, now time.Time, freshWithin time.Duration) bool {
	return !heartbeat.Before(now.Add(-freshWithin)) && !heartbeat.After(now)
}

func loadRuntimeForUpdate(ctx context.Context, tx pgx.Tx, role domain.RuntimeRole) (domain.RuntimeRecord, error) {
	record, err := scanWorkspaceRuntime(tx.QueryRow(ctx, `SELECT `+workspaceRuntimeColumns+`
FROM ops.workspace_runtime WHERE role=$1 FOR UPDATE`, string(role)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RuntimeRecord{}, runtimeConflict(errors.New("workspace runtime role is not registered"))
	}
	if err != nil {
		return domain.RuntimeRecord{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return record, nil
}

func loadWorkspaceForShare(ctx context.Context, tx pgx.Tx, id foundation.ID) (domain.Workspace, error) {
	workspace, err := scanWorkspace(tx.QueryRow(ctx, `SELECT `+workspaceColumns+`
FROM core.workspace WHERE id=$1 FOR SHARE`, string(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Workspace{}, registryNotFound(err)
	}
	if err != nil {
		return domain.Workspace{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return workspace, nil
}
