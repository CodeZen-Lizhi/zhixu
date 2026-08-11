package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	"github.com/jackc/pgx/v5"
)

const defaultRuntimeTakeoverStaleAfter = application.DefaultRuntimeFreshWithin

// RegisterRuntime claims one role only after its authorized revision has loaded.
func (repository *Repository) RegisterRuntime(ctx context.Context, registration application.RuntimeRegistration) (domain.RuntimeRecord, error) {
	if registration.StaleAfter == 0 {
		registration.StaleAfter = defaultRuntimeTakeoverStaleAfter
	}
	if ctx == nil || !validRuntimeRegistration(registration) {
		return domain.RuntimeRecord{}, invalid(errors.New("model settings runtime registration is invalid"))
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return domain.RuntimeRecord{}, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	state, err := loadState(ctx, tx, `FOR UPDATE`)
	if err != nil {
		return domain.RuntimeRecord{}, err
	}
	if err := authorizeRuntimeRegistration(state, registration); err != nil {
		return domain.RuntimeRecord{}, err
	}
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return domain.RuntimeRecord{}, err
	}
	existing, err := scanRuntime(tx.QueryRow(ctx, `SELECT `+runtimeColumns+`
FROM ops.model_settings_runtime WHERE role=$1 FOR UPDATE`, string(registration.Role)))
	var record domain.RuntimeRecord
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		record, err = scanRuntime(tx.QueryRow(ctx, `INSERT INTO ops.model_settings_runtime(
role,instance_id,applied_revision,rollout_id,phase,applied_at,heartbeat_at)
VALUES($1,$2::uuid,$3,$4::uuid,$5,clock_timestamp(),clock_timestamp())
RETURNING `+runtimeColumns,
			string(registration.Role), string(registration.InstanceID), registration.AppliedRevision,
			nullableID(registration.RolloutID), string(registration.Phase)))
	case err != nil:
		return domain.RuntimeRecord{}, classify(err)
	case existing.InstanceID == registration.InstanceID:
		if existing.AppliedRevision != registration.AppliedRevision ||
			!sameOptionalID(existing.RolloutID, registration.RolloutID) || existing.Phase != registration.Phase {
			return domain.RuntimeRecord{}, runtimeConflict(errors.New("model settings runtime same-owner binding changed"))
		}
		record, err = scanRuntime(tx.QueryRow(ctx, `UPDATE ops.model_settings_runtime
SET heartbeat_at=clock_timestamp()
WHERE role=$1 AND instance_id=$2::uuid AND applied_revision=$3
  AND rollout_id IS NOT DISTINCT FROM $4::uuid AND phase=$5
RETURNING `+runtimeColumns, string(registration.Role), string(registration.InstanceID), registration.AppliedRevision,
			nullableID(registration.RolloutID), string(registration.Phase)))
	case !existing.HeartbeatAt.Before(now.Add(-registration.StaleAfter)):
		return domain.RuntimeRecord{}, runtimeConflict(errors.New("model settings runtime owner is still fresh"))
	default:
		record, err = scanRuntime(tx.QueryRow(ctx, `UPDATE ops.model_settings_runtime
SET instance_id=$1::uuid,applied_revision=$2,rollout_id=$3::uuid,phase=$4,
    applied_at=clock_timestamp(),heartbeat_at=clock_timestamp()
WHERE role=$5 AND instance_id=$6::uuid AND heartbeat_at=$7
RETURNING `+runtimeColumns, string(registration.InstanceID), registration.AppliedRevision,
			nullableID(registration.RolloutID), string(registration.Phase), string(registration.Role),
			string(existing.InstanceID), existing.HeartbeatAt))
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RuntimeRecord{}, runtimeConflict(errors.New("model settings runtime registration CAS failed"))
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
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return domain.RuntimeRecord{}, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	state, err := loadState(ctx, tx, `FOR UPDATE`)
	if err != nil {
		return domain.RuntimeRecord{}, err
	}
	existing, err := scanRuntime(tx.QueryRow(ctx, `SELECT `+runtimeColumns+`
FROM ops.model_settings_runtime WHERE role=$1 FOR UPDATE`, string(heartbeat.Role)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RuntimeRecord{}, runtimeOwnershipLost(errors.New("model settings runtime heartbeat owner is missing"))
	}
	if err != nil {
		return domain.RuntimeRecord{}, classify(err)
	}
	if existing.InstanceID != heartbeat.InstanceID || !sameOptionalID(existing.RolloutID, heartbeat.RolloutID) ||
		existing.RolloutID != nil || (existing.Phase != domain.RuntimePhaseActive && existing.Phase != domain.RuntimePhaseUnavailable) {
		return domain.RuntimeRecord{}, runtimeOwnershipLost(errors.New("model settings runtime heartbeat ownership changed"))
	}
	phase := domain.RolloutPhase(state.phase)
	authorizedRevision := existing.AppliedRevision == state.activeRevision
	if phase == domain.RolloutPhaseActivating && state.previousActive.Valid && state.targetRevision.Valid {
		authorizedRevision = existing.AppliedRevision == state.previousActive.Int64 || existing.AppliedRevision == state.targetRevision.Int64
	}
	if !authorizedRevision {
		return domain.RuntimeRecord{}, runtimeConflict(errors.New("model settings runtime heartbeat revision is not authorized"))
	}
	record, err := scanRuntime(tx.QueryRow(ctx, `UPDATE ops.model_settings_runtime
SET heartbeat_at=clock_timestamp()
WHERE role=$1 AND instance_id=$2::uuid AND applied_revision=$3
  AND rollout_id IS NOT DISTINCT FROM $4::uuid AND phase=$5 AND heartbeat_at=$6
RETURNING `+runtimeColumns, string(heartbeat.Role), string(heartbeat.InstanceID), existing.AppliedRevision,
		nullableID(heartbeat.RolloutID), string(existing.Phase), existing.HeartbeatAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RuntimeRecord{}, runtimeOwnershipLost(errors.New("model settings runtime heartbeat CAS failed"))
	}
	if err != nil {
		return domain.RuntimeRecord{}, classify(err)
	}
	if err := commit(tx, ctx); err != nil {
		return domain.RuntimeRecord{}, err
	}
	return record, nil
}

// RestoreRuntimeAvailability changes only a same-owner, same-revision serving
// row after the process has rebuilt and probed that exact local generation.
func (repository *Repository) RestoreRuntimeAvailability(
	ctx context.Context,
	command application.RestoreRuntimeAvailabilityCommand,
) (domain.RuntimeRecord, error) {
	if ctx == nil || !domain.ValidRuntimeRole(command.Role) || !validID(command.InstanceID) || command.AppliedRevision <= 0 {
		return domain.RuntimeRecord{}, invalid(errors.New("model settings runtime availability restore is invalid"))
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return domain.RuntimeRecord{}, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	state, err := loadState(ctx, tx, `FOR UPDATE`)
	if err != nil {
		return domain.RuntimeRecord{}, err
	}
	if !runtimeAvailabilityRestoreAuthorized(state, command.AppliedRevision) {
		return domain.RuntimeRecord{}, runtimeConflict(errors.New("model settings runtime availability restore is not authorized"))
	}
	existing, err := scanRuntime(tx.QueryRow(ctx, `SELECT `+runtimeColumns+`
FROM ops.model_settings_runtime WHERE role=$1 FOR UPDATE`, string(command.Role)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RuntimeRecord{}, runtimeOwnershipLost(errors.New("model settings runtime availability owner is missing"))
	}
	if err != nil {
		return domain.RuntimeRecord{}, classify(err)
	}
	if existing.InstanceID != command.InstanceID || existing.RolloutID != nil {
		return domain.RuntimeRecord{}, runtimeOwnershipLost(errors.New("model settings runtime availability ownership changed"))
	}
	if existing.AppliedRevision != command.AppliedRevision {
		return domain.RuntimeRecord{}, runtimeConflict(errors.New("model settings runtime availability revision changed"))
	}
	if existing.Phase == domain.RuntimePhaseActive {
		record, updateErr := scanRuntime(tx.QueryRow(ctx, `UPDATE ops.model_settings_runtime
SET heartbeat_at=clock_timestamp()
WHERE role=$1 AND instance_id=$2::uuid AND applied_revision=$3
  AND rollout_id IS NULL AND phase='active' AND heartbeat_at=$4
RETURNING `+runtimeColumns, string(command.Role), string(command.InstanceID), command.AppliedRevision, existing.HeartbeatAt))
		if errors.Is(updateErr, pgx.ErrNoRows) {
			return domain.RuntimeRecord{}, runtimeOwnershipLost(errors.New("model settings runtime availability replay CAS failed"))
		}
		if updateErr != nil {
			return domain.RuntimeRecord{}, classify(updateErr)
		}
		if err := commit(tx, ctx); err != nil {
			return domain.RuntimeRecord{}, err
		}
		return record, nil
	}
	if existing.Phase != domain.RuntimePhaseUnavailable {
		return domain.RuntimeRecord{}, runtimeConflict(errors.New("model settings runtime is not unavailable"))
	}
	record, err := scanRuntime(tx.QueryRow(ctx, `UPDATE ops.model_settings_runtime
SET phase='active',heartbeat_at=clock_timestamp()
WHERE role=$1 AND instance_id=$2::uuid AND applied_revision=$3
  AND rollout_id IS NULL AND phase='unavailable' AND heartbeat_at=$4
RETURNING `+runtimeColumns, string(command.Role), string(command.InstanceID), command.AppliedRevision, existing.HeartbeatAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RuntimeRecord{}, runtimeOwnershipLost(errors.New("model settings runtime availability CAS failed"))
	}
	if err != nil {
		return domain.RuntimeRecord{}, classify(err)
	}
	if err := commit(tx, ctx); err != nil {
		return domain.RuntimeRecord{}, err
	}
	return record, nil
}

func runtimeAvailabilityRestoreAuthorized(state stateRecord, revision int64) bool {
	switch domain.RolloutPhase(state.phase) {
	case domain.RolloutPhaseIdle, domain.RolloutPhaseFailed:
		return state.activeRevision == revision
	case domain.RolloutPhasePreparing, domain.RolloutPhaseArming:
		return state.activeRevision == revision && state.previousActive.Valid && state.previousActive.Int64 == revision
	case domain.RolloutPhaseActivating:
		return state.activeRevision == revision && state.targetRevision.Valid && state.targetRevision.Int64 == revision
	default:
		return false
	}
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
	if phase != string(domain.RolloutPhaseIdle) && phase != string(domain.RolloutPhaseFailed) && phase != string(domain.RolloutPhasePreparing) {
		return enqueuePaused(errors.New("model settings rollout blocks workflow enqueue"))
	}
	return nil
}

func authorizeRuntimeRegistration(state stateRecord, registration application.RuntimeRegistration) error {
	if registration.RolloutID != nil || (registration.Phase != domain.RuntimePhaseActive && registration.Phase != domain.RuntimePhaseUnavailable) {
		return runtimeConflict(errors.New("model settings runtime registration must describe serving ownership"))
	}
	phase := domain.RolloutPhase(state.phase)
	expectedRevision := state.activeRevision
	if phase == domain.RolloutPhasePreparing || phase == domain.RolloutPhaseArming || phase == domain.RolloutPhaseFailed {
		if state.previousActive.Valid {
			expectedRevision = state.previousActive.Int64
		}
	}
	if registration.AppliedRevision != expectedRevision {
		return runtimeConflict(errors.New("model settings serving runtime revision is not authorized"))
	}
	return nil
}

func validRuntimeRegistration(registration application.RuntimeRegistration) bool {
	if !domain.ValidRuntimeRole(registration.Role) || !validID(registration.InstanceID) || registration.AppliedRevision < 0 ||
		!validOptionalID(registration.RolloutID) || !domain.ValidRuntimePhase(registration.Phase) ||
		!validFreshWithin(registration.StaleAfter) {
		return false
	}
	if registration.RolloutID == nil {
		return registration.Phase == domain.RuntimePhaseActive || registration.Phase == domain.RuntimePhaseUnavailable
	}
	return registration.Phase == domain.RuntimePhasePrepared || registration.Phase == domain.RuntimePhaseVerifying
}

func sameOptionalID(left, right *foundation.ID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
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
