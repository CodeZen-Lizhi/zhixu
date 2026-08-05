// Package authoringhttp exposes the strict Workspace-scoped Authoring HTTP boundary.
package authoringhttp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	"github.com/go-chi/chi/v5"
)

const (
	defaultTimeout            = 5 * time.Second
	maxSmallRequestBodyBytes  = 4 * 1024
	maxUpdateRequestOverhead  = 32 * 1024
	maxUpdateRequestBodyBytes = domain.MaxBodyBytes*6 + maxUpdateRequestOverhead
	defaultListLimit          = 30
	maxListLimit              = 100
	maxCursorBytes            = 4096
	cursorVersion             = 1
	workingDraftCursorKind    = "authoring-working-drafts"
	documentDraftCursorKind   = "authoring-document-drafts"
	errorCodeUnavailable      = "AUTHORING_HTTP_UNAVAILABLE"
	errorCodeRequestInvalid   = "AUTHORING_REQUEST_INVALID"
	errorCodeInvalidJSON      = "INVALID_JSON"
	errorCodeUnsupportedMedia = "UNSUPPORTED_MEDIA_TYPE"
	errorCodeResultInvalid    = "AUTHORING_HTTP_RESULT_INCONSISTENT"
	errorCodeRequestTimeout   = "AUTHORING_REQUEST_TIMEOUT"
	errorCodeRequestCancelled = "AUTHORING_REQUEST_CANCELLED"
)

// Service is the complete Authoring application contract exposed to the HTTP composition root.
type Service interface {
	CreateWorkingDraft(context.Context, authoringapp.CreateCommand) (authoringapp.CreateResult, error)
	GetWorkingDraft(context.Context, foundation.ID, foundation.ID) (domain.WorkingDraft, error)
	UpdateWorkingDraft(context.Context, authoringapp.UpdateCommand) (authoringapp.UpdateResult, error)
	ListWorkingDrafts(context.Context, authoringapp.ListQuery) (authoringapp.Page, error)
	ListDocumentDrafts(context.Context, authoringapp.DocumentListQuery) (authoringapp.DocumentPage, error)
	FreezeWorkingDraft(context.Context, authoringapp.FreezeCommand) (authoringapp.FreezeResult, error)
	PublishArticleRevision(context.Context, authoringapp.PublishCommand) (authoringapp.PublishResult, error)
	GetDocumentDetail(context.Context, foundation.ID, foundation.ID) (authoringapp.DocumentDetail, error)
	GetOverview(context.Context, foundation.ID) (authoringapp.Overview, error)
}

// Handler maps strict JSON requests to the Authoring application service.
type Handler struct {
	service Service
	timeout time.Duration
}

// NewHandler creates a fail-closed Authoring handler.
func NewHandler(service Service, timeout time.Duration) *Handler {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Handler{service: service, timeout: timeout}
}

// Available reports whether the handler owns a real application service.
func (handler *Handler) Available() bool {
	return handler != nil && !nilDependency(handler.service)
}

// Routes registers Authoring commands and read models beneath /api/v1.
func (handler *Handler) Routes(router chi.Router) {
	router.Post("/workspaces/{workspaceID}/authoring/working-drafts", handler.createWorkingDraft)
	router.Get("/workspaces/{workspaceID}/authoring/working-drafts", handler.listWorkingDrafts)
	router.Get("/workspaces/{workspaceID}/authoring/working-drafts/{draftID}", handler.getWorkingDraft)
	router.Put("/workspaces/{workspaceID}/authoring/working-drafts/{draftID}", handler.updateWorkingDraft)
	router.Post("/workspaces/{workspaceID}/authoring/working-drafts/{draftID}/freeze", handler.freezeWorkingDraft)
	router.Post("/workspaces/{workspaceID}/documents/{documentID}/revisions/{revisionID}/publish-proposals", handler.publishArticleRevision)
	router.Get("/workspaces/{workspaceID}/documents/{documentID}", handler.getDocumentDetail)
	router.Get("/workspaces/{workspaceID}/authoring/documents", handler.listDocumentDrafts)
	router.Get("/workspaces/{workspaceID}/authoring/overview", handler.getOverview)
}

type emptyRequest struct{}

type updateRequest struct {
	ExpectedVersion int64  `json:"expected_version"`
	Title           string `json:"title"`
	TargetPath      string `json:"target_path"`
	Body            string `json:"body"`
}

type freezeRequest struct {
	ExpectedVersion int64 `json:"expected_version"`
}

type workingDraftResponse struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	DocumentID  *string `json:"document_id"`
	Title       string  `json:"title"`
	TargetPath  string  `json:"target_path"`
	Body        string  `json:"body"`
	Status      string  `json:"status"`
	Version     int64   `json:"version"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

type workingDraftSummaryResponse struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	DocumentID  *string `json:"document_id"`
	Title       string  `json:"title"`
	TargetPath  string  `json:"target_path"`
	Status      string  `json:"status"`
	Version     int64   `json:"version"`
	UpdatedAt   string  `json:"updated_at"`
}

type workingDraftPageResponse struct {
	WorkspaceID string                        `json:"workspace_id"`
	Items       []workingDraftSummaryResponse `json:"items"`
	NextCursor  string                        `json:"next_cursor,omitempty"`
}

type documentPageResponse struct {
	WorkspaceID string             `json:"workspace_id"`
	Items       []documentResponse `json:"items"`
	NextCursor  string             `json:"next_cursor,omitempty"`
}

type listCursor struct {
	Version     int    `json:"version"`
	Kind        string `json:"kind"`
	WorkspaceID string `json:"workspace_id"`
	Limit       int    `json:"limit"`
	Status      string `json:"status,omitempty"`
	UpdatedAt   string `json:"updated_at"`
	ID          string `json:"id"`
}

type listPosition struct {
	UpdatedAt time.Time
	ID        foundation.ID
}

type documentResponse struct {
	ID                         string  `json:"id"`
	WorkspaceID                string  `json:"workspace_id"`
	CanonicalPath              string  `json:"canonical_path"`
	Title                      string  `json:"title"`
	LifecycleStatus            string  `json:"lifecycle_status"`
	CurrentPublishedRevisionID *string `json:"current_published_revision_id"`
	Version                    int64   `json:"version"`
	CreatedAt                  string  `json:"created_at"`
	UpdatedAt                  string  `json:"updated_at"`
}

type articleRevisionResponse struct {
	ID               string  `json:"id"`
	WorkspaceID      string  `json:"workspace_id"`
	DocumentID       string  `json:"document_id"`
	SourceVersionID  *string `json:"source_version_id"`
	ParentRevisionID *string `json:"parent_revision_id"`
	RevisionNo       int     `json:"revision_no"`
	Content          string  `json:"content"`
	ContentHash      string  `json:"content_hash"`
	Status           string  `json:"status"`
	OptimizationMode string  `json:"optimization_mode"`
	GitCommit        *string `json:"git_commit"`
	CreatedByType    string  `json:"created_by_type"`
	CreatedAt        string  `json:"created_at"`
}

type publicationResponse struct {
	ID                 string  `json:"id"`
	WorkspaceID        string  `json:"workspace_id"`
	DocumentID         string  `json:"document_id"`
	ArticleRevisionID  string  `json:"article_revision_id"`
	ProposalID         string  `json:"proposal_id"`
	ProposalRevisionID string  `json:"proposal_revision_id"`
	TargetPath         string  `json:"target_path"`
	ContentHash        string  `json:"content_hash"`
	Status             string  `json:"status"`
	GitCommit          *string `json:"git_commit"`
	ErrorCode          *string `json:"error_code"`
	Version            int64   `json:"version"`
	CreatedAt          string  `json:"created_at"`
	UpdatedAt          string  `json:"updated_at"`
	PublishedAt        *string `json:"published_at"`
	ProposalHref       string  `json:"proposal_href"`
}

type workingDraftCommandResponse struct {
	WorkingDraft workingDraftResponse `json:"working_draft"`
	Replayed     bool                 `json:"replayed"`
}

type freezeResponse struct {
	WorkingDraft    workingDraftResponse    `json:"working_draft"`
	Document        documentResponse        `json:"document"`
	ArticleRevision articleRevisionResponse `json:"article_revision"`
	Replayed        bool                    `json:"replayed"`
}

type publishResponse struct {
	Publication publicationResponse `json:"publication"`
	Replayed    bool                `json:"replayed"`
}

type documentDetailResponse struct {
	Document        documentResponse         `json:"document"`
	CurrentRevision *articleRevisionResponse `json:"current_revision"`
	Publication     *publicationResponse     `json:"publication"`
}

type organizingResponse struct {
	Available bool    `json:"available"`
	Reason    *string `json:"reason"`
	Href      *string `json:"href"`
}

type overviewResponse struct {
	WorkspaceID         string                        `json:"workspace_id"`
	Organizing          organizingResponse            `json:"organizing"`
	RecentDrafts        []workingDraftSummaryResponse `json:"recent_drafts"`
	PendingPublications []publicationResponse         `json:"pending_publications"`
	CompletedDocuments  []documentResponse            `json:"completed_documents"`
}

func (handler *Handler) createWorkingDraft(writer http.ResponseWriter, request *http.Request) {
	workspaceID, key, ok := commandRoute(writer, request, "workspaceID")
	if !ok {
		return
	}
	if _, err := decodeJSON[emptyRequest](request, maxSmallRequestBodyBytes, maxSmallRequestBodyBytes); err != nil {
		writeError(writer, err)
		return
	}
	if !handler.available(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.CreateWorkingDraft(ctx, authoringapp.CreateCommand{WorkspaceID: workspaceID, IdempotencyKey: key})
	if err != nil {
		writeError(writer, err)
		return
	}
	response, err := toWorkingDraftCommandResponse(result.Draft, result.Replayed, workspaceID, "")
	if err != nil {
		writeError(writer, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(writer, status, response)
}

func (handler *Handler) getWorkingDraft(writer http.ResponseWriter, request *http.Request) {
	workspaceID, draftID, ok := queryRoute(writer, request, "workspaceID", "draftID")
	if !ok {
		return
	}
	if !handler.available(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	draft, err := handler.service.GetWorkingDraft(ctx, workspaceID, draftID)
	if err != nil {
		writeError(writer, err)
		return
	}
	response, err := toWorkingDraftResponse(draft, workspaceID, draftID)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) listWorkingDrafts(writer http.ResponseWriter, request *http.Request) {
	workspaceID, query, err := parseWorkingDraftListRequest(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	if !handler.available(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	page, err := handler.service.ListWorkingDrafts(ctx, query)
	if err != nil {
		writeError(writer, err)
		return
	}
	response := workingDraftPageResponse{WorkspaceID: string(workspaceID), Items: make([]workingDraftSummaryResponse, 0, len(page.Items))}
	for _, draft := range page.Items {
		item, mapErr := toWorkingDraftSummaryResponse(authoringapp.WorkingDraftSummary{
			ID: draft.ID, WorkspaceID: draft.WorkspaceID, DocumentID: draft.DocumentID, Title: draft.Title,
			TargetPath: draft.TargetPath, Status: draft.Status, Version: draft.Version, UpdatedAt: draft.UpdatedAt,
		}, workspaceID)
		if mapErr != nil {
			writeError(writer, mapErr)
			return
		}
		response.Items = append(response.Items, item)
	}
	if page.Next != nil {
		response.NextCursor, err = encodeListCursor(workingDraftCursorKind, workspaceID, query.Limit, string(query.Status), page.Next.UpdatedAt, page.Next.ID)
		if err != nil {
			writeError(writer, err)
			return
		}
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) listDocumentDrafts(writer http.ResponseWriter, request *http.Request) {
	workspaceID, query, err := parseDocumentDraftListRequest(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	if !handler.available(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	page, err := handler.service.ListDocumentDrafts(ctx, query)
	if err != nil {
		writeError(writer, err)
		return
	}
	response := documentPageResponse{WorkspaceID: string(workspaceID), Items: make([]documentResponse, 0, len(page.Items))}
	for _, document := range page.Items {
		item, mapErr := toDocumentResponse(document, workspaceID, "")
		if mapErr != nil || document.Lifecycle != domain.DocumentDraft {
			if mapErr == nil {
				mapErr = resultInvalid("application returned a non-draft document page")
			}
			writeError(writer, mapErr)
			return
		}
		response.Items = append(response.Items, item)
	}
	if page.Next != nil {
		response.NextCursor, err = encodeListCursor(documentDraftCursorKind, workspaceID, query.Limit, "", page.Next.UpdatedAt, page.Next.ID)
		if err != nil {
			writeError(writer, err)
			return
		}
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) updateWorkingDraft(writer http.ResponseWriter, request *http.Request) {
	workspaceID, draftID, key, ok := identifiedCommandRoute(writer, request, "draftID")
	if !ok {
		return
	}
	wire, err := decodeJSON[updateRequest](request, maxUpdateRequestBodyBytes, domain.MaxBodyBytes)
	if err != nil {
		writeError(writer, err)
		return
	}
	if wire.ExpectedVersion < 1 {
		writeError(writer, requestInvalid("expected_version must be positive"))
		return
	}
	if !handler.available(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.UpdateWorkingDraft(ctx, authoringapp.UpdateCommand{
		WorkspaceID: workspaceID, DraftID: draftID, ExpectedVersion: wire.ExpectedVersion,
		Title: wire.Title, TargetPath: wire.TargetPath, Body: wire.Body, IdempotencyKey: key,
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	response, err := toWorkingDraftCommandResponse(result.Draft, result.Replayed, workspaceID, draftID)
	if err != nil || result.Draft.Version != wire.ExpectedVersion+1 {
		if err == nil {
			err = resultInvalid("application returned an invalid autosave version")
		}
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) freezeWorkingDraft(writer http.ResponseWriter, request *http.Request) {
	workspaceID, draftID, key, ok := identifiedCommandRoute(writer, request, "draftID")
	if !ok {
		return
	}
	wire, err := decodeJSON[freezeRequest](request, maxSmallRequestBodyBytes, maxSmallRequestBodyBytes)
	if err != nil {
		writeError(writer, err)
		return
	}
	if wire.ExpectedVersion < 1 {
		writeError(writer, requestInvalid("expected_version must be positive"))
		return
	}
	if !handler.available(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.FreezeWorkingDraft(ctx, authoringapp.FreezeCommand{
		WorkspaceID: workspaceID, DraftID: draftID, ExpectedVersion: wire.ExpectedVersion, IdempotencyKey: key,
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	response, err := toFreezeResponse(result, workspaceID, draftID, wire.ExpectedVersion)
	if err != nil {
		writeError(writer, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(writer, status, response)
}

func (handler *Handler) publishArticleRevision(writer http.ResponseWriter, request *http.Request) {
	if err := rejectQuery(request); err != nil {
		writeError(writer, err)
		return
	}
	workspaceID, err := parseID(chi.URLParam(request, "workspaceID"))
	if err != nil {
		writeError(writer, err)
		return
	}
	documentID, err := parseID(chi.URLParam(request, "documentID"))
	if err != nil {
		writeError(writer, err)
		return
	}
	revisionID, err := parseID(chi.URLParam(request, "revisionID"))
	if err != nil {
		writeError(writer, err)
		return
	}
	key, err := parseIdempotencyKey(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	if _, err = decodeJSON[emptyRequest](request, maxSmallRequestBodyBytes, maxSmallRequestBodyBytes); err != nil {
		writeError(writer, err)
		return
	}
	if !handler.available(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.PublishArticleRevision(ctx, authoringapp.PublishCommand{
		WorkspaceID: workspaceID, DocumentID: documentID, RevisionID: revisionID, IdempotencyKey: key,
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	response, err := toPublishResponse(result, workspaceID, documentID, revisionID)
	if err != nil {
		writeError(writer, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(writer, status, response)
}

func (handler *Handler) getDocumentDetail(writer http.ResponseWriter, request *http.Request) {
	workspaceID, documentID, ok := queryRoute(writer, request, "workspaceID", "documentID")
	if !ok {
		return
	}
	if !handler.available(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	detail, err := handler.service.GetDocumentDetail(ctx, workspaceID, documentID)
	if err != nil {
		writeError(writer, err)
		return
	}
	response, err := toDocumentDetailResponse(detail, workspaceID, documentID)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) getOverview(writer http.ResponseWriter, request *http.Request) {
	if err := rejectQuery(request); err != nil {
		writeError(writer, err)
		return
	}
	workspaceID, err := parseID(chi.URLParam(request, "workspaceID"))
	if err != nil {
		writeError(writer, err)
		return
	}
	if !handler.available(writer) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	overview, err := handler.service.GetOverview(ctx, workspaceID)
	if err != nil {
		writeError(writer, err)
		return
	}
	response, err := toOverviewResponse(overview, workspaceID)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func commandRoute(writer http.ResponseWriter, request *http.Request, workspaceParam string) (foundation.ID, string, bool) {
	if err := rejectQuery(request); err != nil {
		writeError(writer, err)
		return "", "", false
	}
	workspaceID, err := parseID(chi.URLParam(request, workspaceParam))
	if err != nil {
		writeError(writer, err)
		return "", "", false
	}
	key, err := parseIdempotencyKey(request)
	if err != nil {
		writeError(writer, err)
		return "", "", false
	}
	return workspaceID, key, true
}

func identifiedCommandRoute(writer http.ResponseWriter, request *http.Request, resourceParam string) (foundation.ID, foundation.ID, string, bool) {
	workspaceID, key, ok := commandRoute(writer, request, "workspaceID")
	if !ok {
		return "", "", "", false
	}
	resourceID, err := parseID(chi.URLParam(request, resourceParam))
	if err != nil {
		writeError(writer, err)
		return "", "", "", false
	}
	return workspaceID, resourceID, key, true
}

func queryRoute(writer http.ResponseWriter, request *http.Request, workspaceParam, resourceParam string) (foundation.ID, foundation.ID, bool) {
	if err := rejectQuery(request); err != nil {
		writeError(writer, err)
		return "", "", false
	}
	workspaceID, err := parseID(chi.URLParam(request, workspaceParam))
	if err != nil {
		writeError(writer, err)
		return "", "", false
	}
	resourceID, err := parseID(chi.URLParam(request, resourceParam))
	if err != nil {
		writeError(writer, err)
		return "", "", false
	}
	return workspaceID, resourceID, true
}

func decodeJSON[T any](request *http.Request, maxDocumentBytes, maxStringBytes int) (T, error) {
	var zero T
	contentTypes := request.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		return zero, unsupportedMedia()
	}
	mediaType, _, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return zero, unsupportedMedia()
	}
	if request.ContentLength > int64(maxDocumentBytes) {
		return zero, invalidJSON("request JSON is oversized")
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, int64(maxDocumentBytes)+1))
	if err != nil || len(body) == 0 || len(body) > maxDocumentBytes {
		return zero, invalidJSON("request JSON is empty, unreadable, or oversized")
	}
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxDocumentBytes
	limits.MaxStringBytes = maxStringBytes
	limits.MaxDepth = 4
	limits.MaxArrayItems = 0
	limits.MaxObjectFields = 8
	value, err := strictjson.DecodeObject[T](body, limits, nil)
	if err != nil {
		return zero, invalidJSON("request JSON does not match the Authoring contract")
	}
	return value, nil
}

func parseIdempotencyKey(request *http.Request) (string, error) {
	values := request.Header.Values("Idempotency-Key")
	if len(values) != 1 || !utf8.ValidString(values[0]) {
		return "", foundation.NewError(foundation.ErrorInvalidInput, authoringapp.ErrorCodeIdempotencyKeyInvalid, false, errors.New("exactly one Idempotency-Key is required"))
	}
	key := values[0]
	if key == "" || key != strings.TrimSpace(key) || len(key) > authoringapp.MaxIdempotencyKeyBytes {
		return "", foundation.NewError(foundation.ErrorInvalidInput, authoringapp.ErrorCodeIdempotencyKeyInvalid, false, errors.New("Idempotency-Key is invalid"))
	}
	for _, character := range key {
		if unicode.IsControl(character) {
			return "", foundation.NewError(foundation.ErrorInvalidInput, authoringapp.ErrorCodeIdempotencyKeyInvalid, false, errors.New("Idempotency-Key is invalid"))
		}
	}
	return key, nil
}

func parseID(value string) (foundation.ID, error) {
	id, err := foundation.ParseID(value)
	if err != nil {
		return "", err
	}
	return id, nil
}

func rejectQuery(request *http.Request) error {
	if request.URL.RawQuery != "" {
		return requestInvalid("query parameters are not allowed")
	}
	return nil
}

func parseWorkingDraftListRequest(request *http.Request) (foundation.ID, authoringapp.ListQuery, error) {
	workspaceID, values, limit, err := parseAuthoringListRequest(request)
	if err != nil {
		return "", authoringapp.ListQuery{}, err
	}
	status := domain.WorkingDraftStatus(values.Get("status"))
	if status != "" && !status.Valid() {
		return "", authoringapp.ListQuery{}, requestInvalid("working draft list status is invalid")
	}
	cursor, err := decodeListCursor(values.Get("cursor"), workingDraftCursorKind, workspaceID, limit, string(status))
	if err != nil {
		return "", authoringapp.ListQuery{}, err
	}
	query := authoringapp.ListQuery{WorkspaceID: workspaceID, Status: status, Limit: limit}
	if cursor != nil {
		query.After = &authoringapp.Cursor{UpdatedAt: cursor.UpdatedAt, ID: cursor.ID}
	}
	return workspaceID, query, nil
}

func parseDocumentDraftListRequest(request *http.Request) (foundation.ID, authoringapp.DocumentListQuery, error) {
	workspaceID, values, limit, err := parseAuthoringListRequest(request)
	if err != nil {
		return "", authoringapp.DocumentListQuery{}, err
	}
	if values.Get("status") != "" {
		return "", authoringapp.DocumentListQuery{}, requestInvalid("document draft list status is not supported")
	}
	cursor, err := decodeListCursor(values.Get("cursor"), documentDraftCursorKind, workspaceID, limit, "")
	if err != nil {
		return "", authoringapp.DocumentListQuery{}, err
	}
	query := authoringapp.DocumentListQuery{WorkspaceID: workspaceID, Limit: limit}
	if cursor != nil {
		query.After = &authoringapp.DocumentCursor{UpdatedAt: cursor.UpdatedAt, ID: cursor.ID}
	}
	return workspaceID, query, nil
}

func parseAuthoringListRequest(request *http.Request) (foundation.ID, url.Values, int, error) {
	workspaceID, err := parseID(chi.URLParam(request, "workspaceID"))
	if err != nil {
		return "", nil, 0, err
	}
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return "", nil, 0, requestInvalid("authoring list query is invalid")
	}
	allowed := map[string]struct{}{"status": {}, "limit": {}, "cursor": {}}
	for key, entries := range values {
		if _, ok := allowed[key]; !ok || len(entries) != 1 || entries[0] == "" || entries[0] != strings.TrimSpace(entries[0]) {
			return "", nil, 0, requestInvalid("authoring list query parameter is invalid")
		}
	}
	limit := defaultListLimit
	if raw := values.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > maxListLimit {
			return "", nil, 0, requestInvalid("authoring list limit is invalid")
		}
	}
	return workspaceID, values, limit, nil
}

func encodeListCursor(kind string, workspaceID foundation.ID, limit int, status string, updatedAt time.Time, id foundation.ID) (string, error) {
	if updatedAt.IsZero() || !validResponseID(id) {
		return "", resultInvalid("application returned an invalid authoring list cursor")
	}
	payload, err := json.Marshal(listCursor{
		Version: cursorVersion, Kind: kind, WorkspaceID: string(workspaceID), Limit: limit, Status: status,
		UpdatedAt: updatedAt.UTC().Format(time.RFC3339Nano), ID: string(id),
	})
	if err != nil {
		return "", resultInvalid("authoring list cursor encoding failed")
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeListCursor(raw, kind string, workspaceID foundation.ID, limit int, status string) (*listPosition, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > maxCursorBytes || raw != strings.TrimSpace(raw) {
		return nil, requestInvalid("authoring list cursor is invalid")
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, requestInvalid("authoring list cursor is invalid")
	}
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxCursorBytes
	cursor, err := strictjson.DecodeObject[listCursor](payload, limits, nil)
	if err != nil || cursor.Version != cursorVersion || cursor.Kind != kind || cursor.WorkspaceID != string(workspaceID) ||
		cursor.Limit != limit || cursor.Status != status {
		return nil, requestInvalid("authoring list cursor binding is invalid")
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, cursor.UpdatedAt)
	if err != nil {
		return nil, requestInvalid("authoring list cursor time is invalid")
	}
	id, err := parseID(cursor.ID)
	if err != nil {
		return nil, requestInvalid("authoring list cursor id is invalid")
	}
	return &listPosition{UpdatedAt: updatedAt.UTC(), ID: id}, nil
}

func toWorkingDraftCommandResponse(draft domain.WorkingDraft, replayed bool, workspaceID, draftID foundation.ID) (workingDraftCommandResponse, error) {
	response, err := toWorkingDraftResponse(draft, workspaceID, draftID)
	return workingDraftCommandResponse{WorkingDraft: response, Replayed: replayed}, err
}

func toWorkingDraftResponse(draft domain.WorkingDraft, workspaceID, draftID foundation.ID) (workingDraftResponse, error) {
	if err := draft.Validate(); err != nil || draft.WorkspaceID != workspaceID || (draftID != "" && draft.ID != draftID) {
		return workingDraftResponse{}, resultInvalid("application returned an invalid Working Draft binding")
	}
	return workingDraftResponse{
		ID: string(draft.ID), WorkspaceID: string(draft.WorkspaceID), DocumentID: optionalID(draft.DocumentID),
		Title: draft.Title, TargetPath: draft.TargetPath, Body: draft.Body, Status: string(draft.Status),
		Version: draft.Version, CreatedAt: formatTime(draft.CreatedAt), UpdatedAt: formatTime(draft.UpdatedAt),
	}, nil
}

func toWorkingDraftSummaryResponse(draft authoringapp.WorkingDraftSummary, workspaceID foundation.ID) (workingDraftSummaryResponse, error) {
	if !validResponseID(draft.ID) || draft.WorkspaceID != workspaceID ||
		(draft.DocumentID != "" && !validResponseID(draft.DocumentID)) ||
		!utf8.ValidString(draft.Title) || len(draft.Title) > domain.MaxTitleBytes || strings.ContainsAny(draft.Title, "\r\n\x00") ||
		!utf8.ValidString(draft.TargetPath) || len(draft.TargetPath) > domain.MaxTargetPathBytes || strings.ContainsAny(draft.TargetPath, "\r\n\x00") ||
		!draft.Status.Valid() || draft.Version < 1 || draft.UpdatedAt.IsZero() {
		return workingDraftSummaryResponse{}, resultInvalid("application returned an invalid Working Draft summary")
	}
	return workingDraftSummaryResponse{
		ID: string(draft.ID), WorkspaceID: string(draft.WorkspaceID), DocumentID: optionalID(draft.DocumentID),
		Title: draft.Title, TargetPath: draft.TargetPath, Status: string(draft.Status), Version: draft.Version,
		UpdatedAt: formatTime(draft.UpdatedAt),
	}, nil
}

func toDocumentResponse(document domain.Document, workspaceID, documentID foundation.ID) (documentResponse, error) {
	if err := document.Validate(); err != nil || document.WorkspaceID != workspaceID || (documentID != "" && document.ID != documentID) {
		return documentResponse{}, resultInvalid("application returned an invalid Document binding")
	}
	return documentResponse{
		ID: string(document.ID), WorkspaceID: string(document.WorkspaceID), CanonicalPath: document.CanonicalPath,
		Title: document.Title, LifecycleStatus: string(document.Lifecycle),
		CurrentPublishedRevisionID: optionalID(document.CurrentPublishedRevisionID), Version: document.Version,
		CreatedAt: formatTime(document.CreatedAt), UpdatedAt: formatTime(document.UpdatedAt),
	}, nil
}

func toArticleRevisionResponse(revision domain.ArticleRevision, workspaceID, documentID, revisionID foundation.ID) (articleRevisionResponse, error) {
	if err := revision.Validate(); err != nil || revision.WorkspaceID != workspaceID || revision.DocumentID != documentID ||
		(revisionID != "" && revision.ID != revisionID) ||
		(revision.Status == domain.RevisionPublished && revision.GitCommit == "") ||
		((revision.Status == domain.RevisionDraft || revision.Status == domain.RevisionReview || revision.Status == domain.RevisionApproved) && revision.GitCommit != "") {
		return articleRevisionResponse{}, resultInvalid("application returned an invalid Article Revision binding")
	}
	return articleRevisionResponse{
		ID: string(revision.ID), WorkspaceID: string(revision.WorkspaceID), DocumentID: string(revision.DocumentID),
		SourceVersionID: optionalID(revision.SourceVersionID), ParentRevisionID: optionalID(revision.ParentRevisionID),
		RevisionNo: revision.RevisionNo, Content: revision.Content, ContentHash: revision.ContentHash,
		Status: string(revision.Status), OptimizationMode: revision.OptimizationMode, GitCommit: optionalString(revision.GitCommit),
		CreatedByType: revision.CreatedByType, CreatedAt: formatTime(revision.CreatedAt),
	}, nil
}

func toPublicationResponse(publication domain.PublicationBinding, workspaceID, documentID, revisionID foundation.ID) (publicationResponse, error) {
	if err := publication.Validate(); err != nil || publication.WorkspaceID != workspaceID ||
		(documentID != "" && publication.DocumentID != documentID) ||
		(revisionID != "" && publication.ArticleRevisionID != revisionID) {
		return publicationResponse{}, resultInvalid("application returned an invalid Publication binding")
	}
	return publicationResponse{
		ID: string(publication.ID), WorkspaceID: string(publication.WorkspaceID), DocumentID: string(publication.DocumentID),
		ArticleRevisionID: string(publication.ArticleRevisionID), ProposalID: string(publication.ProposalID),
		ProposalRevisionID: string(publication.ProposalRevisionID), TargetPath: publication.TargetPath,
		ContentHash: publication.ContentHash, Status: string(publication.Status), GitCommit: optionalString(publication.GitCommit),
		ErrorCode: optionalString(publication.ErrorCode), Version: publication.Version,
		CreatedAt: formatTime(publication.CreatedAt), UpdatedAt: formatTime(publication.UpdatedAt),
		PublishedAt: optionalTime(publication.PublishedAt), ProposalHref: "/proposals/" + string(publication.ProposalID),
	}, nil
}

func toFreezeResponse(result authoringapp.FreezeResult, workspaceID, draftID foundation.ID, expectedVersion int64) (freezeResponse, error) {
	draft, err := toWorkingDraftResponse(result.Draft, workspaceID, draftID)
	if err != nil || result.Draft.Version != expectedVersion+1 {
		if err == nil {
			err = resultInvalid("application returned an invalid Freeze Working Draft version")
		}
		return freezeResponse{}, err
	}
	document, err := toDocumentResponse(result.Document, workspaceID, result.Draft.DocumentID)
	if err != nil || result.Draft.DocumentID == "" {
		return freezeResponse{}, resultInvalid("application returned an invalid Freeze Document binding")
	}
	revision, err := toArticleRevisionResponse(result.Revision, workspaceID, result.Document.ID, "")
	if err != nil || result.Revision.Content != result.Draft.Body {
		return freezeResponse{}, resultInvalid("application returned an invalid Freeze Revision binding")
	}
	return freezeResponse{WorkingDraft: draft, Document: document, ArticleRevision: revision, Replayed: result.Replayed}, nil
}

func toPublishResponse(result authoringapp.PublishResult, workspaceID, documentID, revisionID foundation.ID) (publishResponse, error) {
	publication, err := toPublicationResponse(result.Publication, workspaceID, documentID, revisionID)
	return publishResponse{Publication: publication, Replayed: result.Replayed}, err
}

func toDocumentDetailResponse(detail authoringapp.DocumentDetail, workspaceID, documentID foundation.ID) (documentDetailResponse, error) {
	document, err := toDocumentResponse(detail.Document, workspaceID, documentID)
	if err != nil {
		return documentDetailResponse{}, err
	}
	response := documentDetailResponse{Document: document}
	if detail.CurrentRevision != nil {
		revision, mapErr := toArticleRevisionResponse(*detail.CurrentRevision, workspaceID, documentID, "")
		if mapErr != nil {
			return documentDetailResponse{}, mapErr
		}
		response.CurrentRevision = &revision
	}
	if detail.Publication != nil {
		publication, mapErr := toPublicationResponse(*detail.Publication, workspaceID, documentID, "")
		if mapErr != nil {
			return documentDetailResponse{}, mapErr
		}
		response.Publication = &publication
	}
	return response, nil
}

func toOverviewResponse(overview authoringapp.Overview, workspaceID foundation.ID) (overviewResponse, error) {
	if overview.WorkspaceID != workspaceID || len(overview.RecentDrafts) > 100 ||
		len(overview.PendingPublications) > 100 || len(overview.CompletedDocuments) > 100 {
		return overviewResponse{}, resultInvalid("application returned an invalid Authoring overview")
	}
	if overview.Organizing.Available {
		if overview.Organizing.Href != "/authoring/organize" {
			return overviewResponse{}, resultInvalid("application returned an invalid Organizing availability")
		}
	} else if overview.Organizing.Reason == "" || overview.Organizing.Href != "" {
		return overviewResponse{}, resultInvalid("application returned an invalid Organizing availability")
	}
	response := overviewResponse{
		WorkspaceID:         string(workspaceID),
		Organizing:          organizingResponse{Available: overview.Organizing.Available, Reason: optionalString(overview.Organizing.Reason), Href: optionalString(overview.Organizing.Href)},
		RecentDrafts:        make([]workingDraftSummaryResponse, 0, len(overview.RecentDrafts)),
		PendingPublications: make([]publicationResponse, 0, len(overview.PendingPublications)),
		CompletedDocuments:  make([]documentResponse, 0, len(overview.CompletedDocuments)),
	}
	draftIDs := make(map[foundation.ID]struct{}, len(overview.RecentDrafts))
	for _, draft := range overview.RecentDrafts {
		if _, duplicate := draftIDs[draft.ID]; duplicate {
			return overviewResponse{}, resultInvalid("overview contains duplicate Working Draft identities")
		}
		draftIDs[draft.ID] = struct{}{}
		item, err := toWorkingDraftSummaryResponse(draft, workspaceID)
		if err != nil {
			return overviewResponse{}, err
		}
		response.RecentDrafts = append(response.RecentDrafts, item)
	}
	publicationIDs := make(map[foundation.ID]struct{}, len(overview.PendingPublications))
	for _, publication := range overview.PendingPublications {
		if _, duplicate := publicationIDs[publication.ID]; duplicate {
			return overviewResponse{}, resultInvalid("overview contains duplicate Publication identities")
		}
		publicationIDs[publication.ID] = struct{}{}
		if publication.Status != domain.PublicationPending && publication.Status != domain.PublicationRecoveryRequired {
			return overviewResponse{}, resultInvalid("overview contains a non-pending Publication in the pending list")
		}
		item, err := toPublicationResponse(publication, workspaceID, "", "")
		if err != nil {
			return overviewResponse{}, err
		}
		response.PendingPublications = append(response.PendingPublications, item)
	}
	documentIDs := make(map[foundation.ID]struct{}, len(overview.CompletedDocuments))
	for _, document := range overview.CompletedDocuments {
		if _, duplicate := documentIDs[document.ID]; duplicate {
			return overviewResponse{}, resultInvalid("overview contains duplicate Document identities")
		}
		documentIDs[document.ID] = struct{}{}
		if document.Lifecycle != domain.DocumentPublished {
			return overviewResponse{}, resultInvalid("overview contains an incomplete Document in the completed list")
		}
		item, err := toDocumentResponse(document, workspaceID, "")
		if err != nil {
			return overviewResponse{}, err
		}
		response.CompletedDocuments = append(response.CompletedDocuments, item)
	}
	return response, nil
}

func optionalID(value foundation.ID) *string {
	if value == "" {
		return nil
	}
	stringValue := string(value)
	return &stringValue
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func optionalTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := formatTime(*value)
	return &formatted
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func validResponseID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func requestInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, errorCodeRequestInvalid, false, errors.New(message))
}

func invalidJSON(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, errorCodeInvalidJSON, false, errors.New(message))
}

func unsupportedMedia() error {
	return foundation.NewError(foundation.ErrorInvalidInput, errorCodeUnsupportedMedia, false, errors.New("request requires application/json"))
}

func resultInvalid(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, errorCodeResultInvalid, false, errors.New(message))
}

func nilDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func (handler *Handler) available(writer http.ResponseWriter) bool {
	if !handler.Available() {
		writeProblem(writer, http.StatusServiceUnavailable, errorCodeUnavailable, "创作服务暂不可用", true)
		return false
	}
	return true
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Cache-Control", "private, no-store")
	httpapi.WriteJSON(writer, status, value)
}

func writeProblem(writer http.ResponseWriter, status int, code, message string, retryable bool) {
	writer.Header().Set("Cache-Control", "private, no-store")
	httpapi.WriteProblem(writer, status, code, message, retryable, nil)
}

func writeError(writer http.ResponseWriter, err error) {
	if errors.Is(err, context.DeadlineExceeded) {
		writeProblem(writer, http.StatusServiceUnavailable, errorCodeRequestTimeout, "创作请求超时", true)
		return
	}
	if errors.Is(err, context.Canceled) {
		writeProblem(writer, http.StatusServiceUnavailable, errorCodeRequestCancelled, "创作请求已取消", false)
		return
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		writeProblem(writer, http.StatusInternalServerError, "INTERNAL_ERROR", "创作请求失败", false)
		return
	}
	status := httpapi.StatusForErrorKind(classified.Kind)
	message := "创作请求未完成"
	switch classified.Code {
	case errorCodeUnsupportedMedia:
		status, message = http.StatusUnsupportedMediaType, "请求必须使用 application/json"
	case errorCodeInvalidJSON, errorCodeRequestInvalid, authoringapp.ErrorCodeIdempotencyKeyInvalid,
		domain.ErrorCodeDraftInvalid, domain.ErrorCodeTargetPathInvalid, domain.ErrorCodeFreezeInvalid,
		domain.ErrorCodePublicationInvalid, "INVALID_ID":
		status, message = http.StatusBadRequest, "创作请求参数无效"
	case authoringapp.ErrorCodeNotFound:
		status, message = http.StatusNotFound, "请求的创作资源不存在"
	case authoringapp.ErrorCodeIdempotencyConflict, authoringapp.ErrorCodePathConflict, domain.ErrorCodeVersionConflict:
		status, message = http.StatusConflict, "创作版本或状态冲突"
	case errorCodeResultInvalid, authoringapp.ErrorCodeResultInvalid:
		status, message = http.StatusInternalServerError, "创作响应校验失败"
	case authoringapp.ErrorCodeRepositoryUnavailable:
		status, message = http.StatusServiceUnavailable, "创作依赖暂不可用"
	}
	if classified.Kind == foundation.ErrorNonRetryableFailure && status == http.StatusInternalServerError {
		status = http.StatusUnprocessableEntity
	}
	writeProblem(writer, status, classified.Code, message, classified.Retryable)
}

var _ Service = (*authoringapp.Service)(nil)
