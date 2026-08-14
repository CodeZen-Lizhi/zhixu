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

const defaultSnapshotStaleAfter = application.DefaultRuntimeFreshWithin

// Snapshot reads desired, active, rollout, and runtime projections from one repeatable snapshot.
func (repository *Repository) Snapshot(ctx context.Context, staleAfter time.Duration) (domain.Snapshot, error) {
	if ctx == nil {
		return domain.Snapshot{}, invalid(errors.New("model settings snapshot context is nil"))
	}
	if staleAfter == 0 {
		staleAfter = defaultSnapshotStaleAfter
	}
	if !application.ValidRuntimeFreshWithin(staleAfter) {
		return domain.Snapshot{}, invalid(errors.New("model settings snapshot stale interval is invalid"))
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return domain.Snapshot{}, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	snapshot, err := snapshotTx(ctx, tx, staleAfter)
	if err != nil {
		return domain.Snapshot{}, err
	}
	if err := commit(tx, ctx); err != nil {
		return domain.Snapshot{}, err
	}
	return snapshot, nil
}

func snapshotTx(ctx context.Context, tx pgx.Tx, staleAfter time.Duration) (domain.Snapshot, error) {
	state, err := loadState(ctx, tx, ``)
	if err != nil {
		return domain.Snapshot{}, err
	}
	desired, err := loadRevision(ctx, tx, state.desiredRevision)
	if err != nil {
		return domain.Snapshot{}, err
	}
	active, err := loadRevision(ctx, tx, state.activeRevision)
	if err != nil {
		return domain.Snapshot{}, err
	}
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return domain.Snapshot{}, err
	}
	runtimes, err := loadRuntimeSummaries(ctx, tx, now, staleAfter)
	if err != nil {
		return domain.Snapshot{}, err
	}
	rollout, err := state.rollout()
	if err != nil {
		return domain.Snapshot{}, err
	}
	participants, err := loadParticipantSummaries(ctx, tx, state, now, staleAfter)
	if err != nil {
		return domain.Snapshot{}, err
	}
	localRuntime, localErr := loadLocalRuntimeSummary(ctx, tx, now, staleAfter, state)
	if localErr != nil {
		return domain.Snapshot{}, localErr
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

func loadLocalRuntimeSummary(ctx context.Context, tx pgx.Tx, now time.Time, staleAfter time.Duration, state stateRecord) (domain.LocalRuntimeSummary, error) {
	result := domain.LocalRuntimeSummary{Phase: "stopped"}
	var mode, phase, requirementHash, readyHash string
	var heartbeat *time.Time
	var errorCode *string
	if err := tx.QueryRow(ctx, `SELECT mode,observed_phase,requirement_hash,ready_requirement_hash,heartbeat_at,last_error_code,last_error_retryable FROM ops.managed_ollama_runtime WHERE singleton=true`).Scan(&mode, &phase, &requirementHash, &readyHash, &heartbeat, &errorCode, &result.OperationRetryable); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return result, nil
		}
		return domain.LocalRuntimeSummary{}, classify(err)
	}
	result.Mode, result.Phase, result.RequirementHash, result.ReadyHash = mode, phase, requirementHash, readyHash
	if errorCode != nil {
		result.OperationError = *errorCode
	}
	result.Fresh = heartbeat != nil && !heartbeat.Before(now.Add(-staleAfter))
	if state.rolloutID.Valid {
		var operationID, operationPhase string
		var operationError *string
		var operationRetryable bool
		var completed int64
		var total *int64
		var known bool
		row := tx.QueryRow(ctx, `SELECT operation_id::text,phase,completed_bytes,total_bytes,progress_known,error_code,error_retryable FROM ops.managed_ollama_operations WHERE kind='activation' AND rollout_id=$1::uuid ORDER BY created_at DESC LIMIT 1`, state.rolloutID.String)
		if err := row.Scan(&operationID, &operationPhase, &completed, &total, &known, &operationError, &operationRetryable); err == nil {
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
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return domain.LocalRuntimeSummary{}, classify(err)
		}
	}
	return result, nil
}

func loadParticipantSummaries(ctx context.Context, tx pgx.Tx, state stateRecord, now time.Time, staleAfter time.Duration) (domain.ParticipantSummaries, error) {
	result := domain.ParticipantSummaries{}
	if !state.rolloutID.Valid {
		return result, nil
	}
	rows, err := tx.Query(ctx, `SELECT `+participantColumns+`
FROM ops.model_settings_rollout_participant WHERE rollout_id=$1::uuid ORDER BY role`, state.rolloutID.String)
	if err != nil {
		return domain.ParticipantSummaries{}, classify(err)
	}
	defer rows.Close()
	for rows.Next() {
		record, scanErr := scanParticipant(rows)
		if scanErr != nil {
			return domain.ParticipantSummaries{}, classify(scanErr)
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
	if err := rows.Err(); err != nil {
		return domain.ParticipantSummaries{}, classify(err)
	}
	return result, nil
}

func loadRuntimeSummaries(ctx context.Context, tx pgx.Tx, now time.Time, staleAfter time.Duration) (domain.RuntimeSummaries, error) {
	missing := domain.RuntimeSummary{Phase: domain.RuntimePhaseUnavailable, Fresh: false}
	result := domain.RuntimeSummaries{API: missing, Worker: missing}
	rows, err := tx.Query(ctx, `SELECT `+runtimeColumns+` FROM ops.model_settings_runtime ORDER BY role`)
	if err != nil {
		return domain.RuntimeSummaries{}, classify(err)
	}
	defer rows.Close()
	for rows.Next() {
		record, scanErr := scanRuntime(rows)
		if scanErr != nil {
			return domain.RuntimeSummaries{}, classify(scanErr)
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
	if err := rows.Err(); err != nil {
		return domain.RuntimeSummaries{}, classify(err)
	}
	return result, nil
}

func runtimeReady(runtime domain.RuntimeSummary, revision int64) bool {
	return runtime.Fresh && runtime.Phase == domain.RuntimePhaseActive && runtime.AppliedRevision == revision
}

func chatCapability(active domain.SettingsSummary, runtimesReady bool) domain.Capability {
	if active.Settings.Chat.Provider == domain.ChatProviderDisabled {
		return domain.CapabilityDisabled
	}
	if !runtimesReady {
		return domain.CapabilityUnavailable
	}
	return domain.CapabilityConfigured
}

func embeddingCapability(active domain.SettingsSummary, runtimesReady bool) domain.Capability {
	if active.Settings.Embedding.Provider == domain.EmbeddingProviderDisabled {
		return domain.CapabilityDisabled
	}
	if !runtimesReady || active.Settings.Embedding.Provider == domain.EmbeddingProviderOpenAICompatible && !active.Secrets.EmbeddingConfigured {
		return domain.CapabilityUnavailable
	}
	return domain.CapabilityConfigured
}
