package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	"github.com/jackc/pgx/v5"
)

// RegisterRuntime claims one role only after its authorized revision has loaded.
func (repository *Repository) RegisterRuntime(ctx context.Context, registration application.RuntimeRegistration) (domain.RuntimeRecord, error) {
	if ctx == nil || !validRuntimeRegistration(registration) {
		return domain.RuntimeRecord{}, invalid(errors.New("model settings runtime registration is invalid"))
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return domain.RuntimeRecord{}, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	state, err := loadState(ctx, tx, `FOR SHARE`)
	if err != nil {
		return domain.RuntimeRecord{}, err
	}
	if err := authorizeRuntimeRegistration(state, registration); err != nil {
		return domain.RuntimeRecord{}, err
	}
	row := tx.QueryRow(ctx, `INSERT INTO ops.model_settings_runtime(
role,instance_id,applied_revision,rollout_id,phase,applied_at,heartbeat_at)
VALUES($1,$2::uuid,$3,$4::uuid,$5,clock_timestamp(),clock_timestamp())
ON CONFLICT(role) DO UPDATE SET
instance_id=EXCLUDED.instance_id,
applied_revision=EXCLUDED.applied_revision,
rollout_id=EXCLUDED.rollout_id,
phase=EXCLUDED.phase,
applied_at=CASE WHEN ops.model_settings_runtime.instance_id=EXCLUDED.instance_id
    THEN ops.model_settings_runtime.applied_at ELSE EXCLUDED.applied_at END,
heartbeat_at=EXCLUDED.heartbeat_at
WHERE ops.model_settings_runtime.instance_id<>EXCLUDED.instance_id
   OR (ops.model_settings_runtime.applied_revision=EXCLUDED.applied_revision
       AND ops.model_settings_runtime.rollout_id IS NOT DISTINCT FROM EXCLUDED.rollout_id
       AND ops.model_settings_runtime.phase=EXCLUDED.phase)
RETURNING `+runtimeColumns,
		string(registration.Role), string(registration.InstanceID), registration.AppliedRevision,
		nullableID(registration.RolloutID), string(registration.Phase))
	record, err := scanRuntime(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RuntimeRecord{}, runtimeConflict(errors.New("model settings runtime registration changed"))
	}
	if err != nil {
		return domain.RuntimeRecord{}, classify(err)
	}
	if err := commit(tx, ctx); err != nil {
		return domain.RuntimeRecord{}, err
	}
	return record, nil
}

// HeartbeatRuntime renews ownership only while the loaded revision remains authorized.
func (repository *Repository) HeartbeatRuntime(ctx context.Context, heartbeat application.RuntimeHeartbeat) (domain.RuntimeRecord, error) {
	if ctx == nil || !domain.ValidRuntimeRole(heartbeat.Role) || !validID(heartbeat.InstanceID) || !validOptionalID(heartbeat.RolloutID) {
		return domain.RuntimeRecord{}, invalid(errors.New("model settings runtime heartbeat is invalid"))
	}
	record, err := scanRuntime(repository.db.QueryRow(ctx, `UPDATE ops.model_settings_runtime AS runtime
SET heartbeat_at=clock_timestamp()
FROM ops.model_settings_state AS state
WHERE state.singleton=true
  AND runtime.role=$1 AND runtime.instance_id=$2::uuid
  AND runtime.rollout_id IS NOT DISTINCT FROM $3::uuid
  AND (
    (runtime.rollout_id IS NULL AND runtime.applied_revision=state.active_revision
      AND runtime.phase IN ('active','unavailable')
      AND (state.phase IN ('idle','failed','validating') OR (state.phase='draining' AND runtime.phase='active')))
    OR
    (runtime.rollout_id=state.rollout_id AND (
      (runtime.phase IN ('quiescing','quiesced') AND state.phase='draining'
        AND runtime.applied_revision=state.previous_active_revision)
      OR (runtime.phase='prepared' AND state.phase IN ('applying','verifying')
        AND runtime.applied_revision=state.target_revision)
      OR (runtime.phase='verifying' AND state.phase='verifying'
        AND runtime.applied_revision=state.target_revision)
    ))
  )
RETURNING `+prefixedRuntimeColumns("runtime"), string(heartbeat.Role), string(heartbeat.InstanceID), nullableID(heartbeat.RolloutID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RuntimeRecord{}, runtimeConflict(errors.New("model settings runtime heartbeat ownership changed"))
	}
	if err != nil {
		return domain.RuntimeRecord{}, classify(err)
	}
	return record, nil
}

// SetRuntimePhase atomically binds active to the controlling rollout when quiescing,
// then requires that exact binding for later process-driven phase transitions.
func (repository *Repository) SetRuntimePhase(ctx context.Context, command application.RuntimePhaseCommand) (domain.RuntimeRecord, error) {
	if ctx == nil || !domain.ValidRuntimeRole(command.Role) || !validID(command.InstanceID) || command.RolloutID == nil || !validID(*command.RolloutID) {
		return domain.RuntimeRecord{}, invalid(errors.New("model settings runtime phase command is invalid"))
	}
	if err := domain.ValidateRuntimeTransition(command.ExpectedPhase, command.NextPhase); err != nil {
		return domain.RuntimeRecord{}, err
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return domain.RuntimeRecord{}, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	state, err := loadState(ctx, tx, `FOR SHARE`)
	if err != nil {
		return domain.RuntimeRecord{}, err
	}
	if !state.rolloutID.Valid || state.rolloutID.String != string(*command.RolloutID) {
		return domain.RuntimeRecord{}, runtimeConflict(errors.New("model settings runtime rollout changed"))
	}
	var existingRollout any = string(*command.RolloutID)
	var nextRollout any = string(*command.RolloutID)
	switch {
	case command.ExpectedPhase == domain.RuntimePhaseActive && command.NextPhase == domain.RuntimePhaseQuiescing:
		if state.phase != string(domain.RolloutPhaseDraining) || !state.previousActive.Valid {
			return domain.RuntimeRecord{}, runtimeConflict(errors.New("model settings runtime cannot begin quiescing"))
		}
		existingRollout = nil
	case command.ExpectedPhase == domain.RuntimePhaseQuiescing && command.NextPhase == domain.RuntimePhaseQuiesced:
		if state.phase != string(domain.RolloutPhaseDraining) {
			return domain.RuntimeRecord{}, runtimeConflict(errors.New("model settings runtime cannot finish quiescing"))
		}
	case command.ExpectedPhase == domain.RuntimePhasePrepared && command.NextPhase == domain.RuntimePhaseVerifying:
		if state.phase != string(domain.RolloutPhaseVerifying) {
			return domain.RuntimeRecord{}, runtimeConflict(errors.New("model settings runtime cannot verify"))
		}
	default:
		return domain.RuntimeRecord{}, runtimeConflict(errors.New("model settings runtime transition is invalid"))
	}
	record, err := scanRuntime(tx.QueryRow(ctx, `UPDATE ops.model_settings_runtime
SET rollout_id=$1::uuid,phase=$2,heartbeat_at=clock_timestamp()
WHERE role=$3 AND instance_id=$4::uuid AND phase=$5
  AND rollout_id IS NOT DISTINCT FROM $6::uuid
RETURNING `+runtimeColumns,
		nextRollout, string(command.NextPhase), string(command.Role), string(command.InstanceID),
		string(command.ExpectedPhase), existingRollout))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RuntimeRecord{}, runtimeConflict(errors.New("model settings runtime phase ownership changed"))
	}
	if err != nil {
		return domain.RuntimeRecord{}, classify(err)
	}
	if err := commit(tx, ctx); err != nil {
		return domain.RuntimeRecord{}, err
	}
	return record, nil
}

// CheckEnqueue implements riveradapter.EnqueueFence inside the caller's transaction.
// Failed has no live lease and has already restored previous active/runtime ownership.
func (repository *Repository) CheckEnqueue(ctx context.Context, tx pgx.Tx) error {
	if ctx == nil || nilInterface(tx) {
		return invalid(errors.New("model settings enqueue transaction is invalid"))
	}
	var phase string
	if err := tx.QueryRow(ctx, `SELECT phase FROM ops.model_settings_state
WHERE singleton=true FOR SHARE`).Scan(&phase); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return corrupt(errors.New("model settings singleton state is missing"))
		}
		return classify(err)
	}
	if phase != string(domain.RolloutPhaseIdle) && phase != string(domain.RolloutPhaseFailed) && phase != string(domain.RolloutPhaseValidating) {
		return enqueuePaused(errors.New("model settings rollout blocks workflow enqueue"))
	}
	return nil
}

func authorizeRuntimeRegistration(state stateRecord, registration application.RuntimeRegistration) error {
	if registration.RolloutID == nil {
		if state.activeRevision != registration.AppliedRevision ||
			state.phase != string(domain.RolloutPhaseIdle) && state.phase != string(domain.RolloutPhaseFailed) &&
				state.phase != string(domain.RolloutPhaseValidating) {
			return runtimeConflict(errors.New("model settings active runtime revision is not authorized"))
		}
		return nil
	}
	if !state.rolloutID.Valid || state.rolloutID.String != string(*registration.RolloutID) || !state.targetRevision.Valid ||
		state.targetRevision.Int64 != registration.AppliedRevision {
		return runtimeConflict(errors.New("model settings candidate runtime revision is not authorized"))
	}
	if registration.Phase == domain.RuntimePhasePrepared && state.phase != string(domain.RolloutPhaseApplying) && state.phase != string(domain.RolloutPhaseVerifying) {
		return runtimeConflict(errors.New("model settings candidate runtime is not applying"))
	}
	if registration.Phase == domain.RuntimePhaseVerifying && state.phase != string(domain.RolloutPhaseVerifying) {
		return runtimeConflict(errors.New("model settings candidate runtime is not verifying"))
	}
	return nil
}

func validRuntimeRegistration(registration application.RuntimeRegistration) bool {
	if !domain.ValidRuntimeRole(registration.Role) || !validID(registration.InstanceID) || registration.AppliedRevision < 0 ||
		!validOptionalID(registration.RolloutID) || !domain.ValidRuntimePhase(registration.Phase) {
		return false
	}
	if registration.RolloutID == nil {
		return registration.Phase == domain.RuntimePhaseActive || registration.Phase == domain.RuntimePhaseUnavailable
	}
	return registration.Phase == domain.RuntimePhasePrepared || registration.Phase == domain.RuntimePhaseVerifying
}

func validOptionalID(id *foundation.ID) bool { return id == nil || validID(*id) }

func nullableID(id *foundation.ID) any {
	if id == nil {
		return nil
	}
	return string(*id)
}

func prefixedRuntimeColumns(alias string) string {
	return alias + `.role,` + alias + `.instance_id::text,` + alias + `.applied_revision,` +
		alias + `.rollout_id::text,` + alias + `.phase,` + alias + `.applied_at,` + alias + `.heartbeat_at`
}
