package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
)

// RegisterRuntime claims one role only after its authorized revision has loaded.
func (repository *GORMRepository) RegisterRuntime(ctx context.Context, registration application.RuntimeRegistration) (record domain.RuntimeRecord, err error) {
	if registration.StaleAfter == 0 {
		registration.StaleAfter = defaultRuntimeTakeoverStaleAfter
	}
	if err = repository.ready(ctx); err != nil {
		return domain.RuntimeRecord{}, err
	}
	if !validRuntimeRegistration(registration) {
		return domain.RuntimeRecord{}, invalid(errors.New("model settings runtime registration is invalid"))
	}
	err = repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, _ foundation.TransactionScope, database *gorm.DB) error {
		state, stateErr := gormLoadState(callbackCtx, database, `FOR UPDATE`)
		if stateErr != nil {
			return stateErr
		}
		if stateErr = authorizeRuntimeRegistration(state, registration); stateErr != nil {
			return stateErr
		}
		now, nowErr := gormDatabaseNow(callbackCtx, database)
		if nowErr != nil {
			return nowErr
		}
		row, rowErr := gormRawRow(callbackCtx, database, `SELECT `+runtimeColumns+`
FROM ops.model_settings_runtime WHERE role=? FOR UPDATE`, string(registration.Role))
		if rowErr != nil {
			return classifyGORM(callbackCtx, rowErr)
		}
		existing, scanErr := scanRuntime(row)
		switch {
		case gormNoRows(scanErr):
			row, rowErr = gormRawRow(callbackCtx, database, `INSERT INTO ops.model_settings_runtime(
role,instance_id,applied_revision,rollout_id,phase,applied_at,heartbeat_at)
VALUES(?,?::uuid,?,?::uuid,?,clock_timestamp(),clock_timestamp())
RETURNING `+runtimeColumns,
				string(registration.Role), string(registration.InstanceID), registration.AppliedRevision,
				nullableID(registration.RolloutID), string(registration.Phase))
			if rowErr != nil {
				return classifyGORM(callbackCtx, rowErr)
			}
			record, scanErr = scanRuntime(row)
		case scanErr != nil:
			return classifyGORM(callbackCtx, scanErr)
		case existing.InstanceID == registration.InstanceID:
			if existing.AppliedRevision != registration.AppliedRevision ||
				!sameOptionalID(existing.RolloutID, registration.RolloutID) || existing.Phase != registration.Phase {
				return runtimeConflict(errors.New("model settings runtime same-owner binding changed"))
			}
			row, rowErr = gormRawRow(callbackCtx, database, `UPDATE ops.model_settings_runtime
SET heartbeat_at=clock_timestamp()
WHERE role=? AND instance_id=?::uuid AND applied_revision=?
  AND rollout_id IS NOT DISTINCT FROM ?::uuid AND phase=?
RETURNING `+runtimeColumns, string(registration.Role), string(registration.InstanceID), registration.AppliedRevision,
				nullableID(registration.RolloutID), string(registration.Phase))
			if rowErr != nil {
				return classifyGORM(callbackCtx, rowErr)
			}
			record, scanErr = scanRuntime(row)
		case !existing.HeartbeatAt.Before(now.Add(-registration.StaleAfter)):
			return runtimeConflict(errors.New("model settings runtime owner is still fresh"))
		default:
			row, rowErr = gormRawRow(callbackCtx, database, `UPDATE ops.model_settings_runtime
SET instance_id=?::uuid,applied_revision=?,rollout_id=?::uuid,phase=?,
    applied_at=clock_timestamp(),heartbeat_at=clock_timestamp()
WHERE role=? AND instance_id=?::uuid AND heartbeat_at=?
RETURNING `+runtimeColumns, string(registration.InstanceID), registration.AppliedRevision,
				nullableID(registration.RolloutID), string(registration.Phase), string(registration.Role),
				string(existing.InstanceID), existing.HeartbeatAt)
			if rowErr != nil {
				return classifyGORM(callbackCtx, rowErr)
			}
			record, scanErr = scanRuntime(row)
		}
		if gormNoRows(scanErr) {
			return runtimeConflict(errors.New("model settings runtime registration CAS failed"))
		}
		if scanErr != nil {
			return classifyGORM(callbackCtx, scanErr)
		}
		return nil
	})
	if err != nil {
		return domain.RuntimeRecord{}, err
	}
	return record, nil
}

// HeartbeatRuntime renews ownership only while the loaded revision remains authorized.
func (repository *GORMRepository) HeartbeatRuntime(ctx context.Context, heartbeat application.RuntimeHeartbeat) (record domain.RuntimeRecord, err error) {
	if err = repository.ready(ctx); err != nil {
		return domain.RuntimeRecord{}, err
	}
	if !domain.ValidRuntimeRole(heartbeat.Role) || !validID(heartbeat.InstanceID) || !validOptionalID(heartbeat.RolloutID) {
		return domain.RuntimeRecord{}, invalid(errors.New("model settings runtime heartbeat is invalid"))
	}
	err = repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, _ foundation.TransactionScope, database *gorm.DB) error {
		state, stateErr := gormLoadState(callbackCtx, database, `FOR UPDATE`)
		if stateErr != nil {
			return stateErr
		}
		row, rowErr := gormRawRow(callbackCtx, database, `SELECT `+runtimeColumns+`
FROM ops.model_settings_runtime WHERE role=? FOR UPDATE`, string(heartbeat.Role))
		if rowErr != nil {
			return classifyGORM(callbackCtx, rowErr)
		}
		existing, scanErr := scanRuntime(row)
		if gormNoRows(scanErr) {
			return runtimeOwnershipLost(errors.New("model settings runtime heartbeat owner is missing"))
		}
		if scanErr != nil {
			return classifyGORM(callbackCtx, scanErr)
		}
		if existing.InstanceID != heartbeat.InstanceID || !sameOptionalID(existing.RolloutID, heartbeat.RolloutID) ||
			existing.RolloutID != nil || (existing.Phase != domain.RuntimePhaseActive && existing.Phase != domain.RuntimePhaseUnavailable) {
			return runtimeOwnershipLost(errors.New("model settings runtime heartbeat ownership changed"))
		}
		phase := domain.RolloutPhase(state.phase)
		authorizedRevision := existing.AppliedRevision == state.activeRevision
		if phase == domain.RolloutPhaseActivating && state.previousActive.Valid && state.targetRevision.Valid {
			authorizedRevision = existing.AppliedRevision == state.previousActive.Int64 || existing.AppliedRevision == state.targetRevision.Int64
		}
		if !authorizedRevision {
			return runtimeConflict(errors.New("model settings runtime heartbeat revision is not authorized"))
		}
		row, rowErr = gormRawRow(callbackCtx, database, `UPDATE ops.model_settings_runtime
SET heartbeat_at=clock_timestamp()
WHERE role=? AND instance_id=?::uuid AND applied_revision=?
  AND rollout_id IS NOT DISTINCT FROM ?::uuid AND phase=? AND heartbeat_at=?
RETURNING `+runtimeColumns, string(heartbeat.Role), string(heartbeat.InstanceID), existing.AppliedRevision,
			nullableID(heartbeat.RolloutID), string(existing.Phase), existing.HeartbeatAt)
		if rowErr != nil {
			return classifyGORM(callbackCtx, rowErr)
		}
		record, scanErr = scanRuntime(row)
		if gormNoRows(scanErr) {
			return runtimeOwnershipLost(errors.New("model settings runtime heartbeat CAS failed"))
		}
		if scanErr != nil {
			return classifyGORM(callbackCtx, scanErr)
		}
		return nil
	})
	if err != nil {
		return domain.RuntimeRecord{}, err
	}
	return record, nil
}

// RestoreRuntimeAvailability changes only a same-owner, same-revision serving
// row after the process has rebuilt and probed that exact local generation.
func (repository *GORMRepository) RestoreRuntimeAvailability(
	ctx context.Context,
	command application.RestoreRuntimeAvailabilityCommand,
) (record domain.RuntimeRecord, err error) {
	if err = repository.ready(ctx); err != nil {
		return domain.RuntimeRecord{}, err
	}
	if !domain.ValidRuntimeRole(command.Role) || !validID(command.InstanceID) || command.AppliedRevision <= 0 {
		return domain.RuntimeRecord{}, invalid(errors.New("model settings runtime availability restore is invalid"))
	}
	err = repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, _ foundation.TransactionScope, database *gorm.DB) error {
		state, stateErr := gormLoadState(callbackCtx, database, `FOR UPDATE`)
		if stateErr != nil {
			return stateErr
		}
		if !runtimeAvailabilityRestoreAuthorized(state, command.AppliedRevision) {
			return runtimeConflict(errors.New("model settings runtime availability restore is not authorized"))
		}
		row, rowErr := gormRawRow(callbackCtx, database, `SELECT `+runtimeColumns+`
FROM ops.model_settings_runtime WHERE role=? FOR UPDATE`, string(command.Role))
		if rowErr != nil {
			return classifyGORM(callbackCtx, rowErr)
		}
		existing, scanErr := scanRuntime(row)
		if gormNoRows(scanErr) {
			return runtimeOwnershipLost(errors.New("model settings runtime availability owner is missing"))
		}
		if scanErr != nil {
			return classifyGORM(callbackCtx, scanErr)
		}
		if existing.InstanceID != command.InstanceID || existing.RolloutID != nil {
			return runtimeOwnershipLost(errors.New("model settings runtime availability ownership changed"))
		}
		if existing.AppliedRevision != command.AppliedRevision {
			return runtimeConflict(errors.New("model settings runtime availability revision changed"))
		}
		if existing.Phase == domain.RuntimePhaseActive {
			row, rowErr = gormRawRow(callbackCtx, database, `UPDATE ops.model_settings_runtime
SET heartbeat_at=clock_timestamp()
WHERE role=? AND instance_id=?::uuid AND applied_revision=?
  AND rollout_id IS NULL AND phase='active' AND heartbeat_at=?
RETURNING `+runtimeColumns, string(command.Role), string(command.InstanceID), command.AppliedRevision, existing.HeartbeatAt)
			if rowErr != nil {
				return classifyGORM(callbackCtx, rowErr)
			}
			record, scanErr = scanRuntime(row)
			if gormNoRows(scanErr) {
				return runtimeOwnershipLost(errors.New("model settings runtime availability replay CAS failed"))
			}
			if scanErr != nil {
				return classifyGORM(callbackCtx, scanErr)
			}
			return nil
		}
		if existing.Phase != domain.RuntimePhaseUnavailable {
			return runtimeConflict(errors.New("model settings runtime is not unavailable"))
		}
		row, rowErr = gormRawRow(callbackCtx, database, `UPDATE ops.model_settings_runtime
SET phase='active',heartbeat_at=clock_timestamp()
WHERE role=? AND instance_id=?::uuid AND applied_revision=?
  AND rollout_id IS NULL AND phase='unavailable' AND heartbeat_at=?
RETURNING `+runtimeColumns, string(command.Role), string(command.InstanceID), command.AppliedRevision, existing.HeartbeatAt)
		if rowErr != nil {
			return classifyGORM(callbackCtx, rowErr)
		}
		record, scanErr = scanRuntime(row)
		if gormNoRows(scanErr) {
			return runtimeOwnershipLost(errors.New("model settings runtime availability CAS failed"))
		}
		if scanErr != nil {
			return classifyGORM(callbackCtx, scanErr)
		}
		return nil
	})
	if err != nil {
		return domain.RuntimeRecord{}, err
	}
	return record, nil
}

// SetRuntimePhase atomically binds active to the controlling rollout when quiescing,
// then requires that exact binding for later process-driven phase transitions.
func (repository *GORMRepository) SetRuntimePhase(ctx context.Context, command application.RuntimePhaseCommand) (record domain.RuntimeRecord, err error) {
	if err = repository.ready(ctx); err != nil {
		return domain.RuntimeRecord{}, err
	}
	if !domain.ValidRuntimeRole(command.Role) || !validID(command.InstanceID) || command.RolloutID == nil || !validID(*command.RolloutID) {
		return domain.RuntimeRecord{}, invalid(errors.New("model settings runtime phase command is invalid"))
	}
	if err = domain.ValidateRuntimeTransition(command.ExpectedPhase, command.NextPhase); err != nil {
		return domain.RuntimeRecord{}, err
	}
	err = repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, _ foundation.TransactionScope, database *gorm.DB) error {
		state, stateErr := gormLoadState(callbackCtx, database, `FOR SHARE`)
		if stateErr != nil {
			return stateErr
		}
		if !state.rolloutID.Valid || state.rolloutID.String != string(*command.RolloutID) {
			return runtimeConflict(errors.New("model settings runtime rollout changed"))
		}
		var existingRollout any = string(*command.RolloutID)
		var nextRollout any = string(*command.RolloutID)
		switch {
		case command.ExpectedPhase == domain.RuntimePhaseActive && command.NextPhase == domain.RuntimePhaseQuiescing:
			if state.phase != string(domain.RolloutPhaseDraining) || !state.previousActive.Valid {
				return runtimeConflict(errors.New("model settings runtime cannot begin quiescing"))
			}
			existingRollout = nil
		case command.ExpectedPhase == domain.RuntimePhaseQuiescing && command.NextPhase == domain.RuntimePhaseQuiesced:
			if state.phase != string(domain.RolloutPhaseDraining) {
				return runtimeConflict(errors.New("model settings runtime cannot finish quiescing"))
			}
		case command.ExpectedPhase == domain.RuntimePhasePrepared && command.NextPhase == domain.RuntimePhaseVerifying:
			if state.phase != string(domain.RolloutPhaseVerifying) {
				return runtimeConflict(errors.New("model settings runtime cannot verify"))
			}
		default:
			return runtimeConflict(errors.New("model settings runtime transition is invalid"))
		}
		row, rowErr := gormRawRow(callbackCtx, database, `UPDATE ops.model_settings_runtime
SET rollout_id=?::uuid,phase=?,heartbeat_at=clock_timestamp()
WHERE role=? AND instance_id=?::uuid AND phase=?
  AND rollout_id IS NOT DISTINCT FROM ?::uuid
RETURNING `+runtimeColumns,
			nextRollout, string(command.NextPhase), string(command.Role), string(command.InstanceID),
			string(command.ExpectedPhase), existingRollout)
		if rowErr != nil {
			return classifyGORM(callbackCtx, rowErr)
		}
		record, stateErr = scanRuntime(row)
		if gormNoRows(stateErr) {
			return runtimeConflict(errors.New("model settings runtime phase ownership changed"))
		}
		if stateErr != nil {
			return classifyGORM(callbackCtx, stateErr)
		}
		return nil
	})
	if err != nil {
		return domain.RuntimeRecord{}, err
	}
	return record, nil
}

// CheckEnqueue validates workflow admission inside the caller-owned transaction.
func (repository *GORMRepository) CheckEnqueue(ctx context.Context, scope foundation.TransactionScope) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	if nilInterface(scope) {
		return invalid(errors.New("model settings enqueue transaction is invalid"))
	}
	database, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return invalid(err)
	}
	row, err := gormRawRow(ctx, database, `SELECT phase FROM ops.model_settings_state
WHERE singleton=true FOR SHARE`)
	if err != nil {
		return classifyGORM(ctx, err)
	}
	var phase string
	if err = row.Scan(&phase); gormNoRows(err) {
		return corrupt(errors.New("model settings singleton state is missing"))
	}
	if err != nil {
		return classifyGORM(ctx, err)
	}
	if phase != string(domain.RolloutPhaseIdle) && phase != string(domain.RolloutPhaseFailed) && phase != string(domain.RolloutPhasePreparing) {
		return enqueuePaused(errors.New("model settings rollout blocks workflow enqueue"))
	}
	return nil
}
