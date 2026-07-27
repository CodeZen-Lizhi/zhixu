package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	exportapp "github.com/CodeZen-Lizhi/zhixu/internal/export/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/export/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
)

// Claim 以数据库时间获取 PENDING 或租约已到期 RUNNING 任务。
func (repository *Repository) Claim(ctx context.Context, workspaceID, jobID foundation.ID, owner string, lease time.Duration) (domain.Job, bool, error) {
	if repository == nil || isNilDependency(repository.db) {
		return domain.Job{}, false, unavailable(errors.New("export repository is unavailable"))
	}
	owner = strings.TrimSpace(owner)
	if ctx == nil || !validID(workspaceID) || !validID(jobID) || owner == "" || len(owner) > 128 ||
		strings.ContainsAny(owner, "\r\n\t/\x00") || lease <= 0 {
		return domain.Job{}, false, invalid(errors.New("export lease identity is invalid"))
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return domain.Job{}, false, classify(err, true)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	job, err := scanJob(tx.QueryRow(ctx, `SELECT `+selectColumns+` FROM ops.export_job
		WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, string(workspaceID), string(jobID)))
	if err != nil {
		return domain.Job{}, false, classify(err, false)
	}
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return domain.Job{}, false, err
	}
	if expirable(job.Status) && !job.ExpiresAt.After(now) {
		expiredJob, _, expireErr := repository.expireLocked(ctx, tx, job, now)
		if expireErr != nil {
			return domain.Job{}, false, expireErr
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.Job{}, false, classify(err, true)
		}
		return expiredJob, false, nil
	}
	claimable := job.Status == domain.StatusPending ||
		job.Status == domain.StatusRunning && job.LeaseExpiresAt != nil && !job.LeaseExpiresAt.After(now)
	if !claimable {
		if err := tx.Commit(ctx); err != nil {
			return domain.Job{}, false, classify(err, true)
		}
		return job, false, nil
	}
	claimed, err := scanJob(tx.QueryRow(ctx, `WITH db_time AS MATERIALIZED (SELECT clock_timestamp() AS now)
		UPDATE ops.export_job AS job SET
		status='RUNNING',version=job.version+1,lease_owner=$3,
		lease_expires_at=db_time.now + make_interval(secs => $4::double precision),
		attempt_count=job.attempt_count+1,started_at=COALESCE(job.started_at,db_time.now),updated_at=db_time.now,
		error_code=NULL,error_message=NULL
		FROM db_time
		WHERE job.workspace_id=$1 AND job.id=$2 AND job.version=$5
			AND job.expires_at > db_time.now
			AND (job.status='PENDING' OR (job.status='RUNNING' AND job.lease_expires_at <= db_time.now))
		RETURNING `+selectColumns,
		string(workspaceID), string(jobID), owner, lease.Seconds(), job.Version))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			expiredJob, expiredNow, expireErr := repository.expireIfNowDue(ctx, tx, job)
			if expireErr != nil {
				return domain.Job{}, false, expireErr
			}
			if expiredNow {
				if err := tx.Commit(ctx); err != nil {
					return domain.Job{}, false, classify(err, true)
				}
				return expiredJob, false, nil
			}
			return domain.Job{}, false, versionConflict(errors.New("export claim version changed"))
		}
		return domain.Job{}, false, classify(err, true)
	}
	if err := repository.appendLifecycleEvent(ctx, tx, claimed, "claimed"); err != nil {
		return domain.Job{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Job{}, false, classify(err, true)
	}
	return claimed, true, nil
}

// Prepare 将冻结快照与 create-only staging 一次性绑定到有效租约。
func (repository *Repository) Prepare(ctx context.Context, request exportapp.PrepareRequest) (domain.Job, error) {
	if repository == nil || isNilDependency(repository.db) {
		return domain.Job{}, unavailable(errors.New("export repository is unavailable"))
	}
	if err := validatePrepareRequest(ctx, request); err != nil {
		return domain.Job{}, err
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
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return domain.Job{}, err
	}
	if expirable(job.Status) && !job.ExpiresAt.After(now) {
		expiredJob, _, expireErr := repository.expireLocked(ctx, tx, job, now)
		if expireErr != nil {
			return domain.Job{}, expireErr
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.Job{}, classify(err, true)
		}
		return expiredJob, nil
	}
	if job.IsPrepared() && job.Status == domain.StatusRunning && job.Version == request.ExpectedVersion+1 &&
		job.LeaseOwner == request.LeaseOwner && job.LeaseExpiresAt != nil && job.LeaseExpiresAt.After(now) &&
		job.ReadModelRevision == request.ReadModelRevision && job.ExactCount != nil && *job.ExactCount == request.ExactCount &&
		samePreparedFile(job, request.PreparedFile) {
		if err := tx.Commit(ctx); err != nil {
			return domain.Job{}, classify(err, true)
		}
		return job, nil
	}
	if job.Status != domain.StatusRunning || job.Version != request.ExpectedVersion || job.LeaseOwner != request.LeaseOwner ||
		job.LeaseExpiresAt == nil || !job.LeaseExpiresAt.After(now) {
		return domain.Job{}, leaseLost(errors.New("export prepare lease was lost"))
	}
	if job.IsPrepared() {
		return domain.Job{}, resultInvalid(errors.New("export job already has another prepared result"))
	}
	if request.PreparedFile.FinalPath != expectedFinalPath(job) || !validStagingPath(job.ID, request.PreparedFile) {
		return domain.Job{}, resultInvalid(errors.New("export prepared paths do not match job identity"))
	}
	prepared, err := scanJob(tx.QueryRow(ctx, `WITH db_time AS MATERIALIZED (SELECT clock_timestamp() AS now)
		UPDATE ops.export_job AS job SET
		version=job.version+1,read_model_revision=$3,exact_count=$4,prepared_at=db_time.now,
		prepared_staging_path=$5,file_path=$6,file_hash=$7,file_size=$8,updated_at=db_time.now
		FROM db_time
		WHERE job.workspace_id=$1 AND job.id=$2 AND job.version=$9
			AND job.status='RUNNING' AND job.lease_owner=$10
			AND job.lease_expires_at > db_time.now AND job.expires_at > db_time.now
		RETURNING `+selectColumns,
		string(request.WorkspaceID), string(request.JobID), request.ReadModelRevision, request.ExactCount,
		request.PreparedFile.StagingPath, request.PreparedFile.FinalPath, request.PreparedFile.FileHash,
		request.PreparedFile.FileSize, request.ExpectedVersion, request.LeaseOwner))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			expiredJob, expiredNow, expireErr := repository.expireIfNowDue(ctx, tx, job)
			if expireErr != nil {
				return domain.Job{}, expireErr
			}
			if expiredNow {
				if err := tx.Commit(ctx); err != nil {
					return domain.Job{}, classify(err, true)
				}
				return expiredJob, nil
			}
			return domain.Job{}, leaseLost(errors.New("export prepare compare-and-swap failed"))
		}
		return domain.Job{}, classify(err, true)
	}
	if err := repository.appendLifecycleEvent(ctx, tx, prepared, "prepared"); err != nil {
		return domain.Job{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Job{}, classify(err, true)
	}
	return prepared, nil
}

// Complete 在有效 DB-time 租约内归约已经提升的固定结果。
func (repository *Repository) Complete(ctx context.Context, request exportapp.CompleteRequest) (domain.Job, error) {
	if repository == nil || isNilDependency(repository.db) {
		return domain.Job{}, unavailable(errors.New("export repository is unavailable"))
	}
	if err := validateCompletionRequest(ctx, request); err != nil {
		return domain.Job{}, err
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
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return domain.Job{}, err
	}
	if expirable(job.Status) && !job.ExpiresAt.After(now) {
		expiredJob, _, expireErr := repository.expireLocked(ctx, tx, job, now)
		if expireErr != nil {
			return domain.Job{}, expireErr
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.Job{}, classify(err, true)
		}
		return expiredJob, nil
	}
	if job.Status == domain.StatusSucceeded && job.Version == request.ExpectedVersion+1 && samePreparedFile(job, request.PreparedFile) {
		if err := tx.Commit(ctx); err != nil {
			return domain.Job{}, classify(err, true)
		}
		return job, nil
	}
	if job.Status != domain.StatusRunning || job.Version != request.ExpectedVersion || job.LeaseOwner != request.LeaseOwner ||
		job.LeaseExpiresAt == nil || !job.LeaseExpiresAt.After(now) {
		return domain.Job{}, leaseLost(errors.New("export completion lease was lost"))
	}
	if !job.IsPrepared() || !samePreparedFile(job, request.PreparedFile) {
		return domain.Job{}, resultInvalid(errors.New("export completion result differs from prepared binding"))
	}
	completed, err := scanJob(tx.QueryRow(ctx, `WITH db_time AS MATERIALIZED (SELECT clock_timestamp() AS now)
		UPDATE ops.export_job AS job SET
		status='SUCCEEDED',version=job.version+1,updated_at=db_time.now,completed_at=db_time.now,
		lease_owner=NULL,lease_expires_at=NULL,error_code=NULL,error_message=NULL
		FROM db_time
		WHERE job.workspace_id=$1 AND job.id=$2 AND job.version=$3
			AND job.status='RUNNING' AND job.lease_owner=$4
			AND job.lease_expires_at > db_time.now AND job.expires_at > db_time.now
		RETURNING `+selectColumns,
		string(request.WorkspaceID), string(request.JobID), request.ExpectedVersion, request.LeaseOwner))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			expiredJob, expiredNow, expireErr := repository.expireIfNowDue(ctx, tx, job)
			if expireErr != nil {
				return domain.Job{}, expireErr
			}
			if expiredNow {
				if err := tx.Commit(ctx); err != nil {
					return domain.Job{}, classify(err, true)
				}
				return expiredJob, nil
			}
			return domain.Job{}, leaseLost(errors.New("export completion compare-and-swap failed"))
		}
		return domain.Job{}, classify(err, true)
	}
	if err := repository.appendLifecycleEvent(ctx, tx, completed, "completed"); err != nil {
		return domain.Job{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Job{}, classify(err, true)
	}
	return completed, nil
}

// Fail 在有效 DB-time 租约内归约失败；prepared 可重试结果必须等待租约接管。
func (repository *Repository) Fail(ctx context.Context, request exportapp.FailRequest) (domain.Job, error) {
	if repository == nil || isNilDependency(repository.db) {
		return domain.Job{}, unavailable(errors.New("export repository is unavailable"))
	}
	request.ErrorCode = boundedText(request.ErrorCode, 128)
	request.ErrorMessage = boundedText(request.ErrorMessage, 512)
	if ctx == nil || !validID(request.WorkspaceID) || !validID(request.JobID) || request.ExpectedVersion < 1 ||
		strings.TrimSpace(request.LeaseOwner) == "" || request.ErrorCode == "" || request.ErrorMessage == "" {
		return domain.Job{}, invalid(errors.New("export failure binding is invalid"))
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
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return domain.Job{}, err
	}
	if expirable(job.Status) && !job.ExpiresAt.After(now) {
		expiredJob, _, expireErr := repository.expireLocked(ctx, tx, job, now)
		if expireErr != nil {
			return domain.Job{}, expireErr
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.Job{}, classify(err, true)
		}
		return expiredJob, nil
	}
	if !request.Retryable && job.Status == domain.StatusFailed && job.Version == request.ExpectedVersion+1 &&
		job.ErrorCode == request.ErrorCode && job.ErrorMessage == request.ErrorMessage {
		if err := tx.Commit(ctx); err != nil {
			return domain.Job{}, classify(err, true)
		}
		return job, nil
	}
	if request.Retryable && job.Status == domain.StatusPending && job.Version == request.ExpectedVersion+1 {
		if err := tx.Commit(ctx); err != nil {
			return domain.Job{}, classify(err, true)
		}
		return job, nil
	}
	if job.Status != domain.StatusRunning || job.Version != request.ExpectedVersion || job.LeaseOwner != request.LeaseOwner ||
		job.LeaseExpiresAt == nil || !job.LeaseExpiresAt.After(now) {
		return domain.Job{}, leaseLost(errors.New("export failure lease was lost"))
	}
	if job.IsPrepared() && request.Retryable {
		return domain.Job{}, leaseLost(errors.New("prepared export retry must retain its running lease"))
	}
	status := domain.StatusFailed
	stage := "failed"
	var errorCode, errorMessage any = request.ErrorCode, request.ErrorMessage
	if request.Retryable {
		status = domain.StatusPending
		stage = "retry_pending"
		errorCode, errorMessage = nil, nil
	}
	failed, err := scanJob(tx.QueryRow(ctx, `WITH db_time AS MATERIALIZED (SELECT clock_timestamp() AS now)
		UPDATE ops.export_job AS job SET
		status=$3,version=job.version+1,updated_at=db_time.now,
		completed_at=CASE WHEN $4::boolean THEN NULL ELSE db_time.now END,
		lease_owner=NULL,lease_expires_at=NULL,error_code=$5,error_message=$6
		FROM db_time
		WHERE job.workspace_id=$1 AND job.id=$2 AND job.version=$7
			AND job.status='RUNNING' AND job.lease_owner=$8
			AND job.lease_expires_at > db_time.now AND job.expires_at > db_time.now
		RETURNING `+selectColumns,
		string(request.WorkspaceID), string(request.JobID), string(status), request.Retryable,
		errorCode, errorMessage, request.ExpectedVersion, request.LeaseOwner))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			expiredJob, expiredNow, expireErr := repository.expireIfNowDue(ctx, tx, job)
			if expireErr != nil {
				return domain.Job{}, expireErr
			}
			if expiredNow {
				if err := tx.Commit(ctx); err != nil {
					return domain.Job{}, classify(err, true)
				}
				return expiredJob, nil
			}
			return domain.Job{}, leaseLost(errors.New("export failure compare-and-swap failed"))
		}
		return domain.Job{}, classify(err, true)
	}
	if err := repository.appendLifecycleEvent(ctx, tx, failed, stage); err != nil {
		return domain.Job{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Job{}, classify(err, true)
	}
	return failed, nil
}

// Expire 使用数据库当前时间归约一个任务；未到期时只返回当前事实。
func (repository *Repository) Expire(ctx context.Context, workspaceID, jobID foundation.ID) (domain.Job, error) {
	if repository == nil || isNilDependency(repository.db) {
		return domain.Job{}, unavailable(errors.New("export repository is unavailable"))
	}
	if ctx == nil || !validID(workspaceID) || !validID(jobID) {
		return domain.Job{}, invalid(errors.New("export expiry identity is invalid"))
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return domain.Job{}, classify(err, true)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	job, err := scanJob(tx.QueryRow(ctx, `SELECT `+selectColumns+` FROM ops.export_job
		WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, string(workspaceID), string(jobID)))
	if err != nil {
		return domain.Job{}, classify(err, false)
	}
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return domain.Job{}, err
	}
	job, _, err = repository.expireLocked(ctx, tx, job, now)
	if err != nil {
		return domain.Job{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Job{}, classify(err, true)
	}
	return job, nil
}

// RecoveryCandidates 返回数据库时间下可重新投递的有界任务集合。
func (repository *Repository) RecoveryCandidates(ctx context.Context, limit int) ([]domain.Job, error) {
	if repository == nil || isNilDependency(repository.db) {
		return nil, unavailable(errors.New("export repository is unavailable"))
	}
	if ctx == nil || limit < 1 || limit > exportapp.MaxListLimit {
		return nil, invalid(errors.New("export recovery limit is invalid"))
	}
	rows, err := repository.db.Query(ctx, `SELECT `+selectColumns+` FROM ops.export_job
		WHERE expires_at>clock_timestamp()
		  AND (status='PENDING' OR (status='RUNNING' AND lease_expires_at<=clock_timestamp()))
		ORDER BY created_at,id LIMIT $1`, limit)
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

func validatePrepareRequest(ctx context.Context, request exportapp.PrepareRequest) error {
	if ctx == nil || !validID(request.WorkspaceID) || !validID(request.JobID) || request.ExpectedVersion < 1 ||
		strings.TrimSpace(request.LeaseOwner) == "" || len(request.LeaseOwner) > 128 ||
		!validHash(request.ReadModelRevision) || request.ExactCount < 0 || request.ExactCount > exportapp.MaxSnapshotItems ||
		!validHash(request.PreparedFile.FileHash) || request.PreparedFile.FileSize < 0 {
		return invalid(errors.New("export prepare request is invalid"))
	}
	return nil
}

func validateCompletionRequest(ctx context.Context, request exportapp.CompleteRequest) error {
	if ctx == nil || !validID(request.WorkspaceID) || !validID(request.JobID) || request.ExpectedVersion < 1 ||
		strings.TrimSpace(request.LeaseOwner) == "" || len(request.LeaseOwner) > 128 ||
		!validHash(request.PreparedFile.FileHash) || request.PreparedFile.FileSize < 0 ||
		request.PreparedFile.StagingPath == "" || request.PreparedFile.FinalPath == "" {
		return invalid(errors.New("export completion request is invalid"))
	}
	return nil
}

func expectedFinalPath(job domain.Job) string {
	extension := ".json"
	if job.Kind == domain.KindMarkdown {
		extension = ".md"
	}
	return ".knowledge/exports/" + string(job.ID) + extension
}

func validStagingPath(jobID foundation.ID, file exportapp.PreparedFile) bool {
	extension := ".json"
	if strings.HasSuffix(file.FinalPath, ".md") {
		extension = ".md"
	}
	prefix := ".knowledge/exports/.staging/" + string(jobID) + "-"
	suffix := extension + ".stage"
	if !strings.HasPrefix(file.StagingPath, prefix) || !strings.HasSuffix(file.StagingPath, suffix) {
		return false
	}
	nonce := strings.TrimSuffix(strings.TrimPrefix(file.StagingPath, prefix), suffix)
	return len(nonce) == 32 && validLowerHex(nonce)
}

func validLowerHex(value string) bool {
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return value != ""
}

func samePreparedFile(job domain.Job, file exportapp.PreparedFile) bool {
	return job.PreparedStagingPath == file.StagingPath && job.FilePath == file.FinalPath &&
		job.FileHash == file.FileHash && job.FileSize == file.FileSize
}
