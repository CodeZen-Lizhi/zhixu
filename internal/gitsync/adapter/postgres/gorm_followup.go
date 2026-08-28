package postgres

import (
	"context"

	"gorm.io/gorm"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
)

const (
	gormRequireIndexLeaseSQL = `SELECT 1 FROM ops.git_sync_outbox
	WHERE id=? AND workspace_id=? AND run_id=? AND kind='INDEX_FOLLOWUP'
	  AND lease_owner=? AND version=? AND lease_expires_at>clock_timestamp()
	  AND published_at IS NULL AND poisoned_at IS NULL
	FOR UPDATE`

	gormLoadRunForUpdateSQL = `SELECT ` + gormRunColumns + `
	FROM ops.git_sync_run WHERE workspace_id=? AND id=? FOR UPDATE`

	gormBeginIndexSQL = `UPDATE ops.git_sync_run SET
	index_status='RUNNING',index_error_code='',index_retryable=false,
	version=version+1,updated_at=clock_timestamp()
	WHERE workspace_id=? AND id=? AND version=? AND index_status='PENDING'
	RETURNING ` + gormRunColumns

	gormCompleteIndexSQL = `UPDATE ops.git_sync_run SET
	index_status='SUCCEEDED',index_error_code='',index_retryable=false,index_version_id=NULLIF(?,'')::uuid,
	version=version+1,updated_at=clock_timestamp()
	WHERE workspace_id=? AND id=? AND version=? AND status='SUCCEEDED' AND index_status='RUNNING'
	RETURNING ` + gormRunColumns

	gormFailIndexSQL = `UPDATE ops.git_sync_run SET
	index_status='FAILED',index_error_code=?,index_retryable=?,index_version_id=NULL,
	version=version+1,updated_at=clock_timestamp()
	WHERE workspace_id=? AND id=? AND version=? AND status='SUCCEEDED' AND index_status='RUNNING'
	RETURNING ` + gormRunColumns

	gormRetryReceiptSQL = `SELECT request_hash,run_id::text,result_version
	FROM ops.git_sync_index_retry_receipt WHERE workspace_id=? AND idempotency_key=?`

	gormPendingIndexOutboxSQL = `SELECT id::text FROM ops.git_sync_outbox
	WHERE workspace_id=? AND run_id=? AND kind='INDEX_FOLLOWUP'
	  AND published_at IS NULL AND poisoned_at IS NULL
	ORDER BY created_at,id LIMIT 1 FOR UPDATE`

	gormRetryIndexRunSQL = `UPDATE ops.git_sync_run SET
	index_status='PENDING',index_error_code='',index_retryable=false,index_version_id=NULL,
	version=version+1,updated_at=clock_timestamp()
	WHERE workspace_id=? AND id=? AND version=? RETURNING ` + gormRunColumns

	gormInsertRetryOutboxSQL = `INSERT INTO ops.git_sync_outbox(
	id,workspace_id,run_id,kind,event_key,available_at,attempt_count,version,created_at,updated_at
) VALUES(gen_random_uuid(),?,?,'INDEX_FOLLOWUP',?,clock_timestamp(),0,1,clock_timestamp(),clock_timestamp())`

	gormInsertRetryReceiptSQL = `INSERT INTO ops.git_sync_index_retry_receipt(
	workspace_id,idempotency_key,request_hash,run_id,result_version,created_at
) VALUES(?,?,?,?,?,clock_timestamp())`
)

// BeginIndexFollowup advances a successful Pull's independent index state to
// RUNNING while fencing the outbox lease.
func (repository *GORMRepository) BeginIndexFollowup(ctx context.Context, lease domain.OutboxLease, expectedVersion int64) (domain.SyncRun, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.SyncRun{}, false, err
	}
	if validateOutboxLease(lease) != nil || lease.Kind != domain.OutboxIndexFollowup || expectedVersion < 0 {
		return domain.SyncRun{}, false, invalid("Git sync index follow-up start is invalid")
	}
	var result domain.SyncRun
	started := false
	err := repository.within(ctx, func(callbackCtx context.Context, tx *gorm.DB) error {
		if err := gormLockWorkspace(callbackCtx, tx, lease.WorkspaceID, workspaceLockPurpose); err != nil {
			return err
		}
		if err := gormRequireActiveIndexLease(callbackCtx, tx, lease); err != nil {
			return err
		}
		row, err := gormRawRow(callbackCtx, tx, gormLoadRunForUpdateSQL, string(lease.WorkspaceID), string(lease.RunID))
		if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		run, err := scanRun(row)
		if gormNoRows(err) {
			return notFound("Git sync run was not found")
		}
		if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		if expectedVersion > 0 && run.Version != expectedVersion {
			return versionConflict(domain.ErrorCodeRunTransition, "Git sync index follow-up version changed")
		}
		if run.Status != domain.RunSucceeded || run.Direction != domain.DirectionPull {
			result = run
			return nil
		}
		if run.IndexStatus == domain.IndexRunning {
			result, started = run, true
			return nil
		}
		if run.IndexStatus != domain.IndexPending {
			result = run
			return nil
		}
		updatedRow, err := gormRawRow(callbackCtx, tx, gormBeginIndexSQL, string(lease.WorkspaceID), string(lease.RunID), run.Version)
		if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		result, err = scanRun(updatedRow)
		if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		started = true
		return nil
	})
	if err != nil {
		return domain.SyncRun{}, false, err
	}
	return result, started, err
}

// CompleteIndexFollowup marks an active index operation successful.
func (repository *GORMRepository) CompleteIndexFollowup(ctx context.Context, lease domain.OutboxLease, expectedVersion int64, indexVersionID foundation.ID, noIndexRequired bool) (domain.SyncRun, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.SyncRun{}, err
	}
	if validateOutboxLease(lease) != nil || lease.Kind != domain.OutboxIndexFollowup || expectedVersion < 1 ||
		(noIndexRequired && indexVersionID != "") || (!noIndexRequired && !validID(indexVersionID)) {
		return domain.SyncRun{}, invalid("Git sync index follow-up completion is invalid")
	}
	var result domain.SyncRun
	err := repository.within(ctx, func(callbackCtx context.Context, tx *gorm.DB) error {
		if err := gormLockWorkspace(callbackCtx, tx, lease.WorkspaceID, workspaceLockPurpose); err != nil {
			return err
		}
		if err := gormRequireActiveIndexLease(callbackCtx, tx, lease); err != nil {
			return err
		}
		row, err := gormRawRow(callbackCtx, tx, gormCompleteIndexSQL, string(indexVersionID), string(lease.WorkspaceID), string(lease.RunID), expectedVersion)
		if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		if result, err = scanRun(row); gormNoRows(err) {
			return versionConflict(domain.ErrorCodeRunTransition, "Git sync index completion lost its CAS")
		} else if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		return nil
	})
	if err != nil {
		return domain.SyncRun{}, err
	}
	return result, err
}

// FailIndexFollowup records an independent index failure without changing Git
// success state.
func (repository *GORMRepository) FailIndexFollowup(ctx context.Context, lease domain.OutboxLease, expectedVersion int64, code string, retryable bool) (domain.SyncRun, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.SyncRun{}, err
	}
	if validateOutboxLease(lease) != nil || lease.Kind != domain.OutboxIndexFollowup || expectedVersion < 1 || !validText(code, 128) {
		return domain.SyncRun{}, invalid("Git sync index follow-up failure is invalid")
	}
	var result domain.SyncRun
	err := repository.within(ctx, func(callbackCtx context.Context, tx *gorm.DB) error {
		if err := gormLockWorkspace(callbackCtx, tx, lease.WorkspaceID, workspaceLockPurpose); err != nil {
			return err
		}
		if err := gormRequireActiveIndexLease(callbackCtx, tx, lease); err != nil {
			return err
		}
		row, err := gormRawRow(callbackCtx, tx, gormFailIndexSQL, code, retryable, string(lease.WorkspaceID), string(lease.RunID), expectedVersion)
		if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		if result, err = scanRun(row); gormNoRows(err) {
			return versionConflict(domain.ErrorCodeRunTransition, "Git sync index failure lost its CAS")
		} else if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		return nil
	})
	if err != nil {
		return domain.SyncRun{}, err
	}
	return result, err
}

func gormRequireActiveIndexLease(ctx context.Context, database *gorm.DB, lease domain.OutboxLease) error {
	row, err := gormRawRow(ctx, database, gormRequireIndexLeaseSQL, string(lease.ID), string(lease.WorkspaceID), string(lease.RunID), lease.Owner, lease.Version)
	if err != nil {
		return classifyGORM(ctx, err, domain.ErrorCodeUnavailable)
	}
	var marker int
	if err := row.Scan(&marker); gormNoRows(err) {
		return leaseLost("Git sync index follow-up lease was lost")
	} else if err != nil {
		return classifyGORM(ctx, err, domain.ErrorCodeUnavailable)
	}
	return nil
}

// RetryIndex requeues a failed index follow-up with an exact command receipt.
func (repository *GORMRepository) RetryIndex(ctx context.Context, command application.RetryIndexCommand) (domain.SyncRun, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.SyncRun{}, false, err
	}
	if !validID(command.WorkspaceID) || !validID(command.RunID) || command.ExpectedVersion < 1 ||
		!validText(command.IdempotencyKey, 128) || !validText(command.RequestHash, 64) {
		return domain.SyncRun{}, false, invalid("Git sync index retry is invalid")
	}
	var result domain.SyncRun
	replayed := false
	err := repository.within(ctx, func(callbackCtx context.Context, tx *gorm.DB) error {
		if err := gormLockWorkspace(callbackCtx, tx, command.WorkspaceID, workspaceLockPurpose); err != nil {
			return err
		}
		receiptRow, err := gormRawRow(callbackCtx, tx, gormRetryReceiptSQL, string(command.WorkspaceID), command.IdempotencyKey)
		if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		var storedHash, storedRun string
		var resultVersion int64
		receiptErr := receiptRow.Scan(&storedHash, &storedRun, &resultVersion)
		if receiptErr == nil {
			if storedHash != command.RequestHash || storedRun != string(command.RunID) {
				return versionConflict(domain.ErrorCodeIdempotencyConflict, "Git sync retry key is bound to another command")
			}
			runRow, loadErr := gormRawRow(callbackCtx, tx, gormGetRunSQL, string(command.WorkspaceID), string(command.RunID))
			if loadErr != nil {
				return classifyGORM(callbackCtx, loadErr, domain.ErrorCodeUnavailable)
			}
			result, loadErr = scanRun(runRow)
			if loadErr != nil {
				return classifyGORM(callbackCtx, loadErr, domain.ErrorCodeUnavailable)
			}
			replayed = true
			return nil
		}
		if !gormNoRows(receiptErr) {
			return classifyGORM(callbackCtx, receiptErr, domain.ErrorCodeUnavailable)
		}
		runRow, err := gormRawRow(callbackCtx, tx, gormLoadRunForUpdateSQL, string(command.WorkspaceID), string(command.RunID))
		if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		run, err := scanRun(runRow)
		if gormNoRows(err) {
			return notFound("Git sync run was not found")
		}
		if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		if run.Version != command.ExpectedVersion || run.Status != domain.RunSucceeded || run.IndexStatus != domain.IndexFailed {
			return versionConflict(domain.ErrorCodeRunTransition, "Git sync index follow-up cannot be retried from its current state")
		}
		pendingRow, err := gormRawRow(callbackCtx, tx, gormPendingIndexOutboxSQL, string(command.WorkspaceID), string(command.RunID))
		if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		var unresolvedOutboxID string
		if pendingErr := pendingRow.Scan(&unresolvedOutboxID); pendingErr == nil {
			return versionConflict(domain.ErrorCodeRunTransition, "previous Git sync index follow-up delivery has not converged")
		} else if !gormNoRows(pendingErr) {
			return classifyGORM(callbackCtx, pendingErr, domain.ErrorCodeUnavailable)
		}
		updatedRow, err := gormRawRow(callbackCtx, tx, gormRetryIndexRunSQL, string(command.WorkspaceID), string(command.RunID), command.ExpectedVersion)
		if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		result, err = scanRun(updatedRow)
		if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		if _, err := gormExec(callbackCtx, tx, gormInsertRetryOutboxSQL, string(command.WorkspaceID), string(command.RunID),
			"git-sync-index:"+string(command.RunID)+":retry:"+command.IdempotencyKey); err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		if _, err := gormExec(callbackCtx, tx, gormInsertRetryReceiptSQL, string(command.WorkspaceID), command.IdempotencyKey,
			command.RequestHash, string(command.RunID), result.Version); err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		return nil
	})
	if err != nil {
		return domain.SyncRun{}, false, err
	}
	return result, replayed, err
}

// ReplayIndex returns an exact follow-up retry result before reading state.
func (repository *GORMRepository) ReplayIndex(ctx context.Context, command application.RetryIndexCommand) (domain.SyncRun, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.SyncRun{}, false, err
	}
	if !validID(command.WorkspaceID) || !validID(command.RunID) || command.ExpectedVersion < 1 ||
		!validText(command.IdempotencyKey, 128) || !validText(command.RequestHash, 64) {
		return domain.SyncRun{}, false, invalid("Git sync index replay query is invalid")
	}
	row, err := gormRawRow(ctx, repository.database, gormRetryReceiptSQL, string(command.WorkspaceID), command.IdempotencyKey)
	if err != nil {
		return domain.SyncRun{}, false, classifyGORM(ctx, err, domain.ErrorCodeUnavailable)
	}
	var storedHash, storedRun string
	if err := row.Scan(&storedHash, &storedRun, new(int64)); gormNoRows(err) {
		return domain.SyncRun{}, false, nil
	} else if err != nil {
		return domain.SyncRun{}, false, classifyGORM(ctx, err, domain.ErrorCodeUnavailable)
	}
	if storedHash != command.RequestHash || storedRun != string(command.RunID) {
		return domain.SyncRun{}, false, versionConflict(domain.ErrorCodeIdempotencyConflict, "Git sync retry key is bound to another command")
	}
	run, err := repository.GetRun(ctx, command.WorkspaceID, command.RunID)
	if err != nil {
		return domain.SyncRun{}, false, err
	}
	return run, true, nil
}
