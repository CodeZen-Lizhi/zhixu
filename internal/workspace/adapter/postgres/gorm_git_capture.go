package workspacepostgres

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

// ApplyGitCaptureBatch fences commit order and applies Source changes in one
// shared GORM transaction. It does not use the legacy pgx TransactionWriter.
func (repository *GORMRepository) ApplyGitCaptureBatch(ctx context.Context, batch domain.GitCaptureBatch) (domain.GitCaptureBatchResult, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.GitCaptureBatchResult{}, err
	}
	if err := domain.ValidateGitCaptureBatch(batch); err != nil {
		return domain.GitCaptureBatchResult{}, err
	}
	var result domain.GitCaptureBatchResult
	callbackStarted := false
	callbackSucceeded := false
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, tx *gorm.DB, scope foundation.TransactionScope) error {
		callbackStarted = true
		if statement := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?,0))`, string(batch.WorkspaceID)); statement.Error != nil {
			return classifyGORMWorkspace(callbackCtx, statement.Error, "GIT_CAPTURE_CHECKPOINT_QUERY_FAILED")
		}
		replayed, err := beginGORMGitCaptureCheckpoint(callbackCtx, tx, batch)
		if err != nil {
			return err
		}
		registrations := make([]domain.SourceRegistrationResult, 0, len(batch.Registrations))
		for _, registration := range batch.Registrations {
			persisted, err := repository.RegisterSourceVersionScoped(callbackCtx, scope, registration)
			if err != nil {
				return err
			}
			registrations = append(registrations, persisted)
		}
		removed, err := tombstoneGORMGitCaptureSources(callbackCtx, tx, batch.WorkspaceID, batch.RemovedPaths, batch.ObservedAt)
		if err != nil {
			return err
		}
		result = domain.GitCaptureBatchResult{Registrations: registrations, RemovedSourceIDs: removed, Replayed: replayed}
		callbackSucceeded = true
		return nil
	})
	if err != nil {
		fallback := "GIT_CAPTURE_TRANSACTION_FAILED"
		if callbackStarted && callbackSucceeded {
			fallback = "GIT_CAPTURE_COMMIT_FAILED"
		}
		return domain.GitCaptureBatchResult{}, classifyGORMWorkspace(ctx, err, fallback)
	}
	return result, nil
}

// CompleteGitCaptureBatch advances the checkpoint after snapshot activation or
// a proven no-op, using the same transaction-scoped Workspace lock.
func (repository *GORMRepository) CompleteGitCaptureBatch(ctx context.Context, workspaceID, runID foundation.ID, beforeCommit, afterCommit string) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	callbackStarted := false
	callbackSucceeded := false
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, tx *gorm.DB, _ foundation.TransactionScope) error {
		callbackStarted = true
		if statement := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?,0))`, string(workspaceID)); statement.Error != nil {
			return classifyGORMWorkspace(callbackCtx, statement.Error, "GIT_CAPTURE_CHECKPOINT_QUERY_FAILED")
		}
		statement := tx.Exec(`UPDATE core.workspace_git_capture_checkpoint
			SET completed_head_oid=?,inflight_run_id=NULL,inflight_before_oid=NULL,inflight_after_oid=NULL,
				inflight_request_hash=NULL,last_run_id=inflight_run_id,last_before_oid=inflight_before_oid,
				last_after_oid=inflight_after_oid,last_request_hash=inflight_request_hash,
				version=version+1,updated_at=GREATEST(clock_timestamp(),created_at,updated_at)
			WHERE workspace_id=? AND inflight_run_id=? AND inflight_before_oid=? AND inflight_after_oid=?`,
			afterCommit, string(workspaceID), string(runID), beforeCommit, afterCommit)
		if statement.Error != nil {
			return classifyGORMWorkspace(callbackCtx, statement.Error, "GIT_CAPTURE_CHECKPOINT_COMPLETE_FAILED")
		}
		if statement.RowsAffected != 1 {
			row, err := gormWorkspaceRawRow(tx, `SELECT EXISTS(SELECT 1 FROM core.workspace_git_capture_checkpoint
				WHERE workspace_id=? AND completed_head_oid=? AND last_run_id=?
				  AND last_before_oid=? AND last_after_oid=?)`, string(workspaceID), afterCommit, string(runID), beforeCommit, afterCommit)
			if err != nil {
				return classifyGORMWorkspace(callbackCtx, err, "GIT_CAPTURE_CHECKPOINT_QUERY_FAILED")
			}
			var exact bool
			if err := row.Scan(&exact); err != nil {
				return classifyGORMWorkspace(callbackCtx, err, "GIT_CAPTURE_CHECKPOINT_QUERY_FAILED")
			}
			if !exact {
				return foundation.NewError(foundation.ErrorVersionConflict, "GIT_CAPTURE_CHECKPOINT_CONFLICT", true, errors.New("Git capture checkpoint changed"))
			}
		}
		callbackSucceeded = true
		return nil
	})
	if err == nil {
		return nil
	}
	fallback := "GIT_CAPTURE_CHECKPOINT_TRANSACTION_FAILED"
	if callbackStarted && callbackSucceeded {
		fallback = "GIT_CAPTURE_CHECKPOINT_COMMIT_FAILED"
	}
	return classifyGORMWorkspace(ctx, err, fallback)
}

func beginGORMGitCaptureCheckpoint(ctx context.Context, tx *gorm.DB, batch domain.GitCaptureBatch) (bool, error) {
	row, err := gormWorkspaceRawRow(tx, `SELECT completed_head_oid,inflight_run_id::text,inflight_before_oid,inflight_after_oid,inflight_request_hash,
		last_run_id::text,last_before_oid,last_after_oid,last_request_hash
		FROM core.workspace_git_capture_checkpoint WHERE workspace_id=? FOR UPDATE`, string(batch.WorkspaceID))
	if err != nil {
		return false, classifyGORMWorkspace(ctx, err, "GIT_CAPTURE_CHECKPOINT_QUERY_FAILED")
	}
	var completed string
	var runID, before, after, requestHash, lastRunID, lastBefore, lastAfter, lastRequestHash sql.NullString
	err = row.Scan(&completed, &runID, &before, &after, &requestHash, &lastRunID, &lastBefore, &lastAfter, &lastRequestHash)
	if gormWorkspaceNoRows(err) {
		statement := tx.Exec(`INSERT INTO core.workspace_git_capture_checkpoint(
			workspace_id,completed_head_oid,inflight_run_id,inflight_before_oid,inflight_after_oid,inflight_request_hash,
			version,created_at,updated_at
		) VALUES(?,?,?,?,?,?,1,?,?)`, string(batch.WorkspaceID), batch.BeforeCommit, string(batch.RunID), batch.BeforeCommit,
			batch.AfterCommit, batch.RequestHash, batch.ObservedAt.UTC(), batch.ObservedAt.UTC())
		if statement.Error != nil {
			return false, classifyGORMWorkspace(ctx, statement.Error, "GIT_CAPTURE_CHECKPOINT_CREATE_FAILED")
		}
		return false, nil
	}
	if err != nil {
		return false, classifyGORMWorkspace(ctx, err, "GIT_CAPTURE_CHECKPOINT_QUERY_FAILED")
	}
	if runID.Valid || before.Valid || after.Valid || requestHash.Valid {
		if runID.Valid && before.Valid && after.Valid && requestHash.Valid && runID.String == string(batch.RunID) &&
			before.String == batch.BeforeCommit && after.String == batch.AfterCommit && requestHash.String == batch.RequestHash {
			return true, nil
		}
		return false, foundation.NewError(foundation.ErrorRetryableFailure, "GIT_CAPTURE_OUT_OF_ORDER", true, errors.New("another Git capture batch is in flight"))
	}
	if lastRunID.Valid && lastBefore.Valid && lastAfter.Valid && lastRequestHash.Valid &&
		lastRunID.String == string(batch.RunID) && lastBefore.String == batch.BeforeCommit &&
		lastAfter.String == batch.AfterCommit && lastRequestHash.String == batch.RequestHash && completed == batch.AfterCommit {
		return true, nil
	}
	if completed != batch.BeforeCommit {
		return false, foundation.NewError(foundation.ErrorRetryableFailure, "GIT_CAPTURE_OUT_OF_ORDER", true, errors.New("Git capture commit order is not contiguous"))
	}
	statement := tx.Exec(`UPDATE core.workspace_git_capture_checkpoint
		SET inflight_run_id=?,inflight_before_oid=?,inflight_after_oid=?,inflight_request_hash=?,
			version=version+1,updated_at=?
		WHERE workspace_id=? AND completed_head_oid=? AND inflight_run_id IS NULL`, string(batch.RunID), batch.BeforeCommit,
		batch.AfterCommit, batch.RequestHash, batch.ObservedAt.UTC(), string(batch.WorkspaceID), batch.BeforeCommit)
	if statement.Error != nil {
		return false, classifyGORMWorkspace(ctx, statement.Error, "GIT_CAPTURE_CHECKPOINT_UPDATE_FAILED")
	}
	if statement.RowsAffected != 1 {
		return false, foundation.NewError(foundation.ErrorRetryableFailure, "GIT_CAPTURE_OUT_OF_ORDER", true, errors.New("Git capture checkpoint changed"))
	}
	return false, nil
}

func tombstoneGORMGitCaptureSources(ctx context.Context, tx *gorm.DB, workspaceID foundation.ID, paths []string, observedAt time.Time) ([]foundation.ID, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	rows, err := gormWorkspaceRows(tx, `UPDATE core.source SET removed_at=?
		WHERE workspace_id=? AND original_location=ANY(?::text[]) AND removed_at IS NULL
		RETURNING id::text`, observedAt.UTC(), string(workspaceID), pq.Array(paths))
	if err != nil {
		return nil, classifyGORMWorkspace(ctx, err, "GIT_CAPTURE_SOURCE_REMOVE_FAILED")
	}
	for rows.Next() {
		var ignored string
		if err := rows.Scan(&ignored); err != nil {
			_ = rows.Close()
			return nil, classifyGORMWorkspace(ctx, err, "GIT_CAPTURE_SOURCE_REMOVE_SCAN_FAILED")
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, classifyGORMWorkspace(ctx, err, "GIT_CAPTURE_SOURCE_REMOVE_SCAN_FAILED")
	}
	if err := rows.Close(); err != nil {
		return nil, classifyGORMWorkspace(ctx, err, "GIT_CAPTURE_SOURCE_REMOVE_SCAN_FAILED")
	}

	lookup, err := gormWorkspaceRows(tx, `SELECT id::text FROM core.source
		WHERE workspace_id=? AND original_location=ANY(?::text[]) AND removed_at IS NOT NULL ORDER BY id`,
		string(workspaceID), pq.Array(paths))
	if err != nil {
		return nil, classifyGORMWorkspace(ctx, err, "GIT_CAPTURE_SOURCE_REMOVE_QUERY_FAILED")
	}
	defer lookup.Close()
	removed := make([]foundation.ID, 0, len(paths))
	for lookup.Next() {
		var rawID string
		if err := lookup.Scan(&rawID); err != nil {
			return nil, classifyGORMWorkspace(ctx, err, "GIT_CAPTURE_SOURCE_REMOVE_SCAN_FAILED")
		}
		id, err := foundation.ParseID(rawID)
		if err != nil {
			return nil, foundation.NewError(foundation.ErrorConsistencyViolation, "GIT_CAPTURE_SOURCE_ID_INVALID", false, errors.New("Git capture source id is invalid"))
		}
		removed = append(removed, id)
	}
	if err := lookup.Err(); err != nil {
		return nil, classifyGORMWorkspace(ctx, err, "GIT_CAPTURE_SOURCE_REMOVE_SCAN_FAILED")
	}
	sort.Slice(removed, func(left, right int) bool { return removed[left] < removed[right] })
	return removed, nil
}

var _ domain.GitCaptureRepository = (*GORMRepository)(nil)
