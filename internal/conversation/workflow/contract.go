// Package workflow 定义 RAG Conversation 持久 Workflow 的稳定输入与 Definition 契约。
package workflow

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	foundationstrictjson "github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

const (
	// DefinitionKey 是 RAG Question 唯一允许启动的内置 Definition。
	DefinitionKey = "agent-rag-answer"
	// DefinitionVersion 是当前 RAG Question Definition 版本。
	DefinitionVersion int64 = 1
	// NodeKey 是单节点 RAG Definition 的稳定逻辑键。
	NodeKey = "rag-answer"
	// NodeKind 是 Worker Executor Registry 使用的稳定节点类型。
	NodeKind = "agent.rag-answer"
	// InputSchemaVersion 是只含身份与上下文哈希的持久输入版本。
	InputSchemaVersion = 1
	// OutputSchemaVersion 是最终发布回执的持久输出版本。
	OutputSchemaVersion = 1

	maxInputBytes = 1024
	graphHash     = "f57decff412db222c531384812cb42fce7353b3d41940fe69c53a8734a5528a5"
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

// RegisteredDefinition 返回带稳定 Graph Hash 的单节点 RAG Definition。
func RegisteredDefinition() workflowdomain.RegisteredDefinition {
	return workflowdomain.RegisteredDefinition{
		Key: DefinitionKey, Version: DefinitionVersion, InputSchemaVersion: InputSchemaVersion,
		Graph: workflowdomain.CanonicalGraph{Nodes: []workflowdomain.NodeDefinition{{
			Key: NodeKey, Kind: NodeKind, InputSchemaVersion: InputSchemaVersion, OutputSchemaVersion: OutputSchemaVersion,
			RetryPolicy:         workflowdomain.RetryPolicy{MaxRetries: 2, BaseDelay: time.Second, MaxDelay: 10 * time.Second},
			RequiredPermissions: []workflowdomain.Permission{workflowdomain.PermissionReadLocal},
		}}},
		GraphHash: graphHash,
	}
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
