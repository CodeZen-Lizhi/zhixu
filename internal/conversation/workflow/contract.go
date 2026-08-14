// Package workflow 定义 RAG Conversation 持久 Workflow 的稳定输入与 Definition 契约。
package workflow

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	foundationstrictjson "github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

const (
	// DefinitionKey 是 RAG Question 唯一允许启动的内置 Definition。
	DefinitionKey = "agent-rag-answer"
	// DefinitionVersionV1 是不含 Tool 的已持久化 RAG Question Definition 版本。
	DefinitionVersionV1 int64 = 1
	// DefinitionVersionV2 是允许受控只读 Tool 的当前 RAG Question Definition 版本。
	DefinitionVersionV2 int64 = 2
	// DefinitionVersion 是当前 RAG Question Definition 版本。
	DefinitionVersion = DefinitionVersionV2
	// NodeKey 是单节点 RAG Definition 的稳定逻辑键。
	NodeKey = "rag-answer"
	// NodeKind 是 Worker Executor Registry 使用的稳定节点类型。
	NodeKind = "agent.rag-answer"
	// InputSchemaVersion 是只含身份与上下文哈希的持久输入版本。
	InputSchemaVersion = 1
	// OutputSchemaVersion 是最终发布回执的持久输出版本。
	OutputSchemaVersion = 1

	maxInputBytes = 1024
	graphHashV1   = "f57decff412db222c531384812cb42fce7353b3d41940fe69c53a8734a5528a5"
	graphHashV2   = "359a4f9851f07268f50b2d597c697d74ed0f67b7439c2747efbf2ed85f98b490"
)

// PublicationStatus 表示 Workflow receipt 中允许持久化的 Answer 终态。
type PublicationStatus = conversationdomain.AnswerPublicationStatus

const (
	// PublicationStatusCompleted 表示 Answer 已发布为正式 RAG 回答。
	PublicationStatusCompleted = conversationdomain.AnswerPublicationCompleted
	// PublicationStatusRefused 表示 Answer 已发布为结构化拒答。
	PublicationStatusRefused = conversationdomain.AnswerPublicationRefused
	// PublicationStatusClarificationRequired 表示 Answer 已发布为结构化澄清请求。
	PublicationStatusClarificationRequired = conversationdomain.AnswerPublicationClarificationRequired
)

// ResultType 表示 Workflow receipt 中允许持久化的最终结果类型。
type ResultType = conversationdomain.AnswerResultType

const (
	// ResultTypeRAGAnswer 表示完成态正式回答结果。
	ResultTypeRAGAnswer = conversationdomain.AnswerResultRAGAnswer
	// ResultTypeRefusal 表示拒答结果。
	ResultTypeRefusal = conversationdomain.AnswerResultRefusal
	// ResultTypeClarification 表示澄清结果。
	ResultTypeClarification = conversationdomain.AnswerResultClarification
)

// Input 是 Question Workflow 的最小持久输入；正文和历史必须由执行端重新加载。
type Input struct {
	SchemaVersion   int           `json:"schema_version"`
	ConversationID  foundation.ID `json:"conversation_id"`
	QuestionID      foundation.ID `json:"question_id"`
	AnswerID        foundation.ID `json:"answer_id"`
	QuestionOrdinal int64         `json:"question_ordinal"`
	ContextHash     string        `json:"context_hash"`
}

type persistedInput struct {
	SchemaVersion   *int           `json:"schema_version"`
	ConversationID  *foundation.ID `json:"conversation_id"`
	QuestionID      *foundation.ID `json:"question_id"`
	AnswerID        *foundation.ID `json:"answer_id"`
	QuestionOrdinal *int64         `json:"question_ordinal"`
	ContextHash     *string        `json:"context_hash"`
}

// OutputReceipt 是 Workflow 节点输出的稳定发布回执；正文和检索细节必须由执行端重新加载。
type OutputReceipt struct {
	SchemaVersion     int               `json:"schema_version"`
	AnswerID          foundation.ID     `json:"answer_id"`
	PublicationStatus PublicationStatus `json:"publication_status"`
	ResultType        ResultType        `json:"result_type"`
	ModelRunID        foundation.ID     `json:"model_run_id"`
	ResultHash        string            `json:"result_hash"`
}

type persistedOutputReceipt struct {
	SchemaVersion     *int               `json:"schema_version"`
	AnswerID          *foundation.ID     `json:"answer_id"`
	PublicationStatus *PublicationStatus `json:"publication_status"`
	ResultType        *ResultType        `json:"result_type"`
	ModelRunID        *foundation.ID     `json:"model_run_id"`
	ResultHash        *string            `json:"result_hash"`
}

// RegisteredDefinitionV1 返回不含 Tool 的已持久化单节点 RAG Definition。
func RegisteredDefinitionV1() workflowdomain.RegisteredDefinition {
	return workflowdomain.RegisteredDefinition{
		Key: DefinitionKey, Version: DefinitionVersionV1, InputSchemaVersion: InputSchemaVersion,
		Graph: workflowdomain.CanonicalGraph{Nodes: []workflowdomain.NodeDefinition{{
			Key: NodeKey, Kind: NodeKind, InputSchemaVersion: InputSchemaVersion, OutputSchemaVersion: OutputSchemaVersion,
			RetryPolicy:         workflowdomain.RetryPolicy{MaxRetries: 2, BaseDelay: time.Second, MaxDelay: 10 * time.Second},
			RequiredPermissions: []workflowdomain.Permission{workflowdomain.PermissionReadLocal},
		}}},
		GraphHash: graphHashV1,
	}
}

// RegisteredDefinitionV2 返回允许受控只读 Tool 的当前单节点 RAG Definition。
func RegisteredDefinitionV2() workflowdomain.RegisteredDefinition {
	return workflowdomain.RegisteredDefinition{
		Key: DefinitionKey, Version: DefinitionVersionV2, InputSchemaVersion: InputSchemaVersion,
		Graph: workflowdomain.CanonicalGraph{Nodes: []workflowdomain.NodeDefinition{{
			Key: NodeKey, Kind: NodeKind, InputSchemaVersion: InputSchemaVersion, OutputSchemaVersion: OutputSchemaVersion,
			RetryPolicy:         workflowdomain.RetryPolicy{MaxRetries: 2, BaseDelay: time.Second, MaxDelay: 10 * time.Second},
			RequiredPermissions: []workflowdomain.Permission{workflowdomain.PermissionReadLocal},
			AllowedTools: []toolsdomain.ToolRef{
				{Name: "ReadSource", Version: 2},
				{Name: "ValidateCitation", Version: 2},
			},
		}}},
		GraphHash: graphHashV2,
	}
}

// RegisteredDefinition 返回当前可启动的单节点 RAG Definition。
func RegisteredDefinition() workflowdomain.RegisteredDefinition {
	return RegisteredDefinitionV2()
}

// EncodeInput 校验并编码不含 Question 正文或历史的 canonical Workflow Input。
func EncodeInput(input Input) (json.RawMessage, error) {
	if err := validateInput(input); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, contractError(err)
	}
	return encoded, nil
}

// DecodeInput 严格拒绝 unknown、duplicate、trailing、null 与非 canonical 输入。
func DecodeInput(raw json.RawMessage) (Input, error) {
	limits := foundationstrictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxInputBytes
	limits.MaxStringBytes = 128
	limits.MaxArrayItems = 0
	limits.MaxObjectFields = 6
	persisted, err := foundationstrictjson.DecodeObject[persistedInput](raw, limits, nil)
	if err != nil || persisted.SchemaVersion == nil || persisted.ConversationID == nil || persisted.QuestionID == nil ||
		persisted.AnswerID == nil || persisted.QuestionOrdinal == nil || persisted.ContextHash == nil {
		return Input{}, contractError(err)
	}
	input := Input{
		SchemaVersion: *persisted.SchemaVersion, ConversationID: *persisted.ConversationID,
		QuestionID: *persisted.QuestionID, AnswerID: *persisted.AnswerID,
		QuestionOrdinal: *persisted.QuestionOrdinal, ContextHash: *persisted.ContextHash,
	}
	if err := validateInput(input); err != nil {
		return Input{}, err
	}
	return input, nil
}

// EncodeOutputReceipt 校验并编码不含正文、检索摘要或 Provider 数据的 canonical 发布回执。
func EncodeOutputReceipt(output OutputReceipt) (json.RawMessage, error) {
	if err := validateOutputReceipt(output); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return nil, outputContractError(err)
	}
	return encoded, nil
}

// DecodeOutputReceipt 严格拒绝 unknown、duplicate、trailing、null 与不一致的发布回执。
func DecodeOutputReceipt(raw json.RawMessage) (OutputReceipt, error) {
	limits := foundationstrictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxInputBytes
	limits.MaxStringBytes = 128
	limits.MaxArrayItems = 0
	limits.MaxObjectFields = 6
	persisted, err := foundationstrictjson.DecodeObject[persistedOutputReceipt](raw, limits, nil)
	if err != nil || persisted.SchemaVersion == nil || persisted.AnswerID == nil || persisted.PublicationStatus == nil ||
		persisted.ResultType == nil || persisted.ModelRunID == nil || persisted.ResultHash == nil {
		return OutputReceipt{}, outputContractError(err)
	}
	output := OutputReceipt{
		SchemaVersion: *persisted.SchemaVersion, AnswerID: *persisted.AnswerID,
		PublicationStatus: *persisted.PublicationStatus, ResultType: *persisted.ResultType,
		ModelRunID: *persisted.ModelRunID, ResultHash: *persisted.ResultHash,
	}
	if err := validateOutputReceipt(output); err != nil {
		return OutputReceipt{}, err
	}
	return output, nil
}

func validateInput(input Input) error {
	if input.SchemaVersion != InputSchemaVersion || input.QuestionOrdinal < 1 || !validHash(input.ContextHash) {
		return contractError(errors.New("RAG workflow input version, ordinal, or context hash is invalid"))
	}
	seen := make(map[foundation.ID]struct{}, 3)
	for _, id := range []foundation.ID{input.ConversationID, input.QuestionID, input.AnswerID} {
		parsed, err := foundation.ParseID(string(id))
		if err != nil || parsed != id {
			return contractError(errors.New("RAG workflow input identity is invalid"))
		}
		if _, duplicate := seen[id]; duplicate {
			return contractError(errors.New("RAG workflow input identity is reused"))
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validateOutputReceipt(output OutputReceipt) error {
	if output.SchemaVersion != OutputSchemaVersion || !validHash(output.ResultHash) {
		return outputContractError(errors.New("RAG workflow output version or result hash is invalid"))
	}
	expectedType := conversationdomain.ResultTypeForPublicationStatus(output.PublicationStatus)
	if expectedType == "" || output.ResultType != expectedType {
		return outputContractError(errors.New("RAG workflow output publication status and result type are inconsistent"))
	}
	seen := make(map[foundation.ID]struct{}, 2)
	for _, id := range []foundation.ID{output.AnswerID, output.ModelRunID} {
		parsed, err := foundation.ParseID(string(id))
		if err != nil || parsed != id {
			return outputContractError(errors.New("RAG workflow output identity is invalid"))
		}
		if _, duplicate := seen[id]; duplicate {
			return outputContractError(errors.New("RAG workflow output identity is reused"))
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func contractError(cause error) error {
	if cause == nil {
		cause = errors.New("RAG workflow input is invalid")
	}
	return foundation.NewError(foundation.ErrorInvalidInput, "CONVERSATION_WORKFLOW_INPUT_INVALID", false, cause)
}

func outputContractError(cause error) error {
	if cause == nil {
		cause = errors.New("RAG workflow output is invalid")
	}
	return foundation.NewError(foundation.ErrorInvalidInput, "CONVERSATION_WORKFLOW_OUTPUT_INVALID", false, cause)
}
