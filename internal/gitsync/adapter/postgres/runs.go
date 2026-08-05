package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
)

// CreateRun 原子创建 Run 与执行 Outbox；精确重放先于活动运行检查。
func (repository *Repository) CreateRun(ctx context.Context, record application.CreateRunRecord) (domain.SyncRun, bool, error) {
	run := record.Run
	if repository == nil || nilInterface(repository.db) || run.Validate() != nil || run.Status != domain.RunPending {
		return domain.SyncRun{}, false, invalid("Git sync run creation is invalid")
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.SyncRun{}, false, classify(err, domain.ErrorCodeUnavailable)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockWorkspace(ctx, tx, run.WorkspaceID, workspaceLockPurpose); err != nil {
		return domain.SyncRun{}, false, err
	}
	existing, found, err := findRunByCommand(ctx, tx, run.WorkspaceID, run.IdempotencyKey)
	if err != nil {
		return domain.SyncRun{}, false, err
	}
	if found {
		if existing.RequestHash != run.RequestHash || existing.Trigger != run.Trigger || existing.RetryOfRunID != run.RetryOfRunID {
			return domain.SyncRun{}, false, versionConflict(domain.ErrorCodeIdempotencyConflict, "Git sync idempotency key is bound to another run")
		}
		return existing, true, nil
	}
	var configured, autoSync, tokenConfigured bool
	var revision int64
	var remoteURL, branch string
	err = tx.QueryRow(ctx, `SELECT configured,auto_sync,token_configured,revision,COALESCE(normalized_url,''),COALESCE(branch,'')
		FROM ops.git_remote_config WHERE workspace_id=$1 FOR SHARE`, string(run.WorkspaceID)).
		Scan(&configured, &autoSync, &tokenConfigured, &revision, &remoteURL, &branch)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !configured) {
		return domain.SyncRun{}, false, foundation.NewError(foundation.ErrorNotFound, domain.ErrorCodeConfigNotFound, false, errors.New("Git remote is not configured"))
	}
	if err != nil {
		return domain.SyncRun{}, false, classify(err, domain.ErrorCodeUnavailable)
	}
	if !tokenConfigured {
		return domain.SyncRun{}, false, foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeSecretUnavailable, false, errors.New("Git remote token is not configured"))
	}
	if run.Trigger == domain.TriggerAutomatic && !autoSync {
		return domain.SyncRun{}, false, versionConflict(domain.ErrorCodeAutoSyncDisabled, "automatic Git sync is disabled")
	}
	if revision != run.ConfigRevision || remoteURL != run.RemoteURL || branch != run.Branch {
		return domain.SyncRun{}, false, versionConflict(domain.ErrorCodeConfigStale, "Git remote configuration changed before run creation")
	}
	changed, err := marshalChanges(run.ChangedFiles)
	if err != nil {
		return domain.SyncRun{}, false, err
	}
	inserted, err := scanRun(tx.QueryRow(ctx, `INSERT INTO ops.git_sync_run(
		id,workspace_id,config_revision,remote_url,branch,trigger,retry_of_run_id,idempotency_key,request_hash,
		status,direction,failure_class,error_code,retryable,expected_head_oid,expected_remote_oid,
		verified_head_oid,verified_remote_oid,changed_files,index_status,index_error_code,index_retryable,
		attempt_count,version,created_at,updated_at,completed_at
	) VALUES($1,$2,$3,$4,$5,$6,NULLIF($7,'')::uuid,$8,$9,'PENDING','UNKNOWN','NONE','',false,
		NULL,NULL,NULL,NULL,$10,'NOT_REQUIRED','',false,0,1,$11,$11,NULL)
	RETURNING `+runColumns,
		string(run.ID), string(run.WorkspaceID), run.ConfigRevision, run.RemoteURL, run.Branch, string(run.Trigger),
		string(run.RetryOfRunID), run.IdempotencyKey, run.RequestHash, changed, run.CreatedAt.UTC()))
	if err != nil {
		return domain.SyncRun{}, false, classify(err, domain.ErrorCodeUnavailable)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ops.git_sync_outbox(
		id,workspace_id,run_id,kind,event_key,available_at,attempt_count,version,created_at,updated_at
	) VALUES(gen_random_uuid(),$1,$2,'EXECUTE_RUN',$3,clock_timestamp(),0,1,clock_timestamp(),clock_timestamp())`,
		string(run.WorkspaceID), string(run.ID), "git-sync-run:"+string(run.ID)); err != nil {
		return domain.SyncRun{}, false, classify(err, domain.ErrorCodeUnavailable)
	}
	if err := commit(ctx, tx); err != nil {
		return domain.SyncRun{}, false, err
	}
	return inserted, false, nil
}

// ReplayRun 在读取当前配置或分配新 ID 前返回精确匹配的运行。
func (repository *Repository) ReplayRun(ctx context.Context, command application.ReplayRunCommand) (domain.SyncRun, bool, error) {
	if repository == nil || nilInterface(repository.db) || !validID(command.WorkspaceID) ||
		!validText(command.IdempotencyKey, 128) || !validText(command.RequestHash, 64) {
		return domain.SyncRun{}, false, invalid("Git sync run replay query is invalid")
	}
	existing, found, err := findRunByCommand(ctx, repository.db, command.WorkspaceID, command.IdempotencyKey)
	if err != nil || !found {
		return existing, found, err
	}
	if existing.RequestHash != command.RequestHash || existing.Trigger != command.Trigger || existing.RetryOfRunID != command.RetryOfRunID {
		return domain.SyncRun{}, false, versionConflict(domain.ErrorCodeIdempotencyConflict, "Git sync idempotency key is bound to another run")
	}
	return existing, true, nil
}

// GetRun 返回一个 Workspace-scoped Run。
func (repository *Repository) GetRun(ctx context.Context, workspaceID, runID foundation.ID) (domain.SyncRun, error) {
	if repository == nil || nilInterface(repository.db) || !validID(workspaceID) || !validID(runID) {
		return domain.SyncRun{}, invalid("Git sync run query is invalid")
	}
	run, err := scanRun(repository.db.QueryRow(ctx, `SELECT `+runColumns+`
		FROM ops.git_sync_run WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(runID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SyncRun{}, notFound("Git sync run was not found")
	}
	if err != nil {
		return domain.SyncRun{}, classify(err, domain.ErrorCodeUnavailable)
	}
	return run, nil
}

// GetCurrentRun 返回最近创建的 Run，包含当前活动或最近终态。
func (repository *Repository) GetCurrentRun(ctx context.Context, workspaceID foundation.ID) (domain.SyncRun, bool, error) {
	if repository == nil || nilInterface(repository.db) || !validID(workspaceID) {
		return domain.SyncRun{}, false, invalid("Git sync current run query is invalid")
	}
	run, err := scanRun(repository.db.QueryRow(ctx, `SELECT `+runColumns+`
		FROM ops.git_sync_run WHERE workspace_id=$1 ORDER BY created_at DESC,id DESC LIMIT 1`, string(workspaceID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SyncRun{}, false, nil
	}
	if err != nil {
		return domain.SyncRun{}, false, classify(err, domain.ErrorCodeUnavailable)
	}
	return run, true, nil
}

// ListRuns 执行有界 `(created_at,id)` keyset 查询。
func (repository *Repository) ListRuns(ctx context.Context, query application.ListRunsQuery) (application.RunPage, error) {
	if repository == nil || nilInterface(repository.db) || !validID(query.WorkspaceID) || query.Limit < 1 || query.Limit > domain.MaxListLimit ||
		(query.BeforeTime.IsZero() != (query.BeforeID == "")) || (!query.BeforeTime.IsZero() && !validID(query.BeforeID)) {
		return application.RunPage{}, invalid("Git sync run list query is invalid")
	}
	rows, err := repository.db.Query(ctx, `SELECT `+runColumns+`
		FROM ops.git_sync_run
		WHERE workspace_id=$1 AND ($2::timestamptz IS NULL OR (created_at,id)<($2,$3::uuid))
		ORDER BY created_at DESC,id DESC LIMIT $4`,
		string(query.WorkspaceID), nullableTime(query.BeforeTime), nullableID(query.BeforeID), query.Limit+1)
	if err != nil {
		return application.RunPage{}, classify(err, domain.ErrorCodeUnavailable)
	}
	defer rows.Close()
	items := make([]domain.SyncRun, 0, query.Limit+1)
	for rows.Next() {
		run, scanErr := scanRun(rows)
		if scanErr != nil {
			return application.RunPage{}, classify(scanErr, domain.ErrorCodeUnavailable)
		}
		items = append(items, run)
	}
	if err := rows.Err(); err != nil {
		return application.RunPage{}, classify(err, domain.ErrorCodeUnavailable)
	}
	page := application.RunPage{Items: items}
	if len(items) > query.Limit {
		page.Items = items[:query.Limit]
		last := page.Items[len(page.Items)-1]
		page.NextTime, page.NextID = last.CreatedAt, last.ID
	}
	if page.Items == nil {
		page.Items = []domain.SyncRun{}
	}
	return page, nil
}

// BeginAttempt 创建新 Attempt，或在租约过期后接管同一持久 Attempt。
func (repository *Repository) BeginAttempt(ctx context.Context, command application.BeginAttemptCommand) (application.BeginAttemptResult, error) {
	if repository == nil || nilInterface(repository.db) || !validID(command.WorkspaceID) || !validID(command.RunID) ||
		!validID(command.AttemptID) || !validText(command.Owner, 256) || command.LeaseDuration < time.Second || command.LeaseDuration > 10*time.Minute {
		return application.BeginAttemptResult{}, invalid("Git sync attempt claim is invalid")
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return application.BeginAttemptResult{}, classify(err, domain.ErrorCodeUnavailable)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockWorkspace(ctx, tx, command.WorkspaceID, workspaceLockPurpose); err != nil {
		return application.BeginAttemptResult{}, err
	}
	run, err := scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+`
		FROM ops.git_sync_run WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, string(command.WorkspaceID), string(command.RunID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.BeginAttemptResult{}, notFound("Git sync run was not found")
	}
	if err != nil {
		return application.BeginAttemptResult{}, classify(err, domain.ErrorCodeUnavailable)
	}
	if run.Status.Terminal() {
		return application.BeginAttemptResult{Run: run}, nil
	}
	if run.Status == domain.RunPending {
		var configured, tokenConfigured bool
		var revision int64
		err := tx.QueryRow(ctx, `SELECT configured,token_configured,revision FROM ops.git_remote_config
			WHERE workspace_id=$1 FOR SHARE`, string(command.WorkspaceID)).Scan(&configured, &tokenConfigured, &revision)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!configured || !tokenConfigured || revision != run.ConfigRevision)) {
			run, err = scanRun(tx.QueryRow(ctx, `UPDATE ops.git_sync_run SET
				status='STALE',failure_class='STALE_CONFIG',error_code=$3,retryable=false,
				completed_at=clock_timestamp(),version=version+1,updated_at=clock_timestamp()
				WHERE workspace_id=$1 AND id=$2 AND version=$4 RETURNING `+runColumns,
				string(command.WorkspaceID), string(command.RunID), domain.ErrorCodeConfigStale, run.Version))
			if err != nil {
				return application.BeginAttemptResult{}, classify(err, domain.ErrorCodeUnavailable)
			}
			if err := commit(ctx, tx); err != nil {
				return application.BeginAttemptResult{}, err
			}
			return application.BeginAttemptResult{Run: run}, nil
		}
		if err != nil {
			return application.BeginAttemptResult{}, classify(err, domain.ErrorCodeUnavailable)
		}
		attemptNo := run.AttemptCount + 1
		attempt, err := scanAttempt(tx.QueryRow(ctx, `INSERT INTO ops.git_sync_attempt(
			id,workspace_id,run_id,attempt_no,status,phase,error_code,retryable,
			lease_owner,lease_expires_at,started_at,version
		) VALUES($1,$2,$3,$4,'RUNNING','FETCHING','',false,$5,
			clock_timestamp()+($6::bigint*interval '1 millisecond'),clock_timestamp(),1)
		RETURNING `+attemptColumns,
			string(command.AttemptID), string(command.WorkspaceID), string(command.RunID), attemptNo,
			command.Owner, command.LeaseDuration.Milliseconds()))
		if err != nil {
			return application.BeginAttemptResult{}, classify(err, domain.ErrorCodeUnavailable)
		}
		run, err = scanRun(tx.QueryRow(ctx, `UPDATE ops.git_sync_run SET
			status='FETCHING',attempt_count=$3,version=version+1,updated_at=clock_timestamp()
			WHERE workspace_id=$1 AND id=$2 AND version=$4 RETURNING `+runColumns,
			string(command.WorkspaceID), string(command.RunID), attemptNo, run.Version))
		if err != nil {
			return application.BeginAttemptResult{}, classify(err, domain.ErrorCodeUnavailable)
		}
		if err := commit(ctx, tx); err != nil {
			return application.BeginAttemptResult{}, err
		}
		return application.BeginAttemptResult{Run: run, Attempt: attempt, Started: true}, nil
	}
	// A crashed delivery resumes from its persisted phase; it never creates a second Attempt.
	attempt, err := scanAttempt(tx.QueryRow(ctx, `SELECT `+attemptColumns+`
		FROM ops.git_sync_attempt WHERE run_id=$1 AND status='RUNNING' FOR UPDATE`, string(run.ID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.BeginAttemptResult{}, corrupt("active Git sync run has no running attempt")
	}
	if err != nil {
		return application.BeginAttemptResult{}, classify(err, domain.ErrorCodeUnavailable)
	}
	var databaseNow time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
		return application.BeginAttemptResult{}, classify(err, domain.ErrorCodeUnavailable)
	}
	if attempt.LeaseExpiresAt.After(databaseNow) && attempt.LeaseOwner != command.Owner {
		return application.BeginAttemptResult{}, foundation.NewError(foundation.ErrorRetryableFailure, domain.ErrorCodeRunActive, true, errors.New("Git sync attempt lease is still active"))
	}
	attempt, err = scanAttempt(tx.QueryRow(ctx, `UPDATE ops.git_sync_attempt SET
		lease_owner=$2,lease_expires_at=clock_timestamp()+($3::bigint*interval '1 millisecond'),version=version+1
		WHERE id=$1 AND version=$4 AND status='RUNNING' RETURNING `+attemptColumns,
		string(attempt.ID), command.Owner, command.LeaseDuration.Milliseconds(), attempt.Version))
	if err != nil {
		return application.BeginAttemptResult{}, classify(err, domain.ErrorCodeUnavailable)
	}
	if err := commit(ctx, tx); err != nil {
		return application.BeginAttemptResult{}, err
	}
	return application.BeginAttemptResult{Run: run, Attempt: attempt, Started: true}, nil
}

// TransitionRun 在同一事务推进 Run 和 Attempt checkpoint，并续租。
func (repository *Repository) TransitionRun(ctx context.Context, command application.TransitionRunCommand) (domain.SyncRun, domain.SyncAttempt, error) {
	if repository == nil || nilInterface(repository.db) || !validID(command.WorkspaceID) || !validID(command.RunID) ||
		!validID(command.AttemptID) || !validText(command.Owner, 256) || command.ExpectedRunVersion < 1 ||
		command.ExpectedAttemptVersion < 1 || command.LeaseDuration < time.Second || command.LeaseDuration > 10*time.Minute {
		return domain.SyncRun{}, domain.SyncAttempt{}, invalid("Git sync run transition is invalid")
	}
	changed, err := marshalChanges(command.ChangedFiles)
	if err != nil {
		return domain.SyncRun{}, domain.SyncAttempt{}, err
	}
	setCheckpoints := command.ExpectedHeadOID != "" || command.ExpectedRemoteOID != "" || command.ChangedFiles != nil
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.SyncRun{}, domain.SyncAttempt{}, classify(err, domain.ErrorCodeUnavailable)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	run, err := scanRun(tx.QueryRow(ctx, `UPDATE ops.git_sync_run SET
		status=$5,direction=$6,
		expected_head_oid=CASE WHEN $7 THEN NULLIF($8,'') ELSE expected_head_oid END,
		expected_remote_oid=CASE WHEN $7 THEN NULLIF($9,'') ELSE expected_remote_oid END,
		changed_files=CASE WHEN $7 THEN $10::jsonb ELSE changed_files END,
		version=version+1,updated_at=clock_timestamp()
		WHERE workspace_id=$1 AND id=$2 AND version=$3 AND status=$4
		RETURNING `+runColumns,
		string(command.WorkspaceID), string(command.RunID), command.ExpectedRunVersion, string(command.ExpectedStatus),
		string(command.NextStatus), string(command.Direction), setCheckpoints, command.ExpectedHeadOID,
		command.ExpectedRemoteOID, changed))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SyncRun{}, domain.SyncAttempt{}, versionConflict(domain.ErrorCodeRunTransition, "Git sync run transition lost its CAS")
	}
	if err != nil {
		return domain.SyncRun{}, domain.SyncAttempt{}, classify(err, domain.ErrorCodeUnavailable)
	}
	attempt, err := scanAttempt(tx.QueryRow(ctx, `UPDATE ops.git_sync_attempt SET
		phase=$7,
		expected_head_oid=CASE WHEN $8 THEN NULLIF($9,'') ELSE expected_head_oid END,
		expected_remote_oid=CASE WHEN $8 THEN NULLIF($10,'') ELSE expected_remote_oid END,
		result_known=COALESCE($11,result_known),
		lease_expires_at=clock_timestamp()+($6::bigint*interval '1 millisecond'),version=version+1
		WHERE workspace_id=$1 AND run_id=$2 AND id=$3 AND version=$4 AND status='RUNNING'
		  AND lease_owner=$5 AND lease_expires_at>clock_timestamp()
		RETURNING `+attemptColumns,
		string(command.WorkspaceID), string(command.RunID), string(command.AttemptID), command.ExpectedAttemptVersion,
		command.Owner, command.LeaseDuration.Milliseconds(), string(command.NextStatus), setCheckpoints,
		command.ExpectedHeadOID, command.ExpectedRemoteOID, command.ResultKnown))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SyncRun{}, domain.SyncAttempt{}, leaseLost("Git sync attempt lease was lost")
	}
	if err != nil {
		return domain.SyncRun{}, domain.SyncAttempt{}, classify(err, domain.ErrorCodeUnavailable)
	}
	if err := commit(ctx, tx); err != nil {
		return domain.SyncRun{}, domain.SyncAttempt{}, err
	}
	return run, attempt, nil
}

// CompleteRun 原子关闭 Attempt/Run，并在 Pull 成功时写入独立 follow-up Outbox。
func (repository *Repository) CompleteRun(ctx context.Context, command application.CompleteRunCommand) (domain.SyncRun, error) {
	if repository == nil || nilInterface(repository.db) || !validID(command.WorkspaceID) || !validID(command.RunID) ||
		!validID(command.AttemptID) || !validText(command.Owner, 256) || command.ExpectedRunVersion < 1 || command.ExpectedAttemptVersion < 1 ||
		!command.Status.Terminal() {
		return domain.SyncRun{}, invalid("Git sync run completion is invalid")
	}
	changed, err := marshalChanges(command.ChangedFiles)
	if err != nil {
		return domain.SyncRun{}, err
	}
	setCheckpoints := command.ExpectedHeadOID != "" || command.ExpectedRemoteOID != "" || command.ChangedFiles != nil
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.SyncRun{}, classify(err, domain.ErrorCodeUnavailable)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	run, err := scanRun(tx.QueryRow(ctx, `UPDATE ops.git_sync_run SET
		status=$5,direction=$6,failure_class=$7,error_code=$8,retryable=$9,
		expected_head_oid=CASE WHEN $10 THEN NULLIF($11,'') ELSE expected_head_oid END,
		expected_remote_oid=CASE WHEN $10 THEN NULLIF($12,'') ELSE expected_remote_oid END,
		changed_files=CASE WHEN $10 THEN $13::jsonb ELSE changed_files END,
		verified_head_oid=NULLIF($14,''),verified_remote_oid=NULLIF($15,''),
		index_status=$16,index_error_code='',index_retryable=false,
		completed_at=clock_timestamp(),version=version+1,updated_at=clock_timestamp()
		WHERE workspace_id=$1 AND id=$2 AND version=$3 AND status=$4
		RETURNING `+runColumns,
		string(command.WorkspaceID), string(command.RunID), command.ExpectedRunVersion, string(command.ExpectedStatus),
		string(command.Status), string(command.Direction), string(command.FailureClass), command.ErrorCode, command.Retryable,
		setCheckpoints, command.ExpectedHeadOID, command.ExpectedRemoteOID, changed,
		command.VerifiedHeadOID, command.VerifiedRemoteOID, string(command.IndexStatus)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SyncRun{}, versionConflict(domain.ErrorCodeRunTransition, "Git sync completion lost its CAS")
	}
	if err != nil {
		return domain.SyncRun{}, classify(err, domain.ErrorCodeUnavailable)
	}
	attemptStatus := domain.AttemptFailed
	attemptError := command.ErrorCode
	if command.Status == domain.RunSucceeded {
		attemptStatus, attemptError = domain.AttemptSucceeded, ""
	}
	_, err = scanAttempt(tx.QueryRow(ctx, `UPDATE ops.git_sync_attempt SET
		status=$7,phase=$8,result_known=COALESCE($9,result_known),error_code=$10,retryable=$11,
		lease_owner=NULL,lease_expires_at=NULL,completed_at=clock_timestamp(),version=version+1
		WHERE workspace_id=$1 AND run_id=$2 AND id=$3 AND version=$4 AND status='RUNNING'
		  AND lease_owner=$5 AND lease_expires_at>clock_timestamp() AND phase=$6
		RETURNING `+attemptColumns,
		string(command.WorkspaceID), string(command.RunID), string(command.AttemptID), command.ExpectedAttemptVersion,
		command.Owner, string(command.ExpectedStatus), string(attemptStatus), string(command.Status),
		command.ResultKnown, attemptError, command.Retryable))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SyncRun{}, leaseLost("Git sync attempt lease was lost")
	}
	if err != nil {
		return domain.SyncRun{}, classify(err, domain.ErrorCodeUnavailable)
	}
	if run.Status == domain.RunSucceeded && run.Direction == domain.DirectionPull && run.IndexStatus == domain.IndexPending {
		if _, err := tx.Exec(ctx, `INSERT INTO ops.git_sync_outbox(
			id,workspace_id,run_id,kind,event_key,available_at,attempt_count,version,created_at,updated_at
		) VALUES(gen_random_uuid(),$1,$2,'INDEX_FOLLOWUP',$3,clock_timestamp(),0,1,clock_timestamp(),clock_timestamp())`,
			string(run.WorkspaceID), string(run.ID), "git-sync-index:"+string(run.ID)+":initial"); err != nil {
			return domain.SyncRun{}, classify(err, domain.ErrorCodeUnavailable)
		}
	}
	if err := commit(ctx, tx); err != nil {
		return domain.SyncRun{}, err
	}
	return run, nil
}

func findRunByCommand(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, workspaceID foundation.ID, key string) (domain.SyncRun, bool, error) {
	run, err := scanRun(queryer.QueryRow(ctx, `SELECT `+runColumns+`
		FROM ops.git_sync_run WHERE workspace_id=$1 AND idempotency_key=$2`, string(workspaceID), key))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SyncRun{}, false, nil
	}
	if err != nil {
		return domain.SyncRun{}, false, classify(err, domain.ErrorCodeUnavailable)
	}
	return run, true, nil
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}

func nullableID(value foundation.ID) any {
	if value == "" {
		return nil
	}
	return string(value)
}
