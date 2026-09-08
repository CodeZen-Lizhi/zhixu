package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
)

const defaultAffectedChangeBackoff = time.Minute

// AffectedChangeDispatchRepository 在短事务内把 typed outbox 绑定到唯一 Health Scan。
type AffectedChangeDispatchRepository struct {
	scans   *ScanRepository
	planner *healthapp.AffectedChangePlanner
	backoff time.Duration
}

var _ healthapp.AffectedChangeDispatchPort = (*AffectedChangeDispatchRepository)(nil)

// NewAffectedChangeDispatchRepository 构造使用一分钟 active-scan 退避的 PostgreSQL dispatcher。
func NewAffectedChangeDispatchRepository(scans *ScanRepository, planner *healthapp.AffectedChangePlanner) (*AffectedChangeDispatchRepository, error) {
	return newAffectedChangeDispatchRepository(scans, planner, defaultAffectedChangeBackoff)
}

func newAffectedChangeDispatchRepository(scans *ScanRepository, planner *healthapp.AffectedChangePlanner, backoff time.Duration) (*AffectedChangeDispatchRepository, error) {
	if scans == nil || nilScanValue(scans.db) || nilScanValue(scans.runtime) || nilScanValue(scans.ids) || nilScanValue(scans.clock) || planner == nil {
		return nil, affectedChangeUnavailable(errors.New("health affected-change dispatcher dependencies are unavailable"))
	}
	if backoff <= 0 || backoff > 24*time.Hour {
		return nil, affectedChangeInvalid(errors.New("health affected-change backoff is invalid"))
	}
	return &AffectedChangeDispatchRepository{scans: scans, planner: planner, backoff: backoff}, nil
}

type affectedChangeRow struct {
	event        healthapp.AffectedChangeEvent
	attemptCount int64
	version      int64
	availableAt  time.Time
}

type affectedChangeCommitExpectation struct {
	row         affectedChangeRow
	result      healthapp.AffectedChangeDispatchResult
	request     *healthapp.ScanStartRequest
	availableAt time.Time
	errorCode   string
}

// DispatchNext 使用 DB time 与 FOR UPDATE SKIP LOCKED 原子处理最多一个 due event。
func (repository *AffectedChangeDispatchRepository) DispatchNext(ctx context.Context) (healthapp.AffectedChangeDispatchResult, bool, error) {
	if repository == nil || repository.scans == nil || repository.planner == nil || nilScanValue(repository.scans.db) {
		return healthapp.AffectedChangeDispatchResult{}, false, affectedChangeUnavailable(errors.New("health affected-change dispatcher is unavailable"))
	}
	if ctx == nil {
		return healthapp.AffectedChangeDispatchResult{}, false, affectedChangeInvalid(errors.New("health affected-change context is nil"))
	}
	outcome, err := withHealthTransaction(ctx, repository.scans.db, foundation.TransactionOptions{}, func(ctx context.Context, tx healthTransaction) (affectedChangeDispatch, error) {
		return repository.dispatchNextTx(ctx, tx)
	})
	if healthCommitFailed(err) && outcome.expectation != nil {
		err = repository.recoverCommitFailure(ctx, err, *outcome.expectation)
	}
	if err != nil {
		return healthapp.AffectedChangeDispatchResult{}, outcome.found, classifyHealthTransactionError(err, "HEALTH_AFFECTED_BEGIN_FAILED", "HEALTH_AFFECTED_COMMIT_FAILED")
	}
	return outcome.result, outcome.found, outcome.afterCommitError
}

// affectedChangeDispatch 将提交后才返回的人工恢复错误与事务失败分开。
type affectedChangeDispatch struct {
	result           healthapp.AffectedChangeDispatchResult
	found            bool
	expectation      *affectedChangeCommitExpectation
	afterCommitError error
}

func (repository *AffectedChangeDispatchRepository) dispatchNextTx(ctx context.Context, tx healthTransaction) (affectedChangeDispatch, error) {
	now, err := affectedChangeDatabaseTime(ctx, tx)
	if err != nil {
		return affectedChangeDispatch{}, err
	}
	row, found, err := claimAffectedChange(ctx, tx, now)
	if err != nil || !found {
		return affectedChangeDispatch{}, err
	}

	workspaceVersion, sourceMatches, err := verifyAffectedChangeSource(ctx, tx, row.event)
	if err != nil {
		return affectedChangeDispatch{found: true}, err
	}
	if !sourceMatches {
		return repository.poisonAffectedChange(ctx, tx, row, now, healthapp.ErrorCodeAffectedChangeSourceBindingInvalid,
			errors.New("health affected-change source binding no longer matches"))
	}

	request, err := repository.planner.Plan(row.event, workspaceVersion)
	if err != nil {
		var classified *foundation.Error
		if errors.As(err, &classified) && classified.Kind == foundation.ErrorManualRecoveryRequired {
			return repository.poisonAffectedChange(ctx, tx, row, now, classified.Code, err)
		}
		return affectedChangeDispatch{found: true}, err
	}

	started, err := repository.scans.startOrReplayTx(ctx, tx, request)
	if err != nil {
		active, activeErr := activeAffectedWorkspaceScan(ctx, tx, row.event.WorkspaceID)
		if activeErr != nil {
			return affectedChangeDispatch{found: true}, errors.Join(err, activeErr)
		}
		if isHealthScanScopeConflict(err) && active {
			return repository.deferAffectedChange(ctx, tx, row, now)
		}
		return affectedChangeDispatch{found: true}, err
	}

	publishedAt, err := affectedChangeDatabaseTime(ctx, tx)
	if err != nil {
		return affectedChangeDispatch{found: true}, err
	}
	result := healthapp.AffectedChangeDispatchResult{
		EventID:       row.event.ID,
		Outcome:       healthapp.AffectedChangeDispatchPublished,
		ScanID:        started.Scan.ID,
		WorkflowRunID: started.Scan.WorkflowRunID,
	}
	tag, err := tx.Exec(ctx, `UPDATE ops.health_affected_change_outbox
SET bound_scan_id=$2,bound_workflow_run_id=$3,published_at=$4,last_error_code=NULL,
    attempt_count=attempt_count+1,version=version+1,updated_at=$4
WHERE id=$1 AND version=$5 AND published_at IS NULL AND NOT manual_recovery_required`,
		string(row.event.ID), string(result.ScanID), string(result.WorkflowRunID), publishedAt, row.version)
	if err != nil {
		return affectedChangeDispatch{found: true}, classifyAffectedChangeError(err, "HEALTH_AFFECTED_PUBLISH_FAILED")
	}
	if tag.RowsAffected() != 1 {
		return affectedChangeDispatch{found: true}, affectedChangeConsistency(errors.New("health affected-change publish CAS did not match"))
	}
	expectation := affectedChangeCommitExpectation{row: row, result: result, request: &request}
	return affectedChangeDispatch{result: result, found: true, expectation: &expectation}, nil
}

func (repository *AffectedChangeDispatchRepository) deferAffectedChange(ctx context.Context, tx healthTransaction, row affectedChangeRow, now time.Time) (affectedChangeDispatch, error) {
	availableAt := now.Add(repository.backoff)
	result := healthapp.AffectedChangeDispatchResult{EventID: row.event.ID, Outcome: healthapp.AffectedChangeDispatchDeferred}
	tag, err := tx.Exec(ctx, `UPDATE ops.health_affected_change_outbox
SET available_at=$2,last_error_code=$3,attempt_count=attempt_count+1,version=version+1,updated_at=$4
WHERE id=$1 AND version=$5 AND published_at IS NULL AND NOT manual_recovery_required`,
		string(row.event.ID), availableAt, healthapp.ErrorCodeAffectedChangeScanActive, now, row.version)
	if err != nil {
		return affectedChangeDispatch{found: true}, classifyAffectedChangeError(err, "HEALTH_AFFECTED_BACKOFF_FAILED")
	}
	if tag.RowsAffected() != 1 {
		return affectedChangeDispatch{found: true}, affectedChangeConsistency(errors.New("health affected-change backoff CAS did not match"))
	}
	expectation := affectedChangeCommitExpectation{
		row: row, result: result, availableAt: availableAt, errorCode: healthapp.ErrorCodeAffectedChangeScanActive,
	}
	return affectedChangeDispatch{result: result, found: true, expectation: &expectation}, nil
}

func (repository *AffectedChangeDispatchRepository) poisonAffectedChange(ctx context.Context, tx healthTransaction, row affectedChangeRow, now time.Time, code string, cause error) (affectedChangeDispatch, error) {
	if code == "" {
		code = healthapp.ErrorCodeAffectedChangeInvalid
	}
	result := healthapp.AffectedChangeDispatchResult{EventID: row.event.ID, Outcome: healthapp.AffectedChangeDispatchPoisoned}
	tag, err := tx.Exec(ctx, `UPDATE ops.health_affected_change_outbox
SET poisoned_at=$2,manual_recovery_required=true,last_error_code=$3,
    attempt_count=attempt_count+1,version=version+1,updated_at=$2
WHERE id=$1 AND version=$4 AND published_at IS NULL AND NOT manual_recovery_required`,
		string(row.event.ID), now, code, row.version)
	if err != nil {
		return affectedChangeDispatch{found: true}, classifyAffectedChangeError(err, "HEALTH_AFFECTED_POISON_FAILED")
	}
	if tag.RowsAffected() != 1 {
		return affectedChangeDispatch{found: true}, affectedChangeConsistency(errors.New("health affected-change poison CAS did not match"))
	}
	expectation := affectedChangeCommitExpectation{row: row, result: result, errorCode: code}
	return affectedChangeDispatch{result: result, found: true, expectation: &expectation, afterCommitError: foundation.NewError(foundation.ErrorManualRecoveryRequired, code, false, cause)}, nil
}

func (repository *AffectedChangeDispatchRepository) recoverCommitFailure(ctx context.Context, err error, expectation affectedChangeCommitExpectation) error {
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	recovered, recoveryErr := repository.recoverCommittedOutcome(recoveryCtx, expectation)
	if recovered && recoveryErr == nil {
		return nil
	}
	classified := classifyAffectedChangeError(err, "HEALTH_AFFECTED_COMMIT_FAILED")
	if recoveryErr != nil {
		return errors.Join(classified, recoveryErr)
	}
	return classified
}

func (repository *AffectedChangeDispatchRepository) recoverCommittedOutcome(ctx context.Context, expectation affectedChangeCommitExpectation) (bool, error) {
	var attemptCount, version int64
	var lastErrorCode, scanID, workflowRunID *string
	var publishedAt, poisonedAt *time.Time
	var manualRecovery bool
	var availableAt time.Time
	err := repository.scans.db.QueryRow(ctx, `SELECT attempt_count,version,last_error_code,bound_scan_id::text,
bound_workflow_run_id::text,published_at,poisoned_at,manual_recovery_required,available_at
FROM ops.health_affected_change_outbox WHERE id=$1`, string(expectation.row.event.ID)).Scan(
		&attemptCount, &version, &lastErrorCode, &scanID, &workflowRunID, &publishedAt, &poisonedAt, &manualRecovery, &availableAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, classifyAffectedChangeError(err, "HEALTH_AFFECTED_RECOVERY_QUERY_FAILED")
	}
	if attemptCount != expectation.row.attemptCount+1 || version != expectation.row.version+1 {
		return false, nil
	}
	switch expectation.result.Outcome {
	case healthapp.AffectedChangeDispatchPublished:
		if publishedAt == nil || poisonedAt != nil || manualRecovery || lastErrorCode != nil || scanID == nil || workflowRunID == nil || expectation.request == nil || *scanID != string(expectation.result.ScanID) || *workflowRunID != string(expectation.result.WorkflowRunID) {
			return false, nil
		}
		scan, found, loadErr := loadScanByIdempotency(ctx, repository.scans.db, expectation.row.event.WorkspaceID, expectation.request.IdempotencyKey, false)
		if loadErr != nil || !found {
			return false, loadErr
		}
		replayed, replayErr := replayStart(*expectation.request, scan)
		return replayErr == nil && replayed.Scan.ID == expectation.result.ScanID && replayed.Scan.WorkflowRunID == expectation.result.WorkflowRunID, replayErr
	case healthapp.AffectedChangeDispatchDeferred:
		return publishedAt == nil && poisonedAt == nil && !manualRecovery && scanID == nil && workflowRunID == nil &&
			lastErrorCode != nil && *lastErrorCode == expectation.errorCode && availableAt.Equal(expectation.availableAt), nil
	case healthapp.AffectedChangeDispatchPoisoned:
		return publishedAt == nil && poisonedAt != nil && manualRecovery && scanID == nil && workflowRunID == nil &&
			lastErrorCode != nil && *lastErrorCode == expectation.errorCode, nil
	default:
		return false, nil
	}
}

func claimAffectedChange(ctx context.Context, tx healthTransaction, now time.Time) (affectedChangeRow, bool, error) {
	var row affectedChangeRow
	var id, workspaceID, eventType, sourceKind, aggregateID string
	var sourceHash, aggregateSubID, changeCode *string
	err := tx.QueryRow(ctx, `SELECT id::text,workspace_id::text,schema_version,event_version,event_type,
source_kind,source_key,source_hash,aggregate_type,aggregate_id::text,aggregate_sub_id::text,
aggregate_version,change_status,change_code,attempt_count,version,available_at
FROM ops.health_affected_change_outbox
WHERE published_at IS NULL AND NOT manual_recovery_required AND available_at <= $1
ORDER BY available_at,occurred_at,id
FOR UPDATE SKIP LOCKED LIMIT 1`, now).Scan(
		&id, &workspaceID, &row.event.SchemaVersion, &row.event.EventVersion, &eventType,
		&sourceKind, &row.event.SourceKey, &sourceHash, &row.event.AggregateType, &aggregateID, &aggregateSubID,
		&row.event.AggregateVersion, &row.event.ChangeStatus, &changeCode, &row.attemptCount, &row.version, &row.availableAt)
	if errors.Is(err, sql.ErrNoRows) {
		return affectedChangeRow{}, false, nil
	}
	if err != nil {
		return affectedChangeRow{}, false, classifyAffectedChangeError(err, "HEALTH_AFFECTED_CLAIM_FAILED")
	}
	row.event.ID = foundation.ID(id)
	row.event.WorkspaceID = foundation.ID(workspaceID)
	row.event.EventType = healthapp.AffectedChangeEventType(eventType)
	row.event.SourceKind = healthapp.AffectedChangeSourceKind(sourceKind)
	row.event.AggregateID = foundation.ID(aggregateID)
	if sourceHash != nil {
		row.event.SourceHash = *sourceHash
	}
	if aggregateSubID != nil {
		row.event.AggregateSubID = foundation.ID(*aggregateSubID)
	}
	if changeCode != nil {
		row.event.ChangeCode = *changeCode
	}
	return row, true, nil
}

func verifyAffectedChangeSource(ctx context.Context, tx healthTransaction, event healthapp.AffectedChangeEvent) (int64, bool, error) {
	var workspaceVersion int64
	if err := tx.QueryRow(ctx, `SELECT version FROM core.workspace WHERE id=$1`, string(event.WorkspaceID)).Scan(&workspaceVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, classifyAffectedChangeError(err, "HEALTH_AFFECTED_WORKSPACE_QUERY_FAILED")
	}
	var matches bool
	var err error
	switch event.SourceKind {
	case healthapp.AffectedChangeSourceKnowledgeReceipt:
		err = tx.QueryRow(ctx, `SELECT EXISTS (
SELECT 1 FROM core.knowledge_command_receipt
WHERE workspace_id=$1 AND idempotency_key=$2 AND request_hash=$3 AND command_type=$4
  AND aggregate_type=$5 AND aggregate_id=$6 AND aggregate_version=$7)`,
			string(event.WorkspaceID), event.SourceKey, event.SourceHash, event.ChangeStatus,
			event.AggregateType, string(event.AggregateID), event.AggregateVersion).Scan(&matches)
	case healthapp.AffectedChangeSourceIndexVersion:
		err = tx.QueryRow(ctx, `SELECT EXISTS (
SELECT 1 FROM retrieval.index_version
WHERE id=$1 AND workspace_id=$2 AND status=$3 AND failure_code=$4 AND version=$5)`,
			string(event.AggregateID), string(event.WorkspaceID), event.ChangeStatus,
			event.ChangeCode, event.AggregateVersion).Scan(&matches)
	case healthapp.AffectedChangeSourceChunkProjection:
		statusColumn := "vector_status"
		if event.EventType == healthapp.AffectedChangeEventLexicalDegraded {
			statusColumn = "lexical_status"
		}
		err = tx.QueryRow(ctx, `SELECT EXISTS (
SELECT 1 FROM retrieval.chunk_projection projection
JOIN retrieval.index_version index_version
  ON index_version.id=projection.index_version_id AND index_version.workspace_id=projection.workspace_id
WHERE projection.index_version_id=$1 AND projection.chunk_id=$2 AND projection.workspace_id=$3
	  AND CASE WHEN $7='lexical_status' THEN projection.lexical_status ELSE projection.vector_status END=$4
	  AND projection.failure_code=$5 AND index_version.version >= $6)`,
			string(event.AggregateID), string(event.AggregateSubID), string(event.WorkspaceID),
			event.ChangeStatus, event.ChangeCode, event.AggregateVersion, statusColumn).Scan(&matches)
	default:
		return workspaceVersion, false, nil
	}
	if err != nil {
		return 0, false, classifyAffectedChangeError(err, "HEALTH_AFFECTED_SOURCE_QUERY_FAILED")
	}
	return workspaceVersion, matches, nil
}

func activeAffectedWorkspaceScan(ctx context.Context, tx healthTransaction, workspaceID foundation.ID) (bool, error) {
	var active bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (
SELECT 1 FROM ops.health_scan
WHERE workspace_id=$1 AND scope_type='WORKSPACE' AND scope_ref=$1
  AND status IN ('PENDING','RUNNING'))`, string(workspaceID)).Scan(&active)
	if err != nil {
		return false, classifyAffectedChangeError(err, "HEALTH_AFFECTED_ACTIVE_SCAN_QUERY_FAILED")
	}
	return active, nil
}

func affectedChangeDatabaseTime(ctx context.Context, tx healthTransaction) (time.Time, error) {
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return time.Time{}, classifyAffectedChangeError(err, "HEALTH_AFFECTED_DB_TIME_UNAVAILABLE")
	}
	return now.UTC(), nil
}

func isHealthScanScopeConflict(err error) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Kind == foundation.ErrorVersionConflict && classified.Code == domain.ErrorCodeScanTransitionInvalid
}

func affectedChangeInvalid(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, healthapp.ErrorCodeAffectedChangeInvalid, false, cause)
}

func affectedChangeConsistency(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, healthapp.ErrorCodeAffectedChangeInvalid, false, cause)
}

func affectedChangeUnavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, "HEALTH_AFFECTED_CHANGE_UNAVAILABLE", true, cause)
}

func classifyAffectedChangeError(err error, code string) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	return classifyScanError(err, code)
}
