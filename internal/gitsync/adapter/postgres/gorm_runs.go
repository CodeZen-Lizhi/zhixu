package postgres

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
)

const (
	gormInsertRunSQL = `INSERT INTO ops.git_sync_run(
	id,workspace_id,config_revision,remote_url,branch,trigger,retry_of_run_id,idempotency_key,request_hash,
	status,direction,failure_class,error_code,retryable,expected_head_oid,expected_remote_oid,
	verified_head_oid,verified_remote_oid,changed_files,index_status,index_error_code,index_retryable,
	attempt_count,version,created_at,updated_at,completed_at
) VALUES(?,?,?,?,?, ?,NULLIF(?,'')::uuid,?,?, 'PENDING','UNKNOWN','NONE','',false,
	NULL,NULL,NULL,NULL,?,'NOT_REQUIRED','',false,0,1,?,?,NULL)
RETURNING ` + gormRunColumns

	gormInsertRunOutboxSQL = `INSERT INTO ops.git_sync_outbox(
	id,workspace_id,run_id,kind,event_key,available_at,attempt_count,version,created_at,updated_at
) VALUES(gen_random_uuid(),?,?,'EXECUTE_RUN',?,clock_timestamp(),0,1,clock_timestamp(),clock_timestamp())`

	gormMarkRunStaleSQL = `UPDATE ops.git_sync_run SET
	status='STALE',failure_class='STALE_CONFIG',error_code=?,retryable=false,
	completed_at=clock_timestamp(),version=version+1,updated_at=clock_timestamp()
	WHERE workspace_id=? AND id=? AND version=? RETURNING ` + gormRunColumns

	gormInsertAttemptSQL = `INSERT INTO ops.git_sync_attempt(
	id,workspace_id,run_id,attempt_no,status,phase,error_code,retryable,
	lease_owner,lease_expires_at,started_at,version
) VALUES(?,?,?,?,'RUNNING','FETCHING','',false,?,
	clock_timestamp()+(?::bigint*interval '1 millisecond'),clock_timestamp(),1)
RETURNING ` + gormAttemptColumns

	gormStartRunSQL = `UPDATE ops.git_sync_run SET
	status='FETCHING',attempt_count=?,version=version+1,updated_at=clock_timestamp()
	WHERE workspace_id=? AND id=? AND version=? RETURNING ` + gormRunColumns

	gormTakeoverAttemptSQL = `UPDATE ops.git_sync_attempt SET
	lease_owner=?,lease_expires_at=clock_timestamp()+(?::bigint*interval '1 millisecond'),version=version+1
	WHERE id=? AND version=? AND status='RUNNING' RETURNING ` + gormAttemptColumns

	gormTransitionRunSQL = `UPDATE ops.git_sync_run SET
	status=?,direction=?,
	expected_head_oid=CASE WHEN ? THEN NULLIF(?,'') ELSE expected_head_oid END,
	expected_remote_oid=CASE WHEN ? THEN NULLIF(?,'') ELSE expected_remote_oid END,
	changed_files=CASE WHEN ? THEN ?::jsonb ELSE changed_files END,
	version=version+1,updated_at=clock_timestamp()
	WHERE workspace_id=? AND id=? AND version=? AND status=?
	RETURNING ` + gormRunColumns

	gormTransitionAttemptSQL = `UPDATE ops.git_sync_attempt SET
	phase=?,
	expected_head_oid=CASE WHEN ? THEN NULLIF(?,'') ELSE expected_head_oid END,
	expected_remote_oid=CASE WHEN ? THEN NULLIF(?,'') ELSE expected_remote_oid END,
	result_known=COALESCE(?,result_known),
	lease_expires_at=clock_timestamp()+(?::bigint*interval '1 millisecond'),version=version+1
	WHERE workspace_id=? AND run_id=? AND id=? AND version=? AND status='RUNNING'
	  AND lease_owner=? AND lease_expires_at>clock_timestamp()
	RETURNING ` + gormAttemptColumns

	gormCompleteRunSQL = `UPDATE ops.git_sync_run SET
	status=?,direction=?,failure_class=?,error_code=?,retryable=?,
	expected_head_oid=CASE WHEN ? THEN NULLIF(?,'') ELSE expected_head_oid END,
	expected_remote_oid=CASE WHEN ? THEN NULLIF(?,'') ELSE expected_remote_oid END,
	changed_files=CASE WHEN ? THEN ?::jsonb ELSE changed_files END,
	verified_head_oid=NULLIF(?,'') ,verified_remote_oid=NULLIF(?,''),
	index_status=?,index_error_code='',index_retryable=false,
	completed_at=clock_timestamp(),version=version+1,updated_at=clock_timestamp()
	WHERE workspace_id=? AND id=? AND version=? AND status=?
	RETURNING ` + gormRunColumns

	gormCompleteAttemptSQL = `UPDATE ops.git_sync_attempt SET
	status=?,phase=?,result_known=COALESCE(?,result_known),error_code=?,retryable=?,
	lease_owner=NULL,lease_expires_at=NULL,completed_at=clock_timestamp(),version=version+1
	WHERE workspace_id=? AND run_id=? AND id=? AND version=? AND status='RUNNING'
	  AND lease_owner=? AND lease_expires_at>clock_timestamp() AND phase=?
	RETURNING ` + gormAttemptColumns

	gormInsertIndexOutboxSQL = `INSERT INTO ops.git_sync_outbox(
	id,workspace_id,run_id,kind,event_key,available_at,attempt_count,version,created_at,updated_at
) VALUES(gen_random_uuid(),?,?,'INDEX_FOLLOWUP',?,clock_timestamp(),0,1,clock_timestamp(),clock_timestamp())`
)

// CreateRun atomically creates a Run and its execution outbox entry.
func (repository *GORMRepository) CreateRun(ctx context.Context, record application.CreateRunRecord) (domain.SyncRun, bool, error) {
	run := record.Run
	if err := repository.ready(ctx); err != nil {
		return domain.SyncRun{}, false, err
	}
	if run.Validate() != nil || run.Status != domain.RunPending {
		return domain.SyncRun{}, false, invalid("Git sync run creation is invalid")
	}
	var result domain.SyncRun
	replayed := false
	err := repository.within(ctx, func(callbackCtx context.Context, tx *gorm.DB) error {
		if err := gormLockWorkspace(callbackCtx, tx, run.WorkspaceID, workspaceLockPurpose); err != nil {
			return err
		}
		existing, found, err := gormFindRunByCommand(callbackCtx, tx, run.WorkspaceID, run.IdempotencyKey)
		if err != nil {
			return err
		}
		if found {
			if existing.RequestHash != run.RequestHash || existing.Trigger != run.Trigger || existing.RetryOfRunID != run.RetryOfRunID {
				return versionConflict(domain.ErrorCodeIdempotencyConflict, "Git sync idempotency key is bound to another run")
			}
			result, replayed = existing, true
			return nil
		}
		configRow, err := gormRawRow(callbackCtx, tx, gormRunConfigFenceSQL, string(run.WorkspaceID))
		if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		var configured, autoSync, tokenConfigured bool
		var revision int64
		var remoteURL, branch string
		if err := configRow.Scan(&configured, &autoSync, &tokenConfigured, &revision, &remoteURL, &branch); gormNoRows(err) || (err == nil && !configured) {
			return foundation.NewError(foundation.ErrorNotFound, domain.ErrorCodeConfigNotFound, false, errors.New("Git remote is not configured"))
		} else if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		if !tokenConfigured {
			return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeSecretUnavailable, false, errors.New("Git remote token is not configured"))
		}
		if run.Trigger == domain.TriggerAutomatic && !autoSync {
			return versionConflict(domain.ErrorCodeAutoSyncDisabled, "automatic Git sync is disabled")
		}
		if revision != run.ConfigRevision || remoteURL != run.RemoteURL || branch != run.Branch {
			return versionConflict(domain.ErrorCodeConfigStale, "Git remote configuration changed before run creation")
		}
		changed, err := marshalChanges(run.ChangedFiles)
		if err != nil {
			return err
		}
		row, err := gormRawRow(callbackCtx, tx, gormInsertRunSQL,
			string(run.ID), string(run.WorkspaceID), run.ConfigRevision, run.RemoteURL, run.Branch, string(run.Trigger),
			string(run.RetryOfRunID), run.IdempotencyKey, run.RequestHash, gitSyncJSONB(changed), run.CreatedAt.UTC(), run.CreatedAt.UTC())
		if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		result, err = scanRun(row)
		if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		if _, err := gormExec(callbackCtx, tx, gormInsertRunOutboxSQL, string(run.WorkspaceID), string(run.ID), "git-sync-run:"+string(run.ID)); err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		return nil
	})
	if err != nil {
		return domain.SyncRun{}, false, err
	}
	return result, replayed, err
}

// ReplayRun returns the exact idempotent Run before a new state is read.
func (repository *GORMRepository) ReplayRun(ctx context.Context, command application.ReplayRunCommand) (domain.SyncRun, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.SyncRun{}, false, err
	}
	if !validID(command.WorkspaceID) || !validText(command.IdempotencyKey, 128) || !validText(command.RequestHash, 64) {
		return domain.SyncRun{}, false, invalid("Git sync run replay query is invalid")
	}
	existing, found, err := gormFindRunByCommand(ctx, repository.database, command.WorkspaceID, command.IdempotencyKey)
	if err != nil || !found {
		return existing, found, err
	}
	if existing.RequestHash != command.RequestHash || existing.Trigger != command.Trigger || existing.RetryOfRunID != command.RetryOfRunID {
		return domain.SyncRun{}, false, versionConflict(domain.ErrorCodeIdempotencyConflict, "Git sync idempotency key is bound to another run")
	}
	return existing, true, nil
}

// GetRun returns one Workspace-scoped Run.
func (repository *GORMRepository) GetRun(ctx context.Context, workspaceID, runID foundation.ID) (domain.SyncRun, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.SyncRun{}, err
	}
	if !validID(workspaceID) || !validID(runID) {
		return domain.SyncRun{}, invalid("Git sync run query is invalid")
	}
	row, err := gormRawRow(ctx, repository.database, gormGetRunSQL, string(workspaceID), string(runID))
	if err != nil {
		return domain.SyncRun{}, classifyGORM(ctx, err, domain.ErrorCodeUnavailable)
	}
	run, err := scanRun(row)
	if gormNoRows(err) {
		return domain.SyncRun{}, notFound("Git sync run was not found")
	}
	if err != nil {
		return domain.SyncRun{}, classifyGORM(ctx, err, domain.ErrorCodeUnavailable)
	}
	return run, nil
}

// GetCurrentRun returns the newest Run for a Workspace.
func (repository *GORMRepository) GetCurrentRun(ctx context.Context, workspaceID foundation.ID) (domain.SyncRun, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.SyncRun{}, false, err
	}
	if !validID(workspaceID) {
		return domain.SyncRun{}, false, invalid("Git sync current run query is invalid")
	}
	row, err := gormRawRow(ctx, repository.database, gormGetCurrentRunSQL, string(workspaceID))
	if err != nil {
		return domain.SyncRun{}, false, classifyGORM(ctx, err, domain.ErrorCodeUnavailable)
	}
	run, err := scanRun(row)
	if gormNoRows(err) {
		return domain.SyncRun{}, false, nil
	}
	if err != nil {
		return domain.SyncRun{}, false, classifyGORM(ctx, err, domain.ErrorCodeUnavailable)
	}
	return run, true, nil
}

// ListRuns performs a bounded (created_at,id) keyset query.
func (repository *GORMRepository) ListRuns(ctx context.Context, query application.ListRunsQuery) (page application.RunPage, returnErr error) {
	if err := repository.ready(ctx); err != nil {
		return application.RunPage{}, err
	}
	if !validID(query.WorkspaceID) || query.Limit < 1 || query.Limit > domain.MaxListLimit ||
		(query.BeforeTime.IsZero() != (query.BeforeID == "")) || (!query.BeforeTime.IsZero() && !validID(query.BeforeID)) {
		return application.RunPage{}, invalid("Git sync run list query is invalid")
	}
	rows, err := gormRawRows(ctx, repository.database, gormListRunsSQL, string(query.WorkspaceID), nullableTime(query.BeforeTime),
		nullableTime(query.BeforeTime), nullableID(query.BeforeID), query.Limit+1)
	if err != nil {
		return application.RunPage{}, classifyGORM(ctx, err, domain.ErrorCodeUnavailable)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && returnErr == nil {
			page = application.RunPage{}
			returnErr = classifyGORM(ctx, closeErr, domain.ErrorCodeUnavailable)
		}
	}()
	items := make([]domain.SyncRun, 0, query.Limit+1)
	for rows.Next() {
		run, scanErr := scanRun(rows)
		if scanErr != nil {
			return application.RunPage{}, classifyGORM(ctx, scanErr, domain.ErrorCodeUnavailable)
		}
		items = append(items, run)
	}
	if err := rows.Err(); err != nil {
		return application.RunPage{}, classifyGORM(ctx, err, domain.ErrorCodeUnavailable)
	}
	page = application.RunPage{Items: items}
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

// BeginAttempt creates or reclaims the single persisted Attempt.
func (repository *GORMRepository) BeginAttempt(ctx context.Context, command application.BeginAttemptCommand) (application.BeginAttemptResult, error) {
	if err := repository.ready(ctx); err != nil {
		return application.BeginAttemptResult{}, err
	}
	if !validID(command.WorkspaceID) || !validID(command.RunID) || !validID(command.AttemptID) || !validText(command.Owner, 256) ||
		command.LeaseDuration < time.Second || command.LeaseDuration > 10*time.Minute {
		return application.BeginAttemptResult{}, invalid("Git sync attempt claim is invalid")
	}
	var result application.BeginAttemptResult
	err := repository.within(ctx, func(callbackCtx context.Context, tx *gorm.DB) error {
		if err := gormLockWorkspace(callbackCtx, tx, command.WorkspaceID, workspaceLockPurpose); err != nil {
			return err
		}
		row, err := gormRawRow(callbackCtx, tx, gormRunForUpdateSQL, string(command.WorkspaceID), string(command.RunID))
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
		if run.Status.Terminal() {
			result.Run = run
			return nil
		}
		if run.Status == domain.RunPending {
			configRow, queryErr := gormRawRow(callbackCtx, tx, gormAttemptConfigFenceSQL, string(command.WorkspaceID))
			if queryErr != nil {
				return classifyGORM(callbackCtx, queryErr, domain.ErrorCodeUnavailable)
			}
			var configured, tokenConfigured bool
			var revision int64
			scanErr := configRow.Scan(&configured, &tokenConfigured, &revision)
			if gormNoRows(scanErr) || (scanErr == nil && (!configured || !tokenConfigured || revision != run.ConfigRevision)) {
				staleRow, staleErr := gormRawRow(callbackCtx, tx, gormMarkRunStaleSQL, domain.ErrorCodeConfigStale,
					string(command.WorkspaceID), string(command.RunID), run.Version)
				if staleErr != nil {
					return classifyGORM(callbackCtx, staleErr, domain.ErrorCodeUnavailable)
				}
				result.Run, staleErr = scanRun(staleRow)
				if staleErr != nil {
					return classifyGORM(callbackCtx, staleErr, domain.ErrorCodeUnavailable)
				}
				return nil
			}
			if scanErr != nil {
				return classifyGORM(callbackCtx, scanErr, domain.ErrorCodeUnavailable)
			}
			attemptNo := run.AttemptCount + 1
			attemptRow, insertErr := gormRawRow(callbackCtx, tx, gormInsertAttemptSQL, string(command.AttemptID),
				string(command.WorkspaceID), string(command.RunID), attemptNo, command.Owner, command.LeaseDuration.Milliseconds())
			if insertErr != nil {
				return classifyGORM(callbackCtx, insertErr, domain.ErrorCodeUnavailable)
			}
			attempt, scanErr := scanAttempt(attemptRow)
			if scanErr != nil {
				return classifyGORM(callbackCtx, scanErr, domain.ErrorCodeUnavailable)
			}
			startRow, updateErr := gormRawRow(callbackCtx, tx, gormStartRunSQL, attemptNo, string(command.WorkspaceID), string(command.RunID), run.Version)
			if updateErr != nil {
				return classifyGORM(callbackCtx, updateErr, domain.ErrorCodeUnavailable)
			}
			updatedRun, scanErr := scanRun(startRow)
			if scanErr != nil {
				return classifyGORM(callbackCtx, scanErr, domain.ErrorCodeUnavailable)
			}
			result = application.BeginAttemptResult{Run: updatedRun, Attempt: attempt, Started: true}
			return nil
		}
		attemptRow, err := gormRawRow(callbackCtx, tx, gormRunningAttemptSQL, string(run.ID))
		if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		attempt, err := scanAttempt(attemptRow)
		if gormNoRows(err) {
			return corrupt("active Git sync run has no running attempt")
		}
		if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		clockRow, err := gormRawRow(callbackCtx, tx, gormClockTimestampSQL)
		if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		var databaseNow time.Time
		if err := clockRow.Scan(&databaseNow); err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		if attempt.LeaseExpiresAt.After(databaseNow) && attempt.LeaseOwner != command.Owner {
			return foundation.NewError(foundation.ErrorRetryableFailure, domain.ErrorCodeRunActive, true, errors.New("Git sync attempt lease is still active"))
		}
		takeoverRow, err := gormRawRow(callbackCtx, tx, gormTakeoverAttemptSQL, command.Owner, command.LeaseDuration.Milliseconds(),
			string(attempt.ID), attempt.Version)
		if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		attempt, err = scanAttempt(takeoverRow)
		if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		result = application.BeginAttemptResult{Run: run, Attempt: attempt, Started: true}
		return nil
	})
	if err != nil {
		return application.BeginAttemptResult{}, err
	}
	return result, err
}

// TransitionRun advances Run and Attempt checkpoints in one transaction.
func (repository *GORMRepository) TransitionRun(ctx context.Context, command application.TransitionRunCommand) (domain.SyncRun, domain.SyncAttempt, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.SyncRun{}, domain.SyncAttempt{}, err
	}
	if !validID(command.WorkspaceID) || !validID(command.RunID) || !validID(command.AttemptID) || !validText(command.Owner, 256) ||
		command.ExpectedRunVersion < 1 || command.ExpectedAttemptVersion < 1 || command.LeaseDuration < time.Second || command.LeaseDuration > 10*time.Minute {
		return domain.SyncRun{}, domain.SyncAttempt{}, invalid("Git sync run transition is invalid")
	}
	changed, err := marshalChanges(command.ChangedFiles)
	if err != nil {
		return domain.SyncRun{}, domain.SyncAttempt{}, err
	}
	setCheckpoints := command.ExpectedHeadOID != "" || command.ExpectedRemoteOID != "" || command.ChangedFiles != nil
	var run domain.SyncRun
	var attempt domain.SyncAttempt
	err = repository.within(ctx, func(callbackCtx context.Context, tx *gorm.DB) error {
		runRow, err := gormRawRow(callbackCtx, tx, gormTransitionRunSQL, string(command.NextStatus), string(command.Direction),
			setCheckpoints, command.ExpectedHeadOID, setCheckpoints, command.ExpectedRemoteOID, setCheckpoints, gitSyncJSONB(changed),
			string(command.WorkspaceID), string(command.RunID), command.ExpectedRunVersion, string(command.ExpectedStatus))
		if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		if run, err = scanRun(runRow); gormNoRows(err) {
			return versionConflict(domain.ErrorCodeRunTransition, "Git sync run transition lost its CAS")
		} else if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		attemptRow, err := gormRawRow(callbackCtx, tx, gormTransitionAttemptSQL, string(command.NextStatus),
			setCheckpoints, command.ExpectedHeadOID, setCheckpoints, command.ExpectedRemoteOID, command.ResultKnown,
			command.LeaseDuration.Milliseconds(), string(command.WorkspaceID), string(command.RunID), string(command.AttemptID),
			command.ExpectedAttemptVersion, command.Owner)
		if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		if attempt, err = scanAttempt(attemptRow); gormNoRows(err) {
			return leaseLost("Git sync attempt lease was lost")
		} else if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		return nil
	})
	if err != nil {
		return domain.SyncRun{}, domain.SyncAttempt{}, err
	}
	return run, attempt, err
}

// CompleteRun atomically closes the Attempt and Run and emits a pull index
// follow-up when needed.
func (repository *GORMRepository) CompleteRun(ctx context.Context, command application.CompleteRunCommand) (domain.SyncRun, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.SyncRun{}, err
	}
	if !validID(command.WorkspaceID) || !validID(command.RunID) || !validID(command.AttemptID) || !validText(command.Owner, 256) ||
		command.ExpectedRunVersion < 1 || command.ExpectedAttemptVersion < 1 || !command.Status.Terminal() {
		return domain.SyncRun{}, invalid("Git sync run completion is invalid")
	}
	changed, err := marshalChanges(command.ChangedFiles)
	if err != nil {
		return domain.SyncRun{}, err
	}
	setCheckpoints := command.ExpectedHeadOID != "" || command.ExpectedRemoteOID != "" || command.ChangedFiles != nil
	var run domain.SyncRun
	err = repository.within(ctx, func(callbackCtx context.Context, tx *gorm.DB) error {
		runRow, err := gormRawRow(callbackCtx, tx, gormCompleteRunSQL, string(command.Status), string(command.Direction), string(command.FailureClass),
			command.ErrorCode, command.Retryable, setCheckpoints, command.ExpectedHeadOID, setCheckpoints, command.ExpectedRemoteOID,
			setCheckpoints, gitSyncJSONB(changed), command.VerifiedHeadOID, command.VerifiedRemoteOID, string(command.IndexStatus),
			string(command.WorkspaceID), string(command.RunID), command.ExpectedRunVersion, string(command.ExpectedStatus))
		if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		if run, err = scanRun(runRow); gormNoRows(err) {
			return versionConflict(domain.ErrorCodeRunTransition, "Git sync completion lost its CAS")
		} else if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		attemptStatus := domain.AttemptFailed
		attemptError := command.ErrorCode
		if command.Status == domain.RunSucceeded {
			attemptStatus, attemptError = domain.AttemptSucceeded, ""
		}
		attemptRow, err := gormRawRow(callbackCtx, tx, gormCompleteAttemptSQL, string(attemptStatus), string(command.Status), command.ResultKnown,
			attemptError, command.Retryable, string(command.WorkspaceID), string(command.RunID), string(command.AttemptID),
			command.ExpectedAttemptVersion, command.Owner, string(command.ExpectedStatus))
		if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		if _, err = scanAttempt(attemptRow); gormNoRows(err) {
			return leaseLost("Git sync attempt lease was lost")
		} else if err != nil {
			return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
		}
		if run.Status == domain.RunSucceeded && run.Direction == domain.DirectionPull && run.IndexStatus == domain.IndexPending {
			if _, err := gormExec(callbackCtx, tx, gormInsertIndexOutboxSQL, string(run.WorkspaceID), string(run.ID), "git-sync-index:"+string(run.ID)+":initial"); err != nil {
				return classifyGORM(callbackCtx, err, domain.ErrorCodeUnavailable)
			}
		}
		return nil
	})
	if err != nil {
		return domain.SyncRun{}, err
	}
	return run, err
}

func gormFindRunByCommand(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, key string) (domain.SyncRun, bool, error) {
	row, err := gormRawRow(ctx, database, gormRunByCommandSQL, string(workspaceID), key)
	if err != nil {
		return domain.SyncRun{}, false, classifyGORM(ctx, err, domain.ErrorCodeUnavailable)
	}
	run, err := scanRun(row)
	if gormNoRows(err) {
		return domain.SyncRun{}, false, nil
	}
	if err != nil {
		return domain.SyncRun{}, false, classifyGORM(ctx, err, domain.ErrorCodeUnavailable)
	}
	return run, true, nil
}
