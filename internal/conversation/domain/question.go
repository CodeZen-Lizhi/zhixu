package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const (
	// MaxQuestionBytes 是 Question 正文允许的最大 UTF-8 字节数。
	MaxQuestionBytes = retrievaldomain.MaxSearchQueryBytes
	// MaxQuestionScopeBytes 是持久化 canonical Scope JSON 的最大字节数。
	MaxQuestionScopeBytes = 16 * 1024
)

type questionScopeDocument struct {
	RetrievalMode        retrievaldomain.SearchMode `json:"retrieval_mode"`
	SourceIDs            []foundation.ID            `json:"source_ids"`
	SourceVersionIDs     []foundation.ID            `json:"source_version_ids"`
	PathPrefixes         []string                   `json:"path_prefixes"`
	CapturedAtFrom       *string                    `json:"captured_at_from"`
	CapturedAtBefore     *string                    `json:"captured_at_before"`
	AllowOriginalSources bool                       `json:"allow_original_sources"`
	AllowWeb             bool                       `json:"allow_web"`
}

// AnswerDepth 是用户选择的回答详细程度。
type AnswerDepth string

const (
	// AnswerDepthConcise 表示简洁回答。
	AnswerDepthConcise AnswerDepth = "concise"
	// AnswerDepthStandard 表示默认详细程度。
	AnswerDepthStandard AnswerDepth = "standard"
	// AnswerDepthDetailed 表示详细回答。
	AnswerDepthDetailed AnswerDepth = "detailed"
)

// OutputFormat 是用户选择的回答组织格式。
type OutputFormat string

const (
	// OutputFormatMarkdown 表示 Markdown 正文。
	OutputFormatMarkdown OutputFormat = "markdown"
	// OutputFormatOutline 表示层级大纲。
	OutputFormatOutline OutputFormat = "outline"
)

// QuestionScope 保存与 Retrieval 共享的规范过滤条件和显式来源权限。
type QuestionScope struct {
	RetrievalMode        retrievaldomain.SearchMode
	Filter               retrievaldomain.SearchFilter
	AllowOriginalSources bool
	AllowWeb             bool
}

// QuestionRequest 是提交 Question 前的领域输入。
type QuestionRequest struct {
	WorkspaceID    foundation.ID
	ConversationID foundation.ID
	QuestionText   string
	Scope          QuestionScope
	AnswerDepth    AnswerDepth
	OutputFormat   OutputFormat
}

// Question 是 Conversation 中绑定一次 Answer Workflow 的不可变问题事实。
type Question struct {
	ID                    foundation.ID
	Request               QuestionRequest
	Ordinal               int64
	ContextThroughOrdinal int64
	ContextHash           string
	RequestHash           string
	CreatedAt             time.Time
}

// ValidateQuestion 校验 Question 的规范请求、上下文边界和幂等哈希。
func ValidateQuestion(question Question) error {
	id, err := foundation.ParseID(string(question.ID))
	if err != nil || id != question.ID || id == question.Request.WorkspaceID || id == question.Request.ConversationID ||
		question.Ordinal < 1 || question.ContextThroughOrdinal < 0 || question.ContextThroughOrdinal >= question.Ordinal ||
		!validLowerHash(question.ContextHash) || !validLowerHash(question.RequestHash) || question.CreatedAt.IsZero() {
		return invalid(ErrorCodeQuestionInvalid, "question identity, context, or lifecycle is invalid", err)
	}
	canonical, err := CanonicalizeQuestionRequest(question.Request)
	if err != nil || !reflect.DeepEqual(canonical, question.Request) {
		return invalid(ErrorCodeQuestionInvalid, "question request is not canonical", err)
	}
	requestHash, err := ComputeQuestionRequestHash(canonical)
	if err != nil || requestHash != question.RequestHash {
		return inconsistent(ErrorCodeQuestionInvalid, "question request hash is inconsistent")
	}
	if question.ContextThroughOrdinal == 0 {
		emptyContextHash, _, _, hashErr := ComputeContextHash(nil)
		if hashErr != nil || question.ContextHash != emptyContextHash {
			return inconsistent(ErrorCodeQuestionInvalid, "empty question context hash is inconsistent")
		}
	}
	return nil
}

// CanonicalizeQuestionRequest 校验并返回不修改调用方数据的规范请求。
func CanonicalizeQuestionRequest(request QuestionRequest) (QuestionRequest, error) {
	workspaceID, err := foundation.ParseID(string(request.WorkspaceID))
	if err != nil {
		return QuestionRequest{}, invalid(ErrorCodeQuestionInvalid, "question workspace is invalid", err)
	}
	conversationID, err := foundation.ParseID(string(request.ConversationID))
	if err != nil || workspaceID == conversationID {
		return QuestionRequest{}, invalid(ErrorCodeQuestionInvalid, "question conversation is invalid", err)
	}
	mode := request.Scope.RetrievalMode
	if mode == "" {
		mode = retrievaldomain.SearchModeHybrid
	}
	search, err := retrievaldomain.CanonicalizeSearchRequest(retrievaldomain.SearchRequest{
		WorkspaceID: workspaceID,
		Query:       request.QuestionText,
		Mode:        mode,
		Filter:      request.Scope.Filter,
		Limit:       retrievaldomain.MaxSearchLimit,
	})
	if err != nil {
		return QuestionRequest{}, invalid(ErrorCodeQuestionInvalid, "question text or retrieval scope is invalid", err)
	}
	depth := request.AnswerDepth
	if depth == "" {
		depth = AnswerDepthStandard
	}
	if depth != AnswerDepthConcise && depth != AnswerDepthStandard && depth != AnswerDepthDetailed {
		return QuestionRequest{}, invalid(ErrorCodeQuestionInvalid, "answer depth is invalid", nil)
	}
	format := request.OutputFormat
	if format == "" {
		format = OutputFormatMarkdown
	}
	if format != OutputFormatMarkdown && format != OutputFormatOutline {
		return QuestionRequest{}, invalid(ErrorCodeQuestionInvalid, "output format is invalid", nil)
	}
	canonical := QuestionRequest{
		WorkspaceID: workspaceID, ConversationID: conversationID, QuestionText: search.Query,
		Scope: QuestionScope{
			RetrievalMode: search.Mode, Filter: search.Filter,
			AllowOriginalSources: request.Scope.AllowOriginalSources, AllowWeb: request.Scope.AllowWeb,
		},
		AnswerDepth: depth, OutputFormat: format,
	}
	if encoded, encodeErr := encodeCanonicalQuestionScope(canonical.Scope); encodeErr != nil || len(encoded) > MaxQuestionScopeBytes {
		return QuestionRequest{}, invalid(ErrorCodeQuestionInvalid, "question retrieval scope is oversized or cannot be encoded", encodeErr)
	}
	return canonical, nil
}

// EncodeQuestionScope 返回与请求哈希共享字段定义的 canonical 持久化 JSON。
func EncodeQuestionScope(request QuestionRequest) (json.RawMessage, error) {
	canonical, err := CanonicalizeQuestionRequest(request)
	if err != nil {
		return nil, err
	}
	encoded, err := encodeCanonicalQuestionScope(canonical.Scope)
	if err != nil {
		return nil, inconsistent(ErrorCodeQuestionInvalid, "question retrieval scope cannot be encoded")
	}
	return encoded, nil
}

// ComputeQuestionRequestHash 计算绑定正文、Scope 和回答选项的稳定幂等哈希。
func ComputeQuestionRequestHash(request QuestionRequest) (string, error) {
	canonical, err := CanonicalizeQuestionRequest(request)
	if err != nil {
		return "", err
	}
	payload := struct {
		SchemaVersion  int                   `json:"schema_version"`
		WorkspaceID    foundation.ID         `json:"workspace_id"`
		ConversationID foundation.ID         `json:"conversation_id"`
		QuestionText   string                `json:"question_text"`
		Scope          questionScopeDocument `json:"scope"`
		AnswerDepth    AnswerDepth           `json:"answer_depth"`
		OutputFormat   OutputFormat          `json:"output_format"`
	}{
		SchemaVersion: 1, WorkspaceID: canonical.WorkspaceID, ConversationID: canonical.ConversationID,
		QuestionText: canonical.QuestionText,
		Scope:        canonicalQuestionScopeDocument(canonical.Scope),
		AnswerDepth:  canonical.AnswerDepth, OutputFormat: canonical.OutputFormat,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", inconsistent(ErrorCodeQuestionInvalid, "question request hash payload is invalid")
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func encodeCanonicalQuestionScope(scope QuestionScope) (json.RawMessage, error) {
	return json.Marshal(canonicalQuestionScopeDocument(scope))
}

func canonicalQuestionScopeDocument(scope QuestionScope) questionScopeDocument {
	return questionScopeDocument{
		RetrievalMode:        scope.RetrievalMode,
		SourceIDs:            append([]foundation.ID{}, scope.Filter.SourceIDs...),
		SourceVersionIDs:     append([]foundation.ID{}, scope.Filter.SourceVersionIDs...),
		PathPrefixes:         append([]string{}, scope.Filter.PathPrefixes...),
		CapturedAtFrom:       canonicalTime(scope.Filter.CapturedAtFrom),
		CapturedAtBefore:     canonicalTime(scope.Filter.CapturedAtBefore),
		AllowOriginalSources: scope.AllowOriginalSources,
		AllowWeb:             scope.AllowWeb,
	}
}

func canonicalTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339Nano)
	return &formatted
}
