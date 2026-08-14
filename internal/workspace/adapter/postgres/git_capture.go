package workspacepostgres

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
	"github.com/jackc/pgx/v5"
)

// ApplyGitCaptureBatch fences commit order and applies all Source changes in one transaction.
func (r *Repository) ApplyGitCaptureBatch(ctx context.Context, batch domain.GitCaptureBatch) (domain.GitCaptureBatchResult, error) {
	if err := domain.ValidateGitCaptureBatch(batch); err != nil {
		return domain.GitCaptureBatchResult{}, err
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.GitCaptureBatchResult{}, classify(err, "GIT_CAPTURE_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, string(batch.WorkspaceID)); err != nil {
		return domain.GitCaptureBatchResult{}, classify(err, "GIT_CAPTURE_CHECKPOINT_QUERY_FAILED")
	}
	replayed, err := beginGitCaptureCheckpoint(ctx, tx, batch)
	if err != nil {
		return domain.GitCaptureBatchResult{}, err
	}
	writer := TransactionWriter{}
	results := make([]domain.SourceRegistrationResult, 0, len(batch.Registrations))
	for _, registration := range batch.Registrations {
		result, writeErr := writer.RegisterSourceVersion(ctx, tx, registration)
		if writeErr != nil {
			return domain.GitCaptureBatchResult{}, writeErr
		}
		results = append(results, result)
	}
	removed, err := tombstoneGitCaptureSources(ctx, tx, batch.WorkspaceID, batch.RemovedPaths, batch.ObservedAt)
	if err != nil {
		return domain.GitCaptureBatchResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.GitCaptureBatchResult{}, classify(err, "GIT_CAPTURE_COMMIT_FAILED")
	}
	return domain.GitCaptureBatchResult{Registrations: results, RemovedSourceIDs: removed, Replayed: replayed}, nil
}

// CompleteGitCaptureBatch advances the Workspace checkpoint only after the
// corresponding snapshot activation or a proven no-op.
func (r *Repository) CompleteGitCaptureBatch(ctx context.Context, workspaceID, runID foundation.ID, beforeCommit, afterCommit string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return classify(err, "GIT_CAPTURE_CHECKPOINT_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, string(workspaceID)); err != nil {
		return classify(err, "GIT_CAPTURE_CHECKPOINT_QUERY_FAILED")
	}
	tag, err := tx.Exec(ctx, `UPDATE core.workspace_git_capture_checkpoint
		SET completed_head_oid=$4,inflight_run_id=NULL,inflight_before_oid=NULL,inflight_after_oid=NULL,
			inflight_request_hash=NULL,last_run_id=inflight_run_id,last_before_oid=inflight_before_oid,
			last_after_oid=inflight_after_oid,last_request_hash=inflight_request_hash,
			version=version+1,updated_at=GREATEST(clock_timestamp(),created_at,updated_at)
		WHERE workspace_id=$1 AND inflight_run_id=$2 AND inflight_before_oid=$3 AND inflight_after_oid=$4`,
		string(workspaceID), string(runID), beforeCommit, afterCommit)
	if err != nil {
		return classify(err, "GIT_CAPTURE_CHECKPOINT_COMPLETE_FAILED")
	}
	if tag.RowsAffected() != 1 {
		var exact bool
		queryErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM core.workspace_git_capture_checkpoint
			WHERE workspace_id=$1 AND completed_head_oid=$4 AND last_run_id=$2
			  AND last_before_oid=$3 AND last_after_oid=$4)`, string(workspaceID), string(runID), beforeCommit, afterCommit).Scan(&exact)
		if queryErr != nil {
			return classify(queryErr, "GIT_CAPTURE_CHECKPOINT_QUERY_FAILED")
		}
		if !exact {
			return foundation.NewError(foundation.ErrorVersionConflict, "GIT_CAPTURE_CHECKPOINT_CONFLICT", true, errors.New("Git capture checkpoint changed"))
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return classify(err, "GIT_CAPTURE_CHECKPOINT_COMMIT_FAILED")
	}
	return nil
}

func beginGitCaptureCheckpoint(ctx context.Context, tx pgx.Tx, batch domain.GitCaptureBatch) (bool, error) {
	var completed string
	var runID, before, after, requestHash, lastRunID, lastBefore, lastAfter, lastRequestHash *string
	err := tx.QueryRow(ctx, `SELECT completed_head_oid,inflight_run_id::text,inflight_before_oid,inflight_after_oid,inflight_request_hash
		,last_run_id::text,last_before_oid,last_after_oid,last_request_hash
		FROM core.workspace_git_capture_checkpoint WHERE workspace_id=$1 FOR UPDATE`, string(batch.WorkspaceID)).Scan(
		&completed, &runID, &before, &after, &requestHash, &lastRunID, &lastBefore, &lastAfter, &lastRequestHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		_, insertErr := tx.Exec(ctx, `INSERT INTO core.workspace_git_capture_checkpoint(
			workspace_id,completed_head_oid,inflight_run_id,inflight_before_oid,inflight_after_oid,inflight_request_hash,
			version,created_at,updated_at
		) VALUES($1,$2,$3,$2,$4,$5,1,$6,$6)`, string(batch.WorkspaceID), batch.BeforeCommit, string(batch.RunID),
			batch.AfterCommit, batch.RequestHash, batch.ObservedAt.UTC())
		if insertErr != nil {
			return false, classify(insertErr, "GIT_CAPTURE_CHECKPOINT_CREATE_FAILED")
		}
		return false, nil
	}
	if err != nil {
		return false, classify(err, "GIT_CAPTURE_CHECKPOINT_QUERY_FAILED")
	}
	if runID != nil || before != nil || after != nil || requestHash != nil {
		if runID != nil && before != nil && after != nil && requestHash != nil && *runID == string(batch.RunID) &&
			*before == batch.BeforeCommit && *after == batch.AfterCommit && *requestHash == batch.RequestHash {
			return true, nil
		}
		return false, foundation.NewError(foundation.ErrorRetryableFailure, "GIT_CAPTURE_OUT_OF_ORDER", true, errors.New("another Git capture batch is in flight"))
	}
	if lastRunID != nil && lastBefore != nil && lastAfter != nil && lastRequestHash != nil &&
		*lastRunID == string(batch.RunID) && *lastBefore == batch.BeforeCommit && *lastAfter == batch.AfterCommit &&
		*lastRequestHash == batch.RequestHash && completed == batch.AfterCommit {
		return true, nil
	}
	if completed != batch.BeforeCommit {
		return false, foundation.NewError(foundation.ErrorRetryableFailure, "GIT_CAPTURE_OUT_OF_ORDER", true, errors.New("Git capture commit order is not contiguous"))
	}
	tag, err := tx.Exec(ctx, `UPDATE core.workspace_git_capture_checkpoint
		SET inflight_run_id=$2,inflight_before_oid=$3,inflight_after_oid=$4,inflight_request_hash=$5,
			version=version+1,updated_at=$6
		WHERE workspace_id=$1 AND completed_head_oid=$3 AND inflight_run_id IS NULL`, string(batch.WorkspaceID),
		string(batch.RunID), batch.BeforeCommit, batch.AfterCommit, batch.RequestHash, batch.ObservedAt.UTC())
	if err != nil {
		return false, classify(err, "GIT_CAPTURE_CHECKPOINT_UPDATE_FAILED")
	}
	if tag.RowsAffected() != 1 {
		return false, foundation.NewError(foundation.ErrorRetryableFailure, "GIT_CAPTURE_OUT_OF_ORDER", true, errors.New("Git capture checkpoint changed"))
	}
	return false, nil
}

func tombstoneGitCaptureSources(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, paths []string, observedAt time.Time) ([]foundation.ID, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `UPDATE core.source SET removed_at=$3
		WHERE workspace_id=$1 AND original_location=ANY($2::text[]) AND removed_at IS NULL
		RETURNING id::text`, string(workspaceID), paths, observedAt.UTC())
	if err != nil {
		return nil, classify(err, "GIT_CAPTURE_SOURCE_REMOVE_FAILED")
	}
	defer rows.Close()
	removed := make([]foundation.ID, 0, len(paths))
	for rows.Next() {
		var id foundation.ID
		if err := rows.Scan(&id); err != nil {
			return nil, classify(err, "GIT_CAPTURE_SOURCE_REMOVE_SCAN_FAILED")
		}
		removed = append(removed, id)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err, "GIT_CAPTURE_SOURCE_REMOVE_SCAN_FAILED")
	}
	// Replays must return all tombstoned Sources, not only rows changed now.
	lookup, err := tx.Query(ctx, `SELECT id::text FROM core.source
		WHERE workspace_id=$1 AND original_location=ANY($2::text[]) AND removed_at IS NOT NULL ORDER BY id`,
		string(workspaceID), paths)
	if err != nil {
		return nil, classify(err, "GIT_CAPTURE_SOURCE_REMOVE_QUERY_FAILED")
	}
	defer lookup.Close()
	removed = removed[:0]
	for lookup.Next() {
		var id foundation.ID
		if err := lookup.Scan(&id); err != nil {
			return nil, classify(err, "GIT_CAPTURE_SOURCE_REMOVE_SCAN_FAILED")
		}
		removed = append(removed, id)
	}
	if err := lookup.Err(); err != nil {
		return nil, classify(err, "GIT_CAPTURE_SOURCE_REMOVE_SCAN_FAILED")
	}
	sort.Slice(removed, func(left, right int) bool { return removed[left] < removed[right] })
	return removed, nil
}
