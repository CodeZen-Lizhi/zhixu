package workspacepostgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"github.com/jackc/pgx/v5"
)

type lockedSwitch struct {
	State     domain.ControlState
	Operation domain.SwitchOperation
	Gate      mutationGate
	Now       time.Time
}

// ControlSnapshot reads Registry, singleton, operation and runtime state from one snapshot.
func (r *Repository) ControlSnapshot(ctx context.Context, freshWithin time.Duration) (domain.ControlSnapshot, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.ControlSnapshot{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY`); err != nil {
		return domain.ControlSnapshot{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	state, err := loadControlState(ctx, tx, ``)
	if err != nil {
		return domain.ControlSnapshot{}, err
	}
	now, err := workspaceDatabaseNow(ctx, tx)
	if err != nil {
		return domain.ControlSnapshot{}, err
	}
	rows, err := tx.Query(ctx, `SELECT `+workspaceColumns+`
FROM core.workspace WHERE removed_at IS NULL
ORDER BY (status='active') DESC,last_opened_at DESC NULLS LAST,created_at DESC,id`)
	if err != nil {
		return domain.ControlSnapshot{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	registry := make([]domain.Workspace, 0)
	for rows.Next() {
		workspace, scanErr := scanWorkspace(rows)
		if scanErr != nil {
			rows.Close()
			return domain.ControlSnapshot{}, classifyControl(scanErr, domain.ErrorCodeControlDatabaseUnavailable)
		}
		registry = append(registry, workspace)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return domain.ControlSnapshot{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	rows.Close()

	var operation *domain.SwitchOperation
	if state.OperationID != nil {
		persisted, loadErr := loadSwitchOperation(ctx, tx, *state.OperationID, ``)
		if loadErr != nil {
			return domain.ControlSnapshot{}, loadErr
		}
		operation = &persisted
	}
	runtimeRows, err := tx.Query(ctx, `SELECT `+workspaceRuntimeColumns+`
FROM ops.workspace_runtime ORDER BY role`)
	if err != nil {
		return domain.ControlSnapshot{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	runtimes := make([]domain.RuntimeRecord, 0, 2)
	for runtimeRows.Next() {
		runtime, scanErr := scanWorkspaceRuntime(runtimeRows)
		if scanErr != nil {
			runtimeRows.Close()
			return domain.ControlSnapshot{}, classifyControl(scanErr, domain.ErrorCodeControlDatabaseUnavailable)
		}
		runtime.Fresh = runtimeHeartbeatFresh(runtime.HeartbeatAt, now, freshWithin)
		runtimes = append(runtimes, runtime)
	}
	if err := runtimeRows.Err(); err != nil {
		runtimeRows.Close()
		return domain.ControlSnapshot{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	runtimeRows.Close()

	snapshot := domain.ControlSnapshot{State: state, Registry: registry, Operation: operation, Runtimes: runtimes}
	if state.ActiveWorkspaceID != nil {
		for index := range registry {
			if registry[index].ID == *state.ActiveWorkspaceID {
				active := registry[index]
				snapshot.Active = &active
				break
			}
		}
		if snapshot.Active == nil {
			return domain.ControlSnapshot{}, controlCorrupt(errors.New("active workspace is absent from Registry"))
		}
	}
	if err := validateControlSnapshot(snapshot); err != nil {
		return domain.ControlSnapshot{}, err
	}
	if err := commitWorkspaceTx(ctx, tx); err != nil {
		return domain.ControlSnapshot{}, err
	}
	return snapshot, nil
}

// GetSwitchOperation loads one immutable operation binding and current phase.
func (r *Repository) GetSwitchOperation(ctx context.Context, operationID foundation.ID) (domain.SwitchOperation, error) {
	return loadSwitchOperation(ctx, r.db, operationID, ``)
}

// BeginSwitch claims the global mutation gate and persists one idempotent operation.
func (r *Repository) BeginSwitch(ctx context.Context, command application.BeginSwitchStoreCommand) (domain.SwitchOperation, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.SwitchOperation{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if existing, found, err := findIdempotentSwitch(ctx, tx, command.ControllerInstanceID, command.IdempotencyKey, ``); err != nil {
		return domain.SwitchOperation{}, err
	} else if found {
		return replaySwitch(ctx, tx, existing, command)
	}
	state, err := loadControlState(ctx, tx, `FOR UPDATE`)
	if err != nil {
		return domain.SwitchOperation{}, err
	}
	if existing, found, err := findIdempotentSwitch(ctx, tx, command.ControllerInstanceID, command.IdempotencyKey, `FOR UPDATE`); err != nil {
		return domain.SwitchOperation{}, err
	} else if found {
		return replaySwitch(ctx, tx, existing, command)
	}
	if state.StateVersion != command.ExpectedStateVersion {
		return domain.SwitchOperation{}, controlStateConflict(errors.New("workspace control state version changed"))
	}
	if state.OperationID != nil {
		return domain.SwitchOperation{}, switchInProgress(errors.New("another workspace switch is in progress"))
	}
	gate, err := loadMutationGate(ctx, tx, `FOR UPDATE`)
	if err != nil {
		return domain.SwitchOperation{}, err
	}
	if gate.OwnerID != nil {
		return domain.SwitchOperation{}, runtimeMutationConflict(errors.New("another runtime mutation owns the global gate"))
	}
	now, err := workspaceDatabaseNow(ctx, tx)
	if err != nil {
		return domain.SwitchOperation{}, err
	}
	if !command.Deadline.After(now) {
		return domain.SwitchOperation{}, switchLeaseExpired(errors.New("workspace switch deadline has elapsed"))
	}
	target, err := loadWorkspaceForUpdate(ctx, tx, command.TargetWorkspaceID)
	if err != nil {
		return domain.SwitchOperation{}, err
	}
	if target.Availability == domain.WorkspaceAvailabilityMigrationRequired || target.BindingVersion == 0 {
		return domain.SwitchOperation{}, migrationRequired(errors.New("target workspace root binding requires migration"))
	}
	if target.Availability != domain.WorkspaceAvailabilityAvailable || !target.RemovedAt.IsZero() {
		return domain.SwitchOperation{}, foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeWorkspaceAvailabilityInvalid, false, errors.New("target workspace is not available"))
	}
	if target.Status == domain.WorkspaceStatusActive || sameOptionalID(state.ActiveWorkspaceID, target.ID) {
		return domain.SwitchOperation{}, controlStateConflict(errors.New("target workspace is already active"))
	}
	activeID, err := lockDatabaseActiveWorkspace(ctx, tx)
	if err != nil {
		return domain.SwitchOperation{}, err
	}
	if !equalOptionalIDs(activeID, state.ActiveWorkspaceID) {
		return domain.SwitchOperation{}, controlCorrupt(errors.New("workspace control active identity differs from Registry status"))
	}
	leaseExpiresAt := now.Add(command.LeaseDuration)
	grantGeneration := state.GrantGeneration + 1
	operation, err := scanSwitchOperation(tx.QueryRow(ctx, `INSERT INTO ops.workspace_switch(
id,controller_instance_id,idempotency_key,request_hash,previous_workspace_id,target_workspace_id,
target_root_fingerprint,target_binding_version,grant_generation,recovery_generation,
expected_state_version,phase,result,lease_owner_id,lease_expires_at,heartbeat_at,deadline_at,
error_code,version,created_at,updated_at,completed_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'validating',NULL,$12,$13,$14,$15,NULL,1,$14,$14,NULL)
RETURNING `+switchOperationColumns,
		string(command.OperationID), string(command.ControllerInstanceID), command.IdempotencyKey,
		command.RequestHash, nullableFoundationID(state.ActiveWorkspaceID), string(target.ID),
		target.RootFingerprint, target.BindingVersion, grantGeneration, grantGeneration+1,
		command.ExpectedStateVersion, string(command.LeaseOwnerID), leaseExpiresAt, now, command.Deadline.UTC()))
	if err != nil {
		return domain.SwitchOperation{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	if _, err := tx.Exec(ctx, `UPDATE ops.runtime_mutation_gate
SET owner_kind='workspace_switch',owner_id=$1,lease_expires_at=$2,
    version=version+1,updated_at=$3
WHERE singleton=true AND owner_id IS NULL`, string(operation.ID), leaseExpiresAt, now); err != nil {
		return domain.SwitchOperation{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	updatedState, err := scanControlState(tx.QueryRow(ctx, `UPDATE ops.workspace_control_state
SET operation_id=$1,operation_phase='validating',target_workspace_id=$2,
    previous_active_workspace_id=$3,controller_lease_owner_id=$4,
    controller_lease_expires_at=$5,last_error_code=NULL,
    state_version=state_version+1,updated_at=$6
WHERE singleton=true AND state_version=$7 AND operation_id IS NULL
RETURNING `+controlStateColumns,
		string(operation.ID), string(target.ID), nullableFoundationID(state.ActiveWorkspaceID),
		string(command.LeaseOwnerID), leaseExpiresAt, now, command.ExpectedStateVersion))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SwitchOperation{}, controlStateConflict(errors.New("workspace control state changed"))
	}
	if err != nil {
		return domain.SwitchOperation{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	if updatedState.OperationID == nil || *updatedState.OperationID != operation.ID {
		return domain.SwitchOperation{}, controlCorrupt(errors.New("workspace operation was not installed in control state"))
	}
	if err := commitWorkspaceTx(ctx, tx); err != nil {
		return domain.SwitchOperation{}, err
	}
	return operation, nil
}

// RenewSwitch renews operation, singleton and mutation-gate leases in one transaction.
func (r *Repository) RenewSwitch(ctx context.Context, command application.RenewSwitchCommand) (domain.SwitchOperation, error) {
	return r.mutateSwitchPhase(ctx, command.OperationID, command.LeaseOwnerID,
		command.ExpectedPhase, command.ExpectedPhase, command.ExpectedOperationVersion,
		command.ExpectedStateVersion, command.LeaseDuration)
}

// AdvanceSwitch performs one legal phase transition while preserving immutable binding.
func (r *Repository) AdvanceSwitch(ctx context.Context, command application.AdvanceSwitchCommand) (domain.SwitchOperation, error) {
	if err := domain.ValidateSwitchTransition(command.ExpectedPhase, command.NextPhase); err != nil {
		return domain.SwitchOperation{}, err
	}
	return r.mutateSwitchPhase(ctx, command.OperationID, command.LeaseOwnerID,
		command.ExpectedPhase, command.NextPhase, command.ExpectedOperationVersion,
		command.ExpectedStateVersion, command.LeaseDuration)
}

func (r *Repository) mutateSwitchPhase(
	ctx context.Context,
	operationID, ownerID foundation.ID,
	expectedPhase, nextPhase domain.SwitchPhase,
	expectedOperationVersion, expectedStateVersion int64,
	leaseDuration time.Duration,
) (domain.SwitchOperation, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.SwitchOperation{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	locked, err := lockOwnedSwitch(ctx, tx, operationID, ownerID, expectedPhase,
		expectedOperationVersion, expectedStateVersion, true)
	if err != nil {
		return domain.SwitchOperation{}, err
	}
	operation, _, err := renewLockedSwitch(ctx, tx, locked, nextPhase, leaseDuration)
	if err != nil {
		return domain.SwitchOperation{}, err
	}
	if err := commitWorkspaceTx(ctx, tx); err != nil {
		return domain.SwitchOperation{}, err
	}
	return operation, nil
}

// TakeOverSwitch claims a stale lease only after the prior database lease expires.
func (r *Repository) TakeOverSwitch(ctx context.Context, command application.TakeOverSwitchCommand) (domain.SwitchOperation, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.SwitchOperation{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	state, err := loadControlState(ctx, tx, `FOR UPDATE`)
	if err != nil {
		return domain.SwitchOperation{}, err
	}
	operation, err := loadSwitchOperation(ctx, tx, command.OperationID, `FOR UPDATE`)
	if err != nil {
		return domain.SwitchOperation{}, err
	}
	gate, err := loadMutationGate(ctx, tx, `FOR UPDATE`)
	if err != nil {
		return domain.SwitchOperation{}, err
	}
	now, err := workspaceDatabaseNow(ctx, tx)
	if err != nil {
		return domain.SwitchOperation{}, err
	}
	if operation.Result != "" || operation.Phase != command.ExpectedPhase ||
		operation.Version != command.ExpectedOperationVersion || state.StateVersion != command.ExpectedStateVersion ||
		state.OperationID == nil || *state.OperationID != operation.ID || state.OperationPhase != operation.Phase {
		return domain.SwitchOperation{}, controlStateConflict(errors.New("workspace switch takeover state changed"))
	}
	if operation.LeaseOwnerID == nil || state.ControllerLeaseOwnerID == nil ||
		*operation.LeaseOwnerID != *state.ControllerLeaseOwnerID ||
		!state.ControllerLeaseExpiresAt.Equal(operation.LeaseExpiresAt) ||
		gate.OwnerKind != "workspace_switch" || gate.OwnerID == nil || *gate.OwnerID != operation.ID ||
		!gate.LeaseExpiresAt.Equal(operation.LeaseExpiresAt) {
		return domain.SwitchOperation{}, controlCorrupt(errors.New("workspace switch takeover lease facts are inconsistent"))
	}
	if operation.LeaseExpiresAt.After(now) {
		return domain.SwitchOperation{}, switchLeaseHeld(errors.New("workspace switch lease has not expired"))
	}
	// The operation deadline stops forward orchestration in the Controller. It
	// must not prevent a successor from renewing the mutation gate while
	// revocation or rollback is still incomplete.
	leaseExpiresAt := now.Add(command.LeaseDuration)
	operation, err = scanSwitchOperation(tx.QueryRow(ctx, `UPDATE ops.workspace_switch
SET lease_owner_id=$2,lease_expires_at=$3,heartbeat_at=$4,
    version=version+1,updated_at=$4
WHERE id=$1 AND version=$5 AND result IS NULL
RETURNING `+switchOperationColumns,
		string(operation.ID), string(command.NewLeaseOwnerID), leaseExpiresAt, now, command.ExpectedOperationVersion))
	if err != nil {
		return domain.SwitchOperation{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	if _, err := scanControlState(tx.QueryRow(ctx, `UPDATE ops.workspace_control_state
SET controller_lease_owner_id=$2,controller_lease_expires_at=$3,
    state_version=state_version+1,updated_at=$4
WHERE singleton=true AND operation_id=$1 AND state_version=$5
RETURNING `+controlStateColumns,
		string(operation.ID), string(command.NewLeaseOwnerID), leaseExpiresAt, now, command.ExpectedStateVersion)); err != nil {
		return domain.SwitchOperation{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	if err := renewMutationGate(ctx, tx, operation.ID, leaseExpiresAt, now); err != nil {
		return domain.SwitchOperation{}, err
	}
	if err := commitWorkspaceTx(ctx, tx); err != nil {
		return domain.SwitchOperation{}, err
	}
	return operation, nil
}

// RevokeActiveWorkspace clears old active only while the exact revoking lease is held.
func (r *Repository) RevokeActiveWorkspace(ctx context.Context, command application.RevokeActiveCommand) (domain.ControlState, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.ControlState{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	locked, err := lockOwnedSwitch(ctx, tx, command.OperationID, command.LeaseOwnerID,
		domain.SwitchPhaseRevoking, command.ExpectedOperationVersion, command.ExpectedStateVersion, true)
	if err != nil {
		return domain.ControlState{}, err
	}
	if !equalOptionalIDs(locked.State.ActiveWorkspaceID, locked.Operation.PreviousWorkspaceID) {
		return domain.ControlState{}, controlCorrupt(errors.New("previous active workspace ownership changed before revoke"))
	}
	if locked.Operation.PreviousWorkspaceID != nil {
		result, updateErr := tx.Exec(ctx, `UPDATE core.workspace
SET status='inactive',version=version+1,updated_at=GREATEST(updated_at,$2)
WHERE id=$1 AND status='active'`, string(*locked.Operation.PreviousWorkspaceID), locked.Now)
		if updateErr != nil {
			return domain.ControlState{}, classifyControl(updateErr, domain.ErrorCodeControlDatabaseUnavailable)
		}
		if result.RowsAffected() != 1 {
			return domain.ControlState{}, controlStateConflict(errors.New("previous active workspace was not revocable"))
		}
	}
	leaseExpiresAt := locked.Now.Add(command.LeaseDuration)
	if _, err := renewOperationOnly(ctx, tx, locked.Operation, domain.SwitchPhaseRevoking, leaseExpiresAt, locked.Now); err != nil {
		return domain.ControlState{}, err
	}
	state, err := scanControlState(tx.QueryRow(ctx, `UPDATE ops.workspace_control_state
SET active_workspace_id=NULL,resume_workspace_id=$2,
    controller_lease_expires_at=$3,state_version=state_version+1,updated_at=$4
WHERE singleton=true AND operation_id=$1 AND state_version=$5
RETURNING `+controlStateColumns,
		string(locked.Operation.ID), nullableFoundationID(locked.Operation.PreviousWorkspaceID),
		leaseExpiresAt, locked.Now, command.ExpectedStateVersion))
	if err != nil {
		return domain.ControlState{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	if err := renewMutationGate(ctx, tx, locked.Operation.ID, leaseExpiresAt, locked.Now); err != nil {
		return domain.ControlState{}, err
	}
	if err := commitWorkspaceTx(ctx, tx); err != nil {
		return domain.ControlState{}, err
	}
	return state, nil
}

// CommitTargetWorkspace publishes target only after both candidate roles are fresh and prepared.
func (r *Repository) CommitTargetWorkspace(ctx context.Context, command application.CommitTargetCommand) (domain.ControlState, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.ControlState{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	locked, err := lockOwnedSwitch(ctx, tx, command.OperationID, command.LeaseOwnerID,
		domain.SwitchPhaseCommitting, command.ExpectedOperationVersion, command.ExpectedStateVersion, true)
	if err != nil {
		return domain.ControlState{}, err
	}
	if locked.State.ActiveWorkspaceID != nil {
		return domain.ControlState{}, controlStateConflict(errors.New("old workspace is still active at target commit"))
	}
	if err := verifySwitchRuntimes(ctx, tx, locked.Operation, locked.Operation.TargetWorkspaceID,
		locked.Operation.GrantGeneration, true, locked.Now, command.RuntimeFreshWithin); err != nil {
		return domain.ControlState{}, err
	}
	target, err := loadWorkspaceForUpdate(ctx, tx, locked.Operation.TargetWorkspaceID)
	if err != nil {
		return domain.ControlState{}, err
	}
	if target.Availability != domain.WorkspaceAvailabilityAvailable || !target.RemovedAt.IsZero() ||
		target.RootFingerprint != locked.Operation.TargetRootFingerprint || target.BindingVersion != locked.Operation.TargetBindingVersion {
		return domain.ControlState{}, identityConflict(errors.New("target workspace binding changed before commit"))
	}
	result, err := tx.Exec(ctx, `UPDATE core.workspace
SET status='active',last_opened_at=$2,version=version+1,updated_at=GREATEST(updated_at,$2)
WHERE id=$1 AND status='inactive' AND availability='available' AND removed_at IS NULL`,
		string(target.ID), locked.Now)
	if err != nil {
		return domain.ControlState{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	if result.RowsAffected() != 1 {
		return domain.ControlState{}, controlStateConflict(errors.New("target workspace was not commit-ready"))
	}
	leaseExpiresAt := locked.Now.Add(command.LeaseDuration)
	if _, err := renewOperationOnly(ctx, tx, locked.Operation, domain.SwitchPhaseCommitting, leaseExpiresAt, locked.Now); err != nil {
		return domain.ControlState{}, err
	}
	state, err := scanControlState(tx.QueryRow(ctx, `UPDATE ops.workspace_control_state
SET active_workspace_id=$2,resume_workspace_id=$2,grant_generation=$3,
    controller_lease_expires_at=$4,state_version=state_version+1,updated_at=$5
WHERE singleton=true AND operation_id=$1 AND state_version=$6
RETURNING `+controlStateColumns,
		string(locked.Operation.ID), string(target.ID), locked.Operation.GrantGeneration,
		leaseExpiresAt, locked.Now, command.ExpectedStateVersion))
	if err != nil {
		return domain.ControlState{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	if err := renewMutationGate(ctx, tx, locked.Operation.ID, leaseExpiresAt, locked.Now); err != nil {
		return domain.ControlState{}, err
	}
	if err := commitWorkspaceTx(ctx, tx); err != nil {
		return domain.ControlState{}, err
	}
	return state, nil
}

// RestorePreviousWorkspace publishes the exact previous identity after recovery readiness.
func (r *Repository) RestorePreviousWorkspace(ctx context.Context, command application.RestorePreviousCommand) (domain.ControlState, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.ControlState{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	locked, err := lockOwnedSwitch(ctx, tx, command.OperationID, command.LeaseOwnerID,
		domain.SwitchPhaseRecovering, command.ExpectedOperationVersion, command.ExpectedStateVersion, true)
	if err != nil {
		return domain.ControlState{}, err
	}
	if locked.Operation.PreviousWorkspaceID == nil {
		return domain.ControlState{}, controlStateConflict(errors.New("first workspace switch has no previous identity to restore"))
	}
	if err := verifySwitchRuntimes(ctx, tx, locked.Operation, *locked.Operation.PreviousWorkspaceID,
		locked.Operation.RecoveryGeneration, true, locked.Now, command.RuntimeFreshWithin); err != nil {
		return domain.ControlState{}, err
	}
	previous, err := loadWorkspaceForUpdate(ctx, tx, *locked.Operation.PreviousWorkspaceID)
	if err != nil {
		return domain.ControlState{}, err
	}
	if previous.Availability != domain.WorkspaceAvailabilityAvailable || !previous.RemovedAt.IsZero() || previous.BindingVersion < 1 {
		return domain.ControlState{}, identityConflict(errors.New("previous workspace is no longer recoverable"))
	}
	if locked.State.ActiveWorkspaceID != nil {
		if *locked.State.ActiveWorkspaceID != locked.Operation.TargetWorkspaceID {
			return domain.ControlState{}, controlCorrupt(errors.New("unexpected workspace is active during recovery"))
		}
		result, updateErr := tx.Exec(ctx, `UPDATE core.workspace
SET status='inactive',version=version+1,updated_at=GREATEST(updated_at,$2)
WHERE id=$1 AND status='active'`, string(*locked.State.ActiveWorkspaceID), locked.Now)
		if updateErr != nil {
			return domain.ControlState{}, classifyControl(updateErr, domain.ErrorCodeControlDatabaseUnavailable)
		}
		if result.RowsAffected() != 1 {
			return domain.ControlState{}, controlStateConflict(errors.New("failed target was not revocable"))
		}
	}
	result, err := tx.Exec(ctx, `UPDATE core.workspace
SET status='active',last_opened_at=$2,version=version+1,updated_at=GREATEST(updated_at,$2)
WHERE id=$1 AND status='inactive' AND availability='available' AND removed_at IS NULL`,
		string(previous.ID), locked.Now)
	if err != nil {
		return domain.ControlState{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	if result.RowsAffected() != 1 {
		return domain.ControlState{}, controlStateConflict(errors.New("previous workspace was not recoverable"))
	}
	leaseExpiresAt := locked.Now.Add(command.LeaseDuration)
	if _, err := renewOperationOnly(ctx, tx, locked.Operation, domain.SwitchPhaseRecovering, leaseExpiresAt, locked.Now); err != nil {
		return domain.ControlState{}, err
	}
	state, err := scanControlState(tx.QueryRow(ctx, `UPDATE ops.workspace_control_state
SET active_workspace_id=$2,resume_workspace_id=$2,grant_generation=$3,
    controller_lease_expires_at=$4,state_version=state_version+1,updated_at=$5
WHERE singleton=true AND operation_id=$1 AND state_version=$6
RETURNING `+controlStateColumns,
		string(locked.Operation.ID), string(previous.ID), locked.Operation.RecoveryGeneration,
		leaseExpiresAt, locked.Now, command.ExpectedStateVersion))
	if err != nil {
		return domain.ControlState{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	if err := renewMutationGate(ctx, tx, locked.Operation.ID, leaseExpiresAt, locked.Now); err != nil {
		return domain.ControlState{}, err
	}
	if err := commitWorkspaceTx(ctx, tx); err != nil {
		return domain.ControlState{}, err
	}
	return state, nil
}

// FinishSwitch records a terminal result and releases operation and mutation ownership.
func (r *Repository) FinishSwitch(ctx context.Context, command application.FinishSwitchCommand) (domain.SwitchOperation, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.SwitchOperation{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	locked, err := lockOwnedSwitchAnyPhase(ctx, tx, command.OperationID, command.LeaseOwnerID,
		command.ExpectedOperationVersion, command.ExpectedStateVersion, true)
	if err != nil {
		return domain.SwitchOperation{}, err
	}
	if err := domain.ValidateTerminalTransition(locked.Operation.Phase, command.Result, command.ErrorCode); err != nil {
		return domain.SwitchOperation{}, err
	}
	switch command.Result {
	case domain.SwitchResultSucceeded:
		if !sameOptionalID(locked.State.ActiveWorkspaceID, locked.Operation.TargetWorkspaceID) ||
			locked.State.GrantGeneration != locked.Operation.GrantGeneration {
			return domain.SwitchOperation{}, controlStateConflict(errors.New("target workspace is not the committed grant"))
		}
		if err := verifySwitchRuntimes(ctx, tx, locked.Operation, locked.Operation.TargetWorkspaceID,
			locked.Operation.GrantGeneration, false, locked.Now, command.RuntimeFreshWithin); err != nil {
			return domain.SwitchOperation{}, err
		}
	case domain.SwitchResultRolledBack:
		if locked.Operation.PreviousWorkspaceID == nil ||
			!sameOptionalID(locked.State.ActiveWorkspaceID, *locked.Operation.PreviousWorkspaceID) ||
			locked.State.GrantGeneration != locked.Operation.RecoveryGeneration {
			return domain.SwitchOperation{}, controlStateConflict(errors.New("previous workspace is not the recovered grant"))
		}
		if err := verifySwitchRuntimes(ctx, tx, locked.Operation, *locked.Operation.PreviousWorkspaceID,
			locked.Operation.RecoveryGeneration, false, locked.Now, command.RuntimeFreshWithin); err != nil {
			return domain.SwitchOperation{}, err
		}
	case domain.SwitchResultRejected, domain.SwitchResultCancelled:
		if !equalOptionalIDs(locked.State.ActiveWorkspaceID, locked.Operation.PreviousWorkspaceID) ||
			locked.State.GrantGeneration != locked.Operation.GrantGeneration-1 {
			return domain.SwitchOperation{}, controlStateConflict(errors.New("pre-revoke terminal state changed active grant"))
		}
	case domain.SwitchResultFailed:
		if locked.State.ActiveWorkspaceID != nil {
			result, updateErr := tx.Exec(ctx, `UPDATE core.workspace
SET status='inactive',version=version+1,updated_at=GREATEST(updated_at,$2)
WHERE id=$1 AND status='active'`, string(*locked.State.ActiveWorkspaceID), locked.Now)
			if updateErr != nil {
				return domain.SwitchOperation{}, classifyControl(updateErr, domain.ErrorCodeControlDatabaseUnavailable)
			}
			if result.RowsAffected() != 1 {
				return domain.SwitchOperation{}, controlCorrupt(errors.New("failed recovery could not clear active workspace"))
			}
		}
	}
	operation, err := scanSwitchOperation(tx.QueryRow(ctx, `UPDATE ops.workspace_switch
SET result=$2,lease_owner_id=NULL,lease_expires_at=NULL,heartbeat_at=$3,
    error_code=$4,version=version+1,updated_at=$3,completed_at=$3
WHERE id=$1 AND version=$5 AND result IS NULL
RETURNING `+switchOperationColumns,
		string(locked.Operation.ID), string(command.Result), locked.Now,
		nullableText(command.ErrorCode), command.ExpectedOperationVersion))
	if err != nil {
		return domain.SwitchOperation{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	activeWorkspaceID := nullableFoundationID(locked.State.ActiveWorkspaceID)
	resumeWorkspaceID := nullableFoundationID(locked.State.ResumeWorkspaceID)
	if command.Result == domain.SwitchResultFailed {
		activeWorkspaceID = nil
		resumeWorkspaceID = nullableFoundationID(locked.Operation.PreviousWorkspaceID)
	}
	if _, err := scanControlState(tx.QueryRow(ctx, `UPDATE ops.workspace_control_state
SET active_workspace_id=$2,resume_workspace_id=$3,
    operation_id=NULL,operation_phase=NULL,target_workspace_id=NULL,
    previous_active_workspace_id=NULL,controller_lease_owner_id=NULL,
    controller_lease_expires_at=NULL,last_error_code=$4,
    state_version=state_version+1,updated_at=$5
WHERE singleton=true AND operation_id=$1 AND state_version=$6
RETURNING `+controlStateColumns,
		string(locked.Operation.ID), activeWorkspaceID, resumeWorkspaceID,
		nullableText(command.ErrorCode), locked.Now, command.ExpectedStateVersion)); err != nil {
		return domain.SwitchOperation{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	result, err := tx.Exec(ctx, `UPDATE ops.runtime_mutation_gate
SET owner_kind=NULL,owner_id=NULL,lease_expires_at=NULL,
    version=version+1,updated_at=$2
WHERE singleton=true AND owner_kind='workspace_switch' AND owner_id=$1`,
		string(locked.Operation.ID), locked.Now)
	if err != nil {
		return domain.SwitchOperation{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	if result.RowsAffected() != 1 {
		return domain.SwitchOperation{}, controlCorrupt(errors.New("workspace mutation gate was not released"))
	}
	if err := commitWorkspaceTx(ctx, tx); err != nil {
		return domain.SwitchOperation{}, err
	}
	return operation, nil
}

func lockOwnedSwitch(
	ctx context.Context,
	tx pgx.Tx,
	operationID, ownerID foundation.ID,
	expectedPhase domain.SwitchPhase,
	expectedOperationVersion, expectedStateVersion int64,
	requireFresh bool,
) (lockedSwitch, error) {
	locked, err := lockOwnedSwitchAnyPhase(ctx, tx, operationID, ownerID,
		expectedOperationVersion, expectedStateVersion, requireFresh)
	if err != nil {
		return lockedSwitch{}, err
	}
	if locked.Operation.Phase != expectedPhase || locked.State.OperationPhase != expectedPhase {
		return lockedSwitch{}, switchPhaseConflict(errors.New("workspace switch phase changed"))
	}
	return locked, nil
}

func lockOwnedSwitchAnyPhase(
	ctx context.Context,
	tx pgx.Tx,
	operationID, ownerID foundation.ID,
	expectedOperationVersion, expectedStateVersion int64,
	requireFresh bool,
) (lockedSwitch, error) {
	state, err := loadControlState(ctx, tx, `FOR UPDATE`)
	if err != nil {
		return lockedSwitch{}, err
	}
	operation, err := loadSwitchOperation(ctx, tx, operationID, `FOR UPDATE`)
	if err != nil {
		return lockedSwitch{}, err
	}
	gate, err := loadMutationGate(ctx, tx, `FOR UPDATE`)
	if err != nil {
		return lockedSwitch{}, err
	}
	now, err := workspaceDatabaseNow(ctx, tx)
	if err != nil {
		return lockedSwitch{}, err
	}
	if operation.Result != "" || operation.Version != expectedOperationVersion {
		return lockedSwitch{}, controlStateConflict(errors.New("workspace switch operation version changed"))
	}
	if state.StateVersion != expectedStateVersion || state.OperationID == nil || *state.OperationID != operation.ID ||
		state.TargetWorkspaceID == nil || *state.TargetWorkspaceID != operation.TargetWorkspaceID ||
		!equalOptionalIDs(state.PreviousActiveWorkspaceID, operation.PreviousWorkspaceID) {
		return lockedSwitch{}, controlStateConflict(errors.New("workspace control operation binding changed"))
	}
	if operation.LeaseOwnerID == nil || *operation.LeaseOwnerID != ownerID ||
		state.ControllerLeaseOwnerID == nil || *state.ControllerLeaseOwnerID != ownerID {
		if operation.LeaseExpiresAt.After(now) {
			return lockedSwitch{}, switchLeaseHeld(errors.New("workspace switch lease belongs to another owner"))
		}
		return lockedSwitch{}, switchLeaseExpired(errors.New("workspace switch lease ownership expired"))
	}
	if requireFresh && (!operation.LeaseExpiresAt.After(now) || !state.ControllerLeaseExpiresAt.After(now)) {
		return lockedSwitch{}, switchLeaseExpired(errors.New("workspace switch lease expired"))
	}
	// Existing operations remain renewable after their forward deadline so
	// safety recovery can retain exclusive mutation ownership until revocation
	// and rollback reach a durable terminal state.
	if gate.OwnerKind != "workspace_switch" || gate.OwnerID == nil || *gate.OwnerID != operation.ID ||
		!gate.LeaseExpiresAt.Equal(operation.LeaseExpiresAt) || !state.ControllerLeaseExpiresAt.Equal(operation.LeaseExpiresAt) {
		return lockedSwitch{}, controlCorrupt(errors.New("workspace mutation leases are inconsistent"))
	}
	return lockedSwitch{State: state, Operation: operation, Gate: gate, Now: now}, nil
}

func renewLockedSwitch(
	ctx context.Context,
	tx pgx.Tx,
	locked lockedSwitch,
	nextPhase domain.SwitchPhase,
	leaseDuration time.Duration,
) (domain.SwitchOperation, domain.ControlState, error) {
	leaseExpiresAt := locked.Now.Add(leaseDuration)
	operation, err := renewOperationOnly(ctx, tx, locked.Operation, nextPhase, leaseExpiresAt, locked.Now)
	if err != nil {
		return domain.SwitchOperation{}, domain.ControlState{}, err
	}
	updatedState, err := scanControlState(tx.QueryRow(ctx, `UPDATE ops.workspace_control_state
SET operation_phase=$2,controller_lease_expires_at=$3,
    state_version=state_version+1,updated_at=$4
WHERE singleton=true AND operation_id=$1 AND state_version=$5
RETURNING `+controlStateColumns,
		string(operation.ID), string(nextPhase), leaseExpiresAt, locked.Now, locked.State.StateVersion))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SwitchOperation{}, domain.ControlState{}, controlStateConflict(errors.New("workspace control state changed"))
	}
	if err != nil {
		return domain.SwitchOperation{}, domain.ControlState{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	if err := renewMutationGate(ctx, tx, operation.ID, leaseExpiresAt, locked.Now); err != nil {
		return domain.SwitchOperation{}, domain.ControlState{}, err
	}
	return operation, updatedState, nil
}

func renewOperationOnly(
	ctx context.Context,
	tx pgx.Tx,
	operation domain.SwitchOperation,
	nextPhase domain.SwitchPhase,
	leaseExpiresAt, now time.Time,
) (domain.SwitchOperation, error) {
	updated, err := scanSwitchOperation(tx.QueryRow(ctx, `UPDATE ops.workspace_switch
SET phase=$2,lease_expires_at=$3,heartbeat_at=$4,version=version+1,updated_at=$4
WHERE id=$1 AND version=$5 AND result IS NULL
RETURNING `+switchOperationColumns,
		string(operation.ID), string(nextPhase), leaseExpiresAt, now, operation.Version))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SwitchOperation{}, controlStateConflict(errors.New("workspace switch operation changed"))
	}
	if err != nil {
		return domain.SwitchOperation{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return updated, nil
}

func renewMutationGate(ctx context.Context, tx pgx.Tx, operationID foundation.ID, leaseExpiresAt, now time.Time) error {
	result, err := tx.Exec(ctx, `UPDATE ops.runtime_mutation_gate
SET lease_expires_at=$2,version=version+1,updated_at=$3
WHERE singleton=true AND owner_kind='workspace_switch' AND owner_id=$1`,
		string(operationID), leaseExpiresAt, now)
	if err != nil {
		return classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	if result.RowsAffected() != 1 {
		return controlCorrupt(errors.New("workspace mutation gate ownership changed"))
	}
	return nil
}

func loadControlState(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, suffix string) (domain.ControlState, error) {
	state, err := scanControlState(queryer.QueryRow(ctx, `SELECT `+controlStateColumns+`
FROM ops.workspace_control_state WHERE singleton=true `+suffix))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ControlState{}, controlCorrupt(errors.New("workspace control singleton is missing"))
	}
	if err != nil {
		return domain.ControlState{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return state, nil
}

func loadSwitchOperation(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id foundation.ID, suffix string) (domain.SwitchOperation, error) {
	operation, err := scanSwitchOperation(queryer.QueryRow(ctx, `SELECT `+switchOperationColumns+`
FROM ops.workspace_switch WHERE id=$1 `+suffix, string(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SwitchOperation{}, switchNotFound(err)
	}
	if err != nil {
		return domain.SwitchOperation{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return operation, nil
}

func findIdempotentSwitch(
	ctx context.Context,
	queryer interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
	controllerID foundation.ID,
	key, suffix string,
) (domain.SwitchOperation, bool, error) {
	operation, err := scanSwitchOperation(queryer.QueryRow(ctx, `SELECT `+switchOperationColumns+`
FROM ops.workspace_switch
WHERE controller_instance_id=$1 AND idempotency_key=$2 `+suffix,
		string(controllerID), key))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SwitchOperation{}, false, nil
	}
	if err != nil {
		return domain.SwitchOperation{}, false, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return operation, true, nil
}

func replaySwitch(
	ctx context.Context,
	tx pgx.Tx,
	operation domain.SwitchOperation,
	command application.BeginSwitchStoreCommand,
) (domain.SwitchOperation, error) {
	if operation.RequestHash != command.RequestHash || operation.TargetWorkspaceID != command.TargetWorkspaceID {
		return domain.SwitchOperation{}, switchIdempotencyConflict(errors.New("idempotency key was used for another workspace switch request"))
	}
	if err := commitWorkspaceTx(ctx, tx); err != nil {
		return domain.SwitchOperation{}, err
	}
	return operation, nil
}

func loadMutationGate(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, suffix string) (mutationGate, error) {
	gate, err := scanMutationGate(queryer.QueryRow(ctx, `SELECT owner_kind,owner_id::text,
lease_expires_at,version,updated_at
FROM ops.runtime_mutation_gate WHERE singleton=true `+suffix))
	if errors.Is(err, pgx.ErrNoRows) {
		return mutationGate{}, controlCorrupt(errors.New("runtime mutation singleton is missing"))
	}
	if err != nil {
		return mutationGate{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return gate, nil
}

func lockDatabaseActiveWorkspace(ctx context.Context, tx pgx.Tx) (*foundation.ID, error) {
	var value string
	err := tx.QueryRow(ctx, `SELECT id::text FROM core.workspace WHERE status='active' FOR UPDATE`).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	parsed, err := foundation.ParseID(value)
	if err != nil {
		return nil, controlCorrupt(fmt.Errorf("parse active workspace id: %w", err))
	}
	return &parsed, nil
}

func workspaceDatabaseNow(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (time.Time, error) {
	var now time.Time
	if err := queryer.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return time.Time{}, classifyControl(err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return now.UTC(), nil
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

var _ application.ControlStore = (*Repository)(nil)
