package postgres

import (
	"context"
	"errors"
	"strings"

	exportapp "github.com/CodeZen-Lizhi/zhixu/internal/export/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/export/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
)

// ExpireCandidates 原子归约一页数据库时间下已经到期的任务。
func (repository *Repository) ExpireCandidates(ctx context.Context, limit int) ([]domain.Job, error) {
	if repository == nil || isNilDependency(repository.db) {
		return nil, unavailable(errors.New("export repository is unavailable"))
	}
	if ctx == nil || limit < 1 || limit > exportapp.MaxListLimit {
		return nil, invalid(errors.New("export expiry sweep limit is invalid"))
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return nil, classify(err, true)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	rows, err := tx.Query(ctx, `SELECT `+selectColumns+` FROM ops.export_job
		WHERE status IN ('PENDING','RUNNING','SUCCEEDED','FAILED') AND expires_at<=clock_timestamp()
		ORDER BY expires_at,id LIMIT $1 FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return nil, classify(err, true)
	}
	candidates := make([]domain.Job, 0, limit)
	for rows.Next() {
		job, scanErr := scanJob(rows)
		if scanErr != nil {
			rows.Close()
			return nil, classify(scanErr, false)
		}
		candidates = append(candidates, job)
	}
	rowsErr := rows.Err()
	rows.Close()
	if rowsErr != nil {
		return nil, classify(rowsErr, true)
	}
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return nil, err
	}
	result := make([]domain.Job, 0, len(candidates))
	for _, candidate := range candidates {
		expiredJob, changed, expireErr := repository.expireLocked(ctx, tx, candidate, now)
		if expireErr != nil {
			return nil, expireErr
		}
		if changed {
			result = append(result, expiredJob)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, classify(err, true)
	}
	return result, nil
}

// CleanupCandidates 返回等待或重试物理文件删除的有界过期任务。
func (repository *Repository) CleanupCandidates(ctx context.Context, limit int) ([]domain.Job, error) {
	if repository == nil || isNilDependency(repository.db) {
		return nil, unavailable(errors.New("export repository is unavailable"))
	}
	if ctx == nil || limit < 1 || limit > exportapp.MaxListLimit {
		return nil, invalid(errors.New("export cleanup limit is invalid"))
	}
	rows, err := repository.db.Query(ctx, `SELECT `+selectColumns+` FROM ops.export_job
		WHERE status='EXPIRED' AND cleanup_status IN ('PENDING','FAILED') AND file_deleted_at IS NULL
		ORDER BY COALESCE(cleanup_updated_at,updated_at),id LIMIT $1`, limit)
	if err != nil {
		return nil, classify(err, true)
	}
	defer rows.Close()
	result := make([]domain.Job, 0, limit)
	for rows.Next() {
		job, scanErr := scanJob(rows)
		if scanErr != nil {
			return nil, classify(scanErr, false)
		}
		result = append(result, job)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err, true)
	}
	return result, nil
}

// RecordCleanup 以 version CAS 保存成功或可重试失败的清理事实。
func (repository *Repository) RecordCleanup(ctx context.Context, request exportapp.CleanupRequest) (domain.Job, error) {
	if repository == nil || isNilDependency(repository.db) {
		return domain.Job{}, unavailable(errors.New("export repository is unavailable"))
	}
	request.ErrorCode = boundedText(request.ErrorCode, 128)
	if ctx == nil || !validID(request.WorkspaceID) || !validID(request.JobID) || request.ExpectedVersion < 1 ||
		request.Succeeded == (request.ErrorCode != "") {
		return domain.Job{}, invalid(errors.New("export cleanup result is invalid"))
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return domain.Job{}, classify(err, true)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	job, err := scanJob(tx.QueryRow(ctx, `SELECT `+selectColumns+` FROM ops.export_job
		WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, string(request.WorkspaceID), string(request.JobID)))
	if err != nil {
		return domain.Job{}, classify(err, false)
	}
	expectedStatus := domain.CleanupFailed
	if request.Succeeded {
		expectedStatus = domain.CleanupSucceeded
	}
	if job.Version == request.ExpectedVersion+1 && job.CleanupStatus == expectedStatus &&
		(request.Succeeded || job.CleanupError == request.ErrorCode) {
		if err := tx.Commit(ctx); err != nil {
			return domain.Job{}, classify(err, true)
		}
		return job, nil
	}
	if job.Status != domain.StatusExpired || job.Version != request.ExpectedVersion ||
		job.CleanupStatus != domain.CleanupPending && job.CleanupStatus != domain.CleanupFailed {
		return domain.Job{}, versionConflict(errors.New("export cleanup candidate changed"))
	}
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return domain.Job{}, err
	}
	var cleanupError, deletedAt any = request.ErrorCode, nil
	stage := "cleanup_failed"
	if request.Succeeded {
		cleanupError, deletedAt, stage = nil, now, "cleanup_succeeded"
	}
	updated, err := scanJob(tx.QueryRow(ctx, `UPDATE ops.export_job SET
		version=version+1,updated_at=$3,cleanup_status=$4,
		cleanup_attempt_count=cleanup_attempt_count+1,cleanup_error=$5,
		cleanup_updated_at=$3,file_deleted_at=$6
		WHERE workspace_id=$1 AND id=$2 AND version=$7 AND status='EXPIRED'
		RETURNING `+selectColumns,
		string(request.WorkspaceID), string(request.JobID), now, string(expectedStatus), cleanupError, deletedAt, request.ExpectedVersion))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Job{}, versionConflict(errors.New("export cleanup compare-and-swap failed"))
		}
		return domain.Job{}, classify(err, true)
	}
	if err := repository.appendLifecycleEvent(ctx, tx, updated, stage); err != nil {
		return domain.Job{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Job{}, classify(err, true)
	}
	return updated, nil
}

// UnreferencedStaging 返回输入中未被任何 prepared Job 引用的路径，保留输入顺序。
func (repository *Repository) UnreferencedStaging(ctx context.Context, workspaceID foundation.ID, paths []string) ([]string, error) {
	if repository == nil || isNilDependency(repository.db) {
		return nil, unavailable(errors.New("export repository is unavailable"))
	}
	if ctx == nil || !validID(workspaceID) || len(paths) > exportapp.MaxListLimit {
		return nil, invalid(errors.New("export orphan lookup is invalid"))
	}
	if len(paths) == 0 {
		return []string{}, nil
	}
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if !validAnyStagingPath(path) {
			return nil, invalid(errors.New("export orphan path is invalid"))
		}
		if _, exists := seen[path]; exists {
			return nil, invalid(errors.New("export orphan paths are duplicated"))
		}
		seen[path] = struct{}{}
	}
	rows, err := repository.db.Query(ctx, `SELECT candidate.path
		FROM unnest($2::text[]) WITH ORDINALITY AS candidate(path,position)
		WHERE NOT EXISTS (
			SELECT 1 FROM ops.export_job job
			WHERE job.workspace_id=$1 AND job.prepared_staging_path=candidate.path
		)
		ORDER BY candidate.position`, string(workspaceID), paths)
	if err != nil {
		return nil, classify(err, true)
	}
	defer rows.Close()
	result := make([]string, 0, len(paths))
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, classify(err, false)
		}
		result = append(result, path)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err, true)
	}
	return result, nil
}

// OrphanSweepWorkspaces 返回有 Export 历史的稳定 Workspace ID 页面。
func (repository *Repository) OrphanSweepWorkspaces(ctx context.Context, afterWorkspaceID foundation.ID, limit int) ([]foundation.ID, error) {
	if repository == nil || isNilDependency(repository.db) {
		return nil, unavailable(errors.New("export repository is unavailable"))
	}
	if ctx == nil || afterWorkspaceID != "" && !validID(afterWorkspaceID) || limit < 1 || limit > exportapp.MaxListLimit+1 {
		return nil, invalid(errors.New("export orphan workspace query is invalid"))
	}
	rows, err := repository.db.Query(ctx, `SELECT DISTINCT workspace_id::text FROM ops.export_job
		WHERE ($1::uuid IS NULL OR workspace_id>$1::uuid) ORDER BY workspace_id::text LIMIT $2`,
		nullableWorkspace(afterWorkspaceID), limit)
	if err != nil {
		return nil, classify(err, true)
	}
	defer rows.Close()
	result := make([]foundation.ID, 0, limit)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, classify(err, false)
		}
		parsed, err := foundation.ParseID(raw)
		if err != nil {
			return nil, resultInvalid(errors.New("export workspace projection is invalid"))
		}
		result = append(result, parsed)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err, true)
	}
	return result, nil
}

func nullableWorkspace(value foundation.ID) any {
	if value == "" {
		return nil
	}
	return string(value)
}

func validAnyStagingPath(value string) bool {
	const prefix = ".knowledge/exports/.staging/"
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	name := strings.TrimPrefix(value, prefix)
	if len(name) < 36+1+32+len(".md.stage") || len(name) < 37 || name[36] != '-' {
		return false
	}
	if _, err := foundation.ParseID(name[:36]); err != nil {
		return false
	}
	remainder := name[37:]
	var suffix string
	switch {
	case strings.HasSuffix(remainder, ".md.stage"):
		suffix = ".md.stage"
	case strings.HasSuffix(remainder, ".json.stage"):
		suffix = ".json.stage"
	default:
		return false
	}
	nonce := strings.TrimSuffix(remainder, suffix)
	return len(nonce) == 32 && validLowerHex(nonce)
}
