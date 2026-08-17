package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	foundationstrictjson "github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const (
	// MaxQuestionBytes 是 Question 正文允许的最大 UTF-8 字节数。
	MaxQuestionBytes = retrievaldomain.MaxSearchQueryBytes
	// MaxQuestionScopeBytes 是持久化 canonical Scope JSON 的最大字节数。
	MaxQuestionScopeBytes = 16 * 1024
	// QuestionRequestHashSchemaVersionV1 是保留历史 RAG 字节布局的请求哈希版本。
	QuestionRequestHashSchemaVersionV1 = 1
	// QuestionRequestHashSchemaVersionV2 是绑定 Workspace Analysis mode 的请求哈希版本。
	QuestionRequestHashSchemaVersionV2 = 2
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

type questionScopePersistenceDocument struct {
	RetrievalMode        *retrievaldomain.SearchMode `json:"retrieval_mode"`
	SourceIDs            *[]foundation.ID            `json:"source_ids"`
	SourceVersionIDs     *[]foundation.ID            `json:"source_version_ids"`
	PathPrefixes         *[]string                   `json:"path_prefixes"`
	CapturedAtFrom       json.RawMessage             `json:"captured_at_from"`
	CapturedAtBefore     json.RawMessage             `json:"captured_at_before"`
	AllowOriginalSources *bool                       `json:"allow_original_sources"`
	AllowWeb             *bool                       `json:"allow_web"`
}

// QuestionMode 区分固定证据问答与受限工作区分析。
type QuestionMode string

const (
	// QuestionModeRAG 表示现有固定 RAG 问答，也是省略 mode 时的兼容默认值。
	QuestionModeRAG QuestionMode = "rag"
	// QuestionModeWorkspaceAnalysis 表示受限、持久且只读的工作区分析。
	QuestionModeWorkspaceAnalysis QuestionMode = "workspace_analysis"
)

// CanonicalizeQuestionMode 将空值归一为历史 RAG，并拒绝未知执行模式。
func CanonicalizeQuestionMode(mode QuestionMode) (QuestionMode, error) {
	if mode == "" {
		return QuestionModeRAG, nil
	}
	switch mode {
	case QuestionModeRAG, QuestionModeWorkspaceAnalysis:
		return mode, nil
	default:
		return "", invalid(WorkspaceAnalysisModeInvalidCode, "question mode is invalid", nil)
	}
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
	Mode           QuestionMode
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
		ValidateContextHash(question.ContextHash) != nil || !validLowerHash(question.RequestHash) || question.CreatedAt.IsZero() {
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
	questionMode, err := CanonicalizeQuestionMode(request.Mode)
	if err != nil {
		return QuestionRequest{}, err
	}
	if questionMode == QuestionModeWorkspaceAnalysis && !workspaceAnalysisQuestionScopeSupported(request.Scope) {
		return QuestionRequest{}, invalid(WorkspaceAnalysisScopeUnsupportedCode, "workspace analysis scope is unsupported", nil)
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
		WorkspaceID: workspaceID, ConversationID: conversationID, Mode: questionMode, QuestionText: search.Query,
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

func workspaceAnalysisQuestionScopeSupported(scope QuestionScope) bool {
	return !scope.AllowOriginalSources && !scope.AllowWeb &&
		len(scope.Filter.SourceIDs) == 0 && len(scope.Filter.SourceVersionIDs) == 0 &&
		len(scope.Filter.PathPrefixes) == 0 && scope.Filter.CapturedAtFrom == nil && scope.Filter.CapturedAtBefore == nil
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

// DecodeQuestionScope 严格解析持久化 Scope，并拒绝非规范过滤器或时间表示。
func DecodeQuestionScope(workspaceID foundation.ID, raw json.RawMessage) (QuestionScope, error) {
	parsedWorkspaceID, err := foundation.ParseID(string(workspaceID))
	if err != nil || parsedWorkspaceID != workspaceID {
		return QuestionScope{}, invalid(ErrorCodeQuestionInvalid, "question scope workspace is invalid", err)
	}
	limits := foundationstrictjson.DefaultLimits()
	limits.MaxDocumentBytes = MaxQuestionScopeBytes
	limits.MaxStringBytes = MaxQuestionScopeBytes
	limits.MaxArrayItems = retrievaldomain.MaxSearchFilterValues
	limits.MaxObjectFields = 8
	persisted, err := foundationstrictjson.DecodeObject[questionScopePersistenceDocument](raw, limits, nil)
	if err != nil || persisted.RetrievalMode == nil || persisted.SourceIDs == nil || persisted.SourceVersionIDs == nil ||
		persisted.PathPrefixes == nil || persisted.AllowOriginalSources == nil || persisted.AllowWeb == nil {
		return QuestionScope{}, invalid(ErrorCodeQuestionInvalid, "question scope document is invalid", err)
	}
	fromText, err := decodeNullableJSONString(persisted.CapturedAtFrom)
	if err != nil {
		return QuestionScope{}, invalid(ErrorCodeQuestionInvalid, "question scope start time is invalid", err)
	}
	from, err := parseQuestionScopeTime(fromText)
	if err != nil {
		return QuestionScope{}, invalid(ErrorCodeQuestionInvalid, "question scope start time is invalid", err)
	}
	beforeText, err := decodeNullableJSONString(persisted.CapturedAtBefore)
	if err != nil {
		return QuestionScope{}, invalid(ErrorCodeQuestionInvalid, "question scope end time is invalid", err)
	}
	before, err := parseQuestionScopeTime(beforeText)
	if err != nil {
		return QuestionScope{}, invalid(ErrorCodeQuestionInvalid, "question scope end time is invalid", err)
	}
	document := questionScopeDocument{
		RetrievalMode:        *persisted.RetrievalMode,
		SourceIDs:            append([]foundation.ID{}, (*persisted.SourceIDs)...),
		SourceVersionIDs:     append([]foundation.ID{}, (*persisted.SourceVersionIDs)...),
		PathPrefixes:         append([]string{}, (*persisted.PathPrefixes)...),
		CapturedAtFrom:       fromText,
		CapturedAtBefore:     beforeText,
		AllowOriginalSources: *persisted.AllowOriginalSources,
		AllowWeb:             *persisted.AllowWeb,
	}
	search, err := retrievaldomain.CanonicalizeSearchRequest(retrievaldomain.SearchRequest{
		WorkspaceID: workspaceID,
		Query:       "question-scope",
		Mode:        document.RetrievalMode,
		Filter: retrievaldomain.SearchFilter{
			SourceIDs: document.SourceIDs, SourceVersionIDs: document.SourceVersionIDs,
			PathPrefixes: document.PathPrefixes, CapturedAtFrom: from, CapturedAtBefore: before,
		},
		Limit: 1,
	})
	if err != nil {
		return QuestionScope{}, invalid(ErrorCodeQuestionInvalid, "question scope document contains an invalid filter", err)
	}
	scope := QuestionScope{
		RetrievalMode:        search.Mode,
		Filter:               search.Filter,
		AllowOriginalSources: document.AllowOriginalSources,
		AllowWeb:             document.AllowWeb,
	}
	if !reflect.DeepEqual(canonicalQuestionScopeDocument(scope), document) {
		return QuestionScope{}, invalid(ErrorCodeQuestionInvalid, "question scope document is not canonical", nil)
	}
	return scope, nil
}

// ComputeQuestionRequestHash 计算绑定正文、Scope、执行模式和回答选项的稳定幂等哈希。
// RAG 保留历史 v1 字节布局；只有 Workspace Analysis 使用包含 mode 的 v2 布局。
func ComputeQuestionRequestHash(request QuestionRequest) (string, error) {
	canonical, err := CanonicalizeQuestionRequest(request)
	if err != nil {
		return "", err
	}
	legacyPayload := struct {
		SchemaVersion  int                   `json:"schema_version"`
		WorkspaceID    foundation.ID         `json:"workspace_id"`
		ConversationID foundation.ID         `json:"conversation_id"`
		QuestionText   string                `json:"question_text"`
		Scope          questionScopeDocument `json:"scope"`
		AnswerDepth    AnswerDepth           `json:"answer_depth"`
		OutputFormat   OutputFormat          `json:"output_format"`
	}{
		SchemaVersion: QuestionRequestHashSchemaVersionV1, WorkspaceID: canonical.WorkspaceID, ConversationID: canonical.ConversationID,
		QuestionText: canonical.QuestionText,
		Scope:        canonicalQuestionScopeDocument(canonical.Scope),
		AnswerDepth:  canonical.AnswerDepth, OutputFormat: canonical.OutputFormat,
	}
	var encoded []byte
	if canonical.Mode == QuestionModeRAG {
		encoded, err = json.Marshal(legacyPayload)
	} else {
		workspacePayload := struct {
			SchemaVersion  int                   `json:"schema_version"`
			WorkspaceID    foundation.ID         `json:"workspace_id"`
			ConversationID foundation.ID         `json:"conversation_id"`
			Mode           QuestionMode          `json:"mode"`
			QuestionText   string                `json:"question_text"`
			Scope          questionScopeDocument `json:"scope"`
			AnswerDepth    AnswerDepth           `json:"answer_depth"`
			OutputFormat   OutputFormat          `json:"output_format"`
		}{
			SchemaVersion: QuestionRequestHashSchemaVersionV2, WorkspaceID: canonical.WorkspaceID, ConversationID: canonical.ConversationID,
			Mode: canonical.Mode, QuestionText: canonical.QuestionText, Scope: canonicalQuestionScopeDocument(canonical.Scope),
			AnswerDepth: canonical.AnswerDepth, OutputFormat: canonical.OutputFormat,
		}
		encoded, err = json.Marshal(workspacePayload)
	}
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

func parseQuestionScopeTime(value *string) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, *value)
	if err != nil {
		return nil, err
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

func decodeNullableJSONString(raw json.RawMessage) (*string, error) {
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var value string
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return nil, err
	}
	return &value, nil
}
