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
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

func isRetryableScanStartError(err error) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Retryable
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
	if request.WorkflowDefinitionKey != graphapp.SemanticLinkScanWorkflowDefinitionKey {
		return scanRepositoryInvalid(errors.New("semantic link scan workflow binding is invalid"))
	}
	switch request.Scope.Type {
	case graphdomain.SemanticLinkScanScopeTopic:
		if request.WorkflowDefinitionVersion != graphapp.SemanticLinkScanWorkflowDefinitionVersion || request.WorkflowInputSchemaVersion != graphapp.SemanticLinkScanInputSchemaVersion {
			return scanRepositoryInvalid(errors.New("semantic link Topic scan workflow binding is invalid"))
		}
	case graphdomain.SemanticLinkScanScopeSmartCollection:
		if request.WorkflowDefinitionVersion != graphapp.SemanticLinkSmartCollectionScanWorkflowDefinitionVersion || request.WorkflowInputSchemaVersion != graphapp.SemanticLinkSmartCollectionScanInputSchemaVersion {
			return scanRepositoryInvalid(errors.New("semantic link SMART_COLLECTION scan workflow binding is invalid"))
		}
	default:
		return scanRepositoryInvalid(errors.New("semantic link scan scope has no registered workflow"))
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

func semanticLinkScanDefinition(request graphapp.SemanticLinkScanStartRequest) (workflowdomain.RegisteredDefinition, error) {
	switch request.Scope.Type {
	case graphdomain.SemanticLinkScanScopeTopic:
		return graphapp.RegisteredSemanticLinkScanDefinition()
	case graphdomain.SemanticLinkScanScopeSmartCollection:
		return graphapp.RegisteredSemanticLinkSmartCollectionScanDefinition()
	default:
		return workflowdomain.RegisteredDefinition{}, scanRepositoryInvalid(errors.New("semantic link scan scope has no registered workflow"))
	}
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
		&id, &workspaceID, &scopeType, &target.Scope.Ref, &target.Scope.Version, &target.Scope.SchemaVersion, &target.Scope.QueryHash, &target.Scope.ReadModelRevision,
		&fingerprint, &idempotencyKey, &requestHash, &workflowRunID, &status,
		&target.TotalNodes, &target.ProcessedNodes, &target.CandidateCount, &target.SuppressedCount, &target.ReopenedCount, &target.FailedCount,
		&checkpointRaw, &errorRaw, &target.Version, &target.CreatedAt, &target.UpdatedAt, &completedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
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

func nullableScanHash(value string) any {
	if value == "" {
		return nil
	}
	return value
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
	switch platformpostgres.SQLState(err) {
	case "40001", "40P01", "55P03", "08000", "08003", "08006", "57P01":
		return foundation.NewError(foundation.ErrorRetryableFailure, code, true, err)
	case "23505":
		return foundation.NewError(foundation.ErrorVersionConflict, graphdomain.ErrorCodeSemanticLinkScanTransitionInvalid, false, err)
	case "23503", "23514", "55000":
		return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, err)
	}
	return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, err)
}

const scanColumns = `
	id::text,workspace_id::text,scope_type,scope_ref,scope_version,scope_schema_version,COALESCE(scope_hash,''),COALESCE(read_model_revision,''),
	fingerprint,idempotency_key,request_hash,workflow_run_id::text,status,
	total_nodes,processed_nodes,candidate_count,suppressed_count,reopened_count,failed_count,
	checkpoint,last_error,version,created_at,updated_at,completed_at`

const scanSelectSQL = `SELECT ` + scanColumns + ` FROM graph.semantic_link_scan`
