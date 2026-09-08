package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
)

const healthScheduleDispatchLease = 2 * time.Minute

// ScheduleRepository 持久化 Health Schedule 并以 DB time/SKIP LOCKED 领取 due rows。
type ScheduleRepository struct {
	db            healthStore
	ids           foundation.IDGenerator
	dispatchLease time.Duration
}

var _ healthapp.ScheduleStatePort = (*ScheduleRepository)(nil)
var _ healthapp.ScheduleDispatchPort = (*ScheduleRepository)(nil)

// newScheduleRepository 构造 schedule repository。
func newScheduleRepository(db healthStore, ids foundation.IDGenerator, dispatchLeases ...time.Duration) (*ScheduleRepository, error) {
	if nilScanValue(db) || nilScanValue(ids) || len(dispatchLeases) > 1 {
		return nil, repositoryUnavailable(errors.New("health schedule repository dependencies are missing"))
	}
	dispatchLease := healthScheduleDispatchLease
	if len(dispatchLeases) == 1 {
		dispatchLease = dispatchLeases[0]
	}
	if dispatchLease <= 0 {
		return nil, repositoryInvalid(errors.New("health schedule dispatch lease is invalid"))
	}
	return &ScheduleRepository{db: db, ids: ids, dispatchLease: dispatchLease}, nil
}

// Create 创建 schedule；未指定 cadence 时由 application 收敛为 DISABLED。
func (repository *ScheduleRepository) Create(ctx context.Context, command healthapp.ScheduleCreateCommand) (domain.Schedule, error) {
	if repository == nil || nilScanValue(repository.db) || nilScanValue(repository.ids) {
		return domain.Schedule{}, repositoryUnavailable(errors.New("health schedule repository is unavailable"))
	}
	requestHash, err := healthapp.ScheduleCreateRequestHash(command)
	if err != nil || !validScheduleCommandKey(command.IdempotencyKey) {
		return domain.Schedule{}, repositoryInvalid(errors.New("health schedule create idempotency binding is invalid"))
	}
	result, err := withHealthTransaction(ctx, repository.db, foundation.TransactionOptions{}, func(ctx context.Context, tx healthTransaction) (domain.Schedule, error) {
		return repository.createTx(ctx, tx, command, requestHash)
	})
	if healthCommitFailed(err) {
		if receipt, found, recoveryErr := loadScheduleCommand(ctx, repository.db, command.WorkspaceID, command.IdempotencyKey, false); recoveryErr == nil && found && receipt.RequestHash == requestHash && receipt.CommandType == "CREATE" {
			return receipt.Schedule, nil
		}
		return domain.Schedule{}, classifyScanError(err, "HEALTH_SCHEDULE_CREATE_COMMIT_FAILED")
	}
	if err != nil {
		return domain.Schedule{}, classifyHealthTransactionError(err, "HEALTH_SCHEDULE_CREATE_BEGIN_FAILED", "HEALTH_SCHEDULE_CREATE_COMMIT_FAILED")
	}
	return result, nil
}

func (repository *ScheduleRepository) createTx(ctx context.Context, tx healthTransaction, command healthapp.ScheduleCreateCommand, requestHash string) (domain.Schedule, error) {
	if err := lockScheduleWorkspace(ctx, tx, command.WorkspaceID); err != nil {
		return domain.Schedule{}, err
	}
	if receipt, found, loadErr := loadScheduleCommand(ctx, tx, command.WorkspaceID, command.IdempotencyKey, false); loadErr != nil {
		return domain.Schedule{}, loadErr
	} else if found {
		if receipt.RequestHash != requestHash || receipt.CommandType != "CREATE" {
			return domain.Schedule{}, repositoryConflict("health schedule idempotency key is bound to another request")
		}
		return receipt.Schedule, nil
	}
	id, err := repository.ids.New()
	if err != nil {
		return domain.Schedule{}, err
	}
	now, err := databaseTime(ctx, tx)
	if err != nil {
		return domain.Schedule{}, err
	}
	schedule := domain.Schedule{ID: id, WorkspaceID: command.WorkspaceID, Scope: command.Scope, Cadence: command.Cadence, CronExpression: command.CronExpression, Timezone: command.Timezone, MaxItems: command.MaxItems, NextRunAt: cloneTime(command.NextRunAt), Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := domain.ValidateSchedule(schedule); err != nil {
		return domain.Schedule{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ops.health_schedule(
id,workspace_id,scope_type,scope_ref,scope_version,scope_schema_version,scope_hash,
cadence,cron_expression,timezone,max_items,next_run_at,last_run_at,version,created_at,updated_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,NULL,1,$13,$13)`, string(schedule.ID), string(schedule.WorkspaceID), string(schedule.Scope.Type), string(schedule.Scope.Ref), schedule.Scope.Version, schedule.Scope.SchemaVersion, nullableString(schedule.Scope.Hash), string(schedule.Cadence), nullableString(schedule.CronExpression), schedule.Timezone, schedule.MaxItems, schedule.NextRunAt, now); err != nil {
		return domain.Schedule{}, classifyScanError(err, "HEALTH_SCHEDULE_CREATE_FAILED")
	}
	if err := insertScheduleCommand(ctx, tx, command.WorkspaceID, command.IdempotencyKey, requestHash, "CREATE", schedule); err != nil {
		return domain.Schedule{}, err
	}
	return schedule, nil
}

// Update 以 expected version 更新 cadence/timezone/max/next-run。
func (repository *ScheduleRepository) Update(ctx context.Context, command healthapp.ScheduleUpdateCommand) (domain.Schedule, error) {
	if repository == nil || nilScanValue(repository.db) || !validID(command.WorkspaceID) || !validID(command.ScheduleID) || command.ExpectedVersion < 1 {
		return domain.Schedule{}, repositoryInvalid(errors.New("health schedule update command is invalid"))
	}
	requestHash, err := healthapp.ScheduleUpdateRequestHash(command)
	if err != nil || !validScheduleCommandKey(command.IdempotencyKey) {
		return domain.Schedule{}, repositoryInvalid(errors.New("health schedule update idempotency binding is invalid"))
	}
	result, err := withHealthTransaction(ctx, repository.db, foundation.TransactionOptions{}, func(ctx context.Context, tx healthTransaction) (domain.Schedule, error) {
		return repository.updateTx(ctx, tx, command, requestHash)
	})
	if healthCommitFailed(err) {
		if receipt, found, recoveryErr := loadScheduleCommand(ctx, repository.db, command.WorkspaceID, command.IdempotencyKey, false); recoveryErr == nil && found && receipt.RequestHash == requestHash && receipt.CommandType == "UPDATE" {
			return receipt.Schedule, nil
		}
		return domain.Schedule{}, classifyScanError(err, "HEALTH_SCHEDULE_UPDATE_COMMIT_FAILED")
	}
	if err != nil {
		return domain.Schedule{}, classifyHealthTransactionError(err, "HEALTH_SCHEDULE_UPDATE_BEGIN_FAILED", "HEALTH_SCHEDULE_UPDATE_COMMIT_FAILED")
	}
	return result, nil
}

func (repository *ScheduleRepository) updateTx(ctx context.Context, tx healthTransaction, command healthapp.ScheduleUpdateCommand, requestHash string) (domain.Schedule, error) {
	if err := lockScheduleWorkspace(ctx, tx, command.WorkspaceID); err != nil {
		return domain.Schedule{}, err
	}
	if receipt, found, loadErr := loadScheduleCommand(ctx, tx, command.WorkspaceID, command.IdempotencyKey, false); loadErr != nil {
		return domain.Schedule{}, loadErr
	} else if found {
		if receipt.RequestHash != requestHash || receipt.CommandType != "UPDATE" || receipt.Schedule.ID != command.ScheduleID {
			return domain.Schedule{}, repositoryConflict("health schedule idempotency key is bound to another request")
		}
		return receipt.Schedule, nil
	}
	currentState, err := loadScheduleState(ctx, tx, command.WorkspaceID, command.ScheduleID, true)
	if err != nil {
		return domain.Schedule{}, err
	}
	current := currentState.Schedule
	if current.Version != command.ExpectedVersion {
		return domain.Schedule{}, repositoryConflict("health schedule expected version did not match")
	}
	if currentState.PendingDueAt != nil {
		return domain.Schedule{}, foundation.NewError(foundation.ErrorVersionConflict, "HEALTH_SCHEDULE_DISPATCH_PENDING", false, errors.New("health schedule cannot change while a due delivery is pending"))
	}
	now, err := databaseTime(ctx, tx)
	if err != nil {
		return domain.Schedule{}, err
	}
	updated := current
	updated.Cadence, updated.CronExpression, updated.Timezone, updated.MaxItems, updated.NextRunAt = command.Cadence, command.CronExpression, command.Timezone, command.MaxItems, cloneTime(command.NextRunAt)
	if updated.Cadence == "" {
		updated.Cadence = domain.ScheduleCadenceDisabled
		updated.CronExpression, updated.NextRunAt = "", nil
	}
	updated.Version, updated.UpdatedAt = current.Version+1, now
	if err := domain.ValidateSchedule(updated); err != nil {
		return domain.Schedule{}, err
	}
	commandTag, err := tx.Exec(ctx, `UPDATE ops.health_schedule
SET cadence=$4,cron_expression=$5,timezone=$6,max_items=$7,next_run_at=$8,version=version+1,updated_at=$9
WHERE id=$1 AND workspace_id=$2 AND version=$3 AND pending_due_at IS NULL`, string(command.ScheduleID), string(command.WorkspaceID), command.ExpectedVersion, string(updated.Cadence), nullableString(updated.CronExpression), updated.Timezone, updated.MaxItems, updated.NextRunAt, now)
	if err != nil {
		return domain.Schedule{}, classifyScanError(err, "HEALTH_SCHEDULE_UPDATE_FAILED")
	}
	if commandTag.RowsAffected() != 1 {
		return domain.Schedule{}, repositoryConflict("health schedule update CAS did not match")
	}
	if err := insertScheduleCommand(ctx, tx, command.WorkspaceID, command.IdempotencyKey, requestHash, "UPDATE", updated); err != nil {
		return domain.Schedule{}, err
	}
	return updated, nil
}

// Get 返回 Workspace-scoped schedule。
func (repository *ScheduleRepository) Get(ctx context.Context, workspaceID, scheduleID foundation.ID) (domain.Schedule, error) {
	if repository == nil || nilScanValue(repository.db) || !validID(workspaceID) || !validID(scheduleID) {
		return domain.Schedule{}, repositoryInvalid(errors.New("health schedule lookup is invalid"))
	}
	return loadSchedule(ctx, repository.db, workspaceID, scheduleID, false)
}

// ClaimDue 使用 DB time 与 FOR UPDATE SKIP LOCKED 领取或重领持久 pending due。
// 初次领取只推进一次 next_run_at；租约过期后仍返回原 due_at。
func (repository *ScheduleRepository) ClaimDue(ctx context.Context, limit int) ([]healthapp.DueScheduleClaim, error) {
	if repository == nil || nilScanValue(repository.db) || limit < 1 || limit > 100 {
		return nil, repositoryInvalid(errors.New("health schedule claim limit is invalid"))
	}
	result, err := withHealthTransaction(ctx, repository.db, foundation.TransactionOptions{}, func(ctx context.Context, tx healthTransaction) ([]healthapp.DueScheduleClaim, error) {
		return repository.claimDueTx(ctx, tx, limit)
	})

	if err != nil {
		return nil, classifyHealthTransactionError(err, "HEALTH_SCHEDULE_CLAIM_BEGIN_FAILED", "HEALTH_SCHEDULE_CLAIM_COMMIT_FAILED")
	}
	return result, nil
}

func (repository *ScheduleRepository) claimDueTx(ctx context.Context, tx healthTransaction, limit int) ([]healthapp.DueScheduleClaim, error) {
	now, err := databaseTime(ctx, tx)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, scheduleSelectSQL+`
WHERE cadence<>'DISABLED' AND (
  (pending_due_at IS NOT NULL AND dispatch_lease_until<=$1)
  OR (
    pending_due_at IS NULL AND next_run_at<=$1
    AND NOT EXISTS (
      SELECT 1 FROM ops.health_scan scan
      WHERE scan.workspace_id=ops.health_schedule.workspace_id
        AND scan.scope_type=ops.health_schedule.scope_type
        AND scan.scope_ref=ops.health_schedule.scope_ref
        AND scan.status IN ('PENDING','RUNNING'))
  )
)
ORDER BY COALESCE(pending_due_at,next_run_at),id LIMIT $2 FOR UPDATE SKIP LOCKED`, now, limit)
	if err != nil {
		return nil, classifyScanError(err, "HEALTH_SCHEDULE_CLAIM_QUERY_FAILED")
	}
	var due []persistedSchedule
	for rows.Next() {
		var schedule persistedSchedule
		if err := scanScheduleState(rows, &schedule); err != nil {
			rows.Close()
			return nil, err
		}
		due = append(due, schedule)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, classifyScanError(err, "HEALTH_SCHEDULE_CLAIM_QUERY_FAILED")
	}
	rows.Close()
	claims := make([]healthapp.DueScheduleClaim, 0, len(due))
	leaseUntil := now.Add(repository.dispatchLease)
	for _, state := range due {
		schedule := state.Schedule
		var dueAt time.Time
		if state.PendingDueAt == nil {
			dueAt = schedule.NextRunAt.UTC()
			next, nextErr := domain.NextScheduleRun(schedule, now)
			if nextErr != nil {
				return nil, nextErr
			}
			commandTag, updateErr := tx.Exec(ctx, `UPDATE ops.health_schedule
SET pending_due_at=$4,dispatch_lease_until=$5,next_run_at=$6,version=version+1,updated_at=$7
WHERE id=$1 AND workspace_id=$2 AND version=$3 AND pending_due_at IS NULL`, string(schedule.ID), string(schedule.WorkspaceID), schedule.Version, dueAt, leaseUntil, next, now)
			if updateErr != nil {
				return nil, classifyScanError(updateErr, "HEALTH_SCHEDULE_CLAIM_UPDATE_FAILED")
			}
			if commandTag.RowsAffected() != 1 {
				return nil, repositoryConflict("health schedule claim CAS did not match")
			}
			schedule.NextRunAt = next
		} else {
			dueAt = state.PendingDueAt.UTC()
			commandTag, updateErr := tx.Exec(ctx, `UPDATE ops.health_schedule
SET dispatch_lease_until=$4,version=version+1,updated_at=$5
WHERE id=$1 AND workspace_id=$2 AND version=$3
  AND pending_due_at=$6 AND dispatch_lease_until<=$5`, string(schedule.ID), string(schedule.WorkspaceID), schedule.Version, leaseUntil, now, dueAt)
			if updateErr != nil {
				return nil, classifyScanError(updateErr, "HEALTH_SCHEDULE_RECLAIM_UPDATE_FAILED")
			}
			if commandTag.RowsAffected() != 1 {
				return nil, repositoryConflict("health schedule reclaim CAS did not match")
			}
		}
		schedule.Version++
		schedule.UpdatedAt = now
		claims = append(claims, healthapp.DueScheduleClaim{Schedule: schedule, DueAt: dueAt, ClaimedAt: now, LeaseUntil: leaseUntil})
	}
	return claims, nil
}

// AcknowledgeDue 在 Scan 创建或精确重放后清除 pending due，并记录该逻辑执行时间。
func (repository *ScheduleRepository) AcknowledgeDue(ctx context.Context, claim healthapp.DueScheduleClaim) error {
	if repository == nil || nilScanValue(repository.db) || !validScheduleClaim(claim) {
		return repositoryInvalid(errors.New("health schedule acknowledgement is invalid"))
	}
	_, err := withHealthTransaction(ctx, repository.db, foundation.TransactionOptions{}, func(ctx context.Context, tx healthTransaction) (struct{}, error) {
		return struct{}{}, repository.acknowledgeDueTx(ctx, tx, claim)
	})
	if healthCommitFailed(err) {
		if recovered, recoveryErr := loadScheduleState(ctx, repository.db, claim.Schedule.WorkspaceID, claim.Schedule.ID, false); recoveryErr == nil && recovered.PendingDueAt == nil && recovered.Schedule.LastRunAt != nil && recovered.Schedule.LastRunAt.Equal(claim.DueAt) {
			return nil
		} else if recoveryErr != nil {
			return errors.Join(classifyScanError(err, "HEALTH_SCHEDULE_ACK_COMMIT_FAILED"), recoveryErr)
		}
		return classifyScanError(err, "HEALTH_SCHEDULE_ACK_COMMIT_FAILED")
	}
	return classifyHealthTransactionError(err, "HEALTH_SCHEDULE_ACK_BEGIN_FAILED", "HEALTH_SCHEDULE_ACK_COMMIT_FAILED")
}

func (repository *ScheduleRepository) acknowledgeDueTx(ctx context.Context, tx healthTransaction, claim healthapp.DueScheduleClaim) error {
	current, err := loadScheduleState(ctx, tx, claim.Schedule.WorkspaceID, claim.Schedule.ID, true)
	if err != nil {
		return err
	}
	if current.PendingDueAt == nil {
		if current.Schedule.LastRunAt != nil && current.Schedule.LastRunAt.Equal(claim.DueAt) && current.Schedule.Version >= claim.Schedule.Version+1 {
			return nil
		}
		return repositoryConflict("health schedule acknowledgement no longer matches pending due")
	}
	if current.Schedule.Version != claim.Schedule.Version || !current.PendingDueAt.Equal(claim.DueAt) || current.DispatchLeaseUntil == nil || !current.DispatchLeaseUntil.Equal(claim.LeaseUntil) {
		return repositoryConflict("health schedule acknowledgement claim did not match")
	}
	now, err := databaseTime(ctx, tx)
	if err != nil {
		return err
	}
	commandTag, err := tx.Exec(ctx, `UPDATE ops.health_schedule
SET last_run_at=pending_due_at,pending_due_at=NULL,dispatch_lease_until=NULL,
    version=version+1,updated_at=$4
WHERE id=$1 AND workspace_id=$2 AND version=$3
  AND pending_due_at=$5 AND dispatch_lease_until=$6`, string(claim.Schedule.ID), string(claim.Schedule.WorkspaceID), claim.Schedule.Version, now, claim.DueAt, claim.LeaseUntil)
	if err != nil {
		return classifyScanError(err, "HEALTH_SCHEDULE_ACK_FAILED")
	}
	if commandTag.RowsAffected() != 1 {
		return repositoryConflict("health schedule acknowledgement CAS did not match")
	}
	return nil
}

// ReleaseDue 释放当前 claim 的租约并保留 pending due，供下一次 dispatcher 精确重放。
func (repository *ScheduleRepository) ReleaseDue(ctx context.Context, claim healthapp.DueScheduleClaim) error {
	if repository == nil || nilScanValue(repository.db) || !validScheduleClaim(claim) {
		return repositoryInvalid(errors.New("health schedule release is invalid"))
	}
	_, err := withHealthTransaction(ctx, repository.db, foundation.TransactionOptions{}, func(ctx context.Context, tx healthTransaction) (struct{}, error) {
		return struct{}{}, repository.releaseDueTx(ctx, tx, claim)
	})

	return classifyHealthTransactionError(err, "HEALTH_SCHEDULE_RELEASE_BEGIN_FAILED", "HEALTH_SCHEDULE_RELEASE_COMMIT_FAILED")
}

func (repository *ScheduleRepository) releaseDueTx(ctx context.Context, tx healthTransaction, claim healthapp.DueScheduleClaim) error {
	current, err := loadScheduleState(ctx, tx, claim.Schedule.WorkspaceID, claim.Schedule.ID, true)
	if err != nil {
		return err
	}
	if current.PendingDueAt == nil && current.Schedule.LastRunAt != nil && current.Schedule.LastRunAt.Equal(claim.DueAt) {
		return nil
	}
	if current.Schedule.Version != claim.Schedule.Version || current.PendingDueAt == nil || !current.PendingDueAt.Equal(claim.DueAt) || current.DispatchLeaseUntil == nil || !current.DispatchLeaseUntil.Equal(claim.LeaseUntil) {
		return repositoryConflict("health schedule release claim did not match")
	}
	now, err := databaseTime(ctx, tx)
	if err != nil {
		return err
	}
	commandTag, err := tx.Exec(ctx, `UPDATE ops.health_schedule
SET dispatch_lease_until=$4,version=version+1,updated_at=$4
WHERE id=$1 AND workspace_id=$2 AND version=$3
  AND pending_due_at=$5 AND dispatch_lease_until=$6`, string(claim.Schedule.ID), string(claim.Schedule.WorkspaceID), claim.Schedule.Version, now, claim.DueAt, claim.LeaseUntil)
	if err != nil {
		return classifyScanError(err, "HEALTH_SCHEDULE_RELEASE_FAILED")
	}
	if commandTag.RowsAffected() != 1 {
		return repositoryConflict("health schedule release CAS did not match")
	}
	return nil
}

type scheduleQueryDB interface {
	QueryRow(context.Context, string, ...any) healthRow
}

func loadSchedule(ctx context.Context, db scheduleQueryDB, workspaceID, scheduleID foundation.ID, forUpdate bool) (domain.Schedule, error) {
	state, err := loadScheduleState(ctx, db, workspaceID, scheduleID, forUpdate)
	if err != nil {
		return domain.Schedule{}, err
	}
	return state.Schedule, nil
}

type persistedSchedule struct {
	Schedule           domain.Schedule
	PendingDueAt       *time.Time
	DispatchLeaseUntil *time.Time
}

func loadScheduleState(ctx context.Context, db scheduleQueryDB, workspaceID, scheduleID foundation.ID, forUpdate bool) (persistedSchedule, error) {
	query := scheduleSelectSQL + ` WHERE id=$1 AND workspace_id=$2`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	var schedule persistedSchedule
	if err := scanScheduleState(db.QueryRow(ctx, query, string(scheduleID), string(workspaceID)), &schedule); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return persistedSchedule{}, repositoryNotFound(err)
		}
		return persistedSchedule{}, err
	}
	return schedule, nil
}

func scanSchedule(row interface{ Scan(...any) error }, target *domain.Schedule) error {
	state := persistedSchedule{Schedule: *target}
	if err := scanScheduleState(row, &state); err != nil {
		return err
	}
	*target = state.Schedule
	return nil
}

func scanScheduleState(row interface{ Scan(...any) error }, target *persistedSchedule) error {
	var id, workspaceID, scopeType, scopeRef, cadence string
	var scopeHash, cron *string
	schedule := &target.Schedule
	if err := row.Scan(&id, &workspaceID, &scopeType, &scopeRef, &schedule.Scope.Version, &schedule.Scope.SchemaVersion, &scopeHash, &cadence, &cron, &schedule.Timezone, &schedule.MaxItems, &schedule.NextRunAt, &schedule.LastRunAt, &schedule.Version, &schedule.CreatedAt, &schedule.UpdatedAt, &target.PendingDueAt, &target.DispatchLeaseUntil); err != nil {
		return err
	}
	schedule.ID, schedule.WorkspaceID = foundation.ID(id), foundation.ID(workspaceID)
	schedule.Scope.Type, schedule.Scope.Ref = domain.ScanScopeType(scopeType), foundation.ID(scopeRef)
	if scopeHash != nil {
		schedule.Scope.Hash = *scopeHash
	}
	schedule.Cadence = domain.ScheduleCadence(cadence)
	if cron != nil {
		schedule.CronExpression = *cron
	}
	return domain.ValidateSchedule(*schedule)
}

func validScheduleClaim(claim healthapp.DueScheduleClaim) bool {
	return validID(claim.Schedule.WorkspaceID) && validID(claim.Schedule.ID) && claim.Schedule.Version >= 1 &&
		!claim.DueAt.IsZero() && !claim.ClaimedAt.IsZero() && !claim.LeaseUntil.IsZero() &&
		!claim.DueAt.After(claim.ClaimedAt) && claim.LeaseUntil.After(claim.ClaimedAt)
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	result := value.UTC()
	return &result
}

type scheduleCommandReceipt struct {
	RequestHash string
	CommandType string
	ScheduleID  foundation.ID
	ScheduleVer int64
	Schedule    domain.Schedule
}

func validScheduleCommandKey(value string) bool {
	if value == "" || len(value) > 128 || value != strings.TrimSpace(value) || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return false
		}
	}
	return true
}

func lockScheduleWorkspace(ctx context.Context, tx healthTransaction, workspaceID foundation.ID) error {
	var id string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM core.workspace WHERE id=$1 FOR UPDATE`, string(workspaceID)).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return repositoryNotFound(err)
		}
		return classifyScanError(err, "HEALTH_SCHEDULE_WORKSPACE_LOCK_FAILED")
	}
	return nil
}

func insertScheduleCommand(ctx context.Context, tx healthTransaction, workspaceID foundation.ID, key, requestHash, commandType string, schedule domain.Schedule) error {
	envelope := scheduleCommandReceiptEnvelope{
		SchemaVersion: "health-schedule-command-receipt/v1",
		CommandType:   commandType,
		Schedule:      schedule,
	}
	envelope.SnapshotHash = scheduleCommandSnapshotHash(envelope)
	receipt, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ops.health_schedule_command(
workspace_id,idempotency_key,request_hash,command_type,schedule_id,schedule_version,receipt,created_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, string(workspaceID), key, requestHash, commandType, string(schedule.ID), schedule.Version, receipt, schedule.UpdatedAt.UTC()); err != nil {
		return classifyScanError(err, "HEALTH_SCHEDULE_COMMAND_RECEIPT_FAILED")
	}
	return nil
}

func loadScheduleCommand(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) healthRow
}, workspaceID foundation.ID, key string, forUpdate bool) (scheduleCommandReceipt, bool, error) {
	query := `SELECT request_hash,command_type,schedule_id::text,schedule_version,receipt
FROM ops.health_schedule_command WHERE workspace_id=$1 AND idempotency_key=$2`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	var receipt scheduleCommandReceipt
	var scheduleID string
	var raw []byte
	if err := db.QueryRow(ctx, query, string(workspaceID), key).Scan(&receipt.RequestHash, &receipt.CommandType, &scheduleID, &receipt.ScheduleVer, &raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return scheduleCommandReceipt{}, false, nil
		}
		return scheduleCommandReceipt{}, false, classifyScanError(err, "HEALTH_SCHEDULE_COMMAND_LOOKUP_FAILED")
	}
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = 32768
	limits.MaxDepth = 8
	limits.MaxObjectFields = 32
	envelope, decodeErr := strictjson.DecodeObject[scheduleCommandReceiptEnvelope](raw, limits, nil)
	if decodeErr != nil || envelope.SchemaVersion != "health-schedule-command-receipt/v1" || envelope.CommandType != receipt.CommandType || envelope.SnapshotHash != scheduleCommandSnapshotHash(envelope) {
		return scheduleCommandReceipt{}, false, repositoryConsistency(errors.New("health schedule command receipt is invalid"))
	}
	if envelope.Schedule.ID != foundation.ID(scheduleID) || envelope.Schedule.Version != receipt.ScheduleVer || envelope.Schedule.WorkspaceID != workspaceID {
		return scheduleCommandReceipt{}, false, repositoryConsistency(errors.New("health schedule command receipt binding is invalid"))
	}
	if err := domain.ValidateSchedule(envelope.Schedule); err != nil {
		return scheduleCommandReceipt{}, false, err
	}
	receipt.ScheduleID = envelope.Schedule.ID
	receipt.Schedule = envelope.Schedule
	return receipt, true, nil
}

type scheduleCommandReceiptEnvelope struct {
	SchemaVersion string          `json:"schema_version"`
	CommandType   string          `json:"command_type"`
	SnapshotHash  string          `json:"snapshot_hash"`
	Schedule      domain.Schedule `json:"schedule"`
}

// scheduleCommandSnapshotHash 绑定 receipt 的 schema、command type 和完整 schedule snapshot，防止合法 JSON 篡改后伪造 replay。
func scheduleCommandSnapshotHash(envelope scheduleCommandReceiptEnvelope) string {
	payload := struct {
		SchemaVersion string          `json:"schema_version"`
		CommandType   string          `json:"command_type"`
		Schedule      domain.Schedule `json:"schedule"`
	}{SchemaVersion: envelope.SchemaVersion, CommandType: envelope.CommandType, Schedule: envelope.Schedule}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", digest[:])
}

const scheduleColumns = `id::text,workspace_id::text,scope_type,scope_ref::text,scope_version,scope_schema_version,
	scope_hash,cadence,cron_expression,timezone,max_items,next_run_at,last_run_at,version,created_at,updated_at,
	pending_due_at,dispatch_lease_until`

const scheduleSelectSQL = `SELECT ` + scheduleColumns + ` FROM ops.health_schedule`
