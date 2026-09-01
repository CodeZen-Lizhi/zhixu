package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"gorm.io/gorm"
)

// GORMSemanticLinkScanRepository is the staged Scan implementation. The
// shared Graph repository owns its GORM root and UnitOfWork; Workflow and
// Collection only consume the live scope created here.
type GORMSemanticLinkScanRepository struct {
	repository *GORMRepository
	runtime    workflowapplication.ScopedRuntimeStarter
	collection collectionapp.ScopedDurableScanBindingVerifier
	ids        foundation.IDGenerator
	clock      foundation.Clock
}

var _ graphapp.SemanticLinkScanStartPort = (*GORMSemanticLinkScanRepository)(nil)
var _ graphapp.SemanticLinkScanStatePort = (*GORMSemanticLinkScanRepository)(nil)

// NewGORMSemanticLinkScanRepository constructs the complete staged Scan
// repository from one platform Pool and scoped collaborators.
func NewGORMSemanticLinkScanRepository(
	pool *platformpostgres.Pool,
	runtime workflowapplication.ScopedRuntimeStarter,
	collection collectionapp.ScopedDurableScanBindingVerifier,
	ids foundation.IDGenerator,
	clock foundation.Clock,
) (*GORMSemanticLinkScanRepository, error) {
	if pool == nil || isNilScanDependency(runtime) || isNilScanDependency(collection) || isNilScanDependency(ids) || isNilScanDependency(clock) {
		return nil, scanRepositoryUnavailable(errors.New("semantic link GORM scan dependencies are missing"))
	}
	repository, err := NewGORMRepository(pool)
	if err != nil {
		return nil, err
	}
	return &GORMSemanticLinkScanRepository{repository: repository, runtime: runtime, collection: collection, ids: ids, clock: clock}, nil
}

// NewGORMSemanticLinkScanStateRepository constructs the state-only staged
// repository used by the Workflow executor.
func NewGORMSemanticLinkScanStateRepository(pool *platformpostgres.Pool) (*GORMSemanticLinkScanRepository, error) {
	if pool == nil {
		return nil, scanRepositoryUnavailable(errors.New("semantic link GORM scan pool is missing"))
	}
	repository, err := NewGORMRepository(pool)
	if err != nil {
		return nil, err
	}
	return &GORMSemanticLinkScanRepository{repository: repository}, nil
}

// StartOrReplay atomically creates or replays Collection, Workflow/River and
// Scan facts in one repeatable-read UnitOfWork.
func (repository *GORMSemanticLinkScanRepository) StartOrReplay(ctx context.Context, request graphapp.SemanticLinkScanStartRequest) (graphapp.SemanticLinkScanStartResult, error) {
	if repository == nil || repository.repository == nil || isNilScanDependency(repository.runtime) || isNilScanDependency(repository.collection) || isNilScanDependency(repository.ids) || isNilScanDependency(repository.clock) {
		return graphapp.SemanticLinkScanStartResult{}, scanRepositoryUnavailable(errors.New("semantic link GORM scan start repository is unavailable"))
	}
	if ctx == nil {
		return graphapp.SemanticLinkScanStartResult{}, scanRepositoryInvalid(errors.New("semantic link GORM scan context is nil"))
	}
	if err := validateScanStartRequest(request); err != nil {
		return graphapp.SemanticLinkScanStartResult{}, err
	}

	var result graphapp.SemanticLinkScanStartResult
	callbackEntered := false
	callbackSucceeded := false
	err := gormScanWithin(
		ctx,
		repository.repository,
		foundation.TransactionOptions{Isolation: foundation.TransactionIsolationRepeatableRead},
		"GRAPH_SEMANTIC_LINK_SCAN_START_FAILED",
		"GRAPH_SEMANTIC_LINK_SCAN_COMMIT_FAILED",
		func(callbackCtx context.Context, database *gorm.DB, scope foundation.TransactionScope) error {
			callbackEntered = true
			var startErr error
			result, startErr = repository.startOrReplayScoped(callbackCtx, database, scope, request)
			callbackSucceeded = startErr == nil
			return startErr
		},
	)
	if err == nil {
		return result, nil
	}
	if !callbackSucceeded && (!callbackEntered || !isRetryableScanStartError(err)) {
		return graphapp.SemanticLinkScanStartResult{}, err
	}
	recovered, found, recoveryErr := repository.recoverCommittedStart(ctx, request)
	if recoveryErr != nil {
		return graphapp.SemanticLinkScanStartResult{}, errors.Join(err, recoveryErr)
	}
	if found {
		return recovered, nil
	}
	return graphapp.SemanticLinkScanStartResult{}, err
}

func (repository *GORMSemanticLinkScanRepository) startOrReplayScoped(
	ctx context.Context,
	database *gorm.DB,
	scope foundation.TransactionScope,
	request graphapp.SemanticLinkScanStartRequest,
) (graphapp.SemanticLinkScanStartResult, error) {
	if existing, found, err := gormLoadScanByIdempotency(ctx, database, request.WorkspaceID, request.IdempotencyKey, true); err != nil {
		return graphapp.SemanticLinkScanStartResult{}, err
	} else if found {
		return replayScanStart(request, existing)
	}

	if request.Scope.Type == graphdomain.SemanticLinkScanScopeSmartCollection {
		collectionID, err := foundation.ParseID(request.Scope.Ref)
		if err != nil || collectionID != foundation.ID(request.Scope.Ref) {
			return graphapp.SemanticLinkScanStartResult{}, scanRepositoryInvalid(errors.New("smart collection scan identity is invalid"))
		}
		if err := repository.collection.VerifyDurableScanBindingScoped(ctx, scope, collectionapp.DurableScanBinding{
			WorkspaceID:       request.WorkspaceID,
			CollectionID:      collectionID,
			CollectionVersion: request.Scope.Version,
			QueryHash:         request.Scope.QueryHash,
			ReadModelRevision: request.Scope.ReadModelRevision,
			ExactCount:        request.TotalNodes,
		}); err != nil {
			return graphapp.SemanticLinkScanStartResult{}, err
		}
	}

	scanID, err := repository.ids.New()
	if err != nil {
		return graphapp.SemanticLinkScanStartResult{}, err
	}
	input, err := graphapp.EncodeSemanticLinkScanWorkflowInput(request)
	if err != nil {
		return graphapp.SemanticLinkScanStartResult{}, err
	}
	definition, err := semanticLinkScanDefinition(request)
	if err != nil {
		return graphapp.SemanticLinkScanStartResult{}, err
	}
	runtimeRequest, err := workflowapplication.BuildRuntimeStartRequest(
		repository.ids,
		repository.clock,
		request.WorkspaceID,
		semanticLinkScanWorkflowIdempotencyKey(request),
		input,
		definition,
	)
	if err != nil {
		return graphapp.SemanticLinkScanStartResult{}, scanRepositoryClassify(err, "GRAPH_SEMANTIC_LINK_SCAN_WORKFLOW_START_INVALID")
	}
	runtimeResult, err := repository.runtime.StartScoped(ctx, scope, runtimeRequest)
	if err != nil {
		return graphapp.SemanticLinkScanStartResult{}, scanRepositoryClassify(err, "GRAPH_SEMANTIC_LINK_SCAN_WORKFLOW_START_FAILED")
	}
	now, err := gormScanDatabaseNow(ctx, database)
	if err != nil {
		return graphapp.SemanticLinkScanStartResult{}, err
	}
	scan := graphdomain.SemanticLinkScan{
		ID: scanID, WorkspaceID: request.WorkspaceID, Scope: request.Scope,
		Fingerprint: request.Fingerprint, IdempotencyKey: request.IdempotencyKey, RequestHash: request.RequestHash,
		WorkflowRunID: runtimeResult.Run.ID, Status: graphdomain.SemanticLinkScanStatusPending,
		TotalNodes: request.TotalNodes, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	inserted, err := gormInsertSemanticLinkScan(ctx, database, scan)
	if err != nil {
		return graphapp.SemanticLinkScanStartResult{}, err
	}
	if !inserted {
		existing, found, err := gormLoadScanByIdempotency(ctx, database, request.WorkspaceID, request.IdempotencyKey, true)
		if err != nil {
			return graphapp.SemanticLinkScanStartResult{}, err
		}
		if !found {
			return graphapp.SemanticLinkScanStartResult{}, foundation.NewError(
				foundation.ErrorRetryableFailure,
				graphdomain.ErrorCodeDependencyUnavailable,
				true,
				errors.New("semantic link scan idempotency winner is not visible in the current snapshot"),
			)
		}
		return replayScanStart(request, existing)
	}
	if err := graphdomain.ValidateSemanticLinkScan(scan); err != nil {
		return graphapp.SemanticLinkScanStartResult{}, err
	}
	return graphapp.SemanticLinkScanStartResult{Scan: scan}, nil
}

// Get returns one Workspace-scoped Scan projection.
func (repository *GORMSemanticLinkScanRepository) Get(ctx context.Context, workspaceID, scanID foundation.ID) (graphdomain.SemanticLinkScan, error) {
	if ctx == nil {
		return graphdomain.SemanticLinkScan{}, scanRepositoryInvalid(errors.New("semantic link GORM scan context is nil"))
	}
	if !validID(workspaceID) || !validID(scanID) {
		return graphdomain.SemanticLinkScan{}, scanRepositoryInvalid(errors.New("semantic link scan lookup is invalid"))
	}
	database, err := gormScanRootDB(ctx, repositoryCore(repository))
	if err != nil {
		return graphdomain.SemanticLinkScan{}, err
	}
	return gormLoadScanByID(ctx, database, workspaceID, scanID, false)
}

// GetByWorkflowRun returns the unique Scan bound to a Workflow delivery.
func (repository *GORMSemanticLinkScanRepository) GetByWorkflowRun(ctx context.Context, workspaceID, workflowRunID foundation.ID) (graphdomain.SemanticLinkScan, error) {
	if ctx == nil {
		return graphdomain.SemanticLinkScan{}, scanRepositoryInvalid(errors.New("semantic link GORM scan context is nil"))
	}
	if !validID(workspaceID) || !validID(workflowRunID) {
		return graphdomain.SemanticLinkScan{}, scanRepositoryInvalid(errors.New("semantic link scan workflow lookup is invalid"))
	}
	database, err := gormScanRootDB(ctx, repositoryCore(repository))
	if err != nil {
		return graphdomain.SemanticLinkScan{}, err
	}
	row, err := gormRawRow(ctx, database, scanSelectSQL+` WHERE workspace_id=$1 AND workflow_run_id=$2`, string(workspaceID), string(workflowRunID))
	if err != nil {
		return graphdomain.SemanticLinkScan{}, classifyGORMScan(ctx, err, "GRAPH_SEMANTIC_LINK_SCAN_QUERY_FAILED")
	}
	var scan graphdomain.SemanticLinkScan
	err = scanSemanticLinkScan(row, &scan)
	if gormGraphNoRows(err) {
		return graphdomain.SemanticLinkScan{}, notFound(graphdomain.ErrorCodeSemanticLinkScanNotFound, err)
	}
	return scan, err
}

// AdvancePage locks and advances a Scan in one default read-write UoW.
func (repository *GORMSemanticLinkScanRepository) AdvancePage(ctx context.Context, progress graphdomain.SemanticLinkScanProgress) (graphdomain.SemanticLinkScan, error) {
	if ctx == nil {
		return graphdomain.SemanticLinkScan{}, scanRepositoryInvalid(errors.New("semantic link GORM scan context is nil"))
	}
	if !validID(progress.WorkspaceID) || !validID(progress.ScanID) || progress.ExpectedVersion < 1 ||
		progress.ProcessedDelta < 0 || progress.CandidateDelta < 0 || progress.SuppressedDelta < 0 ||
		progress.ReopenedDelta < 0 || progress.FailedDelta < 0 || progress.Checkpoint.ProcessedPage < 1 ||
		len(progress.Checkpoint.Cursor) > 512 {
		return graphdomain.SemanticLinkScan{}, scanRepositoryInvalid(errors.New("semantic link scan progress is invalid"))
	}
	checkpoint, err := encodeScanCheckpoint(progress.Checkpoint)
	if err != nil {
		return graphdomain.SemanticLinkScan{}, err
	}

	var result graphdomain.SemanticLinkScan
	err = gormScanWithin(
		ctx,
		repositoryCore(repository),
		foundation.TransactionOptions{},
		"GRAPH_SEMANTIC_LINK_SCAN_ADVANCE_FAILED",
		"GRAPH_SEMANTIC_LINK_SCAN_ADVANCE_FAILED",
		func(callbackCtx context.Context, database *gorm.DB, _ foundation.TransactionScope) error {
			row, queryErr := gormRawRow(callbackCtx, database, scanSelectSQL+`
				WHERE id=$1 AND workspace_id=$2 AND version=$3 AND status IN ('PENDING','RUNNING')
				FOR UPDATE`, string(progress.ScanID), string(progress.WorkspaceID), progress.ExpectedVersion)
			if queryErr != nil {
				return queryErr
			}
			var current graphdomain.SemanticLinkScan
			scanErr := scanSemanticLinkScan(row, &current)
			if gormGraphNoRows(scanErr) {
				return scanRepositoryVersionConflict("semantic link scan progress expected version mismatch")
			}
			if scanErr != nil {
				return scanErr
			}
			now, nowErr := gormScanDatabaseNow(callbackCtx, database)
			if nowErr != nil {
				return nowErr
			}
			if now.Before(current.UpdatedAt) {
				now = current.UpdatedAt
			}
			result, scanErr = gormUpdateSemanticLinkScan(callbackCtx, database, `
				UPDATE graph.semantic_link_scan
				SET status='RUNNING',
				    processed_nodes=processed_nodes+$4,
				    candidate_count=candidate_count+$5,
				    suppressed_count=suppressed_count+$6,
				    reopened_count=reopened_count+$7,
				    failed_count=failed_count+$8,
				    checkpoint=$9::jsonb,
				    version=version+1,
				    updated_at=$10
				WHERE id=$1 AND workspace_id=$2 AND version=$3 AND status IN ('PENDING','RUNNING')
				RETURNING `+scanColumns,
				string(progress.ScanID), string(progress.WorkspaceID), progress.ExpectedVersion,
				progress.ProcessedDelta, progress.CandidateDelta, progress.SuppressedDelta, progress.ReopenedDelta, progress.FailedDelta,
				graphJSONB(checkpoint), now)
			return scanErr
		},
	)
	if err != nil {
		return graphdomain.SemanticLinkScan{}, err
	}
	return result, nil
}

// Finish preserves the legacy single-statement terminal CAS.
func (repository *GORMSemanticLinkScanRepository) Finish(ctx context.Context, terminal graphdomain.SemanticLinkScanTerminal) (graphdomain.SemanticLinkScan, error) {
	if ctx == nil {
		return graphdomain.SemanticLinkScan{}, scanRepositoryInvalid(errors.New("semantic link GORM scan context is nil"))
	}
	if !validID(terminal.WorkspaceID) || !validID(terminal.ScanID) || terminal.ExpectedVersion < 1 || terminal.At.IsZero() {
		return graphdomain.SemanticLinkScan{}, scanRepositoryInvalid(errors.New("semantic link scan terminal command is invalid"))
	}
	if terminal.Status != graphdomain.SemanticLinkScanStatusSucceeded && terminal.Status != graphdomain.SemanticLinkScanStatusFailed && terminal.Status != graphdomain.SemanticLinkScanStatusCancelled {
		return graphdomain.SemanticLinkScan{}, scanRepositoryInvalid(errors.New("semantic link scan terminal status is invalid"))
	}
	if terminal.Status == graphdomain.SemanticLinkScanStatusFailed {
		if terminal.Error == nil || strings.TrimSpace(terminal.Error.Stage) == "" || strings.TrimSpace(terminal.Error.Code) == "" {
			return graphdomain.SemanticLinkScan{}, scanRepositoryInvalid(errors.New("failed semantic link scan requires an error summary"))
		}
	} else if terminal.Error != nil {
		return graphdomain.SemanticLinkScan{}, scanRepositoryInvalid(errors.New("non-failed semantic link scan cannot carry an error summary"))
	}
	lastError, err := encodeScanError(terminal.Error)
	if err != nil {
		return graphdomain.SemanticLinkScan{}, err
	}
	if lastError != nil {
		lastError = graphJSONB(lastError.([]byte))
	}
	database, err := gormScanRootDB(ctx, repositoryCore(repository))
	if err != nil {
		return graphdomain.SemanticLinkScan{}, err
	}
	return gormUpdateSemanticLinkScan(ctx, database, `
		UPDATE graph.semantic_link_scan
		SET status=$4,last_error=$5::jsonb,version=version+1,updated_at=$6,completed_at=$6
		WHERE id=$1 AND workspace_id=$2 AND version=$3 AND status IN ('PENDING','RUNNING')
		RETURNING `+scanColumns,
		string(terminal.ScanID), string(terminal.WorkspaceID), terminal.ExpectedVersion, string(terminal.Status), lastError, terminal.At.UTC())
}

func (repository *GORMSemanticLinkScanRepository) recoverCommittedStart(ctx context.Context, request graphapp.SemanticLinkScanStartRequest) (graphapp.SemanticLinkScanStartResult, bool, error) {
	database, err := gormScanRootDB(ctx, repositoryCore(repository))
	if err != nil {
		return graphapp.SemanticLinkScanStartResult{}, false, err
	}
	scan, found, err := gormLoadScanByIdempotency(ctx, database, request.WorkspaceID, request.IdempotencyKey, false)
	if err != nil || !found {
		return graphapp.SemanticLinkScanStartResult{}, found, err
	}
	result, replayErr := replayScanStart(request, scan)
	return result, replayErr == nil, replayErr
}

func repositoryCore(repository *GORMSemanticLinkScanRepository) *GORMRepository {
	if repository == nil {
		return nil
	}
	return repository.repository
}

func gormScanWithin(
	ctx context.Context,
	repository *GORMRepository,
	options foundation.TransactionOptions,
	callbackCode string,
	commitCode string,
	work func(context.Context, *gorm.DB, foundation.TransactionScope) error,
) error {
	if repository == nil || work == nil {
		return scanRepositoryUnavailable(errors.New("semantic link GORM transaction boundary is unavailable"))
	}
	return repository.within(
		ctx,
		options,
		func(classifierCtx context.Context, cause error, stage gormGraphTransactionStage) error {
			code := callbackCode
			if stage == gormGraphTransactionStageCommit {
				code = commitCode
			}
			return classifyGORMScan(classifierCtx, cause, code)
		},
		func(callbackCtx context.Context, scope foundation.TransactionScope, transaction *gorm.DB) error {
			return work(callbackCtx, transaction, scope)
		},
	)
}

func gormScanRootDB(ctx context.Context, repository *GORMRepository) (*gorm.DB, error) {
	if repository == nil {
		return nil, scanRepositoryUnavailable(errors.New("semantic link GORM repository is unavailable"))
	}
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	return repository.database.WithContext(ctx), nil
}

func gormScanDatabaseNow(ctx context.Context, database *gorm.DB) (time.Time, error) {
	row, err := gormRawRow(ctx, database, `SELECT CURRENT_TIMESTAMP`)
	if err != nil {
		return time.Time{}, classifyGORMScan(ctx, err, "GRAPH_SEMANTIC_LINK_SCAN_CLOCK_FAILED")
	}
	var now time.Time
	if err := row.Scan(&now); err != nil {
		return time.Time{}, classifyGORMScan(ctx, err, "GRAPH_SEMANTIC_LINK_SCAN_CLOCK_FAILED")
	}
	return now.UTC(), nil
}

func gormInsertSemanticLinkScan(ctx context.Context, database *gorm.DB, scan graphdomain.SemanticLinkScan) (bool, error) {
	checkpoint, err := encodeScanCheckpoint(scan.Checkpoint)
	if err != nil {
		return false, err
	}
	lastError, err := encodeScanError(scan.LastError)
	if err != nil {
		return false, err
	}
	if lastError != nil {
		lastError = graphJSONB(lastError.([]byte))
	}
	row, err := gormRawRow(ctx, database, `
		INSERT INTO graph.semantic_link_scan(
			id,workspace_id,scope_type,scope_ref,scope_version,scope_schema_version,scope_hash,read_model_revision,
			fingerprint,idempotency_key,request_hash,workflow_run_id,status,total_nodes,
			processed_nodes,candidate_count,suppressed_count,reopened_count,failed_count,
			checkpoint,last_error,version,created_at,updated_at,completed_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20::jsonb,$21::jsonb,$22,$23,$24,$25)
		ON CONFLICT (workspace_id,idempotency_key) DO NOTHING
		RETURNING id::text`,
		string(scan.ID), string(scan.WorkspaceID), string(scan.Scope.Type), scan.Scope.Ref, scan.Scope.Version, scan.Scope.SchemaVersion, nullableScanHash(scan.Scope.QueryHash), nullableScanHash(scan.Scope.ReadModelRevision),
		scan.Fingerprint, scan.IdempotencyKey, scan.RequestHash, string(scan.WorkflowRunID), string(scan.Status), scan.TotalNodes,
		scan.ProcessedNodes, scan.CandidateCount, scan.SuppressedCount, scan.ReopenedCount, scan.FailedCount,
		graphJSONB(checkpoint), lastError, scan.Version, scan.CreatedAt.UTC(), scan.UpdatedAt.UTC(), optionalTime(scan.CompletedAt))
	if err != nil {
		return false, classifyGORMScan(ctx, err, "GRAPH_SEMANTIC_LINK_SCAN_CREATE_FAILED")
	}
	var id string
	if err := row.Scan(&id); gormGraphNoRows(err) {
		return false, nil
	} else if err != nil {
		return false, classifyGORMScan(ctx, err, "GRAPH_SEMANTIC_LINK_SCAN_CREATE_FAILED")
	}
	return id != "", nil
}

func gormLoadScanByID(ctx context.Context, database *gorm.DB, workspaceID, scanID foundation.ID, forUpdate bool) (graphdomain.SemanticLinkScan, error) {
	query := scanSelectSQL + ` WHERE id=$1 AND workspace_id=$2`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	row, err := gormRawRow(ctx, database, query, string(scanID), string(workspaceID))
	if err != nil {
		return graphdomain.SemanticLinkScan{}, classifyGORMScan(ctx, err, "GRAPH_SEMANTIC_LINK_SCAN_QUERY_FAILED")
	}
	var scan graphdomain.SemanticLinkScan
	err = scanSemanticLinkScan(row, &scan)
	if gormGraphNoRows(err) {
		return graphdomain.SemanticLinkScan{}, notFound(graphdomain.ErrorCodeSemanticLinkScanNotFound, err)
	}
	return scan, err
}

func gormLoadScanByIdempotency(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, key string, forUpdate bool) (graphdomain.SemanticLinkScan, bool, error) {
	query := scanSelectSQL + ` WHERE workspace_id=$1 AND idempotency_key=$2`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	row, err := gormRawRow(ctx, database, query, string(workspaceID), key)
	if err != nil {
		return graphdomain.SemanticLinkScan{}, false, classifyGORMScan(ctx, err, "GRAPH_SEMANTIC_LINK_SCAN_QUERY_FAILED")
	}
	var scan graphdomain.SemanticLinkScan
	err = scanSemanticLinkScan(row, &scan)
	if gormGraphNoRows(err) {
		return graphdomain.SemanticLinkScan{}, false, nil
	}
	if err != nil {
		return graphdomain.SemanticLinkScan{}, false, err
	}
	return scan, true, nil
}

func gormUpdateSemanticLinkScan(ctx context.Context, database *gorm.DB, query string, arguments ...any) (graphdomain.SemanticLinkScan, error) {
	row, err := gormRawRow(ctx, database, query, arguments...)
	if err != nil {
		return graphdomain.SemanticLinkScan{}, classifyGORMScan(ctx, err, "GRAPH_SEMANTIC_LINK_SCAN_UPDATE_FAILED")
	}
	var scan graphdomain.SemanticLinkScan
	err = scanSemanticLinkScan(row, &scan)
	if gormGraphNoRows(err) {
		return graphdomain.SemanticLinkScan{}, scanRepositoryVersionConflict("semantic link scan CAS did not match")
	}
	if err != nil {
		return graphdomain.SemanticLinkScan{}, err
	}
	return scan, nil
}

func classifyGORMScan(ctx context.Context, err error, code string) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if cause := graphGORMContextCause(ctx, err); cause != nil {
		return scanRepositoryClassify(cause, code)
	}
	if errors.Is(err, sql.ErrTxDone) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, err)
	}
	return scanRepositoryClassify(err, code)
}
