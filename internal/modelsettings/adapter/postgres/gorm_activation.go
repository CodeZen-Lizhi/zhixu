package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	localmodelruntime "github.com/CodeZen-Lizhi/zhixu/internal/localmodelruntime"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	"gorm.io/gorm"
)

// StartActivation fixes an exact desired revision or replays its live operation.
func (repository *GORMRepository) StartActivation(ctx context.Context, command application.StartActivationCommand) (result application.StartActivationResult, err error) {
	if err = repository.ready(ctx); err != nil {
		return application.StartActivationResult{}, err
	}
	if command.FreshWithin == 0 {
		command.FreshWithin = defaultRuntimeTakeoverStaleAfter
	}
	if !validID(command.RolloutID) || command.TargetRevision < 0 ||
		command.ExpectedDesiredRevision != command.TargetRevision || command.ExpectedStateVersion <= 0 ||
		!validLease(command.LeaseDuration) || !validFreshWithin(command.FreshWithin) {
		return application.StartActivationResult{}, invalid(errors.New("model settings activation start is invalid"))
	}
	err = repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope, database *gorm.DB) error {
		state, stateErr := gormLoadState(callbackCtx, database, `FOR UPDATE`)
		if stateErr != nil {
			return stateErr
		}
		phase := domain.RolloutPhase(state.phase)
		if domain.ActiveActivationPhase(phase) {
			if state.targetRevision.Valid && state.targetRevision.Int64 == command.TargetRevision {
				if prepareErr := repository.gormEnsureActivationPreparation(callbackCtx, scope, database, command.RolloutID, command.TargetRevision); prepareErr != nil {
					return prepareErr
				}
				existing, rolloutErr := state.rollout()
				if rolloutErr != nil {
					return rolloutErr
				}
				result = application.StartActivationResult{State: existing, Replayed: true}
				return nil
			}
			return activationConflict(errors.New("another model settings activation is live"))
		}
		if phase != domain.RolloutPhaseIdle && phase != domain.RolloutPhaseFailed {
			return activationConflict(errors.New("model settings activation state cannot start"))
		}
		if state.activeRevision == command.TargetRevision {
			if phase != domain.RolloutPhaseIdle {
				return activationConflict(errors.New("model settings active target has a terminal activation record"))
			}
			if state.desiredRevision != command.ExpectedDesiredRevision {
				return activationConflict(errors.New("model settings desired revision changed"))
			}
			ready, readyErr := gormServingRuntimesReady(callbackCtx, database, command.TargetRevision, command.FreshWithin)
			if readyErr != nil {
				return readyErr
			}
			if !ready {
				return runtimeNotReady(errors.New("model settings active target is degraded"))
			}
			existing, rolloutErr := state.rollout()
			if rolloutErr != nil {
				return rolloutErr
			}
			result = application.StartActivationResult{State: existing, Replayed: true}
			return nil
		}
		if state.version != command.ExpectedStateVersion || state.desiredRevision != command.ExpectedDesiredRevision {
			return activationConflict(errors.New("model settings desired revision or state version changed"))
		}
		row, rowErr := gormRawRow(callbackCtx, database, `UPDATE ops.model_settings_state
SET rollout_id=?::uuid,target_revision=?,previous_active_revision=active_revision,
    phase='preparing',lease_expires_at=clock_timestamp()+(?::bigint*interval '1 microsecond'),
    last_error_code=NULL,version=version+1,updated_at=clock_timestamp()
WHERE singleton=true AND version=? AND desired_revision=? AND phase IN ('idle','failed')
RETURNING `+stateColumns,
			string(command.RolloutID), command.TargetRevision, command.LeaseDuration.Microseconds(),
			command.ExpectedStateVersion, command.TargetRevision)
		if rowErr != nil {
			return classifyGORM(callbackCtx, rowErr)
		}
		updated, scanErr := gormScanState(row)
		if gormNoRows(scanErr) {
			return activationConflict(errors.New("model settings activation start CAS failed"))
		}
		if scanErr != nil {
			return classifyGORM(callbackCtx, scanErr)
		}
		rollout, rolloutErr := updated.rollout()
		if rolloutErr != nil {
			return rolloutErr
		}
		if prepareErr := repository.gormEnsureActivationPreparation(callbackCtx, scope, database, command.RolloutID, command.TargetRevision); prepareErr != nil {
			return prepareErr
		}
		result = application.StartActivationResult{State: rollout}
		return nil
	})
	if err != nil {
		return application.StartActivationResult{}, err
	}
	return result, nil
}

// RenewActivation renews one exact phase/version lease with database time.
func (repository *GORMRepository) RenewActivation(ctx context.Context, command application.RenewActivationCommand) (domain.RolloutState, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.RolloutState{}, err
	}
	if !validID(command.RolloutID) || !domain.ActiveActivationPhase(command.ExpectedPhase) ||
		command.ExpectedVersion <= 0 || !validLease(command.LeaseDuration) {
		return domain.RolloutState{}, invalid(errors.New("model settings activation renewal is invalid"))
	}
	return repository.gormMutateActivationLease(ctx, command.RolloutID, command.ExpectedPhase, command.ExpectedVersion, command.LeaseDuration)
}

func (repository *GORMRepository) gormMutateActivationLease(
	ctx context.Context,
	rolloutID foundation.ID,
	expectedPhase domain.RolloutPhase,
	expectedVersion int64,
	duration time.Duration,
) (result domain.RolloutState, err error) {
	err = repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, _ foundation.TransactionScope, database *gorm.DB) error {
		state, stateErr := gormLoadState(callbackCtx, database, `FOR UPDATE`)
		if stateErr != nil {
			return stateErr
		}
		now, nowErr := gormDatabaseNow(callbackCtx, database)
		if nowErr != nil {
			return nowErr
		}
		if !sameActivation(state, rolloutID, expectedPhase, expectedVersion) {
			return activationConflict(errors.New("model settings activation ownership changed"))
		}
		if !state.leaseExpiresAt.Valid || !state.leaseExpiresAt.Time.After(now) {
			return activationLeaseExpired(errors.New("model settings activation lease expired"))
		}
		row, rowErr := gormRawRow(callbackCtx, database, `UPDATE ops.model_settings_state
SET lease_expires_at=clock_timestamp()+(?::bigint*interval '1 microsecond'),
    version=version+1,updated_at=clock_timestamp()
WHERE singleton=true AND rollout_id=?::uuid AND phase=? AND version=?
RETURNING `+stateColumns, duration.Microseconds(), string(rolloutID), string(expectedPhase), expectedVersion)
		if rowErr != nil {
			return classifyGORM(callbackCtx, rowErr)
		}
		updated, scanErr := gormScanState(row)
		if gormNoRows(scanErr) {
			return activationConflict(errors.New("model settings activation renewal CAS failed"))
		}
		if scanErr != nil {
			return classifyGORM(callbackCtx, scanErr)
		}
		result, scanErr = updated.rollout()
		return scanErr
	})
	if err != nil {
		return domain.RolloutState{}, err
	}
	return result, nil
}

// AdvanceActivation advances preparing to arming after locking and verifying both roles.
func (repository *GORMRepository) AdvanceActivation(ctx context.Context, command application.AdvanceActivationCommand) (domain.RolloutState, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.RolloutState{}, err
	}
	if !validID(command.RolloutID) || command.ExpectedPhase != domain.RolloutPhasePreparing ||
		command.NextPhase != domain.RolloutPhaseArming || command.ExpectedVersion <= 0 ||
		!validLease(command.LeaseDuration) || !validFreshWithin(command.FreshWithin) {
		return domain.RolloutState{}, invalid(errors.New("model settings activation advance is invalid"))
	}
	return repository.gormAdvanceActivation(ctx, command.RolloutID, command.ExpectedVersion, command.FreshWithin, command.LeaseDuration)
}

func (repository *GORMRepository) gormAdvanceActivation(
	ctx context.Context,
	rolloutID foundation.ID,
	expectedVersion int64,
	freshWithin time.Duration,
	leaseDuration time.Duration,
) (result domain.RolloutState, err error) {
	err = repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope, database *gorm.DB) error {
		state, stateErr := gormLoadState(callbackCtx, database, `FOR UPDATE`)
		if stateErr != nil {
			return stateErr
		}
		now, nowErr := gormDatabaseNow(callbackCtx, database)
		if nowErr != nil {
			return nowErr
		}
		if !sameActivation(state, rolloutID, domain.RolloutPhasePreparing, expectedVersion) {
			return activationConflict(errors.New("model settings activation prepare CAS failed"))
		}
		if !state.leaseExpiresAt.Valid || !state.leaseExpiresAt.Time.After(now) {
			return activationLeaseExpired(errors.New("model settings activation lease expired"))
		}
		if verifyErr := gormVerifyActivationRoles(callbackCtx, database, state, now, freshWithin, domain.ParticipantPhasePrepared, state.previousActive.Int64); verifyErr != nil {
			return verifyErr
		}
		if prepareErr := repository.gormVerifyLocalPreparation(callbackCtx, scope, database, state.rolloutID.String); prepareErr != nil {
			return prepareErr
		}
		row, rowErr := gormRawRow(callbackCtx, database, `UPDATE ops.model_settings_state
SET phase='arming',lease_expires_at=clock_timestamp()+(?::bigint*interval '1 microsecond'),
    version=version+1,updated_at=clock_timestamp()
WHERE singleton=true AND rollout_id=?::uuid AND phase='preparing' AND version=?
RETURNING `+stateColumns, leaseDuration.Microseconds(), string(rolloutID), expectedVersion)
		if rowErr != nil {
			return classifyGORM(callbackCtx, rowErr)
		}
		updated, scanErr := gormScanState(row)
		if gormNoRows(scanErr) {
			return activationConflict(errors.New("model settings activation advance CAS failed"))
		}
		if scanErr != nil {
			return classifyGORM(callbackCtx, scanErr)
		}
		result, scanErr = updated.rollout()
		return scanErr
	})
	if err != nil {
		return domain.RolloutState{}, err
	}
	return result, nil
}

// FailActivation terminates only preparing or arming work and preserves active.
func (repository *GORMRepository) FailActivation(ctx context.Context, command application.FailActivationCommand) (result domain.RolloutState, err error) {
	if err = repository.ready(ctx); err != nil {
		return domain.RolloutState{}, err
	}
	if !validID(command.RolloutID) || command.ExpectedVersion <= 0 ||
		(command.ExpectedPhase != domain.RolloutPhasePreparing && command.ExpectedPhase != domain.RolloutPhaseArming) ||
		!canonicalErrorCode(command.ErrorCode) {
		return domain.RolloutState{}, invalid(errors.New("model settings activation failure is invalid"))
	}
	err = repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope, database *gorm.DB) error {
		state, stateErr := gormLoadState(callbackCtx, database, `FOR UPDATE`)
		if stateErr != nil {
			return stateErr
		}
		if !sameActivation(state, command.RolloutID, command.ExpectedPhase, command.ExpectedVersion) {
			return activationConflict(errors.New("model settings activation failure CAS failed"))
		}
		updated, updateErr := gormFailActivationLocked(callbackCtx, database, state, command.ErrorCode)
		if updateErr != nil {
			return updateErr
		}
		lifecycle, lifecycleErr := repository.gormScopedLocalLifecycle(scope)
		if lifecycleErr != nil {
			return lifecycleErr
		}
		if lifecycle != nil {
			if _, completeErr := lifecycle.CompleteActivationPreparation(callbackCtx, command.RolloutID, command.ErrorCode, false); completeErr != nil && !gormNoRows(completeErr) {
				return completeErr
			}
		}
		result, updateErr = updated.rollout()
		return updateErr
	})
	if err != nil {
		return domain.RolloutState{}, err
	}
	return result, nil
}

// CommitActivation verifies both armed roles and performs the sole active pointer change.
func (repository *GORMRepository) CommitActivation(ctx context.Context, command application.CommitActivationCommand) (result domain.RolloutState, err error) {
	if err = repository.ready(ctx); err != nil {
		return domain.RolloutState{}, err
	}
	if !validID(command.RolloutID) || command.ExpectedVersion <= 0 ||
		!validFreshWithin(command.FreshWithin) || !validLease(command.LeaseDuration) {
		return domain.RolloutState{}, invalid(errors.New("model settings activation commit is invalid"))
	}
	err = repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, _ foundation.TransactionScope, database *gorm.DB) error {
		state, stateErr := gormLoadState(callbackCtx, database, `FOR UPDATE`)
		if stateErr != nil {
			return stateErr
		}
		now, nowErr := gormDatabaseNow(callbackCtx, database)
		if nowErr != nil {
			return nowErr
		}
		if !sameActivation(state, command.RolloutID, domain.RolloutPhaseArming, command.ExpectedVersion) {
			return activationConflict(errors.New("model settings activation commit CAS failed"))
		}
		if !state.leaseExpiresAt.Valid || !state.leaseExpiresAt.Time.After(now) {
			return activationLeaseExpired(errors.New("model settings activation lease expired"))
		}
		if verifyErr := gormVerifyActivationRoles(callbackCtx, database, state, now, command.FreshWithin, domain.ParticipantPhaseArmed, state.previousActive.Int64); verifyErr != nil {
			return verifyErr
		}
		row, rowErr := gormRawRow(callbackCtx, database, `UPDATE ops.model_settings_state
SET active_revision=target_revision,phase='activating',
    lease_expires_at=clock_timestamp()+(?::bigint*interval '1 microsecond'),
    version=version+1,updated_at=clock_timestamp()
WHERE singleton=true AND rollout_id=?::uuid AND phase='arming' AND version=?
RETURNING `+stateColumns, command.LeaseDuration.Microseconds(), string(command.RolloutID), command.ExpectedVersion)
		if rowErr != nil {
			return classifyGORM(callbackCtx, rowErr)
		}
		updated, scanErr := gormScanState(row)
		if gormNoRows(scanErr) {
			return activationConflict(errors.New("model settings activation commit CAS failed"))
		}
		if scanErr != nil {
			return classifyGORM(callbackCtx, scanErr)
		}
		result, scanErr = updated.rollout()
		return scanErr
	})
	if err != nil {
		return domain.RolloutState{}, err
	}
	return result, nil
}

// AcknowledgeActivation atomically updates one serving row and its armed participant.
func (repository *GORMRepository) AcknowledgeActivation(ctx context.Context, command application.ActivationAcknowledgement) (result domain.ParticipantRecord, err error) {
	if err = repository.ready(ctx); err != nil {
		return domain.ParticipantRecord{}, err
	}
	if !validID(command.RolloutID) || !domain.ValidRuntimeRole(command.Role) || !validID(command.InstanceID) ||
		command.TargetRevision < 0 || command.ExpectedStateVersion <= 0 || command.ExpectedParticipantVersion <= 0 {
		return domain.ParticipantRecord{}, invalid(errors.New("model settings activation acknowledgement is invalid"))
	}
	err = repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, _ foundation.TransactionScope, database *gorm.DB) error {
		state, stateErr := gormLoadState(callbackCtx, database, `FOR UPDATE`)
		if stateErr != nil {
			return stateErr
		}
		if !sameActivation(state, command.RolloutID, domain.RolloutPhaseActivating, command.ExpectedStateVersion) ||
			!state.targetRevision.Valid || state.targetRevision.Int64 != command.TargetRevision {
			return activationConflict(errors.New("model settings activation acknowledgement state changed"))
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
		row, rowErr = gormRawRow(callbackCtx, database, `SELECT `+participantColumns+`
FROM ops.model_settings_rollout_participant WHERE rollout_id=?::uuid AND role=? FOR UPDATE`,
			string(command.RolloutID), string(command.Role))
		if rowErr != nil {
			return classifyGORM(callbackCtx, rowErr)
		}
		participant, scanErr := scanParticipant(row)
		if gormNoRows(scanErr) {
			return participantConflict(errors.New("model settings activation participant is missing"))
		}
		if scanErr != nil {
			return classifyGORM(callbackCtx, scanErr)
		}
		if runtime.InstanceID != command.InstanceID || runtime.Phase != domain.RuntimePhaseActive || runtime.RolloutID != nil {
			return runtimeOwnershipLost(errors.New("model settings serving ownership changed"))
		}
		if participant.InstanceID != command.InstanceID || participant.TargetRevision != command.TargetRevision {
			return participantConflict(errors.New("model settings activation participant ownership changed"))
		}
		if participant.Phase == domain.ParticipantPhaseActivated && runtime.AppliedRevision == command.TargetRevision {
			result = participant
			return nil
		}
		if participant.Phase != domain.ParticipantPhaseArmed || participant.Version != command.ExpectedParticipantVersion ||
			runtime.AppliedRevision != state.previousActive.Int64 {
			return participantConflict(errors.New("model settings activation acknowledgement CAS failed"))
		}
		update := database.WithContext(callbackCtx).Exec(`UPDATE ops.model_settings_runtime
SET applied_revision=?,applied_at=clock_timestamp(),heartbeat_at=clock_timestamp()
WHERE role=? AND instance_id=?::uuid AND applied_revision=? AND phase='active' AND rollout_id IS NULL`,
			command.TargetRevision, string(command.Role), string(command.InstanceID), state.previousActive.Int64)
		if update.Error != nil {
			return classifyGORM(callbackCtx, update.Error)
		}
		if update.RowsAffected != 1 {
			return runtimeOwnershipLost(errors.New("model settings runtime activation CAS failed"))
		}
		row, rowErr = gormRawRow(callbackCtx, database, `UPDATE ops.model_settings_rollout_participant
SET phase='activated',heartbeat_at=clock_timestamp(),activated_at=clock_timestamp(),version=version+1
WHERE rollout_id=?::uuid AND role=? AND instance_id=?::uuid AND target_revision=?
  AND phase='armed' AND version=?
RETURNING `+participantColumns, string(command.RolloutID), string(command.Role), string(command.InstanceID),
			command.TargetRevision, command.ExpectedParticipantVersion)
		if rowErr != nil {
			return classifyGORM(callbackCtx, rowErr)
		}
		result, scanErr = scanParticipant(row)
		if gormNoRows(scanErr) {
			return participantConflict(errors.New("model settings activation acknowledgement participant CAS failed"))
		}
		if scanErr != nil {
			return classifyGORM(callbackCtx, scanErr)
		}
		return nil
	})
	if err != nil {
		return domain.ParticipantRecord{}, err
	}
	return result, nil
}

// FinalizeActivation clears the binding after both current owners acknowledged target.
func (repository *GORMRepository) FinalizeActivation(ctx context.Context, command application.FinalizeActivationCommand) (result domain.RolloutState, err error) {
	if err = repository.ready(ctx); err != nil {
		return domain.RolloutState{}, err
	}
	if !validID(command.RolloutID) || command.ExpectedVersion <= 0 || !validFreshWithin(command.FreshWithin) {
		return domain.RolloutState{}, invalid(errors.New("model settings activation finalize is invalid"))
	}
	err = repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope, database *gorm.DB) error {
		state, stateErr := gormLoadState(callbackCtx, database, `FOR UPDATE`)
		if stateErr != nil {
			return stateErr
		}
		now, nowErr := gormDatabaseNow(callbackCtx, database)
		if nowErr != nil {
			return nowErr
		}
		if !sameActivation(state, command.RolloutID, domain.RolloutPhaseActivating, command.ExpectedVersion) {
			return activationConflict(errors.New("model settings activation finalize CAS failed"))
		}
		if verifyErr := gormVerifyActivationRoles(callbackCtx, database, state, now, command.FreshWithin, domain.ParticipantPhaseActivated, state.targetRevision.Int64); verifyErr != nil {
			return verifyErr
		}
		row, rowErr := gormRawRow(callbackCtx, database, `UPDATE ops.model_settings_state
SET rollout_id=NULL,target_revision=NULL,previous_active_revision=NULL,phase='idle',
    lease_expires_at=NULL,last_error_code=NULL,version=version+1,updated_at=clock_timestamp()
WHERE singleton=true AND rollout_id=?::uuid AND phase='activating' AND version=?
RETURNING `+stateColumns, string(command.RolloutID), command.ExpectedVersion)
		if rowErr != nil {
			return classifyGORM(callbackCtx, rowErr)
		}
		updated, scanErr := gormScanState(row)
		if gormNoRows(scanErr) {
			return activationConflict(errors.New("model settings activation finalize CAS failed"))
		}
		if scanErr != nil {
			return classifyGORM(callbackCtx, scanErr)
		}
		lifecycle, lifecycleErr := repository.gormScopedLocalLifecycle(scope)
		if lifecycleErr != nil {
			return lifecycleErr
		}
		if lifecycle != nil {
			if _, completeErr := lifecycle.CompleteActivationPreparation(callbackCtx, command.RolloutID, "", false); completeErr != nil && !gormNoRows(completeErr) {
				return completeErr
			}
		}
		result, scanErr = updated.rollout()
		return scanErr
	})
	if err != nil {
		return domain.RolloutState{}, err
	}
	return result, nil
}

// RecoverActivation fails expired pre-commit work and renews expired post-commit work.
func (repository *GORMRepository) RecoverActivation(ctx context.Context, command application.RecoverActivationCommand) (result domain.ActivationRecovery, err error) {
	if err = repository.ready(ctx); err != nil {
		return domain.ActivationRecovery{}, err
	}
	if !validLease(command.LeaseDuration) {
		return domain.ActivationRecovery{}, invalid(errors.New("model settings activation recovery is invalid"))
	}
	err = repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope, database *gorm.DB) error {
		state, stateErr := gormLoadState(callbackCtx, database, `FOR UPDATE`)
		if stateErr != nil {
			return stateErr
		}
		now, nowErr := gormDatabaseNow(callbackCtx, database)
		if nowErr != nil {
			return nowErr
		}
		action := domain.ActivationRecoveryNone
		phase := domain.RolloutPhase(state.phase)
		if domain.ActiveActivationPhase(phase) && state.leaseExpiresAt.Valid && !state.leaseExpiresAt.Time.After(now) {
			switch phase {
			case domain.RolloutPhasePreparing, domain.RolloutPhaseArming:
				state, stateErr = gormFailActivationLocked(callbackCtx, database, state, domain.ErrorCodeActivationLeaseExpired)
				action = domain.ActivationRecoveryFailedPreCommit
			case domain.RolloutPhaseActivating:
				row, rowErr := gormRawRow(callbackCtx, database, `UPDATE ops.model_settings_state
SET lease_expires_at=clock_timestamp()+(?::bigint*interval '1 microsecond'),
    version=version+1,updated_at=clock_timestamp()
WHERE singleton=true AND phase='activating' AND version=?
RETURNING `+stateColumns, command.LeaseDuration.Microseconds(), state.version)
				if rowErr != nil {
					return classifyGORM(callbackCtx, rowErr)
				}
				state, stateErr = gormScanState(row)
				if gormNoRows(stateErr) {
					return corrupt(errors.New("model settings singleton state is missing"))
				}
				action = domain.ActivationRecoveryAdoptedPostCommit
			}
			if stateErr != nil {
				return classifyGORM(callbackCtx, stateErr)
			}
			if action == domain.ActivationRecoveryFailedPreCommit && state.rolloutID.Valid {
				lifecycle, lifecycleErr := repository.gormScopedLocalLifecycle(scope)
				if lifecycleErr != nil {
					return lifecycleErr
				}
				if lifecycle != nil {
					if _, completeErr := lifecycle.CompleteActivationPreparation(callbackCtx, foundation.ID(state.rolloutID.String), domain.ErrorCodeActivationLeaseExpired, true); completeErr != nil && !gormNoRows(completeErr) {
						return completeErr
					}
				}
			}
		}
		rollout, rolloutErr := state.rollout()
		if rolloutErr != nil {
			return rolloutErr
		}
		result = domain.ActivationRecovery{State: rollout, Action: action}
		return nil
	})
	if err != nil {
		return domain.ActivationRecovery{}, err
	}
	return result, nil
}

func gormFailActivationLocked(ctx context.Context, database *gorm.DB, state stateRecord, errorCode string) (stateRecord, error) {
	if _, err := gormLockActivationRuntimes(ctx, database); err != nil {
		return stateRecord{}, err
	}
	for _, role := range []domain.RuntimeRole{domain.RuntimeRoleAPI, domain.RuntimeRoleWorker} {
		result := database.WithContext(ctx).Exec(`UPDATE ops.model_settings_rollout_participant
SET phase='aborted',heartbeat_at=clock_timestamp(),last_error_code=NULL,last_error_retryable=false,
    version=version+1
WHERE rollout_id=?::uuid AND role=? AND phase IN ('preparing','prepared','armed')`,
			state.rolloutID.String, string(role))
		if result.Error != nil {
			return stateRecord{}, classifyGORM(ctx, result.Error)
		}
	}
	row, err := gormRawRow(ctx, database, `UPDATE ops.model_settings_state
SET phase='failed',lease_expires_at=NULL,last_error_code=?,version=version+1,updated_at=clock_timestamp()
WHERE singleton=true AND rollout_id=?::uuid AND phase IN ('preparing','arming') AND version=?
RETURNING `+stateColumns, errorCode, state.rolloutID.String, state.version)
	if err != nil {
		return stateRecord{}, classifyGORM(ctx, err)
	}
	updated, err := gormScanState(row)
	if gormNoRows(err) {
		return stateRecord{}, activationConflict(errors.New("model settings activation failure CAS failed"))
	}
	if err != nil {
		return stateRecord{}, classifyGORM(ctx, err)
	}
	return updated, nil
}

func gormServingRuntimesReady(ctx context.Context, database *gorm.DB, revision int64, freshWithin time.Duration) (bool, error) {
	now, err := gormDatabaseNow(ctx, database)
	if err != nil {
		return false, err
	}
	runtimes, err := gormLockActivationRuntimes(ctx, database)
	if err != nil {
		return false, err
	}
	for _, role := range []domain.RuntimeRole{domain.RuntimeRoleAPI, domain.RuntimeRoleWorker} {
		runtime, ok := runtimes[role]
		if !ok || runtime.RolloutID != nil || runtime.Phase != domain.RuntimePhaseActive ||
			runtime.AppliedRevision != revision || runtime.HeartbeatAt.Before(now.Add(-freshWithin)) {
			return false, nil
		}
	}
	return true, nil
}

func gormVerifyActivationRoles(
	ctx context.Context,
	database *gorm.DB,
	state stateRecord,
	now time.Time,
	freshWithin time.Duration,
	phase domain.ParticipantPhase,
	appliedRevision int64,
) error {
	runtimes, err := gormLockActivationRuntimes(ctx, database)
	if err != nil {
		return err
	}
	participants, err := gormLockActivationParticipants(ctx, database, state.rolloutID.String)
	if err != nil {
		return err
	}
	for _, role := range []domain.RuntimeRole{domain.RuntimeRoleAPI, domain.RuntimeRoleWorker} {
		runtime, runtimeOK := runtimes[role]
		participant, participantOK := participants[role]
		if !runtimeOK || !participantOK || runtime.InstanceID != participant.InstanceID ||
			runtime.RolloutID != nil || runtime.Phase != domain.RuntimePhaseActive || runtime.AppliedRevision != appliedRevision ||
			participant.TargetRevision != state.targetRevision.Int64 || participant.Phase != phase ||
			runtime.HeartbeatAt.Before(now.Add(-freshWithin)) || participant.HeartbeatAt.Before(now.Add(-freshWithin)) {
			return runtimeNotReady(errors.New("both model settings activation roles must be fresh and ready"))
		}
	}
	return nil
}

func gormLockActivationRuntimes(ctx context.Context, database *gorm.DB) (map[domain.RuntimeRole]domain.RuntimeRecord, error) {
	rows, err := gormRawRows(ctx, database, `SELECT `+runtimeColumns+`
FROM ops.model_settings_runtime WHERE role IN ('api','worker') ORDER BY role FOR UPDATE`)
	if err != nil {
		return nil, classifyGORM(ctx, err)
	}
	defer rows.Close()
	result := make(map[domain.RuntimeRole]domain.RuntimeRecord, 2)
	for rows.Next() {
		record, scanErr := scanRuntime(rows)
		if scanErr != nil {
			return nil, classifyGORM(ctx, scanErr)
		}
		result[record.Role] = record
	}
	if err = rows.Err(); err != nil {
		return nil, classifyGORM(ctx, err)
	}
	return result, nil
}

func gormLockActivationParticipants(ctx context.Context, database *gorm.DB, rolloutID string) (map[domain.RuntimeRole]domain.ParticipantRecord, error) {
	rows, err := gormRawRows(ctx, database, `SELECT `+participantColumns+`
FROM ops.model_settings_rollout_participant
WHERE rollout_id=?::uuid AND role IN ('api','worker') ORDER BY role FOR UPDATE`, rolloutID)
	if err != nil {
		return nil, classifyGORM(ctx, err)
	}
	defer rows.Close()
	result := make(map[domain.RuntimeRole]domain.ParticipantRecord, 2)
	for rows.Next() {
		record, scanErr := scanParticipant(rows)
		if scanErr != nil {
			return nil, classifyGORM(ctx, scanErr)
		}
		result[record.Role] = record
	}
	if err = rows.Err(); err != nil {
		return nil, classifyGORM(ctx, err)
	}
	return result, nil
}

func (repository *GORMRepository) gormEnsureActivationPreparation(
	ctx context.Context,
	scope foundation.TransactionScope,
	database *gorm.DB,
	rolloutID foundation.ID,
	targetRevision int64,
) error {
	lifecycle, err := repository.gormScopedLocalLifecycle(scope)
	if err != nil || lifecycle == nil {
		return err
	}
	if !validID(rolloutID) || targetRevision < 0 {
		return invalid(errors.New("local activation preparation binding is invalid"))
	}
	persisted, err := gormLoadRevision(ctx, database, targetRevision)
	if err != nil {
		return err
	}
	managed := domain.RequiresManagedOllama(persisted.settings)
	if !managed.Required || len(managed.Models) == 0 {
		return nil
	}
	models := make([]localmodelruntime.ModelRef, 0, len(managed.Models))
	for _, model := range managed.Models {
		models = append(models, localmodelruntime.ModelRef(model))
	}
	requirement, err := localmodelruntime.NewRequirement(models)
	if err != nil {
		return invalid(fmt.Errorf("local activation requirement is invalid: %w", err))
	}
	holdID, err := deterministicLifecycleID("preparation-hold", rolloutID, targetRevision)
	if err != nil {
		return err
	}
	idempotencyKey := fmt.Sprintf("activation:%s:%d", rolloutID, targetRevision)
	hashInput := strings.Join([]string{"model-settings-activation/v1", string(rolloutID), strconv.FormatInt(targetRevision, 10), requirement.Hash}, "\x00")
	digest := sha256.Sum256([]byte(hashInput))
	requestHash := hex.EncodeToString(digest[:])
	_, err = lifecycle.SeedActivationPreparation(ctx, localmodelruntime.ActivationPreparationCommand{
		OperationID: rolloutID, HoldID: holdID, RolloutID: rolloutID, TargetRevision: targetRevision,
		IdempotencyKey: idempotencyKey, RequestHash: requestHash, Requirement: requirement,
		OwnerID: rolloutID, OwnerEpoch: 1, LeaseDuration: localPreparationLeaseDuration,
	})
	return err
}

func (repository *GORMRepository) gormVerifyLocalPreparation(
	ctx context.Context,
	scope foundation.TransactionScope,
	database *gorm.DB,
	rolloutID string,
) error {
	lifecycle, err := repository.gormScopedLocalLifecycle(scope)
	if err != nil || lifecycle == nil || rolloutID == "" {
		return err
	}
	parsedRollout, err := foundation.ParseID(rolloutID)
	if err != nil {
		return runtimeNotPrepared(errors.New("local model activation rollout identity is invalid"))
	}
	state, err := gormLoadState(ctx, database, ``)
	if err != nil {
		return err
	}
	targetRevision := state.targetRevision.Int64
	persisted, err := gormLoadRevision(ctx, database, targetRevision)
	if err != nil {
		return err
	}
	managed := domain.RequiresManagedOllama(persisted.settings)
	if !managed.Required {
		return nil
	}
	operation, err := lifecycle.ReadOperationByRollout(ctx, parsedRollout)
	if gormNoRows(err) {
		return runtimeNotPrepared(errors.New("local model preparation operation is missing"))
	}
	if err != nil {
		return err
	}
	if operation.Phase != localmodelruntime.OperationPhaseReady && operation.Phase != localmodelruntime.OperationPhaseSucceeded {
		return runtimeNotPrepared(fmt.Errorf("local model preparation is not ready: %s", operation.Phase))
	}
	return nil
}

func (repository *GORMRepository) gormScopedLocalLifecycle(scope foundation.TransactionScope) (localmodelruntime.TxStore, error) {
	if repository == nil || nilInterface(repository.localLifecycle) {
		return nil, nil
	}
	lifecycle, err := repository.localLifecycle.WithScope(scope)
	if err != nil {
		return nil, err
	}
	if nilInterface(lifecycle) {
		return nil, unavailable(errors.New("local model lifecycle scoped store is unavailable"))
	}
	return lifecycle, nil
}
