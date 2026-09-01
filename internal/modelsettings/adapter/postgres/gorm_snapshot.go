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

// Snapshot reads desired, active, rollout, and runtime projections from one repeatable snapshot.
func (repository *GORMRepository) Snapshot(ctx context.Context, staleAfter time.Duration) (snapshot domain.Snapshot, err error) {
	if err = repository.ready(ctx); err != nil {
		return domain.Snapshot{}, err
	}
	if staleAfter == 0 {
		staleAfter = defaultSnapshotStaleAfter
	}
	if !application.ValidRuntimeFreshWithin(staleAfter) {
		return domain.Snapshot{}, invalid(errors.New("model settings snapshot stale interval is invalid"))
	}
	err = repository.within(ctx, foundation.TransactionOptions{
		Isolation: foundation.TransactionIsolationRepeatableRead,
		ReadOnly:  true,
	}, func(callbackCtx context.Context, _ foundation.TransactionScope, database *gorm.DB) error {
		var snapshotErr error
		snapshot, snapshotErr = gormSnapshotTx(callbackCtx, database, staleAfter)
		return snapshotErr
	})
	if err != nil {
		return domain.Snapshot{}, err
	}
	return snapshot, nil
}

func gormSnapshotTx(ctx context.Context, database *gorm.DB, staleAfter time.Duration) (domain.Snapshot, error) {
	state, err := gormLoadState(ctx, database, ``)
	if err != nil {
		return domain.Snapshot{}, err
	}
	desired, err := gormLoadRevision(ctx, database, state.desiredRevision)
	if err != nil {
		return domain.Snapshot{}, err
	}
	active, err := gormLoadRevision(ctx, database, state.activeRevision)
	if err != nil {
		return domain.Snapshot{}, err
	}
	now, err := gormDatabaseNow(ctx, database)
	if err != nil {
		return domain.Snapshot{}, err
	}
	runtimes, err := gormLoadRuntimeSummaries(ctx, database, now, staleAfter)
	if err != nil {
		return domain.Snapshot{}, err
	}
	rollout, err := state.rollout()
	if err != nil {
		return domain.Snapshot{}, err
	}
	participants, err := gormLoadParticipantSummaries(ctx, database, state, now, staleAfter)
	if err != nil {
		return domain.Snapshot{}, err
	}
	localRuntime, err := gormLoadLocalRuntimeSummary(ctx, database, now, staleAfter, state)
	if err != nil {
		return domain.Snapshot{}, err
	}
	activeReady := runtimeReady(runtimes.API, state.activeRevision) && runtimeReady(runtimes.Worker, state.activeRevision)
	snapshot := domain.Snapshot{
		DesiredRevision: state.desiredRevision, ActiveRevision: state.activeRevision,
		DesiredSettings: desired.summary(), ActiveSettings: active.summary(), Runtime: runtimes, Rollout: rollout,
		Participants:    participants,
		ApplyRequired:   state.desiredRevision != state.activeRevision || domain.ActiveActivationPhase(rollout.Phase) || !activeReady,
		RestartRequired: false,
		LocalRuntime:    localRuntime,
	}
	snapshot.ChatCapability = chatCapability(snapshot.ActiveSettings, activeReady)
	snapshot.EmbeddingCapability = embeddingCapability(snapshot.ActiveSettings, activeReady)
	return snapshot, nil
}

func gormLoadLocalRuntimeSummary(ctx context.Context, database *gorm.DB, now time.Time, staleAfter time.Duration, state stateRecord) (domain.LocalRuntimeSummary, error) {
	result := domain.LocalRuntimeSummary{Phase: "stopped"}
	row, err := gormRawRow(ctx, database, `SELECT mode,observed_phase,requirement_hash,ready_requirement_hash,heartbeat_at,last_error_code,last_error_retryable FROM ops.managed_ollama_runtime WHERE singleton=true`)
	if err != nil {
		return domain.LocalRuntimeSummary{}, classifyGORM(ctx, err)
	}
	var mode, phase, requirementHash, readyHash string
	var heartbeat *time.Time
	var errorCode *string
	if err = row.Scan(&mode, &phase, &requirementHash, &readyHash, &heartbeat, &errorCode, &result.OperationRetryable); gormNoRows(err) {
		return result, nil
	} else if err != nil {
		return domain.LocalRuntimeSummary{}, classifyGORM(ctx, err)
	}
	result.Mode, result.Phase, result.RequirementHash, result.ReadyHash = mode, phase, requirementHash, readyHash
	if errorCode != nil {
		result.OperationError = *errorCode
	}
	result.Fresh = heartbeat != nil && !heartbeat.Before(now.Add(-staleAfter))
	if !state.rolloutID.Valid {
		return result, nil
	}
	row, err = gormRawRow(ctx, database, `SELECT operation_id::text,phase,completed_bytes,total_bytes,progress_known,error_code,error_retryable FROM ops.managed_ollama_operations WHERE kind='activation' AND rollout_id=?::uuid ORDER BY created_at DESC LIMIT 1`, state.rolloutID.String)
	if err != nil {
		return domain.LocalRuntimeSummary{}, classifyGORM(ctx, err)
	}
	var operationID, operationPhase string
	var operationError *string
	var operationRetryable, known bool
	var completed int64
	var total *int64
	if err = row.Scan(&operationID, &operationPhase, &completed, &total, &known, &operationError, &operationRetryable); gormNoRows(err) {
		return result, nil
	} else if err != nil {
		return domain.LocalRuntimeSummary{}, classifyGORM(ctx, err)
	}
	if id, parseErr := foundation.ParseID(operationID); parseErr == nil {
		result.OperationID = &id
	}
	result.OperationPhase, result.CompletedBytes, result.ProgressKnown = operationPhase, completed, known
	if operationError != nil {
		result.OperationError = *operationError
	}
	result.OperationRetryable = operationRetryable
	if known && total != nil {
		result.TotalBytes = total
	}
	return result, nil
}

func gormLoadParticipantSummaries(ctx context.Context, database *gorm.DB, state stateRecord, now time.Time, staleAfter time.Duration) (domain.ParticipantSummaries, error) {
	result := domain.ParticipantSummaries{}
	if !state.rolloutID.Valid {
		return result, nil
	}
	rows, err := gormRawRows(ctx, database, `SELECT `+participantColumns+`
FROM ops.model_settings_rollout_participant WHERE rollout_id=?::uuid ORDER BY role`, state.rolloutID.String)
	if err != nil {
		return domain.ParticipantSummaries{}, classifyGORM(ctx, err)
	}
	defer rows.Close()
	for rows.Next() {
		record, scanErr := scanParticipant(rows)
		if scanErr != nil {
			return domain.ParticipantSummaries{}, classifyGORM(ctx, scanErr)
		}
		summary := domain.ParticipantSummary{
			Present: true, TargetRevision: record.TargetRevision, Phase: record.Phase,
			Fresh: !record.HeartbeatAt.Before(now.Add(-staleAfter)), LastErrorCode: record.LastErrorCode,
			ErrorRetryable: record.ErrorRetryable,
		}
		switch record.Role {
		case domain.RuntimeRoleAPI:
			result.API = summary
		case domain.RuntimeRoleWorker:
			result.Worker = summary
		default:
			return domain.ParticipantSummaries{}, corrupt(errors.New("model settings participant role is invalid"))
		}
	}
	if err = rows.Err(); err != nil {
		return domain.ParticipantSummaries{}, classifyGORM(ctx, err)
	}
	return result, nil
}

func gormLoadRuntimeSummaries(ctx context.Context, database *gorm.DB, now time.Time, staleAfter time.Duration) (domain.RuntimeSummaries, error) {
	missing := domain.RuntimeSummary{Phase: domain.RuntimePhaseUnavailable, Fresh: false}
	result := domain.RuntimeSummaries{API: missing, Worker: missing}
	rows, err := gormRawRows(ctx, database, `SELECT `+runtimeColumns+` FROM ops.model_settings_runtime ORDER BY role`)
	if err != nil {
		return domain.RuntimeSummaries{}, classifyGORM(ctx, err)
	}
	defer rows.Close()
	for rows.Next() {
		record, scanErr := scanRuntime(rows)
		if scanErr != nil {
			return domain.RuntimeSummaries{}, classifyGORM(ctx, scanErr)
		}
		summary := domain.RuntimeSummary{
			AppliedRevision: record.AppliedRevision, Phase: record.Phase,
			Fresh: !record.HeartbeatAt.Before(now.Add(-staleAfter)),
		}
		switch record.Role {
		case domain.RuntimeRoleAPI:
			result.API = summary
		case domain.RuntimeRoleWorker:
			result.Worker = summary
		default:
			return domain.RuntimeSummaries{}, corrupt(errors.New("model settings runtime role is invalid"))
		}
	}
	if err = rows.Err(); err != nil {
		return domain.RuntimeSummaries{}, classifyGORM(ctx, err)
	}
	return result, nil
}
