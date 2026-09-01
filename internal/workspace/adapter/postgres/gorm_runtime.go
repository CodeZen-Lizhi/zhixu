package workspacepostgres

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"gorm.io/gorm"
)

// RegisterRuntime claims one role after the exact Workspace binding has loaded.
func (r *GORMRepository) RegisterRuntime(ctx context.Context, registration application.RuntimeRegistration) (record domain.RuntimeRecord, err error) {
	err = r.within(ctx, foundation.TransactionOptions{}, func(txCtx context.Context, tx *gorm.DB, _ foundation.TransactionScope) error {
		state, e := gormLoadControlState(txCtx, tx, "FOR SHARE")
		if e != nil {
			return e
		}
		workspace, e := gormLoadWorkspaceForShare(txCtx, tx, registration.WorkspaceID)
		if e != nil {
			return e
		}
		var operation *domain.SwitchOperation
		if registration.OperationID != nil {
			persisted, loadErr := gormLoadSwitchOperation(txCtx, tx, *registration.OperationID, "FOR SHARE")
			if loadErr != nil {
				return loadErr
			}
			operation = &persisted
		}
		if e = authorizeRuntime(state, operation, workspace, registration); e != nil {
			return e
		}
		now, e := gormWorkspaceNow(txCtx, tx)
		if e != nil {
			return e
		}
		record, e = gormScanRuntime(txCtx, tx, `INSERT INTO ops.workspace_runtime(role,instance_id,workspace_id,operation_id,grant_generation,root_fingerprint,binding_version,phase,started_at,heartbeat_at,version) VALUES(?,?,?,?,?,?,?,?,?, ?,1) ON CONFLICT(role) DO UPDATE SET instance_id=EXCLUDED.instance_id,workspace_id=EXCLUDED.workspace_id,operation_id=EXCLUDED.operation_id,grant_generation=EXCLUDED.grant_generation,root_fingerprint=EXCLUDED.root_fingerprint,binding_version=EXCLUDED.binding_version,phase=EXCLUDED.phase,started_at=CASE WHEN ops.workspace_runtime.instance_id=EXCLUDED.instance_id THEN ops.workspace_runtime.started_at ELSE EXCLUDED.started_at END,heartbeat_at=EXCLUDED.heartbeat_at,version=ops.workspace_runtime.version+1 WHERE ops.workspace_runtime.instance_id<>EXCLUDED.instance_id OR (ops.workspace_runtime.workspace_id=EXCLUDED.workspace_id AND ops.workspace_runtime.operation_id IS NOT DISTINCT FROM EXCLUDED.operation_id AND ops.workspace_runtime.grant_generation=EXCLUDED.grant_generation AND ops.workspace_runtime.root_fingerprint=EXCLUDED.root_fingerprint AND ops.workspace_runtime.binding_version=EXCLUDED.binding_version AND ops.workspace_runtime.phase=EXCLUDED.phase) RETURNING `+workspaceRuntimeColumns, string(registration.Role), string(registration.InstanceID), string(registration.WorkspaceID), nullableFoundationID(registration.OperationID), registration.GrantGeneration, registration.RootFingerprint, registration.BindingVersion, string(registration.Phase), now, now)
		if gormWorkspaceNoRows(e) {
			return runtimeConflict(errors.New("workspace runtime role ownership changed"))
		}
		return e
	})
	if err != nil {
		return domain.RuntimeRecord{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return record, nil
}

// HeartbeatRuntime renews one exact role, instance, operation and version owner.
func (r *GORMRepository) HeartbeatRuntime(ctx context.Context, heartbeat application.RuntimeHeartbeat) (record domain.RuntimeRecord, err error) {
	err = r.within(ctx, foundation.TransactionOptions{}, func(txCtx context.Context, tx *gorm.DB, _ foundation.TransactionScope) error {
		state, e := gormLoadControlState(txCtx, tx, "FOR SHARE")
		if e != nil {
			return e
		}
		current, e := gormLoadRuntimeForUpdate(txCtx, tx, heartbeat.Role)
		if e != nil {
			return e
		}
		if current.InstanceID != heartbeat.InstanceID || current.Version != heartbeat.ExpectedRuntimeVersion || !equalOptionalIDs(current.OperationID, heartbeat.OperationID) {
			return runtimeConflict(errors.New("workspace runtime heartbeat ownership changed"))
		}
		workspace, e := gormLoadWorkspaceForShare(txCtx, tx, current.WorkspaceID)
		if e != nil {
			return e
		}
		var operation *domain.SwitchOperation
		if current.OperationID != nil {
			persisted, loadErr := gormLoadSwitchOperation(txCtx, tx, *current.OperationID, "FOR SHARE")
			if loadErr != nil {
				return loadErr
			}
			operation = &persisted
		}
		if e = authorizeRuntime(state, operation, workspace, application.RuntimeRegistration{Role: current.Role, InstanceID: current.InstanceID, WorkspaceID: current.WorkspaceID, OperationID: current.OperationID, GrantGeneration: current.GrantGeneration, RootFingerprint: current.RootFingerprint, BindingVersion: current.BindingVersion, Phase: current.Phase}); e != nil {
			return e
		}
		now, e := gormWorkspaceNow(txCtx, tx)
		if e != nil {
			return e
		}
		record, e = gormScanRuntime(txCtx, tx, `UPDATE ops.workspace_runtime SET heartbeat_at=?,version=version+1 WHERE role=? AND instance_id=? AND version=? RETURNING `+workspaceRuntimeColumns, now, string(current.Role), string(current.InstanceID), current.Version)
		if gormWorkspaceNoRows(e) {
			return runtimeConflict(errors.New("workspace runtime heartbeat version changed"))
		}
		return e
	})
	if err != nil {
		return domain.RuntimeRecord{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return record, nil
}

// SetRuntimePhase atomically binds and unbinds a process from its controlling switch.
func (r *GORMRepository) SetRuntimePhase(ctx context.Context, command application.RuntimePhaseCommand) (record domain.RuntimeRecord, err error) {
	err = r.within(ctx, foundation.TransactionOptions{}, func(txCtx context.Context, tx *gorm.DB, _ foundation.TransactionScope) error {
		state, e := gormLoadControlState(txCtx, tx, "FOR SHARE")
		if e != nil {
			return e
		}
		current, e := gormLoadRuntimeForUpdate(txCtx, tx, command.Role)
		if e != nil {
			return e
		}
		if current.InstanceID != command.InstanceID || current.Version != command.ExpectedRuntimeVersion || current.Phase != command.ExpectedPhase {
			return runtimeConflict(errors.New("workspace runtime phase ownership changed"))
		}
		var operation *domain.SwitchOperation
		if command.OperationID != nil {
			persisted, loadErr := gormLoadSwitchOperation(txCtx, tx, *command.OperationID, "FOR SHARE")
			if loadErr != nil {
				return loadErr
			}
			operation = &persisted
		}
		nextOperationID, e := authorizeRuntimeTransition(state, operation, current, command)
		if e != nil {
			return e
		}
		now, e := gormWorkspaceNow(txCtx, tx)
		if e != nil {
			return e
		}
		record, e = gormScanRuntime(txCtx, tx, `UPDATE ops.workspace_runtime SET operation_id=?,phase=?,heartbeat_at=?,version=version+1 WHERE role=? AND instance_id=? AND version=? RETURNING `+workspaceRuntimeColumns, nullableFoundationID(nextOperationID), string(command.NextPhase), now, string(current.Role), string(current.InstanceID), current.Version)
		if gormWorkspaceNoRows(e) {
			return runtimeConflict(errors.New("workspace runtime phase version changed"))
		}
		return e
	})
	if err != nil {
		return domain.RuntimeRecord{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return record, nil
}

func gormVerifySwitchRuntimes(ctx context.Context, tx *gorm.DB, operation domain.SwitchOperation, workspaceID foundation.ID, generation int64, prepared bool, now time.Time, freshWithin time.Duration) error {
	rows, err := gormWorkspaceRows(tx, `SELECT `+workspaceRuntimeColumns+` FROM ops.workspace_runtime WHERE role IN ('api','worker') ORDER BY role FOR UPDATE`)
	if err != nil {
		return classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	defer rows.Close()
	seen := map[domain.RuntimeRole]bool{}
	for rows.Next() {
		record, scanErr := scanWorkspaceRuntime(rows)
		if scanErr != nil {
			return classifyGORMControl(ctx, scanErr, domain.ErrorCodeControlDatabaseUnavailable)
		}
		validPhase := record.Phase == domain.RuntimePhaseActive && record.OperationID == nil
		if prepared {
			validPhase = (record.Phase == domain.RuntimePhasePrepared || record.Phase == domain.RuntimePhaseVerifying) && record.OperationID != nil && *record.OperationID == operation.ID
		}
		if record.WorkspaceID != workspaceID || record.GrantGeneration != generation || !validPhase || !runtimeHeartbeatFresh(record.HeartbeatAt, now, freshWithin) {
			return runtimeNotPrepared(errors.New("workspace runtime is not fresh for the required grant"))
		}
		seen[record.Role] = true
	}
	if err = rows.Err(); err != nil {
		return classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	if !seen[domain.RuntimeRoleAPI] || !seen[domain.RuntimeRoleWorker] || len(seen) != 2 {
		return runtimeNotPrepared(errors.New("both workspace runtime roles are required"))
	}
	return nil
}

func gormLoadRuntimeForUpdate(ctx context.Context, tx *gorm.DB, role domain.RuntimeRole) (domain.RuntimeRecord, error) {
	record, err := gormScanRuntime(ctx, tx, `SELECT `+workspaceRuntimeColumns+` FROM ops.workspace_runtime WHERE role=? FOR UPDATE`, string(role))
	if gormWorkspaceNoRows(err) {
		return domain.RuntimeRecord{}, runtimeConflict(errors.New("workspace runtime role is not registered"))
	}
	return record, err
}
func gormLoadWorkspaceForShare(ctx context.Context, tx *gorm.DB, id foundation.ID) (domain.Workspace, error) {
	row, err := gormWorkspaceRawRow(tx, `SELECT `+workspaceColumns+` FROM core.workspace WHERE id=? FOR SHARE`, string(id))
	if err != nil {
		return domain.Workspace{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	workspace, err := scanWorkspace(row)
	if gormWorkspaceNoRows(err) {
		return domain.Workspace{}, registryNotFound(err)
	}
	return workspace, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
}
func gormScanRuntime(ctx context.Context, tx *gorm.DB, query string, arguments ...any) (domain.RuntimeRecord, error) {
	row, err := gormWorkspaceRawRow(tx, query, arguments...)
	if err != nil {
		return domain.RuntimeRecord{}, classifyGORMControl(ctx, err, domain.ErrorCodeControlDatabaseUnavailable)
	}
	return scanWorkspaceRuntime(row)
}
