// Package workflow 把 Agent Application 接入持久化 Workflow Executor/Definition Registry。
package workflow

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

const (
	// RelationAssessmentNodeKind 是 Relation Assessment 节点的稳定类型。
	RelationAssessmentNodeKind = "agent.relation-assessment"
	// RelationAssessmentInputSchemaVersion 是节点输入契约版本。
	RelationAssessmentInputSchemaVersion = 1
	// RelationAssessmentOutputSchemaVersion 是节点输出契约版本。
	RelationAssessmentOutputSchemaVersion = 1
	// RelationAssessmentDefinitionKey 是单节点 Relation Assessment Definition 的稳定键。
	RelationAssessmentDefinitionKey = "agent-relation-assessment"
	// RelationAssessmentDefinitionVersion 是当前 Relation Assessment Definition 版本。
	RelationAssessmentDefinitionVersion int64 = 1

	// ErrorCodeCapabilityUnavailable 表示 Agent Workflow 未因 Chat disabled 而注册。
	ErrorCodeCapabilityUnavailable = "AGENT_WORKFLOW_CAPABILITY_UNAVAILABLE"
	// ErrorCodeInputInvalid 表示 Workflow 输入未通过严格契约。
	ErrorCodeInputInvalid = "AGENT_WORKFLOW_INPUT_INVALID"
	// ErrorCodeOutputInvalid 表示模型业务输出与服务端 Run 绑定不一致。
	ErrorCodeOutputInvalid = "AGENT_WORKFLOW_OUTPUT_INVALID"
	// ErrorCodeRunReplayUnsafe 表示既有 Model Run 无法安全重复执行。
	ErrorCodeRunReplayUnsafe = "AGENT_MODEL_RUN_REPLAY_UNSAFE"
	// ErrorCodeRunFinalizationUnknown 表示 Model Run 终态持久化无法确认。
	ErrorCodeRunFinalizationUnknown = "AGENT_MODEL_RUN_RESULT_UNKNOWN"
)

const (
	defaultPromptID       = "agent.relation-assessment"
	defaultPromptVersion  = "v1"
	defaultProfileID      = "agent.default"
	defaultProfileVersion = "v1"
)

// RelationAssessmentWorkflowInput 冻结一次关系分析的版本、检索事实和双侧 Claim。
type RelationAssessmentWorkflowInput struct {
	ProfileRef       agentdomain.ModelProfileRef `json:"profile_ref"`
	PromptRef        agentdomain.PromptRef       `json:"prompt_ref"`
	SchemaRef        agentdomain.SchemaRef       `json:"schema_ref"`
	ReducedSchemaRef agentdomain.SchemaRef       `json:"reduced_schema_ref"`
	Retrieval        agentdomain.RetrievalRef    `json:"retrieval"`
	Candidate        RelationClaimInput          `json:"candidate"`
	Existing         *RelationClaimInput         `json:"existing,omitempty"`
}

// RelationClaimInput 是 Workflow 边界上的 Claim、Applicability 与待校验证据。
type RelationClaimInput struct {
	Node          RelationNodeInput       `json:"node"`
	Statement     string                  `json:"statement"`
	Applicability json.RawMessage         `json:"applicability"`
	Evidence      []RelationEvidenceInput `json:"evidence"`
}

// RelationNodeInput 是 JSON 契约中的 Claim 引用。
type RelationNodeInput struct {
	Type knowledgedomain.NodeType `json:"type"`
	ID   foundation.ID            `json:"id"`
}

// RelationEvidenceInput 是进入 RelationAnalyzer 前尚未判定资格的证据。
type RelationEvidenceInput struct {
	Citation agentdomain.Citation `json:"citation"`
}

// RelationAssessmentWorkflowOutput 是 Workflow 持久化的关系候选、动作与有界运行摘要。
type RelationAssessmentWorkflowOutput struct {
	SchemaVersion int                        `json:"schema_version"`
	ModelRunRef   foundation.ID              `json:"model_run_ref"`
	ResultType    string                     `json:"result_type"`
	Phase         agentdomain.ModelCallPhase `json:"phase"`
	CallCount     int                        `json:"call_count"`
	Usage         agentdomain.TokenUsage     `json:"usage"`
	SchemaRef     agentdomain.SchemaRef      `json:"schema_ref"`
	BusinessJSON  json.RawMessage            `json:"business_json"`
	Action        RelationAction             `json:"action"`
}

// RelationAction 是 Knowledge MapAssessment 候选动作的稳定 JSON 投影。
type RelationAction struct {
	Decision     knowledgedomain.AssessmentDecision `json:"decision"`
	RelationType *knowledgedomain.RelationType      `json:"relation_type,omitempty"`
	Source       *RelationNodeInput                 `json:"source,omitempty"`
	Target       *RelationNodeInput                 `json:"target,omitempty"`
	OpenConflict bool                               `json:"open_conflict"`
}

// DecodeRelationAssessmentWorkflowInput 严格拒绝 unknown、duplicate、trailing 与非法嵌套 JSON。
func DecodeRelationAssessmentWorkflowInput(raw []byte) (RelationAssessmentWorkflowInput, error) {
	limits := agentdomain.DefaultDecodeLimits()
	limits.MaxDocumentBytes = agentapplication.MaxStructuredInputBytes
	return agentdomain.DecodeStrict(raw, limits, validateRelationAssessmentWorkflowInput)
}

func validateRelationAssessmentWorkflowInput(input RelationAssessmentWorkflowInput) error {
	if input.ProfileRef.Validate() != nil || input.PromptRef.Validate() != nil || input.SchemaRef.Validate() != nil ||
		input.ReducedSchemaRef.Validate() != nil || input.Retrieval.Validate() != nil || validateRelationClaimBoundary(input.Candidate) != nil {
		return workflowError(foundation.ErrorInvalidInput, ErrorCodeInputInvalid, false, errors.New("agent workflow input is invalid"))
	}
	if input.Existing != nil {
		if validateRelationClaimBoundary(*input.Existing) != nil || input.Existing.Node.ID == input.Candidate.Node.ID {
			return workflowError(foundation.ErrorInvalidInput, ErrorCodeInputInvalid, false, errors.New("existing relation claim is invalid"))
		}
	}
	if !relationEvidenceMatchesRuntime(input.Candidate.Evidence, input.Retrieval) ||
		(input.Existing != nil && !relationEvidenceMatchesRuntime(input.Existing.Evidence, input.Retrieval)) {
		return workflowError(foundation.ErrorInvalidInput, ErrorCodeInputInvalid, false, errors.New("relation evidence differs from the workflow retrieval scope"))
	}
	if !validSchemaPair(input.SchemaRef, input.ReducedSchemaRef) {
		return workflowError(foundation.ErrorInvalidInput, ErrorCodeInputInvalid, false, errors.New("agent primary and reduced schemas are not an allowed pair"))
	}
	return nil
}

func validateRelationClaimBoundary(input RelationClaimInput) error {
	if input.Node.Type != knowledgedomain.NodeTypeClaim || !validWorkflowID(input.Node.ID) ||
		!utf8.ValidString(input.Statement) || strings.TrimSpace(input.Statement) != input.Statement || input.Statement == "" ||
		len(input.Statement) > agentapplication.MaxRelationStatementBytes || len(input.Evidence) == 0 ||
		len(input.Evidence) > agentapplication.MaxRelationEvidence {
		return errors.New("relation claim boundary is invalid")
	}
	if _, err := knowledgedomain.ParseApplicability(input.Applicability); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(input.Evidence))
	for _, evidence := range input.Evidence {
		if evidence.Citation.Validate() != nil {
			return errors.New("relation evidence boundary is invalid")
		}
		if _, duplicate := seen[evidence.Citation.ID]; duplicate {
			return errors.New("relation evidence citation is duplicated")
		}
		seen[evidence.Citation.ID] = struct{}{}
	}
	return nil
}

func relationEvidenceMatchesRuntime(evidence []RelationEvidenceInput, retrieval agentdomain.RetrievalRef) bool {
	for _, item := range evidence {
		if item.Citation.IndexVersionID != retrieval.IndexVersionID {
			return false
		}
	}
	return true
}

func validSchemaPair(primary, reduced agentdomain.SchemaRef) bool {
	if primary.Version != agentdomain.OutputSchemaVersionV1 || reduced.Version != agentdomain.OutputSchemaVersionV1 {
		return false
	}
	switch primary.ID {
	case agentdomain.RelationAssessmentSchemaID:
		return reduced.ID == agentapplication.RelationAssessmentReducedSchemaID
	default:
		// RAG Answer 必须先经过 Citation/Openability/Eligibility/Faithfulness 门禁；
		// Faithfulness 必须使用 phase=REVIEW，二者由后续专用 Executor 接入。
		return false
	}
}

func validWorkflowID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

// RegisteredDefinition 返回可注册的版本化单节点 Agent Definition。
func RegisteredDefinition() workflowdomain.RegisteredDefinition {
	return workflowdomain.RegisteredDefinition{
		Key: RelationAssessmentDefinitionKey, Version: RelationAssessmentDefinitionVersion,
		InputSchemaVersion: RelationAssessmentInputSchemaVersion,
		Graph: workflowdomain.CanonicalGraph{Nodes: []workflowdomain.NodeDefinition{{
			Key: "relation-assessment", Kind: RelationAssessmentNodeKind,
			InputSchemaVersion: RelationAssessmentInputSchemaVersion, OutputSchemaVersion: RelationAssessmentOutputSchemaVersion,
			RetryPolicy: workflowdomain.RetryPolicy{MaxRetries: 2, BaseDelay: time.Second, MaxDelay: 10 * time.Second},
		}}},
	}
}

func workflowError(kind foundation.ErrorKind, code string, retryable bool, cause error) error {
	return foundation.NewError(kind, code, retryable, cause)
}
