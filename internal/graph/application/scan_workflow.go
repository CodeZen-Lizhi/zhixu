package application

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

const (
	// SemanticLinkScanNodeKey 是 Topic scan Definition 的唯一节点键。
	SemanticLinkScanNodeKey = "semantic-link-scan-page"
	// SemanticLinkScanNodeKind 是 Worker Executor Registry 的稳定类型。
	SemanticLinkScanNodeKind = "graph.semantic_link_topic_scan"
)

// SemanticLinkScanWorkflowInput 是持久 Workflow Node 的无正文输入。
type SemanticLinkScanWorkflowInput struct {
	SchemaVersion   int           `json:"schema_version"`
	WorkspaceID     foundation.ID `json:"workspace_id"`
	ScopeType       string        `json:"scope_type"`
	ScopeRef        string        `json:"scope_ref"`
	ScopeVersion    int64         `json:"scope_version"`
	Fingerprint     string        `json:"fingerprint"`
	RequestHash     string        `json:"request_hash"`
	IdempotencyKey  string        `json:"idempotency_key"`
	WorkflowVersion string        `json:"workflow_version"`
}

// RegisteredSemanticLinkScanDefinition 返回 API Start 与 Worker Registry 共享的冻结 Definition。
func RegisteredSemanticLinkScanDefinition() (workflowdomain.RegisteredDefinition, error) {
	graph := workflowdomain.CanonicalGraph{Nodes: []workflowdomain.NodeDefinition{{
		Key: SemanticLinkScanNodeKey, Kind: SemanticLinkScanNodeKind,
		InputSchemaVersion: SemanticLinkScanInputSchemaVersion, OutputSchemaVersion: SemanticLinkScanOutputSchemaVersion,
		RetryPolicy:         workflowdomain.RetryPolicy{MaxRetries: 3, BaseDelay: time.Second, MaxDelay: 30 * time.Second},
		RequiredPermissions: []workflowdomain.Permission{workflowdomain.PermissionReadLocal, workflowdomain.PermissionWriteProposal},
	}}}
	graphHash, err := workflowapplication.ComputeCanonicalGraphHash(graph)
	if err != nil {
		return workflowdomain.RegisteredDefinition{}, err
	}
	return workflowdomain.RegisteredDefinition{
		Key: SemanticLinkScanWorkflowDefinitionKey, Version: SemanticLinkScanWorkflowDefinitionVersion,
		InputSchemaVersion: SemanticLinkScanInputSchemaVersion, Graph: graph, GraphHash: graphHash,
	}, nil
}

// EncodeSemanticLinkScanWorkflowInput 生成 Runtime Start 使用的 canonical JSON。
func EncodeSemanticLinkScanWorkflowInput(request SemanticLinkScanStartRequest) (json.RawMessage, error) {
	input := SemanticLinkScanWorkflowInput{
		SchemaVersion: request.WorkflowInputSchemaVersion, WorkspaceID: request.WorkspaceID,
		ScopeType: string(request.Scope.Type), ScopeRef: request.Scope.Ref, ScopeVersion: request.Scope.Version,
		Fingerprint: request.Fingerprint, RequestHash: request.RequestHash, IdempotencyKey: request.IdempotencyKey,
		WorkflowVersion: strconv.FormatInt(request.WorkflowDefinitionVersion, 10),
	}
	if err := validateSemanticLinkScanWorkflowInput(input); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, scanConsistency(err)
	}
	return encoded, nil
}

// DecodeSemanticLinkScanWorkflowInput 严格解析 Worker 输入并拒绝未知字段和尾随 JSON。
func DecodeSemanticLinkScanWorkflowInput(raw json.RawMessage) (SemanticLinkScanWorkflowInput, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var input SemanticLinkScanWorkflowInput
	if err := decoder.Decode(&input); err != nil {
		return SemanticLinkScanWorkflowInput{}, scanInvalid(err)
	}
	if err := decoder.Decode(&struct{}{}); err == nil {
		return SemanticLinkScanWorkflowInput{}, scanInvalid(errors.New("semantic link scan workflow input has trailing JSON"))
	}
	if err := validateSemanticLinkScanWorkflowInput(input); err != nil {
		return SemanticLinkScanWorkflowInput{}, err
	}
	return input, nil
}

// Scope 返回输入绑定的 Topic scope。
func (input SemanticLinkScanWorkflowInput) Scope() graphdomain.SemanticLinkScanScope {
	return graphdomain.SemanticLinkScanScope{
		Type: graphdomain.SemanticLinkScanScopeType(input.ScopeType), Ref: input.ScopeRef,
		Version: input.ScopeVersion, SchemaVersion: SemanticLinkTopicScanScopeSchemaVersion,
	}
}

// Generation 返回首版 rule-only 冻结版本。
func (input SemanticLinkScanWorkflowInput) Generation() graphdomain.SemanticLinkScanGeneration {
	ruleID := SemanticLinkScanRuleID
	return graphdomain.SemanticLinkScanGeneration{
		Rule:            graphdomain.SemanticLinkCandidateGeneration{RuleID: &ruleID, RuleVersion: SemanticLinkScanRuleVersion},
		WorkflowVersion: SemanticLinkScanWorkflowGenerationVersion,
	}
}

func validateSemanticLinkScanWorkflowInput(input SemanticLinkScanWorkflowInput) error {
	if input.SchemaVersion != SemanticLinkScanInputSchemaVersion || !validID(input.WorkspaceID) ||
		input.ScopeType != string(graphdomain.SemanticLinkScanScopeTopic) || input.ScopeRef == "" || input.ScopeVersion < 1 ||
		input.Fingerprint == "" || input.RequestHash == "" || input.IdempotencyKey == "" ||
		input.WorkflowVersion != strconv.FormatInt(SemanticLinkScanWorkflowDefinitionVersion, 10) {
		return scanInvalid(errors.New("semantic link scan workflow input is invalid"))
	}
	if err := graphdomain.ValidateSemanticLinkScanScope(input.Scope()); err != nil {
		return err
	}
	if err := graphdomain.ValidateSemanticLinkScanGeneration(input.Generation()); err != nil {
		return err
	}
	return nil
}
