package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
)

// ClaimNext 以 DB 时钟和 SKIP LOCKED 领取一条 due outbox。
func (repository *Repository) ClaimNext(ctx context.Context, owner string, leaseDuration time.Duration) (domain.OutboxLease, bool, error) {
	if repository == nil || nilInterface(repository.db) || !validText(owner, 256) || leaseDuration < time.Second || leaseDuration > 10*time.Minute {
		return domain.OutboxLease{}, false, invalid("Git sync outbox claim is invalid")
	}
	var id, workspaceID, runID, kind string
	var lease domain.OutboxLease
	err := repository.db.QueryRow(ctx, `WITH candidate AS (
		SELECT id FROM ops.git_sync_outbox
		WHERE published_at IS NULL AND poisoned_at IS NULL AND available_at<=clock_timestamp()
		  AND (lease_expires_at IS NULL OR lease_expires_at<=clock_timestamp())
		ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT 1
	)
	UPDATE ops.git_sync_outbox AS event SET
		lease_owner=$1,lease_expires_at=clock_timestamp()+($2::bigint*interval '1 millisecond'),
		attempt_count=event.attempt_count+1,version=event.version+1,updated_at=clock_timestamp()
	FROM candidate WHERE event.id=candidate.id
	RETURNING event.id::text,event.workspace_id::text,event.run_id::text,event.kind,event.attempt_count,event.version,event.lease_expires_at`,
		owner, leaseDuration.Milliseconds()).Scan(
		&id, &workspaceID, &runID, &kind, &lease.AttemptCount, &lease.Version, &lease.LeaseUntil,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.OutboxLease{}, false, nil
	}
	if err != nil {
		return domain.OutboxLease{}, false, classify(err, domain.ErrorCodeUnavailable)
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

// MarkPublished 完成一个仍被调用方拥有的 outbox lease。
func (repository *Repository) MarkPublished(ctx context.Context, lease domain.OutboxLease) error {
	if err := validateOutboxLease(lease); err != nil {
		return err
	}
	tag, err := repository.db.Exec(ctx, `UPDATE ops.git_sync_outbox SET
		published_at=clock_timestamp(),lease_owner=NULL,lease_expires_at=NULL,
		version=version+1,updated_at=clock_timestamp()
		WHERE id=$1 AND workspace_id=$2 AND lease_owner=$3 AND version=$4
		  AND lease_expires_at>clock_timestamp() AND published_at IS NULL AND poisoned_at IS NULL`,
		string(lease.ID), string(lease.WorkspaceID), lease.Owner, lease.Version)
	if err != nil {
		return classify(err, domain.ErrorCodeUnavailable)
	}
	if tag.RowsAffected() != 1 {
		return leaseLost("Git sync outbox lease was lost")
	}
	return nil
}

// Reschedule 释放一个可重试 lease 并设置下一次可用时间。
func (repository *Repository) Reschedule(ctx context.Context, lease domain.OutboxLease, code string, delay time.Duration) error {
	if validateOutboxLease(lease) != nil || !validText(code, 128) || delay <= 0 || delay > time.Hour {
		return invalid("Git sync outbox retry is invalid")
	}
	tag, err := repository.db.Exec(ctx, `UPDATE ops.git_sync_outbox SET
		available_at=clock_timestamp()+($5::bigint*interval '1 millisecond'),last_error_code=$4,
		lease_owner=NULL,lease_expires_at=NULL,version=version+1,updated_at=clock_timestamp()
		WHERE id=$1 AND workspace_id=$2 AND lease_owner=$3 AND version=$6
		  AND lease_expires_at>clock_timestamp() AND published_at IS NULL AND poisoned_at IS NULL`,
		string(lease.ID), string(lease.WorkspaceID), lease.Owner, code, delay.Milliseconds(), lease.Version)
	if err != nil {
		return classify(err, domain.ErrorCodeUnavailable)
	}
	if tag.RowsAffected() != 1 {
		return leaseLost("Git sync outbox lease was lost")
	}
	return nil
}

// Poison 原子标记不可重试 delivery，并收敛关联 Run 或索引状态。
func (repository *Repository) Poison(ctx context.Context, lease domain.OutboxLease, code string) error {
	if validateOutboxLease(lease) != nil || !validText(code, 128) {
		return invalid("Git sync outbox poison request is invalid")
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return classify(err, domain.ErrorCodeUnavailable)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockWorkspace(ctx, tx, lease.WorkspaceID, workspaceLockPurpose); err != nil {
		return err
	}
	var storedKind, storedRunID string
	err = tx.QueryRow(ctx, `UPDATE ops.git_sync_outbox SET
		poisoned_at=clock_timestamp(),last_error_code=$4,
		lease_owner=NULL,lease_expires_at=NULL,version=version+1,updated_at=clock_timestamp()
		WHERE id=$1 AND workspace_id=$2 AND lease_owner=$3 AND version=$5
		  AND run_id=$6 AND kind=$7 AND published_at IS NULL AND poisoned_at IS NULL
		  AND lease_expires_at>clock_timestamp()
		RETURNING kind,run_id::text`,
		string(lease.ID), string(lease.WorkspaceID), lease.Owner, code, lease.Version,
		string(lease.RunID), string(lease.Kind)).Scan(&storedKind, &storedRunID)
	if errors.Is(err, pgx.ErrNoRows) {
		return leaseLost("Git sync outbox lease was lost")
	}
	if err != nil {
		return classify(err, domain.ErrorCodeUnavailable)
	}
	if storedKind != string(lease.Kind) || storedRunID != string(lease.RunID) {
		return corrupt("Git sync outbox binding changed")
	}
	switch lease.Kind {
	case domain.OutboxExecuteRun:
		if _, err := tx.Exec(ctx, `UPDATE ops.git_sync_run SET
			status=CASE WHEN status IN ('FAST_FORWARDING','PUSHING','VERIFYING')
				THEN 'MANUAL_RECOVERY_REQUIRED' ELSE 'FAILED' END,
			failure_class=CASE WHEN status IN ('FAST_FORWARDING','PUSHING','VERIFYING')
				THEN 'RESULT_UNKNOWN' ELSE 'INTERNAL' END,
			error_code=CASE WHEN status IN ('FAST_FORWARDING','PUSHING','VERIFYING') THEN $3 ELSE $4 END,
			retryable=false,index_status='NOT_REQUIRED',index_error_code='',index_retryable=false,index_version_id=NULL,
			completed_at=clock_timestamp(),version=version+1,updated_at=clock_timestamp()
			WHERE workspace_id=$1 AND id=$2
			  AND status IN ('PENDING','FETCHING','COMPARING','FAST_FORWARDING','PUSHING','VERIFYING')`,
			string(lease.WorkspaceID), string(lease.RunID), domain.ErrorCodeResultUnknown, code); err != nil {
			return classify(err, domain.ErrorCodeUnavailable)
		}
		if _, err := tx.Exec(ctx, `UPDATE ops.git_sync_attempt SET
			status='FAILED',
			phase=CASE WHEN phase IN ('FAST_FORWARDING','PUSHING','VERIFYING')
				THEN 'MANUAL_RECOVERY_REQUIRED' ELSE 'FAILED' END,
			error_code=CASE WHEN phase IN ('FAST_FORWARDING','PUSHING','VERIFYING') THEN $3 ELSE $4 END,
			retryable=false,lease_owner=NULL,lease_expires_at=NULL,
			completed_at=clock_timestamp(),version=version+1
			WHERE workspace_id=$1 AND run_id=$2 AND status='RUNNING'`,
			string(lease.WorkspaceID), string(lease.RunID), domain.ErrorCodeResultUnknown, code); err != nil {
			return classify(err, domain.ErrorCodeUnavailable)
		}
	case domain.OutboxIndexFollowup:
		if _, err := tx.Exec(ctx, `UPDATE ops.git_sync_run SET
			index_status='FAILED',index_error_code=$3,index_retryable=false,index_version_id=NULL,
			version=version+1,updated_at=clock_timestamp()
			WHERE workspace_id=$1 AND id=$2 AND status='SUCCEEDED' AND direction='PULL'
			  AND index_status IN ('PENDING','RUNNING')`,
			string(lease.WorkspaceID), string(lease.RunID), code); err != nil {
			return classify(err, domain.ErrorCodeUnavailable)
		}
	default:
		return corrupt("Git sync outbox kind is invalid")
	}
	return commit(ctx, tx)
}

func validateOutboxLease(lease domain.OutboxLease) error {
	if !validID(lease.ID) || !validID(lease.WorkspaceID) || !validID(lease.RunID) || !validText(lease.Owner, 256) ||
		(lease.Kind != domain.OutboxExecuteRun && lease.Kind != domain.OutboxIndexFollowup) ||
		lease.AttemptCount < 1 || lease.Version < 2 || lease.LeaseUntil.IsZero() {
		return invalid("Git sync outbox lease is invalid")
	}
	return nil
}
