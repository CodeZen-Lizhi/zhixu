package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	"github.com/jackc/pgx/v5"
)

// BeginRollout fixes the current desired revision as one leased rollout target.
func (repository *Repository) BeginRollout(ctx context.Context, command application.BeginRolloutCommand) (domain.RolloutState, error) {
	if ctx == nil || !validID(command.RolloutID) || !validLease(command.LeaseDuration) {
		return domain.RolloutState{}, invalid(errors.New("model settings begin rollout command is invalid"))
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return domain.RolloutState{}, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	state, err := loadState(ctx, tx, `FOR UPDATE`)
	if err != nil {
		return domain.RolloutState{}, err
	}
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return domain.RolloutState{}, err
	}
	if activeRolloutPhase(domain.RolloutPhase(state.phase)) {
		if state.leaseExpiresAt.Valid && state.leaseExpiresAt.Time.After(now) {
			return domain.RolloutState{}, rolloutInProgress(errors.New("model settings rollout lease is active"))
		}
		state, err = failLockedRollout(ctx, tx, state, domain.ErrorCodeRolloutLeaseExpired)
		if err != nil {
			return domain.RolloutState{}, err
		}
	}
	if state.phase != string(domain.RolloutPhaseIdle) && state.phase != string(domain.RolloutPhaseFailed) {
		return domain.RolloutState{}, rolloutConflict(errors.New("model settings rollout state cannot begin"))
	}
	updated, err := scanState(tx.QueryRow(ctx, `UPDATE ops.model_settings_state
SET rollout_id=$1::uuid,target_revision=desired_revision,previous_active_revision=active_revision,
phase='validating',lease_expires_at=clock_timestamp()+($2::bigint*interval '1 microsecond'),
last_error_code=NULL,version=version+1,updated_at=clock_timestamp()
WHERE singleton=true
RETURNING `+stateColumns, string(command.RolloutID), command.LeaseDuration.Microseconds()))
	if err != nil {
		return domain.RolloutState{}, classify(err)
	}
	result, err := updated.rollout()
	if err != nil {
		return domain.RolloutState{}, err
	}
	if err := commit(tx, ctx); err != nil {
		return domain.RolloutState{}, err
	}
	return result, nil
}

// RenewRollout extends the current phase lease without changing its fixed target.
func (repository *Repository) RenewRollout(ctx context.Context, command application.RenewRolloutCommand) (domain.RolloutState, error) {
	if ctx == nil || !validID(command.RolloutID) || !activeRolloutPhase(command.ExpectedPhase) || !validLease(command.LeaseDuration) {
		return domain.RolloutState{}, invalid(errors.New("model settings renew rollout command is invalid"))
	}
	return repository.mutateRollout(ctx, command.RolloutID, command.ExpectedPhase, command.ExpectedPhase, command.LeaseDuration)
}

// AdvanceRollout advances one legal phase and renews its lease atomically.
func (repository *Repository) AdvanceRollout(ctx context.Context, command application.AdvanceRolloutCommand) (domain.RolloutState, error) {
	if ctx == nil || !validID(command.RolloutID) || !validLease(command.LeaseDuration) {
		return domain.RolloutState{}, invalid(errors.New("model settings advance rollout command is invalid"))
	}
	if err := domain.ValidateRolloutTransition(command.ExpectedPhase, command.NextPhase); err != nil {
		return domain.RolloutState{}, err
	}
	return repository.mutateRollout(ctx, command.RolloutID, command.ExpectedPhase, command.NextPhase, command.LeaseDuration)
}

func (repository *Repository) mutateRollout(ctx context.Context, rolloutID foundation.ID, expected, next domain.RolloutPhase, duration time.Duration) (domain.RolloutState, error) {
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return domain.RolloutState{}, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	state, err := loadState(ctx, tx, `FOR UPDATE`)
	if err != nil {
		return domain.RolloutState{}, err
	}
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return domain.RolloutState{}, err
	}
	if !sameRollout(state, rolloutID, expected) {
		return domain.RolloutState{}, rolloutConflict(errors.New("model settings rollout ownership changed"))
	}
	if !state.leaseExpiresAt.Valid || !state.leaseExpiresAt.Time.After(now) {
		return domain.RolloutState{}, leaseExpired(errors.New("model settings rollout lease expired"))
	}
	updated, err := scanState(tx.QueryRow(ctx, `UPDATE ops.model_settings_state
SET phase=$1,lease_expires_at=clock_timestamp()+($2::bigint*interval '1 microsecond'),
version=version+1,updated_at=clock_timestamp()
WHERE singleton=true
RETURNING `+stateColumns, string(next), duration.Microseconds()))
	if err != nil {
		return domain.RolloutState{}, classify(err)
	}
	result, err := updated.rollout()
	if err != nil {
		return domain.RolloutState{}, err
	}
	if err := commit(tx, ctx); err != nil {
		return domain.RolloutState{}, err
	}
	return result, nil
}

// FailRollout preserves active, restores old runtime ownership where possible, and records a stable code.
func (repository *Repository) FailRollout(ctx context.Context, command application.FailRolloutCommand) (domain.RolloutState, error) {
	if ctx == nil || !validID(command.RolloutID) || !canonicalErrorCode(command.ErrorCode) {
		return domain.RolloutState{}, invalid(errors.New("model settings fail rollout command is invalid"))
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return domain.RolloutState{}, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	state, err := loadState(ctx, tx, `FOR UPDATE`)
	if err != nil {
		return domain.RolloutState{}, err
	}
	if !state.rolloutID.Valid || state.rolloutID.String != string(command.RolloutID) || !activeRolloutPhase(domain.RolloutPhase(state.phase)) {
		return domain.RolloutState{}, rolloutConflict(errors.New("model settings rollout ownership changed"))
	}
	state, err = failLockedRollout(ctx, tx, state, command.ErrorCode)
	if err != nil {
		return domain.RolloutState{}, err
	}
	result, err := state.rollout()
	if err != nil {
		return domain.RolloutState{}, err
	}
	if err := commit(tx, ctx); err != nil {
		return domain.RolloutState{}, err
	}
	return result, nil
}

// CommitRollout publishes target only after both role owners are fresh and prepared.
func (repository *Repository) CommitRollout(ctx context.Context, command application.CommitRolloutCommand) (domain.RolloutState, error) {
	if ctx == nil || !validID(command.RolloutID) || !application.ValidRuntimeFreshWithin(command.FreshWithin) {
		return domain.RolloutState{}, invalid(errors.New("model settings commit rollout command is invalid"))
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return domain.RolloutState{}, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	state, err := loadState(ctx, tx, `FOR UPDATE`)
	if err != nil {
		return domain.RolloutState{}, err
	}
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return domain.RolloutState{}, err
	}
	if !sameRollout(state, command.RolloutID, domain.RolloutPhaseVerifying) {
		return domain.RolloutState{}, rolloutConflict(errors.New("model settings rollout is not verifying"))
	}
	if !state.leaseExpiresAt.Valid || !state.leaseExpiresAt.Time.After(now) {
		return domain.RolloutState{}, leaseExpired(errors.New("model settings rollout lease expired"))
	}
	if err := verifyPreparedRuntimes(ctx, tx, state, now, command.FreshWithin); err != nil {
		return domain.RolloutState{}, err
	}
	updated, err := scanState(tx.QueryRow(ctx, `UPDATE ops.model_settings_state
SET active_revision=target_revision,rollout_id=NULL,target_revision=NULL,previous_active_revision=NULL,
phase='idle',lease_expires_at=NULL,last_error_code=NULL,version=version+1,updated_at=clock_timestamp()
WHERE singleton=true
RETURNING `+stateColumns))
	if err != nil {
		return domain.RolloutState{}, classify(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE ops.model_settings_runtime
SET rollout_id=NULL,phase='active',heartbeat_at=clock_timestamp()
WHERE rollout_id=$1::uuid AND applied_revision=$2`, string(command.RolloutID), state.targetRevision.Int64); err != nil {
		return domain.RolloutState{}, classify(err)
	}
	result, err := updated.rollout()
	if err != nil {
		return domain.RolloutState{}, err
	}
	if err := commit(tx, ctx); err != nil {
		return domain.RolloutState{}, err
	}
	return result, nil
}

// RecoverExpiredRollout fails one expired lease and makes a later begin safe.
func (repository *Repository) RecoverExpiredRollout(ctx context.Context) (domain.RolloutState, bool, error) {
	if ctx == nil {
		return domain.RolloutState{}, false, invalid(errors.New("model settings recovery context is nil"))
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return domain.RolloutState{}, false, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	state, err := loadState(ctx, tx, `FOR UPDATE`)
	if err != nil {
		return domain.RolloutState{}, false, err
	}
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return domain.RolloutState{}, false, err
	}
	recovered := activeRolloutPhase(domain.RolloutPhase(state.phase)) && state.leaseExpiresAt.Valid && !state.leaseExpiresAt.Time.After(now)
	if recovered {
		state, err = failLockedRollout(ctx, tx, state, domain.ErrorCodeRolloutLeaseExpired)
		if err != nil {
			return domain.RolloutState{}, false, err
		}
	}
	result, err := state.rollout()
	if err != nil {
		return domain.RolloutState{}, false, err
	}
	if err := commit(tx, ctx); err != nil {
		return domain.RolloutState{}, false, err
	}
	return result, recovered, nil
}

func failLockedRollout(ctx context.Context, tx pgx.Tx, state stateRecord, errorCode string) (stateRecord, error) {
	updated, err := scanState(tx.QueryRow(ctx, `UPDATE ops.model_settings_state
SET phase='failed',lease_expires_at=NULL,last_error_code=$1,version=version+1,updated_at=clock_timestamp()
WHERE singleton=true
RETURNING `+stateColumns, errorCode))
	if err != nil {
		return stateRecord{}, classify(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE ops.model_settings_runtime
SET rollout_id=NULL,
phase=CASE WHEN applied_revision=$2 AND phase IN ('quiescing','quiesced') THEN 'active' ELSE 'unavailable' END
WHERE rollout_id=$1::uuid`, state.rolloutID.String, state.previousActive.Int64); err != nil {
		return stateRecord{}, classify(err)
	}
	return updated, nil
}

func verifyPreparedRuntimes(ctx context.Context, tx pgx.Tx, state stateRecord, now time.Time, freshWithin time.Duration) error {
	rows, err := tx.Query(ctx, `SELECT `+runtimeColumns+`
FROM ops.model_settings_runtime WHERE role IN ('api','worker') ORDER BY role FOR UPDATE`)
	if err != nil {
		return classify(err)
	}
	defer rows.Close()
	seen := map[domain.RuntimeRole]bool{}
	for rows.Next() {
		runtime, scanErr := scanRuntime(rows)
		if scanErr != nil {
			return classify(scanErr)
		}
		if runtime.RolloutID == nil || string(*runtime.RolloutID) != state.rolloutID.String || runtime.AppliedRevision != state.targetRevision.Int64 ||
			(runtime.Phase != domain.RuntimePhasePrepared && runtime.Phase != domain.RuntimePhaseVerifying) || runtime.HeartbeatAt.Before(now.Add(-freshWithin)) {
			return runtimeNotPrepared(errors.New("model settings runtime is not fresh and prepared"))
		}
		seen[runtime.Role] = true
	}
	if err := rows.Err(); err != nil {
		return classify(err)
	}
	if !seen[domain.RuntimeRoleAPI] || !seen[domain.RuntimeRoleWorker] || len(seen) != 2 {
		return runtimeNotPrepared(errors.New("both model settings runtime roles are required"))
	}
	return nil
}

func sameRollout(state stateRecord, id foundation.ID, phase domain.RolloutPhase) bool {
	return state.rolloutID.Valid && state.rolloutID.String == string(id) && state.phase == string(phase)
}

func activeRolloutPhase(phase domain.RolloutPhase) bool {
	switch phase {
	case domain.RolloutPhaseValidating, domain.RolloutPhaseDraining, domain.RolloutPhaseApplying, domain.RolloutPhaseVerifying:
		return true
	default:
		return false
	}
}

func validID(id foundation.ID) bool {
	parsed, err := foundation.ParseID(string(id))
	return err == nil && parsed == id
}

func validLease(duration time.Duration) bool {
	return duration >= time.Second && duration <= 10*time.Minute && duration%time.Microsecond == 0
}

func canonicalErrorCode(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character != '_' && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}
