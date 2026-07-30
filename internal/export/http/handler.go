// Package http 提供异步导出的严格 REST 边界。
package http

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	auditdomain "github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	authdomain "github.com/CodeZen-Lizhi/zhixu/internal/auth/domain"
	authhttp "github.com/CodeZen-Lizhi/zhixu/internal/auth/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	exportapp "github.com/CodeZen-Lizhi/zhixu/internal/export/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/export/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	"github.com/go-chi/chi/v5"
)

const (
	maxRequestBodyBytes = 64 * 1024
	maxIdempotencyBytes = 128
	maxCursorBytes      = 4096
	requestTimeout      = 5 * time.Second
)

// Service 是 Export HTTP 边界所需的应用能力。
type Service interface {
	Create(context.Context, domain.CreateRequest) (exportapp.CreateResult, error)
	GetForScope(context.Context, foundation.ID, foundation.ID, domain.ScopeKind) (domain.Job, error)
	List(context.Context, exportapp.ListQuery) (exportapp.ListPage, error)
	DownloadAsForScope(context.Context, foundation.ID, foundation.ID, domain.ScopeKind, exportapp.DownloadActor) (domain.Job, io.ReadCloser, error)
}

// Handler 将 Export application 映射为 Workspace-scoped REST 资源。
type Handler struct {
	service Service
	timeout time.Duration
}

// NewHandler 创建导出 HTTP Handler；nil Service 时路由明确返回 503。
func NewHandler(service Service) *Handler {
	return &Handler{service: service, timeout: requestTimeout}
}

// Available 报告导出服务是否已完成组装。
func (handler *Handler) Available() bool { return handler != nil && handler.service != nil }

// Routes 在 /api/v1 下注册导出创建、查询、列表和下载端点。
func (handler *Handler) Routes(router chi.Router) {
	router.Post("/exports", handler.create)
	router.Get("/exports/{export_id}", handler.get)
	router.Get("/exports/{export_id}/download", handler.download)
	router.Get("/workspaces/{workspace_id}/exports", handler.list)
	router.Post("/workspaces/{workspace_id}/attachment-exports", handler.createAttachment)
	router.Get("/workspaces/{workspace_id}/attachment-exports", handler.listAttachments)
	router.Get("/workspaces/{workspace_id}/attachment-exports/{export_id}", handler.getAttachment)
	router.Get("/workspaces/{workspace_id}/attachment-exports/{export_id}/download", handler.downloadAttachment)
}

type createRequest struct {
	WorkspaceID       string                 `json:"workspace_id"`
	Kind              domain.Kind            `json:"kind"`
	CollectionID      string                 `json:"collection_id"`
	CollectionVersion int64                  `json:"collection_version"`
	QueryHash         string                 `json:"query_hash"`
	Fields            []domain.Field         `json:"fields,omitempty"`
	RedactionPolicy   domain.RedactionPolicy `json:"redaction_policy,omitempty"`
	IncludeSensitive  bool                   `json:"include_sensitive,omitempty"`
	ExpiresInSeconds  *int64                 `json:"expires_in_seconds,omitempty"`
}

type createResponse struct {
	Job             jobResponse `json:"job"`
	Replayed        bool        `json:"replayed"`
	DispatchPending bool        `json:"dispatch_pending"`
}

type listResponse struct {
	WorkspaceID string        `json:"workspace_id"`
	Items       []jobResponse `json:"items"`
	NextCursor  *string       `json:"next_cursor,omitempty"`
}

type jobResponse struct {
	ID                string                 `json:"id"`
	WorkspaceID       string                 `json:"workspace_id"`
	Kind              domain.Kind            `json:"kind"`
	SchemaVersion     string                 `json:"schema_version"`
	CollectionID      *string                `json:"collection_id"`
	CollectionVersion *int64                 `json:"collection_version"`
	QueryHash         string                 `json:"query_hash"`
	Fields            []domain.Field         `json:"fields"`
	RedactionPolicy   domain.RedactionPolicy `json:"redaction_policy"`
	IncludeSensitive  bool                   `json:"include_sensitive"`
	Status            domain.Status          `json:"status"`
	Version           int64                  `json:"version"`
	ReadModelRevision *string                `json:"read_model_revision,omitempty"`
	ExactCount        *int64                 `json:"exact_count,omitempty"`
	FileHash          string                 `json:"file_hash,omitempty"`
	FileSize          int64                  `json:"file_size"`
	ErrorCode         string                 `json:"error_code,omitempty"`
	ErrorMessage      string                 `json:"error_message,omitempty"`
	AttemptCount      int                    `json:"attempt_count"`
	ExpiresAt         time.Time              `json:"expires_at"`
	CreatedAt         time.Time              `json:"created_at"`
	UpdatedAt         time.Time              `json:"updated_at"`
	StartedAt         *time.Time             `json:"started_at,omitempty"`
	CompletedAt       *time.Time             `json:"completed_at,omitempty"`
	DownloadCount     int                    `json:"download_count"`
	LastDownloadedAt  *time.Time             `json:"last_downloaded_at,omitempty"`
	DownloadURL       string                 `json:"download_url,omitempty"`
}

func (handler *Handler) create(writer http.ResponseWriter, request *http.Request) {
	if !handler.Available() {
		writeUnavailable(writer)
		return
	}
	input, err := decodeCreateRequest(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	workspaceID, err := parseID(input.WorkspaceID)
	if err != nil {
		writeError(writer, err)
		return
	}
	collectionID, err := parseID(input.CollectionID)
	if err != nil {
		writeError(writer, err)
		return
	}
	if input.Kind != domain.KindMarkdown && input.Kind != domain.KindMetadataJSON {
		writeError(writer, invalid("export kind must be MARKDOWN or METADATA_JSON"))
		return
	}
	key, err := parseIdempotencyKey(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	expiresIn := time.Duration(0)
	if input.ExpiresInSeconds != nil {
		if *input.ExpiresInSeconds < 1 || *input.ExpiresInSeconds > int64(exportapp.MaxTTL/time.Second) {
			writeError(writer, invalid("export expiry is invalid"))
			return
		}
		expiresIn = time.Duration(*input.ExpiresInSeconds) * time.Second
	}
	requestedBy := "local"
	permissionScope := string(capability.ReadLocal)
	if principal, ok := authhttp.PrincipalFromContext(request.Context()); ok {
		requestedBy = fmt.Sprintf("%s:%s", principal.Kind, principal.ID)
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	result, err := handler.service.Create(ctx, domain.CreateRequest{
		WorkspaceID: workspaceID, Kind: input.Kind, Scope: domain.Scope{Kind: domain.ScopeCollection, CollectionID: &collectionID, CollectionVersion: &input.CollectionVersion, QueryHash: input.QueryHash},
		Fields: input.Fields, Redaction: input.RedactionPolicy, IncludeSensitive: input.IncludeSensitive,
		IdempotencyKey: key, RequestedBy: requestedBy, PermissionScope: permissionScope, ExpiresIn: expiresIn,
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	if err := validateCreateResult(result.Job, workspaceID, collectionID, input); err != nil {
		writeError(writer, err)
		return
	}
	response := createResponse{Job: toJobResponse(result.Job), Replayed: result.Replayed, DispatchPending: result.DispatchPending}
	writer.Header().Set("Location", "/api/v1/exports/"+string(result.Job.ID)+"?workspace_id="+url.QueryEscape(string(result.Job.WorkspaceID)))
	writer.Header().Set("Cache-Control", "no-store")
	status := http.StatusAccepted
	if result.Replayed {
		status = http.StatusOK
	}
	httpapi.WriteJSON(writer, status, response)
}

func (handler *Handler) get(writer http.ResponseWriter, request *http.Request) {
	if !handler.Available() {
		writeUnavailable(writer)
		return
	}
	workspaceID, exportID, err := parseJobLookup(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	job, err := handler.service.GetForScope(ctx, workspaceID, exportID, domain.ScopeCollection)
	if err != nil {
		writeError(writer, err)
		return
	}
	if err := validateJobResult(job, workspaceID, exportID); err != nil {
		writeError(writer, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(writer, http.StatusOK, toJobResponse(job))
}

func (handler *Handler) list(writer http.ResponseWriter, request *http.Request) {
	if !handler.Available() {
		writeUnavailable(writer)
		return
	}
	workspaceID, err := parseID(chi.URLParam(request, "workspace_id"))
	if err != nil {
		writeError(writer, err)
		return
	}
	values, err := parseQuery(request, "collection_id", "limit", "cursor")
	if err != nil {
		writeError(writer, err)
		return
	}
	collectionID, err := parseID(values.Get("collection_id"))
	if err != nil {
		writeError(writer, err)
		return
	}
	limit := exportapp.DefaultListLimit
	if raw := values.Get("limit"); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 || parsed > exportapp.MaxListLimit {
			writeError(writer, invalid("export list limit is invalid"))
			return
		}
		limit = parsed
	}
	cursor := values.Get("cursor")
	if len(cursor) > maxCursorBytes {
		writeError(writer, invalid("export list cursor is invalid"))
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	page, err := handler.service.List(ctx, exportapp.ListQuery{WorkspaceID: workspaceID, ScopeKind: domain.ScopeCollection, CollectionID: &collectionID, Limit: limit, Cursor: cursor})
	if err != nil {
		writeError(writer, err)
		return
	}
	response := listResponse{WorkspaceID: string(workspaceID), Items: make([]jobResponse, 0, len(page.Items))}
	for _, job := range page.Items {
		if err := validateJobResult(job, workspaceID, job.ID); err != nil {
			writeError(writer, err)
			return
		}
		if job.Scope.CollectionID == nil || *job.Scope.CollectionID != collectionID {
			writeError(writer, invalidResult("export list returned a job outside the requested collection"))
			return
		}
		response.Items = append(response.Items, toJobResponse(job))
	}
	if len(page.NextCursor) > maxCursorBytes {
		writeError(writer, invalidResult("export list returned an invalid cursor"))
		return
	}
	if page.NextCursor != "" {
		response.NextCursor = &page.NextCursor
	}
	writer.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(writer, http.StatusOK, response)
}

func (handler *Handler) download(writer http.ResponseWriter, request *http.Request) {
	if !handler.Available() {
		writeUnavailable(writer)
		return
	}
	workspaceID, exportID, err := parseJobLookup(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	actor, err := downloadActor(request.Context())
	if err != nil {
		writeError(writer, err)
		return
	}
	job, content, err := handler.service.DownloadAsForScope(request.Context(), workspaceID, exportID, domain.ScopeCollection, actor)
	if err != nil {
		writeError(writer, err)
		return
	}
	if content == nil {
		writeError(writer, invalidResult("export download returned a nil result stream"))
		return
	}
	defer content.Close()
	if err := validateJobResult(job, workspaceID, exportID); err != nil || job.Status != domain.StatusSucceeded {
		if err == nil {
			err = invalidResult("export download returned an inconsistent result")
		}
		writeError(writer, err)
		return
	}
	extension, contentType := "json", "application/json"
	if job.Kind == domain.KindMarkdown {
		extension, contentType = "md", "text/markdown; charset=utf-8"
	}
	filename := fmt.Sprintf("collection-%s-%s.%s", dereferenceID(job.Scope.CollectionID), job.ID, extension)
	writer.Header().Set("Content-Type", contentType)
	writer.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	writer.Header().Set("Content-Length", strconv.FormatInt(job.FileSize, 10))
	writer.Header().Set("Cache-Control", "private, no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(http.StatusOK)
	_, _ = io.CopyN(writer, content, job.FileSize)
}

func decodeCreateRequest(request *http.Request) (createRequest, error) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return createRequest{}, foundation.NewError(foundation.ErrorInvalidInput, "UNSUPPORTED_MEDIA_TYPE", false, errors.New("request requires application/json"))
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxRequestBodyBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxRequestBodyBytes {
		return createRequest{}, foundation.NewError(foundation.ErrorInvalidInput, "INVALID_JSON", false, errors.New("request JSON is empty or oversized"))
	}
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = maxRequestBodyBytes
	decoded, err := strictjson.DecodeObject[createRequest](body, limits, nil)
	if err != nil {
		return createRequest{}, foundation.NewError(foundation.ErrorInvalidInput, "INVALID_JSON", false, err)
	}
	return decoded, nil
}

func parseJobLookup(request *http.Request) (foundation.ID, foundation.ID, error) {
	values, err := parseQuery(request, "workspace_id")
	if err != nil {
		return "", "", err
	}
	workspaceID, err := parseID(values.Get("workspace_id"))
	if err != nil {
		return "", "", err
	}
	exportID, err := parseID(chi.URLParam(request, "export_id"))
	return workspaceID, exportID, err
}

func parseQuery(request *http.Request, allowed ...string) (url.Values, error) {
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return nil, invalid("export query parameters are invalid")
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = struct{}{}
	}
	for key, entries := range values {
		if _, ok := allowedSet[key]; !ok || len(entries) != 1 || entries[0] == "" || entries[0] != strings.TrimSpace(entries[0]) {
			return nil, invalid("export query parameter is invalid")
		}
	}
	return values, nil
}

func parseIdempotencyKey(request *http.Request) (string, error) {
	values := request.Header.Values("Idempotency-Key")
	if len(values) != 1 || !utf8.ValidString(values[0]) || values[0] == "" || values[0] != strings.TrimSpace(values[0]) || len(values[0]) > maxIdempotencyBytes {
		return "", invalid("exactly one valid Idempotency-Key is required")
	}
	for _, character := range values[0] {
		if unicode.IsControl(character) {
			return "", invalid("Idempotency-Key is invalid")
		}
	}
	return values[0], nil
}

func parseID(raw string) (foundation.ID, error) {
	parsed, err := foundation.ParseID(raw)
	if err != nil || string(parsed) != raw {
		return "", invalid("export UUID is invalid")
	}
	return parsed, nil
}

func toJobResponse(job domain.Job) jobResponse {
	response := jobResponse{
		ID: string(job.ID), WorkspaceID: string(job.WorkspaceID), Kind: job.Kind, SchemaVersion: job.SchemaVersion,
		CollectionID: cloneIDString(job.Scope.CollectionID), CollectionVersion: cloneInt64(job.Scope.CollectionVersion), QueryHash: job.Scope.QueryHash,
		Fields: append([]domain.Field(nil), job.Fields...), RedactionPolicy: job.Redaction, IncludeSensitive: job.IncludeSensitive,
		Status: job.Status, Version: job.Version, FileHash: job.FileHash, FileSize: job.FileSize, ErrorCode: job.ErrorCode, ErrorMessage: job.ErrorMessage,
		AttemptCount: job.AttemptCount, ExpiresAt: job.ExpiresAt, CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt,
		StartedAt: cloneTime(job.StartedAt), CompletedAt: cloneTime(job.CompletedAt), DownloadCount: job.DownloadCount, LastDownloadedAt: cloneTime(job.LastDownloadedAt),
	}
	if job.PreparedAt != nil {
		response.ReadModelRevision = cloneString(job.ReadModelRevision)
		response.ExactCount = cloneInt64(job.ExactCount)
	}
	if job.Status == domain.StatusSucceeded {
		response.DownloadURL = "/api/v1/exports/" + string(job.ID) + "/download?workspace_id=" + url.QueryEscape(string(job.WorkspaceID))
	}
	return response
}

func cloneIDString(value *foundation.ID) *string {
	if value == nil {
		return nil
	}
	clone := string(*value)
	return &clone
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneString(value string) *string {
	clone := value
	return &clone
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	clone := value.UTC()
	return &clone
}

func dereferenceID(value *foundation.ID) string {
	if value == nil {
		return "export"
	}
	return string(*value)
}

func downloadActor(ctx context.Context) (exportapp.DownloadActor, error) {
	principal, ok := authhttp.PrincipalFromContext(ctx)
	if !ok {
		return exportapp.DownloadActor{Type: auditdomain.ActorAnonymous, Ref: "local"}, nil
	}
	return downloadActorForPrincipal(principal)
}

func downloadActorForPrincipal(principal authdomain.Principal) (exportapp.DownloadActor, error) {
	switch principal.Kind {
	case authdomain.PrincipalSession:
		return exportapp.DownloadActor{Type: auditdomain.ActorUser, Ref: string(principal.ID)}, nil
	case authdomain.PrincipalAPIToken:
		return exportapp.DownloadActor{Type: auditdomain.ActorAPIToken, Ref: string(principal.ID)}, nil
	default:
		return exportapp.DownloadActor{}, invalidResult("export download principal is invalid")
	}
}

func validateCreateResult(job domain.Job, workspaceID, collectionID foundation.ID, input createRequest) error {
	if err := validateJobResult(job, workspaceID, job.ID); err != nil {
		return err
	}
	if job.Kind != input.Kind || job.Scope.CollectionID == nil || *job.Scope.CollectionID != collectionID ||
		job.Scope.CollectionVersion == nil || *job.Scope.CollectionVersion != input.CollectionVersion || job.Scope.QueryHash != input.QueryHash ||
		job.IncludeSensitive != input.IncludeSensitive {
		return invalidResult("export create returned an inconsistent result")
	}
	return nil
}

func validateJobResult(job domain.Job, workspaceID, jobID foundation.ID) error {
	if job.ID != jobID || job.WorkspaceID != workspaceID {
		return invalidResult("export service crossed the requested identity binding")
	}
	if err := job.Validate(); err != nil {
		return invalidResult("export service returned an invalid job")
	}
	return nil
}

func invalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeInvalid, false, errors.New(message))
}

func invalidResult(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeResultInvalid, false, errors.New(message))
}

func writeUnavailable(writer http.ResponseWriter) {
	writeError(writer, foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, errors.New("export service is unavailable")))
}

func writeError(writer http.ResponseWriter, err error) {
	classified := &foundation.Error{}
	if !errors.As(err, &classified) {
		httpapi.WriteProblem(writer, http.StatusInternalServerError, "INTERNAL_ERROR", "导出请求处理失败", false, nil)
		return
	}
	status := httpapi.StatusForErrorKind(classified.Kind)
	if classified.Code == "UNSUPPORTED_MEDIA_TYPE" {
		status = http.StatusUnsupportedMediaType
	}
	if classified.Code == domain.ErrorCodeExpired {
		status = http.StatusGone
	}
	if classified.Code == domain.ErrorCodeResultInvalid {
		status = http.StatusInternalServerError
	}
	message := "导出请求处理失败"
	switch status {
	case http.StatusBadRequest:
		message = "导出请求无效"
	case http.StatusNotFound:
		message = "导出任务不存在"
	case http.StatusForbidden:
		message = "无权执行该导出"
	case http.StatusConflict:
		message = "导出任务状态冲突"
	case http.StatusGone:
		message = "导出结果已过期"
	case http.StatusServiceUnavailable:
		message = "导出服务暂不可用"
	}
	httpapi.WriteProblem(writer, status, classified.Code, message, classified.Retryable, nil)
}
