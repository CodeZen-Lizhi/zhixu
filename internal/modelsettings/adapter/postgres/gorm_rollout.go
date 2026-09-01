package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	"gorm.io/gorm"
)

// BeginRollout fixes the current desired revision as one leased rollout target.
func (repository *GORMRepository) BeginRollout(ctx context.Context, command application.BeginRolloutCommand) (result domain.RolloutState, err error) {
	if err = repository.ready(ctx); err != nil {
		return domain.RolloutState{}, err
	}
	if !validID(command.RolloutID) || !validLease(command.LeaseDuration) {
		return domain.RolloutState{}, invalid(errors.New("model settings begin rollout command is invalid"))
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
		if activeRolloutPhase(domain.RolloutPhase(state.phase)) {
			if state.leaseExpiresAt.Valid && state.leaseExpiresAt.Time.After(now) {
				return rolloutInProgress(errors.New("model settings rollout lease is active"))
			}
			state, stateErr = gormFailLockedRollout(callbackCtx, database, state, domain.ErrorCodeRolloutLeaseExpired)
			if stateErr != nil {
				return stateErr
			}
		}
		if state.phase != string(domain.RolloutPhaseIdle) && state.phase != string(domain.RolloutPhaseFailed) {
			return rolloutConflict(errors.New("model settings rollout state cannot begin"))
		}
		row, rowErr := gormRawRow(callbackCtx, database, `UPDATE ops.model_settings_state
SET rollout_id=?::uuid,target_revision=desired_revision,previous_active_revision=active_revision,
phase='validating',lease_expires_at=clock_timestamp()+(?::bigint*interval '1 microsecond'),
last_error_code=NULL,version=version+1,updated_at=clock_timestamp()
WHERE singleton=true
RETURNING `+stateColumns, string(command.RolloutID), command.LeaseDuration.Microseconds())
		if rowErr != nil {
			return classifyGORM(callbackCtx, rowErr)
		}
		updated, scanErr := gormRolloutScanState(callbackCtx, row)
		if scanErr != nil {
			return scanErr
		}
		result, scanErr = updated.rollout()
		return scanErr
	})
	if err != nil {
		return domain.RolloutState{}, err
	}
	return result, nil
}

// RenewRollout extends the current phase lease without changing its fixed target.
func (repository *GORMRepository) RenewRollout(ctx context.Context, command application.RenewRolloutCommand) (domain.RolloutState, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.RolloutState{}, err
	}
	if !validID(command.RolloutID) || !activeRolloutPhase(command.ExpectedPhase) || !validLease(command.LeaseDuration) {
		return domain.RolloutState{}, invalid(errors.New("model settings renew rollout command is invalid"))
	}
	return repository.gormMutateRollout(ctx, command.RolloutID, command.ExpectedPhase, command.ExpectedPhase, command.LeaseDuration)
}

// AdvanceRollout advances one legal phase and renews its lease atomically.
func (repository *GORMRepository) AdvanceRollout(ctx context.Context, command application.AdvanceRolloutCommand) (domain.RolloutState, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.RolloutState{}, err
	}
	if !validID(command.RolloutID) || !validLease(command.LeaseDuration) {
		return domain.RolloutState{}, invalid(errors.New("model settings advance rollout command is invalid"))
	}
	if err := domain.ValidateRolloutTransition(command.ExpectedPhase, command.NextPhase); err != nil {
		return domain.RolloutState{}, err
	}
	return repository.gormMutateRollout(ctx, command.RolloutID, command.ExpectedPhase, command.NextPhase, command.LeaseDuration)
}

func (repository *GORMRepository) gormMutateRollout(
	ctx context.Context,
	rolloutID foundation.ID,
	expected domain.RolloutPhase,
	next domain.RolloutPhase,
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
		if !sameRollout(state, rolloutID, expected) {
			return rolloutConflict(errors.New("model settings rollout ownership changed"))
		}
		if !state.leaseExpiresAt.Valid || !state.leaseExpiresAt.Time.After(now) {
			return leaseExpired(errors.New("model settings rollout lease expired"))
		}
		row, rowErr := gormRawRow(callbackCtx, database, `UPDATE ops.model_settings_state
SET phase=?,lease_expires_at=clock_timestamp()+(?::bigint*interval '1 microsecond'),
version=version+1,updated_at=clock_timestamp()
WHERE singleton=true
RETURNING `+stateColumns, string(next), duration.Microseconds())
		if rowErr != nil {
			return classifyGORM(callbackCtx, rowErr)
		}
		updated, scanErr := gormRolloutScanState(callbackCtx, row)
		if scanErr != nil {
			return scanErr
		}
		result, scanErr = updated.rollout()
		return scanErr
	})
	if err != nil {
		return domain.RolloutState{}, err
	}
	return result, nil
}

// FailRollout preserves active, restores old runtime ownership where possible, and records a stable code.
func (repository *GORMRepository) FailRollout(ctx context.Context, command application.FailRolloutCommand) (result domain.RolloutState, err error) {
	if err = repository.ready(ctx); err != nil {
		return domain.RolloutState{}, err
	}
	if !validID(command.RolloutID) || !canonicalErrorCode(command.ErrorCode) {
		return domain.RolloutState{}, invalid(errors.New("model settings fail rollout command is invalid"))
	}
	err = repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, _ foundation.TransactionScope, database *gorm.DB) error {
		state, stateErr := gormLoadState(callbackCtx, database, `FOR UPDATE`)
		if stateErr != nil {
			return stateErr
		}
		if !state.rolloutID.Valid || state.rolloutID.String != string(command.RolloutID) || !activeRolloutPhase(domain.RolloutPhase(state.phase)) {
			return rolloutConflict(errors.New("model settings rollout ownership changed"))
		}
		state, stateErr = gormFailLockedRollout(callbackCtx, database, state, command.ErrorCode)
		if stateErr != nil {
			return stateErr
		}
		result, stateErr = state.rollout()
		return stateErr
	})
	if err != nil {
		return domain.RolloutState{}, err
	}
	return result, nil
}

// CommitRollout publishes target only after both role owners are fresh and prepared.
func (repository *GORMRepository) CommitRollout(ctx context.Context, command application.CommitRolloutCommand) (result domain.RolloutState, err error) {
	if err = repository.ready(ctx); err != nil {
		return domain.RolloutState{}, err
	}
	if !validID(command.RolloutID) || !application.ValidRuntimeFreshWithin(command.FreshWithin) {
		return domain.RolloutState{}, invalid(errors.New("model settings commit rollout command is invalid"))
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
		if !sameRollout(state, command.RolloutID, domain.RolloutPhaseVerifying) {
			return rolloutConflict(errors.New("model settings rollout is not verifying"))
		}
		if !state.leaseExpiresAt.Valid || !state.leaseExpiresAt.Time.After(now) {
			return leaseExpired(errors.New("model settings rollout lease expired"))
		}
		if verifyErr := gormVerifyPreparedRuntimes(callbackCtx, database, state, now, command.FreshWithin); verifyErr != nil {
			return verifyErr
		}
		row, rowErr := gormRawRow(callbackCtx, database, `UPDATE ops.model_settings_state
SET active_revision=target_revision,rollout_id=NULL,target_revision=NULL,previous_active_revision=NULL,
phase='idle',lease_expires_at=NULL,last_error_code=NULL,version=version+1,updated_at=clock_timestamp()
WHERE singleton=true
RETURNING `+stateColumns)
		if rowErr != nil {
			return classifyGORM(callbackCtx, rowErr)
		}
		updated, scanErr := gormRolloutScanState(callbackCtx, row)
		if scanErr != nil {
			return scanErr
		}
		update := database.WithContext(callbackCtx).Exec(`UPDATE ops.model_settings_runtime
SET rollout_id=NULL,phase='active',heartbeat_at=clock_timestamp()
WHERE rollout_id=?::uuid AND applied_revision=?`, string(command.RolloutID), state.targetRevision.Int64)
		if update.Error != nil {
			return classifyGORM(callbackCtx, update.Error)
		}
		result, scanErr = updated.rollout()
		return scanErr
	})
	if err != nil {
		return domain.RolloutState{}, err
	}
	return result, nil
}

// RecoverExpiredRollout fails one expired lease and makes a later begin safe.
func (repository *GORMRepository) RecoverExpiredRollout(ctx context.Context) (result domain.RolloutState, recovered bool, err error) {
	if err = repository.ready(ctx); err != nil {
		return domain.RolloutState{}, false, err
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
		recovered = activeRolloutPhase(domain.RolloutPhase(state.phase)) && state.leaseExpiresAt.Valid && !state.leaseExpiresAt.Time.After(now)
		if recovered {
			state, stateErr = gormFailLockedRollout(callbackCtx, database, state, domain.ErrorCodeRolloutLeaseExpired)
			if stateErr != nil {
				return stateErr
			}
		}
		result, stateErr = state.rollout()
		return stateErr
	})
	if err != nil {
		return domain.RolloutState{}, false, err
	}
	return result, recovered, nil
}

func gormFailLockedRollout(ctx context.Context, database *gorm.DB, state stateRecord, errorCode string) (stateRecord, error) {
	row, err := gormRawRow(ctx, database, `UPDATE ops.model_settings_state
SET phase='failed',lease_expires_at=NULL,last_error_code=?,version=version+1,updated_at=clock_timestamp()
WHERE singleton=true
RETURNING `+stateColumns, errorCode)
	if err != nil {
		return stateRecord{}, classifyGORM(ctx, err)
	}
	updated, err := gormRolloutScanState(ctx, row)
	if err != nil {
		return stateRecord{}, err
	}
	result := database.WithContext(ctx).Exec(`UPDATE ops.model_settings_runtime
SET rollout_id=NULL,
phase=CASE WHEN applied_revision=? AND phase IN ('quiescing','quiesced') THEN 'active' ELSE 'unavailable' END
WHERE rollout_id=?::uuid`, state.previousActive.Int64, state.rolloutID.String)
	if result.Error != nil {
		return stateRecord{}, classifyGORM(ctx, result.Error)
	}
	return updated, nil
}

func gormVerifyPreparedRuntimes(ctx context.Context, database *gorm.DB, state stateRecord, now time.Time, freshWithin time.Duration) error {
	rows, err := gormRawRows(ctx, database, `SELECT `+runtimeColumns+`
FROM ops.model_settings_runtime WHERE role IN ('api','worker') ORDER BY role FOR UPDATE`)
	if err != nil {
		return classifyGORM(ctx, err)
	}
	defer rows.Close()
	seen := map[domain.RuntimeRole]bool{}
	for rows.Next() {
		runtime, scanErr := scanRuntime(rows)
		if scanErr != nil {
			return classifyGORM(ctx, scanErr)
		}
		if runtime.RolloutID == nil || string(*runtime.RolloutID) != state.rolloutID.String || runtime.AppliedRevision != state.targetRevision.Int64 ||
			(runtime.Phase != domain.RuntimePhasePrepared && runtime.Phase != domain.RuntimePhaseVerifying) || runtime.HeartbeatAt.Before(now.Add(-freshWithin)) {
			return runtimeNotPrepared(errors.New("model settings runtime is not fresh and prepared"))
		}
		seen[runtime.Role] = true
	}
	if err := rows.Err(); err != nil {
		return classifyGORM(ctx, err)
	}
	if !seen[domain.RuntimeRoleAPI] || !seen[domain.RuntimeRoleWorker] || len(seen) != 2 {
		return runtimeNotPrepared(errors.New("both model settings runtime roles are required"))
	}
	return nil
}

func gormRolloutScanState(ctx context.Context, row interface{ Scan(...any) error }) (stateRecord, error) {
	var state stateRecord
	err := row.Scan(
		&state.desiredRevision, &state.activeRevision, &state.rolloutID, &state.targetRevision,
		&state.previousActive, &state.phase, &state.leaseExpiresAt, &state.lastErrorCode, &state.version,
	)
	if gormNoRows(err) {
		return stateRecord{}, corrupt(errors.New("model settings singleton state is missing"))
	}
	if err != nil {
		return stateRecord{}, classifyGORM(ctx, err)
	}
	if state.desiredRevision < 0 || state.activeRevision < 0 || state.version <= 0 {
		return stateRecord{}, corrupt(errors.New("model settings singleton state is invalid"))
	}
	return state, nil
}
