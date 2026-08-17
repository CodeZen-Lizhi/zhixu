// Package workflow 定义受限 Workspace Analysis 的持久 Workflow 合同。
package workflow

import (
	"encoding/json"
	"errors"

	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	foundationstrictjson "github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

const (
	// WorkspaceAnalysisDefinitionKey 是工作区分析唯一的 Workflow Definition 键。
	WorkspaceAnalysisDefinitionKey = "workspace-analysis"
	// WorkspaceAnalysisDefinitionVersion 是当前工作区分析 Definition 版本。
	WorkspaceAnalysisDefinitionVersion int64 = 1
	// WorkspaceAnalysisInputSchemaVersion 是工作区分析持久输入版本。
	WorkspaceAnalysisInputSchemaVersion = 1
	// WorkspaceAnalysisOutputSchemaVersion 是工作区分析节点输出版本。
	WorkspaceAnalysisOutputSchemaVersion = 1
	workspaceAnalysisMaxInputBytes       = 1024

	// WorkspaceAnalysisNodeInspectWorkspace 是读取 Git 聚合的节点键。
	WorkspaceAnalysisNodeInspectWorkspace = string(conversationdomain.WorkspaceAnalysisPhaseInspectWorkspace)
	// WorkspaceAnalysisNodeRetrieveEvidence 是受限检索的节点键。
	WorkspaceAnalysisNodeRetrieveEvidence = string(conversationdomain.WorkspaceAnalysisPhaseRetrieveEvidence)
	// WorkspaceAnalysisNodeReadEvidence 是 Source 阅读的节点键。
	WorkspaceAnalysisNodeReadEvidence = string(conversationdomain.WorkspaceAnalysisPhaseReadEvidence)
	// WorkspaceAnalysisNodeSynthesizeAnswer 是候选答案生成的节点键。
	WorkspaceAnalysisNodeSynthesizeAnswer = string(conversationdomain.WorkspaceAnalysisPhaseSynthesizeAnswer)
	// WorkspaceAnalysisNodeValidateCitations 是 Citation 校验的节点键。
	WorkspaceAnalysisNodeValidateCitations = string(conversationdomain.WorkspaceAnalysisPhaseValidateCitations)
	// WorkspaceAnalysisNodeReviewPublish 是审核与发布的节点键。
	WorkspaceAnalysisNodeReviewPublish = string(conversationdomain.WorkspaceAnalysisPhaseReviewPublish)

	workspaceAnalysisGraphHash = "6faa6f0eee72c7e99b3e6118d29322c7d0c377b655403c81fd2337e820a18f73"
)

// WorkspaceAnalysisInput 是工作区分析 Workflow 的最小持久输入。
// Question 正文、Scope 与历史必须由执行端按这些身份重新读取。
type WorkspaceAnalysisInput struct {
	SchemaVersion   int           `json:"schema_version"`
	ConversationID  foundation.ID `json:"conversation_id"`
	QuestionID      foundation.ID `json:"question_id"`
	AnswerID        foundation.ID `json:"answer_id"`
	QuestionOrdinal int64         `json:"question_ordinal"`
	ContextHash     string        `json:"context_hash"`
}

type persistedWorkspaceAnalysisInput struct {
	SchemaVersion   *int           `json:"schema_version"`
	ConversationID  *foundation.ID `json:"conversation_id"`
	QuestionID      *foundation.ID `json:"question_id"`
	AnswerID        *foundation.ID `json:"answer_id"`
	QuestionOrdinal *int64         `json:"question_ordinal"`
	ContextHash     *string        `json:"context_hash"`
}

func workspaceAnalysisNodeKind(nodeKey string) string {
	return "agent.workspace-analysis." + nodeKey
}

func workspaceAnalysisReadPermissions() []workflowdomain.Permission {
	return []workflowdomain.Permission{workflowdomain.PermissionReadLocal}
}

// RegisteredWorkspaceAnalysisDefinition 返回未注册、默认关闭的六节点 Definition。
// 节点按 canonical key 顺序声明；依赖关系保留用户可见的执行链。
func RegisteredWorkspaceAnalysisDefinition() workflowdomain.RegisteredDefinition {
	// 每个 v1 逻辑 Operation/Reservation/Call 是一对一关系，失败后不能由 Workflow
	// 创建新的调用；仅允许由同一节点执行恢复其 canonical 的已完成结果。
	noRetry := workflowdomain.RetryPolicy{MaxRetries: 0}
	return workflowdomain.RegisteredDefinition{
		Key: WorkspaceAnalysisDefinitionKey, Version: WorkspaceAnalysisDefinitionVersion,
		InputSchemaVersion: WorkspaceAnalysisInputSchemaVersion,
		Graph: workflowdomain.CanonicalGraph{Nodes: []workflowdomain.NodeDefinition{
			{
				Key: WorkspaceAnalysisNodeInspectWorkspace, Kind: workspaceAnalysisNodeKind(WorkspaceAnalysisNodeInspectWorkspace),
				InputSchemaVersion: WorkspaceAnalysisInputSchemaVersion, OutputSchemaVersion: WorkspaceAnalysisOutputSchemaVersion,
				RetryPolicy: noRetry, RequiredPermissions: workspaceAnalysisReadPermissions(),
				AllowedTools: []toolsdomain.ToolRef{{Name: "ReadGitStatus", Version: 2}},
			},
			{
				Key: WorkspaceAnalysisNodeReadEvidence, Kind: workspaceAnalysisNodeKind(WorkspaceAnalysisNodeReadEvidence),
				Dependencies:       []string{WorkspaceAnalysisNodeRetrieveEvidence},
				InputSchemaVersion: WorkspaceAnalysisInputSchemaVersion, OutputSchemaVersion: WorkspaceAnalysisOutputSchemaVersion,
				RetryPolicy: noRetry, RequiredPermissions: workspaceAnalysisReadPermissions(),
				AllowedTools: []toolsdomain.ToolRef{{Name: "ReadSource", Version: 3}},
			},
			{
				Key: WorkspaceAnalysisNodeRetrieveEvidence, Kind: workspaceAnalysisNodeKind(WorkspaceAnalysisNodeRetrieveEvidence),
				Dependencies:       []string{WorkspaceAnalysisNodeInspectWorkspace},
				InputSchemaVersion: WorkspaceAnalysisInputSchemaVersion, OutputSchemaVersion: WorkspaceAnalysisOutputSchemaVersion,
				RetryPolicy: noRetry, RequiredPermissions: workspaceAnalysisReadPermissions(),
				AllowedTools: []toolsdomain.ToolRef{{Name: "SearchKnowledge", Version: 2}},
			},
			{
				Key: WorkspaceAnalysisNodeReviewPublish, Kind: workspaceAnalysisNodeKind(WorkspaceAnalysisNodeReviewPublish),
				Dependencies:       []string{WorkspaceAnalysisNodeValidateCitations},
				InputSchemaVersion: WorkspaceAnalysisInputSchemaVersion, OutputSchemaVersion: WorkspaceAnalysisOutputSchemaVersion,
				RetryPolicy: noRetry, RequiredPermissions: workspaceAnalysisReadPermissions(),
			},
			{
				Key: WorkspaceAnalysisNodeSynthesizeAnswer, Kind: workspaceAnalysisNodeKind(WorkspaceAnalysisNodeSynthesizeAnswer),
				Dependencies:       []string{WorkspaceAnalysisNodeReadEvidence},
				InputSchemaVersion: WorkspaceAnalysisInputSchemaVersion, OutputSchemaVersion: WorkspaceAnalysisOutputSchemaVersion,
				RetryPolicy: noRetry, RequiredPermissions: workspaceAnalysisReadPermissions(),
			},
			{
				Key: WorkspaceAnalysisNodeValidateCitations, Kind: workspaceAnalysisNodeKind(WorkspaceAnalysisNodeValidateCitations),
				Dependencies:       []string{WorkspaceAnalysisNodeSynthesizeAnswer},
				InputSchemaVersion: WorkspaceAnalysisInputSchemaVersion, OutputSchemaVersion: WorkspaceAnalysisOutputSchemaVersion,
				RetryPolicy: noRetry, RequiredPermissions: workspaceAnalysisReadPermissions(),
				AllowedTools: []toolsdomain.ToolRef{{Name: "ValidateCitation", Version: 3}},
			},
		}},
		GraphHash: workspaceAnalysisGraphHash,
	}
}

// EncodeWorkspaceAnalysisInput 校验并编码不含正文、Scope 或历史的 canonical 输入。
func EncodeWorkspaceAnalysisInput(input WorkspaceAnalysisInput) (json.RawMessage, error) {
	if err := validateWorkspaceAnalysisInput(input); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, workspaceAnalysisInputError(err)
	}
	return encoded, nil
}

// DecodeWorkspaceAnalysisInput 严格拒绝 unknown、duplicate、trailing 与 null。
// PostgreSQL JSONB 读回可能重排键；调用方应通过 Encode 重建 canonical bytes。
func DecodeWorkspaceAnalysisInput(raw json.RawMessage) (WorkspaceAnalysisInput, error) {
	limits := foundationstrictjson.DefaultLimits()
	limits.MaxDocumentBytes = workspaceAnalysisMaxInputBytes
	limits.MaxStringBytes = 128
	limits.MaxArrayItems = 0
	limits.MaxObjectFields = 6
	persisted, err := foundationstrictjson.DecodeObject[persistedWorkspaceAnalysisInput](raw, limits, nil)
	if err != nil || persisted.SchemaVersion == nil || persisted.ConversationID == nil || persisted.QuestionID == nil ||
		persisted.AnswerID == nil || persisted.QuestionOrdinal == nil || persisted.ContextHash == nil {
		return WorkspaceAnalysisInput{}, workspaceAnalysisInputError(err)
	}
	input := WorkspaceAnalysisInput{
		SchemaVersion: *persisted.SchemaVersion, ConversationID: *persisted.ConversationID,
		QuestionID: *persisted.QuestionID, AnswerID: *persisted.AnswerID,
		QuestionOrdinal: *persisted.QuestionOrdinal, ContextHash: *persisted.ContextHash,
	}
	if err := validateWorkspaceAnalysisInput(input); err != nil {
		return WorkspaceAnalysisInput{}, err
	}
	return input, nil
}

func validateWorkspaceAnalysisInput(input WorkspaceAnalysisInput) error {
	if input.SchemaVersion != WorkspaceAnalysisInputSchemaVersion || input.QuestionOrdinal < 1 || !validHash(input.ContextHash) {
		return workspaceAnalysisInputError(errors.New("workspace analysis input version, ordinal, or context hash is invalid"))
	}
	seen := make(map[foundation.ID]struct{}, 3)
	for _, id := range []foundation.ID{input.ConversationID, input.QuestionID, input.AnswerID} {
		parsed, err := foundation.ParseID(string(id))
		if err != nil || parsed != id {
			return workspaceAnalysisInputError(errors.New("workspace analysis input identity is invalid"))
		}
		if _, duplicate := seen[id]; duplicate {
			return workspaceAnalysisInputError(errors.New("workspace analysis input identity is reused"))
		}
		seen[id] = struct{}{}
	}
	return nil
}

func workspaceAnalysisInputError(cause error) error {
	if cause == nil {
		cause = errors.New("workspace analysis input is incomplete")
	}
	return foundation.NewError(
		foundation.ErrorInvalidInput,
		"CONVERSATION_WORKFLOW_INPUT_INVALID",
		false,
		cause,
	)
}
