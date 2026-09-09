// Package conversationhttp 暴露 Conversation、Question、Answer 与 Feedback 的 HTTP 边界。
package conversationhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/gin-gonic/gin"
)

const defaultPageLimit = 20

// Service 是 Handler 依赖的最小 Conversation 应用接口。
type Service interface {
	// CreateConversation 创建或精确重放短期会话。
	CreateConversation(context.Context, application.CreateConversationCommand) (application.CreateConversationResult, error)
	// SubmitQuestion 接受问题并原子启动异步 RAG Workflow。
	SubmitQuestion(context.Context, application.SubmitQuestionCommand) (application.SubmitQuestionResult, error)
	// SubmitFeedback 追加或精确重放 Answer 反馈。
	SubmitFeedback(context.Context, application.SubmitFeedbackCommand) (application.SubmitFeedbackResult, error)
	// ListConversations 返回 Workspace 内的稳定分页会话。
	ListConversations(context.Context, application.ListConversationsQuery) (application.ConversationPage, error)
	// GetConversation 返回 Workspace 内的一个会话。
	GetConversation(context.Context, foundation.ID, foundation.ID) (conversationdomain.Conversation, error)
	// ListTurns 返回会话内的稳定分页 Turn 投影。
	ListTurns(context.Context, application.ListTurnsQuery) (application.TurnPage, error)
	// GetAnswer 返回 Answer 与 Workflow 权威状态投影。
	GetAnswer(context.Context, foundation.ID, foundation.ID) (application.AnswerView, error)
	// GetWorkspaceAnalysisTimeline 返回 Answer-scoped Workspace Analysis 权威时间线。
	GetWorkspaceAnalysisTimeline(context.Context, application.WorkspaceAnalysisTimelineQuery) (conversationdomain.WorkspaceAnalysisTimeline, error)
}

// Handler 将 Conversation Application 映射到稳定 REST 契约。
type Handler struct {
	service Service
	cursors *CursorCodec
	version application.APIVersion
}

// NewHandler 创建 Conversation HTTP Handler；依赖缺失时路由 fail closed。
func NewHandler(service Service, cursors *CursorCodec) *Handler {
	return &Handler{service: service, cursors: cursors}
}

// Routes 在 `/api/v1` Router 下注册 Conversation 产品路由。
func (handler *Handler) Routes(router gin.IRouter) {
	router.POST("/conversations", httpapi.GinHandler(handler.createConversation))
	router.GET("/conversations", httpapi.GinHandler(handler.listConversations))
	router.GET("/conversations/:conversation_id", httpapi.GinHandler(handler.getConversation))
	handler.answerRoutes(router, application.APIVersionV1)
	router.POST("/answers/:answer_id/feedback", httpapi.GinHandler(handler.submitFeedback))
}

// RoutesV2 exposes only the four endpoints whose response contracts include
// dynamic Workspace Analysis. Shared Workflow, Source, feedback and SSE URLs
// remain under v1.
func (handler *Handler) RoutesV2(router gin.IRouter) {
	handler.answerRoutes(router, application.APIVersionV2)
}

func (handler *Handler) answerRoutes(router gin.IRouter, version application.APIVersion) {
	// Each registration owns its version. The same Handler can safely be
	// registered under both groups without a mutable per-request version.
	var versioned Handler
	if handler != nil {
		versioned = *handler
	}
	versioned.version = version
	router.POST("/conversations/:conversation_id/questions", httpapi.GinHandler(versioned.submitQuestion))
	router.GET("/conversations/:conversation_id/turns", httpapi.GinHandler(versioned.listTurns))
	router.GET("/answers/:answer_id", httpapi.GinHandler(versioned.getAnswer))
	router.GET("/answers/:answer_id/analysis-timeline", httpapi.GinHandler(versioned.getWorkspaceAnalysisTimeline))
}

type createConversationRequest struct {
	WorkspaceID string  `json:"workspace_id"`
	Title       *string `json:"title,omitempty"`
}

type questionRequest struct {
	WorkspaceID  string               `json:"workspace_id"`
	Mode         string               `json:"mode,omitempty"`
	Question     string               `json:"question"`
	Scope        questionScopeRequest `json:"scope,omitempty"`
	AnswerDepth  string               `json:"answer_depth,omitempty"`
	OutputFormat string               `json:"output_format,omitempty"`
}

type questionScopeRequest struct {
	RetrievalMode        string   `json:"retrieval_mode,omitempty"`
	SourceIDs            []string `json:"source_ids,omitempty"`
	SourceVersionIDs     []string `json:"source_version_ids,omitempty"`
	PathPrefixes         []string `json:"path_prefixes,omitempty"`
	CapturedAtFrom       *string  `json:"captured_at_from,omitempty"`
	CapturedAtBefore     *string  `json:"captured_at_before,omitempty"`
	AllowOriginalSources bool     `json:"allow_original_sources,omitempty"`
	AllowWeb             bool     `json:"allow_web,omitempty"`
}

type feedbackRequest struct {
	WorkspaceID string  `json:"workspace_id"`
	Type        string  `json:"feedback_type"`
	CitationID  *string `json:"citation_id,omitempty"`
	Comment     *string `json:"comment,omitempty"`
}

type pageResponse[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type conversationResponse struct {
	ID             string  `json:"id"`
	WorkspaceID    string  `json:"workspace_id"`
	Status         string  `json:"status"`
	Title          *string `json:"title"`
	Version        int64   `json:"version"`
	LastActivityAt string  `json:"last_activity_at"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
	ArchivedAt     *string `json:"archived_at"`
}

type questionResponse struct {
	ID                    string        `json:"id"`
	WorkspaceID           string        `json:"workspace_id"`
	ConversationID        string        `json:"conversation_id"`
	Mode                  string        `json:"mode"`
	Question              string        `json:"question"`
	Ordinal               int64         `json:"ordinal"`
	ContextThroughOrdinal int64         `json:"context_through_ordinal"`
	Scope                 scopeResponse `json:"scope"`
	AnswerDepth           string        `json:"answer_depth"`
	OutputFormat          string        `json:"output_format"`
	CreatedAt             string        `json:"created_at"`
}

type scopeResponse struct {
	RetrievalMode        string   `json:"retrieval_mode"`
	SourceIDs            []string `json:"source_ids"`
	SourceVersionIDs     []string `json:"source_version_ids"`
	PathPrefixes         []string `json:"path_prefixes"`
	CapturedAtFrom       *string  `json:"captured_at_from"`
	CapturedAtBefore     *string  `json:"captured_at_before"`
	AllowOriginalSources bool     `json:"allow_original_sources"`
	AllowWeb             bool     `json:"allow_web"`
}

type answerResponse struct {
	ID                string                               `json:"id"`
	WorkspaceID       string                               `json:"workspace_id"`
	ConversationID    string                               `json:"conversation_id"`
	QuestionID        string                               `json:"question_id"`
	PublicationStatus string                               `json:"publication_status"`
	ResultType        string                               `json:"result_type,omitempty"`
	Result            json.RawMessage                      `json:"result,omitempty"`
	AssistantText     string                               `json:"assistant_text,omitempty"`
	Citations         []citationResponse                   `json:"citations"`
	RetrievalSummary  *conversationdomain.RetrievalSummary `json:"retrieval_summary"`
	CurrentStage      *string                              `json:"current_stage"`
	Workflow          workflowResponse                     `json:"workflow"`
	Version           int64                                `json:"version"`
	CreatedAt         string                               `json:"created_at"`
	UpdatedAt         string                               `json:"updated_at"`
}

type citationResponse struct {
	ID              string `json:"id"`
	WorkspaceID     string `json:"workspace_id"`
	IndexVersionID  string `json:"index_version_id"`
	ChunkID         string `json:"chunk_id"`
	SourceVersionID string `json:"source_version_id"`
	SourceSpanID    string `json:"source_span_id"`
	Href            string `json:"href"`
}

type workflowResponse struct {
	RunID     string `json:"run_id"`
	Status    string `json:"status"`
	Version   int64  `json:"version"`
	UpdatedAt string `json:"updated_at"`
	StatusURL string `json:"status_url"`
}

type acceptedQuestionResponse struct {
	Question  questionResponse `json:"question"`
	Answer    answerResponse   `json:"answer"`
	StatusURL string           `json:"status_url"`
}

type turnResponse struct {
	Question questionResponse `json:"question"`
	Answer   answerResponse   `json:"answer"`
}

type feedbackResponse struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	AnswerID    string  `json:"answer_id"`
	Type        string  `json:"feedback_type"`
	CitationID  *string `json:"citation_id"`
	Comment     *string `json:"comment"`
	CreatedAt   string  `json:"created_at"`
}

func (handler *Handler) createConversation(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	var wire createConversationRequest
	if !decodeJSON(w, r, &wire) {
		return
	}
	workspaceID, err := parseID(wire.WorkspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := handler.service.CreateConversation(r.Context(), application.CreateConversationCommand{WorkspaceID: workspaceID, Title: wire.Title, IdempotencyKey: r.Header.Get("Idempotency-Key")})
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	w.Header().Set("ETag", etag(result.Conversation.Version))
	httpapi.WriteJSON(w, status, toConversationResponse(result.Conversation))
}

func (handler *Handler) listConversations(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	workspaceID, err := parseID(r.URL.Query().Get("workspace_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	limit, err := parseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		writeError(w, err)
		return
	}
	cursor, err := handler.cursors.decodeConversation(r.URL.Query().Get("cursor"), workspaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	page, err := handler.service.ListConversations(r.Context(), application.ListConversationsQuery{WorkspaceID: workspaceID, Cursor: cursor, Limit: limit})
	if err != nil {
		writeError(w, err)
		return
	}
	items := make([]conversationResponse, len(page.Items))
	for i := range page.Items {
		items[i] = toConversationResponse(page.Items[i])
	}
	next := ""
	if page.NextCursor != nil {
		next, err = handler.cursors.encodeConversation(workspaceID, *page.NextCursor)
		if err != nil {
			writeError(w, err)
			return
		}
	}
	httpapi.WriteJSON(w, http.StatusOK, pageResponse[conversationResponse]{Items: items, NextCursor: next})
}

func (handler *Handler) getConversation(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	workspaceID, conversationID, err := parseScopedIDs(r.URL.Query().Get("workspace_id"), r.PathValue("conversation_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	conversation, err := handler.service.GetConversation(r.Context(), workspaceID, conversationID)
	if err != nil {
		writeError(w, err)
		return
	}
	if notModified(w, r, conversation.Version) {
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toConversationResponse(conversation))
}

func (handler *Handler) submitQuestion(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	var wire questionRequest
	if !decodeJSON(w, r, &wire) {
		return
	}
	workspaceID, conversationID, err := parseScopedIDs(wire.WorkspaceID, r.PathValue("conversation_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	request, err := toQuestionRequest(wire, workspaceID, conversationID)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := handler.service.SubmitQuestion(r.Context(), application.SubmitQuestionCommand{Request: request, IdempotencyKey: r.Header.Get("Idempotency-Key"), APIVersion: handler.version})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := handler.version.CheckWorkflow(result.Workflow.DefinitionKey, result.Workflow.DefinitionVersion); err != nil {
		writeError(w, err)
		return
	}
	view := application.AnswerView{Answer: result.Answer, Workflow: result.Workflow}
	if result.Answer.PublicationStatus != conversationdomain.AnswerPublicationPending {
		projection, err := conversationdomain.ProjectPublishedAnswer(result.Answer.ResultType, result.Answer.Result)
		if err != nil {
			writeError(w, err)
			return
		}
		view.AssistantText, view.Citations = projection.AssistantText, projection.Citations
	}
	response := acceptedQuestionResponse{Question: toQuestionResponse(result.Question), Answer: toAnswerResponse(view), StatusURL: answerStatusURL(result.Answer.ID, workspaceID, handler.version)}
	status := http.StatusAccepted
	if result.Replayed {
		status = http.StatusOK
	}
	httpapi.WriteJSON(w, status, response)
}

func (handler *Handler) listTurns(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	workspaceID, conversationID, err := parseScopedIDs(r.URL.Query().Get("workspace_id"), r.PathValue("conversation_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	limit, err := parseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		writeError(w, err)
		return
	}
	latest := false
	if raw := r.URL.Query().Get("latest"); raw != "" {
		if raw != "true" {
			writeError(w, foundation.NewError(foundation.ErrorInvalidInput, conversationdomain.ErrorCodeCursorInvalid, false, errors.New("latest must be true")))
			return
		}
		latest = true
		limit = 1
	}
	cursor, err := handler.cursors.decodeTurn(r.URL.Query().Get("cursor"), workspaceID, conversationID, handler.version)
	if err != nil {
		writeError(w, err)
		return
	}
	page, err := handler.service.ListTurns(r.Context(), application.ListTurnsQuery{WorkspaceID: workspaceID, ConversationID: conversationID, Cursor: cursor, Limit: limit, Latest: latest, APIVersion: handler.version})
	if err != nil {
		writeError(w, err)
		return
	}
	items := make([]turnResponse, len(page.Items))
	for i, item := range page.Items {
		if item.Answer == nil {
			writeError(w, foundation.NewError(foundation.ErrorConsistencyViolation, "CONVERSATION_RESULT_INCONSISTENT", false, errors.New("turn answer is missing")))
			return
		}
		if err := handler.version.CheckWorkflow(item.Answer.Workflow.DefinitionKey, item.Answer.Workflow.DefinitionVersion); err != nil {
			writeError(w, err)
			return
		}
		items[i] = turnResponse{Question: toQuestionResponse(item.Question), Answer: toAnswerResponse(*item.Answer)}
	}
	next := ""
	if page.NextCursor != nil {
		next, err = handler.cursors.encodeTurn(workspaceID, conversationID, *page.NextCursor, handler.version)
		if err != nil {
			writeError(w, err)
			return
		}
	}
	httpapi.WriteJSON(w, http.StatusOK, pageResponse[turnResponse]{Items: items, NextCursor: next})
}

func (handler *Handler) getAnswer(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	workspaceID, answerID, err := parseScopedIDs(r.URL.Query().Get("workspace_id"), r.PathValue("answer_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	view, err := handler.service.GetAnswer(r.Context(), workspaceID, answerID)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := handler.version.CheckWorkflow(view.Workflow.DefinitionKey, view.Workflow.DefinitionVersion); err != nil {
		writeError(w, err)
		return
	}
	if notModifiedWithTag(w, r, answerETag(view.Answer.Version, view.Workflow.Version, view.CurrentStage)) {
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toAnswerResponse(view))
}

func (handler *Handler) getWorkspaceAnalysisTimeline(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	workspaceID, answerID, err := parseScopedIDs(r.URL.Query().Get("workspace_id"), r.PathValue("answer_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	timeline, err := handler.service.GetWorkspaceAnalysisTimeline(r.Context(), application.WorkspaceAnalysisTimelineQuery{WorkspaceID: workspaceID, AnswerID: answerID, APIVersion: handler.version})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := handler.version.CheckTimeline(timeline); err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, timeline)
}

func (handler *Handler) submitFeedback(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	var wire feedbackRequest
	if !decodeJSON(w, r, &wire) {
		return
	}
	workspaceID, answerID, err := parseScopedIDs(wire.WorkspaceID, r.PathValue("answer_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := handler.service.SubmitFeedback(r.Context(), application.SubmitFeedbackCommand{Request: conversationdomain.FeedbackRequest{WorkspaceID: workspaceID, AnswerID: answerID, Type: conversationdomain.FeedbackType(wire.Type), CitationID: wire.CitationID, Comment: wire.Comment}, IdempotencyKey: r.Header.Get("Idempotency-Key")})
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	httpapi.WriteJSON(w, status, toFeedbackResponse(result.Feedback))
}

func (handler *Handler) available(w http.ResponseWriter) bool {
	if handler == nil || handler.service == nil || handler.cursors == nil {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, "CONVERSATION_HTTP_UNAVAILABLE", "Conversation 服务暂不可用", false, nil)
		return false
	}
	return true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	if strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
		httpapi.WriteProblem(w, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "必须提供有效的 Idempotency-Key", false, nil)
		return false
	}
	mediaType, _, mediaTypeErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaTypeErr != nil || !strings.EqualFold(mediaType, "application/json") {
		httpapi.WriteProblem(w, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "请求必须使用 application/json", false, nil)
		return false
	}
	if err := httpapi.DecodeJSON(r, target); err != nil {
		httpapi.WriteProblem(w, http.StatusBadRequest, "INVALID_JSON", "请求 JSON 无效", false, nil)
		return false
	}
	return true
}

func toQuestionRequest(wire questionRequest, workspaceID, conversationID foundation.ID) (conversationdomain.QuestionRequest, error) {
	sourceIDs, err := parseIDs(wire.Scope.SourceIDs)
	if err != nil {
		return conversationdomain.QuestionRequest{}, err
	}
	versionIDs, err := parseIDs(wire.Scope.SourceVersionIDs)
	if err != nil {
		return conversationdomain.QuestionRequest{}, err
	}
	from, err := parseTime(wire.Scope.CapturedAtFrom)
	if err != nil {
		return conversationdomain.QuestionRequest{}, err
	}
	before, err := parseTime(wire.Scope.CapturedAtBefore)
	if err != nil {
		return conversationdomain.QuestionRequest{}, err
	}
	return conversationdomain.QuestionRequest{WorkspaceID: workspaceID, ConversationID: conversationID, Mode: conversationdomain.QuestionMode(wire.Mode), QuestionText: wire.Question, Scope: conversationdomain.QuestionScope{RetrievalMode: retrievaldomain.SearchMode(wire.Scope.RetrievalMode), Filter: retrievaldomain.SearchFilter{SourceIDs: sourceIDs, SourceVersionIDs: versionIDs, PathPrefixes: wire.Scope.PathPrefixes, CapturedAtFrom: from, CapturedAtBefore: before}, AllowOriginalSources: wire.Scope.AllowOriginalSources, AllowWeb: wire.Scope.AllowWeb}, AnswerDepth: conversationdomain.AnswerDepth(wire.AnswerDepth), OutputFormat: conversationdomain.OutputFormat(wire.OutputFormat)}, nil
}

func toConversationResponse(value conversationdomain.Conversation) conversationResponse {
	return conversationResponse{ID: string(value.ID), WorkspaceID: string(value.WorkspaceID), Status: string(value.Status), Title: value.Title, Version: value.Version, LastActivityAt: formatTime(value.LastActivityAt), CreatedAt: formatTime(value.CreatedAt), UpdatedAt: formatTime(value.UpdatedAt), ArchivedAt: formatOptionalTime(value.ArchivedAt)}
}

func toQuestionResponse(value conversationdomain.Question) questionResponse {
	filter := value.Request.Scope.Filter
	return questionResponse{ID: string(value.ID), WorkspaceID: string(value.Request.WorkspaceID), ConversationID: string(value.Request.ConversationID), Mode: string(value.Request.Mode), Question: value.Request.QuestionText, Ordinal: value.Ordinal, ContextThroughOrdinal: value.ContextThroughOrdinal, Scope: scopeResponse{RetrievalMode: string(value.Request.Scope.RetrievalMode), SourceIDs: idsToStrings(filter.SourceIDs), SourceVersionIDs: idsToStrings(filter.SourceVersionIDs), PathPrefixes: append([]string{}, filter.PathPrefixes...), CapturedAtFrom: formatOptionalTime(filter.CapturedAtFrom), CapturedAtBefore: formatOptionalTime(filter.CapturedAtBefore), AllowOriginalSources: value.Request.Scope.AllowOriginalSources, AllowWeb: value.Request.Scope.AllowWeb}, AnswerDepth: string(value.Request.AnswerDepth), OutputFormat: string(value.Request.OutputFormat), CreatedAt: formatTime(value.CreatedAt)}
}

func toAnswerResponse(view application.AnswerView) answerResponse {
	answer := view.Answer
	citations := make([]citationResponse, len(view.Citations))
	for i, citation := range view.Citations {
		citations[i] = toCitationResponse(citation)
	}
	var currentStage *string
	if view.CurrentStage != nil {
		value := string(*view.CurrentStage)
		currentStage = &value
	}
	return answerResponse{ID: string(answer.ID), WorkspaceID: string(answer.WorkspaceID), ConversationID: string(answer.ConversationID), QuestionID: string(answer.QuestionID), PublicationStatus: string(answer.PublicationStatus), ResultType: string(answer.ResultType), Result: append(json.RawMessage(nil), answer.Result...), AssistantText: view.AssistantText, Citations: citations, RetrievalSummary: answer.RetrievalSummary, CurrentStage: currentStage, Workflow: workflowResponse{RunID: string(view.Workflow.RunID), Status: string(view.Workflow.Status), Version: view.Workflow.Version, UpdatedAt: formatTime(view.Workflow.UpdatedAt), StatusURL: "/api/v1/workflows/" + url.PathEscape(string(view.Workflow.RunID))}, Version: answer.Version, CreatedAt: formatTime(answer.CreatedAt), UpdatedAt: formatTime(answer.UpdatedAt)}
}

func toCitationResponse(value agentdomain.Citation) citationResponse {
	return citationResponse{ID: value.ID, WorkspaceID: string(value.WorkspaceID), IndexVersionID: string(value.IndexVersionID), ChunkID: string(value.ChunkID), SourceVersionID: string(value.SourceVersionID), SourceSpanID: string(value.SourceSpanID), Href: "/api/v1/workspaces/" + url.PathEscape(string(value.WorkspaceID)) + "/source-versions/" + url.PathEscape(string(value.SourceVersionID)) + "/spans/" + url.PathEscape(string(value.SourceSpanID))}
}

func toFeedbackResponse(value conversationdomain.AnswerFeedback) feedbackResponse {
	return feedbackResponse{ID: string(value.ID), WorkspaceID: string(value.Request.WorkspaceID), AnswerID: string(value.Request.AnswerID), Type: string(value.Request.Type), CitationID: value.Request.CitationID, Comment: value.Request.Comment, CreatedAt: formatTime(value.CreatedAt)}
}

func parseScopedIDs(first, second string) (foundation.ID, foundation.ID, error) {
	a, err := parseID(first)
	if err != nil {
		return "", "", err
	}
	b, err := parseID(second)
	if err != nil || a == b {
		return "", "", invalidRequest()
	}
	return a, b, nil
}
func parseID(value string) (foundation.ID, error) {
	if strings.TrimSpace(value) != value || value == "" {
		return "", invalidRequest()
	}
	id, err := foundation.ParseID(value)
	if err != nil {
		return "", invalidRequest()
	}
	return id, nil
}
func parseIDs(values []string) ([]foundation.ID, error) {
	result := make([]foundation.ID, len(values))
	for i := range values {
		id, err := parseID(values[i])
		if err != nil {
			return nil, err
		}
		result[i] = id
	}
	return result, nil
}
func parseTime(value *string) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, *value)
	if err != nil || parsed.Format(time.RFC3339Nano) != *value {
		return nil, invalidRequest()
	}
	utc := parsed.UTC()
	return &utc, nil
}
func parseLimit(raw string) (int, error) {
	if raw == "" {
		return defaultPageLimit, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || conversationdomain.ValidatePageLimit(value) != nil {
		return 0, invalidCursor()
	}
	return value, nil
}
func idsToStrings(values []foundation.ID) []string {
	result := make([]string, len(values))
	for i := range values {
		result[i] = string(values[i])
	}
	return result
}
func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
func formatOptionalTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	text := formatTime(*value)
	return &text
}
func answerStatusURL(answerID, workspaceID foundation.ID, version application.APIVersion) string {
	return "/api/v" + strconv.Itoa(int(version)) + "/answers/" + url.PathEscape(string(answerID)) + "?workspace_id=" + url.QueryEscape(string(workspaceID))
}
func etag(version int64) string { return fmt.Sprintf("W/\"%d\"", version) }
func notModified(w http.ResponseWriter, r *http.Request, version int64) bool {
	return notModifiedWithTag(w, r, etag(version))
}
func answerETag(answerVersion, workflowVersion int64, currentStage *application.RAGCurrentStage) string {
	stage := "none"
	if currentStage != nil {
		stage = string(*currentStage)
	}
	return fmt.Sprintf("W/\"answer-%d-workflow-%d-stage-%s\"", answerVersion, workflowVersion, stage)
}
func notModifiedWithTag(w http.ResponseWriter, r *http.Request, value string) bool {
	w.Header().Set("ETag", value)
	if r.Header.Get("If-None-Match") == value {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	return false
}
func invalidRequest() error {
	return foundation.NewError(foundation.ErrorInvalidInput, "CONVERSATION_REQUEST_INVALID", false, errors.New("conversation request is invalid"))
}

func writeError(w http.ResponseWriter, err error) {
	if errors.Is(err, context.Canceled) {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, "CONVERSATION_REQUEST_CANCELLED", "请求已取消", false, nil)
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		httpapi.WriteProblem(w, http.StatusServiceUnavailable, "CONVERSATION_REQUEST_TIMEOUT", "请求超时", true, nil)
		return
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		httpapi.WriteProblem(w, http.StatusInternalServerError, "INTERNAL_ERROR", "服务处理失败", false, nil)
		return
	}
	status := httpapi.StatusForErrorKind(classified.Kind)
	if classified.Code == application.ErrorCodeAPIVersionUnsupported {
		httpapi.WriteProblem(w, http.StatusConflict, classified.Code, "此分析任务需要使用 /api/v2 接口", false, nil)
		return
	}
	if classified.Kind == foundation.ErrorNonRetryableFailure {
		status = http.StatusServiceUnavailable
	}
	httpapi.WriteProblem(w, status, classified.Code, "请求未完成："+classified.Code, classified.Retryable, nil)
}

var _ Service = (*application.Service)(nil)
