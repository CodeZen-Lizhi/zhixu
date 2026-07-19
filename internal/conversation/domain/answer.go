package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// AnswerResultType 区分可发布回答、拒答和澄清结果。
type AnswerResultType string

const (
	// AnswerResultRAGAnswer 表示经过 Citation 与 Faithfulness 门禁的 RAG v2 回答。
	AnswerResultRAGAnswer AnswerResultType = AnswerResultType(agentdomain.ResultTypeRAGAnswer)
	// AnswerResultRefusal 表示稳定结构化拒答。
	AnswerResultRefusal AnswerResultType = AnswerResultType(agentdomain.ResultTypeRefusal)
	// AnswerResultClarification 表示需要用户补充信息的结构化澄清。
	AnswerResultClarification AnswerResultType = AnswerResultType(agentdomain.ResultTypeClarification)
)

// PublishedResult 是服务端重新规范序列化并计算哈希后的不可变发布文档。
type PublishedResult struct {
	Type       AnswerResultType
	ModelRunID foundation.ID
	Document   json.RawMessage
	Hash       string
}

// Answer 是 Question 接受时预分配、并由 Workflow 单次发布的持久结果槽。
type Answer struct {
	ID                foundation.ID
	WorkspaceID       foundation.ID
	ConversationID    foundation.ID
	QuestionID        foundation.ID
	WorkflowRunID     foundation.ID
	ModelRunID        *foundation.ID
	PublicationStatus AnswerPublicationStatus
	ResultType        AnswerResultType
	Result            json.RawMessage
	ResultHash        string
	RetrievalSummary  *RetrievalSummary
	Version           int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
	PublishedAt       *time.Time
}

// ValidateAnswer 校验 Answer 的跨事实源身份、发布束和不可变结果哈希。
func ValidateAnswer(answer Answer) error {
	ids := []foundation.ID{answer.ID, answer.WorkspaceID, answer.ConversationID, answer.QuestionID, answer.WorkflowRunID}
	seen := make(map[foundation.ID]struct{}, len(ids)+1)
	for _, value := range ids {
		parsed, err := foundation.ParseID(string(value))
		if err != nil || parsed != value {
			return invalid(ErrorCodeAnswerInvalid, "answer identity is invalid", err)
		}
		if _, duplicate := seen[value]; duplicate {
			return invalid(ErrorCodeAnswerInvalid, "answer identity is reused", nil)
		}
		seen[value] = struct{}{}
	}
	if answer.Version < 1 || answer.CreatedAt.IsZero() || answer.UpdatedAt.Before(answer.CreatedAt) {
		return invalid(ErrorCodeAnswerInvalid, "answer lifecycle is invalid", nil)
	}
	if answer.PublicationStatus == AnswerPublicationPending {
		if answer.Version != 1 || answer.ModelRunID != nil || answer.ResultType != "" || len(answer.Result) != 0 ||
			answer.ResultHash != "" || answer.RetrievalSummary != nil || answer.PublishedAt != nil {
			return invalid(ErrorCodeAnswerInvalid, "pending answer contains a published result", nil)
		}
		return nil
	}
	if answer.ModelRunID == nil || answer.Version != 2 || len(answer.Result) == 0 || !validLowerHash(answer.ResultHash) ||
		answer.RetrievalSummary == nil || answer.PublishedAt == nil || !answer.PublishedAt.Equal(answer.UpdatedAt) ||
		answer.PublishedAt.Before(answer.CreatedAt) {
		return invalid(ErrorCodeAnswerInvalid, "published answer bundle is incomplete", nil)
	}
	modelRunID, err := foundation.ParseID(string(*answer.ModelRunID))
	if err != nil || modelRunID != *answer.ModelRunID {
		return invalid(ErrorCodeAnswerInvalid, "answer model run identity is invalid", err)
	}
	if _, duplicate := seen[modelRunID]; duplicate {
		return invalid(ErrorCodeAnswerInvalid, "answer model run identity is reused", nil)
	}
	expectedType := answerResultTypeForStatus(answer.PublicationStatus)
	if expectedType == "" || answer.ResultType != expectedType {
		return invalid(ErrorCodeAnswerInvalid, "answer status and result type are inconsistent", nil)
	}
	published, err := CanonicalizePublishedResult(answer.ResultType, answer.Result)
	if err != nil || published.ModelRunID != modelRunID || published.Hash != answer.ResultHash || !bytes.Equal(answer.Result, published.Document) {
		return invalid(ErrorCodeAnswerInvalid, "answer result binding or hash is inconsistent", err)
	}
	if err := ValidateRetrievalSummary(answer.WorkspaceID, answer.PublicationStatus, *answer.RetrievalSummary); err != nil {
		return invalid(ErrorCodeAnswerInvalid, "answer retrieval summary is invalid", err)
	}
	return nil
}

// CanonicalizePublishedResult 严格验证并规范化一个最终 Answer 结果。
func CanonicalizePublishedResult(resultType AnswerResultType, raw json.RawMessage) (PublishedResult, error) {
	limits := agentdomain.DefaultDecodeLimits()
	var (
		modelRunID foundation.ID
		document   json.RawMessage
		err        error
	)
	switch resultType {
	case AnswerResultRAGAnswer:
		var decoded agentdomain.RAGAnswerResultV2
		decoded, err = agentdomain.DecodeRAGAnswerV2(raw, limits)
		if err == nil {
			modelRunID = decoded.ModelRunRef
			document, err = json.Marshal(decoded)
		}
	case AnswerResultRefusal:
		var decoded agentdomain.RefusalResult
		decoded, err = agentdomain.DecodeRefusal(raw, limits)
		if err == nil {
			modelRunID = decoded.ModelRunRef
			document, err = json.Marshal(decoded)
		}
	case AnswerResultClarification:
		var decoded ClarificationResult
		decoded, err = DecodeClarification(raw, limits)
		if err == nil {
			modelRunID = decoded.ModelRunRef
			document, err = json.Marshal(decoded)
		}
	default:
		return PublishedResult{}, invalid(ErrorCodeAnswerInvalid, "answer result type is unsupported", nil)
	}
	if err != nil {
		return PublishedResult{}, invalid(ErrorCodeAnswerInvalid, "answer result document is invalid", err)
	}
	digest := sha256.Sum256(document)
	return PublishedResult{Type: resultType, ModelRunID: modelRunID, Document: document, Hash: hex.EncodeToString(digest[:])}, nil
}

// AnswerPublicationStatus 是 Answer 持久发布槽的单向生命周期。
type AnswerPublicationStatus string

const (
	// AnswerPublicationPending 表示等待 Workflow 发布最终结果。
	AnswerPublicationPending AnswerPublicationStatus = "pending"
	// AnswerPublicationCompleted 表示已发布 RAG v2 回答。
	AnswerPublicationCompleted AnswerPublicationStatus = "completed"
	// AnswerPublicationRefused 表示已发布结构化拒答。
	AnswerPublicationRefused AnswerPublicationStatus = "refused"
	// AnswerPublicationClarificationRequired 表示已发布结构化澄清请求。
	AnswerPublicationClarificationRequired AnswerPublicationStatus = "clarification_required"
)

// ValidateAnswerPublicationTransition 校验 Answer 只能从 pending 进入一个终态。
func ValidateAnswerPublicationTransition(from, to AnswerPublicationStatus) error {
	if from == AnswerPublicationPending &&
		(to == AnswerPublicationCompleted || to == AnswerPublicationRefused || to == AnswerPublicationClarificationRequired) {
		return nil
	}
	return versionConflict(ErrorCodeAnswerTransitionInvalid, "answer publication transition is not allowed")
}

func answerResultTypeForStatus(status AnswerPublicationStatus) AnswerResultType {
	switch status {
	case AnswerPublicationCompleted:
		return AnswerResultRAGAnswer
	case AnswerPublicationRefused:
		return AnswerResultRefusal
	case AnswerPublicationClarificationRequired:
		return AnswerResultClarification
	default:
		return ""
	}
}
