// Package http exposes the strict Workspace/Document file-history API.
package http

import (
	"context"
	"errors"
	"io"
	"mime"
	stdhttp "net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/CodeZen-Lizhi/zhixu/internal/documenthistory/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/documenthistory/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
)

const (
	defaultTimeout       = 5 * time.Second
	defaultHistoryLimit  = 30
	maxRequestBodyBytes  = 16 * 1024
	errorInvalidJSON     = "INVALID_JSON"
	errorUnsupportedType = "UNSUPPORTED_MEDIA_TYPE"
	errorUnavailable     = "DOCUMENT_HISTORY_HTTP_UNAVAILABLE"
)

// Service is the complete Document History application boundary.
type Service interface {
	ListHistory(context.Context, application.HistoryQuery) (domain.Page, error)
	CompareVersions(context.Context, application.CompareQuery) (domain.Diff, error)
	PreviewRestore(context.Context, application.PreviewCommand) (domain.RestorePreview, error)
	CreateRestoreProposal(context.Context, application.RestoreCommand) (application.RestoreProposalReceipt, error)
}

// Handler maps strict HTTP requests to Document History operations.
type Handler struct {
	service Service
	timeout time.Duration
}

// NewHandler creates a fail-closed Document History HTTP handler.
func NewHandler(service Service, timeout time.Duration) *Handler {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Handler{service: service, timeout: timeout}
}

// Available reports whether a real application service is configured.
func (handler *Handler) Available() bool { return handler != nil && handler.service != nil }

// Routes registers Document-scoped history, compare and restore endpoints.
func (handler *Handler) Routes(router chi.Router) {
	router.Get("/workspaces/{workspaceID}/documents/{documentID}/history", handler.listHistory)
	router.Get("/workspaces/{workspaceID}/documents/{documentID}/history/compare", handler.compare)
	router.Post("/workspaces/{workspaceID}/documents/{documentID}/restore-previews", handler.previewRestore)
	router.Post("/workspaces/{workspaceID}/documents/{documentID}/restore-proposals", handler.createRestoreProposal)
}

type historyPageResponse struct {
	WorkspaceID     string `json:"workspace_id"`
	DocumentID      string `json:"document_id"`
	DocumentVersion int64  `json:"document_version"`
	Path            string `json:"path"`
	Branch          string `json:"branch"`
	Head            string `json:"head"`
	Dirty           bool   `json:"dirty"`
	Items           []any  `json:"items"`
	NextCursor      string `json:"next_cursor,omitempty"`
}

type historyEntryResponse struct {
	Kind          string   `json:"kind"`
	Commit        *string  `json:"commit"`
	ParentCommits []string `json:"parent_commits"`
	AuthorName    string   `json:"author_name"`
	AuthorEmail   string   `json:"author_email"`
	CommittedAt   *string  `json:"committed_at"`
	Summary       string   `json:"summary"`
}

type managedHistoryEntryResponse struct {
	historyEntryResponse
	ArticleRevisionID  *string `json:"article_revision_id"`
	ArticleRevisionNo  *int    `json:"article_revision_no"`
	ProposalID         *string `json:"proposal_id"`
	ProposalRevisionID *string `json:"proposal_revision_id"`
	ApprovalID         *string `json:"approval_id"`
	WorkflowRunID      *string `json:"workflow_run_id"`
	WritebackID        *string `json:"writeback_id"`
	ProposalType       *string `json:"proposal_type"`
	ApprovalDecidedAt  *string `json:"approval_decided_at"`
}

type compareResponse struct {
	WorkspaceID  string `json:"workspace_id"`
	DocumentID   string `json:"document_id"`
	Path         string `json:"path"`
	Head         string `json:"head"`
	Left         string `json:"left"`
	Right        string `json:"right"`
	LeftContent  string `json:"left_content"`
	RightContent string `json:"right_content"`
	Patch        string `json:"patch"`
	DiffHash     string `json:"diff_hash"`
}

type restorePreviewRequest struct {
	TargetCommit            string `json:"target_commit"`
	ExpectedDocumentVersion int64  `json:"expected_document_version"`
}

type restorePreviewResponse struct {
	WorkspaceID             string `json:"workspace_id"`
	DocumentID              string `json:"document_id"`
	Path                    string `json:"path"`
	TargetCommit            string `json:"target_commit"`
	ExpectedHead            string `json:"expected_head"`
	ExpectedDocumentVersion int64  `json:"expected_document_version"`
	CurrentContentHash      string `json:"current_content_hash"`
	TargetContentHash       string `json:"target_content_hash"`
	CurrentContent          string `json:"current_content"`
	TargetContent           string `json:"target_content"`
	Patch                   string `json:"patch"`
	DiffHash                string `json:"diff_hash"`
	PreviewHash             string `json:"preview_hash"`
	BlockedByDirtyWorktree  bool   `json:"blocked_by_dirty_worktree"`
}

type restoreProposalRequest struct {
	TargetCommit            string `json:"target_commit"`
	ExpectedHead            string `json:"expected_head"`
	ExpectedDocumentVersion int64  `json:"expected_document_version"`
	PreviewHash             string `json:"preview_hash"`
}

type restoreProposalResponse struct {
	ProposalID         string `json:"proposal_id"`
	ProposalRevisionID string `json:"proposal_revision_id"`
	ProposalType       string `json:"proposal_type"`
	Status             string `json:"status"`
	ChangeHash         string `json:"change_hash"`
	Replayed           bool   `json:"replayed"`
}

func (handler *Handler) listHistory(writer stdhttp.ResponseWriter, request *stdhttp.Request) {
	if !handler.requireService(writer) {
		return
	}
	workspaceID, documentID, ok := parseRouteIDs(writer, request)
	if !ok {
		return
	}
	if !onlyQueryParameters(request, "limit", "cursor") {
		writeError(writer, invalid("history query parameters are invalid"))
		return
	}
	limit := defaultHistoryLimit
	if value := request.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			writeError(writer, invalid("history limit is invalid"))
			return
		}
		limit = parsed
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	page, err := handler.service.ListHistory(ctx, application.HistoryQuery{
		WorkspaceID: workspaceID, DocumentID: documentID, Limit: limit, Cursor: request.URL.Query().Get("cursor"),
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	items := make([]any, len(page.Items))
	for index, item := range page.Items {
		items[index] = historyEntry(item)
	}
	writeJSON(writer, stdhttp.StatusOK, historyPageResponse{
		WorkspaceID: string(page.WorkspaceID), DocumentID: string(page.DocumentID), DocumentVersion: page.DocumentVersion,
		Path: page.Path, Branch: page.Branch, Head: page.Head, Dirty: page.Dirty, Items: items, NextCursor: page.NextCursor,
	})
}

func (handler *Handler) compare(writer stdhttp.ResponseWriter, request *stdhttp.Request) {
	if !handler.requireService(writer) {
		return
	}
	workspaceID, documentID, ok := parseRouteIDs(writer, request)
	if !ok {
		return
	}
	if !onlyQueryParameters(request, "left", "right") {
		writeError(writer, invalid("compare query parameters are invalid"))
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.CompareVersions(ctx, application.CompareQuery{
		WorkspaceID: workspaceID, DocumentID: documentID,
		Left: request.URL.Query().Get("left"), Right: request.URL.Query().Get("right"),
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, stdhttp.StatusOK, compareResponse{
		WorkspaceID: string(result.WorkspaceID), DocumentID: string(result.DocumentID), Path: result.Path, Head: result.Head,
		Left: result.Left, Right: result.Right, LeftContent: result.LeftContent, RightContent: result.RightContent,
		Patch: result.Patch, DiffHash: result.DiffHash,
	})
}

func (handler *Handler) previewRestore(writer stdhttp.ResponseWriter, request *stdhttp.Request) {
	if !handler.requireService(writer) {
		return
	}
	workspaceID, documentID, ok := parseRouteIDs(writer, request)
	if !ok {
		return
	}
	if !onlyQueryParameters(request) {
		writeError(writer, invalid("restore preview query parameters are invalid"))
		return
	}
	var input restorePreviewRequest
	if err := decodeRequest(request, &input); err != nil {
		writeError(writer, err)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	preview, err := handler.service.PreviewRestore(ctx, application.PreviewCommand{
		WorkspaceID: workspaceID, DocumentID: documentID, TargetCommit: input.TargetCommit,
		ExpectedDocumentVersion: input.ExpectedDocumentVersion,
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, stdhttp.StatusOK, restorePreviewResponse{
		WorkspaceID: string(preview.WorkspaceID), DocumentID: string(preview.DocumentID), Path: preview.Path,
		TargetCommit: preview.TargetCommit, ExpectedHead: preview.ExpectedHead, ExpectedDocumentVersion: preview.ExpectedDocumentVersion,
		CurrentContentHash: preview.CurrentContentHash, TargetContentHash: preview.TargetContentHash,
		CurrentContent: preview.CurrentContent, TargetContent: preview.TargetContent, Patch: preview.Patch,
		DiffHash: preview.DiffHash, PreviewHash: preview.PreviewHash, BlockedByDirtyWorktree: preview.BlockedByDirtyWorktree,
	})
}

func (handler *Handler) createRestoreProposal(writer stdhttp.ResponseWriter, request *stdhttp.Request) {
	if !handler.requireService(writer) {
		return
	}
	workspaceID, documentID, ok := parseRouteIDs(writer, request)
	if !ok {
		return
	}
	if !onlyQueryParameters(request) {
		writeError(writer, invalid("restore proposal query parameters are invalid"))
		return
	}
	idempotencyKeys := request.Header.Values("Idempotency-Key")
	if len(idempotencyKeys) != 1 {
		writeError(writer, invalid("Idempotency-Key must be provided exactly once"))
		return
	}
	var input restoreProposalRequest
	if err := decodeRequest(request, &input); err != nil {
		writeError(writer, err)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.CreateRestoreProposal(ctx, application.RestoreCommand{
		WorkspaceID: workspaceID, DocumentID: documentID, TargetCommit: input.TargetCommit,
		ExpectedHead: input.ExpectedHead, ExpectedDocumentVersion: input.ExpectedDocumentVersion,
		PreviewHash: input.PreviewHash, IdempotencyKey: idempotencyKeys[0],
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	status := stdhttp.StatusCreated
	if result.Replayed {
		status = stdhttp.StatusOK
	}
	writeJSON(writer, status, restoreProposalResponse{
		ProposalID: string(result.ProposalID), ProposalRevisionID: string(result.ProposalRevisionID),
		ProposalType: result.ProposalType, Status: result.Status, ChangeHash: result.ChangeHash, Replayed: result.Replayed,
	})
}

func (handler *Handler) requireService(writer stdhttp.ResponseWriter) bool {
	if handler != nil && handler.service != nil {
		return true
	}
	writeError(writer, foundation.NewError(
		foundation.ErrorDependencyUnavailable,
		errorUnavailable,
		true,
		errors.New("document history service is unavailable"),
	))
	return false
}

func historyEntry(item domain.Entry) any {
	response := historyEntryResponse{
		Kind: string(item.Kind), ParentCommits: append([]string(nil), item.ParentCommits...),
		AuthorName: item.AuthorName, AuthorEmail: item.AuthorEmail, Summary: item.Summary,
	}
	if item.Kind != domain.EntryCurrentChange {
		commit := item.Commit
		committedAt := item.CommittedAt.UTC().Format(time.RFC3339Nano)
		response.Commit, response.CommittedAt = &commit, &committedAt
	}
	if item.Kind != domain.EntryManaged {
		return response
	}
	managed := managedHistoryEntryResponse{historyEntryResponse: response}
	managed.ArticleRevisionID = optionalID(item.ArticleRevisionID)
	if item.ArticleRevisionNo > 0 {
		value := item.ArticleRevisionNo
		managed.ArticleRevisionNo = &value
	}
	managed.ProposalID = optionalID(item.ProposalID)
	managed.ProposalRevisionID = optionalID(item.ProposalRevisionID)
	managed.ApprovalID = optionalID(item.ApprovalID)
	managed.WorkflowRunID = optionalID(item.WorkflowRunID)
	managed.WritebackID = optionalID(item.WritebackID)
	if item.ProposalType != "" {
		value := item.ProposalType
		managed.ProposalType = &value
	}
	if item.ApprovalDecidedAt != nil {
		value := item.ApprovalDecidedAt.UTC().Format(time.RFC3339Nano)
		managed.ApprovalDecidedAt = &value
	}
	return managed
}

func parseRouteIDs(writer stdhttp.ResponseWriter, request *stdhttp.Request) (foundation.ID, foundation.ID, bool) {
	if request == nil {
		writeError(writer, invalid("request is nil"))
		return "", "", false
	}
	workspaceID, workspaceErr := foundation.ParseID(chi.URLParam(request, "workspaceID"))
	documentID, documentErr := foundation.ParseID(chi.URLParam(request, "documentID"))
	if workspaceErr != nil || documentErr != nil {
		writeError(writer, invalid("route identity is invalid"))
		return "", "", false
	}
	return workspaceID, documentID, true
}

func onlyQueryParameters(request *stdhttp.Request, allowed ...string) bool {
	set := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		set[key] = struct{}{}
	}
	for key, values := range request.URL.Query() {
		if _, ok := set[key]; !ok || len(values) != 1 {
			return false
		}
	}
	return true
}

func decodeRequest(request *stdhttp.Request, target any) error {
	contentTypes := request.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		return foundation.NewError(foundation.ErrorInvalidInput, errorUnsupportedType, false, errors.New("request must use exactly one application/json Content-Type"))
	}
	mediaType, _, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || mediaType != "application/json" {
		return foundation.NewError(foundation.ErrorInvalidInput, errorUnsupportedType, false, errors.New("request must use application/json"))
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxRequestBodyBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxRequestBodyBytes {
		return foundation.NewError(foundation.ErrorInvalidInput, errorInvalidJSON, false, errors.New("request body is invalid"))
	}
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxRequestBodyBytes
	limits.MaxStringBytes = 4096
	decoded, err := strictjson.DecodeObject[map[string]any](body, limits, nil)
	if err != nil || decoded == nil {
		return foundation.NewError(foundation.ErrorInvalidInput, errorInvalidJSON, false, errors.New("request body is invalid"))
	}
	// DecodeJSON applies the concrete schema after duplicate-key and depth checks above.
	request.Body = io.NopCloser(strings.NewReader(string(body)))
	if err := httpapi.DecodeJSON(request, target); err != nil {
		return foundation.NewError(foundation.ErrorInvalidInput, errorInvalidJSON, false, err)
	}
	return nil
}

func optionalID(value foundation.ID) *string {
	if value == "" {
		return nil
	}
	text := string(value)
	return &text
}

func invalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, application.ErrorCodeInvalid, false, errors.New(message))
}

func writeJSON(writer stdhttp.ResponseWriter, status int, value any) {
	writer.Header().Set("Cache-Control", "private, no-store")
	httpapi.WriteJSON(writer, status, value)
}

func writeError(writer stdhttp.ResponseWriter, err error) {
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		httpapi.WriteProblem(writer, stdhttp.StatusInternalServerError, "INTERNAL_ERROR", "文档历史请求失败", false, nil)
		return
	}
	status := httpapi.StatusForErrorKind(classified.Kind)
	if classified.Code == application.ErrorCodeOutputTooLarge {
		status = stdhttp.StatusRequestEntityTooLarge
	} else if classified.Code == errorUnsupportedType {
		status = stdhttp.StatusUnsupportedMediaType
	}
	message := "文档历史请求未完成"
	if classified.Code == application.ErrorCodeDirty {
		message = "工作区存在未提交改动，暂不能创建恢复提案"
	}
	writer.Header().Set("Cache-Control", "private, no-store")
	httpapi.WriteProblem(writer, status, classified.Code, message, classified.Retryable, nil)
}
