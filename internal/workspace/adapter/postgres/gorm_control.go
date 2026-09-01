package workspacepostgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"gorm.io/gorm"
)

// ControlSnapshot reads Registry, singleton, operation and runtime state from one repeatable-read snapshot.
func (r *GORMRepository) ControlSnapshot(ctx context.Context, freshWithin time.Duration) (snapshot domain.ControlSnapshot, err error) {
	err = r.within(ctx, foundation.TransactionOptions{Isolation: foundation.TransactionIsolationRepeatableRead, ReadOnly: true}, func(txCtx context.Context, tx *gorm.DB, _ foundation.TransactionScope) error {
		snapshot.Registry = make([]domain.Workspace, 0)
		snapshot.Runtimes = make([]domain.RuntimeRecord, 0, 2)
		state, e := gormLoadControlState(txCtx, tx, "")
		if e != nil {
			return e
		}
		now, e := gormWorkspaceNow(txCtx, tx)
		if e != nil {
			return e
		}
		rows, e := gormWorkspaceRows(tx, `SELECT `+workspaceColumns+` FROM core.workspace WHERE removed_at IS NULL ORDER BY (status='active') DESC,last_opened_at DESC NULLS LAST,created_at DESC,id`)
		if e != nil {
			return classifyGORMControl(txCtx, e, domain.ErrorCodeControlDatabaseUnavailable)
		}
		defer rows.Close()
		for rows.Next() {
			workspace, scanErr := scanWorkspace(rows)
			if scanErr != nil {
				return classifyGORMControl(txCtx, scanErr, domain.ErrorCodeControlDatabaseUnavailable)
			}
			snapshot.Registry = append(snapshot.Registry, workspace)
		}
		if e = rows.Err(); e != nil {
			return classifyGORMControl(txCtx, e, domain.ErrorCodeControlDatabaseUnavailable)
		}
		snapshot.State = state
		if state.OperationID != nil {
			operation, loadErr := gormLoadSwitchOperation(txCtx, tx, *state.OperationID, "")
			if loadErr != nil {
				return loadErr
			}
			snapshot.Operation = &operation
		}
		runtimeRows, e := gormWorkspaceRows(tx, `SELECT `+workspaceRuntimeColumns+` FROM ops.workspace_runtime ORDER BY role`)
		if e != nil {
			return classifyGORMControl(txCtx, e, domain.ErrorCodeControlDatabaseUnavailable)
		}
		defer runtimeRows.Close()
		for runtimeRows.Next() {
			record, scanErr := scanWorkspaceRuntime(runtimeRows)
			if scanErr != nil {
				return classifyGORMControl(txCtx, scanErr, domain.ErrorCodeControlDatabaseUnavailable)
			}
			record.Fresh = runtimeHeartbeatFresh(record.HeartbeatAt, now, freshWithin)
			snapshot.Runtimes = append(snapshot.Runtimes, record)
		}
		if e = runtimeRows.Err(); e != nil {
			return classifyGORMControl(txCtx, e, domain.ErrorCodeControlDatabaseUnavailable)
		}
		if state.ActiveWorkspaceID != nil {
			for i := range snapshot.Registry {
				if snapshot.Registry[i].ID == *state.ActiveWorkspaceID {
					active := snapshot.Registry[i]
					snapshot.Active = &active
					break
				}
			}
			if snapshot.Active == nil {
				return controlCorrupt(errors.New("active workspace is absent from Registry"))
			}
		}
		return validateControlSnapshot(snapshot)
	})
	if err != nil {
		return domain.ControlSnapshot{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return snapshot, nil
}

func (r *GORMRepository) GetSwitchOperation(ctx context.Context, operationID foundation.ID) (operation domain.SwitchOperation, err error) {
	if err = r.ready(ctx); err != nil {
		return operation, err
	}
	operation, err = gormLoadSwitchOperation(ctx, r.database.WithContext(ctx), operationID, "")
	if err != nil {
		return domain.SwitchOperation{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return operation, nil
}

// BeginSwitch claims the global mutation gate and persists one idempotent operation.
func (r *GORMRepository) BeginSwitch(ctx context.Context, command application.BeginSwitchStoreCommand) (operation domain.SwitchOperation, err error) {
	err = r.within(ctx, foundation.TransactionOptions{}, func(txCtx context.Context, tx *gorm.DB, _ foundation.TransactionScope) error {
		if existing, found, e := gormFindIdempotentSwitch(txCtx, tx, command.ControllerInstanceID, command.IdempotencyKey, ""); e != nil {
			return e
		} else if found {
			if existing.RequestHash != command.RequestHash || existing.TargetWorkspaceID != command.TargetWorkspaceID {
				return switchIdempotencyConflict(errors.New("idempotency key was used for another workspace switch request"))
			}
			operation = existing
			return nil
		}
		state, e := gormLoadControlState(txCtx, tx, "FOR UPDATE")
		if e != nil {
			return e
		}
		if existing, found, e := gormFindIdempotentSwitch(txCtx, tx, command.ControllerInstanceID, command.IdempotencyKey, "FOR UPDATE"); e != nil {
			return e
		} else if found {
			if existing.RequestHash != command.RequestHash || existing.TargetWorkspaceID != command.TargetWorkspaceID {
				return switchIdempotencyConflict(errors.New("idempotency key was used for another workspace switch request"))
			}
			operation = existing
			return nil
		}
		if state.StateVersion != command.ExpectedStateVersion {
			return controlStateConflict(errors.New("workspace control state version changed"))
		}
		if state.OperationID != nil {
			return switchInProgress(errors.New("another workspace switch is in progress"))
		}
		gate, e := gormLoadMutationGate(txCtx, tx, "FOR UPDATE")
		if e != nil {
			return e
		}
		if gate.OwnerID != nil {
			return runtimeMutationConflict(errors.New("another runtime mutation owns the global gate"))
		}
		now, e := gormWorkspaceNow(txCtx, tx)
		if e != nil {
			return e
		}
		if !command.Deadline.After(now) {
			return switchLeaseExpired(errors.New("workspace switch deadline has elapsed"))
		}
		target, e := gormLoadWorkspaceForUpdate(txCtx, tx, command.TargetWorkspaceID)
		if e != nil {
			return e
		}
		if target.Availability == domain.WorkspaceAvailabilityMigrationRequired || target.BindingVersion == 0 {
			return migrationRequired(errors.New("target workspace root binding requires migration"))
		}
		if target.Availability != domain.WorkspaceAvailabilityAvailable || !target.RemovedAt.IsZero() {
			return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeWorkspaceAvailabilityInvalid, false, errors.New("target workspace is not available"))
		}
		if target.Status == domain.WorkspaceStatusActive || sameOptionalID(state.ActiveWorkspaceID, target.ID) {
			return controlStateConflict(errors.New("target workspace is already active"))
		}
		activeID, e := gormLockDatabaseActiveWorkspace(txCtx, tx)
		if e != nil {
			return e
		}
		if !equalOptionalIDs(activeID, state.ActiveWorkspaceID) {
			return controlCorrupt(errors.New("workspace control active identity differs from Registry status"))
		}
		lease := now.Add(command.LeaseDuration)
		operation, e = gormScanSwitch(txCtx, tx, `INSERT INTO ops.workspace_switch(id,controller_instance_id,idempotency_key,request_hash,previous_workspace_id,target_workspace_id,target_root_fingerprint,target_binding_version,grant_generation,recovery_generation,expected_state_version,phase,result,lease_owner_id,lease_expires_at,heartbeat_at,deadline_at,error_code,version,created_at,updated_at,completed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,'validating',NULL,?,?,?, ?,NULL,1,?,?,NULL) RETURNING `+switchOperationColumns, string(command.OperationID), string(command.ControllerInstanceID), command.IdempotencyKey, command.RequestHash, nullableFoundationID(state.ActiveWorkspaceID), string(target.ID), target.RootFingerprint, target.BindingVersion, state.GrantGeneration+1, state.GrantGeneration+2, command.ExpectedStateVersion, string(command.LeaseOwnerID), lease, now, command.Deadline.UTC(), now, now)
		if e != nil {
			return e
		}
		result := tx.Exec(`UPDATE ops.runtime_mutation_gate SET owner_kind='workspace_switch',owner_id=?,lease_expires_at=?,version=version+1,updated_at=? WHERE singleton=true AND owner_id IS NULL`, string(operation.ID), lease, now)
		if result.Error != nil {
			return classifyGORMControl(txCtx, result.Error, domain.ErrorCodeControlDatabaseUnavailable)
		}
		if result.RowsAffected != 1 {
			return runtimeMutationConflict(errors.New("runtime mutation gate ownership changed"))
		}
		updated, e := gormScanControlState(txCtx, tx, `UPDATE ops.workspace_control_state SET operation_id=?,operation_phase='validating',target_workspace_id=?,previous_active_workspace_id=?,controller_lease_owner_id=?,controller_lease_expires_at=?,last_error_code=NULL,state_version=state_version+1,updated_at=? WHERE singleton=true AND state_version=? AND operation_id IS NULL RETURNING `+controlStateColumns, string(operation.ID), string(target.ID), nullableFoundationID(state.ActiveWorkspaceID), string(command.LeaseOwnerID), lease, now, command.ExpectedStateVersion)
		if gormWorkspaceNoRows(e) {
			return controlStateConflict(errors.New("workspace control state changed"))
		}
		if e != nil {
			return e
		}
		if updated.OperationID == nil || *updated.OperationID != operation.ID {
			return controlCorrupt(errors.New("workspace operation was not installed in control state"))
		}
		return nil
	})
	if err != nil {
		return domain.SwitchOperation{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return operation, nil
}

func (r *GORMRepository) RenewSwitch(ctx context.Context, command application.RenewSwitchCommand) (domain.SwitchOperation, error) {
	return r.gormMutateSwitchPhase(ctx, command.OperationID, command.LeaseOwnerID, command.ExpectedPhase, command.ExpectedPhase, command.ExpectedOperationVersion, command.ExpectedStateVersion, command.LeaseDuration)
}
func (r *GORMRepository) AdvanceSwitch(ctx context.Context, command application.AdvanceSwitchCommand) (domain.SwitchOperation, error) {
	if err := r.ready(ctx); err != nil {
		return domain.SwitchOperation{}, err
	}
	if err := domain.ValidateSwitchTransition(command.ExpectedPhase, command.NextPhase); err != nil {
		return domain.SwitchOperation{}, err
	}
	return r.gormMutateSwitchPhase(ctx, command.OperationID, command.LeaseOwnerID, command.ExpectedPhase, command.NextPhase, command.ExpectedOperationVersion, command.ExpectedStateVersion, command.LeaseDuration)
}

func (r *GORMRepository) gormMutateSwitchPhase(ctx context.Context, operationID, ownerID foundation.ID, expectedPhase, nextPhase domain.SwitchPhase, expectedOperationVersion, expectedStateVersion int64, leaseDuration time.Duration) (operation domain.SwitchOperation, err error) {
	err = r.within(ctx, foundation.TransactionOptions{}, func(txCtx context.Context, tx *gorm.DB, _ foundation.TransactionScope) error {
		locked, e := gormLockOwnedSwitch(txCtx, tx, operationID, ownerID, expectedPhase, expectedOperationVersion, expectedStateVersion, true)
		if e != nil {
			return e
		}
		operation, _, e = gormRenewLockedSwitch(txCtx, tx, locked, nextPhase, leaseDuration)
		return e
	})
	if err != nil {
		return domain.SwitchOperation{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return operation, nil
}

func (r *GORMRepository) TakeOverSwitch(ctx context.Context, command application.TakeOverSwitchCommand) (operation domain.SwitchOperation, err error) {
	err = r.within(ctx, foundation.TransactionOptions{}, func(txCtx context.Context, tx *gorm.DB, _ foundation.TransactionScope) error {
		state, e := gormLoadControlState(txCtx, tx, "FOR UPDATE")
		if e != nil {
			return e
		}
		current, e := gormLoadSwitchOperation(txCtx, tx, command.OperationID, "FOR UPDATE")
		if e != nil {
			return e
		}
		gate, e := gormLoadMutationGate(txCtx, tx, "FOR UPDATE")
		if e != nil {
			return e
		}
		now, e := gormWorkspaceNow(txCtx, tx)
		if e != nil {
			return e
		}
		if current.Result != "" || current.Phase != command.ExpectedPhase || current.Version != command.ExpectedOperationVersion || state.StateVersion != command.ExpectedStateVersion || state.OperationID == nil || *state.OperationID != current.ID || state.OperationPhase != current.Phase {
			return controlStateConflict(errors.New("workspace switch takeover state changed"))
		}
		if current.LeaseOwnerID == nil || state.ControllerLeaseOwnerID == nil || *current.LeaseOwnerID != *state.ControllerLeaseOwnerID || !state.ControllerLeaseExpiresAt.Equal(current.LeaseExpiresAt) || gate.OwnerKind != "workspace_switch" || gate.OwnerID == nil || *gate.OwnerID != current.ID || !gate.LeaseExpiresAt.Equal(current.LeaseExpiresAt) {
			return controlCorrupt(errors.New("workspace switch takeover lease facts are inconsistent"))
		}
		if current.LeaseExpiresAt.After(now) {
			return switchLeaseHeld(errors.New("workspace switch lease has not expired"))
		}
		lease := now.Add(command.LeaseDuration)
		operation, e = gormScanSwitch(txCtx, tx, `UPDATE ops.workspace_switch SET lease_owner_id=?,lease_expires_at=?,heartbeat_at=?,version=version+1,updated_at=? WHERE id=? AND version=? AND result IS NULL RETURNING `+switchOperationColumns, string(command.NewLeaseOwnerID), lease, now, now, string(current.ID), command.ExpectedOperationVersion)
		if e != nil {
			return e
		}
		if _, e = gormScanControlState(txCtx, tx, `UPDATE ops.workspace_control_state SET controller_lease_owner_id=?,controller_lease_expires_at=?,state_version=state_version+1,updated_at=? WHERE singleton=true AND operation_id=? AND state_version=? RETURNING `+controlStateColumns, string(command.NewLeaseOwnerID), lease, now, string(current.ID), command.ExpectedStateVersion); e != nil {
			return e
		}
		return gormRenewMutationGate(txCtx, tx, operation.ID, lease, now)
	})
	if err != nil {
		return domain.SwitchOperation{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return operation, nil
}

func (r *GORMRepository) RevokeActiveWorkspace(ctx context.Context, command application.RevokeActiveCommand) (state domain.ControlState, err error) {
	err = r.within(ctx, foundation.TransactionOptions{}, func(txCtx context.Context, tx *gorm.DB, _ foundation.TransactionScope) error {
		locked, e := gormLockOwnedSwitch(txCtx, tx, command.OperationID, command.LeaseOwnerID, domain.SwitchPhaseRevoking, command.ExpectedOperationVersion, command.ExpectedStateVersion, true)
		if e != nil {
			return e
		}
		if !equalOptionalIDs(locked.State.ActiveWorkspaceID, locked.Operation.PreviousWorkspaceID) {
			return controlCorrupt(errors.New("previous active workspace ownership changed before revoke"))
		}
		if locked.Operation.PreviousWorkspaceID != nil {
			result := tx.Exec(`UPDATE core.workspace SET status='inactive',version=version+1,updated_at=GREATEST(updated_at,?) WHERE id=? AND status='active'`, locked.Now, string(*locked.Operation.PreviousWorkspaceID))
			if result.Error != nil {
				return classifyGORMControl(txCtx, result.Error, domain.ErrorCodeControlDatabaseUnavailable)
			}
			if result.RowsAffected != 1 {
				return controlStateConflict(errors.New("previous active workspace was not revocable"))
			}
		}
		lease := locked.Now.Add(command.LeaseDuration)
		if _, e = gormRenewOperationOnly(txCtx, tx, locked.Operation, domain.SwitchPhaseRevoking, lease, locked.Now); e != nil {
			return e
		}
		state, e = gormScanControlState(txCtx, tx, `UPDATE ops.workspace_control_state SET active_workspace_id=NULL,resume_workspace_id=?,controller_lease_expires_at=?,state_version=state_version+1,updated_at=? WHERE singleton=true AND operation_id=? AND state_version=? RETURNING `+controlStateColumns, nullableFoundationID(locked.Operation.PreviousWorkspaceID), lease, locked.Now, string(locked.Operation.ID), command.ExpectedStateVersion)
		if e != nil {
			return e
		}
		return gormRenewMutationGate(txCtx, tx, locked.Operation.ID, lease, locked.Now)
	})
	if err != nil {
		return domain.ControlState{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return state, nil
}

func (r *GORMRepository) CommitTargetWorkspace(ctx context.Context, command application.CommitTargetCommand) (state domain.ControlState, err error) {
	err = r.within(ctx, foundation.TransactionOptions{}, func(txCtx context.Context, tx *gorm.DB, _ foundation.TransactionScope) error {
		locked, e := gormLockOwnedSwitch(txCtx, tx, command.OperationID, command.LeaseOwnerID, domain.SwitchPhaseCommitting, command.ExpectedOperationVersion, command.ExpectedStateVersion, true)
		if e != nil {
			return e
		}
		if locked.State.ActiveWorkspaceID != nil {
			return controlStateConflict(errors.New("old workspace is still active at target commit"))
		}
		if e = gormVerifySwitchRuntimes(txCtx, tx, locked.Operation, locked.Operation.TargetWorkspaceID, locked.Operation.GrantGeneration, true, locked.Now, command.RuntimeFreshWithin); e != nil {
			return e
		}
		target, e := gormLoadWorkspaceForUpdate(txCtx, tx, locked.Operation.TargetWorkspaceID)
		if e != nil {
			return e
		}
		if target.Availability != domain.WorkspaceAvailabilityAvailable || !target.RemovedAt.IsZero() || target.RootFingerprint != locked.Operation.TargetRootFingerprint || target.BindingVersion != locked.Operation.TargetBindingVersion {
			return identityConflict(errors.New("target workspace binding changed before commit"))
		}
		result := tx.Exec(`UPDATE core.workspace SET status='active',last_opened_at=?,version=version+1,updated_at=GREATEST(updated_at,?) WHERE id=? AND status='inactive' AND availability='available' AND removed_at IS NULL`, locked.Now, locked.Now, string(target.ID))
		if result.Error != nil {
			return classifyGORMControl(txCtx, result.Error, domain.ErrorCodeControlDatabaseUnavailable)
		}
		if result.RowsAffected != 1 {
			return controlStateConflict(errors.New("target workspace was not commit-ready"))
		}
		lease := locked.Now.Add(command.LeaseDuration)
		if _, e = gormRenewOperationOnly(txCtx, tx, locked.Operation, domain.SwitchPhaseCommitting, lease, locked.Now); e != nil {
			return e
		}
		state, e = gormScanControlState(txCtx, tx, `UPDATE ops.workspace_control_state SET active_workspace_id=?,resume_workspace_id=?,grant_generation=?,controller_lease_expires_at=?,state_version=state_version+1,updated_at=? WHERE singleton=true AND operation_id=? AND state_version=? RETURNING `+controlStateColumns, string(target.ID), string(target.ID), locked.Operation.GrantGeneration, lease, locked.Now, string(locked.Operation.ID), command.ExpectedStateVersion)
		if e != nil {
			return e
		}
		return gormRenewMutationGate(txCtx, tx, locked.Operation.ID, lease, locked.Now)
	})
	if err != nil {
		return domain.ControlState{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return state, nil
}

func (r *GORMRepository) RestorePreviousWorkspace(ctx context.Context, command application.RestorePreviousCommand) (state domain.ControlState, err error) {
	err = r.within(ctx, foundation.TransactionOptions{}, func(txCtx context.Context, tx *gorm.DB, _ foundation.TransactionScope) error {
		locked, e := gormLockOwnedSwitch(txCtx, tx, command.OperationID, command.LeaseOwnerID, domain.SwitchPhaseRecovering, command.ExpectedOperationVersion, command.ExpectedStateVersion, true)
		if e != nil {
			return e
		}
		if locked.Operation.PreviousWorkspaceID == nil {
			return controlStateConflict(errors.New("first workspace switch has no previous identity to restore"))
		}
		if e = gormVerifySwitchRuntimes(txCtx, tx, locked.Operation, *locked.Operation.PreviousWorkspaceID, locked.Operation.RecoveryGeneration, true, locked.Now, command.RuntimeFreshWithin); e != nil {
			return e
		}
		previous, e := gormLoadWorkspaceForUpdate(txCtx, tx, *locked.Operation.PreviousWorkspaceID)
		if e != nil {
			return e
		}
		if previous.Availability != domain.WorkspaceAvailabilityAvailable || !previous.RemovedAt.IsZero() || previous.BindingVersion < 1 {
			return identityConflict(errors.New("previous workspace is no longer recoverable"))
		}
		if locked.State.ActiveWorkspaceID != nil {
			if *locked.State.ActiveWorkspaceID != locked.Operation.TargetWorkspaceID {
				return controlCorrupt(errors.New("unexpected workspace is active during recovery"))
			}
			result := tx.Exec(`UPDATE core.workspace SET status='inactive',version=version+1,updated_at=GREATEST(updated_at,?) WHERE id=? AND status='active'`, locked.Now, string(*locked.State.ActiveWorkspaceID))
			if result.Error != nil {
				return classifyGORMControl(txCtx, result.Error, domain.ErrorCodeControlDatabaseUnavailable)
			}
			if result.RowsAffected != 1 {
				return controlStateConflict(errors.New("failed target was not revocable"))
			}
		}
		result := tx.Exec(`UPDATE core.workspace SET status='active',last_opened_at=?,version=version+1,updated_at=GREATEST(updated_at,?) WHERE id=? AND status='inactive' AND availability='available' AND removed_at IS NULL`, locked.Now, locked.Now, string(previous.ID))
		if result.Error != nil {
			return classifyGORMControl(txCtx, result.Error, domain.ErrorCodeControlDatabaseUnavailable)
		}
		if result.RowsAffected != 1 {
			return controlStateConflict(errors.New("previous workspace was not recoverable"))
		}
		lease := locked.Now.Add(command.LeaseDuration)
		if _, e = gormRenewOperationOnly(txCtx, tx, locked.Operation, domain.SwitchPhaseRecovering, lease, locked.Now); e != nil {
			return e
		}
		state, e = gormScanControlState(txCtx, tx, `UPDATE ops.workspace_control_state SET active_workspace_id=?,resume_workspace_id=?,grant_generation=?,controller_lease_expires_at=?,state_version=state_version+1,updated_at=? WHERE singleton=true AND operation_id=? AND state_version=? RETURNING `+controlStateColumns, string(previous.ID), string(previous.ID), locked.Operation.RecoveryGeneration, lease, locked.Now, string(locked.Operation.ID), command.ExpectedStateVersion)
		if e != nil {
			return e
		}
		return gormRenewMutationGate(txCtx, tx, locked.Operation.ID, lease, locked.Now)
	})
	if err != nil {
		return domain.ControlState{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return state, nil
}

func (r *GORMRepository) FinishSwitch(ctx context.Context, command application.FinishSwitchCommand) (operation domain.SwitchOperation, err error) {
	err = r.within(ctx, foundation.TransactionOptions{}, func(txCtx context.Context, tx *gorm.DB, _ foundation.TransactionScope) error {
		locked, e := gormLockOwnedSwitchAnyPhase(txCtx, tx, command.OperationID, command.LeaseOwnerID, command.ExpectedOperationVersion, command.ExpectedStateVersion, true)
		if e != nil {
			return e
		}
		if e = domain.ValidateTerminalTransition(locked.Operation.Phase, command.Result, command.ErrorCode); e != nil {
			return e
		}
		switch command.Result {
		case domain.SwitchResultSucceeded:
			if !sameOptionalID(locked.State.ActiveWorkspaceID, locked.Operation.TargetWorkspaceID) || locked.State.GrantGeneration != locked.Operation.GrantGeneration {
				return controlStateConflict(errors.New("target workspace is not the committed grant"))
			}
			if e = gormVerifySwitchRuntimes(txCtx, tx, locked.Operation, locked.Operation.TargetWorkspaceID, locked.Operation.GrantGeneration, false, locked.Now, command.RuntimeFreshWithin); e != nil {
				return e
			}
		case domain.SwitchResultRolledBack:
			if locked.Operation.PreviousWorkspaceID == nil || !sameOptionalID(locked.State.ActiveWorkspaceID, *locked.Operation.PreviousWorkspaceID) || locked.State.GrantGeneration != locked.Operation.RecoveryGeneration {
				return controlStateConflict(errors.New("previous workspace is not the recovered grant"))
			}
			if e = gormVerifySwitchRuntimes(txCtx, tx, locked.Operation, *locked.Operation.PreviousWorkspaceID, locked.Operation.RecoveryGeneration, false, locked.Now, command.RuntimeFreshWithin); e != nil {
				return e
			}
		case domain.SwitchResultRejected, domain.SwitchResultCancelled:
			if !equalOptionalIDs(locked.State.ActiveWorkspaceID, locked.Operation.PreviousWorkspaceID) || locked.State.GrantGeneration != locked.Operation.GrantGeneration-1 {
				return controlStateConflict(errors.New("pre-revoke terminal state changed active grant"))
			}
		case domain.SwitchResultFailed:
			if locked.State.ActiveWorkspaceID != nil {
				result := tx.Exec(`UPDATE core.workspace SET status='inactive',version=version+1,updated_at=GREATEST(updated_at,?) WHERE id=? AND status='active'`, locked.Now, string(*locked.State.ActiveWorkspaceID))
				if result.Error != nil {
					return classifyGORMControl(txCtx, result.Error, domain.ErrorCodeControlDatabaseUnavailable)
				}
				if result.RowsAffected != 1 {
					return controlCorrupt(errors.New("failed recovery could not clear active workspace"))
				}
			}
		}
		operation, e = gormScanSwitch(txCtx, tx, `UPDATE ops.workspace_switch SET result=?,lease_owner_id=NULL,lease_expires_at=NULL,heartbeat_at=?,error_code=?,version=version+1,updated_at=?,completed_at=? WHERE id=? AND version=? AND result IS NULL RETURNING `+switchOperationColumns, string(command.Result), locked.Now, nullableText(command.ErrorCode), locked.Now, locked.Now, string(locked.Operation.ID), command.ExpectedOperationVersion)
		if e != nil {
			return e
		}
		activeID, resumeID := nullableFoundationID(locked.State.ActiveWorkspaceID), nullableFoundationID(locked.State.ResumeWorkspaceID)
		if command.Result == domain.SwitchResultFailed {
			activeID, resumeID = nil, nullableFoundationID(locked.Operation.PreviousWorkspaceID)
		}
		if _, e = gormScanControlState(txCtx, tx, `UPDATE ops.workspace_control_state SET active_workspace_id=?,resume_workspace_id=?,operation_id=NULL,operation_phase=NULL,target_workspace_id=NULL,previous_active_workspace_id=NULL,controller_lease_owner_id=NULL,controller_lease_expires_at=NULL,last_error_code=?,state_version=state_version+1,updated_at=? WHERE singleton=true AND operation_id=? AND state_version=? RETURNING `+controlStateColumns, activeID, resumeID, nullableText(command.ErrorCode), locked.Now, string(locked.Operation.ID), command.ExpectedStateVersion); e != nil {
			return e
		}
		result := tx.Exec(`UPDATE ops.runtime_mutation_gate SET owner_kind=NULL,owner_id=NULL,lease_expires_at=NULL,version=version+1,updated_at=? WHERE singleton=true AND owner_kind='workspace_switch' AND owner_id=?`, locked.Now, string(locked.Operation.ID))
		if result.Error != nil {
			return classifyGORMControl(txCtx, result.Error, domain.ErrorCodeControlDatabaseUnavailable)
		}
		if result.RowsAffected != 1 {
			return controlCorrupt(errors.New("workspace mutation gate was not released"))
		}
		return nil
	})
	if err != nil {
		return domain.SwitchOperation{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return operation, nil
}

func gormLockOwnedSwitch(ctx context.Context, tx *gorm.DB, operationID, ownerID foundation.ID, expectedPhase domain.SwitchPhase, expectedOperationVersion, expectedStateVersion int64, requireFresh bool) (lockedSwitch, error) {
	locked, err := gormLockOwnedSwitchAnyPhase(ctx, tx, operationID, ownerID, expectedOperationVersion, expectedStateVersion, requireFresh)
	if err != nil {
		return lockedSwitch{}, err
	}
	if locked.Operation.Phase != expectedPhase || locked.State.OperationPhase != expectedPhase {
		return lockedSwitch{}, switchPhaseConflict(errors.New("workspace switch phase changed"))
	}
	return locked, nil
}

func gormLockOwnedSwitchAnyPhase(ctx context.Context, tx *gorm.DB, operationID, ownerID foundation.ID, expectedOperationVersion, expectedStateVersion int64, requireFresh bool) (lockedSwitch, error) {
	state, err := gormLoadControlState(ctx, tx, "FOR UPDATE")
	if err != nil {
		return lockedSwitch{}, err
	}
	operation, err := gormLoadSwitchOperation(ctx, tx, operationID, "FOR UPDATE")
	if err != nil {
		return lockedSwitch{}, err
	}
	gate, err := gormLoadMutationGate(ctx, tx, "FOR UPDATE")
	if err != nil {
		return lockedSwitch{}, err
	}
	now, err := gormWorkspaceNow(ctx, tx)
	if err != nil {
		return lockedSwitch{}, err
	}
	if operation.Result != "" || operation.Version != expectedOperationVersion {
		return lockedSwitch{}, controlStateConflict(errors.New("workspace switch operation version changed"))
	}
	if state.StateVersion != expectedStateVersion || state.OperationID == nil || *state.OperationID != operation.ID || state.TargetWorkspaceID == nil || *state.TargetWorkspaceID != operation.TargetWorkspaceID || !equalOptionalIDs(state.PreviousActiveWorkspaceID, operation.PreviousWorkspaceID) {
		return lockedSwitch{}, controlStateConflict(errors.New("workspace control operation binding changed"))
	}
	if operation.LeaseOwnerID == nil || *operation.LeaseOwnerID != ownerID || state.ControllerLeaseOwnerID == nil || *state.ControllerLeaseOwnerID != ownerID {
		if operation.LeaseExpiresAt.After(now) {
			return lockedSwitch{}, switchLeaseHeld(errors.New("workspace switch lease belongs to another owner"))
		}
		return lockedSwitch{}, switchLeaseExpired(errors.New("workspace switch lease ownership expired"))
	}
	if requireFresh && (!operation.LeaseExpiresAt.After(now) || !state.ControllerLeaseExpiresAt.After(now)) {
		return lockedSwitch{}, switchLeaseExpired(errors.New("workspace switch lease expired"))
	}
	if gate.OwnerKind != "workspace_switch" || gate.OwnerID == nil || *gate.OwnerID != operation.ID || !gate.LeaseExpiresAt.Equal(operation.LeaseExpiresAt) || !state.ControllerLeaseExpiresAt.Equal(operation.LeaseExpiresAt) {
		return lockedSwitch{}, controlCorrupt(errors.New("workspace mutation leases are inconsistent"))
	}
	return lockedSwitch{State: state, Operation: operation, Gate: gate, Now: now}, nil
}

func gormRenewLockedSwitch(ctx context.Context, tx *gorm.DB, locked lockedSwitch, nextPhase domain.SwitchPhase, leaseDuration time.Duration) (domain.SwitchOperation, domain.ControlState, error) {
	lease := locked.Now.Add(leaseDuration)
	operation, err := gormRenewOperationOnly(ctx, tx, locked.Operation, nextPhase, lease, locked.Now)
	if err != nil {
		return domain.SwitchOperation{}, domain.ControlState{}, err
	}
	state, err := gormScanControlState(ctx, tx, `UPDATE ops.workspace_control_state SET operation_phase=?,controller_lease_expires_at=?,state_version=state_version+1,updated_at=? WHERE singleton=true AND operation_id=? AND state_version=? RETURNING `+controlStateColumns, string(nextPhase), lease, locked.Now, string(operation.ID), locked.State.StateVersion)
	if gormWorkspaceNoRows(err) {
		return domain.SwitchOperation{}, domain.ControlState{}, controlStateConflict(errors.New("workspace control state changed"))
	}
	if err != nil {
		return domain.SwitchOperation{}, domain.ControlState{}, err
	}
	if err = gormRenewMutationGate(ctx, tx, operation.ID, lease, locked.Now); err != nil {
		return domain.SwitchOperation{}, domain.ControlState{}, err
	}
	return operation, state, nil
}

func gormRenewOperationOnly(ctx context.Context, tx *gorm.DB, operation domain.SwitchOperation, nextPhase domain.SwitchPhase, lease, now time.Time) (domain.SwitchOperation, error) {
	updated, err := gormScanSwitch(ctx, tx, `UPDATE ops.workspace_switch SET phase=?,lease_expires_at=?,heartbeat_at=?,version=version+1,updated_at=? WHERE id=? AND version=? AND result IS NULL RETURNING `+switchOperationColumns, string(nextPhase), lease, now, now, string(operation.ID), operation.Version)
	if gormWorkspaceNoRows(err) {
		return domain.SwitchOperation{}, controlStateConflict(errors.New("workspace switch operation changed"))
	}
	return updated, err
}

func gormRenewMutationGate(ctx context.Context, tx *gorm.DB, operationID foundation.ID, lease, now time.Time) error {
	result := tx.Exec(`UPDATE ops.runtime_mutation_gate SET lease_expires_at=?,version=version+1,updated_at=? WHERE singleton=true AND owner_kind='workspace_switch' AND owner_id=?`, lease, now, string(operationID))
	if result.Error != nil {
		return classifyGORMControl(ctx, result.Error, domain.ErrorCodeControlDatabaseUnavailable)
	}
	if result.RowsAffected != 1 {
		return controlCorrupt(errors.New("workspace mutation gate ownership changed"))
	}
	return nil
}

func gormLoadControlState(ctx context.Context, tx *gorm.DB, suffix string) (domain.ControlState, error) {
	state, err := gormScanControlState(ctx, tx, `SELECT `+controlStateColumns+` FROM ops.workspace_control_state WHERE singleton=true `+suffix)
	if gormWorkspaceNoRows(err) {
		return domain.ControlState{}, controlCorrupt(errors.New("workspace control singleton is missing"))
	}
	return state, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
}

func gormLoadSwitchOperation(ctx context.Context, tx *gorm.DB, id foundation.ID, suffix string) (domain.SwitchOperation, error) {
	operation, err := gormScanSwitch(ctx, tx, `SELECT `+switchOperationColumns+` FROM ops.workspace_switch WHERE id=? `+suffix, string(id))
	if gormWorkspaceNoRows(err) {
		return domain.SwitchOperation{}, switchNotFound(err)
	}
	return operation, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
}

func gormFindIdempotentSwitch(ctx context.Context, tx *gorm.DB, controllerID foundation.ID, key, suffix string) (domain.SwitchOperation, bool, error) {
	operation, err := gormScanSwitch(ctx, tx, `SELECT `+switchOperationColumns+` FROM ops.workspace_switch WHERE controller_instance_id=? AND idempotency_key=? `+suffix, string(controllerID), key)
	if gormWorkspaceNoRows(err) {
		return domain.SwitchOperation{}, false, nil
	}
	return operation, err == nil, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
}

func gormLoadMutationGate(ctx context.Context, tx *gorm.DB, suffix string) (mutationGate, error) {
	row, err := gormWorkspaceRawRow(tx, `SELECT owner_kind,owner_id::text,lease_expires_at,version,updated_at FROM ops.runtime_mutation_gate WHERE singleton=true `+suffix)
	if err != nil {
		return mutationGate{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	gate, err := scanMutationGate(row)
	if gormWorkspaceNoRows(err) {
		return mutationGate{}, controlCorrupt(errors.New("runtime mutation singleton is missing"))
	}
	return gate, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
}

func gormLoadWorkspaceForUpdate(ctx context.Context, tx *gorm.DB, id foundation.ID) (domain.Workspace, error) {
	row, err := gormWorkspaceRawRow(tx, `SELECT `+workspaceColumns+` FROM core.workspace WHERE id=? FOR UPDATE`, string(id))
	if err != nil {
		return domain.Workspace{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	workspace, err := scanWorkspace(row)
	if gormWorkspaceNoRows(err) {
		return domain.Workspace{}, registryNotFound(err)
	}
	return workspace, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
}

func gormLockDatabaseActiveWorkspace(ctx context.Context, tx *gorm.DB) (*foundation.ID, error) {
	row, err := gormWorkspaceRawRow(tx, `SELECT id::text FROM core.workspace WHERE status='active' FOR UPDATE`)
	if err != nil {
		return nil, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	var value string
	if err = row.Scan(&value); gormWorkspaceNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	parsed, err := foundation.ParseID(value)
	if err != nil {
		return nil, controlCorrupt(fmt.Errorf("parse active workspace id: %w", err))
	}
	return &parsed, nil
}

func gormWorkspaceNow(ctx context.Context, tx *gorm.DB) (time.Time, error) {
	row, err := gormWorkspaceRawRow(tx, `SELECT clock_timestamp()`)
	if err != nil {
		return time.Time{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	var now time.Time
	if err = row.Scan(&now); err != nil {
		return time.Time{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return now.UTC(), nil
}

func gormScanControlState(ctx context.Context, tx *gorm.DB, query string, arguments ...any) (domain.ControlState, error) {
	row, err := gormWorkspaceRawRow(tx, query, arguments...)
	if err != nil {
		return domain.ControlState{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return scanControlState(row)
}

func gormScanSwitch(ctx context.Context, tx *gorm.DB, query string, arguments ...any) (domain.SwitchOperation, error) {
	row, err := gormWorkspaceRawRow(tx, query, arguments...)
	if err != nil {
		return domain.SwitchOperation{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return scanSwitchOperation(row)
}

var _ application.ControlStore = (*GORMRepository)(nil)
