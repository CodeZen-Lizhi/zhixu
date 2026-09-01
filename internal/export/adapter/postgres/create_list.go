package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	exportapp "github.com/CodeZen-Lizhi/zhixu/internal/export/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/export/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	exportAttachmentListFirstPageSQL = `SELECT ` + selectColumns + ` FROM ops.export_job
		WHERE workspace_id=$1 AND scope_kind='WORKSPACE_ATTACHMENTS' ORDER BY created_at DESC,id DESC LIMIT $2 FOR UPDATE`
	exportCollectionListFirstPageSQL = `SELECT ` + selectColumns + ` FROM ops.export_job
		WHERE workspace_id=$1 AND scope_kind='COLLECTION' AND collection_id=$2 ORDER BY created_at DESC,id DESC LIMIT $3 FOR UPDATE`
)

// Create 写入或精确重放任务。既有幂等事实先于当前 Collection 状态读取。
func (repository *Repository) Create(ctx context.Context, job domain.Job) (domain.Job, bool, error) {
	if repository == nil || isNilDependency(repository.db) {
		return domain.Job{}, false, unavailable(errors.New("export repository is unavailable"))
	}
	if ctx == nil {
		return domain.Job{}, false, invalid(errors.New("export create context is nil"))
	}
	if err := job.Validate(); err != nil || job.Status != domain.StatusPending || job.Version != 1 {
		return domain.Job{}, false, invalid(errors.New("export create job is invalid"))
	}
	queryDefinition, fields, err := encodeJobDefinition(job)
	if err != nil {
		return domain.Job{}, false, invalid(err)
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return domain.Job{}, false, classify(err, true)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1 || chr(31) || $2,0))`, string(job.WorkspaceID), job.IdempotencyKey); err != nil {
		return domain.Job{}, false, classify(err, true)
	}
	existing, err := scanJob(tx.QueryRow(ctx, `SELECT `+selectColumns+`
		FROM ops.export_job WHERE workspace_id=$1 AND idempotency_key=$2 FOR UPDATE`,
		string(job.WorkspaceID), job.IdempotencyKey))
	if err == nil {
		if !domain.SameRequest(existing, job) {
			return domain.Job{}, false, idempotencyConflict(errors.New("export idempotency key is bound to another request"))
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.Job{}, false, classify(err, true)
		}
		return existing, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.Job{}, false, classify(err, false)
	}
	if job.Scope.Kind == domain.ScopeCollection {
		var collectionVersion int64
		var queryHash string
		if err := tx.QueryRow(ctx, `SELECT version,query_hash FROM learning.smart_collection
			WHERE id=$1 AND workspace_id=$2 AND status='ACTIVE' FOR SHARE`,
			string(*job.Scope.CollectionID), string(job.WorkspaceID)).Scan(&collectionVersion, &queryHash); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return domain.Job{}, false, notFound(errors.New("active collection does not exist"))
			}
			return domain.Job{}, false, classify(err, true)
		}
		if collectionVersion != *job.Scope.CollectionVersion || queryHash != job.Scope.QueryHash {
			return domain.Job{}, false, idempotencyConflict(errors.New("export collection definition changed"))
		}
	} else if enabled, gateErr := attachmentCapabilityEnabledTx(ctx, tx); gateErr != nil {
		return domain.Job{}, false, gateErr
	} else if !enabled {
		return domain.Job{}, false, unavailable(errors.New("attachment export capability is disabled"))
	}
	persisted, err := scanJob(tx.QueryRow(ctx, `WITH db_time AS (SELECT clock_timestamp() AS now)
		INSERT INTO ops.export_job(
			id,workspace_id,kind,schema_version,query_definition,fields,status,idempotency_key,
			request_hash,request_ttl_seconds,version,expires_at,created_at,updated_at,
			scope_kind,collection_id,collection_version,query_hash,attachment_root_contract_version,redaction_policy,include_sensitive,
			permission_scope,requested_by,file_size,attempt_count,download_count,
			cleanup_status,cleanup_attempt_count
		)
		SELECT $1,$2,$3,$4,$5::jsonb,$6::jsonb,'PENDING',$7,$8,$9::bigint,1,
			db_time.now + make_interval(secs => $9::bigint::double precision),db_time.now,db_time.now,
			$10,$11,$12,$13,$14,$15,$16,$17,$18,0,0,0,'NOT_REQUIRED',0
		FROM db_time
		RETURNING `+selectColumns,
		string(job.ID), string(job.WorkspaceID), string(job.Kind), job.SchemaVersion, queryDefinition, fields,
		job.IdempotencyKey, job.RequestHash, job.RequestTTLSeconds,
		string(job.Scope.Kind), nullableIDPointer(job.Scope.CollectionID), nullableInt64(job.Scope.CollectionVersion), nullableText(job.Scope.QueryHash),
		nullableText(job.Scope.AttachmentRootContractVersion), string(job.Redaction), job.IncludeSensitive, job.PermissionScope, job.RequestedBy))
	if err != nil {
		return domain.Job{}, false, classify(err, false)
	}
	if !domain.SameRequest(persisted, job) {
		return domain.Job{}, false, resultInvalid(errors.New("created export differs from canonical request"))
	}
	if err := repository.appendLifecycleEvent(ctx, tx, persisted, "created"); err != nil {
		return domain.Job{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Job{}, false, classify(err, true)
	}
	return persisted, false, nil
}

// Get 按 Workspace 和 Job ID 读取，并使用数据库时间归约到期任务。
func (repository *Repository) Get(ctx context.Context, workspaceID, jobID foundation.ID) (domain.Job, error) {
	if repository == nil || isNilDependency(repository.db) {
		return domain.Job{}, unavailable(errors.New("export repository is unavailable"))
	}
	if ctx == nil || !validID(workspaceID) || !validID(jobID) {
		return domain.Job{}, invalid(errors.New("export lookup is invalid"))
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return domain.Job{}, classify(err, true)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	job, err := scanJob(tx.QueryRow(ctx, `SELECT `+selectColumns+`
		FROM ops.export_job WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, string(workspaceID), string(jobID)))
	if err != nil {
		return domain.Job{}, classify(err, false)
	}
	if expirable(job.Status) {
		now, nowErr := databaseNow(ctx, tx)
		if nowErr != nil {
			return domain.Job{}, nowErr
		}
		job, _, err = repository.expireLocked(ctx, tx, job, now)
		if err != nil {
			return domain.Job{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Job{}, classify(err, true)
	}
	return job, nil
}

// List 返回绑定 Workspace、Collection 与 limit 的 created_at/id 倒序 keyset 页面。
func (repository *Repository) List(ctx context.Context, query exportapp.ListQuery) (exportapp.ListPage, error) {
	if repository == nil || isNilDependency(repository.db) {
		return exportapp.ListPage{}, unavailable(errors.New("export repository is unavailable"))
	}
	if ctx == nil || !validID(query.WorkspaceID) || query.Limit < 1 || query.Limit > exportapp.MaxListLimit ||
		(query.ScopeKind != domain.ScopeCollection && query.ScopeKind != domain.ScopeWorkspaceAttachments) ||
		query.CollectionID != nil && !validID(*query.CollectionID) || (query.ScopeKind == domain.ScopeCollection) != (query.CollectionID != nil) {
		return exportapp.ListPage{}, invalid(errors.New("export list query is invalid"))
	}
	var cursor *cursorValue
	if query.Cursor != "" {
		decoded, err := decodeCursor(query.Cursor, query.WorkspaceID, query.ScopeKind, query.CollectionID, query.Limit)
		if err != nil {
			return exportapp.ListPage{}, invalid(err)
		}
		cursor = &decoded
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return exportapp.ListPage{}, classify(err, true)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	rows, err := queryListRows(ctx, tx, query, cursor)
	if err != nil {
		return exportapp.ListPage{}, classify(err, true)
	}
	jobs := make([]domain.Job, 0, query.Limit+1)
	for rows.Next() {
		job, scanErr := scanJob(rows)
		if scanErr != nil {
			rows.Close()
			return exportapp.ListPage{}, classify(scanErr, false)
		}
		jobs = append(jobs, job)
	}
	rowsErr := rows.Err()
	rows.Close()
	if rowsErr != nil {
		return exportapp.ListPage{}, classify(rowsErr, true)
	}
	hasMore := len(jobs) > query.Limit
	if hasMore {
		jobs = jobs[:query.Limit]
	}
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return exportapp.ListPage{}, err
	}
	for index, job := range jobs {
		updated, _, expireErr := repository.expireLocked(ctx, tx, job, now)
		if expireErr != nil {
			return exportapp.ListPage{}, expireErr
		}
		jobs[index] = updated
	}
	page := exportapp.ListPage{Items: jobs}
	if hasMore && len(jobs) > 0 {
		last := jobs[len(jobs)-1]
		page.NextCursor = encodeCursor(cursorValue{
			WorkspaceID: query.WorkspaceID, ScopeKind: query.ScopeKind, CollectionID: cloneID(query.CollectionID), Limit: query.Limit,
			CreatedAt: last.CreatedAt, ID: last.ID,
		})
	}
	if err := tx.Commit(ctx); err != nil {
		return exportapp.ListPage{}, classify(err, true)
	}
	return page, nil
}

func queryListRows(ctx context.Context, tx exportTransaction, query exportapp.ListQuery, cursor *cursorValue) (exportRows, error) {
	limit := query.Limit + 1
	switch {
	case query.ScopeKind == domain.ScopeWorkspaceAttachments && cursor == nil:
		return tx.Query(ctx, exportAttachmentListFirstPageSQL, string(query.WorkspaceID), limit)
	case query.ScopeKind == domain.ScopeCollection && cursor == nil:
		return tx.Query(ctx, exportCollectionListFirstPageSQL,
			string(query.WorkspaceID), string(*query.CollectionID), limit)
	case query.ScopeKind == domain.ScopeWorkspaceAttachments:
		return tx.Query(ctx, `SELECT `+selectColumns+` FROM ops.export_job
			WHERE workspace_id=$1 AND scope_kind='WORKSPACE_ATTACHMENTS' AND (created_at,id)<($2,$3)
			ORDER BY created_at DESC,id DESC LIMIT $4 FOR UPDATE`,
			string(query.WorkspaceID), cursor.CreatedAt, string(cursor.ID), limit)
	default:
		return tx.Query(ctx, `SELECT `+selectColumns+` FROM ops.export_job
			WHERE workspace_id=$1 AND scope_kind='COLLECTION' AND collection_id=$2 AND (created_at,id)<($3,$4)
			ORDER BY created_at DESC,id DESC LIMIT $5 FOR UPDATE`,
			string(query.WorkspaceID), string(*query.CollectionID), cursor.CreatedAt, string(cursor.ID), limit)
	}
}

func attachmentCapabilityEnabledTx(ctx context.Context, tx exportTransaction) (bool, error) {
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT enabled FROM ops.export_capability
		WHERE capability_key='workspace-attachments' AND contract_version='workspace-attachments/v1' FOR SHARE`).Scan(&enabled); err != nil {
		return false, classify(err, true)
	}
	return enabled, nil
}

func nullableIDPointer(value *foundation.ID) any {
	if value == nil {
		return nil
	}
	return string(*value)
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func expirable(status domain.Status) bool {
	return status == domain.StatusPending || status == domain.StatusRunning || status == domain.StatusSucceeded || status == domain.StatusFailed
}

func (repository *Repository) expireLocked(ctx context.Context, tx exportTransaction, job domain.Job, now time.Time) (domain.Job, bool, error) {
	if !expirable(job.Status) || job.ExpiresAt.After(now) {
		return job, false, nil
	}
	updated, err := scanJob(tx.QueryRow(ctx, `UPDATE ops.export_job SET
		status='EXPIRED',version=version+1,updated_at=$3,completed_at=COALESCE(completed_at,$3),
		lease_owner=NULL,lease_expires_at=NULL,error_code=NULL,error_message=NULL,
		cleanup_status='PENDING',cleanup_attempt_count=0,cleanup_error=NULL,
		cleanup_updated_at=NULL,file_deleted_at=NULL
		WHERE workspace_id=$1 AND id=$2 AND version=$4
		RETURNING `+selectColumns, string(job.WorkspaceID), string(job.ID), now, job.Version))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Job{}, false, versionConflict(errors.New("export expiry version changed"))
		}
		return domain.Job{}, false, classify(err, true)
	}
	if err := repository.appendLifecycleEvent(ctx, tx, updated, "expired"); err != nil {
		return domain.Job{}, false, err
	}
	return updated, true, nil
}

func (repository *Repository) expireIfNowDue(ctx context.Context, tx exportTransaction, job domain.Job) (domain.Job, bool, error) {
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return domain.Job{}, false, err
	}
	return repository.expireLocked(ctx, tx, job, now)
}
