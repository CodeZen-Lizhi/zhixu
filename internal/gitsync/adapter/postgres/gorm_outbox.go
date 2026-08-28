package postgres

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
)

const (
	gormMarkPublishedSQL = `UPDATE ops.git_sync_outbox SET
	published_at=clock_timestamp(),lease_owner=NULL,lease_expires_at=NULL,
	version=version+1,updated_at=clock_timestamp()
	WHERE id=? AND workspace_id=? AND lease_owner=? AND version=?
	  AND lease_expires_at>clock_timestamp() AND published_at IS NULL AND poisoned_at IS NULL`

	gormRescheduleOutboxSQL = `UPDATE ops.git_sync_outbox SET
	available_at=clock_timestamp()+(?::bigint*interval '1 millisecond'),last_error_code=?,
	lease_owner=NULL,lease_expires_at=NULL,version=version+1,updated_at=clock_timestamp()
	WHERE id=? AND workspace_id=? AND lease_owner=? AND version=?
	  AND lease_expires_at>clock_timestamp() AND published_at IS NULL AND poisoned_at IS NULL`

	gormPoisonOutboxSQL = `UPDATE ops.git_sync_outbox SET
	poisoned_at=clock_timestamp(),last_error_code=?,
	lease_owner=NULL,lease_expires_at=NULL,version=version+1,updated_at=clock_timestamp()
	WHERE id=? AND workspace_id=? AND lease_owner=? AND version=?
	  AND run_id=? AND kind=? AND published_at IS NULL AND poisoned_at IS NULL
	  AND lease_expires_at>clock_timestamp()
	RETURNING kind,run_id::text`

	gormPoisonRunSQL = `UPDATE ops.git_sync_run SET
	status=CASE WHEN status IN ('FAST_FORWARDING','PUSHING','VERIFYING')
		THEN 'MANUAL_RECOVERY_REQUIRED' ELSE 'FAILED' END,
	failure_class=CASE WHEN status IN ('FAST_FORWARDING','PUSHING','VERIFYING')
		THEN 'RESULT_UNKNOWN' ELSE 'INTERNAL' END,
	error_code=CASE WHEN status IN ('FAST_FORWARDING','PUSHING','VERIFYING') THEN ? ELSE ? END,
	retryable=false,index_status='NOT_REQUIRED',index_error_code='',index_retryable=false,index_version_id=NULL,
	completed_at=clock_timestamp(),version=version+1,updated_at=clock_timestamp()
	WHERE workspace_id=? AND id=?
	  AND status IN ('PENDING','FETCHING','COMPARING','FAST_FORWARDING','PUSHING','VERIFYING')`

	gormPoisonAttemptSQL = `UPDATE ops.git_sync_attempt SET
	status='FAILED',
	phase=CASE WHEN phase IN ('FAST_FORWARDING','PUSHING','VERIFYING')
		THEN 'MANUAL_RECOVERY_REQUIRED' ELSE 'FAILED' END,
	error_code=CASE WHEN phase IN ('FAST_FORWARDING','PUSHING','VERIFYING') THEN ? ELSE ? END,
	retryable=false,lease_owner=NULL,lease_expires_at=NULL,
	completed_at=clock_timestamp(),version=version+1
	WHERE workspace_id=? AND run_id=? AND status='RUNNING'`

	gormPoisonIndexSQL = `UPDATE ops.git_sync_run SET
	index_status='FAILED',index_error_code=?,index_retryable=false,index_version_id=NULL,
	version=version+1,updated_at=clock_timestamp()
	WHERE workspace_id=? AND id=? AND status='SUCCEEDED' AND direction='PULL'
	  AND index_status IN ('PENDING','RUNNING')`
)

// ClaimNext claims one due outbox row using database time and SKIP LOCKED.
func (repository *GORMRepository) ClaimNext(ctx context.Context, owner string, leaseDuration time.Duration) (domain.OutboxLease, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.OutboxLease{}, false, err
	}
	if !validText(owner, 256) || leaseDuration < time.Second || leaseDuration > 10*time.Minute {
		return domain.OutboxLease{}, false, invalid("Git sync outbox claim is invalid")
	}
	row, err := gormRawRow(ctx, repository.database, gormOutboxClaimSQL, owner, leaseDuration.Milliseconds())
	if err != nil {
		return domain.OutboxLease{}, false, classifyGORM(ctx, err, domain.ErrorCodeUnavailable)
	}
	var id, workspaceID, runID, kind string
	var lease domain.OutboxLease
	if err := row.Scan(&id, &workspaceID, &runID, &kind, &lease.AttemptCount, &lease.Version, &lease.LeaseUntil); gormNoRows(err) {
		return domain.OutboxLease{}, false, nil
	} else if err != nil {
		return domain.OutboxLease{}, false, classifyGORM(ctx, err, domain.ErrorCodeUnavailable)
	}
	parsedID, idErr := foundation.ParseID(id)
	parsedWorkspace, workspaceErr := foundation.ParseID(workspaceID)
	parsedRun, runErr := foundation.ParseID(runID)
	if idErr != nil || workspaceErr != nil || runErr != nil {
		return domain.OutboxLease{}, false, corrupt("Git sync outbox identity is invalid")
	}
	lease.ID, lease.WorkspaceID, lease.RunID = parsedID, parsedWorkspace, parsedRun
	lease.Kind, lease.Owner, lease.LeaseUntil = domain.OutboxKind(kind), owner, lease.LeaseUntil.UTC()
	if lease.Kind != domain.OutboxExecuteRun && lease.Kind != domain.OutboxIndexFollowup {
		return domain.OutboxLease{}, false, corrupt("Git sync outbox kind is invalid")
	}
	return lease, true, nil
}

// MarkPublished completes a still-owned outbox lease.
func (repository *GORMRepository) MarkPublished(ctx context.Context, lease domain.OutboxLease) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	if err := validateOutboxLease(lease); err != nil {
		return err
	}
	rowsAffected, err := gormExec(ctx, repository.database, gormMarkPublishedSQL, string(lease.ID), string(lease.WorkspaceID), lease.Owner, lease.Version)
	if err != nil {
		return classifyGORM(ctx, err, domain.ErrorCodeUnavailable)
	}
	if rowsAffected != 1 {
		return leaseLost("Git sync outbox lease was lost")
	}
	return nil
}

// Reschedule releases a retryable lease and sets its next database-time due.
func (repository *GORMRepository) Reschedule(ctx context.Context, lease domain.OutboxLease, code string, delay time.Duration) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	if validateOutboxLease(lease) != nil || !validText(code, 128) || delay <= 0 || delay > time.Hour {
		return invalid("Git sync outbox retry is invalid")
	}
	rowsAffected, err := gormExec(ctx, repository.database, gormRescheduleOutboxSQL, delay.Milliseconds(), code,
		string(lease.ID), string(lease.WorkspaceID), lease.Owner, lease.Version)
	if err != nil {
		return classifyGORM(ctx, err, domain.ErrorCodeUnavailable)
	}
	if rowsAffected != 1 {
		return leaseLost("Git sync outbox lease was lost")
	}
	return nil
}

// Poison atomically closes an undeliverable outbox row and converges its
// associated Run/Attempt or independent index status.
func (repository *GORMRepository) Poison(ctx context.Context, lease domain.OutboxLease, code string) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	if validateOutboxLease(lease) != nil || !validText(code, 128) {
		return invalid("Git sync outbox poison request is invalid")
	}
	return repository.within(ctx, func(callbackCtx context.Context, tx *gorm.DB) error {
		if err := gormLockWorkspace(callbackCtx, tx, lease.WorkspaceID, workspaceLockPurpose); err != nil {
			return err
		}
		row, err := gormRawRow(callbackCtx, tx, gormPoisonOutboxSQL, code, string(lease.ID), string(lease.WorkspaceID), lease.Owner,
			lease.Version, string(lease.RunID), string(lease.Kind))
		if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		var storedKind, storedRunID string
		if err := row.Scan(&storedKind, &storedRunID); gormNoRows(err) {
			return leaseLost("Git sync outbox lease was lost")
		} else if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		if storedKind != string(lease.Kind) || storedRunID != string(lease.RunID) {
			return corrupt("Git sync outbox binding changed")
		}
		switch lease.Kind {
		case domain.OutboxExecuteRun:
			if _, err := gormExec(callbackCtx, tx, gormPoisonRunSQL, domain.ErrorCodeResultUnknown, code, string(lease.WorkspaceID), string(lease.RunID)); err != nil {
				return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
			}
			if _, err := gormExec(callbackCtx, tx, gormPoisonAttemptSQL, domain.ErrorCodeResultUnknown, code, string(lease.WorkspaceID), string(lease.RunID)); err != nil {
				return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
			}
		case domain.OutboxIndexFollowup:
			if _, err := gormExec(callbackCtx, tx, gormPoisonIndexSQL, code, string(lease.WorkspaceID), string(lease.RunID)); err != nil {
				return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
			}
		default:
			return corrupt("Git sync outbox kind is invalid")
		}
		return nil
	})
}
