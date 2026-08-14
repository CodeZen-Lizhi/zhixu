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
	"github.com/jackc/pgx/v5"
)

const localPreparationLeaseDuration = 30 * time.Second

// StartActivation fixes an exact desired revision or replays its live operation.
func (repository *Repository) StartActivation(ctx context.Context, command application.StartActivationCommand) (application.StartActivationResult, error) {
	if command.FreshWithin == 0 {
		command.FreshWithin = defaultRuntimeTakeoverStaleAfter
	}
	if ctx == nil || !validID(command.RolloutID) || command.TargetRevision < 0 ||
		command.ExpectedDesiredRevision != command.TargetRevision || command.ExpectedStateVersion <= 0 ||
		!validLease(command.LeaseDuration) || !validFreshWithin(command.FreshWithin) {
		return application.StartActivationResult{}, invalid(errors.New("model settings activation start is invalid"))
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return application.StartActivationResult{}, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	state, err := loadState(ctx, tx, `FOR UPDATE`)
	if err != nil {
		return application.StartActivationResult{}, err
	}
	phase := domain.RolloutPhase(state.phase)
	if domain.ActiveActivationPhase(phase) {
		if state.targetRevision.Valid && state.targetRevision.Int64 == command.TargetRevision {
			if err := repository.ensureActivationPreparation(ctx, tx, command.RolloutID, command.TargetRevision); err != nil {
				return application.StartActivationResult{}, err
			}
			existing, rolloutErr := state.rollout()
			if rolloutErr != nil {
				return application.StartActivationResult{}, rolloutErr
			}
			if err := commit(tx, ctx); err != nil {
				return application.StartActivationResult{}, err
			}
			return application.StartActivationResult{State: existing, Replayed: true}, nil
		}
		return application.StartActivationResult{}, activationConflict(errors.New("another model settings activation is live"))
	}
	if phase != domain.RolloutPhaseIdle && phase != domain.RolloutPhaseFailed {
		return application.StartActivationResult{}, activationConflict(errors.New("model settings activation state cannot start"))
	}
	if state.activeRevision == command.TargetRevision {
		if phase != domain.RolloutPhaseIdle {
			return application.StartActivationResult{}, activationConflict(errors.New("model settings active target has a terminal activation record"))
		}
		if state.desiredRevision != command.ExpectedDesiredRevision {
			return application.StartActivationResult{}, activationConflict(errors.New("model settings desired revision changed"))
		}
		ready, readyErr := servingRuntimesReady(ctx, tx, command.TargetRevision, command.FreshWithin)
		if readyErr != nil {
			return application.StartActivationResult{}, readyErr
		}
		if !ready {
			return application.StartActivationResult{}, runtimeNotReady(errors.New("model settings active target is degraded"))
		}
		existing, rolloutErr := state.rollout()
		if rolloutErr != nil {
			return application.StartActivationResult{}, rolloutErr
		}
		if err := commit(tx, ctx); err != nil {
			return application.StartActivationResult{}, err
		}
		return application.StartActivationResult{State: existing, Replayed: true}, nil
	}
	if state.version != command.ExpectedStateVersion || state.desiredRevision != command.ExpectedDesiredRevision {
		return application.StartActivationResult{}, activationConflict(errors.New("model settings desired revision or state version changed"))
	}
	updated, err := scanState(tx.QueryRow(ctx, `UPDATE ops.model_settings_state
SET rollout_id=$1::uuid,target_revision=$2,previous_active_revision=active_revision,
    phase='preparing',lease_expires_at=clock_timestamp()+($3::bigint*interval '1 microsecond'),
    last_error_code=NULL,version=version+1,updated_at=clock_timestamp()
WHERE singleton=true AND version=$4 AND desired_revision=$2 AND phase IN ('idle','failed')
RETURNING `+stateColumns,
		string(command.RolloutID), command.TargetRevision, command.LeaseDuration.Microseconds(), command.ExpectedStateVersion))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.StartActivationResult{}, activationConflict(errors.New("model settings activation start CAS failed"))
	}
	if err != nil {
		return application.StartActivationResult{}, classify(err)
	}
	result, err := updated.rollout()
	if err != nil {
		return application.StartActivationResult{}, err
	}
	if err := repository.ensureActivationPreparation(ctx, tx, command.RolloutID, command.TargetRevision); err != nil {
		return application.StartActivationResult{}, err
	}
	if err := commit(tx, ctx); err != nil {
		return application.StartActivationResult{}, err
	}
	return application.StartActivationResult{State: result}, nil
}

// RenewActivation renews one exact phase/version lease with database time.
func (repository *Repository) RenewActivation(ctx context.Context, command application.RenewActivationCommand) (domain.RolloutState, error) {
	if ctx == nil || !validID(command.RolloutID) || !domain.ActiveActivationPhase(command.ExpectedPhase) ||
		command.ExpectedVersion <= 0 || !validLease(command.LeaseDuration) {
		return domain.RolloutState{}, invalid(errors.New("model settings activation renewal is invalid"))
	}
	return repository.mutateActivationLease(ctx, command.RolloutID, command.ExpectedPhase, command.ExpectedVersion, command.LeaseDuration)
}

func (repository *Repository) mutateActivationLease(ctx context.Context, rolloutID foundation.ID, expectedPhase domain.RolloutPhase, expectedVersion int64, duration time.Duration) (domain.RolloutState, error) {
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
	if !sameActivation(state, rolloutID, expectedPhase, expectedVersion) {
		return domain.RolloutState{}, activationConflict(errors.New("model settings activation ownership changed"))
	}
	if !state.leaseExpiresAt.Valid || !state.leaseExpiresAt.Time.After(now) {
		return domain.RolloutState{}, activationLeaseExpired(errors.New("model settings activation lease expired"))
	}
	updated, err := scanState(tx.QueryRow(ctx, `UPDATE ops.model_settings_state
SET lease_expires_at=clock_timestamp()+($1::bigint*interval '1 microsecond'),
    version=version+1,updated_at=clock_timestamp()
WHERE singleton=true AND rollout_id=$2::uuid AND phase=$3 AND version=$4
RETURNING `+stateColumns, duration.Microseconds(), string(rolloutID), string(expectedPhase), expectedVersion))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RolloutState{}, activationConflict(errors.New("model settings activation renewal CAS failed"))
	}
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

// AdvanceActivation advances preparing to arming after locking and verifying both roles.
func (repository *Repository) AdvanceActivation(ctx context.Context, command application.AdvanceActivationCommand) (domain.RolloutState, error) {
	if ctx == nil || !validID(command.RolloutID) || command.ExpectedPhase != domain.RolloutPhasePreparing ||
		command.NextPhase != domain.RolloutPhaseArming || command.ExpectedVersion <= 0 ||
		!validLease(command.LeaseDuration) || !validFreshWithin(command.FreshWithin) {
		return domain.RolloutState{}, invalid(errors.New("model settings activation advance is invalid"))
	}
	return repository.advanceActivation(ctx, command.RolloutID, command.ExpectedVersion, command.FreshWithin, command.LeaseDuration)
}

func (repository *Repository) advanceActivation(ctx context.Context, rolloutID foundation.ID, expectedVersion int64, freshWithin, leaseDuration time.Duration) (domain.RolloutState, error) {
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
	if !sameActivation(state, rolloutID, domain.RolloutPhasePreparing, expectedVersion) {
		return domain.RolloutState{}, activationConflict(errors.New("model settings activation prepare CAS failed"))
	}
	if !state.leaseExpiresAt.Valid || !state.leaseExpiresAt.Time.After(now) {
		return domain.RolloutState{}, activationLeaseExpired(errors.New("model settings activation lease expired"))
	}
	if err := verifyActivationRoles(ctx, tx, state, now, freshWithin, domain.ParticipantPhasePrepared, state.previousActive.Int64); err != nil {
		return domain.RolloutState{}, err
	}
	if err := repository.verifyLocalPreparation(ctx, tx, state.rolloutID.String); err != nil {
		return domain.RolloutState{}, err
	}
	updated, err := scanState(tx.QueryRow(ctx, `UPDATE ops.model_settings_state
SET phase='arming',lease_expires_at=clock_timestamp()+($1::bigint*interval '1 microsecond'),
    version=version+1,updated_at=clock_timestamp()
WHERE singleton=true AND rollout_id=$2::uuid AND phase='preparing' AND version=$3
RETURNING `+stateColumns, leaseDuration.Microseconds(), string(rolloutID), expectedVersion))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RolloutState{}, activationConflict(errors.New("model settings activation advance CAS failed"))
	}
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

// FailActivation terminates only preparing or arming work and preserves active.
func (repository *Repository) FailActivation(ctx context.Context, command application.FailActivationCommand) (domain.RolloutState, error) {
	if ctx == nil || !validID(command.RolloutID) || command.ExpectedVersion <= 0 ||
		(command.ExpectedPhase != domain.RolloutPhasePreparing && command.ExpectedPhase != domain.RolloutPhaseArming) ||
		!canonicalErrorCode(command.ErrorCode) {
		return domain.RolloutState{}, invalid(errors.New("model settings activation failure is invalid"))
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
	if !sameActivation(state, command.RolloutID, command.ExpectedPhase, command.ExpectedVersion) {
		return domain.RolloutState{}, activationConflict(errors.New("model settings activation failure CAS failed"))
	}
	updated, err := failActivationLocked(ctx, tx, state, command.ErrorCode)
	if err != nil {
		return domain.RolloutState{}, err
	}
	if repository.localLifecycle != nil {
		if _, err := repository.localLifecycle.CompleteActivationPreparation(ctx, tx, command.RolloutID, command.ErrorCode, false); err != nil && !isNoLocalPreparation(err) {
			return domain.RolloutState{}, err
		}
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

// CommitActivation verifies both armed roles and performs the sole active pointer change.
func (repository *Repository) CommitActivation(ctx context.Context, command application.CommitActivationCommand) (domain.RolloutState, error) {
	if ctx == nil || !validID(command.RolloutID) || command.ExpectedVersion <= 0 ||
		!validFreshWithin(command.FreshWithin) || !validLease(command.LeaseDuration) {
		return domain.RolloutState{}, invalid(errors.New("model settings activation commit is invalid"))
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
	if !sameActivation(state, command.RolloutID, domain.RolloutPhaseArming, command.ExpectedVersion) {
		return domain.RolloutState{}, activationConflict(errors.New("model settings activation commit CAS failed"))
	}
	if !state.leaseExpiresAt.Valid || !state.leaseExpiresAt.Time.After(now) {
		return domain.RolloutState{}, activationLeaseExpired(errors.New("model settings activation lease expired"))
	}
	if err := verifyActivationRoles(ctx, tx, state, now, command.FreshWithin, domain.ParticipantPhaseArmed, state.previousActive.Int64); err != nil {
		return domain.RolloutState{}, err
	}
	updated, err := scanState(tx.QueryRow(ctx, `UPDATE ops.model_settings_state
SET active_revision=target_revision,phase='activating',
    lease_expires_at=clock_timestamp()+($1::bigint*interval '1 microsecond'),
    version=version+1,updated_at=clock_timestamp()
WHERE singleton=true AND rollout_id=$2::uuid AND phase='arming' AND version=$3
RETURNING `+stateColumns, command.LeaseDuration.Microseconds(), string(command.RolloutID), command.ExpectedVersion))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RolloutState{}, activationConflict(errors.New("model settings activation commit CAS failed"))
	}
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

// AcknowledgeActivation atomically updates one serving row and its armed participant.
func (repository *Repository) AcknowledgeActivation(ctx context.Context, command application.ActivationAcknowledgement) (domain.ParticipantRecord, error) {
	if ctx == nil || !validID(command.RolloutID) || !domain.ValidRuntimeRole(command.Role) || !validID(command.InstanceID) ||
		command.TargetRevision < 0 || command.ExpectedStateVersion <= 0 || command.ExpectedParticipantVersion <= 0 {
		return domain.ParticipantRecord{}, invalid(errors.New("model settings activation acknowledgement is invalid"))
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return domain.ParticipantRecord{}, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	state, err := loadState(ctx, tx, `FOR UPDATE`)
	if err != nil {
		return domain.ParticipantRecord{}, err
	}
	if !sameActivation(state, command.RolloutID, domain.RolloutPhaseActivating, command.ExpectedStateVersion) ||
		!state.targetRevision.Valid || state.targetRevision.Int64 != command.TargetRevision {
		return domain.ParticipantRecord{}, activationConflict(errors.New("model settings activation acknowledgement state changed"))
	}
	runtime, err := scanRuntime(tx.QueryRow(ctx, `SELECT `+runtimeColumns+`
FROM ops.model_settings_runtime WHERE role=$1 FOR UPDATE`, string(command.Role)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ParticipantRecord{}, runtimeOwnershipLost(errors.New("model settings serving owner is missing"))
	}
	if err != nil {
		return domain.ParticipantRecord{}, classify(err)
	}
	participant, err := scanParticipant(tx.QueryRow(ctx, `SELECT `+participantColumns+`
FROM ops.model_settings_rollout_participant WHERE rollout_id=$1::uuid AND role=$2 FOR UPDATE`,
		string(command.RolloutID), string(command.Role)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ParticipantRecord{}, participantConflict(errors.New("model settings activation participant is missing"))
	}
	if err != nil {
		return domain.ParticipantRecord{}, classify(err)
	}
	if runtime.InstanceID != command.InstanceID || runtime.Phase != domain.RuntimePhaseActive || runtime.RolloutID != nil {
		return domain.ParticipantRecord{}, runtimeOwnershipLost(errors.New("model settings serving ownership changed"))
	}
	if participant.InstanceID != command.InstanceID || participant.TargetRevision != command.TargetRevision {
		return domain.ParticipantRecord{}, participantConflict(errors.New("model settings activation participant ownership changed"))
	}
	if participant.Phase == domain.ParticipantPhaseActivated && runtime.AppliedRevision == command.TargetRevision {
		if err := commit(tx, ctx); err != nil {
			return domain.ParticipantRecord{}, err
		}
		return participant, nil
	}
	if participant.Phase != domain.ParticipantPhaseArmed || participant.Version != command.ExpectedParticipantVersion ||
		runtime.AppliedRevision != state.previousActive.Int64 {
		return domain.ParticipantRecord{}, participantConflict(errors.New("model settings activation acknowledgement CAS failed"))
	}
	runtimeUpdate, err := tx.Exec(ctx, `UPDATE ops.model_settings_runtime
SET applied_revision=$1,applied_at=clock_timestamp(),heartbeat_at=clock_timestamp()
WHERE role=$2 AND instance_id=$3::uuid AND applied_revision=$4 AND phase='active' AND rollout_id IS NULL`,
		command.TargetRevision, string(command.Role), string(command.InstanceID), state.previousActive.Int64)
	if err != nil {
		return domain.ParticipantRecord{}, classify(err)
	}
	if runtimeUpdate.RowsAffected() != 1 {
		return domain.ParticipantRecord{}, runtimeOwnershipLost(errors.New("model settings runtime activation CAS failed"))
	}
	updated, err := scanParticipant(tx.QueryRow(ctx, `UPDATE ops.model_settings_rollout_participant
SET phase='activated',heartbeat_at=clock_timestamp(),activated_at=clock_timestamp(),version=version+1
WHERE rollout_id=$1::uuid AND role=$2 AND instance_id=$3::uuid AND target_revision=$4
  AND phase='armed' AND version=$5
RETURNING `+participantColumns, string(command.RolloutID), string(command.Role), string(command.InstanceID),
		command.TargetRevision, command.ExpectedParticipantVersion))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ParticipantRecord{}, participantConflict(errors.New("model settings activation acknowledgement participant CAS failed"))
	}
	if err != nil {
		return domain.ParticipantRecord{}, classify(err)
	}
	if err := commit(tx, ctx); err != nil {
		return domain.ParticipantRecord{}, err
	}
	return updated, nil
}

// FinalizeActivation clears the binding after both current owners acknowledged target.
func (repository *Repository) FinalizeActivation(ctx context.Context, command application.FinalizeActivationCommand) (domain.RolloutState, error) {
	if ctx == nil || !validID(command.RolloutID) || command.ExpectedVersion <= 0 || !validFreshWithin(command.FreshWithin) {
		return domain.RolloutState{}, invalid(errors.New("model settings activation finalize is invalid"))
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
	if !sameActivation(state, command.RolloutID, domain.RolloutPhaseActivating, command.ExpectedVersion) {
		return domain.RolloutState{}, activationConflict(errors.New("model settings activation finalize CAS failed"))
	}
	if err := verifyActivationRoles(ctx, tx, state, now, command.FreshWithin, domain.ParticipantPhaseActivated, state.targetRevision.Int64); err != nil {
		return domain.RolloutState{}, err
	}
	updated, err := scanState(tx.QueryRow(ctx, `UPDATE ops.model_settings_state
SET rollout_id=NULL,target_revision=NULL,previous_active_revision=NULL,phase='idle',
    lease_expires_at=NULL,last_error_code=NULL,version=version+1,updated_at=clock_timestamp()
WHERE singleton=true AND rollout_id=$1::uuid AND phase='activating' AND version=$2
RETURNING `+stateColumns, string(command.RolloutID), command.ExpectedVersion))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RolloutState{}, activationConflict(errors.New("model settings activation finalize CAS failed"))
	}
	if err != nil {
		return domain.RolloutState{}, classify(err)
	}
	if repository.localLifecycle != nil {
		if _, err := repository.localLifecycle.CompleteActivationPreparation(ctx, tx, command.RolloutID, "", false); err != nil && !isNoLocalPreparation(err) {
			return domain.RolloutState{}, err
		}
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

// RecoverActivation fails expired pre-commit work and renews expired post-commit work.
func (repository *Repository) RecoverActivation(ctx context.Context, command application.RecoverActivationCommand) (domain.ActivationRecovery, error) {
	if ctx == nil || !validLease(command.LeaseDuration) {
		return domain.ActivationRecovery{}, invalid(errors.New("model settings activation recovery is invalid"))
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return domain.ActivationRecovery{}, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	state, err := loadState(ctx, tx, `FOR UPDATE`)
	if err != nil {
		return domain.ActivationRecovery{}, err
	}
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return domain.ActivationRecovery{}, err
	}
	action := domain.ActivationRecoveryNone
	phase := domain.RolloutPhase(state.phase)
	if domain.ActiveActivationPhase(phase) && state.leaseExpiresAt.Valid && !state.leaseExpiresAt.Time.After(now) {
		switch phase {
		case domain.RolloutPhasePreparing, domain.RolloutPhaseArming:
			state, err = failActivationLocked(ctx, tx, state, domain.ErrorCodeActivationLeaseExpired)
			action = domain.ActivationRecoveryFailedPreCommit
		case domain.RolloutPhaseActivating:
			state, err = scanState(tx.QueryRow(ctx, `UPDATE ops.model_settings_state
SET lease_expires_at=clock_timestamp()+($1::bigint*interval '1 microsecond'),
    version=version+1,updated_at=clock_timestamp()
WHERE singleton=true AND phase='activating' AND version=$2
RETURNING `+stateColumns, command.LeaseDuration.Microseconds(), state.version))
			action = domain.ActivationRecoveryAdoptedPostCommit
		}
		if err != nil {
			return domain.ActivationRecovery{}, classify(err)
		}
		if action == domain.ActivationRecoveryFailedPreCommit && repository.localLifecycle != nil && state.rolloutID.Valid {
			if _, prepErr := repository.localLifecycle.CompleteActivationPreparation(ctx, tx, foundation.ID(state.rolloutID.String), domain.ErrorCodeActivationLeaseExpired, true); prepErr != nil && !isNoLocalPreparation(prepErr) {
				return domain.ActivationRecovery{}, prepErr
			}
		}
	}
	result, err := state.rollout()
	if err != nil {
		return domain.ActivationRecovery{}, err
	}
	if err := commit(tx, ctx); err != nil {
		return domain.ActivationRecovery{}, err
	}
	return domain.ActivationRecovery{State: result, Action: action}, nil
}

func failActivationLocked(ctx context.Context, tx pgx.Tx, state stateRecord, errorCode string) (stateRecord, error) {
	if _, err := lockActivationRuntimes(ctx, tx); err != nil {
		return stateRecord{}, err
	}
	for _, role := range []domain.RuntimeRole{domain.RuntimeRoleAPI, domain.RuntimeRoleWorker} {
		if _, err := tx.Exec(ctx, `UPDATE ops.model_settings_rollout_participant
SET phase='aborted',heartbeat_at=clock_timestamp(),last_error_code=NULL,last_error_retryable=false,
    version=version+1
WHERE rollout_id=$1::uuid AND role=$2 AND phase IN ('preparing','prepared','armed')`,
			state.rolloutID.String, string(role)); err != nil {
			return stateRecord{}, classify(err)
		}
	}
	updated, err := scanState(tx.QueryRow(ctx, `UPDATE ops.model_settings_state
SET phase='failed',lease_expires_at=NULL,last_error_code=$1,version=version+1,updated_at=clock_timestamp()
WHERE singleton=true AND rollout_id=$2::uuid AND phase IN ('preparing','arming') AND version=$3
RETURNING `+stateColumns, errorCode, state.rolloutID.String, state.version))
	if errors.Is(err, pgx.ErrNoRows) {
		return stateRecord{}, activationConflict(errors.New("model settings activation failure CAS failed"))
	}
	if err != nil {
		return stateRecord{}, classify(err)
	}
	return updated, nil
}

func servingRuntimesReady(ctx context.Context, tx pgx.Tx, revision int64, freshWithin time.Duration) (bool, error) {
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return false, err
	}
	runtimes, err := lockActivationRuntimes(ctx, tx)
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

func verifyActivationRoles(ctx context.Context, tx pgx.Tx, state stateRecord, now time.Time, freshWithin time.Duration, phase domain.ParticipantPhase, appliedRevision int64) error {
	runtimes, err := lockActivationRuntimes(ctx, tx)
	if err != nil {
		return err
	}
	participants, err := lockActivationParticipants(ctx, tx, state.rolloutID.String)
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

func lockActivationRuntimes(ctx context.Context, tx pgx.Tx) (map[domain.RuntimeRole]domain.RuntimeRecord, error) {
	rows, err := tx.Query(ctx, `SELECT `+runtimeColumns+`
FROM ops.model_settings_runtime WHERE role IN ('api','worker') ORDER BY role FOR UPDATE`)
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()
	result := make(map[domain.RuntimeRole]domain.RuntimeRecord, 2)
	for rows.Next() {
		record, scanErr := scanRuntime(rows)
		if scanErr != nil {
			return nil, classify(scanErr)
		}
		result[record.Role] = record
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err)
	}
	return result, nil
}

func lockActivationParticipants(ctx context.Context, tx pgx.Tx, rolloutID string) (map[domain.RuntimeRole]domain.ParticipantRecord, error) {
	rows, err := tx.Query(ctx, `SELECT `+participantColumns+`
FROM ops.model_settings_rollout_participant
WHERE rollout_id=$1::uuid AND role IN ('api','worker') ORDER BY role FOR UPDATE`, rolloutID)
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()
	result := make(map[domain.RuntimeRole]domain.ParticipantRecord, 2)
	for rows.Next() {
		record, scanErr := scanParticipant(rows)
		if scanErr != nil {
			return nil, classify(scanErr)
		}
		result[record.Role] = record
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err)
	}
	return result, nil
}

func sameActivation(state stateRecord, id foundation.ID, phase domain.RolloutPhase, version int64) bool {
	return state.rolloutID.Valid && state.rolloutID.String == string(id) && state.phase == string(phase) && state.version == version
}

func validFreshWithin(duration time.Duration) bool {
	return duration >= time.Second && duration <= 5*time.Minute && duration%time.Microsecond == 0
}

// ensureActivationPreparation projects the target's non-secret local model
// requirement into the lifecycle tables while the caller still owns the
// model-settings transaction. This is deliberately a no-op for online and
// disabled targets, so they never create a resident Ollama demand.
func (repository *Repository) ensureActivationPreparation(ctx context.Context, tx pgx.Tx, rolloutID foundation.ID, targetRevision int64) error {
	if repository == nil || repository.localLifecycle == nil {
		return nil
	}
	if !validID(rolloutID) || targetRevision < 0 {
		return invalid(errors.New("local activation preparation binding is invalid"))
	}
	persisted, err := loadRevision(ctx, tx, targetRevision)
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
	// Derive both identities from the rollout so idempotent HTTP retries use
	// the exact same operation/hold pair instead of generating a new pair.
	operationID := rolloutID
	holdID, err := deterministicLifecycleID("preparation-hold", rolloutID, targetRevision)
	if err != nil {
		return err
	}
	idempotencyKey := fmt.Sprintf("activation:%s:%d", rolloutID, targetRevision)
	hashInput := strings.Join([]string{"model-settings-activation/v1", string(rolloutID), strconv.FormatInt(targetRevision, 10), requirement.Hash}, "\x00")
	digest := sha256.Sum256([]byte(hashInput))
	requestHash := hex.EncodeToString(digest[:])
	_, err = repository.localLifecycle.SeedActivationPreparation(ctx, tx, localmodelruntime.ActivationPreparationCommand{
		OperationID: operationID, HoldID: holdID, RolloutID: rolloutID, TargetRevision: targetRevision,
		IdempotencyKey: idempotencyKey, RequestHash: requestHash, Requirement: requirement,
		OwnerID: rolloutID, OwnerEpoch: 1, LeaseDuration: localPreparationLeaseDuration,
	})
	return err
}

func (repository *Repository) verifyLocalPreparation(ctx context.Context, tx pgx.Tx, rolloutID string) error {
	if repository == nil || repository.localLifecycle == nil || rolloutID == "" {
		return nil
	}
	parsedRollout, err := foundation.ParseID(rolloutID)
	if err != nil {
		return runtimeNotPrepared(errors.New("local model activation rollout identity is invalid"))
	}
	state, err := loadState(ctx, tx, ``)
	if err != nil {
		return err
	}
	targetRevision := state.targetRevision.Int64
	persisted, err := loadRevision(ctx, tx, targetRevision)
	if err != nil {
		return err
	}
	managed := domain.RequiresManagedOllama(persisted.settings)
	if !managed.Required {
		return nil
	}
	operation, err := repository.localLifecycle.ReadOperationByRollout(ctx, tx, parsedRollout)
	if errors.Is(err, pgx.ErrNoRows) {
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

func isNoLocalPreparation(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

func deterministicLifecycleID(prefix string, rolloutID foundation.ID, revision int64) (foundation.ID, error) {
	digest := sha256.Sum256([]byte(prefix + "\x00" + string(rolloutID) + "\x00" + strconv.FormatInt(revision, 10)))
	var raw [16]byte
	copy(raw[:], digest[:16])
	raw[6] = (raw[6] & 0x0f) | 0x50 // UUID v5-shaped deterministic identifier.
	raw[8] = (raw[8] & 0x3f) | 0x80
	value := fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
	return foundation.ParseID(value)
}
