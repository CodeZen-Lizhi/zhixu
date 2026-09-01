package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	auditdomain "github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	exportapp "github.com/CodeZen-Lizhi/zhixu/internal/export/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/export/domain"
)

// RecordDownload 原子增加统计并追加当前 actor 的 append-only Audit。
func (repository *Repository) RecordDownload(ctx context.Context, request exportapp.DownloadRecord) (domain.Job, error) {
	if repository == nil || isNilDependency(repository.db) || isNilDependency(repository.audit) {
		return domain.Job{}, unavailable(errors.New("export download audit repository is unavailable"))
	}
	if ctx == nil || !validID(request.WorkspaceID) || !validID(request.JobID) || !validID(request.AuditEventID) ||
		(request.ScopeKind != domain.ScopeCollection && request.ScopeKind != domain.ScopeWorkspaceAttachments) ||
		(request.ScopeKind == domain.ScopeCollection && request.EntryCount != nil) ||
		(request.ScopeKind == domain.ScopeWorkspaceAttachments && (request.EntryCount == nil || *request.EntryCount < 0 || *request.EntryCount > 10_000)) ||
		!validHash(request.FileHash) || request.FileSize < 0 {
		return domain.Job{}, invalid(errors.New("export download binding is invalid"))
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
		_, _, expireErr := repository.expireLocked(ctx, tx, job, now)
		if expireErr != nil {
			return domain.Job{}, expireErr
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.Job{}, classify(err, true)
		}
		return domain.Job{}, expired(errors.New("export result has expired"))
	}
	if job.Status != domain.StatusSucceeded {
		return domain.Job{}, versionConflict(errors.New("export result is not ready"))
	}
	if job.Scope.Kind != request.ScopeKind || !sameOptionalInt64(job.EntryCount, request.EntryCount) ||
		job.FileHash != request.FileHash || job.FileSize != request.FileSize {
		return domain.Job{}, resultInvalid(errors.New("export download differs from prepared result"))
	}
	updated, err := scanJob(tx.QueryRow(ctx, `WITH db_time AS MATERIALIZED (SELECT clock_timestamp() AS now)
		UPDATE ops.export_job AS job SET
		version=job.version+1,download_count=job.download_count+1,
		last_downloaded_at=db_time.now,updated_at=db_time.now
		FROM db_time
		WHERE job.workspace_id=$1 AND job.id=$2 AND job.version=$3
			AND job.status='SUCCEEDED' AND job.expires_at > db_time.now
		RETURNING `+selectColumns,
		string(request.WorkspaceID), string(request.JobID), job.Version))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			_, expiredNow, expireErr := repository.expireIfNowDue(ctx, tx, job)
			if expireErr != nil {
				return domain.Job{}, expireErr
			}
			if expiredNow {
				if err := tx.Commit(ctx); err != nil {
					return domain.Job{}, classify(err, true)
				}
				return domain.Job{}, expired(errors.New("export result has expired"))
			}
			return domain.Job{}, versionConflict(errors.New("export download compare-and-swap failed"))
		}
		return domain.Job{}, classify(err, true)
	}
	if updated.LastDownloadedAt == nil {
		return domain.Job{}, resultInvalid(errors.New("export download timestamp is missing"))
	}
	auditEvent, err := downloadAuditEvent(request, *updated.LastDownloadedAt)
	if err != nil {
		return domain.Job{}, invalid(err)
	}
	_, replayed, err := repository.audit.AppendTx(ctx, tx.SideFactTransaction(), auditEvent)
	if err != nil {
		return domain.Job{}, err
	}
	if replayed {
		return domain.Job{}, resultInvalid(errors.New("export download audit identity was already used"))
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Job{}, classify(err, true)
	}
	return updated, nil
}

func downloadAuditEvent(request exportapp.DownloadRecord, occurredAt time.Time) (auditdomain.Event, error) {
	workspaceID := request.WorkspaceID
	correlation, err := json.Marshal(map[string]any{"export_id": string(request.JobID)})
	if err != nil {
		return auditdomain.Event{}, err
	}
	metadataFields := map[string]any{
		"export_id": string(request.JobID), "file_hash": request.FileHash,
		"file_size": request.FileSize, "scope_kind": string(request.ScopeKind), "server_outcome": "prepared_for_return",
	}
	if request.EntryCount != nil {
		metadataFields["entry_count"] = *request.EntryCount
	}
	metadata, err := json.Marshal(metadataFields)
	if err != nil {
		return auditdomain.Event{}, err
	}
	return auditdomain.NewEvent(auditdomain.Event{
		ID: request.AuditEventID, WorkspaceID: &workspaceID,
		ActorType: request.Actor.Type, ActorRef: request.Actor.Ref,
		Action: "export.download", ResourceType: "export_job", ResourceRef: "export_job:" + string(request.JobID),
		Outcome:        auditdomain.OutcomeSucceeded,
		IdempotencyKey: "export.download:" + string(request.JobID) + ":" + string(request.AuditEventID),
		Correlation:    correlation, Metadata: metadata, SchemaVersion: auditdomain.SchemaVersion,
		OccurredAt: occurredAt.UTC().Truncate(time.Microsecond),
	})
}
