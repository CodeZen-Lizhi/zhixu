package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ClaimStart 原子领取一个到期 Outbox，保留 SKIP LOCKED 与最大尝试次数限制。
func (repository *GORMRepository) ClaimStart(ctx context.Context, owner string, lease time.Duration) (organizingapp.StartOutboxLease, bool, error) {
	if err := repository.validateReady(ctx); err != nil {
		return organizingapp.StartOutboxLease{}, false, err
	}
	owner = strings.TrimSpace(owner)
	if owner == "" || len(owner) > 256 || strings.ContainsAny(owner, "\r\n\x00") ||
		lease < time.Microsecond || lease > maxStartLease {
		return organizingapp.StartOutboxLease{}, false, invalid(errors.New("organizing start claim is invalid"))
	}
	var claimed organizingapp.StartOutboxLease
	err := repository.database.WithContext(ctx).Raw(`WITH exhausted AS (
		UPDATE organizing.workflow_start_outbox
		SET status='POISONED',lease_owner=NULL,lease_until=NULL,
			last_error_code='MAX_ATTEMPTS',version=version+1,
			updated_at=clock_timestamp(),poisoned_at=clock_timestamp()
		WHERE status='PENDING' AND attempt_count=1000
		  AND (lease_until IS NULL OR lease_until<=clock_timestamp())
	), candidate AS (
		SELECT id FROM organizing.workflow_start_outbox
		WHERE status='PENDING' AND available_at<=clock_timestamp()
		  AND attempt_count<$3
		  AND (lease_until IS NULL OR lease_until<=clock_timestamp())
		ORDER BY available_at,id
		FOR UPDATE SKIP LOCKED
		LIMIT 1
	)
	UPDATE organizing.workflow_start_outbox AS outbox
	SET lease_owner=$1,
		lease_until=clock_timestamp()+($2::bigint*interval '1 microsecond'),
		attempt_count=outbox.attempt_count+1,
		version=outbox.version+1,
		updated_at=clock_timestamp()
	FROM candidate
		WHERE outbox.id=candidate.id
		RETURNING outbox.id::text,outbox.workspace_id::text,outbox.snapshot_id::text,
			outbox.template_revision_id::text,outbox.template_kind,outbox.lease_owner,
			outbox.attempt_count,outbox.version,outbox.lease_until`, owner, lease.Microseconds(), maxStartAttempts).Row().
		Scan(&claimed.ID, &claimed.WorkspaceID, &claimed.SnapshotID, &claimed.TemplateRevisionID,
			&claimed.TemplateKind, &claimed.Owner, &claimed.AttemptCount, &claimed.Version, &claimed.LeaseUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return organizingapp.StartOutboxLease{}, false, nil
	}
	if err != nil {
		return organizingapp.StartOutboxLease{}, false, classifyGORM(err)
	}
	claimed.LeaseUntil = claimed.LeaseUntil.UTC()
	if err := validateStartLease(claimed); err != nil {
		return organizingapp.StartOutboxLease{}, false, inconsistent(err)
	}
	return claimed, true, nil
}

// RetryStart 只释放精确 lease，并在尝试耗尽时转为 POISONED。
func (repository *GORMRepository) RetryStart(ctx context.Context, record organizingapp.RetryStartRecord) error {
	if err := repository.validateReady(ctx); err != nil {
		return err
	}
	if err := validateStartLease(record.Lease); err != nil || !validRuntimeErrorCode(record.ErrorCode) || record.Delay < 0 || record.Delay > maxStartLease {
		return invalid(errors.New("organizing start retry is invalid"))
	}
	result := repository.database.WithContext(ctx).Exec(`UPDATE organizing.workflow_start_outbox
	SET status=CASE WHEN attempt_count>=$7 THEN 'POISONED' ELSE 'PENDING' END,
		lease_owner=NULL,lease_until=NULL,last_error_code=$1,
		available_at=CASE WHEN attempt_count>=$7 THEN available_at
			ELSE clock_timestamp()+($2::bigint*interval '1 microsecond') END,
		version=version+1,updated_at=clock_timestamp(),
		poisoned_at=CASE WHEN attempt_count>=$7 THEN clock_timestamp() ELSE NULL END
	WHERE id=$3 AND workspace_id=$4 AND status='PENDING' AND lease_owner=$5
	  AND version=$6 AND lease_until>clock_timestamp()`, record.ErrorCode, record.Delay.Microseconds(),
		string(record.Lease.ID), string(record.Lease.WorkspaceID), record.Lease.Owner, record.Lease.Version,
		maxStartAttempts)
	if result.Error != nil {
		return classifyGORM(result.Error)
	}
	if result.RowsAffected != 1 {
		return leaseLost(errors.New("organizing start outbox lease was lost"))
	}
	return nil
}

// PoisonStart 终止精确 lease 对应的不可重试启动。
func (repository *GORMRepository) PoisonStart(ctx context.Context, record organizingapp.PoisonStartRecord) error {
	if err := repository.validateReady(ctx); err != nil {
		return err
	}
	if err := validateStartLease(record.Lease); err != nil || !validRuntimeErrorCode(record.ErrorCode) {
		return invalid(errors.New("organizing start poison request is invalid"))
	}
	result := repository.database.WithContext(ctx).Exec(`UPDATE organizing.workflow_start_outbox
	SET status='POISONED',lease_owner=NULL,lease_until=NULL,last_error_code=$1,
		version=version+1,updated_at=clock_timestamp(),poisoned_at=clock_timestamp()
	WHERE id=$2 AND workspace_id=$3 AND status='PENDING' AND lease_owner=$4
	  AND version=$5 AND lease_until>clock_timestamp()`, record.ErrorCode, string(record.Lease.ID),
		string(record.Lease.WorkspaceID), record.Lease.Owner, record.Lease.Version)
	if result.Error != nil {
		return classifyGORM(result.Error)
	}
	if result.RowsAffected != 1 {
		return leaseLost(errors.New("organizing start outbox lease was lost"))
	}
	return nil
}

// GetRunProjection 一次读取 Workspace 范围内的启动、绑定与结果快照。
func (repository *GORMRepository) GetRunProjection(ctx context.Context, workspaceID, snapshotID foundation.ID) (organizingapp.RunProjection, error) {
	if err := repository.validateReady(ctx); err != nil {
		return organizingapp.RunProjection{}, err
	}
	if !validID(workspaceID) || !validID(snapshotID) {
		return organizingapp.RunProjection{}, invalid(errors.New("organizing run projection lookup is invalid"))
	}
	var (
		projection organizingapp.RunProjection
		binding    domain.RunBinding
		result     domain.RunResult
		bindingID  string
		resultID   string
	)
	err := repository.database.WithContext(ctx).Raw(`SELECT
		o.workspace_id::text,o.snapshot_id::text,o.status,o.attempt_count,
		COALESCE(o.last_error_code,''),o.updated_at,
		COALESCE(b.id::text,''),COALESCE(b.workspace_id::text,''),COALESCE(b.snapshot_id::text,''),
		COALESCE(b.workflow_run_id::text,''),COALESCE(b.definition_key,''),COALESCE(b.definition_version,0),
		COALESCE(b.created_at,'epoch'::timestamptz),
		COALESCE(r.id::text,''),COALESCE(r.workspace_id::text,''),COALESCE(r.run_binding_id::text,''),
		COALESCE(r.snapshot_id::text,''),COALESCE(r.workflow_run_id::text,''),COALESCE(r.node_run_id::text,''),
		COALESCE(r.result_kind,''),COALESCE(r.result_ref::text,''),COALESCE(r.result_hash,''),
		COALESCE(r.created_at,'epoch'::timestamptz)
	FROM organizing.workflow_start_outbox AS o
	LEFT JOIN organizing.run_binding AS b
	  ON b.id=o.run_binding_id AND b.workspace_id=o.workspace_id AND b.snapshot_id=o.snapshot_id
	LEFT JOIN organizing.run_result AS r
	  ON r.run_binding_id=b.id AND r.workspace_id=b.workspace_id AND r.snapshot_id=b.snapshot_id
	WHERE o.workspace_id=$1 AND o.snapshot_id=$2`, string(workspaceID), string(snapshotID)).Row().Scan(
		&projection.WorkspaceID, &projection.SnapshotID, &projection.Status, &projection.AttemptCount,
		&projection.LastErrorCode, &projection.UpdatedAt,
		&bindingID, &binding.WorkspaceID, &binding.SnapshotID, &binding.WorkflowRunID,
		&binding.DefinitionKey, &binding.DefinitionVersion, &binding.CreatedAt,
		&resultID, &result.WorkspaceID, &result.RunBindingID, &result.SnapshotID, &result.WorkflowRunID,
		&result.NodeRunID, &result.Kind, &result.ResultRef, &result.ResultHash, &result.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return organizingapp.RunProjection{}, notFound(err)
	}
	if err != nil {
		return organizingapp.RunProjection{}, classifyGORM(err)
	}
	projection.UpdatedAt = projection.UpdatedAt.UTC()
	if bindingID != "" {
		binding.ID = foundation.ID(bindingID)
		binding.CreatedAt = binding.CreatedAt.UTC()
		projection.Binding = &binding
	}
	if resultID != "" {
		result.ID = foundation.ID(resultID)
		result.CreatedAt = result.CreatedAt.UTC()
		projection.Result = &result
	}
	if !projection.Valid() {
		return organizingapp.RunProjection{}, inconsistent(errors.New("stored organizing run projection is invalid"))
	}
	return projection, nil
}

// CompleteStart 在持有的 Outbox lease 中绑定已启动的 Workflow。
func (repository *GORMRepository) CompleteStart(ctx context.Context, record organizingapp.CompleteStartRecord) (domain.RunBinding, bool, error) {
	if err := repository.validateReady(ctx); err != nil {
		return domain.RunBinding{}, false, err
	}
	if err := validateStartLease(record.Lease); err != nil || !validID(record.BindingID) || !validID(record.WorkflowRunID) ||
		strings.TrimSpace(record.DefinitionKey) == "" || len(record.DefinitionKey) > 128 || strings.ContainsAny(record.DefinitionKey, "\r\n\x00") ||
		record.DefinitionVersion < 1 || record.StartedAt.IsZero() {
		return domain.RunBinding{}, false, invalid(errors.New("organizing start completion is invalid"))
	}
	var result domain.RunBinding
	var replayed bool
	err := repository.within(ctx, foundation.TransactionOptions{}, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		var err error
		result, replayed, err = gormCompleteStart(tx, record)
		return err
	})
	if err != nil {
		return domain.RunBinding{}, false, err
	}
	return result, replayed, nil
}

func gormCompleteStart(tx *gorm.DB, record organizingapp.CompleteStartRecord) (domain.RunBinding, bool, error) {
	binding := domain.RunBinding{ID: record.BindingID, WorkspaceID: record.Lease.WorkspaceID, SnapshotID: record.Lease.SnapshotID,
		WorkflowRunID: record.WorkflowRunID, DefinitionKey: strings.TrimSpace(record.DefinitionKey), DefinitionVersion: record.DefinitionVersion,
		CreatedAt: canonicalTime(record.StartedAt)}
	if err := binding.Validate(); err != nil {
		return domain.RunBinding{}, false, invalid(err)
	}
	existing, found, err := gormLoadRunBinding(tx, binding.WorkspaceID, binding.SnapshotID, true)
	if err != nil {
		return domain.RunBinding{}, false, err
	}
	if found {
		if existing.WorkspaceID != binding.WorkspaceID || existing.SnapshotID != binding.SnapshotID || existing.WorkflowRunID != binding.WorkflowRunID ||
			existing.DefinitionKey != binding.DefinitionKey || existing.DefinitionVersion != binding.DefinitionVersion {
			return domain.RunBinding{}, false, idempotencyConflict(errors.New("organizing snapshot is bound to a different workflow run"))
		}
		return existing, true, nil
	}
	matched, err := gormMatchStartLease(tx, record.Lease)
	if err != nil {
		return domain.RunBinding{}, false, err
	}
	if !matched {
		return domain.RunBinding{}, false, leaseLost(errors.New("organizing start outbox lease was lost"))
	}
	key, version := definitionForTemplateKind(record.Lease.TemplateKind)
	if binding.DefinitionKey != key || binding.DefinitionVersion != version {
		return domain.RunBinding{}, false, invalid(errors.New("organizing workflow definition does not match the frozen template kind"))
	}
	model := runBindingModel{ID: string(binding.ID), WorkspaceID: string(binding.WorkspaceID), SnapshotID: string(binding.SnapshotID),
		WorkflowRunID: string(binding.WorkflowRunID), DefinitionKey: binding.DefinitionKey, DefinitionVersion: binding.DefinitionVersion, CreatedAt: binding.CreatedAt.UTC()}
	if err := tx.Create(&model).Error; err != nil {
		return domain.RunBinding{}, false, classifyGORM(err)
	}
	updated := tx.Model(&startOutboxModel{}).Where("id = ? AND workspace_id = ? AND status = 'PENDING' AND lease_owner = ? AND version = ? AND lease_until > clock_timestamp()",
		string(record.Lease.ID), string(record.Lease.WorkspaceID), record.Lease.Owner, record.Lease.Version).
		Updates(map[string]any{"status": "STARTED", "lease_owner": nil, "lease_until": nil, "run_binding_id": string(binding.ID),
			"version": gorm.Expr("version+1"), "updated_at": binding.CreatedAt.UTC(), "started_at": binding.CreatedAt.UTC()})
	if updated.Error != nil {
		return domain.RunBinding{}, false, classifyGORM(updated.Error)
	}
	if updated.RowsAffected != 1 {
		return domain.RunBinding{}, false, leaseLost(errors.New("organizing start outbox lease was lost"))
	}
	return binding, false, nil
}

func gormMatchStartLease(tx *gorm.DB, lease organizingapp.StartOutboxLease) (bool, error) {
	var model startOutboxModel
	err := tx.Select("id").Where(`id = ? AND workspace_id = ? AND snapshot_id = ? AND template_revision_id = ?
		AND template_kind = ? AND status='PENDING' AND lease_owner = ? AND version = ? AND lease_until > clock_timestamp()`,
		string(lease.ID), string(lease.WorkspaceID), string(lease.SnapshotID), string(lease.TemplateRevisionID), string(lease.TemplateKind), lease.Owner, lease.Version).
		Clauses(clause.Locking{Strength: "UPDATE"}).Take(&model).Error
	if gormNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, classifyGORM(err)
	}
	return model.ID == string(lease.ID), nil
}

func gormLoadRunBinding(tx *gorm.DB, workspaceID, snapshotID foundation.ID, lock bool) (domain.RunBinding, bool, error) {
	query := tx.Select(gormRunBindingColumns).Where("workspace_id = ? AND snapshot_id = ?", string(workspaceID), string(snapshotID))
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var model runBindingModel
	if err := query.Take(&model).Error; err != nil {
		if gormNoRows(err) {
			return domain.RunBinding{}, false, nil
		}
		return domain.RunBinding{}, false, classifyGORM(err)
	}
	binding := model.binding()
	if err := binding.Validate(); err != nil {
		return domain.RunBinding{}, false, inconsistent(err)
	}
	return binding, true, nil
}

func (model runBindingModel) binding() domain.RunBinding {
	return domain.RunBinding{ID: foundation.ID(model.ID), WorkspaceID: foundation.ID(model.WorkspaceID), SnapshotID: foundation.ID(model.SnapshotID),
		WorkflowRunID: foundation.ID(model.WorkflowRunID), DefinitionKey: model.DefinitionKey, DefinitionVersion: model.DefinitionVersion, CreatedAt: model.CreatedAt.UTC()}
}

// GetRunBinding 返回 Snapshot 的不可变 Workflow 绑定。
func (repository *GORMRepository) GetRunBinding(ctx context.Context, workspaceID, snapshotID foundation.ID) (domain.RunBinding, error) {
	if err := repository.validateReady(ctx); err != nil {
		return domain.RunBinding{}, err
	}
	if !validID(workspaceID) || !validID(snapshotID) {
		return domain.RunBinding{}, invalid(errors.New("organizing run binding lookup is invalid"))
	}
	binding, found, err := gormLoadRunBinding(repository.database.WithContext(ctx), workspaceID, snapshotID, false)
	if err != nil {
		return domain.RunBinding{}, err
	}
	if !found {
		return domain.RunBinding{}, notFound(sql.ErrNoRows)
	}
	return binding, nil
}

// BindRunResult 在独立命令事务中追加或重放不可变 Workflow 结果。
func (repository *GORMRepository) BindRunResult(ctx context.Context, record organizingapp.BindRunResultRecord) (domain.RunResult, bool, error) {
	if err := repository.validateReady(ctx); err != nil {
		return domain.RunResult{}, false, err
	}
	if err := record.Result.Validate(); err != nil {
		return domain.RunResult{}, false, invalid(err)
	}
	var result domain.RunResult
	var replayed bool
	err := repository.within(ctx, foundation.TransactionOptions{}, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(? || chr(31) || ?,0))`, string(record.Result.WorkspaceID), string(record.Result.WorkflowRunID)).Error; err != nil {
			return classifyGORM(err)
		}
		var err error
		result, replayed, err = gormBindResult(tx, record.Result, "", 0)
		return err
	})
	if err != nil {
		return domain.RunResult{}, false, err
	}
	return result, replayed, nil
}

// BindSucceededResultScoped 参与 Workflow 终态事务，不拥有提交或回滚。
func (repository *GORMRepository) BindSucceededResultScoped(ctx context.Context, scope foundation.TransactionScope, request organizingapp.TerminalResultRequest) error {
	if err := repository.validateReady(ctx); err != nil {
		return err
	}
	if request.Result.Validate() != nil || request.DefinitionKey == "" || request.DefinitionVersion < 1 {
		return inconsistent(errors.New("organizing terminal result request is invalid"))
	}
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return inconsistent(errors.New("organizing terminal transaction is invalid"))
	}
	_, _, err = gormBindResult(tx.WithContext(ctx), request.Result, request.DefinitionKey, request.DefinitionVersion)
	return err
}

func gormBindResult(tx *gorm.DB, result domain.RunResult, definitionKey string, definitionVersion int64) (domain.RunResult, bool, error) {
	existing, found, err := gormLoadRunResult(tx, result.WorkspaceID, result.WorkflowRunID, true)
	if err != nil {
		return domain.RunResult{}, false, err
	}
	if found {
		if !sameRunResultRequest(existing, result) {
			return domain.RunResult{}, false, idempotencyConflict(errors.New("organizing workflow run is bound to a different result"))
		}
		return existing, true, nil
	}
	var model runBindingModel
	err = tx.Select(gormRunBindingColumns).Where("id = ? AND workspace_id = ?", string(result.RunBindingID), string(result.WorkspaceID)).
		Clauses(clause.Locking{Strength: "SHARE"}).Take(&model).Error
	if gormNoRows(err) {
		if definitionKey != "" {
			return domain.RunResult{}, false, inconsistent(errors.New("organizing terminal run binding was not found"))
		}
		return domain.RunResult{}, false, notFound(err)
	}
	if err != nil {
		return domain.RunResult{}, false, classifyGORM(err)
	}
	binding := model.binding()
	if binding.Validate() != nil || binding.SnapshotID != result.SnapshotID || binding.WorkflowRunID != result.WorkflowRunID ||
		(definitionKey != "" && (binding.DefinitionKey != definitionKey || binding.DefinitionVersion != definitionVersion)) {
		return domain.RunResult{}, false, inconsistent(errors.New("organizing run result does not match its run binding"))
	}
	stored := runResultModel{ID: string(result.ID), WorkspaceID: string(result.WorkspaceID), RunBindingID: string(result.RunBindingID), SnapshotID: string(result.SnapshotID),
		WorkflowRunID: string(result.WorkflowRunID), NodeRunID: string(result.NodeRunID), ResultKind: string(result.Kind), ResultRef: string(result.ResultRef),
		ResultHash: result.ResultHash, CreatedAt: result.CreatedAt.UTC()}
	if err := tx.Create(&stored).Error; err != nil {
		return domain.RunResult{}, false, classifyGORM(err)
	}
	return result, false, nil
}

func gormLoadRunResult(tx *gorm.DB, workspaceID, workflowRunID foundation.ID, lock bool) (domain.RunResult, bool, error) {
	query := tx.Select(gormRunResultColumns).Where("workspace_id = ? AND workflow_run_id = ?", string(workspaceID), string(workflowRunID))
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var model runResultModel
	if err := query.Take(&model).Error; err != nil {
		if gormNoRows(err) {
			return domain.RunResult{}, false, nil
		}
		return domain.RunResult{}, false, classifyGORM(err)
	}
	result := domain.RunResult{ID: foundation.ID(model.ID), WorkspaceID: foundation.ID(model.WorkspaceID), RunBindingID: foundation.ID(model.RunBindingID),
		SnapshotID: foundation.ID(model.SnapshotID), WorkflowRunID: foundation.ID(model.WorkflowRunID), NodeRunID: foundation.ID(model.NodeRunID),
		Kind: domain.ResultKind(model.ResultKind), ResultRef: foundation.ID(model.ResultRef), ResultHash: model.ResultHash, CreatedAt: model.CreatedAt.UTC()}
	if err := result.Validate(); err != nil {
		return domain.RunResult{}, false, inconsistent(err)
	}
	return result, true, nil
}

// GetRunResult 返回 Workspace 范围内的不可变结果。
func (repository *GORMRepository) GetRunResult(ctx context.Context, workspaceID, workflowRunID foundation.ID) (domain.RunResult, error) {
	if err := repository.validateReady(ctx); err != nil {
		return domain.RunResult{}, err
	}
	if !validID(workspaceID) || !validID(workflowRunID) {
		return domain.RunResult{}, invalid(errors.New("organizing run result lookup is invalid"))
	}
	result, found, err := gormLoadRunResult(repository.database.WithContext(ctx), workspaceID, workflowRunID, false)
	if err != nil {
		return domain.RunResult{}, err
	}
	if !found {
		return domain.RunResult{}, notFound(sql.ErrNoRows)
	}
	return result, nil
}
