package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// SemanticLinkScanRuntimeStarter 是现有 Workflow Runtime StartTx seam 的最小子集。
// Graph scan 只负责绑定真实 Workflow Run，不直接写 River 表或伪造 workflow_run_id。
type SemanticLinkScanRuntimeStarter interface {
	StartTx(context.Context, pgx.Tx, workflowapplication.RuntimeStartRequest) (workflowapplication.RuntimeStartResult, error)
}

type semanticLinkScanDB interface {
	DB
	Begin(context.Context) (pgx.Tx, error)
}

// SemanticLinkScanRepository 持久化 Semantic Link scan 事实，并在 Start 时复用
// Workflow Runtime 的事务型 Start seam。
type SemanticLinkScanRepository struct {
	db    DB
	begin interface {
		Begin(context.Context) (pgx.Tx, error)
	}
	runtime SemanticLinkScanRuntimeStarter
	ids     foundation.IDGenerator
	clock   foundation.Clock
}

var _ graphapp.SemanticLinkScanStartPort = (*SemanticLinkScanRepository)(nil)
var _ graphapp.SemanticLinkScanStatePort = (*SemanticLinkScanRepository)(nil)

// NewSemanticLinkScanRepository 构造同时支持 Start 和 State CAS 的 scan repository。
func NewSemanticLinkScanRepository(db semanticLinkScanDB, runtime SemanticLinkScanRuntimeStarter, ids foundation.IDGenerator, clock foundation.Clock) (*SemanticLinkScanRepository, error) {
	if isNilScanDependency(db) || isNilScanDependency(runtime) || isNilScanDependency(ids) || isNilScanDependency(clock) {
		return nil, scanRepositoryUnavailable(errors.New("semantic link scan dependencies are missing"))
	}
	return &SemanticLinkScanRepository{db: db, begin: db, runtime: runtime, ids: ids, clock: clock}, nil
}

// NewSemanticLinkScanStateRepository 构造只用于 executor/checkpoint 的 state port。
func NewSemanticLinkScanStateRepository(db DB) (*SemanticLinkScanRepository, error) {
	if isNilScanDependency(db) {
		return nil, scanRepositoryUnavailable(errors.New("semantic link scan database is missing"))
	}
	repository := &SemanticLinkScanRepository{db: db}
	if beginner, ok := db.(interface {
		Begin(context.Context) (pgx.Tx, error)
	}); ok {
		repository.begin = beginner
	}
	return repository, nil
}

// StartOrReplay 在一个事务内创建或精确重放 Workflow Run 与 scan 事实。
func (repository *SemanticLinkScanRepository) StartOrReplay(ctx context.Context, request graphapp.SemanticLinkScanStartRequest) (graphapp.SemanticLinkScanStartResult, error) {
	if repository == nil || isNilScanDependency(repository.begin) || isNilScanDependency(repository.runtime) || isNilScanDependency(repository.ids) || isNilScanDependency(repository.clock) {
		return graphapp.SemanticLinkScanStartResult{}, scanRepositoryUnavailable(errors.New("semantic link scan start repository is unavailable"))
	}
	if err := validateScanStartRequest(request); err != nil {
		return graphapp.SemanticLinkScanStartResult{}, err
	}
	tx, err := repository.begin.Begin(ctx)
	if err != nil {
		return graphapp.SemanticLinkScanStartResult{}, scanRepositoryClassify(err, "GRAPH_SEMANTIC_LINK_SCAN_START_FAILED")
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	result, err := repository.startOrReplayTx(ctx, tx, request)
	if err != nil {
		return graphapp.SemanticLinkScanStartResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		if recovered, found, recoveryErr := repository.recoverCommittedScanStart(ctx, request); recoveryErr != nil {
			return graphapp.SemanticLinkScanStartResult{}, errors.Join(scanRepositoryClassify(err, "GRAPH_SEMANTIC_LINK_SCAN_COMMIT_FAILED"), recoveryErr)
		} else if found {
			return recovered, nil
		}
		return graphapp.SemanticLinkScanStartResult{}, scanRepositoryClassify(err, "GRAPH_SEMANTIC_LINK_SCAN_COMMIT_FAILED")
	}
	return result, nil
}

func (repository *SemanticLinkScanRepository) startOrReplayTx(ctx context.Context, tx pgx.Tx, request graphapp.SemanticLinkScanStartRequest) (graphapp.SemanticLinkScanStartResult, error) {
	if existing, found, err := loadScanByIdempotency(ctx, tx, request.WorkspaceID, request.IdempotencyKey, true); err != nil {
		return graphapp.SemanticLinkScanStartResult{}, err
	} else if found {
		return replayScanStart(request, existing)
	}

	scanID, err := repository.ids.New()
	if err != nil {
		return graphapp.SemanticLinkScanStartResult{}, err
	}
	input, err := graphapp.EncodeSemanticLinkScanWorkflowInput(request)
	if err != nil {
		return graphapp.SemanticLinkScanStartResult{}, err
	}
	definition, err := graphapp.RegisteredSemanticLinkScanDefinition()
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
	runtimeResult, err := repository.runtime.StartTx(ctx, tx, runtimeRequest)
	if err != nil {
		if existing, found, replayErr := loadScanByIdempotency(ctx, tx, request.WorkspaceID, request.IdempotencyKey, true); replayErr != nil {
			return graphapp.SemanticLinkScanStartResult{}, replayErr
		} else if found {
			return replayScanStart(request, existing)
		}
		return graphapp.SemanticLinkScanStartResult{}, scanRepositoryClassify(err, "GRAPH_SEMANTIC_LINK_SCAN_WORKFLOW_START_FAILED")
	}
	now, err := candidateDatabaseNow(ctx, tx)
	if err != nil {
		return graphapp.SemanticLinkScanStartResult{}, err
	}
	scan := graphdomain.SemanticLinkScan{
		ID: scanID, WorkspaceID: request.WorkspaceID, Scope: request.Scope,
		Fingerprint: request.Fingerprint, IdempotencyKey: request.IdempotencyKey, RequestHash: request.RequestHash,
		WorkflowRunID: runtimeResult.Run.ID, Status: graphdomain.SemanticLinkScanStatusPending,
		TotalNodes: request.TotalNodes, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	inserted, err := insertSemanticLinkScan(ctx, tx, scan)
	if err != nil {
		return graphapp.SemanticLinkScanStartResult{}, err
	}
	if !inserted {
		existing, found, err := loadScanByIdempotency(ctx, tx, request.WorkspaceID, request.IdempotencyKey, true)
		if err != nil {
			return graphapp.SemanticLinkScanStartResult{}, err
		}
		if !found {
			return graphapp.SemanticLinkScanStartResult{}, scanRepositoryConsistency(errors.New("semantic link scan idempotency winner is missing"))
		}
		return replayScanStart(request, existing)
	}
	if err := graphdomain.ValidateSemanticLinkScan(scan); err != nil {
		return graphapp.SemanticLinkScanStartResult{}, err
	}
	return graphapp.SemanticLinkScanStartResult{Scan: scan}, nil
}

// Get 返回 Workspace 绑定的 scan 投影。
func (repository *SemanticLinkScanRepository) Get(ctx context.Context, workspaceID, scanID foundation.ID) (graphdomain.SemanticLinkScan, error) {
	if repository == nil || isNilScanDependency(repository.db) {
		return graphdomain.SemanticLinkScan{}, scanRepositoryUnavailable(errors.New("semantic link scan state repository is unavailable"))
	}
	if !validID(workspaceID) || !validID(scanID) {
		return graphdomain.SemanticLinkScan{}, scanRepositoryInvalid(errors.New("semantic link scan lookup is invalid"))
	}
	scan, err := loadScanByID(ctx, repository.db, workspaceID, scanID, false)
	if err != nil {
		return graphdomain.SemanticLinkScan{}, err
	}
	return scan, nil
}

// GetByWorkflowRun 返回 Worker delivery 绑定的唯一 Scan。
func (repository *SemanticLinkScanRepository) GetByWorkflowRun(ctx context.Context, workspaceID, workflowRunID foundation.ID) (graphdomain.SemanticLinkScan, error) {
	if repository == nil || isNilScanDependency(repository.db) || !validID(workspaceID) || !validID(workflowRunID) {
		return graphdomain.SemanticLinkScan{}, scanRepositoryInvalid(errors.New("semantic link scan workflow lookup is invalid"))
	}
	var scan graphdomain.SemanticLinkScan
	err := scanSemanticLinkScan(repository.db.QueryRow(ctx, scanSelectSQL+` WHERE workspace_id=$1 AND workflow_run_id=$2`, string(workspaceID), string(workflowRunID)), &scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return graphdomain.SemanticLinkScan{}, notFound(graphdomain.ErrorCodeSemanticLinkScanNotFound, err)
	}
	return scan, err
}

// AdvancePage 使用 expected_version 推进一页 checkpoint 和计数。
func (repository *SemanticLinkScanRepository) AdvancePage(ctx context.Context, progress graphdomain.SemanticLinkScanProgress) (graphdomain.SemanticLinkScan, error) {
	if repository == nil || isNilScanDependency(repository.db) {
		return graphdomain.SemanticLinkScan{}, scanRepositoryUnavailable(errors.New("semantic link scan state repository is unavailable"))
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
	var scan graphdomain.SemanticLinkScan
	err = scanSemanticLinkScan(repository.db.QueryRow(ctx, scanSelectSQL+`
		WHERE id=$1 AND workspace_id=$2 AND version=$3 AND status IN ('PENDING','RUNNING')
		FOR UPDATE`, string(progress.ScanID), string(progress.WorkspaceID), progress.ExpectedVersion), &scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return graphdomain.SemanticLinkScan{}, scanRepositoryVersionConflict("semantic link scan progress expected version mismatch")
	}
	if err != nil {
		return graphdomain.SemanticLinkScan{}, err
	}
	now, err := candidateDatabaseNow(ctx, repository.db)
	if err != nil {
		return graphdomain.SemanticLinkScan{}, err
	}
	if now.Before(scan.UpdatedAt) {
		now = scan.UpdatedAt
	}
	return updateSemanticLinkScan(ctx, repository.db, `
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
		checkpoint, now)
}

// Finish 将 scan 置为终态，并保存安全错误摘要。
func (repository *SemanticLinkScanRepository) Finish(ctx context.Context, terminal graphdomain.SemanticLinkScanTerminal) (graphdomain.SemanticLinkScan, error) {
	if repository == nil || isNilScanDependency(repository.db) {
		return graphdomain.SemanticLinkScan{}, scanRepositoryUnavailable(errors.New("semantic link scan state repository is unavailable"))
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
	return updateSemanticLinkScan(ctx, repository.db, `
		UPDATE graph.semantic_link_scan
		SET status=$4,last_error=$5::jsonb,version=version+1,updated_at=$6,completed_at=$6
		WHERE id=$1 AND workspace_id=$2 AND version=$3 AND status IN ('PENDING','RUNNING')
		RETURNING `+scanColumns,
		string(terminal.ScanID), string(terminal.WorkspaceID), terminal.ExpectedVersion, string(terminal.Status), lastError, terminal.At.UTC())
}

func updateSemanticLinkScan(ctx context.Context, db DB, query string, args ...any) (graphdomain.SemanticLinkScan, error) {
	var scan graphdomain.SemanticLinkScan
	err := scanSemanticLinkScan(db.QueryRow(ctx, query, args...), &scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return graphdomain.SemanticLinkScan{}, scanRepositoryVersionConflict("semantic link scan CAS did not match")
	}
	if err != nil {
		return graphdomain.SemanticLinkScan{}, err
	}
	if err := graphdomain.ValidateSemanticLinkScan(scan); err != nil {
		return graphdomain.SemanticLinkScan{}, err
	}
	return scan, nil
}

func insertSemanticLinkScan(ctx context.Context, db DB, scan graphdomain.SemanticLinkScan) (bool, error) {
	checkpoint, err := encodeScanCheckpoint(scan.Checkpoint)
	if err != nil {
		return false, err
	}
	lastError, err := encodeScanError(scan.LastError)
	if err != nil {
		return false, err
	}
	var id string
	err = db.QueryRow(ctx, `
		INSERT INTO graph.semantic_link_scan(
			id,workspace_id,scope_type,scope_ref,scope_version,scope_schema_version,
			fingerprint,idempotency_key,request_hash,workflow_run_id,status,total_nodes,
			processed_nodes,candidate_count,suppressed_count,reopened_count,failed_count,
			checkpoint,last_error,version,created_at,updated_at,completed_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18::jsonb,$19::jsonb,$20,$21,$22,$23)
		ON CONFLICT (workspace_id,idempotency_key) DO NOTHING
		RETURNING id::text`,
		string(scan.ID), string(scan.WorkspaceID), string(scan.Scope.Type), scan.Scope.Ref, scan.Scope.Version, scan.Scope.SchemaVersion,
		scan.Fingerprint, scan.IdempotencyKey, scan.RequestHash, string(scan.WorkflowRunID), string(scan.Status), scan.TotalNodes,
		scan.ProcessedNodes, scan.CandidateCount, scan.SuppressedCount, scan.ReopenedCount, scan.FailedCount,
		checkpoint, lastError, scan.Version, scan.CreatedAt.UTC(), scan.UpdatedAt.UTC(), optionalTime(scan.CompletedAt)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, scanRepositoryClassify(err, "GRAPH_SEMANTIC_LINK_SCAN_CREATE_FAILED")
	}
	return id != "", nil
}

func loadScanByID(ctx context.Context, db DB, workspaceID, scanID foundation.ID, forUpdate bool) (graphdomain.SemanticLinkScan, error) {
	query := scanSelectSQL + ` WHERE id=$1 AND workspace_id=$2`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	var scan graphdomain.SemanticLinkScan
	err := scanSemanticLinkScan(db.QueryRow(ctx, query, string(scanID), string(workspaceID)), &scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return graphdomain.SemanticLinkScan{}, notFound(graphdomain.ErrorCodeSemanticLinkScanNotFound, err)
	}
	if err != nil {
		return graphdomain.SemanticLinkScan{}, err
	}
	return scan, nil
}

func loadScanByIdempotency(ctx context.Context, db DB, workspaceID foundation.ID, key string, forUpdate bool) (graphdomain.SemanticLinkScan, bool, error) {
	query := scanSelectSQL + ` WHERE workspace_id=$1 AND idempotency_key=$2`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	var scan graphdomain.SemanticLinkScan
	err := scanSemanticLinkScan(db.QueryRow(ctx, query, string(workspaceID), key), &scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return graphdomain.SemanticLinkScan{}, false, nil
	}
	if err != nil {
		return graphdomain.SemanticLinkScan{}, false, err
	}
	return scan, true, nil
}

func (repository *SemanticLinkScanRepository) recoverCommittedScanStart(ctx context.Context, request graphapp.SemanticLinkScanStartRequest) (graphapp.SemanticLinkScanStartResult, bool, error) {
	if repository == nil || isNilScanDependency(repository.db) {
		return graphapp.SemanticLinkScanStartResult{}, false, nil
	}
	scan, found, err := loadScanByIdempotency(ctx, repository.db, request.WorkspaceID, request.IdempotencyKey, false)
	if err != nil || !found {
		return graphapp.SemanticLinkScanStartResult{}, found, err
	}
	result, replayErr := replayScanStart(request, scan)
	return result, replayErr == nil, replayErr
}

func replayScanStart(request graphapp.SemanticLinkScanStartRequest, scan graphdomain.SemanticLinkScan) (graphapp.SemanticLinkScanStartResult, error) {
	if scan.WorkspaceID != request.WorkspaceID || scan.Scope != request.Scope || scan.Fingerprint != request.Fingerprint || scan.RequestHash != request.RequestHash || scan.IdempotencyKey != request.IdempotencyKey || scan.TotalNodes != request.TotalNodes {
		return graphapp.SemanticLinkScanStartResult{}, scanRepositoryVersionConflict("semantic link scan idempotency binding differs")
	}
	if err := graphdomain.ValidateSemanticLinkScan(scan); err != nil {
		return graphapp.SemanticLinkScanStartResult{}, err
	}
	return graphapp.SemanticLinkScanStartResult{Scan: scan, Replayed: true}, nil
}

func validateScanStartRequest(request graphapp.SemanticLinkScanStartRequest) error {
	if !validID(request.WorkspaceID) || strings.TrimSpace(request.IdempotencyKey) == "" || len(request.IdempotencyKey) > 128 || strings.ContainsAny(request.IdempotencyKey, "\r\n") || request.TotalNodes < 0 || !canonicalScanHash(request.Fingerprint) || !canonicalScanHash(request.RequestHash) {
		return scanRepositoryInvalid(errors.New("semantic link scan start request is invalid"))
	}
	if request.WorkflowDefinitionKey != graphapp.SemanticLinkScanWorkflowDefinitionKey || request.WorkflowDefinitionVersion != graphapp.SemanticLinkScanWorkflowDefinitionVersion || request.WorkflowInputSchemaVersion != graphapp.SemanticLinkScanInputSchemaVersion {
		return scanRepositoryInvalid(errors.New("semantic link scan workflow binding is invalid"))
	}
	if err := graphdomain.ValidateSemanticLinkScanScope(request.Scope); err != nil {
		return err
	}
	if err := graphdomain.ValidateSemanticLinkScanGeneration(request.Generation); err != nil {
		return err
	}
	fingerprint, err := graphdomain.ComputeSemanticLinkScanFingerprint(request.WorkspaceID, request.Scope, request.Generation)
	if err != nil {
		return err
	}
	if fingerprint != request.Fingerprint {
		return scanRepositoryInvalid(errors.New("semantic link scan fingerprint is inconsistent"))
	}
	return nil
}

func semanticLinkScanWorkflowIdempotencyKey(request graphapp.SemanticLinkScanStartRequest) string {
	payload := strings.Join([]string{string(request.WorkspaceID), request.IdempotencyKey}, "\x00")
	digest := sha256.Sum256([]byte(payload))
	return "semantic-link-scan:" + hex.EncodeToString(digest[:])
}

type scanCheckpointPayload struct {
	Cursor        string       `json:"cursor,omitempty"`
	ProcessedPage int64        `json:"processed_page,omitempty"`
	LastNode      *nodeRefWire `json:"last_node,omitempty"`
}

type nodeRefWire struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type scanErrorPayload struct {
	Stage     string `json:"stage"`
	Code      string `json:"code"`
	Retryable bool   `json:"retryable"`
}

func encodeScanCheckpoint(checkpoint graphdomain.SemanticLinkScanCheckpoint) ([]byte, error) {
	if checkpoint.ProcessedPage < 0 || len(checkpoint.Cursor) > 512 {
		return nil, scanRepositoryInvalid(errors.New("semantic link scan checkpoint is invalid"))
	}
	payload := scanCheckpointPayload{Cursor: checkpoint.Cursor, ProcessedPage: checkpoint.ProcessedPage}
	if checkpoint.LastNode != nil {
		if !validScanCheckpointNodeRef(*checkpoint.LastNode) {
			return nil, scanRepositoryInvalid(errors.New("semantic link scan checkpoint node is invalid"))
		}
		payload.LastNode = &nodeRefWire{Type: string(checkpoint.LastNode.Type), ID: string(checkpoint.LastNode.ID)}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, scanRepositoryConsistency(err)
	}
	return encoded, nil
}

func decodeScanCheckpoint(raw []byte) (graphdomain.SemanticLinkScanCheckpoint, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var payload scanCheckpointPayload
	if err := decoder.Decode(&payload); err != nil {
		return graphdomain.SemanticLinkScanCheckpoint{}, scanRepositoryConsistency(err)
	}
	if err := decoder.Decode(&struct{}{}); err == nil {
		return graphdomain.SemanticLinkScanCheckpoint{}, scanRepositoryConsistency(errors.New("semantic link scan checkpoint has trailing JSON"))
	}
	checkpoint := graphdomain.SemanticLinkScanCheckpoint{Cursor: payload.Cursor, ProcessedPage: payload.ProcessedPage}
	if payload.LastNode != nil {
		ref := knowledge.NodeRef{Type: knowledge.NodeType(payload.LastNode.Type), ID: foundation.ID(payload.LastNode.ID)}
		checkpoint.LastNode = &ref
	}
	if _, err := encodeScanCheckpoint(checkpoint); err != nil {
		return graphdomain.SemanticLinkScanCheckpoint{}, err
	}
	return checkpoint, nil
}

func encodeScanError(scanError *graphdomain.SemanticLinkScanError) (any, error) {
	if scanError == nil {
		return nil, nil
	}
	if strings.TrimSpace(scanError.Stage) == "" || strings.TrimSpace(scanError.Code) == "" || len(scanError.Stage) > 128 || len(scanError.Code) > 128 || strings.ContainsAny(scanError.Stage+scanError.Code, "\r\n") {
		return nil, scanRepositoryInvalid(errors.New("semantic link scan error summary is invalid"))
	}
	encoded, err := json.Marshal(scanErrorPayload{Stage: scanError.Stage, Code: scanError.Code, Retryable: scanError.Retryable})
	if err != nil {
		return nil, scanRepositoryConsistency(err)
	}
	return encoded, nil
}

func decodeScanError(raw []byte) (*graphdomain.SemanticLinkScanError, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var payload scanErrorPayload
	if err := decoder.Decode(&payload); err != nil {
		return nil, scanRepositoryConsistency(err)
	}
	if err := decoder.Decode(&struct{}{}); err == nil {
		return nil, scanRepositoryConsistency(errors.New("semantic link scan error has trailing JSON"))
	}
	value := graphdomain.SemanticLinkScanError{Stage: payload.Stage, Code: payload.Code, Retryable: payload.Retryable}
	if _, err := encodeScanError(&value); err != nil {
		return nil, err
	}
	return &value, nil
}

func scanSemanticLinkScan(row rowScanner, target *graphdomain.SemanticLinkScan) error {
	var id, workspaceID, scopeType, fingerprint, idempotencyKey, requestHash, workflowRunID, status string
	var checkpointRaw []byte
	var errorRaw []byte
	var completedAt *time.Time
	if err := row.Scan(
		&id, &workspaceID, &scopeType, &target.Scope.Ref, &target.Scope.Version, &target.Scope.SchemaVersion,
		&fingerprint, &idempotencyKey, &requestHash, &workflowRunID, &status,
		&target.TotalNodes, &target.ProcessedNodes, &target.CandidateCount, &target.SuppressedCount, &target.ReopenedCount, &target.FailedCount,
		&checkpointRaw, &errorRaw, &target.Version, &target.CreatedAt, &target.UpdatedAt, &completedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		return scanRepositoryClassify(err, "GRAPH_SEMANTIC_LINK_SCAN_QUERY_FAILED")
	}
	checkpoint, err := decodeScanCheckpoint(checkpointRaw)
	if err != nil {
		return err
	}
	lastError, err := decodeScanError(errorRaw)
	if err != nil {
		return err
	}
	target.ID = foundation.ID(id)
	target.WorkspaceID = foundation.ID(workspaceID)
	target.Scope.Type = graphdomain.SemanticLinkScanScopeType(scopeType)
	target.Fingerprint = fingerprint
	target.IdempotencyKey = idempotencyKey
	target.RequestHash = requestHash
	target.WorkflowRunID = foundation.ID(workflowRunID)
	target.Status = graphdomain.SemanticLinkScanStatus(status)
	target.Checkpoint = checkpoint
	target.LastError = lastError
	target.CompletedAt = completedAt
	if err := graphdomain.ValidateSemanticLinkScan(*target); err != nil {
		return err
	}
	return nil
}

func canonicalScanHash(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func optionalTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC()
}

func validScanCheckpointNodeRef(ref knowledge.NodeRef) bool {
	return (ref.Type == knowledge.NodeTypeTopic || ref.Type == knowledge.NodeTypeClaim) && validID(ref.ID)
}

func isNilScanDependency(value any) bool {
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

func scanRepositoryInvalid(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, graphdomain.ErrorCodeSemanticLinkScanInvalid, false, cause)
}

func scanRepositoryVersionConflict(message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, graphdomain.ErrorCodeSemanticLinkScanTransitionInvalid, false, errors.New(message))
}

func scanRepositoryConsistency(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, graphdomain.ErrorCodeSemanticLinkScanInvalid, false, cause)
}

func scanRepositoryUnavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, graphdomain.ErrorCodeSemanticLinkDiscoveryUnavailable, true, cause)
}

func scanRepositoryClassify(err error, code string) error {
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
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "40001", "40P01", "55P03", "08000", "08003", "08006", "57P01":
			return foundation.NewError(foundation.ErrorRetryableFailure, code, true, err)
		case "23505":
			return foundation.NewError(foundation.ErrorVersionConflict, graphdomain.ErrorCodeSemanticLinkScanTransitionInvalid, false, err)
		case "23503", "23514", "55000":
			return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, err)
		}
	}
	return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, err)
}

const scanColumns = `
	id::text,workspace_id::text,scope_type,scope_ref,scope_version,scope_schema_version,
	fingerprint,idempotency_key,request_hash,workflow_run_id::text,status,
	total_nodes,processed_nodes,candidate_count,suppressed_count,reopened_count,failed_count,
	checkpoint,last_error,version,created_at,updated_at,completed_at`

const scanSelectSQL = `SELECT ` + scanColumns + ` FROM graph.semantic_link_scan`
