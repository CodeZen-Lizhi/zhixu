// Package workflow 定义 Health Scan 与通用 durable Workflow 之间的冻结契约。
package workflow

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

// ScanInput 是持久 Workflow Node 的 canonical Health Scan 输入。
type ScanInput struct {
	SchemaVersion           int                       `json:"schema_version"`
	WorkspaceID             foundation.ID             `json:"workspace_id"`
	Scope                   domain.ScanScope          `json:"scope"`
	Coverage                []domain.DetectorCoverage `json:"coverage"`
	Fingerprint             string                    `json:"fingerprint"`
	RequestHash             string                    `json:"request_hash"`
	MaxItems                int64                     `json:"max_items"`
	IdempotencyKey          string                    `json:"idempotency_key"`
	WorkflowVersion         string                    `json:"workflow_version"`
	PreventScopeConcurrency bool                      `json:"prevent_scope_concurrency"`
}

// RegisteredDefinition 返回 API Start 与 Worker Registry 共享的冻结 Definition。
func RegisteredDefinition() (workflowdomain.RegisteredDefinition, error) {
	graph := workflowdomain.CanonicalGraph{Nodes: []workflowdomain.NodeDefinition{{
		Key: healthapp.HealthScanNodeKey, Kind: healthapp.HealthScanNodeKind,
		InputSchemaVersion: healthapp.HealthScanInputSchemaVersion, OutputSchemaVersion: healthapp.HealthScanOutputSchemaVersion,
		RetryPolicy:         workflowdomain.RetryPolicy{MaxRetries: 3, BaseDelay: time.Second, MaxDelay: 30 * time.Second},
		RequiredPermissions: []workflowdomain.Permission{workflowdomain.PermissionReadLocal, workflowdomain.PermissionWriteProposal},
	}}}
	graphHash, err := workflowapp.ComputeCanonicalGraphHash(graph)
	if err != nil {
		return workflowdomain.RegisteredDefinition{}, err
	}
	return workflowdomain.RegisteredDefinition{
		Key: healthapp.HealthScanWorkflowDefinitionKey, Version: healthapp.HealthScanWorkflowDefinitionVersion,
		InputSchemaVersion: healthapp.HealthScanInputSchemaVersion, Graph: graph, GraphHash: graphHash,
	}, nil
}

// EncodeScanInput 生成 Runtime Start 使用的 canonical JSON。
func EncodeScanInput(request healthapp.ScanStartRequest) (json.RawMessage, error) {
	input := ScanInput{
		SchemaVersion: request.WorkflowInputSchemaVersion, WorkspaceID: request.WorkspaceID,
		Scope: request.Scope, Coverage: append([]domain.DetectorCoverage(nil), request.Coverage...),
		Fingerprint: request.Fingerprint, RequestHash: request.RequestHash, MaxItems: request.MaxItems,
		IdempotencyKey: request.IdempotencyKey, WorkflowVersion: strconv.FormatInt(request.WorkflowDefinitionVersion, 10),
		PreventScopeConcurrency: request.PreventScopeConcurrency,
	}
	if err := validateScanInput(input); err != nil {
		return nil, err
	}
	return json.Marshal(input)
}

// DecodeScanInput 严格解析 Worker 输入并拒绝未知字段或尾随 JSON。
func DecodeScanInput(raw json.RawMessage) (ScanInput, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var input ScanInput
	if err := decoder.Decode(&input); err != nil {
		return ScanInput{}, invalid(err)
	}
	if err := decoder.Decode(&struct{}{}); err == nil {
		return ScanInput{}, invalid(errors.New("health scan workflow input contains trailing JSON"))
	}
	if err := validateScanInput(input); err != nil {
		return ScanInput{}, err
	}
	return input, nil
}

func validateScanInput(input ScanInput) error {
	if input.SchemaVersion != healthapp.HealthScanInputSchemaVersion || input.WorkflowVersion != strconv.FormatInt(healthapp.HealthScanWorkflowDefinitionVersion, 10) || !validID(input.WorkspaceID) || !validHash(input.Fingerprint) || !validHash(input.RequestHash) || strings.TrimSpace(input.IdempotencyKey) != input.IdempotencyKey || input.IdempotencyKey == "" || len(input.IdempotencyKey) > 128 || strings.ContainsAny(input.IdempotencyKey, "\r\n") || input.MaxItems < 1 || input.MaxItems > domain.MaxScanItems || len(input.Coverage) == 0 {
		return invalid(errors.New("health scan workflow input is invalid"))
	}
	if err := domain.ValidateScanScope(input.Scope); err != nil {
		return err
	}
	if input.Scope.Type == domain.ScanScopeTypeWorkspace && input.Scope.Ref != input.WorkspaceID {
		return invalid(errors.New("health scan workflow workspace scope is inconsistent"))
	}
	seen := make(map[string]struct{}, len(input.Coverage))
	previous := ""
	for _, item := range input.Coverage {
		if item.DetectorID == "" || strings.TrimSpace(item.DetectorID) != item.DetectorID || item.DetectorVersion == "" || strings.TrimSpace(item.DetectorVersion) != item.DetectorVersion || item.DetectorID < previous {
			return invalid(errors.New("health scan workflow coverage identity is invalid"))
		}
		if _, exists := seen[item.DetectorID]; exists {
			return invalid(errors.New("health scan workflow coverage is duplicated"))
		}
		seen[item.DetectorID] = struct{}{}
		previous = item.DetectorID
		if item.Status != domain.DetectorCoverageStatusPending && item.Status != domain.DetectorCoverageStatusUnavailable {
			return invalid(errors.New("health scan workflow coverage status is invalid"))
		}
		if item.Checkpoint != (domain.ScanCheckpoint{}) || item.Counters != (domain.ScanCounters{}) || item.LastError != nil {
			return invalid(errors.New("health scan workflow coverage contains progress"))
		}
		if item.Status == domain.DetectorCoverageStatusUnavailable {
			if strings.TrimSpace(item.UnavailableReason) == "" || strings.TrimSpace(item.UnavailableReason) != item.UnavailableReason {
				return invalid(errors.New("health scan workflow unavailable reason is invalid"))
			}
		} else if item.UnavailableReason != "" {
			return invalid(errors.New("health scan workflow pending coverage carries unavailable reason"))
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

func invalid(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeScanInvalid, false, cause)
}
