package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"time"

	eventsapp "github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	eventsdomain "github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
	healthworkflow "github.com/CodeZen-Lizhi/zhixu/internal/health/workflow"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

// ScanRepository 持久化 Health Scan 业务事实并复用 Workflow Runtime scoped 启动。
type ScanRepository struct {
	db       healthStore
	runtime  workflowapp.ScopedRuntimeStarter
	events   eventsapp.ScopedAppender
	verifier scopedHealthBindingVerifier
	ids      foundation.IDGenerator
	clock    foundation.Clock
}

var _ healthapp.ScanStartPort = (*ScanRepository)(nil)
var _ healthapp.ScanStatePort = (*ScanRepository)(nil)

// newScanRepository 构造支持 Start、查询和 CAS 推进的 Health Scan Repository。
func newScanRepository(db healthStore, runtime workflowapp.ScopedRuntimeStarter, events eventsapp.ScopedAppender, ids foundation.IDGenerator, clock foundation.Clock, verifiers ...scopedHealthBindingVerifier) (*ScanRepository, error) {
	if nilScanValue(db) || nilScanValue(runtime) || nilScanValue(events) || nilScanValue(ids) || nilScanValue(clock) {
		return nil, repositoryUnavailable(errors.New("health scan repository dependencies are missing"))
	}
	var verifier scopedHealthBindingVerifier
	if len(verifiers) > 0 {
		if len(verifiers) != 1 || nilScanValue(verifiers[0]) {
			return nil, repositoryUnavailable(errors.New("health smart-collection verifier is invalid"))
		}
		verifier = verifiers[0]
	}
	return &ScanRepository{db: db, runtime: runtime, events: events, verifier: verifier, ids: ids, clock: clock}, nil
}

// newScanStateRepository 构造只供 Worker 查询与推进的 Health Scan Repository。
func newScanStateRepository(db healthStore, events eventsapp.ScopedAppender) (*ScanRepository, error) {
	if nilScanValue(db) || nilScanValue(events) {
		return nil, repositoryUnavailable(errors.New("health scan state dependencies are missing"))
	}
	return &ScanRepository{db: db, events: events}, nil
}

// StartOrReplay 在一个 PostgreSQL 事务内创建或精确重放 Workflow、River Job、Scan 与 coverage。
func (repository *ScanRepository) StartOrReplay(ctx context.Context, request healthapp.ScanStartRequest) (healthapp.ScanStartResult, error) {
	if repository == nil || nilScanValue(repository.db) || nilScanValue(repository.runtime) || nilScanValue(repository.ids) || nilScanValue(repository.clock) {
		return healthapp.ScanStartResult{}, repositoryUnavailable(errors.New("health scan start repository is unavailable"))
	}
	if err := validateStartRequest(request); err != nil {
		return healthapp.ScanStartResult{}, err
	}
	result, err := withHealthTransaction(ctx, repository.db, foundation.TransactionOptions{Isolation: foundation.TransactionIsolationRepeatableRead}, func(ctx context.Context, tx healthTransaction) (healthapp.ScanStartResult, error) {
		return repository.startOrReplayTx(ctx, tx, request)
	})
	if healthCommitFailed(err) {
		if recovered, found, recoveryErr := repository.recoverCommittedStart(ctx, request); recoveryErr != nil {
			return healthapp.ScanStartResult{}, errors.Join(classifyScanError(err, "HEALTH_SCAN_COMMIT_FAILED"), recoveryErr)
		} else if found {
			return recovered, nil
		}
		return healthapp.ScanStartResult{}, classifyScanError(err, "HEALTH_SCAN_COMMIT_FAILED")
	}
	if err != nil {
		return healthapp.ScanStartResult{}, classifyHealthTransactionError(err, "HEALTH_SCAN_START_FAILED", "HEALTH_SCAN_COMMIT_FAILED")
	}
	return result, nil
}

func (repository *ScanRepository) startOrReplayTx(ctx context.Context, tx healthTransaction, request healthapp.ScanStartRequest) (healthapp.ScanStartResult, error) {
	if existing, found, err := loadScanByIdempotency(ctx, tx, request.WorkspaceID, request.IdempotencyKey, true); err != nil {
		return healthapp.ScanStartResult{}, err
	} else if found {
		return replayStart(request, existing)
	}
	if request.PreventScopeConcurrency {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1 || chr(31) || $2 || chr(31) || $3,0))`,
			string(request.WorkspaceID), string(request.Scope.Type), string(request.Scope.Ref)); err != nil {
			return healthapp.ScanStartResult{}, classifyScanError(err, "HEALTH_SCAN_SCOPE_LOCK_FAILED")
		}
		var activeID string
		err := tx.QueryRow(ctx, `SELECT id::text FROM ops.health_scan
WHERE workspace_id=$1 AND scope_type=$2 AND scope_ref=$3 AND status IN ('PENDING','RUNNING')
ORDER BY created_at,id LIMIT 1 FOR UPDATE`, string(request.WorkspaceID), string(request.Scope.Type), string(request.Scope.Ref)).Scan(&activeID)
		if err == nil {
			return healthapp.ScanStartResult{}, repositoryConflict("health scan scope already has an active scan")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return healthapp.ScanStartResult{}, classifyScanError(err, "HEALTH_SCAN_SCOPE_QUERY_FAILED")
		}
	}
	if err := repository.verifySmartCollectionBinding(ctx, tx, request); err != nil {
		return healthapp.ScanStartResult{}, err
	}

	scanID, err := repository.ids.New()
	if err != nil {
		return healthapp.ScanStartResult{}, err
	}
	input, err := healthworkflow.EncodeScanInput(request)
	if err != nil {
		return healthapp.ScanStartResult{}, err
	}
	definition, err := healthworkflow.RegisteredDefinition()
	if err != nil {
		return healthapp.ScanStartResult{}, err
	}
	runtimeRequest, err := workflowapp.BuildRuntimeStartRequest(repository.ids, repository.clock, request.WorkspaceID, runtimeIdempotencyKey(request), input, definition)
	if err != nil {
		return healthapp.ScanStartResult{}, classifyScanError(err, "HEALTH_SCAN_WORKFLOW_START_INVALID")
	}
	runtimeResult, err := repository.runtime.StartScoped(ctx, tx.scope, runtimeRequest)
	if err != nil {
		if existing, found, replayErr := loadScanByIdempotency(ctx, tx, request.WorkspaceID, request.IdempotencyKey, true); replayErr != nil {
			return healthapp.ScanStartResult{}, replayErr
		} else if found {
			return replayStart(request, existing)
		}
		return healthapp.ScanStartResult{}, classifyScanError(err, "HEALTH_SCAN_WORKFLOW_START_FAILED")
	}
	now, err := databaseTime(ctx, tx)
	if err != nil {
		return healthapp.ScanStartResult{}, err
	}
	scan := domain.Scan{
		ID: scanID, WorkspaceID: request.WorkspaceID, WorkflowRunID: runtimeResult.Run.ID,
		Scope: request.Scope, Fingerprint: request.Fingerprint, IdempotencyKey: request.IdempotencyKey,
		RequestHash: request.RequestHash, MaxItems: request.MaxItems, Status: domain.ScanStatusPending,
		Coverage: cloneCoverage(request.Coverage), Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	inserted, err := insertScan(ctx, tx, scan)
	if err != nil {
		return healthapp.ScanStartResult{}, err
	}
	if !inserted {
		existing, found, loadErr := loadScanByIdempotency(ctx, tx, request.WorkspaceID, request.IdempotencyKey, true)
		if loadErr != nil {
			return healthapp.ScanStartResult{}, loadErr
		}
		if !found {
			return healthapp.ScanStartResult{}, repositoryConsistency(errors.New("health scan idempotency winner is missing"))
		}
		return replayStart(request, existing)
	}
	if err := insertCoverage(ctx, tx, scan.ID, scan.WorkspaceID, request.Coverage, now); err != nil {
		return healthapp.ScanStartResult{}, err
	}
	created, err := loadScanByID(ctx, tx, scan.WorkspaceID, scan.ID, false)
	if err != nil {
		return healthapp.ScanStartResult{}, err
	}
	return healthapp.ScanStartResult{Scan: created}, nil
}

// Get 返回 Workspace-scoped Scan 与 detector coverage。
func (repository *ScanRepository) Get(ctx context.Context, workspaceID, scanID foundation.ID) (domain.Scan, error) {
	if repository == nil || nilScanValue(repository.db) || !validID(workspaceID) || !validID(scanID) {
		return domain.Scan{}, repositoryInvalid(errors.New("health scan lookup is invalid"))
	}
	return inScanSnapshot(ctx, repository.db, func(ctx context.Context, tx healthTransaction) (domain.Scan, error) {
		return loadScanByID(ctx, tx, workspaceID, scanID, false)
	})
}

// GetByWorkflowRun 返回 Workflow delivery 绑定的唯一 Health Scan。
func (repository *ScanRepository) GetByWorkflowRun(ctx context.Context, workspaceID, workflowRunID foundation.ID) (domain.Scan, error) {
	if repository == nil || nilScanValue(repository.db) || !validID(workspaceID) || !validID(workflowRunID) {
		return domain.Scan{}, repositoryInvalid(errors.New("health scan workflow lookup is invalid"))
	}
	return inScanSnapshot(ctx, repository.db, func(ctx context.Context, tx healthTransaction) (domain.Scan, error) {
		var scan domain.Scan
		err := scanHealthScan(tx.QueryRow(ctx, scanSelectSQL+` WHERE workspace_id=$1 AND workflow_run_id=$2`, string(workspaceID), string(workflowRunID)), &scan)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Scan{}, repositoryNotFound(err)
		}
		if err != nil {
			return domain.Scan{}, err
		}
		coverage, err := loadCoverage(ctx, tx, scan.ID, scan.WorkspaceID, false)
		if err != nil {
			return domain.Scan{}, err
		}
		scan.Coverage = coverage
		if err := domain.ValidateScan(scan); err != nil {
			return domain.Scan{}, err
		}
		return scan, nil
	})
}

// Advance 以 expected version 原子推进一个 detector coverage 与 Scan 聚合计数。
func (repository *ScanRepository) Advance(ctx context.Context, progress healthapp.ScanProgress) (domain.Scan, error) {
	if repository == nil || nilScanValue(repository.db) {
		return domain.Scan{}, repositoryUnavailable(errors.New("health scan state repository is unavailable"))
	}
	updated, err := withHealthTransaction(ctx, repository.db, foundation.TransactionOptions{}, func(ctx context.Context, tx healthTransaction) (domain.Scan, error) {
		return repository.advanceTx(ctx, tx, progress)
	})
	if healthCommitFailed(err) {
		if recovered, recoveryErr := loadScanByID(ctx, repository.db, progress.WorkspaceID, progress.ScanID, false); recoveryErr == nil {
			if result, ok := recoverProgress(recovered, progress); ok {
				return result, nil
			}
		} else {
			return domain.Scan{}, errors.Join(classifyScanError(err, "HEALTH_SCAN_PROGRESS_COMMIT_FAILED"), recoveryErr)
		}
		return domain.Scan{}, classifyScanError(err, "HEALTH_SCAN_PROGRESS_COMMIT_FAILED")
	}
	if err != nil {
		return domain.Scan{}, classifyHealthTransactionError(err, "HEALTH_SCAN_PROGRESS_BEGIN_FAILED", "HEALTH_SCAN_PROGRESS_COMMIT_FAILED")
	}
	return updated, nil
}

func (repository *ScanRepository) advanceTx(ctx context.Context, tx healthTransaction, progress healthapp.ScanProgress) (domain.Scan, error) {
	scan, err := loadScanByID(ctx, tx, progress.WorkspaceID, progress.ScanID, true)
	if err != nil {
		return domain.Scan{}, err
	}
	if scan.Version != progress.ExpectedVersion || (scan.Status != domain.ScanStatusPending && scan.Status != domain.ScanStatusRunning) {
		if recovered, ok := recoverProgress(scan, progress); ok {
			return recovered, nil
		}
		return domain.Scan{}, repositoryConflict("health scan progress expected version did not match")
	}
	current, found := coverageItem(scan.Coverage, progress.DetectorID)
	if !found {
		return domain.Scan{}, repositoryInvalid(errors.New("health scan detector coverage does not exist"))
	}
	if !validCoverageTransition(current.Status, progress.Status) {
		return domain.Scan{}, repositoryConflict("health scan detector transition is invalid")
	}
	checkpoint, err := encodeCheckpoint(progress.Checkpoint)
	if err != nil {
		return domain.Scan{}, err
	}
	failure, err := encodeFailure(progress.LastError)
	if err != nil {
		return domain.Scan{}, err
	}
	now, err := databaseTime(ctx, tx)
	if err != nil {
		return domain.Scan{}, err
	}
	terminalCoverage := isTerminalCoverage(progress.Status)
	detectorTag, err := tx.Exec(ctx, `UPDATE ops.health_scan_detector
SET status=$4,checkpoint=$5::jsonb,failure_summary=$6::jsonb,unavailable_reason=$7,
    processed_count=processed_count+$8,created_count=created_count+$9,reopened_count=reopened_count+$10,
    resolved_count=resolved_count+$11,unchanged_count=unchanged_count+$12,failed_count=failed_count+$13,
    started_at=COALESCE(started_at,$14),completed_at=CASE WHEN $15 THEN $14 ELSE NULL END
WHERE scan_id=$1 AND workspace_id=$2 AND detector_id=$3`, string(progress.ScanID), string(progress.WorkspaceID), progress.DetectorID,
		string(progress.Status), checkpoint, failure, nullableString(progress.UnavailableReason), progress.CountersDelta.Processed,
		progress.CountersDelta.Created, progress.CountersDelta.Reopened, progress.CountersDelta.Resolved,
		progress.CountersDelta.Unchanged, progress.CountersDelta.Failed, now, terminalCoverage)
	if err != nil {
		return domain.Scan{}, classifyScanError(err, "HEALTH_SCAN_DETECTOR_PROGRESS_FAILED")
	}
	if detectorTag.RowsAffected() != 1 {
		return domain.Scan{}, repositoryConflict("health scan detector progress CAS did not match")
	}
	scanTag, err := tx.Exec(ctx, `UPDATE ops.health_scan
SET status='RUNNING',checkpoint=$4::jsonb,
    processed_count=processed_count+$5,created_count=created_count+$6,reopened_count=reopened_count+$7,
    resolved_count=resolved_count+$8,unchanged_count=unchanged_count+$9,failed_count=failed_count+$10,
    version=version+1,updated_at=$11
WHERE id=$1 AND workspace_id=$2 AND version=$3 AND status IN ('PENDING','RUNNING')`, string(progress.ScanID), string(progress.WorkspaceID),
		progress.ExpectedVersion, checkpoint, progress.CountersDelta.Processed, progress.CountersDelta.Created,
		progress.CountersDelta.Reopened, progress.CountersDelta.Resolved, progress.CountersDelta.Unchanged,
		progress.CountersDelta.Failed, now)
	if err != nil {
		return domain.Scan{}, classifyScanError(err, "HEALTH_SCAN_PROGRESS_FAILED")
	}
	if scanTag.RowsAffected() != 1 {
		return domain.Scan{}, repositoryConflict("health scan progress CAS did not match")
	}
	updated, err := loadScanByID(ctx, tx, progress.WorkspaceID, progress.ScanID, false)
	if err != nil {
		return domain.Scan{}, err
	}
	return updated, nil
}

// Finish 使用 DB time 与 expected version 将 Scan 推进为 durable 终态。
func (repository *ScanRepository) Finish(ctx context.Context, terminal healthapp.ScanTerminal) (domain.Scan, error) {
	if repository == nil || nilScanValue(repository.db) || nilScanValue(repository.events) {
		return domain.Scan{}, repositoryUnavailable(errors.New("health scan state repository is unavailable"))
	}
	updated, err := withHealthTransaction(ctx, repository.db, foundation.TransactionOptions{}, func(ctx context.Context, tx healthTransaction) (domain.Scan, error) {
		return repository.finishTx(ctx, tx, terminal)
	})
	if healthCommitFailed(err) {
		if recovered, recoveryErr := loadScanByID(ctx, repository.db, terminal.WorkspaceID, terminal.ScanID, false); recoveryErr == nil {
			if recovered.Version == terminal.ExpectedVersion+1 && recovered.Status == terminal.Status && sameFailure(recovered.LastError, terminal.LastError) {
				return recovered, nil
			}
		} else {
			return domain.Scan{}, errors.Join(classifyScanError(err, "HEALTH_SCAN_FINISH_COMMIT_FAILED"), recoveryErr)
		}
		return domain.Scan{}, classifyScanError(err, "HEALTH_SCAN_FINISH_COMMIT_FAILED")
	}
	if err != nil {
		return domain.Scan{}, classifyHealthTransactionError(err, "HEALTH_SCAN_FINISH_BEGIN_FAILED", "HEALTH_SCAN_FINISH_COMMIT_FAILED")
	}
	return updated, nil
}

func (repository *ScanRepository) finishTx(ctx context.Context, tx healthTransaction, terminal healthapp.ScanTerminal) (domain.Scan, error) {
	scan, err := loadScanByID(ctx, tx, terminal.WorkspaceID, terminal.ScanID, true)
	if err != nil {
		return domain.Scan{}, err
	}
	if scan.Version != terminal.ExpectedVersion || (scan.Status != domain.ScanStatusPending && scan.Status != domain.ScanStatusRunning) {
		if scan.Version == terminal.ExpectedVersion+1 && scan.Status == terminal.Status && sameFailure(scan.LastError, terminal.LastError) {
			return scan, nil
		}
		return domain.Scan{}, repositoryConflict("health scan terminal expected version did not match")
	}
	if terminal.Status == domain.ScanStatusSucceeded && !domain.CoverageComplete(scan.Coverage) {
		return domain.Scan{}, repositoryConflict("health scan success requires complete detector coverage")
	}
	if terminal.Status == domain.ScanStatusPartial && domain.CoverageComplete(scan.Coverage) {
		return domain.Scan{}, repositoryConflict("partial health scan requires incomplete detector coverage")
	}
	failure, err := encodeFailure(terminal.LastError)
	if err != nil {
		return domain.Scan{}, err
	}
	now, err := databaseTime(ctx, tx)
	if err != nil {
		return domain.Scan{}, err
	}
	if terminal.Status == domain.ScanStatusCancelled {
		if _, err := tx.Exec(ctx, `UPDATE ops.health_scan_detector
SET status='CANCELLED',failure_summary=NULL,unavailable_reason=NULL,
    started_at=COALESCE(started_at,$3),completed_at=$3
WHERE scan_id=$1 AND workspace_id=$2 AND status IN ('PENDING','RUNNING')`, string(terminal.ScanID), string(terminal.WorkspaceID), now); err != nil {
			return domain.Scan{}, classifyScanError(err, "HEALTH_SCAN_FINISH_CANCEL_COVERAGE_FAILED")
		}
	}
	commandTag, err := tx.Exec(ctx, `UPDATE ops.health_scan
SET status=$4,last_error=$5::jsonb,version=version+1,updated_at=$6,completed_at=$6
WHERE id=$1 AND workspace_id=$2 AND version=$3 AND status IN ('PENDING','RUNNING')`, string(terminal.ScanID), string(terminal.WorkspaceID),
		terminal.ExpectedVersion, string(terminal.Status), failure, now)
	if err != nil {
		return domain.Scan{}, classifyScanError(err, "HEALTH_SCAN_FINISH_FAILED")
	}
	if commandTag.RowsAffected() != 1 {
		return domain.Scan{}, repositoryConflict("health scan terminal CAS did not match")
	}
	updated, err := loadScanByID(ctx, tx, terminal.WorkspaceID, terminal.ScanID, false)
	if err != nil {
		return domain.Scan{}, err
	}
	if err := appendHealthScanCompletedEvent(ctx, tx, repository.events, updated, now); err != nil {
		return domain.Scan{}, err
	}
	return updated, nil
}

// cancelHealthWorkflowNode 在调用方 scope 内取消 Scan，不提交或回滚事务。
func cancelHealthWorkflowNode(ctx context.Context, tx healthTransaction, appender eventsapp.ScopedAppender, nodeRunID foundation.ID) (bool, error) {
	if nilScanValue(appender) || !validID(nodeRunID) {
		return false, repositoryInvalid(errors.New("health scan cancellation guard or node is invalid"))
	}
	var scanID, workspaceID, workflowRunID, status string
	var version int64
	err := tx.QueryRow(ctx, `SELECT scan.id::text,scan.workspace_id::text,scan.workflow_run_id::text,scan.status,scan.version
FROM ops.health_scan scan JOIN workflow.node_run node ON node.run_id=scan.workflow_run_id
WHERE node.id=$1 FOR UPDATE OF scan`, string(nodeRunID)).Scan(&scanID, &workspaceID, &workflowRunID, &status, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, classifyScanError(err, "HEALTH_SCAN_CANCEL_QUERY_FAILED")
	}
	switch domain.ScanStatus(status) {
	case domain.ScanStatusPending, domain.ScanStatusRunning:
		now, err := databaseTime(ctx, tx)
		if err != nil {
			return false, err
		}
		if _, err := tx.Exec(ctx, `UPDATE ops.health_scan_detector
SET status='CANCELLED',failure_summary=NULL,unavailable_reason=NULL,
    started_at=COALESCE(started_at,$3),completed_at=$3
WHERE scan_id=$1 AND workspace_id=$2 AND status IN ('PENDING','RUNNING')`, scanID, workspaceID, now); err != nil {
			return false, classifyScanError(err, "HEALTH_SCAN_CANCEL_COVERAGE_FAILED")
		}
		commandTag, err := tx.Exec(ctx, `UPDATE ops.health_scan
SET status='CANCELLED',last_error=NULL,version=version+1,updated_at=$4,completed_at=$4
WHERE id=$1 AND workspace_id=$2 AND version=$3 AND status IN ('PENDING','RUNNING')`, scanID, workspaceID, version, now)
		if err != nil {
			return false, classifyScanError(err, "HEALTH_SCAN_CANCEL_FAILED")
		}
		if commandTag.RowsAffected() != 1 {
			return false, nil
		}
		workspace, parseErr := foundation.ParseID(workspaceID)
		if parseErr != nil {
			return false, repositoryConsistency(parseErr)
		}
		scan, parseErr := foundation.ParseID(scanID)
		if parseErr != nil {
			return false, repositoryConsistency(parseErr)
		}
		run, parseErr := foundation.ParseID(workflowRunID)
		if parseErr != nil {
			return false, repositoryConsistency(parseErr)
		}
		if err := appendHealthScanCompletedEvent(ctx, tx, appender, domain.Scan{
			ID: scan, WorkspaceID: workspace, WorkflowRunID: run, Status: domain.ScanStatusCancelled, Version: version + 1,
		}, now); err != nil {
			return false, err
		}
		return true, nil
	case domain.ScanStatusCancelled:
		return true, nil
	case domain.ScanStatusSucceeded, domain.ScanStatusPartial, domain.ScanStatusFailed:
		return false, repositoryConflict("health scan completed before cancellation")
	default:
		return false, repositoryConsistency(errors.New("health scan cancellation observed an invalid status"))
	}
}

func appendHealthScanCompletedEvent(ctx context.Context, tx healthTransaction, appender eventsapp.ScopedAppender, scan domain.Scan, occurredAt time.Time) error {
	workflowRunID := scan.WorkflowRunID
	_, replayed, err := appender.AppendScoped(ctx, tx.scope, eventsdomain.AppendRequest{
		WorkspaceID: scan.WorkspaceID, WorkflowRunID: &workflowRunID,
		Type: "health.scan.completed", ResourceRef: "health_scan:" + string(scan.ID), ResourceVersion: scan.Version,
		PayloadSummary: eventsdomain.PayloadSummary{Status: strings.ToLower(string(scan.Status))}, SchemaVersion: 1,
		SourceEventRef: "health.scan.completed:" + string(scan.ID) + ":v" + strconv.FormatInt(scan.Version, 10),
		OccurredAt:     occurredAt,
	})
	if err != nil {
		return err
	}
	if replayed {
		return repositoryConsistency(errors.New("new health scan terminal transition reused a completion event"))
	}
	return nil
}

func insertScan(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) healthRow
}, scan domain.Scan) (bool, error) {
	checkpoint, err := encodeCheckpoint(scan.Checkpoint)
	if err != nil {
		return false, err
	}
	var id string
	err = db.QueryRow(ctx, `INSERT INTO ops.health_scan(
id,workspace_id,scope_type,scope_ref,scope_version,scope_schema_version,scope_hash,scope_read_model_revision,scope_exact_count,
fingerprint,idempotency_key,request_hash,workflow_run_id,max_items,status,checkpoint,last_error,
processed_count,created_count,reopened_count,resolved_count,unchanged_count,failed_count,
version,created_at,updated_at,completed_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16::jsonb,NULL,$17,$18,$19,$20,$21,$22,$23,$24,$25,NULL)
ON CONFLICT (workspace_id,idempotency_key) DO NOTHING RETURNING id::text`, string(scan.ID), string(scan.WorkspaceID), string(scan.Scope.Type),
		string(scan.Scope.Ref), scan.Scope.Version, scan.Scope.SchemaVersion, nullableString(scan.Scope.Hash), nullableString(scan.Scope.ReadModelRevision), nullableScopeExactCount(scan.Scope), scan.Fingerprint,
		scan.IdempotencyKey, scan.RequestHash, string(scan.WorkflowRunID), scan.MaxItems, string(scan.Status), checkpoint,
		scan.Counters.Processed, scan.Counters.Created, scan.Counters.Reopened, scan.Counters.Resolved, scan.Counters.Unchanged,
		scan.Counters.Failed, scan.Version, scan.CreatedAt.UTC(), scan.UpdatedAt.UTC()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, classifyScanError(err, "HEALTH_SCAN_CREATE_FAILED")
	}
	return id != "", nil
}

func insertCoverage(ctx context.Context, tx healthTransaction, scanID, workspaceID foundation.ID, coverage []domain.DetectorCoverage, now time.Time) error {
	emptyCheckpoint, err := encodeCheckpoint(domain.ScanCheckpoint{})
	if err != nil {
		return err
	}
	for _, item := range coverage {
		if _, err := tx.Exec(ctx, `INSERT INTO ops.health_scan_detector(
scan_id,workspace_id,detector_id,detector_version,status,checkpoint,failure_summary,unavailable_reason,
processed_count,created_count,reopened_count,resolved_count,unchanged_count,failed_count,started_at,completed_at)
VALUES($1,$2,$3,$4,'PENDING',$5::jsonb,NULL,NULL,0,0,0,0,0,0,NULL,NULL)`, string(scanID), string(workspaceID), item.DetectorID, item.DetectorVersion, emptyCheckpoint); err != nil {
			return classifyScanError(err, "HEALTH_SCAN_COVERAGE_CREATE_FAILED")
		}
		if item.Status == domain.DetectorCoverageStatusUnavailable {
			if _, err := tx.Exec(ctx, `UPDATE ops.health_scan_detector
SET status='UNAVAILABLE',unavailable_reason=$4,started_at=$5,completed_at=$5
WHERE scan_id=$1 AND workspace_id=$2 AND detector_id=$3 AND status='PENDING'`, string(scanID), string(workspaceID), item.DetectorID, item.UnavailableReason, now); err != nil {
				return classifyScanError(err, "HEALTH_SCAN_COVERAGE_UNAVAILABLE_FAILED")
			}
		}
	}
	return nil
}

func loadScanByID(ctx context.Context, db healthReadDB, workspaceID, scanID foundation.ID, forUpdate bool) (domain.Scan, error) {
	query := scanSelectSQL + ` WHERE id=$1 AND workspace_id=$2`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	var scan domain.Scan
	err := scanHealthScan(db.QueryRow(ctx, query, string(scanID), string(workspaceID)), &scan)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Scan{}, repositoryNotFound(err)
	}
	if err != nil {
		return domain.Scan{}, err
	}
	coverage, err := loadCoverage(ctx, db, scan.ID, scan.WorkspaceID, forUpdate)
	if err != nil {
		return domain.Scan{}, err
	}
	scan.Coverage = coverage
	if err := domain.ValidateScan(scan); err != nil {
		return domain.Scan{}, err
	}
	return scan, nil
}

func loadScanByIdempotency(ctx context.Context, db healthReadDB, workspaceID foundation.ID, key string, forUpdate bool) (domain.Scan, bool, error) {
	query := scanSelectSQL + ` WHERE workspace_id=$1 AND idempotency_key=$2`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	var scan domain.Scan
	err := scanHealthScan(db.QueryRow(ctx, query, string(workspaceID), key), &scan)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Scan{}, false, nil
	}
	if err != nil {
		return domain.Scan{}, false, err
	}
	coverage, err := loadCoverage(ctx, db, scan.ID, scan.WorkspaceID, forUpdate)
	if err != nil {
		return domain.Scan{}, false, err
	}
	scan.Coverage = coverage
	if err := domain.ValidateScan(scan); err != nil {
		return domain.Scan{}, false, err
	}
	return scan, true, nil
}

func loadCoverage(ctx context.Context, db healthReadDB, scanID, workspaceID foundation.ID, forUpdate bool) ([]domain.DetectorCoverage, error) {
	query := `SELECT detector_id,detector_version,status,checkpoint,failure_summary,unavailable_reason,
processed_count,created_count,reopened_count,resolved_count,unchanged_count,failed_count
FROM ops.health_scan_detector WHERE scan_id=$1 AND workspace_id=$2 ORDER BY detector_id`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	rows, err := db.Query(ctx, query, string(scanID), string(workspaceID))
	if err != nil {
		return nil, classifyScanError(err, "HEALTH_SCAN_COVERAGE_QUERY_FAILED")
	}
	defer rows.Close()
	var result []domain.DetectorCoverage
	for rows.Next() {
		var item domain.DetectorCoverage
		var status string
		var checkpointRaw, failureRaw []byte
		var unavailableReason *string
		if err := rows.Scan(&item.DetectorID, &item.DetectorVersion, &status, &checkpointRaw, &failureRaw, &unavailableReason,
			&item.Counters.Processed, &item.Counters.Created, &item.Counters.Reopened, &item.Counters.Resolved,
			&item.Counters.Unchanged, &item.Counters.Failed); err != nil {
			return nil, classifyScanError(err, "HEALTH_SCAN_COVERAGE_SCAN_FAILED")
		}
		item.Status = domain.DetectorCoverageStatus(status)
		item.Checkpoint, err = decodeCheckpoint(checkpointRaw)
		if err != nil {
			return nil, err
		}
		item.LastError, err = decodeFailure(failureRaw)
		if err != nil {
			return nil, err
		}
		if unavailableReason != nil {
			item.UnavailableReason = *unavailableReason
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyScanError(err, "HEALTH_SCAN_COVERAGE_QUERY_FAILED")
	}
	return result, nil
}

func scanHealthScan(row healthRow, target *domain.Scan) error {
	var id, workspaceID, workflowRunID, scopeType, status string
	var scopeRef string
	var scopeHash, scopeReadModelRevision *string
	var scopeExactCount *int64
	var checkpointRaw, failureRaw []byte
	var completedAt *time.Time
	if err := row.Scan(&id, &workspaceID, &workflowRunID, &scopeType, &scopeRef, &target.Scope.Version, &target.Scope.SchemaVersion,
		&scopeHash, &scopeReadModelRevision, &scopeExactCount, &target.Fingerprint, &target.IdempotencyKey, &target.RequestHash, &target.MaxItems, &status,
		&checkpointRaw, &failureRaw, &target.Counters.Processed, &target.Counters.Created, &target.Counters.Reopened,
		&target.Counters.Resolved, &target.Counters.Unchanged, &target.Counters.Failed, &target.Version,
		&target.CreatedAt, &target.UpdatedAt, &completedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return classifyScanError(err, "HEALTH_SCAN_QUERY_FAILED")
	}
	target.ID, target.WorkspaceID, target.WorkflowRunID = foundation.ID(id), foundation.ID(workspaceID), foundation.ID(workflowRunID)
	target.Scope.Type, target.Scope.Ref = domain.ScanScopeType(scopeType), foundation.ID(scopeRef)
	if scopeHash != nil {
		target.Scope.Hash = *scopeHash
	}
	if scopeReadModelRevision != nil {
		target.Scope.ReadModelRevision = *scopeReadModelRevision
	}
	if scopeExactCount != nil {
		target.Scope.ExactCount = *scopeExactCount
	}
	target.Status = domain.ScanStatus(status)
	checkpoint, err := decodeCheckpoint(checkpointRaw)
	if err != nil {
		return err
	}
	failure, err := decodeFailure(failureRaw)
	if err != nil {
		return err
	}
	target.Checkpoint, target.LastError, target.CompletedAt = checkpoint, failure, completedAt
	return nil
}

type checkpointPayload struct {
	Cursor   string         `json:"cursor,omitempty"`
	Page     int64          `json:"page,omitempty"`
	LastItem *objectPayload `json:"last_item,omitempty"`
}

type objectPayload struct {
	Type domain.ObjectType `json:"type"`
	ID   foundation.ID     `json:"id"`
}

type failurePayload struct {
	Stage     string `json:"stage"`
	Code      string `json:"code"`
	Retryable bool   `json:"retryable"`
}

func encodeCheckpoint(checkpoint domain.ScanCheckpoint) ([]byte, error) {
	payload := checkpointPayload{Cursor: checkpoint.Cursor, Page: checkpoint.Page}
	if checkpoint.LastItem != nil {
		payload.LastItem = &objectPayload{Type: checkpoint.LastItem.Type, ID: checkpoint.LastItem.ID}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, repositoryConsistency(err)
	}
	if len(encoded) > 16_384 || len(checkpoint.Cursor) > 512 || checkpoint.Page < 0 {
		return nil, repositoryInvalid(errors.New("health scan checkpoint is invalid"))
	}
	return encoded, nil
}

func decodeCheckpoint(raw []byte) (domain.ScanCheckpoint, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var payload checkpointPayload
	if err := decoder.Decode(&payload); err != nil {
		return domain.ScanCheckpoint{}, repositoryConsistency(err)
	}
	if err := decoder.Decode(&struct{}{}); err == nil {
		return domain.ScanCheckpoint{}, repositoryConsistency(errors.New("health scan checkpoint contains trailing JSON"))
	}
	checkpoint := domain.ScanCheckpoint{Cursor: payload.Cursor, Page: payload.Page}
	if payload.LastItem != nil {
		checkpoint.LastItem = &domain.ObjectRef{Type: payload.LastItem.Type, ID: payload.LastItem.ID}
	}
	return checkpoint, nil
}

func encodeFailure(summary *domain.FailureSummary) (any, error) {
	if summary == nil {
		return nil, nil
	}
	if strings.TrimSpace(summary.Stage) != summary.Stage || summary.Stage == "" || strings.TrimSpace(summary.Code) != summary.Code || summary.Code == "" || len(summary.Stage) > 128 || len(summary.Code) > 128 || strings.ContainsAny(summary.Stage+summary.Code, "\r\n") {
		return nil, repositoryInvalid(errors.New("health scan failure summary is invalid"))
	}
	return json.Marshal(failurePayload{Stage: summary.Stage, Code: summary.Code, Retryable: summary.Retryable})
}

func decodeFailure(raw []byte) (*domain.FailureSummary, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var payload failurePayload
	if err := decoder.Decode(&payload); err != nil {
		return nil, repositoryConsistency(err)
	}
	if err := decoder.Decode(&struct{}{}); err == nil {
		return nil, repositoryConsistency(errors.New("health scan failure contains trailing JSON"))
	}
	value := &domain.FailureSummary{Stage: payload.Stage, Code: payload.Code, Retryable: payload.Retryable}
	if _, err := encodeFailure(value); err != nil {
		return nil, err
	}
	return value, nil
}

func replayStart(request healthapp.ScanStartRequest, scan domain.Scan) (healthapp.ScanStartResult, error) {
	if scan.WorkspaceID != request.WorkspaceID || scan.Scope != request.Scope || scan.Fingerprint != request.Fingerprint || scan.RequestHash != request.RequestHash || scan.IdempotencyKey != request.IdempotencyKey || scan.MaxItems != request.MaxItems || !sameCoverageBinding(scan.Coverage, request.Coverage) {
		return healthapp.ScanStartResult{}, repositoryConflict("health scan idempotency binding differs")
	}
	return healthapp.ScanStartResult{Scan: scan, Replayed: true}, nil
}

func (repository *ScanRepository) recoverCommittedStart(ctx context.Context, request healthapp.ScanStartRequest) (healthapp.ScanStartResult, bool, error) {
	scan, found, err := loadScanByIdempotency(ctx, repository.db, request.WorkspaceID, request.IdempotencyKey, false)
	if err != nil || !found {
		return healthapp.ScanStartResult{}, found, err
	}
	result, err := replayStart(request, scan)
	return result, true, err
}

func recoverProgress(scan domain.Scan, progress healthapp.ScanProgress) (domain.Scan, bool) {
	if scan.Version != progress.ExpectedVersion+1 || scan.Status != domain.ScanStatusRunning || !sameCheckpoint(scan.Checkpoint, progress.Checkpoint) {
		return domain.Scan{}, false
	}
	item, found := coverageItem(scan.Coverage, progress.DetectorID)
	return scan, found && item.Status == progress.Status && sameCheckpoint(item.Checkpoint, progress.Checkpoint)
}

func coverageItem(values []domain.DetectorCoverage, id string) (domain.DetectorCoverage, bool) {
	for _, item := range values {
		if item.DetectorID == id {
			return item, true
		}
	}
	return domain.DetectorCoverage{}, false
}

func validCoverageTransition(current, next domain.DetectorCoverageStatus) bool {
	switch current {
	case domain.DetectorCoverageStatusPending:
		return next == domain.DetectorCoverageStatusRunning || next == domain.DetectorCoverageStatusFailed || next == domain.DetectorCoverageStatusCancelled || next == domain.DetectorCoverageStatusUnavailable
	case domain.DetectorCoverageStatusRunning:
		return next == domain.DetectorCoverageStatusRunning || next == domain.DetectorCoverageStatusSucceeded || next == domain.DetectorCoverageStatusPartial || next == domain.DetectorCoverageStatusFailed || next == domain.DetectorCoverageStatusCancelled
	default:
		return false
	}
}

func isTerminalCoverage(status domain.DetectorCoverageStatus) bool {
	return status == domain.DetectorCoverageStatusSucceeded || status == domain.DetectorCoverageStatusPartial || status == domain.DetectorCoverageStatusFailed || status == domain.DetectorCoverageStatusCancelled || status == domain.DetectorCoverageStatusUnavailable
}

func allCoverageTerminal(values []domain.DetectorCoverage) bool {
	if len(values) == 0 {
		return false
	}
	for _, item := range values {
		if !isTerminalCoverage(item.Status) {
			return false
		}
	}
	return true
}

func sameCoverageBinding(left, right []domain.DetectorCoverage) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].DetectorID != right[index].DetectorID || left[index].DetectorVersion != right[index].DetectorVersion {
			return false
		}
		if right[index].Status == domain.DetectorCoverageStatusUnavailable && (left[index].Status != domain.DetectorCoverageStatusUnavailable || left[index].UnavailableReason != right[index].UnavailableReason) {
			return false
		}
		if right[index].Status == domain.DetectorCoverageStatusPending && left[index].Status == domain.DetectorCoverageStatusUnavailable {
			return false
		}
	}
	return true
}

func sameCheckpoint(left, right domain.ScanCheckpoint) bool {
	if left.Cursor != right.Cursor || left.Page != right.Page {
		return false
	}
	if left.LastItem == nil || right.LastItem == nil {
		return left.LastItem == nil && right.LastItem == nil
	}
	return *left.LastItem == *right.LastItem
}

func cloneCoverage(values []domain.DetectorCoverage) []domain.DetectorCoverage {
	return append([]domain.DetectorCoverage(nil), values...)
}

func sameFailure(left, right *domain.FailureSummary) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func databaseTime(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) healthRow
}) (time.Time, error) {
	var now time.Time
	if err := db.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&now); err != nil {
		return time.Time{}, classifyScanError(err, "HEALTH_SCAN_DB_TIME_UNAVAILABLE")
	}
	return now.UTC(), nil
}

func inScanSnapshot(ctx context.Context, db healthStore, operation func(context.Context, healthTransaction) (domain.Scan, error)) (domain.Scan, error) {
	result, err := withHealthTransaction(ctx, db, foundation.TransactionOptions{Isolation: foundation.TransactionIsolationRepeatableRead, ReadOnly: true}, operation)
	if err != nil {
		return domain.Scan{}, classifyHealthTransactionError(err, "HEALTH_SCAN_SNAPSHOT_BEGIN_FAILED", "HEALTH_SCAN_SNAPSHOT_COMMIT_FAILED")
	}
	return result, nil
}

func runtimeIdempotencyKey(request healthapp.ScanStartRequest) string {
	digest := sha256.Sum256([]byte(string(request.WorkspaceID) + "\x00" + request.IdempotencyKey))
	return "health-scan:" + hex.EncodeToString(digest[:])
}

func (repository *ScanRepository) verifySmartCollectionBinding(ctx context.Context, tx healthTransaction, request healthapp.ScanStartRequest) error {
	if request.Scope.Type != domain.ScanScopeTypeSmartCollection {
		return nil
	}
	if nilScanValue(repository.verifier) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeScanScopeUnavailable, false, errors.New("health smart-collection binding verifier is unavailable"))
	}
	binding := healthapp.SmartCollectionBinding{
		WorkspaceID: request.WorkspaceID, CollectionID: request.Scope.Ref,
		CollectionVersion: request.Scope.Version, QueryHash: request.Scope.Hash,
		ReadModelRevision: request.Scope.ReadModelRevision, ExactCount: request.Scope.ExactCount,
	}
	if err := repository.verifier.VerifyBindingScoped(ctx, tx.scope, binding); err != nil {
		var classified *foundation.Error
		if errors.As(err, &classified) && (classified.Kind == foundation.ErrorVersionConflict || classified.Kind == foundation.ErrorNotFound) {
			return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeScanScopeStale, false, err)
		}
		return err
	}
	return nil
}

func validateStartRequest(request healthapp.ScanStartRequest) error {
	if !validID(request.WorkspaceID) || request.IdempotencyKey == "" || request.MaxItems < 1 || request.MaxItems > domain.MaxScanItems || request.WorkflowDefinitionKey != healthapp.HealthScanWorkflowDefinitionKey || request.WorkflowDefinitionVersion != healthapp.HealthScanWorkflowDefinitionVersion || request.WorkflowInputSchemaVersion != healthapp.HealthScanInputSchemaVersion || !validHash(request.Fingerprint) || !validHash(request.RequestHash) || len(request.Coverage) == 0 {
		return repositoryInvalid(errors.New("health scan start request is invalid"))
	}
	if err := domain.ValidateScanScope(request.Scope); err != nil {
		return err
	}
	if request.Scope.Type == domain.ScanScopeTypeWorkspace && request.Scope.Ref != request.WorkspaceID {
		return repositoryInvalid(errors.New("health scan workspace scope binding is invalid"))
	}
	if err := validateInitialCoverage(request.Coverage); err != nil {
		return err
	}
	return nil
}

func validateInitialCoverage(values []domain.DetectorCoverage) error {
	seen := make(map[string]struct{}, len(values))
	previous := ""
	for _, item := range values {
		if item.DetectorID == "" || strings.TrimSpace(item.DetectorID) != item.DetectorID || item.DetectorVersion == "" || strings.TrimSpace(item.DetectorVersion) != item.DetectorVersion || item.DetectorID < previous {
			return repositoryInvalid(errors.New("health scan initial coverage identity is invalid"))
		}
		if _, exists := seen[item.DetectorID]; exists {
			return repositoryInvalid(errors.New("health scan initial coverage is duplicated"))
		}
		seen[item.DetectorID] = struct{}{}
		previous = item.DetectorID
		if item.Status != domain.DetectorCoverageStatusPending && item.Status != domain.DetectorCoverageStatusUnavailable {
			return repositoryInvalid(errors.New("health scan initial coverage status is invalid"))
		}
		if item.Checkpoint != (domain.ScanCheckpoint{}) || item.Counters != (domain.ScanCounters{}) || item.LastError != nil {
			return repositoryInvalid(errors.New("health scan initial coverage contains mutable progress"))
		}
		if item.Status == domain.DetectorCoverageStatusUnavailable {
			if strings.TrimSpace(item.UnavailableReason) == "" || strings.TrimSpace(item.UnavailableReason) != item.UnavailableReason {
				return repositoryInvalid(errors.New("health scan unavailable coverage reason is invalid"))
			}
		} else if item.UnavailableReason != "" {
			return repositoryInvalid(errors.New("health scan pending coverage carries an unavailable reason"))
		}
	}
	return nil
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func validHash(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func nullableScopeExactCount(scope domain.ScanScope) any {
	if scope.Type != domain.ScanScopeTypeSmartCollection {
		return nil
	}
	return scope.ExactCount
}

func nilScanValue(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func repositoryInvalid(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeScanInvalid, false, cause)
}

func repositoryConflict(message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeScanTransitionInvalid, false, errors.New(message))
}

func repositoryConsistency(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeScanInvalid, false, cause)
}

func repositoryUnavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, "HEALTH_SCAN_UNAVAILABLE", true, cause)
}

func repositoryNotFound(cause error) error {
	return foundation.NewError(foundation.ErrorNotFound, "HEALTH_SCAN_NOT_FOUND", false, cause)
}

func classifyScanError(err error, code string) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return foundation.NewError(foundation.ErrorNonRetryableFailure, code, false, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return foundation.NewError(foundation.ErrorRetryableFailure, code, true, err)
	}
	switch platformpostgres.SQLState(err) {
	case "40001", "40P01", "55P03", "08000", "08003", "08006", "57P01":
		return foundation.NewError(foundation.ErrorRetryableFailure, code, true, err)
	case "23505":
		return repositoryConflict("health scan uniqueness binding conflicted")
	case "23503", "23514", "55000":
		return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, err)
	}

	return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, err)
}

const scanColumns = `id::text,workspace_id::text,workflow_run_id::text,scope_type,scope_ref::text,
	scope_version,scope_schema_version,scope_hash,scope_read_model_revision,scope_exact_count,fingerprint,idempotency_key,request_hash,max_items,status,
checkpoint,last_error,processed_count,created_count,reopened_count,resolved_count,unchanged_count,failed_count,
version,created_at,updated_at,completed_at`

const scanSelectSQL = `SELECT ` + scanColumns + ` FROM ops.health_scan`
