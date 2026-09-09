package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	foundationstrictjson "github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
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

// PublishedAnswerProjection 是 Repository、HTTP 与有界会话上下文共享的最终结果投影。
type PublishedAnswerProjection struct {
	PublishedResult
	AssistantText string
	Citations     []agentdomain.Citation
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
	if answer.Version != 2 || len(answer.Result) == 0 || !validLowerHash(answer.ResultHash) ||
		answer.PublishedAt == nil || !answer.PublishedAt.Equal(answer.UpdatedAt) ||
		answer.PublishedAt.Before(answer.CreatedAt) {
		return invalid(ErrorCodeAnswerInvalid, "published answer bundle is incomplete", nil)
	}
	if workspaceAnalysisAnswerBundle(answer) {
		return validateWorkspaceAnalysisAnswerBundle(answer, seen)
	}
	if answer.ModelRunID == nil || answer.RetrievalSummary == nil {
		return invalid(ErrorCodeAnswerInvalid, "published answer bundle is incomplete", nil)
	}
	modelRunID, err := foundation.ParseID(string(*answer.ModelRunID))
	if err != nil || modelRunID != *answer.ModelRunID {
		return invalid(ErrorCodeAnswerInvalid, "answer model run identity is invalid", err)
	}
	if _, duplicate := seen[modelRunID]; duplicate {
		return invalid(ErrorCodeAnswerInvalid, "answer model run identity is reused", nil)
	}
	expectedType := ResultTypeForPublicationStatus(answer.PublicationStatus)
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
	projection, err := projectPublishedAnswer(resultType, raw)
	if err != nil {
		return PublishedResult{}, err
	}
	return projection.PublishedResult, nil
}

// ProjectPublishedAnswer 严格规范化最终结果，并投影可进入上下文的主文本与稳定引用。
func ProjectPublishedAnswer(resultType AnswerResultType, raw json.RawMessage) (PublishedAnswerProjection, error) {
	return projectPublishedAnswer(resultType, raw)
}

func projectPublishedAnswer(resultType AnswerResultType, raw json.RawMessage) (PublishedAnswerProjection, error) {
	limits := agentdomain.DefaultDecodeLimits()
	var (
		modelRunID    foundation.ID
		document      json.RawMessage
		assistantText string
		citations     = make([]agentdomain.Citation, 0)
		err           error
	)
	switch resultType {
	case AnswerResultRAGAnswer:
		var decoded agentdomain.RAGAnswerResultV2
		decoded, err = agentdomain.DecodeRAGAnswerV2(raw, limits)
		if err == nil {
			modelRunID = decoded.ModelRunRef
			assistantText = decoded.Payload.Conclusion
			citations = append(citations, decoded.Payload.Citations...)
			document, err = json.Marshal(decoded)
		}
	case AnswerResultRefusal:
		var decoded agentdomain.RefusalResult
		decoded, err = agentdomain.DecodeRefusal(raw, limits)
		if err == nil {
			modelRunID = decoded.ModelRunRef
			assistantText = decoded.Payload.Summary
			document, err = json.Marshal(decoded)
		}
	case AnswerResultClarification:
		var decoded ClarificationResult
		decoded, err = DecodeClarification(raw, limits)
		if err == nil {
			modelRunID = decoded.ModelRunRef
			assistantText = decoded.Payload.Question
			document, err = json.Marshal(decoded)
		}
	case AnswerResultWorkspaceAnalysis:
		var decoded workspaceAnalysisDecodedAnswer
		decoded, err = decodeWorkspaceAnalysisAnswerDocument(raw)
		if err == nil {
			modelRunID = decoded.ModelRunRef
			assistantText = decoded.AnswerMarkdown
			citations = append(citations, decoded.Citations...)
			document = decoded.Document
		}
	case AnswerResultWorkspaceAnalysisRefusal:
		var decoded WorkspaceAnalysisRefusalResult
		decoded, err = decodeWorkspaceAnalysisRefusal(raw)
		if err == nil {
			if decoded.ModelRunRef != nil {
				modelRunID = *decoded.ModelRunRef
			}
			assistantText = decoded.Payload.Summary
			document, err = json.Marshal(decoded)
		}
	case AnswerResultWorkspaceAnalysisTermination:
		status := WorkspaceAnalysisPublicationFailed
		var persisted workspaceAnalysisNullableEnvelope[WorkspaceAnalysisTerminationPayload]
		persisted, err = foundationstrictjson.DecodeObject[workspaceAnalysisNullableEnvelope[WorkspaceAnalysisTerminationPayload]](
			raw, workspaceAnalysisDecodeLimits(), nil,
		)
		if err == nil && persisted.Payload != nil && persisted.Payload.TerminationReason == WorkspaceAnalysisCancelled {
			status = WorkspaceAnalysisPublicationCancelled
		}
		var decoded WorkspaceAnalysisTerminationResult
		if err == nil {
			decoded, err = decodeWorkspaceAnalysisTermination(raw, status)
		}
		if err == nil {
			if decoded.ModelRunRef != nil {
				modelRunID = *decoded.ModelRunRef
			}
			assistantText = decoded.Payload.Summary
			document, err = json.Marshal(decoded)
		}
	default:
		return PublishedAnswerProjection{}, invalid(ErrorCodeAnswerInvalid, "answer result type is unsupported", nil)
	}
	if err != nil {
		return PublishedAnswerProjection{}, invalid(ErrorCodeAnswerInvalid, "answer result document is invalid", err)
	}
	digest := sha256.Sum256(document)
	return PublishedAnswerProjection{
		PublishedResult: PublishedResult{
			Type: resultType, ModelRunID: modelRunID, Document: document, Hash: hex.EncodeToString(digest[:]),
		},
		AssistantText: assistantText,
		Citations:     citations,
	}, nil
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
		(to == AnswerPublicationCompleted || to == AnswerPublicationRefused || to == AnswerPublicationClarificationRequired ||
			to == WorkspaceAnalysisPublicationFailed || to == WorkspaceAnalysisPublicationCancelled) {
		return nil
	}
	return versionConflict(ErrorCodeAnswerTransitionInvalid, "answer publication transition is not allowed")
}

func workspaceAnalysisAnswerBundle(answer Answer) bool {
	return answer.ResultType == AnswerResultWorkspaceAnalysis ||
		answer.ResultType == AnswerResultWorkspaceAnalysisRefusal ||
		answer.ResultType == AnswerResultWorkspaceAnalysisTermination ||
		(answer.ResultType == AnswerResultClarification && answer.RetrievalSummary == nil)
}

func validateWorkspaceAnalysisAnswerBundle(answer Answer, seen map[foundation.ID]struct{}) error {
	if answer.RetrievalSummary != nil {
		return invalid(ErrorCodeAnswerInvalid, "workspace analysis answer cannot contain a rag retrieval summary", nil)
	}
	published, err := CanonicalizeWorkspaceAnalysisPublishedResult(answer.PublicationStatus, answer.ResultType, answer.Result)
	if err != nil || published.Hash != answer.ResultHash || !bytes.Equal(answer.Result, published.Document) ||
		(answer.ModelRunID == nil) != (published.ModelRunID == nil) {
		return invalid(ErrorCodeAnswerInvalid, "workspace analysis answer result binding or hash is inconsistent", err)
	}
	if answer.ResultType == AnswerResultWorkspaceAnalysis {
		decoded, decodeErr := decodeWorkspaceAnalysisAnswerDocument(answer.Result)
		if decodeErr != nil {
			return invalid(ErrorCodeAnswerInvalid, "workspace analysis answer result is inconsistent", decodeErr)
		}
		if decoded.SchemaVersion == WorkspaceAnalysisResultSchemaVersionV2 {
			for _, citation := range decoded.Citations {
				if citation.WorkspaceID != answer.WorkspaceID {
					return invalid(ErrorCodeAnswerInvalid, "workspace analysis v2 citation scope is inconsistent", nil)
				}
			}
		}
	}
	if published.ModelRunID == nil {
		return nil
	}
	modelRunID, parseErr := foundation.ParseID(string(*published.ModelRunID))
	if parseErr != nil || modelRunID != *published.ModelRunID || *answer.ModelRunID != modelRunID {
		return invalid(ErrorCodeAnswerInvalid, "workspace analysis answer model run identity is invalid", parseErr)
	}
	if _, duplicate := seen[modelRunID]; duplicate {
		return invalid(ErrorCodeAnswerInvalid, "workspace analysis answer model run identity is reused", nil)
	}
	return nil
}

// ResultTypeForPublicationStatus 返回一个 Answer 发布终态唯一允许的结果类型；非终态或未知状态返回空值。
func ResultTypeForPublicationStatus(status AnswerPublicationStatus) AnswerResultType {
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
