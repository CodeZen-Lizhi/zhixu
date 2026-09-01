package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	"gorm.io/gorm"
)

// RegisterParticipant claims a role candidate or replaces only a stale binding.
func (repository *GORMRepository) RegisterParticipant(ctx context.Context, registration application.ParticipantRegistration) (record domain.ParticipantRecord, err error) {
	if registration.StaleAfter == 0 {
		registration.StaleAfter = defaultRuntimeTakeoverStaleAfter
	}
	if err = repository.ready(ctx); err != nil {
		return domain.ParticipantRecord{}, err
	}
	if !validParticipantIdentity(registration.RolloutID, registration.Role, registration.InstanceID, registration.TargetRevision) ||
		(registration.InitialPhase != domain.ParticipantPhasePreparing && registration.InitialPhase != domain.ParticipantPhaseActivated) ||
		!validFreshWithin(registration.StaleAfter) {
		return domain.ParticipantRecord{}, invalid(errors.New("model settings participant registration is invalid"))
	}
	err = repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, _ foundation.TransactionScope, database *gorm.DB) error {
		state, stateErr := gormLoadState(callbackCtx, database, `FOR UPDATE`)
		if stateErr != nil {
			return stateErr
		}
		if stateErr = authorizeParticipantRegistration(state, registration); stateErr != nil {
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
		runtime, scanErr := scanRuntime(row)
		if gormNoRows(scanErr) {
			return runtimeOwnershipLost(errors.New("model settings serving owner is missing"))
		}
		if scanErr != nil {
			return classifyGORM(callbackCtx, scanErr)
		}
		if runtime.InstanceID != registration.InstanceID || runtime.Phase != domain.RuntimePhaseActive || runtime.RolloutID != nil {
			return runtimeOwnershipLost(errors.New("model settings serving ownership changed"))
		}
		if registration.InitialPhase == domain.ParticipantPhasePreparing && runtime.AppliedRevision != state.previousActive.Int64 {
			return runtimeNotReady(errors.New("model settings serving runtime is not on previous active revision"))
		}
		if registration.InitialPhase == domain.ParticipantPhaseActivated && runtime.AppliedRevision != state.targetRevision.Int64 {
			return runtimeNotReady(errors.New("model settings serving runtime has not loaded active target"))
		}
		row, rowErr = gormRawRow(callbackCtx, database, `SELECT `+participantColumns+`
FROM ops.model_settings_rollout_participant WHERE rollout_id=?::uuid AND role=? FOR UPDATE`,
			string(registration.RolloutID), string(registration.Role))
		if rowErr != nil {
			return classifyGORM(callbackCtx, rowErr)
		}
		existing, scanErr := scanParticipant(row)
		switch {
		case gormNoRows(scanErr):
			preparedExpression := `NULL`
			activatedExpression := `NULL`
			if registration.InitialPhase == domain.ParticipantPhaseActivated {
				preparedExpression = `clock_timestamp()`
				activatedExpression = `clock_timestamp()`
			}
			row, rowErr = gormRawRow(callbackCtx, database, `INSERT INTO ops.model_settings_rollout_participant(
rollout_id,role,instance_id,target_revision,phase,heartbeat_at,last_error_code,last_error_retryable,
version,prepared_at,activated_at,retired_at)
VALUES(?::uuid,?,?::uuid,?,?,clock_timestamp(),NULL,false,1,`+preparedExpression+`,`+activatedExpression+`,NULL)
RETURNING `+participantColumns,
				string(registration.RolloutID), string(registration.Role), string(registration.InstanceID),
				registration.TargetRevision, string(registration.InitialPhase))
			if rowErr != nil {
				return classifyGORM(callbackCtx, rowErr)
			}
			record, scanErr = scanParticipant(row)
		case scanErr != nil:
			return classifyGORM(callbackCtx, scanErr)
		case existing.InstanceID == registration.InstanceID && existing.TargetRevision == registration.TargetRevision:
			row, rowErr = gormRawRow(callbackCtx, database, `UPDATE ops.model_settings_rollout_participant
SET heartbeat_at=clock_timestamp(),version=version+1
WHERE rollout_id=?::uuid AND role=? AND instance_id=?::uuid AND version=?
RETURNING `+participantColumns,
				string(registration.RolloutID), string(registration.Role), string(registration.InstanceID), existing.Version)
			if rowErr != nil {
				return classifyGORM(callbackCtx, rowErr)
			}
			record, scanErr = scanParticipant(row)
		case existing.TargetRevision != registration.TargetRevision || !existing.HeartbeatAt.Before(now.Add(-registration.StaleAfter)):
			return participantConflict(errors.New("model settings participant owner is fresh or target changed"))
		default:
			preparedExpression := `NULL`
			activatedExpression := `NULL`
			if registration.InitialPhase == domain.ParticipantPhaseActivated {
				preparedExpression = `clock_timestamp()`
				activatedExpression = `clock_timestamp()`
			}
			row, rowErr = gormRawRow(callbackCtx, database, `UPDATE ops.model_settings_rollout_participant
SET instance_id=?::uuid,phase=?,heartbeat_at=clock_timestamp(),last_error_code=NULL,
    last_error_retryable=false,version=version+1,prepared_at=`+preparedExpression+`,
    activated_at=`+activatedExpression+`,retired_at=NULL
WHERE rollout_id=?::uuid AND role=? AND version=?
RETURNING `+participantColumns,
				string(registration.InstanceID), string(registration.InitialPhase), string(registration.RolloutID),
				string(registration.Role), existing.Version)
			if rowErr != nil {
				return classifyGORM(callbackCtx, rowErr)
			}
			record, scanErr = scanParticipant(row)
		}
		if gormNoRows(scanErr) {
			return participantConflict(errors.New("model settings participant registration CAS failed"))
		}
		if scanErr != nil {
			return classifyGORM(callbackCtx, scanErr)
		}
		return nil
	})
	if err != nil {
		return domain.ParticipantRecord{}, err
	}
	return record, nil
}

// HeartbeatParticipant renews an exact role/instance/phase/version binding.
func (repository *GORMRepository) HeartbeatParticipant(ctx context.Context, heartbeat application.ParticipantHeartbeat) (record domain.ParticipantRecord, err error) {
	if err = repository.ready(ctx); err != nil {
		return domain.ParticipantRecord{}, err
	}
	if !validParticipantIdentity(heartbeat.RolloutID, heartbeat.Role, heartbeat.InstanceID, heartbeat.TargetRevision) ||
		!domain.ValidParticipantPhase(heartbeat.ExpectedPhase) || heartbeat.ExpectedVersion <= 0 {
		return domain.ParticipantRecord{}, invalid(errors.New("model settings participant heartbeat is invalid"))
	}
	err = repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, _ foundation.TransactionScope, database *gorm.DB) error {
		state, stateErr := gormLoadState(callbackCtx, database, `FOR UPDATE`)
		if stateErr != nil {
			return stateErr
		}
		if !state.rolloutID.Valid || state.rolloutID.String != string(heartbeat.RolloutID) ||
			!state.targetRevision.Valid || state.targetRevision.Int64 != heartbeat.TargetRevision || !domain.ActiveActivationPhase(domain.RolloutPhase(state.phase)) {
			return participantConflict(errors.New("model settings participant activation changed"))
		}
		row, rowErr := gormRawRow(callbackCtx, database, `SELECT `+runtimeColumns+`
FROM ops.model_settings_runtime WHERE role=? FOR UPDATE`, string(heartbeat.Role))
		if rowErr != nil {
			return classifyGORM(callbackCtx, rowErr)
		}
		runtime, scanErr := scanRuntime(row)
		if gormNoRows(scanErr) {
			return runtimeOwnershipLost(errors.New("model settings serving owner is missing"))
		}
		if scanErr != nil {
			return classifyGORM(callbackCtx, scanErr)
		}
		if runtime.InstanceID != heartbeat.InstanceID {
			return runtimeOwnershipLost(errors.New("model settings serving ownership changed"))
		}
		row, rowErr = gormRawRow(callbackCtx, database, `UPDATE ops.model_settings_rollout_participant
SET heartbeat_at=clock_timestamp(),version=version+1
WHERE rollout_id=?::uuid AND role=? AND instance_id=?::uuid AND target_revision=?
  AND phase=? AND version=?
RETURNING `+participantColumns, string(heartbeat.RolloutID), string(heartbeat.Role), string(heartbeat.InstanceID),
			heartbeat.TargetRevision, string(heartbeat.ExpectedPhase), heartbeat.ExpectedVersion)
		if rowErr != nil {
			return classifyGORM(callbackCtx, rowErr)
		}
		record, scanErr = scanParticipant(row)
		if gormNoRows(scanErr) {
			return participantConflict(errors.New("model settings participant heartbeat CAS failed"))
		}
		if scanErr != nil {
			return classifyGORM(callbackCtx, scanErr)
		}
		return nil
	})
	if err != nil {
		return domain.ParticipantRecord{}, err
	}
	return record, nil
}

// TransitionParticipant changes one role-local phase; activation itself uses AcknowledgeActivation.
func (repository *GORMRepository) TransitionParticipant(ctx context.Context, command application.ParticipantTransitionCommand) (record domain.ParticipantRecord, err error) {
	if err = repository.ready(ctx); err != nil {
		return domain.ParticipantRecord{}, err
	}
	if !validParticipantIdentity(command.RolloutID, command.Role, command.InstanceID, command.TargetRevision) || command.ExpectedVersion <= 0 {
		return domain.ParticipantRecord{}, invalid(errors.New("model settings participant transition is invalid"))
	}
	if err = domain.ValidateParticipantTransition(command.ExpectedPhase, command.NextPhase); err != nil {
		return domain.ParticipantRecord{}, err
	}
	if command.NextPhase == domain.ParticipantPhaseActivated {
		return domain.ParticipantRecord{}, invalid(errors.New("model settings participant activation requires acknowledgement"))
	}
	if command.NextPhase == domain.ParticipantPhaseFailed {
		if !canonicalErrorCode(command.ErrorCode) {
			return domain.ParticipantRecord{}, invalid(errors.New("model settings participant failure code is invalid"))
		}
	} else if command.ErrorCode != "" || command.ErrorRetryable {
		return domain.ParticipantRecord{}, invalid(errors.New("model settings participant diagnostic is invalid"))
	}
	err = repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, _ foundation.TransactionScope, database *gorm.DB) error {
		state, stateErr := gormLoadState(callbackCtx, database, `FOR UPDATE`)
		if stateErr != nil {
			return stateErr
		}
		if stateErr = authorizeParticipantTransition(state, command); stateErr != nil {
			return stateErr
		}
		row, rowErr := gormRawRow(callbackCtx, database, `SELECT `+runtimeColumns+`
FROM ops.model_settings_runtime WHERE role=? FOR UPDATE`, string(command.Role))
		if rowErr != nil {
			return classifyGORM(callbackCtx, rowErr)
		}
		runtime, scanErr := scanRuntime(row)
		if gormNoRows(scanErr) {
			return runtimeOwnershipLost(errors.New("model settings serving owner is missing"))
		}
		if scanErr != nil {
			return classifyGORM(callbackCtx, scanErr)
		}
		if runtime.InstanceID != command.InstanceID {
			return runtimeOwnershipLost(errors.New("model settings serving ownership changed"))
		}
		if command.NextPhase == domain.ParticipantPhaseRetired &&
			(runtime.RolloutID != nil || runtime.Phase != domain.RuntimePhaseActive || runtime.AppliedRevision != command.TargetRevision) {
			return runtimeNotReady(errors.New("model settings serving runtime is not on retired participant target"))
		}
		preparedAt := `prepared_at`
		retiredAt := `retired_at`
		if command.NextPhase == domain.ParticipantPhasePrepared {
			preparedAt = `clock_timestamp()`
		}
		if command.NextPhase == domain.ParticipantPhaseRetired {
			retiredAt = `clock_timestamp()`
		}
		row, rowErr = gormRawRow(callbackCtx, database, `UPDATE ops.model_settings_rollout_participant
SET phase=?,heartbeat_at=clock_timestamp(),last_error_code=?,last_error_retryable=?,
    prepared_at=`+preparedAt+`,retired_at=`+retiredAt+`,version=version+1
WHERE rollout_id=?::uuid AND role=? AND instance_id=?::uuid AND target_revision=?
  AND phase=? AND version=?
RETURNING `+participantColumns,
			string(command.NextPhase), nullableErrorCode(command.ErrorCode), command.ErrorRetryable,
			string(command.RolloutID), string(command.Role), string(command.InstanceID), command.TargetRevision,
			string(command.ExpectedPhase), command.ExpectedVersion)
		if rowErr != nil {
			return classifyGORM(callbackCtx, rowErr)
		}
		record, scanErr = scanParticipant(row)
		if gormNoRows(scanErr) {
			return participantConflict(errors.New("model settings participant transition CAS failed"))
		}
		if scanErr != nil {
			return classifyGORM(callbackCtx, scanErr)
		}
		return nil
	})
	if err != nil {
		return domain.ParticipantRecord{}, err
	}
	return record, nil
}
