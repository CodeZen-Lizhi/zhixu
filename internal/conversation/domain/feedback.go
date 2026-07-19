package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// MaxFeedbackCommentBytes 是反馈备注允许的最大 UTF-8 字节数。
	MaxFeedbackCommentBytes  = 2 * 1024
	maxFeedbackCitationBytes = 128
)

// FeedbackType 是用户对已发布 Answer 提交的受控评测分类。
type FeedbackType string

const (
	// FeedbackHelpful 表示回答有帮助。
	FeedbackHelpful FeedbackType = "helpful"
	// FeedbackIncorrect 表示回答错误。
	FeedbackIncorrect FeedbackType = "incorrect"
	// FeedbackIrrelevantCitation 表示引用与结论无关。
	FeedbackIrrelevantCitation FeedbackType = "irrelevant_citation"
	// FeedbackBrokenCitation 表示引用无法打开。
	FeedbackBrokenCitation FeedbackType = "broken_citation"
	// FeedbackMissingSource 表示回答遗漏重要资料。
	FeedbackMissingSource FeedbackType = "missing_source"
)

// FeedbackRequest 是进入幂等记录前的 Answer Feedback 领域输入。
type FeedbackRequest struct {
	WorkspaceID foundation.ID
	AnswerID    foundation.ID
	Type        FeedbackType
	CitationID  *string
	Comment     *string
}

// AnswerFeedback 是 append-only 的评测事实，不修改 Answer 或正式知识。
type AnswerFeedback struct {
	ID          foundation.ID
	Request     FeedbackRequest
	RequestHash string
	CreatedAt   time.Time
}

// CanonicalizeFeedbackRequest 校验反馈类型、Answer 终态与 Citation 闭包并返回规范副本。
func CanonicalizeFeedbackRequest(request FeedbackRequest, answer Answer) (FeedbackRequest, error) {
	if err := ValidateAnswer(answer); err != nil || answer.ID != request.AnswerID || answer.WorkspaceID != request.WorkspaceID ||
		(answer.PublicationStatus != AnswerPublicationCompleted && answer.PublicationStatus != AnswerPublicationRefused) {
		return FeedbackRequest{}, invalid(ErrorCodeFeedbackInvalid, "feedback answer binding is invalid", err)
	}
	workspaceID, workspaceErr := foundation.ParseID(string(request.WorkspaceID))
	answerID, answerErr := foundation.ParseID(string(request.AnswerID))
	if workspaceErr != nil || answerErr != nil || workspaceID != request.WorkspaceID || answerID != request.AnswerID || workspaceID == answerID ||
		!validFeedbackType(request.Type) {
		return FeedbackRequest{}, invalid(ErrorCodeFeedbackInvalid, "feedback identity or type is invalid", firstError(workspaceErr, answerErr))
	}
	comment, err := canonicalOptionalFeedbackText(request.Comment, MaxFeedbackCommentBytes)
	if err != nil {
		return FeedbackRequest{}, err
	}
	citationID, err := canonicalOptionalFeedbackText(request.CitationID, maxFeedbackCitationBytes)
	if err != nil {
		return FeedbackRequest{}, err
	}
	if request.Type == FeedbackIrrelevantCitation || request.Type == FeedbackBrokenCitation {
		if citationID == nil || answer.PublicationStatus != AnswerPublicationCompleted || answer.ResultType != AnswerResultRAGAnswer {
			return FeedbackRequest{}, invalid(ErrorCodeFeedbackInvalid, "citation feedback requires a published rag answer citation", nil)
		}
		decoded, decodeErr := agentdomain.DecodeRAGAnswerV2(answer.Result, agentdomain.DefaultDecodeLimits())
		if decodeErr != nil || !containsCitation(decoded.Payload.Citations, *citationID) {
			return FeedbackRequest{}, invalid(ErrorCodeFeedbackInvalid, "feedback citation is not present in the answer", decodeErr)
		}
	} else if citationID != nil {
		return FeedbackRequest{}, invalid(ErrorCodeFeedbackInvalid, "non-citation feedback cannot bind a citation", nil)
	}
	return FeedbackRequest{
		WorkspaceID: workspaceID, AnswerID: answerID, Type: request.Type, CitationID: citationID, Comment: comment,
	}, nil
}

// ComputeFeedbackRequestHash 计算绑定 Answer、分类、Citation 和备注的稳定幂等哈希。
func ComputeFeedbackRequestHash(request FeedbackRequest, answer Answer) (string, error) {
	canonical, err := CanonicalizeFeedbackRequest(request, answer)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(struct {
		SchemaVersion int           `json:"schema_version"`
		WorkspaceID   foundation.ID `json:"workspace_id"`
		AnswerID      foundation.ID `json:"answer_id"`
		FeedbackType  FeedbackType  `json:"feedback_type"`
		CitationID    *string       `json:"citation_id"`
		Comment       *string       `json:"comment"`
	}{
		SchemaVersion: 1, WorkspaceID: canonical.WorkspaceID, AnswerID: canonical.AnswerID,
		FeedbackType: canonical.Type, CitationID: canonical.CitationID, Comment: canonical.Comment,
	})
	if err != nil {
		return "", inconsistent(ErrorCodeFeedbackInvalid, "feedback hash payload is invalid")
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// ValidateAnswerFeedback 校验持久反馈请求已经规范且请求哈希精确匹配。
func ValidateAnswerFeedback(feedback AnswerFeedback, answer Answer) error {
	id, err := foundation.ParseID(string(feedback.ID))
	if err != nil || id != feedback.ID || id == feedback.Request.WorkspaceID || id == feedback.Request.AnswerID || feedback.CreatedAt.IsZero() ||
		!validLowerHash(feedback.RequestHash) {
		return invalid(ErrorCodeFeedbackInvalid, "feedback identity or lifecycle is invalid", err)
	}
	canonical, err := CanonicalizeFeedbackRequest(feedback.Request, answer)
	if err != nil || !reflect.DeepEqual(canonical, feedback.Request) {
		return invalid(ErrorCodeFeedbackInvalid, "feedback request is not canonical", err)
	}
	hash, err := ComputeFeedbackRequestHash(canonical, answer)
	if err != nil || hash != feedback.RequestHash {
		return inconsistent(ErrorCodeFeedbackInvalid, "feedback request hash is inconsistent")
	}
	return nil
}

func validFeedbackType(value FeedbackType) bool {
	return value == FeedbackHelpful || value == FeedbackIncorrect || value == FeedbackIrrelevantCitation ||
		value == FeedbackBrokenCitation || value == FeedbackMissingSource
}

func canonicalOptionalFeedbackText(value *string, maximum int) (*string, error) {
	if value == nil {
		return nil, nil
	}
	if !utf8.ValidString(*value) || strings.ContainsRune(*value, '\x00') {
		return nil, invalid(ErrorCodeFeedbackInvalid, "feedback text is invalid", nil)
	}
	canonical := strings.TrimSpace(*value)
	if canonical == "" || len(canonical) > maximum {
		return nil, invalid(ErrorCodeFeedbackInvalid, "feedback text is empty or oversized", nil)
	}
	return &canonical, nil
}

func containsCitation(citations []agentdomain.Citation, citationID string) bool {
	for _, citation := range citations {
		if citation.ID == citationID {
			return true
		}
	}
	return false
}

func firstError(values ...error) error {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}
