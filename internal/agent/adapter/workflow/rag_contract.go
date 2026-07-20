package workflow

import (
	"encoding/json"

	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

const (
	// RAGDefinitionKey 是 RAG Answer Workflow Definition 的稳定键。
	RAGDefinitionKey = conversationworkflow.DefinitionKey
	// RAGDefinitionVersion 是 RAG Answer Workflow Definition 的稳定版本。
	RAGDefinitionVersion = conversationworkflow.DefinitionVersion
	// RAGNodeKey 是 RAG Answer 单节点的稳定逻辑键。
	RAGNodeKey = conversationworkflow.NodeKey
	// RAGNodeKind 是 RAG Answer Executor Registry 使用的稳定节点类型。
	RAGNodeKind = conversationworkflow.NodeKind
	// RAGInputSchemaVersion 是 RAG Answer 持久输入契约版本。
	RAGInputSchemaVersion = conversationworkflow.InputSchemaVersion
	// RAGOutputSchemaVersion 是 RAG Answer 发布回执契约版本。
	RAGOutputSchemaVersion = conversationworkflow.OutputSchemaVersion

	// RAGWorkflowDefinitionKey 是 RAG Answer Workflow Definition 的稳定键。
	RAGWorkflowDefinitionKey = RAGDefinitionKey
	// RAGWorkflowDefinitionVersion 是 RAG Answer Workflow Definition 的稳定版本。
	RAGWorkflowDefinitionVersion = RAGDefinitionVersion
	// RAGWorkflowNodeKey 是 RAG Answer 单节点的稳定逻辑键。
	RAGWorkflowNodeKey = RAGNodeKey
	// RAGWorkflowNodeKind 是 RAG Answer Executor Registry 使用的稳定节点类型。
	RAGWorkflowNodeKind = RAGNodeKind
	// RAGWorkflowInputSchemaVersion 是 RAG Answer 持久输入契约版本。
	RAGWorkflowInputSchemaVersion = RAGInputSchemaVersion
	// RAGWorkflowOutputSchemaVersion 是 RAG Answer 发布回执契约版本。
	RAGWorkflowOutputSchemaVersion = RAGOutputSchemaVersion
)

// RAGWorkflowInput 是只含 Question 身份和冻结上下文哈希的持久输入。
type RAGWorkflowInput = conversationworkflow.Input

// RAGWorkflowOutput 是不含正文、检索详情或 Provider 数据的稳定发布回执。
type RAGWorkflowOutput = conversationworkflow.OutputReceipt

// RAGWorkflowOutputReceipt 是稳定发布回执的显式别名。
type RAGWorkflowOutputReceipt = conversationworkflow.OutputReceipt

// EncodeRAGWorkflowInput 校验并编码 RAG Answer Workflow 的六字段输入。
func EncodeRAGWorkflowInput(input RAGWorkflowInput) (json.RawMessage, error) {
	return conversationworkflow.EncodeInput(input)
}

// DecodeRAGWorkflowInput 严格拒绝 unknown、duplicate、trailing、null 与非法身份绑定。
func DecodeRAGWorkflowInput(raw json.RawMessage) (RAGWorkflowInput, error) {
	return conversationworkflow.DecodeInput(raw)
}

// EncodeRAGWorkflowOutput 校验并编码 RAG Answer Workflow 的稳定发布回执。
func EncodeRAGWorkflowOutput(output RAGWorkflowOutput) (json.RawMessage, error) {
	return conversationworkflow.EncodeOutputReceipt(output)
}

// DecodeRAGWorkflowOutput 严格拒绝 unknown、duplicate、trailing、null 与非法发布绑定。
func DecodeRAGWorkflowOutput(raw json.RawMessage) (RAGWorkflowOutput, error) {
	return conversationworkflow.DecodeOutputReceipt(raw)
}

// EncodeRAGWorkflowOutputReceipt 校验并编码稳定发布回执。
func EncodeRAGWorkflowOutputReceipt(output RAGWorkflowOutputReceipt) (json.RawMessage, error) {
	return conversationworkflow.EncodeOutputReceipt(output)
}

// DecodeRAGWorkflowOutputReceipt 严格解码稳定发布回执。
func DecodeRAGWorkflowOutputReceipt(raw json.RawMessage) (RAGWorkflowOutputReceipt, error) {
	return conversationworkflow.DecodeOutputReceipt(raw)
}

// RegisteredRAGDefinition 返回无 Tool、仅需 READ_LOCAL 的单节点 RAG Definition。
func RegisteredRAGDefinition() workflowdomain.RegisteredDefinition {
	return conversationworkflow.RegisteredDefinition()
}
