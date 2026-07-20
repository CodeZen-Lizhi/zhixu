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
	"github.com/go-chi/chi/v5"
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
}

// Handler 将 Conversation Application 映射到稳定 REST 契约。
type Handler struct {
	service Service
	cursors *CursorCodec
}

// NewHandler 创建 Conversation HTTP Handler；依赖缺失时路由 fail closed。
func NewHandler(service Service, cursors *CursorCodec) *Handler {
	return &Handler{service: service, cursors: cursors}
}

// Routes 在 `/api/v1` Router 下注册 Conversation 产品路由。
func (handler *Handler) Routes(router chi.Router) {
	router.Post("/conversations", handler.createConversation)
	router.Get("/conversations", handler.listConversations)
	router.Get("/conversations/{conversation_id}", handler.getConversation)
	router.Post("/conversations/{conversation_id}/questions", handler.submitQuestion)
	router.Get("/conversations/{conversation_id}/turns", handler.listTurns)
	router.Get("/answers/{answer_id}", handler.getAnswer)
	router.Post("/answers/{answer_id}/feedback", handler.submitFeedback)
}

type createConversationRequest struct {
	WorkspaceID string  `json:"workspace_id"`
	Title       *string `json:"title,omitempty"`
}

type questionRequest struct {
	WorkspaceID  string               `json:"workspace_id"`
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
	workspaceID, conversationID, err := parseScopedIDs(r.URL.Query().Get("workspace_id"), chi.URLParam(r, "conversation_id"))
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
	workspaceID, conversationID, err := parseScopedIDs(wire.WorkspaceID, chi.URLParam(r, "conversation_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	request, err := toQuestionRequest(wire, workspaceID, conversationID)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := handler.service.SubmitQuestion(r.Context(), application.SubmitQuestionCommand{Request: request, IdempotencyKey: r.Header.Get("Idempotency-Key")})
	if err != nil {
		writeError(w, err)
		return
	}
	view := application.AnswerView{Answer: result.Answer, Workflow: result.Workflow}
	response := acceptedQuestionResponse{Question: toQuestionResponse(result.Question), Answer: toAnswerResponse(view), StatusURL: answerStatusURL(result.Answer.ID, workspaceID)}
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
	workspaceID, conversationID, err := parseScopedIDs(r.URL.Query().Get("workspace_id"), chi.URLParam(r, "conversation_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	limit, err := parseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		writeError(w, err)
		return
	}
	cursor, err := handler.cursors.decodeTurn(r.URL.Query().Get("cursor"), workspaceID, conversationID)
	if err != nil {
		writeError(w, err)
		return
	}
	page, err := handler.service.ListTurns(r.Context(), application.ListTurnsQuery{WorkspaceID: workspaceID, ConversationID: conversationID, Cursor: cursor, Limit: limit})
	if err != nil {
		writeError(w, err)
		return
	}
	items := make([]turnResponse, len(page.Items))
	for i, item := range page.Items {
		items[i] = turnResponse{Question: toQuestionResponse(item.Question), Answer: toAnswerResponse(*item.Answer)}
	}
	next := ""
	if page.NextCursor != nil {
		next, err = handler.cursors.encodeTurn(workspaceID, conversationID, *page.NextCursor)
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
	workspaceID, answerID, err := parseScopedIDs(r.URL.Query().Get("workspace_id"), chi.URLParam(r, "answer_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	view, err := handler.service.GetAnswer(r.Context(), workspaceID, answerID)
	if err != nil {
		writeError(w, err)
		return
	}
	if notModifiedWithTag(w, r, answerETag(view.Answer.Version, view.Workflow.Version, view.CurrentStage)) {
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toAnswerResponse(view))
}

func (handler *Handler) submitFeedback(w http.ResponseWriter, r *http.Request) {
	if !handler.available(w) {
		return
	}
	var wire feedbackRequest
	if !decodeJSON(w, r, &wire) {
		return
	}
	workspaceID, answerID, err := parseScopedIDs(wire.WorkspaceID, chi.URLParam(r, "answer_id"))
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
	return conversationdomain.QuestionRequest{WorkspaceID: workspaceID, ConversationID: conversationID, QuestionText: wire.Question, Scope: conversationdomain.QuestionScope{RetrievalMode: retrievaldomain.SearchMode(wire.Scope.RetrievalMode), Filter: retrievaldomain.SearchFilter{SourceIDs: sourceIDs, SourceVersionIDs: versionIDs, PathPrefixes: wire.Scope.PathPrefixes, CapturedAtFrom: from, CapturedAtBefore: before}, AllowOriginalSources: wire.Scope.AllowOriginalSources, AllowWeb: wire.Scope.AllowWeb}, AnswerDepth: conversationdomain.AnswerDepth(wire.AnswerDepth), OutputFormat: conversationdomain.OutputFormat(wire.OutputFormat)}, nil
}

func toConversationResponse(value conversationdomain.Conversation) conversationResponse {
	return conversationResponse{ID: string(value.ID), WorkspaceID: string(value.WorkspaceID), Status: string(value.Status), Title: value.Title, Version: value.Version, LastActivityAt: formatTime(value.LastActivityAt), CreatedAt: formatTime(value.CreatedAt), UpdatedAt: formatTime(value.UpdatedAt), ArchivedAt: formatOptionalTime(value.ArchivedAt)}
}

func toQuestionResponse(value conversationdomain.Question) questionResponse {
	filter := value.Request.Scope.Filter
	return questionResponse{ID: string(value.ID), WorkspaceID: string(value.Request.WorkspaceID), ConversationID: string(value.Request.ConversationID), Question: value.Request.QuestionText, Ordinal: value.Ordinal, ContextThroughOrdinal: value.ContextThroughOrdinal, Scope: scopeResponse{RetrievalMode: string(value.Request.Scope.RetrievalMode), SourceIDs: idsToStrings(filter.SourceIDs), SourceVersionIDs: idsToStrings(filter.SourceVersionIDs), PathPrefixes: append([]string{}, filter.PathPrefixes...), CapturedAtFrom: formatOptionalTime(filter.CapturedAtFrom), CapturedAtBefore: formatOptionalTime(filter.CapturedAtBefore), AllowOriginalSources: value.Request.Scope.AllowOriginalSources, AllowWeb: value.Request.Scope.AllowWeb}, AnswerDepth: string(value.Request.AnswerDepth), OutputFormat: string(value.Request.OutputFormat), CreatedAt: formatTime(value.CreatedAt)}
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
func answerStatusURL(answerID, workspaceID foundation.ID) string {
	return "/api/v1/answers/" + url.PathEscape(string(answerID)) + "?workspace_id=" + url.QueryEscape(string(workspaceID))
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
	if classified.Kind == foundation.ErrorNonRetryableFailure {
		status = http.StatusServiceUnavailable
	}
	httpapi.WriteProblem(w, status, classified.Code, "请求未完成："+classified.Code, classified.Retryable, nil)
}

var _ Service = (*application.Service)(nil)
