package postgres

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"github.com/jackc/pgx/v5"
)

const (
	maxStartLease    = 24 * time.Hour
	maxStartAttempts = 1000
)

var runtimeErrorCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

// ClaimStart 使用数据库时间领取一个到期的 Workflow Start Outbox。
func (repository *Repository) ClaimStart(ctx context.Context, owner string, lease time.Duration) (organizingapp.StartOutboxLease, bool, error) {
	if err := repository.validateReady(ctx); err != nil {
		return organizingapp.StartOutboxLease{}, false, err
	}
	owner = strings.TrimSpace(owner)
	if owner == "" || len(owner) > 256 || strings.ContainsAny(owner, "\r\n\x00") ||
		lease < time.Microsecond || lease > maxStartLease {
		return organizingapp.StartOutboxLease{}, false, invalid(errors.New("organizing start claim is invalid"))
	}
	var claimed organizingapp.StartOutboxLease
	err := repository.db.QueryRow(ctx, `WITH exhausted AS (
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
			outbox.attempt_count,outbox.version,outbox.lease_until`, owner, lease.Microseconds(), maxStartAttempts).
		Scan(&claimed.ID, &claimed.WorkspaceID, &claimed.SnapshotID, &claimed.TemplateRevisionID,
			&claimed.TemplateKind, &claimed.Owner, &claimed.AttemptCount, &claimed.Version, &claimed.LeaseUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return organizingapp.StartOutboxLease{}, false, nil
	}
	if err != nil {
		return organizingapp.StartOutboxLease{}, false, classify(err)
	}
	claimed.LeaseUntil = claimed.LeaseUntil.UTC()
	if err := validateStartLease(claimed); err != nil {
		return organizingapp.StartOutboxLease{}, false, inconsistent(err)
	}
	return claimed, true, nil
}

// CompleteStart 原子追加 RunBinding 并完成持有的 Outbox lease；精确重复调用返回 replay。
func (repository *Repository) CompleteStart(ctx context.Context, record organizingapp.CompleteStartRecord) (domain.RunBinding, bool, error) {
	if err := repository.validateReady(ctx); err != nil {
		return domain.RunBinding{}, false, err
	}
	if err := validateStartLease(record.Lease); err != nil || !validID(record.BindingID) || !validID(record.WorkflowRunID) ||
		strings.TrimSpace(record.DefinitionKey) == "" || len(record.DefinitionKey) > 128 ||
		strings.ContainsAny(record.DefinitionKey, "\r\n\x00") || record.DefinitionVersion < 1 || record.StartedAt.IsZero() {
		return domain.RunBinding{}, false, invalid(errors.New("organizing start completion is invalid"))
	}
	binding := domain.RunBinding{ID: record.BindingID, WorkspaceID: record.Lease.WorkspaceID,
		SnapshotID: record.Lease.SnapshotID, WorkflowRunID: record.WorkflowRunID,
		DefinitionKey: strings.TrimSpace(record.DefinitionKey), DefinitionVersion: record.DefinitionVersion,
		CreatedAt: canonicalTime(record.StartedAt)}
	if err := binding.Validate(); err != nil {
		return domain.RunBinding{}, false, invalid(err)
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.RunBinding{}, false, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	existing, found, err := loadRunBindingBySnapshot(ctx, tx, binding.WorkspaceID, binding.SnapshotID, true)
	if err != nil {
		return domain.RunBinding{}, false, err
	}
	if found {
		if existing.WorkspaceID != binding.WorkspaceID || existing.SnapshotID != binding.SnapshotID ||
			existing.WorkflowRunID != binding.WorkflowRunID || existing.DefinitionKey != binding.DefinitionKey ||
			existing.DefinitionVersion != binding.DefinitionVersion {
			return domain.RunBinding{}, false, idempotencyConflict(errors.New("organizing snapshot is bound to a different workflow run"))
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.RunBinding{}, false, classify(err)
		}
		return existing, true, nil
	}
	validLease, err := lockAndMatchStartLease(ctx, tx, record.Lease)
	if err != nil {
		return domain.RunBinding{}, false, err
	}
	if !validLease {
		return domain.RunBinding{}, false, leaseLost(errors.New("organizing start outbox lease was lost"))
	}
	expectedDefinition, expectedDefinitionVersion := definitionForTemplateKind(record.Lease.TemplateKind)
	if binding.DefinitionKey != expectedDefinition || binding.DefinitionVersion != expectedDefinitionVersion {
		return domain.RunBinding{}, false, invalid(errors.New("organizing workflow definition does not match the frozen template kind"))
	}
	if _, err := tx.Exec(ctx, `INSERT INTO organizing.run_binding(
		id,workspace_id,snapshot_id,workflow_run_id,definition_key,definition_version,created_at
	) VALUES($1,$2,$3,$4,$5,$6,$7)`, string(binding.ID), string(binding.WorkspaceID), string(binding.SnapshotID),
		string(binding.WorkflowRunID), binding.DefinitionKey, binding.DefinitionVersion, binding.CreatedAt.UTC()); err != nil {
		return domain.RunBinding{}, false, classify(err)
	}
	tag, err := tx.Exec(ctx, `UPDATE organizing.workflow_start_outbox
	SET status='STARTED',lease_owner=NULL,lease_until=NULL,run_binding_id=$1,
		version=version+1,updated_at=$2,started_at=$2
	WHERE id=$3 AND workspace_id=$4 AND status='PENDING' AND lease_owner=$5
	  AND version=$6 AND lease_until>clock_timestamp()`, string(binding.ID), binding.CreatedAt.UTC(),
		string(record.Lease.ID), string(record.Lease.WorkspaceID), record.Lease.Owner, record.Lease.Version)
	if err != nil {
		return domain.RunBinding{}, false, classify(err)
	}
	if tag.RowsAffected() != 1 {
		return domain.RunBinding{}, false, leaseLost(errors.New("organizing start outbox lease was lost"))
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.RunBinding{}, false, classify(err)
	}
	return binding, false, nil
}

// RetryStart 释放当前 lease，并按数据库时间安排下一次尝试；达到上限时转为 POISONED。
func (repository *Repository) RetryStart(ctx context.Context, record organizingapp.RetryStartRecord) error {
	if err := repository.validateReady(ctx); err != nil {
		return err
	}
	if err := validateStartLease(record.Lease); err != nil || !validRuntimeErrorCode(record.ErrorCode) || record.Delay < 0 || record.Delay > maxStartLease {
		return invalid(errors.New("organizing start retry is invalid"))
	}
	tag, err := repository.db.Exec(ctx, `UPDATE organizing.workflow_start_outbox
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
	if err != nil {
		return classify(err)
	}
	if tag.RowsAffected() != 1 {
		return leaseLost(errors.New("organizing start outbox lease was lost"))
	}
	return nil
}

// PoisonStart 将当前 lease 关闭为需要人工恢复的终态。
func (repository *Repository) PoisonStart(ctx context.Context, record organizingapp.PoisonStartRecord) error {
	if err := repository.validateReady(ctx); err != nil {
		return err
	}
	if err := validateStartLease(record.Lease); err != nil || !validRuntimeErrorCode(record.ErrorCode) {
		return invalid(errors.New("organizing start poison request is invalid"))
	}
	tag, err := repository.db.Exec(ctx, `UPDATE organizing.workflow_start_outbox
	SET status='POISONED',lease_owner=NULL,lease_until=NULL,last_error_code=$1,
		version=version+1,updated_at=clock_timestamp(),poisoned_at=clock_timestamp()
	WHERE id=$2 AND workspace_id=$3 AND status='PENDING' AND lease_owner=$4
	  AND version=$5 AND lease_until>clock_timestamp()`, record.ErrorCode, string(record.Lease.ID),
		string(record.Lease.WorkspaceID), record.Lease.Owner, record.Lease.Version)
	if err != nil {
		return classify(err)
	}
	if tag.RowsAffected() != 1 {
		return leaseLost(errors.New("organizing start outbox lease was lost"))
	}
	return nil
}

// GetRunBinding 返回 Snapshot 对应的不可变 Workflow Run 绑定。
func (repository *Repository) GetRunBinding(ctx context.Context, workspaceID, snapshotID foundation.ID) (domain.RunBinding, error) {
	if err := repository.validateReady(ctx); err != nil {
		return domain.RunBinding{}, err
	}
	if !validID(workspaceID) || !validID(snapshotID) {
		return domain.RunBinding{}, invalid(errors.New("organizing run binding lookup is invalid"))
	}
	binding, found, err := loadRunBindingBySnapshot(ctx, repository.db, workspaceID, snapshotID, false)
	if err != nil {
		return domain.RunBinding{}, err
	}
	if !found {
		return domain.RunBinding{}, notFound(pgx.ErrNoRows)
	}
	return binding, nil
}

// GetRunProjection 返回 Snapshot 对应的真实 Outbox 状态及可空 RunBinding/RunResult。
func (repository *Repository) GetRunProjection(ctx context.Context, workspaceID, snapshotID foundation.ID) (organizingapp.RunProjection, error) {
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
	err := repository.db.QueryRow(ctx, `SELECT
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
	WHERE o.workspace_id=$1 AND o.snapshot_id=$2`, string(workspaceID), string(snapshotID)).Scan(
		&projection.WorkspaceID, &projection.SnapshotID, &projection.Status, &projection.AttemptCount,
		&projection.LastErrorCode, &projection.UpdatedAt,
		&bindingID, &binding.WorkspaceID, &binding.SnapshotID, &binding.WorkflowRunID,
		&binding.DefinitionKey, &binding.DefinitionVersion, &binding.CreatedAt,
		&resultID, &result.WorkspaceID, &result.RunBindingID, &result.SnapshotID, &result.WorkflowRunID,
		&result.NodeRunID, &result.Kind, &result.ResultRef, &result.ResultHash, &result.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return organizingapp.RunProjection{}, notFound(err)
	}
	if err != nil {
		return organizingapp.RunProjection{}, classify(err)
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

// BindRunResult 追加一个精确的 Workflow 结果绑定；同 Run 的相同事实可幂等回放。
func (repository *Repository) BindRunResult(ctx context.Context, record organizingapp.BindRunResultRecord) (domain.RunResult, bool, error) {
	if err := repository.validateReady(ctx); err != nil {
		return domain.RunResult{}, false, err
	}
	result := record.Result
	if err := result.Validate(); err != nil {
		return domain.RunResult{}, false, invalid(err)
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.RunResult{}, false, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1 || chr(31) || $2,0))`,
		string(result.WorkspaceID), string(result.WorkflowRunID)); err != nil {
		return domain.RunResult{}, false, classify(err)
	}
	existing, found, err := loadRunResult(ctx, tx, result.WorkspaceID, result.WorkflowRunID, true)
	if err != nil {
		return domain.RunResult{}, false, err
	}
	if found {
		if !sameRunResultRequest(existing, result) {
			return domain.RunResult{}, false, idempotencyConflict(errors.New("organizing workflow run is bound to a different result"))
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.RunResult{}, false, classify(err)
		}
		return existing, true, nil
	}
	var binding domain.RunBinding
	err = tx.QueryRow(ctx, `SELECT id::text,workspace_id::text,snapshot_id::text,workflow_run_id::text,
		definition_key,definition_version,created_at FROM organizing.run_binding
		WHERE id=$1 AND workspace_id=$2 FOR SHARE`, string(result.RunBindingID), string(result.WorkspaceID)).
		Scan(&binding.ID, &binding.WorkspaceID, &binding.SnapshotID, &binding.WorkflowRunID,
			&binding.DefinitionKey, &binding.DefinitionVersion, &binding.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RunResult{}, false, notFound(err)
	}
	if err != nil {
		return domain.RunResult{}, false, classify(err)
	}
	binding.CreatedAt = binding.CreatedAt.UTC()
	if binding.Validate() != nil || binding.SnapshotID != result.SnapshotID || binding.WorkflowRunID != result.WorkflowRunID {
		return domain.RunResult{}, false, inconsistent(errors.New("organizing run result does not match its run binding"))
	}
	if _, err := tx.Exec(ctx, `INSERT INTO organizing.run_result(
		id,workspace_id,run_binding_id,snapshot_id,workflow_run_id,node_run_id,
		result_kind,result_ref,result_hash,created_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, string(result.ID), string(result.WorkspaceID),
		string(result.RunBindingID), string(result.SnapshotID), string(result.WorkflowRunID), string(result.NodeRunID),
		string(result.Kind), string(result.ResultRef), result.ResultHash, result.CreatedAt.UTC()); err != nil {
		return domain.RunResult{}, false, classify(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.RunResult{}, false, classify(err)
	}
	return result, false, nil
}

// GetRunResult 返回一个 Workflow Run 的不可变 Organizing 结果绑定。
func (repository *Repository) GetRunResult(ctx context.Context, workspaceID, workflowRunID foundation.ID) (domain.RunResult, error) {
	if err := repository.validateReady(ctx); err != nil {
		return domain.RunResult{}, err
	}
	if !validID(workspaceID) || !validID(workflowRunID) {
		return domain.RunResult{}, invalid(errors.New("organizing run result lookup is invalid"))
	}
	result, found, err := loadRunResult(ctx, repository.db, workspaceID, workflowRunID, false)
	if err != nil {
		return domain.RunResult{}, err
	}
	if !found {
		return domain.RunResult{}, notFound(pgx.ErrNoRows)
	}
	return result, nil
}

func validateStartLease(lease organizingapp.StartOutboxLease) error {
	if !validID(lease.ID) || !validID(lease.WorkspaceID) || !validID(lease.SnapshotID) ||
		!validID(lease.TemplateRevisionID) || !lease.TemplateKind.Valid() || strings.TrimSpace(lease.Owner) == "" ||
		len(lease.Owner) > 256 || strings.ContainsAny(lease.Owner, "\r\n\x00") || lease.AttemptCount < 1 ||
		lease.Version < 2 || lease.LeaseUntil.IsZero() {
		return errors.New("organizing start outbox lease is invalid")
	}
	return nil
}

func validRuntimeErrorCode(value string) bool {
	return value == strings.TrimSpace(value) && runtimeErrorCodePattern.MatchString(value)
}

func sameRunResultRequest(left, right domain.RunResult) bool {
	return left.WorkspaceID == right.WorkspaceID && left.RunBindingID == right.RunBindingID &&
		left.SnapshotID == right.SnapshotID && left.WorkflowRunID == right.WorkflowRunID &&
		left.NodeRunID == right.NodeRunID && left.Kind == right.Kind && left.ResultRef == right.ResultRef &&
		left.ResultHash == right.ResultHash
}

func lockAndMatchStartLease(ctx context.Context, tx pgx.Tx, lease organizingapp.StartOutboxLease) (bool, error) {
	var id foundation.ID
	err := tx.QueryRow(ctx, `SELECT id::text FROM organizing.workflow_start_outbox
		WHERE id=$1 AND workspace_id=$2 AND snapshot_id=$3 AND template_revision_id=$4
		  AND template_kind=$5 AND status='PENDING' AND lease_owner=$6 AND version=$7
		  AND lease_until>clock_timestamp() FOR UPDATE`, string(lease.ID), string(lease.WorkspaceID),
		string(lease.SnapshotID), string(lease.TemplateRevisionID), string(lease.TemplateKind), lease.Owner, lease.Version).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, classify(err)
	}
	return id == lease.ID, nil
}

func loadRunBindingBySnapshot(ctx context.Context, db queryer, workspaceID, snapshotID foundation.ID, lock bool) (domain.RunBinding, bool, error) {
	query := `SELECT id::text,workspace_id::text,snapshot_id::text,workflow_run_id::text,
		definition_key,definition_version,created_at FROM organizing.run_binding
		WHERE workspace_id=$1 AND snapshot_id=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	var binding domain.RunBinding
	err := db.QueryRow(ctx, query, string(workspaceID), string(snapshotID)).Scan(&binding.ID, &binding.WorkspaceID,
		&binding.SnapshotID, &binding.WorkflowRunID, &binding.DefinitionKey, &binding.DefinitionVersion, &binding.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RunBinding{}, false, nil
	}
	if err != nil {
		return domain.RunBinding{}, false, classify(err)
	}
	binding.CreatedAt = binding.CreatedAt.UTC()
	if err := binding.Validate(); err != nil {
		return domain.RunBinding{}, false, inconsistent(err)
	}
	return binding, true, nil
}

func loadRunResult(ctx context.Context, db queryer, workspaceID, workflowRunID foundation.ID, lock bool) (domain.RunResult, bool, error) {
	query := `SELECT id::text,workspace_id::text,run_binding_id::text,snapshot_id::text,
		workflow_run_id::text,node_run_id::text,result_kind,result_ref::text,result_hash,created_at
		FROM organizing.run_result WHERE workspace_id=$1 AND workflow_run_id=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	var result domain.RunResult
	err := db.QueryRow(ctx, query, string(workspaceID), string(workflowRunID)).Scan(&result.ID, &result.WorkspaceID,
		&result.RunBindingID, &result.SnapshotID, &result.WorkflowRunID, &result.NodeRunID,
		&result.Kind, &result.ResultRef, &result.ResultHash, &result.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RunResult{}, false, nil
	}
	if err != nil {
		return domain.RunResult{}, false, classify(err)
	}
	result.CreatedAt = result.CreatedAt.UTC()
	if err := result.Validate(); err != nil {
		return domain.RunResult{}, false, inconsistent(err)
	}
	return result, true, nil
}

func definitionForTemplateKind(kind domain.TemplateKind) (string, int64) {
	switch kind {
	case domain.TemplateTopicArticle:
		return "organizing.topic-article", 1
	case domain.TemplateMergeDocuments:
		return "organizing.merge-documents", 1
	case domain.TemplateKnowledgeReport:
		return "organizing.knowledge-report", 1
	case domain.TemplateInterviewReview:
		return "organizing.interview-review", 1
	default:
		return "", 0
	}
}
