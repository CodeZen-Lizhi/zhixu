package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	"github.com/jackc/pgx/v5"
)

// RegisterParticipant claims a role candidate or replaces only a stale binding.
func (repository *Repository) RegisterParticipant(ctx context.Context, registration application.ParticipantRegistration) (domain.ParticipantRecord, error) {
	if registration.StaleAfter == 0 {
		registration.StaleAfter = defaultRuntimeTakeoverStaleAfter
	}
	if ctx == nil || !validParticipantIdentity(registration.RolloutID, registration.Role, registration.InstanceID, registration.TargetRevision) ||
		(registration.InitialPhase != domain.ParticipantPhasePreparing && registration.InitialPhase != domain.ParticipantPhaseActivated) ||
		!validFreshWithin(registration.StaleAfter) {
		return domain.ParticipantRecord{}, invalid(errors.New("model settings participant registration is invalid"))
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
	if err := authorizeParticipantRegistration(state, registration); err != nil {
		return domain.ParticipantRecord{}, err
	}
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return domain.ParticipantRecord{}, err
	}
	runtime, err := scanRuntime(tx.QueryRow(ctx, `SELECT `+runtimeColumns+`
FROM ops.model_settings_runtime WHERE role=$1 FOR UPDATE`, string(registration.Role)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ParticipantRecord{}, runtimeOwnershipLost(errors.New("model settings serving owner is missing"))
	}
	if err != nil {
		return domain.ParticipantRecord{}, classify(err)
	}
	if runtime.InstanceID != registration.InstanceID || runtime.Phase != domain.RuntimePhaseActive || runtime.RolloutID != nil {
		return domain.ParticipantRecord{}, runtimeOwnershipLost(errors.New("model settings serving ownership changed"))
	}
	if registration.InitialPhase == domain.ParticipantPhasePreparing && runtime.AppliedRevision != state.previousActive.Int64 {
		return domain.ParticipantRecord{}, runtimeNotReady(errors.New("model settings serving runtime is not on previous active revision"))
	}
	if registration.InitialPhase == domain.ParticipantPhaseActivated && runtime.AppliedRevision != state.targetRevision.Int64 {
		return domain.ParticipantRecord{}, runtimeNotReady(errors.New("model settings serving runtime has not loaded active target"))
	}
	existing, err := scanParticipant(tx.QueryRow(ctx, `SELECT `+participantColumns+`
FROM ops.model_settings_rollout_participant WHERE rollout_id=$1::uuid AND role=$2 FOR UPDATE`,
		string(registration.RolloutID), string(registration.Role)))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		preparedExpression := `NULL`
		activatedExpression := `NULL`
		if registration.InitialPhase == domain.ParticipantPhaseActivated {
			preparedExpression = `clock_timestamp()`
			activatedExpression = `clock_timestamp()`
		}
		existing, err = scanParticipant(tx.QueryRow(ctx, `INSERT INTO ops.model_settings_rollout_participant(
rollout_id,role,instance_id,target_revision,phase,heartbeat_at,last_error_code,last_error_retryable,
version,prepared_at,activated_at,retired_at)
VALUES($1::uuid,$2,$3::uuid,$4,$5,clock_timestamp(),NULL,false,1,`+preparedExpression+`,`+activatedExpression+`,NULL)
RETURNING `+participantColumns,
			string(registration.RolloutID), string(registration.Role), string(registration.InstanceID),
			registration.TargetRevision, string(registration.InitialPhase)))
	case err != nil:
		return domain.ParticipantRecord{}, classify(err)
	case existing.InstanceID == registration.InstanceID && existing.TargetRevision == registration.TargetRevision:
		existing, err = scanParticipant(tx.QueryRow(ctx, `UPDATE ops.model_settings_rollout_participant
SET heartbeat_at=clock_timestamp(),version=version+1
WHERE rollout_id=$1::uuid AND role=$2 AND instance_id=$3::uuid AND version=$4
RETURNING `+participantColumns,
			string(registration.RolloutID), string(registration.Role), string(registration.InstanceID), existing.Version))
	case existing.TargetRevision != registration.TargetRevision || !existing.HeartbeatAt.Before(now.Add(-registration.StaleAfter)):
		return domain.ParticipantRecord{}, participantConflict(errors.New("model settings participant owner is fresh or target changed"))
	default:
		preparedExpression := `NULL`
		activatedExpression := `NULL`
		if registration.InitialPhase == domain.ParticipantPhaseActivated {
			preparedExpression = `clock_timestamp()`
			activatedExpression = `clock_timestamp()`
		}
		existing, err = scanParticipant(tx.QueryRow(ctx, `UPDATE ops.model_settings_rollout_participant
SET instance_id=$1::uuid,phase=$2,heartbeat_at=clock_timestamp(),last_error_code=NULL,
    last_error_retryable=false,version=version+1,prepared_at=`+preparedExpression+`,
    activated_at=`+activatedExpression+`,retired_at=NULL
WHERE rollout_id=$3::uuid AND role=$4 AND version=$5
RETURNING `+participantColumns,
			string(registration.InstanceID), string(registration.InitialPhase), string(registration.RolloutID),
			string(registration.Role), existing.Version))
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ParticipantRecord{}, participantConflict(errors.New("model settings participant registration CAS failed"))
	}
	if err != nil {
		return domain.ParticipantRecord{}, classify(err)
	}
	if err := commit(tx, ctx); err != nil {
		return domain.ParticipantRecord{}, err
	}
	return existing, nil
}

// HeartbeatParticipant renews an exact role/instance/phase/version binding.
func (repository *Repository) HeartbeatParticipant(ctx context.Context, heartbeat application.ParticipantHeartbeat) (domain.ParticipantRecord, error) {
	if ctx == nil || !validParticipantIdentity(heartbeat.RolloutID, heartbeat.Role, heartbeat.InstanceID, heartbeat.TargetRevision) ||
		!domain.ValidParticipantPhase(heartbeat.ExpectedPhase) || heartbeat.ExpectedVersion <= 0 {
		return domain.ParticipantRecord{}, invalid(errors.New("model settings participant heartbeat is invalid"))
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
	if !state.rolloutID.Valid || state.rolloutID.String != string(heartbeat.RolloutID) ||
		!state.targetRevision.Valid || state.targetRevision.Int64 != heartbeat.TargetRevision || !domain.ActiveActivationPhase(domain.RolloutPhase(state.phase)) {
		return domain.ParticipantRecord{}, participantConflict(errors.New("model settings participant activation changed"))
	}
	runtime, err := scanRuntime(tx.QueryRow(ctx, `SELECT `+runtimeColumns+`
FROM ops.model_settings_runtime WHERE role=$1 FOR UPDATE`, string(heartbeat.Role)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ParticipantRecord{}, runtimeOwnershipLost(errors.New("model settings serving owner is missing"))
	}
	if err != nil {
		return domain.ParticipantRecord{}, classify(err)
	}
	if runtime.InstanceID != heartbeat.InstanceID {
		return domain.ParticipantRecord{}, runtimeOwnershipLost(errors.New("model settings serving ownership changed"))
	}
	record, err := scanParticipant(tx.QueryRow(ctx, `UPDATE ops.model_settings_rollout_participant
SET heartbeat_at=clock_timestamp(),version=version+1
WHERE rollout_id=$1::uuid AND role=$2 AND instance_id=$3::uuid AND target_revision=$4
  AND phase=$5 AND version=$6
RETURNING `+participantColumns, string(heartbeat.RolloutID), string(heartbeat.Role), string(heartbeat.InstanceID),
		heartbeat.TargetRevision, string(heartbeat.ExpectedPhase), heartbeat.ExpectedVersion))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ParticipantRecord{}, participantConflict(errors.New("model settings participant heartbeat CAS failed"))
	}
	if err != nil {
		return domain.ParticipantRecord{}, classify(err)
	}
	if err := commit(tx, ctx); err != nil {
		return domain.ParticipantRecord{}, err
	}
	return record, nil
}

// TransitionParticipant changes one role-local phase; activation itself uses AcknowledgeActivation.
func (repository *Repository) TransitionParticipant(ctx context.Context, command application.ParticipantTransitionCommand) (domain.ParticipantRecord, error) {
	if ctx == nil || !validParticipantIdentity(command.RolloutID, command.Role, command.InstanceID, command.TargetRevision) || command.ExpectedVersion <= 0 {
		return domain.ParticipantRecord{}, invalid(errors.New("model settings participant transition is invalid"))
	}
	if err := domain.ValidateParticipantTransition(command.ExpectedPhase, command.NextPhase); err != nil {
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
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return domain.ParticipantRecord{}, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	state, err := loadState(ctx, tx, `FOR UPDATE`)
	if err != nil {
		return domain.ParticipantRecord{}, err
	}
	if err := authorizeParticipantTransition(state, command); err != nil {
		return domain.ParticipantRecord{}, err
	}
	runtime, err := scanRuntime(tx.QueryRow(ctx, `SELECT `+runtimeColumns+`
FROM ops.model_settings_runtime WHERE role=$1 FOR UPDATE`, string(command.Role)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ParticipantRecord{}, runtimeOwnershipLost(errors.New("model settings serving owner is missing"))
	}
	if err != nil {
		return domain.ParticipantRecord{}, classify(err)
	}
	if runtime.InstanceID != command.InstanceID {
		return domain.ParticipantRecord{}, runtimeOwnershipLost(errors.New("model settings serving ownership changed"))
	}
	if command.NextPhase == domain.ParticipantPhaseRetired &&
		(runtime.RolloutID != nil || runtime.Phase != domain.RuntimePhaseActive || runtime.AppliedRevision != command.TargetRevision) {
		return domain.ParticipantRecord{}, runtimeNotReady(errors.New("model settings serving runtime is not on retired participant target"))
	}
	preparedAt := `prepared_at`
	retiredAt := `retired_at`
	if command.NextPhase == domain.ParticipantPhasePrepared {
		preparedAt = `clock_timestamp()`
	}
	if command.NextPhase == domain.ParticipantPhaseRetired {
		retiredAt = `clock_timestamp()`
	}
	record, err := scanParticipant(tx.QueryRow(ctx, `UPDATE ops.model_settings_rollout_participant
SET phase=$1,heartbeat_at=clock_timestamp(),last_error_code=$2,last_error_retryable=$3,
    prepared_at=`+preparedAt+`,retired_at=`+retiredAt+`,version=version+1
WHERE rollout_id=$4::uuid AND role=$5 AND instance_id=$6::uuid AND target_revision=$7
  AND phase=$8 AND version=$9
RETURNING `+participantColumns,
		string(command.NextPhase), nullableErrorCode(command.ErrorCode), command.ErrorRetryable,
		string(command.RolloutID), string(command.Role), string(command.InstanceID), command.TargetRevision,
		string(command.ExpectedPhase), command.ExpectedVersion))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ParticipantRecord{}, participantConflict(errors.New("model settings participant transition CAS failed"))
	}
	if err != nil {
		return domain.ParticipantRecord{}, classify(err)
	}
	if err := commit(tx, ctx); err != nil {
		return domain.ParticipantRecord{}, err
	}
	return record, nil
}

func authorizeParticipantRegistration(state stateRecord, registration application.ParticipantRegistration) error {
	if !state.rolloutID.Valid || state.rolloutID.String != string(registration.RolloutID) ||
		!state.targetRevision.Valid || state.targetRevision.Int64 != registration.TargetRevision {
		return participantConflict(errors.New("model settings participant target changed"))
	}
	phase := domain.RolloutPhase(state.phase)
	if registration.InitialPhase == domain.ParticipantPhasePreparing && phase != domain.RolloutPhasePreparing && phase != domain.RolloutPhaseArming {
		return participantConflict(errors.New("model settings participant cannot prepare in current phase"))
	}
	if registration.InitialPhase == domain.ParticipantPhaseActivated && phase != domain.RolloutPhaseActivating {
		return participantConflict(errors.New("model settings participant cannot adopt before commit"))
	}
	return nil
}

func authorizeParticipantTransition(state stateRecord, command application.ParticipantTransitionCommand) error {
	phase := domain.RolloutPhase(state.phase)
	if command.NextPhase == domain.ParticipantPhaseRetired {
		if phase != domain.RolloutPhaseIdle || state.activeRevision != command.TargetRevision {
			return participantConflict(errors.New("model settings participant cannot retire before finalization"))
		}
		return nil
	}
	if !state.rolloutID.Valid || state.rolloutID.String != string(command.RolloutID) ||
		!state.targetRevision.Valid || state.targetRevision.Int64 != command.TargetRevision {
		return participantConflict(errors.New("model settings participant target changed"))
	}
	switch command.NextPhase {
	case domain.ParticipantPhasePrepared:
		if phase != domain.RolloutPhasePreparing && phase != domain.RolloutPhaseArming {
			return participantConflict(errors.New("model settings participant cannot prepare in current phase"))
		}
	case domain.ParticipantPhaseArmed:
		if phase != domain.RolloutPhaseArming {
			return participantConflict(errors.New("model settings participant cannot arm in current phase"))
		}
	case domain.ParticipantPhaseFailed, domain.ParticipantPhaseAborted:
		if phase != domain.RolloutPhasePreparing && phase != domain.RolloutPhaseArming && phase != domain.RolloutPhaseFailed {
			return participantConflict(errors.New("model settings participant cannot abort after commit"))
		}
	}
	return nil
}

func validParticipantIdentity(rolloutID foundation.ID, role domain.RuntimeRole, instanceID foundation.ID, targetRevision int64) bool {
	return validID(rolloutID) && domain.ValidRuntimeRole(role) && validID(instanceID) && targetRevision >= 0
}

func nullableErrorCode(value string) any {
	if value == "" {
		return nil
	}
	return value
}
