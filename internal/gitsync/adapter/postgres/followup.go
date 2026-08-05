package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
)

// BeginIndexFollowup 将成功 Pull 的独立索引状态推进到 RUNNING。
func (repository *Repository) BeginIndexFollowup(ctx context.Context, lease domain.OutboxLease, expectedVersion int64) (domain.SyncRun, bool, error) {
	if repository == nil || nilInterface(repository.db) || validateOutboxLease(lease) != nil ||
		lease.Kind != domain.OutboxIndexFollowup || expectedVersion < 0 {
		return domain.SyncRun{}, false, invalid("Git sync index follow-up start is invalid")
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.SyncRun{}, false, classify(err, domain.ErrorCodeUnavailable)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockWorkspace(ctx, tx, lease.WorkspaceID, workspaceLockPurpose); err != nil {
		return domain.SyncRun{}, false, err
	}
	if err := requireActiveIndexLease(ctx, tx, lease); err != nil {
		return domain.SyncRun{}, false, err
	}
	run, err := scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+`
		FROM ops.git_sync_run WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, string(lease.WorkspaceID), string(lease.RunID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SyncRun{}, false, notFound("Git sync run was not found")
	}
	if err != nil {
		return domain.SyncRun{}, false, classify(err, domain.ErrorCodeUnavailable)
	}
	if expectedVersion > 0 && run.Version != expectedVersion {
		return domain.SyncRun{}, false, versionConflict(domain.ErrorCodeRunTransition, "Git sync index follow-up version changed")
	}
	if run.Status != domain.RunSucceeded || run.Direction != domain.DirectionPull {
		return run, false, nil
	}
	if run.IndexStatus == domain.IndexRunning {
		if err := commit(ctx, tx); err != nil {
			return domain.SyncRun{}, false, err
		}
		return run, true, nil
	}
	if run.IndexStatus != domain.IndexPending {
		return run, false, nil
	}
	run, err = scanRun(tx.QueryRow(ctx, `UPDATE ops.git_sync_run SET
		index_status='RUNNING',index_error_code='',index_retryable=false,
		version=version+1,updated_at=clock_timestamp()
		WHERE workspace_id=$1 AND id=$2 AND version=$3 AND index_status='PENDING'
		RETURNING `+runColumns, string(lease.WorkspaceID), string(lease.RunID), run.Version))
	if err != nil {
		return domain.SyncRun{}, false, classify(err, domain.ErrorCodeUnavailable)
	}
	if err := commit(ctx, tx); err != nil {
		return domain.SyncRun{}, false, err
	}
	return run, true, nil
}

// CompleteIndexFollowup 只完成 IndexStatus，并显式支持无需新索引版本的成功。
func (repository *Repository) CompleteIndexFollowup(ctx context.Context, lease domain.OutboxLease, expectedVersion int64, indexVersionID foundation.ID, noIndexRequired bool) (domain.SyncRun, error) {
	if repository == nil || nilInterface(repository.db) || validateOutboxLease(lease) != nil || lease.Kind != domain.OutboxIndexFollowup ||
		expectedVersion < 1 || (noIndexRequired && indexVersionID != "") || (!noIndexRequired && !validID(indexVersionID)) {
		return domain.SyncRun{}, invalid("Git sync index follow-up completion is invalid")
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.SyncRun{}, classify(err, domain.ErrorCodeUnavailable)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockWorkspace(ctx, tx, lease.WorkspaceID, workspaceLockPurpose); err != nil {
		return domain.SyncRun{}, err
	}
	if err := requireActiveIndexLease(ctx, tx, lease); err != nil {
		return domain.SyncRun{}, err
	}
	run, err := scanRun(tx.QueryRow(ctx, `UPDATE ops.git_sync_run SET
		index_status='SUCCEEDED',index_error_code='',index_retryable=false,index_version_id=NULLIF($4,'')::uuid,
		version=version+1,updated_at=clock_timestamp()
		WHERE workspace_id=$1 AND id=$2 AND version=$3 AND status='SUCCEEDED' AND index_status='RUNNING'
		RETURNING `+runColumns, string(lease.WorkspaceID), string(lease.RunID), expectedVersion, string(indexVersionID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SyncRun{}, versionConflict(domain.ErrorCodeRunTransition, "Git sync index completion lost its CAS")
	}
	if err != nil {
		return domain.SyncRun{}, classify(err, domain.ErrorCodeUnavailable)
	}
	if err := commit(ctx, tx); err != nil {
		return domain.SyncRun{}, err
	}
	return run, nil
}

// FailIndexFollowup 记录独立索引失败，不回滚或降级 Git 状态。
func (repository *Repository) FailIndexFollowup(ctx context.Context, lease domain.OutboxLease, expectedVersion int64, code string, retryable bool) (domain.SyncRun, error) {
	if repository == nil || nilInterface(repository.db) || validateOutboxLease(lease) != nil || lease.Kind != domain.OutboxIndexFollowup ||
		expectedVersion < 1 || !validText(code, 128) {
		return domain.SyncRun{}, invalid("Git sync index follow-up failure is invalid")
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.SyncRun{}, classify(err, domain.ErrorCodeUnavailable)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockWorkspace(ctx, tx, lease.WorkspaceID, workspaceLockPurpose); err != nil {
		return domain.SyncRun{}, err
	}
	if err := requireActiveIndexLease(ctx, tx, lease); err != nil {
		return domain.SyncRun{}, err
	}
	run, err := scanRun(tx.QueryRow(ctx, `UPDATE ops.git_sync_run SET
		index_status='FAILED',index_error_code=$4,index_retryable=$5,index_version_id=NULL,
		version=version+1,updated_at=clock_timestamp()
		WHERE workspace_id=$1 AND id=$2 AND version=$3 AND status='SUCCEEDED' AND index_status='RUNNING'
		RETURNING `+runColumns, string(lease.WorkspaceID), string(lease.RunID), expectedVersion, code, retryable))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SyncRun{}, versionConflict(domain.ErrorCodeRunTransition, "Git sync index failure lost its CAS")
	}
	if err != nil {
		return domain.SyncRun{}, classify(err, domain.ErrorCodeUnavailable)
	}
	if err := commit(ctx, tx); err != nil {
		return domain.SyncRun{}, err
	}
	return run, nil
}

func requireActiveIndexLease(ctx context.Context, tx pgx.Tx, lease domain.OutboxLease) error {
	var marker int
	err := tx.QueryRow(ctx, `SELECT 1 FROM ops.git_sync_outbox
		WHERE id=$1 AND workspace_id=$2 AND run_id=$3 AND kind='INDEX_FOLLOWUP'
		  AND lease_owner=$4 AND version=$5 AND lease_expires_at>clock_timestamp()
		  AND published_at IS NULL AND poisoned_at IS NULL
		FOR UPDATE`, string(lease.ID), string(lease.WorkspaceID), string(lease.RunID), lease.Owner, lease.Version).Scan(&marker)
	if errors.Is(err, pgx.ErrNoRows) {
		return leaseLost("Git sync index follow-up lease was lost")
	}
	if err != nil {
		return classify(err, domain.ErrorCodeUnavailable)
	}
	return nil
}

// RetryIndex 幂等地将 FAILED follow-up 重新排队。
func (repository *Repository) RetryIndex(ctx context.Context, command application.RetryIndexCommand) (domain.SyncRun, bool, error) {
	if repository == nil || nilInterface(repository.db) || !validID(command.WorkspaceID) || !validID(command.RunID) ||
		command.ExpectedVersion < 1 || !validText(command.IdempotencyKey, 128) || !validText(command.RequestHash, 64) {
		return domain.SyncRun{}, false, invalid("Git sync index retry is invalid")
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.SyncRun{}, false, classify(err, domain.ErrorCodeUnavailable)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockWorkspace(ctx, tx, command.WorkspaceID, workspaceLockPurpose); err != nil {
		return domain.SyncRun{}, false, err
	}
	var storedHash, storedRun string
	var resultVersion int64
	err = tx.QueryRow(ctx, `SELECT request_hash,run_id::text,result_version
		FROM ops.git_sync_index_retry_receipt WHERE workspace_id=$1 AND idempotency_key=$2`,
		string(command.WorkspaceID), command.IdempotencyKey).Scan(&storedHash, &storedRun, &resultVersion)
	if err == nil {
		if storedHash != command.RequestHash || storedRun != string(command.RunID) {
			return domain.SyncRun{}, false, versionConflict(domain.ErrorCodeIdempotencyConflict, "Git sync retry key is bound to another command")
		}
		run, loadErr := scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+`
			FROM ops.git_sync_run WHERE workspace_id=$1 AND id=$2`, string(command.WorkspaceID), string(command.RunID)))
		if loadErr != nil {
			return domain.SyncRun{}, false, classify(loadErr, domain.ErrorCodeUnavailable)
		}
		return run, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.SyncRun{}, false, classify(err, domain.ErrorCodeUnavailable)
	}
	run, err := scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+`
		FROM ops.git_sync_run WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, string(command.WorkspaceID), string(command.RunID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SyncRun{}, false, notFound("Git sync run was not found")
	}
	if err != nil {
		return domain.SyncRun{}, false, classify(err, domain.ErrorCodeUnavailable)
	}
	if run.Version != command.ExpectedVersion || run.Status != domain.RunSucceeded || run.IndexStatus != domain.IndexFailed {
		return domain.SyncRun{}, false, versionConflict(domain.ErrorCodeRunTransition, "Git sync index follow-up cannot be retried from its current state")
	}
	var unresolvedOutboxID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM ops.git_sync_outbox
		WHERE workspace_id=$1 AND run_id=$2 AND kind='INDEX_FOLLOWUP'
		  AND published_at IS NULL AND poisoned_at IS NULL
		ORDER BY created_at,id LIMIT 1 FOR UPDATE`, string(command.WorkspaceID), string(command.RunID)).Scan(&unresolvedOutboxID)
	if err == nil {
		return domain.SyncRun{}, false, versionConflict(domain.ErrorCodeRunTransition, "previous Git sync index follow-up delivery has not converged")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.SyncRun{}, false, classify(err, domain.ErrorCodeUnavailable)
	}
	run, err = scanRun(tx.QueryRow(ctx, `UPDATE ops.git_sync_run SET
		index_status='PENDING',index_error_code='',index_retryable=false,index_version_id=NULL,
		version=version+1,updated_at=clock_timestamp()
		WHERE workspace_id=$1 AND id=$2 AND version=$3 RETURNING `+runColumns,
		string(command.WorkspaceID), string(command.RunID), command.ExpectedVersion))
	if err != nil {
		return domain.SyncRun{}, false, classify(err, domain.ErrorCodeUnavailable)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ops.git_sync_outbox(
		id,workspace_id,run_id,kind,event_key,available_at,attempt_count,version,created_at,updated_at
	) VALUES(gen_random_uuid(),$1,$2,'INDEX_FOLLOWUP',$3,clock_timestamp(),0,1,clock_timestamp(),clock_timestamp())`,
		string(command.WorkspaceID), string(command.RunID), "git-sync-index:"+string(command.RunID)+":retry:"+command.IdempotencyKey); err != nil {
		return domain.SyncRun{}, false, classify(err, domain.ErrorCodeUnavailable)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ops.git_sync_index_retry_receipt(
		workspace_id,idempotency_key,request_hash,run_id,result_version,created_at
	) VALUES($1,$2,$3,$4,$5,clock_timestamp())`,
		string(command.WorkspaceID), command.IdempotencyKey, command.RequestHash, string(command.RunID), run.Version); err != nil {
		return domain.SyncRun{}, false, classify(err, domain.ErrorCodeUnavailable)
	}
	if err := commit(ctx, tx); err != nil {
		return domain.SyncRun{}, false, err
	}
	return run, false, nil
}

// ReplayIndex 在检查当前索引状态前返回精确匹配的 follow-up 重试结果。
func (repository *Repository) ReplayIndex(ctx context.Context, command application.RetryIndexCommand) (domain.SyncRun, bool, error) {
	if repository == nil || nilInterface(repository.db) || !validID(command.WorkspaceID) || !validID(command.RunID) ||
		command.ExpectedVersion < 1 || !validText(command.IdempotencyKey, 128) || !validText(command.RequestHash, 64) {
		return domain.SyncRun{}, false, invalid("Git sync index replay query is invalid")
	}
	var storedHash, storedRun string
	err := repository.db.QueryRow(ctx, `SELECT request_hash,run_id::text
		FROM ops.git_sync_index_retry_receipt WHERE workspace_id=$1 AND idempotency_key=$2`,
		string(command.WorkspaceID), command.IdempotencyKey).Scan(&storedHash, &storedRun)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SyncRun{}, false, nil
	}
	if err != nil {
		return domain.SyncRun{}, false, classify(err, domain.ErrorCodeUnavailable)
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
